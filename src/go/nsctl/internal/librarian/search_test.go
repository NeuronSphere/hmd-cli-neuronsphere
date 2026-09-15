package librarian

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeLibrarian answers the three requests an enumeration makes.
type fakeLibrarian struct {
	repos []RepoEntity
	// edges maps a repo identifier to the content item ids hanging off it.
	edges map[string][]string
	// paths maps a content item id to its content_item_path.
	paths map[string]string

	mu sync.Mutex
	// searches, edgeReads and batches count requests per leg, which is how the
	// memoisation and the chunking are asserted.
	searches, edgeReads, batches int
	// batchSizes records how many ids each get_by_nid carried.
	batchSizes []int
	// filtered records any search body that was not the empty object, because a
	// filtered CRUD search answers 500 on a real librarian.
	filtered []string
}

func (f *fakeLibrarian) server(t *testing.T) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.URL.Path == "/api/hmd_lang_artifact_librarian.repo":
			f.searches++
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if len(body) > 0 {
				f.filtered = append(f.filtered, fmt.Sprint(body))
				http.Error(w, "filtered searches answer 500", http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(f.repos)

		case strings.HasPrefix(r.URL.Path, "/api/hmd_lang_artifact_librarian.content_item_has_repo/to/"):
			f.edgeReads++
			id := strings.TrimPrefix(r.URL.Path, "/api/hmd_lang_artifact_librarian.content_item_has_repo/to/")
			var edges []map[string]string
			for _, from := range f.edges[id] {
				edges = append(edges, map[string]string{"ref_from": from, "ref_to": id})
			}
			_ = json.NewEncoder(w).Encode(edges)

		case r.URL.Path == "/apiop/get_by_nid":
			f.batches++
			var body struct {
				Nids []string `json:"nids"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.batchSizes = append(f.batchSizes, len(body.Nids))
			var items []map[string]string
			for _, nid := range body.Nids {
				items = append(items, map[string]string{
					"nid":               nid,
					"content_item_path": f.paths[nid],
					// get_by_nid mints one of these per item whether or not
					// anybody wanted the bytes.
					"download_url": "https://signed.example/" + nid,
				})
			}
			_ = json.NewEncoder(w).Encode(items)

		default:
			http.Error(w, "no route "+r.URL.Path, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return &Client{BaseURL: srv.URL, HTTP: srv.Client(), apiKey: "k"}
}

// trino is a librarian holding one repo with three published build versions and
// one content item that is not a build artifact.
func trino() *fakeLibrarian {
	return &fakeLibrarian{
		repos: []RepoEntity{
			{Identifier: "repo-1", RepoName: "hmd-inf-trino"},
			{Identifier: "repo-2", RepoName: "hmd-ms-transform"},
		},
		edges: map[string][]string{"repo-1": {"ci-1", "ci-2", "ci-3", "ci-4"}},
		paths: map[string]string{
			"ci-1": "repository:/hmd-inf-trino/0.1.9/hmd-inf-trino_0.1.9_build.zip",
			"ci-2": "repository:/hmd-inf-trino/0.1.100/hmd-inf-trino_0.1.100_build.zip",
			"ci-3": "repository:/hmd-inf-trino/0.2.5/hmd-inf-trino_0.2.5_build.zip",
			"ci-4": "some/other/content/item.txt",
		},
	}
}

func TestReposIsUnfilteredAndMemoised(t *testing.T) {
	t.Parallel()

	f := trino()
	c := f.server(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		repos, err := c.Repos(ctx)
		if err != nil {
			t.Fatalf("Repos: %v", err)
		}
		if len(repos) != 2 {
			t.Fatalf("Repos returned %d rows, want 2", len(repos))
		}
	}
	// One request for three calls: the list is every repo at once, so filtering
	// client side costs nothing after the first.
	if f.searches != 1 {
		t.Errorf("made %d searches for three calls, want 1 (memoised)", f.searches)
	}
	if len(f.filtered) > 0 {
		t.Errorf("sent a filtered search body %v; a real librarian answers 500", f.filtered)
	}
}

// TestRepoIdentifierTellsAbsentFromPresent is acceptance criterion 7's first
// half: a repo class the librarian has never heard of is its own answer, because
// its fix is a name to correct rather than a range to widen.
func TestRepoIdentifierTellsAbsentFromPresent(t *testing.T) {
	t.Parallel()

	c := trino().server(t)
	ctx := context.Background()

	got, err := c.RepoIdentifier(ctx, "hmd-inf-trino")
	if err != nil || got != "repo-1" {
		t.Fatalf("RepoIdentifier = %q, %v", got, err)
	}
	_, err = c.RepoIdentifier(ctx, "hmd-inf-nothing")
	if !errors.Is(err, ErrNoSuchRepo) {
		t.Fatalf("RepoIdentifier for an unknown class = %v, want ErrNoSuchRepo", err)
	}
	if !strings.Contains(err.Error(), "hmd-inf-nothing") {
		t.Errorf("the error does not name the class: %v", err)
	}
}

func TestContentItemIDsReadsRefFrom(t *testing.T) {
	t.Parallel()

	c := trino().server(t)
	ids, err := c.ContentItemIDs(context.Background(), "repo-1")
	if err != nil {
		t.Fatalf("ContentItemIDs: %v", err)
	}
	if len(ids) != 4 {
		t.Fatalf("got %d ids, want 4: %v", len(ids), ids)
	}
	// A repo with no content items is an empty list, not an error -- which is
	// also what the never-populated repo_has_repo_version relationship returns,
	// and why nothing here is built on it.
	none, err := c.ContentItemIDs(context.Background(), "repo-2")
	if err != nil || len(none) != 0 {
		t.Errorf("ContentItemIDs for a repo with nothing = %v, %v", none, err)
	}
}

// TestContentItemsByNIDChunks is the measured Lambda limit made a property of
// the client rather than of whoever calls it.
func TestContentItemsByNIDChunks(t *testing.T) {
	t.Parallel()

	f := trino()
	f.edges["repo-big"] = nil
	for i := 0; i < 250; i++ {
		id := fmt.Sprintf("ci-%03d", i)
		f.edges["repo-big"] = append(f.edges["repo-big"], id)
		f.paths[id] = fmt.Sprintf("repository:/hmd-ms-big/0.1.%d/hmd-ms-big_0.1.%d_build.zip", i, i)
	}
	c := f.server(t)

	var lastDone, lastTotal int
	items, err := c.ContentItemsByNID(context.Background(), f.edges["repo-big"],
		func(done, total int) { lastDone, lastTotal = done, total })
	if err != nil {
		t.Fatalf("ContentItemsByNID: %v", err)
	}
	if len(items) != 250 {
		t.Fatalf("got %d items, want 250", len(items))
	}
	if f.batches != 3 {
		t.Errorf("sent %d requests for 250 ids, want 3 (chunked at 100): %v", f.batches, f.batchSizes)
	}
	for _, size := range f.batchSizes {
		if size > 100 {
			t.Errorf("a batch carried %d ids, want at most 100: %v", size, f.batchSizes)
		}
	}
	if lastDone != 250 || lastTotal != 250 {
		t.Errorf("progress finished at %d/%d, want 250/250", lastDone, lastTotal)
	}
}

func TestParseContentPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		want Spec
		ok   bool
	}{
		{"build", "repository:/hmd-inf-trino/0.1.96/hmd-inf-trino_0.1.96_build.zip",
			Spec{Name: "hmd-inf-trino", Version: "0.1.96", ItemType: "build"}, true},
		{"schema", "repository:/hmd-lang-foo/0.3.1/hmd-lang-foo_0.3.1_schema.zip",
			Spec{Name: "hmd-lang-foo", Version: "0.3.1", ItemType: "schema"}, true},
		// Not build artifacts. A librarian holds plenty of these and none of
		// them is this mechanism's business, so they are skipped rather than
		// fatal.
		{"not a repository path", "s3://bucket/thing.zip", Spec{}, false},
		{"too few segments", "repository:/hmd-inf-trino/0.1.96", Spec{}, false},
		{"file names another repo", "repository:/hmd-inf-trino/0.1.96/hmd-ms-other_0.1.96_build.zip", Spec{}, false},
		{"file names another version", "repository:/hmd-inf-trino/0.1.96/hmd-inf-trino_0.2.0_build.zip", Spec{}, false},
		{"not a zip", "repository:/hmd-inf-trino/0.1.96/hmd-inf-trino_0.1.96_build.tar", Spec{}, false},
		{"no item type", "repository:/hmd-inf-trino/0.1.96/hmd-inf-trino_0.1.96_.zip", Spec{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseContentPath(tt.path)
			if tt.ok != (err == nil) {
				t.Fatalf("ParseContentPath(%q) = %+v, %v", tt.path, got, err)
			}
			if !tt.ok {
				return
			}
			if got != tt.want {
				t.Errorf("ParseContentPath(%q) = %+v, want %+v", tt.path, got, tt.want)
			}
			// The parse and the render are one grammar, which is the whole
			// reason this reuses Spec rather than carrying a second regexp.
			if got.ContentPath() != tt.path {
				t.Errorf("does not round-trip: %q", got.ContentPath())
			}
		})
	}
}

// TestParseContentPathRoundTripsEverySpec drives the two halves against each
// other, so a change to either one has to change both.
func TestParseContentPathRoundTripsEverySpec(t *testing.T) {
	t.Parallel()

	for _, s := range []Spec{
		{Name: "hmd-inf-trino", Version: "0.1.96", ItemType: "build"},
		{Name: "hmd-ms-transform", Version: "0.5.201", ItemType: "build"},
		{Name: "hmd-lang-deployment", Version: "1.0", ItemType: "schema"},
	} {
		got, err := ParseContentPath(s.ContentPath())
		if err != nil {
			t.Errorf("ParseContentPath(%q) = %v", s.ContentPath(), err)
			continue
		}
		if got != s {
			t.Errorf("round trip of %+v gave %+v", s, got)
		}
	}
}
