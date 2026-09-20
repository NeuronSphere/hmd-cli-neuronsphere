package msdeploy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestValidateChangeSetPostsTheChangesEnvelope(t *testing.T) {
	t.Parallel()

	var method, path string
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"valid": false,
			"errors": [{"type":"missing_required_role","instance":"airflow","message":"required role, database, not supplied"}],
			"warnings": [{"type":"missing_optional_role","instance":"airflow","message":"optional role, cache, not supplied"}]
		}`))
	}))
	defer server.Close()

	changes := []map[string]any{{"repo_instance_name": "airflow", "repo_class_name": "hmd-app-airflow", "repo_class_version": "0.1"}}
	result, err := New(server.URL).ValidateChangeSet(context.Background(), changes)
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost || path != "/apiop/validate_changeset" {
		t.Errorf("request was %s %s", method, path)
	}
	sent, ok := got["changes"].([]any)
	if !ok || len(sent) != 1 {
		t.Fatalf("changes sent = %v", got["changes"])
	}
	if result.Valid {
		t.Error("valid = true, want false")
	}
	if len(result.Errors) != 1 || result.Errors[0].Type != "missing_required_role" {
		t.Errorf("errors = %+v", result.Errors)
	}
	if len(result.Warnings) != 1 || result.Warnings[0].Type != "missing_optional_role" {
		t.Errorf("warnings = %+v", result.Warnings)
	}
}

// suggest_resource_dependencies is a GET whose id travels as a query
// parameter, not a path segment -- distinct from every other operation this
// package calls, which is why it needs its own request-shape assertion.
func TestSuggestResourceDependenciesIsAGetWithTheIDAsAQueryParam(t *testing.T) {
	t.Parallel()

	var method, path, query string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path, query = r.Method, r.URL.Path, r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"database": {
				"resource_definition": {"resource_namespace":"database.neuronsphere.io","resource_definition_name":"postgres","version":"0.1"},
				"version_spec": "", "tag_selector": "", "required": true,
				"suggested_repo_class_name": "hmd-postgres-rds",
				"candidates": [{"name":"environment-db","identifier":"rid-1"}]
			}
		}`))
	}))
	defer server.Close()

	result, err := New(server.URL).SuggestResourceDependencies(context.Background(), "local", "rcv-1")
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodGet || path != "/apiop/suggest_resource_dependencies/local" {
		t.Errorf("request was %s %s", method, path)
	}
	if query != "repo_class_version_id=rcv-1" {
		t.Errorf("query = %q", query)
	}
	suggestion, ok := result["database"]
	if !ok {
		t.Fatalf("no suggestion for role %q in %v", "database", result)
	}
	if suggestion.SuggestedRepoClassName != "hmd-postgres-rds" {
		t.Errorf("suggested_repo_class_name = %q", suggestion.SuggestedRepoClassName)
	}
	if len(suggestion.Candidates) != 1 || suggestion.Candidates[0].Name != "environment-db" {
		t.Errorf("candidates = %+v", suggestion.Candidates)
	}
}

func TestSuggestResourceDependenciesEscapesTheID(t *testing.T) {
	t.Parallel()

	var query string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		w.Write([]byte(`{}`))
	}))
	defer server.Close()

	if _, err := New(server.URL).SuggestResourceDependencies(context.Background(), "local", "id with spaces"); err != nil {
		t.Fatal(err)
	}
	if query != "repo_class_version_id=id+with+spaces" {
		t.Errorf("query = %q", query)
	}
}
