package hmdenv

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seed writes an hmd.env with the given content and returns the home holding it.
func seed(t *testing.T, content string) string {
	t.Helper()
	home := t.TempDir()
	path := Path(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

// read returns the raw file, so a test can assert on what is *around* the
// managed block and not only on what Parse makes of it.
func read(t *testing.T, home string) string {
	t.Helper()
	data, err := os.ReadFile(Path(home))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func scalar(name, value string) Var {
	return Var{Name: name, Merge: MergeScalar, Value: value}
}

func TestUpsertCreatesAMissingFileAt0600(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	if _, err := Upsert(home, []Var{scalar("GOPROXY", "http://local/go")}); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	info, err := os.Stat(Path(home))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %o, want 600 -- the file holds live secrets", got)
	}
	if !strings.Contains(read(t, home), "GOPROXY='http://local/go'") {
		t.Errorf("file = %q, want the contributed value", read(t, home))
	}
}

func TestUpsertPreservesAnExistingFilesMode(t *testing.T) {
	t.Parallel()

	home := seed(t, "A=b\n")
	if err := os.Chmod(Path(home), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := Upsert(home, []Var{scalar("GOPROXY", "x")}); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	info, err := os.Stat(Path(home))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Errorf("mode = %o, want the 640 the file already had", got)
	}
}

func TestUpsertLeavesEverythingOutsideTheBlockByteIdentical(t *testing.T) {
	t.Parallel()

	// Comments, blanks, an export prefix and double quotes: all of them are
	// lost by a Parse-and-re-serialise writer, which is why this one splices.
	original := "# a hand-written note\n\nexport HMD_REGION=us-west-2\nA=\"quoted value\"\n"
	home := seed(t, original)

	if _, err := Upsert(home, []Var{scalar("GOPROXY", "x")}); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	got := read(t, home)
	if !strings.HasPrefix(got, original) {
		t.Errorf("the original content was not preserved verbatim:\n%q", got)
	}
}

func TestUpsertReplacesExactlyOneBlock(t *testing.T) {
	t.Parallel()

	home := seed(t, "A=b\n")
	if _, err := Upsert(home, []Var{scalar("GOPROXY", "first")}); err != nil {
		t.Fatal(err)
	}
	if _, err := Upsert(home, []Var{scalar("GOPROXY", "second")}); err != nil {
		t.Fatal(err)
	}

	got := read(t, home)
	if n := strings.Count(got, blockBegin); n != 1 {
		t.Errorf("found %d begin markers, want 1:\n%s", n, got)
	}
	if strings.Contains(got, "first") {
		t.Errorf("the previous block survived:\n%s", got)
	}
	if !strings.Contains(got, "GOPROXY='second'") {
		t.Errorf("the new value is missing:\n%s", got)
	}
}

func TestUpsertIsByteIdenticalOnAnUnchangedRerun(t *testing.T) {
	t.Parallel()

	home := seed(t, "A=b\n")
	vars := []Var{scalar("GOPROXY", "x"), scalar("OTHER", "y")}
	if _, err := Upsert(home, vars); err != nil {
		t.Fatal(err)
	}
	first := read(t, home)
	if _, err := Upsert(home, vars); err != nil {
		t.Fatal(err)
	}
	if second := read(t, home); second != first {
		t.Errorf("a second apply rewrote the file:\n%q\nwant:\n%q", second, first)
	}
}

func TestUpsertRemovesTheBlockWhenNothingIsDeclared(t *testing.T) {
	t.Parallel()

	original := "# note\nA=b\n"
	home := seed(t, original)
	if _, err := Upsert(home, []Var{scalar("GOPROXY", "x")}); err != nil {
		t.Fatal(err)
	}
	// Declaring nothing is a convergence too: the last extension ever declared
	// must not keep contributing forever.
	if _, err := Upsert(home, nil); err != nil {
		t.Fatal(err)
	}

	got := read(t, home)
	if strings.Contains(got, "GOPROXY") || strings.Contains(got, blockBegin) {
		t.Errorf("the block outlived its declaration:\n%s", got)
	}
	if got != original {
		t.Errorf("file = %q, want the original %q", got, original)
	}
}

func TestUpsertScalarYieldsToAUserValue(t *testing.T) {
	t.Parallel()

	home := seed(t, "GOPROXY=mine\n")
	res, err := Upsert(home, []Var{scalar("GOPROXY", "theirs")})
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	got := read(t, home)
	if strings.Contains(got, "theirs") {
		t.Errorf("the handback overwrote a hand-set value:\n%s", got)
	}
	if len(res.Yielded) != 1 || res.Yielded[0] != "GOPROXY" {
		t.Errorf("Yielded = %v, want [GOPROXY]", res.Yielded)
	}
	if len(res.Written) != 0 {
		t.Errorf("Written = %v, want none", res.Written)
	}
}

func TestUpsertJSONMapMergesBesideTheUsersEntries(t *testing.T) {
	t.Parallel()

	// The case NERD006 SPEC008 found: replacing the value would delete the
	// JFrog index the user configured.
	home := seed(t, `PYTHON_REGISTRIES={"jfrog":{"url":"https://jfrog/simple"}}`+"\n")
	res, err := Upsert(home, []Var{{
		Name:  "PYTHON_REGISTRIES",
		Merge: MergeJSONMap,
		Key:   "neuronsphere",
		Value: map[string]any{"url": "http://local/+simple/", "publish": true},
	}})
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if len(res.Written) != 1 {
		t.Fatalf("Written = %v, want PYTHON_REGISTRIES", res.Written)
	}

	vars, err := Load(home)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(vars["PYTHON_REGISTRIES"]), &got); err != nil {
		t.Fatalf("the merged value is not JSON: %v (%q)", err, vars["PYTHON_REGISTRIES"])
	}
	if _, ok := got["jfrog"]; !ok {
		t.Errorf("the user's jfrog index was lost: %v", got)
	}
	if _, ok := got["neuronsphere"]; !ok {
		t.Errorf("the contribution was not merged in: %v", got)
	}
}

func TestUpsertJSONMapLetsAUserDefinedKeyWin(t *testing.T) {
	t.Parallel()

	home := seed(t, `PYTHON_REGISTRIES={"neuronsphere":{"url":"https://mine"}}`+"\n")
	res, err := Upsert(home, []Var{{
		Name:  "PYTHON_REGISTRIES",
		Merge: MergeJSONMap,
		Key:   "neuronsphere",
		Value: map[string]any{"url": "http://theirs"},
	}})
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	vars, err := Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(vars["PYTHON_REGISTRIES"], "theirs") {
		t.Errorf("the contribution beat a user-defined key of the same name: %s", vars["PYTHON_REGISTRIES"])
	}
	if len(res.Yielded) != 1 {
		t.Errorf("Yielded = %v, want PYTHON_REGISTRIES", res.Yielded)
	}
}

func TestUpsertJSONMapHandlesAnAbsentUserValue(t *testing.T) {
	t.Parallel()

	home := seed(t, "A=b\n")
	if _, err := Upsert(home, []Var{{
		Name:  "PYTHON_REGISTRIES",
		Merge: MergeJSONMap,
		Key:   "neuronsphere",
		Value: map[string]any{"url": "http://local"},
	}}); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	vars, err := Load(home)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(vars["PYTHON_REGISTRIES"]), &got); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if len(got) != 1 || got["neuronsphere"] == nil {
		t.Errorf("got %v, want just the contribution", got)
	}
}

func TestUpsertJSONMapRefusesToClobberMalformedUserJSON(t *testing.T) {
	t.Parallel()

	// Unparseable is not the same as absent. Overwriting it would destroy
	// something the user plainly meant, so it is reported and left alone.
	home := seed(t, "PYTHON_REGISTRIES=not json at all\n")
	res, err := Upsert(home, []Var{{
		Name:  "PYTHON_REGISTRIES",
		Merge: MergeJSONMap,
		Key:   "neuronsphere",
		Value: map[string]any{"url": "http://local"},
	}})
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if len(res.Problems) != 1 {
		t.Fatalf("Problems = %v, want one", res.Problems)
	}
	if !strings.Contains(read(t, home), "not json at all") {
		t.Error("the user's unparseable value was destroyed")
	}
	if len(res.Written) != 0 {
		t.Errorf("Written = %v, want none", res.Written)
	}
}

func TestUpsertListAppendsAfterTheUsersEntries(t *testing.T) {
	t.Parallel()

	home := seed(t, "GOPROXY=https://proxy.golang.org,direct\n")
	if _, err := Upsert(home, []Var{{
		Name: "GOPROXY", Merge: MergeList, Value: "http://local/go",
	}}); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	vars, err := Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := vars["GOPROXY"], "https://proxy.golang.org,direct,http://local/go"; got != want {
		t.Errorf("GOPROXY = %q, want %q", got, want)
	}
}

func TestUpsertListDoesNotDuplicateOnRerun(t *testing.T) {
	t.Parallel()

	home := seed(t, "HMD_LOCAL_IMAGE_PULL_REGISTRIES=ghcr.io/x\n")
	v := Var{Name: "HMD_LOCAL_IMAGE_PULL_REGISTRIES", Merge: MergeList, Value: "local/y"}
	for i := 0; i < 3; i++ {
		if _, err := Upsert(home, []Var{v}); err != nil {
			t.Fatal(err)
		}
	}

	vars, err := Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(vars["HMD_LOCAL_IMAGE_PULL_REGISTRIES"], "local/y"); n != 1 {
		t.Errorf("the entry appears %d times: %q", n, vars["HMD_LOCAL_IMAGE_PULL_REGISTRIES"])
	}
}

func TestUpsertListHonoursANonDefaultSeparator(t *testing.T) {
	t.Parallel()

	home := seed(t, "GOPROXY=https://a\n")
	if _, err := Upsert(home, []Var{{
		Name: "GOPROXY", Merge: MergeList, Separator: "|", Value: "http://b",
	}}); err != nil {
		t.Fatal(err)
	}

	vars, err := Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := vars["GOPROXY"], "https://a|http://b"; got != want {
		t.Errorf("GOPROXY = %q, want %q", got, want)
	}
}

func TestUpsertIgnoresItsOwnPreviousOutput(t *testing.T) {
	t.Parallel()

	// The user's value is what the file says with the block removed. Reading
	// the merged result back as "the user's value" would make the list grow
	// on every apply and would let a removed contribution persist forever.
	home := seed(t, "GOPROXY=direct\n")
	v := Var{Name: "GOPROXY", Merge: MergeList, Value: "http://local/go"}
	if _, err := Upsert(home, []Var{v}); err != nil {
		t.Fatal(err)
	}
	if _, err := Upsert(home, nil); err != nil {
		t.Fatal(err)
	}

	vars, err := Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if got := vars["GOPROXY"]; got != "direct" {
		t.Errorf("GOPROXY = %q, want the user's own %q", got, "direct")
	}
}

func TestUpsertRoundTripsAValueContainingAnApostrophe(t *testing.T) {
	t.Parallel()

	home := seed(t, "A=b\n")
	const value = "it's here"
	if _, err := Upsert(home, []Var{scalar("NOTE", value)}); err != nil {
		t.Fatal(err)
	}

	vars, err := Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if got := vars["NOTE"]; got != value {
		t.Errorf("NOTE = %q, want %q", got, value)
	}
}

func TestUpsertRefusesADollarInAResolvedValue(t *testing.T) {
	t.Parallel()

	// python-dotenv interpolates $VAR in every value regardless of quote
	// style, while Go does not interpolate at all -- so a surviving $ makes
	// one line mean two different things to the two readers of this file.
	// Skipped and reported, not fatal: one unwritable variable must not stop
	// the others, so the good one beside it still lands.
	home := seed(t, "A=b\n")
	res, err := Upsert(home, []Var{scalar("NOTE", "cost $5"), scalar("GOOD", "fine")})
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if len(res.Problems) != 1 || !strings.Contains(res.Problems[0], "NOTE") {
		t.Errorf("Problems = %v, want one naming NOTE", res.Problems)
	}
	got := read(t, home)
	if strings.Contains(got, "cost $5") {
		t.Errorf("a value containing $ was written:\n%s", got)
	}
	if !strings.Contains(got, "GOOD='fine'") {
		t.Errorf("the usable variable beside it was dropped:\n%s", got)
	}
}

func TestUpsertRefusesAnEmptyHome(t *testing.T) {
	t.Parallel()

	if _, err := Upsert("", []Var{scalar("A", "b")}); err == nil {
		t.Error("Upsert() succeeded with no HMD_HOME, want an error")
	}
}

func TestUpsertRelocatesTheBlockToTheEnd(t *testing.T) {
	t.Parallel()

	// The block only wins by being last, and `hmd configure` appends a new
	// name to the end of this file on every set_key. A block left where it was
	// would be silently outranked by a line written below it.
	home := seed(t, "A=b\n")
	if _, err := Upsert(home, []Var{scalar("GOPROXY", "x")}); err != nil {
		t.Fatal(err)
	}
	appended := read(t, home) + "LATER=written-by-hmd-configure\n"
	if err := os.WriteFile(Path(home), []byte(appended), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Upsert(home, []Var{scalar("GOPROXY", "x")}); err != nil {
		t.Fatal(err)
	}

	got := read(t, home)
	if strings.Index(got, blockBegin) < strings.Index(got, "LATER=") {
		t.Errorf("the block was left above a later line:\n%s", got)
	}
	if !strings.Contains(got, "LATER=written-by-hmd-configure") {
		t.Errorf("the user's later line was lost:\n%s", got)
	}
}

func TestUpsertRefusesAnUnclosedBlock(t *testing.T) {
	t.Parallel()

	// Treating an unterminated marker as running to EOF would swallow every
	// variable below it.
	home := seed(t, blockBegin+"\nGOPROXY='x'\nA=b\n")
	_, err := Upsert(home, []Var{scalar("OTHER", "y")})
	if err == nil {
		t.Fatal("Upsert() accepted a file with an unclosed block")
	}
	if !strings.Contains(read(t, home), "A=b") {
		t.Error("the file was modified despite the refusal")
	}
}

func TestUpsertRefusesTwoBlocks(t *testing.T) {
	t.Parallel()

	one := blockBegin + "\nA='1'\n" + blockEnd + "\n"
	home := seed(t, one+one)
	if _, err := Upsert(home, []Var{scalar("B", "2")}); err == nil {
		t.Fatal("Upsert() accepted a file holding two managed blocks")
	}
}

func TestUpsertDoesNotCreateAFileWithNothingToWrite(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	if _, err := Upsert(home, nil); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if _, err := os.Stat(Path(home)); !os.IsNotExist(err) {
		t.Error("Upsert() created an hmd.env for an empty handback")
	}
}

func TestSplitSeparatesTheBlockFromTheUsersOwnValues(t *testing.T) {
	t.Parallel()

	home := seed(t, "GOPROXY=mine\nHMD_DID=aaa\n")
	if _, err := Upsert(home, []Var{
		scalar("GOPROXY", "theirs"),
		{Name: "PYTHON_REGISTRIES", Merge: MergeJSONMap, Key: "ns", Value: map[string]any{"url": "http://x"}},
	}); err != nil {
		t.Fatal(err)
	}

	managed, user, err := Split(home)
	if err != nil {
		t.Fatalf("Split() error = %v", err)
	}
	if _, ok := managed["PYTHON_REGISTRIES"]; !ok {
		t.Errorf("managed = %v, want the contributed variable", managed)
	}
	if _, ok := managed["GOPROXY"]; ok {
		t.Errorf("managed = %v, want the yielded variable absent", managed)
	}
	if user["GOPROXY"] != "mine" || user["HMD_DID"] != "aaa" {
		t.Errorf("user = %v, want the user's own values", user)
	}
	if _, ok := user["PYTHON_REGISTRIES"]; ok {
		t.Error("the block's own assignment was reported as the user's")
	}
}

func TestSplitOnAMissingFileIsEmpty(t *testing.T) {
	t.Parallel()

	managed, user, err := Split(t.TempDir())
	if err != nil {
		t.Fatalf("Split() error = %v", err)
	}
	if len(managed) != 0 || len(user) != 0 {
		t.Errorf("got %v / %v, want both empty", managed, user)
	}
}
