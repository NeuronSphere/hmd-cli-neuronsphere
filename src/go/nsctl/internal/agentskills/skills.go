// Package agentskills owns nsctl's bundled, host-neutral Agent Skills and the
// deliberately narrow file operations used to install them.
package agentskills

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

//go:embed skills/*/SKILL.md
var bundled embed.FS

const markerName = ".nsctl-skill.json"

type Host string

const (
	Codex  Host = "codex"
	Claude Host = "claude"
	All    Host = "all"
)

type Scope string

const (
	Project Scope = "project"
	User    Scope = "user"
)

type Skill struct {
	Name        string
	Description string
}

type State string

const (
	Missing   State = "missing"
	Installed State = "installed"
	Modified  State = "modified"
	// Outdated is an artifact skill its plugin installed, unedited, whose
	// source has since changed.
	Outdated State = "outdated"
)

type Destination struct {
	Skill       string
	Description string
	Host        Host
	Path        string
	State       State
}

type marker struct {
	Skill string `json:"skill"`
	Hash  string `json:"sha256"`
	// Owner is the plugin that installed an artifact skill; empty for a
	// bundled one. NERD015 SPEC007.
	Owner string `json:"owner,omitempty"`
}

var skills = []Skill{
	{"nsctl-auth-and-profiles", "Guide safe login, profile selection, credential checks, and logout."},
	{"nsctl-debug", "Diagnose a local NeuronSphere problem from status, plans, and logs without destructive repair."},
	{"nsctl-deploy-plan", "Inspect local deployment inputs and effects before starting work."},
	{"nsctl-local-environment", "Create, run, inspect, and stop a local NeuronSphere environment."},
	{"nsctl-local-plugin", "Adapt a service for local NeuronSphere development and Floci-safe deployment."},
	{"nsctl-onboard", "Orient a newcomer to the local NeuronSphere and identify the next safe command."},
	{"nsctl-repoclass-adopt", "Adopt an existing repository that has no NeuronSphere metadata, from detect's evidence."},
	{"nsctl-repoclass-author", "Author and validate a RepoClass through nsctl's supported verbs."},
	{"nsctl-session-environment", "Give a coding session its own environment from a template and its repositories, and keep its work there."},
}

// Source is where skills are copied from: the set embedded in this binary,
// or a directory an installed artifact declared (NERD015 SPEC007).
type Source struct {
	fsys   fs.FS
	skills []Skill
	// owner is the plugin an artifact source belongs to; "" is bundled.
	owner string
}

var bundledSource = &Source{fsys: mustSub(bundled, "skills"), skills: skills}

func mustSub(f fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(f, dir)
	if err != nil {
		panic(err)
	}
	return sub
}

// Bundled is nsctl's own skill set.
func Bundled() *Source { return bundledSource }

// skillNameRe is a skill directory's name, the Agent Skills name grammar.
var skillNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// FromDir reads the skills an artifact declared: every directory directly
// under dir that holds a SKILL.md. The directory was named by the manifest,
// and each skill is named by its own SKILL.md, so nothing here is guessed.
// A name that a bundled skill already has is refused: that name is nsctl's.
func FromDir(dir, owner string) (*Source, error) {
	if owner == "" {
		return nil, fmt.Errorf("an artifact skill source needs an owner")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	src := &Source{fsys: os.DirFS(dir), owner: owner}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name(), "SKILL.md"))
		if err != nil {
			continue
		}
		if !skillNameRe.MatchString(e.Name()) {
			return nil, fmt.Errorf("skill directory %q must match %s", e.Name(), skillNameRe)
		}
		if Known(e.Name()) {
			return nil, fmt.Errorf("skill %q has the name of a skill bundled with nsctl; bundled names belong to nsctl", e.Name())
		}
		src.skills = append(src.skills, Skill{Name: e.Name(), Description: frontmatter(string(data), "description")})
	}
	sort.Slice(src.skills, func(i, j int) bool { return src.skills[i].Name < src.skills[j].Name })
	return src, nil
}

// frontmatter reads one scalar key from a SKILL.md's YAML frontmatter.
func frontmatter(md, key string) string {
	body, ok := strings.CutPrefix(md, "---\n")
	if !ok {
		return ""
	}
	head, _, _ := strings.Cut(body, "\n---")
	for _, line := range strings.Split(head, "\n") {
		if v, ok := strings.CutPrefix(line, key+":"); ok {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}

// List is the source's skills.
func (s *Source) List() []Skill { return append([]Skill(nil), s.skills...) }

func (s *Source) known(name string) (Skill, bool) {
	for _, skill := range s.skills {
		if skill.Name == name {
			return skill, true
		}
	}
	return Skill{}, false
}

func List() []Skill { return Bundled().List() }

func Known(name string) bool {
	_, ok := bundledSource.known(name)
	return ok
}

func Hosts(host Host) ([]Host, error) {
	switch host {
	case Codex, Claude:
		return []Host{host}, nil
	case All:
		return []Host{Codex, Claude}, nil
	default:
		return nil, fmt.Errorf("unknown agent host %q (want codex, claude, or all)", host)
	}
}

// Destinations resolves named bundled skills without touching the
// filesystem. Home is required only for user scope; project must already be
// an existing directory.
func Destinations(names []string, host Host, scope Scope, project, home string) ([]Destination, error) {
	return Bundled().Destinations(names, host, scope, project, home)
}

// Destinations resolves this source's named skills, or all of them when
// names is empty.
func (s *Source) Destinations(names []string, host Host, scope Scope, project, home string) ([]Destination, error) {
	hosts, err := Hosts(host)
	if err != nil {
		return nil, err
	}
	if scope != Project && scope != User {
		return nil, fmt.Errorf("unknown skill scope %q (want project or user)", scope)
	}
	if scope == Project {
		if project == "" {
			return nil, fmt.Errorf("project path is required for project scope")
		}
		info, err := os.Stat(project)
		if err != nil {
			return nil, fmt.Errorf("project path %s: %w", project, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("project path %s is not a directory", project)
		}
	} else if home == "" {
		return nil, fmt.Errorf("user home is required for user scope")
	}
	if len(names) == 0 {
		for _, skill := range s.skills {
			names = append(names, skill.Name)
		}
	}

	var dests []Destination
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		skill, ok := s.known(name)
		if !ok {
			if s.owner == "" {
				return nil, fmt.Errorf("unknown bundled skill %q", name)
			}
			return nil, fmt.Errorf("unknown skill %q", name)
		}
		for _, h := range hosts {
			base := project
			part := ".agents"
			if scope == User {
				base = home
			}
			if h == Claude {
				part = ".claude"
			}
			path := filepath.Join(base, part, "skills", name)
			state, err := s.StateAt(name, path)
			if err != nil {
				return nil, err
			}
			dests = append(dests, Destination{Skill: name, Description: skill.Description, Host: h, Path: path, State: state})
		}
	}
	return dests, nil
}

// StateAt is a bundled skill's state at path.
func StateAt(name, path string) (State, error) { return Bundled().StateAt(name, path) }

// StateAt compares what is at path with this source's copy of name.
//
// For an artifact source, a copy this plugin installed that nobody has
// edited but whose source has since changed is Outdated, which an install
// may replace without --force: that is what an upgrade is. The bundled set
// keeps its original rule, where any difference is Modified.
func (s *Source) StateAt(name, path string) (State, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return Missing, nil
	}
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return Modified, nil
	}
	m, ok := readMarker(path)
	if !ok || m.Skill != name || m.Owner != s.owner {
		return Modified, nil
	}
	hash, err := s.sourceHash(name)
	if err != nil {
		return Modified, nil
	}
	installed, err := directoryHash(path)
	if err != nil {
		return Modified, nil
	}
	switch {
	case m.Hash == hash && installed == hash:
		return Installed, nil
	case s.owner != "" && installed == m.Hash:
		return Outdated, nil
	}
	return Modified, nil
}

// OwnedState classifies an installed artifact skill with no source to
// compare against, which is what remove has: Installed when owner put it
// there and nobody has edited it since, else Modified.
func OwnedState(path, owner string) State {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return Missing
	}
	if err != nil || !info.IsDir() {
		return Modified
	}
	m, ok := readMarker(path)
	if !ok || m.Owner != owner || owner == "" {
		return Modified
	}
	if installed, err := directoryHash(path); err != nil || installed != m.Hash {
		return Modified
	}
	return Installed
}

func readMarker(path string) (marker, bool) {
	data, err := os.ReadFile(filepath.Join(path, markerName))
	if err != nil {
		return marker{}, false
	}
	var m marker
	if json.Unmarshal(data, &m) != nil {
		return marker{}, false
	}
	return m, true
}

// Install installs a bundled skill.
func Install(dest Destination, force, dryRun bool) error {
	return Bundled().Install(dest, force, dryRun)
}

// Install creates a complete staged directory, so a failed write does not
// leave a target skill half present. Callers must resolve all destinations
// before invoking it.
func (s *Source) Install(dest Destination, force, dryRun bool) error {
	if dest.State != Missing && dest.State != Outdated && !force {
		return fmt.Errorf("%s already exists; use --force to replace it", dest.Path)
	}
	if dryRun {
		return nil
	}
	if info, err := os.Stat(dest.Path); err == nil && !info.IsDir() {
		return fmt.Errorf("%s is not a skill directory", dest.Path)
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	parent := filepath.Dir(dest.Path)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(parent, ".nsctl-skill-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err := s.copySkill(dest.Skill, stage); err != nil {
		return err
	}
	if dest.State == Missing {
		return os.Rename(stage, dest.Path)
	}
	backup, err := os.MkdirTemp(parent, ".nsctl-skill-backup-")
	if err != nil {
		return err
	}
	if err := os.Remove(backup); err != nil {
		return err
	}
	if err := os.Rename(dest.Path, backup); err != nil {
		return err
	}
	if err := os.Rename(stage, dest.Path); err != nil {
		_ = os.Rename(backup, dest.Path)
		return err
	}
	return os.RemoveAll(backup)
}

func Remove(dest Destination, force, dryRun bool) error {
	if dest.State == Missing {
		return fmt.Errorf("%s is not installed", dest.Path)
	}
	if dest.State != Installed && !force {
		return fmt.Errorf("%s was changed locally; use --force to remove it", dest.Path)
	}
	if dryRun {
		return nil
	}
	info, err := os.Stat(dest.Path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a skill directory", dest.Path)
	}
	return os.RemoveAll(dest.Path)
}

// skillFiles is every regular file of one skill, by slash path relative to
// the skill's directory. A symlink is refused: the tree came out of a zip or
// a clone, and a link is how either reaches outside it.
func (s *Source) skillFiles(name string) (map[string][]byte, map[string]fs.FileMode, error) {
	files := map[string][]byte{}
	modes := map[string]fs.FileMode{}
	err := fs.WalkDir(s.fsys, name, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("skill %s: %s is not a regular file", name, p)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		data, err := fs.ReadFile(s.fsys, p)
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(p, name+"/")
		if rel == markerName {
			return nil
		}
		files[rel] = data
		modes[rel] = info.Mode()
		return nil
	})
	return files, modes, err
}

func (s *Source) copySkill(name, dest string) error {
	files, modes, err := s.skillFiles(name)
	if err != nil {
		return err
	}
	for rel, data := range files {
		target := filepath.Join(dest, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		mode := fs.FileMode(0o644)
		if modes[rel]&0o111 != 0 {
			mode = 0o755
		}
		if err := os.WriteFile(target, data, mode); err != nil {
			return err
		}
	}
	encoded, err := json.Marshal(marker{Skill: name, Hash: hashFiles(files), Owner: s.owner})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dest, markerName), encoded, 0o644)
}

func (s *Source) sourceHash(name string) (string, error) {
	files, _, err := s.skillFiles(name)
	if err != nil {
		return "", err
	}
	if _, ok := files["SKILL.md"]; !ok {
		return "", fmt.Errorf("skill %s has no SKILL.md", name)
	}
	return hashFiles(files), nil
}

// directoryHash hashes an installed skill the way sourceHash hashes its
// source: every regular file but the marker.
func directoryHash(dir string) (string, error) {
	files, _, err := (&Source{fsys: os.DirFS(dir)}).skillFiles(".")
	if err != nil {
		return "", err
	}
	if _, ok := files["SKILL.md"]; !ok {
		return "", fmt.Errorf("%s has no SKILL.md", dir)
	}
	return hashFiles(files), nil
}

func hashFiles(files map[string][]byte) string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, name := range names {
		_, _ = h.Write([]byte(name + "\x00"))
		_, _ = h.Write(files[name])
		_, _ = h.Write([]byte("\x00"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func CommandPaths(name string) ([]string, error) {
	data, err := fs.ReadFile(bundled, filepath.Join("skills", name, "SKILL.md"))
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# nsctl-command: ") {
			paths = append(paths, strings.TrimPrefix(line, "# nsctl-command: "))
		}
	}
	return paths, nil
}
