package cmd

import (
	"os"
	"strings"
	"testing"
)

// A session is identified by its process as well as its token, so a Claude
// Code hook -- which may not see the exports a SessionStart hook wrote --
// can still find, renew and end its session's lease.

// asSession makes this test's process the session process. Not parallel.
func asSession(t *testing.T) {
	t.Helper()
	prev := sessionProcess
	sessionProcess = func() int { return os.Getpid() }
	t.Cleanup(func() { sessionProcess = prev })
}

func TestASecondSessionAcquireFromTheSameSessionReturnsItsLease(t *testing.T) {
	asSession(t)
	home := registryHome(t, twoEnvRegistry)
	plain := fakeEnv(map[string]string{"HMD_HOME": home})

	out, _, err := run(t, plain, "env", "lease", "acquire", "--session", "--pool", "--json")
	if err != nil {
		t.Fatal(err)
	}
	first := decodeLease(t, out)
	if first.PID != os.Getpid() {
		t.Fatalf("session lease watches pid %d, want the session process %d", first.PID, os.Getpid())
	}
	// A SessionStart hook firing again on /clear or compact.
	out, _, err = run(t, plain, "env", "lease", "acquire", "--session", "--pool", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if again := decodeLease(t, out); again.Token != first.Token || again.Env != first.Env {
		t.Errorf("second acquire = %+v, want the same lease on %s", again, first.Env)
	}
	list, _, _ := run(t, plain, "env", "lease", "list")
	if strings.Count(list, "session") != 1 {
		t.Errorf("the session holds more than one environment: %q", list)
	}
}

func TestHeartbeatWhoamiAndReleaseFindTheSessionWithoutAToken(t *testing.T) {
	asSession(t)
	home := registryHome(t, twoEnvRegistry)
	plain := fakeEnv(map[string]string{"HMD_HOME": home})

	if _, _, err := run(t, plain, "env", "lease", "acquire", "dev", "--session"); err != nil {
		t.Fatal(err)
	}
	if out, _, err := run(t, plain, "env", "lease", "heartbeat"); err != nil || !strings.Contains(out, "dev") {
		t.Errorf("heartbeat without a token = %q, %v", out, err)
	}
	if out, _, err := run(t, plain, "env", "lease", "whoami"); err != nil || !strings.Contains(out, "dev") {
		t.Errorf("whoami without a token = %q, %v", out, err)
	}
	if _, _, err := run(t, plain, "env", "lease", "release", "--session"); err != nil {
		t.Fatalf("release --session without a name or token: %v", err)
	}
	if list, _, _ := run(t, plain, "env", "lease", "list"); !strings.Contains(list, "No environment is leased") {
		t.Errorf("the session's lease survived its release: %q", list)
	}
}

func TestReleaseWithoutANameStillNeedsSession(t *testing.T) {
	t.Parallel()
	home := registryHome(t, twoEnvRegistry)
	if _, _, err := run(t, fakeEnv(map[string]string{"HMD_HOME": home}), "env", "lease", "release"); err == nil {
		t.Error("release with no name and no --session succeeded")
	}
}
