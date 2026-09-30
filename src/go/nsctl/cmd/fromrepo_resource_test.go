package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/artifact"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
)

// NERD010 SPEC009: roles the lock cannot pin -- external ones, and ones that
// name a resource type instead of a repo class -- are filled from the
// environment, never by demanding a pin.

// A foreign repository that names what it needs by type only: the shape
// `add-dependency --resource-*` plus `local require` writes, and the one the
// onboarding principles call for.
const resourceOnlyManifest = `{
  "name": "jaffle-shop-cloud",
  "deploy": {"dependencies": {
    "warehouse": {"required": "false", "resource": {
      "resource_namespace": "database.neuronsphere.io",
      "resource_definition_name": "database-account", "version": "0.1.0"}}
  }},
  "local": {"version": 1, "dependencies": {
    "warehouse": {"external": true, "profiles": ["warehouse"]}
  }}
}`

// foreignRepo writes a manifest and generates its lock, which for these
// fixtures pins nothing.
func foreignRepo(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "meta-data", "manifest.json"), body)
	writeFile(t, filepath.Join(dir, "meta-data", "VERSION"), "0.1")
	if _, _, err := run(t, fakeEnv(nil), "lock", dir); err != nil {
		t.Fatalf("lock: %v", err)
	}
	return dir
}

func TestLockAcceptsAResourceOnlyRole(t *testing.T) {
	t.Parallel()

	repo := foreignRepo(t, resourceOnlyManifest)
	out, _, err := run(t, fakeEnv(nil), "lock", "--check", repo)
	if err != nil {
		t.Fatalf("lock --check: %v", err)
	}
	if !strings.Contains(out, "0 pinned") {
		t.Errorf("lock --check pinned something for a role naming no class:\n%s", out)
	}
}

func TestEnvAddLeavesAnOptionalResourceRoleUnfilled(t *testing.T) {
	t.Parallel()

	home, env := fromRepoEnv(t)
	repo := foreignRepo(t, resourceOnlyManifest)

	_, stderr, err := run(t, fakeEnv(env), "env", "add", "scratch", "--from-repo", repo,
		"--no-pull", "--profile", "warehouse")
	if err != nil {
		t.Fatalf("env add: %v", err)
	}
	if !strings.Contains(stderr, "database.neuronsphere.io/database-account") ||
		!strings.Contains(stderr, "left unfilled") {
		t.Errorf("no note naming the unfilled role:\n%s", stderr)
	}
	m := loadEnv(t, home, "scratch")
	if got := instanceNames(m); len(got) != 1 {
		t.Errorf("declared %v, want only the subject", got)
	}
	if _, ok := subjectDeps(t, m)["warehouse"]; ok {
		t.Error("the subject is wired to a warehouse nothing provides")
	}
}

func TestEnvAddRefusesARequiredResourceRoleNothingProvides(t *testing.T) {
	t.Parallel()

	_, env := fromRepoEnv(t)
	// A required role cannot be profile-gated, so the gate loses its profiles.
	repo := foreignRepo(t, strings.Replace(strings.Replace(resourceOnlyManifest,
		`"required": "false"`, `"required": "true"`, 1),
		`, "profiles": ["warehouse"]`, ``, 1))

	_, _, err := run(t, fakeEnv(env), "env", "add", "scratch", "--from-repo", repo, "--no-pull")
	if err == nil {
		t.Fatal("succeeded, want a refusal")
	}
	for _, s := range []string{"required", "database.neuronsphere.io/database-account", "--name warehouse="} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("error does not contain %q:\n%s", s, err)
		}
	}
}

func TestEnvAddBindsAResourceRoleByName(t *testing.T) {
	t.Parallel()

	home, env := fromRepoEnv(t)
	repo := foreignRepo(t, resourceOnlyManifest)

	if _, _, err := run(t, fakeEnv(env), "env", "add", "scratch", "--from-repo", repo,
		"--no-pull", "--profile", "warehouse", "--name", "warehouse=team-db"); err != nil {
		t.Fatalf("env add: %v", err)
	}
	m := loadEnv(t, home, "scratch")
	if got := subjectDeps(t, m)["warehouse"]; got != "team-db" {
		t.Errorf("warehouse bound to %q, want team-db", got)
	}
	if contains(instanceNames(m), "team-db") {
		t.Error("declared the bound instance; the environment provides it")
	}
}

func TestEnvApplyFillsAResourceRoleFromAProducer(t *testing.T) {
	t.Parallel()

	home, env := fromRepoEnv(t)
	if _, err := artifact.Store(home, "hmd-database-account", "0.1.7",
		producingZip(t, "hmd-database-account", "0.1.7", "database.neuronsphere.io", "database-account")); err != nil {
		t.Fatal(err)
	}
	declareArtifact(t, home, env, "shared-db", "hmd-database-account", "0.1.7")
	repo := foreignRepo(t, resourceOnlyManifest)

	runApplyFromRepo(t, env, "local", "--from-repo", repo, "--profile", "warehouse")

	m := loadEnv(t, home, "local")
	if got := subjectDeps(t, m)["warehouse"]; got != "shared-db" {
		t.Errorf("warehouse bound to %q, want the producer shared-db", got)
	}
}

// D2: `lock --check` skips a profile-gated external role, and so must
// `env add --profile`, instead of demanding a pin the lock rightly lacks.
func TestEnvAddDoesNotPinAProfileGatedExternalRole(t *testing.T) {
	t.Parallel()

	_, env := fromRepoEnv(t)
	repo := foreignRepo(t, `{
  "name": "de-project-template",
  "deploy": {"dependencies": {
    "orchestrator": {"repo_class_name": "hmd-app-airflow", "required": "false", "version_spec": "~= 0.1"}
  }},
  "local": {"version": 1, "dependencies": {
    "orchestrator": {"external": true, "profiles": ["platform"]}
  }}
}`)
	if _, _, err := run(t, fakeEnv(env), "lock", "--check", repo); err != nil {
		t.Fatalf("lock --check: %v", err)
	}
	_, stderr, err := run(t, fakeEnv(env), "env", "add", "scratch", "--from-repo", repo,
		"--no-pull", "--profile", "platform")
	if err != nil {
		t.Fatalf("env add --profile platform: %v", err)
	}
	if !strings.Contains(stderr, "hmd-app-airflow") {
		t.Errorf("no note about the unfilled external role:\n%s", stderr)
	}
}

// D7: a repository whose wants need no pin needs no lock. jaffle-shop-duckdb
// is the shape: a manifest, no dependencies, no companions.
func TestARepositoryThatPinsNothingNeedsNoLock(t *testing.T) {
	t.Parallel()

	home, env := fromRepoEnv(t)
	for name, body := range map[string]string{
		"declares nothing": `{"name": "jaffle-shop-duckdb"}`,
		"requires by type": resourceOnlyManifest,
	} {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "meta-data", "manifest.json"), body)

		out, _, err := run(t, fakeEnv(env), "lock", "--check", dir)
		if err != nil || !strings.Contains(out, "no neuronsphere.lock is needed") {
			t.Errorf("%s: lock --check: %v\n%s", name, err, out)
		}
		slug := strings.ReplaceAll(name, " ", "-")
		if _, _, err := run(t, fakeEnv(env), "env", "add", slug, "--from-repo", dir, "--no-pull"); err != nil {
			t.Errorf("%s: env add --from-repo: %v", name, err)
			continue
		}
		if got := instanceNames(loadEnv(t, home, slug)); len(got) != 1 {
			t.Errorf("%s: declared %v, want only the subject", name, got)
		}
		if _, err := os.Stat(filepath.Join(dir, "neuronsphere.lock")); err == nil {
			t.Errorf("%s: a lock was written into the repository", name)
		}
	}
}

// D3: deleting an environment removes its manifest, so a second repository
// onboarded under the same name starts empty instead of inheriting the first
// one's instances. A manifest deliberately left behind is adopted only when
// asked.
func TestEnvDeleteThenAddDoesNotInheritInstances(t *testing.T) {
	t.Parallel()

	home, env := fromRepoEnv(t)
	first := subjectRepo(t, home, subjectManifest)
	second := foreignRepo(t, `{"name": "jaffle-shop-duckdb"}`)

	if _, _, err := run(t, fakeEnv(env), "env", "add", "ob-0", "--from-repo", first, "--no-pull"); err != nil {
		t.Fatal(err)
	}
	out, _, err := run(t, fakeEnv(env), "env", "delete", "ob-0", "--yes")
	if err != nil || !strings.Contains(out, "Removed") {
		t.Fatalf("env delete: %v\n%s", err, out)
	}
	if _, _, err := run(t, fakeEnv(env), "env", "add", "ob-0", "--from-repo", second, "--no-pull"); err != nil {
		t.Fatal(err)
	}
	if got := instanceNames(loadEnv(t, home, "ob-0")); len(got) != 1 || got[0] != "jaffle-shop-duckdb" {
		t.Errorf("declared %v, want only the second repository", got)
	}

	// --keep-manifest leaves it, and env add then refuses until --adopt.
	if _, _, err := run(t, fakeEnv(env), "env", "delete", "ob-0", "--yes", "--keep-manifest"); err != nil {
		t.Fatal(err)
	}
	_, _, err = run(t, fakeEnv(env), "env", "add", "ob-0", "--from-repo", first, "--no-pull")
	if err == nil || !strings.Contains(err.Error(), "--adopt") {
		t.Fatalf("an orphaned manifest was adopted silently: %v", err)
	}
	if _, _, err := run(t, fakeEnv(env), "env", "add", "ob-0", "--from-repo", first, "--no-pull", "--adopt"); err != nil {
		t.Fatalf("--adopt: %v", err)
	}
	if got := instanceNames(loadEnv(t, home, "ob-0")); !contains(got, "jaffle-shop-duckdb") {
		t.Errorf("--adopt dropped the kept manifest's instances: %v", got)
	}
}

// D4: a repository can be planned with no control plane, and planning it
// registers nothing and writes nothing.
func TestEnvPlanFromRepoIsOfflineAndWritesNothing(t *testing.T) {
	t.Parallel()

	home, env := fromRepoEnv(t)
	repo := subjectRepo(t, home, subjectManifest)
	regBefore, err := os.ReadFile(registry.Path(home))
	if err != nil {
		t.Fatal(err)
	}

	out, _, err := run(t, fakeEnv(env), "env", "plan", "--from-repo", repo, "--lean")
	if err != nil {
		t.Fatalf("env plan --from-repo: %v", err)
	}
	for _, s := range []string{"offline; nothing written", "ms-myapi", "cache", "Profiles: none (lean)"} {
		if !strings.Contains(out, s) {
			t.Errorf("plan does not mention %q:\n%s", s, out)
		}
	}
	regAfter, _ := os.ReadFile(registry.Path(home))
	if string(regBefore) != string(regAfter) {
		t.Error("planning changed the registry")
	}
	if entries, _ := os.ReadDir(filepath.Join(home, "environments")); len(entries) != 0 {
		t.Errorf("planning wrote %d environment manifest(s)", len(entries))
	}
}

// An environment `env delete` unregistered leaves its Floci resources in its
// account, so that account is retired: the next environment gets a fresh one
// rather than inheriting a VPC whose subnet group already exists.
func TestEnvDeleteRetiresTheAccount(t *testing.T) {
	t.Parallel()

	home, env := fromRepoEnv(t)
	dev, _ := loadReg(t, home).Environment("dev", nil)
	if _, _, err := run(t, fakeEnv(env), "env", "delete", "dev", "--yes"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, fakeEnv(env), "env", "add", "fresh"); err != nil {
		t.Fatal(err)
	}
	reg := loadReg(t, home)
	fresh, _ := reg.Environment("fresh", nil)
	if fresh.AccountID == dev.AccountID {
		t.Errorf("the new environment was given %s, the deleted environment's account", dev.AccountID)
	}
	if !contains(reg.RetiredAccounts, dev.AccountID) {
		t.Errorf("retired accounts = %v, want %s", reg.RetiredAccounts, dev.AccountID)
	}
}
