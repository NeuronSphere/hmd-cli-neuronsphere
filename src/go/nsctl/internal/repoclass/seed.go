package repoclass

import "github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"

// Paths maps a repo class to the working tree a manifest asked it to deploy
// from, for the declarations whose path is not the $HMD_REPO_HOME convention.
//
// Only the ones that differ. Paths is taken without a stat, because it means
// "the manifest asked for this tree" -- so seeding it with the convention would
// hand back a directory that may not exist, while the resolver and the runner
// both find a conventional checkout on their own and after stat'ing it.
//
// The comparison is against the path the convention *produces*, not against
// whether source.path was written at all: a manifest that spells the convention
// out is still naming the convention, and treating that as an override is the
// difference this function exists to erase. Two call sites used to disagree
// about exactly this -- the environment path compared the resulting paths and
// the control-plane path asked only whether source.path was set -- so a
// control-plane manifest naming $HMD_REPO_HOME/<class> outright got a tier-two,
// unstat'd answer where an environment manifest got a tier-six one. The
// stricter rule is the one kept.
func Paths(repos []manifest.Repo, lookup Lookup) map[string]string {
	paths := map[string]string{}
	for _, r := range repos {
		path := r.RepoPath(lookup)
		if path == "" || r.RepoClassName == "" {
			continue
		}
		if conventional := (manifest.Repo{RepoClassName: r.RepoClassName}).RepoPath(lookup); conventional == path {
			continue
		}
		paths[r.RepoClassName] = path
	}
	return paths
}

// Seed fills a resolver from what a manifest declares: the working trees it
// named outright, and the versioned artifacts it asked for.
//
// It exists for one line that has to be right in every caller and is invisible
// when it is wrong: **an artifact instance goes into Artifacts, never into
// Paths.** An entry in Paths is tier two and is taken without a stat, while an
// artifact is tier three and resolves through the cache under Home. Folding them
// together would report an artifact's version with Source working-tree -- a
// deploy describing itself as something it is not, which is precisely what
// NERD005 SPEC002's ordering exists to prevent.
//
// The version is recorded whether or not its bytes are cached. An artifact is
// addressed *by* version, so the version is known from the manifest either way,
// and whether this machine holds it is a separate question asked before a deploy
// starts (internal/environment.checkArtifacts) or reported in a listing.
func Seed(r *Resolver, repos []manifest.Repo) {
	if r == nil {
		return
	}
	if r.Paths == nil {
		r.Paths = map[string]string{}
	}
	if r.Artifacts == nil {
		r.Artifacts = map[string]string{}
	}
	lookup := r.Lookup
	if lookup == nil {
		// A Resolver built as a literal rather than through New. os.Expand
		// panics on a nil function, and a resolver with no environment is a
		// legitimate thing to build.
		lookup = func(string) string { return "" }
	}
	for class, path := range Paths(repos, lookup) {
		r.Paths[class] = path
	}
	for _, repo := range repos {
		if repo.SourceType() == manifest.SourceArtifact && repo.RepoClassName != "" {
			r.Artifacts[repo.RepoClassName] = repo.Version
		}
	}
}
