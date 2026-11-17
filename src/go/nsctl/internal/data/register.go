package data

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// register builds catalog.duckdb: one view per table over its files in place,
// or over its snapshot (NERD039 SPEC005).

// TableStatus is one table after a register, or in status.
type TableStatus struct {
	Table    string   `json:"table"`
	Rows     int64    `json:"rows,omitempty"`
	Columns  int      `json:"columns,omitempty"`
	Pinned   bool     `json:"pinned"`
	Snapshot bool     `json:"snapshot"`
	Steps    []string `json:"steps"`
	// Drift lists how the files differ from the pinned schema.
	Drift []string `json:"drift,omitempty"`
	// Landed is where the table's view reads from once landed (SPEC009).
	Landed string `json:"landed,omitempty"`
	// Stale lists sources changed since the snapshot or landing was taken.
	Stale []string `json:"stale,omitempty"`
	Error string   `json:"error,omitempty"`
}

// RegisterReport is what `nsctl data register` prints.
type RegisterReport struct {
	Workspace string        `json:"workspace"`
	Catalog   string        `json:"catalog"`
	Tables    []TableStatus `json:"tables"`
	Dropped   []string      `json:"dropped,omitempty"`
}

// viewSource is the SELECT a table's view runs.
func (w *Workspace) viewSource(t *Table) (string, error) {
	if l, ok := w.Config.Landed[t.Table]; ok {
		return "SELECT * FROM read_parquet(" + lit(l.Path) + ")", nil
	}
	if snap, ok := w.Config.Snapshots[t.Table]; ok {
		return "SELECT * FROM read_parquet(" + lit(snap.Path) + ")", nil
	}
	return w.fileSource(t)
}

// fileSource reads a table from its files, ignoring any snapshot or landing.
func (w *Workspace) fileSource(t *Table) (string, error) {
	abs, err := w.AbsPaths(t)
	if err != nil {
		return "", err
	}
	reader, err := readerSQL(t, abs)
	if err != nil {
		return "", err
	}
	return selectSQL(t, reader)
}

// schemaDiff compares a pinned schema with what the files now give. It is
// pure.
func schemaDiff(pinned []Column, now []Column) []string {
	got := map[string]string{}
	for _, c := range now {
		got[strings.ToLower(c.Name)] = c.Type
	}
	want := map[string]bool{}
	var out []string
	for _, c := range pinned {
		k := strings.ToLower(c.Name)
		want[k] = true
		t, ok := got[k]
		switch {
		case !ok:
			out = append(out, fmt.Sprintf("%s is missing", c.Name))
		case t != c.Type:
			out = append(out, fmt.Sprintf("%s is %s, pinned as %s", c.Name, t, c.Type))
		}
	}
	for _, c := range now {
		if !want[strings.ToLower(c.Name)] {
			out = append(out, fmt.Sprintf("%s is new", c.Name))
		}
	}
	return out
}

// Register (re)builds the catalog. With pin, each table's current schema is
// written into its definition.
func (w *Workspace) Register(ctx context.Context, eng Engine, pin bool) (*RegisterReport, error) {
	tables, err := w.Tables()
	if err != nil {
		return nil, err
	}
	if len(tables) == 0 {
		return nil, nserr.New(nserr.Usage, "workspace %s has no tables yet. Run `nsctl data scan --apply` first", w.Name)
	}
	rep := &RegisterReport{Workspace: w.Name, Catalog: w.Path(CatalogFile)}

	// Existing views, so a table that no longer exists loses its view.
	out, err := eng.Run(ctx, w.session(Request{DB: rep.Catalog,
		Script: "SELECT view_name FROM duckdb_views() WHERE NOT internal AND schema_name = 'main';\n"}))
	if err != nil {
		return nil, err
	}
	keep := map[string]bool{}
	for _, t := range tables {
		keep[t.Table] = true
	}
	var script strings.Builder
	if len(out.Results) > 0 {
		for _, r := range out.Results[0] {
			v := str(r["view_name"])
			if !keep[v] {
				fmt.Fprintf(&script, "DROP VIEW IF EXISTS %s;\n", ident(v))
				rep.Dropped = append(rep.Dropped, v)
			}
		}
	}
	for _, t := range tables {
		src, err := w.viewSource(t)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&script, "CREATE OR REPLACE VIEW %s AS %s;\n", ident(t.Table), src)
	}
	for i, t := range tables {
		fmt.Fprintf(&script, "SELECT %d AS t, column_name, column_type, \"null\" AS nullable FROM (DESCRIBE %s);\n", i, ident(t.Table))
		fmt.Fprintf(&script, "SELECT %d AS t, count(*) AS n FROM %s;\n", i, ident(t.Table))
	}
	res, err := eng.Run(ctx, w.session(Request{DB: rep.Catalog, Script: script.String()}))
	if err != nil {
		return nil, err
	}

	cols := map[int][]Column{}
	rows := map[int]int64{}
	for _, set := range res.Results {
		for _, r := range set {
			i := int(num(r["t"]))
			if _, ok := r["column_name"]; ok {
				cols[i] = append(cols[i], Column{
					Name: str(r["column_name"]), Type: DuckToArrow(str(r["column_type"])),
					Nullable: str(r["nullable"]) != "NO",
				})
			} else {
				rows[i] = num(r["n"])
			}
		}
	}

	for i, t := range tables {
		st := TableStatus{Table: t.Table, Rows: rows[i], Columns: len(cols[i])}
		if t.SchemaMode == SchemaDeclared && len(t.Schema) > 0 {
			if d := schemaDiff(t.Schema, cols[i]); len(d) > 0 {
				st.Error = "the files do not match the declared schema: " + strings.Join(d, "; ")
			}
		} else if len(t.Schema) > 0 {
			st.Drift = schemaDiff(t.Schema, cols[i])
		}
		if pin && st.Error == "" {
			t.Schema = cols[i]
			if err := w.SaveTable(t); err != nil {
				return nil, err
			}
			st.Drift = nil
			w.Mark(t.Table, StepPinned)
		}
		st.Pinned = len(t.Schema) > 0
		_, st.Snapshot = w.Config.Snapshots[t.Table]
		if st.Error == "" {
			w.Mark(t.Table, StepRegistered)
		}
		st.Steps = w.Config.Progress[t.Table]
		rep.Tables = append(rep.Tables, st)
	}
	if err := w.Save(); err != nil {
		return nil, err
	}
	for _, st := range rep.Tables {
		if st.Error != "" {
			return rep, nserr.New(nserr.Fail, "table %s: %s", st.Table, st.Error)
		}
	}
	return rep, nil
}

// SnapshotTable copies a table to Parquet and points its view there.
func (w *Workspace) SnapshotTable(ctx context.Context, eng Engine, name string) (*TableStatus, error) {
	t, err := w.Table(name)
	if err != nil {
		return nil, err
	}
	hashes, err := w.sourceHashes(t)
	if err != nil {
		return nil, err
	}
	// Read from the files, not an earlier snapshot or landing.
	src, err := w.fileSource(t)
	if err != nil {
		return nil, err
	}
	dest := w.Path(SnapshotDir, name+".parquet")
	script := fmt.Sprintf("COPY (%s) TO %s (FORMAT parquet);\nSELECT count(*) AS n FROM read_parquet(%s);\n", src, lit(dest), lit(dest))
	out, err := eng.Run(ctx, w.session(Request{Script: script}))
	if err != nil {
		return nil, err
	}
	if w.Config.Snapshots == nil {
		w.Config.Snapshots = map[string]Snapshot{}
	}
	w.Config.Snapshots[name] = Snapshot{Path: dest, Hashes: hashes}
	if err := w.Save(); err != nil {
		return nil, err
	}
	st := &TableStatus{Table: name, Snapshot: true, Steps: w.Config.Progress[name]}
	if len(out.Results) > 0 && len(out.Results[0]) > 0 {
		st.Rows = num(out.Results[0][0]["n"])
	}
	return st, nil
}

func fileHash(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// StatusReport is what `nsctl data status` prints.
type StatusReport struct {
	Workspace string        `json:"workspace"`
	Root      string        `json:"root"`
	Sources   []Source      `json:"sources"`
	Catalog   string        `json:"catalog,omitempty"`
	Tables    []TableStatus `json:"tables"`
	Next      string        `json:"next"`
	Storage   *Storage      `json:"storage,omitempty"`
	// Export is the repository export --repo last wrote, if any.
	Export string `json:"export,omitempty"`
}

// Status reports progress without running the engine: it reads the
// workspace, re-hashes snapshot sources, and suggests the next command.
// Drift since a pin is reported by register, which reads the files.
func (w *Workspace) Status() (*StatusReport, error) {
	tables, err := w.Tables()
	if err != nil {
		return nil, err
	}
	rep := &StatusReport{Workspace: w.Name, Root: w.Root, Sources: w.Config.Sources}
	if _, err := os.Stat(w.Path(CatalogFile)); err == nil {
		rep.Catalog = w.Path(CatalogFile)
	}
	for _, t := range tables {
		st := TableStatus{Table: t.Table, Pinned: len(t.Schema) > 0, Steps: w.Config.Progress[t.Table]}
		copyOf, isCopy := w.Config.Snapshots[t.Table]
		if isCopy {
			st.Snapshot = true
		}
		// A landed table reads the bucket, so its staleness is the landing's.
		if l, ok := w.Config.Landed[t.Table]; ok {
			st.Landed, copyOf, isCopy = l.Path, l, true
		}
		if isCopy {
			stale, err := w.staleFiles(t, copyOf.Hashes)
			if err != nil {
				return nil, err
			}
			st.Stale = stale
		}
		rep.Tables = append(rep.Tables, st)
	}
	rep.Next = w.next(tables)
	rep.Storage = w.Config.Storage
	if w.Config.Export != nil {
		rep.Export = w.Config.Export.Out
	}
	return rep, nil
}

// staleFiles lists the source files that changed, appeared or went since
// hashes were taken.
func (w *Workspace) staleFiles(t *Table, hashes map[string]string) ([]string, error) {
	files, err := w.tableFiles(t)
	if err != nil {
		return nil, err
	}
	var stale []string
	seen := map[string]bool{}
	for _, f := range files {
		seen[f.Rel] = true
		root, _ := w.SourcePath(f.Source)
		h, err := fileHash(filepath.Join(root, filepath.FromSlash(f.Rel)))
		if err != nil || h != hashes[f.Rel] {
			stale = append(stale, f.Rel)
		}
	}
	for _, f := range sortedKeys(hashes) {
		if !seen[f] {
			stale = append(stale, f+" (gone)")
		}
	}
	return stale, nil
}

// next is the command a person most likely wants now.
func (w *Workspace) next(tables []*Table) string {
	if len(w.Config.Sources) == 0 {
		return "nsctl data scan <folder>"
	}
	if len(tables) == 0 {
		return "nsctl data scan --apply"
	}
	// Profiling is suggested only on the way to a first register; once a
	// table is queryable, skipping its profile was a choice.
	for _, t := range tables {
		if !w.Has(t.Table, StepRegistered) {
			if !w.Has(t.Table, StepProfiled) {
				return "nsctl data profile " + t.Table
			}
			return "nsctl data register"
		}
	}
	return "nsctl data sql \"SELECT * FROM " + tables[0].Table + " LIMIT 10\""
}

// Query runs one statement against the catalog, returning its rows and
// column order. It is read-only unless write.
func (w *Workspace) Query(ctx context.Context, eng Engine, stmt string, write bool) ([]map[string]any, []string, error) {
	if _, err := os.Stat(w.Path(CatalogFile)); err != nil {
		return nil, nil, nserr.New(nserr.Usage, "workspace %s has no catalog yet. Run `nsctl data register` first", w.Name)
	}
	stmt = strings.TrimSpace(stmt)
	if !strings.HasSuffix(stmt, ";") {
		stmt += ";"
	}
	out, err := eng.Run(ctx, w.session(Request{DB: w.Path(CatalogFile), ReadOnly: !write, Script: stmt + "\n"}))
	if err != nil {
		return nil, nil, err
	}
	if len(out.Results) == 0 {
		return []map[string]any{}, nil, nil
	}
	last := len(out.Results) - 1
	var cols []string
	if last < len(out.Columns) {
		cols = out.Columns[last]
	}
	return out.Results[last], cols, nil
}

// SaveQuery keeps a statement in queries/<name>.sql.
func (w *Workspace) SaveQuery(name, stmt string) (string, error) {
	if err := ValidTableName(name); err != nil {
		return "", err
	}
	p := w.Path(QueriesDir, name+".sql")
	return p, writeFile(p, []byte(strings.TrimSpace(stmt)+"\n"))
}

// ViewSQL is the statement register uses for a table's view: plain DuckDB,
// reading the files (or the snapshot) at their absolute paths.
func (w *Workspace) ViewSQL(t *Table) (string, error) {
	src, err := w.viewSource(t)
	if err != nil {
		return "", err
	}
	return "CREATE OR REPLACE VIEW " + ident(t.Table) + " AS " + src + ";", nil
}

// SavedQuery is one queries/<name>.sql.
type SavedQuery struct {
	Name string `json:"name"`
	SQL  string `json:"sql"`
}

// SavedQueries lists the workspace's saved queries by name.
func (w *Workspace) SavedQueries() ([]SavedQuery, error) {
	entries, err := os.ReadDir(w.Path(QueriesDir))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	var out []SavedQuery
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		b, err := os.ReadFile(w.Path(QueriesDir, e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, SavedQuery{Name: strings.TrimSuffix(e.Name(), ".sql"), SQL: strings.TrimSpace(string(b))})
	}
	return out, nil
}

// Ident quotes an SQL identifier for DuckDB.
func Ident(s string) string { return ident(s) }
