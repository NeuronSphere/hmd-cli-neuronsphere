package controlplane

import (
	"fmt"
	"time"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/compose"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
)

// ephemeralFloor is the lowest port an operating system hands out for an
// outbound connection: 49152 on macOS and the BSDs, 32768 on Linux. A published
// port at or above it can be taken out from under the platform between one
// start and the next, so nothing is chosen there.
//
// The lower of the two is used, because the platform runs on both and a port
// that is safe on one machine and not the other is worse than a lower one.
const ephemeralFloor = 32768

// defaultProbeTimeout is how long the fallback connect probe waits. Short: a
// listener on loopback answers immediately or is not there.
const defaultProbeTimeout = 200 * time.Millisecond

// guiSpareOffset is where the Deployment GUI sits inside slot 0: the spare port,
// which is why its default is 19003 and not a number of its own.
const guiSpareOffset = 3

// PortChoice is one port that had to move, for a report the reader can act on.
type PortChoice struct {
	Name string
	From int
	To   int
}

func (c PortChoice) String() string {
	return fmt.Sprintf("%s: %d is in use, moved to %d", c.Name, c.From, c.To)
}

// portPlan is how one published port is chosen.
type portPlan struct {
	name string
	// proto and hostIP are how the port is *published*, and therefore how it has
	// to be probed. The resolver is the one that differs -- 127.0.0.1:<port>/udp
	// -- and probing it as TCP on 0.0.0.0 asks a question the engine never asks:
	// a UDP listener answers no TCP bind, so the port reads free and the engine's
	// own bind then fails (NERD025 SPEC007).
	proto  string
	hostIP string
	// altStart is where the search begins when the preferred port is taken.
	//
	// Not simply "the next port up". Scanning 81..1023 for an alternative to 80
	// would probe a thousand ports that Linux forbids an unprivileged process
	// from binding, each falling back to a connect probe with a timeout. 8080
	// is both the conventional second choice and the fast one.
	altStart int
	width    int
}

// portPlans is the four ports hmd_proxy publishes, and the order they are chosen
// in.
//
// There is no band any more. It used to go first, because 80 contiguous ports
// have the least room to manoeuvre and a single port chosen first could sit in
// the middle of the only window wide enough -- a real constraint, and the single
// hardest thing to place on a busy machine. Each environment now publishes its
// own two ports from its own container, so what is left is four independent
// singles: strictly easier than what it replaces (NERD027 SPEC005).
//
// The Deployment GUI is one of them now. It used to be published only because
// 19003 fell inside the band, which meant a moved band took it off the host
// entirely (NERD027 SPEC001).
var portPlans = []portPlan{
	{name: registry.PortHTTP, altStart: 8080, width: 1},
	{name: registry.PortFloci, altStart: 4567, width: 1},
	{name: registry.PortGUI, altStart: 19004, width: 1},
	{name: registry.PortDNS, altStart: 19154, width: 1, proto: "udp", hostIP: "127.0.0.1"},
}

// ChoosePorts settles which host ports this home publishes, moving off any that
// something else already holds.
//
// The defaults are kept wherever they are free, so a machine that works today
// does not move and writes nothing to the registry. Only a port that is
// actually taken is chosen anew, and only that one is recorded -- which makes
// the recorded set a description of what is unusual about this machine, not a
// copy of the defaults.
//
// ours reports the ports our own containers already publish. Without it a
// restart would find the platform's own ports busy and walk it a little further
// up the port space every time.
func ChoosePorts(reg *registry.Registry, ours map[int]bool, probe compose.Prober) ([]PortChoice, error) {
	cp := &reg.ControlPlane
	if probe == nil {
		probe = compose.BindProber(defaultProbeTimeout)
	}
	// Ports already handed out by this call, so two names cannot choose the
	// same one.
	claimed := map[int]bool{}

	var moved []PortChoice
	for _, plan := range portPlans {
		want := cp.Port(plan.name)
		taken := func(port int) bool {
			if claimed[port] {
				return true
			}
			if ours[port] {
				return false
			}
			return probe(compose.HostBinding{Port: port, Protocol: plan.proto, HostIP: plan.hostIP})
		}
		got := want
		if !isFree(want, plan.width, taken) {
			var err error
			if got, err = firstFree(plan.altStart, plan.width, taken); err != nil {
				return nil, fmt.Errorf("choosing a host port for %s: %w", plan.name, err)
			}
		}
		for p := got; p < got+plan.width; p++ {
			claimed[p] = true
		}
		if got == want {
			continue
		}
		if cp.Ports == nil {
			cp.Ports = map[string]int{}
		}
		cp.Ports[plan.name] = got
		moved = append(moved, PortChoice{Name: plan.name, From: want, To: got})

	}
	return moved, nil
}

func isFree(start, width int, taken func(int) bool) bool {
	if start+width > ephemeralFloor {
		return false
	}
	for p := start; p < start+width; p++ {
		if taken(p) {
			return false
		}
	}
	return true
}

// firstFree returns the lowest port at or above start where `width` consecutive
// ports are all free.
func firstFree(start, width int, taken func(int) bool) (int, error) {
	for p := start; p+width <= ephemeralFloor; p++ {
		free := true
		for q := p; q < p+width; q++ {
			if taken(q) {
				free = false
				// Nothing between here and the busy port can work either, so
				// the scan resumes past it rather than retrying every offset.
				p = q
				break
			}
		}
		if free {
			return p, nil
		}
	}
	if width > 1 {
		return 0, fmt.Errorf("no run of %d free host ports below %d", width, ephemeralFloor)
	}
	return 0, fmt.Errorf("no free host port below %d", ephemeralFloor)
}
