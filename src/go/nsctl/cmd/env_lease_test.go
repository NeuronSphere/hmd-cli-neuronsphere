package cmd

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/lease"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

func decodeLease(t *testing.T, out string) lease.Lease {
	t.Helper()
	var l lease.Lease
	if err := json.Unmarshal([]byte(out), &l); err != nil {
		t.Fatalf("decoding %q: %v", out, err)
	}
	return l
}

func TestLeaseAcquireTakesTheDefaultAndRefusesTheNextHolder(t *testing.T) {
	t.Parallel()
	home := registryHome(t, twoEnvRegistry)
	env := fakeEnv(map[string]string{"HMD_HOME": home})

	out, _, err := run(t, env, "env", "lease", "acquire", "--holder", "a", "--pid", "0", "--json")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	l := decodeLease(t, out)
	if l.Env != "local" || l.Token == "" || l.Holder != "a" {
		t.Errorf("lease = %+v", l)
	}

	_, _, err = run(t, env, "env", "lease", "acquire", "local", "--holder", "b", "--pid", "0")
	if nserr.CodeOf(err) != nserr.InUse {
		t.Fatalf("second acquire error = %v (code %d), want InUse", err, nserr.CodeOf(err))
	}
	if !strings.Contains(err.Error(), "leased by a") {
		t.Errorf("error %q does not name the holder", err)
	}
}

func TestLeaseHonoursHMDLocalEnv(t *testing.T) {
	t.Parallel()
	home := registryHome(t, twoEnvRegistry)
	env := fakeEnv(map[string]string{"HMD_HOME": home, "HMD_LOCAL_ENV": "dev"})

	out, _, err := run(t, env, "env", "lease", "acquire", "--pid", "0", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if l := decodeLease(t, out); l.Env != "dev" {
		t.Errorf("leased %s, want dev", l.Env)
	}
}

func TestLeaseReleaseTakesTheTokenFromTheEnvironment(t *testing.T) {
	t.Parallel()
	home := registryHome(t, twoEnvRegistry)

	out, _, err := run(t, fakeEnv(map[string]string{"HMD_HOME": home}), "env", "lease", "acquire", "--pid", "0", "--json")
	if err != nil {
		t.Fatal(err)
	}
	l := decodeLease(t, out)

	_, _, err = run(t, fakeEnv(map[string]string{"HMD_HOME": home}), "env", "lease", "release", "local", "--token", "nope")
	if nserr.CodeOf(err) != nserr.Usage {
		t.Errorf("release with a wrong token: %v, want a usage error", err)
	}
	if _, _, err := run(t, fakeEnv(map[string]string{"HMD_HOME": home, "NSCTL_LEASE_TOKEN": l.Token}),
		"env", "lease", "release", "local"); err != nil {
		t.Fatalf("release: %v", err)
	}
	out, _, err = run(t, fakeEnv(map[string]string{"HMD_HOME": home}), "env", "lease", "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "No environment is leased") {
		t.Errorf("list after release:\n%s", out)
	}
}

func TestLeaseListNeverPrintsTokens(t *testing.T) {
	t.Parallel()
	home := registryHome(t, twoEnvRegistry)
	env := fakeEnv(map[string]string{"HMD_HOME": home})

	out, _, err := run(t, env, "env", "lease", "acquire", "--pid", "0", "--json")
	if err != nil {
		t.Fatal(err)
	}
	token := decodeLease(t, out).Token
	listed, _, err := run(t, env, "env", "lease", "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(listed, token) {
		t.Error("lease list --json printed a token")
	}
	if !strings.Contains(listed, `"env": "local"`) {
		t.Errorf("list --json:\n%s", listed)
	}
}

// With no nsctl.toml the pool is local plus one cc-N; local busy means cc-1
// is registered for the run.
func TestPoolLeaseRegistersCC1WhenLocalIsBusy(t *testing.T) {
	t.Parallel()
	home := registryHome(t, twoEnvRegistry)
	env := fakeEnv(map[string]string{"HMD_HOME": home})

	if _, _, err := run(t, env, "env", "lease", "acquire", "local", "--pid", "0"); err != nil {
		t.Fatal(err)
	}
	out, _, err := run(t, env, "env", "lease", "acquire", "--pool", "--pid", "0", "--json")
	if err != nil {
		t.Fatalf("pool acquire: %v", err)
	}
	l := decodeLease(t, out)
	if l.Env != "cc-1" || !l.Created {
		t.Fatalf("lease = %+v, want a created cc-1", l)
	}
	if _, ok := loadReg(t, home).Environments["cc-1"]; !ok {
		t.Error("cc-1 was not registered")
	}

	_, _, err = run(t, env, "env", "lease", "acquire", "--pool", "--pid", "0")
	if nserr.CodeOf(err) != nserr.InUse || !strings.Contains(err.Error(), "--wait") {
		t.Errorf("full pool error = %v, want InUse suggesting --wait", err)
	}
}

func TestPoolLeasePicksTheEnvironmentClosestToTheRun(t *testing.T) {
	t.Parallel()
	home := registryHome(t, twoEnvRegistry)
	env := fakeEnv(map[string]string{"HMD_HOME": home})

	writeFile(t, filepath.Join(home, ".config", "nsctl.toml"), "[pool]\nsize = 2\nmembers = [\"local\", \"dev\"]\n")
	writeFile(t, filepath.Join(manifest.Root(home), "local.yaml"),
		"version: 1\nname: local\nrepos:\n  - instance_name: api\n    repo_class_name: hmd-ms-api\n    source: {type: artifact}\n    version: \"0.1\"\n")
	writeFile(t, filepath.Join(manifest.Root(home), "dev.yaml"),
		"version: 1\nname: dev\nrepos:\n  - instance_name: api\n    repo_class_name: hmd-ms-api\n    source: {type: artifact}\n    version: \"0.2\"\n")
	want := filepath.Join(t.TempDir(), "run.yaml")
	writeFile(t, want,
		"version: 1\nname: run\nrepos:\n  - instance_name: api\n    repo_class_name: hmd-ms-api\n    source: {type: artifact}\n    version: \"0.1\"\n")

	out, _, err := run(t, env, "env", "lease", "acquire", "--pool", "--for", want, "--pid", "0", "--json")
	if err != nil {
		t.Fatal(err)
	}
	// local is already at 0.1. dev would win a tie on name, so this passing
	// means the score decided it.
	if l := decodeLease(t, out); l.Env != "local" {
		t.Errorf("leased %s, want local (already at 0.1)", l.Env)
	}
}

func TestLeaseAcquireRefusesAPoolAndAName(t *testing.T) {
	t.Parallel()
	home := registryHome(t, twoEnvRegistry)
	_, _, err := run(t, fakeEnv(map[string]string{"HMD_HOME": home}), "env", "lease", "acquire", "dev", "--pool")
	if nserr.CodeOf(err) != nserr.Usage {
		t.Errorf("error = %v, want a usage error", err)
	}
}

// The --pid default must not depend on the process that builds the command
// tree, or the generated command reference changes on every run.
func TestLeaseAcquirePidDefaultIsStatic(t *testing.T) {
	t.Parallel()
	f := newEnvLeaseAcquireCommand(&Options{}).Flags().Lookup("pid")
	if f == nil {
		t.Fatal("no --pid flag")
	}
	if f.DefValue != "0" {
		t.Fatalf("--pid default = %q, want a static \"0\"", f.DefValue)
	}
}
