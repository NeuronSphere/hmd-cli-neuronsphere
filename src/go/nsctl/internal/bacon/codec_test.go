package bacon

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

// The golden file was written by Python's json.dump(indent=2) -- what
// hmd_lib_manifest.write_manifest produces. Decoding and re-encoding it must
// give the same bytes, or every edit by one front end is a diff under the
// other.
func TestEncodeMatchesPythonByteForByte(t *testing.T) {
	t.Parallel()

	want, err := os.ReadFile("testdata/golden.json")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := Decode(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Encode(doc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("round trip differs from json.dump(indent=2):\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// Key order is the file's, not the alphabet's; unknown keys at every depth
// survive; nested lists of objects survive.
func TestDecodeKeepsOrderAndUnknownKeys(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("testdata/golden.json")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	wantTop := []string{"name", "description", "build", "deploy", "unknown_top", "test", "discovery"}
	if got := doc.Keys(); !equalStrings(got, wantTop) {
		t.Errorf("top-level keys = %v, want %v", got, wantTop)
	}
	deps, ok := doc.Lookup("deploy", "dependencies")
	if !ok {
		t.Fatal("deploy.dependencies missing")
	}
	if got := deps.(*Object).Keys(); !equalStrings(got, []string{"warehouse", "zeta", "alpha"}) {
		t.Errorf("dependency order = %v, want file order", got)
	}
	deep, ok := doc.Lookup("unknown_top", "nested")
	if !ok || len(deep.([]any)) != 3 {
		t.Errorf("unknown_top.nested = %v", deep)
	}
	if n, _ := doc.Lookup("deploy", "default_configuration", "whole"); n != json.Number("1.0") {
		t.Errorf("1.0 became %v (%T); numbers must keep their text", n, n)
	}
}

func TestDecodeRefusesWhatIsNotAnObject(t *testing.T) {
	t.Parallel()

	for _, in := range []string{`[]`, `"x"`, `{"a": 1} {"b": 2}`, `{"a": }`} {
		if _, err := Decode([]byte(in)); err == nil {
			t.Errorf("Decode(%q) succeeded", in)
		}
	}
}

// Set keeps an existing key's position: re-running a verb changes the value
// and nothing else in the diff.
func TestSetKeepsPositionAndDeleteRemoves(t *testing.T) {
	t.Parallel()

	o := NewObject()
	o.Set("a", "1")
	o.Set("b", "2")
	o.Set("c", "3")
	o.Set("b", "changed")
	if got := o.Keys(); !equalStrings(got, []string{"a", "b", "c"}) {
		t.Errorf("keys after re-set = %v", got)
	}
	if v, _ := o.Get("b"); v != "changed" {
		t.Errorf("b = %v", v)
	}
	if !o.Delete("b") || o.Delete("b") {
		t.Error("Delete should report the first removal and not the second")
	}
	if got := o.Keys(); !equalStrings(got, []string{"a", "c"}) {
		t.Errorf("keys after delete = %v", got)
	}
}

func TestSetPathCreatesIntermediatesAndRefusesToClobber(t *testing.T) {
	t.Parallel()

	o := NewObject()
	if err := o.SetPath("v", "deploy", "default_configuration", "schema"); err != nil {
		t.Fatal(err)
	}
	if v, ok := o.Lookup("deploy", "default_configuration", "schema"); !ok || v != "v" {
		t.Errorf("lookup = %v, %v", v, ok)
	}
	o.Set("name", "x")
	if err := o.SetPath("v", "name", "child"); err == nil {
		t.Error("writing under a string succeeded; it should refuse rather than replace the user's value")
	}
	if !o.DeletePath("deploy", "default_configuration", "schema") {
		t.Error("DeletePath did not find the key")
	}
	if _, ok := o.Lookup("deploy", "default_configuration"); !ok {
		t.Error("DeletePath removed the parent; it should leave sections alone")
	}
}

// Values set from Go -- what parseConfig hands over -- encode like their JSON
// counterparts, with maps sorted.
func TestEncodeGoValues(t *testing.T) {
	t.Parallel()

	o := NewObject()
	o.Set("m", map[string]any{"b": 2.0, "a": []any{"x", true, nil}})
	o.Set("i", 3)
	o.Set("f", 2.5)
	o.Set("s", []string{"p", "q"})
	got, err := Encode(o)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"m\": {\n    \"a\": [\n      \"x\",\n      true,\n      null\n    ],\n    \"b\": 2\n  },\n  \"i\": 3,\n  \"f\": 2.5,\n  \"s\": [\n    \"p\",\n    \"q\"\n  ]\n}"
	if string(got) != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
