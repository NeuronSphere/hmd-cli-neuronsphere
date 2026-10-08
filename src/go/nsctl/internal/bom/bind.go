package bom

import (
	"context"
	"sort"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
)

// Binding is one dependency role BindSuggested filled in.
type Binding struct {
	Instance string
	Role     string
	Target   string
}

// Ambiguity is a required, resource-typed role that more than one instance
// could fill. BindSuggested leaves it unbound; the operator chooses.
type Ambiguity struct {
	Instance   string
	Role       string
	Definition msdeploy.ResourceRef
	// Candidates are the instance names that fit, sorted.
	Candidates []string
}

// BindSuggested fills a required, resource-typed dependency role that the
// environment manifest left unbound, when exactly one instance can satisfy it.
//
// suggest_resource_dependencies is documented to leave the choice to the
// operator ("the operator still supplies the satisfying instance"). That is
// right when there is a choice. When there is only one candidate it is
// bookkeeping, and asking for it makes every declaration of a repo spell out
// wiring the control plane already knows.
//
// A candidate is any of:
//
//   - an instance the service suggests (it has a deployment already);
//   - a known entry -- substrate or declared -- whose repo class produces the
//     role's definition. The service cannot suggest these before they are
//     deployed, which is exactly the first apply of a substrate producer, so
//     nsctl answers from what the class declares it produces;
//   - a known entry whose repo class is the one the manifest dependency names
//     (suggested_repo_class_name). That covers roles whose definition nothing
//     produces yet, where the class name is the only link.
//
// A role that carries a tag selector takes the service's candidates only:
// nsctl cannot evaluate a selector against a static produces table, and a
// producer of the type that the selector excludes is not a candidate.
//
// It never touches a role the manifest binds, never binds an optional role
// (that would pull a producer into environments that work without one), and
// never binds an instance to itself. toBind's Dependencies maps are updated in
// place. A service that cannot be reached leaves everything unbound, so
// validate_changeset reports the role as it did before this step existed.
func (s *Seeder) BindSuggested(ctx context.Context, envSlug string, toBind, known []Entry) ([]Binding, []Ambiguity, error) {
	suggestionsByClass := map[string]map[string]msdeploy.RoleSuggestion{}
	producedByClass := map[string][]ProducedDefinition{}

	var bound []Binding
	var ambiguous []Ambiguity
	for i := range toBind {
		e := &toBind[i]
		if e.RepoClassName == "" {
			continue
		}
		suggestions, cached := suggestionsByClass[e.RepoClassName]
		if !cached {
			suggestions = s.suggestionsFor(ctx, envSlug, e.RepoClassName)
			suggestionsByClass[e.RepoClassName] = suggestions
		}

		roles := make([]string, 0, len(suggestions))
		for role := range suggestions {
			roles = append(roles, role)
		}
		sort.Strings(roles)

		for _, role := range roles {
			suggestion := suggestions[role]
			if !suggestionRequired(suggestion.Required) || roleBound(e.Dependencies, role) {
				continue
			}
			names := map[string]bool{}
			for _, c := range suggestion.Candidates {
				if c.Name != e.RepoInstanceName {
					names[c.Name] = true
				}
			}
			if suggestion.TagSelector == "" {
				for _, k := range known {
					if k.RepoInstanceName == e.RepoInstanceName || k.RepoClassName == "" {
						continue
					}
					if (suggestion.SuggestedRepoClassName != "" && k.RepoClassName == suggestion.SuggestedRepoClassName) ||
						s.entryProduces(k, suggestion.ResourceDefinition, producedByClass) {
						names[k.RepoInstanceName] = true
					}
				}
			}

			candidates := make([]string, 0, len(names))
			for n := range names {
				candidates = append(candidates, n)
			}
			sort.Strings(candidates)

			switch len(candidates) {
			case 0:
			case 1:
				if e.Dependencies == nil {
					e.Dependencies = map[string]any{}
				}
				e.Dependencies[role] = candidates[0]
				bound = append(bound, Binding{Instance: e.RepoInstanceName, Role: role, Target: candidates[0]})
			default:
				ambiguous = append(ambiguous, Ambiguity{
					Instance:   e.RepoInstanceName,
					Role:       role,
					Definition: suggestion.ResourceDefinition,
					Candidates: candidates,
				})
			}
		}
	}
	return bound, ambiguous, nil
}

// suggestionsFor asks the service for one repo class's resource-typed roles.
// Any failure is a warning and an empty answer: binding is a convenience, and
// the plan or apply that called it must not depend on it.
func (s *Seeder) suggestionsFor(ctx context.Context, envSlug, repoClass string) map[string]msdeploy.RoleSuggestion {
	rcvID, err := s.repoClassVersionID(ctx, repoClass)
	if err != nil || rcvID == "" {
		return nil
	}
	suggestions, err := s.Client.SuggestResourceDependencies(ctx, envSlug, rcvID)
	if err != nil {
		s.warn("looking up suggested dependencies for %s: %v", repoClass, err)
		return nil
	}
	return suggestions
}

// entryProduces reports whether an entry's repo class produces a definition,
// ignoring version: the service's candidate list does the version matching,
// and the substrate declares a single version of each type.
func (s *Seeder) entryProduces(e Entry, rd msdeploy.ResourceRef, cache map[string][]ProducedDefinition) bool {
	defs, cached := cache[e.RepoClassName]
	if !cached {
		switch {
		case e.RepoClassName == CoreRepoClass:
			defs = CoreProducedDefinitions
		case s.Versions != nil:
			var err error
			defs, err = s.producedBy(e.RepoClassName)
			if err != nil {
				s.warn("reading what %s produces: %v", e.RepoClassName, err)
				defs = nil
			}
		default:
			defs = fallbackProduces[e.RepoClassName]
		}
		cache[e.RepoClassName] = defs
	}
	for _, d := range defs {
		if d.Namespace == rd.ResourceNamespace && d.Name == rd.ResourceDefinitionName {
			return true
		}
	}
	return false
}

// suggestionRequired reads the service's "required", a bool or the string
// "true"/"false" depending on how the role was declared.
func suggestionRequired(v any) bool {
	switch r := v.(type) {
	case bool:
		return r
	case string:
		return strings.EqualFold(strings.TrimSpace(r), "true")
	}
	return false
}

// roleBound reports whether the manifest already names an instance for a role.
func roleBound(deps map[string]any, role string) bool {
	switch v := deps[role].(type) {
	case nil:
		return false
	case string:
		return v != ""
	case []any:
		return len(v) > 0
	case []string:
		return len(v) > 0
	}
	return true
}
