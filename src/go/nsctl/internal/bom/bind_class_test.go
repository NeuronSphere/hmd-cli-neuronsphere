package bom

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
)

// A class-name dependency has no resource type, so the control plane never
// suggests anything for it. The class manifest still names the class it needs,
// and nsctl can read that.
func classDeps(role, class, required string) map[string]any {
	return map[string]any{role: map[string]any{"repo_class_name": class, "required": required}}
}

func bindByClass(t *testing.T, deps map[string]any, toBind []Entry, known []Entry) ([]Binding, []Ambiguity, []string) {
	t.Helper()
	srv := candidateServer(t, `{}`)
	t.Cleanup(srv.Close)
	var warned []string
	s := &Seeder{Client: msdeploy.New(srv.URL), Versions: depsResolver{deps: deps},
		Warn: func(f string, a ...any) { warned = append(warned, strings.TrimSpace(f)) }}
	bound, ambiguous, err := s.BindSuggested(context.Background(), "relpub", toBind, known)
	if err != nil {
		t.Fatal(err)
	}
	return bound, ambiguous, warned
}

func TestBindSuggestedBindsAClassNamedRoleToTheOnlyInstanceOfThatClass(t *testing.T) {
	t.Parallel()

	toBind := []Entry{consumer(nil)}
	known := []Entry{toBind[0], {RepoInstanceName: "argo", RepoClassName: "hmd-app-argo"}, {RepoInstanceName: "otel", RepoClassName: "hmd-inf-otel-collector"}}
	bound, _, _ := bindByClass(t, classDeps("argo", "hmd-app-argo", "true"), toBind, known)

	want := []Binding{{Instance: "ms-deployment", Role: "argo", Target: "argo"}}
	if !reflect.DeepEqual(bound, want) || toBind[0].Dependencies["argo"] != "argo" {
		t.Errorf("bound = %+v deps = %v, want argo bound", bound, toBind[0].Dependencies)
	}
}

func TestBindSuggestedLeavesAnOptionalClassNamedRoleAlone(t *testing.T) {
	t.Parallel()

	toBind := []Entry{consumer(nil)}
	known := []Entry{toBind[0], {RepoInstanceName: "cache", RepoClassName: "hmd-inf-redis"}}
	bound, _, _ := bindByClass(t, classDeps("redis", "hmd-inf-redis", "false"), toBind, known)
	if len(bound) != 0 {
		t.Errorf("an optional role was bound: %+v", bound)
	}
}

func TestBindSuggestedNeverRebindsAClassNamedRoleTheManifestBinds(t *testing.T) {
	t.Parallel()

	toBind := []Entry{consumer(map[string]any{"argo": "my-argo"})}
	known := []Entry{toBind[0], {RepoInstanceName: "argo", RepoClassName: "hmd-app-argo"}}
	bound, _, _ := bindByClass(t, classDeps("argo", "hmd-app-argo", "true"), toBind, known)
	if len(bound) != 0 || toBind[0].Dependencies["argo"] != "my-argo" {
		t.Errorf("bound = %+v deps = %v, want the declared my-argo kept", bound, toBind[0].Dependencies)
	}
}

func TestBindSuggestedListsTwoInstancesOfAClassNamedRolesClass(t *testing.T) {
	t.Parallel()

	toBind := []Entry{consumer(nil)}
	known := []Entry{toBind[0],
		{RepoInstanceName: "b2", RepoClassName: "hmd-inf-s3bucket"},
		{RepoInstanceName: "b1", RepoClassName: "hmd-inf-s3bucket"}}
	bound, ambiguous, _ := bindByClass(t, classDeps("buckets", "hmd-inf-s3bucket", "true"), toBind, known)
	if len(bound) != 0 || len(ambiguous) != 1 || !reflect.DeepEqual(ambiguous[0].Candidates, []string{"b1", "b2"}) {
		t.Errorf("bound = %+v ambiguous = %+v, want b1/b2 ambiguous", bound, ambiguous)
	}
}

// When nothing in the environment is of the class, the useful answer is the
// command that adds one, not the validator's bare "role is not supplied".
func TestBindSuggestedNamesTheCommandThatAddsAMissingClass(t *testing.T) {
	t.Parallel()

	toBind := []Entry{consumer(nil)}
	bound, _, warned := bindByClass(t, classDeps("logging", "hmd-inf-api-gateway", "true"), toBind, toBind)
	if len(bound) != 0 {
		t.Errorf("bound = %+v, want none", bound)
	}
	found := false
	for _, w := range warned {
		if strings.Contains(w, "nsctl instance add") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings %v never name `nsctl instance add`", warned)
	}
}

// A role with a resource block is the suggestion path's to bind.
func TestBindSuggestedLeavesAResourceRoleToTheSuggestionPath(t *testing.T) {
	t.Parallel()

	deps := map[string]any{"workers": map[string]any{"repo_class_name": "hmd-inf-eks-node-group", "required": "true",
		"resource": map[string]any{"resource_namespace": "compute.neuronsphere.io"}}}
	toBind := []Entry{consumer(nil)}
	known := []Entry{toBind[0], {RepoInstanceName: "nodes", RepoClassName: "hmd-inf-eks-node-group"}}
	bound, _, _ := bindByClass(t, deps, toBind, known)
	if len(bound) != 0 {
		t.Errorf("bound = %+v, want none: the resource path owns this role", bound)
	}
}

// An apply seeds only what needs deploying, but the instance a role binds to
// may be one that is already deployed and unchanged. Seed must see the whole
// plan, or apply fails on a role the plan had bound.
func TestSeedBindsToAnUnchangedInstanceItIsNotDeploying(t *testing.T) {
	t.Parallel()

	srv, _ := registerCatalogServer(t)
	defer srv.Close()

	var notes []string
	s := &Seeder{
		Client:   msdeploy.New(srv.URL),
		Versions: depsResolver{deps: classDeps("argo", "hmd-app-argo", "true")},
		Info:     func(f string, a ...any) { notes = append(notes, fmt.Sprintf(f, a...)) },
		Known:    []Entry{{RepoInstanceName: "argo", RepoClassName: "hmd-app-argo"}},
	}
	entries := []Entry{consumer(nil)}
	if _, err := s.Seed(context.Background(), Environment{Slug: "relpub", AccountID: "1", Region: "r"}, entries); err != nil {
		t.Fatal(err)
	}
	if entries[0].Dependencies["argo"] != "argo" {
		t.Errorf("argo = %v, want argo; notes = %v", entries[0].Dependencies["argo"], notes)
	}
}
