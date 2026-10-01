package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// nsctl inspect runs every noun's section over the same repositories, and a
// section that cannot run says why without stopping the others.
func TestInspectRunsEverySection(t *testing.T) {
	t.Parallel()
	dir := inspectRepo(t)
	out, _, err := run(t, fakeEnv(nil), "inspect", dir)
	// The demo manifests name a repo class and nothing else, which validate
	// reports as errors, so the inspection fails, as a gate should.
	if err == nil {
		t.Errorf("an inspection with errors exited 0:\n%s", out)
	}
	for _, want := range []string{
		"Inspected 2 repositories",
		"REPOCLASS", "hmd-lang-demo (in lang)", "repoclass-validate",
		"INSTANCE", "skipped: HMD_HOME is not set",
		"MODEL", "demo.thing", "@trino:final @trino:staging",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("inspect lacks %q:\n%s", want, out)
		}
	}

	out, _, _ = run(t, fakeEnv(nil), "inspect", dir, "--only", "model", "--json")
	var doc struct {
		Sections []struct {
			Noun     string `json:"noun"`
			Findings []struct {
				Severity string `json:"severity"`
				Code     string `json:"code"`
			} `json:"findings"`
		} `json:"sections"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil || len(doc.Sections) != 1 || doc.Sections[0].Noun != "model" {
		t.Fatalf("--only model --json = %s (%v)", out, err)
	}
	if _, _, err := run(t, fakeEnv(nil), "inspect", dir, "--skip", "colour"); err == nil || !strings.Contains(err.Error(), "no section") {
		t.Errorf("an unknown section: %v", err)
	}
}

// A repository without a manifest is described by detection: what it can
// propose, and what it cannot decide.
func TestRepoClassInspectWithoutAManifest(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Makefile"), []byte("deploy:\n\techo hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, _ := run(t, fakeEnv(nil), "repoclass", "inspect", "--path", dir, "--info")
	for _, want := range []string{"no manifest", "repoclass-refused"} {
		if !strings.Contains(out, want) {
			t.Errorf("repoclass inspect lacks %q:\n%s", want, out)
		}
	}
}

// instance inspect reads the environment manifests for the repo classes it
// is pointed at, and a dependency wired to nothing declared is an error.
func TestInstanceInspectFindsUndeclaredWiring(t *testing.T) {
	t.Parallel()
	_, env := repoEnv(t, "hmd-ms-myapi", "hmd-ms-other")
	repo := filepath.Join(env["HMD_REPO_HOME"], "hmd-ms-myapi")
	if err := os.MkdirAll(filepath.Join(repo, "meta-data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "meta-data", "manifest.json"),
		[]byte(`{"name": "hmd-ms-myapi", "description": "an API"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, fakeEnv(env), "instance", "add", "hmd-ms-myapi",
		"--depends", "database-instance=environment-db", "--depends", "cache=nosuch-cache"); err != nil {
		t.Fatalf("instance add: %v", err)
	}
	out, _, err := run(t, fakeEnv(env), "instance", "inspect", repo)
	if err == nil {
		t.Errorf("an undeclared dependency target did not fail:\n%s", out)
	}
	for _, want := range []string{"local", "ms-myapi", "hmd-ms-myapi", "cache=nosuch-cache",
		"instance-dependency-undeclared", `wired to "nosuch-cache"`} {
		if !strings.Contains(out, want) {
			t.Errorf("instance inspect lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, `"environment-db"`) {
		t.Errorf("substrate reported as undeclared:\n%s", out)
	}
	if _, _, err := run(t, fakeEnv(nil), "instance", "inspect", repo); err == nil || !strings.Contains(err.Error(), "HMD_HOME") {
		t.Errorf("instance inspect without a home: %v", err)
	}
}
