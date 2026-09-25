package cpext

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/compose"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/hmdenv"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/router"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/dnsd"
)

// Report is what one apply did, per extension.
type Report struct {
	// Extensions is every declaration, resolved or not, in manifest order.
	Extensions []Extension
	// Results is what happened to each container that was attempted.
	Results []compose.Result
	// SecretProblems are credentials that resolved but could not be delivered.
	// Kept apart from Failures: the container is running, and calling it a
	// failed extension would be wrong in the direction that hides the running
	// one. Reported, and never carrying a value.
	SecretProblems []error
	// Handback is what the managed block of hmd.env now sets, and what it
	// yielded to a value the user set themselves (SPEC010).
	Handback hmdenv.Result
}

// Failures names every extension that did not come up, with its reason.
//
// One line per extension rather than per container: an extension whose three
// services all failed on the same missing image is one problem, and printing
// it three times buries the other extension that failed for a different one.
func (r Report) Failures() []string {
	reasons := map[string]string{}
	var order []string
	note := func(instance, reason string) {
		if _, seen := reasons[instance]; seen {
			return
		}
		reasons[instance] = reason
		order = append(order, instance)
	}
	for _, e := range r.Extensions {
		if e.Failed() {
			note(e.Instance, e.Err.Error())
		}
	}
	for _, res := range r.Results {
		if !res.Failed() {
			continue
		}
		note(instanceOf(res.Service, r.Extensions), res.Err.Error())
	}
	sort.Strings(order)
	lines := make([]string, 0, len(order))
	for _, instance := range order {
		// One line, whatever the underlying error did with newlines: a Docker
		// pull error carries its own, and a reason that wraps stops the list
		// reading as one entry per extension.
		lines = append(lines, fmt.Sprintf("%s: %s", instance, strings.Join(strings.Fields(reasons[instance]), " ")))
	}
	return lines
}

// OK reports that every declared extension came up.
func (r Report) OK() bool { return len(r.Failures()) == 0 }

// Running names the extensions with at least one container up and none failed.
func (r Report) Running() []string {
	failed := map[string]bool{}
	for _, line := range r.Failures() {
		failed[strings.SplitN(line, ":", 2)[0]] = true
	}
	var up []string
	for _, e := range r.Extensions {
		if !e.Failed() && !failed[e.Instance] {
			up = append(up, e.Instance)
		}
	}
	return up
}

// instanceOf maps a prefixed service key back to the extension that owns it.
func instanceOf(serviceKey string, exts []Extension) string {
	for _, e := range exts {
		if strings.HasPrefix(serviceKey, e.Instance+"-") {
			return e.Instance
		}
	}
	return serviceKey
}

// Apply brings every resolved extension's containers up.
//
// It never returns an error for one extension. SPEC006: a failed extension is
// reported, skipped, and left unable to affect the control plane's own
// services or another extension. The caller decides what a failure is worth --
// `control-plane start` warns, because env start returns its error and an
// extension able to fail a start is an extension able to fail every
// environment start on the machine; `control-plane apply` exits non-zero,
// because a converge command that reported success while something did not
// converge is useless in a script.
func Apply(ctx context.Context, runner *compose.Runner, exts []Extension, opts Options, step func(string, ...any)) Report {
	report := Report{Extensions: exts}
	if len(exts) == 0 {
		return report
	}

	// Before the containers, and not for tidiness: controlplane.Start
	// pre-creates its own directories for the stated reason that a missing one
	// would be created by Docker as root-owned, and an extension's state
	// directory inherits that hazard exactly (SPEC008).
	for i := range report.Extensions {
		e := &report.Extensions[i]
		if e.Failed() {
			continue
		}
		if err := os.MkdirAll(e.StateDir, 0o755); err != nil {
			e.Err = fmt.Errorf("creating %s: %w", e.StateDir, err)
			e.Services = nil
		}
	}

	// Env-delivered credentials are applied before the project is built,
	// because a container's environment is fixed at creation. That also puts
	// the value in the config hash, which is the one good thing about this
	// path: a rotated credential recreates the container instead of being
	// ignored until something else restarts it.
	for i := range report.Extensions {
		applyEnvSecrets(&report.Extensions[i])
	}

	project := Project(report.Extensions, opts)
	if len(project.Services) == 0 {
		return report
	}
	// Counted after the profile gate, not before: a component switched off is
	// not a container being started, and saying otherwise makes the count
	// disagree with the lines that follow it.
	active := Profiles(report.Extensions)
	enabled := 0
	for _, s := range project.Services {
		if s.EnabledBy(active) {
			enabled++
		}
	}
	step("Starting %s...", plural(enabled, "extension container"))
	results, err := runner.UpEach(ctx, project, active)
	report.Results = results
	if err != nil {
		// Reserved for a whole-project failure. Attribute it to every
		// extension rather than dropping it, so it cannot vanish.
		for i := range report.Extensions {
			if !report.Extensions[i].Failed() {
				report.Extensions[i].Err = err
			}
		}
		return report
	}
	for _, res := range results {
		if res.Action != compose.ActionSkipped && !res.Failed() {
			step("  %s %s", res.Name, res.Action)
		}
	}

	// After the containers are up, and on every apply -- including one that
	// created nothing. A tmpfs is empty again after any restart, so a delivery
	// that ran only when a container was created would leave the service
	// anonymous after a restart Docker initiated and nsctl did not drive.
	for _, e := range report.Extensions {
		if e.Failed() || !started(results, e) {
			continue
		}
		report.SecretProblems = append(report.SecretProblems,
			DeliverSecrets(ctx, runner, e, opts.ProjectName)...)
	}
	return report
}

// applyEnvSecrets folds an extension's env-delivered credentials into every one
// of its services.
func applyEnvSecrets(e *Extension) {
	env := e.EnvDelivered()
	if len(env) == 0 {
		return
	}
	for i := range e.Services {
		if e.Services[i].Environment == nil {
			e.Services[i].Environment = map[string]string{}
		}
		for k, v := range env {
			e.Services[i].Environment[k] = v
		}
	}
}

// started reports that at least one of this extension's containers came up, so
// there is something to deliver into.
func started(results []compose.Result, e Extension) bool {
	for _, res := range results {
		if res.Failed() || res.Action == compose.ActionSkipped {
			continue
		}
		if strings.HasPrefix(res.Service, e.Instance+"-") {
			return true
		}
	}
	return false
}

// Vhosts is the nginx vhost set for a resolved extension list.
func Vhosts(exts []Extension) []router.ExtensionVhost {
	var vhosts []router.ExtensionVhost
	for _, e := range exts {
		if e.Failed() || e.Host == "" {
			continue
		}
		vhosts = append(vhosts, router.ExtensionVhost{Name: e.Instance, Host: e.Host, Upstream: e.Upstream})
	}
	sort.Slice(vhosts, func(i, j int) bool { return vhosts[i].Name < vhosts[j].Name })
	return vhosts
}

// ReportFailures prints one line per failed extension, and the /etc/hosts line
// every reachable one needs.
//
// At the end rather than buried mid-run: a registry that failed to come up
// will otherwise be blamed for every build failure that follows until somebody
// scrolls back (SPEC006).
func ReportFailures(w io.Writer, report Report) {
	failures := report.Failures()
	if len(failures) == 0 {
		return
	}
	fmt.Fprintf(w, "warning: %s did not start. The rest of the control plane is unaffected.\n",
		plural(len(failures), "extension"))
	for _, line := range failures {
		fmt.Fprintf(w, "  %s\n", line)
	}
}

// ReportCredentials says which credential references resolved and which did
// not, and never what any of them resolved to.
//
// Without this an unresolved credential surfaces as a 401 from a cache, which
// looks exactly like an empty index -- hours from the cause and with nothing to
// correlate against.
func ReportCredentials(w io.Writer, report Report) {
	lines := CredentialReport(report.Extensions)
	if len(lines) == 0 && len(report.SecretProblems) == 0 {
		return
	}
	for _, line := range lines {
		fmt.Fprintf(w, "  credential %s\n", line)
	}
	for _, err := range report.SecretProblems {
		fmt.Fprintf(w, "warning: %v\n", err)
	}
}

// ReportHosts says how each extension's hostname is made to resolve. Derived
// rather than left to the reader, because a name that does not resolve has no
// obvious cause.
//
// Split by whether the name falls under the suffix the local resolver answers
// for. One arrangement covers every name in that subtree, including ones that do
// not exist yet, so demanding an /etc/hosts line per extension would be the very
// friction NERD026 removes -- and NERD025's requirement forbids reporting
// anything on this path as a missing entry in a file nobody was required to
// edit. A name outside the suffix is a different matter: the hosts line really
// is the only remedy, so it is still printed, for those names only.
func ReportHosts(w io.Writer, exts []Extension) {
	var covered, outside []string
	for _, h := range Hosts(exts) {
		if strings.HasSuffix(h, "."+dnsd.DefaultSuffix) || h == dnsd.DefaultSuffix {
			covered = append(covered, h)
			continue
		}
		outside = append(outside, h)
	}
	if len(covered) > 0 {
		fmt.Fprintf(w, "  extensions are served by name: %s\n", strings.Join(covered, " "))
		fmt.Fprintf(w, "    `nsctl dns install` makes every name under %s resolve, once.\n", dnsd.DefaultSuffix)
	}
	if len(outside) > 0 {
		fmt.Fprintf(w, "  these extension names are outside %s, so they need an /etc/hosts line:\n", dnsd.DefaultSuffix)
		fmt.Fprintf(w, "    127.0.0.1 %s\n", strings.Join(outside, " "))
	}
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
