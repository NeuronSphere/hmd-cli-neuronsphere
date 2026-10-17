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

// resourcesResolver answers with a class manifest's deploy.resources, as a
// working tree's manifest does (NERD036).
type resourcesResolver struct {
	depsResolver
	resources map[string]any
}

func (r resourcesResolver) ResolveResources(repoClass, declared string) (map[string]any, error) {
	return r.resources, nil
}

func capturingServer(t *testing.T, added, resynced *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/apiop/add_repo_class_version":
			body, _ := io.ReadAll(r.Body)
			*added = append(*added, string(body))
			_, _ = w.Write([]byte(`{}`))
		case r.URL.Path == "/apiop/resync_repo_class_version_dependencies":
			body, _ := io.ReadAll(r.Body)
			*resynced = append(*resynced, string(body))
			_, _ = w.Write([]byte(`{}`))
		case strings.HasPrefix(r.URL.Path, "/apiop/find_repo_class_versions"):
			_, _ = w.Write([]byte(`[{"identifier":"rcv-1","version":"0.1"}]`))
		case strings.HasPrefix(r.URL.Path, "/api/") && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`[]`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
}

var librarianResources = map[string]any{
	"api": map[string]any{
		"resource_namespace":       "aws.neuronsphere.io",
		"resource_definition_name": "api-gateway-rest-api",
		"version":                  "0.1.0",
		"metadata":                 map[string]any{"connects_to": []any{"function"}},
	},
}

// The architecture model draws from the resources a class version declares,
// so they ride along with registration and with the resync that carries a
// manifest edit to an already-registered version.
func TestRegisterCatalogSendsTheManifestsDeclaredResources(t *testing.T) {
	t.Parallel()

	var added, resynced []string
	srv := capturingServer(t, &added, &resynced)
	defer srv.Close()

	deps := map[string]any{"lib-repo": map[string]any{"repo_class_name": "hmd-inf-s3bucket", "required": "true"}}
	s := &Seeder{Client: msdeploy.New(srv.URL), Versions: resourcesResolver{depsResolver{deps: deps}, librarianResources}}
	entries := []Entry{{RepoInstanceName: "device", RepoClassName: "hmd-ms-device-lib", RepoClassVersion: "0.1"}}
	if err := s.RegisterCatalog(context.Background(), Environment{Slug: "local", AccountID: "1", Region: "r"}, entries); err != nil {
		t.Fatal(err)
	}
	for name, calls := range map[string][]string{"add_repo_class_version": added, "resync": resynced} {
		if len(calls) != 1 {
			t.Fatalf("%s calls = %d, want 1", name, len(calls))
		}
		for _, want := range []string{`"resources"`, `"api-gateway-rest-api"`, `"connects_to":["function"]`} {
			if !strings.Contains(calls[0], want) {
				t.Errorf("%s payload %s lacks %s", name, calls[0], want)
			}
		}
	}
}

// A class with resources and no dependencies still has something to resync.
func TestRegisterCatalogResyncsResourcesWithoutDependencies(t *testing.T) {
	t.Parallel()

	var added, resynced []string
	srv := capturingServer(t, &added, &resynced)
	defer srv.Close()

	s := &Seeder{Client: msdeploy.New(srv.URL), Versions: resourcesResolver{resources: librarianResources}}
	entries := []Entry{{RepoInstanceName: "bucket", RepoClassName: "hmd-inf-s3bucket", RepoClassVersion: "0.1"}}
	if err := s.RegisterCatalog(context.Background(), Environment{Slug: "local", AccountID: "1", Region: "r"}, entries); err != nil {
		t.Fatal(err)
	}
	if len(resynced) != 1 || !strings.Contains(resynced[0], `"resources"`) {
		t.Errorf("resync = %v, want one carrying resources", resynced)
	}
}

// BACON lets deploy.resources declare a produced type (produces defaults to
// true), beside meta-data/resources/*.yaml. Both reach declare_produces; a
// declaration marked produces: false does not.
func TestProducedByIncludesTheManifestsProducedResources(t *testing.T) {
	t.Parallel()

	s := &Seeder{Versions: resourcesResolver{resources: map[string]any{
		"bucket": map[string]any{
			"resource_namespace":       "aws.neuronsphere.io",
			"resource_definition_name": "s3-bucket",
			"version":                  "0.1.0",
			"role":                     "bucket",
		},
		"reads": map[string]any{
			"resource_namespace":       "aws.neuronsphere.io",
			"resource_definition_name": "dynamodb-table",
			"version":                  "0.1.0",
			"produces":                 false,
		},
	}}}
	defs, err := s.producedBy("hmd-inf-s3bucket")
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 1 || defs[0].Name != "s3-bucket" || defs[0].Version != "0.1.0" || defs[0].Role != "bucket" {
		t.Errorf("producedBy = %+v, want only the s3-bucket", defs)
	}
}
