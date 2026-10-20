package cmd

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/envtemplate"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/oci/ocitest"
)

const analyticsTemplate = `version: 1
name: whatever
repos:
  - instance_name: trino
    repo_class_name: hmd-inf-trino
    version: 0.1.4
    source: {type: artifact}
`

func loadTemplate(t *testing.T, home, name string) *manifest.Manifest {
	t.Helper()
	m, err := envtemplate.Load(home, name, fakeEnv(map[string]string{"HMD_HOME": home}))
	if err != nil {
		t.Fatalf("loading template %s: %v", name, err)
	}
	return m
}

func TestTemplateAddFromAFileThenListShowRemove(t *testing.T) {
	t.Parallel()
	home := registryHome(t, twoEnvRegistry)
	env := fakeEnv(map[string]string{"HMD_HOME": home})
	src := filepath.Join(t.TempDir(), "analytics.yaml")
	writeFile(t, src, analyticsTemplate)

	if _, _, err := run(t, env, "template", "add", "analytics", src); err != nil {
		t.Fatalf("template add: %v", err)
	}
	if m := loadTemplate(t, home, "analytics"); m.Name != "analytics" || len(m.Repos) != 1 {
		t.Errorf("stored template = %+v", m)
	}

	out, _, err := run(t, env, "template", "list")
	if err != nil || !strings.Contains(out, "analytics") || !strings.Contains(out, "1 instance") {
		t.Errorf("template list = %q, %v", out, err)
	}
	out, _, err = run(t, env, "template", "show", "analytics")
	if err != nil || !strings.Contains(out, "hmd-inf-trino") || !strings.Contains(out, "name: analytics") {
		t.Errorf("template show = %q, %v", out, err)
	}

	if _, _, err := run(t, env, "template", "remove", "analytics"); err != nil {
		t.Fatalf("template remove: %v", err)
	}
	if envtemplate.Exists(home, "analytics") {
		t.Error("template remove left the file")
	}
	if _, _, err := run(t, env, "template", "remove", "analytics"); nserr.CodeOf(err) != nserr.Usage {
		t.Errorf("removing an absent template = %v, want a usage error", err)
	}
}

func TestTemplateListWithNoneSaysHowToAddOne(t *testing.T) {
	t.Parallel()
	home := registryHome(t, twoEnvRegistry)

	out, _, err := run(t, fakeEnv(map[string]string{"HMD_HOME": home}), "template", "list")
	if err != nil || !strings.Contains(out, "nsctl template add") {
		t.Errorf("template list with none = %q, %v", out, err)
	}
}

func TestTemplateAddRefusesToOverwriteWithoutForce(t *testing.T) {
	t.Parallel()
	home := registryHome(t, twoEnvRegistry)
	env := fakeEnv(map[string]string{"HMD_HOME": home})
	src := filepath.Join(t.TempDir(), "a.yaml")
	writeFile(t, src, analyticsTemplate)

	if _, _, err := run(t, env, "template", "add", "analytics", src); err != nil {
		t.Fatal(err)
	}
	_, _, err := run(t, env, "template", "add", "analytics", src)
	if nserr.CodeOf(err) != nserr.Usage || !strings.Contains(err.Error(), "--force") {
		t.Errorf("second add = %v, want a usage error naming --force", err)
	}
	if _, _, err := run(t, env, "template", "add", "analytics", src, "--force"); err != nil {
		t.Errorf("add --force = %v", err)
	}
}

func TestTemplateAddNeedsExactlyOneSource(t *testing.T) {
	t.Parallel()
	home := registryHome(t, twoEnvRegistry)
	env := fakeEnv(map[string]string{"HMD_HOME": home})
	src := filepath.Join(t.TempDir(), "a.yaml")
	writeFile(t, src, analyticsTemplate)

	for _, args := range [][]string{
		{"template", "add", "analytics"},
		{"template", "add", "analytics", src, "--from-env", "local"},
		{"template", "add", "analytics", "--stack", "x", "--from-env", "local"},
	} {
		if _, _, err := run(t, env, args...); nserr.CodeOf(err) != nserr.Usage {
			t.Errorf("%v = %v, want a usage error", args, err)
		}
	}
}

func TestTemplateAddRefusesABadName(t *testing.T) {
	t.Parallel()
	home := registryHome(t, twoEnvRegistry)
	src := filepath.Join(t.TempDir(), "a.yaml")
	writeFile(t, src, analyticsTemplate)

	_, _, err := run(t, fakeEnv(map[string]string{"HMD_HOME": home}), "template", "add", "../Escape", src)
	if nserr.CodeOf(err) != nserr.Usage {
		t.Errorf("bad name = %v, want a usage error", err)
	}
}

func TestTemplateAddFromEnvDropsWorkingTrees(t *testing.T) {
	t.Parallel()
	home := registryHome(t, twoEnvRegistry)
	env := fakeEnv(map[string]string{"HMD_HOME": home})
	m := &manifest.Manifest{Version: manifest.Version, Name: "local", Path: manifest.DefaultPath(home, "local"),
		Repos: []manifest.Repo{
			{InstanceName: "clickhouse", RepoClassName: "hmd-inf-clickhouse", Version: "0.2.0",
				Source: &manifest.Source{Type: manifest.SourceArtifact}},
			{InstanceName: "my-otel", RepoClassName: "hmd-inf-otel-collector",
				Source: &manifest.Source{Type: manifest.SourceLocal, Path: t.TempDir()}},
		}}
	if err := m.Save(m.Path); err != nil {
		t.Fatal(err)
	}

	_, stderr, err := run(t, env, "template", "add", "telemetry", "--from-env", "local")
	if err != nil {
		t.Fatalf("template add --from-env: %v", err)
	}
	if !strings.Contains(stderr, "my-otel") {
		t.Errorf("stderr %q does not name the dropped working tree", stderr)
	}
	got := loadTemplate(t, home, "telemetry")
	if len(got.Repos) != 1 || got.Repos[0].InstanceName != "clickhouse" {
		t.Errorf("template repos = %+v, want clickhouse only", got.Repos)
	}
}

func TestTemplateAddFromAStackStoresItsInstancesAndRecord(t *testing.T) {
	t.Parallel()
	reg := ocitest.New(t)
	ref := observabilityStack(t, reg)
	home, env := fromRepoEnv(t)

	out, _, err := run(t, fakeEnv(env), "template", "add", "telemetry", "--stack", ref, "--local-url", "http://127.0.0.1:1")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	m := loadTemplate(t, home, "telemetry")
	sink, ok := m.Repo("sink")
	if !ok || sink.RepoClassName != "hmd-inf-s3bucket" || sink.SourceType() != manifest.SourceArtifact {
		t.Errorf("sink = %+v, %v; want the stack's dependency, from its artifact", sink, ok)
	}
	if _, ok := m.Repo("otel"); !ok {
		t.Error("the stack's companion otel is not in the template")
	}
	if len(m.Stacks) != 1 || m.Stacks[0].Version != "0.1.0" {
		t.Errorf("stack records = %+v, want one at 0.1.0", m.Stacks)
	}
	// Nothing reached the environment.
	if envM, _ := manifest.Load(home, "local", fakeEnv(env)); envM != nil && len(envM.Repos) > 0 {
		t.Errorf("template add --stack declared into the environment: %+v", envM.Repos)
	}
}
