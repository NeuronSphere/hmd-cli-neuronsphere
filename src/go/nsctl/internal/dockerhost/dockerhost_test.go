package dockerhost

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
)

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func fixedInspector(out string, err error) Inspector {
	return func(context.Context) (string, error) { return out, err }
}

// DOCKER_HOST wins, and the CLI reporting it under the synthetic "default"
// context must not be mistaken for a context the user chose.
func TestResolvePrefersDOCKERHOST(t *testing.T) {
	r := &Resolver{
		Inspect: fixedInspector("default\tunix:///tmp/fake.sock\tfalse", nil),
		Lookup:  envOf(map[string]string{"DOCKER_HOST": "unix:///tmp/fake.sock"}),
	}
	ep, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ep.Host != "unix:///tmp/fake.sock" {
		t.Errorf("host = %q", ep.Host)
	}
	if ep.Source != SourceEnvHost {
		t.Errorf("source = %q, want %q", ep.Source, SourceEnvHost)
	}
	if ep.Context != "" {
		t.Errorf("context = %q, want empty: DOCKER_HOST chose this, not a context", ep.Context)
	}
}

// The case the whole document exists for: a context naming a socket that is
// not /var/run/docker.sock.
func TestResolveUsesContextEndpoint(t *testing.T) {
	const sock = "unix:///Users/x/.colima/default/docker.sock"
	r := &Resolver{
		Inspect: fixedInspector("colima\t"+sock+"\tfalse", nil),
		Lookup:  envOf(nil),
	}
	ep, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ep.Host != sock {
		t.Errorf("host = %q, want %q", ep.Host, sock)
	}
	if ep.Source != SourceContext || ep.Context != "colima" {
		t.Errorf("source/context = %q/%q", ep.Source, ep.Context)
	}
	if got := ep.Describe(); !strings.Contains(got, "colima") || !strings.Contains(got, sock) {
		t.Errorf("Describe() = %q, must name both the host and the context", got)
	}
}

// Nothing named an endpoint: fall back to exactly what the Engine API client
// would have used anyway, so this can only improve on the old behaviour.
func TestResolveFallsBackToDefaultSocket(t *testing.T) {
	r := &Resolver{
		Inspect: fixedInspector("", errors.New("docker context inspect: boom")),
		Lookup:  envOf(nil),
	}
	ep, err := r.Resolve(context.Background())
	if err == nil {
		t.Error("want the CLI failure carried back for diagnosis")
	}
	if ep.Host != DefaultHost() {
		t.Errorf("host = %q, want %q", ep.Host, DefaultHost())
	}
	if ep.Source != SourceDefaultSocket {
		t.Errorf("source = %q", ep.Source)
	}
}

// The CLI failed but the environment is unambiguous.
func TestResolveFallsBackToEnvWhenCLIFails(t *testing.T) {
	r := &Resolver{
		Inspect: fixedInspector("", errors.New("no docker")),
		Lookup:  envOf(map[string]string{"DOCKER_HOST": "tcp://10.0.0.2:2376"}),
	}
	ep, _ := r.Resolve(context.Background())
	if ep.Host != "tcp://10.0.0.2:2376" || ep.Source != SourceEnvHost {
		t.Errorf("got %q/%q", ep.Host, ep.Source)
	}
}

// Empty or malformed CLI output is "unresolved", not an empty host.
func TestResolveIgnoresUnusableOutput(t *testing.T) {
	for _, out := range []string{"", "colima", "colima\t\tfalse"} {
		r := &Resolver{Inspect: fixedInspector(out, nil), Lookup: envOf(nil)}
		ep, _ := r.Resolve(context.Background())
		if ep.Host != DefaultHost() {
			t.Errorf("out %q: host = %q, want the default", out, ep.Host)
		}
	}
}

func TestResolveIsMemoized(t *testing.T) {
	calls := 0
	r := &Resolver{
		Inspect: func(context.Context) (string, error) {
			calls++
			return "colima\tunix:///x.sock\tfalse", nil
		},
		Lookup: envOf(nil),
	}
	r.Resolve(context.Background())
	r.Resolve(context.Background())
	if calls != 1 {
		t.Errorf("inspector called %d times, want 1", calls)
	}
}

func TestClientOptsRefusesSSH(t *testing.T) {
	ep := Endpoint{Host: "ssh://build@host", Context: "remote", Source: SourceContext}
	_, err := ep.ClientOpts(envOf(nil))
	if !errors.Is(err, ErrUnsupportedScheme) {
		t.Fatalf("err = %v, want ErrUnsupportedScheme", err)
	}
	for _, want := range []string{"ssh://build@host", "docker context use"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message must contain %q; got %q", want, err.Error())
		}
	}
}

func TestClientOptsUnixAndTCP(t *testing.T) {
	unix := Endpoint{Host: "unix:///var/run/docker.sock", Source: SourceDefaultSocket}
	opts, err := unix.ClientOpts(envOf(nil))
	if err != nil {
		t.Fatalf("unix: %v", err)
	}
	if len(opts) != 2 {
		t.Errorf("unix opts = %d, want 2 (host, negotiation)", len(opts))
	}

	tcp := Endpoint{Host: "tcp://10.0.0.2:2376", Source: SourceEnvHost}
	plain, err := tcp.ClientOpts(envOf(nil))
	if err != nil {
		t.Fatalf("tcp: %v", err)
	}
	if len(plain) != 2 {
		t.Errorf("tcp without TLS env = %d opts, want 2", len(plain))
	}
	withTLS, err := tcp.ClientOpts(envOf(map[string]string{"DOCKER_TLS_VERIFY": "1"}))
	if err != nil {
		t.Fatalf("tcp+tls: %v", err)
	}
	if len(withTLS) != 3 {
		t.Errorf("tcp with TLS env = %d opts, want 3", len(withTLS))
	}
}

func TestClientOptsRefusesUnknownScheme(t *testing.T) {
	ep := Endpoint{Host: "carrier-pigeon://nowhere", Source: SourceEnvHost}
	if _, err := ep.ClientOpts(envOf(nil)); !errors.Is(err, ErrUnsupportedScheme) {
		t.Fatalf("err = %v", err)
	}
}

// Describe's strings are quoted verbatim in the preflight refusal, so they are
// the product here.
func TestDescribeNamesHostAndSource(t *testing.T) {
	cases := []struct {
		ep   Endpoint
		want string
	}{
		{Endpoint{Host: "unix:///var/run/docker.sock", Source: SourceDefaultSocket},
			"unix:///var/run/docker.sock (the default socket)"},
		{Endpoint{Host: "unix:///x.sock", Context: "colima", Source: SourceContext},
			`unix:///x.sock (the docker context "colima")`},
		{Endpoint{Host: "tcp://h:2376", Source: SourceEnvHost},
			"tcp://h:2376 (the DOCKER_HOST environment variable)"},
	}
	for _, c := range cases {
		if got := c.ep.Describe(); got != c.want {
			t.Errorf("Describe() = %q, want %q", got, c.want)
		}
	}
}

// An empty context name must not shift the host into the name's column.
func TestParseKeepsColumnsWhenTheNameIsEmpty(t *testing.T) {
	ep, ok := parseContextEndpoint("\tunix:///x.sock\tfalse\n")
	if !ok {
		t.Fatal("want a parse")
	}
	if ep.Host != "unix:///x.sock" {
		t.Errorf("host = %q, want the second column", ep.Host)
	}
	if ep.Context != "" {
		t.Errorf("context = %q, want empty", ep.Context)
	}
}

func TestDefaultHostPerPlatform(t *testing.T) {
	got := DefaultHost()
	want := "unix:///var/run/docker.sock"
	if runtime.GOOS == "windows" {
		want = "npipe:////./pipe/docker_engine"
	}
	if got != want {
		t.Errorf("DefaultHost() = %q, want %q", got, want)
	}
}
