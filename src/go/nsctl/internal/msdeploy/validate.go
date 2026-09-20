package msdeploy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// ValidateIssue is one error or warning validate_changeset reports.
type ValidateIssue struct {
	Type     string `json:"type"`
	Instance string `json:"instance"`
	Message  string `json:"message"`
}

// ValidateResult is what /apiop/validate_changeset answers: whether the
// proposed definition would apply, and why not.
//
// It never fails on a schema-valid definition -- an empty or malformed
// definition comes back as {"valid": false, ...} rather than an HTTP error, so
// a caller never needs *Error handling here the way every other operation
// does.
type ValidateResult struct {
	Valid    bool            `json:"valid"`
	Errors   []ValidateIssue `json:"errors"`
	Warnings []ValidateIssue `json:"warnings"`
}

// ValidateChangeSet dry-runs a proposed ChangeSet definition: it checks that
// every referenced RepoClass + version exists, every required role is
// supplied, every named dependency resolves to a sibling change or an
// existing RepoInstance, and the implied dependency graph has no cycles. It
// performs no DB writes.
//
// It does not check that a resource-typed role's bound instance actually
// produces the required resource type -- only that the instance exists. That
// deeper check runs only at apply_changeset time; CandidateWarnings is what
// replicates it for a dry run.
func (c *Client) ValidateChangeSet(ctx context.Context, changes []map[string]any) (*ValidateResult, error) {
	body, err := c.do(ctx, http.MethodPost, c.BaseURL+"/apiop/validate_changeset",
		map[string]any{"changes": changes}, "validate_changeset")
	if err != nil {
		return nil, err
	}
	var out ValidateResult
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("validate_changeset: decoding the response: %w", err)
	}
	return &out, nil
}

// ResourceRef names a ResourceDefinition the way suggest_resource_dependencies
// echoes it back: namespace, name and version, with no identifier.
type ResourceRef struct {
	ResourceNamespace      string `json:"resource_namespace"`
	ResourceDefinitionName string `json:"resource_definition_name"`
	Version                string `json:"version"`
}

// Candidate is one RepoInstance suggest_resource_dependencies found that
// satisfies a resource-typed role.
type Candidate struct {
	Name       string `json:"name"`
	Identifier string `json:"identifier"`
}

// RoleSuggestion is one resource-typed role's requirement and candidates, as
// suggest_resource_dependencies answers it keyed by role.
//
// A role with no resource requirement -- a plain repo_class dependency --
// never appears in the map at all, so ranging over the result is already
// scoped to resource-typed roles; nothing here needs to classify a role
// itself.
type RoleSuggestion struct {
	ResourceDefinition ResourceRef `json:"resource_definition"`
	VersionSpec        string      `json:"version_spec"`
	TagSelector        string      `json:"tag_selector"`
	// Required carries whatever the service sent -- a bool or the string
	// "true"/"false" depending on how the role was declared -- and nothing
	// here needs to parse it.
	Required               any         `json:"required"`
	SuggestedRepoClassName string      `json:"suggested_repo_class_name"`
	Candidates             []Candidate `json:"candidates"`
}

// SuggestResourceDependencies asks, for one RepoClassVersion deployed into one
// environment type, which RepoInstances would satisfy each of its
// resource-typed dependency roles (NERD0004 SPEC0008).
//
// The environment named by envType must already have an Environment entity --
// EnsureEnvironment, called through RegisterCatalog, is what guarantees that
// before this is ever reached.
func (c *Client) SuggestResourceDependencies(ctx context.Context, envType, repoClassVersionID string) (map[string]RoleSuggestion, error) {
	operation := "suggest_resource_dependencies/" + envType +
		"?repo_class_version_id=" + url.QueryEscape(repoClassVersionID)
	body, err := c.do(ctx, http.MethodGet, c.BaseURL+"/apiop/"+operation, nil, "suggest_resource_dependencies")
	if err != nil {
		return nil, err
	}
	var out map[string]RoleSuggestion
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("suggest_resource_dependencies: decoding the response: %w", err)
	}
	return out, nil
}
