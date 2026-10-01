// Package dbt inspects dbt projects: dbt_project.yml, the sources, models,
// columns and tests declared in model-path YAML, and each model's SQL (its
// output columns, ref() and source() lineage, and materialization).
//
// Nothing here is NeuronSphere-specific. A model's identity is
// <project>.<model>; where it materialises is the project's target schema,
// which dbt only learns from a profile at run time, so manifestations carry
// the scope "dbt:<project>" for something else to bind to a schema.
package dbt

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/inspect"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/inspect/sqlddl"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/model"
)

// Inspector is the dbt inspector.
type Inspector struct{}

func (Inspector) Name() string { return "dbt" }

func (Inspector) CanInspect(_ context.Context, src inspect.Source) bool {
	files, _ := projects(src)
	return len(files) > 0
}

func projects(src inspect.Source) ([]string, error) {
	return inspect.Walk(src, ".", func(p string) bool { return path.Base(p) == "dbt_project.yml" })
}

type projectFile struct {
	Name       string         `yaml:"name"`
	ModelPaths []string       `yaml:"model-paths"`
	Models     map[string]any `yaml:"models"`
}

type schemaFile struct {
	Sources []struct {
		Name     string `yaml:"name"`
		Schema   string `yaml:"schema"`
		Database string `yaml:"database"`
		Tables   []struct {
			Name        string   `yaml:"name"`
			Description string   `yaml:"description"`
			Columns     []column `yaml:"columns"`
		} `yaml:"tables"`
	} `yaml:"sources"`
	Models []struct {
		Name        string   `yaml:"name"`
		Description string   `yaml:"description"`
		Columns     []column `yaml:"columns"`
	} `yaml:"models"`
}

type column struct {
	Name        string      `yaml:"name"`
	Description string      `yaml:"description"`
	DataType    string      `yaml:"data_type"`
	Tests       []yaml.Node `yaml:"tests"`
	DataTests   []yaml.Node `yaml:"data_tests"`
}

// testNames reads a column's tests: a bare name or a one-key mapping.
func (c column) testNames() []string {
	var out []string
	for _, n := range append(append([]yaml.Node(nil), c.Tests...), c.DataTests...) {
		switch n.Kind {
		case yaml.ScalarNode:
			out = append(out, n.Value)
		case yaml.MappingNode:
			if len(n.Content) > 0 {
				out = append(out, n.Content[0].Value)
			}
		}
	}
	return out
}

func (Inspector) Inspect(_ context.Context, src inspect.Source) ([]model.Observation, error) {
	files, err := projects(src)
	if err != nil {
		return nil, err
	}
	var obs []model.Observation
	for _, f := range files {
		o, err := inspectProject(src, f)
		if err != nil {
			return nil, err
		}
		obs = append(obs, o...)
	}
	return obs, nil
}

func prov(file string, line int, auth model.Authority, why string) model.Provenance {
	return model.Provenance{File: file, Line: line, Authority: auth, Confidence: model.Decided, Why: why}
}

func finding(file string, line int, subject model.ID, sev model.Severity, code, msg string) model.Observation {
	return model.Observation{Kind: model.KindFinding, Subject: subject,
		Finding: &model.FindingObs{Severity: sev, Code: code, Message: msg}, Provenance: prov(file, line, model.AuthDbtYAML, "")}
}

type modelDoc struct {
	file        string
	line        int
	description string
	columns     []column
}

func inspectProject(src inspect.Source, projectYML string) ([]model.Observation, error) {
	data, err := fs.ReadFile(src.FS, projectYML)
	if err != nil {
		return nil, err
	}
	var pf projectFile
	if err := yaml.Unmarshal(data, &pf); err != nil || pf.Name == "" {
		return []model.Observation{finding(projectYML, 1, model.ID{}, model.SevWarning, "unparseable-dbt-project", "dbt_project.yml has no name")}, nil
	}
	root := path.Dir(projectYML)
	if len(pf.ModelPaths) == 0 {
		pf.ModelPaths = []string{"models"}
	}
	project, scope := pf.Name, "dbt:"+pf.Name
	var obs []model.Observation
	docs := map[string]modelDoc{}
	var sqlFiles []string

	for _, mp := range pf.ModelPaths {
		dir := path.Join(root, mp)
		if _, err := fs.Stat(src.FS, dir); err != nil {
			continue
		}
		ymls, err := inspect.Walk(src, dir, func(p string) bool {
			return strings.HasSuffix(p, ".yml") || strings.HasSuffix(p, ".yaml")
		})
		if err != nil {
			return nil, err
		}
		for _, y := range ymls {
			o, err := inspectSchemaFile(src, y, docs)
			if err != nil {
				return nil, err
			}
			obs = append(obs, o...)
		}
		sqls, err := inspect.Walk(src, dir, func(p string) bool { return strings.HasSuffix(p, ".sql") })
		if err != nil {
			return nil, err
		}
		sqlFiles = append(sqlFiles, sqls...)
	}

	seen := map[string]bool{}
	for _, f := range sqlFiles {
		name := strings.TrimSuffix(path.Base(f), ".sql")
		seen[name] = true
		body, err := fs.ReadFile(src.FS, f)
		if err != nil {
			return nil, err
		}
		obs = append(obs, inspectModel(project, scope, f, string(body), docs[name], folderMaterialization(pf, root, f))...)
	}
	for _, name := range sortedKeys(docs) {
		if !seen[name] {
			d := docs[name]
			obs = append(obs, finding(d.file, d.line, model.ID{Namespace: project, Name: name}, model.SevWarning,
				"documented-model-missing", fmt.Sprintf("model %s is documented but has no SQL file", name)))
		}
	}
	return obs, nil
}

func inspectSchemaFile(src inspect.Source, file string, docs map[string]modelDoc) ([]model.Observation, error) {
	data, err := fs.ReadFile(src.FS, file)
	if err != nil {
		return nil, err
	}
	var sf schemaFile
	if err := yaml.Unmarshal(data, &sf); err != nil {
		return []model.Observation{finding(file, 1, model.ID{}, model.SevWarning, "unparseable-dbt-yaml", err.Error())}, nil
	}
	text := string(data)
	var obs []model.Observation
	for _, s := range sf.Sources {
		schema := s.Schema
		if schema == "" {
			schema = s.Name
		}
		for _, t := range s.Tables {
			id := model.ID{Namespace: s.Name, Name: t.Name}
			line := inspect.LineOf(text, "name: "+t.Name)
			key := "dbt-source:" + strings.ToLower(schema+"."+t.Name)
			// A source names a table something else owns: it links to that
			// table without providing it, and its identity is the weakest.
			p := prov(file, line, model.AuthInferred, "dbt source")
			obs = append(obs, model.Observation{Kind: model.KindManifestation, Subject: id, Provenance: p,
				Manifest: &model.ManifestObs{Key: key, Tech: "dbt-source", Reference: true,
					Location: model.Location{Catalog: s.Database, Schema: schema, Table: t.Name}}})
			obs = append(obs, model.Observation{Kind: model.KindReference, Provenance: prov(file, line, model.AuthDbtYAML, ""),
				Named: &model.NamedObs{Kind: "table", Key: strings.ToLower(schema + "." + t.Name), Via: "dbt source " + s.Name}})
			for i, c := range t.Columns {
				obs = append(obs, columnObs(id, key, file, text, i, c)...)
			}
		}
	}
	for _, m := range sf.Models {
		docs[m.Name] = modelDoc{file: file, line: inspect.LineOf(text, "name: "+m.Name), description: m.Description, columns: m.Columns}
	}
	return obs, nil
}

func columnObs(id model.ID, key, file, text string, i int, c column) []model.Observation {
	p := prov(file, inspect.LineOf(text, "name: "+c.Name), model.AuthDbtYAML, "documented column")
	a := &model.AttrObs{Name: c.Name, Manifestation: key, Position: i + 1, Type: model.Unknown}
	if c.DataType != "" {
		a.PhysicalType, a.Type = c.DataType, model.SQLType(c.DataType)
	}
	if c.Description != "" {
		a.Description = model.StrPtr(c.Description)
	}
	obs := []model.Observation{{Kind: model.KindAttribute, Subject: id, Attr: a, Provenance: p}}
	for _, test := range c.testNames() {
		obs = append(obs, model.Observation{Kind: model.KindConstraint, Subject: id, Provenance: p,
			Constraint: &model.ConstraintObs{Attribute: c.Name, Constraint: test}})
	}
	return obs
}

var (
	refRE          = regexp.MustCompile(`ref\(\s*['"]([^'"]+)['"]\s*(?:,\s*['"]([^'"]+)['"]\s*)?\)`)
	sourceRE       = regexp.MustCompile(`source\(\s*['"]([^'"]+)['"]\s*,\s*['"]([^'"]+)['"]\s*\)`)
	materializeRE  = regexp.MustCompile(`materialized\s*=\s*['"]([^'"]+)['"]`)
	ifBlockRE      = regexp.MustCompile(`(?s)\{%-?\s*if\b.*?%\}.*?\{%-?\s*endif\s*-?%\}`)
	jinjaStmtRE    = regexp.MustCompile(`(?s)\{%.*?%\}`)
	jinjaExprRE    = regexp.MustCompile(`(?s)\{\{.*?\}\}`)
	jinjaCommentRE = regexp.MustCompile(`(?s)\{#.*?#\}`)
)

func inspectModel(project, scope, file, body string, doc modelDoc, mat string) []model.Observation {
	name := strings.TrimSuffix(path.Base(file), ".sql")
	id := model.ID{Namespace: project, Name: name}
	key := "dbt-model:" + project + "." + name
	auth, why := model.AuthDbtSQL, "dbt model SQL"
	if doc.file != "" {
		auth, why = model.AuthDbtYAML, "dbt model, documented"
	}
	if m := materializeRE.FindStringSubmatch(body); m != nil {
		mat = m[1]
	}
	obs := []model.Observation{
		{Kind: model.KindManifestation, Subject: id, Provenance: prov(file, 1, auth, why),
			Manifest: &model.ManifestObs{Key: key, Tech: "dbt-model", Scope: scope, Format: mat, Primary: true,
				Location: model.Location{Table: name}}},
		{Kind: model.KindProvide, Provenance: prov(file, 1, model.AuthDbtSQL, ""),
			Named: &model.NamedObs{Kind: "dbt-model", Key: project + "." + name}},
	}
	if doc.description != "" {
		obs = append(obs, model.Observation{Kind: model.KindNoun, Subject: id, Provenance: prov(doc.file, doc.line, model.AuthDbtYAML, why),
			Noun: &model.NounObs{Metatype: model.MetaNoun, Description: model.StrPtr(doc.description)}})
	}

	for _, m := range refRE.FindAllStringSubmatchIndex(body, -1) {
		target := body[m[2]:m[3]]
		if m[4] >= 0 { // ref('package', 'model')
			target = body[m[4]:m[5]]
		}
		line := strings.Count(body[:m[0]], "\n") + 1
		from := model.ID{Namespace: project, Name: target}
		obs = append(obs,
			model.Observation{Kind: model.KindLineage, Provenance: prov(file, line, model.AuthDbtSQL, "ref()"),
				Lineage: &model.LineageObs{From: model.Endpoint{ID: from}, To: model.Endpoint{ID: id}, Via: "dbt-ref"}},
			model.Observation{Kind: model.KindReference, Provenance: prov(file, line, model.AuthDbtSQL, ""),
				Named: &model.NamedObs{Kind: "dbt-model", Key: project + "." + target, Via: "ref()"}})
	}
	for _, m := range sourceRE.FindAllStringSubmatchIndex(body, -1) {
		line := strings.Count(body[:m[0]], "\n") + 1
		from := model.ID{Namespace: body[m[2]:m[3]], Name: body[m[4]:m[5]]}
		obs = append(obs, model.Observation{Kind: model.KindLineage, Provenance: prov(file, line, model.AuthDbtSQL, "source()"),
			Lineage: &model.LineageObs{From: model.Endpoint{ID: from}, To: model.Endpoint{ID: id}, Via: "dbt-source"}})
	}

	// Output columns from the final SELECT, with Jinja reduced to something
	// the SQL subset parser reads: conditional blocks (is_incremental) go,
	// expressions become an identifier.
	plain := jinjaCommentRE.ReplaceAllString(body, "")
	plain = ifBlockRE.ReplaceAllStringFunc(plain, blankKeepLines)
	plain = jinjaStmtRE.ReplaceAllStringFunc(plain, blankKeepLines)
	plain = jinjaExprRE.ReplaceAllStringFunc(plain, func(s string) string {
		return "__jinja__" + strings.Repeat("\n", strings.Count(s, "\n"))
	})
	var produced []string
	if st, ok := sqlddl.FinalSelect(plain); ok && !(len(st.Select) == 1 && st.Select[0].Name == "*") {
		for i, it := range st.Select {
			if it.Name == "" || it.Name == "__jinja__" {
				continue
			}
			produced = append(produced, it.Name)
			obs = append(obs, model.Observation{Kind: model.KindAttribute, Subject: id,
				Provenance: prov(file, it.Line, model.AuthDbtSQL, "select list"),
				Attr:       &model.AttrObs{Name: it.Name, Manifestation: key, Position: i + 1, Type: model.Unknown}})
		}
	}
	obs = append(obs, compareDocumented(id, file, doc, produced)...)
	return obs
}

// folderMaterialization reads the materialization dbt_project.yml assigns a
// model by folder: models.<project>.<dir>...: +materialized, the deepest
// setting winning. dbt's own default is view.
func folderMaterialization(pf projectFile, root, file string) string {
	mat := "view"
	var rel string
	for _, mp := range pf.ModelPaths {
		prefix := path.Join(root, mp) + "/"
		if strings.HasPrefix(file, prefix) {
			rel = strings.TrimPrefix(file, prefix)
		}
	}
	cfg, _ := pf.Models[pf.Name].(map[string]any)
	dirs := strings.Split(path.Dir(rel), "/")
	for i := 0; cfg != nil; i++ {
		for _, k := range []string{"+materialized", "materialized"} {
			if v, ok := cfg[k].(string); ok {
				mat = v
			}
		}
		if i >= len(dirs) || dirs[i] == "." {
			break
		}
		cfg, _ = cfg[dirs[i]].(map[string]any)
	}
	return mat
}

func blankKeepLines(s string) string { return strings.Repeat("\n", strings.Count(s, "\n")) }

// compareDocumented relates the YAML documentation to the SQL. Documented
// columns attach their tests and descriptions to the model; differences are
// findings, because a partial schema.yml is common and not an error.
func compareDocumented(id model.ID, file string, doc modelDoc, produced []string) []model.Observation {
	if doc.file == "" {
		return []model.Observation{finding(file, 1, id, model.SevInfo, "undocumented-model",
			fmt.Sprintf("model %s has no schema.yml entry: no description, column docs or tests", id.Name))}
	}
	var obs []model.Observation
	have := map[string]bool{}
	for _, p := range produced {
		have[strings.ToLower(p)] = true
	}
	documented := map[string]bool{}
	for _, c := range doc.columns {
		documented[strings.ToLower(c.Name)] = true
		p := prov(doc.file, doc.line, model.AuthDbtYAML, "documented column")
		for _, test := range c.testNames() {
			obs = append(obs, model.Observation{Kind: model.KindConstraint, Subject: id, Provenance: p,
				Constraint: &model.ConstraintObs{Attribute: c.Name, Constraint: test}})
		}
		if len(produced) > 0 && !have[strings.ToLower(c.Name)] {
			obs = append(obs, finding(doc.file, doc.line, id, model.SevWarning, "documented-column-missing",
				fmt.Sprintf("documents column %s, which %s does not select", c.Name, file)))
		}
	}
	if len(doc.columns) > 0 {
		var missing []string
		for _, p := range produced {
			if !documented[strings.ToLower(p)] {
				missing = append(missing, p)
			}
		}
		if len(missing) > 0 {
			obs = append(obs, finding(doc.file, doc.line, id, model.SevInfo, "undocumented-columns",
				fmt.Sprintf("selects %s, which schema.yml does not document", strings.Join(missing, ", "))))
		}
	}
	return obs
}

func sortedKeys(m map[string]modelDoc) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
