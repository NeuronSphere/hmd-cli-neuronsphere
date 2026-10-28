package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// acquireSession takes a session lease on local with no PID, so the test's
// own process tree never ends it.
func acquireSession(t *testing.T, home string, extra ...string) (string, string) {
	t.Helper()
	args := append([]string{"env", "lease", "acquire", "local", "--session", "--holder", "s", "--pid", "0", "--json"}, extra...)
	out, _, err := run(t, fakeEnv(map[string]string{"HMD_HOME": home}), args...)
	if err != nil {
		t.Fatalf("acquire --session: %v", err)
	}
	l := decodeLease(t, out)
	return l.Env, l.Token
}

func TestSessionAcquireIsASessionLeaseWithTheSessionTTL(t *testing.T) {
	t.Parallel()
	home := registryHome(t, twoEnvRegistry)

	out, _, err := run(t, fakeEnv(map[string]string{"HMD_HOME": home}),
		"env", "lease", "acquire", "--session", "--pid", "0", "--json")
	if err != nil {
		t.Fatal(err)
	}
	l := decodeLease(t, out)
	if l.Scope != "session" || l.Env != "local" || l.TTLSeconds != 8*3600 {
		t.Errorf("session lease = %+v, want scope session on local for 8h", l)
	}
}

func TestSessionTTLComesFromNsctlToml(t *testing.T) {
	t.Parallel()
	home := registryHome(t, twoEnvRegistry)
	if err := os.MkdirAll(filepath.Join(home, ".config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "nsctl.toml"),
		[]byte("[pool]\nsession_ttl = \"2h\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, _, err := run(t, fakeEnv(map[string]string{"HMD_HOME": home}),
		"env", "lease", "acquire", "--session", "--pid", "0", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if l := decodeLease(t, out); l.TTLSeconds != 2*3600 {
		t.Errorf("ttl_seconds = %d, want 7200 from session_ttl", l.TTLSeconds)
	}
}

func TestSessionShellPrintsTheExports(t *testing.T) {
	t.Parallel()
	home := registryHome(t, twoEnvRegistry)

	out, _, err := run(t, fakeEnv(map[string]string{"HMD_HOME": home}),
		"env", "lease", "acquire", "dev", "--session", "--pid", "0", "--shell")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "export HMD_LOCAL_ENV=dev\n") ||
		!strings.Contains(out, "export NSCTL_LEASE_TOKEN=") {
		t.Errorf("--shell output = %q", out)
	}
	if strings.Contains(out, "Leased") {
		t.Errorf("--shell output carries prose a shell would try to run: %q", out)
	}
}

func TestShellAndJSONAreExclusive(t *testing.T) {
	t.Parallel()
	home := registryHome(t, twoEnvRegistry)

	_, _, err := run(t, fakeEnv(map[string]string{"HMD_HOME": home}),
		"env", "lease", "acquire", "--session", "--pid", "0", "--shell", "--json")
	if nserr.CodeOf(err) != nserr.Usage {
		t.Errorf("--shell --json = %v, want a usage error", err)
	}
}

func TestARunInsideASessionIsAnsweredWithTheSessionLease(t *testing.T) {
	t.Parallel()
	home := registryHome(t, twoEnvRegistry)
	_, token := acquireSession(t, home)
	inSession := fakeEnv(map[string]string{"HMD_HOME": home, "NSCTL_LEASE_TOKEN": token})

	for _, args := range [][]string{
		{"env", "lease", "acquire", "--pid", "0", "--json"},
		{"env", "lease", "acquire", "local", "--pid", "0", "--json"},
		{"env", "lease", "acquire", "--pool", "--pid", "0", "--json"},
	} {
		out, _, err := run(t, inSession, args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		l := decodeLease(t, out)
		if l.Env != "local" || l.Token != token || !l.Nested {
			t.Errorf("%v = %+v, want the session's own lease, nested", args, l)
		}
	}
}

func TestARunInsideASessionStillContendsForAnotherEnvironment(t *testing.T) {
	t.Parallel()
	home := registryHome(t, twoEnvRegistry)
	_, token := acquireSession(t, home)

	out, _, err := run(t, fakeEnv(map[string]string{"HMD_HOME": home, "NSCTL_LEASE_TOKEN": token}),
		"env", "lease", "acquire", "dev", "--pid", "0", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if l := decodeLease(t, out); l.Env != "dev" || l.Token == token || l.Nested {
		t.Errorf("acquire dev from inside a session on local = %+v, want a lease of its own", l)
	}
}

func TestReleaseWithoutSessionLeavesTheSessionLease(t *testing.T) {
	t.Parallel()
	home := registryHome(t, twoEnvRegistry)
	env, token := acquireSession(t, home)
	plain := fakeEnv(map[string]string{"HMD_HOME": home})

	// What a script written for run leases does with the token it was handed.
	_, stderr, err := run(t, plain, "env", "lease", "release", env, "--token", token)
	if err != nil {
		t.Fatalf("release of a nested run: %v, want exit 0", err)
	}
	if !strings.Contains(stderr, "--session") {
		t.Errorf("stderr %q does not say how to end the session", stderr)
	}
	out, _, _ := run(t, plain, "env", "lease", "list")
	if !strings.Contains(out, "local") {
		t.Fatalf("the session lease is gone after a run-style release: %q", out)
	}

	if _, _, err := run(t, plain, "env", "lease", "release", env, "--token", token, "--session"); err != nil {
		t.Fatalf("release --session: %v", err)
	}
	out, _, _ = run(t, plain, "env", "lease", "list")
	if !strings.Contains(out, "No environment is leased") {
		t.Errorf("release --session left a lease: %q", out)
	}
}

func TestHeartbeatTakesTheTokenAlone(t *testing.T) {
	t.Parallel()
	home := registryHome(t, twoEnvRegistry)
	_, token := acquireSession(t, home)

	out, _, err := run(t, fakeEnv(map[string]string{"HMD_HOME": home, "NSCTL_LEASE_TOKEN": token}),
		"env", "lease", "heartbeat")
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if !strings.Contains(out, "local") {
		t.Errorf("heartbeat output %q does not name the environment", out)
	}
	_, _, err = run(t, fakeEnv(map[string]string{"HMD_HOME": home}),
		"env", "lease", "heartbeat", "--token", "nope")
	if nserr.CodeOf(err) != nserr.Usage {
		t.Errorf("heartbeat with an unknown token = %v, want a usage error", err)
	}
}

func TestWhoamiDescribesTheSession(t *testing.T) {
	t.Parallel()
	home := registryHome(t, twoEnvRegistry)
	_, token := acquireSession(t, home)

	out, _, err := run(t, fakeEnv(map[string]string{"HMD_HOME": home, "NSCTL_LEASE_TOKEN": token}),
		"env", "lease", "whoami")
	if err != nil {
		t.Fatalf("whoami: %v", err)
	}
	for _, want := range []string{"local", "session", "s", "/local/"} {
		if !strings.Contains(out, want) {
			t.Errorf("whoami output %q lacks %q", out, want)
		}
	}
	if strings.Contains(out, token) {
		t.Error("whoami printed the token")
	}

	_, _, err = run(t, fakeEnv(map[string]string{"HMD_HOME": home}), "env", "lease", "whoami")
	if err == nil {
		t.Error("whoami with no token exited zero")
	}
}

func TestListShowsTheScope(t *testing.T) {
	t.Parallel()
	home := registryHome(t, twoEnvRegistry)
	acquireSession(t, home)

	out, _, err := run(t, fakeEnv(map[string]string{"HMD_HOME": home}), "env", "lease", "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "session") {
		t.Errorf("list output %q does not show the session scope", out)
	}
}
