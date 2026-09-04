package compose

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/errdefs"
	"github.com/docker/go-connections/nat"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// port builds a nat.Port for a "<port>/<proto>" string.
func port(spec string) nat.Port { return nat.Port(spec) }

// fakeAPI implements the narrowed Engine API surface. Narrowing is what makes
// the create/start/recreate decision testable without a daemon.
type fakeAPI struct {
	containers map[string]types.ContainerJSON
	images     map[string]bool
	networks   map[string]bool

	created         []string
	started         []string
	stopped         []string
	removed         []string
	pulled          []string
	networksCreated []string

	lastConfig  *container.Config
	lastHost    *container.HostConfig
	lastNetwork *network.NetworkingConfig
}

func newFakeAPI() *fakeAPI {
	return &fakeAPI{
		containers: map[string]types.ContainerJSON{},
		images:     map[string]bool{},
		networks:   map[string]bool{},
	}
}

func (f *fakeAPI) ContainerInspect(_ context.Context, name string) (types.ContainerJSON, error) {
	c, ok := f.containers[name]
	if !ok {
		return types.ContainerJSON{}, errdefs.NotFound(errNotFound{name})
	}
	return c, nil
}

type errNotFound struct{ name string }

func (e errNotFound) Error() string { return "no such container: " + e.name }

func (f *fakeAPI) ContainerCreate(_ context.Context, cfg *container.Config, host *container.HostConfig, net *network.NetworkingConfig, _ *ocispec.Platform, name string) (container.CreateResponse, error) {
	f.created = append(f.created, name)
	f.lastConfig, f.lastHost, f.lastNetwork = cfg, host, net
	f.containers[name] = types.ContainerJSON{
		ContainerJSONBase: &types.ContainerJSONBase{ID: name, State: &types.ContainerState{}},
		Config:            cfg,
	}
	return container.CreateResponse{ID: name}, nil
}

func (f *fakeAPI) ContainerStart(_ context.Context, name string, _ container.StartOptions) error {
	f.started = append(f.started, name)
	return nil
}

func (f *fakeAPI) ContainerStop(_ context.Context, name string, _ container.StopOptions) error {
	if _, ok := f.containers[name]; !ok {
		return errdefs.NotFound(errNotFound{name})
	}
	f.stopped = append(f.stopped, name)
	return nil
}

func (f *fakeAPI) ContainerRemove(_ context.Context, ref string, _ container.RemoveOptions) error {
	// The daemon accepts a name or an id; the runner removes by id.
	for name, c := range f.containers {
		if name == ref || (c.ContainerJSONBase != nil && c.ContainerJSONBase.ID == ref) {
			f.removed = append(f.removed, ref)
			delete(f.containers, name)
			return nil
		}
	}
	return errdefs.NotFound(errNotFound{ref})
}

func (f *fakeAPI) ImageInspectWithRaw(_ context.Context, ref string) (types.ImageInspect, []byte, error) {
	if !f.images[ref] {
		return types.ImageInspect{}, nil, errdefs.NotFound(errNotFound{ref})
	}
	return types.ImageInspect{}, nil, nil
}

func (f *fakeAPI) ImagePull(_ context.Context, ref string, _ image.PullOptions) (io.ReadCloser, error) {
	f.pulled = append(f.pulled, ref)
	f.images[ref] = true
	return io.NopCloser(strings.NewReader("")), nil
}

func (f *fakeAPI) NetworkInspect(_ context.Context, name string, _ network.InspectOptions) (network.Inspect, error) {
	if !f.networks[name] {
		return network.Inspect{}, errdefs.NotFound(errNotFound{name})
	}
	return network.Inspect{Name: name}, nil
}

func (f *fakeAPI) NetworkCreate(_ context.Context, name string, _ network.CreateOptions) (network.CreateResponse, error) {
	f.networksCreated = append(f.networksCreated, name)
	f.networks[name] = true
	return network.CreateResponse{ID: name}, nil
}

// testProject is a small stand-in with the shapes that matter: a published
// range, a read-only bind, a network alias and a profile.
func testProject() *Project {
	return &Project{
		Name: "proj",
		Networks: map[string]Network{
			"neuronsphere_default": {Name: "neuronsphere_default-abc", External: true},
		},
		Services: []Service{
			{
				Key: "proxy", ContainerName: "hmd_proxy", Image: "nginx:stable-alpine",
				Restart:     "unless-stopped",
				Environment: map[string]string{"B": "2", "A": "1"},
				Ports:       []Port{{80, 80, 80, 80, "tcp"}, {19000, 19003, 19000, 19003, "tcp"}},
				Volumes:     []Mount{{Source: "/h/.cache/nginx", Target: "/etc/nginx/ns", ReadOnly: true}},
				Networks:    []NetworkAttachment{{Name: "neuronsphere_default", Aliases: []string{"proxy"}}},
			},
			{
				Key: "gui", ContainerName: "hmd_deployment_gui", Image: "gui:1",
				Profiles: []string{"deployment-gui"},
				Networks: []NetworkAttachment{{Name: "neuronsphere_default"}},
			},
		},
	}
}

// svc looks a service up by key. testProject is hand-built, so indexing it
// would silently depend on declaration order.
func svc(t *testing.T, p *Project, key string) Service {
	t.Helper()
	for _, s := range p.Services {
		if s.Key == key {
			return s
		}
	}
	t.Fatalf("no service %q", key)
	return Service{}
}

func newRunner(f *fakeAPI) *Runner {
	return &Runner{API: f, Out: io.Discard, Err: io.Discard}
}

func TestUpCreatesMissingContainers(t *testing.T) {
	t.Parallel()

	f := newFakeAPI()
	f.images["nginx:stable-alpine"] = true
	p := testProject()

	results, err := newRunner(f).Up(context.Background(), p, map[string]bool{})
	if err != nil {
		t.Fatalf("Up: %v", err)
	}

	byService := map[string]Action{}
	for _, r := range results {
		byService[r.Service] = r.Action
	}
	if byService["proxy"] != ActionCreated {
		t.Errorf("proxy action = %q, want created", byService["proxy"])
	}
	// The GUI's profile is not active.
	if byService["gui"] != ActionSkipped {
		t.Errorf("gui action = %q, want skipped", byService["gui"])
	}
	if len(f.created) != 1 || f.created[0] != "hmd_proxy" {
		t.Errorf("created = %v, want just hmd_proxy", f.created)
	}
}

func TestUpRunsAProfiledServiceWhenItsProfileIsActive(t *testing.T) {
	t.Parallel()

	f := newFakeAPI()
	f.images["nginx:stable-alpine"] = true
	f.images["gui:1"] = true

	_, err := newRunner(f).Up(context.Background(), testProject(), map[string]bool{"deployment-gui": true})
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	if len(f.created) != 2 {
		t.Errorf("created = %v, want both services", f.created)
	}
}

// The warm restart, and the common case: after `env stop` the containers exist
// with an unchanged configuration and must be started in place, not rebuilt.
func TestUpStartsAnUnchangedStoppedContainerInPlace(t *testing.T) {
	t.Parallel()

	p := testProject()
	f := newFakeAPI()
	f.images["nginx:stable-alpine"] = true
	f.containers["hmd_proxy"] = types.ContainerJSON{
		ContainerJSONBase: &types.ContainerJSONBase{ID: "cid", State: &types.ContainerState{Running: false}},
		Config:            &container.Config{Labels: map[string]string{LabelConfigHash: configHash(p, svc(t, p, "proxy"))}},
	}

	results, err := newRunner(f).Up(context.Background(), p, map[string]bool{})
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	for _, r := range results {
		if r.Service == "proxy" && r.Action != ActionStarted {
			t.Errorf("proxy action = %q, want started", r.Action)
		}
	}
	if len(f.created) != 0 || len(f.removed) != 0 {
		t.Errorf("a warm restart rebuilt the container: created=%v removed=%v", f.created, f.removed)
	}
	if len(f.started) != 1 || f.started[0] != "cid" {
		t.Errorf("started = %v, want the existing container id", f.started)
	}
}

func TestUpLeavesARunningContainerAlone(t *testing.T) {
	t.Parallel()

	p := testProject()
	f := newFakeAPI()
	f.containers["hmd_proxy"] = types.ContainerJSON{
		ContainerJSONBase: &types.ContainerJSONBase{ID: "cid", State: &types.ContainerState{Running: true}},
		Config:            &container.Config{Labels: map[string]string{LabelConfigHash: configHash(p, svc(t, p, "proxy"))}},
	}

	results, err := newRunner(f).Up(context.Background(), p, map[string]bool{})
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	for _, r := range results {
		if r.Service == "proxy" && r.Action != ActionRunning {
			t.Errorf("proxy action = %q, want running", r.Action)
		}
	}
	if len(f.started) != 0 {
		t.Errorf("started %v, want nothing touched", f.started)
	}
}

func TestUpRecreatesWhenTheConfigurationChanged(t *testing.T) {
	t.Parallel()

	p := testProject()
	f := newFakeAPI()
	f.images["nginx:stable-alpine"] = true
	f.containers["hmd_proxy"] = types.ContainerJSON{
		ContainerJSONBase: &types.ContainerJSONBase{ID: "cid", State: &types.ContainerState{Running: true}},
		Config:            &container.Config{Labels: map[string]string{LabelConfigHash: "stale"}},
	}

	results, err := newRunner(f).Up(context.Background(), p, map[string]bool{})
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	for _, r := range results {
		if r.Service == "proxy" && r.Action != ActionRecreated {
			t.Errorf("proxy action = %q, want recreated", r.Action)
		}
	}
	if len(f.removed) != 1 || len(f.created) != 1 {
		t.Errorf("removed=%v created=%v, want one of each", f.removed, f.created)
	}
}

// Both front ends must stay interchangeable: `docker compose ps|stop|down` and
// port_validator.py find containers by these labels.
func TestCreatedContainersCarryTheComposeLabels(t *testing.T) {
	t.Parallel()

	f := newFakeAPI()
	f.images["nginx:stable-alpine"] = true
	if _, err := newRunner(f).Up(context.Background(), testProject(), map[string]bool{}); err != nil {
		t.Fatal(err)
	}

	labels := f.lastConfig.Labels
	want := map[string]string{
		LabelProject:         "proj",
		LabelService:         "proxy",
		LabelContainerNumber: "1",
		LabelOneOff:          "False",
	}
	for k, v := range want {
		if labels[k] != v {
			t.Errorf("label %s = %q, want %q", k, labels[k], v)
		}
	}
	if labels[LabelConfigHash] == "" {
		t.Error("no config hash label; every start would then recreate")
	}
}

func TestPortRangesExpandToIndividualBindings(t *testing.T) {
	t.Parallel()

	f := newFakeAPI()
	f.images["nginx:stable-alpine"] = true
	if _, err := newRunner(f).Up(context.Background(), testProject(), map[string]bool{}); err != nil {
		t.Fatal(err)
	}

	// One single plus a four-wide range.
	if got, want := len(f.lastHost.PortBindings), 5; got != want {
		t.Errorf("got %d port bindings, want %d: %v", got, want, f.lastHost.PortBindings)
	}
	for _, p := range []string{"80/tcp", "19000/tcp", "19003/tcp"} {
		if _, ok := f.lastHost.PortBindings[port(p)]; !ok {
			t.Errorf("no binding for %s", p)
		}
	}
	if got := f.lastHost.PortBindings[port("19003/tcp")][0].HostPort; got != "19003" {
		t.Errorf("19003 maps to host port %q, want 19003", got)
	}
}

func TestBindsAndRestartPolicy(t *testing.T) {
	t.Parallel()

	f := newFakeAPI()
	f.images["nginx:stable-alpine"] = true
	if _, err := newRunner(f).Up(context.Background(), testProject(), map[string]bool{}); err != nil {
		t.Fatal(err)
	}

	if len(f.lastHost.Binds) != 1 || f.lastHost.Binds[0] != "/h/.cache/nginx:/etc/nginx/ns:ro" {
		t.Errorf("binds = %v, want the read-only fragment directory", f.lastHost.Binds)
	}
	if f.lastHost.RestartPolicy.Name != container.RestartPolicyUnlessStopped {
		t.Errorf("restart policy = %q, want unless-stopped", f.lastHost.RestartPolicy.Name)
	}
}

// The network key in the compose file is not the real Docker network name --
// the file declares `name: ${NEURONSPHERE_DOCKER_NETWORK:-...}`. Attaching to
// the key would put the container on a different network from everything else.
func TestContainersAttachToTheResolvedNetworkName(t *testing.T) {
	t.Parallel()

	f := newFakeAPI()
	f.images["nginx:stable-alpine"] = true
	if _, err := newRunner(f).Up(context.Background(), testProject(), map[string]bool{}); err != nil {
		t.Fatal(err)
	}

	ep, ok := f.lastNetwork.EndpointsConfig["neuronsphere_default-abc"]
	if !ok {
		t.Fatalf("not attached to the resolved network: %v", f.lastNetwork.EndpointsConfig)
	}
	if len(ep.Aliases) != 1 || ep.Aliases[0] != "proxy" {
		t.Errorf("aliases = %v, want [proxy]", ep.Aliases)
	}
}

func TestEnvironmentIsSortedSoTheHashIsStable(t *testing.T) {
	t.Parallel()

	f := newFakeAPI()
	f.images["nginx:stable-alpine"] = true
	if _, err := newRunner(f).Up(context.Background(), testProject(), map[string]bool{}); err != nil {
		t.Fatal(err)
	}
	if len(f.lastConfig.Env) != 2 || f.lastConfig.Env[0] != "A=1" || f.lastConfig.Env[1] != "B=2" {
		t.Errorf("env = %v, want sorted", f.lastConfig.Env)
	}
}

// A present image is not re-pulled, even on a floating tag. That is what makes
// an offline start work; --upgrade is the explicit way to ask for newer.
func TestImageIsPulledOnlyWhenAbsent(t *testing.T) {
	t.Parallel()

	f := newFakeAPI()
	if _, err := newRunner(f).Up(context.Background(), testProject(), map[string]bool{}); err != nil {
		t.Fatal(err)
	}
	if len(f.pulled) != 1 || f.pulled[0] != "nginx:stable-alpine" {
		t.Errorf("pulled = %v, want the missing image", f.pulled)
	}

	f2 := newFakeAPI()
	f2.images["nginx:stable-alpine"] = true
	if _, err := newRunner(f2).Up(context.Background(), testProject(), map[string]bool{}); err != nil {
		t.Fatal(err)
	}
	if len(f2.pulled) != 0 {
		t.Errorf("pulled = %v with the image already local", f2.pulled)
	}
}

func TestEnsureNetworkIsIdempotent(t *testing.T) {
	t.Parallel()

	f := newFakeAPI()
	r := newRunner(f)
	if err := r.EnsureNetwork(context.Background(), "net"); err != nil {
		t.Fatal(err)
	}
	if err := r.EnsureNetwork(context.Background(), "net"); err != nil {
		t.Fatal(err)
	}
	if len(f.networksCreated) != 1 {
		t.Errorf("created the network %d times, want once", len(f.networksCreated))
	}
}

// `down` is a stop, not a teardown: the next start restarts in place and takes
// the reconcile fast path.
func TestStopDoesNotRemove(t *testing.T) {
	t.Parallel()

	p := testProject()
	f := newFakeAPI()
	f.containers["hmd_proxy"] = types.ContainerJSON{
		ContainerJSONBase: &types.ContainerJSONBase{ID: "cid"}, Config: &container.Config{},
	}

	if err := newRunner(f).Stop(context.Background(), p, nil); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if len(f.removed) != 0 {
		t.Errorf("Stop removed %v", f.removed)
	}
	if len(f.stopped) != 1 || f.stopped[0] != "hmd_proxy" {
		t.Errorf("stopped = %v", f.stopped)
	}
}

func TestStopToleratesAMissingContainer(t *testing.T) {
	t.Parallel()

	if err := newRunner(newFakeAPI()).Stop(context.Background(), testProject(), nil); err != nil {
		t.Errorf("Stop on an absent container: %v", err)
	}
}

func TestConfigHashChangesWithTheConfiguration(t *testing.T) {
	t.Parallel()

	p := testProject()
	base := configHash(p, svc(t, p, "proxy"))

	if again := configHash(p, svc(t, p, "proxy")); again != base {
		t.Error("the hash is not stable across calls")
	}

	tests := []struct {
		name   string
		mutate func(*Service)
	}{
		{"a different image", func(s *Service) { s.Image = "nginx:other" }},
		{"a different environment value", func(s *Service) { s.Environment = map[string]string{"A": "changed"} }},
		{"a different bind", func(s *Service) { s.Volumes = []Mount{{Source: "/other", Target: "/t"}} }},
		{"a different port", func(s *Service) { s.Ports = []Port{{81, 81, 81, 81, "tcp"}} }},
		{"a different restart policy", func(s *Service) { s.Restart = "always" }},
		{"a different command", func(s *Service) { s.Command = []string{"sh"} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := svc(t, p, "proxy")
			tt.mutate(&s)
			if configHash(p, s) == base {
				t.Errorf("%s did not change the hash, so the container would never be recreated", tt.name)
			}
		})
	}
}

// A renamed network has to force a recreate, or containers stay on the old one.
func TestConfigHashFollowsTheResolvedNetworkName(t *testing.T) {
	t.Parallel()

	p := testProject()
	base := configHash(p, svc(t, p, "proxy"))

	renamed := testProject()
	renamed.Networks["neuronsphere_default"] = Network{Name: "neuronsphere_default-other", External: true}
	if configHash(renamed, renamed.Services[1]) == base {
		t.Error("renaming the network did not change the hash")
	}
}
