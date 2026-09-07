package environment

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/floci"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
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
	labelled   []string
	volumes    []string
}

func (f *fakeDocker) ContainerNames(context.Context) map[string]bool { return f.containers }
func (f *fakeDocker) FlociContainer(_ context.Context, service, _, resource string) (string, bool) {
	name := f.floci[service+"/"+resource]
	return name, name != ""
}
func (f *fakeDocker) ContainersWithLabel(context.Context, string, string) []string { return f.labelled }
func (f *fakeDocker) RemoveContainer(_ context.Context, name string) error {
	f.rec.log("rm container %s", name)
	return nil
}
func (f *fakeDocker) RemoveVolumes(_ context.Context, names ...string) error {
	f.rec.log("rm volumes %s", strings.Join(names, ","))
	return nil
}
func (f *fakeDocker) VolumesMatching(context.Context, string) []string { return f.volumes }
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
		labelled: []string{"quirky_bassi"},
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
