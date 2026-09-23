package bom

import (
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
)

// A stack binds graph-db to an instance the environment is expected to provide
// -- hmd-stack-analytics binds it to `global-graph` -- and nothing in the
// substrate ever created it. ms-deployment resolves a name-bound dependency
// against RepoInstance rows, so the apply failed at register time with
//
//	AssertionError: No repo instance found for name, global-graph
//	(from resource name: global-graph)
//
// The Docker alias existed the whole time, which is what made this confusing:
// `global-graph` answered on the network and named no instance.
func TestRequiresGraphSeesEveryRoleConsumersUse(t *testing.T) {
	t.Parallel()

	for _, role := range GraphRoles {
		entries := []Entry{{
			RepoInstanceName: "trino",
			Dependencies:     map[string]any{role: GraphInstance},
		}}
		if !RequiresGraph(entries) {
			t.Errorf("a %q dependency does not ask for a graph", role)
		}
	}
}

// Lazy is the point: a graph is a JVM per environment that most local work
// never touches, so a default environment deploys none.
func TestRequiresGraphIsFalseWithoutAConsumer(t *testing.T) {
	t.Parallel()

	entries := Substrate(Environment{DeploymentID: "local"}, true)
	if RequiresGraph(entries) {
		t.Error("the bare substrate asks for a graph; a default environment must deploy none")
	}
}

// The instance name is what a bind resolves against, so it is the one value in
// here that cannot drift: `global-graph` is both the instance and the alias.
func TestGraphEntryIsNamedWhatABindTargets(t *testing.T) {
	t.Parallel()

	e := Graph(Environment{DeploymentID: "dev2", GraphContainer: "global-graph-dev2"})
	// Asserted against the literal, not only the constant: renaming the
	// constant would keep a self-comparison green while breaking every stack
	// whose manifest binds the name.
	if e.RepoInstanceName != "global-graph" {
		t.Errorf("instance name = %q, want global-graph -- what a stack binds graph-db to", e.RepoInstanceName)
	}
	if e.RepoInstanceName != GraphInstance {
		t.Errorf("instance name = %q, want GraphInstance %q", e.RepoInstanceName, GraphInstance)
	}
	if e.RepoClassName != GraphRepoClass {
		t.Errorf("repo class = %q, want %q", e.RepoClassName, GraphRepoClass)
	}
	// Scoped to the environment like every other entry: repo_instance is
	// unique by name per Environment, and GraphIdentifier keys the Neptune
	// cluster on the deployment id, so each environment gets its own.
	if e.DeploymentID != "dev2" {
		t.Errorf("deployment id = %q, want the environment's", e.DeploymentID)
	}
}

// hmd-inf-neptune declares base-vpc required (class hmd-vpc). ms-deployment
// validates required roles at changeset apply, so an entry omitting it fails
// with "required role, base-vpc, not provided" -- and it must point at the real
// producer, not at the core instance standing in for a type it does not create.
func TestGraphEntryProvidesItsRequiredVPC(t *testing.T) {
	t.Parallel()

	e := Graph(Environment{DeploymentID: "local"})
	if e.Dependencies["base-vpc"] != BaseVPCInstance {
		t.Errorf("base-vpc = %v, want %q", e.Dependencies["base-vpc"], BaseVPCInstance)
	}
}

// The host is the DNS alias the CLI attaches to the container Floci spawned,
// never Floci's Gremlin proxy: that proxy is not restored after a Floci restart,
// so connections reset while the backend container serves normally.
func TestGraphEntryAddressesTheAlias(t *testing.T) {
	t.Parallel()

	e := Graph(Environment{DeploymentID: "dev2", GraphContainer: "global-graph-dev2"})
	if e.InstanceConfiguration["graph_host"] != "global-graph-dev2" {
		t.Errorf("graph_host = %v, want the environment's alias", e.InstanceConfiguration["graph_host"])
	}
	if e.InstanceConfiguration["graph_port"] != 8182 {
		t.Errorf("graph_port = %v, want 8182", e.InstanceConfiguration["graph_port"])
	}
	// Past its ready timeout the deploy records the cluster anyway, so too
	// short a value reports a graph that is not yet serving -- and trino opens
	// the gremlin socket while loading catalogs. The script's default is 60;
	// the working environments carry 90.
	if e.InstanceConfiguration["ready_timeout"] != 90 {
		t.Errorf("ready_timeout = %v, want 90", e.InstanceConfiguration["ready_timeout"])
	}
}

// An environment registered before graph_container was recorded has none, and
// the shared alias is the right answer there rather than an empty host.
func TestGraphEntryFallsBackToTheSharedAlias(t *testing.T) {
	t.Parallel()

	e := Graph(Environment{DeploymentID: "local"})
	if e.InstanceConfiguration["graph_host"] != GraphHost {
		t.Errorf("graph_host = %v, want %q", e.InstanceConfiguration["graph_host"], GraphHost)
	}
}

// Installed plugin BOMs map these roles to the core instance, which was correct
// while the core RepoClass declared producing
// database.neuronsphere.io/graph-database to stand in for the always-on
// JanusGraph container. The producer is a real hmd-inf-neptune deploy now, so
// the core instance no longer satisfies the role. Normalised here rather than in
// each plugin, because plugins ship independently and an older one would break.
func TestRepointGraphDatabaseMovesCoreOffTheRole(t *testing.T) {
	t.Parallel()

	entries := []Entry{
		{RepoInstanceName: "airflow", Dependencies: map[string]any{"neptune": CoreInstanceName}},
		{RepoInstanceName: "trino", Dependencies: map[string]any{"graph-db": GraphInstance}},
		{RepoInstanceName: "superset", Dependencies: map[string]any{"compute": CoreInstanceName}},
	}
	RepointGraphDatabase(entries)

	if entries[0].Dependencies["neptune"] != GraphInstance {
		t.Errorf("neptune = %v, want it repointed at %q", entries[0].Dependencies["neptune"], GraphInstance)
	}
	// An explicit bind is the author's choice and is left alone.
	if entries[1].Dependencies["graph-db"] != GraphInstance {
		t.Errorf("an explicit bind was rewritten: %v", entries[1].Dependencies["graph-db"])
	}
	// Only graph roles move. compute legitimately resolves to the core.
	if entries[2].Dependencies["compute"] != CoreInstanceName {
		t.Errorf("compute = %v, want it left at the core instance", entries[2].Dependencies["compute"])
	}
}

// The graph deploys in the second changeset, like ext-secrets. The first is
// applied before the concrete core Resources are submitted, where a dependency
// carrying a tag_selector cannot resolve against a type nothing has produced --
// and unlike the substrate, the graph is deployed only on demand, so a name
// reserved for every environment would misreport what a default one deploys.
func TestTheGraphIsNotSubstrate(t *testing.T) {
	t.Parallel()

	if IsSubstrate(GraphInstance) {
		t.Error("the graph is substrate; it is demand-driven and belongs in the second changeset")
	}
	for _, name := range SubstrateNames(manifest.SubstrateFull) {
		if name == GraphInstance {
			t.Errorf("%q is listed as a substrate instance", GraphInstance)
		}
	}
}
