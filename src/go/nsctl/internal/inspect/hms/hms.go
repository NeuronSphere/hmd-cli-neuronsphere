// Package hms inspects an HMD language pack: its .hms schemas under
// src/schemas (the highest-authority source of the model), the Postgres views
// generated from them under src/postgres, the copies packaged under
// src/python/<pkg>/schemas, and the display attribute of *.ui.hms extensions.
//
// Everything that compares those artifacts with each other is a language
// pack convention, so it lives here and reaches the model as findings.
package hms

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/inspect"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/inspect/sqlddl"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/model"
)

const schemasDir = "src/schemas"

// Inspector is the language pack inspector.
type Inspector struct{}

func (Inspector) Name() string { return "hms" }

func (Inspector) CanInspect(_ context.Context, src inspect.Source) bool {
	info, err := fs.Stat(src.FS, schemasDir)
	return err == nil && info.IsDir()
}

// systemColumns are the columns every generated view has besides the noun's
// attributes.
var systemColumns = map[string]model.LogicalType{
	"id": model.String, "created_at": model.Timestamp, "updated_at": model.Timestamp,
	"from_id": model.String, "to_id": model.String,
}

func (Inspector) Inspect(_ context.Context, src inspect.Source) ([]model.Observation, error) {
	files, err := inspect.Walk(src, schemasDir, func(p string) bool { return strings.HasSuffix(p, ".hms") })
	if err != nil {
		return nil, err
	}
	var obs []model.Observation
	docs := map[model.ID]*model.HMSDoc{}
	var ui []string
	for _, f := range files {
		if isExtension(f) {
			ui = append(ui, f)
			continue
		}
		data, err := fs.ReadFile(src.FS, f)
		if err != nil {
			return nil, err
		}
		doc, err := model.ParseHMS(data)
		if err != nil {
			obs = append(obs, finding(f, model.SevError, "unparseable-schema", err.Error()))
			continue
		}
		prov := model.Provenance{File: f, Line: 1, Why: ".hms schema"}
		obs = append(obs, model.HMSObservations(doc, prov)...)
		id := model.ID{Namespace: doc.Namespace, Name: doc.Name}
		docs[id] = doc
		obs = append(obs, checkPath(f, doc)...)
		obs = append(obs, checkTypes(f, doc)...)
	}
	obs = append(obs, checkUI(src, ui, docs)...)
	views, err := inspectViews(src, docs)
	if err != nil {
		return nil, err
	}
	obs = append(obs, views...)
	packaged, err := checkPackagedCopies(src, docs)
	if err != nil {
		return nil, err
	}
	return append(obs, packaged...), nil
}

var extensionRE = regexp.MustCompile(`\.[a-zA-Z\-_]+\.hms$`)

// isExtension matches hmd-schema-loader's rule for <name>.<ext>.hms.
func isExtension(p string) bool { return extensionRE.MatchString(path.Base(p)) }

func finding(file string, sev model.Severity, code, msg string) model.Observation {
	return model.Observation{
		Kind:       model.KindFinding,
		Finding:    &model.FindingObs{Severity: sev, Code: code, Message: msg},
		Provenance: model.Provenance{File: file, Line: 1, Authority: model.AuthHMS, Confidence: model.Decided},
	}
}

// checkPath applies hmd-schema-loader's rule: namespace and name must match
// the file's path under src/schemas.
func checkPath(file string, doc *model.HMSDoc) []model.Observation {
	rel := strings.TrimSuffix(strings.TrimPrefix(file, schemasDir+"/"), ".hms")
	want := strings.Join(append(strings.Split(doc.Namespace, "."), doc.Name), "/")
	if doc.Namespace == "" {
		want = doc.Name
	}
	if rel == want {
		return nil
	}
	o := finding(file, model.SevError, "schema-path-mismatch",
		fmt.Sprintf("declares %s.%s but lives at %s; hmd-schema-loader refuses it", doc.Namespace, doc.Name, rel))
	o.Subject = model.ID{Namespace: doc.Namespace, Name: doc.Name}
	return []model.Observation{o}
}

// checkTypes reports attribute types the HMD Python runtime does not accept,
// including aliases ("int", "boolean") the model itself normalises.
func checkTypes(file string, doc *model.HMSDoc) []model.Observation {
	var out []model.Observation
	runtime := map[string]bool{"string": true, "integer": true, "float": true, "enum": true, "bool": true,
		"timestamp": true, "epoch": true, "collection": true, "mapping": true, "blob": true}
	for _, a := range doc.Attributes {
		if runtime[a.Type] && a.EnumKey != "enum" {
			continue
		}
		msg := fmt.Sprintf("attribute %s has type %q, which hmd-meta-types rejects", a.Name, a.Type)
		if a.EnumKey == "enum" {
			msg = fmt.Sprintf("attribute %s lists its values under \"enum\"; hmd-meta-types only enforces \"enum_def\"", a.Name)
		}
		o := finding(file, model.SevWarning, "runtime-type", msg)
		o.Subject = model.ID{Namespace: doc.Namespace, Name: doc.Name}
		out = append(out, o)
	}
	return out
}

func checkUI(src inspect.Source, files []string, docs map[model.ID]*model.HMSDoc) []model.Observation {
	var out []model.Observation
	for _, f := range files {
		data, err := fs.ReadFile(src.FS, f)
		if err != nil {
			continue
		}
		doc, err := model.ParseHMS(data)
		if err != nil {
			continue
		}
		raw, ok := doc.Extra["display"]
		if !ok {
			continue
		}
		display := strings.Trim(string(raw), `"`)
		base := strings.TrimSuffix(path.Base(f), ".hms")
		base = base[:strings.LastIndex(base, ".")]
		id := model.ID{Namespace: doc.Namespace, Name: base}
		main, ok := docs[id]
		if !ok || hasAttribute(main, display) {
			continue
		}
		o := finding(f, model.SevWarning, "ui-display-unknown",
			fmt.Sprintf("displays %q, which is not an attribute of %s", display, id))
		o.Subject = id
		out = append(out, o)
	}
	return out
}

func hasAttribute(doc *model.HMSDoc, name string) bool {
	if _, ok := systemColumns[name]; ok || name == "identifier" || name == "_created" || name == "_updated" {
		return true
	}
	for _, a := range doc.Attributes {
		if a.Name == name {
			return true
		}
	}
	return false
}

var viewNounRE = regexp.MustCompile(`name\s*=\s*'([^']+)'`)

// inspectViews reads the generated Postgres views: each is a manifestation of
// its noun, and a noun without one, or a view whose columns differ from the
// schema's attributes, is a sign the DDL was not regenerated.
func inspectViews(src inspect.Source, docs map[model.ID]*model.HMSDoc) ([]model.Observation, error) {
	files, err := inspect.Walk(src, "src/postgres", func(p string) bool { return strings.HasSuffix(p, ".sql") })
	if err != nil || len(files) == 0 {
		return nil, nil
	}
	var obs []model.Observation
	viewed := map[model.ID]bool{}
	for _, f := range files {
		data, err := fs.ReadFile(src.FS, f)
		if err != nil {
			return nil, err
		}
		text := string(data)
		for _, st := range sqlddl.Parse(text) {
			if st.Kind != sqlddl.CreateView {
				continue
			}
			stmtText := statementText(text, st.Line)
			m := viewNounRE.FindStringSubmatch(stmtText)
			if m == nil {
				continue
			}
			id := model.ParseID(m[1])
			if viewed[id] {
				continue // the concatenated *_views.sql repeats each view
			}
			viewed[id] = true
			key := "postgres-view:" + st.Name.String()
			prov := model.Provenance{File: f, Line: st.Line, Authority: model.AuthDDL, Confidence: model.Decided,
				Why: "view generated from the .hms schema"}
			obs = append(obs, model.Observation{Kind: model.KindManifestation, Subject: id, Provenance: prov,
				Manifest: &model.ManifestObs{Key: key, Tech: "postgres-view", Location: model.Location{Table: st.Name.Table()}}})
			// An attribute is projected out of the content document
			// (content -> 'x'); anything else is one of the entity table's own
			// columns. Deciding by expression, not by name, is what catches
			// an attribute that shares a system column's name.
			var cols []string
			names := map[string]int{}
			for i, it := range st.Select {
				t, phys := model.Unknown, "jsonb"
				if strings.Contains(it.Expr, "content") && strings.Contains(it.Expr, ">") {
					cols = append(cols, it.Name)
				} else if sys, ok := systemColumns[it.Name]; ok {
					t, phys = sys, ""
				}
				names[it.Name]++
				obs = append(obs, model.Observation{Kind: model.KindAttribute, Subject: id, Provenance: prov,
					Attr: &model.AttrObs{Name: it.Name, Manifestation: key, Position: i + 1, Type: t, PhysicalType: phys}})
			}
			for _, name := range sortedNames(names) {
				if names[name] < 2 {
					continue
				}
				o := finding(f, model.SevError, "view-duplicate-column",
					fmt.Sprintf("view %s selects %s %d times; Postgres refuses to create it. The attribute collides with the entity table's own %s column",
						st.Name, name, names[name], name))
				o.Subject, o.Provenance.Line = id, st.Line
				obs = append(obs, o)
			}
			if doc, ok := docs[id]; ok {
				var want []string
				for _, a := range doc.Attributes {
					want = append(want, a.Name)
				}
				sort.Strings(want)
				sort.Strings(cols)
				if !reflect.DeepEqual(want, cols) && !(len(want) == 0 && len(cols) == 0) {
					o := finding(f, model.SevError, "view-attributes-differ",
						fmt.Sprintf("view %s projects %v but the schema declares %v", st.Name, cols, want))
					o.Subject, o.Provenance.Line = id, st.Line
					obs = append(obs, o)
				}
			}
		}
	}
	for _, id := range sortedIDs(docs) {
		if viewed[id] {
			continue
		}
		o := finding("src/postgres", model.SevWarning, "no-generated-view",
			fmt.Sprintf("%s has a schema but no generated Postgres view; the DDL was not regenerated", id))
		o.Subject = id
		obs = append(obs, o)
	}
	return obs, nil
}

// statementText is the text from a statement's first line to its semicolon.
func statementText(text string, line int) string {
	lines := strings.Split(text, "\n")
	if line < 1 || line > len(lines) {
		return ""
	}
	rest := strings.Join(lines[line-1:], "\n")
	if i := strings.Index(rest, ";"); i >= 0 {
		return rest[:i]
	}
	return rest
}

func sortedNames(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedIDs(docs map[model.ID]*model.HMSDoc) []model.ID {
	out := make([]model.ID, 0, len(docs))
	for id := range docs {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// checkPackagedCopies compares src/schemas with the copies a Python build
// places under src/python/*/schemas. A checked-in copy that lags its source
// ships a model the schema no longer describes.
func checkPackagedCopies(src inspect.Source, docs map[model.ID]*model.HMSDoc) ([]model.Observation, error) {
	copies, err := inspect.Walk(src, "src/python", func(p string) bool {
		return strings.HasSuffix(p, ".hms") && strings.Contains(p, "/schemas/") && !isExtension(p)
	})
	if err != nil || len(copies) == 0 {
		return nil, nil
	}
	packaged := map[model.ID]*model.HMSDoc{}
	where := map[model.ID]string{}
	dir := ""
	for _, f := range copies {
		data, err := fs.ReadFile(src.FS, f)
		if err != nil {
			continue
		}
		doc, err := model.ParseHMS(data)
		if err != nil {
			continue
		}
		id := model.ID{Namespace: doc.Namespace, Name: doc.Name}
		packaged[id], where[id] = doc, f
		dir = f[:strings.Index(f, "/schemas/")+len("/schemas")]
	}
	var obs []model.Observation
	for _, id := range sortedIDs(docs) {
		cp, ok := packaged[id]
		if !ok {
			o := finding(dir, model.SevWarning, "packaged-copy-missing",
				fmt.Sprintf("%s is not in the packaged schemas under %s", id, dir))
			o.Subject = id
			obs = append(obs, o)
			continue
		}
		if !reflect.DeepEqual(attrNames(cp), attrNames(docs[id])) {
			o := finding(where[id], model.SevWarning, "packaged-copy-stale",
				fmt.Sprintf("packaged copy lists attributes %v, the schema %v", attrNames(cp), attrNames(docs[id])))
			o.Subject = id
			obs = append(obs, o)
		}
	}
	return obs, nil
}

func attrNames(doc *model.HMSDoc) []string {
	var out []string
	for _, a := range doc.Attributes {
		out = append(out, a.Name+":"+a.Type)
	}
	return out
}
