package datamodel

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/data"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/inspect/modelsource"
)

// shop is a workspace with saved profiles and relationship evidence, the
// state `nsctl data profile` and `profile --relationships` leave behind. No
// engine is needed to understand it.
func shop(t *testing.T) *data.Workspace {
	t.Helper()
	ws, err := data.Init(t.TempDir(), "shop", []string{t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	src := ws.Config.Sources[0].Name
	profiles := map[string]*data.Profile{
		"customers": {Table: "customers", Rows: 4, Files: 1, Columns: []data.ColumnProfile{
			{Name: "customer_id", Type: "utf8", DuckType: "VARCHAR", Distinct: 4, CandidateKey: true},
			{Name: "email", Type: "utf8", DuckType: "VARCHAR", Distinct: 4, PII: true, CandidateKey: true},
		}},
		"orders": {Table: "orders", Rows: 5, Files: 2, Columns: []data.ColumnProfile{
			{Name: "order_id", Type: "utf8", DuckType: "VARCHAR", Distinct: 5, CandidateKey: true},
			{Name: "customer_id", Type: "utf8", DuckType: "VARCHAR", Distinct: 4},
			{Name: "amount", Type: "float64", DuckType: "DOUBLE", Distinct: 5},
		}},
	}
	for name, p := range profiles {
		if err := ws.SaveTable(&data.Table{Table: name, Source: src, Paths: []string{name + ".csv"}, Format: "csv", SchemaMode: "infer"}); err != nil {
			t.Fatal(err)
		}
		writeJSON(t, ws.Path(data.ProfileDir, name+".json"), p)
		ws.Mark(name, data.StepProfiled)
		ws.Mark(name, data.StepRegistered)
	}
	if err := ws.Save(); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, ws.Path(data.ProfileDir, data.RelationshipsFile), &data.Relationships{Tables: []string{"customers", "orders"},
		Joins: []data.Join{
			{FromTable: "orders", FromColumn: "customer_id", ToTable: "customers", ToColumn: "customer_id",
				FromDistinct: 4, Matched: 4, Inclusion: 1, Cardinality: "many_to_one", NameMatch: true},
			// Too little overlap to ask about.
			{FromTable: "orders", FromColumn: "order_id", ToTable: "customers", ToColumn: "customer_id",
				FromDistinct: 5, Matched: 1, Inclusion: 0.2, Cardinality: "one_to_one"},
		}})
	return ws
}

func writeJSON(t *testing.T, p string, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func ids(ps []Proposal) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.ID)
	}
	return out
}

const join = "orders.customer_id=customers.customer_id"

// Requirements: HMD_CLI_NEURONSPHERE_NERD039_SPEC007
func TestUnderstandProposesEntitiesAndLikelyJoinsAsQuestions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ws := shop(t)
	rep, err := Understand(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(ids(rep.Proposals), " "); got != "customers orders "+join {
		t.Fatalf("proposals = %q; entities first, then the one likely join", got)
	}
	for _, p := range rep.Proposals {
		if p.Question == "" || len(p.Evidence) == 0 {
			t.Errorf("%s has no question or evidence: %+v", p.ID, p)
		}
		for _, word := range []string{"concept", ".hms", "perspective", "noun", "entity"} {
			if strings.Contains(p.Question, word) {
				t.Errorf("%s asks in modeling vocabulary (%q): %s", p.ID, word, p.Question)
			}
		}
	}
	if q := rep.Proposals[2].Question; !strings.Contains(q, "100% of orders.customer_id") || !strings.Contains(q, "belong to one customer") {
		t.Errorf("join question = %q", q)
	}
	if rep.Proposals[1].Key != "order_id" {
		t.Errorf("orders key = %q; a key named after the table wins", rep.Proposals[1].Key)
	}
	b, err := os.ReadFile(filepath.Join(sourcesDir(ws), "orders.source.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	f, err := modelsource.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if f.Prov() != modelsource.Inferred || f.From.Group != "orders" || f.Columns.Type("amount") != "double" {
		t.Errorf("orders source = %+v", f)
	}
	// Asking again proposes nothing new and keeps what is there.
	again, err := Understand(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Proposals) != 3 {
		t.Errorf("a second understand proposes %v", ids(again.Proposals))
	}
}

// Requirements: HMD_CLI_NEURONSPHERE_NERD039_SPEC007
func TestApproveRenamesAndDeclaresAndRejectIsNotAskedAgain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ws := shop(t)
	if _, err := Understand(ctx, ws); err != nil {
		t.Fatal(err)
	}
	if _, err := Approve(ctx, ws, join, Decision{}); err == nil || !strings.Contains(err.Error(), "approve orders first") {
		t.Fatalf("a join before its tables: err = %v", err)
	}
	if _, err := Approve(ctx, ws, "orders", Decision{Name: "Order"}); err == nil {
		t.Error("a name that is not an identifier must be refused")
	}
	if _, err := Approve(ctx, ws, "orders", Decision{Name: "order", Description: "One order a customer placed."}); err != nil {
		t.Fatal(err)
	}
	// The pending join now names the renamed concept.
	b, _ := os.ReadFile(filepath.Join(sourcesDir(ws), "orders__customer_id__customers.source.yaml"))
	if f, err := modelsource.Parse(b); err != nil || f.Yields[0].Key != "order" || f.Yields[2].From != "order" {
		t.Errorf("pending join after the rename = %+v (%v)", f, err)
	}
	if _, err := Approve(ctx, ws, "customers", Decision{Name: "customer"}); err != nil {
		t.Fatal(err)
	}
	rep, err := Approve(ctx, ws, join, Decision{Name: "customer_places_order"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Proposals) != 0 || len(rep.Approved) != 3 {
		t.Fatalf("after three approvals: proposed %v, approved %v", ids(rep.Proposals), ids(rep.Approved))
	}
	hms, err := os.ReadFile(filepath.Join(schemaDir(ws), "order.hms"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Description string                    `json:"description"`
		Attributes  map[string]map[string]any `json:"attributes"`
	}
	if err := json.Unmarshal(hms, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Description != "One order a customer placed." || doc.Attributes["order_id"]["business_id"] != true ||
		doc.Attributes["amount"]["type"] != "float" {
		t.Errorf("order.hms = %s", hms)
	}
	rel, _ := os.ReadFile(filepath.Join(schemaDir(ws), "customer_places_order.hms"))
	if !strings.Contains(string(rel), `"ref_from": "shop.order"`) || !strings.Contains(string(rel), `"ref_to": "shop.customer"`) {
		t.Errorf("customer_places_order.hms = %s", rel)
	}

	// The model reads one noun per table, linked to its concept.
	m, err := Inspect(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, n := range m.Nouns {
		names = append(names, n.ID.String())
	}
	if got := strings.Join(names, " "); got != "shop.customer shop.customer_places_order shop.order" {
		t.Errorf("nouns = %q", got)
	}
	if len(rep.Disagreements) != 0 {
		t.Errorf("disagreements = %+v", rep.Disagreements)
	}

	rep, err = Reject(ctx, ws, join)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(schemaDir(ws), "customer_places_order.hms")); !os.IsNotExist(err) {
		t.Error("rejecting an approved join withdraws its concept")
	}
	rep, err = Understand(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Proposals) != 0 || strings.Join(rep.Rejected, " ") != join {
		t.Errorf("a rejected join was asked again: proposals %v, rejected %v", ids(rep.Proposals), rep.Rejected)
	}
}

// Requirements: HMD_CLI_NEURONSPHERE_NERD039_SPEC007
func TestAnApprovedKeyTheDataContradictsIsReported(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ws := shop(t)
	if _, err := Understand(ctx, ws); err != nil {
		t.Fatal(err)
	}
	if _, err := Approve(ctx, ws, "orders", Decision{}); err != nil {
		t.Fatal(err)
	}
	p, _ := ws.SavedProfile("orders")
	p.Columns[0].CandidateKey = false
	writeJSON(t, ws.Path(data.ProfileDir, "orders.json"), p)
	rep, err := Load(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Disagreements) != 1 || rep.Disagreements[0].Code != "key-contradicted" {
		t.Errorf("disagreements = %+v", rep.Disagreements)
	}
}

// fakeEngine records what docs asks the catalog to run.
type fakeEngine struct{ scripts []string }

func (f *fakeEngine) Run(_ context.Context, req data.Request) (data.Output, error) {
	f.scripts = append(f.scripts, req.Script)
	return data.Output{}, nil
}

// fakeRenderer writes each output from the context, as the templates would.
type fakeRenderer struct{ ctx *DocsContext }

func (f *fakeRenderer) Render(_ context.Context, input, output string) error {
	b, err := os.ReadFile(filepath.Join(input, "data", "workspace.json"))
	if err != nil {
		return err
	}
	f.ctx = &DocsContext{}
	if err := json.Unmarshal(b, f.ctx); err != nil {
		return err
	}
	for _, name := range Outputs {
		body := "# " + name + "\n"
		if name == "views.sql" {
			for _, v := range f.ctx.Views {
				body += v.SQL + "\n"
			}
		}
		if err := os.WriteFile(filepath.Join(output, name), []byte(body), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// Requirements: HMD_CLI_NEURONSPHERE_NERD039_SPEC008
func TestDocsRendersThroughTheRendererAndNeverReplacesAnEditedFile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ws := shop(t)
	if err := os.WriteFile(ws.Path(data.CatalogFile), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Understand(ctx, ws); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"orders", "customers"} {
		if _, err := Approve(ctx, ws, id, Decision{Name: singular(id)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ws.SaveQuery("big_orders", "SELECT * FROM orders WHERE amount > 10"); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "out")
	eng, r := &fakeEngine{}, &fakeRenderer{}
	rep, err := Docs(ctx, ws, eng, r, out, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Written) != len(Outputs) || strings.Join(rep.Views, " ") != "customers orders customer order" {
		t.Errorf("report = %+v", rep)
	}
	dc := r.ctx
	if len(dc.Tables) != 2 || dc.Tables[1].View != "order" || dc.Tables[1].Key != "order_id" {
		t.Errorf("context tables = %+v", dc.Tables)
	}
	for _, c := range dc.Tables[0].Columns {
		if c.Name == "email" && (!c.PII || c.Min != "") {
			t.Errorf("personal data must be withheld from the docs: %+v", c)
		}
	}
	if len(dc.Relationships) != 1 || dc.Relationships[0].Status != "proposed" {
		t.Errorf("an unconfirmed join is shown as such: %+v", dc.Relationships)
	}
	if dc.Queries[0].Name != "big_orders" || !dc.Queries[0].Saved {
		t.Errorf("queries = %+v", dc.Queries)
	}
	if len(eng.scripts) != 1 || !strings.Contains(eng.scripts[0], `CREATE OR REPLACE VIEW "order" AS SELECT "order_id", "customer_id", "amount" FROM "orders";`) {
		t.Errorf("views.sql was not run against the catalog: %q", eng.scripts)
	}
	if entries, _ := os.ReadDir(ws.Path(renderDir)); len(entries) != 0 {
		t.Errorf("the staging folder was left behind: %v", entries)
	}

	edited := filepath.Join(out, "DATA.md")
	if err := os.WriteFile(edited, []byte("my notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Docs(ctx, ws, eng, r, out, false); err == nil || !strings.Contains(err.Error(), "DATA.md") {
		t.Fatalf("an edited output was replaced: %v", err)
	}
	if b, _ := os.ReadFile(edited); string(b) != "my notes\n" {
		t.Error("a refused run must leave the edit in place")
	}
	if _, err := Docs(ctx, ws, eng, r, out, true); err != nil {
		t.Fatal(err)
	}
	stranger := filepath.Join(t.TempDir(), "theirs")
	if err := os.MkdirAll(stranger, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stranger, "views.sql"), []byte("-- someone else's\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stranger, "keep.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Docs(ctx, ws, eng, r, stranger, false); err == nil {
		t.Error("a file docs never wrote was replaced")
	}
	if _, err := Docs(ctx, ws, eng, r, stranger, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(stranger, "keep.txt")); err != nil {
		t.Error("docs never deletes anything in the folder")
	}
}

// Requirements: HMD_CLI_NEURONSPHERE_NERD039_SPEC008
func TestMickeyRunsWithNoNetworkAndTheBakedTemplates(t *testing.T) {
	t.Parallel()
	args := strings.Join(MickeyRenderer{Image: "img"}.args("/in", "/out"), " ")
	for _, want := range []string{"--network none", "/in:/hmd_transform/input:ro", "/out:/hmd_transform/output",
		"--tmpfs /hmd_transform/cache", "MICKEY_SKIP_DEPS=1", `"template_packages":["/opt/hmd-tmpl-data-docs"]`} {
		if !strings.Contains(args, want) {
			t.Errorf("mickey args lack %q: %s", want, args)
		}
	}
}

func TestTrinoTypes(t *testing.T) {
	t.Parallel()
	for arrow, want := range map[string]string{"utf8": "varchar", "int64": "bigint", "float64": "double",
		"decimal128(12, 2)": "decimal(12, 2)", "date32": "date", "timestamp[us]": "timestamp", "bool": "boolean"} {
		if got := TrinoType(arrow); got != want {
			t.Errorf("TrinoType(%s) = %s, want %s", arrow, got, want)
		}
	}
}
