package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// repoEnv builds an HMD_HOME with a registry and a working tree per class, so
// source validation passes without reaching for the real one.
func repoEnv(t *testing.T, classes ...string) (home string, env map[string]string) {
	t.Helper()
	home = registryHome(t, twoEnvRegistry)
	repoHome := t.TempDir()
	for _, c := range classes {
		if err := os.MkdirAll(filepath.Join(repoHome, c), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return home, map[string]string{
		"HMD_HOME":      home,
		"HMD_REPO_HOME": repoHome,
		// Nothing in these tests may reach a real deployment service.
		"HMD_LOCAL_MS_DEPLOYMENT_URL": "http://127.0.0.1:1",
	}
}

func readManifest(t *testing.T, env map[string]string, slug string) *manifest.Manifest {
	t.Helper()
	m, err := manifest.Load(env["HMD_HOME"], slug, fakeEnv(env))
	if err != nil {
		t.Fatalf("loading the manifest: %v", err)
	}
	return m
}

// The first `repo add` in an environment creates the manifest rather than
// requiring one to exist.
func TestRepoAddCreatesTheManifest(t *testing.T) {
	t.Parallel()

	_, env := repoEnv(t, "hmd-ms-myapi")
	out, _, err := run(t, fakeEnv(env), "repo", "add", "hmd-ms-myapi")
	if err != nil {
		t.Fatalf("repo add: %v", err)
	}
	if !strings.Contains(out, "nsctl env apply") {
		t.Errorf("output does not name the command that deploys it:\n%s", out)
	}

	m := readManifest(t, env, "local")
	if m == nil {
		t.Fatal("no manifest was written")
	}
	if len(m.Repos) != 1 || m.Repos[0].InstanceName != "ms-myapi" {
		t.Fatalf("manifest declares %+v", m.Repos)
	}
	if m.Repos[0].RepoClassName != "hmd-ms-myapi" {
		t.Errorf("repo class = %q", m.Repos[0].RepoClassName)
	}
}

func TestRepoAddParsesTheVersionPin(t *testing.T) {
	t.Parallel()

	_, env := repoEnv(t, "hmd-ms-myapi")
	if _, _, err := run(t, fakeEnv(env), "repo", "add", "hmd-ms-myapi@0.3"); err != nil {
		t.Fatalf("repo add: %v", err)
	}
	m := readManifest(t, env, "local")
	if m.Repos[0].Version != "0.3" {
		t.Errorf("version = %q, want 0.3", m.Repos[0].Version)
	}
	if m.Repos[0].RepoClassName != "hmd-ms-myapi" {
		t.Errorf("repo class = %q, want the pin stripped", m.Repos[0].RepoClassName)
	}
}

// A configuration value is typed: `replicas: 2` is an integer to whatever
// consumes it, and "2" is a different thing.
func TestRepoAddTypesConfigurationValues(t *testing.T) {
	t.Parallel()

	_, env := repoEnv(t, "hmd-ms-myapi")
	_, _, err := run(t, fakeEnv(env), "repo", "add", "hmd-ms-myapi",
		"--config", "replicas=2", "--config", "debug=true", "--config", "profile=minimal")
	if err != nil {
		t.Fatalf("repo add: %v", err)
	}

	config := readManifest(t, env, "local").Repos[0].InstanceConfiguration
	if config["replicas"] != 2 {
		t.Errorf("replicas = %#v, want the integer 2", config["replicas"])
	}
	if config["debug"] != true {
		t.Errorf("debug = %#v, want the boolean true", config["debug"])
	}
	if config["profile"] != "minimal" {
		t.Errorf("profile = %#v, want the bare word as a string", config["profile"])
	}
}

func TestRepoAddWritesBothDependencyShapes(t *testing.T) {
	t.Parallel()

	_, env := repoEnv(t, "hmd-ms-myapi")
	_, _, err := run(t, fakeEnv(env), "repo", "add", "hmd-ms-myapi",
		"--depends", "cluster=eks-cluster", "--depends", "peers=a,b")
	if err != nil {
		t.Fatalf("repo add: %v", err)
	}

	deps := readManifest(t, env, "local").Repos[0].Dependencies
	if deps["cluster"] != "eks-cluster" {
		t.Errorf("cluster = %#v", deps["cluster"])
	}
	list, ok := deps["peers"].([]any)
	if !ok || len(list) != 2 || list[0] != "a" || list[1] != "b" {
		t.Errorf("peers = %#v, want the list form", deps["peers"])
	}
}

func TestRepoAddHonoursTheEnvFlag(t *testing.T) {
	t.Parallel()

	_, env := repoEnv(t, "hmd-ms-myapi")
	if _, _, err := run(t, fakeEnv(env), "repo", "add", "hmd-ms-myapi", "--env", "dev"); err != nil {
		t.Fatalf("repo add: %v", err)
	}
	if m := readManifest(t, env, "dev"); m == nil || len(m.Repos) != 1 {
		t.Errorf("dev's manifest = %+v", m)
	}
	if m := readManifest(t, env, "local"); m != nil {
		t.Errorf("the default environment was edited too: %+v", m.Repos)
	}
}

func TestRepoAddRefusals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{"a substrate name", []string{"repo", "add", "hmd-postgres-rds", "--name", "environment-db"}, "reserved"},
		{"no working tree", []string{"repo", "add", "hmd-ms-absent"}, "does not exist"},
		{"a malformed pair", []string{"repo", "add", "hmd-ms-myapi", "--config", "nonsense"}, "key=value"},
		{"an unknown environment", []string{"repo", "add", "hmd-ms-myapi", "--env", "nope"}, "no environment"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, env := repoEnv(t, "hmd-ms-myapi", "hmd-postgres-rds")
			_, stderr, err := run(t, fakeEnv(env), tt.args...)
			if err == nil {
				t.Fatalf("succeeded, want a refusal (stderr: %s)", stderr)
			}
			if got := nserr.CodeOf(err); got != nserr.Usage {
				t.Errorf("exit code = %d, want %d (usage)", got, nserr.Usage)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not mention %q", err, tt.want)
			}
		})
	}
}

func TestRepoAddRefusesADuplicateAndSaysHowToDeclareTwo(t *testing.T) {
	t.Parallel()

	_, env := repoEnv(t, "hmd-ms-myapi")
	if _, _, err := run(t, fakeEnv(env), "repo", "add", "hmd-ms-myapi"); err != nil {
		t.Fatalf("first add: %v", err)
	}
	_, _, err := run(t, fakeEnv(env), "repo", "add", "hmd-ms-myapi")
	if err == nil {
		t.Fatal("a duplicate instance name was accepted")
	}
	if !strings.Contains(err.Error(), "--name") {
		t.Errorf("error does not name the way to declare a second: %v", err)
	}
}

// A rejected addition must not leave a partially-written manifest behind.
func TestRepoAddDoesNotWriteWhenItRefuses(t *testing.T) {
	t.Parallel()

	_, env := repoEnv(t, "hmd-ms-myapi")
	if _, _, err := run(t, fakeEnv(env), "repo", "add", "hmd-ms-myapi"); err != nil {
		t.Fatalf("first add: %v", err)
	}
	if _, _, err := run(t, fakeEnv(env), "repo", "add", "hmd-ms-absent"); err == nil {
		t.Fatal("the invalid addition succeeded")
	}

	m := readManifest(t, env, "local")
	if len(m.Repos) != 1 || m.Repos[0].InstanceName != "ms-myapi" {
		t.Errorf("the refused addition changed the manifest: %+v", m.Repos)
	}
}

func TestRepoRemoveDropsTheDeclaration(t *testing.T) {
	t.Parallel()

	_, env := repoEnv(t, "hmd-ms-myapi", "hmd-ms-other")
	for _, class := range []string{"hmd-ms-myapi", "hmd-ms-other"} {
		if _, _, err := run(t, fakeEnv(env), "repo", "add", class); err != nil {
			t.Fatalf("repo add %s: %v", class, err)
		}
	}

	out, stderr, err := run(t, fakeEnv(env), "repo", "remove", "ms-myapi")
	if err != nil {
		t.Fatalf("repo remove: %v", err)
	}
	if !strings.Contains(out, "Removed ms-myapi") {
		t.Errorf("output = %q", out)
	}
	// Removing a declaration is not a teardown, and saying so avoids a user
	// assuming the instance is gone.
	if !strings.Contains(stderr, "still running") {
		t.Errorf("stderr does not say the deployment survives: %q", stderr)
	}

	m := readManifest(t, env, "local")
	if len(m.Repos) != 1 || m.Repos[0].InstanceName != "ms-other" {
		t.Errorf("manifest = %+v, want only ms-other", m.Repos)
	}
}

func TestRepoRemoveRefusalsNameWhatIsDeclared(t *testing.T) {
	t.Parallel()

	_, env := repoEnv(t, "hmd-ms-myapi")

	// No manifest at all.
	_, _, err := run(t, fakeEnv(env), "repo", "remove", "ms-myapi")
	if err == nil || !strings.Contains(err.Error(), "no manifest") {
		t.Errorf("error = %v, want it to say the environment declares nothing", err)
	}

	if _, _, err := run(t, fakeEnv(env), "repo", "add", "hmd-ms-myapi"); err != nil {
		t.Fatal(err)
	}
	_, _, err = run(t, fakeEnv(env), "repo", "remove", "not-declared")
	if err == nil {
		t.Fatal("removing an undeclared instance succeeded")
	}
	if !strings.Contains(err.Error(), "ms-myapi") {
		t.Errorf("error does not list what is declared: %v", err)
	}
}

// The substrate is deployed whether or not a manifest exists, so listing it is
// what explains instances the user never declared.
func TestRepoListShowsTheSubstrateAndTheDeclarations(t *testing.T) {
	t.Parallel()

	_, env := repoEnv(t, "hmd-ms-myapi")
	if _, _, err := run(t, fakeEnv(env), "repo", "add", "hmd-ms-myapi"); err != nil {
		t.Fatal(err)
	}

	out, stderr, err := run(t, fakeEnv(env), "repo", "list")
	if err != nil {
		t.Fatalf("repo list: %v", err)
	}
	for _, want := range []string{"eks-cluster", "environment-db", "substrate", "ms-myapi", "manifest"} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not contain %q:\n%s", want, out)
		}
	}
	// Unreachable service: the manifest half is still worth showing, and the
	// unknown deployed state is said rather than guessed.
	if !strings.Contains(stderr, "not answering") {
		t.Errorf("stderr does not explain the missing status: %q", stderr)
	}
}

func TestRepoListOnAnEnvironmentWithNoManifestSaysSo(t *testing.T) {
	t.Parallel()

	_, env := repoEnv(t)
	out, _, err := run(t, fakeEnv(env), "repo", "list")
	if err != nil {
		t.Fatalf("repo list: %v", err)
	}
	if !strings.Contains(out, "no manifest") {
		t.Errorf("output does not say the environment has none:\n%s", out)
	}
	if !strings.Contains(out, "nsctl repo add") {
		t.Errorf("output does not name the command that declares one:\n%s", out)
	}
}

func TestDefaultInstanceName(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"hmd-ms-transform": "ms-transform",
		"hmd-inf-trino":    "inf-trino",
		"unprefixed":       "unprefixed",
	}
	for class, want := range tests {
		if got := defaultInstanceName(class); got != want {
			t.Errorf("defaultInstanceName(%q) = %q, want %q", class, got, want)
		}
	}
}

func TestSplitVersion(t *testing.T) {
	t.Parallel()

	tests := []struct{ arg, class, version string }{
		{"hmd-ms-a", "hmd-ms-a", ""},
		{"hmd-ms-a@0.3", "hmd-ms-a", "0.3"},
		{"hmd-ms-a@0.3.1", "hmd-ms-a", "0.3.1"},
		// A leading @ is not a pin with an empty class.
		{"@0.3", "@0.3", ""},
	}
	for _, tt := range tests {
		class, version := splitVersion(tt.arg)
		if class != tt.class || version != tt.version {
			t.Errorf("splitVersion(%q) = %q, %q; want %q, %q", tt.arg, class, version, tt.class, tt.version)
		}
	}
}
