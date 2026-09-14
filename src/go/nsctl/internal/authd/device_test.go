package authd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/deviceauth"
)

// liveServer runs this mock on a real listener, issuing under its own URL.
//
// A real listener rather than a recorder, because the point of these tests is
// to drive the mock with the actual RFC 8628 client over actual HTTP. A test
// that hand-rolled the requests would prove the author's idea of the protocol
// and nothing about the client that has to speak it.
func liveServer(t *testing.T) *httptest.Server {
	t.Helper()
	key, err := LoadOrCreateKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Issuer is filled in after the listener exists, since it has to be the
	// URL the server is actually reachable at.
	srv := NewServer("", key)
	httpSrv := httptest.NewServer(srv.Handler())
	t.Cleanup(httpSrv.Close)
	srv.Issuer = httpSrv.URL
	return httpSrv
}

func fastClient() *http.Client { return &http.Client{Timeout: 5 * time.Second} }

// noWait is a Poll sleep hook that returns at once.
func noWait(ctx context.Context, _ time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

// The whole flow, end to end, with the real client: discover, authorize, poll
// past a pending response, approve in the "browser", and get a token carrying
// the claims that were typed.
func TestTheDeviceFlowRoundTripsWithTheRealClient(t *testing.T) {
	t.Parallel()

	srv := liveServer(t)
	issuer := srv.URL + "/oauth2/" + ServerNS
	ctx := context.Background()

	meta, err := deviceauth.Discover(ctx, fastClient(), issuer)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if meta.DeviceAuthorizationEndpoint != issuer+"/v1/device/authorize" {
		t.Errorf("DeviceAuthorizationEndpoint = %q", meta.DeviceAuthorizationEndpoint)
	}

	auth, err := deviceauth.Authorize(ctx, fastClient(), meta, deviceauth.Request{
		ClientID: "nsctl",
		Scopes:   []string{"openid", "groups"},
	})
	if err != nil {
		t.Fatalf("Authorize() error = %v", err)
	}
	if auth.UserCode == "" || auth.DeviceCode == "" {
		t.Fatalf("Authorize() = %+v", auth)
	}

	// Polling before approval must say pending, not fail.
	pendingErr := postToken(t, meta.TokenEndpoint, url.Values{
		"grant_type":  {deviceauth.GrantType},
		"device_code": {auth.DeviceCode},
	})
	if pendingErr != deviceauth.ErrAuthorizationPending {
		t.Errorf("token before approval = %q, want %q", pendingErr, deviceauth.ErrAuthorizationPending)
	}

	// The "browser": approve the code with a chosen claim set.
	approve(t, auth.VerificationURIComplete, auth.UserCode, "alice",
		"NeuronSphere Superset Admin - Local (none)")

	token, err := deviceauth.Poll(ctx, fastClient(), meta, auth, "nsctl", deviceauth.WithSleep(noWait))
	if err != nil {
		t.Fatalf("Poll() error = %v", err)
	}
	claims, err := DecodeClaims(token.AccessToken)
	if err != nil {
		t.Fatalf("DecodeClaims() error = %v", err)
	}
	if got, _ := claims["sub"].(string); got != "alice" {
		t.Errorf("sub = %v, want alice", claims["sub"])
	}
	// The groups are the reason the device path renders the same form: they
	// decide every policy decision, and a fixed user would test nothing.
	groups, _ := claims["groups"].([]any)
	if len(groups) != 1 || groups[0] != "NeuronSphere Superset Admin - Local (none)" {
		t.Errorf("groups = %v", claims["groups"])
	}
	if token.IDToken == "" {
		t.Error("no id_token; Authlib's parse_id_token is what logs a user in")
	}
}

// A device code is single use: a replay must not mint a second token.
func TestADeviceCodeIsSingleUse(t *testing.T) {
	t.Parallel()

	srv := liveServer(t)
	issuer := srv.URL + "/oauth2/" + ServerNS
	ctx := context.Background()

	meta, err := deviceauth.Discover(ctx, fastClient(), issuer)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := deviceauth.Authorize(ctx, fastClient(), meta, deviceauth.Request{ClientID: "nsctl"})
	if err != nil {
		t.Fatal(err)
	}
	approve(t, auth.VerificationURIComplete, auth.UserCode, "alice", "")

	if _, err := deviceauth.Poll(ctx, fastClient(), meta, auth, "nsctl", deviceauth.WithSleep(noWait)); err != nil {
		t.Fatalf("the first exchange failed: %v", err)
	}
	// The second must be refused, and as expired_token rather than pending --
	// pending would leave a replaying client polling forever.
	code := postToken(t, meta.TokenEndpoint, url.Values{
		"grant_type":  {deviceauth.GrantType},
		"device_code": {auth.DeviceCode},
	})
	if code != deviceauth.ErrExpiredToken {
		t.Errorf("replayed device code = %q, want %q", code, deviceauth.ErrExpiredToken)
	}
}

func TestAnUnknownDeviceCodeIsRefused(t *testing.T) {
	t.Parallel()

	srv := liveServer(t)
	endpoint := srv.URL + "/oauth2/" + ServerNS + "/v1/token"
	code := postToken(t, endpoint, url.Values{
		"grant_type":  {deviceauth.GrantType},
		"device_code": {"never-issued"},
	})
	if code != deviceauth.ErrExpiredToken {
		t.Errorf("unknown device code = %q, want %q", code, deviceauth.ErrExpiredToken)
	}
}

// Approving a code the server never issued must say so rather than silently
// succeed, or a user mistyping their code would wait forever with no clue.
func TestApprovingAnUnknownUserCodeReportsIt(t *testing.T) {
	t.Parallel()

	srv := liveServer(t)
	form := url.Values{"user_code": {"ZZZZ-ZZZZ"}, "username": {"alice"}}
	resp, err := http.PostForm(srv.URL+"/oauth2/"+ServerNS+"/v1/device", form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// Both discovery documents on both authorization servers must advertise the
// device endpoint and the grant, since consumers disagree about which name to
// ask for and the client tries both.
func TestBothDiscoveryDocumentsAdvertiseTheDeviceGrant(t *testing.T) {
	t.Parallel()

	handler := testServer(t).Handler()
	for _, path := range []string{
		"/oauth2/ns/.well-known/openid-configuration",
		"/oauth2/ns/.well-known/oauth-authorization-server",
		"/oauth2/services/.well-known/openid-configuration",
		"/oauth2/services/.well-known/oauth-authorization-server",
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s returned %d", path, rec.Code)
		}
		var doc map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
			t.Fatal(err)
		}
		endpoint, _ := doc["device_authorization_endpoint"].(string)
		if !strings.HasSuffix(endpoint, "/v1/device/authorize") {
			t.Errorf("%s: device_authorization_endpoint = %v", path, doc["device_authorization_endpoint"])
		}
		grants, _ := doc["grant_types_supported"].([]any)
		var found bool
		for _, g := range grants {
			if g == DeviceGrantType {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: grant_types_supported = %v, want it to include the device grant", path, grants)
		}
	}
}

// The verification page is what the user lands on, and it must carry the code
// so a browser opened at the complete URI needs nothing typed.
func TestTheVerificationPageIsPrefilledFromTheURI(t *testing.T) {
	t.Parallel()

	handler := testServer(t).Handler()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/oauth2/ns/v1/device?user_code=BCDF-GHJK", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "BCDF-GHJK") {
		t.Error("the sign-in page did not prefill the user code")
	}
	// The same claims-choosing box the redirect flow offers.
	if !strings.Contains(body, `name="groups"`) {
		t.Error("the device sign-in page offers no groups field")
	}
}

func TestNormalizeUserCodeAcceptsWhatAPersonTypes(t *testing.T) {
	t.Parallel()

	tests := []struct{ in, want string }{
		{"BCDF-GHJK", "BCDF-GHJK"},
		{"bcdfghjk", "BCDF-GHJK"},
		{"  bcdf-ghjk  ", "BCDF-GHJK"},
		{"BCDF GHJK", "BCDF-GHJK"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := normalizeUserCode(tt.in); got != tt.want {
			t.Errorf("normalizeUserCode(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// RFC 8628 section 6.1 asks that a user code be readable off one screen and
// typable into another.
func TestGeneratedUserCodesAvoidAmbiguousGlyphs(t *testing.T) {
	t.Parallel()

	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		code := generateUserCode()
		if len(code) != 9 || code[4] != '-' {
			t.Fatalf("generateUserCode() = %q, want XXXX-XXXX", code)
		}
		for _, r := range strings.ReplaceAll(code, "-", "") {
			if strings.ContainsRune("O0I1S5Z2", r) {
				t.Fatalf("generateUserCode() = %q, which contains the ambiguous %q", code, r)
			}
			if !strings.ContainsRune(userCodeAlphabet, r) {
				t.Fatalf("generateUserCode() = %q, which leaves the alphabet", code)
			}
		}
		seen[code] = true
	}
	// Not a randomness test; a generator returning a constant would make every
	// concurrent sign-in approve someone else's.
	if len(seen) < 150 {
		t.Errorf("only %d distinct codes in 200 draws", len(seen))
	}
}

// An expired code must read as expired rather than as pending, or the client
// polls until the process is killed.
func TestAnExpiredDeviceCodeIsNotPending(t *testing.T) {
	t.Parallel()

	key, err := LoadOrCreateKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer("http://auth.local.neuronsphere.io", key)

	now := time.Now()
	srv.now = func() time.Time { return now }
	deviceCode, userCode := srv.storeDevice(Request{Server: ServerNS})
	if !srv.approveDevice(userCode, func(*Request) {}) {
		t.Fatal("approveDevice() did not find the code it had just issued")
	}

	// Move past the code's lifetime.
	srv.now = func() time.Time { return now.Add(deviceCodeLifetime + time.Second) }
	if _, ok := srv.lookupDevice(deviceCode); ok {
		t.Error("an expired device code was still returned")
	}
	if srv.approveDevice(userCode, func(*Request) {}) {
		t.Error("an expired device code could still be approved")
	}
}

// postToken sends a token request and returns the OAuth error code, or "" on
// success.
func postToken(t *testing.T, endpoint string, form url.Values) string {
	t.Helper()
	resp, err := http.PostForm(endpoint, form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return body.Error
}

// approve plays the browser half: fetch the verification page, then submit the
// form with a chosen username and groups.
func approve(t *testing.T, verificationURI, userCode, username, groups string) {
	t.Helper()
	parsed, err := url.Parse(verificationURI)
	if err != nil {
		t.Fatal(err)
	}
	// The GET is not strictly needed to approve, but it is what a browser
	// does, and a page that 500s would otherwise go unnoticed.
	page, err := http.Get(verificationURI)
	if err != nil {
		t.Fatal(err)
	}
	page.Body.Close()
	if page.StatusCode != http.StatusOK {
		t.Fatalf("the verification page returned %d", page.StatusCode)
	}

	parsed.RawQuery = ""
	resp, err := http.PostForm(parsed.String(), url.Values{
		"user_code": {userCode},
		"username":  {username},
		"groups":    {groups},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("approving returned %d", resp.StatusCode)
	}
}

// offline_access must produce a refresh token, and that token must renew the
// session without a second visit to a browser. This is SPEC005's behaviour,
// and the mock has to implement it or the local platform cannot exercise it.
func TestOfflineAccessIssuesARefreshTokenThatRenews(t *testing.T) {
	t.Parallel()

	srv := liveServer(t)
	issuer := srv.URL + "/oauth2/" + ServerNS
	ctx := context.Background()

	meta, err := deviceauth.Discover(ctx, fastClient(), issuer)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := deviceauth.Authorize(ctx, fastClient(), meta, deviceauth.Request{
		ClientID: "nsctl",
		Scopes:   []string{"openid", "groups", ScopeOfflineAccess},
	})
	if err != nil {
		t.Fatal(err)
	}
	approve(t, auth.VerificationURIComplete, auth.UserCode, "alice", "some-group")

	token, err := deviceauth.Poll(ctx, fastClient(), meta, auth, "nsctl", deviceauth.WithSleep(noWait))
	if err != nil {
		t.Fatalf("Poll() error = %v", err)
	}
	if token.RefreshToken == "" {
		t.Fatal("offline_access was requested and no refresh token was issued")
	}

	renewed, err := deviceauth.Refresh(ctx, fastClient(), meta, token.RefreshToken, "nsctl")
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if renewed.AccessToken == "" {
		t.Fatal("the renewal carried no access token")
	}
	// The renewed token must describe the same person: a refresh that lost the
	// groups would silently downgrade every policy decision about them.
	claims, err := DecodeClaims(renewed.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := claims["sub"].(string); got != "alice" {
		t.Errorf("sub after refresh = %v, want alice", claims["sub"])
	}
	groups, _ := claims["groups"].([]any)
	if len(groups) != 1 || groups[0] != "some-group" {
		t.Errorf("groups after refresh = %v", claims["groups"])
	}

	// Rotated and single use: the old one must not work twice.
	if renewed.RefreshToken == "" {
		t.Error("the renewal issued no new refresh token")
	}
	if _, err := deviceauth.Refresh(ctx, fastClient(), meta, token.RefreshToken, "nsctl"); err == nil {
		t.Error("a used refresh token was accepted a second time")
	}
}

// Without offline_access there is no refresh token to leave lying around.
func TestNoOfflineAccessMeansNoRefreshToken(t *testing.T) {
	t.Parallel()

	srv := liveServer(t)
	issuer := srv.URL + "/oauth2/" + ServerNS
	ctx := context.Background()

	meta, err := deviceauth.Discover(ctx, fastClient(), issuer)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := deviceauth.Authorize(ctx, fastClient(), meta, deviceauth.Request{
		ClientID: "nsctl", Scopes: []string{"openid"},
	})
	if err != nil {
		t.Fatal(err)
	}
	approve(t, auth.VerificationURIComplete, auth.UserCode, "alice", "")
	token, err := deviceauth.Poll(ctx, fastClient(), meta, auth, "nsctl", deviceauth.WithSleep(noWait))
	if err != nil {
		t.Fatal(err)
	}
	if token.RefreshToken != "" {
		t.Error("a refresh token was issued to a client that did not ask for one")
	}
}

// The discovery document advertised this grant from the first version of this
// server and the token endpoint rejected it. Now that nsctl login depends on
// it, the advertisement has to be true.
func TestTheAdvertisedRefreshGrantIsImplemented(t *testing.T) {
	t.Parallel()

	srv := liveServer(t)
	endpoint := srv.URL + "/oauth2/" + ServerNS + "/v1/token"
	code := postToken(t, endpoint, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {"never-issued"},
	})
	// invalid_grant, not unsupported_grant_type: the grant is supported, the
	// token is not one of ours.
	if code != "invalid_grant" {
		t.Errorf("unknown refresh token = %q, want invalid_grant", code)
	}
}
