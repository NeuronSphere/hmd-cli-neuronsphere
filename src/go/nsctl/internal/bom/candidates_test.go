package bom

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/repoclass"
)

// candidateServer answers find_repo_class_versions with a fixed id, and
// suggest_resource_dependencies with the given per-role suggestions --
// whichever repo class or environment is asked, since these tests only ever
// exercise one class at a time.
func candidateServer(t *testing.T, suggestions string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/apiop/find_repo_class_versions/"):
			_, _ = w.Write([]byte(`[{"identifier":"rcv-1","version":"0.1"}]`))
		case strings.HasPrefix(r.URL.Path, "/apiop/suggest_resource_dependencies/"):
			_, _ = w.Write([]byte(suggestions))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
}

const oneResourceTypedRole = `{
	"database": {
		"resource_definition": {"resource_namespace":"database.neuronsphere.io","resource_definition_name":"postgres","version":"0.1"},
		"version_spec": "", "tag_selector": "", "required": true,
		"suggested_repo_class_name": "hmd-postgres-rds",
		"candidates": [{"name":"environment-db","identifier":"rid-1"}]
	}
}`

func TestCandidateWarningsSilentWhenTargetIsACandidate(t *testing.T) {
	t.Parallel()

	srv := candidateServer(t, oneResourceTypedRole)
	defer srv.Close()

	s := &Seeder{Client: msdeploy.New(srv.URL)}
	toCheck := []Entry{{
		RepoInstanceName: "airflow-db-account", RepoClassName: "hmd-database-account", RepoClassVersion: "0.1",
		Dependencies: map[string]any{"database": "environment-db"},
	}}
	warnings, err := s.CandidateWarnings(context.Background(), "local", toCheck, toCheck)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %+v, want none: the bound instance is a listed candidate", warnings)
	}
}

func TestCandidateWarningsSilentWhenTargetMatchesTheSuggestedRepoClass(t *testing.T) {
	t.Parallel()

	srv := candidateServer(t, oneResourceTypedRole)
	defer srv.Close()

	s := &Seeder{Client: msdeploy.New(srv.URL)}
	consumer := Entry{
		RepoInstanceName: "airflow-db-account", RepoClassName: "hmd-database-account", RepoClassVersion: "0.1",
		Dependencies: map[string]any{"database": "some-other-postgres"},
	}
	// Not a listed candidate, but known to be an instance of the role's
	// suggested RepoClass -- the same fallback environment_information.py
	// grants at apply time.
	known := []Entry{consumer, {RepoInstanceName: "some-other-postgres", RepoClassName: "hmd-postgres-rds"}}

	warnings, err := s.CandidateWarnings(context.Background(), "local", []Entry{consumer}, known)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %+v, want none: the RepoClass fallback should have matched", warnings)
	}
}

func TestCandidateWarningsWarnsWhenNeitherCheckPasses(t *testing.T) {
	t.Parallel()

	srv := candidateServer(t, oneResourceTypedRole)
	defer srv.Close()

	s := &Seeder{Client: msdeploy.New(srv.URL)}
	consumer := Entry{
		RepoInstanceName: "airflow-db-account", RepoClassName: "hmd-database-account", RepoClassVersion: "0.1",
		Dependencies: map[string]any{"database": "unrelated-instance"},
	}
	known := []Entry{consumer, {RepoInstanceName: "unrelated-instance", RepoClassName: "hmd-something-else"}}

	warnings, err := s.CandidateWarnings(context.Background(), "local", []Entry{consumer}, known)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %+v, want exactly one", warnings)
	}
	w := warnings[0]
	if w.Instance != "airflow-db-account" || w.Role != "database" || w.Target != "unrelated-instance" {
		t.Errorf("warning = %+v", w)
	}
	if !strings.Contains(w.Message, "database.neuronsphere.io/postgres") || !strings.Contains(w.Message, "hmd-postgres-rds") {
		t.Errorf("message = %q, want it to name the required type and the suggested RepoClass", w.Message)
	}
}

// A role validate_changeset would happily accept because it is a plain
// repo_class dependency -- never resource-typed -- never appears in
// suggest_resource_dependencies's result, so it must never be checked or
// warned about.
func TestCandidateWarningsSkipsRolesWithNoResourceRequirement(t *testing.T) {
	t.Parallel()

	srv := candidateServer(t, `{}`)
	defer srv.Close()

	s := &Seeder{Client: msdeploy.New(srv.URL)}
	toCheck := []Entry{{
		RepoInstanceName: "airflow", RepoClassName: "hmd-app-airflow", RepoClassVersion: "0.1",
		Dependencies: map[string]any{"broker": "some-broker"},
	}}
	warnings, err := s.CandidateWarnings(context.Background(), "local", toCheck, toCheck)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %+v, want none: no resource-typed role was returned", warnings)
	}
}

// A substrate instance bound in the very plan being previewed has no
// concrete Resource yet -- apply.go submits the substrate's Resources only
// after its own ChangeSet has actually deployed, which a dry run never does.
// Warning here would flag every substrate-typed role on a first apply.
func TestCandidateWarningsSkipsASubstrateTargetBeingDeployedInThisPlan(t *testing.T) {
	t.Parallel()

	srv := candidateServer(t, oneResourceTypedRole)
	defer srv.Close()

	s := &Seeder{Client: msdeploy.New(srv.URL)}
	core := Entry{RepoInstanceName: CoreInstanceName, RepoClassName: CoreRepoClass}
	consumer := Entry{
		RepoInstanceName: "airflow-db-account", RepoClassName: "hmd-database-account", RepoClassVersion: "0.1",
		// Bound to the core instance, which oneResourceTypedRole's fixture
		// does not list as a candidate for "database" and has no suggested
		// RepoClass matching hmd-cli-neuronsphere -- exactly the shape a real
		// substrate-produced role warning would otherwise take.
		Dependencies: map[string]any{"database": CoreInstanceName},
	}
	toCheck := []Entry{core, consumer}

	warnings, err := s.CandidateWarnings(context.Background(), "local", toCheck, toCheck)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %+v, want none: the substrate target is being deployed in this same plan", warnings)
	}
}

// The same target is not exempt once it is NOT part of the plan being
// previewed -- an already-deployed substrate instance that genuinely fails
// the candidate check is a real finding, not noise from the two-phase dance.
func TestCandidateWarningsStillWarnsOnAnAlreadyDeployedSubstrateTarget(t *testing.T) {
	t.Parallel()

	srv := candidateServer(t, oneResourceTypedRole)
	defer srv.Close()

	s := &Seeder{Client: msdeploy.New(srv.URL)}
	consumer := Entry{
		RepoInstanceName: "airflow-db-account", RepoClassName: "hmd-database-account", RepoClassVersion: "0.1",
		Dependencies: map[string]any{"database": CoreInstanceName},
	}
	known := []Entry{consumer, {RepoInstanceName: CoreInstanceName, RepoClassName: CoreRepoClass}}

	// consumer is the only thing being deployed; the core instance is
	// already up (not in toCheck), so its candidacy is a real question.
	warnings, err := s.CandidateWarnings(context.Background(), "local", []Entry{consumer}, known)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 {
		t.Errorf("warnings = %+v, want one: the core does not produce this role's type and no fallback matches", warnings)
	}
}

// An entry with no dependencies at all must not call the service.
func TestCandidateWarningsSkipsEntriesWithNoDependencies(t *testing.T) {
	t.Parallel()

	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	s := &Seeder{Client: msdeploy.New(srv.URL)}
	toCheck := []Entry{{RepoInstanceName: "local-neuronsphere", RepoClassName: "hmd-cli-neuronsphere"}}
	warnings, err := s.CandidateWarnings(context.Background(), "local", toCheck, toCheck)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %+v, want none", warnings)
	}
	if called {
		t.Error("the service was called for an entry with no dependencies")
	}
}

// A non-substrate producer that is new in the very plan being previewed is
// never a suggest_resource_dependencies candidate -- the service only
// enumerates RepoInstances that already exist with a deployment -- yet
// apply_changeset validates it fine, because it creates RepoInstanceDeployments
// in changeset order and checks the consumer against the producer's declared
// produces. The dry run has to mirror that from the producer's own
// meta-data/resources declaration, or every same-plan producer is a false
// positive (the live run's `airflow role=trino -> trino` warning).
func TestCandidateWarningsSkipsASamePlanProducerThatDeclaresTheType(t *testing.T) {
	t.Parallel()

	srv := candidateServer(t, oneResourceTypedRole)
	defer srv.Close()

	s := &Seeder{Client: msdeploy.New(srv.URL), Versions: &fakeVersions{produces: map[string][]repoclass.ResourceDeclaration{
		"hmd-inf-trino": {{Namespace: "database.neuronsphere.io", Name: "postgres", Version: "0.1", Produces: true}},
	}}}
	producer := Entry{RepoInstanceName: "trino", RepoClassName: "hmd-inf-trino", RepoClassVersion: "0.1"}
	consumer := Entry{
		RepoInstanceName: "airflow", RepoClassName: "hmd-app-airflow", RepoClassVersion: "0.1",
		Dependencies: map[string]any{"database": "trino"},
	}
	toCheck := []Entry{producer, consumer}

	warnings, err := s.CandidateWarnings(context.Background(), "local", toCheck, toCheck)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %+v, want none: the same-plan producer declares the required type", warnings)
	}
}

// The parent a declaration names counts too: RegisterCatalog declares both
// the produced type and its parent (FromDeclarations), so a requirement for
// the parent resolves at apply time exactly as one for the type itself.
func TestCandidateWarningsSamePlanProducerClearsARequirementForItsParentType(t *testing.T) {
	t.Parallel()

	srv := candidateServer(t, oneResourceTypedRole)
	defer srv.Close()

	s := &Seeder{Client: msdeploy.New(srv.URL), Versions: &fakeVersions{produces: map[string][]repoclass.ResourceDeclaration{
		"acme-platform-warehouse": {{
			Namespace: "acme.com", Name: "postgres-warehouse", Version: "0.1.0", Produces: true,
			Parent: &repoclass.ResourceRef{Namespace: "database.neuronsphere.io", Name: "postgres", Version: "0.1"},
		}},
	}}}
	producer := Entry{RepoInstanceName: "warehouse", RepoClassName: "acme-platform-warehouse", RepoClassVersion: "0.1"}
	consumer := Entry{
		RepoInstanceName: "retention", RepoClassName: "acme-dp-retention", RepoClassVersion: "0.1",
		Dependencies: map[string]any{"database": "warehouse"},
	}
	toCheck := []Entry{producer, consumer}

	warnings, err := s.CandidateWarnings(context.Background(), "local", toCheck, toCheck)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %+v, want none: the required type is the parent of what the producer declares", warnings)
	}
}

// A same-plan producer of some *other* type is the genuine finding this check
// exists for: apply_changeset will reject the binding, so the dry run must say
// so.
func TestCandidateWarningsStillWarnsOnASamePlanProducerOfADifferentType(t *testing.T) {
	t.Parallel()

	srv := candidateServer(t, oneResourceTypedRole)
	defer srv.Close()

	s := &Seeder{Client: msdeploy.New(srv.URL), Versions: &fakeVersions{produces: map[string][]repoclass.ResourceDeclaration{
		"e5acc-producer": {{Namespace: "e5acc.example.com", Name: "widget", Version: "0.1.0", Produces: true}},
	}}}
	producer := Entry{RepoInstanceName: "e5acc-producer", RepoClassName: "e5acc-producer", RepoClassVersion: "0.1"}
	consumer := Entry{
		RepoInstanceName: "e5acc-consumer-wrong", RepoClassName: "e5acc-consumer-wrong", RepoClassVersion: "0.1",
		Dependencies: map[string]any{"database": "e5acc-producer"},
	}
	toCheck := []Entry{producer, consumer}

	warnings, err := s.CandidateWarnings(context.Background(), "local", toCheck, toCheck)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %+v, want exactly one", warnings)
	}
	if w := warnings[0]; w.Instance != "e5acc-consumer-wrong" || w.Role != "database" || w.Target != "e5acc-producer" {
		t.Errorf("warning = %+v", w)
	}
}

// Declared produces only stand in for a producer the service cannot see yet.
// An already-deployed target is exactly what suggest_resource_dependencies
// enumerates, so if it is not a candidate its deployed version does not
// produce the type -- the declaration in a working tree says nothing about
// what is running, and apply_changeset would reject the binding.
func TestCandidateWarningsDoesNotClearAnAlreadyDeployedTargetByDeclarationAlone(t *testing.T) {
	t.Parallel()

	srv := candidateServer(t, oneResourceTypedRole)
	defer srv.Close()

	s := &Seeder{Client: msdeploy.New(srv.URL), Versions: &fakeVersions{produces: map[string][]repoclass.ResourceDeclaration{
		"hmd-inf-trino": {{Namespace: "database.neuronsphere.io", Name: "postgres", Version: "0.1", Produces: true}},
	}}}
	consumer := Entry{
		RepoInstanceName: "airflow", RepoClassName: "hmd-app-airflow", RepoClassVersion: "0.1",
		Dependencies: map[string]any{"database": "trino"},
	}
	known := []Entry{consumer, {RepoInstanceName: "trino", RepoClassName: "hmd-inf-trino"}}

	warnings, err := s.CandidateWarnings(context.Background(), "local", []Entry{consumer}, known)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 {
		t.Errorf("warnings = %+v, want one: the target is already deployed and not a candidate", warnings)
	}
}
