package controlplane

import (
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bundled"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/compose"
)

// The runner is a fourth container in a file three other things already parse,
// so the parse and the exclusive-publisher rule are what can break.
func TestTheRunnerServiceParsesAndPublishesNoHostPort(t *testing.T) {
	t.Parallel()
	data, err := bundled.Read(bundled.ControlPlaneComposeFile)
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"HMD_HOME":      "/home/hmd",
		"HMD_REPO_HOME": "/home/repos",
	}
	p, err := compose.Parse(data, "ns-test", func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("parsing the control-plane compose file: %v", err)
	}

	var runner *compose.Service
	for i := range p.Services {
		if p.Services[i].Key == "nsrunner" {
			runner = &p.Services[i]
		}
	}
	if runner == nil {
		t.Fatal("no nsrunner service in the compose file")
	}
	if len(runner.Ports) != 0 {
		t.Errorf("nsrunner publishes %v; only the proxy may publish", runner.Ports)
	}
	if runner.ContainerName != RunnerContainer {
		t.Errorf("container name = %q, want %q", runner.ContainerName, RunnerContainer)
	}

	// Identical-path mounts are what make the sibling containers it launches
	// resolvable: the bind sources it composes are read by the host daemon.
	var home, repos bool
	for _, v := range runner.Volumes {
		if v.Source == "/home/hmd" && v.Target == "/home/hmd" {
			home = true
		}
		if v.Source == "/home/repos" && v.Target == "/home/repos" {
			repos = true
		}
	}
	if !home || !repos {
		t.Errorf("HMD_HOME/HMD_REPO_HOME are not mounted at identical paths: %v", runner.Volumes)
	}

	// The profile is what keeps it off until it is asked for.
	if !runner.EnabledBy(map[string]bool{RunnerProfile: true}) {
		t.Error("the nsrunner profile does not enable it")
	}
	if runner.EnabledBy(map[string]bool{}) {
		t.Error("nsrunner starts with no profile active")
	}

	if c := compose.CheckExclusivePublisher(p, map[string]bool{RunnerProfile: true}); len(c) != 0 {
		t.Errorf("exclusive-publisher conflicts: %v", c)
	}
}

func TestTheRunnerIsOffUnlessAskedFor(t *testing.T) {
	t.Parallel()
	off := &Options{Lookup: func(string) string { return "" }}
	if RunnerEnabled(off) {
		t.Error("the runner is on by default; its image is not published yet")
	}
	if RunnerRoute(off) != nil {
		t.Error("a disabled runner still claims a proxy route")
	}

	on := &Options{Lookup: func(k string) string {
		if k == "HMD_LOCAL_NEURONSPHERE_ENABLE_RUNNER" {
			return "true"
		}
		return ""
	}}
	if !RunnerEnabled(on) {
		t.Fatal("HMD_LOCAL_NEURONSPHERE_ENABLE_RUNNER=true did not enable it")
	}
	route := RunnerRoute(on)
	if got := route[RunnerRoutePath]; !strings.Contains(got, RunnerContainer) {
		t.Errorf("route = %q, want it to name %s", got, RunnerContainer)
	}
	if !ActiveProfiles(on)[RunnerProfile] {
		t.Error("the runner profile is not active when enabled")
	}
}
