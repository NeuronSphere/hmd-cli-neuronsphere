// Package deviceauth is an OAuth 2.0 device authorization grant client
// (RFC 8628).
//
// The device grant is the right flow for a CLI and the browser-redirect flow
// the Python `hmd login` uses is not. That one runs a Flask server on
// localhost:8082, opens a browser on the same machine, and watches the token
// file's mtime to learn it worked -- so it cannot authenticate a session over
// SSH, inside a container, or on a machine with no browser, and it needs a
// port nothing else has taken. The device grant needs none of that: the user
// reads a code off the terminal and types it into whatever browser they have,
// anywhere.
//
// Nothing here knows about nsctl. It speaks the RFC against whatever endpoint
// discovery names, which is what lets the same code run against a customer's
// own Okta or Auth0 issuer and against the local mock in internal/authd,
// neither of which it can tell apart.
package deviceauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// GrantType is the device grant's RFC 8628 identifier.
const GrantType = "urn:ietf:params:oauth:grant-type:device_code"

// DefaultInterval is how often to poll when the server names no interval.
// RFC 8628 section 3.2 fixes this default at five seconds.
const DefaultInterval = 5 * time.Second

// SlowDownIncrement is how much a `slow_down` response adds to the interval.
// Section 3.5 requires an increase of five seconds.
const SlowDownIncrement = 5 * time.Second

// DefaultExpiry bounds a flow whose server named no expires_in.
const DefaultExpiry = 10 * time.Minute

// DefaultTimeout is the per-request budget.
//
// Thirty seconds, not the librarian's five minutes: every request here is a
// small JSON exchange, and a login that has silently hung is worth reporting
// long before a multi-megabyte artifact pull would be.
const DefaultTimeout = 30 * time.Second

// NewHTTPClient is the client this package expects.
func NewHTTPClient() *http.Client { return &http.Client{Timeout: DefaultTimeout} }

// Request is what to ask for.
type Request struct {
	ClientID string
	Scopes   []string
	Audience string
}

// Authorization is the device authorization response.
type Authorization struct {
	DeviceCode string `json:"device_code"`
	UserCode   string `json:"user_code"`
	// VerificationURI is where the user signs in.
	VerificationURI string `json:"verification_uri"`
	// VerificationURIComplete carries the user code in the URL, so a user
	// whose browser opens has nothing to type. Optional per the RFC.
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// PollInterval is the cadence to poll at, honouring the server's request and
// falling back to the RFC's default.
func (a *Authorization) PollInterval() time.Duration {
	if a.Interval > 0 {
		return time.Duration(a.Interval) * time.Second
	}
	return DefaultInterval
}

// Expiry is how long the user has, falling back to a bounded default so a
// server that names none cannot make this poll forever.
func (a *Authorization) Expiry() time.Duration {
	if a.ExpiresIn > 0 {
		return time.Duration(a.ExpiresIn) * time.Second
	}
	return DefaultExpiry
}

// BrowserURL is the URL to show the user: the complete one when the server
// supplied it, the plain one otherwise.
func (a *Authorization) BrowserURL() string {
	if a.VerificationURIComplete != "" {
		return a.VerificationURIComplete
	}
	return a.VerificationURI
}

// Token is a successful token response.
type Token struct {
	AccessToken  string `json:"access_token"`
	IDToken      string `json:"id_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
}

// ExpiresAt is when the access token stops being usable, or the zero time
// when the server named no lifetime.
func (t *Token) ExpiresAt(now time.Time) time.Time {
	if t.ExpiresIn <= 0 {
		return time.Time{}
	}
	return now.Add(time.Duration(t.ExpiresIn) * time.Second)
}

// Error is an OAuth error response.
//
// It carries the operation, the status and the body for the same reason
// librarian.Error and msdeploy.Error do: the status alone rarely says what was
// wrong, and the server's own error_description usually does.
type Error struct {
	Operation   string
	Status      int
	Code        string
	Description string
	Body        string
}

func (e *Error) Error() string {
	detail := e.Description
	if detail == "" {
		detail = strings.TrimSpace(e.Body)
	}
	if len(detail) > 600 {
		detail = detail[:600] + "..."
	}
	switch {
	case e.Code != "" && detail != "":
		return fmt.Sprintf("%s: %s: %s", e.Operation, e.Code, detail)
	case e.Code != "":
		return fmt.Sprintf("%s: %s", e.Operation, e.Code)
	default:
		return fmt.Sprintf("%s: HTTP %d: %s", e.Operation, e.Status, detail)
	}
}

// The error codes RFC 8628 section 3.5 defines for the polling exchange.
const (
	// ErrAuthorizationPending means the user has not finished yet. Keep polling.
	ErrAuthorizationPending = "authorization_pending"
	// ErrSlowDown means the same, and that the interval must increase.
	ErrSlowDown = "slow_down"
	// ErrAccessDenied means the user refused. Terminal.
	ErrAccessDenied = "access_denied"
	// ErrExpiredToken means they took too long. Terminal.
	ErrExpiredToken = "expired_token"
)

// Pending reports whether this error means "not yet" rather than "no".
func (e *Error) Pending() bool {
	return e.Code == ErrAuthorizationPending || e.Code == ErrSlowDown
}

// Authorize starts the flow, returning the code and URL to show the user.
func Authorize(ctx context.Context, client *http.Client, meta *Metadata, req Request) (*Authorization, error) {
	if meta == nil || meta.DeviceAuthorizationEndpoint == "" {
		return nil, fmt.Errorf("this authorization server publishes no device_authorization_endpoint")
	}
	form := url.Values{}
	form.Set("client_id", req.ClientID)
	if len(req.Scopes) > 0 {
		form.Set("scope", strings.Join(req.Scopes, " "))
	}
	if req.Audience != "" {
		form.Set("audience", req.Audience)
	}

	var auth Authorization
	if err := postForm(ctx, client, meta.DeviceAuthorizationEndpoint, form, "requesting a device code", &auth); err != nil {
		return nil, err
	}
	if auth.DeviceCode == "" || auth.UserCode == "" {
		return nil, fmt.Errorf("requesting a device code: the response carried no device_code or user_code")
	}
	if auth.VerificationURI == "" && auth.VerificationURIComplete == "" {
		return nil, fmt.Errorf("requesting a device code: the response named no verification_uri")
	}
	return &auth, nil
}

// PollOption adjusts how Poll waits.
type PollOption func(*pollConfig)

type pollConfig struct {
	sleep func(context.Context, time.Duration) error
}

// WithSleep replaces the wait between polls.
//
// The seam exists for tests. RFC 8628 floors the interval at five seconds and
// expresses it in whole seconds, so a test that waited for real would take a
// minute to prove the backoff grows -- and would prove it by wall clock, which
// is the least reliable assertion available. With this, the test inspects the
// durations Poll actually asked for.
func WithSleep(fn func(context.Context, time.Duration) error) PollOption {
	return func(c *pollConfig) { c.sleep = fn }
}

func realSleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// Poll exchanges the device code for a token once the user has approved it.
//
// It blocks until the user finishes, refuses, or the code expires, and honours
// ctx throughout -- so Ctrl-C returns promptly rather than at the end of the
// current sleep.
func Poll(ctx context.Context, client *http.Client, meta *Metadata, auth *Authorization, clientID string, opts ...PollOption) (*Token, error) {
	cfg := pollConfig{sleep: realSleep}
	for _, opt := range opts {
		opt(&cfg)
	}
	interval := auth.PollInterval()
	deadline := time.Now().Add(auth.Expiry())

	form := url.Values{}
	form.Set("grant_type", GrantType)
	form.Set("device_code", auth.DeviceCode)
	form.Set("client_id", clientID)

	for {
		// Wait first. The user cannot possibly have approved in the
		// microsecond since the code was issued, and an immediate poll only
		// earns a slow_down from a server that counts them.
		if err := cfg.sleep(ctx, interval); err != nil {
			return nil, err
		}

		var token Token
		err := postForm(ctx, client, meta.TokenEndpoint, form, "exchanging the device code", &token)
		if err == nil {
			if token.AccessToken == "" {
				return nil, fmt.Errorf("exchanging the device code: the response carried no access_token")
			}
			return &token, nil
		}

		oauthErr, ok := err.(*Error)
		if !ok {
			return nil, err
		}
		switch oauthErr.Code {
		case ErrAuthorizationPending:
		case ErrSlowDown:
			interval += SlowDownIncrement
		case ErrExpiredToken:
			return nil, fmt.Errorf("the sign-in code expired before it was approved; run the command again")
		case ErrAccessDenied:
			return nil, fmt.Errorf("the sign-in request was refused")
		default:
			return nil, err
		}

		if time.Now().After(deadline) {
			return nil, fmt.Errorf("the sign-in code expired before it was approved; run the command again")
		}
	}
}

// Refresh exchanges a refresh token for a new access token.
//
// A refresh token is only ever issued when the profile asked for
// offline_access and the server honours it, so a caller must treat an error
// here as "sign in again" rather than as a failure.
func Refresh(ctx context.Context, client *http.Client, meta *Metadata, refreshToken, clientID string) (*Token, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", clientID)

	var token Token
	if err := postForm(ctx, client, meta.TokenEndpoint, form, "refreshing the token", &token); err != nil {
		return nil, err
	}
	if token.AccessToken == "" {
		return nil, fmt.Errorf("refreshing the token: the response carried no access_token")
	}
	// A server may rotate the refresh token or may not return one at all; the
	// caller keeps the old one in the second case, so say so by leaving the
	// field empty rather than inventing a value here.
	return &token, nil
}

// postForm sends a form-encoded request and decodes the JSON response,
// turning any error status into an *Error carrying the server's own words.
func postForm(ctx context.Context, client *http.Client, endpoint string, form url.Values, operation string, out any) error {
	if endpoint == "" {
		return fmt.Errorf("%s: no endpoint", operation)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	defer resp.Body.Close()

	// Bounded: an endpoint answering with something other than a token
	// response should not be able to exhaust memory here.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("%s: reading the response: %w", operation, err)
	}
	if resp.StatusCode >= 400 {
		return oauthError(operation, resp.StatusCode, body)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%s: decoding the response: %w", operation, err)
	}
	return nil
}

// oauthError builds an *Error from an error response.
//
// The RFC 6749 body is the interesting part and the status is not: every
// polling state -- pending, slow_down, denied, expired -- arrives as the same
// 400, distinguished only by the `error` field.
func oauthError(operation string, status int, body []byte) error {
	var payload struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &payload)
	return &Error{
		Operation:   operation,
		Status:      status,
		Code:        payload.Error,
		Description: payload.Description,
		Body:        string(body),
	}
}
