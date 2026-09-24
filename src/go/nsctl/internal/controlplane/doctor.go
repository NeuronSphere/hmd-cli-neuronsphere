package controlplane

import (
	"context"
	"runtime"

	"github.com/docker/docker/client"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/container"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/dockerhost"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/doctor"
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
	}
}

func doctorOptions(opts *Options) doctor.Options {
	o := DoctorOptions(opts)
	// The start reports unresolvable host names itself, as a notice tied to the
	// names it actually had to redirect (see Start), so running the check here
	// too would say the same thing twice; Gate does not call this anyway.
	o.Hosts = nil
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
