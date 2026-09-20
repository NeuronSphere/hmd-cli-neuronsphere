package nsconfig

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// write puts a config at $HMD_HOME/.config/nsctl.toml and returns the home.
func write(t *testing.T, body string) string {
	t.Helper()
	home := t.TempDir()
	path := filepath.Join(home, ".config", Name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

func noEnv(string) string { return "" }

func TestLoadAndResolveTheDefaultProfile(t *testing.T) {
	t.Parallel()

	home := write(t, `
default_profile = "acme"

[profile.neuronsphere]
auth_url = "https://auth.neuronsphere.io/oauth2/ns"

[profile.acme]
auth_url  = "https://auth-aaa-us-west-2.acme-admin-neuronsphere.io/oauth2/ns"
audience  = "api://neuronsphere"
client_id = "0oa1b2c3"
scopes    = ["openid", "groups"]
`)
	cfg, err := Load(home, noEnv)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	profile, err := cfg.Profile("")
	if err != nil {
		t.Fatalf("Profile() error = %v", err)
	}
	if profile.Name != "acme" {
		t.Errorf("Name = %q, want the default_profile", profile.Name)
	}
	if profile.ClientID != "0oa1b2c3" {
		t.Errorf("ClientID = %q", profile.ClientID)
	}
	if profile.Audience != "api://neuronsphere" {
		t.Errorf("Audience = %q", profile.Audience)
	}
	if len(profile.Scopes) != 2 {
		t.Errorf("Scopes = %v, want the file's own", profile.Scopes)
	}
}

func TestProfileDefaults(t *testing.T) {
	t.Parallel()

	home := write(t, "[profile.only]\nauth_url = \"https://example.test/oauth2/ns/\"\n")
	cfg, err := Load(home, noEnv)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	// One profile and no default_profile: the only one is unambiguous.
	profile, err := cfg.Profile("")
	if err != nil {
		t.Fatalf("Profile() error = %v", err)
	}
	if profile.ClientID != DefaultClientID {
		t.Errorf("ClientID = %q, want %q", profile.ClientID, DefaultClientID)
	}
	if len(profile.Scopes) != len(DefaultScopes) {
		t.Errorf("Scopes = %v, want the defaults", profile.Scopes)
	}
	// offline_access is what makes a refresh token possible, and its absence is
	// exactly why the Python login re-authenticates every hour.
	var hasOffline bool
	for _, scope := range profile.Scopes {
		if scope == "offline_access" {
			hasOffline = true
		}
	}
	if !hasOffline {
		t.Error("default scopes must include offline_access")
	}
	// A trailing slash would produce a double slash in every discovery URL.
	if strings.HasSuffix(profile.AuthURL, "/") {
		t.Errorf("AuthURL = %q, want the trailing slash trimmed", profile.AuthURL)
	}
}

// Picking one of several alphabetically would authenticate against an endpoint
// nobody named, so more than one profile and no default is a refusal.
func TestProfileRefusesToGuessBetweenSeveral(t *testing.T) {
	t.Parallel()

	home := write(t, `
[profile.a]
auth_url = "https://a.test/oauth2/ns"

[profile.b]
auth_url = "https://b.test/oauth2/ns"
`)
	cfg, err := Load(home, noEnv)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	_, err = cfg.Profile("")
	if err == nil {
		t.Fatal("Profile() guessed between two profiles")
	}
	for _, name := range []string{"a", "b"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error = %v, want it to list %q", err, name)
		}
	}
}

func TestUnknownProfileListsTheKnownOnes(t *testing.T) {
	t.Parallel()

	home := write(t, "[profile.acme]\nauth_url = \"https://a.test/oauth2/ns\"\n")
	cfg, _ := Load(home, noEnv)
	_, err := cfg.Profile("typo")
	if err == nil {
		t.Fatal("Profile() accepted an undefined profile")
	}
	if !strings.Contains(err.Error(), "acme") {
		t.Errorf("error = %v, want it to name the defined profile", err)
	}
}

// Absent and invalid must be distinguishable: one is answered by prompting,
// and the other must never be answered by overwriting the file.
func TestMissingFileIsErrNoConfig(t *testing.T) {
	t.Parallel()

	_, err := Load(t.TempDir(), noEnv)
	if !errors.Is(err, ErrNoConfig) {
		t.Fatalf("Load() error = %v, want ErrNoConfig", err)
	}
}

func TestSyntaxErrorNamesTheLine(t *testing.T) {
	t.Parallel()

	home := write(t, "[profile.acme]\nauth_url = \n")
	_, err := Load(home, noEnv)
	if err == nil {
		t.Fatal("Load() accepted malformed TOML")
	}
	if errors.Is(err, ErrNoConfig) {
		t.Fatal("a malformed file must not be reported as an absent one")
	}
	if !strings.Contains(err.Error(), "line") {
		t.Errorf("error = %v, want it to name the line", err)
	}
}

// A misspelt key that parsed silently would produce "auth_url is required"
// against a file that visibly contains a URL.
func TestUnknownKeyIsRefused(t *testing.T) {
	t.Parallel()

	home := write(t, "[profile.acme]\nauth_uri = \"https://a.test/oauth2/ns\"\n")
	_, err := Load(home, noEnv)
	if err == nil {
		t.Fatal("Load() accepted an unrecognised key")
	}
	if !strings.Contains(err.Error(), "auth_uri") {
		t.Errorf("error = %v, want it to name the offending key", err)
	}
}

func TestValidateRejectsABareHostname(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, url string
	}{
		{"bare hostname", "auth.example.com"},
		{"no scheme", "//auth.example.com/oauth2/ns"},
		{"empty", "   "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := Profile{Name: "p", AuthURL: tt.url}.Validate()
			if err == nil {
				t.Fatalf("Validate() accepted %q", tt.url)
			}
		})
	}
	if err := (Profile{Name: "p", AuthURL: "http://auth.local.neuronsphere.io/oauth2/ns"}).Validate(); err != nil {
		t.Errorf("Validate() rejected a local http issuer: %v", err)
	}
}

func TestSaveRoundTripsAndIsPrivate(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cfg := &Config{}
	cfg.Set(Profile{Name: "acme", AuthURL: "https://a.test/oauth2/ns"})
	// The first profile written becomes the default, so a user who answered
	// one prompt never has to name a profile afterwards.
	if cfg.DefaultProfile != "acme" {
		t.Errorf("DefaultProfile = %q, want the first profile written", cfg.DefaultProfile)
	}
	if err := Save(home, noEnv, cfg); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	path := Path(home, noEnv)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600", perm)
	}

	reloaded, err := Load(home, noEnv)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	profile, err := reloaded.Profile("")
	if err != nil {
		t.Fatalf("Profile() error = %v", err)
	}
	if profile.AuthURL != "https://a.test/oauth2/ns" {
		t.Errorf("AuthURL = %q", profile.AuthURL)
	}
}

// The override is what lets a test, or a one-off run, avoid depending on
// $HMD_HOME's layout -- the same reason manifest.PathOverride exists.
func TestPathOverride(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	custom := filepath.Join(dir, "elsewhere.toml")
	if err := os.WriteFile(custom, []byte("[profile.x]\nauth_url = \"https://x.test/oauth2/ns\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lookup := func(key string) string {
		if key == PathOverride {
			return custom
		}
		return ""
	}
	if got := Path("/unused", lookup); got != custom {
		t.Errorf("Path() = %q, want %q", got, custom)
	}
	cfg, err := Load("/unused", lookup)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if _, err := cfg.Profile("x"); err != nil {
		t.Errorf("Profile() error = %v", err)
	}
}

func TestProfileNamesAreSorted(t *testing.T) {
	t.Parallel()

	cfg := &Config{Profiles: map[string]Profile{"z": {}, "a": {}, "m": {}}}
	got := cfg.ProfileNames()
	want := []string{"a", "m", "z"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ProfileNames() = %v, want %v", got, want)
		}
	}
}

func TestNoProfiles(t *testing.T) {
	t.Parallel()

	if _, err := (&Config{}).Profile(""); !errors.Is(err, ErrNoProfiles) {
		t.Errorf("Profile() error = %v, want ErrNoProfiles", err)
	}
	var nilCfg *Config
	if _, err := nilCfg.Profile(""); !errors.Is(err, ErrNoProfiles) {
		t.Errorf("Profile() on nil error = %v, want ErrNoProfiles", err)
	}
}

// The errors that refuse to guess show the shape, so the user does not have to
// find the documentation for two lines of TOML.
func TestExampleIsValidTOMLForTheNamedProfile(t *testing.T) {
	t.Parallel()

	cfg, err := Parse([]byte(Example("acme")))
	if err != nil {
		t.Fatalf("the example this package prints does not parse: %v", err)
	}
	profile, err := cfg.Profile("")
	if err != nil {
		t.Fatalf("the example does not resolve a profile: %v", err)
	}
	if profile.Name != "acme" {
		t.Errorf("Name = %q, want acme", profile.Name)
	}
}

// withBuiltin points this build's compiled-in endpoint somewhere for the
// duration of a test. Not parallel-safe, so the tests using it do not call
// t.Parallel(): DefaultAuthURL is a link-time constant everywhere else.
func withBuiltin(t *testing.T, url string) {
	t.Helper()
	previous := DefaultAuthURL
	DefaultAuthURL = url
	t.Cleanup(func() { DefaultAuthURL = previous })
}

// A development build names no endpoint, so the refusal stands.
func TestNoBuiltinByDefault(t *testing.T) {
	if _, ok := Builtin(); ok && DefaultAuthURL == "" {
		t.Error("Builtin() answered with no DefaultAuthURL set")
	}
}

func TestBuiltinAnswersWhenNothingIsConfigured(t *testing.T) {
	withBuiltin(t, "https://auth.neuronsphere.test/oauth2/ns")

	profile, err := Resolve(nil, "")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if profile.Name != BuiltinProfileName {
		t.Errorf("Name = %q, want %q", profile.Name, BuiltinProfileName)
	}
	if profile.AuthURL != "https://auth.neuronsphere.test/oauth2/ns" {
		t.Errorf("AuthURL = %q", profile.AuthURL)
	}
	// Defaults still apply, so a built-in endpoint asks for offline_access and
	// is therefore renewable like any other.
	if profile.ClientID != DefaultClientID || len(profile.Scopes) != len(DefaultScopes) {
		t.Errorf("built-in did not get the usual defaults: %+v", profile)
	}
}

// A build's default is what to do when nobody said otherwise, never an
// override of someone who did.
func TestTheFileBeatsTheBuiltin(t *testing.T) {
	withBuiltin(t, "https://auth.neuronsphere.test/oauth2/ns")

	cfg, err := Parse([]byte("[profile.neuronsphere]\nauth_url = \"https://mine.test/oauth2/ns\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	profile, err := Resolve(cfg, "")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if profile.AuthURL != "https://mine.test/oauth2/ns" {
		t.Errorf("AuthURL = %q, want the file's", profile.AuthURL)
	}
}

// A caller who asked for "staging" and has no file wants to hear that
// "staging" is undefined, not to be signed in somewhere else entirely.
func TestTheBuiltinDoesNotAnswerForAnotherName(t *testing.T) {
	withBuiltin(t, "https://auth.neuronsphere.test/oauth2/ns")

	if _, err := Resolve(nil, "staging"); err == nil {
		t.Fatal("Resolve() returned the built-in for an unrelated profile name")
	}
	// Its own name does resolve, so `--profile neuronsphere` is not a
	// surprising failure on a build that has one.
	if _, err := Resolve(nil, BuiltinProfileName); err != nil {
		t.Errorf("Resolve(%q) error = %v", BuiltinProfileName, err)
	}
}

func TestResolveWithoutABuiltinIsErrNoProfiles(t *testing.T) {
	withBuiltin(t, "")

	if _, err := Resolve(nil, ""); !errors.Is(err, ErrNoProfiles) {
		t.Errorf("Resolve() error = %v, want ErrNoProfiles", err)
	}
}

// A misconfigured build must not become a login against a URL that is not one.
func TestABlankBuiltinIsNoBuiltin(t *testing.T) {
	withBuiltin(t, "   ")

	if _, ok := Builtin(); ok {
		t.Error("whitespace was accepted as a built-in endpoint")
	}
}
