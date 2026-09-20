package msdeploy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// graphServer serves each entity type from a fixture, so the join is tested
// against the shapes the real service returns.
func graphServer(t *testing.T, rows map[string][]map[string]any) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entity := strings.TrimPrefix(r.URL.Path, "/api/hmd_lang_deployment.")
		w.Header().Set("Content-Type", "application/json")
		body := rows[entity]
		if body == nil {
			body = []map[string]any{}
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL)
}

func b64(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(data)
}

// A full environment: two instances, one with a dependency, both deployed.
func fullGraph(t *testing.T) map[string][]map[string]any {
	t.Helper()
	return map[string][]map[string]any{
		// The environment carries its slug as `type`, not `name`.
		"environment": {{"identifier": "env1", "type": "local"}},
		"environment_has_repo_instance": {
			{"ref_from": "env1", "ref_to": "i-app"},
			{"ref_from": "env1", "ref_to": "i-db"},
			// Another environment's instance, which must not be picked up.
			{"ref_from": "env2", "ref_to": "i-other"},
		},
		"repo_instance": {
			{"identifier": "i-app", "name": "app"},
			{"identifier": "i-db", "name": "db"},
			{"identifier": "i-other", "name": "elsewhere"},
		},
		"repo_class": {
			{"identifier": "c-app", "repo_class_name": "hmd-ms-app"},
			{"identifier": "c-db", "repo_class_name": "hmd-postgres-rds"},
		},
		"repo_instance_isa_repo_class": {
			{"ref_from": "i-app", "ref_to": "c-app"},
			{"ref_from": "i-db", "ref_to": "c-db"},
		},
		"repo_instance_req_repo_instance": {
			{"ref_from": "i-app", "ref_to": "i-db", "role": "database"},
			{"ref_from": "i-other", "ref_to": "i-db", "role": "database"},
		},
		"repo_instance_deployment": {
			{"identifier": "d-app", "status": "DEPLOYED", "deployment_id": "local",
				"instance_configuration": b64(t, map[string]any{"replicas": 2})},
			{"identifier": "d-db", "status": "DEPLOYED", "deployment_id": "local"},
		},
		"repo_instance_has_repo_instance_deployment": {
			{"ref_from": "i-app", "ref_to": "d-app", "_created": "2026-01-01T00:00:00Z"},
			{"ref_from": "i-db", "ref_to": "d-db", "_created": "2026-01-01T00:00:00Z"},
		},
		"repo_class_version": {
			{"identifier": "v-app", "version": "1.2.3"},
			{"identifier": "v-db", "version": "0.8"},
		},
		"repo_instance_deployment_has_repo_class_version": {
			{"ref_from": "d-app", "ref_to": "v-app"},
			{"ref_from": "d-db", "ref_to": "v-db"},
		},
	}
}

func TestEnvironmentInstancesJoinsTheGraph(t *testing.T) {
	t.Parallel()

	got, err := graphServer(t, fullGraph(t)).EnvironmentInstances(context.Background(), "local")
	if err != nil {
		t.Fatalf("EnvironmentInstances: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d instances, want 2: %+v", len(got), got)
	}
	// Sorted by name, so this is app then db.
	app, db := got[0], got[1]

	if app.Name != "app" || app.RepoClassName != "hmd-ms-app" || app.RepoClassVersion != "1.2.3" {
		t.Errorf("app = %+v", app)
	}
	if app.Status != StatusDeployed {
		t.Errorf("app status = %q", app.Status)
	}
	if app.InstanceConfiguration["replicas"] != float64(2) {
		t.Errorf("app configuration = %v", app.InstanceConfiguration)
	}
	if !reflect.DeepEqual(app.Dependencies["database"], []string{"db"}) {
		t.Errorf("app dependencies = %v", app.Dependencies)
	}
	if db.Name != "db" || db.RepoClassVersion != "0.8" {
		t.Errorf("db = %+v", db)
	}
}

// Membership comes from environment_has_repo_instance, so another
// environment's instance is not imported into this one's manifest.
func TestEnvironmentInstancesExcludesOtherEnvironments(t *testing.T) {
	t.Parallel()

	got, err := graphServer(t, fullGraph(t)).EnvironmentInstances(context.Background(), "local")
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range got {
		if i.Name == "elsewhere" {
			t.Error("an instance belonging to another environment was returned")
		}
	}
}

func TestEnvironmentInstancesOnAnUnknownEnvironment(t *testing.T) {
	t.Parallel()

	_, err := graphServer(t, fullGraph(t)).EnvironmentInstances(context.Background(), "nope")
	if err == nil {
		t.Fatal("an unknown environment resolved")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("error does not name the environment: %v", err)
	}
}

// The most recent deployment wins, matching InstanceStatus.
func TestEnvironmentInstancesTakesTheNewestDeployment(t *testing.T) {
	t.Parallel()

	rows := fullGraph(t)
	rows["repo_instance_deployment"] = append(rows["repo_instance_deployment"],
		map[string]any{"identifier": "d-app-old", "status": "FAILED", "deployment_id": "local"})
	rows["repo_instance_has_repo_instance_deployment"] = []map[string]any{
		{"ref_from": "i-app", "ref_to": "d-app-old", "_created": "2025-01-01T00:00:00Z"},
		{"ref_from": "i-app", "ref_to": "d-app", "_created": "2026-01-01T00:00:00Z"},
		{"ref_from": "i-db", "ref_to": "d-db", "_created": "2026-01-01T00:00:00Z"},
	}

	got, err := graphServer(t, rows).EnvironmentInstances(context.Background(), "local")
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Status != StatusDeployed {
		t.Errorf("status = %q, want the newest deployment's", got[0].Status)
	}
}

// An instance with no class cannot be deployed from anything, and the caller
// needs to be able to tell rather than write an undeployable declaration.
func TestEnvironmentInstancesReportsAMissingClass(t *testing.T) {
	t.Parallel()

	rows := fullGraph(t)
	rows["repo_instance_isa_repo_class"] = []map[string]any{{"ref_from": "i-db", "ref_to": "c-db"}}

	got, err := graphServer(t, rows).EnvironmentInstances(context.Background(), "local")
	if err != nil {
		t.Fatal(err)
	}
	if got[0].RepoClassName != "" {
		t.Errorf("app class = %q, want empty", got[0].RepoClassName)
	}
}

func TestDecodeConfiguration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value any
		want  map[string]any
	}{
		{"base64", b64(t, map[string]any{"k": "v"}), map[string]any{"k": "v"}},
		{"plain json", `{"k":"v"}`, map[string]any{"k": "v"}},
		{"already decoded", map[string]any{"k": "v"}, map[string]any{"k": "v"}},
		{"empty", "", nil},
		{"absent", nil, nil},
		// Unreadable is reported as absent rather than failing a whole import.
		{"garbage", "!!!not base64 or json", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := decodeConfiguration(tt.value); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("decodeConfiguration(%v) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}
