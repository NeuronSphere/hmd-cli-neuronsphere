package stack

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/localspec"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/repoclass"
)

// Providers is what an environment already provides: which declared
// instances produce which resource types, and what class each instance is.
// NERD017 SPEC010's rule 1 is answered from this, offline.
type Providers struct {
	byResource map[string][]string
	classOf    map[string]string
}

// IndexProviders reads every declared instance's class tree through the
// resolver -- cache or working tree, whichever the environment resolves --
// and records what it produces: meta-data/resources/*.yaml documents marked
// `produces`, and the manifest's deploy.resources. An instance whose tree is
// not on this machine produces nothing by declaration and is indexed by
// class only.
func IndexProviders(m *manifest.Manifest, r *repoclass.Resolver) Providers {
	p := Providers{byResource: map[string][]string{}, classOf: map[string]string{}}
	if m == nil {
		return p
	}
	for _, repo := range m.Repos {
		if repo.InstanceName == "" || repo.RepoClassName == "" {
			continue
		}
		p.classOf[repo.InstanceName] = repo.RepoClassName
		for _, key := range producedBy(r, repo.RepoClassName) {
			p.byResource[key] = append(p.byResource[key], repo.InstanceName)
		}
	}
	for key := range p.byResource {
		sort.Strings(p.byResource[key])
	}
	return p
}

// producedBy lists the "<namespace>/<name>" types a class produces.
func producedBy(r *repoclass.Resolver, class string) []string {
	seen := map[string]bool{}
	var keys []string
	add := func(ns, name string) {
		if ns == "" || name == "" {
			return
		}
		k := ns + "/" + name
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	if r == nil {
		return nil
	}
	if decls, err := r.Produces(class); err == nil {
		for _, d := range decls {
			if d.Produces {
				add(d.Namespace, d.Name)
			}
		}
	}
	if dir := r.Dir(class); dir != "" {
		data, err := os.ReadFile(filepath.Join(dir, "meta-data", "manifest.json"))
		if err == nil {
			var doc struct {
				Deploy struct {
					Resources []struct {
						Namespace string `json:"resource_namespace"`
						Name      string `json:"resource_definition_name"`
					} `json:"resources"`
				} `json:"deploy"`
			}
			if json.Unmarshal(data, &doc) == nil {
				for _, res := range doc.Deploy.Resources {
					add(res.Namespace, res.Name)
				}
			}
		}
	}
	sort.Strings(keys)
	return keys
}

// For lists the instances producing a resource type.
func (p Providers) For(resource string) []string { return p.byResource[resource] }

// ClassOf is the class an instance is declared as, or "".
func (p Providers) ClassOf(instance string) string { return p.classOf[instance] }

// Unsatisfied is a role nothing in the environment provides and the stack's
// lock does not pin.
type Unsatisfied struct {
	Role     string
	Resource string
	Class    string
	Suggest  string
}

func (u Unsatisfied) Error() string {
	what := u.Class
	if u.Resource != "" {
		what = u.Resource + " (" + u.Class + ")"
	}
	msg := fmt.Sprintf("role %q needs %s, and nothing in the environment provides it", u.Role, what)
	if u.Suggest != "" {
		msg += fmt.Sprintf("; add a stack that does (`nsctl stack add %s`)", u.Suggest)
	}
	msg += fmt.Sprintf(", or bind an existing instance with --name %s=<instance>", u.Role)
	return msg
}

// Compose is NERD017 SPEC010: decide, for the wants a stack activates, which
// are filled by what the environment already has.
//
// Returns the binds to apply -- want key to existing instance -- and the
// roles nothing can fill. owned is the instances this stack's own record
// bound on a previous add, which are its to re-declare, not to reuse.
// pinned reports whether the lock pins a class. names are --name overrides,
// which always win.
func Compose(wants []localspec.Want, p Providers, owned map[string]bool, pinned func(class string) bool,
	names map[string]string) (binds map[string]string, unsatisfied []Unsatisfied, conflicts []string) {

	binds = map[string]string{}
	foreign := func(instance string) bool { return instance != "" && !owned[instance] && p.ClassOf(instance) != "" }
	for _, w := range wants {
		if w.Bind != "" {
			continue
		}
		if _, overridden := names[w.Key]; overridden {
			continue
		}
		if len(w.Satisfies) > 0 {
			// A dependency role: rule 1, an existing producer of its resource
			// type; rule 2, the lock; rule 3, refuse.
			if w.Resource != "" {
				if providers := p.For(w.Resource); len(providers) > 0 {
					binds[w.Key] = pickProvider(providers, w.DefaultName)
					continue
				}
			}
			if foreign(w.DefaultName) && p.ClassOf(w.DefaultName) == w.RepoClassName {
				// No resource declared, but an instance of the very class the
				// stack would deploy already runs under the name it would use.
				binds[w.Key] = w.DefaultName
				continue
			}
			if !pinned(w.RepoClassName) {
				unsatisfied = append(unsatisfied, Unsatisfied{Role: w.Key, Resource: w.Resource, Class: w.RepoClassName, Suggest: w.Suggest})
			}
			continue
		}
		// A companion: shared when the environment has that instance already.
		if foreign(w.DefaultName) {
			if p.ClassOf(w.DefaultName) == w.RepoClassName {
				binds[w.Key] = w.DefaultName
			} else {
				conflicts = append(conflicts, fmt.Sprintf("%q is already declared as %s, not %s; rename this stack's with --name %s=<instance>",
					w.DefaultName, p.ClassOf(w.DefaultName), w.RepoClassName, w.Key))
			}
		}
	}
	return binds, unsatisfied, conflicts
}

// pickProvider prefers the instance the stack would have named, else the
// first in sorted order, so a re-add resolves the same way.
func pickProvider(providers []string, preferred string) string {
	for _, p := range providers {
		if p == preferred {
			return p
		}
	}
	return providers[0]
}

// DescribeBinds renders binds for the plan output.
func DescribeBinds(binds map[string]string) string {
	keys := make([]string, 0, len(binds))
	for k := range binds {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+" -> "+binds[k])
	}
	return strings.Join(parts, ", ")
}
