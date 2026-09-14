package authd

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"time"
)

// ScopeOfflineAccess is what a client asks for to be given a refresh token.
const ScopeOfflineAccess = "offline_access"

// refreshLifetime is how long a refresh token stays usable.
//
// Much longer than the hour an access token lasts, because the whole point is
// to survive between working sessions: a refresh token that expired with the
// day would leave `nsctl login` asking for a browser every morning, which is
// the behaviour it exists to remove.
const refreshLifetime = 30 * 24 * time.Hour

// pendingRefresh is an issued refresh token and the session it renews.
type pendingRefresh struct {
	request Request
	expires time.Time
}

// wantsOfflineAccess reports whether these scopes asked for a refresh token.
//
// Asked for, rather than always issued: a service using client_credentials has
// its own credentials and needs no refresh token, and handing one out anyway
// would be a long-lived secret nobody asked to be responsible for.
func wantsOfflineAccess(scopes []string) bool {
	for _, scope := range scopes {
		if scope == ScopeOfflineAccess {
			return true
		}
	}
	return false
}

// storeRefresh issues a refresh token for a session.
func (s *Server) storeRefresh(req Request) string {
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	token := base64.RawURLEncoding.EncodeToString(raw)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refresh == nil {
		s.refresh = map[string]pendingRefresh{}
	}
	now := s.now()
	for existing, pending := range s.refresh {
		if now.After(pending.expires) {
			delete(s.refresh, existing)
		}
	}
	s.refresh[token] = pendingRefresh{request: req, expires: now.Add(refreshLifetime)}
	return token
}

// claimRefresh consumes a refresh token.
//
// Single use, and a new one is issued alongside the new access token --
// rotation, which is what Okta and Auth0 both do by default. It also means the
// client's "keep the old one when the server returns none" branch is not the
// path exercised here, so both halves of that behaviour are reachable.
func (s *Server) claimRefresh(token string) (Request, bool) {
	if token == "" {
		return Request{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	pending, ok := s.refresh[token]
	delete(s.refresh, token)
	if !ok || s.now().After(pending.expires) {
		return Request{}, false
	}
	return pending.request, true
}

// refreshToken is the refresh_token branch of the token endpoint.
//
// This grant was advertised in the discovery document from the first version of
// this server and rejected by the token endpoint, which was a lie a consumer
// could only discover at runtime. `nsctl login` renews rather than
// re-authenticates whenever it holds a refresh token, so the mock has to
// implement what it advertises or the local platform cannot exercise that path.
func (s *Server) refreshGrant(w http.ResponseWriter, server string, r *http.Request) {
	req, ok := s.claimRefresh(r.PostFormValue("refresh_token"))
	if !ok {
		oauthError(w, http.StatusBadRequest, "invalid_grant",
			"that refresh token is unknown, has expired, or has already been used")
		return
	}
	s.issue(w, server, req, "")
}
