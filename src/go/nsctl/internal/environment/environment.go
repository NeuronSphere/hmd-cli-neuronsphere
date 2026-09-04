// Package environment starts and stops one named local environment.
//
// An environment is a self-contained emulated AWS account inside the shared
// Floci, with its own k3s cluster, Postgres and dbaccount. nsctl ships that
// substrate and nothing above it: every workload is a RepoClass a user adds.
package environment

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/container"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/floci"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/k3s"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/router"
)

// Options configures a start or stop.
type Options struct {
	Home   string
	Lookup func(string) string
	// NoDeploy skips the BOM reconcile, bringing up only the infrastructure.
	// Until the deploy path lands it is the only supported mode.
	NoDeploy bool
	Verbose  bool
	Out      io.Writer
	Err      io.Writer
}

func (o *Options) lookup(key string) string {
	if o.Lookup == nil {
		return ""
	}
	return o.Lookup(key)
}

func (o *Options) step(format string, a ...any) {
	if o.Out != nil {
		fmt.Fprintf(o.Out, format+"\n", a...)
	}
}

func (o *Options) warn(format string, a ...any) {
	if o.Err != nil {
		fmt.Fprintf(o.Err, "warning: "+format+"\n", a...)
	}
}

// Start brings one environment's infrastructure up.
//
// The order is load-bearing in two places. The k3s host route is wired before
// the kubeconfig is written, because the kubeconfig points at that stream port
// and every later kubectl goes through it. And the database alias is attached
// before the routes are refreshed, because a service whose database does not
// resolve comes back up answering 500 on a route that looks correctly wired.
func Start(ctx context.Context, opts *Options, name string) error {
	reg, err := registry.Load(opts.Home, opts.Lookup)
	if err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	env, err := reg.Environment(name, opts.Lookup)
	if err != nil {
		return nserr.Wrap(nserr.Usage, err)
	}
	opts.step("Environment: %s", env.Slug)

	d := container.New()
	d.Timeout = 2 * time.Minute
	target := floci.ForAccount(opts.Lookup, env.AccountID, env.LegacyLayout)
	r := router.New(opts.Home, opts.Lookup)
	names := floci.NamesFrom(opts.Lookup, env.DeploymentID, env.Slug)

	routerEnv := router.Env{
		Slug: env.Slug, AccountID: env.AccountID,
		TrinoPort: env.TrinoPort(), K3sPort: env.K3sPort(), SparePort: env.SparePort(),
		IsDefault: env.IsDefault(),
	}

	for _, dir := range env.StateDirs() {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nserr.Wrap(nserr.Fail, fmt.Errorf("creating %s: %w", dir, err))
		}
	}

	// The environment's Floci state lives inside the shared Floci's data dir,
	// namespaced by account, so the ghosts to prune are the control plane's.
	if pruned := floci.PruneAPIGatewayGhosts(reg.ControlPlane.FlociDataDir); pruned > 0 {
		opts.step("  pruned %d unusable API Gateway record(s)", pruned)
	}

	opts.step("Waiting for Floci (account %s)...", env.AccountID)
	if err := target.WaitForHealth(ctx, 5*time.Minute, nil); err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}

	opts.step("Provisioning environment Floci resources...")
	prov, err := floci.NewProvisioner(ctx, target, opts.Out, opts.Err)
	if err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	if err := prov.Provision(ctx, names, env.DBContainer); err != nil {
		opts.warn("%v", err)
	}

	// The database, before anything that needs it resolves. Floci leaves the
	// container stopped on shutdown and nothing restarts it.
	opts.step("Starting the environment database...")
	dbID := floci.EnvDBIdentifier(names)
	dbContainer, err := floci.EnsureRDSRunning(ctx, d, env.AccountID, dbID,
		env.DBContainer, reg.ControlPlane.Network, 2*time.Minute, 0)
	switch {
	case err != nil:
		opts.warn("%v", err)
	case dbContainer == "":
		// Distinguish an instance Floci could not bring back from one that was
		// never deployed: they need different next moves, and the second
		// reading of the first sends people looking in the wrong place.
		switch status := prov.DBInstanceStatus(ctx, dbID); status {
		case "":
			opts.warn("no database for %q has ever been deployed; every service addressing it will fail to connect", env.Slug)
		case "available":
			opts.warn("Floci reports the database for %q available but spawned no container for it", env.Slug)
		default:
			opts.warn("the database for %q is in state %q and has no container -- Floci could not bring it back.\n  Redeploy it with `hmd neuronsphere up --env %s`; until then every service addressing the database answers 500.", env.Slug, status, env.Slug)
		}
	default:
		opts.step("  %s is up and aliased as %s", dbContainer, env.DBContainer)
	}

	// The graph. Floci stops its Neptune container on shutdown and never
	// restarts it, while the cluster record keeps reporting available forever --
	// so waiting on the status alone waits out the whole timeout on the single
	// most common state after a stop.
	graphID := floci.GraphIdentifier(names)
	graphContainer, err := floci.EnsureNeptuneRunning(ctx, d, env.AccountID, graphID,
		env.GraphContainer, reg.ControlPlane.Network)
	if err != nil {
		opts.warn("%v", err)
	} else if graphContainer != "" {
		opts.step("  %s is up and aliased as %s", graphContainer, env.GraphContainer)
	}

	// The k3s cluster. Creating or recreating one is the DAG's job; this only
	// starts what a non-purge stop left behind.
	cluster, state, err := floci.EnsureK3sRunning(ctx, d, env.K3sCluster, env.AccountID,
		expectedK3sImage(opts), 3*time.Minute, 0)
	if err != nil {
		opts.warn("%v", err)
	}
	switch state {
	case floci.K3sMissing:
		opts.warn("no k3s container for cluster %q; it is created by the eks-cluster deploy, which nsctl cannot run yet", env.K3sCluster)
	case floci.K3sStale:
		opts.warn("the k3s container for %q runs an unexpected image; recreating it would drop the cluster's datastore, so nsctl leaves it alone. Use `hmd neuronsphere up` to refresh it.", env.K3sCluster)
	case floci.K3sStarted:
		opts.step("  started the k3s container in place, keeping the cluster's datastore")
	case floci.K3sRunning:
		opts.step("  k3s is already running")
	}

	if state == floci.K3sRunning || state == floci.K3sStarted {
		if err := startCluster(ctx, opts, d, r, routerEnv, env, cluster, reg.ControlPlane.Network, dbContainer, graphContainer); err != nil {
			opts.warn("%v", err)
		}
	}

	// The DAG-deployed services, discovered from Floci rather than from
	// anything this run did: the fragment is rewritten wholesale on every
	// start, so an environment that deploys nothing still needs its routes put
	// back.
	if err := refreshRoutes(ctx, opts, r, routerEnv, target); err != nil {
		opts.warn("%v", err)
	}
	if err := r.Reload(ctx, d.Exec); err != nil {
		opts.warn("%v", err)
	}

	// The substrate deploy, unless the caller asked for infrastructure only.
	// It runs after the infrastructure because every node needs Floci, the
	// cluster and the routes that reach them.
	if !opts.NoDeploy {
		if err := Apply(ctx, opts, name); err != nil {
			return err
		}
		// The database may only now exist, and the CoreDNS record and routes
		// were written before it did.
		if err := refreshAfterDeploy(ctx, opts, reg, env, d, r, routerEnv, target, cluster, names); err != nil {
			opts.warn("%v", err)
		}
	}

	opts.step("Ready.")
	opts.step("  services   http://localhost/%s/<service>/", env.Slug)
	opts.step("  trino      localhost:%d", env.TrinoPort())
	opts.step("  k3s        localhost:%d", env.K3sPort())
	return nil
}

// startCluster does the work that needs a live cluster: the host routes, the
// kubeconfig, the operators and the ingress upstream.
func startCluster(ctx context.Context, opts *Options, d *container.Docker, r *router.Router,
	routerEnv router.Env, env *registry.Environment, cluster, network, dbContainer, graphContainer string) error {

	clusterIP := d.ContainerIP(ctx, cluster, network)
	if clusterIP == "" {
		return fmt.Errorf("could not resolve the k3s container's IP; no host route can be wired")
	}

	// Before the kubeconfig: it points its server at this stream port, and
	// every later kubectl goes through it.
	k3sUpstream := k3s.NodePortAddress(clusterIP, router.K3sAPIPort)
	if err := r.WriteEnvStreams(routerEnv, router.EnvStreamEntries(routerEnv, "", k3sUpstream)); err != nil {
		return err
	}
	if err := r.Reload(ctx, d.Exec); err != nil {
		opts.warn("%v", err)
	}
	opts.step("  k3s API served on host :%d", env.K3sPort())

	if err := floci.WriteKubeconfig(ctx, d, cluster, env.Kubeconfig, env.K3sPort()); err != nil {
		opts.warn("%v", err)
	} else if env.IsDefault() {
		// The historical shared path, so host-side kubectl and the robot suites
		// keep working unchanged.
		legacy := legacyKubeconfigPath(opts.Home)
		if err := copyFile(env.Kubeconfig, legacy); err != nil {
			opts.warn("could not write the shared kubeconfig at %s: %v", legacy, err)
		}
	}

	ops := &k3s.Operators{
		Kube:   &k3s.Kube{Cluster: env.K3sCluster, Container: cluster, Kubeconfig: env.Kubeconfig, Run: d.Run},
		Docker: d,
		Env: k3s.Environment{
			Slug: env.Slug, DBContainer: env.DBContainer, GraphContainer: env.GraphContainer,
			CoreInstanceName: env.CoreInstanceName,
			K3sCluster:       env.K3sCluster, K3sContainer: cluster,
		},
		Network: network, IngressEnabled: ingressEnabled(opts), IngressClass: opts.lookup("HMD_LOCAL_INGRESS_CLASS"),
		Out: opts.Out, Err: opts.Err,
	}

	opts.step("Provisioning the cluster...")
	if err := ops.PrepareNode(ctx); err != nil {
		opts.warn("%v", err)
	}
	if err := ops.EnsureCoreDNSRecordsFor(ctx, dbContainer, graphContainer); err != nil {
		opts.warn("%v", err)
	}
	if err := ops.EnsureIngressController(ctx); err != nil {
		opts.warn("%v", err)
	}

	// Trino, if it is deployed. Both upstreams go into every rewrite, because a
	// writer that knew only one would drop the other's listener.
	trinoUpstream := ""
	if coord, ok := ops.FindTrinoCoordinator(ctx); ok {
		spec := k3s.NodePortSpec{
			Name: router.TrinoNodePortService, Namespace: coord.Namespace,
			Selector: coord.Selector, Port: coord.Port, TargetPort: coord.TargetPort,
			NodePort: router.TrinoNodePort,
		}
		if err := ops.EnsureNodePort(ctx, spec); err != nil {
			opts.warn("%v", err)
		} else {
			trinoUpstream = k3s.NodePortAddress(clusterIP, router.TrinoNodePort)
			opts.step("  Trino exposed on host :%d", env.TrinoPort())
		}
	}
	if err := r.WriteEnvStreams(routerEnv, router.EnvStreamEntries(routerEnv, trinoUpstream, k3sUpstream)); err != nil {
		opts.warn("%v", err)
	}

	// The ingress controller, which fronts every UI the charts expose.
	traefik := k3s.TraefikNodePortSpec(router.TraefikNodePortService, router.TraefikNamespace, router.TraefikNodePort)
	if err := ops.EnsureNodePort(ctx, traefik); err != nil {
		opts.warn("%v", err)
	} else {
		upstream := k3s.NodePortAddress(clusterIP, router.TraefikNodePort)
		if err := r.WriteEnvVhosts(routerEnv, upstream, nil); err != nil {
			opts.warn("%v", err)
		} else {
			opts.step("  UIs served at *.%s.%s", env.Slug, router.IngressDomain)
		}
	}

	// /etc/hosts has no wildcards, so the vhost's *.<slug> server_name cannot
	// help until each name resolves. Unlike the neuronsphere alias, a missing
	// UI hostname breaks nothing else, so this warns rather than refusing.
	if missing := unresolvableHosts(ops.IngressHosts(ctx)); len(missing) > 0 {
		opts.warn("these UI hostnames do not resolve to loopback, so they are unreachable until /etc/hosts has them:\n\n    127.0.0.1 %s\n", strings.Join(missing, " "))
	}
	return nil
}

// refreshRoutes routes every DAG-deployed service in the environment.
func refreshRoutes(ctx context.Context, opts *Options, r *router.Router, routerEnv router.Env, target floci.Target) error {
	gateways, err := floci.NewGateways(ctx, target)
	if err != nil {
		return err
	}
	routes, err := gateways.DeployedServiceRoutes(ctx)
	if err != nil {
		return err
	}
	if len(routes) == 0 {
		return nil
	}
	for path, route := range routes {
		if err := r.UpsertServiceRoute(routerEnv, path, route.RestAPIID, route.StageName); err != nil {
			opts.warn("%v", err)
		}
	}
	opts.step("  routed %d deployed service(s) under /%s/", len(routes), routerEnv.Slug)
	return nil
}

// Stop stops an environment: a stop, never a teardown.
//
// The k3s cluster is stopped rather than deleted and the containers are stopped
// rather than removed, so the next start restarts them in place and keeps the
// cluster's datastore -- which is what makes a restart cheap instead of a full
// redeploy. The control plane is deliberately untouched: stopping it would take
// every other environment's emulated AWS with it.
func Stop(ctx context.Context, opts *Options, name string) error {
	reg, err := registry.Load(opts.Home, opts.Lookup)
	if err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	env, err := reg.Environment(name, opts.Lookup)
	if err != nil {
		return nserr.Wrap(nserr.Usage, err)
	}

	opts.step("Stopping environment %q...", env.Slug)
	d := container.New()
	names := floci.NamesFrom(opts.Lookup, env.DeploymentID, env.Slug)
	existing := d.ContainerNames(ctx)

	stop := func(role, container string) {
		if container == "" || !existing[container] {
			return
		}
		if _, _, err := d.Run(ctx, "stop", container); err != nil {
			opts.warn("could not stop the %s container %s: %v", role, container, err)
			return
		}
		opts.step("  stopped %s (%s)", container, role)
	}

	stop("k3s", floci.K3sContainerName(env.K3sCluster, env.AccountID, existing))
	if name, _ := d.FlociContainer(ctx, "rds", env.AccountID, floci.EnvDBIdentifier(names)); name != "" {
		stop("database", name)
	}
	if name, _ := d.FlociContainer(ctx, "neptune", env.AccountID, floci.GraphIdentifier(names)); name != "" {
		stop("graph", name)
	}

	r := router.New(opts.Home, opts.Lookup)
	if err := r.RemoveEnvRoutes(env.Slug); err != nil {
		opts.warn("%v", err)
	}
	if err := r.Reload(ctx, d.Exec); err != nil {
		opts.warn("%v", err)
	}
	opts.step("Stopped. The control plane is still running; stop it with `nsctl control-plane stop`.")
	return nil
}

func expectedK3sImage(opts *Options) string {
	if v := opts.lookup("HMD_LOCAL_K3S_WRAPPER_IMAGE"); v != "" {
		return v
	}
	// Empty means "do not judge staleness": the pin lives in the compose file's
	// FLOCI_SERVICES_EKS_DEFAULT_IMAGE, and guessing a different default here
	// would condemn a healthy cluster.
	return ""
}

func ingressEnabled(opts *Options) bool {
	switch strings.ToLower(strings.TrimSpace(opts.lookup("HMD_LOCAL_NEURONSPHERE_ENABLE_INGRESS"))) {
	case "false", "0", "no":
		return false
	}
	return true
}

func legacyKubeconfigPath(home string) string {
	return home + "/.cache/k3s/kubeconfig"
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dirOf(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o600)
}

func dirOf(path string) string {
	if i := strings.LastIndex(path, "/"); i > 0 {
		return path[:i]
	}
	return "."
}

// unresolvableHosts are the Ingress hostnames that do not resolve to loopback.
func unresolvableHosts(hosts []string) []string {
	var missing []string
	for _, host := range hosts {
		ips, err := net.LookupIP(host)
		if err != nil {
			missing = append(missing, host)
			continue
		}
		loopback := false
		for _, ip := range ips {
			if ip.IsLoopback() {
				loopback = true
			}
		}
		if !loopback {
			missing = append(missing, host)
		}
	}
	return missing
}

// refreshAfterDeploy re-runs the steps whose inputs the deploy may have created.
//
// The database and graph containers are spawned by Floci during the deploy, so
// the CoreDNS records and the environment routes written before it ran do not
// know about them yet. Rewriting is cheap and idempotent; leaving them stale
// means every chart addressing the database by name fails to resolve it.
func refreshAfterDeploy(ctx context.Context, opts *Options, reg *registry.Registry,
	env *registry.Environment, d *container.Docker, r *router.Router,
	routerEnv router.Env, target floci.Target, cluster string, names floci.Names) error {

	dbContainer, err := floci.EnsureRDSRunning(ctx, d, env.AccountID, floci.EnvDBIdentifier(names),
		env.DBContainer, reg.ControlPlane.Network, 2*time.Minute, 0)
	if err != nil {
		opts.warn("%v", err)
	} else if dbContainer != "" {
		opts.step("  %s is up and aliased as %s", dbContainer, env.DBContainer)
	}
	graphContainer, err := floci.EnsureNeptuneRunning(ctx, d, env.AccountID, floci.GraphIdentifier(names),
		env.GraphContainer, reg.ControlPlane.Network)
	if err != nil {
		opts.warn("%v", err)
	}

	if cluster != "" {
		ops := &k3s.Operators{
			Kube:   &k3s.Kube{Cluster: env.K3sCluster, Container: cluster, Kubeconfig: env.Kubeconfig, Run: d.Run},
			Docker: d,
			Env: k3s.Environment{
				Slug: env.Slug, DBContainer: env.DBContainer, GraphContainer: env.GraphContainer,
				CoreInstanceName: env.CoreInstanceName,
				K3sCluster:       env.K3sCluster, K3sContainer: cluster,
			},
			Network: reg.ControlPlane.Network, IngressEnabled: ingressEnabled(opts),
			Out: opts.Out, Err: opts.Err,
		}
		if err := ops.EnsureCoreDNSRecordsFor(ctx, dbContainer, graphContainer); err != nil {
			opts.warn("%v", err)
		}
	}

	if err := refreshRoutes(ctx, opts, r, routerEnv, target); err != nil {
		opts.warn("%v", err)
	}
	return r.Reload(ctx, d.Exec)
}
