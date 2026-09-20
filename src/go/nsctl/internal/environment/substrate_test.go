package environment

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// NERD014 SPEC001, as a table: what each mode starts.
func TestPlanForPerMode(t *testing.T) {
	t.Parallel()

	cases := map[manifest.Substrate]startPlan{
		manifest.SubstrateNone: {},
		manifest.SubstrateCore: {Database: true, Graph: true, DBAccount: true, CoreResources: true},
		manifest.SubstrateFull: {Database: true, Graph: true, DBAccount: true, Cluster: true, CoreResources: true},
	}
	for mode, want := range cases {
		if got := planFor(mode); got != want {
			t.Errorf("planFor(%s) = %+v, want %+v", mode, got, want)
		}
	}
}

// An unreadable manifest reads as full, with a warning, never as none.
func TestSubstrateModeReadsFullWhenTheManifestIsBroken(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	dir := filepath.Join(home, "environments")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "local.yaml"), []byte("version: 1\nname: local\nsubstrate: some\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var warned strings.Builder
	opts := testOptions(home, nil)
	opts.Err = &warned

	if got := substrateMode(opts, "local"); got != manifest.SubstrateFull {
		t.Errorf("substrateMode = %q, want full", got)
	}
	if !strings.Contains(warned.String(), "substrate") {
		t.Errorf("no warning about the broken key: %q", warned.String())
	}
}

func TestSubstrateModeIsFullWithNoManifest(t *testing.T) {
	t.Parallel()

	if got := substrateMode(testOptions(t.TempDir(), nil), "local"); got != manifest.SubstrateFull {
		t.Errorf("substrateMode = %q, want full", got)
	}
}

// NERD014 SPEC008: none plus a binding to the core instance is refused before
// anything starts, naming the binding and the fix.
func TestRefuseCoreBindingsUnderNone(t *testing.T) {
	t.Parallel()

	m := &manifest.Manifest{Version: 1, Name: "local", Repos: []manifest.Repo{
		{InstanceName: "my-api", RepoClassName: "hmd-ms-myapi",
			Dependencies: map[string]any{"datadog-lambda": "local-neuronsphere", "db": "my-db"}},
		{InstanceName: "my-worker", RepoClassName: "hmd-ms-worker",
			Dependencies: map[string]any{"cores": []any{"other", "local-neuronsphere"}}},
		{InstanceName: "fine", RepoClassName: "hmd-ms-fine",
			Dependencies: map[string]any{"db": "my-db"}},
	}}

	err := refuseCoreBindings(manifest.SubstrateNone, m)
	if err == nil {
		t.Fatal("a core binding was accepted under none")
	}
	if nserr.CodeOf(err) != nserr.Usage {
		t.Errorf("exit code %d, want usage", nserr.CodeOf(err))
	}
	for _, want := range []string{"my-api.datadog-lambda", "my-worker.cores", "--substrate core"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "fine") {
		t.Errorf("an instance with no core binding was named: %v", err)
	}

	for _, mode := range []manifest.Substrate{manifest.SubstrateCore, manifest.SubstrateFull} {
		if err := refuseCoreBindings(mode, m); err != nil {
			t.Errorf("%s refused a core binding: %v", mode, err)
		}
	}
	if err := refuseCoreBindings(manifest.SubstrateNone, nil); err != nil {
		t.Errorf("no manifest was refused: %v", err)
	}
}
