// Package loopback dials the hostnames Floci stamps into its URLs on the local
// machine's loopback address, for a host that has not been told to resolve
// them.
//
// Floci writes FLOCI_HOSTNAME into every presigned S3 and API URL it hands
// back, so a host-side consumer is given a URL naming `neuronsphere:4566`.
// hmd_proxy publishes 4566 on loopback already, so the *address* is reachable
// and only the *name* is not -- which is the whole reason a local NeuronSphere
// used to require an /etc/hosts entry before it would start at all
// (NERD025 SPEC003).
//
// The override changes where the connection goes and leaves the request
// untouched. That is not an implementation detail: `host` is a signed header
// under SigV4, so rewriting the URL to name localhost would invalidate the
// signature on the very URL being fetched. Overriding the dial preserves it.
//
// It applies only to names that do not already resolve. A machine with the
// /etc/hosts entry behaves exactly as before, and -- the case that matters for
// correctness -- nsctl running *inside* a container, where these names are
// Docker aliases pointing at a sibling, is left alone rather than redirected to
// its own loopback.
package loopback

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Names are the hostnames Floci stamps into the URLs it returns. They match
// registry.FlociAlias and the alias the control-plane compose gives the proxy.
var Names = []string{"neuronsphere", "neuronsphere-workload"}

// Address is where a redirected name is dialled instead.
const Address = "127.0.0.1"

// InternalFlociPort is the port Floci listens on inside the network, and so the
// port it stamps into every URL it returns. It never changes; only where the
// host reaches it does.
const InternalFlociPort = 4566

// Resolver looks up a hostname. Injected so the decision is testable without
// depending on what the machine running the tests has in /etc/hosts.
type Resolver func(host string) ([]net.IP, error)

// Redirects reports which of the given names should be dialled on loopback:
// the ones this machine cannot resolve, and the ones it resolves *to* loopback.
//
// Both mean the same thing -- we are on the host, not inside the network -- and
// both need the redirect, because the host port is no longer necessarily the
// in-network one. A name that answers with a routable address is a Docker alias
// pointing at a sibling container, and must be left exactly alone.
func Redirects(names []string, resolve Resolver) map[string]bool {
	if resolve == nil {
		resolve = net.LookupIP
	}
	out := map[string]bool{}
	for _, name := range names {
		ips, err := resolve(name)
		if err != nil || len(ips) == 0 || allLoopback(ips) {
			out[name] = true
		}
	}
	return out
}

func allLoopback(ips []net.IP) bool {
	for _, ip := range ips {
		if !ip.IsLoopback() {
			return false
		}
	}
	return len(ips) > 0
}

// redirect substitutes the loopback address for a named host, and the host port
// for the in-network one.
//
// The port mapping is what lets Floci move off 4566 -- which is also
// LocalStack's port, so a machine already running one has to. A presigned URL
// names the in-network port because that is what Floci stamped into it; the
// host reaches the same service wherever hmd_proxy published it. Only the dial
// changes, so the Host header still carries the name and port that were signed.
//
// An address it does not recognise, or one that is not host:port at all, comes
// back untouched.
func redirect(addr string, hosts map[string]bool, ports map[int]int) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || !hosts[host] {
		return addr
	}
	if n, convErr := strconv.Atoi(port); convErr == nil {
		if mapped, ok := ports[n]; ok && mapped > 0 {
			port = strconv.Itoa(mapped)
		}
	}
	return net.JoinHostPort(Address, port)
}

// Transport returns a RoundTripper that dials the named hosts on loopback.
//
// It clones the given transport rather than mutating it, so installing this
// cannot change the behaviour of a client that was handed http.DefaultTransport
// and expects it unmodified.
func Transport(base http.RoundTripper, hosts map[string]bool, ports map[int]int) http.RoundTripper {
	if len(hosts) == 0 {
		return base
	}
	t, ok := base.(*http.Transport)
	if !ok {
		if t, ok = http.DefaultTransport.(*http.Transport); !ok {
			return base
		}
	}
	clone := t.Clone()
	d := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	clone.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return d.DialContext(ctx, network, redirect(addr, hosts, ports))
	}
	return clone
}

// Process-global on purpose. Every client nsctl builds takes the default
// transport, including ones added after this, and a presigned URL can surface
// in any of them -- an opt-in wrapper would be correct in the places someone
// remembered and silently wrong everywhere else.
var (
	cfgMu sync.RWMutex
	// What to redirect, read at dial time rather than captured.
	cfgHosts map[string]bool
	cfgPorts map[int]int
)

// init wraps http.DefaultTransport once, before anything else can run.
//
// Installed here rather than from Install, and that is the whole of a real data
// race. Install runs from PersistentPreRun, so a process executing more than one
// command -- which is exactly what a test binary is -- wrote this global while
// another goroutine's http.Client.send read it. No lock on this side can fix
// that, because the reader is net/http.
//
// Package load happens on one goroutine before any command or request exists, so
// the single write is safe. Afterwards the global is never written again: Install
// swaps only the configuration above, under a lock the dialer also takes.
//
// Nothing is resolved here. Until Install says otherwise the configuration is
// empty and every address passes straight through, so importing this package
// changes no behaviour by itself.
func init() {
	http.DefaultTransport = dynamicTransport(http.DefaultTransport)
}

// dynamicTransport is Transport with the hosts and ports read per dial instead
// of captured, so the redirect can change without touching the global again.
func dynamicTransport(base http.RoundTripper) http.RoundTripper {
	t, ok := base.(*http.Transport)
	if !ok {
		return base
	}
	clone := t.Clone()
	d := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	clone.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		cfgMu.RLock()
		hosts, ports := cfgHosts, cfgPorts
		cfgMu.RUnlock()
		return d.DialContext(ctx, network, redirect(addr, hosts, ports))
	}
	return clone
}

// Install points the redirect at loopback for whichever of Names this machine
// cannot reach directly, mapping the in-network Floci port to the host port this
// home publishes. A flociHostPort of 0, or one equal to the in-network port,
// leaves the port alone.
//
// Called more than once in a process: from PersistentPreRun with the registry as
// it stands, and again from a start that has just chosen a different Floci port.
// The last call wins, and no call writes http.DefaultTransport.
func Install(resolve Resolver, flociHostPort int) []string {
	hosts := Redirects(Names, resolve)
	ports := map[int]int{}
	if flociHostPort > 0 && flociHostPort != InternalFlociPort {
		ports[InternalFlociPort] = flociHostPort
	}

	cfgMu.Lock()
	cfgHosts, cfgPorts = hosts, ports
	cfgMu.Unlock()

	if len(hosts) == 0 {
		return nil
	}
	out := make([]string, 0, len(hosts))
	for _, name := range Names {
		if hosts[name] {
			out = append(out, name)
		}
	}
	return out
}
