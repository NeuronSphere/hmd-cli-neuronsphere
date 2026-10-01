package sqlddl

import (
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/model"
)

// Type knowledge here is the SQL grammar's, not any perspective's: the
// synonyms SQL itself defines for a type, the names it gives a type's
// arguments, and which family a type belongs to. Which core .hms type a SQL
// type corresponds to is a perspective's business (its enum values'
// hms_type), and is derived, not stated here (NERD033 SPEC003).

// synonyms are spellings SQL (ISO 9075, and Trino after it) defines as the
// same type.
var synonyms = map[string]string{
	"int": "integer", "character varying": "varchar", "character": "char",
	"numeric": "decimal", "dec": "decimal", "double precision": "double",
	"bool": "boolean",
}

// params names a type's arguments in order.
var params = map[string][]string{
	"varchar":   {"max_length"},
	"char":      {"length"},
	"decimal":   {"precision", "scale"},
	"time":      {"precision"},
	"timestamp": {"precision"},
	"array":     {"element"},
	"map":       {"key", "value"},
	"row":       {"fields"},
}

// Type categories.
const (
	CatCharacter   = "character"
	CatBinary      = "binary"
	CatBoolean     = "boolean"
	CatInteger     = "integer"
	CatDecimal     = "decimal"
	CatApproximate = "approximate"
	CatDate        = "date"
	CatTime        = "time"
	CatTimestamp   = "timestamp"
	CatJSON        = "json"
	CatUUID        = "uuid"
	CatArray       = "array"
	CatMap         = "map"
	CatRow         = "row"
	CatUnknown     = "unknown"
)

var categories = map[string]string{
	"varchar": CatCharacter, "char": CatCharacter, "string": CatCharacter, "text": CatCharacter,
	"varbinary": CatBinary, "binary": CatBinary,
	"boolean": CatBoolean,
	"tinyint": CatInteger, "smallint": CatInteger, "integer": CatInteger, "bigint": CatInteger,
	"decimal": CatDecimal,
	"real":    CatApproximate, "double": CatApproximate, "float": CatApproximate,
	"date": CatDate, "time": CatTime, "timestamp": CatTimestamp,
	"json": CatJSON, "uuid": CatUUID,
	"array": CatArray, "map": CatMap, "row": CatRow,
}

// ParseType parses a column type as written ("VARCHAR(255)", "decimal(10,
// 2)", "timestamp(3) with time zone", "array(varchar)").
func ParseType(raw string) model.PhysType {
	t := strings.ToLower(strings.TrimSpace(raw))
	var args []string
	if i := strings.IndexByte(t, '('); i >= 0 {
		if j := strings.LastIndexByte(t, ')'); j > i {
			args = splitTop(t[i+1 : j])
			t = strings.TrimSpace(t[:i] + t[j+1:])
		}
	}
	t = strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(t, "with time zone"), "without time zone"))
	t = strings.Join(strings.Fields(t), " ")
	if s, ok := synonyms[t]; ok {
		t = s
	}
	cat, ok := categories[t]
	if !ok {
		cat = CatUnknown
	}
	names := params[t]
	if t == "row" && len(args) > 1 {
		args = []string{strings.Join(args, ", ")}
	}
	var ps []string
	for i := range args {
		if i < len(names) {
			ps = append(ps, names[i])
		} else {
			ps = append(ps, "arg"+string(rune('1'+i)))
		}
	}
	return model.PhysType{Raw: strings.TrimSpace(raw), Base: t, Args: args, Params: ps, Category: cat}
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
