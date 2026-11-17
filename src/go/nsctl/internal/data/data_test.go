package data

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Requirements: HMD_CLI_NEURONSPHERE_NERD039_SPEC003
func TestDetectDecidesFormatByContent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, file, head, format string
		refused                  bool
	}{
		{name: "parquet by magic, whatever the name", file: "x.bin", head: "PAR1\x15\x04", format: FormatParquet},
		{name: "csv by extension", file: "a.csv", head: "a,b\n1,2\n", format: FormatCSV},
		{name: "csv with a byte-order mark", file: "a.csv", head: "\xef\xbb\xbfa,b\n1,2\n", format: FormatCSV},
		{name: "delimited text with no extension", file: "export", head: "a;b;c\n1;2;3\n4;5;6\n", format: FormatCSV},
		{name: "ndjson by content", file: "events.log", head: "{\"a\": 1}\n{\"a\": 2}\n", format: FormatJSON},
		{name: "json array", file: "a.json", head: "  [ {\"a\": 1} ]", format: FormatJSON},
		{name: "workbook", file: "Budget.XLSX", head: "PK\x03\x04rest", format: FormatXLSX},
		{name: "zip archive refused with a way forward", file: "a.zip", head: "PK\x03\x04", refused: true},
		{name: "sql dump refused", file: "backup.sql", head: "CREATE TABLE", refused: true},
		{name: "legacy workbook refused", file: "old.xls", head: "\xd0\xcf\x11\xe0", refused: true},
		{name: "binary refused", file: "photo.png", head: "\x89PNG\x00\x00", refused: true},
		{name: "prose refused", file: "notes.md", head: "# Notes\nsome text\n", refused: true},
		{name: "empty refused", file: "a.csv", head: "", refused: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := Detect(tc.file, []byte(tc.head))
			if tc.refused {
				if got != "" || reason == "" {
					t.Fatalf("Detect = %q, %q; want refused with a reason", got, reason)
				}
				return
			}
			if got != tc.format {
				t.Fatalf("Detect = %q (%s); want %q", got, reason, tc.format)
			}
		})
	}
}

func cols(names ...string) []ColumnRef {
	out := make([]ColumnRef, len(names))
	for i, n := range names {
		out[i] = ColumnRef{Name: n, Type: "VARCHAR"}
	}
	return out
}

// Requirements: HMD_CLI_NEURONSPHERE_NERD039_SPEC003
func TestGroupFilesProposesTables(t *testing.T) {
	t.Parallel()

	files := []FileInfo{
		{Source: "s", Rel: "orders/month=2026-01/a.csv", Format: FormatCSV, Partitions: []string{"month"}, Columns: cols("order_id", "amount", "month")},
		{Source: "s", Rel: "orders/month=2026-02/b.csv", Format: FormatCSV, Partitions: []string{"month"}, Columns: cols("order_id", "amount", "channel", "month")},
		{Source: "s", Rel: "customers.csv", Format: FormatCSV, Columns: cols("customer_id", "name")},
		{Source: "s", Rel: "messy/regional report.csv", Format: FormatCSV, Columns: cols("region", "qty")},
		{Source: "s", Rel: "book.xlsx", Format: FormatXLSX, Sheet: "Plan", Columns: cols("region", "budget")},
		{Source: "s", Rel: "book.xlsx", Format: FormatXLSX, Sheet: "Actuals", Columns: cols("region", "spent")},
		// Same folder, unrelated columns: two groups, so neither may use a glob.
		{Source: "s", Rel: "mixed/x.csv", Format: FormatCSV, Columns: cols("a", "b", "c")},
		{Source: "s", Rel: "mixed/y.csv", Format: FormatCSV, Columns: cols("p", "q", "r")},
		{Source: "s", Rel: "mixed/z.csv", Format: FormatCSV, Columns: cols("a", "b", "c")},
	}
	got := GroupFiles(files, func(n string) bool { return n == "customers" })
	byName := map[string]Proposal{}
	for _, p := range got {
		byName[p.Table] = p
	}

	orders, ok := byName["orders"]
	if !ok {
		t.Fatalf("no orders group in %v", names(got))
	}
	if !reflect.DeepEqual(orders.Paths, []string{"orders/month=*/*.csv"}) {
		t.Errorf("orders paths = %v; want one hive glob", orders.Paths)
	}
	if len(orders.Files) != 2 {
		t.Errorf("orders groups %d files; drift in one column must not split the table", len(orders.Files))
	}
	if _, ok := byName["customers_2"]; !ok {
		t.Errorf("a name already taken must get a suffix; got %v", names(got))
	}
	if _, ok := byName["regional_report"]; !ok {
		t.Errorf("a single file is named after the file; got %v", names(got))
	}
	if _, ok := byName["book_plan"]; !ok {
		t.Errorf("each sheet is its own table; got %v", names(got))
	}
	if _, ok := byName["book_actuals"]; !ok {
		t.Errorf("each sheet is its own table; got %v", names(got))
	}
	mixed, ok := byName["mixed"]
	if !ok {
		t.Fatalf("no mixed group in %v", names(got))
	}
	if !reflect.DeepEqual(mixed.Paths, []string{"mixed/x.csv", "mixed/z.csv"}) {
		t.Errorf("mixed paths = %v; a glob would also read y.csv, so the files are listed", mixed.Paths)
	}
}

func names(ps []Proposal) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Table)
	}
	return out
}

// Requirements: HMD_CLI_NEURONSPHERE_NERD039_SPEC003
func TestReaderSQLMapsEngineNeutralOptions(t *testing.T) {
	t.Parallel()

	tbl := &Table{Table: "t", Format: FormatCSV, ReaderOptions: map[string]any{
		"delimiter": ";", "header": true, "skip": 2, "hive_partitioning": true,
		"column_types": map[string]any{"order_id": "utf8"},
	}}
	got, err := readerSQL(tbl, []string{"/data/o'brien/*.csv"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"read_csv(['/data/o''brien/*.csv']", "delim=';'", "header=true", "skip=2",
		"types={'order_id': 'VARCHAR'}", "hive_partitioning=true", "union_by_name=true",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("readerSQL = %s\nmissing %s", got, want)
		}
	}

	x := &Table{Table: "x", Format: FormatXLSX, Sheet: "Q1 'final'"}
	got, err = readerSQL(x, []string{"/d/b.xlsx"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "read_xlsx('/d/b.xlsx', sheet='Q1 ''final'''") {
		t.Errorf("xlsx reader = %s", got)
	}
	if _, err := readerSQL(x, []string{"/a.xlsx", "/b.xlsx"}); err == nil {
		t.Error("an xlsx table reading two workbooks must be refused")
	}
	if _, err := readerSQL(&Table{Table: "t", Format: FormatCSV, ReaderOptions: map[string]any{
		"column_types": map[string]any{"a": "varchar(10)"}}}, []string{"/a.csv"}); err == nil {
		t.Error("a column type outside the Arrow vocabulary must be refused")
	}
}

// Requirements: HMD_CLI_NEURONSPHERE_NERD039_SPEC004
func TestSelectSQLAppliesCasts(t *testing.T) {
	t.Parallel()

	got, err := selectSQL(&Table{Table: "t", Casts: map[string]string{"when": "date32", "qty": "int64"}}, "r")
	if err != nil {
		t.Fatal(err)
	}
	want := `SELECT * REPLACE (TRY_CAST("qty" AS BIGINT) AS "qty", TRY_CAST("when" AS DATE) AS "when") FROM r`
	if got != want {
		t.Errorf("selectSQL = %s\nwant %s", got, want)
	}
}

// Requirements: HMD_CLI_NEURONSPHERE_NERD039_SPEC004
func TestAnalyzeFlagsWhatNeedsADecision(t *testing.T) {
	t.Parallel()

	cols := []ColumnProfile{
		{Name: "week_start", DuckType: "VARCHAR"},
		{Name: "quantity", DuckType: "VARCHAR"},
		{Name: "price", DuckType: "VARCHAR"},
		{Name: "account_no", DuckType: "BIGINT"},
		{Name: "contact", DuckType: "VARCHAR"},
		{Name: "signup", DuckType: "DATE"},
		{Name: "first_name", DuckType: "VARCHAR"},
		{Name: "code", DuckType: "VARCHAR"},
	}
	text := map[string]textStats{
		"week_start": {NonEmpty: 100, AsDate: 100},
		"quantity":   {NonEmpty: 100, AsNumber: 96, AsInteger: 96},
		"price":      {NonEmpty: 100, AsNumber: 100, AsInteger: 40},
		"account_no": {NonEmpty: 100, LeadingZero: 12, AsNumber: 100, AsInteger: 100},
		"contact":    {NonEmpty: 100, Email: 90},
		// Dates are digits and dashes: they must not read as phone numbers.
		"signup": {NonEmpty: 100, AsDate: 100, Phone: 100},
		// Text identifiers with leading zeros are kept as text: no issue.
		"code": {NonEmpty: 100, LeadingZero: 50, AsNumber: 100, AsInteger: 100},
	}
	issues := analyze(cols, text)
	kinds := map[string][]string{}
	for _, i := range issues {
		kinds[i.Column] = append(kinds[i.Column], i.Kind)
	}
	want := map[string][]string{
		"week_start": {IssueTextAsDate},
		"quantity":   {IssueTextAsNumber},
		"price":      {IssueTextAsNumber},
		"account_no": {IssueLeadingZeros},
		"contact":    {IssuePII},
		"first_name": {IssuePII},
	}
	if !reflect.DeepEqual(kinds, want) {
		t.Errorf("issues by column = %v\nwant %v", kinds, want)
	}
	for _, i := range issues {
		switch i.Column {
		case "quantity":
			if casts := i.Suggest["casts"].(map[string]any); casts["quantity"] != "int64" {
				t.Errorf("quantity: suggest %v; whole numbers cast to int64", casts)
			}
		case "price":
			if casts := i.Suggest["casts"].(map[string]any); casts["price"] != "float64" {
				t.Errorf("price: suggest %v; decimals cast to float64", casts)
			}
		case "account_no":
			ro := i.Suggest["reader_options"].(map[string]any)["column_types"].(map[string]any)
			if ro["account_no"] != "utf8" {
				t.Errorf("account_no: suggest %v; read it as text", ro)
			}
		}
	}
}

// Requirements: HMD_CLI_NEURONSPHERE_NERD039_SPEC004
func TestSniffIssues(t *testing.T) {
	t.Parallel()

	got := sniffIssues(&sniff{HasHeader: false, SkipRows: 2})
	if len(got) != 2 || got[0].Kind != IssueHeaderJunk || got[1].Kind != IssueNoHeader {
		t.Fatalf("sniffIssues = %+v", got)
	}
	if skip := got[0].Suggest["reader_options"].(map[string]any)["skip"]; skip != int64(2) {
		t.Errorf("skip suggestion = %v, want 2", skip)
	}
	if len(sniffIssues(&sniff{HasHeader: true})) != 0 || sniffIssues(nil) != nil {
		t.Error("a clean sniff, or none, has no issues")
	}
}

// Requirements: HMD_CLI_NEURONSPHERE_NERD039_SPEC004
func TestDriftOfFindsMissingAndRetypedColumns(t *testing.T) {
	t.Parallel()

	files := []FileInfo{
		{Rel: "a.csv", Columns: []ColumnRef{{"id", "VARCHAR"}, {"amount", "DOUBLE"}}},
		{Rel: "b.csv", Columns: []ColumnRef{{"id", "VARCHAR"}, {"amount", "BIGINT"}, {"channel", "VARCHAR"}}},
	}
	got := driftOf(files)
	if len(got) != 2 {
		t.Fatalf("driftOf = %+v; want amount (type) and channel (missing)", got)
	}
	if got[0].Column != "amount" || got[0].Kind != "type" {
		t.Errorf("first drift = %+v", got[0])
	}
	if got[1].Column != "channel" || got[1].Kind != "missing" || got[1].Files["a.csv"] != "(absent)" {
		t.Errorf("second drift = %+v", got[1])
	}
	if driftOf(files[:1]) != nil {
		t.Error("one file cannot drift")
	}
}

// Requirements: HMD_CLI_NEURONSPHERE_NERD039_SPEC005
func TestSchemaDiff(t *testing.T) {
	t.Parallel()

	pinned := []Column{{Name: "id", Type: "utf8"}, {Name: "amount", Type: "float64"}, {Name: "gone", Type: "utf8"}}
	now := []Column{{Name: "ID", Type: "utf8"}, {Name: "amount", Type: "int64"}, {Name: "extra", Type: "utf8"}}
	got := schemaDiff(pinned, now)
	want := []string{"amount is int64, pinned as float64", "gone is missing", "extra is new"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("schemaDiff = %q\nwant %q", got, want)
	}
	if schemaDiff(pinned[:2], []Column{{Name: "id", Type: "utf8"}, {Name: "amount", Type: "float64"}}) != nil {
		t.Error("matching schemas have no diff")
	}
}

// Requirements: HMD_CLI_NEURONSPHERE_NERD039_SPEC003
func TestArrowTypesRoundTrip(t *testing.T) {
	t.Parallel()

	for _, duck := range []string{"VARCHAR", "BIGINT", "DOUBLE", "DATE", "TIMESTAMP", "BOOLEAN", "DECIMAL(18,3)"} {
		arrow := DuckToArrow(duck)
		back, err := ArrowToDuck(arrow)
		if err != nil {
			t.Errorf("%s -> %s: %v", duck, arrow, err)
			continue
		}
		if strings.ReplaceAll(back, " ", "") != duck {
			t.Errorf("%s -> %s -> %s", duck, arrow, back)
		}
	}
	if got := DuckToArrow("STRUCT(a INTEGER)"); got != "duckdb:STRUCT(a INTEGER)" {
		t.Errorf("a nested type is kept as DuckDB spells it, got %s", got)
	}
}

// Requirements: HMD_CLI_NEURONSPHERE_NERD039_SPEC002
func TestDecodeResultsKeepsColumnOrder(t *testing.T) {
	t.Parallel()

	out := []byte(`[{"b":1,"a":"x"},
{"b":2,"a":null}]
[{"n":12345678901234567}]
`)
	res, cols, err := decodeResults(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 || !reflect.DeepEqual(cols[0], []string{"b", "a"}) {
		t.Fatalf("results = %v, columns = %v", res, cols)
	}
	if num(res[1][0]["n"]) != 12345678901234567 {
		t.Errorf("a large count must not be rounded: %v", res[1][0]["n"])
	}
	if _, _, err := decodeResults([]byte(`{"not": "an array"}`)); err == nil {
		t.Error("output that is not result arrays must be an error")
	}
}

// Requirements: HMD_CLI_NEURONSPHERE_NERD039_SPEC002
func TestDockerEngineArgs(t *testing.T) {
	t.Parallel()

	d := DockerEngine{Image: "img:1"}
	args := d.args(Request{DB: "/ws/catalog.duckdb", ReadOnly: true, Mounts: []Mount{
		{Path: "/ws"}, {Path: "/src", ReadOnly: true}, {Path: "/src", ReadOnly: true},
	}})
	got := strings.Join(args, " ")
	for _, want := range []string{"--network none", "-v /ws:/ws", "-v /src:/src:ro", "img:1 -json -bail -readonly /ws/catalog.duckdb"} {
		if !strings.Contains(got, want) {
			t.Errorf("args = %s\nmissing %s", got, want)
		}
	}
	if strings.Count(got, "/src:/src") != 1 {
		t.Errorf("a path is mounted once: %s", got)
	}
	if strings.Contains(strings.Join(d.args(Request{ContinueOnError: true}), " "), "-bail") {
		t.Error("ContinueOnError must not pass -bail")
	}
}

// Requirements: HMD_CLI_NEURONSPHERE_NERD039_SPEC001
func TestWorkspaceLifecycle(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	src := t.TempDir()
	if _, err := Current(home, ""); err == nil {
		t.Fatal("no current workspace must be a usage error")
	}
	if _, err := Init(home, "Bad Name", nil); err == nil {
		t.Fatal("an invalid name must be refused")
	}
	ws, err := Init(home, "sales", []string{src, src})
	if err != nil {
		t.Fatal(err)
	}
	if len(ws.Config.Sources) != 1 {
		t.Errorf("the same folder is one source, got %v", ws.Config.Sources)
	}
	for _, d := range []string{TablesDir, ProfileDir, ModelDir, QueriesDir, SnapshotDir} {
		if _, err := os.Stat(filepath.Join(home, "data", "sales", d)); err != nil {
			t.Errorf("layout: %v", err)
		}
	}
	if _, err := Init(home, "ops", nil); err != nil {
		t.Fatal(err)
	}
	names, current, err := List(home)
	if err != nil || !reflect.DeepEqual(names, []string{"ops", "sales"}) || current != "ops" {
		t.Fatalf("List = %v, %q, %v", names, current, err)
	}
	if err := Use(home, "sales"); err != nil {
		t.Fatal(err)
	}
	cur, err := Current(home, "")
	if err != nil || cur.Name != "sales" {
		t.Fatalf("Current = %v, %v", cur, err)
	}
	cur.Mark("orders", StepGrouped)
	cur.Mark("orders", StepGrouped)
	if err := cur.Save(); err != nil {
		t.Fatal(err)
	}
	again, err := Open(home, "sales")
	if err != nil || !reflect.DeepEqual(again.Config.Progress["orders"], []string{StepGrouped}) {
		t.Fatalf("progress round trip = %v, %v", again.Config.Progress, err)
	}
	if err := Delete(home, "sales"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Error("deleting a workspace must not touch its sources")
	}
	if _, err := Current(home, ""); err == nil {
		t.Error("deleting the current workspace leaves none current")
	}
}

// Requirements: HMD_CLI_NEURONSPHERE_NERD039_SPEC001
func TestOpenRefusesUnknownVersion(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	if _, err := Init(home, "w", nil); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(home, "data", "w", ConfigFile)
	if err := os.WriteFile(p, []byte("version: 99\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(home, "w"); err == nil || !strings.Contains(err.Error(), "version 99") {
		t.Errorf("Open = %v; an unknown version is refused, not partially read", err)
	}
}
