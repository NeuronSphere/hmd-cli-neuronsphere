package compose

import (
	"context"
	"errors"
	"io"
	"testing"
)

// extensionProject is two independent services in sorted key order, which is
// the shape NERD004 SPEC006 is about: "a-ext" sorts before "z-ext", so under
// Up a failure in the first would take the second with it.
func extensionProject() *Project {
	return &Project{
		Name: "proj",
		Networks: map[string]Network{
			"neuronsphere_default": {Name: "neuronsphere_default-abc", External: true},
		},
		Services: []Service{
			{Key: "a-ext-server", Image: "broken:1",
				Networks: []NetworkAttachment{{Name: "neuronsphere_default"}}},
			{Key: "z-ext-server", Image: "fine:1",
				Networks: []NetworkAttachment{{Name: "neuronsphere_default"}}},
			{Key: "off-ext-server", Image: "fine:1", Profiles: []string{"oci"},
				Networks: []NetworkAttachment{{Name: "neuronsphere_default"}}},
		},
	}
}

func result(t *testing.T, results []Result, service string) Result {
	t.Helper()
	for _, r := range results {
		if r.Service == service {
			return r
		}
	}
	t.Fatalf("no result for %q, got %+v", service, results)
	return Result{}
}

// The requirement: one failing service must not stop the others. Up cannot do
// this, which is why UpEach exists.
func TestUpEachKeepsGoingPastAFailure(t *testing.T) {
	t.Parallel()

	f := newFakeAPI()
	f.images["fine:1"] = true
	f.failPull = map[string]error{"broken:1": errors.New("manifest unknown")}

	results, err := newRunner(f).UpEach(context.Background(), extensionProject(), map[string]bool{})
	if err != nil {
		t.Fatalf("UpEach returned a whole-project error for one bad service: %v", err)
	}

	bad := result(t, results, "a-ext-server")
	if !bad.Failed() {
		t.Errorf("the unpullable service reported no error: %+v", bad)
	}
	if bad.Name != "proj-a-ext-server-1" {
		t.Errorf("Name = %q; a failed result must still name its container", bad.Name)
	}

	good := result(t, results, "z-ext-server")
	if good.Failed() {
		t.Errorf("the healthy service failed because another one did: %v", good.Err)
	}
	if good.Action != ActionCreated {
		t.Errorf("Action = %q, want created", good.Action)
	}
	if len(f.started) != 1 || f.started[0] != "proj-z-ext-server-1" {
		t.Errorf("started = %v, want only the healthy service", f.started)
	}
}

// The contrast that motivates UpEach, asserted rather than assumed: Up really
// does abandon everything after the first failure.
func TestUpStopsAtTheFirstFailure(t *testing.T) {
	t.Parallel()

	f := newFakeAPI()
	f.images["fine:1"] = true
	f.failPull = map[string]error{"broken:1": errors.New("manifest unknown")}

	_, err := newRunner(f).Up(context.Background(), extensionProject(), map[string]bool{})
	if err == nil {
		t.Fatal("Up reported success with an unpullable image")
	}
	if len(f.started) != 0 {
		t.Errorf("started = %v, want nothing: Up abandons the rest", f.started)
	}
}

// A profile gate is not a failure. Reported as skipped and nothing else, so
// switching a component off reads differently from it breaking.
func TestUpEachSkipsInactiveProfilesWithoutFailing(t *testing.T) {
	t.Parallel()

	f := newFakeAPI()
	f.images["fine:1"] = true
	f.images["broken:1"] = true

	results, _ := newRunner(f).UpEach(context.Background(), extensionProject(), map[string]bool{})
	off := result(t, results, "off-ext-server")
	if off.Action != ActionSkipped || off.Failed() {
		t.Errorf("off-ext-server = %+v, want skipped and not failed", off)
	}
	for _, r := range results {
		if r.Failed() {
			t.Errorf("%s failed: %v", r.Service, r.Err)
		}
	}
}

func TestUpEachReportsEverySeparateFailure(t *testing.T) {
	t.Parallel()

	f := newFakeAPI()
	f.failPull = map[string]error{
		"broken:1": errors.New("manifest unknown"),
		"fine:1":   errors.New("no such host"),
	}

	results, _ := (&Runner{API: f, Out: io.Discard, Err: io.Discard}).
		UpEach(context.Background(), extensionProject(), map[string]bool{"oci": true})

	failed := 0
	for _, r := range results {
		if r.Failed() {
			failed++
		}
	}
	if failed != 3 {
		t.Errorf("%d failures reported, want 3 -- every one, not just the first", failed)
	}
}
