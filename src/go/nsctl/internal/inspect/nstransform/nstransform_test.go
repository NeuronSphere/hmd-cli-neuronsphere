package nstransform

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/inspect"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/model"
)

// The fixtures are verbatim copies of src/transforms from
// hmd-config-billing-transforms and hmd-config-transform-reporting.

func run(t *testing.T, fixture string) *model.Model {
	t.Helper()
	dir := filepath.Join("testdata", fixture)
	src := inspect.Source{Root: dir, Repo: fixture, FS: os.DirFS(dir)}
	obs, reports := inspect.Run(context.Background(), []inspect.Source{src}, []inspect.Inspector{Inspector{}})
	if len(reports) != 1 || reports[0].Error != "" {
		t.Fatalf("reports = %+v", reports)
	}
	return model.Consolidate(obs)
}

func disagreements(m *model.Model, code string) []model.Disagreement {
	var out []model.Disagreement
	for _, d := range m.Disagreements {
		if d.Code == code {
			out = append(out, d)
		}
	}
	return out
}

func TestBillingLayersAreOneNoun(t *testing.T) {
	t.Parallel()
	m := run(t, "billing")
	n := m.Noun(model.ID{Namespace: "billing", Name: "aws_billing"})
	if n == nil {
		var ids []string
		for _, n := range m.Nouns {
			ids = append(ids, n.ID.String())
		}
		t.Fatalf("billing.aws_billing missing; nouns = %v", ids)
	}
	if len(m.Nouns) != 1 {
		t.Errorf("nouns = %d, want 1", len(m.Nouns))
	}
	var layers []string
	for _, mf := range n.Manifestations {
		layers = append(layers, mf.Layer)
	}
	sort.Strings(layers)
	if strings.Join(layers, ",") != "final,source,staging,ux" {
		t.Errorf("layers = %v", layers)
	}
	if len(n.Attributes) != 171 {
		t.Errorf("attributes = %d, want 171", len(n.Attributes))
	}
	if a := n.Attribute("line_item_unblended_cost"); a == nil || a.Type != model.Float || a.PhysicalType != "double" {
		t.Errorf("line_item_unblended_cost = %+v", a)
	}
	for _, p := range []string{"year", "month"} {
		if a := n.Attribute(p); a == nil || !a.Partition {
			t.Errorf("%s should be a partition: %+v", p, a)
		}
	}
	src := n.Manifestations[0]
	for _, mf := range n.Manifestations {
		if mf.Layer == "source" {
			src = mf
		}
	}
	if !strings.HasPrefix(src.Template, "billing_source.aws_billing_{time.year~") {
		t.Errorf("source template = %q", src.Template)
	}

	// CREATE and INSERT ... SELECT agree on all 171 columns, so nothing about
	// the column lists is reported.
	for _, code := range []string{"column-count", "column-order"} {
		if d := disagreements(m, code); len(d) != 0 {
			t.Errorf("%s: %v", code, d)
		}
	}
	// Every reference resolves inside the repository except the dependency
	// named by the 01 transform's declared tf_name, which is provided too.
	if d := disagreements(m, "unresolved-reference"); len(d) != 0 {
		t.Errorf("unresolved: %v", d)
	}
	if d := disagreements(m, "tf-name-mismatch"); len(d) != 1 || !strings.Contains(d[0].Subject, "01-billing-create-source.yaml") {
		t.Errorf("tf-name-mismatch = %v", d)
	}
	unused := disagreements(m, "unused-run-param")
	var where []string
	for _, d := range unused {
		where = append(where, d.Subject)
	}
	if len(unused) != 2 || !strings.Contains(strings.Join(where, " "), "ddl_02a") || !strings.Contains(strings.Join(where, " "), "ddl_02b") {
		t.Errorf("unused-run-param = %v", where)
	}
	if d := disagreements(m, "type-word-alias"); len(d) != 2 {
		t.Errorf("type-word-alias = %v", d)
	}
}

func TestNTCExportChain(t *testing.T) {
	t.Parallel()
	m := run(t, "reporting")
	n := m.Noun(model.ID{Namespace: "ntc", Name: "ntc_instances_export"})
	if n == nil {
		t.Fatalf("ntc.ntc_instances_export missing")
	}
	if len(n.Manifestations) != 3 {
		t.Errorf("manifestations = %d", len(n.Manifestations))
	}
	// Attributes come from the final layer.
	want := map[string]model.LogicalType{
		"identifier": model.String, "created_at": model.Timestamp, "export_date": model.Date,
		"p_iso_date": model.Date, "p_environment": model.String,
	}
	for name, typ := range want {
		if a := n.Attribute(name); a == nil || a.Type != typ {
			t.Errorf("%s = %+v, want %s", name, a, typ)
		}
	}
	if a := n.Attribute("p_iso_date"); a == nil || !a.Partition {
		t.Errorf("p_iso_date not a partition")
	}
	// created_at is a string in the source layer and a timestamp after it.
	var layerChange bool
	for _, d := range disagreements(m, "layer-type-change") {
		if d.Subject == "ntc.ntc_instances_export.created_at" {
			layerChange = true
		}
	}
	if !layerChange {
		t.Errorf("layer type change for created_at not reported")
	}

	// The drop transform drops a table name the create transform never
	// makes: it lacks the .replace('-', '_') the create applies.
	var dropMiss bool
	for _, d := range disagreements(m, "unresolved-reference") {
		if strings.Contains(d.Message, "drop") && strings.Contains(d.Subject, "ntc_instances_export_{iso_date}_{environment}") {
			dropMiss = true
		}
	}
	if !dropMiss {
		t.Errorf("drop of an uncreated table not reported: %v", m.Disagreements)
	}
	if d := disagreements(m, "tf-name-mismatch"); len(d) != 1 || !strings.Contains(d[0].Message, "nsreporting_04_ntc_drop_source") {
		t.Errorf("tf-name-mismatch = %v", d)
	}
	// The staging INSERT lists the same 13 columns as the staging DDL.
	for _, code := range []string{"column-count", "column-order"} {
		if d := disagreements(m, code); len(d) != 0 {
			t.Errorf("%s: %v", code, d)
		}
	}
}

func TestDbtTransformBindsItsProjectSchema(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join("testdata", "reporting", "src", "transforms", "nsreporting_04_ntc_dbt.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var binding *model.BindingObs
	for _, o := range inspectFile("nsreporting_04_ntc_dbt.yaml", data) {
		if o.Binding != nil {
			binding = o.Binding
		}
	}
	if binding == nil || binding.Scope != "dbt:hmd_config_transform_reporting" || binding.Schema != "ntc" {
		t.Errorf("binding = %+v", binding)
	}
}

func TestRender(t *testing.T) {
	t.Parallel()
	r := renderer{params: map[string]any{
		"schema_name": "ntc", "suffix": "my-staging", "iso_date": "context.iso_date", "bucket": "TRINO_BUCKET",
	}}
	for in, want := range map[string]string{
		"{{ ns_context['schema_name' ]}}_source":               "ntc_source",
		"billing_{{ ns_context['suffix'].replace('-', '_') }}": "billing_my_staging",
		"t_{{ ns_context['iso_date'].replace('-', '_') }}":     "t_{iso_date|replace(-,_)}",
		"t_{{ ns_context['iso_date'] }}":                       "t_{iso_date}",
		"'{{ ns_context['bucket'] }}/x'":                       "'{bucket}/x'",
		"{{ env_var('DBT_SCHEMA') }}":                          "{env:DBT_SCHEMA}",
		"{{ ns_context['missing'] }}":                          "{missing}",
		"{% if x %}a{% endif %}":                               "a",
	} {
		if got := r.render(in); got != want {
			t.Errorf("render(%q) = %q, want %q", in, got, want)
		}
	}
	a := r.render("{{(macros.dateutil.parser.parse(ns_context['time']) - macros.timedelta(days=4)).year}}")
	b := r.render("{{ (macros.dateutil.parser.parse(ns_context['time']) - macros.timedelta(days=4)).year }}")
	c := r.render("{{(macros.dateutil.parser.parse(ns_context['time']) - macros.timedelta(days=4)).month}}")
	if a != b || a == c || !strings.HasPrefix(a, "{time.year~") {
		t.Errorf("expression placeholders: %q %q %q", a, b, c)
	}
}
