package cpext

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/compose"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/repoclass"
)

const projectName = "local_neuronsphere-abc12345"

var platformNetworks = map[string]compose.Network{
	PlatformNetwork: {Name: "neuronsphere_default-abc12345", External: true},
}

// repoClass writes a working tree carrying a version and, when body is
// non-empty, an extension compose file.
func repoClass(t *testing.T, repoHome, class, version, body string) string {
	t.Helper()
	dir := filepath.Join(repoHome, class)
	write(t, filepath.Join(dir, "meta-data", "VERSION"), version)
	if body != "" {
		write(t, filepath.Join(dir, "src", "local", ComposeFile), body)
	}
	return dir
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func manifestFile(t *testing.T, home, body string) {
	t.Helper()
	write(t, filepath.Join(home, ".config", "control-plane.yaml"), body)
}

func env(vars map[string]string) compose.Lookup {
	return func(key string) string { return vars[key] }
}

const registryCompose = `
services:
  server:
    image: ${NS_CONFIG_IMAGE:-devpi:0.1}
    restart: unless-stopped
    volumes:
      - "${NS_EXTENSION_DIR}/data:/data"
    environment:
      NAME: ${NS_EXTENSION_NAME}
    networks: [neuronsphere_default]
  oci:
    image: zot:1
    profiles: ["oci"]
    networks: [neuronsphere_default]
networks:
  neuronsphere_default:
    external: true
    name: ${NEURONSPHERE_DOCKER_NETWORK:-neuronsphere_default}
`

// fixture wires an HMD_HOME and a repo home with one registry extension.
func fixture(t *testing.T, config string) (Options, string) {
	t.Helper()
	home, repos := t.TempDir(), t.TempDir()
	repoClass(t, repos, "hmd-inf-local-registry", "0.1.4", registryCompose)
	manifestFile(t, home, `
version: 1
name: control-plane
repos:
  - instance_name: package-registry
    repo_class_name: hmd-inf-local-registry
`+config)
	return Options{
		Home: home, RepoHome: repos, ProjectName: projectName,
		Lookup:   env(map[string]string{"HMD_REPO_HOME": repos, "NEURONSPHERE_DOCKER_NETWORK": "neuronsphere_default-abc12345"}),
		Networks: platformNetworks,
	}, home
}

// The default NERD004 asks for: no manifest, nothing resolved, no error.
func TestResolveReturnsNothingWithoutAManifest(t *testing.T) {
	t.Parallel()

	exts, err := Resolve(Options{Home: t.TempDir(), ProjectName: projectName})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(exts) != 0 {
		t.Errorf("resolved %d extensions from no manifest", len(exts))
	}
}

func TestResolveReadsTheExtensionComposeFile(t *testing.T) {
	t.Parallel()

	opts, home := fixture(t, "")
	exts, err := Resolve(opts)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(exts) != 1 {
		t.Fatalf("resolved %d extensions", len(exts))
	}
	e := exts[0]
	if e.Failed() {
		t.Fatalf("resolve failed: %v", e.Err)
	}
	if e.Version != "0.1.4" || e.Source != repoclass.SourceWorkingTree {
		t.Errorf("version = %q from %q, want 0.1.4 from a working tree", e.Version, e.Source)
	}
	if want := filepath.Join(home, "package-registry"); e.StateDir != want {
		t.Errorf("StateDir = %q, want %q", e.StateDir, want)
	}
	if len(e.Services) != 2 {
		t.Fatalf("services = %+v", e.Services)
	}
}

// SPEC004: service keys are prefixed, so the container name carries both the
// HMD_HOME digest (via the project) and the instance.
func TestServiceKeysArePrefixedByInstance(t *testing.T) {
	t.Parallel()

	opts, _ := fixture(t, "")
	exts, _ := Resolve(opts)
	got := exts[0].Services[0]
	if got.Key != "package-registry-oci" && got.Key != "package-registry-server" {
		t.Fatalf("key = %q, want a package-registry- prefix", got.Key)
	}
	for _, s := range exts[0].Services {
		if s.Name(projectName) != projectName+"-"+s.Key+"-1" {
			t.Errorf("%s container = %q", s.Key, s.Name(projectName))
		}
	}
}

// The four facts nsctl knows reach the compose file, and interpolation is what
// makes the configuration dynamic (SPEC005).
func TestConfigurationReachesTheComposeFileAsVariables(t *testing.T) {
	t.Parallel()

	opts, home := fixture(t, `    instance_configuration:
      image: devpi:9.9
`)
	exts, _ := Resolve(opts)
	e := exts[0]
	if e.Failed() {
		t.Fatalf("resolve failed: %v", e.Err)
	}
	server := service(t, e, "package-registry-server")
	if server.Image != "devpi:9.9" {
		t.Errorf("image = %q, want the configured one", server.Image)
	}
	if got := server.Environment["NAME"]; got != "package-registry" {
		t.Errorf("NAME = %q, want the instance name", got)
	}
	wantMount := filepath.Join(home, "package-registry", "data")
	if len(server.Volumes) != 1 || server.Volumes[0].Source != wantMount {
		t.Errorf("volumes = %+v, want a bind from %s", server.Volumes, wantMount)
	}
}

// The manifest wins for nsctl's own names: an ambient export must not silently
// beat a checked-in manifest.
func TestManifestConfigurationBeatsTheProcessEnvironment(t *testing.T) {
	t.Parallel()

	opts, _ := fixture(t, `    instance_configuration:
      image: devpi:9.9
`)
	base := opts.Lookup
	opts.Lookup = func(key string) string {
		if key == "NS_CONFIG_IMAGE" {
			return "devpi:from-the-shell"
		}
		return base(key)
	}
	exts, _ := Resolve(opts)
	if got := service(t, exts[0], "package-registry-server").Image; got != "devpi:9.9" {
		t.Errorf("image = %q, want the manifest's value", got)
	}
}

func TestConfigVarsFlattenNestedMaps(t *testing.T) {
	t.Parallel()

	vars := ConfigVars(Extension{
		Instance: "reg", Version: "0.1", StateDir: "/h/reg", RepoDir: "/r/c",
		Config: map[string]any{
			"pypi":    map[string]any{"enabled": true, "upstream": "https://pypi.org"},
			"port":    8080,
			"tags":    []any{"a", "b"},
			"nothing": nil,
			"a-key":   "v",
		},
	})
	for name, want := range map[string]string{
		VarName: "reg", VarVersion: "0.1", VarStateDir: "/h/reg", VarRepoDir: "/r/c",
		"NS_CONFIG_PYPI_ENABLED":  "true",
		"NS_CONFIG_PYPI_UPSTREAM": "https://pypi.org",
		"NS_CONFIG_PORT":          "8080",
		"NS_CONFIG_TAGS":          `["a","b"]`,
		"NS_CONFIG_NOTHING":       "",
		"NS_CONFIG_A_KEY":         "v",
	} {
		if got := vars[name]; got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

// SPEC005's three spellings, including NERD006's own.
func TestActiveProfilesAcceptsEverySpelling(t *testing.T) {
	t.Parallel()

	active := ActiveProfiles("reg", map[string]any{
		"profiles": []any{"listed"},
		"pypi":     map[string]any{"enabled": true},
		"oci":      map[string]any{"enabled": false},
		"npm":      true,
		"go":       false,
		"url":      "http://example",
	})
	for _, want := range []string{"reg-listed", "reg-pypi", "reg-npm"} {
		if !active[want] {
			t.Errorf("%q is not active: %v", want, active)
		}
	}
	for _, notWant := range []string{"reg-oci", "reg-go", "reg-url", "reg-profiles"} {
		if active[notWant] {
			t.Errorf("%q is active and should not be: %v", notWant, active)
		}
	}
}

// Profiles are namespaced, so two extensions using "pypi" do not switch each
// other's containers on.
func TestProfilesAreNamespacedByInstance(t *testing.T) {
	t.Parallel()

	opts, _ := fixture(t, `    instance_configuration:
      oci: {enabled: true}
`)
	exts, _ := Resolve(opts)
	oci := service(t, exts[0], "package-registry-oci")
	if len(oci.Profiles) != 1 || oci.Profiles[0] != "package-registry-oci" {
		t.Fatalf("profiles = %v, want the namespaced one", oci.Profiles)
	}
	if !oci.EnabledBy(Profiles(exts)) {
		t.Error("the enabled component is gated off")
	}
	if oci.EnabledBy(map[string]bool{"oci": true}) {
		t.Error("a bare \"oci\" profile switched another extension's component on")
	}
}

func TestAnUnconfiguredProfileIsOff(t *testing.T) {
	t.Parallel()

	opts, _ := fixture(t, "")
	exts, _ := Resolve(opts)
	if service(t, exts[0], "package-registry-oci").EnabledBy(Profiles(exts)) {
		t.Error("a component nothing enabled is on")
	}
}

func TestResolveRejectsAPinnedContainerName(t *testing.T) {
	t.Parallel()

	opts := brokenFixture(t, `
services:
  server:
    image: devpi:1
    container_name: package_registry
    networks: [neuronsphere_default]
`)
	exts, _ := Resolve(opts)
	assertFailure(t, exts, "container_name")
}

func TestResolveRejectsAPublishedPort(t *testing.T) {
	t.Parallel()

	opts := brokenFixture(t, `
services:
  server:
    image: devpi:1
    ports: ["3141:3141"]
    networks: [neuronsphere_default]
`)
	exts, _ := Resolve(opts)
	assertFailure(t, exts, "host port")
}

// compose.Parse already refuses these; the test is that the refusal reaches
// the reader as this extension's failure rather than as a crash.
func TestResolveRejectsANamedVolume(t *testing.T) {
	t.Parallel()

	opts := brokenFixture(t, `
services:
  server:
    image: devpi:1
    volumes: ["registrydata:/data"]
    networks: [neuronsphere_default]
`)
	exts, _ := Resolve(opts)
	assertFailure(t, exts, "named volumes are not supported")
}

func TestResolveRejectsAnUnknownNetwork(t *testing.T) {
	t.Parallel()

	opts := brokenFixture(t, `
services:
  server:
    image: devpi:1
    networks: [somewhere_else]
`)
	exts, _ := Resolve(opts)
	assertFailure(t, exts, "which the control plane does not declare")
}

// A service naming no network joins the platform network, which is the only
// one it could have meant.
func TestAServiceWithNoNetworkJoinsThePlatformNetwork(t *testing.T) {
	t.Parallel()

	opts := brokenFixture(t, `
services:
  server:
    image: devpi:1
`)
	exts, _ := Resolve(opts)
	if exts[0].Failed() {
		t.Fatalf("resolve failed: %v", exts[0].Err)
	}
	nets := exts[0].Services[0].Networks
	if len(nets) != 1 || nets[0].Name != PlatformNetwork {
		t.Errorf("networks = %+v", nets)
	}
}

func TestResolveNamesAMisnamedComposeFile(t *testing.T) {
	t.Parallel()

	home, repos := t.TempDir(), t.TempDir()
	dir := repoClass(t, repos, "hmd-inf-local-registry", "0.1", "")
	write(t, filepath.Join(dir, "src", "local", "docker-compose.registry.yml"), "services: {}\n")
	manifestFile(t, home, minimalManifest)

	exts, _ := Resolve(Options{Home: home, RepoHome: repos, ProjectName: projectName,
		Lookup: env(map[string]string{"HMD_REPO_HOME": repos}), Networks: platformNetworks})
	assertFailure(t, exts, "docker-compose.registry.yml")
	assertFailure(t, exts, ComposeFile)
}

func TestResolveReportsARepoClassWithNoComposeFile(t *testing.T) {
	t.Parallel()

	home, repos := t.TempDir(), t.TempDir()
	repoClass(t, repos, "hmd-inf-local-registry", "0.1", "")
	manifestFile(t, home, minimalManifest)

	exts, _ := Resolve(Options{Home: home, RepoHome: repos, ProjectName: projectName,
		Lookup: env(map[string]string{"HMD_REPO_HOME": repos}), Networks: platformNetworks})
	assertFailure(t, exts, "contributes no containers")
}

// One broken extension must not take another with it -- the resolve-stage half
// of SPEC006.
func TestOneBrokenExtensionDoesNotAffectAnother(t *testing.T) {
	t.Parallel()

	home, repos := t.TempDir(), t.TempDir()
	repoClass(t, repos, "hmd-inf-local-registry", "0.1.4", registryCompose)
	repoClass(t, repos, "hmd-inf-broken", "0.1", "services:\n  server:\n    image: x\n    container_name: nope\n")
	manifestFile(t, home, `
version: 1
name: control-plane
repos:
  - instance_name: broken
    repo_class_name: hmd-inf-broken
  - instance_name: package-registry
    repo_class_name: hmd-inf-local-registry
`)

	exts, err := Resolve(Options{Home: home, RepoHome: repos, ProjectName: projectName,
		Lookup: env(map[string]string{"HMD_REPO_HOME": repos}), Networks: platformNetworks})
	if err != nil {
		t.Fatalf("Resolve returned a whole-manifest error for one bad extension: %v", err)
	}
	if len(exts) != 2 {
		t.Fatalf("resolved %d extensions, want both", len(exts))
	}
	if !exts[0].Failed() {
		t.Error("the broken extension resolved")
	}
	if exts[1].Failed() {
		t.Errorf("the healthy extension failed because another one did: %v", exts[1].Err)
	}
	// And the healthy one still contributes containers.
	if len(Project(exts, Options{ProjectName: projectName, Networks: platformNetworks}).Services) != 2 {
		t.Error("the healthy extension contributed no services")
	}
}

// A missing checkout is one extension's problem. It must not invalidate the
// manifest, because that would take every other extension down with it --
// which is what SPEC006 forbids, one stage before the containers.
func TestAMissingWorkingTreeFailsOnlyThatExtension(t *testing.T) {
	t.Parallel()

	home, repos := t.TempDir(), t.TempDir()
	repoClass(t, repos, "hmd-inf-local-registry", "0.1.4", registryCompose)
	manifestFile(t, home, `
version: 1
name: control-plane
repos:
  - instance_name: absent
    repo_class_name: hmd-inf-never-checked-out
  - instance_name: package-registry
    repo_class_name: hmd-inf-local-registry
`)

	exts, err := Resolve(Options{Home: home, RepoHome: repos, ProjectName: projectName,
		Lookup: env(map[string]string{"HMD_REPO_HOME": repos}), Networks: platformNetworks})
	if err != nil {
		t.Fatalf("Resolve rejected the whole manifest for one missing checkout: %v", err)
	}
	if len(exts) != 2 {
		t.Fatalf("resolved %d extensions, want both", len(exts))
	}
	if !exts[0].Failed() || !strings.Contains(exts[0].Err.Error(), "no working tree") {
		t.Errorf("absent = %+v, want a missing-tree failure", exts[0])
	}
	if exts[1].Failed() {
		t.Errorf("the checked-out extension failed because another was missing: %v", exts[1].Err)
	}
}

// -- routing ---------------------------------------------------------------

func TestResolveDerivesTheHostAndUpstreamFromOneURL(t *testing.T) {
	t.Parallel()

	opts, _ := fixture(t, `    instance_configuration:
      url: http://registry.local.neuronsphere.io
      upstream: server:3141
`)
	exts, _ := Resolve(opts)
	e := exts[0]
	if e.Failed() {
		t.Fatalf("resolve failed: %v", e.Err)
	}
	if e.Host != "registry.local.neuronsphere.io" {
		t.Errorf("Host = %q", e.Host)
	}
	if want := projectName + "-package-registry-server-1:3141"; e.Upstream != want {
		t.Errorf("Upstream = %q, want %q", e.Upstream, want)
	}
	if got := Hosts(exts); len(got) != 1 || got[0] != e.Host {
		t.Errorf("Hosts = %v", got)
	}
}

func TestRoutingNeedsBothHalves(t *testing.T) {
	for _, tc := range []struct{ name, config, want string }{
		{"url alone", "      url: http://x.example\n", "nothing answers for it"},
		{"upstream alone", "      upstream: server:1\n", "nothing routes to it"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			opts, _ := fixture(t, "    instance_configuration:\n"+tc.config)
			exts, _ := Resolve(opts)
			assertFailure(t, exts, tc.want)
		})
	}
}

func TestUpstreamMustNameADeclaredService(t *testing.T) {
	t.Parallel()

	opts, _ := fixture(t, `    instance_configuration:
      url: http://x.example
      upstream: nosuch:1
`)
	exts, _ := Resolve(opts)
	assertFailure(t, exts, `names service "nosuch"`)
}

func TestVhostsSkipExtensionsThatServeNothing(t *testing.T) {
	t.Parallel()

	opts, _ := fixture(t, "")
	exts, _ := Resolve(opts)
	if got := Vhosts(exts); len(got) != 0 {
		t.Errorf("Vhosts = %+v, want none for an extension with no url", got)
	}
}

// -- helpers ---------------------------------------------------------------

const minimalManifest = `
version: 1
name: control-plane
repos:
  - instance_name: package-registry
    repo_class_name: hmd-inf-local-registry
`

func brokenFixture(t *testing.T, body string) Options {
	t.Helper()
	home, repos := t.TempDir(), t.TempDir()
	repoClass(t, repos, "hmd-inf-local-registry", "0.1", body)
	manifestFile(t, home, minimalManifest)
	return Options{Home: home, RepoHome: repos, ProjectName: projectName,
		Lookup: env(map[string]string{"HMD_REPO_HOME": repos}), Networks: platformNetworks}
}

func assertFailure(t *testing.T, exts []Extension, want string) {
	t.Helper()
	if len(exts) != 1 {
		t.Fatalf("resolved %d extensions", len(exts))
	}
	if !exts[0].Failed() {
		t.Fatalf("resolve succeeded, want a failure mentioning %q", want)
	}
	if !strings.Contains(exts[0].Err.Error(), want) {
		t.Errorf("error %q does not mention %q", exts[0].Err, want)
	}
}

func service(t *testing.T, e Extension, key string) compose.Service {
	t.Helper()
	for _, s := range e.Services {
		if s.Key == key {
			return s
		}
	}
	t.Fatalf("no service %q in %+v", key, e.Services)
	return compose.Service{}
}
