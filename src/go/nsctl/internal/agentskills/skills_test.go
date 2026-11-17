package agentskills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDestinationsUseNativeHostLocations(t *testing.T) {
	t.Parallel()
	project, home := t.TempDir(), t.TempDir()
	dests, err := Destinations([]string{"nsctl-onboard"}, All, Project, project, home)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := dests[0].Path, filepath.Join(project, ".agents", "skills", "nsctl-onboard"); got != want {
		t.Errorf("Codex target = %q, want %q", got, want)
	}
	if got, want := dests[1].Path, filepath.Join(project, ".claude", "skills", "nsctl-onboard"); got != want {
		t.Errorf("Claude target = %q, want %q", got, want)
	}
	dests, err = Destinations([]string{"nsctl-onboard"}, Codex, User, "", home)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := dests[0].Path, filepath.Join(home, ".agents", "skills", "nsctl-onboard"); got != want {
		t.Errorf("user target = %q, want %q", got, want)
	}
}

func TestInstallDetectsModificationAndProtectsRemoval(t *testing.T) {
	t.Parallel()
	project := t.TempDir()
	dests, err := Destinations([]string{"nsctl-onboard"}, Codex, Project, project, "")
	if err != nil {
		t.Fatal(err)
	}
	dest := dests[0]
	if err := Install(dest, false, false); err != nil {
		t.Fatal(err)
	}
	if state, err := StateAt(dest.Skill, dest.Path); err != nil || state != Installed {
		t.Fatalf("state after install = %q, %v", state, err)
	}
	if err := os.WriteFile(filepath.Join(dest.Path, "SKILL.md"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	dest.State, err = StateAt(dest.Skill, dest.Path)
	if err != nil || dest.State != Modified {
		t.Fatalf("state after edit = %q, %v", dest.State, err)
	}
	if err := Remove(dest, false, false); err == nil {
		t.Fatal("removed a locally changed skill without --force")
	}
	if err := Remove(dest, true, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dest.Path); !os.IsNotExist(err) {
		t.Errorf("skill remains after force remove: %v", err)
	}
}

func TestDryRunDoesNotCreateDirectories(t *testing.T) {
	t.Parallel()
	project := t.TempDir()
	dests, err := Destinations([]string{"nsctl-debug"}, Claude, Project, project, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := Install(dests[0], false, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dests[0].Path); !os.IsNotExist(err) {
		t.Errorf("dry run wrote %s: %v", dests[0].Path, err)
	}
}

// Requirements: HMD_CLI_NEURONSPHERE_NERD039_SPEC010
func TestDataSkillsStartFromStatusAndOnboardRoutesToThem(t *testing.T) {
	t.Parallel()
	ladder := []string{"ns-data-start", "ns-data-inventory", "ns-data-profile", "ns-data-register", "ns-data-query"}
	for _, name := range ladder {
		if !Known(name) {
			t.Errorf("%s is not bundled", name)
			continue
		}
		paths, err := CommandPaths(name)
		if err != nil {
			t.Fatal(err)
		}
		if len(paths) == 0 || paths[0] != "data status" {
			t.Errorf("%s must start from `nsctl data status` so it can resume; commands %v", name, paths)
		}
	}
	md, err := bundled.ReadFile("skills/nsctl-onboard/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	body := string(md)
	data, repo := strings.Index(body, "ns-data-start"), strings.Index(body, "nsctl-repoclass-adopt")
	if data < 0 || repo < 0 || data > repo {
		t.Error("nsctl-onboard must route a person with data to ns-data-start before anything about repositories")
	}
}
