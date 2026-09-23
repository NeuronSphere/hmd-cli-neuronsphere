// Package localspec reads the `local` section of a BACON manifest: the
// versioned RepoClasses a repository needs standing up alongside it in order to
// be worth testing, and which of them are optional.
//
// A BACON manifest already declares the repository's real dependencies, by role,
// with a version specifier, and this package does not duplicate that. Two things
// it cannot express are what the `local` section adds. First, companions that are
// not dependencies: a service whose endpoints are *called by* a Transform which
// reads from a Librarian depends on neither, yet locally they are exactly what is
// needed to see whether it works -- there is nowhere in BACON to say so, because
// BACON describes a deployment graph and this is a test fixture. Second,
// optionality with more than one axis: `required: "false"` is a single boolean
// evaluated the same way every time, where a developer needs to run lean while
// iterating and wide before opening a pull request, from one checkout, without
// editing anything.
//
// NERD010 SPEC001.
//
// # Where the section is read from
//
// SPEC001 says the section is read through NERD009 SPEC006's manifest store,
// with its meta-data/manifest.toml, meta-data/manifest.json and repo-root
// neuronsphere.toml tiers. **That store does not exist.** NERD009 is entirely
// proposed; go-toml/v2 is linked into the binary but internal/nsconfig is its
// only importer, and nothing in this module reads a TOML manifest or a repo-root
// neuronsphere.toml at all.
//
// So this package parses meta-data/manifest.json with encoding/json, the way
// every other BACON reader here already does -- repoclass.Manifest,
// librarian.PreBuildArtifacts, cmd.repoIdentityFrom. Implementing the store on
// the way past was rejected deliberately: it is NERD009's, it changes what forty
// Python packages write, and folding it in here would make neither change
// reviewable.
//
// One consequence worth stating for whoever implements that store. SPEC001's
// requirement that the in-memory document be an ordered map, so every key nsctl
// does not model survives a rewrite, does not bite here **because this package
// only reads**. Every write to the `local` section is NERD009's `repoclass`
// store, per SPEC008. Do not grow a writer on top of these structs; they model
// what nsctl understands and would drop the rest.
package localspec

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/repoclass"
)

// Version is the `local` schema version this package understands. A section
// naming no version is read as this one; anything else is refused.
const Version = 1

// FileName is the manifest this package reads, relative to a repo root.
var FileName = filepath.Join("meta-data", "manifest.json")

// Dependency is one entry of deploy.dependencies.
type Dependency struct {
	Role          string
	RepoClassName string
	VersionSpec   string
	// InstanceName is the block's own instance_name, which only 9 of the 517
	// dependency blocks in a full workspace carry. It is tier three of the
	// naming order when present.
	InstanceName string
	Required     bool
	// Resource is the dependency's resource type as "<namespace>/<name>",
	// from the block's `resource`, or "" for a name-only dependency. What
	// NERD017 SPEC010 matches an environment's producers against.
	Resource string
}

// Repo is one entry of local.repos: a companion to start alongside the
// repository, which is not a dependency of it.
//
// It is manifest.Repo's shape extended with Profiles and with Version replaced
// by VersionSpec -- the concrete version is the lock's job, and an author
// writing one here would be hand-maintaining a lock.
type Repo struct {
	InstanceName          string
	RepoClassName         string
	VersionSpec           string
	Profiles              []string
	Dependencies          map[string]any
	InstanceConfiguration map[string]any
}

// Gate is one entry of local.dependencies: what the `local` section adds to a
// dependency role beyond what deploy.dependencies already says.
//
// Profiles gate an *optional* dependency; gating a required one is a manifest
// error, see Load. The other three fields say how the role is filled locally,
// and apply to required and optional roles alike, because BACON has nowhere to
// say either of these things:
//
//   - Bind names an instance the environment already provides -- the substrate's
//     local-neuronsphere, or the ext-secrets the control plane deploys -- so the
//     role is bound to it and nothing is declared or pinned for it. A cloud
//     manifest names hmd-inf-eks-node-group for its compute; locally the k3s node
//     *is* local-neuronsphere, and no artifact of that class could stand in.
//   - Dependencies and InstanceConfiguration are what the declared dependency
//     instance itself needs. deploy.dependencies describes an edge, not the node
//     at its far end: hmd-database-account cannot deploy without a
//     database-instance and a db_name, and nothing but the repository that wants
//     it locally knows which those should be. Companions carry the same two
//     fields for the same reason.
//
// A bound role declares nothing, so Bind with either of the other two is
// refused rather than one of them silently losing.
type Gate struct {
	Profiles              []string
	Bind                  string
	Dependencies          map[string]any
	InstanceConfiguration map[string]any
	// External marks a role the environment must fill -- another stack's
	// instance, matched by resource -- so nothing is pinned or bundled for
	// it and `stack add` refuses when nothing provides it (NERD017 SPEC010).
	External bool
	// Suggest is a stack reference that would satisfy the role, named in the
	// refusal when nothing in the environment does. Never acted on.
	Suggest string
}

// Local is the `local` section.
type Local struct {
	Version         int
	DefaultProfiles []string
	// Stack marks the repository as a stack: a RepoClass that exists to name
	// other RepoClasses (NERD017 SPEC001). It decides whether the repository
	// itself becomes an instance, which shape alone cannot -- a wrapper and an
	// ordinary repository under test both carry a local section and may both
	// omit deploy.commands.
	Stack        bool
	Repos        []Repo
	Dependencies map[string]Gate
}

// Manifest is the part of a BACON manifest this package reads.
type Manifest struct {
	// Path is the file it was read from, for messages that should name it.
	Path string
	// RepoClassName is BACON's top-level `name`.
	RepoClassName string
	// Dependencies is deploy.dependencies, sorted by role -- so a lock generated
	// twice from one manifest is the same bytes, which Go's map iteration order
	// would otherwise prevent.
	Dependencies []Dependency
	Local        Local
}

// Want is one thing a repository asks for locally: a dependency to fill, or a
// companion to start beside it.
//
// One per *entry*, not per repo class. Three roles filled by one class are three
// wants and three instances -- hmd-inf-credentials does exactly that three times
// in hmd-inf-trino -- while the lock groups them back into one pinned version.
type Want struct {
	// Key is what --name addresses this entry by: the dependency role, or the
	// companion's declared instance_name.
	Key           string
	RepoClassName string
	VersionSpec   string
	// DefaultName is the lower half of the naming order: the block's own
	// instance_name if it has one, else the Key. Never derived from the repo
	// class, which would collide the moment one class fills two roles.
	//
	// The upper half -- a binding an environment manifest already holds, and a
	// --name override -- belongs to whoever writes a manifest, because this
	// package never sees one.
	DefaultName string
	// Profiles gating this entry. Empty means unconditional.
	Profiles []string
	// Satisfies is the dependency roles this entry fills, and is empty for a
	// companion. That is what lets a reader tell a real graph edge from a test
	// fixture, and it is a list because one class can fill several roles.
	Satisfies []string
	Required  bool
	// Bind is the instance the environment already provides for this role, from
	// the gate's `bind`. A bound want is neither declared nor pinned: the role
	// is bound to that name and the lock has nothing to say about it.
	Bind                  string
	Dependencies          map[string]any
	InstanceConfiguration map[string]any
	// Resource, External and Suggest are the dependency's resource type and
	// the gate's external flag and suggestion, for a dependency want; empty
	// for a companion.
	Resource string
	External bool
	Suggest  string
}

// Load reads repoDir/meta-data/manifest.json.
//
// Every problem is reported at once rather than failing on the first, so an
// author fixing a hand-edited section sees the whole list -- manifest.Validate's
// rule, for the same reason.
func Load(repoDir string) (*Manifest, error) {
	path := filepath.Join(repoDir, FileName)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	m, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	m.Path = path
	return m, nil
}

// Parse builds a Manifest from raw BACON JSON.
func Parse(data []byte) (*Manifest, error) {
	var doc struct {
		Name   string `json:"name"`
		Deploy struct {
			Dependencies map[string]struct {
				RepoClassName string `json:"repo_class_name"`
				VersionSpec   string `json:"version_spec"`
				InstanceName  string `json:"instance_name"`
				Required      any    `json:"required"`
				Resource      *struct {
					Namespace string `json:"resource_namespace"`
					Name      string `json:"resource_definition_name"`
				} `json:"resource"`
			} `json:"dependencies"`
		} `json:"deploy"`
		Local *struct {
			Version         int      `json:"version"`
			Stack           bool     `json:"stack"`
			DefaultProfiles []string `json:"default_profiles"`
			Repos           []struct {
				InstanceName          string         `json:"instance_name"`
				RepoClassName         string         `json:"repo_class_name"`
				VersionSpec           string         `json:"version_spec"`
				Profiles              []string       `json:"profiles"`
				Dependencies          map[string]any `json:"dependencies"`
				InstanceConfiguration map[string]any `json:"instance_configuration"`
			} `json:"repos"`
			Dependencies map[string]struct {
				Profiles              []string       `json:"profiles"`
				Bind                  string         `json:"bind"`
				Dependencies          map[string]any `json:"dependencies"`
				InstanceConfiguration map[string]any `json:"instance_configuration"`
				Suggest               string         `json:"suggest"`
				External              any            `json:"external"`
			} `json:"dependencies"`
		} `json:"local"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parsing the manifest: %w", err)
	}

	m := &Manifest{RepoClassName: doc.Name}
	var problems []string

	roles := make([]string, 0, len(doc.Deploy.Dependencies))
	for role := range doc.Deploy.Dependencies {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	isRole := map[string]bool{}
	for _, role := range roles {
		block := doc.Deploy.Dependencies[role]
		isRole[role] = true
		if block.RepoClassName == "" {
			problems = append(problems, fmt.Sprintf(
				"deploy.dependencies.%s: 'repo_class_name' is required", role))
			continue
		}
		required, err := truthy(block.Required)
		if err != nil {
			problems = append(problems, fmt.Sprintf("deploy.dependencies.%s: %v", role, err))
			continue
		}
		d := Dependency{
			Role:          role,
			RepoClassName: block.RepoClassName,
			VersionSpec:   strings.TrimSpace(block.VersionSpec),
			InstanceName:  block.InstanceName,
			Required:      required,
		}
		if block.Resource != nil && block.Resource.Namespace != "" && block.Resource.Name != "" {
			d.Resource = block.Resource.Namespace + "/" + block.Resource.Name
		}
		m.Dependencies = append(m.Dependencies, d)
	}

	if doc.Local != nil {
		l := doc.Local
		switch l.Version {
		case 0, Version:
			m.Local.Version = Version
		default:
			problems = append(problems, fmt.Sprintf(
				"local: schema version %d is not supported; this nsctl understands %d", l.Version, Version))
		}
		m.Local.DefaultProfiles = l.DefaultProfiles
		m.Local.Stack = l.Stack

		seen := map[string]bool{}
		for i, r := range l.Repos {
			where := fmt.Sprintf("local.repos[%d]", i)
			switch {
			case r.InstanceName == "":
				problems = append(problems, where+": 'instance_name' is required")
			case seen[r.InstanceName]:
				problems = append(problems, fmt.Sprintf(
					"%s: duplicate instance_name %q", where, r.InstanceName))
			case isRole[r.InstanceName]:
				// Both would default to the same instance name and one would
				// silently win -- and which one depends on iteration order.
				problems = append(problems, fmt.Sprintf(
					"%s: %q is also a dependency role, so the two would claim one instance;"+
						" rename the companion", where, r.InstanceName))
			default:
				seen[r.InstanceName] = true
			}
			if r.RepoClassName == "" {
				problems = append(problems, where+": 'repo_class_name' is required")
				continue
			}
			m.Local.Repos = append(m.Local.Repos, Repo{
				InstanceName:          r.InstanceName,
				RepoClassName:         r.RepoClassName,
				VersionSpec:           strings.TrimSpace(r.VersionSpec),
				Profiles:              r.Profiles,
				Dependencies:          r.Dependencies,
				InstanceConfiguration: r.InstanceConfiguration,
			})
		}

		if len(l.Dependencies) > 0 {
			m.Local.Dependencies = map[string]Gate{}
		}
		gated := make([]string, 0, len(l.Dependencies))
		for role := range l.Dependencies {
			gated = append(gated, role)
		}
		sort.Strings(gated)
		for _, role := range gated {
			dep, known := find(m.Dependencies, role)
			g := l.Dependencies[role]
			g.Bind = strings.TrimSpace(g.Bind)
			switch {
			case !known && isRole[role]:
				// The block existed but failed its own validation above; the
				// problem is already reported and repeating it helps nobody.
				continue
			case !known:
				problems = append(problems, fmt.Sprintf(
					"local.dependencies.%s: no dependency named %q is declared under deploy.dependencies", role, role))
				continue
			case dep.Required && len(g.Profiles) > 0:
				// ms-deployment fails the whole ChangeSet on an unmet required
				// role, and a SKIPPED status does not mock one -- so gating this
				// would produce a failure three layers from its cause.
				problems = append(problems, fmt.Sprintf(
					"local.dependencies.%s: %q is required, and a required dependency cannot be"+
						" profile-gated -- ms-deployment fails the whole ChangeSet on an unmet"+
						" required role", role, role))
				continue
			case g.Bind != "" && (len(g.Dependencies) > 0 || len(g.InstanceConfiguration) > 0):
				// Nothing is declared for a bound role, so there is nothing for
				// either of these to configure; one of them would silently lose.
				problems = append(problems, fmt.Sprintf(
					"local.dependencies.%s: 'bind' names an instance the environment already provides,"+
						" so nothing is declared for it -- drop 'dependencies' and 'instance_configuration',"+
						" or drop 'bind'", role))
				continue
			case dep.Required && g.Bind == "" && len(g.Dependencies) == 0 && len(g.InstanceConfiguration) == 0 && strings.TrimSpace(g.Suggest) == "" && g.External == nil:
				problems = append(problems, fmt.Sprintf(
					"local.dependencies.%s: %q is required, so the entry must say something --"+
						" 'bind', 'dependencies' or 'instance_configuration'", role, role))
				continue
			}
			external, err := truthy(g.External)
			if err != nil {
				problems = append(problems, fmt.Sprintf("local.dependencies.%s: external: %v", role, err))
				continue
			}
			if external && g.Bind != "" {
				problems = append(problems, fmt.Sprintf("local.dependencies.%s: 'external' and 'bind' both say the environment fills it; keep one", role))
				continue
			}
			m.Local.Dependencies[role] = Gate{
				External:              external,
				Suggest:               strings.TrimSpace(g.Suggest),
				Profiles:              g.Profiles,
				Bind:                  g.Bind,
				Dependencies:          g.Dependencies,
				InstanceConfiguration: g.InstanceConfiguration,
			}
		}
	}

	if len(problems) > 0 {
		return nil, fmt.Errorf("the local declaration is not valid:\n  - %s",
			strings.Join(problems, "\n  - "))
	}
	return m, nil
}

// Activate returns the wants a set of activated profiles selects.
//
// The semantics are docker compose's, deliberately, because they are one concept
// instead of two and are already in the fingers of everyone who will read a
// manifest:
//
//   - an entry carrying a `profiles` key starts only when at least one of those
//     profiles is activated;
//   - an entry with no `profiles` key always starts;
//   - therefore **lean requires no declaration at all** -- it is what activating
//     nothing gives you, and no author has to remember to define it.
//
// A required dependency always activates: gating one is refused at Load. An
// optional dependency activates only when local.dependencies gates it with an
// active profile -- one the section does not mention keeps exactly today's
// behaviour, which is to stay undeclared. Nothing in a manifest distinguishes
// "optional in the cloud" from "optional for this process to boot", so the safe
// default is that nsctl changes nothing it was not told to change.
//
// Whatever else the gate says -- a binding, or the dependency instance's own
// dependencies and configuration -- rides on the want, so whoever declares it
// declares it whole.
func (m *Manifest) Activate(profiles []string) []Want {
	active := map[string]bool{}
	for _, p := range profiles {
		active[p] = true
	}
	anyOf := func(gate []string) bool {
		for _, p := range gate {
			if active[p] {
				return true
			}
		}
		return false
	}

	var wants []Want
	for _, d := range m.Dependencies {
		gate, gated := m.Local.Dependencies[d.Role]
		switch {
		case d.Required:
		case !gated:
			continue
		case !anyOf(gate.Profiles):
			continue
		}
		name := d.InstanceName
		if name == "" {
			name = d.Role
		}
		w := Want{
			Key:           d.Role,
			RepoClassName: d.RepoClassName,
			VersionSpec:   d.VersionSpec,
			DefaultName:   name,
			Satisfies:     []string{d.Role},
			Required:      d.Required,
			Resource:      d.Resource,
		}
		if gated {
			w.Profiles = gate.Profiles
			w.Bind = gate.Bind
			w.Suggest = gate.Suggest
			w.External = gate.External
			w.Dependencies = gate.Dependencies
			w.InstanceConfiguration = gate.InstanceConfiguration
		}
		wants = append(wants, w)
	}
	for _, r := range m.Local.Repos {
		if len(r.Profiles) > 0 && !anyOf(r.Profiles) {
			continue
		}
		wants = append(wants, Want{
			Key:                   r.InstanceName,
			RepoClassName:         r.RepoClassName,
			VersionSpec:           r.VersionSpec,
			DefaultName:           r.InstanceName,
			Profiles:              r.Profiles,
			Dependencies:          r.Dependencies,
			InstanceConfiguration: r.InstanceConfiguration,
		})
	}
	return wants
}

// Wants is every declared want, whatever profile it is gated by.
//
// This is what a lock pins: activation is a read-time filter, and a lock that
// covered only the profile in use when it was generated would force a re-resolve
// -- and therefore a network trip, and therefore a different answer -- the first
// time anybody switched profiles.
func (m *Manifest) Wants() []Want { return m.Activate(m.AllProfiles()) }

// AllProfiles is every profile the declaration mentions, sorted. What
// --all-profiles activates.
func (m *Manifest) AllProfiles() []string {
	seen := map[string]bool{}
	for _, r := range m.Local.Repos {
		for _, p := range r.Profiles {
			seen[p] = true
		}
	}
	for _, g := range m.Local.Dependencies {
		for _, p := range g.Profiles {
			seen[p] = true
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Profiles resolves which profiles to activate: the ones asked for, else what
// the section defaults to.
func (m *Manifest) Profiles(asked []string, lean bool) []string {
	switch {
	case lean:
		return nil
	case len(asked) > 0:
		return asked
	default:
		return m.Local.DefaultProfiles
	}
}

func find(deps []Dependency, role string) (Dependency, bool) {
	for _, d := range deps {
		if d.Role == role {
			return d, true
		}
	}
	return Dependency{}, false
}

// truthy reads BACON's `required`.
//
// One implementation, in internal/repoclass, which owns deploy.* -- this
// package owns local.*. Two readings of `required` that disagreed would be a
// defect nothing could observe until a deploy failed for a role one of them
// thought optional.
func truthy(v any) (bool, error) { return repoclass.Truthy(v) }
