package localspec

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// writeRepo lays out a repository with a manifest and the given bundle files.
func writeRepo(t *testing.T, manifest string, bundles map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "meta-data", "bundles"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, body := range bundles {
		if err := os.WriteFile(filepath.Join(dir, "meta-data", "bundles", name+".json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const analyticsBundle = `{
  "bundle_name": "analytics",
  "roles": {
    "compute": {"repo_class_name": "hmd-inf-eks-node-group", "required": "true", "provision": "false"},
    "graph-db": {
      "required": "true", "provision": "false",
      "resource": {"resource_namespace": "graph.neuronsphere.io", "resource_definition_name": "gremlin-endpoint"}
    },
    "ext-secrets": {"repo_class_name": "hmd-inf-ext-secrets", "required": "true", "provision": "false"},
    "airflow-db": {
      "repo_class_name": "hmd-database-account", "required": "true", "version_spec": "~= 0.1",
      "instance_configuration": {"db_name": "airflow"}
    },
    "airflow": {
      "repo_class_name": "hmd-app-airflow", "required": "true", "version_spec": "~= 0.4",
      "bind": {"compute": "compute", "db-credentials": "airflow-db"},
      "instance_configuration": {"replicaCount": 1}
    },
    "superset": {
      "repo_class_name": "hmd-app-superset", "required": "false", "instance_name": "bi",
      "bind": {"compute": "compute"}
    },
    "trino": {"repo_class_name": "hmd-inf-trino", "required": "false"}
  },
  "local": {
    "default_profiles": ["superset"],
    "roles": {
      "compute": {"bind": "local-neuronsphere"},
      "ext-secrets": {"bind": "ext-secrets"},
      "superset": {"profiles": ["superset", "bi"]}
    }
  }
}`

func TestLoadReadsTheOnlyBundleFileWhenTheManifestHasNoLocalSection(t *testing.T) {
	dir := writeRepo(t, `{"name": "hmd-stack-analytics"}`, map[string]string{"analytics": analyticsBundle})

	m, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.Bundle != "analytics" {
		t.Errorf("Bundle = %q, want analytics", m.Bundle)
	}
	if m.RepoClassName != "hmd-stack-analytics" {
		t.Errorf("RepoClassName = %q", m.RepoClassName)
	}
	if !m.Local.Stack {
		t.Error("a repository shipping a bundle is a stack: it names classes, it is not one")
	}
	if !reflect.DeepEqual(m.Local.DefaultProfiles, []string{"superset"}) {
		t.Errorf("DefaultProfiles = %v", m.Local.DefaultProfiles)
	}
	if len(m.Notices) != 0 {
		t.Errorf("Notices = %v, want none", m.Notices)
	}
}

func TestReuseOnlyRolesBecomeDependenciesBoundOrExternal(t *testing.T) {
	dir := writeRepo(t, `{"name": "hmd-stack-analytics"}`, map[string]string{"analytics": analyticsBundle})
	m, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}

	var roles []string
	for _, d := range m.Dependencies {
		roles = append(roles, d.Role)
		if !d.Required {
			t.Errorf("%s: want required", d.Role)
		}
	}
	if want := []string{"compute", "ext-secrets", "graph-db"}; !reflect.DeepEqual(roles, want) {
		t.Fatalf("dependency roles = %v, want %v", roles, want)
	}
	if g := m.Local.Dependencies["compute"]; g.Bind != "local-neuronsphere" || g.External {
		t.Errorf("compute gate = %+v, want bound to local-neuronsphere", g)
	}
	if g := m.Local.Dependencies["graph-db"]; !g.External || g.Bind != "" {
		t.Errorf("graph-db gate = %+v: a reuse-only role nothing binds is the environment's to fill", g)
	}
	graph, _ := find(m.Dependencies, "graph-db")
	if graph.Resource != "graph.neuronsphere.io/gremlin-endpoint" {
		t.Errorf("graph-db resource = %q", graph.Resource)
	}
}

func TestProvisionedRolesBecomeCompanionsWithBindsResolvedToInstances(t *testing.T) {
	dir := writeRepo(t, `{"name": "hmd-stack-analytics"}`, map[string]string{"analytics": analyticsBundle})
	m, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	repos := map[string]Repo{}
	for _, r := range m.Local.Repos {
		repos[r.InstanceName] = r
	}

	airflow := repos["airflow"]
	if airflow.RepoClassName != "hmd-app-airflow" || airflow.VersionSpec != "~= 0.4" {
		t.Errorf("airflow = %+v", airflow)
	}
	// compute is reuse-only and bound locally; airflow-db is provisioned.
	wantDeps := map[string]any{"compute": "local-neuronsphere", "db-credentials": "airflow-db"}
	if !reflect.DeepEqual(airflow.Dependencies, wantDeps) {
		t.Errorf("airflow dependencies = %v, want %v", airflow.Dependencies, wantDeps)
	}
	if airflow.Profiles != nil {
		t.Errorf("a required role is unconditional, got profiles %v", airflow.Profiles)
	}
	if airflow.InstanceConfiguration["replicaCount"] != float64(1) {
		t.Errorf("airflow configuration = %v", airflow.InstanceConfiguration)
	}

	// instance_name renames the companion; its profiles come from the local block.
	bi, ok := repos["bi"]
	if !ok {
		t.Fatalf("no companion named bi in %v", repos)
	}
	if !reflect.DeepEqual(bi.Profiles, []string{"superset", "bi"}) {
		t.Errorf("bi profiles = %v", bi.Profiles)
	}
	// An optional role the local block does not gate is gated by its own name,
	// so it stays off until asked for -- the bundle's own meaning of optional.
	if !reflect.DeepEqual(repos["trino"].Profiles, []string{"trino"}) {
		t.Errorf("trino profiles = %v, want [trino]", repos["trino"].Profiles)
	}
}

func TestActivatingTheDefaultProfilesStartsRequiredRolesAndTheGatedOnes(t *testing.T) {
	dir := writeRepo(t, `{"name": "hmd-stack-analytics"}`, map[string]string{"analytics": analyticsBundle})
	m, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, w := range m.Activate(m.Profiles(nil, false)) {
		keys = append(keys, w.Key)
	}
	want := []string{"compute", "ext-secrets", "graph-db", "airflow", "airflow-db", "bi"}
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("activated = %v, want %v", keys, want)
	}
}

func TestALocalSectionWinsAndAStackFlagIsReportedDeprecated(t *testing.T) {
	dir := writeRepo(t,
		`{"name": "hmd-stack-x", "local": {"stack": true, "repos": [{"instance_name": "a", "repo_class_name": "hmd-a"}]}}`,
		map[string]string{"analytics": analyticsBundle})
	m, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.Bundle != "" || len(m.Local.Repos) != 1 {
		t.Fatalf("the local section should have been read, got bundle %q, repos %v", m.Bundle, m.Local.Repos)
	}
	if len(m.Notices) != 1 || !strings.Contains(m.Notices[0], "meta-data/bundles") {
		t.Errorf("Notices = %v, want one deprecation pointing at meta-data/bundles", m.Notices)
	}
}

func TestSeveralBundleFilesNeedAName(t *testing.T) {
	other := strings.Replace(analyticsBundle, `"bundle_name": "analytics"`, `"bundle_name": "lean"`, 1)
	dir := writeRepo(t, `{"name": "hmd-bundle-core"}`, map[string]string{"analytics": analyticsBundle, "lean": other})

	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "analytics, lean") {
		t.Fatalf("Load error = %v, want one naming both bundles", err)
	}
	m, err := LoadBundle(dir, "lean")
	if err != nil {
		t.Fatal(err)
	}
	if m.Bundle != "lean" {
		t.Errorf("Bundle = %q", m.Bundle)
	}
}

func TestABundleFileProblemIsReportedWithTheFile(t *testing.T) {
	dir := writeRepo(t, `{"name": "hmd-stack-x"}`, map[string]string{
		"broken": `{"bundle_name": "broken", "roles": {"x": {"required": "true"},
		  "y": {"repo_class_name": "hmd-y", "bind": {"compute": "nowhere"}}}}`,
	})
	_, err := Load(dir)
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"broken.json", "roles.x", "roles.y.bind.compute"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestARepositoryWithNoLocalSectionAndNoBundleIsUnchanged(t *testing.T) {
	dir := writeRepo(t, `{"name": "hmd-ms-x", "deploy": {"dependencies": {"vpc": {"repo_class_name": "hmd-vpc", "required": "true"}}}}`, nil)
	m, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.Bundle != "" || m.Local.Stack || len(m.Dependencies) != 1 {
		t.Errorf("got %+v", m)
	}
}

// NERD0019 SPEC0007: a local.stack repository and its migrated bundle file
// plan identical environments.

const stackManifest = `{
  "name": "hmd-stack-analytics",
  "deploy": {"dependencies": {
    "compute": {"repo_class_name": "hmd-cli-neuronsphere", "required": "true"},
    "database-instance": {"repo_class_name": "hmd-postgres-rds", "required": "true"},
    "graph-db": {"repo_class_name": "hmd-inf-neptune", "required": "true",
      "resource": {"resource_namespace": "graph.neuronsphere.io", "resource_definition_name": "gremlin-endpoint"}}
  }},
  "local": {
    "stack": true,
    "default_profiles": ["airflow"],
    "dependencies": {
      "compute": {"bind": "local-neuronsphere"},
      "database-instance": {"bind": "environment-db"},
      "graph-db": {"bind": "global-graph"}
    },
    "repos": [
      {"instance_name": "airflow", "repo_class_name": "hmd-app-airflow", "version_spec": "== 0.4.352", "profiles": ["airflow"],
       "dependencies": {"compute": "local-neuronsphere", "db-credentials": "airflow-db-account", "neptune": "global-graph"},
       "instance_configuration": {"replicaCount": 1}},
      {"instance_name": "airflow-db-account", "repo_class_name": "hmd-database-account", "version_spec": "== 0.1.3",
       "dependencies": {"create-service": "local-neuronsphere", "database-instance": "environment-db"}},
      {"instance_name": "trino", "repo_class_name": "hmd-inf-trino", "profiles": ["trino"]},
      {"instance_name": "secret-key", "repo_class_name": "hmd-inf-superset-secret-key"}
    ]
  }
}`

func TestABundleMigratedFromALocalSectionPlansTheSameWants(t *testing.T) {
	before, err := Parse([]byte(stackManifest))
	if err != nil {
		t.Fatal(err)
	}
	data, err := ToBundle(before, "analytics")
	if err != nil {
		t.Fatal(err)
	}
	after, err := ParseBundle(data, "analytics")
	if err != nil {
		t.Fatalf("%v\n%s", err, data)
	}
	if !reflect.DeepEqual(before.Local.DefaultProfiles, after.Local.DefaultProfiles) {
		t.Errorf("default profiles %v != %v", before.Local.DefaultProfiles, after.Local.DefaultProfiles)
	}
	for _, profiles := range [][]string{nil, {"airflow"}, {"trino"}, before.AllProfiles()} {
		// Keyed, because order is not part of a plan: the deploy graph orders
		// instances, and a bundle's roles are a JSON object.
		b, a := byKey(before.Activate(profiles)), byKey(after.Activate(profiles))
		if !reflect.DeepEqual(b, a) {
			t.Errorf("profiles %v:\n before %+v\n after  %+v\nbundle:\n%s", profiles, b, a, data)
		}
	}
}

func TestMigrationRefusesADependencyOnAnInstanceNoRoleFills(t *testing.T) {
	m, err := Parse([]byte(`{"name": "hmd-stack-x", "local": {"stack": true, "repos": [
	  {"instance_name": "a", "repo_class_name": "hmd-a", "dependencies": {"vpc": "somebody-elses-vpc"}}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ToBundle(m, "x"); err == nil || !strings.Contains(err.Error(), "somebody-elses-vpc") {
		t.Errorf("err = %v, want one naming the instance", err)
	}
}

func byKey(wants []Want) map[string]Want {
	out := map[string]Want{}
	for _, w := range wants {
		out[w.Key] = w
	}
	return out
}
