package perspective

import (
	"strconv"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/model"
)

// sqlParams names the positional parameters of the SQL types whose enum
// values declare parameters. A definition's parameters are a JSON object,
// which has no order, so the order lives here.
var sqlParams = map[string][]string{
	"varchar":   {"max_length"},
	"char":      {"length"},
	"decimal":   {"precision", "scale"},
	"time":      {"precision"},
	"timestamp": {"precision"},
	"array":     {"element"},
	"map":       {"key", "value"},
	"row":       {"fields"},
}

var sqlAliases = map[string]string{
	"int": "integer", "double precision": "double", "character varying": "varchar",
	"character": "char", "numeric": "decimal", "bool": "boolean", "string": "varchar",
	"text": "varchar", "float": "double",
}

// SQLDatatype turns a SQL column type as written ("VARCHAR(255)",
// "decimal(10, 2)", "timestamp(3) with time zone") into the datatype value of
// a SQL-family perspective, and the core .hms type that value's enum entry
// names. A type the definition does not list keeps its spelling and has
// type Unknown; validation reports it.
func SQLDatatype(d *Definition, physical string) (model.Value, model.LogicalType) {
	t := strings.ToLower(strings.TrimSpace(physical))
	var args []string
	if i := strings.IndexByte(t, '('); i >= 0 {
		if j := strings.LastIndexByte(t, ')'); j > i {
			args = splitTop(t[i+1 : j])
			t = strings.TrimSpace(t[:i] + t[j+1:])
		}
	}
	t = strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(t, "with time zone"), "without time zone"))
	if a, ok := sqlAliases[t]; ok {
		t = a
	}
	v := model.Value{Value: t, Definition: strings.ToUpper(strings.TrimSpace(physical))}
	var ext Extension
	if d != nil {
		ext, _ = d.Extension(Attribute, "datatype")
	}
	ev, ok := ext.EnumValue(t)
	if !ok {
		return v, model.Unknown
	}
	v.Definition = ev.Definition
	names := sqlParams[t]
	for i, a := range args {
		if i >= len(names) {
			break
		}
		if v.Parameters == nil {
			v.Parameters = map[string]any{}
		}
		a = strings.TrimSpace(a)
		if n, err := strconv.Atoi(a); err == nil {
			v.Parameters[names[i]] = n
		} else {
			v.Parameters[names[i]] = a
		}
	}
	if len(names) == 1 && names[0] == "fields" && len(args) > 0 {
		v.Parameters = map[string]any{"fields": strings.Join(args, ", ")}
	}
	typ, ok := model.HMSType(ev.HMSType)
	if !ok {
		typ = model.Unknown
	}
	return v, typ
}

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

// RenderSQL writes a datatype value back as SQL: DECIMAL(10,2).
func RenderSQL(v model.Value) string {
	def := v.Definition
	if def == "" {
		def = strings.ToUpper(v.String())
	}
	names, ok := sqlParams[v.String()]
	if !ok || len(v.Parameters) == 0 {
		return def
	}
	var parts []string
	for _, n := range names {
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
	return ""
}

// splitTop splits on commas not inside parentheses.
func splitTop(s string) []string {
	var out []string
	depth, start := 0, 0
	for i, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	if rest := strings.TrimSpace(s[start:]); rest != "" {
		out = append(out, rest)
	}
	return out
}
