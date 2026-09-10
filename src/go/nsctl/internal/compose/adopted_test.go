package compose

import (
	"context"
	"strings"
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
)

// listingAPI is fakeAPI with a real ContainerList, which is what the label
// sweep reads.
type listingAPI struct {
	*fakeAPI
	list []types.Container
}

func (l *listingAPI) ContainerList(context.Context, container.ListOptions) ([]types.Container, error) {
	return l.list, nil
}

func labelled(name, project, service string) types.Container {
	return types.Container{
		Names:  []string{"/" + name},
		Labels: map[string]string{LabelProject: project, LabelService: service},
	}
}

// The sweep is by label rather than by name, so a container whose declaration
// has been removed -- or whose repo class has been deleted -- is still found.
func TestAdoptedFindsContainersTheProjectDoesNotDescribe(t *testing.T) {
	t.Parallel()

	api := &listingAPI{fakeAPI: newFakeAPI(), list: []types.Container{
		labelled("hmd_proxy", "proj", "proxy"),
		labelled("proj-registry-server-1", "proj", "registry-server"),
		labelled("proj-gone-server-1", "proj", "gone-server"),
	}}
	got := (&Runner{API: api}).Adopted(context.Background(), testProject())

	if _, ok := got["proxy"]; ok {
		t.Error("a service the project describes was adopted")
	}
	for _, key := range []string{"registry-server", "gone-server"} {
		if got[key] == "" {
			t.Errorf("%s was not adopted: %v", key, got)
		}
	}
}

// An unlabelled container is nobody's; claiming one would let this sweep stop
// something a person started by hand.
func TestAdoptedIgnoresUnlabelledContainers(t *testing.T) {
	t.Parallel()

	api := &listingAPI{fakeAPI: newFakeAPI(), list: []types.Container{
		{Names: []string{"/handmade"}, Labels: map[string]string{LabelProject: "proj"}},
	}}
	if got := (&Runner{API: api}).Adopted(context.Background(), testProject()); len(got) != 0 {
		t.Errorf("Adopted = %v, want nothing", got)
	}
}

// A daemon that cannot be asked yields nothing rather than an error: this
// drives a stop, and refusing to stop the control plane because a list could
// not be read is the wrong failure.
func TestAdoptedIsEmptyWithoutAProject(t *testing.T) {
	t.Parallel()

	r := &Runner{API: newFakeAPI()}
	if got := r.Adopted(context.Background(), nil); got != nil {
		t.Errorf("Adopted(nil) = %v", got)
	}
	if got := r.Adopted(context.Background(), &Project{}); got != nil {
		t.Errorf("Adopted(unnamed) = %v", got)
	}
}

func TestStopNamedAndRemoveNamed(t *testing.T) {
	t.Parallel()

	f := newFakeAPI()
	f.images["nginx:stable-alpine"] = true
	p := testProject()
	if _, err := newRunner(f).Up(context.Background(), p, map[string]bool{}); err != nil {
		t.Fatalf("Up: %v", err)
	}

	r := newRunner(f)
	if err := r.StopNamed(context.Background(), []string{"hmd_proxy", "not-there"}); err != nil {
		t.Fatalf("StopNamed: %v", err)
	}
	if len(f.stopped) != 1 || f.stopped[0] != "hmd_proxy" {
		t.Errorf("stopped = %v; a container that is gone is not a failure", f.stopped)
	}
	if err := r.RemoveNamed(context.Background(), []string{"hmd_proxy"}); err != nil {
		t.Fatalf("RemoveNamed: %v", err)
	}
	if len(f.removed) != 1 {
		t.Errorf("removed = %v", f.removed)
	}
}

// A sweep that stops at the first container it cannot reach is how the others
// accumulate, so every failure is reported and every container attempted.
func TestStopNamedReportsEveryFailure(t *testing.T) {
	t.Parallel()

	f := newFakeAPI()
	f.containers["a"] = types.ContainerJSON{}
	f.containers["b"] = types.ContainerJSON{}
	r := &Runner{API: &refusingAPI{fakeAPI: f}, Out: discard{}, Err: discard{}}

	err := r.StopNamed(context.Background(), []string{"a", "b"})
	if err == nil {
		t.Fatal("StopNamed reported success")
	}
	for _, want := range []string{"a:", "b:"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

type refusingAPI struct{ *fakeAPI }

func (refusingAPI) ContainerStop(context.Context, string, container.StopOptions) error {
	return errStopRefused
}

type stopRefused struct{}

func (stopRefused) Error() string { return "device or resource busy" }

var errStopRefused = stopRefused{}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
