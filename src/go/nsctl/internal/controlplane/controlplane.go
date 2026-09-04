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
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bundled"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/compose"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/container"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/floci"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/router"
)

// HostsEntries are the names the host must resolve to a loopback address.
//
// `neuronsphere` is baked into the presigned S3 and API URLs Floci hands back
// to host-side consumers, so the host has to resolve it for those URLs to work
// at all.
var HostsEntries = []string{"neuronsphere", "neuronsphere-workload"}

// GUIProfile gates the Deployment GUI container.
const GUIProfile = "deployment-gui"

// DefaultGUIPort is slot 0's spare port.
const DefaultGUIPort = 19003

// MSDeploymentURL is the control-plane route to hmd-ms-deployment.
const MSDeploymentURL = "http://localhost/hmd_ms_deployment"

// Options configures a start or a stop.
type Options struct {
	Home    string
	Lookup  func(string) string
	Verbose bool
	// Upgrade pulls newer images before starting, rather than reusing what is
	// already local.
	Upgrade bool
	Out     io.Writer
	Err     io.Writer
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

// CheckHostsEntries verifies the host resolves the Floci aliases to loopback.
//
// Without them every presigned URL Floci hands back is unusable from the host,
// and the failure surfaces far away as a connection refused inside some
// unrelated tool.
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
	return nserr.New(nserr.Usage,
		"%s must resolve to a loopback address. Add this line to /etc/hosts and try again:\n\n    127.0.0.1 %s",
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
	if err := CheckHostsEntries(nil); err != nil {
		return err
	}
	if err := CheckNoLegacyEnvFlociState(reg); err != nil {
		return err
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

	guiImage := DeploymentGUIImage(ctx, opts, docker.ImagePresent)
	project, err := Project(opts, reg, guiImage)
	if err != nil {
		return err
	}
	active := ActiveProfiles(opts)

	runner, err := compose.NewRunner(opts.Out, opts.Err)
	if err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}

	opts.step("Validating ports...")
	if conflicts := compose.CheckExclusivePublisher(project, active); len(conflicts) > 0 {
		return nserr.New(nserr.Usage,
			"only hmd_proxy may publish host ports in this layout, but:\n  %s", joinConflicts(conflicts))
	}
	for _, c := range compose.CheckReserved(project, active) {
		opts.warn("%s", c)
	}
	ours := runner.OurPorts(ctx, reg.ControlPlane.ComposeProject)
	for _, c := range compose.CheckInUse(ctx, project, active, ours, nil) {
		opts.warn("%s", c)
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

	opts.step("Provisioning control-plane Floci resources...")
	prov, err := floci.NewProvisioner(ctx, target, opts.Out, opts.Err)
	if err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	if err := prov.Provision(ctx, names, floci.ControlPlaneDBAlias); err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}

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
	if err := r.WriteControlPlaneStreams(target.Alias); err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	if err := r.WriteControlPlaneVhosts(GUIEnabled(opts), GUIPort(opts)); err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	if err := r.Reload(ctx, docker.Exec); err != nil {
		// A proxy that did not reload still serves its old routes, which beats
		// aborting the start.
		opts.warn("%v", err)
	}

	opts.step("Control plane ready.")
	opts.step("  services   %s/", MSDeploymentURL)
	opts.step("  floci      %s", target.Endpoint)
	if GUIEnabled(opts) {
		opts.step("  gui        http://localhost:%d", GUIPort(opts))
	}
	return nil
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
	// The GUI image does not matter for a stop, but the service must still be
	// parsed and profile-enabled or its container is left running -- the same
	// reason export_control_plane_compose_env runs before every compose call,
	// not only before `up`.
	project, err := Project(opts, reg, "")
	if err != nil {
		return err
	}
	runner, err := compose.NewRunner(opts.Out, opts.Err)
	if err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}

	opts.step("Stopping control-plane containers...")
	if err := runner.Stop(ctx, project, ActiveProfiles(opts)); err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	// The network is deliberately left in place so stopped containers can
	// restart onto it. Only a purge removes it.
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
	registryHost := opts.lookup("HMD_LOCAL_NS_CONTAINER_REGISTRY")
	if registryHost == "" {
		registryHost = "ghcr.io/neuronsphere"
	}
	version := opts.lookup("HMD_PROJECTBUILDER_VERSION")
	if version == "" {
		version = "stable"
	}
	return registryHost + "/hmd-img-projectbuilder:" + version
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
	overlay := map[string]string{
		"HMD_HOME":                    opts.Home,
		"NEURONSPHERE_DOCKER_NETWORK": reg.ControlPlane.Network,
		"HMD_LOCAL_GUI_HOST_PORT":     strconv.Itoa(GUIPort(opts)),
	}
	if guiImage != "" {
		overlay["HMD_DEPLOYMENT_GUI_IMAGE"] = guiImage
	}
	return func(key string) string {
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
	return active
}

// GUIEnabled matches bom_seeder.gui_enabled: true unless explicitly falsy.
func GUIEnabled(opts *Options) bool {
	switch strings.ToLower(strings.TrimSpace(opts.lookup("HMD_LOCAL_NEURONSPHERE_ENABLE_GUI"))) {
	case "0", "false", "no":
		return false
	}
	return true
}

// GUIPort is the host port hmd_proxy serves the GUI at.
func GUIPort(opts *Options) int {
	if raw := opts.lookup("HMD_LOCAL_GUI_HOST_PORT"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			return n
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
