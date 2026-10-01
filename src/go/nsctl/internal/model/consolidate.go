package model

import (
	"fmt"
	"sort"
	"strings"
)

// Consolidate turns observations into the canonical model (NERD032 SPEC004).
// It is deterministic: the same observations, in any order, give the same
// model, and every merge records why it happened.
func Consolidate(observations []Observation) *Model {
	obs := append([]Observation(nil), observations...)
	sort.SliceStable(obs, func(i, j int) bool { return obsLess(obs[i], obs[j]) })

	c := &consolidator{
		parent:  map[ID]ID{},
		reasons: map[ID]string{},
		nouns:   map[ID]*Noun{},
		model:   &Model{},
	}
	c.bind(obs)
	c.link(obs)
	c.buildNouns(obs)
	c.buildManifestations(obs)
	c.buildAttributes(obs)
	c.applyConstraints(obs)
	c.compareLayers()
	c.resolveLineage(obs)
	c.resolveReferences(obs)
	c.passFindings(obs)

	for _, n := range c.nouns {
		c.model.Nouns = append(c.model.Nouns, n)
	}
	sort.Slice(c.model.Nouns, func(i, j int) bool {
		return c.model.Nouns[i].ID.String() < c.model.Nouns[j].ID.String()
	})
	sort.SliceStable(c.model.Disagreements, func(i, j int) bool {
		a, b := c.model.Disagreements[i], c.model.Disagreements[j]
		if sevRank(a.Severity) != sevRank(b.Severity) {
			return sevRank(a.Severity) < sevRank(b.Severity)
		}
		if a.Subject != b.Subject {
			return a.Subject < b.Subject
		}
		return a.Message < b.Message
	})
	return c.model
}

func sevRank(s Severity) int {
	switch s {
	case SevError:
		return 0
	case SevWarning:
		return 1
	}
	return 2
}

func obsLess(a, b Observation) bool {
	if a.Provenance.Authority != b.Provenance.Authority {
		return a.Provenance.Authority > b.Provenance.Authority
	}
	if a.Provenance.Repo != b.Provenance.Repo {
		return a.Provenance.Repo < b.Provenance.Repo
	}
	if a.Provenance.File != b.Provenance.File {
		return a.Provenance.File < b.Provenance.File
	}
	return a.Provenance.Line < b.Provenance.Line
}

type consolidator struct {
	bindings map[string]string
	parent   map[ID]ID
	reasons  map[ID]string
	// locIndex maps a location key to the identity that first claimed it.
	locIndex map[string]ID
	nouns    map[ID]*Noun
	canonIDs map[ID]ID
	model    *Model
}

func (c *consolidator) disagree(sev Severity, code, subject, format string, args ...any) *Disagreement {
	c.model.Disagreements = append(c.model.Disagreements, Disagreement{
		Severity: sev, Code: code, Subject: subject, Message: fmt.Sprintf(format, args...),
	})
	return &c.model.Disagreements[len(c.model.Disagreements)-1]
}

// bind collects Binding observations and fills in the schema of every
// manifestation in a bound scope.
func (c *consolidator) bind(obs []Observation) {
	c.bindings = map[string]string{}
	for _, o := range obs {
		if o.Kind != KindBinding || o.Binding == nil {
			continue
		}
		if prev, ok := c.bindings[o.Binding.Scope]; ok && prev != o.Binding.Schema {
			d := c.disagree(SevWarning, "conflicting-binding", o.Binding.Scope,
				"bound to schema %q and %q", prev, o.Binding.Schema)
			d.Sources = []Provenance{o.Provenance}
			continue
		}
		c.bindings[o.Binding.Scope] = o.Binding.Schema
	}
	for i := range obs {
		m := obs[i].Manifest
		if m != nil && m.Scope != "" && m.Location.Schema == "" {
			if schema, ok := c.bindings[m.Scope]; ok {
				m.Location.Schema = schema
			}
		}
	}
}

func (c *consolidator) find(id ID) ID {
	if _, ok := c.parent[id]; !ok {
		c.parent[id] = id
		return id
	}
	for c.parent[id] != id {
		id = c.parent[id]
	}
	return id
}

func (c *consolidator) union(a, b ID, reason string) {
	ra, rb := c.find(a), c.find(b)
	if ra == rb {
		return
	}
	c.parent[rb] = ra
	if _, ok := c.reasons[b]; !ok {
		c.reasons[b] = reason
	}
}

// link decides which identities are one noun: a shared physical location, or
// an explicit same-as declaration.
func (c *consolidator) link(obs []Observation) {
	c.locIndex = map[string]ID{}
	for _, o := range obs {
		c.find(o.Subject)
		switch {
		case o.Manifest != nil:
			key := o.Manifest.Location.Key()
			if key == "" {
				continue
			}
			if first, ok := c.locIndex[key]; ok {
				if first != o.Subject {
					c.union(first, o.Subject, fmt.Sprintf("same physical table %s (%s)", key, o.Provenance.Where()))
				}
			} else {
				c.locIndex[key] = o.Subject
			}
		case o.Kind == KindSameAs && o.SameAs != nil:
			c.union(o.Subject, o.SameAs.Other, fmt.Sprintf("declared the same (%s)", o.Provenance.Where()))
		}
	}
}

// canonical picks each group's identity: the one stated with the highest
// authority, ties to the alphabetically first.
func (c *consolidator) canonical(obs []Observation) map[ID]ID {
	best := map[ID]ID{}
	auth := map[ID]Authority{}
	for _, o := range obs {
		if o.Subject.IsZero() {
			continue
		}
		root := c.find(o.Subject)
		a := o.Provenance.Authority
		cur, ok := best[root]
		if !ok || a > auth[root] || (a == auth[root] && o.Subject.String() < cur.String()) {
			best[root] = o.Subject
			auth[root] = a
		}
	}
	out := map[ID]ID{}
	for id := range c.parent {
		if b, ok := best[c.find(id)]; ok {
			out[id] = b
		}
	}
	return out
}

func (c *consolidator) canon(id ID) ID {
	return c.canonIDs[id]
}

func (c *consolidator) noun(id ID) *Noun {
	cid := c.canon(id)
	if cid.IsZero() {
		return nil
	}
	n, ok := c.nouns[cid]
	if !ok {
		n = &Noun{ID: cid, Metatype: MetaNoun}
		c.nouns[cid] = n
	}
	if id != cid && !hasAlias(n, id) {
		n.Aliases = append(n.Aliases, Alias{ID: id, Reason: c.aliasReason(id)})
	}
	return n
}

func (c *consolidator) aliasReason(id ID) string {
	for cur := id; ; {
		if r, ok := c.reasons[cur]; ok {
			return r
		}
		p := c.parent[cur]
		if p == cur {
			return "linked"
		}
		cur = p
	}
}

func hasAlias(n *Noun, id ID) bool {
	for _, a := range n.Aliases {
		if a.ID == id {
			return true
		}
	}
	return false
}

func (c *consolidator) buildNouns(obs []Observation) {
	c.canonIDs = c.canonical(obs)
	seen := map[ID]Provenance{}
	for _, o := range obs {
		if o.Subject.IsZero() {
			continue
		}
		switch o.Kind {
		case KindNoun, KindAttribute, KindManifestation, KindConstraint:
		default:
			continue
		}
		n := c.noun(o.Subject)
		if o.Kind != KindNoun || o.Noun == nil {
			continue
		}
		if prev, ok := seen[n.ID]; ok {
			// Two definitions of one noun: the stronger one is already in place.
			if prev.Authority == AuthHMS && o.Provenance.Authority == AuthHMS &&
				(prev.Repo != o.Provenance.Repo || prev.File != o.Provenance.File) {
				d := c.disagree(SevWarning, "duplicate-definition", n.ID.String(),
					"defined in both %s and %s", prev.Where(), o.Provenance.Where())
				d.Sources = []Provenance{prev, o.Provenance}
			}
			n.Sources = append(n.Sources, o.Provenance)
			continue
		}
		seen[n.ID] = o.Provenance
		if o.Noun.Metatype != "" {
			n.Metatype = o.Noun.Metatype
		}
		n.Description = o.Noun.Description
		n.RefFrom, n.RefTo = o.Noun.RefFrom, o.Noun.RefTo
		n.Authoritative = o.Provenance.Authority >= AuthHMS
		if len(o.Noun.Extra) > 0 {
			n.Extra = map[string]any{}
			for k, v := range o.Noun.Extra {
				n.Extra[k] = rawExtra(v)
			}
		}
		n.Sources = append(n.Sources, o.Provenance)
	}
}

func (c *consolidator) manifestation(n *Noun, key string) *Manifestation {
	for _, m := range n.Manifestations {
		if m.Key == key {
			return m
		}
	}
	return nil
}

func (c *consolidator) buildManifestations(obs []Observation) {
	for _, o := range obs {
		if o.Kind != KindManifestation || o.Manifest == nil {
			continue
		}
		n := c.noun(o.Subject)
		mo := o.Manifest
		m := c.manifestation(n, mo.Key)
		if m == nil {
			// Observations are in authority order, so the first is the strongest.
			m = &Manifestation{
				Key: mo.Key, Tech: mo.Tech, Scope: mo.Scope, Location: mo.Location,
				Layer: mo.Layer, Format: mo.Format, Partitions: mo.Partitions,
				Template: mo.Template, Primary: mo.Primary,
			}
			n.Manifestations = append(n.Manifestations, m)
		} else {
			m.Primary = m.Primary || mo.Primary
			if len(m.Partitions) == 0 {
				m.Partitions = mo.Partitions
			}
		}
		m.Sources = append(m.Sources, o.Provenance)
	}
	for _, n := range c.nouns {
		sort.SliceStable(n.Manifestations, func(i, j int) bool {
			return n.Manifestations[i].Key < n.Manifestations[j].Key
		})
	}
}

type columnSet struct {
	prov Provenance
	cols []*AttrObs
}

func (c *consolidator) buildAttributes(obs []Observation) {
	// Columns per manifestation, per source file, in authority order.
	sets := map[*Manifestation][]*columnSet{}
	hmsAttrs := map[ID][]Observation{}
	for _, o := range obs {
		if o.Kind != KindAttribute || o.Attr == nil {
			continue
		}
		n := c.noun(o.Subject)
		if o.Attr.Manifestation == "" {
			hmsAttrs[n.ID] = append(hmsAttrs[n.ID], o)
			continue
		}
		m := c.manifestation(n, o.Attr.Manifestation)
		if m == nil {
			m = &Manifestation{Key: o.Attr.Manifestation, Tech: "unknown"}
			n.Manifestations = append(n.Manifestations, m)
		}
		var set *columnSet
		for _, s := range sets[m] {
			if s.prov.Repo == o.Provenance.Repo && s.prov.File == o.Provenance.File {
				set = s
			}
		}
		if set == nil {
			set = &columnSet{prov: o.Provenance}
			sets[m] = append(sets[m], set)
		}
		set.cols = append(set.cols, o.Attr)
	}

	for m, list := range sets {
		for _, s := range list {
			sort.SliceStable(s.cols, func(i, j int) bool { return s.cols[i].Position < s.cols[j].Position })
		}
		top := list[0]
		for _, a := range top.cols {
			m.Columns = append(m.Columns, Column{
				Name: a.Name, PhysicalType: a.PhysicalType, Type: a.Type, Required: a.Required,
				Sources: []Provenance{top.prov},
			})
		}
		for _, other := range list[1:] {
			c.compareColumns(m, top, other)
		}
	}

	for _, n := range c.nouns {
		if attrs, ok := hmsAttrs[n.ID]; ok {
			c.nounAttributesFromHMS(n, attrs)
			continue
		}
		c.nounAttributesFromManifestation(n)
	}
}

func (c *consolidator) compareColumns(m *Manifestation, top, other *columnSet) {
	subject := m.Location.String()
	if subject == "" {
		subject = m.Key
	}
	if len(top.cols) != len(other.cols) {
		d := c.disagree(SevError, "column-count", subject,
			"%s lists %d columns, %s lists %d", top.prov.Where(), len(top.cols), other.prov.Where(), len(other.cols))
		d.Sources = []Provenance{top.prov, other.prov}
		return
	}
	for i := range top.cols {
		a, b := top.cols[i], other.cols[i]
		if !strings.EqualFold(a.Name, b.Name) {
			d := c.disagree(SevError, "column-order", subject,
				"column %d is %q in %s but %q in %s", i+1, a.Name, top.prov.Where(), b.Name, other.prov.Where())
			d.Sources = []Provenance{top.prov, other.prov}
			continue
		}
		if a.Type != Unknown && b.Type != Unknown && a.Type != b.Type {
			d := c.disagree(SevWarning, "column-type", subject+"."+a.Name,
				"%s in %s but %s in %s", a.Type, top.prov.Where(), b.Type, other.prov.Where())
			d.Sources = []Provenance{top.prov, other.prov}
		}
	}
	for i := range m.Columns {
		m.Columns[i].Sources = append(m.Columns[i].Sources, other.prov)
	}
	m.Sources = appendUnique(m.Sources, other.prov)
}

func appendUnique(list []Provenance, p Provenance) []Provenance {
	for _, q := range list {
		if q.Repo == p.Repo && q.File == p.File && q.Line == p.Line {
			return list
		}
	}
	return append(list, p)
}

func (c *consolidator) nounAttributesFromHMS(n *Noun, attrs []Observation) {
	first := attrs[0].Provenance
	for _, o := range attrs {
		if o.Provenance.Repo != first.Repo || o.Provenance.File != first.File {
			continue // a duplicate definition, already reported
		}
		a := o.Attr
		attr := &Attribute{
			Name: a.Name, Type: a.Type, HMSType: a.HMSType, Required: a.Required,
			EnumDef: a.EnumDef, Description: a.Description, Sources: []Provenance{o.Provenance},
		}
		if len(a.Extra) > 0 {
			attr.Extra = map[string]any{}
			for k, v := range a.Extra {
				attr.Extra[k] = rawExtra(v)
			}
		}
		n.Attributes = append(n.Attributes, attr)
	}
	sort.SliceStable(n.Attributes, func(i, j int) bool {
		return ordinal(attrs, n.Attributes[i].Name) < ordinal(attrs, n.Attributes[j].Name)
	})
}

func ordinal(attrs []Observation, name string) int {
	for _, o := range attrs {
		if o.Attr.Name == name {
			return o.Attr.Ordinal
		}
	}
	return 0
}

// primary is the manifestation whose columns become the noun's attributes:
// the one an inspector marked primary, else the one stated with the highest
// authority, else the first by key.
func primary(n *Noun) *Manifestation {
	var best *Manifestation
	bestAuth := Authority(-1)
	for _, m := range n.Manifestations {
		if len(m.Columns) == 0 {
			continue
		}
		if m.Primary {
			return m
		}
		a := Authority(0)
		for _, s := range m.Sources {
			if s.Authority > a {
				a = s.Authority
			}
		}
		if a > bestAuth {
			best, bestAuth = m, a
		}
	}
	return best
}

func (c *consolidator) nounAttributesFromManifestation(n *Noun) {
	m := primary(n)
	if m == nil {
		return
	}
	parts := map[string]bool{}
	for _, p := range m.Partitions {
		parts[strings.ToLower(p)] = true
	}
	for _, col := range m.Columns {
		n.Attributes = append(n.Attributes, &Attribute{
			Name: col.Name, Type: col.Type, PhysicalType: col.PhysicalType,
			Required: col.Required, Partition: parts[strings.ToLower(col.Name)],
			Sources: col.Sources,
		})
	}
}

func (c *consolidator) applyConstraints(obs []Observation) {
	for _, o := range obs {
		if o.Kind != KindConstraint || o.Constraint == nil {
			continue
		}
		n := c.noun(o.Subject)
		a := n.Attribute(o.Constraint.Attribute)
		if a == nil {
			d := c.disagree(SevWarning, "constraint-unknown-attribute", n.ID.String()+"."+o.Constraint.Attribute,
				"%s test on an attribute no inspected artifact defines", o.Constraint.Constraint)
			d.Sources = []Provenance{o.Provenance}
			continue
		}
		a.Sources = append(a.Sources, o.Provenance)
		if o.Constraint.Constraint != "not_null" {
			continue
		}
		if a.Required != nil && !*a.Required && n.Authoritative {
			d := c.disagree(SevWarning, "required-conflict", n.ID.String()+"."+a.Name,
				".hms says not required, %s asserts not_null", o.Provenance.Where())
			d.Sources = []Provenance{o.Provenance}
			continue
		}
		a.Required = BoolPtr(true)
	}
}

// compareLayers notes, as information, a column whose logical type differs
// between manifestations of one noun. Casting between layers is normal; the
// note makes it visible, it does not call it wrong.
func (c *consolidator) compareLayers() {
	for _, n := range c.nouns {
		if len(n.Manifestations) < 2 {
			continue
		}
		types := map[string]map[LogicalType][]string{}
		var order []string
		for _, m := range n.Manifestations {
			for _, col := range m.Columns {
				if col.Type == Unknown {
					continue
				}
				name := strings.ToLower(col.Name)
				if types[name] == nil {
					types[name] = map[LogicalType][]string{}
					order = append(order, name)
				}
				types[name][col.Type] = append(types[name][col.Type], label(m))
			}
		}
		for _, name := range order {
			if len(types[name]) < 2 {
				continue
			}
			var parts []string
			for _, t := range sortedTypes(types[name]) {
				parts = append(parts, fmt.Sprintf("%s in %s", t, strings.Join(types[name][t], ", ")))
			}
			c.disagree(SevInfo, "layer-type-change", n.ID.String()+"."+name, "%s", strings.Join(parts, "; "))
		}
	}
}

func label(m *Manifestation) string {
	if m.Layer != "" {
		return m.Layer
	}
	return m.Key
}

func sortedTypes(m map[LogicalType][]string) []LogicalType {
	out := make([]LogicalType, 0, len(m))
	for t := range m {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func (c *consolidator) resolve(e Endpoint) string {
	if !e.ID.IsZero() {
		if cid, ok := c.canonIDs[e.ID]; ok {
			return cid.String()
		}
		return e.ID.String()
	}
	if id, ok := c.locIndex[e.Location.Key()]; ok {
		return c.canon(id).String()
	}
	return e.Location.String()
}

func (c *consolidator) resolveLineage(obs []Observation) {
	seen := map[string]bool{}
	for _, o := range obs {
		if o.Kind != KindLineage || o.Lineage == nil {
			continue
		}
		e := Edge{From: c.resolve(o.Lineage.From), To: c.resolve(o.Lineage.To), Via: o.Lineage.Via, Source: o.Provenance}
		k := e.From + "\x00" + e.To + "\x00" + e.Via
		if e.From == e.To || seen[k] {
			continue // a layer feeding the next layer of the same noun
		}
		seen[k] = true
		c.model.Lineage = append(c.model.Lineage, e)
	}
	sort.SliceStable(c.model.Lineage, func(i, j int) bool {
		a, b := c.model.Lineage[i], c.model.Lineage[j]
		if a.To != b.To {
			return a.To < b.To
		}
		return a.From < b.From
	})
}

// resolveReferences reports every reference no inspected artifact provides.
// Every manifestation provides its table; every noun provides itself.
func (c *consolidator) resolveReferences(obs []Observation) {
	provided := map[string]bool{}
	for _, o := range obs {
		if o.Kind == KindProvide && o.Named != nil {
			provided[o.Named.Kind+"\x00"+o.Named.Key] = true
		}
	}
	for key := range c.locIndex {
		provided["table\x00"+key] = true
	}
	for id := range c.canonIDs {
		provided["noun\x00"+id.String()] = true
	}
	for _, o := range obs {
		if o.Kind != KindReference || o.Named == nil {
			continue
		}
		key := o.Named.Key
		if o.Named.Kind == "table" {
			key = strings.ToLower(key)
		}
		if provided[o.Named.Kind+"\x00"+key] {
			continue
		}
		via := ""
		if o.Named.Via != "" {
			via = " (" + o.Named.Via + ")"
		}
		d := c.disagree(SevWarning, "unresolved-reference", o.Named.Key,
			"%s refers to %s %q%s, which no inspected artifact provides",
			o.Provenance.Where(), o.Named.Kind, o.Named.Key, via)
		d.Sources = []Provenance{o.Provenance}
	}
}

func (c *consolidator) passFindings(obs []Observation) {
	for _, o := range obs {
		if o.Kind != KindFinding || o.Finding == nil {
			continue
		}
		subject := o.Subject.String()
		if subject == "" {
			subject = o.Provenance.Where()
		}
		d := c.disagree(o.Finding.Severity, o.Finding.Code, subject, "%s", o.Finding.Message)
		d.Sources = []Provenance{o.Provenance}
	}
}
