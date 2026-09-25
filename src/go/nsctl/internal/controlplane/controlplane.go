// Package controlplane brings the shared half of a local NeuronSphere up and
// down.
//
// One control plane per HMD_HOME serves N environments. Stopping it takes every
// environment's emulated AWS with it -- their Lambdas, gateways, buckets and
// secrets, not just the deployment graph's availability -- which is why it has
// its own verb and its own refusal.
package controlplane

import (
	"context"
	"fmt"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/hosturl"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/authd"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bundled"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/compose"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/container"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/cpext"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/dnsd"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/doctor"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/floci"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/loopback"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/pgcheck"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/router"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/runner"
)

// HostsEntries are the names a host-side consumer of a presigned URL has to
// resolve.
//
// `neuronsphere` is baked into the presigned S3 and API URLs Floci hands back,
// so a consumer given one of those URLs has to resolve the name to follow it.
//
// nsctl no longer needs the machine to do that -- internal/loopback dials these
// names itself -- so this is a report rather than a requirement. It still
// matters for the legacy Python artifact path, which follows the same URLs
// through hmd_lib_librarian_client and has no such override.
var HostsEntries = []string{"neuronsphere", "neuronsphere-workload"}

// dockerClient is the Docker surface Stop needs to reach Floci-spawned
// containers, narrowed so the sweep is testable without a daemon.
type dockerClient interface {
	ContainersWithLabel(ctx context.Context, key, value string) []string
	ContainersWithLabelOnNetwork(ctx context.Context, key, value, network string) []string
	Running(ctx context.Context, name string) (bool, error)
	Stop(ctx context.Context, name string) error
}

var newDocker = func() dockerClient { return container.New() }

// GUIProfile gates the Deployment GUI container.
const GUIProfile = "deployment-gui"

// AuthClientID is the trusted client id every web application's secret lists.
//
// A name rather than an opaque id because there is no registry to allocate one
// from: locally a client's secret is its id, so the id may as well say what it
// is when it turns up in a token or a log.
const AuthClientID = "neuronsphere-local"

// AuthProfile gates the mock identity provider container.
const AuthProfile = "authd"

// DNSProfile gates the wildcard resolver container.
const DNSProfile = "dnsd"

// DNSContainer is the resolver's container name and DNSPort the port it
// listens on. Like the identity provider it publishes nothing to the host: it
// is reached through hmd_proxy, which is the only service that may publish a
// host port.
const DNSContainer = "hmd_dnsd"

// DNSPortEnv overrides the resolver's port. The name belongs to internal/dnsd,
// beside the port it overrides; this is an alias so a refusal naming it here and
// the reader honouring it there cannot drift apart.
const DNSPortEnv = dnsd.PortEnv

// DefaultDNSPort is where the resolver listens.
//
// Not 5353, the obvious choice: mDNS holds it on macOS -- shared between
// mDNSResponder, Chrome and Spotify with SO_REUSEPORT, which Docker's port
// publisher does not set -- so publishing it fails outright. See
// internal/dnsd.
const DefaultDNSPort = dnsd.DefaultPort

// portRemedy names every way to move a published port, because a refusal that
// states a precondition without naming what satisfies it is incomplete
// (NERD023 SPEC002).
//
// Every name here is the constant the reader honours, never a literal: a remedy
// naming a variable nothing reads is worse than no remedy, and writing the name
// twice is how that happens.
//
// Nothing is described as fixed any more. 80 and 4566 once were, and the refusal
// said so; since NERD025 SPEC008 they are chosen like the rest -- probed before
// the project is built and moved only when something else already holds them --
// so a conflict here means the platform could not place a port it needs, not
// that the user must free one.
const portRemedy = "  Move the platform's ports, or free the ones above:\n" +
	"    " + registry.HTTPPortEnv + "         the HTTP routes (80)\n" +
	"    " + registry.FlociPortEnv + "        the Floci stream (4566)\n" +
	"    " + registry.GUIPortEnv + "     the Deployment GUI (19003)\n" +
	"    " + DNSPortEnv + "          the wildcard resolver (19153)\n" +
	"    " + registry.EnvPortBaseEnv + "     where each environment's own ports are derived from\n" +
	"  nsctl already probes these and moves off a port something else holds; set one\n" +
	"  only to pin it somewhere of your choosing. An environment's Trino and k3s\n" +
	"  ports are published by its own router, not here."

// DNSEnabledEnv turns the wildcard resolver off.
//
// On by default. User interfaces are reached by name -- the way the cloud
// reaches them -- so the resolver is how they are reached at all, and a
// default-off resolver would make the common path the one nobody has switched
// on. It is inert until `nsctl dns install` points the machine at it, so
// running it costs a container and nothing else.
const DNSEnabledEnv = "HMD_LOCAL_NEURONSPHERE_ENABLE_DNS"

// AuthContainer is the identity provider's container name and AuthPort the port
// it listens on. Like the runner it publishes nothing to the host: it is
// reached through hmd_proxy, by name, so that one URL works from the browser,
// from a pod and from a Floci container alike.
const (
	AuthContainer = "hmd_authd"
	AuthPort      = 8080
)

// DefaultGUIPort is slot 0's spare port.
const DefaultGUIPort = 19003

// MSDeploymentURL is the control-plane route to hmd-ms-deployment.
func MSDeploymentURL() string { return hosturl.Route("hmd_ms_deployment") }

// ourProjects is every compose project whose published ports belong to this
// local NeuronSphere: the control plane's, and each environment's.
//
// An environment's router publishes that environment's Trino and k3s ports
// (NERD027 SPEC002), under the project label the registry already records. Left
// out, the next start probes a running environment's own listeners, finds them
// busy, and moves the platform's ports out from under the environment using them
// -- the same defect OurPorts' second return value was added to prevent, one
// scope wider (NERD027 SPEC004).
//
// An environment registered before routers existed has no project name, and an
// empty label filter matches every container on the machine, so those are
// skipped rather than passed through.
func ourProjects(reg *registry.Registry) []string {
	projects := []string{reg.ControlPlane.ComposeProject}
	for _, slug := range sortedEnvSlugs(reg) {
		if p := reg.Environments[slug].ComposeProject; p != "" {
			projects = append(projects, p)
		}
	}
	return projects
}

// sortedEnvSlugs keeps the project order stable, so a probe failure names the
// same project twice running.
func sortedEnvSlugs(reg *registry.Registry) []string {
	slugs := make([]string, 0, len(reg.Environments))
	for slug := range reg.Environments {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)
	return slugs
}

// applyChosenPorts re-resolves this process's host-facing facts after a start has
// settled which ports this home publishes.
//
// hosturl and the loopback redirect are process-global and resolved once, in
// PersistentPreRun, from the registry as it stood *before* the command ran
// (cmd/root.go applyHostPorts). That is the right shape and the wrong moment for
// a start that moves a port: every URL the rest of the start prints would name
// the old one, and the redirect would aim Floci's presigned URLs at a port
// nothing publishes any more. Idempotent, so calling it on the ordinary start
// where nothing moved costs a map read (NERD025 SPEC008).
func applyChosenPorts(reg *registry.Registry) {
	hosturl.Apply(reg.ControlPlane.Port(registry.PortHTTP), reg.ControlPlane.Port(registry.PortFloci))
	loopback.Install(nil, hosturl.FlociPort())
}

// Options configures a start or a stop.
type Options struct {
	Home   string
	Lookup func(string) string
	// Version is the -ldflags-injected build version. It tags the DAG-runner
	// image this nsctl builds, so an image can be traced back to the binary
	// that produced it.
	Version string
	Verbose bool
	// Upgrade pulls newer images before starting, rather than reusing what is
	// already local.
	Upgrade bool
	// StartingEnv is the environment the caller is about to start, exempt from
	// the reconciliation below. `env start` starts the control plane first, and
	// stopping the very containers it is about to start would be a visible
	// stop/start cycle for no reason. Empty for a bare `control-plane start`,
	// which is starting no environment at all.
	StartingEnv string
	Out         io.Writer
	Err         io.Writer
}

func (o *Options) lookup(key string) string {
	if o.Lookup == nil {
		return ""
	}
	return o.Lookup(key)
}

func (o *Options) step(format string, a ...any) {
	if o.Out == nil {
		return
	}
	fmt.Fprintf(o.Out, format+"\n", a...)
}

func (o *Options) warn(format string, a ...any) {
	if o.Err == nil {
		return
	}
	fmt.Fprintf(o.Err, "warning: "+format+"\n", a...)
}

// Resolver looks up a hostname. Injected so the pre-flight is testable.
type Resolver func(host string) ([]net.IP, error)

// hostNamesWarning is what a start says about the Floci host names, or "" when
// there is nothing to say.
//
// Driven by what actually fails to resolve, not by what was redirected. Those are
// different sets since NERD025 SPEC008 widened the dial rule: a name answering on
// loopback is redirected too, for the *port*, because the host port is no longer
// necessarily the in-network one. Reporting the redirected set told a machine that
// already had the /etc/hosts line that the names did not resolve, and pointed it
// at the line it already had -- which is precisely what this requirement forbids.
func hostNamesWarning(resolve Resolver) string {
	if err := CheckHostsEntries(resolve); err != nil {
		return err.Error()
	}
	return ""
}

// CheckHostsEntries verifies the host resolves the Floci aliases to loopback.
//
// Kept as a diagnostic after NERD025 SPEC004 removed it from the start path:
// `nsctl doctor` reports it, nothing gates on it. The seam is unchanged -- an
// injected Resolver, nothing runtime-sensitive -- so NERD021 SPEC012 still
// holds.
func CheckHostsEntries(resolve Resolver) error {
	if resolve == nil {
		resolve = net.LookupIP
	}
	var missing []string
	for _, host := range HostsEntries {
		ips, err := resolve(host)
		if err != nil || !anyLoopback(ips) {
			missing = append(missing, host)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return nserr.New(nserr.Usage, "%s", hostNamesNotice(missing))
}

// hostNamesNotice says what is degraded and names both remedies.
//
// It no longer stops a start: nsctl dials these names on loopback itself
// (internal/loopback), so what remains affected is the legacy Python artifact
// path -- `hmd build` and `push-artifact` dereference the same presigned URLs
// through hmd_lib_librarian_client, which has no such override. Saying so is
// the point: an instruction to edit a file, with no statement of what breaks
// without it, is what made this look mandatory when it was not.
func hostNamesNotice(missing []string) string {
	return fmt.Sprintf(
		"%s do not resolve on this host. nsctl reaches them anyway, but `hmd build` and `push-artifact` cannot follow a presigned URL until they do.\n"+
			"  Give the host the names with `nsctl dns install`, or add this line to /etc/hosts:\n\n    127.0.0.1 %s\n",
		strings.Join(missing, " and "), strings.Join(HostsEntries, " "))
}

func anyLoopback(ips []net.IP) bool {
	for _, ip := range ips {
		if ip.IsLoopback() {
			return true
		}
	}
	return false
}

// CheckNoLegacyEnvFlociState refuses to start when an environment still holds
// state from the container-per-environment layout.
//
// That state cannot be merged into the single Floci: it is namespaced on disk
// by account prefix in a format Floci does not document, so anything done with
// it would be a guess. Ignoring it silently would be worse than failing -- the
// environment's Lambdas, gateways, buckets and secrets would appear to have
// vanished while the start reported success.
func CheckNoLegacyEnvFlociState(reg *registry.Registry) error {
	var stale []string
	for _, name := range reg.Names() {
		env := reg.Environments[name]
		if env.LegacyLayout {
			// Its own Floci data dir *is* the control plane's, which is exactly
			// where its state belongs.
			continue
		}
		if entries, err := os.ReadDir(env.FlociDataDir()); err == nil && len(entries) > 0 {
			stale = append(stale, env.Slug)
		}
	}
	if len(stale) == 0 {
		return nil
	}
	return nserr.New(nserr.Usage,
		"environment(s) %s still hold Floci state from the container-per-environment layout, which cannot be migrated into the shared Floci.\nPurge them first: hmd neuronsphere down --purge --env <name>",
		strings.Join(stale, ", "))
}

// Start brings the control plane up.
//
// A never-bootstrapped HMD_HOME runs the bootstrap DAG (see Bootstrap); an
// already-bootstrapped one does not. That is a deliberate difference from the
// Python, which runs the DAG on every start. It can afford to because the work
// is idempotent, but it is not free -- it redeploys the Postgres through
// Terraform and re-registers three Lambdas on every `up`. Floci's persistent
// state carries the Lambdas and their gateways across a restart, so a warm
// start recovers the gateway ids by listing them instead, and only pays for
// what actually needs doing.
func Start(ctx context.Context, opts *Options) error {
	reg, err := registry.Load(opts.Home, opts.Lookup)
	if err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	// Docker first, because it is the one prerequisite this CLI claims to have
	// and the only failure every later step shares. Without it the first
	// symptom was whichever call happened to run first -- "connecting to
	// Docker: ..." from the Engine API, or a bare `docker: executable file not
	// found` from a shelled-out command -- neither of which says what to
	// install. The message that does say it was written and never called.
	// ... and the CLI answering is not the same question as the Engine API
	// answering. The two resolve the daemon differently, so a machine whose
	// docker context names anything but /var/run/docker.sock passed this gate
	// and then failed four steps later creating the network, with two port
	// warnings in between that were about its own proxy (NERD021 SPEC003).
	engine, checks, err := doctor.Gate(ctx, doctorOptions(opts))
	for _, c := range checks {
		if c.Status == doctor.StatusWarn {
			opts.warn("%s", strings.TrimSpace(c.Detail))
			if c.Remedy != "" {
				opts.warn("%s", strings.TrimSpace(c.Remedy))
			}
		}
	}
	if err != nil {
		return nserr.New(nserr.Usage, "%s", doctor.FirstFailure(checks))
	}
	loopback.Install(nil, reg.ControlPlane.Port(registry.PortFloci))
	// Reported, not refused. nsctl dials these itself (NERD025 SPEC003), so the
	// start proceeds; what is left degraded is the legacy Python artifact path,
	// which fetches the same presigned URLs through hmd_lib_librarian_client and
	// has no such override.
	if notice := hostNamesWarning(nil); notice != "" {
		opts.warn("%s", notice)
	}
	if err := CheckNoLegacyEnvFlociState(reg); err != nil {
		return err
	}

	// Persist a synthesized registry before anything writes state.
	//
	// With no registry file, Load infers whether this HMD_HOME was ever
	// brought up, and one of its signals is a non-empty floci/data. That was
	// sound when only the Python could create such state; it is not now,
	// because a bootstrap that fails partway leaves Floci data behind and the
	// next run would read it as "already bootstrapped" and skip the bootstrap
	// entirely. Writing the file first makes the answer explicit from here on
	// -- false on a genuinely new home, true on a real legacy install -- and
	// leaves "no registry but Floci data" meaning only what it used to.
	if reg.Synthesized {
		if err := reg.Save(opts.Home); err != nil {
			return nserr.Wrap(nserr.Fail, err)
		}
	}

	r := router.New(opts.Home, opts.Lookup)
	target := floci.ControlPlane(opts.Lookup)
	docker := container.New()

	// The route fragments and Floci's persistent state both live under
	// HMD_HOME, and the compose file binds them read-only -- a missing
	// directory would be created by Docker as root-owned.
	for _, dir := range []string{r.CacheDir(), reg.ControlPlane.FlociDataDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nserr.Wrap(nserr.Fail, fmt.Errorf("creating %s: %w", dir, err))
		}
	}

	// Before Floci starts, so it cannot rehydrate the ghosts.
	if pruned := floci.PruneAPIGatewayGhosts(reg.ControlPlane.FlociDataDir); pruned > 0 {
		opts.step("  pruned %d unusable API Gateway record(s)", pruned)
	}
	if _, err := r.WriteBootstrapConfig(target.Alias); err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}

	// From the endpoint the gate proved, not a second resolution: the whole
	// defect was two halves of nsctl addressing two different daemons.
	runner, err := compose.NewRunnerAt(engine, opts.Lookup, opts.Out, opts.Err)
	if err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}

	// Which host ports this home publishes, settled before the project is built
	// -- the compose file interpolates them, so choosing afterwards would
	// publish one set and probe another.
	//
	// ours is read first for the same reason CheckInUse needs it: the
	// platform's own published ports look foreign to a probe, and without this
	// a restart would walk the whole platform up the port space every time.
	ourPorts, portsKnown := runner.OurPorts(ctx, ourProjects(reg)...)
	if !portsKnown {
		ourPorts = nil
	}
	if moved, err := ChoosePorts(reg, ourPorts, nil); err != nil {
		return nserr.Wrap(nserr.Fail, err)
	} else if len(moved) > 0 {
		for _, m := range moved {
			opts.step("  %s", m)
		}
		// Recorded before anything is created. A port chosen and not persisted
		// would be chosen again differently on the next start, and every URL
		// derived from it would move with it.
		if err := reg.Save(opts.Home); err != nil {
			return nserr.Wrap(nserr.Fail, fmt.Errorf("recording the chosen host ports: %w", err))
		}
	}

	// The ports are settled; re-resolve everything derived from them before
	// anything is created or printed.
	applyChosenPorts(reg)

	guiImage := DeploymentGUIImage(ctx, opts, docker.ImagePresent)
	project, err := Project(opts, reg, guiImage)
	if err != nil {
		return err
	}
	active := ActiveProfiles(opts)

	// Resolved here, before anything is created, for two reasons. The proxy
	// takes an alias per extension hostname and its config hash has to cover
	// them, so the alias set has to be known before the core project starts.
	// And a manifest that will not parse is worth saying now rather than after
	// a five-minute bootstrap.
	//
	// An invalid manifest is a warning, not a refusal: the control plane is
	// what a user needs in order to fix one, so failing the start would be the
	// least helpful moment to object (SPEC006).
	exts, err := ResolveExtensions(ctx, opts, reg, project)
	if err != nil {
		opts.warn("%v", err)
		opts.warn("no extensions will be started; the rest of the control plane is unaffected")
	}
	aliasExtensions(project, exts)

	// Before anything is created, and before the ports check, because this is
	// the failure that eats someone else's platform rather than merely failing
	// this one.
	//
	// The four control-plane services pin container_name, so their names are
	// global while every other identifier -- network, compose project, Floci
	// data dir -- is namespaced by an HMD_HOME hash. Starting here would
	// force-remove and recreate another HMD_HOME's containers pointed at this
	// home, and interrupting that start leaves the other with no proxy at all.
	// It has happened.
	if owners := compose.CheckOwnership(ctx, runner.API, project, active); len(owners) > 0 {
		return nserr.New(nserr.InUse,
			"another HMD_HOME's control plane already owns these containers:\n  %s\n"+
				"This HMD_HOME is %s (project %s). The control-plane container names are fixed by "+
				"the compose file and are not namespaced, so starting here would recreate them "+
				"pointed at this home and leave the other one without them. Stop that control "+
				"plane first, with `nsctl control-plane stop --home <its HMD_HOME>`.",
			compose.JoinOwners(owners), opts.Home, reg.ControlPlane.ComposeProject)
	}

	opts.step("Validating ports...")
	if conflicts := compose.CheckExclusivePublisher(project, active); len(conflicts) > 0 {
		return nserr.New(nserr.Usage,
			"only hmd_proxy may publish host ports in this layout, but:\n  %s", joinConflicts(conflicts))
	}
	for _, c := range compose.CheckReserved(project, active) {
		opts.warn("%s", c)
	}
	ours, known := ourPorts, portsKnown
	if !known {
		// Without this the findings below read as fact. They are guesses:
		// every port the platform itself publishes looks foreign when the
		// engine could not be asked which ones are ours (NERD021 SPEC005).
		opts.warn("could not ask the container engine which ports the local NeuronSphere " +
			"already publishes; the port warnings below may name ports that are its own")
	}
	if conflicts := compose.CheckInUse(ctx, project, active, ours, nil); len(conflicts) > 0 {
		// A taken port is not a risk the start can take: hmd_proxy publishes
		// its ports as one contiguous range, so a single foreign listener fails
		// the whole container and takes every route down with it. Warning and
		// continuing only delays the same failure into the engine's own
		// message, which names the port and nothing else.
		//
		// Unless the engine could not be asked which ports are ours. Then the
		// findings are guesses, and refusing on a guess would block a start
		// over the platform's own ports.
		if !known {
			for _, c := range conflicts {
				opts.warn("%s", c)
			}
		} else {
			return nserr.New(nserr.InUse,
				"these host ports are in use by something outside the local NeuronSphere:\n  %s\n%s",
				joinConflicts(conflicts), portRemedy)
		}
	}

	// Before compose: a profile is active for a container whose image does not
	// exist yet, and compose would fail trying to pull it from a registry that
	// does not have it.
	// The identity provider is this binary in a container -- the Dockerfile's
	// ENTRYPOINT is /nsctl -- so enabling it needs the image built. So does the
	// resolver, which runs the same image and is on by default.
	if NsctlImageNeeded(opts) {
		if err := EnsureNsctlImage(ctx, opts, docker); err != nil {
			return err
		}
	}

	if err := runner.EnsureNetwork(ctx, reg.ControlPlane.Network); err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}

	if opts.Upgrade {
		opts.step("Upgrade -- pulling the latest images...")
		for _, s := range project.Services {
			if !s.EnabledBy(active) {
				continue
			}
			if err := runner.Pull(ctx, s.Image); err != nil {
				opts.warn("%v", err)
			}
		}
	}

	// Before compose starts Floci, because after it the container is recreated
	// from the configured image and the refusal arrives as a crash loop that
	// nothing attributes to a version bump.
	if err := checkPostgresMajor(ctx, opts, docker, project, reg); err != nil {
		return err
	}

	// Which environment containers were up before Floci is, so the
	// reconciliation after it can tell what Floci woke from what the user had
	// running. Taken here because the next call starts Floci.
	runningBefore := runningEnvContainers(ctx, docker, reg)

	opts.step("Starting control-plane containers...")
	results, err := runner.Up(ctx, project, active)
	if err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	for _, res := range results {
		if res.Action != compose.ActionSkipped {
			opts.step("  %s %s", res.Name, res.Action)
		}
	}

	// Before waiting on Floci, because the wait goes *through* the proxy.
	// Floci publishes nothing itself; :4566 is an nginx stream listener. So a
	// proxy that cannot start reports as "Floci is not ready after 5m0s" --
	// five minutes spent on the wrong diagnosis, against a Floci that is
	// healthy and says so in its own logs.
	if err := checkProxyStarted(ctx, opts, docker); err != nil {
		return err
	}

	opts.step("Waiting for Floci...")
	if err := target.WaitForHealth(ctx, 5*time.Minute, func(waited, total time.Duration) {
		opts.step("  still waiting (%s/%s)...", waited.Round(time.Second), total)
	}); err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}

	// The control plane's Postgres. Floci leaves the backing container stopped
	// on shutdown and nothing restarts it: the Python relies on the bootstrap
	// DAG's Terraform apply, and wait_for_rds_instance only polls. Without this
	// ms-deployment comes back up and answers 500 on every request while its
	// instance still reports available.
	opts.step("Starting the control-plane database...")
	names := floci.NamesFrom(opts.Lookup, "", "local")
	dbID := floci.ControlPlaneDBIdentifier(names)
	dbContainer, err := floci.EnsureRDSRunning(ctx, docker, target.AccountID, dbID,
		floci.ControlPlaneDBAlias, reg.ControlPlane.Network, 2*time.Minute, 0)
	switch {
	case err != nil:
		opts.warn("%v", err)
	case dbContainer == "":
		if reg.ControlPlane.Bootstrapped {
			opts.warn("no database container for %s; the control plane has never deployed one", dbID)
		}
		// Not bootstrapped: there is no instance yet because nothing has
		// deployed one. Bootstrap does that below.
	default:
		opts.step("  %s is up and aliased as %s", dbContainer, floci.ControlPlaneDBAlias)
	}

	// Floci spawns its database and graph backends from image references
	// pinned into its own configuration, and a reference that resolves nowhere
	// is not reported as a configuration error: the instance goes to state
	// "failed" while Terraform polls "Still creating..." until someone kills
	// it. Checked before the bootstrap deploys either, so a wrong registry or
	// version costs seconds instead of minutes and says which variable is at
	// fault.
	if !reg.ControlPlane.Bootstrapped {
		for _, problem := range floci.EnsureBackendImages(ctx, docker, floci.ContainerName, opts.step,
			floci.RegistryHint{Home: opts.Home, Lookup: opts.Lookup}) {
			return nserr.New(nserr.Usage, "%s", problem)
		}
	}

	// Floci stops the containers it spawned when it restarts and does not
	// bring them back, so the graph needs the same restart the database above
	// just had. Only on a warm start: during a bootstrap the DAG deploys the
	// graph and aliases it as one of its nodes.
	//
	// Without this the control plane comes up with `global-graph` resolving to
	// nothing, and every consumer of it fails in its own way -- Trino
	// crashlooping on "global-graph: Name or service not known" is the visible
	// one, several layers from the cause.
	if reg.ControlPlane.Bootstrapped {
		graphContainer, err := floci.EnsureNeptuneRunning(ctx, docker, target.AccountID,
			CPGraphIdentifier(names), CPGraphHost, reg.ControlPlane.Network)
		switch {
		case err != nil:
			opts.warn("%v", err)
		case graphContainer != "":
			opts.step("  %s is up and aliased as %s", graphContainer, CPGraphHost)
		}
	}

	opts.step("Provisioning control-plane Floci resources...")
	prov, err := floci.NewProvisioner(ctx, target, opts.Out, opts.Err)
	if err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	if err := prov.Provision(ctx, names, floci.ControlPlaneDBAlias); err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}

	restoreEnvironmentState(ctx, opts, docker, reg, runningBefore)

	// Pulled once here rather than discovered missing midway through a deploy.
	//
	// SPEC008 made this a prerequisite of *cluster provisioning* too, on the
	// premise that kubectl would run inside projectbuilder. It does not --
	// projectbuilder ships no kubectl -- so Kubernetes work execs into the k3s
	// node instead and this is back to being a deploy-time dependency only. It
	// is still pulled early because a first deploy otherwise pays for it at the
	// least convenient moment.
	if err := EnsureProjectBuilder(ctx, opts, runner); err != nil {
		opts.warn("%v", err)
	}

	if !reg.ControlPlane.Bootstrapped {
		if err := Bootstrap(ctx, opts, reg, target, docker, r, names); err != nil {
			return err
		}
	}

	// The `okta` secret, whenever auth is on.
	//
	// On every start rather than in Bootstrap: the bootstrap happens once, and
	// toggling the identity provider on afterwards has to take effect without
	// purging the control plane to get a second bootstrap.
	if AuthEnabled(opts) {
		if err := seedAuthSecret(ctx, opts, target); err != nil {
			opts.warn("%v", err)
		} else {
			opts.step("  identity provider issuing as %s", AuthIssuerBase(opts))
		}
	}

	// After Bootstrap, and last of the container work. Nothing here needs the
	// deployment graph -- an extension is not a deployment (SPEC003) -- but a
	// start that brought extensions up before the platform they sit beside
	// would report them running against a control plane that was not yet.
	extReport := applyExtensions(ctx, opts, r, runner, exts, extensionOptions(opts, reg, project))
	reportUndeclared(ctx, opts, runner, project, exts)

	opts.step("Configuring control-plane routes...")
	gateways, err := floci.NewGateways(ctx, target)
	if err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	services, err := gateways.ServiceGateways(ctx)
	if err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	if len(services) == 0 {
		opts.warn("Floci reports no control-plane API Gateways; the services may not be deployed yet")
	}
	for _, id := range uniqueValues(services) {
		if err := gateways.DeployStage(ctx, id, floci.DefaultStage); err != nil {
			opts.warn("%v", err)
		}
	}

	if err := r.WriteBaseConfig(); err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	if err := r.WriteControlPlaneRoutes(services, floci.DefaultStage, "", nil); err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	dnsHost, dnsPort := "", 0
	if DNSEnabled(opts) {
		dnsHost, dnsPort = DNSContainer, DNSPort(opts, reg)
	}
	if err := r.WriteControlPlaneStreams(target.Alias, dnsHost, dnsPort); err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	if err := r.WriteControlPlaneVhosts(router.ControlPlaneVhosts{
		GUIEnabled: GUIEnabled(opts), GUIPort: GUIPort(opts, reg),
		AuthHost: AuthHost(opts),
	}); err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	if err := r.Reload(ctx, docker.Exec); err != nil {
		// A proxy that did not reload still serves its old routes, which beats
		// aborting the start.
		opts.warn("%v", err)
	}

	opts.step("Control plane ready.")
	opts.step("  services   %s/", MSDeploymentURL())
	opts.step("  floci      %s", target.Endpoint)
	if GUIEnabled(opts) {
		opts.step("  gui        http://localhost:%d", GUIPort(opts, reg))
	}
	for _, e := range extReport.Extensions {
		if !e.Failed() && e.URL != "" {
			opts.step("  %-10s %s", e.Instance, e.URL)
		}
	}
	cpext.ReportHosts(opts.Out, extReport.Extensions)
	cpext.ReportCredentials(opts.Out, extReport)
	cpext.ReportHandback(opts.Out, extReport.Handback)

	// Last, and a warning rather than an error. `env start` calls this and
	// returns what it returns, so an extension able to fail a start is an
	// extension able to fail every environment start on the machine (SPEC006).
	cpext.ReportFailures(opts.Err, extReport)
	return nil
}

// envAccounts is the registry as the wake sweep wants it: a slug and the Floci
// account its resources live in.
func envAccounts(reg *registry.Registry) []floci.EnvAccount {
	out := make([]floci.EnvAccount, 0, len(reg.Environments))
	for _, slug := range reg.Names() {
		out = append(out, floci.EnvAccount{Slug: slug, AccountID: reg.Environments[slug].AccountID})
	}
	return out
}

// runningEnvContainers snapshots what the user already had running, before the
// call that starts Floci.
func runningEnvContainers(ctx context.Context, d dockerClient, reg *registry.Registry) map[string]bool {
	return floci.RunningEnvContainers(ctx, d, envAccounts(reg), reg.ControlPlane.Network)
}

// restoreEnvironmentState puts back down the environments Floci woke on its way
// up, so that starting the control plane does not start anything else. See
// floci.StopWoken for why Floci does that and why it is right of it to.
func restoreEnvironmentState(ctx context.Context, opts *Options, d dockerClient, reg *registry.Registry, before map[string]bool) {
	stopped, failures := floci.StopWoken(ctx, d, envAccounts(reg), reg.ControlPlane.Network, before, opts.StartingEnv)
	for _, f := range failures {
		opts.warn("leaving %s running: Floci started it for the %s environment and stopping it failed: %v",
			f.Container, f.Slug, f.Err)
	}
	if len(stopped) == 0 {
		return
	}
	were := "were"
	if len(stopped) == 1 {
		were = "was"
	}
	opts.step("  stopped what Floci restarted for %s, which %s not running", strings.Join(stopped, ", "), were)
}

// stopFlociSpawned stops every container Floci has spawned via the host
// docker socket -- RDS, Neptune, and whatever else it grows. These are
// created imperatively, not as compose services or Adopted-label containers,
// so neither of Stop's other sweeps touches them; left running, they orphan
// against the network the next start recreates. Found by the floci label, the
// same way PurgeAll's catch-all sweep finds them.
func stopFlociSpawned(ctx context.Context, opts *Options, d dockerClient) {
	for _, name := range d.ContainersWithLabel(ctx, container.LabelFloci, "") {
		if err := d.Stop(ctx, name); err != nil {
			opts.warn("%v", err)
		}
	}
}

// Stop stops the control plane's containers, leaving the network and every
// environment's persistent state in place so the next start restarts them.
//
// running is the environments the caller found up; the refusal is the caller's
// to make, so this stays a pure stop.
func Stop(ctx context.Context, opts *Options) error {
	reg, err := registry.Load(opts.Home, opts.Lookup)
	if err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	// The GUI image does not matter for a stop; the project is parsed for its
	// service list alone. No profile set is passed either -- Stop is
	// deliberately profile-blind, because a container started while a profile
	// was active must still be stopped by a run without it.
	project, err := Project(opts, reg, "")
	if err != nil {
		return err
	}
	runner, err := compose.NewRunner(ctx, opts.Out, opts.Err)
	if err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}

	opts.step("Stopping control-plane containers...")
	// Extensions first, by label rather than by re-resolving the manifest.
	// A container whose repo class has since been deleted, or whose
	// declaration has been removed, still has to stop -- and it is exactly the
	// one a name-driven sweep would miss and leave running against a network
	// that is about to go away.
	if err := runner.StopNamed(ctx, sortedValues(runner.Adopted(ctx, project))); err != nil {
		// A warning: an extension that would not stop is not a reason to leave
		// the control plane running.
		opts.warn("%v", err)
	}
	stopFlociSpawned(ctx, opts, newDocker())
	if err := runner.Stop(ctx, project); err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	// The network is deliberately left in place so stopped containers can
	// restart onto it. Only a purge removes it.
	return nil
}

// Remove is Stop for a purge: the containers go rather than stopping.
//
// `control-plane stop` must leave them in place -- a stopped container restarts
// onto the same network with the same config, which is the whole of a warm
// start. A purge must not: it removes the network those containers are attached
// to and every path under $HMD_HOME they are configured from, so leaving four
// exited containers named after a platform that no longer exists is a leftover,
// and `docker ps -a` said so on the first real run.
//
// compose.Runner.Remove was written for this ("Only a purge does this") and
// nothing called it. PurgeAll went through Stop, so a full purge left hmd_proxy,
// floci, hmd_deployment_gui and hmd_nsrunner behind as Exited.
func Remove(ctx context.Context, opts *Options) error {
	reg, err := registry.Load(opts.Home, opts.Lookup)
	if err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	// Profile-blind for the same reason Stop is: a container started while a
	// profile was active must still be removed by a run without it.
	project, err := Project(opts, reg, "")
	if err != nil {
		return err
	}
	runner, err := compose.NewRunner(ctx, opts.Out, opts.Err)
	if err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}

	opts.step("Removing control-plane containers...")
	if err := runner.RemoveNamed(ctx, sortedValues(runner.Adopted(ctx, project))); err != nil {
		opts.warn("%v", err)
	}
	if err := runner.Remove(ctx, project); err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	return nil
}

// EnsureProjectBuilder pulls the image every kubectl call runs inside.
func EnsureProjectBuilder(ctx context.Context, opts *Options, runner *compose.Runner) error {
	image := ProjectBuilderRef(opts)
	opts.step("Ensuring %s is present...", image)
	return runner.EnsureImage(ctx, image)
}

// ProjectBuilderRef is the projectbuilder image, honouring the version pin.
func ProjectBuilderRef(opts *Options) string {
	return runner.ProjectBuilderRef(opts.Lookup)
}

// ComposeCacheDir is where the embedded compose file is materialised.
//
// SPEC006 already requires this for anything an external tool has to read --
// neither docker nor helm can read an embed.FS. Here it also gives the
// com.docker.compose.project.config_files label a real path to point at, so
// `docker compose -p <project> ps` works from any directory.
func ComposeCacheDir(home string) string {
	return filepath.Join(home, ".cache", "neuronsphere")
}

// Project parses the bundled compose file for this HMD_HOME, materialising it
// alongside so the compose labels can name it.
func Project(opts *Options, reg *registry.Registry, guiImage string) (*compose.Project, error) {
	data, err := bundled.Read(bundled.ControlPlaneComposeFile)
	if err != nil {
		return nil, nserr.Wrap(nserr.Fail, fmt.Errorf("reading the bundled compose file: %w", err))
	}
	project, err := compose.Parse(data, reg.ControlPlane.ComposeProject, ComposeEnv(opts, reg, guiImage))
	if err != nil {
		return nil, nserr.Wrap(nserr.Fail, err)
	}

	dir := ComposeCacheDir(opts.Home)
	path := filepath.Join(dir, filepath.Base(bundled.ControlPlaneComposeFile))
	if err := os.MkdirAll(dir, 0o755); err == nil && os.WriteFile(path, data, 0o644) == nil {
		project.ConfigFile = path
		project.WorkingDir = filepath.Join(opts.Home, ".cache")
	}
	return project, nil
}

// ComposeEnv layers the values export_control_plane_compose_env sets over the
// process environment, so the compose file interpolates exactly as it does
// under the Python CLI -- without any of it reaching os.Environ.
//
// guiImage is resolved by the caller, since finding a locally cached image
// needs Docker. An empty one leaves the compose file's own default in play --
// the published :stable tag, which is degraded but not a reason to fail a
// start, matching what the Python does when resolution raises.
func ComposeEnv(opts *Options, reg *registry.Registry, guiImage string) compose.Lookup {
	envLo, envHi := reg.ControlPlane.EnvPortRange()
	overlay := map[string]string{
		"HMD_HOME":                    opts.Home,
		"NEURONSPHERE_DOCKER_NETWORK": reg.ControlPlane.Network,
		registry.GUIPortEnv:           strconv.Itoa(GUIPort(opts, reg)),
		// The ports this home settled on. A value the user exported still wins
		// -- see the lookup below -- so these are a default that already knows
		// what else is running on the machine, not an override.
		registry.HTTPPortEnv:     strconv.Itoa(reg.ControlPlane.Port(registry.PortHTTP)),
		registry.FlociPortEnv:    strconv.Itoa(reg.ControlPlane.Port(registry.PortFloci)),
		registry.TrinoPortEnv:    strconv.Itoa(reg.ControlPlane.Port(registry.PortTrino)),
		"HMD_LOCAL_DNS_PORT":     strconv.Itoa(reg.ControlPlane.Port(registry.PortDNS)),
		registry.EnvPortRangeEnv: fmt.Sprintf("%d-%d", envLo, envHi),
	}
	if guiImage != "" {
		overlay["HMD_DEPLOYMENT_GUI_IMAGE"] = guiImage
	}
	// Always set, so the compose file's own default -- a plain
	// hmd-img-nsctl:latest, which is only ever a hand build -- never decides
	// which image these containers run. EnsureNsctlImage has already made sure
	// this tag exists.
	if NsctlImageNeeded(opts) {
		overlay["HMD_NSCTL_IMAGE"] = NsctlImageRef(opts)
	}
	if AuthEnabled(opts) {
		// The issuer reaches the container, because the server stamps it into
		// every token's `iss` and has no other way to know the name it is
		// reached by -- deriving it from the request's Host would produce a
		// different issuer per caller and reject every token somewhere.
		overlay[authd.IssuerEnv] = AuthIssuerBase(opts)
	}
	// The runner mounts HMD_REPO_HOME at its own absolute path, so the compose
	// interpolation needs a real one. Defaulting it to HMD_HOME keeps the bind
	// valid when the variable is unset; the runner then reports a missing
	// working tree per repo, which names the actual problem, rather than
	// compose failing to create the container at all.
	repoHome := opts.lookup("HMD_REPO_HOME")
	if repoHome == "" {
		repoHome = opts.Home
	}
	overlay["HMD_REPO_HOME"] = repoHome
	return func(key string) string {
		// A value the user set wins over the overlay -- except HMD_HOME, which
		// is the home this control plane is *for*: --home (or the HMD_HOME the
		// command resolved) is the statement, and an HMD_HOME exported for
		// another home in the same shell must not redirect Floci's data dir
		// and network to that other home.
		if key == "HMD_HOME" {
			return opts.Home
		}
		if v := opts.lookup(key); v != "" {
			return v
		}
		return overlay[key]
	}
}

// ActiveProfiles is the compose profile set, which is how the GUI is gated.
func ActiveProfiles(opts *Options) map[string]bool {
	active := map[string]bool{}
	if GUIEnabled(opts) {
		active[GUIProfile] = true
	}
	if AuthEnabled(opts) {
		active[AuthProfile] = true
	}
	if DNSEnabled(opts) {
		active[DNSProfile] = true
	}
	return active
}

// DNSPort is the host port hmd_proxy streams the resolver at.
//
// Read back from the registry, not only from the environment. The port is
// *chosen* (NERD025 SPEC008) when 19153 is already held, and the chosen value is
// what compose publishes -- so a writer that read the constant instead made the
// proxy publish one port and stream to another, and the suffix stopped resolving
// with nothing to see but a timeout. The user's own override still wins over
// what was probed; a nil or silent registry keeps the historical port.
func DNSPort(opts *Options, reg *registry.Registry) int {
	if raw := opts.lookup(DNSPortEnv); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			return n
		}
	}
	if reg != nil {
		if p := reg.ControlPlane.Port(registry.PortDNS); p > 0 {
			return p
		}
	}
	return DefaultDNSPort
}

// NsctlImageNeeded reports whether anything in the control plane runs this
// binary in a container.
//
// Two things do -- the identity provider and the wildcard resolver -- and the
// image is a local build that no registry has ever held. Both the build and the
// HMD_NSCTL_IMAGE overlay were gated on the identity provider alone, which was
// correct until the resolver started running by default: after that, a machine
// with authd off (the default) reached the engine and failed trying to pull
// hmd-img-nsctl:latest. Found on a live start, which is the only place it could
// be found (NERD026 SPEC001).
func NsctlImageNeeded(opts *Options) bool {
	return AuthEnabled(opts) || DNSEnabled(opts)
}

// DNSEnabled reports whether the wildcard resolver should run: true unless
// explicitly falsy, matching GUIEnabled.
func DNSEnabled(opts *Options) bool {
	switch strings.ToLower(strings.TrimSpace(opts.lookup(DNSEnabledEnv))) {
	case "0", "false", "no":
		return false
	}
	return true
}

// AuthEnabled, AuthHost and AuthIssuerBase read the identity provider's
// switches. The logic is in internal/authd because internal/environment needs
// the hostname too and must not import this package.
func AuthEnabled(opts *Options) bool { return authd.Enabled(opts.lookup) }

// AuthHost is the hostname the identity provider is served at, or "".
func AuthHost(opts *Options) string { return authd.Host(opts.lookup) }

// AuthIssuerBase is the one URL it is reached at, from everywhere.
func AuthIssuerBase(opts *Options) string { return authd.IssuerBase(opts.lookup) }

// GUIEnabled matches bom_seeder.gui_enabled: true unless explicitly falsy.
func GUIEnabled(opts *Options) bool {
	switch strings.ToLower(strings.TrimSpace(opts.lookup("HMD_LOCAL_NEURONSPHERE_ENABLE_GUI"))) {
	case "0", "false", "no":
		return false
	}
	return true
}

// GUIPort is the host port hmd_proxy serves the GUI at.
//
// DNSPort's reasoning, with one extra hazard of its own: the GUI was published
// only because 19003 happens to fall inside the environment band, so a band that
// moved took the Deployment GUI off the host altogether. It is now a chosen port
// in its own right, which is also what lets it be published explicitly
// (NERD027 SPEC001).
func GUIPort(opts *Options, reg *registry.Registry) int {
	if raw := opts.lookup(registry.GUIPortEnv); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			return n
		}
	}
	if reg != nil {
		if p := reg.ControlPlane.Port(registry.PortGUI); p > 0 {
			return p
		}
	}
	return DefaultGUIPort
}

func joinConflicts(conflicts []compose.Conflict) string {
	lines := make([]string, 0, len(conflicts))
	for _, c := range conflicts {
		lines = append(lines, c.String())
	}
	return strings.Join(lines, "\n  ")
}

// uniqueValues is the distinct gateway ids, sorted, so a gateway fronting two
// service names is deployed once.
func uniqueValues(m map[string]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range m {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

// seedAuthSecret writes the `okta` secret pointing at the local provider.
func seedAuthSecret(ctx context.Context, opts *Options, target floci.Target) error {
	prov, err := floci.NewProvisioner(ctx, target, opts.Out, opts.Err)
	if err != nil {
		return fmt.Errorf("reaching Floci to write the okta secret: %w", err)
	}
	base := AuthIssuerBase(opts)
	return prov.OktaSecret(ctx,
		authd.IssuerFor(base, authd.ServerNS),
		authd.IssuerFor(base, authd.ServerServices),
		AuthClientID)
}

// ConfiguredPostgresImage is the image Floci will spawn RDS backends from, read
// off the parsed compose project.
//
// Read from the project rather than rebuilt from the environment: the value
// lives behind two nested ${VAR:-default} expansions, and a reconstruction that
// disagrees with what Floci is actually handed would check the wrong image --
// the same trap floci.K3sWrapperImage documents for the cluster.
func ConfiguredPostgresImage(project *compose.Project) string {
	if project == nil {
		return ""
	}
	for _, s := range project.Services {
		if s.ContainerName == floci.ContainerName || s.Key == "floci" {
			return s.Environment["FLOCI_SERVICES_RDS_DEFAULT_POSTGRES_IMAGE"]
		}
	}
	return ""
}

// checkPostgresMajor refuses a start whose postgres image cannot read the data
// directories already on disk.
func checkPostgresMajor(ctx context.Context, opts *Options, docker *container.Docker,
	project *compose.Project, reg *registry.Registry) error {

	image := ConfiguredPostgresImage(project)
	if image == "" {
		return nil
	}
	mismatches := pgcheck.FindMismatches(ctx, docker, image, reg.ControlPlane.FlociDataDir)
	if len(mismatches) == 0 {
		return nil
	}
	return nserr.New(nserr.Usage, "%s", pgcheck.Explain(mismatches))
}

// ProxyContainer is the nginx container every host-facing route goes through.
const ProxyContainer = "hmd_proxy"

// proxyInspector is the Docker surface checkProxyStarted needs, narrowed so the
// check is testable without a daemon.
type proxyInspector interface {
	Running(ctx context.Context, name string) (bool, error)
	Logs(ctx context.Context, name string, lines int) string
}

// checkProxyStarted refuses when the proxy is not actually up.
//
// `compose up` reports success once it has *started* a container; nginx exiting
// on a bad configuration a moment later is a restart loop, not a start failure,
// so nothing upstream notices. Everything then fails as something else: Floci's
// health check is an nginx stream listener, so a dead proxy is reported as a
// dead Floci, and the control plane is declared ready over a platform that
// answers nothing.
//
// The config is the usual cause and nginx names the line, so its own words are
// the error. A stale route fragment under $HMD_HOME/.cache/nginx written by an
// older nsctl is enough to do it -- one referencing a variable that release no
// longer defines bricked a platform exactly this way.
func checkProxyStarted(ctx context.Context, opts *Options, docker proxyInspector) error {
	if docker == nil {
		return nil
	}
	up, err := docker.Running(ctx, ProxyContainer)
	if err != nil || up {
		// Cannot tell is not a failure: this gates a start, and the same
		// doctrine ImageOf and InspectState state applies.
		return nil
	}
	detail := strings.TrimSpace(docker.Logs(ctx, ProxyContainer, 20))
	if detail == "" {
		detail = "(no output)"
	}
	return nserr.New(nserr.Fail,
		"%s started and is not running. Every host-facing route goes through it, including Floci's, so nothing else can come up.\n%s\n"+
			"  An invalid route fragment is the usual cause. They are regenerated, so moving %s aside and starting again is safe.",
		ProxyContainer, detail, filepath.Join(opts.Home, ".cache", "nginx"))
}
