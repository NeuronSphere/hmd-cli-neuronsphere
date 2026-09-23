// Package bom seeds the deployment graph and hands back the ordered nodes to
// run.
//
// nsctl ships the environment *substrate* and nothing above it: the core
// instance, the environment's Postgres, its k3s cluster and its dbaccount.
// Every workload -- airflow, argo, transform, trino, superset and the rest --
// is a RepoClass a user adds, declared in the environment manifest. That is the
// whole difference from bom_seeder.py, which carries a built-in catalogue.
package bom

import (
	"context"
	"fmt"
	"sort"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/repoclass"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/runner"
)

// The substrate. These four are what make an environment an environment rather
// than a bare Floci account.
const (
	// CoreInstanceName is identical in every environment: repo_instance is
	// unique by name per Environment, so this is not ambiguous. It matches the
	// cloud.
	CoreInstanceName = "local-neuronsphere"
	CoreRepoClass    = "hmd-cli-neuronsphere"

	// BaseVPCInstance produces the environment's network:  a VPC, its subnets
	// and the DB subnet group instances are placed into.
	//
	// It replaces an imperative workaround. The CLI used to create these
	// directly, because Floci's Ec2Service.ensureDefaultResources guards on
	// region alone while its VPC storage is per-account -- so the first account
	// to touch EC2 marks the region seeded and every other account is left with
	// no default VPC, and RDS then fails CreateDBInstance with
	// "InvalidVPCNetworkStateFault: No subnets available for DB subnet group
	// default". Deploying the repo that owns VPCs is the same fix expressed as
	// the platform expresses everything else, and it is what makes base-vpc
	// resolve to a real producing RepoInstance rather than to the core instance
	// declaring a type it does not create.
	BaseVPCInstance  = "base-vpc"
	BaseVPCRepoClass = "hmd-vpc"

	// EnvDBInstance produces the environment's
	// database.neuronsphere.io/postgres Resource, which is what every
	// database-instance dependency resolves against -- hmd-database-account's
	// above all. It is in the core changeset because ms-dbaccount and every
	// user database depend on it.
	EnvDBInstance  = "environment-db"
	EnvDBRepoClass = "hmd-postgres-rds"

	// EKSClusterInstance is deployed as a real DAG node so the same repo
	// produces kubernetes.neuronsphere.io/kubernetes-cluster in the cloud and
	// locally -- through terraform-aws-modules/eks there and its src/local/cdktf
	// overlay here. Everything about the Docker container Floci spawns in
	// response stays with nsctl: none of it is visible to Terraform.
	EKSClusterInstance  = "eks-cluster"
	EKSClusterRepoClass = "hmd-inf-eks-cluster"
)

// substrateInstanceByClass maps a substrate repo class to the instance name
// this environment deploys it as.
//
// Needed because the same class is named differently elsewhere. A cloud
// environment calls its cluster whatever its operators called it -- hmdtr1's
// dev runs hmd-inf-eks-cluster as "eks-upg" -- while every local environment
// deploys it as "eks-cluster". Matching on the name alone would treat the cloud
// instance as something to import, when it is the very thing the substrate
// already provides.
var substrateInstanceByClass = map[string]string{
	CoreRepoClass:       CoreInstanceName,
	BaseVPCRepoClass:    BaseVPCInstance,
	EnvDBRepoClass:      EnvDBInstance,
	EKSClusterRepoClass: EKSClusterInstance,
}

// SubstrateInstanceFor names the local instance of a substrate repo class, and
// reports whether that class is substrate at all.
func SubstrateInstanceFor(repoClass string) (string, bool) {
	name, ok := substrateInstanceByClass[repoClass]
	return name, ok
}

// LocalDBSubnetGroup is the named group the local CDKTF overlay places
// instances in, working around Floci's per-account VPC seeding bug.
const LocalDBSubnetGroup = "hmd-local-db-subnets"

// Entry is one BOM entry: an instance of a repo class in an environment.
type Entry struct {
	RepoInstanceName      string         `json:"repo_instance_name"`
	RepoClassName         string         `json:"repo_class_name"`
	RepoClassVersion      string         `json:"repo_class_version,omitempty"`
	DeploymentID          string         `json:"deployment_id"`
	InstanceConfiguration map[string]any `json:"instance_configuration"`
	// Dependencies maps a role to an instance name, or to a list of them --
	// both shapes the change_set schema allows.
	Dependencies map[string]any `json:"dependencies"`
}

// Environment is what the seeder needs to know about the target environment.
type Environment struct {
	Slug         string
	DeploymentID string
	AccountID    string
	// AccessKeyID is what a request signs with, and therefore which emulated
	// account Floci resolves it in. It defaults to AccountID and is carried
	// separately because they are different ideas: signing with the wrong key
	// does not fail, it succeeds against the wrong account.
	AccessKeyID string
	Region      string
	// DBContainer is the DNS alias the environment's Postgres answers on.
	DBContainer string
	// GraphContainer is the DNS alias the environment's graph answers on, where
	// it has one of its own. Empty falls back to the shared GraphHost.
	GraphContainer string
	// K3sCluster and K3sContainer name the cluster and the container Floci
	// spawned for it.
	K3sCluster   string
	K3sContainer string
}

// Substrate is the BOM nsctl ships: the core instance, the environment's
// Postgres and its k3s cluster.
//
// Every dependency a RepoClassVersion marks required must be supplied even
// where the local overlay references none of them, because ms-deployment
// validates required *roles* at changeset apply rather than at deploy -- the
// failure is "required role, rds-loggroup, not provided". base-vpc is
// resource-typed, so the core instance genuinely declares producing
// network.neuronsphere.io/vpc; datadog-lambda and rds-loggroup are name-only
// roles nothing validates beyond presence, and the overlay creates neither.
func Substrate(env Environment, withCluster bool) []Entry {
	entries := []Entry{
		{
			RepoInstanceName:      CoreInstanceName,
			RepoClassName:         CoreRepoClass,
			DeploymentID:          env.DeploymentID,
			InstanceConfiguration: map[string]any{},
			Dependencies:          map[string]any{},
		},
		{
			RepoInstanceName:      BaseVPCInstance,
			RepoClassName:         BaseVPCRepoClass,
			DeploymentID:          env.DeploymentID,
			InstanceConfiguration: map[string]any{},
			Dependencies:          map[string]any{},
		},
		{
			RepoInstanceName: EnvDBInstance,
			RepoClassName:    EnvDBRepoClass,
			DeploymentID:     env.DeploymentID,
			InstanceConfiguration: map[string]any{
				"db_subnet_group_name": LocalDBSubnetGroup,
			},
			Dependencies: map[string]any{
				// A real producer now, which is also what orders the VPC ahead
				// of the database that needs its subnet group.
				"base-vpc":       BaseVPCInstance,
				"datadog-lambda": CoreInstanceName,
				"rds-loggroup":   CoreInstanceName,
			},
		},
	}
	if withCluster {
		entries = append(entries, Entry{
			RepoInstanceName:      EKSClusterInstance,
			RepoClassName:         EKSClusterRepoClass,
			DeploymentID:          env.DeploymentID,
			InstanceConfiguration: map[string]any{},
			Dependencies: map[string]any{
				// hmd-inf-eks-cluster's local overlay looks the account's
				// subnets up by boto3 and its docstring notes it depends on
				// something else having seeded them first. That was true only
				// by execution order; declaring the dependency makes it so.
				"base-vpc":       BaseVPCInstance,
				"datadog-lambda": CoreInstanceName,
			},
		})
	}
	return entries
}

// InjectEndpoints fills in the per-environment values that cannot be constants.
//
// The database's host is the DNS alias, not Floci's published endpoint: Floci
// publishes an RDS instance on its own IP from the 7001-7099 proxy range, and
// that proxy is not restored when Floci restarts -- connections are reset while
// the backend container keeps serving. The alias is what ms-dbaccount reaches
// it by, and the admin secret's host is its only route there.
//
// The cluster's endpoint uses the account-qualified container name, resolved
// once here so the local CDKTF overlay never reimplements the naming rule -- it
// only reads cluster_name and cluster_endpoint off instance_configuration.
func InjectEndpoints(entries []Entry, env Environment) {
	for i := range entries {
		switch entries[i].RepoInstanceName {
		case EnvDBInstance:
			if env.DBContainer == "" {
				continue
			}
			config := copyConfig(entries[i].InstanceConfiguration)
			config["db_host"] = env.DBContainer
			config["db_port"] = 5432
			entries[i].InstanceConfiguration = config
		case EKSClusterInstance:
			config := copyConfig(entries[i].InstanceConfiguration)
			config["cluster_name"] = env.K3sCluster
			if env.K3sContainer != "" {
				config["cluster_endpoint"] = "https://" + env.K3sContainer + ":6443"
			}
			entries[i].InstanceConfiguration = config
		}
	}
}

func copyConfig(in map[string]any) map[string]any {
	out := make(map[string]any, len(in)+2)
	for k, v := range in {
		out[k] = v
	}
	return out
}

// VersionResolver reports the version and metadata to register a repo class
// under.
// DependencyTargets flattens a dependency mapping to the instance names it
// names, in either of the two shapes the schema allows.
func DependencyTargets(deps map[string]any) []string {
	out := make([]string, 0, len(deps))
	for _, target := range deps {
		switch t := target.(type) {
		case string:
			out = append(out, t)
		case []any:
			for _, one := range t {
				if name, ok := one.(string); ok {
					out = append(out, name)
				}
			}
		case []string:
			out = append(out, t...)
		}
	}
	return out
}

type VersionResolver interface {
	// Resolve reports the version to register a repo class under, plus the
	// dependencies and default configuration read from wherever that version
	// came from. declared is the version the BOM entry asked for, which a
	// manifest can pin and which loses to a working tree.
	Resolve(repoClass, declared string) (version string, dependencies map[string]any, defaultConfig map[string]any, err error)
	// Produces reads the Resource types a repo class declares it emits, from
	// its own meta-data/resources/*.yaml. Nothing declared is not an error --
	// most repos declare none.
	Produces(repoClass string) ([]repoclass.ResourceDeclaration, error)
}

// DiscoveryResolver is the optional half of VersionResolver: the BACON
// discovery block to register with a version (NERD0013 SPEC0005 in
// hmd-ms-deployment). Optional so a resolver that has no tree to read -- or
// predates the block -- still seeds; a nil result leaves the key off the
// registration and the service default applies.
type DiscoveryResolver interface {
	ResolveDiscovery(repoClass, declared string) (map[string]any, error)
}

// Seeder registers the catalog, records the plan and hands back the nodes to
// run.
type Seeder struct {
	Client   *msdeploy.Client
	Versions VersionResolver
	// Region is the hmd region the scripts deploy into; empty means the
	// runner's default.
	Region string
	// Warn reports something a seed carried on past. Optional; nil discards.
	Warn func(format string, a ...any)
}

func (s *Seeder) warn(format string, a ...any) {
	if s.Warn != nil {
		s.Warn(format, a...)
	}
}

// Seed registers the repo classes, records the plan and returns the nodes to
// execute.
//
// The order is ms-deployment's contract, not a preference: repo class versions
// first, then the resource-type definitions, then the produced-resource
// declarations, then the Environment and DeploymentSet, then the instances in
// dependency order, then each instance's configuration.
// ResolveVersions fills in each entry's RepoClassVersion.
//
// Called before the reconcile plan is computed, not left to Seed: the version
// is part of what an entry's digest covers, so a plan computed against
// version-less entries cannot see a version bump as drift, and the digest it
// records would not match the one the Python front end writes for the same
// entry.
func ResolveVersions(entries []Entry, versions VersionResolver) error {
	for i := range entries {
		version, _, _, err := versions.Resolve(entries[i].RepoClassName, entries[i].RepoClassVersion)
		if err != nil {
			return fmt.Errorf("resolving %s: %w", entries[i].RepoClassName, err)
		}
		if version != "" {
			entries[i].RepoClassVersion = version
		}
	}
	return nil
}

// RegisterCatalog performs the changeset-independent catalog writes Seed needs
// before it ever builds a ChangeSet: repo class version registration, resource
// type registration, produced-resource declarations, and the environment's own
// entities. Every write is idempotent -- add_repo_class_version tolerates
// "already exists", declare_produces is deduped server-side, and
// EnsureEnvironment/EnsureDeploymentSet find-first -- so this is safe to call
// speculatively. That is exactly what `nsctl env plan` needs: a dry run's
// validate_changeset and suggest_resource_dependencies calls have to resolve
// against real registered data, without ever building or applying a ChangeSet.
func (s *Seeder) RegisterCatalog(ctx context.Context, env Environment, entries []Entry) error {
	InjectEndpoints(entries, env)

	// 1. Register each repo class version, with the dependencies and default
	// configuration read from wherever the version came from -- registering a
	// bundled artifact's version alongside a working tree's dependencies would
	// describe a build that never existed.
	for i := range entries {
		version, deps, defaultConfig, err := s.Versions.Resolve(entries[i].RepoClassName, entries[i].RepoClassVersion)
		if err != nil {
			return fmt.Errorf("resolving %s: %w", entries[i].RepoClassName, err)
		}
		if version == "" {
			version = entries[i].RepoClassVersion
		}
		if version == "" {
			return fmt.Errorf("no version for %s, and the BOM entry declares none", entries[i].RepoClassName)
		}
		if deps == nil {
			deps = entries[i].Dependencies
		}
		if defaultConfig == nil {
			defaultConfig = entries[i].InstanceConfiguration
		}
		payload := map[string]any{
			"repo_class_name":       entries[i].RepoClassName,
			"version":               version,
			"dependencies":          deps,
			"default_configuration": defaultConfig,
		}
		// The discovery block rides along when the resolver can read one from
		// the same tree. Failing to read it is not failing to register: the
		// version and its dependencies resolved, and a catalog entry without a
		// summary is the state every version was in before this existed.
		if dr, ok := s.Versions.(DiscoveryResolver); ok {
			discovery, err := dr.ResolveDiscovery(entries[i].RepoClassName, entries[i].RepoClassVersion)
			if err != nil {
				s.warn("reading %s's discovery metadata: %v", entries[i].RepoClassName, err)
			} else if len(discovery) > 0 {
				payload["discovery"] = discovery
			}
		}
		if err := s.Client.APIOpTolerateExists(ctx, "add_repo_class_version", payload); err != nil {
			return err
		}
		entries[i].RepoClassVersion = version
	}

	// 2. Register the resource types before anything claims to produce one.
	// The base catalog first, because a repo's own definition parents onto it;
	// then each producing repo's own declarations. Skipping this worked only
	// against a graph something else had already seeded.
	if err := s.SeedBaseResourceDefinitions(ctx); err != nil {
		return fmt.Errorf("seeding the base resource definitions: %w", err)
	}
	for _, repoClass := range producingClasses(entries) {
		if err := s.UpsertResourceDefinitions(ctx, repoClass); err != nil {
			return fmt.Errorf("reading the resource types %s declares: %w", repoClass, err)
		}
	}

	// 3. Declare what each producing RepoClass provides, before the changeset
	// applies, so a resource-typed dependency validates against a producing
	// instance rather than failing to resolve.
	//
	// The core's declaration needs its RepoClassVersion, which step 1
	// registers only when the core instance is in the changeset. Under a
	// substrate without one (NERD014) it never is, and an environment that
	// deploys no core has nothing to declare for it -- so the declaration is
	// skipped when the core is neither here nor already registered, rather
	// than failing every changeset the mode was chosen for.
	if coreIn(entries) || s.coreRegistered(ctx) {
		if err := s.DeclareCoreProduces(ctx); err != nil {
			return err
		}
	}
	for _, repoClass := range producingClasses(entries) {
		defs, err := s.producedBy(repoClass)
		if err != nil {
			return err
		}
		if len(defs) == 0 {
			continue
		}
		if err := s.DeclareProduces(ctx, repoClass, defs); err != nil {
			return err
		}
	}

	// 4. The environment's own entities.
	if err := s.Client.EnsureEnvironment(ctx, env.Slug, env.AccountID, env.Region); err != nil {
		return err
	}
	if err := s.Client.EnsureDeploymentSet(ctx, env.Slug, env.Slug); err != nil {
		return err
	}
	return nil
}

// Seed registers the catalog (RegisterCatalog), records every entry as a
// DEPLOY_NEXT RepoInstanceDeployment with its dependency edges, fetches each
// one's resolved configuration and renders the node to run (NERD0015).
//
// No ChangeSet is involved: that is the orchestrator's premium path, and the
// core image the local control plane runs has none of it. What the service
// still owns is the graph -- register_deployed_instance creates the same edge
// set apply_changeset did, and get_deployment_config resolves dependencies
// against it -- so the client never writes RepoInstance rows itself.
func (s *Seeder) Seed(ctx context.Context, env Environment, entries []Entry) ([]msdeploy.DeploymentNode, error) {
	if len(entries) == 0 {
		return nil, fmt.Errorf("nothing to seed")
	}
	if err := s.RegisterCatalog(ctx, env, entries); err != nil {
		return nil, err
	}

	// 5. The instances, dependencies first: register_deployed_instance resolves
	// an entry's dependencies only against instances already in the
	// environment, so an out-of-order list fails with "No repo instance".
	sorted, err := TopoSort(entries)
	if err != nil {
		return nil, err
	}
	batch := make(map[string]bool, len(sorted))
	for _, e := range sorted {
		batch[e.RepoInstanceName] = true
	}
	rids := make(map[string]string, len(sorted))
	for _, e := range sorted {
		rid, err := s.Client.RegisterDeployedInstance(ctx, msdeploy.RegisterInstance{
			Environment:           env.Slug,
			RepoClassName:         e.RepoClassName,
			Version:               e.RepoClassVersion,
			InstanceName:          e.RepoInstanceName,
			DeploymentID:          e.DeploymentID,
			InstanceConfiguration: e.InstanceConfiguration,
			Dependencies:          e.Dependencies,
			Status:                msdeploy.StatusDeployNext,
		})
		if err != nil {
			return nil, fmt.Errorf("registering %s: %w", e.RepoInstanceName, err)
		}
		rids[e.RepoInstanceName] = rid
	}

	// 6. Every instance is recorded, so every dependency now resolves -- to
	// baked resources for what is already deployed, to a `hmd_resource_ref`
	// for what is in this batch and still DEPLOY_NEXT, which hmd-cli-deploy
	// resolves just before it runs.
	nodes := make([]msdeploy.DeploymentNode, 0, len(sorted))
	for _, e := range sorted {
		config, err := s.Client.DeploymentConfig(ctx, env.Slug, e.RepoInstanceName)
		if err != nil {
			return nil, fmt.Errorf("resolving the configuration of %s: %w", e.RepoInstanceName, err)
		}
		script, err := runner.DeployScript(runner.ScriptParams{
			RepoClass: e.RepoClassName, Instance: e.RepoInstanceName, Version: e.RepoClassVersion,
			DeploymentID: e.DeploymentID, Region: s.Region, Environment: env.Slug,
			RID: rids[e.RepoInstanceName], Config: config,
		})
		if err != nil {
			return nil, err
		}
		var deps []string
		for _, target := range DependencyTargets(e.Dependencies) {
			if batch[target] && target != e.RepoInstanceName {
				deps = append(deps, target)
			}
		}
		nodes = append(nodes, msdeploy.DeploymentNode{
			InstanceName:  e.RepoInstanceName,
			RepoClassName: e.RepoClassName,
			Version:       e.RepoClassVersion,
			RIDNid:        rids[e.RepoInstanceName],
			Script:        script,
			Dependencies:  deps,
		})
	}
	return nodes, nil
}

// TopoSort orders entries so every entry follows the entries it depends on.
//
// This is the CLI-side sort, and it exists for graph construction rather than
// execution: apply_changeset_to_environment resolves an entry's dependencies
// only against instances already added earlier in the same call. The
// service-side traversal that decides execution order is a different sort
// entirely.
//
// A dependency on an instance the BOM does not declare is left alone rather
// than treated as an error: it may already exist in the graph from an earlier
// changeset, which is the normal case for a delta apply.
func TopoSort(entries []Entry) ([]Entry, error) {
	byName := make(map[string]Entry, len(entries))
	var order []string
	for _, e := range entries {
		if _, dup := byName[e.RepoInstanceName]; dup {
			continue
		}
		byName[e.RepoInstanceName] = e
		order = append(order, e.RepoInstanceName)
	}

	state := map[string]int{} // 0 unvisited, 1 in progress, 2 done
	var out []Entry
	var visit func(name string, path []string) error
	visit = func(name string, path []string) error {
		switch state[name] {
		case 2:
			return nil
		case 1:
			return fmt.Errorf("the BOM has a dependency cycle: %v", append(path, name))
		}
		state[name] = 1
		entry := byName[name]

		deps := DependencyTargets(entry.Dependencies)
		// Deterministic, so the same BOM always produces the same order.
		sort.Strings(deps)
		for _, target := range deps {
			if target == name {
				continue
			}
			if _, declared := byName[target]; !declared {
				continue
			}
			if err := visit(target, append(path, name)); err != nil {
				return err
			}
		}
		state[name] = 2
		out = append(out, entry)
		return nil
	}

	for _, name := range order {
		if err := visit(name, nil); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// declares reports whether the BOM includes an entry of a repo class.
// producedBy is what a repo class declares it produces, read from the repo.
//
// The hardcoded list is a fallback for the one case it can be needed: a repo
// whose working tree is not on this machine, where there is no file to read.
// It is never preferred over the repo's own declaration -- that is how the two
// drifted, the list naming a different namespace than hmd-vpc's own file did.
func (s *Seeder) producedBy(repoClass string) ([]ProducedDefinition, error) {
	declarations, err := s.Versions.Produces(repoClass)
	if err != nil {
		return nil, fmt.Errorf("reading what %s produces: %w", repoClass, err)
	}
	if defs := FromDeclarations(declarations); len(defs) > 0 {
		return defs, nil
	}
	return fallbackProduces[repoClass], nil
}

// producingClasses is every repo class in the BOM, deduplicated, in the order
// the entries name them.
func producingClasses(entries []Entry) []string {
	var classes []string
	seen := map[string]bool{}
	for _, e := range entries {
		if e.RepoClassName == "" || seen[e.RepoClassName] {
			continue
		}
		seen[e.RepoClassName] = true
		classes = append(classes, e.RepoClassName)
	}
	return classes
}

func declares(entries []Entry, repoClass string) bool {
	for _, e := range entries {
		if e.RepoClassName == repoClass {
			return true
		}
	}
	return false
}

// SubstrateFor is Substrate composed per mode (NERD014 SPEC004): none yields no
// entries, core the three that need no cluster, full what Substrate yields.
//
// withCluster is ANDed with the mode rather than replaced by it. It is an
// environment variable people have set, and full with k3s disabled means what
// it meant before modes existed: a full environment whose cluster is elsewhere.
func SubstrateFor(env Environment, mode manifest.Substrate, withCluster bool) []Entry {
	switch mode {
	case manifest.SubstrateNone:
		return nil
	case manifest.SubstrateCore:
		return Substrate(env, false)
	default:
		return Substrate(env, withCluster)
	}
}

// SubstrateNames lists the instances a mode deploys, in deploy order. What
// `repo list` prints as substrate rows; IsSubstrate is deliberately not
// consulted, because a name is reserved whether or not this environment
// deploys it.
func SubstrateNames(mode manifest.Substrate) []string {
	var out []string
	for _, e := range SubstrateFor(Environment{}, mode, true) {
		out = append(out, e.RepoInstanceName)
	}
	return out
}

// coreIn reports whether the changeset deploys the core instance.
func coreIn(entries []Entry) bool {
	for _, e := range entries {
		if e.RepoInstanceName == CoreInstanceName {
			return true
		}
	}
	return false
}

// coreRegistered reports whether an earlier changeset registered the core's
// RepoClassVersion, in which case its produced types are declared as before.
func (s *Seeder) coreRegistered(ctx context.Context) bool {
	id, err := s.repoClassVersionID(ctx, CoreRepoClass)
	return err == nil && id != ""
}

// substrateInstances is what Substrate names, as a set.
var substrateInstances = map[string]bool{
	CoreInstanceName:   true,
	BaseVPCInstance:    true,
	EnvDBInstance:      true,
	EKSClusterInstance: true,
}

// IsSubstrate reports whether an instance is part of the substrate rather than
// something declared on top of it.
//
// The two are applied as separate changesets, so this is what decides which
// phase an entry belongs to.
func IsSubstrate(instanceName string) bool { return substrateInstances[instanceName] }
