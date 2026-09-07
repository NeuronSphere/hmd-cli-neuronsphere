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

// NewChangeSetName picks a name not already in use.
//
// apply_changeset asserts exactly one changeset row with the given name, so a
// repeated seed -- a delta-apply on restart, say -- must not collide with the
// first call's row and fail that assertion.
func (c *Client) NewChangeSetName(ctx context.Context, envSlug string) (string, error) {
	base := envSlug + "-changeset"
	for i := 0; ; i++ {
		name := base
		if i > 0 {
			name = fmt.Sprintf("%s-%d", base, i)
		}
		existing, err := c.Search(ctx, EntityChangeSet, Filter{Attribute: "name", Operator: "=", Value: name})
		if err != nil {
			return "", err
		}
		if len(existing) == 0 {
			return name, nil
		}
		if i > 500 {
			return "", fmt.Errorf("could not find an unused changeset name for %q", envSlug)
		}
	}
}

// DeploymentNode is one unit of work from generate_local_deployment: one
// RepoInstance, one script, one `hmd ... deploy`. Indivisible.
type DeploymentNode struct {
	InstanceName          string         `json:"instance_name"`
	RepoClassName         string         `json:"repo_class_name"`
	Version               string         `json:"version"`
	RIDNid                string         `json:"rid_nid"`
	Script                string         `json:"script"`
	Dependencies          []string       `json:"dependencies"`
	InstanceConfiguration map[string]any `json:"instance_configuration"`
}

// ApplyChangeSet applies a changeset and returns its ChangeSetDeployment id.
//
// skipAsync stops ms-deployment doing what it does in the cloud -- building an
// Argo Workflow and posting it to the Argo server -- so that the caller drives
// execution instead. It was the local marker, and it is no longer the only
// option: a control plane configured with HMD_WORKFLOW_RUNNER_URL submits to
// the DAG runner itself, which is the inversion this whole path exists for.
//
// It stays selectable rather than being deleted. The service-submits path
// routes through an async self-invoking Lambda, and until that is proven under
// Floci the working path has to remain reachable.
func (c *Client) ApplyChangeSet(ctx context.Context, changeSetName, deploymentSetName string, skipAsync bool) (string, error) {
	result, err := c.APIOp(ctx, "apply_changeset", map[string]any{
		"change_set_name":     changeSetName,
		"deployment_set_name": deploymentSetName,
		"skip_async":          skipAsync,
	})
	if err != nil {
		return "", err
	}
	nid, _ := result["csd_nid"].(string)
	if nid == "" {
		return "", fmt.Errorf("apply_changeset returned no csd_nid")
	}
	return nid, nil
}

// GenerateLocalDeployment returns the deployment nodes in DAG order.
//
// The order comes from ms-deployment's own Kahn's-algorithm traversal of the
// reduced RepoInstanceReqRepoInstance edges, which is what makes execution
// order correct. It is a different sort from the one the caller does before
// applying, and the two must not be conflated.
func (c *Client) GenerateLocalDeployment(ctx context.Context, csdNid string) ([]DeploymentNode, error) {
	body, err := c.APIOpRaw(ctx, "generate_local_deployment/"+csdNid, nil)
	if err != nil {
		return nil, err
	}
	var manifest struct {
		Nodes []DeploymentNode `json:"nodes"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		return nil, fmt.Errorf("decoding the deployment manifest: %w", err)
	}
	return manifest.Nodes, nil
}
