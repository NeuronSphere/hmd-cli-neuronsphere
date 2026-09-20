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

func resourceEnv() Environment {
	return Environment{Slug: "dev2", DeploymentID: "dev2", K3sCluster: "ns-dev2-abc"}
}

func find(resources []Resource, name string) (Resource, bool) {
	for _, r := range resources {
		if r.Name == name {
			return r, true
		}
	}
	return Resource{}, false
}

func tagValue(r Resource, key string) string {
	for _, t := range r.Tags {
		if t.Key == key {
			return t.Value
		}
	}
	return ""
}

// The tag that made this whole subsystem necessary. hmd-database-account's
// create-service dependency selects
// application.neuronsphere.io/microservice with repo_class=hmd-ms-dbaccount,
// and a tag_selector matches Resources rather than produced types.
func TestServiceResourcesCarryTheRepoClassTag(t *testing.T) {
	t.Parallel()

	resources := LocalCoreResources(resourceEnv(), "neuronsphere_default", "", []ServiceSpec{
		{Name: "hmd_ms_dbaccount", RepoClass: "hmd-ms-dbaccount", APIBaseURL: "http://localhost/dev2/hmd_ms_dbaccount"},
	})
	r, ok := find(resources, "local-service-hmd_ms_dbaccount")
	if !ok {
		t.Fatalf("no microservice Resource for the service: %v", resources)
	}
	if r.Namespace != nsApplication || r.Definition != "microservice" {
		t.Errorf("typed %s/%s, want %s/microservice", r.Namespace, r.Definition, nsApplication)
	}
	if got := tagValue(r, "repo_class"); got != "hmd-ms-dbaccount" {
		t.Errorf("repo_class tag = %q, want hmd-ms-dbaccount", got)
	}
	if got := tagValue(r, "environment"); got != "dev2" {
		t.Errorf("environment tag = %q, want the slug", got)
	}
	if r.Output["api_base_url"] != "http://localhost/dev2/hmd_ms_dbaccount" {
		t.Errorf("api_base_url = %v", r.Output["api_base_url"])
	}
}

func TestLocalCoreResourcesAlwaysDescribeTheNetwork(t *testing.T) {
	t.Parallel()

	resources := LocalCoreResources(resourceEnv(), "neuronsphere_default-abc", "", nil)
	r, ok := find(resources, "neuronsphere_default-abc")
	if !ok {
		t.Fatalf("no docker-network Resource: %v", resources)
	}
	if r.Namespace != nsNetwork || r.Definition != "docker-network" {
		t.Errorf("typed %s/%s, want %s/docker-network", r.Namespace, r.Definition, nsNetwork)
	}
	if tagValue(r, "deployment_id") != "dev2" {
		t.Errorf("deployment_id tag = %q, want dev2", tagValue(r, "deployment_id"))
	}
}

// Without a cluster there is nothing to describe, and a Resource claiming a
// compute node or an ingress controller that does not exist is worse than none.
func TestClusterResourcesOnlyExistWithACluster(t *testing.T) {
	t.Parallel()

	without := LocalCoreResources(resourceEnv(), "net", "", nil)
	if _, ok := find(without, "ns-dev2-abc-traefik"); ok {
		t.Error("an ingress-controller Resource was built with no cluster")
	}
	with := LocalCoreResources(resourceEnv(), "net", "ns-dev2-abc", nil)
	compute, ok := find(with, "ns-dev2-abc-compute")
	if !ok {
		t.Fatalf("no compute-node Resource: %v", with)
	}
	if compute.Namespace != nsCompute {
		t.Errorf("compute typed %s, want %s", compute.Namespace, nsCompute)
	}
	traefik, ok := find(with, "ns-dev2-abc-traefik")
	if !ok {
		t.Fatalf("no ingress-controller Resource: %v", with)
	}
	// alb, not traefik: local Traefik answers to the cloud's ALB class so the
	// same chart renders the same Ingress in both places.
	if traefik.Output["ingress_class"] != "alb" {
		t.Errorf("ingress_class = %v, want alb", traefik.Output["ingress_class"])
	}
}

func TestSubmitResourcesPostsAgainstTheCoreDeployment(t *testing.T) {
	t.Parallel()

	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	s := &Seeder{Client: msdeploy.New(srv.URL)}
	resources := LocalCoreResources(resourceEnv(), "net", "", []ServiceSpec{
		{Name: "hmd_ms_dbaccount", RepoClass: "hmd-ms-dbaccount"},
	})
	if n := s.SubmitResources(context.Background(), "rid-1", resources); n != len(resources) {
		t.Fatalf("submitted %d of %d", n, len(resources))
	}
	for _, body := range bodies {
		if body["repo_instance_deployment_id"] != "rid-1" {
			t.Errorf("submitted against %v, want rid-1", body["repo_instance_deployment_id"])
		}
	}
}

// Without a deployment there is nothing to attach to. It has to say so: a
// silent skip here surfaces much later as a dependency that will not resolve.
func TestSubmitResourcesWarnsWithNoCoreDeployment(t *testing.T) {
	t.Parallel()

	var warnings []string
	s := &Seeder{Warn: func(format string, a ...any) { warnings = append(warnings, format) }}
	if n := s.SubmitResources(context.Background(), "", []Resource{{Name: "x"}}); n != 0 {
		t.Errorf("submitted %d, want 0", n)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want one", warnings)
	}
}

// One failed submission must not abort the rest: these are discovery records,
// and losing the environment over one is the worse trade.
func TestSubmitResourcesContinuesPastAFailure(t *testing.T) {
	t.Parallel()

	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"detail":"nope"}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	var warnings []string
	s := &Seeder{
		Client: msdeploy.New(srv.URL),
		Warn:   func(format string, a ...any) { warnings = append(warnings, format) },
	}
	n := s.SubmitResources(context.Background(), "rid-1", []Resource{{Name: "a"}, {Name: "b"}})
	if n != 1 {
		t.Errorf("submitted %d, want 1 of 2", n)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "could not submit") {
		t.Errorf("warnings = %v, want one naming the failure", warnings)
	}
}
