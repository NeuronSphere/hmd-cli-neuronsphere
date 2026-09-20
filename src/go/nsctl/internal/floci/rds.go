package floci

import (
	"context"
	"fmt"
	"strings"
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
	EnsureNetworkAlias(ctx context.Context, name, alias, network string) (bool, error)
	Restart(ctx context.Context, name string) error
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
		// The reconnect flag is ignored here on purpose: Postgres listens on
		// 0.0.0.0, so the new IP a reconnect assigns costs it nothing. The
		// graph below is the one that cannot say the same.
		if _, err := d.EnsureNetworkAlias(ctx, name, alias, network); err != nil {
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

// Graph identifiers, from bom_seeder.graph_cluster_identifier.
const (
	graphInstanceName = "global-graph"
	graphRepoClass    = "hmd-inf-neptune"
)

// GraphIdentifier is the Neptune DBClusterIdentifier the graph node creates.
//
// Its environment component is the literal "local" in every environment, not
// the slug -- graph_cluster_identifier passes a literal there.
func GraphIdentifier(names Names) string {
	return tools.ResourceIdentifier(
		graphInstanceName, graphRepoClass,
		names.DeploymentID, "local", names.Region, names.CustomerCode,
	)
}

// EnsureNeptuneRunning returns the running graph container, starting it if
// Floci left it stopped.
//
// Floci stops the container it spawned on shutdown and, unlike RDS, never
// brings it back, while the cluster record survives and keeps reporting
// available forever. Waiting on the status alone therefore waits out the whole
// timeout on the single most common state after a stop, and then reports "no
// graph".
//
// An absent container returns "" without an error: the graph is provisioned
// only when something in the BOM asks for one, so its absence is the normal
// case for a default environment.
func EnsureNeptuneRunning(ctx context.Context, d Containers, accountID, identifier, alias, network string) (string, error) {
	name, running := d.FlociContainer(ctx, "neptune", accountID, identifier)
	if name == "" {
		return "", nil
	}
	if !running {
		if err := d.Start(ctx, name); err != nil {
			return name, fmt.Errorf("starting the graph container %s: %w", name, err)
		}
	}
	if alias != "" && network != "" {
		reconnected, err := d.EnsureNetworkAlias(ctx, name, alias, network)
		if err != nil {
			return name, err
		}
		// The graph has to be restarted onto its new address, and only when it
		// actually moved.
		//
		// The Gremlin server binds the container's *specific* IP, not 0.0.0.0.
		// Attaching the alias disconnects and reconnects the container, which
		// renumbers it -- so on a cold bootstrap the server kept listening on
		// 172.18.0.8 while `global-graph` resolved to 172.18.0.7, and every
		// client got ECONNREFUSED against a container Docker called healthy and
		// whose own log showed a happy server. The artifact librarian answered
		// 500 to every request for it.
		//
		// Invisible on a warm platform: the alias is already attached, nothing
		// reconnects, and the address the server bound at its last start is
		// still its own.
		if reconnected {
			if err := d.Restart(ctx, name); err != nil {
				return name, fmt.Errorf(
					"restarting the graph container %s after aliasing it as %s: %w", name, alias, err)
			}
		}
	}
	return name, nil
}

// RDSInstances is the RDS surface the reconcile needs.
type RDSInstances interface {
	DBInstanceStatus(ctx context.Context, identifier string) string
	DeleteDBInstance(ctx context.Context, identifier string) error
}

// ReconcileRDSInstance clears an instance Floci cannot serve, so the next
// deploy creates it fresh.
//
// This has no counterpart in the Python, and the gap is real. Terraform's
// refresh reconciles the *record*: an instance that exists is "no changes",
// whatever state it is in. But Floci can leave a record behind whose backing
// container it failed to recreate -- state `failed`, no container -- and no
// number of re-applies fixes that, because from Terraform's point of view
// nothing is wrong. ReconcileK3sCluster is the same repair on the cluster side;
// this is the database's.
//
// Only a `failed` instance with no container is touched. An `available` one is
// left alone even if its container is missing: that is a transient state Floci
// may still resolve, and deleting a database over a race is not a trade worth
// making.
func ReconcileRDSInstance(ctx context.Context, api RDSInstances, d Containers, accountID, identifier string) (bool, error) {
	status := api.DBInstanceStatus(ctx, identifier)
	if status == "" || !strings.EqualFold(status, "failed") {
		return false, nil
	}
	if name, _ := d.FlociContainer(ctx, "rds", accountID, identifier); name != "" {
		// A container exists after all; starting it is EnsureRDSRunning's job.
		return false, nil
	}
	if err := api.DeleteDBInstance(ctx, identifier); err != nil {
		return false, fmt.Errorf("clearing the failed database instance %s: %w", identifier, err)
	}
	return true, nil
}

// SubnetGroups is the DB subnet group surface the migration needs.
type SubnetGroups interface {
	DBSubnetGroupExists(ctx context.Context, name string) bool
	DeleteDBSubnetGroup(ctx context.Context, name string) error
}

// ReleaseUnmanagedSubnetGroup clears a DB subnet group the CLI created
// imperatively, so the base-vpc deploy can create it as a managed resource.
//
// A one-time migration for an environment provisioned before hmd-vpc owned the
// network. Terraform has no state for the old group, so its create fails with
// DBSubnetGroupAlreadyExists and the whole substrate deploy stops.
//
// Only called when base-vpc has never been deployed into this environment. A
// group Terraform already owns is left alone -- deleting that one would make
// every subsequent apply recreate it.
//
// Floci permits deleting a group an instance still references, and briefly
// breaks DescribeDBInstances for that instance until it exists again -- so this
// runs immediately before the deploy that recreates it, never on its own.
func ReleaseUnmanagedSubnetGroup(ctx context.Context, api SubnetGroups, name string) (bool, error) {
	if !api.DBSubnetGroupExists(ctx, name) {
		return false, nil
	}
	if err := api.DeleteDBSubnetGroup(ctx, name); err != nil {
		return false, fmt.Errorf("releasing the unmanaged DB subnet group %s: %w", name, err)
	}
	return true, nil
}
