// Package repoclass answers "what does this repo declare" from the repo itself.
//
// A repo already says what it produces and consumes, in its BACON
// manifest.json and its NERD0004 meta-data/resources/*.yaml. That is the single
// source nsctl reads, rather than the parallel per-plugin inventory
// nsplugin.json used to carry -- a second place to say the same thing is the
// one that goes stale.
package repoclass

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/repotree"
)

// SentinelVersion is what a repo resolves to when nothing else names a version.
const SentinelVersion = "0.1.0"

// Lookup resolves an environment variable, returning "" when unset.
type Lookup = func(string) string

// Source says where a resolved version came from, so a caller can read the rest
// of the repo's metadata from the same place -- registering one tree's version
// alongside another's dependencies would describe a build that never existed.
type Source string

const (
	// SourcePin is an explicit HMD_LOCAL_VERSION_<REPO_CLASS>.
	SourcePin Source = "pin"
	// SourceWorkingTree is the repo's own meta-data/VERSION.
	SourceWorkingTree Source = "working-tree"
	// SourceBundled is a tree the binary carries. It beats a working tree,
	// because a shipped tree is a reproducible version and a checkout is a work
	// in progress -- the same reason bom_seeder.resolve_repo_version prefers a
	// bundled artifact.
	SourceBundled Source = "bundled"
	// SourceDeclared is the version an environment manifest or BOM entry named.
	SourceDeclared Source = "declared"
	// SourceSentinel is the fallback.
	SourceSentinel Source = "sentinel"
)

// Resolution is a resolved version and where it came from.
type Resolution struct {
	Version string
	Source  Source
	// Root is the directory the version came from, or "" when it came from a
	// pin, a declared version or the sentinel.
	Root string
}

// Resolver finds repos: the trees the binary carries, and the ones checked out
// under $HMD_REPO_HOME.
type Resolver struct {
	// RepoHome is $HMD_REPO_HOME.
	RepoHome string
	// Home is $HMD_HOME, where a bundled tree is materialised. Empty means
	// this resolver has no bundled tier -- which is right for a caller that
	// only ever looks at checkouts, and wrong for anything that deploys.
	Home   string
	Lookup Lookup
	// Paths overrides where a repo class's working tree lives, for a repo an
	// environment manifest declares with an explicit source path -- which by
	// definition is not under RepoHome.
	Paths map[string]string
}

// New builds a Resolver with no bundled tier.
func New(repoHome string, lookup Lookup) *Resolver {
	return NewWithHome(repoHome, "", lookup)
}

// NewWithHome builds a Resolver that can also materialise the trees the binary
// carries, under $HMD_HOME.
func NewWithHome(repoHome, home string, lookup Lookup) *Resolver {
	if lookup == nil {
		lookup = func(string) string { return "" }
	}
	return &Resolver{RepoHome: repoHome, Home: home, Lookup: lookup, Paths: map[string]string{}}
}

// PreferLocalVersionsEnv asks for every repo class to resolve from its working
// tree rather than from the tree the binary carries. Per repo, the same is
// HMD_LOCAL_VERSION_<REPO_CLASS>=local. Both mirror bom_seeder.
const PreferLocalVersionsEnv = "HMD_LOCAL_NEURONSPHERE_PREFER_LOCAL_VERSIONS"

// preferLocal reports whether the user has explicitly asked for this repo
// class's checkout, which is what keeps developing these repos possible now
// that a bundled tree otherwise wins.
func (r *Resolver) preferLocal(repoClass string) bool {
	if strings.EqualFold(strings.TrimSpace(r.Lookup(VersionEnvVar(repoClass))), "local") {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(r.Lookup(PreferLocalVersionsEnv))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// treeDir is the checkout for a repo class. declared reports that an
// environment manifest named the path explicitly, which is a request rather
// than a convention and therefore outranks a bundled tree.
func (r *Resolver) treeDir(repoClass string) (dir string, declared bool) {
	if override, ok := r.Paths[repoClass]; ok && override != "" {
		return override, true
	}
	if r.RepoHome == "" {
		return "", false
	}
	candidate := filepath.Join(r.RepoHome, repoClass)
	if info, err := os.Stat(candidate); err == nil && info.IsDir() {
		return candidate, false
	}
	return "", false
}

// BundledDir materialises the tree the binary carries for a repo class, or ""
// when it carries none.
func (r *Resolver) BundledDir(repoClass string) string {
	if r.Home == "" {
		return ""
	}
	return repotree.Dir(r.Home, repoClass)
}

// VersionEnvVar is the variable that pins one repo class's version.
func VersionEnvVar(repoClass string) string {
	return "HMD_LOCAL_VERSION_" + strings.ToUpper(strings.ReplaceAll(repoClass, "-", "_"))
}

// Dir is the tree describing a repo class, or "" when there is none.
//
// Precedence follows ResolveVersion's, so the version, the dependencies and the
// deploy sources all come from one place: an explicitly declared path, then a
// checkout the user asked for, then the tree the binary carries, then whatever
// checkout happens to be lying around.
func (r *Resolver) Dir(repoClass string) string {
	tree, declared := r.treeDir(repoClass)
	if tree != "" && (declared || r.preferLocal(repoClass)) {
		return tree
	}
	if b := r.BundledDir(repoClass); b != "" {
		return b
	}
	return tree
}

// ResolveVersion picks the version to register a repo class under.
//
// The tiers are bom_seeder.resolve_repo_version's, and the reason they are in
// this order is its: a shipped tree is a reproducible version while a checkout
// is a work in progress, so the bundled one wins unless the developer says
// otherwise. Saying otherwise is the second tier, and without it local
// development of the ten bundled repos would be impossible.
//
//  1. HMD_LOCAL_VERSION_<REPO_CLASS> pinned to a literal version.
//  2. A checkout the user asked for: that variable set to "local",
//     HMD_LOCAL_NEURONSPHERE_PREFER_LOCAL_VERSIONS, or a source path the
//     environment manifest declares outright.
//  3. The tree the binary carries.
//  4. The version the BOM entry declared.
//  5. Whatever checkout happens to be under $HMD_REPO_HOME, as a last resort.
//  6. The sentinel.
func (r *Resolver) ResolveVersion(repoClass, declared string) Resolution {
	if pinned := r.Lookup(VersionEnvVar(repoClass)); pinned != "" && !strings.EqualFold(pinned, "local") {
		return Resolution{Version: pinned, Source: SourcePin}
	}

	tree, declaredPath := r.treeDir(repoClass)
	if tree != "" && (declaredPath || r.preferLocal(repoClass)) {
		if v := readVersion(tree); v != "" {
			return Resolution{Version: v, Source: SourceWorkingTree, Root: tree}
		}
	}
	if b := r.BundledDir(repoClass); b != "" {
		if v := readVersion(b); v != "" {
			return Resolution{Version: v, Source: SourceBundled, Root: b}
		}
	}
	if declared != "" {
		return Resolution{Version: declared, Source: SourceDeclared}
	}
	if v := readVersion(tree); v != "" {
		return Resolution{Version: v, Source: SourceWorkingTree, Root: tree}
	}
	return Resolution{Version: SentinelVersion, Source: SourceSentinel}
}

func readVersion(dir string) string {
	if dir == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(dir, "meta-data", "VERSION"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// Manifest is the part of a BACON manifest.json nsctl reads.
type Manifest struct {
	Name   string `json:"name"`
	Deploy struct {
		Dependencies         map[string]any `json:"dependencies"`
		DefaultConfiguration map[string]any `json:"default_configuration"`
	} `json:"deploy"`
}

// LoadManifest reads a repo's meta-data/manifest.json.
//
// A missing manifest is not an error: not every repo class in a BOM has a
// working tree on this machine, and the BOM entry's own declarations stand in.
func (r *Resolver) LoadManifest(repoClass string) (*Manifest, error) {
	return r.loadManifestFrom(repoClass, r.Dir(repoClass))
}

func (r *Resolver) loadManifestFrom(repoClass, dir string) (*Manifest, error) {
	if dir == "" {
		return nil, nil
	}
	data, err := os.ReadFile(filepath.Join(dir, "meta-data", "manifest.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s's manifest: %w", repoClass, err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing %s's manifest: %w", repoClass, err)
	}
	return &m, nil
}

// Resolve reports the version, dependencies and default configuration to
// register a repo class under. It satisfies bom.VersionResolver.
//
// The dependencies and configuration come from the same place the version did,
// so the registered RepoClassVersion describes one coherent build.
func (r *Resolver) Resolve(repoClass, declared string) (string, map[string]any, map[string]any, error) {
	resolution := r.ResolveVersion(repoClass, declared)
	// From the tree the version came from, when there was one. Reading the
	// version from one place and the dependencies from another describes a
	// build that never existed, which is what the Source doc warns about and
	// what re-deriving the directory here used to risk.
	dir := resolution.Root
	if dir == "" {
		dir = r.Dir(repoClass)
	}
	manifest, err := r.loadManifestFrom(repoClass, dir)
	if err != nil {
		return resolution.Version, nil, nil, err
	}
	if manifest == nil {
		// nil rather than an empty map: the caller falls back to what the BOM
		// entry declares, and an empty map would silently overwrite it.
		return resolution.Version, nil, nil, nil
	}
	return resolution.Version, manifest.Deploy.Dependencies, manifest.Deploy.DefaultConfiguration, nil
}

// PublishedRegistry is where a repo class's built image is published.
//
// ghcr.io/hmdlabs, not ghcr.io/neuronsphere: the latter mirrored only the
// released subset and has gone stale. It lives here rather than in
// internal/controlplane because both the control plane and internal/floci
// resolve images against it, and both already depend on this package -- a
// second copy is a second place to miss when it moves, which is how the old
// default came to name tags that existed nowhere.
const PublishedRegistry = "ghcr.io/hmdlabs"
