package cmd

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/artifact"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/localspec"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/lock"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// NERD019 SPEC004 and SPEC007.

// analyticsBOM models the running platform: substrate, a storage stack's
// bucket, a warehouse (metastore + trino), superset from a working tree,
// airflow, and an unrelated transform.
func analyticsBOM() []msdeploy.BOMEntry {
	return []msdeploy.BOMEntry{
		{RepoInstanceName: "local-neuronsphere", RepoClassName: "hmd-cli-neuronsphere", RepoClassVersion: "1.0"},
		{RepoInstanceName: "environment-db", RepoClassName: "hmd-inf-rds", RepoClassVersion: "0.2"},
		{RepoInstanceName: "bucket", RepoClassName: "hmd-inf-s3bucket", RepoClassVersion: "0.1.13"},
		{RepoInstanceName: "metastore", RepoClassName: "hmd-inf-hive-metastore", RepoClassVersion: "0.4.2",
			Dependencies: map[string]any{"warehouse-bucket": "bucket", "database": "environment-db"}},
		{RepoInstanceName: "trino", RepoClassName: "hmd-inf-trino", RepoClassVersion: "0.3.12",
			Dependencies: map[string]any{"metastore": "metastore", "compute": "local-neuronsphere"}},
		{RepoInstanceName: "superset", RepoClassName: "hmd-inf-superset", RepoClassVersion: "0.5.1",
			Dependencies: map[string]any{"query": "trino"}},
		{RepoInstanceName: "airflow", RepoClassName: "hmd-app-airflow", RepoClassVersion: "0.4.344",
			Dependencies: map[string]any{"query": "trino", "compute": "local-neuronsphere"}},
		{RepoInstanceName: "transform", RepoClassName: "hmd-ms-transform", RepoClassVersion: "1.0.899"},
	}
}

// analyticsHome is a registry home whose default environment declares the
// BOM's instances: bucket from a "storage" stack, superset from a working
// tree at supersetTree, the rest as artifacts already in the cache.
func analyticsHome(t *testing.T) (home string, env map[string]string, bomFile, supersetTree string) {
	t.Helper()
	home, env = fromRepoEnv(t)
	supersetTree = t.TempDir()
	writeFile(t, filepath.Join(supersetTree, "meta-data", "manifest.json"), `{"name": "hmd-inf-superset"}`)
	writeFile(t, filepath.Join(supersetTree, "meta-data", "VERSION"), "0.5")
	m := &manifest.Manifest{Version: manifest.Version, Name: "local", Path: manifest.DefaultPath(home, "local"),
		Repos: []manifest.Repo{
			{InstanceName: "bucket", RepoClassName: "hmd-inf-s3bucket", Version: "0.1.13", Source: &manifest.Source{Type: manifest.SourceArtifact}},
			{InstanceName: "metastore", RepoClassName: "hmd-inf-hive-metastore", Version: "0.4.2", Source: &manifest.Source{Type: manifest.SourceArtifact},
				InstanceConfiguration: map[string]any{"warehouse_dir": "/Users/me/warehouse"}},
			{InstanceName: "trino", RepoClassName: "hmd-inf-trino", Version: "0.3.12", Source: &manifest.Source{Type: manifest.SourceArtifact}},
			{InstanceName: "superset", RepoClassName: "hmd-inf-superset", Source: &manifest.Source{Type: manifest.SourceLocal, Path: supersetTree}},
			{InstanceName: "airflow", RepoClassName: "hmd-app-airflow", Version: "0.4.344", Source: &manifest.Source{Type: manifest.SourceArtifact}},
		},
		Stacks: []manifest.StackRecord{{Name: "storage", Version: "0.1.0", Ref: "oci://ghcr.io/hmdlabs/stacks/storage", Declared: []string{"bucket"}}},
	}
	if err := m.Save(m.Path); err != nil {
		t.Fatal(err)
	}
	for _, cv := range [][2]string{{"hmd-inf-hive-metastore", "0.4.2"}, {"hmd-inf-trino", "0.3.12"}, {"hmd-app-airflow", "0.4.344"}} {
		if _, err := artifact.Store(home, cv[0], cv[1], artifactZip(t, cv[0], cv[1], "deployed")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := artifact.Store(home, "hmd-inf-s3bucket", "0.1.13",
		producingZip(t, "hmd-inf-s3bucket", "0.1.13", "storage.neuronsphere.io", "bucket")); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(analyticsBOM())
	bomFile = filepath.Join(t.TempDir(), "bom.json")
	if err := os.WriteFile(bomFile, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return home, env, bomFile, supersetTree
}

func TestStackInitScaffoldsARepoClassWithItsWorkflow(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "hmd-stack-x")
	out, _, err := run(t, fakeEnv(nil), "stack", "init", "hmd-stack-x", "--path", dir, "--description", "An x")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, f := range []string{"meta-data/manifest.json", "meta-data/VERSION", ".github/workflows/stack.yml"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s not written: %v", f, err)
		}
	}
	spec, err := localspec.Load(dir)
	if err != nil {
		t.Fatalf("scaffold does not parse: %v", err)
	}
	if spec.RepoClassName != "hmd-stack-x" || len(spec.Wants()) != 0 {
		t.Errorf("spec = %+v", spec)
	}
	wf, _ := os.ReadFile(filepath.Join(dir, ".github", "workflows", "stack.yml"))
	for _, want := range []string{"stacks/x", "nsctl repoclass validate", "nsctl stack build", "--from build/stack --bump", "secrets.GITHUB_TOKEN", "packages: write", "nsctl lock --resolve", "setup-nsctl"} {
		if !strings.Contains(string(wf), want) {
			t.Errorf("workflow lacks %q", want)
		}
	}
	_, _, err = run(t, fakeEnv(nil), "stack", "init", "hmd-stack-x", "--path", dir)
	if nserr.CodeOf(err) != nserr.Usage || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("second init: %v", err)
	}
	_, _, err = run(t, fakeEnv(nil), "stack", "init", "Bad_Name", "--path", t.TempDir())
	if nserr.CodeOf(err) != nserr.Usage {
		t.Errorf("bad name: %v", err)
	}
}

func TestStackInitFromBOMDerivesBuildsAndRefreshes(t *testing.T) {
	t.Parallel()
	home, env, bomFile, _ := analyticsHome(t)
	dir := filepath.Join(t.TempDir(), "hmd-stack-analytics")

	// Dry run: the table, and nothing on disk.
	out, errOut, err := run(t, fakeEnv(env), "stack", "init", "hmd-stack-analytics", "--path", dir,
		"--from-bom", bomFile, "--env", "local", "--select", "trino,airflow", "--dry-run")
	if err != nil {
		t.Fatalf("%v\n%s%s", err, out, errOut)
	}
	for _, want := range []string{
		"trino", "companion (profile trino)",
		"metastore", "hmd-inf-hive-metastore", "companion",
		"bucket", "external role (stack storage)",
		"local-neuronsphere", "bound role",
		"Dry run: nothing written.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("table lacks %q:\n%s", want, out)
		}
	}
	if !strings.Contains(errOut, "review: metastore copies instance_configuration warehouse_dir") {
		t.Errorf("host-specific review missing: %q", errOut)
	}
	if strings.Contains(out, "transform") || strings.Contains(out, "superset") {
		t.Errorf("unreached instances in the table:\n%s", out)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("--dry-run wrote something")
	}

	// For real: manifest, lock, workflow.
	out, _, err = run(t, fakeEnv(env), "stack", "init", "hmd-stack-analytics", "--path", dir,
		"--from-bom", bomFile, "--env", "local", "--select", "trino,airflow")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	spec, err := localspec.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	wants := map[string]localspec.Want{}
	for _, w := range spec.Wants() {
		wants[w.Key] = w
	}
	if wants["trino"].VersionSpec != "== 0.3.12" || wants["metastore"].VersionSpec != "== 0.4.2" {
		t.Errorf("companions = %+v", wants)
	}
	if w := wants["warehouse-bucket"]; !w.External || w.Suggest != "oci://ghcr.io/hmdlabs/stacks/storage" || w.Resource != "storage.neuronsphere.io/bucket" {
		t.Errorf("cross-stack role = %+v", w)
	}
	if wants["compute"].Bind != "local-neuronsphere" || wants["database"].Bind != "environment-db" {
		t.Errorf("substrate roles = %+v / %+v", wants["compute"], wants["database"])
	}
	l, err := lock.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	pinned := map[string]string{}
	for _, e := range l.Resolved {
		pinned[e.RepoClassName] = e.Version
	}
	if pinned["hmd-inf-trino"] != "0.3.12" || pinned["hmd-inf-hive-metastore"] != "0.4.2" || pinned["hmd-app-airflow"] != "0.4.344" {
		t.Errorf("lock pins = %v", pinned)
	}
	if _, ok := pinned["hmd-inf-s3bucket"]; ok {
		t.Error("an external role must not be pinned")
	}
	if e, _ := l.Entry("hmd-inf-trino"); e.Digest == "" {
		t.Error("cached companions should carry digests")
	}

	// The derived stack builds offline from the cache.
	out, _, err = run(t, fakeEnv(env), "stack", "build", dir)
	if err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	if strings.Count(out, "<- artifact cache") != 3 {
		t.Errorf("expected three companions from the cache:\n%s", out)
	}

	// --diff agrees with itself, disagrees after the environment moves, and
	// --update brings it back.
	if _, _, err := run(t, fakeEnv(env), "stack", "init", "hmd-stack-analytics", "--path", dir,
		"--from-bom", bomFile, "--env", "local", "--select", "trino,airflow", "--diff"); err != nil {
		t.Errorf("--diff on an unchanged environment: %v", err)
	}
	moved := analyticsBOM()
	for i := range moved {
		if moved[i].RepoInstanceName == "trino" {
			moved[i].RepoClassVersion = "0.3.13"
		}
	}
	data, _ := json.Marshal(moved)
	if err := os.WriteFile(bomFile, data, 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err = run(t, fakeEnv(env), "stack", "init", "hmd-stack-analytics", "--path", dir,
		"--from-bom", bomFile, "--env", "local", "--select", "trino,airflow", "--diff")
	if nserr.CodeOf(err) != nserr.Fail || !strings.Contains(err.Error(), "0.3.13") {
		t.Errorf("--diff after a move: %v", err)
	}
	if _, err := artifact.Store(home, "hmd-inf-trino", "0.3.13", artifactZip(t, "hmd-inf-trino", "0.3.13", "moved")); err != nil {
		t.Fatal(err)
	}
	out, _, err = run(t, fakeEnv(env), "stack", "init", "hmd-stack-analytics", "--path", dir,
		"--from-bom", bomFile, "--env", "local", "--select", "trino,airflow", "--update")
	if err != nil || !strings.Contains(out, "Rewrote the local section") {
		t.Errorf("--update: %q, %v", out, err)
	}
	spec, _ = localspec.Load(dir)
	for _, w := range spec.Wants() {
		if w.Key == "trino" && w.VersionSpec != "== 0.3.13" {
			t.Errorf("trino after update = %s", w.VersionSpec)
		}
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, "meta-data", "manifest.json")); !strings.Contains(string(raw), `"description"`) {
		t.Error("--update dropped keys it should preserve")
	}
	out, _, err = run(t, fakeEnv(env), "stack", "init", "hmd-stack-analytics", "--path", dir,
		"--from-bom", bomFile, "--env", "local", "--select", "trino,airflow", "--update")
	if err != nil || !strings.Contains(out, "already up to date") {
		t.Errorf("idempotent --update: %q, %v", out, err)
	}
}

func TestStackInitRefusesAWorkingTreeUnlessBundled(t *testing.T) {
	t.Parallel()
	home, env, bomFile, _ := analyticsHome(t)
	dir := filepath.Join(t.TempDir(), "hmd-stack-viz")
	_, _, err := run(t, fakeEnv(env), "stack", "init", "hmd-stack-viz", "--path", dir,
		"--from-bom", bomFile, "--env", "local", "--select", "superset")
	if nserr.CodeOf(err) != nserr.Usage || !strings.Contains(err.Error(), "--bundle-local superset") {
		t.Fatalf("err = %v", err)
	}
	out, _, err := run(t, fakeEnv(env), "stack", "init", "hmd-stack-viz", "--path", dir,
		"--from-bom", bomFile, "--env", "local", "--select", "superset", "--bundle-local", "superset")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "Bundled superset (hmd-inf-superset@0.5.1)") || !artifact.Cached(home, "hmd-inf-superset", "0.5.1") {
		t.Errorf("working tree not bundled: %q", out)
	}
	if _, _, err := run(t, fakeEnv(env), "stack", "build", dir); err != nil {
		t.Errorf("build with the bundled tree: %v", err)
	}
}

func TestStackInitIncludeProvidedBundlesTheOtherStacksInstance(t *testing.T) {
	t.Parallel()
	_, env, bomFile, _ := analyticsHome(t)
	dir := filepath.Join(t.TempDir(), "hmd-stack-wh")
	out, _, err := run(t, fakeEnv(env), "stack", "init", "hmd-stack-wh", "--path", dir,
		"--from-bom", bomFile, "--env", "local", "--select", "trino", "--include-provided", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "external role") {
		t.Errorf("--include-provided must bundle the bucket:\n%s", out)
	}
}

// graphFixture serves the entity rows EnvironmentInstances joins, for one
// environment with metastore -> environment-db.
func graphFixture(t *testing.T) string {
	t.Helper()
	b64 := func(v any) string {
		data, _ := json.Marshal(v)
		return base64.StdEncoding.EncodeToString(data)
	}
	rows := map[string][]map[string]any{
		"environment":                     {{"identifier": "env1", "type": "local"}},
		"environment_has_repo_instance":   {{"ref_from": "env1", "ref_to": "i-ms"}, {"ref_from": "env1", "ref_to": "i-db"}},
		"repo_instance":                   {{"identifier": "i-ms", "name": "metastore"}, {"identifier": "i-db", "name": "environment-db"}},
		"repo_class":                      {{"identifier": "c-ms", "repo_class_name": "hmd-inf-hive-metastore"}, {"identifier": "c-db", "repo_class_name": "hmd-inf-rds"}},
		"repo_instance_isa_repo_class":    {{"ref_from": "i-ms", "ref_to": "c-ms"}, {"ref_from": "i-db", "ref_to": "c-db"}},
		"repo_instance_req_repo_instance": {{"ref_from": "i-ms", "ref_to": "i-db", "role": "database"}},
		"repo_instance_deployment": {
			{"identifier": "d-ms", "status": "DEPLOYED", "deployment_id": "local", "instance_configuration": b64(map[string]any{"db_name": "hive"})},
			{"identifier": "d-db", "status": "DEPLOYED", "deployment_id": "local"},
		},
		"repo_instance_has_repo_instance_deployment": {
			{"ref_from": "i-ms", "ref_to": "d-ms", "_created": "2026-01-01T00:00:00Z"},
			{"ref_from": "i-db", "ref_to": "d-db", "_created": "2026-01-01T00:00:00Z"},
		},
		"repo_class_version":                              {{"identifier": "v-ms", "version": "0.4.2"}, {"identifier": "v-db", "version": "0.2"}},
		"repo_instance_deployment_has_repo_class_version": {{"ref_from": "d-ms", "ref_to": "v-ms"}, {"ref_from": "d-db", "ref_to": "v-db"}},
	}
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
	return srv.URL
}

func TestStackInitFromEnvWritesTheReferenceBOM(t *testing.T) {
	t.Parallel()
	home, env := fromRepoEnv(t)
	env["HMD_LOCAL_MS_DEPLOYMENT_URL"] = graphFixture(t)
	if _, err := artifact.Store(home, "hmd-inf-hive-metastore", "0.4.2", artifactZip(t, "hmd-inf-hive-metastore", "0.4.2", "x")); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "hmd-stack-ms")
	out, _, err := run(t, fakeEnv(env), "stack", "init", "hmd-stack-ms", "--path", dir, "--from-env", "local", "--select", "metastore")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	ref, err := os.ReadFile(filepath.Join(dir, "meta-data", "reference-bom.json"))
	if err != nil {
		t.Fatal(err)
	}
	var entries []msdeploy.BOMEntry
	if err := json.Unmarshal(ref, &entries); err != nil || len(entries) != 2 {
		t.Fatalf("reference BOM = %s (%v)", ref, err)
	}
	if !strings.Contains(out, "environment-db") || !strings.Contains(out, "bound role") {
		t.Errorf("out = %q", out)
	}
	// The reference BOM re-derives to the same thing.
	if _, _, err := run(t, fakeEnv(env), "stack", "init", "hmd-stack-ms", "--path", dir,
		"--from-bom", filepath.Join(dir, "meta-data", "reference-bom.json"), "--select", "metastore", "--diff"); err != nil {
		t.Errorf("--diff against the written reference BOM: %v", err)
	}
}
