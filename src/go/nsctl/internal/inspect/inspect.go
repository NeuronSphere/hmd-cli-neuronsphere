// Package inspect runs inspectors over repositories and collects what they
// observed (NERD032 SPEC001). It knows nothing about any one kind of
// artifact: an inspector is a small type in its own package, and the command
// decides which inspectors run.
package inspect

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/model"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/repoclass"
)

// Source is one repository to inspect.
type Source struct {
	// Root is the absolute directory.
	Root string `json:"root"`
	// Repo is the repository's name: its BACON name, else its directory name.
	Repo string `json:"repo"`
	// Revision is the commit HEAD points at, "" when it cannot be read.
	Revision string `json:"revision,omitempty"`
	// FS reads the repository; paths are slash-separated and repo-relative.
	FS fs.FS `json:"-"`
}

// Inspector reads one kind of artifact and reports what it saw.
type Inspector interface {
	// Name identifies the inspector in provenance.
	Name() string
	// CanInspect is a cheap check that the source holds anything this
	// inspector reads.
	CanInspect(ctx context.Context, src Source) bool
	// Inspect returns observations. A file it cannot read is a finding
	// observation, not an error; an error means the whole source was
	// unreadable.
	Inspect(ctx context.Context, src Source) ([]model.Observation, error)
}

// Report says what one inspector made of one source.
type Report struct {
	Repo         string `json:"repo"`
	Inspector    string `json:"inspector"`
	Observations int    `json:"observations"`
	Error        string `json:"error,omitempty"`
}

// Run inspects every source with every inspector that can, and stamps each
// observation with its source's repository, revision and inspector. An
// inspector's error becomes a finding: one unreadable source does not end the
// inspection of the rest.
func Run(ctx context.Context, sources []Source, inspectors []Inspector) ([]model.Observation, []Report) {
	var all []model.Observation
	var reports []Report
	for _, src := range sources {
		for _, ins := range inspectors {
			if err := ctx.Err(); err != nil {
				return all, reports
			}
			if !ins.CanInspect(ctx, src) {
				continue
			}
			obs, err := ins.Inspect(ctx, src)
			r := Report{Repo: src.Repo, Inspector: ins.Name(), Observations: len(obs)}
			if err != nil {
				r.Error = err.Error()
				obs = append(obs, model.Observation{
					Kind:       model.KindFinding,
					Finding:    &model.FindingObs{Severity: model.SevWarning, Code: "inspector-failed", Message: err.Error()},
					Provenance: model.Provenance{File: "."},
				})
			}
			for i := range obs {
				p := &obs[i].Provenance
				if p.Inspector == "" {
					p.Inspector = ins.Name()
				}
				p.Repo, p.Revision = src.Repo, src.Revision
			}
			all = append(all, obs...)
			reports = append(reports, r)
		}
	}
	return all, reports
}

// Discover turns paths into sources. A path that is a repository (it has
// meta-data/ or .git) is one source; any other directory stands for the
// repositories directly inside it, so a parent directory inspects a whole
// set of repositories at once.
func Discover(paths []string) ([]Source, error) {
	seen := map[string]bool{}
	var out []Source
	add := func(dir string) {
		if seen[dir] {
			return
		}
		seen[dir] = true
		out = append(out, NewSource(dir))
	}
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(abs)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("%s is not a directory", p)
		}
		if isRepo(abs) {
			add(abs)
			continue
		}
		entries, err := os.ReadDir(abs)
		if err != nil {
			return nil, err
		}
		found := false
		for _, e := range entries {
			child := filepath.Join(abs, e.Name())
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") && isRepo(child) {
				add(child)
				found = true
			}
		}
		if !found {
			// A plain directory with no repositories in it is still inspected,
			// so that a scratch copy of a few files works.
			add(abs)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Root < out[j].Root })
	return out, nil
}

// NewSource describes one directory.
func NewSource(dir string) Source {
	name := filepath.Base(dir)
	if m, err := repoclass.ReadManifest(dir); err == nil && m != nil && m.Name != "" {
		name = m.Name
	}
	return Source{Root: dir, Repo: name, Revision: Revision(dir), FS: os.DirFS(dir)}
}

func isRepo(dir string) bool {
	for _, marker := range []string{"meta-data", ".git"} {
		if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
			return true
		}
	}
	return false
}

// Revision reads the commit HEAD points at without running git: HEAD, the
// ref it names, then packed-refs. A linked worktree's .git is a file naming
// its gitdir, whose commondir holds the shared refs. Anything unexpected
// gives "", which only weakens provenance.
func Revision(dir string) string {
	gitdir := filepath.Join(dir, ".git")
	info, err := os.Stat(gitdir)
	if err != nil {
		return ""
	}
	if !info.IsDir() {
		data, err := os.ReadFile(gitdir)
		if err != nil {
			return ""
		}
		line := strings.TrimSpace(string(data))
		if !strings.HasPrefix(line, "gitdir:") {
			return ""
		}
		gitdir = strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
		if !filepath.IsAbs(gitdir) {
			gitdir = filepath.Join(dir, gitdir)
		}
	}
	head, err := os.ReadFile(filepath.Join(gitdir, "HEAD"))
	if err != nil {
		return ""
	}
	ref := strings.TrimSpace(string(head))
	if !strings.HasPrefix(ref, "ref:") {
		return ref
	}
	ref = strings.TrimSpace(strings.TrimPrefix(ref, "ref:"))
	common := gitdir
	if data, err := os.ReadFile(filepath.Join(gitdir, "commondir")); err == nil {
		common = strings.TrimSpace(string(data))
		if !filepath.IsAbs(common) {
			common = filepath.Join(gitdir, common)
		}
	}
	for _, d := range []string{gitdir, common} {
		if data, err := os.ReadFile(filepath.Join(d, filepath.FromSlash(ref))); err == nil {
			return strings.TrimSpace(string(data))
		}
	}
	packed, err := os.ReadFile(filepath.Join(common, "packed-refs"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(packed), "\n") {
		if sha, name, ok := strings.Cut(strings.TrimSpace(line), " "); ok && name == ref {
			return sha
		}
	}
	return ""
}

// Walk lists the regular files under root in src whose names match, in
// lexical order. Hidden directories and dependency trees (node_modules,
// target, dbt_packages) are skipped.
func Walk(src Source, root string, match func(path string) bool) ([]string, error) {
	var out []string
	err := fs.WalkDir(src.FS, root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == root {
				return err
			}
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (strings.HasPrefix(name, ".") || skipDirs[name]) {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() && match(path) {
			out = append(out, path)
		}
		return nil
	})
	return out, err
}

var skipDirs = map[string]bool{
	"node_modules": true, "target": true, "dbt_packages": true, "logs": true,
	"__pycache__": true, "venv": true, "build": true, "dist": true,
}

// LineOf returns the 1-based line on which needle first occurs in text, or 0.
func LineOf(text, needle string) int {
	i := strings.Index(text, needle)
	if i < 0 {
		return 0
	}
	return strings.Count(text[:i], "\n") + 1
}
