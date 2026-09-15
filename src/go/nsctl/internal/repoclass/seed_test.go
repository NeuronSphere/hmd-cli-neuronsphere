package repoclass

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
)

func repoHomeLookup(repoHome string) Lookup {
	return func(k string) string {
		if k == "HMD_REPO_HOME" {
			return repoHome
		}
		return ""
	}
}

// Paths means "the manifest asked for this tree" and is taken without a stat.
// The convention is not that, however it was written down.
func TestPathsOnlyOverridesWhatDiffers(t *testing.T) {
	t.Parallel()

	repoHome := t.TempDir()
	elsewhere := filepath.Join(t.TempDir(), "vendored")
	if err := os.MkdirAll(elsewhere, 0o755); err != nil {
		t.Fatal(err)
	}
	lookup := repoHomeLookup(repoHome)

	paths := Paths([]manifest.Repo{
		// The convention -- the runner finds this one on its own.
		{InstanceName: "a", RepoClassName: "hmd-ms-a"},
		// Spelled out, but to the same place. Still not an override, and this
		// is the case the control-plane path used to treat as one.
		{InstanceName: "b", RepoClassName: "hmd-ms-b", Source: &manifest.Source{Path: filepath.Join(repoHome, "hmd-ms-b")}},
		// Genuinely elsewhere.
		{InstanceName: "c", RepoClassName: "hmd-ms-c", Source: &manifest.Source{Path: elsewhere}},
	}, lookup)

	want := map[string]string{"hmd-ms-c": elsewhere}
	if !reflect.DeepEqual(paths, want) {
		t.Errorf("Paths = %v, want %v", paths, want)
	}
}

// The regression the extraction risks: an artifact declaration landing in Paths
// resolves at tier two, without a stat, and reports the version with Source
// working-tree -- a deploy describing itself as something it is not.
func TestSeedPutsArtifactsInArtifactsAndNotInPaths(t *testing.T) {
	t.Parallel()

	repoHome := t.TempDir()
	elsewhere := filepath.Join(t.TempDir(), "vendored")
	r := NewWithHome(repoHome, "", repoHomeLookup(repoHome))
	Seed(r, []manifest.Repo{
		{InstanceName: "tree", RepoClassName: "hmd-ms-tree", Source: &manifest.Source{Path: elsewhere}},
		{InstanceName: "art", RepoClassName: "hmd-inf-art", Version: "0.1.4",
			Source: &manifest.Source{Type: manifest.SourceArtifact}},
		// Declared but never fetched. The version is recorded either way: an
		// artifact is addressed by version, and whether its bytes are here is a
		// separate question asked before the deploy starts.
		{InstanceName: "gone", RepoClassName: "hmd-inf-gone", Version: "0.2.0",
			Source: &manifest.Source{Type: manifest.SourceArtifact}},
	})

	wantPaths := map[string]string{"hmd-ms-tree": elsewhere}
	if !reflect.DeepEqual(r.Paths, wantPaths) {
		t.Errorf("Paths = %v, want %v", r.Paths, wantPaths)
	}
	wantArtifacts := map[string]string{"hmd-inf-art": "0.1.4", "hmd-inf-gone": "0.2.0"}
	if !reflect.DeepEqual(r.Artifacts, wantArtifacts) {
		t.Errorf("Artifacts = %v, want %v", r.Artifacts, wantArtifacts)
	}

	// The consequence, stated as the caller sees it: an artifact declaration
	// that had leaked into Paths would resolve here as a working tree.
	if got := r.ResolveVersion("hmd-inf-art", ""); got.Source != SourceArtifact || got.Version != "0.1.4" {
		t.Errorf("ResolveVersion = %+v, want 0.1.4 from an artifact", got)
	}
}

// A resolver built as a literal rather than through New has nil maps and a nil
// Lookup, and os.Expand panics on the latter.
func TestSeedOnABareResolver(t *testing.T) {
	t.Parallel()

	r := &Resolver{}
	Seed(r, []manifest.Repo{
		{InstanceName: "art", RepoClassName: "hmd-inf-art", Version: "0.1.4",
			Source: &manifest.Source{Type: manifest.SourceArtifact}},
		{InstanceName: "tree", RepoClassName: "hmd-ms-tree", Source: &manifest.Source{Path: "/vendored/hmd-ms-tree"}},
	})
	if r.Artifacts["hmd-inf-art"] != "0.1.4" {
		t.Errorf("Artifacts = %v, want the declared version", r.Artifacts)
	}
	if r.Paths["hmd-ms-tree"] != "/vendored/hmd-ms-tree" {
		t.Errorf("Paths = %v, want the declared tree", r.Paths)
	}
}
