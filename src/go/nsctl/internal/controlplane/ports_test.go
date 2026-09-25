package controlplane

import (
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/compose"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
)

// busy builds a prober that reports the given ports taken.
func busy(ports ...int) compose.Prober {
	taken := map[int]bool{}
	for _, p := range ports {
		taken[p] = true
	}
	return func(b compose.HostBinding) bool { return taken[b.Port] }
}

func free() compose.Prober { return func(compose.HostBinding) bool { return false } }

// The common case: nothing is in the way, nothing moves, and nothing is
// written. An install that works today must stay byte-identical.
func TestChoosePortsRecordsNothingWhenTheDefaultsAreFree(t *testing.T) {
	t.Parallel()

	reg := &registry.Registry{}
	cp := &reg.ControlPlane
	moved, err := ChoosePorts(reg, nil, free())
	if err != nil {
		t.Fatalf("ChoosePorts: %v", err)
	}
	if len(moved) != 0 {
		t.Errorf("ports moved with nothing in the way: %v", moved)
	}
	if len(cp.Ports) != 0 {
		t.Errorf("recorded %v; a home on the defaults should write nothing", cp.Ports)
	}
}

// The case this exists for: something else holds the port and the user does
// nothing about it.
func TestChoosePortsMovesOffATakenPort(t *testing.T) {
	t.Parallel()

	reg := &registry.Registry{}
	cp := &reg.ControlPlane
	moved, err := ChoosePorts(reg, nil, busy(80, 4566))
	if err != nil {
		t.Fatalf("ChoosePorts: %v", err)
	}
	if len(moved) != 2 {
		t.Fatalf("got %d moves, want 2: %v", len(moved), moved)
	}
	if got := cp.Port(registry.PortHTTP); got == 80 {
		t.Error("http stayed on the port that was taken")
	}
	if got := cp.Port(registry.PortFloci); got == 4566 {
		t.Error("floci stayed on the port that was taken")
	}
	// Untouched ports are not recorded, so the record says what moved.
	if _, ok := cp.Ports[registry.PortTrino]; ok {
		t.Error("trino was recorded although it did not move")
	}
}

// There is no band to place any more, so the hardest thing ChoosePorts used to
// do is simply gone: four independent single ports, each moved only if something
// else holds it (NERD027 SPEC005). The old test asserted that one busy port
// inside the window moved all eighty; it is replaced rather than fixed, because
// the behaviour it pinned is the behaviour being removed.
func TestOneBusyPortMovesOnlyItself(t *testing.T) {
	t.Parallel()

	// The HTTP port alone is taken.
	probe := func(b compose.HostBinding) bool { return b.Port == 80 }
	reg := &registry.Registry{}
	moved, err := ChoosePorts(reg, nil, probe)
	if err != nil {
		t.Fatalf("ChoosePorts: %v", err)
	}
	if len(moved) != 1 || moved[0].Name != registry.PortHTTP {
		t.Fatalf("moved = %+v, want only the HTTP port", moved)
	}
	if got := reg.ControlPlane.Port(registry.PortHTTP); got != 8080 {
		t.Errorf("HTTP moved to %d, want the conventional second choice 8080", got)
	}
	// Everything else stayed, and nothing else was recorded.
	for _, name := range []string{registry.PortFloci, registry.PortGUI, registry.PortDNS} {
		if _, recorded := reg.ControlPlane.Ports[name]; recorded {
			t.Errorf("%s was recorded though it never moved", name)
		}
	}
}

// Our own ports are not an obstacle to ourselves -- a restart must not walk the
// platform up the port space every time.
func TestChoosePortsTreatsOurOwnPortsAsFree(t *testing.T) {
	t.Parallel()

	reg := &registry.Registry{}
	ours := map[int]bool{80: true, 4566: true}
	moved, err := ChoosePorts(reg, ours, busy(80, 4566))
	if err != nil {
		t.Fatalf("ChoosePorts: %v", err)
	}
	if len(moved) != 0 {
		t.Errorf("moved off our own ports: %v", moved)
	}
}

// A chosen port must not land on another chosen port, or the band and a single
// port would fight.
func TestChosenPortsDoNotCollideWithEachOther(t *testing.T) {
	t.Parallel()

	reg := &registry.Registry{}
	cp := &reg.ControlPlane
	// Everything below 19200 is taken, so all five have to move and the band
	// is large enough to swallow a careless single-port choice.
	var taken []int
	for p := 80; p < 19200; p++ {
		taken = append(taken, p)
	}
	if _, err := ChoosePorts(reg, nil, busy(taken...)); err != nil {
		t.Fatalf("ChoosePorts: %v", err)
	}
	lo, hi := cp.EnvPortRange()
	seen := map[int]string{}
	for _, name := range []string{registry.PortHTTP, registry.PortFloci, registry.PortTrino, registry.PortDNS} {
		port := cp.Port(name)
		if prev, dup := seen[port]; dup {
			t.Errorf("%s and %s both chose %d", prev, name, port)
		}
		seen[port] = name
		if lo <= port && port <= hi {
			t.Errorf("%s chose %d, inside the environment band %d-%d", name, port, lo, hi)
		}
	}
}

// A chosen port must never land where the OS hands out ephemeral ports, or it
// will be taken out from under the platform later.
func TestChosenPortsStayBelowTheEphemeralRange(t *testing.T) {
	t.Parallel()

	reg := &registry.Registry{}
	cp := &reg.ControlPlane
	var taken []int
	for p := 80; p < 30000; p++ {
		taken = append(taken, p)
	}
	if _, err := ChoosePorts(reg, nil, busy(taken...)); err != nil {
		t.Fatalf("ChoosePorts: %v", err)
	}
	for _, name := range []string{registry.PortHTTP, registry.PortFloci, registry.PortTrino, registry.PortDNS, registry.PortEnvBase} {
		if got := cp.Port(name); got >= ephemeralFloor {
			t.Errorf("%s chose %d, at or above the ephemeral floor %d", name, got, ephemeralFloor)
		}
	}
}

// Exhaustion is reported, not absorbed.
func TestChoosePortsReportsExhaustion(t *testing.T) {
	t.Parallel()

	reg := &registry.Registry{}
	_, err := ChoosePorts(reg, nil, func(compose.HostBinding) bool { return true })
	if err == nil {
		t.Fatal("every port was taken and ChoosePorts succeeded")
	}
	if !strings.Contains(err.Error(), "free host port") {
		t.Errorf("error does not say what could not be found: %v", err)
	}
}

// The resolver is published as 127.0.0.1:<port>/udp, and it is the one binding
// whose protocol and interface actually differ from the rest. Probing it as TCP
// on 0.0.0.0 asks a question the engine never asks: a UDP listener answers no
// TCP bind, so the port reads as free and the engine's own bind then fails. This
// is precisely what BindProber exists to prevent (NERD025 SPEC007, SPEC008).
func TestTheResolverPortIsProbedAsItIsPublished(t *testing.T) {
	t.Parallel()

	var asked []compose.HostBinding
	probe := func(b compose.HostBinding) bool {
		asked = append(asked, b)
		return false
	}
	reg := &registry.Registry{}
	if _, err := ChoosePorts(reg, nil, probe); err != nil {
		t.Fatalf("ChoosePorts: %v", err)
	}

	var seen bool
	for _, b := range asked {
		if b.Port == 19153 {
			seen = true
			if b.Protocol != "udp" {
				t.Errorf("the resolver was probed as %q, want udp", b.Protocol)
			}
			if b.HostIP != "127.0.0.1" {
				t.Errorf("the resolver was probed on %q, want 127.0.0.1", b.HostIP)
			}
		}
	}
	if !seen {
		t.Errorf("the resolver's port was never probed: %v", asked)
	}
}

// The Deployment GUI is a chosen port in its own right now. It used to be
// published only because 19003 fell inside the band, which meant a moved band
// took it off the host with no sign but a refused connection -- and nothing
// probed it, so a machine already using 19003 collided silently (NERD027
// SPEC001).
func TestTheGUIPortIsChosenLikeAnyOther(t *testing.T) {
	t.Parallel()

	probe := func(b compose.HostBinding) bool { return b.Port == 19003 }
	reg := &registry.Registry{}
	moved, err := ChoosePorts(reg, nil, probe)
	if err != nil {
		t.Fatalf("ChoosePorts: %v", err)
	}
	if len(moved) != 1 || moved[0].Name != registry.PortGUI {
		t.Fatalf("moved = %+v, want only the GUI port", moved)
	}
	if got := reg.ControlPlane.Port(registry.PortGUI); got == 19003 {
		t.Error("the GUI did not move off the port something else holds")
	}
}
