package data

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// A scan walks the sources, decides each file's format by its content,
// describes the readable ones with DuckDB, and proposes groups (NERD039
// SPEC003). It writes nothing; Apply does.

// FileInfo is one candidate file: a file, or one sheet of a workbook.
type FileInfo struct {
	Source string `json:"source"`
	// Rel is the slash-separated path relative to the source.
	Rel    string `json:"path"`
	Format string `json:"format"`
	Sheet  string `json:"sheet,omitempty"`
	// Partitions are hive-style key=value directory keys on the path.
	Partitions []string    `json:"partitions,omitempty"`
	Columns    []ColumnRef `json:"columns,omitempty"`
}

// ColumnRef is a column as DuckDB described it.
type ColumnRef struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// Unsupported is a file scan will not read, and why.
type Unsupported struct {
	Source string `json:"source"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Proposal is a proposed group.
type Proposal struct {
	Table      string      `json:"table"`
	Source     string      `json:"source"`
	Paths      []string    `json:"paths"`
	Format     string      `json:"format"`
	Sheet      string      `json:"sheet,omitempty"`
	Files      []string    `json:"files"`
	Partitions []string    `json:"partitions,omitempty"`
	Columns    []ColumnRef `json:"columns"`
}

// ScanReport is what `nsctl data scan` prints.
type ScanReport struct {
	Workspace   string        `json:"workspace"`
	FilesSeen   int           `json:"files_seen"`
	Proposals   []Proposal    `json:"proposals"`
	Unsupported []Unsupported `json:"unsupported,omitempty"`
	// Covered counts files an existing table already reads.
	Covered int `json:"covered"`
	// Missing names existing tables whose paths now match no file.
	Missing []string `json:"missing,omitempty"`
	Applied []string `json:"applied,omitempty"`
}

// dumpExt are formats scan recognises and declines (out of scope, SPEC003).
var dumpExt = map[string]string{
	".sql":     "an SQL dump; database dumps are not supported yet",
	".dump":    "a database dump; not supported yet",
	".bak":     "a database backup; not supported yet",
	".backup":  "a database backup; not supported yet",
	".sqlite":  "a SQLite database; databases are not supported yet",
	".sqlite3": "a SQLite database; databases are not supported yet",
	".db":      "a database file; databases are not supported yet",
	".duckdb":  "a DuckDB database; databases are not supported yet",
	".mdb":     "an Access database; not supported",
	".accdb":   "an Access database; not supported",
}

var textExt = map[string]bool{".csv": true, ".tsv": true, ".txt": true, ".psv": true, ".dat": true, ".tab": true}

// Detect decides a file's format from its first bytes and its name. It
// returns the format, or "" and a reason the file is not read.
func Detect(name string, head []byte) (format, reason string) {
	ext := strings.ToLower(filepath.Ext(name))
	if r, ok := dumpExt[ext]; ok {
		return "", r
	}
	if bytes.HasPrefix(head, []byte("PAR1")) {
		return FormatParquet, ""
	}
	if bytes.HasPrefix(head, []byte("PK\x03\x04")) {
		if ext == ".xlsx" || ext == ".xlsm" {
			return FormatXLSX, ""
		}
		return "", "a zip archive; extract it and scan the folder (reading inside archives is not supported yet)"
	}
	if ext == ".xls" {
		return "", "a legacy .xls workbook; save it as .xlsx or CSV"
	}
	if bytes.IndexByte(head, 0) >= 0 {
		return "", "not a text, Parquet or Excel file"
	}
	body := bytes.TrimLeft(bytes.TrimPrefix(head, []byte("\xef\xbb\xbf")), " \t\r\n")
	if len(body) == 0 {
		return "", "empty"
	}
	if ext == ".json" || ext == ".jsonl" || ext == ".ndjson" || body[0] == '{' || body[0] == '[' {
		if body[0] == '{' || body[0] == '[' {
			return FormatJSON, ""
		}
		return "", "named .json but does not start with { or ["
	}
	if textExt[ext] {
		return FormatCSV, ""
	}
	if looksDelimited(body) {
		return FormatCSV, ""
	}
	return "", "not a recognised data format"
}

// looksDelimited is true when the first lines split into the same number of
// fields (more than one) on one common delimiter.
func looksDelimited(b []byte) bool {
	lines := strings.Split(string(b), "\n")
	if len(lines) > 6 {
		lines = lines[:6]
	}
	if len(lines) < 2 {
		return false
	}
	for _, d := range []string{",", "\t", ";", "|"} {
		n := strings.Count(lines[0], d)
		if n == 0 {
			continue
		}
		ok := true
		for _, l := range lines[1 : len(lines)-1] {
			if strings.TrimSpace(l) != "" && strings.Count(l, d) != n {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// walk lists the candidate files under every source.
func (w *Workspace) walk() (files []FileInfo, unsupported []Unsupported, err error) {
	for _, src := range w.Config.Sources {
		err := filepath.WalkDir(src.Path, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				unsupported = append(unsupported, Unsupported{Source: src.Name, Path: rel(src.Path, p), Reason: err.Error()})
				return nil
			}
			name := d.Name()
			if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "~$") {
				if d.IsDir() && p != src.Path {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() || !d.Type().IsRegular() {
				return nil
			}
			r := rel(src.Path, p)
			head, err := readHead(p, 8192)
			if err != nil {
				unsupported = append(unsupported, Unsupported{Source: src.Name, Path: r, Reason: err.Error()})
				return nil
			}
			format, reason := Detect(name, head)
			if format == "" {
				unsupported = append(unsupported, Unsupported{Source: src.Name, Path: r, Reason: reason})
				return nil
			}
			fi := FileInfo{Source: src.Name, Rel: r, Format: format, Partitions: partitionKeys(r)}
			if format == FormatXLSX {
				sheets, err := xlsxSheets(p)
				if err != nil {
					unsupported = append(unsupported, Unsupported{Source: src.Name, Path: r, Reason: "workbook: " + err.Error()})
					return nil
				}
				for _, s := range sheets {
					f := fi
					f.Sheet = s
					files = append(files, f)
				}
				return nil
			}
			files = append(files, fi)
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
	}
	return files, unsupported, nil
}

func rel(root, p string) string {
	r, err := filepath.Rel(root, p)
	if err != nil {
		return p
	}
	return filepath.ToSlash(r)
}

func readHead(p string, n int) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, n)
	m, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, err
	}
	return buf[:m], nil
}

// partitionKeys are the hive-style key=value directories on a path.
func partitionKeys(rel string) []string {
	parts := strings.Split(path.Dir(rel), "/")
	var keys []string
	for _, p := range parts {
		if k, _, ok := strings.Cut(p, "="); ok && k != "" {
			keys = append(keys, k)
		}
	}
	return keys
}

// xlsxSheets lists a workbook's sheet names, in workbook order.
func xlsxSheets(p string) ([]string, error) {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name != "xl/workbook.xml" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		var wb struct {
			Sheets []struct {
				Name string `xml:"name,attr"`
			} `xml:"sheets>sheet"`
		}
		if err := xml.NewDecoder(io.LimitReader(rc, 8<<20)).Decode(&wb); err != nil {
			return nil, err
		}
		var names []string
		for _, s := range wb.Sheets {
			names = append(names, s.Name)
		}
		if len(names) == 0 {
			return nil, fmt.Errorf("no sheets")
		}
		return names, nil
	}
	return nil, fmt.Errorf("no xl/workbook.xml; not an Excel workbook")
}

// describe asks DuckDB for every candidate file's columns in one session.
// A file DuckDB cannot read becomes Unsupported with DuckDB's reason.
func (w *Workspace) describe(ctx context.Context, eng Engine, files []FileInfo) ([]FileInfo, []Unsupported, error) {
	if len(files) == 0 {
		return nil, nil, nil
	}
	var script strings.Builder
	for i, f := range files {
		root, _ := w.SourcePath(f.Source)
		t := &Table{Table: "f", Format: f.Format, Sheet: f.Sheet, ReaderOptions: map[string]any{}}
		reader, err := readerSQL(t, []string{filepath.Join(root, filepath.FromSlash(f.Rel))})
		if err != nil {
			return nil, nil, err
		}
		fmt.Fprintf(&script, "SELECT %d AS f, column_name, column_type FROM (DESCRIBE SELECT * FROM %s);\n", i, reader)
	}
	out, err := eng.Run(ctx, Request{Script: script.String(), Mounts: w.Mounts(), ContinueOnError: true})
	if err != nil {
		return nil, nil, err
	}
	for _, rows := range out.Results {
		for _, r := range rows {
			i := int(num(r["f"]))
			if i >= 0 && i < len(files) {
				files[i].Columns = append(files[i].Columns, ColumnRef{Name: str(r["column_name"]), Type: str(r["column_type"])})
			}
		}
	}
	var ok []FileInfo
	var bad []Unsupported
	for _, f := range files {
		if len(f.Columns) == 0 {
			bad = append(bad, Unsupported{Source: f.Source, Path: f.Rel, Reason: unreadableReason(out.Stderr, f)})
			continue
		}
		ok = append(ok, f)
	}
	return ok, bad, nil
}

// unreadableReason picks DuckDB's message about this file out of a
// ContinueOnError session's stderr, or says it could not be read.
func unreadableReason(stderr string, f FileInfo) string {
	for _, block := range strings.Split(stderr, "\n\n") {
		if strings.Contains(block, f.Rel) {
			first := strings.SplitN(strings.TrimSpace(block), "\n", 2)[0]
			return "DuckDB could not read it: " + first
		}
	}
	return "DuckDB could not read it"
}

// GroupFiles proposes groups. It is pure, so the rule is testable: files
// group when they share a source, a format and a directory pattern (hive
// key=value levels generalised to key=*), and their column names overlap by
// at least half. A workbook sheet is always its own group. Groups of several
// files get a glob when the glob would read exactly those files, and an
// explicit list otherwise.
func GroupFiles(files []FileInfo, taken func(string) bool) []Proposal {
	type cluster struct {
		key   string
		dir   string
		ext   string
		names map[string]bool
		files []FileInfo
	}
	var clusters []*cluster
	for _, f := range files {
		dir := patternDir(f.Rel)
		ext := strings.ToLower(path.Ext(f.Rel))
		key := f.Source + "|" + f.Format + "|" + dir + "|" + ext
		if f.Format == FormatXLSX {
			key += "|" + f.Rel + "|" + f.Sheet
		}
		names := columnNames(f)
		var hit *cluster
		for _, c := range clusters {
			if c.key == key && jaccard(c.names, names) >= 0.5 {
				hit = c
				break
			}
		}
		if hit == nil {
			hit = &cluster{key: key, dir: dir, ext: ext, names: map[string]bool{}}
			clusters = append(clusters, hit)
		}
		for n := range names {
			hit.names[n] = true
		}
		hit.files = append(hit.files, f)
	}

	used := map[string]bool{}
	isTaken := func(n string) bool { return used[n] || (taken != nil && taken(n)) }
	var out []Proposal
	for _, c := range clusters {
		first := c.files[0]
		p := Proposal{Source: first.Source, Format: first.Format, Sheet: first.Sheet, Partitions: first.Partitions}
		for _, f := range c.files {
			p.Files = append(p.Files, f.Rel)
		}
		sort.Strings(p.Files)
		p.Paths = groupPaths(c.dir, c.ext, p.Files, first, files)
		p.Columns = unionColumns(c.files)
		p.Table = uniqueName(tableName(c.dir, first, len(c.files)), isTaken)
		used[p.Table] = true
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Table < out[j].Table })
	return out
}

// patternDir is a file's directory with key=value levels made key=*.
func patternDir(rel string) string {
	d := path.Dir(rel)
	if d == "." {
		return ""
	}
	parts := strings.Split(d, "/")
	for i, p := range parts {
		if k, _, ok := strings.Cut(p, "="); ok && k != "" {
			parts[i] = k + "=*"
		}
	}
	return strings.Join(parts, "/")
}

func groupPaths(dir, ext string, groupFiles []string, first FileInfo, all []FileInfo) []string {
	if len(groupFiles) == 1 {
		return groupFiles
	}
	glob := "*" + ext
	if dir != "" {
		glob = dir + "/" + glob
	}
	in := map[string]bool{}
	for _, f := range groupFiles {
		in[f] = true
	}
	for _, f := range all {
		if f.Source != first.Source {
			continue
		}
		if m, _ := path.Match(glob, f.Rel); m && !in[f.Rel] {
			return groupFiles
		}
	}
	for _, f := range groupFiles {
		if m, _ := path.Match(glob, f); !m {
			return groupFiles
		}
	}
	return []string{glob}
}

// tableName names a group: a workbook sheet after the workbook and sheet, a
// single file after the file, and several files after the folder that holds
// them (skipping key=value partition levels).
func tableName(dir string, f FileInfo, files int) string {
	stem := strings.TrimSuffix(path.Base(f.Rel), path.Ext(f.Rel))
	if f.Format == FormatXLSX {
		return snake(stem + "_" + f.Sheet)
	}
	if files == 1 && len(f.Partitions) == 0 {
		return snake(stem)
	}
	parts := strings.Split(dir, "/")
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] != "" && !strings.HasSuffix(parts[i], "=*") {
			return snake(parts[i])
		}
	}
	return snake(stem)
}

func columnNames(f FileInfo) map[string]bool {
	part := map[string]bool{}
	for _, k := range f.Partitions {
		part[strings.ToLower(k)] = true
	}
	m := map[string]bool{}
	for _, c := range f.Columns {
		n := strings.ToLower(c.Name)
		if !part[n] {
			m[n] = true
		}
	}
	return m
}

func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1
	}
	inter := 0
	for k := range a {
		if b[k] {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	return float64(inter) / float64(union)
}

// unionColumns is every column the group's files have, in first-seen order,
// with the first type seen. Type differences are drift, which profile reports.
func unionColumns(files []FileInfo) []ColumnRef {
	seen := map[string]bool{}
	var out []ColumnRef
	for _, f := range files {
		for _, c := range f.Columns {
			k := strings.ToLower(c.Name)
			if !seen[k] {
				seen[k] = true
				out = append(out, c)
			}
		}
	}
	return out
}

// covered reports whether an existing table already reads a file.
func covered(tables []*Table, f FileInfo) bool {
	for _, t := range tables {
		if t.Source != f.Source || t.Format != f.Format {
			continue
		}
		if f.Format == FormatXLSX && t.Sheet != f.Sheet {
			continue
		}
		for _, p := range t.Paths {
			if m, _ := path.Match(p, f.Rel); m {
				return true
			}
		}
	}
	return false
}

// ScanOptions are the flags of `nsctl data scan`.
type ScanOptions struct {
	// Add are paths to add as sources before scanning.
	Add []string
	// Apply writes the proposals as tables.
	Apply bool
	// Rename maps a proposed table name to the one to use.
	Rename map[string]string
}

// Scan runs a scan.
func (w *Workspace) Scan(ctx context.Context, eng Engine, opts ScanOptions) (*ScanReport, error) {
	for _, p := range opts.Add {
		if _, err := w.AddSource(p); err != nil {
			return nil, err
		}
	}
	if len(w.Config.Sources) == 0 {
		return nil, usageNoSources(w)
	}
	existing, err := w.Tables()
	if err != nil {
		return nil, err
	}
	all, unsupported, err := w.walk()
	if err != nil {
		return nil, err
	}
	distinct := map[string]bool{}
	for _, f := range all {
		distinct[f.Source+"/"+f.Rel] = true
	}
	rep := &ScanReport{Workspace: w.Name, FilesSeen: len(distinct) + len(unsupported)}

	var fresh []FileInfo
	for _, f := range all {
		if covered(existing, f) {
			rep.Covered++
		} else {
			fresh = append(fresh, f)
		}
	}
	for _, t := range existing {
		matched := false
		for _, f := range all {
			if covered([]*Table{t}, f) {
				matched = true
				break
			}
		}
		if !matched {
			rep.Missing = append(rep.Missing, t.Table)
		}
	}

	described, bad, err := w.describe(ctx, eng, fresh)
	if err != nil {
		return nil, err
	}
	rep.Unsupported = append(unsupported, bad...)
	names := map[string]bool{}
	for _, t := range existing {
		names[t.Table] = true
	}
	rep.Proposals = GroupFiles(described, func(n string) bool { return names[n] })
	for i := range rep.Proposals {
		if to, ok := opts.Rename[rep.Proposals[i].Table]; ok {
			if err := ValidTableName(to); err != nil {
				return nil, err
			}
			rep.Proposals[i].Table = to
		}
	}

	if opts.Apply {
		for _, p := range rep.Proposals {
			if names[p.Table] {
				return nil, usageTableExists(p.Table)
			}
			names[p.Table] = true
			t := &Table{
				Table: p.Table, Source: p.Source, Paths: p.Paths, Format: p.Format, Sheet: p.Sheet,
				SchemaMode: SchemaInfer,
			}
			if len(p.Partitions) > 0 {
				t.ReaderOptions = map[string]any{"hive_partitioning": true}
			}
			if err := w.SaveTable(t); err != nil {
				return nil, err
			}
			w.Mark(p.Table, StepGrouped)
			rep.Applied = append(rep.Applied, p.Table)
		}
	}
	if err := w.Save(); err != nil {
		return nil, err
	}
	return rep, nil
}
