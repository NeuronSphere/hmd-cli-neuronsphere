package environment

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/artifact"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/authd"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bom"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/container"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/floci"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/k3s"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/librarian"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/reconcile"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/repoclass"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/router"
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

// connectMSDeployment resolves the deployment service's route and refuses
// early if nothing answers there, so a caller fails in a sentence rather than
// however many minutes into a plan or apply the first request to it would take.
func connectMSDeployment(ctx context.Context, opts *Options) (*msdeploy.Client, error) {
	url := msDeploymentURL(opts)
	client := msdeploy.New(url)
	if !client.Reachable(ctx) {
		return nil, nserr.New(nserr.Fail,
			"hmd-ms-deployment is not answering at %s. Start the control plane first with `nsctl control-plane start`.", url)
	}
	return client, nil
}

// buildBomEnv derives the bom.Environment a registered environment maps to,
// alongside the Floci target and names later steps need -- resolved once so
// Apply and ComputePlan agree on exactly what `env apply` would build.
func buildBomEnv(ctx context.Context, opts *Options, d *container.Docker, env *registry.Environment) (bom.Environment, floci.Target, floci.Names) {
	names := floci.NamesFrom(opts.Lookup, env.DeploymentID, env.Slug)
	existing := d.ContainerNames(ctx)
	k3sContainer := floci.K3sContainerName(env.K3sCluster, env.AccountID, existing)

	target := floci.ForAccount(opts.Lookup, env.AccountID, env.LegacyLayout)
	bomEnv := bom.Environment{
		Slug: env.Slug, DeploymentID: env.DeploymentID, AccountID: env.AccountID,
		// The access key is what Floci reads the account off, and it is carried
		// separately from AccountID because signing with the wrong one does not
		// fail -- it succeeds against the wrong account.
		AccessKeyID: target.AccessKeyID,
		Region:      names.Region, DBContainer: env.DBContainer,
		K3sCluster: env.K3sCluster, K3sContainer: k3sContainer,
	}
	return bomEnv, target, names
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
	client, err := connectMSDeployment(ctx, opts)
	if err != nil {
		return err
	}
	bomEnv, target, names := buildBomEnv(ctx, opts, d, env)

	// Everything above the substrate is what the manifest declares. An
	// environment with none is not an error -- that is the empty environment,
	// and it gets whatever substrate its recorded mode names (NERD014).
	declared, err := manifest.Load(opts.Home, env.Slug, opts.Lookup)
	if err != nil {
		return nserr.Wrap(nserr.Usage, err)
	}
	mode := declared.SubstrateMode()
	if env.LegacyLayout {
		mode = manifest.SubstrateFull
	}
	steps := planFor(mode)
	if err := refuseCoreBindings(mode, declared); err != nil {
		return err
	}
	// The cluster entry only makes sense when k3s is enabled: without it the
	// eks-cluster node deploys a cluster nothing will run.
	substrate := bom.SubstrateFor(bomEnv, mode, k3sEnabled(opts))
	var (
		declaredRepos []manifest.Repo
		repoPaths     map[string]string
	)
	if declared != nil {
		declaredRepos = declared.Repos
		repoPaths = repoclass.Paths(declaredRepos, opts.Lookup)
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

	// The External Secrets operator, unless opted out. It reaches nsctl through
	// the environment manifest today, which on a developer machine the Python
	// CLI wrote -- so an environment created by `nsctl env add` alone got a
	// cluster with no operator on it, and the first cloud chart rendering an
	// ExternalSecret failed on the missing CRD. Bundling the repo (SPEC006) is
	// what lets it deploy; declaring it here is what makes it deploy at all.
	//
	// Not in Substrate's own list: substrateInstances decides which of the two
	// changesets an entry lands in, and these belong in the second. The first
	// is applied before the concrete core Resources are submitted, and a
	// dependency carrying a tag_selector cannot resolve against a type nothing
	// has produced yet.
	if steps.Cluster && extSecretsEnabled(opts) && k3sEnabled(opts) {
		entries = append(entries, bom.ExtSecrets(bomEnv)...)
	}

	// Before the plan is computed, not only inside Seed.
	//
	// Seed injects the per-environment endpoints on the way past, but the
	// reconcile snapshot hashes the desired state built here. Injecting only in
	// Seed means the digest covers a pre-injection entry that never matches
	// what was seeded, and ext-secrets reads as drifted on every single apply.
	// change_set_builder does the same thing in both places for the same
	// reason, and says so: "Omitting it left ext-secrets permanently drifted."
	// Defaults first, then the account. A manifest that declares ext-secrets
	// itself shadows bom.ExtSecrets' entry, and InjectExtSecretsAccount only
	// patches keys that already exist -- so without this an environment whose
	// manifest carries no instance_configuration got the chart's cloud
	// defaults: IRSA against a role in the control plane's account, every
	// ClusterSecretStore stuck at InvalidProviderConfig.
	bom.ApplyExtSecretsDefaults(entries, bomEnv.AccessKeyID)
	bom.InjectExtSecretsAccount(entries, bomEnv.AccessKeyID)

	// Floci spawns its database backend from an image reference pinned into its
	// own configuration, and a reference that resolves nowhere fails the
	// instance while the deploy polls indefinitely. Catch it in seconds here.
	for _, problem := range floci.EnsureBackendImages(ctx, d, floci.ContainerName, opts.step) {
		return nserr.New(nserr.Usage, "%s", problem)
	}

	// Said here so the `tofu init` failure 955 log lines below has a cause
	// attached to it, rather than reading as a broken bundled tree.
	WarnUndeployableSlug(opts.warn, env.Slug)

	if prov, err := floci.NewProvisioner(ctx, target, opts.Out, opts.Err); err == nil {
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

	resolver := repoclass.NewWithHome(opts.lookup("HMD_REPO_HOME"), opts.Home, opts.Lookup)
	repoclass.Seed(resolver, declaredRepos)
	if err := checkArtifacts(opts, resolver, declaredRepos); err != nil {
		return err
	}
	// Merged after the resolver is seeded, never before: repoclass.Paths drops
	// every artifact instance, because manifest.Repo.RepoPath answers "" for a
	// non-local source. Without this the runner falls through to its own bundled
	// and any-checkout tiers and deploys whatever tree it finds under the
	// version the resolver just reported -- the High risk this is written
	// against, and one that looks exactly like success.
	for class, path := range artifact.Paths(opts.Home, declaredRepos) {
		if repoPaths == nil {
			repoPaths = map[string]string{}
		}
		repoPaths[class] = path
	}

	// Versions are resolved before the plan, not left to the seeder: the
	// version is part of what an entry's digest covers, so planning against
	// version-less entries would neither see a version bump as drift nor
	// record a digest the Python front end would agree with.
	if err := bom.ResolveVersions(entries, resolver); err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}

	// What is already deployed and current does not need deploying again. The
	// graph answers the first half; the snapshot of the last apply answers the
	// second, since the graph records that an instance is DEPLOYED but not
	// what it was deployed *from*.
	plan := reconcile.Degraded(entries)
	purged := WasPurged(opts.Home, env.DeploymentID)
	if opts.ForceRedeploy {
		opts.step("Redeploying everything, as asked")
	} else if status, err := client.InstanceStatus(ctx, env.DeploymentID); err != nil {
		// Fail safe rather than fail closed: an unreadable graph means nothing
		// is known, so everything is deployed rather than silently skipped.
		opts.warn("could not read the deployment graph, so nothing is assumed about what is deployed: %v", err)
	} else {
		snapshot := reconcile.LoadSnapshot(env.StateDir)
		if purged {
			// The graph's DEPLOYED records outlived a purge and describe
			// nothing that still exists, so an entry with no digest from a
			// post-purge apply is a fresh deploy, not "no information". Keyed
			// on the snapshot rather than forcing everything, so a retry after
			// a partial failure redeploys only what has not come back. See
			// PurgedMarkerPath.
			stale := 0
			for _, entry := range entries {
				if _, recorded := snapshot[entry.RepoInstanceName]; !recorded {
					opts.markForRedeploy(entry.RepoInstanceName)
					stale++
				}
			}
			if stale > 0 {
				opts.step("%q was purged: %d instance(s) the deployment graph still calls deployed are redeployed", env.DeploymentID, stale)
			}
		}
		plan = reconcile.Compute(entries, status, snapshot, opts.redeployInstances)
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

	seeder := &bom.Seeder{
		Client: client, Versions: resolver,
		Region: names.Region,
		Warn:   opts.warn,
	}

	// Two phases, not one changeset.
	//
	// A dependency carrying a tag_selector is matched against concrete
	// Resources, not against produced types, and the Resources describing this
	// environment's core can only be attached once local-neuronsphere has a
	// RepoInstanceDeployment -- which a changeset is what creates. Seeding
	// everything at once therefore fails at apply on a graph where those
	// Resources do not already exist:
	//
	//	For RepoInstance, airflow-db-account, role, create-service: supplied
	//	instance, local-neuronsphere, satisfies neither the required resource
	//	type ... nor a suggested repo_class.
	//
	// So: the substrate applies and deploys first, its Resources are submitted,
	// and only then is everything above it seeded.
	phaseA, phaseB := splitSubstrate(entries)

	// The kubeconfig a deploy container mounts has to name the in-network
	// k3s server: the host file points at a published localhost port that a
	// container cannot reach, which a CDKTF kubernetes provider (transform's
	// Argo ServiceAccount, its PriorityClasses) reports as "connection
	// refused" on every resource.
	k3sContainer := clusterFor(steps, floci.K3sContainerName(env.K3sCluster, env.AccountID, d.ContainerNames(ctx)))
	kubeconfig := clusterFor(steps, env.Kubeconfig)
	if kubeconfig != "" && k3sContainer != "" {
		kube := &k3s.Kube{
			Cluster: env.K3sCluster, Container: k3sContainer, Kubeconfig: kubeconfig,
			TempDir: runner.TempDir(opts.Home),
		}
		path, cleanup, err := kube.KubeconfigForContainer()
		if err == nil {
			kubeconfig = path
			defer cleanup()
		}
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
			K3sCluster:           clusterFor(steps, env.K3sCluster),
			K3sContainer:         k3sContainer,
			Kubeconfig:           kubeconfig,
			Environment:          env.Slug,
			RepoHome:             opts.lookup("HMD_REPO_HOME"),
			Home:                 opts.Home,
			Lookup:               opts.Lookup,
			RepoPaths:            repoPaths,
			DeploymentID:         env.DeploymentID,
			Region:               names.Region,
			CustomerCode:         names.CustomerCode,
			Extra: map[string]string{
				"AWS_REGION": opts.lookup("AWS_REGION"),
			},
		},
		Out: opts.Out, Err: opts.Err,
		Parallelism: RunnerParallelism(opts.lookup),
		LogDir:      filepath.Join(env.StatePath(), "logs"),
	}

	// Phase A: the substrate.
	substrateNodes, err := runPhase(ctx, opts, seeder, run, bomEnv, phaseA, "substrate")
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
		prov, provErr := floci.NewProvisioner(ctx, target, opts.Out, opts.Err)
		if provErr == nil {
			released, relErr := floci.ReleaseUnmanagedSubnetGroup(ctx, prov, floci.LocalDBSubnetGroup)
			if relErr != nil {
				opts.warn("%v", relErr)
			} else if released {
				opts.step("  released the CLI-created DB subnet group; retrying so base-vpc can own it")
				substrateNodes, err = runPhase(ctx, opts, seeder, run, bomEnv, phaseA, "substrate")
			}
		}
	}
	// The concrete Resources describing this environment's core, attached to
	// the deployment Phase A just created. Between the phases, because a
	// Phase-B entry whose dependency selects one needs it to already exist.
	if err == nil && steps.CoreResources {
		cluster := ""
		if steps.Cluster && k3sEnabled(opts) {
			cluster = bomEnv.K3sCluster
		}
		resources := bom.LocalCoreResources(bomEnv, reg.ControlPlane.Network, cluster, foundationServices(env.Slug))
		opts.step("Submitting core resources...")
		count := seeder.SubmitResources(ctx, seeder.CoreDeploymentID(ctx, bomEnv, substrateNodes), resources)
		opts.step("  %d core resource(s) submitted", count)

		// The cluster Phase A may have just created, made usable before Phase B
		// deploys the first chart onto it.
		if steps.Cluster {
			provisionNewCluster(ctx, opts, reg, env, d)
		}
	}
	if err == nil {
		// The database and graph Phase A may have just created, named before
		// Phase B addresses them -- the same reason provisionNewCluster runs
		// here for the cluster.
		//
		// On a first start there is no environment database until Phase A's
		// deploy makes one, so `env start` has nothing to attach the alias to
		// and says so ("no database ... has ever been deployed"). Phase B's
		// first node is a hmd-database-account, which posts to ms-dbaccount,
		// which connects to `hmd_db-<env>` -- and without this the name does
		// not resolve yet:
		//
		//     psycopg2.OperationalError: could not translate host name
		//     "hmd_db-local" to address: Name or service not known
		//
		// Attaching it only after Phase B, as the call below did alone, made
		// that a first-start-only failure that a retry then papered over: every
		// warm start inherits the alias from the run before, which is why it
		// survived every --force-full-redeploy and only a purged environment
		// showed it.
		refreshSpawnedAliases(ctx, opts, reg, env, d, names)

		// Phase B: everything the manifest declares.
		_, err = runPhase(ctx, opts, seeder, run, bomEnv, phaseB, "declared")
	}

	// Recorded before the error is returned, so a run that failed partway
	// still keeps what it settled and a retry picks up where it stopped
	// rather than redeploying the instances that worked.
	//
	// Only what is known good goes in: what this run settled, plus what the
	// plan already found deployed and current. A digest recorded for an entry
	// that failed would make the next run read it as up to date.
	succeeded := run.Succeeded
	if writeErr := reconcile.WriteSnapshot(env.StateDir, settled(plan, succeeded), nil); writeErr != nil {
		opts.warn("%v", writeErr)
	}
	// Again, for what Phase B itself spawned, and on failure as well as
	// success: a node that failed for want of the alias succeeds on the retry
	// only if the retry can resolve the name. The call before Phase B covers
	// what Phase A created; this one covers the rest and is why a failed run
	// still leaves the environment addressable.
	refreshSpawnedAliases(ctx, opts, reg, env, d, names)
	if err != nil {
		return nserr.Wrap(nserr.DeployFailed, err)
	}
	opts.step("  deployed %d instance(s)", len(succeeded))
	if purged {
		if err := ClearPurged(opts.Home, env.DeploymentID); err != nil {
			opts.warn("could not clear the purge marker for %q: %v", env.DeploymentID, err)
		}
	}
	return nil
}

// settled is the entries whose recorded digest is trustworthy after a run.
// checkArtifacts refuses an apply that names an artifact nothing has fetched,
// and reports a working tree that will pre-empt one.
//
// Run before the deploy starts rather than left to the node that would mount
// nothing, for the same reason EnsureBackendImages is: a manifest naming three
// uncached versions should say so in seconds and name all three, not fail on the
// first one nine hundred log lines in with a message about a missing directory.
//
// Nothing here contacts the librarian. Resolution is offline by construction, so
// this is a stat of the cache and the message says as much -- a message implying
// a 404 would invite the reader to debug a network that was never involved.
func checkArtifacts(opts *Options, resolver *repoclass.Resolver, repos []manifest.Repo) error {
	var missing []string
	for _, r := range repos {
		if r.SourceType() != manifest.SourceArtifact || r.RepoClassName == "" {
			continue
		}
		// Development beats distribution: a tree the user explicitly asked for
		// still wins. Said out loud on the way past, because it is the kind of
		// substitution that is invisible until it has cost an afternoon -- and
		// the user asked for it by setting the variable, so printing it costs
		// nothing next to debugging its absence.
		if tree := resolver.PreemptedArtifact(r.RepoClassName); tree != "" {
			opts.warn("using the working tree at %s for %s instead of the declared artifact %s",
				tree, r.RepoClassName, r.Version)
			continue
		}
		if artifact.Cached(opts.Home, r.RepoClassName, r.Version) {
			continue
		}
		spec := librarian.Spec{Name: r.RepoClassName, Version: r.Version, ItemType: r.ArtifactType()}
		missing = append(missing, artifact.Unavailable(
			opts.Home, r.RepoClassName, r.Version, spec.ContentPath(),
			opts.lookup("HMD_REPO_HOME"), nil).Error()+
			"\nFetch it with:  nsctl artifact pull "+spec.String())
	}
	if len(missing) == 0 {
		return nil
	}
	return nserr.New(nserr.Usage, "%s", strings.Join(missing, "\n\n"))
}

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

// extSecretsEnabled matches bom_seeder's opt-out: on unless explicitly falsy.
//
// Gated in this package rather than inside internal/bom, so the BOM
// constructors stay pure functions of their arguments -- the same division
// k3sEnabled and Substrate's withCluster parameter already draw.
func extSecretsEnabled(opts *Options) bool {
	switch strings.ToLower(strings.TrimSpace(opts.lookup(bom.ExtSecretsEnabledEnv))) {
	case "false", "0", "no":
		return false
	}
	return true
}

// clusterFor is a cluster-scoped runner setting under a mode that has one, and
// empty otherwise. Empty rather than merely unused, because Docker creates a
// directory at a bind-mount source that does not exist -- see
// kubeconfigUnusable for what that cost.
func clusterFor(plan startPlan, value string) string {
	if plan.Cluster {
		return value
	}
	return ""
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
	return runner.ProjectBuilderRef(opts.Lookup)
}

// splitSubstrate divides a plan's entries into the two changesets an apply
// seeds: the substrate, then everything declared on top of it.
//
// Order within each half is preserved, since Seed topologically sorts what it
// is given and the input order is already dependency-ordered.
func splitSubstrate(entries []bom.Entry) (substrate, declared []bom.Entry) {
	for _, e := range entries {
		if bom.IsSubstrate(e.RepoInstanceName) {
			substrate = append(substrate, e)
		} else {
			declared = append(declared, e)
		}
	}
	return substrate, declared
}

// runPhase seeds one changeset and deploys it, returning the nodes it created.
//
// An empty phase is not an error and not a no-op worth announcing: a reconcile
// plan that found the substrate already current leaves nothing to apply, and
// the phase after it still has work.
func runPhase(ctx context.Context, opts *Options, seeder *bom.Seeder, run *runner.Runner,
	env bom.Environment, entries []bom.Entry, label string) ([]msdeploy.DeploymentNode, error) {

	if len(entries) == 0 {
		return nil, nil
	}
	opts.step("Seeding the %s plan...", label)
	nodes, err := seeder.Seed(ctx, env, entries)
	if err != nil {
		return nil, nserr.Wrap(nserr.Fail, err)
	}
	opts.step("  %d deployment node(s)", len(nodes))
	if len(nodes) == 0 {
		return nodes, nil
	}
	opts.step("Running the %s deployment...", label)
	return nodes, run.Run(ctx, nodes)
}

// foundationServices are the Lambdas nsctl owns directly, advertised as
// microservice Resources so a dependency naming one by tag_selector resolves.
//
// They are never RepoInstances -- their own manifests' required dependencies
// would not resolve locally -- so a concrete Resource is the only way a
// consumer can find them. hmd-ms-dbaccount is the one that matters most:
// every hmd-database-account instance's create-service role selects it.
func foundationServices(envSlug string) []bom.ServiceSpec {
	return []bom.ServiceSpec{
		{
			Name:       "hmd_ms_dbaccount",
			RepoClass:  DBAccountRepoClass,
			APIBaseURL: "http://localhost/" + envSlug + "/hmd_ms_dbaccount",
		},
		// Control-plane services are addressed unprefixed; an environment's own
		// sit under its /<slug>/ prefix.
		{Name: "hmd_ms_deployment", RepoClass: "hmd-ms-deployment", APIBaseURL: DefaultMSDeploymentURL},
		{Name: "hmd_ms_naming", RepoClass: "hmd-ms-naming", APIBaseURL: "http://localhost/hmd_ms_naming"},
		{Name: "hmd_ms_artifact_lib", RepoClass: "hmd-ms-artifact-lib", APIBaseURL: "http://localhost/hmd_ms_artifact_lib"},
	}
}

// provisionNewCluster writes the kubeconfig, CoreDNS records and ingress for a
// cluster that Phase A has only just created, before Phase B needs them.
//
// Start already knows the cluster "may only now exist" and re-runs startCluster
// for exactly that case -- but it does so *after* Apply returns, and the cluster
// is created inside Apply's first phase and consumed by its second. On a cold
// start that ordering never held:
//
//	Phase A  eks-cluster deployed        <- the cluster now exists
//	         7 core resource(s) submitted
//	Phase B  ext-secrets-crds ...        <- helm, and no kubeconfig has been written
//	         IsADirectoryError: [Errno 21] Is a directory: '/root/.kube/config'
//
// Docker creates a missing bind-mount source as a directory, so the first chart
// both fails and leaves a directory where the kubeconfig belongs. 538c10a made
// WriteKubeconfig replace such a directory, which is the right repair and does
// not help here: nothing had called WriteKubeconfig at all. startCluster is its
// only caller, and on a cold start it is skipped -- there is no container yet --
// so the recovery Start performs afterwards is one phase too late to matter.
//
// Invisible on a warm platform, like the five defects before it: an environment
// that has ever been started has a kubeconfig on disk, and Phase B reads it
// without anyone noticing who wrote it.
//
// Best-effort, and quiet when there is nothing to do. A warm apply finds a
// readable kubeconfig and returns without touching the cluster.
func provisionNewCluster(ctx context.Context, opts *Options, reg *registry.Registry,
	env *registry.Environment, d *container.Docker) {

	if !k3sEnabled(opts) {
		return
	}
	cluster := floci.K3sContainerName(env.K3sCluster, env.AccountID, d.ContainerNames(ctx))
	if running, _ := d.Running(ctx, cluster); !running {
		// Phase A did not create one, or it is not up. Start's post-Apply
		// recovery reports that, and reporting it twice from two places is
		// worse than reporting it once from the one that can act on it.
		return
	}
	if !kubeconfigUnusable(env.Kubeconfig) {
		return
	}

	opts.step("Provisioning the cluster the substrate created...")
	routerEnv := router.Env{
		Slug: env.Slug, AccountID: env.AccountID,
		TrinoPort: env.TrinoPort(), K3sPort: env.K3sPort(), SparePort: env.SparePort(),
		IsDefault: env.IsDefault(),
	}
	// Resolved now rather than passed in: the database is a Phase A node too,
	// so its container did not exist when Start looked.
	dbContainer, _ := d.FlociContainer(ctx, "rds", env.AccountID,
		floci.EnvDBIdentifier(floci.NamesFrom(opts.Lookup, env.DeploymentID, env.Slug)))
	graphContainer, _ := d.FlociContainer(ctx, "neptune", env.AccountID,
		floci.GraphIdentifier(floci.NamesFrom(opts.Lookup, env.DeploymentID, env.Slug)))

	if err := startCluster(ctx, opts, d, router.New(opts.Home, opts.Lookup), routerEnv, env,
		cluster, reg.ControlPlane.Network, dbContainer, graphContainer); err != nil {
		opts.warn("%v", err)
	}
}

// kubeconfigUnusable reports whether a path cannot be handed to kubectl or helm.
//
// Three states, one answer. Absent is the cold start: nothing has written it
// yet. A *directory* is the daemon's doing -- Docker creates a missing
// bind-mount source as one, so the first deploy to mount the path before the
// cluster wrote it leaves a directory that every later deploy then mounts,
// dying with "IsADirectoryError: [Errno 21] Is a directory: '/root/.kube/config'"
// and naming only the container's path. Empty is an interrupted write.
//
// A stat error other than "not exist" also reads as unusable: something that
// cannot be inspected cannot be trusted to be a kubeconfig, and rewriting one
// is cheap.
func kubeconfigUnusable(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		return true
	}
	return info.IsDir() || info.Size() == 0
}

// refreshSpawnedAliases attaches the environment's names to the database and
// graph containers Floci may have spawned during this apply. Idempotent, and
// each step only warns, so a container that is not there yet costs nothing.
func refreshSpawnedAliases(ctx context.Context, opts *Options, reg *registry.Registry,
	env *registry.Environment, d *container.Docker, names floci.Names) {

	var dbContainer, graphContainer string
	var err error
	if dbContainer, err = floci.EnsureRDSRunning(ctx, d, env.AccountID, floci.EnvDBIdentifier(names),
		env.DBContainer, reg.ControlPlane.Network, 2*time.Minute, 0); err != nil {
		opts.warn("%v", err)
	} else if dbContainer != "" {
		opts.step("  %s is up and aliased as %s", dbContainer, env.DBContainer)
	}
	if graphContainer, err = floci.EnsureNeptuneRunning(ctx, d, env.AccountID, floci.GraphIdentifier(names),
		env.GraphContainer, reg.ControlPlane.Network); err != nil {
		opts.warn("%v", err)
	} else if graphContainer != "" {
		opts.step("  %s is up and aliased as %s", graphContainer, env.GraphContainer)
	}

	// The Docker alias is only half of it. A pod resolves these names through
	// the cluster's coredns-custom record, which was written when the cluster
	// was provisioned -- before Phase A created the database on a first start,
	// so it holds no entry for it or a stale one from a container since
	// replaced. The alias above fixes the Floci Lambdas and leaves every chart
	// broken:
	//
	//     connection to server at "hmd_db-local" (172.27.0.11), port 5432
	//     failed: Connection refused
	//
	// Rewriting is cheap and idempotent, and the records are rebuilt wholesale
	// from the containers as they are now.
	cluster := floci.K3sContainerName(env.K3sCluster, env.AccountID, d.ContainerNames(ctx))
	if cluster == "" {
		return
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
		Network: reg.ControlPlane.Network, IngressEnabled: ingressEnabled(opts),
		Out: opts.Out, Err: opts.Err,
	}
	if err := ops.EnsureCoreDNSRecordsFor(ctx, dbContainer, graphContainer); err != nil {
		opts.warn("%v", err)
	}
}
