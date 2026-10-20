package envtemplate

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
)

func noEnv(string) string { return "" }

func artifactRepo(name, class, version string) manifest.Repo {
	return manifest.Repo{InstanceName: name, RepoClassName: class, Version: version,
		Source: &manifest.Source{Type: manifest.SourceArtifact}}
}

func TestSaveThenLoadRoundTripsUnderTheTemplatesDirectory(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	m := &manifest.Manifest{Version: manifest.Version, Name: "ignored",
		Repos: []manifest.Repo{artifactRepo("trino", "hmd-inf-trino", "0.1.4")}}

	if err := Save(home, "analytics", m); err != nil {
		t.Fatalf("Save() = %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "templates", "analytics.yaml")); err != nil {
		t.Fatalf("template file: %v", err)
	}
	got, err := Load(home, "analytics", noEnv)
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	// The template's name is the file's, whatever the source manifest said.
	if got.Name != "analytics" || len(got.Repos) != 1 || got.Repos[0].InstanceName != "trino" {
		t.Errorf("Load() = %+v", got)
	}
}

func TestLoadOfAnAbsentTemplateIsNotFound(t *testing.T) {
	t.Parallel()
	if _, err := Load(t.TempDir(), "nope", noEnv); !errors.Is(err, ErrNotFound) {
		t.Errorf("Load(absent) = %v, want ErrNotFound", err)
	}
}

func TestListIsSortedAndIgnoresOtherFiles(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	for _, name := range []string{"telemetry", "analytics"} {
		if err := Save(home, name, &manifest.Manifest{Version: manifest.Version}); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, "templates", "README.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := List(home)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"analytics", "telemetry"}) {
		t.Errorf("List() = %v", got)
	}
	if got, err := List(t.TempDir()); err != nil || len(got) != 0 {
		t.Errorf("List(no templates dir) = %v, %v; want empty, nil", got, err)
	}
}

func TestRemoveDeletesTheFileAndReportsAbsence(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	if err := Save(home, "analytics", &manifest.Manifest{Version: manifest.Version}); err != nil {
		t.Fatal(err)
	}
	if err := Remove(home, "analytics"); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	if Exists(home, "analytics") {
		t.Error("Remove() left the template")
	}
	if err := Remove(home, "analytics"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Remove(absent) = %v, want ErrNotFound", err)
	}
}

func TestNamesFollowEnvironmentRules(t *testing.T) {
	t.Parallel()
	for _, ok := range []string{"analytics", "telemetry-2", "a", "0x"} {
		if err := ValidName(ok); err != nil {
			t.Errorf("ValidName(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "Analytics", "-x", "a/b", "../x", "a.b", string(make([]byte, 64))} {
		if err := ValidName(bad); err == nil {
			t.Errorf("ValidName(%q) accepted it", bad)
		}
	}
	if err := Save(t.TempDir(), "../escape", &manifest.Manifest{Version: manifest.Version}); err == nil {
		t.Error("Save() accepted a name that leaves the templates directory")
	}
}

func TestFromEnvironmentDropsWorkingTrees(t *testing.T) {
	t.Parallel()
	env := &manifest.Manifest{Version: manifest.Version, Name: "dev", Profiles: []string{"full"},
		Repos: []manifest.Repo{
			artifactRepo("clickhouse", "hmd-inf-clickhouse", "0.2.0"),
			{InstanceName: "my-otel", RepoClassName: "hmd-inf-otel-collector",
				Source: &manifest.Source{Type: manifest.SourceLocal, Path: "/w/hmd-inf-otel-collector"}},
			// No source: resolved by the tiers at deploy time, not someone's
			// working tree, so it is part of the shape.
			{InstanceName: "trino", RepoClassName: "hmd-inf-trino"},
		}}

	tmpl, dropped := FromEnvironment(env)
	if len(tmpl.Repos) != 2 || tmpl.Repos[0].InstanceName != "clickhouse" || tmpl.Repos[1].InstanceName != "trino" {
		t.Errorf("template repos = %+v, want clickhouse and trino", tmpl.Repos)
	}
	if !reflect.DeepEqual(dropped, []string{"my-otel"}) {
		t.Errorf("dropped = %v, want [my-otel]", dropped)
	}
	if !reflect.DeepEqual(tmpl.Profiles, []string{"full"}) {
		t.Errorf("profiles = %v, want them kept", tmpl.Profiles)
	}
	// The environment itself is untouched.
	if len(env.Repos) != 3 {
		t.Errorf("FromEnvironment() mutated its input: %+v", env.Repos)
	}
}
