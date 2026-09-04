package environment

import (
	"context"
	"fmt"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bom"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/container"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/floci"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/reconcile"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/repoclass"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/runner"
)

// DefaultMSDeploymentURL is the control-plane route to hmd-ms-deployment, from
// the host.
const DefaultMSDeploymentURL = "http://localhost/hmd_ms_deployment"

// msDeploymentURL is where to reach the deployment service.
//
// Overridable so a test can point somewhere it controls: a hardcoded localhost
// would make every unit test depend on whatever is running on this machine.
func msDeploymentURL(opts *Options) string {
	return MSDeploymentURL(opts.Lookup)
}

// MSDeploymentURL is where to reach the deployment service, for a caller that
// has a lookup but no Options.
func MSDeploymentURL(lookup func(string) string) string {
	if lookup != nil {
		if v := lookup("HMD_LOCAL_MS_DEPLOYMENT_URL"); v != "" {
			return v
		}
	}
	return DefaultMSDeploymentURL
}

// Apply seeds and deploys the environment's substrate.
//
// The substrate is what makes an environment an environment: the core instance,
// its Postgres and its k3s cluster. Every workload above that is a RepoClass a
// user adds, and applying those is what `nsctl env apply` will do once the
// environment manifest lands.
func Apply(ctx context.Context, opts *Options, name string) error {
	reg, err := registry.Load(opts.Home, opts.Lookup)
	if err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	env, err := reg.Environment(name, opts.Lookup)
	if err != nil {
		return nserr.Wrap(nserr.Usage, err)
	}

	d := container.New()
	url := msDeploymentURL(opts)
	client := msdeploy.New(url)
	if !client.Reachable(ctx) {
		return nserr.New(nserr.Fail,
			"hmd-ms-deployment is not answering at %s. Start the control plane first with `nsctl control-plane start`.", url)
	}

	names := floci.NamesFrom(opts.Lookup, env.DeploymentID, env.Slug)
	existing := d.ContainerNames(ctx)
	k3sContainer := floci.K3sContainerName(env.K3sCluster, env.AccountID, existing)

	bomEnv := bom.Environment{
		Slug: env.Slug, DeploymentID: env.DeploymentID, AccountID: env.AccountID,
		Region: names.Region, DBContainer: env.DBContainer,
		K3sCluster: env.K3sCluster, K3sContainer: k3sContainer,
	}

	// The cluster entry only makes sense when k3s is enabled: without it the
	// eks-cluster node deploys a cluster nothing will run.
	substrate := bom.Substrate(bomEnv, k3sEnabled(opts))

	// Everything above the substrate is what the manifest declares. An
	// environment with none is not an error -- that is the empty environment,
	// and it gets a cluster and a database with nothing on them.
	declared, err := manifest.Load(opts.Home, env.Slug, opts.Lookup)
	if err != nil {
		return nserr.Wrap(nserr.Usage, err)
	}
	var (
		declaredRepos []manifest.Repo
		repoPaths     map[string]string
	)
	if declared != nil {
		declaredRepos = declared.Repos
		repoPaths = bom.RepoPaths(declaredRepos, opts.Lookup)
		opts.step("Manifest %s declares %d instance(s)", declared.Path, len(declaredRepos))
		if unsupported := declared.Unsupported(); len(unsupported) > 0 {
			// Said rather than ignored: these configure plugin discovery, which
			// nsctl does not implement, and a user who wrote them is entitled
			// to know they changed nothing.
			opts.warn("the manifest sets %s, which nsctl does not implement; those keys had no effect",
				strings.Join(unsupported, " and "))
		}
	}
	entries := append(substrate, bom.Declared(bomEnv, declaredRepos)...)

	// Floci spawns its database backend from an image reference pinned into its
	// own configuration, and a reference that resolves nowhere fails the
	// instance while the deploy polls indefinitely. Catch it in seconds here.
	for _, problem := range floci.CheckBackendImages(ctx, d, floci.ContainerName) {
		return nserr.New(nserr.Usage, "%s", problem)
	}

	if prov, err := floci.NewProvisioner(ctx, floci.ForAccount(opts.Lookup, env.AccountID, env.LegacyLayout), opts.Out, opts.Err); err == nil {
		// Clear a database record Floci cannot serve, so the deploy below
		// creates it rather than refreshing to "no changes" against a dead
		// instance.
		cleared, err := floci.ReconcileRDSInstance(ctx, prov, d, env.AccountID, floci.EnvDBIdentifier(names))
		if err != nil {
			opts.warn("%v", err)
		} else if cleared {
			opts.step("  cleared the failed database record so it can be recreated")
		}

	}

	// What is already deployed and current does not need deploying again. The
	// graph answers the first half; the snapshot of the last apply answers the
	// second, since the graph records that an instance is DEPLOYED but not
	// what it was deployed *from*.
	plan := reconcile.Degraded(entries)
	if opts.ForceRedeploy {
		opts.step("Redeploying everything, as asked")
	} else if status, err := client.InstanceStatus(ctx, env.DeploymentID); err != nil {
		// Fail safe rather than fail closed: an unreadable graph means nothing
		// is known, so everything is deployed rather than silently skipped.
		opts.warn("could not read the deployment graph, so nothing is assumed about what is deployed: %v", err)
	} else {
		plan = reconcile.Compute(entries, status, reconcile.LoadSnapshot(env.StateDir), nil)
		opts.step("Plan: %s", plan.Summary())
		for _, name := range plan.Remove {
			// Reported, never acted on. Removing a line from a manifest is not
			// an instruction to destroy what it deployed.
			opts.warn("%s is deployed but no longer declared; nsctl leaves it running", name)
		}
		if plan.Empty() {
			// Record the digests anyway: an environment deployed before
			// snapshots existed has none, and without one drift is invisible
			// forever.
			if err := reconcile.WriteSnapshot(env.StateDir, entries, nil); err != nil {
				opts.warn("%v", err)
			}
			opts.step("  everything declared is already deployed and current")
			return nil
		}
		entries = plan.Deploy()
	}

	resolver := repoclass.New(opts.lookup("HMD_REPO_HOME"), opts.Lookup)
	for class, path := range repoPaths {
		resolver.Paths[class] = path
	}
	seeder := &bom.Seeder{Client: client, Versions: resolver}

	opts.step("Seeding the substrate changeset...")
	csdNid, nodes, err := seeder.Seed(ctx, bomEnv, entries)
	if err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	opts.step("  %d deployment node(s)", len(nodes))
	if len(nodes) == 0 {
		return nil
	}

	run := &runner.Runner{
		Docker: d,
		Client: client,
		Config: runner.Config{
			Network:              reg.ControlPlane.Network,
			Image:                projectBuilderRef(opts),
			FlociEndpoint:        floci.InternalEndpoint,
			AccountID:            env.AccountID,
			DeploymentServiceURL: "http://hmd_proxy/hmd_ms_deployment",
			LocalProxy:           "http://hmd_proxy/" + env.Slug,
			K3sCluster:           env.K3sCluster,
			Kubeconfig:           env.Kubeconfig,
			RepoHome:             opts.lookup("HMD_REPO_HOME"),
			RepoPaths:            repoPaths,
			DeploymentID:         env.DeploymentID,
			Region:               names.Region,
			CustomerCode:         names.CustomerCode,
			Extra: map[string]string{
				"AWS_REGION": opts.lookup("AWS_REGION"),
			},
		},
		Out: opts.Out, Err: opts.Err,
	}

	opts.step("Running the deployment...")
	err = run.Run(ctx, csdNid, nodes)
	if err != nil && subnetGroupCollision(run.LastFailure) {
		// A one-time migration for an environment provisioned before hmd-vpc
		// owned the network. The CLI created the DB subnet group imperatively,
		// so Terraform has no state for it and its create fails with
		// DBSubnetGroupAlreadyExists.
		//
		// Done on the failure rather than pre-emptively, because there is no
		// cheap way to ask who owns the group: the deployment graph does not
		// record it, and an environment whose base-vpc genuinely owns it must
		// keep it or every apply would recreate it. Reacting to the collision
		// itself is exact -- it only ever fires when the group really is in the
		// way -- and it costs one failed apply the first time.
		//
		// Floci lets an in-use group be deleted, and DescribeDBInstances fails
		// for the instance referencing it until one exists again, so the retry
		// follows immediately.
		prov, provErr := floci.NewProvisioner(ctx, floci.ForAccount(opts.Lookup, env.AccountID, env.LegacyLayout), opts.Out, opts.Err)
		if provErr == nil {
			released, relErr := floci.ReleaseUnmanagedSubnetGroup(ctx, prov, floci.LocalDBSubnetGroup)
			if relErr != nil {
				opts.warn("%v", relErr)
			} else if released {
				opts.step("  released the CLI-created DB subnet group; retrying so base-vpc can own it")
				csdNid, nodes, err = seeder.Seed(ctx, bomEnv, entries)
				if err != nil {
					return nserr.Wrap(nserr.Fail, err)
				}
				err = run.Run(ctx, csdNid, nodes)
			}
		}
	}
	if err != nil {
		return nserr.Wrap(nserr.DeployFailed, err)
	}
	opts.step("  deployed %d instance(s)", len(run.Succeeded))

	// Record only what is known good: everything the run settled, plus what
	// the plan already found deployed and current. Recording a digest for an
	// entry that failed would make the next run read it as up to date.
	if err := reconcile.WriteSnapshot(env.StateDir, settled(plan, run.Succeeded), nil); err != nil {
		opts.warn("%v", err)
	}
	return nil
}

// settled is the entries whose recorded digest is trustworthy after a run.
func settled(plan *reconcile.Plan, succeeded []string) []bom.Entry {
	known := make(map[string]bool, len(succeeded)+len(plan.Unchanged))
	for _, name := range succeeded {
		known[name] = true
	}
	for _, name := range plan.Unchanged {
		known[name] = true
	}
	out := make([]bom.Entry, 0, len(known))
	for _, e := range plan.Desired {
		if known[e.RepoInstanceName] {
			out = append(out, e)
		}
	}
	return out
}

// subnetGroupCollision reports the one failure the migration above repairs.
func subnetGroupCollision(res *runner.Result) bool {
	if res == nil {
		return false
	}
	return strings.Contains(string(res.Stderr), "DBSubnetGroupAlreadyExists") ||
		strings.Contains(string(res.Stdout), "DBSubnetGroupAlreadyExists")
}

func k3sEnabled(opts *Options) bool {
	switch strings.ToLower(strings.TrimSpace(opts.lookup("HMD_LOCAL_NEURONSPHERE_ENABLE_K3S"))) {
	case "false", "0", "no":
		return false
	}
	return true
}

// projectBuilderRef is the image every deploy node runs in.
func projectBuilderRef(opts *Options) string {
	registryHost := opts.lookup("HMD_LOCAL_NS_CONTAINER_REGISTRY")
	if registryHost == "" {
		registryHost = "ghcr.io/neuronsphere"
	}
	version := opts.lookup("HMD_PROJECTBUILDER_VERSION")
	if version == "" {
		version = "stable"
	}
	return fmt.Sprintf("%s/hmd-img-projectbuilder:%s", registryHost, version)
}
