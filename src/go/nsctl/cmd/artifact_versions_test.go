package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/librarian"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/versions"
)

// trinoLibrarian is the document's worked example in miniature: a repo class
// that has moved past 0.1, which is what makes `~= 0.1` interesting.
func trinoLibrarian(t *testing.T) *fakeLibrarian {
	t.Helper()
	f := newFakeLibrarian(t)
	for _, v := range []string{"0.1.9", "0.1.96", "0.1.100", "0.2.5"} {
		f.content[librarian.Spec{Name: "hmd-inf-trino", Version: v, ItemType: "build"}.ContentPath()] =
			[]byte("zip")
	}
	f.content[librarian.Spec{Name: "hmd-lang-foo", Version: "0.3.1", ItemType: "schema"}.ContentPath()] =
		[]byte("zip")
	return f
}

func versionsRun(t *testing.T, f *fakeLibrarian, home string, args ...string) (string, string, error) {
	t.Helper()
	return run(t, fakeEnv(map[string]string{librarian.APIKeyEnv: "k"}),
		append([]string{"artifact", "versions", "--home", home, "--url", f.URL}, args...)...)
}

func TestArtifactVersionsListsWhatIsPublished(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	out, errOut, err := versionsRun(t, trinoLibrarian(t), home, "hmd-inf-trino")
	if err != nil {
		t.Fatalf("versions: %v", err)
	}
	// Newest first, and only the build artifacts.
	if !strings.Contains(out, "0.2.5  0.1.100  0.1.96  0.1.9") {
		t.Errorf("versions are not listed newest first:\n%s", out)
	}
	if !strings.Contains(out, "4 published build versions") {
		t.Errorf("the count is not reported:\n%s", out)
	}
	if !strings.Contains(out, "queried just now") {
		t.Errorf("the age of the answer is not reported:\n%s", out)
	}
	// Progress is reassurance, not the answer, so it goes to stderr: half a
	// minute of silence reads as a hang, and a script capturing the answer
	// should not have to filter it out.
	if !strings.Contains(errOut, "hmd-inf-trino") {
		t.Errorf("nothing was reported while querying:\n%s", errOut)
	}

	// And the answer was cached, so --offline has something to read.
	if _, statErr := os.Stat(versions.Path(home, "hmd-inf-trino")); statErr != nil {
		t.Errorf("the enumeration was not cached: %v", statErr)
	}
}

// TestArtifactVersionsResolvesASpecifier is acceptance criterion 3's shape: the
// highest satisfying version, which for `~= 0.1` is *not* the highest 0.1.x.
func TestArtifactVersionsResolvesASpecifier(t *testing.T) {
	t.Parallel()

	out, _, err := versionsRun(t, trinoLibrarian(t), t.TempDir(), "hmd-inf-trino", "--spec", "~= 0.1")
	if err != nil {
		t.Fatalf("versions: %v", err)
	}
	if !strings.Contains(out, "resolves to 0.2.5") {
		t.Errorf("`~= 0.1` did not resolve to 0.2.5, which is the case that surprises readers:\n%s", out)
	}

	out, _, err = versionsRun(t, trinoLibrarian(t), t.TempDir(), "hmd-inf-trino", "--spec", "== 0.1.*")
	if err != nil {
		t.Fatalf("versions: %v", err)
	}
	if !strings.Contains(out, "resolves to 0.1.100") {
		t.Errorf("`== 0.1.*` did not resolve to 0.1.100:\n%s", out)
	}
}

// TestArtifactVersionsRefusesAnOrderedSpecifier is acceptance criterion 2, and
// it asserts on the remedies rather than on the refusal: an author who wrote
// ">=0.3" needs to be told which of the two things they meant.
func TestArtifactVersionsRefusesAnOrderedSpecifier(t *testing.T) {
	t.Parallel()

	_, _, err := versionsRun(t, trinoLibrarian(t), t.TempDir(), "hmd-inf-trino", "--spec", ">=0.3")
	if err == nil {
		t.Fatal("an ordered specifier was accepted")
	}
	for _, want := range []string{">=0.3", "~= 0.3", "== 0.3.*"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q:\n%v", want, err)
		}
	}
	if got := nserr.CodeOf(err); got != nserr.Usage {
		t.Errorf("exit code = %d, want %d (usage): the fix is to write a different specifier", got, nserr.Usage)
	}
}

// TestArtifactVersionsTellsAbsentFromUnsatisfiable is acceptance criterion 7.
// The two failures look alike and are not: one is a name to correct, the other
// a range to widen.
func TestArtifactVersionsTellsAbsentFromUnsatisfiable(t *testing.T) {
	t.Parallel()

	f := trinoLibrarian(t)

	_, _, absent := versionsRun(t, f, t.TempDir(), "hmd-inf-nothing")
	if absent == nil {
		t.Fatal("a repo class the librarian has never heard of was accepted")
	}
	if !strings.Contains(absent.Error(), "no repo of that name") {
		t.Errorf("an absent repo class is not reported as absent:\n%v", absent)
	}

	_, _, none := versionsRun(t, f, t.TempDir(), "hmd-inf-trino", "--spec", "~= 9.0")
	if none == nil {
		t.Fatal("a specifier nothing satisfies was accepted")
	}
	if strings.Contains(none.Error(), "no repo of that name") {
		t.Errorf("a present repo class was reported as absent:\n%v", none)
	}
	for _, want := range []string{"4 published build versions", "none satisfying", "0.2.5"} {
		if !strings.Contains(none.Error(), want) {
			t.Errorf("the message does not say %q, so there is nothing to act on:\n%v", want, none)
		}
	}
}

// A repo class that has published only schemas is a third answer again, and
// naming the types it does have is what turns it into one the reader can act on.
func TestArtifactVersionsReportsTheWrongItemType(t *testing.T) {
	t.Parallel()

	_, _, err := versionsRun(t, trinoLibrarian(t), t.TempDir(), "hmd-lang-foo")
	if err == nil {
		t.Fatal("a repo class with no build artifacts was accepted")
	}
	for _, want := range []string{"no build artifacts", "schema", "--type"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message does not say %q:\n%v", want, err)
		}
	}

	out, _, err := versionsRun(t, trinoLibrarian(t), t.TempDir(), "hmd-lang-foo", "--type", "schema")
	if err != nil {
		t.Fatalf("versions --type schema: %v", err)
	}
	if !strings.Contains(out, "0.3.1") {
		t.Errorf("--type schema did not find the schema:\n%s", out)
	}
}

// TestArtifactVersionsOfflineReadsTheCacheAndItsAge is what makes the recorded
// query time load bearing: "this was the newest version when somebody last
// asked" is a different claim from "this is the newest version".
func TestArtifactVersionsOfflineReadsTheCacheAndItsAge(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	stale := &versions.Published{
		RepoClass: "hmd-inf-trino",
		QueriedAt: time.Now().UTC().Add(-72 * time.Hour),
		Items:     []versions.Item{{Version: "0.1.96", ItemType: "build"}},
	}
	if err := versions.Save(home, stale); err != nil {
		t.Fatal(err)
	}

	// The librarian holds more than the cache does, and --offline must not
	// notice: an implicit refresh would make --offline mean "usually offline".
	out, _, err := versionsRun(t, trinoLibrarian(t), home, "hmd-inf-trino", "--offline")
	if err != nil {
		t.Fatalf("versions --offline: %v", err)
	}
	if !strings.Contains(out, "0.1.96") || strings.Contains(out, "0.2.5") {
		t.Errorf("--offline did not answer from the cache alone:\n%s", out)
	}
	if !strings.Contains(out, "3 days ago") {
		t.Errorf("--offline did not say how old the answer is:\n%s", out)
	}
}

func TestArtifactVersionsOfflineWithNoCacheNamesTheOnlineCommand(t *testing.T) {
	t.Parallel()

	_, _, err := versionsRun(t, trinoLibrarian(t), t.TempDir(), "hmd-inf-trino", "--offline")
	if err == nil {
		t.Fatal("--offline succeeded with nothing cached")
	}
	if !strings.Contains(err.Error(), "nsctl artifact versions hmd-inf-trino") {
		t.Errorf("the refusal does not name the command that would answer:\n%v", err)
	}
}

// A repo class with more versions than fit on a screen is summarised, and says
// how many it did not print. A silent truncation reads as "that is all of them".
func TestArtifactVersionsSummarisesALongHistory(t *testing.T) {
	t.Parallel()

	f := newFakeLibrarian(t)
	for i := 0; i < 120; i++ {
		v := "0.1." + itoa(i)
		f.content[librarian.Spec{Name: "hmd-ms-old", Version: v, ItemType: "build"}.ContentPath()] =
			[]byte("zip")
	}

	out, _, err := versionsRun(t, f, t.TempDir(), "hmd-ms-old")
	if err != nil {
		t.Fatalf("versions: %v", err)
	}
	if !strings.Contains(out, "and 100 more") {
		t.Errorf("the versions it did not print are not accounted for:\n%s", out)
	}

	all, _, err := versionsRun(t, f, t.TempDir(), "hmd-ms-old", "--all")
	if err != nil {
		t.Fatalf("versions --all: %v", err)
	}
	if strings.Contains(all, "more") || !strings.Contains(all, "0.1.0") {
		t.Errorf("--all did not list every version:\n%s", all)
	}
}

// TestArtifactPullResolvesTheNewest: the version is optional, and what it
// resolved to is printed rather than assumed.
func TestArtifactPullResolvesTheNewest(t *testing.T) {
	t.Parallel()

	cloud, local := trinoLibrarian(t), newFakeLibrarian(t)
	// Give the newest version a real tree, since pull unpacks what it fetches.
	cloud.content[librarian.Spec{Name: "hmd-inf-trino", Version: "0.2.5", ItemType: "build"}.ContentPath()] =
		artifactZip(t, "hmd-inf-trino", "0.2.5", "newest")

	home := t.TempDir()
	out, _, err := run(t, fakeEnv(map[string]string{librarian.APIKeyEnv: "k"}),
		"artifact", "pull", "hmd-inf-trino",
		"--home", home, "--url", cloud.URL, "--local-url", local.URL)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if !strings.Contains(out, "Resolved hmd-inf-trino to 0.2.5") {
		t.Errorf("pull did not say which version it chose:\n%s", out)
	}
	if _, held := local.content[librarian.Spec{
		Name: "hmd-inf-trino", Version: "0.2.5", ItemType: "build"}.ContentPath()]; !held {
		t.Error("the control plane's librarian does not hold the resolved version")
	}
}

// A specifier in place of a version resolves the same way, which is what lets a
// range from a manifest be pulled without first looking it up by hand.
func TestArtifactPullResolvesASpecifier(t *testing.T) {
	t.Parallel()

	cloud, local := trinoLibrarian(t), newFakeLibrarian(t)
	cloud.content[librarian.Spec{Name: "hmd-inf-trino", Version: "0.1.100", ItemType: "build"}.ContentPath()] =
		artifactZip(t, "hmd-inf-trino", "0.1.100", "series")

	out, _, err := run(t, fakeEnv(map[string]string{librarian.APIKeyEnv: "k"}),
		"artifact", "pull", "hmd-inf-trino@== 0.1.*",
		"--home", t.TempDir(), "--url", cloud.URL, "--local-url", local.URL)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if !strings.Contains(out, "0.1.100") {
		t.Errorf("pull did not resolve the series to 0.1.100:\n%s", out)
	}
}

// An exact version still asks nothing: a reference that names a version is
// already settled, and a resolution request would be a network round trip for an
// answer that was typed in.
func TestArtifactPullWithAVersionQueriesNothing(t *testing.T) {
	t.Parallel()

	req, err := parseArtifactRequest("hmd-inf-trino@0.1.96")
	if err != nil {
		t.Fatalf("parseArtifactRequest: %v", err)
	}
	if req.Version != "0.1.96" || !req.Spec.Empty() {
		t.Errorf("an exact version became %+v, want no resolution", req)
	}
}

func TestParseArtifactRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		ref      string
		name     string
		version  string
		spec     string
		itemType string
	}{
		{"hmd-inf-x", "hmd-inf-x", "", "", "build"},
		{"hmd-inf-x@0.1.4", "hmd-inf-x", "0.1.4", "", "build"},
		{"hmd-inf-x@0.1.4:schema", "hmd-inf-x", "0.1.4", "", "schema"},
		{"hmd-inf-x:schema", "hmd-inf-x", "", "", "schema"},
		{"hmd-inf-x@~= 0.1", "hmd-inf-x", "", "~= 0.1", "build"},
		{"hmd-inf-x@== 0.1.*:schema", "hmd-inf-x", "", "== 0.1.*", "schema"},
	}
	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			t.Parallel()

			got, err := parseArtifactRequest(tt.ref)
			if err != nil {
				t.Fatalf("parseArtifactRequest(%q) = %v", tt.ref, err)
			}
			if got.Name != tt.name || got.Version != tt.version || got.ItemType != tt.itemType {
				t.Errorf("parseArtifactRequest(%q) = %+v", tt.ref, got)
			}
			if tt.spec == "" && !got.Spec.Empty() {
				t.Errorf("parseArtifactRequest(%q) invented a specifier: %s", tt.ref, got.Spec)
			}
			if tt.spec != "" && got.Spec.String() != tt.spec {
				t.Errorf("parseArtifactRequest(%q) specifier = %q, want %q", tt.ref, got.Spec, tt.spec)
			}
		})
	}

	// An ordered specifier is refused before anything is asked of a librarian.
	if _, err := parseArtifactRequest("hmd-inf-x@>=0.3"); err == nil {
		t.Error("parseArtifactRequest accepted an ordered specifier")
	} else if !strings.Contains(err.Error(), "~= 0.3") {
		t.Errorf("the refusal does not name the remedy: %v", err)
	}
}

// The cache is a plain, readable file, and its shape is part of what NERD005
// SPEC003 means by cache: deleting it costs a re-query and nothing else.
func TestVersionsCacheIsReadable(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	if _, _, err := versionsRun(t, trinoLibrarian(t), home, "hmd-inf-trino"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".cache", "neuronsphere", "versions", "hmd-inf-trino.json"))
	if err != nil {
		t.Fatal(err)
	}
	var p versions.Published
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatalf("the cache is not readable JSON: %v", err)
	}
	if p.QueriedAt.IsZero() || len(p.Items) != 4 {
		t.Errorf("cached %+v", p)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
