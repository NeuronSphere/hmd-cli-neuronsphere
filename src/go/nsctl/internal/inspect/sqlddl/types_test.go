package sqlddl

import (
	"reflect"
	"testing"
)

func TestParseType(t *testing.T) {
	t.Parallel()
	for raw, want := range map[string]struct {
		base, cat string
		args      []string
		params    []string
	}{
		"varchar":                     {"varchar", CatCharacter, nil, nil},
		"VARCHAR(255)":                {"varchar", CatCharacter, []string{"255"}, []string{"max_length"}},
		"character varying(9)":        {"varchar", CatCharacter, []string{"9"}, []string{"max_length"}},
		"DATE":                        {"date", CatDate, nil, nil},
		"decimal(10, 2)":              {"decimal", CatDecimal, []string{"10", "2"}, []string{"precision", "scale"}},
		"numeric(5)":                  {"decimal", CatDecimal, []string{"5"}, []string{"precision"}},
		"timestamp(3) with time zone": {"timestamp", CatTimestamp, []string{"3"}, []string{"precision"}},
		"int":                         {"integer", CatInteger, nil, nil},
		"double precision":            {"double", CatApproximate, nil, nil},
		"array(varchar)":              {"array", CatArray, []string{"varchar"}, []string{"element"}},
		"map(varchar, array(bigint))": {"map", CatMap, []string{"varchar", "array(bigint)"}, []string{"key", "value"}},
		"row(a varchar, b bigint)":    {"row", CatRow, []string{"a varchar, b bigint"}, []string{"fields"}},
		"geometry":                    {"geometry", CatUnknown, nil, nil},
	} {
		got := ParseType(raw)
		if got.Base != want.base || got.Category != want.cat || !reflect.DeepEqual(got.Args, want.args) ||
			!reflect.DeepEqual(got.Params, want.params) || got.Raw != raw {
			t.Errorf("%q = %+v, want %+v", raw, got, want)
		}
	}
}

func TestWithRecordsArrays(t *testing.T) {
	t.Parallel()
	st := Parse(`CREATE TABLE s.t (a INT) WITH (format = 'PARQUET', partitioned_by = ARRAY['a'])`)[0]
	if !st.WithArrays["partitioned_by"] || st.WithArrays["format"] || st.With["format"] != "PARQUET" {
		t.Errorf("with = %v arrays = %v", st.With, st.WithArrays)
	}
}
