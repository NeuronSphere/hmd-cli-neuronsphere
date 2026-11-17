package datamodel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bacon"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/data"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// export --repo (NERD039 SPEC011): the one step that makes a repository. It
// is a repo class with nothing to build, holding the model as a NEP051 model
// repository, the table definitions and saved queries, the model as one JSON
// document for generators, and the rendered outputs.

// ExportReport is what export prints.
type ExportReport struct {
	Repo      string   `json:"repo"`
	Name      string   `json:"name"`
	Created   bool     `json:"created"`
	Written   []string `json:"written"`
	Unchanged []string `json:"unchanged"`
	// Kept are files an earlier export wrote that this one no longer
	// produces (a table was removed); export never deletes them.
	Kept []string `json:"kept,omitempty"`
	// Findings are what `nsctl repoclass validate` says about the result.
	Findings []string `json:"findings"`
}

// ExportOptions are the person's choices.
type ExportOptions struct {
	Repo  string
	Name  string
	Force bool
}

// Export writes or refreshes the repository.
func Export(ctx context.Context, w *data.Workspace, r Renderer, opt ExportOptions) (*ExportReport, error) {
	dir, err := filepath.Abs(opt.Repo)
	if err != nil {
		return nil, nserr.Wrap(nserr.Fail, err)
	}
	rec := w.Config.Export
	ours := rec != nil && rec.Out == dir
	if !ours {
		if err := mustBeEmpty(dir); err != nil {
			return nil, err
		}
		rec = &data.DocsRecord{Out: dir}
	}
	if rec.Hashes == nil {
		rec.Hashes = map[string]string{}
	}
	files, err := exportFiles(ctx, w, r)
	if err != nil {
		return nil, err
	}

	// Check every file before writing any, so a refusal changes nothing.
	var refused []string
	for _, rel := range sortedKeys(files) {
		cur, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, nserr.Wrap(nserr.Fail, err)
		}
		if h := hash(cur); h != rec.Hashes[rel] && h != hash(files[rel]) && !opt.Force {
			refused = append(refused, rel)
		}
	}
	if len(refused) > 0 {
		return nil, nserr.New(nserr.Usage, "not replacing %s in %s: edited since the last export. Pass --force to replace them",
			strings.Join(refused, ", "), dir)
	}

	name := opt.Name
	if name == "" {
		name = filepath.Base(dir)
	}
	rep := &ExportReport{Repo: dir, Name: name, Written: []string{}, Unchanged: []string{}, Findings: []string{}}
	if _, _, ok := bacon.Find(dir); !ok {
		// The manifest is the person's once it exists: a later export never
		// rewrites it, so what they add to it (a deploy, discovery) stays.
		s, err := bacon.Init(dir, name, fmt.Sprintf("The data in nsctl data workspace %s: its model, tables, queries and documentation", w.Name))
		if err != nil {
			return nil, err
		}
		build, err := s.Doc.EnsureObject("build")
		if err != nil {
			return nil, err
		}
		// Nothing here is built; hmd build needs the empty list to say so.
		build.Set("commands", []any{})
		if err := s.Save(); err != nil {
			return nil, err
		}
		rep.Created = true
		rep.Written = append(rep.Written, filepath.ToSlash(filepath.Join("meta-data", "manifest.json")))
	}
	for _, rel := range sortedKeys(files) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if cur, err := os.ReadFile(p); err == nil && bytes.Equal(cur, files[rel]) {
			rep.Unchanged = append(rep.Unchanged, rel)
		} else {
			if err := write(p, files[rel]); err != nil {
				return nil, err
			}
			rep.Written = append(rep.Written, rel)
		}
	}
	for _, rel := range sortedKeys(rec.Hashes) {
		if _, ok := files[rel]; !ok {
			if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); err == nil {
				rep.Kept = append(rep.Kept, rel)
			}
		}
	}
	rec.Hashes = map[string]string{}
	for rel, b := range files {
		rec.Hashes[rel] = hash(b)
	}
	w.Config.Export = rec
	if err := w.Save(); err != nil {
		return nil, err
	}
	if s, err := bacon.Open(dir); err == nil {
		for _, f := range bacon.Validate(s, bacon.Known{}) {
			rep.Findings = append(rep.Findings, f.String())
		}
	}
	return rep, nil
}

// mustBeEmpty refuses a directory with anything in it but .git, so a person
// may `git init` first.
func mustBeEmpty(dir string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	for _, e := range entries {
		if e.Name() != ".git" {
			return nserr.New(nserr.Usage, "%s is not empty and was not exported from this workspace; choose a new folder", dir)
		}
	}
	return nil
}

// exportFiles is every file export writes, by repository-relative path.
func exportFiles(ctx context.Context, w *data.Workspace, r Renderer) (map[string][]byte, error) {
	files := map[string][]byte{}
	// The model: the workspace's model/ folder is already a NEP051 model
	// repository, laid out as the export needs.
	model := w.Path(data.ModelDir)
	err := filepath.WalkDir(model, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(model, p)
		files[filepath.ToSlash(rel)] = b
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, nserr.Wrap(nserr.Fail, err)
	}

	tables, err := w.Tables()
	if err != nil {
		return nil, err
	}
	for _, t := range tables {
		b, err := os.ReadFile(w.TablePath(t.Table))
		if err != nil {
			return nil, nserr.Wrap(nserr.Fail, err)
		}
		files["src/data/tables/"+t.Table+".table.yaml"] = b
	}
	queries, err := w.SavedQueries()
	if err != nil {
		return nil, err
	}
	for _, q := range queries {
		files["src/data/queries/"+q.Name+".sql"] = []byte(q.SQL + "\n")
	}
	src, err := yaml.Marshal(map[string]any{
		"note":    "Where each source folder was on the machine that exported this repository. Table paths are relative to their source.",
		"sources": w.Config.Sources,
	})
	if err != nil {
		return nil, err
	}
	files["src/data/sources.yaml"] = src

	m, err := Inspect(ctx, w)
	if err != nil {
		return nil, err
	}
	doc, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	files["src/model/export.json"] = append(doc, '\n')

	rendered, _, err := Render(ctx, w, r)
	if err != nil {
		return nil, err
	}
	for name, b := range rendered {
		files["docs/data/"+name] = b
	}
	files["README.md"] = []byte(exportReadme(w))
	return files, nil
}

func exportReadme(w *data.Workspace) string {
	return fmt.Sprintf(`# %s

The data in the nsctl data workspace %q: what each table is, how the tables
join, and how to query them. Exported with `+"`nsctl data export --repo`"+`;
export again to refresh everything here except meta-data/manifest.json.

| Path | What it is |
|---|---|
| docs/data/DATA.md | The data dictionary: every table and its columns |
| docs/data/relationships.md, .puml | How the tables join, with the evidence |
| docs/data/views.sql | CREATE VIEW statements that run in any DuckDB |
| docs/data/questions.md | Saved and suggested queries |
| src/schemas/, src/sources/ | The model, as NEP051 concepts (.hms) and sources |
| src/data/tables/, src/data/queries/ | Table definitions and saved queries |
| src/model/export.json | The model as one JSON document, for generators |
| meta-data/manifest.json | The repo class manifest; nothing here is built |

A source with `+"`provenance: inferred`"+` is a proposal nobody has answered yet.
`, w.Name, w.Name)
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
