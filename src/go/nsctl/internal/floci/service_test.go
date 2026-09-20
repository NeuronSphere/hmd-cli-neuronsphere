package floci

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/repoclass"
)

func repoWith(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	meta := filepath.Join(dir, "meta-data")
	if err := os.MkdirAll(meta, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(meta, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// The RepoClass declares its own service configuration, which is why nsctl
// needs no table of its own for the foundation services.
func TestServiceConfigReadsTheManifestDeclaration(t *testing.T) {
	t.Parallel()

	dir := repoWith(t, map[string]string{"manifest.json": `{
	  "deploy": {"default_configuration": {"service_config": {
	    "service_loader": "deployment",
	    "operations_modules": ["hmd_ms_base.crud_operations"]
	  }}}
	}`})

	config := ServiceConfig(dir)
	if config["service_loader"] != "deployment" {
		t.Errorf("service_loader = %v", config["service_loader"])
	}
	if _, ok := config["operations_modules"]; !ok {
		t.Errorf("operations_modules missing: %v", config)
	}
}

// Reading config_local.json's top level rather than its service_config key
// yields a configuration with no operations_modules, and the service then
// starts and fails every request with a KeyError.
func TestServiceConfigTakesOnlyTheServiceConfigKey(t *testing.T) {
	t.Parallel()

	dir := repoWith(t, map[string]string{"config_local.json": `{
	  "loader_config": {"ignored": ["at-the-top-level"]},
	  "service_config": {"operations_modules": ["real.module"]}
	}`})

	config := ServiceConfig(dir)
	if _, leaked := config["loader_config"]; leaked {
		t.Errorf("a top-level key was read as service configuration: %v", config)
	}
	if config["operations_modules"] == nil {
		t.Errorf("the service_config key was not read: %v", config)
	}
}

func TestServiceConfigOverlaysLocalOnManifest(t *testing.T) {
	t.Parallel()

	dir := repoWith(t, map[string]string{
		"manifest.json":     `{"deploy": {"default_configuration": {"service_config": {"a": "from-manifest", "b": "kept"}}}}`,
		"config_local.json": `{"service_config": {"a": "from-local"}}`,
	})

	config := ServiceConfig(dir)
	if config["a"] != "from-local" {
		t.Errorf("a = %v, want the local override to win", config["a"])
	}
	if config["b"] != "kept" {
		t.Errorf("b = %v, want the manifest value kept", config["b"])
	}
}

func TestServiceConfigOnARepoThatDeclaresNone(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"no files":       "",
		"empty manifest": `{}`,
		"malformed":      `{not json`,
	}
	for name, manifest := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			files := map[string]string{}
			if manifest != "" {
				files["manifest.json"] = manifest
			}
			if got := ServiceConfig(repoWith(t, files)); got != nil {
				t.Errorf("ServiceConfig = %v, want nil", got)
			}
		})
	}
	if got := ServiceConfig(""); got != nil {
		t.Errorf(`ServiceConfig("") = %v, want nil`, got)
	}
}

// The three services nsctl bootstraps must each declare enough to start.
// Skipped when the repos are not checked out, since that is normal here.
func TestFoundationServicesDeclareTheirConfiguration(t *testing.T) {
	t.Parallel()

	repoHome := os.Getenv("HMD_REPO_HOME")
	if repoHome == "" {
		t.Skip("HMD_REPO_HOME is not set")
	}
	for _, repoClass := range []string{"hmd-ms-naming", "hmd-ms-artifact-lib", "hmd-ms-deployment"} {
		dir := filepath.Join(repoHome, repoClass)
		if _, err := os.Stat(dir); err != nil {
			t.Skipf("%s is not checked out", repoClass)
		}
		config := ServiceConfig(dir)
		if config == nil {
			t.Errorf("%s declares no service_config; its Lambda would start with {} and fail every request", repoClass)
			continue
		}
		if config["operations_modules"] == nil {
			t.Errorf("%s declares no operations_modules: %v", repoClass, config)
		}
	}
}

// Left unresolved, the service starts and then fails every request with
// "Secret dependency:db-credentials not found in PS or SM".
func TestLocalizeServiceConfigResolvesThePostgresEngine(t *testing.T) {
	t.Parallel()

	config := LocalizeServiceConfig(map[string]any{
		"service_loader": "deployment",
		"hmd_db_engines": map[string]any{"postgres": map[string]any{
			"engine_type": "postgres",
			"engine_config": map[string]any{
				"db_secret_name": "dependency:db-credentials",
				"db_name":        "deployment",
				"proxy_host":     "dependency:pgbouncer",
				"proxy_port":     6432,
			},
		}},
	}, "hmd_db", "hmd_ms_deployment", "global-graph", "hmd-ms-x", Names{DeploymentID: "aaa", Region: "reg1", CustomerCode: "none"})

	engine := config["hmd_db_engines"].(map[string]any)["postgres"].(map[string]any)
	got := engine["engine_config"].(map[string]any)

	want := map[string]any{
		"host": "hmd_db", "user": "hmd_ms_deployment",
		"password": "hmd_ms_deployment", "db_name": "hmd_ms_deployment",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	// The cloud wiring is replaced, not merged: a leftover secret name or
	// proxy is what the service fails on.
	for _, gone := range []string{"db_secret_name", "proxy_host", "proxy_port"} {
		if _, present := got[gone]; present {
			t.Errorf("%s survived localization: %v", gone, got)
		}
	}
	if config["service_loader"] != "deployment" {
		t.Error("localization dropped an unrelated key")
	}
}

func TestLocalizeServiceConfigResolvesTheGraphEngine(t *testing.T) {
	t.Parallel()

	config := LocalizeServiceConfig(map[string]any{
		"hmd_db_engines": map[string]any{
			"graph": map[string]any{
				"engine_type":   "gremlin",
				"engine_config": map[string]any{"db_host": "dependency:neptune-db"},
			},
			// Floci serves DynamoDB directly, so this needs no resolution.
			"dynamo": map[string]any{
				"engine_type":   "dynamo",
				"engine_config": map[string]any{"point_in_time_recovery": "enabled"},
			},
		},
	}, "hmd_db", "hmd_ms_artifact_lib", "global-graph", "hmd-ms-x", Names{DeploymentID: "aaa", Region: "reg1", CustomerCode: "none"})

	engines := config["hmd_db_engines"].(map[string]any)
	graph := engines["graph"].(map[string]any)["engine_config"].(map[string]any)
	if graph["db_host"] != "global-graph" {
		t.Errorf("graph db_host = %v, want the alias", graph["db_host"])
	}
	dynamo := engines["dynamo"].(map[string]any)["engine_config"].(map[string]any)
	if dynamo["point_in_time_recovery"] != "enabled" {
		t.Errorf("the dynamo engine was altered: %v", dynamo)
	}
}

// The caller's map may be a cached read of the manifest, so localizing in
// place would leak one service's connection details into the next.
func TestLocalizeServiceConfigDoesNotMutateItsInput(t *testing.T) {
	t.Parallel()

	engineConfig := map[string]any{"db_secret_name": "dependency:db-credentials"}
	original := map[string]any{
		"hmd_db_engines": map[string]any{"postgres": map[string]any{
			"engine_type": "postgres", "engine_config": engineConfig,
		}},
	}

	LocalizeServiceConfig(original, "hmd_db", "one", "", "hmd-ms-x", Names{DeploymentID: "aaa", Region: "reg1", CustomerCode: "none"})

	if engineConfig["db_secret_name"] != "dependency:db-credentials" {
		t.Errorf("the input was mutated: %v", engineConfig)
	}
}

func TestLocalizeServiceConfigLeavesConfigsWithNoEnginesAlone(t *testing.T) {
	t.Parallel()

	if got := LocalizeServiceConfig(nil, "hmd_db", "one", "", "hmd-ms-x", Names{DeploymentID: "aaa", Region: "reg1", CustomerCode: "none"}); got != nil {
		t.Errorf("LocalizeServiceConfig(nil) = %v", got)
	}
	config := map[string]any{"service_loader": "naming"}
	if got := LocalizeServiceConfig(config, "hmd_db", "one", "", "hmd-ms-x", Names{DeploymentID: "aaa", Region: "reg1", CustomerCode: "none"}); got["service_loader"] != "naming" {
		t.Errorf("got %v", got)
	}
}

// An empty graph host must not blank out a declared one -- an environment
// without a graph should leave the declaration as it found it.
func TestLocalizeServiceConfigKeepsTheGraphHostWhenThereIsNone(t *testing.T) {
	t.Parallel()

	config := LocalizeServiceConfig(map[string]any{
		"hmd_db_engines": map[string]any{"graph": map[string]any{
			"engine_type":   "gremlin",
			"engine_config": map[string]any{"db_host": "dependency:neptune-db"},
		}},
	}, "hmd_db", "one", "", "hmd-ms-x", Names{DeploymentID: "aaa", Region: "reg1", CustomerCode: "none"})

	graph := config["hmd_db_engines"].(map[string]any)["graph"].(map[string]any)["engine_config"].(map[string]any)
	if graph["db_host"] != "dependency:neptune-db" {
		t.Errorf("db_host = %v, want the declaration untouched", graph["db_host"])
	}
}

// The cloud takes the table name from the table CDKTF creates. Without one the
// service fails every request with KeyError: 'dynamo_table'.
func TestLocalizeServiceConfigNamesTheDynamoTable(t *testing.T) {
	t.Parallel()

	config := LocalizeServiceConfig(map[string]any{
		"hmd_db_engines": map[string]any{"dynamo": map[string]any{
			"engine_type":   "dynamo",
			"engine_config": map[string]any{"point_in_time_recovery": "enabled"},
		}},
	}, "hmd_db", "x", "", "hmd-ms-artifact-lib",
		Names{DeploymentID: "aaa", Region: "reg1", CustomerCode: "none"})

	engine := config["hmd_db_engines"].(map[string]any)["dynamo"].(map[string]any)
	got := engine["engine_config"].(map[string]any)
	if got["dynamo_table"] == nil || got["dynamo_table"] == "" {
		t.Errorf("no dynamo_table was named: %v", got)
	}
	// The declared value survives alongside it.
	if got["point_in_time_recovery"] != "enabled" {
		t.Errorf("a declared value was dropped: %v", got)
	}
}

// The local gremlin-server speaks neither wss nor query strategies.
func TestLocalizeServiceConfigSetsTheGremlinProtocol(t *testing.T) {
	t.Parallel()

	config := LocalizeServiceConfig(map[string]any{
		"hmd_db_engines": map[string]any{"graph": map[string]any{
			"engine_type":   "gremlin",
			"engine_config": map[string]any{"db_host": "dependency:neptune-db"},
		}},
	}, "hmd_db", "x", "global-graph", "hmd-ms-artifact-lib", Names{})

	got := config["hmd_db_engines"].(map[string]any)["graph"].(map[string]any)["engine_config"].(map[string]any)
	if got["db_protocol"] != "ws" || got["with_strategies"] != false {
		t.Errorf("gremlin engine = %v", got)
	}
}

// A repo that states a value keeps it: these fill gaps, they do not override.
func TestLocalizeServiceConfigDoesNotOverrideDeclaredValues(t *testing.T) {
	t.Parallel()

	config := LocalizeServiceConfig(map[string]any{
		"hmd_db_engines": map[string]any{
			"graph": map[string]any{
				"engine_type": "gremlin",
				"engine_config": map[string]any{
					"db_host": "dependency:neptune-db", "db_protocol": "wss",
				},
			},
			"dynamo": map[string]any{
				"engine_type":   "dynamo",
				"engine_config": map[string]any{"dynamo_table": "declared-table"},
			},
		},
	}, "hmd_db", "x", "global-graph", "hmd-ms-x", Names{})

	engines := config["hmd_db_engines"].(map[string]any)
	graph := engines["graph"].(map[string]any)["engine_config"].(map[string]any)
	if graph["db_protocol"] != "wss" {
		t.Errorf("db_protocol = %v, want the declared wss", graph["db_protocol"])
	}
	dynamo := engines["dynamo"].(map[string]any)["engine_config"].(map[string]any)
	if dynamo["dynamo_table"] != "declared-table" {
		t.Errorf("dynamo_table = %v, want the declared name", dynamo["dynamo_table"])
	}
}

// LibrarianBase exports these at cloud deploy time, and the service raises
// "Environment variable, X, not populated" without them. Both are declared in
// the repo's own manifest.
func TestServiceParametersReadsTheManifest(t *testing.T) {
	t.Parallel()

	dir := repoWith(t, map[string]string{"manifest.json": `{
	  "deploy": {"default_configuration": {
	    "content_path_configs": {"local": {"path": "/x"}},
	    "graph_queries": {"q": {"query_template": "g.V()"}}
	  }}
	}`})

	params := ServiceParameters(dir)
	if params["CONTENT_PATH_CONFIGS"] == "" {
		t.Errorf("CONTENT_PATH_CONFIGS missing: %v", params)
	}
	if params["GRAPH_QUERY_CONFIG"] == "" {
		t.Errorf("GRAPH_QUERY_CONFIG missing: %v", params)
	}
	// JSON-encoded, since that is how the service parses them.
	if !strings.HasPrefix(params["CONTENT_PATH_CONFIGS"], "{") {
		t.Errorf("CONTENT_PATH_CONFIGS is not JSON: %q", params["CONTENT_PATH_CONFIGS"])
	}
}

func TestServiceParametersPrefersTheLocalOverride(t *testing.T) {
	t.Parallel()

	dir := repoWith(t, map[string]string{
		"manifest.json":     `{"deploy": {"default_configuration": {"content_path_configs": {"from": "manifest"}}}}`,
		"config_local.json": `{"content_path_configs": {"from": "local"}}`,
	})
	if got := ServiceParameters(dir)["CONTENT_PATH_CONFIGS"]; !strings.Contains(got, "local") {
		t.Errorf("CONTENT_PATH_CONFIGS = %s, want the local override", got)
	}
}

func TestServiceParametersOnARepoThatDeclaresNone(t *testing.T) {
	t.Parallel()

	if got := ServiceParameters(repoWith(t, map[string]string{"manifest.json": `{}`})); got != nil {
		t.Errorf("ServiceParameters = %v, want nil", got)
	}
	if got := ServiceParameters(""); got != nil {
		t.Errorf(`ServiceParameters("") = %v, want nil`, got)
	}
}

// A librarian refuses to serve without a bucket, so the caller needs to know
// which services that applies to.
func TestLibrarianStyle(t *testing.T) {
	t.Parallel()

	librarian := repoWith(t, map[string]string{
		"manifest.json": `{"deploy": {"default_configuration": {"content_path_configs": {"local": {}}}}}`,
	})
	if !LibrarianStyle(librarian) {
		t.Error("a repo declaring content_path_configs was not recognised")
	}
	plain := repoWith(t, map[string]string{"manifest.json": `{"deploy": {"default_configuration": {}}}`})
	if LibrarianStyle(plain) {
		t.Error("a repo declaring none was treated as a librarian")
	}
}

// librarianRepo writes a manifest shaped like hmd-ms-artifact-lib's: a
// content_path_configs block (what makes it librarian-style) and a required
// lib-repo dependency on hmd-inf-s3bucket.
func librarianRepo(t *testing.T, deps string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "meta-data"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"hmd-ms-artifact-lib","deploy":{"dependencies":` + deps +
		`,"default_configuration":{"content_path_configs":{}}}}`
	if err := os.WriteFile(filepath.Join(dir, "meta-data", "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// SPEC014 said the fix was a literal BUCKET_NAME in another repository, and
// that nothing here could close it. The name is not declared anywhere in the
// cloud either -- librarian_base.get_full_bucket_name composes it from the
// lib-repo dependency as
//
//	f"{instance_name}-{repo_name}-{deployment_id}-{environment}-{hmd_region}-{customer_code}"
//
// This asserts the Go derivation produces that exact string.
func TestLibrarianBucketNameMatchesTheCloudsOwnFormula(t *testing.T) {
	t.Parallel()

	dir := librarianRepo(t, `{"lib-repo":{"repo_class_name":"hmd-inf-s3bucket","required":"true"}}`)
	names := Names{Region: "reg1", CustomerCode: "hmdtr1"}

	got := LibrarianBucketName(dir, "cp", names)
	// get_full_bucket_name with instance_name=lib-repo, repo_name=hmd-inf-s3bucket,
	// deployment_id=cp, environment=local, hmd_region=reg1, customer_code=hmdtr1.
	want := "lib-repo-hmd-inf-s3bucket-cp-local-reg1-hmdtr1"
	if got != want {
		t.Errorf("LibrarianBucketName = %q, want %q", got, want)
	}
}

// A BOM-style dependency naming its instance wins over the key, because that is
// what the cloud reads as lib-repo.instance_name.
func TestLibrarianBucketNamePrefersADeclaredInstanceName(t *testing.T) {
	t.Parallel()

	dir := librarianRepo(t, `{"lib-repo":{"repo_class_name":"hmd-inf-s3bucket","instance_name":"artifact-librarian"}}`)
	got := LibrarianBucketName(dir, "cp", Names{Region: "reg1", CustomerCode: "hmdtr1"})
	if want := "artifact-librarian-hmd-inf-s3bucket-cp-local-reg1-hmdtr1"; got != want {
		t.Errorf("LibrarianBucketName = %q, want %q", got, want)
	}
}

// A librarian declaring no bucket dependency names no bucket, and the caller
// still warns. Guessing one here would point the service at storage nothing
// else uses -- which is the one thing SPEC014 was right about.
func TestLibrarianBucketNameGuessesNothingWithoutADependency(t *testing.T) {
	t.Parallel()

	dir := librarianRepo(t, `{"neptune-db":{"repo_class_name":"hmd-inf-neptune"}}`)
	if got := LibrarianBucketName(dir, "cp", Names{Region: "reg1", CustomerCode: "hmdtr1"}); got != "" {
		t.Errorf("LibrarianBucketName = %q, want the empty string", got)
	}
}

// A service that reads no librarian parameters needs no bucket, whatever it
// depends on.
func TestLibrarianBucketNameIgnoresANonLibrarian(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "meta-data"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"hmd-ms-naming","deploy":{"dependencies":` +
		`{"lib-repo":{"repo_class_name":"hmd-inf-s3bucket"}},"default_configuration":{}}}`
	if err := os.WriteFile(filepath.Join(dir, "meta-data", "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := LibrarianBucketName(dir, "cp", Names{Region: "reg1", CustomerCode: "hmdtr1"}); got != "" {
		t.Errorf("LibrarianBucketName = %q for a non-librarian, want the empty string", got)
	}
}

// noEnv is a lookup with nothing set -- the fresh-install case.
func noEnv(string) string { return "" }

// nothingPublished is images_test.go's fake with every pull failing, which is
// how a version that exists nowhere behaves.
func nothingPublished() *fakeImages {
	return &fakeImages{present: map[string]bool{}, failing: map[string]bool{}}
}

// A locally built image wins, and nothing is fetched. This is the iteration
// loop: `hmd build` in a service's repo and restart.
func TestServiceImagePrefersALocalBuild(t *testing.T) {
	t.Parallel()

	f := &fakeImages{present: map[string]bool{"hmd-ms-naming:0.1.58": true}}
	got, err := ServiceImage(context.Background(), f, ImageRef{Service: "hmd-ms-naming", Version: "0.1.58"}, noEnv)
	if err != nil {
		t.Fatalf("ServiceImage() error = %v", err)
	}
	if got != "hmd-ms-naming:0.1.58" {
		t.Errorf("ServiceImage() = %q, want the local build", got)
	}
	if len(f.pulled) != 0 {
		t.Errorf("pulled %v with a local image available", f.pulled)
	}
}

// Nothing cached is the `brew install` case: no repositories, so `hmd build` is
// advice the machine cannot take. Refusing here stopped a cold bootstrap three
// services in, on images that are published.
func TestServiceImagePullsThePublishedImage(t *testing.T) {
	t.Parallel()

	published := repoclass.PublishedRegistry + "/hmd-ms-naming:0.1.58"
	f := &fakeImages{present: map[string]bool{}, failing: map[string]bool{}}
	got, err := ServiceImage(context.Background(), f, ImageRef{Service: "hmd-ms-naming", Version: "0.1.58"}, noEnv)
	if err != nil {
		t.Fatalf("ServiceImage() error = %v", err)
	}
	if got != published {
		t.Errorf("ServiceImage() = %q, want %q", got, published)
	}
}

// An explicitly named registry is a statement about where images come from:
// it is pulled from, and the published registry is never tried instead of it.
func TestServiceImageDoesNotCrossAnExplicitRegistry(t *testing.T) {
	t.Parallel()

	lookup := func(k string) string {
		if k == "HMD_CONTAINER_REGISTRY" {
			return "my.registry"
		}
		return ""
	}
	ref := ImageRef{Service: "hmd-ms-naming", Version: "0.1.58"}

	f := &fakeImages{present: map[string]bool{}, failing: map[string]bool{"my.registry/hmd-ms-naming:0.1.58": true}}
	if _, err := ServiceImage(context.Background(), f, ref, lookup); err == nil {
		t.Error("ServiceImage() succeeded across an explicitly named registry")
	}
	if len(f.pulled) != 1 || f.pulled[0] != "my.registry/hmd-ms-naming:0.1.58" {
		t.Errorf("pulled %v, want only the named registry's image", f.pulled)
	}

	f = &fakeImages{present: map[string]bool{}, failing: map[string]bool{}}
	got, err := ServiceImage(context.Background(), f, ref, lookup)
	if err != nil || got != "my.registry/hmd-ms-naming:0.1.58" {
		t.Errorf("ServiceImage() = %q, %v; want the named registry's image pulled", got, err)
	}
}

// A version that exists nowhere is what the refusal was really written for, and
// it still names both remedies.
func TestServiceImageRefusesAVersionThatExistsNowhere(t *testing.T) {
	t.Parallel()

	f := nothingPublished()
	f.failing[repoclass.PublishedRegistry+"/hmd-ms-naming:9.9.9"] = true
	_, err := ServiceImage(context.Background(), f, ImageRef{Service: "hmd-ms-naming", Version: "9.9.9"}, noEnv)
	if err == nil {
		t.Fatal("ServiceImage() succeeded for a version that exists nowhere")
	}
	for _, want := range []string{"hmd-ms-naming", "9.9.9", "hmd build", "HMD_LOCAL_VERSION_HMD_MS_NAMING"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}

// The deployment service runs the core image under its own service name
// (NERD0015): the image is looked up by image class, the error and the pin
// variable name it by that class too, and nothing about the service name
// leaks into the reference.
func TestServiceImageResolvesTheImageClassNotTheServiceName(t *testing.T) {
	t.Parallel()

	f := &fakeImages{present: map[string]bool{"hmd-ms-deployment-core:0.1.7": true}}
	ref := ImageRef{Service: MSDeploymentServiceName, ImageClass: MSDeploymentImageClass, Version: "0.1.7"}
	got, err := ServiceImage(context.Background(), f, ref, noEnv)
	if err != nil {
		t.Fatalf("ServiceImage() error = %v", err)
	}
	if got != "hmd-ms-deployment-core:0.1.7" {
		t.Errorf("ServiceImage() = %q, want the core image", got)
	}

	f = nothingPublished()
	f.failing[repoclass.PublishedRegistry+"/hmd-ms-deployment-core:9.9.9"] = true
	ref.Version = "9.9.9"
	_, err = ServiceImage(context.Background(), f, ref, noEnv)
	if err == nil || !strings.Contains(err.Error(), "HMD_LOCAL_VERSION_HMD_MS_DEPLOYMENT_CORE") {
		t.Errorf("error = %v, want the core class's pin variable named", err)
	}
}

// HMD_LOCAL_IMAGE_<SERVICE> is a full reference and wins outright: no version
// resolution, no registry candidates. Present locally it is used as is; absent
// it is pulled; unpullable it is an error naming the variable.
func TestServiceImageHonoursAFullRefOverride(t *testing.T) {
	t.Parallel()

	premium := "ghcr.io/hmdlabs/hmd-ms-deployment:0.4.900"
	lookup := func(k string) string {
		if k == "HMD_LOCAL_IMAGE_HMD_MS_DEPLOYMENT" {
			return premium
		}
		return ""
	}
	ref := ImageRef{Service: MSDeploymentServiceName, ImageClass: MSDeploymentImageClass, Version: "0.1.7"}

	f := &fakeImages{present: map[string]bool{premium: true, "hmd-ms-deployment-core:0.1.7": true}}
	got, err := ServiceImage(context.Background(), f, ref, lookup)
	if err != nil || got != premium {
		t.Fatalf("ServiceImage() = %q, %v; want the override", got, err)
	}
	if len(f.pulled) != 0 {
		t.Errorf("pulled %v with the override present", f.pulled)
	}

	f = &fakeImages{present: map[string]bool{}, failing: map[string]bool{}}
	got, err = ServiceImage(context.Background(), f, ref, lookup)
	if err != nil || got != premium {
		t.Fatalf("ServiceImage() = %q, %v; want the override pulled", got, err)
	}
	if len(f.pulled) != 1 || f.pulled[0] != premium {
		t.Errorf("pulled %v, want just the override", f.pulled)
	}

	f = nothingPublished()
	f.failing[premium] = true
	_, err = ServiceImage(context.Background(), f, ref, lookup)
	if err == nil || !strings.Contains(err.Error(), "HMD_LOCAL_IMAGE_HMD_MS_DEPLOYMENT") {
		t.Errorf("error = %v, want the override variable named", err)
	}
}

// Every foundation service but ms-deployment is its own image class.
func TestImageClassForIsIdentityExceptForMSDeployment(t *testing.T) {
	t.Parallel()

	if got := ImageClassFor("hmd-ms-naming"); got != "hmd-ms-naming" {
		t.Errorf("ImageClassFor(hmd-ms-naming) = %q", got)
	}
	if got := ImageClassFor(MSDeploymentServiceName); got != MSDeploymentImageClass {
		t.Errorf("ImageClassFor(%s) = %q, want %s", MSDeploymentServiceName, got, MSDeploymentImageClass)
	}
}
