// Package perspective reads perspective definitions (NERD032 SPEC007): the
// Modeler's description of the technology-specific metadata a noun, a
// relationship or an attribute may carry beside its core .hms schema.
//
// The shape is the one hmd-ms-mickey serves and hmd-app-modeler edits,
// unchanged, plus two optional keys a Modeler reader ignores: hms_type on an
// enum value (the core .hms type an attribute with that value has) and
// binding_key on a definition (the entity extension that tells several
// bindings of one noun apart). nsctl embeds the definitions its inspectors
// use; a repository's src/perspectives/<name>.perspective.json overrides one
// of the same name.
package perspective

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

//go:embed defs/*.perspective.json
var embedded embed.FS

// Dir is where a repository keeps perspective definitions.
const Dir = "src/perspectives"

// Suffix is a definition file's suffix.
const Suffix = ".perspective.json"

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
	EntityExtensions       []map[string]Extension `json:"entity_extensions"`
	NounExtensions         []map[string]Extension `json:"noun_extensions"`
	RelationshipExtensions []map[string]Extension `json:"relationship_extensions"`
	AttributeExtensions    []map[string]Extension `json:"attribute_extensions"`
	// Origin is "embedded" or the file the definition was read from.
	Origin string `json:"-"`
}

// Extension is one key a perspective declares.
type Extension struct {
	Display       string      `json:"display"`
	Description   string      `json:"description"`
	ExtensionType string      `json:"extension_type"`
	Default       any         `json:"default"`
	EnumValues    []EnumValue `json:"enum_values,omitempty"`
}

// EnumValue is one choice of an enum extension.
type EnumValue struct {
	ID          string               `json:"id"`
	Description string               `json:"description"`
	Definition  string               `json:"definition"`
	Parameters  map[string]Parameter `json:"parameters,omitempty"`
	// HMSType is the core .hms type an attribute with this value has.
	HMSType string `json:"hms_type,omitempty"`
}

// Parameter is a parameter of an enum value.
type Parameter struct {
	Description string `json:"description"`
	Default     any    `json:"default"`
}

func (d *Definition) list(at Attach) []map[string]Extension {
	switch at {
	case Entity:
		return d.EntityExtensions
	case Noun:
		return d.NounExtensions
	case Relationship:
		return d.RelationshipExtensions
	case Attribute:
		return d.AttributeExtensions
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
		for _, m := range d.list(a) {
			if e, ok := m[key]; ok {
				return e, true
			}
		}
	}
	return Extension{}, false
}

// EnumValue returns an enum extension's value by id.
func (e Extension) EnumValue(id string) (EnumValue, bool) {
	for _, v := range e.EnumValues {
		if v.ID == id {
			return v, true
		}
	}
	return EnumValue{}, false
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

// Registry is the set of definitions an inspection uses.
type Registry struct {
	defs map[string]*Definition
}

// Default returns a registry of the embedded definitions.
func Default() *Registry {
	r := &Registry{defs: map[string]*Definition{}}
	files, _ := fs.Glob(embedded, "defs/*"+Suffix)
	for _, f := range files {
		data, _ := embedded.ReadFile(f)
		d, err := Parse(data)
		if err != nil {
			panic(fmt.Sprintf("embedded perspective %s: %v", f, err)) // a build defect
		}
		d.Origin = "embedded"
		r.defs[d.Name] = d
	}
	return r
}

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
