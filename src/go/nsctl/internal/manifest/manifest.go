// Package manifest reads and writes an environment's desired state.
//
// A manifest at $HMD_HOME/environments/<slug>.{yaml,yml,json} declares the
// repo instances a user wants deployed on top of the substrate nsctl ships.
// It is the source of truth: `nsctl repo add` and `repo remove` edit this
// file, and `nsctl env apply` reconciles the environment to it. Editing it by
// hand and running `env apply` is the same operation, which is the point --
// the imperative verbs are wrappers, not a second way to say the same thing.
//
// The format is the one env_manifest.py already defines, so a manifest written
// by either front end is readable by the other. Two keys are Python-only:
// `plugins` and `plugin_config` configure a plugin-discovery mechanism nsctl
// deliberately does not implement (a RepoClass declares what it produces and
// needs; a parallel per-plugin inventory is the copy that goes stale). nsctl
// preserves both keys verbatim on save and reports them through Unsupported so
// a caller can say plainly that they had no effect.
package manifest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Version is the manifest schema version nsctl writes and understands.
const Version = 1

// PathOverride names an explicit manifest path, bypassing discovery. Useful
// for one-off runs and tests, and the same variable the Python CLI honours.
const PathOverride = "HMD_LOCAL_ENV_MANIFEST"

// Extensions are tried in this order by Find. YAML first: it is the authoring
// format, JSON is accepted for generated or checked-in manifests.
var Extensions = []string{".yaml", ".yml", ".json"}

// Source kinds a declared instance can deploy from: a working tree on this
// machine, or a versioned artifact held by the control plane's Artifact
// Librarian (NERD005).
const (
	SourceLocal    = "local"
	SourceArtifact = "artifact"
)

// DefaultArtifactType is the librarian content item type an artifact source
// resolves from when it names none. `build` is what `hmd build` publishes and
// what every pin in this repository's own pre_build_artifacts uses.
const DefaultArtifactType = "build"

// artifactTypePattern is a shape, deliberately, and not an allowlist of the
// librarian's content item types. The installer variants' exact spellings are
// not present in this repository, and a wrong allowlist rejects a legitimate
// manifest with an error its author cannot fix -- whereas a wrong type fails on
// the fetch with librarian.ErrNotPublished naming the exact content path, which
// is already clear and actionable. NERD005 SPEC001 records the softening.
var artifactTypePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

var knownSources = []string{SourceArtifact, SourceLocal}

// Lookup reads an environment variable. Injected so tests need no t.Setenv,
// which panics under t.Parallel.
type Lookup = func(string) string

// Source says where an instance's code comes from: the working tree for a
// local source, or the librarian content item type for an artifact source.
type Source struct {
	Type string `yaml:"type,omitempty" json:"type,omitempty"`
	Path string `yaml:"path,omitempty" json:"path,omitempty"`
	// ArtifactType is the librarian content item type, for an artifact source
	// distributed as something other than a build. Meaningless for a local
	// source.
	ArtifactType string `yaml:"artifact_type,omitempty" json:"artifact_type,omitempty"`
}

// Repo is one declared repo instance.
type Repo struct {
	InstanceName          string         `yaml:"instance_name" json:"instance_name"`
	RepoClassName         string         `yaml:"repo_class_name" json:"repo_class_name"`
	Source                *Source        `yaml:"source,omitempty" json:"source,omitempty"`
	Version               string         `yaml:"version,omitempty" json:"version,omitempty"`
	InstanceConfiguration map[string]any `yaml:"instance_configuration,omitempty" json:"instance_configuration,omitempty"`
	Dependencies          map[string]any `yaml:"dependencies,omitempty" json:"dependencies,omitempty"`
}

// SourceType is the declared kind, defaulting to a local working tree.
func (r Repo) SourceType() string {
	if r.Source == nil || r.Source.Type == "" {
		return SourceLocal
	}
	return r.Source.Type
}

// ArtifactType is the librarian content item type to resolve this instance
// from, defaulting to build.
func (r Repo) ArtifactType() string {
	if r.Source == nil || r.Source.ArtifactType == "" {
		return DefaultArtifactType
	}
	return r.Source.ArtifactType
}

// RepoPath is the working tree this instance deploys from, or "" when it
// cannot be resolved. source.path wins; otherwise $HMD_REPO_HOME/<class>, the
// same convention the runner uses to find the directory it mounts.
func (r Repo) RepoPath(lookup Lookup) string {
	if r.SourceType() != SourceLocal {
		return ""
	}
	if r.Source != nil && r.Source.Path != "" {
		return os.Expand(r.Source.Path, lookup)
	}
	repoHome := lookup("HMD_REPO_HOME")
	if repoHome == "" {
		return ""
	}
	return filepath.Join(repoHome, r.RepoClassName)
}

// Manifest is a parsed environment manifest.
type Manifest struct {
	Version int    `yaml:"version" json:"version"`
	Name    string `yaml:"name" json:"name"`
	Repos   []Repo `yaml:"repos" json:"repos"`

	// Profiles are the local profiles this environment was created with, when it
	// was created from a repository (NERD010 SPEC005).
	//
	// Recorded rather than recomputed, and that is not bookkeeping: without it a
	// later bare `nsctl env apply` falls back to the repository's
	// default_profiles and reconciles away instances the user explicitly asked
	// for -- a delta-apply that is correct according to a file nobody re-read.
	Profiles []string `yaml:"profiles,omitempty" json:"profiles,omitempty"`

	// Substrate is how much infrastructure this environment runs beneath its
	// declared instances: none, core or full (NERD014 SPEC002). Absent means
	// full, so an environment written before the key existed keeps the
	// behaviour it had.
	//
	// Recorded here rather than in the registry because the Python front end
	// rebuilds the registry from the fields it knows and would drop this one
	// on its first save; it reads this file with .get() and writes it never.
	// And recorded rather than recomputed for Profiles' reason: a later bare
	// `nsctl env apply` must not add or reconcile away a cluster according to
	// a rule nobody re-read.
	Substrate string `yaml:"substrate,omitempty" json:"substrate,omitempty"`

	// Bindings maps a repository's declared want -- a dependency role, a
	// companion's declared instance_name, or the repository's own repo class --
	// to the instance name it was given in this environment.
	//
	// It exists because a lock names no instances. Two engineers may deploy one
	// repo class at one locked version under different local instance names and
	// must still resolve the same roles, so a role is what travels between
	// machines and this file is where the local naming lives. On a re-apply this
	// map is authoritative, which is what stops a renamed instance being
	// duplicated under its default name.
	Bindings map[string]string `yaml:"bindings,omitempty" json:"bindings,omitempty"`

	// Stacks records every stack added to this environment (NERD017
	// SPEC009): what was installed, from where, at which digest, and the
	// bindings its planner chose. A stack's bindings live here and not in
	// Bindings so a stack and a --from-repo subject can share an environment
	// without one apply overwriting the other's naming. Optional; the schema
	// version is unchanged.
	Stacks []StackRecord `yaml:"stacks,omitempty" json:"stacks,omitempty"`

	// Extra carries every top-level key nsctl does not model, so writing a
	// manifest back never drops what the Python front end put there.
	Extra map[string]any `yaml:",inline" json:"-"`

	// Path is where this manifest was read from, empty for one built in
	// memory. Not serialised.
	Path string `yaml:"-" json:"-"`

	// Scope is which manifest this is. It decides the reserved names and the
	// wording of every message, and it is set by the loader rather than read
	// from the file: which directory a manifest was found in is what makes it
	// an environment's or the control plane's, not anything it says about
	// itself. Not serialised.
	Scope Scope `yaml:"-" json:"-"`
}

// StackRecord is one added stack.
type StackRecord struct {
	Name    string `yaml:"name" json:"name"`
	Version string `yaml:"version" json:"version"`
	// Ref is the source reference without a version; Digest is the manifest
	// digest that was installed.
	Ref    string `yaml:"ref" json:"ref"`
	Digest string `yaml:"digest,omitempty" json:"digest,omitempty"`
	// Profiles and Bindings are what the stack's planner recorded, with the
	// meanings Manifest.Profiles and Manifest.Bindings have.
	Profiles []string          `yaml:"profiles,omitempty" json:"profiles,omitempty"`
	Bindings map[string]string `yaml:"bindings,omitempty" json:"bindings,omitempty"`
}

// Stack returns the record for a stack name.
func (m *Manifest) Stack(name string) (StackRecord, int, bool) {
	for i, s := range m.Stacks {
		if s.Name == name {
			return s, i, true
		}
	}
	return StackRecord{}, -1, false
}

// SetStack adds or replaces a record by name.
func (m *Manifest) SetStack(rec StackRecord) {
	if _, i, ok := m.Stack(rec.Name); ok {
		m.Stacks[i] = rec
		return
	}
	m.Stacks = append(m.Stacks, rec)
}

// RemoveStack drops a record, reporting whether there was one.
func (m *Manifest) RemoveStack(name string) bool {
	_, i, ok := m.Stack(name)
	if !ok {
		return false
	}
	m.Stacks = append(m.Stacks[:i], m.Stacks[i+1:]...)
	return true
}

// Unsupported names the keys present in this manifest that nsctl ignores.
// Preserved on save, but they change nothing about what nsctl deploys.
func (m *Manifest) Unsupported() []string {
	var keys []string
	for _, k := range []string{"plugins", "plugin_config"} {
		if _, ok := m.Extra[k]; ok {
			keys = append(keys, k)
		}
	}
	return keys
}

// Repo returns the declaration for an instance name.
func (m *Manifest) Repo(instanceName string) (Repo, bool) {
	for _, r := range m.Repos {
		if r.InstanceName == instanceName {
			return r, true
		}
	}
	return Repo{}, false
}

// Root is where authored manifests live.
//
// Deliberately not under .cache: a manifest is input a developer edits and may
// commit, not machine state `env purge` is free to delete.
func Root(home string) string { return filepath.Join(home, "environments") }

// ControlPlanePathOverride names an explicit control-plane manifest path,
// bypassing discovery. The analogue of PathOverride, and used for the same
// reasons: one-off runs, and tests that must not depend on $HMD_HOME's layout.
const ControlPlanePathOverride = "HMD_LOCAL_CP_MANIFEST"

// ControlPlaneRoot is where the control-plane manifest lives.
//
// .config/ because that is where authored configuration already lives --
// hmd.env, uv.toml, pip.conf -- and because it is not .cache/. The sentence
// that keeps environment manifests out of .cache decides this file's location
// too (NERD004 SPEC001).
func ControlPlaneRoot(home string) string { return filepath.Join(home, ".config") }

// ControlPlaneDefaultPath is where a control-plane manifest is created when
// there is none.
func ControlPlaneDefaultPath(home string) string {
	return filepath.Join(ControlPlaneRoot(home), ControlPlaneName+".yaml")
}

// FindControlPlane locates the control-plane manifest, returning "" when there
// is none.
func FindControlPlane(home string, lookup Lookup) string {
	if override := lookup(ControlPlanePathOverride); override != "" {
		path := os.Expand(override, lookup)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
		return ""
	}
	for _, ext := range Extensions {
		candidate := filepath.Join(ControlPlaneRoot(home), ControlPlaneName+ext)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

// LoadControlPlane reads $HMD_HOME/.config/control-plane.yaml.
//
// No manifest is not an error: it returns nil and the control plane starts
// exactly as it does today, deploying nothing extra. That default is NERD004's
// requirement rather than a convenience -- an extension surface whose cost is
// paid by people who do not use it is the wrong surface -- and it is the same
// decision Load already makes for an environment with no manifest.
func LoadControlPlane(home string, lookup Lookup) (*Manifest, error) {
	path := FindControlPlane(home, lookup)
	if path == "" {
		return nil, nil
	}
	return LoadFileScoped(path, lookup, ScopeControlPlane)
}

// Find locates the manifest for a slug, returning "" when it has none.
func Find(home, slug string, lookup Lookup) string {
	if override := lookup(PathOverride); override != "" {
		path := os.Expand(override, lookup)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
		return ""
	}
	for _, ext := range Extensions {
		candidate := filepath.Join(Root(home), slug+ext)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

// Load reads the manifest for a slug.
//
// A slug with no manifest is not an error: it returns nil, and the caller
// deploys the substrate alone. That is the empty environment `env add` makes.
func Load(home, slug string, lookup Lookup) (*Manifest, error) {
	path := Find(home, slug, lookup)
	if path == "" {
		return nil, nil
	}
	return LoadFile(path, lookup)
}

// LoadFile parses and validates the environment manifest at path.
func LoadFile(path string, lookup Lookup) (*Manifest, error) {
	return LoadFileScoped(path, lookup, ScopeEnvironment)
}

// LoadFileScoped parses and validates the manifest at path in a given scope.
func LoadFileScoped(path string, lookup Lookup, scope Scope) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading the %s %s: %w", scope.Noun(), path, err)
	}
	m, err := Parse(data, filepath.Ext(path))
	if err != nil {
		return nil, fmt.Errorf("parsing the %s %s: %w", scope.Noun(), path, err)
	}
	m.Path = path
	m.Scope = scope
	if problems := m.Validate(lookup); len(problems) > 0 {
		return nil, fmt.Errorf("the %s %s is not valid:\n%s",
			scope.Noun(), path, "  - "+strings.Join(problems, "\n  - "))
	}
	return m, nil
}

// Parse builds a Manifest from raw bytes. ext routes the decoder so a syntax
// error names the format the author actually wrote.
func Parse(data []byte, ext string) (*Manifest, error) {
	m := &Manifest{Version: Version}
	if strings.EqualFold(ext, ".json") {
		// A JSON manifest round-trips through YAML: yaml.v3 parses JSON, and
		// routing it here keeps the inline Extra map working, which
		// encoding/json cannot express.
		var doc any
		if err := json.Unmarshal(data, &doc); err != nil {
			return nil, err
		}
		reencoded, err := yaml.Marshal(doc)
		if err != nil {
			return nil, err
		}
		data = reencoded
	}
	if err := yaml.Unmarshal(data, m); err != nil {
		return nil, err
	}
	return m, nil
}

// Save writes the manifest to path, creating the directory as needed.
//
// Comments and key order in a hand-edited file are lost: the manifest is
// re-serialised from the parsed document rather than patched in place.
// Unmodelled keys survive, values and all.
func (m *Manifest) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	var (
		data []byte
		err  error
	)
	if strings.EqualFold(filepath.Ext(path), ".json") {
		data, err = m.marshalJSON()
	} else {
		data, err = yaml.Marshal(m)
	}
	if err != nil {
		return fmt.Errorf("serialising the %s: %w", m.Scope.Noun(), err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing the %s %s: %w", m.Scope.Noun(), path, err)
	}
	m.Path = path
	return nil
}

// marshalJSON round-trips through YAML so the inline Extra map is flattened
// the same way it is for a YAML manifest.
func (m *Manifest) marshalJSON() ([]byte, error) {
	intermediate, err := yaml.Marshal(m)
	if err != nil {
		return nil, err
	}
	var doc any
	if err := yaml.Unmarshal(intermediate, &doc); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// DefaultPath is where a manifest for a slug is created when it has none.
func DefaultPath(home, slug string) string {
	return filepath.Join(Root(home), slug+".yaml")
}

// Validate reports every problem at once rather than failing on the first, so
// a user fixing a hand-edited manifest sees the whole list.
//
// reserved is checked against the instance names the manifest's scope owns:
// declaring one would make it eligible for removal the moment it left the
// manifest, and nothing user-declared may claim a name nsctl creates.
func (m *Manifest) Validate(lookup Lookup) []string {
	var problems []string

	switch {
	case m.Name == "":
		problems = append(problems, "'name' is required")
	case m.Scope == ScopeControlPlane && m.Name != ControlPlaneName:
		// The only thing another name could mean is that this file was meant
		// to be an environment manifest and landed in .config/.
		problems = append(problems, fmt.Sprintf(
			"'name' must be %q in a control-plane manifest, got %q -- an environment manifest belongs in %s",
			ControlPlaneName, m.Name, "$HMD_HOME/environments/"))
	}
	if m.Version != Version {
		problems = append(problems,
			fmt.Sprintf("'version' must be %d, got %d", Version, m.Version))
	}
	if _, err := ParseSubstrate(m.Substrate); err != nil {
		problems = append(problems, "'substrate': "+err.Error())
	}

	seen := map[string]int{}
	for i, r := range m.Repos {
		where := fmt.Sprintf("repos[%d]", i)
		if r.InstanceName == "" {
			problems = append(problems, where+": 'instance_name' is required")
		} else {
			where = fmt.Sprintf("repos[%d] (%q)", i, r.InstanceName)
			if first, dup := seen[r.InstanceName]; dup {
				problems = append(problems, fmt.Sprintf(
					"%s: duplicate instance_name, already declared at repos[%d]", where, first))
			} else {
				seen[r.InstanceName] = i
			}
			if m.Scope.Reserved(r.InstanceName) {
				problems = append(problems, fmt.Sprintf(
					"%s: %q %s and cannot be declared",
					where, r.InstanceName, m.Scope.reservedReason(r.InstanceName)))
			}
		}
		if r.RepoClassName == "" {
			problems = append(problems, where+": 'repo_class_name' is required")
		}
		problems = append(problems, r.validateSource(where, lookup, m.Scope)...)
		problems = append(problems, r.validateDependencies(where)...)
	}

	keys := make([]string, 0, len(m.Bindings))
	for k := range m.Bindings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if strings.TrimSpace(m.Bindings[k]) == "" {
			problems = append(problems, fmt.Sprintf(
				"bindings[%q]: an instance name is required -- a binding to nothing would silently"+
					" re-default on the next apply", k))
		}
	}

	stackNames := map[string]bool{}
	for i, st := range m.Stacks {
		where := fmt.Sprintf("stacks[%d]", i)
		switch {
		case st.Name == "":
			problems = append(problems, where+": 'name' is required")
		case stackNames[st.Name]:
			problems = append(problems, fmt.Sprintf("%s: duplicate stack %q", where, st.Name))
		default:
			stackNames[st.Name] = true
		}
		if st.Version == "" {
			problems = append(problems, where+": 'version' is required")
		}
		if st.Ref == "" {
			problems = append(problems, where+": 'ref' is required")
		}
		for k, v := range st.Bindings {
			if strings.TrimSpace(v) == "" {
				problems = append(problems, fmt.Sprintf("%s bindings[%q]: an instance name is required", where, k))
			}
		}
	}
	return problems
}

func (r Repo) validateSource(where string, lookup Lookup, scope Scope) []string {
	switch r.SourceType() {
	case SourceLocal:
	case SourceArtifact:
		// Returned from here rather than falling through, so that none of the
		// working-tree checks below run: an artifact source resolves from the
		// librarian even with $HMD_REPO_HOME pointed at an empty directory,
		// which is NERD005 SPEC002's reversal of the last existing tier.
		//
		// Note this also means the checks run in *both* scopes. The
		// ScopeControlPlane exemption further down is about missing host
		// state; everything here is a file-format error, and a control-plane
		// manifest is no more entitled to a malformed one.
		var problems []string
		if strings.TrimSpace(r.Version) == "" {
			problems = append(problems, where+": 'version' is required for an artifact source"+
				" -- an artifact is addressed by version, and there is no meta-data/VERSION to read it from")
		}
		if r.Source != nil && r.Source.Path != "" {
			problems = append(problems, where+
				": 'source.path' is meaningless for an artifact source;"+
				" use 'type: "+SourceLocal+"' to deploy from a working tree")
		}
		if t := r.ArtifactType(); !artifactTypePattern.MatchString(t) {
			problems = append(problems, fmt.Sprintf(
				"%s: 'source.artifact_type' %q is not a librarian content item type", where, t))
		}
		return problems
	default:
		return []string{fmt.Sprintf("%s: unknown source type %q; expected one of %s",
			where, r.SourceType(), strings.Join(knownSources, ", "))}
	}
	if r.RepoClassName == "" {
		return nil
	}
	if scope == ScopeControlPlane {
		// Deliberately not checked here. A control-plane manifest that failed
		// validation would take *every* extension down with the one whose
		// checkout is missing, which is exactly what NERD004 SPEC006 forbids.
		// internal/cpext reports a missing tree against the one instance that
		// has it, and the rest are applied.
		return nil
	}
	// A local instance deploys by mounting its working tree into the
	// projectbuilder container, so the tree has to exist before the deploy
	// starts rather than failing several minutes in.
	path := r.RepoPath(lookup)
	if path == "" {
		return []string{where + ": cannot locate a local working tree -- set 'source.path' or export HMD_REPO_HOME"}
	}
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		return []string{fmt.Sprintf("%s: local repo path does not exist: %s", where, path)}
	}
	return nil
}

// validateDependencies enforces the change_set schema's variant C: each value
// is an instance name or a list of them. Anything else fails server-side with
// a message that does not name the offender.
func (r Repo) validateDependencies(where string) []string {
	var problems []string
	roles := make([]string, 0, len(r.Dependencies))
	for role := range r.Dependencies {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	for _, role := range roles {
		switch target := r.Dependencies[role].(type) {
		case string:
		case []any:
			for _, t := range target {
				if _, ok := t.(string); !ok {
					problems = append(problems, fmt.Sprintf(
						"%s: dependency %q must be an instance name or a list of instance names", where, role))
					break
				}
			}
		default:
			problems = append(problems, fmt.Sprintf(
				"%s: dependency %q must be an instance name or a list of instance names", where, role))
		}
	}
	return problems
}
