package pgupgrade

import (
	"context"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/container"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/floci"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

func onePlan() Plan {
	v := VolumePlan{
		Volume: "floci-rds-db", From: "12", To: "14",
		Image: "hmd-postgres-base:0.3.12", DumpImage: "postgres:12-alpine",
		Backup: BackupVolume("floci-rds-db", "12"),
		Dump:   DumpVolume("floci-rds-db", "12"),
		Helper: HelperContainer("floci-rds-db"),
	}
	v.Steps = steps(v, false)
	return Plan{Image: v.Image, Volumes: []VolumePlan{v}}
}

// A stopped postgres on the volume is exactly what a platform shut down for the
// migration looks like. Refusing it would refuse every correct invocation.
func TestGateAllowsAStoppedContainerOnTheVolume(t *testing.T) {
	d := newFake()
	d.users["floci-rds-db"] = []container.VolumeUser{
		{Name: "floci-rds-db", Running: false, Image: "postgres:12-alpine"},
	}
	if got := Gate(context.Background(), d, onePlan()); len(got) != 0 {
		t.Errorf("a stopped container must not block: %+v", got)
	}
}

// A running one is corruption waiting to happen, and --force must not reach it.
// The Python checks for this not at all.
func TestGateRefusesARunningContainerAndForceCannotBypassIt(t *testing.T) {
	d := newFake()
	d.users["floci-rds-db"] = []container.VolumeUser{
		{Name: "floci-rds-db", Running: true, Image: "postgres:12-alpine"},
	}
	blockers := Gate(context.Background(), d, onePlan())
	if len(blockers) != 1 {
		t.Fatalf("want one blocker, got %+v", blockers)
	}
	if blockers[0].Forcible {
		t.Error("a container running on the volume must not be forcible")
	}
	for _, force := range []bool{false, true} {
		err := Refuse(blockers, force, "")
		if err == nil {
			t.Fatalf("--force=%v must not proceed past a running container", force)
		}
		if code := nserr.CodeOf(err); code != nserr.InUse {
			t.Errorf("exit code = %v, want InUse", code)
		}
		if !strings.Contains(err.Error(), "floci-rds-db") {
			t.Errorf("the refusal does not name what is running: %v", err)
		}
	}
}

// Our own helper, still running, is wreckage from a crashed run rather than
// someone else's server. Refusing on it would make every crash unrecoverable.
func TestGateIgnoresOurOwnLeftoverHelper(t *testing.T) {
	d := newFake()
	helper := HelperContainer("floci-rds-db")
	d.users["floci-rds-db"] = []container.VolumeUser{
		{Name: helper, Running: true, Image: "postgres:12-alpine"},
	}
	if got := Gate(context.Background(), d, onePlan()); len(got) != 0 {
		t.Errorf("a leftover helper must not block: %+v", got)
	}
	if err := clearHelpers(context.Background(), d, onePlan().Volumes[0]); err != nil {
		t.Fatal(err)
	}
	if d.firstMatching("rm -f "+helper) < 0 {
		t.Error("the leftover helper was never removed")
	}
}

// Floci does not hold the directory, but it restarts databases on demand, so
// the default is to refuse -- and a user who can see it is idle may override.
func TestGateRefusesARunningFlociButForceProceeds(t *testing.T) {
	d := newFake()
	d.running[floci.ContainerName] = true

	blockers := Gate(context.Background(), d, onePlan())
	if len(blockers) != 1 || !blockers[0].Forcible {
		t.Fatalf("want one forcible blocker, got %+v", blockers)
	}
	err := Refuse(blockers, false, "")
	if err == nil {
		t.Fatal("a running Floci must be refused by default")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("the refusal does not mention the override: %v", err)
	}
	if err := Refuse(blockers, true, ""); err != nil {
		t.Errorf("--force must proceed past a running Floci: %v", err)
	}
}

// The remedy names the stop sequence, and the caller's extra line -- which is
// where the running environments get named.
func TestRefuseCarriesTheCallersRemedy(t *testing.T) {
	d := newFake()
	d.running[floci.ContainerName] = true
	err := Refuse(Gate(context.Background(), d, onePlan()), false, "Running environments: local, nerd013")
	if err == nil {
		t.Fatal("want a refusal")
	}
	for _, want := range []string{"nsctl env stop", "nsctl control-plane stop", "nerd013"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not carry %q: %v", want, err)
		}
	}
}
