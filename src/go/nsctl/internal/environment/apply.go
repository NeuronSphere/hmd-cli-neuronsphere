package environment

import (
	"context"
	"fmt"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bom"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/container"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/floci"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
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
	if v := opts.lookup("HMD_LOCAL_MS_DEPLOYMENT_URL"); v != "" {
		return v
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
	entries := bom.Substrate(bomEnv, k3sEnabled(opts))

	// Floci spawns its database backend from an image reference pinned into its
	// own configuration, and a reference that resolves nowhere fails the
	// instance while the deploy polls indefinitely. Catch it in seconds here.
	for _, problem := range floci.CheckBackendImages(ctx, d, floci.ContainerName) {
		return nserr.New(nserr.Usage, "%s", problem)
	}

	// Clear a database record Floci cannot serve, so the deploy below creates
	// it rather than refreshing to "no changes" against a dead instance.
	if prov, err := floci.NewProvisioner(ctx, floci.ForAccount(opts.Lookup, env.AccountID, env.LegacyLayout), opts.Out, opts.Err); err == nil {
		cleared, err := floci.ReconcileRDSInstance(ctx, prov, d, env.AccountID, floci.EnvDBIdentifier(names))
		if err != nil {
			opts.warn("%v", err)
		} else if cleared {
			opts.step("  cleared the failed database record so it can be recreated")
		}
	}

	resolver := repoclass.New(opts.lookup("HMD_REPO_HOME"), opts.Lookup)
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
	if err := run.Run(ctx, csdNid, nodes); err != nil {
		return nserr.Wrap(nserr.DeployFailed, err)
	}
	opts.step("  deployed %d instance(s)", len(run.Succeeded))
	return nil
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
