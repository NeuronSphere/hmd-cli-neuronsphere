package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/artifact"
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
