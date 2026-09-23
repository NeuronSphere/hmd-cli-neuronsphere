package environment

import (
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bom"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
)

// Demand decides, so unset means enabled -- unlike ext-secrets, where unset
// means "on regardless". This flag only ever takes the graph away.
func TestGraphEnabledDefaultsToDemand(t *testing.T) {
	t.Parallel()

	if !graphEnabled(testOptions(t.TempDir(), nil)) {
		t.Error("graphEnabled is false with the variable unset; demand is what decides")
	}
	for _, off := range []string{"false", "FALSE", "0", "no", " no "} {
		opts := testOptions(t.TempDir(), map[string]string{bom.GraphEnabledEnv: off})
		if graphEnabled(opts) {
			t.Errorf("%s=%q did not turn the graph off", bom.GraphEnabledEnv, off)
		}
	}
	// Anything else is on: a typo must not silently remove a graph something
	// depends on.
	opts := testOptions(t.TempDir(), map[string]string{bom.GraphEnabledEnv: "true"})
	if !graphEnabled(opts) {
		t.Error("an explicit true turned the graph off")
	}
}

// composeEntries is the composition Apply performs between assembling the
// substrate and resolving versions. Held here rather than reached through Apply,
// which needs Docker and a live ms-deployment.
func composeEntries(opts *Options, env bom.Environment, repos []manifest.Repo) []bom.Entry {
	entries := append(bom.SubstrateFor(env, manifest.SubstrateFull, true), bom.Declared(env, repos)...)
	if graphEnabled(opts) {
		if bom.RequiresGraph(entries) && !bom.DeclaresGraph(entries) {
			entries = append(entries, bom.Graph(env))
		}
		bom.RepointGraphDatabase(entries)
	}
	return entries
}

func hasInstance(entries []bom.Entry, name string) bool {
	for _, e := range entries {
		if e.RepoInstanceName == name {
			return true
		}
	}
	return false
}

// The regression this exists for. hmd-stack-analytics binds graph-db to
// `global-graph`, which is the shape `nsctl stack init --from-env` produces, and
// nothing ever created that instance -- so the apply got as far as trino and
// ms-deployment answered
//
//	AssertionError: No repo instance found for name, global-graph
//
// reaching nsctl only as "registering trino: HTTP 500".
func TestABoundGraphRoleGetsAnInstanceToResolveAgainst(t *testing.T) {
	t.Parallel()

	env := bom.Environment{DeploymentID: "local", GraphContainer: "global-graph-local"}
	repos := []manifest.Repo{{
		InstanceName:  "trino",
		RepoClassName: "hmd-inf-trino",
		Dependencies:  map[string]any{"graph-db": bom.GraphInstance},
	}}

	entries := composeEntries(testOptions(t.TempDir(), nil), env, repos)
	if !hasInstance(entries, bom.GraphInstance) {
		t.Fatalf("no %q instance for trino's bind to resolve against: %v",
			bom.GraphInstance, instanceNames(entries))
	}
}

// Lazy stays lazy: the cost of getting this wrong is a JVM in every
// environment, which is what the compose era did and why this is demand-driven.
func TestAnEnvironmentNothingAsksAGraphOfDeploysNone(t *testing.T) {
	t.Parallel()

	env := bom.Environment{DeploymentID: "local", GraphContainer: "global-graph-local"}
	repos := []manifest.Repo{{
		InstanceName:  "superset",
		RepoClassName: "hmd-inf-superset",
		Dependencies:  map[string]any{"compute": bom.CoreInstanceName},
	}}

	entries := composeEntries(testOptions(t.TempDir(), nil), env, repos)
	if hasInstance(entries, bom.GraphInstance) {
		t.Errorf("a graph was deployed for an environment that asked for none: %v", instanceNames(entries))
	}
}

// Turning it off with a consumer present leaves that consumer unresolved, which
// is the intended, visible outcome -- not a quietly graph-less environment.
func TestTheOverrideSuppressesTheGraphEvenUnderDemand(t *testing.T) {
	t.Parallel()

	env := bom.Environment{DeploymentID: "local"}
	repos := []manifest.Repo{{
		InstanceName:  "trino",
		RepoClassName: "hmd-inf-trino",
		Dependencies:  map[string]any{"graph-db": bom.GraphInstance},
	}}

	opts := testOptions(t.TempDir(), map[string]string{bom.GraphEnabledEnv: "false"})
	if entries := composeEntries(opts, env, repos); hasInstance(entries, bom.GraphInstance) {
		t.Error("the graph was deployed with the override set false")
	}
}

// The graph lands in the second changeset with its consumers, not in the
// substrate phase that is applied before the core Resources are submitted.
func TestTheGraphIsSeededWithTheDeclaredPhase(t *testing.T) {
	t.Parallel()

	env := bom.Environment{DeploymentID: "local"}
	repos := []manifest.Repo{{
		InstanceName:  "trino",
		RepoClassName: "hmd-inf-trino",
		Dependencies:  map[string]any{"graph-db": bom.GraphInstance},
	}}

	entries := composeEntries(testOptions(t.TempDir(), nil), env, repos)
	_, declared := splitSubstrate(entries)
	if !hasInstance(declared, bom.GraphInstance) {
		t.Errorf("the graph is not in the declared phase: %v", instanceNames(declared))
	}
}

func instanceNames(entries []bom.Entry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.RepoInstanceName)
	}
	return out
}

// Every environment the Python CLI created declares global-graph itself, with
// its own engine_version and ready_timeout. Appending a second entry under that
// name would double-count the reconcile plan and muddle its digest long before
// TopoSort's dedupe got to shadow it.
func TestAManifestThatDeclaresItsOwnGraphGetsNoSecondOne(t *testing.T) {
	t.Parallel()

	env := bom.Environment{DeploymentID: "local", GraphContainer: "global-graph-local"}
	repos := []manifest.Repo{
		{
			InstanceName:          bom.GraphInstance,
			RepoClassName:         bom.GraphRepoClass,
			InstanceConfiguration: map[string]any{"graph_host": "global-graph-local", "ready_timeout": 90},
			Dependencies:          map[string]any{"base-vpc": bom.CoreInstanceName},
		},
		{
			InstanceName:  "trino",
			RepoClassName: "hmd-inf-trino",
			Dependencies:  map[string]any{"graph-db": bom.GraphInstance},
		},
	}

	entries := composeEntries(testOptions(t.TempDir(), nil), env, repos)
	count := 0
	for _, e := range entries {
		if e.RepoInstanceName == bom.GraphInstance {
			count++
		}
	}
	if count != 1 {
		t.Errorf("%d %q entries, want exactly 1: %v", count, bom.GraphInstance, instanceNames(entries))
	}
	// And it is the manifest's own, ready_timeout and all -- not a replacement.
	for _, e := range entries {
		if e.RepoInstanceName == bom.GraphInstance && e.InstanceConfiguration["ready_timeout"] != 90 {
			t.Errorf("the manifest's graph config was replaced: %v", e.InstanceConfiguration)
		}
	}
}

// A repo whose BOM still maps a graph role to the core instance -- the stand-in
// from when the core RepoClass declared producing the graph type -- is moved to
// the real producer, so it does not resolve against an instance that creates no
// graph.
func TestALegacyCoreMappingIsRepointedAtTheGraph(t *testing.T) {
	t.Parallel()

	env := bom.Environment{DeploymentID: "local"}
	repos := []manifest.Repo{{
		InstanceName:  "airflow",
		RepoClassName: "hmd-app-airflow",
		Dependencies:  map[string]any{"neptune": bom.CoreInstanceName},
	}}

	entries := composeEntries(testOptions(t.TempDir(), nil), env, repos)
	if !hasInstance(entries, bom.GraphInstance) {
		t.Fatalf("no graph was added for a core-mapped role: %v", instanceNames(entries))
	}
	for _, e := range entries {
		if e.RepoInstanceName == "airflow" && e.Dependencies["neptune"] != bom.GraphInstance {
			t.Errorf("neptune = %v, want %q", e.Dependencies["neptune"], bom.GraphInstance)
		}
	}
}
