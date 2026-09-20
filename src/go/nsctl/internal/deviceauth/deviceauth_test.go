package deviceauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fastClient is the real client with a short timeout, so a test that hangs
// fails rather than waits.
func fastClient() *http.Client { return &http.Client{Timeout: 5 * time.Second} }

// oauthErr writes the RFC 6749 error shape every polling state arrives as.
func oauthErr(w http.ResponseWriter, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":             code,
		"error_description": code + " happened",
	})
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

// discoveryServer serves a document naming its own endpoints.
func discoveryServer(t *testing.T, mux *http.ServeMux, paths ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	if len(paths) == 0 {
		paths = []string{"/.well-known/openid-configuration"}
	}
	for _, path := range paths {
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{
				"issuer":                        srv.URL,
				"token_endpoint":                srv.URL + "/v1/token",
				"device_authorization_endpoint": srv.URL + "/v1/device/authorize",
			})
		})
	}
	return srv
}

func TestDiscoverReadsTheOIDCDocument(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	srv := discoveryServer(t, mux)

	meta, err := Discover(context.Background(), fastClient(), srv.URL)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if meta.TokenEndpoint != srv.URL+"/v1/token" {
		t.Errorf("TokenEndpoint = %q", meta.TokenEndpoint)
	}
	if meta.DeviceAuthorizationEndpoint != srv.URL+"/v1/device/authorize" {
		t.Errorf("DeviceAuthorizationEndpoint = %q", meta.DeviceAuthorizationEndpoint)
	}
}

// Okta publishes the OIDC name and Superset/Airflow build the OAuth one, so a
// client that knows only the first cannot talk to half the servers in the
// platform.
func TestDiscoverFallsBackToTheOAuthDocument(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	srv := discoveryServer(t, mux, "/.well-known/oauth-authorization-server")
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})

	meta, err := Discover(context.Background(), fastClient(), srv.URL)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if meta.DeviceAuthorizationEndpoint == "" {
		t.Error("Discover() found the document but no device endpoint")
	}
}

// A server that answers discovery but cannot do the grant is a different
// problem from a wrong URL, and must not be reported as a missing field.
func TestDiscoverNamesTheMissingDeviceEndpoint(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"issuer": srv.URL, "token_endpoint": srv.URL + "/v1/token"})
	})

	_, err := Discover(context.Background(), fastClient(), srv.URL)
	if err == nil {
		t.Fatal("Discover() succeeded against a server with no device endpoint")
	}
	if !strings.Contains(err.Error(), "does not support the device authorization grant") {
		t.Errorf("Discover() error = %v, want it to name the unsupported grant", err)
	}
}

func TestDiscoverRejectsAnEmptyURL(t *testing.T) {
	t.Parallel()

	if _, err := Discover(context.Background(), fastClient(), "   "); err == nil {
		t.Fatal("Discover() accepted an empty issuer")
	}
}

// The happy path, end to end: authorize, poll past a pending response, and get
// a token.
func TestAuthorizeAndPoll(t *testing.T) {
	t.Parallel()

	var polls int32
	mux := http.NewServeMux()
	srv := discoveryServer(t, mux)
	mux.HandleFunc("POST /v1/device/authorize", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if got := r.PostFormValue("client_id"); got != "nsctl" {
			t.Errorf("client_id = %q, want nsctl", got)
		}
		if got := r.PostFormValue("scope"); got != "openid groups" {
			t.Errorf("scope = %q", got)
		}
		if got := r.PostFormValue("audience"); got != "api://neuronsphere" {
			t.Errorf("audience = %q", got)
		}
		writeJSON(w, map[string]any{
			"device_code":               "dev-code",
			"user_code":                 "BCDF-GHJK",
			"verification_uri":          srv.URL + "/v1/device",
			"verification_uri_complete": srv.URL + "/v1/device?user_code=BCDF-GHJK",
			"expires_in":                600,
			"interval":                  0, // exercise the RFC default
		})
	})
	mux.HandleFunc("POST /v1/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if got := r.PostFormValue("grant_type"); got != GrantType {
			t.Errorf("grant_type = %q", got)
		}
		if got := r.PostFormValue("device_code"); got != "dev-code" {
			t.Errorf("device_code = %q", got)
		}
		if atomic.AddInt32(&polls, 1) == 1 {
			oauthErr(w, ErrAuthorizationPending)
			return
		}
		writeJSON(w, map[string]any{
			"access_token": "at", "id_token": "it", "refresh_token": "rt",
			"token_type": "Bearer", "expires_in": 3600, "scope": "openid groups",
		})
	})

	ctx := context.Background()
	meta, err := Discover(ctx, fastClient(), srv.URL)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	auth, err := Authorize(ctx, fastClient(), meta, Request{
		ClientID: "nsctl", Scopes: []string{"openid", "groups"}, Audience: "api://neuronsphere",
	})
	if err != nil {
		t.Fatalf("Authorize() error = %v", err)
	}
	if auth.PollInterval() != DefaultInterval {
		t.Errorf("PollInterval() = %v, want the RFC default %v", auth.PollInterval(), DefaultInterval)
	}
	if auth.BrowserURL() != auth.VerificationURIComplete {
		t.Error("BrowserURL() should prefer the complete URI")
	}

	var waited recorder
	token, err := Poll(ctx, fastClient(), meta, auth, "nsctl", WithSleep(waited.sleep))
	if err != nil {
		t.Fatalf("Poll() error = %v", err)
	}
	// The server sent interval 0, so the RFC default is what Poll waits.
	if len(waited.waits) == 0 || waited.waits[0] != DefaultInterval {
		t.Errorf("first wait = %v, want the RFC default %v", waited.waits, DefaultInterval)
	}
	if token.AccessToken != "at" || token.RefreshToken != "rt" {
		t.Errorf("Poll() = %+v", token)
	}
	if polls < 2 {
		t.Errorf("polls = %d, want the pending response to have been retried", polls)
	}
}

// recorder is a sleep hook that returns at once and remembers what it was
// asked to wait, so a test can assert on the backoff rather than experience it.
type recorder struct{ waits []time.Duration }

func (r *recorder) sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	r.waits = append(r.waits, d)
	return nil
}

// slow_down must increase the interval, not merely be retried: a server that
// sends it is asking the client to back off, and a client that ignores it gets
// rate-limited into an expired code.
func TestPollHonoursSlowDown(t *testing.T) {
	t.Parallel()

	var calls int
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("POST /v1/token", func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			oauthErr(w, ErrSlowDown)
			return
		}
		writeJSON(w, map[string]any{"access_token": "at", "token_type": "Bearer"})
	})

	meta := &Metadata{TokenEndpoint: srv.URL + "/v1/token"}
	auth := &Authorization{DeviceCode: "d", UserCode: "u", Interval: 5, ExpiresIn: 600}

	var waited recorder
	if _, err := Poll(context.Background(), fastClient(), meta, auth, "nsctl", WithSleep(waited.sleep)); err != nil {
		t.Fatalf("Poll() error = %v", err)
	}

	// Each slow_down adds five seconds, per RFC 8628 section 3.5. A client
	// that merely retried would have waited 5s three times.
	want := []time.Duration{5 * time.Second, 10 * time.Second, 15 * time.Second}
	if len(waited.waits) != len(want) {
		t.Fatalf("waits = %v, want %v", waited.waits, want)
	}
	for i, w := range want {
		if waited.waits[i] != w {
			t.Errorf("wait %d = %v, want %v (waits: %v)", i, waited.waits[i], w, waited.waits)
		}
	}
}

func TestPollReportsTerminalErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		code string
		want string
	}{
		{"the user refused", ErrAccessDenied, "refused"},
		{"the code expired", ErrExpiredToken, "expired"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mux := http.NewServeMux()
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)
			mux.HandleFunc("POST /v1/token", func(w http.ResponseWriter, r *http.Request) {
				oauthErr(w, tt.code)
			})

			meta := &Metadata{TokenEndpoint: srv.URL + "/v1/token"}
			auth := &Authorization{DeviceCode: "d", Interval: 1, ExpiresIn: 600}
			var waited recorder
			_, err := Poll(context.Background(), fastClient(), meta, auth, "nsctl", WithSleep(waited.sleep))
			if err == nil {
				t.Fatalf("Poll() succeeded on %s", tt.code)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Poll() error = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

// An unrecognised OAuth error must surface the server's own words rather than
// be swallowed as "still pending", which would hang the CLI forever.
func TestPollSurfacesAnUnknownError(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("POST /v1/token", func(w http.ResponseWriter, r *http.Request) {
		oauthErr(w, "invalid_client")
	})

	meta := &Metadata{TokenEndpoint: srv.URL + "/v1/token"}
	auth := &Authorization{DeviceCode: "d", Interval: 1, ExpiresIn: 600}
	var waited recorder
	_, err := Poll(context.Background(), fastClient(), meta, auth, "nsctl", WithSleep(waited.sleep))
	if err == nil {
		t.Fatal("Poll() succeeded on invalid_client")
	}
	var oerr *Error
	if !errors.As(err, &oerr) {
		t.Fatalf("Poll() error = %T, want *Error", err)
	}
	if oerr.Code != "invalid_client" {
		t.Errorf("Code = %q", oerr.Code)
	}
	if !strings.Contains(err.Error(), "invalid_client happened") {
		t.Errorf("error = %v, want the server's description", err)
	}
}

// Ctrl-C must return at once rather than at the end of the current sleep.
func TestPollHonoursContextCancellation(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("POST /v1/token", func(w http.ResponseWriter, r *http.Request) {
		oauthErr(w, ErrAuthorizationPending)
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	meta := &Metadata{TokenEndpoint: srv.URL + "/v1/token"}
	// A long interval: only honouring the context can make this return fast.
	auth := &Authorization{DeviceCode: "d", Interval: 30, ExpiresIn: 600}

	done := make(chan error, 1)
	go func() {
		_, err := Poll(ctx, fastClient(), meta, auth, "nsctl")
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Poll() error = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Poll() ignored a cancelled context")
	}
}

// A device code that expires while the user is away must end the loop rather
// than poll until the process is killed.
func TestPollStopsAtTheDeadline(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("POST /v1/token", func(w http.ResponseWriter, r *http.Request) {
		oauthErr(w, ErrAuthorizationPending)
	})

	meta := &Metadata{TokenEndpoint: srv.URL + "/v1/token"}
	auth := &Authorization{DeviceCode: "d", Interval: 1, ExpiresIn: 1}
	// A real but tiny wait: the deadline is wall-clock, so a sleeper that
	// returned instantly would spin until ExpiresIn elapsed anyway.
	tiny := func(ctx context.Context, _ time.Duration) error {
		return realSleepFor(ctx, 300*time.Millisecond)
	}
	_, err := Poll(context.Background(), fastClient(), meta, auth, "nsctl", WithSleep(tiny))
	if err == nil {
		t.Fatal("Poll() ran past the code's expiry")
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Errorf("Poll() error = %v, want it to name the expiry", err)
	}
}

func TestAuthorizeRefusesAServerWithNoDeviceEndpoint(t *testing.T) {
	t.Parallel()

	_, err := Authorize(context.Background(), fastClient(), &Metadata{}, Request{ClientID: "nsctl"})
	if err == nil {
		t.Fatal("Authorize() accepted metadata with no device endpoint")
	}
}

func TestAuthorizeRefusesAnIncompleteResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body map[string]any
	}{
		{"no device_code", map[string]any{"user_code": "X", "verification_uri": "u"}},
		{"no user_code", map[string]any{"device_code": "d", "verification_uri": "u"}},
		{"no verification_uri", map[string]any{"device_code": "d", "user_code": "X"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mux := http.NewServeMux()
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)
			mux.HandleFunc("POST /v1/device/authorize", func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, tt.body)
			})

			meta := &Metadata{DeviceAuthorizationEndpoint: srv.URL + "/v1/device/authorize"}
			if _, err := Authorize(context.Background(), fastClient(), meta, Request{ClientID: "c"}); err == nil {
				t.Fatalf("Authorize() accepted a response with %s", tt.name)
			}
		})
	}
}

func TestRefresh(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("POST /v1/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if got := r.PostFormValue("grant_type"); got != "refresh_token" {
			t.Errorf("grant_type = %q", got)
		}
		if got := r.PostFormValue("refresh_token"); got != "old" {
			t.Errorf("refresh_token = %q", got)
		}
		writeJSON(w, map[string]any{"access_token": "new", "expires_in": 3600})
	})

	meta := &Metadata{TokenEndpoint: srv.URL + "/v1/token"}
	token, err := Refresh(context.Background(), fastClient(), meta, "old", "nsctl")
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if token.AccessToken != "new" {
		t.Errorf("AccessToken = %q", token.AccessToken)
	}
	// The server rotated nothing, so the field is empty and the caller keeps
	// the refresh token it already had.
	if token.RefreshToken != "" {
		t.Errorf("RefreshToken = %q, want empty when the server returns none", token.RefreshToken)
	}
}

func TestTokenExpiresAt(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	if got := (&Token{ExpiresIn: 3600}).ExpiresAt(now); !got.Equal(now.Add(time.Hour)) {
		t.Errorf("ExpiresAt() = %v", got)
	}
	// No lifetime means unknown, and unknown must not become "expired now".
	if got := (&Token{}).ExpiresAt(now); !got.IsZero() {
		t.Errorf("ExpiresAt() = %v, want the zero time when the server named none", got)
	}
}

func TestErrorMessage(t *testing.T) {
	t.Parallel()

	err := &Error{Operation: "op", Status: 400, Code: "slow_down", Description: "back off"}
	if got := err.Error(); got != "op: slow_down: back off" {
		t.Errorf("Error() = %q", got)
	}
	if !(&Error{Code: ErrSlowDown}).Pending() || !(&Error{Code: ErrAuthorizationPending}).Pending() {
		t.Error("Pending() should be true for both waiting states")
	}
	if (&Error{Code: ErrAccessDenied}).Pending() {
		t.Error("Pending() should be false for access_denied")
	}
	// A body with no OAuth shape still has to produce something readable.
	plain := &Error{Operation: "op", Status: 503, Body: "gateway down"}
	if got := plain.Error(); !strings.Contains(got, "503") || !strings.Contains(got, "gateway down") {
		t.Errorf("Error() = %q", got)
	}
	_ = fmt.Sprint(plain)
}

// realSleepFor waits d, honouring ctx. Used where a test needs wall-clock time
// to pass rather than merely to record that Poll asked for it.
func realSleepFor(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}
