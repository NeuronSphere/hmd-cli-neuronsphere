package cmd

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/artifact"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/oci/ocitest"
)

// NERD017 SPEC010: reuse if present, provide if absent.

// observabilityManifest is a stack with one companion (otel) and one required
// dependency role, `sink`, filled by an hmd-inf-s3bucket that produces
// storage.neuronsphere.io/bucket. The gate carries a suggestion.
const observabilityManifest = `{
  "name": "hmd-stack-observability",
  "deploy": {
    "commands": [["exec", "true"]],
    "dependencies": {
      "sink": {"repo_class_name": "hmd-inf-s3bucket", "required": "true", "version_spec": "0.1.13",
               "resource": {"resource_namespace": "storage.neuronsphere.io", "resource_definition_name": "bucket", "version": "0.1.0"}}
    }
  },
  "local": {"version": 1, "default_profiles": [],
    "dependencies": {"sink": {"suggest": "warehouse", "instance_configuration": {"bucket_name": "otel-sink"}}},
    "repos": [
      {"instance_name": "otel", "repo_class_name": "hmd-inf-otel", "version_spec": "0.1.5",
       "dependencies": {"sink": "sink"}}
    ]
  }
}`

// producingZip is a class tree that declares it produces a resource type.
func producingZip(t *testing.T, class, version, ns, name string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for file, body := range map[string]string{
		"meta-data/manifest.json":               `{"name": "` + class + `"}`,
		"meta-data/VERSION":                     version,
		"meta-data/resources/" + name + ".yaml": "resource_namespace: " + ns + "\nresource_definition_name: " + name + "\nversion: 0.1.0\nproduces: true\n",
	} {
		w, err := zw.Create(file)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(body))
	}
	_ = zw.Close()
	return buf.Bytes()
}

// observabilityStack publishes the fixture with its companions' zips and
// returns the reference.
func observabilityStack(t *testing.T, reg *ocitest.Registry) string {
	t.Helper()
	repoDir := t.TempDir()
	writeFile(t, filepath.Join(repoDir, "meta-data", "manifest.json"), observabilityManifest)
	writeFile(t, filepath.Join(repoDir, "meta-data", "VERSION"), "0.1.0")
	if _, _, err := run(t, fakeEnv(nil), "lock", repoDir); err != nil {
		t.Fatalf("lock: %v", err)
	}
	artifactsDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(artifactsDir, "hmd-inf-otel_0.1.5_build.zip"), artifactZip(t, "hmd-inf-otel", "0.1.5", "x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifactsDir, "hmd-inf-s3bucket_0.1.13_build.zip"),
		producingZip(t, "hmd-inf-s3bucket", "0.1.13", "storage.neuronsphere.io", "bucket"), 0o644); err != nil {
		t.Fatal(err)
	}
	return publishStack(t, reg, repoDir, artifactsDir, "observability")
}

// declareArtifact declares an artifact-sourced instance in the default
// environment by hand, as a previous stack or `repo add` would have.
func declareArtifact(t *testing.T, home string, env map[string]string, name, class, version string) {
	t.Helper()
	m, err := manifest.Load(home, "local", fakeEnv(env))
	if err != nil {
		t.Fatal(err)
	}
	if m == nil {
		m = &manifest.Manifest{Version: manifest.Version, Name: "local", Path: manifest.DefaultPath(home, "local")}
	}
	m.Repos = append(m.Repos, manifest.Repo{InstanceName: name, RepoClassName: class, Version: version,
		Source: &manifest.Source{Type: manifest.SourceArtifact}})
	if err := m.Save(m.Path); err != nil {
		t.Fatal(err)
	}
}

func TestStackAddDeclaresADependencyNobodyProvides(t *testing.T) {
	t.Parallel()
	reg := ocitest.New(t)
	ref := observabilityStack(t, reg)
	home, env := fromRepoEnv(t)
	out, _, err := run(t, fakeEnv(env), "stack", "add", ref, "--env", "local", "--local-url", "http://127.0.0.1:1")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	m := loadEnv(t, home, "local")
	sink, ok := m.Repo("sink")
	if !ok || sink.RepoClassName != "hmd-inf-s3bucket" || sink.Version != "0.1.13" {
		t.Errorf("sink declared from the lock: %+v, %v", sink, ok)
	}
	if cfg, _ := sink.InstanceConfiguration["bucket_name"].(string); cfg != "otel-sink" {
		t.Errorf("gate configuration not applied: %v", sink.InstanceConfiguration)
	}
	otel, _ := m.Repo("otel")
	if otel.Dependencies["sink"] != "sink" {
		t.Errorf("otel wired to %v", otel.Dependencies)
	}
	if strings.Contains(out, "Shared with") {
		t.Errorf("nothing was shared: %q", out)
	}
}

func TestStackAddBindsToAnExistingProducerByResource(t *testing.T) {
	t.Parallel()
	reg := ocitest.New(t)
	ref := observabilityStack(t, reg)
	home, env := fromRepoEnv(t)

	// The environment already runs a bucket under another name, from a class
	// tree in the cache that declares it produces the resource type.
	if _, err := artifact.Store(home, "hmd-inf-s3bucket", "0.1.11",
		producingZip(t, "hmd-inf-s3bucket", "0.1.11", "storage.neuronsphere.io", "bucket")); err != nil {
		t.Fatal(err)
	}
	declareArtifact(t, home, env, "warehouse-bucket", "hmd-inf-s3bucket", "0.1.11")

	out, _, err := run(t, fakeEnv(env), "stack", "add", ref, "--env", "local", "--local-url", "http://127.0.0.1:1")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "shared: already in the environment") || !strings.Contains(out, "Shared with the environment: sink -> warehouse-bucket") {
		t.Errorf("out = %q", out)
	}
	m := loadEnv(t, home, "local")
	if _, ok := m.Repo("sink"); ok {
		t.Error("a provided role must not be declared")
	}
	otel, _ := m.Repo("otel")
	if otel.Dependencies["sink"] != "warehouse-bucket" {
		t.Errorf("otel must be wired to the provider: %v", otel.Dependencies)
	}
	subject, _ := m.Repo("stack-observability")
	if subject.Dependencies["sink"] != "warehouse-bucket" {
		t.Errorf("the stack itself must bind the role: %v", subject.Dependencies)
	}
	rec, _, _ := m.Stack("observability")
	if rec.Bindings["sink"] != "warehouse-bucket" {
		t.Errorf("record bindings = %v", rec.Bindings)
	}
	// Removing the stack leaves the provider, which is not the stack's.
	if _, _, err := run(t, fakeEnv(env), "stack", "remove", "observability", "--env", "local"); err != nil {
		t.Fatal(err)
	}
	m = loadEnv(t, home, "local")
	if _, ok := m.Repo("warehouse-bucket"); !ok {
		t.Error("remove took an instance the stack only bound")
	}
}

func TestStackAddRefusesAnUnsatisfiedRoleNamingTheSuggestion(t *testing.T) {
	t.Parallel()
	reg := ocitest.New(t)
	// A stack whose role is optional-but-active and unpinned: gate it on a
	// profile with no companion behind it, so the lock has nothing to pin.
	repoDir := t.TempDir()
	writeFile(t, filepath.Join(repoDir, "meta-data", "manifest.json"), `{
  "name": "hmd-stack-airflow",
  "deploy": {"commands": [["exec", "true"]], "dependencies": {
    "query": {"repo_class_name": "hmd-inf-trino", "required": "false",
              "resource": {"resource_namespace": "trino.neuronsphere.io", "resource_definition_name": "cluster", "version": "0.1.0"}}
  }},
  "local": {"version": 1, "default_profiles": ["query"],
    "dependencies": {"query": {"profiles": ["query"], "suggest": "warehouse"}},
    "repos": [{"instance_name": "airflow", "repo_class_name": "hmd-app-airflow", "version_spec": "0.4.344"}]
  }
}`)
	writeFile(t, filepath.Join(repoDir, "meta-data", "VERSION"), "0.1.0")
	// Pin only airflow: trino is the environment's to provide, so the lock
	// carries nothing for it.
	writeFile(t, filepath.Join(repoDir, "neuronsphere.lock"), `version = 1
repo_class_name = "hmd-stack-airflow"
generated_from = "pins"

[[resolved]]
repo_class_name = "hmd-app-airflow"
version = "0.4.344"
profiles = []
content_path = "repository:/hmd-app-airflow/0.4.344/hmd-app-airflow_0.4.344_build.zip"
`)
	artifactsDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(artifactsDir, "hmd-app-airflow_0.4.344_build.zip"), artifactZip(t, "hmd-app-airflow", "0.4.344", "x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ref := publishStack(t, reg, repoDir, artifactsDir, "airflow")

	_, env := fromRepoEnv(t)
	_, _, err := run(t, fakeEnv(env), "stack", "add", ref, "--env", "local", "--local-url", "http://127.0.0.1:1")
	if nserr.CodeOf(err) != nserr.Usage {
		t.Fatalf("err = %v", err)
	}
	for _, want := range []string{"trino.neuronsphere.io/cluster", "nsctl stack add warehouse", "--name query=<instance>"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message lacks %q:\n%v", want, err)
		}
	}
	// --lean drops the gated role, and the stack adds fine.
	if _, _, err := run(t, fakeEnv(env), "stack", "add", ref, "--env", "local", "--local-url", "http://127.0.0.1:1", "--lean"); err != nil {
		t.Errorf("--lean: %v", err)
	}
}

func TestStackAddSharesACompanionAlreadyDeclaredAndRefusesAClassClash(t *testing.T) {
	t.Parallel()
	reg := ocitest.New(t)
	ref := observabilityStack(t, reg)
	home, env := fromRepoEnv(t)
	declareArtifact(t, home, env, "otel", "hmd-inf-otel", "0.1.4")
	out, _, err := run(t, fakeEnv(env), "stack", "add", ref, "--env", "local", "--local-url", "http://127.0.0.1:1")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "Shared with the environment: otel -> otel") {
		t.Errorf("out = %q", out)
	}
	m := loadEnv(t, home, "local")
	otel, _ := m.Repo("otel")
	if otel.Version != "0.1.4" {
		t.Errorf("a shared companion must keep the environment's version, got %s", otel.Version)
	}

	// The same name as a different class is a refusal, not a silent replace.
	home2, env2 := fromRepoEnv(t)
	declareArtifact(t, home2, env2, "otel", "hmd-inf-redis", "0.1.42")
	_, _, err = run(t, fakeEnv(env2), "stack", "add", ref, "--env", "local", "--local-url", "http://127.0.0.1:1")
	if nserr.CodeOf(err) != nserr.Usage || !strings.Contains(err.Error(), "--name otel=<instance>") {
		t.Errorf("clash: %v", err)
	}
}
