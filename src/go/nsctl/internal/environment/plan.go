package environment

import (
	"context"
	"fmt"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bom"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/container"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/reconcile"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/repoclass"
)

// PlanResult is `nsctl env plan`'s answer: what `env apply` would add or
// change, whether ms-deployment would accept it, and which resource-typed
// dependencies validate_changeset would wave through but apply_changeset
// would reject.
type PlanResult struct {
	EnvSlug   string
	Substrate manifest.Substrate
	Reconcile *reconcile.Plan
	// Validation is nil when Reconcile.Empty() -- there is nothing to
	// validate when nothing would deploy.
	Validation *msdeploy.ValidateResult
	Warnings   []bom.CandidateWarning
}

// ComputePlan is the dry run behind `nsctl env plan`: it builds exactly what
// `env apply` would build, and stops before anything is applied.
//
// It reuses the same reconcile.Compute diff Apply computes, then registers
// the catalog (RegisterCatalog -- the same idempotent writes Apply's Seed
// performs before it ever builds a ChangeSet), validates the would-be
// ChangeSet definition server-side, and warns on resource-typed dependencies
// validate_changeset does not check. It never calls apply_changeset, never
// runs a node, and never writes the environment's own reconcile snapshot --
// that is an Apply-only side effect, because a plan that was never applied
// must not be mistaken later for one that was.
func ComputePlan(ctx context.Context, opts *Options, name string) (*PlanResult, error) {
	reg, err := registry.Load(opts.Home, opts.Lookup)
	if err != nil {
		return nil, nserr.Wrap(nserr.Fail, err)
	}
	env, err := reg.Environment(name, opts.Lookup)
	if err != nil {
		return nil, nserr.Wrap(nserr.Usage, err)
	}

	d := container.New()
	client, err := connectMSDeployment(ctx, opts)
	if err != nil {
		return nil, err
	}
	bomEnv, _, _ := buildBomEnv(ctx, opts, d, env)

	declared, err := manifest.Load(opts.Home, env.Slug, opts.Lookup)
	if err != nil {
		return nil, nserr.Wrap(nserr.Usage, err)
	}
	mode := declared.SubstrateMode()
	if env.LegacyLayout {
		mode = manifest.SubstrateFull
	}

	substrate := bom.SubstrateFor(bomEnv, mode, k3sEnabled(opts))
	var declaredRepos []manifest.Repo
	if declared != nil {
		declaredRepos = declared.Repos
	}
	entries := append(substrate, bom.Declared(bomEnv, declaredRepos)...)
	// The same demand-driven graph Apply appends, so a plan does not omit an
	// instance the apply it is predicting would deploy.
	if graphEnabled(opts) {
		if bom.RequiresGraph(entries) && !bom.DeclaresGraph(entries) {
			entries = append(entries, bom.Graph(bomEnv))
		}
		bom.RepointGraphDatabase(entries)
	}

	resolver := repoclass.NewWithHome(opts.lookup("HMD_REPO_HOME"), opts.Home, opts.Lookup)
	repoclass.Seed(resolver, declaredRepos)
	if err := bom.ResolveVersions(entries, resolver); err != nil {
		return nil, nserr.Wrap(nserr.Fail, err)
	}

	result := &PlanResult{EnvSlug: env.Slug, Substrate: mode}

	purged := WasPurged(opts.Home, env.DeploymentID)
	redeploy := map[string]bool{}
	status, err := client.InstanceStatus(ctx, env.DeploymentID)
	if err != nil {
		// Fail safe rather than fail closed, exactly as Apply does: an
		// unreadable graph means nothing is known, not that everything changed.
		result.Reconcile = reconcile.Degraded(entries)
		return result, nil
	}
	snapshot := reconcile.LoadSnapshot(env.StateDir)
	if purged {
		// A purge leaves the graph's DEPLOYED records describing nothing that
		// still exists; an entry with no post-purge digest would otherwise
		// read as unchanged. See Apply's identical handling.
		for _, entry := range entries {
			if _, recorded := snapshot[entry.RepoInstanceName]; !recorded {
				redeploy[entry.RepoInstanceName] = true
			}
		}
	}
	result.Reconcile = reconcile.Compute(entries, status, snapshot, redeploy)
	if result.Reconcile.Empty() {
		return result, nil
	}

	toDeploy := result.Reconcile.Deploy()
	seeder := &bom.Seeder{Client: client, Versions: resolver, Warn: opts.warn}
	if err := seeder.RegisterCatalog(ctx, bomEnv, toDeploy); err != nil {
		return nil, nserr.Wrap(nserr.Fail, fmt.Errorf("registering the catalog: %w", err))
	}

	sorted, err := bom.TopoSort(toDeploy)
	if err != nil {
		return nil, nserr.Wrap(nserr.Fail, err)
	}
	changes := make([]map[string]any, 0, len(sorted))
	for _, e := range sorted {
		changes = append(changes, map[string]any{
			"deployment_id":          e.DeploymentID,
			"repo_instance_name":     e.RepoInstanceName,
			"repo_class_name":        e.RepoClassName,
			"repo_class_version":     e.RepoClassVersion,
			"instance_configuration": e.InstanceConfiguration,
			"dependencies":           e.Dependencies,
		})
	}
	validation, err := client.ValidateChangeSet(ctx, changes)
	if err != nil {
		return nil, nserr.Wrap(nserr.Fail, fmt.Errorf("validate_changeset: %w", err))
	}
	result.Validation = validation

	warnings, err := seeder.CandidateWarnings(ctx, env.Slug, toDeploy, entries)
	if err != nil {
		opts.warn("checking resource candidates: %v", err)
	} else {
		result.Warnings = warnings
	}

	return result, nil
}
