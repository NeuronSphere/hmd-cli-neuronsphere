package compose

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"sync"
	"syscall"
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
// environments coexist on one machine: hmd_proxy owns 80, 4566, 18080, the
// whole 19000-19079 band and the resolver's UDP listener, and nginx adds and
// removes listeners inside them with a reload rather than a restart.
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

// HostBinding is one published host port as the engine will bind it: a port, a
// protocol, and the interface it is pinned to (empty for every interface).
//
// The protocol and the interface are not decoration. A probe that assumes TCP
// cannot see a UDP listener at all, and a probe that assumes loopback cannot
// see a listener on a real interface -- which is invisible to the check and
// still fatal to the bind.
type HostBinding struct {
	Port     int
	Protocol string
	HostIP   string
}

func (b HostBinding) network() string {
	if b.Protocol == "udp" {
		return "udp"
	}
	return "tcp"
}

func (b HostBinding) address() string {
	host := b.HostIP
	if host == "" {
		// What Docker binds when a published port names no interface.
		host = "0.0.0.0"
	}
	return net.JoinHostPort(host, strconv.Itoa(b.Port))
}

func (b HostBinding) String() string {
	if b.Protocol == "udp" {
		return strconv.Itoa(b.Port) + "/udp"
	}
	return strconv.Itoa(b.Port)
}

// Dialer reports whether something is already listening on a host port.
type Dialer func(port int) bool

// TCPDialer probes 127.0.0.1 with a short timeout. This replaces
// port_validator.check_ports_in_use, which does the same thing with a
// non-blocking connect; it needs no cluster and no Docker.
//
// Retained as the fallback for a port this process may not bind -- see
// BindProber -- not as the primary probe.
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

// Prober reports whether a binding is already taken by something else.
type Prober func(b HostBinding) bool

// BindProber asks the question the engine is about to ask: can this address be
// bound?
//
// A connect probe asks something subtly different -- is anything answering on
// loopback -- and the two disagree in both directions that matter. A listener
// pinned to a real interface answers no connect on 127.0.0.1 and still refuses
// the engine's 0.0.0.0 bind. A UDP listener answers no TCP connect at all. Both
// then surface as a raw bind failure from the engine, after a check that said
// the port was free.
//
// A failed bind is only a conflict when it failed for being taken. On Linux a
// port below 1024 cannot be bound without privileges nsctl does not have, and
// reporting "not permitted" as "in use" would refuse every start on a machine
// where port 80 is merely privileged. That case falls back to the connect
// probe, which needs no privilege and is right about a listening socket.
//
// The bind is held for the length of a syscall pair. A third party racing for
// the same port in that window would see it taken; the alternative is not
// checking at all.
func BindProber(fallbackTimeout time.Duration) Prober {
	dial := TCPDialer(fallbackTimeout)
	// SO_REUSEADDR is cleared, and that is the whole difference between a probe
	// that agrees with the engine and one that does not. Go sets it on every
	// listener by default; the engine does not. With it set, binding 0.0.0.0
	// succeeds on a BSD kernel even while another socket holds the same port on
	// a specific interface -- so the probe passes and the engine's bind then
	// fails, which is precisely the case this exists to catch.
	lc := net.ListenConfig{
		Control: func(_, _ string, c syscall.RawConn) error {
			var serr error
			if err := c.Control(func(fd uintptr) {
				serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 0)
			}); err != nil {
				return err
			}
			return serr
		},
	}
	return func(b HostBinding) bool {
		var err error
		if b.network() == "udp" {
			var pc net.PacketConn
			if pc, err = lc.ListenPacket(context.Background(), "udp", b.address()); err == nil {
				pc.Close()
				return false
			}
		} else {
			var ln net.Listener
			if ln, err = lc.Listen(context.Background(), "tcp", b.address()); err == nil {
				ln.Close()
				return false
			}
		}
		if bindErrorMeansInUse(err) {
			return true
		}
		// Could not be decided by binding. UDP has no connect probe to fall
		// back to, so an undecidable UDP port is reported free rather than
		// guessed at: a false conflict refuses a start that would have worked.
		if b.network() == "udp" {
			return false
		}
		return dial(b.Port)
	}
}

// bindErrorMeansInUse distinguishes "something already has this" from "this
// process may not have it", which need opposite answers.
func bindErrorMeansInUse(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE)
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
	for _, b := range HostBindings(p, active) {
		if !seen[b.Port] {
			seen[b.Port] = true
			out = append(out, b.Port)
		}
	}
	sort.Ints(out)
	return out
}

// HostBindings is every binding the enabled services publish, ranges expanded
// and sorted, each carrying the protocol and interface the engine will use.
func HostBindings(p *Project, active map[string]bool) []HostBinding {
	type key struct {
		port  int
		proto string
		ip    string
	}
	seen := map[key]bool{}
	var out []HostBinding
	for _, s := range p.Services {
		if !s.EnabledBy(active) {
			continue
		}
		for _, port := range s.Ports {
			if port.HostStart == 0 && port.HostEnd == 0 {
				continue
			}
			for host := port.HostStart; host <= port.HostEnd; host++ {
				b := HostBinding{Port: host, Protocol: port.Protocol, HostIP: port.HostIP}
				k := key{host, b.network(), port.HostIP}
				if !seen[k] {
					seen[k] = true
					out = append(out, b)
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Port != out[j].Port {
			return out[i].Port < out[j].Port
		}
		return out[i].network() < out[j].network()
	})
	return out
}

// CheckInUse probes which of a project's host bindings are already taken by
// something that is not ours.
//
// ours reports a port already bound by one of our own compose projects, which
// is not a conflict -- without it every running environment would report its
// own ports as taken.
func CheckInUse(ctx context.Context, p *Project, active map[string]bool, ours map[int]bool, probe Prober) []Conflict {
	bindings := HostBindings(p, active)
	if probe == nil {
		probe = BindProber(200 * time.Millisecond)
	}

	// Eighty-four sequential probes would add most of half a minute
	// to a cold start in the worst case, so they run concurrently.
	type result struct {
		binding HostBinding
		busy    bool
	}
	results := make([]result, len(bindings))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 32)
	for i, b := range bindings {
		results[i] = result{binding: b}
		if ours[b.Port] {
			continue
		}
		wg.Add(1)
		go func(i int, b HostBinding) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			select {
			case <-ctx.Done():
				return
			default:
			}
			results[i] = result{binding: b, busy: probe(b)}
		}(i, b)
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
				Port: r.binding.Port, Service: owner[r.binding.Port],
				Reason: fmt.Sprintf("%s is already in use on this host by something that is not part "+
					"of the local NeuronSphere", r.binding),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
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
