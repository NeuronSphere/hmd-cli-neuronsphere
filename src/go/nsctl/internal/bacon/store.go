package bacon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/pelletier/go-toml/v2"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/atomicfile"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// Format is the file format a store was read in.
type Format string

const (
	FormatJSON Format = "json"
	FormatTOML Format = "toml"
)

// Location is where the manifest sits relative to the repo root, as the
// three-tier precedence names it.
type Location string

const (
	LocationMetaData Location = "meta-data"
	LocationRoot     Location = "root"
)

// tier is one entry in the precedence list.
type tier struct {
	rel      string
	format   Format
	location Location
}

// tiers is SPEC006's precedence: hmd_lib_manifest's own order first, then
// the repo-root neuronsphere.toml the platform is heading toward. The root
// tier is last because hmd_lib_manifest does not look there yet.
var tiers = []tier{
	{filepath.Join("meta-data", "manifest.toml"), FormatTOML, LocationMetaData},
	{filepath.Join("meta-data", "manifest.json"), FormatJSON, LocationMetaData},
	{"neuronsphere.toml", FormatTOML, LocationRoot},
}

// ErrNotFound is returned by Open when no tier exists.
var ErrNotFound = errors.New("no repo class manifest")

// VersionFile is the repo class version, MAJOR.MINOR, beside the manifest.
const VersionFile = "meta-data/VERSION"

// Store is one repo class manifest: where it was read from, in what format,
// and the document. A write returns to the same file in the same format.
type Store struct {
	Dir      string
	Path     string
	Rel      string
	Format   Format
	Location Location
	Doc      *Object
}

// Find reports which tier a directory has, if any.
func Find(dir string) (path string, t tier, ok bool) {
	for _, t := range tiers {
		p := filepath.Join(dir, t.rel)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p, t, true
		}
	}
	return "", tier{}, false
}

// Open reads the manifest at the first tier present under dir.
func Open(dir string) (*Store, error) {
	path, t, ok := Find(dir)
	if !ok {
		return nil, fmt.Errorf("%w under %s: looked for meta-data/manifest.toml, meta-data/manifest.json and neuronsphere.toml", ErrNotFound, dir)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc *Object
	switch t.format {
	case FormatJSON:
		doc, err = Decode(data)
	case FormatTOML:
		doc, err = decodeTOML(data)
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return &Store{Dir: dir, Path: path, Rel: t.rel, Format: t.format, Location: t.location, Doc: doc}, nil
}

// Save writes the document back to the file it came from.
//
// JSON only. A TOML write would have to be a surgical key-path edit that
// leaves untouched text byte-identical (SPEC006), and nothing linked into
// this binary can do that; refusing with the file named is better than a
// rewrite that drops every comment the user wrote.
func (s *Store) Save() error {
	if s.Format != FormatJSON {
		return nserr.New(nserr.Fail,
			"writing %s: TOML writes are not implemented yet (NERD009 SPEC006); edit the file by hand", s.Path)
	}
	data, err := Encode(s.Doc)
	if err != nil {
		return err
	}
	if err := atomicfile.Write(s.Path, data, 0o644, 0o755); err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	return nil
}

// Init writes the minimum viable repo class manifest (SPEC007): name,
// description and an empty build section -- the three members the schema
// requires -- at meta-data/manifest.json, and meta-data/VERSION as 0.1 when
// there is none. It refuses when any tier already exists, naming the verbs
// the caller probably wanted.
func Init(dir, name, description string) (*Store, error) {
	if path, _, ok := Find(dir); ok {
		return nil, nserr.New(nserr.Usage,
			"%s already exists; use `nsctl repoclass describe` to read it or `nsctl repoclass validate` to check it", path)
	}
	doc := NewObject()
	doc.Set("name", name)
	doc.Set("description", description)
	doc.Set("build", NewObject())
	t := tiers[1]
	s := &Store{Dir: dir, Path: filepath.Join(dir, t.rel), Rel: t.rel, Format: t.format, Location: t.location, Doc: doc}
	if err := s.Save(); err != nil {
		return nil, err
	}
	version := filepath.Join(dir, filepath.FromSlash(VersionFile))
	if _, err := os.Stat(version); errors.Is(err, os.ErrNotExist) {
		if err := atomicfile.Write(version, []byte("0.1\n"), 0o644, 0o755); err != nil {
			return nil, nserr.Wrap(nserr.Fail, err)
		}
	}
	return s, nil
}

// decodeTOML reads a TOML tier into an Object. Order is lost -- go-toml/v2
// decodes into maps -- so keys are sorted; that is fine for a tier that is
// only ever read.
func decodeTOML(data []byte) (*Object, error) {
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	return fromMap(raw), nil
}

func fromMap(m map[string]any) *Object {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	o := NewObject()
	for _, k := range keys {
		o.Set(k, fromValue(m[k]))
	}
	return o
}

func fromValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return fromMap(t)
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = fromValue(e)
		}
		return out
	case []map[string]any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = fromMap(e)
		}
		return out
	default:
		return t
	}
}
