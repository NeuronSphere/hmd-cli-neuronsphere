package modelsource

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/inspect"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/model"
)

// testdata/retail is NEP051's example model: sources over a pack's concepts.

// Requirements: HMD_CLI_NEURONSPHERE_NERD039_SPEC007
func TestParseAndMarshalKeepNEP051Files(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile("testdata/retail/src/sources/orders_export.source.yaml")
	if err != nil {
		t.Fatal(err)
	}
	f, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if f.From.CIT != "orders_export_csv" || f.Prov() != Declared || len(f.Columns) != 11 || f.Columns[0].Name != "order_no" {
		t.Errorf("orders_export = %+v", f)
	}
	rel := f.Yields.Get("order_contains_product")
	if rel == nil || !rel.IsRelationship() || rel.From != "order" || len(rel.Maps) != 3 {
		t.Errorf("order_contains_product = %+v", rel)
	}
	out, err := Marshal(f, "")
	if err != nil {
		t.Fatal(err)
	}
	again, err := Parse(out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(again.Yields) != len(f.Yields) || again.Yields[0].Key != "customer" || again.Columns[10].Type != "decimal(12,2)" {
		t.Errorf("round trip lost order or content:\n%s", out)
	}
	if !strings.Contains(string(out), "  order: {order_number: order_no, ordered_at: order_ts, channel: channel}") {
		t.Errorf("a yield is written on one line:\n%s", out)
	}

	b, _ = os.ReadFile("testdata/retail/src/sources/erp_customer.source.yaml")
	f, err = Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if seg := f.Yields.Get("customer").Maps[3]; seg.Attribute != "segment" || !strings.HasPrefix(seg.SQL, "case seg_cd") {
		t.Errorf("an sql mapping = %+v", seg)
	}
	for _, bad := range []string{
		"source: x\nfrom: {table: a.b.c, group: g}\nyields: {c: {id: id}}\n",
		"source: x\nfrom: {group: g}\nyields: {r: {from: a, to: b}}\n",
		"source: x\nfrom: {group: g}\nprovenance: maybe\nyields: {c: {id: id}}\n",
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

// Requirements: HMD_CLI_NEURONSPHERE_NERD039_SPEC007
func TestSourcesBecomeObservationsAtTheirProvenancesAuthority(t *testing.T) {
	t.Parallel()
	dir, _ := filepath.Abs("testdata/retail")
	src := inspect.Source{Root: dir, Repo: "example-retail-model", FS: os.DirFS(dir)}
	obs, err := Inspector{}.Inspect(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	customer := model.ID{Namespace: "example_retail", Name: "customer"}
	var located, nouns int
	for _, o := range obs {
		if o.Kind == model.KindFinding {
			t.Errorf("finding: %+v", o.Finding)
		}
		if o.Subject == customer && o.Kind == model.KindNoun {
			nouns++
			if o.Provenance.Authority != model.AuthSourceDeclared {
				t.Errorf("a declared source is at %d", o.Provenance.Authority)
			}
		}
		if o.Kind == model.KindBinding && o.Binding.Values["table_name"].String() != "" {
			located++
			if o.Subject != customer || o.Binding.Values["table_name"].String() != "cust_mstr" {
				t.Errorf("only a one-noun source stands for its table: %+v", o)
			}
		}
		if o.Kind == model.KindNoun && o.Subject.Name == "order_contains_product" &&
			(o.Noun.RefFrom != "example_retail.order" || o.Noun.RefTo != "example_retail.product") {
			t.Errorf("relationship = %+v", o.Noun)
		}
	}
	if nouns != 2 || located != 1 {
		t.Errorf("customer is yielded by 2 sources (got %d); 1 binding has a location (got %d)", nouns, located)
	}
	if Authority(Inferred) != model.AuthInferred || Authority("") != model.AuthInferred || Authority(Declared) != 90 {
		t.Error("authority follows provenance")
	}
}
