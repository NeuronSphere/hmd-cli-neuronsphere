// Package dockercreds resolves the credential the docker CLI holds for a
// registry: what `docker login` stored, inline or in a credential helper.
//
// The Engine API ignores ~/.docker/config.json (see internal/compose), and k3s
// reads none of it, so anything nsctl hands a credential to has to be given it
// explicitly. This is the one place that reads it.
package dockercreds

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Credential is a registry username and secret. Never logged.
type Credential struct {
	Username string
	Secret   string
}

// Resolver reads a docker CLI configuration directory.
type Resolver struct {
	// ConfigDir is the docker config directory; empty means DOCKER_CONFIG,
	// then ~/.docker, as the CLI resolves it.
	ConfigDir string
	// Helper runs `docker-credential-<helper> get` with server on stdin. Nil
	// runs the real binary.
	Helper func(ctx context.Context, helper, server string) ([]byte, error)
}

type configFile struct {
	Auths       map[string]struct{ Auth string } `json:"auths"`
	CredsStore  string                           `json:"credsStore"`
	CredHelpers map[string]string                `json:"credHelpers"`
}

// Lookup returns the credential for server, and whether there is one.
//
// Every failure -- no config, a malformed one, a helper that knows nothing --
// is "no credential". A caller reports that as not being logged in, which is
// the remedy whatever the cause.
func (r Resolver) Lookup(ctx context.Context, server string) (Credential, bool) {
	b, err := os.ReadFile(filepath.Join(r.configDir(), "config.json"))
	if err != nil {
		return Credential{}, false
	}
	var cfg configFile
	if err := json.Unmarshal(b, &cfg); err != nil {
		return Credential{}, false
	}

	helper := cfg.CredHelpers[server]
	if helper == "" {
		helper = cfg.CredsStore
	}
	if helper != "" {
		if c, ok := r.fromHelper(ctx, helper, server); ok {
			return c, true
		}
	}
	for key, a := range cfg.Auths {
		if hostOf(key) != server || a.Auth == "" {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(a.Auth)
		if err != nil {
			continue
		}
		user, secret, found := strings.Cut(string(raw), ":")
		if found && secret != "" {
			return Credential{Username: user, Secret: secret}, true
		}
	}
	return Credential{}, false
}

func (r Resolver) fromHelper(ctx context.Context, helper, server string) (Credential, bool) {
	run := r.Helper
	if run == nil {
		run = runHelper
	}
	out, err := run(ctx, helper, server)
	if err != nil {
		return Credential{}, false
	}
	var resp struct{ Username, Secret string }
	if err := json.Unmarshal(out, &resp); err != nil || resp.Secret == "" {
		return Credential{}, false
	}
	return Credential{Username: resp.Username, Secret: resp.Secret}, true
}

func runHelper(ctx context.Context, helper, server string) ([]byte, error) {
	c := exec.CommandContext(ctx, "docker-credential-"+helper, "get")
	c.Stdin = strings.NewReader(server)
	var out bytes.Buffer
	c.Stdout = &out
	if err := c.Run(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func (r Resolver) configDir() string {
	if r.ConfigDir != "" {
		return r.ConfigDir
	}
	if d := os.Getenv("DOCKER_CONFIG"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".docker")
}

// hostOf reduces an auths key, which older CLIs wrote as a URL, to its host.
func hostOf(key string) string {
	key = strings.TrimPrefix(strings.TrimPrefix(key, "https://"), "http://")
	host, _, _ := strings.Cut(key, "/")
	return host
}
