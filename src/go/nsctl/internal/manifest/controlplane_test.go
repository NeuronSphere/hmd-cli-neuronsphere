package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeControlPlaneManifest(t *testing.T, home, ext, body string) string {
	t.Helper()
	path := filepath.Join(ControlPlaneRoot(home), ControlPlaneName+ext)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// The default NERD004 asks for: no manifest, nothing extra, no error.
func TestLoadControlPlaneReturnsNothingWhenThereIsNoManifest(t *testing.T) {
	t.Parallel()

	m, err := LoadControlPlane(t.TempDir(), fakeEnv(nil))
	if err != nil {
		t.Fatalf("LoadControlPlane: %v", err)
	}
	if m != nil {
		t.Errorf("got a manifest where there is none: %+v", m)
	}
}

func TestLoadControlPlaneReadsADeclaredExtension(t *testing.T) {
	t.Parallel()

	repos := repoHome(t, "hmd-inf-local-registry")
	home := t.TempDir()
	path := writeControlPlaneManifest(t, home, ".yaml", `
version: 1
name: control-plane
repos:
  - instance_name: package-registry
    repo_class_name: hmd-inf-local-registry
    version: "0.1"
    instance_configuration:
      url: http://registry.local.neuronsphere.io
`)

	m, err := LoadControlPlane(home, fakeEnv(map[string]string{"HMD_REPO_HOME": repos}))
	if err != nil {
		t.Fatalf("LoadControlPlane: %v", err)
	}
	if m.Path != path {
		t.Errorf("Path = %q, want %q", m.Path, path)
	}
	if m.Scope != ScopeControlPlane {
		t.Errorf("Scope = %v, want ScopeControlPlane", m.Scope)
	}
	if len(m.Repos) != 1 || m.Repos[0].InstanceName != "package-registry" {
		t.Fatalf("repos = %+v", m.Repos)
	}
	if got := m.Repos[0].InstanceConfiguration["url"]; got != "http://registry.local.neuronsphere.io" {
		t.Errorf("url = %v", got)
	}
}

// Extensions are tried in the order Find already defines, so a .json manifest
// is readable by whichever front end wrote it.
func TestLoadControlPlaneAcceptsEveryExtension(t *testing.T) {
	for _, ext := range Extensions {
		t.Run(ext, func(t *testing.T) {
			t.Parallel()

			home := t.TempDir()
			body := "version: 1\nname: control-plane\nrepos: []\n"
			if ext == ".json" {
				body = `{"version": 1, "name": "control-plane", "repos": []}`
			}
			writeControlPlaneManifest(t, home, ext, body)

			m, err := LoadControlPlane(home, fakeEnv(nil))
			if err != nil {
				t.Fatalf("LoadControlPlane: %v", err)
			}
			if m == nil {
				t.Fatal("no manifest read")
			}
		})
	}
}

func TestFindControlPlaneHonoursTheOverride(t *testing.T) {
	t.Parallel()

	elsewhere := filepath.Join(t.TempDir(), "somewhere.yaml")
	if err := os.WriteFile(elsewhere, []byte("version: 1\nname: control-plane\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	// A manifest in the conventional place, to prove the override wins.
	writeControlPlaneManifest(t, home, ".yaml", "version: 1\nname: control-plane\n")

	got := FindControlPlane(home, fakeEnv(map[string]string{ControlPlanePathOverride: elsewhere}))
	if got != elsewhere {
		t.Errorf("FindControlPlane = %q, want %q", got, elsewhere)
	}
}

// A manifest that names something else is a file in the wrong directory, and
// saying so is more use than applying it.
func TestControlPlaneManifestMustBeNamedControlPlane(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	writeControlPlaneManifest(t, home, ".yaml", "version: 1\nname: dev2\nrepos: []\n")

	_, err := LoadControlPlane(home, fakeEnv(nil))
	if err == nil {
		t.Fatal("a manifest named dev2 was accepted")
	}
	for _, want := range []string{`'name' must be "control-plane"`, "$HMD_HOME/environments/"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestControlPlaneScopeRefusesTheNamesItOwns(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		"control-plane-vpc", "control-plane-db", "control-plane-graph",
		"hmd-ms-naming", "hmd-ms-artifact-lib", "hmd-ms-deployment",
		"proxy", "floci", "deployment-gui", "authd",
	} {
		if !ScopeControlPlane.Reserved(name) {
			t.Errorf("%q is not reserved in the control-plane scope", name)
		}
		// Not reserved for an environment: these are the control plane's, and
		// an environment declaring one is a different (allowed) thing.
		if ScopeEnvironment.Reserved(name) {
			t.Errorf("%q is reserved in the environment scope, which owns none of it", name)
		}
	}
}

// The substrate's names stay refused in both scopes: one in .config/ is a file
// that landed in the wrong directory.
func TestBothScopesRefuseTheSubstrateNames(t *testing.T) {
	t.Parallel()

	for _, name := range ReservedNames() {
		if !ScopeEnvironment.Reserved(name) || !ScopeControlPlane.Reserved(name) {
			t.Errorf("%q is not reserved in both scopes", name)
		}
	}
}

func TestControlPlaneManifestRefusesAReservedInstanceName(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	writeControlPlaneManifest(t, home, ".yaml", `
version: 1
name: control-plane
repos:
  - instance_name: control-plane-db
    repo_class_name: hmd-postgres-rds
`)

	_, err := LoadControlPlane(home, fakeEnv(nil))
	if err == nil {
		t.Fatal("control-plane-db was accepted")
	}
	if !strings.Contains(err.Error(), "is reserved by the control plane") {
		t.Errorf("error %q does not say who owns the name", err)
	}
}

// The message has to distinguish the two owners, because it decides whether
// the user renames an instance or moves the whole file.
func TestReservedReasonNamesTheOwner(t *testing.T) {
	t.Parallel()

	if got := ScopeControlPlane.reservedReason("eks-cluster"); !strings.Contains(got, "environment substrate") {
		t.Errorf("eks-cluster: %q", got)
	}
	if got := ScopeControlPlane.reservedReason("hmd-ms-deployment"); !strings.Contains(got, "control plane") {
		t.Errorf("hmd-ms-deployment: %q", got)
	}
}

// The zero Scope has to keep behaving as an environment's, because `repo add`
// builds a Manifest in memory and never sets one.
func TestZeroScopeIsTheEnvironmentScope(t *testing.T) {
	t.Parallel()

	m := &Manifest{Version: Version, Name: "dev2", Repos: []Repo{
		{InstanceName: "eks-cluster", RepoClassName: "hmd-inf-eks-cluster"},
	}}
	problems := m.Validate(fakeEnv(nil))
	if len(problems) == 0 {
		t.Fatal("a substrate name was accepted by a zero-scope manifest")
	}
	if !strings.Contains(strings.Join(problems, "\n"), "environment substrate") {
		t.Errorf("problems = %v", problems)
	}
}

// Loading, editing and saving a control-plane manifest keeps its scope, so the
// messages a later Save produces still name the right file.
func TestSaveKeepsTheControlPlaneScope(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	writeControlPlaneManifest(t, home, ".yaml", "version: 1\nname: control-plane\nrepos: []\n")
	m, err := LoadControlPlane(home, fakeEnv(nil))
	if err != nil {
		t.Fatalf("LoadControlPlane: %v", err)
	}
	if err := m.Save(m.Path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	reloaded, err := LoadControlPlane(home, fakeEnv(nil))
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Scope != ScopeControlPlane || reloaded.Name != ControlPlaneName {
		t.Errorf("reloaded = %+v", reloaded)
	}
}
