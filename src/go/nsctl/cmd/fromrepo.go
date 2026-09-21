package cmd

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/artifact"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/librarian"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/localspec"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/lock"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// fromRepo holds the flags `env add` and `env apply` share when they build an
// environment out of a repository.
type fromRepo struct {
	path        string
	profiles    []string
	allProfiles bool
	lean        bool
	names       []string

	// subject, when set, is how the repository itself is declared instead of
	// as its working tree: a stack is deployed from its own artifact (NERD017
	// SPEC003). subjectVersion goes with it.
	subject        *manifest.Source
	subjectVersion string
	// recorded, when set, is the prior bindings and profiles to honour in place
	// of the environment manifest's own. A stack keeps its record apart from
	// the manifest's --from-repo bindings so two can share one environment
	// (NERD017 SPEC008 and SPEC009).
	recorded *recordedPlan
	// compose, when set, decides which activated wants the environment
	// already fills (NERD017 SPEC010). It returns want key -> existing
	// instance; each such want is bound rather than declared, and every
	// dependency naming the want's default name is rewritten to the bound
	// instance.
	compose func(spec *localspec.Manifest, wants []localspec.Want, l *lock.Lock, overrides map[string]string) (map[string]string, error)
}

// recordedPlan is what a previous run of the same planner recorded.
type recordedPlan struct {
	Bindings map[string]string
	Profiles []string
}

// bind registers the flags common to both verbs. Each verb adds its own
// pull-shaped flag, because their defaults are opposite and that asymmetry is
// the substance of NERD010 SPEC006.
func (f *fromRepo) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.path, "from-repo", "",
		"Build the environment from the repository at this path")
	cmd.Flags().StringSliceVar(&f.profiles, "profile", nil,
		"Local profiles to activate. Repeatable, or comma-separated")
	cmd.Flags().BoolVar(&f.allProfiles, "all-profiles", false,
		"Activate every profile the lock mentions")
	cmd.Flags().BoolVar(&f.lean, "lean", false,
		"Activate no profiles: the repository and its unconditional entries alone")
	cmd.Flags().StringArrayVar(&f.names, "name", nil,
		"Name one instance, as <role-or-declared-name>=<instance>. Repeatable")
}

// requested reports whether the user asked for any of this.
func (f *fromRepo) requested() bool { return f.path != "" }

// plannedInstance is one instance a repository's declaration asks for.
type plannedInstance struct {
	Repo manifest.Repo
	Want localspec.Want
	// Substrate marks a want the environment substrate already provides, which
	// is declared by nobody and still bound to by the repository.
	Substrate bool
}

// repoPlan is what a repository's manifest and lock say an environment holds.
type repoPlan struct {
	Spec      *localspec.Manifest
	Lock      *lock.Lock
	Profiles  []string
	Instances []plannedInstance
	// Bindings maps every want's key to the instance name it was given, which is
	// what the environment manifest records so a re-apply resolves the same way.
	Bindings map[string]string
	// Subject is the repository itself, always deployed from its working tree:
	// that is what makes it the thing under test.
	Subject manifest.Repo
	// Renamed names the instances an override moved away from, which are left
	// deployed rather than torn down.
	Renamed []string
	// Composed is what the environment already provided for this plan's
	// wants (NERD017 SPEC010), for the report.
	Composed map[string]string
}

// planFromRepo reads a repository's declaration and lock and works out what the
// environment should hold.
//
// existing is the environment manifest as it stands, or nil on a first start. It
// is the top tier of the naming order: a binding already recorded there wins over
// every default, which is what stops a re-apply duplicating an instance somebody
// renamed.
func planFromRepo(f *fromRepo, existing *manifest.Manifest) (*repoPlan, error) {
	repoDir, err := filepath.Abs(f.path)
	if err != nil {
		return nil, nserr.Wrap(nserr.Usage, err)
	}
	spec, err := localspec.Load(repoDir)
	if err != nil {
		return nil, nserr.Wrap(nserr.Usage, err)
	}
	l, err := lock.Read(repoDir)
	if err != nil {
		// Never a fall back to resolving on the fly: the whole point of the lock
		// is that a fresh clone gets the same answer as the machine that wrote
		// it, and an on-the-fly resolve is exactly the different answer.
		return nil, nserr.Wrap(nserr.Usage, withLockRemedy(err))
	}
	if f.allProfiles && f.lean {
		return nil, nserr.New(nserr.Usage, "--all-profiles and --lean ask for opposite things")
	}
	overrides, err := parseNames(f.names)
	if err != nil {
		return nil, err
	}

	var record *recordedPlan
	switch {
	case f.recorded != nil:
		record = f.recorded
	case existing != nil:
		record = &recordedPlan{Bindings: existing.Bindings, Profiles: existing.Profiles}
	}
	var bound map[string]string
	if record != nil {
		bound = record.Bindings
	}
	profiles := activeProfiles(f, spec, l, record)

	plan := &repoPlan{Spec: spec, Lock: l, Profiles: profiles, Bindings: map[string]string{}}
	taken := map[string]bool{}
	roles := map[string]string{}

	wants := spec.Activate(profiles)
	renames := map[string]string{}
	if f.compose != nil {
		binds, err := f.compose(spec, wants, l, overrides)
		if err != nil {
			return nil, err
		}
		for i := range wants {
			if b, ok := binds[wants[i].Key]; ok && wants[i].Bind == "" {
				wants[i].Bind = b
				if wants[i].DefaultName != b {
					renames[wants[i].DefaultName] = b
				}
			}
		}
		plan.Composed = binds
	}
	for i := range wants {
		wants[i].Dependencies = rewriteTargets(wants[i].Dependencies, renames)
	}

	for _, w := range wants {
		if w.Bind != "" {
			// The role is filled by something the environment already provides,
			// so there is nothing to declare, nothing to pin, and nothing to
			// name: a --name for it would rename an instance nsctl does not own.
			if hasOverride(w.Key, overrides) {
				return nil, nserr.New(nserr.Usage,
					"--name %s=%s: %s declares %s bound to %q, which the environment provides;"+
						" a bound role cannot be renamed", w.Key, overrides[w.Key], spec.Path, w.Key, w.Bind)
			}
			plan.Bindings[w.Key] = w.Bind
			for _, role := range w.Satisfies {
				roles[role] = w.Bind
			}
			plan.Instances = append(plan.Instances, plannedInstance{Want: w, Substrate: true,
				Repo: manifest.Repo{InstanceName: w.Bind, RepoClassName: w.RepoClassName}})
			continue
		}
		entry, pinned := l.Entry(w.RepoClassName)
		if !pinned {
			return nil, nserr.New(nserr.Usage,
				"%s pins no version for %s, which %s declares.\nRegenerate it with `nsctl lock`",
				lock.FileName, w.RepoClassName, spec.Path)
		}
		name := resolveInstanceName(w, overrides, bound)
		if previous, ok := bound[w.Key]; ok && previous != name {
			// A rename adds the new instance and leaves the old one deployed.
			// Nothing tears an instance down on the user's behalf -- the promise
			// `nsctl repo remove` already makes -- and a rename of a deployed
			// instance is a teardown in ms-deployment's terms, not a relabel.
			plan.Renamed = append(plan.Renamed, previous)
		}
		plan.Bindings[w.Key] = name
		for _, role := range w.Satisfies {
			roles[role] = name
		}

		if manifest.ScopeEnvironment.Reserved(name) {
			// The substrate deploys this whether or not a manifest names it, and
			// Validate refuses a declaration that claims the name. The repository
			// still binds to it.
			plan.Instances = append(plan.Instances, plannedInstance{Want: w, Substrate: true,
				Repo: manifest.Repo{InstanceName: name, RepoClassName: w.RepoClassName}})
			continue
		}
		if taken[name] {
			return nil, nserr.New(nserr.Usage,
				"two of this repository's declarations would both be called %q."+
					" Tell them apart with --name <role-or-declared-name>=<instance>", name)
		}
		taken[name] = true
		plan.Instances = append(plan.Instances, plannedInstance{
			Want: w,
			Repo: manifest.Repo{
				InstanceName:          name,
				RepoClassName:         w.RepoClassName,
				Version:               entry.Version,
				Source:                &manifest.Source{Type: manifest.SourceArtifact},
				InstanceConfiguration: w.InstanceConfiguration,
				Dependencies:          w.Dependencies,
			},
		})
	}

	// The repository itself, from its working tree. NERD005 SPEC002's precedence
	// is what lets a developer of any *companion* do the same for theirs.
	subject := resolveSubjectName(spec.RepoClassName, overrides, bound)
	if previous, ok := bound[spec.RepoClassName]; ok && previous != subject {
		plan.Renamed = append(plan.Renamed, previous)
	}
	plan.Bindings[spec.RepoClassName] = subject
	source := &manifest.Source{Type: manifest.SourceLocal, Path: repoDir}
	version := ""
	if f.subject != nil {
		source, version = f.subject, f.subjectVersion
	}
	plan.Subject = manifest.Repo{
		InstanceName:  subject,
		RepoClassName: spec.RepoClassName,
		Version:       version,
		Source:        source,
	}
	if len(roles) > 0 {
		deps := make(map[string]any, len(roles))
		for role, instance := range roles {
			deps[role] = instance
		}
		plan.Subject.Dependencies = deps
	}
	return plan, nil
}

// activeProfiles settles which profiles are on: what was asked for, then what
// the environment already recorded, then what the repository defaults to.
//
// The middle tier keys off **bindings, not profiles**, and that is the whole
// reason it is written this way. A lean environment records *no* profiles, so
// testing the recorded profile list for emptiness cannot tell "created lean"
// from "never created from a repository" -- and getting that wrong means a bare
// `nsctl env apply` silently re-expands a lean environment to the repository's
// default_profiles, which is exactly the delta-apply SPEC005 step 5 exists to
// prevent: correct according to a file nobody re-read.
//
// Bindings answer the question the profile list cannot, because a --from-repo
// run always binds at least the repository itself.
func activeProfiles(f *fromRepo, spec *localspec.Manifest, l *lock.Lock,
	record *recordedPlan) []string {

	switch {
	case f.lean:
		return nil
	case f.allProfiles:
		return l.AllProfiles()
	case len(f.profiles) > 0:
		return f.profiles
	case record != nil && len(record.Bindings) > 0:
		// Possibly empty, and that is the point: it means lean.
		return record.Profiles
	default:
		return spec.Local.DefaultProfiles
	}
}

// resolveInstanceName is the naming order, most specific first.
//
//  1. What the environment manifest already binds. An instance somebody renamed
//     stays renamed, and is never duplicated under its default name.
//  2. A --name override.
//  3. The declaration's own instance_name.
//  4. The dependency role, or the companion's declared name.
//
// Never derived from the repo class: one class often fills several roles --
// hmd-inf-credentials fills three in hmd-inf-trino -- and a class-derived default
// would collide all of them onto one instance.
func resolveInstanceName(w localspec.Want, overrides, bound map[string]string) string {
	if name, ok := bound[w.Key]; ok && !hasOverride(w.Key, overrides) {
		return name
	}
	if name, ok := overrides[w.Key]; ok {
		return name
	}
	return w.DefaultName
}

// resolveSubjectName names the repository under test. Its key is its own repo
// class, so `--name hmd-ms-myapi=myapi` renames it like anything else.
func resolveSubjectName(repoClass string, overrides, bound map[string]string) string {
	if name, ok := bound[repoClass]; ok && !hasOverride(repoClass, overrides) {
		return name
	}
	if name, ok := overrides[repoClass]; ok {
		return name
	}
	return defaultInstanceName(repoClass)
}

// rewriteTargets replaces dependency targets that name a renamed want.
func rewriteTargets(deps map[string]any, renames map[string]string) map[string]any {
	if len(deps) == 0 || len(renames) == 0 {
		return deps
	}
	out := make(map[string]any, len(deps))
	for role, target := range deps {
		switch v := target.(type) {
		case string:
			if r, ok := renames[v]; ok {
				target = r
			}
		case []any:
			list := make([]any, len(v))
			for i, item := range v {
				if str, ok := item.(string); ok {
					if r, ok := renames[str]; ok {
						list[i] = r
						continue
					}
				}
				list[i] = item
			}
			target = list
		}
		out[role] = target
	}
	return out
}

func hasOverride(key string, overrides map[string]string) bool {
	_, ok := overrides[key]
	return ok
}

// parseNames reads --name, which addresses a want by its key: a dependency role,
// a companion's declared instance_name, or the repository's own repo class.
func parseNames(pairs []string) (map[string]string, error) {
	out := map[string]string{}
	for _, pair := range pairs {
		key, value, err := splitPair(pair, "--name")
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(value) == "" {
			return nil, nserr.New(nserr.Usage, "--name %q: an instance name is required", pair)
		}
		if manifest.ScopeEnvironment.Reserved(value) {
			return nil, nserr.New(nserr.Usage,
				"--name %q: %q is a name the environment substrate creates, so nothing may be"+
					" declared under it", pair, value)
		}
		out[key] = value
	}
	return out, nil
}

// checkNamesAreUsed refuses a --name that addressed nothing.
//
// Silently ignoring one is the worst outcome available: the user believes they
// renamed an instance, the manifest says otherwise, and the two only disagree
// visibly several minutes into a deploy.
func checkNamesAreUsed(f *fromRepo, plan *repoPlan) error {
	overrides, err := parseNames(f.names)
	if err != nil {
		return err
	}
	var unused []string
	for key := range overrides {
		if _, ok := plan.Bindings[key]; !ok {
			unused = append(unused, key)
		}
	}
	if len(unused) == 0 {
		return nil
	}
	sort.Strings(unused)
	known := make([]string, 0, len(plan.Bindings))
	for key := range plan.Bindings {
		known = append(known, key)
	}
	sort.Strings(known)
	return nserr.New(nserr.Usage,
		"--name names nothing this repository declares: %s\nIt addresses a dependency role, a"+
			" companion's declared instance_name, or the repository's own repo class. Active here: %s",
		strings.Join(unused, ", "), strings.Join(known, ", "))
}

// apply writes the plan into an environment manifest, replacing what a previous
// --from-repo run put there and leaving everything else alone.
//
// Two kinds of instance stop being asked for, and they are treated differently
// because the user asked for different things.
//
// A **renamed** one is always undeclared. Naming an instance something else is a
// request about that instance, and leaving both names declared would deploy the
// same thing twice into one environment -- the duplicate the naming order exists
// to prevent.
//
// One dropped by **deactivating a profile** stays declared unless prune. The user
// narrowed which profiles are on; they said nothing about the instances, and
// quietly undeclaring them would make a later apply look like it had lost them.
//
// Neither is ever torn down. These verbs edit a manifest, which is the promise
// `nsctl repo remove` already makes.
func (p *repoPlan) apply(m *manifest.Manifest, prune bool) (added, kept []string) {
	added, kept = p.applyInto(m, prune, &m.Bindings)
	m.Profiles = p.Profiles
	return added, kept
}

// applyInto is apply with the bindings kept somewhere other than the
// manifest's own -- a stack record's -- and the manifest's profiles left
// alone. *bindings holds the previous run's bindings on entry and this run's
// on return.
func (p *repoPlan) applyInto(m *manifest.Manifest, prune bool, bindings *map[string]string) (added, kept []string) {
	owned := map[string]bool{}
	for _, previous := range *bindings {
		owned[previous] = true
	}
	renamed := map[string]bool{}
	for _, previous := range p.Renamed {
		renamed[previous] = true
	}

	declare := make([]manifest.Repo, 0, len(p.Instances)+1)
	for _, in := range p.Instances {
		if !in.Substrate {
			declare = append(declare, in.Repo)
		}
	}
	declare = append(declare, p.Subject)

	wanted := map[string]bool{}
	for _, r := range declare {
		wanted[r.InstanceName] = true
	}

	repos := make([]manifest.Repo, 0, len(m.Repos)+len(declare))
	for _, existing := range m.Repos {
		switch {
		case wanted[existing.InstanceName]:
			// Re-declared below, at whatever the lock now pins.
			continue
		case renamed[existing.InstanceName]:
			continue
		case owned[existing.InstanceName] && prune:
			continue
		case owned[existing.InstanceName]:
			kept = append(kept, existing.InstanceName)
		}
		repos = append(repos, existing)
	}
	for _, r := range declare {
		added = append(added, r.InstanceName)
		repos = append(repos, r)
	}

	m.Repos = repos
	*bindings = p.Bindings
	sort.Strings(kept)
	return added, kept
}

// missingArtifacts is every planned instance whose bytes are not in the cache.
func (p *repoPlan) missingArtifacts(home string) []plannedInstance {
	var missing []plannedInstance
	for _, in := range p.Instances {
		if in.Substrate {
			continue
		}
		if !artifact.Cached(home, in.Repo.RepoClassName, in.Repo.Version) {
			missing = append(missing, in)
		}
	}
	if p.Subject.Source != nil && p.Subject.Source.Type == manifest.SourceArtifact &&
		!artifact.Cached(home, p.Subject.RepoClassName, p.Subject.Version) {
		missing = append(missing, plannedInstance{Repo: p.Subject})
	}
	return missing
}

// spec is the librarian address of a planned instance.
func (in plannedInstance) spec() librarian.Spec {
	return librarian.Spec{
		Name:     in.Repo.RepoClassName,
		Version:  in.Repo.Version,
		ItemType: in.Repo.ArtifactType(),
	}
}

// pullMissing fetches what the cache does not hold, printing each fetch on its
// own line so a long first start reads as progress rather than a hang.
func pullMissing(cmd *cobra.Command, opts *Options, libs *librarians, home string,
	missing []plannedInstance) error {

	if len(missing) == 0 {
		return nil
	}
	cloud, err := libs.cloud(cmd, opts)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Fetching %d artifact(s) from %s\n", len(missing), cloud.BaseURL)
	for _, in := range missing {
		spec := in.spec()
		fmt.Fprintf(out, "  %s\n", spec)
		data, err := cloud.Fetch(cmd.Context(), spec.ContentPath())
		if err != nil {
			return nserr.Wrap(nserr.Fail, err)
		}
		if err := storeLocally(cmd, libs.local(), home, spec, data); err != nil {
			return err
		}
	}
	return nil
}

// uncachedError reports planned instances nothing has fetched, in NERD005
// SPEC007's shape and naming the command that fixes it.
func uncachedError(opts *Options, home string, missing []plannedInstance, pullFlag string) error {
	reports := make([]string, 0, len(missing))
	for _, in := range missing {
		spec := in.spec()
		reports = append(reports, artifact.Unavailable(
			home, in.Repo.RepoClassName, in.Repo.Version, spec.ContentPath(),
			opts.Lookup("HMD_REPO_HOME"), nil).Error()+
			"\nFetch it with:  nsctl artifact pull "+spec.String())
	}
	return nserr.New(nserr.Usage, "%s\n\nOr fetch them all with %s.",
		strings.Join(reports, "\n\n"), pullFlag)
}

// render prints what the plan declared, in the order a reader wants it.
func (p *repoPlan) render(cmd *cobra.Command, slug string) {
	out := cmd.OutOrStdout()
	if len(p.Profiles) == 0 {
		fmt.Fprintf(out, "Profiles: none (lean)\n")
	} else {
		fmt.Fprintf(out, "Profiles: %s\n", strings.Join(p.Profiles, ", "))
	}
	from := "working tree " + p.Subject.Source.Path
	if p.Subject.Source.Type == manifest.SourceArtifact {
		from = "artifact " + p.Subject.Version
	}
	fmt.Fprintf(out, "  %-28s %-34s %s\n", p.Subject.InstanceName, p.Subject.RepoClassName, from)
	for _, in := range p.Instances {
		switch {
		case in.Want.Bind != "" && p.Composed[in.Want.Key] != "":
			fmt.Fprintf(out, "  %-28s %-34s shared: already in the environment\n",
				in.Repo.InstanceName, in.Repo.RepoClassName)
		case in.Want.Bind != "":
			fmt.Fprintf(out, "  %-28s %-34s bound: provided by the environment\n",
				in.Repo.InstanceName, in.Repo.RepoClassName)
		case in.Substrate:
			fmt.Fprintf(out, "  %-28s %-34s provided by the substrate\n",
				in.Repo.InstanceName, in.Repo.RepoClassName)
		default:
			fmt.Fprintf(out, "  %-28s %-34s %s\n",
				in.Repo.InstanceName, in.Repo.RepoClassName, in.Repo.Version)
		}
	}
	for _, previous := range p.Renamed {
		fmt.Fprintf(cmd.ErrOrStderr(),
			"warning: %s was renamed, so it is no longer declared. Anything already deployed"+
				" under that name stays deployed -- `nsctl repo list --env %s` shows it as"+
				" undeclared\n", previous, slug)
	}
}
