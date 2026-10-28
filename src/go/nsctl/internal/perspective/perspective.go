// Package perspective reads perspective definitions (NERD032 SPEC007): the
// Modeler's description of the technology-specific metadata a noun, a
// relationship or an attribute may carry beside its core .hms schema.
//
// A perspective is data only (NERD033): it says which keys a technology
// needs and what their values may be, and implies no code generator. nsctl
// embeds none. A registry holds the definitions a repository declares under
// src/perspectives/<name>.perspective.json, and the ones derived from the
// files that realise a model.
//
// The shape is the one hmd-ms-mickey serves and hmd-app-modeler edits,
// unchanged, plus optional keys a Modeler reader ignores: hms_type and
// aliases on an enum value, position on a parameter, binding_key and
// name_pattern on a definition.
package perspective

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/model"
)

// Dir is where a repository keeps perspective definitions.
const Dir = "src/perspectives"

// Suffix is a definition file's suffix.
const Suffix = ".perspective.json"

// OriginDerived is the Origin of a definition derived from files rather
// than read from one.
const OriginDerived = "derived"

// Attach is where an extension's values attach.
type Attach string

const (
	Entity       Attach = "entity"
	Noun         Attach = "noun"
	Relationship Attach = "relationship"
	Attribute    Attach = "attribute"
)

// Definition is one perspective definition.
type Definition struct {
	Name                   string                 `json:"perspective_name"`
	Display                string                 `json:"perspective_display"`
	GraphDisplay           map[string]any         `json:"graph_display"`
	BindingKey             string                 `json:"binding_key,omitempty"`
	NamePattern            *NamePattern           `json:"name_pattern,omitempty"`
	EntityExtensions       []map[string]Extension `json:"entity_extensions"`
	NounExtensions         []map[string]Extension `json:"noun_extensions"`
	RelationshipExtensions []map[string]Extension `json:"relationship_extensions"`
	AttributeExtensions    []map[string]Extension `json:"attribute_extensions"`
	// Origin is OriginDerived or the file the definition was read from.
	Origin string `json:"-"`
}

// Extension is one key a perspective declares.
type Extension struct {
	Display       string      `json:"display"`
	Description   string      `json:"description"`
	ExtensionType string      `json:"extension_type"`
	Default       any         `json:"default"`
	EnumValues    []EnumValue `json:"enum_values,omitempty"`
	// Aliases are other names of this key: the name a format's parser gives
	// the property, when a person renamed it.
	Aliases []string `json:"aliases,omitempty"`
}

// EnumValue is one choice of an enum extension.
type EnumValue struct {
	ID          string               `json:"id"`
	Description string               `json:"description"`
	Definition  string               `json:"definition"`
	Parameters  map[string]Parameter `json:"parameters,omitempty"`
	// HMSType is the core .hms type an attribute with this value has.
	HMSType string `json:"hms_type,omitempty"`
	// Aliases are other spellings of this value.
	Aliases []string `json:"aliases,omitempty"`
}

// Parameter is a parameter of an enum value.
type Parameter struct {
	Description string `json:"description"`
	Default     any    `json:"default"`
	// Position orders the parameters where they are written positionally
	// (DECIMAL(precision, scale)), since a JSON object has no order.
	Position int `json:"position,omitempty"`
}

// NamePattern is how a binding's physical name is made from the noun and
// the binding's values, as a sequence of parts per name segment.
type NamePattern struct {
	Schema []NamePart `json:"schema,omitempty"`
	Table  []NamePart `json:"table,omitempty"`
}

// NamePart is a literal, a reference to the noun (namespace, name) or to an
// entity key of the binding, or a value only known at run time.
type NamePart struct {
	Lit     string `json:"lit,omitempty"`
	Ref     string `json:"ref,omitempty"`
	Runtime string `json:"runtime,omitempty"`
}

func (d *Definition) list(at Attach) *[]map[string]Extension {
	switch at {
	case Entity:
		return &d.EntityExtensions
	case Noun:
		return &d.NounExtensions
	case Relationship:
		return &d.RelationshipExtensions
	case Attribute:
		return &d.AttributeExtensions
	}
	return nil
}

// Extension returns the extension declared under key at an attach point. An
// entity extension is declared for nouns and relationships alike.
func (d *Definition) Extension(at Attach, key string) (Extension, bool) {
	ats := []Attach{at}
	if at == Noun || at == Relationship {
		ats = append(ats, Entity)
	}
	for _, a := range ats {
		for _, m := range *d.list(a) {
			if e, ok := m[key]; ok {
				return e, true
			}
		}
	}
	return Extension{}, false
}

// Resolve is the key a property named name maps to at an attach point: the
// key of that name, or the key that has it as an alias.
func (d *Definition) Resolve(at Attach, name string) (string, bool) {
	if _, ok := d.Extension(at, name); ok {
		return name, true
	}
	ats := []Attach{at}
	if at == Noun || at == Relationship {
		ats = append(ats, Entity)
	}
	for _, a := range ats {
		for _, m := range *d.list(a) {
			for k, e := range m {
				for _, al := range e.Aliases {
					if al == name {
						return k, true
					}
				}
			}
		}
	}
	return "", false
}

// Keys lists the keys declared at an attach point, in declaration order.
func (d *Definition) Keys(at Attach) []string {
	var out []string
	for _, m := range *d.list(at) {
		for k := range m {
			out = append(out, k)
		}
	}
	return out
}

// Declare appends an extension at an attach point.
func (d *Definition) Declare(at Attach, key string, e Extension) {
	l := d.list(at)
	*l = append(*l, map[string]Extension{key: e})
}

// Rename renames a key at every attach point it is declared at. The old
// name becomes an alias, so values that arrive under it still map to the key.
func (d *Definition) Rename(key, to string) bool {
	found := false
	for _, at := range []Attach{Entity, Noun, Relationship, Attribute} {
		for _, m := range *d.list(at) {
			if e, ok := m[key]; ok {
				delete(m, key)
				e.Aliases = append(e.Aliases, key)
				m[to] = e
				found = true
			}
		}
	}
	if found && d.BindingKey == key {
		d.BindingKey = to
	}
	return found
}

// Drop removes a key at every attach point.
func (d *Definition) Drop(key string) bool {
	found := false
	for _, at := range []Attach{Entity, Noun, Relationship, Attribute} {
		l := d.list(at)
		kept := (*l)[:0]
		for _, m := range *l {
			if _, ok := m[key]; ok {
				found = true
				continue
			}
			kept = append(kept, m)
		}
		*l = kept
	}
	if found && d.BindingKey == key {
		d.BindingKey = ""
	}
	return found
}

// EnumValue returns an enum extension's value by id or alias.
func (e Extension) EnumValue(id string) (EnumValue, bool) {
	for _, v := range e.EnumValues {
		if v.ID == id {
			return v, true
		}
	}
	for _, v := range e.EnumValues {
		for _, a := range v.Aliases {
			if a == id {
				return v, true
			}
		}
	}
	return EnumValue{}, false
}

// OrderedParameters is the value's parameter names by Position, then name.
func (v EnumValue) OrderedParameters() []string {
	out := make([]string, 0, len(v.Parameters))
	for n := range v.Parameters {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool {
		pi, pj := v.Parameters[out[i]].Position, v.Parameters[out[j]].Position
		if pi != pj {
			return pi < pj
		}
		return out[i] < out[j]
	})
	return out
}

// Parse decodes a definition.
func Parse(data []byte) (*Definition, error) {
	var d Definition
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, err
	}
	if d.Name == "" {
		return nil, fmt.Errorf("no perspective_name")
	}
	if d.BindingKey != "" {
		if _, ok := d.Extension(Entity, d.BindingKey); !ok {
			return nil, fmt.Errorf("%s: binding_key %q is not an entity extension", d.Name, d.BindingKey)
		}
	}
	return &d, nil
}

// Marshal encodes a definition as a definition file.
func (d *Definition) Marshal() []byte {
	c := *d
	for _, l := range []*[]map[string]Extension{&c.EntityExtensions, &c.NounExtensions, &c.RelationshipExtensions, &c.AttributeExtensions} {
		if *l == nil {
			*l = []map[string]Extension{}
		}
	}
	if c.GraphDisplay == nil {
		c.GraphDisplay = map[string]any{}
	}
	out, _ := json.MarshalIndent(&c, "", "  ")
	return append(out, '\n')
}

// Clone is a deep copy.
func (d *Definition) Clone() *Definition {
	c, err := Parse(d.Marshal())
	if err != nil {
		panic(err) // a definition always round-trips
	}
	c.Origin = d.Origin
	return c
}

// Registry is the set of definitions an inspection uses.
type Registry struct {
	defs map[string]*Definition
}

// New returns an empty registry.
func New() *Registry { return &Registry{defs: map[string]*Definition{}} }

// Add adds a definition, replacing one of the same name.
func (r *Registry) Add(d *Definition) { r.defs[d.Name] = d }

// LoadDir reads <dir>/*.perspective.json from fsys, replacing a definition
// of the same name. where prefixes Origin. Unreadable files are returned as
// errors and skipped.
func (r *Registry) LoadDir(fsys fs.FS, dir, where string) []error {
	files, err := fs.Glob(fsys, path.Join(dir, "*"+Suffix))
	if err != nil {
		return []error{err}
	}
	var errs []error
	for _, f := range files {
		data, err := fs.ReadFile(fsys, f)
		if err == nil {
			var d *Definition
			if d, err = Parse(data); err == nil {
				d.Origin = path.Join(where, f)
				r.defs[d.Name] = d
				continue
			}
		}
		errs = append(errs, fmt.Errorf("%s: %w", path.Join(where, f), err))
	}
	return errs
}

// Get returns a definition, or nil.
func (r *Registry) Get(name string) *Definition { return r.defs[name] }

// Names lists the definitions, sorted.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.defs))
	for n := range r.defs {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Known reports whether a sidecar suffix names a perspective (as opposed to
// another extension file such as .ui.hms).
func (r *Registry) Known(name string) bool { return r.defs[strings.ToLower(name)] != nil }

// CoreType is the core .hms type attribute-level values imply: the hms_type
// of the first enum attribute extension whose value declares one, else
// Unknown.
func CoreType(d *Definition, values map[string]model.Value) model.LogicalType {
	if d == nil {
		return model.Unknown
	}
	for _, m := range d.AttributeExtensions {
		for key, ext := range m {
			v, ok := values[key]
			if !ok || ext.ExtensionType != "enum" {
				continue
			}
			if ev, ok := ext.EnumValue(v.String()); ok && ev.HMSType != "" {
				if t, ok := model.HMSType(ev.HMSType); ok {
					return t
				}
			}
		}
	}
	return model.Unknown
}

// TypeKey is the attribute key whose enum values carry hms_type: the one a
// physical type is read from, "" when the definition has none.
func (d *Definition) TypeKey() string {
	if d == nil {
		return ""
	}
	for _, m := range d.AttributeExtensions {
		for key, ext := range m {
			for _, ev := range ext.EnumValues {
				if ev.HMSType != "" {
					return key
				}
			}
		}
	}
	return ""
}

// Physical writes an enum value back in its definition's spelling, its
// parameters in Position order: DECIMAL(10,2).
func (d *Definition) Physical(key string, v model.Value) string {
	def := v.Definition
	if def == "" {
		def = strings.ToUpper(v.String())
	}
	if d == nil || len(v.Parameters) == 0 {
		return def
	}
	ext, _ := d.Extension(Attribute, key)
	ev, ok := ext.EnumValue(v.String())
	if !ok {
		return def
	}
	var parts []string
	for _, n := range ev.OrderedParameters() {
		if p, ok := v.Parameters[n]; ok {
			parts = append(parts, strings.ToUpper(strings.TrimSpace(toString(p))))
		}
	}
	if len(parts) == 0 {
		return def
	}
	return def + "(" + strings.Join(parts, ",") + ")"
}

func toString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int:
		return strconv.Itoa(x)
	}
	return fmt.Sprint(v)
}
