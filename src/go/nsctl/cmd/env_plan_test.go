package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bom"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/environment"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/reconcile"
)

func samplePlanResult() *environment.PlanResult {
	return &environment.PlanResult{
		EnvSlug: "local",
		Reconcile: &reconcile.Plan{
			Add:       []bom.Entry{{RepoInstanceName: "airflow", RepoClassName: "hmd-app-airflow"}},
			Change:    []bom.Entry{{RepoInstanceName: "environment-db", RepoClassName: "hmd-postgres-rds"}},
			Unchanged: []string{"local-neuronsphere"},
			Remove:    []string{"orphaned-instance"},
		},
		Validation: &msdeploy.ValidateResult{
			Valid: false,
			Errors: []msdeploy.ValidateIssue{
				{Type: "missing_required_role", Instance: "airflow", Message: "required role, database, not supplied"},
			},
		},
		Warnings: []bom.CandidateWarning{
			{Instance: "airflow-db-account", Role: "database", Target: "unrelated-instance",
				Message: "unrelated-instance does not produce database.neuronsphere.io/postgres"},
		},
	}
}

func TestRenderPlanTextNamesEveryChange(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	renderPlanText(&buf, samplePlanResult())
	out := buf.String()

	for _, want := range []string{
		"local", "airflow", "environment-db", "orphaned-instance",
		"FAILED", "missing_required_role", "required role, database, not supplied",
		"Candidate warnings", "airflow-db-account", "unrelated-instance",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q:\n%s", want, out)
		}
	}
}

// This is what Demo 2 pastes directly into a PR body, so it has to read as a
// complete, self-contained review artifact.
func TestRenderPlanMDIsAPasteableReviewArtifact(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	renderPlanMD(&buf, samplePlanResult())
	out := buf.String()

	for _, want := range []string{
		"## Plan: local",
		"**Add:**", "`airflow`",
		"**Change:**", "`environment-db`",
		"**Deployed but no longer declared**", "`orphaned-instance`",
		"### Validation",
		"missing_required_role",
		"### Candidate warnings",
		"airflow-db-account", "unrelated-instance",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("md output missing %q:\n%s", want, out)
		}
	}
}

// A validation with no errors reports a plain green check, with no empty
// "Candidate warnings" section to confuse a reviewer.
func TestRenderPlanMDIsQuietWhenThereIsNothingToFlag(t *testing.T) {
	t.Parallel()

	result := &environment.PlanResult{
		EnvSlug:    "local",
		Reconcile:  &reconcile.Plan{Add: []bom.Entry{{RepoInstanceName: "airflow", RepoClassName: "hmd-app-airflow"}}},
		Validation: &msdeploy.ValidateResult{Valid: true},
	}
	var buf bytes.Buffer
	renderPlanMD(&buf, result)
	out := buf.String()

	if !strings.Contains(out, "accepts this definition") {
		t.Errorf("a clean validation should say so plainly:\n%s", out)
	}
	if strings.Contains(out, "### Candidate warnings") {
		t.Errorf("no warnings should mean no warnings section:\n%s", out)
	}
}

func TestRenderPlanJSONRoundTrips(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	if err := renderPlanJSON(&buf, samplePlanResult()); err != nil {
		t.Fatal(err)
	}

	var doc planJSON
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	if doc.Environment != "local" {
		t.Errorf("environment = %q", doc.Environment)
	}
	if len(doc.Add) != 1 || doc.Add[0].Instance != "airflow" {
		t.Errorf("add = %+v", doc.Add)
	}
	if len(doc.Change) != 1 || doc.Change[0].Instance != "environment-db" {
		t.Errorf("change = %+v", doc.Change)
	}
	if doc.Validation == nil || doc.Validation.Valid {
		t.Fatalf("validation = %+v, want present and invalid", doc.Validation)
	}
	if len(doc.Validation.Errors) != 1 {
		t.Errorf("validation errors = %+v", doc.Validation.Errors)
	}
	if len(doc.Warnings) != 1 || doc.Warnings[0].Target != "unrelated-instance" {
		t.Errorf("candidate_warnings = %+v", doc.Warnings)
	}
}

// An environment with nothing to deploy validates nothing -- the JSON must
// omit the key entirely rather than emit a misleading empty object.
func TestRenderPlanJSONOmitsValidationWhenThereIsNoneToReport(t *testing.T) {
	t.Parallel()

	result := &environment.PlanResult{EnvSlug: "local", Reconcile: reconcile.Degraded(nil)}
	var buf bytes.Buffer
	if err := renderPlanJSON(&buf, result); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), `"validation"`) {
		t.Errorf("validation key present with nothing to validate:\n%s", buf.String())
	}
}

// fakeMSDeploymentServer answers every request ComputePlan's full run makes
// (InstanceStatus's three searches, RegisterCatalog's writes,
// CandidateWarnings' suggestion lookup) the same way
// internal/bom's registerCatalogServer does, plus a validate_changeset case
// this test controls -- so `env plan` reaches an actual Validation result
// instead of stopping early on an unreachable service or an empty reconcile.
func fakeMSDeploymentServer(t *testing.T, valid bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/apiop/validate_changeset"):
			if valid {
				_, _ = w.Write([]byte(`{"valid": true}`))
			} else {
				_, _ = w.Write([]byte(`{"valid": false, "errors": [` +
					`{"type":"missing_required_role","instance":"airflow","message":"required role, database, not supplied"}` +
					`]}`))
			}
		case strings.HasPrefix(r.URL.Path, "/apiop/find_repo_class_versions"):
			_, _ = w.Write([]byte(`[{"identifier":"rcv-1","version":"0.1"}]`))
		case strings.HasPrefix(r.URL.Path, "/apiop/register_deployed_instance"):
			_, _ = w.Write([]byte(`{"repo_instance_id":"ri-1","repo_instance_deployment_id":"rid-1"}`))
		case strings.HasPrefix(r.URL.Path, "/api/") && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`[]`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// This is the N1 regression: `env plan --output json` on an invalid plan must
// exit nonzero, exactly as --output text/md already do, not just print
// "valid": false and exit 0.
func TestEnvPlanJSONExitsNonzeroWhenValidationFails(t *testing.T) {
	t.Parallel()

	srv := fakeMSDeploymentServer(t, false)
	home := registryHome(t, twoEnvRegistry)
	env := fakeEnv(map[string]string{"HMD_HOME": home, "HMD_LOCAL_MS_DEPLOYMENT_URL": srv.URL})

	out, _, err := run(t, env, "env", "plan", "local", "--output", "json")

	if err == nil {
		t.Fatal("an invalid plan printed as JSON did not produce an error")
	}
	if !strings.Contains(err.Error(), "validate_changeset rejects this definition") {
		t.Errorf("unexpected error: %v", err)
	}
	if !strings.Contains(out, `"valid": false`) {
		t.Errorf("json output missing the invalid validation result:\n%s", out)
	}
}

// The companion case: a valid plan must still print JSON and exit 0, so the
// regression test above can't be satisfied by simply always failing.
func TestEnvPlanJSONSucceedsWhenValidationPasses(t *testing.T) {
	t.Parallel()

	srv := fakeMSDeploymentServer(t, true)
	home := registryHome(t, twoEnvRegistry)
	env := fakeEnv(map[string]string{"HMD_HOME": home, "HMD_LOCAL_MS_DEPLOYMENT_URL": srv.URL})

	out, _, err := run(t, env, "env", "plan", "local", "--output", "json")

	if err != nil {
		t.Fatalf("a valid plan produced an error: %v", err)
	}
	if !strings.Contains(out, `"valid": true`) {
		t.Errorf("json output missing the valid validation result:\n%s", out)
	}
}

func TestEnvPlanRejectsAnUnknownOutputFormat(t *testing.T) {
	t.Parallel()

	home := registryHome(t, twoEnvRegistry)
	_, _, err := run(t, fakeEnv(map[string]string{"HMD_HOME": home}), "env", "plan", "local", "--output", "yaml")
	if err == nil {
		t.Fatal("an unknown --output value was accepted")
	}
	if !strings.Contains(err.Error(), "text, json, md") {
		t.Errorf("the error does not name the valid values: %v", err)
	}
}
