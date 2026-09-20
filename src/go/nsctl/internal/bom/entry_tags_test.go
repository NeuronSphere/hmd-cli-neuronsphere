package bom

import (
	"reflect"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
)

// TestEntryAndBOMEntryAgreeOnTheirSharedKeys pins the two halves of one schema
// against each other.
//
// bom.Entry is a change set definition going in; msdeploy.BOMEntry is a Bill of
// Materials coming out. They are the same six core keys, which is what makes a
// BOM read from one environment very nearly a change set applicable to another
// -- and change_set.definition sets additionalProperties: false, so a key
// misspelt on either side is a 422 rather than a field quietly ignored.
//
// The types stay separate because this package imports msdeploy and the
// dependency cannot run both ways, and because BOMEntry carries five more
// fields that only exist on the way out. This test is the cost of that, and it
// lives here for the same reason: this is the side that can see both.
func TestEntryAndBOMEntryAgreeOnTheirSharedKeys(t *testing.T) {
	t.Parallel()

	shared := []string{
		"RepoInstanceName", "RepoClassName", "RepoClassVersion",
		"DeploymentID", "InstanceConfiguration", "Dependencies",
	}

	entry := reflect.TypeOf(Entry{})
	bomEntry := reflect.TypeOf(msdeploy.BOMEntry{})

	for _, name := range shared {
		mine, ok := entry.FieldByName(name)
		if !ok {
			t.Errorf("bom.Entry has no %s", name)
			continue
		}
		theirs, ok := bomEntry.FieldByName(name)
		if !ok {
			t.Errorf("msdeploy.BOMEntry has no %s", name)
			continue
		}
		if got, want := jsonName(theirs.Tag.Get("json")), jsonName(mine.Tag.Get("json")); got != want {
			t.Errorf("%s: msdeploy.BOMEntry writes %q, bom.Entry writes %q", name, got, want)
		}
	}
}

// jsonName is the tag's key, without omitempty and friends.
func jsonName(tag string) string {
	for i := 0; i < len(tag); i++ {
		if tag[i] == ',' {
			return tag[:i]
		}
	}
	return tag
}
