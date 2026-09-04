package registry

import (
	"fmt"
	"path/filepath"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/container"
)

// AlreadyExistsError says an environment name is taken.
type AlreadyExistsError struct{ Name string }

func (e *AlreadyExistsError) Error() string {
	return fmt.Sprintf("environment %q already exists", e.Name)
}

// NewEnvironment builds and registers an environment, returning it.
//
// Every derived name matches env_registry._build_environment, because the two
// front ends address the same containers: an environment created by one has to
// be startable by the other, and a differently-derived container name is an
// environment neither can find.
//
// The caller must Save.
func (r *Registry) NewEnvironment(home, name string, lookup Lookup) (*Environment, error) {
	slug, err := ValidateSlug(name)
	if err != nil {
		return nil, err
	}
	if _, taken := r.Environments[slug]; taken {
		return nil, &AlreadyExistsError{Name: slug}
	}
	slot, err := r.AllocatePortSlot(slug)
	if err != nil {
		return nil, err
	}

	hash := container.HMDHomeHash(home)
	stateDir := filepath.Join(EnvironmentsRoot(home), slug)
	env := Environment{
		AccountID: r.AllocateAccountID(),
		// Empty, not absent: an environment is registered before it is
		// bootstrapped, and Bootstrapped() reads this to tell the two apart.
		Bootstrap: map[string]any{},
		// The instance name is identical in every environment: repo_instance
		// is unique by name per Environment, so `local-neuronsphere` in dev2
		// is a different instance from the one in local. This matches the
		// cloud.
		CoreInstanceName: "local-neuronsphere",
		DBContainer:      "hmd_db-" + slug,
		DeploymentID:     slug,
		GraphContainer:   "global-graph-" + slug,
		Kubeconfig:       filepath.Join(stateDir, "k3s", "kubeconfig"),
		Name:             slug,
		PortBase:         portBase(lookup),
		PortSlot:         slot,
		Slug:             slug,
		StateDir:         stateDir,
	}
	if hash != "" {
		env.ComposeProject = "ns-" + hash + "-env-" + slug
		env.K3sCluster = "ns-" + slug + "-" + hash
	} else {
		env.ComposeProject = "ns-env-" + slug
		env.K3sCluster = "ns-" + slug
	}

	if r.Environments == nil {
		r.Environments = map[string]Environment{}
	}
	r.Environments[slug] = env
	if r.DefaultEnv == "" {
		r.DefaultEnv = slug
	}
	return &env, nil
}

// RemoveEnvironment unregisters an environment.
//
// The registry entry only. Containers, volumes and state directories are the
// caller's to deal with -- an unregistered environment whose containers are
// still running is worse than either state alone, so a caller that cannot
// clean up should refuse rather than call this.
func (r *Registry) RemoveEnvironment(name string, lookup Lookup) error {
	env, err := r.Environment(name, lookup)
	if err != nil {
		return err
	}
	delete(r.Environments, env.Slug)
	if r.DefaultEnv == env.Slug {
		// Promote whichever remains, deterministically, so the registry never
		// names a default that is not there.
		r.DefaultEnv = ""
		if names := r.Names(); len(names) > 0 {
			r.DefaultEnv = names[0]
		}
	}
	return nil
}
