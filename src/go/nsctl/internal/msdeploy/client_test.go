package msdeploy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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

func TestServiceVersion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status int
		body   string
		want   string
		wantOK bool
	}{
		{
			// The literal shape hmd-base-service serves: FastAPI builds
			// info.version from HMD_REPO_VERSION.
			name:   "reports info.version",
			status: http.StatusOK,
			body:   `{"openapi":"3.1.0","info":{"title":"ms-deployment","version":"0.4"}}`,
			want:   "0.4",
			wantOK: true,
		},
		{
			name:   "not found",
			status: http.StatusNotFound,
			body:   `{"detail":"Not Found"}`,
		},
		{
			name:   "schema without a version",
			status: http.StatusOK,
			body:   `{"openapi":"3.1.0","info":{"title":"ms-deployment"}}`,
		},
		{
			name:   "unparseable body",
			status: http.StatusOK,
			body:   `<html>gateway</html>`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var path string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path = r.URL.Path
				w.WriteHeader(test.status)
				fmt.Fprint(w, test.body)
			}))
			defer server.Close()

			got, ok := New(server.URL).ServiceVersion(context.Background())
			if ok != test.wantOK || got != test.want {
				t.Fatalf("ServiceVersion() = %q, %v; want %q, %v", got, ok, test.want, test.wantOK)
			}
			if path != "/openapi.json" {
				t.Errorf("requested %q, want /openapi.json", path)
			}
		})
	}
}

func TestServiceVersionUnreachable(t *testing.T) {
	t.Parallel()
	// A closed port: no control plane at all, which is the ordinary case for
	// `nsctl version` on a fresh machine.
	if got, ok := New("http://127.0.0.1:1").ServiceVersion(context.Background()); ok {
		t.Fatalf("ServiceVersion() = %q, true; want unreachable", got)
	}
}

// A DeploymentSet carries its environments in a base64-JSON `definition`, and
// the field is required. nsctl sent {name, environment} instead, which the
// entity rejects:
//
//	PUT hmd_lang_deployment.deployment_set: HTTP 422:
//	{"detail":[{"type":"missing","loc":["body","definition"], ...}]}
//
// Only a cold start reached it -- on any graph that already had the row, the
// find-first guard returned before the PUT.
func TestEnsureDeploymentSetCreatesADefinition(t *testing.T) {
	t.Parallel()

	var put map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPut {
			_ = json.NewDecoder(r.Body).Decode(&put)
			_, _ = w.Write([]byte(`{"identifier":"ds-1"}`))
			return
		}
		_, _ = w.Write([]byte(`[]`)) // no existing row
	}))
	defer server.Close()

	if err := New(server.URL).EnsureDeploymentSet(context.Background(), "dev2", "dev2"); err != nil {
		t.Fatal(err)
	}
	if put["name"] != "dev2" {
		t.Errorf("name = %v, want dev2", put["name"])
	}
	entries := decodeForTest(t, put["definition"])
	if len(entries) != 1 {
		t.Fatalf("definition has %d entries, want 1: %v", len(entries), entries)
	}
	// The slug, not a hardcoded "local": otherwise every named environment's
	// changeset resolves to the default environment's graph.
	if entries[0]["environment"] != "dev2" {
		t.Errorf("definition names %v, want dev2", entries[0]["environment"])
	}
	if _, ok := entries[0]["deployment_gate"]; !ok {
		t.Errorf("definition entry has no deployment_gate: %v", entries[0])
	}
}

// A row naming the wrong environment is repaired in place, because every
// changeset applied through it would otherwise land on that environment's
// graph.
func TestEnsureDeploymentSetRepairsAWrongEnvironment(t *testing.T) {
	t.Parallel()

	stale, err := EncodeCollection([]map[string]any{{"environment": "local"}})
	if err != nil {
		t.Fatal(err)
	}
	var put map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPut {
			_ = json.NewDecoder(r.Body).Decode(&put)
			_, _ = w.Write([]byte(`{"identifier":"ds-1"}`))
			return
		}
		_, _ = fmt.Fprintf(w, `[{"identifier":"ds-1","name":"dev2","definition":%q}]`, stale)
	}))
	defer server.Close()

	if err := New(server.URL).EnsureDeploymentSet(context.Background(), "dev2", "dev2"); err != nil {
		t.Fatal(err)
	}
	if put["identifier"] != "ds-1" {
		t.Fatalf("repair sent identifier %v, want ds-1 -- without it the CRUD layer adds a second row", put["identifier"])
	}
	if entries := decodeForTest(t, put["definition"]); entries[0]["environment"] != "dev2" {
		t.Errorf("repaired definition names %v, want dev2", entries[0]["environment"])
	}
}

// A row that already names the right environment is left alone.
func TestEnsureDeploymentSetLeavesAMatchingRow(t *testing.T) {
	t.Parallel()

	current, err := EncodeCollection([]map[string]any{{"environment": "dev2"}})
	if err != nil {
		t.Fatal(err)
	}
	puts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPut {
			puts++
			_, _ = w.Write([]byte(`{}`))
			return
		}
		_, _ = fmt.Fprintf(w, `[{"identifier":"ds-1","name":"dev2","definition":%q}]`, current)
	}))
	defer server.Close()

	if err := New(server.URL).EnsureDeploymentSet(context.Background(), "dev2", "dev2"); err != nil {
		t.Fatal(err)
	}
	if puts != 0 {
		t.Errorf("wrote %d times, want none", puts)
	}
}

func decodeForTest(t *testing.T, value any) []map[string]any {
	t.Helper()
	entries, err := DecodeCollection(value)
	if err != nil {
		t.Fatalf("decoding the definition: %v", err)
	}
	return entries
}

// get_deployment_resources is a GET answering a bare array, so it fits neither
// APIOp (an object) nor APIOpRaw (a POST). NERD0006's consumer path reads it
// exactly as hmd-cli-deploy does: GET /apiop/<op>, body as served.
func TestAPIOpGetIsAGetAndReturnsTheBody(t *testing.T) {
	t.Parallel()

	var method, path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		w.Write([]byte(`[{"resource_name":"db","output":{"host":"h"}}]`))
	}))
	defer server.Close()

	body, err := New(server.URL).APIOpGet(context.Background(), "get_deployment_resources/rid-1")
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodGet || path != "/apiop/get_deployment_resources/rid-1" {
		t.Errorf("request was %s %s", method, path)
	}
	var resources []map[string]any
	if err := json.Unmarshal(body, &resources); err != nil || len(resources) != 1 {
		t.Errorf("body = %s (%v)", body, err)
	}
}

func TestAPIOpGetReportsTheServiceError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "RepoInstanceDeployment not found", http.StatusNotFound)
	}))
	defer server.Close()

	_, err := New(server.URL).APIOpGet(context.Background(), "get_deployment_resources/nope")
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Fatalf("err = %v, want a 404 *Error", err)
	}
}

// TestReachableRetriesTransientFailure covers a Floci Lambda cold start: its
// idle-eviction reaper runs on its own timer, independent of request timing,
// so a probe landing in the ~1s gap between eviction and the next container
// coming back up must not read as "not answering."
func TestReachableRetriesTransientFailure(t *testing.T) {
	t.Parallel()

	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusBadGateway) // container evicted, not yet cold-started
			return
		}
		w.WriteHeader(http.StatusOK) // cold start finished
	}))
	defer server.Close()

	if !New(server.URL).Reachable(context.Background()) {
		t.Error("Reachable gave up before the transient failure cleared")
	}
	if calls < 3 {
		t.Errorf("Reachable made %d call(s), want at least 3 (it must retry)", calls)
	}
}

func TestReachableRespectsContextCancellation(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	if New(server.URL).Reachable(ctx) {
		t.Error("a cancelled context reported reachable")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Reachable took %v after context cancellation, want it to return promptly", elapsed)
	}
}
