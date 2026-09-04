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
)

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
