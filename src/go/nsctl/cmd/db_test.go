package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/pgupgrade"
	"github.com/spf13/cobra"
)

// An unattended run that meant to pass --yes must fail loudly. Defaulting
// either way is the wrong answer for a command that rewrites databases, and a
// pipe or a CI job is exactly where the wrong default would do it silently.
//
// Asserted here rather than in the CLI contract suite because reaching the
// prompt at all needs a mismatched data directory on the machine, which that
// suite cannot stage.
func TestConfirmUpgradeRefusesWithoutATerminal(t *testing.T) {
	var out, errOut bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetIn(strings.NewReader("yes\n")) // a pipe, not a terminal
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)

	plan := pgupgrade.Plan{Volumes: []pgupgrade.VolumePlan{{Volume: "floci-rds-db"}}}
	if confirmUpgrade(cmd, plan) {
		t.Fatal("a non-terminal stdin must not be read as consent, even when it says yes")
	}
	// And it names the flag that would have been consent, rather than leaving
	// an unattended caller to guess why it stopped.
	if !strings.Contains(errOut.String(), "--yes") {
		t.Errorf("the refusal does not name --yes: %q", errOut.String())
	}
}

// The plan is printed before anything runs, and the one irreversible step is
// marked as such. A destructive command that describes itself only in the past
// tense makes --dry-run the only safe way to read it.
func TestRenderUpgradePlanMarksTheDestructiveStep(t *testing.T) {
	v := pgupgrade.VolumePlan{
		Volume: "floci-rds-db", From: "12", To: "14",
		Steps: []pgupgrade.Step{
			{Kind: pgupgrade.StepDump, What: "pg_dumpall floci-rds-db into hmd-pgdump-x"},
			{Kind: pgupgrade.StepWipe, What: "empty floci-rds-db so the new image can initdb"},
		},
	}
	var out bytes.Buffer
	renderUpgradePlan(&out, pgupgrade.Plan{Image: "hmd-postgres-base:0.3.12", Volumes: []pgupgrade.VolumePlan{v}})

	got := out.String()
	for _, want := range []string{"floci-rds-db", "PostgreSQL 12 -> 14", "pg_dumpall"} {
		if !strings.Contains(got, want) {
			t.Errorf("the plan does not mention %q:\n%s", want, got)
		}
	}
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "empty floci-rds-db") && !strings.Contains(line, "!") {
			t.Errorf("the wipe is not marked as destructive: %q", line)
		}
		if strings.Contains(line, "pg_dumpall") && strings.Contains(line, "!") {
			t.Errorf("a non-destructive step is marked destructive: %q", line)
		}
	}
}

// A resumed migration says so, because the steps it prints are a subset of the
// usual ones and an unexplained short plan reads like a bug.
func TestRenderUpgradePlanSaysWhenItIsResuming(t *testing.T) {
	var out bytes.Buffer
	renderUpgradePlan(&out, pgupgrade.Plan{
		Image: "i",
		Volumes: []pgupgrade.VolumePlan{{
			Volume: "floci-rds-db", From: "12", To: "14", Resume: pgupgrade.StageWiped,
		}},
	})
	if !strings.Contains(out.String(), "wiped") {
		t.Errorf("a resumed plan does not say where it is resuming from:\n%s", out.String())
	}
}
