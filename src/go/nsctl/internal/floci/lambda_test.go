package floci

import "testing"

// The runner URL is the variable Phase 4 claimed to have wired and had not:
// hmd-ms-deployment reads it, nothing set it, and HMD_LOCAL_SERVICE_SUBMIT=1
// therefore waited two minutes for a workflow nobody was asked to submit.
func TestOnlyMSDeploymentGetsAWorkflowRunner(t *testing.T) {
	t.Parallel()

	opts := ServiceEnvOpts{WorkflowRunnerURL: "http://hmd_nsrunner:8080"}
	names := Names{Region: "reg1", CustomerCode: "none", DeploymentID: "cp"}

	env := ServiceEnv(MSDeploymentRepoClass, "0.5", names, "hmd_db", nil, opts)
	if got := env[WorkflowRunnerURLVar]; got != opts.WorkflowRunnerURL {
		t.Errorf("%s = %q, want %q", WorkflowRunnerURLVar, got, opts.WorkflowRunnerURL)
	}

	// Every other foundation service shares this function and has no use for a
	// runner. Deciding that here rather than at the call sites is the whole
	// reason ServiceEnv takes options at all.
	for _, repoClass := range []string{"hmd-ms-naming", "hmd-ms-artifact-lib", "hmd-ms-dbaccount"} {
		other := ServiceEnv(repoClass, "0.5", names, "hmd_db", nil, opts)
		if got, ok := other[WorkflowRunnerURLVar]; ok {
			t.Errorf("%s was given %s=%q", repoClass, WorkflowRunnerURLVar, got)
		}
	}
}

// An unset variable is not a neutral default -- it is what makes
// hmd-ms-deployment run the deployment in-process. Writing an empty string, or
// an address for a container that is not running, would replace a working
// fallback with a deploy that fails.
func TestNoRunnerMeansNoVariableAtAll(t *testing.T) {
	t.Parallel()

	env := ServiceEnv(MSDeploymentRepoClass, "0.5", Names{}, "hmd_db", nil, ServiceEnvOpts{})
	if got, ok := env[WorkflowRunnerURLVar]; ok {
		t.Errorf("%s = %q with no runner enabled; want it absent", WorkflowRunnerURLVar, got)
	}
}
