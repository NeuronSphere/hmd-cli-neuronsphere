package perspective

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/model"
)

// fixtures is the definitions under testdata: the Modeler's ansi-sql, and
// the four the spike once embedded (spike-*), kept as reference data.
func fixtures(t *testing.T) *Registry {
	t.Helper()
	r := New()
	if errs := r.LoadDir(os.DirFS("testdata"), ".", "testdata"); len(errs) > 0 {
		t.Fatal(errs)
	}
	return r
}

func TestNothingIsEmbedded(t *testing.T) {
	t.Parallel()
	if names := New().Names(); len(names) != 0 {
		t.Errorf("a new registry has %v", names)
	}
}

func TestSpikeDefinitionsUseTheModelerShape(t *testing.T) {
	t.Parallel()
	r := fixtures(t)
	if got := strings.Join(r.Names(), ","); got != "ansi-sql,dbt,librarian-content,postgres-view,trino" {
		t.Fatalf("names = %s", got)
	}
	trino := r.Get("trino")
	if trino.BindingKey != "layer" || trino.Origin != "testdata/spike-trino.perspective.json" {
		t.Errorf("trino = %+v", trino)
	}
	dt, ok := trino.Extension(Attribute, "datatype")
	if !ok || dt.ExtensionType != "enum" {
		t.Fatalf("datatype = %+v", dt)
	}
	for _, ev := range dt.EnumValues {
		if _, ok := model.HMSType(ev.HMSType); !ok {
			t.Errorf("enum value %s has hms_type %q, not an .hms type", ev.ID, ev.HMSType)
		}
	}
	// Entity extensions are declared for nouns and relationships alike.
	if _, ok := trino.Extension(Noun, "table_name"); !ok {
		t.Error("table_name not visible at the noun attach point")
	}
	if _, ok := trino.Extension(Attribute, "table_name"); ok {
		t.Error("table_name visible at the attribute attach point")
	}
	if trino.TypeKey() != "datatype" {
		t.Errorf("type key = %q", trino.TypeKey())
	}
}

// The Modeler's own ansi-sql definition, as hmd-ms-mickey serves it, parses:
// the shape is the Modeler's, not a variant of it.
func TestModelerDefinitionParses(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join("testdata", "ansi-sql.perspective.json"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "ansi-sql" || d.BindingKey != "" {
		t.Errorf("definition = %+v", d)
	}
	if ext, ok := d.Extension(Attribute, "datatype"); !ok || len(ext.EnumValues) < 8 {
		t.Errorf("datatype = %+v", ext)
	}
}

func TestRepositoryDefinitionReplacesOneOfTheSameName(t *testing.T) {
	t.Parallel()
	r := New()
	r.Add(&Definition{Name: "trino", Display: "Derived", Origin: OriginDerived})
	fsys := fstest.MapFS{
		"src/perspectives/trino.perspective.json": {Data: []byte(`{"perspective_name": "trino", "perspective_display": "Ours",
			"entity_extensions": [], "noun_extensions": [], "relationship_extensions": [], "attribute_extensions": []}`)},
		"src/perspectives/broken.perspective.json": {Data: []byte(`{`)},
	}
	errs := r.LoadDir(fsys, Dir, "my-repo")
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "my-repo/src/perspectives/broken.perspective.json") {
		t.Errorf("errors = %v", errs)
	}
	if d := r.Get("trino"); d.Display != "Ours" || d.Origin != "my-repo/src/perspectives/trino.perspective.json" {
		t.Errorf("trino = %+v", d)
	}
}

// Parameter order and aliases are data, so writing a value back needs no
// knowledge of SQL.
func TestPhysicalFollowsParameterPositionsAndAliases(t *testing.T) {
	t.Parallel()
	d := &Definition{Name: "x"}
	d.Declare(Attribute, "datatype", Extension{ExtensionType: "enum", EnumValues: []EnumValue{
		{ID: "decimal", Definition: "DECIMAL", HMSType: "float", Aliases: []string{"numeric"}, Parameters: map[string]Parameter{
			"scale": {Position: 2}, "precision": {Position: 1}}},
	}})
	v := model.Value{Value: "decimal", Definition: "DECIMAL", Parameters: map[string]any{"precision": 10, "scale": 2}}
	if got := d.Physical("datatype", v); got != "DECIMAL(10,2)" {
		t.Errorf("physical = %s", got)
	}
	ext, _ := d.Extension(Attribute, "datatype")
	if ev, ok := ext.EnumValue("numeric"); !ok || ev.ID != "decimal" {
		t.Errorf("alias = %+v", ev)
	}
	if CoreType(d, map[string]model.Value{"datatype": model.V("numeric")}) != model.Float {
		t.Error("an alias does not give the core type")
	}
}

func TestEditsReplayAndReportStale(t *testing.T) {
	t.Parallel()
	d := &Definition{Name: "trino", Display: "trino", BindingKey: "layer",
		NamePattern: &NamePattern{Schema: []NamePart{{Ref: "namespace"}, {Lit: "_"}, {Ref: "layer"}}}}
	d.Declare(Entity, "layer", Extension{ExtensionType: "enum"})
	d.Declare(Entity, "format", Extension{ExtensionType: "enum"})
	d.Declare(Entity, "template", Extension{ExtensionType: "short_text"})
	d.Declare(Attribute, "datatype", Extension{ExtensionType: "enum", EnumValues: []EnumValue{{ID: "geometry"}}})
	dv := &Derivation{Definition: d, Status: StatusDerived}
	var edits []Edit
	for _, args := range [][]string{
		{"trino", "rename-key", "format", "storage"},
		{"trino", "rename-key", "storage", "storage_format"},
		{"trino", "rename-key", "layer", "zone"},
		{"trino", "drop-key", "template"},
		{"trino", "hms-type", "geometry", "blob"},
		{"trino", "rename", "warehouse"},
		{"warehouse", "drop-key", "no_such_key"},
	} {
		e, err := ParseEdit(args[0], args[1], args[2:])
		if err != nil {
			t.Fatal(err)
		}
		edits = append(edits, e)
	}
	renames, dropped, stale := dv.Apply(edits)
	if dv.Status != StatusEdited || d.Name != "warehouse" || d.BindingKey != "zone" || d.NamePattern.Schema[2].Ref != "zone" {
		t.Errorf("definition = %+v", d)
	}
	if renames["format"] != "storage_format" || renames["layer"] != "zone" || len(renames) != 2 || !dropped["template"] {
		t.Errorf("renames = %v dropped = %v", renames, dropped)
	}
	if len(stale) != 1 || stale[0].Key != "no_such_key" {
		t.Errorf("stale = %+v", stale)
	}
	ext, _ := d.Extension(Attribute, "datatype")
	if ext.EnumValues[0].HMSType != "blob" {
		t.Errorf("hms-type edit not applied: %+v", ext)
	}
	if _, err := ParseEdit("trino", "hms-type", []string{"geometry", "shape"}); err == nil {
		t.Error("hms-type accepted a type .hms does not have")
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()
	b := &model.Binding{Perspective: "trino", Name: "final", Values: map[string]model.Value{
		"table_name": model.V("t"), "colour": model.V("red"), "format": model.V("parquet"),
	}, Columns: []model.Column{
		{Name: "a", Values: map[string]model.Value{"datatype": {Value: "varchar", Parameters: map[string]any{"max_length": 9}}}},
		{Name: "b", Values: map[string]model.Value{"datatype": {Value: "geometry"}}},
		{Name: "c", Values: map[string]model.Value{"datatype": {Value: "date", Parameters: map[string]any{"precision": 3}}}},
	}}
	m := &model.Model{Nouns: []*model.Noun{
		{ID: model.ID{Namespace: "s", Name: "t"}, Metatype: model.MetaNoun, Bindings: []*model.Binding{b,
			{Perspective: "unheard-of"}}},
	}}
	got := map[string]string{}
	for _, d := range Validate(m, fixtures(t)) {
		got[d.Code+" "+d.Subject] = d.Message
	}
	for _, want := range []string{
		"perspective-undeclared-key s.t@trino:final",
		"perspective-enum-value s.t#b@trino:final",
		"perspective-parameter s.t#c@trino:final",
		"perspective-unknown s.t@unheard-of",
	} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %s in %v", want, got)
		}
	}
	if len(got) != 4 {
		t.Errorf("validations = %v", got)
	}
}
