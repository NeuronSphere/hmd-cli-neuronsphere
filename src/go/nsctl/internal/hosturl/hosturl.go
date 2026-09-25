// Package hosturl is where this machine reaches the local control plane.
//
// Every host-facing URL nsctl builds -- the deployment service, the Artifact
// Librarian, Floci, an environment's routes -- is the same two facts plus a
// path: which port hmd_proxy publishes HTTP on, and which port it streams Floci
// on. Those were written down as `http://localhost/...` and
// `http://localhost:4566` in a dozen places, which is exactly what stopped
// either port from ever moving (NERD007 SPEC001).
//
// They are resolved once per process, from the registry, before any command
// runs. Process-global for the same reason internal/loopback is: a URL can be
// built anywhere, including in code added later, and an opt-in accessor would
// be correct in the places someone remembered and silently wrong everywhere
// else.
//
// The defaults are the historical ports, so a process that never calls Apply --
// a unit test, a command run before a registry exists -- behaves exactly as it
// did.
package hosturl

import (
	"fmt"
	"strings"
	"sync"
)

const (
	defaultHTTPPort  = 80
	defaultFlociPort = 4566
)

var (
	mu        sync.RWMutex
	httpPort  = defaultHTTPPort
	flociPort = defaultFlociPort
)

// Apply records the ports this home publishes. Zero leaves a port unchanged,
// so a partially-known registry cannot reset the other half to a default.
func Apply(http, floci int) {
	mu.Lock()
	defer mu.Unlock()
	if http > 0 {
		httpPort = http
	}
	if floci > 0 {
		flociPort = floci
	}
}

// Reset restores the defaults. For tests, which must not inherit each other's
// ports.
func Reset() { Apply2(defaultHTTPPort, defaultFlociPort) }

// Apply2 sets both ports unconditionally.
func Apply2(http, floci int) {
	mu.Lock()
	defer mu.Unlock()
	httpPort, flociPort = http, floci
}

// HTTPPort and FlociPort are the published ports.
func HTTPPort() int {
	mu.RLock()
	defer mu.RUnlock()
	return httpPort
}

func FlociPort() int {
	mu.RLock()
	defer mu.RUnlock()
	return flociPort
}

// Base is the origin the control plane's routes are served at.
//
// Port 80 is left off, because it is the default for the scheme and every URL
// in the documentation, in a bookmark and in a test suite omits it.
func Base() string {
	if p := HTTPPort(); p != defaultHTTPPort {
		return fmt.Sprintf("http://localhost:%d", p)
	}
	return "http://localhost"
}

// HostBase is Base for a name the proxy answers to rather than for localhost.
//
// A user interface is reached at its Ingress hostname, but it is served by the
// same hmd_proxy on the same HTTP port -- so the port has to travel with the
// name. The start summary built this by hand as "http://" + host + "/" and so
// printed a link to port 80 on a home whose HTTP port had moved, which is
// reachable by nothing (NERD025 SPEC006).
//
// Port 80 is omitted for Base's reason: it is the scheme's default and every
// URL in the documentation and in a bookmark omits it.
func HostBase(host string) string {
	if p := HTTPPort(); p != defaultHTTPPort {
		return fmt.Sprintf("http://%s:%d", host, p)
	}
	return "http://" + host
}

// Route is Base with a path, normalised to one leading slash and no trailing
// one -- callers append their own, and a doubled slash reaches nginx as a
// different location than the one that was configured.
func Route(path string) string {
	return Base() + "/" + strings.Trim(path, "/")
}

// Floci is where the host reaches the emulator. Always carries its port: 4566
// is not a default for anything.
func Floci() string {
	return fmt.Sprintf("http://localhost:%d", FlociPort())
}
