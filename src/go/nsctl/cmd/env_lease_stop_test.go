package cmd

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// NERD035 SPEC006: a session's end stops its environment.

type stopRecorder struct {
	mu   sync.Mutex
	envs []string
}

func (r *stopRecorder) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.envs...)
}

// stubStop records stops for one test. Not parallel, for stubStart's reason.
func stubStop(t *testing.T) *stopRecorder {
	t.Helper()
	r := &stopRecorder{}
	prev := stopEnvironment
	stopEnvironment = func(_ context.Context, _ *Options, _, slug string, _ io.Writer) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.envs = append(r.envs, slug)
		return nil
	}
	t.Cleanup(func() { stopEnvironment = prev })
	return r
}

func TestEndingASessionStopsItsEnvironment(t *testing.T) {
	stops := stubStop(t)
	home := registryHome(t, twoEnvRegistry)
	env, token := acquireSession(t, home)

	_, stderr, err := run(t, fakeEnv(map[string]string{"HMD_HOME": home}),
		"env", "lease", "release", env, "--token", token, "--session")
	if err != nil {
		t.Fatal(err)
	}
	if got := stops.got(); len(got) != 1 || got[0] != env {
		t.Errorf("stopped %v, want [%s]", got, env)
	}
	if !strings.Contains(stderr, "stopping it") {
		t.Errorf("stderr %q does not say the environment is being stopped", stderr)
	}
}

func TestKeepRunningOnReleaseOrAcquireSkipsTheStop(t *testing.T) {
	stops := stubStop(t)
	home := registryHome(t, twoEnvRegistry)
	plain := fakeEnv(map[string]string{"HMD_HOME": home})

	env, token := acquireSession(t, home)
	if _, _, err := run(t, plain, "env", "lease", "release", env, "--token", token, "--session", "--keep-running"); err != nil {
		t.Fatal(err)
	}
	env, token = acquireSession(t, home, "--keep-running")
	if _, _, err := run(t, plain, "env", "lease", "release", env, "--token", token, "--session"); err != nil {
		t.Fatal(err)
	}
	if got := stops.got(); len(got) != 0 {
		t.Errorf("stopped %v, want nothing", got)
	}
}

func TestAnExpiredSessionIsStoppedByTheNextCommandThatLooks(t *testing.T) {
	stops := stubStop(t)
	home := registryHome(t, twoEnvRegistry)
	plain := fakeEnv(map[string]string{"HMD_HOME": home})

	if _, _, err := run(t, plain, "env", "lease", "acquire", "dev", "--session", "--pid", "0", "--ttl", "1ms"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	out, stderr, err := run(t, plain, "env", "lease", "list")
	if err != nil {
		t.Fatal(err)
	}
	if got := stops.got(); len(got) != 1 || got[0] != "dev" {
		t.Errorf("stopped %v, want [dev]", got)
	}
	if !strings.Contains(stderr, "session on dev ended") {
		t.Errorf("stderr %q does not say why dev is being stopped", stderr)
	}
	if strings.Contains(out, "stopping") {
		t.Errorf("stdout %q carries the stop; it belongs on stderr", out)
	}
}

func TestARunLeaseEndingStopsNothingHere(t *testing.T) {
	stops := stubStop(t)
	home := registryHome(t, twoEnvRegistry)
	plain := fakeEnv(map[string]string{"HMD_HOME": home})

	out, _, err := run(t, plain, "env", "lease", "acquire", "dev", "--pid", "0", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, plain, "env", "lease", "release", "dev", "--token", decodeLease(t, out).Token); err != nil {
		t.Fatal(err)
	}
	if got := stops.got(); len(got) != 0 {
		t.Errorf("a run lease's release stopped %v", got)
	}
}

func TestKeepRunningNeedsSession(t *testing.T) {
	t.Parallel()
	home := registryHome(t, twoEnvRegistry)
	plain := fakeEnv(map[string]string{"HMD_HOME": home})

	for _, args := range [][]string{
		{"env", "lease", "acquire", "dev", "--pid", "0", "--keep-running"},
		{"env", "lease", "release", "dev", "--token", "x", "--keep-running"},
	} {
		if _, _, err := run(t, plain, args...); nserr.CodeOf(err) != nserr.Usage || !strings.Contains(err.Error(), "--session") {
			t.Errorf("%v = %v, want a usage error naming --session", args, err)
		}
	}
}
