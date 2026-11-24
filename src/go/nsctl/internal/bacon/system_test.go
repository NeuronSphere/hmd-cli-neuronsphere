package bacon

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// NERD041 SPEC001: the shorthand with no flags, the object form holding only
// what was given, and every bad value refused with the document untouched.
func TestSetSystemForms(t *testing.T) {
	t.Parallel()

	doc := base()
	key, err := SetSystem(doc, System{Name: "transform"})
	if err != nil || key != "system" {
		t.Fatalf("SetSystem = %q, %v", key, err)
	}
	if v, _ := doc.Get("system"); v != "transform" {
		t.Errorf("shorthand not written: %#v", v)
	}

	if _, err := SetSystem(doc, System{Name: "cli-go", Role: "cli", Parent: "cli"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(encoded(t, doc), `"system": {
    "name": "cli-go",
    "role": "cli",
    "parent": "cli"
  }`) {
		t.Errorf("object form not written with only the given fields:\n%s", encoded(t, doc))
	}
}

func TestSetSystemRefusesBadValues(t *testing.T) {
	t.Parallel()

	cases := map[string]System{
		"empty name":        {},
		"upper case":        {Name: "Transform"},
		"double hyphen":     {Name: "cli--go"},
		"leading digit":     {Name: "1cli"},
		"trailing hyphen":   {Name: "cli-"},
		"unknown role":      {Name: "transform", Role: "worker"},
		"bad parent":        {Name: "cli-go", Parent: "CLI"},
		"its own parent":    {Name: "cli", Parent: "cli"},
		"whitespace parent": {Name: "cli-go", Parent: " "},
	}
	for name, s := range cases {
		t.Run(name, func(t *testing.T) {
			doc := base()
			before := encoded(t, doc)
			if _, err := SetSystem(doc, s); err == nil {
				t.Fatalf("SetSystem(%+v) accepted", s)
			}
			if encoded(t, doc) != before {
				t.Error("the document changed on a refused value")
			}
		})
	}
}

// SPEC002: clear removes the key and reports whether there was one.
func TestClearSystem(t *testing.T) {
	t.Parallel()

	doc := base()
	if _, ok := ClearSystem(doc); ok {
		t.Error("clearing nothing reported a change")
	}
	SetSystem(doc, System{Name: "transform"})
	if key, ok := ClearSystem(doc); !ok || key != "system" {
		t.Errorf("ClearSystem = %q, %v", key, ok)
	}
	if _, ok := doc.Get("system"); ok {
		t.Error("system survived clear")
	}
}

// SystemOf reads both forms, and rejects what the schema rejects.
func TestSystemOf(t *testing.T) {
	t.Parallel()

	doc := base()
	if _, ok, err := SystemOf(doc); ok || err != nil {
		t.Errorf("no key: ok=%v err=%v", ok, err)
	}
	doc.Set("system", "transform")
	if s, ok, err := SystemOf(doc); !ok || err != nil || s != (System{Name: "transform"}) {
		t.Errorf("shorthand: %+v %v %v", s, ok, err)
	}
	obj := NewObject()
	obj.Set("name", "cli-go")
	obj.Set("role", "cli")
	obj.Set("description", "Go CLIs")
	obj.Set("parent", "cli")
	doc.Set("system", obj)
	want := System{Name: "cli-go", Role: "cli", Description: "Go CLIs", Parent: "cli"}
	if s, ok, err := SystemOf(doc); !ok || err != nil || s != want {
		t.Errorf("object: %+v %v %v", s, ok, err)
	}
	for _, bad := range []any{"", []any{"a"}, 3.0} {
		doc.Set("system", bad)
		if _, _, err := SystemOf(doc); err == nil {
			t.Errorf("SystemOf accepted %#v", bad)
		}
	}
	unknown := NewObject()
	unknown.Set("name", "transform")
	unknown.Set("owner", "someone")
	doc.Set("system", unknown)
	if _, _, err := SystemOf(doc); err == nil {
		t.Error("SystemOf accepted an unknown key")
	}
}

// SPEC003: describe carries the system as an object, whichever form, and
// leaves a manifest without one exactly as before.
func TestDescribeReportsTheSystem(t *testing.T) {
	t.Parallel()

	doc := base()
	before, _ := Encode(Describe(doc))
	if strings.Contains(string(before), "system") {
		t.Fatalf("a manifest without system reports one:\n%s", before)
	}
	doc.Set("system", "transform")
	sys, ok := Describe(doc).Object("system")
	if !ok {
		t.Fatal("describe dropped the system")
	}
	if n, _ := sys.String("name"); n != "transform" || sys.Len() != 1 {
		t.Errorf("shorthand described as %v", sys.Keys())
	}
	SetSystem(doc, System{Name: "transform", Role: "engine"})
	sys, _ = Describe(doc).Object("system")
	if r, _ := sys.String("role"); r != "engine" {
		t.Errorf("role = %q", r)
	}
}

// SPEC004: resolution across manifests.
func TestResolveSystems(t *testing.T) {
	t.Parallel()

	entries := []SystemEntry{
		{Manifest: "a/meta-data/manifest.json", RepoClass: "hmd-cli-a", System: System{Name: "cli-go", Role: "cli", Description: "Go CLIs", Parent: "cli"}},
		{Manifest: "b/meta-data/manifest.json", RepoClass: "hmd-cli-b", System: System{Name: "cli-go", Description: "Go CLIs"}},
		{Manifest: "c/meta-data/manifest.json", RepoClass: "hmd-cli-c", System: System{Name: "cli-python", Parent: "cli"}},
		{Manifest: "d/meta-data/manifest.json", RepoClass: "hmd-ms-x", System: System{Name: "transform", Description: "one"}},
		{Manifest: "e/meta-data/manifest.json", RepoClass: "hmd-lib-x", System: System{Name: "transform", Description: "two", Role: "library"}},
	}
	r := ResolveSystems(entries)

	byName := map[string]ResolvedSystem{}
	var names []string
	for _, s := range r.Systems {
		byName[s.Name] = s
		names = append(names, s.Name)
	}
	if !reflect.DeepEqual(names, []string{"cli", "cli-go", "cli-python", "transform"}) {
		t.Fatalf("systems = %v (want sorted, with the parent-only cli)", names)
	}
	if cli := byName["cli"]; len(cli.Members) != 0 || !reflect.DeepEqual(cli.Subsystems, []string{"cli-go", "cli-python"}) {
		t.Errorf("cli = %+v", cli)
	}
	if g := byName["cli-go"]; g.Description != "Go CLIs" || g.Parent != "cli" || len(g.Members) != 2 || g.Members[0].Role != "cli" {
		t.Errorf("cli-go = %+v (agreeing definitions must resolve)", g)
	}
	if tr := byName["transform"]; tr.Description != "" {
		t.Errorf("a conflicting description resolved to %q", tr.Description)
	}
	if len(r.Conflicts) != 1 {
		t.Fatalf("conflicts = %+v", r.Conflicts)
	}
	c := r.Conflicts[0]
	if c.System != "transform" || c.Field != "description" || !reflect.DeepEqual(c.Manifests, []string{"d/meta-data/manifest.json", "e/meta-data/manifest.json"}) {
		t.Errorf("conflict = %+v", c)
	}
	if len(r.Cycles) != 0 {
		t.Errorf("cycles = %v", r.Cycles)
	}
	if r.OK() {
		t.Error("a report with a conflict is OK")
	}
}

func TestResolveSystemsFindsCycles(t *testing.T) {
	t.Parallel()

	r := ResolveSystems([]SystemEntry{
		{Manifest: "a", RepoClass: "a", System: System{Name: "x", Parent: "y"}},
		{Manifest: "b", RepoClass: "b", System: System{Name: "y", Parent: "z"}},
		{Manifest: "c", RepoClass: "c", System: System{Name: "z", Parent: "x"}},
		{Manifest: "d", RepoClass: "d", System: System{Name: "w", Parent: "x"}},
	})
	if !reflect.DeepEqual(r.Cycles, [][]string{{"x", "y", "z"}}) {
		t.Errorf("cycles = %v, want the one loop, reported once", r.Cycles)
	}
	if r.OK() {
		t.Error("a report with a cycle is OK")
	}
}

// ScanSystems reads repo classes one level under each root, skipping hidden
// directories and worktrees, and records unreadable system keys as problems.
func TestScanSystems(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	write := func(dir, manifest string) {
		os.MkdirAll(filepath.Join(root, dir, "meta-data"), 0o755)
		os.WriteFile(filepath.Join(root, dir, "meta-data", "manifest.json"), []byte(manifest), 0o644)
	}
	write("hmd-ms-x", `{"name": "hmd-ms-x", "description": "d", "build": {}, "system": {"name": "transform", "role": "core"}}`)
	write("hmd-lib-y", `{"name": "hmd-lib-y", "description": "d", "build": {}}`)
	write("hmd-bad", `{"name": "hmd-bad", "description": "d", "build": {}, "system": "Bad"}`)
	write(".hidden", `{"name": "hidden", "description": "d", "build": {}, "system": "transform"}`)
	write("a-worktree", `{"name": "wt", "description": "d", "build": {}, "system": "transform"}`)
	os.WriteFile(filepath.Join(root, "a-worktree", ".git"), []byte("gitdir: elsewhere\n"), 0o644)
	os.MkdirAll(filepath.Join(root, "not-a-repo-class"), 0o755)

	entries, problems, err := ScanSystems([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].RepoClass != "hmd-ms-x" || entries[0].System.Role != "core" {
		t.Errorf("entries = %+v", entries)
	}
	if !strings.HasSuffix(entries[0].Manifest, filepath.Join("hmd-ms-x", "meta-data", "manifest.json")) {
		t.Errorf("manifest path = %q", entries[0].Manifest)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "hmd-bad") {
		t.Errorf("problems = %v", problems)
	}
}

// SPEC005: membership is declared, never inferred, and nothing outside the
// system verbs, describe and systems reads the key. Every Go file that names
// the key "system" must be one of these.
func TestOnlyTheSystemVerbsReadTheKey(t *testing.T) {
	t.Parallel()

	allowed := map[string]bool{
		"internal/bacon/system.go":   true,
		"internal/bacon/describe.go": true,
		"cmd/repoclass_system.go":    true,
		"cmd/repoclass.go":           true, // the describe table
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		data, _ := os.ReadFile(path)
		if strings.Contains(string(data), `"system"`) && !allowed[filepath.ToSlash(rel)] {
			t.Errorf("%s names the system key; membership is read only by the system verbs, describe and systems", rel)
		}
		return nil
	})
}
