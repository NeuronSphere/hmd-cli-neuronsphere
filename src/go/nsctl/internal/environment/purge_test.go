package environment

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/container"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/floci"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/runner"
)

// These tests deliberately do not call t.Parallel: withFakes swaps
// package-level constructors, and a parallel sibling would see whichever fake
// happened to be installed.
//
// recorder logs every destructive call in the order it was made. The order is
// the thing under test: what a purge removes is easy to get right and easy to
// get right too late.
type recorder struct{ calls []string }

func (r *recorder) log(format string, a ...any) { r.calls = append(r.calls, fmt.Sprintf(format, a...)) }

func (r *recorder) index(prefix string) int {
	for i, c := range r.calls {
		if strings.HasPrefix(c, prefix) {
			return i
		}
	}
	return -1
}

type fakeDocker struct {
	rec        *recorder
	containers map[string]bool
	floci      map[string]string
	labelled   map[string][]string
	volumes    []string
	// attached are volumes some container still holds. They are another
	// platform's, and a sweep must not take them.
	attached map[string]bool
}

func (f *fakeDocker) ContainerNames(context.Context) map[string]bool { return f.containers }
func (f *fakeDocker) FlociContainer(_ context.Context, service, _, resource string) (string, bool) {
	name := f.floci[service+"/"+resource]
	return name, name != ""
}

// Keyed by the filter the caller asks for, because the purge now makes three
// different label queries and a fake that answers them all identically would
// let the Floci sweep pass while querying the projectbuilder label.
func (f *fakeDocker) ContainersWithLabel(_ context.Context, key, value string) []string {
	return f.ContainersWithLabelOnNetwork(context.Background(), key, value, "")
}

// Keyed by "<filter>@<network>" when a network is named, so a test can give one
// platform's containers and another's different answers -- which is the whole
// point of the narrowing.
func (f *fakeDocker) ContainersWithLabelOnNetwork(_ context.Context, key, value, network string) []string {
	filter := key
	if value != "" {
		filter = key + "=" + value
	}
	if network != "" {
		if named, ok := f.labelled[filter+"@"+network]; ok {
			return named
		}
		return nil
	}
	return f.labelled[filter]
}
func (f *fakeDocker) RemoveContainer(_ context.Context, name string) error {
	f.rec.log("rm container %s", name)
	return nil
}
func (f *fakeDocker) RemoveVolumes(_ context.Context, names ...string) error {
	f.rec.log("rm volumes %s", strings.Join(names, ","))
	return nil
}
func (f *fakeDocker) VolumesMatching(context.Context, string) []string { return f.volumes }

func (f *fakeDocker) DanglingVolumesMatching(context.Context, string) []string {
	var out []string
	for _, v := range f.volumes {
		if !f.attached[v] {
			out = append(out, v)
		}
	}
	return out
}
func (f *fakeDocker) RemoveNetwork(_ context.Context, name string) error {
	f.rec.log("rm network %s", name)
	return nil
}
func (f *fakeDocker) Exec(context.Context, string, ...string) ([]byte, error) { return nil, nil }

type fakeFloci struct{ rec *recorder }

func (f *fakeFloci) DeleteDBInstance(_ context.Context, id string) error {
	f.rec.log("floci delete instance %s", id)
	return nil
}
func (f *fakeFloci) DeleteDBCluster(_ context.Context, id string) error {
	f.rec.log("floci delete cluster %s", id)
	return nil
}

type fakeClusters struct{ rec *recorder }

func (f *fakeClusters) DeleteCluster(_ context.Context, name string) error {
	f.rec.log("floci delete eks %s", name)
	return nil
}

// withFakes swaps the constructors for the duration of one test.
func withFakes(t *testing.T, rec *recorder, d *fakeDocker) {
	t.Helper()
	od, of, oc := newDocker, newFlociDeleter, newClusterDeleter
	newDocker = func() dockerClient { return d }
	newFlociDeleter = func(context.Context, floci.Target, *Options) (flociDeleter, error) {
		return &fakeFloci{rec: rec}, nil
	}
	newClusterDeleter = func(context.Context, floci.Target) (clusterDeleter, error) {
		return &fakeClusters{rec: rec}, nil
	}
	t.Cleanup(func() { newDocker, newFlociDeleter, newClusterDeleter = od, of, oc })
}

func purgeHome(t *testing.T, envs map[string]registry.Environment) string {
	t.Helper()
	home := t.TempDir()
	reg := &registry.Registry{
		Version:      1,
		ControlPlane: registry.ControlPlane{Bootstrapped: true, Network: "neuronsphere_default-abc12345"},
		Environments: envs,
	}
	for slug := range envs {
		reg.DefaultEnv = slug
	}
	if err := reg.Save(home); err != nil {
		t.Fatal(err)
	}
	return home
}

func testEnv(home, slug string) registry.Environment {
	return registry.Environment{
		AccountID: "100000000001", Bootstrap: map[string]any{},
		DBContainer: "hmd_db_" + slug, DeploymentID: slug,
		GraphContainer: "graph_" + slug, K3sCluster: "k3s-" + slug,
		Kubeconfig: filepath.Join(home, ".cache", "environments", slug, "k3s", "kubeconfig"),
		Name:       slug, Slug: slug, PortBase: 19000,
		StateDir: filepath.Join(home, ".cache", "environments", slug),
	}
}

// The bug this whole verb exists to not repeat. The Python removes the floci
// container and only then asks Floci to delete the control plane's RDS instance
// and graph, so both calls fail against a dead endpoint and their containers and
// volumes are left behind -- 49 stale floci-* volumes on one machine.
func TestTheControlPlaneIsStoppedAfterFlociHasServedItsDeletes(t *testing.T) {
	rec := &recorder{}
	home := purgeHome(t, map[string]registry.Environment{})
	withFakes(t, rec, &fakeDocker{rec: rec, floci: map[string]string{}, volumes: []string{"floci-rds-x"}})

	opts := &Options{Home: home, Lookup: func(string) string { return "" }, Out: io.Discard, Err: io.Discard}
	err := PurgeAll(context.Background(), opts, ControlPlaneTeardown{
		GraphIdentifier: "cp-graph",
		Stop: func(context.Context) error {
			rec.log("stop control plane")
			return nil
		},
	})
	if err != nil {
		t.Fatalf("PurgeAll: %v", err)
	}

	stop := rec.index("stop control plane")
	if stop < 0 {
		t.Fatalf("the control plane was never stopped; calls were %v", rec.calls)
	}
	for _, before := range []string{"floci delete instance", "floci delete cluster cp-graph"} {
		at := rec.index(before)
		if at < 0 {
			t.Errorf("%q never happened; calls were %v", before, rec.calls)
			continue
		}
		if at > stop {
			t.Errorf("%q happened after the control plane stopped; Floci cannot serve a delete it is not running for", before)
		}
	}
	// And the volume sweep is the belt to that braces: it runs afterwards
	// precisely because it needs no Floci.
	if sweep := rec.index("rm volumes floci-rds-x"); sweep < stop {
		t.Errorf("the leftover-volume sweep ran before the stop, where it can see nothing new")
	}
}

// An environment's own resources are deleted while Floci is up too, and its
// registry entry goes last: an unregistered environment whose containers still
// run is the state `env delete` refuses to create.
func TestAnEnvironmentIsUnregisteredOnlyAfterItIsGone(t *testing.T) {
	rec := &recorder{}
	home := t.TempDir()
	env := testEnv(home, "dev")
	home = purgeHome(t, map[string]registry.Environment{"dev": env})
	env = testEnv(home, "dev")
	reg, _ := registry.Load(home, nil)
	reg.Environments["dev"] = env
	if err := reg.Save(home); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(env.Kubeconfig), 0o755); err != nil {
		t.Fatal(err)
	}

	withFakes(t, rec, &fakeDocker{
		rec:        rec,
		containers: map[string]bool{"floci-eks-100000000001.k3s-dev": true},
		floci: map[string]string{
			"rds/" + floci.EnvDBIdentifier(floci.NamesFrom(nil, "dev", "dev")): "floci-rds-dev",
		},
		labelled: map[string][]string{runner.EnvironmentLabel + "=dev": {"quirky_bassi"}},
	})

	opts := &Options{Home: home, Lookup: func(string) string { return "" }, Out: io.Discard, Err: io.Discard}
	if err := Purge(context.Background(), opts, "dev"); err != nil {
		t.Fatalf("Purge: %v", err)
	}

	if rec.index("floci delete eks k3s-dev") < 0 {
		t.Errorf("the cluster was never deleted through Floci; calls were %v", rec.calls)
	}
	// The projectbuilder an interrupted deploy left behind. It exits with a
	// Docker-generated name and the label is the only thing tying it here.
	if rec.index("rm container quirky_bassi") < 0 {
		t.Errorf("an interrupted deploy's container was not swept; calls were %v", rec.calls)
	}
	if _, err := os.Stat(env.StateDir); !os.IsNotExist(err) {
		t.Errorf("the state directory survived the purge")
	}
	after, err := registry.Load(home, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, still := after.Environments["dev"]; still {
		t.Errorf("dev is still registered after being purged")
	}
}

// A legacy-layout environment shares the control plane's database and graph, so
// the rules above would destroy state that is not its own. Refused, not
// half-done -- which is also what the Python does by skipping its teardown.
func TestALegacyEnvironmentIsRefusedRatherThanHalfPurged(t *testing.T) {
	rec := &recorder{}
	home := t.TempDir()
	env := testEnv(home, "local")
	env.LegacyLayout = true
	home = purgeHome(t, map[string]registry.Environment{"local": env})
	withFakes(t, rec, &fakeDocker{rec: rec, floci: map[string]string{}})

	opts := &Options{Home: home, Lookup: func(string) string { return "" }, Out: io.Discard, Err: io.Discard}
	err := Purge(context.Background(), opts, "local")
	if err == nil {
		t.Fatal("a legacy-layout environment was purged; it shares the control plane's state")
	}
	if code := nserr.CodeOf(err); code != nserr.Usage {
		t.Errorf("exit code %d, want %d", code, nserr.Usage)
	}
	if len(rec.calls) != 0 {
		t.Errorf("the refusal still destroyed things: %v", rec.calls)
	}
	after, _ := registry.Load(home, nil)
	if _, still := after.Environments["local"]; !still {
		t.Errorf("a refused purge unregistered the environment anyway")
	}
}

// The first real purge left floci-ecr-registry running. It is neither an eks,
// an rds nor a neptune container, so nothing named it -- and it held its own
// volume and an endpoint on the platform network, so the volume sweep and the
// network removal both failed behind it and two floci-rds-db-* volumes survived
// with it. Sweeping by account catches every service Floci runs, including the
// ones it grows later.
func TestAnEnvironmentPurgeSweepsFlociContainersItDoesNotNameByService(t *testing.T) {
	rec := &recorder{}
	home := t.TempDir()
	env := testEnv(home, "dev")
	home = purgeHome(t, map[string]registry.Environment{"dev": env})
	withFakes(t, rec, &fakeDocker{
		rec:   rec,
		floci: map[string]string{},
		// Keyed by network: the sweep asks only about this platform's. The
		// same account label on another platform's network -- every HMD_HOME
		// numbers its first environment 000000000001 -- must be left alone.
		labelled: map[string][]string{
			container.LabelFlociAccount + "=" + env.AccountID + "@neuronsphere_default-abc12345": {"floci-ecr-registry"},
			container.LabelFlociAccount + "=" + env.AccountID + "@neuronsphere_default-other01":  {"floci-eks-000000000001.ns-local-other01"},
			container.LabelFlociAccount + "=" + env.AccountID:                                    {"floci-ecr-registry", "floci-eks-000000000001.ns-local-other01"},
		},
	})

	opts := &Options{Home: home, Lookup: func(string) string { return "" }, Out: io.Discard, Err: io.Discard}
	if err := Purge(context.Background(), opts, "dev"); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if rec.index("rm container floci-ecr-registry") < 0 {
		t.Errorf("the ECR registry survived the purge; calls were %v", rec.calls)
	}
	if rec.index("rm container floci-eks-000000000001.ns-local-other01") >= 0 {
		t.Errorf("another platform's container, sharing only the account label, was removed; calls were %v", rec.calls)
	}
}

// Ordering, which is the whole of this verb. A container still holding a volume
// or a network endpoint makes both removals fail, so the cross-account sweep has
// to land between the control-plane teardown and them -- not after, where it
// would be sweeping up behind two failures it could have prevented.
func TestTheFlociSweepRunsBeforeTheVolumeAndNetworkRemovals(t *testing.T) {
	rec := &recorder{}
	home := purgeHome(t, map[string]registry.Environment{})
	withFakes(t, rec, &fakeDocker{
		rec:     rec,
		floci:   map[string]string{},
		volumes: []string{"floci-rds-x"},
		// Keyed by network: the sweep asks only about this platform's.
		labelled: map[string][]string{
			container.LabelFloci + "@neuronsphere_default-abc12345": {"floci-ecr-registry"},
		},
	})

	opts := &Options{Home: home, Lookup: func(string) string { return "" }, Out: io.Discard, Err: io.Discard}
	err := PurgeAll(context.Background(), opts, ControlPlaneTeardown{
		GraphIdentifier: "cp-graph",
		Stop: func(context.Context) error {
			rec.log("stop control plane")
			return nil
		},
	})
	if err != nil {
		t.Fatalf("PurgeAll: %v", err)
	}

	swept := rec.index("rm container floci-ecr-registry")
	if swept < 0 {
		t.Fatalf("the cross-account Floci sweep never ran; calls were %v", rec.calls)
	}
	if stop := rec.index("stop control plane"); swept < stop {
		t.Errorf("the sweep ran before the control plane came down, where Floci may still respawn what it removes")
	}
	for _, after := range []string{"rm volumes floci-rds-x", "rm network"} {
		at := rec.index(after)
		if at < 0 {
			t.Errorf("%q never happened; calls were %v", after, rec.calls)
			continue
		}
		if at < swept {
			t.Errorf("%q ran before the Floci sweep, so it fails behind whatever the sweep would have removed", after)
		}
	}
}

// A purge must not take another HMD_HOME's containers or volumes.
//
// Two scopes, and the first attempt at this fixed only the second. Floci volume
// names are global to the daemon and carry nothing naming the HMD_HOME that
// created them, so the leftover sweep matched every platform's -- that is the
// dangling check. But the container sweep ahead of it matched every Floci-
// labelled container on the daemon, because accounts are allocated per registry
// and both platforms start at 000000000001, so it removed the other platform's
// containers *and thereby dangled its volumes*, and the dangling check then
// let them through. Scoping the container sweep to this platform's network --
// the one identifier that does carry the HMD_HOME hash -- is what makes the
// dangling check mean anything.
//
// A purge run from a throwaway HMD_HOME destroyed a working platform's
// control-plane database, its deployment graph and its clusters' datastores.
// This is that, pinned at both layers.
func TestThePurgeLeavesAnotherPlatformsContainersAndVolumesAlone(t *testing.T) {
	rec := &recorder{}
	home := purgeHome(t, map[string]registry.Environment{})
	ours := "neuronsphere_default-abc12345"
	withFakes(t, rec, &fakeDocker{
		rec:   rec,
		floci: map[string]string{},
		labelled: map[string][]string{
			// Only ours answers for our network. Theirs is on another one and
			// must never be asked for, let alone removed.
			container.LabelFloci + "@" + ours: {"floci-ours"},
			container.LabelFloci:              {"floci-ours", "floci-theirs"},
		},
		volumes: []string{"floci-rds-db-ours", "floci-rds-db-theirs"},
		// Still held by the other platform's container, stopped or running.
		attached: map[string]bool{"floci-rds-db-theirs": true},
	})

	opts := &Options{Home: home, Lookup: func(string) string { return "" }, Out: io.Discard, Err: io.Discard}
	if err := PurgeAll(context.Background(), opts, ControlPlaneTeardown{
		GraphIdentifier: "cp-graph",
		Stop:            func(context.Context) error { return nil },
	}); err != nil {
		t.Fatalf("PurgeAll() error = %v", err)
	}

	all := strings.Join(rec.calls, "\n")
	if !strings.Contains(all, "rm container floci-ours") {
		t.Errorf("the purge did not remove its own container:\n%s", all)
	}
	if strings.Contains(all, "floci-theirs") {
		t.Errorf("the purge touched another platform's container:\n%s", all)
	}
	if !strings.Contains(all, "floci-rds-db-ours") {
		t.Errorf("the purge did not collect its own leftover volume:\n%s", all)
	}
	if strings.Contains(all, "floci-rds-db-theirs") {
		t.Errorf("the purge took another platform's volume:\n%s", all)
	}
}
