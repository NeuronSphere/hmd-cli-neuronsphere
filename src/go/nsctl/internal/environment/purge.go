package environment

import (
	"context"
	"os"
	"path/filepath"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/container"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/floci"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/router"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/runner"
)

// dockerClient is the Docker surface a purge needs, narrowed so the ordering
// below can be driven without a daemon.
//
// This is the one operation where a leftover is the whole failure mode, and the
// only way to see the order is to record it -- a test against the real daemon
// would have to create the very containers and volumes it is checking get
// removed. So the seam earns itself here where it would not elsewhere.
type dockerClient interface {
	ContainerNames(ctx context.Context) map[string]bool
	FlociContainer(ctx context.Context, service, accountID, resourceID string) (string, bool)
	ContainersWithLabel(ctx context.Context, key, value string) []string
	RemoveContainer(ctx context.Context, name string) error
	RemoveVolumes(ctx context.Context, names ...string) error
	VolumesMatching(ctx context.Context, prefix string) []string
	DanglingVolumesMatching(ctx context.Context, prefix string) []string
	ContainersWithLabelOnNetwork(ctx context.Context, key, value, network string) []string
	RemoveNetwork(ctx context.Context, name string) error
	Exec(ctx context.Context, name string, args ...string) ([]byte, error)
}

// flociDeleter is the Floci surface a purge needs: the two deletes, and nothing
// that could create anything.
type flociDeleter interface {
	DeleteDBInstance(ctx context.Context, identifier string) error
	DeleteDBCluster(ctx context.Context, identifier string) error
}

// clusterDeleter is the EKS surface a purge needs.
type clusterDeleter interface {
	DeleteCluster(ctx context.Context, name string) error
}

// The constructors, as variables so a test can substitute them. The production
// wiring is the line each is initialised with.
var (
	newDocker = func() dockerClient { return container.New() }

	newFlociDeleter = func(ctx context.Context, t floci.Target, opts *Options) (flociDeleter, error) {
		return floci.NewProvisioner(ctx, t, opts.Out, opts.Err)
	}

	newClusterDeleter = func(ctx context.Context, t floci.Target) (clusterDeleter, error) {
		return floci.NewClusters(ctx, t)
	}
)

// Purge tears one environment down: its cluster, database, graph, routes and
// state, and finally its registry entry.
//
// The order is the whole of this function. Everything Floci spawned is deleted
// through Floci *while Floci is still running*, which is the mistake the Python
// makes in the other direction: stop_neuronsphere_extend removes the floci
// container and only then asks it to delete the control plane's RDS instance
// and graph, so both calls fail against a dead endpoint --
//
//	Could not delete the control-plane RDS instance: Could not connect to the
//	endpoint URL "http://localhost:4566/"
//
// -- and their containers and volumes are left behind. Forty-nine stale floci-*
// volumes had accumulated on one machine that way. A purge is the operation
// where leftovers are the entire failure mode, so this one deletes first and
// stops afterwards.
//
// The registry entry goes last for the opposite reason: an unregistered
// environment whose containers are still running is the state `env delete`
// refuses to create, because nothing left knows how to address them.
func Purge(ctx context.Context, opts *Options, name string) error {
	reg, err := registry.Load(opts.Home, opts.Lookup)
	if err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	env, err := reg.Environment(name, opts.Lookup)
	if err != nil {
		return nserr.Wrap(nserr.Usage, err)
	}
	if err := purgeEnvironment(ctx, opts, reg, env); err != nil {
		return err
	}
	if err := reg.RemoveEnvironment(env.Slug, opts.Lookup); err != nil {
		return nserr.Wrap(nserr.Usage, err)
	}
	if err := reg.Save(opts.Home); err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	opts.step("Purged %s. The control plane is still running.", env.Slug)
	return nil
}

// purgeEnvironment destroys one environment's resources but leaves the registry
// alone, so PurgeAll can remove the whole file in one go afterwards.
func purgeEnvironment(ctx context.Context, opts *Options, reg *registry.Registry, env *registry.Environment) error {
	// A legacy-layout environment *is* the control plane's Floci, Postgres and
	// graph -- its database container is the shared hmd_db. Purging it with the
	// rules below would take the control plane's state with it, and the Python
	// skips its teardown for the same reason. Refused rather than half-done.
	if env.LegacyLayout {
		return nserr.New(nserr.Usage,
			"%q was migrated from the pre-multi-environment layout and shares the control plane's "+
				"database and graph, so purging it here would destroy state that is not its own. "+
				"Use `hmd neuronsphere down --purge --env %s`.", env.Slug, env.Slug)
	}

	opts.step("Purging environment %q...", env.Slug)
	d := newDocker()
	names := floci.NamesFrom(opts.Lookup, env.DeploymentID, env.Slug)
	target := floci.ForAccount(opts.Lookup, env.AccountID, env.LegacyLayout)

	// Everything below is best-effort past this point: a purge that cannot
	// reach Floci must still remove what it can reach, and say what it could
	// not. Stopping at the first failure is how leftovers accumulate.
	clusters, err := newClusterDeleter(ctx, target)
	if err != nil {
		opts.warn("could not reach Floci to delete %s's cluster: %v", env.Slug, err)
	} else if err := clusters.DeleteCluster(ctx, env.K3sCluster); err != nil {
		opts.warn("could not delete the cluster %s: %v", env.K3sCluster, err)
	}

	prov, err := newFlociDeleter(ctx, target, opts)
	if err != nil {
		opts.warn("could not reach Floci to delete %s's database and graph: %v", env.Slug, err)
	} else {
		dbID := floci.EnvDBIdentifier(names)
		if err := prov.DeleteDBInstance(ctx, dbID); err != nil {
			opts.warn("%v", err)
		}
		// Floci mounts no volume for a graph: it lives in the container's
		// writable layer, so deleting the record and removing the container is
		// the data deletion.
		if err := prov.DeleteDBCluster(ctx, floci.GraphIdentifier(names)); err != nil {
			opts.warn("%v", err)
		}
	}

	// The containers and volumes Floci does not remove for us.
	// DeleteDBInstance deliberately leaves the volume -- that is what makes
	// clearing a failed record a repair -- so a purge has to take it here.
	existing := d.ContainerNames(ctx)
	removeContainers(ctx, opts, d, "cluster", floci.K3sContainerName(env.K3sCluster, env.AccountID, existing))
	if err := d.RemoveVolumes(ctx, floci.K3sVolumeCandidates(env.K3sCluster, env.AccountID)...); err != nil {
		opts.warn("%v", err)
	}
	for _, spawned := range []struct{ service, resource, role string }{
		{"rds", floci.EnvDBIdentifier(names), "database"},
		{"neptune", floci.GraphIdentifier(names), "graph"},
	} {
		name, _ := d.FlociContainer(ctx, spawned.service, env.AccountID, spawned.resource)
		if name == "" {
			continue
		}
		removeContainers(ctx, opts, d, spawned.role, name)
		if spawned.service == "rds" {
			if err := d.RemoveVolumes(ctx, name); err != nil {
				opts.warn("%v", err)
			}
		}
	}

	// A projectbuilder an interrupted deploy left running holds the network
	// open and cannot be found any other way: it exits with a Docker-generated
	// name and the label the runner sets is the only thing tying it here.
	for _, name := range d.ContainersWithLabel(ctx, runner.EnvironmentLabel, env.Slug) {
		removeContainers(ctx, opts, d, "deploy", name)
	}

	// Everything else Floci spawned for this account. The loop above names
	// three services -- eks, rds, neptune -- and Floci runs more than three: the
	// first real purge left floci-ecr-registry running, which held its volume
	// and an endpoint on the platform network, so the volume sweep and the
	// network removal both failed behind it. Swept by account rather than
	// enumerated by service, so a service Floci adds later needs no change here.
	//
	// Scoped to this platform's network, for the reason the control-plane
	// sweep below gives: every HMD_HOME allocates account 000000000001 to its
	// first environment, so the account label alone matched another
	// platform's stopped k3s and database containers and removed them.
	for _, name := range d.ContainersWithLabelOnNetwork(ctx, container.LabelFlociAccount, env.AccountID, reg.ControlPlane.Network) {
		removeContainers(ctx, opts, d, "Floci-spawned", name)
	}

	r := router.New(opts.Home, opts.Lookup)
	if err := r.RemoveEnvRoutes(env.Slug); err != nil {
		opts.warn("%v", err)
	}
	if err := r.Reload(ctx, d.Exec); err != nil {
		opts.warn("%v", err)
	}

	// The deployment graph keeps calling every instance DEPLOYED, and a
	// re-created environment under this name would inherit that. See
	// PurgedMarkerPath.
	if err := MarkPurged(opts.Home, env.DeploymentID); err != nil {
		opts.warn("could not record that %q was purged, so a later apply may skip instances the graph still calls deployed: %v", env.DeploymentID, err)
	}

	// The state directory carries the Postgres data, the graph data, the
	// kubeconfig and the reconcile snapshots, so one removal covers all four.
	if env.StateDir != "" {
		if err := os.RemoveAll(env.StateDir); err != nil {
			opts.warn("could not remove %s: %v", env.StateDir, err)
		} else {
			opts.step("  removed %s", env.StateDir)
		}
	}
	// The default environment also writes the historical shared kubeconfig,
	// which nothing in either implementation has ever cleaned up.
	if env.IsDefault() {
		removeIfPresent(opts, legacyKubeconfigPath(opts.Home))
	}

	// The manifest is left, and said so.
	//
	// It is the user's declaration of intent, not this environment's runtime
	// state -- often version-controlled, and the reason to purge is frequently
	// to rebuild from it. Deleting it would make `purge` destroy something the
	// user wrote.
	//
	// But it lives in $HMD_HOME/environments/, not in the state directory this
	// function removes, and a purge *unregisters* the environment -- so the
	// manifest is orphaned, and `env add` under the same name silently adopts
	// it again. That is how a purge announcing "none of it comes back" was
	// followed by a start that tried to deploy 28 workload repos nobody had
	// declared in that session. Naming the file is the difference between
	// deliberate and surprising.
	if path := manifest.DefaultPath(opts.Home, env.Slug); path != "" {
		if _, err := os.Stat(path); err == nil {
			opts.step("  kept %s; `nsctl env add %s` will pick it up again", path, env.Slug)
		}
	}
	return nil
}

// ControlPlaneTeardown is what PurgeAll needs from the control plane, supplied
// by the caller rather than imported.
//
// GraphIdentifier is passed in rather than derived because the identifier
// helpers are per-instance and asking the wrong one finds nothing and returns
// no error: floci.GraphIdentifier names the *environment's* graph, while the
// control plane's is a different instance under a different deployment id. That
// exact confusion once left the control-plane graph stopped after every restart
// and surfaced four layers away as Trino failing to resolve global-graph.
type ControlPlaneTeardown struct {
	GraphIdentifier string
	// Stop takes the control plane's containers down. Called after every Floci
	// delete above it, never before.
	Stop func(context.Context) error
}

// PurgeAll purges every environment and then the control plane.
//
// The control plane goes last, and its own Floci-hosted resources are deleted
// before its containers stop, for the same reason Purge does: Floci is what
// serves the delete.
func PurgeAll(ctx context.Context, opts *Options, cp ControlPlaneTeardown) error {
	reg, err := registry.Load(opts.Home, opts.Lookup)
	if err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}

	for _, slug := range reg.Names() {
		env, err := reg.Environment(slug, opts.Lookup)
		if err != nil {
			opts.warn("%v", err)
			continue
		}
		if err := purgeEnvironment(ctx, opts, reg, env); err != nil {
			// A legacy environment refuses; that must not stop the rest.
			opts.warn("%v", err)
		}
	}

	d := newDocker()
	target := floci.ControlPlane(opts.Lookup)
	names := floci.NamesFrom(opts.Lookup, "", "local")

	opts.step("Purging the control plane...")
	if prov, err := newFlociDeleter(ctx, target, opts); err != nil {
		opts.warn("could not reach Floci to delete the control plane's database and graph: %v", err)
	} else {
		if err := prov.DeleteDBInstance(ctx, floci.ControlPlaneDBIdentifier(names)); err != nil {
			opts.warn("%v", err)
		}
		if err := prov.DeleteDBCluster(ctx, cp.GraphIdentifier); err != nil {
			opts.warn("%v", err)
		}
	}
	for _, spawned := range []struct{ service, resource, role string }{
		{"rds", floci.ControlPlaneDBIdentifier(names), "control-plane database"},
		{"neptune", cp.GraphIdentifier, "control-plane graph"},
	} {
		name, _ := d.FlociContainer(ctx, spawned.service, target.AccountID, spawned.resource)
		if name == "" {
			continue
		}
		removeContainers(ctx, opts, d, spawned.role, name)
		if spawned.service == "rds" {
			if err := d.RemoveVolumes(ctx, name); err != nil {
				opts.warn("%v", err)
			}
		}
	}

	// Only now: stopping Floci is what makes every call above impossible.
	if cp.Stop != nil {
		if err := cp.Stop(ctx); err != nil {
			opts.warn("%v", err)
		}
	}

	// Every remaining Floci-spawned container on *this platform's network*. The
	// environment sweeps above cover the accounts the registry knows about;
	// this covers the control plane's own Lambdas and anything an environment
	// purged before this fix landed left behind. It runs before the volume
	// sweep and the network removal because both of those fail behind a
	// container still holding what they are trying to remove -- which is
	// exactly how the first real run of this ended.
	//
	// Scoped to the network, not merely to the Floci label. Accounts are
	// allocated per registry and every HMD_HOME starts at 000000000001, so the
	// account label cannot tell two platforms apart; the network can, because
	// it carries the HMD_HOME hash. A label-only sweep removed another
	// platform's containers -- and that, not the volume sweep, is what made its
	// volumes dangling, so the dangling check below was necessary and on its
	// own useless. Both were needed; only having one was found out by trying
	// it.
	for _, name := range d.ContainersWithLabelOnNetwork(ctx, container.LabelFloci, "", reg.ControlPlane.Network) {
		removeContainers(ctx, opts, d, "Floci-spawned", name)
	}

	// A name sweep, after the fact, for the volumes an earlier purge -- or the
	// Python's, which asks Floci after killing it -- could not reach. Floci's
	// spawned volumes carry no account label, so the name is the only handle.
	// Forty-nine of these had accumulated on one machine before anyone looked.
	//
	// Dangling only. This used to sweep every matching name, justified by "by
	// this point every account is going anyway" -- true within one Floci, and
	// false across HMD_HOMEs, because volume names are global to the daemon
	// while the network, compose project and Floci data directory are all
	// namespaced. A purge run from a throwaway HMD_HOME therefore destroyed a
	// working platform's control-plane database, deployment graph and cluster
	// datastores. It was not a near miss; it happened.
	//
	// This home's own volumes are dangling by now -- its containers were
	// removed above -- so nothing this purge should collect is missed, while a
	// volume another platform's container still holds is left alone.
	if stale := d.DanglingVolumesMatching(ctx, flociVolumePrefix); len(stale) > 0 {
		if err := d.RemoveVolumes(ctx, stale...); err != nil {
			opts.warn("%v", err)
		} else {
			opts.step("  removed %d leftover Floci volume(s)", len(stale))
		}
	}
	if reg.ControlPlane.Network != "" {
		if err := d.RemoveNetwork(ctx, reg.ControlPlane.Network); err != nil {
			opts.warn("%v", err)
		}
	}

	for _, rel := range [][]string{
		{"floci", "data"}, {"postgresql", "data"}, {"graph_db"},
		{".cache", "nginx"}, {".cache", "k3s"}, {".cache", "environments"},
		{".cache", "neuronsphere"},
	} {
		removeIfPresent(opts, filepath.Join(append([]string{opts.Home}, rel...)...))
	}
	opts.step("Purged. `nsctl control-plane start` bootstraps a new one.")
	return nil
}

// removeContainers force-removes containers, reporting each one it took.
func removeContainers(ctx context.Context, opts *Options, d dockerClient, role string, names ...string) {
	for _, name := range names {
		if name == "" {
			continue
		}
		if err := d.RemoveContainer(ctx, name); err != nil {
			opts.warn("%v", err)
			continue
		}
		opts.step("  removed %s (%s)", name, role)
	}
}

func removeIfPresent(opts *Options, path string) {
	if path == "" {
		return
	}
	if _, err := os.Stat(path); err != nil {
		return
	}
	if err := os.RemoveAll(path); err != nil {
		opts.warn("could not remove %s: %v", path, err)
		return
	}
	opts.step("  removed %s", path)
}

// flociVolumePrefix is what Floci names the volumes it spawns for the backends
// it manages: floci-rds-*, floci-eks-*.
const flociVolumePrefix = "floci-"
