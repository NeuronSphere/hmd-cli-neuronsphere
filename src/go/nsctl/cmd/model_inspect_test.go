package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// inspectRepo writes two repositories: one with an .hms noun, one with
// transforms that build a table in a staging and a final schema and load one
// from the other. Nothing declares a perspective: trino, its layers and which
// one is primary are derived (NERD033).
func inspectRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"lang/meta-data/manifest.json": `{"name": "hmd-lang-demo"}`,
		"lang/src/schemas/hmd_lang_demo/environment.hms": `{"name": "environment", "namespace": "hmd_lang_demo",
			"metatype": "noun", "attributes": {"type": {"type": "string", "description": ""}}}`,
		"tf/meta-data/manifest.json": `{"name": "hmd-config-demo"}`,
		"tf/src/transforms/ddl.yaml": `type: provider
config:
  provider_class: TrinoOperator
  params:
    sql: |
      CREATE TABLE IF NOT EXISTS demo_final.thing (id VARCHAR, at DATE)
      WITH (partitioned_by = ARRAY['at'])
  run_params: {}
`,
		"tf/src/transforms/load.yaml": `type: provider
config:
  provider_class: TrinoOperator
  params:
    sql: |
      CREATE TABLE IF NOT EXISTS demo_staging.thing (id VARCHAR, at DATE);
      INSERT INTO demo_final.thing SELECT id, at FROM demo_staging.thing
  run_params: {}
`,
	}
	for rel, body := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestInspectPrintsTheModelOfAParentDirectory(t *testing.T) {
	t.Parallel()
	dir := inspectRepo(t)
	out, errOut, err := run(t, fakeEnv(nil), "inspect", dir)
	if err != nil {
		t.Fatalf("inspect: %v\n%s", err, errOut)
	}
	for _, want := range []string{
		"Inspected 2 repositories",
		"hmd_lang_demo.environment  (noun, .hms)",
		"demo.thing  (noun)",
		"@trino:final demo_final.thing (table, primary)",
		"@trino:final DATE     partition",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if !strings.Contains(errOut, "HMD_HOME is not set") {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestInspectStoresSnapshotsUnderHome(t *testing.T) {
	t.Parallel()
	dir, home := inspectRepo(t), t.TempDir()
	out, _, err := run(t, fakeEnv(nil), "--home", home, "inspect", dir)
	if err != nil || !strings.Contains(out, "stored as snapshot #1") {
		t.Fatalf("first: %v\n%s", err, out)
	}
	out, _, err = run(t, fakeEnv(nil), "--home", home, "inspect", dir)
	if err != nil || !strings.Contains(out, "from snapshot #1") {
		t.Fatalf("second: %v\n%s", err, out)
	}
	out, _, err = run(t, fakeEnv(nil), "--home", home, "inspect", dir, "--refresh")
	if err != nil || !strings.Contains(out, "stored as snapshot #2") {
		t.Fatalf("refresh: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(home, ".cache", "neuronsphere", "inspect", "model.db")); err != nil {
		t.Errorf("store: %v", err)
	}
}

// The NERD032 change-detection loop: snapshot, edit, snapshot, diff.
func TestInspectDiffAfterAnEdit(t *testing.T) {
	t.Parallel()
	dir, home := inspectRepo(t), t.TempDir()
	if _, _, err := run(t, fakeEnv(nil), "--home", home, "inspect", "diff", dir); err == nil ||
		!strings.Contains(err.Error(), "nsctl inspect --refresh") {
		t.Fatalf("diff with no snapshots: %v", err)
	}
	if _, _, err := run(t, fakeEnv(nil), "--home", home, "inspect", dir, "--refresh"); err != nil {
		t.Fatal(err)
	}
	hmsPath := filepath.Join(dir, "lang/src/schemas/hmd_lang_demo/environment.hms")
	if err := os.WriteFile(hmsPath, []byte(`{"name": "environment", "namespace": "hmd_lang_demo", "metatype": "noun",
		"attributes": {"type": {"type": "string", "description": ""}, "region": {"type": "string", "required": false}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	ddl := filepath.Join(dir, "tf/src/transforms/ddl.yaml")
	data, _ := os.ReadFile(ddl)
	if err := os.WriteFile(ddl, []byte(strings.Replace(string(data), "id VARCHAR", "id BIGINT", 1)), 0o644); err != nil {
		t.Fatal(err)
	}

	out, _, err := run(t, fakeEnv(nil), "--home", home, "inspect", "diff", dir, "--live")
	if err != nil || !strings.Contains(out, "snapshot #1 -> working tree") {
		t.Fatalf("live diff: %v\n%s", err, out)
	}
	if _, _, err := run(t, fakeEnv(nil), "--home", home, "inspect", dir, "--refresh"); err != nil {
		t.Fatal(err)
	}
	out, _, err = run(t, fakeEnv(nil), "--home", home, "inspect", "diff", dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"4 model changes (snapshot #1 -> #2)",
		"info demo.thing#id [layer-type-change] integer in trino:final; string in trino:staging",
		"demo.thing#id\n  PropertyTypeChanged: string -> integer",
		"demo.thing#id@trino:final\n  PerspectiveValueChanged datatype: VARCHAR -> BIGINT",
		"hmd_lang_demo.environment#region\n  PropertyAdded: string, not required",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("diff lacks %q:\n%s", want, out)
		}
	}
}

// An exported core document and its sidecar, inspected as a language pack,
// give back the same noun with the same perspective values: the round trip
// through files is lossless.
func TestInspectExportRoundTripsThroughSidecars(t *testing.T) {
	t.Parallel()
	dir, outDir := inspectRepo(t), t.TempDir()
	if _, _, err := run(t, fakeEnv(nil), "inspect", dir, "demo.thing", "--out", outDir); err != nil {
		t.Fatal(err)
	}
	out, _, err := run(t, fakeEnv(nil), "inspect", outDir, "demo.thing")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"demo.thing  (noun, .hms)", "@trino:final demo_final.thing", "@trino:final DATE     partition"} {
		if !strings.Contains(out, want) {
			t.Errorf("re-inspected export lacks %q:\n%s", want, out)
		}
	}
}

func TestInspectPerspectiveListShowsDerivedOnes(t *testing.T) {
	t.Parallel()
	out, _, err := run(t, fakeEnv(nil), "inspect", "perspective", "list", inspectRepo(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"trino", "derived", "layer", "datatype,is_partition"} {
		if !strings.Contains(out, want) {
			t.Errorf("list lacks %q:\n%s", want, out)
		}
	}
	// Nothing is built in: with nothing to inspect there is nothing in effect.
	out, _, err = run(t, fakeEnv(nil), "inspect", "perspectives", "list", t.TempDir())
	if err != nil || strings.Contains(out, "trino") {
		t.Errorf("empty directory lists %q (%v)", out, err)
	}
}

func TestInspectPerspectiveDeriveExplainsItself(t *testing.T) {
	t.Parallel()
	out, _, err := run(t, fakeEnv(nil), "inspect", "perspective", "derive", inspectRepo(t), "--evidence")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"trino  (derived)",
		"binding key  layer: staging -> final",
		"name pattern <namespace>_<layer>.<name>",
		"date→timestamp",
		"the same table name in schemas that differ only in their last _-separated segment",
		"primary: the most downstream binding whose columns are declared",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("derive lacks %q:\n%s", want, out)
		}
	}
}

// Derive, edit, materialise, re-inspect: the materialised perspective is
// declared from then on, carries the edit, and gives back the same model.
func TestInspectPerspectiveEditAndMaterialise(t *testing.T) {
	t.Parallel()
	dir, home, lang := inspectRepo(t), t.TempDir(), t.TempDir()
	if _, _, err := run(t, fakeEnv(nil), "inspect", "perspective", "edit", "trino", "rename-key", "is_partition", "partition_key",
		"--path", dir); err == nil || !strings.Contains(err.Error(), "HMD_HOME") {
		t.Fatalf("edit without a home: %v", err)
	}
	if _, _, err := run(t, fakeEnv(nil), "--home", home, "inspect", "perspective", "edit", "trino", "drop-key", "colour",
		"--path", dir); err == nil || !strings.Contains(err.Error(), "does not apply") {
		t.Fatalf("an edit that does not apply: %v", err)
	}
	out, _, err := run(t, fakeEnv(nil), "--home", home, "inspect", "perspective", "edit", "trino", "rename-key", "is_partition", "partition_key",
		"--path", dir)
	if err != nil || !strings.Contains(out, "recorded") {
		t.Fatalf("edit: %v\n%s", err, out)
	}
	out, _, err = run(t, fakeEnv(nil), "--home", home, "inspect", "perspective", "derive", dir)
	if err != nil || !strings.Contains(out, "trino  (edited)") || !strings.Contains(out, "partition_key") {
		t.Fatalf("derive after the edit: %v\n%s", err, out)
	}
	if _, _, err := run(t, fakeEnv(nil), "--home", home, "inspect", "perspective", "materialise", "trino", dir); err == nil {
		t.Error("materialise without --to")
	}
	out, _, err = run(t, fakeEnv(nil), "--home", home, "inspect", "perspective", "materialise", "trino", dir, "--to", lang)
	if err != nil || !strings.Contains(out, "materialised trino: a definition and 1 sidecars") {
		t.Fatalf("materialise: %v\n%s", err, out)
	}
	for _, f := range []string{"src/perspectives/trino.perspective.json", "src/schemas/demo/thing.trino.hms"} {
		if _, err := os.Stat(filepath.Join(lang, f)); err != nil {
			t.Errorf("materialised: %v", err)
		}
	}
	// The materialised repository now declares trino: nothing is derived for
	// it, and its sidecar agrees with the DDL.
	out, _, err = run(t, fakeEnv(nil), "inspect", "perspective", "list", dir, lang)
	if err != nil || !strings.Contains(out, "src/perspectives/trino.perspective.json") {
		t.Fatalf("list after materialise: %v\n%s", err, out)
	}
	out, _, err = run(t, fakeEnv(nil), "inspect", dir, lang, "demo.thing")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"@trino:final demo_final.thing (table, primary)", "@trino:final DATE     partition"} {
		if !strings.Contains(out, want) {
			t.Errorf("re-inspection lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "perspective-") {
		t.Errorf("materialised values disagree with their definition:\n%s", out)
	}
}

// A generator's context is the .hms document with the perspective values
// merged under extensions, as hmd-schema-loader merges a sidecar.
func TestInspectContextMergesExtensions(t *testing.T) {
	t.Parallel()
	out, _, err := run(t, fakeEnv(nil), "inspect", inspectRepo(t), "demo.thing", "--context")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Name       string `json:"name"`
		Extensions map[string]struct {
			Bindings []map[string]json.RawMessage `json:"bindings"`
		} `json:"extensions"`
	}
	body := out[strings.Index(out, "{"):]
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if doc.Name != "thing" || len(doc.Extensions["trino"].Bindings) != 2 {
		t.Errorf("context = %s", out)
	}
}

func TestInspectNounFilterJSONAndHMS(t *testing.T) {
	t.Parallel()
	dir := inspectRepo(t)
	out, _, err := run(t, fakeEnv(nil), "inspect", dir, "environment", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Model struct {
			Nouns []struct {
				ID struct{ Namespace, Name string } `json:"id"`
			} `json:"nouns"`
		} `json:"model"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil || len(doc.Model.Nouns) != 1 || doc.Model.Nouns[0].ID.Name != "environment" {
		t.Fatalf("json = %s (%v)", out, err)
	}

	out, _, err = run(t, fakeEnv(nil), "inspect", dir, "hmd_lang_demo.environment", "--hms")
	if err != nil || !strings.Contains(out, `"namespace": "hmd_lang_demo"`) {
		t.Errorf("hms: %v\n%s", err, out)
	}
	// A DATE column is a timestamp in the core document; DATE itself goes to
	// the trino perspective sidecar beside it.
	out, _, err = run(t, fakeEnv(nil), "inspect", dir, "demo.thing", "--hms")
	if err != nil {
		t.Fatalf("hms export: %v", err)
	}
	for _, want := range []string{
		"# src/schemas/demo/thing.hms", `"at": {"type":"timestamp"}`,
		"# src/schemas/demo/thing.trino.hms", `"definition": "DATE"`, `"value": "final"`,
		"# src/perspectives/trino.perspective.json", `"binding_key": "layer"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("export lacks %q:\n%s", want, out)
		}
	}
	outDir := t.TempDir()
	if _, _, err := run(t, fakeEnv(nil), "inspect", dir, "demo.thing", "--out", outDir); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"src/schemas/demo/thing.hms", "src/schemas/demo/thing.trino.hms"} {
		if _, err := os.Stat(filepath.Join(outDir, f)); err != nil {
			t.Errorf("--out: %v", err)
		}
	}
	if _, _, err := run(t, fakeEnv(nil), "inspect", dir, "no_such_noun"); err == nil {
		t.Error("an unknown noun should be a usage error")
	}
}
