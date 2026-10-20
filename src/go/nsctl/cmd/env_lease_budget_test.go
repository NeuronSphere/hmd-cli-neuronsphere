package cmd

import (
	"context"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// NERD035 SPEC008: max_running is a budget a starting session acquire waits
// on and never evicts for.

func TestASessionThatWouldExceedMaxRunningIsRefused(t *testing.T) {
	t.Parallel()
	home, env := fromRepoEnv(t)
	writePool(t, home, "[pool]\nsize = 3\nmembers = [\"local\"]\nmax_running = 1\n")
	saveTemplate(t, home, "base", artifactInstance("trino", "hmd-inf-trino", "0.1.4"))
	if _, _, err := run(t, fakeEnv(env), "env", "lease", "acquire", "local", "--session", "--pid", "0"); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{
		{"env", "lease", "acquire", "--session", "--pool", "--pid", "0", "--template", "base", "--no-pull"},
		{"env", "lease", "acquire", "dev", "--session", "--pid", "0", "--template", "base", "--no-pull"},
	} {
		_, _, err := run(t, fakeEnv(env), args...)
		if nserr.CodeOf(err) != nserr.InUse {
			t.Fatalf("%v = %v (code %d), want InUse", args, err, nserr.CodeOf(err))
		}
		if !strings.Contains(err.Error(), "max_running") || !strings.Contains(err.Error(), "local") {
			t.Errorf("%v error %q does not name the budget and what uses it", args, err)
		}
	}
	out, _, _ := run(t, fakeEnv(env), "env", "lease", "list")
	if strings.Contains(out, "cc-1") || strings.Count(out, "session") != 1 {
		t.Errorf("a refused acquire left a trace: %q", out)
	}
}

func TestTheBudgetIgnoresAcquiresThatStartNothing(t *testing.T) {
	t.Parallel()
	home, env := fromRepoEnv(t)
	writePool(t, home, "[pool]\nsize = 3\nmembers = [\"local\"]\nmax_running = 1\n")
	saveTemplate(t, home, "base", artifactInstance("trino", "hmd-inf-trino", "0.1.4"))
	if _, _, err := run(t, fakeEnv(env), "env", "lease", "acquire", "local", "--session", "--pid", "0"); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{
		{"env", "lease", "acquire", "dev", "--session", "--pid", "0", "--template", "base", "--no-pull", "--no-start"},
		{"env", "lease", "acquire", "--pool", "--pid", "0"},
	} {
		if _, _, err := run(t, fakeEnv(env), args...); err != nil {
			t.Errorf("%v = %v; it starts nothing, so the budget does not apply", args, err)
		}
	}
}

func TestUnderMaxRunningASessionStartsAsBefore(t *testing.T) {
	home, env := fromRepoEnv(t)
	writePool(t, home, "[pool]\nsize = 3\nmembers = [\"local\"]\nmax_running = 2\n")
	saveTemplate(t, home, "base", artifactInstance("trino", "hmd-inf-trino", "0.1.4"))
	stubStart(t, func(context.Context, *Options, string, string, startOptions) error { return nil })
	if _, _, err := run(t, fakeEnv(env), "env", "lease", "acquire", "local", "--session", "--pid", "0"); err != nil {
		t.Fatal(err)
	}

	out, stderr, err := run(t, fakeEnv(env), "env", "lease", "acquire", "--session", "--pool", "--pid", "0",
		"--template", "base", "--no-pull", "--json")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if l := decodeLease(t, out); l.Env != "cc-1" {
		t.Errorf("leased %s, want a new cc-1", l.Env)
	}
}
