package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSubstrateDefaultsToFull(t *testing.T) {
	t.Parallel()

	for _, in := range []string{"", "full", " Full ", "FULL"} {
		got, err := ParseSubstrate(in)
		if err != nil || got != SubstrateFull {
			t.Errorf("ParseSubstrate(%q) = %q, %v; want full", in, got, err)
		}
	}
	for in, want := range map[string]Substrate{"none": SubstrateNone, "core": SubstrateCore} {
		got, err := ParseSubstrate(in)
		if err != nil || got != want {
			t.Errorf("ParseSubstrate(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestParseSubstrateNamesTheChoices(t *testing.T) {
	t.Parallel()

	_, err := ParseSubstrate("bogus")
	if err == nil {
		t.Fatal("a bogus mode was accepted")
	}
	for _, want := range []string{"bogus", "none", "core", "full"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// Absent means full: an environment written before the key existed keeps the
// behaviour it had.
func TestSubstrateModeOfAnAbsentKeyIsFull(t *testing.T) {
	t.Parallel()

	var none *Manifest
	if none.SubstrateMode() != SubstrateFull {
		t.Errorf("nil manifest = %q", none.SubstrateMode())
	}
	if (&Manifest{}).SubstrateMode() != SubstrateFull {
		t.Errorf("empty manifest = %q", (&Manifest{}).SubstrateMode())
	}
	if (&Manifest{Substrate: "core"}).SubstrateMode() != SubstrateCore {
		t.Errorf("core manifest = %q", (&Manifest{Substrate: "core"}).SubstrateMode())
	}
}

func TestSubstrateRoundTripsInBothFormats(t *testing.T) {
	t.Parallel()

	m := &Manifest{Version: Version, Name: "dev", Substrate: "none"}
	for _, name := range []string{"dev.yaml", "dev.json"} {
		path := filepath.Join(t.TempDir(), name)
		if err := m.Save(path); err != nil {
			t.Fatalf("Save(%s): %v", name, err)
		}
		back, err := LoadFile(path, fakeEnv(nil))
		if err != nil {
			t.Fatalf("LoadFile(%s): %v", name, err)
		}
		if back.SubstrateMode() != SubstrateNone {
			t.Errorf("%s round-tripped substrate to %q", name, back.Substrate)
		}
	}
}

// A hand-edited key fails the same way the flag does, with the file named by
// the loader.
func TestValidateRejectsAnUnknownSubstrate(t *testing.T) {
	t.Parallel()

	m := &Manifest{Version: Version, Name: "dev", Substrate: "some"}
	problems := strings.Join(m.Validate(fakeEnv(nil)), "\n")
	if !strings.Contains(problems, "'substrate'") || !strings.Contains(problems, "some") {
		t.Errorf("an unknown substrate was accepted: %q", problems)
	}
}

// The key is omitted when unset so a manifest nsctl rewrites for another
// reason does not sprout a key the user never chose.
func TestSaveOmitsAnUnsetSubstrate(t *testing.T) {
	t.Parallel()

	m := &Manifest{Version: Version, Name: "dev"}
	path := filepath.Join(t.TempDir(), "dev.yaml")
	if err := m.Save(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "substrate") {
		t.Errorf("an unset substrate was written:\n%s", data)
	}
}
