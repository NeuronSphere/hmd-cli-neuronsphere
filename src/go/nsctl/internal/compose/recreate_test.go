package compose

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
)

// running puts a container in the fake in the state Up would leave alone: same
// configuration hash, and up.
func running(t *testing.T, f *fakeAPI, p *Project, key string) {
	t.Helper()
	s := svc(t, p, key)
	name := s.Name(p.Name)
	f.containers[name] = types.ContainerJSON{
		ContainerJSONBase: &types.ContainerJSONBase{ID: name, State: &types.ContainerState{Running: true}},
		Config:            &container.Config{Labels: map[string]string{LabelConfigHash: configHash(p, s)}},
	}
}

// The property Up cannot provide. Up reconciles on a configuration hash, and
// the failure this exists for -- a data directory deleted out from under a
// running container -- changes no configuration, so Up correctly leaves it
// alone and the container stays useless.
func TestRecreateReplacesAContainerUpWouldHaveLeftAlone(t *testing.T) {
	t.Parallel()

	f := newFakeAPI()
	f.images["nginx:stable-alpine"] = true
	p := testProject()
	running(t, f, p, "proxy")

	// Up leaves it exactly as it is...
	results, err := newRunner(f).Up(context.Background(), p, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r.Service == "proxy" && r.Action != ActionRunning {
			t.Fatalf("Up did not leave the container alone: %v", r.Action)
		}
	}
	if len(f.removed) != 0 || len(f.created) != 0 {
		t.Fatalf("Up rebuilt something: removed=%v created=%v", f.removed, f.created)
	}

	// ...and Recreate does not.
	res, err := newRunner(f).Recreate(context.Background(), p, "proxy")
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != ActionRecreated {
		t.Errorf("action = %v, want %v", res.Action, ActionRecreated)
	}
	if len(f.removed) != 1 || f.removed[0] != "hmd_proxy" {
		t.Errorf("removed = %v, want just hmd_proxy", f.removed)
	}
	if len(f.created) != 1 || f.created[0] != "hmd_proxy" {
		t.Errorf("created = %v, want just hmd_proxy", f.created)
	}
	if len(f.started) == 0 {
		t.Error("the recreated container was never started")
	}
}

// One service, not the project. Recreating Floci already costs the databases it
// spawned; taking the proxy and the GUI with it would be gratuitous.
func TestRecreateTouchesNoOtherService(t *testing.T) {
	t.Parallel()

	f := newFakeAPI()
	f.images["nginx:stable-alpine"] = true
	f.images["gui:1"] = true
	p := testProject()
	running(t, f, p, "proxy")
	running(t, f, p, "gui")

	if _, err := newRunner(f).Recreate(context.Background(), p, "proxy"); err != nil {
		t.Fatal(err)
	}
	for _, name := range append(append([]string{}, f.removed...), f.created...) {
		if name != "hmd_proxy" {
			t.Errorf("Recreate touched %s", name)
		}
	}
}

// A service with no container is created. The caller wants it running; how it
// came not to be is not their question.
func TestRecreateCreatesAServiceThatHasNoContainer(t *testing.T) {
	t.Parallel()

	f := newFakeAPI()
	f.images["nginx:stable-alpine"] = true
	p := testProject()

	res, err := newRunner(f).Recreate(context.Background(), p, "proxy")
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != ActionCreated {
		t.Errorf("action = %v, want %v", res.Action, ActionCreated)
	}
	if len(f.removed) != 0 {
		t.Errorf("removed %v for a container that did not exist", f.removed)
	}
}

// Recreate is the one path that removes Floci deliberately, so it is exactly
// the path that must not be the one that stops warning about the cost.
func TestRecreatingFlociStillWarnsAboutTheDatabasesItSpawned(t *testing.T) {
	t.Parallel()

	f := newFakeAPI()
	f.images["floci/floci:2.0.1"] = true
	p := testProject()
	p.Services = append(p.Services, Service{
		Key: FlociService, ContainerName: "floci", Image: "floci/floci:2.0.1",
	})
	running(t, f, p, FlociService)

	var errOut bytes.Buffer
	r := &Runner{API: f, PullImage: f.pull, Err: &errOut}
	if _, err := r.Recreate(context.Background(), p, FlociService); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut.String(), "recovery of the databases it spawned is not reliable") {
		t.Errorf("no warning about the cost of recreating Floci: %q", errOut.String())
	}
}

func TestRecreateNamesAServiceTheProjectDoesNotHave(t *testing.T) {
	t.Parallel()

	_, err := newRunner(newFakeAPI()).Recreate(context.Background(), testProject(), "nope")
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("err = %v, want one naming the missing service", err)
	}
}
