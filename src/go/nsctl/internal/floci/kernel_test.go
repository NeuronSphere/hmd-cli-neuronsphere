package floci

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// scriptedRunner answers each call from a queue, recording what it was asked.
type scriptedRunner struct {
	answers []string
	errs    []error
	calls   [][]string
}

func (r *scriptedRunner) Run(_ context.Context, args ...string) ([]byte, []byte, error) {
	i := len(r.calls)
	r.calls = append(r.calls, args)
	var out string
	var err error
	if i < len(r.answers) {
		out = r.answers[i]
	}
	if i < len(r.errs) {
		err = r.errs[i]
	}
	return []byte(out), nil, err
}

func (r *scriptedRunner) call(t *testing.T, i int) string {
	t.Helper()
	if i >= len(r.calls) {
		t.Fatalf("want at least %d calls, got %d", i+1, len(r.calls))
	}
	return strings.Join(r.calls[i], " ")
}

func TestALoadedModuleIsLeftAlone(t *testing.T) {
	t.Parallel()
	r := &scriptedRunner{answers: []string{"present:1"}}
	state, err := EnsureBridgeNetfilter(context.Background(), r, "", "img", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != BridgeAlreadyLoaded {
		t.Errorf("state = %v, want already loaded", state)
	}
	if len(r.calls) != 1 {
		t.Errorf("want one probe and no modprobe, got %d calls", len(r.calls))
	}
}

// present:0 is not this function's business: the module -- the part only the
// host can supply -- is there, and k3s sets the value itself.
func TestAZeroSysctlIsNotAMissingModule(t *testing.T) {
	t.Parallel()
	r := &scriptedRunner{answers: []string{"present:0"}}
	state, _ := EnsureBridgeNetfilter(context.Background(), r, "", "img", false)
	if state != BridgeAlreadyLoaded {
		t.Errorf("state = %v, want already loaded", state)
	}
	if len(r.calls) != 1 {
		t.Errorf("modprobe ran for a sysctl value: %v", r.calls)
	}
}

func TestAnAbsentModuleIsLoadedAndConfirmed(t *testing.T) {
	t.Parallel()
	r := &scriptedRunner{answers: []string{"absent", "", "present:1"}}
	state, err := EnsureBridgeNetfilter(context.Background(), r, "", "k3s:img", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != BridgeLoaded {
		t.Errorf("state = %v, want loaded", state)
	}
	load := r.call(t, 1)
	for _, want := range []string{"--privileged", "--network host", "/lib/modules:/lib/modules:ro", "modprobe", "br_netfilter"} {
		if !strings.Contains(load, want) {
			t.Errorf("load is missing %q: %s", want, load)
		}
	}
}

// A modprobe that exits 0 and leaves the sysctl absent must not be recorded as a
// fix, which is why the state is re-read rather than inferred.
func TestASilentlyIneffectiveLoadIsNotAFix(t *testing.T) {
	t.Parallel()
	r := &scriptedRunner{answers: []string{"absent", "", "absent"}}
	state, err := EnsureBridgeNetfilter(context.Background(), r, "", "img", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != BridgeUnavailable {
		t.Errorf("state = %v, want unavailable", state)
	}
}

func TestAKernelThatCannotBePreparedIsReportedNotFatal(t *testing.T) {
	t.Parallel()
	r := &scriptedRunner{
		answers: []string{"absent", ""},
		errs:    []error{nil, errors.New("modprobe: module not found")},
	}
	state, err := EnsureBridgeNetfilter(context.Background(), r, "", "img", false)
	if state != BridgeUnavailable {
		t.Errorf("state = %v, want unavailable", state)
	}
	if err == nil || !strings.Contains(err.Error(), "module not found") {
		t.Errorf("the cause is dropped: %v", err)
	}
}

func TestAnEngineThatWillNotAnswerIsUnknownAndNotUnavailable(t *testing.T) {
	t.Parallel()
	r := &scriptedRunner{errs: []error{errors.New("cannot connect")}}
	state, _ := EnsureBridgeNetfilter(context.Background(), r, "", "img", false)
	if state != BridgeUnknown {
		t.Errorf("state = %v, want unknown -- a question docker did not answer is not a diagnosis", state)
	}
}

func TestOptingOutRunsNothing(t *testing.T) {
	t.Parallel()
	r := &scriptedRunner{answers: []string{"absent"}}
	state, err := EnsureBridgeNetfilter(context.Background(), r, "", "img", true)
	if err != nil || state != BridgeSkipped {
		t.Errorf("state = %v, err = %v, want skipped", state, err)
	}
	if len(r.calls) != 0 {
		t.Errorf("opting out still touched the engine: %v", r.calls)
	}
}

// The probe must sample a fresh namespace, because that is what the k3s
// container gets; the host namespace is a different question with a different
// answer.
func TestTheProbeReadsAFreshNamespace(t *testing.T) {
	t.Parallel()
	r := &scriptedRunner{answers: []string{"present:1"}}
	if _, err := ProbeBridgeNetfilter(context.Background(), r, "", "img"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	probe := r.call(t, 0)
	if !strings.Contains(probe, "--network none") {
		t.Errorf("probe does not use a fresh namespace: %s", probe)
	}
	if strings.Contains(probe, "--network host") {
		t.Errorf("probe reads the host namespace: %s", probe)
	}
	if !strings.Contains(probe, "--pull never") {
		t.Errorf("probe may reach the network: %s", probe)
	}
}

func TestHostArgsPinsOnlyWhenThereIsAnEndpoint(t *testing.T) {
	t.Parallel()
	if got := HostArgs("", "run"); strings.Join(got, " ") != "run" {
		t.Errorf("an empty host added flags: %v", got)
	}
	if got := HostArgs("unix:///x.sock", "run"); strings.Join(got, " ") != "--host unix:///x.sock run" {
		t.Errorf("host not prepended: %v", got)
	}
}

// execer is a K3sExecer that answers with a fixed reading.
type execer struct {
	out  string
	err  error
	args []string
}

func (e *execer) Exec(_ context.Context, _ string, args ...string) ([]byte, error) {
	e.args = args
	return []byte(e.out), e.err
}

func TestTheClusterReadingComesFromInsideTheContainer(t *testing.T) {
	t.Parallel()
	e := &execer{out: "present:1\n"}
	got, err := ClusterBridgeNetfilter(context.Background(), e, "floci-eks-x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "present:1" {
		t.Errorf("answer = %q", got)
	}
	// It must read the container's own namespace, not run a new one: that is
	// the only namespace whose value decides whether a ClusterIP works.
	if strings.Join(e.args, " ") != "sh -c "+BridgeProbeScript {
		t.Errorf("unexpected exec: %v", e.args)
	}
}

func TestAnAbsentModuleIsReportedFromInsideTheCluster(t *testing.T) {
	t.Parallel()
	got, err := ClusterBridgeNetfilter(context.Background(), &execer{out: "absent"}, "c")
	if err != nil || got != "absent" {
		t.Errorf("got %q, err %v", got, err)
	}
}

func TestAClusterThatWillNotAnswerIsAnError(t *testing.T) {
	t.Parallel()
	if _, err := ClusterBridgeNetfilter(context.Background(), &execer{err: errors.New("not running")}, "c"); err == nil {
		t.Error("an unanswered exec must not read as a healthy cluster")
	}
}
