// Package status assembles a read-only snapshot of a local NeuronSphere.
//
// It reproduces environments.environment_status, with one correction. That
// function inspects env.db_container and env.graph_container by name, but both
// are Docker network *aliases* attached to Floci-spawned containers whose real
// names are opaque -- floci_deployer.floci_container_name says so outright:
// "docker inspect on the DNS alias we later attach does not work, because an
// alias is not an object". So the Python reports those two as not running even
// when they are. Here they are resolved by Floci's label triple instead.
package status

import (
	"context"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/hosturl"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/container"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/tools"
)

// MSDeploymentURL is the control-plane route to hmd-ms-deployment, served by
// hmd_proxy. Matches environments._MS_DEPLOYMENT_URL.
func MSDeploymentURL() string { return hosturl.Route("hmd_ms_deployment") }

// DefaultFlociEndpoint is where hmd_proxy streams the single Floci. The Floci
// container itself publishes nothing.
func DefaultFlociEndpoint() string { return hosturl.Floci() }

// DefaultGUIPort is the Deployment GUI's published port -- slot 0's spare,
// which is why the port stride cannot be widened.
const DefaultGUIPort = 19003

// Identifiers the substrate's Floci resources are recorded under. These are the
// instance and repo-class names bom_seeder uses for the two entries every
// environment gets.
const (
	envDBInstance  = "environment-db"
	envDBRepoClass = "hmd-postgres-rds"
	graphInstance  = "global-graph"
	graphRepoClass = "hmd-inf-neptune"
	// The control plane's own Postgres, from bootstrap_dag. Its deployment id
	// is "cp" and its environment component is the literal "local".
	cpDBInstance    = "control-plane-db"
	cpDBRepoClass   = "hmd-postgres-rds"
	cpDeploymentID  = "cp"
	defaultRegion   = "reg1"
	defaultCustomer = "none"
)

// Container is one container's role, resolved name and state.
type Container struct {
	Role    string
	Name    string
	Running bool
	// Absent distinguishes "there is no such container" from "it is stopped".
	// A lazily-provisioned resource that was never created and one that was
	// stopped by `env stop` need different words.
	Absent bool
}

// Environment is a snapshot of one environment.
type Environment struct {
	Name           string
	AccountID      string
	DeploymentID   string
	LegacyLayout   bool
	Bootstrapped   bool
	K3sCluster     string
	ComposeProject string
	StateDir       string
	// Substrate is the environment's recorded mode (NERD014); the rows and
	// routes below name only what it runs.
	Substrate  manifest.Substrate
	Routes     map[string]string
	RouteOrder []string
	Containers []Container
}

// Running reports whether every container this environment owns is up.
//
// The shared Floci is listed for information and not counted: it is the
// control plane's, and an environment whose mode runs nothing of its own is
// stopped as far as `control-plane stop` is concerned, not running because
// the control plane is.
func (e Environment) Running() bool {
	owned := 0
	for _, c := range e.Containers {
		if c.Role == "floci" {
			continue
		}
		owned++
		if !c.Running {
			return false
		}
	}
	return owned > 0
}

// Extension is a declared control-plane extension (NERD004 SPEC012).
//
// Version and Source travel together deliberately: "0.1.4 from a working tree"
// and "0.1.4 from a bundled tree" are different facts, and the difference
// decides what a reader does next.
type Extension struct {
	Instance  string
	RepoClass string
	Version   string
	Source    string
	URL       string
	// Problem is why this extension did not resolve, empty when it did. A
	// declaration that failed still appears: dropping it would lose the only
	// place its name is shown, and "declared, and here is why it is not
	// running" is the report a reader needs.
	Problem string
	// Containers is how many this extension contributes, Running how many are
	// up. Both zero for one that did not resolve.
	Containers int
	Running    int
}

// HandbackVar is one variable an extension contributes to hmd.env (SPEC010).
//
// State is "managed" when the block sets it, "yours" when a value set outside
// the block won instead. The second is the fact worth surfacing: a hand-set
// value is otherwise an invisible reason for a local index not being used.
type HandbackVar struct {
	Name  string
	Owner string
	Merge string
	State string
}

// ExtensionQuery is one extension to snapshot, as plain data.
//
// Plain data rather than the resolved type, so this package stays free of
// internal/cpext: status describes what is running and has no business
// resolving repo classes.
type ExtensionQuery struct {
	Instance   string
	RepoClass  string
	Version    string
	Source     string
	URL        string
	Problem    string
	Containers []string
}

// ControlPlane is a snapshot of the shared half.
type ControlPlane struct {
	// Handback is what the managed block of hmd.env carries, and HandbackPath
	// the file it is in.
	Handback       []HandbackVar
	HandbackPath   string
	Bootstrapped   bool
	ComposeProject string
	Network        string
	NetworkExists  bool
	FlociDataDir   string
	MSDeploymentUp bool
	Containers     []Container
	Extensions     []Extension
	RunningEnvs    []string
	StoppedEnvs    []string
}

// Docker is the subset of container.Docker this package uses. Narrowed so the
// snapshot logic is testable without a daemon.
type Docker interface {
	Running(ctx context.Context, name string) (bool, error)
	ContainerNames(ctx context.Context) map[string]bool
	FlociContainer(ctx context.Context, service, accountID, resourceID string) (string, bool)
	NetworkExists(ctx context.Context, name string) bool
}

// Prober reports whether a control-plane service answers.
type Prober func(ctx context.Context, url string) bool

// httpProbeAttempts and httpProbeBackoff bound the retry a single probe call
// makes. Floci evicts an idle Lambda container on its own timer, independent
// of request timing, and cold-starts a fresh one in under a second on the
// next request -- a probe landing in that gap is a transient miss, not the
// service being down, so one failed attempt must not read as unreachable.
const (
	httpProbeAttempts = 3
	httpProbeBackoff  = 300 * time.Millisecond
)

// HTTPProber probes a URL, treating any non-5xx as reachable. Retries a few
// times with a short backoff before giving up -- see httpProbeAttempts.
//
// A 4xx counts: hmd-ms-base only registers /api/... and /apiop/... routes, so a
// 404 from the root still confirms the proxy -> API Gateway -> Lambda chain is
// wired. Matches hmd_cli_neuronsphere._wait_for_service.
func HTTPProber(timeout time.Duration) Prober {
	client := &http.Client{Timeout: timeout}
	return func(ctx context.Context, url string) bool {
		for attempt := 1; attempt <= httpProbeAttempts; attempt++ {
			if probeOnce(ctx, client, url) {
				return true
			}
			if attempt == httpProbeAttempts {
				return false
			}
			select {
			case <-ctx.Done():
				return false
			case <-time.After(httpProbeBackoff):
			}
		}
		return false
	}
}

func probeOnce(ctx context.Context, client *http.Client, url string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode < 500
}

// Reporter builds snapshots.
type Reporter struct {
	Docker Docker
	Probe  Prober
	Lookup registry.Lookup
	// Substrate answers an environment's recorded mode by slug; nil means
	// full for every environment. Injected rather than read here so the
	// package stays free of HMD_HOME's layout.
	Substrate func(slug string) manifest.Substrate
	// Routed answers whether an environment routes a host port, from the
	// router's own record. Injected for the same reason as Substrate.
	//
	// nil means nothing is routed, which is the opposite of Substrate's
	// permissive default and deliberately so: this exists to stop reporting an
	// endpoint the environment merely reserved, and a nil that fell back to
	// "yes" would put the defect straight back (NERD023 SPEC003).
	Routed func(slug string, port int) bool
	// RoutedService answers whether an environment routes one HTTP service,
	// from the router's own record and for the same reason as Routed: the
	// substrate mode no longer decides whether the database-account service is
	// deployed, so reporting its route from the mode advertises an endpoint
	// nothing answers (NERD024 SPEC001).
	RoutedService func(slug, service string) bool
	// ControlPlane is the shared half's recorded state, for the host ports this
	// home actually publishes. The zero value answers the historical defaults
	// through Port(), so a caller that has no registry behaves as before.
	//
	// Needed because a published port is now *chosen* around whatever else is on
	// the machine (NERD025 SPEC008): reporting the GUI from the constant printed
	// a link to a port nothing answers on any home where 19003 had moved.
	ControlPlane registry.ControlPlane
}

func (r *Reporter) routed(slug string, port int) bool {
	if r.Routed == nil {
		return false
	}
	return r.Routed(slug, port)
}

func (r *Reporter) routedService(slug, service string) bool {
	if r.RoutedService == nil {
		return false
	}
	return r.RoutedService(slug, service)
}

func (r *Reporter) substrate(slug string) manifest.Substrate {
	if r.Substrate == nil {
		return manifest.SubstrateFull
	}
	return r.Substrate(slug)
}

func (r *Reporter) lookup(key string) string {
	if r.Lookup == nil {
		return ""
	}
	return r.Lookup(key)
}

// EnvironmentStatus snapshots one environment.
func (r *Reporter) EnvironmentStatus(ctx context.Context, e *registry.Environment) Environment {
	mode := r.substrate(e.Slug)
	if e.LegacyLayout {
		mode = manifest.SubstrateFull
	}
	hasDB := mode != manifest.SubstrateNone
	hasCluster := mode == manifest.SubstrateFull
	out := Environment{
		Name:           e.Slug,
		AccountID:      e.AccountID,
		DeploymentID:   e.DeploymentID,
		LegacyLayout:   e.LegacyLayout,
		Bootstrapped:   e.Bootstrapped(),
		K3sCluster:     e.K3sCluster,
		ComposeProject: e.ComposeProject,
		StateDir:       e.StateDir,
		Substrate:      mode,
		Routes:         map[string]string{},
	}

	flociEndpoint := firstNonEmpty(r.lookup("FLOCI_ENDPOINT"), r.lookup("MINISTACK_ENDPOINT"), DefaultFlociEndpoint())
	out.addRoute("services", hosturl.Route(e.Slug)+"/<service>/")
	out.addRoute("floci", flociEndpoint)
	// dbaccount only when it is actually routed, which is the Trino rule below
	// applied for the same reason: an environment that declares no
	// hmd-database-account consumer runs no dbaccount service, and a row for
	// one is a URL that answers nothing.
	if hasDB && r.routedService(e.Slug, "hmd_ms_dbaccount") {
		out.addRoute("dbaccount", hosturl.Route(e.Slug+"/hmd_ms_dbaccount")+"/")
	}
	if hasCluster {
		// Trino only when it is actually routed. The port belongs to the
		// environment's slot whether or not anything listens on it, and
		// reporting it from the slot alone advertised a Trino to every
		// full-substrate environment that had never deployed one.
		if r.routed(e.Slug, e.TrinoPort()) {
			out.addRoute("trino", "localhost:"+strconv.Itoa(e.TrinoPort()))
		}
		out.addRoute("k3s", "localhost:"+strconv.Itoa(e.K3sPort()))
	}
	if r.guiEnabled() {
		gui := "http://localhost:" + strconv.Itoa(r.guiPort())
		out.addRoute("deployment_gui", gui)
		// The key itself is unrecoverable -- only its hash is stored -- so this
		// surfaces the endpoint alone.
		out.addRoute("deployment_gui_mcp", gui+"/mcp/")
	}

	if r.Docker == nil {
		return out
	}
	existing := r.Docker.ContainerNames(ctx)

	region := firstNonEmpty(r.lookup("HMD_REGION"), defaultRegion)
	customer := firstNonEmpty(r.lookup("HMD_CUSTOMER_CODE"), defaultCustomer)

	// The one Floci serves every environment's account.
	out.Containers = append(out.Containers, r.containerStatus(ctx, "floci", registry.FlociContainer))

	// Only what the mode runs (NERD014 SPEC007): a row saying "absent" for
	// something this environment never starts is not information.
	if hasDB {
		dbID := tools.ResourceIdentifier(envDBInstance, envDBRepoClass, e.DeploymentID, e.Slug, region, customer)
		out.Containers = append(out.Containers, r.flociResourceStatus(ctx, "db", "rds", e.AccountID, dbID))

		// The graph's environment component is "local" for every environment, not
		// the slug: graph_cluster_identifier passes a literal there.
		graphID := tools.ResourceIdentifier(graphInstance, graphRepoClass, e.DeploymentID, "local", region, customer)
		out.Containers = append(out.Containers, r.flociResourceStatus(ctx, "graph", "neptune", e.AccountID, graphID))
	}
	if hasCluster {
		k3s := container.K3sContainerName(e.K3sCluster, e.AccountID, existing)
		out.Containers = append(out.Containers, r.containerStatus(ctx, "k3s", k3s))
		// The environment's own router, which is what publishes its Trino and
		// k3s ports. Reported for the reason every other row is: a port that
		// does not answer has no obvious cause otherwise (NERD027 SPEC002).
		if name := e.Router(); name != "" {
			out.Containers = append(out.Containers, r.containerStatus(ctx, "router", name))
		}
	}

	return out
}

// ControlPlaneStatus snapshots the shared half, including which environments
// are up -- what `control-plane stop` refuses on.
func (r *Reporter) ControlPlaneStatus(ctx context.Context, reg *registry.Registry) ControlPlane {
	out := ControlPlane{
		Bootstrapped:   reg.ControlPlane.Bootstrapped,
		ComposeProject: reg.ControlPlane.ComposeProject,
		Network:        reg.ControlPlane.Network,
		FlociDataDir:   reg.ControlPlane.FlociDataDir,
	}

	if r.Docker != nil {
		out.NetworkExists = r.Docker.NetworkExists(ctx, reg.ControlPlane.Network)
		out.Containers = []Container{
			r.containerStatus(ctx, "proxy", "hmd_proxy"),
			r.containerStatus(ctx, "floci", registry.FlociContainer),
		}
		if r.guiEnabled() {
			out.Containers = append(out.Containers, r.containerStatus(ctx, "deployment_gui", "hmd_deployment_gui"))
		}
		region := firstNonEmpty(r.lookup("HMD_REGION"), defaultRegion)
		customer := firstNonEmpty(r.lookup("HMD_CUSTOMER_CODE"), defaultCustomer)
		cpDB := tools.ResourceIdentifier(cpDBInstance, cpDBRepoClass, cpDeploymentID, "local", region, customer)
		out.Containers = append(out.Containers,
			r.flociResourceStatus(ctx, "db", "rds", registry.ControlPlaneAccountID, cpDB))
	}

	if r.Probe != nil {
		out.MSDeploymentUp = r.Probe(ctx, MSDeploymentURL()+"/")
	}

	for _, name := range reg.Names() {
		e := reg.Environments[name]
		if r.EnvironmentStatus(ctx, &e).Running() {
			out.RunningEnvs = append(out.RunningEnvs, name)
		} else {
			out.StoppedEnvs = append(out.StoppedEnvs, name)
		}
	}
	return out
}

// ExtensionStatus counts each declared extension's containers.
func (r *Reporter) ExtensionStatus(ctx context.Context, queries []ExtensionQuery) []Extension {
	out := make([]Extension, 0, len(queries))
	for _, q := range queries {
		e := Extension{
			Instance: q.Instance, RepoClass: q.RepoClass, Version: q.Version,
			Source: q.Source, URL: q.URL, Problem: q.Problem,
			Containers: len(q.Containers),
		}
		for _, name := range q.Containers {
			if r.Docker == nil {
				break
			}
			if running, _ := r.Docker.Running(ctx, name); running {
				e.Running++
			}
		}
		out = append(out, e)
	}
	return out
}

func (r *Reporter) containerStatus(ctx context.Context, role, name string) Container {
	running := false
	if r.Docker != nil && name != "" {
		running, _ = r.Docker.Running(ctx, name)
	}
	return Container{Role: role, Name: name, Running: running}
}

// flociResourceStatus resolves a Floci-managed container by its label triple.
//
// An empty name means Floci has no container for that resource at all, which is
// a normal state rather than a fault -- a default environment provisions no
// graph, since the Neptune cluster is lazy. That is reported distinctly from a
// container that exists and is merely stopped.
func (r *Reporter) flociResourceStatus(ctx context.Context, role, service, accountID, resourceID string) Container {
	name, running := r.Docker.FlociContainer(ctx, service, accountID, resourceID)
	if name == "" {
		return Container{Role: role, Name: "(not provisioned)", Running: false, Absent: true}
	}
	return Container{Role: role, Name: name, Running: running}
}

func (r *Reporter) guiEnabled() bool {
	return !isFalsy(r.lookup("HMD_LOCAL_NEURONSPHERE_ENABLE_GUI"))
}

// guiPort falls back to the default on an unparsable value rather than
// failing, matching bom_seeder.gui_port -- a typo in one environment variable
// should not stop status reporting.
func (r *Reporter) guiPort() int {
	if raw := r.lookup(registry.GUIPortEnv); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			return n
		}
	}
	if p := r.ControlPlane.Port(registry.PortGUI); p > 0 {
		return p
	}
	return DefaultGUIPort
}

func (e *Environment) addRoute(key, value string) {
	e.Routes[key] = value
	e.RouteOrder = append(e.RouteOrder, key)
}

func isFalsy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "false", "no":
		return true
	}
	return false
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
