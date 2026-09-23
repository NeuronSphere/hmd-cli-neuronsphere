package bom

// The graph database.
//
// Provisioned lazily, mirroring bom_seeder's graph_bom_entry and the three
// helpers around it: a default environment deploys no graph at all. It used to
// run unconditionally as a compose container in every environment, which was a
// JVM per environment that most local work never touches.
//
// The reason this needs an entry at all, rather than riding on the alias: a
// stack binds graph-db to an instance it expects the environment to provide --
// hmd-stack-analytics binds it to `global-graph` -- and ms-deployment resolves a
// name-bound dependency against RepoInstance rows. `global-graph` was only ever
// a Docker network alias, so it answered on the network while naming no
// instance, and the apply failed at register time with
//
//	AssertionError: No repo instance found for name, global-graph
//	(from resource name: global-graph)
//
// startPlan.Graph is not that entry and never was: it restarts a container an
// earlier deploy already spawned, which is why the gap was invisible until
// something bound the role.
const (
	GraphInstance  = "global-graph"
	GraphRepoClass = "hmd-inf-neptune"
)

// GraphHost is the alias consumers address, unchanged from the compose era:
// they still open ws://global-graph:8182/gremlin. Mirrors k3s.GraphHost, which
// writes the CoreDNS record pods resolve it through; not imported, because this
// package is pure data about a BOM.
const GraphHost = "global-graph"

// GraphEnabledEnv opts out, matching the Python. Unset means "only if something
// needs it", which is the point of making this lazy -- so unlike
// ExtSecretsEnabledEnv this is not simply default-on.
//
// Set false and a consumer that asks for a graph then fails to resolve its
// dependency, which is the intended, visible outcome of turning it off.
const GraphEnabledEnv = "HMD_LOCAL_NEURONSPHERE_ENABLE_GRAPH"

// GraphRoles are the roles consumers ask for a
// database.neuronsphere.io/graph-database under.
//
// Matching on the role rather than re-reading every repo's manifest keeps this
// to the assembled BOM, which is the only thing available at seed time. Repos
// aren't consistent about the name: trino uses "graph-db", transform
// "neptune-db", airflow "neptune".
var GraphRoles = []string{"graph-db", "neptune-db", "neptune"}

// RequiresGraph reports whether anything in entries declares a dependency on a
// graph database.
//
// The role's presence is the question, not what it points at: a bind names an
// instance this environment is expected to provide, and providing it is exactly
// what is being decided here.
func RequiresGraph(entries []Entry) bool {
	for _, e := range entries {
		for _, role := range GraphRoles {
			if _, ok := e.Dependencies[role]; ok {
				return true
			}
		}
	}
	return false
}

// DeclaresGraph reports whether entries already carry the graph instance.
//
// Every environment the Python CLI created declares it explicitly -- with its
// own engine_version and ready_timeout -- so on a developer machine the entry
// below is usually already there. TopoSort would dedupe by instance name and
// keep that first declaration, which is the ext-secrets shadowing rule and the
// right outcome; this guard is so the duplicate never reaches ResolveVersions
// and reconcile in the first place, where two entries under one name would
// double-count the plan and muddle its digest.
func DeclaresGraph(entries []Entry) bool {
	for _, e := range entries {
		if e.RepoInstanceName == GraphInstance {
			return true
		}
	}
	return false
}

// Graph is the entry that provisions this environment's graph.
//
// Its Neptune cluster identifier is floci.GraphIdentifier, keyed on the
// deployment id, so each environment gets its own container -- and
// hmd-inf-neptune's src/local/deploy_local.sh is idempotent on that identifier,
// so a repeat apply is a no-op rather than a second cluster.
func Graph(env Environment) Entry {
	host := env.GraphContainer
	if host == "" {
		// An environment registered before graph_container was recorded has
		// none, and the shared alias is a better answer than an empty host.
		host = GraphHost
	}
	return Entry{
		RepoInstanceName: GraphInstance,
		RepoClassName:    GraphRepoClass,
		DeploymentID:     env.DeploymentID,
		InstanceConfiguration: map[string]any{
			// Addressed by the DNS alias the CLI attaches to the Floci-spawned
			// container, never Floci's Gremlin proxy -- that proxy is not
			// restored after a Floci restart, so connections are reset while
			// the backend container keeps serving normally.
			"graph_host": host,
			"graph_port": 8182,
			// deploy_local.sh defaults this to 60 and, past it, records the
			// cluster anyway with a warning rather than failing. A gremlin JVM
			// cold start can exceed that, and the cluster then reads available
			// with nothing serving behind it -- which is how trino, who opens
			// the gremlin socket while loading catalogs, fails its readiness
			// probe and gets rolled back reporting only "context deadline
			// exceeded". 90 is what the working Python-authored environments
			// carry. engine_version is left to the script, whose default is
			// the same 1.4.7.0 those environments name explicitly.
			"ready_timeout": 90,
		},
		Dependencies: map[string]any{
			// hmd-inf-neptune declares base-vpc required, so omitting it fails
			// the changeset at apply with "required role, base-vpc, not
			// provided". The real producer, not the core instance standing in
			// for a type it does not create -- which is also what orders the
			// VPC ahead of it.
			"base-vpc": BaseVPCInstance,
		},
	}
}

// RepointGraphDatabase points graph dependencies at the real producer instead
// of the core instance. Mutates entries in place.
//
// Installed plugin BOMs map these roles to CoreInstanceName, which was correct
// while the core RepoClass declared producing
// database.neuronsphere.io/graph-database to stand in for the always-on
// JanusGraph container. That producer is a real hmd-inf-neptune deploy now, so
// the core instance no longer satisfies the role. Normalised here rather than in
// each plugin for the same reason the Python does it centrally: plugins ship as
// independent packages, and an older installed one would otherwise break.
//
// An explicit bind to anything else is the author's choice and is left alone.
func RepointGraphDatabase(entries []Entry) {
	for i := range entries {
		for _, role := range GraphRoles {
			if entries[i].Dependencies[role] == CoreInstanceName {
				entries[i].Dependencies[role] = GraphInstance
			}
		}
	}
}
