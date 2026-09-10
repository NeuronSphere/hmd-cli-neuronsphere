// Package keyring resolves private index credentials from the OS keychain.
//
// This is a port of hmd-cli-tools' registry_tools.resolve_index_password and
// scrub_secrets. NERD004 SPEC009 originally specified calling that helper
// rather than reimplementing it, on the grounds that a second copy of a probe
// order is the copy that goes stale. That was reversed deliberately: calling it
// means a Python interpreter and an installed hmd-cli-tools on the host, and
// nsctl's whole premise is a single binary whose only prerequisite is Docker.
//
// The staleness risk is real and is answered rather than dismissed:
// TestCandidateOrderMatchesThePython pins the candidate order and the source it
// came from, so a change there fails here rather than being discovered as a
// credential nsctl cannot see and uv can.
//
// The failure this guards against is specific. The Python probes four service
// names; a port that checked three would report a missing credential the user
// can see with their own eyes in Keychain Access.
package keyring

import (
	"context"
	"errors"
	"net/url"
	"os/exec"
	"regexp"
	"strings"
)

// urlCredential redacts the password in a scheme://user:pass@host URL. The
// expression is registry_tools._URL_CRED_RE, character for character.
var urlCredential = regexp.MustCompile(`(://[^/@\s:]+:)[^/@\s]+(@)`)

// Scrub redacts embedded URL passwords from arbitrary text before it is
// surfaced. Every error path that can carry an index URL goes through it.
func Scrub(text string) string {
	if text == "" {
		return text
	}
	return urlCredential.ReplaceAllString(text, "$1***$2")
}

// SafeURL returns a URL with any user:pass@ userinfo stripped, for logging.
//
// The port of registry_tools._safe_url, including its shape: scheme, host with
// port, and path -- query, params and fragment dropped -- with the bare host
// and then a placeholder as fallbacks.
func SafeURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "<url>"
	}
	host := parsed.Hostname()
	if port := parsed.Port(); port != "" {
		host += ":" + port
	}
	rebuilt := (&url.URL{Scheme: parsed.Scheme, Host: host, Path: parsed.Path}).String()
	if rebuilt != "" {
		return rebuilt
	}
	if host != "" {
		return host
	}
	return "<url>"
}

// Candidates is the keychain service names an index URL is looked up under, in
// probe order.
//
// uv's convention first -- `uv auth login` and UV_KEYRING_PROVIDER=subprocess
// store under `uv:<host>` -- then the clean URL and the host forms, which is
// where a credential set directly with `keyring set` lands.
//
// Exported because the order is the contract with hmd-cli-tools, and a
// contract that is only asserted inside an unexported function is one nobody
// can test against.
func Candidates(raw string) []string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	host := parsed.Hostname()

	var candidates []string
	if host != "" {
		candidates = append(candidates, "uv:"+host)
	}
	candidates = append(candidates, SafeURL(raw))
	if parsed.Scheme != "" && host != "" {
		candidates = append(candidates, parsed.Scheme+"://"+host)
	}
	if host != "" {
		candidates = append(candidates, host)
	}
	return candidates
}

// ErrUnavailable is returned when this host has no keychain helper at all.
//
// Distinguished from "nothing stored" because they call for different advice:
// the first is a machine to configure, the second a credential to add. The
// Python conflates them into None; nsctl does not, because it is the only
// thing the user will see.
var ErrUnavailable = errors.New("no keychain helper on this host")

// ErrNotFound is returned when every candidate was probed and none answered.
var ErrNotFound = errors.New("no credential stored")

// Runner executes a keychain helper. Injected so tests need no real keychain
// and no t.Setenv, which panics under t.Parallel.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// Keyring reads the host keychain.
type Keyring struct {
	// Run defaults to exec.CommandContext output.
	Run Runner
	// Warn reports a backend that could not be asked. A locked or misconfigured
	// keychain is not a failure of the whole lookup -- the Python logs and
	// continues to the next candidate, and so does this.
	Warn func(format string, a ...any)

	// available overrides the helper-on-PATH check. Injected for tests, which
	// must exercise the probe logic rather than whether the machine running
	// them happens to have a keychain client.
	available func() bool
}

// New builds a Keyring over the real helper.
func New(warn func(format string, a ...any)) *Keyring {
	return &Keyring{Run: execRun, Warn: warn}
}

func execRun(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// Available reports whether this host has a keychain helper to ask.
func (k *Keyring) Available() bool {
	if k.available != nil {
		return k.available()
	}
	if HelperName == "" {
		return false
	}
	_, err := exec.LookPath(HelperName)
	return err == nil
}

// Resolve reads a password for one explicit keychain service.
func (k *Keyring) Resolve(ctx context.Context, service, username string) (string, error) {
	return k.ResolveAny(ctx, []string{service}, username)
}

// ResolveIndexPassword resolves an index password from the host keychain,
// probing the candidate service names in order.
//
// The port of registry_tools.resolve_index_password. An empty username yields
// ErrNotFound without probing anything, matching the Python's `not username`
// guard -- the keychain is keyed by account, and a lookup with no account is
// not a lookup.
func (k *Keyring) ResolveIndexPassword(ctx context.Context, indexURL, username string) (string, error) {
	return k.ResolveAny(ctx, Candidates(indexURL), username)
}

// ResolveAny probes each service name in order and returns the first password
// found.
//
// Per service, two legs, exactly as the Python: get_password(service,
// username), then get_credential(service, None) -- which covers a credential uv
// stored under an account name that differs from the index username. The second
// leg exists only where the platform's keyring backend implements it; see
// credentialArgs.
//
// A backend that cannot be asked is a warning and a continue, never a failure:
// a locked keychain on one candidate must not hide a credential stored under
// the next.
func (k *Keyring) ResolveAny(ctx context.Context, services []string, username string) (string, error) {
	if !k.Available() {
		return "", ErrUnavailable
	}
	if username == "" {
		return "", ErrNotFound
	}
	for _, service := range services {
		if service == "" {
			continue
		}
		for _, args := range [][]string{
			passwordArgs(service, username),
			credentialArgs(service),
		} {
			if args == nil {
				continue
			}
			out, err := k.Run(ctx, HelperName, args...)
			if err != nil {
				// Not found is the common case and is not worth a warning; a
				// helper that failed some other way is. Neither stops the
				// probe.
				if !isNotFound(err) {
					k.warn("keyring lookup failed for service %q: %v", service, err)
				}
				continue
			}
			if password := trimHelperOutput(string(out)); password != "" {
				return password, nil
			}
		}
	}
	return "", ErrNotFound
}

// trimHelperOutput removes the single trailing newline a helper adds, and
// nothing else. A stored password may legitimately end in spaces.
func trimHelperOutput(out string) string {
	return strings.TrimSuffix(strings.TrimSuffix(out, "\n"), "\r")
}

func (k *Keyring) warn(format string, a ...any) {
	if k.Warn != nil {
		k.Warn(format, a...)
	}
}

// isNotFound reports the helper's "no such item" exit, which every backend
// signals with a non-zero status and no output.
func isNotFound(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr)
}
