package msdeploy

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// BOMEntry is one instance as the service's own Bill of Materials describes it.
//
// Field for field what DeployBomCreator.create_node_bom emits, including the
// conditional keys. Those conditions are why so much of this is omitempty: an
// image_only entry carries no configuration and no dependencies at all, and a
// decoder that invented empty ones would turn "this instance deploys an image"
// into "this instance has no dependencies", which is a different claim.
//
// internal/bom.Entry is the same six core keys going the other way -- a BOM
// read out of one environment is very nearly a change set definition applicable
// to another, because they are one schema. The two types stay separate because
// internal/bom imports this package and the dependency cannot run both ways;
// a test pins the shared tags so the duplication cannot drift.
type BOMEntry struct {
	RepoInstanceName string `json:"repo_instance_name"`
	RepoClassName    string `json:"repo_class_name"`
	RepoClassVersion string `json:"repo_class_version"`
	DeploymentID     string `json:"deployment_id"`
	Status           string `json:"status"`

	HMDRegion string `json:"hmd_region,omitempty"`
	// AutoDeploy is `any` because the entity types it as a string holding
	// "true"/"false" while the BOM copies whatever is stored. Decoding it as a
	// bool fails on the string spelling, and nothing here needs its value.
	AutoDeploy any  `json:"auto_deploy,omitempty"`
	ImageOnly  bool `json:"image_only,omitempty"`

	InstanceConfiguration map[string]any `json:"instance_configuration,omitempty"`
	// Dependencies maps a role to the instance filling it, spelled as a bare
	// string for one target and as a list for several. Both are valid in a
	// manifest too; Targets normalises them.
	Dependencies       map[string]any `json:"dependencies,omitempty"`
	ConfigArtifactSpec string         `json:"config_artifact_spec,omitempty"`
}

// Targets is the instance names filling a role, whichever way it was spelled.
func (e BOMEntry) Targets(role string) []string {
	return dependencyTargets(e.Dependencies[role])
}

// Roles are the dependency roles this entry declares, sorted.
func (e BOMEntry) Roles() []string {
	roles := make([]string, 0, len(e.Dependencies))
	for role := range e.Dependencies {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	return roles
}

func dependencyTargets(value any) []string {
	switch v := value.(type) {
	case string:
		if v = strings.TrimSpace(v); v != "" {
			return []string{v}
		}
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	case []string:
		return v
	}
	return nil
}

// DeploymentBOM reads an environment's Bill of Materials.
//
// One request. The service traverses the deployment DAG itself and caches the
// result in Redis, which is why this is not EnvironmentInstances pointed at a
// different host: that function issues ten unfiltered whole-table searches and
// joins them here, which is fine against a local graph holding one environment
// and is not fine against a cloud graph holding every environment a tenant has
// ever deployed.
//
// Three properties of the answer that a caller must not re-derive:
//
//   - It is in deployment-DAG order.
//   - It contains only instances whose current deployment is DEPLOYED or
//     FAILED. Everything else is dropped server side, so this is not a listing
//     of everything an environment declares.
//   - The service picks an instance's current deployment by the `current`
//     attribute on the edge. EnvironmentInstances picks the newest by _created.
//     The two agree most of the time, which is what makes the disagreement hard
//     to notice; a cloud read uses the server's answer by construction.
func (c *Client) DeploymentBOM(ctx context.Context, envType string) ([]BOMEntry, error) {
	body, err := c.APIOpGet(ctx, "get_deployment_bom/"+envType)
	if err != nil {
		return nil, err
	}
	var bom []BOMEntry
	if err := json.Unmarshal(body, &bom); err != nil {
		return nil, fmt.Errorf("decoding the BOM for %q: %w", envType, err)
	}
	return bom, nil
}

// Environment is one row of the environment entity.
//
// Type is the slug, and it is the business id: get_valid_environment resolves
// an environment by `type` and asserts exactly one match, so this is the name
// every other operation takes.
type Environment struct {
	Type          string
	AccountNumber string
	Region        string
}

// Environments lists every environment the service knows, in one unfiltered
// search -- the same idiom internal/librarian uses for Repos, and the empty
// body Filter's MarshalJSON already produces.
//
// Deliberately without an instance count per environment. No field carries one,
// so it would mean one BOM fetch each: an N+1 hidden behind a listing, against
// the one operation that is expensive.
func (c *Client) Environments(ctx context.Context) ([]Environment, error) {
	rows, err := c.Search(ctx, EntityEnvironment, Filter{})
	if err != nil {
		return nil, err
	}
	out := make([]Environment, 0, len(rows))
	for _, row := range rows {
		slug := stringField(row, "type")
		if slug == "" {
			continue
		}
		out = append(out, Environment{
			Type:          slug,
			AccountNumber: stringField(row, "account_number"),
			Region:        stringField(row, "hmd_region"),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out, nil
}

// EnvironmentTypes is just the slugs, for an error that has to say what exists.
func EnvironmentTypes(envs []Environment) []string {
	types := make([]string, 0, len(envs))
	for _, e := range envs {
		types = append(types, e.Type)
	}
	return types
}
