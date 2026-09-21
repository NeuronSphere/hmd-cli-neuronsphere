package plugin

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nsconfig"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/oci"
)

// Limits on what an archive may unpack to. A plugin is one binary and a
// licence; these are generous.
const (
	maxArchiveBytes = 512 << 20
	maxEntries      = 64
)

// ErrNoPlatform is an artifact with no layer for this GOOS_GOARCH.
var ErrNoPlatform = errors.New("plugin is not published for this platform")

// Installed is what Install leaves behind.
type Installed struct {
	Descriptor Descriptor
	// Digest is the manifest digest, what the declaration records.
	Digest digest.Digest
	Dir    string
	Binary string
	// Warning is a non-fatal note (an unchecked dev-build version).
	Warning string
}

// Declaration is the nsctl.toml table for this install.
func (i *Installed) Declaration(ref oci.Ref) nsconfig.Plugin {
	return nsconfig.Plugin{
		Name:    i.Descriptor.Name,
		Source:  ref.WithTag("").String(),
		Version: i.Descriptor.Version,
		Digest:  i.Digest.String(),
	}
}

// Install fetches ref, which must name a version, and unpacks this
// platform's binary under home. SPEC003.
//
// The directory is staged and renamed, so an interrupted install leaves
// nothing a declaration could point at, and an existing directory for the
// same version is replaced rather than trusted.
func Install(ctx context.Context, home string, f oci.Fetcher, ref oci.Ref, nsctlVersion string) (*Installed, error) {
	if home == "" {
		return nil, errors.New("installing a plugin needs an HMD_HOME")
	}
	b, err := f.Fetch(ctx, ref)
	if err != nil {
		return nil, err
	}
	if b.ArtifactType != ArtifactType && b.ConfigMediaType != ArtifactType {
		return nil, fmt.Errorf("%s is not a plugin: artifactType %q, config %q; a plugin artifact is %s",
			ref, b.ArtifactType, b.ConfigMediaType, ArtifactType)
	}
	d, err := ParseDescriptor(b.Config)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ref, err)
	}
	warn, err := d.CheckNsctlVersion(nsctlVersion)
	if err != nil {
		return nil, err
	}
	layer, err := selectLayer(d, b.Layers)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ref, err)
	}

	var archive bytes.Buffer
	if err := f.Blob(ctx, ref, layer, &archive); err != nil {
		return nil, err
	}

	dir := Dir(home, d.Name, d.Version)
	staging := dir + ".installing"
	if err := os.RemoveAll(staging); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return nil, err
	}
	if err := untar(archive.Bytes(), staging); err != nil {
		os.RemoveAll(staging)
		return nil, fmt.Errorf("%s: unpacking the plugin: %w", ref, err)
	}
	bin := Binary(staging, d.Name)
	info, err := os.Stat(bin)
	if err != nil {
		os.RemoveAll(staging)
		return nil, fmt.Errorf("%s: the archive holds no %s at its root (SPEC002)", ref, BinaryName(d.Name))
	}
	if info.Mode()&0o111 == 0 {
		if err := os.Chmod(bin, 0o755); err != nil {
			os.RemoveAll(staging)
			return nil, err
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		os.RemoveAll(staging)
		return nil, err
	}
	if err := os.Rename(staging, dir); err != nil {
		os.RemoveAll(staging)
		return nil, err
	}
	return &Installed{Descriptor: d, Digest: b.Digest, Dir: dir, Binary: Binary(dir, d.Name), Warning: warn}, nil
}

// selectLayer picks this platform's layer: by the descriptor's platforms
// table first, then by layer annotations, and refuses naming what exists.
func selectLayer(d Descriptor, layers []v1.Descriptor) (v1.Descriptor, error) {
	key := PlatformKey()
	if p, ok := d.Platforms[key]; ok {
		for _, l := range layers {
			if l.Digest.String() == p.Digest {
				return l, nil
			}
		}
	}
	goos, goarch, _ := strings.Cut(key, "_")
	for _, l := range layers {
		if l.Annotations[AnnotationOS] == goos && l.Annotations[AnnotationArch] == goarch {
			return l, nil
		}
	}
	return v1.Descriptor{}, fmt.Errorf("%w %s; published for: %s", ErrNoPlatform, key,
		strings.Join(d.PlatformKeys(), ", "))
}

// untar unpacks a gzipped tar of regular files into dest. Paths must stay
// inside dest, links are refused, and the archive is capped.
func untar(data []byte, dest string) error {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("not a gzipped tar: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var total int64
	entries := 0
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		entries++
		if entries > maxEntries {
			return fmt.Errorf("more than %d entries; a plugin archive is one binary and a licence", maxEntries)
		}
		name := filepath.Clean(strings.TrimPrefix(hdr.Name, "./"))
		if name == "." || name == "" {
			continue
		}
		if filepath.IsAbs(name) || strings.HasPrefix(name, "..") || strings.Contains(name, string(filepath.Separator)+"..") {
			return fmt.Errorf("entry %q escapes the archive", hdr.Name)
		}
		target := filepath.Join(dest, name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			total += hdr.Size
			if total > maxArchiveBytes {
				return fmt.Errorf("archive exceeds %d bytes", maxArchiveBytes)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(hdr.Mode) & 0o777
			if mode == 0 {
				mode = 0o644
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, io.LimitReader(tr, hdr.Size)); err != nil {
				out.Close()
				return err
			}
			out.Close()
		default:
			return fmt.Errorf("entry %q is not a regular file or directory", hdr.Name)
		}
	}
}

// ErrNotInstalled is a declared plugin whose binary is absent.
var ErrNotInstalled = errors.New("plugin is declared but not installed")

// Resolve is the executable for a declaration: the dev path when set, else
// the installed binary. The dev path must exist; the installed binary's
// absence is ErrNotInstalled, whose remedy is `nsctl plugin install`.
func Resolve(home string, decl nsconfig.Plugin) (string, error) {
	if decl.Dev() {
		path := strings.TrimSpace(decl.Path)
		info, err := os.Stat(path)
		if err != nil {
			return "", fmt.Errorf("plugin %s: path %s: %w", decl.Name, path, err)
		}
		if info.IsDir() {
			return "", fmt.Errorf("plugin %s: path %s is a directory, not an executable", decl.Name, path)
		}
		return path, nil
	}
	dir := Dir(home, decl.Name, decl.Version)
	if dir == "" {
		return "", fmt.Errorf("plugin %s: %w", decl.Name, ErrNotInstalled)
	}
	bin := Binary(dir, decl.Name)
	if _, err := os.Stat(bin); err != nil {
		return "", fmt.Errorf("plugin %s@%s: %w; run `nsctl plugin install %s`", decl.Name, decl.Version, ErrNotInstalled, decl.Name)
	}
	return bin, nil
}

// Remove deletes every installed version of a noun. It never touches a dev
// path.
func Remove(home, name string) error { return removeVersions(home, name, "") }

// Prune deletes every installed version of a noun except keep, after an
// upgrade.
func Prune(home, name, keep string) error { return removeVersions(home, name, keep) }

func removeVersions(home, name, keep string) error {
	root := Root(home)
	if root == "" {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), name+"@") && (keep == "" || e.Name() != name+"@"+keep) {
			if err := os.RemoveAll(filepath.Join(root, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// State is what `plugin list` reports for a declaration.
type State string

const (
	StateInstalled State = "installed"
	StateMissing   State = "missing binary"
	StateDev       State = "path"
)

// StateOf classifies a declaration.
func StateOf(home string, decl nsconfig.Plugin) State {
	if decl.Dev() {
		return StateDev
	}
	if _, err := Resolve(home, decl); err != nil {
		return StateMissing
	}
	return StateInstalled
}
