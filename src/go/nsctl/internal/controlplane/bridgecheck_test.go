package controlplane

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/doctor"
)

// fakeRunner answers by subcommand, so a test can make the image present and
// the probe fail independently of each other.
type fakeRunner struct {
	calls  [][]string
	stdout map[string]string
	err    map[string]error
	stderr map[string]string
}

func (f *fakeRunner) Run(_ context.Context, args ...string) ([]byte, []byte, error) {
	f.calls = append(f.calls, args)
	key := subcommand(args)
	return []byte(f.stdout[key]), []byte(f.stderr[key]), f.err[key]
}

// subcommand is the first argument that is not a global flag or its value.
func subcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "--host" {
			i++
			continue
		}
		return args[i]
	}
	return ""
}

// probing is a runner whose image is present and whose probe says answer.
func probing(answer string) *fakeRunner {
	return &fakeRunner{stdout: map[string]string{"image": "sha256:x", "run": answer}}
}

func onlyCheck(t *testing.T, checks []doctor.Check) doctor.Check {
	t.Helper()
	if len(checks) != 1 {
		t.Fatalf("want exactly one row, got %d: %+v", len(checks), checks)
	}
	if checks[0].Name != "bridge netfilter" {
		t.Errorf("row name = %q", checks[0].Name)
	}
	return checks[0]
}

func TestALoadedModuleIsOK(t *testing.T) {
	t.Parallel()
	c := onlyCheck(t, bridgeCheck(context.Background(), probing("present:1\n"), ""))
	if c.Status != doctor.StatusOK {
		t.Errorf("status = %v, want ok", c.Status)
	}
	if c.Remedy != "" {
		t.Errorf("a working kernel needs no remedy, got %q", c.Remedy)
	}
}

func TestAnAbsentModuleWarnsAndNamesModprobe(t *testing.T) {
	t.Parallel()
	c := onlyCheck(t, bridgeCheck(context.Background(), probing("absent"), ""))
	// Warn, not fail: Gate's caller does not know whether a cluster is coming
	// (NERD028 SPEC004).
	if c.Status != doctor.StatusWarn {
		t.Errorf("status = %v, want warning", c.Status)
	}
	if !strings.Contains(c.Detail, "br_netfilter") {
		t.Errorf("detail does not name the module: %q", c.Detail)
	}
	if !strings.Contains(c.Remedy, "modprobe br_netfilter") {
		t.Errorf("remedy does not name the command: %q", c.Remedy)
	}
}

// The two faults need opposite fixes, so the rows must never be interchangeable:
// telling someone to modprobe a module that is already loaded wastes the one
// piece of advice they get.
func TestAZeroSysctlAsksForASysctlAndNotAModprobe(t *testing.T) {
	t.Parallel()
	c := onlyCheck(t, bridgeCheck(context.Background(), probing("present:0"), ""))
	if c.Status != doctor.StatusWarn {
		t.Errorf("status = %v, want warning", c.Status)
	}
	if !strings.Contains(c.Remedy, "sysctl") {
		t.Errorf("remedy does not name sysctl: %q", c.Remedy)
	}
	if strings.Contains(c.Remedy, "modprobe") {
		t.Errorf("remedy tells the user to load an already-loaded module: %q", c.Remedy)
	}
}

func TestAnUnreadableSysctlIsNotAMissingModule(t *testing.T) {
	t.Parallel()
	c := onlyCheck(t, bridgeCheck(context.Background(), probing("unreadable"), ""))
	if c.Status != doctor.StatusWarn {
		t.Errorf("status = %v, want warning", c.Status)
	}
	if c.Remedy != "" {
		t.Errorf("an unknown answer earns no remedy, got %q", c.Remedy)
	}
}

// The cold-first-run contract, and the row most likely to regress: nothing is
// pulled, so a machine that has not cached the probe image gets no row at all
// rather than a warning about the probe (NERD028 SPEC003).
func TestAnAbsentProbeImageIsNoRow(t *testing.T) {
	t.Parallel()
	d := &fakeRunner{err: map[string]error{"image": errors.New("No such image")}}
	if checks := bridgeCheck(context.Background(), d, ""); len(checks) != 0 {
		t.Fatalf("want no rows, got %+v", checks)
	}
	if len(d.calls) != 1 {
		t.Errorf("want the probe skipped entirely, got %d calls", len(d.calls))
	}
}

func TestAnEngineThatWillNotRunAContainerWarns(t *testing.T) {
	t.Parallel()
	d := probing("")
	d.err = map[string]error{"run": errors.New("exit status 125")}
	d.stderr = map[string]string{"run": "docker: permission denied"}
	c := onlyCheck(t, bridgeCheck(context.Background(), d, ""))
	if c.Status != doctor.StatusWarn {
		t.Errorf("status = %v, want warning", c.Status)
	}
	if !strings.Contains(c.Detail, "permission denied") {
		t.Errorf("detail drops the engine's own words: %q", c.Detail)
	}
	if c.Remedy != "" {
		t.Errorf("a question the engine did not answer earns no remedy, got %q", c.Remedy)
	}
}

func TestAnUnplannedAnswerIsNotReadAsWorking(t *testing.T) {
	t.Parallel()
	c := onlyCheck(t, bridgeCheck(context.Background(), probing("maybe?"), ""))
	if c.Status != doctor.StatusWarn {
		t.Errorf("status = %v, want warning", c.Status)
	}
	if !strings.Contains(c.Detail, "maybe?") {
		t.Errorf("detail does not quote what it could not read: %q", c.Detail)
	}
}

// The property a reviewer cannot see by reading the row text: the probe must run
// on the endpoint Gate proved, not on whatever the docker CLI defaults to. This
// is the defect NERD021 exists to prevent, one layer down.
func TestEveryCallIsPinnedToTheGivenEndpoint(t *testing.T) {
	t.Parallel()
	d := probing("present:1")
	bridgeCheck(context.Background(), d, "unix:///x.sock")
	if len(d.calls) != 2 {
		t.Fatalf("want an image check and a probe, got %d calls", len(d.calls))
	}
	for _, call := range d.calls {
		if len(call) < 2 || call[0] != "--host" || call[1] != "unix:///x.sock" {
			t.Errorf("call not pinned to the endpoint: %v", call)
		}
	}
}

func TestNoEndpointMeansNoHostFlag(t *testing.T) {
	t.Parallel()
	d := probing("present:1")
	bridgeCheck(context.Background(), d, "")
	for _, call := range d.calls {
		for _, arg := range call {
			if arg == "--host" {
				t.Errorf("unasked-for --host in %v", call)
			}
		}
	}
}

// A spelling test, deliberately: each of these flags is load-bearing, and
// removing one should be a decision someone has to defend rather than a silent
// behaviour change. --network none gives the fresh namespace that makes the
// reading representative of what k3s gets; --pull never keeps a preflight from
// reaching the network.
func TestTheProbeRunsInAFreshNamespaceAndNeverPulls(t *testing.T) {
	t.Parallel()
	d := probing("present:1")
	bridgeCheck(context.Background(), d, "")
	run := strings.Join(d.calls[1], " ")
	for _, want := range []string{"--rm", "--network none", "--pull never", bridgeProbeImage} {
		if !strings.Contains(run, want) {
			t.Errorf("probe is missing %q: %s", want, run)
		}
	}
	if strings.Contains(run, "--network host") {
		t.Errorf("probe reads the host namespace, not the one k3s gets: %s", run)
	}
}
