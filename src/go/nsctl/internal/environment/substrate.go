package environment

import (
	"fmt"
	"sort"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bom"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// startPlan is which infrastructure steps a substrate mode runs (NERD014
// SPEC001). It is the one part of Start that can be tested without Docker,
// which is why it is a value rather than a chain of ifs inside Start.
type startPlan struct {
	// Database and Graph start the containers Floci spawned during an earlier
	// deploy; DBAccount deploys the per-environment service that needs the
	// first; Cluster is the whole k3s block and its post-apply recovery;
	// CoreResources are the concrete Resources describing the core, which
	// need a core deployment to attach to.
	Database, Graph, DBAccount, Cluster, CoreResources bool
}

func planFor(mode manifest.Substrate) startPlan {
	switch mode {
	case manifest.SubstrateNone:
		return startPlan{}
	case manifest.SubstrateCore:
		return startPlan{Database: true, Graph: true, DBAccount: true, CoreResources: true}
	default:
		return startPlan{Database: true, Graph: true, DBAccount: true, Cluster: true, CoreResources: true}
	}
}

// substrateMode is the environment's recorded mode (NERD014 SPEC002).
//
// An unreadable manifest is reported and read as full, never as none: the
// worse failure of the two is skipping a database something depends on.
func substrateMode(opts *Options, slug string) manifest.Substrate {
	mode, err := manifest.LoadSubstrate(opts.Home, slug, opts.Lookup)
	if err != nil {
		opts.warn("%v; running the full substrate", err)
		return manifest.SubstrateFull
	}
	return mode
}

// refuseCoreBindings is NERD014 SPEC008: under none there is no core instance,
// so a dependency bound to it -- the stub NERD013 supplies for a required
// name-only role -- fails the changeset later with "No repo instance". Say now
// what the deploy would say in ten minutes, and name the fix.
func refuseCoreBindings(mode manifest.Substrate, m *manifest.Manifest) error {
	if mode != manifest.SubstrateNone || m == nil {
		return nil
	}
	var bound []string
	for _, r := range m.Repos {
		roles := make([]string, 0, len(r.Dependencies))
		for role := range r.Dependencies {
			roles = append(roles, role)
		}
		sort.Strings(roles)
		for _, role := range roles {
			if dependsOnCore(r.Dependencies[role]) {
				bound = append(bound, fmt.Sprintf("%s.%s", r.InstanceName, role))
			}
		}
	}
	if len(bound) == 0 {
		return nil
	}
	return nserr.New(nserr.Usage,
		"substrate none has no %s instance, but %s binds to it: %s.\n  Use `nsctl env start --substrate core`, or remove the binding.",
		bom.CoreInstanceName, plural(len(bound), "dependency", "dependencies"), strings.Join(bound, ", "))
}

func dependsOnCore(target any) bool {
	switch t := target.(type) {
	case string:
		return t == bom.CoreInstanceName
	case []any:
		for _, v := range t {
			if s, ok := v.(string); ok && s == bom.CoreInstanceName {
				return true
			}
		}
	}
	return false
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
