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

// The band needs a contiguous window, so one busy port inside it moves all 80.
func TestChoosePortsMovesTheWholeBandForOneBusyPort(t *testing.T) {
	t.Parallel()

	reg := &registry.Registry{}
	cp := &reg.ControlPlane
	moved, err := ChoosePorts(reg, nil, busy(19050))
	if err != nil {
		t.Fatalf("ChoosePorts: %v", err)
	}
	if len(moved) != 1 || moved[0].Name != registry.PortEnvBase {
		t.Fatalf("want the band to move, got %v", moved)
	}
	lo, hi := cp.EnvPortRange()
	if lo <= 19050 && 19050 <= hi {
		t.Errorf("the new band %d-%d still contains the busy port", lo, hi)
	}
	if hi-lo+1 != registry.EnvPortWidth {
		t.Errorf("the band is %d wide, want %d", hi-lo+1, registry.EnvPortWidth)
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

// The Deployment GUI is published only because 19003 falls inside the band, so a
// band that moved took it off the host with no sign but a refused connection.
// It is slot 0's spare, so it moves with the band (NERD027 SPEC001 publishes it
// in its own right and removes the coupling).
func TestAMovedBandTakesTheGUIWithIt(t *testing.T) {
	t.Parallel()

	// Everything in the default band is held, so the band has to move.
	probe := func(b compose.HostBinding) bool {
		return b.Port >= registry.DefaultPortBase && b.Port < registry.DefaultPortBase+registry.EnvPortWidth
	}
	reg := &registry.Registry{}
	if _, err := ChoosePorts(reg, nil, probe); err != nil {
		t.Fatalf("ChoosePorts: %v", err)
	}

	base := reg.ControlPlane.Port(registry.PortEnvBase)
	if base == registry.DefaultPortBase {
		t.Fatalf("the band did not move: %d", base)
	}
	if got, want := reg.ControlPlane.Port(registry.PortGUI), base+3; got != want {
		t.Errorf("the GUI is on %d, want %d -- slot 0's spare inside the moved band", got, want)
	}
}
