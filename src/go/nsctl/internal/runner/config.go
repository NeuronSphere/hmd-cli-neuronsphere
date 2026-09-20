package runner

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
)

// heredocOpen is how the generated script hands `hmd deploy` its resolved
// configuration: `--config-file STDIN <<'EOF'` at the end of the command line,
// the JSON, then a line that is only `EOF`. Quoted, so the shell expands
// nothing in the body -- and so nothing here has to un-expand it.
const (
	heredocOpen  = "--config-file STDIN <<'EOF'"
	heredocClose = "EOF"
)

// ExtractConfig reads the resolved configuration out of a generated deploy
// script, or reports false when the script carries none.
//
// A service-generated node carries no instance_configuration of its own:
// generate_local_deployment emits the identity, the script and the edges, and
// the configuration -- the merged defaults, the instance's values and every
// dependency role with its NERD0006 hmd_resources -- exists only inside the
// script, as the heredoc `hmd deploy --config-file STDIN` reads. A node that
// runs that script gets it for free. A foreign node does not run that script,
// so the configuration is lifted out of it here and handed over as
// HMD_INSTANCE_CONFIG instead.
//
// Only the first heredoc is read: the generated script has exactly one, and a
// node with several is not a shape this runner produces.
func ExtractConfig(script string) (map[string]any, bool) {
	lines := strings.Split(script, "\n")
	start := -1
	for i, line := range lines {
		if strings.HasSuffix(strings.TrimRight(line, " \t"), heredocOpen) {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return nil, false
	}
	end := -1
	for i := start; i < len(lines); i++ {
		if strings.TrimRight(lines[i], " \t\r") == heredocClose {
			end = i
			break
		}
	}
	if end < 0 {
		return nil, false
	}
	var config map[string]any
	if err := json.Unmarshal([]byte(strings.Join(lines[start:end], "\n")), &config); err != nil {
		return nil, false
	}
	return config, true
}

// resourceFetcher answers a read-only deployment-service operation.
// *msdeploy.Client satisfies it; tests use a fake.
type resourceFetcher interface {
	APIOpGet(ctx context.Context, operation string) ([]byte, error)
}

// ResolveResourceRefs turns every hmd_resource_ref pointer in a configuration
// into the hmd_resources it points at, in place.
//
// A dependency deployed in the same ChangeSet has no outputs when the config is
// generated, so ms-deployment stamps its role with a pointer to the
// RepoInstanceDeployment instead, and hmd-cli-deploy resolves the pointer
// immediately before dispatching its tools
// (hmd_cli_deploy/controller.py:_resolve_dependency_resource_outputs). A
// foreign node replaces that command and therefore that step, so the same
// resolution is done here, the same way: every role in the dependency tree,
// object-valued or list-valued, nested to any depth; each deployment fetched
// once however many roles point at it; the outputs appended to hmd_resources
// so a consumer sees one shape whether the role was baked or resolved; and the
// pointer left where it was. Best effort, exactly as the Python is: a fetch
// that fails is a warning and an unresolved role, not a failed deploy.
func ResolveResourceRefs(ctx context.Context, config map[string]any, fetch resourceFetcher, warn func(string, ...any)) {
	if config == nil || fetch == nil {
		return
	}
	if warn == nil {
		warn = func(string, ...any) {}
	}
	pending := map[string][]map[string]any{}
	var order []string
	for _, role := range dependencyRoles(config) {
		ref, _ := role["hmd_resource_ref"].(map[string]any)
		rid, _ := ref["repo_instance_deployment_id"].(string)
		if rid == "" {
			continue
		}
		if _, seen := pending[rid]; !seen {
			order = append(order, rid)
		}
		pending[rid] = append(pending[rid], role)
	}
	for _, rid := range order {
		body, err := fetch.APIOpGet(ctx, "get_deployment_resources/"+rid)
		if err != nil {
			warn("could not fetch the resources deployment %s produced; its dependents deploy without them: %v", rid, err)
			continue
		}
		var resources []any
		if err := json.Unmarshal(body, &resources); err != nil {
			warn("could not read the resources deployment %s produced: %v", rid, err)
			continue
		}
		for _, role := range pending[rid] {
			existing, _ := role["hmd_resources"].([]any)
			role["hmd_resources"] = append(existing, resources...)
		}
	}
}

// dependencyRoles is every role configuration in the tree under a config's
// `dependencies`, depth first: hmd_cli_deploy's _dependency_role_configs.
func dependencyRoles(config map[string]any) []map[string]any {
	var roles []map[string]any
	stack := []map[string]any{config}
	for len(stack) > 0 {
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		deps, _ := node["dependencies"].(map[string]any)
		for _, value := range deps {
			items, ok := value.([]any)
			if !ok {
				items = []any{value}
			}
			for _, item := range items {
				if role, ok := item.(map[string]any); ok {
					roles = append(roles, role)
					stack = append(stack, role)
				}
			}
		}
	}
	return roles
}

// foreignConfig is the HMD_INSTANCE_CONFIG a foreign node receives: the
// script's resolved configuration with its pointers resolved, falling back to
// whatever the node itself carries -- which is what a bootstrap node has and a
// service-generated node lacks.
func (r *Runner) foreignConfig(ctx context.Context, node msdeploy.DeploymentNode) []byte {
	config, ok := ExtractConfig(node.Script)
	if !ok {
		config = node.InstanceConfiguration
	}
	if config == nil {
		return []byte("{}")
	}
	if r.Client != nil {
		ResolveResourceRefs(ctx, config, r.Client, r.warn)
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		r.warn("could not encode %s's configuration: %v", node.InstanceName, err)
		return []byte("{}")
	}
	return encoded
}
