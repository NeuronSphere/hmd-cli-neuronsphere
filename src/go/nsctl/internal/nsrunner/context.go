package nsrunner

import (
	"fmt"
	"path/filepath"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/floci"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/runner"
)

// resolveConfig builds the projectbuilder invocation's context for an
// environment, from HMD_HOME alone.
//
// This is what lets a submitter that knows nothing about the local platform
// submit anyway. hmd-ms-deployment can name a changeset and generate its nodes,
// but it has no idea which Docker network the platform is on, where Floci is,
// or where this environment's kubeconfig was written -- and it should not, or
// the service would be carrying local-platform knowledge that only exists to
// serve one front end. The runner reads it here instead, from the same registry
// the CLI reads.
//
// Anything the caller pinned wins, so the CLI -- which has already computed
// exactly this to reach the same cluster -- is never second-guessed.
func (s *Server) resolveConfig(envName string, override *ConfigOverride) (runner.Config, *registry.Environment, error) {
	reg, err := registry.Load(s.Home, s.Lookup)
	if err != nil {
		return runner.Config{}, nil, fmt.Errorf("reading the environment registry: %w", err)
	}
	env, err := reg.Environment(envName, s.Lookup)
	if err != nil {
		return runner.Config{}, nil, err
	}
	names := floci.NamesFrom(s.Lookup, env.DeploymentID, env.Slug)

	cfg := runner.Config{
		Network:       reg.ControlPlane.Network,
		Image:         projectBuilderRef(s.Lookup),
		FlociEndpoint: floci.InternalEndpoint,
		AccountID:     env.AccountID,
		// Container-side addresses, not the host's: inside the platform network
		// the proxy is hmd_proxy, and the environment's own services hang off
		// its route prefix.
		DeploymentServiceURL: "http://hmd_proxy/hmd_ms_deployment",
		LocalProxy:           "http://hmd_proxy/" + env.Slug,
		K3sCluster:           env.K3sCluster,
		Kubeconfig:           env.Kubeconfig,
		RepoHome:             s.Lookup("HMD_REPO_HOME"),
		DeploymentID:         env.DeploymentID,
		Region:               names.Region,
		CustomerCode:         names.CustomerCode,
		WorkDir:              s.WorkDir,
		Extra:                map[string]string{"AWS_REGION": s.Lookup("AWS_REGION")},
	}

	if override != nil {
		applyOverride(&cfg, override)
	}
	return cfg, env, nil
}

// applyOverride fills in what the submitter pinned, leaving the resolved value
// wherever it pinned nothing.
func applyOverride(cfg *runner.Config, o *ConfigOverride) {
	set(&cfg.Network, o.Network)
	set(&cfg.Image, o.Image)
	set(&cfg.FlociEndpoint, o.FlociEndpoint)
	set(&cfg.AccountID, o.AccountID)
	set(&cfg.DeploymentServiceURL, o.DeploymentServiceURL)
	set(&cfg.LocalProxy, o.LocalProxy)
	set(&cfg.K3sCluster, o.K3sCluster)
	set(&cfg.Kubeconfig, o.Kubeconfig)
	set(&cfg.RepoHome, o.RepoHome)
	set(&cfg.DeploymentID, o.DeploymentID)
	set(&cfg.Region, o.Region)
	set(&cfg.CustomerCode, o.CustomerCode)
	if len(o.RepoPaths) > 0 {
		cfg.RepoPaths = o.RepoPaths
	}
	for k, v := range o.Extra {
		if cfg.Extra == nil {
			cfg.Extra = map[string]string{}
		}
		cfg.Extra[k] = v
	}
	// WorkDir is deliberately not overridable. It has to name a path that
	// exists identically on the host and in this container, and only the
	// runner knows what it was given.
}

func set(dst *string, v string) {
	if v != "" {
		*dst = v
	}
}

// projectBuilderRef is the image every deploy node runs in. It mirrors the
// CLI's resolution rather than importing it, because internal/environment
// depends on this direction already.
func projectBuilderRef(lookup func(string) string) string {
	host := lookup("HMD_LOCAL_NS_CONTAINER_REGISTRY")
	if host == "" {
		host = "ghcr.io/neuronsphere"
	}
	version := lookup("HMD_PROJECTBUILDER_VERSION")
	if version == "" {
		version = "stable"
	}
	return fmt.Sprintf("%s/hmd-img-projectbuilder:%s", host, version)
}

// WorkflowDir is where a runner keeps its records.
func WorkflowDir(home string) string {
	return filepath.Join(home, ".cache", "nsrunner", "workflows")
}

// DefaultWorkDir is where overlay workspaces and generated scripts are written.
//
// Under HMD_HOME on purpose: these paths become bind-mount sources resolved by
// the host daemon, so they must exist at the same absolute path inside this
// container and outside it. HMD_HOME is already mounted that way; the system
// temp dir is not.
func DefaultWorkDir(home string) string {
	return filepath.Join(home, ".cache", "nsrunner", "work")
}
