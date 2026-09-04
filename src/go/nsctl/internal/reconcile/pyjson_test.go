package reconcile

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bom"
)

// goldenEntry is one vector from testdata/python-entry-hashes.json, produced by
// calling CPython's change_set_builder.entry_hash directly.
type goldenEntry struct {
	Name  string `json:"name"`
	Entry struct {
		DeploymentID          string         `json:"deployment_id"`
		RepoInstanceName      string         `json:"repo_instance_name"`
		RepoClassName         string         `json:"repo_class_name"`
		RepoClassVersion      string         `json:"repo_class_version"`
		InstanceConfiguration map[string]any `json:"instance_configuration"`
		Dependencies          map[string]any `json:"dependencies"`
	} `json:"entry"`
	// Payload is the exact json.dumps output Python hashed.
	Payload string `json:"payload"`
	Hash    string `json:"hash"`
}

type golden struct {
	Entries        []goldenEntry `json:"entries"`
	DefinitionHash string        `json:"definition_hash"`
}

// loadGolden decodes with UseNumber so 2 and 2.0 stay distinguishable. Python
// hashed an int and a float differently ("2" against "2.0"), and decoding into
// float64 would collapse both to the same value and make the vector unable to
// catch the difference it exists to catch. In real use entries come from YAML,
// which keeps the two apart on its own.
func loadGolden(t *testing.T) golden {
	t.Helper()
	data, err := os.ReadFile("testdata/python-entry-hashes.json")
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var g golden
	if err := dec.Decode(&g); err != nil {
		t.Fatal(err)
	}
	if len(g.Entries) == 0 {
		t.Fatal("the golden file has no vectors")
	}
	return g
}

func (g goldenEntry) bomEntry() bom.Entry {
	return bom.Entry{
		RepoInstanceName:      g.Entry.RepoInstanceName,
		RepoClassName:         g.Entry.RepoClassName,
		RepoClassVersion:      g.Entry.RepoClassVersion,
		DeploymentID:          g.Entry.DeploymentID,
		InstanceConfiguration: g.Entry.InstanceConfiguration,
		Dependencies:          g.Entry.Dependencies,
	}
}

// The serialiser is checked against Python's actual output before the digest
// is, so a mismatch says which byte differs rather than only that two hex
// strings do not match.
func TestPythonJSONMatchesCPythonByteForByte(t *testing.T) {
	t.Parallel()

	for _, vector := range loadGolden(t).Entries {
		t.Run(vector.Name, func(t *testing.T) {
			t.Parallel()
			e := vector.bomEntry()
			payload := map[string]any{
				"repo_instance_name":     e.RepoInstanceName,
				"repo_class_name":        e.RepoClassName,
				"repo_class_version":     versionOrNull(e.RepoClassVersion),
				"instance_configuration": normalise(e.InstanceConfiguration),
				"dependencies":           normalise(e.Dependencies),
			}
			if got := pythonJSON(payload); got != vector.Payload {
				t.Errorf("serialised form differs from CPython's\n got: %s\nwant: %s", got, vector.Payload)
			}
		})
	}
}

// The digest a user's environment already carries was written by the Python
// front end. If these disagree, every entry reads as changed on the first
// `nsctl env apply` and a working environment is redeployed wholesale.
func TestEntryHashMatchesTheCPythonDigest(t *testing.T) {
	t.Parallel()

	for _, vector := range loadGolden(t).Entries {
		t.Run(vector.Name, func(t *testing.T) {
			t.Parallel()
			if got := EntryHash(vector.bomEntry()); got != vector.Hash {
				t.Errorf("EntryHash = %s, want CPython's %s\npayload: %s",
					got, vector.Hash, vector.Payload)
			}
		})
	}
}

func TestDefinitionHashMatchesTheCPythonDigest(t *testing.T) {
	t.Parallel()

	g := loadGolden(t)
	entries := make([]bom.Entry, 0, len(g.Entries))
	for _, vector := range g.Entries {
		entries = append(entries, vector.bomEntry())
	}
	if got := DefinitionHash(entries); got != g.DefinitionHash {
		t.Errorf("DefinitionHash = %s, want CPython's %s", got, g.DefinitionHash)
	}
}

// Order-independent by construction, and worth pinning: the seeder
// topologically sorts before applying, so the same definition reaches this in
// more than one order.
func TestDefinitionHashIgnoresOrder(t *testing.T) {
	t.Parallel()

	g := loadGolden(t)
	forward := make([]bom.Entry, 0, len(g.Entries))
	for _, vector := range g.Entries {
		forward = append(forward, vector.bomEntry())
	}
	reversed := make([]bom.Entry, len(forward))
	for i, e := range forward {
		reversed[len(forward)-1-i] = e
	}
	if DefinitionHash(forward) != DefinitionHash(reversed) {
		t.Error("DefinitionHash depends on entry order")
	}
}

// deployment_id is stamped per environment, not declared, so renaming an
// environment must not read as a change to every entry in it.
func TestEntryHashIgnoresTheDeploymentID(t *testing.T) {
	t.Parallel()

	base := bom.Entry{RepoInstanceName: "a", RepoClassName: "hmd-ms-a", RepoClassVersion: "1.0",
		DeploymentID: "local"}
	other := base
	other.DeploymentID = "dev2"
	if EntryHash(base) != EntryHash(other) {
		t.Error("the digest changed with the deployment id")
	}
}

// A nil mapping and an empty one are the same declaration; the Python builder
// always produces the empty one.
func TestEntryHashTreatsNilAndEmptyMapsAlike(t *testing.T) {
	t.Parallel()

	withNil := bom.Entry{RepoInstanceName: "a", RepoClassName: "hmd-ms-a", RepoClassVersion: "1.0"}
	withEmpty := withNil
	withEmpty.InstanceConfiguration = map[string]any{}
	withEmpty.Dependencies = map[string]any{}
	if EntryHash(withNil) != EntryHash(withEmpty) {
		t.Error("a nil mapping digests differently from an empty one")
	}
}

func TestEntryHashNoticesEveryDeclaredField(t *testing.T) {
	t.Parallel()

	base := bom.Entry{
		RepoInstanceName: "a", RepoClassName: "hmd-ms-a", RepoClassVersion: "1.0",
		InstanceConfiguration: map[string]any{"k": "v"},
		Dependencies:          map[string]any{"role": "b"},
	}
	baseline := EntryHash(base)

	mutations := map[string]func(e *bom.Entry){
		"instance name": func(e *bom.Entry) { e.RepoInstanceName = "b" },
		"repo class":    func(e *bom.Entry) { e.RepoClassName = "hmd-ms-b" },
		"version":       func(e *bom.Entry) { e.RepoClassVersion = "1.1" },
		"configuration": func(e *bom.Entry) { e.InstanceConfiguration = map[string]any{"k": "w"} },
		"dependencies":  func(e *bom.Entry) { e.Dependencies = map[string]any{"role": "c"} },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			mutated := base
			mutate(&mutated)
			if EntryHash(mutated) == baseline {
				t.Errorf("changing the %s did not change the digest", name)
			}
		})
	}
}
