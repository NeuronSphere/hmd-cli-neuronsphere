package versions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/librarian"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/versionspec"
)

// librarianHolding stands up a librarian whose one repo owns the given content
// paths, keyed by content item id.
func librarianHolding(t *testing.T, repoName string, paths map[string]string) *librarian.Client {
	t.Helper()

	var ids []string
	for id := range paths {
		ids = append(ids, id)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/hmd_lang_artifact_librarian.repo":
			_ = json.NewEncoder(w).Encode([]map[string]string{
				{"identifier": "repo-1", "repo_name": repoName},
			})
		case strings.HasSuffix(r.URL.Path, "/to/repo-1"):
			var edges []map[string]string
			for _, id := range ids {
				edges = append(edges, map[string]string{"ref_from": id})
			}
			_ = json.NewEncoder(w).Encode(edges)
		case r.URL.Path == "/apiop/get_by_nid":
			var body struct {
				Nids []string `json:"nids"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			var items []map[string]any
			for _, nid := range body.Nids {
				items = append(items, map[string]any{
					"content_item":      map[string]string{"identifier": nid, "content_item_path": paths[nid]},
					"content_item_type": "build",
				})
			}
			_ = json.NewEncoder(w).Encode(items)
		default:
			http.Error(w, "no route "+r.URL.Path, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	c, err := librarian.New(librarian.Config{Lookup: func(key string) string {
		switch key {
		case librarian.URLEnv:
			return srv.URL
		case librarian.APIKeyEnv:
			return "k"
		}
		return ""
	}})
	if err != nil {
		t.Fatalf("librarian.New: %v", err)
	}
	c.HTTP = srv.Client()
	return c
}

func TestEnumerateReadsVersionsOutOfContentPaths(t *testing.T) {
	t.Parallel()

	c := librarianHolding(t, "hmd-inf-trino", map[string]string{
		"ci-1": "repository:/hmd-inf-trino/0.1.9/hmd-inf-trino_0.1.9_build.zip",
		"ci-2": "repository:/hmd-inf-trino/0.1.100/hmd-inf-trino_0.1.100_build.zip",
		"ci-3": "repository:/hmd-inf-trino/0.2.5/hmd-inf-trino_0.2.5_build.zip",
		"ci-4": "repository:/hmd-inf-trino/0.2.5/hmd-inf-trino_0.2.5_schema.zip",
	})

	var steps []string
	p, err := Enumerate(context.Background(), c, "hmd-inf-trino", func(s string) {
		steps = append(steps, s)
	})
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	// Newest first, and the 0.2.5 published twice as two item types is one
	// version of each rather than two of one.
	if got := p.Versions("build"); !reflect.DeepEqual(got, []string{"0.2.5", "0.1.100", "0.1.9"}) {
		t.Errorf("Versions(build) = %v", got)
	}
	if got := p.Versions("schema"); !reflect.DeepEqual(got, []string{"0.2.5"}) {
		t.Errorf("Versions(schema) = %v", got)
	}
	if p.QueriedAt.IsZero() {
		t.Error("the enumeration records no query time, so nothing can report its age")
	}
	if len(steps) == 0 {
		t.Error("nothing was reported while enumerating; silence reads as a hang")
	}

	// The selection SPEC003 defines, over what a librarian actually holds.
	spec, err := versionspec.Parse("~= 0.1")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := spec.Highest(p.Versions("build")); !ok || got != "0.2.5" {
		t.Errorf("`~= 0.1` resolved to %q (%v), want 0.2.5", got, ok)
	}
}

// TestEnumerateSkipsWhatItCannotParse: a librarian holds content items that are
// not build artifacts, and one of them must not fail the enumeration.
func TestEnumerateSkipsWhatItCannotParse(t *testing.T) {
	t.Parallel()

	c := librarianHolding(t, "hmd-inf-trino", map[string]string{
		"ci-1": "repository:/hmd-inf-trino/0.1.9/hmd-inf-trino_0.1.9_build.zip",
		"ci-2": "some/thing/that/is/not/a/content/path",
		"ci-3": "repository:/hmd-inf-trino/0.1.9/notes.txt",
		// A path that parses but belongs to another repo class: skipped too,
		// because the answer is about this class.
		"ci-4": "repository:/hmd-ms-other/0.4.0/hmd-ms-other_0.4.0_build.zip",
	})

	p, err := Enumerate(context.Background(), c, "hmd-inf-trino", nil)
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	if got := p.Versions("build"); !reflect.DeepEqual(got, []string{"0.1.9"}) {
		t.Errorf("Versions(build) = %v, want just 0.1.9", got)
	}
}

// TestEnumerateReportsAnAbsentRepoClass is criterion 7: absent is not the same
// answer as present-with-nothing-satisfying, and the two have different fixes.
func TestEnumerateReportsAnAbsentRepoClass(t *testing.T) {
	t.Parallel()

	c := librarianHolding(t, "hmd-inf-trino", map[string]string{
		"ci-1": "repository:/hmd-inf-trino/0.1.9/hmd-inf-trino_0.1.9_build.zip",
	})

	_, err := Enumerate(context.Background(), c, "hmd-inf-nothing", nil)
	if !errors.Is(err, librarian.ErrNoSuchRepo) {
		t.Fatalf("Enumerate for an unknown class = %v, want ErrNoSuchRepo", err)
	}

	// Present, and nothing satisfies: an enumeration that succeeded and a
	// selection that found nothing.
	p, err := Enumerate(context.Background(), c, "hmd-inf-trino", nil)
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	spec, err := versionspec.Parse("~= 9.0")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := spec.Highest(p.Versions("build")); ok {
		t.Errorf("`~= 9.0` resolved to %q, want nothing satisfying", got)
	}
}

func TestCacheRoundTrip(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	queried := time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC)
	p := &Published{
		RepoClass: "hmd-inf-trino",
		QueriedAt: queried,
		Items:     []Item{{Version: "0.1.96", ItemType: "build"}},
	}
	if err := Save(home, p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Beside the unpacked artifacts, and nowhere an environment's state lives.
	want := filepath.Join(home, ".cache", "neuronsphere", "versions", "hmd-inf-trino.json")
	if got := Path(home, "hmd-inf-trino"); got != want {
		t.Errorf("Path = %q, want %q", got, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("nothing was written: %v", err)
	}

	back, err := Load(home, "hmd-inf-trino")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !back.QueriedAt.Equal(queried) {
		t.Errorf("QueriedAt = %v, want %v -- without it nothing can report how old the data is", back.QueriedAt, queried)
	}
	if got := back.Versions("build"); !reflect.DeepEqual(got, []string{"0.1.96"}) {
		t.Errorf("Versions = %v", got)
	}
	if age := back.Age(queried.Add(72 * time.Hour)); age != 72*time.Hour {
		t.Errorf("Age = %v, want 72h", age)
	}
}

func TestLoadWithoutACacheIsItsOwnAnswer(t *testing.T) {
	t.Parallel()

	if _, err := Load(t.TempDir(), "hmd-inf-trino"); !errors.Is(err, ErrNoCache) {
		t.Errorf("Load with no cache = %v, want ErrNoCache", err)
	}
	// No HMD_HOME: nowhere to read, and nowhere to write either -- but writing
	// is not an error, because refusing to answer a question already answered
	// because the result cannot be filed would be the wrong trade.
	if _, err := Load("", "hmd-inf-trino"); !errors.Is(err, ErrNoCache) {
		t.Errorf("Load with no home = %v, want ErrNoCache", err)
	}
	if err := Save("", &Published{RepoClass: "hmd-inf-trino"}); err != nil {
		t.Errorf("Save with no home = %v, want it to be a no-op", err)
	}
}

func TestItemTypes(t *testing.T) {
	t.Parallel()

	p := &Published{Items: []Item{
		{Version: "0.1.0", ItemType: "build"},
		{Version: "0.1.0", ItemType: "schema"},
		{Version: "0.2.0", ItemType: "build"},
	}}
	got := p.ItemTypes()
	if len(got) != 2 {
		t.Fatalf("ItemTypes = %v, want two", got)
	}
	if got := p.Versions(""); len(got) != 2 {
		t.Errorf("Versions(\"\") = %v, want every version once", got)
	}
}

// TestEnumerateChunksARepoClassWithHundredsOfVersions is acceptance criterion 4
// against a fake, so the live run has something to fail against rather than
// something to discover.
func TestEnumerateChunksARepoClassWithHundredsOfVersions(t *testing.T) {
	t.Parallel()

	paths := map[string]string{}
	for i := 0; i < 392; i++ {
		paths[fmt.Sprintf("ci-%03d", i)] = fmt.Sprintf(
			"repository:/hmd-ms-old/0.1.%d/hmd-ms-old_0.1.%d_build.zip", i, i)
	}
	c := librarianHolding(t, "hmd-ms-old", paths)

	p, err := Enumerate(context.Background(), c, "hmd-ms-old", nil)
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	if got := len(p.Versions("build")); got != 392 {
		t.Fatalf("enumerated %d versions, want 392", got)
	}
	if got := p.Versions("build")[0]; got != "0.1.391" {
		t.Errorf("newest = %q, want 0.1.391", got)
	}
}
