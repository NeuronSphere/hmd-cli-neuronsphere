package reconcile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bom"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
)

func entry(name, version string) bom.Entry {
	return bom.Entry{
		RepoInstanceName: name,
		RepoClassName:    "hmd-ms-" + name,
		RepoClassVersion: version,
		DeploymentID:     "local",
	}
}

func names(entries []bom.Entry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.RepoInstanceName)
	}
	return out
}

func TestComputeDeploysWhatIsNotDeployed(t *testing.T) {
	t.Parallel()

	desired := []bom.Entry{entry("a", "1.0"), entry("b", "1.0")}
	plan := Compute(desired, map[string]string{"a": msdeploy.StatusDeployed}, nil, nil)

	if got := names(plan.Add); !reflect.DeepEqual(got, []string{"b"}) {
		t.Errorf("Add = %v, want [b]", got)
	}
	if !reflect.DeepEqual(plan.Unchanged, []string{"a"}) {
		t.Errorf("Unchanged = %v, want [a]", plan.Unchanged)
	}
}

// A previous failure is not a reason to leave it alone.
func TestComputeRetriesAFailedInstance(t *testing.T) {
	t.Parallel()

	desired := []bom.Entry{entry("a", "1.0")}
	plan := Compute(desired, map[string]string{"a": msdeploy.StatusFailed}, nil, nil)
	if len(plan.Add) != 1 {
		t.Errorf("a FAILED instance was not proposed for deployment: %+v", plan)
	}
}

func TestComputeNoticesDrift(t *testing.T) {
	t.Parallel()

	deployed := entry("a", "1.0")
	drifted := entry("a", "1.1")
	plan := Compute([]bom.Entry{drifted},
		map[string]string{"a": msdeploy.StatusDeployed},
		map[string]string{"a": EntryHash(deployed)}, nil)

	if got := names(plan.Change); !reflect.DeepEqual(got, []string{"a"}) {
		t.Errorf("Change = %v, want [a]", got)
	}
	if len(plan.Unchanged) != 0 {
		t.Errorf("Unchanged = %v, want none", plan.Unchanged)
	}
}

// No snapshot means no information. Reading that as "changed" would redeploy
// an entire working environment the first time a user runs this.
func TestComputeLeavesADeployedEntryAloneWithNoSnapshot(t *testing.T) {
	t.Parallel()

	plan := Compute([]bom.Entry{entry("a", "1.0")},
		map[string]string{"a": msdeploy.StatusDeployed}, map[string]string{}, nil)

	if !plan.Empty() {
		t.Errorf("a missing snapshot proposed work: %+v", plan)
	}
	if !reflect.DeepEqual(plan.Unchanged, []string{"a"}) {
		t.Errorf("Unchanged = %v, want [a]", plan.Unchanged)
	}
}

// The graph and the cluster can disagree. Redeploying is the only thing that
// makes them agree again.
func TestComputeRedeploysWhenTheHelmReleaseIsGone(t *testing.T) {
	t.Parallel()

	e := entry("a", "1.0")
	plan := Compute([]bom.Entry{e},
		map[string]string{"a": msdeploy.StatusDeployed},
		map[string]string{"a": EntryHash(e)},
		map[string]bool{"a": true})

	if got := names(plan.Add); !reflect.DeepEqual(got, []string{"a"}) {
		t.Errorf("Add = %v, want [a] despite the graph calling it deployed", got)
	}
}

func TestComputeReportsWhatIsDeployedButUndeclared(t *testing.T) {
	t.Parallel()

	plan := Compute([]bom.Entry{entry("a", "1.0")}, map[string]string{
		"a":       msdeploy.StatusDeployed,
		"orphan":  msdeploy.StatusDeployed,
		"another": msdeploy.StatusDeployed,
		// Not deployed, so not something a user left behind.
		"failed": msdeploy.StatusFailed,
	}, nil, nil)

	if !reflect.DeepEqual(plan.Remove, []string{"another", "orphan"}) {
		t.Errorf("Remove = %v, want [another orphan]", plan.Remove)
	}
}

// Deploy has to preserve the definition's order: it was topologically sorted,
// and running a dependent before its dependency fails.
func TestDeployKeepsTheDefinitionOrder(t *testing.T) {
	t.Parallel()

	desired := []bom.Entry{entry("first", "1.0"), entry("second", "1.0"), entry("third", "1.0")}
	plan := Compute(desired, map[string]string{"second": msdeploy.StatusDeployed}, nil, nil)
	// second is unchanged, so only first and third deploy -- in that order.
	if got := names(plan.Deploy()); !reflect.DeepEqual(got, []string{"first", "third"}) {
		t.Errorf("Deploy = %v, want [first third]", got)
	}
}

// A transient outage of the deployment service must never read as "everything
// was removed from the manifest".
func TestDegradedProposesNothing(t *testing.T) {
	t.Parallel()

	plan := Degraded([]bom.Entry{entry("a", "1.0")})
	if !plan.Degraded || !plan.Empty() || len(plan.Remove) != 0 {
		t.Errorf("a degraded plan proposed work: %+v", plan)
	}
	if !strings.Contains(plan.Summary(), "could not be read") {
		t.Errorf("Summary = %q, want it to explain why nothing is proposed", plan.Summary())
	}
}

func TestSnapshotRoundTrips(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	entries := []bom.Entry{entry("a", "1.0"), entry("b", "2.0")}
	if err := WriteSnapshot(dir, entries, map[string]string{"a": "release-a"}); err != nil {
		t.Fatalf("WriteSnapshot: %v", err)
	}

	digests := LoadSnapshot(dir)
	if digests["a"] != EntryHash(entries[0]) || digests["b"] != EntryHash(entries[1]) {
		t.Errorf("snapshot digests = %v", digests)
	}

	// The Helm release is recorded, so a later run can notice it went missing.
	data, err := os.ReadFile(SnapshotPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	var doc Snapshot
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	var releaseFor string
	for _, e := range doc.Entries {
		if e.RepoInstanceName == "a" {
			releaseFor = e.K8sRelease
		}
	}
	if releaseFor != "release-a" {
		t.Errorf("k8s_release = %q, want release-a", releaseFor)
	}
}

// An unreadable snapshot is missing information, not evidence of change.
func TestLoadSnapshotIsBestEffort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(dir string)
	}{
		{"no snapshot", func(string) {}},
		{"malformed", func(dir string) {
			_ = os.WriteFile(SnapshotPath(dir), []byte("{not json"), 0o644)
		}},
		{"no entries", func(dir string) {
			_ = os.WriteFile(SnapshotPath(dir), []byte(`{"entries": []}`), 0o644)
		}},
		{"entries missing fields", func(dir string) {
			_ = os.WriteFile(SnapshotPath(dir), []byte(`{"entries": [{"hash": "x"}, {"repo_instance_name": "a"}]}`), 0o644)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			tt.setup(dir)
			if got := LoadSnapshot(dir); len(got) != 0 {
				t.Errorf("LoadSnapshot = %v, want nothing", got)
			}
		})
	}
}

// An interrupted write must leave the previous snapshot readable rather than a
// truncated file that reads as "nothing was ever applied".
func TestWriteSnapshotReplacesAtomically(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := WriteSnapshot(dir, []bom.Entry{entry("a", "1.0")}, nil); err != nil {
		t.Fatal(err)
	}
	if err := WriteSnapshot(dir, []bom.Entry{entry("a", "2.0")}, nil); err != nil {
		t.Fatal(err)
	}

	if digests := LoadSnapshot(dir); digests["a"] != EntryHash(entry("a", "2.0")) {
		t.Error("the second write did not replace the first")
	}
	if _, err := os.Stat(filepath.Join(dir, SnapshotFilename+".tmp")); !os.IsNotExist(err) {
		t.Error("the temporary file was left behind")
	}
}

// The snapshot lives in the environment's state directory, which `env purge`
// removes -- so purging resets drift tracking along with everything else.
func TestSnapshotPathIsInTheStateDirectory(t *testing.T) {
	t.Parallel()

	if got := SnapshotPath("/state/local"); got != "/state/local/"+SnapshotFilename {
		t.Errorf("SnapshotPath = %q", got)
	}
	if got := SnapshotPath(""); got != "" {
		t.Errorf("SnapshotPath with no state directory = %q, want empty", got)
	}
}

// Writing with no state directory is a no-op, not a failure: an environment
// without one still deploys, it just carries no drift information.
func TestWriteSnapshotWithNoStateDirectoryIsANoOp(t *testing.T) {
	t.Parallel()

	if err := WriteSnapshot("", []bom.Entry{entry("a", "1.0")}, nil); err != nil {
		t.Errorf("WriteSnapshot: %v", err)
	}
}
