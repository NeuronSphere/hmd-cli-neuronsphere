package floci

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordExec captures the psql statements EnsureCoreDatabases runs and answers
// them from a scripted set of replies.
type recordExec struct {
	mu         sync.Mutex
	statements []string
	// replies maps a substring of a statement to its stdout.
	replies map[string]string
	// failOn makes any statement containing this substring fail.
	failOn string
	// readyAfter makes `SELECT 1` fail this many times first, standing in for
	// a container still running its entrypoint.
	readyAfter int
	selects    int
}

func (r *recordExec) Exec(_ context.Context, _ string, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	sql := args[len(args)-1]
	r.statements = append(r.statements, sql)

	if sql == "SELECT 1" {
		r.selects++
		if r.selects <= r.readyAfter {
			return nil, errors.New("could not connect to server")
		}
		return []byte("1"), nil
	}
	if r.failOn != "" && strings.Contains(sql, r.failOn) {
		return nil, errors.New("permission denied")
	}
	for needle, reply := range r.replies {
		if strings.Contains(sql, needle) {
			return []byte(reply), nil
		}
	}
	return nil, nil
}

func (r *recordExec) ran(substring string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.statements {
		if strings.Contains(s, substring) {
			return true
		}
	}
	return false
}

func TestEnsureCoreDatabasesCreatesEachOne(t *testing.T) {
	t.Parallel()

	exec := &recordExec{}
	if err := EnsureCoreDatabases(context.Background(), exec, "hmd_db", nil, time.Second, time.Millisecond); err != nil {
		t.Fatalf("EnsureCoreDatabases: %v", err)
	}
	for _, db := range CoreDatabases {
		if !exec.ran(`CREATE DATABASE "` + db.Name + `"`) {
			t.Errorf("no CREATE DATABASE for %s", db.Name)
		}
		if !exec.ran("rolname = '" + db.User + "'") {
			t.Errorf("no role guard for %s", db.User)
		}
		if !exec.ran(`GRANT ALL PRIVILEGES ON DATABASE "` + db.Name + `"`) {
			t.Errorf("no grant for %s", db.Name)
		}
	}
}

// CREATE DATABASE cannot run inside a DO block, so it is guarded by an
// existence check instead -- and a database that exists must not be recreated.
func TestEnsureCoreDatabasesSkipsExistingDatabases(t *testing.T) {
	t.Parallel()

	exec := &recordExec{replies: map[string]string{"FROM pg_database": "1"}}
	if err := EnsureCoreDatabases(context.Background(), exec,
		"hmd_db", []CoreDatabase{{"one", "one"}}, time.Second, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if exec.ran("CREATE DATABASE") {
		t.Error("an existing database was recreated")
	}
	// The role guard and the grant still run, so a database created by hand
	// still ends up owned and granted correctly.
	if !exec.ran("GRANT ALL PRIVILEGES") {
		t.Error("the grant was skipped for an existing database")
	}
}

// A container that is up is not necessarily accepting connections: on a cold
// boot it is still running its entrypoint.
func TestEnsureCoreDatabasesWaitsForPostgres(t *testing.T) {
	t.Parallel()

	exec := &recordExec{readyAfter: 3}
	if err := EnsureCoreDatabases(context.Background(), exec,
		"hmd_db", []CoreDatabase{{"one", "one"}}, 5*time.Second, time.Millisecond); err != nil {
		t.Fatalf("EnsureCoreDatabases: %v", err)
	}
	if exec.selects < 4 {
		t.Errorf("gave up after %d attempts", exec.selects)
	}
}

func TestEnsureCoreDatabasesTimesOutWithTheReason(t *testing.T) {
	t.Parallel()

	exec := &recordExec{readyAfter: 1000}
	err := EnsureCoreDatabases(context.Background(), exec,
		"hmd_db", []CoreDatabase{{"one", "one"}}, 20*time.Millisecond, time.Millisecond)
	if err == nil {
		t.Fatal("a database that never accepted connections succeeded")
	}
	for _, want := range []string{"hmd_db", "connect"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// One database failing must not hide the others, and the error has to name
// which of them went wrong.
func TestEnsureCoreDatabasesReportsEveryFailure(t *testing.T) {
	t.Parallel()

	exec := &recordExec{failOn: "CREATE DATABASE"}
	err := EnsureCoreDatabases(context.Background(), exec, "hmd_db",
		[]CoreDatabase{{"one", "one"}, {"two", "two"}}, time.Second, time.Millisecond)
	if err == nil {
		t.Fatal("failures were swallowed")
	}
	for _, want := range []string{"one", "two"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

func TestMajorFromImageRef(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"ghcr.io/hmdlabs/hmd-postgres-base:pg16":        "16",
		"hmd-postgres-base:postgres-15":                 "15",
		"registry:5000/hmd-postgres-base:postgresql-14": "14",
		// A release tag is not a PostgreSQL version. Reading 0 out of "0.2.11"
		// would declare an engine_version that disagrees with the binary.
		"ghcr.io/hmdlabs/hmd-postgres-base:0.2.11": "",
		"hmd-postgres-base:stable":                 "",
		// A port in the registry host must not be read as a tag.
		"localhost:5000/hmd-postgres-base": "",
		"":                                 "",
	}
	for ref, want := range tests {
		if got := MajorFromImageRef(ref); got != want {
			t.Errorf("MajorFromImageRef(%q) = %q, want %q", ref, got, want)
		}
	}
}
