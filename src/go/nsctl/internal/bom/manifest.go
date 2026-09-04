package bom

import (
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
)

// Declared turns an environment manifest's repo declarations into BOM entries.
//
// This is the whole of what a user adds. It sits on top of Substrate and is
// deployed by the same changeset, so a declared instance can depend on the
// substrate by name -- `eks-cluster`, `environment-db` -- exactly as the
// substrate entries depend on each other.
//
// Nothing is auto-wired. A declaration's dependencies are what the manifest
// says and no more, matching change_set_builder.entry_from_declaration: a
// dependency nsctl invented would be one the user cannot see or remove.
func Declared(env Environment, repos []manifest.Repo) []Entry {
	entries := make([]Entry, 0, len(repos))
	for _, r := range repos {
		config := r.InstanceConfiguration
		if config == nil {
			config = map[string]any{}
		}
		deps := r.Dependencies
		if deps == nil {
			deps = map[string]any{}
		}
		entries = append(entries, Entry{
			RepoInstanceName:      r.InstanceName,
			RepoClassName:         r.RepoClassName,
			RepoClassVersion:      r.Version,
			DeploymentID:          env.DeploymentID,
			InstanceConfiguration: config,
			Dependencies:          deps,
		})
	}
	return entries
}

// RepoPaths maps a repo class to the working tree it deploys from, for the
// declarations whose path is not the $HMD_REPO_HOME convention.
//
// Only the ones that differ: the runner and the version resolver find the rest
// on their own, and a smaller map keeps the override explicit about what it is
// actually overriding.
func RepoPaths(repos []manifest.Repo, lookup manifest.Lookup) map[string]string {
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
