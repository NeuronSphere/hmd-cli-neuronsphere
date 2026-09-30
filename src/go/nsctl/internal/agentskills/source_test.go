package agentskills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// NERD015 SPEC007: skills from an installed artifact go through the same
// installer as the bundled set, and are owned by the plugin that placed them.

func writeSkill(t *testing.T, root, name, body string, extra map[string]string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: " + name + "\ndescription: " + body + "\n---\n\n# " + name + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
	for rel, content := range extra {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFromDirNamesSkillsByTheirOwnSKILLmd(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeSkill(t, root, "transform-author", "Author transforms.", nil)
	writeSkill(t, root, "transform-debug", "Debug transforms.", nil)
	if err := os.MkdirAll(filepath.Join(root, "not-a-skill"), 0o755); err != nil {
		t.Fatal(err)
	}
	src, err := FromDir(root, "hmd-ms-transform")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range src.List() {
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") != "transform-author,transform-debug" {
		t.Errorf("skills = %v; a directory without SKILL.md is not a skill", names)
	}
	if src.List()[0].Description != "Author transforms." {
		t.Errorf("description = %q", src.List()[0].Description)
	}
}

func TestFromDirRefusesABundledName(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeSkill(t, root, "nsctl-debug", "An impostor.", nil)
	if _, err := FromDir(root, "acme-kit"); err == nil {
		t.Fatal("an artifact skill took a bundled skill's name")
	}
}

func TestArtifactSkillInstallsEveryFileAndIsOwned(t *testing.T) {
	t.Parallel()
	root, home := t.TempDir(), t.TempDir()
	writeSkill(t, root, "transform-author", "Author transforms.", map[string]string{"references/yaml.md": "ref"})
	src, err := FromDir(root, "hmd-ms-transform")
	if err != nil {
		t.Fatal(err)
	}
	dests, err := src.Destinations(nil, Claude, User, "", home)
	if err != nil {
		t.Fatal(err)
	}
	if len(dests) != 1 || dests[0].State != Missing {
		t.Fatalf("dests = %+v", dests)
	}
	if err := src.Install(dests[0], false, false); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dests[0].Path, "references", "yaml.md")); err != nil || string(b) != "ref" {
		t.Errorf("a skill's resources were not copied: %q %v", b, err)
	}
	if st := OwnedState(dests[0].Path, "hmd-ms-transform"); st != Installed {
		t.Errorf("OwnedState = %q", st)
	}
	if st := OwnedState(dests[0].Path, "someone-else"); st != Modified {
		t.Errorf("another plugin's skill must not read as its own: %q", st)
	}
}

func TestAnUnmodifiedSkillFromAnOlderVersionIsReplacedWithoutForce(t *testing.T) {
	t.Parallel()
	v1, v2, home := t.TempDir(), t.TempDir(), t.TempDir()
	writeSkill(t, v1, "transform-author", "Old.", nil)
	writeSkill(t, v2, "transform-author", "New.", nil)

	old, _ := FromDir(v1, "hmd-ms-transform")
	dests, _ := old.Destinations(nil, Claude, User, "", home)
	if err := old.Install(dests[0], false, false); err != nil {
		t.Fatal(err)
	}

	next, _ := FromDir(v2, "hmd-ms-transform")
	dests, err := next.Destinations(nil, Claude, User, "", home)
	if err != nil {
		t.Fatal(err)
	}
	if dests[0].State != Outdated {
		t.Fatalf("state = %q, want %q", dests[0].State, Outdated)
	}
	if err := next.Install(dests[0], false, false); err != nil {
		t.Fatalf("an unmodified older copy needed --force: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(dests[0].Path, "SKILL.md"))
	if !strings.Contains(string(b), "New.") {
		t.Errorf("SKILL.md = %s", b)
	}
}

func TestAnEditedArtifactSkillIsNotReplacedOrRemoved(t *testing.T) {
	t.Parallel()
	root, home := t.TempDir(), t.TempDir()
	writeSkill(t, root, "transform-author", "Author.", nil)
	src, _ := FromDir(root, "hmd-ms-transform")
	dests, _ := src.Destinations(nil, Claude, User, "", home)
	if err := src.Install(dests[0], false, false); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dests[0].Path, "SKILL.md"), []byte("mine now"), 0o644); err != nil {
		t.Fatal(err)
	}
	dests, _ = src.Destinations(nil, Claude, User, "", home)
	if dests[0].State != Modified {
		t.Fatalf("state = %q", dests[0].State)
	}
	if err := src.Install(dests[0], false, false); err == nil {
		t.Error("replaced a skill the user edited")
	}
	dest := dests[0]
	dest.State = OwnedState(dest.Path, "hmd-ms-transform")
	if err := Remove(dest, false, false); err == nil {
		t.Error("removed a skill the user edited")
	}
}

func TestAUserSkillOfTheSameNameIsNotOurs(t *testing.T) {
	t.Parallel()
	root, home := t.TempDir(), t.TempDir()
	writeSkill(t, root, "transform-author", "Author.", nil)
	theirs := filepath.Join(home, ".claude", "skills", "transform-author")
	if err := os.MkdirAll(theirs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(theirs, "SKILL.md"), []byte("hand written"), 0o644); err != nil {
		t.Fatal(err)
	}
	src, _ := FromDir(root, "hmd-ms-transform")
	dests, _ := src.Destinations(nil, Claude, User, "", home)
	if dests[0].State != Modified {
		t.Errorf("a hand-written skill reads as %q", dests[0].State)
	}
}
