package stack

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/oci"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/oci/ocitest"
)

// NERD019 SPEC001: stack build writes an OCI image layout, deterministically.

func TestLayoutRoundTripsAndIsDeterministic(t *testing.T) {
	t.Parallel()
	ref, _ := oci.ParseRef("ghcr.io/hmdlabs/stacks/obs")
	m, blobs, _, err := Build(ShortName(ref), "0.1.0", fixtureLock(), fixtureZips(t))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "build", "stack")
	d1, err := WriteLayout(dir, m, blobs, "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"oci-layout", "index.json", filepath.Join("blobs", "sha256", d1.Encoded())} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s missing: %v", f, err)
		}
	}
	// Rewriting produces the same bytes and the same digest.
	before, _ := os.ReadFile(filepath.Join(dir, "index.json"))
	d2, err := WriteLayout(dir, m, blobs, "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "index.json"))
	if d1 != d2 || !bytes.Equal(before, after) {
		t.Error("layout is not deterministic")
	}

	got, gotBlobs, tag, err := ReadLayout(dir)
	if err != nil {
		t.Fatal(err)
	}
	if tag != "0.1.0" || got.ArtifactType != ArtifactType || len(got.Layers) != 3 || len(gotBlobs) != len(blobs) {
		t.Errorf("ReadLayout = tag %q, %+v, %d blobs", tag, got, len(gotBlobs))
	}
	for d, b := range blobs {
		if !bytes.Equal(gotBlobs[d], b) {
			t.Errorf("blob %s differs after round trip", d)
		}
	}
}

func TestReadLayoutRefusals(t *testing.T) {
	t.Parallel()
	if _, _, _, err := ReadLayout(t.TempDir()); err == nil {
		t.Error("empty dir accepted")
	}
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "oci-layout"), []byte(`{"imageLayoutVersion":"1.0.0"}`), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "index.json"), []byte(`{"schemaVersion":2,"manifests":[]}`), 0o644)
	if _, _, _, err := ReadLayout(dir); err == nil {
		t.Error("index with no manifest accepted")
	}
}

func TestPushLayoutRoundTripsThroughARegistry(t *testing.T) {
	t.Parallel()
	reg := ocitest.New(t, ocitest.RequireToken("ci", "pat"))
	ref, _ := oci.ParseRef(reg.Host() + "/acme/stacks/obs")
	m, blobs, _, err := Build(ShortName(ref), "0.1.0", fixtureLock(), fixtureZips(t))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	built, err := WriteLayout(dir, m, blobs, "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	c := oci.New(oci.Credential{Username: "ci", Secret: "pat", Source: "--token"})
	// Same tag as built: the bytes and the digest are the layout's.
	pushed, err := PushLayout(context.Background(), c, ref.WithTag("0.1.0"), dir)
	if err != nil {
		t.Fatal(err)
	}
	if pushed != built {
		t.Errorf("pushed %s, built %s", pushed, built)
	}
	inst, err := Install(context.Background(), t.TempDir(), c, ref.WithTag("0.1.0"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if inst.Stack.Digest != built || inst.Stack.Version != "0.1.0" {
		t.Errorf("installed = %s %s", inst.Stack.Digest, inst.Stack.Version)
	}
	// A different tag (--bump chose it): the manifest is retagged, the zips
	// are not, and the consumer sees the pushed version throughout.
	bumped, err := PushLayout(context.Background(), c, ref.WithTag("0.2.0"), dir)
	if err != nil {
		t.Fatal(err)
	}
	if bumped == built {
		t.Error("a retagged manifest must have a new digest")
	}
	inst, err = Install(context.Background(), t.TempDir(), c, ref.WithTag("0.2.0"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if inst.Stack.Version != "0.2.0" || inst.Dir == "" || !strings.HasSuffix(inst.Dir, "hmd-stack-obs@0.2.0") {
		t.Errorf("retagged install = version %q dir %s", inst.Stack.Version, inst.Dir)
	}
	for _, l := range inst.Stack.Layers {
		if l.Subject && l.Version != "0.2.0" {
			t.Errorf("subject layer version = %s", l.Version)
		}
	}
}

func TestNextVersion(t *testing.T) {
	t.Parallel()
	cases := []struct {
		tags    []string
		version string
		want    string
	}{
		{nil, "0.1", "0.1.0"},
		{nil, "0.1.4", "0.1.4"},
		{[]string{"0.1.3", "0.1.2"}, "0.1", "0.1.4"},
		{[]string{"0.1.3"}, "0.2", "0.2.0"},   // VERSION moved ahead
		{[]string{"0.3.9"}, "0.1", "0.3.10"},  // VERSION behind: the registry wins
		{[]string{"1.0.0"}, "1.0.7", "1.0.7"}, // explicit three-part VERSION, unpublished
	}
	for _, tc := range cases {
		got, err := NextVersion(tc.tags, tc.version)
		if err != nil {
			t.Errorf("NextVersion(%v, %q): %v", tc.tags, tc.version, err)
			continue
		}
		if got != tc.want {
			t.Errorf("NextVersion(%v, %q) = %q, want %q", tc.tags, tc.version, got, tc.want)
		}
	}
	if _, err := NextVersion([]string{"1.0.7"}, "1.0.7"); err == nil {
		t.Error("an already-published explicit version must be refused")
	}
	if _, err := NextVersion(nil, "abc"); err == nil {
		t.Error("a non-version VERSION must be refused")
	}
}
