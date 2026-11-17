package datamodel

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/data"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/inspect/modelsource"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/model"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// Decision is what a person said yes to, with the names they chose.
type Decision struct {
	Name        string
	Description string
	// Key overrides an entity's identifying column; "-" means none.
	Key string
}

// find resolves a proposal id: a table, or a join's from.col=to.col.
func find(w *data.Workspace, id string) (*modelsource.File, *data.Join, error) {
	sources, err := readSources(w)
	if err != nil {
		return nil, nil, err
	}
	if f, ok := sources[id]; ok {
		return f, nil, nil
	}
	if rel, err := w.SavedRelationships(); err == nil {
		for _, j := range rel.Joins {
			if j.ID() == id {
				if f, ok := sources[relSource(j)]; ok {
					jj := j
					return f, &jj, nil
				}
			}
		}
	}
	return nil, nil, nserr.New(nserr.Usage, "no proposal %q. `nsctl data understand` lists them", id)
}

// Approve records a yes: the concept's .hms is written and its source
// becomes declared.
func Approve(ctx context.Context, w *data.Workspace, id string, d Decision) (*Report, error) {
	f, j, err := find(w, id)
	if err != nil {
		return nil, err
	}
	if d.Name != "" && !identRE.MatchString(d.Name) {
		return nil, nserr.New(nserr.Usage, "name %q: use lower case letters, digits and _, starting with a letter", d.Name)
	}
	if j != nil || (len(f.Yields) == 3 && f.Yields[2].IsRelationship()) {
		err = approveRelationship(w, f, j, d)
	} else {
		err = approveEntity(w, f, d)
	}
	if err != nil {
		return nil, err
	}
	return Load(ctx, w)
}

func approveEntity(w *data.Workspace, f *modelsource.File, d Decision) error {
	table := f.From.Group
	y := &f.Yields[0]
	old := y.ConceptName()
	name := old
	if d.Name != "" {
		name = d.Name
	}
	if name != old && exists(filepath.Join(schemaDir(w), name+".hms")) {
		return nserr.New(nserr.Usage, "a concept named %s is already approved", name)
	}
	key := ""
	if prof, err := w.SavedProfile(table); err == nil {
		key = keyOf(prof)
	}
	if d.Key == "-" {
		key = ""
	} else if d.Key != "" {
		if f.Columns.Type(d.Key) == "" {
			return nserr.New(nserr.Usage, "%s has no column %s", table, d.Key)
		}
		key = d.Key
	}
	y.Key, y.Concept = name, ""

	desc := d.Description
	if desc == "" {
		desc = hmsDescription(filepath.Join(schemaDir(w), old+".hms"))
	}
	n := &model.Noun{ID: model.ID{Namespace: modelsource.Namespace(w.Name), Name: name}, Metatype: model.MetaNoun}
	if desc != "" {
		n.Description = model.StrPtr(desc)
	}
	for _, m := range y.Maps {
		a := &model.Attribute{Name: m.Attribute, Type: modelsource.LogicalType(f.Columns.Type(m.Column))}
		if m.Attribute == key {
			a.Required = model.BoolPtr(true)
			a.Extra = map[string]any{"business_id": true}
		}
		n.Attributes = append(n.Attributes, a)
	}
	b, err := model.ExportHMS(n, true)
	if err != nil {
		return err
	}
	if name != old {
		_ = os.Remove(filepath.Join(schemaDir(w), old+".hms"))
	}
	if err := write(filepath.Join(schemaDir(w), name+".hms"), b); err != nil {
		return err
	}
	f.Provenance = modelsource.Declared
	if err := writeSource(w, f, "Approved in nsctl data: "+table+" is "+name+"."); err != nil {
		return err
	}
	if name == old {
		return nil
	}
	return renamePending(w, old, name)
}

// renamePending makes the joins still waiting for an answer name a renamed
// concept by its new name, so the model has one noun for it, not two.
func renamePending(w *data.Workspace, old, name string) error {
	sources, err := readSources(w)
	if err != nil {
		return err
	}
	for _, s := range sources {
		if s.Prov() == modelsource.Declared || len(s.Yields) != 3 || !s.Yields[2].IsRelationship() {
			continue
		}
		changed := false
		for i := 0; i < 2; i++ {
			y := &s.Yields[i]
			switch {
			case y.Concept == old:
				y.Concept, changed = name, true
			case y.Concept == "" && y.Key == old:
				if s.Yields[2].From == old {
					s.Yields[2].From = name
				}
				if s.Yields[2].To == old {
					s.Yields[2].To = name
				}
				y.Key, changed = name, true
			}
		}
		if changed {
			if err := writeSource(w, s, "Proposed by nsctl data understand."); err != nil {
				return err
			}
		}
	}
	return nil
}

func approveRelationship(w *data.Workspace, f *modelsource.File, j *data.Join, d Decision) error {
	if j == nil {
		return nserr.New(nserr.Fail, "%s has no relationship evidence; run `nsctl data profile --relationships`", f.Source)
	}
	sources, err := readSources(w)
	if err != nil {
		return err
	}
	var ends [2]string
	for i, t := range []string{j.FromTable, j.ToTable} {
		s, ok := sources[t]
		if !ok || s.Prov() != modelsource.Declared {
			return nserr.New(nserr.Usage, "approve %s first: a relationship joins two approved tables", t)
		}
		ends[i] = s.Yields[0].ConceptName()
	}
	name := d.Name
	if name == "" {
		name = f.Yields[len(f.Yields)-1].Key
		if name == j.FromTable+"_"+j.ToTable {
			name = ends[0] + "_" + ends[1]
		}
	}
	fromKey := ""
	if s := sources[j.FromTable]; len(s.Yields) > 0 {
		if k := hmsKey(filepath.Join(schemaDir(w), ends[0]+".hms")); k != "" {
			fromKey = k
		}
	}
	toKey := hmsKey(filepath.Join(schemaDir(w), ends[1]+".hms"))
	jj := *j
	if toKey != "" {
		jj.ToColumn = toKey
	}
	f.Yields = relationshipYields(jj, fromKey, ends[0], ends[1], name)

	ns := modelsource.Namespace(w.Name)
	n := &model.Noun{ID: model.ID{Namespace: ns, Name: name}, Metatype: model.MetaRelationship,
		RefFrom: ns + "." + ends[0], RefTo: ns + "." + ends[1]}
	desc := d.Description
	if desc == "" {
		desc = fmt.Sprintf("Each %s refers to one %s by %s.", ends[0], ends[1], j.FromColumn)
	}
	n.Description = model.StrPtr(desc)
	fromCard := "M"
	if j.Cardinality == "one_to_one" {
		fromCard = "1"
	}
	n.Extra = map[string]any{"adornments": map[string]any{
		"ref_from": map[string]any{"arrow": false, "cardinality": fromCard},
		"ref_to":   map[string]any{"arrow": true, "cardinality": "1"},
	}}
	b, err := model.ExportHMS(n, true)
	if err != nil {
		return err
	}
	if err := write(filepath.Join(schemaDir(w), name+".hms"), b); err != nil {
		return err
	}
	f.Provenance = modelsource.Declared
	return writeSource(w, f, "Approved in nsctl data: "+j.ID()+".")
}

// Reject records a no: the proposal's files are removed and understand will
// not ask again. Rejecting an approved proposal withdraws the approval.
func Reject(ctx context.Context, w *data.Workspace, id string) (*Report, error) {
	f, j, err := find(w, id)
	if err != nil {
		return nil, err
	}
	key := f.Source
	if j != nil {
		key = j.ID()
	}
	if f.Prov() == modelsource.Declared {
		_ = os.Remove(filepath.Join(schemaDir(w), f.Yields[len(f.Yields)-1].Key+".hms"))
		if j == nil {
			_ = os.Remove(filepath.Join(schemaDir(w), f.Yields[0].ConceptName()+".hms"))
		}
	}
	if err := os.Remove(sourcePath(w, f.Source)); err != nil {
		return nil, nserr.Wrap(nserr.Fail, err)
	}
	for _, r := range w.Config.Rejected {
		if r == key {
			return Load(ctx, w)
		}
	}
	w.Config.Rejected = append(w.Config.Rejected, key)
	if err := w.Save(); err != nil {
		return nil, err
	}
	return Load(ctx, w)
}

// hmsDescription reads an .hms file's description, "" when absent.
func hmsDescription(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var doc struct {
		Description string `json:"description"`
	}
	_ = json.Unmarshal(b, &doc)
	return doc.Description
}

// hmsKey reads the business_id attribute of an .hms file.
func hmsKey(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var doc struct {
		Attributes map[string]struct {
			BusinessID bool `json:"business_id"`
		} `json:"attributes"`
	}
	_ = json.Unmarshal(b, &doc)
	for name, a := range doc.Attributes {
		if a.BusinessID {
			return name
		}
	}
	return ""
}
