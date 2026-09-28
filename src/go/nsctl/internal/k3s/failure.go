package k3s

import (
	"context"
	"fmt"
	"strings"
)

// failureContextScript collects the two things that explain a chart which timed
// out, and nothing else.
//
// Warning events first, because a pod that cannot start says why there and
// nowhere else. Then any ExternalSecret that is not ready: locally these are how
// every chart gets its credentials, and one that cannot reach the secret store
// takes down every release that mounts what it would have produced -- which
// reads, from outside, as the chart being slow.
const failureContextScript = `
kubectl get events -A --field-selector type=Warning \
  --sort-by=.lastTimestamp \
  -o 'custom-columns=NS:.metadata.namespace,OBJECT:.involvedObject.name,REASON:.reason,MESSAGE:.message' \
  2>/dev/null | tail -15
echo '%%SPLIT%%'
kubectl get externalsecrets.external-secrets.io -A \
  -o 'custom-columns=NS:.metadata.namespace,NAME:.metadata.name,READY:.status.conditions[0].status,REASON:.status.conditions[0].message' \
  2>/dev/null | awk 'NR==1 || $3!="True"' | head -12
`

// FailureContext returns what the cluster knows about a deploy that failed, or
// "" when it knows nothing useful.
//
// This exists because the only thing a user saw when the local cluster could not
// resolve a name was "redis took too long". The helm log said the release had
// been rolled back on a deadline; the actual cause -- a Secret that External
// Secrets never created, because it could not reach the secret store -- was
// visible only in cluster events, and `--atomic` had already deleted the pods
// that would have shown it. A timeout is a symptom, and printing only the
// symptom is what turns a ten-minute problem into an afternoon.
func (o *Operators) FailureContext(ctx context.Context) string {
	stdout, _, err := o.Kube.RunKube(ctx, []byte(failureContextScript), nil)
	if err != nil {
		return ""
	}
	parts := strings.SplitN(string(stdout), "%%SPLIT%%", 2)

	var b strings.Builder
	if events := trimTable(parts[0]); events != "" {
		fmt.Fprintf(&b, "--- the cluster's recent warnings ---\n%s\n", events)
	}
	if len(parts) == 2 {
		if secrets := trimTable(parts[1]); secrets != "" {
			fmt.Fprintf(&b, "--- ExternalSecrets that are not ready ---\n%s\n"+
				"  A chart waiting on one of these will time out and be rolled back. If the reason is a\n"+
				"  name that will not resolve, the cluster cannot reach a Service: see `nsctl doctor`.\n",
				secrets)
		}
	}
	return b.String()
}

// trimTable returns a kubectl table only when it has rows under its header.
func trimTable(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var kept []string
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			kept = append(kept, strings.TrimRight(l, " "))
		}
	}
	// One line is the header alone, which says nothing.
	if len(kept) < 2 {
		return ""
	}
	return strings.Join(kept, "\n")
}
