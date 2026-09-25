package controlplane

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bundled"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/hosturl"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
)

func testOptions(home string, env map[string]string) *Options {
	return &Options{
		Home:   home,
		Lookup: func(key string) string { return env[key] },
		Out:    io.Discard,
		Err:    io.Discard,
	}
}

// Without the hosts entries every presigned URL Floci hands back is unusable
// from the host, and the failure surfaces far away as a connection refused
// inside some unrelated tool.
// The notice has to say what is actually degraded and name both remedies. An
// instruction to edit /etc/hosts with no statement of what breaks without it is
// what made this look mandatory when it never was (NERD025 SPEC004).
func TestTheHostNamesNoticeNamesBothRemedies(t *testing.T) {
	t.Parallel()

	got := hostNamesNotice([]string{"neuronsphere"})
	for _, want := range []string{
		"neuronsphere",
		"nsctl dns install",
		"/etc/hosts",
		"127.0.0.1 neuronsphere neuronsphere-workload",
		"hmd build",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("notice should mention %q, got:\n%s", want, got)
		}
	}
	// It must not read as a precondition of starting.
	for _, unwanted := range []string{"and try again", "must resolve"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("notice still reads as a gate (%q), got:\n%s", unwanted, got)
		}
	}
}

func TestCheckHostsEntries(t *testing.T) {
	t.Parallel()

	loopback := func(string) ([]net.IP, error) { return []net.IP{net.ParseIP("127.0.0.1")}, nil }
	elsewhere := func(string) ([]net.IP, error) { return []net.IP{net.ParseIP("10.1.2.3")}, nil }
	unresolvable := func(string) ([]net.IP, error) { return nil, errors.New("no such host") }

	if err := CheckHostsEntries(loopback); err != nil {
		t.Errorf("loopback entries were rejected: %v", err)
	}

	for _, tt := range []struct {
		name    string
		resolve Resolver
	}{
		{"unresolvable", unresolvable},
		{"resolves somewhere else", elsewhere},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := CheckHostsEntries(tt.resolve)
			if err == nil {
				t.Fatal("the pre-flight passed")
			}
			if got := nserr.CodeOf(err); got != nserr.Usage {
				t.Errorf("exit code = %d, want %d", got, nserr.Usage)
			}
			// The refusal has to carry the line to paste.
			if !strings.Contains(err.Error(), "127.0.0.1 neuronsphere neuronsphere-workload") {
				t.Errorf("error does not carry the /etc/hosts line:\n%s", err)
			}
		})
	}
}

// That state cannot be merged into the shared Floci, and ignoring it would make
// an environment's Lambdas, gateways and secrets appear to have vanished while
// the start reported success.
func TestCheckNoLegacyEnvFlociState(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	stateDir := filepath.Join(home, "envs", "dev")
	flociData := filepath.Join(stateDir, "floci", "data")
	if err := os.MkdirAll(flociData, 0o755); err != nil {
		t.Fatal(err)
	}

	reg := &registry.Registry{Environments: map[string]registry.Environment{
		"dev": {Slug: "dev", StateDir: stateDir},
	}}

	// Empty is fine.
	if err := CheckNoLegacyEnvFlociState(reg); err != nil {
		t.Errorf("an empty Floci data dir was rejected: %v", err)
	}

	if err := os.WriteFile(filepath.Join(flociData, "s3.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := CheckNoLegacyEnvFlociState(reg)
	if err == nil {
		t.Fatal("stale per-environment Floci state was accepted")
	}
	if !strings.Contains(err.Error(), "dev") || !strings.Contains(err.Error(), "--purge") {
		t.Errorf("error does not name the environment and the remedy:\n%s", err)
	}
}

// A legacy-layout environment's own Floci data dir *is* the control plane's,
// which is exactly where its state belongs.
func TestCheckNoLegacyEnvFlociStateExemptsALegacyEnvironment(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	flociData := filepath.Join(home, "floci", "data")
	if err := os.MkdirAll(flociData, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(flociData, "s3.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := &registry.Registry{Environments: map[string]registry.Environment{
		"local": {Slug: "local", StateDir: home, LegacyLayout: true},
	}}
	if err := CheckNoLegacyEnvFlociState(reg); err != nil {
		t.Errorf("a legacy-layout environment was rejected: %v", err)
	}
}

func TestGUIEnabledAndPort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		env         map[string]string
		wantEnabled bool
		wantPort    int
	}{
		{"defaults", nil, true, DefaultGUIPort},
		{"explicitly false", map[string]string{"HMD_LOCAL_NEURONSPHERE_ENABLE_GUI": "false"}, false, DefaultGUIPort},
		{"zero is falsy", map[string]string{"HMD_LOCAL_NEURONSPHERE_ENABLE_GUI": "0"}, false, DefaultGUIPort},
		{"anything else is enabled", map[string]string{"HMD_LOCAL_NEURONSPHERE_ENABLE_GUI": "yes"}, true, DefaultGUIPort},
		{"a port override", map[string]string{"HMD_LOCAL_GUI_HOST_PORT": "18080"}, true, 18080},
		{"garbage falls back", map[string]string{"HMD_LOCAL_GUI_HOST_PORT": "nope"}, true, DefaultGUIPort},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			opts := testOptions("", tt.env)
			if got := GUIEnabled(opts); got != tt.wantEnabled {
				t.Errorf("GUIEnabled = %v, want %v", got, tt.wantEnabled)
			}
			if got := GUIPort(opts, nil); got != tt.wantPort {
				t.Errorf("GUIPort = %d, want %d", got, tt.wantPort)
			}
		})
	}
}

// The GUI is profile-gated, which is what HMD_LOCAL_NEURONSPHERE_ENABLE_GUI
// turns off.
func TestActiveProfilesGateTheGUI(t *testing.T) {
	t.Parallel()

	if !ActiveProfiles(testOptions("", nil))[GUIProfile] {
		t.Error("the GUI profile is inactive by default")
	}
	if ActiveProfiles(testOptions("", map[string]string{"HMD_LOCAL_NEURONSPHERE_ENABLE_GUI": "false"}))[GUIProfile] {
		t.Error("the GUI profile is active with the GUI disabled")
	}
}

// The compose file interpolates HMD_HOME and the network name, and the network
// must be the persisted one -- not one re-derived from a hash.
func TestComposeEnvSuppliesTheRegistrysNetwork(t *testing.T) {
	t.Parallel()

	reg := &registry.Registry{ControlPlane: registry.ControlPlane{Network: "neuronsphere_default-57aa833c"}}
	lookup := ComposeEnv(testOptions("/home/hmd", nil), reg, "gui:1.2.3")

	if got := lookup("HMD_HOME"); got != "/home/hmd" {
		t.Errorf("HMD_HOME = %q", got)
	}
	if got := lookup("NEURONSPHERE_DOCKER_NETWORK"); got != "neuronsphere_default-57aa833c" {
		t.Errorf("network = %q, want the registry's", got)
	}
	if got := lookup("HMD_DEPLOYMENT_GUI_IMAGE"); got != "gui:1.2.3" {
		t.Errorf("GUI image = %q", got)
	}
}

// The process environment wins, so a developer override still applies.
func TestComposeEnvLetsTheProcessEnvironmentWin(t *testing.T) {
	t.Parallel()

	reg := &registry.Registry{ControlPlane: registry.ControlPlane{Network: "from-registry"}}
	opts := testOptions("/home/hmd", map[string]string{"NEURONSPHERE_DOCKER_NETWORK": "from-shell"})
	if got := ComposeEnv(opts, reg, "")("NEURONSPHERE_DOCKER_NETWORK"); got != "from-shell" {
		t.Errorf("network = %q, want the process value", got)
	}
}

// Except HMD_HOME: `--home nsdemo` with HMD_HOME=hmdtr1 exported in the same
// shell used to hand compose hmdtr1's Floci data dir and network, so the
// "fresh" control plane silently ran the other home's Floci.
func TestComposeEnvHomeIsTheCommandsNotTheShells(t *testing.T) {
	t.Parallel()

	reg := &registry.Registry{ControlPlane: registry.ControlPlane{Network: "n"}}
	opts := testOptions("/home/nsdemo", map[string]string{"HMD_HOME": "/home/hmdtr1"})
	if got := ComposeEnv(opts, reg, "")("HMD_HOME"); got != "/home/nsdemo" {
		t.Errorf("HMD_HOME = %q, want the command's home, not the shell's", got)
	}
}

// An empty GUI image leaves the compose file's own :stable default in play,
// which is degraded but not a reason to fail a start.
func TestComposeEnvOmitsAnEmptyGUIImage(t *testing.T) {
	t.Parallel()

	lookup := ComposeEnv(testOptions("/h", nil), &registry.Registry{}, "")
	if got := lookup("HMD_DEPLOYMENT_GUI_IMAGE"); got != "" {
		t.Errorf("GUI image = %q, want empty so the compose default applies", got)
	}
}

// A locally cached image wins over a published one, so `hmd build` in the app
// repo is enough to iterate on the GUI.
func TestDeploymentGUIImagePrefersALocalBuild(t *testing.T) {
	t.Parallel()

	local := "hmd-app-neuronsphere-core:" + GUIImageVersion
	present := func(_ context.Context, ref string) bool { return ref == local }

	if got := DeploymentGUIImage(context.Background(), testOptions("", nil), present); got != local {
		t.Errorf("got %q, want the locally cached %q", got, local)
	}
}

func TestDeploymentGUIImageFallsThroughToThePublishedRef(t *testing.T) {
	t.Parallel()

	want := GUIPublishedRegistry + "/" + GUIRepoClass + ":" + GUIImageVersion
	got := DeploymentGUIImage(context.Background(), testOptions("", nil), func(context.Context, string) bool { return false })
	if got != want {
		t.Errorf("got %q, want the published ref %q", got, want)
	}
	// Never the compose file's floating :stable default -- the pin is the point.
	if strings.HasSuffix(got, ":stable") {
		t.Errorf("resolved to a floating tag: %q", got)
	}
}

func TestDeploymentGUIImageHonoursTheVersionOverride(t *testing.T) {
	t.Parallel()

	opts := testOptions("", map[string]string{"HMD_LOCAL_VERSION_HMD_APP_NEURONSPHERE_CORE": "9.9.9"})
	got := DeploymentGUIImage(context.Background(), opts, func(context.Context, string) bool { return false })
	if !strings.HasSuffix(got, ":9.9.9") {
		t.Errorf("got %q, want the overridden version", got)
	}
}

// The premium overlay is a different image (FROM the core), so running it
// locally is a full reference, not a version of the core class.
func TestDeploymentGUIImageHonoursAFullRefOverride(t *testing.T) {
	t.Parallel()

	premium := "ghcr.io/hmdlabs/hmd-app-neuronsphere:0.1.100"
	opts := testOptions("", map[string]string{"HMD_LOCAL_IMAGE_HMD_APP_NEURONSPHERE": premium})
	got := DeploymentGUIImage(context.Background(), opts, func(context.Context, string) bool { return false })
	if got != premium {
		t.Errorf("got %q, want the override %q", got, premium)
	}
}

// The order mirrors image_cache.image_candidates, which is the single source of
// the order floci_deployer.resolve_image_uri resolves in.
func TestImageCandidatesOrder(t *testing.T) {
	t.Parallel()

	opts := testOptions("", map[string]string{
		"HMD_CONTAINER_REGISTRY":          "build.registry",
		"HMD_LOCAL_NS_CONTAINER_REGISTRY": "local.registry",
		"HMD_LOCAL_IMAGE_PULL_REGISTRIES": "extra.one, extra.two",
	})
	got := ImageCandidates(opts, "repo", "1.0")
	want := []string{
		"build.registry/repo:1.0",
		"local.registry/repo:1.0",
		"extra.one/repo:1.0",
		"extra.two/repo:1.0",
		PublishedRegistryDefault + "/repo:1.0",
		"repo:1.0",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("candidate %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestImageCandidatesSkipsUnsetPrefixesAndDeduplicates(t *testing.T) {
	t.Parallel()

	opts := testOptions("", map[string]string{
		"HMD_CONTAINER_REGISTRY":          "same.registry",
		"HMD_LOCAL_NS_CONTAINER_REGISTRY": "same.registry",
	})
	got := ImageCandidates(opts, "repo", "1.0")
	want := []string{"same.registry/repo:1.0", PublishedRegistryDefault + "/repo:1.0", "repo:1.0"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestProjectBuilderRef(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		// The default is hmdlabs and an exact patch. Spelt out rather than
		// built from the constants, so moving a pin has to be deliberate: the
		// previous default named a registry and a tag that existed nowhere,
		// and a test written against the constants would have agreed with it.
		{"defaults", nil, "ghcr.io/hmdlabs/hmd-img-projectbuilder:0.5.389"},
		{"a pinned version", map[string]string{"HMD_PROJECTBUILDER_VERSION": "0.9"}, "ghcr.io/hmdlabs/hmd-img-projectbuilder:0.9"},
		{"a registry override", map[string]string{"HMD_LOCAL_NS_CONTAINER_REGISTRY": "my.registry"}, "my.registry/hmd-img-projectbuilder:0.5.389"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ProjectBuilderRef(testOptions("", tt.env)); got != tt.want {
				t.Errorf("ProjectBuilderRef = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestUniqueValuesIsSortedAndDeduplicated(t *testing.T) {
	t.Parallel()

	// A gateway fronting two service names must be deployed once.
	got := uniqueValues(map[string]string{"a": "id2", "b": "id1", "c": "id1"})
	if len(got) != 2 || got[0] != "id1" || got[1] != "id2" {
		t.Errorf("uniqueValues = %v, want [id1 id2]", got)
	}
}

// fakeFlociDocker is the narrow dockerClient double for stopFlociSpawned:
// RDS and Neptune are not compose services, so the only way to find them is
// the floci label, and the only way to see this sweep ran is to record what
// it stopped.
type fakeFlociDocker struct {
	labelled map[string][]string
	// onNetwork is keyed "key=value@network", the shape
	// ContainersWithLabelOnNetwork is asked for.
	onNetwork  map[string][]string
	running    map[string]bool
	runningErr map[string]error
	stopped    []string
	stopErr    map[string]error
}

func (f *fakeFlociDocker) ContainersWithLabelOnNetwork(_ context.Context, key, value, network string) []string {
	return f.onNetwork[key+"="+value+"@"+network]
}

func (f *fakeFlociDocker) Running(_ context.Context, name string) (bool, error) {
	if err := f.runningErr[name]; err != nil {
		return false, err
	}
	return f.running[name], nil
}

func (f *fakeFlociDocker) ContainersWithLabel(_ context.Context, key, value string) []string {
	filter := key
	if value != "" {
		filter = key + "=" + value
	}
	return f.labelled[filter]
}

func (f *fakeFlociDocker) Stop(_ context.Context, name string) error {
	f.stopped = append(f.stopped, name)
	return f.stopErr[name]
}

func TestStopFlociSpawnedStopsEveryFlociLabelledContainer(t *testing.T) {
	t.Parallel()

	d := &fakeFlociDocker{labelled: map[string][]string{
		"floci": {"floci-rds-db-abc123", "floci-neptune-def456"},
	}}
	stopFlociSpawned(context.Background(), testOptions("", nil), d)

	want := []string{"floci-rds-db-abc123", "floci-neptune-def456"}
	if len(d.stopped) != len(want) {
		t.Fatalf("stopped = %v, want %v", d.stopped, want)
	}
	for i, name := range want {
		if d.stopped[i] != name {
			t.Errorf("stopped[%d] = %q, want %q", i, d.stopped[i], name)
		}
	}
}

func TestStopFlociSpawnedWarnsRatherThanStopsOnAFailure(t *testing.T) {
	t.Parallel()

	var errBuf strings.Builder
	opts := &Options{Home: "", Lookup: func(string) string { return "" }, Out: io.Discard, Err: &errBuf}
	d := &fakeFlociDocker{
		labelled: map[string][]string{"floci": {"floci-rds-db-abc123", "floci-neptune-def456"}},
		stopErr:  map[string]error{"floci-rds-db-abc123": errors.New("container is restarting")},
	}
	stopFlociSpawned(context.Background(), opts, d)

	// Both attempted, in order -- one failing container must not stop the sweep.
	if len(d.stopped) != 2 {
		t.Fatalf("stopped = %v, want both containers attempted", d.stopped)
	}
	if !strings.Contains(errBuf.String(), "container is restarting") {
		t.Errorf("Err = %q, want it to report the stop failure", errBuf.String())
	}
}

// fakeProxy answers whether hmd_proxy is up, and what it said on the way down.
type fakeProxy struct {
	running bool
	err     error
	logs    string
}

func (f fakeProxy) Running(context.Context, string) (bool, error) { return f.running, f.err }
func (f fakeProxy) Logs(context.Context, string, int) string      { return f.logs }

// A dead proxy must be reported as a dead proxy.
//
// Floci publishes no host port of its own -- :4566 is an nginx stream listener
// -- so its health check runs through hmd_proxy. When the proxy could not start
// on a stale route fragment, the start spent five minutes reporting "Floci at
// http://localhost:4566 is not ready", against a Floci that was healthy and
// logging normally the whole time.
func TestAProxyThatCannotStartIsNamedRatherThanBlamedOnFloci(t *testing.T) {
	t.Parallel()

	opts := testOptions("/tmp/home", nil)
	err := checkProxyStarted(context.Background(), opts,
		fakeProxy{running: false, logs: `nginx: [emerg] unknown "ns_auth" variable`})
	if err == nil {
		t.Fatal("checkProxyStarted() succeeded with the proxy down")
	}
	// nginx names the problem; the error has to carry its words, say why this
	// stops everything else, and point at the fragments. A diagnosis with no
	// next step is what the Floci timeout already was.
	for _, want := range []string{"hmd_proxy", "ns_auth", "Floci", ".cache/nginx"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q: %v", want, err)
		}
	}
}

// A running proxy, an unreadable answer, and no client at all are all fine:
// this gates a start, so anything indeterminate must not refuse one.
func TestTheProxyCheckRefusesOnlyWhenItKnows(t *testing.T) {
	t.Parallel()

	opts := testOptions("/tmp/home", nil)
	for _, tt := range []struct {
		name string
		d    proxyInspector
	}{
		{"running", fakeProxy{running: true}},
		{"cannot tell", fakeProxy{err: errors.New("docker is not answering")}},
		{"no client", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := checkProxyStarted(context.Background(), opts, tt.d); err != nil {
				t.Errorf("checkProxyStarted() = %v, want nil", err)
			}
		})
	}
}

// A refusal that states a precondition without naming what satisfies it is
// incomplete (NERD023 SPEC002). The port refusal names every override, and each
// one has to be a variable something actually reads -- a remedy naming a
// variable nothing honours is worse than no remedy.
//
// Asserted against the readers rather than against literals. The old version of
// this test spelled the names out a second time, which proves only that two
// strings in the test file agree with each other: renaming the variable a reader
// honours would have left it green.
func TestThePortRemedyNamesRealOverrides(t *testing.T) {
	t.Parallel()

	// Read by Go. The constants are the same identifiers the readers use --
	// registry.portBase, GUIPort, DNSPort -- so a rename cannot desynchronise
	// them.
	for _, name := range []string{registry.EnvPortBaseEnv, registry.GUIPortEnv, DNSPortEnv} {
		if !strings.Contains(portRemedy, name) {
			t.Errorf("the remedy does not name %s:\n%s", name, portRemedy)
		}
	}

	// Read by the bundled compose file, which is the other kind of reader. This
	// is the half that could genuinely drift, so it is checked against the file.
	data, err := bundled.Read(bundled.ControlPlaneComposeFile)
	if err != nil {
		t.Fatalf("reading the bundled compose file: %v", err)
	}
	for _, name := range []string{
		registry.EnvPortRangeEnv, registry.TrinoPortEnv, registry.GUIPortEnv,
		registry.HTTPPortEnv, registry.FlociPortEnv, DNSPortEnv,
	} {
		if !strings.Contains(portRemedy, name) {
			t.Errorf("the remedy does not name %s:\n%s", name, portRemedy)
		}
		if !strings.Contains(string(data), name) {
			t.Errorf("%s is named in the remedy but the compose file never reads it", name)
		}
	}

	// 80 and 4566 were once fixed and the refusal said so. Since NERD025 SPEC008
	// they are chosen like the rest, so a remedy still calling them fixed sends
	// the reader to free a port that nsctl would have moved off by itself.
	if strings.Contains(portRemedy, "fixed") {
		t.Errorf("the remedy still claims a port is fixed:\n%s", portRemedy)
	}
}

// A port this home recorded is a port something else on the machine already
// holds, so the config writers have to read it back. They did not: compose
// published reg.ControlPlane.Port(PortDNS) while nginx listened on whatever the
// process environment said, so a home whose resolver had moved published one
// port and streamed to another -- and the hostname silently stopped resolving
// (NERD025 SPEC008, NERD026 SPEC001).
func TestTheResolverPortIsReadBackFromTheRegistry(t *testing.T) {
	t.Parallel()

	reg := &registry.Registry{ControlPlane: registry.ControlPlane{
		Ports: map[string]int{registry.PortDNS: 19154},
	}}

	if got := DNSPort(testOptions("", nil), reg); got != 19154 {
		t.Errorf("DNSPort = %d, want the recorded 19154", got)
	}
	// The user's own statement still wins over what was probed.
	over := testOptions("", map[string]string{DNSPortEnv: "15353"})
	if got := DNSPort(over, reg); got != 15353 {
		t.Errorf("DNSPort with an override = %d, want 15353", got)
	}
	// A home that recorded nothing keeps the historical port.
	if got := DNSPort(testOptions("", nil), &registry.Registry{}); got != DefaultDNSPort {
		t.Errorf("DNSPort with an empty registry = %d, want %d", got, DefaultDNSPort)
	}
}

// The Deployment GUI's port has the same shape, and one extra hazard: it was
// published only because 19003 fell inside the environment band, so a moved band
// took the GUI off the host with no sign but a refused connection.
func TestTheGUIPortIsReadBackFromTheRegistry(t *testing.T) {
	t.Parallel()

	reg := &registry.Registry{ControlPlane: registry.ControlPlane{
		Ports: map[string]int{registry.PortGUI: 19004},
	}}

	if got := GUIPort(testOptions("", nil), reg); got != 19004 {
		t.Errorf("GUIPort = %d, want the recorded 19004", got)
	}
	over := testOptions("", map[string]string{"HMD_LOCAL_GUI_HOST_PORT": "19999"})
	if got := GUIPort(over, reg); got != 19999 {
		t.Errorf("GUIPort with an override = %d, want 19999", got)
	}
	if got := GUIPort(testOptions("", nil), &registry.Registry{}); got != DefaultGUIPort {
		t.Errorf("GUIPort with an empty registry = %d, want %d", got, DefaultGUIPort)
	}
}

// hosturl and the dial redirect are resolved once per process, from the registry
// as it stood before the command ran (cmd/root.go applyHostPorts). A start that
// *moves* a port invalidates both: every URL the rest of the start prints would
// name the old port, and the redirect would aim Floci's presigned URLs at a port
// nothing publishes any more. "Resolved once per process" is right; resolved
// before the ports were chosen is not (NERD025 SPEC008).
func TestTheChosenPortsReachTheURLsThisStartPrints(t *testing.T) {
	hosturl.Reset()
	defer hosturl.Reset()

	reg := &registry.Registry{ControlPlane: registry.ControlPlane{
		Ports: map[string]int{registry.PortHTTP: 8080, registry.PortFloci: 14566},
	}}

	applyChosenPorts(reg)

	if got := hosturl.Base(); got != "http://localhost:8080" {
		t.Errorf("hosturl.Base after the ports moved = %q, want :8080", got)
	}
	if got := hosturl.Floci(); got != "http://localhost:14566" {
		t.Errorf("hosturl.Floci after the ports moved = %q, want :14566", got)
	}
	if got := MSDeploymentURL(); got != "http://localhost:8080/hmd_ms_deployment" {
		t.Errorf("the deployment route = %q, want the moved port", got)
	}
}
