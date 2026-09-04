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

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/repoclass"
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
	Region       string
	// DBContainer is the DNS alias the environment's Postgres answers on.
	DBContainer string
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

// Seeder seeds a changeset and hands back the nodes to run.
type Seeder struct {
	Client   *msdeploy.Client
	Versions VersionResolver
}

// Seed registers the repo classes, applies a changeset and returns the
// ChangeSetDeployment id with the nodes to execute.
//
// The order is ms-deployment's contract, not a preference: repo class versions
// first, then the produced-resource declarations, then the Environment and
// DeploymentSet, then the ChangeSet, then apply, then generate.
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

func (s *Seeder) Seed(ctx context.Context, env Environment, entries []Entry) (string, []msdeploy.DeploymentNode, error) {
	if len(entries) == 0 {
		return "", nil, fmt.Errorf("nothing to seed")
	}
	InjectEndpoints(entries, env)

	// 1. Register each repo class version, with the dependencies and default
	// configuration read from wherever the version came from -- registering a
	// bundled artifact's version alongside a working tree's dependencies would
	// describe a build that never existed.
	for i := range entries {
		version, deps, defaultConfig, err := s.Versions.Resolve(entries[i].RepoClassName, entries[i].RepoClassVersion)
		if err != nil {
			return "", nil, fmt.Errorf("resolving %s: %w", entries[i].RepoClassName, err)
		}
		if version == "" {
			version = entries[i].RepoClassVersion
		}
		if version == "" {
			return "", nil, fmt.Errorf("no version for %s, and the BOM entry declares none", entries[i].RepoClassName)
		}
		if deps == nil {
			deps = entries[i].Dependencies
		}
		if defaultConfig == nil {
			defaultConfig = entries[i].InstanceConfiguration
		}
		if err := s.Client.APIOpTolerateExists(ctx, "add_repo_class_version", map[string]any{
			"repo_class_name":       entries[i].RepoClassName,
			"version":               version,
			"dependencies":          deps,
			"default_configuration": defaultConfig,
		}); err != nil {
			return "", nil, err
		}
		entries[i].RepoClassVersion = version
	}

	// 2. Declare what each producing RepoClass provides, before the changeset
	// applies, so a resource-typed dependency validates against a producing
	// instance rather than failing to resolve.
	if err := s.DeclareCoreProduces(ctx); err != nil {
		return "", nil, err
	}
	for _, repoClass := range producingClasses(entries) {
		defs, err := s.producedBy(repoClass)
		if err != nil {
			return "", nil, err
		}
		if len(defs) == 0 {
			continue
		}
		if err := s.DeclareProduces(ctx, repoClass, defs); err != nil {
			return "", nil, err
		}
	}

	// 3. The environment's own entities.
	if err := s.Client.EnsureEnvironment(ctx, env.Slug, env.AccountID, env.Region); err != nil {
		return "", nil, err
	}
	if err := s.Client.EnsureDeploymentSet(ctx, env.Slug, env.Slug); err != nil {
		return "", nil, err
	}

	// 4. The changeset. Entries are topologically sorted first, because
	// apply_changeset_to_environment processes them in list order and resolves
	// each entry's dependencies only against instances already added earlier in
	// the same call -- an out-of-order list fails with "No repo instance". This
	// pass makes graph *construction* succeed and is not the sort that decides
	// execution order.
	sorted, err := TopoSort(entries)
	if err != nil {
		return "", nil, err
	}
	definition := make([]map[string]any, 0, len(sorted))
	for _, e := range sorted {
		definition = append(definition, map[string]any{
			"deployment_id":          e.DeploymentID,
			"repo_instance_name":     e.RepoInstanceName,
			"repo_class_name":        e.RepoClassName,
			"repo_class_version":     e.RepoClassVersion,
			"instance_configuration": e.InstanceConfiguration,
			"dependencies":           e.Dependencies,
		})
	}
	encoded, err := msdeploy.EncodeCollection(definition)
	if err != nil {
		return "", nil, err
	}
	changeSetName, err := s.Client.NewChangeSetName(ctx, env.Slug)
	if err != nil {
		return "", nil, err
	}
	if _, err := s.Client.PutEntity(ctx, msdeploy.EntityChangeSet, map[string]any{
		"name":       changeSetName,
		"definition": encoded,
	}); err != nil {
		return "", nil, err
	}

	// 5 and 6. Apply, then ask for the ordered nodes.
	csdNid, err := s.Client.ApplyChangeSet(ctx, changeSetName, env.Slug)
	if err != nil {
		return "", nil, err
	}
	nodes, err := s.Client.GenerateLocalDeployment(ctx, csdNid)
	if err != nil {
		return csdNid, nil, err
	}
	return csdNid, nodes, nil
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
