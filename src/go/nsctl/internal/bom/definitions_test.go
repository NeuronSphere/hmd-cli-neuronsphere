package bom

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/repoclass"
)

// declaringResolver answers with a fixed set of declarations.
type declaringResolver struct {
	declarations map[string][]repoclass.ResourceDeclaration
}

func (r declaringResolver) Resolve(repoClass, declared string) (string, map[string]any, map[string]any, error) {
	if declared == "" {
		declared = "0.1"
	}
	return declared, nil, nil, nil
}

func (r declaringResolver) Produces(repoClass string) ([]repoclass.ResourceDeclaration, error) {
	return r.declarations[repoClass], nil
}

// auroraPostgres is hmd-postgres-rds's own declaration, the one whose absence
// from the graph produced
//
//	declaring aws.neuronsphere.io/aurora-postgres:
//	declare_produces_resource_definition: HTTP 400: ... not found
//
// on the first cold start after a purge.
var auroraPostgres = repoclass.ResourceDeclaration{
	Namespace:        "aws.neuronsphere.io",
	Name:             "aurora-postgres",
	Version:          "0.1.0",
	Role:             "database",
	Produces:         true,
	Description:      "An Amazon Aurora PostgreSQL-compatible cluster.",
	ResourceMetadata: map[string]any{"provider": "aws"},
	OutputSchema:     map[string]any{"type": "object"},
	Parent: &repoclass.ResourceRef{
		Namespace: "database.neuronsphere.io",
		Name:      "postgres",
		Version:   "0.1.0",
	},
}

// recorder captures the apiop calls in order.
func recorder(t *testing.T) (*httptest.Server, *[]string, *[]map[string]any) {
	t.Helper()
	var ops []string
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		op := strings.TrimPrefix(r.URL.Path, "/apiop/")
		ops = append(ops, op)
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(op, "find_repo_class_versions") {
			// An array, and non-empty: DeclareProduces needs an id, and refuses
			// to declare anything without one.
			_, _ = w.Write([]byte(`[{"identifier":"rcv-1","version":"0.1"}]`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	return srv, &ops, &bodies
}

func TestUpsertResourceDefinitionsForwardsTheRepoDocument(t *testing.T) {
	t.Parallel()

	srv, ops, bodies := recorder(t)
	defer srv.Close()

	s := &Seeder{
		Client: msdeploy.New(srv.URL),
		Versions: declaringResolver{map[string][]repoclass.ResourceDeclaration{
			"hmd-postgres-rds": {auroraPostgres},
		}},
	}
	if err := s.UpsertResourceDefinitions(context.Background(), "hmd-postgres-rds"); err != nil {
		t.Fatal(err)
	}

	if len(*ops) != 1 || (*ops)[0] != "upsert_resource_definition" {
		t.Fatalf("called %v, want one upsert_resource_definition", *ops)
	}
	got := (*bodies)[0]
	for key, want := range map[string]any{
		"resource_namespace":       "aws.neuronsphere.io",
		"resource_definition_name": "aurora-postgres",
		"version":                  "0.1.0",
	} {
		if got[key] != want {
			t.Errorf("%s = %v, want %v", key, got[key], want)
		}
	}
	// The parent is the whole reason the base catalog is seeded first; a
	// definition sent without it is orphaned from the hierarchy that resolution
	// walks.
	parent, ok := got["parent"].(map[string]any)
	if !ok {
		t.Fatalf("parent = %v, want the declared parent", got["parent"])
	}
	if parent["resource_definition_name"] != "postgres" {
		t.Errorf("parent = %v, want database.neuronsphere.io/postgres", parent)
	}
	for _, key := range []string{"description", "resource_metadata", "output_schema"} {
		if got[key] == nil {
			t.Errorf("%s was dropped; the repo's document is forwarded as it stands", key)
		}
	}
}

// A repo that declares nothing is the ordinary case and must not call anything.
func TestUpsertResourceDefinitionsIsQuietWithNoDeclarations(t *testing.T) {
	t.Parallel()

	srv, ops, _ := recorder(t)
	defer srv.Close()

	s := &Seeder{Client: msdeploy.New(srv.URL), Versions: declaringResolver{nil}}
	if err := s.UpsertResourceDefinitions(context.Background(), "hmd-inf-something"); err != nil {
		t.Fatal(err)
	}
	if len(*ops) != 0 {
		t.Errorf("called %v, want nothing", *ops)
	}
}

// An upsert that fails is a warning, not a failed start: DeclareProduces is the
// operation that needs the type and reports its absence precisely.
func TestUpsertResourceDefinitionsWarnsRatherThanFailing(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"detail":"nope"}`))
	}))
	defer srv.Close()

	var warnings []string
	s := &Seeder{
		Client: msdeploy.New(srv.URL),
		Versions: declaringResolver{map[string][]repoclass.ResourceDeclaration{
			"hmd-postgres-rds": {auroraPostgres},
		}},
		Warn: func(format string, a ...any) { warnings = append(warnings, format) },
	}
	if err := s.UpsertResourceDefinitions(context.Background(), "hmd-postgres-rds"); err != nil {
		t.Fatalf("UpsertResourceDefinitions = %v, want a warning and no error", err)
	}
	if len(warnings) != 1 {
		t.Errorf("warnings = %v, want one", warnings)
	}
}

// The ordering is the bug. Seeding the base catalog and upserting a repo's own
// types has to happen before anything declares producing one, or the declare
// 400s on a graph that has never seen the type.
func TestSeedRegistersResourceTypesBeforeDeclaringProduces(t *testing.T) {
	t.Parallel()

	srv, ops, _ := recorder(t)
	defer srv.Close()

	s := &Seeder{
		Client: msdeploy.New(srv.URL),
		Versions: declaringResolver{map[string][]repoclass.ResourceDeclaration{
			"hmd-postgres-rds": {auroraPostgres},
		}},
	}
	// Seed fails further along -- the fake answers {} to everything, so there is
	// no deployment id to register -- which is fine: the ordering under test is over by
	// then.
	_, _ = s.Seed(context.Background(), Environment{Slug: "local", AccountID: "1", Region: "reg1"},
		[]Entry{{RepoInstanceName: "environment-db", RepoClassName: "hmd-postgres-rds", RepoClassVersion: "0.8"}})

	indexOf := func(op string) int {
		for i, got := range *ops {
			if got == op {
				return i
			}
		}
		return -1
	}
	base := indexOf("seed_base_resource_definitions")
	upsert := indexOf("upsert_resource_definition")
	declare := indexOf("declare_produces_resource_definition")

	if base < 0 {
		t.Fatalf("the base catalog was never seeded; calls were %v", *ops)
	}
	if upsert < 0 {
		t.Fatalf("hmd-postgres-rds's own types were never registered; calls were %v", *ops)
	}
	if declare < 0 {
		t.Fatalf("nothing declared what it produces; calls were %v", *ops)
	}
	if !(base < upsert && upsert < declare) {
		t.Errorf("order was base=%d upsert=%d declare=%d; want base < upsert < declare (calls: %v)",
			base, upsert, declare, *ops)
	}
}

// discoveringResolver also answers ResolveDiscovery, per repo class.
type discoveringResolver struct {
	declaringResolver
	discovery map[string]map[string]any
}

func (r discoveringResolver) ResolveDiscovery(repoClass, declared string) (map[string]any, error) {
	return r.discovery[repoClass], nil
}

// NERD0013 SPEC0005 (hmd-ms-deployment): the registered version carries the
// manifest's discovery block, and the key is absent -- not empty -- when the
// tree declares none, so the service default applies. A resolver that cannot
// answer for discovery at all (the three older fakes) is still accepted.
func TestSeedIncludesDiscoveryOnlyWhenPresent(t *testing.T) {
	t.Parallel()

	srv, ops, bodies := recorder(t)
	defer srv.Close()

	s := &Seeder{
		Client: msdeploy.New(srv.URL),
		Versions: discoveringResolver{
			declaringResolver: declaringResolver{},
			discovery: map[string]map[string]any{
				"hmd-with": {"summary": "Does things.", "capabilities": []any{}},
			},
		},
	}
	_, _ = s.Seed(context.Background(), Environment{Slug: "local", AccountID: "1", Region: "reg1"},
		[]Entry{
			{RepoInstanceName: "with", RepoClassName: "hmd-with", RepoClassVersion: "0.1"},
			{RepoInstanceName: "without", RepoClassName: "hmd-without", RepoClassVersion: "0.1"},
		})

	registered := map[string]map[string]any{}
	for i, op := range *ops {
		if op == "add_repo_class_version" {
			registered[(*bodies)[i]["repo_class_name"].(string)] = (*bodies)[i]
		}
	}
	with, ok := registered["hmd-with"]
	if !ok {
		t.Fatalf("hmd-with was never registered; calls were %v", *ops)
	}
	d, _ := with["discovery"].(map[string]any)
	if d["summary"] != "Does things." {
		t.Errorf("hmd-with discovery = %v, want the manifest's block", with["discovery"])
	}
	without, ok := registered["hmd-without"]
	if !ok {
		t.Fatalf("hmd-without was never registered; calls were %v", *ops)
	}
	if _, present := without["discovery"]; present {
		t.Errorf("hmd-without carried a discovery key: %v", without)
	}
}
