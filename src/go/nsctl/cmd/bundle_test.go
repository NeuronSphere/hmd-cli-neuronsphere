package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/localspec"
)

// NERD0019 SPEC0006: `nsctl bundle` is the verb; `nsctl stack` still works,
// is hidden, and says so.

func TestBundleIsTheVerbAndStackIsAHiddenDeprecatedAlias(t *testing.T) {
	out, _, err := run(t, fakeEnv(nil), "--help")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "\n  bundle ") {
		t.Errorf("root help does not list bundle:\n%s", out)
	}
	if strings.Contains(out, "\n  stack ") {
		t.Errorf("root help still lists stack:\n%s", out)
	}

	out, _, err = run(t, fakeEnv(nil), "bundle", "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, sub := range []string{"add", "list", "remove", "init", "build", "push"} {
		if !strings.Contains(out, "\n  "+sub+" ") {
			t.Errorf("bundle help lacks %s:\n%s", sub, out)
		}
	}

	// Whether the environment exists does not matter: the warning comes first.
	out, errOut, _ := run(t, fakeEnv(nil), "--home", t.TempDir(), "stack", "list", "--env", "nowhere")
	if !strings.Contains(errOut, `use "nsctl bundle list"`) {
		t.Errorf("stack list should warn it is deprecated, got:\n%s%s", out, errOut)
	}
}

func TestBundleMigrateMovesTheLocalSectionIntoABundleFile(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "meta-data", "manifest.json")
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		t.Fatal(err)
	}
	original := `{
  "name": "hmd-stack-analytics",
  "description": "kept",
  "access": [{"name": "airflow", "instance": "airflow"}],
  "deploy": {"dependencies": {"compute": {"repo_class_name": "hmd-cli-neuronsphere", "required": "true"}}},
  "local": {"stack": true, "default_profiles": ["airflow"],
    "dependencies": {"compute": {"bind": "local-neuronsphere"}},
    "repos": [{"instance_name": "airflow", "repo_class_name": "hmd-app-airflow", "profiles": ["airflow"],
               "dependencies": {"compute": "local-neuronsphere"}}]}
}`
	if err := os.WriteFile(manifestPath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := localspec.Load(dir)
	if err != nil {
		t.Fatal(err)
	}

	out, _, err := run(t, fakeEnv(nil), "bundle", "migrate", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, filepath.Join("meta-data", "bundles", "analytics.json")) {
		t.Errorf("output does not name the bundle file:\n%s", out)
	}

	after, err := localspec.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if after.Bundle != "analytics" || len(after.Notices) != 0 {
		t.Errorf("after: bundle %q, notices %v", after.Bundle, after.Notices)
	}
	if len(after.Wants()) != len(before.Wants()) {
		t.Errorf("wants %d before, %d after", len(before.Wants()), len(after.Wants()))
	}
	var doc map[string]any
	data, _ := os.ReadFile(manifestPath)
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if _, has := doc["local"]; has {
		t.Error("the local section should be gone")
	}
	if deploy, _ := doc["deploy"].(map[string]any); deploy["dependencies"] != nil {
		t.Error("deploy.dependencies moved into the bundle's roles and should be gone")
	}
	if doc["description"] != "kept" || doc["access"] == nil {
		t.Errorf("unrelated keys lost: %v", doc)
	}

	if _, _, err := run(t, fakeEnv(nil), "bundle", "migrate", dir); err == nil {
		t.Error("migrating twice should refuse: there is no local section left")
	}
}
