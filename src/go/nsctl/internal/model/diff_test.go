package model

import (
	"strings"
	"testing"
)

func changes(cs []Change) string {
	var lines []string
	for _, c := range cs {
		lines = append(lines, c.String())
	}
	return strings.Join(lines, "\n")
}

func TestDiffIdenticalModelsIsEmpty(t *testing.T) {
	t.Parallel()
	a, b := Consolidate(layered()), Consolidate(layered())
	if cs := Diff(a, b); len(cs) != 0 {
		t.Errorf("changes:\n%s", changes(cs))
	}
}

func TestDiffReportsCoreAndPerspectiveChanges(t *testing.T) {
	t.Parallel()
	before := layered()
	after := layered()
	for i := range after {
		a := after[i].Attr
		if a != nil && a.Name == "cost" && a.Binding == "trino:final" {
			a.Type = String
			a.Values = map[string]Value{"datatype": datatype("varchar", "VARCHAR"), "is_partition": V(false)}
		}
	}
	after = append(after, Observation{Kind: KindAttribute, Subject: ID{"billing", "aws_billing"},
		Provenance: prov("ddl_staging.yaml", AuthDDL),
		Attr: &AttrObs{Name: "region", Binding: "trino:staging", Position: 4, Type: String,
			Values: map[string]Value{"datatype": datatype("varchar", "VARCHAR")}}})
	got := changes(Diff(Consolidate(before), Consolidate(after)))
	for _, want := range []string{
		"billing.aws_billing#cost  PropertyTypeChanged: float -> string",
		"billing.aws_billing#cost@trino:final  PerspectiveValueChanged datatype: DOUBLE -> VARCHAR",
		"billing.aws_billing#region@trino:staging  PerspectiveValueAdded datatype: VARCHAR",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
}

func TestDiffParametersAreValues(t *testing.T) {
	t.Parallel()
	a := []Observation{{Kind: KindBinding, Subject: ID{"s", "t"}, Provenance: prov("a", AuthDDL),
		Binding: &BindingObs{Perspective: "trino", Values: map[string]Value{"schema_name": V("s")}}},
		{Kind: KindAttribute, Subject: ID{"s", "t"}, Provenance: prov("a", AuthDDL), Attr: &AttrObs{Name: "amt", Binding: "trino", Position: 1, Type: Float,
			Values: map[string]Value{"datatype": {Value: "decimal", Definition: "DECIMAL", Parameters: map[string]any{"precision": 10, "scale": 2}}}}}}
	b := []Observation{a[0], {Kind: KindAttribute, Subject: ID{"s", "t"}, Provenance: prov("a", AuthDDL), Attr: &AttrObs{Name: "amt", Binding: "trino", Position: 1, Type: Float,
		Values: map[string]Value{"datatype": {Value: "decimal", Definition: "DECIMAL", Parameters: map[string]any{"precision": 12, "scale": 2}}}}}}
	got := changes(Diff(Consolidate(a), Consolidate(b)))
	if got != "s.t#amt@trino  PerspectiveValueChanged datatype: DECIMAL(precision=10,scale=2) -> DECIMAL(precision=12,scale=2)" {
		t.Errorf("diff = %q", got)
	}
}

func TestDiffIgnoresAFindingThatOnlyMoved(t *testing.T) {
	t.Parallel()
	a := []Disagreement{{Severity: SevInfo, Code: "unused-run-param", Subject: "repo/ddl.yaml:190", Message: "x"}}
	b := []Disagreement{{Severity: SevInfo, Code: "unused-run-param", Subject: "repo/ddl.yaml:191", Message: "x"}}
	if cs := Diff(&Model{Disagreements: a}, &Model{Disagreements: b}); len(cs) != 0 {
		t.Errorf("changes:\n%s", changes(cs))
	}
}

func TestDiffHMSAttributeAdded(t *testing.T) {
	t.Parallel()
	doc, _ := loadHMS(t, "environment.hms")
	before := Consolidate(HMSObservations(doc, prov("environment.hms", AuthHMS)))
	doc.Attributes = append(doc.Attributes, HMSAttribute{Name: "region", Type: "string", Required: BoolPtr(false)})
	after := Consolidate(HMSObservations(doc, prov("environment.hms", AuthHMS)))
	got := changes(Diff(before, after))
	if got != "hmd_lang_nsreporting.environment#region  PropertyAdded: string, not required" {
		t.Errorf("diff = %q", got)
	}
}
