package compose

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
)

// FlociService is the compose service key for Floci, which supervises the
// containers backing every account's databases.
const FlociService = "floci"

// ProxyService is the only service allowed to publish host ports. Everything
// else is reached through it, and that invariant is what lets several named
// environments coexist on one machine: hmd_proxy owns 80, 4566, 18080 and the
// whole 19000-19079 band, and nginx adds and removes listeners inside it with a
// reload rather than a restart.
const ProxyService = "proxy"

// ReservedPorts are host ports claimed by tools other than the containers, so a
// service mapping one is a conflict even though nothing is listening yet.
var ReservedPorts = map[int]string{
	8082: "hmd login (Okta OAuth2 callback)",
	80:   "hmd_proxy (NeuronSphere HTTP routes)",
	4566: "hmd_proxy (control-plane Floci stream)",
}

// Conflict is one port problem, phrased so the message can be printed as is.
type Conflict struct {
	Port    int
	Service string
	Reason  string
}

func (c Conflict) String() string {
	return fmt.Sprintf("port %d (%s): %s", c.Port, c.Service, c.Reason)
}

// Dialer reports whether something is already listening on a host port.
type Dialer func(port int) bool

// TCPDialer probes 127.0.0.1 with a short timeout. This replaces
// port_validator.check_ports_in_use, which does the same thing with a
// non-blocking connect; it needs no cluster and no Docker.
func TCPDialer(timeout time.Duration) Dialer {
	return func(port int) bool {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), timeout)
		if err != nil {
			return false
		}
		conn.Close()
		return true
	}
}

// CheckExclusivePublisher returns every published host port owned by a service
// other than the proxy.
//
// In this layout that set must be empty: databases are unreachable from the
// host and every service is addressed through hmd_proxy. The Python raises
// SystemExit on it, and so does nsctl -- it is a refused precondition, not a
// warning.
func CheckExclusivePublisher(p *Project, active map[string]bool) []Conflict {
	var out []Conflict
	for _, s := range p.Services {
		if s.Key == ProxyService || !s.EnabledBy(active) {
			continue
		}
		for _, port := range s.Ports {
			for host := port.HostStart; host <= port.HostEnd && host != 0; host++ {
				out = append(out, Conflict{
					Port: host, Service: s.Key,
					Reason: "only hmd_proxy may publish host ports in this layout; route it through hmd_proxy instead -- HTTP at http://localhost/<env>/<service>/, other protocols via an nginx stream listener",
				})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out
}

// CheckReserved returns services mapping a port reserved for something else. A
// port mapped by the proxy is not a conflict -- the proxy is who the
// reservation is for.
func CheckReserved(p *Project, active map[string]bool) []Conflict {
	var out []Conflict
	for _, s := range p.Services {
		if s.Key == ProxyService || !s.EnabledBy(active) {
			continue
		}
		for _, port := range s.Ports {
			for host := port.HostStart; host <= port.HostEnd && host != 0; host++ {
				if reason, ok := ReservedPorts[host]; ok {
					out = append(out, Conflict{Port: host, Service: s.Key, Reason: "reserved for " + reason})
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out
}

// HostPorts is every host port the enabled services publish, sorted.
//
// Ranges are expanded. port_validator.parse_host_port returns None for a range,
// so the Python never checks the 19000-19079 band at all -- eighty ports that
// silently skip the in-use probe. Expanding them here is why the probe below
// runs concurrently.
func HostPorts(p *Project, active map[string]bool) []int {
	seen := map[int]bool{}
	var out []int
	for _, s := range p.Services {
		if !s.EnabledBy(active) {
			continue
		}
		for _, port := range s.Ports {
			if port.HostStart == 0 && port.HostEnd == 0 {
				continue
			}
			for host := port.HostStart; host <= port.HostEnd; host++ {
				if !seen[host] {
					seen[host] = true
					out = append(out, host)
				}
			}
		}
	}
	sort.Ints(out)
	return out
}

// CheckInUse probes which of a project's host ports are already bound by
// something that is not ours.
//
// ours reports a port already bound by one of our own compose projects, which
// is not a conflict -- without it every running environment would report its
// own ports as taken.
func CheckInUse(ctx context.Context, p *Project, active map[string]bool, ours map[int]bool, dial Dialer) []Conflict {
	ports := HostPorts(p, active)
	if dial == nil {
		dial = TCPDialer(200 * time.Millisecond)
	}

	// Eighty sequential dials at 200ms each would add sixteen seconds to a cold
	// start in the worst case, so they run concurrently.
	type probe struct {
		port int
		busy bool
	}
	results := make([]probe, len(ports))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 32)
	for i, port := range ports {
		if ours[port] {
			results[i] = probe{port: port, busy: false}
			continue
		}
		wg.Add(1)
		go func(i, port int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			select {
			case <-ctx.Done():
				return
			default:
			}
			results[i] = probe{port: port, busy: dial(port)}
		}(i, port)
	}
	wg.Wait()

	owner := map[int]string{}
	for _, s := range p.Services {
		if !s.EnabledBy(active) {
			continue
		}
		for _, port := range s.Ports {
			for host := port.HostStart; host <= port.HostEnd && host != 0; host++ {
				if _, seen := owner[host]; !seen {
					owner[host] = s.Key
				}
			}
		}
	}

	var out []Conflict
	for _, r := range results {
		if r.busy {
			out = append(out, Conflict{
				Port: r.port, Service: owner[r.port],
				Reason: "already in use on this host by something that is not part of the local NeuronSphere",
			})
		}
	}
	return out
}

// OurPorts returns the host ports bound by containers in the given compose
// projects, using this runner's daemon connection, and whether it could
// actually ask.
func (r *Runner) OurPorts(ctx context.Context, projects ...string) (map[int]bool, bool) {
	return OurPorts(ctx, r.API, projects...)
}

// listAPI is the container-listing surface OurPorts needs.
type listAPI interface {
	ContainerList(ctx context.Context, options container.ListOptions) ([]types.Container, error)
}

// OurPorts returns the host ports currently bound by containers in the given
// compose projects, and whether the engine could be asked at all.
//
// The Python parses `docker ps`'s Ports text column for this. Reading the
// structured Ports field off the API instead removes the parsing entirely.
//
// The second return replaces a bare `continue`. An empty set read as "none of
// these ports are ours" made every port the platform itself publishes look
// like a foreign process: on a machine whose engine nsctl could not reach, a
// start warned that 19000 and 19001 were "already in use ... by something that
// is not part of the local NeuronSphere" when they were nsctl's own proxy from
// the previous run. The empty set is still the safe answer; what changes is
// that "I could not ask" stops being spelled the same way as "none of them are
// ours" (NERD021 SPEC005).
func OurPorts(ctx context.Context, api listAPI, projects ...string) (map[int]bool, bool) {
	out := map[int]bool{}
	known := true
	for _, project := range projects {
		if project == "" {
			continue
		}
		f := filters.NewArgs()
		f.Add("label", LabelProject+"="+project)
		list, err := api.ContainerList(ctx, container.ListOptions{Filters: f})
		if err != nil {
			// Docker not running, or a timeout. Keep whatever the other
			// projects reported, and tell the caller this is partial.
			known = false
			continue
		}
		for _, c := range list {
			for _, port := range c.Ports {
				if port.PublicPort != 0 {
					out[int(port.PublicPort)] = true
				}
			}
		}
	}
	return out, known
}
