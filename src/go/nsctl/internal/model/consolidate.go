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
	obs := make([]Observation, len(observations))
	for i, o := range observations {
		// Scopes fill in binding locations; never mutate the caller's copy.
		if o.Binding != nil {
			b := *o.Binding
			if b.Location == (Location{}) {
				b.Location = LocationOf(b.Values)
			}
			o.Binding = &b
		}
		obs[i] = o
	}
	sort.SliceStable(obs, func(i, j int) bool { return obsLess(obs[i], obs[j]) })

	c := &consolidator{
		parent:  map[ID]ID{},
		reasons: map[ID]string{},
		nouns:   map[ID]*Noun{},
		model:   &Model{},
	}
	c.scope(obs)
	c.link(obs)
	c.buildNouns(obs)
	c.buildBindings(obs)
	c.buildAttributes(obs)
	c.applyConstraints(obs)
	c.compareBindings()
	c.resolveLineage(obs)
	c.resolveReferences(obs)
	c.passFindings(obs)

	for _, n := range c.nouns {
		c.model.Nouns = append(c.model.Nouns, n)
	}
	sort.Slice(c.model.Nouns, func(i, j int) bool {
		return c.model.Nouns[i].ID.String() < c.model.Nouns[j].ID.String()
	})
	SortDisagreements(c.model.Disagreements)
	return c.model
}

// SortDisagreements orders disagreements by severity, subject and message.
func SortDisagreements(ds []Disagreement) {
	sort.SliceStable(ds, func(i, j int) bool {
		a, b := ds[i], ds[j]
		if sevRank(a.Severity) != sevRank(b.Severity) {
			return sevRank(a.Severity) < sevRank(b.Severity)
		}
		if a.Subject != b.Subject {
			return a.Subject < b.Subject
		}
		return a.Message < b.Message
	})
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
	scopes  map[string]string
	parent  map[ID]ID
	reasons map[ID]string
	// locIndex maps a location key to the identity that first claimed it.
	locIndex map[string]ID
	// provides holds the location keys some non-reference binding states.
	provides map[string]bool
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

// scope collects scope observations and fills in the schema of every binding
// in a scoped set.
func (c *consolidator) scope(obs []Observation) {
	c.scopes = map[string]string{}
	for _, o := range obs {
		if o.Kind != KindScope || o.Scope == nil {
			continue
		}
		if prev, ok := c.scopes[o.Scope.Scope]; ok && prev != o.Scope.Schema {
			d := c.disagree(SevWarning, "conflicting-scope", o.Scope.Scope,
				"bound to schema %q and %q", prev, o.Scope.Schema)
			d.Sources = []Provenance{o.Provenance}
			continue
		}
		c.scopes[o.Scope.Scope] = o.Scope.Schema
	}
	for i := range obs {
		b := obs[i].Binding
		if b == nil || b.Scope == "" || b.Location.Schema != "" {
			continue
		}
		if schema, ok := c.scopes[b.Scope]; ok {
			b.Location.Schema = schema
			if _, set := b.Values["schema_name"]; !set {
				values := map[string]Value{"schema_name": V(schema)}
				for k, v := range b.Values {
					values[k] = v
				}
				b.Values = values
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
	c.provides = map[string]bool{}
	for _, o := range obs {
		c.find(o.Subject)
		switch {
		case o.Kind == KindBinding && o.Binding != nil:
			key := o.Binding.Location.Key()
			if key == "" {
				continue
			}
			if !o.Binding.Reference {
				c.provides[key] = true
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

func (c *consolidator) canon(id ID) ID { return c.canonIDs[id] }

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
		case KindNoun, KindAttribute, KindBinding, KindConstraint:
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

func bindingByKey(n *Noun, key string) *Binding {
	for _, b := range n.Bindings {
		if BindingKey(b.Perspective, b.Name) == key {
			return b
		}
	}
	return nil
}

// buildBindings merges every observation of one binding. Observations come
// in authority order, so a value is the strongest source's; a weaker source
// that states a different value for the same key is a disagreement -- the
// case of a declared perspective sidecar and the DDL that contradicts it.
func (c *consolidator) buildBindings(obs []Observation) {
	setBy := map[*Binding]map[string]Provenance{}
	for _, o := range obs {
		if o.Kind != KindBinding || o.Binding == nil {
			continue
		}
		n := c.noun(o.Subject)
		bo := o.Binding
		b := bindingByKey(n, bo.Key())
		if b == nil {
			b = &Binding{
				Perspective: bo.Perspective, Name: bo.Name, Scope: bo.Scope, Location: bo.Location,
				Primary: bo.Primary, Reference: bo.Reference, Values: map[string]Value{},
			}
			n.Bindings = append(n.Bindings, b)
			setBy[b] = map[string]Provenance{}
		} else {
			b.Primary = b.Primary || bo.Primary
			b.Reference = b.Reference && bo.Reference
			if b.Location.Key() == "" {
				b.Location = bo.Location
			}
		}
		for _, k := range sortedValueKeys(bo.Values) {
			v := bo.Values[k]
			cur, ok := b.Values[k]
			switch {
			case !ok:
				b.Values[k] = v
				setBy[b][k] = o.Provenance
			case !cur.Equal(v):
				prev := setBy[b][k]
				d := c.disagree(SevWarning, "perspective-value-conflict", n.ID.String()+"@"+b.Label(),
					"%s is %q in %s but %q in %s", k, cur.String(), prev.Where(), v.String(), o.Provenance.Where())
				d.Sources = []Provenance{prev, o.Provenance}
			}
		}
		b.Sources = appendUnique(b.Sources, o.Provenance)
	}
	for _, n := range c.nouns {
		sort.SliceStable(n.Bindings, func(i, j int) bool {
			return n.Bindings[i].Label() < n.Bindings[j].Label()
		})
	}
}

func sortedValueKeys(m map[string]Value) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type columnSet struct {
	prov    Provenance
	cols    []*AttrObs
	ordered bool
}

func (c *consolidator) buildAttributes(obs []Observation) {
	// Columns per binding, per source file, in authority order.
	sets := map[*Binding][]*columnSet{}
	owner := map[*Binding]*Noun{}
	hmsAttrs := map[ID][]Observation{}
	for _, o := range obs {
		if o.Kind != KindAttribute || o.Attr == nil {
			continue
		}
		n := c.noun(o.Subject)
		if o.Attr.Binding == "" {
			hmsAttrs[n.ID] = append(hmsAttrs[n.ID], o)
			continue
		}
		b := bindingByKey(n, o.Attr.Binding)
		if b == nil {
			persp, name, _ := strings.Cut(o.Attr.Binding, ":")
			b = &Binding{Perspective: persp, Name: name, Values: map[string]Value{}}
			n.Bindings = append(n.Bindings, b)
		}
		owner[b] = n
		var set *columnSet
		for _, s := range sets[b] {
			if s.prov.Repo == o.Provenance.Repo && s.prov.File == o.Provenance.File {
				set = s
			}
		}
		if set == nil {
			set = &columnSet{prov: o.Provenance}
			sets[b] = append(sets[b], set)
		}
		set.cols = append(set.cols, o.Attr)
		set.ordered = set.ordered || o.Attr.Position > 0
	}

	for b, list := range sets {
		var ordered, partial []*columnSet
		for _, s := range list {
			if s.ordered {
				sort.SliceStable(s.cols, func(i, j int) bool { return s.cols[i].Position < s.cols[j].Position })
				ordered = append(ordered, s)
			} else {
				sort.SliceStable(s.cols, func(i, j int) bool { return s.cols[i].Name < s.cols[j].Name })
				partial = append(partial, s)
			}
		}
		if len(ordered) > 0 {
			top := ordered[0]
			for _, a := range top.cols {
				b.Columns = append(b.Columns, columnOf(a, top.prov))
			}
			for _, other := range ordered[1:] {
				c.compareColumns(b, top, other)
			}
		}
		// Unordered values (a sidecar) overlay columns by name; a sidecar's
		// value outranks an inferred one, and contradicting it is reported.
		for _, s := range partial {
			for _, a := range s.cols {
				col := b.Column(a.Name)
				if col == nil {
					b.Columns = append(b.Columns, columnOf(a, s.prov))
					continue
				}
				c.overlayColumn(owner[b], b, col, a, s.prov)
			}
		}
		b.Sources = appendUnique(b.Sources, list[0].prov)
	}

	for _, n := range c.nouns {
		if attrs, ok := hmsAttrs[n.ID]; ok {
			c.nounAttributesFromHMS(n, attrs)
			continue
		}
		if n.Authoritative {
			continue // an .hms noun with no attributes has none, whatever its views select
		}
		c.nounAttributesFromBinding(n)
	}
}

func columnOf(a *AttrObs, p Provenance) Column {
	col := Column{Name: a.Name, Type: a.Type, Required: a.Required, Sources: []Provenance{p}}
	if col.Type == "" {
		col.Type = Unknown
	}
	if len(a.Values) > 0 {
		col.Values = map[string]Value{}
		for k, v := range a.Values {
			col.Values[k] = v
		}
	}
	return col
}

// overlayColumn applies a partial source's values to a column. The partial
// source is stronger when its authority is higher (a declared sidecar over
// inferred DDL); either way, a value the two state differently is reported.
func (c *consolidator) overlayColumn(n *Noun, b *Binding, col *Column, a *AttrObs, p Provenance) {
	stronger := len(col.Sources) == 0 || p.Authority > col.Sources[0].Authority
	if col.Values == nil {
		col.Values = map[string]Value{}
	}
	for _, k := range sortedValueKeys(a.Values) {
		v := a.Values[k]
		cur, ok := col.Values[k]
		if ok && !cur.Equal(v) {
			d := c.disagree(SevWarning, "perspective-value-conflict", n.ID.String()+"#"+col.Name+"@"+b.Label(),
				"%s is %q in %s but %q in %s", k, cur.String(), col.Sources[0].Where(), v.String(), p.Where())
			d.Sources = []Provenance{col.Sources[0], p}
		}
		if !ok || stronger {
			col.Values[k] = v
		}
	}
	if stronger && a.Type != "" && a.Type != Unknown {
		col.Type = a.Type
	}
	col.Sources = append(col.Sources, p)
}

func (c *consolidator) compareColumns(b *Binding, top, other *columnSet) {
	subject := b.Location.String()
	if subject == "" {
		subject = b.Label()
	}
	if len(top.cols) != len(other.cols) {
		d := c.disagree(SevError, "column-count", subject,
			"%s lists %d columns, %s lists %d", top.prov.Where(), len(top.cols), other.prov.Where(), len(other.cols))
		d.Sources = []Provenance{top.prov, other.prov}
		return
	}
	for i := range top.cols {
		x, y := top.cols[i], other.cols[i]
		if !strings.EqualFold(x.Name, y.Name) {
			d := c.disagree(SevError, "column-order", subject,
				"column %d is %q in %s but %q in %s", i+1, x.Name, top.prov.Where(), y.Name, other.prov.Where())
			d.Sources = []Provenance{top.prov, other.prov}
			continue
		}
		if x.Type != Unknown && y.Type != Unknown && x.Type != "" && y.Type != "" && x.Type != y.Type {
			d := c.disagree(SevWarning, "column-type", subject+"#"+x.Name,
				"%s in %s but %s in %s", x.Type, top.prov.Where(), y.Type, other.prov.Where())
			d.Sources = []Provenance{top.prov, other.prov}
		}
	}
	for i := range b.Columns {
		b.Columns[i].Sources = append(b.Columns[i].Sources, other.prov)
	}
	b.Sources = appendUnique(b.Sources, other.prov)
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

// nounAttributesFromBinding gives a noun no .hms schema defines the
// attributes of its primary binding: core type and requiredness only. Every
// physical detail stays a perspective value on the binding's column.
func (c *consolidator) nounAttributesFromBinding(n *Noun) {
	b := n.Primary()
	if b == nil {
		return
	}
	for _, col := range b.Columns {
		n.Attributes = append(n.Attributes, &Attribute{
			Name: col.Name, Type: col.Type, Required: col.Required, Sources: col.Sources,
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
			d := c.disagree(SevWarning, "constraint-unknown-attribute", n.ID.String()+"#"+o.Constraint.Attribute,
				"%s test on an attribute no inspected artifact defines", o.Constraint.Constraint)
			d.Sources = []Provenance{o.Provenance}
			continue
		}
		a.Sources = append(a.Sources, o.Provenance)
		if o.Constraint.Constraint != "not_null" {
			continue
		}
		if a.Required != nil && !*a.Required && n.Authoritative {
			d := c.disagree(SevWarning, "required-conflict", n.ID.String()+"#"+a.Name,
				".hms says not required, %s asserts not_null", o.Provenance.Where())
			d.Sources = []Provenance{o.Provenance}
			continue
		}
		a.Required = BoolPtr(true)
	}
}

// compareBindings notes, as information, an attribute whose core type
// differs between bindings of one noun. Casting between layers is normal;
// the note makes it visible, it does not call it wrong.
func (c *consolidator) compareBindings() {
	for _, n := range c.nouns {
		if len(n.Bindings) < 2 {
			continue
		}
		types := map[string]map[LogicalType][]string{}
		var order []string
		for _, b := range n.Bindings {
			for _, col := range b.Columns {
				if col.Type == Unknown || col.Type == "" {
					continue
				}
				name := strings.ToLower(col.Name)
				if types[name] == nil {
					types[name] = map[LogicalType][]string{}
					order = append(order, name)
				}
				types[name][col.Type] = append(types[name][col.Type], b.Label())
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
			c.disagree(SevInfo, "layer-type-change", n.ID.String()+"#"+name, "%s", strings.Join(parts, "; "))
		}
	}
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
// Every binding that is not a reference provides its table; every noun
// provides itself.
func (c *consolidator) resolveReferences(obs []Observation) {
	provided := map[string]bool{}
	for _, o := range obs {
		if o.Kind == KindProvide && o.Named != nil {
			provided[o.Named.Kind+"\x00"+o.Named.Key] = true
		}
	}
	for key := range c.provides {
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
