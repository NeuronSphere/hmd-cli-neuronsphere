package lease

import (
	"errors"
	"os"
	"testing"
	"time"
)

func sessionReq(holder string, pid int) Request {
	return Request{Holder: holder, PID: pid, TTL: 8 * time.Hour, Scope: ScopeSession,
		Template: "telemetry", Repos: []string{"/w/hmd-inf-clickhouse"}}
}

func TestASessionLeaseRecordsItsScopeTemplateAndRepos(t *testing.T) {
	t.Parallel()
	s, _, _ := testStore(t)

	if _, err := s.Acquire("local", sessionReq("claude-a", 100)); err != nil {
		t.Fatal(err)
	}
	leases, _, err := s.List()
	if err != nil || len(leases) != 1 {
		t.Fatalf("List() = %v, %v", leases, err)
	}
	l := leases[0]
	if l.Scope != ScopeSession || l.Template != "telemetry" ||
		len(l.Repos) != 1 || l.Repos[0] != "/w/hmd-inf-clickhouse" {
		t.Errorf("stored lease = %+v", l)
	}
	if !l.IsSession() {
		t.Error("IsSession() = false for a session lease")
	}
}

func TestARunLeaseHasNoScopeOnDiskAndIsNotASession(t *testing.T) {
	t.Parallel()
	s, _, _ := testStore(t)

	l, err := s.Acquire("local", req("a", 100))
	if err != nil {
		t.Fatal(err)
	}
	// Absent means run, so files written before scopes existed read the same.
	if l.Scope != "" || l.IsSession() {
		t.Errorf("run lease = %+v", l)
	}
}

func TestByTokenFindsTheLiveLeaseItHolds(t *testing.T) {
	t.Parallel()
	s, now, _ := testStore(t)

	if _, err := s.Acquire("local", req("a", 100)); err != nil {
		t.Fatal(err)
	}
	mine, err := s.Acquire("dev", sessionReq("b", 200))
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.ByToken(mine.Token)
	if err != nil || got == nil || got.Env != "dev" {
		t.Fatalf("ByToken() = %+v, %v; want dev", got, err)
	}
	if got, err := s.ByToken("nope"); err != nil || got != nil {
		t.Errorf("ByToken(unknown) = %+v, %v; want nil, nil", got, err)
	}
	if got, err := s.ByToken(""); err != nil || got != nil {
		t.Errorf("ByToken(\"\") = %+v, %v; want nil, nil", got, err)
	}

	*now = now.Add(9 * time.Hour)
	if got, _ := s.ByToken(mine.Token); got != nil {
		t.Errorf("ByToken() after expiry = %+v, want nil", got)
	}
}

func TestHeartbeatRenewsByTokenAlone(t *testing.T) {
	t.Parallel()
	s, now, _ := testStore(t)

	l, err := s.Acquire("dev", sessionReq("b", 200))
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(7 * time.Hour)
	got, err := s.Heartbeat(l.Token)
	if err != nil || got.Env != "dev" {
		t.Fatalf("Heartbeat() = %+v, %v", got, err)
	}
	*now = now.Add(7 * time.Hour) // 14h after acquire, 7h after the heartbeat
	if cur, _ := s.ByToken(l.Token); cur == nil {
		t.Error("a heartbeat did not keep the session lease alive")
	}
	if _, err := s.Heartbeat("nope"); !errors.Is(err, ErrNotHolder) {
		t.Errorf("Heartbeat(unknown) error = %v, want ErrNotHolder", err)
	}
}

func TestReleaseLeavesASessionLeaseUnlessToldItIsTheSession(t *testing.T) {
	t.Parallel()
	s, _, _ := testStore(t)

	l, err := s.Acquire("dev", sessionReq("b", 200))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Release("dev", l.Token); !errors.Is(err, ErrSessionLease) {
		t.Fatalf("Release() of a session lease = %v, want ErrSessionLease", err)
	}
	if cur, _ := s.ByToken(l.Token); cur == nil {
		t.Fatal("a run-style release ended the session lease")
	}
	if err := s.ReleaseSession("dev", l.Token, false); err != nil {
		t.Fatalf("ReleaseSession() = %v", err)
	}
	if cur, _ := s.ByToken(l.Token); cur != nil {
		t.Error("ReleaseSession() left the lease in place")
	}
}

func TestReleaseSessionStillNeedsTheToken(t *testing.T) {
	t.Parallel()
	s, _, _ := testStore(t)

	if _, err := s.Acquire("dev", sessionReq("b", 200)); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseSession("dev", "nope", false); !errors.Is(err, ErrNotHolder) {
		t.Errorf("ReleaseSession(wrong token) = %v, want ErrNotHolder", err)
	}
}

func TestReleaseSessionAlsoEndsARunLease(t *testing.T) {
	t.Parallel()
	s, _, _ := testStore(t)

	l, err := s.Acquire("dev", req("a", 100))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseSession("dev", l.Token, false); err != nil {
		t.Errorf("ReleaseSession() of a run lease = %v", err)
	}
}

func TestSessionPIDIsTheNearestClaudeAncestor(t *testing.T) {
	t.Parallel()
	// 10 nsctl <- 20 zsh -c <- 30 claude <- 40 zsh <- 1 login
	tree := map[int]struct {
		ppid int
		name string
	}{
		20: {30, "zsh"},
		30: {40, "claude"},
		40: {1, "-zsh"},
		1:  {0, "launchd"},
	}
	parent := func(pid int) (int, string, error) {
		p, ok := tree[pid]
		if !ok {
			return 0, "", errors.New("no such process")
		}
		return p.ppid, p.name, nil
	}
	if got := SessionPID(20, parent); got != 30 {
		t.Errorf("SessionPID() = %d, want the claude process 30", got)
	}
}

func TestSessionPIDFallsBackToTheParent(t *testing.T) {
	t.Parallel()
	parent := func(pid int) (int, string, error) {
		switch pid {
		case 20:
			return 1, "-zsh", nil
		case 1:
			return 0, "launchd", nil
		}
		return 0, "", errors.New("no such process")
	}
	if got := SessionPID(20, parent); got != 20 {
		t.Errorf("SessionPID() = %d, want the parent 20", got)
	}
	broken := func(int) (int, string, error) { return 0, "", errors.New("denied") }
	if got := SessionPID(20, broken); got != 20 {
		t.Errorf("SessionPID() with an unreadable table = %d, want the parent 20", got)
	}
}

func TestSessionPIDMatchesClaudeByBaseName(t *testing.T) {
	t.Parallel()
	parent := func(pid int) (int, string, error) {
		switch pid {
		case 20:
			return 30, "/bin/zsh", nil
		case 30:
			return 1, "/opt/homebrew/bin/claude", nil
		case 1:
			return 0, "launchd", nil
		}
		return 0, "", errors.New("no such process")
	}
	if got := SessionPID(20, parent); got != 30 {
		t.Errorf("SessionPID() = %d, want 30", got)
	}
}

func TestProcParentReadsThisProcess(t *testing.T) {
	t.Parallel()
	ppid, name, err := ProcParent(os.Getpid())
	if err != nil {
		t.Skipf("process table unreadable here: %v", err)
	}
	if ppid != os.Getppid() || name == "" {
		t.Errorf("ProcParent(self) = %d, %q; want %d and a name", ppid, name, os.Getppid())
	}
}
