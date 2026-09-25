package floci

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/hmdenv"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/repoclass"
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
	if problems := EnsureBackendImages(context.Background(), f, "floci", nil, RegistryHint{}); len(problems) > 0 {
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
	problems := EnsureBackendImages(context.Background(), f, "floci", nil, RegistryHint{})
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
	if problems := EnsureBackendImages(context.Background(), f, "floci", nil, RegistryHint{}); len(problems) > 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}
	if len(f.pulled) != 0 {
		t.Errorf("a cached image was pulled anyway: %v", f.pulled)
	}
}

// The incident this was written for. A first-run user had
// HMD_LOCAL_NS_CONTAINER_REGISTRY inherited from an older install, pointing at
// an org that publishes no 0.3.12 of the database image. The message named the
// variable and nothing else, so there was no way to tell it was set at all --
// and they deleted HMD_HOME instead, which cannot clear a shell variable and
// cost them a Floci bound to a directory that no longer existed.
func TestAPullFailureNamesTheOverridesValueAndWhereItWasSet(t *testing.T) {
	t.Parallel()

	ref := "ghcr.io/neuronsphere/hmd-postgres-base:0.3.12"
	f := &fakeImages{
		env:     map[string]string{rdsImageEnv: ref},
		present: map[string]bool{},
		failing: map[string]bool{ref: true},
	}
	shell := map[string]string{RegistryEnv: "ghcr.io/neuronsphere"}
	problems := EnsureBackendImages(context.Background(), f, "floci", nil,
		RegistryHint{Home: t.TempDir(), Lookup: func(k string) string { return shell[k] }})
	if len(problems) != 1 {
		t.Fatalf("want one problem, got %v", problems)
	}
	for _, want := range []string{
		ref,                         // what failed
		"ghcr.io/neuronsphere",      // what the variable is set to
		hmdenv.OriginShell,          // where it was set
		repoclass.PublishedRegistry, // what nsctl would have used
		"Do not delete $HMD_HOME",   // the recovery that makes it worse
		"nsctl control-plane stop",  // the one that does not
	} {
		if !strings.Contains(problems[0], want) {
			t.Errorf("the message does not name %q:\n%s", want, problems[0])
		}
	}
}

// An override set in this home's hmd.env is named by path, not as "your shell":
// the remedy is a different file, and saying the wrong one sends the reader
// looking where the value is not. Deleting HMD_HOME *would* clear this one, so
// the warning against doing so is not repeated here.
func TestAPullFailureNamesTheHmdEnvFileWhenThatIsWhereItWasSet(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hmdenv.Path(home), []byte(RegistryEnv+"=ghcr.io/neuronsphere\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ref := "ghcr.io/neuronsphere/hmd-postgres-base:0.3.12"
	f := &fakeImages{
		env:     map[string]string{rdsImageEnv: ref},
		present: map[string]bool{},
		failing: map[string]bool{ref: true},
	}
	problems := EnsureBackendImages(context.Background(), f, "floci", nil, RegistryHint{Home: home})
	if len(problems) != 1 {
		t.Fatalf("want one problem, got %v", problems)
	}
	if !strings.Contains(problems[0], hmdenv.Path(home)) {
		t.Errorf("the message does not name the file it was set in:\n%s", problems[0])
	}
	if strings.Contains(problems[0], hmdenv.OriginShell) {
		t.Errorf("the message blames the shell for a value in hmd.env:\n%s", problems[0])
	}
}
