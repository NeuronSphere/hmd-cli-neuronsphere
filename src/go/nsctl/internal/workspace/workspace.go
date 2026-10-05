// Package workspace decides what of the developer's machine a local cluster's
// node can see.
//
// Floci honours host volumes only on CreateCluster (the floci:host-volume
// creation tags) and refuses to change them afterwards, so the node gets one
// mount of the whole projects root, opted into per environment. Which repos a
// workload actually sees is the chart's business: it hostPath-mounts only the
// repos its instance lists, beneath NodePath.
package workspace

import (
	"os"
	"path/filepath"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
)

// NodePath is where the projects root appears inside the k3s node.
const NodePath = "/hmd/workspace"

// ProjectsPath is the host directory the node mounts, by the rule `hmd
// neuronsphere up` used for its Jupyter container: $HMD_HOME/studio/projects
// when it exists, else HMD_REPO_HOME. Empty when neither is an absolute
// directory, since a relative or blank allowlist root would canonicalize
// against Floci's own working directory.
func ProjectsPath(home string, lookup func(string) string) string {
	if home != "" {
		studio := filepath.Join(home, "studio", "projects")
		if info, err := os.Stat(studio); err == nil && info.IsDir() {
			return studio
		}
	}
	if repoHome := lookup("HMD_REPO_HOME"); filepath.IsAbs(repoHome) {
		return filepath.Clean(repoHome)
	}
	return ""
}

// HostVolumes are the Floci host-volume specs an environment's cluster is
// created with: the projects root, read-write, when the manifest opts in and
// there is a projects root to mount. Nil otherwise.
func HostVolumes(m *manifest.Manifest, home string, lookup func(string) string) []string {
	if m == nil || !m.HostWorkspace {
		return nil
	}
	projects := ProjectsPath(home, lookup)
	if projects == "" {
		return nil
	}
	return []string{projects + ":" + NodePath}
}
