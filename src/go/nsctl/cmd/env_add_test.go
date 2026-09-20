package cmd

import (
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
)

func loadReg(t *testing.T, home string) *registry.Registry {
	t.Helper()
	reg, err := registry.Load(home, func(string) string { return "" })
	if err != nil {
		t.Fatalf("loading the registry: %v", err)
	}
	return reg
}

func TestEnvAddRegistersAnEnvironment(t *testing.T) {
	t.Parallel()

	home := registryHome(t, twoEnvRegistry)
	out, _, err := run(t, fakeEnv(map[string]string{"HMD_HOME": home}), "env", "add", "scratch")
	if err != nil {
		t.Fatalf("env add: %v", err)
	}
	// The next step is what makes it usable, and saying so is the difference
	// between a registered environment and a working one.
	if !strings.Contains(out, "nsctl env start scratch") {
		t.Errorf("output does not name the next step:\n%s", out)
	}

	reg := loadReg(t, home)
	env, ok := reg.Environments["scratch"]
	if !ok {
		t.Fatalf("scratch was not registered: %v", reg.Names())
	}
	if env.Slug != "scratch" || env.DeploymentID != "scratch" {
		t.Errorf("environment = %+v", env)
	}
	if env.DBContainer != "hmd_db-scratch" || env.GraphContainer != "global-graph-scratch" {
		t.Errorf("container names = %q / %q", env.DBContainer, env.GraphContainer)
	}
	if env.CoreInstanceName != "local-neuronsphere" {
		t.Errorf("core instance = %q, want the same name every environment uses", env.CoreInstanceName)
	}
	// Registered but not bootstrapped: those are different states and the
	// registry has to be able to tell them apart.
	if env.Bootstrapped() {
		t.Error("a freshly registered environment reports as bootstrapped")
	}
}

// The account and slot are what keep two environments from colliding, so a new
// one must get values neither existing environment holds.
func TestEnvAddAllocatesAFreeAccountAndSlot(t *testing.T) {
	t.Parallel()

	home := registryHome(t, twoEnvRegistry)
	if _, _, err := run(t, fakeEnv(map[string]string{"HMD_HOME": home}), "env", "add", "scratch"); err != nil {
		t.Fatal(err)
	}

	reg := loadReg(t, home)
	scratch := reg.Environments["scratch"]
	for _, other := range []string{"local", "dev"} {
		existing := reg.Environments[other]
		if scratch.AccountID == existing.AccountID {
			t.Errorf("scratch reuses %s's account %s", other, existing.AccountID)
		}
		if scratch.PortSlot == existing.PortSlot {
			t.Errorf("scratch reuses %s's port slot %d", other, existing.PortSlot)
		}
	}
	if scratch.AccountID == registry.ControlPlaneAccountID {
		t.Error("scratch took the control plane's account")
	}
}

func TestEnvAddRefusals(t *testing.T) {
	t.Parallel()

	tests := []struct{ name, arg, want string }{
		{"duplicate", "local", "already exists"},
		{"too long", "abcdefghijklmnopq", "invalid environment name"},
		{"empty", "", "must not be empty"},
		{"underscore", "my_env", "invalid environment name"},
		// Would shadow http://localhost/api/, making the environment's own
		// services unreachable.
		{"reserved", "api", "is reserved"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			home := registryHome(t, twoEnvRegistry)
			_, _, err := run(t, fakeEnv(map[string]string{"HMD_HOME": home}), "env", "add", tt.arg)
			if err == nil {
				t.Fatal("succeeded, want a refusal")
			}
			if got := nserr.CodeOf(err); got != nserr.Usage {
				t.Errorf("exit code = %d, want %d (usage)", got, nserr.Usage)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not mention %q", err, tt.want)
			}
			// A refused add must leave the registry alone.
			if names := loadReg(t, home).Names(); len(names) != 2 {
				t.Errorf("the registry changed: %v", names)
			}
		})
	}
}

// A name is normalised before it is validated, so `Scratch` registers as
// `scratch` -- the same thing `env start Scratch` then resolves to. Matching
// env_registry.validate_slug, which normalises rather than refusing.
func TestEnvAddNormalisesTheName(t *testing.T) {
	t.Parallel()

	home := registryHome(t, twoEnvRegistry)
	if _, _, err := run(t, fakeEnv(map[string]string{"HMD_HOME": home}), "env", "add", "Scratch"); err != nil {
		t.Fatalf("env add: %v", err)
	}
	if _, ok := loadReg(t, home).Environments["scratch"]; !ok {
		t.Errorf("Scratch did not register as scratch: %v", loadReg(t, home).Names())
	}
}

func TestEnvAddDefaultFlagPromotesIt(t *testing.T) {
	t.Parallel()

	home := registryHome(t, twoEnvRegistry)
	if _, _, err := run(t, fakeEnv(map[string]string{"HMD_HOME": home}),
		"env", "add", "scratch", "--default"); err != nil {
		t.Fatal(err)
	}
	if got := loadReg(t, home).DefaultEnv; got != "scratch" {
		t.Errorf("default = %q, want scratch", got)
	}

	// Without the flag the existing default stands.
	home2 := registryHome(t, twoEnvRegistry)
	if _, _, err := run(t, fakeEnv(map[string]string{"HMD_HOME": home2}), "env", "add", "other"); err != nil {
		t.Fatal(err)
	}
	if got := loadReg(t, home2).DefaultEnv; got != "local" {
		t.Errorf("default = %q, want local to stand", got)
	}
}

// Unregistering is destructive to addressability, so it is confirmed.
func TestEnvDeleteRequiresConfirmation(t *testing.T) {
	t.Parallel()

	home := registryHome(t, twoEnvRegistry)
	_, _, err := run(t, fakeEnv(map[string]string{"HMD_HOME": home}), "env", "delete", "dev")
	if err == nil {
		t.Fatal("deleted without confirmation")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Errorf("error does not name the confirmation flag: %v", err)
	}
	if _, still := loadReg(t, home).Environments["dev"]; !still {
		t.Error("dev was removed despite the refusal")
	}
}

func TestEnvDeleteUnregisters(t *testing.T) {
	t.Parallel()

	home := registryHome(t, twoEnvRegistry)
	out, stderr, err := run(t, fakeEnv(map[string]string{"HMD_HOME": home}),
		"env", "delete", "dev", "--yes")
	if err != nil {
		t.Fatalf("env delete: %v", err)
	}
	if !strings.Contains(out, "Unregistered dev") {
		t.Errorf("output = %q", out)
	}
	// State on disk is not silently destroyed, and the user is told.
	if !strings.Contains(stderr, "state directory") {
		t.Errorf("stderr does not mention the state left behind: %q", stderr)
	}

	reg := loadReg(t, home)
	if _, still := reg.Environments["dev"]; still {
		t.Error("dev is still registered")
	}
	if reg.DefaultEnv != "local" {
		t.Errorf("default = %q, want local untouched", reg.DefaultEnv)
	}
}

// A registry naming a default that is not there is worse than one with a
// different default.
func TestEnvDeleteRepointsTheDefault(t *testing.T) {
	t.Parallel()

	home := registryHome(t, twoEnvRegistry)
	if _, _, err := run(t, fakeEnv(map[string]string{"HMD_HOME": home}),
		"env", "delete", "local", "--yes"); err != nil {
		t.Fatalf("env delete: %v", err)
	}
	reg := loadReg(t, home)
	if reg.DefaultEnv != "dev" {
		t.Errorf("default = %q, want the surviving environment", reg.DefaultEnv)
	}
}

func TestEnvDeleteOnAnUnknownEnvironment(t *testing.T) {
	t.Parallel()

	home := registryHome(t, twoEnvRegistry)
	_, _, err := run(t, fakeEnv(map[string]string{"HMD_HOME": home}), "env", "delete", "nope", "--yes")
	if err == nil {
		t.Fatal("deleting an unknown environment succeeded")
	}
	if got := nserr.CodeOf(err); got != nserr.Usage {
		t.Errorf("exit code = %d, want %d (usage)", got, nserr.Usage)
	}
}
