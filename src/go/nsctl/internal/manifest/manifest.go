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

// Source kinds a declared instance can deploy from. Only a local working tree
// is supported today; `artifact` is reserved so a manifest written against the
// eventual name fails with "not supported yet" rather than "unknown".
const (
	SourceLocal    = "local"
	SourceArtifact = "artifact"
)

var knownSources = []string{SourceArtifact, SourceLocal}

// Lookup reads an environment variable. Injected so tests need no t.Setenv,
// which panics under t.Parallel.
type Lookup = func(string) string

// Source locates the working tree an instance deploys from.
type Source struct {
	Type string `yaml:"type,omitempty" json:"type,omitempty"`
	Path string `yaml:"path,omitempty" json:"path,omitempty"`
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

	// Extra carries every top-level key nsctl does not model, so writing a
	// manifest back never drops what the Python front end put there.
	Extra map[string]any `yaml:",inline" json:"-"`

	// Path is where this manifest was read from, empty for one built in
	// memory. Not serialised.
	Path string `yaml:"-" json:"-"`
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

// LoadFile parses and validates the manifest at path.
func LoadFile(path string, lookup Lookup) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading the environment manifest %s: %w", path, err)
	}
	m, err := Parse(data, filepath.Ext(path))
	if err != nil {
		return nil, fmt.Errorf("parsing the environment manifest %s: %w", path, err)
	}
	m.Path = path
	if problems := m.Validate(lookup); len(problems) > 0 {
		return nil, fmt.Errorf("the environment manifest %s is not valid:\n%s",
			path, "  - "+strings.Join(problems, "\n  - "))
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
		return fmt.Errorf("serialising the environment manifest: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing the environment manifest %s: %w", path, err)
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
// reserved is checked against the substrate instance names the caller owns:
// declaring one would make it eligible for removal the moment it left the
// manifest, and nothing user-declared may claim a name nsctl creates.
func (m *Manifest) Validate(lookup Lookup) []string {
	var problems []string

	if m.Name == "" {
		problems = append(problems, "'name' is required")
	}
	if m.Version != Version {
		problems = append(problems,
			fmt.Sprintf("'version' must be %d, got %d", Version, m.Version))
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
			if Reserved(r.InstanceName) {
				problems = append(problems, fmt.Sprintf(
					"%s: %q is reserved for the environment substrate and cannot be declared",
					where, r.InstanceName))
			}
		}
		if r.RepoClassName == "" {
			problems = append(problems, where+": 'repo_class_name' is required")
		}
		problems = append(problems, r.validateSource(where, lookup)...)
		problems = append(problems, r.validateDependencies(where)...)
	}
	return problems
}

func (r Repo) validateSource(where string, lookup Lookup) []string {
	switch r.SourceType() {
	case SourceLocal:
	case SourceArtifact:
		return []string{fmt.Sprintf(
			"%s: source type %q is not supported yet; use 'type: %s' to deploy from a local working tree",
			where, SourceArtifact, SourceLocal)}
	default:
		return []string{fmt.Sprintf("%s: unknown source type %q; expected one of %s",
			where, r.SourceType(), strings.Join(knownSources, ", "))}
	}
	if r.RepoClassName == "" {
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
