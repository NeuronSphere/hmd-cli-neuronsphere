package environment

import (
	"context"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// A plan against an unreachable service says which service and how to start
// it -- the same refusal Apply gives, since ComputePlan needs the same
// connection before it can do anything else.
func TestComputePlanRefusesWhenMSDeploymentIsUnreachable(t *testing.T) {
	t.Parallel()

	opts := testOptions(registryHome(t), map[string]string{
		"HMD_LOCAL_MS_DEPLOYMENT_URL": "http://127.0.0.1:1/hmd_ms_deployment",
	})
	_, err := ComputePlan(context.Background(), opts, "local")
	if err == nil {
		t.Fatal("ComputePlan succeeded against an unreachable service")
	}
	if !strings.Contains(err.Error(), "control-plane start") {
		t.Errorf("the error does not name the remedy: %v", err)
	}
}

func TestComputePlanOnAnUnknownEnvironmentIsAUsageError(t *testing.T) {
	t.Parallel()

	opts := testOptions(registryHome(t), nil)
	_, err := ComputePlan(context.Background(), opts, "nope")
	if err == nil {
		t.Fatal("an unknown environment was accepted")
	}
	if got := nserr.CodeOf(err); got != nserr.Usage {
		t.Errorf("exit code = %d, want %d", got, nserr.Usage)
	}
	if !strings.Contains(err.Error(), "local") {
		t.Errorf("the error does not name what exists: %v", err)
	}
}
