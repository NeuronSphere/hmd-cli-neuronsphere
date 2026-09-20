package authd

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	key, err := LoadOrCreateKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return NewServer("http://auth.local.neuronsphere.io", key)
}

// The key is on disk so that a container restart does not invalidate every
// token already issued -- which looks like an authentication bug from a browser
// session that was working a second earlier.
func TestTheSigningKeySurvivesARestart(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	first, err := LoadOrCreateKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadOrCreateKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID() != second.ID() {
		t.Errorf("kid changed across loads: %s then %s", first.ID(), second.ID())
	}
	if first.private.N.Cmp(second.private.N) != 0 {
		t.Error("a second load generated a new key instead of reading the stored one")
	}
}

// The whole point of publishing a JWKS is that someone can verify a token with
// it. okta_jwt_verifier fetches /v1/keys, matches on kid and checks the
// signature; this does the same thing, so a mistake in either the JWK encoding
// or the signing input fails here rather than inside Python.
func TestAPublishedKeyVerifiesATokenThisServerSigned(t *testing.T) {
	t.Parallel()

	key, err := LoadOrCreateKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	token, err := Mint(key, "http://auth.local", Request{Server: ServerServices}, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	jwk := key.JWKS()["keys"].([]any)[0].(map[string]any)
	if jwk["kid"] != key.ID() {
		t.Fatalf("the published kid %v is not the signing kid %s", jwk["kid"], key.ID())
	}
	modulus, err := base64.RawURLEncoding.DecodeString(jwk["n"].(string))
	if err != nil {
		t.Fatal(err)
	}
	exponent, err := base64.RawURLEncoding.DecodeString(jwk["e"].(string))
	if err != nil {
		t.Fatal(err)
	}
	published := &rsa.PublicKey{
		N: new(big.Int).SetBytes(modulus),
		E: int(new(big.Int).SetBytes(exponent).Int64()),
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d segments, want 3", len(parts))
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	if err := rsa.VerifyPKCS1v15(published, crypto.SHA256, digest[:], signature); err != nil {
		t.Errorf("a token this server signed does not verify against the key it publishes: %v", err)
	}
}

// Every Rego bundle's token_is_valid admits exactly two audiences, and which
// one a token carries is the difference between a service call and a user call.
// One server issuing both would make that untestable.
func TestTheTwoServersIssueTheTwoAudiences(t *testing.T) {
	t.Parallel()

	key, err := LoadOrCreateKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for server, want := range map[string]string{
		ServerNS:       AudienceNS,
		ServerServices: AudienceServices,
	} {
		token, err := Mint(key, "http://auth.local", Request{Server: server}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		claims, err := DecodeClaims(token)
		if err != nil {
			t.Fatal(err)
		}
		if claims["aud"] != want {
			t.Errorf("%s server issued aud %v, want %s", server, claims["aud"], want)
		}
		if claims["iss"] != "http://auth.local/oauth2/"+server {
			t.Errorf("%s server issued iss %v", server, claims["iss"])
		}
	}
}

// A policy that indexes token.payload.groups[_] against a missing key is
// undefined, not false -- which reads as a broken policy rather than a token
// without groups. So both list claims are always present.
func TestListClaimsAreAlwaysArraysNeverNull(t *testing.T) {
	t.Parallel()

	key, err := LoadOrCreateKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	token, err := Mint(key, "http://auth.local", Request{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	claims, err := DecodeClaims(token)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"groups", "scp"} {
		value, present := claims[name]
		if !present {
			t.Errorf("%s is absent", name)
			continue
		}
		if _, ok := value.([]any); !ok {
			t.Errorf("%s is %T, want an array", name, value)
		}
	}
}

// The reason this exists at all: a policy reading a claim nobody has invented
// yet still has to be testable, so arbitrary claims win over the defaults.
func TestCustomClaimsOverrideTheDefaults(t *testing.T) {
	t.Parallel()

	key, err := LoadOrCreateKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	token, err := Mint(key, "http://auth.local", Request{
		Subject: "alice",
		Groups:  []string{"NeuronSphere Airflow Admin - Local (none)"},
		Claims:  map[string]any{"email": "alice@example.test", "department": "platform"},
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	claims, err := DecodeClaims(token)
	if err != nil {
		t.Fatal(err)
	}
	if claims["email"] != "alice@example.test" {
		t.Errorf("email = %v, want the overridden value", claims["email"])
	}
	if claims["department"] != "platform" {
		t.Errorf("an unknown claim was dropped: %v", claims["department"])
	}
	groups, _ := claims["groups"].([]any)
	if len(groups) != 1 || groups[0] != "NeuronSphere Airflow Admin - Local (none)" {
		t.Errorf("groups = %v", claims["groups"])
	}
}

// A discovery document is a promise about four URLs, and Authlib and Trino both
// take it literally. Advertising an endpoint that 404s is worse than not
// advertising it, so every URL the document names is fetched here.
func TestEveryAdvertisedEndpointResolves(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	handler := srv.Handler()

	for _, discovery := range []string{
		"/oauth2/ns/.well-known/openid-configuration",
		"/oauth2/ns/.well-known/oauth-authorization-server",
		"/oauth2/services/.well-known/openid-configuration",
		"/oauth2/services/.well-known/oauth-authorization-server",
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, discovery, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s returned %d", discovery, rec.Code)
		}
		var doc map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
			t.Fatal(err)
		}

		for _, field := range []string{"authorization_endpoint", "token_endpoint", "userinfo_endpoint", "jwks_uri"} {
			advertised, ok := doc[field].(string)
			if !ok {
				t.Errorf("%s omits %s", discovery, field)
				continue
			}
			if !strings.HasPrefix(advertised, srv.Issuer) {
				t.Errorf("%s advertises %s outside the issuer: %s", discovery, field, advertised)
				continue
			}
			parsed, err := url.Parse(advertised)
			if err != nil {
				t.Fatal(err)
			}
			// GET everything; the token endpoint is POST-only, so a 405 there
			// still proves the route exists, which is what is being asked.
			probe := httptest.NewRecorder()
			handler.ServeHTTP(probe, httptest.NewRequest(http.MethodGet, parsed.Path+"?redirect_uri=http://app.local/cb", nil))
			if probe.Code == http.StatusNotFound {
				t.Errorf("%s advertises %s -> %s, which does not exist", discovery, field, advertised)
			}
		}
	}
}

// Client credentials, the grant a service account uses. No registry is
// consulted: the client's secret is its id, which is the same convention local
// Postgres users follow, and inventing a registration protocol to guard nothing
// would only mean every deploy had to call it first.
func TestClientCredentialsMintsAServiceToken(t *testing.T) {
	t.Parallel()

	handler := testServer(t).Handler()
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {"ms-deployment"},
		"client_secret": {"ms-deployment"},
		"scope":         {"openid groups"},
	}
	req := httptest.NewRequest(http.MethodPost, "/oauth2/services/v1/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("token endpoint returned %d: %s", rec.Code, rec.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	// Both, always: hmd-lib-auth verifies the access token while Authlib reads
	// the user out of the id_token, and a response with one of them logs a user
	// in as nobody.
	for _, field := range []string{"access_token", "id_token"} {
		token, ok := body[field].(string)
		if !ok || token == "" {
			t.Fatalf("%s missing from %v", field, body)
		}
	}
	claims, err := DecodeClaims(body["access_token"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if claims["aud"] != AudienceServices {
		t.Errorf("aud = %v, want the services audience", claims["aud"])
	}
	if claims["sub"] != "ms-deployment" || claims["cid"] != "ms-deployment" {
		t.Errorf("sub/cid = %v/%v, want the client id", claims["sub"], claims["cid"])
	}
}

// The browser flow, end to end, and the single-use property of the code.
func TestTheAuthorizationCodeFlowRoundTripsAndTheCodeIsSingleUse(t *testing.T) {
	t.Parallel()

	handler := testServer(t).Handler()

	form := url.Values{
		"client_id":    {"superset"},
		"redirect_uri": {"http://superset.local.neuronsphere.io/oauth-authorized/okta"},
		"state":        {"opaque-state"},
		"scope":        {"openid email profile groups"},
		"username":     {"alice"},
		"groups":       {"NeuronSphere Superset Admin - Local (none)\nNeuronSphere Superset Gamma - Local (none)"},
	}
	req := httptest.NewRequest(http.MethodPost, "/oauth2/ns/v1/authorize", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("authorize returned %d: %s", rec.Code, rec.Body)
	}
	location, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if location.Query().Get("state") != "opaque-state" {
		t.Errorf("state was not round-tripped: %s", location)
	}
	code := location.Query().Get("code")
	if code == "" {
		t.Fatal("no code in the redirect")
	}

	exchange := func() *httptest.ResponseRecorder {
		body := url.Values{"grant_type": {"authorization_code"}, "code": {code}}
		r := httptest.NewRequest(http.MethodPost, "/oauth2/ns/v1/token", strings.NewReader(body.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}

	first := exchange()
	if first.Code != http.StatusOK {
		t.Fatalf("exchange returned %d: %s", first.Code, first.Body)
	}
	var tokens map[string]any
	if err := json.Unmarshal(first.Body.Bytes(), &tokens); err != nil {
		t.Fatal(err)
	}
	claims, err := DecodeClaims(tokens["id_token"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if claims["email"] != "alice@local.neuronsphere.io" {
		t.Errorf("email = %v; Flask-AppBuilder stores this on the user record", claims["email"])
	}
	if claims["preferred_username"] != "alice" {
		t.Errorf("preferred_username = %v; this is what the apps key the user on", claims["preferred_username"])
	}
	groups, _ := claims["groups"].([]any)
	if len(groups) != 2 {
		t.Errorf("groups = %v, want the two lines from the form", claims["groups"])
	}
	// The id_token's audience is the client, per OIDC -- not the API audience
	// the access token carries.
	if claims["aud"] != "superset" {
		t.Errorf("id_token aud = %v, want the client id", claims["aud"])
	}

	if second := exchange(); second.Code == http.StatusOK {
		t.Error("the same authorization code was exchanged twice")
	}
}

// The token vending machine: a claim set in, a token out, no flow in the way.
func TestAdminTokenMintsFromAClaimSet(t *testing.T) {
	t.Parallel()

	handler := testServer(t).Handler()
	body := strings.NewReader(`{"server":"services","subject":"tester",
	  "groups":["ops"],"scopes":["service"],"claims":{"tenant":"acme"}}`)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/token", body))

	if rec.Code != http.StatusOK {
		t.Fatalf("admin mint returned %d: %s", rec.Code, rec.Body)
	}
	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	claims, err := DecodeClaims(response["access_token"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if claims["tenant"] != "acme" {
		t.Errorf("the custom claim did not survive: %v", claims)
	}
	if claims["aud"] != AudienceServices {
		t.Errorf("aud = %v", claims["aud"])
	}
}

// An unknown server is named rather than silently treated as the default: a
// typo that quietly issued the wrong audience would fail much later, in a
// policy decision, looking like the policy was wrong.
func TestAnUnknownAuthorizationServerIsRefused(t *testing.T) {
	t.Parallel()

	key, err := LoadOrCreateKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, err = Mint(key, "http://auth.local", Request{Server: "typo"}, time.Now())
	if err == nil {
		t.Fatal("an unknown server was accepted")
	}
	for _, want := range []string{"typo", ServerNS, ServerServices} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q: %v", want, err)
		}
	}
}
