// Package artifact caches the unpacked trees of versioned librarian artifacts.
//
// A RepoClass declared `source: {type: artifact}` deploys from a versioned zip
// the control plane's Artifact Librarian holds, not from a checkout. A deploy
// node still runs in a sibling projectbuilder container against a bind mount, so
// the zip has to become an ordinary host directory before anything downstream
// can use it -- at which point it enters bom.RepoPaths and
// runner.Config.RepoPaths exactly as an explicit source.path does, and nothing
// past resolution learns that anything changed. NERD005 SPEC003.
//
// The trees live under $HMD_HOME/.cache/neuronsphere/artifacts, beside the ones
// repotree materialises out of the binary and for the same reason: the runner
// binds $HMD_HOME at its own absolute path, so the host and container path
// spaces coincide there and nowhere else.
//
// This one is *cache*, and the contrast with an environment's state directory is
// worth stating: an unpacked artifact is wholly reconstructible from the
// librarian, so deleting it costs a refetch and nothing else. `env purge` removes
// an environment's state directory and never reaches .cache/neuronsphere, which
// is why one cache keyed by class and version needs no purge work.
//
// Nothing here fetches. A fetch belongs to the command the user typed, never to
// resolution: an apply that reaches the internet without being asked is an apply
// that behaves differently on an aeroplane. The offline guarantee is a property
// of this package's imports, not of a runtime flag.
package artifact

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/opencontainers/go-digest"
	"sync"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
)

// maxEntry caps one file unpacked from an artifact, matching repotree's limit on
// the archives it reads.
const maxEntry = 32 << 20

// mu serialises unpacking. Deploy nodes of different repo classes run
// concurrently, and two of them arriving at a cold cache would otherwise race on
// the same staging directory.
var mu sync.Mutex

// SkipDirs are build outputs and VCS state, excluded from a tree this package
// zips and from the tarballs tools/repopack writes. Everything else travels:
// guessing which files a repo's deploy reads is how a packaged tree deploys
// differently from a checkout.
var SkipDirs = map[string]bool{
	".git": true, "build": true, "target": true, "dist": true,
	"node_modules": true, "__pycache__": true, ".terraform": true,
	"cdktf.out": true, ".pytest_cache": true, ".mypy_cache": true,
	// A deploy's own output, not its source. The runner reads
	// meta-data/resources_output/ out of the workspace after a node runs and
	// submits what it finds to ms-deployment -- so a copy carried in an artifact
	// would have every environment submitting whoever's deploy produced it.
	"resources_output": true,
}

// Root is where unpacked artifacts live.
func Root(home string) string {
	return filepath.Join(home, ".cache", "neuronsphere", "artifacts")
}

// Dir is the host path an artifact unpacks to, or "" when it cannot be named.
//
// The token in the directory name is the *version*, where repotree uses a
// content digest. The version is what the manifest asked for and what `nsctl
// status` reports, so keying on anything else would let the two disagree.
func Dir(home, class, version string) string {
	if home == "" || class == "" || version == "" {
		return ""
	}
	return filepath.Join(Root(home), class+"@"+version)
}

// Cached reports whether an artifact is already unpacked, which is the whole
// question resolution asks: present means no fetch, and that is what makes
// `nsctl env apply` work on a disconnected machine.
//
// It stats meta-data inside the tree rather than the tree itself. A bare
// directory stat vouches for an empty directory, which is precisely the state an
// interrupted unpack used to leave.
func Cached(home, class, version string) bool {
	dir := Dir(home, class, version)
	if dir == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(dir, "meta-data"))
	return err == nil && info.IsDir()
}

// Store unpacks an artifact zip into the cache and returns its host path.
//
// It is idempotent: an already-cached version is returned without touching the
// archive, so a caller need not ask Cached first.
//
// Unlike repotree.Dir this returns an error rather than "". "The binary carries
// no tree for this class" is a legitimate state a caller falls through on; a
// failed unpack of a version a manifest named by number is not, and returning
// nothing for it is how a deploy ends up reporting a version it did not use.
func Store(home, class, version string, data []byte) (string, error) {
	dir := Dir(home, class, version)
	if dir == "" {
		return "", fmt.Errorf("cannot cache an artifact: home, repo class and version are all required"+
			" (got %q, %q, %q)", home, class, version)
	}
	if Cached(home, class, version) {
		return dir, nil
	}

	mu.Lock()
	defer mu.Unlock()
	// Re-checked under the lock: another goroutine may have unpacked it between
	// the check above and here, and unpacking a second time over a tree being
	// read is the race the mutex exists for.
	if Cached(home, class, version) {
		return dir, nil
	}

	// Unpacked beside the destination and renamed into place, so an unpack
	// interrupted midway leaves no half-tree that the version then vouches for.
	staging := dir + ".unpacking"
	if err := os.RemoveAll(staging); err != nil {
		return "", err
	}
	if err := UnzipInto(data, staging); err != nil {
		os.RemoveAll(staging)
		return "", fmt.Errorf("%s@%s: unpacking the artifact: %w", class, version, err)
	}
	if err := validate(staging); err != nil {
		os.RemoveAll(staging)
		return "", fmt.Errorf("%s@%s: %w", class, version, err)
	}
	if err := os.Rename(staging, dir); err != nil {
		os.RemoveAll(staging)
		// Lost the race with another process; if it won, the tree is there.
		if Cached(home, class, version) {
			return dir, nil
		}
		return "", fmt.Errorf("%s@%s: %w", class, version, err)
	}
	// The digest sidecar is best-effort: a tree without one is a tree an
	// older nsctl unpacked, and Digest answers "unknown" for it. NERD017
	// SPEC007.
	if err := os.MkdirAll(filepath.Dir(sidecar(dir)), 0o755); err == nil {
		_ = os.WriteFile(sidecar(dir), []byte(digest.FromBytes(data).String()+"\n"), 0o644)
	}
	return dir, nil
}

// sidecar is the file holding the zip's digest for an unpacked tree. It lives
// under a sibling of Root rather than inside the tree, so the runner's bind
// mount and validate() see a repo tree and nothing else, and rather than
// beside the tree, so a listing of Root is still exactly the cached trees.
func sidecar(dir string) string {
	return filepath.Join(filepath.Dir(dir)+"-digests", filepath.Base(dir))
}

// Digest is the sha256 of the zip a cached tree was unpacked from, or false
// when the tree is absent or was unpacked before the sidecar existed.
func Digest(home, class, version string) (string, bool) {
	dir := Dir(home, class, version)
	if dir == "" || !Cached(home, class, version) {
		return "", false
	}
	data, err := os.ReadFile(sidecar(dir))
	if err != nil {
		return "", false
	}
	d, err := digest.Parse(strings.TrimSpace(string(data)))
	if err != nil {
		return "", false
	}
	return d.String(), true
}

// removeSidecar drops the digest and keeps the tree, for a test that models
// an older cache.
func removeSidecar(home, class, version string) error {
	return os.RemoveAll(sidecar(Dir(home, class, version)))
}

// Invalidate drops the unpacked tree for a version, so the next Store re-unpacks.
//
// This is what makes re-registering a version safe. Registering overwrites the
// artifact in the librarian, and an unpacked copy left in place would then deploy
// the *previous* build under the new build's version -- the same failure, from
// the other direction, that an artifact source refusing to fall back to a stale
// checkout exists to prevent. NERD005 SPEC005.
func Invalidate(home, class, version string) error {
	dir := Dir(home, class, version)
	if dir == "" {
		return nil
	}
	mu.Lock()
	defer mu.Unlock()
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.RemoveAll(sidecar(dir)); err != nil {
		return err
	}
	return os.RemoveAll(dir + ".unpacking")
}

// Paths maps repo class to the unpacked tree for every instance that is both
// artifact-sourced and already cached.
//
// Keyed by class, not instance, because that is what bom.RepoPaths produces and
// what the resolver and the runner both consult. Uncached instances are absent
// rather than present-and-empty: a caller that merges this into repoPaths must
// see the gap, since an empty path silently falls through to a checkout.
func Paths(home string, repos []manifest.Repo) map[string]string {
	paths := map[string]string{}
	for _, r := range repos {
		if r.SourceType() != manifest.SourceArtifact || r.RepoClassName == "" {
			continue
		}
		if !Cached(home, r.RepoClassName, r.Version) {
			continue
		}
		paths[r.RepoClassName] = Dir(home, r.RepoClassName, r.Version)
	}
	return paths
}

// validate refuses a tree that is not a repo.
//
// It checks two path literals rather than calling repoclass.LoadManifest, and
// the duplication is deliberate: repoclass imports *this* package to resolve an
// artifact-sourced class, so a dependency the other way would be a cycle. The
// two literals are the ones ResolveVersion and floci.ServiceConfig read, and the
// cost of them drifting is a tree that fails deep inside a deploy node instead of
// here, with the path that was missing named.
//
// src/ is deliberately *not* required. A manifest may name an artifact of type
// schema or configuration, and those legitimately have no sources.
func validate(dir string) error {
	for _, rel := range []string{
		filepath.Join("meta-data", "manifest.json"),
		filepath.Join("meta-data", "VERSION"),
	} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			return fmt.Errorf("the artifact is not a repo tree: %s is missing", filepath.ToSlash(rel))
		}
	}
	return nil
}

// UnzipInto writes an artifact zip into dest, staging first so an interrupted
// write cannot leave a half-tree that the next run mistakes for a cache hit.
func UnzipInto(data []byte, dest string) error {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(filepath.Dir(dest), ".staging-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)

	for _, f := range zr.File {
		name := filepath.Clean(filepath.FromSlash(f.Name))
		if name == "." || strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
			return fmt.Errorf("refusing an entry outside the archive root: %q", f.Name)
		}
		target := filepath.Join(staging, name)
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		w, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(w, io.LimitReader(rc, maxEntry))
		rc.Close()
		w.Close()
		if err != nil {
			return err
		}
	}
	os.RemoveAll(dest)
	return os.Rename(staging, dest)
}

// Zip packs a working tree into an artifact zip.
//
// Written deterministically -- sorted entries, fixed mode, no mtime, no uid/gid
// -- for the same reason repopack writes its tarballs that way: a zip that
// churned would upload new bytes for an unchanged tree, and a version whose
// contents move under it is exactly what SPEC003's immutability assumption
// cannot survive.
func Zip(root string) ([]byte, error) {
	files, err := collect(root)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no files under %s", root)
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range files {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			return nil, err
		}
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(0o644)
		// Modified is left at its zero value rather than set from the file: the
		// DOS timestamp it encodes is then a constant, so the same tree gives
		// the same bytes on any machine and on any day.
		w, err := zw.CreateHeader(header)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(data); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// collect lists the files to pack, as sorted slash-separated relative paths.
func collect(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if path == root {
				return nil
			}
			if SkipDirs[name] || strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".egg-info") {
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
	sort.Strings(files)
	return files, nil
}

// Unavailable reports an artifact that could not be resolved, naming the version
// wanted and every place that was looked.
//
// The shape is tools/repopack's `unavailable`, ported rather than reinvented, and
// for the same reason: "the version was never pulled", "the local librarian is
// not running", "the credential expired" and "the presigned host does not
// resolve" all arrive at a caller as one failed request, and a bare 404 sends the
// reader to debug whichever of the four they happen to think of first.
//
// cause is nil for the ordinary case -- resolution found nothing cached -- and
// the message then says that nothing was contacted. Resolution is offline by
// construction, and a message implying a 404 from a librarian it never asked
// invites the reader to debug a network that was never involved.
func Unavailable(home, class, version, contentPath, repoHome string, cause error) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%s@%s is not available.\n", class, version)
	fmt.Fprintf(&b, "  artifact cache:   %s  (absent)\n", or(Dir(home, class, version), "<no HMD_HOME>"))

	switch {
	case cause == nil:
		b.WriteString("  local librarian:  not contacted; resolution never reaches the network\n")
	default:
		fmt.Fprintf(&b, "  local librarian:  %s  (%v)\n", contentPath, cause)
	}
	if repoHome != "" {
		fmt.Fprintf(&b, "  working tree:     %s  (absent; and not requested)\n", filepath.Join(repoHome, class))
	}

	var dns *net.DNSError
	switch {
	case errors.As(cause, &dns):
		// The librarian answers /apiop/get with a presigned URL hosted at
		// neuronsphere:4566, which resolves from the host only through the
		// /etc/hosts entry plus the nginx stream that fronts it. This is neither
		// a missing artifact nor a down librarian, and reporting it as either
		// sends the reader in the wrong direction.
		fmt.Fprintf(&b, "%s did not resolve, so the presigned URL is unreachable from this host.\n", dns.Name)
		b.WriteString("Add this line to /etc/hosts and try again:\n\n    127.0.0.1 neuronsphere neuronsphere-workload")
	case cause == nil:
		b.WriteString("Register a local build of it, or pull the published version into the control plane.")
	default:
		b.WriteString("The control plane's librarian could not serve it. Check that it is running with `nsctl control-plane status`.")
	}
	return errors.New(b.String())
}

func or(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
