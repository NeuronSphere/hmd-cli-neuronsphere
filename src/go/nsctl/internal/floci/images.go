package floci

import (
	"context"
	"fmt"
	"strings"
)

// Floci resolves the image for a database or graph backend from its own
// configuration and pins it into the instance record at creation. A pin that
// does not resolve is not reported as a configuration error: Floci answers
// CreateDBInstance with a 404 from the Docker daemon, leaves the instance in
// state `failed`, and Terraform sits polling "Still creating..." until someone
// kills it.
const (
	rdsImageEnv     = "FLOCI_SERVICES_RDS_DEFAULT_POSTGRES_IMAGE"
	neptuneImageEnv = "FLOCI_SERVICES_NEPTUNE_DEFAULT_IMAGE"
	eksImageEnv     = "FLOCI_SERVICES_EKS_DEFAULT_IMAGE"
)

// ContainerEnvReader reads the environment a container was created with.
type ContainerEnvReader interface {
	ContainerEnv(ctx context.Context, name string) map[string]string
}

// K3sWrapperImage is the image Floci is configured to spawn k3s clusters from,
// or "" when it cannot be read.
//
// Read off the running Floci container rather than reconstructed from the
// process environment. The pin lives in the compose file, behind two nested
// defaults, and a reconstruction that disagrees with what Floci actually got
// declares a healthy cluster stale and destroys it -- a scar the Python's
// configured_k3s_wrapper_image carries. What Floci was started with is the only
// answer that decides anything, so ask Floci.
func K3sWrapperImage(ctx context.Context, d ContainerEnvReader) string {
	return d.ContainerEnv(ctx, ContainerName)[eksImageEnv]
}

// ImageChecker reports whether an image reference can be resolved.
type ImageChecker interface {
	ImagePresent(ctx context.Context, ref string) bool
	ContainerEnv(ctx context.Context, name string) map[string]string
}

// CheckBackendImages verifies the images Floci is configured to spawn its
// database and graph backends from.
//
// Worth a pre-flight because the failure is otherwise silent and slow: the
// compose file's default version is the floating tag `stable`, and a registry
// override that has no `stable` produces a reference that exists nowhere. The
// deploy then hangs for as long as anyone lets it.
//
// The check is local only -- an image already pulled is proof enough, and a
// registry round trip would make a start depend on the network.
func CheckBackendImages(ctx context.Context, d ImageChecker, container string) []string {
	env := d.ContainerEnv(ctx, container)
	var problems []string
	for _, key := range []string{rdsImageEnv, neptuneImageEnv} {
		ref := env[key]
		if ref == "" || d.ImagePresent(ctx, ref) {
			continue
		}
		problems = append(problems, fmt.Sprintf(
			"%s is set to %s, which is not in the local image cache. Floci spawns its backend from that reference and leaves the instance in state \"failed\" when it cannot resolve it, while the deploy polls \"Still creating...\" indefinitely.\n  Pull it, or pin a version that exists -- for the database that is HMD_POSTGRES_BASE_VERSION.",
			key, ref))
	}
	return problems
}

// ContainerEnvFromInspect parses `docker inspect -f {{range .Config.Env}}...`
// output into a map.
func ContainerEnvFromInspect(out string) map[string]string {
	env := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), "=")
		if found && key != "" {
			env[key] = value
		}
	}
	return env
}
