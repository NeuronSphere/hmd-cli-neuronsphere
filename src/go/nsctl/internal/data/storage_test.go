package data

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recorder answers every session with one row, n, and keeps the requests.
type recorder struct{ reqs []Request }

func (r *recorder) Run(_ context.Context, req Request) (Output, error) {
	r.reqs = append(r.reqs, req)
	return Output{Results: Result{{{"n": json.Number("5")}}}}, nil
}

// Requirements: HMD_CLI_NEURONSPHERE_NERD039_SPEC009
func TestParseTarget(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ to, bucket, prefix, instance string }{
		{"s3://my-data", "my-data", "", ""},
		{"s3://my-data/shop/", "my-data", "shop", ""},
		{"instance:storage", "", "", "storage"},
	} {
		b, p, i, err := ParseTarget(c.to)
		if err != nil || b != c.bucket || p != c.prefix || i != c.instance {
			t.Errorf("ParseTarget(%q) = %q %q %q %v", c.to, b, p, i, err)
		}
	}
	for _, bad := range []string{"my-data", "https://x/y", "s3://", "instance:"} {
		if _, _, _, err := ParseTarget(bad); err == nil {
			t.Errorf("ParseTarget(%q) accepted", bad)
		}
	}
	st := &Storage{Bucket: "b", Prefix: "shop"}
	if got := st.LandedURL("orders"); got != "s3://b/shop/raw/orders/data.parquet" {
		t.Errorf("LandedURL = %s", got)
	}
}

// Requirements: HMD_CLI_NEURONSPHERE_NERD039_SPEC009
func TestLandIsIdempotentAndSessionsJoinTheEnvironmentOnlyWithStorage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "orders.csv"), []byte("id\n1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, err := Init(t.TempDir(), "shop", []string{src})
	if err != nil {
		t.Fatal(err)
	}
	tbl := &Table{Table: "orders", Source: ws.Config.Sources[0].Name, Paths: []string{"orders.csv"}, Format: "csv", SchemaMode: "infer"}
	if err := ws.SaveTable(tbl); err != nil {
		t.Fatal(err)
	}
	if r := ws.session(Request{}); r.Network != "" || r.Setup != "" {
		t.Errorf("a workspace with no storage has no network and no secret: %+v", r)
	}
	if _, err := ws.Land(ctx, &recorder{}, "orders", false); err == nil {
		t.Error("land without a target must be refused")
	}

	ws.Config.Storage = &Storage{Env: "dev", Target: "s3://b", Bucket: "b"}
	ws.Remote = &Remote{Network: "net", Endpoint: "neuronsphere:4566", Region: "us-west-2", KeyID: "000000000010", Secret: "dummykey"}
	eng := &recorder{}
	rep, err := ws.Land(ctx, eng, "orders", false)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Unchanged || rep.Rows != 5 || rep.URL != "s3://b/raw/orders/data.parquet" {
		t.Errorf("first land = %+v", rep)
	}
	req := eng.reqs[0]
	if req.Network != "net" || !strings.Contains(req.Setup, "ENDPOINT 'neuronsphere:4566'") ||
		!strings.HasPrefix(req.Setup, ".output /dev/null\n") || strings.Contains(req.Script, "dummykey") {
		t.Errorf("land session = %+v", req)
	}
	if !strings.Contains(req.Script, "COPY (SELECT * FROM read_csv(") || !strings.Contains(req.Script, "TO 's3://b/raw/orders/data.parquet'") {
		t.Errorf("land reads the files and writes the bucket: %s", req.Script)
	}
	if src, _ := ws.viewSource(tbl); src != "SELECT * FROM read_parquet('s3://b/raw/orders/data.parquet')" {
		t.Errorf("a landed table's view reads the bucket: %s", src)
	}

	again, err := ws.Land(ctx, eng, "orders", false)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Unchanged || len(eng.reqs) != 1 {
		t.Errorf("unchanged files are not written again: %+v, %d sessions", again, len(eng.reqs))
	}
	if _, err := ws.Land(ctx, eng, "orders", true); err != nil || len(eng.reqs) != 2 {
		t.Errorf("--force writes anyway: %v, %d sessions", err, len(eng.reqs))
	}
	if strings.Contains(eng.reqs[1].Script, "read_parquet('s3://") && strings.Contains(eng.reqs[1].Script, "COPY (SELECT * FROM read_parquet('s3://") {
		t.Error("landing again reads the files, not the earlier landing")
	}

	if err := os.WriteFile(filepath.Join(src, "orders.csv"), []byte("id\n1\n2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := ws.Status()
	if err != nil {
		t.Fatal(err)
	}
	if st.Tables[0].Landed == "" || strings.Join(st.Tables[0].Stale, ",") != "orders.csv" || st.Storage.Bucket != "b" {
		t.Errorf("status after the file changed = %+v", st.Tables[0])
	}
	if rep, err := ws.Land(ctx, eng, "orders", false); err != nil || rep.Unchanged {
		t.Errorf("a changed file is landed again: %+v %v", rep, err)
	}
}
