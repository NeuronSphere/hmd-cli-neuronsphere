package controlplane

import (
	"errors"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/compose"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
)

func TestFlociNeedsRecreate(t *testing.T) {
	t.Parallel()

	running := []compose.Result{
		{Service: "proxy", Action: compose.ActionRunning},
		{Service: compose.FlociService, Action: compose.ActionRunning},
	}
	probeErr := errors.New("container is restarting")

	for _, tt := range []struct {
		name     string
		results  []compose.Result
		token    string
		verify   func() (bool, error)
		want     bool
		wantErr  bool
		verified bool
	}{
		{
			// The reported failure: left alone by the reconciler, and reading
			// a directory the host replaced underneath it.
			name: "left running and looking elsewhere", results: running, token: "t",
			verify: func() (bool, error) { return false, nil }, want: true, verified: true,
		},
		{
			name: "left running and still looking here", results: running, token: "t",
			verify: func() (bool, error) { return true, nil }, want: false, verified: true,
		},
		{
			// Every other action resolved the bind in the act, so there is
			// nothing to ask and an exec spent asking could only be wrong.
			name: "created", results: []compose.Result{{Service: compose.FlociService, Action: compose.ActionCreated}}, token: "t",
			want: false,
		},
		{
			name: "recreated", results: []compose.Result{{Service: compose.FlociService, Action: compose.ActionRecreated}}, token: "t",
			want: false,
		},
		{
			name: "started from stopped", results: []compose.Result{{Service: compose.FlociService, Action: compose.ActionStarted}}, token: "t",
			want: false,
		},
		{
			// Recreating a working Floci costs the databases it spawned, so
			// "could not tell" must never be read as "yes".
			name: "the probe could not reach an answer", results: running, token: "t",
			verify: func() (bool, error) { return false, probeErr }, want: false, wantErr: true, verified: true,
		},
		{
			// No token means the stamp was never written; there is nothing to
			// compare against and a mismatch would be meaningless.
			name: "unstamped", results: running, token: "",
			want: false,
		},
		{
			name: "floci was not in this project", results: []compose.Result{{Service: "proxy", Action: compose.ActionRunning}}, token: "t",
			want: false,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			verify := func() (bool, error) {
				called = true
				if tt.verify == nil {
					t.Fatal("verify was called when there was nothing to ask")
				}
				return tt.verify()
			}
			_, got, err := flociNeedsRecreate(tt.results, tt.token, verify)
			if got != tt.want {
				t.Errorf("recreate = %v, want %v", got, tt.want)
			}
			if (err != nil) != tt.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if called != tt.verified {
				t.Errorf("verify called = %v, want %v", called, tt.verified)
			}
		})
	}
}

// The index is what lets the caller replace the line it already printed, so a
// recreate is not reported as "running".
func TestFlociNeedsRecreateReportsWhichResultIsFlocis(t *testing.T) {
	t.Parallel()

	results := []compose.Result{
		{Service: "proxy", Action: compose.ActionRunning},
		{Service: compose.FlociService, Action: compose.ActionRunning},
		{Service: "dnsd", Action: compose.ActionRunning},
	}
	i, _, _ := flociNeedsRecreate(results, "t", func() (bool, error) { return false, nil })
	if i != 1 {
		t.Errorf("index = %d, want 1", i)
	}
}

// The two shapes differ in whether the bootstrap that follows rebuilds what the
// recreated Floci lost, and the user has to be told which one they are in.
func TestWipedHomeDetailDistinguishesTheSelfRepairingCase(t *testing.T) {
	t.Parallel()

	synthesized := wipedHomeDetail(&registry.Registry{Synthesized: true})
	if !strings.Contains(synthesized, "bootstrapped from scratch") {
		t.Errorf("a synthesized registry must say it rebuilds itself: %q", synthesized)
	}
	kept := wipedHomeDetail(&registry.Registry{})
	if !strings.Contains(kept, "nsctl control-plane reset") {
		t.Errorf("a surviving registry must name the repair: %q", kept)
	}
}
