package bom

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
)

// depsResolver answers with a class manifest's dependencies, as a working
// tree's manifest does.
type depsResolver struct {
	declaringResolver
	deps map[string]any
}

func (r depsResolver) Resolve(repoClass, declared string) (string, map[string]any, map[string]any, error) {
	v, _, cfg, err := r.declaringResolver.Resolve(repoClass, declared)
	return v, r.deps, cfg, err
}

// The control plane refuses to re-register a version, so a manifest edited
// after the first registration never reached it. RegisterCatalog now resyncs
// the version's dependency edges from the manifest it just read, so an edit
// takes effect without a version bump.
func TestRegisterCatalogResyncsAnAlreadyRegisteredVersionsDependencies(t *testing.T) {
	t.Parallel()

	var resynced []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/apiop/add_repo_class_version":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"message":"RepoClass, hmd-ms-deployment, already has version 0.4"}`))
		case r.URL.Path == "/apiop/resync_repo_class_version_dependencies":
			body, _ := io.ReadAll(r.Body)
			resynced = append(resynced, string(body))
			_, _ = w.Write([]byte(`{}`))
		case strings.HasPrefix(r.URL.Path, "/apiop/find_repo_class_versions"):
			_, _ = w.Write([]byte(`[{"identifier":"rcv-1","version":"0.4"}]`))
		case strings.HasPrefix(r.URL.Path, "/api/") && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`[]`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	deps := map[string]any{"workers": map[string]any{"repo_class_name": "hmd-inf-eks-node-group", "required": "false"}}
	s := &Seeder{Client: msdeploy.New(srv.URL), Versions: depsResolver{deps: deps}}
	entries := []Entry{{RepoInstanceName: "ms-deployment", RepoClassName: "hmd-ms-deployment", RepoClassVersion: "0.4"}}
	if err := s.RegisterCatalog(context.Background(), Environment{Slug: "relpub", AccountID: "1", Region: "r"}, entries); err != nil {
		t.Fatal(err)
	}
	if len(resynced) != 1 {
		t.Fatalf("resync calls = %d, want 1", len(resynced))
	}
	for _, want := range []string{`"repo_class_name":"hmd-ms-deployment"`, `"version":"0.4"`, `"workers"`, `"required":"false"`} {
		if !strings.Contains(resynced[0], want) {
			t.Errorf("resync payload %s lacks %s", resynced[0], want)
		}
	}
}

func TestRegisterCatalogSkipsTheResyncWhenTheClassDeclaresNoDependencies(t *testing.T) {
	t.Parallel()

	srv, calls := registerCatalogServer(t)
	defer srv.Close()

	s := &Seeder{Client: msdeploy.New(srv.URL), Versions: declaringResolver{}}
	entries := []Entry{{RepoInstanceName: "x", RepoClassName: "hmd-inf-x", RepoClassVersion: "0.1"}}
	if err := s.RegisterCatalog(context.Background(), Environment{Slug: "relpub", AccountID: "1", Region: "r"}, entries); err != nil {
		t.Fatal(err)
	}
	if contains(*calls, "POST /apiop/resync_repo_class_version_dependencies") {
		t.Errorf("resynced a class with no manifest dependencies: %v", *calls)
	}
}

// Registration succeeded; a resync that fails must not take the plan down.
func TestRegisterCatalogToleratesAFailedResync(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/apiop/resync_repo_class_version_dependencies":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"boom"}`))
		case strings.HasPrefix(r.URL.Path, "/apiop/find_repo_class_versions"):
			_, _ = w.Write([]byte(`[{"identifier":"rcv-1","version":"0.4"}]`))
		case strings.HasPrefix(r.URL.Path, "/api/") && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`[]`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	var warned []string
	s := &Seeder{Client: msdeploy.New(srv.URL), Versions: depsResolver{deps: map[string]any{"a": map[string]any{}}},
		Warn: func(f string, a ...any) { warned = append(warned, f) }}
	entries := []Entry{{RepoInstanceName: "x", RepoClassName: "hmd-inf-x", RepoClassVersion: "0.1"}}
	if err := s.RegisterCatalog(context.Background(), Environment{Slug: "relpub", AccountID: "1", Region: "r"}, entries); err != nil {
		t.Fatalf("err = %v, want the failed resync tolerated", err)
	}
	if len(warned) == 0 {
		t.Error("a failed resync was not reported")
	}
}
