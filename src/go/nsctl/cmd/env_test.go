package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/status"
)

// registryHome writes a registry into a temporary HMD_HOME.
func registryHome(t *testing.T, body string) string {
	t.Helper()
	home := t.TempDir()
	path := registry.Path(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return home
}

const twoEnvRegistry = `{
  "control_plane": {"bootstrapped": true, "compose_project": "cp", "floci_data_dir": "/d", "network": "net"},
  "default_env": "local",
  "environments": {
    "local": {"account_id": "000000000001", "bootstrap": {"csd_nid": "nid"}, "compose_project": "p1",
              "core_instance_name": "local-neuronsphere", "db_container": "hmd_db-local",
              "deployment_id": "local", "graph_container": "global-graph-local",
              "k3s_cluster": "ns-local-abc", "kubeconfig": "/k", "legacy_layout": false,
              "name": "local", "port_base": 19000, "port_slot": 8, "slug": "local", "state_dir": "/s"},
    "dev": {"account_id": "000000000002", "bootstrap": {}, "compose_project": "p2",
            "core_instance_name": "local-neuronsphere", "db_container": "hmd_db-dev",
            "deployment_id": "dev", "graph_container": "global-graph-dev",
            "k3s_cluster": "ns-dev-abc", "kubeconfig": "/k2", "legacy_layout": false,
            "name": "dev", "port_base": 19000, "port_slot": 1, "slug": "dev", "state_dir": "/s2"}
  },
  "version": 1
}`

func TestEnvListRendersEveryEnvironment(t *testing.T) {
	t.Parallel()

	home := registryHome(t, twoEnvRegistry)
	out, _, err := run(t, fakeEnv(map[string]string{"HMD_HOME": home}), "env", "list")
	if err != nil {
		t.Fatalf("env list: %v", err)
	}

	for _, want := range []string{
		"local", "dev", "000000000001", "000000000002", "ns-local-abc", "ns-dev-abc",
		"(default)", // the default environment is marked
	} {
		if !strings.Contains(out, want) {
			t.Errorf("env list output does not contain %q:\n%s", want, out)
		}
	}
	// `dev` has an empty bootstrap record, so it is not bootstrapped.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "dev") && !strings.Contains(line, "no") {
			t.Errorf("dev should report bootstrapped=no:\n%s", line)
		}
	}
}

// An empty result is worth a sentence, not a blank line -- and the sentence
// should name the command that fixes it.
func TestEnvListOnAnEmptyRegistrySaysSoAndNamesTheFix(t *testing.T) {
	t.Parallel()

	home := registryHome(t, `{"control_plane": {}, "default_env": "local", "environments": {}, "version": 1}`)
	out, _, err := run(t, fakeEnv(map[string]string{"HMD_HOME": home}), "env", "list")
	if err != nil {
		t.Fatalf("env list: %v", err)
	}
	if !strings.Contains(out, "No environments registered") {
		t.Errorf("output does not say the registry is empty:\n%s", out)
	}
	if !strings.Contains(out, "nsctl env add") {
		t.Errorf("output does not name the command that creates one:\n%s", out)
	}
}

func TestEnvCommandsRefuseWithoutAnHmdHome(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{{"env", "list"}, {"env", "status"}, {"control-plane", "status"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			_, _, err := run(t, fakeEnv(nil), args...)
			if err == nil {
				t.Fatal("succeeded with no HMD_HOME, want a refusal")
			}
			if got := nserr.CodeOf(err); got != nserr.Usage {
				t.Errorf("exit code = %d, want %d (usage)", got, nserr.Usage)
			}
		})
	}
}

func TestEnvStatusOnAnUnknownEnvironmentIsAUsageError(t *testing.T) {
	t.Parallel()

	home := registryHome(t, twoEnvRegistry)
	_, _, err := run(t, fakeEnv(map[string]string{"HMD_HOME": home}), "env", "status", "nope")
	if err == nil {
		t.Fatal("succeeded on an unknown environment, want an error")
	}
	if got := nserr.CodeOf(err); got != nserr.Usage {
		t.Errorf("exit code = %d, want %d (usage)", got, nserr.Usage)
	}
	// It has to name what does exist, or the operator goes looking for the list.
	for _, want := range []string{"nope", "local", "dev"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestRenderEnvStatusShowsContainersAndRoutes(t *testing.T) {
	t.Parallel()

	snap := status.Environment{
		Name: "local", AccountID: "000000000001", DeploymentID: "local",
		Bootstrapped: true, K3sCluster: "ns-local-abc", ComposeProject: "p", StateDir: "/s",
		Containers: []status.Container{
			{Role: "floci", Name: "floci", Running: true},
			{Role: "db", Name: "floci-rds-db-ABC", Running: false},
			{Role: "graph", Name: "(not provisioned)", Absent: true},
		},
		Routes:     map[string]string{"trino": "localhost:19033"},
		RouteOrder: []string{"trino"},
	}

	var buf bytes.Buffer
	renderEnvStatus(&buf, snap)
	out := buf.String()

	for _, want := range []string{
		"local", "000000000001", "ns-local-abc",
		"running", "stopped", "absent",
		"floci-rds-db-ABC", "localhost:19033",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status output does not contain %q:\n%s", want, out)
		}
	}
}

// A stopped resource and one that was never created need different words, or a
// default environment's lazily-provisioned graph reads as a failure.
func TestStateDistinguishesAbsentFromStopped(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		c    status.Container
		want string
	}{
		{"running", status.Container{Running: true}, "running"},
		{"stopped", status.Container{}, "stopped"},
		{"absent", status.Container{Absent: true}, "absent"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := state(tt.c); got != tt.want {
				t.Errorf("state() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRenderControlPlaneStatusNamesRunningAndStoppedEnvironments(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	renderControlPlaneStatus(&buf, "/home", status.ControlPlane{
		Bootstrapped: true, ComposeProject: "cp", Network: "net", NetworkExists: true,
		FlociDataDir: "/d", MSDeploymentUp: false,
		Containers:  []status.Container{{Role: "proxy", Name: "hmd_proxy", Running: true}},
		RunningEnvs: []string{"up"}, StoppedEnvs: []string{"down"},
	})
	out := buf.String()

	for _, want := range []string{"/home", "present", "not answering", "hmd_proxy", "running  up", "stopped  down"} {
		if !strings.Contains(out, want) {
			t.Errorf("control-plane output does not contain %q:\n%s", want, out)
		}
	}
}

func TestRenderControlPlaneStatusWithNoEnvironments(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	renderControlPlaneStatus(&buf, "/home", status.ControlPlane{})
	if !strings.Contains(buf.String(), "(none registered)") {
		t.Errorf("output does not say there are no environments:\n%s", buf.String())
	}
}
