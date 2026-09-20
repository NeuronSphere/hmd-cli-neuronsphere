package compose

import (
	"context"
	"strings"
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
)

// ownedBy builds a *running* container labelled with a compose project, the way
// a control plane started from another HMD_HOME leaves one.
func ownedBy(project, home string) types.ContainerJSON {
	c := stoppedOwnedBy(project, home)
	c.State.Running = true
	return c
}

// stoppedOwnedBy is the same container after `control-plane stop`: still
// present, still holding the name, serving nothing.
func stoppedOwnedBy(project, home string) types.ContainerJSON {
	return types.ContainerJSON{
		ContainerJSONBase: &types.ContainerJSONBase{
			ID:    "id-" + project,
			State: &types.ContainerState{},
		},
		Config: &container.Config{
			Labels: map[string]string{LabelProject: project},
			Env:    []string{"HMD_HOME=" + home, "OTHER=x"},
		},
	}
}

func ownershipProject() *Project {
	return &Project{
		Name: "local_neuronsphere-11111111",
		Services: []Service{
			{Key: "proxy", ContainerName: "hmd_proxy"},
			{Key: "floci", ContainerName: "floci"},
			{Key: "authd", ContainerName: "hmd_authd", Profiles: []string{"authd"}},
		},
	}
}

// SPEC014's gap, made detectable. The control-plane container names are fixed
// by the compose file while the network, compose project and Floci data dir are
// all namespaced by an HMD_HOME hash -- so a second HMD_HOME would find the
// first's containers, see a config hash that cannot match, and recreate them
// pointed at itself.
func TestAnotherHomesContainersAreDetected(t *testing.T) {
	t.Parallel()

	api := newFakeAPI()
	api.containers["hmd_proxy"] = ownedBy("local_neuronsphere-99999999", "/Users/someone/other-home")
	api.containers["floci"] = ownedBy("local_neuronsphere-99999999", "/Users/someone/other-home")

	owners := CheckOwnership(context.Background(), api, ownershipProject(), nil)
	if len(owners) != 2 {
		t.Fatalf("found %d owners, want 2: %v", len(owners), owners)
	}
	// Both paths named: "something else owns this" without saying what is not
	// actionable, and the other HMD_HOME is the thing the user has to go stop.
	rendered := JoinOwners(owners)
	for _, want := range []string{"hmd_proxy", "floci", "local_neuronsphere-99999999", "/Users/someone/other-home"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("the refusal does not name %q: %s", want, rendered)
		}
	}
}

// Our own containers are the warm-restart case, which is most starts.
func TestOurOwnContainersAreNotAConflict(t *testing.T) {
	t.Parallel()

	p := ownershipProject()
	api := newFakeAPI()
	api.containers["hmd_proxy"] = ownedBy(p.Name, "/Users/someone/hmd")
	api.containers["floci"] = ownedBy(p.Name, "/Users/someone/hmd")

	if owners := CheckOwnership(context.Background(), api, p, nil); len(owners) != 0 {
		t.Errorf("our own containers were reported as conflicts: %v", owners)
	}
}

// A container that cannot be inspected, or carries no project label at all, is
// unknown rather than another home's. This decides whether to refuse a start,
// so a daemon hiccup must not invent an owner -- and an unlabelled container
// predates the labels or was started by hand, which upService's hash comparison
// has always handled on its own.
func TestUnknownOwnershipIsNotAConflict(t *testing.T) {
	t.Parallel()

	api := newFakeAPI()
	// floci is absent entirely: ContainerInspect returns not-found.
	api.containers["hmd_proxy"] = types.ContainerJSON{
		ContainerJSONBase: &types.ContainerJSONBase{ID: "id"},
		Config:            &container.Config{Labels: map[string]string{}},
	}

	if owners := CheckOwnership(context.Background(), api, ownershipProject(), nil); len(owners) != 0 {
		t.Errorf("an unlabelled or missing container was reported as a conflict: %v", owners)
	}
}

// A service whose profile is off is not started, so who owns its container is
// not this start's business -- refusing on hmd_authd when the identity
// provider is disabled would block a start that would never have touched it.
func TestAnInactiveProfilesContainerIsIgnored(t *testing.T) {
	t.Parallel()

	api := newFakeAPI()
	api.containers["hmd_authd"] = ownedBy("local_neuronsphere-99999999", "/other")

	if owners := CheckOwnership(context.Background(), api, ownershipProject(), nil); len(owners) != 0 {
		t.Errorf("an inactive profile's container was reported as a conflict: %v", owners)
	}
	active := map[string]bool{"authd": true}
	if owners := CheckOwnership(context.Background(), api, ownershipProject(), active); len(owners) != 1 {
		t.Errorf("with the identity provider enabled its container should conflict, got %v", owners)
	}
}

// The refusal used to name a remedy that did not resolve it. `control-plane
// stop` stops rather than removes, so the containers kept their names, the next
// start refused again with the same message, and there was nothing left to try
// short of `docker rm`. A stopped container is serving nothing; taking its name
// costs its owner only what stopping already cost it.
func TestAStoppedOwnersContainersAreNotAConflict(t *testing.T) {
	t.Parallel()

	api := newFakeAPI()
	api.containers["hmd_proxy"] = stoppedOwnedBy("local_neuronsphere-99999999", "/Users/someone/other-home")
	api.containers["floci"] = stoppedOwnedBy("local_neuronsphere-99999999", "/Users/someone/other-home")

	if owners := CheckOwnership(context.Background(), api, ownershipProject(), nil); len(owners) != 0 {
		t.Errorf("found %d owners for a stopped control plane, want 0: %v", len(owners), owners)
	}
}

// One running container is still a conflict even when its siblings are stopped:
// a platform mid-shutdown, or one whose proxy alone survived, is still a
// platform this start would break.
func TestOneRunningOwnerIsStillAConflict(t *testing.T) {
	t.Parallel()

	api := newFakeAPI()
	api.containers["hmd_proxy"] = ownedBy("local_neuronsphere-99999999", "/Users/someone/other-home")
	api.containers["floci"] = stoppedOwnedBy("local_neuronsphere-99999999", "/Users/someone/other-home")

	owners := CheckOwnership(context.Background(), api, ownershipProject(), nil)
	if len(owners) != 1 || owners[0].Container != "hmd_proxy" {
		t.Errorf("owners = %v, want hmd_proxy alone", owners)
	}
}
