package artifact

import (
	"testing"

	"github.com/opencontainers/go-digest"
)

// NERD017 SPEC007: Store keeps the zip's digest beside the unpacked tree so a
// lock can be filled from the cache and a stack can cross-check it.

func TestStoreRecordsTheZipDigestBesideTheTree(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	data := zipOf(t, map[string]string{"meta-data/manifest.json": "{}", "meta-data/VERSION": "0.1"})

	if _, ok := Digest(home, "hmd-inf-a", "0.1.0"); ok {
		t.Fatal("no digest before Store")
	}
	if _, err := Store(home, "hmd-inf-a", "0.1.0", data); err != nil {
		t.Fatal(err)
	}
	got, ok := Digest(home, "hmd-inf-a", "0.1.0")
	if !ok || got != digest.FromBytes(data).String() {
		t.Errorf("Digest = %q, %v; want %s", got, ok, digest.FromBytes(data))
	}
	if err := Invalidate(home, "hmd-inf-a", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	if _, ok := Digest(home, "hmd-inf-a", "0.1.0"); ok {
		t.Error("Invalidate must drop the digest with the tree")
	}
}

func TestDigestIsAbsentForATreeStoredWithoutOne(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	data := zipOf(t, map[string]string{"meta-data/manifest.json": "{}", "meta-data/VERSION": "0.1"})
	if _, err := Store(home, "hmd-inf-a", "0.1.0", data); err != nil {
		t.Fatal(err)
	}
	// Simulate a tree unpacked by an older nsctl: the sidecar is gone but the
	// tree is cached. Digest says so rather than guessing.
	if err := removeSidecar(home, "hmd-inf-a", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	if !Cached(home, "hmd-inf-a", "0.1.0") {
		t.Fatal("tree must still be cached")
	}
	if _, ok := Digest(home, "hmd-inf-a", "0.1.0"); ok {
		t.Error("digest must be absent")
	}
}

func TestStoreKeepsTheOriginalZip(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	data := zipOf(t, map[string]string{"meta-data/manifest.json": "{}", "meta-data/VERSION": "0.1"})
	if _, err := Store(home, "hmd-inf-a", "0.1.0", data); err != nil {
		t.Fatal(err)
	}
	got, ok := Zipped(home, "hmd-inf-a", "0.1.0")
	if !ok || digest.FromBytes(got) != digest.FromBytes(data) {
		t.Errorf("Zipped = %v, ok %v", len(got), ok)
	}
	if err := removeSidecar(home, "hmd-inf-a", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	if _, ok := Zipped(home, "hmd-inf-a", "0.1.0"); ok {
		t.Error("an older cache has no zip")
	}
}
