package perspective

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/model"
)

func TestEmbeddedDefinitionsUseTheModelerShape(t *testing.T) {
	t.Parallel()
	r := Default()
	if got := strings.Join(r.Names(), ","); got != "dbt,librarian-content,postgres-view,trino" {
		t.Fatalf("names = %s", got)
	}
	trino := r.Get("trino")
	if trino.BindingKey != "layer" || trino.Origin != "embedded" {
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

func TestRepositoryDefinitionOverridesEmbedded(t *testing.T) {
	t.Parallel()
	r := Default()
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

func TestSQLDatatype(t *testing.T) {
	t.Parallel()
	trino := Default().Get("trino")
	for physical, want := range map[string]struct {
		id, render string
		core       model.LogicalType
	}{
		"varchar":                     {"varchar", "VARCHAR", model.String},
		"VARCHAR(255)":                {"varchar", "VARCHAR(255)", model.String},
		"double":                      {"double", "DOUBLE", model.Float},
		"DATE":                        {"date", "DATE", model.Timestamp},
		"decimal(10, 2)":              {"decimal", "DECIMAL(10,2)", model.Float},
		"timestamp(3) with time zone": {"timestamp", "TIMESTAMP(3)", model.Timestamp},
		"int":                         {"integer", "INTEGER", model.Integer},
		"array(varchar)":              {"array", "ARRAY(VARCHAR)", model.Collection},
		"geometry":                    {"geometry", "GEOMETRY", model.Unknown},
	} {
		v, core := SQLDatatype(trino, physical)
		if v.String() != want.id || core != want.core || RenderSQL(v) != want.render {
			t.Errorf("%q: value %+v core %s render %s; want %+v", physical, v, core, RenderSQL(v), want)
		}
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
			{Perspective: "ansi-sql"}}},
	}}
	got := map[string]string{}
	for _, d := range Validate(m, Default()) {
		got[d.Code+" "+d.Subject] = d.Message
	}
	for _, want := range []string{
		"perspective-undeclared-key s.t@trino:final",
		"perspective-enum-value s.t#b@trino:final",
		"perspective-parameter s.t#c@trino:final",
		"perspective-unknown s.t@ansi-sql",
	} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %s in %v", want, got)
		}
	}
	if len(got) != 4 {
		t.Errorf("validations = %v", got)
	}
}
