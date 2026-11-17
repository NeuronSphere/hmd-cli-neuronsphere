// Package data is `nsctl data` (NERD039): a person's own files, made
// queryable with DuckDB, with what nsctl learns kept in a workspace under
// $HMD_HOME/data/<workspace>/.
//
// A workspace is nsctl's state, not the person's: it is not a repository and
// not a repo class. Sources are only ever read. Everything that decides
// something is computed here and returned as data, so the CLI's --json output,
// the bundled agent skills and the tests all see the same answer.
package data

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/atomicfile"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// Layout names, relative to a workspace root (NERD039 SPEC001).
const (
	ConfigFile  = "data.yaml"
	TablesDir   = "tables"
	ProfileDir  = "profile"
	ModelDir    = "model"
	QueriesDir  = "queries"
	SnapshotDir = "snapshot"
	CatalogFile = "catalog.duckdb"

	// currentFile, under $HMD_HOME/data, names the current workspace.
	currentFile = ".current"
)

// configVersion is data.yaml's schema version. An unknown version is refused,
// not partially read.
const configVersion = 1

// Progress steps a table moves through, in order (SPEC001).
const (
	StepGrouped    = "grouped"
	StepProfiled   = "profiled"
	StepPinned     = "pinned"
	StepRegistered = "registered"
)

// Source is one root a person pointed nsctl data at. Table paths are relative
// to a source, so the absolute location is recorded once.
type Source struct {
	Name string `yaml:"name" json:"name"`
	Path string `yaml:"path" json:"path"`
}

// Snapshot records what a table's snapshot was taken from, so status can say
// when the sources have moved on (SPEC005).
type Snapshot struct {
	Path   string            `yaml:"path" json:"path"`
	Hashes map[string]string `yaml:"hashes" json:"hashes"`
}

// Config is data.yaml.
type Config struct {
	Version   int                 `yaml:"version"`
	Sources   []Source            `yaml:"sources"`
	Progress  map[string][]string `yaml:"progress,omitempty"`
	Snapshots map[string]Snapshot `yaml:"snapshots,omitempty"`
}

// Workspace is one workspace on disk.
type Workspace struct {
	Name   string
	Root   string
	Config Config
}

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// ValidName reports whether name can be a workspace name: it becomes a
// directory, so it is kept to a portable, unsurprising set.
func ValidName(name string) error {
	if !nameRE.MatchString(name) {
		return nserr.New(nserr.Usage,
			"workspace name %q is not valid: use lowercase letters, digits, '-' and '_', starting with a letter or digit", name)
	}
	return nil
}

// Dir is $HMD_HOME/data.
func Dir(home string) string { return filepath.Join(home, "data") }

// Init creates a workspace with the given sources and makes it current.
// Initialising an existing workspace adds the sources it does not have yet.
func Init(home, name string, sourcePaths []string) (*Workspace, error) {
	if err := ValidName(name); err != nil {
		return nil, err
	}
	root := filepath.Join(Dir(home), name)
	ws := &Workspace{Name: name, Root: root, Config: Config{Version: configVersion}}
	if _, err := os.Stat(filepath.Join(root, ConfigFile)); err == nil {
		existing, err := Open(home, name)
		if err != nil {
			return nil, err
		}
		ws = existing
	}
	for _, d := range []string{TablesDir, ProfileDir, ModelDir, QueriesDir, SnapshotDir} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			return nil, nserr.Wrap(nserr.Fail, err)
		}
	}
	for _, p := range sourcePaths {
		if _, err := ws.AddSource(p); err != nil {
			return nil, err
		}
	}
	if err := ws.Save(); err != nil {
		return nil, err
	}
	if err := Use(home, name); err != nil {
		return nil, err
	}
	return ws, nil
}

// AddSource records a source root, returning its name. A path already
// recorded is not added twice.
func (w *Workspace) AddSource(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", nserr.Wrap(nserr.Usage, err)
	}
	abs = filepath.Clean(abs)
	info, err := os.Stat(abs)
	if err != nil {
		return "", nserr.New(nserr.Usage, "source %s: %v", path, err)
	}
	if !info.IsDir() {
		return "", nserr.New(nserr.Usage, "source %s is a file; pass the directory that holds it", path)
	}
	for _, s := range w.Config.Sources {
		if s.Path == abs {
			return s.Name, nil
		}
	}
	name := uniqueName(snake(filepath.Base(abs)), func(n string) bool {
		for _, s := range w.Config.Sources {
			if s.Name == n {
				return true
			}
		}
		return false
	})
	w.Config.Sources = append(w.Config.Sources, Source{Name: name, Path: abs})
	return name, nil
}

// SourcePath is a source's absolute path.
func (w *Workspace) SourcePath(name string) (string, bool) {
	for _, s := range w.Config.Sources {
		if s.Name == name {
			return s.Path, true
		}
	}
	return "", false
}

// Open reads a workspace.
func Open(home, name string) (*Workspace, error) {
	if err := ValidName(name); err != nil {
		return nil, err
	}
	root := filepath.Join(Dir(home), name)
	b, err := os.ReadFile(filepath.Join(root, ConfigFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nserr.New(nserr.Usage,
			"no workspace %q. Create it with `nsctl data init %s <folder>`, or list them with `nsctl data list`", name, name)
	}
	if err != nil {
		return nil, nserr.Wrap(nserr.Fail, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return nil, nserr.New(nserr.Fail, "%s: %v", filepath.Join(root, ConfigFile), err)
	}
	if cfg.Version != configVersion {
		return nil, nserr.New(nserr.Fail,
			"%s has version %d; this nsctl reads version %d", filepath.Join(root, ConfigFile), cfg.Version, configVersion)
	}
	return &Workspace{Name: name, Root: root, Config: cfg}, nil
}

// Current opens the workspace named, or the current one when name is empty.
func Current(home, name string) (*Workspace, error) {
	if name != "" {
		return Open(home, name)
	}
	b, err := os.ReadFile(filepath.Join(Dir(home), currentFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nserr.New(nserr.Usage,
			"no current workspace. Start one with `nsctl data init <name> <folder>`")
	}
	if err != nil {
		return nil, nserr.Wrap(nserr.Fail, err)
	}
	return Open(home, strings.TrimSpace(string(b)))
}

// Use makes a workspace current.
func Use(home, name string) error {
	if _, err := os.Stat(filepath.Join(Dir(home), name, ConfigFile)); err != nil {
		return nserr.New(nserr.Usage, "no workspace %q", name)
	}
	return writeFile(filepath.Join(Dir(home), currentFile), []byte(name+"\n"))
}

// List names every workspace, and which is current.
func List(home string) (names []string, current string, err error) {
	entries, err := os.ReadDir(Dir(home))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", nserr.Wrap(nserr.Fail, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			if _, err := os.Stat(filepath.Join(Dir(home), e.Name(), ConfigFile)); err == nil {
				names = append(names, e.Name())
			}
		}
	}
	sort.Strings(names)
	if b, err := os.ReadFile(filepath.Join(Dir(home), currentFile)); err == nil {
		current = strings.TrimSpace(string(b))
	}
	return names, current, nil
}

// Delete removes a workspace. Sources are untouched: they were never written.
func Delete(home, name string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	root := filepath.Join(Dir(home), name)
	if _, err := os.Stat(filepath.Join(root, ConfigFile)); err != nil {
		return nserr.New(nserr.Usage, "no workspace %q", name)
	}
	if err := os.RemoveAll(root); err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	if b, err := os.ReadFile(filepath.Join(Dir(home), currentFile)); err == nil && strings.TrimSpace(string(b)) == name {
		_ = os.Remove(filepath.Join(Dir(home), currentFile))
	}
	return nil
}

// Save writes data.yaml.
func (w *Workspace) Save() error {
	w.Config.Version = configVersion
	b, err := yaml.Marshal(&w.Config)
	if err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	return writeFile(filepath.Join(w.Root, ConfigFile), b)
}

// Mark records that a table reached a step.
func (w *Workspace) Mark(table, step string) {
	if w.Config.Progress == nil {
		w.Config.Progress = map[string][]string{}
	}
	for _, s := range w.Config.Progress[table] {
		if s == step {
			return
		}
	}
	w.Config.Progress[table] = append(w.Config.Progress[table], step)
}

// Has reports whether a table reached a step.
func (w *Workspace) Has(table, step string) bool {
	for _, s := range w.Config.Progress[table] {
		if s == step {
			return true
		}
	}
	return false
}

// Path joins elements onto the workspace root.
func (w *Workspace) Path(elem ...string) string {
	return filepath.Join(append([]string{w.Root}, elem...)...)
}

// Mounts are the host paths the engine needs: every source read-only, the
// workspace read-write.
func (w *Workspace) Mounts() []Mount {
	m := []Mount{{Path: w.Root}}
	for _, s := range w.Config.Sources {
		m = append(m, Mount{Path: s.Path, ReadOnly: true})
	}
	return m
}

var tableNameRE = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// ValidTableName reports whether name can name a table or a saved query: it
// becomes a view name and a file name.
func ValidTableName(name string) error {
	if !tableNameRE.MatchString(name) {
		return nserr.New(nserr.Usage,
			"%q is not a valid name: use lowercase letters, digits and '_', starting with a letter or '_'", name)
	}
	return nil
}

func usageNoSources(w *Workspace) error {
	return nserr.New(nserr.Usage,
		"workspace %s has no sources. Pass a folder: `nsctl data scan <folder>`", w.Name)
}

func usageTableExists(name string) error {
	return nserr.New(nserr.Usage,
		"a table named %s already exists; rename the proposal with --name %s=<new name>", name, name)
}

func writeFile(path string, b []byte) error {
	if err := atomicfile.Write(path, b, 0o644, 0o755); err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	return nil
}

var nonWord = regexp.MustCompile(`[^a-z0-9]+`)

// snake turns a file or directory name into a table or source name.
func snake(s string) string {
	s = strings.ToLower(s)
	s = nonWord.ReplaceAllString(s, "_")
	s = strings.Trim(s, "_")
	if s == "" {
		s = "data"
	}
	if s[0] >= '0' && s[0] <= '9' {
		s = "t_" + s
	}
	return s
}

// uniqueName appends _2, _3, ... until taken reports false.
func uniqueName(base string, taken func(string) bool) string {
	if !taken(base) {
		return base
	}
	for i := 2; ; i++ {
		n := fmt.Sprintf("%s_%d", base, i)
		if !taken(n) {
			return n
		}
	}
}
