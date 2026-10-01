package inspect

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/model"
)

func write(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDiscoverExpandsAParentDirectory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, map[string]string{
		"repo-a/meta-data/manifest.json": `{"name": "hmd-lang-a"}`,
		"repo-b/.git/HEAD":               "0123456789abcdef\n",
		"not-a-repo/readme.txt":          "x",
		".hidden/meta-data/x":            "x",
	})
	srcs, err := Discover([]string{root, filepath.Join(root, "repo-a")})
	if err != nil {
		t.Fatal(err)
	}
	if len(srcs) != 2 {
		t.Fatalf("sources = %+v", srcs)
	}
	if srcs[0].Repo != "hmd-lang-a" || srcs[1].Repo != "repo-b" || srcs[1].Revision != "0123456789abcdef" {
		t.Errorf("sources = %+v", srcs)
	}
}

func TestRevisionFollowsRefsAndPackedRefs(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, map[string]string{
		"loose/.git/HEAD":                  "ref: refs/heads/main\n",
		"loose/.git/refs/heads/main":       "aaaa\n",
		"packed/.git/HEAD":                 "ref: refs/heads/main\n",
		"packed/.git/packed-refs":          "# pack-refs\nbbbb refs/heads/main\n",
		"main/.git/worktrees/wt/HEAD":      "ref: refs/heads/feat\n",
		"main/.git/worktrees/wt/commondir": "../..\n",
		"main/.git/refs/heads/feat":        "cccc\n",
	})
	write(t, root, map[string]string{"wt/.git": "gitdir: " + filepath.Join(root, "main/.git/worktrees/wt") + "\n"})
	for dir, want := range map[string]string{"loose": "aaaa", "packed": "bbbb", "wt": "cccc", "none": ""} {
		if got := Revision(filepath.Join(root, dir)); got != want {
			t.Errorf("Revision(%s) = %q, want %q", dir, got, want)
		}
	}
}

type fake struct {
	name string
	err  error
}

func (f fake) Name() string                            { return f.name }
func (f fake) CanInspect(context.Context, Source) bool { return true }
func (f fake) Inspect(context.Context, Source) ([]model.Observation, error) {
	return []model.Observation{{Kind: model.KindNoun, Subject: model.ID{Name: "n"}}}, f.err
}

func TestRunStampsProvenanceAndKeepsGoingOnError(t *testing.T) {
	t.Parallel()
	srcs := []Source{{Repo: "r1", Revision: "abc"}, {Repo: "r2"}}
	obs, reports := Run(context.Background(), srcs, []Inspector{fake{name: "ok"}, fake{name: "bad", err: errors.New("boom")}})
	if len(reports) != 4 || reports[1].Error != "boom" {
		t.Fatalf("reports = %+v", reports)
	}
	// 4 runs x 1 observation, plus 2 findings for the failures.
	if len(obs) != 6 {
		t.Fatalf("observations = %d", len(obs))
	}
	if obs[0].Provenance.Repo != "r1" || obs[0].Provenance.Revision != "abc" || obs[0].Provenance.Inspector != "ok" {
		t.Errorf("provenance = %+v", obs[0].Provenance)
	}
	var findings int
	for _, o := range obs {
		if o.Kind == model.KindFinding && o.Finding.Code == "inspector-failed" {
			findings++
		}
	}
	if findings != 2 {
		t.Errorf("findings = %d", findings)
	}
}
