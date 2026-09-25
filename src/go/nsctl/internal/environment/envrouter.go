package environment

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/compose"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/container"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/router"
)

// routerImage is what an environment's router runs. The same nginx the control
// plane's proxy uses, so nothing new is pulled.
const routerImage = "nginx:stable-alpine"

// routerDocker is the engine surface the router lifecycle needs, narrowed so a
// test needs no daemon.
type routerDocker interface {
	Exists(ctx context.Context, name string) bool
	Running(ctx context.Context, name string) (bool, error)
	PublishedPorts(ctx context.Context, name string) []int
	RemoveContainer(ctx context.Context, name string) error
	Run(ctx context.Context, args ...string) (stdout, stderr []byte, err error)
}

// desiredRouterPorts is the set of host ports an environment's router publishes:
// exactly the ones that environment uses, and no others.
//
// This is the whole of NERD027 in one function. The scheme it replaces reserved
// four ports per environment for sixteen environments and published all eighty
// up front, of which 47 could never carry a listener at all -- FlociPort has
// none by design, GraphPort and SparePort are called by nothing.
//
// The slot arithmetic survives as the address (SPEC003): the same environment
// name lands on the same ports across machines and re-creations. What stops is
// binding them before anything answers there.
func desiredRouterPorts(env *registry.Environment, cluster, trino bool) []int {
	if !cluster {
		// No cluster, nothing to reach: not even a container.
		return nil
	}
	ports := []int{env.K3sPort()}
	if trino {
		ports = append(ports, env.TrinoPort())
		// The default environment additionally keeps the historical port, so
		// integration suites that name it need no change -- and only when a
		// coordinator exists, because advertising a port nothing answers on is
		// the defect NERD023 SPEC003 corrected.
		if env.IsDefault() && env.TrinoPort() != router.LegacyTrinoHostPort {
			ports = append(ports, router.LegacyTrinoHostPort)
		}
	}
	sort.Ints(ports)
	return ports
}

// ensureEnvRouter creates, recreates or removes an environment's router so that
// it publishes exactly desiredRouterPorts.
//
// Recreated only when the set actually changes. An engine cannot add a published
// port to a running container, so a change means replacing it -- which
// interrupts a kubectl session or a Trino client against *this* environment and
// nothing else. That is the whole reason the dynamic ports moved off hmd_proxy:
// recreating the proxy would cut the deploy that asked for the port, because
// deploying hmd-inf-trino is exactly what makes a coordinator exist.
func ensureEnvRouter(ctx context.Context, d routerDocker, home, network, project string,
	env *registry.Environment, cluster, trino bool) error {

	name := env.Router()
	want := desiredRouterPorts(env, cluster, trino)
	exists := d.Exists(ctx, name)

	if len(want) == 0 {
		// Nothing to publish. A router left behind would hold ports the next
		// start routes around, which is a confusing way to lose a port.
		if exists {
			return d.RemoveContainer(ctx, name)
		}
		return nil
	}

	if exists {
		running, _ := d.Running(ctx, name)
		if running && samePorts(d.PublishedPorts(ctx, name), want) {
			return nil
		}
		if err := d.RemoveContainer(ctx, name); err != nil {
			return err
		}
	}

	args := []string{"run", "-d", "--name", name, "--restart", "unless-stopped"}
	if network != "" {
		args = append(args, "--network", network)
	}
	if project != "" {
		// The label OurPorts filters on. Without it the next start probes these
		// listeners, finds them busy and moves the ports out from under the
		// environment that is using them (NERD027 SPEC004).
		args = append(args, "--label", compose.LabelProject+"="+project)
	}
	for _, p := range want {
		args = append(args, "-p", fmt.Sprintf("%d:%d", p, p))
	}
	cache := router.NewEnv(home, env.Slug, nil).CacheDir()
	args = append(args,
		"-v", filepath.Join(cache, "neuronsphere.conf")+":/etc/nginx/nginx.conf:ro",
		"-v", cache+":"+router.NSDir+":ro",
		routerImage,
	)
	if _, stderr, err := d.Run(ctx, args...); err != nil {
		return fmt.Errorf("creating %s on %s: %w: %s", name, portList(want), err, stderr)
	}
	return nil
}

func samePorts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	x := append([]int(nil), a...)
	y := append([]int(nil), b...)
	sort.Ints(x)
	sort.Ints(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

func portList(ports []int) string {
	out := ""
	for i, p := range ports {
		if i > 0 {
			out += ", "
		}
		out += strconv.Itoa(p)
	}
	return out
}

// syncEnvRouter brings an environment's router in line with what it now serves,
// writing its config shell first so the container has something valid to start
// on.
func syncEnvRouter(ctx context.Context, opts *Options, d *container.Docker,
	env *registry.Environment, network string, cluster, trino bool) error {

	r := router.NewEnv(opts.Home, env.Slug, opts.Lookup)
	if err := r.WriteBaseConfig(); err != nil {
		return err
	}
	// A platform upgraded from before this existed still has the environment's
	// stream fragment in the control plane's root, where it now names ports
	// hmd_proxy no longer publishes. Harmless but stale, and stale config is how
	// a later reader concludes the wrong thing.
	if err := router.New(opts.Home, opts.Lookup).RemoveEnvStreams(env.Slug); err != nil {
		opts.warn("%v", err)
	}
	return ensureEnvRouter(ctx, d, opts.Home, network, env.ComposeProject, env, cluster, trino)
}
