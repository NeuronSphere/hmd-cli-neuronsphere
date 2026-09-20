package cpext

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/compose"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/hmdenv"
)

// registryHandback is the declaration NERD006 phase 1 needs, as SPEC010
// documents it.
func registryHandback() map[string]any {
	return map[string]any{
		"url": "http://registry.local.neuronsphere.io",
		"handback": []any{
			map[string]any{
				"name":  "PYTHON_REGISTRIES",
				"merge": "json-map",
				"key":   "neuronsphere",
				"value": map[string]any{
					"url":      "${NS_CONFIG_URL}/hmd/local/+simple/",
					"username": "hmd",
					"password": "hmd",
					"publish":  true,
				},
			},
		},
	}
}

func testLookup(vars map[string]string) compose.Lookup {
	return func(key string) string { return vars[key] }
}

func TestHandbackFromReadsTheDeclaredVariables(t *testing.T) {
	t.Parallel()

	lookup := testLookup(map[string]string{"NS_CONFIG_URL": "http://registry.local.neuronsphere.io"})
	got, err := handbackFrom("registry", registryHandback(), lookup)
	if err != nil {
		t.Fatalf("handbackFrom() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d vars, want 1", len(got))
	}
	v := got[0]
	if v.Name != "PYTHON_REGISTRIES" || v.Merge != hmdenv.MergeJSONMap || v.Key != "neuronsphere" {
		t.Errorf("got %+v", v)
	}
	if v.Source != "registry" {
		t.Errorf("Source = %q, want the contributing instance", v.Source)
	}
	value, ok := v.Value.(map[string]any)
	if !ok {
		t.Fatalf("Value = %T, want a map", v.Value)
	}
	// The URL is declared once, in `url`, and referred to here.
	if got, want := value["url"], "http://registry.local.neuronsphere.io/hmd/local/+simple/"; got != want {
		t.Errorf("url = %v, want %q -- ${NS_CONFIG_URL} should have been expanded", got, want)
	}
}

func TestHandbackFromDefaultsToScalar(t *testing.T) {
	t.Parallel()

	got, err := handbackFrom("x", map[string]any{
		"handback": []any{map[string]any{"name": "A", "value": "b"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Merge != hmdenv.MergeScalar {
		t.Errorf("Merge = %q, want the scalar default", got[0].Merge)
	}
}

func TestHandbackFromIsAbsentWithoutADeclaration(t *testing.T) {
	t.Parallel()

	got, err := handbackFrom("x", map[string]any{"url": "http://a"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("got %v, want nothing declared", got)
	}
}

func TestHandbackFromRejects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config map[string]any
		want   string
	}{
		{"not a list", map[string]any{"handback": "PYTHON_REGISTRIES"}, "must be a list"},
		{"item not a block", map[string]any{"handback": []any{"A=b"}}, "must be a block"},
		{"no name", map[string]any{"handback": []any{map[string]any{"value": "b"}}}, "has no 'name'"},
		{"unusable name", map[string]any{"handback": []any{
			map[string]any{"name": "not a var", "value": "b"}}}, "not a usable variable name"},
		{"duplicate name", map[string]any{"handback": []any{
			map[string]any{"name": "A", "value": "b"},
			map[string]any{"name": "A", "value": "c"}}}, "duplicate"},
		{"unknown merge", map[string]any{"handback": []any{
			map[string]any{"name": "A", "merge": "append", "value": "b"}}}, "unknown merge"},
		{"json-map without a key", map[string]any{"handback": []any{
			map[string]any{"name": "A", "merge": "json-map", "value": map[string]any{"x": "y"}}}}, "no 'key'"},
		{"key on a scalar", map[string]any{"handback": []any{
			map[string]any{"name": "A", "merge": "scalar", "key": "k", "value": "b"}}}, "where a key has no meaning"},
		{"separator on a scalar", map[string]any{"handback": []any{
			map[string]any{"name": "A", "merge": "scalar", "separator": ",", "value": "b"}}}, "no list to split"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := handbackFrom("x", tt.config, nil)
			if err == nil {
				t.Fatalf("handbackFrom() accepted %v", tt.config)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to mention %q", err, tt.want)
			}
		})
	}
}

func TestHandbackAdmitsALiteralLoopbackPassword(t *testing.T) {
	t.Parallel()

	// Named after the rule so it survives someone tightening the gate: a
	// handback may carry a literal the manifest itself carries. NERD006's
	// local index is anonymous on the loopback side by design, and refusing
	// its dummy would refuse the only consumer this mechanism has.
	lookup := testLookup(map[string]string{"NS_CONFIG_URL": "http://local"})
	if _, err := handbackFrom("registry", registryHandback(), lookup); err != nil {
		t.Fatalf("a literal dummy credential was refused: %v", err)
	}
}

func TestHandbackRefusesASecretReference(t *testing.T) {
	t.Parallel()

	// A Credential's resolved value is unreachable from this lookup, so the
	// reference could only ever expand to nothing. Refused so that it is an
	// error the reader can act on rather than an empty string they must notice.
	_, err := handbackFrom("registry", map[string]any{
		"handback": []any{map[string]any{"name": "TOKEN", "value": "${NS_SECRET_UPSTREAM}"}},
	}, testLookup(nil))
	if err == nil {
		t.Fatal("handbackFrom() accepted a credential reference")
	}
	if !strings.Contains(err.Error(), "NS_SECRET_UPSTREAM") || !strings.Contains(err.Error(), "plain file") {
		t.Errorf("error = %q, want it to name the reference and say why", err)
	}
}

func TestHandbackRefusesAnUnresolvedReference(t *testing.T) {
	t.Parallel()

	_, err := handbackFrom("x", map[string]any{
		"handback": []any{map[string]any{"name": "A", "value": "${NOT_SET}/path"}},
	}, testLookup(nil))
	if err == nil {
		t.Fatal("handbackFrom() accepted a value that did not fully resolve")
	}
	if !strings.Contains(err.Error(), "NOT_SET") {
		t.Errorf("error = %q, want it to name the variable that did not resolve", err)
	}
}

func TestHandbackCollectsFromResolvedExtensionsOnly(t *testing.T) {
	t.Parallel()

	exts := []Extension{
		{Instance: "a", Handback: []hmdenv.Var{{Name: "B", Value: "1"}}},
		{Instance: "b", Handback: []hmdenv.Var{{Name: "A", Value: "2"}}},
		{Instance: "broken", Err: errors.New("no working tree"), Handback: []hmdenv.Var{{Name: "C", Value: "3"}}},
	}
	got, err := Handback(exts)
	if err != nil {
		t.Fatalf("Handback() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d vars, want 2 -- a failed extension contributes nothing", len(got))
	}
	// Sorted, so a regenerated block is byte-identical when nothing changed.
	if got[0].Name != "A" || got[1].Name != "B" {
		t.Errorf("got %v, want them sorted by name", []string{got[0].Name, got[1].Name})
	}
}

func TestHandbackRefusesTwoExtensionsClaimingOneVariable(t *testing.T) {
	t.Parallel()

	// Neither is written. Picking an arbitrary winner is how this becomes a
	// bug that moves when you look at it.
	exts := []Extension{
		{Instance: "a", Handback: []hmdenv.Var{{Name: "GOPROXY", Source: "a", Value: "1"}}},
		{Instance: "b", Handback: []hmdenv.Var{{Name: "GOPROXY", Source: "b", Value: "2"}}},
	}
	_, err := Handback(exts)
	if err == nil {
		t.Fatal("Handback() accepted two owners for one variable")
	}
	if !strings.Contains(err.Error(), "a") || !strings.Contains(err.Error(), "b") {
		t.Errorf("error = %q, want it to name both instances", err)
	}
}

func TestHandbackAllowsTwoExtensionsToShareOneMap(t *testing.T) {
	t.Parallel()

	// Different keys in one json-map is the whole point of the map shape.
	exts := []Extension{
		{Instance: "a", Handback: []hmdenv.Var{
			{Name: "PYTHON_REGISTRIES", Merge: hmdenv.MergeJSONMap, Key: "one", Source: "a"}}},
		{Instance: "b", Handback: []hmdenv.Var{
			{Name: "PYTHON_REGISTRIES", Merge: hmdenv.MergeJSONMap, Key: "two", Source: "b"}}},
	}
	got, err := Handback(exts)
	if err != nil {
		t.Fatalf("Handback() error = %v", err)
	}
	if len(got) != 2 {
		t.Errorf("got %d vars, want both entries", len(got))
	}
}

func TestReportHandbackNamesWhatYielded(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	ReportHandback(&buf, hmdenv.Result{
		Path:    "/home/.config/hmd.env",
		Written: []string{"PYTHON_REGISTRIES"},
		Yielded: []string{"GOPROXY"},
	})
	out := buf.String()
	if !strings.Contains(out, "PYTHON_REGISTRIES") || !strings.Contains(out, "GOPROXY") {
		t.Errorf("output = %q, want both reported", out)
	}
	// The gap this discharges: the lookup was loaded before the write.
	if !strings.Contains(out, "next") {
		t.Errorf("output = %q, want it to say when the value takes effect", out)
	}
}

func TestReportHandbackSaysNothingWhenNothingHappened(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	ReportHandback(&buf, hmdenv.Result{Path: "/x"})
	if buf.Len() != 0 {
		t.Errorf("output = %q, want silence", buf.String())
	}
}
