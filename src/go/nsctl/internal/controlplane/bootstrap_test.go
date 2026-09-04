package controlplane

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/floci"
)

// The script has to match what ms-deployment's deploy_base.deploy_node
// generates, because the same hmd-cli-deploy on the other side parses it.
func TestDeployScriptMatchesTheGeneratedForm(t *testing.T) {
	t.Parallel()

	script, err := DeployScript("hmd-postgres-rds", "control-plane-db", "0.8", "cp", "reg1",
		map[string]any{"db_host": "hmd_db", "db_port": 5432})
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"--repo-name hmd-postgres-rds",
		"--repo-version 0.8",
		"--hmd-region reg1",
		"--instance-name control-plane-db",
		"--deployment-id cp",
		"--config-file STDIN",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script is missing %q:\n%s", want, script)
		}
	}

	// Quoted delimiter, so the shell expands nothing in the JSON body.
	if !strings.Contains(script, "<<'EOF'") {
		t.Errorf("the heredoc delimiter is not quoted:\n%s", script)
	}
	if !strings.HasSuffix(script, "\nEOF") {
		t.Errorf("the heredoc is not terminated:\n%s", script)
	}

	// Both are omitted because ms-deployment does not exist while this runs.
	if strings.Contains(script, "--register") {
		t.Error("the script registers with a service that does not exist yet")
	}
	if strings.Contains(script, "HMD_REPO_INSTANCE_DEPLOYMENT_ID") {
		t.Error("the script exports a deployment id nothing can resolve yet")
	}
}

func TestDeployScriptCarriesTheConfigurationAsJSON(t *testing.T) {
	t.Parallel()

	config := map[string]any{"db_port": 5432, "db_username": "postgres"}
	script, err := DeployScript("hmd-postgres-rds", "control-plane-db", "0.8", "cp", "", config)
	if err != nil {
		t.Fatal(err)
	}

	_, body, found := strings.Cut(script, "<<'EOF'\n")
	if !found {
		t.Fatalf("no heredoc body:\n%s", script)
	}
	body = strings.TrimSuffix(body, "\nEOF")

	var got map[string]any
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("the body is not valid JSON: %v\n%s", err, body)
	}
	if got["db_username"] != "postgres" || got["db_port"] != float64(5432) {
		t.Errorf("configuration = %v", got)
	}
}

// An unset region must not produce `--hmd-region ` with nothing after it,
// which shifts every later flag by one.
func TestDeployScriptDefaultsTheRegion(t *testing.T) {
	t.Parallel()

	script, err := DeployScript("hmd-inf-neptune", "control-plane-graph", "0.3", "cp", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, "--hmd-region reg1") {
		t.Errorf("region was not defaulted:\n%s", script)
	}
}

func TestDeployScriptWithNoConfigurationSendsAnObject(t *testing.T) {
	t.Parallel()

	script, err := DeployScript("hmd-inf-neptune", "control-plane-graph", "0.3", "cp", "reg1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, "{}") {
		t.Errorf("an absent configuration was not sent as an empty object:\n%s", script)
	}
}

// The cloud default_configuration describes Aurora, whose values a plain
// aws_db_instance cannot use, so these have to be passed explicitly.
func TestPostgresConfigPinsWhatTheCloudDefaultWouldBreak(t *testing.T) {
	t.Parallel()

	config := postgresConfig(t.Context(), stubImages{})
	for key, want := range map[string]any{
		"db_subnet_group_name": "hmd-local-db-subnets",
		"db_host":              "hmd_db",
		"db_port":              5432,
		"db_username":          "postgres",
		"instance_type":        "db.t3.micro",
	} {
		if config[key] != want {
			t.Errorf("%s = %v, want %v", key, config[key], want)
		}
	}
	// Not declared when it cannot be read from the image, rather than guessed.
	if _, present := config["engine_version"]; present {
		t.Errorf("engine_version was declared without an image to read it from: %v", config["engine_version"])
	}
}

func TestGraphConfigNamesTheAlias(t *testing.T) {
	t.Parallel()

	config := graphConfig()
	if config["graph_host"] != CPGraphHost || config["graph_port"] != 8182 {
		t.Errorf("graph configuration = %v", config)
	}
}

// ms-deployment is last: everything before it is what it needs to run, and it
// is what every later deploy reports to.
func TestMSDeploymentIsTheLastFoundationService(t *testing.T) {
	t.Parallel()

	if got := bootstrapServices[len(bootstrapServices)-1]; got != "hmd-ms-deployment" {
		t.Errorf("last foundation service = %q, want hmd-ms-deployment", got)
	}
	naming, artifactLib := indexOf(bootstrapServices, "hmd-ms-naming"), indexOf(bootstrapServices, "hmd-ms-artifact-lib")
	if naming < 0 || artifactLib < 0 {
		t.Fatalf("services = %v", bootstrapServices)
	}
	// artifact-lib's neptune-db dependency is required and resolves to the
	// graph alias, so it cannot precede the graph -- which the DAG deploys
	// before any of these.
	if naming > artifactLib {
		t.Errorf("naming deploys after artifact-lib: %v", bootstrapServices)
	}
}

func indexOf(items []string, want string) int {
	for i, item := range items {
		if item == want {
			return i
		}
	}
	return -1
}

// stubImages reports no images and no container environment, standing in for a
// Floci whose configuration cannot be read.
type stubImages struct{}

func (stubImages) ImagePresent(context.Context, string) bool { return false }
func (stubImages) ContainerEnv(context.Context, string) map[string]string {
	return map[string]string{}
}

// The control plane's graph is its own instance under deployment id `cp`, not
// the environment's `global-graph`. Asking floci.GraphIdentifier for it
// silently finds nothing, which is how the control-plane graph came to be left
// stopped after a restart while the code read as correct.
func TestCPGraphIdentifierIsNotTheEnvironmentGraph(t *testing.T) {
	t.Parallel()

	names := floci.Names{Region: "reg1", CustomerCode: "none", DeploymentID: "aaa"}
	cp := CPGraphIdentifier(names)

	if !strings.Contains(cp, CPGraphInstance) {
		t.Errorf("CPGraphIdentifier = %q, want it to name %s", cp, CPGraphInstance)
	}
	if !strings.Contains(cp, CPDeploymentID) {
		t.Errorf("CPGraphIdentifier = %q, want deployment id %s", cp, CPDeploymentID)
	}
	if cp == floci.GraphIdentifier(names) {
		t.Error("the control plane's graph identifier matches the environment's")
	}
}
