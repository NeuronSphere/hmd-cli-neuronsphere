// Package nstransform inspects NeuronSphere transform definitions
// (src/transforms/*.yaml): the Trino SQL in TrinoOperator parameters, the
// dep_tf_name chain between transforms, and the schema a dbt transform
// materialises its project into.
//
// The NeuronSphere conventions this relies on live here and nowhere else:
// Jinja rendered from a transform's own run_params, run-time values left as
// {placeholders}, and layer schemas named <namespace>_<layer> (source,
// staging, final, ux) that make one logical noun out of a table per layer.
package nstransform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/inspect"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/inspect/nsexport"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/inspect/sqlddl"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/model"
)

const transformsDir = "src/transforms"

// Layers are the schema suffixes the reporting and billing transforms use,
// in pipeline order. The last is the primary manifestation.
var Layers = []string{"source", "staging", "final", "ux"}

const primaryLayer = "final"

// Inspector is the NeuronSphere transform inspector.
type Inspector struct{}

func (Inspector) Name() string { return "nstransform" }

func (Inspector) CanInspect(_ context.Context, src inspect.Source) bool {
	info, err := fs.Stat(src.FS, transformsDir)
	return err == nil && info.IsDir()
}

// transform is the part of a transform YAML the inspector reads.
type transform struct {
	Type        string `yaml:"type"`
	QueryConfig struct {
		QueryName  string         `yaml:"query_name"`
		QueryValue map[string]any `yaml:"query_value"`
	} `yaml:"query_config"`
	Config struct {
		ProviderClass  string            `yaml:"provider_class"`
		Params         map[string]any    `yaml:"params"`
		RunParams      map[string]any    `yaml:"run_params"`
		DbtProjectName string            `yaml:"dbt_project_name"`
		Env            map[string]string `yaml:"env"`
		Profile        struct {
			ProfileArgs map[string]any `yaml:"profile_args"`
		} `yaml:"profile"`
	} `yaml:"config"`
}

func (Inspector) Inspect(_ context.Context, src inspect.Source) ([]model.Observation, error) {
	files, err := inspect.Walk(src, transformsDir, func(p string) bool {
		return strings.HasSuffix(p, ".yaml") || strings.HasSuffix(p, ".yml")
	})
	if err != nil {
		return nil, err
	}
	var obs []model.Observation
	for _, f := range files {
		data, err := fs.ReadFile(src.FS, f)
		if err != nil {
			return nil, err
		}
		obs = append(obs, inspectFile(f, data)...)
	}
	return obs, nil
}

func prov(file string, line int, auth model.Authority, why string) model.Provenance {
	return model.Provenance{File: file, Line: line, Authority: auth, Confidence: model.Decided, Why: why}
}

func finding(file string, line int, sev model.Severity, code, msg string) model.Observation {
	return model.Observation{
		Kind:       model.KindFinding,
		Finding:    &model.FindingObs{Severity: sev, Code: code, Message: msg},
		Provenance: prov(file, line, model.AuthDDL, ""),
	}
}

func named(kind model.Kind, file string, line int, what, key, via string) model.Observation {
	return model.Observation{
		Kind:       kind,
		Named:      &model.NamedObs{Kind: what, Key: key, Via: via},
		Provenance: prov(file, line, model.AuthDDL, ""),
	}
}

func inspectFile(file string, data []byte) []model.Observation {
	var root yaml.Node
	var tf transform
	if err := yaml.Unmarshal(data, &root); err != nil {
		return []model.Observation{finding(file, 1, model.SevError, "unparseable-transform", err.Error())}
	}
	if err := root.Decode(&tf); err != nil || tf.Type == "" {
		return nil // not a transform definition
	}
	text := string(data)
	var obs []model.Observation

	// The transform's name, and the one it depends on.
	stem := strings.TrimSuffix(strings.TrimSuffix(path.Base(file), ".yaml"), ".yml")
	obs = append(obs, named(model.KindProvide, file, 1, "transform", stem, ""))
	if declared, _ := tf.QueryConfig.QueryValue["tf_name"].(string); declared != "" && declared != stem {
		obs = append(obs, named(model.KindProvide, file, inspect.LineOf(text, "tf_name:"), "transform", declared, ""))
		obs = append(obs, finding(file, inspect.LineOf(text, "tf_name:"), model.SevWarning, "tf-name-mismatch",
			fmt.Sprintf("query_value.tf_name is %q but the file is %q; which name dependants must use depends on how the transform is registered", declared, stem)))
	}
	if dep, _ := tf.QueryConfig.QueryValue["dep_tf_name"].(string); dep != "" {
		obs = append(obs, named(model.KindReference, file, inspect.LineOf(text, "dep_tf_name:"), "transform", dep, "dep_tf_name"))
	}

	r := renderer{params: tf.Config.RunParams}
	// Unused run_params: values nothing in the file reads.
	for _, k := range sortedKeys(tf.Config.RunParams) {
		if !regexp.MustCompile(`ns_context\[\s*['"]` + regexp.QuoteMeta(k) + `['"]\s*\]`).MatchString(text) {
			obs = append(obs, finding(file, inspect.LineOf(text, k+":"), model.SevInfo, "unused-run-param",
				fmt.Sprintf("run_params.%s is set but nothing in the transform reads ns_context['%s']", k, k)))
		}
	}

	if tf.Type == "dbt" && tf.Config.DbtProjectName != "" {
		if schema := r.dbtSchema(tf); schema != "" {
			obs = append(obs, model.Observation{
				Kind:       model.KindBinding,
				Binding:    &model.BindingObs{Scope: "dbt:" + tf.Config.DbtProjectName, Schema: schema},
				Provenance: prov(file, inspect.LineOf(text, "dbt_project_name:"), model.AuthDDL, "dbt transform's target schema"),
			})
		}
	}

	sql, _ := tf.Config.Params["sql"].(string)
	if sql == "" {
		return obs
	}
	sqlLine := sqlStartLine(&root)
	rendered := r.render(sql)
	itemType, _ := tf.QueryConfig.QueryValue["item_type"].(string)
	if itemType != "" {
		obs = append(obs, named(model.KindReference, file, inspect.LineOf(text, "item_type:"), "content-type", itemType, "query item_type"))
	}
	for _, st := range sqlddl.Parse(rendered) {
		line := sqlLine + st.Line - 1
		obs = append(obs, statementObservations(file, line, st)...)
		// A transform triggered by Librarian content of one type that creates
		// an external table over it: the table reads that content.
		if itemType != "" && st.Kind == sqlddl.CreateTable && st.With["external_location"] != "" {
			to, _ := tableID(st.Name)
			p := prov(file, line, model.AuthInferred, "external table created for each content item of the queried type")
			p.Confidence = model.Evidence
			obs = append(obs, model.Observation{Kind: model.KindLineage, Provenance: p, Lineage: &model.LineageObs{
				From: model.Endpoint{ID: nsexport.ContentTypeID(itemType)}, To: model.Endpoint{ID: to}, Via: "librarian-content-type"}})
		}
	}
	return obs
}

// sqlStartLine is the line of the first line of config.params.sql.
func sqlStartLine(root *yaml.Node) int {
	n := root
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		n = n.Content[0]
	}
	for _, key := range []string{"config", "params", "sql"} {
		next := (*yaml.Node)(nil)
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == key {
				next = n.Content[i+1]
			}
		}
		if next == nil {
			return 1
		}
		n = next
	}
	if n.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
		return n.Line + 1
	}
	return n.Line
}

// tableID is the logical identity of a physical table: its schema with the
// layer suffix removed is the namespace, and its name with run-time
// placeholder segments removed is the name.
func tableID(name sqlddl.Name) (id model.ID, layer string) {
	schema := name.Schema()
	ns := schema
	for _, l := range Layers {
		if strings.HasSuffix(schema, "_"+l) {
			ns, layer = strings.TrimSuffix(schema, "_"+l), l
			break
		}
	}
	return model.ID{Namespace: ns, Name: stem(name.Table())}, layer
}

var placeholderSegment = regexp.MustCompile(`_?\{[^}]*\}`)

// stem removes {placeholder} segments from a table name.
func stem(table string) string {
	s := placeholderSegment.ReplaceAllString(table, "")
	return strings.Trim(s, "_")
}

func location(name sqlddl.Name) model.Location {
	return model.Location{Catalog: name.Catalog(), Schema: name.Schema(), Table: name.Table()}
}

func manifestKey(name sqlddl.Name) string {
	return "trino:" + strings.ToLower(name.Schema()+"."+name.Table())
}

func manifestation(file string, line int, st sqlddl.Statement, tech string, auth model.Authority, why string) model.Observation {
	id, layer := tableID(st.Name)
	m := &model.ManifestObs{
		Key: manifestKey(st.Name), Tech: tech, Location: location(st.Name), Layer: layer,
		Primary: layer == primaryLayer, Partitions: st.Partitions,
	}
	if strings.Contains(st.Name.Table(), "{") || strings.Contains(st.Name.Schema(), "{") {
		m.Template = st.Name.String()
	}
	if f, ok := st.With["format"]; ok {
		m.Format = strings.ToLower(f)
	}
	return model.Observation{Kind: model.KindManifestation, Subject: id, Manifest: m, Provenance: prov(file, line, auth, why)}
}

func statementObservations(file string, line int, st sqlddl.Statement) []model.Observation {
	var obs []model.Observation
	switch st.Kind {
	case sqlddl.CreateSchema:
		obs = append(obs, named(model.KindProvide, file, line, "schema", strings.ToLower(st.Name.String()), ""))
	case sqlddl.CreateTable:
		if st.Name.Schema() == "" {
			return nil
		}
		m := manifestation(file, line, st, "trino-table", model.AuthDDL, "CREATE TABLE")
		obs = append(obs, m, schemaRef(file, line, st.Name))
		for i, c := range st.Columns {
			a := &model.AttrObs{
				Name: c.Name, Manifestation: m.Manifest.Key, Position: i + 1,
				PhysicalType: c.Type, Type: model.SQLType(c.Type),
			}
			if c.NotNull {
				a.Required = model.BoolPtr(true)
			}
			p := m.Provenance
			p.Line = line + c.Line - st.Line
			obs = append(obs, model.Observation{Kind: model.KindAttribute, Subject: m.Subject, Attr: a, Provenance: p})
		}
		if len(st.Select) > 0 {
			obs = append(obs, selectObservations(file, line, st, m)...)
		}
	case sqlddl.CreateView:
		if st.Name.Schema() == "" {
			return nil
		}
		m := manifestation(file, line, st, "trino-view", model.AuthDDL, "CREATE VIEW")
		obs = append(obs, m, schemaRef(file, line, st.Name))
		obs = append(obs, selectObservations(file, line, st, m)...)
	case sqlddl.Insert:
		if st.Name.Schema() == "" {
			return nil
		}
		m := manifestation(file, line, st, "trino-table", model.AuthInsertSelect, "INSERT target")
		m.Manifest.Primary = false
		m.Manifest.Reference = true
		obs = append(obs, m)
		obs = append(obs, named(model.KindReference, file, line, "table", strings.ToLower(st.Name.Schema()+"."+st.Name.Table()), "insert into"))
		obs = append(obs, selectObservations(file, line, st, m)...)
	case sqlddl.Drop:
		what := strings.ToLower(st.Object)
		switch what {
		case "table", "view":
			obs = append(obs, named(model.KindReference, file, line, "table", strings.ToLower(st.Name.Schema()+"."+st.Name.Table()), "drop"))
		case "schema":
			obs = append(obs, named(model.KindReference, file, line, "schema", strings.ToLower(st.Name.String()), "drop"))
		}
	}
	return obs
}

func schemaRef(file string, line int, name sqlddl.Name) model.Observation {
	return named(model.KindReference, file, line, "schema", strings.ToLower(name.Schema()), "table in schema")
}

// selectObservations records the select list as columns of the target (for
// an INSERT or a view with an explicit list), and lineage from each table
// read.
func selectObservations(file string, line int, st sqlddl.Statement, target model.Observation) []model.Observation {
	var obs []model.Observation
	via := "insert-select"
	if st.Kind == sqlddl.CreateView {
		via = "view"
	}
	for _, from := range st.From {
		if from.Schema() == "" {
			continue
		}
		fromID, _ := tableID(from)
		obs = append(obs, model.Observation{
			Kind: model.KindLineage,
			Lineage: &model.LineageObs{
				From: model.Endpoint{ID: fromID, Location: location(from)},
				To:   model.Endpoint{ID: target.Subject},
				Via:  via,
			},
			Provenance: prov(file, line, target.Provenance.Authority, via),
		})
		obs = append(obs, named(model.KindReference, file, line, "table", strings.ToLower(from.Schema()+"."+from.Table()), "select from"))
	}
	if len(st.Select) == 1 && st.Select[0].Name == "*" {
		return obs
	}
	names := st.InsertCols
	typeWords := 0
	if len(names) == 0 {
		for _, it := range st.Select {
			name := it.Name
			if it.TypeWordAlias {
				// "col" varchar: Trino names the column varchar. The INSERT is
				// positional so it works, and the author plainly meant col.
				name = it.Source
				typeWords++
			}
			names = append(names, name)
		}
	}
	auth := model.AuthInsertSelect
	if st.Kind == sqlddl.CreateView {
		auth = model.AuthDDL
	}
	for i, n := range names {
		p := prov(file, line, auth, "select list")
		if i < len(st.Select) {
			p.Line = line + st.Select[i].Line - st.Line
		}
		obs = append(obs, model.Observation{Kind: model.KindAttribute, Subject: target.Subject, Provenance: p,
			Attr: &model.AttrObs{Name: n, Manifestation: target.Manifest.Key, Position: i + 1, Type: model.Unknown}})
	}
	if typeWords > 0 {
		obs = append(obs, finding(file, line, model.SevInfo, "type-word-alias",
			fmt.Sprintf("%d select items are written \"col\" <type>, which aliases the column to the type name instead of casting it; the insert only works by position",
				typeWords)))
	}
	return obs
}

// renderer renders the Jinja in a transform's SQL from its run_params.
type renderer struct {
	params map[string]any
}

var (
	jinjaExpr  = regexp.MustCompile(`\{\{(.*?)\}\}`)
	jinjaStmt  = regexp.MustCompile(`\{%.*?%\}`)
	contextRef = regexp.MustCompile(`ns_context\[\s*['"]([^'"]+)['"]\s*\]`)
	simpleRef  = regexp.MustCompile(`^ns_context\[\s*['"]([^'"]+)['"]\s*\]((?:\.replace\(\s*'[^']*'\s*,\s*'[^']*'\s*\))*)$`)
	replaceArg = regexp.MustCompile(`\.replace\(\s*'([^']*)'\s*,\s*'([^']*)'\s*\)`)
	envVarRef  = regexp.MustCompile(`^env_var\(\s*['"]([^'"]+)['"]\s*\)$`)
	runtimeVal = regexp.MustCompile(`^(context|entity|transform|transform_instance)\.|^[A-Z][A-Z0-9_]*$`)
)

// value returns a run_param's literal value, or false when it is only known
// at run time: absent, a context/entity path, or an environment-variable name.
func (r renderer) value(key string) (string, bool) {
	v, ok := r.params[key]
	if !ok || v == nil {
		return "", false
	}
	s := fmt.Sprint(v)
	if runtimeVal.MatchString(s) {
		return "", false
	}
	return s, true
}

func (r renderer) render(text string) string {
	text = jinjaStmt.ReplaceAllString(text, "")
	return jinjaExpr.ReplaceAllStringFunc(text, func(m string) string {
		expr := strings.TrimSpace(m[2 : len(m)-2])
		return r.renderExpr(expr)
	})
}

func (r renderer) renderExpr(expr string) string {
	if sm := simpleRef.FindStringSubmatch(expr); sm != nil {
		key, chain := sm[1], sm[2]
		reps := replaceArg.FindAllStringSubmatch(chain, -1)
		if v, ok := r.value(key); ok {
			for _, rp := range reps {
				v = strings.ReplaceAll(v, rp[1], rp[2])
			}
			return v
		}
		ph := key
		for _, rp := range reps {
			ph += fmt.Sprintf("|replace(%s,%s)", rp[1], rp[2])
		}
		return "{" + ph + "}"
	}
	if sm := envVarRef.FindStringSubmatch(expr); sm != nil {
		return "{env:" + sm[1] + "}"
	}
	// Anything else: name it by the context keys it reads and its final
	// accessor, plus a short digest so different expressions stay different.
	var keys []string
	for _, k := range contextRef.FindAllStringSubmatch(expr, -1) {
		keys = append(keys, k[1])
	}
	label := strings.Join(keys, ",")
	if i := strings.LastIndex(expr, ")."); i >= 0 && !strings.ContainsAny(expr[i+2:], "()[] ") {
		label += "." + expr[i+2:]
	}
	if label == "" {
		label = "expr"
	}
	sum := sha256.Sum256([]byte(strings.Join(strings.Fields(expr), "")))
	return "{" + label + "~" + hex.EncodeToString(sum[:])[:4] + "}"
}

// dbtSchema resolves the schema a dbt transform targets:
// profile_args.schema, through env_var() into config.env, through Jinja into
// run_params.
func (r renderer) dbtSchema(tf transform) string {
	raw, _ := tf.Config.Profile.ProfileArgs["schema"].(string)
	if raw == "" {
		return ""
	}
	out := jinjaExpr.ReplaceAllStringFunc(raw, func(m string) string {
		expr := strings.TrimSpace(m[2 : len(m)-2])
		if sm := envVarRef.FindStringSubmatch(expr); sm != nil {
			if v, ok := tf.Config.Env[sm[1]]; ok {
				return r.render(v)
			}
		}
		return r.renderExpr(expr)
	})
	if strings.Contains(out, "{") {
		return ""
	}
	return out
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
