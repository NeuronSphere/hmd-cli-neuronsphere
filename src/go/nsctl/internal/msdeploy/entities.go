package msdeploy

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
)

// Entity types in the hmd_lang_deployment language pack. Fully qualified: the
// CRUD routes are named after the entity's full type.
const (
	EntityEnvironment   = "hmd_lang_deployment.environment"
	EntityDeploymentSet = "hmd_lang_deployment.deployment_set"
	EntityChangeSet     = "hmd_lang_deployment.change_set"
)

// EnsureEnvironment idempotently creates the Environment entity for a named
// local environment.
//
// Each environment gets its own row, which is what makes identical instance
// names safe across environments: repo_instance is unique by name *per
// Environment*, so project-bucket in dev2 is a different instance from the one
// in local, and no BOM entry needs renaming.
//
// `type` *is* the environment's name. It is the Environment's business id and
// the only field ms-deployment resolves an environment by -- get_valid_environment,
// apply_changeset and destroy_deploymentset all match on it, and the first two
// assert or index a single result. So a named local environment is typed by its
// slug, exactly as a cloud environment is typed dev or prod.
//
// Find-first, because ms-base PUT never upserts on a business key: without the
// guard each call adds another row and get_valid_environment answers 500.
func (c *Client) EnsureEnvironment(ctx context.Context, slug, accountID, region string) error {
	existing, err := c.Search(ctx, EntityEnvironment, Filter{Attribute: "type", Operator: "=", Value: slug})
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return nil
	}
	if region == "" {
		region = "reg1"
	}
	_, err = c.PutEntity(ctx, EntityEnvironment, map[string]any{
		"type":           slug,
		"account_number": accountID,
		"hmd_region":     region,
	})
	return err
}

// EnsureDeploymentSet idempotently creates an environment's DeploymentSet, and
// repairs one written before environments were typed by name.
//
// Such a row says environment: "local" and survives an env delete and recreate,
// because the deployment graph lives in the shared control-plane Postgres
// rather than in the environment's own state -- so without the repair a
// recreated environment keeps applying its changesets to the default one.
func (c *Client) EnsureDeploymentSet(ctx context.Context, name, envSlug string) error {
	existing, err := c.Search(ctx, EntityDeploymentSet, Filter{Attribute: "name", Operator: "=", Value: name})
	if err != nil {
		return err
	}
	definition, err := EncodeCollection(deploymentSetDefinition(envSlug))
	if err != nil {
		return err
	}

	if len(existing) == 0 {
		_, err = c.PutEntity(ctx, EntityDeploymentSet, map[string]any{
			"name":       name,
			"definition": definition,
		})
		return err
	}

	row := existing[0]
	if targets := deploymentSetEnvironments(row); len(targets) == 1 && targets[envSlug] {
		return nil
	}
	// The repair. A row naming the wrong environment is not a cosmetic
	// mismatch: every changeset applied through it lands on that environment's
	// graph, and a --prune would destroy from it.
	if _, err := c.PutEntity(ctx, EntityDeploymentSet, map[string]any{
		"identifier": row["identifier"],
		"name":       name,
		"definition": definition,
	}); err != nil {
		return fmt.Errorf(
			"deployment set %q targets %v instead of %q, and could not be updated: %w. "+
				"It predates environments being typed by name; recreate the local deployment "+
				"graph with `hmd neuronsphere down --purge` before starting %q again",
			name, sortedKeys(deploymentSetEnvironments(row)), envSlug, err, envSlug)
	}
	return nil
}

// deploymentSetDefinition is the one-environment definition for envSlug.
//
// A deployment set names its environments by Environment.type, which for a
// local environment is its slug. Hardcoding "local" would make every named
// environment's changeset resolve to the default environment's graph.
func deploymentSetDefinition(envSlug string) []map[string]any {
	return []map[string]any{{
		"environment":     envSlug,
		"deployment_gate": map[string]any{"transforms": []any{}, "approval": false},
	}}
}

// deploymentSetEnvironments is the set of environments a row's definition
// names. An undecodable definition yields none, which reads as "wrong" and
// sends the caller down the repair path -- the safe direction.
func deploymentSetEnvironments(row map[string]any) map[string]bool {
	targets := map[string]bool{}
	entries, err := DecodeCollection(row["definition"])
	if err != nil {
		return targets
	}
	for _, entry := range entries {
		if env, ok := entry["environment"].(string); ok && env != "" {
			targets[env] = true
		}
	}
	return targets
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// DeploymentNode is one unit of work: one RepoInstance, one script, one
// `hmd ... deploy`. Indivisible.
//
// Seed builds these itself (NERD0015): RIDNid is what register_deployed_instance
// returned, Script is DeployScript over what get_deployment_config resolved, and
// Dependencies are the entry's role targets that are in the same batch, which
// is what the scheduler orders on.
type DeploymentNode struct {
	InstanceName          string         `json:"instance_name"`
	RepoClassName         string         `json:"repo_class_name"`
	Version               string         `json:"version"`
	RIDNid                string         `json:"rid_nid"`
	Script                string         `json:"script"`
	Dependencies          []string       `json:"dependencies"`
	InstanceConfiguration map[string]any `json:"instance_configuration"`
}

// RegisterInstance is the payload of register_deployed_instance.
//
// Status DEPLOY_NEXT records a plan: the RepoInstanceDeployment exists with its
// edges but is not current until set_deployment_status flips it -- exactly the
// record apply_changeset creates per ChangeSet entry. The default, DEPLOYED, is
// what `hmd deploy --local` uses to record what it has already done.
// Dependencies maps a role to an instance name or list of names already in the
// environment; the service refuses one it cannot find, so register in
// dependency order.
type RegisterInstance struct {
	Environment           string         `json:"environment"`
	RepoClassName         string         `json:"repo_class_name"`
	Version               string         `json:"version"`
	InstanceName          string         `json:"instance_name"`
	DeploymentID          string         `json:"deployment_id"`
	InstanceConfiguration map[string]any `json:"instance_configuration"`
	Dependencies          map[string]any `json:"dependencies,omitempty"`
	Status                string         `json:"status,omitempty"`
	HMDRegion             string         `json:"hmd_region,omitempty"`
}

// RegisterDeployedInstance records an instance and returns its
// RepoInstanceDeployment id -- the RIDNid a node reports status and submits
// produced resources against.
func (c *Client) RegisterDeployedInstance(ctx context.Context, in RegisterInstance) (string, error) {
	if in.InstanceConfiguration == nil {
		in.InstanceConfiguration = map[string]any{}
	}
	result, err := c.APIOp(ctx, "register_deployed_instance", in)
	if err != nil {
		return "", err
	}
	rid, _ := result["repo_instance_deployment_id"].(string)
	if rid == "" {
		return "", fmt.Errorf("register_deployed_instance returned no repo_instance_deployment_id for %s", in.InstanceName)
	}
	return rid, nil
}

// DeploymentConfig is the configuration an instance deploys with, as the
// service resolves it: class defaults, the instance's own configuration and one
// entry per dependency role with that dependency's produced resources (baked
// when it is deployed, a `hmd_resource_ref` when it is still DEPLOY_NEXT).
func (c *Client) DeploymentConfig(ctx context.Context, envSlug, instance string) (map[string]any, error) {
	body, err := c.APIOpGet(ctx, "get_deployment_config/"+envSlug+"/"+instance)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decoding the configuration of %s: %w", instance, err)
	}
	return out, nil
}
