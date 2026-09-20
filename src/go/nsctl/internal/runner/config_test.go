package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureScript is a generate_local_deployment script captured from a live
// control plane on 2026-09-16 (the NERD009 acceptance probe): a quoted heredoc
// carrying the resolved configuration, with its one dependency role baked as
// hmd_resources because that dependency was already deployed.
func fixtureScript(t *testing.T) string {
	return readFixture(t, "generate_local_deployment.sh")
}

// refsScript is the same shape, constructed from deploy_base.py's template
// for what the captured one cannot show: hmd_resource_ref pointers, on a
// role, on a nested role and on a list-valued role, beside a baked role.
func refsScript(t *testing.T) string {
	return readFixture(t, "generate_local_deployment_refs.sh")
}

func readFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// The resolved configuration of a service-generated node lives only in the
// generated script's heredoc: generate_local_deployment emits no
// instance_configuration, so a foreign node reads it from there.
func TestExtractConfigReadsTheHeredoc(t *testing.T) {
	t.Parallel()

	config, ok := ExtractConfig(fixtureScript(t))
	if !ok {
		t.Fatal("no configuration was extracted")
	}
	if config["instance_name"] != "exec-probe" || config["capture"] != float64(1) {
		t.Errorf("instance_name = %v, capture = %v", config["instance_name"], config["capture"])
	}
	deps, _ := config["dependencies"].(map[string]any)
	db, _ := deps["database"].(map[string]any)
	baked, _ := db["hmd_resources"].([]any)
	if len(baked) != 1 {
		t.Fatalf("database.hmd_resources = %v", db["hmd_resources"])
	}
	output := baked[0].(map[string]any)["output"].(map[string]any)
	if output["host"] != "hmd_db-local" || output["port"] != float64(5432) {
		t.Errorf("database output = %v", output)
	}
}

// The heredoc is quoted (<<'EOF') precisely so that the shell leaves `$`,
// backticks and backslashes alone; the extractor must too.
func TestExtractConfigLeavesShellMetacharactersAlone(t *testing.T) {
	t.Parallel()

	config, ok := ExtractConfig(refsScript(t))
	if !ok {
		t.Fatal("no configuration was extracted")
	}
	if config["greeting"] != "$HOME has `backticks` and \\backslashes\\" {
		t.Errorf("greeting = %q", config["greeting"])
	}
}

func TestExtractConfigWithoutAHeredoc(t *testing.T) {
	t.Parallel()

	for name, script := range map[string]string{
		"empty":               "",
		"image-only node":     "hmd --debug --repo-name x --repo-version 0.1 docker deploy",
		"config from a file":  "hmd deploy --config-file /mnt/deploy_base/cfg/x.json",
		"unterminated":        "hmd deploy --config-file STDIN <<'EOF'\n{\"a\": 1}\n",
		"not json":            "hmd deploy --config-file STDIN <<'EOF'\nnot json\nEOF\n",
		"deploy_local script": "bash src/local/deploy_local.sh",
	} {
		if config, ok := ExtractConfig(script); ok {
			t.Errorf("%s: extracted %v from a script without a configuration", name, config)
		}
	}
}

// fakeFetcher answers get_deployment_resources, recording every call.
type fakeFetcher struct {
	calls     []string
	resources map[string]string
	fail      map[string]bool
}

func (f *fakeFetcher) APIOpGet(_ context.Context, op string) ([]byte, error) {
	f.calls = append(f.calls, op)
	rid := strings.TrimPrefix(op, "get_deployment_resources/")
	if f.fail[rid] {
		return nil, errors.New("HTTP 500")
	}
	body, ok := f.resources[rid]
	if !ok {
		return nil, fmt.Errorf("%s: HTTP 404", op)
	}
	return []byte(body), nil
}

// A dependency deployed in the same ChangeSet carries only a pointer to its
// RepoInstanceDeployment; hmd deploy resolves that pointer into hmd_resources
// just before dispatching, and replacing hmd deploy means replacing that step.
// Every role is walked -- nested and list-valued alike -- each deployment is
// fetched once, and a role that was baked is left alone.
func TestResolveResourceRefsFillsEveryPointer(t *testing.T) {
	t.Parallel()

	config, _ := ExtractConfig(refsScript(t))
	fetch := &fakeFetcher{resources: map[string]string{
		"hmd_ms_deployment-local-reg1-rid-1809161200002": `[{"resource_name":"cache","output":{"host":"redis-local"},"tags":[]}]`,
		"hmd_ms_deployment-local-reg1-rid-1809161200003": `[{"resource_name":"eks-cluster","output":{"name":"ns-local"},"tags":[]}]`,
	}}
	var warnings []string
	ResolveResourceRefs(context.Background(), config, fetch, func(f string, a ...any) {
		warnings = append(warnings, fmt.Sprintf(f, a...))
	})

	if len(warnings) != 0 {
		t.Errorf("warnings: %v", warnings)
	}
	if len(fetch.calls) != 2 {
		t.Errorf("fetched %d times, want once per deployment: %v", len(fetch.calls), fetch.calls)
	}

	deps := config["dependencies"].(map[string]any)
	host := func(role map[string]any) any {
		resources, _ := role["hmd_resources"].([]any)
		if len(resources) != 1 {
			return fmt.Sprintf("hmd_resources = %v", role["hmd_resources"])
		}
		return resources[0].(map[string]any)["output"].(map[string]any)["host"]
	}
	cache := deps["cache"].(map[string]any)
	if got := host(cache); got != "redis-local" {
		t.Errorf("cache: %v", got)
	}
	// The pointer stays beside the resolution, as hmd deploy leaves it.
	if cache["hmd_resource_ref"] == nil {
		t.Error("the pointer was removed")
	}
	cluster := cache["dependencies"].(map[string]any)["cluster"].(map[string]any)
	if resources, _ := cluster["hmd_resources"].([]any); len(resources) != 1 {
		t.Errorf("the nested role was not resolved: %v", cluster)
	}
	workers := deps["workers"].([]any)
	if got := host(workers[0].(map[string]any)); got != "redis-local" {
		t.Errorf("workers[0]: %v", got)
	}
	if resources, _ := workers[1].(map[string]any)["hmd_resources"].([]any); len(resources) != 0 {
		t.Errorf("a baked role was touched: %v", workers[1])
	}
	db := deps["database"].(map[string]any)
	if resources, _ := db["hmd_resources"].([]any); len(resources) != 1 {
		t.Errorf("the baked database role was touched: %v", db)
	}
}

// Best effort, as hmd deploy's is: a deployment whose resources cannot be
// fetched is a warning and an unresolved role, never a failed deploy.
func TestResolveResourceRefsWarnsAndCarriesOn(t *testing.T) {
	t.Parallel()

	config, _ := ExtractConfig(refsScript(t))
	fetch := &fakeFetcher{
		resources: map[string]string{
			"hmd_ms_deployment-local-reg1-rid-1809161200003": `[{"resource_name":"eks-cluster","output":{},"tags":[]}]`,
		},
		fail: map[string]bool{"hmd_ms_deployment-local-reg1-rid-1809161200002": true},
	}
	var warnings []string
	ResolveResourceRefs(context.Background(), config, fetch, func(f string, a ...any) {
		warnings = append(warnings, fmt.Sprintf(f, a...))
	})

	if len(warnings) != 1 || !strings.Contains(warnings[0], "1809161200002") {
		t.Errorf("warnings = %v, want one naming the deployment", warnings)
	}
	deps := config["dependencies"].(map[string]any)
	cluster := deps["cache"].(map[string]any)["dependencies"].(map[string]any)["cluster"].(map[string]any)
	if resources, _ := cluster["hmd_resources"].([]any); len(resources) != 1 {
		t.Errorf("the fetch that worked was not applied: %v", cluster)
	}
	if _, present := deps["cache"].(map[string]any)["hmd_resources"]; present {
		t.Error("a failed fetch left an hmd_resources key behind")
	}
}

func TestResolveResourceRefsWithNothingToDo(t *testing.T) {
	t.Parallel()

	fetch := &fakeFetcher{}
	ResolveResourceRefs(context.Background(), map[string]any{"dependencies": map[string]any{
		"db": map[string]any{"hmd_resources": []any{}},
	}}, fetch, func(string, ...any) { t.Error("warned with nothing to fetch") })
	ResolveResourceRefs(context.Background(), nil, fetch, nil)
	if len(fetch.calls) != 0 {
		t.Errorf("fetched %v", fetch.calls)
	}
}
