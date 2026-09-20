package bacon

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestOpenReportsNoManifest(t *testing.T) {
	t.Parallel()

	_, err := Open(t.TempDir())
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// SPEC006's precedence: TOML under meta-data, then JSON, then the root file.
func TestOpenFollowsThePrecedence(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "neuronsphere.toml"), "name = \"root\"\n")
	s, err := Open(dir)
	if err != nil || s.Format != FormatTOML || s.Location != LocationRoot {
		t.Fatalf("root tier: %+v, %v", s, err)
	}
	writeFile(t, filepath.Join(dir, "meta-data", "manifest.json"), `{"name": "json"}`)
	s, err = Open(dir)
	if err != nil || s.Format != FormatJSON || s.Rel != filepath.Join("meta-data", "manifest.json") {
		t.Fatalf("json tier: %+v, %v", s, err)
	}
	writeFile(t, filepath.Join(dir, "meta-data", "manifest.toml"), "name = \"toml\"\n[build]\nmechanism = \"external\"\n")
	s, err = Open(dir)
	if err != nil || s.Format != FormatTOML {
		t.Fatalf("toml tier: %+v, %v", s, err)
	}
	if name, _ := s.Doc.String("name"); name != "toml" {
		t.Errorf("name = %q", name)
	}
	if mech, ok := s.Doc.Lookup("build", "mechanism"); !ok || mech != "external" {
		t.Errorf("build.mechanism = %v, %v", mech, ok)
	}
}

// A write returns to the file it was read from; a TOML tier refuses, naming
// the file, rather than rewriting it without its comments.
func TestSaveReturnsToTheSameFileAndRefusesTOML(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "meta-data", "manifest.json")
	writeFile(t, path, "{\n  \"name\": \"x\",\n  \"keep\": {\n    \"me\": 1\n  }\n}")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.Doc.Set("description", "added")
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "\"keep\"") || !strings.Contains(string(data), "\"description\": \"added\"") {
		t.Errorf("saved:\n%s", data)
	}

	tdir := t.TempDir()
	tpath := filepath.Join(tdir, "meta-data", "manifest.toml")
	writeFile(t, tpath, "# a comment\nname = \"x\"\n")
	ts, err := Open(tdir)
	if err != nil {
		t.Fatal(err)
	}
	err = ts.Save()
	if err == nil || nserr.CodeOf(err) != nserr.Fail || !strings.Contains(err.Error(), tpath) {
		t.Errorf("TOML save = %v, want a refusal naming %s", err, tpath)
	}
	after, _ := os.ReadFile(tpath)
	if string(after) != "# a comment\nname = \"x\"\n" {
		t.Errorf("the TOML file was touched:\n%s", after)
	}
}

// SPEC007: init writes the three required members and VERSION, and refuses
// when any tier exists, naming describe and validate.
func TestInitWritesTheMinimumAndRefusesASecondTime(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	s, err := Init(dir, "acme-api", "The Acme API")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(s.Path)
	want := "{\n  \"name\": \"acme-api\",\n  \"description\": \"The Acme API\",\n  \"build\": {}\n}"
	if string(data) != want {
		t.Errorf("wrote:\n%s\nwant:\n%s", data, want)
	}
	version, _ := os.ReadFile(filepath.Join(dir, "meta-data", "VERSION"))
	if string(version) != "0.1\n" {
		t.Errorf("VERSION = %q", version)
	}

	_, err = Init(dir, "again", "")
	if err == nil || nserr.CodeOf(err) != nserr.Usage {
		t.Fatalf("second init = %v, want a usage refusal", err)
	}
	for _, want := range []string{"nsctl repoclass describe", "nsctl repoclass validate", s.Path} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not mention %q: %v", want, err)
		}
	}
}

// An existing VERSION is the user's and is not overwritten.
func TestInitKeepsAnExistingVersion(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "meta-data", "VERSION"), "2.7\n")
	if _, err := Init(dir, "x", "y"); err != nil {
		t.Fatal(err)
	}
	version, _ := os.ReadFile(filepath.Join(dir, "meta-data", "VERSION"))
	if string(version) != "2.7\n" {
		t.Errorf("VERSION = %q, want the user's 2.7", version)
	}
}
