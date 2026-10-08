package derive

import (
	"fmt"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/model"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/perspective"
)

// deriveFromValues derives a definition for each perspective whose bindings
// an inspector named itself, with values it already chose, when nothing
// declares that perspective: the keys are the keys seen, and each key's kind
// follows from its values. Edits are replayed, and values follow renamed and
// dropped keys.
func deriveFromValues(obs []model.Observation, known map[string]*perspective.Definition, edits []perspective.Edit) ([]model.Observation, []*perspective.Derivation, []perspective.Edit) {
	entity := map[string]map[string]*seen{}
	attrs := map[string]map[string]*seen{}
	named := map[string]bool{}
	var order []string
	note := func(p string) {
		if entity[p] == nil {
			entity[p], attrs[p] = map[string]*seen{}, map[string]*seen{}
			order = append(order, p)
		}
	}
	for _, o := range obs {
		switch {
		case o.Kind == model.KindBinding && o.Binding != nil:
			p := o.Binding.Perspective
			if known[p] != nil {
				continue
			}
			note(p)
			if o.Binding.Name != "" {
				named[p] = true
			}
			collect(entity[p], o.Binding.Values, o.Provenance.Where())
		case o.Kind == model.KindAttribute && o.Attr != nil && o.Attr.Binding != "":
			p, _, _ := strings.Cut(o.Attr.Binding, ":")
			if known[p] != nil {
				continue
			}
			note(p)
			collect(attrs[p], o.Attr.Values, o.Provenance.Where()+"#"+o.Attr.Name)
		}
	}
	if len(order) == 0 {
		return obs, nil, nil
	}
	var out []*perspective.Derivation
	var stale []perspective.Edit
	type change struct {
		name    string
		renames map[string]string
		dropped map[string]bool
	}
	changes := map[string]change{}
	for _, p := range order {
		def := &perspective.Definition{Name: p, Display: p, GraphDisplay: map[string]any{}, Origin: perspective.OriginDerived}
		dv := &perspective.Derivation{Definition: def, Status: perspective.StatusDerived}
		if named[p] {
			def.BindingKey = valueOnlyBindingKey
			def.Declare(perspective.Entity, valueOnlyBindingKey, perspective.Extension{Display: "Binding", ExtensionType: "short_text",
				Description: "The name the inspector gave this binding of the noun."})
			dv.Evidence = append(dv.Evidence, perspective.Evidence{Item: "binding_key", Rule: "the inspector named several bindings of one noun"})
		}
		for _, at := range []struct {
			attach perspective.Attach
			seen   map[string]*seen
		}{{perspective.Entity, entity[p]}, {perspective.Attribute, attrs[p]}} {
			for _, k := range sortedKeys(at.seen) {
				ext, ev := extensionOf(k, at.seen[k])
				ev.Item = string(at.attach) + "." + k
				ev.Rule = "named by the inspector; " + ev.Rule
				def.Declare(at.attach, k, ext)
				dv.Evidence = append(dv.Evidence, ev)
			}
		}
		r, d, s := dv.Apply(edits)
		stale = append(stale, s...)
		changes[p] = change{def.Name, r, d}
		out = append(out, dv)
	}
	rewrite := func(c change, values map[string]model.Value) map[string]model.Value {
		if len(c.renames) == 0 && len(c.dropped) == 0 {
			return values
		}
		nv := map[string]model.Value{}
		for k, v := range values {
			if c.dropped[k] {
				continue
			}
			if to, ok := c.renames[k]; ok {
				k = to
			}
			nv[k] = v
		}
		return nv
	}
	for i, o := range obs {
		switch {
		case o.Kind == model.KindBinding && o.Binding != nil:
			if c, ok := changes[o.Binding.Perspective]; ok {
				b := *o.Binding
				b.Perspective, b.Values = c.name, rewrite(c, b.Values)
				obs[i].Binding = &b
			}
		case o.Kind == model.KindAttribute && o.Attr != nil && o.Attr.Binding != "":
			p, name, _ := strings.Cut(o.Attr.Binding, ":")
			if c, ok := changes[p]; ok {
				a := *o.Attr
				a.Binding, a.Values = model.BindingKey(c.name, name), rewrite(c, a.Values)
				obs[i].Attr = &a
			}
		}
	}
	return obs, out, stale
}

// collect records values an inspector chose as seen properties: a bool is a
// flag, anything else text whose kind its values decide.
func collect(into map[string]*seen, values map[string]model.Value, where string) {
	for k, v := range values {
		if into[k] == nil {
			into[k] = newSeen()
		}
		p := model.Prop{Value: v.String(), Class: model.ClassText}
		if _, ok := v.Value.(bool); ok {
			p.Class = model.ClassBool
		}
		into[k].add(p, where)
	}
}

// resolveSidecar reads a sidecar against its perspective's definition, at
// the authority the inspector gave it.
func resolveSidecar(o model.Observation, known map[string]*perspective.Definition) []model.Observation {
	sc := o.Sidecar
	def := known[strings.ToLower(sc.Perspective)]
	if def == nil {
		def = known[sc.Perspective]
	}
	finding := func(sev model.Severity, code, msg string) []model.Observation {
		p := o.Provenance
		return []model.Observation{{Kind: model.KindFinding, Provenance: p,
			Finding: &model.FindingObs{Severity: sev, Code: code, Message: msg}}}
	}
	if def == nil {
		return finding(model.SevInfo, "unknown-extension",
			fmt.Sprintf("extension file for %q, which is neither ui nor a known perspective", sc.Perspective))
	}
	obs, err := model.SidecarObservations(sc.Data, def.Name, def.BindingKey, o.Provenance)
	if err != nil {
		return finding(model.SevError, "unparseable-sidecar", err.Error())
	}
	for _, x := range obs {
		if x.Attr != nil {
			x.Attr.Type = perspective.CoreType(def, x.Attr.Values)
		}
	}
	return obs
}
