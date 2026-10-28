package derive

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/model"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/perspective"
)

// observations turns the dialect's objects into binding and attribute
// observations of the definition in effect.
func (d *dialectRun) observations() []model.Observation {
	nm := d.namer()
	rank := map[string]int{}
	for i, v := range nm.variants {
		rank[v] = i + 1
	}
	// The primary binding of a noun is its most downstream one whose
	// columns are declared (a view's are only selected).
	primary := map[model.ID]string{}
	best := map[model.ID]int{}
	for _, o := range d.objects {
		if o.Object.Reference || len(o.Object.Columns) == 0 {
			continue
		}
		id, layer := nm.identity(o.Object.Location)
		if r, ok := best[id]; !ok || rank[layer] > r {
			best[id], primary[id] = rank[layer], layer
		}
	}

	var out []model.Observation
	for _, o := range d.objects {
		ob := o.Object
		id, layer := nm.identity(ob.Location)
		values := map[string]model.Value{}
		for k, p := range ob.Props {
			if key, ok := d.key(perspective.Entity, k); ok {
				values[key] = d.value(perspective.Entity, key, p)
			}
		}
		if hasPlaceholder(ob.Location) {
			if key, ok := d.key(perspective.Entity, "template"); ok {
				values[key] = model.V(ob.Location.Schema + "." + ob.Location.Table)
				if ob.Location.Catalog != "" {
					values[key] = model.V(ob.Location.String())
				}
			}
		}
		_, isPrimary := primary[id]
		b := &model.BindingObs{Perspective: d.def.Name, Name: layer, Location: ob.Location,
			Primary: isPrimary && primary[id] == layer && !ob.Reference, Reference: ob.Reference, Values: values}
		out = append(out, model.Observation{Kind: model.KindBinding, Subject: id, Binding: b, Provenance: o.Provenance})

		typeKey := d.def.TypeKey()
		for _, c := range ob.Columns {
			a := &model.AttrObs{Name: c.Name, Binding: b.Key(), Position: c.Position, Type: model.Unknown,
				Values: map[string]model.Value{}}
			for k, p := range c.Props {
				if key, ok := d.key(perspective.Attribute, k); ok {
					a.Values[key] = d.value(perspective.Attribute, key, p)
				}
			}
			if typeKey != "" {
				a.Type = perspective.CoreType(d.def, map[string]model.Value{typeKey: a.Values[typeKey]})
			}
			if c.NotNull {
				a.Required = model.BoolPtr(true)
			}
			if len(a.Values) == 0 {
				a.Values = nil
			}
			p := o.Provenance
			if c.Line > 0 {
				p.Line = c.Line
			}
			out = append(out, model.Observation{Kind: model.KindAttribute, Subject: id, Attr: a, Provenance: p})
		}
		for _, c := range ob.Selected {
			p := o.Provenance
			p.Authority, p.Why = ob.SelectAuthority, "select list"
			if c.Line > 0 {
				p.Line = c.Line
			}
			out = append(out, model.Observation{Kind: model.KindAttribute, Subject: id, Provenance: p,
				Attr: &model.AttrObs{Name: c.Name, Binding: b.Key(), Position: c.Position, Type: model.Unknown}})
		}
	}
	return out
}

// value is a property as a perspective value of the definition in effect:
// an enum value is its id with its definition's spelling (and a data type's
// arguments as named parameters), a bool is a bool, anything else text.
func (d *dialectRun) value(at perspective.Attach, key string, p model.Prop) model.Value {
	ext, declared := d.def.Extension(at, key)
	switch {
	case p.Type != nil:
		v := model.Value{Value: p.Type.Base, Definition: strings.ToUpper(p.Type.Raw)}
		if ev, ok := ext.EnumValue(p.Type.Base); ok {
			v.Value, v.Definition = ev.ID, ev.Definition
		}
		for i, a := range p.Type.Args {
			if i >= len(p.Type.Params) {
				break
			}
			if v.Parameters == nil {
				v.Parameters = map[string]any{}
			}
			a = strings.TrimSpace(a)
			if n, err := strconv.Atoi(a); err == nil {
				v.Parameters[p.Type.Params[i]] = n
			} else {
				v.Parameters[p.Type.Params[i]] = a
			}
		}
		return v
	case p.Class == model.ClassBool || (declared && ext.ExtensionType == "bool"):
		b, _ := strconv.ParseBool(p.Value)
		return model.V(b)
	case declared && ext.ExtensionType == "enum":
		if ev, ok := ext.EnumValue(strings.ToLower(p.Value)); ok {
			return model.Value{Value: ev.ID, Definition: ev.Definition}
		}
		return model.Value{Value: strings.ToLower(p.Value), Definition: strings.ToUpper(p.Value)}
	}
	return model.V(p.Value)
}

// undeclaredFindings reports, once per property, what the files say that a
// declared definition has no key for (NERD033 SPEC003 step 7).
func (d *dialectRun) undeclaredFindings() []model.Observation {
	var out []model.Observation
	for _, k := range sortedKeys(d.undeclared) {
		for _, at := range sortedKeys(d.undeclared[k]) {
			out = append(out, model.Observation{Kind: model.KindFinding,
				Provenance: model.Provenance{Inspector: "derive", File: d.def.Origin, Authority: model.AuthInferred, Confidence: model.Evidence},
				Finding: &model.FindingObs{Severity: model.SevInfo, Code: "perspective-key-undeclared",
					Message: fmt.Sprintf("%d %s properties named %q have no key in %s; their values are not read",
						d.undeclared[k][at], at, k, d.def.Name)}})
		}
	}
	return out
}
