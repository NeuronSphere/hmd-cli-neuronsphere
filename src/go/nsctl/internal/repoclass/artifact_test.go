package repoclass

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/artifact"
)

// cacheArtifact unpacks a minimal artifact into home's cache, as `nsctl
// artifact pull` would.
func cacheArtifact(t *testing.T, home, class, version, manifest string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	files := map[string]string{
		"meta-data/manifest.json": manifest,
		"meta-data/VERSION":       version,
		"src/cdktf/stack.py":      "from the artifact",
	}
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	dir, err := artifact.Store(home, class, version, buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestAnArtifactBeatsAnUnaskedForCheckout is NERD005 SPEC002's reversal of the
// last existing tier, and the only decision in that document not forced by prior
// art. The manifest asked for a version; a checkout that happens to be lying
// around is not that version, and substituting it produces a deploy that reports
// a version it did not use.
func TestAnArtifactBeatsAnUnaskedForCheckout(t *testing.T) {
	t.Parallel()

	repos := repoHome(t, "hmd-inf-x", "9.9.9-from-the-checkout", `{"name": "hmd-inf-x"}`)
	home := t.TempDir()
	dir := cacheArtifact(t, home, "hmd-inf-x", "0.1.4", `{"name": "hmd-inf-x"}`)

	r := NewWithHome(repos, home, fakeEnv(nil))
	r.Artifacts["hmd-inf-x"] = "0.1.4"

	got := r.ResolveVersion("hmd-inf-x", "")
	if got.Version != "0.1.4" || got.Source != SourceArtifact {
		t.Errorf("ResolveVersion = %+v, want 0.1.4 from the artifact", got)
	}
	if got.Root != dir {
		t.Errorf("Root = %q, want the unpacked artifact at %q", got.Root, dir)
	}
	if d := r.Dir("hmd-inf-x"); d != dir {
		t.Errorf("Dir = %q, want the unpacked artifact -- deploying the checkout under the artifact's version"+
			" is the failure this tier exists to prevent", d)
	}
	if r.PreemptedArtifact("hmd-inf-x") != "" {
		t.Error("PreemptedArtifact named a checkout nobody asked for")
	}
}

// Development beats distribution. The entire purpose of $HMD_REPO_HOME is to run
// uncommitted changes, and an artifact source that overrode it would make the
// platform untestable by the people who build it.
func TestACheckoutTheUserAskedForBeatsAnArtifact(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		env  map[string]string
		path bool
	}{
		{name: "PREFER_LOCAL_VERSIONS", env: map[string]string{PreferLocalVersionsEnv: "true"}},
		{name: "the per-class variable set to local", env: map[string]string{"HMD_LOCAL_VERSION_HMD_INF_X": "local"}},
		{name: "a source path the manifest declared", path: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			repos := repoHome(t, "hmd-inf-x", "9.9.9", `{"name": "hmd-inf-x"}`)
			home := t.TempDir()
			cacheArtifact(t, home, "hmd-inf-x", "0.1.4", `{"name": "hmd-inf-x"}`)

			r := NewWithHome(repos, home, fakeEnv(tt.env))
			r.Artifacts["hmd-inf-x"] = "0.1.4"
			tree := filepath.Join(repos, "hmd-inf-x")
			if tt.path {
				r.Paths["hmd-inf-x"] = tree
			}

			got := r.ResolveVersion("hmd-inf-x", "")
			if got.Version != "9.9.9" || got.Source != SourceWorkingTree {
				t.Errorf("ResolveVersion = %+v, want the working tree's 9.9.9", got)
			}
			// Said out loud on the way past: the substitution is invisible until
			// it has cost an afternoon, and the user asked for it by setting the
			// variable.
			if p := r.PreemptedArtifact("hmd-inf-x"); p != tree {
				t.Errorf("PreemptedArtifact = %q, want %q so the caller can announce it", p, tree)
			}
		})
	}
}

// An explicit pin still wins, and pre-empts nothing worth announcing: the user
// named a version, which is the same kind of thing an artifact source names.
func TestAnExplicitPinBeatsAnArtifact(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cacheArtifact(t, home, "hmd-inf-x", "0.1.4", `{"name": "hmd-inf-x"}`)

	r := NewWithHome("", home, fakeEnv(map[string]string{"HMD_LOCAL_VERSION_HMD_INF_X": "7.7.7"}))
	r.Artifacts["hmd-inf-x"] = "0.1.4"

	if got := r.ResolveVersion("hmd-inf-x", ""); got.Version != "7.7.7" || got.Source != SourcePin {
		t.Errorf("ResolveVersion = %+v, want the pin", got)
	}
	if p := r.PreemptedArtifact("hmd-inf-x"); p != "" {
		t.Errorf("PreemptedArtifact = %q for a pin, which names a version rather than a tree", p)
	}
}

// An uncached artifact still reports the version the manifest asked for: an
// artifact is addressed *by* version, so that version is known either way.
// Whether the bytes are here is a different question, asked before the deploy
// starts.
func TestAnUncachedArtifactStillReportsItsVersion(t *testing.T) {
	t.Parallel()

	repos := repoHome(t, "hmd-inf-x", "9.9.9", `{"name": "hmd-inf-x", "deploy": {"dependencies": {"db": {}}}}`)
	r := NewWithHome(repos, t.TempDir(), fakeEnv(nil))
	r.Artifacts["hmd-inf-x"] = "0.1.4"

	got := r.ResolveVersion("hmd-inf-x", "")
	if got.Version != "0.1.4" || got.Source != SourceArtifact {
		t.Errorf("ResolveVersion = %+v, want the declared 0.1.4 from the artifact", got)
	}
	if got.Root != "" {
		t.Errorf("Root = %q, want nothing -- the artifact was never fetched", got.Root)
	}
	if d := r.Dir("hmd-inf-x"); d != "" {
		t.Errorf("Dir = %q; falling back to a checkout here is the silent substitution"+
			" the whole tier exists to refuse", d)
	}
	// And the dependencies must not come from the checkout either: reading one
	// build's dependencies under another build's version describes a build that
	// never existed.
	version, deps, _, err := r.Resolve("hmd-inf-x", "")
	if err != nil {
		t.Fatal(err)
	}
	if version != "0.1.4" {
		t.Errorf("Resolve version = %q", version)
	}
	if deps != nil {
		t.Errorf("Resolve returned %v from a tree that is not the artifact", deps)
	}
}

// The cached tree is where the version, the dependencies and the deploy sources
// all come from, which is the invariant the package states.
func TestResolveReadsTheArtifactsOwnManifest(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cacheArtifact(t, home, "hmd-inf-x", "0.1.4",
		`{"name": "hmd-inf-x", "deploy": {"dependencies": {"cluster": {"repo_class_name": "hmd-inf-eks-cluster"}}}}`)

	r := NewWithHome("", home, fakeEnv(nil))
	r.Artifacts["hmd-inf-x"] = "0.1.4"

	version, deps, _, err := r.Resolve("hmd-inf-x", "")
	if err != nil {
		t.Fatal(err)
	}
	if version != "0.1.4" {
		t.Errorf("version = %q", version)
	}
	if _, ok := deps["cluster"]; !ok {
		t.Errorf("dependencies = %v, want the artifact's own", deps)
	}
}

// A class with no artifact declared resolves exactly as it did before.
func TestNoArtifactDeclaredChangesNothing(t *testing.T) {
	t.Parallel()

	repos := repoHome(t, "hmd-inf-x", "9.9.9", `{"name": "hmd-inf-x"}`)
	home := t.TempDir()
	cacheArtifact(t, home, "hmd-inf-x", "0.1.4", `{"name": "hmd-inf-x"}`)

	// Cached, but no manifest asked for it.
	r := NewWithHome(repos, home, fakeEnv(nil))
	if got := r.ResolveVersion("hmd-inf-x", "0.2.0"); got.Source != SourceDeclared || got.Version != "0.2.0" {
		t.Errorf("ResolveVersion = %+v, want the BOM entry's declared version", got)
	}
	if _, err := os.Stat(artifact.Dir(home, "hmd-inf-x", "0.1.4")); err != nil {
		t.Fatalf("the fixture did not cache anything: %v", err)
	}
}
