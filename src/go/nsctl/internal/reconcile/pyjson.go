package reconcile

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// pythonJSON serialises a value exactly as CPython's
// json.dumps(value, sort_keys=True, default=str) does.
//
// Byte-for-byte, because the result is hashed and compared against digests the
// Python front end wrote. Go's encoding/json differs in four ways that all
// matter here:
//
//	separators   Python defaults to ", " and ": "; Go emits neither space.
//	non-ASCII    Python escapes it as \uXXXX; Go emits raw UTF-8.
//	HTML         Go escapes < > & as < etc; Python does not.
//	floats       Python uses repr, so 2.0 stays "2.0"; Go emits "2".
//
// Any one of them would make every entry read as changed on a user's first
// `nsctl env apply` after using the Python CLI, which is a full redeploy of a
// working environment.
func pythonJSON(value any) string {
	var b strings.Builder
	writePythonJSON(&b, value)
	return b.String()
}

func writePythonJSON(b *strings.Builder, value any) {
	switch v := value.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if v {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case string:
		writePythonString(b, v)
	case json.Number:
		b.WriteString(v.String())
	case float32:
		writePythonFloat(b, float64(v))
	case float64:
		writePythonFloat(b, v)
	case int:
		b.WriteString(strconv.Itoa(v))
	case int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		fmt.Fprintf(b, "%d", v)
	case map[string]any:
		writePythonMap(b, v)
	case map[string]string:
		converted := make(map[string]any, len(v))
		for k, item := range v {
			converted[k] = item
		}
		writePythonMap(b, converted)
	case []any:
		b.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				b.WriteString(", ")
			}
			writePythonJSON(b, item)
		}
		b.WriteByte(']')
	case []string:
		b.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				b.WriteString(", ")
			}
			writePythonString(b, item)
		}
		b.WriteByte(']')
	default:
		// json.dumps(default=str) stringifies anything it cannot serialise,
		// rather than failing. Reaching here means a type the entry builder
		// does not produce, so matching that behaviour is the safe reading.
		writePythonString(b, fmt.Sprint(v))
	}
}

func writePythonMap(b *strings.Builder, m map[string]any) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteString(", ")
		}
		writePythonString(b, k)
		b.WriteString(": ")
		writePythonJSON(b, m[k])
	}
	b.WriteByte('}')
}

// writePythonFloat matches repr(float): an integral value keeps its ".0", and
// the non-finite values become the bare words json.dumps emits for them.
func writePythonFloat(b *strings.Builder, f float64) {
	switch {
	case math.IsNaN(f):
		b.WriteString("NaN")
	case math.IsInf(f, 1):
		b.WriteString("Infinity")
	case math.IsInf(f, -1):
		b.WriteString("-Infinity")
	case f == math.Trunc(f) && math.Abs(f) < 1e16:
		b.WriteString(strconv.FormatFloat(f, 'f', 1, 64))
	default:
		b.WriteString(strconv.FormatFloat(f, 'g', -1, 64))
	}
}

// writePythonString reproduces json.dumps's default ensure_ascii=True escaping.
func writePythonString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			switch {
			case r < 0x20:
				fmt.Fprintf(b, `\u%04x`, r)
			case r < 0x7f:
				b.WriteRune(r)
			case r <= 0xffff:
				fmt.Fprintf(b, `\u%04x`, r)
			default:
				// Outside the BMP Python emits a surrogate pair, as the JSON
				// spec requires for \u escapes.
				r -= 0x10000
				fmt.Fprintf(b, `\u%04x\u%04x`, 0xd800+(r>>10), 0xdc00+(r&0x3ff))
			}
		}
	}
	b.WriteByte('"')
}
