package floci

import (
	"context"
	"sort"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/container"
)

// EnvAccount names an environment and the Floci account its resources live in.
//
// A pair rather than a registry.Environment so this package stays free of the
// registry: what the sweep needs is a slug to report and an account to filter
// by, and both callers have those already.
type EnvAccount struct {
	Slug      string
	AccountID string
}

// WakeDocker is the container surface the sweep needs.
type WakeDocker interface {
	ContainersWithLabelOnNetwork(ctx context.Context, key, value, network string) []string
	Running(ctx context.Context, name string) (bool, error)
	Stop(ctx context.Context, name string) error
}

// ContainersByEnv maps each environment's slug to the Floci-spawned containers
// that belong to it.
//
// The account label is the whole test. Floci stamps io.floci.account on every
// container it spawns; each environment holds an account of its own and the
// control plane's is ControlPlaneAccountID, so an environment's containers are
// exactly those carrying its account, and the control plane's own database and
// graph can never be caught by this.
func ContainersByEnv(ctx context.Context, d WakeDocker, envs []EnvAccount, network string) map[string][]string {
	out := map[string][]string{}
	for _, e := range envs {
		if e.AccountID == "" || e.AccountID == ControlPlaneAccountID {
			continue
		}
		if names := d.ContainersWithLabelOnNetwork(ctx, container.LabelFlociAccount,
			e.AccountID, network); len(names) > 0 {
			out[e.Slug] = append(out[e.Slug], names...)
		}
	}
	return out
}

// RunningEnvContainers is the set of environment containers up right now. It is
// the "before" half of StopWoken: what the user already had running.
func RunningEnvContainers(ctx context.Context, d WakeDocker, envs []EnvAccount, network string) map[string]bool {
	out := map[string]bool{}
	for _, names := range ContainersByEnv(ctx, d, envs, network) {
		for _, name := range names {
			if running, err := d.Running(ctx, name); err == nil && running {
				out[name] = true
			}
		}
	}
	return out
}

// WakeFailure is a container that should have been stopped and would not be.
type WakeFailure struct {
	Slug      string
	Container string
	Err       error
}

// StopWoken puts back down the environment containers Floci started while
// something else was starting, and reports the environments it touched.
//
// Floci restarts the backing container of every resource it has persisted.
// That is not a guess: started by hand with no nsctl in the picture, its own
// log announces "Starting RDS backend container for instance:
// environment-db-...-scratch-..." for an environment nsctl calls stopped. Its
// EKS clusters come back too, the moment anything asks that service a question.
// And it is right for an emulator -- in AWS those instances do still exist, and
// starting one thing does not stop another's database.
//
// Locally it means every environment ever created comes back alongside whatever
// the user did start, a Postgres and a k3s cluster apiece, while `nsctl env
// list` still reports them stopped: the resources are real and idle, and
// nsctl's account of the machine disagrees with the daemon's.
//
// The invariant is the plain one: *starting one thing does not change which
// other environments are running*. Only a container that was down in `before`
// and is up now is stopped, so an environment the user had running is never
// touched, and neither is one Floci left alone. `exempt` is the environment the
// caller is itself starting.
func StopWoken(ctx context.Context, d WakeDocker, envs []EnvAccount, network string, before map[string]bool, exempt string) ([]string, []WakeFailure) {
	woke := map[string]bool{}
	var failures []WakeFailure
	for slug, names := range ContainersByEnv(ctx, d, envs, network) {
		if slug == exempt {
			continue
		}
		for _, name := range names {
			if before[name] {
				continue
			}
			if running, err := d.Running(ctx, name); err != nil || !running {
				continue
			}
			if err := d.Stop(ctx, name); err != nil {
				failures = append(failures, WakeFailure{Slug: slug, Container: name, Err: err})
				continue
			}
			woke[slug] = true
		}
	}
	return sortedKeys(woke), failures
}

func sortedKeys(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
