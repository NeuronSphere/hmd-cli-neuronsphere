package cmd

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/artifact"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/librarian"
)

// artifactZip is a minimal deployable tree: the two files the cache validates
// and one source file that says which build it came from.
func artifactZip(t *testing.T, class, version, marker string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{
		"meta-data/manifest.json": `{"name": "` + class + `"}`,
		"meta-data/VERSION":       version,
		"src/cdktf/stack.py":      marker,
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
	return buf.Bytes()
}

// fakeLibrarian is a librarian that holds content, serving both the read path
// (/apiop/get plus a presigned download) and the three-leg write path.
type fakeLibrarian struct {
	t       *testing.T
	content map[string][]byte
	URL     string
}

func newFakeLibrarian(t *testing.T) *fakeLibrarian {
	t.Helper()
	f := &fakeLibrarian{t: t, content: map[string][]byte{}}

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	f.URL = srv.URL
	t.Cleanup(srv.Close)

	// The write legs. The content path travels through the upload URL's query
	// so the presigned leg stays credential-free, as the real one is.
	mux.HandleFunc("/apiop/put", func(w http.ResponseWriter, r *http.Request) {
		var manifests []struct {
			ContentItemPath string `json:"content_item_path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&manifests)
		if len(manifests) != 1 {
			f.t.Errorf("put body held %d manifests, want one", len(manifests))
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"nid": "n1",
			"upload_specs": []map[string]any{{
				"upload_url":  srv.URL + "/upload?path=" + manifests[0].ContentItemPath,
				"part_number": 1, "part_size": 1, "mime_type": "application/zip",
			}},
		}})
	})
	mux.HandleFunc("/upload", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("x-api-key") != "" {
			f.t.Error("the presigned upload carried a credential")
		}
		var body bytes.Buffer
		_, _ = body.ReadFrom(r.Body)
		f.content[r.URL.Query().Get("path")] = body.Bytes()
		w.Header().Set("ETag", `"e"`)
	})
	mux.HandleFunc("/apiop/close", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"status": "success"}]`))
	})

	// The read legs.
	mux.HandleFunc("/apiop/get", func(w http.ResponseWriter, r *http.Request) {
		var query struct {
			Value string `json:"value"`
		}
		_ = json.NewDecoder(r.Body).Decode(&query)
		if _, ok := f.content[query.Value]; !ok {
			_, _ = w.Write([]byte("[]"))
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"download_url": srv.URL + "/download?path=" + query.Value},
		})
	})
	mux.HandleFunc("/download", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(f.content[r.URL.Query().Get("path")])
	})

	// The enumeration legs, derived from the content this librarian holds --
	// which is exactly where a real one derives them from too, since the version
	// and the item type live in the content path. The path doubles as the
	// content item's id, so the three requests chain without a second map.
	mux.HandleFunc("/api/hmd_lang_artifact_librarian.repo", func(w http.ResponseWriter, r *http.Request) {
		seen := map[string]bool{}
		repos := []map[string]string{}
		for path := range f.content {
			spec, err := librarian.ParseContentPath(path)
			if err != nil || seen[spec.Name] {
				continue
			}
			seen[spec.Name] = true
			repos = append(repos, map[string]string{"identifier": spec.Name, "repo_name": spec.Name})
		}
		_ = json.NewEncoder(w).Encode(repos)
	})
	mux.HandleFunc("/api/hmd_lang_artifact_librarian.content_item_has_repo/to/",
		func(w http.ResponseWriter, r *http.Request) {
			repo := strings.TrimPrefix(r.URL.Path,
				"/api/hmd_lang_artifact_librarian.content_item_has_repo/to/")
			edges := []map[string]string{}
			for path := range f.content {
				if spec, err := librarian.ParseContentPath(path); err == nil && spec.Name == repo {
					edges = append(edges, map[string]string{"ref_from": path})
				}
			}
			_ = json.NewEncoder(w).Encode(edges)
		})
	mux.HandleFunc("/apiop/get_by_nid", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Nids []string `json:"nids"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		items := []map[string]string{}
		for _, nid := range body.Nids {
			items = append(items, map[string]string{"nid": nid, "content_item_path": nid})
		}
		_ = json.NewEncoder(w).Encode(items)
	})
	return f
}

const registryClass = "hmd-inf-local-registry"

func registryPath(version string) string {
	return librarian.Spec{Name: registryClass, Version: version, ItemType: "build"}.ContentPath()
}

// TestArtifactPullFillsTheControlPlaneAndTheCache is SPEC004's acceptance
// criterion: the artifact is retrievable from the local librarian afterwards,
// and an apply -- which never reaches the network -- can resolve it.
func TestArtifactPullFillsTheControlPlaneAndTheCache(t *testing.T) {
	t.Parallel()

	cloud, local := newFakeLibrarian(t), newFakeLibrarian(t)
	cloud.content[registryPath("0.1.4")] = artifactZip(t, registryClass, "0.1.4", "published")

	home := t.TempDir()
	out, _, err := run(t, fakeEnv(map[string]string{librarian.APIKeyEnv: "k"}),
		"artifact", "pull", registryClass+"@0.1.4",
		"--home", home, "--url", cloud.URL, "--local-url", local.URL)
	if err != nil {
		t.Fatal(err)
	}

	if _, held := local.content[registryPath("0.1.4")]; !held {
		t.Errorf("the control plane's librarian does not hold the artifact; it holds %v", keysOf(local.content))
	}
	if !artifact.Cached(home, registryClass, "0.1.4") {
		t.Error("the artifact was not unpacked, so an offline apply would still fail")
	}
	if got := readCached(t, home, "0.1.4"); got != "published" {
		t.Errorf("the unpacked tree holds %q", got)
	}
	if !strings.Contains(out, "0.1.4") {
		t.Errorf("pull said nothing useful:\n%s", out)
	}
}

// A version that was never published fails as that, not as a bare HTTP error.
func TestArtifactPullUnpublishedVersion(t *testing.T) {
	t.Parallel()

	cloud, local := newFakeLibrarian(t), newFakeLibrarian(t)
	_, _, err := run(t, fakeEnv(map[string]string{librarian.APIKeyEnv: "k"}),
		"artifact", "pull", registryClass+"@9.9.9",
		"--home", t.TempDir(), "--url", cloud.URL, "--local-url", local.URL)
	if err == nil {
		t.Fatal("pull succeeded for a version the librarian does not have")
	}
	if !strings.Contains(err.Error(), "not published") {
		t.Errorf("error = %v, want it to say the version was never published", err)
	}
}

// TestArtifactRegisterRoundTrip is SPEC005's acceptance criterion, end to end:
// register a build, re-register it after a change, and the second register must
// be what resolves. A register that left the previous unpacked tree in place
// would deploy the old build under the new build's version.
func TestArtifactRegisterRoundTrip(t *testing.T) {
	t.Parallel()

	local := newFakeLibrarian(t)
	home := t.TempDir()
	zipPath := filepath.Join(t.TempDir(), "build.zip")

	for _, marker := range []string{"first build", "second build"} {
		if err := os.WriteFile(zipPath, artifactZip(t, registryClass, "0.1.4", marker), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := run(t, fakeEnv(nil), "artifact", "register", zipPath,
			"--home", home, "--local-url", local.URL); err != nil {
			t.Fatalf("register %s: %v", marker, err)
		}
		if got := readCached(t, home, "0.1.4"); got != marker {
			t.Errorf("after registering %q the cache holds %q", marker, got)
		}
	}
	if _, held := local.content[registryPath("0.1.4")]; !held {
		t.Errorf("the librarian does not hold the registered artifact; it holds %v", keysOf(local.content))
	}
}

// register takes the repo class and version from inside the artifact rather than
// from its filename: the two disagree for anything renamed, and the tree is what
// a deploy actually reads its version out of.
func TestArtifactRegisterReadsTheIdentityFromInsideTheZip(t *testing.T) {
	t.Parallel()

	local := newFakeLibrarian(t)
	home := t.TempDir()
	zipPath := filepath.Join(t.TempDir(), "misleading-name.zip")
	if err := os.WriteFile(zipPath, artifactZip(t, registryClass, "0.2.7", "x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, fakeEnv(nil), "artifact", "register", zipPath,
		"--home", home, "--local-url", local.URL); err != nil {
		t.Fatal(err)
	}
	if !artifact.Cached(home, registryClass, "0.2.7") {
		t.Errorf("the artifact cached nothing at 0.2.7; the librarian holds %v", keysOf(local.content))
	}
}

// A directory is zipped on the fly, which is what makes a checkout registrable
// without the Python CLI installed -- the environment nsctl exists to serve.
func TestArtifactRegisterZipsADirectory(t *testing.T) {
	t.Parallel()

	local := newFakeLibrarian(t)
	home := t.TempDir()
	tree := t.TempDir()
	writeFile(t, filepath.Join(tree, "meta-data", "manifest.json"), `{"name": "`+registryClass+`"}`)
	writeFile(t, filepath.Join(tree, "meta-data", "VERSION"), "0.3.1")
	writeFile(t, filepath.Join(tree, "src", "cdktf", "stack.py"), "from the tree")
	// A deploy's own output must not travel: an artifact carrying one has every
	// environment submitting whoever's deploy produced it.
	writeFile(t, filepath.Join(tree, "meta-data", "resources_output", "c.json"), `{"leaked": true}`)

	if _, _, err := run(t, fakeEnv(nil), "artifact", "register", tree,
		"--home", home, "--local-url", local.URL); err != nil {
		t.Fatal(err)
	}
	dir := artifact.Dir(home, registryClass, "0.3.1")
	if got := readFile(t, filepath.Join(dir, "src", "cdktf", "stack.py")); got != "from the tree" {
		t.Errorf("the unpacked tree holds %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "meta-data", "resources_output")); err == nil {
		t.Error("resources_output travelled into the artifact")
	}
}

// With no input at all, register names all three and the invocation that
// produces the last -- rather than failing with whichever it happened to try.
func TestArtifactRegisterWithNothingNamesAllThreeInputs(t *testing.T) {
	t.Parallel()

	_, _, err := run(t, fakeEnv(nil), "artifact", "register", "--home", t.TempDir())
	if err == nil {
		t.Fatal("register succeeded with nothing to register")
	}
	for _, want := range []string{"a zip:", "a directory:", "a build:", "hmd build", BuildOutputDirEnv} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message does not mention %q:\n%v", want, err)
		}
	}
}

// register with no argument reads the zip `hmd build` writes, resolving the repo
// and version from meta-data/ in the working directory -- the second half of the
// fork in hmd-cli-build's controller, made available after the fact.
func TestArtifactRegisterFindsTheBuildOutput(t *testing.T) {
	t.Parallel()

	local := newFakeLibrarian(t)
	home := t.TempDir()
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "meta-data", "manifest.json"), `{"name": "`+registryClass+`"}`)
	writeFile(t, filepath.Join(repo, "meta-data", "VERSION"), "0.4.2")

	outputDir := t.TempDir()
	conventional := filepath.Join(outputDir, registryClass+"-0.4.2-build", registryClass+"_0.4.2_build.zip")
	if err := os.MkdirAll(filepath.Dir(conventional), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(conventional, artifactZip(t, registryClass, "0.4.2", "built"), 0o644); err != nil {
		t.Fatal(err)
	}

	// --repo rather than a chdir: every test in this package runs in parallel,
	// and a process-global working directory is not something a parallel test
	// may move.
	if _, _, err := run(t, fakeEnv(map[string]string{BuildOutputDirEnv: outputDir}),
		"artifact", "register", "--repo", repo, "--home", home, "--local-url", local.URL); err != nil {
		t.Fatal(err)
	}
	if got := readCached(t, home, "0.4.2"); got != "built" {
		t.Errorf("the cache holds %q, want the build output", got)
	}
}

// unpack is the repair path: it refills a cache that was deleted, from the
// control plane alone, with no cloud round trip and nothing rebuilt.
func TestArtifactUnpackRefillsADeletedCache(t *testing.T) {
	t.Parallel()

	local := newFakeLibrarian(t)
	local.content[registryPath("0.1.4")] = artifactZip(t, registryClass, "0.1.4", "published")
	home := t.TempDir()

	if _, _, err := run(t, fakeEnv(nil), "artifact", "unpack", registryClass+"@0.1.4",
		"--home", home, "--local-url", local.URL); err != nil {
		t.Fatal(err)
	}
	if got := readCached(t, home, "0.1.4"); got != "published" {
		t.Errorf("the cache holds %q", got)
	}
}

// A version the control plane does not hold fails with the shape SPEC007 asks
// for, naming every place that was looked -- not a bare 404.
func TestArtifactUnpackMissingVersionNamesEveryPlaceLooked(t *testing.T) {
	t.Parallel()

	local := newFakeLibrarian(t)
	home := t.TempDir()
	_, _, err := run(t, fakeEnv(nil), "artifact", "unpack", registryClass+"@0.1.4",
		"--home", home, "--local-url", local.URL, "--url", "https://unused.example")
	if err == nil {
		t.Fatal("unpack succeeded for a version the control plane does not hold")
	}
	for _, want := range []string{registryClass + "@0.1.4", "artifact cache", "local librarian"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message does not mention %q:\n%v", want, err)
		}
	}
}

// SPEC006: the spec string BACON writes is already the librarian's own
// content-path grammar, so a pre-build artifact is copied across with nothing
// translated and nothing new named.
func TestArtifactCacheCopiesPreBuildArtifacts(t *testing.T) {
	t.Parallel()

	cloud, local := newFakeLibrarian(t), newFakeLibrarian(t)
	schema := librarian.Spec{Name: "hmd-lang-foo", Version: "0.3.1", ItemType: "schema"}
	build := librarian.Spec{Name: "hmd-inf-bar", Version: "0.2.0", ItemType: "build"}
	cloud.content[schema.ContentPath()] = []byte("schema bytes")
	cloud.content[build.ContentPath()] = []byte("build bytes")

	manifestPath := filepath.Join(t.TempDir(), "manifest.json")
	writeFile(t, manifestPath, `{"build": {"pre_build_artifacts": [
		["hmd-lang-foo@0.3.1:schema", "src/schemas/"],
		["hmd-inf-bar@0.2.0:build", "external/bar"]]}}`)

	if _, _, err := run(t, fakeEnv(map[string]string{librarian.APIKeyEnv: "k"}),
		"artifact", "cache", "--manifest", manifestPath,
		"--home", t.TempDir(), "--url", cloud.URL, "--local-url", local.URL); err != nil {
		t.Fatal(err)
	}
	for _, spec := range []librarian.Spec{schema, build} {
		if got := string(local.content[spec.ContentPath()]); got == "" {
			t.Errorf("%s was not copied; the control plane holds %v", spec, keysOf(local.content))
		}
	}
	// Build inputs, not RepoClasses: they are stored and deliberately not
	// unpacked, since an apply resolves RepoClasses and not schemas.
	if artifact.Cached(t.TempDir(), "hmd-lang-foo", "0.3.1") {
		t.Error("a pre-build artifact was unpacked into the RepoClass cache")
	}
}

// The item type defaults to build, which is what `hmd build` publishes and what
// every pin in this repository's own pre_build_artifacts uses.
func TestParseArtifactRefDefaultsTheItemType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		ref  string
		want string
	}{
		{"hmd-inf-x@0.1.4", "hmd-inf-x@0.1.4:build"},
		{"hmd-inf-x@0.1.4:schema", "hmd-inf-x@0.1.4:schema"},
	}
	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			t.Parallel()

			spec, err := parseArtifactRef(tt.ref)
			if err != nil {
				t.Fatal(err)
			}
			if spec.String() != tt.want {
				t.Errorf("parseArtifactRef(%q) = %q, want %q", tt.ref, spec, tt.want)
			}
		})
	}
	if _, err := parseArtifactRef("hmd-inf-x"); err == nil {
		t.Error("parseArtifactRef accepted a ref with no version")
	}
}

func readCached(t *testing.T, home, version string) string {
	t.Helper()
	return readFile(t, filepath.Join(artifact.Dir(home, registryClass, version), "src", "cdktf", "stack.py"))
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return string(data)
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func keysOf(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
