package bom

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/repoclass"
)

func definitionKeys(defs []ProducedDefinition) []string {
	out := make([]string, 0, len(defs))
	for _, d := range defs {
		out = append(out, d.Namespace+"/"+d.Name)
	}
	return out
}

// declare_produces_resource_definition takes no parent link, so whether a
// requirement for the parent type resolves depends on ms-deployment already
// knowing the hierarchy. Declaring both makes it resolve either way -- and it
// is what the hardcoded list this replaces did.
func TestFromDeclarationsDeclaresTheParentToo(t *testing.T) {
	t.Parallel()

	defs := FromDeclarations([]repoclass.ResourceDeclaration{{
		Namespace: "network.neuronsphere.io", Name: "vpc", Version: "0.1.0",
		Role: "network", Produces: true,
		Parent: &repoclass.ResourceRef{
			Namespace: "network.neuronsphere.io", Name: "network", Version: "0.1.0",
		},
	}})

	want := []string{"network.neuronsphere.io/vpc", "network.neuronsphere.io/network"}
	if got := definitionKeys(defs); !reflect.DeepEqual(got, want) {
		t.Errorf("FromDeclarations = %v, want %v", got, want)
	}
}

func TestFromDeclarationsSkipsWhatIsNotProduced(t *testing.T) {
	t.Parallel()

	defs := FromDeclarations([]repoclass.ResourceDeclaration{
		{Namespace: "a.io", Name: "defined", Version: "0.1.0"},
		{Namespace: "a.io", Name: "emitted", Version: "0.1.0", Produces: true},
	})
	if got := definitionKeys(defs); !reflect.DeepEqual(got, []string{"a.io/emitted"}) {
		t.Errorf("FromDeclarations = %v, want only the emitted type", got)
	}
}

// Two declarations sharing a parent must not declare it twice.
func TestFromDeclarationsDeduplicates(t *testing.T) {
	t.Parallel()

	parent := &repoclass.ResourceRef{Namespace: "base.io", Name: "thing", Version: "0.1.0"}
	defs := FromDeclarations([]repoclass.ResourceDeclaration{
		{Namespace: "a.io", Name: "one", Version: "0.1.0", Produces: true, Parent: parent},
		{Namespace: "a.io", Name: "two", Version: "0.1.0", Produces: true, Parent: parent},
	})
	want := []string{"a.io/one", "base.io/thing", "a.io/two"}
	if got := definitionKeys(defs); !reflect.DeepEqual(got, want) {
		t.Errorf("FromDeclarations = %v, want %v", got, want)
	}
}

// fakeVersions is a VersionResolver whose produced declarations are set per
// repo class.
type fakeVersions struct {
	produces map[string][]repoclass.ResourceDeclaration
	err      error
}

func (f *fakeVersions) Resolve(string, string) (string, map[string]any, map[string]any, error) {
	return "1.0", nil, nil, nil
}

func (f *fakeVersions) Produces(repoClass string) ([]repoclass.ResourceDeclaration, error) {
	return f.produces[repoClass], f.err
}

// The repo's own file is the source of truth. The hardcoded list is a fallback
// for a repo that is not on this machine -- preferring it is how the two
// drifted, with the list naming a namespace the repo's file did not.
func TestProducedByPrefersTheRepoDeclaration(t *testing.T) {
	t.Parallel()

	s := &Seeder{Versions: &fakeVersions{produces: map[string][]repoclass.ResourceDeclaration{
		BaseVPCRepoClass: {{
			Namespace: "declared.io", Name: "vpc", Version: "0.1.0", Produces: true,
		}},
	}}}

	defs, err := s.producedBy(BaseVPCRepoClass)
	if err != nil {
		t.Fatal(err)
	}
	if got := definitionKeys(defs); !reflect.DeepEqual(got, []string{"declared.io/vpc"}) {
		t.Errorf("producedBy = %v, want the repo's own declaration", got)
	}
}

func TestProducedByFallsBackWhenTheRepoIsAbsent(t *testing.T) {
	t.Parallel()

	s := &Seeder{Versions: &fakeVersions{}}
	defs, err := s.producedBy(BaseVPCRepoClass)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(defs, BaseVPCProducedDefinitions) {
		t.Errorf("producedBy = %v, want the fallback", defs)
	}
	// A repo class with neither declarations nor a fallback produces nothing,
	// which is the normal case for a user-added workload.
	if defs, err := s.producedBy("hmd-ms-myapi"); err != nil || len(defs) != 0 {
		t.Errorf("producedBy = %v, %v; want nothing", defs, err)
	}
}

func TestProducingClassesDeduplicatesInOrder(t *testing.T) {
	t.Parallel()

	got := producingClasses([]Entry{
		{RepoClassName: "b"}, {RepoClassName: "a"}, {RepoClassName: "b"}, {RepoClassName: ""},
	})
	if want := []string{"b", "a"}; !reflect.DeepEqual(got, want) {
		t.Errorf("producingClasses = %v, want %v", got, want)
	}
}

// The check that would have caught the namespace mismatch: what hmd-vpc's own
// file declares has to cover what the fallback claims, or an environment
// deployed from a working tree resolves differently from one without it.
//
// Skipped rather than failed when the repo is not checked out, since that is a
// normal state for this repo's own test run.
func TestBaseVPCDeclarationCoversTheFallback(t *testing.T) {
	t.Parallel()

	repoHome := os.Getenv("HMD_REPO_HOME")
	if repoHome == "" {
		repoHome = filepath.Join("..", "..", "..", "..", "..")
	}
	if _, err := os.Stat(filepath.Join(repoHome, BaseVPCRepoClass)); err != nil {
		t.Skipf("%s is not checked out at %s", BaseVPCRepoClass, repoHome)
	}

	resolver := repoclass.New(repoHome, func(string) string { return "" })
	declarations, err := resolver.Produces(BaseVPCRepoClass)
	if err != nil {
		t.Fatalf("reading what %s produces: %v", BaseVPCRepoClass, err)
	}
	if len(declarations) == 0 {
		t.Skipf("%s declares no resources", BaseVPCRepoClass)
	}

	declared := map[string]bool{}
	for _, key := range definitionKeys(FromDeclarations(declarations)) {
		declared[key] = true
	}
	for _, want := range BaseVPCProducedDefinitions {
		key := want.Namespace + "/" + want.Name
		if !declared[key] {
			t.Errorf("%s's meta-data/resources does not declare %s, which the fallback claims it produces; "+
				"a consumer requiring it resolves only when the repo is absent", BaseVPCRepoClass, key)
		}
	}
}
