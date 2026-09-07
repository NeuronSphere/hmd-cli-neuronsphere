package compose

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Owner is a container this project would take over, and who has it now.
type Owner struct {
	Container string
	// Project is the compose project the container is labelled with.
	Project string
	// Home is the HMD_HOME that project's containers were started for, read
	// off the container's own environment. Empty when it does not say.
	Home string
}

func (o Owner) String() string {
	if o.Home != "" {
		return fmt.Sprintf("%s (project %s, HMD_HOME %s)", o.Container, o.Project, o.Home)
	}
	return fmt.Sprintf("%s (project %s)", o.Container, o.Project)
}

// CheckOwnership reports the project's containers that already exist and belong
// to a different compose project.
//
// The control plane's four services pin container_name, so hmd_proxy, floci,
// hmd_deployment_gui and hmd_nsrunner are global names -- while the network,
// the compose project and the Floci data directory are all namespaced by an
// HMD_HOME hash. Two HMD_HOMEs therefore get distinct everything *except* the
// containers, and upService inspects by name and consults only the config hash:
// a second HMD_HOME finds the first's containers, sees a hash that cannot match
// (the bind paths differ), force-removes them and recreates them pointed at
// itself. Killing that start then leaves the first with no proxy at all.
//
// SPEC014 recorded the collision and noted that nothing detected it. This is
// the detection. It does not fix the collision -- see the SPEC.
//
// A container that cannot be inspected is *unknown*, not a conflict. This
// decides whether to refuse, so a daemon hiccup must not invent an owner; the
// same doctrine the container package's ImageOf and InspectState state.
func CheckOwnership(ctx context.Context, api dockerAPI, p *Project, active map[string]bool) []Owner {
	if p == nil {
		return nil
	}
	var owners []Owner
	for _, s := range p.Services {
		if !s.EnabledBy(active) {
			continue
		}
		name := s.Name(p.Name)
		existing, err := api.ContainerInspect(ctx, name)
		if err != nil || existing.Config == nil {
			continue
		}
		owner := existing.Config.Labels[LabelProject]
		// No label at all is a container this project has never managed --
		// a hand-started one, or one from before the labels existed. Not
		// something to claim ownership of by silence, but not another
		// HMD_HOME's either, and upService's hash comparison already handles
		// it the way it always has.
		if owner == "" || owner == p.Name {
			continue
		}
		owners = append(owners, Owner{Container: name, Project: owner, Home: envValue(existing.Config.Env, "HMD_HOME")})
	}
	sort.Slice(owners, func(i, j int) bool { return owners[i].Container < owners[j].Container })
	return owners
}

// envValue reads one variable out of a container's KEY=VALUE environment.
func envValue(env []string, key string) string {
	for _, kv := range env {
		if name, value, ok := strings.Cut(kv, "="); ok && name == key {
			return value
		}
	}
	return ""
}

// JoinOwners renders owners one per line for an error message.
func JoinOwners(owners []Owner) string {
	lines := make([]string, 0, len(owners))
	for _, o := range owners {
		lines = append(lines, o.String())
	}
	return strings.Join(lines, "\n  ")
}
