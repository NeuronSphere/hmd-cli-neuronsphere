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
}

var skills = []Skill{
	{"nsctl-auth-and-profiles", "Guide safe login, profile selection, credential checks, and logout."},
	{"nsctl-debug", "Diagnose a local NeuronSphere problem from status, plans, and logs without destructive repair."},
	{"nsctl-deploy-plan", "Inspect local deployment inputs and effects before starting work."},
	{"nsctl-local-environment", "Create, run, inspect, and stop a local NeuronSphere environment."},
	{"nsctl-local-plugin", "Adapt a service for local NeuronSphere development and Floci-safe deployment."},
	{"nsctl-onboard", "Orient a newcomer to the local NeuronSphere and identify the next safe command."},
	{"nsctl-repoclass-author", "Author and validate a RepoClass through nsctl's supported verbs."},
}

func List() []Skill { return append([]Skill(nil), skills...) }

func Known(name string) bool {
	for _, skill := range skills {
		if skill.Name == name {
			return true
		}
	}
	return false
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

// Destinations resolves named skills without touching the filesystem. Home is
// required only for user scope; project must already be an existing directory.
func Destinations(names []string, host Host, scope Scope, project, home string) ([]Destination, error) {
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

	var dests []Destination
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		if !Known(name) {
			return nil, fmt.Errorf("unknown bundled skill %q", name)
		}
		var description string
		for _, skill := range skills {
			if skill.Name == name {
				description = skill.Description
				break
			}
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
			state, err := StateAt(name, path)
			if err != nil {
				return nil, err
			}
			dests = append(dests, Destination{Skill: name, Description: description, Host: h, Path: path, State: state})
		}
	}
	return dests, nil
}

func StateAt(name, path string) (State, error) {
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
	data, err := os.ReadFile(filepath.Join(path, markerName))
	if err != nil {
		return Modified, nil
	}
	var m marker
	if json.Unmarshal(data, &m) != nil || m.Skill != name {
		return Modified, nil
	}
	hash, err := sourceHash(name)
	if err != nil || m.Hash != hash {
		return Modified, nil
	}
	installed, err := directoryHash(path)
	if err != nil || installed != hash {
		return Modified, nil
	}
	return Installed, nil
}

// Install creates a complete staged directory, so a failed write does not
// leave a target skill half present. Callers must resolve all destinations
// before invoking it.
func Install(dest Destination, force, dryRun bool) error {
	if dest.State != Missing && !force {
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
	if err := copyBundled(dest.Skill, stage); err != nil {
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

func copyBundled(name, dest string) error {
	data, err := fs.ReadFile(bundled, filepath.Join("skills", name, "SKILL.md"))
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dest, "SKILL.md"), data, 0o644); err != nil {
		return err
	}
	hash, err := sourceHash(name)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(marker{Skill: name, Hash: hash})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dest, markerName), encoded, 0o644)
}

func sourceHash(name string) (string, error) {
	data, err := fs.ReadFile(bundled, filepath.Join("skills", name, "SKILL.md"))
	if err != nil {
		return "", err
	}
	return hashFiles(map[string][]byte{"SKILL.md": data}), nil
}

func directoryHash(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		return "", err
	}
	return hashFiles(map[string][]byte{"SKILL.md": data}), nil
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
