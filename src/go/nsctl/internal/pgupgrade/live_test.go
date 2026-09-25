//go:build docker

// This is the only test that ever exercises the dump and the restore against a
// real postgres. Everything else here runs behind a fake daemon, which proves
// the order of operations but cannot prove that what comes out the far end is
// the data that went in. The Python's forty-one tests cover detection only and
// never call upgrade_volume at all, which is how its restore came to report
// success on a cluster it had not written to.
//
// Self-contained: its own throwaway volume, two public images, no HMD_HOME, and
// nothing on this machine is read or changed. Run it with `make test-pgupgrade`.

package pgupgrade

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/container"
)

const (
	oldImage = "postgres:12-alpine"
	newImage = "postgres:14-alpine"
	// The password is the point: pg_dumpall carries role passwords as hashes
	// in its globals, and a migration that loses them leaves every service
	// authenticating against the database broken in a way the data itself
	// does not show.
	rolePassword = "hunter2"
)

func TestLiveMigrationPreservesATableAndARolePassword(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	d := container.New()
	if err := d.Available(ctx); err != nil {
		t.Skipf("no container engine: %v", err)
	}

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	volume := "hmd-pgupgrade-live-" + suffix
	v := VolumePlan{
		Volume: volume, From: "12", To: "14",
		Image: newImage, DumpImage: oldImage,
		Backup: BackupVolume(volume, "12"),
		Dump:   DumpVolume(volume, "12"),
		Helper: HelperContainer(volume),
	}
	v.Steps = steps(v, false)

	seed := "hmd-pgupgrade-seed-" + suffix
	t.Cleanup(func() {
		clean, stop := context.WithTimeout(context.Background(), 2*time.Minute)
		defer stop()
		_ = d.RemoveContainer(clean, seed)
		_ = d.RemoveContainer(clean, v.Helper)
		for _, name := range []string{volume, v.Backup, v.Dump} {
			_, _, _ = d.Run(clean, "volume", "rm", "-f", name)
		}
	})

	// A PostgreSQL 12 cluster with something in it worth keeping.
	startAndWait(ctx, t, d, seed, oldImage, volume)
	sql(ctx, t, d, seed, fmt.Sprintf("CREATE ROLE app LOGIN PASSWORD '%s'", rolePassword))
	// One statement per call: psql -c wraps everything it is handed in a
	// single transaction, and CREATE DATABASE cannot run inside one.
	sql(ctx, t, d, seed, "CREATE DATABASE appdb OWNER app")
	sqlIn(ctx, t, d, seed, "appdb", "CREATE TABLE widget (id int primary key, name text)")
	sqlIn(ctx, t, d, seed, "appdb", "INSERT INTO widget VALUES (1, 'kept'), (2, 'also kept')")
	hashBefore := strings.TrimSpace(query(ctx, t, d, seed, "postgres",
		"SELECT rolpassword FROM pg_authid WHERE rolname = 'app'"))
	if hashBefore == "" {
		t.Fatal("the seed role has no stored password, so this test would prove nothing")
	}
	if _, _, err := d.Run(ctx, "rm", "-f", seed); err != nil {
		t.Fatal(err)
	}

	// The thing under test.
	e := &Executor{
		Docker: d, Timeouts: DefaultTimeouts(),
		Warn: func(format string, args ...any) { t.Logf("warning: "+format, args...) },
		Progress: func(volume string, step Step) {
			t.Logf("%s: %s", volume, step.What)
		},
	}
	if err := e.Run(ctx, Plan{Image: newImage, Volumes: []VolumePlan{v}}); err != nil {
		t.Fatalf("the migration failed: %v", err)
	}

	// What came out the far end, read with the new major.
	check := "hmd-pgupgrade-check-" + suffix
	t.Cleanup(func() {
		clean, stop := context.WithTimeout(context.Background(), time.Minute)
		defer stop()
		_ = d.RemoveContainer(clean, check)
	})
	startAndWait(ctx, t, d, check, newImage, volume)

	if got := strings.TrimSpace(query(ctx, t, d, check, "postgres", "SHOW server_version_num")); !strings.HasPrefix(got, "14") {
		t.Fatalf("the cluster reports server_version_num %q, so it is not running PostgreSQL 14", got)
	}
	if got := strings.TrimSpace(query(ctx, t, d, check, "appdb", "SELECT count(*) FROM widget")); got != "2" {
		t.Errorf("the table came back with %q rows, want 2", got)
	}
	if got := strings.TrimSpace(query(ctx, t, d, check, "appdb", "SELECT name FROM widget WHERE id = 2")); got != "also kept" {
		t.Errorf("the row's contents did not survive: %q", got)
	}
	hashAfter := strings.TrimSpace(query(ctx, t, d, check, "postgres",
		"SELECT rolpassword FROM pg_authid WHERE rolname = 'app'"))
	if hashAfter != hashBefore {
		t.Errorf("the role's stored password changed across the migration:\n  before %q\n  after  %q", hashBefore, hashAfter)
	}

	// And the backup is still there, because the whole design says it is.
	if !volumeHasData(ctx, d, v.Backup, oldImage) {
		t.Errorf("%s is empty: a migration must leave its backup behind", v.Backup)
	}
}

func startAndWait(ctx context.Context, t *testing.T, d *container.Docker, name, image, volume string) {
	t.Helper()
	_ = d.RemoveContainer(ctx, name)
	if _, stderr, err := d.Run(ctx, "run", "-d", "--name", name,
		"-v", volume+":"+PGData, "-e", "POSTGRES_PASSWORD=postgres", image); err != nil {
		t.Fatalf("starting %s on %s: %s", image, volume, strings.TrimSpace(string(stderr)))
	}
	deadline := time.Now().Add(3 * time.Minute)
	for {
		if _, _, err := d.Run(ctx, "exec", name, "pg_isready", "-U", "postgres"); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never became ready on %s:\n%s", image, volume, d.Logs(ctx, name, 30))
		}
		time.Sleep(time.Second)
	}
}

func sql(ctx context.Context, t *testing.T, d *container.Docker, name, statement string) {
	t.Helper()
	sqlIn(ctx, t, d, name, "postgres", statement)
}

func sqlIn(ctx context.Context, t *testing.T, d *container.Docker, name, database, statement string) {
	t.Helper()
	_, stderr, err := d.Run(ctx, "exec", name, "psql", "-U", "postgres", "-d", database,
		"-v", "ON_ERROR_STOP=1", "-c", statement)
	if err != nil {
		t.Fatalf("seeding %s: %s", database, strings.TrimSpace(string(stderr)))
	}
}

func query(ctx context.Context, t *testing.T, d *container.Docker, name, database, sql string) string {
	t.Helper()
	stdout, stderr, err := d.Run(ctx, "exec", name, "psql", "-U", "postgres", "-d", database, "-At", "-c", sql)
	if err != nil {
		t.Fatalf("querying %s: %s", database, strings.TrimSpace(string(stderr)))
	}
	return string(stdout)
}
