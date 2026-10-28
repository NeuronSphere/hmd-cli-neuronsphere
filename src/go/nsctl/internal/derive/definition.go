package derive

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/model"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/perspective"
)

// Enumeration thresholds: a text property is an enum when it has at most
// maxEnumValues distinct values, each a bare word, and its values repeat:
// it is seen at least minEnumRepeat times as often as it has values. Two
// names seen once each are names, not a vocabulary. The thresholds are in
// every enum's evidence.
const (
	maxEnumValues = 6
	minEnumRepeat = 2
)

var bareWord = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

// categoryHMS is the nearest core .hms type of each SQL type category,
// used for a physical type no .hms attribute is paired with.
var categoryHMS = map[string]string{
	"character": "string", "binary": "blob", "boolean": "bool", "integer": "integer",
	"decimal": "float", "approximate": "float", "date": "timestamp", "time": "string",
	"timestamp": "timestamp", "json": "mapping", "uuid": "string", "array": "collection",
	"map": "mapping", "row": "mapping",
}

// seen collects one property's values across objects.
type seen struct {
	class    model.PropClass
	values   map[string]int // by canonical value
	spelling map[string]map[string]bool
	where    map[string][]string
	types    map[string]model.PhysType
	support  int
}

func newSeen() *seen {
	return &seen{values: map[string]int{}, spelling: map[string]map[string]bool{},
		where: map[string][]string{}, types: map[string]model.PhysType{}}
}

func (s *seen) add(p model.Prop, where string) {
	s.class = p.Class
	s.support++
	v := p.Value
	if p.Type != nil {
		v = p.Type.Base
		s.types[v] = *p.Type
	}
	canon := v
	if p.Class == model.ClassKeyword || p.Class == model.ClassText || p.Class == model.ClassType {
		canon = strings.ToLower(v)
	}
	s.values[canon]++
	if s.spelling[canon] == nil {
		s.spelling[canon] = map[string]bool{}
	}
	if p.Type != nil {
		// The spelling is the type name as written, without arguments.
		sp := strings.ToLower(p.Type.Raw)
		if i := strings.IndexByte(sp, '('); i >= 0 {
			sp = sp[:i]
		}
		s.spelling[canon][strings.Join(strings.Fields(sp), " ")] = true
	}
	if len(s.where[canon]) < 3 {
		s.where[canon] = append(s.where[canon], where)
	}
}

func (s *seen) examples() []string {
	var out []string
	for _, v := range sortedKeys(s.where) {
		for _, w := range s.where[v] {
			if len(out) < 3 {
				out = append(out, w)
			}
		}
	}
	return out
}

// derive proposes the dialect's definition from its objects.
func (d *dialectRun) derive() *perspective.Derivation {
	def := &perspective.Definition{GraphDisplay: map[string]any{}, Origin: perspective.OriginDerived}
	dv := &perspective.Derivation{Definition: def, Status: perspective.StatusDerived}
	add := func(e perspective.Evidence) { dv.Evidence = append(dv.Evidence, e) }

	variants, vev := d.variants()
	d.nm = &namer{variants: variants}
	if len(variants) > 0 {
		def.BindingKey = BindingKeyName
		var evs []perspective.EnumValue
		for _, v := range variants {
			evs = append(evs, perspective.EnumValue{ID: v, Description: v, Definition: v})
		}
		def.Declare(perspective.Entity, BindingKeyName, perspective.Extension{
			Display: "Layer", ExtensionType: "enum", Default: "", EnumValues: evs,
			Description: "Which of a noun's objects this binding is, read from the varying schema suffix; in the order data flows between them.",
		})
		vev.Item = "binding_key"
		add(vev)
		def.NamePattern = &perspective.NamePattern{
			Schema: []perspective.NamePart{{Ref: "namespace"}, {Lit: "_"}, {Ref: BindingKeyName}},
			Table:  []perspective.NamePart{{Ref: "name"}},
		}
		add(perspective.Evidence{Item: "name_pattern", Rule: "schema is <namespace>_<" + BindingKeyName + ">, table is the noun's name",
			Support: vev.Support, Examples: vev.Examples})
		add(perspective.Evidence{Item: "primary", Rule: "the most downstream binding whose columns are declared",
			Support: len(variants), Examples: []string{strings.Join(variants, " -> ")}})
	}

	// Entity properties.
	entity := map[string]*seen{}
	templated := 0
	var templateWhere []string
	nameMatches, nameTotal := 0, 0
	for _, o := range d.objects {
		where := o.Provenance.Where()
		for k, p := range o.Object.Props {
			if entity[k] == nil {
				entity[k] = newSeen()
			}
			entity[k].add(p, where)
		}
		if hasPlaceholder(o.Object.Location) {
			templated++
			if len(templateWhere) < 3 {
				templateWhere = append(templateWhere, where)
			}
		}
		if t, ok := o.Object.Props["table_name"]; ok {
			nameTotal++
			id, _ := d.nm.identity(o.Object.Location)
			if t.Value == id.Name {
				nameMatches++
			}
		}
	}
	for _, k := range sortedKeys(entity) {
		ext, ev := extensionOf(k, entity[k])
		if k == "table_name" && nameTotal > 0 && nameMatches*2 > nameTotal {
			ext.Default = map[string]any{"ref": "name"}
			ev.Rule += fmt.Sprintf("; default is the noun's name, as in %d of %d objects", nameMatches, nameTotal)
		}
		def.Declare(perspective.Entity, k, ext)
		ev.Item = "entity." + k
		add(ev)
	}
	if templated > 0 {
		def.Declare(perspective.Entity, "template", perspective.Extension{
			Display: "Name Template", ExtensionType: "short_text",
			Description: "The qualified name before run-time values are known, each such value written {name}.",
		})
		add(perspective.Evidence{Item: "entity.template", Rule: "a physical name has {placeholder} segments known only at run time",
			Support: templated, Examples: templateWhere})
	}

	// Attribute properties.
	attrs := map[string]*seen{}
	paired := map[string]map[model.LogicalType][]string{}
	for _, o := range d.objects {
		id, _ := d.nm.identity(o.Object.Location)
		for _, c := range o.Object.Columns {
			where := fmt.Sprintf("%s#%s", o.Provenance.Where(), c.Name)
			for k, p := range c.Props {
				if attrs[k] == nil {
					attrs[k] = newSeen()
				}
				attrs[k].add(p, where)
				if p.Type != nil {
					if t, ok := d.hmsTypes[id][strings.ToLower(c.Name)]; ok {
						if paired[p.Type.Base] == nil {
							paired[p.Type.Base] = map[model.LogicalType][]string{}
						}
						paired[p.Type.Base][t] = append(paired[p.Type.Base][t], id.String()+"#"+c.Name)
					}
				}
			}
		}
	}
	for _, k := range sortedKeys(attrs) {
		s := attrs[k]
		ext, ev := extensionOf(k, s)
		ev.Item = "attribute." + k
		add(ev)
		if s.class == model.ClassType {
			for i, v := range ext.EnumValues {
				t, tev := hmsTypeOf(v.ID, s.types[v.ID], paired[v.ID])
				ext.EnumValues[i].HMSType = t
				tev.Item = "attribute." + k + "=" + v.ID
				tev.Support = s.values[v.ID]
				tev.Examples = s.where[v.ID]
				add(tev)
			}
		}
		def.Declare(perspective.Attribute, k, ext)
	}
	return dv
}

// extensionOf decides an extension's kind from what was seen.
func extensionOf(key string, s *seen) (perspective.Extension, perspective.Evidence) {
	ext := perspective.Extension{Display: display(key)}
	ev := perspective.Evidence{Support: s.support, Examples: s.examples()}
	distinct := sortedKeys(s.values)
	switch {
	case s.class == model.ClassBool:
		ext.ExtensionType = "bool"
		// Grammar states a flag only when it holds: the default is the other.
		ext.Default = "true"
		if len(distinct) == 1 && distinct[0] == "true" {
			ext.Default = "false"
		}
		ev.Rule = "a flag, stated only when it differs from the default " + fmt.Sprint(ext.Default)
	case s.class == model.ClassType:
		ext.ExtensionType = "enum"
		for _, v := range distinct {
			t := s.types[v]
			ev := perspective.EnumValue{ID: v, Description: v, Definition: strings.ToUpper(v)}
			if len(t.Params) > 0 {
				ev.Parameters = map[string]perspective.Parameter{}
				for i, p := range t.Params {
					ev.Parameters[p] = perspective.Parameter{Description: p, Default: nil, Position: i + 1}
				}
			}
			for sp := range s.spelling[v] {
				if sp != v && sp != "" {
					ev.Aliases = append(ev.Aliases, sp)
				}
			}
			sort.Strings(ev.Aliases)
			ext.EnumValues = append(ext.EnumValues, ev)
		}
		ev.Rule = "a data type: one enum value per base type seen, its arguments as ordered parameters"
	case s.class == model.ClassKeyword:
		ext.ExtensionType = "enum"
		ext.EnumValues = enumValues(distinct)
		ev.Rule = "a keyword of the grammar"
	case s.class == model.ClassText && enumerable(s, distinct):
		ext.ExtensionType = "enum"
		ext.EnumValues = enumValues(distinct)
		ev.Rule = fmt.Sprintf("text with %d distinct bare-word values over %d objects (enum when at most %d values, each seen %d times on average)",
			len(distinct), s.support, maxEnumValues, minEnumRepeat)
	default:
		ext.ExtensionType = "short_text"
		switch s.class {
		case model.ClassIdent:
			ev.Rule = "an identifier"
		case model.ClassList:
			ev.Rule = "a list, comma-separated"
		default:
			ev.Rule = fmt.Sprintf("text with %d distinct values over %d objects", len(distinct), s.support)
		}
	}
	return ext, ev
}

func enumerable(s *seen, distinct []string) bool {
	if len(distinct) > maxEnumValues || s.support < minEnumRepeat*len(distinct) {
		return false
	}
	for _, v := range distinct {
		if !bareWord.MatchString(v) {
			return false
		}
	}
	return true
}

func enumValues(ids []string) []perspective.EnumValue {
	var out []perspective.EnumValue
	for _, v := range ids {
		out = append(out, perspective.EnumValue{ID: v, Description: v, Definition: strings.ToUpper(v)})
	}
	return out
}

// hmsTypeOf is the core type of a physical type: the .hms type its columns
// are paired with where both exist, else its category's nearest type.
func hmsTypeOf(base string, t model.PhysType, paired map[model.LogicalType][]string) (string, perspective.Evidence) {
	if len(paired) > 0 {
		var best model.LogicalType
		n := -1
		var exceptions []string
		for _, lt := range sortedTypes(paired) {
			if len(paired[lt]) > n {
				best, n = lt, len(paired[lt])
			}
		}
		for _, lt := range sortedTypes(paired) {
			if lt != best {
				for _, w := range paired[lt] {
					exceptions = append(exceptions, fmt.Sprintf("%s is %s", w, lt))
				}
			}
		}
		return string(best), perspective.Evidence{Rule: fmt.Sprintf("paired with .hms attributes of type %s", best),
			Exceptions: exceptions, Review: len(exceptions) > 0}
	}
	if h, ok := categoryHMS[t.Category]; ok {
		return h, perspective.Evidence{Rule: fmt.Sprintf("no .hms attribute to pair with; nearest .hms type of SQL category %s", t.Category)}
	}
	return "", perspective.Evidence{Rule: "no .hms attribute to pair with, and an unknown SQL type", Review: true}
}

func sortedTypes(m map[model.LogicalType][]string) []model.LogicalType {
	out := make([]model.LogicalType, 0, len(m))
	for t := range m {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func display(key string) string {
	words := strings.Split(strings.TrimPrefix(key, "is_"), "_")
	for i, w := range words {
		if w != "" {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}
