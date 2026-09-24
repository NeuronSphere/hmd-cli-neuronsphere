package controlplane

import (
	"context"
	"strings"
	"time"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/container"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/floci"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/repoclass"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/router"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/runner"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/tools"
)

// The control plane's own instances.
//
// The instance name is what make_standard_name folds into the admin database
// secret, so anything deriving that name independently has to agree with it.
const (
	CPDBInstance  = "control-plane-db"
	CPDBRepoClass = "hmd-postgres-rds"

	// The control plane's own network. Deploying hmd-vpc is what creates the
	// VPC, subnets and the named DB subnet group instances are placed into.
	//
	// It replaces an imperative workaround. The Python creates all three with
	// the EC2 SDK from provision_resources, because Floci's
	// Ec2Service.ensureDefaultResources guards on region alone while its VPC
	// storage is per-account -- so the first account to touch EC2 marks the
	// region seeded and every other account is left with no default VPC, and
	// RDS then refuses CreateDBInstance. Deploying the repo that owns VPCs is
	// the same fix said the way the platform says everything else, and it is
	// what the environment substrate already does with base-vpc.
	CPVPCInstance  = "control-plane-vpc"
	CPVPCRepoClass = "hmd-vpc"

	CPGraphInstance  = "control-plane-graph"
	CPGraphRepoClass = "hmd-inf-neptune"
	CPGraphHost      = "global-graph"

	// CPDeploymentID is the deployment id control-plane instances carry. The
	// control plane is not one of the environments, so it is not a slug.
	CPDeploymentID = "cp"
)

// bootstrapServices are the foundation Lambdas, by service name, in the order
// they can be deployed. ms-deployment is last on purpose: everything above it
// is what it needs to run, and it is what every later deploy reports to.
//
// The image each one runs is floci.ImageClassFor(service): the same class for
// all but ms-deployment, which runs the hmd-ms-deployment-core image under the
// hmd-ms-deployment service name (NERD0015).
var bootstrapServices = []string{
	"hmd-ms-naming",
	// After the graph: artifact-lib's neptune-db dependency is required and
	// resolves to the graph alias, which only exists once the graph node ran.
	"hmd-ms-artifact-lib",
	"hmd-ms-deployment",
}

// CPGraphIdentifier is the DBClusterIdentifier the control-plane graph node
// creates.
//
// Not floci.GraphIdentifier, which names the *environment's* graph
// (instance `global-graph`, the environment's deployment id). The control
// plane's is its own instance under deployment id `cp`, and asking for the
// wrong one silently finds nothing -- which is how the control-plane graph
// came to be left stopped after a restart while the code looked correct.
func CPGraphIdentifier(names floci.Names) string {
	return tools.ResourceIdentifier(
		CPGraphInstance, CPGraphRepoClass, CPDeploymentID, "local",
		names.Region, names.CustomerCode)
}

// Bootstrap brings up a control plane that has never been bootstrapped.
//
// The shape is a DAG rather than a sequence of imperative calls for one
// reason worth keeping: the control plane's Postgres and graph are deployed by
// the same RepoClasses the cloud uses -- hmd-postgres-rds and hmd-inf-neptune
// -- through the same projectbuilder path every other deploy takes. What they
// produce is a real Resource, not a record hand-written to describe a
// container something happened to start.
//
// Nothing is reported to hmd-ms-deployment while this runs, because
// ms-deployment is the last node: it does not exist yet. The Python buffers
// the status updates and replays them afterwards, but its own docstring
// records that the replay writes nothing -- the bootstrap ids are locally
// generated and no entity corresponds to them, so every replayed call 404s.
// nsctl does not pretend otherwise; it runs the nodes untracked and says so.
func Bootstrap(ctx context.Context, opts *Options, reg *registry.Registry,
	target floci.Target, docker *container.Docker, r *router.Router,
	names floci.Names) error {

	opts.step("Bootstrapping the control plane...")
	opts.step("  hmd-ms-deployment does not exist yet, so these nodes are not recorded anywhere")

	resolver := repoclass.NewWithHome(opts.lookup("HMD_REPO_HOME"), opts.Home, opts.Lookup)
	deployer := &runner.Runner{
		Docker: docker,
		// No client: the service these would report to is the last node. A
		// nil client makes the runner's status calls no-ops rather than a
		// stream of connection errors.
		Client: nil,
		Config: runner.Config{
			Network:              reg.ControlPlane.Network,
			Image:                ProjectBuilderRef(opts),
			FlociEndpoint:        floci.InternalEndpoint,
			AccountID:            target.AccountID,
			DeploymentServiceURL: MSDeploymentURL(),
			LocalProxy:           "http://hmd_proxy",
			RepoHome:             opts.lookup("HMD_REPO_HOME"),
			Home:                 opts.Home,
			Lookup:               opts.Lookup,
			Environment:          CPDeploymentID,
			DeploymentID:         CPDeploymentID,
			Region:               names.Region,
			CustomerCode:         names.CustomerCode,
			Extra:                map[string]string{"AWS_REGION": opts.lookup("AWS_REGION")},
		},
		Out: opts.Out, Err: opts.Err,
	}

	// 1. The network, which the database is placed into. Without it
	// CreateDBInstance fails with DBSubnetGroupNotFoundFault, since the group
	// the configuration names does not exist until this runs.
	if err := deployNode(ctx, opts, deployer, resolver, CPVPCInstance, CPVPCRepoClass, nil); err != nil {
		return err
	}

	// 2. The control-plane Postgres, deployed for real.
	pgConfig := postgresConfig(ctx, docker)
	if err := deployNode(ctx, opts, deployer, resolver, CPDBInstance, CPDBRepoClass, pgConfig); err != nil {
		return err
	}

	// 3. Its alias, then the databases on it. Both are CLI-side work the
	// deploy cannot do from inside its own container: the alias is a Docker
	// network operation, and ms-deployment's database has to exist before
	// ms-deployment can, which rules out creating it through a service.
	opts.step("Starting the control-plane database...")
	dbContainer, err := floci.EnsureRDSRunning(ctx, docker, target.AccountID,
		floci.ControlPlaneDBIdentifier(names), floci.ControlPlaneDBAlias,
		reg.ControlPlane.Network, 3*time.Minute, 0)
	if err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	if dbContainer == "" {
		return nserr.New(nserr.Fail,
			"the control-plane database deploy reported success but Floci spawned no container for %s",
			floci.ControlPlaneDBIdentifier(names))
	}
	opts.step("  %s is up and aliased as %s", dbContainer, floci.ControlPlaneDBAlias)

	opts.step("Creating the control-plane databases...")
	if err := floci.EnsureCoreDatabases(ctx, docker, dbContainer, nil, 4*time.Minute, 0); err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	for _, db := range floci.CoreDatabases {
		opts.step("  %s", db.Name)
	}

	// 4. The graph, likewise deployed for real, then aliased.
	if err := deployNode(ctx, opts, deployer, resolver, CPGraphInstance, CPGraphRepoClass, graphConfig()); err != nil {
		return err
	}
	opts.step("Starting the control-plane graph...")
	graphContainer, err := floci.EnsureNeptuneRunning(ctx, docker, target.AccountID,
		CPGraphIdentifier(names), CPGraphHost, reg.ControlPlane.Network)
	if err != nil {
		opts.warn("%v", err)
	} else if graphContainer != "" {
		opts.step("  %s is up and aliased as %s", graphContainer, CPGraphHost)
	}

	// 5. The foundation Lambdas, ms-deployment last.
	services, err := floci.NewServices(ctx, target)
	if err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	// The buckets those Lambdas need. Passed as a function rather than a
	// provisioner so the one thing a foundation service may do to Floci's S3 is
	// create the bucket it was told to.
	prov, err := floci.NewProvisioner(ctx, target, opts.Out, opts.Err)
	if err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	ensureBucket := prov.EnsureBucket
	gateways := map[string]string{}
	for _, repoClass := range bootstrapServices {
		apiID, err := deployFoundationService(ctx, opts, docker, services, resolver, repoClass, names, ensureBucket)
		if err != nil {
			return err
		}
		gateways[floci.LambdaName(repoClass)] = apiID
	}

	// 6. Routes, so the services just deployed are reachable at the paths
	// everything else expects.
	if err := r.WriteControlPlaneRoutes(gateways, floci.DefaultStage, "", nil); err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}

	reg.ControlPlane.Bootstrapped = true
	if err := reg.Save(opts.Home); err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	opts.step("  bootstrapped")
	return nil
}

// deployNode runs one real projectbuilder deploy.
func deployNode(ctx context.Context, opts *Options, deployer *runner.Runner,
	resolver *repoclass.Resolver, instance, repoClass string, config map[string]any) error {

	version := resolver.ResolveVersion(repoClass, "").Version
	// No RID: ms-deployment does not exist while this runs, so the deploy
	// neither reports to it nor submits produced Resources.
	script, err := runner.DeployScript(runner.ScriptParams{
		RepoClass: repoClass, Instance: instance, Version: version,
		DeploymentID: CPDeploymentID, Region: opts.lookup("HMD_REGION"),
		Environment: "local", Config: config,
	})
	if err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	node := msdeploy.DeploymentNode{
		InstanceName:          instance,
		RepoClassName:         repoClass,
		Version:               version,
		Script:                script,
		InstanceConfiguration: config,
	}
	opts.step("Deploying %s (%s@%s)...", instance, repoClass, version)
	if res := deployer.RunNode(ctx, node); res.Failed {
		deployer.ReportFailure(res)
		return nserr.New(nserr.DeployFailed, "deploying %s failed", instance)
	}
	return nil
}

// deployFoundationService deploys one control-plane Lambda and routes it.
func deployFoundationService(ctx context.Context, opts *Options, docker *container.Docker,
	services *floci.Services, resolver *repoclass.Resolver, repoClass string,
	names floci.Names, ensureBucket func(context.Context, string) error) (string, error) {

	// Everything image- and descriptor-shaped comes from the image class; every
	// name -- Lambda, route, DB user, HMD_REPO_NAME -- from the service.
	imageClass := floci.ImageClassFor(repoClass)
	version := resolver.ResolveVersion(imageClass, "").Version
	if ref := opts.lookup(repoclass.ImageEnvVar(repoClass)); ref != "" {
		// HMD_REPO_VERSION should say what actually runs.
		if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
			version = ref[i+1:]
		}
	}
	image, err := floci.ServiceImage(ctx, docker,
		floci.ImageRef{Service: repoClass, ImageClass: imageClass, Version: version}, opts.Lookup)
	if err != nil {
		return "", nserr.Wrap(nserr.Usage, err)
	}
	if imageClass != repoClass {
		opts.step("  %s runs %s", repoClass, image)
	}
	// The descriptor -- SERVICE_CONFIG, parameters -- is the image class's,
	// except under a full-reference override: an engineer running the premium
	// hmd-ms-deployment image needs *its* operations_modules (a superset), so
	// when a tree for the service-named class resolves (a checkout under
	// HMD_REPO_HOME, or HMD_LOCAL_VERSION_<SERVICE>) it is preferred.
	descriptor := imageClass
	if opts.lookup(repoclass.ImageEnvVar(repoClass)) != "" && resolver.Dir(repoClass) != "" {
		descriptor = repoClass
		opts.step("  configured from the %s descriptor (%s)", repoClass, resolver.Dir(repoClass))
	}
	// The RepoClass declares its engines the way the cloud wires them, so the
	// declaration is resolved to what exists locally before it is handed over.
	config := floci.LocalizeServiceConfig(
		floci.ServiceConfig(resolver.Dir(descriptor)),
		floci.ControlPlaneDBAlias, floci.LambdaName(repoClass), CPGraphHost, repoClass, names)
	env := floci.ServiceEnv(repoClass, version, names, floci.ControlPlaneDBAlias, config)
	for k, v := range floci.ServiceParameters(resolver.Dir(descriptor)) {
		env[k] = v
	}
	// A librarian reads BUCKET_NAME and refuses to serve without it.
	//
	// SPEC014 recorded this as a gap only another repository could close, on the
	// premise that nothing in the RepoClass names the bucket. That premise was
	// wrong: the RepoClass declares a required `lib-repo` dependency on
	// hmd-inf-s3bucket, and the cloud *derives* the name from it rather than
	// declaring it anywhere -- see floci.LibrarianBucketName. So the name is
	// available here, from the manifest, by the cloud's own formula.
	//
	// The bucket is created directly rather than deployed as a BOM entry
	// because ms-deployment is the last node of this bootstrap and does not
	// exist yet. EnsureBucket is what Provision already does for the tfstate
	// bucket, for the same reason.
	if _, named := env["BUCKET_NAME"]; !named {
		bucket := floci.LibrarianBucketName(resolver.Dir(descriptor), CPDeploymentID, names)
		switch {
		case bucket == "" && floci.LibrarianStyle(resolver.Dir(descriptor)):
			// Still reported rather than guessed. A librarian that declares no
			// bucket dependency at all names no bucket, and inventing one here
			// would point it at storage nothing else uses.
			opts.warn("%s reads BUCKET_NAME and declares no %s dependency to derive one from, so it will not serve. "+
				"Everything else in the control plane is unaffected.", repoClass, floci.LibrarianBucketRepoClass)
		case bucket != "":
			if err := ensureBucket(ctx, bucket); err != nil {
				opts.warn("could not create %s's bucket %s: %v", repoClass, bucket, err)
			} else {
				env["BUCKET_NAME"] = bucket
				opts.step("  bucket %s", bucket)
			}
		}
	}

	opts.step("Deploying %s...", repoClass)
	apiID, err := services.SetupService(ctx, repoClass, image, env)
	if err != nil {
		return "", nserr.Wrap(nserr.Fail, err)
	}
	opts.step("  routed %s/%s/", strings.TrimSuffix(MSDeploymentURL(), "/hmd_ms_deployment"),
		floci.LambdaName(repoClass))
	return apiID, nil
}

// postgresConfig is the configuration the control-plane Postgres deploys with.
//
// Passed explicitly rather than left to the manifest: the cloud
// default_configuration describes Aurora, and those values would override the
// local overlay's with ones a plain aws_db_instance cannot use.
//
// engine_version is read from the image Floci will actually spawn rather than
// hardcoded, so the declared version and the binary that initialises the data
// directory cannot drift apart.
func postgresConfig(ctx context.Context, d floci.ImageChecker) map[string]any {
	config := map[string]any{
		// Explicit placement: Floci's implicit "default" subnet group is
		// unusable for any account but the first to touch EC2 in a region.
		"db_subnet_group_name": floci.LocalDBSubnetGroup,
		// The backend container directly, not Floci's RDS proxy, which does
		// not survive a Floci restart.
		"db_host":     floci.ControlPlaneDBAlias,
		"db_port":     5432,
		"db_username": "postgres",
		// What hmd-postgres-base bakes in as POSTGRES_PASSWORD.
		"db_password":       "admin",
		"instance_type":     "db.t3.micro",
		"allocated_storage": 20,
	}
	if major := floci.PostgresMajor(ctx, d, floci.ContainerName); major != "" {
		config["engine_version"] = major
	}
	return config
}

// graphConfig tells hmd-inf-neptune's local overlay which alias the CLI will
// attach once the node succeeds -- never Floci's own Gremlin proxy, which is
// not restored after a Floci restart.
func graphConfig() map[string]any {
	return map[string]any{"graph_host": CPGraphHost, "graph_port": 8182}
}
