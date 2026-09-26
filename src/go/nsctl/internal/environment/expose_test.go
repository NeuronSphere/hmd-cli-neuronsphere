package environment

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The routing that reads what a deploy created must run after the deploy, not
// only before it.
//
// This is asserted against the source because the property is structural: it is
// *where* the call sits relative to Apply, which no amount of faking the engine
// can observe. Running it only before the deploy was a defect with three faces
// on a first run -- UIs advertised at a domain that resolves nowhere, Trino
// answering 404 on every path, and nothing on its host port -- and all three
// healed on a second `nsctl env start` that nobody knew to run, which is what
// made it expensive to diagnose rather than merely broken.
func TestTheDeployDependentRoutingRunsAfterTheDeploy(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile("environment.go")
	if err != nil {
		t.Fatalf("reading environment.go: %v", err)
	}
	body := string(src)

	const call = "exposeDeployedWorkloads(ctx,"
	if n := strings.Count(body, call); n != 2 {
		t.Errorf("exposeDeployedWorkloads is called %d times, want 2 (once before the deploy for a warm start, once after it for a first run)", n)
	}

	// The post-deploy one is the one that matters, and it is the one a later
	// edit is most likely to drop.
	after := funcBody(t, body, "func refreshAfterDeploy(")
	if !strings.Contains(after, call) {
		t.Error("refreshAfterDeploy does not re-run exposeDeployedWorkloads; a first run's UIs will be " +
			"advertised at hmd-cli-helm's hardcoded .local.neuronsphere.io and Trino will have no host port")
	}
	before := funcBody(t, body, "func startCluster(")
	if !strings.Contains(before, call) {
		t.Error("startCluster no longer exposes deployed workloads; a warm start would stop re-wiring them")
	}
}

// funcBody returns the source from a function's signature to the next
// top-level declaration.
func funcBody(t *testing.T, src, signature string) string {
	t.Helper()
	i := strings.Index(src, signature)
	if i < 0 {
		t.Fatalf("could not find %q", signature)
	}
	rest := src[i+len(signature):]
	if j := regexp.MustCompile(`(?m)^func `).FindStringIndex(rest); j != nil {
		return rest[:j[0]]
	}
	return rest
}
