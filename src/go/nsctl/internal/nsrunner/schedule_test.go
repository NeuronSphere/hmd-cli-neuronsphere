package nsrunner

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// dagDocker is the fake the scheduling tests are built on. Two things it
// records are what SPEC011 actually claims:
//
//   - for each node, which nodes had already finished when it started -- the
//     ordering half;
//   - whether the nodes named in `overlap` were all inside Run at the same
//     moment, enforced by holding each of them until the last one arrives --
//     the concurrency half. A scheduler that ran them one at a time would sit
//     on that gate until it timed out, and the node would fail.
type dagDocker struct {
	mu         sync.Mutex
	finished   map[string]bool
	sawDone    map[string]map[string]bool
	running    map[string]bool
	overlapped map[string]map[string]bool
	fail       map[string]bool
	// linger holds a node open past the point its peers have returned, which
	// is how a test puts one node still in flight when another has failed.
	linger map[string]time.Duration

	overlap map[string]bool
	arrived int
	gate    chan struct{}
}

func newDagDocker(overlap ...string) *dagDocker {
	d := &dagDocker{
		finished: map[string]bool{}, sawDone: map[string]map[string]bool{},
		running: map[string]bool{}, overlapped: map[string]map[string]bool{},
		fail: map[string]bool{}, linger: map[string]time.Duration{},
		overlap: map[string]bool{}, gate: make(chan struct{}),
	}
	for _, name := range overlap {
		d.overlap[name] = true
	}
	if len(overlap) == 0 {
		close(d.gate)
	}
	return d
}

func (d *dagDocker) Run(_ context.Context, args ...string) ([]byte, []byte, error) {
	name := instanceOf(args)

	d.mu.Lock()
	d.sawDone[name] = copySet(d.finished)
	// Two nodes overlap if and only if one of them entered while the other was
	// already running, so recording it in both directions at entry catches
	// every overlap without sampling.
	d.overlapped[name] = copySet(d.running)
	for other := range d.running {
		d.overlapped[other][name] = true
	}
	d.running[name] = true
	held := d.overlap[name]
	if held {
		d.arrived++
		if d.arrived == len(d.overlap) {
			close(d.gate)
		}
	}
	d.mu.Unlock()

	// Every node dwells briefly so that "ran alongside" is observable at all.
	// Without it a node can enter and leave this function between two
	// dispatches, and a scheduler that ignored every edge would still look
	// serial. It is orders of magnitude longer than the dispatch loop's own
	// work, which is a setNode and a small file write.
	time.Sleep(50 * time.Millisecond)

	if held {
		select {
		case <-d.gate:
		case <-time.After(10 * time.Second):
			// Deliberately a node failure rather than a t.Fatal: this runs on a
			// worker goroutine, and failing the node surfaces in the workflow
			// the test is already asserting on.
			d.mu.Lock()
			delete(d.running, name)
			d.mu.Unlock()
			return nil, []byte("never ran concurrently"), &execError{}
		}
	}

	d.mu.Lock()
	wait := d.linger[name]
	d.mu.Unlock()
	time.Sleep(wait)

	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.running, name)
	d.finished[name] = true
	if d.fail[name] {
		return []byte("out"), []byte("boom"), &execError{}
	}
	return []byte("out"), nil, nil
}

func copySet(m map[string]bool) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

// startedAfter is the set of nodes that had finished by the time `instance`
// began.
func (d *dagDocker) startedAfter(instance string) map[string]bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return copySet(d.sawDone[instance])
}

// alongside is every node that was running at the same time as this one.
func (d *dagDocker) alongside(instance string) map[string]bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return copySet(d.overlapped[instance])
}

func (d *dagDocker) ran(instance string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.sawDone[instance]
	return ok
}

// instanceOf reads the node's identity off the container's environment, which
// dockerArgs always passes.
func instanceOf(args []string) string {
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, "HMD_INSTANCE_NAME="); ok {
			return v
		}
	}
	return ""
}

// A diamond is the smallest graph that tests both halves of SPEC011 at once:
// `top` gates `left` and `right`, which must run together, and `bottom` must
// wait for both.
func TestADiamondRespectsItsEdgesAndRunsTheMiddleConcurrently(t *testing.T) {
	t.Parallel()
	d := newDagDocker("left", "right")
	s := testServer(t, d)

	wf, err := s.Submit(submission(
		node("top", "hmd-inf-thing"),
		node("left", "hmd-inf-thing", "top"),
		node("right", "hmd-inf-thing", "top"),
		node("bottom", "hmd-inf-thing", "left", "right"),
	))
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	got := waitTerminal(t, s, wf.Name)

	// The concurrency half: left and right only get past the gate if they were
	// both inside Run at once, so anything else fails them.
	if got.Phase != PhaseSucceeded {
		t.Fatalf("phase = %q, want Succeeded (%s) -- the middle nodes did not overlap", got.Phase, got.Message)
	}
	if got.Parallelism != 4 {
		t.Errorf("parallelism = %d, want the default 4", got.Parallelism)
	}

	if !d.alongside("left")["right"] {
		t.Error("left and right never overlapped")
	}

	// The ordering half. Nothing may run alongside the two ends, and each node
	// must find its dependencies already finished when it starts.
	for _, end := range []string{"top", "bottom"} {
		if with := d.alongside(end); len(with) != 0 {
			t.Errorf("%s ran alongside %v; the diamond serialises at both ends", end, with)
		}
	}
	if after := d.startedAfter("top"); len(after) != 0 {
		t.Errorf("top started after %v, want nothing -- it has no dependencies", after)
	}
	for _, mid := range []string{"left", "right"} {
		if !d.startedAfter(mid)["top"] {
			t.Errorf("%s started before top finished", mid)
		}
	}
	after := d.startedAfter("bottom")
	if !after["left"] || !after["right"] || !after["top"] {
		t.Errorf("bottom started after %v, want all of top, left and right", after)
	}
	if len(got.Succeeded) != 4 {
		t.Errorf("Succeeded = %v, want all four", got.Succeeded)
	}
}

// Fail-fast under concurrency means two different things at once: a node that
// was never started must not start, and a node already in flight must be left
// to finish -- killing it mid-deploy would leave half-applied Terraform behind,
// which is worse than the deploy it was doing.
func TestAFailureStopsUnstartedNodesAndLetsInFlightOnesFinish(t *testing.T) {
	t.Parallel()
	d := newDagDocker("doomed", "sibling")
	d.fail["doomed"] = true
	// The sibling is deliberately still running when doomed fails, which is the
	// only arrangement that distinguishes draining from abandoning.
	d.linger["sibling"] = 300 * time.Millisecond
	s := testServer(t, d)

	wf, err := s.Submit(submission(
		node("doomed", "hmd-inf-thing"),
		node("sibling", "hmd-inf-thing"),
		node("downstream", "hmd-inf-thing", "doomed"),
	))
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	got := waitTerminal(t, s, wf.Name)

	if got.Phase != PhaseFailed {
		t.Fatalf("phase = %q, want Failed", got.Phase)
	}
	if got.Failure == nil || got.Failure.InstanceName != "doomed" {
		t.Fatalf("Failure = %+v, want the failed node named", got.Failure)
	}
	if d.ran("downstream") {
		t.Error("downstream ran; a node that had not started must not start after a failure")
	}
	// The in-flight sibling landed, so it belongs in the snapshot -- omitting
	// it would redeploy it forever.
	if len(got.Succeeded) != 1 || got.Succeeded[0] != "sibling" {
		t.Errorf("Succeeded = %v, want [sibling]", got.Succeeded)
	}
	byName := nodesByName(got)
	if byName["doomed"].Phase != NodeFailed {
		t.Errorf("doomed = %q, want Failed", byName["doomed"].Phase)
	}
	if byName["sibling"].Phase != NodeSucceeded {
		t.Errorf("sibling = %q, want Succeeded -- it finished", byName["sibling"].Phase)
	}
	if byName["downstream"].Phase != NodePending {
		t.Errorf("downstream = %q, want Pending -- it was never attempted", byName["downstream"].Phase)
	}
}

// The core repo class deploys nothing, but it is still a node: apply_changeset
// gives it real edges, so a scheduler that skipped its release step would leave
// everything that depends on it waiting forever.
func TestTheCoreInstanceReleasesItsSuccessors(t *testing.T) {
	t.Parallel()
	d := newDagDocker()
	s := testServer(t, d)

	wf, err := s.Submit(submission(
		node("core", "hmd-cli-neuronsphere"),
		node("dependent", "hmd-inf-thing", "core"),
	))
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	got := waitTerminal(t, s, wf.Name)

	if got.Phase != PhaseSucceeded {
		t.Fatalf("phase = %q, want Succeeded (%s)", got.Phase, got.Message)
	}
	if len(got.Succeeded) != 2 {
		t.Errorf("Succeeded = %v, want both", got.Succeeded)
	}
	if !d.ran("dependent") {
		t.Error("the dependent never ran; the core node did not release it")
	}
	if d.ran("core") {
		t.Error("the core node launched a container; it is a status flip")
	}
}

// A dependency naming an instance outside this changeset is already met: the
// field arrives filtered to what is being deployed, so an edge to something
// absent points at something already deployed. Treating it as unmet would hang.
func TestADependencyOutsideTheChangesetIsAlreadyMet(t *testing.T) {
	t.Parallel()
	d := newDagDocker()
	s := testServer(t, d)

	wf, err := s.Submit(submission(node("only", "hmd-inf-thing", "deployed-last-week")))
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	got := waitTerminal(t, s, wf.Name)

	if got.Phase != PhaseSucceeded {
		t.Fatalf("phase = %q, want Succeeded (%s)", got.Phase, got.Message)
	}
}

// Nothing should emit a cycle, but a scheduler that hangs on one holds its
// environment's mutex forever, which is a far worse failure than a bad report.
func TestACycleIsReportedRatherThanHungOn(t *testing.T) {
	t.Parallel()
	d := newDagDocker()
	s := testServer(t, d)

	wf, err := s.Submit(submission(
		node("a", "hmd-inf-thing", "b"),
		node("b", "hmd-inf-thing", "a"),
	))
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	got := waitTerminal(t, s, wf.Name)

	if got.Phase != PhaseFailed {
		t.Fatalf("phase = %q, want Failed", got.Phase)
	}
	if !strings.Contains(got.Message, "cycle") {
		t.Errorf("message = %q, want it to name the cycle", got.Message)
	}
	if d.ran("a") || d.ran("b") {
		t.Error("a node in the cycle ran; neither was ever runnable")
	}
}

// Concurrency is overridable per submission, and 1 is the way back to the
// sequential behaviour this replaced.
func TestASubmissionCanPinItsConcurrency(t *testing.T) {
	t.Parallel()
	d := newDagDocker()
	s := testServer(t, d)

	sub := submission(
		node("first", "hmd-inf-thing"),
		node("second", "hmd-inf-thing"),
	)
	sub.Workflow.Parallelism = 1
	wf, err := s.Submit(sub)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	got := waitTerminal(t, s, wf.Name)

	if got.Phase != PhaseSucceeded {
		t.Fatalf("phase = %q, want Succeeded (%s)", got.Phase, got.Message)
	}
	if got.Parallelism != 1 {
		t.Errorf("parallelism = %d, want the pinned 1", got.Parallelism)
	}
	// With one worker the ready queue is drained in payload order, so the
	// second node cannot have started until the first was done.
	if !d.startedAfter("second")["first"] {
		t.Error("second started before first finished; the pin was ignored")
	}
}

// Parallel nodes share one log, and that log is what `env start` and
// `env attach` show. Without attribution it reads as several deploys shuffled
// together; with one node in flight there is nothing to attribute, so the
// prefix is not there to be noise.
func TestParallelOutputIsAttributedAndSequentialOutputIsNot(t *testing.T) {
	t.Parallel()
	d := newDagDocker("left", "right")
	s := testServer(t, d)

	wf, err := s.Submit(submission(
		node("left", "hmd-inf-thing"),
		node("right", "hmd-inf-thing"),
	))
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	waitTerminal(t, s, wf.Name)
	backlog, _ := s.Store.Log(wf.Name).Subscribe()
	log := strings.Join(backlog, "")
	if !strings.Contains(log, "[left] ") || !strings.Contains(log, "[right] ") {
		t.Errorf("the parallel log attributes nothing:\n%s", log)
	}

	solo := testServer(t, newDagDocker())
	one, err := solo.Submit(submission(node("alone", "hmd-inf-thing")))
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	waitTerminal(t, solo, one.Name)
	backlog, _ = solo.Store.Log(one.Name).Subscribe()
	if log := strings.Join(backlog, ""); strings.Contains(log, "[alone] ") {
		t.Errorf("a single-node run was prefixed anyway:\n%s", log)
	}
}

func nodesByName(wf Workflow) map[string]NodeStatus {
	out := make(map[string]NodeStatus, len(wf.Nodes))
	for _, n := range wf.Nodes {
		out[n.InstanceName] = n
	}
	return out
}
