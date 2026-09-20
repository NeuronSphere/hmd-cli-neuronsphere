package authd

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// The two authorization servers, because hmd-lib-auth already distinguishes
// them and the policies check the difference.
//
// lambda_helper._get_issuer_and_audience picks `services_issuer` with audience
// `api://neuronsphere-services` for a service account and `ns_issuer` with
// `api://neuronsphere` otherwise, and every Rego bundle's token_is_valid admits
// exactly those two audiences:
//
//	auds := ["api://neuronsphere", "api://neuronsphere-services"]
//	token.payload.aud == auds[_]
//
// One server issuing both audiences would have made that distinction
// untestable, which is most of what there is to test.
const (
	ServerNS       = "ns"
	ServerServices = "services"

	AudienceNS       = "api://neuronsphere"
	AudienceServices = "api://neuronsphere-services"
)

// Servers is every authorization server this serves, in a stable order.
var Servers = []string{ServerNS, ServerServices}

// AudienceFor is the audience a server issues by default.
func AudienceFor(server string) string {
	if server == ServerServices {
		return AudienceServices
	}
	return AudienceNS
}

// IssuerFor is a server's issuer URL: the `iss` claim, and the base every
// endpoint hangs off.
//
// Okta's own shape, `{host}/oauth2/{server}`, so the paths underneath are
// `/oauth2/{server}/v1/token` and the rest -- which is what the consumers build
// by appending to the issuer they were configured with.
func IssuerFor(base, server string) string {
	return strings.TrimSuffix(base, "/") + "/oauth2/" + server
}

// KnownServer reports whether a name is one of the two.
func KnownServer(server string) bool {
	for _, s := range Servers {
		if s == server {
			return true
		}
	}
	return false
}

// defaultEmailDomain is only ever seen locally; it exists so the address has a
// domain at all.
const defaultEmailDomain = "local.neuronsphere.io"

// DefaultLifetime matches Okta's default access-token lifetime. Long enough
// that a browser session outlives a deploy, short enough that a stale token in
// a script is noticed rather than inherited.
const DefaultLifetime = time.Hour

// Request describes a token to mint. Every field is optional; the zero value
// yields a service token for the `ns` server.
// Tagged because this is the body of POST /admin/token, not only an internal
// struct: Go's case-insensitive field matching would accept these anyway, but
// then the wire format would be whatever the field names happened to be.
type Request struct {
	Server   string   `json:"server,omitempty"`
	Subject  string   `json:"subject,omitempty"`
	Audience string   `json:"audience,omitempty"`
	ClientID string   `json:"client_id,omitempty"`
	Groups   []string `json:"groups,omitempty"`
	Scopes   []string `json:"scopes,omitempty"`
	// Claims are merged last and may override anything above. This is the
	// "various custom claims" the whole exercise is for: a policy that reads a
	// claim nobody has invented yet still has to be testable.
	Claims map[string]any `json:"claims,omitempty"`
	// LifetimeSeconds rather than a time.Duration, which JSON renders as a
	// nanosecond count no caller would guess.
	LifetimeSeconds int `json:"lifetime_seconds,omitempty"`
}

// lifetime is the request's token lifetime, or the default.
func (r Request) lifetime() time.Duration {
	if r.LifetimeSeconds > 0 {
		return time.Duration(r.LifetimeSeconds) * time.Second
	}
	return DefaultLifetime
}

// Mint builds and signs the claim set for a request.
//
// The claim names are not arbitrary. `groups` is what both apps' user-info
// mapping reads; `scp` is Okta's scope claim and what shared.rego reaches for;
// `sub` and `cid` are what the OPA authorizer uses for its principal, in that
// order of preference; `preferred_username`, `name` and `email` are what
// CustomSsoSecurityManager returns for the user record. A claim missing here is
// a KeyError several layers away in Python.
func Mint(key *Key, base string, req Request, now time.Time) (string, error) {
	server := req.Server
	if server == "" {
		server = ServerNS
	}
	if !KnownServer(server) {
		return "", fmt.Errorf("unknown authorization server %q; known: %s",
			server, strings.Join(Servers, ", "))
	}

	audience := req.Audience
	if audience == "" {
		audience = AudienceFor(server)
	}
	subject := req.Subject
	if subject == "" {
		subject = "local-user"
	}
	clientID := req.ClientID
	if clientID == "" {
		clientID = subject
	}
	lifetime := req.lifetime()

	claims := map[string]any{
		"iss": IssuerFor(base, server),
		"aud": audience,
		"sub": subject,
		"cid": clientID,
		"iat": now.Unix(),
		"exp": now.Add(lifetime).Unix(),
		// Okta stamps these and some consumers log them; cheap to be faithful.
		"ver":                1,
		"uid":                subject,
		"preferred_username": subject,
		"name":               subject,
		// A plausible address rather than the bare subject: Flask-AppBuilder
		// stores this on the user record and Superset shows it, and a user
		// whose email is "alice" reads as a bug in the mapping.
		"email": subject + "@" + defaultEmailDomain,
	}
	// Always present, never null: a policy indexing token.payload.groups[_]
	// against a missing key is undefined rather than false, which reads as a
	// policy bug rather than a token without groups.
	claims["groups"] = orEmpty(req.Groups)
	claims["scp"] = orEmpty(req.Scopes)

	for k, v := range req.Claims {
		claims[k] = v
	}
	return key.Sign(claims)
}

// orEmpty renders a nil slice as [] rather than null.
func orEmpty(values []string) []any {
	out := make([]any, 0, len(values))
	for _, v := range values {
		out = append(out, v)
	}
	return out
}

// DecodeClaims reads a JWT's payload without verifying it.
//
// Deliberately unverified, and safe in both callers. The userinfo endpoint is
// reading back a token this server signed moments earlier, and the CLI's decode
// is a debugging aid. Everything that makes an authorisation decision on these
// claims does so in Python, against JWKS -- and the Rego policies do the same
// thing this does, io.jwt.decode, which is the reason the local platform can
// test policy without a trusted issuer at all.
func DecodeClaims(token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("not a compact JWT: want three dot-separated segments, got %d", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decoding the payload: %w", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("parsing the payload: %w", err)
	}
	return claims, nil
}
