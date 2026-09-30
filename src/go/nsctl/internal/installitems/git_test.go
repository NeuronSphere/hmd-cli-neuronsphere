package installitems

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// NERD031 SPEC011: a git item is cloned into the user's repository folder,
// once, and is the user's from then on.

func bareRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	work := tree(t, files)
	bare := filepath.Join(t.TempDir(), "notes.git")
	for _, args := range [][]string{
		{"-C", work, "init", "-q", "-b", "main"},
		{"-C", work, "add", "-A"},
		{"-C", work, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "init"},
		{"clone", "-q", "--bare", work, bare},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return bare
}

func gitDocsTree(t *testing.T, url string) string {
	return manifestTree(t, `{"items": [
	  {"kind": "docs", "title": "Design notes", "source": {"git": "`+url+`", "ref": "main"}},
	  {"kind": "agent-skills", "dir": "skills", "source": {"git": "`+url+`"}}]}`, nil)
}

func TestAGitItemClonesIntoTheRepoFolderOnce(t *testing.T) {
	t.Parallel()
	url := bareRepo(t, map[string]string{
		"README.md":                 "notes",
		"skills/notes-fmt/SKILL.md": "---\nname: notes-fmt\n---\n",
	})
	f := newFixture(t, false)
	items, err := f.install(gitDocsTree(t, url))
	if err != nil {
		t.Fatalf("%v\n%s", err, f.stderr.String())
	}
	clone := filepath.Join(f.repos, "notes")
	if items[0].Path != clone || !items[0].Clone {
		t.Errorf("docs item = %+v", items[0])
	}
	if _, err := os.Stat(filepath.Join(clone, "README.md")); err != nil {
		t.Fatalf("not cloned: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.userDir, ".claude", "skills", "notes-fmt", "SKILL.md")); err != nil {
		t.Errorf("skills were not copied from the clone: %v", err)
	}

	// The user's edit survives a second install over the same origin.
	if err := os.WriteFile(filepath.Join(clone, "README.md"), []byte("my edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.install(gitDocsTree(t, url)); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(clone, "README.md")); string(b) != "my edit" {
		t.Errorf("a second install touched the clone: %q", b)
	}
	// Remove forgets the clone and never deletes it.
	var out bytes.Buffer
	if err := Remove(f.home, "kit", items, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(clone); err != nil {
		t.Errorf("remove deleted the user's clone: %v", err)
	}
	if !strings.Contains(out.String(), clone) {
		t.Errorf("remove did not name the clone it kept:\n%s", out.String())
	}
}

func TestAnotherOriginAtTheTargetIsRefused(t *testing.T) {
	t.Parallel()
	url := bareRepo(t, map[string]string{"README.md": "notes"})
	other := bareRepo(t, map[string]string{"README.md": "other"})
	f := newFixture(t, false)
	if out, err := exec.Command("git", "clone", "-q", other, filepath.Join(f.repos, "notes")).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	_, err := f.install(manifestTree(t, `{"items": [{"kind": "docs", "title": "t", "source": {"git": "`+url+`"}}]}`, nil))
	if err == nil || !strings.Contains(err.Error(), filepath.Join(f.repos, "notes")) {
		t.Fatalf("err = %v", err)
	}
}

func TestAGitItemNeedsARepoHome(t *testing.T) {
	t.Parallel()
	url := bareRepo(t, map[string]string{"README.md": "notes"})
	f := newFixture(t, false)
	f.repos = ""
	_, err := f.install(manifestTree(t, `{"items": [{"kind": "docs", "title": "t", "source": {"git": "`+url+`"}}]}`, nil))
	if err == nil || !strings.Contains(err.Error(), "HMD_REPO_HOME") {
		t.Fatalf("err = %v", err)
	}
}

func TestSameOrigin(t *testing.T) {
	t.Parallel()
	for _, pair := range [][2]string{
		{"https://github.com/acme/notes.git", "https://github.com/acme/notes"},
		{"https://github.com/acme/notes/", "https://github.com/acme/notes.git"},
		{"git@github.com:acme/notes.git", "git@github.com:acme/notes"},
	} {
		if !sameOrigin(pair[0], pair[1]) {
			t.Errorf("%q and %q are the same origin", pair[0], pair[1])
		}
	}
	if sameOrigin("https://github.com/acme/notes", "https://github.com/evil/notes") {
		t.Error("different owners compared equal")
	}
}
