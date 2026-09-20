package repoclass

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// ResourcesDir is where a repo declares the Resources it produces and
// consumes, one YAML document per definition (NERD0004).
const ResourcesDir = "resources"

// ResourceRef names a resource definition.
type ResourceRef struct {
	Namespace string `yaml:"resource_namespace"`
	Name      string `yaml:"resource_definition_name"`
	Version   string `yaml:"version"`
}

// ResourceDeclaration is one meta-data/resources/*.yaml document.
type ResourceDeclaration struct {
	Namespace string       `yaml:"resource_namespace"`
	Name      string       `yaml:"resource_definition_name"`
	Version   string       `yaml:"version"`
	Role      string       `yaml:"role"`
	Parent    *ResourceRef `yaml:"parent"`
	// Produces records that deploying this RepoClass emits a Resource of this
	// type. A declaration without it describes a type the repo defines but
	// does not itself create.
	Produces bool `yaml:"produces"`
	// The remaining fields describe the type to ms-deployment when the
	// definition is upserted. They are carried as `any` because nsctl never
	// reads into them -- it forwards the repo's own document, and typing them
	// would be a second schema to keep in step with the service's.
	Description      string `yaml:"description"`
	ResourceMetadata any    `yaml:"resource_metadata"`
	OutputSchema     any    `yaml:"output_schema"`
	// Path is the file this came from, for an error that names it.
	Path string `yaml:"-"`
}

// Produces reads what a repo class declares it produces.
//
// This replaces asking nsctl to carry its own table of who produces what. A
// repo already states it in the file the cloud deploy path reads, and a second
// list in another language is the one that goes stale -- as it had: nsctl's
// hardcoded entry for hmd-vpc named a different namespace than the repo's own
// declaration did.
//
// A repo with no resources directory produces nothing by declaration, which is
// not an error: most repos declare none, and the caller falls back to whatever
// it knew before.
func (r *Resolver) Produces(repoClass string) ([]ResourceDeclaration, error) {
	dir := r.Dir(repoClass)
	if dir == "" {
		return nil, nil
	}
	root := filepath.Join(dir, "meta-data", ResourcesDir)
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", root, err)
	}

	var declarations []ResourceDeclaration
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch filepath.Ext(e.Name()) {
		case ".yaml", ".yml":
			names = append(names, e.Name())
		}
	}
	// Deterministic, so the order declarations are registered in does not
	// depend on the filesystem.
	sort.Strings(names)

	for _, name := range names {
		path := filepath.Join(root, name)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		var declaration ResourceDeclaration
		if err := yaml.Unmarshal(data, &declaration); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", path, err)
		}
		if declaration.Namespace == "" || declaration.Name == "" {
			// Not every file under here has to be a definition, and a
			// malformed one should not stop a deploy that never needed it.
			continue
		}
		declaration.Path = path
		declarations = append(declarations, declaration)
	}
	return declarations, nil
}

// Produced narrows a set of declarations to the ones the repo actually emits.
func Produced(declarations []ResourceDeclaration) []ResourceDeclaration {
	var out []ResourceDeclaration
	for _, d := range declarations {
		if d.Produces {
			out = append(out, d)
		}
	}
	return out
}
