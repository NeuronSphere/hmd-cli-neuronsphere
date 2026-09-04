package floci

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
