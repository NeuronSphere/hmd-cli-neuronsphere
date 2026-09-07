package nsrunner

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Store keeps workflows in memory and mirrors them to disk.
//
// Disk is what makes the inversion worth anything. A deployment that outlives
// the CLI has to be findable by the next CLI, and a runner that restarts
// mid-deploy must not leave a changeset that no one can report on -- so the
// record is written on every transition rather than at the end.
type Store struct {
	dir string

	mu   sync.Mutex
	byID map[string]*record
	logs map[string]*logBuffer
}

// NewStore opens (and creates) the workflow directory.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating the workflow directory %s: %w", dir, err)
	}
	s := &Store{dir: dir, byID: map[string]*record{}, logs: map[string]*logBuffer{}}
	s.loadAll()
	return s, nil
}

// loadAll reads what a previous process left behind. A record that cannot be
// parsed is skipped rather than fatal: one unreadable file must not stop the
// runner from serving every other workflow.
func (s *Store) loadAll() {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue
		}
		var w Workflow
		if err := json.Unmarshal(data, &w); err != nil || w.Name == "" {
			continue
		}
		// A workflow still marked Running belongs to a process that is gone.
		// Leaving it Running would let a caller wait forever on a deploy that
		// nothing is executing, and would keep its environment locked.
		if !w.Phase.Terminal() {
			w.Phase = PhaseFailed
			w.Message = "the runner restarted while this deployment was in flight"
			w.FinishedAt = now()
		}
		s.byID[w.Name] = &record{wf: w}
	}
}

func (s *Store) path(name string) string    { return filepath.Join(s.dir, name+".json") }
func (s *Store) logPath(name string) string { return filepath.Join(s.dir, name+".log") }

// Put registers a workflow and its log.
func (s *Store) Put(r *record) {
	name := r.name()
	s.mu.Lock()
	s.byID[name] = r
	s.logs[name] = newLogBuffer(s.logPath(name))
	s.mu.Unlock()
	s.Persist(r)
}

// Get returns a workflow by name.
func (s *Store) Get(name string) (*record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.byID[name]
	return w, ok
}

// Log returns the log buffer for a workflow, recovering one for a workflow this
// process did not run.
func (s *Store) Log(name string) *logBuffer {
	s.mu.Lock()
	defer s.mu.Unlock()
	if lb, ok := s.logs[name]; ok {
		return lb
	}
	if _, ok := s.byID[name]; !ok {
		return nil
	}
	lb := replayLogBuffer(s.logPath(name))
	s.logs[name] = lb
	return lb
}

// ActiveFor returns the running workflow for an environment, if there is one.
// This is the per-environment mutex: two submissions must not interleave on one
// environment, exactly as the cloud path's workflow-level synchronization
// mutex prevents.
func (s *Store) ActiveFor(environment string) (*record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.byID {
		if r.snapshot().Environment != environment {
			continue
		}
		if !r.phase().Terminal() {
			return r, true
		}
	}
	return nil, false
}

// List returns every workflow, newest first.
func (s *Store) List() []Workflow {
	s.mu.Lock()
	out := make([]Workflow, 0, len(s.byID))
	for _, r := range s.byID {
		out = append(out, r.snapshot())
	}
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].SubmittedAt > out[j].SubmittedAt })
	return out
}

// Persist writes a workflow's current state to disk. A failed write is
// deliberately silent: losing the mirror degrades reporting after a restart,
// and must not fail the deployment that is otherwise going fine.
func (s *Store) Persist(r *record) {
	snap := r.snapshot()
	data, err := json.MarshalIndent(&snap, "", "  ")
	if err != nil {
		return
	}
	tmp := s.path(snap.Name) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return
	}
	// Rename so a reader never sees a half-written record.
	_ = os.Rename(tmp, s.path(snap.Name))
}
