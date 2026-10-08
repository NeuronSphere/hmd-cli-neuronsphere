package derive

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/inspect"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/inspect/nstransform"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/model"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/perspective"
)

// observe runs the transform inspector over the named fixtures of
// internal/inspect/nstransform: verbatim src/transforms of
// hmd-config-billing-transforms and hmd-config-transform-reporting.
func observe(t *testing.T, fixtures ...string) []model.Observation {
	t.Helper()
	var srcs []inspect.Source
	for _, f := range fixtures {
		dir := filepath.Join("..", "inspect", "nstransform", "testdata", f)
		srcs = append(srcs, inspect.Source{Root: dir, Repo: f, FS: os.DirFS(dir)})
	}
	obs, reports := inspect.Run(context.Background(), srcs, []inspect.Inspector{nstransform.Inspector{}})
	for _, r := range reports {
		if r.Error != "" {
			t.Fatal(r.Error)
		}
	}
	return obs
}

func derivation(t *testing.T, res Result, name string) *perspective.Derivation {
	t.Helper()
	for _, dv := range res.Derivations {
		if dv.Definition.Name == name {
			return dv
		}
	}
	t.Fatalf("no derivation %q in %d", name, len(res.Derivations))
	return nil
}

// dump is the consolidated model as sorted lines: every binding with its
// values, every column with its values, every attribute, lineage edge and
// disagreement.
func dump(m *model.Model) string {
	var lines []string
	val := func(v model.Value) string {
		p, _ := json.Marshal(v.Parameters)
		return fmt.Sprintf("%v(%s)%s", v.Value, v.Definition, p)
	}
	values := func(vs map[string]model.Value) string {
		var out []string
		for k, v := range vs {
			out = append(out, k+"="+val(v))
		}
		sort.Strings(out)
		return strings.Join(out, " ")
	}
	for _, n := range m.Nouns {
		for _, b := range n.Bindings {
			lines = append(lines, fmt.Sprintf("B %s @%s primary=%v ref=%v loc=%s %s", n.ID, b.Label(), b.Primary, b.Reference, b.Location, values(b.Values)))
			for _, c := range b.Columns {
				lines = append(lines, fmt.Sprintf("C %s @%s %s type=%s %s", n.ID, b.Label(), c.Name, c.Type, values(c.Values)))
			}
		}
		for _, a := range n.Attributes {
			req := "?"
			if a.Required != nil {
				req = fmt.Sprint(*a.Required)
			}
			lines = append(lines, fmt.Sprintf("A %s %s type=%s req=%s", n.ID, a.Name, a.Type, req))
		}
	}
	for _, e := range m.Lineage {
		lines = append(lines, fmt.Sprintf("L %s -> %s %s", e.From, e.To, e.Via))
	}
	for _, d := range m.Disagreements {
		lines = append(lines, fmt.Sprintf("D %s %s %s", d.Severity, d.Code, d.Subject))
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n") + "\n"
}

// The golden files are the model the spike's hand-written trino inspector
// produced, with its embedded definition, before derivation existed. A
// derived perspective must give exactly that model: every noun, binding,
// value, column type, lineage edge and disagreement.
func TestDerivationReproducesTheSpikesModel(t *testing.T) {
	t.Parallel()
	for _, f := range []string{"billing", "reporting"} {
		want, err := os.ReadFile(filepath.Join("testdata", f+".model.txt"))
		if err != nil {
			t.Fatal(err)
		}
		got := dump(model.Consolidate(Run(observe(t, f), perspective.New(), nil).Observations))
		if got != string(want) {
			t.Errorf("%s: model differs from the spike's\n--- got\n%s", f, got)
		}
	}
}

// NERD033 SPEC003 acceptance: derived from both corpora, the trino
// definition agrees with the one the spike embedded wherever the files say
// anything: the binding key and its values, each key's attach point and
// kind, each enum value, and each data type's core .hms type.
func TestDerivedTrinoAgreesWithTheSpikeDefinition(t *testing.T) {
	t.Parallel()
	res := Run(observe(t, "billing", "reporting"), perspective.New(), nil)
	got := derivation(t, res, "trino").Definition
	data, err := os.ReadFile(filepath.Join("..", "perspective", "testdata", "spike-trino.perspective.json"))
	if err != nil {
		t.Fatal(err)
	}
	spike, err := perspective.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.BindingKey != spike.BindingKey || got.Origin != perspective.OriginDerived {
		t.Errorf("binding key %q (origin %s), spike %q", got.BindingKey, got.Origin, spike.BindingKey)
	}
	layer, _ := got.Extension(perspective.Entity, "layer")
	var layers []string
	for _, v := range layer.EnumValues {
		layers = append(layers, v.ID)
	}
	if strings.Join(layers, ",") != "source,staging,final,ux" {
		t.Errorf("layers in data-flow order = %v", layers)
	}
	for _, at := range []perspective.Attach{perspective.Entity, perspective.Attribute} {
		for _, k := range got.Keys(at) {
			mine, _ := got.Extension(at, k)
			theirs, ok := spike.Extension(at, k)
			if !ok {
				t.Errorf("%s key %s is not in the spike definition", at, k)
				continue
			}
			// The spike wrote the binding key as text; derivation knows its values.
			if mine.ExtensionType != theirs.ExtensionType && k != got.BindingKey {
				t.Errorf("%s.%s is %s, spike %s", at, k, mine.ExtensionType, theirs.ExtensionType)
			}
			for _, ev := range mine.EnumValues {
				if k == got.BindingKey {
					continue
				}
				sv, ok := theirs.EnumValue(ev.ID)
				if !ok {
					t.Errorf("%s.%s value %s is not in the spike definition", at, k, ev.ID)
				} else if sv.HMSType != ev.HMSType {
					t.Errorf("%s.%s=%s has hms_type %q, spike %q", at, k, ev.ID, ev.HMSType, sv.HMSType)
				}
			}
		}
	}
	for _, k := range []string{"layer", "format", "partitioned_by", "table_name", "table_type", "template", "external_location"} {
		if _, ok := got.Extension(perspective.Entity, k); !ok {
			t.Errorf("entity key %s not derived", k)
		}
	}
	if tn, _ := got.Extension(perspective.Entity, "table_name"); fmt.Sprint(tn.Default) != "map[ref:name]" {
		t.Errorf("table_name default = %v", tn.Default)
	}
	if got.NamePattern == nil || len(got.NamePattern.Schema) != 3 || got.NamePattern.Schema[2].Ref != "layer" {
		t.Errorf("name pattern = %+v", got.NamePattern)
	}
	// Every piece has evidence, and none of this corpus needs review.
	dv := derivation(t, res, "trino")
	items := map[string]bool{}
	for _, e := range dv.Evidence {
		items[e.Item] = true
		if e.Review {
			t.Errorf("%s flagged for review: %+v", e.Item, e)
		}
	}
	for _, want := range []string{"binding_key", "name_pattern", "primary", "entity.format", "attribute.datatype", "attribute.datatype=varchar"} {
		if !items[want] {
			t.Errorf("no evidence for %s", want)
		}
	}
}

// Edits are replayed on every derivation, and the values follow them.
func TestEditsFollowIntoValues(t *testing.T) {
	t.Parallel()
	edits := []perspective.Edit{
		{Perspective: "trino", Op: "rename-key", Key: "format", To: "storage_format"},
		{Perspective: "trino", Op: "rename", To: "warehouse"},
		{Perspective: "warehouse", Op: "drop-key", Key: "external_location"},
	}
	res := Run(observe(t, "billing"), perspective.New(), edits)
	if len(res.Stale) != 0 {
		t.Errorf("stale = %+v", res.Stale)
	}
	dv := derivation(t, res, "warehouse")
	if dv.Status != perspective.StatusEdited {
		t.Errorf("status = %s", dv.Status)
	}
	m := model.Consolidate(res.Observations)
	n := m.Noun(model.ID{Namespace: "billing", Name: "aws_billing"})
	final, source := n.Binding("warehouse", "final"), n.Binding("warehouse", "source")
	if final == nil || final.Values["storage_format"].String() != "parquet" || final.Values["format"].Value != nil {
		t.Fatalf("final = %+v", final)
	}
	if _, ok := source.Values["external_location"]; ok {
		t.Errorf("dropped key still has a value: %+v", source.Values)
	}
}

// A definition a repository declares is used as it stands: nothing is
// derived for it, and its name pattern decides identity.
func TestDeclaredDefinitionIsUsedNotDerived(t *testing.T) {
	t.Parallel()
	reg := perspective.New()
	decl := &perspective.Definition{Name: "trino", BindingKey: "zone", NamePattern: &perspective.NamePattern{
		Schema: []perspective.NamePart{{Ref: "namespace"}, {Lit: "_"}, {Ref: "zone"}}}}
	decl.Declare(perspective.Entity, "zone", perspective.Extension{ExtensionType: "enum",
		EnumValues: []perspective.EnumValue{{ID: "staging"}, {ID: "final"}}})
	reg.Add(decl)
	res := Run(observe(t, "billing"), reg, nil)
	for _, dv := range res.Derivations {
		if dv.Definition.Name == "trino" {
			t.Fatalf("trino derived despite a declared definition")
		}
	}
	m := model.Consolidate(res.Observations)
	// Only staging and final are zones: source and ux schemas are their own
	// namespaces now.
	n := m.Noun(model.ID{Namespace: "billing", Name: "aws_billing"})
	if n == nil || n.Binding("trino", "final") == nil || n.Binding("trino", "staging") == nil {
		t.Fatalf("billing.aws_billing = %+v", n)
	}
	if m.Noun(model.ID{Namespace: "billing_ux", Name: "aws_billing"}) == nil {
		t.Errorf("billing_ux.aws_billing missing")
	}
	// What the files say that the declared definition has no key for is
	// reported once per property, not given a value it would reject.
	var undeclared []string
	for _, d := range m.Disagreements {
		if d.Code == "perspective-key-undeclared" {
			undeclared = append(undeclared, d.Message)
		}
	}
	if len(undeclared) == 0 || !strings.Contains(strings.Join(undeclared, "\n"), `named "format" have no key in trino`) {
		t.Errorf("undeclared = %v", undeclared)
	}
	if v := n.Binding("trino", "final").Values; len(v) != 0 {
		t.Errorf("values outside the declared definition: %v", v)
	}
}

// A renamed key keeps its old name as an alias, so a materialised
// definition still reads the property its parser names the old way.
func TestDeclaredDefinitionReadsThroughKeyAliases(t *testing.T) {
	t.Parallel()
	res := Run(observe(t, "billing"), perspective.New(), []perspective.Edit{
		{Perspective: "trino", Op: "rename-key", Key: "format", To: "storage_format"}})
	reg := perspective.New()
	reg.Add(derivation(t, res, "trino").Definition)
	m := model.Consolidate(Run(observe(t, "billing"), reg, nil).Observations)
	final := m.Noun(model.ID{Namespace: "billing", Name: "aws_billing"}).Binding("trino", "final")
	if final.Values["storage_format"].String() != "parquet" {
		t.Errorf("final values = %v", final.Values)
	}
	for _, d := range m.Disagreements {
		if d.Code == "perspective-key-undeclared" {
			t.Errorf("undeclared: %s", d.Message)
		}
	}
}

// One table alone shows no varying schema segment, so it binds without a
// layer, in its own schema's namespace, and is primary.
func TestSingleTableHasNoLayer(t *testing.T) {
	t.Parallel()
	obj := model.Observation{Kind: model.KindObject, Provenance: model.Provenance{File: "a.yaml", Authority: model.AuthDDL},
		Object: &model.ObjectObs{Dialect: "trino", Location: model.Location{Schema: "demo_final", Table: "thing"},
			Props: map[string]model.Prop{"table_name": {Value: "thing", Class: model.ClassIdent}},
			Columns: []model.ObjectColumn{{Name: "at", Position: 1, Props: map[string]model.Prop{
				"datatype": {Value: "DATE", Class: model.ClassType, Type: &model.PhysType{Raw: "DATE", Base: "date", Category: "date"}}}}}}}
	res := Run([]model.Observation{obj}, perspective.New(), nil)
	m := model.Consolidate(res.Observations)
	n := m.Noun(model.ID{Namespace: "demo_final", Name: "thing"})
	if n == nil || len(n.Bindings) != 1 || n.Bindings[0].Name != "" || !n.Bindings[0].Primary {
		t.Fatalf("noun = %+v", n)
	}
	if a := n.Attribute("at"); a == nil || a.Type != model.Timestamp {
		t.Errorf("at = %+v", a)
	}
	if derivation(t, res, "trino").Definition.BindingKey != "" {
		t.Error("a binding key from one table")
	}
}

// Where an .hms attribute and a column are the same attribute, the .hms type
// decides the data type's core type, over the SQL category's.
func TestHMSPairingDecidesCoreType(t *testing.T) {
	t.Parallel()
	id := model.ID{Namespace: "demo_final", Name: "thing"}
	obs := []model.Observation{
		{Kind: model.KindAttribute, Subject: id, Provenance: model.Provenance{File: "thing.hms", Authority: model.AuthHMS},
			Attr: &model.AttrObs{Name: "at", Type: model.String}},
		{Kind: model.KindObject, Provenance: model.Provenance{File: "a.yaml", Authority: model.AuthDDL},
			Object: &model.ObjectObs{Dialect: "trino", Location: model.Location{Schema: "demo_final", Table: "thing"},
				Columns: []model.ObjectColumn{{Name: "at", Position: 1, Props: map[string]model.Prop{
					"datatype": {Value: "DATE", Class: model.ClassType, Type: &model.PhysType{Raw: "DATE", Base: "date", Category: "date"}}}}}}},
	}
	dv := derivation(t, Run(obs, perspective.New(), nil), "trino")
	ext, _ := dv.Definition.Extension(perspective.Attribute, "datatype")
	if ev, _ := ext.EnumValue("date"); ev.HMSType != "string" {
		t.Errorf("date hms_type = %q", ev.HMSType)
	}
	for _, e := range dv.Evidence {
		if e.Item == "attribute.datatype=date" && !strings.Contains(e.Rule, "paired") {
			t.Errorf("evidence = %+v", e)
		}
	}
}

// A sidecar of a perspective nothing declares or derives is reported, not
// read.
func TestUnknownSidecarIsReported(t *testing.T) {
	t.Parallel()
	obs := []model.Observation{{Kind: model.KindSidecar, Provenance: model.Provenance{File: "x.colour.hms"},
		Sidecar: &model.SidecarObs{Perspective: "colour", Data: []byte(`{"name": "x"}`)}}}
	res := Run(obs, perspective.New(), nil)
	if len(res.Observations) != 1 || res.Observations[0].Finding == nil || res.Observations[0].Finding.Code != "unknown-extension" {
		t.Errorf("observations = %+v", res.Observations)
	}
}
