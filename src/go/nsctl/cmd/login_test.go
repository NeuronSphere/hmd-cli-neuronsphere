package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/authd"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/browser"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/deviceauth"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/hmdenv"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nsconfig"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/tokenstore"
)

// loginHarness drives runLogin directly.
//
// The cobra constructor builds its own loginDeps, so a test that went through
// the command tree could not replace the clock or the browser. Calling the
// function is what makes the seams reachable while still exercising everything
// above internal/.
type loginHarness struct {
	home   string
	opts   *Options
	deps   *loginDeps
	cmd    *cobra.Command
	out    *bytes.Buffer
	errBuf *bytes.Buffer
}

func newLoginHarness(t *testing.T, env hmdenv.Lookup) *loginHarness {
	t.Helper()
	if env == nil {
		env = fakeEnv(nil)
	}
	home := t.TempDir()
	h := &loginHarness{
		home:   home,
		opts:   &Options{Version: "9.9.9", Home: home, Lookup: env},
		out:    &bytes.Buffer{},
		errBuf: &bytes.Buffer{},
		deps: &loginDeps{
			Now: func() time.Time { return time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC) },
			// Never actually open a browser from a test.
			OpenURL: func(context.Context, string, ...string) error { return nil },
			IsTTY:   func() bool { return false },
			// Poll as fast as the server will answer; the cadence itself is
			// covered in internal/deviceauth, not here.
			Sleep: func(ctx context.Context, _ time.Duration) error {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(5 * time.Millisecond):
					return nil
				}
			},
		},
	}
	h.cmd = &cobra.Command{}
	h.cmd.SetOut(h.out)
	h.cmd.SetErr(h.errBuf)
	h.cmd.SetContext(context.Background())
	return h
}

func (h *loginHarness) login(args loginArgs) error {
	return runLogin(h.cmd, h.opts, h.deps, args)
}

func (h *loginHarness) writeConfig(t *testing.T, body string) {
	t.Helper()
	path := filepath.Join(h.home, ".config", nsconfig.Name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// authdServer runs the real mock identity provider and returns its ns issuer.
func authdServer(t *testing.T) string {
	t.Helper()
	key, err := authd.LoadOrCreateKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := authd.NewServer("", key)
	httpSrv := httptest.NewServer(srv.Handler())
	t.Cleanup(httpSrv.Close)
	srv.Issuer = httpSrv.URL
	return httpSrv.URL + "/oauth2/" + authd.ServerNS
}

// approveWhenAsked watches for a user code and approves it in the background,
// standing in for the person at the browser.
func approveWhenAsked(t *testing.T, issuer, username, group string) {
	t.Helper()
	go func() {
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			code := pendingUserCode()
			if code != "" {
				_, _ = http.PostForm(issuer+"/v1/device", url.Values{
					"user_code": {code},
					"username":  {username},
					"groups":    {group},
				})
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
}

// pendingUserCode is filled in by the browser hook below: the login command
// passes the complete verification URI to the browser opener, and that URI
// carries the user code, which is exactly what a real user reads.
var pendingCode = make(chan string, 8)

func pendingUserCode() string {
	select {
	case code := <-pendingCode:
		return code
	default:
		return ""
	}
}

// Sign in end to end against the real mock, through the real command.
func TestLoginSignsInAgainstTheLocalMock(t *testing.T) {
	issuer := authdServer(t)
	h := newLoginHarness(t, nil)
	h.writeConfig(t, "[profile.local]\nauth_url = \""+issuer+"\"\n")

	// The browser hook is where the user code reaches the "person". Runner is
	// a command runner, so the URL is the opener's last argument rather than
	// its name -- `open <url>` on darwin, `xdg-open <url>` elsewhere.
	h.deps.OpenURL = func(_ context.Context, _ string, args ...string) error {
		if len(args) == 0 {
			return nil
		}
		parsed, err := url.Parse(args[len(args)-1])
		if err != nil {
			return nil
		}
		pendingCode <- parsed.Query().Get("user_code")
		return nil
	}
	approveWhenAsked(t, issuer, "alice", "NeuronSphere Superset Admin - Local (none)")

	if err := h.login(loginArgs{profile: "local"}); err != nil {
		t.Fatalf("login: %v", err)
	}

	out := h.out.String()
	if !strings.Contains(out, "Code:") || !strings.Contains(out, "Open:") {
		t.Errorf("the code and URL were not printed:\n%s", out)
	}
	if !strings.Contains(out, "Signed in") {
		t.Errorf("no success line:\n%s", out)
	}

	file, err := tokenstore.Load(h.home)
	if err != nil {
		t.Fatal(err)
	}
	if !file.Login.Present() {
		t.Fatal("no token was cached")
	}
	if file.Login.Profile != "local" {
		t.Errorf("Profile = %q", file.Login.Profile)
	}
	claims, err := authd.DecodeClaims(file.Login.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := claims["sub"].(string); got != "alice" {
		t.Errorf("sub = %v, want alice", claims["sub"])
	}
	// The interop that motivates writing this file at all.
	if _, err := os.Stat(tokenstore.Path(h.home)); err != nil {
		t.Errorf("the shared token file is not where every other tool looks: %v", err)
	}
}

// A working credential is not worth a browser.
func TestLoginShortCircuitsOnAValidToken(t *testing.T) {
	t.Parallel()

	h := newLoginHarness(t, nil)
	h.writeConfig(t, "[profile.local]\nauth_url = \"https://unreachable.invalid/oauth2/ns\"\n")

	login := tokenstore.Login{AccessToken: "at", Profile: "local"}
	login.SetExpiry(h.deps.now().Add(2 * time.Hour))
	if err := tokenstore.Store(h.home, login); err != nil {
		t.Fatal(err)
	}

	// The auth_url is unreachable on purpose: reaching it would mean the
	// short-circuit did not happen.
	if err := h.login(loginArgs{profile: "local"}); err != nil {
		t.Fatalf("login: %v", err)
	}
	if !strings.Contains(h.out.String(), "Already signed in") {
		t.Errorf("out = %q, want the short-circuit", h.out.String())
	}
}

// Refusing, not prompting, when there is no terminal to answer.
func TestLoginWithoutConfigRefusesWithUsage(t *testing.T) {
	t.Parallel()

	h := newLoginHarness(t, nil)
	err := h.login(loginArgs{noPrompt: true})
	if err == nil {
		t.Fatal("login succeeded with no configuration")
	}
	if code := nserr.CodeOf(err); code != nserr.Usage {
		t.Errorf("exit code = %d, want %d", code, nserr.Usage)
	}
	// The message must name the path and show the shape.
	if !strings.Contains(err.Error(), nsconfig.Path(h.home, h.opts.Lookup)) {
		t.Errorf("error = %v, want it to name the config path", err)
	}
	if !strings.Contains(err.Error(), "auth_url") {
		t.Errorf("error = %v, want it to show the key", err)
	}
}

// Not a TTY is the same refusal as --no-prompt: prompting into a pipe hangs a
// script instead of telling it what is wrong.
func TestLoginWithoutATerminalRefusesRatherThanPrompts(t *testing.T) {
	t.Parallel()

	h := newLoginHarness(t, nil)
	h.deps.IsTTY = func() bool { return false }
	err := h.login(loginArgs{})
	if nserr.CodeOf(err) != nserr.Usage {
		t.Fatalf("error = %v (code %d), want a usage refusal", err, nserr.CodeOf(err))
	}
}

// On a terminal the question is asked once and the answer is written, so it is
// never asked again.
func TestLoginPromptsAndSavesTheEndpoint(t *testing.T) {
	t.Parallel()

	h := newLoginHarness(t, nil)
	h.deps.IsTTY = func() bool { return true }
	h.cmd.SetIn(strings.NewReader("https://auth.example.test/oauth2/ns\n"))

	// The flow will fail at discovery -- the host does not exist -- but the
	// profile must have been written before that, which is the behaviour under
	// test.
	_ = h.login(loginArgs{})

	cfg, err := nsconfig.Load(h.home, h.opts.Lookup)
	if err != nil {
		t.Fatalf("the prompt did not write a config: %v", err)
	}
	profile, err := cfg.Profile("")
	if err != nil {
		t.Fatalf("Profile() error = %v", err)
	}
	if profile.AuthURL != "https://auth.example.test/oauth2/ns" {
		t.Errorf("AuthURL = %q", profile.AuthURL)
	}
	if cfg.DefaultProfile == "" {
		t.Error("the first profile written should become the default")
	}
}

// A file that exists and does not parse must never be prompted over: a typo
// must not become data loss.
func TestLoginNeverOverwritesAnInvalidConfig(t *testing.T) {
	t.Parallel()

	h := newLoginHarness(t, nil)
	h.deps.IsTTY = func() bool { return true }
	h.cmd.SetIn(strings.NewReader("https://replacement.test/oauth2/ns\n"))
	original := "[profile.local]\nauth_uri = \"https://typo.test/oauth2/ns\"\n"
	h.writeConfig(t, original)

	err := h.login(loginArgs{})
	if nserr.CodeOf(err) != nserr.Usage {
		t.Fatalf("error = %v (code %d), want a usage refusal", err, nserr.CodeOf(err))
	}

	data, readErr := os.ReadFile(nsconfig.Path(h.home, h.opts.Lookup))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != original {
		t.Errorf("the invalid config was rewritten:\n%s", data)
	}
}

// An unknown profile against a populated file is a question already answered,
// differently -- so it is a refusal listing what exists, not a prompt.
func TestLoginWithAnUnknownProfileListsTheKnownOnes(t *testing.T) {
	t.Parallel()

	h := newLoginHarness(t, nil)
	h.deps.IsTTY = func() bool { return true }
	h.writeConfig(t, "[profile.acme]\nauth_url = \"https://a.test/oauth2/ns\"\n")

	err := h.login(loginArgs{profile: "typo"})
	if nserr.CodeOf(err) != nserr.Usage {
		t.Fatalf("error = %v, want a usage refusal", err)
	}
	if !strings.Contains(err.Error(), "acme") {
		t.Errorf("error = %v, want it to list the defined profile", err)
	}
}

// --auth-url needs no file at all, which is what lets a scripted install pass
// the URL once; --save then writes it.
func TestLoginAuthURLFlagSaves(t *testing.T) {
	t.Parallel()

	h := newLoginHarness(t, nil)
	_ = h.login(loginArgs{authURL: "https://flag.test/oauth2/ns", save: true, noPrompt: true})

	cfg, err := nsconfig.Load(h.home, h.opts.Lookup)
	if err != nil {
		t.Fatalf("--save wrote no config: %v", err)
	}
	profile, err := cfg.Profile("")
	if err != nil {
		t.Fatal(err)
	}
	if profile.AuthURL != "https://flag.test/oauth2/ns" {
		t.Errorf("AuthURL = %q", profile.AuthURL)
	}
}

func TestLoginRejectsABareHostname(t *testing.T) {
	t.Parallel()

	h := newLoginHarness(t, nil)
	err := h.login(loginArgs{authURL: "auth.example.test", noPrompt: true})
	if nserr.CodeOf(err) != nserr.Usage {
		t.Fatalf("error = %v, want a usage refusal", err)
	}
}

// An expired token with a refresh token must renew without a browser. This is
// what offline_access is requested for, and what the Python login cannot do.
func TestLoginRefreshesRatherThanReauthenticating(t *testing.T) {
	t.Parallel()

	var refreshes int
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(w, map[string]any{
			"issuer":                        srv.URL,
			"token_endpoint":                srv.URL + "/v1/token",
			"device_authorization_endpoint": srv.URL + "/v1/device/authorize",
		})
	})
	mux.HandleFunc("POST /v1/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.PostFormValue("grant_type") != "refresh_token" {
			t.Errorf("grant_type = %q, want a refresh", r.PostFormValue("grant_type"))
		}
		refreshes++
		writeTestJSON(w, map[string]any{"access_token": "renewed", "expires_in": 3600})
	})
	mux.HandleFunc("POST /v1/device/authorize", func(w http.ResponseWriter, r *http.Request) {
		t.Error("a browser flow was started when a refresh should have sufficed")
		http.Error(w, "unexpected", http.StatusInternalServerError)
	})

	h := newLoginHarness(t, nil)
	h.writeConfig(t, "[profile.local]\nauth_url = \""+srv.URL+"\"\n")

	stale := tokenstore.Login{AccessToken: "old", RefreshToken: "rt", Profile: "local"}
	stale.SetExpiry(h.deps.now().Add(-time.Minute))
	if err := tokenstore.Store(h.home, stale); err != nil {
		t.Fatal(err)
	}

	if err := h.login(loginArgs{profile: "local"}); err != nil {
		t.Fatalf("login: %v", err)
	}
	if refreshes != 1 {
		t.Errorf("refreshes = %d, want 1", refreshes)
	}
	file, _ := tokenstore.Load(h.home)
	if file.Login.AccessToken != "renewed" {
		t.Errorf("AccessToken = %q", file.Login.AccessToken)
	}
	// The server rotated nothing, so the old refresh token must survive.
	if file.Login.RefreshToken != "rt" {
		t.Errorf("RefreshToken = %q, want the previous one kept", file.Login.RefreshToken)
	}
	if !strings.Contains(h.out.String(), "Renewed") {
		t.Errorf("out = %q", h.out.String())
	}
}

func writeTestJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

// Every command under $HMD_HOME must refuse uniformly when it is unset.
func TestLoginCommandsRequireHome(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"login", "logout", "whoami"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, _, err := run(t, fakeEnv(nil), name, "--help")
			if err != nil {
				t.Fatalf("%s --help: %v", name, err)
			}
		})
	}

	// Without --help the command runs and must refuse for want of a home.
	_, _, err := run(t, fakeEnv(nil), "whoami")
	if nserr.CodeOf(err) != nserr.Usage {
		t.Errorf("whoami with no HMD_HOME: error = %v (code %d), want %d",
			err, nserr.CodeOf(err), nserr.Usage)
	}
}

func TestLogoutAndWhoami(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	env := fakeEnv(map[string]string{"HMD_HOME": home})

	// Not signed in.
	_, _, err := run(t, env, "whoami")
	if nserr.CodeOf(err) != nserr.Usage {
		t.Errorf("whoami before login: error = %v, want a usage refusal", err)
	}
	out, _, err := run(t, env, "logout")
	if err != nil {
		t.Fatalf("logout before login: %v", err)
	}
	if !strings.Contains(out, "Not signed in") {
		t.Errorf("logout out = %q", out)
	}

	// Cache a real, decodable token.
	key, err := authd.LoadOrCreateKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	token, err := authd.Mint(key, "http://auth.local.neuronsphere.io", authd.Request{
		Server:  authd.ServerNS,
		Subject: "alice",
		Groups:  []string{"NeuronSphere Superset Admin - Local (none)"},
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	login := tokenstore.Login{AccessToken: token, Profile: "local", Issuer: "http://auth.local.neuronsphere.io"}
	login.SetExpiry(time.Now().Add(time.Hour))
	if err := tokenstore.Store(home, login); err != nil {
		t.Fatal(err)
	}

	out, _, err = run(t, env, "whoami")
	if err != nil {
		t.Fatalf("whoami: %v", err)
	}
	for _, want := range []string{"alice", "Superset Admin", "local", "not verified"} {
		if !strings.Contains(out, want) {
			t.Errorf("whoami out missing %q:\n%s", want, out)
		}
	}

	out, _, err = run(t, env, "logout")
	if err != nil {
		t.Fatalf("logout: %v", err)
	}
	if !strings.Contains(out, "Signed out") {
		t.Errorf("logout out = %q", out)
	}
	file, _ := tokenstore.Load(home)
	if file.Login.Present() {
		t.Error("logout left the credential behind")
	}
}

// The browser is a convenience; a failure to open one must not fail the login.
func TestBrowserFailureIsANoteNotAnError(t *testing.T) {
	t.Parallel()

	err := browser.Open(context.Background(), "https://example.test",
		func(context.Context, string, ...string) error { return os.ErrNotExist })
	if err == nil {
		t.Fatal("Open() hid a failure")
	}
	// runLogin must treat that error as a note; asserting the shape here keeps
	// the contract visible where the helper lives.
	if !strings.Contains(err.Error(), "opening a browser") {
		t.Errorf("error = %v", err)
	}
}

// /dev/null is a character device on every Unix, so a check for ModeCharDevice
// alone mistakes `nsctl login </dev/null` -- the usual way a script says there
// is nobody here -- for a terminal, and answers it with a prompt into the void.
func TestDevNullIsNotATerminal(t *testing.T) {
	t.Parallel()

	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Skipf("no %s on this platform: %v", os.DevNull, err)
	}
	t.Cleanup(func() { devNull.Close() })

	cmd := &cobra.Command{}
	cmd.SetIn(devNull)
	if stdinIsTerminal(cmd) {
		t.Errorf("%s was reported as a terminal", os.DevNull)
	}
}

// A buffer is not a person either -- this is the path every other test takes.
func TestABufferIsNotATerminal(t *testing.T) {
	t.Parallel()

	cmd := &cobra.Command{}
	cmd.SetIn(strings.NewReader("whatever"))
	if stdinIsTerminal(cmd) {
		t.Error("a buffer was reported as a terminal")
	}
}

// A profile that worked against the local mock and fails against a real
// provider fails with a bare "invalid_client", which says nothing about the key
// the file never had to set.
func TestInvalidClientExplainsTheMissingClientID(t *testing.T) {
	t.Parallel()

	rejection := &deviceauth.Error{
		Operation: "requesting a device code", Status: 400,
		Code: "invalid_client", Description: "Invalid value for 'client_id' parameter.",
	}

	defaulted := nsconfig.Profile{Name: "acme", ClientID: nsconfig.DefaultClientID}
	got := explainClientRejection(rejection, defaulted)
	for _, want := range []string{"client_id", "[profile.acme]", nsconfig.DefaultClientID} {
		if !strings.Contains(got.Error(), want) {
			t.Errorf("error missing %q:\n%s", want, got)
		}
	}

	// A profile that named a client id is being told something true about that
	// client; adding "you set no client_id" would be false and misleading.
	named := nsconfig.Profile{Name: "acme", ClientID: "0oa1b2c3"}
	if explainClientRejection(rejection, named).Error() != rejection.Error() {
		t.Error("the hint was added to a profile that already names a client id")
	}

	// Unrelated failures pass through untouched.
	other := &deviceauth.Error{Operation: "op", Code: "server_error"}
	if explainClientRejection(other, defaulted).Error() != other.Error() {
		t.Error("the hint was added to an unrelated error")
	}
}
