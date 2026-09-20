package environment

import (
	"errors"
	"os"
	"path/filepath"
)

// PurgedMarkerPath is the file a purge leaves for a deployment id, under the
// registry's cache directory rather than the environment's state directory,
// which the purge removes.
//
// A purge takes the containers, the cluster and the state directory, but not
// the control plane's deployment graph: hmd-ms-deployment still records every
// instance of that deployment id as DEPLOYED. A later `env add` under the
// same name inherits those records, and with no snapshot to compare against
// the reconcile calls them unchanged -- "no snapshot means no information" is
// the right rule for an environment that predates snapshots, and exactly the
// wrong one for an environment that was just destroyed. The marker tells the
// next apply which case it is in.
func PurgedMarkerPath(home, deploymentID string) string {
	return filepath.Join(home, ".cache", "neuronsphere", "purged", deploymentID)
}

// MarkPurged records that a deployment id's graph records are stale.
func MarkPurged(home, deploymentID string) error {
	path := PurgedMarkerPath(home, deploymentID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte("every instance the deployment graph records as DEPLOYED was purged; the next apply redeploys them all\n"), 0o644)
}

// WasPurged reports whether a purge marker stands for a deployment id.
func WasPurged(home, deploymentID string) bool {
	_, err := os.Stat(PurgedMarkerPath(home, deploymentID))
	return err == nil
}

// ClearPurged removes the marker once an apply has redeployed everything.
func ClearPurged(home, deploymentID string) error {
	err := os.Remove(PurgedMarkerPath(home, deploymentID))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
