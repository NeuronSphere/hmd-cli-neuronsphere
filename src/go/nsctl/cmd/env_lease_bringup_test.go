package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/environment"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/lease"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
)

// NERD035 SPEC005: a session acquire composes, places, and brings the
// environment up.

// stubStart replaces startEnvironment for one test. Tests that call it must
// not be parallel: Go resumes parallel tests only after every sequential one
// has finished, so nothing else reads the hook while it is replaced.
func stubStart(t *testing.T, fn func(ctx context.Context, opts *environment.Options, name string) error) {
	t.Helper()
	prev := startEnvironment
	startEnvironment = fn
	t.Cleanup(func() { startEnvironment = prev })
}

func writePool(t *testing.T, home, body string) {
	t.Helper()
	writeFile(t, filepath.Join(home, ".config", "nsctl.toml"), body)
}

func saveEnvManifest(t *testing.T, home string, m *manifest.Manifest) {
	t.Helper()
	m.Version = manifest.Version
	if err := m.Save(manifest.DefaultPath(home, m.Name)); err != nil {
		t.Fatal(err)
	}
}

func markReleased(t *testing.T, home, env string, at time.Time) {
	t.Helper()
	path := filepath.Join(lease.Dir(home), env+".released")
	writeFile(t, path, "")
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

func TestSessionAcquireWritesTheCompositionAndRecordsItOnTheLease(t *testing.T) {
	t.Parallel()
	home, env := fromRepoEnv(t)
	saveTemplate(t, home, "base", artifactInstance("cache", "hmd-inf-redis", "0.2.1"))
	repo := subjectRepo(t, home, subjectManifest)

	out, stderr, err := run(t, fakeEnv(env), "env", "lease", "acquire", "dev", "--session", "--pid", "0",
		"--template", "base", "--repo", repo, "--no-start", "--json")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	l := decodeLease(t, out)
	if l.Env != "dev" || l.Template != "base" || len(l.Repos) != 1 || l.Repos[0] != repo {
		t.Errorf("lease = %+v, want dev with template base and repo %s", l, repo)
	}
	m := loadEnv(t, home, "dev")
	if m.Name != "dev" || m.Template != "base" {
		t.Errorf("manifest name %q template %q", m.Name, m.Template)
	}
	if api, ok := m.Repo("ms-myapi"); !ok || api.SourceType() != manifest.SourceLocal {
		t.Errorf("subject not declared from its tree: %v", instanceNames(m))
	}
}

func TestACompositionThatFailsTakesNoLease(t *testing.T) {
	t.Parallel()
	home, env := fromRepoEnv(t)

	_, _, err := run(t, fakeEnv(env), "env", "lease", "acquire", "--session", "--pid", "0", "--template", "nope", "--no-start")
	if err == nil {
		t.Fatal("acquire with an unknown template succeeded")
	}
	out, _, _ := run(t, fakeEnv(env), "env", "lease", "list")
	if !strings.Contains(out, "No environment is leased") {
		t.Errorf("a refused composition left a lease: %q", out)
	}
	_ = home
}

func TestTemplateAndRepoNeedSession(t *testing.T) {
	t.Parallel()
	_, env := fromRepoEnv(t)

	_, _, err := run(t, fakeEnv(env), "env", "lease", "acquire", "--pid", "0", "--template", "base")
	if err == nil || !strings.Contains(err.Error(), "--session") {
		t.Errorf("--template without --session = %v, want a usage error naming --session", err)
	}
}

func TestSessionManifestKeepsTheEnvironmentsSubstrate(t *testing.T) {
	t.Parallel()
	home, env := fromRepoEnv(t)
	saveEnvManifest(t, home, &manifest.Manifest{Name: "dev", Substrate: string(manifest.SubstrateCore)})
	saveTemplate(t, home, "base", artifactInstance("trino", "hmd-inf-trino", "0.1.4"))

	if _, stderr, err := run(t, fakeEnv(env), "env", "lease", "acquire", "dev", "--session", "--pid", "0",
		"--template", "base", "--no-start", "--no-pull"); err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if m := loadEnv(t, home, "dev"); m.SubstrateMode() != manifest.SubstrateCore {
		t.Errorf("substrate = %q, want the environment's recorded core", m.Substrate)
	}
}

func TestPoolPrefersAnEnvironmentOfTheSameTemplate(t *testing.T) {
	t.Parallel()
	home, env := fromRepoEnv(t)
	writePool(t, home, "[pool]\nsize = 2\nmembers = [\"local\", \"dev\"]\n")
	saveTemplate(t, home, "telemetry", artifactInstance("clickhouse", "hmd-inf-clickhouse", "0.2.0"))
	saveEnvManifest(t, home, &manifest.Manifest{Name: "local", Template: "analytics",
		Repos: []manifest.Repo{artifactInstance("clickhouse", "hmd-inf-clickhouse", "0.2.0")}})
	saveEnvManifest(t, home, &manifest.Manifest{Name: "dev", Template: "telemetry"})
	// local is the warmer and matches the instances; the template still wins.
	markReleased(t, home, "local", time.Now())
	markReleased(t, home, "dev", time.Now().Add(-time.Hour))

	out, stderr, err := run(t, fakeEnv(env), "env", "lease", "acquire", "--session", "--pool", "--pid", "0",
		"--template", "telemetry", "--no-start", "--no-pull", "--json")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if l := decodeLease(t, out); l.Env != "dev" {
		t.Errorf("leased %s, want dev, the telemetry environment", l.Env)
	}
}

func TestPoolAvoidsAnotherSessionsWorkingTrees(t *testing.T) {
	t.Parallel()
	home, env := fromRepoEnv(t)
	writePool(t, home, "[pool]\nsize = 2\nmembers = [\"local\", \"dev\"]\n")
	saveTemplate(t, home, "telemetry", artifactInstance("clickhouse", "hmd-inf-clickhouse", "0.2.0"))
	ch := artifactInstance("clickhouse", "hmd-inf-clickhouse", "0.2.0")
	stale := manifest.Repo{InstanceName: "their-otel", RepoClassName: "hmd-inf-otel-collector",
		Source: &manifest.Source{Type: manifest.SourceLocal, Path: t.TempDir()}}
	saveEnvManifest(t, home, &manifest.Manifest{Name: "local", Template: "telemetry", Repos: []manifest.Repo{ch, stale}})
	saveEnvManifest(t, home, &manifest.Manifest{Name: "dev", Template: "telemetry", Repos: []manifest.Repo{ch}})
	markReleased(t, home, "local", time.Now())
	markReleased(t, home, "dev", time.Now().Add(-time.Hour))

	out, stderr, err := run(t, fakeEnv(env), "env", "lease", "acquire", "--session", "--pool", "--pid", "0",
		"--template", "telemetry", "--no-start", "--no-pull", "--json")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if l := decodeLease(t, out); l.Env != "dev" {
		t.Errorf("leased %s, want dev, the one without another session's working tree", l.Env)
	}
}

func TestBringUpStartsTheLeasedEnvironmentAfterWritingItsManifest(t *testing.T) {
	home, env := fromRepoEnv(t)
	saveTemplate(t, home, "base", artifactInstance("trino", "hmd-inf-trino", "0.1.4"))
	var started string
	stubStart(t, func(_ context.Context, opts *environment.Options, name string) error {
		started = name
		m, err := manifest.Load(opts.Home, name, opts.Lookup)
		if err != nil || m == nil || m.Template != "base" {
			t.Errorf("at start the manifest was %+v, %v; want the composition", m, err)
		}
		return nil
	})

	out, _, err := run(t, fakeEnv(env), "env", "lease", "acquire", "dev", "--session", "--pid", "0",
		"--template", "base", "--no-pull", "--shell")
	if err != nil {
		t.Fatal(err)
	}
	if started != "dev" {
		t.Errorf("started %q, want dev", started)
	}
	// stdout is still nothing but the exports eval reads.
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if !strings.HasPrefix(line, "export ") {
			t.Errorf("stdout carries %q, which eval would try to run", line)
		}
	}
}

func TestAFailedBringUpKeepsTheLease(t *testing.T) {
	home, env := fromRepoEnv(t)
	saveTemplate(t, home, "base", artifactInstance("trino", "hmd-inf-trino", "0.1.4"))
	stubStart(t, func(context.Context, *environment.Options, string) error {
		return errors.New("k3s would not start")
	})

	out, stderr, err := run(t, fakeEnv(env), "env", "lease", "acquire", "dev", "--session", "--pid", "0",
		"--template", "base", "--no-pull", "--shell")
	if err == nil {
		t.Fatal("a failed bring-up exited zero")
	}
	if !strings.Contains(out, "export NSCTL_LEASE_TOKEN=") {
		t.Errorf("the exports were not printed before bring-up: %q", out)
	}
	if !strings.Contains(stderr+err.Error(), "nsctl env apply dev") {
		t.Errorf("the failure does not say how to retry: %v\n%s", err, stderr)
	}
	list, _, _ := run(t, fakeEnv(env), "env", "lease", "list")
	if !strings.Contains(list, "dev") {
		t.Errorf("the lease went with the failed bring-up: %q", list)
	}
}
