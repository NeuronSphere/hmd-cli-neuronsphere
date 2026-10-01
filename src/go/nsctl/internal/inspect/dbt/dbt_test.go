package dbt

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

// Fixtures are verbatim copies of the dbt projects in
// hmd-config-transform-reporting, hmd-config-transform-reporting-views and
// hmd-tf-transform-reporting.

func observe(t *testing.T, fixtures ...string) []model.Observation {
	t.Helper()
	var srcs []inspect.Source
	for _, f := range fixtures {
		dir := filepath.Join("testdata", f)
		srcs = append(srcs, inspect.Source{Root: dir, Repo: f, FS: os.DirFS(dir)})
	}
	obs, reports := inspect.Run(context.Background(), srcs, []inspect.Inspector{Inspector{}})
	for _, r := range reports {
		if r.Error != "" {
			t.Fatal(r.Error)
		}
	}
	return obs
}

func codes(m *model.Model) map[string][]string {
	out := map[string][]string{}
	for _, d := range m.Disagreements {
		out[d.Code] = append(out[d.Code], d.Subject)
	}
	for _, v := range out {
		sort.Strings(v)
	}
	return out
}

func TestReportingProject(t *testing.T) {
	t.Parallel()
	m := model.Consolidate(observe(t, "reporting"))
	const ns = "hmd_config_transform_reporting"

	sti := m.Noun(model.ID{Namespace: ns, Name: "staging_transform_instance"})
	if sti == nil || len(sti.Manifestations) != 1 || sti.Manifestations[0].Format != "incremental" {
		t.Fatalf("staging_transform_instance = %+v", sti)
	}
	var names []string
	for _, a := range sti.Attributes {
		names = append(names, a.Name)
	}
	if got := strings.Join(names, ","); got != "identifier,transform_name,transform_version,created_at,scheduled_at,started_at,completed_at,status,export_date,_created,_updated,iso_date,environment" {
		t.Errorf("columns = %s", got)
	}

	dim := m.Noun(model.ID{Namespace: ns, Name: "dim_transform"})
	if dim == nil || dim.Description == nil || *dim.Description != "Transform dimension (name + version)." {
		t.Fatalf("dim_transform = %+v", dim)
	}
	// No config() in the SQL: dbt_project.yml's staging folder says table.
	if dim.Manifestations[0].Format != "table" {
		t.Errorf("dim_transform materialized = %q", dim.Manifestations[0].Format)
	}
	if a := dim.Attribute("transform_id"); a == nil || a.Required == nil || !*a.Required {
		t.Errorf("transform_id should be required by its not_null test: %+v", a)
	}

	fact := m.Noun(model.ID{Namespace: ns, Name: "fact_transform_instance_count"})
	if fact == nil || len(fact.Attributes) != 6 {
		t.Fatalf("fact_transform_instance_count = %+v", fact)
	}

	// Lineage: the source feeds staging, staging feeds dims and facts.
	edges := map[string]bool{}
	for _, e := range m.Lineage {
		edges[e.From+" -> "+e.To+" ("+e.Via+")"] = true
	}
	for _, want := range []string{
		"ntc_final.ntc_instances_export -> " + ns + ".staging_transform_instance (dbt-source)",
		ns + ".staging_transform_instance -> " + ns + ".dim_transform (dbt-ref)",
		ns + ".dim_date_hour -> " + ns + ".fact_transform_instance_count (dbt-ref)",
	} {
		if !edges[want] {
			t.Errorf("missing edge %s; have %v", want, edges)
		}
	}

	got := codes(m)
	// schema.yml documents four dims; staging and facts are undocumented.
	if want := []string{ns + ".fact_transform_instance_count", ns + ".fact_transform_instance_times",
		ns + ".staging_transform_instance", ns + ".staging_transform_times"}; strings.Join(got["undocumented-model"], ",") != strings.Join(want, ",") {
		t.Errorf("undocumented-model = %v", got["undocumented-model"])
	}
	// Inside one repository the source table is not produced by anything.
	if strings.Join(got["unresolved-reference"], ",") != "ntc_final.ntc_instances_export" {
		t.Errorf("unresolved-reference = %v", got["unresolved-reference"])
	}
	if len(got["documented-column-missing"]) != 0 || len(got["constraint-unknown-attribute"]) != 0 {
		t.Errorf("documented columns not matched to SQL: %v", m.Disagreements)
	}
}

// With the reporting transform's binding of the project to schema ntc, the
// views repository's sources resolve to the reporting project's models.
func TestViewsResolveAgainstBoundModels(t *testing.T) {
	t.Parallel()
	obs := observe(t, "reporting", "views")
	unbound := codes(model.Consolidate(obs))["unresolved-reference"]
	if len(unbound) != 7 {
		t.Errorf("without a binding: unresolved = %v", unbound)
	}

	obs = append(obs, model.Observation{Kind: model.KindBinding,
		Binding:    &model.BindingObs{Scope: "dbt:hmd_config_transform_reporting", Schema: "ntc"},
		Provenance: model.Provenance{Repo: "transform", File: "04.yaml", Authority: model.AuthDDL}})
	m := model.Consolidate(obs)
	if got := codes(m)["unresolved-reference"]; strings.Join(got, ",") != "ntc_final.ntc_instances_export" {
		t.Errorf("with a binding: unresolved = %v", got)
	}
	fact := m.Noun(model.ID{Namespace: "hmd_config_transform_reporting", Name: "fact_transform_instance_count"})
	if fact == nil || len(fact.Manifestations) != 2 || len(fact.Aliases) != 1 || fact.Aliases[0].ID.String() != "ntc.fact_transform_instance_count" {
		t.Fatalf("fact = %+v", fact)
	}
	var edge bool
	for _, e := range m.Lineage {
		if e.From == "hmd_config_transform_reporting.fact_transform_instance_count" &&
			e.To == "hmd_config_transform_reporting_views.monthly_success" {
			edge = true
		}
	}
	if !edge {
		t.Errorf("views lineage not resolved to the reporting models: %v", m.Lineage)
	}
}

func TestScaffoldProjectIsANegativeCase(t *testing.T) {
	t.Parallel()
	m := model.Consolidate(observe(t, "scaffold"))
	// dbt init's two example models, nothing else.
	if len(m.Nouns) != 2 {
		t.Errorf("nouns = %d", len(m.Nouns))
	}
}
