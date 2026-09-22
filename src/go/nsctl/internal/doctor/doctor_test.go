package doctor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/dockerhost"
)

type okCLI struct{ err error }

func (c okCLI) Available(context.Context) error { return c.err }

func fixedResolver(ep dockerhost.Endpoint) *dockerhost.Resolver {
	// Resolver is bypassed for an endpoint that already knows its source, so
	// the test controls Source directly rather than through the CLI format.
	return &dockerhost.Resolver{
		Inspect: func(context.Context) (string, error) {
			name := ep.Context
			if ep.Source == dockerhost.SourceDefaultSocket {
				name = "default"
			}
			return name + "\t" + ep.Host + "\tfalse", nil
		},
		Lookup: func(string) string { return "" },
	}
}

func reaching(d dockerhost.Daemon, err error) func(context.Context, dockerhost.Endpoint) (dockerhost.Daemon, error) {
	return func(context.Context, dockerhost.Endpoint) (dockerhost.Daemon, error) { return d, err }
}

func healthy() dockerhost.Daemon {
	return dockerhost.Daemon{
		ServerVersion: "27.3.1", OSType: "linux", OperatingSystem: "Docker Desktop",
		NCPU: 8, MemTotal: 16 << 30,
	}
}

func find(checks []Check, name string) (Check, bool) {
	for _, c := range checks {
		if c.Name == name {
			return c, true
		}
	}
	return Check{}, false
}

// The exact shape of the original bug: the CLI answers, so the old preflight
// passed, and the engine the work needs is unreachable.
func TestGateFailsWhenTheEngineIsUnreachableThoughTheCLIAnswers(t *testing.T) {
	ep := dockerhost.Endpoint{Host: "unix:///var/run/docker.sock", Source: dockerhost.SourceDefaultSocket}
	_, checks, err := Gate(context.Background(), Options{
		Docker:   okCLI{},
		Resolver: fixedResolver(ep),
		Connect:  reaching(dockerhost.Daemon{}, errors.New("Cannot connect to the Docker daemon")),
		GOOS:     "darwin",
	})
	if err == nil {
		t.Fatal("want a refusal: the CLI answering is not the engine answering")
	}
	if cli, _ := find(checks, "docker CLI"); cli.Status != StatusOK {
		t.Error("the CLI check must still pass; that is the whole point")
	}
	c, ok := find(checks, "engine reachable")
	if !ok || c.Status != StatusFail {
		t.Fatalf("want a failed reachability check, got %+v", c)
	}
	for _, want := range []string{"unix:///var/run/docker.sock", "legacy default socket", "docker context"} {
		if !strings.Contains(c.Detail+c.Remedy, want) {
			t.Errorf("the refusal must mention %q; got %q / %q", want, c.Detail, c.Remedy)
		}
	}
}

// A missing docker binary stops before anything tries to resolve an endpoint.
func TestGateFailsFastWithoutTheCLI(t *testing.T) {
	_, checks, err := Gate(context.Background(), Options{
		Docker: okCLI{err: errors.New("docker not found on PATH")},
	})
	if err == nil {
		t.Fatal("want a refusal")
	}
	if len(checks) != 1 || checks[0].Status != StatusFail {
		t.Fatalf("want exactly the CLI failure, got %+v", checks)
	}
}

func TestGatePassesOnAHealthyEngine(t *testing.T) {
	ep := dockerhost.Endpoint{Host: "unix:///x.sock", Context: "colima", Source: dockerhost.SourceContext}
	got, checks, err := Gate(context.Background(), Options{
		Docker: okCLI{}, Resolver: fixedResolver(ep),
		Connect: reaching(healthy(), nil), GOOS: "darwin",
		Lookup: func(k string) string { return map[string]string{"HOME": "/Users/x"}["HOME"] },
		Home:   "/Users/x/hmd",
	})
	if err != nil {
		t.Fatalf("Gate: %v (%+v)", err, checks)
	}
	if got.Host != ep.Host {
		t.Errorf("Gate returned %q, want the endpoint it proved (%q)", got.Host, ep.Host)
	}
	if Failed(checks) {
		t.Errorf("no check should fail: %+v", checks)
	}
}

// Warn, never refuse: the thresholds are a proposal, not a measurement.
func TestSmallEngineWarnsAndDoesNotRefuse(t *testing.T) {
	small := healthy()
	small.NCPU, small.MemTotal = 2, 2<<30
	_, checks, err := Gate(context.Background(), Options{
		Docker: okCLI{}, Resolver: fixedResolver(dockerhost.Endpoint{Host: "unix:///x.sock"}),
		Connect: reaching(small, nil), GOOS: "linux",
	})
	if err != nil {
		t.Fatalf("a small engine must not refuse a start: %v", err)
	}
	c, _ := find(checks, "engine capacity")
	if c.Status != StatusWarn {
		t.Fatalf("want a warning, got %v", c.Status)
	}
	for _, want := range []string{"2 CPUs", "colima start"} {
		if !strings.Contains(c.Detail+c.Remedy, want) {
			t.Errorf("want %q in the warning; got %q / %q", want, c.Detail, c.Remedy)
		}
	}
}

// A native Linux daemon shares the host filesystem, so there is nothing to say.
func TestNativeEngineHasNoMountWarning(t *testing.T) {
	native := healthy()
	native.OperatingSystem = "Ubuntu 24.04"
	_, checks, _ := Gate(context.Background(), Options{
		Docker: okCLI{}, Resolver: fixedResolver(dockerhost.Endpoint{Host: "unix:///x.sock"}),
		Connect: reaching(native, nil), GOOS: "linux",
		Lookup: func(string) string { return "/opt/elsewhere" },
		Home:   "/opt/elsewhere/hmd",
	})
	c, _ := find(checks, "bind mounts")
	if c.Status != StatusOK {
		t.Errorf("a native daemon needs no mount warning, got %v: %s", c.Status, c.Detail)
	}
}

// The path that would silently bind-mount as an empty directory.
func TestVMEngineWarnsAboutPathsOutsideHome(t *testing.T) {
	env := map[string]string{"HOME": "/Users/x", "HMD_REPO_HOME": "/opt/hmd/repos"}
	_, checks, _ := Gate(context.Background(), Options{
		Docker: okCLI{}, Resolver: fixedResolver(dockerhost.Endpoint{Host: "unix:///x.sock"}),
		Connect: reaching(healthy(), nil), GOOS: "darwin",
		Lookup: func(k string) string { return env[k] },
		Home:   "/Users/x/hmd",
	})
	c, _ := find(checks, "bind mounts")
	if c.Status != StatusWarn {
		t.Fatalf("want a warning, got %v: %s", c.Status, c.Detail)
	}
	if !strings.Contains(c.Detail, "/opt/hmd/repos") {
		t.Errorf("the warning must name the path: %q", c.Detail)
	}
	if strings.Contains(c.Detail, "/Users/x/hmd") {
		t.Errorf("a path under home is fine and must not be named: %q", c.Detail)
	}
}

// The canary: after this change the two can only disagree through a bug.
func TestDisagreementBetweenCLIAndAPIWarns(t *testing.T) {
	checks := Run(context.Background(), Options{
		Docker:   okCLI{},
		Resolver: fixedResolver(dockerhost.Endpoint{Host: "unix:///resolved.sock"}),
		Connect:  reaching(healthy(), nil), GOOS: "linux",
		CLIEndpoint: func(context.Context) (string, error) {
			return "colima\tunix:///different.sock\tfalse", nil
		},
	})
	c, ok := find(checks, "CLI and API agree")
	if !ok || c.Status != StatusWarn {
		t.Fatalf("want a warning, got %+v", c)
	}
	if !strings.Contains(c.Detail, "unix:///different.sock") {
		t.Errorf("the warning must name both endpoints: %q", c.Detail)
	}
}

func TestRunReportsHostNames(t *testing.T) {
	checks := Run(context.Background(), Options{
		Docker:   okCLI{},
		Resolver: fixedResolver(dockerhost.Endpoint{Host: "unix:///x.sock"}),
		Connect:  reaching(healthy(), nil), GOOS: "linux",
		Hosts: func() error { return errors.New("neuronsphere must resolve to a loopback address") },
	})
	c, ok := find(checks, "host names")
	if !ok || c.Status != StatusFail {
		t.Fatalf("want a failed hosts check, got %+v", c)
	}
	if !Failed(checks) {
		t.Error("Failed must report it")
	}
}

func TestReportPrintsEveryLineOfDetailAndRemedy(t *testing.T) {
	var b strings.Builder
	Report(&b, []Check{{Name: "engine", Status: StatusWarn, Detail: "first\nsecond", Remedy: "do this"}})
	for _, want := range []string{"engine", "warning", "first", "second", "do this"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("report missing %q:\n%s", want, b.String())
		}
	}
}
