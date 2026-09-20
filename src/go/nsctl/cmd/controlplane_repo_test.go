package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// cpEnv builds an HMD_HOME with a registry and a repo home holding one
// extension repo class, so a declaration resolves without a real platform.
func cpEnv(t *testing.T, classes ...string) (string, map[string]string) {
	t.Helper()
	home := registryHome(t, twoEnvRegistry)
	repoHome := t.TempDir()
	for _, c := range classes {
		dir := filepath.Join(repoHome, c, "src", "local")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := "services:\n  server:\n    image: thing:1\n    networks: [neuronsphere_default]\n"
		if err := os.WriteFile(filepath.Join(dir, "docker-compose.extension.yml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return home, map[string]string{
		"HMD_HOME":                    home,
		"HMD_REPO_HOME":               repoHome,
		"HMD_LOCAL_MS_DEPLOYMENT_URL": "http://127.0.0.1:1",
	}
}

func readCPManifest(t *testing.T, env map[string]string) *manifest.Manifest {
	t.Helper()
	m, err := manifest.LoadControlPlane(env["HMD_HOME"], fakeEnv(env))
	if err != nil {
		t.Fatalf("loading the control-plane manifest: %v", err)
	}
	return m
}

// The first `add` creates the manifest rather than requiring one to exist.
func TestControlPlaneRepoAddCreatesTheManifest(t *testing.T) {
	t.Parallel()

	home, env := cpEnv(t, "hmd-inf-local-registry")
	out, _, err := run(t, fakeEnv(env), "control-plane", "repo", "add", "hmd-inf-local-registry")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	want := filepath.Join(home, ".config", "control-plane.yaml")
	if !strings.Contains(out, want) {
		t.Errorf("output %q does not name %q", out, want)
	}

	m := readCPManifest(t, env)
	if m == nil {
		t.Fatal("no manifest was written")
	}
	if m.Name != manifest.ControlPlaneName {
		t.Errorf("name = %q, want %q", m.Name, manifest.ControlPlaneName)
	}
	if len(m.Repos) != 1 || m.Repos[0].InstanceName != "inf-local-registry" {
		t.Fatalf("repos = %+v", m.Repos)
	}
}

// Declaring is not starting, and the output has to say which verb does.
func TestControlPlaneRepoAddPointsAtApply(t *testing.T) {
	t.Parallel()

	_, env := cpEnv(t, "hmd-inf-local-registry")
	out, _, err := run(t, fakeEnv(env), "control-plane", "repo", "add", "hmd-inf-local-registry")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if !strings.Contains(out, "nsctl control-plane apply") {
		t.Errorf("output %q does not point at apply", out)
	}
}

// Dotted keys nest, which is what makes `pypi.enabled=true` the block shape
// NERD006 writes by hand and cpext reads as a profile.
func TestControlPlaneRepoAddNestsDottedConfig(t *testing.T) {
	t.Parallel()

	_, env := cpEnv(t, "hmd-inf-local-registry")
	_, _, err := run(t, fakeEnv(env), "control-plane", "repo", "add", "hmd-inf-local-registry",
		"--name", "package-registry",
		"--config", "pypi.enabled=true",
		"--config", "url=http://registry.local.neuronsphere.io")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	config := readCPManifest(t, env).Repos[0].InstanceConfiguration
	pypi, ok := config["pypi"].(map[string]any)
	if !ok {
		t.Fatalf("pypi = %#v, want a block", config["pypi"])
	}
	if pypi["enabled"] != true {
		t.Errorf("pypi.enabled = %#v, want true", pypi["enabled"])
	}
	if config["url"] != "http://registry.local.neuronsphere.io" {
		t.Errorf("url = %#v", config["url"])
	}
}

// Overwriting a scalar with a block silently would drop the earlier --config.
func TestControlPlaneRepoAddRefusesConflictingConfig(t *testing.T) {
	t.Parallel()

	_, env := cpEnv(t, "hmd-inf-local-registry")
	_, _, err := run(t, fakeEnv(env), "control-plane", "repo", "add", "hmd-inf-local-registry",
		"--config", "pypi=yes", "--config", "pypi.enabled=true")
	if err == nil {
		t.Fatal("a conflicting --config was accepted")
	}
	if nserr.CodeOf(err) != nserr.Usage {
		t.Errorf("exit code = %d, want Usage", nserr.CodeOf(err))
	}
	if !strings.Contains(err.Error(), "conflicts with") {
		t.Errorf("error %q does not explain the conflict", err)
	}
}

func TestControlPlaneRepoAddRefusesAReservedName(t *testing.T) {
	t.Parallel()

	_, env := cpEnv(t, "hmd-inf-local-registry")
	_, _, err := run(t, fakeEnv(env), "control-plane", "repo", "add", "hmd-inf-local-registry",
		"--name", "control-plane-db")
	if err == nil {
		t.Fatal("a reserved name was accepted")
	}
	if !strings.Contains(err.Error(), "reserved") || !strings.Contains(err.Error(), "--name") {
		t.Errorf("error %q does not say what to do instead", err)
	}
}

func TestControlPlaneRepoAddRefusesADuplicate(t *testing.T) {
	t.Parallel()

	_, env := cpEnv(t, "hmd-inf-local-registry")
	args := []string{"control-plane", "repo", "add", "hmd-inf-local-registry"}
	if _, _, err := run(t, fakeEnv(env), args...); err != nil {
		t.Fatalf("first add: %v", err)
	}
	_, _, err := run(t, fakeEnv(env), args...)
	if err == nil {
		t.Fatal("a duplicate instance name was accepted")
	}
	if !strings.Contains(err.Error(), "already declared") {
		t.Errorf("error %q", err)
	}
}

// Removing a line is not an instruction to destroy what it started, and the
// note has to say so or a user will assume it was torn down.
func TestControlPlaneRepoRemoveSaysNothingWasTornDown(t *testing.T) {
	t.Parallel()

	_, env := cpEnv(t, "hmd-inf-local-registry")
	if _, _, err := run(t, fakeEnv(env), "control-plane", "repo", "add",
		"hmd-inf-local-registry", "--name", "package-registry"); err != nil {
		t.Fatalf("add: %v", err)
	}

	out, errOut, err := run(t, fakeEnv(env), "control-plane", "repo", "remove", "package-registry")
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !strings.Contains(out, "Removed package-registry") {
		t.Errorf("stdout %q", out)
	}
	if !strings.Contains(errOut, "still running") || !strings.Contains(errOut, "untouched") {
		t.Errorf("stderr %q does not say the containers and data survive", errOut)
	}
	if m := readCPManifest(t, env); len(m.Repos) != 0 {
		t.Errorf("repos = %+v, want none", m.Repos)
	}
}

func TestControlPlaneRepoRemoveNamesWhatIsDeclared(t *testing.T) {
	t.Parallel()

	_, env := cpEnv(t, "hmd-inf-local-registry")
	if _, _, err := run(t, fakeEnv(env), "control-plane", "repo", "add",
		"hmd-inf-local-registry", "--name", "package-registry"); err != nil {
		t.Fatalf("add: %v", err)
	}
	_, _, err := run(t, fakeEnv(env), "control-plane", "repo", "remove", "nosuch")
	if err == nil {
		t.Fatal("removing an undeclared instance succeeded")
	}
	if !strings.Contains(err.Error(), "package-registry") {
		t.Errorf("error %q does not list what is declared", err)
	}
}

func TestControlPlaneRepoRemoveWithoutAManifest(t *testing.T) {
	t.Parallel()

	_, env := cpEnv(t)
	_, _, err := run(t, fakeEnv(env), "control-plane", "repo", "remove", "anything")
	if err == nil {
		t.Fatal("removing from a nonexistent manifest succeeded")
	}
	if !strings.Contains(err.Error(), "no control-plane manifest") {
		t.Errorf("error %q", err)
	}
}

// An HMD_HOME declaring nothing says so and names the verb that changes it,
// rather than printing an empty table.
func TestControlPlaneRepoListWithNoExtensions(t *testing.T) {
	t.Parallel()

	_, env := cpEnv(t)
	out, _, err := run(t, fakeEnv(env), "control-plane", "repo", "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "declares no extensions") ||
		!strings.Contains(out, "control-plane repo add") {
		t.Errorf("output %q", out)
	}
}

// The version source is a column of its own: "0.1.4 from a working tree" and
// "0.1.4 from a bundled tree" are different facts.
func TestControlPlaneRepoListReportsTheVersionSource(t *testing.T) {
	t.Parallel()

	_, env := cpEnv(t, "hmd-inf-local-registry")
	version := filepath.Join(env["HMD_REPO_HOME"], "hmd-inf-local-registry", "meta-data", "VERSION")
	if err := os.MkdirAll(filepath.Dir(version), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(version, []byte("0.1.4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, fakeEnv(env), "control-plane", "repo", "add",
		"hmd-inf-local-registry", "--name", "package-registry"); err != nil {
		t.Fatalf("add: %v", err)
	}

	out, _, err := run(t, fakeEnv(env), "control-plane", "repo", "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, want := range []string{"package-registry", "0.1.4", "working-tree", "FROM"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q does not contain %q", out, want)
		}
	}
}

// A declaration that will not resolve still appears, with its reason: dropping
// it would lose the only place its name is shown.
func TestControlPlaneRepoListShowsAnUnresolvedExtension(t *testing.T) {
	t.Parallel()

	_, env := cpEnv(t)
	home := env["HMD_HOME"]
	path := filepath.Join(home, ".config", "control-plane.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "version: 1\nname: control-plane\nrepos:\n" +
		"  - instance_name: absent\n    repo_class_name: hmd-inf-never-checked-out\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	out, _, err := run(t, fakeEnv(env), "control-plane", "repo", "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "absent") || !strings.Contains(out, "unresolved") {
		t.Errorf("output %q", out)
	}
	if !strings.Contains(out, "no working tree") {
		t.Errorf("output %q does not give the reason", out)
	}
}

// `apply` against a control plane that is not running says so, rather than
// failing somewhere inside the Docker client.
func TestControlPlaneApplyRefusesWhenNothingIsRunning(t *testing.T) {
	t.Parallel()

	_, env := cpEnv(t)
	_, _, err := run(t, fakeEnv(env), "control-plane", "apply")
	if err == nil {
		t.Skip("a control plane is running on this machine")
	}
	if !strings.Contains(err.Error(), "control-plane start") {
		t.Errorf("error %q does not name the verb that fixes it", err)
	}
}
