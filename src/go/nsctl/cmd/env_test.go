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

func TestRenderControlPlaneStatusShowsTheHandbackBlock(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	renderControlPlaneStatus(&buf, "/home", status.ControlPlane{
		HandbackPath: "/home/.config/hmd.env",
		Handback: []status.HandbackVar{
			{Name: "PYTHON_REGISTRIES.neuronsphere", Merge: "json-map", State: "managed", Owner: "registry"},
			{Name: "GOPROXY", Merge: "scalar", State: "yours", Owner: "registry"},
		},
	})
	out := buf.String()

	for _, want := range []string{"/home/.config/hmd.env", "PYTHON_REGISTRIES.neuronsphere", "managed", "GOPROXY", "yours"} {
		if !strings.Contains(out, want) {
			t.Errorf("handback section does not contain %q:\n%s", want, out)
		}
	}
}

func TestRenderControlPlaneStatusOmitsAnEmptyHandbackSection(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	renderControlPlaneStatus(&buf, "/home", status.ControlPlane{})
	if strings.Contains(buf.String(), "Handback") {
		t.Errorf("an empty handback section was rendered:\n%s", buf.String())
	}
}

// startTarget exercises the resolution `env start` performs before it starts
// anything, which is the whole point of the function: it runs ahead of the
// control plane so a first run registers and a typo refuses, both without
// Docker.
//
// Called directly rather than through `env start`. Driving the command would
// mean that a regression in the ordering this asserts does not fail the test --
// it starts a real control plane, on whatever machine is running the suite.
func startTarget(t *testing.T, home, name string) (string, string, error) {
	t.Helper()
	root := NewRootCommand("test", func(string) string { return "" })
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)

	opts := &Options{Version: "test"}
	opts.resolve(home, func(string) string { return "" }, func(string) {})
	_, err := resolveStartTarget(root, opts, home, name)
	return out.String(), errOut.String(), err
}

// An unknown name used to be refused by environment.Start -- after the control
// plane had been bootstrapped, which on a cold machine is ten minutes. It is a
// typo; it should cost a line.
func TestEnvStartRefusesAnUnknownNameBeforeStartingAnything(t *testing.T) {
	t.Parallel()

	home := registryHome(t, twoEnvRegistry)
	_, _, err := startTarget(t, home, "nosuchenv")
	if err == nil {
		t.Fatal("env start <unknown> succeeded, want a refusal")
	}
	if got := nserr.CodeOf(err); got != nserr.Usage {
		t.Errorf("code = %d, want %d (usage)", got, nserr.Usage)
	}
	// Naming what does exist is what makes a typo one line from fixed.
	for _, want := range []string{"nosuchenv", "local", "dev"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}

// A name that is registered resolves and creates nothing.
func TestEnvStartAcceptsARegisteredName(t *testing.T) {
	t.Parallel()

	home := registryHome(t, twoEnvRegistry)
	out, _, err := startTarget(t, home, "dev")
	if err != nil {
		t.Fatalf("env start dev = %v, want nil", err)
	}
	if strings.Contains(out, "Registered environment") {
		t.Errorf("an existing environment was re-registered:\n%s", out)
	}
}

// The empty registry is the one case that is not a typo, because there is
// nothing to have mistyped.
func TestEnvStartRegistersTheFirstEnvironment(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	out, _, err := startTarget(t, home, "")
	if err != nil {
		t.Fatalf("env start on an empty home = %v, want nil", err)
	}
	if !strings.Contains(out, "Registered environment") {
		t.Errorf("output does not report the registration:\n%s", out)
	}

	reg := loadReg(t, home)
	if _, ok := reg.Environments[registry.DefaultEnvName]; !ok {
		t.Errorf("environments = %v, want %q", reg.Names(), registry.DefaultEnvName)
	}
	if reg.DefaultEnv != registry.DefaultEnvName {
		t.Errorf("DefaultEnv = %q, want %q", reg.DefaultEnv, registry.DefaultEnvName)
	}
}

// A named first run creates that name, not the default.
func TestEnvStartRegistersTheNameItWasGiven(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	if _, _, err := startTarget(t, home, "scratch"); err != nil {
		t.Fatalf("env start scratch = %v, want nil", err)
	}
	reg := loadReg(t, home)
	if _, ok := reg.Environments["scratch"]; !ok {
		t.Errorf("environments = %v, want scratch", reg.Names())
	}
	if reg.DefaultEnv != "scratch" {
		t.Errorf("DefaultEnv = %q, want scratch", reg.DefaultEnv)
	}
}
