package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/agentskills"
)

func TestAgentSkillsInstallListAndRemoveAtBothProjectTargets(t *testing.T) {
	t.Parallel()
	project := t.TempDir()
	out, _, err := run(t, fakeEnv(nil), "agent", "skills", "install", "nsctl-onboard", "--host", "all", "--path", project)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	for _, want := range []string{".agents/skills/nsctl-onboard", ".claude/skills/nsctl-onboard"} {
		if !strings.Contains(out, want) {
			t.Errorf("install output %q does not name %q", out, want)
		}
	}
	for _, path := range []string{
		filepath.Join(project, ".agents", "skills", "nsctl-onboard", "SKILL.md"),
		filepath.Join(project, ".claude", "skills", "nsctl-onboard", "SKILL.md"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s was not installed: %v", path, err)
		}
	}

	out, _, err = run(t, fakeEnv(nil), "agent", "skills", "list", "--host", "codex", "--path", project, "--json")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var got []agentskills.Destination
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("list JSON: %v\n%s", err, out)
	}
	found := false
	for _, dest := range got {
		if dest.Skill == "nsctl-onboard" {
			found = dest.State == agentskills.Installed
		}
	}
	if !found {
		t.Errorf("list did not report nsctl-onboard installed: %#v", got)
	}

	if _, _, err := run(t, fakeEnv(nil), "agent", "skills", "remove", "nsctl-onboard", "--host", "all", "--path", project); err != nil {
		t.Fatalf("remove: %v", err)
	}
}

func TestAgentSkillsRefuseExistingSkillBeforeWritingOtherHost(t *testing.T) {
	t.Parallel()
	project := t.TempDir()
	existing := filepath.Join(project, ".claude", "skills", "nsctl-debug")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(existing, "SKILL.md"), []byte("user content"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := run(t, fakeEnv(nil), "agent", "skills", "install", "nsctl-debug", "--host", "all", "--path", project)
	if err == nil {
		t.Fatal("install unexpectedly replaced an existing skill")
	}
	if _, err := os.Stat(filepath.Join(project, ".agents", "skills", "nsctl-debug")); !os.IsNotExist(err) {
		t.Errorf("Codex skill was installed despite Claude conflict: %v", err)
	}
}

func TestBundledSkillCommandPathsExist(t *testing.T) {
	t.Parallel()
	root := NewRootCommand("9.9.9", fakeEnv(nil))
	for _, skill := range agentskills.List() {
		paths, err := agentskills.CommandPaths(skill.Name)
		if err != nil {
			t.Fatalf("%s: %v", skill.Name, err)
		}
		for _, path := range paths {
			if _, _, err := root.Find(strings.Fields(path)); err != nil {
				t.Errorf("%s documents unavailable command %q: %v", skill.Name, path, err)
			}
		}
	}
}
