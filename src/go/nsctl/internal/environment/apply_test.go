package environment

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bom"
)

// The substrate and what the manifest declares are applied as two changesets,
// because the concrete Resources a declared entry's tag_selector matches can
// only be attached once the substrate's core instance has a deployment.
func TestSplitSubstrateSeparatesTheTwoPhases(t *testing.T) {
	t.Parallel()

	entries := []bom.Entry{
		{RepoInstanceName: "local-neuronsphere"},
		{RepoInstanceName: "base-vpc"},
		{RepoInstanceName: "environment-db"},
		{RepoInstanceName: "eks-cluster"},
		{RepoInstanceName: "airflow"},
		{RepoInstanceName: "airflow-db-account"},
	}
	substrate, declared := splitSubstrate(entries)

	if len(substrate) != 4 {
		t.Errorf("substrate has %d entries, want 4: %v", len(substrate), names(substrate))
	}
	if len(declared) != 2 {
		t.Errorf("declared has %d entries, want 2: %v", len(declared), names(declared))
	}
	// Order matters: Seed sorts topologically, but only within what it is given.
	if len(substrate) == 4 && substrate[0].RepoInstanceName != "local-neuronsphere" {
		t.Errorf("substrate order changed: %v", names(substrate))
	}
}

// An environment with an empty manifest is the ordinary case -- a cluster and a
// database with nothing on them -- and must not produce an empty second
// changeset.
func TestSplitSubstrateHandlesAnEmptyManifest(t *testing.T) {
	t.Parallel()

	_, declared := splitSubstrate([]bom.Entry{{RepoInstanceName: "local-neuronsphere"}})
	if len(declared) != 0 {
		t.Errorf("declared = %v, want none", names(declared))
	}
}

// hmd-ms-dbaccount is the service every hmd-database-account instance's
// create-service role selects, and it is addressed under the environment's
// prefix rather than at the control-plane root.
func TestFoundationServicesAdvertiseDbaccountPerEnvironment(t *testing.T) {
	t.Parallel()

	var dbaccount *bom.ServiceSpec
	for i, svc := range foundationServices("dev2") {
		if svc.RepoClass == DBAccountRepoClass {
			dbaccount = &foundationServices("dev2")[i]
		}
	}
	if dbaccount == nil {
		t.Fatal("no hmd-ms-dbaccount service advertised")
	}
	if dbaccount.APIBaseURL != "http://localhost/dev2/hmd_ms_dbaccount" {
		t.Errorf("api_base_url = %q, want the environment-prefixed route", dbaccount.APIBaseURL)
	}
}

func names(entries []bom.Entry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.RepoInstanceName)
	}
	return out
}

// The sixth cold-start defect, and the same shape as the five before it: a
// warm platform always has a kubeconfig on disk, so Phase B read one without
// anyone noticing that nothing in a cold start writes it.
func TestKubeconfigUnusableCoversEveryStateACoLdStartProduces(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	missing := filepath.Join(dir, "never-written")
	if !kubeconfigUnusable(missing) {
		t.Error("a kubeconfig nothing has written yet is unusable; this is the cold start")
	}

	// What Docker leaves when a deploy mounts the path before the cluster
	// writes it. Every later deploy then mounts the directory.
	asDir := filepath.Join(dir, "docker-made-this")
	if err := os.MkdirAll(asDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if !kubeconfigUnusable(asDir) {
		t.Error("a directory is never a kubeconfig")
	}

	empty := filepath.Join(dir, "interrupted")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !kubeconfigUnusable(empty) {
		t.Error("an empty file is an interrupted write, not a kubeconfig")
	}

	good := filepath.Join(dir, "kubeconfig")
	if err := os.WriteFile(good, []byte("apiVersion: v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if kubeconfigUnusable(good) {
		t.Error("a warm apply must not rewrite a kubeconfig that works")
	}

	// No path configured is not a problem to solve here.
	if kubeconfigUnusable("") {
		t.Error("an unset path is not an unusable kubeconfig")
	}
}
