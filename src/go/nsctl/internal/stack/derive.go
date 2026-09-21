package stack

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bom"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
)

// Node is one instance of the environment a stack is derived from: what the
// BOM says, plus what the environment manifest adds (source, configuration,
// which stack declared it). NERD019 SPEC004.
type Node struct {
	Name         string
	Class        string
	Version      string
	Dependencies map[string][]string
	// Config is the environment manifest's declared instance_configuration
	// -- never the live instance's -- or nil.
	Config map[string]any
	// Local marks an instance deployed from a working tree: it has no
	// published artifact.
	Local bool
	// Stack is the name of the stack record that declared it, or "".
	Stack string
	// StackRef is that record's reference, for the suggestion.
	StackRef string
}

// Graph is the environment as a derivation sees it.
type Graph struct {
	nodes map[string]Node
}

// GraphFromBOM builds a graph from a BOM export (`nsctl bom show --json`)
// and, when present, the environment manifest.
func GraphFromBOM(entries []msdeploy.BOMEntry, m *manifest.Manifest) Graph {
	g := Graph{nodes: map[string]Node{}}
	for _, e := range entries {
		deps := map[string][]string{}
		for _, role := range e.Roles() {
			deps[role] = e.Targets(role)
		}
		g.nodes[e.RepoInstanceName] = Node{Name: e.RepoInstanceName, Class: e.RepoClassName, Version: e.RepoClassVersion, Dependencies: deps}
	}
	g.overlay(m)
	return g
}

// GraphFromInstances builds a graph from the live ms-deployment graph.
func GraphFromInstances(instances []msdeploy.DeployedInstance, m *manifest.Manifest) Graph {
	g := Graph{nodes: map[string]Node{}}
	for _, i := range instances {
		deps := map[string][]string{}
		for role, targets := range i.Dependencies {
			deps[role] = append([]string(nil), targets...)
		}
		g.nodes[i.Name] = Node{Name: i.Name, Class: i.RepoClassName, Version: i.RepoClassVersion, Dependencies: deps}
	}
	g.overlay(m)
	return g
}

// BOMFromInstances converts the live graph to the export shape, so
// --from-env can write the reference BOM CI will read.
func BOMFromInstances(instances []msdeploy.DeployedInstance) []msdeploy.BOMEntry {
	out := make([]msdeploy.BOMEntry, 0, len(instances))
	for _, i := range instances {
		deps := map[string]any{}
		for role, targets := range i.Dependencies {
			switch len(targets) {
			case 0:
			case 1:
				deps[role] = targets[0]
			default:
				list := make([]any, len(targets))
				for k, t := range targets {
					list[k] = t
				}
				deps[role] = list
			}
		}
		out = append(out, msdeploy.BOMEntry{
			RepoInstanceName: i.Name, RepoClassName: i.RepoClassName, RepoClassVersion: i.RepoClassVersion,
			Status: i.Status, Dependencies: deps,
		})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].RepoInstanceName < out[b].RepoInstanceName })
	return out
}

func (g *Graph) overlay(m *manifest.Manifest) {
	if m == nil {
		return
	}
	declaredBy := map[string]manifest.StackRecord{}
	for _, rec := range m.Stacks {
		for _, in := range rec.Owned() {
			declaredBy[in] = rec
		}
	}
	for _, r := range m.Repos {
		n, ok := g.nodes[r.InstanceName]
		if !ok {
			// Declared but not in the BOM (never applied): still part of the
			// environment as far as a derivation is concerned.
			n = Node{Name: r.InstanceName, Class: r.RepoClassName, Version: r.Version, Dependencies: map[string][]string{}}
			for role, target := range r.Dependencies {
				switch v := target.(type) {
				case string:
					n.Dependencies[role] = []string{v}
				case []any:
					for _, item := range v {
						if s, ok := item.(string); ok {
							n.Dependencies[role] = append(n.Dependencies[role], s)
						}
					}
				}
			}
		}
		// Only an explicit checkout is a working tree. An instance with no
		// source block resolves through the tier chain and was deployed at
		// the version the graph records, which is a published one; a stack
		// bundles it at that version and `stack build` finds the bytes.
		if r.Source != nil && r.Source.Type == manifest.SourceLocal {
			n.Local = true
		}
		if len(r.InstanceConfiguration) > 0 {
			n.Config = r.InstanceConfiguration
		}
		if rec, ok := declaredBy[r.InstanceName]; ok {
			n.Stack, n.StackRef = rec.Name, rec.Ref
		}
		g.nodes[r.InstanceName] = n
	}
}

// Node looks an instance up.
func (g Graph) Node(name string) (Node, bool) {
	n, ok := g.nodes[name]
	return n, ok
}

// Names lists every instance, sorted.
func (g Graph) Names() []string {
	names := make([]string, 0, len(g.nodes))
	for n := range g.nodes {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Kind is a row of SPEC004's classification table.
type Kind string

const (
	KindRoot       Kind = "root"         // a selected instance: a companion in its own profile
	KindCompanion  Kind = "companion"    // bundled, unconditional
	KindSubstrate  Kind = "substrate"    // bound to the reserved name; nothing bundled
	KindBundled    Kind = "bundled"      // a class nsctl ships: bound to the instance every environment has
	KindCrossStack Kind = "cross-stack"  // a role another stack fills; nothing bundled
	KindLocal      Kind = "working-tree" // no published artifact: refused unless bundled
)

// Row is one classified instance.
type Row struct {
	Instance string
	Class    string
	Version  string
	Kind     Kind
	// Role and Via say how the walk reached it: the dependency role on Via.
	// Empty for a root.
	Role string
	Via  string
	// Stack and Suggest are set for a cross-stack row.
	Stack   string
	Suggest string
	// Resource is the resource type a cross-stack role is matched by, when
	// the producer's class declares one.
	Resource string
	// HostSpecific lists configuration keys whose copied values look tied to
	// this machine, for the author to review.
	HostSpecific []string
}

// Options steer a derivation.
type Options struct {
	Roots []string
	// BundleLocal names working-tree instances to bundle rather than refuse.
	BundleLocal []string
	// IncludeProvided bundles instances other stacks declared rather than
	// referencing those stacks.
	IncludeProvided bool
	// ResourceOf answers a class's first produced resource type, or "".
	ResourceOf func(class string) string
	// Substrate reports a name the environment substrate provides.
	Substrate func(name string) bool
	// Bundled reports a class nsctl ships and every environment already
	// runs (ext-secrets, the graph); an instance of one is the
	// environment's, not the stack's.
	Bundled func(class string) bool
}

// Derivation is what Derive settles on.
type Derivation struct {
	Rows []Row
	// Local is the `local` section, and Dependencies the deploy.dependencies
	// block, as JSON-ready maps.
	Local        map[string]any
	Dependencies map[string]any
	// Refused lists working-tree instances that stopped the derivation.
	Refused []string
}

// ErrNoRoots is a derivation with nothing selected.
var ErrNoRoots = errors.New("nothing selected; pass --select <instance>,...")

// Derive walks from the roots down their required dependencies and
// classifies every instance reached. SPEC004.
func Derive(g Graph, opts Options) (*Derivation, error) {
	if len(opts.Roots) == 0 {
		return nil, ErrNoRoots
	}
	substrate := opts.Substrate
	if substrate == nil {
		substrate = func(name string) bool { return bom.IsSubstrate(name) || manifest.ScopeEnvironment.Reserved(name) }
	}
	resourceOf := opts.ResourceOf
	if resourceOf == nil {
		resourceOf = func(string) string { return "" }
	}
	bundleLocal := map[string]bool{}
	for _, b := range opts.BundleLocal {
		bundleLocal[b] = true
	}
	isRoot := map[string]bool{}
	for _, r := range opts.Roots {
		if _, ok := g.Node(r); !ok {
			return nil, fmt.Errorf("no instance %q in the environment; it has: %s", r, strings.Join(g.Names(), ", "))
		}
		isRoot[r] = true
	}

	d := &Derivation{}
	seen := map[string]bool{}
	type visit struct{ name, role, via string }
	queue := make([]visit, 0, len(opts.Roots))
	for _, r := range opts.Roots {
		queue = append(queue, visit{name: r})
	}
	var rows []Row
	for len(queue) > 0 {
		v := queue[0]
		queue = queue[1:]
		if seen[v.name] {
			continue
		}
		seen[v.name] = true
		n, _ := g.Node(v.name)
		row := Row{Instance: n.Name, Class: n.Class, Version: n.Version, Role: v.role, Via: v.via}
		switch {
		case substrate(n.Name):
			row.Kind = KindSubstrate
			rows = append(rows, row)
			continue // the substrate's own dependencies are the environment's
		case opts.Bundled != nil && opts.Bundled(n.Class) && !isRoot[n.Name]:
			row.Kind = KindBundled
			rows = append(rows, row)
			continue
		case n.Stack != "" && !opts.IncludeProvided && !isRoot[n.Name]:
			row.Kind = KindCrossStack
			row.Stack, row.Suggest, row.Resource = n.Stack, n.StackRef, resourceOf(n.Class)
			rows = append(rows, row)
			continue // the other stack owns its dependencies
		case n.Local && !bundleLocal[n.Name]:
			row.Kind = KindLocal
			d.Refused = append(d.Refused, n.Name)
		case isRoot[n.Name]:
			row.Kind = KindRoot
		default:
			row.Kind = KindCompanion
		}
		row.HostSpecific = hostSpecific(n.Config)
		rows = append(rows, row)
		for _, role := range sortedRoles(n.Dependencies) {
			for _, target := range n.Dependencies[role] {
				if _, ok := g.Node(target); ok {
					queue = append(queue, visit{name: target, role: role, via: n.Name})
				}
			}
		}
	}
	d.Rows = rows
	if len(d.Refused) > 0 {
		sort.Strings(d.Refused)
		return d, fmt.Errorf("%s deployed from a working tree and %s no published artifact; bundle %s with --bundle-local %s, or select something else",
			strings.Join(d.Refused, ", "), plural(len(d.Refused), "has", "have"), plural(len(d.Refused), "it", "them"), strings.Join(d.Refused, ","))
	}
	d.Local, d.Dependencies = render(g, rows, isRoot)
	return d, nil
}

// render turns rows into the `local` section and deploy.dependencies.
func render(g Graph, rows []Row, isRoot map[string]bool) (map[string]any, map[string]any) {
	bound := map[string]string{}  // instance -> substrate name, for rewriting
	roleOf := map[string]string{} // cross-stack instance -> role key
	deps := map[string]any{}
	gates := map[string]any{}
	var repos []any
	var profiles []string

	for _, r := range rows {
		switch r.Kind {
		case KindSubstrate, KindBundled:
			bound[r.Instance] = r.Instance
			role := r.Role
			if role == "" {
				role = r.Instance
			}
			deps[role] = map[string]any{"repo_class_name": r.Class, "required": "true"}
			gates[role] = map[string]any{"bind": r.Instance}
		case KindCrossStack:
			role := r.Role
			if role == "" {
				role = r.Instance
			}
			roleOf[r.Instance] = role
			dep := map[string]any{"repo_class_name": r.Class, "required": "true", "version_spec": "== " + r.Version}
			if ns, name, ok := strings.Cut(r.Resource, "/"); ok {
				dep["resource"] = map[string]any{"resource_namespace": ns, "resource_definition_name": name}
			}
			deps[role] = dep
			gate := map[string]any{"external": true}
			if r.Suggest != "" {
				gate["suggest"] = r.Suggest
			} else if r.Stack != "" {
				gate["suggest"] = r.Stack
			}
			gates[role] = gate
		}
	}
	for _, r := range rows {
		if r.Kind != KindRoot && r.Kind != KindCompanion && r.Kind != KindLocal {
			continue
		}
		n, _ := g.Node(r.Instance)
		repo := map[string]any{
			"instance_name":   r.Instance,
			"repo_class_name": r.Class,
			"version_spec":    "== " + r.Version,
		}
		if r.Kind == KindRoot {
			repo["profiles"] = []any{r.Instance}
			profiles = append(profiles, r.Instance)
		}
		if len(n.Dependencies) > 0 {
			rewritten := map[string]any{}
			for _, role := range sortedRoles(n.Dependencies) {
				targets := n.Dependencies[role]
				var out []any
				for _, t := range targets {
					switch {
					case bound[t] != "":
						out = append(out, bound[t])
					case roleOf[t] != "":
						out = append(out, roleOf[t])
					default:
						out = append(out, t)
					}
				}
				if len(out) == 1 {
					rewritten[role] = out[0]
				} else if len(out) > 1 {
					rewritten[role] = out
				}
			}
			if len(rewritten) > 0 {
				repo["dependencies"] = rewritten
			}
		}
		if len(n.Config) > 0 {
			repo["instance_configuration"] = n.Config
		}
		repos = append(repos, repo)
	}
	sort.Strings(profiles)
	local := map[string]any{
		"version":          1,
		"default_profiles": toAny(profiles),
		"repos":            repos,
	}
	if len(gates) > 0 {
		local["dependencies"] = gates
	}
	return local, deps
}

// hostSpecific lists configuration keys whose values look tied to one
// machine: absolute paths, loopback hosts, and ports.
func hostSpecific(config map[string]any) []string {
	var keys []string
	for k, v := range config {
		s, ok := v.(string)
		if !ok {
			continue
		}
		if hostSpecificRe.MatchString(s) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

var hostSpecificRe = regexp.MustCompile(`^/|localhost|127\.0\.0\.1|host\.docker\.internal|:\d{4,5}\b`)

func sortedRoles(deps map[string][]string) []string {
	roles := make([]string, 0, len(deps))
	for r := range deps {
		roles = append(roles, r)
	}
	sort.Strings(roles)
	return roles
}

func toAny(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// ManifestJSON renders the stack RepoClass manifest a derivation produces.
func (d *Derivation) ManifestJSON(name, description string) ([]byte, error) {
	doc := map[string]any{
		"name":        name,
		"description": description,
		"build":       map[string]any{},
		"deploy": map[string]any{
			"commands": []any{[]any{"exec", "true"}},
		},
		"local": d.Local,
	}
	if len(d.Dependencies) > 0 {
		doc["deploy"].(map[string]any)["dependencies"] = d.Dependencies
	}
	return json.MarshalIndent(doc, "", "  ")
}

// LocalDiffers reports whether a manifest's `local` section differs from
// this derivation's, comparing canonical JSON. For --diff.
func (d *Derivation) LocalDiffers(manifestJSON []byte) (bool, string, error) {
	var existing struct {
		Local any `json:"local"`
	}
	if err := json.Unmarshal(manifestJSON, &existing); err != nil {
		return false, "", err
	}
	a, err := json.Marshal(existing.Local)
	if err != nil {
		return false, "", err
	}
	// Round-trip the derived section through JSON so types compare equal.
	raw, err := json.Marshal(d.Local)
	if err != nil {
		return false, "", err
	}
	var derived any
	_ = json.Unmarshal(raw, &derived)
	b, _ := json.Marshal(derived)
	if string(a) == string(b) {
		return false, "", nil
	}
	return true, fmt.Sprintf("checked in: %s\nderived:    %s", a, b), nil
}
