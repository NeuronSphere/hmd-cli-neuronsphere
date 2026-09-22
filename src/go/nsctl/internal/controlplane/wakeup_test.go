package controlplane

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/container"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
)

const testNetwork = "neuronsphere_default-abc123"

// Two environments plus a control plane, each with an account of its own.
func wakeupRegistry() *registry.Registry {
	return &registry.Registry{
		ControlPlane: registry.ControlPlane{Network: testNetwork},
		Environments: map[string]registry.Environment{
			"local":   {Slug: "local", AccountID: "000000000001"},
			"scratch": {Slug: "scratch", AccountID: "000000000002"},
			// Never enumerated: the control plane's own resources.
			"legacy": {Slug: "legacy", AccountID: registry.ControlPlaneAccountID},
		},
	}
}

func wakeupDocker(running map[string]bool) *fakeFlociDocker {
	key := func(account string) string {
		return container.LabelFlociAccount + "=" + account + "@" + testNetwork
	}
	return &fakeFlociDocker{
		onNetwork: map[string][]string{
			key("000000000001"):                 {"floci-rds-db-local", "floci-eks-local"},
			key("000000000002"):                 {"floci-rds-db-scratch", "floci-eks-scratch"},
			key(registry.ControlPlaneAccountID): {"floci-rds-db-controlplane"},
		},
		running: running,
	}
}

// The defect: Floci restarts the backing container of every resource it has
// persisted, so environments nsctl calls stopped come back with the control
// plane. Starting the control plane must not change which environments run.
func TestRestoreEnvironmentStateStopsWhatFlociWoke(t *testing.T) {
	t.Parallel()

	before := map[string]bool{} // nothing was running
	d := wakeupDocker(map[string]bool{
		"floci-rds-db-local":   true, // Floci woke both
		"floci-rds-db-scratch": true,
	})

	restoreEnvironmentState(context.Background(), testOptions("", nil), d, wakeupRegistry(), before)

	sort.Strings(d.stopped)
	if got := strings.Join(d.stopped, ","); got != "floci-rds-db-local,floci-rds-db-scratch" {
		t.Fatalf("stopped %q", got)
	}
}

// An environment the user had running is not an environment Floci woke.
func TestRestoreEnvironmentStateLeavesWhatWasAlreadyRunning(t *testing.T) {
	t.Parallel()

	before := map[string]bool{"floci-rds-db-local": true, "floci-eks-local": true}
	d := wakeupDocker(map[string]bool{
		"floci-rds-db-local":   true,
		"floci-eks-local":      true,
		"floci-rds-db-scratch": true, // only this one is new
	})

	restoreEnvironmentState(context.Background(), testOptions("", nil), d, wakeupRegistry(), before)

	if got := strings.Join(d.stopped, ","); got != "floci-rds-db-scratch" {
		t.Fatalf("stopped %q, want only the one Floci woke", got)
	}
}

// `env start` starts the control plane first. Stopping the containers it is
// about to start would be a visible stop/start cycle for nothing.
func TestRestoreEnvironmentStateExemptsTheEnvironmentBeingStarted(t *testing.T) {
	t.Parallel()

	opts := testOptions("", nil)
	opts.StartingEnv = "local"
	d := wakeupDocker(map[string]bool{
		"floci-rds-db-local":   true,
		"floci-rds-db-scratch": true,
	})

	restoreEnvironmentState(context.Background(), opts, d, wakeupRegistry(), map[string]bool{})

	if got := strings.Join(d.stopped, ","); got != "floci-rds-db-scratch" {
		t.Fatalf("stopped %q, want the started environment left alone", got)
	}
}

// The control plane's own database and graph share no account with any
// environment, so this can never reach them.
func TestRestoreEnvironmentStateNeverTouchesTheControlPlane(t *testing.T) {
	t.Parallel()

	d := wakeupDocker(map[string]bool{"floci-rds-db-controlplane": true})

	restoreEnvironmentState(context.Background(), testOptions("", nil), d, wakeupRegistry(), map[string]bool{})

	for _, name := range d.stopped {
		if strings.Contains(name, "controlplane") {
			t.Fatalf("stopped the control plane's own container %q", name)
		}
	}
}

// A container that will not stop is a warning: the control plane is up and
// refusing the start over an idle Postgres would help nobody.
func TestRestoreEnvironmentStateWarnsRatherThanFailing(t *testing.T) {
	t.Parallel()

	d := wakeupDocker(map[string]bool{"floci-rds-db-scratch": true})
	d.stopErr = map[string]error{"floci-rds-db-scratch": errors.New("no")}

	restoreEnvironmentState(context.Background(), testOptions("", nil), d, wakeupRegistry(), map[string]bool{})
	// Reaching here without a panic or an error return is the assertion.
}

// The snapshot is what tells a woken container from a running one.
func TestRunningEnvContainersSeesOnlyEnvironments(t *testing.T) {
	t.Parallel()

	d := wakeupDocker(map[string]bool{
		"floci-rds-db-local":        true,
		"floci-rds-db-controlplane": true,
		"floci-eks-scratch":         false,
	})

	got := runningEnvContainers(context.Background(), d, wakeupRegistry())

	if !got["floci-rds-db-local"] {
		t.Error("an environment's running container was missed")
	}
	if got["floci-rds-db-controlplane"] {
		t.Error("the control plane's container was counted as an environment's")
	}
	if got["floci-eks-scratch"] {
		t.Error("a stopped container was counted as running")
	}
}
