package environment

import (
	"reflect"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bom"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/reconcile"
)

// deployedUnder is what the graph and the snapshot say after an apply of the
// given mode: every entry DEPLOYED, every digest recorded.
func deployedUnder(entries []bom.Entry) (status, snapshot map[string]string) {
	status, snapshot = map[string]string{}, map[string]string{}
	for _, e := range entries {
		status[e.RepoInstanceName] = msdeploy.StatusDeployed
		snapshot[e.RepoInstanceName] = reconcile.EntryHash(e)
	}
	return status, snapshot
}

func entryNames(entries []bom.Entry) []string {
	var out []string
	for _, e := range entries {
		out = append(out, e.RepoInstanceName)
	}
	return out
}

// Raising the mode adds the missing substrate entries and redeploys nothing
// that is already deployed.
//
// Requirements: HMD_CLI_NEURONSPHERE_NERD014_SPEC005
func TestRaisingTheModeAddsAndNeverRedeploys(t *testing.T) {
	t.Parallel()

	env := bom.Environment{DeploymentID: "scratch", AccessKeyID: "000000000014"}
	for _, from := range []manifest.Substrate{manifest.SubstrateNone, manifest.SubstrateCore} {
		low := desiredEntries(testOptions(t.TempDir(), nil), env, from, nil)
		high := desiredEntries(testOptions(t.TempDir(), nil), env, manifest.SubstrateFull, nil)
		status, snapshot := deployedUnder(low)

		plan := reconcile.Compute(high, status, snapshot, nil)
		if len(plan.Change) != 0 {
			t.Errorf("%s->full: already-deployed entries drifted and would be redeployed: %v", from, entryNames(plan.Change))
		}
		have := map[string]bool{}
		for _, n := range entryNames(low) {
			have[n] = true
		}
		var wantAdd []string
		for _, n := range entryNames(high) {
			if !have[n] {
				wantAdd = append(wantAdd, n)
			}
		}
		if len(wantAdd) == 0 {
			t.Fatalf("%s->full: full adds nothing over %s, so the test proves nothing", from, from)
		}
		if !reflect.DeepEqual(entryNames(plan.Add), wantAdd) {
			t.Errorf("%s->full: Add = %v, want exactly the missing substrate %v", from, entryNames(plan.Add), wantAdd)
		}
		for _, n := range entryNames(plan.Deploy()) {
			if have[n] {
				t.Errorf("%s->full: %s was already deployed and is in the deploy set", from, n)
			}
		}
	}
	// The k3s block's redeploy-a-missing-cluster rule is gated on the mode.
	if planFor(manifest.SubstrateCore).Cluster || !planFor(manifest.SubstrateFull).Cluster {
		t.Error("only full runs the cluster block that adds a missing cluster")
	}
}

// Lowering the mode removes and stops nothing: the reconcile reports the
// substrate entries as deployed but undeclared and leaves them, deploying
// nothing, and the lowered plan skips every infrastructure step.
//
// Requirements: HMD_CLI_NEURONSPHERE_NERD014_SPEC006
func TestLoweringTheModeDestroysNothing(t *testing.T) {
	t.Parallel()

	env := bom.Environment{DeploymentID: "scratch", AccessKeyID: "000000000014"}
	full := desiredEntries(testOptions(t.TempDir(), nil), env, manifest.SubstrateFull, nil)
	status, snapshot := deployedUnder(full)

	for _, to := range []manifest.Substrate{manifest.SubstrateNone, manifest.SubstrateCore} {
		low := desiredEntries(testOptions(t.TempDir(), nil), env, to, nil)
		plan := reconcile.Compute(low, status, snapshot, nil)

		if !plan.Empty() || len(plan.Deploy()) != 0 {
			t.Errorf("full->%s: lowering deploys something: add=%v change=%v", to, entryNames(plan.Add), entryNames(plan.Change))
		}
		kept := map[string]bool{}
		for _, n := range entryNames(low) {
			kept[n] = true
		}
		var wantRemove []string
		for _, n := range entryNames(full) {
			if !kept[n] {
				wantRemove = append(wantRemove, n)
			}
		}
		if len(wantRemove) == 0 {
			t.Fatalf("full->%s: lowering drops nothing, so the test proves nothing", to)
		}
		// Reported (Remove is "never acted on", and apply warns "nsctl leaves
		// it running"); none of the dropped entries is in the deploy set.
		got := append([]string(nil), plan.Remove...)
		want := append([]string(nil), wantRemove...)
		sortStrings(got)
		sortStrings(want)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("full->%s: Remove = %v, want the dropped substrate %v", to, got, want)
		}
	}
	if got := planFor(manifest.SubstrateNone); got != (startPlan{}) {
		t.Errorf("none still names steps to run: %+v", got)
	}
}

func sortStrings(s []string) {
	for i := range s {
		for j := i + 1; j < len(s); j++ {
			if s[j] < s[i] {
				s[i], s[j] = s[j], s[i]
			}
		}
	}
}
