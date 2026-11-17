package data

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// A profile reports what a person needs to decide about a table (NERD039
// SPEC004). It is evidence, never a decision: issues carry a suggested fix,
// and only a person (or a skill acting on their answer) applies it.

// sampleRows bounds the rows statistics are computed over unless --full.
const sampleRows = 100000

// ColumnProfile is one column's statistics.
type ColumnProfile struct {
	Name         string   `json:"name"`
	Type         string   `json:"type"`
	DuckType     string   `json:"duckdb_type"`
	NullFraction float64  `json:"null_fraction"`
	Distinct     int64    `json:"distinct_approx"`
	Min          string   `json:"min,omitempty"`
	Max          string   `json:"max,omitempty"`
	Sample       []string `json:"sample,omitempty"`
	PII          bool     `json:"pii,omitempty"`
	CandidateKey bool     `json:"candidate_key,omitempty"`
}

// Issue is something worth a decision, with the change that would settle it.
type Issue struct {
	Column  string `json:"column,omitempty"`
	Kind    string `json:"kind"`
	Message string `json:"message"`
	// Suggest is a fragment of the table definition: {"casts": {...}} or
	// {"reader_options": {...}}.
	Suggest map[string]any `json:"suggest,omitempty"`
}

// Issue kinds.
const (
	IssueTextAsDate   = "text_as_date"
	IssueTextAsNumber = "text_as_number"
	IssueLeadingZeros = "leading_zeros"
	IssueHeaderJunk   = "header_rows_skipped"
	IssueNoHeader     = "no_header"
	IssuePII          = "personal_data"
	IssueDrift        = "schema_drift"
)

// Drift is a column that differs across a table's files.
type Drift struct {
	Column string            `json:"column"`
	Kind   string            `json:"kind"` // missing | type
	Files  map[string]string `json:"files"`
}

// Profile is profile/<table>.json.
type Profile struct {
	Table      string          `json:"table"`
	ProfiledAt string          `json:"profiled_at"`
	Rows       int64           `json:"rows"`
	Sampled    bool            `json:"sampled"`
	Files      int             `json:"files"`
	Columns    []ColumnProfile `json:"columns"`
	Issues     []Issue         `json:"issues"`
	Drift      []Drift         `json:"drift,omitempty"`
}

// textStats are counts over a column read as text, for the issue rules.
type textStats struct {
	NonEmpty, LeadingZero, AsDate, AsNumber, AsInteger, Email, Phone int64
}

var piiNameRE = regexp.MustCompile(`(^|_)(e_?mail|phone|mobile|cell|ssn|social_security|national_id|nino|passport|dob|date_of_birth|birth_?date|first_?name|last_?name|full_?name|surname|given_name|street|address|post_?code|zip_?code|ip_address)(_|$)`)

// analyze applies the issue rules. It is pure.
func analyze(cols []ColumnProfile, text map[string]textStats) []Issue {
	var issues []Issue
	for i := range cols {
		c := &cols[i]
		ts, ok := text[c.Name]
		if ok && ts.NonEmpty > 0 {
			frac := func(n int64) float64 { return float64(n) / float64(ts.NonEmpty) }
			if c.DuckType == "VARCHAR" && frac(ts.AsDate) >= 0.95 && ts.LeadingZero == 0 {
				issues = append(issues, Issue{Column: c.Name, Kind: IssueTextAsDate,
					Message: fmt.Sprintf("%s is read as text, but %.0f%% of its values are dates", c.Name, 100*frac(ts.AsDate)),
					Suggest: map[string]any{"casts": map[string]any{c.Name: "date32"}}})
			} else if c.DuckType == "VARCHAR" && frac(ts.AsNumber) >= 0.95 && ts.LeadingZero == 0 {
				to := "float64"
				if ts.AsInteger == ts.AsNumber {
					to = "int64"
				}
				issues = append(issues, Issue{Column: c.Name, Kind: IssueTextAsNumber,
					Message: fmt.Sprintf("%s is read as text, but %.0f%% of its values are numbers", c.Name, 100*frac(ts.AsNumber)),
					Suggest: map[string]any{"casts": map[string]any{c.Name: to}}})
			}
			if ts.LeadingZero > 0 && c.DuckType != "VARCHAR" {
				issues = append(issues, Issue{Column: c.Name, Kind: IssueLeadingZeros,
					Message: fmt.Sprintf("%s is read as a number, which drops the leading zeros in %d values (e.g. identifiers like 0042)", c.Name, ts.LeadingZero),
					Suggest: map[string]any{"reader_options": map[string]any{"column_types": map[string]any{c.Name: "utf8"}}}})
			}
			// A date matches the phone pattern's digits-and-dashes, so a column
			// that is mostly dates is never a phone number.
			if frac(ts.Email) >= 0.5 || (c.DuckType == "VARCHAR" && frac(ts.Phone) >= 0.8 && frac(ts.AsDate) < 0.5) {
				c.PII = true
			}
		}
		if piiNameRE.MatchString(strings.ToLower(c.Name)) {
			c.PII = true
		}
		if c.PII {
			issues = append(issues, Issue{Column: c.Name, Kind: IssuePII,
				Message: fmt.Sprintf("%s looks like personal data; its samples are redacted", c.Name)})
		}
	}
	return issues
}

// sniff is sniff_csv's verdict on a table's first file.
type sniff struct {
	HasHeader bool
	SkipRows  int64
}

// A reader option the table already sets is a decision made, not an issue.
func sniffIssues(s *sniff, opts map[string]any) []Issue {
	if s == nil {
		return nil
	}
	_, skipSet := opts["skip"]
	_, headerSet := opts["header"]
	var out []Issue
	if s.SkipRows > 0 && !skipSet {
		out = append(out, Issue{Kind: IssueHeaderJunk,
			Message: fmt.Sprintf("the first %d line(s) are not data (a title or notes above the header)", s.SkipRows),
			Suggest: map[string]any{"reader_options": map[string]any{"skip": s.SkipRows}}})
	}
	if !s.HasHeader && !headerSet {
		out = append(out, Issue{Kind: IssueNoHeader,
			Message: "the file has no header row; columns are named column0, column1, ...",
			Suggest: map[string]any{"reader_options": map[string]any{"header": false}}})
	}
	return out
}

// driftOf compares per-file column types. It is pure.
func driftOf(files []FileInfo) []Drift {
	if len(files) < 2 {
		return nil
	}
	types := map[string]map[string]string{} // column -> file -> type
	var order []string
	for _, f := range files {
		for _, c := range f.Columns {
			k := c.Name
			if types[k] == nil {
				types[k] = map[string]string{}
				order = append(order, k)
			}
			types[k][f.Rel] = c.Type
		}
	}
	var out []Drift
	for _, col := range order {
		byFile := types[col]
		if len(byFile) < len(files) {
			d := Drift{Column: col, Kind: "missing", Files: map[string]string{}}
			for _, f := range files {
				if t, ok := byFile[f.Rel]; ok {
					d.Files[f.Rel] = t
				} else {
					d.Files[f.Rel] = "(absent)"
				}
			}
			out = append(out, d)
			continue
		}
		distinct := map[string]bool{}
		for _, t := range byFile {
			distinct[t] = true
		}
		if len(distinct) > 1 {
			out = append(out, Drift{Column: col, Kind: "type", Files: byFile})
		}
	}
	return out
}

// tableFiles are the scanned files a table reads.
func (w *Workspace) tableFiles(t *Table) ([]FileInfo, error) {
	all, _, err := w.walk()
	if err != nil {
		return nil, err
	}
	var out []FileInfo
	for _, f := range all {
		if covered([]*Table{t}, f) {
			out = append(out, f)
		}
	}
	return out, nil
}

// ProfileTable profiles one table and saves the result.
func (w *Workspace) ProfileTable(ctx context.Context, eng Engine, name string, full bool) (*Profile, error) {
	t, err := w.Table(name)
	if err != nil {
		return nil, err
	}
	abs, err := w.AbsPaths(t)
	if err != nil {
		return nil, err
	}
	reader, err := readerSQL(t, abs)
	if err != nil {
		return nil, err
	}
	sel, err := selectSQL(t, reader)
	if err != nil {
		return nil, err
	}
	sampled := "(" + sel + ")"
	if !full {
		sampled = fmt.Sprintf("(SELECT * FROM (%s) USING SAMPLE %d ROWS)", sel, sampleRows)
	}

	// Session one: row count and per-column summary.
	script := fmt.Sprintf("SELECT count(*) AS n FROM (%s);\n", sel) +
		fmt.Sprintf("SELECT column_name, column_type, min, max, approx_unique, null_percentage FROM (SUMMARIZE SELECT * FROM %s);\n", sampled) +
		fmt.Sprintf("SELECT * FROM (%s) USING SAMPLE reservoir(5 ROWS) REPEATABLE (42);\n", sel)
	out, err := eng.Run(ctx, Request{Script: script, Mounts: w.Mounts()})
	if err != nil {
		return nil, err
	}
	if len(out.Results) < 2 {
		return nil, fmt.Errorf("profile %s: DuckDB returned %d result sets, want 3", name, len(out.Results))
	}
	p := &Profile{Table: name, ProfiledAt: time.Now().UTC().Format(time.RFC3339), Sampled: !full}
	if len(out.Results[0]) > 0 {
		p.Rows = num(out.Results[0][0]["n"])
	}
	for _, r := range out.Results[1] {
		nullPct, _ := strconv.ParseFloat(str(r["null_percentage"]), 64)
		p.Columns = append(p.Columns, ColumnProfile{
			Name: str(r["column_name"]), DuckType: str(r["column_type"]), Type: DuckToArrow(str(r["column_type"])),
			Min: str(r["min"]), Max: str(r["max"]), Distinct: num(r["approx_unique"]), NullFraction: nullPct / 100,
		})
	}
	if len(out.Results) > 2 {
		for _, row := range out.Results[2] {
			for i := range p.Columns {
				p.Columns[i].Sample = append(p.Columns[i].Sample, str(row[p.Columns[i].Name]))
			}
		}
	}

	// Session two: text checks over the raw values, exact key checks, and
	// the CSV sniffer's verdict on the first file.
	text, keys, sn, err := w.textChecks(ctx, eng, t, abs, p, full)
	if err != nil {
		return nil, err
	}
	for i := range p.Columns {
		p.Columns[i].CandidateKey = keys[p.Columns[i].Name]
	}
	// Always a list, so a clean profile reads as no issues, not a missing key.
	p.Issues = append(append([]Issue{}, sniffIssues(sn, t.ReaderOptions)...), analyze(p.Columns, text)...)
	for i := range p.Columns {
		if p.Columns[i].PII {
			for j := range p.Columns[i].Sample {
				p.Columns[i].Sample[j] = "(redacted)"
			}
		}
	}

	// Drift: describe each file on its own.
	files, err := w.tableFiles(t)
	if err != nil {
		return nil, err
	}
	p.Files = len(files)
	if len(files) > 1 {
		described, _, err := w.describe(ctx, eng, files)
		if err != nil {
			return nil, err
		}
		p.Drift = driftOf(described)
		for _, d := range p.Drift {
			p.Issues = append(p.Issues, Issue{Column: d.Column, Kind: IssueDrift,
				Message: fmt.Sprintf("%s differs across files (%s)", d.Column, d.Kind)})
		}
	}

	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := writeFile(w.Path(ProfileDir, name+".json"), b); err != nil {
		return nil, err
	}
	w.Mark(name, StepProfiled)
	if err := w.Save(); err != nil {
		return nil, err
	}
	return p, nil
}

func (w *Workspace) textChecks(ctx context.Context, eng Engine, t *Table, abs []string, p *Profile, full bool) (map[string]textStats, map[string]bool, *sniff, error) {
	// For CSV the values are read as text, so a leading zero the typed reader
	// would drop is still visible.
	raw := *t
	raw.Casts = nil
	raw.ReaderOptions = map[string]any{}
	for k, v := range t.ReaderOptions {
		if k != "column_types" {
			raw.ReaderOptions[k] = v
		}
	}
	rawReader, err := readerSQL(&raw, abs)
	if err != nil {
		return nil, nil, nil, err
	}
	if t.Format == FormatCSV {
		rawReader = strings.TrimSuffix(rawReader, ")") + ", all_varchar=true)"
	}
	src := "(" + "SELECT * FROM " + rawReader + ")"
	if !full {
		src = fmt.Sprintf("(SELECT * FROM %s USING SAMPLE %d ROWS)", rawReader, sampleRows)
	}
	var sel []string
	for i, c := range p.Columns {
		v := fmt.Sprintf("NULLIF(TRIM(CAST(%s AS VARCHAR)), '')", ident(c.Name))
		sel = append(sel,
			fmt.Sprintf("count(%s) AS nn_%d", v, i),
			fmt.Sprintf("count(*) FILTER (WHERE regexp_full_match(%s, '0[0-9]+')) AS lz_%d", v, i),
			fmt.Sprintf("count(TRY_CAST(%s AS DATE)) AS dt_%d", v, i),
			fmt.Sprintf("count(TRY_CAST(%s AS DOUBLE)) AS nm_%d", v, i),
			fmt.Sprintf("count(TRY_CAST(%s AS BIGINT)) AS it_%d", v, i),
			fmt.Sprintf(`count(*) FILTER (WHERE regexp_full_match(%s, '[^@\s]+@[^@\s]+\.[^@\s]+')) AS em_%d`, v, i),
			fmt.Sprintf(`count(*) FILTER (WHERE regexp_full_match(%s, '\+?[0-9][0-9 ().-]{6,}[0-9]') AND length(regexp_replace(%s, '[^0-9]', '', 'g')) >= 7) AS ph_%d`, v, v, i),
		)
	}
	script := ""
	if len(sel) > 0 {
		script += "SELECT " + strings.Join(sel, ", ") + " FROM " + src + ";\n"
	}
	// Exact uniqueness, only for columns the approximate count says might be
	// keys: no nulls and distinct close to the row count.
	// approx_unique undercounts small columns, so the pre-filter is loose;
	// the exact count decides. Dates, times and floats are not proposed: in
	// a small table they are unique by accident, not by design.
	var keyCols []int
	for i, c := range p.Columns {
		if c.NullFraction == 0 && p.Rows > 0 && keyableType(c.DuckType) &&
			float64(c.Distinct) >= 0.7*float64(min64(p.Rows, sampleRows)) {
			keyCols = append(keyCols, i)
		}
	}
	reader, _ := readerSQL(t, abs)
	selAll, err := selectSQL(t, reader)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(keyCols) > 0 {
		var ks []string
		for _, i := range keyCols {
			ks = append(ks, fmt.Sprintf("count(DISTINCT %s) = count(*) AND count(%s) = count(*) AS k_%d", ident(p.Columns[i].Name), ident(p.Columns[i].Name), i))
		}
		script += "SELECT " + strings.Join(ks, ", ") + " FROM (" + selAll + ");\n"
	}
	if t.Format == FormatCSV && len(abs) > 0 {
		first := abs[0]
		if files, err := w.tableFiles(t); err == nil && len(files) > 0 {
			root, _ := w.SourcePath(files[0].Source)
			first = root + "/" + files[0].Rel
		}
		script += fmt.Sprintf("SELECT HasHeader, SkipRows FROM sniff_csv(%s);\n", lit(first))
	}
	if script == "" {
		return nil, nil, nil, nil
	}
	out, err := eng.Run(ctx, Request{Script: script, Mounts: w.Mounts()})
	if err != nil {
		return nil, nil, nil, err
	}
	res := out.Results
	text := map[string]textStats{}
	keys := map[string]bool{}
	var sn *sniff
	idx := 0
	if len(sel) > 0 && idx < len(res) && len(res[idx]) > 0 {
		r := res[idx][0]
		for i, c := range p.Columns {
			text[c.Name] = textStats{
				NonEmpty: num(r[fmt.Sprintf("nn_%d", i)]), LeadingZero: num(r[fmt.Sprintf("lz_%d", i)]),
				AsDate: num(r[fmt.Sprintf("dt_%d", i)]), AsNumber: num(r[fmt.Sprintf("nm_%d", i)]),
				AsInteger: num(r[fmt.Sprintf("it_%d", i)]), Email: num(r[fmt.Sprintf("em_%d", i)]),
				Phone: num(r[fmt.Sprintf("ph_%d", i)]),
			}
		}
		idx++
	}
	if len(keyCols) > 0 && idx < len(res) && len(res[idx]) > 0 {
		r := res[idx][0]
		for _, i := range keyCols {
			if b, ok := r[fmt.Sprintf("k_%d", i)].(bool); ok && b {
				keys[p.Columns[i].Name] = true
			}
		}
		idx++
	}
	if t.Format == FormatCSV && idx < len(res) && len(res[idx]) > 0 {
		r := res[idx][0]
		hh, _ := r["HasHeader"].(bool)
		sn = &sniff{HasHeader: hh, SkipRows: num(r["SkipRows"])}
	}
	return text, keys, sn, nil
}

// keyableType is true for the column types a key is made of.
func keyableType(duck string) bool {
	switch duck {
	case "VARCHAR", "UUID", "TINYINT", "SMALLINT", "INTEGER", "BIGINT", "HUGEINT",
		"UTINYINT", "USMALLINT", "UINTEGER", "UBIGINT":
		return true
	}
	return false
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// sortedKeys is a map's keys, sorted.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
