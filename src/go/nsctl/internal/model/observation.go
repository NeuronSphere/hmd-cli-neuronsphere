package model

import (
	"encoding/json"
	"fmt"
)

// Authority orders sources that speak about the same element (NERD032
// SPEC002). Higher wins.
type Authority int

const (
	AuthInferred     Authority = 10
	AuthDbtSQL       Authority = 40
	AuthDbtYAML      Authority = 50
	AuthInsertSelect Authority = 60
	AuthDDL          Authority = 80
	AuthHMS          Authority = 100
)

// Confidence reuses the repoclass detect vocabulary (NERD009 SPEC009).
type Confidence string

const (
	// Decided: the artifact states it.
	Decided Confidence = "decided"
	// Evidence: inferred from the artifact, not stated by it.
	Evidence Confidence = "evidence"
)

// Provenance is where an observation came from.
type Provenance struct {
	Inspector  string     `json:"inspector"`
	Repo       string     `json:"repo"`
	Revision   string     `json:"revision,omitempty"`
	File       string     `json:"file"`
	Line       int        `json:"line,omitempty"`
	Authority  Authority  `json:"authority"`
	Confidence Confidence `json:"confidence"`
	Why        string     `json:"why,omitempty"`
}

// Where is repo:file:line, the form a person opens.
func (p Provenance) Where() string {
	where := p.File
	if p.Repo != "" {
		where = p.Repo + "/" + where
	}
	if p.Line > 0 {
		where = fmt.Sprintf("%s:%d", where, p.Line)
	}
	return where
}

// Kind is what an observation reports.
type Kind string

const (
	// KindNoun: a noun or relationship exists (NounObs).
	KindNoun Kind = "noun"
	// KindAttribute: an attribute of a noun, or, when AttrObs.Binding is
	// set, that attribute as one perspective binding has it.
	KindAttribute Kind = "attribute"
	// KindBinding: a perspective binding of a noun (BindingObs).
	KindBinding Kind = "binding"
	// KindLineage: one thing is derived from another.
	KindLineage Kind = "lineage"
	// KindConstraint: a test or constraint on an attribute.
	KindConstraint Kind = "constraint"
	// KindProvide: an artifact provides something others may reference by
	// name (a transform name, a content type).
	KindProvide Kind = "provide"
	// KindReference: an artifact relies on something being provided.
	KindReference Kind = "reference"
	// KindScope: a scope's bindings live in a schema (ScopeObs).
	KindScope Kind = "scope"
	// KindSameAs: two identities are one noun, by explicit declaration.
	KindSameAs Kind = "same_as"
	// KindFinding: an inspector-local anomaly, passed through as-is.
	KindFinding Kind = "finding"
)

// Observation is one fact one inspector saw. It does not decide truth.
type Observation struct {
	Kind       Kind           `json:"kind"`
	Subject    ID             `json:"subject"`
	Noun       *NounObs       `json:"noun,omitempty"`
	Attr       *AttrObs       `json:"attr,omitempty"`
	Binding    *BindingObs    `json:"binding,omitempty"`
	Lineage    *LineageObs    `json:"lineage,omitempty"`
	Constraint *ConstraintObs `json:"constraint,omitempty"`
	Named      *NamedObs      `json:"named,omitempty"`
	Scope      *ScopeObs      `json:"scope,omitempty"`
	SameAs     *SameAsObs     `json:"same_as,omitempty"`
	Finding    *FindingObs    `json:"finding,omitempty"`
	Object     *ObjectObs     `json:"object,omitempty"`
	Sidecar    *SidecarObs    `json:"sidecar,omitempty"`
	Provenance Provenance     `json:"provenance"`
}

// NounObs is a noun's own facts.
type NounObs struct {
	Metatype    Metatype                   `json:"metatype"`
	Description *string                    `json:"description,omitempty"`
	RefFrom     string                     `json:"ref_from,omitempty"`
	RefTo       string                     `json:"ref_to,omitempty"`
	Extra       map[string]json.RawMessage `json:"extra,omitempty"`
}

// AttrObs is an .hms attribute, or an attribute as a perspective binding has
// it.
type AttrObs struct {
	Name string `json:"name"`
	// Binding is BindingKey(perspective, name) of the binding this column
	// belongs to; empty for an attribute of the noun itself (.hms).
	Binding string `json:"binding,omitempty"`
	// Position orders a binding's columns (a CREATE TABLE, a select list).
	// Zero means the source lists values for some attributes in no order (a
	// perspective sidecar): they overlay the ordered columns by name and do
	// not take part in column-list comparison.
	Position    int         `json:"position,omitempty"`
	Type        LogicalType `json:"type"`
	HMSType     string      `json:"hms_type,omitempty"`
	Required    *bool       `json:"required,omitempty"`
	EnumDef     []string    `json:"enum_def,omitempty"`
	Description *string     `json:"description,omitempty"`
	// Values are the attribute-level perspective values.
	Values map[string]Value `json:"values,omitempty"`
	// Ordinal is the attribute's order within an .hms file.
	Ordinal int                        `json:"ordinal,omitempty"`
	Extra   map[string]json.RawMessage `json:"extra,omitempty"`
}

// BindingObs is one perspective binding of Subject.
type BindingObs struct {
	Perspective string `json:"perspective"`
	// Name is the binding_key value, "" for a single-binding perspective.
	Name  string `json:"name,omitempty"`
	Scope string `json:"scope,omitempty"`
	// Location links bindings across inspectors; LocationOf(Values) when
	// unset.
	Location Location         `json:"location"`
	Primary  bool             `json:"primary,omitempty"`
	Values   map[string]Value `json:"values,omitempty"`
	// Reference marks a binding that names a table owned elsewhere (a dbt
	// source, an INSERT target): it links identities but does not count as
	// providing the table, so a reference to it can still go unresolved.
	Reference bool `json:"reference,omitempty"`
}

// Key is the binding's identity within its noun.
func (b *BindingObs) Key() string { return BindingKey(b.Perspective, b.Name) }

// BindingKey identifies a binding within a noun.
func BindingKey(perspective, name string) string {
	if name == "" {
		return perspective
	}
	return perspective + ":" + name
}

// Endpoint names one end of a lineage edge: a noun when the inspector knows
// it, otherwise a physical location consolidation resolves.
type Endpoint struct {
	ID       ID       `json:"id,omitempty"`
	Location Location `json:"location,omitempty"`
	// Dialect, with Location and no ID, asks derivation to name the noun at
	// Location by that dialect's name pattern.
	Dialect string `json:"dialect,omitempty"`
}

func (e Endpoint) String() string {
	if !e.ID.IsZero() {
		return e.ID.String()
	}
	return e.Location.String()
}

// LineageObs is To derived from From.
type LineageObs struct {
	From Endpoint `json:"from"`
	To   Endpoint `json:"to"`
	Via  string   `json:"via"`
}

// ConstraintObs is a constraint on Subject's attribute.
type ConstraintObs struct {
	Attribute string `json:"attribute"`
	// Constraint is not_null, unique, or another test name.
	Constraint string `json:"constraint"`
}

// NamedObs is a provide or reference: Kind of thing, Key its name. A
// reference without a matching provide is a disagreement.
type NamedObs struct {
	Kind string `json:"kind"`
	Key  string `json:"key"`
	// Via says how the reference was made (dep_tf_name, drop, dbt source).
	Via string `json:"via,omitempty"`
}

// ScopeObs puts every binding of Scope that lacks a schema into Schema.
type ScopeObs struct {
	Scope  string `json:"scope"`
	Schema string `json:"schema"`
}

// SameAsObs declares Subject and Other one noun.
type SameAsObs struct {
	Other ID `json:"other"`
}

// FindingObs is an inspector's own anomaly.
type FindingObs struct {
	Severity Severity `json:"severity"`
	Code     string   `json:"code"`
	Message  string   `json:"message"`
}

// BoolPtr is a helper for tri-state fields.
func BoolPtr(b bool) *bool { return &b }

// StrPtr is a helper for optional strings.
func StrPtr(s string) *string { return &s }
