package environment

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/container"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
)

const wakeNetwork = "neuronsphere_default-abc123"

type fakeWakeDocker struct {
	onNetwork map[string][]string
	running   map[string]bool
	stopped   []string
}

func (f *fakeWakeDocker) ContainersWithLabelOnNetwork(_ context.Context, key, value, network string) []string {
	return f.onNetwork[key+"="+value+"@"+network]
}

func (f *fakeWakeDocker) Running(_ context.Context, name string) (bool, error) {
	return f.running[name], nil
}

func (f *fakeWakeDocker) Stop(_ context.Context, name string) error {
	f.stopped = append(f.stopped, name)
	return nil
}

func wakeRegistry() *registry.Registry {
	return &registry.Registry{
		ControlPlane: registry.ControlPlane{Network: wakeNetwork},
		Environments: map[string]registry.Environment{
			"local":   {Slug: "local", AccountID: "000000000001"},
			"scratch": {Slug: "scratch", AccountID: "000000000002"},
		},
	}
}

func wakeDocker(running map[string]bool) *fakeWakeDocker {
	key := func(a string) string { return container.LabelFlociAccount + "=" + a + "@" + wakeNetwork }
	return &fakeWakeDocker{
		onNetwork: map[string][]string{
			key("000000000001"): {"floci-eks-local"},
			key("000000000002"): {"floci-eks-scratch", "floci-rds-db-scratch"},
		},
		running: running,
	}
}

// Starting one environment must not start the others. Floci's EKS service
// brings back every cluster it knows about the moment it is asked a question,
// which is what `env start` does -- so a start of `local` used to leave every
// other environment's k3s running while nsctl still called them stopped.
func TestStartSweepsClustersFlociWokeForOtherEnvironments(t *testing.T) {
	t.Parallel()

	d := wakeDocker(map[string]bool{
		"floci-eks-local":      true,
		"floci-eks-scratch":    true, // Floci woke this one
		"floci-rds-db-scratch": true, // and this
	})

	sweepWokenEnvironments(context.Background(), testOptions("", nil), d, wakeRegistry(), "local", map[string]bool{})

	sort.Strings(d.stopped)
	if got := strings.Join(d.stopped, ","); got != "floci-eks-scratch,floci-rds-db-scratch" {
		t.Fatalf("stopped %q; the started environment must be exempt and the woken one swept", got)
	}
}

// An environment the user deliberately had running is not one Floci woke.
func TestStartLeavesAnotherRunningEnvironmentAlone(t *testing.T) {
	t.Parallel()

	before := map[string]bool{"floci-eks-scratch": true, "floci-rds-db-scratch": true}
	d := wakeDocker(map[string]bool{
		"floci-eks-local":      true,
		"floci-eks-scratch":    true,
		"floci-rds-db-scratch": true,
	})

	sweepWokenEnvironments(context.Background(), testOptions("", nil), d, wakeRegistry(), "local", before)

	if len(d.stopped) != 0 {
		t.Fatalf("stopped %v, but every container was already running before the start", d.stopped)
	}
}
