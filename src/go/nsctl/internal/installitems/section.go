// Package installitems carries out a BACON manifest's install section: what
// installing a published artifact puts on a workstation. NERD031.
//
// The section is declarative and closed. Every item is one of a fixed set of
// kinds, each of which nsctl carries out itself; there is no exec, so
// installing never runs code the artifact carries (SPEC004). What an item
// needs from the host is named in requires, with the author's own hint for
// getting it, and is looked up on PATH and nothing more.
package installitems

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bacon"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nsconfig"
)

// ErrNoSection is a manifest that declares nothing to install.
var ErrNoSection = errors.New("declares nothing to install (no install section in its manifest)")

// Section is the manifest's install object.
type Section struct {
	Requires []Requirement `json:"requires,omitempty"`
	Items    []Item        `json:"items"`
}

// Requirement is a host binary an item needs.
type Requirement struct {
	Binary      string `json:"binary"`
	InstallHint string `json:"install_hint"`
}

// Item is one thing to put on the workstation.
type Item struct {
	Kind      string   `json:"kind"`
	Noun      string   `json:"noun,omitempty"`
	Summary   string   `json:"summary,omitempty"`
	Title     string   `json:"title,omitempty"`
	Dir       string   `json:"dir,omitempty"`
	Format    string   `json:"format,omitempty"`
	IndexHint string   `json:"index_hint,omitempty"`
	Runtime   *Runtime `json:"runtime,omitempty"`
	Source    *Source  `json:"source,omitempty"`
}

// Runtime is how a command item runs.
type Runtime struct {
	Kind        string            `json:"kind"`
	Python      string            `json:"python,omitempty"`
	Lock        string            `json:"lock,omitempty"`
	EntryPoint  string            `json:"entry_point,omitempty"`
	Scripts     map[string]string `json:"scripts,omitempty"`
	Interpreter string            `json:"interpreter,omitempty"`
	Binaries    map[string]string `json:"binaries,omitempty"`
}

// Source is where an item's content comes from. The zero value, and
// {"artifact": true}, is the artifact itself.
type Source struct {
	Artifact bool   `json:"artifact,omitempty"`
	Git      string `json:"git,omitempty"`
	Ref      string `json:"ref,omitempty"`
	Checkout string `json:"checkout,omitempty"`
}

// FromGit reports whether the item is placed by cloning.
func (it Item) FromGit() bool { return it.Source != nil && it.Source.Git != "" }

// Key names the item's placed directory: the noun for a command, else the
// kind and its position, which is stable for one version of one artifact.
func (it Item) Key(i int) string {
	if it.Kind == nsconfig.KindCommand {
		return it.Noun
	}
	return fmt.Sprintf("%s-%d", it.Kind, i+1)
}

// Parse reads the install section out of a manifest's JSON. Keys inside the
// section are decoded strictly: an artifact written for a newer nsctl must
// fail here, before anything is placed, rather than half-install.
func Parse(manifest []byte) (*Section, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(manifest, &doc); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	raw, ok := doc["install"]
	if !ok || string(raw) == "null" {
		return nil, ErrNoSection
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var sec Section
	if err := dec.Decode(&sec); err != nil {
		return nil, fmt.Errorf("install section: %w", err)
	}
	if len(sec.Items) == 0 {
		return nil, fmt.Errorf("install section: items is empty")
	}
	return &sec, nil
}

// ParseDir reads the install section of the manifest under root, in
// whichever format BACON found it.
func ParseDir(root string) (*Section, error) {
	store, err := bacon.Open(root)
	if err != nil {
		return nil, err
	}
	data, err := bacon.Encode(store.Doc)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// entryPointRe is module:function.
var entryPointRe = regexp.MustCompile(`^[A-Za-z_][\w.]*:[A-Za-z_]\w*$`)

// hashRe finds a --hash option in a requirements file.
var hashRe = regexp.MustCompile(`--hash=sha256:[0-9a-fA-F]+`)

// Validate checks the section against the artifact tree at root. With root
// "" it checks only what needs no files, which is what authoring-time
// validation of a manifest that is not yet built can do.
func (s *Section) Validate(root string) error {
	var errs []string
	add := func(i int, format string, args ...any) {
		errs = append(errs, fmt.Sprintf("item %d: ", i+1)+fmt.Sprintf(format, args...))
	}
	required := map[string]bool{}
	for _, r := range s.Requires {
		if r.Binary == "" || r.InstallHint == "" {
			errs = append(errs, "requires: every entry needs binary and install_hint")
		}
		required[r.Binary] = true
	}
	nouns := map[string]bool{}
	for i, it := range s.Items {
		if it.Source != nil && it.Source.Artifact && it.Source.Git != "" {
			add(i, "source is either the artifact or git, not both")
		}
		switch it.Kind {
		case nsconfig.KindCommand:
			if err := nsconfig.ValidPluginName(it.Noun); err != nil {
				add(i, "noun: %v", err)
			} else if nouns[it.Noun] {
				add(i, "noun %q is declared twice", it.Noun)
			}
			nouns[it.Noun] = true
			if it.Summary == "" {
				add(i, "a command needs a summary")
			}
			if it.Runtime == nil {
				add(i, "a command needs a runtime")
				continue
			}
			s.validateRuntime(root, i, it, required, add)
		case nsconfig.KindSkills:
			if it.Dir == "" && !it.FromGit() {
				add(i, "agent-skills needs a dir")
			}
			checkPath(root, i, it, it.Dir, true, add)
		case nsconfig.KindDocs:
			if it.Title == "" {
				add(i, "docs needs a title")
			}
			switch it.Format {
			case "", "rst", "markdown", "html", "text":
			default:
				add(i, "format %q is not one of rst, markdown, html, text", it.Format)
			}
			if it.Dir == "" && !it.FromGit() {
				add(i, "docs needs a dir")
			}
			checkPath(root, i, it, it.Dir, true, add)
		default:
			add(i, "unknown kind %q (want %s, %s or %s)", it.Kind,
				nsconfig.KindCommand, nsconfig.KindSkills, nsconfig.KindDocs)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("install section is invalid:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}

func (s *Section) validateRuntime(root string, i int, it Item, required map[string]bool, add func(int, string, ...any)) {
	rt := it.Runtime
	switch rt.Kind {
	case nsconfig.RuntimePython:
		if it.FromGit() {
			add(i, "a python command cannot come from git: its lock must be a published pin")
			return
		}
		if rt.Python == "" {
			add(i, "python needs a python version")
		}
		if !entryPointRe.MatchString(rt.EntryPoint) {
			add(i, "entry_point %q must be module:function", rt.EntryPoint)
		}
		if rt.Lock == "" {
			add(i, "python needs a lock")
			return
		}
		if !checkPath(root, i, it, rt.Lock, false, add) || root == "" {
			return
		}
		data, err := os.ReadFile(filepath.Join(root, rt.Lock))
		if err == nil && !lockIsHashed(string(data)) {
			add(i, "lock %s pins names, not content: every requirement needs a --hash (uv pip compile --generate-hashes)", rt.Lock)
		}
	case nsconfig.RuntimeScripts:
		if len(rt.Scripts) == 0 {
			add(i, "scripts needs at least one script")
		}
		if rt.Interpreter != "" && !required[rt.Interpreter] {
			add(i, "interpreter %q must also be listed in requires, with a hint", rt.Interpreter)
		}
		for _, name := range sortedKeys(rt.Scripts) {
			if !nsconfig.ValidScriptName(name) {
				add(i, "script name %q must match ^[a-z][a-z0-9-]*$", name)
			}
			checkPath(root, i, it, rt.Scripts[name], false, add)
		}
	case nsconfig.RuntimeBinary:
		if it.FromGit() {
			add(i, "a binary command cannot come from git")
			return
		}
		if len(rt.Binaries) == 0 {
			add(i, "binary needs binaries")
		}
		for _, key := range sortedKeys(rt.Binaries) {
			checkPath(root, i, it, rt.Binaries[key], false, add)
		}
	default:
		add(i, "unknown runtime %q (want python, scripts or binary)", rt.Kind)
	}
}

// lockIsHashed reports whether every requirement line carries a hash.
func lockIsHashed(lock string) bool {
	// Join continuation lines, then check each requirement.
	joined := strings.ReplaceAll(lock, "\\\n", " ")
	seen := false
	for _, line := range strings.Split(joined, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "-") {
			continue
		}
		seen = true
		if !hashRe.MatchString(line) {
			return false
		}
	}
	return seen
}

// checkPath refuses a path that leaves the artifact root, directly or through
// a symlink, and one that is absent. A git item's paths are inside a clone
// that does not exist yet, so only their shape is checked.
func checkPath(root string, i int, it Item, rel string, dir bool, add func(int, string, ...any)) bool {
	if rel == "" {
		return true
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		add(i, "path %q is outside the artifact", rel)
		return false
	}
	if root == "" || it.FromGit() {
		return true
	}
	full := filepath.Join(root, clean)
	info, err := os.Lstat(full)
	if err != nil {
		add(i, "path %s: not in the artifact", rel)
		return false
	}
	// Every component must be a real file or directory: a symlink anywhere
	// on the way is how a zip reaches outside itself.
	for p := full; p != root && strings.HasPrefix(p, root); p = filepath.Dir(p) {
		li, err := os.Lstat(p)
		if err != nil || li.Mode()&os.ModeSymlink != 0 {
			add(i, "path %s: a symlink is outside the artifact", rel)
			return false
		}
	}
	if dir && !info.IsDir() {
		add(i, "path %s is not a directory", rel)
		return false
	}
	if !dir && !info.Mode().IsRegular() {
		add(i, "path %s is not a file", rel)
		return false
	}
	return true
}

// Required is every host binary the section needs: requires, plus git for a
// git-sourced item when the author did not list it.
func (s *Section) Required() []Requirement {
	out := append([]Requirement(nil), s.Requires...)
	have := map[string]bool{}
	for _, r := range out {
		have[r.Binary] = true
	}
	for _, it := range s.Items {
		if it.FromGit() && !have["git"] {
			out = append(out, Requirement{Binary: "git",
				InstallHint: "install git: https://git-scm.com/downloads (macOS: xcode-select --install)"})
			have["git"] = true
		}
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
