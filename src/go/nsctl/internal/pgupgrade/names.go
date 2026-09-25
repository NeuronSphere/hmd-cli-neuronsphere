// Package pgupgrade migrates a Postgres data directory across a major version.
//
// internal/pgcheck detects the mismatch; this performs the repair it names. The
// two are separate packages on purpose. pgcheck is documented as biased in one
// direction -- an image that is not pulled, a Docker that will not answer, an
// uninitialised volume all yield *no* mismatch, because a false alarm that
// refuses a working platform is worse than a missed warning. A destructive
// rewrite needs the opposite bias, stopping at the first thing it cannot
// establish. Two opposite failure biases in one package is how one of them gets
// applied to the wrong function.
//
// The migration is in place: dump with the old image, clear the directory,
// let the new image initdb, restore. Not a volume swap, because Floci
// re-derives the volume name from the container it spawns and would simply
// ignore a differently-named copy.
//
// Two artifacts, two jobs:
//
//   - hmd-pgdump-<volume>-pg<old> holds the SQL dump and the stage manifest.
//     It resumes a migration *forward*.
//   - hmd-pgbackup-<volume>-pg<old> holds a byte copy of the old data
//     directory. It is an old-major directory by definition, so the new image
//     can never read it: it is the rollback, never a resume source.
//
// Both live outside Floci's floci-rds- namespace so pgcheck never rescans them
// and reports a successful migration as a fresh mismatch.
//
// This package must not import internal/runner. The helper containers are run
// directly through the docker CLI, one at a time, with explicit timeouts; the
// compose runner is a different lifecycle model and routing a database rewrite
// through it would put a known-flaky dependency in the destructive path.
package pgupgrade

import (
	"fmt"
	"os"
)

const (
	// PGData is where the postgres images put their data directory.
	PGData = "/var/lib/postgresql/data"

	// DumpDir is where the dump volume is mounted inside a helper.
	DumpDir = "/dump"

	// DumpFile is the dump's name inside the dump volume.
	DumpFile = "dumpall.sql"

	// StateFile is the stage manifest's name inside the dump volume.
	StateFile = "state.json"

	// DumpTrailer is what pg_dumpall writes as its last line. Its presence is
	// the only proof the dump is whole: pg_dumpall exits 0 on a truncated file
	// if the disk fills under it, and the exit status is therefore not enough
	// to authorise the wipe that follows.
	DumpTrailer = "PostgreSQL database cluster dump complete"

	backupPrefix = "hmd-pgbackup-"
	dumpPrefix   = "hmd-pgdump-"
	helperPrefix = "hmd-pg-upgrade-"

	// DumpImageEnv overrides the image used to read the old data directory.
	DumpImageEnv = "HMD_LOCAL_PG_DUMP_IMAGE"
)

// BackupVolume names the byte copy of volume's old data directory.
func BackupVolume(volume, major string) string {
	return fmt.Sprintf("%s%s-pg%s", backupPrefix, volume, major)
}

// DumpVolume names the volume holding the SQL dump and the stage manifest.
func DumpVolume(volume, major string) string {
	return fmt.Sprintf("%s%s-pg%s", dumpPrefix, volume, major)
}

// HelperContainer names the throwaway postgres this runs to dump or restore.
//
// The whole volume name, not a suffix of it. The Python truncates to the last
// twelve characters, which collides for any two Floci instances whose names
// diverge earlier than that -- and a collision here means one migration's
// helper is removed out from under it by another's. Docker's name grammar
// accepts anything a volume name can hold, so there is nothing to shorten for.
func HelperContainer(volume string) string {
	return helperPrefix + volume
}

// IsHelper reports whether a container name is one of ours.
//
// A *running* container on a target volume is normally a refusal. One of these
// is the exception: it is a helper this command left behind when it crashed,
// and removing it is how the retry gets to start.
func IsHelper(name string) bool {
	return len(name) > len(helperPrefix) && name[:len(helperPrefix)] == helperPrefix
}

// DumpImage is an image able to read a data directory written by major.
//
// The *old* postgres, which the configured image by definition is not. Stock
// upstream rather than hmd-postgres-base: the previous NeuronSphere tag may no
// longer exist in any registry the user can reach, and nothing here needs the
// extensions it adds -- reading a data directory and running pg_dumpall are
// core postgres.
func DumpImage(major string) string {
	if override := os.Getenv(DumpImageEnv); override != "" {
		return override
	}
	return fmt.Sprintf("postgres:%s-alpine", major)
}
