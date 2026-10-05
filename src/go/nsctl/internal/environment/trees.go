package environment

import (
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/artifact"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bom"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/reconcile"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/repoclass"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/runner"
)

// workingTreeDigests digests the developer's tree each entry will deploy from,
// keyed by instance name (NERD034 SPEC002).
//
// The tree is the one the runner will mount, chosen by the same
// runner.Config.SourceTree, so the plan and the deploy cannot disagree about
// which directory an edit has to be in. A shared tree -- bundled, or an
// unpacked artifact -- is versioned and gets none. A tree that cannot be read
// is warned about and gets none, which reads as "no information", never as
// drift.
func workingTreeDigests(opts *Options, repoPaths map[string]string, entries []bom.Entry) map[string]string {
	cfg := runner.Config{
		RepoHome:  opts.lookup("HMD_REPO_HOME"),
		Home:      opts.Home,
		Lookup:    opts.Lookup,
		RepoPaths: repoPaths,
	}
	byDir := map[string]string{}
	out := map[string]string{}
	for _, e := range entries {
		dir, shared := cfg.SourceTree(e.RepoClassName)
		if dir == "" || shared {
			continue
		}
		digest, seen := byDir[dir]
		if !seen {
			var err error
			if digest, err = reconcile.TreeDigest(dir); err != nil {
				opts.warn("%v; edits to it will not be noticed until --force-full-redeploy", err)
			}
			byDir[dir] = digest
		}
		if digest != "" {
			out[e.RepoInstanceName] = digest
		}
	}
	return out
}

// deployRepoPaths is the per-class source override Apply hands the runner:
// each declared checkout, then each cached artifact. For a caller -- the
// plan -- that needs the same answer without Apply's artifact checks.
func deployRepoPaths(opts *Options, repos []manifest.Repo) map[string]string {
	paths := repoclass.Paths(repos, opts.Lookup)
	for class, path := range artifact.Paths(opts.Home, repos) {
		if paths == nil {
			paths = map[string]string{}
		}
		paths[class] = path
	}
	return paths
}
