package bom

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
)

// planServer answers the catalogue calls like registerCatalogServer and
// records every register_deployed_instance body and every configuration fetch,
// handing back a RID per instance and a configuration that names it.
func planServer(t *testing.T) (*httptest.Server, *[]string, *[]map[string]any) {
	t.Helper()
	var calls []string
	var registered []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/apiop/find_repo_class_versions"):
			_, _ = w.Write([]byte(`[{"identifier":"rcv-1","version":"0.1"}]`))
		case strings.HasPrefix(r.URL.Path, "/apiop/register_deployed_instance"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			registered = append(registered, body)
			name, _ := body["instance_name"].(string)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"repo_instance_id": "ri-" + name, "repo_instance_deployment_id": "rid-" + name,
			})
		case strings.HasPrefix(r.URL.Path, "/apiop/get_deployment_config/"):
			name := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			_ = json.NewEncoder(w).Encode(map[string]any{"instance_name": name, "resolved": true})
		case strings.HasPrefix(r.URL.Path, "/api/") && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`[]`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	return srv, &calls, &registered
}

// Seed records every entry as a DEPLOY_NEXT plan, dependencies first, forwards
// the role bindings, and only then asks for configurations -- because a
// configuration resolves against dependencies that have to exist already.
func TestSeedRegistersInDependencyOrderThenFetchesConfig(t *testing.T) {
	t.Parallel()

	srv, calls, registered := planServer(t)
	defer srv.Close()

	s := &Seeder{Client: msdeploy.New(srv.URL), Versions: declaringResolver{}, Region: "reg1"}
	nodes, err := s.Seed(context.Background(), Environment{Slug: "dev2", AccountID: "1", Region: "reg1"},
		[]Entry{
			{RepoInstanceName: "api", RepoClassName: "acme-api", RepoClassVersion: "0.3", DeploymentID: "aaa",
				Dependencies: map[string]any{"db": "warehouse", "bucket": []any{"landing"}}},
			{RepoInstanceName: "warehouse", RepoClassName: "hmd-postgres-rds", RepoClassVersion: "0.8", DeploymentID: "aaa"},
			{RepoInstanceName: "landing", RepoClassName: "hmd-inf-s3bucket", RepoClassVersion: "0.2", DeploymentID: "aaa"},
		})
	if err != nil {
		t.Fatal(err)
	}

	var order []string
	for _, body := range *registered {
		order = append(order, body["instance_name"].(string))
		if body["status"] != msdeploy.StatusDeployNext {
			t.Errorf("%s registered with status %v, want %s", body["instance_name"], body["status"], msdeploy.StatusDeployNext)
		}
		if body["environment"] != "dev2" {
			t.Errorf("%s registered into environment %v, want dev2", body["instance_name"], body["environment"])
		}
	}
	if len(order) != 3 || order[2] != "api" {
		t.Fatalf("registration order = %v; api must come after warehouse and landing", order)
	}
	api := (*registered)[2]
	if deps, _ := api["dependencies"].(map[string]any); deps["db"] != "warehouse" {
		t.Errorf("api's dependencies were not forwarded: %v", api["dependencies"])
	}

	// Every registration precedes every configuration fetch.
	lastRegister, firstConfig := -1, len(*calls)
	for i, c := range *calls {
		if strings.HasPrefix(c, "POST /apiop/register_deployed_instance") {
			lastRegister = i
		}
		if strings.HasPrefix(c, "GET /apiop/get_deployment_config/dev2/") && i < firstConfig {
			firstConfig = i
		}
	}
	if firstConfig < lastRegister {
		t.Errorf("a configuration was fetched before every instance was registered: %v", *calls)
	}

	byName := map[string]msdeploy.DeploymentNode{}
	for _, n := range nodes {
		byName[n.InstanceName] = n
	}
	if got := byName["api"]; got.RIDNid != "rid-api" {
		t.Errorf("api RIDNid = %q, want rid-api", got.RIDNid)
	}
	if got := byName["api"].Script; !strings.Contains(got, "export HMD_REPO_INSTANCE_DEPLOYMENT_ID=rid-api\n") ||
		!strings.Contains(got, "--environment dev2") || !strings.Contains(got, `"resolved": true`) {
		t.Errorf("api's script does not carry the RID, the environment and the resolved configuration:\n%s", got)
	}
	// Dependencies on the node are the batch-local edges the scheduler orders on.
	if got := byName["api"].Dependencies; len(got) != 2 {
		t.Errorf("api.Dependencies = %v, want warehouse and landing", got)
	}
	if got := byName["warehouse"].Dependencies; len(got) != 0 {
		t.Errorf("warehouse.Dependencies = %v, want none", got)
	}
}

// A role bound to an instance outside the batch -- something already deployed
// -- is forwarded to the service (which resolves it) but is not a scheduling
// edge, since nothing here will deploy it.
func TestSeedDependenciesOutsideTheBatchAreNotEdges(t *testing.T) {
	t.Parallel()

	srv, _, registered := planServer(t)
	defer srv.Close()

	s := &Seeder{Client: msdeploy.New(srv.URL), Versions: declaringResolver{}}
	nodes, err := s.Seed(context.Background(), Environment{Slug: "local", AccountID: "1", Region: "reg1"},
		[]Entry{{RepoInstanceName: "api", RepoClassName: "acme-api", RepoClassVersion: "0.3", DeploymentID: "aaa",
			Dependencies: map[string]any{"db": "already-there"}}})
	if err != nil {
		t.Fatal(err)
	}
	if deps, _ := (*registered)[0]["dependencies"].(map[string]any); deps["db"] != "already-there" {
		t.Errorf("the binding was not forwarded: %v", (*registered)[0]["dependencies"])
	}
	if len(nodes[0].Dependencies) != 0 {
		t.Errorf("api.Dependencies = %v, want none", nodes[0].Dependencies)
	}
}

// A registration the service refuses stops the seed with the instance named:
// nothing later can resolve against a dependency that was never recorded.
func TestSeedStopsAtARefusedRegistration(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/apiop/find_repo_class_versions"):
			_, _ = w.Write([]byte(`[{"identifier":"rcv-1","version":"0.1"}]`))
		case strings.HasPrefix(r.URL.Path, "/apiop/register_deployed_instance"):
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"message":"No repo instance found for name, nope"}`))
		case strings.HasPrefix(r.URL.Path, "/api/") && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`[]`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	s := &Seeder{Client: msdeploy.New(srv.URL), Versions: declaringResolver{}}
	_, err := s.Seed(context.Background(), Environment{Slug: "local", AccountID: "1", Region: "reg1"},
		[]Entry{{RepoInstanceName: "api", RepoClassName: "acme-api", RepoClassVersion: "0.3", DeploymentID: "aaa",
			Dependencies: map[string]any{"db": "nope"}}})
	if err == nil || !strings.Contains(err.Error(), "registering api") {
		t.Fatalf("err = %v, want the refused registration named", err)
	}
}
