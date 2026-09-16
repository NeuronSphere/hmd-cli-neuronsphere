package cmd

import (
	"context"
	"sync"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/artifact"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/librarian"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/repoclass"
)

// roleResolver answers what a repo class declares about its dependency roles,
// out of the artifact holding that exact version's manifest.
//
// It is the injected roleFacts the closure calls, which is what keeps
// selectionFlags.apply a function over data: the traversal decides, and the
// side effect of going and getting the manifest lives here.
//
// NERD013 SPEC005. A class's roles cannot be read until its artifact is here,
// so the fetch is interleaved with the traversal rather than following it. The
// invariant that made NERD012's order load-bearing is untouched -- nothing is
// declared until its artifact is in the control plane -- and this run fetches
// strictly less than before, because it expands strictly fewer roles.
type roleResolver struct {
	ctx  context.Context
	home string
	// cloud and local are nil when this resolver may not fetch, which is what
	// `bom show` without --resolve and `bom import --dry-run` use: they answer
	// from whatever is already unpacked here and report the rest as unread.
	cloud *librarian.Client
	local *librarian.Client

	mu    sync.Mutex
	cache map[string]map[string]repoclass.RoleFact
	// known records the answer for a class-version whose roles could not be
	// read, so a second ask does not mean a second failed fetch.
	known map[string]bool
}

func newRoleResolver(ctx context.Context, home string, cloud, local *librarian.Client) *roleResolver {
	return &roleResolver{
		ctx: ctx, home: home, cloud: cloud, local: local,
		cache: map[string]map[string]repoclass.RoleFact{},
		known: map[string]bool{},
	}
}

// facts satisfies roleFacts.
//
// A failure to fetch is reported as "unknown" rather than as an error. The
// closure answers unknown by following every role, which is the conservative
// direction, and the fetch is attempted again by fetchSelection -- which is
// where a failure becomes a message and a non-zero exit, so that a broken
// artifact is reported once rather than twice in two different vocabularies.
func (r *roleResolver) facts(repoClass, version string) (map[string]repoclass.RoleFact, bool) {
	if repoClass == "" || version == "" {
		return nil, false
	}
	key := repoClass + "@" + version

	r.mu.Lock()
	defer r.mu.Unlock()
	if answered, ok := r.known[key]; ok {
		return r.cache[key], answered
	}

	if !artifact.Cached(r.home, repoClass, version) && r.cloud != nil && r.local != nil {
		spec := librarian.Spec{Name: repoClass, Version: version, ItemType: manifest.DefaultArtifactType}
		// Errors are deliberately dropped; see the doc comment.
		_ = pullArtifact(r.ctx, r.cloud, r.local, r.home, spec)
	}

	roles, err := repoclass.RolesInDir(artifact.Dir(r.home, repoClass, version))
	if err != nil || roles == nil {
		r.known[key] = false
		return nil, false
	}
	r.cache[key] = roles
	r.known[key] = true
	return roles, true
}
