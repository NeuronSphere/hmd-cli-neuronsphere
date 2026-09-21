package cmd

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/oci/ocitest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/stack"
)

// NERD017 SPEC011: the manifest's `license` is the author's declaration.
// A tree is zipped as it says, every layer is annotated with what its own
// manifest says, and nothing is refused on licence grounds.

// zipWith is artifactZip with the manifest body and extra entries chosen by
// the test, for a companion that declares something.
func zipWith(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func zipEntries(t *testing.T, data []byte) map[string]bool {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, f := range zr.File {
		out[f.Name] = true
	}
	return out
}

// declaringStackManifest is stackManifest with a licence: the subject keeps
// its service code out of what it publishes.
var declaringStackManifest = strings.Replace(stackManifest, `"build": {},`,
	`"build": {}, "license": {"spdx": "Apache-2.0", "exclude": ["src/python/"]},`, 1)

func TestStackBuildSubjectHonoursItsDeclaredLicence(t *testing.T) {
	t.Parallel()
	repoDir, artifactsDir := stackRepo(t)
	writeFile(t, filepath.Join(repoDir, "meta-data", "manifest.json"), declaringStackManifest)
	writeFile(t, filepath.Join(repoDir, "src", "python", "svc.py"), "service code")
	writeFile(t, filepath.Join(repoDir, "src", "local", "scripts", "python", "seed.sh"), "kept")
	writeFile(t, filepath.Join(repoDir, "src", "cdktf", "stack.py"), "kept")
	home := t.TempDir()

	out, _, err := run(t, fakeEnv(nil), "--home", home, "stack", "build", repoDir, "--artifacts", artifactsDir)
	if err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Licences: hmd-stack-obs=Apache-2.0 hmd-inf-clickhouse=(undeclared) hmd-inf-otel=(undeclared)") {
		t.Errorf("out = %q", out)
	}
	m, blobs, _, err := stack.ReadLayout(filepath.Join(repoDir, "build", "stack"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Annotations[stack.AnnotationLicenses] != "Apache-2.0" {
		t.Errorf("manifest licence = %q", m.Annotations[stack.AnnotationLicenses])
	}
	var subject v1.Descriptor
	for _, d := range m.Layers {
		if d.Annotations[stack.AnnotationClass] == "hmd-stack-obs" {
			subject = d
		}
	}
	if subject.Annotations[stack.AnnotationLicenses] != "Apache-2.0" {
		t.Errorf("subject layer = %v", subject.Annotations)
	}
	entries := zipEntries(t, blobs[subject.Digest])
	if entries["src/python/svc.py"] {
		t.Error("the subject zip carries src/python/svc.py despite the declared exclude")
	}
	for _, want := range []string{"src/local/scripts/python/seed.sh", "src/cdktf/stack.py", "meta-data/manifest.json", "neuronsphere.lock"} {
		if !entries[want] {
			t.Errorf("subject zip lacks %s", want)
		}
	}
}

// A companion that arrives as bytes is the author's: annotated from the
// manifest inside, pushed whole, never re-scoped and never refused.
func TestStackPushAnnotatesCompanionsFromTheirOwnManifest(t *testing.T) {
	t.Parallel()
	reg := ocitest.New(t)
	repoDir, artifactsDir := stackRepo(t)
	if err := os.WriteFile(filepath.Join(artifactsDir, "hmd-inf-otel_0.1.5_build.zip"), zipWith(t, map[string]string{
		"meta-data/manifest.json": `{"name": "hmd-inf-otel", "license": {"spdx": "BUSL-1.1", "exclude": ["src/python"]}}`,
		"meta-data/VERSION":       "0.1.5",
		"src/python/app.py":       "the author's bytes",
	}), 0o644); err != nil {
		t.Fatal(err)
	}
	pub := fakeEnv(map[string]string{"HMD_REGISTRY_TOKEN": "pat"})
	out, errOut, err := run(t, pub, "--home", t.TempDir(), "stack", "push", repoDir, reg.Host()+"/acme/stacks/obs", "--artifacts", artifactsDir)
	if err != nil {
		t.Fatalf("push: %v\n%s\n%s", err, out, errOut)
	}
	if !strings.Contains(out, "Licences: hmd-stack-obs=(undeclared) hmd-inf-clickhouse=(undeclared) hmd-inf-otel=BUSL-1.1") {
		t.Errorf("out = %q", out)
	}
	if strings.Contains(errOut, "warning") {
		t.Errorf("a declaration is not a warning: %q", errOut)
	}
	raw, ok := reg.Manifest("acme/stacks/obs", "0.1.0")
	if !ok {
		t.Fatal("nothing pushed")
	}
	var m v1.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, d := range m.Layers {
		if d.Annotations[stack.AnnotationClass] != "hmd-inf-otel" {
			continue
		}
		if d.Annotations[stack.AnnotationLicenses] != "BUSL-1.1" {
			t.Errorf("otel layer = %v", d.Annotations)
		}
		blob, ok := reg.Blob("acme/stacks/obs", d.Digest)
		if !ok || !zipEntries(t, blob)["src/python/app.py"] {
			t.Error("the companion's bytes were altered on the way to the registry")
		}
	}
	if _, ok := m.Annotations[stack.AnnotationLicenses]; ok {
		t.Errorf("an undeclared subject gave the manifest a licence: %v", m.Annotations)
	}

	// The layout path says the same thing.
	if _, _, err := run(t, fakeEnv(nil), "--home", t.TempDir(), "stack", "build", repoDir, "--artifacts", artifactsDir); err != nil {
		t.Fatal(err)
	}
	out, _, err = run(t, pub, "--home", t.TempDir(), "stack", "push", reg.Host()+"/acme/stacks/obs2", "--from", filepath.Join(repoDir, "build", "stack"))
	if err != nil {
		t.Fatalf("push --from: %v\n%s", err, out)
	}
	if !strings.Contains(out, "hmd-inf-otel=BUSL-1.1") {
		t.Errorf("out = %q", out)
	}
}

func TestArtifactPushHonoursTheDeclaration(t *testing.T) {
	t.Parallel()
	reg := ocitest.New(t)
	pub := fakeEnv(map[string]string{"HMD_REGISTRY_TOKEN": "pat"})

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "meta-data", "manifest.json"), `{"name": "hmd-ms-x", "license": {"spdx": "MIT", "exclude": ["src/python"]}}`)
	writeFile(t, filepath.Join(dir, "meta-data", "VERSION"), "0.2.0")
	writeFile(t, filepath.Join(dir, "src", "cdktf", "stack.py"), "kept")
	writeFile(t, filepath.Join(dir, "src", "python", "app.py"), "excluded")
	out, _, err := run(t, pub, "--home", t.TempDir(), "artifact", "push", dir, reg.Host()+"/acme/classes/hmd-ms-x")
	if err != nil {
		t.Fatalf("push dir: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Licence: MIT") {
		t.Errorf("out = %q", out)
	}
	raw, _ := reg.Manifest("acme/classes/hmd-ms-x", "0.2.0")
	var m v1.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m.Annotations[stack.AnnotationLicenses] != "MIT" || m.Layers[0].Annotations[stack.AnnotationLicenses] != "MIT" {
		t.Errorf("annotations: manifest %v, layer %v", m.Annotations, m.Layers[0].Annotations)
	}
	blob, _ := reg.Blob("acme/classes/hmd-ms-x", m.Layers[0].Digest)
	if entries := zipEntries(t, blob); entries["src/python/app.py"] || !entries["src/cdktf/stack.py"] {
		t.Errorf("pushed zip entries = %v", entries)
	}

	// A zip file is pushed as it is; the declaration inside still annotates.
	file := filepath.Join(t.TempDir(), "hmd-ms-y_0.3.0_build.zip")
	if err := os.WriteFile(file, zipWith(t, map[string]string{
		"meta-data/manifest.json": `{"name": "hmd-ms-y", "license": "BUSL-1.1"}`,
		"meta-data/VERSION":       "0.3.0",
		"src/python/app.py":       "the author's bytes",
	}), 0o644); err != nil {
		t.Fatal(err)
	}
	out, errOut, err := run(t, pub, "--home", t.TempDir(), "artifact", "push", file, reg.Host()+"/acme/classes/hmd-ms-y")
	if err != nil {
		t.Fatalf("push zip: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Licence: BUSL-1.1") || strings.Contains(errOut, "warning") {
		t.Errorf("out = %q, err = %q", out, errOut)
	}
	raw, _ = reg.Manifest("acme/classes/hmd-ms-y", "0.3.0")
	m = v1.Manifest{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	blob, _ = reg.Blob("acme/classes/hmd-ms-y", m.Layers[0].Digest)
	if !zipEntries(t, blob)["src/python/app.py"] {
		t.Error("a zip file was altered on the way to the registry")
	}

	// Undeclared: pushed whole, unannotated, and said so.
	plain := t.TempDir()
	writeFile(t, filepath.Join(plain, "meta-data", "manifest.json"), `{"name": "hmd-inf-z"}`)
	writeFile(t, filepath.Join(plain, "meta-data", "VERSION"), "1.0.0")
	writeFile(t, filepath.Join(plain, "src", "python", "app.py"), "travels")
	out, _, err = run(t, pub, "--home", t.TempDir(), "artifact", "push", plain, reg.Host()+"/acme/classes/hmd-inf-z")
	if err != nil {
		t.Fatalf("push undeclared: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Licence: (undeclared)") {
		t.Errorf("out = %q", out)
	}
	raw, _ = reg.Manifest("acme/classes/hmd-inf-z", "1.0.0")
	m = v1.Manifest{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Layers[0].Annotations[stack.AnnotationLicenses]; ok {
		t.Errorf("undeclared class annotated: %v", m.Layers[0].Annotations)
	}
	blob, _ = reg.Blob("acme/classes/hmd-inf-z", m.Layers[0].Digest)
	if !zipEntries(t, blob)["src/python/app.py"] {
		t.Error("an undeclared tree lost src/python")
	}
}
