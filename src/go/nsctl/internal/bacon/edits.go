package bacon

import (
	"fmt"
	"sort"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/artifact"
)

// The write verbs (SPEC008), each returning the dotted key it changed so the
// command can print the one line a write is allowed. Every add-* is
// key-addressed: re-adding replaces the entry with the same key in place.

// SetMechanism writes <section>.mechanism.
func SetMechanism(doc *Object, section, mechanism string) (string, error) {
	if !buildMechanisms[mechanism] {
		return "", fmt.Errorf("mechanism must be tool_set or external, got %q", mechanism)
	}
	if err := doc.SetPath(mechanism, section, "mechanism"); err != nil {
		return "", err
	}
	return JoinKey(section, "mechanism"), nil
}

// AddCommand is hmd manifest's section_add_command: the entry for the tool
// is replaced in place, or appended.
func AddCommand(doc *Object, section, tool string, args []string) (string, error) {
	sec, err := doc.EnsureObject(section)
	if err != nil {
		return "", err
	}
	argv := make([]any, 0, 1+len(args))
	argv = append(argv, tool)
	for _, a := range args {
		argv = append(argv, a)
	}
	commands, _ := sec.Array("commands")
	replaced := false
	for i, entry := range commands {
		if first, ok := firstTool(entry); ok && first == tool {
			commands[i] = argv
			replaced = true
		}
	}
	if !replaced {
		commands = append(commands, argv)
	}
	sec.Set("commands", commands)
	return JoinKey(section, "commands"), nil
}

// RemoveCommand drops every entry naming the tool.
func RemoveCommand(doc *Object, section, tool string) (string, bool, error) {
	sec, ok := doc.Object(section)
	if !ok {
		return JoinKey(section, "commands"), false, nil
	}
	commands, _ := sec.Array("commands")
	kept := commands[:0:0]
	removed := false
	for _, entry := range commands {
		if first, ok := firstTool(entry); ok && first == tool {
			removed = true
			continue
		}
		kept = append(kept, entry)
	}
	if kept == nil {
		kept = []any{}
	}
	sec.Set("commands", kept)
	return JoinKey(section, "commands"), removed, nil
}

// SetExecCommand makes <section>.commands exactly one exec entry (SPEC003:
// one exec per phase, never beside a tool). It returns what it replaced so
// the caller can say so.
func SetExecCommand(doc *Object, section string, argv []string) (key string, replaced []any, err error) {
	if len(argv) == 0 {
		return "", nil, fmt.Errorf("exec needs a command to run")
	}
	sec, err := doc.EnsureObject(section)
	if err != nil {
		return "", nil, err
	}
	replaced, _ = sec.Array("commands")
	entry := make([]any, 0, 1+len(argv))
	entry = append(entry, "exec")
	for _, a := range argv {
		entry = append(entry, a)
	}
	sec.Set("commands", []any{entry})
	return JoinKey(section, "commands"), replaced, nil
}

// SetImage writes deploy.image.
func SetImage(doc *Object, ref string) (string, error) {
	if strings.TrimSpace(ref) == "" {
		return "", fmt.Errorf("an image reference is required")
	}
	if err := doc.SetPath(ref, "deploy", "image"); err != nil {
		return "", err
	}
	return "deploy.image", nil
}

// SetLicence writes the top-level `license` declaration (NERD017 SPEC011):
// the string shorthand when nothing is excluded, else the object form with
// spdx and the cleaned exclude list. It replaces whatever was there; a
// declaration is one statement, not a list to add to.
func SetLicence(doc *Object, spdx string, exclude []string) (string, error) {
	spdx = strings.TrimSpace(spdx)
	if spdx == "" {
		return "", fmt.Errorf("an SPDX licence expression is required")
	}
	var cleaned []any
	seen := map[string]bool{}
	for _, e := range exclude {
		c, err := artifact.CleanExclude(e)
		if err != nil {
			return "", err
		}
		if !seen[c] {
			seen[c] = true
			cleaned = append(cleaned, c)
		}
	}
	if len(cleaned) == 0 {
		doc.Set("license", spdx)
		return "license", nil
	}
	obj := NewObject()
	obj.Set("spdx", spdx)
	obj.Set("exclude", cleaned)
	doc.Set("license", obj)
	return "license", nil
}

// ClearLicence removes the declaration; ok reports whether there was one.
func ClearLicence(doc *Object) (key string, ok bool) {
	return "license", doc.Delete("license")
}

// Dependency is what add-dependency writes for a role.
type Dependency struct {
	RepoClassName          string
	Required               bool
	VersionSpec            string
	ResourceNamespace      string
	ResourceDefinitionName string
	ResourceVersion        string
	ResourceVersionSpec    string
	Tags                   map[string]string
}

// AddDependency is hmd manifest deploy-add-dependency: `required` as the
// string the schema's enum demands, the resource block when any of its keys
// are given, the role replaced in place.
func AddDependency(doc *Object, role string, d Dependency) (string, error) {
	deps, err := ensurePath(doc, "deploy", "dependencies")
	if err != nil {
		return "", err
	}
	entry := NewObject()
	if d.RepoClassName != "" {
		entry.Set("repo_class_name", d.RepoClassName)
	}
	entry.Set("required", fmt.Sprintf("%t", d.Required))
	if d.VersionSpec != "" {
		entry.Set("version_spec", d.VersionSpec)
	}
	if d.ResourceNamespace != "" || d.ResourceDefinitionName != "" || d.ResourceVersion != "" {
		res := NewObject()
		if d.ResourceNamespace != "" {
			res.Set("resource_namespace", d.ResourceNamespace)
		}
		if d.ResourceDefinitionName != "" {
			res.Set("resource_definition_name", d.ResourceDefinitionName)
		}
		if d.ResourceVersion != "" {
			res.Set("version", d.ResourceVersion)
		}
		if d.ResourceVersionSpec != "" {
			res.Set("version_spec", d.ResourceVersionSpec)
		}
		if len(d.Tags) > 0 {
			sel := NewObject()
			for _, k := range sortedKeys(d.Tags) {
				sel.Set(k, d.Tags[k])
			}
			res.Set("tag_selector", sel)
		}
		entry.Set("resource", res)
	}
	deps.Set(role, entry)
	return JoinKey("deploy", "dependencies", role), nil
}

// RemoveDependency drops a role.
func RemoveDependency(doc *Object, role string) (string, bool) {
	return JoinKey("deploy", "dependencies", role), doc.DeletePath("deploy", "dependencies", role)
}

// Resource is what add-resource writes under deploy.resources.
type Resource struct {
	Namespace, Name, Version string
	Produces                 *bool
	Role, Description        string
}

// AddResource is hmd manifest deploy-add-resource.
func AddResource(doc *Object, name string, r Resource) (string, error) {
	if r.Namespace == "" || r.Name == "" || r.Version == "" {
		return "", fmt.Errorf("a resource needs --resource-namespace, --resource-definition-name and --version")
	}
	resources, err := ensurePath(doc, "deploy", "resources")
	if err != nil {
		return "", err
	}
	entry := NewObject()
	entry.Set("resource_namespace", r.Namespace)
	entry.Set("resource_definition_name", r.Name)
	entry.Set("version", r.Version)
	if r.Produces != nil {
		entry.Set("produces", *r.Produces)
	}
	if r.Role != "" {
		entry.Set("role", r.Role)
	}
	if r.Description != "" {
		entry.Set("description", r.Description)
	}
	resources.Set(name, entry)
	return JoinKey("deploy", "resources", name), nil
}

// RemoveResource drops a resource.
func RemoveResource(doc *Object, name string) (string, bool) {
	return JoinKey("deploy", "resources", name), doc.DeletePath("deploy", "resources", name)
}

// SetConfig writes a dotted key under deploy.default_configuration. The
// value is whatever the caller typed it as -- parseConfig's JSON rule.
func SetConfig(doc *Object, dotted string, value any) (string, error) {
	path := append([]string{"deploy", "default_configuration"}, SplitKey(dotted)...)
	if err := doc.SetPath(value, path...); err != nil {
		return "", err
	}
	return JoinKey(path...), nil
}

// UnsetConfig removes a dotted key under deploy.default_configuration.
func UnsetConfig(doc *Object, dotted string) (string, bool) {
	path := append([]string{"deploy", "default_configuration"}, SplitKey(dotted)...)
	return JoinKey(path...), doc.DeletePath(path...)
}

// SetSummary writes discovery.summary.
func SetSummary(doc *Object, summary string) (string, error) {
	if err := doc.SetPath(summary, "discovery", "summary"); err != nil {
		return "", err
	}
	return "discovery.summary", nil
}

// AddEntryPoint is keyed on path.
func AddEntryPoint(doc *Object, path, description string) (string, error) {
	entry := NewObject()
	entry.Set("path", path)
	entry.Set("description", description)
	return addDiscoveryItem(doc, "entry_points", "path", path, entry)
}

// AddCapability is keyed on name.
func AddCapability(doc *Object, name, kind, description, location string) (string, error) {
	if !capabilityKinds[kind] {
		return "", fmt.Errorf("kind must be one of endpoint, cli_command, function, class, operation; got %q", kind)
	}
	entry := NewObject()
	entry.Set("name", name)
	entry.Set("kind", kind)
	entry.Set("description", description)
	if location != "" {
		entry.Set("location", location)
	}
	return addDiscoveryItem(doc, "capabilities", "name", name, entry)
}

// AddRelatedDoc is keyed on path.
func AddRelatedDoc(doc *Object, title, path string) (string, error) {
	entry := NewObject()
	entry.Set("title", title)
	entry.Set("path", path)
	return addDiscoveryItem(doc, "related_docs", "path", path, entry)
}

// addDiscoveryItem is the Python's replace-then-append: entries with the same
// key are dropped and the new one goes on the end.
func addDiscoveryItem(doc *Object, list, keyField, keyValue string, entry *Object) (string, error) {
	disc, err := doc.EnsureObject("discovery")
	if err != nil {
		return "", err
	}
	existing, _ := disc.Array(list)
	kept := make([]any, 0, len(existing)+1)
	for _, item := range existing {
		if obj, ok := item.(*Object); ok {
			if v, _ := obj.String(keyField); v == keyValue {
				continue
			}
		}
		kept = append(kept, item)
	}
	kept = append(kept, entry)
	disc.Set(list, kept)
	return JoinKey("discovery", list), nil
}

func ensurePath(doc *Object, path ...string) (*Object, error) {
	cur := doc
	for _, key := range path {
		next, err := cur.EnsureObject(key)
		if err != nil {
			return nil, err
		}
		cur = next
	}
	return cur, nil
}

func firstTool(entry any) (string, bool) {
	argv, ok := entry.([]any)
	if !ok || len(argv) == 0 {
		return "", false
	}
	s, ok := argv[0].(string)
	return s, ok
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
