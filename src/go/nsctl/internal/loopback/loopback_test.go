package loopback

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
)

func resolves(ips ...string) Resolver {
	return func(string) ([]net.IP, error) {
		out := make([]net.IP, 0, len(ips))
		for _, s := range ips {
			out = append(out, net.ParseIP(s))
		}
		return out, nil
	}
}

func resolvesNothing(string) ([]net.IP, error) {
	return nil, errors.New("no such host")
}

// A machine without the /etc/hosts entry is the case this exists for.
func TestAnUnresolvableNameIsRedirected(t *testing.T) {
	t.Parallel()

	got := Redirects(Names, resolvesNothing)
	for _, name := range Names {
		if !got[name] {
			t.Errorf("%s does not resolve but was not redirected", name)
		}
	}
}

// Inside a container the name is a Docker alias pointing at a sibling, and must
// not be rewritten to the container's own loopback. A routable answer is how
// that case is told apart from the host's.
func TestAResolvableNameIsLeftAlone(t *testing.T) {
	t.Parallel()

	if got := Redirects(Names, resolves("172.18.0.5")); len(got) != 0 {
		t.Errorf("a name resolving to a Docker network address was redirected: %v", got)
	}
}

// The signature over a presigned URL covers the Host header, so the override
// has to change where the connection goes without changing what is sent.
func TestTheHostHeaderSurvivesTheRedirect(t *testing.T) {
	t.Parallel()

	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Host
	}))
	defer srv.Close()

	_, port, err := net.SplitHostPort(mustURL(t, srv.URL).Host)
	if err != nil {
		t.Fatalf("SplitHostPort: %v", err)
	}

	client := &http.Client{Transport: Transport(http.DefaultTransport, map[string]bool{"neuronsphere": true}, nil)}
	resp, err := client.Get("http://neuronsphere:" + port + "/restapis/abc/local/_user_request_/thing")
	if err != nil {
		t.Fatalf("the redirected request did not reach the server: %v", err)
	}
	defer resp.Body.Close()

	if want := "neuronsphere:" + port; seen != want {
		t.Errorf("server saw Host %q, want %q -- rewriting it would void the SigV4 signature", seen, want)
	}
}

// Everything else dials normally; this must not become a general-purpose
// interceptor.
func TestAnUnrelatedHostIsNotRedirected(t *testing.T) {
	t.Parallel()

	hosts := map[string]bool{"neuronsphere": true}
	for _, tt := range []struct{ addr, want string }{
		{"example.com:443", "example.com:443"},
		{"neuronsphere:4566", "127.0.0.1:4566"},
		{"neuronsphere-workload:4566", "neuronsphere-workload:4566"}, // not in the set
		{"neuronsphere", "neuronsphere"},                             // no port: left alone
	} {
		if got := redirect(tt.addr, hosts, nil); got != tt.want {
			t.Errorf("redirect(%q) = %q, want %q", tt.addr, got, tt.want)
		}
	}
}

// 4566 is LocalStack's port too, so a machine already running one publishes
// Floci elsewhere. The URL Floci stamped still names 4566; the dial has to go
// where it actually landed.
func TestAMovedFlociPortIsDialledWhereItLanded(t *testing.T) {
	t.Parallel()

	hosts := map[string]bool{"neuronsphere": true}
	ports := map[int]int{4566: 14566}
	if got := redirect("neuronsphere:4566", hosts, ports); got != "127.0.0.1:14566" {
		t.Errorf("redirect = %q, want 127.0.0.1:14566", got)
	}
	// A different port on the same host is not the emulator and is not remapped.
	if got := redirect("neuronsphere:8080", hosts, ports); got != "127.0.0.1:8080" {
		t.Errorf("redirect = %q, want the port left alone", got)
	}
}

// A name that answers with a routable address is a Docker alias pointing at a
// sibling container. Rewriting it would send a container to its own loopback.
func TestAnInNetworkAliasIsNeverRedirected(t *testing.T) {
	t.Parallel()

	inNetwork := func(string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("172.18.0.5")}, nil
	}
	if got := Redirects(Names, inNetwork); len(got) != 0 {
		t.Errorf("an in-network alias was redirected: %v", got)
	}
}

// On the host the name may resolve to loopback -- an /etc/hosts entry -- and it
// still needs the redirect, because the published port may have moved.
func TestALoopbackAnswerIsStillRedirected(t *testing.T) {
	t.Parallel()

	if got := Redirects(Names, resolves("127.0.0.1")); len(got) != len(Names) {
		t.Errorf("a loopback answer was not redirected: %v", got)
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parsing %q: %v", raw, err)
	}
	return u
}

// Install is called more than once in a process: from PersistentPreRun with the
// registry as it stands, and again from a start that has just chosen a different
// Floci port. The last call wins, and none of them writes http.DefaultTransport
// -- which is what keeps net/http's own read of that global from racing.
func TestInstallChangesTheRedirectWithoutTouchingTheGlobal(t *testing.T) {
	before := http.DefaultTransport
	t.Cleanup(func() { Install(func(string) ([]net.IP, error) { return []net.IP{net.IPv4(172, 18, 0, 5)}, nil }, 0) })

	all := func(string) ([]net.IP, error) { return nil, errors.New("no such host") }

	if got := Install(all, 14566); len(got) != 2 {
		t.Fatalf("Install redirected %v, want both names", got)
	}
	if http.DefaultTransport != before {
		t.Error("Install wrote http.DefaultTransport; it must only swap the configuration")
	}

	if got := Install(all, 24566); len(got) != 2 {
		t.Fatalf("the second Install redirected %v", got)
	}
	if http.DefaultTransport != before {
		t.Error("the second Install wrote http.DefaultTransport")
	}

	// A machine that resolves the names to a routable address redirects nothing,
	// and that is a state Install has to be able to return to.
	routable := func(string) ([]net.IP, error) { return []net.IP{net.IPv4(172, 18, 0, 5)}, nil }
	if got := Install(routable, 0); got != nil {
		t.Errorf("Install redirected %v for names that resolve off-loopback", got)
	}
}

// Two commands in one process -- which is what the test binary is -- must not
// race on the global they both install into. The detector caught this only once
// there were enough parallel tests to collide, which is the usual way.
func TestConcurrentInstallsDoNotRace(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })

	all := func(string) ([]net.IP, error) { return nil, errors.New("no such host") }

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			Install(all, 14566+i)
		}(i)
	}
	wg.Wait()
}
