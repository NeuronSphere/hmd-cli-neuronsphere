package datamodel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/data"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// docs (NERD039 SPEC008): nsctl assembles a Mickey context and the Mickey
// image renders it with the hmd-tmpl-data-docs template set. nsctl renders
// nothing itself; a new output format is a template.

// Outputs are the files docs writes, and the only ones it ever replaces.
var Outputs = []string{"DATA.md", "relationships.md", "relationships.puml", "views.sql", "questions.md"}

// DocsImageEnv overrides the renderer image; DefaultDocsImage is the pinned
// one.
const (
	DocsImageEnv     = "HMD_DATA_DOCS_IMAGE"
	DefaultDocsImage = "ghcr.io/hmdlabs/hmd-tmpl-data-docs:0.1.0"
)

// templatePackage is where the image installs the template set, and
// templateSet the set within it.
const (
	templatePackage = "/opt/hmd-tmpl-data-docs"
	templateSet     = "data-docs"
)

// renderDir is the workspace's staging area: the context goes in, the
// renderer's output comes out, and only then is anything copied to the
// person's folder.
const renderDir = "render"

// Renderer turns a context folder into the outputs.
type Renderer interface {
	Render(ctx context.Context, input, output string) error
}

// MickeyRenderer runs the template image with no network.
type MickeyRenderer struct {
	Bin   string
	Image string
}

func (m MickeyRenderer) args(input, output string) []string {
	cfg, _ := json.Marshal(map[string]any{
		"project_name":      templateSet,
		"template_packages": []string{templatePackage},
		"templates":         []string{templateSet},
		"contexts":          []string{"data/*.json"},
	})
	a := []string{"run", "--rm", "--network", "none"}
	if runtime.GOOS == "linux" {
		a = append(a, "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()))
	}
	return append(a,
		"-v", input+":/hmd_transform/input:ro",
		"-v", output+":/hmd_transform/output",
		"--tmpfs", "/hmd_transform/cache",
		"-e", "MICKEY_SKIP_DEPS=1",
		"-e", "TRANSFORM_INSTANCE_CONTEXT="+string(cfg),
		m.Image)
}

func (m MickeyRenderer) Render(ctx context.Context, input, output string) error {
	bin := m.Bin
	if bin == "" {
		bin = "docker"
	}
	c := exec.CommandContext(ctx, bin, m.args(input, output)...)
	var out bytes.Buffer
	c.Stdout, c.Stderr = &out, &out
	if err := c.Run(); err != nil {
		tail := strings.TrimSpace(out.String())
		if len(tail) > 2000 {
			tail = tail[len(tail)-2000:]
		}
		return nserr.New(nserr.Fail, "rendering with %s: %v\n%s", m.Image, err, tail)
	}
	return nil
}

// DocsReport is what docs prints.
type DocsReport struct {
	Out       string   `json:"out"`
	Written   []string `json:"written"`
	Unchanged []string `json:"unchanged"`
	// Views are the named views docs created in the catalog.
	Views []string `json:"views"`
}

// Docs renders the outputs into out, then creates the named views in the
// catalog so what a person queries is what the outputs document.
func Docs(ctx context.Context, w *data.Workspace, eng data.Engine, r Renderer, out string, force bool) (*DocsReport, error) {
	if _, err := os.Stat(w.Path(data.CatalogFile)); err != nil {
		return nil, nserr.New(nserr.Usage, "workspace %s has no catalog yet. Run `nsctl data register` first", w.Name)
	}
	rendered, dc, err := Render(ctx, w, r)
	if err != nil {
		return nil, err
	}
	rep, err := place(w, out, rendered, force)
	if err != nil {
		return nil, err
	}
	if _, _, err := w.Query(ctx, eng, string(rendered["views.sql"]), true); err != nil {
		return nil, nserr.New(nserr.Fail, "views.sql did not run against the catalog: %v", err)
	}
	for _, v := range dc.Views {
		rep.Views = append(rep.Views, v.Name)
	}
	return rep, nil
}

// Render runs the renderer over the workspace's context and returns each
// output's content, writing nothing outside the workspace's staging folder.
func Render(ctx context.Context, w *data.Workspace, r Renderer) (map[string][]byte, *DocsContext, error) {
	dc, err := Context(ctx, w)
	if err != nil {
		return nil, nil, err
	}
	// A new folder for every run: a bind mount of a folder deleted and made
	// again at the same path can reach the container as the old, deleted
	// one (seen under colima's virtiofs), and the render then has nowhere to
	// write.
	if err := os.MkdirAll(w.Path(renderDir), 0o755); err != nil {
		return nil, nil, nserr.Wrap(nserr.Fail, err)
	}
	stage, err := os.MkdirTemp(w.Path(renderDir), "run-")
	if err != nil {
		return nil, nil, nserr.Wrap(nserr.Fail, err)
	}
	defer os.RemoveAll(stage)
	input, output := filepath.Join(stage, "input"), filepath.Join(stage, "output")
	b, err := json.MarshalIndent(dc, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	if err := write(filepath.Join(input, "data", "workspace.json"), b); err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(output, 0o755); err != nil {
		return nil, nil, nserr.Wrap(nserr.Fail, err)
	}
	if err := r.Render(ctx, input, output); err != nil {
		return nil, nil, err
	}
	rendered := map[string][]byte{}
	for _, name := range Outputs {
		b, err := os.ReadFile(filepath.Join(output, name))
		if err != nil {
			return nil, nil, nserr.New(nserr.Fail, "the renderer did not write %s; is %s the data docs image?", name, DocsImageEnv)
		}
		rendered[name] = b
	}
	return rendered, dc, nil
}

// place copies the rendered files into out. A file docs wrote before and
// someone has since edited, or a file docs never wrote, is not replaced
// unless force; nothing is deleted.
func place(w *data.Workspace, out string, rendered map[string][]byte, force bool) (*DocsReport, error) {
	rec := w.Config.Docs
	if rec == nil || rec.Out != out {
		rec = &data.DocsRecord{Out: out}
	}
	if rec.Hashes == nil {
		rec.Hashes = map[string]string{}
	}
	var refused []string
	for _, name := range Outputs {
		cur, err := os.ReadFile(filepath.Join(out, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, nserr.Wrap(nserr.Fail, err)
		}
		if h := hash(cur); h != rec.Hashes[name] && h != hash(rendered[name]) && !force {
			refused = append(refused, filepath.Join(out, name))
		}
	}
	if len(refused) > 0 {
		return nil, nserr.New(nserr.Usage,
			"not replacing %s: nsctl did not write it, or it was edited since. Move it aside, or pass --force to replace it",
			strings.Join(refused, ", "))
	}
	rep := &DocsReport{Out: out, Written: []string{}, Unchanged: []string{}, Views: []string{}}
	for _, name := range Outputs {
		p := filepath.Join(out, name)
		if cur, err := os.ReadFile(p); err == nil && bytes.Equal(cur, rendered[name]) {
			rep.Unchanged = append(rep.Unchanged, p)
		} else {
			if err := write(p, rendered[name]); err != nil {
				return nil, err
			}
			rep.Written = append(rep.Written, p)
		}
		rec.Hashes[name] = hash(rendered[name])
	}
	w.Config.Docs = rec
	if err := w.Save(); err != nil {
		return nil, err
	}
	return rep, nil
}

func hash(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// DocsContext is the Mickey context: what the templates read, plus the
// NERD032 model and the profiles for any template that wants more.
type DocsContext struct {
	Workspace     string                   `json:"workspace"`
	GeneratedAt   string                   `json:"generated_at"`
	Tables        []DocTable               `json:"tables"`
	Relationships []DocRelationship        `json:"relationships"`
	Views         []DocView                `json:"views"`
	Queries       []DocQuery               `json:"queries"`
	Model         any                      `json:"model"`
	Profiles      map[string]*data.Profile `json:"profiles"`
}

// DocTable is a table, and the entity it is when one was proposed.
type DocTable struct {
	Name  string `json:"name"`
	Alias string `json:"alias"`
	// Status is approved, proposed, or "" for a table nobody has been
	// asked about (or whose proposal was rejected).
	Status string `json:"status"`
	// View is the entity's name when it is approved under a name of its
	// own; ViewSQLName is it quoted for SQL.
	View        string      `json:"view,omitempty"`
	ViewSQLName string      `json:"view_sql_name,omitempty"`
	Description string      `json:"description,omitempty"`
	Key         string      `json:"key,omitempty"`
	Rows        int64       `json:"rows"`
	Format      string      `json:"format"`
	Paths       []string    `json:"paths"`
	Columns     []DocColumn `json:"columns"`
	Issues      []string    `json:"issues"`
}

// DocColumn is one column as the dictionary shows it.
type DocColumn struct {
	Name     string   `json:"name"`
	SQLType  string   `json:"sql_type"`
	NullPct  float64  `json:"null_pct"`
	Distinct int64    `json:"distinct"`
	Min      string   `json:"min,omitempty"`
	Max      string   `json:"max,omitempty"`
	PII      bool     `json:"pii"`
	Notes    []string `json:"notes"`
}

// DocRelationship is a join, confirmed or not.
type DocRelationship struct {
	Name        string `json:"name"`
	Status      string `json:"status"`
	FromTable   string `json:"from_table"`
	FromColumn  string `json:"from_column"`
	ToTable     string `json:"to_table"`
	ToColumn    string `json:"to_column"`
	FromLabel   string `json:"from_label"`
	ToLabel     string `json:"to_label"`
	FromAlias   string `json:"from_alias"`
	ToAlias     string `json:"to_alias"`
	Evidence    string `json:"evidence"`
	Cardinality string `json:"cardinality"`
	Description string `json:"description,omitempty"`
}

// DocView is one CREATE VIEW in views.sql.
type DocView struct {
	Name        string `json:"name"`
	SQL         string `json:"sql"`
	Description string `json:"description,omitempty"`
}

// DocQuery is a saved or suggested query.
type DocQuery struct {
	Name  string `json:"name"`
	Title string `json:"title"`
	SQL   string `json:"sql"`
	Saved bool   `json:"saved"`
}

// Context assembles the docs context from the workspace.
func Context(ctx context.Context, w *data.Workspace) (*DocsContext, error) {
	tables, err := w.Tables()
	if err != nil {
		return nil, err
	}
	rep, err := Load(ctx, w)
	if err != nil {
		return nil, err
	}
	m, err := Inspect(ctx, w)
	if err != nil {
		return nil, err
	}
	sources, err := readSources(w)
	if err != nil {
		return nil, err
	}
	dc := &DocsContext{Workspace: w.Name, GeneratedAt: time.Now().UTC().Format("2006-01-02"),
		Tables: []DocTable{}, Relationships: []DocRelationship{}, Views: []DocView{}, Queries: []DocQuery{},
		Model: m, Profiles: map[string]*data.Profile{}}

	entity := map[string]Proposal{}
	for _, list := range [][]Proposal{rep.Approved, rep.Proposals} {
		for _, p := range list {
			if p.Kind == Entity {
				entity[p.Table] = p
			}
		}
	}
	label := map[string]string{}
	viewName := map[string]string{}
	for _, t := range tables {
		dt := DocTable{Name: t.Table, Alias: "t_" + t.Table, Format: t.Format, Paths: t.Paths,
			Columns: []DocColumn{}, Issues: []string{}}
		label[t.Table] = t.Table
		viewName[t.Table] = data.Ident(t.Table)
		if p, ok := entity[t.Table]; ok {
			dt.Status, dt.Description, dt.Key = p.Status, p.Description, p.Key
			if p.Status == "approved" && p.Name != t.Table {
				dt.View, dt.ViewSQLName = p.Name, data.Ident(p.Name)
				label[t.Table], viewName[t.Table] = p.Name, dt.ViewSQLName
			}
		}
		if prof, err := w.SavedProfile(t.Table); err == nil {
			dc.Profiles[t.Table] = prof
			dt.Rows = prof.Rows
			notes := map[string][]string{}
			for _, i := range prof.Issues {
				if i.Column == "" {
					dt.Issues = append(dt.Issues, i.Message)
				} else if i.Kind != data.IssuePII {
					notes[i.Column] = append(notes[i.Column], i.Message+".")
				}
			}
			for _, c := range prof.Columns {
				dcol := DocColumn{Name: c.Name, SQLType: c.DuckType, NullPct: math.Round(c.NullFraction*1000) / 10,
					Distinct: c.Distinct, PII: c.PII, Notes: notes[c.Name]}
				if dcol.Notes == nil {
					dcol.Notes = []string{}
				}
				if !c.PII {
					dcol.Min, dcol.Max = c.Min, c.Max
				}
				dt.Columns = append(dt.Columns, dcol)
			}
		}
		dc.Tables = append(dc.Tables, dt)

		v, err := w.ViewSQL(t)
		if err != nil {
			return nil, err
		}
		desc := fmt.Sprintf("%s: %s files, read in place.", t.Table, t.Format)
		if l, ok := w.Config.Landed[t.Table]; ok {
			desc = fmt.Sprintf("%s: landed in environment %s at %s; `nsctl data connect` prints the secret a DuckDB needs to read it.",
				t.Table, w.Config.Storage.Env, l.Path)
		}
		dc.Views = append(dc.Views, DocView{Name: t.Table, SQL: v, Description: desc})
	}
	// Named views, after the table views they read.
	for _, dt := range dc.Tables {
		if dt.View == "" {
			continue
		}
		f := sources[dt.Name]
		var cols []string
		for _, mp := range f.Yields[0].Maps {
			if mp.SQL != "" {
				cols = append(cols, mp.SQL+" AS "+data.Ident(mp.Attribute))
			} else if mp.Column == mp.Attribute {
				cols = append(cols, data.Ident(mp.Column))
			} else {
				cols = append(cols, data.Ident(mp.Column)+" AS "+data.Ident(mp.Attribute))
			}
		}
		dc.Views = append(dc.Views, DocView{Name: dt.View, Description: dt.Description,
			SQL: fmt.Sprintf("CREATE OR REPLACE VIEW %s AS SELECT %s FROM %s;", dt.ViewSQLName, strings.Join(cols, ", "), data.Ident(dt.Name))})
	}

	var suggested []DocQuery
	for _, list := range [][]Proposal{rep.Approved, rep.Proposals} {
		for _, p := range list {
			if p.Kind != Relationship || p.Join == nil {
				continue
			}
			j := p.Join
			dc.Relationships = append(dc.Relationships, DocRelationship{Name: p.Name, Status: p.Status,
				FromTable: j.FromTable, FromColumn: j.FromColumn, ToTable: j.ToTable, ToColumn: j.ToColumn,
				FromLabel: label[j.FromTable], ToLabel: label[j.ToTable], FromAlias: "t_" + j.FromTable, ToAlias: "t_" + j.ToTable,
				Evidence:    fmt.Sprintf("%d of %d values match", j.Matched, j.FromDistinct),
				Cardinality: strings.ReplaceAll(j.Cardinality, "_", " "), Description: p.Description})
			if p.Status != "approved" {
				continue
			}
			suggested = append(suggested, DocQuery{Name: p.Name,
				Title: fmt.Sprintf("How many %s rows does each %s have?", label[j.FromTable], label[j.ToTable]),
				SQL: fmt.Sprintf("SELECT t.%s, count(*) AS %s\nFROM %s AS f\nJOIN %s AS t ON f.%s = t.%s\nGROUP BY ALL\nORDER BY 2 DESC\nLIMIT 10;",
					data.Ident(j.ToColumn), data.Ident(label[j.FromTable]+"_rows"), viewName[j.FromTable], viewName[j.ToTable],
					data.Ident(j.FromColumn), data.Ident(j.ToColumn))})
		}
	}
	sort.SliceStable(dc.Relationships, func(a, b int) bool { return dc.Relationships[a].Status < dc.Relationships[b].Status })

	saved, err := w.SavedQueries()
	if err != nil {
		return nil, err
	}
	for _, q := range saved {
		dc.Queries = append(dc.Queries, DocQuery{Name: q.Name, Title: strings.ReplaceAll(q.Name, "_", " "), SQL: q.SQL, Saved: true})
	}
	dc.Queries = append(dc.Queries, suggested...)
	return dc, nil
}
