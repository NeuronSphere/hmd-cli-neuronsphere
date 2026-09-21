package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/oci"
)

// archiveRe matches GoReleaser's default archive name: anything, then
// _<os>_<arch>.tar.gz.
var archiveRe = regexp.MustCompile(`_([a-z0-9]+)_([a-z0-9]+)\.tar\.gz$`)

// Build assembles the artifact from a directory holding plugin.json and the
// per-platform tarballs. The descriptor's platforms table is filled from the
// tarballs found; a descriptor that lists one there is no tarball for is
// refused. SPEC002 and SPEC003.
func Build(dir string) (v1.Manifest, map[digest.Digest][]byte, Descriptor, error) {
	raw, err := os.ReadFile(filepath.Join(dir, DescriptorFile))
	if err != nil {
		return v1.Manifest{}, nil, Descriptor{}, fmt.Errorf("reading %s: %w", DescriptorFile, err)
	}
	var d Descriptor
	if err := json.Unmarshal(raw, &d); err != nil {
		return v1.Manifest{}, nil, Descriptor{}, fmt.Errorf("%s: %w", DescriptorFile, err)
	}
	declared := d.Platforms
	d.Platforms = map[string]Platform{}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return v1.Manifest{}, nil, Descriptor{}, err
	}
	blobs := map[digest.Digest][]byte{}
	var layers []v1.Descriptor
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		m := archiveRe.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		goos, goarch := m[1], m[2]
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return v1.Manifest{}, nil, Descriptor{}, err
		}
		dg := digest.FromBytes(data)
		blobs[dg] = data
		layers = append(layers, v1.Descriptor{
			MediaType: BinaryMediaType, Digest: dg, Size: int64(len(data)),
			Annotations: map[string]string{AnnotationOS: goos, AnnotationArch: goarch, AnnotationTitle: name},
		})
		d.Platforms[goos+"_"+goarch] = Platform{Digest: dg.String(), Size: int64(len(data))}
	}
	if len(layers) == 0 {
		return v1.Manifest{}, nil, Descriptor{}, fmt.Errorf("%s holds no <name>_<version>_<os>_<arch>.tar.gz archives", dir)
	}
	for key := range declared {
		if _, ok := d.Platforms[key]; !ok {
			return v1.Manifest{}, nil, Descriptor{}, fmt.Errorf("%s lists platform %s but no archive for it is in %s", DescriptorFile, key, dir)
		}
	}
	d, err = ParseDescriptor(mustJSON(d))
	if err != nil {
		return v1.Manifest{}, nil, Descriptor{}, err
	}

	config := mustJSON(d)
	cfgDigest := digest.FromBytes(config)
	blobs[cfgDigest] = config
	manifest := v1.Manifest{
		Versioned:    oci.Versioned(),
		MediaType:    v1.MediaTypeImageManifest,
		ArtifactType: ArtifactType,
		Config:       v1.Descriptor{MediaType: ArtifactType, Digest: cfgDigest, Size: int64(len(config))},
		Layers:       layers,
		Annotations: map[string]string{
			"org.opencontainers.image.version": d.Version,
			"org.opencontainers.image.title":   BinaryName(d.Name),
		},
	}
	if d.Summary != "" {
		manifest.Annotations["org.opencontainers.image.description"] = d.Summary
	}
	return manifest, blobs, d, nil
}

// Publish builds from dir and pushes to ref, tagging with the descriptor's
// version when ref names none. Returns the manifest digest and the tag.
func Publish(ctx context.Context, c *oci.Client, ref oci.Ref, dir string) (digest.Digest, oci.Ref, error) {
	m, blobs, d, err := Build(dir)
	if err != nil {
		return "", ref, err
	}
	if ref.Tag == "" {
		ref = ref.WithTag(d.Version)
	}
	if _, err := c.PushBlob(ctx, ref, m.Config.MediaType, bytesReader(blobs[m.Config.Digest]), m.Config.Size); err != nil {
		return "", ref, err
	}
	for _, l := range m.Layers {
		if _, err := c.PushBlob(ctx, ref, l.MediaType, bytesReader(blobs[l.Digest]), l.Size); err != nil {
			return "", ref, err
		}
	}
	dg, err := c.PushManifest(ctx, ref, m)
	return dg, ref, err
}

func mustJSON(v any) []byte {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic(err)
	}
	return data
}
