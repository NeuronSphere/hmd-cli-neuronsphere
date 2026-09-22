package doctor

import (
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/dockerhost"
)

// SPEC011 decided the rootless socket would be a doctor warning rather than a
// code change. The warning was the whole of that decision, and the how-to tells
// users to expect it.
func TestRootlessSocketWarns(t *testing.T) {
	checks := Options{}.rootlessSocket(dockerhost.Daemon{Rootless: true})
	if len(checks) != 1 {
		t.Fatalf("got %d checks, want 1", len(checks))
	}
	if checks[0].Status != StatusWarn {
		t.Errorf("status = %v, want warn", checks[0].Status)
	}
	if !strings.Contains(checks[0].Detail, "/var/run/docker.sock") {
		t.Errorf("the detail should name the path deploy nodes mount: %q", checks[0].Detail)
	}
	if checks[0].Remedy == "" {
		t.Error("a warning with no remedy is just bad news")
	}
}

// A rootful engine is the ordinary case, and doctor's value is the short list.
func TestRootlessSocketSaysNothingWhenRootful(t *testing.T) {
	if checks := (Options{}).rootlessSocket(dockerhost.Daemon{Rootless: false}); len(checks) != 0 {
		t.Fatalf("got %d checks, want none: %+v", len(checks), checks)
	}
}
