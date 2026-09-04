package repoclass

import (
	"os"
	"path/filepath"
	"testing"
)

func fakeEnv(vars map[string]string) Lookup {
	return func(key string) string { return vars[key] }
}

func repoHome(t *testing.T, repoClass, version, manifest string) string {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, repoClass, "meta-data")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if version != "" {
		if err := os.WriteFile(filepath.Join(dir, "VERSION"), []byte(version+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if manifest != "" {
		if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

func TestVersionEnvVar(t *testing.T) {
	t.Parallel()

	if got := VersionEnvVar("hmd-postgres-rds"); got != "HMD_LOCAL_VERSION_HMD_POSTGRES_RDS" {
		t.Errorf("VersionEnvVar = %q", got)
	}
}

func TestResolveVersionPrecedence(t *testing.T) {
	t.Parallel()

	home := repoHome(t, "hmd-inf-thing", "0.7", "")

	tests := []struct {
		name       string
		env        map[string]string
		declared   string
		wantVer    string
		wantSource Source
	}{
		{"a pin wins over everything", map[string]string{"HMD_LOCAL_VERSION_HMD_INF_THING": "9.9"}, "1.0", "9.9", SourcePin},
		{"the working tree next", nil, "1.0", "0.7", SourceWorkingTree},
		{"then the declared version", nil, "1.0", "0.7", SourceWorkingTree},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := New(home, fakeEnv(tt.env))
			got := r.ResolveVersion("hmd-inf-thing", tt.declared)
			if got.Version != tt.wantVer || got.Source != tt.wantSource {
				t.Errorf("got %+v, want %s from %s", got, tt.wantVer, tt.wantSource)
			}
		})
	}
}

// `=local` asks for the tree rather than pinning a literal version.
func TestLocalIsNotTreatedAsAPin(t *testing.T) {
	t.Parallel()

	home := repoHome(t, "hmd-inf-thing", "0.7", "")
	r := New(home, fakeEnv(map[string]string{"HMD_LOCAL_VERSION_HMD_INF_THING": "local"}))
	got := r.ResolveVersion("hmd-inf-thing", "")
	if got.Version != "0.7" || got.Source != SourceWorkingTree {
		t.Errorf("got %+v, want the tree's version", got)
	}
}

func TestResolveVersionFallsBackToDeclaredThenSentinel(t *testing.T) {
	t.Parallel()

	r := New(t.TempDir(), fakeEnv(nil))
	if got := r.ResolveVersion("absent", "1.2.3"); got.Version != "1.2.3" || got.Source != SourceDeclared {
		t.Errorf("got %+v, want the declared version", got)
	}
	if got := r.ResolveVersion("absent", ""); got.Version != SentinelVersion || got.Source != SourceSentinel {
		t.Errorf("got %+v, want the sentinel", got)
	}
}

// nil rather than an empty map: the caller falls back to what the BOM entry
// declares, and an empty map would silently overwrite it.
func TestResolveReturnsNilMetadataWithoutAManifest(t *testing.T) {
	t.Parallel()

	r := New(t.TempDir(), fakeEnv(nil))
	_, deps, config, err := r.Resolve("absent", "")
	if err != nil {
		t.Fatal(err)
	}
	if deps != nil || config != nil {
		t.Errorf("deps=%v config=%v, want both nil", deps, config)
	}
}

func TestResolveReadsTheManifest(t *testing.T) {
	t.Parallel()

	manifest := `{"name":"hmd-inf-thing","deploy":{"dependencies":{"base-vpc":{"required":"true"}},"default_configuration":{"a":1}}}`
	home := repoHome(t, "hmd-inf-thing", "0.7", manifest)
	r := New(home, fakeEnv(nil))

	version, deps, config, err := r.Resolve("hmd-inf-thing", "")
	if err != nil {
		t.Fatal(err)
	}
	if version != "0.7" {
		t.Errorf("version = %q", version)
	}
	if _, ok := deps["base-vpc"]; !ok {
		t.Errorf("dependencies = %v", deps)
	}
	if config["a"] == nil {
		t.Errorf("default configuration = %v", config)
	}
}

// A repo class in the BOM need not have a working tree on this machine.
func TestLoadManifestToleratesAMissingRepo(t *testing.T) {
	t.Parallel()

	r := New(t.TempDir(), fakeEnv(nil))
	m, err := r.LoadManifest("absent")
	if err != nil || m != nil {
		t.Errorf("got %v, %v; want nil, nil", m, err)
	}
}

func TestLoadManifestReportsMalformedJSON(t *testing.T) {
	t.Parallel()

	home := repoHome(t, "hmd-inf-thing", "0.7", "{not json")
	r := New(home, fakeEnv(nil))
	if _, err := r.LoadManifest("hmd-inf-thing"); err == nil {
		t.Error("malformed JSON was accepted")
	}
}

// An environment manifest may declare a repo with an explicit source path,
// which by definition is not under HMD_REPO_HOME.
func TestPathOverrideWins(t *testing.T) {
	t.Parallel()

	elsewhere := repoHome(t, "other-name", "3.3", "")
	r := New(t.TempDir(), fakeEnv(nil))
	r.Paths["hmd-inf-thing"] = filepath.Join(elsewhere, "other-name")

	if got := r.ResolveVersion("hmd-inf-thing", ""); got.Version != "3.3" {
		t.Errorf("got %+v, want the overridden tree's version", got)
	}
}
