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
	// Normalised here as Load and Environment do. portBase calls this
	// unguarded, so a nil lookup used to panic rather than take the defaults it
	// plainly means.
	if lookup == nil {
		lookup = func(string) string { return "" }
	}
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
		RouterContainer:  "hmd_router-" + slug,
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

// EnsureFirstEnvironment registers and persists an environment when this
// registry holds none at all. It reports whether it created one.
//
// The name is the one asked for, or HMD_LOCAL_ENV, or DefaultEnv -- the same
// order Environment resolves in, so `env start` and `env start <name>` create
// exactly the environment they would then have gone looking for.
//
// The guard is `len(r.Environments) == 0` and nothing else. Environment's rule
// that a missing environment is an error rather than a creation is there to
// stop a typo becoming a second environment alongside the one that was meant;
// with nothing registered there is no such hazard, and the alternative is a
// fresh install that runs an entire control-plane bootstrap and then refuses,
// with a fix the user could not have known to run first.
//
// Persisted here rather than left to the caller: an environment that exists in
// memory and not on disk is one whose containers the next invocation cannot
// name, and every caller would have to remember to Save.
func (r *Registry) EnsureFirstEnvironment(home, name string, lookup Lookup) (*Environment, bool, error) {
	if len(r.Environments) > 0 {
		return nil, false, nil
	}
	if lookup == nil {
		lookup = func(string) string { return "" }
	}
	if name == "" {
		name = lookup("HMD_LOCAL_ENV")
	}
	if name == "" {
		name = r.DefaultEnv
	}
	if name == "" {
		name = DefaultEnvName
	}

	env, err := r.NewEnvironment(home, name, lookup)
	if err != nil {
		return nil, false, err
	}
	// NewEnvironment only fills DefaultEnv when it was empty. applyDefaults
	// always sets it -- to `local` -- so on a synthesized registry it is
	// already populated and would name an environment that does not exist.
	r.DefaultEnv = env.Slug
	if err := r.Save(home); err != nil {
		return nil, false, err
	}
	return env, true, nil
}
