// Package repotree materialises the repo trees the binary carries onto disk.
//
// A deploy node runs in a projectbuilder container launched as a *sibling*
// through the Docker socket, so every bind mount it composes is resolved by the
// host daemon. A tree that exists only inside the binary is not mountable, and
// a tree unpacked anywhere the runner container cannot also see at the same
// absolute path is not mountable either. $HMD_HOME satisfies both: the runner
// binds it at its own absolute path precisely so the two path spaces coincide.
//
// The pattern is EnsureRunnerImage's, which unpacks the runner's own sources to
// $HMD_HOME/.cache/neuronsphere/nsrunner-src and builds from there. The
// directory is suffixed with the archive's digest for the same reason that
// image tag is: it changes exactly when the contents do, so an unpack happens
// once per version rather than once per deploy, and two versions of nsctl on
// one HMD_HOME do not overwrite each other's trees.
package repotree

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bundled"
)

// maxEntry caps one file, matching the runner-source unpacker's limit.
const maxEntry = 32 << 20

// mu serialises unpacking. Deploy nodes of different repo classes run
// concurrently, and two of them arriving at a cold cache would otherwise race
// on the same temporary directory.
var mu sync.Mutex

// Root is where materialised trees live.
func Root(home string) string {
	return filepath.Join(home, ".cache", "neuronsphere", "repos")
}

// Available reports whether this binary carries a tree for a repo class.
func Available(repoClass string) bool {
	_, ok := bundled.RepoArchive(repoClass)
	return ok
}

// Dir materialises the bundled tree for a repo class and returns its host path,
// or "" when the binary carries none.
//
// Unpacking is idempotent and cheap after the first call: the digest in the
// directory name means an existing directory is already the right contents.
func Dir(home, repoClass string) string {
	if home == "" || repoClass == "" {
		return ""
	}
	data, ok := bundled.RepoArchive(repoClass)
	if !ok {
		return ""
	}
	sum := sha256.Sum256(data)
	dir := filepath.Join(Root(home), repoClass+"@"+hex.EncodeToString(sum[:])[:12])

	mu.Lock()
	defer mu.Unlock()
	if info, err := os.Stat(dir); err == nil && info.IsDir() {
		return dir
	}
	// Unpacked beside the destination and renamed into place, so a start
	// interrupted midway leaves no half-tree that the digest then vouches for.
	staging := dir + ".unpacking"
	if err := os.RemoveAll(staging); err != nil {
		return ""
	}
	if err := Unpack(staging, data); err != nil {
		os.RemoveAll(staging)
		return ""
	}
	if err := os.Rename(staging, dir); err != nil {
		os.RemoveAll(staging)
		// Lost the race with another process; if it won, the tree is there.
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
		return ""
	}
	return dir
}

// ErrEmpty is an archive that unpacked no files.
var ErrEmpty = errors.New("the archive contains no files")

// Unpack writes a gzipped tar into dir, replacing whatever is there.
//
// Replacing rather than merging: a stale file left by an older nsctl would
// otherwise be mounted into a deploy, and a tree that is *almost* the bundled
// one is worse than either.
func Unpack(dir string, data []byte) error {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("reading the archive: %w", err)
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
			return fmt.Errorf("reading the archive: %w", err)
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
		_, copyErr := io.Copy(file, io.LimitReader(tr, maxEntry))
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		written++
	}
	if written == 0 {
		return ErrEmpty
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
