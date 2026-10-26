package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// NERD017 SPEC009: the stacks record round-trips and validates.

func TestStackRecordsRoundTrip(t *testing.T) {
	t.Parallel()
	m := &Manifest{Version: Version, Name: "dev", Scope: ScopeEnvironment}
	m.SetStack(StackRecord{Name: "obs", Version: "0.1.0", Ref: "oci://ghcr.io/hmdlabs/stacks/obs", Digest: "sha256:1",
		Bindings: map[string]string{"hmd-stack-obs": "obs", "otel": "otel"}})
	m.SetStack(StackRecord{Name: "obs", Version: "0.2.0", Ref: "oci://ghcr.io/hmdlabs/stacks/obs", Digest: "sha256:2"})
	if len(m.Bundles) != 1 || m.Bundles[0].Version != "0.2.0" {
		t.Fatalf("SetStack must replace by name: %+v", m.Bundles)
	}
	if problems := m.Validate(func(string) string { return "" }); len(problems) != 0 {
		t.Fatalf("Validate: %v", problems)
	}
	path := filepath.Join(t.TempDir(), "dev.yaml")
	if err := m.Save(path); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "bundles:") {
		t.Errorf("not serialised: %s", data)
	}
	back, err := LoadFile(path, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	rec, _, ok := back.Stack("obs")
	if !ok || rec.Digest != "sha256:2" {
		t.Errorf("Stack after reload = %+v, %v", rec, ok)
	}
	if !back.RemoveStack("obs") || back.RemoveStack("obs") {
		t.Error("RemoveStack must report true once")
	}
}

func TestStackRecordValidation(t *testing.T) {
	t.Parallel()
	m := &Manifest{Version: Version, Name: "dev", Scope: ScopeEnvironment,
		Bundles: []StackRecord{{Name: "", Version: "", Ref: ""}, {Name: "x", Version: "1", Ref: "r", Bindings: map[string]string{"k": " "}}, {Name: "x", Version: "1", Ref: "r"}}}
	problems := strings.Join(m.Validate(func(string) string { return "" }), "\n")
	for _, want := range []string{"bundles[0]: 'name' is required", "'version' is required", "'ref' is required", `bindings["k"]`, `duplicate bundle "x"`} {
		if !strings.Contains(problems, want) {
			t.Errorf("missing %q in:\n%s", want, problems)
		}
	}
}

// NERD0019 SPEC0006: `stacks:` is read as `bundles:`, and only `bundles:` is written.

func TestLegacyStacksKeyIsReadAsBundles(t *testing.T) {
	t.Parallel()
	m, err := Parse([]byte(`
version: 1
name: dev
bundles:
  - {name: obs, version: 0.2.0, ref: oci://ghcr.io/neuronsphere/stacks/obs}
stacks:
  - {name: obs, version: 0.1.0, ref: oci://ghcr.io/neuronsphere/stacks/obs}
  - {name: analytics, version: 0.1.4, ref: oci://ghcr.io/neuronsphere/stacks/analytics, declared: [airflow]}
`), ".yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Bundles) != 2 || m.LegacyStacks != nil {
		t.Fatalf("Bundles = %+v, LegacyStacks = %+v", m.Bundles, m.LegacyStacks)
	}
	if rec, _, _ := m.Stack("obs"); rec.Version != "0.2.0" {
		t.Errorf("a name under both keys keeps the bundles: record, got %+v", rec)
	}
	if rec, _, ok := m.Stack("analytics"); !ok || rec.Declared[0] != "airflow" {
		t.Errorf("analytics = %+v, %v", rec, ok)
	}

	path := filepath.Join(t.TempDir(), "dev.yaml")
	if err := m.Save(path); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "stacks:") || !strings.Contains(string(data), "bundles:") {
		t.Errorf("want only bundles: written, got:\n%s", data)
	}
}
