// Package reconcile diffs an environment's desired state against what it has
// actually deployed.
//
// Desired is the substrate plus the environment manifest. Actual is the
// deployment graph. What comes out is a plan: what to add, what changed, what
// to leave alone, and what is deployed but no longer declared.
//
// The digests here are compared against ones the Python front end wrote, so
// they are byte-compatible with change_set_builder.entry_hash rather than
// merely stable. A digest that disagrees turns a user's first `nsctl env
// apply` into a full redeploy of a working environment.
package reconcile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bom"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
)

// SnapshotFilename records the last successfully applied definition.
//
// It lives in the environment's own state directory, so `env purge` -- which
// removes that directory -- resets drift tracking to "no information" along
// with everything else, rather than leaving a snapshot describing an
// environment that no longer exists.
const SnapshotFilename = "applied-changeset.json"

// EntryHash is a stable digest of one entry, used to detect drift.
//
// deployment_id is excluded: it is stamped per environment and is not part of
// what the user declared, so including it would make every entry read as
// changed the moment an environment is renamed or re-slugged.
func EntryHash(entry bom.Entry) string {
	payload := map[string]any{
		"repo_instance_name":     entry.RepoInstanceName,
		"repo_class_name":        entry.RepoClassName,
		"repo_class_version":     versionOrNull(entry.RepoClassVersion),
		"instance_configuration": normalise(entry.InstanceConfiguration),
		"dependencies":           normalise(entry.Dependencies),
	}
	sum := sha256.Sum256([]byte(pythonJSON(payload)))
	return hex.EncodeToString(sum[:])
}

// versionOrNull maps an unset version to JSON null.
//
// Go models the field as a string, so "unset" and "the empty string" are the
// same value; Python models it as Optional[str] and hashes an unresolved
// version as null. Nothing produces a genuinely empty version -- the seeder
// refuses an entry without one -- so treating "" as null is the reading that
// agrees with a digest the Python front end wrote.
func versionOrNull(version string) any {
	if version == "" {
		return nil
	}
	return version
}

// normalise turns an absent mapping into the empty one the Python builder
// always produces, so a nil map and an empty map digest identically.
func normalise(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

// DefinitionHash digests a whole definition, independent of entry order.
func DefinitionHash(entries []bom.Entry) string {
	digests := make([]string, 0, len(entries))
	for _, e := range entries {
		digests = append(digests, EntryHash(e))
	}
	sort.Strings(digests)
	var joined string
	for _, d := range digests {
		joined += d
	}
	sum := sha256.Sum256([]byte(joined))
	return hex.EncodeToString(sum[:])
}

// SnapshotEntry is one record of what was applied.
type SnapshotEntry struct {
	RepoInstanceName string `json:"repo_instance_name"`
	RepoClassName    string `json:"repo_class_name"`
	RepoClassVersion string `json:"repo_class_version"`
	Hash             string `json:"hash"`
	// K8sRelease is the Helm release the entry installed, when the apply
	// observed one. Absent means "installs none, or none was seen" -- never
	// "its release is gone".
	K8sRelease string `json:"k8s_release,omitempty"`
}

// Snapshot is the applied-changeset record.
type Snapshot struct {
	Entries []SnapshotEntry `json:"entries"`
}

// SnapshotPath is where an environment's snapshot lives.
func SnapshotPath(stateDir string) string {
	if stateDir == "" {
		return ""
	}
	return filepath.Join(stateDir, SnapshotFilename)
}

// LoadSnapshot maps instance name to the digest recorded at the last apply.
//
// An unreadable or absent snapshot yields an empty map, which means "no drift
// information" and is treated as such by Plan. It never means "everything
// changed": a missing snapshot must not trigger a redeploy of a working
// environment.
func LoadSnapshot(stateDir string) map[string]string {
	digests := map[string]string{}
	path := SnapshotPath(stateDir)
	if path == "" {
		return digests
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return digests
	}
	var doc Snapshot
	if err := json.Unmarshal(data, &doc); err != nil {
		return digests
	}
	for _, e := range doc.Entries {
		if e.RepoInstanceName != "" && e.Hash != "" {
			digests[e.RepoInstanceName] = e.Hash
		}
	}
	return digests
}

// WriteSnapshot records what was applied, replacing any prior snapshot.
//
// Written to a temporary file and renamed, so an interrupted write leaves the
// previous snapshot intact rather than a truncated one that reads as "nothing
// was ever applied".
func WriteSnapshot(stateDir string, entries []bom.Entry, releases map[string]string) error {
	path := SnapshotPath(stateDir)
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	doc := Snapshot{Entries: make([]SnapshotEntry, 0, len(entries))}
	for _, e := range entries {
		doc.Entries = append(doc.Entries, SnapshotEntry{
			RepoInstanceName: e.RepoInstanceName,
			RepoClassName:    e.RepoClassName,
			RepoClassVersion: e.RepoClassVersion,
			Hash:             EntryHash(e),
			K8sRelease:       releases[e.RepoInstanceName],
		})
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("serialising the applied-changeset snapshot: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replacing %s: %w", path, err)
	}
	return nil
}

// Plan is what a reconcile proposes.
type Plan struct {
	// Desired is the whole definition, whether or not it needs deploying.
	Desired []bom.Entry
	// Add is never deployed here, or deployed and since failed.
	Add []bom.Entry
	// Change is deployed, but its declaration has drifted since.
	Change []bom.Entry
	// Unchanged is deployed and matches what was recorded.
	Unchanged []string
	// Remove is deployed but declared by nothing. Reported, never acted on:
	// nsctl does not destroy on a user's behalf.
	Remove []string
	// Degraded means the deployment graph could not be read, so Add and
	// Change are empty because nothing is known rather than because there is
	// nothing to do.
	Degraded bool
}

// Deploy is what needs running: the additions and the drifted, in the order
// the definition gave them.
func (p *Plan) Deploy() []bom.Entry {
	needed := make(map[string]bool, len(p.Add)+len(p.Change))
	for _, e := range p.Add {
		needed[e.RepoInstanceName] = true
	}
	for _, e := range p.Change {
		needed[e.RepoInstanceName] = true
	}
	out := make([]bom.Entry, 0, len(needed))
	for _, e := range p.Desired {
		if needed[e.RepoInstanceName] {
			out = append(out, e)
		}
	}
	return out
}

// Empty reports whether there is nothing to deploy.
func (p *Plan) Empty() bool { return len(p.Add) == 0 && len(p.Change) == 0 }

// Summary is a one-line description of the plan.
func (p *Plan) Summary() string {
	if p.Degraded {
		return "the deployment graph could not be read, so nothing is assumed about what is deployed"
	}
	return fmt.Sprintf("%d to deploy, %d changed, %d unchanged, %d deployed but undeclared",
		len(p.Add), len(p.Change), len(p.Unchanged), len(p.Remove))
}

// Compute diffs the desired definition against the graph.
//
// status comes from Client.InstanceStatus and snapshot from LoadSnapshot.
// missingReleases names entries the graph calls DEPLOYED whose recorded Helm
// release is no longer on the cluster; redeploying is the only thing that can
// make the two agree.
//
// An entry deployed with no recorded digest is left alone rather than
// redeployed: no snapshot means no information, and guessing "changed" would
// redeploy a whole working environment the first time a user runs this.
func Compute(desired []bom.Entry, status map[string]string,
	snapshot map[string]string, missingReleases map[string]bool) *Plan {

	plan := &Plan{Desired: desired}
	declared := make(map[string]bool, len(desired))

	for _, entry := range desired {
		name := entry.RepoInstanceName
		declared[name] = true

		if status[name] != msdeploy.StatusDeployed {
			plan.Add = append(plan.Add, entry)
			continue
		}
		if missingReleases[name] {
			plan.Add = append(plan.Add, entry)
			continue
		}
		recorded, known := snapshot[name]
		if known && recorded != EntryHash(entry) {
			plan.Change = append(plan.Change, entry)
			continue
		}
		plan.Unchanged = append(plan.Unchanged, name)
	}

	for name, s := range status {
		if s == msdeploy.StatusDeployed && !declared[name] {
			plan.Remove = append(plan.Remove, name)
		}
	}
	sort.Strings(plan.Remove)
	return plan
}

// Degraded is the fail-safe plan for when the graph cannot be read.
//
// Nothing to add and nothing to remove: a transient outage of the deployment
// service must never be read as "everything was removed from the manifest".
func Degraded(desired []bom.Entry) *Plan {
	return &Plan{Desired: desired, Degraded: true}
}
