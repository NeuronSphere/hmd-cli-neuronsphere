package model

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func prov(file string, auth Authority) Provenance {
	return Provenance{Inspector: "test", Repo: "repo", File: file, Line: 1, Authority: auth, Confidence: Decided}
}

func loadHMS(t *testing.T, name string) (*HMSDoc, []byte) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "hms", name))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := ParseHMS(data)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return doc, data
}

// Every fixture copied from a real language pack exports back to a document
// equal to its source: the IR keeps everything .hms says.
func TestHMSRoundTrip(t *testing.T) {
	t.Parallel()
	files, _ := filepath.Glob(filepath.Join("testdata", "hms", "*.hms"))
	if len(files) < 8 {
		t.Fatalf("expected the fixture set, found %d files", len(files))
	}
	for _, f := range files {
		name := filepath.Base(f)
		t.Run(name, func(t *testing.T) {
			doc, data := loadHMS(t, name)
			m := Consolidate(HMSObservations(doc, prov(name, AuthHMS)))
			n := m.Noun(ID{doc.Namespace, doc.Name})
			if n == nil {
				t.Fatalf("noun missing from model")
			}
			out, err := ExportHMS(n, false)
			if err != nil {
				t.Fatal(err)
			}
			var want, got any
			if err := json.Unmarshal(data, &want); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("export is not JSON: %v\n%s", err, out)
			}
			if !reflect.DeepEqual(want, got) {
				t.Errorf("round trip differs\nwant %v\ngot  %v", want, got)
			}
		})
	}
}

func TestHMSAttributesKeepFileOrderAndAliases(t *testing.T) {
	t.Parallel()
	doc, _ := loadHMS(t, "transform_instance.hms")
	if doc.Attributes[0].Name != "instance_name" || doc.Attributes[1].Name != "status" {
		t.Fatalf("attributes not in file order: %v, %v", doc.Attributes[0].Name, doc.Attributes[1].Name)
	}
	m := Consolidate(HMSObservations(doc, prov("ti.hms", AuthHMS)))
	n := m.Noun(ID{"hmd_lang_transform", "transform_instance"})
	if !n.Authoritative || n.Metatype != MetaNoun {
		t.Fatalf("noun = %+v", n)
	}
	if a := n.Attribute("status"); a.Type != Enum || len(a.EnumDef) != 10 || a.Required == nil || !*a.Required {
		t.Errorf("status = %+v", a)
	}

	doc, _ = loadHMS(t, "noun_b.hms")
	m = Consolidate(HMSObservations(doc, prov("noun_b.hms", AuthHMS)))
	for _, a := range m.Nouns[0].Attributes {
		if a.HMSType == "int" && a.Type != Integer {
			t.Errorf("int alias maps to %s, want integer", a.Type)
		}
	}
}

// Only an attribute nothing typed blocks a valid .hms export: a DATE column
// is already timestamp in the core, with DATE kept as a perspective value.
func TestExportRefusesOnlyUnknownTypes(t *testing.T) {
	t.Parallel()
	n := &Noun{ID: ID{"ntc", "x"}, Metatype: MetaNoun, Attributes: []*Attribute{
		{Name: "export_date", Type: Timestamp},
		{Name: "derived", Type: Unknown},
	}}
	_, err := ExportHMS(n, false)
	var ee *ExportError
	if !errors.As(err, &ee) || len(ee.Attributes) != 1 || !strings.Contains(ee.Attributes[0], "derived") {
		t.Fatalf("err = %v", err)
	}
	out, err := ExportHMS(n, true)
	if err != nil || !strings.Contains(string(out), `"derived": {"type":"string"}`) ||
		!strings.Contains(string(out), `"export_date": {"type":"timestamp"}`) {
		t.Fatalf("lossy export = %s, %v", out, err)
	}
}

func datatype(id, def string) Value { return Value{Value: id, Definition: def} }

// layered is a two-layer table the way the NS transforms build one, as
// bindings of a trino perspective.
func layered() []Observation {
	var obs []Observation
	id := ID{"billing", "aws_billing"}
	for _, layer := range []struct {
		name    string
		primary bool
		types   []Value
		core    []LogicalType
	}{
		{"staging", false, []Value{datatype("varchar", "VARCHAR"), datatype("varchar", "VARCHAR"), datatype("varchar", "VARCHAR")},
			[]LogicalType{String, String, String}},
		{"final", true, []Value{datatype("varchar", "VARCHAR"), datatype("double", "DOUBLE"), datatype("varchar", "VARCHAR")},
			[]LogicalType{String, Float, String}},
	} {
		p := prov("ddl_"+layer.name+".yaml", AuthDDL)
		b := &BindingObs{Perspective: "trino", Name: layer.name, Primary: layer.primary, Values: map[string]Value{
			"schema_name": V("billing_" + layer.name), "table_name": V("aws_billing"), "partitioned_by": V("year"),
		}}
		obs = append(obs, Observation{Kind: KindBinding, Subject: id, Provenance: p, Binding: b})
		for i, col := range []string{"identity_line_item_id", "cost", "year"} {
			obs = append(obs, Observation{Kind: KindAttribute, Subject: id, Provenance: p, Attr: &AttrObs{
				Name: col, Binding: b.Key(), Position: i + 1, Type: layer.core[i],
				Values: map[string]Value{"datatype": layer.types[i], "is_partition": V(col == "year")},
			}})
		}
	}
	return obs
}

func TestConsolidateLayersIntoOneNoun(t *testing.T) {
	t.Parallel()
	m := Consolidate(layered())
	if len(m.Nouns) != 1 {
		t.Fatalf("nouns = %d", len(m.Nouns))
	}
	n := m.Nouns[0]
	if len(n.Bindings) != 2 || n.Bindings[0].Label() != "trino:final" || n.Bindings[0].Location.String() != "billing_final.aws_billing" {
		t.Fatalf("bindings = %+v", n.Bindings)
	}
	// Attributes come from the primary (final) binding, core types only.
	if a := n.Attribute("cost"); a == nil || a.Type != Float {
		t.Errorf("cost = %+v", a)
	}
	if col := n.Binding("trino", "final").Column("cost"); col.Values["datatype"].Definition != "DOUBLE" {
		t.Errorf("final cost datatype = %+v", col.Values)
	}
	var info bool
	for _, d := range m.Disagreements {
		if d.Code == "layer-type-change" && d.Severity == SevInfo && d.Subject == "billing.aws_billing#cost" {
			info = true
		} else if d.Severity != SevInfo {
			t.Errorf("unexpected disagreement %v", d)
		}
	}
	if !info {
		t.Errorf("a cross-layer type change should be reported as information: %v", m.Disagreements)
	}
}

// The order observations arrive in must not change the model.
func TestConsolidateIsOrderIndependent(t *testing.T) {
	t.Parallel()
	obs := layered()
	a, _ := json.Marshal(Consolidate(obs))
	for i, j := 0, len(obs)-1; i < j; i, j = i+1, j-1 {
		obs[i], obs[j] = obs[j], obs[i]
	}
	b, _ := json.Marshal(Consolidate(obs))
	if string(a) != string(b) {
		t.Errorf("model depends on observation order\n%s\n%s", a, b)
	}
}

func TestInsertThatDisagreesWithCreateIsReported(t *testing.T) {
	t.Parallel()
	obs := layered()
	p := prov("03-staging-to-final.yaml", AuthInsertSelect)
	for i, col := range []string{"identity_line_item_id", "cost"} {
		obs = append(obs, Observation{Kind: KindAttribute, Subject: ID{"billing", "aws_billing"}, Provenance: p,
			Attr: &AttrObs{Name: col, Binding: "trino:final", Position: i + 1, Type: Unknown}})
	}
	m := Consolidate(obs)
	var found bool
	for _, d := range m.Disagreements {
		if d.Code == "column-count" && d.Severity == SevError {
			found = true
			if !strings.Contains(d.Message, "lists 3 columns") || !strings.Contains(d.Message, "lists 2") {
				t.Errorf("message = %s", d.Message)
			}
		}
	}
	if !found {
		t.Fatalf("column-count disagreement missing: %v", m.Disagreements)
	}
}

// A dbt source naming the same physical table as a transform's DDL is the
// same noun; the DDL's identity wins because it is stated with more authority.
func TestSharedLocationLinksAcrossInspectors(t *testing.T) {
	t.Parallel()
	ddl := ID{"ntc", "ntc_instances_export"}
	src := ID{"ntc_final", "ntc_instances_export"}
	mdl := ID{"reporting", "staging_transform_instance"}
	obs := []Observation{
		{Kind: KindBinding, Subject: ddl, Provenance: prov("ddl03.yaml", AuthDDL), Binding: &BindingObs{
			Perspective: "trino", Name: "final", Primary: true,
			Values: map[string]Value{"schema_name": V("ntc_final"), "table_name": V("ntc_instances_export")}}},
		{Kind: KindBinding, Subject: src, Provenance: prov("schema.yml", AuthInferred), Binding: &BindingObs{
			Perspective: "dbt", Name: "source:ntc_final", Reference: true,
			Values: map[string]Value{"database": V("hive"), "schema_name": V("ntc_final"), "table_name": V("ntc_instances_export")}}},
		{Kind: KindBinding, Subject: mdl, Provenance: prov("staging.sql", AuthDbtSQL), Binding: &BindingObs{
			Perspective: "dbt", Name: "model", Scope: "dbt:reporting",
			Values: map[string]Value{"table_name": V("staging_transform_instance")}}},
		{Kind: KindScope, Provenance: prov("04-dbt.yaml", AuthDDL), Scope: &ScopeObs{Scope: "dbt:reporting", Schema: "ntc"}},
		{Kind: KindLineage, Provenance: prov("staging.sql", AuthDbtSQL), Lineage: &LineageObs{
			From: Endpoint{ID: src}, To: Endpoint{ID: mdl}, Via: "dbt-source"}},
		{Kind: KindReference, Provenance: prov("views.yml", AuthDbtYAML), Named: &NamedObs{
			Kind: "table", Key: "ntc.staging_transform_instance", Via: "dbt source"}},
		{Kind: KindReference, Provenance: prov("views.yml", AuthDbtYAML), Named: &NamedObs{
			Kind: "table", Key: "ntc.nothing_makes_this", Via: "dbt source"}},
	}
	m := Consolidate(obs)
	n := m.Noun(ddl)
	if n == nil || len(n.Bindings) != 2 || len(n.Aliases) != 1 || n.Aliases[0].ID != src {
		t.Fatalf("noun = %+v", n)
	}
	if !strings.Contains(n.Aliases[0].Reason, "same physical table ntc_final.ntc_instances_export") {
		t.Errorf("reason = %q", n.Aliases[0].Reason)
	}
	if len(m.Lineage) != 1 || m.Lineage[0].From != "ntc.ntc_instances_export" {
		t.Errorf("lineage = %+v", m.Lineage)
	}
	// The scope put the dbt model in schema ntc, which is now also its
	// schema_name value; the first reference resolves.
	if b := m.Noun(mdl).Binding("dbt", "model"); b.Values["schema_name"].String() != "ntc" {
		t.Errorf("scoped binding = %+v", b)
	}
	var unresolved []string
	for _, d := range m.Disagreements {
		if d.Code == "unresolved-reference" {
			unresolved = append(unresolved, d.Subject)
		}
	}
	if !reflect.DeepEqual(unresolved, []string{"ntc.nothing_makes_this"}) {
		t.Errorf("unresolved = %v", unresolved)
	}
	// Consolidation filled in locations on copies, not on the caller's data.
	if obs[2].Binding.Location.Schema != "" {
		t.Errorf("caller's observation was mutated")
	}
}

func TestNotNullTestMakesAttributeRequired(t *testing.T) {
	t.Parallel()
	id := ID{"dbt_r", "dim_status"}
	p := prov("schema.yml", AuthDbtYAML)
	obs := []Observation{
		{Kind: KindBinding, Subject: id, Provenance: p, Binding: &BindingObs{Perspective: "dbt", Name: "model"}},
		{Kind: KindAttribute, Subject: id, Provenance: p, Attr: &AttrObs{Name: "status_id", Binding: "dbt:model", Position: 1, Type: Unknown}},
		{Kind: KindConstraint, Subject: id, Provenance: p, Constraint: &ConstraintObs{Attribute: "status_id", Constraint: "not_null"}},
		{Kind: KindConstraint, Subject: id, Provenance: p, Constraint: &ConstraintObs{Attribute: "ghost", Constraint: "unique"}},
	}
	m := Consolidate(obs)
	a := m.Nouns[0].Attribute("status_id")
	if a.Required == nil || !*a.Required {
		t.Errorf("status_id required = %v", a.Required)
	}
	if len(m.Disagreements) != 1 || m.Disagreements[0].Code != "constraint-unknown-attribute" {
		t.Errorf("disagreements = %v", m.Disagreements)
	}
}

func TestDuplicateHMSDefinitionIsReported(t *testing.T) {
	t.Parallel()
	doc, _ := loadHMS(t, "environment.hms")
	a := HMSObservations(doc, Provenance{Repo: "hmd-lang-nsreporting", File: "src/schemas/environment.hms"})
	b := HMSObservations(doc, Provenance{Repo: "other", File: "environment.hms"})
	m := Consolidate(append(a, b...))
	if len(m.Disagreements) != 1 || m.Disagreements[0].Code != "duplicate-definition" {
		t.Fatalf("disagreements = %v", m.Disagreements)
	}
	if len(m.Nouns[0].Attributes) != 1 {
		t.Errorf("attributes duplicated: %d", len(m.Nouns[0].Attributes))
	}
}

// A noun's trino bindings export as one sidecar and read back as the same
// bindings; read at .hms authority beside contradicting DDL, the declared
// value wins and the contradiction is reported.
func TestSidecarRoundTripAndDeclaredValuesWin(t *testing.T) {
	t.Parallel()
	m := Consolidate(layered())
	n := m.Nouns[0]
	data, ok := ExportSidecar(n, "trino", "layer")
	if !ok || !strings.Contains(string(data), `"bindings"`) || !strings.Contains(string(data), `"layer": {`) {
		t.Fatalf("sidecar = %s", data)
	}
	if _, ok := ExportSidecar(n, "dbt", "binding"); ok {
		t.Error("a perspective the noun has no binding of exports nothing")
	}

	back, err := SidecarObservations(data, "trino", "layer", prov("aws_billing.trino.hms", AuthHMS))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, o := range back {
		if o.Kind == KindBinding {
			names = append(names, o.Binding.Key())
		}
	}
	if strings.Join(names, ",") != "trino:final,trino:staging" {
		t.Fatalf("bindings read back = %v", names)
	}
	round := Consolidate(back).Nouns[0]
	if c := round.Binding("trino", "final").Column("cost"); c == nil || c.Values["datatype"].Definition != "DOUBLE" {
		t.Errorf("read back cost = %+v", c)
	}

	// Declare cost DECIMAL in the sidecar; the DDL still says DOUBLE.
	declared := `{"namespace": "billing", "name": "aws_billing", "bindings": [
		{"layer": {"value": "final"}, "attributes": {"cost": {"datatype": {"value": "decimal", "definition": "DECIMAL"}}}}]}`
	side, err := SidecarObservations([]byte(declared), "trino", "layer", prov("aws_billing.trino.hms", AuthHMS))
	if err != nil {
		t.Fatal(err)
	}
	mixed := Consolidate(append(layered(), side...))
	col := mixed.Nouns[0].Binding("trino", "final").Column("cost")
	if col.Values["datatype"].Value != "decimal" {
		t.Errorf("declared value should win: %+v", col.Values["datatype"])
	}
	var conflict bool
	for _, d := range mixed.Disagreements {
		if d.Code == "perspective-value-conflict" && d.Subject == "billing.aws_billing#cost@trino:final" {
			conflict = true
		}
	}
	if !conflict {
		t.Errorf("conflict not reported: %v", mixed.Disagreements)
	}
}
