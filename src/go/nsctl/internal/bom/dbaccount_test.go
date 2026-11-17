package bom

import "testing"

// Requirements: HMD_CLI_NEURONSPHERE_NERD024_SPEC001
func TestRequiresDBAccountSeesADeclaredConsumer(t *testing.T) {
	entries := []Entry{
		{RepoInstanceName: "local-neuronsphere", RepoClassName: "hmd-cli-neuronsphere"},
		{RepoInstanceName: "airflow-db-account", RepoClassName: DBAccountConsumerClass},
	}
	if !RequiresDBAccount(entries) {
		t.Fatal("a declared hmd-database-account instance is demand for the service")
	}
}

// Requirements: HMD_CLI_NEURONSPHERE_NERD024_SPEC001
func TestRequiresDBAccountIsFalseWithoutOne(t *testing.T) {
	entries := []Entry{
		{RepoInstanceName: "local-neuronsphere", RepoClassName: "hmd-cli-neuronsphere"},
		{RepoInstanceName: "acme-loader", RepoClassName: "acme-loader"},
	}
	if RequiresDBAccount(entries) {
		t.Fatal("an environment that asks for no database account needs no dbaccount service")
	}
}

// The substrate alone must not pull the service in. That is the whole point of
// NERD024 SPEC001: an environment on `core` that declares nothing still gets a
// database and a graph, and no Lambda.
//
// Requirements: HMD_CLI_NEURONSPHERE_NERD024_SPEC001
func TestRequiresDBAccountIgnoresTheSubstrate(t *testing.T) {
	env := Environment{Slug: "local", DeploymentID: "did", AccountID: "000000000002"}
	if RequiresDBAccount(Substrate(env, true)) {
		t.Fatal("the substrate declares no database account of its own")
	}
}
