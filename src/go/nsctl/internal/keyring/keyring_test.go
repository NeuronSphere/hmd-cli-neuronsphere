package keyring

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

// The probe order is the contract with hmd-cli-tools. It is pinned here, with
// the source it came from, so a change there fails here rather than being
// discovered as a credential nsctl cannot see and uv can.
//
// registry_tools.resolve_index_password, hmd-cli-tools:
//
//	candidates = []
//	if host: candidates.append(f"uv:{host}")
//	candidates.append(_safe_url(url))
//	if parsed.scheme and host: candidates.append(f"{parsed.scheme}://{host}")
//	if host: candidates.append(host)
func TestCandidateOrderMatchesThePython(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, url string
		want      []string
	}{
		{
			name: "a full index URL",
			url:  "https://hmdlabs.jfrog.io/artifactory/api/pypi/hmd_pypi/simple",
			want: []string{
				"uv:hmdlabs.jfrog.io",
				"https://hmdlabs.jfrog.io/artifactory/api/pypi/hmd_pypi/simple",
				"https://hmdlabs.jfrog.io",
				"hmdlabs.jfrog.io",
			},
		},
		{
			// The clean URL keeps the port; the scheme://host form does not.
			// That asymmetry is _safe_url's, and it is load bearing: both
			// spellings are probed because either may be what was stored.
			name: "a host with a port",
			url:  "http://registry.local:8081/simple",
			want: []string{
				"uv:registry.local",
				"http://registry.local:8081/simple",
				"http://registry.local",
				"registry.local",
			},
		},
		{
			// Userinfo is stripped before the URL is used as a service name,
			// or the service string would carry the very secret being looked up.
			name: "an embedded credential",
			url:  "https://user:tok@hmdlabs.jfrog.io/simple",
			want: []string{
				"uv:hmdlabs.jfrog.io",
				"https://hmdlabs.jfrog.io/simple",
				"https://hmdlabs.jfrog.io",
				"hmdlabs.jfrog.io",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := Candidates(tc.url); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Candidates:\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestCandidatesForSomethingWithNoHost(t *testing.T) {
	t.Parallel()

	// _safe_url falls back to the path, then the host, then a placeholder.
	// Whatever it produces, no candidate may be empty: an empty service name
	// would probe the whole keychain.
	for _, raw := range []string{"", "not a url", "file:///tmp/x"} {
		for _, c := range Candidates(raw) {
			if strings.TrimSpace(c) == "" {
				t.Errorf("Candidates(%q) produced an empty service name: %q", raw, Candidates(raw))
			}
		}
	}
}

func TestSafeURLStripsUserinfo(t *testing.T) {
	t.Parallel()

	for raw, want := range map[string]string{
		"https://user:tok@h.example/p":   "https://h.example/p",
		"https://user:tok@h.example:8/p": "https://h.example:8/p",
		"https://h.example":              "https://h.example",
	} {
		if got := SafeURL(raw); got != want {
			t.Errorf("SafeURL(%q) = %q, want %q", raw, got, want)
		}
	}
}

// Credentials must never reach logs or exception messages, and this is the
// function every error path goes through.
func TestScrubRedactsEmbeddedPasswords(t *testing.T) {
	t.Parallel()

	got := Scrub("failed: https://user:s3cr3t@h.example/simple and http://a:b@x.example/")
	if strings.Contains(got, "s3cr3t") || strings.Contains(got, "://a:b@") {
		t.Fatalf("Scrub left a secret behind: %q", got)
	}
	if !strings.Contains(got, "https://user:***@h.example/simple") {
		t.Errorf("Scrub = %q", got)
	}
	if Scrub("") != "" {
		t.Error("Scrub rewrote the empty string")
	}
}

// -- resolution ------------------------------------------------------------

// fakeHelper answers a fixed set of argv lines and records what it was asked.
type fakeHelper struct {
	answers map[string]string
	asked   []string
	fail    map[string]error
}

func (f *fakeHelper) run(_ context.Context, name string, args ...string) ([]byte, error) {
	key := name + " " + strings.Join(args, " ")
	f.asked = append(f.asked, key)
	if err, ok := f.fail[key]; ok {
		return nil, err
	}
	if answer, ok := f.answers[key]; ok {
		return []byte(answer), nil
	}
	return nil, &exec.ExitError{}
}

// testKeyring forces Available true, so the probe logic is tested rather than
// whether this machine happens to have a helper binary.
func testKeyring(f *fakeHelper) *Keyring {
	k := &Keyring{Run: f.run, Warn: func(string, ...any) {}}
	k.available = func() bool { return true }
	return k
}

func passwordKey(service, username string) string {
	return HelperName + " " + strings.Join(passwordArgs(service, username), " ")
}

func TestResolveTriesCandidatesInOrderAndStops(t *testing.T) {
	t.Parallel()

	// Stored under the third candidate, so the first two must be probed and
	// the fourth must not.
	f := &fakeHelper{answers: map[string]string{
		passwordKey("https://h.example", "me"): "s3cr3t\n",
	}}
	got, err := testKeyring(f).ResolveIndexPassword(context.Background(), "https://h.example/simple", "me")
	if err != nil {
		t.Fatalf("ResolveIndexPassword: %v", err)
	}
	if got != "s3cr3t" {
		t.Errorf("password = %q; the helper's trailing newline must be trimmed", got)
	}
	if !strings.Contains(strings.Join(f.asked, "\n"), "uv:h.example") {
		t.Errorf("uv's convention was not probed first: %v", f.asked)
	}
	for _, asked := range f.asked {
		if strings.Contains(asked, " h.example ") || strings.HasSuffix(asked, " h.example") {
			t.Errorf("probing continued past a hit: %v", f.asked)
		}
	}
}

// A locked or misconfigured backend on one candidate must not hide a
// credential stored under the next.
func TestResolveContinuesPastABackendError(t *testing.T) {
	t.Parallel()

	f := &fakeHelper{
		fail:    map[string]error{passwordKey("uv:h.example", "me"): errors.New("keychain locked")},
		answers: map[string]string{passwordKey("https://h.example", "me"): "s3cr3t"},
	}
	var warned int
	k := testKeyring(f)
	k.Warn = func(string, ...any) { warned++ }

	got, err := k.ResolveIndexPassword(context.Background(), "https://h.example/simple", "me")
	if err != nil || got != "s3cr3t" {
		t.Fatalf("got %q, %v", got, err)
	}
	if warned == 0 {
		t.Error("a backend that could not be asked was not reported")
	}
}

// The Python returns None outright when username is falsy. A lookup with no
// account is not a lookup, and probing anyway would search the whole keychain.
func TestResolveWithoutAUsernameProbesNothing(t *testing.T) {
	t.Parallel()

	f := &fakeHelper{}
	_, err := testKeyring(f).ResolveIndexPassword(context.Background(), "https://h.example/simple", "")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
	if len(f.asked) != 0 {
		t.Errorf("the keychain was probed with no account: %v", f.asked)
	}
}

func TestResolveReportsNotFoundDistinctlyFromUnavailable(t *testing.T) {
	t.Parallel()

	if _, err := testKeyring(&fakeHelper{}).Resolve(context.Background(), "uv:h", "me"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}

	// A host with no helper is a machine to configure, not a credential to
	// add, and the two need different advice.
	k := &Keyring{Run: (&fakeHelper{}).run}
	k.available = func() bool { return false }
	if _, err := k.Resolve(context.Background(), "uv:h", "me"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
}

func TestResolveSkipsEmptyServiceNames(t *testing.T) {
	t.Parallel()

	f := &fakeHelper{}
	_, _ = testKeyring(f).ResolveAny(context.Background(), []string{"", "uv:h"}, "me")
	for _, asked := range f.asked {
		if strings.Contains(asked, "-s  ") || strings.HasSuffix(asked, "service  username me") {
			t.Errorf("an empty service name was probed: %v", f.asked)
		}
	}
}

// An explicit keyring_service is a single candidate: the manifest said which
// one, so there is nothing to probe for.
func TestResolveUsesAnExplicitServiceAlone(t *testing.T) {
	t.Parallel()

	f := &fakeHelper{answers: map[string]string{passwordKey("uv:hmdlabs.jfrog.io", "me"): "tok"}}
	got, err := testKeyring(f).Resolve(context.Background(), "uv:hmdlabs.jfrog.io", "me")
	if err != nil || got != "tok" {
		t.Fatalf("got %q, %v", got, err)
	}
	if len(f.asked) != 1 {
		t.Errorf("asked %v, want exactly one lookup", f.asked)
	}
}
