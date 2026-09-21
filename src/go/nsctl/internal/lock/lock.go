// Package lock reads and writes neuronsphere.lock: the generated, checked-in
// file that pins every RepoClass a repository's local environment declares to a
// concrete version and librarian content path.
//
// The pairing is the one every reader already knows -- a hand-authored
// declaration beside a generated lock, as with pyproject.toml and uv.lock, or
// Cargo.toml and Cargo.lock. That is worth something on its own: nobody has to
// be told which file to edit. NERD010 SPEC002 and SPEC007.
//
// # Why the root, and why TOML
//
// The root, because NERD009 SPEC006 already names a repo-root neuronsphere.toml
// as the direction the platform intends, and a *generated* file can go there
// today where the manifest cannot: the manifest is held back only by
// hmd_lib_manifest's path list not looking at the root, and nothing but nsctl
// ever reads the lock. Putting it under meta-data/ would mean moving it on the
// day of the flip for no benefit in the meantime.
//
// TOML, because it is what the platform is converging on, because BACON already
// supports it, and because go-toml/v2 is already linked into the binary.
//
// # Keyed by repo class, never by instance
//
// A lock entry is a repo class, a version, a content path, and the dependency
// *roles* it fills. It never names an instance, and that is load bearing rather
// than incidental: two engineers may deploy the same class at the same locked
// version under different local instance names, and both must still resolve the
// same roles from the same checked-in lock. A role is portable across machines
// and an instance name is not. What an instance is called locally is the
// environment manifest's business -- see NERD010 SPEC005's naming order.
//
// It follows that `satisfies` is a list. One class routinely fills several
// roles: hmd-inf-credentials fills users, ro-users and adhoc-users in
// hmd-inf-trino, and hmd-inf-eks-node-group fills compute and worker-compute.
// A single string could not say so.
//
// # Nothing here reaches a network
//
// Building a lock contacts nothing; `nsctl lock --from-env` contacts
// ms-deployment, which is local, and that call is the command's, not this
// package's. Checking a lock contacts nothing either -- `--check` has to run on
// an aeroplane and in a CI job with no librarian credential, so it deliberately
// does not verify that a pinned version still exists anywhere.
package lock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/atomicfile"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/librarian"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/localspec"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
)

// Version is the lock schema version nsctl writes and understands.
const Version = 1

// FileName is the lock, at the repository root.
const FileName = "neuronsphere.lock"

// ErrAbsent reports a repository with no lock. Distinguished because its fix is
// `nsctl lock` and a malformed lock's is not.
var ErrAbsent = errors.New("no " + FileName)

// Entry is one pinned repo class.
type Entry struct {
	RepoClassName string `toml:"repo_class_name"`
	Version       string `toml:"version"`
	// Profiles gating this entry, unioned across every want that named the
	// class. Empty means unconditional, and is written as an empty list rather
	// than an absent key so a reader never has to guess which it is looking at.
	Profiles []string `toml:"profiles"`
	// Satisfies is the dependency roles this entry was resolved for, sorted, and
	// absent for a companion -- which is what lets a reader tell a real graph
	// edge from a test fixture.
	Satisfies   []string `toml:"satisfies,omitempty"`
	ContentPath string   `toml:"content_path"`
	// Digest is the sha256 of the build zip ContentPath names, when something
	// has seen the bytes: `nsctl lock` from the cache, or a pull. Optional,
	// and omitted rather than empty so a lock written before it existed is
	// byte-identical on rewrite. The schema stays at Version 1 because Parse
	// is a non-strict decode and an older nsctl simply ignores the key.
	// NERD017 SPEC007.
	Digest string `toml:"digest,omitempty"`
	// Source is an OCI reference, without a version, that serves this
	// class's build zip as an artifact (NERD016 SPEC009): where a consumer
	// with no tenant fetches it. Optional; content_path stays the
	// librarian's address for the paid path.
	Source string `toml:"source,omitempty"`
}

// Lock is a parsed neuronsphere.lock.
type Lock struct {
	// Version is the lock *schema* version, not any repo's version.
	Version       int    `toml:"version"`
	RepoClassName string `toml:"repo_class_name"`
	// GeneratedFrom is "env:<name>", "pins" or "manifest". A reader should treat
	// "manifest" as weaker than "env:<name>": resolving each range to its newest
	// satisfying version is a guess that they are mutually compatible, where a
	// running environment is proof.
	GeneratedFrom string  `toml:"generated_from"`
	Resolved      []Entry `toml:"resolved"`
}

// Path is where a repository's lock lives.
func Path(repoDir string) string { return filepath.Join(repoDir, FileName) }

// Read parses the lock at a repository root. A repository with none returns
// ErrAbsent.
func Read(repoDir string) (*Lock, error) {
	data, err := os.ReadFile(Path(repoDir))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, fmt.Errorf("%s: %w", Path(repoDir), ErrAbsent)
	case err != nil:
		return nil, fmt.Errorf("reading %s: %w", Path(repoDir), err)
	}
	l, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", Path(repoDir), err)
	}
	return l, nil
}

// Parse builds a Lock from raw TOML.
//
// An unknown schema version is refused with the version found and the versions
// supported, and nothing is returned with it: a silently-ignored `resolved`
// entry is a missing instance nobody goes looking for, so a half-honoured lock
// is worse than none.
func Parse(data []byte) (*Lock, error) {
	var l Lock
	if err := toml.Unmarshal(data, &l); err != nil {
		return nil, fmt.Errorf("parsing the lock: %w", err)
	}
	if l.Version != Version {
		return nil, fmt.Errorf(
			"lock schema version %d is not supported; this nsctl understands %d."+
				"\nUpgrade nsctl, or regenerate the lock with `nsctl lock`", l.Version, Version)
	}
	return &l, nil
}

// Write replaces a repository's lock.
//
// Through atomicfile rather than os.WriteFile: this is a file a pre-commit hook
// or a CI job may be reading at the same moment, and a plain truncate-and-write
// leaves a window in which they see half of one.
func Write(repoDir string, l *Lock) error {
	data, err := Marshal(l)
	if err != nil {
		return err
	}
	return atomicfile.Write(Path(repoDir), data, 0o644, 0o755)
}

// Marshal is the lock's bytes, as Write would put them on disk. A stack
// artifact carries exactly these as its config blob (NERD017 SPEC002).
func Marshal(l *Lock) ([]byte, error) {
	data, err := toml.Marshal(l)
	if err != nil {
		return nil, fmt.Errorf("serialising the lock: %w", err)
	}
	return data, nil
}

// Entry returns the pin for a repo class.
func (l *Lock) Entry(repoClass string) (Entry, bool) {
	for _, e := range l.Resolved {
		if e.RepoClassName == repoClass {
			return e, true
		}
	}
	return Entry{}, false
}

// SetDigest records the zip digest for a repo class, reporting whether the
// class is in the lock.
func (l *Lock) SetDigest(repoClass, digest string) bool {
	for i := range l.Resolved {
		if l.Resolved[i].RepoClassName == repoClass {
			l.Resolved[i].Digest = digest
			return true
		}
	}
	return false
}

// VerifyDigest refuses bytes whose digest disagrees with the entry's. An
// entry with no recorded digest cannot disagree: nothing was promised.
func (e Entry) VerifyDigest(digest string) error {
	if e.Digest == "" || e.Digest == digest {
		return nil
	}
	return fmt.Errorf("%s@%s: the lock pins %s but the artifact is %s; "+
		"the publisher's lock and the published bytes disagree, so this is not the stack they tested",
		e.RepoClassName, e.Version, e.Digest, digest)
}

// AllProfiles is every profile the lock mentions, sorted. What --all-profiles
// activates.
func (l *Lock) AllProfiles() []string {
	seen := map[string]bool{}
	for _, e := range l.Resolved {
		for _, p := range e.Profiles {
			seen[p] = true
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Build groups wants by repo class and pins each to the version versionOf
// returns, which is "" for one it could not resolve.
//
// Every want is pinned, whatever profile gates it. Activation is a read-time
// filter, and a lock covering only the profile in use when it was generated
// would force a re-resolve -- a network trip, and therefore a different answer
// -- the first time anybody switched profiles, which defeats having a lock.
//
// The wants it could not resolve come back beside the lock rather than as an
// error, so a caller can report every failure at once with the remedies for
// each instead of one per run. A genuine conflict -- one class two wants pin
// apart -- is the error, because a lock cannot pin a class twice and picking
// either silently would be a version nobody chose.
//
// A bound want (localspec.Want.Bind) is skipped: the role is filled by an
// instance the environment provides, nothing of that class is deployed for it,
// and a pin nobody reads would only go stale.
func Build(repoClass, generatedFrom string, wants []localspec.Want,
	versionOf func(localspec.Want) string) (*Lock, []localspec.Want, error) {

	type group struct {
		version   string
		profiles  map[string]bool
		satisfies map[string]bool
		// unconditional records that some want named this class with no profile
		// at all, which makes the class unconditional however many gated wants
		// also name it: a profile is a filter, and something that always starts
		// cannot be filtered out by a profile some other entry named.
		unconditional bool
	}
	groups := map[string]*group{}
	var classes []string
	var unresolved []localspec.Want

	for _, w := range wants {
		if w.Bind != "" {
			continue
		}
		version := strings.TrimSpace(versionOf(w))
		if version == "" {
			unresolved = append(unresolved, w)
			continue
		}
		g, seen := groups[w.RepoClassName]
		if !seen {
			g = &group{version: version, profiles: map[string]bool{}, satisfies: map[string]bool{}}
			groups[w.RepoClassName] = g
			classes = append(classes, w.RepoClassName)
		}
		if g.version != version {
			return nil, nil, fmt.Errorf(
				"%s is pinned to two versions: %s and %s."+
					"\nA lock pins a repo class once, so the declarations that want them must agree",
				w.RepoClassName, g.version, version)
		}
		if len(w.Profiles) == 0 {
			g.unconditional = true
		}
		for _, p := range w.Profiles {
			g.profiles[p] = true
		}
		for _, r := range w.Satisfies {
			g.satisfies[r] = true
		}
	}

	// Sorted, so a lock generated twice from one manifest is the same bytes.
	// Otherwise every regeneration is a diff and nobody reviews them.
	sort.Strings(classes)
	l := &Lock{Version: Version, RepoClassName: repoClass, GeneratedFrom: generatedFrom}
	for _, class := range classes {
		g := groups[class]
		e := Entry{
			RepoClassName: class,
			Version:       g.version,
			Profiles:      []string{},
			Satisfies:     keys(g.satisfies),
			// The librarian's own grammar, called rather than copied.
			ContentPath: librarian.Spec{
				Name: class, Version: g.version, ItemType: manifest.DefaultArtifactType,
			}.ContentPath(),
		}
		if !g.unconditional {
			e.Profiles = keys(g.profiles)
		}
		l.Resolved = append(l.Resolved, e)
	}
	return l, unresolved, nil
}

// Check reports whether a lock still covers what a manifest declares, contacting
// nothing.
//
// The two directions are returned separately because they have different fixes:
// a declared want with no entry means the lock is stale and `nsctl lock` fixes
// it, while an entry no longer declared is stale in the other direction and is
// a warning, since a developer mid-refactor should not be blocked by it.
func Check(l *Lock, wants []localspec.Want) (missing, extra []string) {
	pinned := map[string]bool{}
	for _, e := range l.Resolved {
		pinned[e.RepoClassName] = true
	}
	declared := map[string]bool{}
	for _, w := range wants {
		if w.Bind != "" {
			continue
		}
		declared[w.RepoClassName] = true
		if !pinned[w.RepoClassName] {
			missing = append(missing, w.RepoClassName)
		}
	}
	for _, e := range l.Resolved {
		if !declared[e.RepoClassName] {
			extra = append(extra, e.RepoClassName)
		}
	}
	missing = dedupe(missing)
	extra = dedupe(extra)
	return missing, extra
}

func keys(set map[string]bool) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func dedupe(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
