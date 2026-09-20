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

// ImageChecker reports whether an image reference can be resolved, and fetches
// one that cannot.
type ImageChecker interface {
	ImagePresent(ctx context.Context, ref string) bool
	PullImage(ctx context.Context, ref string) error
	ContainerEnv(ctx context.Context, name string) map[string]string
}

// EnsureBackendImages makes sure the images Floci is configured to spawn its
// database and graph backends from are in the local cache, pulling any that are
// not.
//
// This used to be a check that refused. The refusal was right about the failure
// it prevents -- a reference that resolves nowhere leaves the instance in state
// "failed" while Terraform polls "Still creating..." until someone kills it --
// and wrong about the remedy, because "not cached" is not the same as "does not
// exist". On the machine this proposal is about, where the only prerequisite is
// Docker, *nothing* is cached, so the guard turned every genuinely fresh start
// into a hard stop at the control-plane database. It did exactly that on the
// first cold start run with no HMD_REPO_HOME.
//
// EnsureProjectBuilder already pulls the one image nsctl names elsewhere, so an
// absent image was always something nsctl knew how to solve. What survives is
// the refusal for the case that is genuinely unactionable: a pull that fails,
// which is the wrong-registry and no-such-tag case the message was written for.
//
// Returned rather than raised so the caller decides; both callers stop.
func EnsureBackendImages(ctx context.Context, d ImageChecker, container string, progress func(string, ...any)) []string {
	env := d.ContainerEnv(ctx, container)
	var problems []string
	for _, key := range []string{rdsImageEnv, neptuneImageEnv} {
		ref := env[key]
		if ref == "" || d.ImagePresent(ctx, ref) {
			continue
		}
		if progress != nil {
			progress("  pulling %s, which Floci spawns its backend from...", ref)
		}
		if err := d.PullImage(ctx, ref); err != nil {
			problems = append(problems, fmt.Sprintf(
				"%s is set to %s, which is not in the local image cache and could not be pulled:\n  %v\n"+
					"Floci spawns its backend from that reference and leaves the instance in state \"failed\" when it cannot resolve it, while the deploy polls \"Still creating...\" indefinitely.\n"+
					"  Pin a version that exists -- for the database that is HMD_POSTGRES_BASE_VERSION -- or check HMD_LOCAL_NS_CONTAINER_REGISTRY.",
				key, ref, err))
		}
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
