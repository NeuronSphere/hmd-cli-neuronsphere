package msdeploy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
)

// Entity and relationship types used to read an environment's instances back
// out of the graph.
const (
	EntityRepoClass                 = "hmd_lang_deployment.repo_class"
	EntityRepoClassVersion          = "hmd_lang_deployment.repo_class_version"
	EntityEnvironmentHasInstance    = "hmd_lang_deployment.environment_has_repo_instance"
	EntityInstanceIsaClass          = "hmd_lang_deployment.repo_instance_isa_repo_class"
	EntityInstanceReqInstance       = "hmd_lang_deployment.repo_instance_req_repo_instance"
	EntityDeploymentHasClassVersion = "hmd_lang_deployment.repo_instance_deployment_has_repo_class_version"
)

// DeployedInstance is one repo instance as the graph has it.
type DeployedInstance struct {
	Name                  string
	RepoClassName         string
	RepoClassVersion      string
	Status                string
	InstanceConfiguration map[string]any
	// Dependencies maps a role to the instance names filling it. A role with
	// one target is written back as a string, matching how a manifest
	// ordinarily spells it.
	Dependencies map[string][]string
}

// EnvironmentInstances reads every repo instance belonging to an environment.
//
// Scoped through environment_has_repo_instance rather than by filtering on
// deployment_id, because that edge is what actually records membership --
// instance names repeat across environments, and the deployment_id lives on
// the deployment rather than the instance.
//
// Everything is read unfiltered and joined here. That is more requests than a
// filtered search would be, but the service has no query language for a
// traversal and the alternative is one round trip per instance per edge.
func (c *Client) EnvironmentInstances(ctx context.Context, envSlug string) ([]DeployedInstance, error) {
	type fetch struct {
		entity string
		into   *[]map[string]any
	}
	var (
		environments, membership, instances, isa, classes []map[string]any
		requires, deployments, edges, deploymentToVersion []map[string]any
		versions                                          []map[string]any
	)
	for _, f := range []fetch{
		{EntityEnvironment, &environments},
		{EntityEnvironmentHasInstance, &membership},
		{EntityRepoInstance, &instances},
		{EntityInstanceIsaClass, &isa},
		{EntityRepoClass, &classes},
		{EntityInstanceReqInstance, &requires},
		{EntityRepoInstanceDeployment, &deployments},
		{EntityRepoInstanceHasRID, &edges},
		{EntityDeploymentHasClassVersion, &deploymentToVersion},
		{EntityRepoClassVersion, &versions},
	} {
		rows, err := c.Search(ctx, f.entity, Filter{})
		if err != nil {
			return nil, err
		}
		*f.into = rows
	}

	// The environment entity carries its slug as `type`, not `name`.
	var envID string
	for _, e := range environments {
		if stringField(e, "type") == envSlug {
			envID = stringField(e, "identifier")
			break
		}
	}
	if envID == "" {
		return nil, fmt.Errorf("no environment %q in the deployment graph", envSlug)
	}

	member := map[string]bool{}
	for _, m := range membership {
		if stringField(m, "ref_from") == envID {
			member[stringField(m, "ref_to")] = true
		}
	}

	nameByID := map[string]string{}
	for _, i := range instances {
		nameByID[stringField(i, "identifier")] = stringField(i, "name")
	}
	classNameByID := map[string]string{}
	for _, c := range classes {
		classNameByID[stringField(c, "identifier")] = stringField(c, "repo_class_name")
	}
	classOfInstance := map[string]string{}
	for _, e := range isa {
		classOfInstance[stringField(e, "ref_from")] = classNameByID[stringField(e, "ref_to")]
	}
	versionByID := map[string]string{}
	for _, v := range versions {
		versionByID[stringField(v, "identifier")] = stringField(v, "version")
	}
	versionOfDeployment := map[string]string{}
	for _, e := range deploymentToVersion {
		versionOfDeployment[stringField(e, "ref_from")] = versionByID[stringField(e, "ref_to")]
	}
	deploymentByID := map[string]map[string]any{}
	for _, d := range deployments {
		deploymentByID[stringField(d, "identifier")] = d
	}

	// The most recent deployment per instance, the same most-recent-by-_created
	// rule InstanceStatus follows.
	newest := map[string]string{}
	latest := map[string]map[string]any{}
	for _, e := range edges {
		from := stringField(e, "ref_from")
		if !member[from] {
			continue
		}
		deployment, known := deploymentByID[stringField(e, "ref_to")]
		if !known {
			continue
		}
		created := stringField(e, "_created")
		if prior, seen := newest[from]; !seen || created >= prior {
			newest[from] = created
			latest[from] = deployment
		}
	}

	dependencies := map[string]map[string][]string{}
	for _, r := range requires {
		from := stringField(r, "ref_from")
		if !member[from] {
			continue
		}
		target := nameByID[stringField(r, "ref_to")]
		role := stringField(r, "role")
		if target == "" || role == "" {
			continue
		}
		if dependencies[from] == nil {
			dependencies[from] = map[string][]string{}
		}
		dependencies[from][role] = append(dependencies[from][role], target)
	}

	out := make([]DeployedInstance, 0, len(member))
	for id := range member {
		name := nameByID[id]
		if name == "" {
			continue
		}
		instance := DeployedInstance{
			Name:          name,
			RepoClassName: classOfInstance[id],
			Dependencies:  dependencies[id],
		}
		if deployment := latest[id]; deployment != nil {
			instance.Status = stringField(deployment, "status")
			instance.RepoClassVersion = versionOfDeployment[stringField(deployment, "identifier")]
			instance.InstanceConfiguration = decodeConfiguration(deployment["instance_configuration"])
		}
		for _, targets := range instance.Dependencies {
			sort.Strings(targets)
		}
		out = append(out, instance)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// decodeConfiguration reads an instance_configuration attribute, which the
// service stores base64-encoded. An undecodable value yields nil rather than
// an error: a configuration that cannot be read is worth reporting as absent,
// not worth failing a whole import over.
func decodeConfiguration(value any) map[string]any {
	switch v := value.(type) {
	case map[string]any:
		return v
	case string:
		if v == "" {
			return nil
		}
		if decoded, err := base64.StdEncoding.DecodeString(v); err == nil {
			var out map[string]any
			if json.Unmarshal(decoded, &out) == nil {
				return out
			}
		}
		var out map[string]any
		if json.Unmarshal([]byte(v), &out) == nil {
			return out
		}
	}
	return nil
}

// CoreInstanceDeployment resolves the RepoInstanceDeployment of a named
// instance in one environment.
//
// A port of find_core_deployment_node. On a restart the local-neuronsphere
// deployment persists in ms-deployment but the nodes list a seed returns is
// gone, so the concrete core Resources can be reattached to it without
// re-seeding a changeset -- which would create a duplicate.
//
// Every environment has its own instance of that name, so membership is
// narrowed through environment_has_repo_instance before a deployment is picked.
// Best-effort: any lookup failure answers "" and the caller carries on.
func (c *Client) CoreInstanceDeployment(ctx context.Context, instanceName, envSlug string) (string, error) {
	environments, err := c.Search(ctx, EntityEnvironment, Filter{Attribute: "type", Operator: "=", Value: envSlug})
	if err != nil {
		return "", err
	}
	envNids := map[string]bool{}
	for _, e := range environments {
		if nid, _ := e["identifier"].(string); nid != "" {
			envNids[nid] = true
		}
	}
	if len(envNids) == 0 {
		return "", nil
	}

	instances, err := c.Search(ctx, EntityRepoInstance, Filter{Attribute: "name", Operator: "=", Value: instanceName})
	if err != nil {
		return "", err
	}
	membership, err := c.Search(ctx, EntityEnvironmentHasInstance, Filter{})
	if err != nil {
		return "", err
	}
	// The edge search is not assumed to filter by ref_from, so the join is done
	// here. The local graph is small enough that this is cheaper than a round
	// trip per instance.
	owned := map[string]bool{}
	for _, edge := range membership {
		from, _ := edge["ref_from"].(string)
		to, _ := edge["ref_to"].(string)
		if envNids[from] {
			owned[to] = true
		}
	}
	instanceNids := map[string]bool{}
	for _, instance := range instances {
		if nid, _ := instance["identifier"].(string); nid != "" && owned[nid] {
			instanceNids[nid] = true
		}
	}
	if len(instanceNids) == 0 {
		return "", nil
	}

	edges, err := c.Search(ctx, EntityRepoInstanceHasRID, Filter{})
	if err != nil {
		return "", err
	}
	// The most recent wins: an instance redeployed across restarts has several,
	// and Resources belong on the one in force.
	var latest string
	for _, edge := range edges {
		from, _ := edge["ref_from"].(string)
		to, _ := edge["ref_to"].(string)
		if instanceNids[from] && to > latest {
			latest = to
		}
	}
	return latest, nil
}
