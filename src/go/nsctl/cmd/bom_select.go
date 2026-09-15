package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bom"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// selectionFlags are how a BOM is cherry-picked, shared by show and import so
// that a show is a preview of the import that follows it.
type selectionFlags struct {
	instances []string
	classes   []string
	exclude   []string
	all       bool
	noDeps    bool
	failed    bool
}

func (s *selectionFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringArrayVar(&s.instances, "instance", nil, "An instance to take, by name. Repeatable")
	cmd.Flags().StringArrayVar(&s.classes, "class", nil, "Every instance of a repo class. Repeatable")
	cmd.Flags().StringArrayVar(&s.exclude, "exclude", nil, "An instance to leave out. Repeatable")
	cmd.Flags().BoolVar(&s.all, "all", false, "Take the whole BOM")
	cmd.Flags().BoolVar(&s.noDeps, "no-deps", false,
		"Take the selection literally, without what fills its roles")
	cmd.Flags().BoolVar(&s.failed, "include-failed", false,
		"Include instances whose last deployment FAILED")
}

func (s *selectionFlags) any() bool {
	return s.all || len(s.instances) > 0 || len(s.classes) > 0
}

// unfilled is a role nothing local will satisfy.
type unfilled struct {
	Instance string
	Role     string
	Target   string
	Why      string
}

// bomSelection is what a selection resolved to, with every decision recorded so
// it can be reported rather than merely acted on.
type bomSelection struct {
	// Picked is what will be declared, in the BOM's own deployment-DAG order.
	Picked []msdeploy.BOMEntry
	// viaDeps maps an instance that was pulled in by the closure to the role
	// that wanted it. An instance that was asked for outright is absent.
	viaDeps map[string]string
	// bound maps a dependency target that needs no import to the reason.
	bound map[string]string
	// rename maps a cloud instance name to the local name that fills its place.
	// Identity for everything the closure imports; the interesting entries are
	// the substrate, whose instance this environment names for itself.
	rename map[string]string
	// skipped maps an instance left out to why.
	skipped map[string]string
	// unfilled are roles that will be dropped from the declaration.
	unfilled []unfilled
}

// apply resolves a selection against a BOM.
//
// declared reports whether the local manifest already has an instance of that
// name; reserved reports whether the name belongs to the environment substrate.
// Both are injected so this stays a function over data.
func (s *selectionFlags) apply(bom []msdeploy.BOMEntry,
	declared func(string) bool, reserved func(string) bool) (bomSelection, error) {

	index := make(map[string]msdeploy.BOMEntry, len(bom))
	order := make(map[string]int, len(bom))
	for i, e := range bom {
		index[e.RepoInstanceName] = e
		order[e.RepoInstanceName] = i
	}

	seed, err := s.seed(bom, index)
	if err != nil {
		return bomSelection{}, err
	}

	excluded := map[string]bool{}
	for _, name := range s.exclude {
		excluded[name] = true
	}

	out := bomSelection{
		viaDeps: map[string]string{},
		bound:   map[string]string{},
		skipped: map[string]string{},
		rename:  map[string]string{},
	}
	picked := map[string]bool{}
	visited := map[string]bool{}

	// Iterative rather than recursive, so a graph that is not the DAG it claims
	// to be terminates instead of overflowing a stack.
	type want struct{ name, why string }
	queue := make([]want, 0, len(seed))
	for _, name := range seed {
		if excluded[name] {
			out.skipped[name] = "excluded"
			continue
		}
		queue = append(queue, want{name, ""})
	}

	for len(queue) > 0 {
		w := queue[0]
		queue = queue[1:]
		if visited[w.name] {
			continue
		}
		visited[w.name] = true

		// A closure step. --exclude applies to what was asked for, not to what
		// the asked-for things need: silently unfilling a role would
		// reintroduce the failure the closure exists to prevent.
		if w.why != "" && excluded[w.name] {
			return bomSelection{}, nserr.New(nserr.Usage,
				"--exclude %s: it is %s, so excluding it would leave that role unfilled.\n"+
					"Drop the exclusion, or pass --no-deps to take the selection literally.",
				w.name, w.why)
		}

		// The substrate is deployed whether or not a manifest names it, so a
		// role pointing at it is filled locally by an instance already there.
		//
		// Matched by repo class as well as by name, because the two
		// environments name the same thing differently: a cloud environment
		// calls its cluster whatever its operators called it, and every local
		// one deploys hmd-inf-eks-cluster as `eks-cluster`. Matching on the name
		// alone imports the cloud's cluster as though it were a workload -- and
		// then deploys a second cluster inside the first.
		entry, known := index[w.name]
		if local, isSubstrate := substrateInstance(entry, known); isSubstrate || reserved(w.name) {
			if local == "" {
				local = w.name
			}
			out.bound[w.name] = "substrate"
			if local != w.name {
				out.bound[w.name] = "substrate, as " + local
			}
			out.rename[w.name] = local
			continue
		}
		if declared(w.name) {
			out.bound[w.name] = "already declared"
			out.rename[w.name] = w.name
			continue
		}
		if !known {
			// Only reachable from the closure: a seed name that matched nothing
			// was refused in seed().
			continue
		}
		if !s.failed && entry.Status != msdeploy.StatusDeployed {
			out.skipped[w.name] = strings.ToLower(entry.Status)
			continue
		}
		if entry.RepoClassVersion == "" {
			// Nothing to deploy from, so a declaration would be one `env apply`
			// refuses. The BOM should never carry this; saying so beats
			// declaring an instance with no version.
			out.skipped[w.name] = "no version in the BOM"
			continue
		}

		picked[w.name] = true
		out.rename[w.name] = w.name
		out.Picked = append(out.Picked, entry)
		// Recorded here rather than on arrival, so the closure note lists what
		// was actually imported. A target that turned out to be substrate, or
		// already declared, or absent from the BOM is accounted for once, by
		// the line that says what happened to it.
		if w.why != "" {
			out.viaDeps[w.name] = w.why
		}

		if s.noDeps {
			continue
		}
		for _, role := range entry.Roles() {
			for _, target := range entry.Targets(role) {
				queue = append(queue, want{target, fmt.Sprintf("%s needs it for %s", w.name, role)})
			}
		}
	}

	// A role is dropped when nothing local will fill it. Computed after the
	// traversal, because whether a target was picked is only settled then.
	for _, e := range out.Picked {
		for _, role := range e.Roles() {
			for _, target := range e.Targets(role) {
				switch {
				case picked[target], out.bound[target] != "":
				case s.noDeps:
					out.unfilled = append(out.unfilled, unfilled{e.RepoInstanceName, role, target, "not selected"})
				case out.skipped[target] != "":
					out.unfilled = append(out.unfilled, unfilled{e.RepoInstanceName, role, target, out.skipped[target]})
				default:
					out.unfilled = append(out.unfilled, unfilled{e.RepoInstanceName, role, target, "not in the BOM"})
				}
			}
		}
	}

	// Back into the BOM's order, which is the order the instances can be
	// deployed in and the order a reader saw them in.
	sort.Slice(out.Picked, func(i, j int) bool {
		return order[out.Picked[i].RepoInstanceName] < order[out.Picked[j].RepoInstanceName]
	})
	return out, nil
}

// seed is what was asked for outright, before the closure.
//
// A selector matching nothing is refused rather than quietly contributing
// nothing: it is a typo, and finding out at the end that an import was smaller
// than intended is much worse than finding out now.
func (s *selectionFlags) seed(bom []msdeploy.BOMEntry, index map[string]msdeploy.BOMEntry) ([]string, error) {
	if s.all {
		names := make([]string, 0, len(bom))
		for _, e := range bom {
			names = append(names, e.RepoInstanceName)
		}
		return names, nil
	}

	var names []string
	for _, name := range s.instances {
		if _, ok := index[name]; !ok {
			return nil, nserr.New(nserr.Usage, "--instance %s: this BOM has no such instance. It has: %s",
				name, strings.Join(bomInstanceNames(bom), ", "))
		}
		names = append(names, name)
	}
	for _, class := range s.classes {
		var matched []string
		for _, e := range bom {
			if e.RepoClassName == class {
				matched = append(matched, e.RepoInstanceName)
			}
		}
		if len(matched) == 0 {
			return nil, nserr.New(nserr.Usage, "--class %s: this BOM deploys no instance of it. It deploys: %s",
				class, strings.Join(bomClassNames(bom), ", "))
		}
		names = append(names, matched...)
	}
	return names, nil
}

// resolvedRoles is the dependency mapping to declare for an entry: the roles
// something local will fill, and only those.
//
// A role with one target is written as a bare string rather than a one-element
// list. Both are valid, and the string is how a person writes it -- which is
// the same choice declarationFor makes for the local path.
func (s bomSelection) resolvedRoles(e msdeploy.BOMEntry) map[string]any {
	resolvable := map[string]bool{}
	for _, p := range s.Picked {
		resolvable[p.RepoInstanceName] = true
	}
	for name := range s.bound {
		resolvable[name] = true
	}

	out := map[string]any{}
	for _, role := range e.Roles() {
		var kept []string
		for _, target := range e.Targets(role) {
			if !resolvable[target] {
				continue
			}
			// Through the rename, so a role that pointed at the cloud's name
			// for a substrate instance points at this environment's.
			if local, ok := s.rename[target]; ok && local != "" {
				kept = append(kept, local)
				continue
			}
			kept = append(kept, target)
		}
		switch len(kept) {
		case 0:
		case 1:
			out[role] = kept[0]
		default:
			list := make([]any, 0, len(kept))
			for _, t := range kept {
				list = append(list, t)
			}
			out[role] = list
		}
	}
	return out
}

// report prints everything the selection decided that is not the table itself.
//
// All of it to stderr: it is commentary on an answer, and a script capturing
// the answer should not have to filter it out.
func (s bomSelection) report(cmd *cobra.Command) {
	err := cmd.ErrOrStderr()

	if len(s.viaDeps) > 0 {
		fmt.Fprintf(err, "note: %s pulled in by the closure:\n", count(len(s.viaDeps), "instance"))
		for _, name := range sortedKeysOf(s.viaDeps) {
			fmt.Fprintf(err, "  %-30s %s\n", name, s.viaDeps[name])
		}
	}
	if len(s.bound) > 0 {
		fmt.Fprintf(err, "note: bound without importing:\n")
		for _, name := range sortedKeysOf(s.bound) {
			fmt.Fprintf(err, "  %-30s %s\n", name, s.bound[name])
		}
	}
	for _, name := range sortedKeysOf(s.skipped) {
		fmt.Fprintf(err, "note: skipping %s (%s)\n", name, s.skipped[name])
	}
	for _, u := range s.unfilled {
		fmt.Fprintf(err,
			"warning: %s's %s role wants %s, which is %s; the role will not be declared\n",
			u.Instance, u.Role, u.Target, u.Why)
	}
}

// substrateInstance names the local instance of an entry's repo class, when
// that class is one the environment substrate deploys itself.
func substrateInstance(e msdeploy.BOMEntry, known bool) (string, bool) {
	if !known {
		return "", false
	}
	return bom.SubstrateInstanceFor(e.RepoClassName)
}

func bomInstanceNames(bom []msdeploy.BOMEntry) []string {
	names := make([]string, 0, len(bom))
	for _, e := range bom {
		names = append(names, e.RepoInstanceName)
	}
	sort.Strings(names)
	return names
}

func bomClassNames(bom []msdeploy.BOMEntry) []string {
	seen := map[string]bool{}
	var names []string
	for _, e := range bom {
		if e.RepoClassName != "" && !seen[e.RepoClassName] {
			seen[e.RepoClassName] = true
			names = append(names, e.RepoClassName)
		}
	}
	sort.Strings(names)
	return names
}
