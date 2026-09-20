package repoclass

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeResource(t *testing.T, repoHome, repoClass, name, body string) {
	t.Helper()
	dir := filepath.Join(repoHome, repoClass, "meta-data", ResourcesDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const vpcDeclaration = `
resource_namespace: network.neuronsphere.io
resource_definition_name: vpc
version: 0.1.0
parent:
  resource_namespace: network.neuronsphere.io
  resource_definition_name: network
  version: 0.1.0
produces: true
role: network
`

func TestProducesReadsTheDeclaration(t *testing.T) {
	t.Parallel()

	repoHome := t.TempDir()
	writeResource(t, repoHome, "hmd-vpc", "vpc.yaml", vpcDeclaration)

	got, err := New(repoHome, fakeEnv(nil)).Produces("hmd-vpc")
	if err != nil {
		t.Fatalf("Produces: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d declarations, want 1: %+v", len(got), got)
	}
	d := got[0]
	if d.Namespace != "network.neuronsphere.io" || d.Name != "vpc" || d.Role != "network" || !d.Produces {
		t.Errorf("declaration = %+v", d)
	}
	if d.Parent == nil || d.Parent.Name != "network" {
		t.Errorf("parent = %+v", d.Parent)
	}
	if d.Path == "" {
		t.Error("the declaration does not record the file it came from")
	}
}

// A repo can define a type without emitting one; only `produces: true` says it
// deploys an instance of it.
func TestProducedNarrowsToWhatIsEmitted(t *testing.T) {
	t.Parallel()

	repoHome := t.TempDir()
	writeResource(t, repoHome, "hmd-thing", "emitted.yaml", vpcDeclaration)
	writeResource(t, repoHome, "hmd-thing", "defined-only.yaml", `
resource_namespace: thing.neuronsphere.io
resource_definition_name: thing
version: 0.1.0
role: thing
`)

	all, err := New(repoHome, fakeEnv(nil)).Produces("hmd-thing")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("read %d declarations, want both", len(all))
	}
	produced := Produced(all)
	if len(produced) != 1 || produced[0].Name != "vpc" {
		t.Errorf("Produced = %+v, want only the emitted one", produced)
	}
}

// Most repos declare none, and that is not an error.
func TestProducesOnARepoWithNoResources(t *testing.T) {
	t.Parallel()

	repoHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoHome, "hmd-plain", "meta-data"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := New(repoHome, fakeEnv(nil)).Produces("hmd-plain")
	if err != nil {
		t.Errorf("Produces: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %+v, want nothing", got)
	}
}

func TestProducesOnAnAbsentRepo(t *testing.T) {
	t.Parallel()

	got, err := New(t.TempDir(), fakeEnv(nil)).Produces("hmd-not-here")
	if err != nil {
		t.Errorf("Produces: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %+v, want nothing", got)
	}
}

// Filesystem order is not a contract, so the read order is made one.
func TestProducesIsDeterministicAndSkipsNonYAML(t *testing.T) {
	t.Parallel()

	repoHome := t.TempDir()
	for _, name := range []string{"zebra.yaml", "alpha.yml", "middle.yaml"} {
		writeResource(t, repoHome, "hmd-many", name, `
resource_namespace: ns.neuronsphere.io
resource_definition_name: `+name[:3]+`
version: 0.1.0
produces: true
`)
	}
	writeResource(t, repoHome, "hmd-many", "README.md", "not a declaration")

	got, err := New(repoHome, fakeEnv(nil)).Produces("hmd-many")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range got {
		names = append(names, d.Name)
	}
	if want := []string{"alp", "mid", "zeb"}; !reflect.DeepEqual(names, want) {
		t.Errorf("names = %v, want %v", names, want)
	}
}

// A file that is not a definition must not stop a deploy that never needed it.
func TestProducesSkipsADocumentWithNoType(t *testing.T) {
	t.Parallel()

	repoHome := t.TempDir()
	writeResource(t, repoHome, "hmd-odd", "notes.yaml", "some: value\n")
	writeResource(t, repoHome, "hmd-odd", "real.yaml", vpcDeclaration)

	got, err := New(repoHome, fakeEnv(nil)).Produces("hmd-odd")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "vpc" {
		t.Errorf("got %+v, want only the real declaration", got)
	}
}

func TestProducesReportsMalformedYAML(t *testing.T) {
	t.Parallel()

	repoHome := t.TempDir()
	writeResource(t, repoHome, "hmd-broken", "bad.yaml", "\tnot: [valid")
	_, err := New(repoHome, fakeEnv(nil)).Produces("hmd-broken")
	if err == nil {
		t.Fatal("malformed YAML parsed cleanly")
	}
	if !contains(err.Error(), "bad.yaml") {
		t.Errorf("error does not name the file: %v", err)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle ||
		len(needle) == 0 || indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
