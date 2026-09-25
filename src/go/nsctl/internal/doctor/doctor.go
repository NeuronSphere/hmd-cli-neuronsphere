// Package doctor answers "can nsctl work on this machine", once, for both the
// start preflight and the nsctl doctor command.
//
// One package rather than two so the gate and the diagnostic cannot drift:
// the defect that motivated NERD021 was a preflight that asked the docker CLI
// whether an engine was running and then did its work through the Engine API,
// which resolves a different endpoint. A check that is not the one the work
// depends on is worse than no check, because it reports an all-clear.
package doctor

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/dockerhost"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/hmdenv"
)

// Status is a check's outcome.
type Status int

const (
	// StatusOK means nothing to do.
	StatusOK Status = iota
	// StatusWarn means the user should know, and may ignore it. A threshold
	// nobody has measured only ever warns (NERD021 SPEC008).
	StatusWarn
	// StatusFail means a start cannot proceed.
	StatusFail
)

func (s Status) String() string {
	switch s {
	case StatusOK:
		return "ok"
	case StatusWarn:
		return "warning"
	default:
		return "failed"
	}
}

// Check is one finding, phrased so it can be printed as it stands.
type Check struct {
	Name   string
	Status Status
	Detail string
	// Remedy is the command or edit that fixes it, empty when there is
	// nothing to fix.
	Remedy string
}

// MinCPU and MinMemory are what the local platform wants: Floci, a Postgres,
// a graph and a k3s cluster. They are a proposal rather than a measurement,
// which is exactly why crossing them only warns.
const (
	MinCPU    = 4
	MinMemory = int64(8) << 30
)

// CLIChecker is the docker CLI probe, narrowed to what the gate needs.
type CLIChecker interface {
	Available(ctx context.Context) error
}

// Options carries every seam, so the whole suite runs in a unit test without a
// daemon, a docker binary or a home directory.
type Options struct {
	Lookup   hmdenv.Lookup
	Home     string
	GOOS     string
	Docker   CLIChecker
	Resolver *dockerhost.Resolver
	// Connect reaches the engine at a resolved endpoint. Injected rather than
	// built here so a test needs no socket.
	Connect func(ctx context.Context, ep dockerhost.Endpoint) (dockerhost.Daemon, error)
	// CLIEndpoint is what the docker CLI itself resolves, for the agreement
	// check. Empty skips it.
	CLIEndpoint func(ctx context.Context) (string, error)
	// Hosts verifies the /etc/hosts entries. Nil skips it.
	Hosts func() error
	// Suffix verifies that the wildcard local suffix resolves on this machine.
	// Nil skips it.
	//
	// A separate row from Hosts, because they fail independently and are fixed
	// differently: Hosts is the two bare single-label names that no
	// suffix-scoped resolver can claim and that nsctl dials itself (NERD026
	// SPEC004), while this is every user interface, the OIDC issuer and the
	// package index -- reported, never required (NERD026 SPEC003).
	Suffix func() error
	// Environments reports whether each running environment's substrate is
	// current and answering (NERD024 SPEC006). Nil skips it.
	//
	// Injected for the reason status.Reporter injects Substrate and Routed:
	// this package must not learn HMD_HOME's layout, and it must not import the
	// environment package, which imports it back through the start preflight.
	// The caller owns the knowledge; doctor owns the reporting.
	//
	// Run only from Run, never from Gate. Gate is the preflight other commands
	// call before starting anything, and a preflight that asks Floci whether a
	// start may proceed fails in exactly the case where the start is what would
	// have fixed it.
	Environments func(ctx context.Context) []Check
}

func (o Options) lookup(key string) string {
	if o.Lookup == nil {
		return ""
	}
	return o.Lookup(key)
}

// Gate is the subset a start must pass. It returns the endpoint it proved, so
// the caller builds its Engine API client from the same one rather than
// resolving a second time.
func Gate(ctx context.Context, o Options) (dockerhost.Endpoint, []Check, error) {
	var checks []Check

	if o.Docker != nil {
		if err := o.Docker.Available(ctx); err != nil {
			checks = append(checks, Check{
				Name: "docker CLI", Status: StatusFail, Detail: err.Error(),
				Remedy: "Install Docker, or start your container engine.",
			})
			return dockerhost.Endpoint{}, checks, ErrFailed
		}
		checks = append(checks, Check{Name: "docker CLI", Status: StatusOK, Detail: "found and answering"})
	}

	ep, resolveErr := o.resolver().Resolve(ctx)
	checks = append(checks, Check{Name: "engine endpoint", Status: StatusOK, Detail: ep.Describe()})

	daemon, err := o.connect(ctx, ep)
	if err != nil {
		checks = append(checks, Check{
			Name: "engine reachable", Status: StatusFail,
			Detail: unreachable(ep, err, resolveErr),
			Remedy: "docker context use <name>    (`docker context ls` lists them)",
		})
		return ep, checks, ErrFailed
	}
	checks = append(checks, Check{
		Name:   "engine reachable",
		Status: StatusOK,
		Detail: fmt.Sprintf("%s %s (%s)", daemon.OperatingSystem, daemon.ServerVersion, daemon.OSType),
	})

	checks = append(checks, o.capacity(daemon)...)
	checks = append(checks, o.mounts(daemon)...)
	checks = append(checks, o.rootlessSocket(daemon)...)
	return ep, checks, nil
}

// ErrFailed reports that a check failed; the checks carry what and why.
var ErrFailed = fmt.Errorf("a preflight check failed")

// Run is the whole suite, for nsctl doctor. It adds the checks a start makes
// elsewhere or not at all, and never stops at the first failure: someone
// running doctor wants the list, not the first item.
func Run(ctx context.Context, o Options) []Check {
	ep, checks, err := Gate(ctx, o)
	if err != nil {
		return checks
	}
	checks = append(checks, o.agreement(ctx, ep)...)
	if o.Environments != nil {
		checks = append(checks, o.Environments(ctx)...)
	}
	// Both name rows are warnings, not failures. Name resolution is reported,
	// not required (NERD025 SPEC004): nsctl dials the bare Floci names itself and
	// a platform whose suffix does not resolve still starts, deploys and serves
	// -- what is degraded is the legacy artifact path and reaching a user
	// interface by name. Failing here made `nsctl doctor` exit non-zero on a
	// working platform whose owner had simply not run one optional step, which is
	// a false negative for anything scripting it.
	if o.Hosts != nil {
		if err := o.Hosts(); err != nil {
			checks = append(checks, Check{Name: "host names", Status: StatusWarn, Detail: err.Error()})
		} else {
			checks = append(checks, Check{Name: "host names", Status: StatusOK, Detail: "resolve to loopback"})
		}
	}
	if o.Suffix != nil {
		if err := o.Suffix(); err != nil {
			checks = append(checks, Check{
				Name:   "local names",
				Status: StatusWarn,
				Detail: err.Error(),
				Remedy: "`nsctl dns status` says which of the two it is; `nsctl dns install` prints the one step that points this machine at the resolver",
			})
		} else {
			checks = append(checks, Check{
				Name: "local names", Status: StatusOK, Detail: "the wildcard suffix resolves",
			})
		}
	}
	return checks
}

func (o Options) resolver() *dockerhost.Resolver {
	if o.Resolver != nil {
		return o.Resolver
	}
	return dockerhost.NewResolver(o.Lookup)
}

func (o Options) connect(ctx context.Context, ep dockerhost.Endpoint) (dockerhost.Daemon, error) {
	if o.Connect == nil {
		return dockerhost.Daemon{}, fmt.Errorf("no way to reach %s", ep.Describe())
	}
	return o.Connect(ctx, ep)
}

// unreachable is the message the original bug report should have produced.
func unreachable(ep dockerhost.Endpoint, err, resolveErr error) string {
	var b strings.Builder
	fmt.Fprintf(&b, "nsctl cannot reach the container engine at %s.\n", ep.Describe())
	// Keyed on the host, not the source: a machine with no context configured
	// reports context "default" naming this same legacy socket, which is the
	// same situation and wants the same sentence.
	if ep.Host == dockerhost.DefaultHost() {
		b.WriteString("That is the legacy default socket, which nothing on this machine " +
			"selected. The docker CLI answers, so an engine is running somewhere else.\n")
	}
	if resolveErr != nil {
		fmt.Fprintf(&b, "Resolving the endpoint also reported: %v\n", resolveErr)
	}
	fmt.Fprintf(&b, "Reported by the engine: %v", err)
	return b.String()
}

func (o Options) capacity(d dockerhost.Daemon) []Check {
	c := Check{Name: "engine capacity", Detail: d.DescribeCapacity()}
	if d.NCPU > 0 && d.NCPU < MinCPU || d.MemTotal > 0 && d.MemTotal < MinMemory {
		c.Status = StatusWarn
		c.Detail = fmt.Sprintf("the container engine has %s. The local NeuronSphere runs Floci, "+
			"Postgres, a graph and a k3s cluster; below %d CPUs and %d GiB it will start slowly "+
			"and services may be OOM-killed with no message of their own",
			d.DescribeCapacity(), MinCPU, MinMemory>>30)
		c.Remedy = "On Colima: colima stop && colima start --cpu 4 --memory 12 --disk 100\n" +
			"On Docker Desktop: Settings -> Resources."
	}
	return []Check{c}
}

// mounts warns about bind-mount sources a VM-backed engine may not see.
//
// The rule that holds for every engine here is "outside the user's home", not
// a list of a particular runtime's shared directories: Docker Desktop shares a
// configurable set, Colima shares $HOME and /tmp/colima, and nsctl cannot read
// either. So this says "may not be visible" and names the paths, rather than
// asserting a failure it cannot confirm (NERD021 SPEC007).
func (o Options) mounts(d dockerhost.Daemon) []Check {
	goos := o.GOOS
	if !d.VMBacked(goos) {
		return []Check{{Name: "bind mounts", Status: StatusOK, Detail: "the engine runs on this host"}}
	}
	home := o.lookup("HOME")
	var outside []string
	for _, p := range []struct{ name, path string }{
		{"HMD_HOME", o.Home},
		{"HMD_REPO_HOME", o.lookup("HMD_REPO_HOME")},
	} {
		if p.path == "" || home == "" {
			continue
		}
		if !under(p.path, home) {
			outside = append(outside, fmt.Sprintf("%s  %s", p.name, p.path))
		}
	}
	detail := fmt.Sprintf("the engine runs in a virtual machine (%s on %s, from a %s host), "+
		"so a bind mount is visible to it only if the path is shared into that VM",
		daemonName(d), d.OSType, goos)
	if len(outside) == 0 {
		return []Check{{Name: "bind mounts", Status: StatusOK, Detail: detail + "; the paths nsctl mounts are under your home directory"}}
	}
	return []Check{{
		Name: "bind mounts", Status: StatusWarn,
		Detail: detail + ".\nThese are outside your home directory and may mount as empty " +
			"directories:\n    " + strings.Join(outside, "\n    "),
		Remedy: "Share them with the VM, or move them under your home directory.\n" +
			"On Colima: colima stop && colima start --mount <path>:w",
	}}
}

// rootlessSocket warns when the engine is rootless, and says nothing when it
// is not: a rootful engine is the ordinary case and doctor's value is the
// short list.
//
// SPEC011 decided the rootless socket would be a warning rather than a code
// change, because the path is right for every rootful engine and nsctl cannot
// know what a given rootless daemon put it at. The warning was the whole of
// that decision and was never wired, so the how-to promised a report nothing
// emitted.
//
// What makes it worth saying: the deploy nodes nsctl runs bind-mount
// /var/run/docker.sock to drive the engine themselves. Against a rootless
// daemon that source does not exist, and Docker creates a missing source as an
// empty directory rather than failing, so the deploy gets a socket that is not
// one (NERD021 SPEC011).
func (o Options) rootlessSocket(d dockerhost.Daemon) []Check {
	if !d.Rootless {
		return nil
	}
	return []Check{{
		Name: "engine socket", Status: StatusWarn,
		Detail: "the engine is rootless, so its socket is not at /var/run/docker.sock " +
			"(usually /run/user/<uid>/docker.sock). The containers nsctl runs to deploy " +
			"mount that path to reach the engine; with nothing there the daemon creates " +
			"an empty directory rather than reporting an error, and the deploy fails later " +
			"and somewhere else.",
		Remedy: "Use a rootful engine for the local platform, or expect deploy nodes to need the socket path adjusted.",
	}}
}

func daemonName(d dockerhost.Daemon) string {
	if d.OperatingSystem != "" {
		return d.OperatingSystem
	}
	return "the engine"
}

func under(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// agreement is the canary for this whole class of bug: the CLI and the Engine
// API must resolve the same endpoint, and nobody could see that they did not.
func (o Options) agreement(ctx context.Context, ep dockerhost.Endpoint) []Check {
	if o.CLIEndpoint == nil {
		return nil
	}
	out, err := o.CLIEndpoint(ctx)
	if err != nil {
		return nil
	}
	fields := strings.Split(strings.TrimSpace(out), "\t")
	if len(fields) < 2 || fields[1] == "" || fields[1] == ep.Host {
		return []Check{{Name: "CLI and API agree", Status: StatusOK, Detail: ep.Host}}
	}
	return []Check{{
		Name: "CLI and API agree", Status: StatusWarn,
		Detail: fmt.Sprintf("the docker CLI resolves %s but nsctl resolved %s; they must agree",
			fields[1], ep.Host),
		Remedy: "Please report this with the output of `docker context inspect`.",
	}}
}

// FirstFailure is the failed check's detail and remedy, for a refusal that has
// to be one error rather than a printed list.
func FirstFailure(checks []Check) string {
	for _, c := range checks {
		if c.Status == StatusFail {
			if c.Remedy == "" {
				return c.Detail
			}
			return c.Detail + "\n" + c.Remedy
		}
	}
	return "a preflight check failed"
}

// Failed reports whether any check failed.
func Failed(checks []Check) bool {
	for _, c := range checks {
		if c.Status == StatusFail {
			return true
		}
	}
	return false
}

// Warned reports whether any check warned.
func Warned(checks []Check) bool {
	for _, c := range checks {
		if c.Status == StatusWarn {
			return true
		}
	}
	return false
}

// Report prints findings.
func Report(w io.Writer, checks []Check) {
	for _, c := range checks {
		fmt.Fprintf(w, "%-20s %-8s %s\n", c.Name, c.Status, firstLine(c.Detail))
		for _, line := range restLines(c.Detail) {
			fmt.Fprintf(w, "%-29s %s\n", "", line)
		}
		for _, line := range strings.Split(strings.TrimRight(c.Remedy, "\n"), "\n") {
			if line != "" {
				fmt.Fprintf(w, "%-29s %s\n", "", line)
			}
		}
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func restLines(s string) []string {
	i := strings.IndexByte(s, '\n')
	if i < 0 {
		return nil
	}
	return strings.Split(strings.TrimRight(s[i+1:], "\n"), "\n")
}
