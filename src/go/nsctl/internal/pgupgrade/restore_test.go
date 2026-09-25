package pgupgrade

import (
	"strings"
	"testing"
)

// The errors a correct restore produces against an image whose dbs_init.sh has
// already made some of what the dump recreates. None of these is a failure.
func TestClassifyPassesTheErrorsACorrectRestoreMakes(t *testing.T) {
	stderr := strings.Join([]string{
		`psql:/dump/dumpall.sql:14: ERROR:  role "postgres" already exists`,
		`psql:/dump/dumpall.sql:15: ERROR:  current user cannot be dropped`,
		`psql:/dump/dumpall.sql:31: ERROR:  database "hmd" already exists`,
		`psql:/dump/dumpall.sql:44: ERROR:  role "reader" does not exist`,
		`psql:/dump/dumpall.sql:51: ERROR:  must be member of role "hmd_owner"`,
		`psql:/dump/dumpall.sql:60: ERROR:  cannot drop the currently open database`,
		`NOTICE:  schema "public" does not exist, skipping`,
		`WARNING:  no privileges were granted for "public"`,
		`DETAIL:  Key (oid)=(16384) is not present.`,
		`HINT:  Use DROP ... CASCADE.`,
		``,
	}, "\n")
	if fatal := classify(stderr); len(fatal) != 0 {
		t.Errorf("a correct restore was reported as failed: %q", fatal)
	}
}

// Everything outside the allowlist fails the command. The Python logs these and
// still reports "Migrated 1 of 1", which is how a restore that achieved nothing
// looks exactly like one that worked.
func TestClassifyFailsOnAnythingElse(t *testing.T) {
	for _, line := range []string{
		`psql:/dump/dumpall.sql:9: ERROR:  syntax error at or near "CREATE"`,
		`psql:/dump/dumpall.sql:9: ERROR:  could not extend file "base/16384/2601": No space left on device`,
		`psql:/dump/dumpall.sql:9: ERROR:  out of memory`,
		`psql: error: connection to server failed: FATAL:  the database system is starting up`,
		`psql:/dump/dumpall.sql:9: PANIC:  could not write to file`,
		// A FATAL is never benign, whatever words follow it: the server is
		// refusing or dying, not reporting an object that was already there.
		`psql: FATAL:  role "postgres" does not exist`,
	} {
		if fatal := classify(line); len(fatal) != 1 {
			t.Errorf("classify(%q) = %q, want it treated as fatal", line, fatal)
		}
	}
}

// The allowlist matches the message, not an object that happens to be named
// after it -- a database called "already exists" would otherwise excuse every
// error mentioning it.
func TestClassifyJudgesTheLevelNotTheText(t *testing.T) {
	if fatal := classify(`psql:/dump/dumpall.sql:2: LOG:  statement: CREATE DATABASE "ERROR: x"`); len(fatal) != 0 {
		t.Errorf("a LOG line is not an error: %q", fatal)
	}
}

// The restore is checked against what the old cluster actually held, read back
// from the new one. This check parses nothing, which is why it is the one that
// decides.
func TestInventoryReportsOnlyLosses(t *testing.T) {
	old := Inventory{Databases: []string{"hmd", "trino"}, Roles: []string{"hmd", "reader"}}

	// dbs_init.sh adds objects of its own; extras are the normal case.
	now := Inventory{
		Databases: []string{"hmd", "postgres", "trino"},
		Roles:     []string{"hmd", "postgres", "reader"},
	}
	if lost := old.missingFrom(now); len(lost) != 0 {
		t.Errorf("extras must not be reported as losses: %q", lost)
	}

	partial := Inventory{Databases: []string{"hmd"}, Roles: []string{"hmd"}}
	lost := old.missingFrom(partial)
	if len(lost) != 2 {
		t.Fatalf("want the database and the role reported lost, got %q", lost)
	}
	if !strings.Contains(strings.Join(lost, " "), `database "trino"`) ||
		!strings.Contains(strings.Join(lost, " "), `role "reader"`) {
		t.Errorf("the losses are not named: %q", lost)
	}
}

func TestParseList(t *testing.T) {
	got := parseList("trino\n\n hmd \nhmd\n")
	if len(got) != 2 || got[0] != "hmd" || got[1] != "trino" {
		t.Errorf("parseList = %q, want [hmd trino]", got)
	}
}
