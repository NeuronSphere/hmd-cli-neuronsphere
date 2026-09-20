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
