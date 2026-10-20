package cmd

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/envtemplate"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
)

// NERD035 SPEC004: an environment composed from a template and the
// repositories being edited.

// redisRepo is a working tree of hmd-inf-redis itself: the class myapi's lock
// pins as its "cache" companion.
func redisRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "meta-data", "manifest.json"),
		`{"name": "hmd-inf-redis", "local": {"version": 1, "repos": []}}`)
	writeFile(t, filepath.Join(dir, "meta-data", "VERSION"), "0.2.1")
	if _, _, err := run(t, fakeEnv(nil), "lock", dir); err != nil {
		t.Fatalf("lock: %v", err)
	}
	return dir
}

func saveTemplate(t *testing.T, home, name string, repos ...manifest.Repo) {
	t.Helper()
	if err := envtemplate.Save(home, name, &manifest.Manifest{Version: manifest.Version, Repos: repos}); err != nil {
		t.Fatal(err)
	}
}

func artifactInstance(name, class, version string) manifest.Repo {
	return manifest.Repo{InstanceName: name, RepoClassName: class, Version: version,
		Source: &manifest.Source{Type: manifest.SourceArtifact}}
}

func instancesOf(m *manifest.Manifest, class string) []manifest.Repo {
	var out []manifest.Repo
	for _, r := range m.Repos {
		if r.RepoClassName == class {
			out = append(out, r)
		}
	}
	return out
}

func TestEnvAddFromATemplateAloneDeclaresTheTemplate(t *testing.T) {
	t.Parallel()
	home, env := fromRepoEnv(t)
	saveTemplate(t, home, "analytics", artifactInstance("trino", "hmd-inf-trino", "0.1.4"))

	if out, _, err := run(t, fakeEnv(env), "env", "add", "work", "--template", "analytics", "--no-pull"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	m := loadEnv(t, home, "work")
	if m.Name != "work" || m.Template != "analytics" {
		t.Errorf("manifest name %q template %q, want work and analytics", m.Name, m.Template)
	}
	if _, ok := m.Repo("trino"); !ok {
		t.Errorf("template instance not declared: %v", instanceNames(m))
	}
}

func TestEnvAddSharesATemplateInstanceWithTheRepository(t *testing.T) {
	t.Parallel()
	home, env := fromRepoEnv(t)
	saveTemplate(t, home, "base", artifactInstance("cache", "hmd-inf-redis", "0.2.1"))
	repo := subjectRepo(t, home, subjectManifest)

	if out, _, err := run(t, fakeEnv(env), "env", "add", "work", "--template", "base", "--repo", repo); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	m := loadEnv(t, home, "work")
	if got := instancesOf(m, "hmd-inf-redis"); len(got) != 1 {
		t.Errorf("redis instances = %+v, want the template's one, shared", got)
	}
	api, ok := m.Repo("ms-myapi")
	if !ok || api.SourceType() != manifest.SourceLocal || api.Source.Path != repo {
		t.Errorf("subject = %+v, %v; want ms-myapi from %s", api, ok, repo)
	}
	// A composed environment records no single repository's bindings: it is
	// recomposed, not re-planned.
	if len(m.Bindings) != 0 {
		t.Errorf("bindings = %v, want none recorded", m.Bindings)
	}
}

func TestTheEditedRepositoryReplacesTheTemplatesInstanceOfItsClass(t *testing.T) {
	t.Parallel()
	home, env := fromRepoEnv(t)
	saveTemplate(t, home, "base", artifactInstance("redis-main", "hmd-inf-redis", "0.2.1"))
	redis := redisRepo(t)

	if out, _, err := run(t, fakeEnv(env), "env", "add", "work", "--template", "base", "--repo", redis); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	m := loadEnv(t, home, "work")
	got := instancesOf(m, "hmd-inf-redis")
	if len(got) != 1 || got[0].InstanceName != "redis-main" || got[0].SourceType() != manifest.SourceLocal {
		t.Errorf("redis instances = %+v, want redis-main from the working tree", got)
	}
}

func TestTheEditedRepositoryReplacesAnotherRepositorysCompanionInEitherOrder(t *testing.T) {
	t.Parallel()
	for _, order := range []string{"redis first", "myapi first"} {
		t.Run(order, func(t *testing.T) {
			t.Parallel()
			home, env := fromRepoEnv(t)
			redis := redisRepo(t)
			api := subjectRepo(t, home, subjectManifest)
			args := []string{"env", "add", "work", "--repo", redis, "--repo", api}
			if order == "myapi first" {
				args = []string{"env", "add", "work", "--repo", api, "--repo", redis}
			}
			if out, _, err := run(t, fakeEnv(env), args...); err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			m := loadEnv(t, home, "work")
			got := instancesOf(m, "hmd-inf-redis")
			if len(got) != 1 || got[0].SourceType() != manifest.SourceLocal || got[0].Source.Path != redis {
				t.Errorf("redis instances = %+v, want exactly the working tree", got)
			}
			if _, ok := m.Repo("ms-myapi"); !ok {
				t.Errorf("myapi not declared: %v", instanceNames(m))
			}
		})
	}
}

func TestATemplateInstanceOfAnotherClassUnderAWantedNameIsAConflict(t *testing.T) {
	t.Parallel()
	home, env := fromRepoEnv(t)
	saveTemplate(t, home, "base", artifactInstance("cache", "hmd-inf-memcached", "1.0.0"))
	repo := subjectRepo(t, home, subjectManifest)

	_, _, err := run(t, fakeEnv(env), "env", "add", "work", "--template", "base", "--repo", repo)
	if nserr.CodeOf(err) != nserr.Usage {
		t.Fatalf("error = %v, want a usage error", err)
	}
	for _, want := range []string{"cache", "hmd-inf-memcached", "hmd-inf-redis"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
	if _, err := registryEnvironment(t, home, "work"); err == nil {
		t.Error("a refused composition still registered the environment")
	}
}

func TestTwoRepositoriesOfOneClassAreRefused(t *testing.T) {
	t.Parallel()
	_, env := fromRepoEnv(t)
	a, b := redisRepo(t), redisRepo(t)

	_, _, err := run(t, fakeEnv(env), "env", "add", "work", "--repo", a, "--repo", b)
	if nserr.CodeOf(err) != nserr.Usage || !strings.Contains(err.Error(), "hmd-inf-redis") {
		t.Errorf("error = %v, want a usage error naming the class", err)
	}
}

func TestANameIsUsedIfAnyRepositoryUsesIt(t *testing.T) {
	t.Parallel()
	home, env := fromRepoEnv(t)
	redis := redisRepo(t)
	api := subjectRepo(t, home, subjectManifest)

	if out, _, err := run(t, fakeEnv(env), "env", "add", "work", "--repo", redis, "--repo", api,
		"--name", "hmd-ms-myapi=api"); err != nil {
		t.Fatalf("--name used by the second repository only: %v\n%s", err, out)
	}
	if _, ok := loadEnv(t, home, "work").Repo("api"); !ok {
		t.Error("--name did not rename myapi")
	}

	_, _, err := run(t, fakeEnv(env), "env", "add", "other", "--repo", redis, "--name", "nope=x")
	if nserr.CodeOf(err) != nserr.Usage || !strings.Contains(err.Error(), "nope") {
		t.Errorf("unused --name = %v, want a usage error naming it", err)
	}
}

func TestEnvAddCompositionFlagsAreChecked(t *testing.T) {
	t.Parallel()
	home, env := fromRepoEnv(t)
	repo := subjectRepo(t, home, subjectManifest)

	_, _, err := run(t, fakeEnv(env), "env", "add", "work", "--template", "nope")
	if nserr.CodeOf(err) != nserr.Usage || !strings.Contains(err.Error(), "nope") {
		t.Errorf("unknown template = %v, want a usage error", err)
	}
	_, _, err = run(t, fakeEnv(env), "env", "add", "work", "--repo", repo, "--from-repo", repo)
	if nserr.CodeOf(err) != nserr.Usage {
		t.Errorf("--repo with --from-repo = %v, want a usage error", err)
	}
}

// registryEnvironment reads one environment back from the registry.
func registryEnvironment(t *testing.T, home, name string) (*registry.Environment, error) {
	t.Helper()
	reg, err := registry.Load(home, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	return reg.Environment(name, func(string) string { return "" })
}
