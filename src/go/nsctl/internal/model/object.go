package model

// An object observation is a table or view as its format's parser sees it,
// before any perspective names it (NERD033 SPEC002). Its properties are keyed
// by the names the format's grammar gives them; derivation decides which
// perspective they belong to, which noun the object is, and what kind of
// value each property is.

const (
	// KindObject: a physical object a format parser read (ObjectObs).
	KindObject Kind = "object"
	// KindSidecar: a perspective sidecar not yet read against its
	// definition (SidecarObs). Derivation resolves it once every definition,
	// declared or derived, is known.
	KindSidecar Kind = "sidecar"
)

// PropClass is what the grammar says a property's value is.
type PropClass string

const (
	// ClassIdent: an identifier (a catalog, schema or table name). Never an
	// enumeration, however few distinct values it has.
	ClassIdent PropClass = "ident"
	// ClassKeyword: a keyword of the grammar (TABLE, VIEW).
	ClassKeyword PropClass = "keyword"
	// ClassText: a string literal.
	ClassText PropClass = "text"
	// ClassList: a list of identifiers or literals, kept comma-joined.
	ClassList PropClass = "list"
	// ClassBool: a flag the grammar either states or does not.
	ClassBool PropClass = "bool"
	// ClassType: a data type; Prop.Type holds it parsed.
	ClassType PropClass = "type"
)

// Prop is one property of an object or a column.
type Prop struct {
	Value string    `json:"value"`
	Class PropClass `json:"class"`
	Type  *PhysType `json:"type,omitempty"`
}

// PhysType is a data type as written, parsed by its grammar: the canonical
// base name (synonyms the grammar defines resolved), its arguments, the
// grammar's names for them, and the grammar's category for the base.
type PhysType struct {
	Raw      string   `json:"raw"`
	Base     string   `json:"base"`
	Args     []string `json:"args,omitempty"`
	Params   []string `json:"params,omitempty"`
	Category string   `json:"category"`
}

// ObjectObs is a physical object of Dialect at Location.
type ObjectObs struct {
	// Dialect is the family of the format the object is written in
	// ("trino"). A derived perspective is named after it.
	Dialect  string          `json:"dialect"`
	Location Location        `json:"location"`
	Props    map[string]Prop `json:"props,omitempty"`
	// Columns are the declared columns, at the observation's authority.
	Columns []ObjectColumn `json:"columns,omitempty"`
	// Selected are the columns a select list produces (an INSERT ...
	// SELECT, a view), at SelectAuthority. They have names and order only.
	Selected        []ObjectColumn `json:"selected,omitempty"`
	SelectAuthority Authority      `json:"select_authority,omitempty"`
	// Reference marks an object named but not declared here (an INSERT
	// target): it links identities but does not provide the object.
	Reference bool `json:"reference,omitempty"`
}

// ObjectColumn is one column of an object, in order.
type ObjectColumn struct {
	Name     string          `json:"name"`
	Position int             `json:"position"`
	Line     int             `json:"line,omitempty"`
	NotNull  bool            `json:"not_null,omitempty"`
	Props    map[string]Prop `json:"props,omitempty"`
}

// SidecarObs is a perspective sidecar's raw document.
type SidecarObs struct {
	Perspective string `json:"perspective"`
	Data        []byte `json:"data"`
}
