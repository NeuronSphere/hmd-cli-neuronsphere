package cmd

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/envtemplate"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/localspec"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/lock"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/repoclass"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/stack"
)

// sessionShape is what an environment is composed from (NERD035 SPEC004): a
// template and the repositories being edited. shared carries the planner
// flags -- --profile, --all-profiles, --lean, --name -- which apply to every
// repository; its path is unused.
type sessionShape struct {
	template string
	repos    []string
	shared   *fromRepo
}

// bind registers --template and --repo. The planner flags come from shared's
// own bind, which the verb calls for --from-repo anyway.
func (s *sessionShape) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&s.template, "template", "",
		"Start from this template (see `nsctl template list`)")
	cmd.Flags().StringArrayVar(&s.repos, "repo", nil,
		"A repository being edited, deployed from its working tree with what its lock pins. Repeatable")
}

func (s *sessionShape) requested() bool { return s.template != "" || len(s.repos) > 0 }

// composition is a composed manifest and how it came about.
type composition struct {
	Manifest *manifest.Manifest
	Plans    []*repoPlan
	// Notes are things worth saying that are not errors.
	Notes []string
}

// missingArtifacts is every planned instance whose bytes are not cached.
func (c *composition) missingArtifacts(home string) []plannedInstance {
	var out []plannedInstance
	for _, p := range c.Plans {
		out = append(out, p.missingArtifacts(home)...)
	}
	return out
}

// composeSession builds the manifest for environment slug: the template, then
// each repository planned onto it in turn (NERD035 SPEC004).
//
// Each repository is planned the way `env add --from-repo` plans one, against
// the manifest as composed so far, with NERD017 SPEC010's reuse: a companion
// or dependency role the composition already fills -- the same instance name
// and class, or a producer of the role's resource type -- is bound rather
// than declared again, so two repositories needing Postgres get one Postgres.
//
// The repository being edited wins over anything else of its class: when the
// composition already declares exactly one instance of it, from the template
// or as another repository's companion, the working tree takes that
// instance's place and name, so whatever depended on it still resolves.
//
// An instance name the composition already uses for a different class is an
// error naming both, never a silent replacement.
func composeSession(opts *Options, home, slug string, s *sessionShape) (*composition, error) {
	m := &manifest.Manifest{Version: manifest.Version}
	if s.template != "" {
		t, err := envtemplate.Load(home, s.template, opts.Lookup)
		if err != nil {
			return nil, templateError(err)
		}
		m = t
	}
	m.Name, m.Path, m.Template, m.Scope = slug, manifest.DefaultPath(home, slug), s.template, manifest.ScopeEnvironment
	// Bindings and profiles record one repository's plan for re-planning it.
	// A composed environment is recomposed, never re-planned, and a template's
	// would be read as each repository's own.
	m.Bindings, m.Profiles = nil, nil

	shared := s.shared
	if shared == nil {
		shared = &fromRepo{}
	}
	overrides, err := parseNames(shared.names)
	if err != nil {
		return nil, err
	}

	c := &composition{Manifest: m}
	subjects := map[string]string{} // class -> instance
	pathOf := map[string]string{}   // class -> repository path
	used := map[string]bool{}
	for _, path := range s.repos {
		dir, err := filepath.Abs(path)
		if err != nil {
			return nil, nserr.Wrap(nserr.Usage, err)
		}
		spec, err := localspec.Load(dir)
		if err != nil {
			return nil, nserr.Wrap(nserr.Usage, err)
		}
		class := spec.RepoClassName
		if other, dup := pathOf[class]; dup {
			return nil, nserr.New(nserr.Usage,
				"--repo %s and --repo %s are both %s; an environment deploys one working tree of a class", other, dir, class)
		}
		pathOf[class] = dir

		f := *shared
		f.path = dir
		f.recorded = &recordedPlan{}
		f.providers = environmentProviders(opts, home, m)
		f.compose = composeOnto(opts, home, m, dir)
		if _, named := overrides[class]; !named {
			if replaced := replaceable(m, class); len(replaced) == 1 {
				f.names = append(append([]string(nil), shared.names...), class+"="+replaced[0])
			}
		}

		plan, err := planFromRepo(&f, m)
		if err != nil {
			return nil, err
		}
		if err := checkClassConflicts(m, plan, dir); err != nil {
			return nil, err
		}
		var discard map[string]string
		plan.applyInto(m, false, &discard)
		for key := range plan.Bindings {
			used[key] = true
		}
		if plan.DeclareSubject {
			subjects[class] = plan.Subject.InstanceName
		}
		c.Plans = append(c.Plans, plan)
	}

	var unused []string
	for key := range overrides {
		if !used[key] {
			unused = append(unused, key)
		}
	}
	if len(unused) > 0 {
		sort.Strings(unused)
		return nil, nserr.New(nserr.Usage,
			"--name names nothing any of these repositories declares: %s", strings.Join(unused, ", "))
	}

	c.Notes = append(c.Notes, replaceWithWorkingTrees(m, subjects)...)

	if problems := m.Validate(opts.Lookup); len(problems) > 0 {
		return nil, nserr.New(nserr.Usage, "the composed environment is not valid:\n  - %s",
			strings.Join(problems, "\n  - "))
	}
	return c, nil
}

// replaceable is the instances of class that a working tree of it may take
// over: everything not itself declared from a working tree.
func replaceable(m *manifest.Manifest, class string) []string {
	var out []string
	for _, r := range m.Repos {
		if r.RepoClassName == class && !(r.Source != nil && r.Source.Type == manifest.SourceLocal) {
			out = append(out, r.InstanceName)
		}
	}
	return out
}

// checkClassConflicts refuses a plan that would declare an instance under a
// name the composition already gives a different class. applyInto would
// replace it without a word.
func checkClassConflicts(m *manifest.Manifest, plan *repoPlan, dir string) error {
	declare := make([]manifest.Repo, 0, len(plan.Instances)+1)
	for _, in := range plan.Instances {
		if !in.Substrate {
			declare = append(declare, in.Repo)
		}
	}
	if plan.DeclareSubject {
		declare = append(declare, plan.Subject)
	}
	var lines []string
	for _, r := range declare {
		if have, ok := m.Repo(r.InstanceName); ok && have.RepoClassName != r.RepoClassName {
			lines = append(lines, fmt.Sprintf("%q is already declared as %s, and %s would declare it as %s",
				r.InstanceName, have.RepoClassName, dir, r.RepoClassName))
		}
	}
	if len(lines) == 0 {
		return nil
	}
	return nserr.New(nserr.Usage, "instance names collide:\n  - %s\nRename one with --name <role-or-declared-name>=<instance>",
		strings.Join(lines, "\n  - "))
}

// replaceWithWorkingTrees is the edited-repository-wins rule for what the
// planning order could not settle: a repository planned *before* another
// whose companion is its class. The one other instance of the class is
// undeclared, and every dependency on it, and every stack's binding to it,
// moves to the working tree. Two or more are left alone and reported: which
// one the tree should replace is not something to guess.
func replaceWithWorkingTrees(m *manifest.Manifest, subjects map[string]string) []string {
	var notes []string
	renames := map[string]string{}
	classes := make([]string, 0, len(subjects))
	for class := range subjects {
		classes = append(classes, class)
	}
	sort.Strings(classes)
	for _, class := range classes {
		subject := subjects[class]
		var others []string
		for _, name := range replaceable(m, class) {
			if name != subject {
				others = append(others, name)
			}
		}
		switch len(others) {
		case 0:
		case 1:
			renames[others[0]] = subject
		default:
			notes = append(notes, fmt.Sprintf("%s is deployed from its working tree as %s, and %s also declare it from artifacts;"+
				" they are left as they are", class, subject, strings.Join(others, ", ")))
		}
	}
	if len(renames) == 0 {
		return notes
	}
	kept := m.Repos[:0]
	for _, r := range m.Repos {
		if _, gone := renames[r.InstanceName]; gone {
			continue
		}
		r.Dependencies = rewriteTargets(r.Dependencies, renames)
		kept = append(kept, r)
	}
	m.Repos = kept
	for i := range m.Stacks {
		rec := &m.Stacks[i]
		for key, instance := range rec.Bindings {
			if to, ok := renames[instance]; ok {
				rec.Bindings[key] = to
			}
		}
		declared := rec.Declared[:0]
		for _, name := range rec.Declared {
			if _, gone := renames[name]; !gone {
				declared = append(declared, name)
			}
		}
		rec.Declared = declared
	}
	return notes
}

// composeOnto is NERD017 SPEC010's reuse for a repository composed into a
// session: what the composition already fills is bound. Unlike a stack's, a
// role nothing fills is not an error here -- the planner's own rules for
// external and resource-only roles apply, exactly as for `env add
// --from-repo`.
func composeOnto(opts *Options, home string, m *manifest.Manifest, dir string) func(
	*localspec.Manifest, []localspec.Want, *lock.Lock, map[string]string) (map[string]string, error) {

	return func(_ *localspec.Manifest, wants []localspec.Want, l *lock.Lock, overrides map[string]string) (map[string]string, error) {
		resolver := repoclass.NewWithHome(opts.Lookup("HMD_REPO_HOME"), home, opts.Lookup)
		repoclass.Seed(resolver, m.Repos)
		providers := stack.IndexProviders(m, resolver)
		pinned := func(class string) bool { _, ok := l.Entry(class); return ok }
		binds, _, conflicts := stack.Compose(wants, providers, map[string]bool{}, pinned, overrides)
		if len(conflicts) > 0 {
			return nil, nserr.New(nserr.Usage, "%s's instance names collide with the composition's:\n  - %s",
				dir, strings.Join(conflicts, "\n  - "))
		}
		return binds, nil
	}
}
