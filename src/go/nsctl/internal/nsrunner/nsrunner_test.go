package nsrunner

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/runner"
)

// fakeDocker stands in for the Docker CLI. Every node "deploys" by recording
// its invocation, so a whole workflow can run with no daemon.
type fakeDocker struct {
	mu   sync.Mutex
	runs int
	fail map[int]bool
}

func (f *fakeDocker) Run(_ context.Context, args ...string) ([]byte, []byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs++
	if f.fail[f.runs] {
		return []byte("out"), []byte("boom"), &execError{}
	}
	return []byte("out"), nil, nil
}

type execError struct{}

func (e *execError) Error() string { return "exit status 1" }

// testHome builds an HMD_HOME with one registered environment and a working
// tree for the repo class the nodes name.
func testHome(t *testing.T) (home string, lookup func(string) string) {
	t.Helper()
	home = t.TempDir()
	repoHome := filepath.Join(home, "repos")
	// Several classes, not one: two nodes of the same repo class no longer
	// deploy at once (they share this working tree), so a test that needs two
	// nodes to overlap has to give them different classes.
	for _, class := range []string{
		"hmd-inf-thing", "hmd-inf-left", "hmd-inf-right",
		"hmd-inf-doomed", "hmd-inf-sibling",
	} {
		mkdir(t, filepath.Join(repoHome, class))
	}

	reg := map[string]any{
		"version":     1,
		"default_env": "local",
		"control_plane": map[string]any{
			"network": "neuronsphere_default", "bootstrapped": true,
			"compose_project": "ns-test",
		},
		"environments": map[string]any{
			"local": map[string]any{
				"name": "local", "slug": "local",
				"account_id": "000000000001", "deployment_id": "local",
				"state_dir":   filepath.Join(home, ".cache", "environments", "local"),
				"k3s_cluster": "ns-local-test", "port_slot": 0,
				"kubeconfig": filepath.Join(home, ".cache", "environments", "local", "k3s", "kubeconfig"),
			},
		},
	}
	data, err := json.Marshal(reg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".cache", "neuronsphere", "environments.json")
	mkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"HMD_HOME": home, "HMD_REPO_HOME": repoHome}
	return home, func(k string) string { return env[k] }
}

func mkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func testServer(t *testing.T, d runner.Docker) *Server {
	t.Helper()
	home, lookup := testHome(t)
	s, err := NewServer(home, lookup, os.Stderr)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	s.Docker = d
	// No ms-deployment in a unit test; an unreachable client would only add
	// warnings to the log, so the status flips are simply not attempted.
	s.MSDeploymentURL = ""
	t.Cleanup(func() { s.Shutdown(context.Background()) })
	return s
}

func submission(nodes ...map[string]any) Submission {
	raw, _ := json.Marshal(map[string]any{
		"namespace": "local",
		"workflow": map[string]any{
			"csd_nid": "csd-1", "environment": "local", "nodes": nodes,
		},
	})
	var sub Submission
	_ = json.Unmarshal(raw, &sub)
	return sub
}

// node builds one DAG node. The trailing names are its `dependencies` -- the
// field ms-deployment has always populated and the scheduler now reads. A node
// with none is runnable immediately, and several of those run at once.
func node(instance, class string, deps ...string) map[string]any {
	return map[string]any{
		"instance_name": instance, "repo_class_name": class,
		"version": "0.1", "rid_nid": instance + "-rid",
		"script":       "hmd --repo-name " + class + " deploy",
		"dependencies": deps,
	}
}

// waitTerminal blocks until the workflow settles, which is what the log stream
// closing already signals.
func waitTerminal(t *testing.T, s *Server, name string) Workflow {
	t.Helper()
	lb := s.Store.Log(name)
	if lb == nil {
		t.Fatalf("no log for %s", name)
	}
	_, ch := lb.Subscribe()
	for ch != nil {
		if _, open := <-ch; !open {
			break
		}
	}
	rec, ok := s.Store.Get(name)
	if !ok {
		t.Fatalf("workflow %s vanished", name)
	}
	return rec.snapshot()
}

func TestASuccessfulWorkflowRecordsEveryNode(t *testing.T) {
	t.Parallel()
	d := &fakeDocker{}
	s := testServer(t, d)

	wf, err := s.Submit(submission(node("thing", "hmd-inf-thing")))
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	got := waitTerminal(t, s, wf.Name)

	if got.Phase != PhaseSucceeded {
		t.Errorf("phase = %q, want Succeeded (%s)", got.Phase, got.Message)
	}
	if len(got.Succeeded) != 1 || got.Succeeded[0] != "thing" {
		t.Errorf("Succeeded = %v, want [thing]", got.Succeeded)
	}
	if got.Nodes[0].Phase != NodeSucceeded {
		t.Errorf("node phase = %q, want Succeeded", got.Nodes[0].Phase)
	}
}

// A failure must stop the run for everything downstream of it. The nodes that
// never ran stay Pending rather than Failed, because calling them failed would
// be a claim the reconcile snapshot then acts on.
//
// The chain here is explicit now that the scheduler reads `dependencies`:
// without the edge these two are independent and would rightly run at once.
func TestAFailedNodeStopsTheRunAndLeavesLaterNodesPending(t *testing.T) {
	t.Parallel()
	d := &fakeDocker{fail: map[int]bool{1: true}}
	s := testServer(t, d)

	wf, err := s.Submit(submission(
		node("first", "hmd-inf-thing"),
		node("second", "hmd-inf-thing", "first"),
	))
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	got := waitTerminal(t, s, wf.Name)

	if got.Phase != PhaseFailed {
		t.Errorf("phase = %q, want Failed", got.Phase)
	}
	if got.Nodes[0].Phase != NodeFailed {
		t.Errorf("first node = %q, want Failed", got.Nodes[0].Phase)
	}
	if got.Nodes[1].Phase != NodePending {
		t.Errorf("second node = %q, want Pending -- it was never attempted", got.Nodes[1].Phase)
	}
	if len(got.Succeeded) != 0 {
		t.Errorf("Succeeded = %v, want empty", got.Succeeded)
	}
	if got.Failure == nil || got.Failure.InstanceName != "first" {
		t.Errorf("Failure = %+v, want the first node", got.Failure)
	}
	if d.runs != 1 {
		t.Errorf("ran %d nodes, want 1 -- the second must not start", d.runs)
	}
}

// The partial-progress record is what a reconcile snapshot reads, so a run that
// failed partway must still name what landed: no more, or a failed entry is
// skipped next time; no less, or a succeeded one is redeployed forever.
func TestAPartialRunRecordsWhatLanded(t *testing.T) {
	t.Parallel()
	d := &fakeDocker{fail: map[int]bool{2: true}}
	s := testServer(t, d)

	wf, err := s.Submit(submission(
		node("first", "hmd-inf-thing"),
		node("second", "hmd-inf-thing", "first"),
		node("third", "hmd-inf-thing", "second"),
	))
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	got := waitTerminal(t, s, wf.Name)

	if len(got.Succeeded) != 1 || got.Succeeded[0] != "first" {
		t.Errorf("Succeeded = %v, want [first]", got.Succeeded)
	}
}

// The core instance is a status flip that executes nothing.
func TestTheCoreInstanceLaunchesNoContainer(t *testing.T) {
	t.Parallel()
	d := &fakeDocker{}
	s := testServer(t, d)

	wf, err := s.Submit(submission(node("core", "hmd-cli-neuronsphere")))
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	got := waitTerminal(t, s, wf.Name)

	if got.Phase != PhaseSucceeded {
		t.Errorf("phase = %q, want Succeeded", got.Phase)
	}
	if d.runs != 0 {
		t.Errorf("ran %d containers, want 0", d.runs)
	}
}

// Two submissions must not interleave on one environment. This is the race the
// in-process runner could not refuse, because nothing knew the other was there.
func TestASecondSubmissionForABusyEnvironmentIsRefused(t *testing.T) {
	t.Parallel()
	block := make(chan struct{})
	d := &blockingDocker{gate: block, started: make(chan struct{}, 1)}
	s := testServer(t, d)

	first, err := s.Submit(submission(node("thing", "hmd-inf-thing")))
	if err != nil {
		t.Fatalf("first Submit: %v", err)
	}
	<-d.started

	_, err = s.Submit(submission(node("other", "hmd-inf-thing")))
	var busy *BusyError
	if err == nil {
		t.Fatal("the second submission was accepted; it must be refused")
	}
	if !asBusy(err, &busy) {
		t.Fatalf("err = %v, want a BusyError", err)
	}
	if busy.Workflow != first.Name {
		t.Errorf("refused naming %q, want %q", busy.Workflow, first.Name)
	}

	close(block)
	waitTerminal(t, s, first.Name)

	// Once it finishes the environment is free again.
	if _, err := s.Submit(submission(node("other", "hmd-inf-thing"))); err != nil {
		t.Errorf("submitting after the first finished: %v", err)
	}
}

type blockingDocker struct {
	gate    chan struct{}
	started chan struct{}
	once    sync.Once
}

func (b *blockingDocker) Run(_ context.Context, _ ...string) ([]byte, []byte, error) {
	b.once.Do(func() {})
	select {
	case b.started <- struct{}{}:
	default:
	}
	<-b.gate
	return nil, nil, nil
}

func asBusy(err error, target **BusyError) bool {
	b, ok := err.(*BusyError)
	if ok {
		*target = b
	}
	return ok
}

func TestSubmitRejectsAManifestWithNoEnvironment(t *testing.T) {
	t.Parallel()
	s := testServer(t, &fakeDocker{})
	sub := submission(node("thing", "hmd-inf-thing"))
	sub.Workflow.Environment = ""
	if _, err := s.Submit(sub); err == nil {
		t.Fatal("accepted a submission naming no environment")
	}
}

// A record left Running belongs to a process that is gone. Leaving it Running
// would let a caller wait forever and would keep its environment locked.
func TestAWorkflowLeftRunningByADeadProcessIsRecoveredAsFailed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	stale := Workflow{Name: "nsrun-old", Environment: "local", Phase: PhaseRunning}
	data, _ := json.MarshalIndent(&stale, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "nsrun-old.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	rec, ok := store.Get("nsrun-old")
	if !ok {
		t.Fatal("the stale workflow was not loaded")
	}
	if got := rec.snapshot(); got.Phase != PhaseFailed {
		t.Errorf("phase = %q, want Failed", got.Phase)
	}
	if _, busy := store.ActiveFor("local"); busy {
		t.Error("the recovered workflow still holds its environment")
	}
}

// Recovering a log must not destroy it: newLogBuffer truncates, so the read
// path has to use a constructor that does not.
func TestRecoveringALogDoesNotTruncateIt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	wf := Workflow{Name: "nsrun-old", Environment: "local", Phase: PhaseSucceeded}
	data, _ := json.MarshalIndent(&wf, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "nsrun-old.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nsrun-old.log"), []byte("earlier output\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	backlog, ch := store.Log("nsrun-old").Subscribe()
	if ch != nil {
		t.Error("a finished workflow handed out a live channel")
	}
	if len(backlog) == 0 || !strings.Contains(backlog[0], "earlier output") {
		t.Errorf("backlog = %v, want the log from disk", backlog)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "nsrun-old.log")); len(b) == 0 {
		t.Error("the log file was truncated by reading it")
	}
}

func TestTheAPIReportsAWorkflowAndRefusesAnUnknownOne(t *testing.T) {
	t.Parallel()
	s := testServer(t, &fakeDocker{})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	body := strings.NewReader(`{"namespace":"local","workflow":{"csd_nid":"csd-1","environment":"local","nodes":[]}}`)
	resp, err := http.Post(srv.URL+"/api/v1/workflows/local", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("submit status = %d, want 200", resp.StatusCode)
	}
	var submitted struct {
		Metadata struct{ Name string } `json:"metadata"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&submitted); err != nil {
		t.Fatal(err)
	}
	// The name is what a changeset deployment records, exactly as it records
	// Argo's.
	if !strings.HasPrefix(submitted.Metadata.Name, "nsrun-") {
		t.Errorf("name = %q, want an nsrun- prefix", submitted.Metadata.Name)
	}

	missing, err := http.Get(srv.URL + "/api/v1/workflows/local/nsrun-nope")
	if err != nil {
		t.Fatal(err)
	}
	defer missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Errorf("unknown workflow status = %d, want 404", missing.StatusCode)
	}
}

func TestABusySubmissionIsA409(t *testing.T) {
	t.Parallel()
	block := make(chan struct{})
	d := &blockingDocker{gate: block, started: make(chan struct{}, 1)}
	s := testServer(t, d)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	if _, err := s.Submit(submission(node("thing", "hmd-inf-thing"))); err != nil {
		t.Fatal(err)
	}
	<-d.started

	body := strings.NewReader(`{"namespace":"local","workflow":{"csd_nid":"csd-2","environment":"local","nodes":[]}}`)
	resp, err := http.Post(srv.URL+"/api/v1/workflows/local", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("status = %d, want 409", resp.StatusCode)
	}
	close(block)
}

// The client and the service are two halves of one protocol, so the round trip
// is what is worth asserting: submit, stream, then read the outcome.
func TestTheClientRoundTripsASubmissionAndItsLog(t *testing.T) {
	t.Parallel()
	s := testServer(t, &fakeDocker{})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL)

	ctx := context.Background()
	if !c.Reachable(ctx) {
		t.Fatal("Reachable said no to a running server")
	}

	name, err := c.Submit(ctx, "local", Manifest{
		CSDNid: "csd-1", Environment: "local",
		Nodes: decodeNodes(t, node("thing", "hmd-inf-thing")),
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	var log strings.Builder
	if err := c.Stream(ctx, "local", name, &log); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if !strings.Contains(log.String(), "thing") {
		t.Errorf("the streamed log never mentions the node:\n%s", log.String())
	}

	wf, err := c.Get(ctx, "local", name)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if wf.Phase != PhaseSucceeded {
		t.Errorf("phase = %q, want Succeeded (%s)", wf.Phase, wf.Message)
	}
	if len(wf.Succeeded) != 1 {
		t.Errorf("Succeeded = %v, want one instance", wf.Succeeded)
	}
}

// A failed deploy has to reach the CLI as a failure with the detail attached,
// or the caller cannot say which node stopped it.
func TestTheClientSeesAFailureAndItsDetail(t *testing.T) {
	t.Parallel()
	s := testServer(t, &fakeDocker{fail: map[int]bool{1: true}})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL)
	ctx := context.Background()

	name, err := c.Submit(ctx, "local", Manifest{
		CSDNid: "csd-1", Environment: "local",
		Nodes: decodeNodes(t, node("thing", "hmd-inf-thing")),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Stream(ctx, "local", name, io.Discard); err != nil {
		t.Fatal(err)
	}
	wf, err := c.Get(ctx, "local", name)
	if err != nil {
		t.Fatal(err)
	}
	if wf.Phase != PhaseFailed {
		t.Fatalf("phase = %q, want Failed", wf.Phase)
	}
	if wf.Failure == nil || wf.Failure.InstanceName != "thing" {
		t.Fatalf("Failure = %+v, want the failed node named", wf.Failure)
	}
	// The subnet-group migration reads stderr off this, so it has to survive.
	if !strings.Contains(wf.Failure.Stderr, "boom") {
		t.Errorf("stderr = %q, want the node's stderr", wf.Failure.Stderr)
	}
}

// Active is how `env attach` finds a deployment it did not start.
func TestActiveFindsAnInFlightDeploymentAndNothingAfterwards(t *testing.T) {
	t.Parallel()
	block := make(chan struct{})
	d := &blockingDocker{gate: block, started: make(chan struct{}, 1)}
	s := testServer(t, d)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL)
	ctx := context.Background()

	name, err := c.Submit(ctx, "local", Manifest{
		CSDNid: "csd-1", Environment: "local",
		Nodes: decodeNodes(t, node("thing", "hmd-inf-thing")),
	})
	if err != nil {
		t.Fatal(err)
	}
	<-d.started

	wf, active, err := c.Active(ctx, "local", "local")
	if err != nil {
		t.Fatal(err)
	}
	if !active || wf.Name != name {
		t.Fatalf("Active = (%q, %v), want the in-flight %q", wf.Name, active, name)
	}

	close(block)
	waitTerminal(t, s, name)

	if _, active, err = c.Active(ctx, "local", "local"); err != nil || active {
		t.Errorf("Active still reports a deployment after it finished (err=%v)", err)
	}
}

// A busy submission must be distinguishable, because the CLI turns it into a
// different exit code than a real failure.
func TestTheClientReportsBusyDistinctly(t *testing.T) {
	t.Parallel()
	block := make(chan struct{})
	d := &blockingDocker{gate: block, started: make(chan struct{}, 1)}
	s := testServer(t, d)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL)
	ctx := context.Background()

	if _, err := c.Submit(ctx, "local", Manifest{
		CSDNid: "csd-1", Environment: "local",
		Nodes: decodeNodes(t, node("thing", "hmd-inf-thing")),
	}); err != nil {
		t.Fatal(err)
	}
	<-d.started

	_, err := c.Submit(ctx, "local", Manifest{CSDNid: "csd-2", Environment: "local"})
	var se *SubmitError
	if !errorsAs(err, &se) {
		t.Fatalf("err = %v, want a SubmitError", err)
	}
	if !se.Busy() {
		t.Errorf("status = %d, want it to report busy", se.Status)
	}
	close(block)
}

func TestReachableIsFalseWithNoRunner(t *testing.T) {
	t.Parallel()
	// A closed port, not a hung one: the CLI must decide quickly that there is
	// no runner and fall back, not stall every deploy waiting for one.
	if NewClient("http://127.0.0.1:1").Reachable(context.Background()) {
		t.Error("Reachable said yes with nothing listening")
	}
}

func decodeNodes(t *testing.T, raw ...map[string]any) []msdeploy.DeploymentNode {
	t.Helper()
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	var nodes []msdeploy.DeploymentNode
	if err := json.Unmarshal(data, &nodes); err != nil {
		t.Fatal(err)
	}
	return nodes
}

func errorsAs(err error, target **SubmitError) bool {
	se, ok := err.(*SubmitError)
	if ok {
		*target = se
	}
	return ok
}
