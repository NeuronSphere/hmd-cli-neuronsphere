package dockerhost

import (
	"context"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/container"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/hmdenv"
)

// CLIInspector asks the docker CLI which endpoint it resolves.
//
// One exec, measured at roughly 13ms -- the same order as the `docker version`
// call Available already makes -- and it implements the whole precedence chain
// by construction rather than by re-derivation. See NERD021 SPEC002 for why
// this is not a parse of ~/.docker/contexts.
func CLIInspector(d *container.Docker) Inspector {
	if d == nil {
		d = container.New()
	}
	return func(ctx context.Context) (string, error) {
		return d.ContextEndpoint(ctx)
	}
}

// NewResolver returns a Resolver backed by the real docker CLI.
func NewResolver(lookup hmdenv.Lookup) *Resolver {
	return &Resolver{Inspect: CLIInspector(nil), Lookup: lookup}
}
