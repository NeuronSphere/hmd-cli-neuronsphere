package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/librarian"
)

// writeTree lays down a minimal repo tree: the manifest naming the class, a
// VERSION, and one source file whose contents identify which tier produced it.
func writeTree(t *testing.T, root, class, version, marker string) string {
	t.Helper()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(root, "meta-data"), 0o755))
	must(os.MkdirAll(filepath.Join(root, "src", "cdktf"), 0o755))
	must(os.WriteFile(filepath.Join(root, "meta-data", "manifest.json"),
		[]byte(`{"name": "`+class+`"}`), 0o644))
	must(os.WriteFile(filepath.Join(root, "meta-data", "VERSION"), []byte(version), 0o644))
	must(os.WriteFile(filepath.Join(root, "src", "cdktf", "stack.py"), []byte(marker), 0o644))
	return root
}

func manifestWith(t *testing.T, entries string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, []byte(`{"build": {"pre_build_artifacts": [`+entries+`]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestLoadPinsKeysByRepoClassNotDirectory is the asymmetry the whole tool turns
// on: the spec names the repo class, the destination beside it names the
// plugin. Keying on the directory silently mismatches every aliased artifact.
func TestLoadPinsKeysByRepoClassNotDirectory(t *testing.T) {
	t.Parallel()

	path := manifestWith(t, `
		["hmd-inf-ext-secrets@0.2.54:build", "src/python/hmd_cli_neuronsphere/external/ext-secrets"],
		["hmd-inf-superset@0.5.228:build", "src/python/hmd_cli_neuronsphere/external/apache_superset"]`)

	pins, err := loadPins(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(pins) != 2 {
		t.Fatalf("loadPins() returned %d pins, want 2", len(pins))
	}
	if got := pins["hmd-inf-ext-secrets"].Version; got != "0.2.54" {
		t.Errorf("pins[hmd-inf-ext-secrets].Version = %q, want 0.2.54", got)
	}
	if _, aliased := pins["ext-secrets"]; aliased {
		t.Error("loadPins keyed a pin by its destination directory")
	}
	if got := pins["hmd-inf-superset"].ContentPath(); !strings.Contains(got, "hmd-inf-superset_0.5.228_build.zip") {
		t.Errorf("ContentPath() = %q", got)
	}
}

func TestLoadPinsRejectsAMalformedSpec(t *testing.T) {
	t.Parallel()

	if _, err := loadPins(manifestWith(t, `["hmd-vpc:build", "dest"]`)); err == nil {
		t.Error("loadPins accepted a spec with no version")
	}
	if _, err := loadPins(""); err == nil {
		t.Error("loadPins accepted an empty -manifest")
	}
}

// TestLocatePrefersTheArtifactDirectory: a Python build that already ran costs
// nothing to reuse, and the tool must not reach for the network behind it.
func TestLocatePrefersTheArtifactDirectory(t *testing.T) {
	t.Parallel()

	artifacts := t.TempDir()
	// Deliberately a directory named for the plugin, holding a manifest naming
	// the class -- the shape `hmd build` actually produces.
	writeTree(t, filepath.Join(artifacts, "the-alias"), "hmd-vpc", "0.2.41", "from the artifact dir")

	cache := t.TempDir()
	writeTree(t, filepath.Join(cache, "hmd-vpc@0.2.41"), "hmd-vpc", "0.2.41", "from the cache")

	o := options{
		artifacts: artifacts,
		cache:     cache,
		manifest:  manifestWith(t, `["hmd-vpc@0.2.41:build", "dest"]`),
	}
	pins, err := loadPins(o.manifest)
	if err != nil {
		t.Fatal(err)
	}

	dir, source, err := locate(o, "hmd-vpc", pins, manifestIndex(artifacts), noClient(t))
	if err != nil {
		t.Fatal(err)
	}
	if source != "artifact" {
		t.Errorf("source = %q, want artifact", source)
	}
	assertMarker(t, dir, "from the artifact dir")
}

func TestLocateFallsBackToTheCacheWithoutFetching(t *testing.T) {
	t.Parallel()

	cache := t.TempDir()
	writeTree(t, filepath.Join(cache, "hmd-vpc@0.2.41"), "hmd-vpc", "0.2.41", "from the cache")

	o := options{cache: cache, manifest: manifestWith(t, `["hmd-vpc@0.2.41:build", "dest"]`)}
	pins, err := loadPins(o.manifest)
	if err != nil {
		t.Fatal(err)
	}

	dir, source, err := locate(o, "hmd-vpc", pins, nil, noClient(t))
	if err != nil {
		t.Fatal(err)
	}
	if source != "cached" {
		t.Errorf("source = %q, want cached", source)
	}
	assertMarker(t, dir, "from the cache")
}

// TestLocateCacheIsKeyedOnTheVersion: a pin that moves must refetch. A cache
// keyed on the class alone would serve the old tree forever.
func TestLocateCacheIsKeyedOnTheVersion(t *testing.T) {
	t.Parallel()

	cache := t.TempDir()
	writeTree(t, filepath.Join(cache, "hmd-vpc@0.2.40"), "hmd-vpc", "0.2.40", "the old version")

	o := options{cache: cache, offline: true, manifest: manifestWith(t, `["hmd-vpc@0.2.41:build", "dest"]`)}
	pins, err := loadPins(o.manifest)
	if err != nil {
		t.Fatal(err)
	}

	_, _, err = locate(o, "hmd-vpc", pins, nil, noClient(t))
	if err == nil {
		t.Fatal("locate() served a cache entry for a different version")
	}
	if !strings.Contains(err.Error(), "hmd-vpc@0.2.41") {
		t.Errorf("the error does not name the version wanted: %v", err)
	}
}

// TestLocateUndeclaredClassSaysWhereToDeclareIt. With the committed trees gone
// this is the failure a new BUNDLED_REPOS entry hits, and "no such file" would
// send the reader looking in the wrong place entirely.
func TestLocateUndeclaredClassSaysWhereToDeclareIt(t *testing.T) {
	t.Parallel()

	o := options{cache: t.TempDir(), manifest: manifestWith(t, `["hmd-vpc@0.2.41:build", "dest"]`)}
	pins, err := loadPins(o.manifest)
	if err != nil {
		t.Fatal(err)
	}

	_, _, err = locate(o, "hmd-ms-newcomer", pins, nil, noClient(t))
	if err == nil {
		t.Fatal("locate() accepted a class with no pin")
	}
	for _, want := range []string{"hmd-ms-newcomer", "pre_build_artifacts", o.manifest} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q: %v", want, err)
		}
	}
}

// TestUnavailableNamesEveryPlaceLooked. Three different problems -- never
// fetched, librarian down, credential expired -- otherwise arrive identically.
func TestUnavailableNamesEveryPlaceLooked(t *testing.T) {
	t.Parallel()

	spec, err := librarian.ParseSpec("hmd-vpc@0.2.41:build")
	if err != nil {
		t.Fatal(err)
	}
	o := options{artifacts: "/some/external", cache: "/some/cache", manifest: "/some/manifest.json"}
	msg := unavailable(o, spec, "/some/cache/hmd-vpc@0.2.41", librarian.ErrNoCredentials).Error()

	for _, want := range []string{
		"hmd-vpc@0.2.41", "/some/external", "/some/cache/hmd-vpc@0.2.41", "hmd login",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("the error does not mention %q:\n%s", want, msg)
		}
	}

	published := unavailable(o, spec, "/c", librarian.ErrNotPublished).Error()
	if !strings.Contains(published, "no such version") {
		t.Errorf("an unpublished version should say so:\n%s", published)
	}
}

// TestPackIsDeterministic. The archive digest names the directory repotree
// unpacks into, so a tarball that churned would re-unpack on every start.
func TestPackIsDeterministic(t *testing.T) {
	t.Parallel()

	root := writeTree(t, t.TempDir(), "hmd-vpc", "0.2.41", "stable")
	out := t.TempDir()

	first := filepath.Join(out, "a.tar.gz")
	second := filepath.Join(out, "b.tar.gz")
	if _, err := pack(root, first); err != nil {
		t.Fatal(err)
	}
	if _, err := pack(root, second); err != nil {
		t.Fatal(err)
	}

	a, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Error("two packs of the same tree produced different bytes")
	}
}

// noClient fails the test if a tier reaches for the librarian.
func noClient(t *testing.T) func() (*librarian.Client, error) {
	t.Helper()
	return func() (*librarian.Client, error) {
		return nil, errors.New("the test did not expect a fetch")
	}
}

func assertMarker(t *testing.T, dir, want string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "src", "cdktf", "stack.py"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Errorf("resolved to the tree marked %q, want %q", data, want)
	}
}

// TestPackLeavesServiceCodeOut is the licence boundary: the descriptor paths
// are Apache 2.0 everywhere, src/python and friends are BUSL 1.1 in the three
// service repos, and the Apache-licensed binary must never embed the latter,
// even when an artifact happens to contain them.
func TestPackLeavesServiceCodeOut(t *testing.T) {
	t.Parallel()

	root := writeTree(t, t.TempDir(), "hmd-ms-deployment", "0.4.1", "descriptor")
	for _, rel := range []string{
		"src/python/hmd_ms_deployment/deploy_logic.py",
		"src/typescript/index.ts",
		"src/docker/Dockerfile",
		"src/helm/chart/Chart.yaml",
		"src/local/scripts/python/seed.sh", // "python" below the top level is not service code
	} {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	files, err := collect(root)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(files, "\n")
	for _, banned := range []string{"src/python/", "src/typescript/", "src/docker/"} {
		if strings.Contains(got, banned) {
			t.Errorf("packed %s:\n%s", banned, got)
		}
	}
	for _, kept := range []string{"src/cdktf/stack.py", "src/helm/chart/Chart.yaml", "src/local/scripts/python/seed.sh", "meta-data/VERSION"} {
		if !strings.Contains(got, kept) {
			t.Errorf("dropped %s:\n%s", kept, got)
		}
	}
}
