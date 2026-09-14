package tokenstore

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestLoadOfAMissingFileIsNotAnError(t *testing.T) {
	t.Parallel()

	file, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if file.Login.Present() {
		t.Error("an absent file reported a credential")
	}
}

func TestStoreRoundTrip(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	expiry := time.Date(2026, 9, 14, 22, 10, 0, 0, time.UTC)
	login := Login{
		AccessToken: "at", IDToken: "it", RefreshToken: "rt",
		Issuer: "https://a.test/oauth2/ns", Profile: "acme", TokenType: "Bearer",
		Scope: "openid groups",
	}
	login.SetExpiry(expiry)
	if err := Store(home, login); err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	file, err := Load(home)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	got := file.Login
	if got.AccessToken != "at" || got.IDToken != "it" || got.RefreshToken != "rt" {
		t.Errorf("Login = %+v", got)
	}
	if got.Profile != "acme" || got.Issuer != "https://a.test/oauth2/ns" {
		t.Errorf("Login = %+v", got)
	}
	if !got.Expiry().Equal(expiry) {
		t.Errorf("Expiry() = %v, want %v", got.Expiry(), expiry)
	}
}

// THE contract. Roughly forty Python packages call
// hmd_cli_tools.okta_tools.get_auth_token, and internal/librarian reimplements
// the same read in Go; all of them take data["login"]["access_token"]. If this
// test fails, every one of them stops seeing the token nsctl wrote.
func TestWrittenFileStillParsesAsLoginAccessToken(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	login := Login{AccessToken: "the-token", IDToken: "the-id-token", RefreshToken: "rt"}
	login.SetExpiry(time.Now().Add(time.Hour))
	if err := Store(home, login); err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	data, err := os.ReadFile(Path(home))
	if err != nil {
		t.Fatal(err)
	}
	// Decode the way the Python and the Go librarian client do: an untyped
	// map, reaching for login.access_token and nothing else.
	var generic map[string]any
	if err := yaml.Unmarshal(data, &generic); err != nil {
		t.Fatalf("the file is not plain YAML: %v", err)
	}
	loginBlock, ok := generic["login"].(map[string]any)
	if !ok {
		t.Fatalf("no `login` mapping in:\n%s", data)
	}
	if got, _ := loginBlock["access_token"].(string); got != "the-token" {
		t.Errorf("login.access_token = %v, want the-token; file:\n%s", loginBlock["access_token"], data)
	}
	if got, _ := loginBlock["id_token"].(string); got != "the-id-token" {
		t.Errorf("login.id_token = %v", loginBlock["id_token"])
	}
	// The new keys must be inside `login:`, where both readers ignore them --
	// not hoisted to the top level, where they would change the document shape.
	if _, hoisted := generic["refresh_token"]; hoisted {
		t.Error("refresh_token was written at the top level, not inside login")
	}
	if _, ok := loginBlock["refresh_token"]; !ok {
		t.Error("refresh_token should sit inside the login mapping")
	}
}

// The Python writer sets no mode, so the bearer token typically lands 0644.
// This one must not.
func TestTheTokenFileIsPrivate(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	if err := Store(home, Login{AccessToken: "at"}); err != nil {
		t.Fatalf("Store() error = %v", err)
	}
	info, err := os.Stat(Path(home))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode = %o, want 600", perm)
	}
	dir, err := os.Stat(filepath.Dir(Path(home)))
	if err != nil {
		t.Fatal(err)
	}
	if perm := dir.Mode().Perm(); perm != 0o700 {
		t.Errorf("directory mode = %o, want 700", perm)
	}
}

// A key written by the Python CLI, or by a later version of this one, must
// survive a login here.
func TestUnknownKeysSurviveARewrite(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	path := Path(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	original := "login:\n  access_token: old\n  something_else: keep-me\nother_block:\n  key: value\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Store(home, Login{AccessToken: "new"}); err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	data, _ := os.ReadFile(path)
	var generic map[string]any
	if err := yaml.Unmarshal(data, &generic); err != nil {
		t.Fatal(err)
	}
	if _, ok := generic["other_block"]; !ok {
		t.Errorf("a top-level block was dropped:\n%s", data)
	}
	loginBlock := generic["login"].(map[string]any)
	if got, _ := loginBlock["access_token"].(string); got != "new" {
		t.Errorf("access_token = %v, want the new one", loginBlock["access_token"])
	}
	if _, ok := loginBlock["something_else"]; !ok {
		t.Errorf("an unknown key inside login was dropped:\n%s", data)
	}
}

func TestClearRemovesTheFileWhenNothingElseIsInIt(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	if err := Store(home, Login{AccessToken: "at"}); err != nil {
		t.Fatal(err)
	}
	if err := Clear(home); err != nil {
		t.Fatalf("Clear() error = %v", err)
	}
	if _, err := os.Stat(Path(home)); !os.IsNotExist(err) {
		t.Errorf("the file survived Clear(): %v", err)
	}
	// Clearing twice is not an error; logging out when not logged in is a
	// no-op, not a failure.
	if err := Clear(home); err != nil {
		t.Errorf("Clear() on an absent file error = %v", err)
	}
}

// A file carrying something this package did not write must not be deleted
// wholesale just because the credential in it is being discarded.
func TestClearKeepsAFileWithOtherContent(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	path := Path(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("login:\n  access_token: at\nkeep:\n  me: yes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Clear(home); err != nil {
		t.Fatalf("Clear() error = %v", err)
	}
	file, err := Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if file.Login.Present() {
		t.Error("Clear() left a credential behind")
	}
	if _, ok := file.Extra["keep"]; !ok {
		t.Error("Clear() dropped an unrelated block")
	}
}

func TestValidity(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)

	var live Login
	live.AccessToken = "at"
	live.SetExpiry(now.Add(time.Hour))
	if !live.Valid(now) {
		t.Error("a token expiring in an hour should be valid")
	}
	if live.ExpiresWithin(now, time.Minute) {
		t.Error("a token expiring in an hour is not within a minute of expiry")
	}
	if !live.ExpiresWithin(now, 2*time.Hour) {
		t.Error("a token expiring in an hour is within two hours of expiry")
	}

	var dead Login
	dead.AccessToken = "at"
	dead.SetExpiry(now.Add(-time.Second))
	if dead.Valid(now) {
		t.Error("an expired token reported valid")
	}

	// Unknown expiry means unknown, not expired: the server is the authority,
	// and discarding a working credential because its lifetime was never
	// stated is the wrong failure.
	unknown := Login{AccessToken: "at"}
	if !unknown.Valid(now) {
		t.Error("a token with no stated expiry should be treated as valid")
	}
	if unknown.ExpiresWithin(now, time.Hour) {
		t.Error("a token with no stated expiry is never within a window")
	}

	if (Login{}).Valid(now) {
		t.Error("an absent token reported valid")
	}
}

func TestExpiryOfAMalformedTimestampIsUnknown(t *testing.T) {
	t.Parallel()

	// A malformed timestamp should cost a re-login at worst, not a refusal to
	// run at all.
	login := Login{AccessToken: "at", ExpiresAt: "not a time"}
	if !login.Expiry().IsZero() {
		t.Error("an unparseable expiry should read as unknown")
	}
	if !login.Valid(time.Now()) {
		t.Error("an unparseable expiry should not invalidate the token")
	}
}

func TestSetExpiryClearsOnZero(t *testing.T) {
	t.Parallel()

	login := Login{AccessToken: "at", ExpiresAt: "2026-09-14T22:10:00Z"}
	login.SetExpiry(time.Time{})
	if login.ExpiresAt != "" {
		t.Errorf("ExpiresAt = %q, want cleared", login.ExpiresAt)
	}
}

func TestRefreshable(t *testing.T) {
	t.Parallel()

	if (Login{RefreshToken: "  "}).Refreshable() {
		t.Error("whitespace is not a refresh token")
	}
	if !(Login{RefreshToken: "rt"}).Refreshable() {
		t.Error("a refresh token should be refreshable")
	}
}

func TestNoHomeIsAnError(t *testing.T) {
	t.Parallel()

	if Path("") != "" {
		t.Error("Path() invented a location with no home")
	}
	if _, err := Load(""); err == nil {
		t.Error("Load() succeeded with no home")
	}
	if err := Store("", Login{AccessToken: "at"}); err == nil {
		t.Error("Store() succeeded with no home")
	}
	if err := Clear(""); err == nil {
		t.Error("Clear() succeeded with no home")
	}
}

// The path must stay the one internal/librarian and the Python compute.
func TestRelPathMatchesTheSharedLocation(t *testing.T) {
	t.Parallel()

	if got := Path("/home"); got != filepath.Join("/home", ".cache", "tokens.yaml") {
		t.Errorf("Path() = %q", got)
	}
}
