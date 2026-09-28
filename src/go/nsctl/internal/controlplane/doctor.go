package controlplane

import (
	"context"
	"fmt"
	"net"
	"runtime"
	"strconv"

	"github.com/docker/docker/client"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/container"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/dnsd"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/dockerhost"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/doctor"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
)

// DoctorOptions assembles the checks from an Options, so the start preflight
// and `nsctl doctor` run the same suite against the same seams.
func DoctorOptions(opts *Options) doctor.Options {
	d := container.New()
	return doctor.Options{
		Lookup:      opts.Lookup,
		Home:        opts.Home,
		GOOS:        runtime.GOOS,
		Docker:      d,
		Resolver:    &dockerhost.Resolver{Inspect: dockerhost.CLIInspector(d), Lookup: opts.Lookup},
		Connect:     ConnectEngine,
		CLIEndpoint: d.ContextEndpoint,
		Hosts:       func() error { return CheckHostsEntries(nil) },
		Suffix:      func() error { return CheckLocalSuffix(context.Background(), opts) },
		Images:      func(ctx context.Context) []doctor.Check { return ImageChecks(ctx, opts) },
		Storage:     func(ctx context.Context) []doctor.Check { return StorageChecks(ctx, opts) },
		Bridge:      BridgeChecks,
	}
}

// CheckLocalSuffix reports whether the wildcard local suffix resolves on this
// machine, and when it does not, which of the two failures it is.
//
// They need opposite fixes: the resolver is not running (nothing answers on its
// port), or the machine is not pointed at it (it answers, and the system
// resolver still does not). A single message covering both would tell a user
// whose control plane is down to edit a resolver file that was already correct.
//
// The name probed has deliberately never been deployed: resolving it proves the
// wildcard, which is the property /etc/hosts cannot have and therefore the one
// worth asserting (NERD026 SPEC003).
func CheckLocalSuffix(ctx context.Context, opts *Options) error {
	suffix := dnsd.DefaultSuffix
	probe := dnsd.ProbeName(suffix)

	if ips, err := net.LookupIP(probe); err == nil && len(ips) > 0 {
		return nil
	}

	// Best effort: a home that cannot be read still reports the default port,
	// which is the right thing to name in the failure.
	var reg *registry.Registry
	if opts.Home != "" {
		if loaded, err := registry.Load(opts.Home, opts.Lookup); err == nil {
			reg = loaded
		}
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(DNSPort(opts, reg)))
	if err := dnsd.Answers(ctx, addr, probe); err != nil {
		// "Nothing answers" has a third cause besides a stopped resolver and a
		// misconfigured machine, and it is invisible from here: an engine whose
		// port forwarder carries no UDP. Saying "the resolver is not running"
		// to someone whose resolver is running, and answering, sends them to
		// restart a control plane that was never the problem.
		if why := ColimaUDPUnreachable(currentEndpointHost(ctx, opts)); why != "" {
			return fmt.Errorf("%s does not resolve, and nothing answers on %s either.\n  %s", probe, addr, why)
		}
		return fmt.Errorf("%s does not resolve, and nothing answers on %s either -- the resolver is not running", probe, addr)
	}
	return fmt.Errorf("%s does not resolve, though the resolver on %s answers for it -- this machine is not pointed at it", probe, addr)
}

// currentEndpointHost is the engine endpoint nsctl would use, or "" when it
// cannot be resolved. Best effort by design: it only ever refines a message.
func currentEndpointHost(ctx context.Context, opts *Options) string {
	d := container.New()
	// A failed resolve still reports the endpoint it fell back to, which is the
	// one the work would use, so the error adds nothing here.
	ep, _ := (&dockerhost.Resolver{Inspect: dockerhost.CLIInspector(d), Lookup: opts.Lookup}).Resolve(ctx)
	return ep.Host
}

func doctorOptions(opts *Options) doctor.Options {
	o := DoctorOptions(opts)
	// The start reports unresolvable host names itself, as a notice tied to the
	// names it actually had to redirect (see Start), so running the check here
	// too would say the same thing twice. Belt and braces either way: Gate runs
	// neither of these rows -- they belong to the diagnostic, not the preflight,
	// because neither is a reason to refuse a start (NERD025 SPEC004).
	o.Hosts = nil
	o.Suffix = nil
	// Gate does not run these either, but say so here rather than depend on
	// that: both reach the network, and a start that refused because a machine
	// is offline -- or because the store it is about to create does not exist
	// yet -- would be wrong about what it measured.
	o.Images = nil
	o.Storage = nil
	// Bridge is deliberately kept. It reaches no network and asks Floci
	// nothing -- it reads one file in a throwaway container on the endpoint
	// Gate just proved -- so neither reason above applies, and a start heading
	// for a cluster that cannot route to its own Services is exactly when it is
	// worth saying so (NERD028 SPEC004).
	return o
}

// ConnectEngine reaches the engine at a resolved endpoint and asks what it is.
func ConnectEngine(ctx context.Context, ep dockerhost.Endpoint) (dockerhost.Daemon, error) {
	opts, err := ep.ClientOpts(nil)
	if err != nil {
		return dockerhost.Daemon{}, err
	}
	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return dockerhost.Daemon{}, err
	}
	defer cli.Close()
	return dockerhost.Probe(ctx, cli)
}
