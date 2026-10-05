package reconcile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bom"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
)

// workingTree lays out a repo the way a chart-and-cdktf RepoClass looks.
func workingTree(t *testing.T, manifest string) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"meta-data/manifest.json":        manifest,
		"meta-data/VERSION":              "0.1",
		"src/helm/Chart.yaml":            "name: otel\n",
		"src/helm/values.yaml":           "exporter: s3\n",
		"src/helm/.helmignore":           "*.tgz\n",
		"src/cdktf/main.py":              "print('stack')\n",
		"src/local/cdktf/cdktf_local.py": "print('overlay')\n",
		"src/python/otel/__init__.py":    "",
		"README.md":                      "docs\n",
		"test/otel.robot":                "*** Test Cases ***\n",
	}
	for rel, content := range files {
		writeTreeFile(t, dir, rel, content)
	}
	return dir
}

func writeTreeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const helmCdktfManifest = `{"deploy": {"commands": [["cdktf"], ["helm"]], "default_configuration": {"exporter": "s3"}}}`

func mustDigest(t *testing.T, dir string) string {
	t.Helper()
	d, err := TreeDigest(dir)
	if err != nil {
		t.Fatalf("TreeDigest: %v", err)
	}
	if d == "" {
		t.Fatal("TreeDigest returned an empty digest for a real tree")
	}
	return d
}

func TestTreeDigestIsStable(t *testing.T) {
	t.Parallel()

	dir := workingTree(t, helmCdktfManifest)
	if a, b := mustDigest(t, dir), mustDigest(t, dir); a != b {
		t.Errorf("the same tree digested twice differently: %s vs %s", a, b)
	}
}

// What a deploy reads: the manifest, the deploy tools' sources and the local
// overlay. Each is an edit that has to reach the environment (NERD034).
func TestTreeDigestNoticesWhatADeployReads(t *testing.T) {
	t.Parallel()

	for _, edit := range []struct{ name, rel, content string }{
		{"chart value", "src/helm/values.yaml", "exporter: clickhouse\n"},
		{"new template", "src/helm/templates/svc.yaml", "kind: Service\n"},
		{"helmignore", "src/helm/.helmignore", "*.tgz\n*.bak\n"},
		{"manifest default", "meta-data/manifest.json", strings.Replace(helmCdktfManifest, `"s3"`, `"clickhouse"`, 1)},
		{"cdktf stack", "src/cdktf/main.py", "print('changed')\n"},
		{"local overlay", "src/local/cdktf/cdktf_local.py", "print('changed')\n"},
	} {
		t.Run(edit.name, func(t *testing.T) {
			t.Parallel()
			dir := workingTree(t, helmCdktfManifest)
			before := mustDigest(t, dir)
			writeTreeFile(t, dir, edit.rel, edit.content)
			if mustDigest(t, dir) == before {
				t.Errorf("editing %s left the digest unchanged", edit.rel)
			}
		})
	}
}

// What a deploy does not read, or writes itself. Any of these changing the
// digest would redeploy on every apply, or on every build.
func TestTreeDigestIgnoresWhatADeployDoesNotRead(t *testing.T) {
	t.Parallel()

	for _, edit := range []struct{ name, rel string }{
		{"resources a deploy emitted", "meta-data/resources_output/cluster.json"},
		{"python source of a helm+cdktf class", "src/python/otel/core.py"},
		{"bytecode", "src/cdktf/__pycache__/main.cpython-311.pyc"},
		{"cdktf provider bindings", "src/cdktf/imports/aws/__init__.py"},
		{"synth output", "src/cdktf/cdktf.out/stacks/x/cdk.tf.json"},
		{"terraform cache", "src/cdktf/.terraform/providers/x"},
		{"build output", "src/helm/build/chart.tgz"},
		{"a log", "src/cdktf/deploy.log"},
		{"Finder litter", "src/helm/.DS_Store"},
		{"docs", "docs/index.rst"},
		{"tests", "test/more.robot"},
	} {
		t.Run(edit.name, func(t *testing.T) {
			t.Parallel()
			dir := workingTree(t, helmCdktfManifest)
			before := mustDigest(t, dir)
			writeTreeFile(t, dir, edit.rel, "noise\n")
			if mustDigest(t, dir) != before {
				t.Errorf("writing %s changed the digest", edit.rel)
			}
		})
	}
}

// A manifest whose deploy commands nsctl cannot read still gets every edit
// under src/ noticed, rather than none.
func TestTreeDigestWithoutReadableDeployCommandsCoversAllOfSrc(t *testing.T) {
	t.Parallel()

	for _, manifest := range []string{`{}`, `not json`, `{"deploy": {"commands": [{"helm": true}]}}`} {
		dir := workingTree(t, manifest)
		before := mustDigest(t, dir)
		writeTreeFile(t, dir, "src/python/otel/core.py", "changed\n")
		if mustDigest(t, dir) == before {
			t.Errorf("manifest %q: an edit under src/ went unnoticed", manifest)
		}
	}
}

func TestTreeDigestOfAMissingTreeIsAnError(t *testing.T) {
	t.Parallel()

	if _, err := TreeDigest(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Error("a tree that is not there digested without complaint")
	}
}

// NERD034 SPEC002: an instance the entry hash calls unchanged, whose tree has
// moved on since it was recorded, is a change.
func TestMarkTreesChangedMovesAnEditedTreeToChange(t *testing.T) {
	t.Parallel()

	desired := []bom.Entry{entry("otel", "0.1"), entry("clickhouse", "0.1"), entry("fresh", "0.1"), entry("bundled", "0.1")}
	status := map[string]string{}
	snapshot := map[string]string{}
	for _, e := range desired {
		status[e.RepoInstanceName] = msdeploy.StatusDeployed
		snapshot[e.RepoInstanceName] = EntryHash(e)
	}
	plan := Compute(desired, status, snapshot, nil)

	moved := plan.MarkTreesChanged(
		// recorded: fresh has none yet (first apply after upgrading).
		map[string]string{"otel": "old", "clickhouse": "same", "bundled": "x"},
		// current: bundled is no longer a working tree.
		map[string]string{"otel": "new", "clickhouse": "same", "fresh": "f"},
	)

	if got := strings.Join(moved, ","); got != "otel" {
		t.Errorf("moved %q, want only otel", got)
	}
	if got := strings.Join(names(plan.Change), ","); got != "otel" {
		t.Errorf("Change = %q, want otel", got)
	}
	if got := strings.Join(plan.Unchanged, ","); got != "clickhouse,fresh,bundled" {
		t.Errorf("Unchanged = %q", got)
	}
	if !strings.Contains(plan.Summary(), "1 from a local tree") {
		t.Errorf("Summary = %q, want it to say the change came from a tree", plan.Summary())
	}
	if got := names(plan.Deploy()); len(got) != 1 || got[0] != "otel" {
		t.Errorf("Deploy = %v, want otel", got)
	}
}

func TestSnapshotRecordsTreeDigests(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	entries := []bom.Entry{entry("otel", "0.1"), entry("bundled", "0.1")}
	if err := WriteSnapshot(dir, entries, nil, map[string]string{"otel": "abc"}); err != nil {
		t.Fatal(err)
	}
	trees := LoadSnapshotTrees(dir)
	if trees["otel"] != "abc" {
		t.Errorf("otel's tree digest = %q, want abc", trees["otel"])
	}
	if _, ok := trees["bundled"]; ok {
		t.Error("an entry with no working tree recorded a digest")
	}
	// The entry hashes the Python front end reads are untouched.
	if LoadSnapshot(dir)["otel"] != EntryHash(entries[0]) {
		t.Error("recording a tree digest disturbed the entry hash")
	}
	raw, _ := os.ReadFile(SnapshotPath(dir))
	if strings.Count(string(raw), "tree_digest") != 1 {
		t.Errorf("want tree_digest only where one was recorded:\n%s", raw)
	}
}

// An exec RepoClass (NERD009) runs a command of its own that may read anything
// in its checkout -- the tutorial's lives in scripts/ -- so its whole tree is
// what a deploy reads, less the same build and cache output.
func TestTreeDigestOfAnExecRepoClassCoversItsWholeTree(t *testing.T) {
	t.Parallel()

	dir := workingTree(t, `{"deploy": {"commands": [["exec", "sh", "scripts/deploy-local.sh"]]}}`)
	writeTreeFile(t, dir, "scripts/deploy-local.sh", "echo deploy\n")
	before := mustDigest(t, dir)

	writeTreeFile(t, dir, "target/nsctl-demo.txt", "Deployed\n")
	writeTreeFile(t, dir, ".git/index", "noise\n")
	writeTreeFile(t, dir, "test/results/output.xml", "<robot/>\n")
	writeTreeFile(t, dir, "docs/index.rst", "docs\n")
	if mustDigest(t, dir) != before {
		t.Error("the deploy's own output, git, tests or docs changed the digest")
	}
	writeTreeFile(t, dir, "scripts/deploy-local.sh", "echo changed\n")
	if mustDigest(t, dir) == before {
		t.Error("editing the exec deploy script went unnoticed")
	}
}
