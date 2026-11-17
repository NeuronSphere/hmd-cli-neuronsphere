// Package nsdata inspects an nsctl data workspace (NERD032 SPEC001 as
// NERD039 SPEC007 amends it): each table in tables/ is a binding of a noun
// named after the table, with its columns; a pinned schema is the authority
// of a pinned table (30), and everything a profile measured -- column types,
// candidate keys, value overlap between columns -- is inferred evidence.
//
// A workspace keeps what a person decided in model/, a NEP051 model
// repository (sources and .hms concepts). It is inspected here too, under the
// workspace's name, so one path -- the workspace -- is the whole model.
package nsdata

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/data"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/inspect"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/inspect/hms"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/inspect/modelsource"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/model"
)

// Perspective is the binding a table gives its noun.
const Perspective = "nsdata"

// MinInclusion is the share of a column's values another table's key must
// hold before the overlap is reported as a candidate relationship.
const MinInclusion = 0.9

// Inspector is the nsctl data workspace inspector.
type Inspector struct{}

func (Inspector) Name() string { return "nsdata" }

func (Inspector) CanInspect(_ context.Context, src inspect.Source) bool {
	if _, err := fs.Stat(src.FS, data.ConfigFile); err != nil {
		return false
	}
	info, err := fs.Stat(src.FS, data.TablesDir)
	return err == nil && info.IsDir()
}

// TableID is the noun a workspace table stands for.
func TableID(workspace, table string) model.ID {
	return model.ID{Namespace: modelsource.Namespace(workspace), Name: table}
}

// RelationshipID is the candidate relationship a join stands for.
func RelationshipID(workspace string, j data.Join) model.ID {
	return model.ID{Namespace: modelsource.Namespace(workspace), Name: j.FromTable + "__" + j.FromColumn + "__" + j.ToTable}
}

func (ins Inspector) Inspect(ctx context.Context, src inspect.Source) ([]model.Observation, error) {
	obs, err := ins.inspectWorkspace(src)
	if err != nil {
		return nil, err
	}
	sub, err := fs.Sub(src.FS, data.ModelDir)
	if err != nil {
		return obs, nil
	}
	msrc := inspect.Source{Root: path.Join(src.Root, data.ModelDir), Repo: src.Repo, Revision: src.Revision, FS: sub}
	for _, mi := range []inspect.Inspector{hms.Inspector{}, modelsource.Inspector{}} {
		if !mi.CanInspect(ctx, msrc) {
			continue
		}
		mo, err := mi.Inspect(ctx, msrc)
		if err != nil {
			return nil, err
		}
		for i := range mo {
			mo[i].Provenance.Inspector = mi.Name()
			mo[i].Provenance.File = path.Join(data.ModelDir, mo[i].Provenance.File)
		}
		obs = append(obs, mo...)
	}
	return obs, nil
}

func (Inspector) inspectWorkspace(src inspect.Source) ([]model.Observation, error) {
	files, err := inspect.Walk(src, data.TablesDir, func(p string) bool { return strings.HasSuffix(p, ".table.yaml") })
	if err != nil {
		return nil, err
	}
	ns := modelsource.Namespace(src.Repo)
	var obs []model.Observation
	for _, file := range files {
		b, err := fs.ReadFile(src.FS, file)
		if err != nil {
			return nil, err
		}
		var t data.Table
		if err := yaml.Unmarshal(b, &t); err != nil || t.Table == "" {
			obs = append(obs, model.Observation{Kind: model.KindFinding,
				Finding:    &model.FindingObs{Severity: model.SevWarning, Code: "table-unreadable", Message: fmt.Sprintf("%v", err)},
				Provenance: model.Provenance{File: file, Authority: model.AuthInferred, Confidence: model.Evidence}})
			continue
		}
		obs = append(obs, inspectTable(src, file, &t, ns)...)
	}
	if b, err := fs.ReadFile(src.FS, path.Join(data.ProfileDir, data.RelationshipsFile)); err == nil {
		var rel data.Relationships
		if err := json.Unmarshal(b, &rel); err == nil {
			obs = append(obs, relationships(src, &rel)...)
		}
	}
	return obs, nil
}

func inspectTable(src inspect.Source, file string, t *data.Table, ns string) []model.Observation {
	id := model.ID{Namespace: ns, Name: t.Table}
	p := model.Provenance{File: file, Authority: model.AuthInferred, Confidence: model.Evidence,
		Why: "a group of data files"}
	values := map[string]model.Value{
		"catalog": model.V(modelsource.GroupCatalog), "schema_name": model.V(ns), "table_name": model.V(t.Table),
		"format": model.V(t.Format), "paths": model.V(strings.Join(t.Paths, ", ")),
	}
	bo := &model.BindingObs{Perspective: Perspective, Primary: true, Values: values}
	obs := []model.Observation{{Kind: model.KindBinding, Subject: id, Provenance: p, Binding: bo}}

	if len(t.Schema) > 0 {
		pp := p
		pp.Authority, pp.Confidence, pp.Why = model.AuthPinned, model.Decided, "pinned schema"
		for i, c := range t.Schema {
			obs = append(obs, model.Observation{Kind: model.KindAttribute, Subject: id, Provenance: pp,
				Attr: &model.AttrObs{Name: c.Name, Binding: bo.Key(), Position: i + 1, Type: ArrowLogical(c.Type),
					Required: model.BoolPtr(!c.Nullable)}})
		}
	}
	prof, err := fs.ReadFile(src.FS, path.Join(data.ProfileDir, t.Table+".json"))
	if err != nil {
		return obs
	}
	var pr data.Profile
	if json.Unmarshal(prof, &pr) != nil {
		return obs
	}
	pp := p
	pp.File, pp.Why = path.Join(data.ProfileDir, t.Table+".json"), "profiled"
	for i, c := range pr.Columns {
		if len(t.Schema) == 0 {
			obs = append(obs, model.Observation{Kind: model.KindAttribute, Subject: id, Provenance: pp,
				Attr: &model.AttrObs{Name: c.Name, Binding: bo.Key(), Position: i + 1, Type: ArrowLogical(c.Type)}})
		}
		if c.CandidateKey {
			obs = append(obs, model.Observation{Kind: model.KindConstraint, Subject: id, Provenance: pp,
				Constraint: &model.ConstraintObs{Attribute: c.Name, Constraint: "unique"}})
		}
	}
	return obs
}

// relationships reports value overlap as candidate relationships. A join a
// person has been asked about is left out: its proposal (a source in model/)
// says the same thing, and a rejected one is not a candidate any more.
func relationships(src inspect.Source, rel *data.Relationships) []model.Observation {
	workspace := src.Repo
	ns := modelsource.Namespace(workspace)
	rejected := map[string]bool{}
	if b, err := fs.ReadFile(src.FS, data.ConfigFile); err == nil {
		var cfg data.Config
		if yaml.Unmarshal(b, &cfg) == nil {
			for _, r := range cfg.Rejected {
				rejected[r] = true
			}
		}
	}
	var obs []model.Observation
	for _, j := range rel.Joins {
		if j.Inclusion < MinInclusion || rejected[j.ID()] {
			continue
		}
		proposal := path.Join(data.ModelDir, modelsource.Dir, j.FromTable+"__"+j.FromColumn+"__"+j.ToTable+".source.yaml")
		if _, err := fs.Stat(src.FS, proposal); err == nil {
			continue
		}
		p := model.Provenance{File: path.Join(data.ProfileDir, data.RelationshipsFile), Authority: model.AuthInferred,
			Confidence: model.Evidence,
			Why: fmt.Sprintf("%d of %d distinct %s.%s values appear in %s.%s", j.Matched, j.FromDistinct,
				j.FromTable, j.FromColumn, j.ToTable, j.ToColumn)}
		obs = append(obs, model.Observation{Kind: model.KindNoun, Subject: RelationshipID(workspace, j), Provenance: p,
			Noun: &model.NounObs{Metatype: model.MetaRelationship,
				RefFrom: model.ID{Namespace: ns, Name: j.FromTable}.String(),
				RefTo:   model.ID{Namespace: ns, Name: j.ToTable}.String()}})
	}
	return obs
}

// ArrowLogical is the .hms type nearest an Arrow type name.
func ArrowLogical(arrow string) model.LogicalType {
	t := strings.ToLower(arrow)
	switch {
	case t == "utf8" || t == "large_utf8" || t == "string":
		return model.String
	case strings.HasPrefix(t, "int") || strings.HasPrefix(t, "uint"):
		return model.Integer
	case strings.HasPrefix(t, "float") || strings.HasPrefix(t, "decimal"):
		return model.Float
	case t == "bool":
		return model.Bool
	case strings.HasPrefix(t, "date") || strings.HasPrefix(t, "timestamp") || strings.HasPrefix(t, "time"):
		return model.Timestamp
	case t == "binary":
		return model.Blob
	}
	return model.Unknown
}
