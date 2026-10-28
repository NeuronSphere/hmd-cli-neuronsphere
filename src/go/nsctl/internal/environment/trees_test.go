package environment

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/artifact"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bom"
)

func writeRepo(t *testing.T, dir string) {
	t.Helper()
	for rel, content := range map[string]string{
		"meta-data/manifest.json": `{"deploy": {"commands": [["helm"]]}}`,
		"meta-data/VERSION":       "0.1",
		"src/helm/values.yaml":    "a: 1\n",
	} {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// NERD034 SPEC002: only a tree a developer owns is digested. A cached
// artifact is versioned, and a class with no tree on disk has nothing to read.
func TestWorkingTreeDigestsCoverOnlyDevelopersTrees(t *testing.T) {
	t.Parallel()

	home, repoHome := t.TempDir(), t.TempDir()
	writeRepo(t, filepath.Join(repoHome, "hmd-inf-otel-collector"))
	cached := filepath.Join(artifact.Root(home), "hmd-inf-clickhouse", "0.1")
	writeRepo(t, cached)

	opts := &Options{
		Home: home, Out: io.Discard, Err: io.Discard,
		Lookup: func(k string) string {
			if k == "HMD_REPO_HOME" {
				return repoHome
			}
			return ""
		},
	}
	entries := []bom.Entry{
		{RepoInstanceName: "otel", RepoClassName: "hmd-inf-otel-collector"},
		{RepoInstanceName: "otel-2", RepoClassName: "hmd-inf-otel-collector"},
		{RepoInstanceName: "clickhouse", RepoClassName: "hmd-inf-clickhouse"},
		{RepoInstanceName: "nowhere", RepoClassName: "hmd-inf-absent"},
	}
	trees := workingTreeDigests(opts, map[string]string{"hmd-inf-clickhouse": cached}, entries)

	if trees["otel"] == "" || trees["otel"] != trees["otel-2"] {
		t.Errorf("both instances of the checked-out class want its digest: %v", trees)
	}
	for _, name := range []string{"clickhouse", "nowhere"} {
		if _, ok := trees[name]; ok {
			t.Errorf("%s has no developer's tree but recorded a digest", name)
		}
	}
}
