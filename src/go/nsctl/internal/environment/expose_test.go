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

// `env apply` deploys the workloads that create Ingresses, so it has to re-read
// them too -- and it is the path `stack add --apply` and `nsctl quickstart`
// take, which makes it the one a first run actually goes through.
//
// Asserted structurally for the same reason as the test above: the property is
// *where* the call sits relative to the deploy. A first run through quickstart
// deployed Airflow, Superset and Trino and left all three advertised at
// hmd-cli-helm's hardcoded .local.neuronsphere.io with no vhost on hmd_proxy,
// because Apply never called this.
func TestApplyAlsoRunsTheDeployDependentRouting(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile("apply.go")
	if err != nil {
		t.Fatalf("reading apply.go: %v", err)
	}
	body := string(src)

	apply := funcBody(t, body, "func Apply(")
	if !strings.Contains(apply, "exposeAfterApply(ctx,") {
		t.Error("Apply does not re-run the deploy-dependent routing; a stack deployed by " +
			"`stack add --apply` or `nsctl quickstart` will be unreachable by name until " +
			"someone runs `nsctl env start` again")
	}

	// It is the same routing Start runs, not a second implementation of it.
	// Two ways to wire this is how the two drift.
	after := funcBody(t, body, "func exposeAfterApply(")
	if !strings.Contains(after, "exposeDeployedWorkloads(ctx,") {
		t.Error("exposeAfterApply no longer calls exposeDeployedWorkloads; it must reuse the " +
			"same function Start does rather than reimplement the wiring")
	}

	// The deploy is the point of the command. Routing that fails after a deploy
	// that worked must warn, not turn success into failure.
	if strings.Contains(after, "return nserr") || strings.Contains(after, "return err") {
		t.Error("exposeAfterApply returns an error; it is best-effort and must warn instead, " +
			"or a deploy that succeeded gets reported as failed")
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
