package bom

import (
	"reflect"
	"sort"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
)

// The two lists are written out in both packages -- bom cannot be imported
// from manifest without a cycle -- so the duplication is checked here rather
// than trusted. A substrate instance a manifest may claim is one `env apply`
// can be talked into destroying.
func TestReservedNamesMatchTheSubstrate(t *testing.T) {
	t.Parallel()

	entries := Substrate(Environment{DeploymentID: "local"}, true)
	var substrate []string
	for _, e := range entries {
		substrate = append(substrate, e.RepoInstanceName)
	}
	sort.Strings(substrate)

	reserved := manifest.ReservedNames()
	if !reflect.DeepEqual(substrate, reserved) {
		t.Errorf("the substrate is %v but manifest reserves %v; the two lists have drifted",
			substrate, reserved)
	}
}

func TestDeclaredCarriesTheDeclarationThrough(t *testing.T) {
	t.Parallel()

	entries := Declared(Environment{DeploymentID: "local"}, []manifest.Repo{{
		InstanceName:          "my-api",
		RepoClassName:         "hmd-ms-myapi",
		Version:               "0.3",
		InstanceConfiguration: map[string]any{"replicas": 2},
		Dependencies:          map[string]any{"eks-cluster": "eks-cluster"},
	}})

	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	e := entries[0]
	if e.RepoInstanceName != "my-api" || e.RepoClassName != "hmd-ms-myapi" || e.RepoClassVersion != "0.3" {
		t.Errorf("entry = %+v", e)
	}
	if e.DeploymentID != "local" {
		t.Errorf("deployment_id = %q, want the environment's", e.DeploymentID)
	}
	if e.Dependencies["eks-cluster"] != "eks-cluster" {
		t.Errorf("dependencies = %v", e.Dependencies)
	}
}

// A nil map reaches the changeset as JSON null, which fails schema validation
// server-side; both keys are required and must be objects.
func TestDeclaredNeverLeavesANilMap(t *testing.T) {
	t.Parallel()

	e := Declared(Environment{DeploymentID: "local"}, []manifest.Repo{{
		InstanceName: "my-api", RepoClassName: "hmd-ms-myapi",
	}})[0]

	if e.InstanceConfiguration == nil || e.Dependencies == nil {
		t.Errorf("entry has a nil mapping: %+v", e)
	}
}

// Nothing is auto-wired: what a user declares is what gets deployed, so a
// dependency they cannot see is never added on their behalf.
func TestDeclaredAddsNoDependencies(t *testing.T) {
	t.Parallel()

	e := Declared(Environment{DeploymentID: "local"}, []manifest.Repo{{
		InstanceName: "my-api", RepoClassName: "hmd-ms-myapi",
	}})[0]

	if len(e.Dependencies) != 0 {
		t.Errorf("dependencies were invented: %v", e.Dependencies)
	}
}

// A declared instance sorts after the substrate it depends on, which is what
// makes `eks-cluster: eks-cluster` in a manifest work at all.
func TestDeclaredEntriesSortAfterTheSubstrate(t *testing.T) {
	t.Parallel()

	env := Environment{DeploymentID: "local"}
	entries := append(Substrate(env, true), Declared(env, []manifest.Repo{{
		InstanceName:  "my-api",
		RepoClassName: "hmd-ms-myapi",
		Dependencies:  map[string]any{"cluster": EKSClusterInstance},
	}})...)

	sorted, err := TopoSort(entries)
	if err != nil {
		t.Fatalf("TopoSort: %v", err)
	}
	positions := positionsOf(sorted)
	cluster, api := indexOf(positions, EKSClusterInstance), indexOf(positions, "my-api")
	if cluster < 0 || api < 0 {
		t.Fatalf("order = %v", positions)
	}
	if cluster > api {
		t.Errorf("the declared instance sorted before the cluster it needs: %v", positions)
	}
}

func indexOf(names []string, want string) int {
	for i, n := range names {
		if n == want {
			return i
		}
	}
	return -1
}

// The schema allows a role to name several instances, so the sort has to see
// every one of them or it can order a dependency after its dependent.
func TestDependencyTargetsReadsBothShapes(t *testing.T) {
	t.Parallel()

	got := DependencyTargets(map[string]any{
		"one":      "a",
		"many":     []any{"b", "c"},
		"typed":    []string{"d"},
		"nonsense": 42,
	})
	sort.Strings(got)
	want := []string{"a", "b", "c", "d"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DependencyTargets = %v, want %v", got, want)
	}
}

func TestTopoSortOrdersThroughAListDependency(t *testing.T) {
	t.Parallel()

	sorted, err := TopoSort([]Entry{
		{RepoInstanceName: "app", Dependencies: map[string]any{"needs": []any{"db", "cache"}}},
		{RepoInstanceName: "db"},
		{RepoInstanceName: "cache"},
	})
	if err != nil {
		t.Fatalf("TopoSort: %v", err)
	}
	if positionsOf(sorted)[2] != "app" {
		t.Errorf("order = %v, want app last", positionsOf(sorted))
	}
}
