package status

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
)

// fakeDocker answers from maps, so the snapshot logic is testable without a
// daemon -- the same narrowing bartleby uses over the real Docker client.
type fakeDocker struct {
	running   map[string]bool
	names     map[string]bool
	resources map[string]string // "<service>/<account>/<resource>" -> container name
	networks  map[string]bool
}

func (f *fakeDocker) Running(_ context.Context, name string) (bool, error) {
	return f.running[name], nil
}

func (f *fakeDocker) ContainerNames(context.Context) map[string]bool {
	if f.names == nil {
		return map[string]bool{}
	}
	return f.names
}

func (f *fakeDocker) FlociContainer(_ context.Context, service, account, resource string) (string, bool) {
	name := f.resources[service+"/"+account+"/"+resource]
	return name, f.running[name]
}

func (f *fakeDocker) NetworkExists(_ context.Context, name string) bool {
	return f.networks[name]
}

func fakeEnv(vars map[string]string) registry.Lookup {
	return func(key string) string { return vars[key] }
}

// liveEnv mirrors the environment on the machine this was developed against:
// account 000000000001, slot 8, cluster ns-local-57aa833c.
func liveEnv() *registry.Environment {
	return &registry.Environment{
		AccountID:      "000000000001",
		Bootstrap:      map[string]any{"csd_nid": "ms-deployment-local", "k3s_uid": "uid"},
		ComposeProject: "ns-57aa833c-env-local",
		DBContainer:    "hmd_db-local",
		DeploymentID:   "local",
		GraphContainer: "global-graph-local",
		K3sCluster:     "ns-local-57aa833c",
		Kubeconfig:     "/home/.cache/environments/local/k3s/kubeconfig",
		Name:           "local",
		PortBase:       19000,
		PortSlot:       8,
		Slug:           "local",
		StateDir:       "/home/.cache/environments/local",
	}
}

// The Python inspects env.db_container by name, but that is a network alias
// attached to a Floci-spawned container whose real name is opaque -- so it
// always reports the database stopped. Resolving by the label triple is what
// makes the answer true.
func TestDatabaseAndGraphResolveByFlociLabelNotByAlias(t *testing.T) {
	t.Parallel()

	const dbContainer = "floci-rds-db-9AB0B4E70F8247B398089EB7-42cf27"
	const graphContainer = "floci-neptune-global-graph-hmd-inf-neptune-local-local-reg1-hmdtr1"

	d := &fakeDocker{
		running: map[string]bool{dbContainer: true, graphContainer: true, "floci": true},
		resources: map[string]string{
			"rds/000000000001/environment-db-hmd-postgres-rds-local-local-reg1-hmdtr1":  dbContainer,
			"neptune/000000000001/global-graph-hmd-inf-neptune-local-local-reg1-hmdtr1": graphContainer,
		},
	}
	r := &Reporter{Docker: d, Lookup: fakeEnv(map[string]string{"HMD_CUSTOMER_CODE": "hmdtr1"})}

	snap := r.EnvironmentStatus(context.Background(), liveEnv())

	byRole := map[string]Container{}
	for _, c := range snap.Containers {
		byRole[c.Role] = c
	}
	if got := byRole["db"]; got.Name != dbContainer || !got.Running {
		t.Errorf("db = %+v, want the label-resolved container, running", got)
	}
	if got := byRole["graph"]; got.Name != graphContainer || !got.Running {
		t.Errorf("graph = %+v, want the label-resolved container, running", got)
	}
	// The alias must not appear as a container name.
	for _, c := range snap.Containers {
		if c.Name == "hmd_db-local" || c.Name == "global-graph-local" {
			t.Errorf("%s reported the DNS alias %q as a container", c.Role, c.Name)
		}
	}
}

// A default environment provisions no graph at all -- the Neptune cluster is
// lazy. That is a normal state, not a fault.
func TestAnUnprovisionedResourceIsReportedNotProvisioned(t *testing.T) {
	t.Parallel()

	r := &Reporter{Docker: &fakeDocker{}, Lookup: fakeEnv(nil)}
	snap := r.EnvironmentStatus(context.Background(), liveEnv())

	for _, c := range snap.Containers {
		if c.Role == "graph" {
			if c.Running {
				t.Error("an absent graph must not report running")
			}
			if c.Name != "(not provisioned)" {
				t.Errorf("graph name = %q, want an explicit not-provisioned marker", c.Name)
			}
		}
	}
}

func TestEnvironmentStatusResolvesTheK3sContainer(t *testing.T) {
	t.Parallel()

	const k3s = "floci-eks-000000000001.ns-local-57aa833c"
	r := &Reporter{
		Docker: &fakeDocker{names: map[string]bool{k3s: true}, running: map[string]bool{k3s: true}},
		Lookup: fakeEnv(nil),
	}
	snap := r.EnvironmentStatus(context.Background(), liveEnv())

	for _, c := range snap.Containers {
		if c.Role == "k3s" {
			if c.Name != k3s || !c.Running {
				t.Errorf("k3s = %+v, want %q running", c, k3s)
			}
			return
		}
	}
	t.Error("no k3s container in the snapshot")
}

func TestRoutesUseTheEnvironmentsOwnPorts(t *testing.T) {
	t.Parallel()

	// Trino routed, so this test is about the slot arithmetic rather than about
	// whether Trino is there; TestTrinoRouteOnlyWhenRouted owns that.
	r := &Reporter{Docker: &fakeDocker{}, Lookup: fakeEnv(nil),
		Routed: func(string, int) bool { return true }}
	snap := r.EnvironmentStatus(context.Background(), liveEnv())

	want := map[string]string{
		"services": "http://localhost/local/<service>/",
		"floci":    "http://localhost:4566",
		// slot 8: base 19032, trino +1; k3s band is base + 16*4 + slot.
		"trino":          "localhost:19033",
		"k3s":            "localhost:19072",
		"deployment_gui": "http://localhost:19003",
	}
	for key, val := range want {
		if snap.Routes[key] != val {
			t.Errorf("route %s = %q, want %q", key, snap.Routes[key], val)
		}
	}
	if len(snap.RouteOrder) != len(snap.Routes) {
		t.Errorf("route order has %d entries for %d routes", len(snap.RouteOrder), len(snap.Routes))
	}
}

// NERD023 SPEC003: an environment's Trino port belongs to its slot whether or
// not anything listens on it, so the route is reported from the router's record
// instead. Asserted in both directions -- the defect this replaces would have
// passed a routed-only assertion.
func TestTrinoRouteOnlyWhenRouted(t *testing.T) {
	t.Parallel()

	env := liveEnv()

	var askedSlug string
	var askedPort int
	routed := &Reporter{Docker: &fakeDocker{}, Lookup: fakeEnv(nil),
		Routed: func(slug string, port int) bool {
			askedSlug, askedPort = slug, port
			return true
		}}
	if got := routed.EnvironmentStatus(context.Background(), env).Routes["trino"]; got != "localhost:19033" {
		t.Errorf("a routed Trino should be reported, got %q", got)
	}
	if askedSlug != env.Slug || askedPort != env.TrinoPort() {
		t.Errorf("asked about %q:%d, want %q:%d", askedSlug, askedPort, env.Slug, env.TrinoPort())
	}

	absent := &Reporter{Docker: &fakeDocker{}, Lookup: fakeEnv(nil),
		Routed: func(string, int) bool { return false }}
	snap := absent.EnvironmentStatus(context.Background(), env)
	if got, ok := snap.Routes["trino"]; ok {
		t.Errorf("an unrouted Trino must not be reported, got %q", got)
	}
	// The rest of a full substrate is unaffected.
	if _, ok := snap.Routes["k3s"]; !ok {
		t.Errorf("the k3s route should survive, got %v", snap.RouteOrder)
	}
	if len(snap.RouteOrder) != len(snap.Routes) {
		t.Errorf("route order has %d entries for %d routes", len(snap.RouteOrder), len(snap.Routes))
	}

	// A nil hook reports nothing routed. Unlike Substrate's nil, this default is
	// restrictive on purpose: a permissive one would reinstate the defect.
	nilHook := &Reporter{Docker: &fakeDocker{}, Lookup: fakeEnv(nil)}
	if got, ok := nilHook.EnvironmentStatus(context.Background(), env).Routes["trino"]; ok {
		t.Errorf("a nil Routed hook must not report Trino, got %q", got)
	}
}

func TestGUIRoutesAreOmittedWhenDisabled(t *testing.T) {
	t.Parallel()

	r := &Reporter{Docker: &fakeDocker{}, Lookup: fakeEnv(map[string]string{
		"HMD_LOCAL_NEURONSPHERE_ENABLE_GUI": "false",
	})}
	snap := r.EnvironmentStatus(context.Background(), liveEnv())

	if _, ok := snap.Routes["deployment_gui"]; ok {
		t.Error("the GUI route is present with the GUI disabled")
	}
}

func TestGUIPortHonoursTheOverrideAndFallsBackOnGarbage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"the default", "", "http://localhost:19003"},
		{"an override", "18080", "http://localhost:18080"},
		{"garbage falls back rather than failing", "not-a-port", "http://localhost:19003"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := &Reporter{Docker: &fakeDocker{}, Lookup: fakeEnv(map[string]string{"HMD_LOCAL_GUI_HOST_PORT": tt.raw})}
			snap := r.EnvironmentStatus(context.Background(), liveEnv())
			if got := snap.Routes["deployment_gui"]; got != tt.want {
				t.Errorf("gui route = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRunningRequiresEveryContainer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		snap Environment
		want bool
	}{
		{"all up", Environment{Containers: []Container{{Running: true}, {Running: true}}}, true},
		{"one down", Environment{Containers: []Container{{Running: true}, {Running: false}}}, false},
		{"none listed is not running", Environment{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.snap.Running(); got != tt.want {
				t.Errorf("Running() = %v, want %v", got, tt.want)
			}
		})
	}
}

// SPEC002: control-plane stop refuses while any environment is running, so the
// snapshot has to say which.
func TestControlPlaneStatusSplitsRunningFromStoppedEnvironments(t *testing.T) {
	t.Parallel()

	const upK3s = "floci-eks-000000000001.ns-up-57aa833c"
	d := &fakeDocker{
		names:    map[string]bool{upK3s: true},
		running:  map[string]bool{"floci": true, "hmd_proxy": true, upK3s: true, "db-up": true, "graph-up": true},
		networks: map[string]bool{"neuronsphere_default-57aa833c": true},
		resources: map[string]string{
			"rds/000000000001/environment-db-hmd-postgres-rds-up-up-reg1-none":     "db-up",
			"neptune/000000000001/global-graph-hmd-inf-neptune-up-local-reg1-none": "graph-up",
		},
	}
	reg := &registry.Registry{
		ControlPlane: registry.ControlPlane{
			Bootstrapped:   true,
			ComposeProject: "local_neuronsphere-57aa833c",
			Network:        "neuronsphere_default-57aa833c",
		},
		DefaultEnv: "up",
		Environments: map[string]registry.Environment{
			"up": {Slug: "up", DeploymentID: "up", AccountID: "000000000001",
				K3sCluster: "ns-up-57aa833c", PortBase: 19000},
			"down": {Slug: "down", DeploymentID: "down", AccountID: "000000000002",
				K3sCluster: "ns-down-57aa833c", PortBase: 19000, PortSlot: 1},
		},
	}

	r := &Reporter{Docker: d, Lookup: fakeEnv(nil)}
	snap := r.ControlPlaneStatus(context.Background(), reg)

	if len(snap.RunningEnvs) != 1 || snap.RunningEnvs[0] != "up" {
		t.Errorf("running = %v, want [up]", snap.RunningEnvs)
	}
	if len(snap.StoppedEnvs) != 1 || snap.StoppedEnvs[0] != "down" {
		t.Errorf("stopped = %v, want [down]", snap.StoppedEnvs)
	}
	if !snap.NetworkExists {
		t.Error("the network exists and should be reported present")
	}
	if !snap.Bootstrapped {
		t.Error("bootstrapped should carry through from the registry")
	}
}

func TestControlPlaneOmitsTheGUIContainerWhenDisabled(t *testing.T) {
	t.Parallel()

	r := &Reporter{Docker: &fakeDocker{}, Lookup: fakeEnv(map[string]string{
		"HMD_LOCAL_NEURONSPHERE_ENABLE_GUI": "0",
	})}
	snap := r.ControlPlaneStatus(context.Background(), &registry.Registry{})

	for _, c := range snap.Containers {
		if c.Role == "deployment_gui" {
			t.Error("the GUI container is listed with the GUI disabled")
		}
	}
}

// A 4xx counts as reachable: hmd-ms-base only registers /api/... and
// /apiop/... routes, so a 404 from the root still proves the
// proxy -> API Gateway -> Lambda chain is wired.
func TestHTTPProber(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status int
		want   bool
	}{
		{"200 is reachable", http.StatusOK, true},
		{"404 is reachable", http.StatusNotFound, true},
		{"502 is not", http.StatusBadGateway, false},
		{"503 is not", http.StatusServiceUnavailable, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
			}))
			defer srv.Close()
			if got := HTTPProber(2*time.Second)(context.Background(), srv.URL); got != tt.want {
				t.Errorf("HTTPProber() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHTTPProberOnADeadEndpoint(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	if HTTPProber(500*time.Millisecond)(context.Background(), url) {
		t.Error("a closed server reported reachable")
	}
}

// TestHTTPProberRetriesTransientFailure covers a Floci Lambda cold start: its
// idle-eviction reaper runs on its own timer, independent of request timing,
// so a probe landing in the ~1s gap between eviction and the next container
// coming back up must not read that as "the service is down."
func TestHTTPProberRetriesTransientFailure(t *testing.T) {
	t.Parallel()

	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusBadGateway) // container evicted, not yet cold-started
			return
		}
		w.WriteHeader(http.StatusOK) // cold start finished
	}))
	defer srv.Close()

	if !HTTPProber(2*time.Second)(context.Background(), srv.URL) {
		t.Error("HTTPProber gave up before the transient failure cleared")
	}
	if calls < 3 {
		t.Errorf("HTTPProber made %d call(s), want at least 3 (it must retry)", calls)
	}
}

// TestHTTPProberRespectsContextCancellation covers a caller that gives up --
// retrying must not ignore ctx and keep sleeping/retrying regardless.
func TestHTTPProberRespectsContextCancellation(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	if HTTPProber(2*time.Second)(ctx, srv.URL) {
		t.Error("a cancelled context reported reachable")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("HTTPProber took %v after context cancellation, want it to return promptly", elapsed)
	}
}
