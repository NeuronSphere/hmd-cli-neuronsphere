package msdeploy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nsconfig"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/tokenstore"
)

func cloudLookup(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

// TestCloudSendsTheRawToken pins the header format against hmd_rest_client's
// _get_headers. A `Bearer ` prefix is the obvious thing to write and it fails
// authentication against a service that works for every Python client.
func TestCloudSendsTheRawToken(t *testing.T) {
	t.Parallel()

	var gotAuth, gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotKey = r.Header.Get("x-api-key")
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()

	c, err := NewCloud(CloudConfig{
		Home:    t.TempDir(),
		FlagURL: srv.URL,
		Lookup:  cloudLookup(map[string]string{AuthTokenEnv: "a-token", APIKeyEnv: "a-key"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.DeploymentBOM(context.Background(), "dev"); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "a-token" {
		t.Errorf("Authorization = %q, want the raw token with no Bearer prefix", gotAuth)
	}
	if gotKey != "a-key" {
		t.Errorf("x-api-key = %q, want both credentials sent together", gotKey)
	}
}

// TestCloudReadsTheCachedToken is the ordinary path: somebody ran `nsctl login`
// and nothing else is configured.
func TestCloudReadsTheCachedToken(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".cache"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, tokenstore.RelPath),
		[]byte("login:\n  access_token: from-the-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()

	c, err := NewCloud(CloudConfig{Home: home, FlagURL: srv.URL, Lookup: cloudLookup(nil)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.DeploymentBOM(context.Background(), "dev"); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "from-the-file" {
		t.Errorf("Authorization = %q, want the token nsctl login cached", gotAuth)
	}
}

// TestCloudRefusesWithNoCredential fails at construction rather than at the
// first request, so "you never set this up" and "your token expired" stay
// distinguishable.
func TestCloudRefusesWithNoCredential(t *testing.T) {
	t.Parallel()

	_, err := NewCloud(CloudConfig{
		Home:    t.TempDir(),
		FlagURL: "https://deploy.example",
		Lookup:  cloudLookup(nil),
	})
	if err == nil {
		t.Fatal("NewCloud succeeded with no credential at all")
	}
	if !strings.Contains(err.Error(), "nsctl login") {
		t.Errorf("error = %v, want one naming the remedy", err)
	}
}

// TestLocalClientSendsNoCredentials guards the other direction: the control
// plane's librarian and deployment service are anonymous on loopback, and
// growing a credential field must not start sending one.
func TestLocalClientSendsNoCredentials(t *testing.T) {
	t.Parallel()

	var headers http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers = r.Header.Clone()
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()

	if _, err := New(srv.URL).Search(context.Background(), EntityEnvironment, Filter{}); err != nil {
		t.Fatal(err)
	}
	if got := headers.Get("Authorization"); got != "" {
		t.Errorf("a local client sent Authorization: %q", got)
	}
	if got := headers.Get("x-api-key"); got != "" {
		t.Errorf("a local client sent x-api-key: %q", got)
	}
}

// TestAPIOpGetUsesGET is the mechanical reason this method exists:
// get_deployment_bom is declared GET, and a GET operation called with POST
// answers 405.
func TestAPIOpGetUsesGET(t *testing.T) {
	t.Parallel()

	var method, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		_, _ = w.Write([]byte(`[{"repo_instance_name":"a"}]`))
	}))
	defer srv.Close()

	c := New(srv.URL)
	body, err := c.APIOpGet(context.Background(), "get_deployment_bom/dev")
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodGet {
		t.Errorf("method = %s, want GET", method)
	}
	if want := "/apiop/get_deployment_bom/dev"; path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	// And the array survives, which APIOp would have discarded into an empty
	// map -- a decoder that silently returns nothing is worse than one that
	// errors.
	if !strings.HasPrefix(strings.TrimSpace(string(body)), "[") {
		t.Errorf("body = %q, want the array intact", body)
	}
	if got, _ := c.APIOp(context.Background(), "whatever", nil); len(got) != 0 {
		t.Log("APIOp on an array body returns an empty map, which is why APIOpGet is raw")
	}
}

// TestBOMDecodesEveryShape is the decoder contract.
//
// The fixture is SYNTHESISED from deploy_bom_creator.create_node_bom's field
// order, not captured from a live service -- NERD012 acceptance criterion 1 is
// what closes that gap, and it is not yet met. What this test does prove is
// that the conditional keys behave as the Python's branches say: an image_only
// entry carries no configuration and no dependencies, and a role is a bare
// string for one target and a list for several.
func TestBOMDecodesEveryShape(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile(filepath.Join("testdata", "bom_synthesised.json"))
	if err != nil {
		t.Fatal(err)
	}
	var bom []BOMEntry
	if err := json.Unmarshal(data, &bom); err != nil {
		t.Fatal(err)
	}
	if len(bom) != 4 {
		t.Fatalf("decoded %d entries, want 4", len(bom))
	}

	byName := map[string]BOMEntry{}
	for _, e := range bom {
		byName[e.RepoInstanceName] = e
	}

	image := byName["transform-image"]
	if !image.ImageOnly {
		t.Error("image_only was not decoded")
	}
	if image.InstanceConfiguration != nil || image.Dependencies != nil {
		t.Error("an image_only entry came back with configuration or dependencies invented for it")
	}

	transform := byName["ms-transform"]
	if transform.RepoClassVersion != "0.5.201" {
		t.Errorf("repo_class_version = %q, want the concrete version", transform.RepoClassVersion)
	}
	if got := transform.Targets("database-instance"); !reflect.DeepEqual(got, []string{"environment-db"}) {
		t.Errorf("a single-target role decoded as %v", got)
	}
	if got := transform.Targets("librarian"); !reflect.DeepEqual(got, []string{"artifact-lib", "device-lib"}) {
		t.Errorf("a multi-target role decoded as %v", got)
	}
	if got := transform.Roles(); !reflect.DeepEqual(got, []string{"database-instance", "librarian"}) {
		t.Errorf("Roles() = %v, want them sorted", got)
	}
	if transform.ConfigArtifactSpec == "" || transform.AutoDeploy == nil {
		t.Error("the optional keys were dropped")
	}

	// FAILED entries are in a BOM. The server emits DEPLOYED and FAILED both,
	// and deciding what to do about that is the caller's business.
	if byName["trino"].Status != "FAILED" {
		t.Error("a FAILED entry was not decoded as such")
	}
}

// TestEnvironmentsListsTheSlugs covers the listing SPEC004 asks for, including
// the row a graph can hold that names nothing.
func TestEnvironmentsListsTheSlugs(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"type":"prod","account_number":"2","hmd_region":"reg1"},
			{"type":"dev","account_number":"1","hmd_region":"reg1"},
			{"account_number":"3"}
		]`))
	}))
	defer srv.Close()

	envs, err := New(srv.URL).Environments(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := EnvironmentTypes(envs); !reflect.DeepEqual(got, []string{"dev", "prod"}) {
		t.Errorf("Environments() = %v, want them sorted with the typeless row skipped", got)
	}
	if envs[0].AccountNumber != "1" || envs[0].Region != "reg1" {
		t.Errorf("the account and region were not read: %+v", envs[0])
	}
}

// TestCloudResolvesThroughTheSharedRule proves this client goes through
// nsconfig rather than keeping its own ladder.
func TestCloudResolvesThroughTheSharedRule(t *testing.T) {
	t.Parallel()

	c, err := NewCloud(CloudConfig{
		Home:    t.TempDir(),
		Profile: nsconfig.Profile{Name: "acme", CustomerCode: "acme", Region: "reg1"},
		Lookup:  cloudLookup(map[string]string{AuthTokenEnv: "t"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://ms-deployment-aaa-reg1.acme-admin-neuronsphere.io"; c.BaseURL != want {
		t.Errorf("BaseURL = %q, want %q", c.BaseURL, want)
	}
}
