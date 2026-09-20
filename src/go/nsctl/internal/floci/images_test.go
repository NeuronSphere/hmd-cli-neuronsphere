package floci

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeImages struct {
	env     map[string]string
	present map[string]bool
	pulled  []string
	failing map[string]bool
}

func (f *fakeImages) ContainerEnv(context.Context, string) map[string]string { return f.env }
func (f *fakeImages) ImagePresent(_ context.Context, ref string) bool        { return f.present[ref] }
func (f *fakeImages) PullImage(_ context.Context, ref string) error {
	f.pulled = append(f.pulled, ref)
	if f.failing[ref] {
		return errors.New("not found")
	}
	f.present[ref] = true
	return nil
}

// The guard this replaced refused whenever an image was not cached. On the
// machine this proposal is about -- one whose only prerequisite is Docker --
// nothing is cached, so it turned every genuinely fresh start into a hard stop
// at the control-plane database. It did exactly that on the first cold start
// run with no HMD_REPO_HOME.
func TestAnUncachedBackendImageIsPulledRatherThanRefused(t *testing.T) {
	t.Parallel()

	f := &fakeImages{
		env: map[string]string{
			rdsImageEnv:     "reg/hmd-postgres-base:stable",
			neptuneImageEnv: "reg/hmd-img-gremlin-server:0.3.5",
		},
		present: map[string]bool{},
		failing: map[string]bool{},
	}
	if problems := EnsureBackendImages(context.Background(), f, "floci", nil); len(problems) > 0 {
		t.Fatalf("a pullable image must not stop a start: %v", problems)
	}
	if len(f.pulled) != 2 {
		t.Errorf("pulled %v, want both references", f.pulled)
	}
}

// The refusal survives for the case it was actually written for: a reference
// that resolves nowhere. Floci answers CreateDBInstance with a 404, leaves the
// instance `failed`, and Terraform polls "Still creating..." forever -- so
// stopping in seconds beats starting and hanging.
func TestAnUnpullableBackendImageStillStopsTheStart(t *testing.T) {
	t.Parallel()

	ref := "ghcr.io/hmdlabs/hmd-postgres-base:stable"
	f := &fakeImages{
		env:     map[string]string{rdsImageEnv: ref},
		present: map[string]bool{},
		failing: map[string]bool{ref: true},
	}
	problems := EnsureBackendImages(context.Background(), f, "floci", nil)
	if len(problems) != 1 {
		t.Fatalf("want one problem, got %v", problems)
	}
	// The registry override is named because it is the likeliest cause and was
	// the actual one: hmdlabs publishes no `stable` for this image, while the
	// default registry does.
	for _, want := range []string{ref, rdsImageEnv, "HMD_LOCAL_NS_CONTAINER_REGISTRY", "HMD_POSTGRES_BASE_VERSION"} {
		if !strings.Contains(problems[0], want) {
			t.Errorf("the message does not name %q: %s", want, problems[0])
		}
	}
}

// A cached image is proof enough; no registry round trip, so a warm start does
// not depend on the network.
func TestACachedBackendImageIsNotPulledAgain(t *testing.T) {
	t.Parallel()

	ref := "reg/hmd-postgres-base:0.2.11"
	f := &fakeImages{
		env:     map[string]string{rdsImageEnv: ref},
		present: map[string]bool{ref: true},
		failing: map[string]bool{},
	}
	if problems := EnsureBackendImages(context.Background(), f, "floci", nil); len(problems) > 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}
	if len(f.pulled) != 0 {
		t.Errorf("a cached image was pulled anyway: %v", f.pulled)
	}
}
