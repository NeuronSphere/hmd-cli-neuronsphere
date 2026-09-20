// Command nsctlsrc packs this Go module's sources into the tarball nsctl
// embeds and builds the nsctl image from.
//
// The nsctl image is not published anywhere, so nsctl builds it on demand --
// and the Dockerfile that builds it compiles the binary from module source, of
// which a `brew install`ed nsctl has none. Carrying the source inside the
// binary is what makes the image reachable from every install channel rather
// than only from a working tree.
//
// The archive is written deterministically -- entries sorted, no modification
// times, no ownership -- because its SHA-256 is the nsctl image's tag. A
// tarball that changed on every run would rebuild the image on every start.
//
// It lives in the module but is imported by nothing in it, so `go run
// ./tools/nsctlsrc` compiles without internal/bundled, whose //go:embed needs
// the very file this writes.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// skipNames are directory names never packed, wherever they appear: build/ is
// the compiled binary, and testdata/ (with the _test.go files below) is 100 KB
// of an archive that only ever runs `go build`.
var skipNames = map[string]bool{
	"build":    true,
	"testdata": true,
}

// skipPath is this command's own output directory, matched by path rather than
// by name -- internal/runner is a real package the build needs, and excluding
// every directory called "image" would be wrong.
const skipPath = "internal/bundled/image"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "nsctlsrc: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	out := filepath.Join(root, "internal", "bundled", "image", "source.tar.gz")

	paths, err := collect(root)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return fmt.Errorf("no sources found under %s", root)
	}

	archive, err := pack(root, paths)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(out, archive, 0o644); err != nil {
		return err
	}
	fmt.Printf("packed %d files into %s (%d bytes)\n", len(paths), out, len(archive))
	return nil
}

// collect lists the module-relative paths to pack, sorted.
func collect(root string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if rel == "." {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			// Dotted directories are editor and tool state, none of which the
			// image build reads.
			if skipNames[name] || strings.HasPrefix(name, ".") ||
				filepath.ToSlash(rel) == skipPath {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	return paths, nil
}

// pack writes the gzipped tar. Every header field that is not the name, the
// size or "is this executable" is fixed, so identical sources produce identical
// bytes on any machine.
func pack(root string, paths []string) ([]byte, error) {
	var buf bytes.Buffer
	// BestCompression rather than the default: this is written once per build
	// and carried in every copy of the binary.
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	tw := tar.NewWriter(zw)

	for _, rel := range paths {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return nil, err
		}
		// No directory entries: the extractor creates parents. One fewer thing
		// whose ordering and mode could differ between platforms.
		if err := tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg,
			Name:     rel,
			Mode:     0o644,
			Size:     int64(len(data)),
			Format:   tar.FormatUSTAR,
		}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(data); err != nil {
			return nil, err
		}
	}

	// The placeholder that lets the packed sources build themselves.
	//
	// The Dockerfile compiles this module inside the image, and
	// internal/bundled has a //go:embed for image/*.tar.gz that fails at
	// compile time when nothing matches. The archive cannot contain itself, so
	// it carries an empty one instead: the image's own nsctl is a binary that
	// cannot build a further nsctl image, which is exactly right, and
	// EnsureNsctlImage recognises an empty archive and says so.
	empty, err := emptyArchive()
	if err != nil {
		return nil, err
	}
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     "internal/bundled/image/source.tar.gz",
		Mode:     0o644,
		Size:     int64(len(empty)),
		Format:   tar.FormatUSTAR,
	}); err != nil {
		return nil, err
	}
	if _, err := tw.Write(empty); err != nil {
		return nil, err
	}

	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// emptyArchive is a valid, empty gzipped tar.
func emptyArchive() ([]byte, error) {
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	if err := tar.NewWriter(zw).Close(); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
