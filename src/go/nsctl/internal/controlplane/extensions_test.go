package controlplane

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/compose"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/cpext"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/hmdenv"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/router"
)

// cpext names the platform network itself, because an extension is parsed
// before the core project is available in every caller. This is the check that
// keeps the copy honest rather than trusted.
func TestBundledProjectDeclaresThePlatformNetwork(t *testing.T) {
	t.Parallel()

	reg := &registry.Registry{ControlPlane: registry.ControlPlane{
		ComposeProject: "local_neuronsphere-abc12345",
		Network:        "neuronsphere_default-abc12345",
	}}
	project, err := Project(testOptions(t.TempDir(), nil), reg, "")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if _, ok := project.Networks[cpext.PlatformNetwork]; !ok {
		t.Fatalf("the bundled compose file no longer declares a network keyed %q; it has %v",
			cpext.PlatformNetwork, keys(project.Networks))
	}
}

// Likewise for the service keys internal/manifest refuses as instance names.
func TestReservedNamesCoverTheBundledServiceKeys(t *testing.T) {
	t.Parallel()

	reg := &registry.Registry{ControlPlane: registry.ControlPlane{ComposeProject: "p", Network: "n"}}
	project, err := Project(testOptions(t.TempDir(), nil), reg, "")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	for _, s := range project.Services {
		if !manifest.ScopeControlPlane.Reserved(s.Key) {
			t.Errorf("the bundled compose file has a service keyed %q, which a control-plane manifest "+
				"would accept as an instance name", s.Key)
		}
	}
}

// The proxy is the only thing that answers for an extension's hostname, so a
// sibling container has to resolve the name to it. The alias must be on the
// project *before* it starts, or the config hash does not cover it and the
// proxy is never recreated to pick it up.
func TestAliasExtensionsAddsHostnamesToTheProxy(t *testing.T) {
	t.Parallel()

	reg := &registry.Registry{ControlPlane: registry.ControlPlane{
		ComposeProject: "p", Network: "neuronsphere_default-abc",
	}}
	opts := testOptions(t.TempDir(), nil)
	project, err := Project(opts, reg, "")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	before := hashOf(t, project, compose.ProxyService)

	aliasExtensions(project, []cpext.Extension{
		{Instance: "registry", Host: "registry.local.neuronsphere.io"},
		{Instance: "quiet"},
		{Instance: "broken", Host: "broken.example", Err: os.ErrNotExist},
	})

	aliases := proxyAliases(t, project)
	if !contains(aliases, "registry.local.neuronsphere.io") {
		t.Errorf("aliases = %v, want the extension's hostname", aliases)
	}
	if contains(aliases, "broken.example") {
		t.Errorf("a failed extension's hostname was aliased: %v", aliases)
	}
	// The identity provider's alias, already in the bundled file, must survive.
	if len(aliases) < 2 {
		t.Errorf("aliases = %v, want the existing ones kept", aliases)
	}
	if hashOf(t, project, compose.ProxyService) == before {
		t.Error("the proxy's config hash did not change, so it would never be recreated to pick the alias up")
	}
}

func TestAliasExtensionsIsANoOpWithoutHostnames(t *testing.T) {
	t.Parallel()

	reg := &registry.Registry{ControlPlane: registry.ControlPlane{ComposeProject: "p", Network: "n"}}
	opts := testOptions(t.TempDir(), nil)
	project, err := Project(opts, reg, "")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	before := hashOf(t, project, compose.ProxyService)
	aliasExtensions(project, []cpext.Extension{{Instance: "quiet"}})
	if hashOf(t, project, compose.ProxyService) != before {
		t.Error("the proxy was recreated for an extension that serves nothing")
	}
}

// An HMD_HOME with no manifest resolves nothing and complains about nothing --
// the default NERD004 requires.
func TestResolveExtensionsIsQuietWithoutAManifest(t *testing.T) {
	t.Parallel()

	reg := &registry.Registry{ControlPlane: registry.ControlPlane{ComposeProject: "p", Network: "n"}}
	exts, err := ResolveExtensions(context.Background(), testOptions(t.TempDir(), nil), reg, nil)
	if err != nil {
		t.Fatalf("ResolveExtensions: %v", err)
	}
	if len(exts) != 0 {
		t.Errorf("resolved %d extensions from no manifest", len(exts))
	}
}

func TestExtensionManifestPathNamesTheFileToCreate(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	want := filepath.Join(home, ".config", "control-plane.yaml")
	if got := extensionManifestPath(testOptions(home, nil)); got != want {
		t.Errorf("extensionManifestPath = %q, want %q", got, want)
	}
}

// -- helpers ---------------------------------------------------------------

func proxyAliases(t *testing.T, p *compose.Project) []string {
	t.Helper()
	for _, s := range p.Services {
		if s.Key != compose.ProxyService {
			continue
		}
		for _, attach := range s.Networks {
			if attach.Name == cpext.PlatformNetwork {
				return attach.Aliases
			}
		}
	}
	t.Fatal("the bundled proxy service has no platform-network attachment")
	return nil
}

// hashOf is the digest upService compares, so "the hash changed" really does
// mean "this container would be recreated".
func hashOf(t *testing.T, p *compose.Project, key string) string {
	t.Helper()
	for _, s := range p.Services {
		if s.Key == key {
			return compose.ConfigHash(p, s)
		}
	}
	t.Fatalf("no service %q", key)
	return ""
}

func keys(m map[string]compose.Network) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func contains(hay []string, needle string) bool {
	for _, v := range hay {
		if v == needle {
			return true
		}
	}
	return false
}

// Declaring nothing is a convergence too. The vhost fragment has to be
// rewritten even when there is nothing to write, or the last extension ever
// declared keeps its listener forever.
func TestApplyingAnEmptySetStillRewritesTheVhosts(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	opts := testOptions(home, nil)
	r := router.New(home, opts.Lookup)
	if err := r.WriteExtensionVhosts([]router.ExtensionVhost{
		{Name: "gone", Host: "gone.example", Upstream: "c:1"},
	}); err != nil {
		t.Fatalf("seeding the fragment: %v", err)
	}

	applyExtensions(context.Background(), opts, r, &compose.Runner{}, nil, cpext.Options{})

	data, err := os.ReadFile(filepath.Join(r.VhostDir(), router.ExtensionFragment))
	if err != nil {
		t.Fatalf("reading the fragment: %v", err)
	}
	if strings.Contains(string(data), "gone.example") {
		t.Errorf("an undeclared extension kept its listener:\n%s", data)
	}
}

// The handback's half of the same property: a variable contributed by an
// extension that is no longer declared has to disappear, and the user's own
// values around it have to survive it going.
func TestApplyingAnEmptySetRemovesTheHandbackBlock(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	opts := testOptions(home, nil)
	r := router.New(home, opts.Lookup)

	seeded := "# a note the user wrote\nHMD_DID=aaa\n"
	if err := os.MkdirAll(filepath.Dir(hmdenv.Path(home)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hmdenv.Path(home), []byte(seeded), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := hmdenv.Upsert(home, []hmdenv.Var{
		{Name: "GOPROXY", Merge: hmdenv.MergeScalar, Value: "http://gone/go", Source: "gone"},
	}); err != nil {
		t.Fatalf("seeding the block: %v", err)
	}

	applyExtensions(context.Background(), opts, r, &compose.Runner{}, nil, cpext.Options{})

	data, err := os.ReadFile(hmdenv.Path(home))
	if err != nil {
		t.Fatalf("reading hmd.env: %v", err)
	}
	if strings.Contains(string(data), "GOPROXY") {
		t.Errorf("an undeclared extension kept its variable:\n%s", data)
	}
	if string(data) != seeded {
		t.Errorf("hmd.env = %q, want the user's own content untouched at %q", data, seeded)
	}
}

// A handback that cannot be written warns and lets the rest of the apply
// stand, exactly as a vhost that cannot be written does.
func TestApplyingAHandbackNeverFailsTheApply(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	opts := testOptions(home, nil)
	r := router.New(home, opts.Lookup)

	// Two blocks: nsctl cannot tell which is its own, so Upsert refuses.
	block := "# >>> ns handback >>>\nA='1'\n# <<< ns handback <<<\n"
	if err := os.MkdirAll(filepath.Dir(hmdenv.Path(home)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hmdenv.Path(home), []byte(block+block), 0o600); err != nil {
		t.Fatal(err)
	}

	report := applyExtensions(context.Background(), opts, r, &compose.Runner{}, nil, cpext.Options{})
	if !report.OK() {
		t.Error("an unwritable handback failed the apply")
	}
}
