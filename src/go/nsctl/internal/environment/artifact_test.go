package environment

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/artifact"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/repoclass"
)

func cacheArtifact(t *testing.T, home, class, version string) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{
		"meta-data/manifest.json": `{"name": "` + class + `"}`,
		"meta-data/VERSION":       version,
	} {
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
	if _, err := artifact.Store(home, class, version, buf.Bytes()); err != nil {
		t.Fatal(err)
	}
}

func artifactRepo(instance, class, version string) manifest.Repo {
	return manifest.Repo{
		InstanceName: instance, RepoClassName: class, Version: version,
		Source: &manifest.Source{Type: manifest.SourceArtifact},
	}
}

// An apply that names versions nothing has fetched says so in seconds and names
// all of them, rather than failing on the first nine hundred log lines in with a
// message about a missing directory.
func TestCheckArtifactsNamesEveryUncachedVersion(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cacheArtifact(t, home, "hmd-inf-here", "0.1.4")

	var errBuf strings.Builder
	opts := &Options{Home: home, Err: &errBuf, Lookup: func(string) string { return "" }}
	resolver := repoclass.NewWithHome("", home, opts.Lookup)
	repos := []manifest.Repo{
		artifactRepo("here", "hmd-inf-here", "0.1.4"),
		artifactRepo("gone", "hmd-inf-gone", "0.2.0"),
		artifactRepo("also-gone", "hmd-inf-also-gone", "0.3.0"),
		{InstanceName: "local", RepoClassName: "hmd-inf-local"},
	}
	for _, r := range repos {
		if r.SourceType() == manifest.SourceArtifact {
			resolver.Artifacts[r.RepoClassName] = r.Version
		}
	}

	err := checkArtifacts(opts, resolver, repos)
	if err == nil {
		t.Fatal("checkArtifacts accepted a manifest naming two uncached versions")
	}
	msg := err.Error()
	for _, want := range []string{"hmd-inf-gone@0.2.0", "hmd-inf-also-gone@0.3.0",
		"nsctl artifact pull hmd-inf-gone@0.2.0:build"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the message does not mention %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "hmd-inf-here") {
		t.Errorf("a cached version was reported as missing:\n%s", msg)
	}
	// Resolution never reaches the network, so a message implying a 404 would
	// invite the reader to debug one that was never involved.
	if !strings.Contains(msg, "not contacted") {
		t.Errorf("the message does not say that nothing was contacted:\n%s", msg)
	}
}

// A working tree the user explicitly asked for still wins -- and nsctl says so
// on the way past, because the substitution is otherwise invisible until it has
// cost an afternoon.
func TestCheckArtifactsAnnouncesAPreemptingCheckout(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	var errBuf strings.Builder
	opts := &Options{Home: home, Err: &errBuf, Lookup: func(string) string { return "" }}

	resolver := repoclass.NewWithHome("", home, opts.Lookup)
	resolver.Artifacts["hmd-inf-x"] = "0.1.4"
	resolver.Paths["hmd-inf-x"] = "/work/hmd-inf-x"

	// Uncached, and still not an error: the checkout is what will be deployed.
	if err := checkArtifacts(opts, resolver, []manifest.Repo{artifactRepo("x", "hmd-inf-x", "0.1.4")}); err != nil {
		t.Fatalf("checkArtifacts failed for an artifact a checkout pre-empts: %v", err)
	}
	warning := errBuf.String()
	for _, want := range []string{"/work/hmd-inf-x", "hmd-inf-x", "0.1.4"} {
		if !strings.Contains(warning, want) {
			t.Errorf("the warning does not mention %q:\n%s", want, warning)
		}
	}
}

// A manifest with no artifact sources is unaffected, which is every manifest
// that exists today.
func TestCheckArtifactsIgnoresLocalSources(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	opts := &Options{Home: home, Lookup: func(string) string { return "" }}
	repos := []manifest.Repo{
		{InstanceName: "a", RepoClassName: "hmd-ms-a"},
		{InstanceName: "b", RepoClassName: "hmd-ms-b", Source: &manifest.Source{Type: manifest.SourceLocal}},
	}
	if err := checkArtifacts(opts, repoclass.NewWithHome("", home, opts.Lookup), repos); err != nil {
		t.Fatalf("checkArtifacts failed for a manifest with no artifact sources: %v", err)
	}
}

// bom.RepoPaths drops every artifact instance, because manifest.Repo.RepoPath
// answers "" for a non-local source. artifact.Paths is what fills the gap, and
// without the merge the runner falls through to its own tiers and deploys
// whatever tree it finds under the version the resolver reported.
func TestArtifactPathsFillTheGapBomRepoPathsLeaves(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cacheArtifact(t, home, "hmd-inf-x", "0.1.4")
	repos := []manifest.Repo{artifactRepo("x", "hmd-inf-x", "0.1.4")}

	if got := (manifest.Repo{}); got.RepoPath(func(string) string { return "" }) != "" {
		t.Fatal("the premise of this test has changed")
	}
	if path := repos[0].RepoPath(func(string) string { return "/repos" }); path != "" {
		t.Errorf("RepoPath = %q for an artifact source, want \"\" -- this is why the merge exists", path)
	}
	paths := artifact.Paths(home, repos)
	if paths["hmd-inf-x"] != artifact.Dir(home, "hmd-inf-x", "0.1.4") {
		t.Errorf("artifact.Paths = %v", paths)
	}
}
