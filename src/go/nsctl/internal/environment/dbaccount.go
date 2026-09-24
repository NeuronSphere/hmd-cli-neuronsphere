package environment

import (
	"context"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/container"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/floci"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/repoclass"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/router"
)

// DBAccountRepoClass is the per-environment database-account service.
//
// It is substrate rather than a user-added RepoClass: every RepoClass that
// wants a database depends on hmd-database-account, whose deploy calls this
// service. Nothing that needs a database can deploy until it is serving, which
// is also why it cannot itself be deployed through the graph -- its own
// manifest's required dependencies would never resolve.
const DBAccountRepoClass = "hmd-ms-dbaccount"

// MSBaseFloor is the oldest hmd-ms-base a service reached through an
// environment route may be built on (NERD024 SPEC005).
//
// Below it the SigV4 credential scope hmd_proxy must put in Authorization --
// Floci resolves a v1 REST API's owning account from that scope and from
// nothing else -- is parsed as a bearer token inside hmd-base-service's
// UserMiddleware. Middleware runs outside any route's error handling, so the
// okta_jwt_verifier DecodeError surfaces as a 500 with no body on *every*
// request, including the unauthenticated create_db_account POST that every
// hmd-database-account deploy node makes.
//
// 0.2.280 relocates the caller's credential to X-NS-Authorization and 0.2.282
// adds hmd-lib-auth 0.1.128's guard returning no claims for non-JWT input; only
// the second stops the crash, because the crash is in middleware rather than in
// a route. Declared rather than probed: an image does not report the base it
// was built on, and a floor discovered from a crash is the state NERD024 exists
// to leave.
const MSBaseFloor = "0.2.282"

// EnsureDBAccount deploys the environment's dbaccount Lambda and routes it.
//
// Idempotent: the Lambda is updated in place when it exists, and the gateway is
// recreated so stale routes from a previous run cannot hijack traffic.
func EnsureDBAccount(ctx context.Context, opts *Options, reg *registry.Registry,
	env *registry.Environment, d *container.Docker, r *router.Router,
	routerEnv router.Env, target floci.Target, names floci.Names) error {

	resolver := repoclass.NewWithHome(opts.lookup("HMD_REPO_HOME"), opts.Home, opts.Lookup)
	version := resolver.ResolveVersion(DBAccountRepoClass, "").Version

	image, err := floci.ServiceImage(ctx, d, floci.ImageRef{Service: DBAccountRepoClass, Version: version}, opts.Lookup)
	if err != nil {
		return err
	}

	services, err := floci.NewServices(ctx, target)
	if err != nil {
		return err
	}
	serviceConfig := floci.LocalizeServiceConfig(
		floci.ServiceConfig(resolver.Dir(DBAccountRepoClass)),
		env.DBContainer, floci.LambdaName(DBAccountRepoClass), env.GraphContainer,
		DBAccountRepoClass, names)
	envVars := floci.ServiceEnv(DBAccountRepoClass, version, names, env.DBContainer, serviceConfig)

	opts.step("Deploying %s...", DBAccountRepoClass)
	apiID, err := services.SetupService(ctx, DBAccountRepoClass, image, envVars)
	if err != nil {
		return err
	}

	// Routed under the environment prefix: hmd-cli-dbaccount posts to
	// {NS_LOCAL_PROXY}/hmd_ms_dbaccount/..., and NS_LOCAL_PROXY carries the
	// slug because the service is the environment's own.
	name := floci.LambdaName(DBAccountRepoClass)
	if err := r.WriteEnvRoutes(routerEnv, map[string]string{name: apiID}, floci.DefaultStage, nil); err != nil {
		return err
	}
	opts.step("  routed http://localhost/%s/%s/", env.Slug, name)
	return nil
}

// loadServiceConfig reads a service's meta-data/config_local.json, which is
// what its SERVICE_CONFIG carries. Absent is normal.

// deployedVersion is the version of the service Floci is actually serving,
// read from the HMD_REPO_VERSION ServiceEnv wrote when it was deployed
// (NERD024 SPEC002).
//
// A function that does not exist answers "", which is indistinguishable from
// "deploy it" and needs no separate absence check. So does a function whose
// configuration carries no version: it was deployed by something that did not
// write one, which is exactly the drift this reads for.
func deployedVersion(ctx context.Context, services *floci.Services, repoClass string) string {
	cfg, err := services.FunctionEnv(ctx, floci.LambdaName(repoClass))
	if err != nil {
		return ""
	}
	return cfg["HMD_REPO_VERSION"]
}

// DBAccountState is what a reconcile found and what it left behind.
type DBAccountState struct {
	// Deployed is the version Floci was serving before the reconcile, empty
	// when the service was not deployed at all.
	Deployed string
	// Resolved is the version this binary resolves for the service.
	Resolved string
	// Serving is the version running after the reconcile: Resolved when it
	// deployed or refreshed, Deployed when it found nothing to do.
	Serving string
	// Changed reports whether Floci or the proxy were touched.
	Changed bool
}

// ReconcileDBAccount brings the environment's dbaccount service up to the
// version this binary resolves, deploying it when it is absent (NERD024
// SPEC003).
//
// The caller decides whether the environment wants the service at all; this
// decides only whether what is there is current. A current service costs one
// GetFunction and touches neither Floci nor the proxy, which is what makes it
// safe to run on every apply.
//
// The deployed version is read before the image is resolved, deliberately:
// resolving an image can pull it, and an environment that is already current
// should not pay for a pull to discover it has nothing to do.
func ReconcileDBAccount(ctx context.Context, opts *Options, reg *registry.Registry,
	env *registry.Environment, d *container.Docker, r *router.Router,
	routerEnv router.Env, target floci.Target, names floci.Names) (DBAccountState, error) {

	services, err := floci.NewServices(ctx, target)
	if err != nil {
		return DBAccountState{}, err
	}
	resolver := repoclass.NewWithHome(opts.lookup("HMD_REPO_HOME"), opts.Home, opts.Lookup)
	state := DBAccountState{Resolved: resolver.ResolveVersion(DBAccountRepoClass, "").Version}
	state.Deployed = deployedVersion(ctx, services, DBAccountRepoClass)
	state.Serving = state.Deployed

	if state.Deployed == state.Resolved {
		return state, nil
	}
	if state.Deployed == "" {
		opts.step("%s is not deployed in %q; deploying it", DBAccountRepoClass, env.Slug)
	} else {
		// Said as a version change rather than as an upgrade: the resolver's
		// tiers let a developer pin an older one on purpose, and calling that
		// an upgrade would misreport what happened.
		opts.step("%s in %q is %s, this nsctl resolves %s; refreshing it",
			DBAccountRepoClass, env.Slug, state.Deployed, state.Resolved)
	}
	if err := EnsureDBAccount(ctx, opts, reg, env, d, r, routerEnv, target, names); err != nil {
		return state, err
	}
	state.Serving = state.Resolved
	state.Changed = true
	return state, nil
}
