package bacon

import (
	"fmt"
	"strings"
)

// Edits to the `local` section (NERD010 SPEC001), the way NERD019 SPEC008's
// `nsctl repoclass local` verbs make them. The section is created on first
// use with version 1.

// Companion is what local add writes under local.repos[].
type Companion struct {
	InstanceName  string
	RepoClassName string
	VersionSpec   string
	Profiles      []string
	// Dependencies maps a role to an instance name (or role key) the
	// companion depends on.
	Dependencies map[string]string
}

func ensureLocal(doc *Object) (*Object, error) {
	local, err := doc.EnsureObject("local")
	if err != nil {
		return nil, err
	}
	if _, ok := local.Get("version"); !ok {
		local.Set("version", int64(1))
	}
	if _, ok := local.Get("default_profiles"); !ok {
		local.Set("default_profiles", []any{})
	}
	if _, ok := local.Get("repos"); !ok {
		local.Set("repos", []any{})
	}
	return local, nil
}

// AddCompanion adds or replaces the local.repos entry with the instance
// name. A role this RepoClass needs is not a companion: it is declared with
// deploy add-dependency and gated with local bind or local require.
func AddCompanion(doc *Object, c Companion) (string, error) {
	if c.InstanceName == "" || c.RepoClassName == "" {
		return "", fmt.Errorf("a companion needs an instance name and a repo class")
	}
	local, err := ensureLocal(doc)
	if err != nil {
		return "", err
	}
	entry := NewObject()
	entry.Set("instance_name", c.InstanceName)
	entry.Set("repo_class_name", c.RepoClassName)
	if c.VersionSpec != "" {
		entry.Set("version_spec", c.VersionSpec)
	}
	if len(c.Profiles) > 0 {
		entry.Set("profiles", toAnyList(c.Profiles))
	}
	if len(c.Dependencies) > 0 {
		deps := NewObject()
		for _, k := range sortedKeys(c.Dependencies) {
			deps.Set(k, c.Dependencies[k])
		}
		entry.Set("dependencies", deps)
	}
	repos, _ := local.Array("repos")
	replaced := false
	for i, r := range repos {
		if obj, ok := r.(*Object); ok {
			if name, _ := obj.String("instance_name"); name == c.InstanceName {
				repos[i] = entry
				replaced = true
			}
		}
	}
	if !replaced {
		repos = append(repos, entry)
	}
	local.Set("repos", repos)
	return JoinKey("local", "repos", c.InstanceName), nil
}

// RemoveCompanion drops the local.repos entry with the instance name.
func RemoveCompanion(doc *Object, instanceName string) (string, bool) {
	key := JoinKey("local", "repos", instanceName)
	local, ok := doc.Object("local")
	if !ok {
		return key, false
	}
	repos, _ := local.Array("repos")
	kept := repos[:0]
	removed := false
	for _, r := range repos {
		if obj, ok := r.(*Object); ok {
			if name, _ := obj.String("instance_name"); name == instanceName {
				removed = true
				continue
			}
		}
		kept = append(kept, r)
	}
	local.Set("repos", kept)
	return key, removed
}

// BindRole writes local.dependencies.<role>.bind: the environment provides
// the role under that instance name and nothing is pinned for it.
func BindRole(doc *Object, role, instance string) (string, error) {
	if !hasDependency(doc, role) {
		return "", fmt.Errorf("no dependency %q under deploy.dependencies; add it first with `nsctl repoclass deploy add-dependency %s`", role, role)
	}
	gates, err := ensurePath(doc, "local", "dependencies")
	if err != nil {
		return "", err
	}
	if _, err := ensureLocal(doc); err != nil {
		return "", err
	}
	gate := NewObject()
	gate.Set("bind", instance)
	gates.Set(role, gate)
	return JoinKey("local", "dependencies", role), nil
}

// RequireRole writes local.dependencies.<role> as external: another stack's
// instance must fill it, matched by resource; suggest names that stack.
func RequireRole(doc *Object, role, suggest string) (string, error) {
	if !hasDependency(doc, role) {
		return "", fmt.Errorf("no dependency %q under deploy.dependencies; add it first with `nsctl repoclass deploy add-dependency %s --resource-namespace ... --resource-definition-name ...`", role, role)
	}
	gates, err := ensurePath(doc, "local", "dependencies")
	if err != nil {
		return "", err
	}
	if _, err := ensureLocal(doc); err != nil {
		return "", err
	}
	gate := NewObject()
	gate.Set("external", true)
	if suggest != "" {
		gate.Set("suggest", suggest)
	}
	gates.Set(role, gate)
	return JoinKey("local", "dependencies", role), nil
}

// SetDefaultProfiles replaces local.default_profiles.
func SetDefaultProfiles(doc *Object, profiles []string) (string, error) {
	local, err := ensureLocal(doc)
	if err != nil {
		return "", err
	}
	local.Set("default_profiles", toAnyList(profiles))
	return JoinKey("local", "default_profiles"), nil
}

func hasDependency(doc *Object, role string) bool {
	_, ok := doc.Lookup("deploy", "dependencies", role)
	return ok
}

func toAnyList(s []string) []any {
	out := make([]any, 0, len(s))
	for _, v := range s {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
