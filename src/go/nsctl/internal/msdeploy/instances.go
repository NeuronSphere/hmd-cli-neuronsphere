package msdeploy

import (
	"context"
	"sort"
)

// Entity types involved in resolving what an environment has deployed.
const (
	EntityRepoInstance           = "hmd_lang_deployment.repo_instance"
	EntityRepoInstanceDeployment = "hmd_lang_deployment.repo_instance_deployment"
	EntityRepoInstanceHasRID     = "hmd_lang_deployment.repo_instance_has_repo_instance_deployment"
)

// Status values ms-deployment accepts for a RepoInstanceDeployment.
//
// A closed set: the service rejects anything outside it with a 400 naming the
// whole list. There is no in-progress state at all -- no DEPLOYING -- so a node
// goes straight from DEPLOY_NEXT to its outcome.
const (
	StatusDeployed = "DEPLOYED"
	StatusFailed   = "FAILED"
	// StatusDeployNext is a recorded plan: the deployment exists with its
	// edges but is not current until set_deployment_status flips it.
	StatusDeployNext = "DEPLOY_NEXT"
	// StatusDestroyed is the terminal status of a destroy manifest's nodes,
	// which are the same nodes with their edges reversed.
	StatusDestroyed = "DESTROYED"

	// StatusNeverDeployed is reported for an instance that exists in the graph
	// but has no deployment in this environment. Distinct from an instance
	// nsctl has never heard of, which is absent from the map entirely.
	StatusNeverDeployed = ""
)

// ChangeSetDeployment statuses are a different set again: CREATED, STARTED,
// COMPLETED, FAILED, DESTROYED. A finished deployment is COMPLETED, not
// DEPLOYED.
const (
	CSDStatusCompleted = "COMPLETED"
	CSDStatusFailed    = "FAILED"
	// CSDStatusStarted is set when execution begins. The in-process runner
	// never sent it -- nothing could observe a deployment it owned anyway --
	// but a submitted workflow is observable while it runs, so the changeset
	// has to say so.
	CSDStatusStarted = "STARTED"
	// CSDStatusDestroyed is the terminal status of a destroy changeset.
	CSDStatusDestroyed = "DESTROYED"
)

// InstanceStatus maps each repo instance name to the status of its most recent
// deployment in one environment.
//
// The status cannot be read off repo_instance: that entity carries only
// _created, _updated, auto_deploy, identifier and name. It lives on
// repo_instance_deployment, reachable only through the edge entity, so this
// reads all three and joins them rather than filtering one -- filtering
// repo_instance by deployment_id returns a 500, because the attribute is not
// on that entity at all.
//
// Scoped by deployment_id for a reason that is easy to miss: repo_instance is
// unique by name *per environment*, so every environment has a row under the
// same name. An unscoped join reports a sibling environment's status, and
// because the sibling's rows are scanned too, a real status can be clobbered
// by a neighbour's absence.
//
// Resolution is per name rather than per instance id, for that same reason: a
// per-id map collapsed by name afterwards lets whichever row was scanned last
// win.
func (c *Client) InstanceStatus(ctx context.Context, deploymentID string) (map[string]string, error) {
	instances, err := c.Search(ctx, EntityRepoInstance, Filter{})
	if err != nil {
		return nil, err
	}
	deployments, err := c.Search(ctx, EntityRepoInstanceDeployment, Filter{})
	if err != nil {
		return nil, err
	}
	edges, err := c.Search(ctx, EntityRepoInstanceHasRID, Filter{})
	if err != nil {
		return nil, err
	}

	byNid := make(map[string]map[string]any, len(deployments))
	for _, d := range deployments {
		if nid := stringField(d, "identifier"); nid != "" {
			byNid[nid] = d
		}
	}

	nameByInstance := make(map[string]string, len(instances))
	status := make(map[string]string, len(instances))
	for _, i := range instances {
		nid, name := stringField(i, "identifier"), stringField(i, "name")
		if nid == "" || name == "" {
			continue
		}
		nameByInstance[nid] = name
		if _, seen := status[name]; !seen {
			status[name] = StatusNeverDeployed
		}
	}

	newest := map[string]string{}
	for _, e := range edges {
		deployment, known := byNid[stringField(e, "ref_to")]
		if !known || !matchesEnvironment(deployment, deploymentID) {
			continue
		}
		name := nameByInstance[stringField(e, "ref_from")]
		if name == "" {
			continue
		}
		created := stringField(e, "_created")
		if prior, seen := newest[name]; !seen || created >= prior {
			newest[name] = created
			status[name] = stringField(deployment, "status")
		}
	}
	return status, nil
}

// matchesEnvironment reports whether a deployment belongs to an environment.
//
// A deployment carrying no deployment_id at all matches, so a graph seeded
// before that field was populated still resolves rather than reading as empty.
func matchesEnvironment(deployment map[string]any, deploymentID string) bool {
	if deployment == nil {
		return false
	}
	if deploymentID == "" {
		return true
	}
	value, present := deployment["deployment_id"]
	if !present || value == nil {
		return true
	}
	did, _ := value.(string)
	return did == deploymentID
}

func stringField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, _ := m[key].(string)
	return v
}

// DeployedNames lists the instance names an environment has deployed, sorted.
func DeployedNames(status map[string]string) []string {
	var names []string
	for name, s := range status {
		if s == StatusDeployed {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}
