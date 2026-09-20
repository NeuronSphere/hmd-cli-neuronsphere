package bacon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"unicode/utf8"
)

// Decode reads a JSON document into an ordered Object. Numbers are kept as
// json.Number so that 1.0 stays 1.0 on the way back out.
func Decode(data []byte) (*Object, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, fmt.Errorf("the document is not a JSON object")
	}
	obj, err := decodeObject(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("trailing content after the document")
		}
		return nil, err
	}
	return obj, nil
}

// decodeObject reads members until the closing brace, whose opening brace the
// caller has already consumed.
func decodeObject(dec *json.Decoder) (*Object, error) {
	obj := NewObject()
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		if delim, ok := tok.(json.Delim); ok && delim == '}' {
			return obj, nil
		}
		key, ok := tok.(string)
		if !ok {
			return nil, fmt.Errorf("object key is %v, not a string", tok)
		}
		value, err := decodeValue(dec)
		if err != nil {
			return nil, err
		}
		obj.Set(key, value)
	}
}

func decodeArray(dec *json.Decoder) ([]any, error) {
	list := []any{}
	for {
		if !dec.More() {
			// Consume the closing bracket.
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return list, nil
		}
		value, err := decodeValue(dec)
		if err != nil {
			return nil, err
		}
		list = append(list, value)
	}
}

func decodeValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			return decodeObject(dec)
		case '[':
			return decodeArray(dec)
		}
		return nil, fmt.Errorf("unexpected %v", t)
	default:
		return t, nil
	}
}

// Encode writes an Object the way Python's json.dump(indent=2) does -- the
// layout hmd_lib_manifest.write_manifest produces -- so a file edited by
// either front end does not churn under the other: two-space indent, one
// element per line, {} and [] for empties, non-ASCII escaped, no trailing
// newline.
func Encode(o *Object) ([]byte, error) {
	var buf bytes.Buffer
	if err := encodeValue(&buf, o, 0); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func encodeValue(buf *bytes.Buffer, v any, depth int) error {
	switch t := v.(type) {
	case nil:
		buf.WriteString("null")
	case *Object:
		return encodeObject(buf, t.keys, func(k string) any { return t.values[k] }, depth)
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return encodeObject(buf, keys, func(k string) any { return t[k] }, depth)
	case []any:
		return encodeArray(buf, t, depth)
	case []string:
		list := make([]any, len(t))
		for i, s := range t {
			list[i] = s
		}
		return encodeArray(buf, list, depth)
	case string:
		encodeString(buf, t)
	case bool:
		buf.WriteString(strconv.FormatBool(t))
	case json.Number:
		buf.WriteString(t.String())
	case int:
		buf.WriteString(strconv.Itoa(t))
	case int64:
		buf.WriteString(strconv.FormatInt(t, 10))
	case float64:
		// Python prints a whole float with a trailing .0 and Go does not; a
		// config value that was typed as a number on the command line is
		// integral in every case that has come up, so write it that way.
		if t == float64(int64(t)) {
			buf.WriteString(strconv.FormatInt(int64(t), 10))
		} else {
			buf.WriteString(strconv.FormatFloat(t, 'g', -1, 64))
		}
	default:
		// Anything else goes through encoding/json; it is a programming
		// error for it to arrive here, not a document error.
		data, err := json.Marshal(t)
		if err != nil {
			return err
		}
		buf.Write(data)
	}
	return nil
}

func encodeObject(buf *bytes.Buffer, keys []string, get func(string) any, depth int) error {
	if len(keys) == 0 {
		buf.WriteString("{}")
		return nil
	}
	buf.WriteString("{\n")
	for i, k := range keys {
		indent(buf, depth+1)
		encodeString(buf, k)
		buf.WriteString(": ")
		if err := encodeValue(buf, get(k), depth+1); err != nil {
			return err
		}
		if i < len(keys)-1 {
			buf.WriteString(",")
		}
		buf.WriteString("\n")
	}
	indent(buf, depth)
	buf.WriteString("}")
	return nil
}

func encodeArray(buf *bytes.Buffer, list []any, depth int) error {
	if len(list) == 0 {
		buf.WriteString("[]")
		return nil
	}
	buf.WriteString("[\n")
	for i, v := range list {
		indent(buf, depth+1)
		if err := encodeValue(buf, v, depth+1); err != nil {
			return err
		}
		if i < len(list)-1 {
			buf.WriteString(",")
		}
		buf.WriteString("\n")
	}
	indent(buf, depth)
	buf.WriteString("]")
	return nil
}

func indent(buf *bytes.Buffer, depth int) {
	for i := 0; i < depth; i++ {
		buf.WriteString("  ")
	}
}

// encodeString is json.dumps's default (ensure_ascii=True) string form: the
// short escapes for the control characters that have them, \u00XX for the
// rest, \uXXXX (surrogate pairs above the BMP) for everything non-ASCII, and
// no escaping of / or of <, > and &.
func encodeString(buf *bytes.Buffer, s string) {
	const hex = "0123456789abcdef"
	buf.WriteByte('"')
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		switch {
		case r == '"':
			buf.WriteString(`\"`)
		case r == '\\':
			buf.WriteString(`\\`)
		case r == '\n':
			buf.WriteString(`\n`)
		case r == '\r':
			buf.WriteString(`\r`)
		case r == '\t':
			buf.WriteString(`\t`)
		case r == '\b':
			buf.WriteString(`\b`)
		case r == '\f':
			buf.WriteString(`\f`)
		case r < 0x20 || (r > 0x7e && r < 0x10000):
			buf.WriteString(`\u`)
			buf.WriteByte(hex[(r>>12)&0xf])
			buf.WriteByte(hex[(r>>8)&0xf])
			buf.WriteByte(hex[(r>>4)&0xf])
			buf.WriteByte(hex[r&0xf])
		case r >= 0x10000:
			r -= 0x10000
			for _, unit := range []rune{0xd800 + (r >> 10), 0xdc00 + (r & 0x3ff)} {
				buf.WriteString(`\u`)
				buf.WriteByte(hex[(unit>>12)&0xf])
				buf.WriteByte(hex[(unit>>8)&0xf])
				buf.WriteByte(hex[(unit>>4)&0xf])
				buf.WriteByte(hex[unit&0xf])
			}
		default:
			buf.WriteRune(r)
		}
	}
	buf.WriteByte('"')
}
