package environment

import (
	"context"
	"io"
	"os"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
)

// A purge takes everything but the deployment graph's records, and a later
// `env add` under the same name inherits them. The marker is what tells the
// next apply that "deployed" there means "gone".
func TestAPurgeLeavesAMarkerForItsDeploymentID(t *testing.T) {
	rec := &recorder{}
	home := t.TempDir()
	env := testEnv(home, "dev")
	home = purgeHome(t, map[string]registry.Environment{"dev": env})
	withFakes(t, rec, &fakeDocker{rec: rec, floci: map[string]string{}, labelled: map[string][]string{}})

	if WasPurged(home, env.DeploymentID) {
		t.Fatal("a marker stood before any purge")
	}
	opts := &Options{Home: home, Lookup: func(string) string { return "" }, Out: io.Discard, Err: io.Discard}
	if err := Purge(context.Background(), opts, "dev"); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if !WasPurged(home, env.DeploymentID) {
		t.Fatal("the purge left no marker for its deployment id")
	}
	if err := ClearPurged(home, env.DeploymentID); err != nil {
		t.Fatal(err)
	}
	if WasPurged(home, env.DeploymentID) {
		t.Fatal("the marker survived being cleared")
	}
	// Clearing twice is not an error: an apply that found no marker has
	// nothing to remove.
	if err := ClearPurged(home, env.DeploymentID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(PurgedMarkerPath(home, env.DeploymentID)); !os.IsNotExist(err) {
		t.Fatalf("marker path state after clearing: %v", err)
	}
}
