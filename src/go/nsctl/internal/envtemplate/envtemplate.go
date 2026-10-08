// Package envtemplate stores environment templates (NERD035 SPEC003): named
// environment manifests a session environment starts from, kept at
// $HMD_HOME/templates/<name>.yaml.
//
// A template is an environment manifest -- same schema, same validation --
// and nothing more. nsctl ships none and lists none it does not find on disk.
package envtemplate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
)

var (
	// ErrNotFound is a template name with no file behind it.
	ErrNotFound = errors.New("no such template")
	// ErrInvalidName is a name ValidName refuses.
	ErrInvalidName = errors.New("invalid template name")
)

// nameRE is an environment name's rule with a longer limit: a template is not
// a slug baked into container names, so it need not fit in sixteen characters.
var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// ValidName refuses a name that is not a plain kebab-case word. It is also
// what keeps a name from reaching outside the templates directory.
func ValidName(name string) error {
	if !nameRE.MatchString(name) {
		return fmt.Errorf("%w %q: use 1-63 characters, lowercase letters, digits and hyphens, starting with a letter or digit", ErrInvalidName, name)
	}
	return nil
}

// Dir is where templates live.
func Dir(home string) string { return filepath.Join(home, "templates") }

// Path is where the template called name is written.
func Path(home, name string) string { return filepath.Join(Dir(home), name+".yaml") }

// find is the existing file for name in any manifest extension, or "".
func find(home, name string) string {
	for _, ext := range manifest.Extensions {
		p := filepath.Join(Dir(home), name+ext)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}

// Exists reports whether a template called name is on disk.
func Exists(home, name string) bool {
	return ValidName(name) == nil && find(home, name) != ""
}

// Load reads and validates the template called name.
func Load(home, name string, lookup manifest.Lookup) (*manifest.Manifest, error) {
	if err := ValidName(name); err != nil {
		return nil, err
	}
	path := find(home, name)
	if path == "" {
		return nil, fmt.Errorf("%w %q in %s", ErrNotFound, name, Dir(home))
	}
	return manifest.LoadFile(path, lookup)
}

// Save writes m as the template called name, naming it name whatever it was
// called before. An existing file in another extension is replaced, so a
// name always has exactly one file.
func Save(home, name string, m *manifest.Manifest) error {
	if err := ValidName(name); err != nil {
		return err
	}
	if old := find(home, name); old != "" && old != Path(home, name) {
		if err := os.Remove(old); err != nil {
			return err
		}
	}
	m.Name = name
	if m.Version == 0 {
		m.Version = manifest.Version
	}
	return m.Save(Path(home, name))
}

// Remove deletes the template called name.
func Remove(home, name string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	path := find(home, name)
	if path == "" {
		return fmt.Errorf("%w %q in %s", ErrNotFound, name, Dir(home))
	}
	return os.Remove(path)
}

// List names every template on disk, sorted.
func List(home string) ([]string, error) {
	entries, err := os.ReadDir(Dir(home))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := filepath.Ext(e.Name())
		if !isManifestExt(ext) {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ext)
		if ValidName(name) != nil || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

func isManifestExt(ext string) bool {
	for _, e := range manifest.Extensions {
		if strings.EqualFold(e, ext) {
			return true
		}
	}
	return false
}

// FromEnvironment is env's manifest as a template: everything it declares
// except the instances explicitly declared `source: {type: local}` -- working
// trees, which belong to whoever was editing them, not to the shape. An
// instance with no source keeps it: it resolves through the tiers at deploy
// time like any other. dropped names what was left out. env is not modified.
func FromEnvironment(env *manifest.Manifest) (tmpl *manifest.Manifest, dropped []string) {
	cp := *env
	cp.Path = ""
	cp.Repos = nil
	for _, r := range env.Repos {
		if r.Source != nil && r.Source.Type == manifest.SourceLocal {
			dropped = append(dropped, r.InstanceName)
			continue
		}
		cp.Repos = append(cp.Repos, r)
	}
	return &cp, dropped
}
