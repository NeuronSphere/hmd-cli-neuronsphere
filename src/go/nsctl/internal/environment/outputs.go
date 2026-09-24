package environment

import (
	"path/filepath"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
)

// ResourceOutputDir is where an environment's deploy records the resource
// outputs its nodes produced, one file per instance.
//
// Beside the logs, under the environment's own state directory, so a purge takes
// it with the environment and two environments never read each other's. The
// copy exists because `nsctl env credentials` resolves an access declaration
// that names an output key, and requiring the control plane to answer for that
// would make a read of local state depend on a running API (NERD023 SPEC004).
func ResourceOutputDir(env *registry.Environment) string {
	if env == nil {
		return ""
	}
	return filepath.Join(env.StatePath(), "resources_output")
}
