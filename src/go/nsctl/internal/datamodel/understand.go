// Package datamodel learns what a data workspace's tables mean (NERD039
// SPEC007). It writes nothing but NEP051 files under the workspace's model/
// folder -- a source per proposal, with provenance inferred, and on approval
// the concept's .hms -- and reads them back through ordinary NERD032
// inspection, so `nsctl model inspect`, `diff` and `export` work on a
// workspace with no extra code.
//
// What to ask about is decided from those files and the profile evidence: a
// table with no source file, or a join with none, is a new proposal; a
// source with provenance inferred is still waiting for an answer; a rejected
// one is recorded in data.yaml and not asked again. The inspection is run for
// what it disagrees about, which is shown to the person and never resolved.
package datamodel

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/atomicfile"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/data"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/derive"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/inspect"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/inspect/modelsource"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/inspect/nsdata"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/model"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/perspective"
)

// Proposal kinds.
const (
	Entity       = "entity"
	Relationship = "relationship"
)

// Proposal is one thing understand asks a person about.
type Proposal struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Status string `json:"status"` // proposed | approved
	// Name is the concept's name: the table's until a person renames it.
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Table       string `json:"table,omitempty"`
	// Key is the column that identifies a row of an entity.
	Key  string     `json:"key,omitempty"`
	Join *data.Join `json:"join,omitempty"`
	// Question is asked in plain words; Evidence is what it rests on.
	Question string   `json:"question"`
	Evidence []string `json:"evidence"`
	Files    []string `json:"files"`
}

// Report is what understand, approve and reject print.
type Report struct {
	Workspace string     `json:"workspace"`
	Proposals []Proposal `json:"proposals"`
	Approved  []Proposal `json:"approved"`
	Rejected  []string   `json:"rejected"`
	// Disagreements are what inspection found inconsistent: an approved key
	// the data no longer supports, a source that does not read.
	Disagreements []model.Disagreement `json:"disagreements"`
	// Notes say what is missing for a fuller answer.
	Notes []string `json:"notes,omitempty"`
}

// Layout of the workspace's model folder: a NEP051 model repository.
func sourcesDir(w *data.Workspace) string {
	return w.Path(data.ModelDir, filepath.FromSlash(modelsource.Dir))
}
func schemaDir(w *data.Workspace) string {
	return w.Path(data.ModelDir, "src", "schemas", modelsource.Namespace(w.Name))
}

func sourcePath(w *data.Workspace, source string) string {
	return filepath.Join(sourcesDir(w), source+".source.yaml")
}

// relSource is the source name of a relationship proposal.
func relSource(j data.Join) string { return j.FromTable + "__" + j.FromColumn + "__" + j.ToTable }

// Understand writes a proposal for every table and candidate join nobody has
// decided on yet, then reports every proposal and decision.
func Understand(ctx context.Context, w *data.Workspace) (*Report, error) {
	tables, err := w.Tables()
	if err != nil {
		return nil, err
	}
	if len(tables) == 0 {
		return nil, nserr.New(nserr.Usage, "workspace %s has no tables yet. Run `nsctl data scan --apply` first", w.Name)
	}
	var notes []string
	profiles := map[string]*data.Profile{}
	for _, t := range tables {
		p, err := w.SavedProfile(t.Table)
		if err != nil {
			notes = append(notes, fmt.Sprintf("%s has no profile; run `nsctl data profile %s` so it can be proposed", t.Table, t.Table))
			continue
		}
		profiles[t.Table] = p
	}
	rel, err := w.SavedRelationships()
	if err != nil {
		notes = append(notes, "no relationship evidence; run `nsctl data profile --relationships` to have joins proposed")
		rel = &data.Relationships{}
	}
	rejected := map[string]bool{}
	for _, id := range w.Config.Rejected {
		rejected[id] = true
	}
	for _, t := range tables {
		p := profiles[t.Table]
		if p == nil || rejected[t.Table] || exists(sourcePath(w, t.Table)) {
			continue
		}
		if err := writeSource(w, entitySource(t, p), "Proposed by nsctl data understand. Approve or reject it with\nnsctl data approve|reject "+t.Table+"."); err != nil {
			return nil, err
		}
	}
	for _, j := range rel.Joins {
		if !proposable(j) || rejected[j.ID()] || exists(sourcePath(w, relSource(j))) ||
			profiles[j.FromTable] == nil || profiles[j.ToTable] == nil {
			continue
		}
		f := relationshipSource(j, keyOf(profiles[j.FromTable]))
		if err := writeSource(w, f, "Proposed by nsctl data understand. Approve or reject it with\nnsctl data approve|reject "+j.ID()+"."); err != nil {
			return nil, err
		}
	}
	rep, err := Load(ctx, w)
	if err != nil {
		return nil, err
	}
	rep.Notes = append(notes, rep.Notes...)
	return rep, nil
}

// proposable: most of the column's values are in the key, and either the
// names agree or the values are text, where a chance overlap is unlikely.
func proposable(j data.Join) bool {
	if j.Inclusion < nsdata.MinInclusion || j.FromDistinct < 2 {
		return false
	}
	return j.NameMatch || j.FromDistinct >= 3
}

// keyOf picks the column that identifies a row: a candidate key named like
// the table's id, else the first candidate key, else none.
func keyOf(p *data.Profile) string {
	var keys []string
	for _, c := range p.Columns {
		if c.CandidateKey {
			keys = append(keys, c.Name)
		}
	}
	stem := singular(p.Table)
	for _, k := range keys {
		if k == "id" || k == stem+"_id" || k == p.Table+"_id" {
			return k
		}
	}
	if len(keys) > 0 {
		return keys[0]
	}
	return ""
}

func entitySource(t *data.Table, p *data.Profile) *modelsource.File {
	f := &modelsource.File{Source: t.Table, From: modelsource.From{Group: t.Table}, Provenance: modelsource.Inferred,
		Evidence: &modelsource.Evidence{}}
	y := modelsource.Yield{Key: t.Table}
	cols := columnsOf(t, p)
	for _, c := range cols {
		f.Columns = append(f.Columns, modelsource.Column{Name: c.Name, Type: TrinoType(c.Type)})
		y.Maps = append(y.Maps, modelsource.Mapping{Attribute: c.Name, Column: c.Name})
	}
	f.Yields = modelsource.Yields{y}
	f.Evidence.Notes = append(f.Evidence.Notes, fmt.Sprintf("%d rows from %d file(s)", p.Rows, p.Files))
	if k := keyOf(p); k != "" {
		f.Evidence.Notes = append(f.Evidence.Notes, fmt.Sprintf("key: %s is different on every row and never empty", k))
	} else {
		f.Evidence.Notes = append(f.Evidence.Notes, "no column is different on every row")
	}
	return f
}

// columnsOf is the pinned schema, else the profiled columns.
func columnsOf(t *data.Table, p *data.Profile) []data.Column {
	if len(t.Schema) > 0 {
		return t.Schema
	}
	var out []data.Column
	for _, c := range p.Columns {
		out = append(out, data.Column{Name: c.Name, Type: c.Type, Nullable: c.NullFraction > 0})
	}
	return out
}

func relationshipSource(j data.Join, fromKey string) *modelsource.File {
	f := &modelsource.File{Source: relSource(j), From: modelsource.From{Group: j.FromTable}, Provenance: modelsource.Inferred,
		Evidence: &modelsource.Evidence{Notes: []string{
			fmt.Sprintf("%d of %d distinct %s.%s values appear in %s.%s", j.Matched, j.FromDistinct, j.FromTable, j.FromColumn, j.ToTable, j.ToColumn),
			"cardinality: " + strings.ReplaceAll(j.Cardinality, "_", " "),
		}}}
	f.Yields = relationshipYields(j, fromKey, j.FromTable, j.ToTable, "")
	return f
}

// relationshipYields maps the two ends and the relationship between them.
// A table that refers to itself needs two yield names for one concept.
func relationshipYields(j data.Join, fromKey, fromConcept, toConcept, name string) modelsource.Yields {
	from := modelsource.Yield{Key: fromConcept}
	if fromKey != "" {
		from.Maps = []modelsource.Mapping{{Attribute: fromKey, Column: fromKey}}
	}
	to := modelsource.Yield{Key: toConcept, Maps: []modelsource.Mapping{{Attribute: j.ToColumn, Column: j.FromColumn}}}
	if fromConcept == toConcept {
		from.Key, from.Concept = "from_"+fromConcept, fromConcept
		to.Key, to.Concept = "to_"+toConcept, toConcept
	}
	if name == "" {
		name = fromConcept + "_" + toConcept
	}
	rel := modelsource.Yield{Key: name, From: from.Key, To: to.Key}
	return modelsource.Yields{from, to, rel}
}

func writeSource(w *data.Workspace, f *modelsource.File, header string) error {
	b, err := modelsource.Marshal(f, header)
	if err != nil {
		return err
	}
	return write(sourcePath(w, f.Source), b)
}

func write(path string, b []byte) error {
	if err := atomicfile.Write(path, b, 0o644, 0o755); err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	return nil
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// readSources reads every source in the workspace's model.
func readSources(w *data.Workspace) (map[string]*modelsource.File, error) {
	out := map[string]*modelsource.File{}
	entries, err := os.ReadDir(sourcesDir(w))
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".source.yaml") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(sourcesDir(w), e.Name()))
		if err != nil {
			return nil, err
		}
		f, err := modelsource.Parse(b)
		if err != nil {
			return nil, nserr.New(nserr.Fail, "%s: %v", filepath.Join(sourcesDir(w), e.Name()), err)
		}
		out[f.Source] = f
	}
	return out, nil
}

// Load reports the workspace's proposals and decisions without writing.
func Load(ctx context.Context, w *data.Workspace) (*Report, error) {
	sources, err := readSources(w)
	if err != nil {
		return nil, err
	}
	rel, _ := w.SavedRelationships()
	joins := map[string]data.Join{}
	if rel != nil {
		for _, j := range rel.Joins {
			joins[relSource(j)] = j
		}
	}
	rep := &Report{Workspace: w.Name, Proposals: []Proposal{}, Approved: []Proposal{},
		Rejected: append([]string{}, w.Config.Rejected...), Disagreements: []model.Disagreement{}}
	names := make([]string, 0, len(sources))
	for n := range sources {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		p := describe(w, sources[n], sources, joins)
		if p.Status == "approved" {
			rep.Approved = append(rep.Approved, p)
		} else {
			rep.Proposals = append(rep.Proposals, p)
		}
	}
	// Entities first: a relationship is approved after both its ends.
	sort.SliceStable(rep.Proposals, func(i, j int) bool {
		return rep.Proposals[i].Kind == Entity && rep.Proposals[j].Kind != Entity
	})
	m, err := Inspect(ctx, w)
	if err != nil {
		return nil, err
	}
	ns := modelsource.Namespace(w.Name) + "."
	for _, d := range m.Disagreements {
		if strings.HasPrefix(d.Subject, ns) {
			rep.Disagreements = append(rep.Disagreements, d)
		}
	}
	rep.Disagreements = append(rep.Disagreements, keyChecks(w, rep.Approved)...)
	return rep, nil
}

func describe(w *data.Workspace, f *modelsource.File, all map[string]*modelsource.File, joins map[string]data.Join) Proposal {
	p := Proposal{ID: f.Source, Status: "proposed", Evidence: []string{},
		Files: []string{sourcePath(w, f.Source)}}
	if f.Evidence != nil {
		p.Evidence = append(p.Evidence, f.Evidence.Notes...)
	}
	if f.Prov() == modelsource.Declared {
		p.Status = "approved"
	}
	if j, ok := joins[f.Source]; ok || len(f.Yields) == 3 && f.Yields[2].IsRelationship() {
		p.Kind = Relationship
		rel := f.Yields[len(f.Yields)-1]
		p.Name = rel.Key
		if ok {
			jj := j
			p.ID, p.Join = j.ID(), &jj
			p.Question = joinQuestion(j)
		}
		if h := filepath.Join(schemaDir(w), rel.Key+".hms"); exists(h) {
			p.Files = append(p.Files, h)
			p.Description = hmsDescription(h)
		}
		return p
	}
	p.Kind, p.Table = Entity, f.From.Group
	p.Name = f.Yields[0].ConceptName()
	for _, n := range p.Evidence {
		if k, ok := strings.CutPrefix(n, "key: "); ok {
			p.Key, _, _ = strings.Cut(k, " ")
		}
	}
	if h := filepath.Join(schemaDir(w), p.Name+".hms"); exists(h) {
		p.Files = append(p.Files, h)
		p.Description = hmsDescription(h)
		if k := hmsKey(h); k != "" {
			p.Key = k
		}
	}
	p.Question = entityQuestion(p)
	return p
}

func entityQuestion(p Proposal) string {
	if p.Key == "" {
		return fmt.Sprintf("No column of %s is different on every row. Keep it as a table whose rows have no identifier?", p.Table)
	}
	return fmt.Sprintf("Is each row of %s one %s, identified by %s?", p.Table, singular(p.Table), p.Key)
}

func joinQuestion(j data.Join) string {
	pct := int(j.Inclusion*100 + 0.5)
	if j.Inclusion < 1 && pct == 100 {
		pct = 99
	}
	q := fmt.Sprintf("%d%% of %s.%s values (%d of %d) appear in %s.%s. ", pct, j.FromTable, j.FromColumn,
		j.Matched, j.FromDistinct, j.ToTable, j.ToColumn)
	if j.Cardinality == "one_to_one" {
		return q + fmt.Sprintf("Does each %s row match exactly one %s row?", j.FromTable, j.ToTable)
	}
	return q + fmt.Sprintf("Does each %s row belong to one %s?", singular(j.FromTable), singular(j.ToTable))
}

// keyChecks reports an approved key the data now contradicts: the profile no
// longer finds it different on every row.
func keyChecks(w *data.Workspace, approved []Proposal) []model.Disagreement {
	var out []model.Disagreement
	for _, p := range approved {
		if p.Kind != Entity || p.Key == "" {
			continue
		}
		prof, err := w.SavedProfile(p.Table)
		if err != nil {
			continue
		}
		for _, c := range prof.Columns {
			if c.Name == p.Key && !c.CandidateKey {
				out = append(out, model.Disagreement{Severity: model.SevWarning, Code: "key-contradicted",
					Subject: modelsource.Namespace(w.Name) + "." + p.Name,
					Message: fmt.Sprintf("%s was approved as the key of %s, but the latest profile finds repeated or empty values", p.Key, p.Table)})
			}
		}
	}
	return out
}

// Inspect runs NERD032 inspection over the workspace: its tables and
// profiles, and its model's sources and concepts.
func Inspect(ctx context.Context, w *data.Workspace) (*model.Model, error) {
	src := inspect.Source{Root: w.Root, Repo: w.Name, FS: os.DirFS(w.Root)}
	obs, _ := inspect.Run(ctx, []inspect.Source{src}, []inspect.Inspector{nsdata.Inspector{}})
	res := derive.Run(obs, perspective.New(), nil)
	return model.Consolidate(res.Observations), nil
}

var identRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// singular is a plain English singular, good enough for table names.
func singular(s string) string {
	switch {
	case strings.HasSuffix(s, "ies") && len(s) > 3:
		return s[:len(s)-3] + "y"
	case strings.HasSuffix(s, "sses"), strings.HasSuffix(s, "xes"), strings.HasSuffix(s, "ches"), strings.HasSuffix(s, "shes"):
		return s[:len(s)-2]
	case strings.HasSuffix(s, "s") && !strings.HasSuffix(s, "ss") && len(s) > 1:
		return s[:len(s)-1]
	}
	return s
}

// TrinoType is the Trino type NEP051 columns use for an Arrow type.
func TrinoType(arrow string) string {
	t := strings.ToLower(arrow)
	switch {
	case t == "utf8" || t == "large_utf8" || t == "string":
		return "varchar"
	case t == "int8":
		return "tinyint"
	case t == "int16" || t == "uint8":
		return "smallint"
	case t == "int32" || t == "uint16":
		return "integer"
	case t == "int64" || t == "uint32" || t == "uint64":
		return "bigint"
	case t == "float32":
		return "real"
	case t == "float64":
		return "double"
	case strings.HasPrefix(t, "decimal"):
		return strings.Replace(t, "decimal128", "decimal", 1)
	case t == "bool":
		return "boolean"
	case strings.HasPrefix(t, "date"):
		return "date"
	case strings.HasPrefix(t, "timestamp"):
		return "timestamp"
	case strings.HasPrefix(t, "time"):
		return "time"
	case t == "binary":
		return "varbinary"
	}
	// No Trino equivalent: keep the type, marked as not Trino's.
	return arrow
}
