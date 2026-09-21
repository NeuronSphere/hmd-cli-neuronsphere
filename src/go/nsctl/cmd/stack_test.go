package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/artifact"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/lock"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/oci/ocitest"
)

// stackManifest is a stack RepoClass: nothing of its own to deploy, one
// unconditional companion, one profile-gated companion. NERD017 SPEC001.
const stackManifest = `{
  "name": "hmd-stack-obs",
  "description": "otel with an optional clickhouse",
  "build": {},
  "deploy": {"commands": [["exec", "true"]]},
  "local": {"version": 1, "default_profiles": [], "repos": [
    {"instance_name": "otel",       "repo_class_name": "hmd-inf-otel",       "version_spec": "0.1.5"},
    {"instance_name": "clickhouse", "repo_class_name": "hmd-inf-clickhouse", "version_spec": "0.3.0",
     "profiles": ["full"]}
  ]}
}`

// stackRepo writes a stack repository with its lock, and a release directory
// holding the companions' build zips as `hmd build` would name them.
func stackRepo(t *testing.T) (repoDir, artifactsDir string) {
	t.Helper()
	repoDir = t.TempDir()
	writeFile(t, filepath.Join(repoDir, "meta-data", "manifest.json"), stackManifest)
	writeFile(t, filepath.Join(repoDir, "meta-data", "VERSION"), "0.1.0")
	if _, _, err := run(t, fakeEnv(nil), "lock", repoDir); err != nil {
		t.Fatalf("lock: %v", err)
	}
	artifactsDir = t.TempDir()
	for _, cv := range [][2]string{{"hmd-inf-otel", "0.1.5"}, {"hmd-inf-clickhouse", "0.3.0"}} {
		name := cv[0] + "_" + cv[1] + "_build.zip"
		if err := os.WriteFile(filepath.Join(artifactsDir, name), artifactZip(t, cv[0], cv[1], "released"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return repoDir, artifactsDir
}

// publishStack pushes the fixture stack to a registry as a paid publisher
// would, from a release directory, and returns the versionless reference.
func publishStack(t *testing.T, reg *ocitest.Registry, repoDir, artifactsDir, name string) string {
	t.Helper()
	ref := reg.Host() + "/hmdlabs/stacks/" + name
	pubEnv := fakeEnv(map[string]string{"HMD_REGISTRY_TOKEN": "pat", "HMD_LOCAL_MS_DEPLOYMENT_URL": "http://127.0.0.1:1"})
	out, errOut, err := run(t, pubEnv, "--home", t.TempDir(), "stack", "push", repoDir, ref, "--artifacts", artifactsDir)
	if err != nil {
		t.Fatalf("stack push: %v\n%s%s", err, out, errOut)
	}
	if !strings.Contains(out, "Pushed oci://"+ref+":0.1.0") {
		t.Fatalf("push out = %q", out)
	}
	return ref
}

func TestStackPushReadsTheVersionAndFillsDigests(t *testing.T) {
	t.Parallel()
	reg := ocitest.New(t)
	repoDir, artifactsDir := stackRepo(t)
	ref := reg.Host() + "/hmdlabs/stacks/obs"
	env := fakeEnv(map[string]string{"HMD_REGISTRY_TOKEN": "pat"})

	out, _, err := run(t, env, "--home", t.TempDir(), "stack", "push", repoDir, ref, "--artifacts", artifactsDir, "--update-lock")
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if !strings.Contains(out, "credential from HMD_REGISTRY_TOKEN") || !strings.Contains(out, "(3 layer(s))") {
		t.Errorf("out = %q", out)
	}
	l, err := lock.Read(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range l.Resolved {
		if !strings.HasPrefix(e.Digest, "sha256:") {
			t.Errorf("--update-lock left %s without a digest", e.RepoClassName)
		}
	}
	if _, ok := reg.Manifest("hmdlabs/stacks/obs", "0.1.0"); !ok {
		t.Error("nothing stored under the VERSION tag")
	}
}

func TestStackPushRefusals(t *testing.T) {
	t.Parallel()
	reg := ocitest.New(t)
	repoDir, artifactsDir := stackRepo(t)
	home := t.TempDir()

	// Anonymous: refused before any request.
	_, _, err := run(t, fakeEnv(nil), "--home", home, "stack", "push", repoDir, reg.Host()+"/x/obs", "--artifacts", artifactsDir)
	if nserr.CodeOf(err) != nserr.Usage || !strings.Contains(err.Error(), "--token") {
		t.Errorf("anonymous: %v", err)
	}
	if n := len(reg.Requests()); n != 0 {
		t.Errorf("anonymous push made %d requests", n)
	}
	// A missing companion zip with no librarian to fall back to.
	empty := t.TempDir()
	_, _, err = run(t, fakeEnv(map[string]string{"HMD_REGISTRY_TOKEN": "pat"}), "--home", home,
		"stack", "push", repoDir, reg.Host()+"/x/obs", "--artifacts", empty)
	if nserr.CodeOf(err) != nserr.Usage || !strings.Contains(err.Error(), "_build.zip") {
		t.Errorf("missing zip: %v", err)
	}
	// Not a stack.
	plain := t.TempDir()
	writeFile(t, filepath.Join(plain, "meta-data", "manifest.json"), `{"name":"hmd-ms-x"}`)
	_, _, err = run(t, fakeEnv(map[string]string{"HMD_REGISTRY_TOKEN": "pat"}), "--home", home,
		"stack", "push", plain, reg.Host()+"/x/obs:1.0", "--artifacts", artifactsDir)
	if nserr.CodeOf(err) != nserr.Usage || !strings.Contains(err.Error(), "not a stack") {
		t.Errorf("plain repo: %v", err)
	}
}

func TestStackAddDeclaresFromAnAnonymousPull(t *testing.T) {
	t.Parallel()
	reg := ocitest.New(t)
	repoDir, artifactsDir := stackRepo(t)
	ref := publishStack(t, reg, repoDir, artifactsDir, "obs")

	home, env := fromRepoEnv(t)
	// No credential of any kind, and the control plane's librarian is a
	// closed port: the Put is a warning, not a failure (SPEC003 step 4).
	out, errOut, err := run(t, fakeEnv(env), "stack", "add", ref, "--env", "local", "--local-url", "http://127.0.0.1:1")
	if err != nil {
		t.Fatalf("stack add: %v\n%s%s", err, out, errOut)
	}
	for _, want := range []string{
		"Resolved oci://" + ref + " to 0.1.0",
		"credential: anonymous",
		"hmd-stack-obs@0.1.0",
		"Declared stack obs 0.1.0",
		"Run: nsctl env apply local",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("out lacks %q:\n%s", want, out)
		}
	}
	if !strings.Contains(errOut, "warning: the control plane's Artifact Librarian") {
		t.Errorf("expected the librarian warning, got %q", errOut)
	}

	m := loadEnv(t, home, "local")
	subject, ok := m.Repo("stack-obs")
	if !ok || subject.SourceType() != manifest.SourceArtifact || subject.Version != "0.1.0" || subject.RepoClassName != "hmd-stack-obs" {
		t.Errorf("subject = %+v, %v (instances %v)", subject, ok, instanceNames(m))
	}
	otel, ok := m.Repo("otel")
	if !ok || otel.Version != "0.1.5" || otel.SourceType() != manifest.SourceArtifact {
		t.Errorf("otel = %+v", otel)
	}
	if _, ok := m.Repo("clickhouse"); ok {
		t.Error("profile-gated companion declared without its profile")
	}
	rec, _, ok := m.Stack("obs")
	if !ok || rec.Version != "0.1.0" || rec.Ref != "oci://"+ref || !strings.HasPrefix(rec.Digest, "sha256:") {
		t.Errorf("record = %+v", rec)
	}
	if rec.Bindings["hmd-stack-obs"] != "stack-obs" || rec.Bindings["otel"] != "otel" {
		t.Errorf("bindings = %v", rec.Bindings)
	}
	if len(m.Bindings) != 0 {
		t.Errorf("a stack must not write the manifest's own bindings: %v", m.Bindings)
	}
	for _, cv := range [][2]string{{"hmd-stack-obs", "0.1.0"}, {"hmd-inf-otel", "0.1.5"}, {"hmd-inf-clickhouse", "0.3.0"}} {
		if !artifact.Cached(home, cv[0], cv[1]) {
			t.Errorf("%s@%s not cached", cv[0], cv[1])
		}
	}
	if _, err := os.Stat(filepath.Join(artifact.Dir(home, "hmd-stack-obs", "0.1.0"), lock.FileName)); err != nil {
		t.Errorf("lock not written into the stack's tree: %v", err)
	}

	// Idempotent, and a profile widens it.
	out, _, err = run(t, fakeEnv(env), "stack", "add", ref, "--env", "local", "--local-url", "http://127.0.0.1:1", "--profile", "full")
	if err != nil {
		t.Fatalf("second add: %v", err)
	}
	if !strings.Contains(out, "already declared") {
		t.Errorf("second add should say so: %q", out)
	}
	m = loadEnv(t, home, "local")
	if _, ok := m.Repo("clickhouse"); !ok {
		t.Error("--profile full must declare clickhouse")
	}
	rec, _, _ = m.Stack("obs")
	if strings.Join(rec.Profiles, ",") != "full" {
		t.Errorf("record profiles = %v", rec.Profiles)
	}

	// list and remove.
	out, _, err = run(t, fakeEnv(env), "stack", "list", "--env", "local")
	if err != nil || !strings.Contains(out, "obs") || !strings.Contains(out, "0.1.0") || !strings.Contains(out, "clickhouse") {
		t.Errorf("list: %q, %v", out, err)
	}
	out, _, err = run(t, fakeEnv(env), "stack", "remove", "obs", "--env", "local")
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !strings.Contains(out, "undeclared: clickhouse, otel, stack-obs") {
		t.Errorf("remove out = %q", out)
	}
	m = loadEnv(t, home, "local")
	if len(m.Repos) != 0 || len(m.Stacks) != 0 {
		t.Errorf("after remove: repos %v, stacks %v", instanceNames(m), m.Stacks)
	}
	if !artifact.Cached(home, "hmd-inf-otel", "0.1.5") {
		t.Error("remove must keep the cache")
	}
	_, _, err = run(t, fakeEnv(env), "stack", "remove", "obs", "--env", "local")
	if nserr.CodeOf(err) != nserr.Usage {
		t.Errorf("removing twice: %v", err)
	}
}

func TestStackRemoveLeavesSharedInstancesAlone(t *testing.T) {
	t.Parallel()
	reg := ocitest.New(t)
	repoDir, artifactsDir := stackRepo(t)
	ref := publishStack(t, reg, repoDir, artifactsDir, "obs")
	home, env := fromRepoEnv(t)
	if _, _, err := run(t, fakeEnv(env), "stack", "add", ref, "--env", "local", "--local-url", "http://127.0.0.1:1"); err != nil {
		t.Fatal(err)
	}
	// The user also declares otel by hand under the same name: now it is
	// theirs as much as the stack's, and remove must not take it.
	m := loadEnv(t, home, "local")
	m.Bindings = map[string]string{"otel": "otel"}
	if err := m.Save(m.Path); err != nil {
		t.Fatal(err)
	}
	out, _, err := run(t, fakeEnv(env), "stack", "remove", "obs", "--env", "local")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "undeclared: otel") || !strings.Contains(out, "stack-obs") {
		t.Errorf("out = %q", out)
	}
	m = loadEnv(t, home, "local")
	if _, ok := m.Repo("otel"); !ok {
		t.Error("shared instance removed")
	}
}

func TestStackVersionsListsAndCaches(t *testing.T) {
	t.Parallel()
	reg := ocitest.New(t)
	repoDir, artifactsDir := stackRepo(t)
	publishStack(t, reg, repoDir, artifactsDir, "obs")
	// A second, newer version and a moving tag.
	m, _ := reg.Manifest("hmdlabs/stacks/obs", "0.1.0")
	reg.PutRaw("hmdlabs/stacks/obs", "0.2.0", "application/vnd.oci.image.manifest.v1+json", m, nil)
	reg.PutRaw("hmdlabs/stacks/obs", "latest", "application/vnd.oci.image.manifest.v1+json", m, nil)

	home := t.TempDir()
	env := fakeEnv(nil)
	out, _, err := run(t, env, "--home", home, "stack", "versions", reg.Host()+"/hmdlabs/stacks/obs", "--spec", "== 0.1.*")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(strings.TrimSpace(out), "0.2.0\n0.1.0") || strings.Contains(out, "latest") || !strings.Contains(out, "to 0.1.0") {
		t.Errorf("out = %q", out)
	}
	out, _, err = run(t, env, "--home", home, "stack", "versions", reg.Host()+"/hmdlabs/stacks/obs", "--offline")
	if err != nil || !strings.Contains(out, "From cache") || !strings.Contains(out, "0.2.0") {
		t.Errorf("offline: %q, %v", out, err)
	}
}

func TestStackPullCachesWithoutDeclaring(t *testing.T) {
	t.Parallel()
	reg := ocitest.New(t)
	repoDir, artifactsDir := stackRepo(t)
	ref := publishStack(t, reg, repoDir, artifactsDir, "obs")
	home, env := fromRepoEnv(t)
	out, _, err := run(t, fakeEnv(env), "stack", "pull", ref+":0.1.0", "--local-url", "http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Cached stack obs 0.1.0") {
		t.Errorf("out = %q", out)
	}
	if !artifact.Cached(home, "hmd-inf-clickhouse", "0.3.0") {
		t.Error("not cached")
	}
	if m, _ := manifest.Load(home, "local", fakeEnv(env)); m != nil && len(m.Stacks) != 0 {
		t.Error("pull must not declare")
	}
}

func TestStackAddRefusals(t *testing.T) {
	t.Parallel()
	reg := ocitest.New(t)
	_, _, err := run(t, fakeEnv(nil), "stack", "add", "obs")
	if nserr.CodeOf(err) != nserr.Usage || !strings.Contains(err.Error(), "HMD_HOME") {
		t.Errorf("no home: %v", err)
	}
	_, env := fromRepoEnv(t)
	_, _, err = run(t, fakeEnv(env), "stack", "add", "github.com/acme/stack@1.0")
	if nserr.CodeOf(err) != nserr.Usage || !strings.Contains(err.Error(), "NERD016") {
		t.Errorf("github: %v", err)
	}
	_, _, err = run(t, fakeEnv(env), "stack", "add", reg.Host()+"/hmdlabs/stacks/absent")
	if nserr.CodeOf(err) != nserr.Fail || !strings.Contains(err.Error(), "private") {
		t.Errorf("absent: %v", err)
	}
	_, _, err = run(t, fakeEnv(env), "stack", "versions", "127.0.0.1:1/x/y")
	if nserr.CodeOf(err) != nserr.Fail || !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Errorf("closed port must fail cleanly naming the host: %v", err)
	}
}

// TestLockFillsDigestsFromTheCache is NERD017 SPEC007: `nsctl lock` records
// a digest for anything the cache has seen, and keeps one it had.
func TestLockFillsDigestsFromTheCache(t *testing.T) {
	t.Parallel()
	home, env := fromRepoEnv(t)
	repoDir := t.TempDir()
	writeFile(t, filepath.Join(repoDir, "meta-data", "manifest.json"), stackManifest)
	writeFile(t, filepath.Join(repoDir, "meta-data", "VERSION"), "0.1.0")
	data := artifactZip(t, "hmd-inf-otel", "0.1.5", "cached")
	if _, err := artifact.Store(home, "hmd-inf-otel", "0.1.5", data); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, fakeEnv(env), "lock", repoDir); err != nil {
		t.Fatal(err)
	}
	l, err := lock.Read(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	otel, _ := l.Entry("hmd-inf-otel")
	if !strings.HasPrefix(otel.Digest, "sha256:") {
		t.Errorf("cached class has no digest: %+v", otel)
	}
	ch, _ := l.Entry("hmd-inf-clickhouse")
	if ch.Digest != "" {
		t.Errorf("uncached class must have none: %+v", ch)
	}
	// Regenerating keeps a digest the previous lock had at the same version.
	if err := artifact.Invalidate(home, "hmd-inf-otel", "0.1.5"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, fakeEnv(env), "lock", repoDir); err != nil {
		t.Fatal(err)
	}
	l, _ = lock.Read(repoDir)
	if again, _ := l.Entry("hmd-inf-otel"); again.Digest != otel.Digest {
		t.Errorf("digest lost on regenerate: %q", again.Digest)
	}
}
