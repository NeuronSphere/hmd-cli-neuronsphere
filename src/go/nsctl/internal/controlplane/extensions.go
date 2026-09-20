package controlplane

import (
	"context"
	"sort"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/compose"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/container"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/cpext"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/hmdenv"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/keyring"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/router"
)

// extensionOptions is what internal/cpext needs to resolve a manifest.
//
// The compose lookup is the control plane's own, so an extension's file
// interpolates HMD_HOME, HMD_REPO_HOME and the network exactly as the bundled
// one does; cpext layers the manifest's own NS_* names on top (SPEC005).
func extensionOptions(opts *Options, reg *registry.Registry, project *compose.Project) cpext.Options {
	networks := map[string]compose.Network(nil)
	if project != nil {
		networks = project.Networks
	}
	return cpext.Options{
		Home:        opts.Home,
		RepoHome:    opts.lookup("HMD_REPO_HOME"),
		ProjectName: reg.ControlPlane.ComposeProject,
		Lookup:      ComposeEnv(opts, reg, ""),
		Networks:    networks,
	}
}

// ResolveExtensions reads $HMD_HOME/.config/control-plane.yaml and resolves
// every extension it declares.
//
// A manifest that is itself invalid is an error the caller decides about; a
// single extension that will not resolve is carried in its own Extension.Err
// and affects nothing else (SPEC006).
func ResolveExtensions(ctx context.Context, opts *Options, reg *registry.Registry,
	project *compose.Project) ([]cpext.Extension, error) {

	exts, err := cpext.Resolve(extensionOptions(opts, reg, project))
	if err != nil {
		return nil, nserr.Wrap(nserr.Usage, err)
	}
	resolveCredentials(ctx, opts, exts)
	return exts, nil
}

// resolveCredentials reads each declared keychain reference (SPEC009).
//
// On every resolve, which means on every start and every apply: a rotated
// keychain entry takes effect on the next one, and nothing watches the
// keychain. An unresolved reference is recorded on the credential and never
// fails the extension -- refusing to start a registry because one private
// upstream has no token would take the cached public index down with it.
func resolveCredentials(ctx context.Context, opts *Options, exts []cpext.Extension) {
	declared := false
	for _, e := range exts {
		if len(e.Credentials) > 0 {
			declared = true
			break
		}
	}
	if !declared {
		return
	}
	kr := keyring.New(opts.warn)
	if !kr.Available() {
		// Said once, rather than once per credential, and distinguished from
		// "nothing stored": this is a machine to configure, not a credential
		// to add.
		opts.warn("no host keychain helper (%s) is available, so no declared credential can be resolved",
			keyring.HelperName)
	}
	user := opts.lookup("USER")
	for i := range exts {
		exts[i].ResolveCredentials(ctx, kr, user)
	}
}

// aliasExtensions gives hmd_proxy a network alias per extension hostname, so a
// sibling container resolves the same name the host does (SPEC007).
//
// Done before the core project is started, which means the proxy's config hash
// covers the alias set: adding or removing an extension recreates the proxy,
// which is the only way the alias actually takes effect.
func aliasExtensions(project *compose.Project, exts []cpext.Extension) {
	hosts := cpext.Hosts(exts)
	if len(hosts) == 0 || project == nil {
		return
	}
	for i, s := range project.Services {
		if s.Key != compose.ProxyService {
			continue
		}
		for j, attach := range s.Networks {
			if attach.Name != cpext.PlatformNetwork {
				continue
			}
			project.Services[i].Networks[j].Aliases = append(
				append([]string(nil), attach.Aliases...), hosts...)
		}
	}
}

// applyExtensions starts the declared extensions and writes their vhosts.
//
// Never fatal. The caller decides what a failure is worth: `start` warns,
// because env start returns its error and an extension able to fail a start is
// an extension able to fail every environment start on the machine; `apply`
// exits non-zero, because a converge command that reported success while
// something did not converge is useless in a script (SPEC006).
func applyExtensions(ctx context.Context, opts *Options, r *router.Router,
	runner *compose.Runner, exts []cpext.Extension, cpOpts cpext.Options) cpext.Report {

	report := cpext.Apply(ctx, runner, exts, cpOpts, opts.step)

	// Written whole even when nothing is declared, so an extension that is no
	// longer in the manifest loses its listener by being absent -- the
	// property WriteControlPlaneVhosts already relies on.
	if err := r.WriteExtensionVhosts(cpext.Vhosts(report.Extensions)); err != nil {
		opts.warn("%v", err)
	}
	report.Handback = writeHandback(opts, report.Extensions)
	return report
}

// writeHandback contributes the declared variables back into hmd.env (SPEC010).
//
// Here rather than in either caller, so `start` and `apply` cannot drift: both
// funnel through applyExtensions, and a handback that only one of them wrote
// would be a variable whose presence depended on which verb you last used.
//
// The block is regenerated whole, including when nothing is declared -- the
// same reason the vhost fragment above is. Never fatal, for the same reason
// too: a control plane that is up with an unwritten handback is better than a
// start that failed over a file.
func writeHandback(opts *Options, exts []cpext.Extension) hmdenv.Result {
	vars, err := cpext.Handback(exts)
	if err != nil {
		opts.warn("%v", err)
		return hmdenv.Result{}
	}
	res, err := hmdenv.Upsert(opts.Home, vars)
	if err != nil {
		opts.warn("%v", err)
	}
	return res
}

// Apply converges the declared extensions without doing the rest of a start.
//
// The fast path SPEC003 asks for: no Floci health wait, no bootstrap, no
// gateway sweep, no route rewrite. `control-plane start` applies too -- an
// extension is up whenever the control plane is -- and this is what makes a
// manifest edit cost seconds rather than a full start.
func Apply(ctx context.Context, opts *Options) error {
	reg, err := registry.Load(opts.Home, opts.Lookup)
	if err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	docker := container.New()
	if !docker.Exists(ctx, router.ProxyContainer) {
		return nserr.New(nserr.Fail,
			"the control plane is not running. Start it first with `nsctl control-plane start`.")
	}

	project, err := Project(opts, reg, "")
	if err != nil {
		return err
	}
	exts, err := ResolveExtensions(ctx, opts, reg, project)
	if err != nil {
		return err
	}

	runner, err := compose.NewRunner(opts.Out, opts.Err)
	if err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}

	// Declaring nothing is a convergence too, and the interesting one: the
	// vhost fragment still has to be rewritten so a listener for an extension
	// that is no longer declared goes away, and containers left behind still
	// have to be reported. Returning early here left the last extension ever
	// declared serving forever.
	r := router.New(opts.Home, opts.Lookup)
	report := applyExtensions(ctx, opts, r, runner, exts, extensionOptions(opts, reg, project))
	reportUndeclared(ctx, opts, runner, project, exts)
	warnMissingAliases(ctx, opts, docker, reg.ControlPlane.Network, exts)

	if err := r.Reload(ctx, docker.Exec); err != nil {
		opts.warn("%v", err)
	}
	cpext.ReportHosts(opts.Out, report.Extensions)
	cpext.ReportCredentials(opts.Out, report)
	cpext.ReportHandback(opts.Out, report.Handback)
	cpext.ReportFailures(opts.Err, report)
	if !report.OK() {
		return nserr.New(nserr.DeployFailed, "%d of %d extension(s) did not start",
			len(report.Failures()), len(report.Extensions))
	}
	if len(exts) == 0 {
		opts.step("No extensions declared in %s.", extensionManifestPath(opts))
		return nil
	}
	opts.step("%d extension(s) up to date.", len(report.Extensions))
	return nil
}

// warnMissingAliases says when hmd_proxy does not yet answer for an
// extension's hostname on the platform network.
//
// Apply cannot fix it. The alias is part of the proxy's compose configuration,
// so it takes effect when the proxy is recreated, and recreating hmd_proxy is
// not something a fast path may do -- it is the only container publishing host
// ports and every route on the machine goes through it. Reconnecting it to the
// network live would change its IP under every established consumer.
//
// So it is reported rather than done, and reported precisely: the URL already
// works from the host, where /etc/hosts points the name at the proxy. It is
// sibling containers and cluster pods, which resolve the name through Docker,
// that need the alias and therefore a start.
func warnMissingAliases(ctx context.Context, opts *Options, docker *container.Docker,
	network string, exts []cpext.Extension) {

	hosts := cpext.Hosts(exts)
	if len(hosts) == 0 || network == "" {
		return
	}
	have := map[string]bool{}
	for _, alias := range docker.NetworkAliases(ctx, router.ProxyContainer, network) {
		have[alias] = true
	}
	var missing []string
	for _, host := range hosts {
		if !have[host] {
			missing = append(missing, host)
		}
	}
	if len(missing) == 0 {
		return
	}
	opts.warn("hmd_proxy does not answer for %s on the platform network yet, so other containers "+
		"cannot resolve it. It works from the host already; run `nsctl control-plane start` to finish wiring it.",
		strings.Join(missing, ", "))
}

// reportUndeclared names containers this project still runs that no extension
// declares any more.
//
// Reported and left alone, never destroyed -- the environment path's rule for
// the environment path's reason: removing a line from a manifest is not an
// instruction to destroy what it started; `docker rm` and `nsctl env purge`
// are the explicit verbs.
func reportUndeclared(ctx context.Context, opts *Options, runner *compose.Runner,
	project *compose.Project, exts []cpext.Extension) {

	declared := &compose.Project{Name: project.Name, Services: project.Services}
	for _, e := range exts {
		declared.Services = append(declared.Services, e.Services...)
	}
	for _, name := range sortedValues(runner.Adopted(ctx, declared)) {
		opts.warn("%s is running but no longer declared; nsctl leaves it alone", name)
	}
}

// extensionManifestPath is where the manifest is, or where one would go, for a
// message that tells the reader which file to create.
func extensionManifestPath(opts *Options) string {
	if path := manifest.FindControlPlane(opts.Home, opts.lookup); path != "" {
		return path
	}
	return manifest.ControlPlaneDefaultPath(opts.Home)
}

// sortedValues renders a service-keyed container map as a stable list.
func sortedValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
