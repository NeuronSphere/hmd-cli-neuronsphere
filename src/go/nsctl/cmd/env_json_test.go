package cmd

import (
	"encoding/json"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/status"
)

// A script reads `env list --json` instead of the table, so every column the
// table shows is a field, and the default environment is a flag, not a suffix.
func TestEnvListJSONHasEveryEnvironmentAndMarksTheDefault(t *testing.T) {
	t.Parallel()

	home := registryHome(t, twoEnvRegistry)
	out, _, err := run(t, fakeEnv(map[string]string{"HMD_HOME": home}), "env", "list", "--json")
	if err != nil {
		t.Fatalf("env list --json: %v", err)
	}

	var got []envListItem
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not a JSON array: %v\n%s", err, out)
	}
	byName := map[string]envListItem{}
	for _, e := range got {
		byName[e.Name] = e
	}
	if len(byName) != 2 {
		t.Fatalf("got %d environments, want 2: %s", len(byName), out)
	}
	local, dev := byName["local"], byName["dev"]
	if !local.Default || dev.Default {
		t.Errorf("default flags = local:%v dev:%v, want local only", local.Default, dev.Default)
	}
	if local.AccountID != "000000000001" || local.K3sCluster != "ns-local-abc" || local.PortSlot != 8 {
		t.Errorf("local = %+v", local)
	}
	if dev.Bootstrapped {
		t.Errorf("dev has an empty bootstrap record, want bootstrapped=false")
	}
	if local.Layout != "current" {
		t.Errorf("layout = %q, want current", local.Layout)
	}
}

// An empty registry is an empty array: the sentence the table prints would
// break a parser.
func TestEnvListJSONOnAnEmptyRegistryIsAnEmptyArray(t *testing.T) {
	t.Parallel()

	home := registryHome(t, `{"control_plane": {}, "default_env": "local", "environments": {}, "version": 1}`)
	out, _, err := run(t, fakeEnv(map[string]string{"HMD_HOME": home}), "env", "list", "--json")
	if err != nil {
		t.Fatalf("env list --json: %v", err)
	}
	var got []envListItem
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not a JSON array: %v\n%s", err, out)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("got %v, want []", got)
	}
}

func TestEnvStatusDocumentCarriesContainersRoutesAndRunning(t *testing.T) {
	t.Parallel()

	snap := status.Environment{
		Name: "local", AccountID: "000000000001", DeploymentID: "local",
		Bootstrapped: true, K3sCluster: "ns-local-abc", ComposeProject: "p", StateDir: "/s",
		Substrate: "core",
		Containers: []status.Container{
			{Role: "floci", Name: "floci", Running: true},
			{Role: "db", Name: "floci-rds-db-ABC", Running: true},
			{Role: "graph", Name: "(not provisioned)", Absent: true},
		},
		Routes:     map[string]string{"trino": "localhost:19033", "gui": "localhost:19000"},
		RouteOrder: []string{"trino", "gui"},
	}

	raw, err := json.Marshal(envStatusDocument(snap))
	if err != nil {
		t.Fatal(err)
	}
	var doc envStatusDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}

	if doc.Name != "local" || doc.Substrate != "core" || !doc.Bootstrapped {
		t.Errorf("doc = %+v", doc)
	}
	// The graph was never provisioned, so not every owned container is up.
	if doc.Running {
		t.Errorf("running = true with an absent graph, want false")
	}
	states := map[string]string{}
	for _, c := range doc.Containers {
		states[c.Role] = c.State
	}
	if states["db"] != "running" || states["graph"] != "absent" {
		t.Errorf("container states = %v", states)
	}
	// Routes keep the table's order, which a map would lose.
	if len(doc.Routes) != 2 || doc.Routes[0].Name != "trino" || doc.Routes[1].Address != "localhost:19000" {
		t.Errorf("routes = %+v", doc.Routes)
	}
}
