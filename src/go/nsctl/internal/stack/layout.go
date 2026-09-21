package stack

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/lock"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/oci"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/versionspec"
)

// WorkflowTemplate is the CI workflow `stack init` writes (NERD019 SPEC007);
// {{.Name}} is the stack's short name.
//
//go:embed templates/stack.yml
var WorkflowTemplate string

// DefaultLayoutDir is where `stack build` writes, under the repository.
// artifact.SkipDirs already excludes build/, so the subject's zip never
// carries its own output.
const DefaultLayoutDir = "build/stack"

// WriteLayout writes a manifest and its blobs as an OCI image layout under
// dir, tagged in index.json, and returns the manifest digest. Writing the
// same inputs twice yields the same files. NERD019 SPEC001.
func WriteLayout(dir string, m v1.Manifest, blobs map[digest.Digest][]byte, tag string) (digest.Digest, error) {
	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	blobDir := filepath.Join(dir, "blobs", "sha256")
	if err := os.MkdirAll(blobDir, 0o755); err != nil {
		return "", err
	}
	if m.MediaType == "" {
		m.MediaType = v1.MediaTypeImageManifest
	}
	if m.SchemaVersion == 0 {
		m.Versioned = oci.Versioned()
	}
	body, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	md := digest.FromBytes(body)
	write := func(d digest.Digest, data []byte) error {
		if d.Algorithm() != digest.SHA256 {
			return fmt.Errorf("layout: %s is not sha256", d)
		}
		return os.WriteFile(filepath.Join(blobDir, d.Encoded()), data, 0o644)
	}
	for _, desc := range append([]v1.Descriptor{m.Config}, m.Layers...) {
		data, ok := blobs[desc.Digest]
		if !ok {
			return "", fmt.Errorf("layout: no bytes for %s", desc.Digest)
		}
		if err := write(desc.Digest, data); err != nil {
			return "", err
		}
	}
	if err := write(md, body); err != nil {
		return "", err
	}
	index := v1.Index{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: v1.MediaTypeImageIndex,
		Manifests: []v1.Descriptor{{
			MediaType:    m.MediaType,
			Digest:       md,
			Size:         int64(len(body)),
			ArtifactType: m.ArtifactType,
			Annotations:  map[string]string{v1.AnnotationRefName: tag},
		}},
	}
	indexBody, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "index.json"), indexBody, 0o644); err != nil {
		return "", err
	}
	layout, _ := json.Marshal(v1.ImageLayout{Version: v1.ImageLayoutVersion})
	if err := os.WriteFile(filepath.Join(dir, v1.ImageLayoutFile), layout, 0o644); err != nil {
		return "", err
	}
	return md, nil
}

// ReadLayout reads back what WriteLayout wrote: the one manifest, its blobs
// (verified by digest), and the tag from index.json.
func ReadLayout(dir string) (v1.Manifest, map[digest.Digest][]byte, string, error) {
	fail := func(err error) (v1.Manifest, map[digest.Digest][]byte, string, error) {
		return v1.Manifest{}, nil, "", err
	}
	indexBody, err := os.ReadFile(filepath.Join(dir, "index.json"))
	if err != nil {
		return fail(fmt.Errorf("%s is not an OCI image layout: %w", dir, err))
	}
	var index v1.Index
	if err := json.Unmarshal(indexBody, &index); err != nil {
		return fail(fmt.Errorf("%s/index.json: %w", dir, err))
	}
	if len(index.Manifests) != 1 {
		return fail(fmt.Errorf("%s/index.json names %d manifests; a stack layout holds exactly one", dir, len(index.Manifests)))
	}
	desc := index.Manifests[0]
	read := func(d digest.Digest) ([]byte, error) {
		data, err := os.ReadFile(filepath.Join(dir, "blobs", d.Algorithm().String(), d.Encoded()))
		if err != nil {
			return nil, err
		}
		if got := digest.FromBytes(data); got != d {
			return nil, fmt.Errorf("blob %s has digest %s; the layout is corrupt", d, got)
		}
		return data, nil
	}
	body, err := read(desc.Digest)
	if err != nil {
		return fail(err)
	}
	var m v1.Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return fail(fmt.Errorf("manifest %s: %w", desc.Digest, err))
	}
	blobs := map[digest.Digest][]byte{}
	for _, d := range append([]v1.Descriptor{m.Config}, m.Layers...) {
		data, err := read(d.Digest)
		if err != nil {
			return fail(err)
		}
		blobs[d.Digest] = data
	}
	return m, blobs, desc.Annotations[v1.AnnotationRefName], nil
}

// PushLayout pushes a built layout to ref, which must name a tag.
//
// When the tag differs from the one the layout was built with (--bump chose
// it at push time), the manifest's version annotations and the subject
// layer's version are rewritten to the tag before pushing: the tag is the
// addressable truth, and a consumer's `Read` cross-checks the two. The zips
// are untouched, so only the manifest digest changes, and that is the digest
// returned. Retag reports whether that happened.
func PushLayout(ctx context.Context, c *oci.Client, ref oci.Ref, dir string) (digest.Digest, error) {
	m, blobs, tag, err := ReadLayout(dir)
	if err != nil {
		return "", err
	}
	if ref.Tag == "" {
		return "", fmt.Errorf("push needs a tag: %s", ref)
	}
	if tag != ref.Tag {
		if err := Retag(&m, blobs, ref.Tag); err != nil {
			return "", err
		}
	}
	for _, d := range append([]v1.Descriptor{m.Config}, m.Layers...) {
		if _, err := c.PushBlob(ctx, ref, d.MediaType, bytes.NewReader(blobs[d.Digest]), d.Size); err != nil {
			return "", err
		}
	}
	return c.PushManifest(ctx, ref, m)
}

// ErrPublished is a version the registry already has.
var ErrPublished = errors.New("already published")

// NextVersion is --bump (NERD019 SPEC003): the next patch of the newest
// published tag, unless VERSION is an unpublished explicit version or a
// newer major.minor, in which case VERSION (with .0 appended to a two-part
// one). An explicit VERSION that is already published is refused.
func NextVersion(published []string, version string) (string, error) {
	version = strings.TrimSpace(version)
	if !versionspec.IsVersion(version) {
		return "", fmt.Errorf("%q is not a version", version)
	}
	parts := strings.Split(version, ".")
	explicit := len(parts) >= 3
	base := strings.Join(parts[:min(2, len(parts))], ".")

	var tags []string
	for _, t := range published {
		if versionspec.IsVersion(t) {
			tags = append(tags, t)
		}
	}
	versionspec.Sort(tags)
	if explicit {
		for _, t := range tags {
			if t == version {
				return "", fmt.Errorf("%w: %s; bump meta-data/VERSION or drop it to a major.minor and let --bump count", ErrPublished, version)
			}
		}
		return version, nil
	}
	if len(tags) == 0 {
		return base + ".0", nil
	}
	newest := tags[0]
	np := strings.Split(newest, ".")
	newestBase := strings.Join(np[:min(2, len(np))], ".")
	if versionspec.Compare(base, newestBase) > 0 {
		return base + ".0", nil
	}
	patch := 0
	if len(np) >= 3 {
		patch, _ = strconv.Atoi(np[2])
	}
	return fmt.Sprintf("%s.%d", newestBase, patch+1), nil
}

// Retag rewrites a built manifest to a new stack version: the stack
// annotations and the subject layer's version annotation. The subject is the
// layer whose class is the lock's repo_class_name.
func Retag(m *v1.Manifest, blobs map[digest.Digest][]byte, version string) error {
	if !versionspec.IsVersion(version) {
		return fmt.Errorf("%q is not a version", version)
	}
	config, ok := blobs[m.Config.Digest]
	if !ok {
		return errors.New("retag: the layout holds no config blob")
	}
	l, err := lock.Parse(config)
	if err != nil {
		return fmt.Errorf("retag: %w", err)
	}
	if m.Annotations == nil {
		m.Annotations = map[string]string{}
	}
	m.Annotations[AnnotationStackVersion] = version
	m.Annotations["org.opencontainers.image.version"] = version
	for i := range m.Layers {
		if m.Layers[i].Annotations[AnnotationClass] == l.RepoClassName {
			m.Layers[i].Annotations[AnnotationVersion] = version
			m.Layers[i].Annotations[AnnotationTitle] = fmt.Sprintf("%s_%s_%s.zip", l.RepoClassName, version, ItemType)
		}
	}
	return nil
}
