package cmd

import (
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// leaseLocal leases local to holder "a" and returns its token.
func leaseLocal(t *testing.T, home string) string {
	t.Helper()
	out, _, err := run(t, fakeEnv(map[string]string{"HMD_HOME": home}),
		"env", "lease", "acquire", "local", "--holder", "a", "--pid", "0", "--json")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	return decodeLease(t, out).Token
}

// NERD035 SPEC001: every command that changes a leased environment refuses
// anyone but the holder, before it touches anything.
func TestLeaseRefusesOtherHoldersOnEveryMutatingCommand(t *testing.T) {
	t.Parallel()
	// Only verbs that cannot reach Docker. env start/apply/stop/purge/delete
	// run for real against the host daemon if the guard is missing, so they
	// are not in this table.
	for _, args := range [][]string{
		{"repo", "add", "hmd-ms-foo", "--env", "local"},
		{"repo", "remove", "foo", "--env", "local"},
		{"repo", "import", "--env", "local"},
		{"stack", "remove", "observability", "--env", "local"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			home := registryHome(t, twoEnvRegistry)
			leaseLocal(t, home)

			_, _, err := run(t, fakeEnv(map[string]string{"HMD_HOME": home}), args...)
			if nserr.CodeOf(err) != nserr.InUse {
				t.Fatalf("error = %v (code %d), want InUse", err, nserr.CodeOf(err))
			}
			if !strings.Contains(err.Error(), "leased by a") {
				t.Errorf("error %q does not name the holder", err)
			}
		})
	}
}

// deadPortsRegistry is twoEnvRegistry with the control plane's HTTP and Floci
// ports on port 1, so a verb that gets past a missing guard reaches no
// emulator either. Docker is already dead (TestMain).
var deadPortsRegistry = strings.Replace(twoEnvRegistry,
	`"network": "net"}`, `"network": "net", "ports": {"http": 1, "floci": 1}}`, 1)

// The verbs that would reach Docker and Floci without the guard. Each is
// expected to stop at the guard; requireNoDocker and the dead ports make a
// regression fail instead of acting on the machine's platform.
func TestLeaseRefusesOtherHoldersOnDestructiveVerbs(t *testing.T) {
	requireNoDocker(t)
	if !strings.Contains(deadPortsRegistry, `"floci": 1`) {
		t.Fatal("deadPortsRegistry did not get its ports; the fixture changed shape")
	}
	for _, args := range [][]string{
		{"env", "start", "local"},
		{"env", "apply", "local"},
		{"env", "stop", "local"},
		{"env", "purge", "local", "--yes"},
		{"env", "purge", "--yes"},
		{"env", "delete", "local", "--yes"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			home := registryHome(t, deadPortsRegistry)
			leaseLocal(t, home)

			_, _, err := run(t, fakeEnv(map[string]string{
				"HMD_HOME":                    home,
				"HMD_LOCAL_MS_DEPLOYMENT_URL": "http://127.0.0.1:1/hmd_ms_deployment",
			}), args...)
			if nserr.CodeOf(err) != nserr.InUse {
				t.Fatalf("error = %v (code %d), want InUse", err, nserr.CodeOf(err))
			}
			if !strings.Contains(err.Error(), "leased by a") {
				t.Errorf("error %q does not name the holder", err)
			}
		})
	}
}

func TestLeaseHolderMayChangeItsEnvironment(t *testing.T) {
	t.Parallel()
	home := registryHome(t, twoEnvRegistry)
	token := leaseLocal(t, home)

	if _, _, err := run(t, fakeEnv(map[string]string{"HMD_HOME": home, "NSCTL_LEASE_TOKEN": token}),
		"repo", "add", "hmd-ms-foo", "--env", "local", "--path", t.TempDir()); err != nil {
		t.Fatalf("holder via NSCTL_LEASE_TOKEN: %v", err)
	}
	if _, _, err := run(t, fakeEnv(map[string]string{"HMD_HOME": home}),
		"repo", "remove", "ms-foo", "--env", "local", "--lease-token", token); err != nil {
		t.Fatalf("holder via --lease-token: %v", err)
	}
}

func TestLeaseOnOneEnvironmentLeavesTheOthersAlone(t *testing.T) {
	t.Parallel()
	home := registryHome(t, twoEnvRegistry)
	leaseLocal(t, home)

	if _, _, err := run(t, fakeEnv(map[string]string{"HMD_HOME": home}),
		"repo", "add", "hmd-ms-foo", "--env", "dev", "--path", t.TempDir()); err != nil {
		t.Fatalf("repo add into the unleased dev: %v", err)
	}
}

func TestIgnoreLeaseProceedsAndSaysWhose(t *testing.T) {
	t.Parallel()
	home := registryHome(t, twoEnvRegistry)
	leaseLocal(t, home)

	_, stderr, err := run(t, fakeEnv(map[string]string{"HMD_HOME": home}),
		"repo", "add", "hmd-ms-foo", "--env", "local", "--path", t.TempDir(), "--ignore-lease")
	if err != nil {
		t.Fatalf("--ignore-lease: %v", err)
	}
	if !strings.Contains(stderr, "ignoring") || !strings.Contains(stderr, "a ") {
		t.Errorf("stderr %q does not say whose lease it ignored", stderr)
	}
}

func TestReadOnlyCommandsIgnoreLeases(t *testing.T) {
	t.Parallel()
	home := registryHome(t, twoEnvRegistry)
	leaseLocal(t, home)

	if _, _, err := run(t, fakeEnv(map[string]string{"HMD_HOME": home}),
		"repo", "list", "--env", "local"); nserr.CodeOf(err) == nserr.InUse {
		t.Fatalf("repo list was refused: %v", err)
	}
}
