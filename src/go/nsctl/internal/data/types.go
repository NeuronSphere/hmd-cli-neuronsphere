package data

import (
	"fmt"
	"regexp"
	"strings"
)

// Table definitions name types in Apache Arrow's logical vocabulary, as
// Librarian NERD012 does, so a group translates into a content item type
// unchanged. DuckDB is the engine here; these two functions are the only place
// the vocabularies meet.

var duckToArrow = map[string]string{
	"BOOLEAN":                  "bool",
	"TINYINT":                  "int8",
	"SMALLINT":                 "int16",
	"INTEGER":                  "int32",
	"BIGINT":                   "int64",
	"HUGEINT":                  "decimal128(38, 0)",
	"UTINYINT":                 "uint8",
	"USMALLINT":                "uint16",
	"UINTEGER":                 "uint32",
	"UBIGINT":                  "uint64",
	"FLOAT":                    "float32",
	"DOUBLE":                   "float64",
	"VARCHAR":                  "utf8",
	"BLOB":                     "binary",
	"DATE":                     "date32",
	"TIME":                     "time64[us]",
	"TIMESTAMP":                "timestamp[us]",
	"TIMESTAMP_NS":             "timestamp[ns]",
	"TIMESTAMP_MS":             "timestamp[ms]",
	"TIMESTAMP_S":              "timestamp[s]",
	"TIMESTAMP WITH TIME ZONE": "timestamp[us, UTC]",
	"UUID":                     "utf8",
	"INTERVAL":                 "month_day_nano_interval",
}

var arrowToDuck = map[string]string{
	"bool":                    "BOOLEAN",
	"int8":                    "TINYINT",
	"int16":                   "SMALLINT",
	"int32":                   "INTEGER",
	"int64":                   "BIGINT",
	"uint8":                   "UTINYINT",
	"uint16":                  "USMALLINT",
	"uint32":                  "UINTEGER",
	"uint64":                  "UBIGINT",
	"float32":                 "FLOAT",
	"float64":                 "DOUBLE",
	"utf8":                    "VARCHAR",
	"large_utf8":              "VARCHAR",
	"string":                  "VARCHAR",
	"binary":                  "BLOB",
	"date32":                  "DATE",
	"time64[us]":              "TIME",
	"timestamp[us]":           "TIMESTAMP",
	"timestamp[ns]":           "TIMESTAMP_NS",
	"timestamp[ms]":           "TIMESTAMP_MS",
	"timestamp[s]":            "TIMESTAMP_S",
	"timestamp[us, UTC]":      "TIMESTAMPTZ",
	"month_day_nano_interval": "INTERVAL",
}

var decimalRE = regexp.MustCompile(`^DECIMAL\((\d+),\s*(\d+)\)$`)
var arrowDecimalRE = regexp.MustCompile(`^decimal128\((\d+),\s*(\d+)\)$`)

// DuckToArrow names a DuckDB column type in Arrow's vocabulary. A nested or
// unknown type is kept as DuckDB spells it, prefixed "duckdb:", so nothing is
// silently mistyped.
func DuckToArrow(duck string) string {
	d := strings.ToUpper(strings.TrimSpace(duck))
	if a, ok := duckToArrow[d]; ok {
		return a
	}
	if m := decimalRE.FindStringSubmatch(d); m != nil {
		return fmt.Sprintf("decimal128(%s, %s)", m[1], m[2])
	}
	return "duckdb:" + duck
}

// ArrowToDuck is the reverse, for casts and column types a person wrote.
func ArrowToDuck(arrow string) (string, error) {
	a := strings.TrimSpace(arrow)
	if strings.HasPrefix(a, "duckdb:") {
		return strings.TrimPrefix(a, "duckdb:"), nil
	}
	if d, ok := arrowToDuck[a]; ok {
		return d, nil
	}
	if m := arrowDecimalRE.FindStringSubmatch(a); m != nil {
		return fmt.Sprintf("DECIMAL(%s, %s)", m[1], m[2]), nil
	}
	return "", fmt.Errorf("unknown type %q: use an Arrow type name such as utf8, int64, float64, date32 or timestamp[us]", arrow)
}
