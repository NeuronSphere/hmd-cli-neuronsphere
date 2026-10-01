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

func TestDiffReportsSemanticChanges(t *testing.T) {
	t.Parallel()
	before := layered()
	after := layered()
	for i := range after {
		a := after[i].Attr
		if a == nil {
			continue
		}
		// The primary (final) layer's cost becomes a string; the staging
		// layer gains a column.
		if a.Name == "cost" && a.Manifestation == "trino:billing_final.aws_billing" {
			a.Type, a.PhysicalType = String, "varchar"
		}
	}
	after = append(after, Observation{Kind: KindAttribute, Subject: ID{"billing", "aws_billing"},
		Provenance: prov("ddl_staging.yaml", AuthDDL),
		Attr:       &AttrObs{Name: "region", Manifestation: "trino:billing_staging.aws_billing", Position: 4, Type: String, PhysicalType: "varchar"}})
	got := changes(Diff(Consolidate(before), Consolidate(after)))
	for _, want := range []string{
		"billing.aws_billing.cost  type: float -> string",
		"billing.aws_billing@billing_staging.aws_billing.region  added: string (varchar)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	// The final layer is the noun's attributes, so its change is not repeated
	// as a column change.
	if strings.Contains(got, "@billing_final.aws_billing.cost") {
		t.Errorf("primary layer change reported twice:\n%s", got)
	}
}

func TestDiffHMSAttributeAdded(t *testing.T) {
	t.Parallel()
	doc, _ := loadHMS(t, "environment.hms")
	before := Consolidate(HMSObservations(doc, prov("environment.hms", AuthHMS)))
	doc.Attributes = append(doc.Attributes, HMSAttribute{Name: "region", Type: "string", Required: BoolPtr(false)})
	after := Consolidate(HMSObservations(doc, prov("environment.hms", AuthHMS)))
	got := changes(Diff(before, after))
	if got != "hmd_lang_nsreporting.environment.region  added: string, not required" {
		t.Errorf("diff = %q", got)
	}
}
