package model

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// ChangeKind is what happened to an element between two models.
type ChangeKind string

const (
	Added   ChangeKind = "added"
	Removed ChangeKind = "removed"
	Changed ChangeKind = "changed"
)

// Change is one semantic difference. Subject is the element's identity:
// a noun, noun.attribute, a manifestation (noun@location) or one of its
// columns, a lineage edge, or a disagreement.
type Change struct {
	Subject string     `json:"subject"`
	Kind    ChangeKind `json:"kind"`
	// Field is what changed for a Changed element: type, required, enum_def,
	// partition, physical_type, ref_from, ref_to.
	Field string `json:"field,omitempty"`
	From  string `json:"from,omitempty"`
	To    string `json:"to,omitempty"`
}

func (c Change) String() string {
	switch c.Kind {
	case Changed:
		return fmt.Sprintf("%s  %s: %s -> %s", c.Subject, c.Field, orNone(c.From), orNone(c.To))
	case Added:
		if c.To != "" {
			return fmt.Sprintf("%s  added: %s", c.Subject, c.To)
		}
	case Removed:
		if c.From != "" {
			return fmt.Sprintf("%s  removed: %s", c.Subject, c.From)
		}
	}
	return fmt.Sprintf("%s  %s", c.Subject, c.Kind)
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

// Diff compares two models by identity. It reports what a person would call
// the change -- an attribute's type, a column added to one layer, a new
// disagreement -- never which files moved.
func Diff(before, after *Model) []Change {
	var out []Change
	old, cur := nounIndex(before), nounIndex(after)
	for _, id := range unionKeys(old, cur) {
		a, b := old[id], cur[id]
		switch {
		case a == nil:
			out = append(out, Change{Subject: id, Kind: Added, To: describeNoun(b)})
		case b == nil:
			out = append(out, Change{Subject: id, Kind: Removed, From: describeNoun(a)})
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
	if len(n.Manifestations) > 0 {
		s += fmt.Sprintf(", %d manifestations", len(n.Manifestations))
	}
	return s
}

func describeAttr(a *Attribute) string {
	s := string(a.Type)
	if a.PhysicalType != "" {
		s += " (" + a.PhysicalType + ")"
	}
	switch {
	case a.Required == nil:
	case *a.Required:
		s += ", required"
	default:
		s += ", not required"
	}
	if a.Partition {
		s += ", partition"
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
	if a.RefFrom != b.RefFrom {
		out = append(out, Change{Subject: id, Kind: Changed, Field: "ref_from", From: a.RefFrom, To: b.RefFrom})
	}
	if a.RefTo != b.RefTo {
		out = append(out, Change{Subject: id, Kind: Changed, Field: "ref_to", From: a.RefTo, To: b.RefTo})
	}
	if a.Metatype != b.Metatype {
		out = append(out, Change{Subject: id, Kind: Changed, Field: "metatype", From: string(a.Metatype), To: string(b.Metatype)})
	}

	oa, ob := attrIndex(a.Attributes), attrIndex(b.Attributes)
	for _, name := range unionKeys(oa, ob) {
		x, y := oa[name], ob[name]
		subject := id + "." + name
		switch {
		case x == nil:
			out = append(out, Change{Subject: subject, Kind: Added, To: describeAttr(y)})
		case y == nil:
			out = append(out, Change{Subject: subject, Kind: Removed, From: describeAttr(x)})
		default:
			out = append(out, diffAttr(subject, x, y)...)
		}
	}

	// Manifestations, and the columns of every one that is not already the
	// source of the noun's attributes.
	primaryKey := ""
	if !b.Authoritative {
		if p := primary(b); p != nil {
			primaryKey = p.Key
		}
	}
	ma, mb := manifestIndex(a.Manifestations), manifestIndex(b.Manifestations)
	for _, key := range unionKeys(ma, mb) {
		x, y := ma[key], mb[key]
		switch {
		case x == nil:
			out = append(out, Change{Subject: id + "@" + manifestLabel(y), Kind: Added, To: y.Tech})
		case y == nil:
			out = append(out, Change{Subject: id + "@" + manifestLabel(x), Kind: Removed, From: x.Tech})
		case key != primaryKey:
			out = append(out, diffColumns(id+"@"+manifestLabel(y), x.Columns, y.Columns)...)
		}
	}
	return out
}

func manifestLabel(m *Manifestation) string {
	if l := m.Location.String(); l != "" {
		return l
	}
	return m.Key
}

func attrIndex(attrs []*Attribute) map[string]*Attribute {
	out := map[string]*Attribute{}
	for _, a := range attrs {
		out[a.Name] = a
	}
	return out
}

func manifestIndex(ms []*Manifestation) map[string]*Manifestation {
	out := map[string]*Manifestation{}
	for _, m := range ms {
		out[m.Key] = m
	}
	return out
}

func diffAttr(subject string, x, y *Attribute) []Change {
	var out []Change
	if x.Type != y.Type {
		out = append(out, Change{Subject: subject, Kind: Changed, Field: "type", From: string(x.Type), To: string(y.Type)})
	} else if x.PhysicalType != y.PhysicalType && x.PhysicalType != "" && y.PhysicalType != "" {
		out = append(out, Change{Subject: subject, Kind: Changed, Field: "physical_type", From: x.PhysicalType, To: y.PhysicalType})
	}
	if reqString(x.Required) != reqString(y.Required) {
		out = append(out, Change{Subject: subject, Kind: Changed, Field: "required", From: reqString(x.Required), To: reqString(y.Required)})
	}
	if x.Partition != y.Partition {
		out = append(out, Change{Subject: subject, Kind: Changed, Field: "partition", From: fmt.Sprint(x.Partition), To: fmt.Sprint(y.Partition)})
	}
	if strings.Join(x.EnumDef, ",") != strings.Join(y.EnumDef, ",") {
		out = append(out, Change{Subject: subject, Kind: Changed, Field: "enum_def",
			From: strings.Join(x.EnumDef, ", "), To: strings.Join(y.EnumDef, ", ")})
	}
	return out
}

func diffColumns(subject string, a, b []Column) []Change {
	ia, ib := map[string]*Column{}, map[string]*Column{}
	for i := range a {
		ia[strings.ToLower(a[i].Name)] = &a[i]
	}
	for i := range b {
		ib[strings.ToLower(b[i].Name)] = &b[i]
	}
	var out []Change
	for _, name := range unionKeys(ia, ib) {
		x, y := ia[name], ib[name]
		s := subject + "." + name
		switch {
		case x == nil:
			out = append(out, Change{Subject: s, Kind: Added, To: colString(y)})
		case y == nil:
			out = append(out, Change{Subject: s, Kind: Removed, From: colString(x)})
		case x.Type != y.Type:
			out = append(out, Change{Subject: s, Kind: Changed, Field: "type", From: colString(x), To: colString(y)})
		case x.PhysicalType != y.PhysicalType:
			out = append(out, Change{Subject: s, Kind: Changed, Field: "physical_type", From: x.PhysicalType, To: y.PhysicalType})
		}
	}
	return out
}

func colString(c *Column) string {
	if c.PhysicalType != "" {
		return fmt.Sprintf("%s (%s)", c.Type, c.PhysicalType)
	}
	return string(c.Type)
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
			out = append(out, Change{Subject: "lineage " + k, Kind: Added})
		case !inB:
			out = append(out, Change{Subject: "lineage " + k, Kind: Removed})
		}
	}
	return out
}

// lineRef matches the ":<line>" of a file:line reference.
var lineRef = regexp.MustCompile(`(\.ya?ml|\.sql|\.hms|\.json):\d+`)

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
			out = append(out, Change{Subject: "disagreement " + k, Kind: Added})
		case !ib[k]:
			out = append(out, Change{Subject: "disagreement " + k, Kind: Removed})
		}
	}
	return out
}
