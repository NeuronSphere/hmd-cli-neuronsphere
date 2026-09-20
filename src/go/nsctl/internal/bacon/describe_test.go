package bacon

import (
	"bytes"
	"os"
	"testing"
)

// The golden describe output was captured from `hmd describe` on the same
// manifest (testdata/describe-input.json); the Go output must contain the
// Python's document as a strict subset, in the same order, with the
// additions -- dependency `resource`, `test` -- on top.
func TestDescribeMatchesThePythonSummary(t *testing.T) {
	t.Parallel()

	input, err := os.ReadFile("testdata/describe-input.json")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := Decode(input)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/describe-golden.json")
	if err != nil {
		t.Fatal(err)
	}
	got := Describe(doc)

	// Strip the two additions, then compare byte-for-byte with the Python.
	if deps, ok := got.Array("dependencies"); ok {
		for _, d := range deps {
			d.(*Object).Delete("resource")
		}
	}
	got.Delete("test")
	encoded, err := Encode(got)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, bytes.TrimRight(want, "\n")) {
		t.Errorf("describe differs from hmd describe:\n--- got ---\n%s\n--- want ---\n%s", encoded, want)
	}
}

func TestDescribeAdditions(t *testing.T) {
	t.Parallel()

	input, _ := os.ReadFile("testdata/describe-input.json")
	doc, _ := Decode(input)
	got := Describe(doc)

	deps, _ := got.Array("dependencies")
	warehouse := deps[0].(*Object)
	if res, ok := warehouse.Object("resource"); !ok || res.Len() == 0 {
		t.Errorf("the resource block was dropped from the dependency: %v", warehouse.Keys())
	}
	if req, _ := warehouse.Get("required"); req != true {
		t.Errorf("required = %v (%T), want the boolean true", req, req)
	}
	if _, ok := got.Object("test"); !ok {
		t.Error("the test section was dropped")
	}
}

// discovery.summary falls back to the description, and an empty discovery
// block is omitted -- the Python's rules.
func TestDescribeSummaryFallsBackToDescription(t *testing.T) {
	t.Parallel()

	doc := NewObject()
	doc.Set("name", "x")
	doc.Set("description", "the description")
	disc := NewObject()
	disc.Set("entry_points", []any{})
	disc.Set("capabilities", []any{})
	doc.Set("discovery", disc)

	got := Describe(doc)
	d, ok := got.Object("discovery")
	if !ok {
		t.Fatal("discovery omitted although it has keys")
	}
	if s, _ := d.String("summary"); s != "the description" {
		t.Errorf("summary = %q", s)
	}
	if _, ok := d.Get("entry_points"); ok {
		t.Error("an empty entry_points list was reported")
	}
}
