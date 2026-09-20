package bom

import (
	"context"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
)

// CandidateWarning flags a bound dependency instance that validate_changeset
// accepts -- the target exists -- but apply_changeset would reject, because it
// neither produces the role's required resource type nor is an instance of
// the role's suggested RepoClass.
type CandidateWarning struct {
	// Instance is the entry whose dependency is in question.
	Instance string
	Role     string
	// Target is the instance name bound to the role.
	Target  string
	Message string
}

// CandidateWarnings replicates, for a dry run, the resource-typing check that
// otherwise only runs at apply time
// (environment_information.py:_instance_satisfies_any_resource_req /
// _instance_is_of_class, :678-715): a bound instance is accepted if it
// satisfies the role's resource requirement, or failing that if it is an
// instance of the role's suggested RepoClass. validate_changeset checks
// neither -- only that the target instance exists somewhere.
//
// toCheck is the entries whose roles are inspected (the would-be changeset --
// typically a reconcile.Plan's Deploy()). known is every entry the plan
// knows about, desired or not, so a target that resolves to an unchanged
// substrate instance can still be classified by RepoClassName.
//
// A target that is itself new in this plan is a special case the service
// cannot answer: suggest_resource_dependencies enumerates only RepoInstances
// that already exist with a deployment, so a same-plan producer is never a
// candidate -- yet apply_changeset accepts it, because it creates
// RepoInstanceDeployments in changeset order and validates each consumer
// against the producer's declared produces (resource_information.py
// :instance_satisfies_requirement). For such a target the check reads what
// its class declares it produces -- the same meta-data/resources declarations
// RegisterCatalog pushes, own type and parent -- and clears the role when the
// required type is among them. Deeper ancestry than the parent is not known
// client-side, so a requirement for a grandparent type still warns; the exact
// server-side answer would be get_producers/<rd_id>?include_subtypes=true.
//
// One SuggestResourceDependencies call per distinct RepoClassName in toCheck;
// its result is already scoped to resource-typed roles, so a role with only a
// class-name dependency is never inspected.
func (s *Seeder) CandidateWarnings(ctx context.Context, envSlug string, toCheck, known []Entry) ([]CandidateWarning, error) {
	classOf := make(map[string]string, len(known))
	for _, e := range known {
		classOf[e.RepoInstanceName] = e.RepoClassName
	}
	// A substrate instance bound in this very plan has no concrete Resource
	// yet: apply.go deploys the substrate as its own first ChangeSet and only
	// submits its Resources (SubmitResources, between phaseA and phaseB)
	// once that deploy has actually run -- something a dry run never does.
	// Warning here would flag every substrate-typed role on a first apply, and
	// none of them are real: `env apply` resolves them by construction.
	beingDeployed := make(map[string]bool, len(toCheck))
	// Every other same-plan target is cleared by declaration instead, so
	// a producer of the wrong type is still reported.
	samePlan := make(map[string]bool, len(toCheck))
	for _, e := range toCheck {
		samePlan[e.RepoInstanceName] = true
		if IsSubstrate(e.RepoInstanceName) {
			beingDeployed[e.RepoInstanceName] = true
		}
	}
	declaredByClass := map[string][]ProducedDefinition{}
	declares := func(target string, rd msdeploy.ResourceRef) bool {
		if !samePlan[target] || s.Versions == nil {
			return false
		}
		class := classOf[target]
		if class == "" {
			return false
		}
		defs, cached := declaredByClass[class]
		if !cached {
			var err error
			defs, err = s.producedBy(class)
			if err != nil {
				s.warn("reading what %s produces: %v", class, err)
				defs = nil
			}
			declaredByClass[class] = defs
		}
		for _, d := range defs {
			if d.Namespace == rd.ResourceNamespace && d.Name == rd.ResourceDefinitionName {
				return true
			}
		}
		return false
	}

	suggestionsByClass := map[string]map[string]msdeploy.RoleSuggestion{}
	var warnings []CandidateWarning
	for _, e := range toCheck {
		if len(e.Dependencies) == 0 || e.RepoClassName == "" {
			continue
		}
		suggestions, cached := suggestionsByClass[e.RepoClassName]
		if !cached {
			rcvID, err := s.repoClassVersionID(ctx, e.RepoClassName)
			if err != nil || rcvID == "" {
				suggestionsByClass[e.RepoClassName] = nil
				continue
			}
			suggestions, err = s.Client.SuggestResourceDependencies(ctx, envSlug, rcvID)
			if err != nil {
				s.warn("checking resource candidates for %s: %v", e.RepoClassName, err)
				suggestions = nil
			}
			suggestionsByClass[e.RepoClassName] = suggestions
		}
		for role, suggestion := range suggestions {
			for _, target := range DependencyTargets(map[string]any{role: e.Dependencies[role]}) {
				if beingDeployed[target] {
					continue
				}
				if candidateNamed(suggestion.Candidates, target) {
					continue
				}
				if suggestion.SuggestedRepoClassName != "" && classOf[target] == suggestion.SuggestedRepoClassName {
					continue
				}
				if declares(target, suggestion.ResourceDefinition) {
					continue
				}
				warnings = append(warnings, CandidateWarning{
					Instance: e.RepoInstanceName,
					Role:     role,
					Target:   target,
					Message:  candidateWarningMessage(target, suggestion),
				})
			}
		}
	}
	return warnings, nil
}

func candidateNamed(candidates []msdeploy.Candidate, name string) bool {
	for _, c := range candidates {
		if c.Name == name {
			return true
		}
	}
	return false
}

func candidateWarningMessage(target string, suggestion msdeploy.RoleSuggestion) string {
	rd := suggestion.ResourceDefinition
	msg := target + " does not produce " + rd.ResourceNamespace + "/" + rd.ResourceDefinitionName
	if suggestion.SuggestedRepoClassName != "" {
		msg += " and is not an instance of " + suggestion.SuggestedRepoClassName
	}
	return msg + "; validate_changeset only checks that it exists, not that it satisfies this role"
}
