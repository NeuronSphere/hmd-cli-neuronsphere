package cmd

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/artifact"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// A repository declaring one required dependency, one ungated companion and one
// profile-gated companion. Exact specifiers throughout, so `nsctl lock` settles
// it with no environment and no network.
const subjectManifest = `{
  "name": "hmd-ms-myapi",
  "deploy": {"dependencies": {
    "base-vpc":   {"repo_class_name": "hmd-vpc",          "required": "true", "version_spec": "0.1.5"},
    "app-store":  {"repo_class_name": "hmd-inf-s3bucket", "required": "true", "version_spec": "0.1.13"}
  }},
  "local": {"version": 1, "default_profiles": ["transforms"], "repos": [
    {"instance_name": "cache",        "repo_class_name": "hmd-inf-redis",     "version_spec": "0.2.1"},
    {"instance_name": "ms-transform", "repo_class_name": "hmd-ms-transform",  "version_spec": "0.5.201",
     "profiles": ["transforms", "full"]}
  ]}
}`

// subjectRepo writes the manifest, generates its lock, and caches every artifact
// it names so no test here reaches a network.
func subjectRepo(t *testing.T, home, body string) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "meta-data", "manifest.json"), body)
	writeFile(t, filepath.Join(dir, "meta-data", "VERSION"), "0.4.2")
	if _, _, err := run(t, fakeEnv(nil), "lock", dir); err != nil {
		t.Fatalf("lock: %v", err)
	}
	for _, cv := range [][2]string{
		{"hmd-vpc", "0.1.5"}, {"hmd-inf-s3bucket", "0.1.13"},
		{"hmd-inf-redis", "0.2.1"}, {"hmd-ms-transform", "0.5.201"},
	} {
		if _, err := artifact.Store(home, cv[0], cv[1], artifactZip(t, cv[0], cv[1], "cached")); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// loadEnv reads back what was written, through the real loader, so Validate is
// part of every assertion rather than something these tests take on trust.
func loadEnv(t *testing.T, home, slug string) *manifest.Manifest {
	t.Helper()
	m, err := manifest.Load(home, slug, fakeEnv(map[string]string{"HMD_HOME": home}))
	if err != nil {
		t.Fatalf("loading the environment manifest: %v", err)
	}
	if m == nil {
		t.Fatal("no environment manifest was written")
	}
	return m
}

func instanceNames(m *manifest.Manifest) []string {
	var names []string
	for _, r := range m.Repos {
		names = append(names, r.InstanceName)
	}
	sort.Strings(names)
	return names
}

// fromRepoEnv is a registry plus an HMD_REPO_HOME pointed at an empty directory:
// the acceptance criterion's condition, so nothing here can quietly resolve
// through a checkout.
func fromRepoEnv(t *testing.T) (home string, env map[string]string) {
	t.Helper()
	home = registryHome(t, twoEnvRegistry)
	return home, map[string]string{
		"HMD_HOME":                    home,
		"HMD_REPO_HOME":               t.TempDir(),
		"HMD_LOCAL_MS_DEPLOYMENT_URL": "http://127.0.0.1:1",
	}
}

func TestEnvAddFromRepoDeclaresTheActivatedEntries(t *testing.T) {
	t.Parallel()

	home, env := fromRepoEnv(t)
	repo := subjectRepo(t, home, subjectManifest)

	out, _, err := run(t, fakeEnv(env), "env", "add", "scratch", "--from-repo", repo, "--no-pull")
	if err != nil {
		t.Fatalf("env add --from-repo: %v", err)
	}
	m := loadEnv(t, home, "scratch")

	// default_profiles is "transforms", so the gated companion is in. base-vpc
	// is not declared: the substrate creates it, and Validate would refuse a
	// declaration that claimed the name.
	got := instanceNames(m)
	if len(got) != 4 {
		t.Fatalf("declared %v, want four instances (subject, two companions, one dependency)", got)
	}
	for _, name := range []string{"cache", "ms-transform", "app-store", "ms-myapi"} {
		if !contains(got, name) {
			t.Errorf("declared %v, which is missing %q", got, name)
		}
	}
	if contains(got, "base-vpc") {
		t.Error("declared base-vpc, which the substrate creates")
	}
	if !strings.Contains(out, "provided by the substrate") {
		t.Errorf("output does not say why base-vpc is absent:\n%s", out)
	}

	// Every companion and dependency is artifact-sourced at its pinned version;
	// the subject is its working tree, which is what makes it under test.
	for _, r := range m.Repos {
		if r.InstanceName == "ms-myapi" {
			if r.SourceType() != manifest.SourceLocal || r.Source.Path != repo {
				t.Errorf("the subject is %+v, want the working tree at %s", r.Source, repo)
			}
			continue
		}
		if r.SourceType() != manifest.SourceArtifact {
			t.Errorf("%s is %q, want an artifact", r.InstanceName, r.SourceType())
		}
		// manifest.Validate refuses an artifact source with no version, and a
		// lock is exactly where a version-less entry would come from.
		if r.Version == "" {
			t.Errorf("%s carries no version", r.InstanceName)
		}
	}

	// SPEC005 step 5: without this a later bare apply falls back to
	// default_profiles and reconciles away what the user asked for.
	if !reflect.DeepEqual(m.Profiles, []string{"transforms"}) {
		t.Errorf("profiles = %v, want the activated ones recorded", m.Profiles)
	}
	// The repository binds every role it declared, including the substrate's.
	deps := subjectDeps(t, m)
	if deps["base-vpc"] != "base-vpc" || deps["app-store"] != "app-store" {
		t.Errorf("the subject's dependencies are %v, want every declared role bound", deps)
	}
}

// subjectDeps is the repository-under-test's role -> instance map: the binding
// ms-deployment actually resolves a dependency against, and the reason a lock
// never has to name an instance.
func subjectDeps(t *testing.T, m *manifest.Manifest) map[string]string {
	t.Helper()
	for _, r := range m.Repos {
		if r.SourceType() != manifest.SourceLocal {
			continue
		}
		out := map[string]string{}
		for role, target := range r.Dependencies {
			name, _ := target.(string)
			out[role] = name
		}
		return out
	}
	t.Fatal("no local-sourced instance: the repository under test was never declared")
	return nil
}

func TestEnvAddFromRepoProfileSelection(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name  string
		args  []string
		want  []string // instances, sorted
		gated bool
	}{
		{"default_profiles", nil, nil, true},
		{"lean", []string{"--lean"}, nil, false},
		{"an explicit profile", []string{"--profile", "full"}, nil, true},
		{"all profiles", []string{"--all-profiles"}, nil, true},
		{"a profile nothing names", []string{"--profile", "nope"}, nil, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			home, env := fromRepoEnv(t)
			repo := subjectRepo(t, home, subjectManifest)
			args := append([]string{"env", "add", "scratch", "--from-repo", repo, "--no-pull"}, tt.args...)
			if _, _, err := run(t, fakeEnv(env), args...); err != nil {
				t.Fatalf("env add: %v", err)
			}
			got := instanceNames(loadEnv(t, home, "scratch"))

			// Lean needs no declaration: it is what activating nothing gives you.
			for _, always := range []string{"cache", "app-store", "ms-myapi"} {
				if !contains(got, always) {
					t.Errorf("declared %v, missing the unconditional %q", got, always)
				}
			}
			if contains(got, "ms-transform") != tt.gated {
				t.Errorf("declared %v; the gated companion present = %v, want %v",
					got, contains(got, "ms-transform"), tt.gated)
			}
		})
	}
}

// The naming order, which is what lets two engineers share one lock while
// naming their instances differently.
func TestEnvAddFromRepoNaming(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name     string
		args     []string
		instance string
		absent   string
	}{
		// Tier four: the dependency role, which is already how these read.
		{name: "defaults to the role", instance: "app-store"},
		// Tier two: an override.
		{name: "--name overrides it", args: []string{"--name", "app-store=buckets"},
			instance: "buckets", absent: "app-store"},
		// A companion is addressed by its declared instance_name.
		{name: "--name renames a companion", args: []string{"--name", "cache=redis"},
			instance: "redis", absent: "cache"},
		// The subject is addressed by its own repo class.
		{name: "--name renames the subject", args: []string{"--name", "hmd-ms-myapi=api"},
			instance: "api", absent: "ms-myapi"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			home, env := fromRepoEnv(t)
			repo := subjectRepo(t, home, subjectManifest)
			args := append([]string{"env", "add", "scratch", "--from-repo", repo, "--no-pull"}, tt.args...)
			if _, _, err := run(t, fakeEnv(env), args...); err != nil {
				t.Fatalf("env add: %v", err)
			}
			m := loadEnv(t, home, "scratch")
			got := instanceNames(m)
			if !contains(got, tt.instance) {
				t.Errorf("declared %v, want %q", got, tt.instance)
			}
			if tt.absent != "" && contains(got, tt.absent) {
				t.Errorf("declared %v, which still has the default %q", got, tt.absent)
			}
			// However it was named, the role resolves to it -- which is the
			// property the whole naming order exists for.
			if tt.instance == "buckets" && subjectDeps(t, m)["app-store"] != "buckets" {
				t.Errorf("the role does not resolve to the renamed instance: %v", subjectDeps(t, m))
			}
		})
	}
}

// The bindings are what make a rename survive. Recorded in the manifest because
// a lock names no instances and a role is all that travels between machines.
func TestEnvAddFromRepoRecordsItsBindings(t *testing.T) {
	t.Parallel()

	home, env := fromRepoEnv(t)
	repo := subjectRepo(t, home, subjectManifest)
	if _, _, err := run(t, fakeEnv(env), "env", "add", "scratch", "--from-repo", repo,
		"--no-pull", "--name", "cache=redis"); err != nil {
		t.Fatal(err)
	}
	m := loadEnv(t, home, "scratch")
	if m.Bindings["cache"] != "redis" {
		t.Errorf("bindings = %v, want the override recorded", m.Bindings)
	}
	if m.Bindings["hmd-ms-myapi"] != "ms-myapi" {
		t.Errorf("bindings = %v, want the subject bound too", m.Bindings)
	}
	if m.Bindings["base-vpc"] != "base-vpc" {
		t.Errorf("bindings = %v, want the substrate role bound", m.Bindings)
	}
}

// One lock, two environments, two namings -- the cross-machine property, checked
// without needing two machines.
func TestOneLockSupportsTwoNamings(t *testing.T) {
	t.Parallel()

	home, env := fromRepoEnv(t)
	repo := subjectRepo(t, home, subjectManifest)
	lockBytes, err := os.ReadFile(filepath.Join(repo, "neuronsphere.lock"))
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := run(t, fakeEnv(env), "env", "add", "local2", "--from-repo", repo,
		"--no-pull", "--name", "app-store=buckets"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, fakeEnv(env), "env", "add", "dev2", "--from-repo", repo,
		"--no-pull", "--name", "app-store=store"); err != nil {
		t.Fatal(err)
	}

	after, err := os.ReadFile(filepath.Join(repo, "neuronsphere.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if string(lockBytes) != string(after) {
		t.Error("env add rewrote the lock; it is an input, not an output")
	}
	for slug, want := range map[string]string{"local2": "buckets", "dev2": "store"} {
		m := loadEnv(t, home, slug)
		if got := subjectDeps(t, m)["app-store"]; got != want {
			t.Errorf("%s resolves app-store to %q, want %q", slug, got, want)
		}
		// Same class, same pinned version, different instance name.
		for _, r := range m.Repos {
			if r.InstanceName == want && r.Version != "0.1.13" {
				t.Errorf("%s pinned %s at %q, want the locked 0.1.13", slug, want, r.Version)
			}
		}
	}
}

func TestEnvAddFromRepoRefusals(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name   string
		setup  func(t *testing.T, home string) string
		args   []string
		want   string
		noLock bool
	}{
		{
			name: "no lock",
			setup: func(t *testing.T, home string) string {
				dir := t.TempDir()
				writeFile(t, filepath.Join(dir, "meta-data", "manifest.json"), subjectManifest)
				return dir
			},
			want: "nsctl lock",
		},
		{
			name:  "--name addressing nothing",
			setup: func(t *testing.T, home string) string { return subjectRepo(t, home, subjectManifest) },
			args:  []string{"--name", "nowhere=x"},
			want:  "names nothing this repository declares",
		},
		{
			name:  "--name onto a substrate instance",
			setup: func(t *testing.T, home string) string { return subjectRepo(t, home, subjectManifest) },
			args:  []string{"--name", "cache=eks-cluster"},
			want:  "substrate creates",
		},
		{
			name:  "--name with no value",
			setup: func(t *testing.T, home string) string { return subjectRepo(t, home, subjectManifest) },
			args:  []string{"--name", "cache="},
			want:  "instance name is required",
		},
		{
			name:  "two entries with one name",
			setup: func(t *testing.T, home string) string { return subjectRepo(t, home, subjectManifest) },
			args:  []string{"--name", "cache=app-store"},
			want:  "both be called",
		},
		{
			name:  "--lean and --all-profiles together",
			setup: func(t *testing.T, home string) string { return subjectRepo(t, home, subjectManifest) },
			args:  []string{"--lean", "--all-profiles"},
			want:  "opposite things",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			home, env := fromRepoEnv(t)
			repo := tt.setup(t, home)
			args := append([]string{"env", "add", "scratch", "--from-repo", repo, "--no-pull"}, tt.args...)
			_, _, err := run(t, fakeEnv(env), args...)
			if err == nil {
				t.Fatal("succeeded, want a refusal")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not mention %q", err, tt.want)
			}
			if got := nserr.CodeOf(err); got != nserr.Usage {
				t.Errorf("exit code = %d, want %d (usage)", got, nserr.Usage)
			}
			// The registry is not written when the declaration is unreadable: a
			// slot allocated for an environment that was never created is worse
			// than no environment at all.
			if _, statErr := os.Stat(filepath.Join(home, "environments", "scratch.yaml")); statErr == nil {
				t.Error("a refused env add still wrote a manifest")
			}
		})
	}
}

// The lock is an input. An env add that rewrote it would make the file describe
// a machine rather than a repository.
func TestEnvAddFromRepoLeavesTheRepositoryAlone(t *testing.T) {
	t.Parallel()

	home, env := fromRepoEnv(t)
	repo := subjectRepo(t, home, subjectManifest)
	before := snapshot(t, repo)

	if _, _, err := run(t, fakeEnv(env), "env", "add", "scratch", "--from-repo", repo, "--no-pull"); err != nil {
		t.Fatal(err)
	}
	if after := snapshot(t, repo); !reflect.DeepEqual(before, after) {
		t.Errorf("env add modified the repository:\nbefore %v\nafter  %v", before, after)
	}
}

func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		rel, _ := filepath.Rel(dir, path)
		out[rel] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
