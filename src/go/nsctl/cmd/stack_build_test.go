package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/artifact"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/localspec"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/lock"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/oci/ocitest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/stack"
)

// NERD019 SPEC001-SPEC003 and NERD016 SPEC009.

func TestStackBuildWritesADeterministicLayoutFromArtifacts(t *testing.T) {
	t.Parallel()
	repoDir, artifactsDir := stackRepo(t)
	home := t.TempDir()
	env := fakeEnv(map[string]string{"HMD_LOCAL_MS_DEPLOYMENT_URL": "http://127.0.0.1:1"})

	out, _, err := run(t, env, "--home", home, "stack", "build", repoDir, "--artifacts", artifactsDir)
	if err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	if !strings.Contains(out, "<- --artifacts") || !strings.Contains(out, "Built stack 0.1.0 (3 layer(s)") {
		t.Errorf("out = %q", out)
	}
	layout := filepath.Join(repoDir, "build", "stack")
	_, _, tag, err := stack.ReadLayout(layout)
	if err != nil || tag != "0.1.0" {
		t.Fatalf("layout: %v, tag %q", err, tag)
	}
	first, _ := os.ReadFile(filepath.Join(layout, "index.json"))
	if _, _, err := run(t, env, "--home", home, "stack", "build", repoDir, "--artifacts", artifactsDir); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(filepath.Join(layout, "index.json"))
	if string(first) != string(second) {
		t.Error("two builds from the same inputs differ")
	}
	// The subject zip never carries the layout: build/ is skipped.
	if _, _, err := run(t, env, "--home", home, "stack", "build", repoDir, "--artifacts", artifactsDir); err != nil {
		t.Fatal(err)
	}
}

func TestStackBuildTakesCompanionsFromTheCache(t *testing.T) {
	t.Parallel()
	repoDir, _ := stackRepo(t)
	home := t.TempDir()
	for _, cv := range [][2]string{{"hmd-inf-otel", "0.1.5"}, {"hmd-inf-clickhouse", "0.3.0"}} {
		if _, err := artifact.Store(home, cv[0], cv[1], artifactZip(t, cv[0], cv[1], "cached")); err != nil {
			t.Fatal(err)
		}
	}
	out, _, err := run(t, fakeEnv(nil), "--home", home, "stack", "build", repoDir)
	if err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	if strings.Count(out, "<- artifact cache") != 2 {
		t.Errorf("expected both companions from the cache: %q", out)
	}
}

func TestStackBuildTakesCompanionsFromAnOCISource(t *testing.T) {
	t.Parallel()
	reg := ocitest.New(t)
	repoDir, artifactsDir := stackRepo(t)
	home := t.TempDir()
	pub := fakeEnv(map[string]string{"HMD_REGISTRY_TOKEN": "pat"})

	// A third party publishes each companion as a class artifact...
	for _, cv := range [][2]string{{"hmd-inf-otel", "0.1.5"}, {"hmd-inf-clickhouse", "0.3.0"}} {
		zip := filepath.Join(artifactsDir, cv[0]+"_"+cv[1]+"_build.zip")
		out, _, err := run(t, pub, "--home", home, "artifact", "push", zip, reg.Host()+"/acme/classes/"+cv[0])
		if err != nil {
			t.Fatalf("artifact push: %v\n%s", err, out)
		}
		if !strings.Contains(out, "Pushed oci://"+reg.Host()+"/acme/classes/"+cv[0]+":"+cv[1]) {
			t.Errorf("push out = %q", out)
		}
	}
	// ...and names them in the lock.
	l, err := lock.Read(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	for i := range l.Resolved {
		l.Resolved[i].Source = "oci://" + reg.Host() + "/acme/classes/" + l.Resolved[i].RepoClassName
	}
	if err := lock.Write(repoDir, l); err != nil {
		t.Fatal(err)
	}
	// Anonymous build, no --artifacts, empty cache: the OCI source serves.
	out, _, err := run(t, fakeEnv(nil), "--home", home, "stack", "build", repoDir)
	if err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	if strings.Count(out, "<- source oci://") != 2 {
		t.Errorf("expected both companions from their OCI source: %q", out)
	}

	// And `artifact pull <oci-ref>` fills the cache from the same artifact.
	out, _, err = run(t, fakeEnv(nil), "--home", t.TempDir(), "artifact", "pull", reg.Host()+"/acme/classes/hmd-inf-otel", "--local-url", "http://127.0.0.1:1")
	if err != nil || !strings.Contains(out, "Unpacked hmd-inf-otel@0.1.5") {
		t.Errorf("artifact pull oci: %q, %v", out, err)
	}
}

func TestStackBuildRefusalNamesEveryTierAndTheRemedy(t *testing.T) {
	t.Parallel()
	repoDir, _ := stackRepo(t)
	_, _, err := run(t, fakeEnv(nil), "--home", t.TempDir(), "stack", "build", repoDir)
	if nserr.CodeOf(err) != nserr.Usage {
		t.Fatalf("err = %v", err)
	}
	for _, want := range []string{"artifact cache (not cached)", "no `source` in the lock entry", "cloud librarian", "nsctl artifact push"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message lacks %q:\n%v", want, err)
		}
	}
}

func TestStackPushFromLayoutWithBump(t *testing.T) {
	t.Parallel()
	reg := ocitest.New(t)
	repoDir, artifactsDir := stackRepo(t)
	home := t.TempDir()
	if _, _, err := run(t, fakeEnv(nil), "--home", home, "stack", "build", repoDir, "--artifacts", artifactsDir); err != nil {
		t.Fatal(err)
	}
	layout := filepath.Join(repoDir, "build", "stack")
	ref := reg.Host() + "/acme/stacks/obs"
	pub := fakeEnv(map[string]string{"HMD_REGISTRY_TOKEN": "pat"})

	// First push: nothing published, VERSION 0.1.0 is explicit -> 0.1.0.
	out, _, err := run(t, pub, "--home", home, "stack", "push", ref, "--from", layout, "--bump")
	if err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Version: 0.1.0 (--bump; published: none)") || !strings.Contains(out, "Pushed oci://"+ref+":0.1.0") {
		t.Errorf("out = %q", out)
	}
	// Same again: 0.1.0 is published, and an explicit VERSION cannot bump.
	_, _, err = run(t, pub, "--home", home, "stack", "push", ref, "--from", layout, "--bump")
	if nserr.CodeOf(err) != nserr.Usage || !strings.Contains(err.Error(), "already published") {
		t.Errorf("republish: %v", err)
	}
	// A two-part VERSION lets --bump count from the registry.
	if err := os.WriteFile(filepath.Join(repoDir, "meta-data", "VERSION"), []byte("0.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, fakeEnv(nil), "--home", home, "stack", "build", repoDir, "--artifacts", artifactsDir, "--tag", "0.1"); err != nil {
		t.Fatal(err)
	}
	out, _, err = run(t, pub, "--home", home, "stack", "push", ref, "--from", layout, "--bump")
	if err != nil {
		t.Fatalf("bump: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Version: 0.1.1") {
		t.Errorf("bump out = %q", out)
	}
	// An explicit existing tag without --bump is refused too.
	_, _, err = run(t, pub, "--home", home, "stack", "push", ref+":0.1.1", "--from", layout)
	if nserr.CodeOf(err) != nserr.Usage || !strings.Contains(err.Error(), "immutable") {
		t.Errorf("overwrite: %v", err)
	}
	// The consumer sees both, newest first, and names the stack by its ref.
	out, _, err = run(t, fakeEnv(nil), "--home", home, "stack", "versions", ref)
	if err != nil || !strings.HasPrefix(strings.TrimSpace(out), "0.1.1\n0.1.0") {
		t.Errorf("versions: %q, %v", out, err)
	}
	_, env := fromRepoEnv(t)
	out, _, err = run(t, fakeEnv(env), "stack", "add", ref, "--env", "local", "--local-url", "http://127.0.0.1:1")
	if err != nil || !strings.Contains(out, "Declared stack obs 0.1.1") {
		t.Errorf("add from a layout-built stack: %q, %v", out, err)
	}
}

func TestArtifactPushRefusals(t *testing.T) {
	t.Parallel()
	reg := ocitest.New(t)
	_, _, err := run(t, fakeEnv(nil), "--home", t.TempDir(), "artifact", "push", t.TempDir(), reg.Host()+"/x/y")
	if nserr.CodeOf(err) != nserr.Usage || !strings.Contains(err.Error(), "--token") {
		t.Errorf("anonymous: %v", err)
	}
	pub := fakeEnv(map[string]string{"HMD_REGISTRY_TOKEN": "pat"})
	_, _, err = run(t, pub, "--home", t.TempDir(), "artifact", "push", t.TempDir(), reg.Host()+"/x/y")
	if nserr.CodeOf(err) != nserr.Usage || !strings.Contains(err.Error(), "not a RepoClass tree") {
		t.Errorf("empty dir: %v", err)
	}
}

// NERD019 SPEC005: lock --resolve settles ranges from the OCI source first,
// then the librarian, and keeps sources across regenerates.
func TestLockResolvePinsRangesFromTheOCISource(t *testing.T) {
	t.Parallel()
	reg := ocitest.New(t)
	repoDir := t.TempDir()
	writeFile(t, filepath.Join(repoDir, "meta-data", "manifest.json"), `{
  "name": "hmd-stack-obs",
  "deploy": {"commands": [["exec", "true"]]},
  "local": {"version": 1, "default_profiles": [], "repos": [
    {"instance_name": "otel", "repo_class_name": "hmd-inf-otel", "version_spec": "== 0.1.*"}
  ]}
}`)
	writeFile(t, filepath.Join(repoDir, "meta-data", "VERSION"), "0.1")
	home := t.TempDir()

	// A range with nowhere to look is refused, naming the remedy.
	_, _, err := run(t, fakeEnv(nil), "--home", home, "lock", repoDir, "--resolve")
	if nserr.CodeOf(err) != nserr.Fail || !strings.Contains(err.Error(), "nsctl artifact push") {
		t.Fatalf("no sources: %v", err)
	}
	// Without --resolve a range is still refused as before.
	_, _, err = run(t, fakeEnv(nil), "--home", home, "lock", repoDir)
	if nserr.CodeOf(err) != nserr.Usage {
		t.Errorf("range without --resolve: %v", err)
	}

	// Publish two versions of the class and name the source in the lock.
	pub := fakeEnv(map[string]string{"HMD_REGISTRY_TOKEN": "pat"})
	for _, v := range []string{"0.1.5", "0.1.9", "0.2.0"} {
		zip := filepath.Join(t.TempDir(), "hmd-inf-otel_"+v+"_build.zip")
		if err := os.WriteFile(zip, artifactZip(t, "hmd-inf-otel", v, "x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := run(t, pub, "--home", home, "artifact", "push", zip, reg.Host()+"/acme/classes/hmd-inf-otel"); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(repoDir, "neuronsphere.lock"), `version = 1
repo_class_name = "hmd-stack-obs"
generated_from = "pins"

[[resolved]]
repo_class_name = "hmd-inf-otel"
version = "0.1.5"
profiles = []
content_path = "repository:/hmd-inf-otel/0.1.5/hmd-inf-otel_0.1.5_build.zip"
source = "oci://`+reg.Host()+`/acme/classes/hmd-inf-otel"
`)
	out, _, err := run(t, fakeEnv(nil), "--home", home, "lock", repoDir, "--resolve")
	if err != nil {
		t.Fatalf("resolve: %v\n%s", err, out)
	}
	if !strings.Contains(out, `Resolved hmd-inf-otel "== 0.1.*" to 0.1.9 (source oci://`) {
		t.Errorf("out = %q", out)
	}
	l, err := lock.Read(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	e, _ := l.Entry("hmd-inf-otel")
	if e.Version != "0.1.9" || e.Source != "oci://"+reg.Host()+"/acme/classes/hmd-inf-otel" || l.GeneratedFrom != "resolve" {
		t.Errorf("entry = %+v (from %s)", e, l.GeneratedFrom)
	}
	// A plain regenerate keeps the source too.
	if _, _, err := run(t, fakeEnv(nil), "--home", home, "lock", repoDir, "--pin", "hmd-inf-otel@0.2.0"); err != nil {
		t.Fatal(err)
	}
	l, _ = lock.Read(repoDir)
	if e, _ := l.Entry("hmd-inf-otel"); e.Source == "" || e.Version != "0.2.0" {
		t.Errorf("source lost on regenerate: %+v", e)
	}
}

// NERD019 SPEC006: validate knows a stack.
func TestRepoclassValidateChecksAStack(t *testing.T) {
	t.Parallel()
	repoDir, _ := stackRepo(t)
	out, _, err := run(t, fakeEnv(nil), "repoclass", "--path", repoDir, "validate")
	if err != nil {
		t.Fatalf("a fresh stack must validate: %v\n%s", err, out)
	}
	if !strings.Contains(out, "has no digest yet") {
		t.Errorf("expected the digest note: %q", out)
	}
	// Remove the lock: an error naming `nsctl lock`.
	if err := os.Remove(filepath.Join(repoDir, "neuronsphere.lock")); err != nil {
		t.Fatal(err)
	}
	out, _, err = run(t, fakeEnv(nil), "repoclass", "--path", repoDir, "validate")
	if nserr.CodeOf(err) != nserr.Fail || !strings.Contains(out, "nsctl lock") {
		t.Errorf("no lock: %v\n%s", err, out)
	}
	// An unbound, unpinned role.
	writeFile(t, filepath.Join(repoDir, "meta-data", "manifest.json"), `{
  "name": "hmd-stack-obs",
  "description": "d", "build": {},
  "deploy": {"commands": [["exec", "true"]], "dependencies": {
    "sink": {"repo_class_name": "hmd-inf-s3bucket", "required": "true", "version_spec": "0.1.13"}}},
  "local": {"version": 1, "default_profiles": [], "dependencies": {"sink": {"instance_configuration": {"x": "y"}}},
    "repos": [{"instance_name": "otel", "repo_class_name": "hmd-inf-otel", "version_spec": "0.1.5"}]}
}`)
	writeFile(t, filepath.Join(repoDir, "neuronsphere.lock"), "version = 1\nrepo_class_name = \"hmd-stack-obs\"\ngenerated_from = \"pins\"\n\n[[resolved]]\nrepo_class_name = \"hmd-inf-otel\"\nversion = \"0.1.5\"\nprofiles = []\ncontent_path = \"x\"\n")
	out, _, err = run(t, fakeEnv(nil), "repoclass", "--path", repoDir, "validate")
	if nserr.CodeOf(err) != nserr.Fail || !strings.Contains(out, "deploy.dependencies.sink") || !strings.Contains(out, "declared but not pinned") {
		t.Errorf("unbound role: %v\n%s", err, out)
	}
	// A plain RepoClass is untouched by any of this.
	plain := t.TempDir()
	writeFile(t, filepath.Join(plain, "meta-data", "manifest.json"), `{"name":"hmd-ms-x","description":"d","build":{},"deploy":{"commands":[["exec","true"]]}}`)
	writeFile(t, filepath.Join(plain, "meta-data", "VERSION"), "0.1")
	if _, _, err := run(t, fakeEnv(nil), "repoclass", "--path", plain, "validate"); err != nil {
		t.Errorf("plain repo: %v", err)
	}
}

// NERD019 SPEC008: the local section is authored by verbs, and what they
// write is what stack build and stack add read.
func TestRepoclassLocalVerbsAuthorAStack(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	rcl := func(args ...string) (string, error) {
		out, _, err := run(t, fakeEnv(nil), append([]string{"repoclass", "--path", dir}, args...)...)
		return out, err
	}
	if _, err := rcl("init", "hmd-stack-obs", "--description", "obs"); err != nil {
		t.Fatal(err)
	}
	if _, err := rcl("deploy", "set-command", "exec", "true"); err != nil {
		t.Fatal(err)
	}
	if out, err := rcl("local", "add", "hmd-inf-otel", "--spec", "== 0.1.5", "--name", "otel", "--depends", "sink=sink"); err != nil || !strings.Contains(out, "wrote meta-data/manifest.json local.repos.otel") {
		t.Fatalf("add: %q %v", out, err)
	}
	if _, err := rcl("local", "add", "hmd-inf-clickhouse", "--spec", "== 0.3.0", "--profile", "full"); err != nil {
		t.Fatal(err)
	}
	if _, err := rcl("deploy", "add-dependency", "sink", "--repo-class-name", "hmd-inf-s3bucket", "--version-spec", "0.1.13",
		"--resource-namespace", "storage.neuronsphere.io", "--resource-definition-name", "bucket", "--resource-version", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := rcl("deploy", "add-dependency", "compute", "--repo-class-name", "hmd-inf-eks-node-group"); err != nil {
		t.Fatal(err)
	}
	if _, err := rcl("local", "bind", "compute", "local-neuronsphere"); err != nil {
		t.Fatal(err)
	}
	if _, err := rcl("local", "require", "sink", "--suggest", "storage"); err != nil {
		t.Fatal(err)
	}
	if _, err := rcl("local", "set-default-profiles", "full"); err != nil {
		t.Fatal(err)
	}
	// A role nobody declared is refused with the remedy.
	if _, err := rcl("local", "bind", "nope", "x"); nserr.CodeOf(err) != nserr.Usage || !strings.Contains(err.Error(), "deploy add-dependency nope") {
		t.Errorf("unknown role: %v", err)
	}

	out, err := rcl("local", "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"otel", "companion", "inf-clickhouse", "full", "compute", "bound -> local-neuronsphere", "sink", "external (suggest storage)", "default profiles: full"} {
		if !strings.Contains(out, want) {
			t.Errorf("list lacks %q:\n%s", want, out)
		}
	}
	// The result parses, validates the way a stack does, locks with no
	// network (exact specs, external and bound roles unpinned)...
	spec, err := localspec.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Local.Repos) != 2 || spec.Local.Dependencies["sink"].Suggest != "storage" || !spec.Local.Dependencies["sink"].External {
		t.Errorf("spec = %+v", spec.Local)
	}
	if _, _, err := run(t, fakeEnv(nil), "lock", dir); err != nil {
		t.Fatalf("lock: %v", err)
	}
	l, _ := lock.Read(dir)
	if _, ok := l.Entry("hmd-inf-s3bucket"); ok {
		t.Error("an external role must not be pinned")
	}
	if _, ok := l.Entry("hmd-inf-eks-node-group"); ok {
		t.Error("a bound role must not be pinned")
	}
	if out, _, err := run(t, fakeEnv(nil), "repoclass", "--path", dir, "validate"); err != nil {
		t.Errorf("validate: %v\n%s", err, out)
	}
	if _, err := rcl("local", "remove", "inf-clickhouse"); err != nil {
		t.Fatal(err)
	}
	spec, _ = localspec.Load(dir)
	if len(spec.Local.Repos) != 1 {
		t.Errorf("remove left %d companions", len(spec.Local.Repos))
	}
}
