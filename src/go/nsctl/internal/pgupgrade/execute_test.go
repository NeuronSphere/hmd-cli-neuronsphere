package pgupgrade

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func exec(d *fakeDocker) *Executor {
	return &Executor{Docker: d, Timeouts: DefaultTimeouts()}
}

// The migration a healthy volume gets, end to end, asserted as an order of
// operations. This is the test that says the command is an upgrade rather than
// a delete.
func TestACleanMigrationNeverDestroysBeforeItHasProvenTheDump(t *testing.T) {
	d := newFake()
	d.put("floci-rds-db", "PG_VERSION", "12")
	p := onePlan()

	if err := exec(d).Run(context.Background(), p); err != nil {
		t.Fatalf("a healthy volume must migrate: %v", err)
	}

	dumped := d.firstMatching("pg_dumpall")
	checked := d.firstMatching("wc -c")
	wiped := d.firstMatching("rm -rf " + PGData)
	restored := d.firstMatching("-f " + DumpDir + "/" + DumpFile)
	backed := d.firstMatching("cp -a")

	for _, c := range []struct {
		name string
		i    int
	}{{"backup", backed}, {"dump", dumped}, {"check", checked}, {"wipe", wiped}, {"restore", restored}} {
		if c.i < 0 {
			t.Fatalf("%s never ran: %v", c.name, d.ran())
		}
	}
	if !(backed < dumped && dumped < checked && checked < wiped && wiped < restored) {
		t.Errorf("out of order: backup=%d dump=%d check=%d wipe=%d restore=%d\n%v",
			backed, dumped, checked, wiped, restored, d.ran())
	}

	// And the old image is fetched before anything at all happens.
	if len(d.pulled) == 0 || d.pulled[0] != "postgres:12-alpine" {
		t.Errorf("the old image was not pulled first: %v", d.pulled)
	}
	if pull := d.firstMatching("pull postgres:12-alpine"); pull > backed {
		t.Errorf("the pull happened after work had begun: %v", d.ran())
	}

	// The manifest reached verified. Read from what was written rather than
	// from the volume, which this run has by then removed along with it.
	if got := lastStageWritten(t, d); got != StageVerified {
		t.Errorf("final stage = %q, want %q", got, StageVerified)
	}
}

// pg_dumpall exits 0 on a truncated file when the disk fills under it. The
// trailer is the only proof, and without it nothing may be destroyed.
func TestATruncatedDumpStopsBeforeTheWipe(t *testing.T) {
	d := newFake()
	d.dumpBody = "-- a dump that stops half\n" // no trailer
	d.put("floci-rds-db", "PG_VERSION", "12")

	err := exec(d).Run(context.Background(), onePlan())
	if err == nil {
		t.Fatal("an incomplete dump must fail the migration")
	}
	if !strings.Contains(err.Error(), "complete dump") {
		t.Errorf("the failure does not say the dump is incomplete: %v", err)
	}
	if i := d.firstMatching("rm -rf " + PGData); i >= 0 {
		t.Fatal("the data directory was cleared despite an incomplete dump")
	}
	// The user's data is untouched, and the message says so.
	if !strings.Contains(err.Error(), "as it was") {
		t.Errorf("the failure does not say the data is intact: %v", err)
	}
}

// The Python creates the backup volume idempotently and then copies over it
// unconditionally, so a second run after a crash at the wiped stage replaces
// the only good copy of the data with an empty directory.
func TestAnExistingBackupIsNeverCopiedOver(t *testing.T) {
	d := newFake()
	p := onePlan()
	d.put(p.Volumes[0].Backup, "PG_VERSION", "12")
	d.put("floci-rds-db", "PG_VERSION", "12")

	var warnings []string
	e := exec(d)
	e.Warn = func(format string, args ...any) { warnings = append(warnings, format) }
	if err := e.Run(context.Background(), p); err != nil {
		t.Fatal(err)
	}

	if i := d.firstMatching("cp -a"); i >= 0 {
		t.Error("an existing backup was copied over")
	}
	if len(warnings) == 0 {
		t.Error("keeping an existing backup must be said out loud")
	}
	if d.files[p.Volumes[0].Backup]["PG_VERSION"] != "12" {
		t.Error("the backup's contents did not survive")
	}
}

// A restore whose stderr carries anything outside the allowlist fails the
// command. The Python warns and reports the volume migrated.
func TestAFailedRestoreFailsTheCommand(t *testing.T) {
	d := newFake()
	d.put("floci-rds-db", "PG_VERSION", "12")
	d.failures["-f "+DumpDir] = `psql:/dump/dumpall.sql:9: ERROR:  out of memory`

	err := exec(d).Run(context.Background(), onePlan())
	if err == nil {
		t.Fatal("a failed restore must fail the migration")
	}
	if !strings.Contains(err.Error(), "out of memory") {
		t.Errorf("the failure does not quote what went wrong: %v", err)
	}
	// And it says where the old data still is.
	if !strings.Contains(err.Error(), "hmd-pgbackup-") {
		t.Errorf("the failure does not name the backup: %v", err)
	}
}

// The check that decides, because it parses nothing: what the old cluster held
// must be found in the new one.
func TestARestoreThatLosesADatabaseIsCaught(t *testing.T) {
	d := newFake()
	d.put("floci-rds-db", "PG_VERSION", "12")

	// psql exits 0 and says nothing, but once the wipe has happened the
	// cluster comes back holding only what the new image's dbs_init.sh made.
	// Nothing in the restore's own output reveals that, which is the point.
	e := exec(d)
	e.Docker = &lossyFake{fakeDocker: d, after: "rm -rf"}

	err := e.Run(context.Background(), onePlan())
	if err == nil {
		t.Fatal("a restore that lost a database must fail")
	}
	if !strings.Contains(err.Error(), `database "trino"`) {
		t.Errorf("the failure does not name what was lost: %v", err)
	}
}

// A volume left wiped by a crash has no way back but the dump, so it resumes
// without being asked -- and must not try to dump an empty directory.
func TestAWipedVolumeResumesWithoutRedumping(t *testing.T) {
	d := newFake()
	p := onePlan()
	dump := p.Volumes[0].Dump
	d.put(dump, DumpFile, d.dumpBody)
	writeStage(t, d, dump, State{
		Stage: StageWiped, Volume: "floci-rds-db", From: "12", To: "14",
		Databases: []string{"hmd", "trino"}, Roles: []string{"hmd", "reader"},
	})

	// Re-plan against that state, the way a fresh invocation would.
	v := p.Volumes[0]
	v.Resume = StageWiped
	v.Steps = steps(v, false)
	p.Volumes[0] = v

	if err := exec(d).Run(context.Background(), p); err != nil {
		t.Fatalf("a wiped volume must resume: %v", err)
	}
	if i := d.firstMatching("pg_dumpall"); i >= 0 {
		t.Error("an empty data directory was dumped again")
	}
	if i := d.firstMatching("rm -rf " + PGData); i >= 0 {
		t.Error("an already-wiped directory was wiped again")
	}
	if i := d.firstMatching("-f " + DumpDir + "/" + DumpFile); i < 0 {
		t.Error("the dump was never restored")
	}
}

// --keep-dump is the only thing that leaves the dump volume behind; otherwise a
// verified migration tidies up after itself.
func TestTheDumpVolumeIsRemovedOnlyWhenNotKept(t *testing.T) {
	d := newFake()
	d.put("floci-rds-db", "PG_VERSION", "12")
	if err := exec(d).Run(context.Background(), onePlan()); err != nil {
		t.Fatal(err)
	}
	if i := d.firstMatching("volume rm hmd-pgdump-"); i < 0 {
		t.Error("a verified migration should remove its dump volume")
	}

	kept := newFake()
	kept.put("floci-rds-db", "PG_VERSION", "12")
	p := onePlan()
	v := p.Volumes[0]
	v.Steps = steps(v, true)
	p.Volumes[0] = v
	if err := exec(kept).Run(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if i := kept.firstMatching("volume rm hmd-pgdump-"); i >= 0 {
		t.Error("--keep-dump must leave the dump volume in place")
	}
}

// No container of ours may be left holding the data directory: Floci wants it
// back, and a helper still mounted is how a migrated volume comes back up
// refusing connections.
func TestTheHelperIsAlwaysRemoved(t *testing.T) {
	d := newFake()
	d.dumpBody = "truncated"
	_ = exec(d).Run(context.Background(), onePlan())

	calls := d.ran()
	helper := HelperContainer("floci-rds-db")
	if !strings.Contains(calls[len(calls)-1], "rm -f "+helper) {
		t.Errorf("the last thing a failed migration does must be to release the volume: %v", calls[len(calls)-1])
	}
}

// lossyFake reports an empty cluster once the wipe has happened, modelling a
// restore that ran but achieved nothing.
type lossyFake struct {
	*fakeDocker
	after string
	seen  bool
}

func (l *lossyFake) Run(ctx context.Context, args ...string) ([]byte, []byte, error) {
	if strings.Contains(strings.Join(args, " "), l.after) {
		l.seen = true
	}
	if l.seen && strings.Contains(strings.Join(args, " "), databasesQuery) {
		l.fakeDocker.calls = append(l.fakeDocker.calls, args)
		return []byte("hmd"), nil, nil
	}
	return l.fakeDocker.Run(ctx, args...)
}

// lastStageWritten decodes the final manifest the run recorded. Reading it off
// the recorded argv rather than out of the volume, because a verified migration
// removes the volume the manifest lived in.
func lastStageWritten(t *testing.T, d *fakeDocker) Stage {
	t.Helper()
	var last Stage
	for _, call := range d.calls {
		for i, a := range call {
			if a != "-c" || !strings.Contains(call[i+1], "base64 -d") {
				continue
			}
			body, err := base64.StdEncoding.DecodeString(strings.Fields(call[i+1])[2])
			if err != nil {
				t.Fatalf("the manifest was not written as base64: %v", err)
			}
			var s State
			if err := json.Unmarshal(body, &s); err != nil {
				t.Fatalf("the manifest is not JSON: %v", err)
			}
			last = s.Stage
		}
	}
	if last == StageNone {
		t.Fatal("no manifest was ever written")
	}
	return last
}

func writeStage(t *testing.T, d *fakeDocker, dump string, s State) {
	t.Helper()
	body, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	d.put(dump, StateFile, string(body))
}
