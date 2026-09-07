package floci

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeContainers implements the Docker surface the lifecycle helpers use.
type fakeContainers struct {
	restarted           []string
	aliasAlreadyPresent bool
	name                string
	running             bool
	started             []string
	aliased             []string
	startErr            error
	psqlAfter           int
	psqlCalls           int
}

func (f *fakeContainers) FlociContainer(context.Context, string, string, string) (string, bool) {
	return f.name, f.running
}

func (f *fakeContainers) Start(_ context.Context, name string) error {
	if f.startErr != nil {
		return f.startErr
	}
	f.started = append(f.started, name)
	f.running = true
	return nil
}

func (f *fakeContainers) EnsureNetworkAlias(_ context.Context, name, alias, _ string) (bool, error) {
	f.aliased = append(f.aliased, name+"="+alias)
	// The cold-start shape by default: the alias was not there, so attaching it
	// reconnected the container and renumbered it.
	return !f.aliasAlreadyPresent, nil
}

func (f *fakeContainers) Restart(_ context.Context, name string) error {
	f.restarted = append(f.restarted, name)
	return nil
}

func (f *fakeContainers) Exec(_ context.Context, _ string, _ ...string) ([]byte, error) {
	f.psqlCalls++
	if f.psqlCalls > f.psqlAfter {
		return nil, nil
	}
	return nil, errors.New("could not connect")
}

// Floci leaves the backing container stopped on shutdown and nothing restarts
// it -- wait_for_rds_instance only polls. Without this the database stays down
// while its instance reports available.
func TestEnsureRDSRunningStartsAStoppedContainer(t *testing.T) {
	t.Parallel()

	f := &fakeContainers{name: "floci-rds-db-ABC", running: false}
	got, err := EnsureRDSRunning(context.Background(), f, "000000000000", "cp-db", "hmd_db", "net", time.Second, time.Millisecond)
	if err != nil {
		t.Fatalf("EnsureRDSRunning: %v", err)
	}
	if got != "floci-rds-db-ABC" {
		t.Errorf("container = %q", got)
	}
	if len(f.started) != 1 {
		t.Errorf("started = %v, want the stopped container", f.started)
	}
	if len(f.aliased) != 1 || f.aliased[0] != "floci-rds-db-ABC=hmd_db" {
		t.Errorf("aliased = %v, want the canonical name attached", f.aliased)
	}
}

func TestEnsureRDSRunningLeavesARunningContainerAlone(t *testing.T) {
	t.Parallel()

	f := &fakeContainers{name: "floci-rds-db-ABC", running: true}
	if _, err := EnsureRDSRunning(context.Background(), f, "acct", "id", "hmd_db", "net", time.Second, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if len(f.started) != 0 {
		t.Errorf("started %v, want nothing", f.started)
	}
}

// Nothing was ever deployed here; the caller decides whether that is fatal.
func TestEnsureRDSRunningReportsNoContainerWithoutFailing(t *testing.T) {
	t.Parallel()

	got, err := EnsureRDSRunning(context.Background(), &fakeContainers{}, "acct", "id", "hmd_db", "net", time.Second, time.Millisecond)
	if err != nil {
		t.Errorf("EnsureRDSRunning: %v", err)
	}
	if got != "" {
		t.Errorf("container = %q, want empty", got)
	}
}

// Floci reports an instance available as soon as it has recorded it, while the
// Postgres behind it may still be running its first-boot init.
func TestWaitForPostgresPollsUntilItAccepts(t *testing.T) {
	t.Parallel()

	f := &fakeContainers{psqlAfter: 2}
	if err := WaitForPostgres(context.Background(), f, "c", 10*time.Second, time.Millisecond); err != nil {
		t.Fatalf("WaitForPostgres: %v", err)
	}
	if f.psqlCalls < 3 {
		t.Errorf("gave up after %d attempts", f.psqlCalls)
	}
}

func TestWaitForPostgresTimesOut(t *testing.T) {
	t.Parallel()

	f := &fakeContainers{psqlAfter: 1000}
	err := WaitForPostgres(context.Background(), f, "c", 10*time.Millisecond, time.Millisecond)
	if err == nil {
		t.Fatal("WaitForPostgres succeeded against a database that never answered")
	}
	if !strings.Contains(err.Error(), "c") {
		t.Errorf("error %q does not name the container", err)
	}
}

// The identifier must match what the CDKTF overlay derives, because the
// container is looked up by it to attach the hmd_db alias.
func TestControlPlaneDBIdentifier(t *testing.T) {
	t.Parallel()

	names := Names{DeploymentID: "ignored", Environment: "ignored", Region: "reg1", CustomerCode: "hmdtr1"}
	// Observed on a live install as the io.floci.resource-id label.
	want := "control-plane-db-hmd-postgres-rds-cp-local-reg1-hmdtr1"
	if got := ControlPlaneDBIdentifier(names); got != want {
		t.Errorf("ControlPlaneDBIdentifier = %q, want %q", got, want)
	}
}

func TestEnvDBIdentifier(t *testing.T) {
	t.Parallel()

	names := Names{DeploymentID: "local", Environment: "local", Region: "reg1", CustomerCode: "hmdtr1"}
	want := "environment-db-hmd-postgres-rds-local-local-reg1-hmdtr1"
	if got := EnvDBIdentifier(names); got != want {
		t.Errorf("EnvDBIdentifier = %q, want %q", got, want)
	}
}

// The sixth defect a cold start found, and the one that looked least like a
// defect: `docker ps` reported the graph healthy and its own log showed a
// working Gremlin server, while every client got connection refused.
//
// Attaching the alias disconnects and reconnects the container, which renumbers
// it. The Gremlin server binds the container's specific address rather than
// 0.0.0.0, so it kept listening on the address it had at startup while
// `global-graph` resolved to the new one.
func TestTheGraphIsRestartedOntoTheAddressAliasingGaveIt(t *testing.T) {
	t.Parallel()

	f := &fakeContainers{name: "floci-neptune-cp", running: true}
	name, err := EnsureNeptuneRunning(context.Background(), f, "000000000000", "cp-graph",
		"global-graph", "neuronsphere_default")
	if err != nil {
		t.Fatalf("EnsureNeptuneRunning: %v", err)
	}
	if name != "floci-neptune-cp" {
		t.Fatalf("name = %q", name)
	}
	if len(f.restarted) != 1 || f.restarted[0] != "floci-neptune-cp" {
		t.Errorf("restarted = %v, want the graph once: a reconnect moved it and the server did not follow", f.restarted)
	}
}

// And not otherwise. On a warm start the alias is already attached, nothing
// reconnects, and restarting the graph would drop every open Gremlin
// connection for no reason at all.
func TestAWarmStartDoesNotRestartTheGraph(t *testing.T) {
	t.Parallel()

	f := &fakeContainers{name: "floci-neptune-cp", running: true, aliasAlreadyPresent: true}
	if _, err := EnsureNeptuneRunning(context.Background(), f, "000000000000", "cp-graph",
		"global-graph", "neuronsphere_default"); err != nil {
		t.Fatalf("EnsureNeptuneRunning: %v", err)
	}
	if len(f.restarted) != 0 {
		t.Errorf("restarted = %v on a warm start, want none", f.restarted)
	}
}

// The database is deliberately left alone either way: Postgres listens on
// 0.0.0.0, so the address it was given is not the address it serves on.
func TestTheDatabaseIsNotRestartedByAliasing(t *testing.T) {
	t.Parallel()

	f := &fakeContainers{name: "floci-rds-db", running: true}
	if _, err := EnsureRDSRunning(context.Background(), f, "000000000000", "env-db",
		"hmd_db", "neuronsphere_default", time.Second, time.Millisecond); err != nil {
		t.Fatalf("EnsureRDSRunning: %v", err)
	}
	if len(f.restarted) != 0 {
		t.Errorf("restarted = %v, want none: Postgres does not bind its container IP", f.restarted)
	}
}
