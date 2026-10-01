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
	// KindAttribute: an attribute of a noun, or a column of one of its
	// manifestations when AttrObs.Manifestation is set.
	KindAttribute Kind = "attribute"
	// KindManifestation: a physical binding of a noun.
	KindManifestation Kind = "manifestation"
	// KindLineage: one thing is derived from another.
	KindLineage Kind = "lineage"
	// KindConstraint: a test or constraint on an attribute.
	KindConstraint Kind = "constraint"
	// KindProvide: an artifact provides something others may reference by
	// name (a transform name, a table template).
	KindProvide Kind = "provide"
	// KindReference: an artifact relies on something being provided.
	KindReference Kind = "reference"
	// KindBinding: a scope's manifestations live in a schema.
	KindBinding Kind = "binding"
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
	Manifest   *ManifestObs   `json:"manifest,omitempty"`
	Lineage    *LineageObs    `json:"lineage,omitempty"`
	Constraint *ConstraintObs `json:"constraint,omitempty"`
	Named      *NamedObs      `json:"named,omitempty"`
	Binding    *BindingObs    `json:"binding,omitempty"`
	SameAs     *SameAsObs     `json:"same_as,omitempty"`
	Finding    *FindingObs    `json:"finding,omitempty"`
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

// AttrObs is an attribute or a manifestation column.
type AttrObs struct {
	Name string `json:"name"`
	// Manifestation is the key of the manifestation this column belongs to;
	// empty for an attribute of the noun itself (.hms).
	Manifestation string      `json:"manifestation,omitempty"`
	Position      int         `json:"position,omitempty"`
	Type          LogicalType `json:"type"`
	HMSType       string      `json:"hms_type,omitempty"`
	PhysicalType  string      `json:"physical_type,omitempty"`
	Required      *bool       `json:"required,omitempty"`
	EnumDef       []string    `json:"enum_def,omitempty"`
	Description   *string     `json:"description,omitempty"`
	Partition     bool        `json:"partition,omitempty"`
	// Ordinal is the attribute's order within an .hms file.
	Ordinal int                        `json:"ordinal,omitempty"`
	Extra   map[string]json.RawMessage `json:"extra,omitempty"`
}

// ManifestObs is a physical binding.
type ManifestObs struct {
	Key        string   `json:"key"`
	Tech       string   `json:"tech"`
	Scope      string   `json:"scope,omitempty"`
	Location   Location `json:"location"`
	Layer      string   `json:"layer,omitempty"`
	Format     string   `json:"format,omitempty"`
	Partitions []string `json:"partitions,omitempty"`
	Template   string   `json:"template,omitempty"`
	Primary    bool     `json:"primary,omitempty"`
	// Reference marks a manifestation that names a table owned elsewhere (a
	// dbt source, an INSERT target): it links identities but does not count as
	// providing the table, so a reference to it can still go unresolved.
	Reference bool `json:"reference,omitempty"`
}

// Endpoint names one end of a lineage edge: a noun when the inspector knows
// it, otherwise a physical location consolidation resolves.
type Endpoint struct {
	ID       ID       `json:"id,omitempty"`
	Location Location `json:"location,omitempty"`
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

// BindingObs puts every manifestation of Scope that lacks a schema into
// Schema.
type BindingObs struct {
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
