package floci

import (
	"context"
	"fmt"
	"time"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/tools"
)

// Identifiers for the control plane's own Postgres, from bootstrap_dag.
const (
	ControlPlaneDBInstance   = "control-plane-db"
	ControlPlaneDBRepoClass  = "hmd-postgres-rds"
	ControlPlaneDeploymentID = "cp"
	// ControlPlaneDBAlias is the canonical name the whole platform addresses
	// the database by.
	ControlPlaneDBAlias = "hmd_db"
)

// ControlPlaneDBIdentifier is the DBInstanceIdentifier the control-plane
// Postgres node creates.
//
// It must match what the CDKTF overlay derives from HmdCdkTfStack.base_name,
// because the container is looked up by this id to alias it as hmd_db. Its
// environment component is the literal "local", not a slug.
func ControlPlaneDBIdentifier(names Names) string {
	return tools.ResourceIdentifier(
		ControlPlaneDBInstance, ControlPlaneDBRepoClass,
		ControlPlaneDeploymentID, "local", names.Region, names.CustomerCode,
	)
}

// EnvDBIdentifier is the DBInstanceIdentifier an environment's Postgres node
// creates.
func EnvDBIdentifier(names Names) string {
	return tools.ResourceIdentifier(
		"environment-db", "hmd-postgres-rds",
		names.DeploymentID, names.Environment, names.Region, names.CustomerCode,
	)
}

// Containers is the Docker surface the lifecycle helpers need.
type Containers interface {
	FlociContainer(ctx context.Context, service, accountID, resourceID string) (string, bool)
	Start(ctx context.Context, name string) error
	EnsureNetworkAlias(ctx context.Context, name, alias, network string) error
	Exec(ctx context.Context, name string, args ...string) ([]byte, error)
}

// EnsureRDSRunning returns the running container backing an RDS instance,
// starting it if Floci left it stopped.
//
// Not a plain wait: unlike Neptune, Floci does have a start path for RDS, but
// nothing invokes it on a restart -- the Python relies on the bootstrap DAG's
// Terraform apply to bring the instance back. wait_for_rds_instance only polls;
// there is no start_rds_instance. So after a `down` the container sits stopped
// and every service that needs a database fails with a connection error while
// the instance still reports available.
//
// Returns "" rather than an error when there is no container at all: nothing
// was ever deployed here, and the caller decides whether that is fatal.
func EnsureRDSRunning(ctx context.Context, d Containers, accountID, identifier, alias, network string, timeout, poll time.Duration) (string, error) {
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	name, running := d.FlociContainer(ctx, "rds", accountID, identifier)
	if name == "" {
		return "", nil
	}
	if !running {
		if err := d.Start(ctx, name); err != nil {
			return name, fmt.Errorf("starting the database container %s: %w", name, err)
		}
	}
	if err := WaitForPostgres(ctx, d, name, timeout, poll); err != nil {
		return name, err
	}
	if alias != "" && network != "" {
		if err := d.EnsureNetworkAlias(ctx, name, alias, network); err != nil {
			return name, err
		}
	}
	return name, nil
}

// WaitForPostgres blocks until the container accepts a connection.
//
// Floci reports an instance available as soon as it has recorded it, while the
// Postgres behind it may still be running its first-boot init. Callers need the
// second -- a database that accepts connections -- which is the same two-part
// condition wait_for_rds_instance checks.
// poll is the retry cadence; zero means the default two seconds.
func WaitForPostgres(ctx context.Context, d Containers, containerName string, timeout, poll time.Duration) error {
	if poll == 0 {
		poll = 2 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		if _, err := d.Exec(ctx, containerName, "psql", "-U", "postgres", "-c", "SELECT 1"); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the database in %s did not accept connections within %s", containerName, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}
