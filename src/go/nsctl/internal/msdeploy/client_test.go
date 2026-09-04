package msdeploy

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
)

// ms-base types these fields as str and base64-decodes them, so a native list
// is rejected 422 and a plain JSON string fails base64 decoding.
func TestEncodeCollectionIsBase64JSON(t *testing.T) {
	t.Parallel()

	encoded, err := EncodeCollection([]map[string]any{{"name": "a"}})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("the value is not base64: %v", err)
	}
	var out []map[string]any
	if err := json.Unmarshal(decoded, &out); err != nil {
		t.Fatalf("the decoded value is not JSON: %v", err)
	}
	if len(out) != 1 || out[0]["name"] != "a" {
		t.Errorf("round trip lost the content: %v", out)
	}
}

func TestDecodeCollectionTakesEitherShape(t *testing.T) {
	t.Parallel()

	encoded, _ := EncodeCollection([]map[string]any{{"name": "a"}})
	tests := []struct {
		name  string
		input any
		want  int
	}{
		{"base64 JSON", encoded, 1},
		{"plain JSON", `[{"name":"a"}]`, 1},
		{"a native list", []any{map[string]any{"name": "a"}}, 1},
		{"nil", nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := DecodeCollection(tt.input)
			if err != nil {
				t.Fatalf("DecodeCollection: %v", err)
			}
			if len(got) != tt.want {
				t.Errorf("got %v, want %d entries", got, tt.want)
			}
		})
	}
}

// add_repo_class_version answers 400 "already has version" and re-registering
// has to be a no-op: more than one seeding path registers the same repo class.
func TestErrorAlreadyExists(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  *Error
		want bool
	}{
		{"the real message", &Error{Status: 400, Body: `{"detail":"RepoClass, X, already has version 0.1."}`}, true},
		{"a different 400", &Error{Status: 400, Body: `{"detail":"required role, rds-loggroup, not provided"}`}, false},
		{"a 500", &Error{Status: 500, Body: "already"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.err.AlreadyExists(); got != tt.want {
				t.Errorf("AlreadyExists = %v, want %v", got, tt.want)
			}
		})
	}
}

// The body is where ms-deployment says what was wrong; the status alone is
// useless.
func TestErrorCarriesTheBody(t *testing.T) {
	t.Parallel()

	err := &Error{Operation: "apply_changeset", Status: http.StatusBadRequest, Body: "required role, rds-loggroup, not provided"}
	msg := err.Error()
	for _, want := range []string{"apply_changeset", "400", "rds-loggroup"} {
		if !contains(msg, want) {
			t.Errorf("error %q does not contain %q", msg, want)
		}
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// The zero Filter means "everything". Its struct encoding names an empty
// attribute, and ms-deployment answers that with a 500 rather than every row,
// so the wire form has to be a bare {} -- what Python's _search_entities
// sends.
func TestEmptyFilterMarshalsToAnEmptyObject(t *testing.T) {
	t.Parallel()

	data, err := json.Marshal(Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{}" {
		t.Errorf("Filter{} marshals to %s, want {}", data)
	}
}

// A real filter still serialises in full, including values that omitempty on
// the field would have dropped.
func TestFilterKeepsFalsyValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		filter Filter
		want   string
	}{
		{"string", Filter{"name", "=", "local"}, `{"attribute":"name","operator":"=","value":"local"}`},
		{"empty string", Filter{"name", "=", ""}, `{"attribute":"name","operator":"=","value":""}`},
		{"false", Filter{"auto_deploy", "=", false}, `{"attribute":"auto_deploy","operator":"=","value":false}`},
		{"zero", Filter{"count", "=", 0}, `{"attribute":"count","operator":"=","value":0}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			data, err := json.Marshal(tt.filter)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != tt.want {
				t.Errorf("got %s, want %s", data, tt.want)
			}
		})
	}
}
