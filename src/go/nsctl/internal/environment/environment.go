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

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/authd"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bom"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/container"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/floci"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/k3s"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
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
	// ForceRedeploy deploys every declared instance whether or not the graph
	// already has it, bypassing drift detection entirely. The escape hatch for
	// a digest that disagrees with reality.
	ForceRedeploy bool
	Verbose       bool
	Out           io.Writer
	Err           io.Writer

	// redeployInstances names instances the deploy must run again even though
	// the graph calls them DEPLOYED. Clearing a cluster record out from under
	// the plan is invisible to it -- the graph still says the instance is
	// deployed and the snapshot still matches -- so without this the reconcile
	// would delete a cluster nothing then rebuilds.
	redeployInstances map[string]bool
}

// markForRedeploy forces one instance into the next plan.
func (o *Options) markForRedeploy(instance string) {
	if o.redeployInstances == nil {
		o.redeployInstances = map[string]bool{}
	}
	o.redeployInstances[instance] = true
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

	// How much substrate this environment runs (NERD014). Settled before the
	// first step so a binding the mode cannot satisfy is refused here, not by
	// the changeset ten minutes on.
	mode := substrateMode(opts, env.Slug)
	if env.LegacyLayout && mode != manifest.SubstrateFull {
		// Its database and graph are the control plane's own; there is
		// nothing to skip.
		opts.warn("substrate %s is ignored for the legacy layout; running the full substrate", mode)
		mode = manifest.SubstrateFull
	}
	plan := planFor(mode)
	if mode != manifest.SubstrateFull {
		opts.step("Substrate: %s", mode)
	}
	if declared, err := manifest.Load(opts.Home, env.Slug, opts.Lookup); err == nil {
		if err := refuseCoreBindings(mode, declared); err != nil {
			return err
		}
	}

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
	var dbContainer, graphContainer string
	dbID := floci.EnvDBIdentifier(names)
	if plan.Database {
		opts.step("Starting the environment database...")
		dbContainer, err = floci.EnsureRDSRunning(ctx, d, env.AccountID, dbID,
			env.DBContainer, reg.ControlPlane.Network, 2*time.Minute, 0)
	}
	switch {
	case !plan.Database:
	case err != nil:
		opts.warn("%v", err)
	case dbContainer == "":
		// Distinguish an instance Floci could not bring back from one that was
		// never deployed: they need different next moves, and the second
		// reading of the first sends people looking in the wrong place.
		switch status := prov.DBInstanceStatus(ctx, dbID); status {
		case "":
			// No instance record means no database, whatever the deployment
			// graph says -- the same reasoning K3sMissing applies to the
			// cluster below, and it has to be applied here for the same reason.
			//
			// The graph lives in the control plane's Postgres and outlives an
			// `env purge`, which destroys the environment's resources and
			// leaves every RepoInstanceDeployment saying DEPLOYED. So a purge
			// followed by `env add` and `env start` planned "3 unchanged" and
			// never recreated the database -- two lines under this very
			// warning, which had already said it did not exist.
			opts.markForRedeploy(bom.EnvDBInstance)
			opts.warn("no database for %q has ever been deployed; the deploy creates it", env.Slug)
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
	if plan.Graph {
		graphID := floci.GraphIdentifier(names)
		graphContainer, err = floci.EnsureNeptuneRunning(ctx, d, env.AccountID, graphID,
			env.GraphContainer, reg.ControlPlane.Network)
		if err != nil {
			opts.warn("%v", err)
		} else if graphContainer != "" {
			opts.step("  %s is up and aliased as %s", graphContainer, env.GraphContainer)
		}
	}

	// The k3s cluster, in three steps that each refuse the others' job:
	// the reconcile clears a record whose container Floci cannot serve, the
	// deploy creates one, and EnsureK3sRunning starts what a non-purge stop
	// left behind. None of it under a mode without a cluster (NERD014).
	var (
		res          floci.K3sResult
		cluster      string
		wrapperImage string
		clusterFatal error
	)
	if plan.Cluster {
		res, cluster, wrapperImage, clusterFatal = startK3s(ctx, opts, reg, env, d, r, routerEnv, target, dbContainer, graphContainer)
	}

	// dbaccount, before anything that wants a database can ask for one.
	//
	// It must also come before the DAG routes below: WriteEnvRoutes is the bulk
	// writer and rewrites the fragment wholesale, so running it afterwards
	// would erase every route refreshRoutes had just spliced in. That is the
	// same order the Python uses -- write_env_routes, then
	// refresh_deployed_service_routes.
	if plan.DBAccount {
		if err := EnsureDBAccount(ctx, opts, reg, env, d, r, routerEnv, target, names); err != nil {
			opts.warn("%v", err)
		}
	} else if err := r.WriteEnvRoutes(routerEnv, nil, floci.DefaultStage, nil); err != nil {
		// Rewritten empty rather than left: a dbaccount route from an earlier
		// full run would advertise a service that no longer answers.
		opts.warn("%v", err)
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

	if clusterFatal != nil {
		opts.warn("%v", clusterFatal)
		for _, line := range readySummary(env, mode, clusterFatal) {
			opts.step("%s", line)
		}
		return nserr.Wrap(nserr.Fail, clusterFatal)
	}

	// The substrate deploy, unless the caller asked for infrastructure only.
	// It runs after the infrastructure because every node needs Floci, the
	// cluster and the routes that reach them.
	if !opts.NoDeploy {
		if err := Apply(ctx, opts, name); err != nil {
			return err
		}
		// The cluster may only now exist. A run that started with no container
		// -- a first bootstrap, or one the reconcile just cleared to pick up a
		// new wrapper image -- skipped every step above that needs a live API
		// server, so do them now rather than leave the kubeconfig, CoreDNS
		// records and ingress to a second `nsctl env start` nobody knows to run.
		if plan.Cluster && res.State == floci.K3sMissing {
			after, err := floci.EnsureK3sRunning(ctx, d, floci.K3sStartOptions{
				Cluster:          env.K3sCluster,
				AccountID:        env.AccountID,
				ExpectedImage:    wrapperImage,
				Network:          reg.ControlPlane.Network,
				NormalizeRunning: true,
				Timeout:          3 * time.Minute,
			})
			cluster = after.Container
			if after.Detached {
				opts.step("  took the k3s container off Docker's default bridge so its node IP stays put")
			}
			if after.DetachErr != nil {
				opts.warn("%v", after.DetachErr)
			}
			if err != nil {
				opts.warn("%v", err)
				clusterFatal = err
			} else if after.State == floci.K3sMissing {
				clusterFatal = fmt.Errorf(
					"the deploy did not create a k3s container for cluster %q; nothing will schedule."+
						"\n  Redeploy it with `nsctl env start --force-full-redeploy`", env.K3sCluster)
			} else if after.State == floci.K3sRunning || after.State == floci.K3sStarted {
				opts.step("Provisioning the cluster the deploy created...")
				if err := startCluster(ctx, opts, d, r, routerEnv, env, cluster, reg.ControlPlane.Network, dbContainer, graphContainer); err != nil {
					opts.warn("%v", err)
				}
				clusterFatal = floci.VerifyK3sAlive(ctx, d, cluster)
			}
		}
		// The database may only now exist, and the CoreDNS record and routes
		// were written before it did.
		if err := refreshAfterDeploy(ctx, opts, reg, env, d, r, routerEnv, target, cluster, names, plan); err != nil {
			opts.warn("%v", err)
		}
		if clusterFatal != nil {
			opts.warn("%v", clusterFatal)
			for _, line := range readySummary(env, mode, clusterFatal) {
				opts.step("%s", line)
			}
			return nserr.Wrap(nserr.Fail, clusterFatal)
		}
	}

	for _, line := range readySummary(env, mode, nil) {
		opts.step("%s", line)
	}
	return nil
}

// startK3s is the cluster part of Start: reconcile, ensure, provision, verify.
// It returns what the post-apply recovery needs to finish the job when the
// deploy creates the container this run found missing.
func startK3s(ctx context.Context, opts *Options, reg *registry.Registry, env *registry.Environment,
	d *container.Docker, r *router.Router, routerEnv router.Env, target floci.Target,
	dbContainer, graphContainer string) (res floci.K3sResult, cluster, wrapperImage string, clusterFatal error) {

	wrapperImage = expectedK3sImage(ctx, opts, d)
	if clusters, err := floci.NewClusters(ctx, target); err != nil {
		opts.warn("%v", err)
	} else {
		action, err := floci.ReconcileK3sCluster(ctx, clusters, d, floci.K3sReconcileOptions{
			Cluster:       env.K3sCluster,
			AccountID:     env.AccountID,
			ExpectedImage: wrapperImage,
		})
		switch {
		case err != nil:
			opts.warn("%v", err)
		case action == floci.K3sReconcileCleared:
			opts.markForRedeploy(bom.EKSClusterInstance)
			opts.step("  cleared the stale k3s cluster %q, keeping its datastore; the deploy recreates it", env.K3sCluster)
			if opts.NoDeploy {
				opts.warn("the k3s cluster was cleared, but --no-deploy skips the deploy that recreates it; re-run `nsctl env start` without --no-deploy")
			}
		case action == floci.K3sReconcileUnreadable:
			opts.warn("could not read the image of the k3s container for %q; leaving the cluster alone rather than recreating it", env.K3sCluster)
		}
	}

	var k3sErr error
	res, k3sErr = floci.EnsureK3sRunning(ctx, d, floci.K3sStartOptions{
		Cluster:          env.K3sCluster,
		AccountID:        env.AccountID,
		ExpectedImage:    wrapperImage,
		Network:          reg.ControlPlane.Network,
		NormalizeRunning: true,
		Timeout:          3 * time.Minute,
	})
	cluster = res.Container
	if res.Detached {
		opts.step("  took the k3s container off Docker's default bridge so its node IP stays put")
	}
	if res.DetachErr != nil {
		opts.warn("%v", res.DetachErr)
	}
	if k3sErr != nil {
		opts.warn("%v", k3sErr)
	}
	switch res.State {
	case floci.K3sMissing:
		// No container means no cluster, whatever the graph says. The graph
		// records that the instance is DEPLOYED and the snapshot still matches,
		// so the plan would call it unchanged and skip it forever -- which is
		// how a cleared cluster stays cleared. Force it back into the plan.
		opts.markForRedeploy(bom.EKSClusterInstance)
		opts.warn("no k3s container for cluster %q; the deploy creates it", env.K3sCluster)
	case floci.K3sStale:
		opts.warn("the k3s container for %q runs an unexpected image and the reconcile could not clear it, so nsctl leaves it alone rather than drop its datastore. Use `hmd neuronsphere up` to refresh it.", env.K3sCluster)
	case floci.K3sStarted:
		if res.Restarted {
			opts.step("  restarted the k3s container to apply its network change, keeping the cluster's datastore")
		} else {
			opts.step("  started the k3s container in place, keeping the cluster's datastore")
		}
	case floci.K3sRunning:
		opts.step("  k3s is already running")
	}

	// clusterFatal is the one k3s failure that must not be swallowed. Every
	// step inside startCluster is a best-effort `docker exec` that only warns,
	// which is right individually and wrong in aggregate: a container that dies
	// mid-provision warns a dozen times, fails at nothing, and is then
	// advertised as ready on a port nothing listens on.
	if res.State == floci.K3sRunning || res.State == floci.K3sStarted {
		if k3sErr != nil {
			clusterFatal = k3sErr
		} else {
			if err := startCluster(ctx, opts, d, r, routerEnv, env, cluster, reg.ControlPlane.Network, dbContainer, graphContainer); err != nil {
				opts.warn("%v", err)
			}
			clusterFatal = floci.VerifyK3sAlive(ctx, d, cluster)
		}
	}
	return res, cluster, wrapperImage, clusterFatal
}

// readySummary is the closing report.
//
// The k3s line is omitted when the cluster is dead: a port nothing listens on
// reads as a working cluster, and that is exactly how a dead cluster came to be
// reported as a successful start.
//
// Under a mode without a cluster the trino and k3s lines are omitted for the
// same reason, and the database and dbaccount lines say what core does run
// (NERD014 SPEC007).
func readySummary(env *registry.Environment, mode manifest.Substrate, clusterFatal error) []string {
	plan := planFor(mode)
	head := "Ready."
	if clusterFatal != nil {
		head = "Started, but the k3s cluster is not running. Nothing will schedule."
	}
	lines := []string{
		head,
		fmt.Sprintf("  services   http://localhost/%s/<service>/", env.Slug),
	}
	if plan.Cluster {
		lines = append(lines, fmt.Sprintf("  trino      localhost:%d", env.TrinoPort()))
		if clusterFatal == nil {
			lines = append(lines, fmt.Sprintf("  k3s        localhost:%d", env.K3sPort()))
		}
		return lines
	}
	if plan.Database {
		lines = append(lines,
			fmt.Sprintf("  database   %s:5432", env.DBContainer),
			fmt.Sprintf("  dbaccount  http://localhost/%s/hmd_ms_dbaccount/", env.Slug))
	}
	return append(lines, fmt.Sprintf("  substrate  %s", mode))
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
			AuthHost: authd.Host(opts.lookup),
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
	if changed := ops.NormalizeIngressPaths(ctx); len(changed) > 0 {
		opts.step("  rewrote ALB wildcard paths on %s", strings.Join(changed, ", "))
	}

	// /etc/hosts has no wildcards, so the vhost's *.<slug> server_name cannot
	// help until each name resolves. Unlike the neuronsphere alias, a missing
	// UI hostname breaks nothing else, so this warns rather than refusing.
	hosts := ops.IngressHosts(ctx)
	if missing := unresolvableHosts(hosts); len(missing) > 0 {
		opts.warn("these UI hostnames do not resolve to loopback, so they are unreachable until /etc/hosts has them:\n\n    127.0.0.1 %s\n", strings.Join(missing, " "))
	}
	// A Floci Lambda has no /etc/hosts and no CoreDNS: it reaches an
	// Ingress-served API -- ms-transform submitting to Argo -- only if the
	// hostname is a Docker alias of the proxy, the way the identity provider's
	// issuer already is.
	if len(hosts) > 0 {
		if reconnected, err := d.EnsureNetworkAliases(ctx, router.ProxyContainer, network, hosts); err != nil {
			opts.warn("could not alias the UI hostnames on %s: %v", router.ProxyContainer, err)
		} else if reconnected {
			opts.step("  %s answers for %s on the Docker network", router.ProxyContainer, strings.Join(hosts, ", "))
		}
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

// expectedK3sImage is the wrapper image the k3s container is meant to run.
//
// The explicit override wins; otherwise it is read off the running Floci
// container, which is the only place the effective pin exists -- the compose
// file's FLOCI_SERVICES_EKS_DEFAULT_IMAGE resolves through two nested defaults
// and a registry override. Reconstructing it here instead would disagree with
// what Floci actually got and condemn a healthy cluster; "" when Floci cannot
// be read means "do not judge staleness", for the same reason.
func expectedK3sImage(ctx context.Context, opts *Options, d floci.ContainerEnvReader) string {
	if v := opts.lookup("HMD_LOCAL_K3S_WRAPPER_IMAGE"); v != "" {
		return v
	}
	return floci.K3sWrapperImage(ctx, d)
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
	routerEnv router.Env, target floci.Target, cluster string, names floci.Names, plan startPlan) error {

	var dbContainer, graphContainer string
	var err error
	if plan.Database {
		dbContainer, err = floci.EnsureRDSRunning(ctx, d, env.AccountID, floci.EnvDBIdentifier(names),
			env.DBContainer, reg.ControlPlane.Network, 2*time.Minute, 0)
		if err != nil {
			opts.warn("%v", err)
		} else if dbContainer != "" {
			opts.step("  %s is up and aliased as %s", dbContainer, env.DBContainer)
		}
	}
	if plan.Graph {
		graphContainer, err = floci.EnsureNeptuneRunning(ctx, d, env.AccountID, floci.GraphIdentifier(names),
			env.GraphContainer, reg.ControlPlane.Network)
		if err != nil {
			opts.warn("%v", err)
		}
	}

	if cluster != "" {
		ops := &k3s.Operators{
			Kube:   &k3s.Kube{Cluster: env.K3sCluster, Container: cluster, Kubeconfig: env.Kubeconfig, Run: d.Run},
			Docker: d,
			Env: k3s.Environment{
				Slug: env.Slug, DBContainer: env.DBContainer, GraphContainer: env.GraphContainer,
				CoreInstanceName: env.CoreInstanceName,
				K3sCluster:       env.K3sCluster, K3sContainer: cluster,
				AuthHost: authd.Host(opts.lookup),
			},
			Network: reg.ControlPlane.Network, IngressEnabled: ingressEnabled(opts),
			Out: opts.Out, Err: opts.Err,
		}
		if err := ops.EnsureCoreDNSRecordsFor(ctx, dbContainer, graphContainer); err != nil {
			opts.warn("%v", err)
		}
		// A deploy may have added an Ingress (Airflow, Argo, a UI). The
		// proxy's hostname aliases are what let a Floci Lambda reach it (see
		// startCluster), and they were computed before this deploy ran.
		if hosts := ops.IngressHosts(ctx); len(hosts) > 0 {
			if reconnected, err := d.EnsureNetworkAliases(ctx, router.ProxyContainer, reg.ControlPlane.Network, hosts); err != nil {
				opts.warn("could not alias the UI hostnames on %s: %v", router.ProxyContainer, err)
			} else if reconnected {
				opts.step("  %s answers for %s on the Docker network", router.ProxyContainer, strings.Join(hosts, ", "))
			}
		}
	}

	if err := refreshRoutes(ctx, opts, r, routerEnv, target); err != nil {
		opts.warn("%v", err)
	}
	return r.Reload(ctx, d.Exec)
}

// DeployableSlug is the only environment name whose CDKTF nodes can deploy
// today, and the reason is not in this repository.
//
// ms-deployment's deploy_base.deploy_node passes the *environment's own name*
// as `hmd deploy --environment`, because a local Environment entity is typed by
// its slug -- that is what makes identical instance names safe across
// environments, and both front ends seed it that way on purpose
// (deploymentSetDefinition, ensure_environment). hmd-lib-cdktf then reads that
// same value as the AWS deployment *tier* and decides S3 addressing from it:
//
//	s3_use_path_style=True if self.environment == "local" else None
//	use_path_style=True    if self.environment == "local" else None
//
// So an environment named anything else gets virtual-host S3 addressing against
// Floci, which publishes no per-bucket DNS, and its very first CDKTF node dies
// in `tofu init`:
//
//	Failed to get existing workspaces: ... Get
//	"http://hmd.000000000001.reg1.tfstate.neuronsphere:4566/?list-type=2...":
//	dial tcp: lookup hmd.000000000001.reg1.tfstate.neuronsphere: no such host
//
// One name overloaded for two jobs. The fix belongs in hmd-lib-cdktf, whose own
// comment three lines above already says "The endpoint itself comes from
// AWS_ENDPOINT_URL in the environment" -- so the predicate should be "the
// endpoint is a local Floci", not "the tier is literally called local".
//
// Fixed in hmd-lib-cdktf on 2026-09-08: is_local_environment() asks whether
// AWS_ENDPOINT_URL is set rather than what the environment is called, and
// HmdCdkTfStack.is_local carries the answer to all seventeen sites that used
// to compare the name.
//
// The warning stays anyway, because the fix lives in a library that reaches a
// deploy only through the projectbuilder image. Until one ships carrying it,
// a differently-named environment still fails exactly as described, and a
// removed warning would leave that unexplained. Delete this, its two callers
// and the bullets in docs/nsctl.rst and the README once the image has it.
const DeployableSlug = "local"

// WarnUndeployableSlug names the defect above before it costs a deploy.
//
// A warning rather than a refusal. Nothing here is wrong -- registering,
// starting, stopping and purging a differently-named environment all work, the
// substrate's cluster and database come up, and the day hmd-lib-cdktf stops
// comparing the tier to a literal the name works unchanged. Refusing would
// forbid a state that is only broken elsewhere.
//
// Named at both ends: `env add`, so the choice is informed, and the start, so
// the failure 955 log lines into `tofu init` has something attached to it.
func WarnUndeployableSlug(warn func(string, ...any), slug string) {
	if slug == "" || slug == DeployableSlug {
		return
	}
	warn("environment %q is not named %q. Its CDKTF nodes will fail in `tofu init` with "+
		"\"no such host\" for the tfstate bucket unless the projectbuilder image carries "+
		"hmd-lib-cdktf's is_local_environment fix (2026-09-08).\n"+
		"  Before it, hmd-lib-cdktf decided S3 path-style addressing by comparing the environment's "+
		"name, while ms-deployment passes the environment's own slug as --environment.\n"+
		"  The substrate's cluster and database come up either way; only deploys are affected. "+
		"See SPEC014.", slug, DeployableSlug)
}
