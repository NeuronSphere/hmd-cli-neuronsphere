// Package model is the canonical data model that nsctl inspect builds
// (NERD032): nouns, their attributes, and the relationships between them,
// exactly as an HMD language pack schema (.hms) states them, plus perspective
// bindings -- the technology-specific metadata kept beside the core schema in
// <name>.<perspective>.hms sidecars (SPEC007) -- and two things that describe
// the inspection rather than any noun: provenance and lineage.
//
// The package knows .hms and the perspective value shape, nothing else about
// NeuronSphere. What a perspective's keys mean belongs to its definition
// (internal/perspective); layer naming, transform YAML and Jinja belong to
// inspectors, which reach this package only as Observations.
package model

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ID is a noun's identity: an .hms namespace and name. A noun learned from
// .hms has the identity .hms gives it; any other noun has the one its
// inspector assigned, with the reason recorded on the observation.
type ID struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

func (id ID) String() string {
	if id.Namespace == "" {
		return id.Name
	}
	return id.Namespace + "." + id.Name
}

// IsZero reports whether the identity is unset.
func (id ID) IsZero() bool { return id.Namespace == "" && id.Name == "" }

// ParseID splits a fully qualified name at its last dot, the way .hms
// resolves ref_from and ref_to.
func ParseID(fqn string) ID {
	if i := strings.LastIndex(fqn, "."); i >= 0 {
		return ID{Namespace: fqn[:i], Name: fqn[i+1:]}
	}
	return ID{Name: fqn}
}

// Metatype is the .hms metatype.
type Metatype string

const (
	MetaNoun         Metatype = "noun"
	MetaRelationship Metatype = "relationship"
)

// LogicalType is an attribute's core type: always an .hms runtime type, or
// Unknown when nothing stated one. A physical type with no exact .hms
// equivalent (DATE, DECIMAL) has its nearest .hms type here and its exact
// type as a perspective value.
type LogicalType string

const (
	String     LogicalType = "string"
	Integer    LogicalType = "integer"
	Float      LogicalType = "float"
	Bool       LogicalType = "bool"
	Enum       LogicalType = "enum"
	Timestamp  LogicalType = "timestamp"
	Epoch      LogicalType = "epoch"
	Collection LogicalType = "collection"
	Mapping    LogicalType = "mapping"
	Blob       LogicalType = "blob"
	// Unknown is an attribute whose type no inspected artifact states.
	Unknown LogicalType = "unknown"
)

// IsHMS reports whether .hms can express the type.
func (t LogicalType) IsHMS() bool { return t != Unknown && t != "" }

// HMSType maps an .hms attribute type, including the aliases found in real
// language packs, to its logical type. ok is false for a type the HMD runtime
// would reject.
func HMSType(s string) (t LogicalType, ok bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "string":
		return String, true
	case "integer", "int":
		return Integer, true
	case "float":
		return Float, true
	case "bool", "boolean":
		return Bool, true
	case "enum":
		return Enum, true
	case "timestamp":
		return Timestamp, true
	case "epoch":
		return Epoch, true
	case "collection":
		return Collection, true
	case "mapping":
		return Mapping, true
	case "blob":
		return Blob, true
	}
	return Unknown, false
}

// Value is one perspective value, in the Modeler's shape: the value itself
// (an enum id, text, or a bool), and for an enum value its definition and
// parameters.
type Value struct {
	Value      any            `json:"value"`
	Definition string         `json:"definition,omitempty"`
	Parameters map[string]any `json:"parameters,omitempty"`
}

// V is a plain value.
func V(x any) Value { return Value{Value: x} }

func (v Value) String() string {
	switch x := v.Value.(type) {
	case nil:
		return ""
	case string:
		return x
	default:
		return fmt.Sprint(x)
	}
}

// Equal compares two values by content.
func (v Value) Equal(w Value) bool {
	a, _ := json.Marshal(v)
	b, _ := json.Marshal(w)
	return string(a) == string(b)
}

// Location is where a binding lives physically. It is how consolidation
// recognises two inspectors describing the same table, and is read from a
// binding's catalog/database, schema_name and table_name values. Catalog is
// ignored when locations are compared, because different tools reach the
// same table through different catalogs.
type Location struct {
	Catalog string `json:"catalog,omitempty"`
	Schema  string `json:"schema,omitempty"`
	Table   string `json:"table,omitempty"`
}

// Key is the comparison key: lower-case schema.table. Empty when the schema
// is not known, since a bare table name is too weak to link on.
func (l Location) Key() string {
	if l.Schema == "" || l.Table == "" {
		return ""
	}
	return strings.ToLower(l.Schema + "." + l.Table)
}

func (l Location) String() string {
	parts := []string{}
	for _, p := range []string{l.Catalog, l.Schema, l.Table} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, ".")
}

// LocationOf reads a location from perspective values by the conventional
// keys every location-bearing perspective uses.
func LocationOf(values map[string]Value) Location {
	l := Location{Schema: values["schema_name"].String(), Table: values["table_name"].String()}
	if c := values["catalog"].String(); c != "" {
		l.Catalog = c
	} else {
		l.Catalog = values["database"].String()
	}
	return l
}

// Model is the consolidated result of one inspection.
type Model struct {
	Nouns         []*Noun        `json:"nouns"`
	Lineage       []Edge         `json:"lineage,omitempty"`
	Disagreements []Disagreement `json:"disagreements,omitempty"`
}

// Noun returns the noun with the identity, or nil.
func (m *Model) Noun(id ID) *Noun {
	for _, n := range m.Nouns {
		if n.ID == id {
			return n
		}
	}
	return nil
}

// Noun is a noun or relationship, in .hms terms, with its perspective
// bindings.
type Noun struct {
	ID          ID       `json:"id"`
	Metatype    Metatype `json:"metatype"`
	Description *string  `json:"description,omitempty"`
	// RefFrom and RefTo are fully qualified noun names, for a relationship.
	RefFrom string `json:"ref_from,omitempty"`
	RefTo   string `json:"ref_to,omitempty"`
	// Authoritative is true when an .hms schema defines the noun; its
	// attributes are then the schema's, not any binding's.
	Authoritative bool         `json:"authoritative"`
	Attributes    []*Attribute `json:"attributes,omitempty"`
	// Bindings are the noun's perspective values, one per perspective and
	// binding name.
	Bindings []*Binding `json:"bindings,omitempty"`
	// Aliases are the other identities consolidation folded into this one,
	// each with why.
	Aliases []Alias        `json:"aliases,omitempty"`
	Sources []Provenance   `json:"sources,omitempty"`
	Extra   map[string]any `json:"extra,omitempty"`
}

// Attribute returns the attribute with the name, or nil.
func (n *Noun) Attribute(name string) *Attribute {
	for _, a := range n.Attributes {
		if a.Name == name {
			return a
		}
	}
	return nil
}

// Binding returns the noun's binding of a perspective by name, or nil.
func (n *Noun) Binding(perspective, name string) *Binding {
	for _, b := range n.Bindings {
		if b.Perspective == perspective && b.Name == name {
			return b
		}
	}
	return nil
}

// Primary is the binding whose columns are the noun's attributes when no
// .hms schema defines it: the one an inspector marked primary, else the one
// stated with the highest authority, else the first.
func (n *Noun) Primary() *Binding {
	var best *Binding
	bestAuth := Authority(-1)
	for _, b := range n.Bindings {
		if len(b.Columns) == 0 {
			continue
		}
		if b.Primary {
			return b
		}
		a := Authority(0)
		for _, s := range b.Sources {
			if s.Authority > a {
				a = s.Authority
			}
		}
		if a > bestAuth {
			best, bestAuth = b, a
		}
	}
	return best
}

// Alias records that an identity was merged into a noun.
type Alias struct {
	ID     ID     `json:"id"`
	Reason string `json:"reason"`
}

// Attribute is an .hms attribute, and nothing else.
type Attribute struct {
	Name string      `json:"name"`
	Type LogicalType `json:"type"`
	// HMSType is the type exactly as an .hms file spelled it, so that an
	// alias such as "int" survives a round trip.
	HMSType string `json:"hms_type,omitempty"`
	// Required is a tri-state: nil is unknown (NERD032 SPEC003).
	Required    *bool          `json:"required,omitempty"`
	EnumDef     []string       `json:"enum_def,omitempty"`
	Description *string        `json:"description,omitempty"`
	Extra       map[string]any `json:"extra,omitempty"`
	Sources     []Provenance   `json:"sources,omitempty"`
}

// Binding is one realisation of a noun within a perspective: the values a
// <name>.<perspective>.hms sidecar holds for it. Name is the value of the
// definition's binding_key ("final", "model"), empty for a perspective that
// allows one binding per noun.
type Binding struct {
	Perspective string `json:"perspective"`
	Name        string `json:"name,omitempty"`
	// Scope groups bindings whose schema is decided elsewhere, e.g. the
	// models of one dbt project; a scope observation fills the schema in.
	Scope    string   `json:"scope,omitempty"`
	Location Location `json:"location"`
	// Primary marks the binding whose columns are the noun's attributes.
	Primary bool `json:"primary,omitempty"`
	// Reference is true when every source only refers to this binding (a dbt
	// source, an INSERT target): it links, it does not provide.
	Reference bool             `json:"reference,omitempty"`
	Values    map[string]Value `json:"values,omitempty"`
	Columns   []Column         `json:"columns,omitempty"`
	Sources   []Provenance     `json:"sources,omitempty"`
}

// Label is perspective, or perspective:name.
func (b *Binding) Label() string {
	if b.Name == "" {
		return b.Perspective
	}
	return b.Perspective + ":" + b.Name
}

// Column returns a column by name, or nil.
func (b *Binding) Column(name string) *Column {
	for i := range b.Columns {
		if strings.EqualFold(b.Columns[i].Name, name) {
			return &b.Columns[i]
		}
	}
	return nil
}

// Column is an attribute as one binding has it: its core type (from the
// perspective's datatype, when one is stated) and its attribute-level
// perspective values.
type Column struct {
	Name     string           `json:"name"`
	Type     LogicalType      `json:"type"`
	Required *bool            `json:"required,omitempty"`
	Values   map[string]Value `json:"values,omitempty"`
	Sources  []Provenance     `json:"sources,omitempty"`
}

// Edge is lineage: To is derived from From. An endpoint that no inspected
// artifact produces is kept as its physical name.
type Edge struct {
	From   string     `json:"from"`
	To     string     `json:"to"`
	Via    string     `json:"via"`
	Source Provenance `json:"source"`
}

// Severity grades a disagreement.
type Severity string

const (
	SevError   Severity = "error"
	SevWarning Severity = "warning"
	SevInfo    Severity = "info"
)

// Disagreement is something the inspected artifacts cannot all be right
// about, or a reference nothing inspected produces.
type Disagreement struct {
	Severity Severity     `json:"severity"`
	Code     string       `json:"code"`
	Subject  string       `json:"subject"`
	Message  string       `json:"message"`
	Sources  []Provenance `json:"sources,omitempty"`
}

func (d Disagreement) String() string {
	return fmt.Sprintf("%s %s: %s", d.Severity, d.Subject, d.Message)
}

// rawExtra decodes a raw extra value for storage in an Extra map.
func rawExtra(raw json.RawMessage) any {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	return v
}
