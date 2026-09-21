// Package stack reads, builds and installs stacks. NERD017.
//
// A stack is a RepoClass whose repository carries a `local` section and a
// neuronsphere.lock, published as one OCI artifact: the lock as the config
// blob and one layer per build zip -- every companion the lock pins, plus the
// stack RepoClass itself. Installing one is fetching, verifying, and unpacking
// every zip into the artifact cache, after which the NERD010 planner treats
// the cached stack tree exactly as it would a checkout.
//
// Nothing here declares anything in an environment; that is cmd's, through
// the --from-repo planner. Nothing here is on the resolve path either: this
// package imports internal/oci and is imported only by verbs.
package stack

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/artifact"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/lock"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/oci"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/repoclass"
)

// Media types and annotation keys of the stack artifact. SPEC002.
const (
	ArtifactType   = "application/vnd.neuronsphere.stack.v1+toml"
	LockMediaType  = "application/vnd.neuronsphere.lock.v1+toml"
	BuildMediaType = "application/vnd.neuronsphere.repoclass.build.v1+zip"

	AnnotationClass    = "io.neuronsphere.repoclass.name"
	AnnotationVersion  = "io.neuronsphere.repoclass.version"
	AnnotationItemType = "io.neuronsphere.repoclass.item_type"
	AnnotationTitle    = "org.opencontainers.image.title"

	AnnotationStackName    = "io.neuronsphere.stack.name"
	AnnotationStackVersion = "io.neuronsphere.stack.version"
	// AnnotationImageRegistries is proposed by SPEC008 and read, not written:
	// a stack whose images live outside the default registries names their
	// prefixes here, and `stack add` surfaces them as a line for hmd.env.
	AnnotationImageRegistries = "io.neuronsphere.stack.image_registries"

	// ItemType is the librarian item type every layer carries.
	ItemType = "build"
)

// DefaultNamespace is where a bare stack name expands to. Derived from the
// published image registry, since a stack's images must be pullable from the
// same place for it to be usable at all.
var DefaultNamespace = repoclass.PublishedRegistry + "/stacks"

// Layer is one build zip in a stack artifact.
type Layer struct {
	Class      string
	Version    string
	Descriptor v1.Descriptor
	// Subject marks the stack RepoClass's own zip.
	Subject bool
}

// Stack is a parsed stack artifact.
type Stack struct {
	// Name is the stack's short name (the last path segment of its
	// reference, or the annotation when present).
	Name    string
	Version string
	// Class is the stack RepoClass, the lock's repo_class_name.
	Class  string
	Lock   *lock.Lock
	Layers []Layer
	Digest digest.Digest
	Ref    oci.Ref
	// ImageRegistries is SPEC008's annotation, split, or nil.
	ImageRegistries []string
}

// ErrNotAStack is an artifact of another type.
var ErrNotAStack = errors.New("not a stack artifact")

// Read parses a fetched bundle, pairing every layer with a lock entry (or
// the subject) and refusing a mismatch either way: a half-honoured stack is
// a missing instance nobody goes looking for. SPEC002.
func Read(b *oci.Bundle) (*Stack, error) {
	if b.ArtifactType != ArtifactType && b.ConfigMediaType != LockMediaType {
		return nil, fmt.Errorf("%w: %s has artifactType %q and config %q; a stack is %s with a %s config",
			ErrNotAStack, b.Ref, b.ArtifactType, b.ConfigMediaType, ArtifactType, LockMediaType)
	}
	l, err := lock.Parse(b.Config)
	if err != nil {
		return nil, fmt.Errorf("%s: the stack's lock: %w", b.Ref, err)
	}
	if l.RepoClassName == "" {
		return nil, fmt.Errorf("%s: the stack's lock names no repo_class_name", b.Ref)
	}
	s := &Stack{
		Name:    b.Annotations[AnnotationStackName],
		Version: b.Annotations[AnnotationStackVersion],
		Class:   l.RepoClassName,
		Lock:    l,
		Digest:  b.Digest,
		Ref:     b.Ref,
	}
	if s.Name == "" {
		s.Name = ShortName(b.Ref)
	}
	if s.Version == "" {
		s.Version = b.Ref.Tag
	}
	if reg := strings.TrimSpace(b.Annotations[AnnotationImageRegistries]); reg != "" {
		for _, r := range strings.Split(reg, ",") {
			if r = strings.TrimSpace(r); r != "" {
				s.ImageRegistries = append(s.ImageRegistries, r)
			}
		}
	}

	seen := map[string]bool{}
	subjectSeen := false
	for _, d := range b.Layers {
		class := d.Annotations[AnnotationClass]
		version := d.Annotations[AnnotationVersion]
		if class == "" || version == "" {
			return nil, fmt.Errorf("%s: layer %s carries no %s/%s annotations", b.Ref, d.Digest, AnnotationClass, AnnotationVersion)
		}
		layer := Layer{Class: class, Version: version, Descriptor: d}
		switch {
		case class == l.RepoClassName:
			if subjectSeen {
				return nil, fmt.Errorf("%s: two layers claim to be the stack itself (%s)", b.Ref, class)
			}
			subjectSeen = true
			layer.Subject = true
			if s.Version == "" {
				s.Version = version
			} else if version != s.Version {
				return nil, fmt.Errorf("%s: the stack's own layer is %s@%s but the artifact is version %s", b.Ref, class, version, s.Version)
			}
		default:
			entry, ok := l.Entry(class)
			if !ok {
				return nil, fmt.Errorf("%s: layer %s@%s is not pinned by the stack's lock", b.Ref, class, version)
			}
			if entry.Version != version {
				return nil, fmt.Errorf("%s: layer %s@%s but the lock pins %s", b.Ref, class, version, entry.Version)
			}
			if err := entry.VerifyDigest(d.Digest.String()); err != nil {
				return nil, fmt.Errorf("%s: %w", b.Ref, err)
			}
			if seen[class] {
				return nil, fmt.Errorf("%s: %s appears twice", b.Ref, class)
			}
			seen[class] = true
		}
		s.Layers = append(s.Layers, layer)
	}
	if !subjectSeen {
		return nil, fmt.Errorf("%s: no layer carries the stack RepoClass %s itself", b.Ref, l.RepoClassName)
	}
	for _, e := range l.Resolved {
		if !seen[e.RepoClassName] {
			return nil, fmt.Errorf("%s: the lock pins %s@%s but the artifact carries no layer for it", b.Ref, e.RepoClassName, e.Version)
		}
	}
	if s.Version == "" {
		return nil, fmt.Errorf("%s: cannot tell the stack's version: no tag and no annotation", b.Ref)
	}
	return s, nil
}

// ShortName is the last path segment of a reference: what a stack is called.
func ShortName(ref oci.Ref) string {
	parts := strings.Split(ref.Repository, "/")
	return parts[len(parts)-1]
}

// Build assembles the artifact. zips maps repo class to build zip and must
// hold the subject (l.RepoClassName) and every pinned class; the returned
// lock carries every zip's digest and is the config blob. Deterministic for
// equal inputs. SPEC002 and SPEC006.
//
// name is the stack's short name annotation, or "" when the builder does not
// know where the artifact will be pushed (`stack build` writing a layout):
// a consumer then names the stack after the reference it pulled it from.
func Build(name, version string, l *lock.Lock, zips map[string][]byte) (v1.Manifest, map[digest.Digest][]byte, *lock.Lock, error) {
	if l.RepoClassName == "" {
		return v1.Manifest{}, nil, nil, errors.New("the lock names no repo_class_name")
	}
	pinned := *l
	pinned.Resolved = append([]lock.Entry(nil), l.Resolved...)

	blobs := map[digest.Digest][]byte{}
	var layers []v1.Descriptor
	add := func(class, ver string, data []byte) v1.Descriptor {
		d := digest.FromBytes(data)
		blobs[d] = data
		return v1.Descriptor{
			MediaType: BuildMediaType, Digest: d, Size: int64(len(data)),
			Annotations: map[string]string{
				AnnotationClass: class, AnnotationVersion: ver, AnnotationItemType: ItemType,
				AnnotationTitle: fmt.Sprintf("%s_%s_%s.zip", class, ver, ItemType),
			},
		}
	}

	subject, ok := zips[l.RepoClassName]
	if !ok {
		return v1.Manifest{}, nil, nil, fmt.Errorf("no build zip for the stack itself (%s)", l.RepoClassName)
	}
	layers = append(layers, add(l.RepoClassName, version, subject))

	classes := make([]string, 0, len(pinned.Resolved))
	for _, e := range pinned.Resolved {
		classes = append(classes, e.RepoClassName)
	}
	sort.Strings(classes)
	for _, class := range classes {
		e, _ := pinned.Entry(class)
		data, ok := zips[class]
		if !ok {
			return v1.Manifest{}, nil, nil, fmt.Errorf("no build zip for %s@%s, which the lock pins", class, e.Version)
		}
		d := add(class, e.Version, data)
		if err := e.VerifyDigest(d.Digest.String()); err != nil {
			return v1.Manifest{}, nil, nil, err
		}
		pinned.SetDigest(class, d.Digest.String())
		layers = append(layers, d)
	}
	for class := range zips {
		if class == l.RepoClassName {
			continue
		}
		if _, ok := pinned.Entry(class); !ok {
			return v1.Manifest{}, nil, nil, fmt.Errorf("a zip for %s was supplied but the lock does not pin it", class)
		}
	}

	config, err := lock.Marshal(&pinned)
	if err != nil {
		return v1.Manifest{}, nil, nil, err
	}
	cfgDigest := digest.FromBytes(config)
	blobs[cfgDigest] = config
	m := v1.Manifest{
		Versioned:    oci.Versioned(),
		MediaType:    v1.MediaTypeImageManifest,
		ArtifactType: ArtifactType,
		Config:       v1.Descriptor{MediaType: LockMediaType, Digest: cfgDigest, Size: int64(len(config))},
		Layers:       layers,
		Annotations: map[string]string{
			AnnotationStackVersion:              version,
			"org.opencontainers.image.version":  version,
			"org.opencontainers.image.licenses": "Apache-2.0",
		},
	}
	if name != "" {
		m.Annotations[AnnotationStackName] = name
	}
	return m, blobs, &pinned, nil
}

// Installed is what Install leaves in the cache.
type Installed struct {
	Stack *Stack
	// Dir is the cached tree for the stack RepoClass; Dirs every class's.
	Dir  string
	Dirs map[string]string
	// Zips holds every fetched zip by class, for a caller that also wants to
	// put them somewhere else (the local librarian).
	Zips map[string][]byte
}

// Install fetches ref, which must name a version, verifies every layer, and
// unpacks each into the artifact cache. The stack's own tree receives the
// artifact's lock at its root, digests included, so everything downstream
// sees a repository. SPEC003 steps 2-3.
func Install(ctx context.Context, home string, f oci.Fetcher, ref oci.Ref, progress func(string)) (*Installed, error) {
	if home == "" {
		return nil, errors.New("installing a stack needs an HMD_HOME")
	}
	if progress == nil {
		progress = func(string) {}
	}
	b, err := f.Fetch(ctx, ref)
	if err != nil {
		return nil, err
	}
	s, err := Read(b)
	if err != nil {
		return nil, err
	}
	inst := &Installed{Stack: s, Dirs: map[string]string{}, Zips: map[string][]byte{}}
	for _, layer := range s.Layers {
		progress(fmt.Sprintf("  %s@%s (%s)", layer.Class, layer.Version, layer.Descriptor.Digest))
		var buf bytes.Buffer
		if err := f.Blob(ctx, ref, layer.Descriptor, &buf); err != nil {
			return nil, err
		}
		data := buf.Bytes()
		if err := artifact.Invalidate(home, layer.Class, layer.Version); err != nil {
			return nil, err
		}
		dir, err := artifact.Store(home, layer.Class, layer.Version, data)
		if err != nil {
			return nil, err
		}
		inst.Dirs[layer.Class] = dir
		inst.Zips[layer.Class] = data
		if layer.Subject {
			inst.Dir = dir
			if err := os.WriteFile(filepath.Join(dir, lock.FileName), b.Config, 0o644); err != nil {
				return nil, fmt.Errorf("writing the stack's lock into %s: %w", dir, err)
			}
		}
	}
	return inst, nil
}

// Push publishes a built manifest and its blobs to ref.
func Push(ctx context.Context, c *oci.Client, ref oci.Ref, m v1.Manifest, blobs map[digest.Digest][]byte) (digest.Digest, error) {
	if _, err := c.PushBlob(ctx, ref, m.Config.MediaType, bytes.NewReader(blobs[m.Config.Digest]), m.Config.Size); err != nil {
		return "", err
	}
	for _, l := range m.Layers {
		if _, err := c.PushBlob(ctx, ref, l.MediaType, bytes.NewReader(blobs[l.Digest]), l.Size); err != nil {
			return "", err
		}
	}
	return c.PushManifest(ctx, ref, m)
}

// VersionsKey is the versions-cache key for a stack reference, in the shape
// SPEC005 names: stack~<host>~<path>.
func VersionsKey(ref oci.Ref) string {
	return "stack~" + ref.Host + "~" + strings.ReplaceAll(ref.Repository, "/", "~")
}
