package bom

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/repoclass"
)

// roleJSON builds one resource-typed role as suggest_resource_dependencies
// answers it, for the given definition, requiredness, tag selector and
// candidate instance names.
func roleJSON(role, ns, name, required, tagSelector string, candidates ...string) string {
	cs := ""
	for i, c := range candidates {
		if i > 0 {
			cs += ","
		}
		cs += `{"name":"` + c + `","identifier":"rid-` + c + `"}`
	}
	return `{"` + role + `": {
		"resource_definition": {"resource_namespace":"` + ns + `","resource_definition_name":"` + name + `","version":"0.1.0"},
		"version_spec": "~= 0.1", "tag_selector": "` + tagSelector + `", "required": ` + required + `,
		"suggested_repo_class_name": "",
		"candidates": [` + cs + `]}}`
}

// roleWithClass is roleJSON for a role whose manifest dependency also names a
// repo_class_name, which the service returns as suggested_repo_class_name.
func roleWithClass(class, role, ns, name, required, tagSelector string, candidates ...string) string {
	return strings.Replace(roleJSON(role, ns, name, required, tagSelector, candidates...),
		`"suggested_repo_class_name": ""`, `"suggested_repo_class_name": "`+class+`"`, 1)
}

func consumer(deps map[string]any) Entry {
	return Entry{
		RepoInstanceName: "ms-deployment", RepoClassName: "hmd-ms-deployment", RepoClassVersion: "0.4",
		Dependencies: deps,
	}
}

func TestBindSuggestedBindsAnUnboundRequiredRoleFromTheOnlyCandidate(t *testing.T) {
	t.Parallel()

	srv := candidateServer(t, roleJSON("workers", "compute.neuronsphere.io", "compute-node", "true", "", "local-neuronsphere"))
	defer srv.Close()

	s := &Seeder{Client: msdeploy.New(srv.URL)}
	toBind := []Entry{consumer(map[string]any{})}
	bound, ambiguous, err := s.BindSuggested(context.Background(), "relpub", toBind, toBind)
	if err != nil {
		t.Fatal(err)
	}
	if got := toBind[0].Dependencies["workers"]; got != "local-neuronsphere" {
		t.Errorf("workers = %v, want local-neuronsphere", got)
	}
	want := []Binding{{Instance: "ms-deployment", Role: "workers", Target: "local-neuronsphere"}}
	if !reflect.DeepEqual(bound, want) {
		t.Errorf("bound = %+v, want %+v", bound, want)
	}
	if len(ambiguous) != 0 {
		t.Errorf("ambiguous = %+v, want none", ambiguous)
	}
}

// The service lists only instances that already have a deployment, so on a
// first apply a substrate producer is never a candidate. nsctl knows what the
// substrate produces, so it answers instead.
func TestBindSuggestedBindsToASubstrateProducerTheServiceCannotYetSee(t *testing.T) {
	t.Parallel()

	srv := candidateServer(t, roleJSON("workers", "compute.neuronsphere.io", "compute-node", "true", ""))
	defer srv.Close()

	s := &Seeder{Client: msdeploy.New(srv.URL), Versions: &fakeVersions{}}
	core := Entry{RepoInstanceName: CoreInstanceName, RepoClassName: CoreRepoClass}
	toBind := []Entry{consumer(nil)}
	known := []Entry{core, toBind[0]}
	if _, _, err := s.BindSuggested(context.Background(), "relpub", toBind, known); err != nil {
		t.Fatal(err)
	}
	if got := toBind[0].Dependencies["workers"]; got != CoreInstanceName {
		t.Errorf("workers = %v, want %s", got, CoreInstanceName)
	}
}

func TestBindSuggestedBindsToABaseVPCThatProducesTheNetworkType(t *testing.T) {
	t.Parallel()

	srv := candidateServer(t, roleJSON("base-vpc", "network.neuronsphere.io", "vpc", "true", ""))
	defer srv.Close()

	s := &Seeder{Client: msdeploy.New(srv.URL), Versions: &fakeVersions{}}
	vpc := Entry{RepoInstanceName: BaseVPCInstance, RepoClassName: BaseVPCRepoClass}
	toBind := []Entry{consumer(nil)}
	if _, _, err := s.BindSuggested(context.Background(), "relpub", toBind, []Entry{vpc, toBind[0]}); err != nil {
		t.Fatal(err)
	}
	if got := toBind[0].Dependencies["base-vpc"]; got != BaseVPCInstance {
		t.Errorf("base-vpc = %v, want %s", got, BaseVPCInstance)
	}
}

func TestBindSuggestedBindsToASamePlanProducerThatDeclaresTheType(t *testing.T) {
	t.Parallel()

	srv := candidateServer(t, roleJSON("database", "database.neuronsphere.io", "postgres", "true", ""))
	defer srv.Close()

	s := &Seeder{Client: msdeploy.New(srv.URL), Versions: &fakeVersions{produces: map[string][]repoclass.ResourceDeclaration{
		"hmd-inf-trino": {{Namespace: "database.neuronsphere.io", Name: "postgres", Version: "0.1.0", Produces: true}},
	}}}
	producer := Entry{RepoInstanceName: "trino", RepoClassName: "hmd-inf-trino"}
	toBind := []Entry{consumer(nil), producer}
	if _, _, err := s.BindSuggested(context.Background(), "relpub", toBind, toBind); err != nil {
		t.Fatal(err)
	}
	if got := toBind[0].Dependencies["database"]; got != "trino" {
		t.Errorf("database = %v, want trino", got)
	}
}

// Two fitting instances is a choice for the operator. Picking one would make
// the manifest mean something different the day a second producer appears.
func TestBindSuggestedLeavesAnAmbiguousRoleUnboundAndNamesTheOptions(t *testing.T) {
	t.Parallel()

	srv := candidateServer(t, roleJSON("workers", "compute.neuronsphere.io", "compute-node", "true", "", "node-b", "node-a"))
	defer srv.Close()

	s := &Seeder{Client: msdeploy.New(srv.URL)}
	toBind := []Entry{consumer(nil)}
	bound, ambiguous, err := s.BindSuggested(context.Background(), "relpub", toBind, toBind)
	if err != nil {
		t.Fatal(err)
	}
	if _, set := toBind[0].Dependencies["workers"]; set {
		t.Errorf("workers was bound to %v, want it left unbound", toBind[0].Dependencies["workers"])
	}
	if len(bound) != 0 {
		t.Errorf("bound = %+v, want none", bound)
	}
	if len(ambiguous) != 1 || ambiguous[0].Role != "workers" || !reflect.DeepEqual(ambiguous[0].Candidates, []string{"node-a", "node-b"}) {
		t.Errorf("ambiguous = %+v, want workers with [node-a node-b]", ambiguous)
	}
}

func TestBindSuggestedNeverChangesARoleTheManifestBinds(t *testing.T) {
	t.Parallel()

	srv := candidateServer(t, roleJSON("workers", "compute.neuronsphere.io", "compute-node", "true", "", "local-neuronsphere"))
	defer srv.Close()

	s := &Seeder{Client: msdeploy.New(srv.URL)}
	toBind := []Entry{consumer(map[string]any{"workers": "my-nodes"})}
	bound, ambiguous, err := s.BindSuggested(context.Background(), "relpub", toBind, toBind)
	if err != nil {
		t.Fatal(err)
	}
	if got := toBind[0].Dependencies["workers"]; got != "my-nodes" {
		t.Errorf("workers = %v, want the declared my-nodes", got)
	}
	if len(bound) != 0 || len(ambiguous) != 0 {
		t.Errorf("bound = %+v ambiguous = %+v, want neither", bound, ambiguous)
	}
}

// Binding an optional role would pull a substrate producer into every
// environment that has been working without one.
func TestBindSuggestedLeavesAnOptionalRoleAlone(t *testing.T) {
	t.Parallel()

	srv := candidateServer(t, roleJSON("workers", "compute.neuronsphere.io", "compute-node", "false", "", "local-neuronsphere"))
	defer srv.Close()

	s := &Seeder{Client: msdeploy.New(srv.URL)}
	toBind := []Entry{consumer(nil)}
	bound, _, err := s.BindSuggested(context.Background(), "relpub", toBind, toBind)
	if err != nil {
		t.Fatal(err)
	}
	if _, set := toBind[0].Dependencies["workers"]; set || len(bound) != 0 {
		t.Errorf("an optional role was bound: deps = %v bound = %+v", toBind[0].Dependencies, bound)
	}
}

// The service folds a role's tag selector into its candidates. nsctl cannot
// evaluate one against a static produces table, so it trusts only the service.
func TestBindSuggestedUsesOnlyTheServiceWhenTheRoleHasATagSelector(t *testing.T) {
	t.Parallel()

	srv := candidateServer(t, roleJSON("create-service", "application.neuronsphere.io", "microservice", "true", "repo_class=hmd-ms-dbaccount"))
	defer srv.Close()

	s := &Seeder{Client: msdeploy.New(srv.URL), Versions: &fakeVersions{}}
	core := Entry{RepoInstanceName: CoreInstanceName, RepoClassName: CoreRepoClass}
	toBind := []Entry{consumer(nil)}
	bound, _, err := s.BindSuggested(context.Background(), "relpub", toBind, []Entry{core, toBind[0]})
	if err != nil {
		t.Fatal(err)
	}
	if len(bound) != 0 {
		t.Errorf("bound = %+v, want none: the core instance produces microservice but the tag selector narrows it", bound)
	}
}

func TestBindSuggestedNeverBindsAnInstanceToItself(t *testing.T) {
	t.Parallel()

	srv := candidateServer(t, roleJSON("database", "database.neuronsphere.io", "postgres", "true", "", "ms-deployment"))
	defer srv.Close()

	s := &Seeder{Client: msdeploy.New(srv.URL)}
	toBind := []Entry{consumer(nil)}
	bound, ambiguous, err := s.BindSuggested(context.Background(), "relpub", toBind, toBind)
	if err != nil {
		t.Fatal(err)
	}
	if len(bound) != 0 || len(ambiguous) != 0 {
		t.Errorf("bound = %+v ambiguous = %+v, want neither", bound, ambiguous)
	}
}

// An unreachable service must not stop the plan: a role that cannot be bound
// stays as it was and validate_changeset reports it, which is what happened
// before this step existed.
func TestBindSuggestedToleratesAnUnreachableService(t *testing.T) {
	t.Parallel()

	s := &Seeder{Client: msdeploy.New("http://127.0.0.1:1")}
	toBind := []Entry{consumer(nil)}
	bound, ambiguous, err := s.BindSuggested(context.Background(), "relpub", toBind, toBind)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(bound) != 0 || len(ambiguous) != 0 {
		t.Errorf("bound = %+v ambiguous = %+v, want neither", bound, ambiguous)
	}
}

// Some roles have no resource definition that anything produces yet (logging,
// db-credentials). The manifest still names the repo class it expects, and an
// instance of that class in the environment is the answer.
func TestBindSuggestedBindsToTheOnlyInstanceOfTheSuggestedRepoClass(t *testing.T) {
	t.Parallel()

	srv := candidateServer(t, roleWithClass("hmd-inf-logging", "logging", "logging.neuronsphere.io", "logging", "true", ""))
	defer srv.Close()

	s := &Seeder{Client: msdeploy.New(srv.URL), Versions: &fakeVersions{}}
	logs := Entry{RepoInstanceName: "logs", RepoClassName: "hmd-inf-logging"}
	other := Entry{RepoInstanceName: "bucket", RepoClassName: "hmd-inf-s3bucket"}
	toBind := []Entry{consumer(nil), logs, other}
	bound, _, err := s.BindSuggested(context.Background(), "relpub", toBind, toBind)
	if err != nil {
		t.Fatal(err)
	}
	if got := toBind[0].Dependencies["logging"]; got != "logs" {
		t.Errorf("logging = %v, want logs; bound = %+v", got, bound)
	}
}

// A class match and a produced-type match that name the same instance are one
// candidate, not two.
func TestBindSuggestedCountsAnInstanceMatchedTwoWaysOnce(t *testing.T) {
	t.Parallel()

	srv := candidateServer(t, roleWithClass(BaseVPCRepoClass, "base-vpc", "network.neuronsphere.io", "vpc", "true", ""))
	defer srv.Close()

	s := &Seeder{Client: msdeploy.New(srv.URL), Versions: &fakeVersions{}}
	vpc := Entry{RepoInstanceName: BaseVPCInstance, RepoClassName: BaseVPCRepoClass}
	toBind := []Entry{consumer(nil)}
	bound, ambiguous, err := s.BindSuggested(context.Background(), "relpub", toBind, []Entry{vpc, toBind[0]})
	if err != nil {
		t.Fatal(err)
	}
	if len(bound) != 1 || bound[0].Target != BaseVPCInstance || len(ambiguous) != 0 {
		t.Errorf("bound = %+v ambiguous = %+v, want base-vpc bound once", bound, ambiguous)
	}
}

func TestBindSuggestedLeavesTwoInstancesOfTheSuggestedClassAmbiguous(t *testing.T) {
	t.Parallel()

	srv := candidateServer(t, roleWithClass("hmd-inf-s3bucket", "bucket", "storage.neuronsphere.io", "bucket", "true", ""))
	defer srv.Close()

	s := &Seeder{Client: msdeploy.New(srv.URL), Versions: &fakeVersions{}}
	toBind := []Entry{consumer(nil)}
	known := []Entry{
		toBind[0],
		{RepoInstanceName: "b2", RepoClassName: "hmd-inf-s3bucket"},
		{RepoInstanceName: "b1", RepoClassName: "hmd-inf-s3bucket"},
	}
	bound, ambiguous, err := s.BindSuggested(context.Background(), "relpub", toBind, known)
	if err != nil {
		t.Fatal(err)
	}
	if len(bound) != 0 || len(ambiguous) != 1 || !reflect.DeepEqual(ambiguous[0].Candidates, []string{"b1", "b2"}) {
		t.Errorf("bound = %+v ambiguous = %+v, want b1/b2 ambiguous", bound, ambiguous)
	}
}

// With a tag selector the service decides who qualifies; a class name alone
// must not override that.
func TestBindSuggestedIgnoresTheSuggestedClassWhenTheRoleHasATagSelector(t *testing.T) {
	t.Parallel()

	srv := candidateServer(t, roleWithClass("hmd-inf-logging", "logging", "logging.neuronsphere.io", "logging", "true", "env=prod"))
	defer srv.Close()

	s := &Seeder{Client: msdeploy.New(srv.URL), Versions: &fakeVersions{}}
	toBind := []Entry{consumer(nil), {RepoInstanceName: "logs", RepoClassName: "hmd-inf-logging"}}
	bound, _, err := s.BindSuggested(context.Background(), "relpub", toBind, toBind)
	if err != nil {
		t.Fatal(err)
	}
	if len(bound) != 0 {
		t.Errorf("bound = %+v, want none", bound)
	}
}
