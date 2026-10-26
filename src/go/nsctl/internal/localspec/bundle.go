package localspec

// Bundle files: meta-data/bundles/<name>.json (NERD0019, hmd-ms-deployment).
//
// A stack and a deployment-service Bundle are one noun, `bundle`. A repository
// that ships a bundle file needs no `local` section: the file says everything
// the section did, in the vocabulary the deployment service already resolves,
// and this file translates it into the same Manifest so the planner, the lock
// and `stack build` read one shape whichever the repository wrote.
//
//   - A role with `provision: "false"` is reuse-only: the environment fills it.
//     It becomes a dependency, bound to the instance the bundle file's
//     local.roles.<role>.bind names, or external -- matched by resource or
//     class at plan time -- when nothing binds it.
//   - Every other role is provisioned and becomes a companion, named by its
//     instance_name or else the role. Its `bind` ({member role: bundle role})
//     is resolved here to the instance each bundle role will have, which is
//     what a companion's dependencies name.
//   - An optional provisioned role is gated by local.roles.<role>.profiles, or
//     by a profile named after the role when the file gives none: optional in
//     a bundle means omitted until asked for, and a profile is how a developer
//     asks.
//   - local.default_profiles is the section's default_profiles.
//
// Server-only fields -- config_schema, default_configuration, config_map,
// dedicated, version on the file -- mean nothing to a local plan and are not
// read here. local.reuse is the server's concern too: locally, a bound role is
// reused and an unbound one composes against the environment (NERD017
// SPEC010), which is `auto`.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// BundleDir is where a repository's bundle files live, relative to its root.
var BundleDir = filepath.Join("meta-data", "bundles")

type bundleFile struct {
	BundleName string `json:"bundle_name"`
	Roles      map[string]struct {
		RepoClassName string `json:"repo_class_name"`
		VersionSpec   string `json:"version_spec"`
		InstanceName  string `json:"instance_name"`
		Required      any    `json:"required"`
		Provision     any    `json:"provision"`
		Resource      *struct {
			Namespace string `json:"resource_namespace"`
			Name      string `json:"resource_definition_name"`
		} `json:"resource"`
		Bind                  map[string]any `json:"bind"`
		InstanceConfiguration map[string]any `json:"instance_configuration"`
	} `json:"roles"`
	Local *struct {
		DefaultProfiles []string `json:"default_profiles"`
		Roles           map[string]struct {
			Profiles []string `json:"profiles"`
			Bind     string   `json:"bind"`
		} `json:"roles"`
	} `json:"local"`
}

// BundleNames is the bundle files a repository ships, by file name, sorted.
func BundleNames(repoDir string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(repoDir, BundleDir))
	switch {
	case os.IsNotExist(err):
		return nil, nil
	case err != nil:
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, strings.TrimSuffix(e.Name(), ".json"))
		}
	}
	sort.Strings(names)
	return names, nil
}

// LoadBundle reads repoDir/meta-data/bundles/<name>.json, with the manifest
// beside it for the repository's own name.
func LoadBundle(repoDir, name string) (*Manifest, error) {
	path := filepath.Join(repoDir, FileName)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var doc struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%s: parsing the manifest: %w", path, err)
	}
	bundlePath := filepath.Join(repoDir, BundleDir, name+".json")
	raw, err := os.ReadFile(bundlePath)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", bundlePath, err)
	}
	m, err := ParseBundle(raw, name)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", bundlePath, err)
	}
	m.RepoClassName = doc.Name
	m.Path = bundlePath
	return m, nil
}

// ParseBundle builds a Manifest from a bundle file. fallbackName names the
// bundle when the file carries no bundle_name.
func ParseBundle(data []byte, fallbackName string) (*Manifest, error) {
	var doc bundleFile
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parsing the bundle: %w", err)
	}
	name := doc.BundleName
	if name == "" {
		name = fallbackName
	}
	m := &Manifest{Bundle: name}
	m.Local.Version = Version
	m.Local.Stack = true
	var problems []string

	type localRole struct {
		Profiles []string
		Bind     string
	}
	local := map[string]localRole{}
	if doc.Local != nil {
		m.Local.DefaultProfiles = doc.Local.DefaultProfiles
		for role, l := range doc.Local.Roles {
			local[role] = localRole{Profiles: l.Profiles, Bind: strings.TrimSpace(l.Bind)}
		}
	}

	roles := make([]string, 0, len(doc.Roles))
	for role := range doc.Roles {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	if len(roles) == 0 {
		problems = append(problems, "the bundle declares no roles")
	}
	for role := range local {
		if _, ok := doc.Roles[role]; !ok {
			problems = append(problems, fmt.Sprintf("local.roles.%s: the bundle has no role %q", role, role))
		}
	}

	// The instance each role will have once planned, which is what a
	// provisioned member's bind resolves to.
	reuseOnly := map[string]bool{}
	instanceOf := map[string]string{}
	for _, role := range roles {
		r := doc.Roles[role]
		provision, err := truthyDefault(r.Provision, true)
		if err != nil {
			problems = append(problems, fmt.Sprintf("roles.%s.provision: %v", role, err))
			continue
		}
		reuseOnly[role] = !provision
		switch {
		case !provision && local[role].Bind != "":
			instanceOf[role] = local[role].Bind
		case r.InstanceName != "":
			instanceOf[role] = r.InstanceName
		default:
			instanceOf[role] = role
		}
	}

	for _, role := range roles {
		r := doc.Roles[role]
		where := "roles." + role
		hasResource := r.Resource != nil && r.Resource.Namespace != "" && r.Resource.Name != ""
		if r.RepoClassName == "" && !hasResource {
			problems = append(problems, where+": name a 'repo_class_name' or a 'resource'")
			continue
		}
		required, err := truthyDefault(r.Required, true)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s.required: %v", where, err))
			continue
		}
		l := local[role]

		if reuseOnly[role] {
			d := Dependency{
				Role:          role,
				RepoClassName: r.RepoClassName,
				VersionSpec:   strings.TrimSpace(r.VersionSpec),
				InstanceName:  r.InstanceName,
				Required:      required,
			}
			if hasResource {
				d.Resource = r.Resource.Namespace + "/" + r.Resource.Name
			}
			if required && len(l.Profiles) > 0 {
				problems = append(problems, fmt.Sprintf(
					"local.roles.%s: %q is required, and a required role the environment fills cannot be profile-gated", role, role))
				continue
			}
			m.Dependencies = append(m.Dependencies, d)
			if m.Local.Dependencies == nil {
				m.Local.Dependencies = map[string]Gate{}
			}
			m.Local.Dependencies[role] = Gate{Bind: l.Bind, External: l.Bind == "", Profiles: l.Profiles}
			continue
		}

		if l.Bind != "" {
			problems = append(problems, fmt.Sprintf(
				"local.roles.%s.bind: only a reuse-only role (provision \"false\") is bound to an existing instance", role))
			continue
		}
		if r.RepoClassName == "" {
			problems = append(problems, where+": a provisioned role needs a 'repo_class_name' to deploy")
			continue
		}
		var deps map[string]any
		if len(r.Bind) > 0 {
			deps = map[string]any{}
			memberRoles := make([]string, 0, len(r.Bind))
			for k := range r.Bind {
				memberRoles = append(memberRoles, k)
			}
			sort.Strings(memberRoles)
			for _, memberRole := range memberRoles {
				resolved, err := resolveBind(r.Bind[memberRole], instanceOf)
				if err != nil {
					problems = append(problems, fmt.Sprintf("%s.bind.%s: %v", where, memberRole, err))
					continue
				}
				deps[memberRole] = resolved
			}
		}
		profiles := l.Profiles
		if !required && len(profiles) == 0 {
			profiles = []string{role}
		}
		m.Local.Repos = append(m.Local.Repos, Repo{
			InstanceName:          instanceOf[role],
			RepoClassName:         r.RepoClassName,
			VersionSpec:           strings.TrimSpace(r.VersionSpec),
			Profiles:              profiles,
			Dependencies:          deps,
			InstanceConfiguration: r.InstanceConfiguration,
		})
	}

	if len(problems) > 0 {
		return nil, &InvalidError{Problems: problems}
	}
	return m, nil
}

// resolveBind turns a bind target -- one bundle role, or a list of them -- into
// the instance name(s) those roles will have.
func resolveBind(target any, instanceOf map[string]string) (any, error) {
	one := func(v any) (string, error) {
		role, ok := v.(string)
		if !ok {
			return "", fmt.Errorf("a bind names a bundle role, got %v", v)
		}
		instance, known := instanceOf[role]
		if !known {
			return "", fmt.Errorf("the bundle has no role %q", role)
		}
		return instance, nil
	}
	if list, ok := target.([]any); ok {
		out := make([]any, 0, len(list))
		for _, v := range list {
			instance, err := one(v)
			if err != nil {
				return nil, err
			}
			out = append(out, instance)
		}
		return out, nil
	}
	return one(target)
}

// truthyDefault reads a BACON boolean, taking def when it is absent.
func truthyDefault(v any, def bool) (bool, error) {
	if v == nil {
		return def, nil
	}
	return truthy(v)
}

// ToBundle writes a manifest's local section as a bundle file (NERD0019
// SPEC0006): what `nsctl bundle migrate` puts in meta-data/bundles/<name>.json.
//
// A dependency role the section binds or marks external is reuse-only; one it
// declares an instance for, and every companion, is provisioned. A companion's
// dependencies name instances, and a bundle's bind names roles, so each
// instance is mapped back to the role that fills it -- which is why a
// dependency on an instance no role fills is refused rather than dropped.
// ParseBundle of the result activates the same wants for every profile set.
func ToBundle(m *Manifest, name string) ([]byte, error) {
	roles := map[string]any{}
	localRoles := map[string]any{}
	roleOfInstance := map[string]string{}
	claim := func(instance, role string) {
		if _, taken := roleOfInstance[instance]; !taken {
			roleOfInstance[instance] = role
		}
	}
	required := func(b bool) string {
		if b {
			return "true"
		}
		return "false"
	}

	type pending struct {
		role string
		decl map[string]any
		deps map[string]any
	}
	var provisioned []pending

	for _, d := range m.Dependencies {
		gate := m.Local.Dependencies[d.Role]
		decl := map[string]any{"required": required(d.Required)}
		if d.RepoClassName != "" {
			decl["repo_class_name"] = d.RepoClassName
		}
		if d.VersionSpec != "" {
			decl["version_spec"] = d.VersionSpec
		}
		if d.InstanceName != "" {
			decl["instance_name"] = d.InstanceName
		}
		if ns, rd, ok := strings.Cut(d.Resource, "/"); ok {
			decl["resource"] = map[string]any{"resource_namespace": ns, "resource_definition_name": rd}
		}
		local := map[string]any{}
		if len(gate.Profiles) > 0 {
			local["profiles"] = gate.Profiles
		}
		instance := d.InstanceName
		if instance == "" {
			instance = d.Role
		}
		switch {
		case gate.Bind != "":
			decl["provision"] = "false"
			local["bind"] = gate.Bind
			claim(gate.Bind, d.Role)
		case gate.External || d.RepoClassName == "":
			decl["provision"] = "false"
			claim(instance, d.Role)
		default:
			if len(gate.InstanceConfiguration) > 0 {
				decl["instance_configuration"] = gate.InstanceConfiguration
			}
			provisioned = append(provisioned, pending{d.Role, decl, gate.Dependencies})
			claim(instance, d.Role)
		}
		roles[d.Role] = decl
		if len(local) > 0 {
			localRoles[d.Role] = local
		}
	}
	for _, r := range m.Local.Repos {
		decl := map[string]any{
			"repo_class_name": r.RepoClassName,
			"required":        required(len(r.Profiles) == 0),
		}
		if r.VersionSpec != "" {
			decl["version_spec"] = r.VersionSpec
		}
		if len(r.InstanceConfiguration) > 0 {
			decl["instance_configuration"] = r.InstanceConfiguration
		}
		if len(r.Profiles) > 0 {
			localRoles[r.InstanceName] = map[string]any{"profiles": r.Profiles}
		}
		roles[r.InstanceName] = decl
		provisioned = append(provisioned, pending{r.InstanceName, decl, r.Dependencies})
		claim(r.InstanceName, r.InstanceName)
	}

	var problems []string
	for _, p := range provisioned {
		if len(p.deps) == 0 {
			continue
		}
		bind := map[string]any{}
		for memberRole, target := range p.deps {
			resolved, err := roleFor(target, roleOfInstance)
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s.dependencies.%s: %v", p.role, memberRole, err))
				continue
			}
			bind[memberRole] = resolved
		}
		p.decl["bind"] = bind
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return nil, &InvalidError{Problems: problems}
	}

	doc := map[string]any{"bundle_name": name, "roles": roles}
	local := map[string]any{}
	if len(m.Local.DefaultProfiles) > 0 {
		local["default_profiles"] = m.Local.DefaultProfiles
	}
	if len(localRoles) > 0 {
		local["roles"] = localRoles
	}
	if len(local) > 0 {
		doc["local"] = local
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// roleFor maps a dependency's instance name(s) to the bundle role(s) filling them.
func roleFor(target any, roleOfInstance map[string]string) (any, error) {
	one := func(v any) (string, error) {
		instance, ok := v.(string)
		if !ok {
			return "", fmt.Errorf("an instance name was expected, got %v", v)
		}
		role, known := roleOfInstance[instance]
		if !known {
			return "", fmt.Errorf("names %q, which no role of the bundle fills; declare it as a dependency or a companion", instance)
		}
		return role, nil
	}
	if list, ok := target.([]any); ok {
		out := make([]any, 0, len(list))
		for _, v := range list {
			role, err := one(v)
			if err != nil {
				return nil, err
			}
			out = append(out, role)
		}
		return out, nil
	}
	return one(target)
}
