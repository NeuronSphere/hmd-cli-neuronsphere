package msdeploy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// classVersionServer answers find_repo_class_versions with rows, in the
// paginated envelope when the request asks for a page and as a bare list
// otherwise, and records every CRUD PUT body.
func classVersionServer(t *testing.T, rows []map[string]any) (*httptest.Server, *[]map[string]any, *[]string) {
	t.Helper()
	var (
		mu    sync.Mutex
		puts  []map[string]any
		paths []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		paths = append(paths, r.Method+" "+r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/apiop/find_repo_class_versions/"):
			if r.URL.Query().Get("include_deps") != "" {
				_ = json.NewEncoder(w).Encode(map[string]any{"items": rows, "total": len(rows)})
				return
			}
			_ = json.NewEncoder(w).Encode(rows)
		case r.Method == http.MethodPut && r.URL.Path == "/api/"+EntityRepoClassVersion:
			body, _ := io.ReadAll(r.Body)
			var got map[string]any
			if err := json.Unmarshal(body, &got); err != nil {
				t.Errorf("PUT body is not JSON: %v", err)
			}
			puts = append(puts, got)
			_, _ = w.Write(body)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	return srv, &puts, &paths
}

func decodeBlobField(t *testing.T, row map[string]any, key string) any {
	t.Helper()
	s, ok := row[key].(string)
	if !ok {
		t.Fatalf("%s was sent as %T, not the base64 string ms-base decodes", key, row[key])
	}
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("%s is not base64: %v", key, err)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%s does not decode to JSON: %v", key, err)
	}
	return out
}

// The bug this exists for: add_repo_class_version refuses a version it has,
// so an edited default_configuration never reached the catalog and every
// deploy merged the first one registered.
func TestSyncRepoClassVersionUpdatesAChangedDefault(t *testing.T) {
	t.Parallel()

	srv, puts, _ := classVersionServer(t, []map[string]any{
		{"identifier": "rcv-old", "version": "0.1.9", "default_configuration": map[string]any{"replicas": 1}},
		{
			"identifier":            "rcv-1",
			"version":               "0.1.10",
			"default_configuration": map[string]any{"replicas": 1, "exporter": "s3"},
			"deploy_commands":       []any{[]any{"helm"}},
			"toolset":               map[string]any{"name": "helm"},
			"dependencies":          map[string]any{"cluster": map[string]any{"repo_class_name": "x"}},
			"_created":              "2026-10-01T00:00:00",
		},
	})
	defer srv.Close()

	updated, err := New(srv.URL).SyncRepoClassVersion(context.Background(), "hmd-inf-otel-collector", "0.1.10",
		map[string]any{"default_configuration": map[string]any{"replicas": 1, "exporter": "clickhouse"}})
	if err != nil {
		t.Fatal(err)
	}
	if !updated {
		t.Fatal("a changed default reported no update")
	}
	if len(*puts) != 1 {
		t.Fatalf("want one PUT, got %d", len(*puts))
	}
	row := (*puts)[0]
	if row["identifier"] != "rcv-1" || row["version"] != "0.1.10" {
		t.Errorf("updated the wrong row: %v", row)
	}
	got := decodeBlobField(t, row, "default_configuration").(map[string]any)
	if got["exporter"] != "clickhouse" {
		t.Errorf("default_configuration = %v, want the tree's", got)
	}
	// ms-base PUT replaces the whole row, so what the update does not touch
	// must ride along or it is erased.
	if cmds := decodeBlobField(t, row, "deploy_commands"); cmds == nil {
		t.Error("deploy_commands was dropped")
	}
	if ts := decodeBlobField(t, row, "toolset").(map[string]any); ts["name"] != "helm" {
		t.Errorf("toolset = %v, want it kept", ts)
	}
	// Not attributes of the entity: the op adds dependencies, ms-base owns
	// the timestamps.
	for _, k := range []string{"dependencies", "_created"} {
		if _, ok := row[k]; ok {
			t.Errorf("%s was sent; it is not a repo_class_version attribute", k)
		}
	}
}

func TestSyncRepoClassVersionWritesNothingWhenCurrent(t *testing.T) {
	t.Parallel()

	srv, puts, _ := classVersionServer(t, []map[string]any{
		{"identifier": "rcv-1", "version": "0.1", "default_configuration": map[string]any{"a": 1.0, "b": []any{"x"}}},
	})
	defer srv.Close()

	updated, err := New(srv.URL).SyncRepoClassVersion(context.Background(), "c", "0.1",
		map[string]any{"default_configuration": map[string]any{"b": []any{"x"}, "a": 1}})
	if err != nil {
		t.Fatal(err)
	}
	if updated || len(*puts) != 0 {
		t.Errorf("an unchanged default was written: updated=%v puts=%v", updated, *puts)
	}
}

// A row served with its blobs still encoded compares by content, not by its
// encoding.
func TestSyncRepoClassVersionReadsAnEncodedRow(t *testing.T) {
	t.Parallel()

	encoded, _ := EncodeCollection(map[string]any{"a": 1})
	srv, puts, _ := classVersionServer(t, []map[string]any{
		{"identifier": "rcv-1", "version": "0.1", "default_configuration": encoded},
	})
	defer srv.Close()

	updated, err := New(srv.URL).SyncRepoClassVersion(context.Background(), "c", "0.1",
		map[string]any{"default_configuration": map[string]any{"a": 1}})
	if err != nil {
		t.Fatal(err)
	}
	if updated || len(*puts) != 0 {
		t.Errorf("an encoded but equal default was rewritten: %v", *puts)
	}
}

// q is a substring filter, so 0.1 matches 0.10 too; only the exact version is
// ever the one updated.
func TestSyncRepoClassVersionMatchesTheVersionExactly(t *testing.T) {
	t.Parallel()

	srv, puts, paths := classVersionServer(t, []map[string]any{
		{"identifier": "rcv-10", "version": "0.10", "default_configuration": map[string]any{"a": 1}},
		{"identifier": "rcv-1", "version": "0.1", "default_configuration": map[string]any{"a": 1}},
	})
	defer srv.Close()

	if _, err := New(srv.URL).SyncRepoClassVersion(context.Background(), "c", "0.1",
		map[string]any{"default_configuration": map[string]any{"a": 2}}); err != nil {
		t.Fatal(err)
	}
	if len(*puts) != 1 || (*puts)[0]["identifier"] != "rcv-1" {
		t.Errorf("want rcv-1 updated alone, got %v", *puts)
	}
	// Asked for one page without the per-version dependency walk.
	if !strings.Contains((*paths)[0], "include_deps=false") {
		t.Errorf("lookup %q resolves every version's dependencies", (*paths)[0])
	}
}

func TestSyncRepoClassVersionWithNoSuchVersionIsAnError(t *testing.T) {
	t.Parallel()

	srv, _, _ := classVersionServer(t, nil)
	defer srv.Close()

	if _, err := New(srv.URL).SyncRepoClassVersion(context.Background(), "c", "0.1",
		map[string]any{"default_configuration": map[string]any{"a": 1}}); err == nil {
		t.Fatal("a version the catalog does not have was reported as synced")
	}
}
