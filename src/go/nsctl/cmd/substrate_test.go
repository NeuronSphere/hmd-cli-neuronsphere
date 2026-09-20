package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/status"
)

// NERD014 SPEC003: a mistyped mode is a usage error before the control plane
// starts. Driving the real `env start` would start one, so the test asserts
// the refusal arrives from flag handling -- with nothing recorded -- by
// checking the manifest is untouched afterwards.
func TestEnvStartRefusesABadSubstrateBeforeStartingAnything(t *testing.T) {
	t.Parallel()

	home, env := repoEnv(t)
	_, _, err := run(t, fakeEnv(env), "env", "start", "local", "--substrate", "bogus")
	if err == nil {
		t.Fatal("a bogus substrate was accepted")
	}
	if got := nserr.CodeOf(err); got != nserr.Usage {
		t.Errorf("code = %d, want %d (usage): %v", got, nserr.Usage, err)
	}
	for _, want := range []string{"bogus", "none", "core", "full"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
	if m, _ := manifest.Load(home, "local", fakeEnv(env)); m != nil {
		t.Errorf("a manifest was written for a refused mode: %+v", m)
	}
}

// NERD014 SPEC002: the mode is recorded in the environment manifest, creating
// the file when there is none, and full is recorded as absence.
func TestRecordSubstrateRoundTrips(t *testing.T) {
	t.Parallel()

	home, env := repoEnv(t, "hmd-ms-myapi")
	opts := &Options{Version: "test"}
	opts.resolve(home, fakeEnv(env), func(string) {})

	if err := recordSubstrate(opts, home, "local", manifest.SubstrateNone); err != nil {
		t.Fatal(err)
	}
	m := readManifest(t, env, "local")
	if m.SubstrateMode() != manifest.SubstrateNone || len(m.Repos) != 0 {
		t.Errorf("recorded %+v, want substrate none with no instances", m)
	}

	// Declaring an instance afterwards keeps the mode.
	if _, _, err := run(t, fakeEnv(env), "repo", "add", "hmd-ms-myapi"); err != nil {
		t.Fatal(err)
	}
	if m := readManifest(t, env, "local"); m.SubstrateMode() != manifest.SubstrateNone {
		t.Errorf("repo add dropped the mode: %+v", m)
	}

	if err := recordSubstrate(opts, home, "local", manifest.SubstrateFull); err != nil {
		t.Fatal(err)
	}
	if m := readManifest(t, env, "local"); m.Substrate != "" {
		t.Errorf("full was recorded as %q, want absence", m.Substrate)
	}
}

// NERD014 SPEC007: repo list prints as substrate rows only what the mode
// deploys.
func TestRepoListSubstrateRowsFollowTheMode(t *testing.T) {
	t.Parallel()

	home, env := repoEnv(t, "hmd-ms-myapi")
	opts := &Options{Version: "test"}
	opts.resolve(home, fakeEnv(env), func(string) {})
	if _, _, err := run(t, fakeEnv(env), "repo", "add", "hmd-ms-myapi"); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		mode        manifest.Substrate
		rows, noRow []string
	}{
		{manifest.SubstrateNone, nil, []string{"local-neuronsphere", "base-vpc", "environment-db", "eks-cluster"}},
		{manifest.SubstrateCore, []string{"local-neuronsphere", "base-vpc", "environment-db"}, []string{"eks-cluster"}},
		{manifest.SubstrateFull, []string{"local-neuronsphere", "base-vpc", "environment-db", "eks-cluster"}, nil},
	}
	for _, c := range cases {
		if err := recordSubstrate(opts, home, "local", c.mode); err != nil {
			t.Fatal(err)
		}
		out, _, err := run(t, fakeEnv(env), "repo", "list")
		if err != nil {
			t.Fatalf("%s: repo list: %v", c.mode, err)
		}
		for _, want := range c.rows {
			if !strings.Contains(out, want) {
				t.Errorf("%s: output lacks %q:\n%s", c.mode, want, out)
			}
		}
		for _, absent := range c.noRow {
			if strings.Contains(out, absent) {
				t.Errorf("%s: output lists %q, which the mode does not deploy:\n%s", c.mode, absent, out)
			}
		}
		if !strings.Contains(out, "ms-myapi") {
			t.Errorf("%s: the declared instance is missing:\n%s", c.mode, out)
		}
	}
}

func TestEnvStatusNamesTheSubstrateAndDropsTheClusterLine(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	renderEnvStatus(&buf, statusSnapshot(manifest.SubstrateNone))
	out := buf.String()
	if !strings.Contains(out, "substrate") || !strings.Contains(out, "none") {
		t.Errorf("status does not name the mode:\n%s", out)
	}
	if strings.Contains(out, "k3s cluster") {
		t.Errorf("a none environment lists its cluster:\n%s", out)
	}

	buf.Reset()
	renderEnvStatus(&buf, statusSnapshot(manifest.SubstrateFull))
	if strings.Contains(buf.String(), "substrate") {
		t.Errorf("full is the default and should not announce itself:\n%s", buf.String())
	}
}

func TestEnvListHasASubstrateColumn(t *testing.T) {
	t.Parallel()

	reg := &registry.Registry{
		DefaultEnv: "local",
		Environments: map[string]registry.Environment{
			"local": {Name: "local", Slug: "local", AccountID: "000000000001", K3sCluster: "ns-local-abc"},
			"dev":   {Name: "dev", Slug: "dev", AccountID: "000000000002", K3sCluster: "ns-dev-abc", PortSlot: 1},
		},
	}
	modes := map[string]manifest.Substrate{"local": manifest.SubstrateNone, "dev": manifest.SubstrateFull}
	var out, errOut bytes.Buffer
	renderEnvList(&out, &errOut, reg, func(slug string) manifest.Substrate { return modes[slug] })

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if !strings.Contains(lines[0], "SUBSTRATE") {
		t.Errorf("header lacks the column: %q", lines[0])
	}
	for _, line := range lines[1:] {
		switch {
		case strings.HasPrefix(line, "local") && !strings.Contains(line, "none"):
			t.Errorf("local row does not say none: %q", line)
		case strings.HasPrefix(line, "dev") && !strings.Contains(line, "full"):
			t.Errorf("dev row does not say full: %q", line)
		}
	}
}

func statusSnapshot(mode manifest.Substrate) status.Environment {
	return status.Environment{
		Name: "local", AccountID: "000000000001", DeploymentID: "local",
		K3sCluster: "ns-local-abc", ComposeProject: "p", StateDir: "/s", Substrate: mode,
		Routes: map[string]string{}, RouteOrder: nil,
	}
}
