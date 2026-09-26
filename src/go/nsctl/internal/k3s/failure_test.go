package k3s

import (
	"context"
	"strings"
	"testing"
)

func contextFrom(t *testing.T, out string, err error) string {
	t.Helper()
	o := &Operators{Kube: &Kube{Container: "c", Run: func(_ context.Context, _ ...string) ([]byte, []byte, error) {
		return []byte(out), nil, err
	}}}
	return o.FailureContext(context.Background())
}

func TestWarningsAndUnreadySecretsAreBothReported(t *testing.T) {
	t.Parallel()
	got := contextFrom(t, `NS OBJECT REASON MESSAGE
redis-local redis-0 FailedMount secret "redis-local-hmd-inf-redis-secret" not found
%%SPLIT%%
NS NAME READY REASON
redis-local redis-secret False could not get secret data: lookup neuronsphere: i/o timeout
`, nil)
	for _, want := range []string{"redis-local-hmd-inf-redis-secret", "i/o timeout", "not ready", "nsctl doctor"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// A header with no rows under it says nothing, and printing it would bury the
// node's real output under two empty tables.
func TestHeadersWithNoRowsAreSuppressed(t *testing.T) {
	t.Parallel()
	got := contextFrom(t, "NS OBJECT REASON MESSAGE\n%%SPLIT%%\nNS NAME READY REASON\n", nil)
	if got != "" {
		t.Errorf("empty tables were printed:\n%s", got)
	}
}

func TestAClusterThatWillNotAnswerAddsNothing(t *testing.T) {
	t.Parallel()
	if got := contextFrom(t, "", context.DeadlineExceeded); got != "" {
		t.Errorf("an unreachable cluster produced output: %s", got)
	}
}

func TestOnlyWarningsStillReport(t *testing.T) {
	t.Parallel()
	got := contextFrom(t, "NS OBJECT REASON MESSAGE\nkube-system x BackOff crashlooping\n%%SPLIT%%\nNS NAME READY REASON\n", nil)
	if !strings.Contains(got, "crashlooping") {
		t.Errorf("warnings dropped when no ExternalSecret was unready:\n%s", got)
	}
	if strings.Contains(got, "not ready") {
		t.Errorf("an empty secrets table was printed:\n%s", got)
	}
}
