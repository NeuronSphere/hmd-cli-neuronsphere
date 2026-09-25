package controlplane

import (
	"context"
	"fmt"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/compose"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/container"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/floci"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
)

// repairStaleFlociDataDir recreates a Floci that is no longer looking at this
// home's data directory.
//
// Deleting HMD_HOME is what a stuck user reaches for, and it is the one
// recovery that makes things worse. The containers are named globally and keyed
// on the home's *path*, so they keep running; a bind mount is resolved once, at
// container creation, so Floci keeps writing into the directory that was
// unlinked. Start then remakes the path -- a new inode the running container
// cannot see -- and the reconciler leaves the container alone, correctly, since
// deleting a directory changes no configuration.
//
// Nothing downstream can attribute what follows. Floci answers every
// control-plane call from memory, CreateBucket reports a bucket it loaded at
// startup, and the first thing to touch the disk is `tofu init` refreshing
// CDKTF state, ~950 log lines into a deploy, with an S3 InternalError 500
// (NERD001 SPEC014).
//
// Only ActionRunning is at risk. A container that was created, recreated or
// started here resolved its binds in the act, so probing it would spend an exec
// to learn nothing -- and would be the one call that could produce a false
// positive.
func repairStaleFlociDataDir(
	ctx context.Context,
	opts *Options,
	runner *compose.Runner,
	project *compose.Project,
	results []compose.Result,
	reg *registry.Registry,
	token string,
) ([]compose.Result, error) {
	i, recreate, err := flociNeedsRecreate(results, token, func() (bool, error) {
		return floci.SentinelMatches(ctx, container.New(), floci.ContainerName, token)
	})
	if err != nil {
		opts.warn("could not check whether %s is still reading %s: %v",
			floci.ContainerName, reg.ControlPlane.FlociDataDir, err)
	}
	if !recreate {
		return results, nil
	}

	opts.warn("%s is running against a data directory that is no longer %s.\n%s\n  %s",
		floci.ContainerName, reg.ControlPlane.FlociDataDir, wipedHomeDetail(reg), floci.DoNotDeleteHome)
	opts.step("Recreating %s so it reads this home's state...", floci.ContainerName)

	res, err := runner.Recreate(ctx, project, compose.FlociService)
	if err != nil {
		return results, nserr.Wrap(nserr.Fail, fmt.Errorf("recreating %s: %w", floci.ContainerName, err))
	}
	results[i] = res
	opts.step("  %s %s", res.Name, res.Action)
	return results, nil
}

// wipedHomeDetail says which of the two shapes this is, because the
// consequences differ and only one of them is self-repairing.
//
// A synthesized registry means registry.json went too, so Bootstrapped is false
// and the bootstrap below rebuilds everything the recreated Floci has lost.
// With the registry intact, only the data directory went: Bootstrapped stays
// true, the bootstrap is skipped, and the platform is left half-built. nsctl
// does not clear that flag on its own -- a false positive there would destroy a
// working platform's state -- so it names the repair instead.
func wipedHomeDetail(reg *registry.Registry) string {
	if reg.Synthesized {
		return "  This home's registry is gone as well, so it is being bootstrapped from scratch."
	}
	return "  This home's registry survived, so nsctl still believes the control plane is bootstrapped " +
		"while the recreated Floci holds none of its state. Run `nsctl control-plane reset` to rebuild it."
}

// flociNeedsRecreate is the decision, separated from the doing so it can be
// tested against a table rather than a daemon.
//
// verify is called only when there is something worth asking: Floci is in the
// results and was left exactly as it was found. It returns the index so the
// caller can replace the result it reports to the user.
func flociNeedsRecreate(results []compose.Result, token string, verify func() (bool, error)) (int, bool, error) {
	if token == "" {
		return -1, false, nil
	}
	i := -1
	for n, res := range results {
		if res.Service == compose.FlociService {
			i = n
		}
	}
	if i < 0 || results[i].Action != compose.ActionRunning {
		return i, false, nil
	}
	// Conservative: only a read that succeeded and disagreed is acted on.
	// Recreating a Floci that was working costs the databases it spawned, so a
	// probe that could not reach an answer changes nothing.
	ok, err := verify()
	if err != nil {
		return i, false, err
	}
	return i, !ok, nil
}
