// Package tokenstore reads and writes $HMD_HOME/.cache/tokens.yaml, the
// credential cache `hmd login` and `nsctl login` share.
//
// The schema is load-bearing far outside this repository. Roughly forty Python
// packages call hmd_cli_tools.okta_tools.get_auth_token, and
// internal/librarian/client.go reimplements the same read in Go; all of them
// take data["login"]["access_token"] and nothing else. So this package extends
// the file and never reshapes it: every field it adds sits inside `login:`,
// where both existing readers ignore it, and the day nsctl can write this file
// is the day authenticated artifact pulls start working in Go and Python alike.
//
// One thing is deliberately not copied from the Python. That writer does a
// Path.touch() followed by a bare open(..., "w") and sets no mode, so the
// bearer token lands at whatever the umask allows -- typically 0644, readable
// by every other user on the machine. This package writes 0600.
package tokenstore

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/atomicfile"
)

// RelPath is the file's location relative to $HMD_HOME.
//
// hmd_cache_folder_path() joined with okta_tools.HMD_TOKEN_FILENAME, which is
// the same value internal/librarian computes; the two must not drift.
var RelPath = filepath.Join(".cache", "tokens.yaml")

// Login is the `login:` mapping.
//
// AccessToken and IDToken are the two keys that existed before nsctl could
// write this file, and they keep their names and their place. Everything below
// them is new and additive.
type Login struct {
	AccessToken string `yaml:"access_token"`
	IDToken     string `yaml:"id_token,omitempty"`

	RefreshToken string `yaml:"refresh_token,omitempty"`
	// ExpiresAt is when the access token stops being usable, RFC 3339.
	// Omitted when the server named no lifetime -- an absent expiry means
	// "unknown", which is treated as "assume valid", not as "expired".
	ExpiresAt string `yaml:"expires_at,omitempty"`
	// Issuer and Profile record which endpoint and which profile minted this,
	// so `whoami` can say so and a refresh can find the same server again.
	Issuer    string `yaml:"issuer,omitempty"`
	Profile   string `yaml:"profile,omitempty"`
	TokenType string `yaml:"token_type,omitempty"`
	Scope     string `yaml:"scope,omitempty"`

	// Extra carries keys this version does not know about, so a rewrite here
	// cannot silently drop what another writer put there. The same inline-map
	// trick manifest.Manifest uses to preserve plugins/plugin_config verbatim.
	Extra map[string]any `yaml:",inline"`
}

// File is the whole document.
type File struct {
	Login Login          `yaml:"login"`
	Extra map[string]any `yaml:",inline"`
}

// Path is where the file lives, or "" with no home.
func Path(home string) string {
	if home == "" {
		return ""
	}
	return filepath.Join(home, RelPath)
}

// Load reads the file. A missing file is an empty File and no error: having
// never logged in is a state, not a failure.
func Load(home string) (*File, error) {
	path := Path(home)
	if path == "" {
		return nil, fmt.Errorf("no HMD_HOME")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &File{}, nil
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var file File
	if err := yaml.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return &file, nil
}

// Save writes the file, preserving anything already in it that this package
// does not model.
func Save(home string, file *File) error {
	path := Path(home)
	if path == "" {
		return fmt.Errorf("no HMD_HOME")
	}
	data, err := yaml.Marshal(file)
	if err != nil {
		return fmt.Errorf("encoding %s: %w", path, err)
	}
	// 0600 on the file, 0700 on .cache/: this is a bearer token, and the
	// directory it sits in should not advertise that it is there.
	return atomicfile.Write(path, data, 0o600, 0o700)
}

// Store replaces the stored credential, keeping unknown keys.
//
// Read-modify-write rather than a plain overwrite so that a key written by the
// Python CLI, or by a later version of this one, survives a login here.
func Store(home string, login Login) error {
	file, err := Load(home)
	if err != nil {
		return err
	}
	// Carry forward whatever was in the old login block but is not in the new
	// one, then let the new values win.
	if file.Login.Extra != nil && login.Extra == nil {
		login.Extra = file.Login.Extra
	}
	file.Login = login
	return Save(home, file)
}

// Clear removes the stored credential.
//
// The `login:` block is emptied rather than the file deleted, because the file
// may carry keys this package did not write. With nothing else in it, the file
// is removed -- leaving an empty `login: {}` behind would make `whoami` report
// a credential that is not there.
func Clear(home string) error {
	path := Path(home)
	if path == "" {
		return fmt.Errorf("no HMD_HOME")
	}
	file, err := Load(home)
	if err != nil {
		return err
	}
	if len(file.Extra) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing %s: %w", path, err)
		}
		return nil
	}
	file.Login = Login{}
	return Save(home, file)
}

// Present reports whether a credential is stored at all.
func (l Login) Present() bool { return strings.TrimSpace(l.AccessToken) != "" }

// Expiry parses ExpiresAt, returning the zero time when it is absent or
// unparseable. Unparseable is treated as unknown rather than as an error: a
// malformed timestamp should cost a re-login, not a refusal to run.
func (l Login) Expiry() time.Time {
	if l.ExpiresAt == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, l.ExpiresAt)
	if err != nil {
		return time.Time{}
	}
	return t
}

// SetExpiry records when the token stops being usable. A zero time clears it.
func (l *Login) SetExpiry(t time.Time) {
	if t.IsZero() {
		l.ExpiresAt = ""
		return
	}
	l.ExpiresAt = t.UTC().Format(time.RFC3339)
}

// Valid reports whether the credential is present and has not expired.
//
// An unknown expiry counts as valid. The server is the authority on whether a
// token works, and refusing to use one whose lifetime was never stated would
// discard a perfectly good credential.
func (l Login) Valid(now time.Time) bool {
	if !l.Present() {
		return false
	}
	expiry := l.Expiry()
	return expiry.IsZero() || now.Before(expiry)
}

// ExpiresWithin reports whether the token expires inside d.
//
// Used to refresh slightly early: a token valid for another two seconds will
// have expired by the time the next request is answered, and the round trip
// that discovers this costs more than refreshing did.
func (l Login) ExpiresWithin(now time.Time, d time.Duration) bool {
	expiry := l.Expiry()
	if expiry.IsZero() {
		return false
	}
	return now.Add(d).After(expiry)
}

// Refreshable reports whether a silent renewal is possible.
func (l Login) Refreshable() bool { return strings.TrimSpace(l.RefreshToken) != "" }
