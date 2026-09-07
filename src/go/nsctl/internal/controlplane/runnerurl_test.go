package controlplane

import "testing"

// The in-network address, not the host route. hmd-ms-deployment runs as a
// Lambda inside Floci: http://localhost/nsrunner is hmd_proxy's route from the
// host, and localhost inside that Lambda is its own container.
func TestTheRunnerURLIsInNetworkAndOffByDefault(t *testing.T) {
	t.Parallel()

	off := &Options{Lookup: func(string) string { return "" }}
	if got := RunnerInternalURL(off); got != "" {
		t.Errorf("RunnerInternalURL with no runner = %q, want empty", got)
	}
	if route := RunnerRoute(off); route != nil {
		t.Errorf("RunnerRoute with no runner = %v, want nil", route)
	}

	on := &Options{Lookup: func(k string) string {
		if k == "HMD_LOCAL_NEURONSPHERE_ENABLE_RUNNER" {
			return "true"
		}
		return ""
	}}
	want := "http://hmd_nsrunner:8080"
	if got := RunnerInternalURL(on); got != want {
		t.Errorf("RunnerInternalURL = %q, want %q", got, want)
	}
	// The proxy passthrough and the Lambda's variable must name the same place;
	// they were two literals before.
	if got := RunnerRoute(on)[RunnerRoutePath]; got != want {
		t.Errorf("RunnerRoute[%s] = %q, want %q", RunnerRoutePath, got, want)
	}
}
