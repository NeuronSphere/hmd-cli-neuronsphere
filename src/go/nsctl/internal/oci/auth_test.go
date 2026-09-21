package oci

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nsconfig"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/tokenstore"
)

func TestParseChallenge(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in     string
		scheme string
		params map[string]string
	}{
		{
			in:     `Bearer realm="https://ghcr.io/token",service="ghcr.io",scope="repository:hmdlabs/stacks/x:pull"`,
			scheme: "Bearer",
			params: map[string]string{"realm": "https://ghcr.io/token", "service": "ghcr.io", "scope": "repository:hmdlabs/stacks/x:pull"},
		},
		{
			// A quoted comma inside a value must not split the parameter.
			in:     `Bearer realm="https://r/token",scope="repository:a/b:pull,push"`,
			scheme: "Bearer",
			params: map[string]string{"realm": "https://r/token", "scope": "repository:a/b:pull,push"},
		},
		{
			in:     `Basic realm="Registry Realm"`,
			scheme: "Basic",
			params: map[string]string{"realm": "Registry Realm"},
		},
		{
			// Unquoted values and odd spacing.
			in:     `bearer realm=https://r/token , service=r`,
			scheme: "Bearer",
			params: map[string]string{"realm": "https://r/token", "service": "r"},
		},
		{in: "", scheme: "", params: map[string]string{}},
	}
	for _, tc := range cases {
		scheme, params := parseChallenge(tc.in)
		if scheme != tc.scheme {
			t.Errorf("parseChallenge(%q) scheme = %q, want %q", tc.in, scheme, tc.scheme)
		}
		if len(params) != len(tc.params) {
			t.Errorf("parseChallenge(%q) params = %v, want %v", tc.in, params, tc.params)
			continue
		}
		for k, v := range tc.params {
			if params[k] != v {
				t.Errorf("parseChallenge(%q)[%s] = %q, want %q", tc.in, k, params[k], v)
			}
		}
	}
}

// TestResolveCredentialPrecedence is SPEC006's list, one case per tier, and
// the Source each reports.
func TestResolveCredentialPrecedence(t *testing.T) {
	t.Parallel()

	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	home := t.TempDir()
	if err := tokenstore.Store(home, tokenstore.Login{AccessToken: "login-jwt"}); err != nil {
		t.Fatal(err)
	}
	profiles := []nsconfig.Profile{
		{Name: "acme", AuthURL: "https://auth.acme", RegistryURL: "https://registry.acme-admin-neuronsphere.io"},
		{Name: "other", AuthURL: "https://auth.other", RegistryURL: "https://registry.other.io"},
	}

	t.Run("flag wins", func(t *testing.T) {
		t.Parallel()
		c := ResolveCredential("registry.acme-admin-neuronsphere.io", "flag-tok",
			env(map[string]string{TokenEnv: "env-tok"}), home, profiles)
		if c.Secret != "flag-tok" || c.Username != DefaultUser || c.Source != "--token" {
			t.Errorf("got %+v", c)
		}
	})
	t.Run("environment second, with its user", func(t *testing.T) {
		t.Parallel()
		c := ResolveCredential("registry.acme-admin-neuronsphere.io", "",
			env(map[string]string{TokenEnv: "env-tok", UserEnv: "alice"}), home, profiles)
		if c.Secret != "env-tok" || c.Username != "alice" || c.Source != TokenEnv {
			t.Errorf("got %+v", c)
		}
	})
	t.Run("profile by host match, login token as bearer", func(t *testing.T) {
		t.Parallel()
		c := ResolveCredential("registry.acme-admin-neuronsphere.io", "", env(nil), home, profiles)
		if c.Bearer != "login-jwt" || c.Source != "profile acme" {
			t.Errorf("got %+v", c)
		}
		if c.Anonymous() {
			t.Error("must not be anonymous")
		}
	})
	t.Run("profile host mismatch is anonymous", func(t *testing.T) {
		t.Parallel()
		c := ResolveCredential("ghcr.io", "", env(nil), home, profiles)
		if !c.Anonymous() || c.Source != "anonymous" {
			t.Errorf("got %+v", c)
		}
	})
	t.Run("profile match without a login is anonymous", func(t *testing.T) {
		t.Parallel()
		empty := t.TempDir()
		c := ResolveCredential("registry.other.io", "", env(nil), empty, profiles)
		if !c.Anonymous() {
			t.Errorf("got %+v", c)
		}
	})
	t.Run("no home at all is anonymous", func(t *testing.T) {
		t.Parallel()
		c := ResolveCredential("ghcr.io", "", env(nil), "", nil)
		if !c.Anonymous() {
			t.Errorf("got %+v", c)
		}
	})
	t.Run("a malformed token file is anonymous, not an error", func(t *testing.T) {
		t.Parallel()
		bad := t.TempDir()
		if err := os.MkdirAll(filepath.Join(bad, ".cache"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(tokenstore.Path(bad), []byte(":\n:::"), 0o600); err != nil {
			t.Fatal(err)
		}
		c := ResolveCredential("registry.acme-admin-neuronsphere.io", "", env(nil), bad, profiles)
		if !c.Anonymous() {
			t.Errorf("got %+v", c)
		}
	})
}
