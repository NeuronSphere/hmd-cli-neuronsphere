package cmd

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/container"
)

// TestMain points every test in this package at a Docker daemon that does not
// exist. Container and volume names are global, not per HMD_HOME, so a test
// whose command reaches Docker by mistake -- a refusal not yet implemented, a
// guard that regressed -- would otherwise act on the platform running on the
// machine. On 2026-10-08 one such test, `env purge --yes` in a scratch home,
// removed the real control plane's containers and swept two dozen volumes.
//
// DOCKER_HOST outranks every docker context, for the CLI nsctl shells out to
// and for the Engine API endpoint internal/dockerhost resolves, so this fails
// closed whichever path a command takes.
func TestMain(m *testing.M) {
	os.Setenv("DOCKER_HOST", "unix:///nonexistent/nsctl-cmd-test/docker.sock")
	os.Unsetenv("DOCKER_CONTEXT")
	// A session's bring-up and its end start and stop environments. No test
	// reaches the real ones unless it says so with stubStart/stubStop; a
	// session lease that merely expires in a test must not run env stop.
	startEnvironment = func(context.Context, *Options, string, string, startOptions) error {
		return errors.New("starting an environment is stubbed out in cmd tests")
	}
	stopEnvironment = func(context.Context, *Options, string, string, io.Writer) error { return nil }
	os.Exit(m.Run())
}

func TestTestsCannotReachDocker(t *testing.T) {
	requireNoDocker(t)
}

// requireNoDocker stops a test that runs a destructive verb if TestMain's dead
// endpoint is not in effect: a test that can see real containers must not be
// allowed to purge them.
func requireNoDocker(t *testing.T) {
	t.Helper()
	if names := container.New().ContainerNames(context.Background()); len(names) > 0 {
		t.Fatalf("this test can see %d real container(s); refusing to run a destructive verb", len(names))
	}
}
