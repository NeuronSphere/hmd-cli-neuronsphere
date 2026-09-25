package pgupgrade

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/pgcheck"
)

// The invariant this whole package is arranged around: nothing destructive may
// be planned before the dump exists and has been proven whole.
//
// Asserted over the plan rather than over observed side effects, which is the
// reason Plan is separate from Execute at all. If this test is ever weakened,
// the command becomes a way to delete a database.
func TestNoWipeIsPlannedBeforeAProvenDump(t *testing.T) {
	for _, resume := range []Stage{StageNone, StageBackedUp, StageDumped, StageWiped, StageRestored} {
		t.Run(string(resume), func(t *testing.T) {
			v := VolumePlan{
				Volume: "floci-rds-db", From: "12", To: "14",
				Image: "hmd-postgres-base:0.3.12", DumpImage: "postgres:12-alpine",
				Backup: "b", Dump: "d", Helper: "h", Resume: resume,
			}
			v.Steps = steps(v, false)

			wipe := indexOf(v.Steps, StepWipe)
			if wipe < 0 {
				return // nothing destructive planned at all
			}
			dump, check := indexOf(v.Steps, StepDump), indexOf(v.Steps, StepCheckDump)
			if check < 0 {
				t.Fatalf("a wipe is planned with no check that the dump is complete: %v", kinds(v.Steps))
			}
			if check > wipe {
				t.Errorf("the dump is checked after the wipe: %v", kinds(v.Steps))
			}
			if dump >= 0 && dump > wipe {
				t.Errorf("the dump is taken after the wipe: %v", kinds(v.Steps))
			}
		})
	}
}

// A wiped volume is the one state that has no way back: the live directory is
// empty and the dump is the only copy. Resuming it must not re-dump (there is
// nothing to dump) and must not re-wipe.
func TestAWipedVolumeResumesFromTheDump(t *testing.T) {
	v := VolumePlan{Resume: StageWiped, Dump: "d", Helper: "h"}
	got := kinds(steps(v, false))

	for _, unwanted := range []StepKind{StepDump, StepWipe, StepBackup, StepInventory} {
		if contains(got, unwanted) {
			t.Errorf("a wiped volume must not plan %q: %v", unwanted, got)
		}
	}
	for _, wanted := range []StepKind{StepCheckDump, StepRestore, StepVerify} {
		if !contains(got, wanted) {
			t.Errorf("a wiped volume must still plan %q: %v", wanted, got)
		}
	}
}

// A dump whose volume is still intact is re-taken rather than reused. A stale
// dump replayed over data written since is worse than paying the dump cost
// twice, and nothing here can tell the two apart without being asked.
func TestADumpedButUnwipedVolumeIsDumpedAgain(t *testing.T) {
	got := kinds(steps(VolumePlan{Resume: StageDumped}, false))
	if !contains(got, StepDump) {
		t.Errorf("a dumped-but-unwiped volume must be dumped again: %v", got)
	}
	if !contains(got, StepWipe) {
		t.Errorf("a dumped-but-unwiped volume still needs wiping: %v", got)
	}
}

// Backing up again over an existing backup is how the only good copy of the
// data gets replaced with an empty directory -- the Python does exactly this
// on a second run after a crash at the wiped stage.
func TestAnExistingBackupIsKeptNotOverwritten(t *testing.T) {
	v := VolumePlan{Volume: "v", Backup: "hmd-pgbackup-v-pg12", ReuseBackup: true}
	v.Steps = steps(v, false)
	i := indexOf(v.Steps, StepBackup)
	if i < 0 {
		t.Fatalf("the backup step must still be reported: %v", kinds(v.Steps))
	}
	if !strings.Contains(v.Steps[i].What, "keep the existing backup") {
		t.Errorf("the plan does not say the backup is kept: %q", v.Steps[i].What)
	}
}

// A verified migration has nothing left to do but tidy up.
func TestAVerifiedVolumePlansNoWork(t *testing.T) {
	got := kinds(steps(VolumePlan{Resume: StageVerified}, false))
	for _, unwanted := range []StepKind{StepDump, StepWipe, StepRestore, StepVerify, StepBackup} {
		if contains(got, unwanted) {
			t.Errorf("a verified volume must not plan %q: %v", unwanted, got)
		}
	}
	if !contains(got, StepDropDump) {
		t.Errorf("a verified volume should drop its dump: %v", got)
	}
}

// --keep-dump is the only thing that leaves the dump behind.
func TestKeepDumpLeavesTheDumpVolume(t *testing.T) {
	if contains(kinds(steps(VolumePlan{}, true)), StepDropDump) {
		t.Error("--keep-dump must not plan to remove the dump volume")
	}
}

func TestStageOrdering(t *testing.T) {
	if !StageWiped.AtLeast(StageDumped) {
		t.Error("wiped comes after dumped")
	}
	if StageDumped.AtLeast(StageWiped) {
		t.Error("dumped does not come after wiped")
	}
	if !StageNone.AtLeast(StageNone) {
		t.Error("a stage has reached itself")
	}
	// A manifest from another version, or a corrupted one, is no progress --
	// never a stage that would let a resume skip the dump.
	if Stage("half-way").Valid() {
		t.Error("an unknown stage must not be valid")
	}
	if Stage("half-way").AtLeast(StageBackedUp) {
		t.Error("an unknown stage must not count as progress")
	}
}

func TestNames(t *testing.T) {
	v := "floci-rds-db-hmd"
	if got, want := BackupVolume(v, "12"), "hmd-pgbackup-db-hmd-pg12"; got != want {
		t.Errorf("BackupVolume = %q, want %q", got, want)
	}
	if got, want := DumpVolume(v, "12"), "hmd-pgdump-db-hmd-pg12"; got != want {
		t.Errorf("DumpVolume = %q, want %q", got, want)
	}
	// Neither artifact may CONTAIN Floci's prefix, not merely fail to start
	// with it: container.VolumesMatching matches a substring, so a name that
	// carries it anywhere is swept by the detector. Both hold an old-major
	// data directory by definition, so one the detector can see turns a
	// successful migration into a permanent refusal of `nsctl env start`.
	// A live rehearsal found exactly that, with the backup reported as the
	// next thing to migrate.
	for _, name := range []string{BackupVolume(v, "12"), DumpVolume(v, "12")} {
		if strings.Contains(name, pgcheck.VolumePrefix) {
			t.Errorf("%q carries %q, so the detector will sweep it", name, pgcheck.VolumePrefix)
		}
	}
	// And the major must still be readable back off the name, because that is
	// what picks the image that can read the dump before the dump is read.
	if got := majorOf(DumpVolume(v, "12")); got != "12" {
		t.Errorf("majorOf(%q) = %q, want 12", DumpVolume(v, "12"), got)
	}
	// The whole volume name, so two instances that diverge early cannot share
	// a helper and remove each other's out from under them.
	a, b := HelperContainer("floci-rds-alpha-db"), HelperContainer("floci-rds-beta-db")
	if a == b {
		t.Errorf("two volumes share a helper name: %q", a)
	}
	if !IsHelper(a) || IsHelper("floci-rds-alpha-db") {
		t.Errorf("IsHelper does not recognise only our own containers")
	}
}

func TestDumpImageIsTheOldMajorFromUpstream(t *testing.T) {
	if got, want := DumpImage("12"), "postgres:12-alpine"; got != want {
		t.Errorf("DumpImage = %q, want %q", got, want)
	}
	t.Setenv(DumpImageEnv, "mirror.example/postgres:12")
	if got, want := DumpImage("12"), "mirror.example/postgres:12"; got != want {
		t.Errorf("DumpImage with an override = %q, want %q", got, want)
	}
}

func indexOf(steps []Step, kind StepKind) int {
	for i, s := range steps {
		if s.Kind == kind {
			return i
		}
	}
	return -1
}

func kinds(steps []Step) []StepKind {
	out := make([]StepKind, 0, len(steps))
	for _, s := range steps {
		out = append(out, s.Kind)
	}
	return out
}

func contains(ks []StepKind, want StepKind) bool {
	for _, k := range ks {
		if k == want {
			return true
		}
	}
	return false
}

// A --volume that names nothing needing migration is a typo. Quietly migrating
// everything instead is the wrong reading of it, and the one that rewrites
// databases the user did not name.
func TestRestrictRefusesAVolumeThatIsNotAMismatch(t *testing.T) {
	ms := []pgcheck.Mismatch{{Volume: "floci-rds-a"}, {Volume: "floci-rds-b"}}

	got, err := restrict(ms, []string{"floci-rds-b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Volume != "floci-rds-b" {
		t.Errorf("restrict picked %+v, want just floci-rds-b", got)
	}

	_, err = restrict(ms, []string{"floci-rds-typo"})
	if err == nil {
		t.Fatal("a --volume matching nothing must be refused")
	}
	// And it must say what would have worked, because the names are long and
	// the user is reading them off a refusal they did not expect.
	for _, want := range []string{"floci-rds-typo", "floci-rds-a", "floci-rds-b"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}

	if _, err := restrict(nil, []string{"floci-rds-a"}); err == nil {
		t.Error("--volume against an empty mismatch list must be refused")
	}
}

// A crash between the wipe and the restore leaves an empty data directory,
// which has no PG_VERSION, which means the detector cannot see it. Without
// picking the migration back up from its dump volume, the one stage that has
// no way back would also be the one stage the command cannot be pointed at:
// it would report nothing to migrate while the data sat in a dump beside an
// empty directory.
//
// A live rehearsal found exactly this: after emptying the volume by hand,
// `nsctl db upgrade --dry-run` offered to migrate the backup instead.
func TestBuildPlanPicksUpAMigrationTheDetectorCannotSee(t *testing.T) {
	d := newFake()
	volume := "floci-rds-db-aaaaaaaa-1111"
	dump := DumpVolume(volume, "12")
	d.put(dump, DumpFile, d.dumpBody)
	body, err := json.Marshal(State{
		Stage: StageWiped, Volume: volume, From: "12", To: "14",
		Databases: []string{"hmd"}, Roles: []string{"hmd"},
	})
	if err != nil {
		t.Fatal(err)
	}
	d.put(dump, StateFile, string(body))

	plan, err := BuildPlan(context.Background(), d, Options{Image: "new:14"})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Volumes) != 1 {
		t.Fatalf("want the unfinished migration found, got %+v", plan.Volumes)
	}
	v := plan.Volumes[0]
	if v.Volume != volume {
		t.Errorf("found %q, want %q", v.Volume, volume)
	}
	if v.Resume != StageWiped {
		t.Errorf("resume stage = %q, want %q", v.Resume, StageWiped)
	}
	// And it resumes forward rather than starting over on an empty directory.
	got := kinds(v.Steps)
	if contains(got, StepDump) || contains(got, StepWipe) {
		t.Errorf("an empty data directory must not be dumped or wiped again: %v", got)
	}
	if !contains(got, StepRestore) {
		t.Errorf("the dump must still be restored: %v", got)
	}
}

// A finished migration's dump is not an invitation to run it again.
func TestBuildPlanIgnoresAVerifiedDump(t *testing.T) {
	d := newFake()
	dump := DumpVolume("floci-rds-db-aaaaaaaa-1111", "12")
	body, _ := json.Marshal(State{Stage: StageVerified, Volume: "floci-rds-db-aaaaaaaa-1111", From: "12", To: "14"})
	d.put(dump, StateFile, string(body))

	plan, err := BuildPlan(context.Background(), d, Options{Image: "new:14"})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Empty() {
		t.Errorf("a verified migration must not be offered again: %+v", plan.Volumes)
	}
}

// Planning reads. It must not bring into being the very volumes it is asking
// about: `docker run -v name:/path` creates a named volume that is not there,
// so a --dry-run that mounted its way to an answer would leave empty volumes
// behind while printing "Nothing was changed".
func TestBuildPlanMountsNothingThatDoesNotExist(t *testing.T) {
	d := newFake()
	volume := "floci-rds-db-aaaaaaaa-1111"
	d.put(volume, "PG_VERSION", "12")

	if _, err := BuildPlan(context.Background(), d, Options{Image: "new:14"}); err != nil {
		t.Fatal(err)
	}
	for _, call := range d.ran() {
		if !strings.HasPrefix(call, "run ") {
			continue
		}
		for _, absent := range []string{DumpVolume(volume, "12"), BackupVolume(volume, "12")} {
			if strings.Contains(call, absent+":") {
				t.Errorf("planning mounted %q, which does not exist yet: %s", absent, call)
			}
		}
	}
}
