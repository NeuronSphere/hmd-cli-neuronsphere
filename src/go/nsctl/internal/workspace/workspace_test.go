package workspace

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
)

func lookupOf(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

// The legacy `hmd neuronsphere up` rule: a studio checkout under HMD_HOME wins
// over HMD_REPO_HOME, so the notebooks see the same tree they always did.
func TestProjectsPathPrefersTheStudioCheckout(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	studio := filepath.Join(home, "studio", "projects")
	if err := os.MkdirAll(studio, 0o755); err != nil {
		t.Fatal(err)
	}
	got := ProjectsPath(home, lookupOf(map[string]string{"HMD_REPO_HOME": "/elsewhere"}))
	if got != studio {
		t.Errorf("ProjectsPath = %q, want the studio checkout %q", got, studio)
	}
}

func TestProjectsPathFallsBackToRepoHome(t *testing.T) {
	t.Parallel()

	got := ProjectsPath(t.TempDir(), lookupOf(map[string]string{"HMD_REPO_HOME": "/Users/dev/projects"}))
	if got != "/Users/dev/projects" {
		t.Errorf("ProjectsPath = %q, want HMD_REPO_HOME", got)
	}
}

// No path is better than a relative one: an empty allowlist root would
// canonicalize to Floci's own working directory.
func TestProjectsPathIsEmptyWithoutAnAbsoluteRepoHome(t *testing.T) {
	t.Parallel()

	for _, repoHome := range []string{"", "relative/projects"} {
		if got := ProjectsPath(t.TempDir(), lookupOf(map[string]string{"HMD_REPO_HOME": repoHome})); got != "" {
			t.Errorf("ProjectsPath with HMD_REPO_HOME=%q = %q, want empty", repoHome, got)
		}
	}
}

func TestHostVolumesRequireTheOptIn(t *testing.T) {
	t.Parallel()

	lookup := lookupOf(map[string]string{"HMD_REPO_HOME": "/Users/dev/projects"})
	for name, m := range map[string]*manifest.Manifest{
		"no manifest":  nil,
		"not opted in": {Name: "local"},
	} {
		if got := HostVolumes(m, t.TempDir(), lookup); got != nil {
			t.Errorf("%s: HostVolumes = %v, want none", name, got)
		}
	}
}

func TestHostVolumesMountTheProjectsRootReadWrite(t *testing.T) {
	t.Parallel()

	m := &manifest.Manifest{Name: "local", HostWorkspace: true}
	got := HostVolumes(m, t.TempDir(), lookupOf(map[string]string{"HMD_REPO_HOME": "/Users/dev/projects"}))
	want := []string{"/Users/dev/projects:" + NodePath}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("HostVolumes = %v, want %v", got, want)
	}
}

func TestHostVolumesNeedAProjectsPath(t *testing.T) {
	t.Parallel()

	m := &manifest.Manifest{Name: "local", HostWorkspace: true}
	if got := HostVolumes(m, t.TempDir(), lookupOf(nil)); got != nil {
		t.Errorf("HostVolumes = %v, want none without a projects path", got)
	}
}
