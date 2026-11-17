package modelsource

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// File is a NEP051 source file, src/sources/<source>.source.yaml. Columns
// and yields keep the order they are written in, so a file nsctl rewrites
// reads the way a person left it.
type File struct {
	Source     string    `yaml:"source"`
	From       From      `yaml:"from"`
	Provenance string    `yaml:"provenance,omitempty"`
	Evidence   *Evidence `yaml:"evidence,omitempty"`
	Columns    Columns   `yaml:"columns,omitempty"`
	Yields     Yields    `yaml:"yields"`
	ChangedAt  string    `yaml:"changed_at,omitempty"`
	LandsIn    string    `yaml:"lands_in,omitempty"`
}

// Provenance values (NEP051).
const (
	Declared = "declared"
	Observed = "observed"
	Inferred = "inferred"
)

// Prov is the file's provenance, declared when unset.
func (f *File) Prov() string {
	if f.Provenance == "" {
		return Declared
	}
	return f.Provenance
}

// From is where a source's rows come from: exactly one of a Librarian
// content item type (cit, with format), a warehouse table, or a file group
// in an nsctl data workspace (NERD039 SPEC007).
type From struct {
	CIT    string `yaml:"cit,omitempty"`
	Format string `yaml:"format,omitempty"`
	Table  string `yaml:"table,omitempty"`
	Group  string `yaml:"group,omitempty"`
}

// Evidence is why a mapping was made. It is kept after approval.
type Evidence struct {
	Snapshot string   `yaml:"snapshot,omitempty"`
	Notes    []string `yaml:"notes,omitempty"`
}

// Column is one name: type entry.
type Column struct {
	Name string
	Type string
}

// Columns is the ordered columns block.
type Columns []Column

// Type returns a column's type, "" when it is not listed.
func (cs Columns) Type(name string) string {
	for _, c := range cs {
		if c.Name == name {
			return c.Type
		}
	}
	return ""
}

func (cs *Columns) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: columns must be a mapping of name: type", n.Line)
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		*cs = append(*cs, Column{Name: n.Content[i].Value, Type: n.Content[i+1].Value})
	}
	return nil
}

func (cs Columns) MarshalYAML() (any, error) {
	m := &yaml.Node{Kind: yaml.MappingNode}
	for _, c := range cs {
		m.Content = append(m.Content, scalar(c.Name), scalar(c.Type))
	}
	return m, nil
}

// Mapping is one attribute of a yield: a column, or an SQL expression.
type Mapping struct {
	Attribute string
	Column    string
	SQL       string
}

// Yield is what a row contains of one concept.
type Yield struct {
	// Key is the yield's name: the concept, or any name when Concept is set.
	Key     string
	Concept string
	// From and To name two other yields; set, the yield is a relationship.
	From, To string
	Maps     []Mapping
}

// ConceptName is the concept the yield is of.
func (y Yield) ConceptName() string {
	if y.Concept != "" {
		return y.Concept
	}
	return y.Key
}

// IsRelationship reports whether the yield joins two others.
func (y Yield) IsRelationship() bool { return y.From != "" || y.To != "" }

// Yields is the ordered yields block.
type Yields []Yield

// Get returns the yield with the key, or nil.
func (ys Yields) Get(key string) *Yield {
	for i := range ys {
		if ys[i].Key == key {
			return &ys[i]
		}
	}
	return nil
}

func (ys *Yields) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: yields must be a mapping of concept: {attribute: column}", n.Line)
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		y := Yield{Key: n.Content[i].Value}
		v := n.Content[i+1]
		if v.Kind != yaml.MappingNode {
			return fmt.Errorf("line %d: yield %s must be a mapping", v.Line, y.Key)
		}
		for j := 0; j+1 < len(v.Content); j += 2 {
			k, val := v.Content[j].Value, v.Content[j+1]
			switch k {
			case "from":
				y.From = val.Value
			case "to":
				y.To = val.Value
			case "concept":
				y.Concept = val.Value
			default:
				m := Mapping{Attribute: k}
				switch val.Kind {
				case yaml.ScalarNode:
					m.Column = val.Value
				case yaml.MappingNode:
					var expr struct {
						SQL  string `yaml:"sql"`
						Path string `yaml:"path"`
					}
					if err := val.Decode(&expr); err != nil {
						return err
					}
					m.SQL = expr.SQL
				default:
					return fmt.Errorf("line %d: %s.%s must be a column or {sql: ...}", val.Line, y.Key, k)
				}
				y.Maps = append(y.Maps, m)
			}
		}
		*ys = append(*ys, y)
	}
	return nil
}

func (ys Yields) MarshalYAML() (any, error) {
	m := &yaml.Node{Kind: yaml.MappingNode}
	for _, y := range ys {
		// One line per yield, the way NEP051 writes them.
		v := &yaml.Node{Kind: yaml.MappingNode, Style: yaml.FlowStyle}
		if y.Concept != "" {
			v.Content = append(v.Content, scalar("concept"), scalar(y.Concept))
		}
		if y.From != "" {
			v.Content = append(v.Content, scalar("from"), scalar(y.From))
		}
		if y.To != "" {
			v.Content = append(v.Content, scalar("to"), scalar(y.To))
		}
		for _, mp := range y.Maps {
			if mp.SQL != "" {
				v.Content = append(v.Content, scalar(mp.Attribute), &yaml.Node{Kind: yaml.MappingNode, Style: yaml.FlowStyle,
					Content: []*yaml.Node{scalar("sql"), scalar(mp.SQL)}})
				continue
			}
			v.Content = append(v.Content, scalar(mp.Attribute), scalar(mp.Column))
		}
		m.Content = append(m.Content, scalar(y.Key), v)
	}
	return m, nil
}

func scalar(s string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Value: s} }

// Parse reads a source file and checks what NEP051 requires of it.
func Parse(b []byte) (*File, error) {
	var f File
	if err := yaml.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	if f.Source == "" {
		return nil, fmt.Errorf("source: is required")
	}
	set := 0
	for _, v := range []string{f.From.CIT, f.From.Table, f.From.Group} {
		if v != "" {
			set++
		}
	}
	if set != 1 {
		return nil, fmt.Errorf("from: needs exactly one of cit, table or group")
	}
	switch f.Prov() {
	case Declared, Observed, Inferred:
	default:
		return nil, fmt.Errorf("provenance %q: want declared, observed or inferred", f.Provenance)
	}
	if len(f.Yields) == 0 {
		return nil, fmt.Errorf("yields: is required")
	}
	for _, y := range f.Yields {
		if y.IsRelationship() && (f.Yields.Get(y.From) == nil || f.Yields.Get(y.To) == nil) {
			return nil, fmt.Errorf("yield %s: from and to must name other yields in this file", y.Key)
		}
	}
	return &f, nil
}

// Marshal writes a source file, with a header comment when one is given.
func Marshal(f *File, header string) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(f); err != nil {
		return nil, err
	}
	b := buf.Bytes()
	if header == "" {
		return b, nil
	}
	var h strings.Builder
	for _, line := range strings.Split(strings.TrimRight(header, "\n"), "\n") {
		h.WriteString("# " + line + "\n")
	}
	return append([]byte(h.String()), b...), nil
}
