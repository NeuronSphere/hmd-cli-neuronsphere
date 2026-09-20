package runner

import (
	"encoding/json"
	"strings"
	"testing"
)

// The script has to match what ms-deployment's deploy_base.deploy_node used to
// generate, because the same hmd-cli-deploy on the other side parses it.
func TestDeployScriptMatchesTheGeneratedForm(t *testing.T) {
	t.Parallel()

	script, err := DeployScript(ScriptParams{
		RepoClass: "hmd-postgres-rds", Instance: "control-plane-db", Version: "0.8",
		DeploymentID: "cp", Region: "reg1", Environment: "local",
		Config: map[string]any{"db_host": "hmd_db", "db_port": 5432},
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"--repo-name hmd-postgres-rds",
		"--repo-version 0.8",
		"--hmd-region reg1",
		"--instance-name control-plane-db",
		"--environment local",
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

	// Omitted because the bootstrap runs before ms-deployment exists.
	if strings.Contains(script, "--register") {
		t.Error("the script registers with a service that does not exist yet")
	}
	if strings.Contains(script, "HMD_REPO_INSTANCE_DEPLOYMENT_ID") {
		t.Error("the script exports a deployment id nothing can resolve yet")
	}
}

// With a RID the export is load-bearing: hmd-cli-deploy reads it to submit
// produced Resources and, without it under HMD_ENVIRONMENT=local, registers a
// spurious `<repo>-local` instance of its own.
func TestDeployScriptCarriesTheRIDAndTheEnvironment(t *testing.T) {
	t.Parallel()

	script, err := DeployScript(ScriptParams{
		RepoClass: "hmd-inf-s3bucket", Instance: "landing", Version: "0.2",
		DeploymentID: "aaa", Environment: "dev2", RID: "rid-123",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(script, "export HMD_REPO_INSTANCE_DEPLOYMENT_ID=rid-123\n") {
		t.Errorf("the RID is not exported first:\n%s", script)
	}
	if !strings.Contains(script, "--environment dev2") {
		t.Errorf("the environment is not the instance's own:\n%s", script)
	}

	// The command line is still what Localize and ExtractConfig look for.
	localized := Localize(script)
	if !strings.Contains(localized, "deploy --local") {
		t.Errorf("Localize did not find the deploy line:\n%s", localized)
	}
	config, ok := ExtractConfig(localized)
	if !ok {
		t.Fatalf("ExtractConfig did not find the heredoc:\n%s", localized)
	}
	if len(config) != 0 {
		t.Errorf("an absent configuration was not sent as an empty object: %v", config)
	}
}

func TestDeployScriptCarriesTheConfigurationAsJSON(t *testing.T) {
	t.Parallel()

	config := map[string]any{"db_port": 5432, "db_username": "postgres"}
	script, err := DeployScript(ScriptParams{
		RepoClass: "hmd-postgres-rds", Instance: "control-plane-db", Version: "0.8",
		DeploymentID: "cp", Config: config,
	})
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
// which shifts every later flag by one; likewise the environment.
func TestDeployScriptDefaultsTheRegionAndEnvironment(t *testing.T) {
	t.Parallel()

	script, err := DeployScript(ScriptParams{RepoClass: "hmd-inf-neptune", Instance: "control-plane-graph", Version: "0.3", DeploymentID: "cp"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, "--hmd-region reg1") {
		t.Errorf("region was not defaulted:\n%s", script)
	}
	if !strings.Contains(script, "--environment local") {
		t.Errorf("environment was not defaulted:\n%s", script)
	}
	if !strings.Contains(script, "{}") {
		t.Errorf("an absent configuration was not sent as an empty object:\n%s", script)
	}
}
