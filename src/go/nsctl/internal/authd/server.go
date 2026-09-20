package authd

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Server serves both authorization servers on one listener.
type Server struct {
	// Issuer is the base URL this is reached at, without a trailing slash.
	//
	// Configured rather than derived from the request's Host, and that is the
	// whole of the reachability problem. The `iss` claim has to be one string
	// however the server was reached -- browser through hmd_proxy, pod through
	// CoreDNS, Lambda through the Docker network alias -- because a consumer
	// that fetched JWKS from one name and reads `iss` as another rejects every
	// token. Deriving it from Host would produce three issuers and three sets
	// of failures that all look like bad policy.
	Issuer string

	key   *Key
	now   func() time.Time
	mu    sync.Mutex
	codes map[string]pendingAuth
	// devices holds issued device codes awaiting approval, beside the
	// authorization codes and under the same mutex.
	devices map[string]pendingDevice
	// refresh holds issued refresh tokens, same again.
	refresh map[string]pendingRefresh
}

// pendingAuth is an issued authorization code awaiting exchange.
type pendingAuth struct {
	request Request
	nonce   string
	expires time.Time
}

// codeLifetime is how long an authorization code is exchangeable. Short: the
// browser redirects straight into the exchange.
const codeLifetime = 5 * time.Minute

// NewServer builds a server signing with key and issuing under issuer.
func NewServer(issuer string, key *Key) *Server {
	return &Server{
		Issuer:  strings.TrimSuffix(issuer, "/"),
		key:     key,
		now:     time.Now,
		codes:   map[string]pendingAuth{},
		devices: map[string]pendingDevice{},
		refresh: map[string]pendingRefresh{},
	}
}

// Handler routes every endpoint, for both authorization servers.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, server := range Servers {
		prefix := "/oauth2/" + server
		name := server
		// Both discovery names. Okta publishes the OIDC one; Superset and
		// Airflow ask for the OAuth one -- `server_metadata_url` is built as
		// OKTA_BASE_URL + "/.well-known/oauth-authorization-server" in both --
		// and Trino asks for the OIDC one. Serving the same document at both
		// costs a route and removes a whole class of "which spec is this".
		mux.HandleFunc("GET "+prefix+"/.well-known/openid-configuration", s.metadata(name))
		mux.HandleFunc("GET "+prefix+"/.well-known/oauth-authorization-server", s.metadata(name))
		mux.HandleFunc("GET "+prefix+"/v1/keys", s.keys)
		mux.HandleFunc("POST "+prefix+"/v1/token", s.token(name))
		mux.HandleFunc("GET "+prefix+"/v1/authorize", s.authorize(name))
		mux.HandleFunc("POST "+prefix+"/v1/authorize", s.authorizeSubmit(name))
		mux.HandleFunc("GET "+prefix+"/v1/userinfo", s.userinfo)
		// RFC 8628. /v1/device/authorize is where the CLI asks for a code;
		// /v1/device is where the person approves it, and is the URL printed
		// on the terminal, so it is deliberately the shorter of the two.
		mux.HandleFunc("POST "+prefix+"/v1/device/authorize", s.deviceAuthorize(name))
		mux.HandleFunc("GET "+prefix+"/v1/device", s.deviceVerify(name))
		mux.HandleFunc("POST "+prefix+"/v1/device", s.deviceApprove(name))
	}
	// Not an Okta endpoint. This is the token vending machine the exercise is
	// for: a test asks for a claim set and gets a token, with no browser and no
	// client credentials in the way.
	mux.HandleFunc("POST /admin/token", s.adminToken)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "issuer": s.Issuer})
	})
	return mux
}

// metadata is the discovery document, identical for both discovery names.
func (s *Server) metadata(server string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		issuer := IssuerFor(s.Issuer, server)
		writeJSON(w, http.StatusOK, map[string]any{
			"issuer":                        issuer,
			"authorization_endpoint":        issuer + "/v1/authorize",
			"token_endpoint":                issuer + "/v1/token",
			"userinfo_endpoint":             issuer + "/v1/userinfo",
			"jwks_uri":                      issuer + "/v1/keys",
			"device_authorization_endpoint": issuer + "/v1/device/authorize",
			"response_types_supported":      []string{"code"},
			"grant_types_supported": []string{
				"authorization_code", "client_credentials", "refresh_token", DeviceGrantType,
			},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
			"scopes_supported":                      []string{"openid", "email", "profile", "groups", "offline_access"},
			"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post"},
			"claims_supported": []string{
				"iss", "aud", "sub", "cid", "iat", "exp", "groups", "scp",
				"preferred_username", "name", "email",
			},
		})
	}
}

func (s *Server) keys(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.key.JWKS())
}

// token serves client_credentials and authorization_code.
//
// Client credentials are never checked against a registry, because there is no
// registry: a client's secret is its id (see the local Okta branch in
// hmd-lib-cdktf-factories, and the local Postgres convention it follows). The
// alternative was an admin API that every deploy would have to call before it
// could authenticate, which is a registration protocol invented to guard
// nothing.
func (s *Server) token(server string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			oauthError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		switch grant := r.PostFormValue("grant_type"); grant {
		case "client_credentials":
			clientID, _ := clientCredentials(r)
			req := Request{
				Server:   server,
				Subject:  clientID,
				ClientID: clientID,
				Scopes:   strings.Fields(r.PostFormValue("scope")),
			}
			s.issue(w, server, req, "")
		case "authorization_code":
			code := r.PostFormValue("code")
			pending, ok := s.claimCode(code)
			if !ok {
				oauthError(w, http.StatusBadRequest, "invalid_grant",
					"that authorization code is unknown or has expired")
				return
			}
			s.issue(w, server, pending.request, pending.nonce)
		case DeviceGrantType:
			s.deviceToken(w, server, r)
		case "refresh_token":
			s.refreshGrant(w, server, r)
		default:
			oauthError(w, http.StatusBadRequest, "unsupported_grant_type",
				fmt.Sprintf("%q is not supported; use client_credentials, authorization_code or %s",
					grant, DeviceGrantType))
		}
	}
}

// issue mints the access token, and an id_token beside it.
//
// Both, always. Authlib's parse_id_token is what Superset and Airflow read the
// user out of, so an access token alone logs a user in as nobody; and
// hmd-lib-auth verifies the access token. Minting one set of claims into both
// keeps the two views of a session from disagreeing.
func (s *Server) issue(w http.ResponseWriter, server string, req Request, nonce string) {
	req.Server = server
	now := s.now()
	access, err := Mint(s.key, s.Issuer, req, now)
	if err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	idReq := req
	idReq.Claims = map[string]any{}
	for k, v := range req.Claims {
		idReq.Claims[k] = v
	}
	// The id_token's audience is the client, per OIDC, not the API.
	idReq.Audience = req.ClientID
	if nonce != "" {
		idReq.Claims["nonce"] = nonce
	}
	idToken, err := Mint(s.key, s.Issuer, idReq, now)
	if err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	body := map[string]any{
		"token_type":   "Bearer",
		"expires_in":   int(DefaultLifetime.Seconds()),
		"access_token": access,
		"id_token":     idToken,
		"scope":        strings.Join(req.Scopes, " "),
	}
	// Only when asked for. A service on client_credentials already holds its
	// own credentials, and a refresh token it never wanted is a long-lived
	// secret nobody agreed to look after.
	if wantsOfflineAccess(req.Scopes) {
		body["refresh_token"] = s.storeRefresh(req)
	}
	writeJSON(w, http.StatusOK, body)
}

// authorize renders the sign-in form.
//
// A form rather than an automatic redirect, because choosing the claims is the
// feature. The groups a user carries decide their Superset and Airflow role and
// every Rego decision about them, so being able to type a different set and log
// in again is the difference between testing authorisation and watching it
// succeed.
func (s *Server) authorize(server string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("redirect_uri") == "" {
			oauthError(w, http.StatusBadRequest, "invalid_request", "redirect_uri is required")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = signInForm.Execute(w, map[string]any{
			"Action":      IssuerFor(s.Issuer, server) + "/v1/authorize",
			"Server":      server,
			"ClientID":    q.Get("client_id"),
			"RedirectURI": q.Get("redirect_uri"),
			"State":       q.Get("state"),
			"Scope":       q.Get("scope"),
			"Nonce":       q.Get("nonce"),
		})
	}
}

// authorizeSubmit turns the form into a code and redirects back.
func (s *Server) authorizeSubmit(server string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			oauthError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		redirectURI := r.PostFormValue("redirect_uri")
		target, err := url.Parse(redirectURI)
		if err != nil || redirectURI == "" {
			oauthError(w, http.StatusBadRequest, "invalid_request", "redirect_uri is missing or unparseable")
			return
		}
		subject := r.PostFormValue("username")
		if subject == "" {
			subject = "local-user"
		}
		req := Request{
			Server:   server,
			Subject:  subject,
			ClientID: r.PostFormValue("client_id"),
			Groups:   splitLines(r.PostFormValue("groups")),
			Scopes:   strings.Fields(r.PostFormValue("scope")),
		}
		code := s.storeCode(req, r.PostFormValue("nonce"))

		q := target.Query()
		q.Set("code", code)
		if state := r.PostFormValue("state"); state != "" {
			q.Set("state", state)
		}
		target.RawQuery = q.Encode()
		http.Redirect(w, r, target.String(), http.StatusFound)
	}
}

// userinfo returns the bearer token's own claims.
//
// Read back from the token rather than from a user store, because there is no
// user store -- the token is the only description of the user that exists.
func (s *Server) userinfo(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	claims, err := DecodeClaims(strings.TrimSpace(token))
	if err != nil {
		oauthError(w, http.StatusUnauthorized, "invalid_token", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, claims)
}

// adminToken mints a token from a claim set, with no flow at all.
func (s *Server) adminToken(w http.ResponseWriter, r *http.Request) {
	var req Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	server := req.Server
	if server == "" {
		server = ServerNS
	}
	token, err := Mint(s.key, s.Issuer, req, s.now())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": token,
		"token_type":   "Bearer",
		"issuer":       IssuerFor(s.Issuer, server),
	})
}

func (s *Server) storeCode(req Request, nonce string) string {
	raw := make([]byte, 24)
	_, _ = rand.Read(raw)
	code := base64.RawURLEncoding.EncodeToString(raw)

	s.mu.Lock()
	defer s.mu.Unlock()
	// Swept on write rather than on a timer: the map only grows when someone
	// logs in, so the moment of growth is the right moment to prune.
	now := s.now()
	for existing, pending := range s.codes {
		if now.After(pending.expires) {
			delete(s.codes, existing)
		}
	}
	s.codes[code] = pendingAuth{request: req, nonce: nonce, expires: now.Add(codeLifetime)}
	return code
}

// claimCode consumes a code. Single use: an exchanged code is deleted whether
// or not the exchange then succeeds.
func (s *Server) claimCode(code string) (pendingAuth, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pending, ok := s.codes[code]
	delete(s.codes, code)
	if !ok || s.now().After(pending.expires) {
		return pendingAuth{}, false
	}
	return pending, true
}

// clientCredentials reads the client id and secret from either place the spec
// allows. Both are accepted because Authlib and Trino disagree about which to
// use, and neither is configurable.
func clientCredentials(r *http.Request) (id, secret string) {
	if id, secret, ok := r.BasicAuth(); ok {
		return id, secret
	}
	return r.PostFormValue("client_id"), r.PostFormValue("client_secret")
}

func splitLines(value string) []string {
	var out []string
	for _, line := range strings.FieldsFunc(value, func(r rune) bool { return r == '\n' || r == '\r' }) {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// oauthError is the error shape RFC 6749 defines, which is what the clients
// parse. A plain 400 with prose gets reported by Authlib as "invalid response".
func oauthError(w http.ResponseWriter, status int, code, description string) {
	writeJSON(w, status, map[string]any{"error": code, "error_description": description})
}

var signInForm = template.Must(template.New("signin").Parse(`<!doctype html>
<title>NeuronSphere local sign-in</title>
<style>
 body{font:14px system-ui,sans-serif;max-width:32rem;margin:4rem auto;padding:0 1rem}
 label{display:block;margin:1rem 0 .25rem;font-weight:600}
 input,textarea{width:100%;padding:.5rem;font:inherit;box-sizing:border-box}
 textarea{height:7rem;font-family:ui-monospace,monospace}
 button{margin-top:1.5rem;padding:.6rem 1.2rem;font:inherit}
 p{color:#555}
</style>
<h1>Sign in ({{.Server}})</h1>
<p>This is the local mock identity provider. Whatever you type below becomes the
token's claims &mdash; the groups decide your role in the application and every
policy decision about you.</p>
<form method="post" action="{{.Action}}">
 <input type="hidden" name="client_id" value="{{.ClientID}}">
 <input type="hidden" name="redirect_uri" value="{{.RedirectURI}}">
 <input type="hidden" name="state" value="{{.State}}">
 <input type="hidden" name="scope" value="{{.Scope}}">
 <input type="hidden" name="nonce" value="{{.Nonce}}">
 <label for="username">Username</label>
 <input id="username" name="username" value="local-user" autofocus>
 <label for="groups">Groups, one per line</label>
 <textarea id="groups" name="groups" spellcheck="false"></textarea>
 <button type="submit">Sign in</button>
</form>
`))
