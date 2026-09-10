package cpext

import (
	"errors"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/compose"
)

func failed(instance, reason string) Extension {
	return Extension{Instance: instance, Err: errors.New(reason)}
}

func TestFailuresReportOneLinePerExtension(t *testing.T) {
	t.Parallel()

	// Three containers of one extension failing on the same missing image is
	// one problem. Printing it three times buries the other one.
	report := Report{
		Extensions: []Extension{{Instance: "registry"}, {Instance: "cache"}},
		Results: []compose.Result{
			{Service: "registry-a", Err: errors.New("manifest unknown")},
			{Service: "registry-b", Err: errors.New("manifest unknown")},
			{Service: "registry-c", Err: errors.New("manifest unknown")},
			{Service: "cache-a", Err: errors.New("no such host")},
		},
	}
	got := report.Failures()
	if len(got) != 2 {
		t.Fatalf("Failures = %v, want one line per extension", got)
	}
	if got[0] != "cache: no such host" || got[1] != "registry: manifest unknown" {
		t.Errorf("Failures = %v", got)
	}
	if report.OK() {
		t.Error("OK reported true with failures")
	}
}

func TestFailuresIncludeResolveFailures(t *testing.T) {
	t.Parallel()

	report := Report{Extensions: []Extension{failed("broken", "no working tree"), {Instance: "fine"}}}
	got := report.Failures()
	if len(got) != 1 || !strings.Contains(got[0], "no working tree") {
		t.Fatalf("Failures = %v", got)
	}
	if running := report.Running(); len(running) != 1 || running[0] != "fine" {
		t.Errorf("Running = %v, want only the healthy extension", running)
	}
}

func TestOKIsTrueWhenNothingFailed(t *testing.T) {
	t.Parallel()

	report := Report{
		Extensions: []Extension{{Instance: "registry"}},
		Results:    []compose.Result{{Service: "registry-a", Action: compose.ActionCreated}},
	}
	if !report.OK() {
		t.Errorf("OK = false with no failures: %v", report.Failures())
	}
}

// A control plane with no manifest is OK, which is what makes "absent, nothing
// extra, no complaint" the default NERD004 requires.
func TestAnEmptyReportIsOK(t *testing.T) {
	t.Parallel()

	if !(Report{}).OK() {
		t.Error("an empty report is not OK")
	}
}

func TestReportFailuresSaysTheRestIsUnaffected(t *testing.T) {
	t.Parallel()

	var b strings.Builder
	ReportFailures(&b, Report{Extensions: []Extension{failed("registry", "manifest unknown")}})
	out := b.String()
	for _, want := range []string{"registry: manifest unknown", "rest of the control plane is unaffected"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q does not contain %q", out, want)
		}
	}
}

func TestReportFailuresPrintsNothingWhenNothingFailed(t *testing.T) {
	t.Parallel()

	var b strings.Builder
	ReportFailures(&b, Report{Extensions: []Extension{{Instance: "registry"}}})
	if b.String() != "" {
		t.Errorf("printed %q for a clean run", b.String())
	}
}

// nsctl cannot write /etc/hosts, so it prints the exact line rather than
// leaving a name that does not resolve with no obvious cause.
func TestReportHostsPrintsTheLineToAdd(t *testing.T) {
	t.Parallel()

	var b strings.Builder
	ReportHosts(&b, []Extension{
		{Instance: "registry", Host: "registry.local.neuronsphere.io"},
		{Instance: "quiet"},
	})
	out := b.String()
	if !strings.Contains(out, "127.0.0.1 registry.local.neuronsphere.io") {
		t.Errorf("output %q", out)
	}
	if strings.Contains(out, "quiet") {
		t.Errorf("an extension serving nothing appeared in the hosts line: %q", out)
	}
}

func TestReportHostsPrintsNothingWhenNoExtensionServes(t *testing.T) {
	t.Parallel()

	var b strings.Builder
	ReportHosts(&b, []Extension{{Instance: "quiet"}})
	if b.String() != "" {
		t.Errorf("printed %q", b.String())
	}
}

func TestVhostsCarryTheResolvedUpstream(t *testing.T) {
	t.Parallel()

	got := Vhosts([]Extension{
		{Instance: "registry", Host: "r.example", Upstream: "proj-registry-server-1:3141"},
		failed("broken", "no tree"),
		{Instance: "quiet"},
	})
	if len(got) != 1 {
		t.Fatalf("Vhosts = %+v, want only the one that serves", got)
	}
	if got[0].Name != "registry" || got[0].Upstream != "proj-registry-server-1:3141" {
		t.Errorf("Vhosts[0] = %+v", got[0])
	}
}

// The project carries the control plane's own name, so the compose labels
// group the platform and its extensions together.
func TestProjectSharesTheControlPlaneProjectName(t *testing.T) {
	t.Parallel()

	p := Project([]Extension{
		{Instance: "z", Services: []compose.Service{{Key: "z-server"}}},
		{Instance: "a", Services: []compose.Service{{Key: "a-server"}}},
		failed("broken", "no tree"),
	}, Options{ProjectName: projectName, Networks: platformNetworks})

	if p.Name != projectName {
		t.Errorf("Name = %q, want the control plane's", p.Name)
	}
	if len(p.Services) != 2 {
		t.Fatalf("services = %+v; a failed extension must contribute none", p.Services)
	}
	if p.Services[0].Key != "a-server" {
		t.Errorf("services are not in a stable order: %+v", p.Services)
	}
}
