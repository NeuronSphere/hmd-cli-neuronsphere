// Command repopack packs the repo trees nsctl embeds.
//
// A deploy node runs in a sibling projectbuilder container against a bind mount
// of the repo's working tree, so a machine with no $HMD_REPO_HOME cannot deploy
// anything: the substrate's first node failed with "no working tree for hmd-vpc
// under <HMD_REPO_HOME>". That made "a single binary whose only prerequisite is
// Docker" untrue of everything past `control-plane start`, which is most of
// what nsctl is for. These archives are what nsctl unpacks to stand in.
//
// Every tree comes from the repo class's published `build` artifact, pinned in
// this repository's own meta-data/manifest.json as a BACON pre_build_artifact.
// Three sources, per class, in this order:
//
//  1. -artifacts, the pre_build_artifacts destination `hmd build` populates.
//     Free when a Python build has already run.
//  2. -cache, a previous fetch by this tool, keyed <class>@<version>.
//  3. The artifact librarian, fetched directly. This is what lets `make
//     generate` run from GoReleaser's before.hooks on a CI runner with no `hmd`
//     installed, given HMD_ARTIFACT_LIBRARIAN_API_KEY.
//
// Until 2026-09-11 there was a fourth: trees committed under bundled-repos/,
// because probing the seven undeclared classes returned Unauthorized and seven
// pins nobody could resolve would have broken `hmd build` for everyone. The
// probe passed with a fresh credential -- all ten publish a consumable `build`
// artifact -- and the committed trees are gone. They were not merely redundant:
// bundled-repos/hmd-vpc had drifted from its repository and was missing the
// NERD0004 add_resource_output block, so a binary built from it deployed a VPC
// that handed hmd-postgres-rds no db_subnet_group_name. Their meta-data/VERSION
// also carried the MAJOR.MINOR stub (0.4) rather than the published version
// (0.4.854), which is the version ResolveVersion then reported for a tree it
// had not used.
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
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/artifact"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/librarian"
)

// packDirs are the top-level directories a deploy needs: the metadata that
// names the version and the dependencies, and the sources the deploy runs.
var packDirs = []string{"meta-data", "src"}

// serviceDirs are the src/ subtrees that hold a repo class's service code
// rather than its deploy descriptor. They are never packed, whatever the
// artifact contains: the deploy descriptor (meta-data, cdktf, helm, local,
// opa-bundles) is Apache 2.0 in every repo class, but these directories are
// Business Source License 1.1 in hmd-ms-deployment, hmd-ms-librarian and
// hmd-app-neuronsphere, and a byte of them inside the binary would make the
// Apache-licensed nsctl a mixed-licence artifact. The deploy never needs
// them either -- the service runs from its published image.
var serviceDirs = map[string]bool{"python": true, "typescript": true, "docker": true}

func main() {
	out := flag.String("out", "", "directory to write <class>.tar.gz into")
	artifacts := flag.String("artifacts", "", "pre_build_artifacts destination, searched first")
	cache := flag.String("cache", "", "directory for artifacts fetched by this tool")
	manifestPath := flag.String("manifest", "", "this repository's meta-data/manifest.json, for the version pins")
	offline := flag.Bool("offline", false, "never fetch; fail instead")
	flag.Parse()

	if *out == "" || flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: repopack -out DIR -manifest FILE [-artifacts DIR] [-cache DIR] [-offline] CLASS...")
		os.Exit(2)
	}
	if err := run(options{
		out:       *out,
		artifacts: *artifacts,
		cache:     *cache,
		manifest:  *manifestPath,
		offline:   *offline,
		classes:   flag.Args(),
	}); err != nil {
		fmt.Fprintln(os.Stderr, "repopack:", err)
		os.Exit(1)
	}
}

type options struct {
	out, artifacts, cache, manifest string
	offline                         bool
	classes                         []string
}

func run(o options) error {
	if err := os.MkdirAll(o.out, 0o755); err != nil {
		return err
	}
	pins, err := loadPins(o.manifest)
	if err != nil {
		return err
	}
	index := manifestIndex(o.artifacts)

	// Built on first use, so a run served entirely from -artifacts or -cache
	// needs no credentials at all. That is the common case on a developer
	// machine and the one `make test` hits.
	var client *librarian.Client
	var clientErr error
	clientOnce := func() (*librarian.Client, error) {
		if client == nil && clientErr == nil {
			client, clientErr = librarian.New(librarian.Config{})
		}
		return client, clientErr
	}

	total := 0
	for _, class := range o.classes {
		src, source, err := locate(o, class, pins, index, clientOnce)
		if err != nil {
			return err
		}
		n, err := pack(src, filepath.Join(o.out, class+".tar.gz"))
		if err != nil {
			return fmt.Errorf("%s: %w", class, err)
		}
		total += n
		fmt.Printf("  %-28s %7d bytes (%s)\n", class, n, source)
	}
	fmt.Printf("  %-28s %7d bytes\n", "total", total)
	return nil
}

// locate resolves one repo class to a directory to pack, fetching it if that is
// the only way, and reports which tier answered.
func locate(o options, class string, pins map[string]librarian.Spec, index map[string]string,
	clientOnce func() (*librarian.Client, error)) (dir, source string, err error) {

	if dir := index[class]; dir != "" {
		return dir, "artifact", nil
	}

	spec, pinned := pins[class]
	if !pinned {
		return "", "", fmt.Errorf("%s is not declared in %s.\n"+
			"  Add it to build.pre_build_artifacts as \"%s@<version>:build\", or drop it from BUNDLED_REPOS.",
			class, o.manifest, class)
	}

	cached := filepath.Join(o.cache, spec.Name+"@"+spec.Version)
	if o.cache != "" {
		if _, statErr := os.Stat(filepath.Join(cached, "meta-data")); statErr == nil {
			return cached, "cached", nil
		}
	}

	if o.offline {
		return "", "", unavailable(o, spec, cached, errors.New("offline"))
	}
	client, err := clientOnce()
	if err != nil {
		return "", "", unavailable(o, spec, cached, err)
	}
	if o.cache == "" {
		return "", "", fmt.Errorf("%s must be fetched but no -cache directory was given", spec)
	}

	fmt.Printf("  %-28s fetching %s\n", class, spec.Version)
	data, err := client.Fetch(context.Background(), spec.ContentPath())
	if err != nil {
		return "", "", unavailable(o, spec, cached, err)
	}
	if err := artifact.UnzipInto(data, cached); err != nil {
		return "", "", fmt.Errorf("%s: unpacking the artifact: %w", spec, err)
	}
	return cached, "fetched", nil
}

// unavailable reports a class that could not be resolved, naming the version
// wanted and every place that was looked. A bare 404 or "no credentials" here
// is indistinguishable from three different problems with three different
// fixes, which is the failure NERD002's cold-bootstrap errors exist to avoid.
func unavailable(o options, spec librarian.Spec, cached string, cause error) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%s is not available.\n", spec)
	fmt.Fprintf(&b, "  pre_build_artifacts: %s  (absent)\n", or(o.artifacts, "<not searched>"))
	fmt.Fprintf(&b, "  fetch cache:         %s  (absent)\n", or(cached, "<no -cache given>"))
	fmt.Fprintf(&b, "  artifact librarian:  %v\n", cause)

	switch {
	case errors.Is(cause, librarian.ErrNoCredentials):
		b.WriteString("Run `hmd login`, or set HMD_ARTIFACT_LIBRARIAN_API_KEY.\n")
		b.WriteString("Alternatively run `hmd build --prebuild-download-only` to populate the\n")
		b.WriteString("pre_build_artifacts destination, which this tool reads first.")
	case errors.Is(cause, librarian.ErrNotPublished):
		b.WriteString("The librarian has no such version. Check the pin in " + o.manifest + ".")
	default:
		var lerr *librarian.Error
		if errors.As(cause, &lerr) && lerr.Unauthorized() {
			b.WriteString("The credential was rejected. Run `hmd login` for a fresh token.")
		} else {
			b.WriteString("Run `hmd build --prebuild-download-only` to populate the\n")
			b.WriteString("pre_build_artifacts destination, which this tool reads first.")
		}
	}
	return errors.New(b.String())
}

func or(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// loadPins reads build.pre_build_artifacts and keys it by repo class.
//
// The spec's <name> is the repo class; the destination directory beside it is
// the plugin alias and is deliberately not used as a key.
func loadPins(path string) (map[string]librarian.Spec, error) {
	if path == "" {
		return nil, errors.New("no -manifest given; the version pins live in build.pre_build_artifacts")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading the pins: %w", err)
	}
	specs, err := librarian.PreBuildArtifacts(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	pins := map[string]librarian.Spec{}
	for _, spec := range specs {
		pins[spec.Name] = spec
	}
	return pins, nil
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
				if artifact.SkipDirs[name] || strings.HasSuffix(name, ".egg-info") {
					return fs.SkipDir
				}
				if top == "src" && serviceDirs[name] && filepath.Dir(path) == start {
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
