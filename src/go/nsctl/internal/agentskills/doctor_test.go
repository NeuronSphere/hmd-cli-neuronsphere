package agentskills

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Requirements: HMD_CLI_NEURONSPHERE_NERD015_SPEC005
func TestSkillDiagnosisIsReadOnlyAndReportsEntryPointOwnershipAndVersion(t *testing.T) {
	t.Parallel()
	project := t.TempDir()

	// Usable target: diagnosis reports Missing and creates nothing.
	dests, err := Destinations([]string{"nsctl-debug"}, All, Project, project, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dests {
		if d.State != Missing {
			t.Errorf("%s state = %q, want missing", d.Path, d.State)
		}
	}
	if entries, _ := os.ReadDir(project); len(entries) != 0 {
		t.Errorf("reading state created %d entries in the project", len(entries))
	}

	// Unusable target: reported as an error, not silently accepted.
	if _, err := Destinations([]string{"nsctl-debug"}, All, Project, filepath.Join(project, "absent"), ""); err == nil {
		t.Error("a nonexistent project path was reported usable")
	}

	// Installed, then each deviation the doctor must name.
	if err := Install(dests[0], false, false); err != nil {
		t.Fatal(err)
	}
	if st, _ := StateAt("nsctl-debug", dests[0].Path); st != Installed {
		t.Fatalf("fresh install state = %q", st)
	}
	if st, _ := StateAt("nsctl-onboard", dests[0].Path); st != Modified {
		t.Errorf("a skill directory owned for another skill read as %q, want modified", st)
	}
	before, _ := os.ReadFile(filepath.Join(dests[0].Path, "SKILL.md"))
	if err := os.Remove(filepath.Join(dests[0].Path, markerName)); err != nil {
		t.Fatal(err)
	}
	if st, _ := StateAt("nsctl-debug", dests[0].Path); st != Modified {
		t.Errorf("missing ownership marker read as %q, want modified", st)
	}
	after, _ := os.ReadFile(filepath.Join(dests[0].Path, "SKILL.md"))
	if string(before) != string(after) {
		t.Error("diagnosis changed the installed skill")
	}

	// Bundled version differing from the installed one is reported (Outdated)
	// without any host binary being involved.
	v1, v2, home := t.TempDir(), t.TempDir(), t.TempDir()
	writeSkill(t, v1, "s", "Old.", nil)
	writeSkill(t, v2, "s", "New.", nil)
	old, _ := FromDir(v1, "owner")
	od, _ := old.Destinations(nil, Claude, User, "", home)
	if err := old.Install(od[0], false, false); err != nil {
		t.Fatal(err)
	}
	next, _ := FromDir(v2, "owner")
	nd, _ := next.Destinations(nil, Claude, User, "", home)
	if nd[0].State != Outdated {
		t.Errorf("version difference state = %q, want outdated", nd[0].State)
	}
}

// Requirements: HMD_CLI_NEURONSPHERE_NERD015_SPEC006
func TestBundledSkillsStateTheLocalCredentialBoundary(t *testing.T) {
	t.Parallel()
	read := func(name string) string {
		b, err := fs.ReadFile(Bundled().fsys, name+"/SKILL.md")
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(strings.Fields(string(b)), " ")
	}
	debug := read("nsctl-debug")
	for _, want := range []string{
		"No local command sends a credential",
		"`nsctl login`, `nsctl logout` and `nsctl whoami` are neither diagnostic steps nor repairs",
		"never propose signing in",
		"hosted NeuronSphere tenant or a private registry",
	} {
		if !strings.Contains(debug, want) {
			t.Errorf("nsctl-debug lacks %q", want)
		}
	}
	env := read("nsctl-local-environment")
	if !strings.Contains(env, "A container engine and `HMD_HOME` are the only prerequisites:") ||
		!strings.Contains(env, "no local command sends a credential") {
		t.Error("nsctl-local-environment does not state the prerequisites and credential boundary")
	}
	auth := read("nsctl-auth-and-profiles")
	if !strings.Contains(auth, "hosted NeuronSphere tenant and private registries") ||
		!strings.Contains(auth, "signing in never fixes a local failure") {
		t.Error("the identity skill is not scoped to a hosted tenant and private registries")
	}
}
