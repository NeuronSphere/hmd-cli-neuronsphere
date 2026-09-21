// Package classart publishes and fetches one RepoClass build zip as an OCI
// artifact. NERD016 SPEC009.
//
// A stack (NERD017) bundles many zips; this is the single-class form a
// third party publishes for each of their RepoClasses so a lock entry can
// name a free source. The layer annotations are NERD017 SPEC002's, so a
// stack builder reads both shapes with one rule.
package classart

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/artifact"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/oci"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/stack"
)

// Media types of the RepoClass artifact.
const (
	ArtifactType      = "application/vnd.neuronsphere.repoclass.v1+json"
	ManifestMediaType = "application/vnd.neuronsphere.bacon.v1+json"
)

// ErrNotAClass is an artifact of another type.
var ErrNotAClass = errors.New("not a RepoClass artifact")

// Build assembles the artifact for one class: the BACON manifest as config,
// the zip as the one layer.
func Build(class, version string, manifestJSON, zip []byte) (v1.Manifest, map[digest.Digest][]byte) {
	cfg := digest.FromBytes(manifestJSON)
	z := digest.FromBytes(zip)
	blobs := map[digest.Digest][]byte{cfg: manifestJSON, z: zip}
	m := v1.Manifest{
		Versioned:    oci.Versioned(),
		MediaType:    v1.MediaTypeImageManifest,
		ArtifactType: ArtifactType,
		Config:       v1.Descriptor{MediaType: ManifestMediaType, Digest: cfg, Size: int64(len(manifestJSON))},
		Layers: []v1.Descriptor{{
			MediaType: stack.BuildMediaType, Digest: z, Size: int64(len(zip)),
			Annotations: map[string]string{
				stack.AnnotationClass: class, stack.AnnotationVersion: version, stack.AnnotationItemType: stack.ItemType,
				stack.AnnotationTitle: fmt.Sprintf("%s_%s_%s.zip", class, version, stack.ItemType),
			},
		}},
		Annotations: map[string]string{
			"org.opencontainers.image.title":   class,
			"org.opencontainers.image.version": version,
		},
	}
	return m, blobs
}

// FromDir zips a repository tree (as artifact.Zip does) and reads its class
// and version from meta-data. A zip file is read as-is and its meta-data
// inspected inside.
func FromDir(path string) (class, version string, manifestJSON, zip []byte, err error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", "", nil, nil, err
	}
	if info.IsDir() {
		manifestJSON, err = os.ReadFile(filepath.Join(path, "meta-data", "manifest.json"))
		if err != nil {
			return "", "", nil, nil, fmt.Errorf("%s is not a RepoClass tree: %w", path, err)
		}
		v, err := os.ReadFile(filepath.Join(path, "meta-data", "VERSION"))
		if err != nil {
			return "", "", nil, nil, fmt.Errorf("%s is not a RepoClass tree: %w", path, err)
		}
		zip, err = artifact.Zip(path)
		if err != nil {
			return "", "", nil, nil, err
		}
		class, err = artifact.NameOf(manifestJSON)
		if err != nil {
			return "", "", nil, nil, err
		}
		return class, strings.TrimSpace(string(v)), manifestJSON, zip, nil
	}
	zip, err = os.ReadFile(path)
	if err != nil {
		return "", "", nil, nil, err
	}
	manifestJSON, version, err = artifact.Inspect(zip)
	if err != nil {
		return "", "", nil, nil, err
	}
	class, err = artifact.NameOf(manifestJSON)
	if err != nil {
		return "", "", nil, nil, err
	}
	return class, version, manifestJSON, zip, nil
}

// Fetched is one class's zip from a registry.
type Fetched struct {
	Class, Version string
	Zip            []byte
	Digest         digest.Digest // of the zip
	Manifest       digest.Digest // of the artifact manifest
}

// Fetch retrieves a class artifact. ref must name a version.
func Fetch(ctx context.Context, f oci.Fetcher, ref oci.Ref) (*Fetched, error) {
	b, err := f.Fetch(ctx, ref)
	if err != nil {
		return nil, err
	}
	if b.ArtifactType != ArtifactType && b.ConfigMediaType != ManifestMediaType {
		return nil, fmt.Errorf("%w: %s has artifactType %q", ErrNotAClass, ref, b.ArtifactType)
	}
	if len(b.Layers) != 1 {
		return nil, fmt.Errorf("%s: a RepoClass artifact has one layer, this has %d", ref, len(b.Layers))
	}
	layer := b.Layers[0]
	var buf bytes.Buffer
	if err := f.Blob(ctx, ref, layer, &buf); err != nil {
		return nil, err
	}
	return &Fetched{
		Class:    layer.Annotations[stack.AnnotationClass],
		Version:  layer.Annotations[stack.AnnotationVersion],
		Zip:      buf.Bytes(),
		Digest:   layer.Digest,
		Manifest: b.Digest,
	}, nil
}

// Push publishes a class artifact under ref's tag.
func Push(ctx context.Context, c *oci.Client, ref oci.Ref, m v1.Manifest, blobs map[digest.Digest][]byte) (digest.Digest, error) {
	return stack.Push(ctx, c, ref, m, blobs)
}
