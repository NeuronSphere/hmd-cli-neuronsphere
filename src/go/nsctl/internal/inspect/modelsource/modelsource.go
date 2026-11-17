// Package modelsource inspects NEP051 source files, src/sources/*.source.yaml
// (NERD032 SPEC001 as NERD039 SPEC007 amends it). A source says which
// concepts a table, a Librarian content item type or an nsctl data file group
// yields, and which column holds each attribute. Its authority follows its
// provenance: declared is a person's decision (90), inferred is a proposal
// (10), observed sits between.
//
// A source that yields exactly one noun stands for that noun's table, so its
// binding carries the table's location and links the concept to whatever
// else describes that table (an nsctl data group, a CREATE TABLE). A source
// that yields several nouns from one row (an order line yields a customer, an
// order and a product) links nothing by location: no one of them is the
// table.
package modelsource

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/inspect"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/model"
)

// Dir is where a model repository keeps its sources.
const Dir = "src/sources"

// Perspective is the binding a source gives each concept it yields.
const Perspective = "source"

// GroupCatalog is the catalog an nsctl data file group's location is in; its
// schema is the workspace (NERD039 SPEC007).
const GroupCatalog = "nsdata"

// Authority of a source with each provenance.
func Authority(prov string) model.Authority {
	switch prov {
	case Declared:
		return model.AuthSourceDeclared
	case Observed:
		return model.AuthInsertSelect
	}
	return model.AuthInferred
}

// Inspector is the NEP051 source inspector.
type Inspector struct{}

func (Inspector) Name() string { return "modelsource" }

func (Inspector) CanInspect(_ context.Context, src inspect.Source) bool {
	info, err := fs.Stat(src.FS, Dir)
	return err == nil && info.IsDir()
}

var identRE = regexp.MustCompile(`[^a-z0-9_]+`)

// Namespace is the namespace an unqualified concept falls back to: the
// repository's name as an identifier. A workspace's model is inspected under
// the workspace's name.
func Namespace(repo string) string {
	return strings.Trim(identRE.ReplaceAllString(strings.ToLower(repo), "_"), "_")
}

func (Inspector) Inspect(_ context.Context, src inspect.Source) ([]model.Observation, error) {
	files, err := inspect.Walk(src, Dir, func(p string) bool { return strings.HasSuffix(p, ".source.yaml") })
	if err != nil {
		return nil, err
	}
	concepts, err := conceptIndex(src)
	if err != nil {
		return nil, err
	}
	ns := Namespace(src.Repo)
	var obs []model.Observation
	for _, file := range files {
		b, err := fs.ReadFile(src.FS, file)
		if err != nil {
			return nil, err
		}
		f, err := Parse(b)
		if err != nil {
			obs = append(obs, finding(file, "source-unreadable", err.Error()))
			continue
		}
		if want := strings.TrimSuffix(path.Base(file), ".source.yaml"); f.Source != want {
			obs = append(obs, finding(file, "source-name-mismatch",
				fmt.Sprintf("source %q must equal its file name, %q", f.Source, want)))
		}
		obs = append(obs, inspectFile(file, string(b), f, ns, concepts)...)
	}
	return obs, nil
}

func inspectFile(file, text string, f *File, ns string, concepts map[string][]model.ID) []model.Observation {
	auth := Authority(f.Prov())
	conf := model.Decided
	if f.Prov() == Inferred {
		conf = model.Evidence
	}
	resolve := func(name string) model.ID {
		if strings.Contains(name, ".") {
			return model.ParseID(name)
		}
		if ids := concepts[name]; len(ids) == 1 {
			return ids[0]
		}
		return model.ID{Namespace: ns, Name: name}
	}
	nouns := 0
	for _, y := range f.Yields {
		if !y.IsRelationship() {
			nouns++
		}
	}
	values := map[string]model.Value{"source": model.V(f.Source)}
	switch {
	case f.From.Group != "":
		values["group"] = model.V(f.From.Group)
		if nouns == 1 {
			values["catalog"], values["schema_name"], values["table_name"] = model.V(GroupCatalog), model.V(ns), model.V(f.From.Group)
		}
	case f.From.Table != "":
		values["table"] = model.V(f.From.Table)
		if parts := strings.Split(f.From.Table, "."); nouns == 1 && len(parts) == 3 {
			values["catalog"], values["schema_name"], values["table_name"] = model.V(parts[0]), model.V(parts[1]), model.V(parts[2])
		}
	case f.From.CIT != "":
		values["content_item_type"] = model.V(f.From.CIT)
	}

	var obs []model.Observation
	for _, y := range f.Yields {
		id := resolve(y.ConceptName())
		p := model.Provenance{File: file, Line: inspect.LineOf(text, "\n  "+y.Key+":"), Authority: auth, Confidence: conf,
			Why: "provenance: " + f.Prov()}
		no := &model.NounObs{Metatype: model.MetaNoun}
		if y.IsRelationship() {
			no.Metatype = model.MetaRelationship
			no.RefFrom = resolve(f.Yields.Get(y.From).ConceptName()).String()
			no.RefTo = resolve(f.Yields.Get(y.To).ConceptName()).String()
		}
		obs = append(obs, model.Observation{Kind: model.KindNoun, Subject: id, Provenance: p, Noun: no})
		bv := map[string]model.Value{}
		for k, v := range values {
			// Only the one noun a source stands for gets the location.
			if y.IsRelationship() && (k == "catalog" || k == "schema_name" || k == "table_name") {
				continue
			}
			bv[k] = v
		}
		bo := &model.BindingObs{Perspective: Perspective, Name: f.Source, Values: bv}
		obs = append(obs, model.Observation{Kind: model.KindBinding, Subject: id, Provenance: p, Binding: bo})
		for i, m := range y.Maps {
			t := model.Unknown
			if m.Column != "" {
				t = LogicalType(f.Columns.Type(m.Column))
			}
			obs = append(obs, model.Observation{Kind: model.KindAttribute, Subject: id, Provenance: p,
				Attr: &model.AttrObs{Name: m.Attribute, Binding: bo.Key(), Position: i + 1, Type: t}})
		}
	}
	return obs
}

// conceptIndex maps each concept name in the repository's .hms schemas to
// its identities, so an unqualified yield resolves to the concept it names.
func conceptIndex(src inspect.Source) (map[string][]model.ID, error) {
	out := map[string][]model.ID{}
	if _, err := fs.Stat(src.FS, "src/schemas"); err != nil {
		return out, nil
	}
	files, err := inspect.Walk(src, "src/schemas", func(p string) bool {
		base := path.Base(p)
		// A sidecar is <name>.<perspective>.hms; only the schema names a concept.
		return strings.HasSuffix(base, ".hms") && strings.Count(base, ".") == 1
	})
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		ns := path.Base(path.Dir(f))
		name := strings.TrimSuffix(path.Base(f), ".hms")
		out[name] = append(out[name], model.ID{Namespace: ns, Name: name})
	}
	return out, nil
}

// LogicalType is the .hms type nearest a Trino column type.
func LogicalType(trino string) model.LogicalType {
	t := strings.ToLower(strings.TrimSpace(trino))
	if i := strings.IndexByte(t, '('); i >= 0 {
		t = t[:i]
	}
	switch t {
	case "":
		return model.Unknown
	case "varchar", "char", "uuid", "json":
		return model.String
	case "tinyint", "smallint", "integer", "bigint":
		return model.Integer
	case "real", "double", "decimal":
		return model.Float
	case "boolean":
		return model.Bool
	case "date", "timestamp", "time":
		return model.Timestamp
	case "varbinary":
		return model.Blob
	case "array":
		return model.Collection
	case "map", "row":
		return model.Mapping
	}
	return model.Unknown
}

func finding(file, code, msg string) model.Observation {
	return model.Observation{Kind: model.KindFinding,
		Finding:    &model.FindingObs{Severity: model.SevWarning, Code: code, Message: msg},
		Provenance: model.Provenance{File: file, Authority: model.AuthInferred, Confidence: model.Evidence}}
}
