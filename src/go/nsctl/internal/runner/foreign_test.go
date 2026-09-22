package runner

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
)

// foreignRepo is a repo class that deploys with its own toolset: an exec
// command in its manifest and, optionally, the image to run it in.
func foreignRepo(t *testing.T, image string, extra ...string) string {
	t.Helper()
	repo := t.TempDir()
	manifest := `{"name":"acme-api","description":"x","build":{"mechanism":"external"},` +
		`"deploy":{"commands":[["exec","make","deploy"]]`
	if image != "" {
		manifest += `,"image":"` + image + `"`
	}
	manifest += `}}`
	mustWrite(t, filepath.Join(repo, "meta-data", "manifest.json"), manifest)
	mustWrite(t, filepath.Join(repo, "meta-data", "VERSION"), "0.3")
	for i := 0; i+1 < len(extra); i += 2 {
		mustWrite(t, filepath.Join(repo, extra[i]), extra[i+1])
	}
	return repo
}

// SPEC003: a repo that has written down its deploy command has said something
// more specific than a conventional filename, so exec outranks deploy_local.sh
// -- and the generated script, which is discarded exactly as it is for a
// deploy_local.sh node.
func TestExecOutranksDeployLocalAndTheGeneratedScript(t *testing.T) {
	t.Parallel()

	repo := foreignRepo(t, "", "src/local/deploy_local.sh", "echo hi")
	r := testRunner(t, &fakeDocker{}, "")
	workspace, cmd, cleanup, err := r.prepareWorkspace(repo, "hmd deploy", false)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	if strings.Join(cmd.Argv, " ") != "make deploy" {
		t.Errorf("argv = %q, want the exec command", cmd.Argv)
	}
	if cmd.Script != "" {
		t.Errorf("a script survived: %q", cmd.Script)
	}
	// Nothing in src/local applies to a foreign node, so there is no overlay
	// and the tree is mounted as it is.
	if workspace != repo {
		t.Errorf("workspace = %q, want the repo itself", workspace)
	}
}

// A shared tree -- bundled or an unpacked artifact -- is still copied first,
// for the same reason as any other node: the workspace is writable and outputs
// must not land in a cache every environment reads.
func TestAForeignNodeOnASharedTreeIsIsolated(t *testing.T) {
	t.Parallel()

	repo := foreignRepo(t, "")
	r := testRunner(t, &fakeDocker{}, "")
	workspace, cmd, cleanup, err := r.prepareWorkspace(repo, "hmd deploy", true)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if workspace == repo {
		t.Error("the shared tree was mounted directly")
	}
	if cmd.Argv == nil {
		t.Error("the isolated copy lost its exec command")
	}
	if _, err := os.Stat(filepath.Join(workspace, "meta-data", "manifest.json")); err != nil {
		t.Error("the copy has no manifest")
	}
}

// Two exec entries are refused rather than half-run.
func TestTwoExecEntriesAreRefused(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	mustWrite(t, filepath.Join(repo, "meta-data", "manifest.json"),
		`{"name":"x","deploy":{"commands":[["exec","make","a"],["exec","make","b"]]}}`)
	r := testRunner(t, &fakeDocker{}, "")
	if _, _, _, err := r.prepareWorkspace(repo, "hmd deploy", false); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Errorf("err = %v, want a refusal", err)
	}
}

// A malformed manifest fails the node rather than silently deploying it as a
// NeuronSphere repo.
func TestAnUnreadableManifestFailsTheNode(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	mustWrite(t, filepath.Join(repo, "meta-data", "manifest.json"), `{not json`)
	r := testRunner(t, &fakeDocker{}, "")
	if _, _, _, err := r.prepareWorkspace(repo, "hmd deploy", false); err == nil {
		t.Error("a malformed manifest was tolerated")
	}
}

// A repo with no exec entry is exactly what it was: the generated script,
// localized.
func TestNativeNodesAreUnchanged(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	mustWrite(t, filepath.Join(repo, "meta-data", "manifest.json"), `{"name":"x","deploy":{"commands":[["helm"]]}}`)
	r := testRunner(t, &fakeDocker{}, "")
	_, cmd, cleanup, err := r.prepareWorkspace(repo, "hmd deploy", false)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if cmd.Argv != nil || cmd.Script != "hmd deploy --local" {
		t.Errorf("cmd = %+v", cmd)
	}
}

// spec005 is the injected environment NERD009 SPEC005 lists, and nothing else.
// KUBECONFIG joins it when the environment has a cluster.
var spec005 = []string{
	"AWS_ENDPOINT_URL", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_DEFAULT_REGION",
	"HMD_INSTANCE_NAME", "HMD_REPO_NAME", "HMD_REPO_VERSION", "HMD_INSTANCE_CONFIG",
	"HMD_DID", "HMD_ENVIRONMENT", "HMD_CUSTOMER_CODE", "HMD_LOCAL_K3S_CLUSTER_NAME",
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// realKubeconfig writes a kubeconfig that actually exists, because the mount
// guard tests the file rather than the field: a path that is not there is not
// mounted, so a synthetic one would assert the wrong branch.
func realKubeconfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, []byte("apiVersion: v1\nkind: Config\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The contract, asserted against the map dockerArgsForeign consumes rather
// than a copy of it: a variable added to the map fails here until the spec
// list agrees, and a variable added to the command line without going through
// the map fails the second half.
func TestForeignEnvIsExactlySPEC005(t *testing.T) {
	t.Parallel()

	node := msdeploy.DeploymentNode{InstanceName: "api", RepoClassName: "acme-api", Version: "0.3"}
	kubeconfig := realKubeconfig(t)
	for _, tt := range []struct {
		name       string
		kubeconfig string
		want       []string
	}{
		{"without a cluster", "", spec005},
		{"with a cluster", kubeconfig, append(append([]string(nil), spec005...), "KUBECONFIG")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := testRunner(t, &fakeDocker{}, "")
			r.Config.Kubeconfig = tt.kubeconfig
			// Extra is what hmd.env adds for hmd-cli-*; it is not part of the
			// contract and must not leak in.
			r.Config.Extra = map[string]string{"AWS_REGION": "us-east-1", "HMD_LOCAL_NS_CONTAINER_REGISTRY": "x"}

			env := r.foreignEnv(node, []byte(`{"a":1}`))
			got := sortedKeys(env)
			if strings.Join(got, ",") != strings.Join(sortedCopy(tt.want), ",") {
				t.Errorf("injected variables\n got %v\nwant %v", got, sortedCopy(tt.want))
			}

			args := r.dockerArgsForeign(node, "/ws", nodeCommand{Argv: []string{"make", "deploy"}}, []byte(`{"a":1}`))
			var fromArgs []string
			for _, kv := range envFlags(args) {
				k, v, _ := strings.Cut(kv, "=")
				fromArgs = append(fromArgs, k)
				if env[k] != v {
					t.Errorf("%s: command line carries %q, map carries %q", k, v, env[k])
				}
			}
			if strings.Join(sortedCopy(fromArgs), ",") != strings.Join(got, ",") {
				t.Errorf("command line variables %v differ from the map %v", sortedCopy(fromArgs), got)
			}
			if env["AWS_DEFAULT_REGION"] != "us-east-1" || env["HMD_REPO_VERSION"] != "0.3" || env["HMD_INSTANCE_CONFIG"] != `{"a":1}` {
				t.Errorf("values: %v", env)
			}
			if tt.kubeconfig != "" && env["KUBECONFIG"] != ForeignKubeconfigPath {
				t.Errorf("KUBECONFIG = %q", env["KUBECONFIG"])
			}
		})
	}
}

// What SPEC005 drops: the entrypoint override, the mounted script, /root
// paths, HMD_HOME and the dummy registry credentials. A foreign image may have
// no bash, an entrypoint of its own, and no root user.
func TestForeignInvocationAssumesOnlyAnOCIImage(t *testing.T) {
	t.Parallel()

	r := testRunner(t, &fakeDocker{}, "")
	r.Config.Kubeconfig = realKubeconfig(t)
	node := msdeploy.DeploymentNode{InstanceName: "api", RepoClassName: "acme-api", Version: "0.3"}
	args := r.dockerArgsForeign(node, "/ws", nodeCommand{Argv: []string{"sh", "-c", "make deploy"}, Image: "ghcr.io/acme/ci:3.2"}, []byte("{}"))
	joined := strings.Join(args, "\x00")

	for _, forbidden := range []string{"--entrypoint", "/tmp/hmd-deploy-node.sh", "/root", "HMD_HOME=", "DOCKER_USERNAME", "DOCKER_PASSWORD", "NS_LOCAL_PROXY", "HMD_DEPLOYMENT_SERVICE_URL"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("the invocation carries %q:\n%q", forbidden, args)
		}
	}
	// The argv follows the image verbatim, so the image's own entrypoint --
	// if it has one -- receives it.
	tail := args[len(args)-4:]
	if strings.Join(tail, "\x00") != strings.Join([]string{"ghcr.io/acme/ci:3.2", "sh", "-c", "make deploy"}, "\x00") {
		t.Errorf("tail = %q", tail)
	}
	for _, want := range []string{
		"-v\x00/ws:/workspace", "-w\x00/workspace",
		"-v\x00/var/run/docker.sock:/var/run/docker.sock",
		"-v\x00" + r.Config.Kubeconfig + ":" + ForeignKubeconfigPath + ":ro",
		"--network\x00net",
		"--label\x00" + EnvironmentLabel + "=", "--label\x00" + InstanceLabel + "=api",
		"run\x00--rm",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the invocation lacks %q:\n%q", want, args)
		}
	}
}

// Absent deploy.image the node runs in projectbuilder, so a repo that declares
// exec and nothing else behaves as before the key existed.
func TestForeignNodeDefaultsToProjectbuilder(t *testing.T) {
	t.Parallel()

	r := testRunner(t, &fakeDocker{}, "")
	args := r.dockerArgsForeign(msdeploy.DeploymentNode{InstanceName: "a"}, "/ws", nodeCommand{Argv: []string{"make"}}, []byte("{}"))
	if args[len(args)-2] != "pb" {
		t.Errorf("image = %q, want the configured projectbuilder", args[len(args)-2])
	}
}

// End to end through RunNode: the manifest's exec argv runs in the manifest's
// image, no script is written or mounted, and the configuration comes from
// the generated script's heredoc rather than the node's (empty) field.
func TestRunNodeRunsAForeignNodeInItsOwnImage(t *testing.T) {
	t.Parallel()

	repoHome := t.TempDir()
	repo := filepath.Join(repoHome, "acme-api")
	mustWrite(t, filepath.Join(repo, "meta-data", "manifest.json"),
		`{"name":"acme-api","deploy":{"commands":[["exec","make","deploy"]],"image":"alpine:3.20"}}`)
	d := &fakeDocker{}
	r := testRunner(t, d, repoHome)

	res := r.RunNode(context.Background(), msdeploy.DeploymentNode{
		InstanceName: "api", RepoClassName: "acme-api", Version: "0.3",
		Script: fixtureScript(t),
	})
	if res.Failed {
		t.Fatalf("RunNode: %v", res.Err)
	}
	joined := strings.Join(d.args, " ")
	if !strings.HasSuffix(joined, " alpine:3.20 make deploy") {
		t.Errorf("the node did not run its argv in its image:\n%s", joined)
	}
	if strings.Contains(joined, "nsctl-deploy-") || strings.Contains(joined, "--entrypoint") {
		t.Errorf("a script was written or mounted:\n%s", joined)
	}
	var config string
	for _, kv := range envFlags(d.args) {
		if v, ok := strings.CutPrefix(kv, "HMD_INSTANCE_CONFIG="); ok {
			config = v
		}
	}
	if !strings.Contains(config, `"host":"hmd_db-local"`) {
		t.Errorf("HMD_INSTANCE_CONFIG does not carry the resolved dependency outputs: %s", config)
	}
}

// The native path gains HMD_REPO_VERSION too: a deploy_local.sh has exactly
// the same need for it that an exec argv has.
func TestNativeNodesCarryTheRepoVersion(t *testing.T) {
	t.Parallel()

	repoHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoHome, "hmd-inf-thing"), 0o755); err != nil {
		t.Fatal(err)
	}
	d := &fakeDocker{}
	r := testRunner(t, d, repoHome)
	r.RunNode(context.Background(), msdeploy.DeploymentNode{
		InstanceName: "thing", RepoClassName: "hmd-inf-thing", Version: "0.7", Script: "hmd deploy",
	})
	if !strings.Contains(strings.Join(envFlags(d.args), "|"), "HMD_REPO_VERSION=0.7") {
		t.Errorf("HMD_REPO_VERSION missing: %v", envFlags(d.args))
	}
}

// The runner collects meta-data/resources_output/*.json after the node exits,
// and hmd-cli-helm creates that directory itself on the native path. A foreign
// toolset cannot be expected to know the convention, and the first live run
// proved it: `sh: can't create meta-data/resources_output/probe.json:
// nonexistent directory`. The runner creates it, for both the direct mount
// and the isolated copy.
func TestAForeignWorkspaceHasAResourcesOutputDirectory(t *testing.T) {
	t.Parallel()

	for _, isolate := range []bool{false, true} {
		repo := foreignRepo(t, "")
		r := testRunner(t, &fakeDocker{}, "")
		workspace, _, cleanup, err := r.prepareWorkspace(repo, "hmd deploy", isolate)
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		if info, err := os.Stat(filepath.Join(workspace, "meta-data", "resources_output")); err != nil || !info.IsDir() {
			t.Errorf("isolate=%v: no resources_output directory in the workspace: %v", isolate, err)
		}
	}
}
