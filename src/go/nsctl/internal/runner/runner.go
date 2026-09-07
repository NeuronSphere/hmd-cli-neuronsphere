// Package runner executes deployment nodes.
//
// Every node is one RepoInstance, one script, one `hmd ... deploy` -- run
// inside an hmd-img-projectbuilder container against the local Floci. The
// Python that performs a deploy is therefore an implementation detail of that
// image rather than a host prerequisite, which is what makes the rest of this
// port possible at all.
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

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
)

// CoreRepoClass is deployed by nothing: apply_changeset creates the core
// instance as a proper DEPLOY_NEXT instance with all its edges, and the runner
// marks it DEPLOYED without executing anything. It exists to be the producer
// every local resource-typed dependency resolves against.
const CoreRepoClass = "hmd-cli-neuronsphere"

// The protocol's status values live with the client that speaks it. Aliased
// here so the call sites below read as the runner's own vocabulary.
const (
	StatusDeployed     = msdeploy.StatusDeployed
	StatusFailed       = msdeploy.StatusFailed
	CSDStatusCompleted = msdeploy.CSDStatusCompleted
	CSDStatusFailed    = msdeploy.CSDStatusFailed
)

// deployCmd matches the generated `hmd ... deploy ...` command line.
var deployCmd = regexp.MustCompile(`^(\s*hmd\b[^|;&]*?\bdeploy)(\s|$)`)

// Docker runs docker subcommands.
type Docker interface {
	Run(ctx context.Context, args ...string) (stdout, stderr []byte, err error)
}

// Config is everything a node's container needs to know.
type Config struct {
	// Network is the Docker network the container joins.
	Network string
	// Image is the projectbuilder to run.
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
	// Kubeconfig is the host path to mount, already rewritten for in-container
	// use.
	Kubeconfig string
	// RepoHome is where working trees are found.
	RepoHome string
	// RepoPaths overrides where a repo class's tree lives, for an instance an
	// environment manifest declares with an explicit source path -- which by
	// definition is not under RepoHome.
	RepoPaths map[string]string
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

// Run executes the nodes in order, stopping at the first failure.
//
// Stopping is deliberate: the nodes arrive in dependency order, so everything
// after a failure depends on it and would fail in a way that says less.
func (r *Runner) Run(ctx context.Context, csdNid string, nodes []msdeploy.DeploymentNode) error {
	r.Succeeded = nil
	r.LastFailure = nil
	for _, node := range nodes {
		res := r.RunNode(ctx, node)
		if res.Failed {
			failure := res
			r.LastFailure = &failure
			r.SetNodeStatus(ctx, node, StatusFailed)
			r.SetCSDStatus(ctx, csdNid, CSDStatusFailed)
			r.printFailure(res)
			return fmt.Errorf("deploying %s failed", node.InstanceName)
		}
		r.Succeeded = append(r.Succeeded, node.InstanceName)
		r.SetNodeStatus(ctx, node, StatusDeployed)
	}
	r.SetCSDStatus(ctx, csdNid, CSDStatusCompleted)
	return nil
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

	repoPath := r.repoPath(node.RepoClassName)
	if repoPath == "" {
		// There is no bundled-in-image fallback for a repo's own deploy source,
		// unlike the best-effort sibling mounts elsewhere, so this is fatal and
		// says what to do about it.
		return Result{Node: node, Failed: true, Err: fmt.Errorf(
			"no working tree for %s under %s; clone it there, or declare an explicit source path in the environment manifest",
			node.RepoClassName, r.Config.RepoHome)}
	}

	workspace, script, cleanup, err := r.prepareWorkspace(repoPath, node.Script)
	if err != nil {
		return Result{Node: node, Failed: true, Err: err}
	}
	defer cleanup()

	scriptFile, err := os.CreateTemp(r.Config.WorkDir, "nsctl-deploy-*.sh")
	if err != nil {
		return Result{Node: node, Failed: true, Err: fmt.Errorf("writing the deploy script: %w", err)}
	}
	scriptPath := scriptFile.Name()
	defer os.Remove(scriptPath)
	if _, err := scriptFile.WriteString(script); err != nil {
		scriptFile.Close()
		return Result{Node: node, Failed: true, Err: fmt.Errorf("writing the deploy script: %w", err)}
	}
	scriptFile.Close()

	args := r.dockerArgs(node, workspace, scriptPath)
	r.step("  deploying %s (%s@%s)...", node.InstanceName, node.RepoClassName, node.Version)

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
func (r *Runner) dockerArgs(node msdeploy.DeploymentNode, workspace, scriptPath string) []string {
	config, _ := json.Marshal(node.InstanceConfiguration)
	if node.InstanceConfiguration == nil {
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
		"HMD_CUSTOMER_CODE":          orDefault(r.Config.CustomerCode, "none"),
		"HMD_DID":                    r.Config.DeploymentID,
		"HMD_REGION":                 orDefault(r.Config.Region, "reg1"),
		"HMD_HOSTNAME":               "localhost",
		// The node's own identity and resolved configuration. `hmd deploy` gets
		// these on its command line, but a deploy_local.sh override replaces
		// that command entirely -- without them it cannot derive the resource
		// identifier make_standard_name produces, nor read the values the CLI
		// resolved for it.
		"HMD_INSTANCE_NAME":   node.InstanceName,
		"HMD_REPO_NAME":       node.RepoClassName,
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

func (r *Runner) repoPath(repoClass string) string {
	if repoClass == "" {
		return ""
	}
	if override := r.Config.RepoPaths[repoClass]; override != "" {
		if info, err := os.Stat(override); err == nil && info.IsDir() {
			return override
		}
		return ""
	}
	if r.Config.RepoHome == "" {
		return ""
	}
	dir := filepath.Join(r.Config.RepoHome, repoClass)
	if info, err := os.Stat(dir); err == nil && info.IsDir() {
		return dir
	}
	return ""
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

func (r *Runner) SetCSDStatus(ctx context.Context, csdNid, status string) {
	if r.Client == nil || csdNid == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := r.Client.APIOpRaw(ctx, "set_change_set_deployment_status/"+csdNid+"/"+status, nil); err != nil {
		r.warn("could not record the changeset deployment as %s: %v", status, err)
	}
}
