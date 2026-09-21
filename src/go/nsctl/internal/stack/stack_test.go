package stack

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/artifact"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/lock"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/oci"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/oci/ocitest"
)

// zipOf builds a repo-tree zip: meta-data/manifest.json and VERSION are what
// artifact.Store requires.
func zipOf(t *testing.T, class, version string, extra map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	files := map[string]string{
		"meta-data/manifest.json": `{"name":"` + class + `"}`,
		"meta-data/VERSION":       version,
	}
	for k, v := range extra {
		files[k] = v
	}
	for name, content := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func fixtureLock() *lock.Lock {
	return &lock.Lock{Version: lock.Version, RepoClassName: "hmd-stack-obs", GeneratedFrom: "pins", Resolved: []lock.Entry{
		{RepoClassName: "hmd-inf-otel", Version: "0.1.5", Profiles: []string{}, ContentPath: "repository:/hmd-inf-otel/0.1.5/hmd-inf-otel_0.1.5_build.zip"},
		{RepoClassName: "hmd-inf-clickhouse", Version: "0.3.0", Profiles: []string{"full"}, ContentPath: "repository:/hmd-inf-clickhouse/0.3.0/hmd-inf-clickhouse_0.3.0_build.zip"},
	}}
}

func fixtureZips(t *testing.T) map[string][]byte {
	return map[string][]byte{
		"hmd-stack-obs":      zipOf(t, "hmd-stack-obs", "0.1", map[string]string{"neuronsphere.lock": "version = 1\n"}),
		"hmd-inf-otel":       zipOf(t, "hmd-inf-otel", "0.1", nil),
		"hmd-inf-clickhouse": zipOf(t, "hmd-inf-clickhouse", "0.3", nil),
	}
}

func refTo(t *testing.T, reg *ocitest.Registry, s string) oci.Ref {
	t.Helper()
	r, err := oci.ParseRef(reg.Host() + "/" + s)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestBuildIsDeterministicAndFillsDigests(t *testing.T) {
	t.Parallel()
	ref, _ := oci.ParseRef("ghcr.io/hmdlabs/stacks/obs")
	zips := fixtureZips(t)
	m1, blobs1, pinned, err := Build(ref, "0.1.0", fixtureLock(), zips)
	if err != nil {
		t.Fatal(err)
	}
	m2, _, _, err := Build(ref, "0.1.0", fixtureLock(), zips)
	if err != nil {
		t.Fatal(err)
	}
	if digest.FromBytes(blobs1[m1.Config.Digest]) != m2.Config.Digest || len(m1.Layers) != 3 {
		t.Errorf("Build is not deterministic or wrong shape: %+v vs %+v", m1, m2)
	}
	if m1.ArtifactType != ArtifactType || m1.Config.MediaType != LockMediaType {
		t.Errorf("types = %q / %q", m1.ArtifactType, m1.Config.MediaType)
	}
	if m1.Layers[0].Annotations[AnnotationClass] != "hmd-stack-obs" || m1.Layers[0].Annotations[AnnotationVersion] != "0.1.0" {
		t.Errorf("subject layer first: %+v", m1.Layers[0].Annotations)
	}
	if m1.Annotations[AnnotationStackName] != "obs" || m1.Annotations[AnnotationStackVersion] != "0.1.0" {
		t.Errorf("annotations = %v", m1.Annotations)
	}
	for _, e := range pinned.Resolved {
		if !strings.HasPrefix(e.Digest, "sha256:") {
			t.Errorf("%s: digest not filled", e.RepoClassName)
		}
	}
	// The config blob is the pinned lock.
	back, err := lock.Parse(blobs1[m1.Config.Digest])
	if err != nil {
		t.Fatal(err)
	}
	if e, _ := back.Entry("hmd-inf-otel"); e.Digest == "" {
		t.Error("config lock lacks digests")
	}
}

func TestBuildRefusals(t *testing.T) {
	t.Parallel()
	ref, _ := oci.ParseRef("ghcr.io/hmdlabs/stacks/obs")
	zips := fixtureZips(t)
	delete(zips, "hmd-inf-clickhouse")
	if _, _, _, err := Build(ref, "0.1.0", fixtureLock(), zips); err == nil || !strings.Contains(err.Error(), "hmd-inf-clickhouse@0.3.0") {
		t.Errorf("missing companion: %v", err)
	}
	zips = fixtureZips(t)
	zips["hmd-inf-extra"] = zipOf(t, "hmd-inf-extra", "1", nil)
	if _, _, _, err := Build(ref, "0.1.0", fixtureLock(), zips); err == nil || !strings.Contains(err.Error(), "hmd-inf-extra") {
		t.Errorf("unpinned zip: %v", err)
	}
	// A lock that already pins a digest the bytes disagree with.
	l := fixtureLock()
	l.SetDigest("hmd-inf-otel", "sha256:"+strings.Repeat("0", 64))
	if _, _, _, err := Build(ref, "0.1.0", l, fixtureZips(t)); err == nil || !strings.Contains(err.Error(), "disagree") {
		t.Errorf("digest disagreement: %v", err)
	}
}

func TestReadPairsLayersWithTheLock(t *testing.T) {
	t.Parallel()
	reg := ocitest.New(t, ocitest.NoChallenge())
	ref := refTo(t, reg, "hmdlabs/stacks/obs")
	m, blobs, _, err := Build(ref, "0.1.0", fixtureLock(), fixtureZips(t))
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
	if s.Name != "obs" || s.Version != "0.1.0" || s.Class != "hmd-stack-obs" || len(s.Layers) != 3 || !s.Layers[0].Subject {
		t.Errorf("stack = %+v", s)
	}

	// A layer the lock does not pin.
	extra := m
	extra.Layers = append(append([]v1.Descriptor(nil), m.Layers...), m.Layers[1])
	extra.Layers[3].Annotations = map[string]string{AnnotationClass: "hmd-inf-rogue", AnnotationVersion: "9"}
	reg.Put("hmdlabs/stacks/rogue", "0.1.0", extra, blobs)
	b, _ = oci.New(oci.Credential{}).Fetch(context.Background(), refTo(t, reg, "hmdlabs/stacks/rogue:0.1.0"))
	if _, err := Read(b); err == nil || !strings.Contains(err.Error(), "hmd-inf-rogue") {
		t.Errorf("unpinned layer: %v", err)
	}

	// A pinned class with no layer.
	short := m
	short.Layers = m.Layers[:2]
	reg.Put("hmdlabs/stacks/short", "0.1.0", short, blobs)
	b, _ = oci.New(oci.Credential{}).Fetch(context.Background(), refTo(t, reg, "hmdlabs/stacks/short:0.1.0"))
	if _, err := Read(b); err == nil || !strings.Contains(err.Error(), "carries no layer") {
		t.Errorf("missing layer: %v", err)
	}

	// The wrong kind of artifact.
	other := m
	other.ArtifactType, other.Config.MediaType = "application/vnd.neuronsphere.plugin.v1+json", "application/vnd.neuronsphere.plugin.v1+json"
	reg.Put("hmdlabs/stacks/plugin", "0.1.0", other, blobs)
	b, _ = oci.New(oci.Credential{}).Fetch(context.Background(), refTo(t, reg, "hmdlabs/stacks/plugin:0.1.0"))
	if _, err := Read(b); !errors.Is(err, ErrNotAStack) {
		t.Errorf("plugin as stack: %v", err)
	}
}

func TestInstallFillsTheCacheAndWritesTheLock(t *testing.T) {
	t.Parallel()
	reg := ocitest.New(t)
	ref := refTo(t, reg, "hmdlabs/stacks/obs")
	m, blobs, _, err := Build(ref, "0.1.0", fixtureLock(), fixtureZips(t))
	if err != nil {
		t.Fatal(err)
	}
	reg.Put("hmdlabs/stacks/obs", "0.1.0", m, blobs)

	home := t.TempDir()
	var lines []string
	inst, err := Install(context.Background(), home, oci.New(oci.Credential{}), ref.WithTag("0.1.0"), func(s string) { lines = append(lines, s) })
	if err != nil {
		t.Fatal(err)
	}
	for _, class := range []string{"hmd-stack-obs", "hmd-inf-otel", "hmd-inf-clickhouse"} {
		version := map[string]string{"hmd-stack-obs": "0.1.0", "hmd-inf-otel": "0.1.5", "hmd-inf-clickhouse": "0.3.0"}[class]
		if !artifact.Cached(home, class, version) {
			t.Errorf("%s@%s not cached", class, version)
		}
		if d, ok := artifact.Digest(home, class, version); !ok || d == "" {
			t.Errorf("%s@%s has no digest sidecar", class, version)
		}
		if _, ok := inst.Zips[class]; !ok {
			t.Errorf("%s zip not returned", class)
		}
	}
	if inst.Dir != artifact.Dir(home, "hmd-stack-obs", "0.1.0") {
		t.Errorf("Dir = %s", inst.Dir)
	}
	data, err := os.ReadFile(filepath.Join(inst.Dir, lock.FileName))
	if err != nil {
		t.Fatal(err)
	}
	l, err := lock.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if e, _ := l.Entry("hmd-inf-otel"); e.Digest == "" {
		t.Error("the lock written into the tree must carry digests")
	}
	if len(lines) != 3 {
		t.Errorf("progress lines = %v", lines)
	}
}

func TestPushThenInstallRoundTrip(t *testing.T) {
	t.Parallel()
	reg := ocitest.New(t, ocitest.RequireToken("ci", "pat"))
	ref := refTo(t, reg, "acme/stacks/obs:0.1.0")
	m, blobs, _, err := Build(ref, "0.1.0", fixtureLock(), fixtureZips(t))
	if err != nil {
		t.Fatal(err)
	}
	c := oci.New(oci.Credential{Username: "ci", Secret: "pat", Source: "--token"})
	d, err := Push(context.Background(), c, ref, m, blobs)
	if err != nil {
		t.Fatal(err)
	}
	inst, err := Install(context.Background(), t.TempDir(), oci.New(oci.Credential{Username: "ci", Secret: "pat"}), ref, nil)
	if err != nil {
		t.Fatal(err)
	}
	if inst.Stack.Digest != d {
		t.Errorf("round trip digest %s != %s", inst.Stack.Digest, d)
	}
}

func TestVersionsKeyAndShortName(t *testing.T) {
	t.Parallel()
	ref, _ := oci.ParseRef("ghcr.io/hmdlabs/stacks/obs")
	if VersionsKey(ref) != "stack~ghcr.io~hmdlabs~stacks~obs" {
		t.Errorf("VersionsKey = %q", VersionsKey(ref))
	}
	if ShortName(ref) != "obs" {
		t.Errorf("ShortName = %q", ShortName(ref))
	}
}
