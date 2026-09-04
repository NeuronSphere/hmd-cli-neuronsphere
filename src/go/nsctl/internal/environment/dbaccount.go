package environment

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

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

// EnsureDBAccount deploys the environment's dbaccount Lambda and routes it.
//
// Idempotent: the Lambda is updated in place when it exists, and the gateway is
// recreated so stale routes from a previous run cannot hijack traffic.
func EnsureDBAccount(ctx context.Context, opts *Options, reg *registry.Registry,
	env *registry.Environment, d *container.Docker, r *router.Router,
	routerEnv router.Env, target floci.Target, names floci.Names) error {

	resolver := repoclass.New(opts.lookup("HMD_REPO_HOME"), opts.Lookup)
	version := resolver.ResolveVersion(DBAccountRepoClass, "").Version

	image, err := resolveServiceImage(ctx, d, opts, DBAccountRepoClass, version)
	if err != nil {
		return err
	}

	services, err := floci.NewServices(ctx, target)
	if err != nil {
		return err
	}
	serviceConfig := loadServiceConfig(resolver.Dir(DBAccountRepoClass))
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

// resolveServiceImage finds the image to run a foundation service from.
//
// The bare <repo>:<version> tag is what Floci is handed, and the local
// convention tags every built image that way; a registry-qualified candidate is
// tried first so a pulled image works too.
func resolveServiceImage(ctx context.Context, d *container.Docker, opts *Options, repoClass, version string) (string, error) {
	var candidates []string
	for _, prefix := range []string{
		opts.lookup("HMD_CONTAINER_REGISTRY"),
		opts.lookup("HMD_LOCAL_NS_CONTAINER_REGISTRY"),
	} {
		if prefix != "" {
			candidates = append(candidates, prefix+"/"+repoClass+":"+version)
		}
	}
	candidates = append(candidates, repoClass+":"+version)

	for _, ref := range candidates {
		if d.ImagePresent(ctx, ref) {
			return ref, nil
		}
	}
	return "", fmt.Errorf(
		"no local image for %s@%s (looked for %v). Build it with `hmd build` in that repo, or set HMD_LOCAL_VERSION_%s to a version you have",
		repoClass, version, candidates, repoclass.VersionEnvVar(repoClass))
}

// loadServiceConfig reads a service's meta-data/config_local.json, which is
// what its SERVICE_CONFIG carries. Absent is normal.
func loadServiceConfig(repoDir string) map[string]any {
	if repoDir == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(repoDir, "meta-data", "config_local.json"))
	if err != nil {
		return nil
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		return nil
	}
	return config
}
