package model

import (
	"encoding/json"
	"fmt"
	"sort"
)

// A perspective sidecar is <name>.<perspective>.hms beside <name>.hms
// (NERD032 SPEC007). hmd-schema-loader merges it into the noun's
// extensions[<perspective>]. Values have the Modeler's shape; attribute-level
// values sit under "attributes"; a perspective with a binding_key holds a
// "bindings" list, each entry carrying its binding_key value:
//
//	{"namespace": "ntc", "name": "ntc_instances_export",
//	 "bindings": [{"layer": {"value": "final"}, "table_name": {"value": "..."},
//	               "attributes": {"export_date": {"datatype": {"value": "date", "definition": "DATE"}}}}]}
//
// Without a binding_key the entity values and "attributes" sit at the top.

type sidecarEntry map[string]json.RawMessage

// ExportSidecar writes a noun's bindings of one perspective as a sidecar
// document. bindingKey is the definition's binding_key ("" for a
// single-binding perspective). ok is false when the noun has no binding of
// the perspective.
func ExportSidecar(n *Noun, perspective, bindingKey string) (data []byte, ok bool) {
	var entries []map[string]any
	for _, b := range n.Bindings {
		if b.Perspective != perspective {
			continue
		}
		e := map[string]any{}
		for k, v := range b.Values {
			e[k] = v
		}
		if bindingKey != "" {
			e[bindingKey] = V(b.Name)
		}
		attrs := map[string]any{}
		for _, c := range b.Columns {
			if len(c.Values) > 0 {
				attrs[c.Name] = c.Values
			}
		}
		if len(attrs) > 0 {
			e["attributes"] = attrs
		}
		entries = append(entries, e)
	}
	if len(entries) == 0 {
		return nil, false
	}
	doc := map[string]any{"namespace": n.ID.Namespace, "name": n.ID.Name}
	if bindingKey == "" {
		for k, v := range entries[0] {
			doc[k] = v
		}
	} else {
		doc["bindings"] = entries
	}
	out, _ := json.MarshalIndent(doc, "", "  ")
	return append(out, '\n'), true
}

// SidecarObservations reads a sidecar document as binding and attribute
// observations of the noun it names. Attribute values carry no position:
// they overlay whatever columns other sources state, by name.
func SidecarObservations(data []byte, perspective, bindingKey string, prov Provenance) ([]Observation, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, err
	}
	var ns, name string
	_ = json.Unmarshal(top["namespace"], &ns)
	if err := json.Unmarshal(top["name"], &name); err != nil || name == "" {
		return nil, fmt.Errorf("sidecar has no name")
	}
	id := ID{Namespace: ns, Name: name}
	var entries []sidecarEntry
	if bindingKey != "" {
		if raw, ok := top["bindings"]; ok {
			if err := json.Unmarshal(raw, &entries); err != nil {
				return nil, fmt.Errorf("bindings: %w", err)
			}
		} else {
			entries = []sidecarEntry{top}
		}
	} else {
		entries = []sidecarEntry{top}
	}
	var obs []Observation
	for i, e := range entries {
		values := map[string]Value{}
		var attrs map[string]map[string]Value
		for k, raw := range e {
			switch k {
			case "namespace", "name", "bindings":
				continue
			case "attributes":
				if err := json.Unmarshal(raw, &attrs); err != nil {
					return nil, fmt.Errorf("binding %d attributes: %w", i, err)
				}
			default:
				var v Value
				if err := json.Unmarshal(raw, &v); err != nil {
					return nil, fmt.Errorf("binding %d %s: %w", i, k, err)
				}
				values[k] = v
			}
		}
		bname := ""
		if bindingKey != "" {
			bname = values[bindingKey].String()
			delete(values, bindingKey)
		}
		bo := &BindingObs{Perspective: perspective, Name: bname, Values: values}
		obs = append(obs, Observation{Kind: KindBinding, Subject: id, Binding: bo, Provenance: prov})
		names := make([]string, 0, len(attrs))
		for a := range attrs {
			names = append(names, a)
		}
		sort.Strings(names)
		for _, a := range names {
			obs = append(obs, Observation{Kind: KindAttribute, Subject: id, Provenance: prov,
				Attr: &AttrObs{Name: a, Binding: bo.Key(), Type: Unknown, Values: attrs[a]}})
		}
	}
	return obs, nil
}
