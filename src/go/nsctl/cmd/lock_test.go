package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/lock"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// lockRepo writes a BACON manifest into a fresh directory and returns it.
func lockRepo(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "meta-data", "manifest.json"), body)
	return dir
}

// Exact specifiers throughout, so tier two resolves the whole thing with no
// environment and no network -- which is what `nsctl lock` has to do on a
// machine that has never deployed anything.
const lockable = `{
  "name": "hmd-ms-myapi",
  "deploy": {"dependencies": {
    "app-store": {"repo_class_name": "hmd-inf-s3bucket", "required": "true", "version_spec": "0.1.13"}
  }},
  "local": {"version": 1, "repos": [
    {"instance_name": "cache", "repo_class_name": "hmd-inf-redis", "version_spec": "0.2.1"},
    {"instance_name": "ms-transform", "repo_class_name": "hmd-ms-transform", "version_spec": "0.5.201",
     "profiles": ["transforms"]}
  ]}
}`

func TestLockWritesEveryProfilesEntries(t *testing.T) {
	t.Parallel()

	repo := lockRepo(t, lockable)
	out, _, err := run(t, fakeEnv(nil), "lock", repo)
	if err != nil {
		t.Fatalf("lock: %v", err)
	}

	l, err := lock.Read(repo)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if l.RepoClassName != "hmd-ms-myapi" || l.GeneratedFrom != "pins" {
		t.Errorf("header = %+v", l)
	}
	// The gated companion is pinned too: activation is a read-time filter, and a
	// lock covering only today's profile would force a re-resolve -- a network
	// trip, and a different answer -- the first time anybody switched.
	if len(l.Resolved) != 3 {
		t.Fatalf("pinned %d entries, want all 3 whatever profile gates them", len(l.Resolved))
	}
	if _, ok := l.Entry("hmd-ms-transform"); !ok {
		t.Error("the profile-gated companion was not pinned")
	}
	if !strings.Contains(out, lock.FileName) {
		t.Errorf("output does not name the file it wrote:\n%s", out)
	}
}

// SPEC003's message, which is the whole product when tier three does not exist:
// it names the specifier it could not resolve and both remedies.
func TestLockRefusesARangeAndNamesBothRemedies(t *testing.T) {
	t.Parallel()

	repo := lockRepo(t, `{"name":"hmd-ms-myapi","deploy":{"dependencies":{
		"transform": {"repo_class_name":"hmd-ms-transform","required":"true","version_spec":"~= 0.5"}}}}`)
	_, _, err := run(t, fakeEnv(nil), "lock", repo)
	if err == nil {
		t.Fatal("succeeded, want a refusal")
	}
	for _, s := range []string{"~= 0.5", "hmd-ms-transform", "--pin", "--from-env"} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("error does not mention %q:\n%s", s, err)
		}
	}
	if got := nserr.CodeOf(err); got != nserr.Usage {
		t.Errorf("exit code = %d, want %d (usage): the fix is a flag", got, nserr.Usage)
	}
	// Nothing was written, so a failed run leaves no half-lock behind.
	if _, statErr := os.Stat(lock.Path(repo)); !os.IsNotExist(statErr) {
		t.Error("a refused lock still wrote a file")
	}
}

// Every failure at once. One per run would mean one edit per run on a manifest
// with several ranges, which every real manifest has.
func TestLockReportsEveryUnresolvedEntry(t *testing.T) {
	t.Parallel()

	repo := lockRepo(t, `{"name":"x","deploy":{"dependencies":{
		"a": {"repo_class_name":"hmd-ms-alpha","required":"true","version_spec":"~= 0.5"},
		"b": {"repo_class_name":"hmd-ms-bravo","required":"true","version_spec":">= 0.2"}}}}`)
	_, _, err := run(t, fakeEnv(nil), "lock", repo)
	if err == nil {
		t.Fatal("succeeded, want a refusal")
	}
	for _, s := range []string{"hmd-ms-alpha", "hmd-ms-bravo"} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("error does not name %q:\n%s", s, err)
		}
	}
}

func TestLockPinResolvesARange(t *testing.T) {
	t.Parallel()

	repo := lockRepo(t, `{"name":"x","deploy":{"dependencies":{
		"transform": {"repo_class_name":"hmd-ms-transform","required":"true","version_spec":"~= 0.5"}}}}`)
	if _, _, err := run(t, fakeEnv(nil), "lock", repo, "--pin", "hmd-ms-transform@0.5.201"); err != nil {
		t.Fatalf("lock --pin: %v", err)
	}
	l, err := lock.Read(repo)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := l.Entry("hmd-ms-transform")
	if !ok || e.Version != "0.5.201" {
		t.Errorf("entry = %+v, %v", e, ok)
	}
	// A dependency records the role it was resolved for; a companion does not.
	if len(e.Satisfies) != 1 || e.Satisfies[0] != "transform" {
		t.Errorf("satisfies = %v, want the role", e.Satisfies)
	}
}

func TestLockPinRefusals(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct{ name, pin, want string }{
		{"no version", "hmd-ms-transform", "@"},
		{"no class", "@0.5.201", "@"},
		{"a class nothing declares", "hmd-ms-nowhere@0.1.0", "does not declare"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			repo := lockRepo(t, lockable)
			_, _, err := run(t, fakeEnv(nil), "lock", repo, "--pin", tt.pin)
			if err == nil {
				t.Fatal("succeeded, want a refusal")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not mention %q", err, tt.want)
			}
		})
	}
}

// --check writes nothing, in every outcome. That is not a style preference: a
// validate verb that rewrites a file cannot be run on a dirty tree, which is
// exactly when it is wanted.
func TestLockCheckWritesNothing(t *testing.T) {
	t.Parallel()

	repo := lockRepo(t, lockable)
	if _, _, err := run(t, fakeEnv(nil), "lock", repo); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(lock.Path(repo))
	if err != nil {
		t.Fatal(err)
	}

	// Current: succeeds.
	if _, _, err := run(t, fakeEnv(nil), "lock", repo, "--check"); err != nil {
		t.Errorf("--check on a current lock: %v", err)
	}
	// Stale: fails. The manifest grew a want the lock has never seen.
	writeFile(t, filepath.Join(repo, "meta-data", "manifest.json"),
		`{"name":"hmd-ms-myapi","deploy":{"dependencies":{
			"added": {"repo_class_name":"hmd-inf-added","required":"true","version_spec":"0.1.0"}}}}`)
	_, _, err = run(t, fakeEnv(nil), "lock", repo, "--check")
	if err == nil {
		t.Error("--check passed a stale lock")
	}

	after, readErr := os.ReadFile(lock.Path(repo))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(before) != string(after) {
		t.Errorf("--check modified the lock:\nbefore %s\nafter  %s", before, after)
	}
}

func TestLockCheckFindings(t *testing.T) {
	t.Parallel()

	// Pinned: hmd-inf-kept and hmd-inf-dropped. Declared: hmd-inf-kept and
	// hmd-inf-added. One finding in each direction, and they are not the same
	// severity.
	repo := lockRepo(t, `{"name":"x","deploy":{"dependencies":{
		"kept":    {"repo_class_name":"hmd-inf-kept","required":"true","version_spec":"0.1.0"},
		"dropped": {"repo_class_name":"hmd-inf-dropped","required":"true","version_spec":"0.1.0"}}}}`)
	if _, _, err := run(t, fakeEnv(nil), "lock", repo); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(repo, "meta-data", "manifest.json"), `{"name":"x","deploy":{"dependencies":{
		"kept":  {"repo_class_name":"hmd-inf-kept","required":"true","version_spec":"0.1.0"},
		"added": {"repo_class_name":"hmd-inf-added","required":"true","version_spec":"0.1.0"}}}}`)

	_, stderr, err := run(t, fakeEnv(nil), "lock", repo, "--check")
	if err == nil {
		t.Fatal("--check passed a stale lock")
	}
	if !strings.Contains(err.Error(), "hmd-inf-added") {
		t.Errorf("the error does not name the unpinned want:\n%s", err)
	}
	if !strings.Contains(err.Error(), "nsctl lock") {
		t.Errorf("the error does not name the fix:\n%s", err)
	}
	// Stale in the other direction is a warning, because a developer mid-refactor
	// should not be blocked by it.
	if !strings.Contains(stderr, "hmd-inf-dropped") {
		t.Errorf("the no-longer-declared entry is not warned about:\n%s", stderr)
	}
	if strings.Contains(err.Error(), "hmd-inf-dropped") {
		t.Errorf("the no-longer-declared entry was raised as an error:\n%s", err)
	}
}

func TestLockCheckOnARepoWithNoLock(t *testing.T) {
	t.Parallel()

	_, _, err := run(t, fakeEnv(nil), "lock", lockRepo(t, lockable), "--check")
	if err == nil {
		t.Fatal("succeeded, want a refusal")
	}
	if !strings.Contains(err.Error(), "nsctl lock") {
		t.Errorf("error %q does not name the command that writes one", err)
	}
}

// A lock nsctl cannot read is refused whole, and --check is where a CI job meets
// that first.
func TestLockCheckRefusesAnUnknownSchemaVersion(t *testing.T) {
	t.Parallel()

	repo := lockRepo(t, lockable)
	writeFile(t, lock.Path(repo), "version = 7\nrepo_class_name = \"x\"\n")

	_, _, err := run(t, fakeEnv(nil), "lock", repo, "--check")
	if err == nil {
		t.Fatal("succeeded, want a refusal")
	}
	if !strings.Contains(err.Error(), "7") {
		t.Errorf("error %q does not name the version it found", err)
	}
	if got := nserr.CodeOf(err); got != nserr.Usage {
		t.Errorf("exit code = %d, want %d (usage)", got, nserr.Usage)
	}
}

// A repository with neither a local section nor any dependency still locks --
// to an empty lock, which is a true statement and a valid input to `env add`.
func TestLockOnARepoThatDeclaresNothing(t *testing.T) {
	t.Parallel()

	repo := lockRepo(t, `{"name":"hmd-ms-bare"}`)
	if _, _, err := run(t, fakeEnv(nil), "lock", repo); err != nil {
		t.Fatalf("lock: %v", err)
	}
	l, err := lock.Read(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Resolved) != 0 {
		t.Errorf("pinned %d entries from a manifest that declares none", len(l.Resolved))
	}
}

// The two ways a --pin lands on nothing have different fixes, so the message
// tells them apart. An optional dependency the `local` section does not gate is
// declared and deliberately not wanted locally -- reporting that as "does not
// declare" sends the reader to fix a spelling that is already right.
func TestLockPinOnAnUngatedOptionalDependency(t *testing.T) {
	t.Parallel()

	repo := lockRepo(t, `{"name":"x","deploy":{"dependencies":{
		"metastore": {"repo_class_name":"hmd-inf-hive-metastore","required":"false","version_spec":"~= 0.1"}}}}`)
	_, _, err := run(t, fakeEnv(nil), "lock", repo, "--pin", "hmd-inf-hive-metastore@0.1.23")
	if err == nil {
		t.Fatal("succeeded, want a refusal")
	}
	if !strings.Contains(err.Error(), "local.dependencies") {
		t.Errorf("error does not name the fix (gate it):\n%s", err)
	}
	if strings.Contains(err.Error(), "does not declare") {
		t.Errorf("error reports a declared class as undeclared:\n%s", err)
	}
}

// An optional dependency the `local` section does not gate is never pinned
// either, so a range on one is not a reason `nsctl lock` cannot run.
func TestLockIgnoresARangeOnAnUngatedOptionalDependency(t *testing.T) {
	t.Parallel()

	repo := lockRepo(t, `{"name":"x","deploy":{"dependencies":{
		"kept":      {"repo_class_name":"hmd-inf-kept","required":"true","version_spec":"0.1.0"},
		"metastore": {"repo_class_name":"hmd-inf-hive-metastore","required":"false","version_spec":"~= 0.1"}}}}`)
	if _, _, err := run(t, fakeEnv(nil), "lock", repo); err != nil {
		t.Fatalf("lock: %v -- an unwanted optional dependency's range should not block it", err)
	}
	l, err := lock.Read(repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := l.Entry("hmd-inf-hive-metastore"); ok {
		t.Error("pinned an optional dependency nothing wants locally")
	}
}

// "Generate one with `nsctl lock`" belongs only to a repository that has no
// lock. A lock that exists but this binary cannot parse carries its own remedy,
// and following the generate advice would overwrite a file whose only problem is
// that nsctl is too old for it.
func TestLockCheckDoesNotSuggestGeneratingOverAnUnreadableLock(t *testing.T) {
	t.Parallel()

	repo := lockRepo(t, lockable)
	writeFile(t, lock.Path(repo), "version = 7\nrepo_class_name = \"x\"\n")
	_, _, err := run(t, fakeEnv(nil), "lock", repo, "--check")
	if err == nil {
		t.Fatal("succeeded, want a refusal")
	}
	if strings.Contains(err.Error(), "Generate one with") {
		t.Errorf("advises generating over a lock that exists:\n%s", err)
	}

	// And it still says it for a repository that genuinely has none.
	_, _, err = run(t, fakeEnv(nil), "lock", lockRepo(t, lockable), "--check")
	if err == nil || !strings.Contains(err.Error(), "Generate one with") {
		t.Errorf("a repository with no lock is not told to generate one: %v", err)
	}
}

// A class whose role is bound to what the environment provides is never
// deployed, so a version for it is a version of nothing.
func TestLockPinOnABoundRole(t *testing.T) {
	t.Parallel()

	repo := lockRepo(t, `{
  "name": "hmd-ms-myapi",
  "deploy": {"dependencies": {
    "compute":   {"repo_class_name": "hmd-inf-eks-node-group", "required": "true", "version_spec": "~= 0.1"},
    "app-store": {"repo_class_name": "hmd-inf-s3bucket",       "required": "true", "version_spec": "0.1.13"}
  }},
  "local": {"version": 1, "dependencies": {"compute": {"bind": "local-neuronsphere"}}}
}`)
	_, _, err := run(t, fakeEnv(nil), "lock", repo, "--pin", "hmd-inf-eks-node-group@0.1.4")
	if err == nil {
		t.Fatal("succeeded, want a refusal")
	}
	if !strings.Contains(err.Error(), "bound to") {
		t.Errorf("error %q does not say the role is bound", err)
	}

	// Without the pin the range on the bound class is no obstacle: it is
	// skipped, and the exact specifier settles the rest.
	if _, _, err := run(t, fakeEnv(nil), "lock", repo); err != nil {
		t.Fatalf("lock: %v", err)
	}
}
