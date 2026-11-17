//go:build docker

package data

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// The live test runs the real DuckDB image over testdata/corpus. It needs a
// container engine and the image: `go test -tags docker ./internal/data/`,
// with HMD_DUCKDB_IMAGE naming a local build (hmd-img-duckdb:dev) until the
// image is published.

func liveEngine(t *testing.T) Engine {
	t.Helper()
	img := os.Getenv(ImageEnv)
	if img == "" {
		img = DefaultImage
	}
	return DockerEngine{Image: img}
}

// corpusHashes fingerprints every file in the corpus, so the test can prove
// nothing wrote to a source.
func corpusHashes(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		h, err := fileHash(p)
		out[p] = h
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Requirements: HMD_CLI_NEURONSPHERE_NERD039, HMD_CLI_NEURONSPHERE_NERD039_SPEC002, HMD_CLI_NEURONSPHERE_NERD039_SPEC003, HMD_CLI_NEURONSPHERE_NERD039_SPEC004, HMD_CLI_NEURONSPHERE_NERD039_SPEC005, HMD_CLI_NEURONSPHERE_NERD039_SPEC006
func TestLiveCorpusFromFilesToSQL(t *testing.T) {
	ctx := context.Background()
	eng := liveEngine(t)
	corpus, err := filepath.Abs("testdata/corpus")
	if err != nil {
		t.Fatal(err)
	}
	before := corpusHashes(t, corpus)

	// The workspace must be somewhere the container engine shares with the
	// host; the default temp directory is not, under every engine.
	home, err := os.MkdirTemp(filepath.Join("..", "..", "..", "..", "..", "target"), "data-live-")
	if err != nil {
		if err := os.MkdirAll(filepath.Join("..", "..", "..", "..", "..", "target"), 0o755); err != nil {
			t.Fatal(err)
		}
		home, err = os.MkdirTemp(filepath.Join("..", "..", "..", "..", "..", "target"), "data-live-")
		if err != nil {
			t.Fatal(err)
		}
	}
	home, _ = filepath.Abs(home)
	t.Cleanup(func() { _ = os.RemoveAll(home) })

	ws, err := Init(home, "shop", []string{corpus})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := ws.Scan(ctx, eng, ScanOptions{Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"budget_actuals", "budget_plan", "customers", "events", "orders", "products", "regional_report"}
	if got := names(rep.Proposals); !equalStrings(got, want) {
		t.Fatalf("proposals = %v\nwant %v", got, want)
	}
	if rep.FilesSeen != 10 || len(rep.Unsupported) != 3 {
		t.Errorf("files seen %d, unsupported %v; want 10 and 3 (zip, sql, md)", rep.FilesSeen, rep.Unsupported)
	}

	orders, err := ws.ProfileTable(ctx, eng, "orders", false)
	if err != nil {
		t.Fatal(err)
	}
	if orders.Rows != 5 || orders.Files != 2 {
		t.Errorf("orders: %d rows from %d files; want 5 from 2", orders.Rows, orders.Files)
	}
	if !hasIssue(orders, "channel", IssueDrift) {
		t.Errorf("orders: channel is only in February's file; issues %+v", orders.Issues)
	}
	if !column(orders, "order_id").CandidateKey || column(orders, "order_id").Type != "utf8" {
		t.Errorf("order_id = %+v; want a text candidate key (leading zeros kept)", column(orders, "order_id"))
	}

	report, err := ws.ProfileTable(ctx, eng, "regional_report", false)
	if err != nil {
		t.Fatal(err)
	}
	if !hasIssue(report, "", IssueHeaderJunk) || !hasIssue(report, "quantity", IssueTextAsNumber) {
		t.Errorf("regional_report issues = %+v; want junk lines above the header and quantity as text", report.Issues)
	}

	customers, err := ws.ProfileTable(ctx, eng, "customers", false)
	if err != nil {
		t.Fatal(err)
	}
	email := column(customers, "email")
	if !email.PII || len(email.Sample) == 0 || email.Sample[0] != "(redacted)" {
		t.Errorf("email = %+v; personal data must be flagged and its samples redacted", email)
	}
	if column(customers, "signup_date").PII {
		t.Error("a date column is not a phone number")
	}

	reg, err := ws.Register(ctx, eng, true)
	if err != nil {
		t.Fatal(err)
	}
	rows := map[string]int64{}
	for _, s := range reg.Tables {
		rows[s.Table] = s.Rows
	}
	wantRows := map[string]int64{"budget_actuals": 2, "budget_plan": 3, "customers": 4, "events": 3, "orders": 5, "products": 3, "regional_report": 21}
	for k, v := range wantRows {
		if rows[k] != v {
			t.Errorf("%s: %d rows, want %d", k, rows[k], v)
		}
	}

	got, cols, err := ws.Query(ctx, eng, "SELECT c.name, sum(o.amount) AS total FROM orders o JOIN customers c USING (customer_id) GROUP BY ALL ORDER BY total DESC LIMIT 1", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || str(got[0]["name"]) != "Ada Lovelace" || !equalStrings(cols, []string{"name", "total"}) {
		t.Errorf("join = %v %v", got, cols)
	}
	if _, _, err := ws.Query(ctx, eng, "CREATE TABLE x AS SELECT 1", false); err == nil {
		t.Error("the catalog is read-only to a query without --write")
	}

	if _, err := ws.SnapshotTable(ctx, eng, "orders"); err != nil {
		t.Fatal(err)
	}
	st, err := ws.Status()
	if err != nil {
		t.Fatal(err)
	}
	for _, ts := range st.Tables {
		if ts.Table == "orders" && (!ts.Snapshot || len(ts.Stale) != 0) {
			t.Errorf("orders status = %+v; a fresh snapshot is not stale", ts)
		}
	}

	after := corpusHashes(t, corpus)
	for p, h := range before {
		if after[p] != h {
			t.Errorf("source %s changed; sources are only ever read", p)
		}
	}
	if len(after) != len(before) {
		t.Errorf("the corpus gained or lost files: %d -> %d", len(before), len(after))
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func hasIssue(p *Profile, column, kind string) bool {
	for _, i := range p.Issues {
		if i.Kind == kind && (column == "" || i.Column == column) {
			return true
		}
	}
	return false
}

func column(p *Profile, name string) ColumnProfile {
	for _, c := range p.Columns {
		if c.Name == name {
			return c
		}
	}
	return ColumnProfile{}
}
