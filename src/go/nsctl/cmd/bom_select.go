package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bom"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/repoclass"
)

// selectionFlags are how a BOM is cherry-picked, shared by show and import so
// that a show is a preview of the import that follows it.
type selectionFlags struct {
	instances []string
	classes   []string
	exclude   []string
	with      []string
	all       bool
	noDeps    bool
	failed    bool
	noStubs   bool
}

func (s *selectionFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringArrayVar(&s.instances, "instance", nil, "An instance to take, by name. Repeatable")
	cmd.Flags().StringArrayVar(&s.classes, "class", nil, "Every instance of a repo class. Repeatable")
	cmd.Flags().StringArrayVar(&s.exclude, "exclude", nil, "An instance to leave out. Repeatable")
	cmd.Flags().StringArrayVar(&s.with, "with", nil,
		"Follow an optional role, by role name or repo class. Repeatable")
	cmd.Flags().BoolVar(&s.all, "all", false, "Take the whole BOM")
	cmd.Flags().BoolVar(&s.noDeps, "no-deps", false,
		"Take the selection literally, without what fills its roles")
	cmd.Flags().BoolVar(&s.failed, "include-failed", false,
		"Include instances whose last deployment FAILED")
	cmd.Flags().BoolVar(&s.noStubs, "no-stub-roles", false,
		"Refuse a required role nothing local fills, rather than binding it to the core instance")
}

func (s *selectionFlags) any() bool {
	return s.all || len(s.instances) > 0 || len(s.classes) > 0
}

// roleFacts reports what a repo class declares about its dependency roles, at
// the version a BOM entry names.
//
// ok is false when the declaration could not be read at all -- no artifact
// here, no manifest inside it, a manifest that will not parse. Every caller
// answers that by following the role anyway and saying so: a closure that
// quietly shrank because a file was missing would drop a required role, and the
// failure would arrive at `env apply` naming the role rather than the read that
// failed.
type roleFacts func(repoClass, version string) (map[string]repoclass.RoleFact, bool)

// allRolesUnknown is the source to use when nothing can be read -- `show`
// without --resolve against an empty cache. It reproduces the pre-NERD013
// behaviour exactly, which is what makes that the safe default.
func allRolesUnknown(string, string) (map[string]repoclass.RoleFact, bool) { return nil, false }

// unfilled is a role nothing local will satisfy.
type unfilled struct {
	Instance string
	Role     string
	Target   string
	Why      string
}

// roleRef names one declared role of one instance, and what it points at.
type roleRef struct {
	Instance  string
	Role      string
	Target    string
	RepoClass string
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
	// notFollowed are optional roles the closure deliberately did not follow.
	// Kept apart from unfilled because nothing went wrong: NERD013 SPEC001.
	notFollowed []roleRef
	// stubbed are required, name-only roles bound to the core instance because
	// nothing local fills them. NERD013 SPEC003. One entry per *role*, so two
	// instances needing the same absent thing are both reported.
	stubbed []roleRef
	// stubTargets are the instance names those roles now point at. Kept apart
	// from bound so a stub is reported once, by the warning that explains it,
	// rather than twice in two vocabularies.
	stubTargets map[string]string
	// unread are instances whose repo class declarations could not be read, so
	// every one of their roles was followed.
	unread []string
}

// apply resolves a selection against a BOM.
//
// declared reports whether the local manifest already has an instance of that
// name; reserved reports whether the name belongs to the environment substrate;
// roles reports what a repo class declares about its dependency roles. All
// three are injected so this stays a function over data -- a BOM and three
// predicates in, a decision out -- and therefore testable without a server.
func (s *selectionFlags) apply(entries []msdeploy.BOMEntry,
	declared func(string) bool, reserved func(string) bool, roles roleFacts) (bomSelection, error) {

	if roles == nil {
		roles = allRolesUnknown
	}

	index := make(map[string]msdeploy.BOMEntry, len(entries))
	order := make(map[string]int, len(entries))
	for i, e := range entries {
		index[e.RepoInstanceName] = e
		order[e.RepoInstanceName] = i
	}
	// The BOM's name for a target's class, falling back to the one the
	// dependency block declares -- which is all there is for a target the BOM
	// does not contain, and is exactly the case worth naming.
	classOf := func(instance string, fact repoclass.RoleFact) string {
		if known := index[instance].RepoClassName; known != "" {
			return known
		}
		return fact.RepoClassName
	}

	seed, err := s.seed(entries, index)
	if err != nil {
		return bomSelection{}, err
	}

	excluded := map[string]bool{}
	for _, name := range s.exclude {
		excluded[name] = true
	}

	out := bomSelection{
		viaDeps:     map[string]string{},
		bound:       map[string]string{},
		skipped:     map[string]string{},
		rename:      map[string]string{},
		stubTargets: map[string]string{},
	}
	picked := map[string]bool{}
	visited := map[string]bool{}

	// What --with matched, and what optional roles there were to match, so a
	// --with that contributed nothing can be refused with a useful list.
	matchedWith := map[string]bool{}
	optionalSeen := map[string]bool{}
	requiredSeen := map[string]bool{}

	// Iterative rather than recursive, so a graph that is not the DAG it claims
	// to be terminates instead of overflowing a stack.
	type want struct {
		name string
		// why is the role that wanted it, empty when asked for outright.
		why string
		// fact and known describe the role that reached it, for deciding
		// whether it may be excluded.
		fact  repoclass.RoleFact
		known bool
	}
	queue := make([]want, 0, len(seed))
	for _, name := range seed {
		if excluded[name] {
			out.skipped[name] = "excluded"
			continue
		}
		queue = append(queue, want{name: name})
	}

	for len(queue) > 0 {
		w := queue[0]
		queue = queue[1:]
		if visited[w.name] {
			continue
		}
		visited[w.name] = true

		// A closure step. Whether --exclude may remove it depends on the role
		// that wanted it, because that is what decides whether the role can be
		// filled some other way once the target is gone.
		//
		//   optional      -- the role is simply not declared. Always excludable.
		//   name-only     -- nothing but the role's presence is validated, so it
		//                    binds to the core instance below. Excludable.
		//   resource-typed - the producer is validated against the resource type
		//                    and cannot be faked. Refused.
		//   unknown       -- it may be required. Refused, as before NERD013.
		if w.why != "" && excluded[w.name] {
			switch {
			case !w.known:
				return bomSelection{}, nserr.New(nserr.Usage,
					"--exclude %s: %s, and this run could not read %s's role declarations,\n"+
						"so it cannot tell whether that role is required.\n"+
						"Fetch the artifacts (drop --no-pull, or pass --resolve to `bom show`), or pass --no-deps.",
					w.name, w.why, classOf(w.name, w.fact))
			case w.fact.Required && w.fact.ResourceTyped():
				return bomSelection{}, nserr.New(nserr.Usage,
					"--exclude %s: %s, and that role asks for the resource type %s,\n"+
						"which hmd-ms-deployment validates against what the producing instance really produces.\n"+
						"It cannot be bound to something that does not produce it. Drop the exclusion, or pass --no-deps.",
					w.name, w.why, w.fact.ResourceType)
			}
			out.skipped[w.name] = "excluded"
			continue
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

		// NERD013 SPEC001. Follow a role only when the repo class declares it
		// required -- or when this run cannot tell, in which case it follows
		// everything and says so.
		facts, readable := roles(entry.RepoClassName, entry.RepoClassVersion)
		if !readable {
			out.unread = append(out.unread, w.name)
		}
		for _, role := range entry.Roles() {
			fact, haveRole := facts[role]
			for _, target := range entry.Targets(role) {
				ref := roleRef{w.name, role, target, classOf(target, fact)}
				switch {
				case !readable, !haveRole:
					// Conservative: a role the declaration does not mention is
					// version skew between the deployed edge and the manifest,
					// and guessing it optional is the dangerous guess.
				case fact.Required:
					requiredSeen[role] = true
					requiredSeen[fact.RepoClassName] = true
				default:
					optionalSeen[role] = true
					if fact.RepoClassName != "" {
						optionalSeen[fact.RepoClassName] = true
					}
					if ref.RepoClass != "" {
						optionalSeen[ref.RepoClass] = true
					}
					if !s.wantsRole(role, fact.RepoClassName, ref.RepoClass, matchedWith) {
						out.notFollowed = append(out.notFollowed, ref)
						continue
					}
				}
				queue = append(queue, want{name: target, why: fmt.Sprintf("%s needs it for %s", w.name, role),
					fact: fact, known: readable && haveRole})
			}
		}
	}

	if err := s.checkWith(matchedWith, optionalSeen, requiredSeen); err != nil {
		return bomSelection{}, err
	}

	// What became of each role of each picked instance. Computed after the
	// traversal, because whether a target was picked is only settled then.
	for _, e := range out.Picked {
		facts, readable := roles(e.RepoClassName, e.RepoClassVersion)
		for _, role := range e.Roles() {
			fact, haveRole := facts[role]
			for _, target := range e.Targets(role) {
				if picked[target] || out.bound[target] != "" {
					continue
				}
				ref := roleRef{e.RepoInstanceName, role, target, classOf(target, fact)}
				optional := readable && haveRole && !fact.Required
				stubbable := readable && haveRole && fact.Required && !fact.ResourceTyped() && !s.noStubs
				switch {
				case s.noDeps:
					out.unfilled = append(out.unfilled, unfilled{ref.Instance, role, target, "not selected"})
				case optional:
					// Already recorded as notFollowed. Nothing is wrong: an
					// optional role that is not declared is exactly what
					// hmd-ms-deployment's missing_optional_role tolerates.
				case stubbable:
					// NERD013 SPEC003. Presence is the whole of what is
					// validated, so the core instance can stand in -- the same
					// move internal/bom's Substrate already makes for
					// datadog-lambda and rds-loggroup.
					//
					// Appended per role even when this target was already
					// stubbed for someone else, so every instance affected is
					// named. Silently binding the second one would under-report
					// exactly the thing this warning exists to surface.
					out.stubbed = append(out.stubbed, ref)
					out.stubTargets[target] = bom.CoreInstanceName
					out.rename[target] = bom.CoreInstanceName
				case out.skipped[target] != "":
					out.unfilled = append(out.unfilled, unfilled{ref.Instance, role, target, out.skipped[target]})
				case readable && haveRole && fact.ResourceTyped():
					out.unfilled = append(out.unfilled, unfilled{ref.Instance, role, target,
						"not filled here, and its " + fact.ResourceType + " role cannot be stubbed"})
				default:
					out.unfilled = append(out.unfilled, unfilled{ref.Instance, role, target, "not in the BOM"})
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

// wantsRole reports whether --with asked for this optional role, by role name
// or by either spelling of the repo class that fills it -- what the dependency
// block declares, and what the BOM entry actually is. They differ when a role
// is filled by a class other than the one suggested, which resource-typed
// resolution permits.
func (s *selectionFlags) wantsRole(role, declaredClass, targetClass string, matched map[string]bool) bool {
	hit := false
	for _, w := range s.with {
		if w == role || (declaredClass != "" && w == declaredClass) || (targetClass != "" && w == targetClass) {
			matched[w] = true
			hit = true
		}
	}
	return hit
}

// checkWith refuses a --with that contributed nothing.
//
// The same rule seed() applies to --instance and --class, for the same reason:
// a selector that silently matches nothing has exactly the shape of a
// successful smaller import, and finding out afterwards is much worse than
// finding out now. A --with naming something that was already required is not
// an error -- it asked for an outcome it got.
func (s *selectionFlags) checkWith(matched, optional, required map[string]bool) error {
	for _, w := range s.with {
		if matched[w] || required[w] {
			continue
		}
		names := sortedKeysOf(optional)
		if len(names) == 0 {
			return nserr.New(nserr.Usage,
				"--with %s: this selection reached no optional roles at all, so there is nothing to add.", w)
		}
		return nserr.New(nserr.Usage,
			"--with %s: no optional role or repo class of that name was reached. There are: %s",
			w, strings.Join(names, ", "))
	}
	return nil
}

// seed is what was asked for outright, before the closure.
//
// A selector matching nothing is refused rather than quietly contributing
// nothing: it is a typo, and finding out at the end that an import was smaller
// than intended is much worse than finding out now.
func (s *selectionFlags) seed(entries []msdeploy.BOMEntry, index map[string]msdeploy.BOMEntry) ([]string, error) {
	if s.all {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.RepoInstanceName)
		}
		return names, nil
	}

	var names []string
	for _, name := range s.instances {
		if _, ok := index[name]; !ok {
			return nil, nserr.New(nserr.Usage, "--instance %s: this BOM has no such instance. It has: %s",
				name, strings.Join(bomInstanceNames(entries), ", "))
		}
		names = append(names, name)
	}
	for _, class := range s.classes {
		var matched []string
		for _, e := range entries {
			if e.RepoClassName == class {
				matched = append(matched, e.RepoInstanceName)
			}
		}
		if len(matched) == 0 {
			return nil, nserr.New(nserr.Usage, "--class %s: this BOM deploys no instance of it. It deploys: %s",
				class, strings.Join(bomClassNames(entries), ", "))
		}
		names = append(names, matched...)
	}
	return names, nil
}

// present reports whether a BOM instance ends up in this environment by any
// route -- imported, bound as substrate, or stubbed to the core instance.
func (s bomSelection) present(target string) bool {
	for _, p := range s.Picked {
		if p.RepoInstanceName == target {
			return true
		}
	}
	if _, ok := s.bound[target]; ok {
		return true
	}
	_, ok := s.stubTargets[target]
	return ok
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
	for name := range s.stubTargets {
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
			// for a substrate instance points at this environment's -- and so a
			// stubbed role points at the core instance.
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
	// An optional role that was not followed usually means its target was
	// not imported -- but not always: the target may be here anyway, picked
	// through some other instance's required role or bound as substrate. The
	// role is then kept (resolvedRoles keeps every role whose target is
	// present), so saying it "was not imported" would be false. Two notes.
	var dropped, kept []roleRef
	for _, r := range s.notFollowed {
		if s.present(r.Target) {
			kept = append(kept, r)
		} else {
			dropped = append(dropped, r)
		}
	}
	if len(dropped) > 0 {
		fmt.Fprintf(err, "note: %s not followed, so %s not imported (pass --with <role> to add one):\n",
			count(len(dropped), "optional role"), theyWereOrItWas(len(dropped)))
		for _, r := range sortedRefs(dropped) {
			fmt.Fprintf(err, "  %-30s %s -> %s\n", r.Instance+":"+r.Role, orDash(r.RepoClass), r.Target)
		}
	}
	if len(kept) > 0 {
		fmt.Fprintf(err, "note: %s not followed, but the target is here anyway, so the role is kept:\n",
			count(len(kept), "optional role"))
		for _, r := range sortedRefs(kept) {
			fmt.Fprintf(err, "  %-30s %s -> %s\n", r.Instance+":"+r.Role, orDash(r.RepoClass), r.Target)
		}
	}
	if len(s.stubbed) > 0 {
		fmt.Fprintf(err,
			"warning: %s bound to %s because nothing here fills %s.\n"+
				"         Only the role's presence is validated, so the deploy will proceed -- but the\n"+
				"         instance below is NOT deployed, and anything that needs it at runtime will not find it.\n"+
				"         Pass --no-stub-roles to refuse instead.\n",
			count(len(s.stubbed), "required role"), bom.CoreInstanceName, themOrIt(len(s.stubbed)))
		for _, r := range sortedRefs(s.stubbed) {
			fmt.Fprintf(err, "  %-30s wanted %s (%s)\n", r.Instance+":"+r.Role, r.Target, orDash(r.RepoClass))
		}
	}
	if len(s.unread) > 0 {
		sorted := append([]string{}, s.unread...)
		sort.Strings(sorted)
		fmt.Fprintf(err,
			"note: could not read the role declarations of %s, so every role of %s was followed: %s\n",
			count(len(sorted), "instance"), themOrIt(len(sorted)), strings.Join(sorted, ", "))
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

func sortedRefs(refs []roleRef) []roleRef {
	out := append([]roleRef{}, refs...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Instance != out[j].Instance {
			return out[i].Instance < out[j].Instance
		}
		return out[i].Role < out[j].Role
	})
	return out
}

func theyWereOrItWas(n int) string {
	if n == 1 {
		return "it was"
	}
	return "they were"
}

func themOrIt(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}

func bomInstanceNames(entries []msdeploy.BOMEntry) []string {
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.RepoInstanceName)
	}
	sort.Strings(names)
	return names
}

func bomClassNames(entries []msdeploy.BOMEntry) []string {
	seen := map[string]bool{}
	var names []string
	for _, e := range entries {
		if e.RepoClassName != "" && !seen[e.RepoClassName] {
			seen[e.RepoClassName] = true
			names = append(names, e.RepoClassName)
		}
	}
	sort.Strings(names)
	return names
}
