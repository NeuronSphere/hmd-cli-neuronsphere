package compose

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
)

// The bundled file must satisfy the invariant the whole layout rests on:
// hmd_proxy is the only container that publishes host ports.
func TestTheBundledFileHasOneExclusivePublisher(t *testing.T) {
	t.Parallel()

	p := parseControlPlane(t, controlPlaneEnv())
	active := map[string]bool{"deployment-gui": true}

	if got := CheckExclusivePublisher(p, active); len(got) > 0 {
		t.Errorf("a service other than the proxy publishes host ports: %v", got)
	}
	if got := CheckReserved(p, active); len(got) > 0 {
		t.Errorf("a service claims a reserved port: %v", got)
	}
}

func TestCheckExclusivePublisherFindsAnOffender(t *testing.T) {
	t.Parallel()

	p := &Project{Name: "p", Services: []Service{
		{Key: "proxy", Ports: []Port{{80, 80, 80, 80, "tcp"}}},
		{Key: "db", Ports: []Port{{5432, 5432, 5432, 5432, "tcp"}}},
	}}

	got := CheckExclusivePublisher(p, nil)
	if len(got) != 1 {
		t.Fatalf("got %v, want one offender", got)
	}
	if got[0].Port != 5432 || got[0].Service != "db" {
		t.Errorf("offender = %+v, want db on 5432", got[0])
	}
	// The refusal has to say what to do instead.
	if got[0].Reason == "" {
		t.Error("the conflict carries no remedy")
	}
}

func TestCheckExclusivePublisherIgnoresADisabledService(t *testing.T) {
	t.Parallel()

	p := &Project{Name: "p", Services: []Service{
		{Key: "gui", Profiles: []string{"deployment-gui"}, Ports: []Port{{19003, 19003, 8000, 8000, "tcp"}}},
	}}
	if got := CheckExclusivePublisher(p, map[string]bool{}); len(got) != 0 {
		t.Errorf("a profile-disabled service was reported: %v", got)
	}
	if got := CheckExclusivePublisher(p, map[string]bool{"deployment-gui": true}); len(got) != 1 {
		t.Errorf("an enabled offender was not reported: %v", got)
	}
}

func TestCheckReservedIgnoresTheProxyItself(t *testing.T) {
	t.Parallel()

	// The proxy is who the 80 and 4566 reservations are *for*.
	p := &Project{Name: "p", Services: []Service{
		{Key: "proxy", Ports: []Port{{80, 80, 80, 80, "tcp"}, {4566, 4566, 4566, 4566, "tcp"}}},
	}}
	if got := CheckReserved(p, nil); len(got) != 0 {
		t.Errorf("the proxy was reported against its own reservations: %v", got)
	}

	other := &Project{Name: "p", Services: []Service{
		{Key: "thing", Ports: []Port{{8082, 8082, 8082, 8082, "tcp"}}},
	}}
	got := CheckReserved(other, nil)
	if len(got) != 1 || got[0].Port != 8082 {
		t.Fatalf("got %v, want the hmd login port reported", got)
	}
	if got[0].Reason == "" {
		t.Error("the conflict does not say who reserved the port")
	}
}

// port_validator.parse_host_port returns None for a range, so the Python never
// probes the 19000-19079 band at all. Expanding it is the point.
func TestHostPortsExpandsRanges(t *testing.T) {
	t.Parallel()

	p := parseControlPlane(t, controlPlaneEnv())
	ports := HostPorts(p, map[string]bool{"deployment-gui": true})

	if len(ports) != 83 {
		t.Errorf("got %d host ports, want 83 (80, 4566, 18080 and the 80-wide band)", len(ports))
	}
	want := map[int]bool{80: true, 4566: true, 18080: true, 19000: true, 19003: true, 19079: true}
	have := map[int]bool{}
	for _, p := range ports {
		have[p] = true
	}
	for port := range want {
		if !have[port] {
			t.Errorf("port %d is missing from the expanded set", port)
		}
	}
	// Sorted and deduplicated.
	for i := 1; i < len(ports); i++ {
		if ports[i] <= ports[i-1] {
			t.Fatalf("ports are not sorted and unique around index %d: %v", i, ports[i-1:i+1])
		}
	}
}

func TestHostPortsSkipsContainerOnlyPublications(t *testing.T) {
	t.Parallel()

	p := &Project{Services: []Service{{Key: "a", Ports: []Port{{0, 0, 8080, 8080, "tcp"}}}}}
	if got := HostPorts(p, nil); len(got) != 0 {
		t.Errorf("got %v, want none -- Docker picks the host port", got)
	}
}

func TestCheckInUseReportsOnlyForeignPorts(t *testing.T) {
	t.Parallel()

	p := &Project{Name: "p", Services: []Service{
		{Key: "proxy", Ports: []Port{{80, 82, 80, 82, "tcp"}}},
	}}

	// 80 is ours, 81 is someone else's, 82 is free.
	busy := map[int]bool{80: true, 81: true}
	ours := map[int]bool{80: true}

	got := CheckInUse(context.Background(), p, nil, ours, func(port int) bool { return busy[port] })
	if len(got) != 1 {
		t.Fatalf("got %v, want only the foreign port", got)
	}
	if got[0].Port != 81 {
		t.Errorf("reported port %d, want 81", got[0].Port)
	}
	if got[0].Service != "proxy" {
		t.Errorf("reported service %q, want the port's owner", got[0].Service)
	}
}

// Without this, every already-running environment reports its own ports taken.
func TestCheckInUseTreatsOurOwnPortsAsFree(t *testing.T) {
	t.Parallel()

	p := &Project{Name: "p", Services: []Service{
		{Key: "proxy", Ports: []Port{{19000, 19079, 19000, 19079, "tcp"}}},
	}}
	ours := map[int]bool{}
	for i := 19000; i <= 19079; i++ {
		ours[i] = true
	}

	got := CheckInUse(context.Background(), p, nil, ours, func(int) bool { return true })
	if len(got) != 0 {
		t.Errorf("got %d conflicts for ports we already bind, want none", len(got))
	}
}

// fakeLister returns canned container summaries.
type fakeLister struct {
	byProject map[string][]types.Container
	queried   []string
}

func (f *fakeLister) ContainerList(_ context.Context, opts container.ListOptions) ([]types.Container, error) {
	var project string
	opts.Filters.WalkValues("label", func(v string) error {
		if len(v) > len(LabelProject)+1 && v[:len(LabelProject)] == LabelProject {
			project = v[len(LabelProject)+1:]
		}
		return nil
	})
	f.queried = append(f.queried, project)
	return f.byProject[project], nil
}

// Reading the structured Ports field off the API removes the text parsing the
// Python does on `docker ps`'s Ports column.
func TestOurPortsReadsPublishedPortsStructurally(t *testing.T) {
	t.Parallel()

	f := &fakeLister{byProject: map[string][]types.Container{
		"cp":  {{Ports: []types.Port{{PublicPort: 80, PrivatePort: 80}, {PrivatePort: 8000}}}},
		"env": {{Ports: []types.Port{{PublicPort: 19033, PrivatePort: 8080}}}},
	}}

	got, known := OurPorts(context.Background(), f, "cp", "env", "")
	if !known {
		t.Error("the engine answered for both projects; this must report known")
	}
	if !got[80] || !got[19033] {
		t.Errorf("got %v, want the published ports", got)
	}
	// A container port with no public binding is not a host port.
	if got[8000] {
		t.Error("an unpublished container port was reported as bound on the host")
	}
	if len(f.queried) != 2 {
		t.Errorf("queried %v, want the two named projects and not the empty one", f.queried)
	}
}

func TestOurPortsToleratesADeadDaemon(t *testing.T) {
	t.Parallel()

	// An empty set is still the safe answer -- but it must not be reported as
	// "none of these ports are ours", which is what turned nsctl's own proxy
	// into a foreign process in the warnings (NERD021 SPEC005).
	got, known := OurPorts(context.Background(), errLister{}, "cp")
	if len(got) != 0 {
		t.Errorf("got %v, want an empty set", got)
	}
	if known {
		t.Error("a dead daemon must report that the answer is not known")
	}
}

// A failure on one project must not discard what the others reported, and must
// still mark the whole answer partial.
func TestOurPortsReportsAPartialAnswer(t *testing.T) {
	t.Parallel()

	f := &flakyLister{ok: map[string][]types.Container{
		"cp": {{Ports: []types.Port{{PublicPort: 80, PrivatePort: 80}}}},
	}}
	got, known := OurPorts(context.Background(), f, "cp", "broken")
	if !got[80] {
		t.Errorf("got %v, want the port the engine did report", got)
	}
	if known {
		t.Error("one failed project makes the answer partial")
	}
}

// flakyLister answers for the projects it knows and fails for the rest.
type flakyLister struct{ ok map[string][]types.Container }

func (f *flakyLister) ContainerList(_ context.Context, opts container.ListOptions) ([]types.Container, error) {
	for _, p := range opts.Filters.Get("label") {
		name := strings.TrimPrefix(p, LabelProject+"=")
		if list, found := f.ok[name]; found {
			return list, nil
		}
	}
	return nil, errors.New("no daemon")
}

type errLister struct{}

func (errLister) ContainerList(context.Context, container.ListOptions) ([]types.Container, error) {
	return nil, context.DeadlineExceeded
}

func TestTCPDialerReportsAListener(t *testing.T) {
	t.Parallel()

	ln, err := listenOnAFreePort()
	if err != nil {
		t.Skipf("could not bind a local port: %v", err)
	}
	defer ln.close()

	dial := TCPDialer(500 * time.Millisecond)
	if !dial(ln.port) {
		t.Errorf("port %d has a listener but was reported free", ln.port)
	}
	ln.close()
	if dial(ln.port) {
		t.Errorf("port %d was reported busy after the listener closed", ln.port)
	}
}

// listener is a bound port that can be closed, for the dialer test.
type listener struct {
	port int
	ln   net.Listener
	once sync.Once
}

func (l *listener) close() { l.once.Do(func() { l.ln.Close() }) }

func listenOnAFreePort() (*listener, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	return &listener{port: ln.Addr().(*net.TCPAddr).Port, ln: ln}, nil
}

var _ = filters.NewArgs
