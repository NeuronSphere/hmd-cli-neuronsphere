package bom

import (
	"strings"
	"testing"
)

func testEnv() Environment {
	return Environment{
		Slug: "local", DeploymentID: "local", AccountID: "000000000001", Region: "reg1",
		DBContainer: "hmd_db-local", K3sCluster: "ns-local-abc",
		K3sContainer: "floci-eks-000000000001.ns-local-abc",
	}
}

// nsctl ships the substrate and nothing above it. A workload appearing here
// would be a built-in catalogue, which is exactly what this port removed.
func TestSubstrateIsOnlyTheSubstrate(t *testing.T) {
	t.Parallel()

	entries := Substrate(testEnv(), true)
	got := map[string]string{}
	for _, e := range entries {
		got[e.RepoInstanceName] = e.RepoClassName
	}
	want := map[string]string{
		CoreInstanceName:   CoreRepoClass,
		BaseVPCInstance:    BaseVPCRepoClass,
		EnvDBInstance:      EnvDBRepoClass,
		EKSClusterInstance: EKSClusterRepoClass,
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want exactly the substrate %v", got, want)
	}
	for name, class := range want {
		if got[name] != class {
			t.Errorf("%s = %q, want %q", name, got[name], class)
		}
	}
	for _, workload := range []string{"airflow", "argo", "transform", "trino", "superset"} {
		if _, present := got[workload]; present {
			t.Errorf("%s is built in; it must be a RepoClass the user adds", workload)
		}
	}
}

func TestSubstrateOmitsTheClusterWhenK3sIsOff(t *testing.T) {
	t.Parallel()

	for _, e := range Substrate(testEnv(), false) {
		if e.RepoInstanceName == EKSClusterInstance {
			t.Error("the cluster entry was included with k3s disabled; it would deploy a cluster nothing runs on")
		}
	}
}

// ms-deployment validates required *roles* at changeset apply rather than at
// deploy, so every dependency the RepoClassVersion marks required must be
// supplied even where the local overlay references none of them.
func TestSubstrateSuppliesEveryRequiredRole(t *testing.T) {
	t.Parallel()

	byName := map[string]Entry{}
	for _, e := range Substrate(testEnv(), true) {
		byName[e.RepoInstanceName] = e
	}
	for _, role := range []string{"datadog-lambda", "rds-loggroup"} {
		if byName[EnvDBInstance].Dependencies[role] != CoreInstanceName {
			t.Errorf("the database does not supply the %q role", role)
		}
	}
	if byName[EKSClusterInstance].Dependencies["datadog-lambda"] != CoreInstanceName {
		t.Error("the cluster does not supply the datadog-lambda role")
	}

	// base-vpc resolves to a real producing instance now, not to the core
	// instance declaring a type it does not create. That is also what orders
	// the VPC ahead of the two entries that need its subnets.
	for _, instance := range []string{EnvDBInstance, EKSClusterInstance} {
		if got := byName[instance].Dependencies["base-vpc"]; got != BaseVPCInstance {
			t.Errorf("%s takes base-vpc from %q, want %q", instance, got, BaseVPCInstance)
		}
	}
}

// The VPC has to be deployed before the database that needs its subnet group
// and the cluster whose overlay looks its subnets up by boto3.
func TestSubstrateOrdersTheVPCFirst(t *testing.T) {
	t.Parallel()

	sorted, err := TopoSort(Substrate(testEnv(), true))
	if err != nil {
		t.Fatal(err)
	}
	position := map[string]int{}
	for i, e := range sorted {
		position[e.RepoInstanceName] = i
	}
	for _, after := range []string{EnvDBInstance, EKSClusterInstance} {
		if position[BaseVPCInstance] > position[after] {
			t.Errorf("%s is ordered before base-vpc: %v", after, positionsOf(sorted))
		}
	}
}

// The database's host is the DNS alias, not Floci's published endpoint: that
// endpoint is a port from the 7001-7099 proxy range which Floci does not
// restore on restart.
func TestInjectEndpointsUsesTheAliasAndTheQualifiedContainer(t *testing.T) {
	t.Parallel()

	entries := Substrate(testEnv(), true)
	InjectEndpoints(entries, testEnv())

	byName := map[string]Entry{}
	for _, e := range entries {
		byName[e.RepoInstanceName] = e
	}
	db := byName[EnvDBInstance].InstanceConfiguration
	if db["db_host"] != "hmd_db-local" {
		t.Errorf("db_host = %v, want the DNS alias", db["db_host"])
	}
	if db["db_port"] != 5432 {
		t.Errorf("db_port = %v, want 5432 rather than a proxy-range port", db["db_port"])
	}
	// The subnet group must survive the injection.
	if db["db_subnet_group_name"] != LocalDBSubnetGroup {
		t.Errorf("the subnet group was lost: %v", db)
	}

	cluster := byName[EKSClusterInstance].InstanceConfiguration
	if cluster["cluster_name"] != "ns-local-abc" {
		t.Errorf("cluster_name = %v", cluster["cluster_name"])
	}
	if cluster["cluster_endpoint"] != "https://floci-eks-000000000001.ns-local-abc:6443" {
		t.Errorf("cluster_endpoint = %v, want the account-qualified container", cluster["cluster_endpoint"])
	}
}

// apply_changeset_to_environment resolves an entry's dependencies only against
// instances already added earlier in the same call, so an out-of-order list
// fails with "No repo instance".
func TestTopoSortPutsDependenciesFirst(t *testing.T) {
	t.Parallel()

	entries := []Entry{
		{RepoInstanceName: "db", Dependencies: map[string]any{"base-vpc": "core"}},
		{RepoInstanceName: "core"},
		{RepoInstanceName: "app", Dependencies: map[string]any{"database-instance": "db"}},
	}
	sorted, err := TopoSort(entries)
	if err != nil {
		t.Fatalf("TopoSort: %v", err)
	}
	position := map[string]int{}
	for i, e := range sorted {
		position[e.RepoInstanceName] = i
	}
	if position["core"] > position["db"] || position["db"] > position["app"] {
		t.Errorf("order is wrong: %v", positionsOf(sorted))
	}
}

// A dependency on an instance the BOM does not declare may already exist in the
// graph from an earlier changeset, which is the normal case for a delta apply.
func TestTopoSortIgnoresUndeclaredDependencies(t *testing.T) {
	t.Parallel()

	sorted, err := TopoSort([]Entry{
		{RepoInstanceName: "app", Dependencies: map[string]any{"x": "already-deployed"}},
	})
	if err != nil {
		t.Fatalf("TopoSort: %v", err)
	}
	if len(sorted) != 1 {
		t.Errorf("got %v, want the single entry", positionsOf(sorted))
	}
}

func TestTopoSortReportsACycle(t *testing.T) {
	t.Parallel()

	_, err := TopoSort([]Entry{
		{RepoInstanceName: "a", Dependencies: map[string]any{"x": "b"}},
		{RepoInstanceName: "b", Dependencies: map[string]any{"y": "a"}},
	})
	if err == nil {
		t.Fatal("a cycle was accepted")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("the error does not name the problem: %v", err)
	}
}

func TestTopoSortIsDeterministic(t *testing.T) {
	t.Parallel()

	entries := Substrate(testEnv(), true)
	first, err := TopoSort(entries)
	if err != nil {
		t.Fatal(err)
	}
	second, err := TopoSort(entries)
	if err != nil {
		t.Fatal(err)
	}
	for i := range first {
		if first[i].RepoInstanceName != second[i].RepoInstanceName {
			t.Fatalf("the order is not stable: %v vs %v", positionsOf(first), positionsOf(second))
		}
	}
}

func positionsOf(entries []Entry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.RepoInstanceName)
	}
	return out
}

// kubernetes-cluster is produced by the eks-cluster instance, not the core one,
// so the same repo produces that Resource in the cloud and locally.
func TestCoreProducedDefinitionsExcludeTheCluster(t *testing.T) {
	t.Parallel()

	for _, d := range CoreProducedDefinitions {
		if d.Name == "kubernetes-cluster" {
			t.Error("kubernetes-cluster is declared core-produced; it comes from the eks-cluster instance")
		}
	}
	// network and vpc are produced by the base-vpc instance now. Declaring them
	// here too would make two producers compete for the same dependency.
	for _, d := range CoreProducedDefinitions {
		if d.Name == "network" || d.Name == "vpc" {
			t.Errorf("%s is still declared core-produced; base-vpc produces it", d.Name)
		}
	}
}
