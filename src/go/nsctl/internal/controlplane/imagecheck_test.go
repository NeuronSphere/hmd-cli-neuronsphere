package controlplane

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/doctor"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/floci"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/hmdenv"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/oci"
)

type fakeTags struct {
	tags []string
	err  error
}

func (f *fakeTags) AllTags(context.Context, oci.Ref) ([]string, error) { return f.tags, f.err }

// The reported failure, asked ten seconds in instead of ten minutes in. The
// registry answers, and simply does not publish the pinned tag.
func TestAMissingTagFailsAndNamesWhatTheRepositoryDoesPublish(t *testing.T) {
	t.Parallel()

	f := &fakeTags{tags: []string{"0.1.6", "stable", "0.2.11"}}
	c := imageCheck(context.Background(), f, "ghcr.io/neuronsphere/hmd-postgres-base:0.3.12",
		"ghcr.io/neuronsphere", hmdenv.OriginShell)

	if c.Status != doctor.StatusFail {
		t.Fatalf("status = %v, want %v", c.Status, doctor.StatusFail)
	}
	for _, want := range []string{"0.3.12", "0.2.11", "stable"} {
		if !strings.Contains(c.Detail, want) {
			t.Errorf("the detail does not name %q: %s", want, c.Detail)
		}
	}
	// The remedy has to be the setting the reader can change, and where it is.
	for _, want := range []string{floci.RegistryEnv, "ghcr.io/neuronsphere", hmdenv.OriginShell, "ghcr.io/hmdlabs"} {
		if !strings.Contains(c.Remedy, want) {
			t.Errorf("the remedy does not name %q: %s", want, c.Remedy)
		}
	}
}

func TestATagThatExistsPasses(t *testing.T) {
	t.Parallel()

	f := &fakeTags{tags: []string{"0.3.11", "0.3.12"}}
	c := imageCheck(context.Background(), f, "ghcr.io/hmdlabs/hmd-postgres-base:0.3.12", "", hmdenv.OriginDefault)
	if c.Status != doctor.StatusOK {
		t.Errorf("status = %v (%s), want %v", c.Status, c.Detail, doctor.StatusOK)
	}
}

// Offline is not misconfigured. Failing here would make doctor exit non-zero
// on a correctly configured machine with no network, which is a false negative
// for anything scripting it -- and quickstart gates on doctor.
func TestAnUnreachableRegistryOnlyWarns(t *testing.T) {
	t.Parallel()

	f := &fakeTags{err: errors.New("dial tcp: i/o timeout")}
	c := imageCheck(context.Background(), f, "ghcr.io/hmdlabs/hmd-postgres-base:0.3.12", "", hmdenv.OriginDefault)
	if c.Status != doctor.StatusWarn {
		t.Errorf("status = %v, want %v", c.Status, doctor.StatusWarn)
	}
}

// A registry that answers "you may not look" is a configuration problem, not a
// network one: ghcr.io packages are private by default, so this is what a
// registry holding only some of the platform's images looks like from outside.
func TestARefusedRepositoryFails(t *testing.T) {
	t.Parallel()

	for _, status := range []int{401, 403, 404} {
		f := &fakeTags{err: &oci.Error{Operation: "list tags", Status: status, Source: "anonymous"}}
		c := imageCheck(context.Background(), f, "ghcr.io/neuronsphere/hmd-img-k3s-floci:0.3.4",
			"ghcr.io/neuronsphere", hmdenv.OriginShell)
		if c.Status != doctor.StatusFail {
			t.Errorf("HTTP %d: status = %v, want %v", status, c.Status, doctor.StatusFail)
		}
	}
}

// With nothing overriding it, the remedy is about the tag, and must not tell
// the reader to change a variable that is not set.
func TestTheRemedyDoesNotInventAnOverride(t *testing.T) {
	t.Parallel()

	r := remedy("", hmdenv.OriginDefault)
	if strings.Contains(r, "set in") || strings.Contains(r, floci.DoNotDeleteHome) {
		t.Errorf("the remedy blames a setting that does not exist: %s", r)
	}
	if !strings.Contains(r, "pin a tag that exists") {
		t.Errorf("the remedy does not name the actual problem: %s", r)
	}
}

func TestShortNameIsTheRepositorysLastElement(t *testing.T) {
	t.Parallel()

	for ref, want := range map[string]string{
		"ghcr.io/hmdlabs/hmd-postgres-base:0.3.12": "hmd-postgres-base",
		"ghcr.io/hmdlabs/hmd-img-k3s-floci:0.3.4":  "hmd-img-k3s-floci",
		"registry:2": "registry",
	} {
		if got := shortName(ref); got != want {
			t.Errorf("shortName(%q) = %q, want %q", ref, got, want)
		}
	}
}
