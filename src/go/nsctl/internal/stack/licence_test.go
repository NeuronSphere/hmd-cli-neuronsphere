package stack

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/oci"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/oci/ocitest"
)

// NERD017 SPEC011: every layer carries the licence its class declared, and
// the manifest carries the subject's. Nothing is inferred and nothing is
// refused -- an undeclared layer is simply unannotated.

// licensedZips is fixtureZips with declarations: the subject a shorthand,
// clickhouse the object form (with an exclude that the bytes, already a
// zip, are not subject to), otel nothing at all.
func licensedZips(t *testing.T) map[string][]byte {
	t.Helper()
	return map[string][]byte{
		"hmd-stack-obs": zipOf(t, "hmd-stack-obs", "0.1", map[string]string{
			"neuronsphere.lock":       "version = 1\n",
			"meta-data/manifest.json": `{"name":"hmd-stack-obs","license":"MIT"}`,
		}),
		"hmd-inf-otel": zipOf(t, "hmd-inf-otel", "0.1", nil),
		"hmd-inf-clickhouse": zipOf(t, "hmd-inf-clickhouse", "0.3", map[string]string{
			"meta-data/manifest.json": `{"name":"hmd-inf-clickhouse","license":{"spdx":"BUSL-1.1","exclude":["src/python"]}}`,
			"src/python/app.py":       "travels as the author's bytes",
		}),
	}
}

func TestBuildAnnotatesEachLayerWithItsDeclaredLicence(t *testing.T) {
	t.Parallel()
	ref, _ := oci.ParseRef("ghcr.io/hmdlabs/stacks/obs")
	m, _, _, err := Build(ShortName(ref), "0.1.0", fixtureLock(), licensedZips(t))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, l := range m.Layers {
		got[l.Annotations[AnnotationClass]] = l.Annotations[AnnotationLicenses]
		if l.Annotations[AnnotationClass] == "hmd-inf-otel" {
			if v, ok := l.Annotations[AnnotationLicenses]; ok {
				t.Errorf("an undeclared layer must carry no %s, got %q", AnnotationLicenses, v)
			}
		}
	}
	if got["hmd-stack-obs"] != "MIT" || got["hmd-inf-clickhouse"] != "BUSL-1.1" || got["hmd-inf-otel"] != "" {
		t.Errorf("layer licences = %v", got)
	}
	if m.Annotations[AnnotationLicenses] != "MIT" {
		t.Errorf("the manifest carries the subject's licence, got %q", m.Annotations[AnnotationLicenses])
	}

	// A subject that declares nothing leaves the manifest unannotated too:
	// a stack is not Apache, or anything else, by fiat.
	plain, _, _, err := Build(ShortName(ref), "0.1.0", fixtureLock(), fixtureZips(t))
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := plain.Annotations[AnnotationLicenses]; ok {
		t.Errorf("undeclared subject gave manifest licence %q", v)
	}

	// Bytes that are not a zip cannot be read for a declaration.
	bad := licensedZips(t)
	bad["hmd-inf-otel"] = []byte("not a zip")
	if _, _, _, err := Build(ShortName(ref), "0.1.0", fixtureLock(), bad); err == nil || !strings.Contains(err.Error(), "hmd-inf-otel") {
		t.Errorf("non-zip layer: %v", err)
	}
}

func TestReadAndInstallSurfaceTheLayerLicence(t *testing.T) {
	t.Parallel()
	reg := ocitest.New(t, ocitest.NoChallenge())
	ref := refTo(t, reg, "hmdlabs/stacks/obs")
	m, blobs, _, err := Build(ShortName(ref), "0.1.0", fixtureLock(), licensedZips(t))
	if err != nil {
		t.Fatal(err)
	}
	reg.Put("hmdlabs/stacks/obs", "0.1.0", m, blobs)
	b, err := oci.New(oci.Credential{}).Fetch(context.Background(), ref.WithTag("0.1.0"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := Read(b)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, l := range s.Layers {
		got[l.Class] = l.Licence
	}
	if got["hmd-stack-obs"] != "MIT" || got["hmd-inf-clickhouse"] != "BUSL-1.1" || got["hmd-inf-otel"] != "" {
		t.Errorf("Read licences = %v", got)
	}

	var lines []string
	if _, err := Install(context.Background(), t.TempDir(), oci.New(oci.Credential{}), ref.WithTag("0.1.0"), func(s string) { lines = append(lines, s) }); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "hmd-stack-obs@0.1.0") || !strings.Contains(joined, "MIT") || !strings.Contains(joined, "BUSL-1.1") {
		t.Errorf("progress does not show the declared licences:\n%s", joined)
	}
}

func TestRetagKeepsTheLicenceAnnotation(t *testing.T) {
	t.Parallel()
	ref, _ := oci.ParseRef("ghcr.io/hmdlabs/stacks/obs")
	m, blobs, _, err := Build(ShortName(ref), "0.1.0", fixtureLock(), licensedZips(t))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "build", "stack")
	if _, err := WriteLayout(dir, m, blobs, "0.1.0"); err != nil {
		t.Fatal(err)
	}
	got, gotBlobs, _, err := ReadLayout(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := Retag(&got, gotBlobs, "0.2.0"); err != nil {
		t.Fatal(err)
	}
	if got.Annotations[AnnotationLicenses] != "MIT" || got.Layers[0].Annotations[AnnotationLicenses] != "MIT" {
		t.Errorf("retag lost the licence: manifest %v, subject %v", got.Annotations, got.Layers[0].Annotations)
	}
	if got.Layers[0].Annotations[AnnotationVersion] != "0.2.0" {
		t.Errorf("retag did not move the version: %v", got.Layers[0].Annotations)
	}
}
