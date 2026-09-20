package repoclass

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// RoleFact is what a repo class declares about one of its dependency roles.
//
// It is read from the class's own BACON manifest rather than from a deployed
// edge, because that is where the declaration lives: a RepoInstance's
// dependencies record which instance fills a role and nothing about whether the
// role had to be filled at all.
type RoleFact struct {
	Role string
	// RepoClassName is the producer the block names. For a resource-typed role
	// this is a fallback suggestion rather than an answer, per BACON.
	RepoClassName string
	// Required is deploy.dependencies.<role>.required. hmd-ms-deployment fails
	// a whole ChangeSet on a required role nothing fills and accepts an
	// optional one silently, so this is the only field that decides whether a
	// role must be followed.
	Required bool
	// ResourceType is "<namespace>/<definition>" when the block declares a
	// resource, and empty for a name-only role.
	//
	// The distinction matters beyond bookkeeping: a resource-typed role is
	// validated against what the producing instance really produces, while a
	// name-only role is validated only for presence. Only the second can be
	// bound to something that does not produce it.
	ResourceType string
}

// ResourceTyped reports whether the role asks for a resource definition rather
// than naming a producer.
func (f RoleFact) ResourceTyped() bool { return f.ResourceType != "" }

// RolesFrom reads a deploy.dependencies mapping.
//
// It takes the already-decoded map rather than re-reading the file, so the
// roles and the dependencies registered with add_repo_class_version are the
// same bytes read once -- a second decode of the same field is a second place
// for the two to disagree.
//
// A block that is not an object is skipped rather than refused: this is a
// predicate over a manifest somebody else published, and failing the whole
// import over a shape nsctl does not model would be a worse answer than
// treating that one role conservatively.
func RolesFrom(deps map[string]any) (map[string]RoleFact, error) {
	out := make(map[string]RoleFact, len(deps))
	var problems []string

	roles := make([]string, 0, len(deps))
	for role := range deps {
		roles = append(roles, role)
	}
	sort.Strings(roles)

	for _, role := range roles {
		block, ok := deps[role].(map[string]any)
		if !ok {
			continue
		}
		required, err := Truthy(block["required"])
		if err != nil {
			problems = append(problems, fmt.Sprintf("deploy.dependencies.%s: %v", role, err))
			continue
		}
		fact := RoleFact{Role: role, Required: required}
		if name, ok := block["repo_class_name"].(string); ok {
			fact.RepoClassName = name
		}
		if resource, ok := block["resource"].(map[string]any); ok {
			namespace, _ := resource["resource_namespace"].(string)
			definition, _ := resource["resource_definition_name"].(string)
			switch {
			case namespace != "" && definition != "":
				fact.ResourceType = namespace + "/" + definition
			case definition != "":
				fact.ResourceType = definition
			case namespace != "":
				fact.ResourceType = namespace
			}
		}
		out[role] = fact
	}

	if len(problems) > 0 {
		return out, fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return out, nil
}

// RolesInDir reads the roles a repo class declares, from a directory holding
// its meta-data/manifest.json -- an unpacked artifact, or a working tree.
//
// A missing manifest is (nil, nil), not an error: a caller that cannot tell
// whether a role is required must close over it, and saying so is its job
// rather than this function's.
func RolesInDir(dir string) (map[string]RoleFact, error) {
	if dir == "" {
		return nil, nil
	}
	data, err := os.ReadFile(filepath.Join(dir, "meta-data", "manifest.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var m struct {
		Deploy struct {
			Dependencies map[string]any `json:"dependencies"`
		} `json:"deploy"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing the manifest: %w", err)
	}
	if m.Deploy.Dependencies == nil {
		// A manifest that declares no dependencies is readable and has no
		// roles, which is a different answer from "unreadable" and must not
		// collapse into it.
		return map[string]RoleFact{}, nil
	}
	return RolesFrom(m.Deploy.Dependencies)
}

// Truthy reads BACON's `required`, which every one of the 517 dependency blocks
// in a full workspace writes as the *string* "true" or "false". A bool is
// accepted too, because nothing in the schema forbids one.
//
// Absent is false. That is the schema's own default for an optional key and it
// is the safe direction here only because every caller that acts on a false
// answer reports doing so.
func Truthy(v any) (bool, error) {
	switch t := v.(type) {
	case nil:
		return false, nil
	case bool:
		return t, nil
	case string:
		b, err := strconv.ParseBool(strings.TrimSpace(t))
		if err != nil {
			return false, fmt.Errorf("'required' is %q, which is neither true nor false", t)
		}
		return b, nil
	default:
		return false, fmt.Errorf("'required' must be true or false, got %v", v)
	}
}
