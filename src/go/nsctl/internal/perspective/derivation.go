package perspective

import (
	"fmt"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/model"
)

// Status of a perspective in the perspective IR (NERD033 SPEC005).
const (
	StatusDerived = "derived"
	StatusEdited  = "edited"
)

// Derivation is one perspective in the IR: a definition proposed from the
// files, the evidence for each of its pieces, and whether a person has
// edited it since.
type Derivation struct {
	Definition *Definition `json:"definition"`
	Status     string      `json:"status"`
	Evidence   []Evidence  `json:"evidence,omitempty"`
}

// Evidence explains one derived piece: which rule produced it, how many
// objects support it, examples and exceptions, and whether a person should
// look at it.
type Evidence struct {
	// Item is what the evidence is for: "binding_key", "name_pattern",
	// "primary", "entity.<key>", "attribute.<key>", "attribute.<key>=<value>".
	Item       string   `json:"item"`
	Rule       string   `json:"rule"`
	Support    int      `json:"support"`
	Examples   []string `json:"examples,omitempty"`
	Exceptions []string `json:"exceptions,omitempty"`
	Review     bool     `json:"review,omitempty"`
}

// Edit is one change a person made to a derived perspective, kept as an
// operation so that deriving again after the files change keeps it.
type Edit struct {
	Perspective string `json:"perspective"`
	// Op is rename (the perspective, to To), rename-key (Key to To),
	// drop-key (Key), or hms-type (enum value Value of the type key gets
	// .hms type To).
	Op    string `json:"op"`
	Key   string `json:"key,omitempty"`
	Value string `json:"value,omitempty"`
	To    string `json:"to,omitempty"`
}

// Ops are the edit operations, with the arguments each takes after the
// perspective's name.
var Ops = map[string][]string{
	"rename":     {"to"},
	"rename-key": {"key", "to"},
	"drop-key":   {"key"},
	"hms-type":   {"value", "to"},
}

// ParseEdit reads an edit from its command-line form: op and arguments.
func ParseEdit(perspective, op string, args []string) (Edit, error) {
	want, ok := Ops[op]
	if !ok {
		return Edit{}, fmt.Errorf("unknown edit %q; one of rename, rename-key, drop-key, hms-type", op)
	}
	if len(args) != len(want) {
		return Edit{}, fmt.Errorf("%s takes %s", op, strings.Join(want, " "))
	}
	e := Edit{Perspective: perspective, Op: op}
	for i, w := range want {
		switch w {
		case "to":
			e.To = args[i]
		case "key":
			e.Key = args[i]
		case "value":
			e.Value = args[i]
		}
	}
	if e.Op == "hms-type" {
		if _, ok := model.HMSType(e.To); !ok {
			return Edit{}, fmt.Errorf("%q is not an .hms type", e.To)
		}
	}
	return e, nil
}

// Apply applies the edits naming d's perspective, in order, and returns the
// key renames applied (old key to new) so values can follow them. Edits
// that no longer fit the derivation (a key that is gone) are returned as
// stale, not applied.
func (dv *Derivation) Apply(edits []Edit) (renames map[string]string, dropped map[string]bool, stale []Edit) {
	renames, dropped = map[string]string{}, map[string]bool{}
	d := dv.Definition
	for _, e := range edits {
		if e.Perspective != d.Name {
			continue
		}
		ok := true
		switch e.Op {
		case "rename":
			d.Name = e.To
			if d.Display == e.Perspective {
				d.Display = e.To
			}
		case "rename-key":
			if ok = d.Rename(e.Key, e.To); ok {
				// renames maps a derived key to its current name, so a
				// key renamed twice maps from where it started.
				chained := false
				for from, to := range renames {
					if to == e.Key {
						renames[from], chained = e.To, true
					}
				}
				if !chained {
					renames[e.Key] = e.To
				}
				if d.NamePattern != nil {
					for _, parts := range [][]NamePart{d.NamePattern.Schema, d.NamePattern.Table} {
						for i := range parts {
							if parts[i].Ref == e.Key {
								parts[i].Ref = e.To
							}
						}
					}
				}
			}
		case "drop-key":
			if ok = d.Drop(e.Key); ok {
				dropped[e.Key] = true
			}
		case "hms-type":
			ok = setHMSType(d, e.Value, e.To)
		}
		if ok {
			dv.Status = StatusEdited
			for i := range dv.Evidence {
				if dv.Evidence[i].Item == "attribute."+d.TypeKey()+"="+e.Value && e.Op == "hms-type" {
					dv.Evidence[i].Review = false
				}
			}
		} else {
			stale = append(stale, e)
		}
	}
	return renames, dropped, stale
}

func setHMSType(d *Definition, value, to string) bool {
	key := d.TypeKey()
	if key == "" {
		// No value has a type yet: the first enum attribute key is the
		// candidate.
		for _, m := range d.AttributeExtensions {
			for k, ext := range m {
				if ext.ExtensionType == "enum" && key == "" {
					key = k
				}
			}
		}
	}
	for _, m := range d.AttributeExtensions {
		ext, ok := m[key]
		if !ok {
			continue
		}
		for i := range ext.EnumValues {
			if ext.EnumValues[i].ID == value {
				ext.EnumValues[i].HMSType = to
				m[key] = ext
				return true
			}
		}
	}
	return false
}
