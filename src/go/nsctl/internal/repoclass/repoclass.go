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

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/artifact"
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
	// SourceArtifact is a versioned artifact the control plane's librarian
	// holds, unpacked into the cache. It beats a bundled tree and an
	// unasked-for checkout, because the manifest named a version and neither of
	// those is that version -- quietly substituting one produces the worst
	// outcome available, a deploy that reports a version it did not use.
	SourceArtifact Source = "artifact"
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
	// Artifacts maps a repo class to the version a manifest declared
	// `source: {type: artifact}` for. It is deliberately not folded into Paths:
	// an entry there means "the manifest named this working tree" and is taken
	// without a stat, which is tier two, while an artifact is tier three and
	// resolves through the cache under Home. Conflating them would report an
	// artifact's version with Source working-tree.
	Artifacts map[string]string
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
	return &Resolver{
		RepoHome: repoHome, Home: home, Lookup: lookup,
		Paths:     map[string]string{},
		Artifacts: map[string]string{},
	}
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

// artifactDir is the unpacked tree for a repo class a manifest declared an
// artifact source for, or "" when none was declared or none is cached.
//
// It never fetches. Resolution is offline by construction -- an apply that
// reached the internet without being asked is an apply that behaves differently
// on an aeroplane -- so a version that was never pulled resolves to no directory
// here and is reported by whoever checked before the deploy started.
func (r *Resolver) artifactDir(repoClass string) string {
	version := r.Artifacts[repoClass]
	if version == "" || r.Home == "" {
		return ""
	}
	if !artifact.Cached(r.Home, repoClass, version) {
		return ""
	}
	return artifact.Dir(r.Home, repoClass, version)
}

// PreemptedArtifact reports the working tree that will be used in place of a
// declared artifact source, or "" when none will be.
//
// Development beats distribution: a tree the user explicitly asked for still
// wins, because the whole purpose of $HMD_REPO_HOME is to run uncommitted
// changes and an artifact source that overrode it would make the platform
// untestable by the people who build it. But the substitution is exactly the
// kind that is invisible until it has cost an afternoon, so a caller is given
// what it needs to say so on the way past.
func (r *Resolver) PreemptedArtifact(repoClass string) string {
	if r.Artifacts[repoClass] == "" {
		return ""
	}
	if pinned := r.Lookup(VersionEnvVar(repoClass)); pinned != "" && !strings.EqualFold(pinned, "local") {
		return ""
	}
	tree, declared := r.treeDir(repoClass)
	if tree != "" && (declared || r.preferLocal(repoClass)) {
		return tree
	}
	return ""
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
	return "HMD_LOCAL_VERSION_" + envSuffix(repoClass)
}

// ImageEnvVar is the variable that names a full image reference to run for a
// service, bypassing version resolution and the image class entirely.
func ImageEnvVar(service string) string {
	return "HMD_LOCAL_IMAGE_" + envSuffix(service)
}

func envSuffix(name string) string {
	return strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
}

// Dir is the tree describing a repo class, or "" when there is none.
//
// Precedence follows ResolveVersion's, so the version, the dependencies and the
// deploy sources all come from one place: an explicitly declared path, then a
// checkout the user asked for, then a declared artifact, then the tree the
// binary carries, then whatever checkout happens to be lying around.
//
// A declared artifact that is not cached answers "" rather than falling through.
// Every remaining tier would return code that is not the version ResolveVersion
// reported, and a caller is better served by nothing than by the wrong tree.
func (r *Resolver) Dir(repoClass string) string {
	tree, declared := r.treeDir(repoClass)
	if tree != "" && (declared || r.preferLocal(repoClass)) {
		return tree
	}
	if r.Artifacts[repoClass] != "" {
		// Returned from here whether or not it is cached. Falling through to a
		// bundled tree or a checkout would hand back code that is not the
		// version ResolveVersion just reported, which is the whole failure this
		// tier exists to refuse -- and it would do it silently.
		return r.artifactDir(repoClass)
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
//  3. The version a manifest declared an artifact source for.
//  4. The tree the binary carries.
//  5. The version the BOM entry declared.
//  6. Whatever checkout happens to be under $HMD_REPO_HOME, as a last resort.
//  7. The sentinel.
//
// Tier three is NERD005 SPEC002's reversal of the last existing tier, and the
// only one of these not forced by prior art. An instance declared
// `source: {type: artifact}` resolves from the librarian even when a checkout of
// that class happens to be sitting in $HMD_REPO_HOME: the manifest asked for a
// version, a stale checkout is not that version, and substituting it produces a
// deploy that reports a version it did not use.
//
// It reports the declared version whether or not the artifact is cached, because
// an artifact is addressed *by* version and that version is known from the
// manifest either way. Whether the bytes are on this machine is a different
// question, asked before the deploy starts so it fails in seconds with the four
// causes told apart rather than nine hundred log lines in.
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
	if version := r.Artifacts[repoClass]; version != "" {
		// Root is the cached tree when there is one, and "" when there is not.
		// Resolve then falls back to r.Dir for the manifest, which answers ""
		// as well -- so the dependencies come from the BOM entry rather than
		// from some other tree of the same class, which is the mismatch
		// Source's doc comment warns about.
		return Resolution{Version: version, Source: SourceArtifact, Root: r.artifactDir(repoClass)}
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
		// Commands is the deploy phase's tool list. nsctl acts on exactly one
		// shape of it -- a single ["exec", ...argv] entry, see ExecCommand --
		// and reads the rest only to know it is not that shape. The inner
		// type is loose on purpose: real manifests carry object arguments
		// (`["docker", "build", {"is_windows": true}]`), and a reader typed
		// as strings would refuse repos it deploys today.
		Commands [][]any `json:"commands"`
		// Image is the OCI image an exec node runs in (NERD009 SPEC004).
		// Empty means projectbuilder, exactly as before the key existed.
		Image                string         `json:"image"`
		Dependencies         map[string]any `json:"dependencies"`
		DefaultConfiguration map[string]any `json:"default_configuration"`
	} `json:"deploy"`
	// Discovery is the BACON discovery block (summary, entry_points,
	// capabilities, related_docs), forwarded verbatim when a version is
	// registered so the deployment service's catalog can search it.
	Discovery map[string]any `json:"discovery"`
}

// LoadManifest reads a repo's meta-data/manifest.json.
//
// A missing manifest is not an error: not every repo class in a BOM has a
// working tree on this machine, and the BOM entry's own declarations stand in.
func (r *Resolver) LoadManifest(repoClass string) (*Manifest, error) {
	return r.loadManifestFrom(repoClass, r.Dir(repoClass))
}

func (r *Resolver) loadManifestFrom(repoClass, dir string) (*Manifest, error) {
	m, err := ReadManifest(dir)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", repoClass, err)
	}
	return m, nil
}

// ReadManifest reads the manifest under a repo class root, or nil, nil when
// the root is empty or carries none.
//
// Exported for the runner, which already holds the directory it is about to
// mount and must read the deploy commands from that tree and no other: the
// resolver's tiers exist to pick a tree, and once one is picked, everything
// about the node has to come from it.
func ReadManifest(dir string) (*Manifest, error) {
	if dir == "" {
		return nil, nil
	}
	data, err := os.ReadFile(filepath.Join(dir, "meta-data", "manifest.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading the manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing the manifest: %w", err)
	}
	return &m, nil
}

// ExecCommand is the argv a foreign node runs, or nil when the deploy phase is
// not an exec phase (NERD009 SPEC003).
//
// `exec` is BACON's spec-mandatory "run this command in the repo root" tool
// (hmd-docs-bacon toolsets.rst:74-91), and it is the whole phase when it is
// present: exactly one entry, nothing beside it. Two exec entries, or an exec
// beside a native tool, are refused rather than half-run -- the ordering and
// failure semantics of a multi-command foreign deploy are the author's
// business, and a guess about them is worse than a refusal that names the
// problem.
func (m *Manifest) ExecCommand() ([]string, error) {
	if m == nil {
		return nil, nil
	}
	execs := 0
	for _, entry := range m.Deploy.Commands {
		if len(entry) > 0 && entry[0] == "exec" {
			execs++
		}
	}
	switch {
	case execs == 0:
		return nil, nil
	case execs > 1:
		return nil, fmt.Errorf("deploy.commands declares %d exec commands; exactly one exec command per phase is supported", execs)
	case len(m.Deploy.Commands) > 1:
		return nil, fmt.Errorf("deploy.commands declares exec beside %d other command(s); exec cannot be combined with other deploy commands", len(m.Deploy.Commands)-1)
	}
	entry := m.Deploy.Commands[0]
	if len(entry) < 2 {
		return nil, fmt.Errorf("deploy.commands declares exec with no command to run")
	}
	argv := make([]string, 0, len(entry)-1)
	for i, arg := range entry[1:] {
		s, ok := arg.(string)
		if !ok {
			return nil, fmt.Errorf("deploy.commands exec argument %d is %T, not a string", i+1, arg)
		}
		argv = append(argv, s)
	}
	return argv, nil
}

// Resolve reports the version, dependencies and default configuration to
// register a repo class under. It satisfies bom.VersionResolver.
//
// The dependencies and configuration come from the same place the version did,
// so the registered RepoClassVersion describes one coherent build.
func (r *Resolver) Resolve(repoClass, declared string) (string, map[string]any, map[string]any, error) {
	resolution, manifest, err := r.resolveManifest(repoClass, declared)
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

// ResolveDiscovery reports the BACON discovery block to register alongside the
// version Resolve reports, read from the same tree. It satisfies
// bom.DiscoveryResolver. nil when there is no manifest or no discovery section,
// so the caller leaves the key off the registration and the service default
// applies.
func (r *Resolver) ResolveDiscovery(repoClass, declared string) (map[string]any, error) {
	_, manifest, err := r.resolveManifest(repoClass, declared)
	if err != nil || manifest == nil || len(manifest.Discovery) == 0 {
		return nil, err
	}
	return manifest.Discovery, nil
}

// resolveManifest is the tree resolution Resolve and ResolveDiscovery share,
// so a version and everything registered with it come from one build.
func (r *Resolver) resolveManifest(repoClass, declared string) (Resolution, *Manifest, error) {
	resolution := r.ResolveVersion(repoClass, declared)
	// From the tree the version came from, when there was one. Reading the
	// version from one place and the dependencies from another describes a
	// build that never existed, which is what the Source doc warns about and
	// what re-deriving the directory here used to risk.
	dir := resolution.Root
	if dir == "" && resolution.Source != SourceArtifact {
		// Not for an artifact whose tree is not cached. r.Dir would answer with
		// the bundled tree or a checkout of the same class, and reading the
		// dependencies of one version out of another build is precisely the
		// mismatch Source's doc comment warns about -- worse here than
		// elsewhere, because the version reported alongside them is the
		// artifact's.
		dir = r.Dir(repoClass)
	}
	manifest, err := r.loadManifestFrom(repoClass, dir)
	return resolution, manifest, err
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
