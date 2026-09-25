package runner

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/artifact"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/repotree"
)

// The generated script runs a bare `hmd ... deploy`, which makes hmd-cli-deploy
// pull the build bundle from the Artifact Librarian -- absent locally.
func TestLocalize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			"a plain deploy command",
			"hmd deploy --deployment-id local",
			"hmd deploy --local --deployment-id local",
		},
		{
			"already localized",
			"hmd deploy --local --deployment-id local",
			"hmd deploy --local --deployment-id local",
		},
		{
			"only the command line is transformed",
			"export FOO=deploy\nhmd deploy --x\necho deploy",
			"export FOO=deploy\nhmd deploy --local --x\necho deploy",
		},
		{
			"only the first deploy line",
			"hmd deploy --a\nhmd deploy --b",
			"hmd deploy --local --a\nhmd deploy --b",
		},
		{"empty", "", ""},
		{"nothing to do", "echo hello", "echo hello"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := Localize(tt.input); got != tt.want {
				t.Errorf("Localize()\n got %q\nwant %q", got, tt.want)
			}
		})
	}
}

// fakeDocker records the invocation.
type fakeDocker struct {
	args   []string
	stdout string
	stderr string
	err    error
}

func (f *fakeDocker) Run(_ context.Context, args ...string) ([]byte, []byte, error) {
	f.args = args
	return []byte(f.stdout), []byte(f.stderr), f.err
}

func testRunner(t *testing.T, d *fakeDocker, repoHome string) *Runner {
	t.Helper()
	return &Runner{
		Docker: d,
		Config: Config{
			Network: "net", Image: "pb", FlociEndpoint: "http://neuronsphere:4566",
			AccountID: "000000000001", DeploymentServiceURL: "http://hmd_proxy/hmd_ms_deployment",
			LocalProxy: "http://hmd_proxy/local", K3sCluster: "ns-local-abc",
			RepoHome: repoHome, DeploymentID: "local", Region: "reg1", CustomerCode: "hmdtr1",
		},
		// io.Discard, not os.NewFile(0, os.DevNull). That does not open
		// /dev/null -- it wraps file descriptor 0, the process's stdin, and
		// merely names it "/dev/null". os.NewFile installs a finalizer that
		// closes the descriptor it wraps, so every collected helper closed fd 0;
		// the next open reused that number and a later finalizer closed it out
		// from under its owner. It surfaced as `bad file descriptor` on whatever
		// directory copyTree happened to be walking, in about one run in three.
		// (It also put stdin into non-blocking mode for the whole process.)
		Out: io.Discard, Err: io.Discard,
	}
}

// apply_changeset already created every edge the core instance exists for;
// there is nothing to run.
func TestCoreInstanceIsAStatusFlipNotADeploy(t *testing.T) {
	t.Parallel()

	d := &fakeDocker{}
	r := testRunner(t, d, t.TempDir())
	res := r.RunNode(context.Background(), msdeploy.DeploymentNode{
		InstanceName: "local-neuronsphere", RepoClassName: CoreRepoClass,
	})
	if res.Failed {
		t.Errorf("the core instance failed: %v", res.Err)
	}
	if d.args != nil {
		t.Errorf("a container was launched for the core instance: %v", d.args)
	}
}

// There is no bundled-in-image fallback for a repo's own deploy source, so a
// missing tree is fatal and has to say what to do about it.
func TestMissingWorkingTreeFailsWithARemedy(t *testing.T) {
	t.Parallel()

	r := testRunner(t, &fakeDocker{}, t.TempDir())
	res := r.RunNode(context.Background(), msdeploy.DeploymentNode{
		InstanceName: "thing", RepoClassName: "hmd-inf-thing", Script: "hmd deploy",
	})
	if !res.Failed {
		t.Fatal("a node with no working tree succeeded")
	}
	if !strings.Contains(res.Err.Error(), "hmd-inf-thing") || !strings.Contains(res.Err.Error(), "manifest") {
		t.Errorf("the error does not name the repo and the remedy: %v", res.Err)
	}
}

func TestDockerArgsCarryTheAccountAndTheNodeIdentity(t *testing.T) {
	t.Parallel()

	repoHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoHome, "hmd-inf-thing"), 0o755); err != nil {
		t.Fatal(err)
	}
	d := &fakeDocker{}
	r := testRunner(t, d, repoHome)

	node := msdeploy.DeploymentNode{
		InstanceName: "thing", RepoClassName: "hmd-inf-thing", Version: "0.1",
		Script:                "hmd deploy --deployment-id local",
		InstanceConfiguration: map[string]any{"db_host": "hmd_db-local"},
	}
	if res := r.RunNode(context.Background(), node); res.Failed {
		t.Fatalf("RunNode: %v", res.Err)
	}

	joined := strings.Join(d.args, " ")
	for _, want := range []string{
		// The account selector: an ambient key would put every environment's
		// CDKTF state in the same account.
		"AWS_ACCESS_KEY_ID=000000000001",
		"AWS_ENDPOINT_URL=http://neuronsphere:4566",
		"HMD_ENVIRONMENT=local",
		// In-container the local nginx is hmd_proxy, not neuronsphere, which is
		// the Floci alias on :4566.
		"HMD_DEPLOYMENT_SERVICE_URL=http://hmd_proxy/hmd_ms_deployment",
		// The environment's route prefix, or a deploy tool posts to a
		// control-plane path where its service is not routed at all.
		"NS_LOCAL_PROXY=http://hmd_proxy/local",
		"HMD_LOCAL_K3S_CLUSTER_NAME=ns-local-abc",
		// A deploy_local.sh override replaces the generated command, so the
		// node's identity has to reach it another way.
		"HMD_INSTANCE_NAME=thing",
		"HMD_REPO_NAME=hmd-inf-thing",
		"--entrypoint bash",
		"--network net",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args do not contain %q:\n%s", want, joined)
		}
	}
	if !strings.Contains(joined, "/tmp/hmd-deploy-node.sh") {
		t.Errorf("the script was not mounted:\n%s", joined)
	}
	// The script is mounted rather than inlined, because a node with many
	// resolved dependencies exceeds ARG_MAX.
	if strings.Contains(joined, "hmd deploy --local --deployment-id") {
		t.Errorf("the script was passed as an argument:\n%s", joined)
	}
}

func TestDockerArgsAreDeterministic(t *testing.T) {
	t.Parallel()

	repoHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoHome, "hmd-inf-thing"), 0o755); err != nil {
		t.Fatal(err)
	}
	node := msdeploy.DeploymentNode{InstanceName: "t", RepoClassName: "hmd-inf-thing", Script: "hmd deploy"}

	first := &fakeDocker{}
	testRunner(t, first, repoHome).RunNode(context.Background(), node)
	second := &fakeDocker{}
	testRunner(t, second, repoHome).RunNode(context.Background(), node)

	// The script path differs (a temp file), so compare the environment flags.
	firstEnv := envFlags(first.args)
	secondEnv := envFlags(second.args)
	if strings.Join(firstEnv, "|") != strings.Join(secondEnv, "|") {
		t.Errorf("the environment is not deterministic:\n%v\n%v", firstEnv, secondEnv)
	}
}

func envFlags(args []string) []string {
	var out []string
	for i, a := range args {
		if a == "-e" && i+1 < len(args) {
			out = append(out, args[i+1])
		}
	}
	return out
}

// The workspace is mounted read-write and these deploys write
// meta-data/resources_output/, so an overlay must never mount the developer's
// own checkout.
func TestOverlayWorkspaceLeavesTheCheckoutAlone(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	mustWrite(t, filepath.Join(repo, "src", "cdktf", "main.py"), "cloud")
	mustWrite(t, filepath.Join(repo, "src", "local", "cdktf", "main.py"), "local")
	mustWrite(t, filepath.Join(repo, "meta-data", "VERSION"), "0.1")

	r := testRunner(t, &fakeDocker{}, "")
	workspace, cmd, cleanup, err := r.prepareWorkspace(repo, "hmd deploy", false)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	script := cmd.Script

	if workspace == repo {
		t.Fatal("the developer's checkout was mounted directly")
	}
	overlaid := readFile(t, filepath.Join(workspace, "src", "cdktf", "main.py"))
	if overlaid != "local" {
		t.Errorf("the overlay was not applied: %q", overlaid)
	}
	if readFile(t, filepath.Join(repo, "src", "cdktf", "main.py")) != "cloud" {
		t.Error("the developer's checkout was modified")
	}
	if !strings.Contains(script, "--local") {
		t.Errorf("the script was not localized: %q", script)
	}
}

// A full override replaces the generated command entirely, so it is not
// localized either.
func TestDeployLocalOverrideReplacesTheCommand(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	mustWrite(t, filepath.Join(repo, "src", "local", "deploy_local.sh"), "echo hi")

	r := testRunner(t, &fakeDocker{}, "")
	workspace, cmd, cleanup, err := r.prepareWorkspace(repo, "hmd deploy", false)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	if script := cmd.Script; script != "bash src/local/deploy_local.sh" {
		t.Errorf("script = %q, want the override", script)
	}
	if workspace == repo {
		t.Error("the override ran against the developer's checkout")
	}
}

// Outputs must never become inputs: the runner submits every
// resources_output/*.json it finds, so copying a prior run's forward would
// republish Resources for a deploy that did not happen.
func TestOverlayDoesNotCarryPriorResourceOutputsForward(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	mustWrite(t, filepath.Join(repo, "src", "local", "cdktf", "main.py"), "local")
	mustWrite(t, filepath.Join(repo, "meta-data", "resources_output", "old.json"), "{}")

	r := testRunner(t, &fakeDocker{}, "")
	workspace, _, cleanup, err := r.prepareWorkspace(repo, "hmd deploy", false)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	if _, err := os.Stat(filepath.Join(workspace, "meta-data", "resources_output", "old.json")); !os.IsNotExist(err) {
		t.Error("a prior run's produced resources were copied into the workspace")
	}
}

// A repo with no src/local is mounted as it is.
func TestNoOverlayMountsTheRepoDirectly(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	mustWrite(t, filepath.Join(repo, "meta-data", "VERSION"), "0.1")

	r := testRunner(t, &fakeDocker{}, "")
	workspace, _, cleanup, err := r.prepareWorkspace(repo, "hmd deploy", false)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if workspace != repo {
		t.Errorf("workspace = %q, want the repo itself", workspace)
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// A manifest can declare a working tree outside HMD_REPO_HOME, and the runner
// has to mount that one rather than looking for a directory under the
// convention that does not exist.
func TestRepoPathOverrideIsMounted(t *testing.T) {
	t.Parallel()

	elsewhere := filepath.Join(t.TempDir(), "vendored")
	if err := os.MkdirAll(elsewhere, 0o755); err != nil {
		t.Fatal(err)
	}
	r := &Runner{Config: Config{
		RepoHome:  t.TempDir(),
		RepoPaths: map[string]string{"hmd-ms-a": elsewhere},
	}}

	if got, _ := r.repoPath("hmd-ms-a"); got != elsewhere {
		t.Errorf("repoPath = %q, want the declared tree %q", got, elsewhere)
	}
}

// An override pointing nowhere must not fall back to the convention: silently
// deploying a different tree than the manifest names is worse than failing.
func TestRepoPathOverrideDoesNotFallBack(t *testing.T) {
	t.Parallel()

	repoHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoHome, "hmd-ms-a"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := &Runner{Config: Config{
		RepoHome:  repoHome,
		RepoPaths: map[string]string{"hmd-ms-a": "/definitely/not/here"},
	}}

	if got, _ := r.repoPath("hmd-ms-a"); got != "" {
		t.Errorf("repoPath = %q, want the missing override to resolve to nothing", got)
	}
}

// TestAnArtifactTreeIsIsolatedLikeABundledOne. An unpacked artifact arrives
// through RepoPaths exactly as a vendored checkout does, but it is not the same
// kind of thing: it is keyed by version, shared by every environment on this
// HMD_HOME, and a deploy writing meta-data/resources_output/ into it would hand
// the next environment this one's resources. Mounting it directly is the quiet
// version of that bug.
func TestAnArtifactTreeIsIsolatedLikeABundledOne(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cached := artifact.Dir(home, "hmd-ms-a", "0.1.4")
	if err := os.MkdirAll(filepath.Join(cached, "meta-data"), 0o755); err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(t.TempDir(), "vendored")
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		path       string
		wantShared bool
	}{
		{"an unpacked artifact", cached, true},
		{"a vendored checkout", checkout, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := &Runner{Config: Config{Home: home, RepoPaths: map[string]string{"hmd-ms-a": tt.path}}}
			got, shared := r.repoPath("hmd-ms-a")
			if got != tt.path {
				t.Errorf("repoPath = %q, want %q", got, tt.path)
			}
			if shared != tt.wantShared {
				t.Errorf("shared = %v, want %v", shared, tt.wantShared)
			}
		})
	}
}

// The predicate is asked of the path, so a cache root added later is covered by
// having put it in the same place rather than by remembering to list it.
func TestSharedTreeCoversEveryCacheRoot(t *testing.T) {
	t.Parallel()

	const home = "/hmd"
	for _, path := range []string{
		filepath.Join(repotree.Root(home), "hmd-vpc@abc123"),
		filepath.Join(artifact.Root(home), "hmd-inf-x@0.1.4"),
	} {
		if !sharedTree(home, path) {
			t.Errorf("sharedTree(%q) = false", path)
		}
	}
	for _, path := range []string{"", "/elsewhere/hmd-vpc", filepath.Join(home, "environments", "local")} {
		if sharedTree(home, path) {
			t.Errorf("sharedTree(%q) = true", path)
		}
	}
	if sharedTree("", filepath.Join(artifact.Root(home), "x@1")) {
		t.Error("sharedTree said true with no HMD_HOME to anchor it")
	}
}
