package artifact

import (
	"archive/zip"
	"bytes"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
)

// zipOf builds an artifact zip from slash-separated paths to contents, which is
// what the librarian hands back for every content item type it holds.
func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
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
	return buf.Bytes()
}

// tree is a minimal valid artifact: the two files validate insists on, and one
// source file whose contents say which build produced it.
func tree(marker string) map[string]string {
	return map[string]string{
		"meta-data/manifest.json": `{"name": "hmd-inf-local-registry"}`,
		"meta-data/VERSION":       "0.1",
		"src/cdktf/stack.py":      marker,
	}
}

// TestStoreRegisterRoundTrip is SPEC005's acceptance test at the unit level:
// build, register, deploy -- then change the code, register again, and deploy
// the *new* tree. Without Invalidate the second apply runs the first build's
// code under the second build's version, which is the failure SPEC002 refuses to
// allow from the other direction.
func TestStoreRegisterRoundTrip(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	const class, version = "hmd-inf-local-registry", "0.1.4"
	marker := filepath.Join("src", "cdktf", "stack.py")

	if Cached(home, class, version) {
		t.Fatal("a cold cache reported a hit")
	}
	dir, err := Store(home, class, version, zipOf(t, tree("first build")))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(dir, Root(home)) {
		t.Errorf("unpacked to %s, outside %s; the runner container mounts HMD_HOME and nothing else", dir, Root(home))
	}
	if !Cached(home, class, version) {
		t.Error("Cached is false immediately after Store")
	}
	if got := read(t, filepath.Join(dir, marker)); got != "first build" {
		t.Errorf("stack.py = %q, want the first build", got)
	}

	// Re-storing without invalidating keeps the cached tree: present means no
	// fetch, and that is what makes an apply on a disconnected machine work.
	if _, err := Store(home, class, version, zipOf(t, tree("second build"))); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(dir, marker)); got != "first build" {
		t.Errorf("stack.py = %q after a re-Store; a cached version must not be re-unpacked", got)
	}

	if err := Invalidate(home, class, version); err != nil {
		t.Fatal(err)
	}
	if Cached(home, class, version) {
		t.Fatal("Cached is true after Invalidate")
	}
	if _, err := Store(home, class, version, zipOf(t, tree("second build"))); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(dir, marker)); got != "second build" {
		t.Errorf("stack.py = %q after invalidate-and-re-store, want the second build", got)
	}
}

// The token in the directory name is the version, so two versions of one class
// coexist rather than overwriting each other.
func TestTwoVersionsCoexist(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	const class = "hmd-inf-local-registry"
	old, err := Store(home, class, "0.1.4", zipOf(t, tree("old")))
	if err != nil {
		t.Fatal(err)
	}
	recent, err := Store(home, class, "0.1.5", zipOf(t, tree("new")))
	if err != nil {
		t.Fatal(err)
	}
	if old == recent {
		t.Fatalf("both versions unpacked to %s", old)
	}
	if got := read(t, filepath.Join(old, "src", "cdktf", "stack.py")); got != "old" {
		t.Errorf("0.1.4 holds %q", got)
	}
}

// A tree that is not a repo fails here, naming the path that was missing, rather
// than deep inside a deploy node where the cause is a broken bind mount.
func TestStoreRejectsATreeThatIsNotARepo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{
			name:  "no manifest",
			files: map[string]string{"meta-data/VERSION": "0.1", "src/x.py": "x"},
			want:  "meta-data/manifest.json",
		},
		{
			name:  "no VERSION",
			files: map[string]string{"meta-data/manifest.json": "{}", "src/x.py": "x"},
			want:  "meta-data/VERSION",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			home := t.TempDir()
			_, err := Store(home, "hmd-inf-local-registry", "0.1.4", zipOf(t, tt.files))
			if err == nil {
				t.Fatal("Store accepted a tree that is not a repo")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want it to name %s", err, tt.want)
			}
			if Cached(home, "hmd-inf-local-registry", "0.1.4") {
				t.Error("a rejected tree was left in the cache")
			}
			if residue(t, Root(home)) {
				t.Error("a rejected unpack left a .unpacking directory behind")
			}
		})
	}
}

// src/ is deliberately not required: a manifest may name a schema or
// configuration artifact, and those legitimately have no sources. This narrows
// SPEC003's Medium risk row on purpose, so it is pinned rather than incidental.
func TestASchemaArtifactNeedsNoSrc(t *testing.T) {
	t.Parallel()

	_, err := Store(t.TempDir(), "hmd-lang-foo", "0.3.1", zipOf(t, map[string]string{
		"meta-data/manifest.json":   `{"name": "hmd-lang-foo"}`,
		"meta-data/VERSION":         "0.3",
		"schemas/thing.schema.json": "{}",
	}))
	if err != nil {
		t.Fatalf("Store rejected a schema artifact: %v", err)
	}
}

// An entry naming a path outside the destination is refused rather than written.
func TestUnzipIntoRefusesTraversal(t *testing.T) {
	t.Parallel()

	dest := filepath.Join(t.TempDir(), "tree")
	err := UnzipInto(zipOf(t, map[string]string{"../escaped.txt": "nope"}), dest)
	if err == nil {
		t.Fatal("UnzipInto accepted an entry outside the archive root")
	}
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(dest), "escaped.txt")); statErr == nil {
		t.Error("the escaping entry was written")
	}
}

// Deploy nodes of different repo classes run concurrently, so two of them
// arriving at a cold cache race on the same staging directory. The mutex is the
// mechanism SPEC003 names; this is the test that it is actually load-bearing.
func TestConcurrentStoresProduceOneTree(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	const class, version, n = "hmd-inf-local-registry", "0.1.4", 8

	var wg sync.WaitGroup
	dirs := make([]string, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			dirs[i], errs[i] = Store(home, class, version, zipOf(t, tree("build")))
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
		if dirs[i] != dirs[0] {
			t.Errorf("goroutine %d unpacked to %s, goroutine 0 to %s", i, dirs[i], dirs[0])
		}
	}
	entries, err := os.ReadDir(Root(home))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("the cache holds %v, want exactly one tree", names)
	}
	if residue(t, Root(home)) {
		t.Error("a concurrent unpack left a .unpacking directory behind")
	}
}

// Paths feeds repoPaths, and an entry there is a bind mount. Only an instance
// that is both artifact-sourced and already unpacked has one.
func TestPathsMapsOnlyArtifactSourcedAndCached(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	if _, err := Store(home, "hmd-inf-cached", "0.1.4", zipOf(t, tree("x"))); err != nil {
		t.Fatal(err)
	}
	if _, err := Store(home, "hmd-inf-local", "0.2.0", zipOf(t, tree("x"))); err != nil {
		t.Fatal(err)
	}

	paths := Paths(home, []manifest.Repo{
		{InstanceName: "cached", RepoClassName: "hmd-inf-cached", Version: "0.1.4",
			Source: &manifest.Source{Type: manifest.SourceArtifact}},
		{InstanceName: "uncached", RepoClassName: "hmd-inf-uncached", Version: "9.9.9",
			Source: &manifest.Source{Type: manifest.SourceArtifact}},
		// Unpacked, but declared local: a working tree the user asked for wins,
		// and handing the runner a cached artifact here would silently deploy
		// something other than their uncommitted changes.
		{InstanceName: "local", RepoClassName: "hmd-inf-local", Version: "0.2.0"},
	})

	if len(paths) != 1 {
		t.Fatalf("Paths() = %v, want only the cached artifact instance", paths)
	}
	if got := paths["hmd-inf-cached"]; got != Dir(home, "hmd-inf-cached", "0.1.4") {
		t.Errorf("paths[hmd-inf-cached] = %q", got)
	}
}

// The cache is reconstructible from the librarian and is shared by every
// environment, so it lives outside any environment's state directory -- which is
// what makes `nsctl env purge` cost a refetch and nothing else.
func TestTheCacheIsOutsideAnyEnvironmentStateDirectory(t *testing.T) {
	t.Parallel()

	home := "/somewhere/hmd"
	stateDirs := filepath.Join(home, ".cache", "environments")
	if strings.HasPrefix(Root(home), stateDirs) {
		t.Errorf("the artifact cache at %s lies under %s, so purging an environment would delete it",
			Root(home), stateDirs)
	}
}

func TestZipIsDeterministicAndExcludesBuildOutput(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	write(t, root, "meta-data/manifest.json", `{"name": "hmd-inf-local-registry"}`)
	write(t, root, "meta-data/VERSION", "0.1")
	write(t, root, "src/cdktf/stack.py", "stack")
	write(t, root, "test/suite.robot", "*** Settings ***")
	write(t, root, "README.md", "readme")
	// The exclusion that matters: a deploy's output left in a checkout would
	// have every environment submitting whoever's deploy produced it.
	write(t, root, "meta-data/resources_output/cluster.json", `{"leaked": true}`)
	write(t, root, "build/wheel.whl", "binary")
	write(t, root, ".git/config", "[core]")

	first, err := Zip(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Zip(root)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Error("two Zips of one tree differ; a version's bytes must not move under it")
	}

	names := entryNames(t, first)
	for _, want := range []string{"meta-data/manifest.json", "meta-data/VERSION", "src/cdktf/stack.py",
		"test/suite.robot", "README.md"} {
		if !names[want] {
			t.Errorf("%s is missing from the zip", want)
		}
	}
	for _, unwanted := range []string{"meta-data/resources_output/cluster.json", "build/wheel.whl", ".git/config"} {
		if names[unwanted] {
			t.Errorf("%s was packed", unwanted)
		}
	}
}

// A zipped tree unpacks to the same tree, which is the whole contract between
// `register` and the cache.
func TestZipRoundTripsThroughStore(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	write(t, root, "meta-data/manifest.json", `{"name": "hmd-inf-local-registry"}`)
	write(t, root, "meta-data/VERSION", "0.1")
	write(t, root, "src/cdktf/stack.py", "stack")

	data, err := Zip(root)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := Store(t.TempDir(), "hmd-inf-local-registry", "0.1.4", data)
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(dir, "src", "cdktf", "stack.py")); got != "stack" {
		t.Errorf("stack.py = %q after a round trip", got)
	}
}

// The four causes SPEC007 insists are distinguishable. All four arrive at a
// caller as one failed request otherwise, and a bare 404 sends the reader to
// debug whichever they think of first.
func TestUnavailableTellsTheCausesApart(t *testing.T) {
	t.Parallel()

	const class, version = "hmd-inf-local-registry", "0.1.4"
	contentPath := "repository:/" + class + "/" + version + "/" + class + "_" + version + "_build.zip"

	tests := []struct {
		name     string
		cause    error
		want     []string
		unwanted []string
	}{
		{
			name:  "nothing cached, nothing contacted",
			cause: nil,
			want:  []string{class + "@" + version, "not contacted", "artifact cache"},
			// Resolution never reaches the network, so a message implying a 404
			// invites the reader to debug one that was never involved.
			unwanted: []string{"404"},
		},
		{
			name:  "the librarian answered, and has no such version",
			cause: errors.New("HTTP 404"),
			want:  []string{contentPath, "404", "nsctl control-plane status"},
		},
		{
			name:  "the presigned host does not resolve",
			cause: &net.DNSError{Name: "neuronsphere", Err: "no such host"},
			want:  []string{"neuronsphere", "/etc/hosts", "127.0.0.1", "nsctl doctor"},
			// nsctl redirects these names itself, so reaching this branch means
			// something other than a missing hosts entry; the message must not
			// send the reader straight back to the old remedy as if it were the
			// whole story.
			unwanted: []string{"and try again"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			msg := Unavailable("/hmd", class, version, contentPath, "/repos", tt.cause).Error()
			for _, want := range tt.want {
				if !strings.Contains(msg, want) {
					t.Errorf("the message does not mention %q:\n%s", want, msg)
				}
			}
			for _, unwanted := range tt.unwanted {
				if strings.Contains(msg, unwanted) {
					t.Errorf("the message mentions %q:\n%s", unwanted, msg)
				}
			}
			if !strings.Contains(msg, filepath.Join("/repos", class)) {
				t.Errorf("the message does not name the working tree that was not requested:\n%s", msg)
			}
		})
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// residue reports a leftover staging directory, which is a half-tree the version
// in its name would otherwise vouch for.
func residue(t *testing.T, root string) bool {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".unpacking") || strings.HasPrefix(e.Name(), ".staging-") {
			return true
		}
	}
	return false
}

func entryNames(t *testing.T, data []byte) map[string]bool {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	return names
}
