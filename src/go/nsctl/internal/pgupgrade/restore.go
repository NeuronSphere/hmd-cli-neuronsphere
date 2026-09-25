package pgupgrade

import (
	"fmt"
	"sort"
	"strings"
)

// benign is a closed allowlist of the psql complaints a correct restore
// produces, and nothing else.
//
// ON_ERROR_STOP stays off, because the new image's baked dbs_init.sh genuinely
// pre-creates roles and databases that pg_dumpall's --clean --if-exists output
// then recreates, and stopping on the first collision would abort every
// migration. But "errors are expected" is not the same as "errors are fine":
// the Python logs a warning and reports the volume migrated, so a restore that
// achieved nothing at all still reads as a success.
//
// An allowlist rather than a denylist because the set of *expected* errors is
// small, enumerable and stable, while the set of things that can go wrong with
// a database restore is neither.
var benign = []string{
	// The object is already there -- dbs_init.sh made it.
	"already exists",
	// --clean emits DROP for objects this cluster never had.
	"does not exist",
	// Role grants replayed in an order the membership does not yet support;
	// the later GRANT in the same dump fixes it.
	"must be member of role",
	// pg_dumpall --clean emits DROP ROLE for the role running the restore.
	"current user cannot be dropped",
	// ...and DROP DATABASE for the one psql is connected to.
	"cannot drop the currently open database",
	// Same, worded differently across majors.
	"is being accessed by other users",
}

// classify splits psql's stderr into the complaints a correct restore makes and
// the ones that mean it failed.
//
// Only lines carrying a server error level are judged. NOTICE and WARNING are
// commentary; DETAIL, HINT, CONTEXT, STATEMENT and LINE are continuations of
// whatever preceded them, and judging them separately would read a benign
// error's hint as an unexplained failure.
//
// FATAL and PANIC are never benign whatever they say: they are the server
// refusing or dying, not an object that was already there.
func classify(stderr string) (fatal []string) {
	for _, raw := range strings.Split(stderr, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		level, ok := errorLevel(line)
		if !ok {
			continue
		}
		if level == "ERROR" && isBenign(line) {
			continue
		}
		fatal = append(fatal, line)
	}
	return fatal
}

// errorLevel is the server error level a psql stderr line carries, if any.
// Lines look like `psql:/dump/dumpall.sql:42: ERROR:  role "x" already exists`.
func errorLevel(line string) (string, bool) {
	for _, level := range []string{"ERROR:", "FATAL:", "PANIC:"} {
		if i := strings.Index(line, level); i >= 0 {
			// Only where it is the message's level, not a database named
			// "fatal:" quoted inside someone else's text.
			if i == 0 || line[i-1] == ' ' || line[i-1] == ':' || line[i-1] == '\t' {
				return strings.TrimSuffix(level, ":"), true
			}
		}
	}
	return "", false
}

func isBenign(line string) bool {
	lower := strings.ToLower(line)
	for _, pattern := range benign {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

// Inventory is what a cluster holds, as read from it rather than inferred from
// the dump.
type Inventory struct {
	Databases []string
	Roles     []string
}

// missingFrom lists what the old cluster held and the new one does not.
//
// One-directional on purpose: the new image's dbs_init.sh creates objects of
// its own, so extras are the normal case and only losses are a failure.
func (old Inventory) missingFrom(now Inventory) []string {
	var lost []string
	lost = append(lost, missing("database", old.Databases, now.Databases)...)
	lost = append(lost, missing("role", old.Roles, now.Roles)...)
	sort.Strings(lost)
	return lost
}

func missing(kind string, before, after []string) []string {
	have := map[string]bool{}
	for _, name := range after {
		have[name] = true
	}
	var lost []string
	for _, name := range before {
		if !have[name] {
			lost = append(lost, fmt.Sprintf("%s %q", kind, name))
		}
	}
	return lost
}

// parseList reads psql -At output into a sorted, de-duplicated list.
func parseList(out string) []string {
	seen := map[string]bool{}
	var names []string
	for _, line := range strings.Split(out, "\n") {
		name := strings.TrimSpace(line)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// The inventory queries. Template databases are excluded because the new
// cluster makes its own, and the pg_ roles because they are the server's, not
// the user's -- in both cases a difference would be the images differing rather
// than data having been lost.
const (
	databasesQuery = "SELECT datname FROM pg_database WHERE NOT datistemplate ORDER BY 1"
	rolesQuery     = "SELECT rolname FROM pg_roles WHERE rolname NOT LIKE 'pg\\_%' ORDER BY 1"
)
