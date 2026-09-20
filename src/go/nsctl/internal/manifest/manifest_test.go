package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeEnv(vars map[string]string) Lookup {
	return func(key string) string { return vars[key] }
}

// repoHome makes a working tree for each class so source validation passes.
func repoHome(t *testing.T, classes ...string) string {
	t.Helper()
	home := t.TempDir()
	for _, c := range classes {
		if err := os.MkdirAll(filepath.Join(home, c), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

func writeManifest(t *testing.T, home, slug, body string) string {
	t.Helper()
	path := filepath.Join(Root(home), slug+".yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// An environment with no manifest is the empty one `env add` makes, not an
// error: the substrate deploys and nothing sits on it.
func TestLoadReturnsNothingWhenAnEnvironmentHasNoManifest(t *testing.T) {
	t.Parallel()

	m, err := Load(t.TempDir(), "scratch", fakeEnv(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if m != nil {
		t.Errorf("got a manifest for an environment that has none: %+v", m)
	}
}

func TestLoadReadsADeclaredInstance(t *testing.T) {
	t.Parallel()

	repos := repoHome(t, "hmd-ms-myapi")
	home := t.TempDir()
	writeManifest(t, home, "dev2", `
version: 1
name: dev2
repos:
  - instance_name: my-api
    repo_class_name: hmd-ms-myapi
    version: "0.3"
    instance_configuration:
      replicas: 2
    dependencies:
      eks-cluster: eks-cluster
      database-instance: environment-db
`)

	m, err := Load(home, "dev2", fakeEnv(map[string]string{"HMD_REPO_HOME": repos}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if m.Name != "dev2" || m.Version != 1 {
		t.Errorf("name/version = %q/%d", m.Name, m.Version)
	}
	if len(m.Repos) != 1 {
		t.Fatalf("got %d repos, want 1", len(m.Repos))
	}
	r := m.Repos[0]
	if r.InstanceName != "my-api" || r.RepoClassName != "hmd-ms-myapi" || r.Version != "0.3" {
		t.Errorf("repo = %+v", r)
	}
	if r.Dependencies["eks-cluster"] != "eks-cluster" {
		t.Errorf("dependencies = %v", r.Dependencies)
	}
	if r.InstanceConfiguration["replicas"] != 2 {
		t.Errorf("instance_configuration = %v", r.InstanceConfiguration)
	}
}

// The extension routes the decoder, and both formats mean the same thing.
func TestLoadReadsJSONAndYAMLAlike(t *testing.T) {
	t.Parallel()

	repos := repoHome(t, "hmd-ms-myapi")
	lookup := fakeEnv(map[string]string{"HMD_REPO_HOME": repos})

	home := t.TempDir()
	path := filepath.Join(Root(home), "dev.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"version":1,"name":"dev","repos":[{"instance_name":"my-api","repo_class_name":"hmd-ms-myapi"}]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := Load(home, "dev", lookup)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(m.Repos) != 1 || m.Repos[0].InstanceName != "my-api" {
		t.Errorf("JSON manifest parsed as %+v", m)
	}
}

// YAML wins when both exist, matching the Python discovery order.
func TestFindPrefersYAML(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	writeManifest(t, home, "dev", "version: 1\nname: dev\n")
	if err := os.WriteFile(filepath.Join(Root(home), "dev.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Find(home, "dev", fakeEnv(nil)); filepath.Ext(got) != ".yaml" {
		t.Errorf("Find = %q, want the YAML manifest", got)
	}
}

func TestFindHonoursThePathOverride(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	explicit := filepath.Join(dir, "somewhere-else.yaml")
	if err := os.WriteFile(explicit, []byte("version: 1\nname: dev\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	writeManifest(t, home, "dev", "version: 1\nname: dev\n")

	got := Find(home, "dev", fakeEnv(map[string]string{PathOverride: explicit}))
	if got != explicit {
		t.Errorf("Find = %q, want the override %q", got, explicit)
	}
}

// The Python front end writes these two keys and nsctl does not implement what
// they configure. Dropping them on save would break the other front end, so
// they survive a round trip and are reported instead.
func TestPluginKeysArePreservedAndReported(t *testing.T) {
	t.Parallel()

	repos := repoHome(t, "hmd-ms-myapi")
	home := t.TempDir()
	writeManifest(t, home, "dev", `
version: 1
name: dev
plugins:
  - ns-telemetry
plugin_config:
  ns-telemetry:
    profile: minimal
repos:
  - instance_name: my-api
    repo_class_name: hmd-ms-myapi
`)

	m, err := Load(home, "dev", fakeEnv(map[string]string{"HMD_REPO_HOME": repos}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	unsupported := strings.Join(m.Unsupported(), ",")
	if unsupported != "plugins,plugin_config" {
		t.Errorf("Unsupported() = %q", unsupported)
	}

	out := filepath.Join(t.TempDir(), "dev.yaml")
	if err := m.Save(out); err != nil {
		t.Fatalf("Save: %v", err)
	}
	saved, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"plugins:", "ns-telemetry", "plugin_config:", "profile: minimal"} {
		if !strings.Contains(string(saved), want) {
			t.Errorf("saving dropped %q:\n%s", want, saved)
		}
	}
}

func TestSaveRoundTrips(t *testing.T) {
	t.Parallel()

	repos := repoHome(t, "hmd-ms-myapi")
	lookup := fakeEnv(map[string]string{"HMD_REPO_HOME": repos})

	m := &Manifest{Version: Version, Name: "dev", Repos: []Repo{{
		InstanceName:          "my-api",
		RepoClassName:         "hmd-ms-myapi",
		InstanceConfiguration: map[string]any{"replicas": 2},
		Dependencies:          map[string]any{"eks-cluster": "eks-cluster"},
	}}}

	for _, name := range []string{"dev.yaml", "dev.json"} {
		path := filepath.Join(t.TempDir(), name)
		if err := m.Save(path); err != nil {
			t.Fatalf("Save(%s): %v", name, err)
		}
		back, err := LoadFile(path, lookup)
		if err != nil {
			t.Fatalf("LoadFile(%s): %v", name, err)
		}
		if len(back.Repos) != 1 || back.Repos[0].InstanceName != "my-api" {
			t.Errorf("%s round-tripped to %+v", name, back.Repos)
		}
		if back.Repos[0].Dependencies["eks-cluster"] != "eks-cluster" {
			t.Errorf("%s lost dependencies: %v", name, back.Repos[0].Dependencies)
		}
	}
}

// Every problem at once: a user fixing a hand-edited file should see the whole
// list rather than one error per run.
func TestValidateReportsEveryProblemAtOnce(t *testing.T) {
	t.Parallel()

	m := &Manifest{Version: 2, Repos: []Repo{
		{RepoClassName: "hmd-ms-a"},
		{InstanceName: "dup", RepoClassName: "hmd-ms-b", Source: &Source{Type: "local", Path: "/definitely/not/here"}},
		{InstanceName: "dup", RepoClassName: "hmd-ms-c", Source: &Source{Type: "local", Path: "/definitely/not/here"}},
	}}

	problems := strings.Join(m.Validate(fakeEnv(nil)), "\n")
	for _, want := range []string{
		"'name' is required",
		"'version' must be 1",
		"'instance_name' is required",
		"duplicate instance_name",
	} {
		if !strings.Contains(problems, want) {
			t.Errorf("problems did not mention %q:\n%s", want, problems)
		}
	}
}

func TestValidateRejectsASubstrateName(t *testing.T) {
	t.Parallel()

	repos := repoHome(t, "hmd-postgres-rds")
	m := &Manifest{Version: Version, Name: "dev", Repos: []Repo{
		{InstanceName: "environment-db", RepoClassName: "hmd-postgres-rds"},
	}}

	problems := strings.Join(m.Validate(fakeEnv(map[string]string{"HMD_REPO_HOME": repos})), "\n")
	if !strings.Contains(problems, "reserved for the environment substrate") {
		t.Errorf("a substrate name was accepted: %q", problems)
	}
}

func TestValidateSourceTypes(t *testing.T) {
	t.Parallel()

	repos := repoHome(t, "hmd-ms-a")
	lookup := fakeEnv(map[string]string{"HMD_REPO_HOME": repos})

	tests := []struct {
		name    string
		source  *Source
		version string
		want    string
	}{
		{"missing tree", &Source{Type: "local", Path: "/definitely/not/here"}, "", "local repo path does not exist"},
		{"unknown kind", &Source{Type: "wishful"}, "", "unknown source type"},
		{"default is local", nil, "", ""},

		// NERD005 SPEC001. A version is required because an artifact is
		// addressed *by* version: there is no meta-data/VERSION to fall back
		// on, and the 0.1.0 sentinel would be a silently wrong answer.
		{"artifact without a version", &Source{Type: SourceArtifact}, "", "'version' is required"},
		{"artifact with a path", &Source{Type: SourceArtifact, Path: "/somewhere"}, "0.1.4", "'source.path' is meaningless"},
		{"artifact with a malformed type", &Source{Type: SourceArtifact, ArtifactType: "Build Zip"}, "0.1.4", "not a librarian content item type"},
		// No working tree is consulted, and none is required.
		{"artifact", &Source{Type: SourceArtifact}, "0.1.4", ""},
		{"artifact with an explicit type", &Source{Type: SourceArtifact, ArtifactType: "schema"}, "0.1.4", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := &Manifest{Version: Version, Name: "dev", Repos: []Repo{
				{InstanceName: "a", RepoClassName: "hmd-ms-a", Source: tt.source, Version: tt.version},
			}}
			problems := strings.Join(m.Validate(lookup), "\n")
			if tt.want == "" {
				if problems != "" {
					t.Errorf("valid manifest reported %q", problems)
				}
				return
			}
			if !strings.Contains(problems, tt.want) {
				t.Errorf("problems = %q, want it to mention %q", problems, tt.want)
			}
		})
	}
}

// An artifact source resolves from the librarian even with $HMD_REPO_HOME
// pointed at an empty directory. That is NERD005 SPEC002's deliberate reversal
// of the last existing tier, and it is the acceptance criterion for the whole
// document, so it is asserted at the validation layer too: a manifest that
// cannot be validated without a checkout can never be applied without one.
func TestValidateAcceptsAnArtifactWithNoCheckoutAnywhere(t *testing.T) {
	t.Parallel()

	empty := t.TempDir()
	m := &Manifest{Version: Version, Name: "dev", Repos: []Repo{
		{InstanceName: "a", RepoClassName: "hmd-ms-a", Version: "0.1.4",
			Source: &Source{Type: SourceArtifact}},
	}}

	if problems := m.Validate(fakeEnv(map[string]string{"HMD_REPO_HOME": empty})); len(problems) > 0 {
		t.Errorf("an artifact source needed a checkout: %q", problems)
	}
}

// validateSource returns early for a control-plane manifest, so that one
// extension's missing checkout cannot fail every other extension with it
// (NERD004 SPEC006). A malformed artifact declaration is not host state
// though -- it is a file-format error -- so it must still be reported in that
// scope. Ordering these two checks the other way around is the subtle bug.
func TestValidateChecksAnArtifactInControlPlaneScopeToo(t *testing.T) {
	t.Parallel()

	lookup := fakeEnv(nil)
	for _, tt := range []struct {
		name string
		repo Repo
		want string
	}{
		{"no version", Repo{InstanceName: "a", RepoClassName: "hmd-ms-a",
			Source: &Source{Type: SourceArtifact}}, "'version' is required"},
		{"stray path", Repo{InstanceName: "a", RepoClassName: "hmd-ms-a", Version: "0.1.4",
			Source: &Source{Type: SourceArtifact, Path: "/somewhere"}}, "'source.path' is meaningless"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := &Manifest{Version: Version, Name: ControlPlaneName,
				Scope: ScopeControlPlane, Repos: []Repo{tt.repo}}
			problems := strings.Join(m.Validate(lookup), "\n")
			if !strings.Contains(problems, tt.want) {
				t.Errorf("problems = %q, want it to mention %q", problems, tt.want)
			}
		})
	}
}

// A local source in control-plane scope keeps its exemption: the missing tree
// is cpext's to report, per instance.
func TestValidateSkipsAMissingTreeInControlPlaneScope(t *testing.T) {
	t.Parallel()

	m := &Manifest{Version: Version, Name: ControlPlaneName, Scope: ScopeControlPlane,
		Repos: []Repo{{InstanceName: "a", RepoClassName: "hmd-ms-a",
			Source: &Source{Type: SourceLocal, Path: "/definitely/not/here"}}}}

	if problems := m.Validate(fakeEnv(nil)); len(problems) > 0 {
		t.Errorf("control-plane scope reported a missing tree: %q", problems)
	}
}

// ArtifactType defaults to build, which is what hmd build publishes and what
// every pin in this repository's own pre_build_artifacts uses.
func TestArtifactTypeDefaults(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		repo Repo
		want string
	}{
		{"no source", Repo{}, DefaultArtifactType},
		{"no artifact_type", Repo{Source: &Source{Type: SourceArtifact}}, DefaultArtifactType},
		{"explicit", Repo{Source: &Source{Type: SourceArtifact, ArtifactType: "schema"}}, "schema"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.repo.ArtifactType(); got != tt.want {
				t.Errorf("ArtifactType = %q, want %q", got, tt.want)
			}
		})
	}
}

// The change_set schema types a dependency value as a string or a list of
// them. Anything else fails server-side with a message that does not say which
// entry was at fault.
func TestValidateRejectsAMalformedDependency(t *testing.T) {
	t.Parallel()

	repos := repoHome(t, "hmd-ms-a")
	lookup := fakeEnv(map[string]string{"HMD_REPO_HOME": repos})

	ok := &Manifest{Version: Version, Name: "dev", Repos: []Repo{{
		InstanceName: "a", RepoClassName: "hmd-ms-a",
		Dependencies: map[string]any{"one": "x", "many": []any{"y", "z"}},
	}}}
	if problems := ok.Validate(lookup); len(problems) > 0 {
		t.Errorf("a valid dependency mapping was rejected: %v", problems)
	}

	bad := &Manifest{Version: Version, Name: "dev", Repos: []Repo{{
		InstanceName: "a", RepoClassName: "hmd-ms-a",
		Dependencies: map[string]any{"role": 42},
	}}}
	problems := strings.Join(bad.Validate(lookup), "\n")
	if !strings.Contains(problems, `dependency "role"`) {
		t.Errorf("problems = %q, want the offending role named", problems)
	}
}

func TestRepoPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		repo Repo
		env  map[string]string
		want string
	}{
		{
			name: "convention",
			repo: Repo{RepoClassName: "hmd-ms-a"},
			env:  map[string]string{"HMD_REPO_HOME": "/repos"},
			want: "/repos/hmd-ms-a",
		},
		{
			name: "explicit path wins",
			repo: Repo{RepoClassName: "hmd-ms-a", Source: &Source{Path: "/elsewhere/a"}},
			env:  map[string]string{"HMD_REPO_HOME": "/repos"},
			want: "/elsewhere/a",
		},
		{
			name: "expanded",
			repo: Repo{RepoClassName: "hmd-ms-a", Source: &Source{Path: "$WORK/a"}},
			env:  map[string]string{"WORK": "/w"},
			want: "/w/a",
		},
		{
			name: "unresolvable without a repo home",
			repo: Repo{RepoClassName: "hmd-ms-a"},
			want: "",
		},
		{
			name: "not a local source",
			repo: Repo{RepoClassName: "hmd-ms-a", Source: &Source{Type: SourceArtifact}},
			env:  map[string]string{"HMD_REPO_HOME": "/repos"},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.repo.RepoPath(fakeEnv(tt.env)); got != tt.want {
				t.Errorf("RepoPath = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLoadFileReportsMalformedYAML(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "dev.yaml")
	if err := os.WriteFile(path, []byte("\tnot: [valid"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadFile(path, fakeEnv(nil))
	if err == nil {
		t.Fatal("malformed YAML parsed cleanly")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error does not name the file: %v", err)
	}
}
