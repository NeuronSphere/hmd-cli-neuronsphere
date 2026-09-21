package classart

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/stack"
)

// NERD017 SPEC011 for the single-class artifact: the one layer and the
// manifest carry what the class declares; a tree is zipped as its
// manifest's exclude says, a zip file is pushed as it is.

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func names(t *testing.T, data []byte) map[string]bool {
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

func TestFromDirHonoursTheDeclarationAndBuildAnnotatesIt(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	manifest := `{"name": "hmd-ms-x", "license": {"spdx": "Apache-2.0", "exclude": ["src/python"]}}`
	writeTree(t, root, map[string]string{
		"meta-data/manifest.json": manifest,
		"meta-data/VERSION":       "0.4.2",
		"src/cdktf/stack.py":      "stack",
		"src/python/app.py":       "service code",
	})
	class, version, manifestJSON, data, err := FromDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if class != "hmd-ms-x" || version != "0.4.2" {
		t.Errorf("class/version = %s/%s", class, version)
	}
	got := names(t, data)
	if got["src/python/app.py"] || !got["src/cdktf/stack.py"] {
		t.Errorf("tree zip entries = %v", got)
	}

	m, blobs, err := Build(class, version, manifestJSON, data)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Layers) != 1 || m.Layers[0].Annotations[stack.AnnotationLicenses] != "Apache-2.0" {
		t.Errorf("layer = %+v", m.Layers)
	}
	if m.Annotations[stack.AnnotationLicenses] != "Apache-2.0" || m.Annotations["org.opencontainers.image.title"] != "hmd-ms-x" {
		t.Errorf("manifest annotations = %v", m.Annotations)
	}
	if _, ok := blobs[m.Layers[0].Digest]; !ok {
		t.Error("layer blob missing")
	}

	// A zip file is the author's bytes: nothing is stripped, and the
	// declaration inside is still what annotates it.
	full := t.TempDir()
	writeTree(t, full, map[string]string{"meta-data/manifest.json": `{"name": "hmd-ms-x"}`, "meta-data/VERSION": "0.4.2"})
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{"meta-data/manifest.json": manifest, "meta-data/VERSION": "0.4.2", "src/python/app.py": "kept"} {
		w, _ := zw.Create(name)
		w.Write([]byte(body))
	}
	zw.Close()
	file := filepath.Join(t.TempDir(), "hmd-ms-x_0.4.2_build.zip")
	if err := os.WriteFile(file, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, mj, data, err := FromDir(file)
	if err != nil {
		t.Fatal(err)
	}
	if !names(t, data)["src/python/app.py"] {
		t.Error("a zip file lost an entry; it must be pushed as is")
	}
	m, _, err = Build("hmd-ms-x", "0.4.2", mj, data)
	if err != nil {
		t.Fatal(err)
	}
	if m.Layers[0].Annotations[stack.AnnotationLicenses] != "Apache-2.0" {
		t.Errorf("zip-file layer = %+v", m.Layers[0].Annotations)
	}

	// Undeclared: no annotation anywhere.
	plain := t.TempDir()
	writeTree(t, plain, map[string]string{"meta-data/manifest.json": `{"name": "hmd-inf-y"}`, "meta-data/VERSION": "1.0"})
	class, version, mj, data, err = FromDir(plain)
	if err != nil {
		t.Fatal(err)
	}
	m, _, err = Build(class, version, mj, data)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Layers[0].Annotations[stack.AnnotationLicenses]; ok {
		t.Error("undeclared class was annotated")
	}
	if _, ok := m.Annotations[stack.AnnotationLicenses]; ok {
		t.Error("undeclared class manifest was annotated")
	}
}
