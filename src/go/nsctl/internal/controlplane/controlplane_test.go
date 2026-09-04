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
			if got := GUIPort(opts); got != tt.wantPort {
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

	local := "hmd-app-neuronsphere:" + GUIImageVersion
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

	opts := testOptions("", map[string]string{"HMD_LOCAL_VERSION_HMD_APP_NEURONSPHERE": "9.9.9"})
	got := DeploymentGUIImage(context.Background(), opts, func(context.Context, string) bool { return false })
	if !strings.HasSuffix(got, ":9.9.9") {
		t.Errorf("got %q, want the overridden version", got)
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
		{"defaults", nil, "ghcr.io/neuronsphere/hmd-img-projectbuilder:stable"},
		{"a pinned version", map[string]string{"HMD_PROJECTBUILDER_VERSION": "0.9"}, "ghcr.io/neuronsphere/hmd-img-projectbuilder:0.9"},
		{"a registry override", map[string]string{"HMD_LOCAL_NS_CONTAINER_REGISTRY": "my.registry"}, "my.registry/hmd-img-projectbuilder:stable"},
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
