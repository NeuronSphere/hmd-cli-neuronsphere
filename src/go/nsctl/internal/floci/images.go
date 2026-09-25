package floci

import (
	"context"
	"fmt"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/hmdenv"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/repoclass"
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

// RegistryEnv is the variable that decides which org every image Floci spawns
// a backend from is pulled out of. It is the one setting on this path a user is
// likely to have inherited from an older install.
const RegistryEnv = "HMD_LOCAL_NS_CONTAINER_REGISTRY"

// versionEnv names the variable that pins each backend's tag, so a failure can
// say which one to change rather than making the reader find it.
var versionEnv = map[string]string{
	rdsImageEnv:     "HMD_POSTGRES_BASE_VERSION",
	neptuneImageEnv: "HMD_IMG_GREMLIN_SERVER_VERSION",
	eksImageEnv:     "HMD_LOCAL_K3S_WRAPPER_IMAGE",
}

// RegistryHint is what a pull failure needs in order to explain itself: where
// the registry setting came from, and what nsctl would have used instead.
//
// The zero value is safe; the message loses the origin line and nothing else.
type RegistryHint struct {
	Home   string
	Lookup hmdenv.Lookup
}

// explain writes the pull failure the way someone who has never seen this
// platform can act on it.
//
// The message this replaced named RegistryEnv and stopped there. That left no
// way to tell whether the variable was set at all, still less where -- and a
// first-run user who read it concluded their HMD_HOME was bad and deleted it,
// which cannot clear a shell variable and cost them a Floci bound to a
// directory that no longer existed. Hence the value, the origin, and the last
// paragraph (NERD023 SPEC009).
func (h RegistryHint) explain(key, ref string, err error) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s is set to %s, which is not in the local image cache and could not be pulled:\n  %v\n", key, ref, err)
	b.WriteString("Floci spawns its backend from that reference and leaves the instance in state \"failed\" when it cannot resolve it, while the deploy polls \"Still creating...\" indefinitely.\n")

	value, origin := hmdenv.OriginOf(h.Home, h.Lookup, RegistryEnv)
	switch {
	case origin == hmdenv.OriginDefault:
		fmt.Fprintf(&b, "  %s is not set anywhere, so this came from nsctl's own default, %s -- which is where these images are published. The tag is what is wrong.\n",
			RegistryEnv, repoclass.PublishedRegistry)
		if v := versionEnv[key]; v != "" {
			fmt.Fprintf(&b, "  Pin one that exists with %s.\n", v)
		}
	case value == repoclass.PublishedRegistry:
		if v := versionEnv[key]; v != "" {
			fmt.Fprintf(&b, "  %s=%s (from %s) is where these images are published, so the tag is what is wrong. Pin one that exists with %s.\n",
				RegistryEnv, value, origin, v)
		}
	default:
		fmt.Fprintf(&b, "  %s=%s, set in %s. nsctl's own default is %s, which is where these images are built and published; %s carries only some of them. Unset the variable, or set it to %s.\n",
			RegistryEnv, value, origin, repoclass.PublishedRegistry, value, repoclass.PublishedRegistry)
		if origin == hmdenv.OriginShell {
			b.WriteString("  " + DoNotDeleteHome + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// DoNotDeleteHome is said wherever a start fails and the reader is likely to
// reach for the one recovery that makes things worse.
//
// Deleting HMD_HOME leaves every container running -- they are named globally
// and keyed on the home's *path* -- still bound to a directory that no longer
// exists, which surfaces later as an unattributable S3 500 inside `tofu init`
// (NERD001 SPEC014, NERD023 SPEC009).
const DoNotDeleteHome = "Do not delete $HMD_HOME to start over: the platform's containers keep running and keep writing to the directory you deleted. " +
	"`nsctl control-plane stop` stops them, and `nsctl env purge` discards an environment's data."

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
func EnsureBackendImages(ctx context.Context, d ImageChecker, container string, progress func(string, ...any), hint RegistryHint) []string {
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
			problems = append(problems, hint.explain(key, ref, err))
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
