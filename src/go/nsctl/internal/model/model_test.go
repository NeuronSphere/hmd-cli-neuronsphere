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

func TestExportRefusesExtensionTypes(t *testing.T) {
	t.Parallel()
	n := &Noun{ID: ID{"ntc", "x"}, Metatype: MetaNoun, Attributes: []*Attribute{
		{Name: "export_date", Type: Date},
		{Name: "name", Type: String},
	}}
	_, err := ExportHMS(n, false)
	var ee *ExportError
	if !errors.As(err, &ee) || len(ee.Attributes) != 1 || !strings.Contains(ee.Attributes[0], "export_date") {
		t.Fatalf("err = %v", err)
	}
	out, err := ExportHMS(n, true)
	if err != nil || !strings.Contains(string(out), `"export_date": {"type":"string"}`) {
		t.Fatalf("lossy export = %s, %v", out, err)
	}
}

func TestSQLTypeMapping(t *testing.T) {
	t.Parallel()
	for physical, want := range map[string]LogicalType{
		"varchar": String, "VARCHAR(255)": String, "double": Float, "timestamp(3)": Timestamp,
		"TIMESTAMP": Timestamp, "DATE": Date, "bigint": Integer, "decimal(10,2)": Decimal,
		"boolean": Bool, "array(varchar)": Collection, "geometry": Unknown,
	} {
		if got := SQLType(physical); got != want {
			t.Errorf("SQLType(%q) = %s, want %s", physical, got, want)
		}
	}
}

// layered is a three-layer table the way the NS transforms build one.
func layered() []Observation {
	var obs []Observation
	id := ID{"billing", "aws_billing"}
	for _, layer := range []struct {
		name    string
		primary bool
		types   []string
	}{
		{"staging", false, []string{"varchar", "varchar", "varchar"}},
		{"final", true, []string{"varchar", "double", "varchar"}},
	} {
		key := "trino:billing_" + layer.name + ".aws_billing"
		p := prov("ddl_"+layer.name+".yaml", AuthDDL)
		obs = append(obs, Observation{Kind: KindManifestation, Subject: id, Provenance: p, Manifest: &ManifestObs{
			Key: key, Tech: "trino-table", Location: Location{Schema: "billing_" + layer.name, Table: "aws_billing"},
			Layer: layer.name, Primary: layer.primary, Partitions: []string{"year"},
		}})
		for i, col := range []string{"identity_line_item_id", "cost", "year"} {
			obs = append(obs, Observation{Kind: KindAttribute, Subject: id, Provenance: p, Attr: &AttrObs{
				Name: col, Manifestation: key, Position: i + 1,
				PhysicalType: layer.types[i], Type: SQLType(layer.types[i]),
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
	if len(n.Manifestations) != 2 {
		t.Fatalf("manifestations = %d", len(n.Manifestations))
	}
	// Attributes come from the primary (final) layer.
	if a := n.Attribute("cost"); a == nil || a.Type != Float {
		t.Errorf("cost = %+v", a)
	}
	if a := n.Attribute("year"); a == nil || !a.Partition {
		t.Errorf("year = %+v", a)
	}
	var info bool
	for _, d := range m.Disagreements {
		if d.Code == "layer-type-change" && d.Severity == SevInfo && strings.HasSuffix(d.Subject, ".cost") {
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
	key := "trino:billing_final.aws_billing"
	p := prov("03-staging-to-final.yaml", AuthInsertSelect)
	for i, col := range []string{"identity_line_item_id", "cost"} {
		obs = append(obs, Observation{Kind: KindAttribute, Subject: ID{"billing", "aws_billing"}, Provenance: p,
			Attr: &AttrObs{Name: col, Manifestation: key, Position: i + 1, Type: Unknown}})
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
	model := ID{"dbt_reporting", "staging_transform_instance"}
	obs := []Observation{
		{Kind: KindManifestation, Subject: ddl, Provenance: prov("ddl03.yaml", AuthDDL), Manifest: &ManifestObs{
			Key: "trino:ntc_final.ntc_instances_export", Tech: "trino-table",
			Location: Location{Schema: "ntc_final", Table: "ntc_instances_export"}, Layer: "final", Primary: true}},
		{Kind: KindManifestation, Subject: src, Provenance: prov("schema.yml", AuthDbtYAML), Manifest: &ManifestObs{
			Key: "dbt-source:ntc_final.ntc_instances_export", Tech: "dbt-source",
			Location: Location{Catalog: "hive", Schema: "ntc_final", Table: "ntc_instances_export"}}},
		{Kind: KindManifestation, Subject: model, Provenance: prov("staging.sql", AuthDbtSQL), Manifest: &ManifestObs{
			Key: "dbt-model:staging_transform_instance", Tech: "dbt-model", Scope: "dbt:reporting",
			Location: Location{Table: "staging_transform_instance"}}},
		{Kind: KindBinding, Provenance: prov("04-dbt.yaml", AuthDDL), Binding: &BindingObs{Scope: "dbt:reporting", Schema: "ntc"}},
		{Kind: KindLineage, Provenance: prov("staging.sql", AuthDbtSQL), Lineage: &LineageObs{
			From: Endpoint{ID: src}, To: Endpoint{ID: model}, Via: "dbt-source"}},
		{Kind: KindReference, Provenance: prov("views.yml", AuthDbtYAML), Named: &NamedObs{
			Kind: "table", Key: "ntc.staging_transform_instance", Via: "dbt source"}},
		{Kind: KindReference, Provenance: prov("views.yml", AuthDbtYAML), Named: &NamedObs{
			Kind: "table", Key: "ntc.nothing_makes_this", Via: "dbt source"}},
	}
	m := Consolidate(obs)
	n := m.Noun(ddl)
	if n == nil || len(n.Manifestations) != 2 || len(n.Aliases) != 1 || n.Aliases[0].ID != src {
		t.Fatalf("noun = %+v", n)
	}
	if !strings.Contains(n.Aliases[0].Reason, "same physical table ntc_final.ntc_instances_export") {
		t.Errorf("reason = %q", n.Aliases[0].Reason)
	}
	if len(m.Lineage) != 1 || m.Lineage[0].From != "ntc.ntc_instances_export" {
		t.Errorf("lineage = %+v", m.Lineage)
	}
	// The binding put the dbt model in schema ntc, so the first reference
	// resolves and only the second is reported.
	var unresolved []string
	for _, d := range m.Disagreements {
		if d.Code == "unresolved-reference" {
			unresolved = append(unresolved, d.Subject)
		}
	}
	if !reflect.DeepEqual(unresolved, []string{"ntc.nothing_makes_this"}) {
		t.Errorf("unresolved = %v", unresolved)
	}
}

func TestNotNullTestMakesAttributeRequired(t *testing.T) {
	t.Parallel()
	id := ID{"dbt_r", "dim_status"}
	p := prov("schema.yml", AuthDbtYAML)
	obs := []Observation{
		{Kind: KindManifestation, Subject: id, Provenance: p, Manifest: &ManifestObs{Key: "dbt-model:dim_status", Tech: "dbt-model"}},
		{Kind: KindAttribute, Subject: id, Provenance: p, Attr: &AttrObs{Name: "status_id", Manifestation: "dbt-model:dim_status", Position: 1, Type: Unknown}},
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
