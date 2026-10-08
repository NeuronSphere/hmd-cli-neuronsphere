package lease

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// NERD035 SPEC006: a session's end stops its environment, outside the lock,
// behind a placeholder lease.

type stops struct {
	mu   sync.Mutex
	envs []string
	// during is what List showed while the stop ran.
	during []Lease
	err    error
}

func (st *stops) list() []string {
	st.mu.Lock()
	defer st.mu.Unlock()
	return append([]string(nil), st.envs...)
}

// stoppingStore is testStore with a recording OnSessionEnd. The callback lists
// leases, which only works if the host lock is not held while it runs.
func stoppingStore(t *testing.T) (*Store, *time.Time, map[int]bool, *stops) {
	t.Helper()
	s, now, alive := testStore(t)
	st := &stops{}
	s.PID = 900
	alive[900] = true
	s.OnSessionEnd = func(env string) error {
		leases, _, err := s.List()
		if err != nil {
			t.Errorf("listing inside the stop: %v", err)
		}
		st.mu.Lock()
		defer st.mu.Unlock()
		st.envs = append(st.envs, env)
		st.during = leases
		return st.err
	}
	return s, now, alive, st
}

func TestEndingASessionStopsItsEnvironmentOutsideTheLock(t *testing.T) {
	t.Parallel()
	s, _, _, st := stoppingStore(t)

	l, err := s.Acquire("dev", sessionReq("b", 200))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseSession("dev", l.Token, false); err != nil {
		t.Fatalf("ReleaseSession() = %v", err)
	}
	if got := st.list(); len(got) != 1 || got[0] != "dev" {
		t.Fatalf("stopped %v, want [dev]", got)
	}
	// While it stopped, the environment was held -- by the stop, not by b.
	if len(st.during) != 1 || !strings.Contains(st.during[0].Holder, "stopping") || st.during[0].PID != 900 {
		t.Errorf("during the stop the leases were %+v, want one placeholder held by pid 900", st.during)
	}
	// And afterwards it is free and freshly released.
	leases, _, _ := s.List()
	if len(leases) != 0 {
		t.Errorf("after the stop the leases are %+v, want none", leases)
	}
	if releasedAt(s.releasedPath("dev")).IsZero() {
		t.Error("the stopped environment has no .released time")
	}
}

func TestKeepRunningEndsTheSessionWithoutAStop(t *testing.T) {
	t.Parallel()
	s, _, _, st := stoppingStore(t)

	l, err := s.Acquire("dev", sessionReq("b", 200))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseSession("dev", l.Token, true); err != nil {
		t.Fatal(err)
	}
	r := sessionReq("c", 300)
	r.KeepRunning = true
	l2, err := s.Acquire("dev", r)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseSession("dev", l2.Token, false); err != nil {
		t.Fatal(err)
	}
	if got := st.list(); len(got) != 0 {
		t.Errorf("stopped %v, want nothing: one release and one lease asked to keep it running", got)
	}
}

func TestAnExpiredSessionIsStoppedOnceByWhoeverNoticesIt(t *testing.T) {
	t.Parallel()
	s, now, _, st := stoppingStore(t)

	if _, err := s.Acquire("dev", sessionReq("b", 200)); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(9 * time.Hour)
	if _, _, err := s.List(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.List(); err != nil {
		t.Fatal(err)
	}
	if got := st.list(); len(got) != 1 || got[0] != "dev" {
		t.Errorf("stopped %v, want dev exactly once", got)
	}
}

func TestADeadSessionProcessStopsItsEnvironment(t *testing.T) {
	t.Parallel()
	s, _, alive, st := stoppingStore(t)

	if _, err := s.Acquire("dev", sessionReq("b", 200)); err != nil {
		t.Fatal(err)
	}
	delete(alive, 200)
	if _, err := s.Acquire("dev", req("next", 100)); err == nil {
		t.Error("the environment was granted while its stop was due; the stop must hold it")
	}
	if got := st.list(); len(got) != 1 {
		t.Errorf("stopped %v, want dev", got)
	}
	if _, err := s.Acquire("dev", req("next", 100)); err != nil {
		t.Errorf("after the stop the environment is still held: %v", err)
	}
}

func TestARunLeaseEndingStopsNothing(t *testing.T) {
	t.Parallel()
	s, now, _, st := stoppingStore(t)

	l, err := s.Acquire("dev", req("a", 100))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Release("dev", l.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Acquire("local", req("a", 100)); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Hour)
	_, _, _ = s.List()
	if got := st.list(); len(got) != 0 {
		t.Errorf("stopped %v after run leases ended, want nothing", got)
	}
}

func TestAFailedStopStillFreesTheEnvironment(t *testing.T) {
	t.Parallel()
	s, _, _, st := stoppingStore(t)
	st.err = errors.New("docker went away")

	l, err := s.Acquire("dev", sessionReq("b", 200))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseSession("dev", l.Token, false); err != nil {
		t.Fatalf("ReleaseSession() = %v; a failed stop is reported, not a failed release", err)
	}
	if leases, _, _ := s.List(); len(leases) != 0 {
		t.Errorf("a failed stop left %+v", leases)
	}
}

func TestWithoutACallbackAnEndedSessionIsSimplyGone(t *testing.T) {
	t.Parallel()
	s, now, _ := testStore(t)

	if _, err := s.Acquire("dev", sessionReq("b", 200)); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(9 * time.Hour)
	if leases, _, _ := s.List(); len(leases) != 0 {
		t.Errorf("leases = %+v, want none: nothing here can stop an environment, so nothing holds it", leases)
	}
}
