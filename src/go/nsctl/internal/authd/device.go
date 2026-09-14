package authd

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"html/template"
	"math/big"
	"net/http"
	"strings"
	"time"
)

// DeviceGrantType is RFC 8628's grant identifier.
const DeviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"

// deviceCodeLifetime is how long a user has to approve a device code.
//
// Ten minutes, RFC 8628's own example. Longer than an authorization code's
// five because a human has to read a code off one screen and type it into
// another, possibly on a different machine.
const deviceCodeLifetime = 10 * time.Minute

// devicePollInterval is the cadence the client is told to poll at.
const devicePollInterval = 5 * time.Second

// pendingDevice is an issued device code awaiting approval.
type pendingDevice struct {
	userCode string
	expires  time.Time
	// approved is false until someone signs in at the verification URI.
	// request is meaningless until then.
	approved bool
	request  Request
}

// userCodeAlphabet excludes glyphs that are ambiguous when read off a screen
// and typed into a browser: no O/0, no I/1, no S/5, no Z/2. RFC 8628 section
// 6.1 asks for exactly this consideration, and the mock is where a user code
// actually gets typed by a person during a test.
const userCodeAlphabet = "BCDFGHJKLMNPQRTVWXY346789"

// deviceAuthorize issues a device code and the user code that approves it.
func (s *Server) deviceAuthorize(server string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			oauthError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		req := Request{
			Server:   server,
			ClientID: r.PostFormValue("client_id"),
			Scopes:   strings.Fields(r.PostFormValue("scope")),
			Audience: r.PostFormValue("audience"),
		}
		deviceCode, userCode := s.storeDevice(req)

		issuer := IssuerFor(s.Issuer, server)
		verification := issuer + "/v1/device"
		writeJSON(w, http.StatusOK, map[string]any{
			"device_code": deviceCode,
			"user_code":   userCode,
			// Both forms. The complete one is what a browser is pointed at so
			// the user types nothing; the plain one is what a user reads off
			// the terminal when the browser is on another machine, which is
			// the case the whole grant exists for.
			"verification_uri":          verification,
			"verification_uri_complete": verification + "?user_code=" + userCode,
			"expires_in":                int(deviceCodeLifetime.Seconds()),
			"interval":                  int(devicePollInterval.Seconds()),
		})
	}
}

// deviceVerify renders the sign-in form for a user code.
//
// The same claims-choosing form the redirect flow uses. Choosing the claims is
// the point of this whole server -- the groups decide every Rego decision and
// both applications' role mapping -- so a device path that logged in a fixed
// user would be testing something else.
func (s *Server) deviceVerify(server string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = deviceForm.Execute(w, map[string]any{
			"Action":   IssuerFor(s.Issuer, server) + "/v1/device",
			"Server":   server,
			"UserCode": r.URL.Query().Get("user_code"),
		})
	}
}

// deviceApprove binds the typed claims to the pending device code.
func (s *Server) deviceApprove(server string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			oauthError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		userCode := normalizeUserCode(r.PostFormValue("user_code"))
		subject := r.PostFormValue("username")
		if subject == "" {
			subject = "local-user"
		}
		approved := s.approveDevice(userCode, func(req *Request) {
			req.Server = server
			req.Subject = subject
			req.Groups = splitLines(r.PostFormValue("groups"))
			if req.ClientID == "" {
				req.ClientID = subject
			}
		})

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if !approved {
			w.WriteHeader(http.StatusBadRequest)
			_ = deviceResult.Execute(w, map[string]any{
				"OK": false,
				"Message": "That code is not one this server issued, or it has expired. " +
					"Run the command again to get a new one.",
			})
			return
		}
		_ = deviceResult.Execute(w, map[string]any{
			"OK":      true,
			"Message": "You are signed in. You can close this window and return to the terminal.",
		})
	}
}

// deviceToken is the device_code branch of the token endpoint.
//
// Every state here is an HTTP 400 distinguished only by the `error` field,
// which is what RFC 8628 section 3.5 specifies and what the client's polling
// loop switches on.
func (s *Server) deviceToken(w http.ResponseWriter, server string, r *http.Request) {
	code := r.PostFormValue("device_code")
	pending, known := s.lookupDevice(code)
	if !known {
		oauthError(w, http.StatusBadRequest, "expired_token",
			"that device code is unknown or has expired")
		return
	}
	if !pending.approved {
		oauthError(w, http.StatusBadRequest, "authorization_pending",
			"the user has not finished signing in yet")
		return
	}
	// Single use: consumed whether or not the issue below then succeeds, so a
	// replayed device code cannot mint a second token.
	s.consumeDevice(code)
	s.issue(w, server, pending.request, "")
}

// storeDevice issues a device code and its user code.
func (s *Server) storeDevice(req Request) (deviceCode, userCode string) {
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	deviceCode = base64.RawURLEncoding.EncodeToString(raw)
	userCode = generateUserCode()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.devices == nil {
		s.devices = map[string]pendingDevice{}
	}
	// Swept on write, like storeCode: the map only grows when someone signs
	// in, so that is the right moment to prune.
	now := s.now()
	for existing, device := range s.devices {
		if now.After(device.expires) {
			delete(s.devices, existing)
		}
	}
	s.devices[deviceCode] = pendingDevice{
		userCode: userCode,
		expires:  now.Add(deviceCodeLifetime),
		request:  req,
	}
	return deviceCode, userCode
}

// approveDevice marks the device code behind a user code approved, letting the
// caller fill in the claims. It reports whether such a code was pending.
func (s *Server) approveDevice(userCode string, fill func(*Request)) bool {
	if userCode == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for code, device := range s.devices {
		if device.userCode != userCode || now.After(device.expires) {
			continue
		}
		fill(&device.request)
		device.approved = true
		s.devices[code] = device
		return true
	}
	return false
}

// lookupDevice returns a pending device code, treating an expired one as
// unknown so the client is told `expired_token` rather than left polling.
func (s *Server) lookupDevice(code string) (pendingDevice, bool) {
	if code == "" {
		return pendingDevice{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	device, ok := s.devices[code]
	if !ok || s.now().After(device.expires) {
		return pendingDevice{}, false
	}
	return device, true
}

func (s *Server) consumeDevice(code string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.devices, code)
}

// generateUserCode builds a grouped code like BCDF-GHJK.
//
// crypto/rand rather than math/rand: this code is the only thing standing
// between a pending sign-in and whoever guesses it, and it is short by design.
func generateUserCode() string {
	const groups, size = 2, 4
	out := make([]byte, 0, groups*size+groups-1)
	limit := big.NewInt(int64(len(userCodeAlphabet)))
	for g := 0; g < groups; g++ {
		if g > 0 {
			out = append(out, '-')
		}
		for i := 0; i < size; i++ {
			n, err := rand.Int(rand.Reader, limit)
			if err != nil {
				// rand.Int only fails if the entropy source does, at which
				// point nothing this server issues is trustworthy anyway.
				panic(fmt.Sprintf("authd: no entropy for a user code: %v", err))
			}
			out = append(out, userCodeAlphabet[n.Int64()])
		}
	}
	return string(out)
}

// normalizeUserCode accepts what a person types: any case, and with or
// without the grouping hyphen.
func normalizeUserCode(value string) string {
	var out strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(value)) {
		if r == '-' || r == ' ' {
			continue
		}
		out.WriteRune(r)
	}
	code := out.String()
	if len(code) == 8 {
		return code[:4] + "-" + code[4:]
	}
	return code
}

var deviceForm = template.Must(template.New("device").Parse(`<!doctype html>
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
<p>This is the local mock identity provider. Enter the code your terminal is
showing, then whatever claims you want the token to carry &mdash; the groups
decide your role in the application and every policy decision about you.</p>
<form method="post" action="{{.Action}}">
 <label for="user_code">Code from the terminal</label>
 <input id="user_code" name="user_code" value="{{.UserCode}}" spellcheck="false" autofocus>
 <label for="username">Username</label>
 <input id="username" name="username" value="local-user">
 <label for="groups">Groups, one per line</label>
 <textarea id="groups" name="groups" spellcheck="false"></textarea>
 <button type="submit">Sign in</button>
</form>
`))

var deviceResult = template.Must(template.New("deviceresult").Parse(`<!doctype html>
<title>NeuronSphere local sign-in</title>
<style>
 body{font:14px system-ui,sans-serif;max-width:32rem;margin:4rem auto;padding:0 1rem}
</style>
<h1>{{if .OK}}Signed in{{else}}That did not work{{end}}</h1>
<p>{{.Message}}</p>
`))
