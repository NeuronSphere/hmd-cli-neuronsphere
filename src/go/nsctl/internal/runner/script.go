package runner

import (
	"encoding/json"
	"fmt"
)

// ScriptParams is what one node's deploy script needs to say.
type ScriptParams struct {
	RepoClass    string
	Instance     string
	Version      string
	DeploymentID string
	// Region defaults to reg1, the local Floci region.
	Region string
	// Environment is the Environment.type the instance belongs to -- the
	// environment slug locally, "local" for the control plane's own nodes.
	Environment string
	// RID is the RepoInstanceDeployment id the deploy reports against. Empty
	// for the control-plane bootstrap, which runs before ms-deployment exists.
	RID    string
	Config map[string]any
}

// DeployScript is the command a projectbuilder node runs.
//
// It is what the service's generate_local_deployment used to render, minus the
// echo markers nothing read: `hmd deploy` with the configuration on STDIN
// through a quoted heredoc (so the shell expands nothing in the JSON), and the
// RepoInstanceDeployment id exported for hmd-cli-deploy to submit produced
// Resources and report against. That export is load-bearing: with
// HMD_ENVIRONMENT=local and no id, hmd-cli-deploy registers a spurious
// `<repo>-local` instance of its own and the resources land there.
//
// No `--register`: the runner records the status itself, after the script.
// Localize adds `--local`; ExtractConfig relies on the exact
// `--config-file STDIN <<'EOF'` suffix.
func DeployScript(p ScriptParams) (string, error) {
	if p.Region == "" {
		p.Region = "reg1"
	}
	if p.Environment == "" {
		p.Environment = "local"
	}
	if p.Config == nil {
		p.Config = map[string]any{}
	}
	body, err := json.MarshalIndent(p.Config, "", "  ")
	if err != nil {
		return "", fmt.Errorf("serialising the configuration for %s: %w", p.Instance, err)
	}
	prefix := ""
	if p.RID != "" {
		prefix = "export HMD_REPO_INSTANCE_DEPLOYMENT_ID=" + p.RID + "\n"
	}
	return fmt.Sprintf(
		"%shmd --debug --repo-name %s --repo-version %s --hmd-region %s deploy "+
			"--instance-name %s --environment %s --deployment-id %s --config-file STDIN <<'EOF'\n%s\nEOF",
		prefix, p.RepoClass, p.Version, p.Region, p.Instance, p.Environment, p.DeploymentID, body), nil
}
