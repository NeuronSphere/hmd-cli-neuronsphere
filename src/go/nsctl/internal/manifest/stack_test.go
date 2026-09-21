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
	if len(m.Stacks) != 1 || m.Stacks[0].Version != "0.2.0" {
		t.Fatalf("SetStack must replace by name: %+v", m.Stacks)
	}
	if problems := m.Validate(func(string) string { return "" }); len(problems) != 0 {
		t.Fatalf("Validate: %v", problems)
	}
	path := filepath.Join(t.TempDir(), "dev.yaml")
	if err := m.Save(path); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "stacks:") {
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
		Stacks: []StackRecord{{Name: "", Version: "", Ref: ""}, {Name: "x", Version: "1", Ref: "r", Bindings: map[string]string{"k": " "}}, {Name: "x", Version: "1", Ref: "r"}}}
	problems := strings.Join(m.Validate(func(string) string { return "" }), "\n")
	for _, want := range []string{"stacks[0]: 'name' is required", "'version' is required", "'ref' is required", `bindings["k"]`, `duplicate stack "x"`} {
		if !strings.Contains(problems, want) {
			t.Errorf("missing %q in:\n%s", want, problems)
		}
	}
}
