// Command repopack packs the repo trees nsctl embeds.
//
// A deploy node runs in a sibling projectbuilder container against a bind mount
// of the repo's working tree, so a machine with no $HMD_REPO_HOME cannot deploy
// anything: the substrate's first node failed with "no working tree for hmd-vpc
// under <HMD_REPO_HOME>". That made "a single binary whose only prerequisite is
// Docker" untrue of everything past `control-plane start`, which is most of
// what nsctl is for. These archives are what nsctl unpacks to stand in.
//
// Two sources, per repo class, in this order:
//
//  1. -artifacts, the BACON pre_build_artifacts destination that `hmd build`
//     populates. A published artifact is a reproducible version, which is why
//     bom_seeder.resolve_repo_version prefers one over any working tree.
//  2. -fallback, the trees committed to this repository.
//
// The fallback is not a convenience. `make generate` runs from GoReleaser's
// before.hooks on a clean CI checkout with no network and no credentials, so a
// build that could only fetch would fail exactly where releases are made. Only
// three of the ten classes are declared as pre_build_artifacts today
// (hmd-inf-ext-secrets, its CRDs and hmd-inf-neptune); the rest reach the
// binary through the committed trees until they publish artifacts of their own.
//
// A class is identified by the `name` in its own meta-data/manifest.json, never
// by its directory: the artifact directories are named after the plugin
// (ext-secrets, apache_superset) while the repo classes are hmd-inf-ext-secrets
// and hmd-inf-superset. _artifact_version_index's docstring makes the same
// point for the same reason.
//
// The archives are written deterministically -- fixed mode, no mtime, no
// uid/gid, sorted entries -- because their digest names the cache directory
// they unpack into. A tarball that churned would re-unpack on every start.
package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// skipDirs are build outputs and VCS state. Everything else in meta-data/ and
// src/ travels: guessing which files a repo's deploy reads is how a bundled
// tree deploys differently from a checkout.
var skipDirs = map[string]bool{
	".git": true, "build": true, "target": true, "dist": true,
	"node_modules": true, "__pycache__": true, ".terraform": true,
	"cdktf.out": true, ".pytest_cache": true, ".mypy_cache": true,
	// A deploy's own output, not its source. The runner reads
	// meta-data/resources_output/ out of the workspace after a node runs and
	// submits what it finds to ms-deployment -- so a copy left in a checkout
	// would have every environment submitting whoever's deploy produced it.
	"resources_output": true,
}

// packDirs are the top-level directories a deploy needs: the metadata that
// names the version and the dependencies, and the sources the deploy runs.
var packDirs = []string{"meta-data", "src"}

func main() {
	out := flag.String("out", "", "directory to write <class>.tar.gz into")
	artifacts := flag.String("artifacts", "", "pre_build_artifacts destination, searched first")
	fallback := flag.String("fallback", "", "committed trees, searched second")
	flag.Parse()

	if *out == "" || flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: repopack -out DIR [-artifacts DIR] [-fallback DIR] CLASS...")
		os.Exit(2)
	}
	if err := run(*out, *artifacts, *fallback, flag.Args()); err != nil {
		fmt.Fprintln(os.Stderr, "repopack:", err)
		os.Exit(1)
	}
}

func run(out, artifacts, fallback string, classes []string) error {
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	index := manifestIndex(artifacts)

	total := 0
	for _, class := range classes {
		src := index[class]
		source := "artifact"
		if src == "" {
			src = filepath.Join(fallback, class)
			source = "bundled"
			if _, err := os.Stat(src); err != nil {
				return fmt.Errorf("%s has neither a built artifact under %s nor a committed tree at %s; "+
					"run `make refresh-bundled-repos` with the repo checked out under $HMD_REPO_HOME",
					class, artifacts, src)
			}
		}
		n, err := pack(src, filepath.Join(out, class+".tar.gz"))
		if err != nil {
			return fmt.Errorf("%s: %w", class, err)
		}
		total += n
		fmt.Printf("  %-28s %7d bytes (%s)\n", class, n, source)
	}
	fmt.Printf("  %-28s %7d bytes\n", "total", total)
	return nil
}

// manifestIndex maps repo class to artifact directory, keyed by each artifact's
// own manifest name.
func manifestIndex(root string) map[string]string {
	index := map[string]string{}
	if root == "" {
		return index
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		// An empty index is legitimate: external/ is only populated by
		// `hmd build`, so a source checkout has none.
		return index
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		data, err := os.ReadFile(filepath.Join(dir, "meta-data", "manifest.json"))
		if err != nil {
			continue
		}
		var m struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(data, &m) != nil || m.Name == "" {
			continue
		}
		index[m.Name] = dir
	}
	return index
}

// collect lists the files to pack, as sorted slash-separated relative paths.
func collect(root string) ([]string, error) {
	var files []string
	for _, top := range packDirs {
		start := filepath.Join(root, top)
		if _, err := os.Stat(start); err != nil {
			continue
		}
		err := filepath.WalkDir(start, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			name := d.Name()
			if d.IsDir() {
				if skipDirs[name] || strings.HasSuffix(name, ".egg-info") {
					return fs.SkipDir
				}
				return nil
			}
			if strings.HasPrefix(name, ".") {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			files = append(files, filepath.ToSlash(rel))
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(files)
	return files, nil
}

func pack(root, out string) (int, error) {
	files, err := collect(root)
	if err != nil {
		return 0, err
	}
	if len(files) == 0 {
		return 0, fmt.Errorf("no files under %s", root)
	}

	var buf strings.Builder
	zw, _ := gzip.NewWriterLevel(&writerTo{&buf}, gzip.BestCompression)
	tw := tar.NewWriter(zw)
	for _, name := range files {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			return 0, err
		}
		// Every field but name and size is fixed, so the same tree gives the
		// same bytes on any machine and the digest naming the cache directory
		// stays put.
		if err := tw.WriteHeader(&tar.Header{
			Name:     name,
			Size:     int64(len(data)),
			Typeflag: tar.TypeReg,
			Mode:     0o644,
			Format:   tar.FormatUSTAR,
		}); err != nil {
			return 0, err
		}
		if _, err := tw.Write(data); err != nil {
			return 0, err
		}
	}
	if err := tw.Close(); err != nil {
		return 0, err
	}
	if err := zw.Close(); err != nil {
		return 0, err
	}
	payload := []byte(buf.String())
	if err := os.WriteFile(out, payload, 0o644); err != nil {
		return 0, err
	}
	return len(payload), nil
}

// writerTo adapts a strings.Builder to io.Writer for gzip.
type writerTo struct{ b *strings.Builder }

func (w *writerTo) Write(p []byte) (int, error) { return w.b.Write(p) }

var _ io.Writer = (*writerTo)(nil)
