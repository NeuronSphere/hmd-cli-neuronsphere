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

// Resolver finds repos under $HMD_REPO_HOME.
type Resolver struct {
	// RepoHome is $HMD_REPO_HOME.
	RepoHome string
	Lookup   Lookup
	// Paths overrides where a repo class's working tree lives, for a repo an
	// environment manifest declares with an explicit source path -- which by
	// definition is not under RepoHome.
	Paths map[string]string
}

// New builds a Resolver.
func New(repoHome string, lookup Lookup) *Resolver {
	if lookup == nil {
		lookup = func(string) string { return "" }
	}
	return &Resolver{RepoHome: repoHome, Lookup: lookup, Paths: map[string]string{}}
}

// VersionEnvVar is the variable that pins one repo class's version.
func VersionEnvVar(repoClass string) string {
	return "HMD_LOCAL_VERSION_" + strings.ToUpper(strings.ReplaceAll(repoClass, "-", "_"))
}

// Dir is the working tree for a repo class, or "" when there is none.
func (r *Resolver) Dir(repoClass string) string {
	if override, ok := r.Paths[repoClass]; ok && override != "" {
		return override
	}
	if r.RepoHome == "" {
		return ""
	}
	dir := filepath.Join(r.RepoHome, repoClass)
	if info, err := os.Stat(dir); err == nil && info.IsDir() {
		return dir
	}
	return ""
}

// ResolveVersion picks the version to register a repo class under.
//
// An explicit pin wins over everything. Otherwise the working tree's own
// VERSION is used when one exists, then the version the BOM declared, then a
// sentinel.
//
// This is deliberately simpler than bom_seeder.resolve_repo_version, which
// prefers a *bundled artifact* over the working tree on the grounds that a
// published artifact is a reproducible version while a tree is a work in
// progress. nsctl embeds no artifacts yet -- pre_build_artifacts is not wired
// into the Go build -- so there is nothing to prefer, and inventing a
// precedence for something that does not exist would be worse than saying so.
// When the embedded trees land, this gains the artifact tier above the tree.
func (r *Resolver) ResolveVersion(repoClass, declared string) Resolution {
	if pinned := r.Lookup(VersionEnvVar(repoClass)); pinned != "" && pinned != "local" {
		return Resolution{Version: pinned, Source: SourcePin}
	}
	dir := r.Dir(repoClass)
	if v := readVersion(dir); v != "" {
		return Resolution{Version: v, Source: SourceWorkingTree, Root: dir}
	}
	if declared != "" {
		return Resolution{Version: declared, Source: SourceDeclared}
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
	dir := r.Dir(repoClass)
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
	manifest, err := r.LoadManifest(repoClass)
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
