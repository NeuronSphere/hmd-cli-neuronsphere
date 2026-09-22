// Package runner executes deployment nodes.
//
// Every node is one RepoInstance run in a container against the local Floci.
// For a NeuronSphere repo class that is one script, one `hmd ... deploy`,
// inside hmd-img-projectbuilder: the Python that performs a deploy is an
// implementation detail of that image rather than a host prerequisite, which
// is what makes the rest of this port possible at all. For a foreign repo
// class (NERD009) it is the argv its manifest declares as ["exec", ...], in
// the image its manifest names, under a contract that assumes nothing of that
// image beyond its being an OCI image -- see dockerArgsForeign.
package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/artifact"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/repotree"
)

// CoreRepoClass is deployed by nothing: apply_changeset creates the core
// instance as a proper DEPLOY_NEXT instance with all its edges, and the runner
// marks it DEPLOYED without executing anything. It exists to be the producer
// every local resource-typed dependency resolves against.
const CoreRepoClass = "hmd-cli-neuronsphere"

// The protocol's status values live with the client that speaks it. Aliased
// here so the call sites below read as the runner's own vocabulary.
const (
	StatusDeployed = msdeploy.StatusDeployed
	StatusFailed   = msdeploy.StatusFailed
)

// deployCmd matches the generated `hmd ... deploy ...` command line.
var deployCmd = regexp.MustCompile(`^(\s*hmd\b[^|;&]*?\bdeploy)(\s|$)`)

// Docker runs docker subcommands.
type Docker interface {
	Run(ctx context.Context, args ...string) (stdout, stderr []byte, err error)
}

// EnvironmentLabel and InstanceLabel are set on every projectbuilder container,
// so a purge can find one an interrupted deploy left running.
const (
	EnvironmentLabel = "io.neuronsphere.nsctl.environment"
	InstanceLabel    = "io.neuronsphere.nsctl.instance"
)

// LocalArtifactLibrarianURL is the control plane's Artifact Librarian as seen
// from a container on the Docker network -- the in-network spelling of
// librarian.LocalBaseURL, which is the host's. The key is whatever non-empty
// string satisfies the client library's pre-flight; the local librarian does
// not check it.
const (
	LocalArtifactLibrarianURL    = "http://hmd_proxy/hmd_ms_artifact_lib/"
	LocalArtifactLibrarianAPIKey = "local-dummy"
)

// Config is everything a node's container needs to know.
type Config struct {
	// Network is the Docker network the container joins.
	Network string
	// Image is the projectbuilder to run, and the image a foreign node runs
	// in when its manifest names none.
	Image string
	// FlociEndpoint is the in-network Floci address, which becomes
	// AWS_ENDPOINT_URL.
	FlociEndpoint string
	// AccountID is the account selector. Floci reads the account straight off a
	// 12-digit access key, so an ambient AWS_ACCESS_KEY_ID would put every
	// environment's CDKTF state in the same account.
	AccountID string
	// DeploymentServiceURL is ms-deployment as seen *from* the container.
	// Host-side the local nginx is localhost; in-container it is hmd_proxy --
	// not `neuronsphere`, which is the Floci alias on :4566.
	DeploymentServiceURL string
	// LocalProxy is the container-reachable root a deploy tool joins its own
	// /hmd_ms_<svc>/... path onto. It carries the environment's route prefix,
	// because the services reached this way are the environment's own: without
	// it, hmd-cli-dbaccount posts to a control-plane path where ms-dbaccount is
	// not routed at all.
	LocalProxy string
	// K3sCluster scopes hmd-cli-helm's in-container image import to this
	// HMD_HOME's node rather than the unscoped default.
	K3sCluster string
	// K3sContainer is that node as a Docker container name. It is how a deploy
	// on the Docker network addresses a LoadBalancer or NodePort service inside
	// the cluster -- a Lambda's Redis or AMQP host, which in the cloud is an
	// external-dns hostname for an NLB and here is the node itself.
	K3sContainer string
	// Kubeconfig is the host path to mount, already rewritten for in-container
	// use.
	Kubeconfig string
	// RepoHome is where working trees are found.
	RepoHome string
	// Home is $HMD_HOME, where a tree the binary carries is materialised. It
	// has to be a host path the sibling projectbuilder resolves identically,
	// which is exactly what HMD_HOME already is.
	Home string
	// Lookup reads the environment, for the two variables that ask for a
	// checkout in preference to a bundled tree.
	Lookup func(string) string
	// RepoPaths overrides where a repo class's tree lives, for an instance an
	// environment manifest declares with an explicit source path -- which by
	// definition is not under RepoHome.
	RepoPaths map[string]string
	// Environment is the slug this deploy belongs to, or the control plane's
	// deployment id when it is the bootstrap.
	//
	// It exists to be a label. A projectbuilder container is started with
	// --rm and a Docker-generated name, so one left behind by an interrupted
	// deploy has nothing tying it to the environment whose purge should sweep
	// it -- and it holds the network open until someone finds it by hand.
	Environment string
	// DeploymentID, Region, CustomerCode name what the deploy produces.
	DeploymentID string
	Region       string
	CustomerCode string
	// Extra is passed through as additional environment variables.
	Extra map[string]string
	// WorkDir is where the overlay workspaces and generated scripts are
	// written. Empty means the system temp dir, which is right for the CLI.
	//
	// It matters when the runner is itself containerised: the paths below are
	// bind-mount sources resolved by the *host* daemon, not inside this
	// process's filesystem, so they have to name a directory that exists at the
	// same absolute path on both sides.
	WorkDir string
}

// Runner executes nodes and records what happened.
type Runner struct {
	Docker Docker
	Client *msdeploy.Client
	Config Config
	Out    io.Writer
	Err    io.Writer

	// LastFailure is the node that stopped the most recent Run, with its
	// output. Callers that can repair a specific failure need to see which one
	// it was.
	LastFailure *Result

	// Parallelism is how many independent nodes Run deploys at once; zero
	// means DefaultParallelism, one makes a run sequential again (what someone
	// bisecting a deploy that only misbehaves under concurrency needs).
	Parallelism int

	// LogDir, when set, is where a failed node's complete stdout and stderr
	// are written (<LogDir>/<instance>.log), since the report below keeps
	// only the tail and a provider crash names its cause well above it.
	LogDir string

	// Succeeded is the instance names the most recent Run settled successfully.
	// A partial run is normal -- the DAG stops at the first failure -- and a
	// reconcile snapshot must record exactly what landed: no more, so a failed
	// entry stays eligible for retry, and no less, so a succeeded one is not
	// redeployed every time.
	Succeeded []string
}

func (r *Runner) step(format string, a ...any) {
	if r.Out != nil {
		fmt.Fprintf(r.Out, format+"\n", a...)
	}
}

func (r *Runner) warn(format string, a ...any) {
	if r.Err != nil {
		fmt.Fprintf(r.Err, "warning: "+format+"\n", a...)
	}
}

// Result is one node's outcome.
type Result struct {
	Node   msdeploy.DeploymentNode
	Failed bool
	Stdout []byte
	Stderr []byte
	Err    error
}

// RunNode executes one node.
func (r *Runner) RunNode(ctx context.Context, node msdeploy.DeploymentNode) Result {
	// The core instance is a status flip, not a deploy: there is nothing to
	// run, and apply_changeset already created every edge it exists for.
	if node.RepoClassName == CoreRepoClass {
		r.step("  %s (core instance, nothing to deploy)", node.InstanceName)
		return Result{Node: node}
	}
	if strings.TrimSpace(node.Script) == "" {
		return Result{Node: node, Failed: true, Err: fmt.Errorf("the node carries no deploy script")}
	}

	repoPath, shared := r.repoPath(node.RepoClassName)
	if repoPath == "" {
		return Result{Node: node, Failed: true, Err: fmt.Errorf(
			"no tree for %s: the binary carries none, and there is none under %s. "+
				"Clone it there, or declare an explicit source path in the environment manifest",
			node.RepoClassName, r.Config.RepoHome)}
	}

	// A cached tree -- bundled or unpacked from an artifact -- is shared by
	// every environment on this HMD_HOME and vouched for by the token in its
	// directory name, so it is never the thing a deploy writes into.
	workspace, cmd, cleanup, err := r.prepareWorkspace(repoPath, node.Script, shared)
	if err != nil {
		return Result{Node: node, Failed: true, Err: err}
	}
	defer cleanup()

	var args []string
	if cmd.Argv != nil {
		// A foreign node: no script to write, and the configuration the
		// generated script would have carried is lifted out of it instead.
		args = r.dockerArgsForeign(node, workspace, cmd, r.foreignConfig(ctx, node))
		r.step("  deploying %s (%s@%s) in %s: %s...", node.InstanceName, node.RepoClassName, node.Version,
			orDefault(cmd.Image, r.Config.Image), strings.Join(cmd.Argv, " "))
	} else {
		scriptFile, err := os.CreateTemp(r.Config.WorkDir, "nsctl-deploy-*.sh")
		if err != nil {
			return Result{Node: node, Failed: true, Err: fmt.Errorf("writing the deploy script: %w", err)}
		}
		scriptPath := scriptFile.Name()
		defer os.Remove(scriptPath)
		if _, err := scriptFile.WriteString(cmd.Script); err != nil {
			scriptFile.Close()
			return Result{Node: node, Failed: true, Err: fmt.Errorf("writing the deploy script: %w", err)}
		}
		scriptFile.Close()

		args = r.dockerArgs(node, workspace, scriptPath, cmd.Override)
		r.step("  deploying %s (%s@%s)...", node.InstanceName, node.RepoClassName, node.Version)
	}

	stdout, stderr, err := r.Docker.Run(ctx, args...)
	if err != nil {
		return Result{Node: node, Failed: true, Stdout: stdout, Stderr: stderr, Err: err}
	}

	if n := r.submitProducedResources(ctx, workspace, node); n > 0 {
		r.step("    submitted %d produced resource(s)", n)
	}
	return Result{Node: node, Stdout: stdout, Stderr: stderr}
}

// dockerArgs builds the projectbuilder invocation.
func (r *Runner) dockerArgs(node msdeploy.DeploymentNode, workspace, scriptPath string, override bool) []string {
	// A seeded node carries its resolved configuration only inside the
	// generated deploy script. `hmd deploy` reads it from there, but a
	// src/local/deploy_local.sh override replaces that script and reads
	// HMD_INSTANCE_CONFIG instead -- which was "{}" for every such node, so a
	// script keyed on its configuration (hmd-inf-neptune's graph_host)
	// silently fell back to its defaults. Lift it out of the script for
	// those, as the foreign-node path does. Only for those: the resolved
	// configuration of a node with many dependencies runs past the exec
	// argument limit as an environment variable ("argument list too long"),
	// and a generated script never reads the variable.
	instanceConfig := node.InstanceConfiguration
	if instanceConfig == nil && override {
		if extracted, ok := ExtractConfig(node.Script); ok {
			instanceConfig = extracted
		}
	}
	config, _ := json.Marshal(instanceConfig)
	if instanceConfig == nil {
		config = []byte("{}")
	}

	env := map[string]string{
		"AWS_ENDPOINT_URL": r.Config.FlociEndpoint,
		// The account selector -- see Config.AccountID.
		"AWS_ACCESS_KEY_ID":     r.Config.AccountID,
		"AWS_SECRET_ACCESS_KEY": "dummykey",
		"AWS_DEFAULT_REGION":    orDefault(r.Config.Extra["AWS_REGION"], "us-west-2"),
		"HMD_ENVIRONMENT":       "local",
		// hmd-cli-* asserts HMD_HOME is set; the deploy only needs scratch space
		// there.
		"HMD_HOME": "/root/hmd",
		// `hmd cdktf deploy` unconditionally resolves registry credentials.
		// Locally these feed a no-op overlay, so dummy values satisfy the
		// pre-flight without configuring a real registry.
		"DOCKER_USERNAME":            "local",
		"DOCKER_PASSWORD":            "local",
		"HMD_DEPLOYMENT_SERVICE_URL": r.Config.DeploymentServiceURL,
		"NS_LOCAL_PROXY":             r.Config.LocalProxy,
		"HMD_LOCAL_K3S_CLUSTER_NAME": r.Config.K3sCluster,
		"HMD_LOCAL_K3S_CONTAINER":    r.Config.K3sContainer,
		// The control plane's Artifact Librarian, as a Lambda or a pod on the
		// network reaches it. A service whose cloud stack pulls artifacts
		// (transform configs, content-item types) otherwise falls back to the
		// library's cloud default URL and fails on the first pull.
		"HMD_ARTIFACT_LIBRARIAN_URL":     LocalArtifactLibrarianURL,
		"HMD_ARTIFACT_LIBRARIAN_API_KEY": LocalArtifactLibrarianAPIKey,
		"HMD_CUSTOMER_CODE":              orDefault(r.Config.CustomerCode, "none"),
		"HMD_DID":                        r.Config.DeploymentID,
		"HMD_REGION":                     orDefault(r.Config.Region, "reg1"),
		"HMD_HOSTNAME":                   "localhost",
		// The node's own identity and resolved configuration. `hmd deploy` gets
		// these on its command line, but a deploy_local.sh override replaces
		// that command entirely -- without them it cannot derive the resource
		// identifier make_standard_name produces, nor read the values the CLI
		// resolved for it.
		"HMD_INSTANCE_NAME":   node.InstanceName,
		"HMD_REPO_NAME":       node.RepoClassName,
		"HMD_REPO_VERSION":    node.Version,
		"HMD_INSTANCE_CONFIG": string(config),
	}
	for k, v := range r.Config.Extra {
		if v != "" {
			env[k] = v
		}
	}

	args := []string{
		"run", "--rm",
		// Override the projectbuilder ENTRYPOINT (`hmd`) so the script runs
		// under bash rather than being parsed as an `hmd` subcommand.
		"--entrypoint", "bash",
		"--network", r.Config.Network,
		// The only handle a purge has on a container an interrupted deploy left
		// behind: --rm means these exist only when a run did not finish, and
		// the name Docker gave it says nothing about which environment it
		// belongs to.
		"--label", EnvironmentLabel + "=" + r.Config.Environment,
		"--label", InstanceLabel + "=" + node.InstanceName,
	}
	for _, key := range sortedKeys(env) {
		args = append(args, "-e", key+"="+env[key])
	}
	args = append(args, "-v", "/var/run/docker.sock:/var/run/docker.sock")
	if r.Config.Kubeconfig != "" {
		args = append(args,
			"-v", r.Config.Kubeconfig+":/root/.kube/config:ro",
			"-e", "HMD_LOCAL_K3S_KUBECONFIG=/root/.kube/config",
		)
	}
	args = append(args,
		"-v", workspace+":/workspace", "-w", "/workspace",
		// The script is mounted rather than passed as `bash -c`: a node with
		// many resolved dependencies generates a script whose inlined config
		// exceeds ARG_MAX and fails before it runs with "argument list too
		// long".
		"-v", scriptPath+":/tmp/hmd-deploy-node.sh:ro",
		r.Config.Image, "/tmp/hmd-deploy-node.sh",
	)
	return args
}

// ForeignKubeconfigPath is where a foreign node finds the cluster's kubeconfig,
// named by KUBECONFIG. A neutral path rather than /root/.kube/config, because
// the image's user need not be root.
const ForeignKubeconfigPath = "/etc/nsctl/kubeconfig"

// foreignEnv is the environment a foreign node receives, and it is the whole
// of NERD009 SPEC005's contract: nothing here exists to satisfy hmd-cli-*,
// because there is no hmd in the container to assert on it. No HMD_HOME, no
// dummy registry credentials, no service URLs a NeuronSphere tool would
// resolve, and none of Config.Extra -- the variables hmd.env adds for the
// native tools are not part of what a stranger's command is promised.
//
// dockerArgsForeign consumes this map and nothing else for `-e`, so the test
// that pins the contract asserts against the map the command line is built
// from rather than a copy of it.
func (r *Runner) foreignEnv(node msdeploy.DeploymentNode, config []byte) map[string]string {
	env := map[string]string{
		"AWS_ENDPOINT_URL": r.Config.FlociEndpoint,
		// The account selector -- see Config.AccountID.
		"AWS_ACCESS_KEY_ID":     r.Config.AccountID,
		"AWS_SECRET_ACCESS_KEY": "dummykey",
		"AWS_DEFAULT_REGION":    orDefault(r.Config.Extra["AWS_REGION"], "us-west-2"),
		"HMD_INSTANCE_NAME":     node.InstanceName,
		"HMD_REPO_NAME":         node.RepoClassName,
		"HMD_REPO_VERSION":      node.Version,
		"HMD_INSTANCE_CONFIG":   string(config),
		"HMD_DID":               r.Config.DeploymentID,
		"HMD_ENVIRONMENT":       "local",
		"HMD_CUSTOMER_CODE":     orDefault(r.Config.CustomerCode, "none"),
		// Scopes an image import into this HMD_HOME's node, for a toolset
		// that knows to do one.
		"HMD_LOCAL_K3S_CLUSTER_NAME": r.Config.K3sCluster,
	}
	if r.Config.Kubeconfig != "" {
		env["KUBECONFIG"] = ForeignKubeconfigPath
	}
	return env
}

// dockerArgsForeign builds a foreign node's invocation (NERD009 SPEC005).
//
// What it does not do, said out loud because each is a thing dockerArgs does:
// no --entrypoint override, since the image may have no bash and an
// entrypoint of its own that matters; no script written and mounted, since an
// exec argv is a handful of words and carries its configuration in the
// environment; no /root path, since the image's user need not be root. The
// argv follows the image verbatim.
func (r *Runner) dockerArgsForeign(node msdeploy.DeploymentNode, workspace string, cmd nodeCommand, config []byte) []string {
	env := r.foreignEnv(node, config)
	args := []string{
		"run", "--rm",
		"--network", r.Config.Network,
		// The purge's only handle on a container an interrupted deploy left
		// behind; see dockerArgs.
		"--label", EnvironmentLabel + "=" + r.Config.Environment,
		"--label", InstanceLabel + "=" + node.InstanceName,
	}
	for _, key := range sortedKeys(env) {
		args = append(args, "-e", key+"="+env[key])
	}
	// The daemon is a sibling: every bind mount a toolset asks of it resolves
	// on the host, not inside this container.
	args = append(args, "-v", "/var/run/docker.sock:/var/run/docker.sock")
	if r.Config.Kubeconfig != "" {
		args = append(args, "-v", r.Config.Kubeconfig+":"+ForeignKubeconfigPath+":ro")
	}
	args = append(args, "-v", workspace+":/workspace", "-w", "/workspace")
	args = append(args, orDefault(cmd.Image, r.Config.Image))
	return append(args, cmd.Argv...)
}

// Localize makes a generated deploy script deploy from the mounted source.
//
// The generated script runs a bare `hmd ... deploy`, which makes hmd-cli-deploy
// pull the build bundle from the Artifact Librarian -- absent locally.
// Inserting --local after the deploy subcommand makes it deploy from the
// mounted /workspace instead. Only the command line is transformed, once;
// export, heredoc and config lines are untouched, and it is a no-op when
// --local is already there.
func Localize(script string) string {
	if script == "" {
		return script
	}
	lines := strings.Split(script, "\n")
	for i, line := range lines {
		m := deployCmd.FindStringSubmatchIndex(line)
		if m == nil {
			continue
		}
		if strings.Contains(line, "--local") {
			return script // already localized
		}
		lines[i] = line[:m[3]] + " --local" + line[m[3]:]
		break
	}
	return strings.Join(lines, "\n")
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// sortedKeys gives the container a deterministic environment, so two runs of
// the same node produce the same command line.
func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// repoPath is the host directory to mount for a repo class, and whether that
// directory is a cache this HMD_HOME shares rather than a tree its owner owns.
//
// The precedence is repoclass.ResolveVersion's, for the reason its comment
// gives: reading a version from one tree and deploying another describes a
// build that never existed. An explicitly declared source path wins, then a
// checkout the developer asked for, then the tree the binary carries, then
// whatever checkout is under $HMD_REPO_HOME. An artifact-sourced instance
// arrives through RepoPaths, seeded by whoever read the manifest.
//
// It must be a host path either way. projectbuilder is launched as a sibling
// through the Docker socket, so the daemon resolves every bind mount on the
// host: a tree that exists only inside this binary is not mountable, which is
// why a bundled one is materialised under $HMD_HOME first.
func (r *Runner) repoPath(repoClass string) (path string, shared bool) {
	if repoClass == "" {
		return "", false
	}
	if override := r.Config.RepoPaths[repoClass]; override != "" {
		if info, err := os.Stat(override); err == nil && info.IsDir() {
			// An override is usually a checkout, and then it is the developer's
			// to write into. An unpacked artifact arrives the same way and is
			// not: it is keyed by version, shared by every environment here, and
			// a deploy writing meta-data/resources_output/ into it would hand
			// the next environment this one's resources.
			return override, sharedTree(r.Config.Home, override)
		}
		return "", false
	}

	tree := ""
	if r.Config.RepoHome != "" {
		candidate := filepath.Join(r.Config.RepoHome, repoClass)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			tree = candidate
		}
	}
	if tree != "" && r.preferLocal(repoClass) {
		return tree, false
	}
	if dir := repotree.Dir(r.Config.Home, repoClass); dir != "" {
		return dir, true
	}
	return tree, false
}

// sharedTree reports whether a path is one of the caches under $HMD_HOME rather
// than a tree on disk that somebody owns.
//
// Asked of the path rather than tracked alongside it, because the property
// belongs to where the tree lives: every cache root is token-addressed, so a
// third one added later is covered by having put it in the same place.
func sharedTree(home, path string) bool {
	if home == "" || path == "" {
		return false
	}
	for _, root := range []string{repotree.Root(home), artifact.Root(home)} {
		if path == root || strings.HasPrefix(path, root+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

// preferLocal mirrors repoclass's: the developer asking for their own checkout,
// per repo class or across the board.
func (r *Runner) preferLocal(repoClass string) bool {
	if r.Config.Lookup == nil {
		return false
	}
	pin := strings.TrimSpace(r.Config.Lookup("HMD_LOCAL_VERSION_" +
		strings.ToUpper(strings.ReplaceAll(repoClass, "-", "_"))))
	if strings.EqualFold(pin, "local") {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(r.Config.Lookup("HMD_LOCAL_NEURONSPHERE_PREFER_LOCAL_VERSIONS"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// printFailure puts the failed node's output under the node that failed.
// ReportFailure prints a failed node's diagnosis. Exported so a caller
// running nodes one at a time through RunNode -- the bootstrap DAG does --
// reports a failure the same way Run does, rather than saying only that
// something failed.
func (r *Runner) ReportFailure(res Result) { r.printFailure(res) }

func (r *Runner) printFailure(res Result) {
	if r.Err == nil {
		return
	}
	fmt.Fprintf(r.Err, "\n%s failed: %v\n", res.Node.InstanceName, res.Err)
	if out := strings.TrimSpace(string(res.Stdout)); out != "" {
		fmt.Fprintf(r.Err, "--- stdout ---\n%s\n", tail(out, 60))
	}
	if out := strings.TrimSpace(string(res.Stderr)); out != "" {
		fmt.Fprintf(r.Err, "--- stderr ---\n%s\n", tail(out, 60))
	}
	if r.LogDir != "" {
		if path, err := writeNodeLog(r.LogDir, res); err == nil {
			fmt.Fprintf(r.Err, "full output: %s\n", path)
		}
	}
}

// writeNodeLog saves a failed node's complete output under dir.
func writeNodeLog(dir string, res Result) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, res.Node.InstanceName+".log")
	var b strings.Builder
	fmt.Fprintf(&b, "%s failed: %v\n\n--- stdout ---\n%s\n\n--- stderr ---\n%s\n",
		res.Node.InstanceName, res.Err, res.Stdout, res.Stderr)
	return path, os.WriteFile(path, []byte(b.String()), 0o644)
}

// tail keeps the last n lines, which is where a deploy says why it failed.
func tail(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return "... (" + fmt.Sprint(len(lines)-n) + " earlier lines omitted)\n" + strings.Join(lines[len(lines)-n:], "\n")
}

func (r *Runner) SetNodeStatus(ctx context.Context, node msdeploy.DeploymentNode, status string) {
	if r.Client == nil || node.RIDNid == "" {
		return
	}
	// Best effort with a short budget: a status that did not land is a
	// reporting gap, not a reason to fail a deploy that worked.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := r.Client.APIOpRaw(ctx, "set_deployment_status/"+node.RIDNid+"/"+status, nil); err != nil {
		r.warn("could not record %s as %s: %v", node.InstanceName, status, err)
	}
}

// ProjectBuilderDefaultRegistry and ProjectBuilderDefaultVersion are the image
// every deploy node runs in, when nothing overrides it.
//
// hmdlabs, not neuronsphere: neuronsphere carried only the released subset and
// has no projectbuilder at all, so the old default resolved nowhere and every
// fresh install failed on the bootstrap's first node. An exact patch rather
// than a floating tag because hmdlabs publishes no `stable` -- the tag the old
// default asked for has never existed under either org -- and because "which
// projectbuilder was this built against" should have an answer.
//
// 0.5.389 is the first build that carries its Apache 2.0 licence label; like
// 0.5.388 before it, it has hmd-lib-cdktf's 2026-09-08 path-style-S3 fix,
// which is what an environment not named `local` needs in order to deploy.
const (
	ProjectBuilderDefaultRegistry = "ghcr.io/hmdlabs"
	ProjectBuilderDefaultVersion  = "0.5.389"
)

// ProjectBuilderRef resolves the projectbuilder image from the environment.
//
// One implementation for all three callers -- the control plane, an
// environment apply and the runner service. They had drifted into three copies
// of the same eight lines, which is three places to miss when a default moves,
// and the default has now moved.
func ProjectBuilderRef(lookup func(string) string) string {
	if lookup == nil {
		lookup = func(string) string { return "" }
	}
	host := lookup("HMD_LOCAL_NS_CONTAINER_REGISTRY")
	if host == "" {
		host = ProjectBuilderDefaultRegistry
	}
	version := lookup("HMD_PROJECTBUILDER_VERSION")
	if version == "" {
		version = ProjectBuilderDefaultVersion
	}
	return host + "/hmd-img-projectbuilder:" + version
}
