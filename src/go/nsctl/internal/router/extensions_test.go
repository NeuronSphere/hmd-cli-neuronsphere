package router

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func extensionFragment(t *testing.T, r *Router) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(r.VhostDir(), ExtensionFragment))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestExtensionVhostServesTheDeclaredHost(t *testing.T) {
	t.Parallel()

	r := New(t.TempDir(), fakeEnv(nil))
	err := r.WriteExtensionVhosts([]ExtensionVhost{
		{Name: "package-registry", Host: "registry.local.neuronsphere.io", Upstream: "p-package-registry-server-1:3141"},
	})
	if err != nil {
		t.Fatalf("WriteExtensionVhosts: %v", err)
	}

	body := extensionFragment(t, r)
	for _, want := range []string{
		"server_name registry.local.neuronsphere.io;",
		`set $ns_ext_package_registry "p-package-registry-server-1:3141";`,
		"proxy_pass http://$ns_ext_package_registry;",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("fragment does not contain %q:\n%s", want, body)
		}
	}
}

// The upstream goes through a resolver variable for the reason namedVhostServer
// gives: a literal proxy_pass host is resolved once at config load, so an
// extension whose container is absent would make `nginx -t` fail and reject the
// whole reload -- taking every other route down with it.
func TestExtensionVhostDefersTheUpstreamLookup(t *testing.T) {
	t.Parallel()

	r := New(t.TempDir(), fakeEnv(nil))
	if err := r.WriteExtensionVhosts([]ExtensionVhost{
		{Name: "reg", Host: "r.example", Upstream: "c:1"},
	}); err != nil {
		t.Fatalf("WriteExtensionVhosts: %v", err)
	}
	body := extensionFragment(t, r)
	if !strings.Contains(body, "resolver ") {
		t.Errorf("no resolver directive, so an absent container would break every route:\n%s", body)
	}
	if strings.Contains(body, "proxy_pass http://c:1") {
		t.Errorf("the upstream is a literal, which is resolved at config load:\n%s", body)
	}
}

// Written whole every time: an extension that is no longer declared loses its
// listener by being absent, which is how the fragment stays truthful.
func TestExtensionVhostsAreRewrittenWhole(t *testing.T) {
	t.Parallel()

	r := New(t.TempDir(), fakeEnv(nil))
	if err := r.WriteExtensionVhosts([]ExtensionVhost{
		{Name: "gone", Host: "gone.example", Upstream: "c:1"},
		{Name: "stays", Host: "stays.example", Upstream: "d:2"},
	}); err != nil {
		t.Fatalf("WriteExtensionVhosts: %v", err)
	}
	if err := r.WriteExtensionVhosts([]ExtensionVhost{
		{Name: "stays", Host: "stays.example", Upstream: "d:2"},
	}); err != nil {
		t.Fatalf("WriteExtensionVhosts: %v", err)
	}

	body := extensionFragment(t, r)
	if strings.Contains(body, "gone.example") {
		t.Errorf("an undeclared extension kept its listener:\n%s", body)
	}
	if !strings.Contains(body, "stays.example") {
		t.Errorf("the declared extension lost its listener:\n%s", body)
	}
}

// Declaring nothing has to truncate the fragment rather than leave the previous
// one in place, or the last extension ever declared serves forever.
func TestNoExtensionsTruncatesTheFragment(t *testing.T) {
	t.Parallel()

	r := New(t.TempDir(), fakeEnv(nil))
	if err := r.WriteExtensionVhosts([]ExtensionVhost{
		{Name: "reg", Host: "r.example", Upstream: "c:1"},
	}); err != nil {
		t.Fatalf("WriteExtensionVhosts: %v", err)
	}
	if err := r.WriteExtensionVhosts(nil); err != nil {
		t.Fatalf("WriteExtensionVhosts: %v", err)
	}
	if body := extensionFragment(t, r); strings.Contains(body, "server {") {
		t.Errorf("a server block survived an empty set:\n%s", body)
	}
}

// An extension that serves nothing contributes no block rather than a broken
// one with an empty server_name.
func TestExtensionVhostSkipsIncompleteEntries(t *testing.T) {
	t.Parallel()

	r := New(t.TempDir(), fakeEnv(nil))
	if err := r.WriteExtensionVhosts([]ExtensionVhost{
		{Name: "quiet"},
		{Name: "half", Host: "h.example"},
	}); err != nil {
		t.Fatalf("WriteExtensionVhosts: %v", err)
	}
	if body := extensionFragment(t, r); strings.Contains(body, "server {") {
		t.Errorf("an incomplete entry produced a server block:\n%s", body)
	}
}

// An instance name may hold dots and dashes; an nginx variable may not.
func TestVarSafeRendersAnInstanceName(t *testing.T) {
	t.Parallel()

	if got := varSafe("package-registry.v2"); got != "package_registry_v2" {
		t.Errorf("varSafe = %q", got)
	}
}
