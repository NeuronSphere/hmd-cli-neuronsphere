package bom

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/repoclass"
)

// registerCatalogServer answers every request RegisterCatalog and Seed can
// make: an array for a CRUD search (POST /api/<entity>, find-first for
// EnsureEnvironment/EnsureDeploymentSet/NewChangeSetName), an object for
// everything else (PutEntity and every /apiop/<operation>), and a non-empty
// array for find_repo_class_versions specifically, since DeclareProduces
// refuses to declare anything without an id.
func registerCatalogServer(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/apiop/find_repo_class_versions"):
			_, _ = w.Write([]byte(`[{"identifier":"rcv-1","version":"0.1"}]`))
		case strings.HasPrefix(r.URL.Path, "/apiop/register_deployed_instance"):
			_, _ = w.Write([]byte(`{"repo_instance_id":"ri-1","repo_instance_deployment_id":"rid-1"}`))
		case strings.HasPrefix(r.URL.Path, "/api/") && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`[]`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	return srv, &calls
}

// RegisterCatalog is the changeset-independent half of Seed: it registers
// class versions, resource types and produces declarations, and the
// environment's own entities -- and it must never build or apply a
// ChangeSet. This is what makes it safe for `nsctl env plan` to call ahead of
// a dry-run validate_changeset.
func TestRegisterCatalogStopsBeforeTheChangeset(t *testing.T) {
	t.Parallel()

	srv, calls := registerCatalogServer(t)
	defer srv.Close()

	s := &Seeder{Client: msdeploy.New(srv.URL), Versions: declaringResolver{}}
	entries := []Entry{{RepoInstanceName: "local-neuronsphere", RepoClassName: "hmd-cli-neuronsphere", RepoClassVersion: "0.1"}}
	if err := s.RegisterCatalog(context.Background(), Environment{Slug: "local", AccountID: "1", Region: "reg1"}, entries); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"POST /apiop/add_repo_class_version",
		"POST /apiop/seed_base_resource_definitions",
		"PUT /api/hmd_lang_deployment.environment",
		"PUT /api/hmd_lang_deployment.deployment_set",
	}
	for _, w := range want {
		if !contains(*calls, w) {
			t.Errorf("RegisterCatalog never called %q; calls were %v", w, *calls)
		}
	}
	for _, c := range *calls {
		if strings.Contains(c, "change_set") || strings.Contains(c, "apply_changeset") || strings.Contains(c, "generate_local_deployment") {
			t.Fatalf("RegisterCatalog called %q; it must stop before the changeset", c)
		}
	}
}

// Seed performs RegisterCatalog's steps and then records the plan -- the
// extraction must not have dropped anything from the full sequence.
func TestSeedStillPerformsRegisterCatalogsSteps(t *testing.T) {
	t.Parallel()

	srv, calls := registerCatalogServer(t)
	defer srv.Close()

	s := &Seeder{Client: msdeploy.New(srv.URL), Versions: declaringResolver{}}
	_, err := s.Seed(context.Background(), Environment{Slug: "local", AccountID: "1", Region: "reg1"},
		[]Entry{{RepoInstanceName: "local-neuronsphere", RepoClassName: "hmd-cli-neuronsphere", RepoClassVersion: "0.1"}})
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"POST /apiop/add_repo_class_version",
		"POST /apiop/seed_base_resource_definitions",
		"POST /apiop/register_deployed_instance",
		"GET /apiop/get_deployment_config/local/local-neuronsphere",
	} {
		if !contains(*calls, want) {
			t.Errorf("Seed never called %q; calls were %v", want, *calls)
		}
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// treeResolver reports a version whose tree carries a default configuration,
// the way repoclass.Resolver does for a working tree.
type treeResolver struct {
	defaults map[string]any
}

func (r treeResolver) Resolve(repoClass, declared string) (string, map[string]any, map[string]any, error) {
	return "0.1", nil, r.defaults, nil
}

func (r treeResolver) Produces(string) ([]repoclass.ResourceDeclaration, error) { return nil, nil }

// existingVersionServer answers add_repo_class_version the way ms-deployment
// does for a version it already has, and serves that version with stored as
// its default configuration.
func existingVersionServer(t *testing.T, stored map[string]any) (*httptest.Server, *[]string) {
	t.Helper()
	var (
		mu    sync.Mutex
		calls []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/apiop/add_repo_class_version":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"message":"RepoClass, hmd-inf-otel-collector, already has version 0.1."}`))
		case strings.HasPrefix(r.URL.Path, "/apiop/find_repo_class_versions"):
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"identifier": "rcv-1", "version": "0.1", "default_configuration": stored},
			})
		case strings.HasPrefix(r.URL.Path, "/api/") && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`[]`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	return srv, &calls
}

func registerOtel(t *testing.T, srv *httptest.Server, resolver VersionResolver, instanceConfig map[string]any) {
	t.Helper()
	s := &Seeder{Client: msdeploy.New(srv.URL), Versions: resolver, Warn: func(string, ...any) {}}
	entries := []Entry{{
		RepoInstanceName: "otel-collector", RepoClassName: "hmd-inf-otel-collector",
		RepoClassVersion: "0.1", InstanceConfiguration: instanceConfig,
	}}
	if err := s.RegisterCatalog(context.Background(), Environment{Slug: "local", AccountID: "1", Region: "reg1"}, entries); err != nil {
		t.Fatal(err)
	}
}

// NERD034 SPEC001: the version is already registered, the tree's default has
// since changed, and the catalog row is updated rather than left stale.
func TestRegisterCatalogUpdatesAnExistingVersionsChangedDefault(t *testing.T) {
	t.Parallel()

	srv, calls := existingVersionServer(t, map[string]any{"exporter": "s3"})
	defer srv.Close()

	registerOtel(t, srv, treeResolver{defaults: map[string]any{"exporter": "clickhouse"}}, nil)
	if !contains(*calls, "PUT /api/hmd_lang_deployment.repo_class_version") {
		t.Errorf("the changed default was never written; calls were %v", *calls)
	}
}

func TestRegisterCatalogLeavesAnUnchangedDefaultAlone(t *testing.T) {
	t.Parallel()

	srv, calls := existingVersionServer(t, map[string]any{"exporter": "clickhouse"})
	defer srv.Close()

	registerOtel(t, srv, treeResolver{defaults: map[string]any{"exporter": "clickhouse"}}, nil)
	if contains(*calls, "PUT /api/hmd_lang_deployment.repo_class_version") {
		t.Errorf("an unchanged default was rewritten; calls were %v", *calls)
	}
}

// With no tree, RegisterCatalog registers the instance's own configuration as
// the class default. That is not a statement about the class and must never
// overwrite what the catalog already holds.
func TestRegisterCatalogNeverSyncsTheInstanceFallback(t *testing.T) {
	t.Parallel()

	srv, calls := existingVersionServer(t, map[string]any{"exporter": "s3"})
	defer srv.Close()

	registerOtel(t, srv, declaringResolver{}, map[string]any{"exporter": "instance-only"})
	for _, c := range *calls {
		if strings.HasPrefix(c, "POST /apiop/find_repo_class_versions/hmd-inf-otel-collector") || c == "PUT /api/hmd_lang_deployment.repo_class_version" {
			t.Errorf("the instance fallback reached the catalog: %q", c)
		}
	}
}
