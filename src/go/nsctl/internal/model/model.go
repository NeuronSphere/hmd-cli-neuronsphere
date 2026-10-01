// Package model is the canonical data model that nsctl inspect builds
// (NERD032): nouns, their attributes, and the relationships between them,
// shaped like an HMD language pack schema (.hms), plus the few things .hms
// cannot say -- where each fact came from, the physical tables and views that
// carry a noun, lineage between nouns, and two logical types.
//
// The package knows .hms concepts and nothing else about NeuronSphere. Layer
// naming, transform YAML and Jinja belong to inspectors, which reach this
// package only as Observations.
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

// LogicalType is an attribute's type in the model. Every .hms runtime type is
// one; Date and Decimal are the extensions the corpus needed (NERD032 SPEC003).
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
	// Date has no .hms equivalent; a Trino DATE column is the usual source.
	Date LogicalType = "date"
	// Decimal has no .hms equivalent; float would lose its exactness.
	Decimal LogicalType = "decimal"
	// Unknown is a column whose type no inspector could state.
	Unknown LogicalType = "unknown"
)

// IsHMS reports whether .hms can express the type.
func (t LogicalType) IsHMS() bool {
	switch t {
	case String, Integer, Float, Bool, Enum, Timestamp, Epoch, Collection, Mapping, Blob:
		return true
	}
	return false
}

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

// SQLType maps a SQL column type to its logical type. The physical spelling
// is always kept beside it; this mapping is what lets a varchar column and an
// .hms string attribute be compared.
func SQLType(physical string) LogicalType {
	t := strings.ToLower(strings.TrimSpace(physical))
	if i := strings.IndexByte(t, '('); i >= 0 {
		t = strings.TrimSpace(t[:i])
	}
	switch {
	case t == "varchar", t == "char", t == "text", t == "string", t == "uuid",
		strings.HasPrefix(t, "character"):
		return String
	case t == "bigint", t == "int", t == "integer", t == "smallint", t == "tinyint":
		return Integer
	case t == "double", t == "real", t == "float", t == "double precision":
		return Float
	case t == "decimal", t == "numeric":
		return Decimal
	case t == "boolean", t == "bool":
		return Bool
	case t == "date":
		return Date
	case strings.HasPrefix(t, "timestamp"):
		return Timestamp
	case strings.HasPrefix(t, "map"), t == "json", t == "jsonb", strings.HasPrefix(t, "row"):
		return Mapping
	case strings.HasPrefix(t, "array"):
		return Collection
	case t == "varbinary", t == "bytea", t == "blob":
		return Blob
	}
	return Unknown
}

// Location is where a manifestation lives physically. Catalog is optional
// and ignored when two locations are compared, because the same table is
// reached through different catalogs by different tools.
type Location struct {
	Catalog string `json:"catalog,omitempty"`
	Schema  string `json:"schema,omitempty"`
	Table   string `json:"table"`
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

// Noun is a noun or relationship, in .hms terms.
type Noun struct {
	ID          ID       `json:"id"`
	Metatype    Metatype `json:"metatype"`
	Description *string  `json:"description,omitempty"`
	// RefFrom and RefTo are fully qualified noun names, for a relationship.
	RefFrom string `json:"ref_from,omitempty"`
	RefTo   string `json:"ref_to,omitempty"`
	// Authoritative is true when an .hms schema defines the noun; its
	// attributes are then the schema's, not any manifestation's.
	Authoritative  bool             `json:"authoritative"`
	Attributes     []*Attribute     `json:"attributes,omitempty"`
	Manifestations []*Manifestation `json:"manifestations,omitempty"`
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

// Alias records that an identity was merged into a noun.
type Alias struct {
	ID     ID     `json:"id"`
	Reason string `json:"reason"`
}

// Attribute is an .hms attribute.
type Attribute struct {
	Name string      `json:"name"`
	Type LogicalType `json:"type"`
	// HMSType is the type exactly as an .hms file spelled it, so that an
	// alias such as "int" survives a round trip.
	HMSType string `json:"hms_type,omitempty"`
	// PhysicalType is the type as the winning manifestation spelled it.
	PhysicalType string `json:"physical_type,omitempty"`
	// Required is a tri-state: nil is unknown (NERD032 SPEC003).
	Required    *bool          `json:"required,omitempty"`
	EnumDef     []string       `json:"enum_def,omitempty"`
	Description *string        `json:"description,omitempty"`
	Partition   bool           `json:"partition,omitempty"`
	Extra       map[string]any `json:"extra,omitempty"`
	Sources     []Provenance   `json:"sources,omitempty"`
}

// Manifestation is one physical binding of a noun.
type Manifestation struct {
	// Key is the inspector-chosen identity of this binding, stable across runs.
	Key  string `json:"key"`
	Tech string `json:"tech"`
	// Scope groups manifestations whose schema is decided elsewhere, e.g. the
	// models of one dbt project; a Binding observation fills the schema in.
	Scope      string   `json:"scope,omitempty"`
	Location   Location `json:"location"`
	Layer      string   `json:"layer,omitempty"`
	Format     string   `json:"format,omitempty"`
	Partitions []string `json:"partitions,omitempty"`
	// Template is the physical name before run-time values were known, with
	// each such value written {name}. Empty for a fully static name.
	Template string       `json:"template,omitempty"`
	Primary  bool         `json:"primary,omitempty"`
	Columns  []Column     `json:"columns,omitempty"`
	Sources  []Provenance `json:"sources,omitempty"`
}

// Column is a manifestation's column.
type Column struct {
	Name         string       `json:"name"`
	PhysicalType string       `json:"physical_type,omitempty"`
	Type         LogicalType  `json:"type"`
	Required     *bool        `json:"required,omitempty"`
	Sources      []Provenance `json:"sources,omitempty"`
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
