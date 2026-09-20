package controlplane

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bundled"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// NsctlImageName is the nsctl image's repository. It is deliberately
// unqualified: no registry holds this image, every copy is built locally.
const NsctlImageName = "hmd-img-nsctl"

// maxArchiveEntry bounds a single file unpacked from the embedded archive.
// The archive is our own build output, so this guards a corrupted embed rather
// than a hostile one, but an unbounded io.Copy from a tar reader is a habit
// worth not having.
const maxArchiveEntry = 32 << 20

// NsctlImageRef is the image tag the authd container runs from.
//
// HMD_NSCTL_IMAGE wins, so someone iterating with `make image` keeps their
// own build. Otherwise the tag carries the embedded sources' digest as well as
// the version: the version alone would go stale between meta-data/VERSION
// bumps and serve a control plane an image built from code it no longer ships,
// whereas the digest changes exactly when the sources do.
func NsctlImageRef(opts *Options) string {
	if ref := opts.lookup("HMD_NSCTL_IMAGE"); ref != "" {
		return ref
	}
	version := opts.Version
	if version == "" {
		version = "dev"
	}
	digest, err := nsctlSourceDigest()
	if err != nil {
		return NsctlImageName + ":" + version
	}
	return NsctlImageName + ":" + version + "-" + digest
}

// nsctlSourceDigest is the first 12 hex of the embedded archive's SHA-256.
func nsctlSourceDigest() (string, error) {
	data, err := bundled.Read(bundled.NsctlSourceArchive)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:12], nil
}

// ImageBuilder is the Docker subset EnsureNsctlImage needs, narrowed so the
// build can be exercised without a daemon.
type ImageBuilder interface {
	ImagePresent(ctx context.Context, ref string) bool
	Run(ctx context.Context, args ...string) (stdout, stderr []byte, err error)
}

// EnsureNsctlImage builds the nsctl image if it is not already present.
//
// The image is not published, so this is how it exists at all on a machine
// that installed nsctl from Homebrew or the install script. It is a no-op on
// every start after the first: the tag is content-addressed, so the answer only
// changes when the runner's sources do.
func EnsureNsctlImage(ctx context.Context, opts *Options, docker ImageBuilder) error {
	ref := NsctlImageRef(opts)
	if docker.ImagePresent(ctx, ref) {
		return nil
	}
	if opts.lookup("HMD_NSCTL_IMAGE") != "" {
		// An explicit tag that is not here is a mistake worth naming rather
		// than silently overwriting with a build of our own sources.
		return nserr.New(nserr.Usage,
			"HMD_NSCTL_IMAGE names %s, which is not in the local image cache. "+
				"Build it, or unset the variable and let nsctl build its own.", ref)
	}

	dir := NsctlBuildDir(opts.Home)
	opts.step("Building %s...", ref)
	opts.step("  the runner image is not published, so it is built here. This takes a")
	opts.step("  minute or two the first time and is then reused.")

	if err := unpackNsctlSource(dir); err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}

	version := opts.Version
	if version == "" {
		version = "dev"
	}
	// Tagged twice: the content-addressed tag is what runs, and the plain
	// version tag is what a human finds in `docker images`.
	_, stderr, err := docker.Run(ctx, "build",
		"--build-arg", "VERSION="+version,
		"-t", ref,
		"-t", NsctlImageName+":"+version,
		dir)
	if err != nil {
		return nserr.Wrap(nserr.Fail, fmt.Errorf("building %s: %w\n%s", ref, err, strings.TrimSpace(string(stderr))))
	}
	opts.step("  built %s", ref)
	return nil
}

// NsctlBuildDir is where the embedded sources are unpacked to be built.
func NsctlBuildDir(home string) string {
	return filepath.Join(ComposeCacheDir(home), "nsctl-src")
}

// errEmptyRunnerSource is what an nsctl running *as* the runner reports. Its
// own embedded archive is empty -- the sources cannot contain themselves -- so
// it can serve a DAG but not build the image it is serving from.
var errEmptyRunnerSource = errors.New(
	"this nsctl carries no runner sources, which means it is the one running inside the " +
		"runner image. Build the image from a host nsctl or with `make image`")

// unpackNsctlSource writes the embedded archive into dir.
func unpackNsctlSource(dir string) error {
	data, err := bundled.Read(bundled.NsctlSourceArchive)
	if err != nil {
		return err
	}
	return unpackArchive(dir, data)
}

// unpackArchive writes a gzipped tar into dir, replacing whatever is there.
// Replacing rather than merging matters: a stale file left by an older nsctl
// would otherwise be compiled into the image.
func unpackArchive(dir string, data []byte) error {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("reading the embedded runner sources: %w", err)
	}
	defer zr.Close()

	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	written := 0
	tr := tar.NewReader(zr)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("reading the embedded runner sources: %w", err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		path, err := safeJoin(dir, header.Name)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		if _, err := io.Copy(file, io.LimitReader(tr, maxArchiveEntry)); err != nil {
			file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		written++
	}
	if written == 0 {
		return errEmptyRunnerSource
	}
	return nil
}

// safeJoin refuses an archive entry that would escape dir.
func safeJoin(dir, name string) (string, error) {
	path := filepath.Join(dir, filepath.FromSlash(name))
	if !strings.HasPrefix(path, filepath.Clean(dir)+string(os.PathSeparator)) {
		return "", fmt.Errorf("refusing to unpack %q outside %s", name, dir)
	}
	return path, nil
}
