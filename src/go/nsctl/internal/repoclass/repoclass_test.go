package repoclass

import (
	"os"
	"path/filepath"
	"strings"
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

// The tiers are bom_seeder.resolve_repo_version's.
//
// This test asserted the wrong order until 2026-09-07: a working tree beat a
// declared version unconditionally, where the Python only prefers a tree when
// the developer explicitly asks. The old third case even named itself "then the
// declared version" while asserting the tree won -- a duplicate of the case
// above it, which is how the divergence survived being written down.
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
		{"a tree the user asked for beats a declared version",
			map[string]string{"HMD_LOCAL_NEURONSPHERE_PREFER_LOCAL_VERSIONS": "true"}, "1.0", "0.7", SourceWorkingTree},
		{"otherwise the declared version, not a tree that merely exists", nil, "1.0", "1.0", SourceDeclared},
		{"a tree is still better than the sentinel", nil, "", "0.7", SourceWorkingTree},
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

// NERD0013 SPEC0005 (hmd-ms-deployment): the discovery block is read from the
// same tree the version came from, so a registered version and its discovery
// describe one build.
func TestResolveDiscoveryReadsTheManifest(t *testing.T) {
	t.Parallel()

	manifest := `{"name":"hmd-inf-thing","deploy":{"dependencies":{}},` +
		`"discovery":{"summary":"Does things.","capabilities":[{"name":"x","kind":"function","description":"y"}]}}`
	home := repoHome(t, "hmd-inf-thing", "0.7", manifest)
	r := New(home, fakeEnv(nil))

	discovery, err := r.ResolveDiscovery("hmd-inf-thing", "")
	if err != nil {
		t.Fatal(err)
	}
	if discovery["summary"] != "Does things." {
		t.Errorf("discovery = %v", discovery)
	}
	caps, _ := discovery["capabilities"].([]any)
	if len(caps) != 1 {
		t.Errorf("capabilities = %v", discovery["capabilities"])
	}
}

// nil, not an empty map, when the manifest has no discovery section or there
// is no manifest at all: the seeder leaves the key off the payload.
func TestResolveDiscoveryNilWithoutSection(t *testing.T) {
	t.Parallel()

	home := repoHome(t, "hmd-inf-thing", "0.7", `{"name":"hmd-inf-thing","deploy":{}}`)
	r := New(home, fakeEnv(nil))
	if d, err := r.ResolveDiscovery("hmd-inf-thing", ""); err != nil || d != nil {
		t.Errorf("got %v, %v; want nil, nil", d, err)
	}

	r = New(t.TempDir(), fakeEnv(nil))
	if d, err := r.ResolveDiscovery("absent", ""); err != nil || d != nil {
		t.Errorf("got %v, %v; want nil, nil", d, err)
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

// NERD009 SPEC003/004: the two deploy keys nsctl acts on for a foreign node.
// Commands are typed loosely because real manifests carry object arguments in
// their command lists (`["docker", "build", {"is_windows": true}]`), and a
// reader that refused those would refuse repos it reads today.
func TestReadManifestReadsDeployCommandsAndImage(t *testing.T) {
	t.Parallel()

	home := repoHome(t, "acme-api", "0.3", `{"name":"acme-api",
		"build":{"commands":[["docker","build",{"is_windows":true}]]},
		"deploy":{"commands":[["exec","make","deploy"]],"image":"ghcr.io/acme/ci:3.2"}}`)
	m, err := ReadManifest(filepath.Join(home, "acme-api"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Deploy.Image != "ghcr.io/acme/ci:3.2" {
		t.Errorf("image = %q", m.Deploy.Image)
	}
	if len(m.Deploy.Commands) != 1 || len(m.Deploy.Commands[0]) != 3 {
		t.Errorf("commands = %v", m.Deploy.Commands)
	}
}

// A directory without a manifest answers nil, nil: the caller decides whether
// that is an error, and for the runner it is not -- most repo classes it
// deploys are described by the BOM entry alone.
func TestReadManifestToleratesAnAbsentFile(t *testing.T) {
	t.Parallel()

	m, err := ReadManifest(t.TempDir())
	if m != nil || err != nil {
		t.Errorf("got %v, %v; want nil, nil", m, err)
	}
	if m, err := ReadManifest(""); m != nil || err != nil {
		t.Errorf("empty dir: got %v, %v; want nil, nil", m, err)
	}
}

// SPEC003: exactly one exec entry per phase, and it is the whole phase. Two are
// refused rather than half-run, because the ordering and failure semantics of
// a multi-command foreign deploy are the author's business.
func TestExecCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		commands string
		want     []string
		wantErr  string
	}{
		{"no commands", `[]`, nil, ""},
		{"absent", ``, nil, ""},
		{"native tools", `[["helm"],["cdktf"]]`, nil, ""},
		{"object argument on a native tool", `[["docker","deploy",{"x":1}]]`, nil, ""},
		{"one exec", `[["exec","make","deploy"]]`, []string{"make", "deploy"}, ""},
		{"shell form", `[["exec","sh","-c","env > out"]]`, []string{"sh", "-c", "env > out"}, ""},
		{"two execs", `[["exec","make","a"],["exec","make","b"]]`, nil, "exactly one"},
		{"exec beside a native tool", `[["exec","make","deploy"],["helm"]]`, nil, "combined"},
		{"native tool before exec", `[["helm"],["exec","make","deploy"]]`, nil, "combined"},
		{"empty argv", `[["exec"]]`, nil, "no command"},
		{"non-string argument", `[["exec","make",{"x":1}]]`, nil, "string"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			manifest := `{"name":"x","deploy":{}}`
			if tt.commands != "" {
				manifest = `{"name":"x","deploy":{"commands":` + tt.commands + `}}`
			}
			home := repoHome(t, "x", "0.1", manifest)
			m, err := ReadManifest(filepath.Join(home, "x"))
			if err != nil {
				t.Fatal(err)
			}
			got, err := m.ExecCommand()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want one mentioning %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(got, "\x00") != strings.Join(tt.want, "\x00") {
				t.Errorf("argv = %q, want %q", got, tt.want)
			}
		})
	}
}
