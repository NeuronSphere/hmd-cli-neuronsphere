package nsrunner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

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
	mkdir(t, filepath.Join(repoHome, "hmd-inf-thing"))

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

func node(instance, class string) map[string]any {
	return map[string]any{
		"instance_name": instance, "repo_class_name": class,
		"version": "0.1", "rid_nid": instance + "-rid",
		"script": "hmd --repo-name " + class + " deploy",
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

// A failure must stop the run: everything after it depends on it. The nodes
// that never ran stay Pending rather than Failed, because calling them failed
// would be a claim the reconcile snapshot then acts on.
func TestAFailedNodeStopsTheRunAndLeavesLaterNodesPending(t *testing.T) {
	t.Parallel()
	d := &fakeDocker{fail: map[int]bool{1: true}}
	s := testServer(t, d)

	wf, err := s.Submit(submission(
		node("first", "hmd-inf-thing"),
		node("second", "hmd-inf-thing"),
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
		node("second", "hmd-inf-thing"),
		node("third", "hmd-inf-thing"),
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
