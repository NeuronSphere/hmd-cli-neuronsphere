package container

import "testing"

// What is worth asking again, and what only gets slower for being asked.
func TestRetryablePull(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  string
		want bool
	}{
		// The failure that cost a whole control-plane start on a cold engine.
		{"ghcr token timeout", `Error response from daemon: failed to resolve reference "ghcr.io/hmdlabs/x:1": failed to authorize: failed to fetch anonymous token: Get "https://ghcr.io/token?scope=repository%3Ax%3Apull&service=ghcr.io": dial tcp 140.82.114.34:443: i/o timeout`, true},
		{"connection refused", "dial tcp 1.2.3.4:443: connect: connection refused", true},
		{"connection reset", "read tcp: connection reset by peer", true},
		{"dns", `dial tcp: lookup ghcr.io: no such host`, true},
		{"tls handshake", "net/http: TLS handshake timeout", true},
		{"registry 503", "received unexpected HTTP status: 503 Service Unavailable", true},
		{"rate limited", "toomanyrequests: Too Many Requests", true},

		// Permanent: the answer will not change.
		{"absent tag", "manifest unknown: manifest tagged by \"9.9\" is not found", false},
		{"absent repo", "Error response from daemon: repository does not exist or may require 'docker login': denied", false},
		{"bad credential", "unauthorized: authentication required", false},
		{"malformed ref", "invalid reference format", false},

		// Unrecognised is not retried: a failure nobody has classified is more
		// likely permanent than not, and retrying it hides it for longer.
		{"unknown", "something nobody has seen before", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := retryablePull(tc.msg); got != tc.want {
				t.Fatalf("retryablePull(%q) = %v, want %v", tc.msg, got, tc.want)
			}
		})
	}
}

// A permanent failure must not be retried even though it is a failure: the
// classification is what keeps a missing image fast.
func TestRetryablePullPrefersPermanenceOverTransport(t *testing.T) {
	// Carries both a permanent word and a transient one; permanent wins.
	msg := "manifest unknown after i/o timeout"
	if retryablePull(msg) {
		t.Fatalf("retryablePull(%q) = true, want false", msg)
	}
}
