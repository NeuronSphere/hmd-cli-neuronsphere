package dockercreds

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func noHelper(context.Context, string, string) ([]byte, error) {
	return nil, errors.New("no helper expected")
}

// `docker login` without a credential store writes the pair base64'd inline.
func TestAnInlineAuthIsDecoded(t *testing.T) {
	t.Parallel()

	auth := base64.StdEncoding.EncodeToString([]byte("alice:ghp_secret"))
	r := Resolver{ConfigDir: writeConfig(t, `{"auths":{"ghcr.io":{"auth":"`+auth+`"}}}`), Helper: noHelper}
	c, ok := r.Lookup(context.Background(), "ghcr.io")
	if !ok || c.Username != "alice" || c.Secret != "ghp_secret" {
		t.Errorf("Lookup = %+v, %v; want alice/ghp_secret", c, ok)
	}
}

// On a Mac the default is credsStore: osxkeychain, and auths carries only an
// empty marker for the registry.
func TestTheCredentialStoreIsAsked(t *testing.T) {
	t.Parallel()

	var asked string
	r := Resolver{
		ConfigDir: writeConfig(t, `{"auths":{"ghcr.io":{}},"credsStore":"osxkeychain"}`),
		Helper: func(_ context.Context, helper, server string) ([]byte, error) {
			asked = helper + " " + server
			return []byte(`{"ServerURL":"ghcr.io","Username":"bob","Secret":"tok"}`), nil
		},
	}
	c, ok := r.Lookup(context.Background(), "ghcr.io")
	if !ok || c.Username != "bob" || c.Secret != "tok" {
		t.Errorf("Lookup = %+v, %v; want bob/tok", c, ok)
	}
	if asked != "osxkeychain ghcr.io" {
		t.Errorf("asked %q, want the store for ghcr.io", asked)
	}
}

// A per-registry helper wins over the default store, as it does for docker.
func TestAPerRegistryHelperWins(t *testing.T) {
	t.Parallel()

	var helperUsed string
	r := Resolver{
		ConfigDir: writeConfig(t, `{"credsStore":"desktop","credHelpers":{"ghcr.io":"gh"}}`),
		Helper: func(_ context.Context, helper, _ string) ([]byte, error) {
			helperUsed = helper
			return []byte(`{"Username":"u","Secret":"s"}`), nil
		},
	}
	if _, ok := r.Lookup(context.Background(), "ghcr.io"); !ok {
		t.Fatal("no credential")
	}
	if helperUsed != "gh" {
		t.Errorf("used %q, want the per-registry helper", helperUsed)
	}
}

// Nothing logged in, no config, a helper that knows nothing: no credential,
// and never an error -- the caller's report is "not logged in", not a crash.
func TestNoCredentialIsNotAnError(t *testing.T) {
	t.Parallel()

	for name, r := range map[string]Resolver{
		"no config":      {ConfigDir: t.TempDir(), Helper: noHelper},
		"other registry": {ConfigDir: writeConfig(t, `{"auths":{"docker.io":{"auth":"eDp5"}}}`), Helper: noHelper},
		"helper misses": {
			ConfigDir: writeConfig(t, `{"credsStore":"osxkeychain"}`),
			Helper: func(context.Context, string, string) ([]byte, error) {
				return nil, errors.New("credentials not found in native keychain")
			},
		},
		"malformed": {ConfigDir: writeConfig(t, `{not json`), Helper: noHelper},
	} {
		if c, ok := r.Lookup(context.Background(), "ghcr.io"); ok {
			t.Errorf("%s: Lookup = %+v, want none", name, c)
		}
	}
}
