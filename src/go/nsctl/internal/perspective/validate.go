package perspective

import (
	"fmt"
	"sort"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/model"
)

// Validate checks every perspective value in a model against its
// definition, with hmd-lib-ns-model's rules: the perspective is known, each
// key is declared at its attach point, an enum value is one of the declared
// enum_values, and a parameter is one the chosen value declares.
func Validate(m *model.Model, r *Registry) []model.Disagreement {
	var out []model.Disagreement
	add := func(sev model.Severity, code, subject, format string, src []model.Provenance, args ...any) {
		d := model.Disagreement{Severity: sev, Code: code, Subject: subject, Message: fmt.Sprintf(format, args...)}
		if len(src) > 0 {
			d.Sources = src[:1]
		}
		out = append(out, d)
	}
	for _, n := range m.Nouns {
		at := Noun
		if n.Metatype == model.MetaRelationship {
			at = Relationship
		}
		for _, b := range n.Bindings {
			subject := n.ID.String() + "@" + b.Label()
			d := r.Get(b.Perspective)
			if d == nil {
				add(model.SevError, "perspective-unknown", subject, "no definition of perspective %q", b.Sources, b.Perspective)
				continue
			}
			if d.BindingKey == "" && b.Name != "" {
				add(model.SevWarning, "perspective-binding-name", subject,
					"perspective %s allows one binding per noun but this one is named %q", b.Sources, d.Name, b.Name)
			}
			for _, k := range keys(b.Values) {
				checkValue(d, at, k, b.Values[k], subject, b.Sources, add)
			}
			for _, c := range b.Columns {
				csub := n.ID.String() + "#" + c.Name + "@" + b.Label()
				for _, k := range keys(c.Values) {
					checkValue(d, Attribute, k, c.Values[k], csub, c.Sources, add)
				}
			}
		}
	}
	return out
}

type adder func(sev model.Severity, code, subject, format string, src []model.Provenance, args ...any)

func checkValue(d *Definition, at Attach, key string, v model.Value, subject string, src []model.Provenance, add adder) {
	ext, ok := d.Extension(at, key)
	if !ok {
		add(model.SevError, "perspective-undeclared-key", subject,
			"%s declares no %s extension %q", src, d.Name, at, key)
		return
	}
	if ext.ExtensionType != "enum" || v.Value == nil {
		return
	}
	ev, ok := ext.EnumValue(v.String())
	if !ok {
		add(model.SevError, "perspective-enum-value", subject,
			"%s %s is %q, which is not one of its enum_values", src, d.Name, key, v.String())
		return
	}
	for _, p := range keysAny(v.Parameters) {
		if _, ok := ev.Parameters[p]; !ok {
			add(model.SevWarning, "perspective-parameter", subject,
				"%s %s %q takes no parameter %q", src, d.Name, key, ev.ID, p)
		}
	}
}

func keys(m map[string]model.Value) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func keysAny(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
