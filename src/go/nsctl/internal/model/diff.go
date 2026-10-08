package model

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// ChangeKind names a semantic change. The names follow hmd-lib-ns-model's
// semantic diff spec (Concept*, Property*, Relationship*, PerspectiveValue*);
// the binding, lineage and disagreement kinds are this inspector's own.
type ChangeKind string

const (
	ConceptAdded                ChangeKind = "ConceptAdded"
	ConceptRemoved              ChangeKind = "ConceptRemoved"
	MetatypeChanged             ChangeKind = "MetatypeChanged"
	RelationshipEndpointChanged ChangeKind = "RelationshipEndpointChanged"
	PropertyAdded               ChangeKind = "PropertyAdded"
	PropertyRemoved             ChangeKind = "PropertyRemoved"
	PropertyTypeChanged         ChangeKind = "PropertyTypeChanged"
	RequirednessChanged         ChangeKind = "RequirednessChanged"
	EnumChanged                 ChangeKind = "EnumChanged"
	PerspectiveBindingAdded     ChangeKind = "PerspectiveBindingAdded"
	PerspectiveBindingRemoved   ChangeKind = "PerspectiveBindingRemoved"
	PerspectiveValueAdded       ChangeKind = "PerspectiveValueAdded"
	PerspectiveValueChanged     ChangeKind = "PerspectiveValueChanged"
	PerspectiveValueRemoved     ChangeKind = "PerspectiveValueRemoved"
	LineageAdded                ChangeKind = "LineageAdded"
	LineageRemoved              ChangeKind = "LineageRemoved"
	DisagreementAdded           ChangeKind = "DisagreementAdded"
	DisagreementResolved        ChangeKind = "DisagreementResolved"
)

// Change is one semantic difference.
type Change struct {
	// SemanticID is hmd-lib-ns-model's identity grammar: <ns>.<name>,
	// <ns>.<name>#<attribute>, and a perspective overlay
	// <...>@<perspective>, with :<binding> for one of several bindings.
	SemanticID string     `json:"semantic_id"`
	Kind       ChangeKind `json:"kind"`
	// Key is the perspective value key, or ref_from/ref_to.
	Key  string `json:"key,omitempty"`
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

func (c Change) String() string {
	s := c.SemanticID + "  " + string(c.Kind)
	if c.Key != "" {
		s += " " + c.Key
	}
	switch {
	case c.From != "" && c.To != "":
		s += ": " + c.From + " -> " + c.To
	case c.To != "":
		s += ": " + c.To
	case c.From != "":
		s += ": " + c.From
	}
	return s
}

// Diff compares two models by identity and reports what a person would call
// the change -- an attribute's type, a perspective value, a new disagreement
// -- never which files moved.
func Diff(before, after *Model) []Change {
	var out []Change
	old, cur := nounIndex(before), nounIndex(after)
	for _, id := range unionKeys(old, cur) {
		a, b := old[id], cur[id]
		switch {
		case a == nil:
			out = append(out, Change{SemanticID: id, Kind: ConceptAdded, To: describeNoun(b)})
		case b == nil:
			out = append(out, Change{SemanticID: id, Kind: ConceptRemoved, From: describeNoun(a)})
		default:
			out = append(out, diffNoun(a, b)...)
		}
	}
	out = append(out, diffEdges(before.Lineage, after.Lineage)...)
	out = append(out, diffDisagreements(before.Disagreements, after.Disagreements)...)
	return out
}

func nounIndex(m *Model) map[string]*Noun {
	out := map[string]*Noun{}
	if m == nil {
		return out
	}
	for _, n := range m.Nouns {
		out[n.ID.String()] = n
	}
	return out
}

func unionKeys[V any](a, b map[string]V) []string {
	seen := map[string]bool{}
	var keys []string
	for _, m := range []map[string]V{a, b} {
		for k := range m {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	sort.Strings(keys)
	return keys
}

func describeNoun(n *Noun) string {
	s := fmt.Sprintf("%s, %d attributes", n.Metatype, len(n.Attributes))
	if len(n.Bindings) > 0 {
		var labels []string
		for _, b := range n.Bindings {
			labels = append(labels, b.Label())
		}
		s += ", @" + strings.Join(labels, " @")
	}
	return s
}

func describeAttr(a *Attribute) string {
	s := string(a.Type)
	switch {
	case a.Required == nil:
	case *a.Required:
		s += ", required"
	default:
		s += ", not required"
	}
	return s
}

func reqString(b *bool) string {
	switch {
	case b == nil:
		return "unknown"
	case *b:
		return "required"
	}
	return "not required"
}

func diffNoun(a, b *Noun) []Change {
	id := b.ID.String()
	var out []Change
	if a.Metatype != b.Metatype {
		out = append(out, Change{SemanticID: id, Kind: MetatypeChanged, From: string(a.Metatype), To: string(b.Metatype)})
	}
	if a.RefFrom != b.RefFrom {
		out = append(out, Change{SemanticID: id, Kind: RelationshipEndpointChanged, Key: "ref_from", From: a.RefFrom, To: b.RefFrom})
	}
	if a.RefTo != b.RefTo {
		out = append(out, Change{SemanticID: id, Kind: RelationshipEndpointChanged, Key: "ref_to", From: a.RefTo, To: b.RefTo})
	}

	oa, ob := attrIndex(a.Attributes), attrIndex(b.Attributes)
	for _, name := range unionKeys(oa, ob) {
		x, y := oa[name], ob[name]
		sid := id + "#" + name
		switch {
		case x == nil:
			out = append(out, Change{SemanticID: sid, Kind: PropertyAdded, To: describeAttr(y)})
		case y == nil:
			out = append(out, Change{SemanticID: sid, Kind: PropertyRemoved, From: describeAttr(x)})
		default:
			if x.Type != y.Type {
				out = append(out, Change{SemanticID: sid, Kind: PropertyTypeChanged, From: string(x.Type), To: string(y.Type)})
			}
			if reqString(x.Required) != reqString(y.Required) {
				out = append(out, Change{SemanticID: sid, Kind: RequirednessChanged, From: reqString(x.Required), To: reqString(y.Required)})
			}
			if strings.Join(x.EnumDef, ",") != strings.Join(y.EnumDef, ",") {
				out = append(out, Change{SemanticID: sid, Kind: EnumChanged,
					From: strings.Join(x.EnumDef, ", "), To: strings.Join(y.EnumDef, ", ")})
			}
		}
	}

	ba, bb := bindingIndex(a.Bindings), bindingIndex(b.Bindings)
	for _, label := range unionKeys(ba, bb) {
		x, y := ba[label], bb[label]
		sid := id + "@" + label
		switch {
		case x == nil:
			out = append(out, Change{SemanticID: sid, Kind: PerspectiveBindingAdded, To: y.Location.String()})
		case y == nil:
			out = append(out, Change{SemanticID: sid, Kind: PerspectiveBindingRemoved, From: x.Location.String()})
		default:
			out = append(out, diffValues(sid, x.Values, y.Values)...)
			out = append(out, diffColumns(id, label, x, y)...)
		}
	}
	return out
}

func attrIndex(attrs []*Attribute) map[string]*Attribute {
	out := map[string]*Attribute{}
	for _, a := range attrs {
		out[a.Name] = a
	}
	return out
}

func bindingIndex(bs []*Binding) map[string]*Binding {
	out := map[string]*Binding{}
	for _, b := range bs {
		out[b.Label()] = b
	}
	return out
}

// valueString renders a perspective value for a change: the definition with
// its parameters when it has one (DECIMAL(10,2)), else the value.
func valueString(v Value) string {
	s := v.String()
	if v.Definition != "" {
		s = v.Definition
	}
	if len(v.Parameters) > 0 {
		keys := make([]string, 0, len(v.Parameters))
		for k := range v.Parameters {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var parts []string
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s=%v", k, v.Parameters[k]))
		}
		s += "(" + strings.Join(parts, ",") + ")"
	}
	return s
}

func diffValues(sid string, a, b map[string]Value) []Change {
	var out []Change
	for _, k := range unionKeys(a, b) {
		x, inA := a[k]
		y, inB := b[k]
		switch {
		case !inA:
			out = append(out, Change{SemanticID: sid, Kind: PerspectiveValueAdded, Key: k, To: valueString(y)})
		case !inB:
			out = append(out, Change{SemanticID: sid, Kind: PerspectiveValueRemoved, Key: k, From: valueString(x)})
		case !x.Equal(y):
			out = append(out, Change{SemanticID: sid, Kind: PerspectiveValueChanged, Key: k, From: valueString(x), To: valueString(y)})
		}
	}
	return out
}

// diffColumns reports attribute-level perspective values, at
// <noun>#<attribute>@<binding>. A column that appears or disappears in a
// binding is reported once, as its values being added or removed.
func diffColumns(id, label string, a, b *Binding) []Change {
	ia, ib := map[string]*Column{}, map[string]*Column{}
	for i := range a.Columns {
		ia[strings.ToLower(a.Columns[i].Name)] = &a.Columns[i]
	}
	for i := range b.Columns {
		ib[strings.ToLower(b.Columns[i].Name)] = &b.Columns[i]
	}
	var out []Change
	for _, name := range unionKeys(ia, ib) {
		x, y := ia[name], ib[name]
		sid := id + "#" + name + "@" + label
		var xv, yv map[string]Value
		if x != nil {
			xv = x.Values
		}
		if y != nil {
			yv = y.Values
		}
		changes := diffValues(sid, xv, yv)
		if len(changes) == 0 && (x == nil) != (y == nil) {
			// A column with no values of its own still appeared or went.
			if x == nil {
				changes = append(changes, Change{SemanticID: sid, Kind: PerspectiveValueAdded, Key: "column", To: string(y.Type)})
			} else {
				changes = append(changes, Change{SemanticID: sid, Kind: PerspectiveValueRemoved, Key: "column", From: string(x.Type)})
			}
		}
		out = append(out, changes...)
	}
	return out
}

func diffEdges(a, b []Edge) []Change {
	key := func(e Edge) string { return e.From + " -> " + e.To + " (" + e.Via + ")" }
	ia, ib := map[string]Edge{}, map[string]Edge{}
	for _, e := range a {
		ia[key(e)] = e
	}
	for _, e := range b {
		ib[key(e)] = e
	}
	var out []Change
	for _, k := range unionKeys(ia, ib) {
		_, inA := ia[k]
		_, inB := ib[k]
		switch {
		case !inA:
			out = append(out, Change{SemanticID: k, Kind: LineageAdded})
		case !inB:
			out = append(out, Change{SemanticID: k, Kind: LineageRemoved})
		}
	}
	return out
}

// lineRef matches the ":<line>" of a file:line reference.
var lineRef = regexp.MustCompile(`(\.ya?ml|\.sql|\.hms|\.json|\.py):\d+`)

// diffDisagreements reports disagreements that appeared or went away. A
// disagreement's identity ignores line numbers: an edit that only moves a
// finding down a line has not changed it.
func diffDisagreements(a, b []Disagreement) []Change {
	key := func(d Disagreement) string {
		return lineRef.ReplaceAllString(string(d.Severity)+" "+d.Subject+" ["+d.Code+"] "+d.Message, "$1")
	}
	ia, ib := map[string]bool{}, map[string]bool{}
	for _, d := range a {
		ia[key(d)] = true
	}
	for _, d := range b {
		ib[key(d)] = true
	}
	var out []Change
	for _, k := range unionKeys(ia, ib) {
		switch {
		case !ia[k]:
			out = append(out, Change{SemanticID: k, Kind: DisagreementAdded})
		case !ib[k]:
			out = append(out, Change{SemanticID: k, Kind: DisagreementResolved})
		}
	}
	return out
}
