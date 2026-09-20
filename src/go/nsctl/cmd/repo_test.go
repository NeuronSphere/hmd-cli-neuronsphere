package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/artifact"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
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

// A role with one target is written as a bare string, which is how a person
// writes it; several targets become the list form the schema also allows.
func TestDeclarationForSpellsDependenciesNaturally(t *testing.T) {
	t.Parallel()

	r := declarationFor(msdeploy.DeployedInstance{
		Name: "trino", RepoClassName: "hmd-inf-trino", RepoClassVersion: "0.1.225",
		InstanceConfiguration: map[string]any{"workers": 2},
		Dependencies: map[string][]string{
			"metastore": {"hive-metastore"},
			"compute":   {"local-neuronsphere", "other"},
		},
	})

	if r.InstanceName != "trino" || r.RepoClassName != "hmd-inf-trino" || r.Version != "0.1.225" {
		t.Errorf("declaration = %+v", r)
	}
	if r.Dependencies["metastore"] != "hive-metastore" {
		t.Errorf("single dependency = %#v, want a bare string", r.Dependencies["metastore"])
	}
	list, ok := r.Dependencies["compute"].([]any)
	if !ok || len(list) != 2 {
		t.Errorf("multiple dependencies = %#v, want a list", r.Dependencies["compute"])
	}
	if r.InstanceConfiguration["workers"] != 2 {
		t.Errorf("configuration = %v", r.InstanceConfiguration)
	}
}

func TestDeclarationForOmitsEmptyDependencies(t *testing.T) {
	t.Parallel()

	r := declarationFor(msdeploy.DeployedInstance{Name: "a", RepoClassName: "hmd-ms-a"})
	if r.Dependencies != nil {
		t.Errorf("dependencies = %v, want none written", r.Dependencies)
	}
}

func TestStatusWord(t *testing.T) {
	t.Parallel()

	if got := statusWord(msdeploy.StatusNeverDeployed); got != "never deployed here" {
		t.Errorf("statusWord(never) = %q", got)
	}
	if got := statusWord("FAILED"); got != "failed" {
		t.Errorf("statusWord(FAILED) = %q", got)
	}
}

// Import needs a deployment service; without one there is nothing to read and
// saying so beats writing an empty manifest.
func TestRepoImportRefusesWithoutTheDeploymentService(t *testing.T) {
	t.Parallel()

	_, env := repoEnv(t)
	_, _, err := run(t, fakeEnv(env), "repo", "import")
	if err == nil {
		t.Fatal("import succeeded with no deployment service")
	}
	if !strings.Contains(err.Error(), "not answering") {
		t.Errorf("error = %v, want it to say the service is unreachable", err)
	}
}

// The acceptance criterion NERD005 states as "`nsctl status` distinguishes
// '0.1.4 from an artifact' from '0.1.4 from a working tree'". The declaration
// cannot say it -- both read `manifest` -- so the listing resolves, and FROM is
// the column that carries the answer.
//
// It runs with nothing at all running: repoEnv points the deployment service at
// a closed port, so this also pins that resolution never turns a listing into an
// error.
func TestRepoListSaysWhereEachVersionCameFrom(t *testing.T) {
	t.Parallel()

	home, env := repoEnv(t)
	// Same version as the cached artifact, so the FROM column is the only thing
	// that can tell the two apart. No declared version: the checkout's own
	// meta-data/VERSION is what a bare `nsctl repo add` resolves through.
	tree := filepath.Join(env["HMD_REPO_HOME"], "hmd-ms-tree")
	writeFile(t, filepath.Join(tree, "meta-data", "VERSION"), "0.1.4")

	if _, err := artifact.Store(home, "hmd-inf-cached", "0.1.4",
		artifactZip(t, "hmd-inf-cached", "0.1.4", "cached")); err != nil {
		t.Fatal(err)
	}

	m := &manifest.Manifest{Version: manifest.Version, Name: "local", Repos: []manifest.Repo{
		{InstanceName: "cached", RepoClassName: "hmd-inf-cached", Version: "0.1.4",
			Source: &manifest.Source{Type: manifest.SourceArtifact}},
		{InstanceName: "uncached", RepoClassName: "hmd-inf-uncached", Version: "0.2.0",
			Source: &manifest.Source{Type: manifest.SourceArtifact}},
		{InstanceName: "ms-tree", RepoClassName: "hmd-ms-tree"},
	}}
	if err := m.Save(manifest.DefaultPath(home, "local")); err != nil {
		t.Fatal(err)
	}

	out, stderr, err := run(t, fakeEnv(env), "repo", "list")
	if err != nil {
		t.Fatalf("repo list: %v", err)
	}
	if !strings.Contains(stderr, "not answering") {
		t.Errorf("stderr does not explain the missing status: %q", stderr)
	}
	rows := listRows(t, out)

	for _, tc := range []struct {
		instance, version, declared, from string
	}{
		// An artifact whose bytes are here, and a checkout of the same version:
		// identical but for FROM, which is the whole point.
		{instance: "cached", version: "0.1.4", declared: "manifest", from: "artifact"},
		{instance: "ms-tree", version: "0.1.4", declared: "manifest", from: "working-tree"},
		// Declared but never pulled. The version is known -- an artifact is
		// addressed by version -- and the bytes are not, which is the state
		// `nsctl artifact pull` fixes.
		{instance: "uncached", version: "0.2.0", declared: "manifest", from: "artifact (uncached)"},
		{instance: "eks-cluster", version: "-", declared: "substrate", from: "-"},
	} {
		t.Run(tc.instance, func(t *testing.T) {
			t.Parallel()

			row, ok := rows[tc.instance]
			if !ok {
				t.Fatalf("no row for %s:\n%s", tc.instance, out)
			}
			if row["VERSION"] != tc.version {
				t.Errorf("VERSION = %q, want %q", row["VERSION"], tc.version)
			}
			if row["DECLARED"] != tc.declared {
				t.Errorf("DECLARED = %q, want %q", row["DECLARED"], tc.declared)
			}
			if row["FROM"] != tc.from {
				t.Errorf("FROM = %q, want %q", row["FROM"], tc.from)
			}
		})
	}

	if rows["cached"]["VERSION"] != rows["ms-tree"]["VERSION"] {
		t.Fatal("the fixture no longer pins the two to one version, so FROM is not what distinguishes them")
	}
	if rows["cached"]["FROM"] == rows["ms-tree"]["FROM"] {
		t.Errorf("an artifact and a working tree read the same in FROM:\n%s", out)
	}
	// Named with the version, because that is what `pull` takes.
	if !strings.Contains(out, "nsctl artifact pull hmd-inf-uncached@0.2.0:build") {
		t.Errorf("output does not name the command that fetches the missing artifact:\n%s", out)
	}
}

// listRows parses `repo list`'s table into one field map per instance, keyed by
// the header. Reading columns by name is what keeps these assertions about the
// contents rather than about the column order -- and splitting on the padding
// rather than on whitespace is what keeps "REPO CLASS" and "artifact
// (uncached)" one cell each.
func listRows(t *testing.T, out string) map[string]map[string]string {
	t.Helper()

	padding := regexp.MustCompile(`\s{2,}`)
	rows := map[string]map[string]string{}
	var header []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, " \t")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := padding.Split(line, -1)
		if fields[0] == "INSTANCE" {
			header = fields
			continue
		}
		if header == nil || len(fields) != len(header) {
			continue
		}
		row := map[string]string{}
		for i, name := range header {
			row[name] = fields[i]
		}
		rows[fields[0]] = row
	}
	if header == nil {
		t.Fatalf("no table in the output:\n%s", out)
	}
	return rows
}

// A relative --path is written absolute. The manifest is read by `env apply`
// from wherever it is run, and by the runner as a bind-mount source resolved
// by the Docker daemon -- which rejected `platform/warehouse` as a volume
// name on the first Demo 0 run. A path carrying a variable is left for
// RepoPath to expand.
func TestRepoAddAbsolutisesARelativePath(t *testing.T) {
	t.Parallel()

	_, env := repoEnv(t, "acme-platform-warehouse")
	if _, _, err := run(t, fakeEnv(env), "repo", "add", "acme-platform-warehouse", "--path", "."); err != nil {
		t.Fatalf("repo add: %v", err)
	}
	want, _ := filepath.Abs(".")
	if got := readManifest(t, env, "local").Repos[0].Source.Path; got != want {
		t.Errorf("source.path = %q, want %q", got, want)
	}

	_, env = repoEnv(t, "acme-platform-runner")
	env["ACME_HOME"] = want
	if _, _, err := run(t, fakeEnv(env), "repo", "add", "acme-platform-runner", "--path", "$ACME_HOME/."); err != nil {
		t.Fatalf("repo add: %v", err)
	}
	if got := readManifest(t, env, "local").Repos[0].Source.Path; got != "$ACME_HOME/." {
		t.Errorf("a path with a variable was rewritten to %q", got)
	}
}
