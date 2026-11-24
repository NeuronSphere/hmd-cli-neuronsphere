package bacon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// System is the BACON `system` key (hmd-docs-bacon NERD0002; NERD041): the
// group of repo classes a repo class is worked on with. Name is required;
// Role says what part it plays; Description and Parent define the system and
// belong only in the manifest that introduces it.
type System struct {
	Name        string
	Role        string
	Description string
	Parent      string
}

// SystemRoles are the closed set of roles NERD0002 allows, in its order.
var SystemRoles = []string{"core", "engine", "image", "library", "ui", "cli", "docs"}

var systemNamePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

const systemNameRule = "lower-case letters, digits and single hyphens, starting with a letter"

func (s System) validate() error {
	if !systemNamePattern.MatchString(s.Name) {
		return fmt.Errorf("system name %q must be %s", s.Name, systemNameRule)
	}
	if s.Role != "" && !contains(SystemRoles, s.Role) {
		return fmt.Errorf("role %q must be one of %s", s.Role, strings.Join(SystemRoles, ", "))
	}
	if s.Parent != "" && !systemNamePattern.MatchString(s.Parent) {
		return fmt.Errorf("parent %q must be %s", s.Parent, systemNameRule)
	}
	if strings.TrimSpace(s.Parent) == "" && s.Parent != "" {
		return fmt.Errorf("parent must not be blank")
	}
	if s.Parent != "" && s.Parent == s.Name {
		return fmt.Errorf("system %q cannot be its own parent", s.Name)
	}
	return nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// SetSystem writes the `system` key whole (NERD041 SPEC001): the string
// shorthand when only the name is given, else the object form holding only
// the fields given. A refused value leaves the document untouched.
func SetSystem(doc *Object, s System) (string, error) {
	s.Description = strings.TrimSpace(s.Description)
	if err := s.validate(); err != nil {
		return "", err
	}
	if s.Role == "" && s.Description == "" && s.Parent == "" {
		doc.Set("system", s.Name)
		return "system", nil
	}
	obj := NewObject()
	obj.Set("name", s.Name)
	if s.Role != "" {
		obj.Set("role", s.Role)
	}
	if s.Description != "" {
		obj.Set("description", s.Description)
	}
	if s.Parent != "" {
		obj.Set("parent", s.Parent)
	}
	doc.Set("system", obj)
	return "system", nil
}

// ClearSystem removes the key; ok reports whether there was one (SPEC002).
func ClearSystem(doc *Object) (key string, ok bool) {
	return "system", doc.Delete("system")
}

// SystemOf reads the key in either form. ok is false when there is none; an
// error means the key is there but is not what NERD0002's schema allows.
func SystemOf(doc *Object) (System, bool, error) {
	raw, ok := doc.Get("system")
	if !ok {
		return System{}, false, nil
	}
	var s System
	switch v := raw.(type) {
	case string:
		s.Name = v
	case *Object:
		for _, key := range v.Keys() {
			val, _ := v.Get(key)
			str, isString := val.(string)
			if !isString {
				return System{}, true, fmt.Errorf("system.%s must be a string", key)
			}
			switch key {
			case "name":
				s.Name = str
			case "role":
				s.Role = str
			case "description":
				s.Description = str
			case "parent":
				s.Parent = str
			default:
				return System{}, true, fmt.Errorf("system.%s is not part of the key (name, role, description, parent)", key)
			}
		}
	default:
		return System{}, true, errors.New("system must be a name or an object with name, role, description and parent")
	}
	if err := s.validate(); err != nil {
		return System{}, true, err
	}
	return s, true, nil
}

// describeSystem is the describe entry (SPEC003): always the object form,
// holding only what the manifest says.
func describeSystem(doc *Object) (*Object, bool) {
	s, ok, err := SystemOf(doc)
	if !ok || err != nil {
		return nil, false
	}
	obj := NewObject()
	obj.Set("name", s.Name)
	for _, kv := range [][2]string{{"role", s.Role}, {"description", s.Description}, {"parent", s.Parent}} {
		if kv[1] != "" {
			obj.Set(kv[0], kv[1])
		}
	}
	return obj, true
}

// SystemEntry is one repo class's declaration, as read from its manifest.
type SystemEntry struct {
	Manifest  string
	RepoClass string
	System    System
}

// SystemMember is a repo class in a resolved system.
type SystemMember struct {
	RepoClass string `json:"repo_class"`
	Role      string `json:"role,omitempty"`
	Manifest  string `json:"manifest"`
}

// ResolvedSystem is one system after resolution across a workspace.
type ResolvedSystem struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parent      string         `json:"parent,omitempty"`
	Members     []SystemMember `json:"members"`
	Subsystems  []string       `json:"subsystems"`
}

// SystemConflict is two or more manifests defining the same field of one
// system differently. Nothing picks a winner (NERD0002 SPEC0004).
type SystemConflict struct {
	System    string   `json:"system"`
	Field     string   `json:"field"`
	Values    []string `json:"values"`
	Manifests []string `json:"manifests"`
}

// SystemsReport is what `nsctl repoclass systems` prints.
type SystemsReport struct {
	Systems   []ResolvedSystem `json:"systems"`
	Conflicts []SystemConflict `json:"conflicts"`
	Cycles    [][]string       `json:"cycles"`
	Problems  []string         `json:"problems"`
}

// OK is the exit status: no conflict, no cycle, no unreadable key.
func (r SystemsReport) OK() bool {
	return len(r.Conflicts) == 0 && len(r.Cycles) == 0 && len(r.Problems) == 0
}

// ResolveSystems groups declarations by system and resolves each system's
// definition from the inline fields (NERD041 SPEC004): agreeing manifests
// resolve, differing ones are a conflict and resolve to nothing. A system
// named only as a parent is listed; parent loops are reported once each.
func ResolveSystems(entries []SystemEntry) SystemsReport {
	type defs struct {
		members []SystemMember
		fields  map[string]map[string][]string // field -> value -> manifests
	}
	all := map[string]*defs{}
	get := func(name string) *defs {
		d, ok := all[name]
		if !ok {
			d = &defs{fields: map[string]map[string][]string{"description": {}, "parent": {}}}
			all[name] = d
		}
		return d
	}
	for _, e := range entries {
		d := get(e.System.Name)
		d.members = append(d.members, SystemMember{RepoClass: e.RepoClass, Role: e.System.Role, Manifest: e.Manifest})
		if e.System.Description != "" {
			d.fields["description"][e.System.Description] = append(d.fields["description"][e.System.Description], e.Manifest)
		}
		if e.System.Parent != "" {
			d.fields["parent"][e.System.Parent] = append(d.fields["parent"][e.System.Parent], e.Manifest)
			get(e.System.Parent)
		}
	}

	var report SystemsReport
	resolved := map[string]*ResolvedSystem{}
	names := make([]string, 0, len(all))
	for name := range all {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		d := all[name]
		rs := &ResolvedSystem{Name: name, Members: d.members, Subsystems: []string{}}
		if rs.Members == nil {
			rs.Members = []SystemMember{}
		}
		sort.Slice(rs.Members, func(i, j int) bool { return rs.Members[i].RepoClass < rs.Members[j].RepoClass })
		for _, field := range []string{"description", "parent"} {
			values := d.fields[field]
			switch len(values) {
			case 0:
			case 1:
				for v := range values {
					if field == "description" {
						rs.Description = v
					} else {
						rs.Parent = v
					}
				}
			default:
				c := SystemConflict{System: name, Field: field}
				for v, manifests := range values {
					c.Values = append(c.Values, v)
					c.Manifests = append(c.Manifests, manifests...)
				}
				sort.Strings(c.Values)
				sort.Strings(c.Manifests)
				report.Conflicts = append(report.Conflicts, c)
			}
		}
		resolved[name] = rs
	}
	for _, name := range names {
		if p := resolved[name].Parent; p != "" {
			resolved[p].Subsystems = append(resolved[p].Subsystems, name)
		}
	}
	report.Cycles = parentCycles(names, resolved)
	for _, name := range names {
		report.Systems = append(report.Systems, *resolved[name])
	}
	if report.Conflicts == nil {
		report.Conflicts = []SystemConflict{}
	}
	if report.Cycles == nil {
		report.Cycles = [][]string{}
	}
	report.Problems = []string{}
	return report
}

// parentCycles follows each system's resolved parent and reports every loop
// once, rotated to start at its smallest name.
func parentCycles(names []string, resolved map[string]*ResolvedSystem) [][]string {
	var cycles [][]string
	seen := map[string]bool{}
	for _, start := range names {
		index := map[string]int{}
		var path []string
		for cur := start; cur != ""; cur = resolved[cur].Parent {
			if i, ok := index[cur]; ok {
				loop := append([]string(nil), path[i:]...)
				min := 0
				for k := range loop {
					if loop[k] < loop[min] {
						min = k
					}
				}
				loop = append(loop[min:], loop[:min]...)
				key := strings.Join(loop, "\x00")
				if !seen[key] {
					seen[key] = true
					cycles = append(cycles, loop)
				}
				break
			}
			index[cur] = len(path)
			path = append(path, cur)
		}
	}
	return cycles
}

// ScanSystems reads the repo classes directly under each root (and the root
// itself when it is one), skipping hidden directories and git worktrees, and
// returns each declared system. A manifest whose key is malformed is a
// problem, named by its path; a repo class without the key is skipped.
func ScanSystems(roots []string) ([]SystemEntry, []string, error) {
	var entries []SystemEntry
	var problems []string
	visit := func(dir string) {
		s, err := Open(dir)
		if err != nil {
			return
		}
		sys, ok, err := SystemOf(s.Doc)
		if !ok {
			return
		}
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", s.Path, err))
			return
		}
		name, _ := s.Doc.String("name")
		entries = append(entries, SystemEntry{Manifest: s.Path, RepoClass: name, System: sys})
	}
	for _, root := range roots {
		abs, err := filepath.Abs(root)
		if err != nil {
			return nil, nil, err
		}
		visit(abs)
		children, err := os.ReadDir(abs)
		if err != nil {
			return nil, nil, err
		}
		for _, c := range children {
			if !c.IsDir() || strings.HasPrefix(c.Name(), ".") {
				continue
			}
			dir := filepath.Join(abs, c.Name())
			if fi, err := os.Lstat(filepath.Join(dir, ".git")); err == nil && !fi.IsDir() {
				continue // a worktree: its main checkout is the repo class
			}
			visit(dir)
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Manifest < entries[j].Manifest })
	sort.Strings(problems)
	return entries, problems, nil
}
