package dockerhost

import (
	"context"
	"fmt"
	"strings"

	"github.com/docker/docker/api/types/system"
)

// Daemon is what the engine says about itself: whether it was reached at all,
// and whether it is big enough, from one call.
type Daemon struct {
	ServerVersion string
	// OSType is the daemon's operating system ("linux"), which is what makes
	// VMBacked answerable.
	OSType string
	// OperatingSystem is its self-description: "Docker Desktop", "Alpine Linux
	// v3.20" under Colima, "Ubuntu 24.04" on a Linux host.
	OperatingSystem string
	NCPU            int
	MemTotal        int64
	// Rootless is derived from SecurityOptions: v27.3.1's system.Info has no
	// field of its own for it.
	Rootless bool
}

// InfoAPI is the one-call surface Probe needs, narrowed so it fakes without a
// daemon -- compose's dockerAPI for the same reason.
type InfoAPI interface {
	Info(ctx context.Context) (system.Info, error)
}

// Probe asks the engine about itself.
func Probe(ctx context.Context, api InfoAPI) (Daemon, error) {
	info, err := api.Info(ctx)
	if err != nil {
		return Daemon{}, err
	}
	d := Daemon{
		ServerVersion:   info.ServerVersion,
		OSType:          info.OSType,
		OperatingSystem: info.OperatingSystem,
		NCPU:            info.NCPU,
		MemTotal:        info.MemTotal,
	}
	for _, opt := range info.SecurityOptions {
		if strings.Contains(opt, "name=rootless") {
			d.Rootless = true
		}
	}
	return d, nil
}

// VMBacked reports whether the daemon runs in a virtual machine, which is what
// makes a bind-mount source a question rather than a path.
//
// The test is the host's GOOS against the daemon's OSType, never a runtime
// name. Docker Desktop, Colima, Rancher Desktop and a remote Linux engine are
// all a Linux daemon under a macOS or Windows CLI, and they all share the rule
// that a host path is visible only if it was shared into the VM (NERD021
// SPEC007).
func (d Daemon) VMBacked(goos string) bool {
	if d.OSType == "" {
		return false
	}
	return !strings.EqualFold(d.OSType, goos)
}

// DescribeCapacity renders the engine's size for a warning.
func (d Daemon) DescribeCapacity() string {
	return fmt.Sprintf("%d CPUs and %.1f GiB of memory", d.NCPU, float64(d.MemTotal)/(1<<30))
}
