package k3s

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/container"
)

// The operator scripts run inside projectbuilder against a real k3s cluster,
// which no unit test can stand in for: busybox sed, k3s's addon controller and
// CoreDNS's server-block parser all reject things a fake accepts.
//
// Skipped unless NSCTL_LIVE is set, because it needs a running platform. Run it
// with NSCTL_LIVE=1 and -count=1: these assert against container state Go's
// test cache cannot see, so a cached pass means nothing.
func liveKube(t *testing.T) (*Operators, context.Context) {
	t.Helper()
	if os.Getenv("NSCTL_LIVE") == "" {
		t.Skip("set NSCTL_LIVE=1 with a running local NeuronSphere to run this")
	}

	home := os.Getenv("HMD_HOME")
	if home == "" {
		t.Skip("HMD_HOME is not set")
	}
	cluster := envOr("NSCTL_LIVE_CLUSTER", "ns-local-57aa833c")
	k3sContainer := envOr("NSCTL_LIVE_K3S_CONTAINER", "floci-eks-000000000001."+cluster)
	network := envOr("NSCTL_LIVE_NETWORK", "neuronsphere_default-57aa833c")

	d := container.New()
	d.Timeout = 5 * time.Minute

	o := &Operators{
		Kube: &Kube{
			Cluster:    cluster,
			Container:  k3sContainer,
			Kubeconfig: home + "/.cache/environments/local/k3s/kubeconfig",
			Run:        d.Run,
		},
		Docker: d,
		Env: Environment{
			Slug: "local", DBContainer: "hmd_db-local", GraphContainer: "global-graph-local",
			CoreInstanceName: "local-neuronsphere",
			K3sCluster:       cluster, K3sContainer: k3sContainer,
		},
		Network: network, IngressEnabled: true,
		Out: os.Stdout, Err: os.Stderr,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	t.Cleanup(cancel)
	return o, ctx
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func TestLiveRunKubeReachesTheCluster(t *testing.T) {
	o, ctx := liveKube(t)

	stdout, stderr, err := o.Kube.RunKube(ctx, []byte("kubectl get nodes -o name\n"), nil)
	if err != nil {
		t.Fatalf("RunKube: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	if !strings.Contains(string(stdout), "node/") {
		t.Errorf("no nodes returned:\nstdout=%s\nstderr=%s", stdout, stderr)
	}
	t.Logf("nodes: %s", strings.TrimSpace(string(stdout)))
}

func TestLivePrepareNode(t *testing.T) {
	o, ctx := liveKube(t)
	if err := o.PrepareNode(ctx); err != nil {
		t.Fatalf("PrepareNode: %v", err)
	}
}

func TestLiveCoreDNSRecords(t *testing.T) {
	o, ctx := liveKube(t)

	db := os.Getenv("NSCTL_LIVE_DB_CONTAINER")
	graph := os.Getenv("NSCTL_LIVE_GRAPH_CONTAINER")
	if err := o.EnsureCoreDNSRecordsFor(ctx, db, graph); err != nil {
		t.Fatalf("EnsureCoreDNSRecordsFor: %v", err)
	}

	// CoreDNS rejects a malformed server block by crash-looping, so a healthy
	// rollout is the assertion that matters.
	stdout, stderr, err := o.Kube.RunKube(ctx, []byte(
		"kubectl -n kube-system get configmap coredns-custom -o jsonpath='{.data.neuronsphere\\.server}'\n"), nil)
	if err != nil {
		t.Fatalf("reading the ConfigMap back: %v\n%s", err, stderr)
	}
	if !strings.Contains(string(stdout), "neuronsphere:53") {
		t.Errorf("the applied ConfigMap has no neuronsphere block:\n%s", stdout)
	}
	t.Logf("coredns-custom:\n%s", stdout)
}

func TestLiveIngressController(t *testing.T) {
	o, ctx := liveKube(t)
	if err := o.EnsureIngressController(ctx); err != nil {
		t.Fatalf("EnsureIngressController: %v", err)
	}

	stdout, _, err := o.Kube.RunKube(ctx, []byte("kubectl get ingressclass -o name\n"), nil)
	if err != nil {
		t.Fatalf("listing ingress classes: %v", err)
	}
	if !strings.Contains(string(stdout), "ingressclass.networking.k8s.io/alb") {
		t.Errorf("the alb IngressClass is missing:\n%s", stdout)
	}
}

func TestLiveReadClusterState(t *testing.T) {
	o, ctx := liveKube(t)

	state := o.ReadClusterState(ctx)
	if !state.Read {
		t.Fatal("the cluster could not be read")
	}
	if state.IncarnationID == "" {
		t.Error("no kube-system UID; the cluster-recreated check would never fire")
	}
	t.Logf("incarnation=%s releases=%d", state.IncarnationID, len(state.HelmReleases))
}

// The k3s container must be on the NeuronSphere network and nothing else.
//
// Floci's EKS spawner attaches it to Docker's default bridge as well -- every
// other container it spawns (RDS, Neptune, Lambda) gets the configured network
// alone. k3s then takes eth0, the bridge, for its node IP, and the bridge hands
// out addresses by start order: the address churns between runs while the
// datastore in /var/lib/rancher/k3s keeps the previous one. That is what killed
// the cluster eleven seconds into a warm start, and one inspect catches it.
func TestLiveK3sContainerIsOnlyOnTheNeuronSphereNetwork(t *testing.T) {
	o, ctx := liveKube(t)

	d := container.New()
	d.Timeout = 30 * time.Second
	nets := d.ContainerNetworks(ctx, o.Env.K3sContainer)
	if len(nets) == 0 {
		t.Fatalf("no networks for %s; is it there?", o.Env.K3sContainer)
	}
	for _, n := range nets {
		if n == container.DefaultBridgeNetwork {
			t.Errorf("%s is still on Docker's default bridge (networks: %v); "+
				"its node IP will churn on the next restart", o.Env.K3sContainer, nets)
		}
	}
	t.Logf("networks: %v", nets)
}

// A live run must fail on a dead cluster rather than skip past it: the whole
// bug was that a container which had exited still read as a successful start.
func TestLiveK3sContainerIsRunning(t *testing.T) {
	o, ctx := liveKube(t)

	d := container.New()
	d.Timeout = 30 * time.Second
	state, ok := d.InspectState(ctx, o.Env.K3sContainer)
	if !ok {
		t.Fatalf("could not read the state of %s", o.Env.K3sContainer)
	}
	if !state.Running {
		t.Fatalf("%s is %s (exit code %d); see `docker logs --tail 50 %s`",
			o.Env.K3sContainer, state.Status, state.ExitCode, o.Env.K3sContainer)
	}
}
