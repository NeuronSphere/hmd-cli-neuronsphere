package data

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// Formats a group can have. xlsx is local only: Librarian NERD012 has no such
// format, so landing always writes Parquet (NERD039 SPEC003).
const (
	FormatCSV     = "csv"
	FormatParquet = "parquet"
	FormatJSON    = "json"
	FormatXLSX    = "xlsx"
)

// Schema modes, as Librarian NERD012 names them.
const (
	SchemaInfer    = "infer"
	SchemaDeclared = "declared"
)

// Column is one schema entry, in NERD012's shape. Type is an Apache Arrow
// logical type name.
type Column struct {
	Name        string `yaml:"name" json:"name"`
	Type        string `yaml:"type" json:"type"`
	Nullable    bool   `yaml:"nullable" json:"nullable"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
}

// Table is tables/<table>.table.yaml: one group (SPEC003).
type Table struct {
	Table  string   `yaml:"table" json:"table"`
	Source string   `yaml:"source" json:"source"`
	Paths  []string `yaml:"paths" json:"paths"`
	Format string   `yaml:"format" json:"format"`
	// Sheet is the workbook sheet, for xlsx.
	Sheet string `yaml:"sheet,omitempty" json:"sheet,omitempty"`
	// ReaderOptions are engine-neutral (NERD012): delimiter, header, skip,
	// quote, hive_partitioning, json_format, column_types.
	ReaderOptions map[string]any    `yaml:"reader_options,omitempty" json:"reader_options,omitempty"`
	SchemaMode    string            `yaml:"schema_mode" json:"schema_mode"`
	Schema        []Column          `yaml:"schema,omitempty" json:"schema,omitempty"`
	Casts         map[string]string `yaml:"casts,omitempty" json:"casts,omitempty"`
}

// TablePath is a table's definition file.
func (w *Workspace) TablePath(name string) string {
	return w.Path(TablesDir, name+".table.yaml")
}

// Tables reads every table definition, sorted by name.
func (w *Workspace) Tables() ([]*Table, error) {
	entries, err := os.ReadDir(w.Path(TablesDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, nserr.Wrap(nserr.Fail, err)
	}
	var out []*Table
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".table.yaml") {
			continue
		}
		t, err := w.Table(strings.TrimSuffix(e.Name(), ".table.yaml"))
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Table < out[j].Table })
	return out, nil
}

// Table reads one table definition.
func (w *Workspace) Table(name string) (*Table, error) {
	b, err := os.ReadFile(w.TablePath(name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nserr.New(nserr.Usage, "no table %q in workspace %s. `nsctl data scan --apply` creates tables", name, w.Name)
	}
	if err != nil {
		return nil, nserr.Wrap(nserr.Fail, err)
	}
	var t Table
	if err := yaml.Unmarshal(b, &t); err != nil {
		return nil, nserr.New(nserr.Fail, "%s: %v", w.TablePath(name), err)
	}
	if t.SchemaMode == "" {
		t.SchemaMode = SchemaInfer
	}
	return &t, nil
}

// SaveTable writes a table definition.
func (w *Workspace) SaveTable(t *Table) error {
	b, err := yaml.Marshal(t)
	if err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	return writeFile(w.TablePath(t.Table), b)
}

// AbsPaths are a table's path patterns made absolute against its source.
func (w *Workspace) AbsPaths(t *Table) ([]string, error) {
	root, ok := w.SourcePath(t.Source)
	if !ok {
		return nil, nserr.New(nserr.Fail, "table %s names source %q, which the workspace does not have", t.Table, t.Source)
	}
	out := make([]string, len(t.Paths))
	for i, p := range t.Paths {
		out[i] = filepath.Join(root, filepath.FromSlash(p))
	}
	return out, nil
}

// readerSQL is the table function that reads a table's files, with its
// reader options. Every value is quoted here, never by a caller.
func readerSQL(t *Table, abs []string) (string, error) {
	list := make([]string, len(abs))
	for i, p := range abs {
		list[i] = lit(p)
	}
	files := "[" + strings.Join(list, ", ") + "]"
	opts := t.ReaderOptions
	var args []string
	boolOpt := func(key, duck string) {
		if v, ok := opts[key]; ok {
			args = append(args, fmt.Sprintf("%s=%t", duck, truthy(v)))
		}
	}
	switch t.Format {
	case FormatCSV:
		if v, ok := opts["delimiter"]; ok {
			args = append(args, "delim="+lit(fmt.Sprint(v)))
		}
		if v, ok := opts["quote"]; ok {
			args = append(args, "quote="+lit(fmt.Sprint(v)))
		}
		boolOpt("header", "header")
		if v, ok := opts["skip"]; ok {
			args = append(args, fmt.Sprintf("skip=%d", num(fmt.Sprint(v))))
		}
		if v, ok := opts["encoding"]; ok {
			args = append(args, "encoding="+lit(fmt.Sprint(v)))
		}
		if types, err := columnTypesArg(opts["column_types"]); err != nil {
			return "", fmt.Errorf("table %s: %w", t.Table, err)
		} else if types != "" {
			args = append(args, "types="+types)
		}
		boolOpt("hive_partitioning", "hive_partitioning")
		args = append(args, "union_by_name=true", "filename=false")
		return "read_csv(" + files + joinArgs(args) + ")", nil
	case FormatParquet:
		boolOpt("hive_partitioning", "hive_partitioning")
		args = append(args, "union_by_name=true")
		return "read_parquet(" + files + joinArgs(args) + ")", nil
	case FormatJSON:
		if v, ok := opts["json_format"]; ok {
			f := fmt.Sprint(v)
			if f == "newline_delimited" || f == "array" {
				args = append(args, "format="+lit(f))
			}
		}
		boolOpt("hive_partitioning", "hive_partitioning")
		args = append(args, "union_by_name=true")
		return "read_json(" + files + joinArgs(args) + ")", nil
	case FormatXLSX:
		if len(abs) != 1 {
			return "", fmt.Errorf("table %s: an xlsx table reads one workbook, not %d", t.Table, len(abs))
		}
		if t.Sheet != "" {
			args = append(args, "sheet="+lit(t.Sheet))
		}
		boolOpt("header", "header")
		args = append(args, "all_varchar=false")
		return "read_xlsx(" + lit(abs[0]) + joinArgs(args) + ")", nil
	}
	return "", fmt.Errorf("table %s: format %q is not supported", t.Table, t.Format)
}

// selectSQL reads a table with its casts applied.
func selectSQL(t *Table, reader string) (string, error) {
	if len(t.Casts) == 0 {
		return "SELECT * FROM " + reader, nil
	}
	cols := make([]string, 0, len(t.Casts))
	for c := range t.Casts {
		cols = append(cols, c)
	}
	sort.Strings(cols)
	var repl []string
	for _, c := range cols {
		duck, err := ArrowToDuck(t.Casts[c])
		if err != nil {
			return "", fmt.Errorf("table %s, cast of %s: %w", t.Table, c, err)
		}
		repl = append(repl, fmt.Sprintf("TRY_CAST(%s AS %s) AS %s", ident(c), duck, ident(c)))
	}
	return "SELECT * REPLACE (" + strings.Join(repl, ", ") + ") FROM " + reader, nil
}

// columnTypesArg turns reader_options.column_types ({col: arrow type}) into
// DuckDB's types={...}. It is how a leading-zero identifier stays text.
func columnTypesArg(v any) (string, error) {
	m, ok := v.(map[string]any)
	if !ok || len(m) == 0 {
		return "", nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		duck, err := ArrowToDuck(fmt.Sprint(m[k]))
		if err != nil {
			return "", fmt.Errorf("column_types.%s: %w", k, err)
		}
		parts = append(parts, lit(k)+": "+lit(duck))
	}
	return "{" + strings.Join(parts, ", ") + "}", nil
}

func joinArgs(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return ", " + strings.Join(args, ", ")
}

func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x == "true" || x == "yes" || x == "1"
	}
	return false
}

// lit is a SQL string literal.
func lit(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// ident is a quoted SQL identifier.
func ident(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
