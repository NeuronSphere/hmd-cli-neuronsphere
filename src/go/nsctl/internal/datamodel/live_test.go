//go:build docker

package datamodel

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/data"
)

// The live test runs the real images over internal/data/testdata/corpus:
// `go test -tags docker ./internal/datamodel/`, with HMD_DUCKDB_IMAGE and
// HMD_DATA_DOCS_IMAGE naming local builds until the images are published.

func image(env, def string) string {
	if v := os.Getenv(env); v != "" {
		return v
	}
	return def
}

// Requirements: HMD_CLI_NEURONSPHERE_NERD039_SPEC004, HMD_CLI_NEURONSPHERE_NERD039_SPEC007, HMD_CLI_NEURONSPHERE_NERD039_SPEC008
func TestLiveFromFilesToDocs(t *testing.T) {
	ctx := context.Background()
	eng := data.DockerEngine{Image: image(data.ImageEnv, data.DefaultImage)}
	corpus, _ := filepath.Abs("../data/testdata/corpus")
	target, _ := filepath.Abs("../../../../../target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	// Under target/, which the container engine shares with the host.
	home, err := os.MkdirTemp(target, "data-live-model-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })

	ws, err := data.Init(home, "shop", []string{corpus})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ws.Scan(ctx, eng, data.ScanOptions{Apply: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.Register(ctx, eng, false); err != nil {
		t.Fatal(err)
	}
	rel, err := ws.ProfileRelationships(ctx, eng)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]float64{}
	for _, j := range rel.Joins {
		found[j.ID()] = j.Inclusion
	}
	for _, id := range []string{join, "events.customer_id=customers.customer_id"} {
		if found[id] != 1 {
			t.Errorf("%s: inclusion %v, want 1 (joins %v)", id, found[id], found)
		}
	}

	rep, err := Understand(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(ids(rep.Proposals), " "), join) {
		t.Fatalf("proposals %v lack %s", ids(rep.Proposals), join)
	}
	for _, step := range []struct{ id, name string }{{"orders", "order"}, {"customers", "customer"}, {join, "customer_places_order"}} {
		if _, err := Approve(ctx, ws, step.id, Decision{Name: step.name}); err != nil {
			t.Fatalf("approve %s: %v", step.id, err)
		}
	}

	img := os.Getenv(DocsImageEnv)
	if img == "" {
		t.Skip("set " + DocsImageEnv + " (e.g. hmd-tmpl-data-docs:dev) to render docs")
	}
	out := filepath.Join(home, "out")
	if _, err := Docs(ctx, ws, eng, MickeyRenderer{Image: img}, out, false); err != nil {
		t.Fatal(err)
	}
	for _, name := range Outputs {
		if b, err := os.ReadFile(filepath.Join(out, name)); err != nil || len(b) == 0 {
			t.Errorf("%s: %v", name, err)
		}
	}
	dict, _ := os.ReadFile(filepath.Join(out, "DATA.md"))
	if !strings.Contains(string(dict), "## order") || !strings.Contains(string(dict), "*Unconfirmed.*") {
		t.Errorf("DATA.md does not document the named table and mark the rest unconfirmed:\n%s", dict)
	}
	rows, _, err := ws.Query(ctx, eng, `SELECT count(*) AS n FROM "order" JOIN customer USING (customer_id)`, false)
	if err != nil || len(rows) != 1 {
		t.Fatalf("the named views docs created: %v %v", rows, err)
	}
}
