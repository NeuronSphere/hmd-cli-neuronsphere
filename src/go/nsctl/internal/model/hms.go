package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// HMSDoc is one .hms schema document, as hmd-schema-loader reads it: strict
// JSON with name, namespace, metatype, attributes, and ref_from/ref_to for a
// relationship. Keys the model does not interpret are kept in Extra so that a
// document survives a round trip unchanged.
type HMSDoc struct {
	Namespace   string
	Name        string
	Metatype    Metatype
	Description *string
	RefFrom     string
	RefTo       string
	Attributes  []HMSAttribute
	Extra       map[string]json.RawMessage
}

// HMSAttribute is one attribute of an HMSDoc, in file order.
type HMSAttribute struct {
	Name        string
	Type        string
	Description *string
	Required    *bool
	EnumDef     []string
	// EnumKey is "enum_def", or "enum" for the alias one real pack uses.
	EnumKey string
	Extra   map[string]json.RawMessage
}

var hmsTopKeys = map[string]bool{
	"name": true, "namespace": true, "metatype": true, "description": true,
	"ref_from": true, "ref_to": true, "attributes": true,
}

var hmsAttrKeys = map[string]bool{
	"type": true, "description": true, "required": true, "enum_def": true, "enum": true,
}

// ParseHMS decodes an .hms document.
func ParseHMS(data []byte) (*HMSDoc, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, err
	}
	doc := &HMSDoc{Extra: map[string]json.RawMessage{}}
	str := func(key string) (string, error) {
		raw, ok := top[key]
		if !ok {
			return "", nil
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", fmt.Errorf("%s: %w", key, err)
		}
		return s, nil
	}
	var err error
	if doc.Name, err = str("name"); err != nil {
		return nil, err
	}
	if doc.Namespace, err = str("namespace"); err != nil {
		return nil, err
	}
	meta, err := str("metatype")
	if err != nil {
		return nil, err
	}
	doc.Metatype = Metatype(meta)
	if doc.RefFrom, err = str("ref_from"); err != nil {
		return nil, err
	}
	if doc.RefTo, err = str("ref_to"); err != nil {
		return nil, err
	}
	if _, ok := top["description"]; ok {
		d, err := str("description")
		if err != nil {
			return nil, err
		}
		doc.Description = &d
	}
	if doc.Name == "" {
		return nil, fmt.Errorf("no name")
	}
	if raw, ok := top["attributes"]; ok {
		if doc.Attributes, err = parseHMSAttributes(raw); err != nil {
			return nil, fmt.Errorf("attributes: %w", err)
		}
	}
	for k, v := range top {
		if !hmsTopKeys[k] {
			doc.Extra[k] = v
		}
	}
	return doc, nil
}

func parseHMSAttributes(raw json.RawMessage) ([]HMSAttribute, error) {
	names, err := orderedKeys(raw)
	if err != nil {
		return nil, err
	}
	var byName map[string]map[string]json.RawMessage
	if err := json.Unmarshal(raw, &byName); err != nil {
		return nil, err
	}
	attrs := make([]HMSAttribute, 0, len(names))
	for _, name := range names {
		fields := byName[name]
		a := HMSAttribute{Name: name, Extra: map[string]json.RawMessage{}}
		for k, v := range fields {
			var err error
			switch k {
			case "type":
				err = json.Unmarshal(v, &a.Type)
			case "description":
				var d string
				err = json.Unmarshal(v, &d)
				a.Description = &d
			case "required":
				var b bool
				err = json.Unmarshal(v, &b)
				a.Required = &b
			case "enum_def", "enum":
				err = json.Unmarshal(v, &a.EnumDef)
				a.EnumKey = k
			default:
				a.Extra[k] = v
			}
			if err != nil {
				return nil, fmt.Errorf("%s.%s: %w", name, k, err)
			}
		}
		attrs = append(attrs, a)
	}
	return attrs, nil
}

// orderedKeys returns the keys of a JSON object in document order.
func orderedKeys(raw json.RawMessage) ([]string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("not an object")
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		keys = append(keys, tok.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return nil, err
		}
	}
	return keys, nil
}

// HMSObservations turns a parsed document into observations: one noun
// observation and one per attribute, all at .hms authority.
func HMSObservations(doc *HMSDoc, prov Provenance) []Observation {
	prov.Authority = AuthHMS
	prov.Confidence = Decided
	id := ID{Namespace: doc.Namespace, Name: doc.Name}
	obs := []Observation{{
		Kind:    KindNoun,
		Subject: id,
		Noun: &NounObs{
			Metatype: doc.Metatype, Description: doc.Description,
			RefFrom: doc.RefFrom, RefTo: doc.RefTo, Extra: doc.Extra,
		},
		Provenance: prov,
	}}
	for i, a := range doc.Attributes {
		t, ok := HMSType(a.Type)
		if !ok {
			t = Unknown
		}
		extra := a.Extra
		if a.EnumKey == "enum" {
			// Remember the alias so the export writes the file's own spelling.
			extra = cloneRaw(extra)
			extra["__enum_key"] = json.RawMessage(`"enum"`)
		}
		obs = append(obs, Observation{
			Kind:    KindAttribute,
			Subject: id,
			Attr: &AttrObs{
				Name: a.Name, Type: t, HMSType: a.Type, Required: a.Required,
				EnumDef: a.EnumDef, Description: a.Description, Ordinal: i + 1, Extra: extra,
			},
			Provenance: prov,
		})
	}
	return obs
}

func cloneRaw(m map[string]json.RawMessage) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	return out
}

// ExportError lists attributes .hms cannot express.
type ExportError struct {
	Noun       ID
	Attributes []string
}

func (e *ExportError) Error() string {
	return fmt.Sprintf("%s: attributes with types .hms cannot express: %s (export with lossy to downgrade them to string)",
		e.Noun, strings.Join(e.Attributes, ", "))
}

// ExportHMS writes a noun as an .hms document. A noun learned from .hms
// exports to a document equal to its source. An attribute whose logical type
// is an extension makes the export fail with an *ExportError, unless lossy is
// set, in which case it is written as string.
func ExportHMS(n *Noun, lossy bool) ([]byte, error) {
	var bad []string
	for _, a := range n.Attributes {
		if a.HMSType == "" && !a.Type.IsHMS() {
			bad = append(bad, fmt.Sprintf("%s (%s)", a.Name, a.Type))
		}
	}
	if len(bad) > 0 && !lossy {
		return nil, &ExportError{Noun: n.ID, Attributes: bad}
	}
	var b bytes.Buffer
	b.WriteString("{\n")
	fields := []string{}
	add := func(key string, v any) {
		raw, _ := json.Marshal(v)
		fields = append(fields, fmt.Sprintf("  %q: %s", key, raw))
	}
	add("name", n.ID.Name)
	add("namespace", n.ID.Namespace)
	if n.Description != nil {
		add("description", *n.Description)
	}
	add("metatype", string(n.Metatype))
	if n.RefFrom != "" {
		add("ref_from", n.RefFrom)
	}
	if n.RefTo != "" {
		add("ref_to", n.RefTo)
	}
	for _, k := range sortedKeys(n.Extra) {
		add(k, n.Extra[k])
	}
	var attrs bytes.Buffer
	attrs.WriteString("{")
	for i, a := range n.Attributes {
		if i > 0 {
			attrs.WriteString(",")
		}
		raw, _ := json.Marshal(a.Name)
		attrs.WriteString("\n    ")
		attrs.Write(raw)
		attrs.WriteString(": ")
		attrs.Write(exportAttr(a))
	}
	if len(n.Attributes) > 0 {
		attrs.WriteString("\n  ")
	}
	attrs.WriteString("}")
	fields = append(fields, `  "attributes": `+attrs.String())
	b.WriteString(strings.Join(fields, ",\n"))
	b.WriteString("\n}\n")
	return b.Bytes(), nil
}

func exportAttr(a *Attribute) []byte {
	m := map[string]any{}
	typ := a.HMSType
	if typ == "" {
		typ = string(a.Type)
		if !a.Type.IsHMS() {
			typ = string(String)
		}
	}
	m["type"] = typ
	if a.Description != nil {
		m["description"] = *a.Description
	}
	if a.Required != nil {
		m["required"] = *a.Required
	}
	enumKey := "enum_def"
	for k, v := range a.Extra {
		if k == "__enum_key" {
			if s, ok := v.(string); ok {
				enumKey = s
			}
			continue
		}
		m[k] = v
	}
	if a.EnumDef != nil {
		m[enumKey] = a.EnumDef
	}
	raw, _ := json.Marshal(m)
	return raw
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
