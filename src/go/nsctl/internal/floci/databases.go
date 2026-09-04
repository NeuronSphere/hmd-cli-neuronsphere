package floci

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// CoreDatabase is one control-plane database and the role that owns it.
type CoreDatabase struct {
	Name string
	User string
}

// CoreDatabases are the databases the control plane needs before anything can
// use it.
//
// The control plane has no dbaccount of its own -- dbaccount is
// per-environment, matching the cloud -- so this direct path is the only thing
// that creates them. ms-deployment's own database in particular has to exist
// before ms-deployment can run at all, which rules out provisioning it through
// a service.
var CoreDatabases = []CoreDatabase{
	{"hmd_ms_naming", "hmd_ms_naming"},
	{"hmd_ms_deployment", "hmd_ms_deployment"},
	// The Deployment GUI's Django database. It runs as a control-plane
	// container, so like the two Lambdas above it has no dbaccount.
	{"deployment_gui", "deployment_gui"},
}

// Execer runs a command in a container.
type Execer interface {
	Exec(ctx context.Context, name string, args ...string) ([]byte, error)
}

// EnsureCoreDatabases creates the control-plane databases and their roles.
//
// Idempotent, and safe to run against a database that already has them: the
// role is guarded by a DO block and the database by an existence check, since
// CREATE DATABASE cannot run inside one.
//
// Passwords equal usernames, which is the local convention -- see the local
// database password note in the deployer. Nothing here is reachable from
// outside the Docker network.
func EnsureCoreDatabases(ctx context.Context, d Execer, container string,
	databases []CoreDatabase, timeout, poll time.Duration) error {

	if len(databases) == 0 {
		databases = CoreDatabases
	}
	if err := waitForPsql(ctx, d, container, timeout, poll); err != nil {
		return err
	}

	var failures []string
	for _, db := range databases {
		role := fmt.Sprintf(
			`DO $do$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '%s') `+
				`THEN CREATE ROLE "%s" LOGIN PASSWORD '%s'; END IF; END $do$;`,
			db.User, db.User, db.User)
		if _, err := psql(ctx, d, container, role); err != nil {
			failures = append(failures, fmt.Sprintf("role %s: %v", db.User, err))
			continue
		}

		out, err := psql(ctx, d, container,
			fmt.Sprintf("SELECT 1 FROM pg_database WHERE datname = '%s'", db.Name))
		if err != nil {
			failures = append(failures, fmt.Sprintf("checking for %s: %v", db.Name, err))
			continue
		}
		if strings.TrimSpace(string(out)) != "1" {
			if _, err := psql(ctx, d, container,
				fmt.Sprintf(`CREATE DATABASE "%s" OWNER "%s";`, db.Name, db.User)); err != nil {
				failures = append(failures, fmt.Sprintf("creating %s: %v", db.Name, err))
				continue
			}
		}
		if _, err := psql(ctx, d, container,
			fmt.Sprintf(`GRANT ALL PRIVILEGES ON DATABASE "%s" TO "%s";`, db.Name, db.User)); err != nil {
			failures = append(failures, fmt.Sprintf("granting on %s: %v", db.Name, err))
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("could not ensure the control-plane databases in %s: %s",
			container, strings.Join(failures, "; "))
	}
	return nil
}

// waitForPsql blocks until Postgres accepts a query. On a cold boot the
// container is running while its entrypoint is still initialising, so being up
// is not the same as being ready.
func waitForPsql(ctx context.Context, d Execer, container string, timeout, poll time.Duration) error {
	if poll <= 0 {
		poll = 2 * time.Second
	}
	deadline := time.Now().Add(timeout)
	var last error
	for {
		if _, err := psql(ctx, d, container, "SELECT 1"); err == nil {
			return nil
		} else {
			last = err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s did not accept connections within %s: %w", container, timeout, last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}

// psql runs one statement as the superuser, returning only the value so a
// caller can compare it.
func psql(ctx context.Context, d Execer, container, sql string) ([]byte, error) {
	return d.Exec(ctx, container, "psql", "-U", "postgres", "-tAc", sql)
}
