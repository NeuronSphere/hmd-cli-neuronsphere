package floci

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/container"
)

// fakeK3s implements the Docker surface the k3s lifecycle uses, recording an
// ordered call log so tests can assert that the detach happens *before* the
// start -- a disconnect only reaches k3s on its next boot.
type fakeK3s struct {
	names    map[string]bool
	networks []string
	running  bool
	image    string
	state    container.State
	stateOK  bool
	log      string

	readyAfter int // execs that fail before the node reports Ready
	execs      int
	execErr    error

	calls     []string
	startErr  error
	detachErr error
	removed   []string
	volumes   []string
}

func (f *fakeK3s) ContainerNames(context.Context) map[string]bool { return f.names }

func (f *fakeK3s) ContainerNetworks(context.Context, string) []string { return f.networks }

func (f *fakeK3s) Running(context.Context, string) (bool, error) { return f.running, nil }

func (f *fakeK3s) ImageOf(context.Context, string) string { return f.image }

func (f *fakeK3s) Start(_ context.Context, name string) error {
	if f.startErr != nil {
		return f.startErr
	}
	f.calls = append(f.calls, "start "+name)
	f.running = true
	return nil
}

func (f *fakeK3s) Stop(_ context.Context, name string) error {
	f.calls = append(f.calls, "stop "+name)
	f.running = false
	return nil
}

func (f *fakeK3s) DetachFromDefaultBridge(_ context.Context, name, keep string) (bool, error) {
	if f.detachErr != nil {
		return false, f.detachErr
	}
	var onBridge, onKeep bool
	for _, n := range f.networks {
		switch n {
		case container.DefaultBridgeNetwork:
			onBridge = true
		case keep:
			onKeep = true
		}
	}
	if !onBridge {
		return false, nil
	}
	if !onKeep {
		return false, errors.New("only on the bridge")
	}
	f.calls = append(f.calls, "detach "+name)
	f.networks = []string{keep}
	return true, nil
}

func (f *fakeK3s) Exec(_ context.Context, _ string, _ ...string) ([]byte, error) {
	f.execs++
	if f.execErr != nil {
		return nil, f.execErr
	}
	if f.execs > f.readyAfter {
		return []byte("neuronsphere-local   Ready    control-plane   1d   v1.34.1+k3s1"), nil
	}
	return nil, errors.New("Error response from daemon: container is not running")
}

func (f *fakeK3s) InspectState(context.Context, string) (container.State, bool) {
	return f.state, f.stateOK
}

func (f *fakeK3s) Logs(context.Context, string, int) string { return f.log }

func (f *fakeK3s) RemoveContainer(_ context.Context, name string) error {
	f.removed = append(f.removed, name)
	return nil
}

func (f *fakeK3s) RemoveVolumes(_ context.Context, names ...string) error {
	f.volumes = append(f.volumes, names...)
	return nil
}

func aliveK3s(names ...string) *fakeK3s {
	set := map[string]bool{}
	for _, n := range names {
		set[n] = true
	}
	return &fakeK3s{
		names:    set,
		networks: []string{"neuronsphere_default-abc"},
		running:  true,
		state:    container.State{Running: true, Status: "running"},
		stateOK:  true,
	}
}

const testCluster = "ns-local-abc"
const testContainer = K3sContainerPrefix + "000000000001." + testCluster

// Floci's EKS spawner leaves the k3s container on Docker's default bridge as
// well as the configured network, and the bridge's addresses churn by start
// order. Taking it off the bridge is only useful before the container boots.
func TestEnsureK3sRunningDetachesBeforeStarting(t *testing.T) {
	t.Parallel()

	f := aliveK3s(testContainer)
	f.running = false
	f.networks = []string{container.DefaultBridgeNetwork, "neuronsphere_default-abc"}

	res, err := EnsureK3sRunning(context.Background(), f, K3sStartOptions{
		Cluster: testCluster, AccountID: "000000000001",
		Network: "neuronsphere_default-abc", Poll: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("EnsureK3sRunning: %v", err)
	}
	if !res.Detached {
		t.Error("Detached = false, want the container taken off the bridge")
	}
	want := []string{"detach " + testContainer, "start " + testContainer}
	if strings.Join(f.calls, ",") != strings.Join(want, ",") {
		t.Errorf("calls = %v, want %v", f.calls, want)
	}
}

// The same idempotence EnsureNetworkAlias is documented for: a restart must not
// churn networking when there is nothing to fix.
func TestEnsureK3sRunningLeavesASingleNetworkAlone(t *testing.T) {
	t.Parallel()

	f := aliveK3s(testContainer)
	f.running = false

	res, err := EnsureK3sRunning(context.Background(), f, K3sStartOptions{
		Cluster: testCluster, AccountID: "000000000001",
		Network: "neuronsphere_default-abc", Poll: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("EnsureK3sRunning: %v", err)
	}
	if res.Detached {
		t.Error("Detached = true, want no networking change")
	}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "detach") {
			t.Errorf("unexpected detach in %v", f.calls)
		}
	}
}

// A container that will not detach still runs, and a cluster is more useful
// than no cluster: the failure is reported, not obeyed.
func TestEnsureK3sRunningStartsEvenWhenTheDetachFails(t *testing.T) {
	t.Parallel()

	f := aliveK3s(testContainer)
	f.running = false
	f.detachErr = errors.New("attached only to Docker's default bridge")

	res, err := EnsureK3sRunning(context.Background(), f, K3sStartOptions{
		Cluster: testCluster, AccountID: "000000000001",
		Network: "neuronsphere_default-abc", Poll: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("EnsureK3sRunning: %v", err)
	}
	if res.DetachErr == nil {
		t.Error("DetachErr = nil, want the failure reported")
	}
	if len(f.calls) != 1 || f.calls[0] != "start "+testContainer {
		t.Errorf("calls = %v, want the container started anyway", f.calls)
	}
}

// A running container that is still dual-homed has already taken the bridge
// address for its node IP; only a bounce applies the fix.
func TestEnsureK3sRunningBouncesARunningContainerOffTheBridge(t *testing.T) {
	t.Parallel()

	f := aliveK3s(testContainer)
	f.networks = []string{container.DefaultBridgeNetwork, "neuronsphere_default-abc"}

	res, err := EnsureK3sRunning(context.Background(), f, K3sStartOptions{
		Cluster: testCluster, AccountID: "000000000001",
		Network: "neuronsphere_default-abc", NormalizeRunning: true, Poll: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("EnsureK3sRunning: %v", err)
	}
	if !res.Restarted || !res.Detached {
		t.Errorf("Restarted=%v Detached=%v, want both", res.Restarted, res.Detached)
	}
	want := []string{"stop " + testContainer, "detach " + testContainer, "start " + testContainer}
	if strings.Join(f.calls, ",") != strings.Join(want, ",") {
		t.Errorf("calls = %v, want %v", f.calls, want)
	}
}

// `docker inspect` saying Running is not the same as the cluster answering.
// Trusting it is how a k3s container that was up-but-dead reported success.
func TestEnsureK3sRunningProbesAnAlreadyRunningContainer(t *testing.T) {
	t.Parallel()

	f := aliveK3s(testContainer)
	f.execErr = errors.New("the connection to the server was refused")

	_, err := EnsureK3sRunning(context.Background(), f, K3sStartOptions{
		Cluster: testCluster, AccountID: "000000000001",
		Network:      "neuronsphere_default-abc",
		ProbeTimeout: 10 * time.Millisecond, Poll: time.Millisecond,
	})
	if err == nil {
		t.Fatal("EnsureK3sRunning = nil, want the unanswering cluster reported")
	}
	if f.execs == 0 {
		t.Error("the already-running container was never probed")
	}
}

// The container dies about eleven seconds in. Burying that in a three-minute
// wait costs the one line in `docker logs` that says why.
func TestWaitForK3sAPIStopsWhenTheContainerDies(t *testing.T) {
	t.Parallel()

	f := aliveK3s(testContainer)
	f.execErr = errors.New("container is not running")
	f.state = container.State{Running: false, Status: "exited", ExitCode: 1}
	f.log = `level=fatal msg="Failed to start networking: unable to initialize network policy controller: error getting node subnet: failed to find interface with specified node ip"`

	start := time.Now()
	err := WaitForK3sAPI(context.Background(), f, testContainer, time.Minute, time.Millisecond)

	var died *K3sDiedError
	if !errors.As(err, &died) {
		t.Fatalf("WaitForK3sAPI = %v, want a K3sDiedError", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("waited for the full timeout instead of noticing the container had exited")
	}
	if !strings.Contains(died.Error(), "exit code 1") {
		t.Errorf("error should name the exit code: %v", died)
	}
	if !strings.Contains(died.Error(), "hmd-img-k3s-floci") {
		t.Errorf("error should name the remedy for this fatal: %v", died)
	}
}

func TestK3sDiagnosis(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		log  string
		want string // substring expected in the diagnosis, "" for no diagnosis
	}{
		{
			name: "node ip",
			log:  `level=fatal msg="... failed to find interface with specified node ip"`,
			want: "hmd-img-k3s-floci",
		},
		{
			name: "storage backend",
			log:  `level=fatal msg="... --storage-backend invalid"`,
			want: "hmd-img-k3s-floci",
		},
		{
			name: "cgroup missing controllers",
			log: `E0923 19:54:37.130193 40 kubelet.go:1703] "Failed to start ContainerManager" ` +
				`err="failed to initialize top level QOS containers: error validating root ` +
				`container [kubepods] : cgroup [\"kubepods\"] has some missing controllers: ` +
				`cpu, cpuset, hugetlb, memory, pids"`,
			want: "nsctl env purge",
		},
		{
			name: "unrecognized fatal",
			log:  `level=fatal msg="something never seen before"`,
			want: "",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := k3sDiagnosis(c.log)
			if c.want == "" {
				if got != "" {
					t.Errorf("k3sDiagnosis(%q) = %q, want no diagnosis", c.log, got)
				}
				return
			}
			if !strings.Contains(got, c.want) {
				t.Errorf("k3sDiagnosis(%q) = %q, want it to mention %q", c.log, got, c.want)
			}
		})
	}
}

// A docker hiccup, or a container mid-launch, is a question rather than an
// answer. Condemning either fails starts that were fine.
func TestWaitForK3sAPIDoesNotCondemnAnUnreadableOrStartingContainer(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name    string
		state   container.State
		stateOK bool
	}{
		{"docker could not be asked", container.State{}, false},
		{"the container is still being created", container.State{Status: "created"}, true},
		{"the container is restarting", container.State{Status: "restarting"}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := aliveK3s(testContainer)
			f.state, f.stateOK = tt.state, tt.stateOK
			f.readyAfter = 2 // fails twice, then answers Ready

			if err := WaitForK3sAPI(context.Background(), f, testContainer, time.Minute, time.Millisecond); err != nil {
				t.Errorf("WaitForK3sAPI = %v, want it to keep waiting and then succeed", err)
			}
		})
	}
}

func TestVerifyK3sAlive(t *testing.T) {
	t.Parallel()

	running := aliveK3s(testContainer)
	if err := VerifyK3sAlive(context.Background(), running, testContainer); err != nil {
		t.Errorf("VerifyK3sAlive on a running container = %v, want nil", err)
	}

	dead := aliveK3s(testContainer)
	dead.state = container.State{Running: false, Status: "exited", ExitCode: 1}
	if err := VerifyK3sAlive(context.Background(), dead, testContainer); err == nil {
		t.Error("VerifyK3sAlive on an exited container = nil, want an error")
	}

	// Never fail a start on a question docker did not answer.
	unreadable := aliveK3s(testContainer)
	unreadable.stateOK = false
	if err := VerifyK3sAlive(context.Background(), unreadable, testContainer); err != nil {
		t.Errorf("VerifyK3sAlive with no answer from docker = %v, want nil", err)
	}
}

// fakeClusters is the EKS surface the reconcile uses.
type fakeClusters struct {
	exists  bool
	ok      bool
	deleted []string
}

func (f *fakeClusters) ClusterExists(context.Context, string) (bool, bool) {
	return f.exists, f.ok
}

func (f *fakeClusters) DeleteCluster(_ context.Context, name string) error {
	f.deleted = append(f.deleted, name)
	f.exists = false
	return nil
}

func TestReconcileK3sCluster(t *testing.T) {
	t.Parallel()

	const expected = "ghcr.io/example/hmd-img-k3s-floci:0.3"

	tests := []struct {
		name         string
		api          *fakeClusters
		image        string
		running      bool
		hasContainer bool
		expected     string
		want         K3sReconcileAction
		wantDeleted  bool
		wantErr      bool
	}{
		{
			name:  "a healthy cluster is left alone",
			api:   &fakeClusters{exists: true, ok: true},
			image: expected, running: true, hasContainer: true,
			expected: expected, want: K3sReconcileNone,
		},
		{
			// What a non-purge stop leaves behind. EnsureK3sRunning starts it,
			// and recreating would drop the datastore and every Helm release.
			name:  "a stopped container on the expected image is not stale",
			api:   &fakeClusters{exists: true, ok: true},
			image: expected, running: false, hasContainer: true,
			expected: expected, want: K3sReconcileNone,
		},
		{
			name:     "no cluster record means the deploy creates one",
			api:      &fakeClusters{exists: false, ok: true},
			expected: expected, want: K3sReconcileNoCluster,
		},
		{
			// ImageOf reports "" both for a container that is gone and for one
			// it could not inspect. Only the first is staleness.
			name:  "an unreadable image is not staleness",
			api:   &fakeClusters{exists: true, ok: true},
			image: "", running: true, hasContainer: true,
			expected: expected, want: K3sReconcileUnreadable,
		},
		{
			name:  "a stale image is cleared so the deploy rebuilds it",
			api:   &fakeClusters{exists: true, ok: true},
			image: "ghcr.io/example/hmd-img-k3s-floci:0.2", running: true, hasContainer: true,
			expected: expected, want: K3sReconcileCleared, wantDeleted: true,
		},
		{
			name:     "a record with no container at all is cleared",
			api:      &fakeClusters{exists: true, ok: true},
			expected: expected, want: K3sReconcileCleared, wantDeleted: true,
		},
		{
			// Stricter than the Python: "Floci did not reply" and "there is no
			// such cluster" lead to opposite actions.
			name:  "a cluster is never destroyed over an unanswered question",
			api:   &fakeClusters{ok: false},
			image: "something-else", hasContainer: true,
			expected: expected, want: K3sReconcileNone, wantErr: true,
		},
		{
			// Without a pin there is no such thing as a stale container.
			name:  "no expected image means no judgement",
			api:   &fakeClusters{exists: true, ok: true},
			image: "anything", hasContainer: true,
			expected: "", want: K3sReconcileNone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := aliveK3s()
			if tt.hasContainer {
				f.names = map[string]bool{testContainer: true}
			}
			f.image, f.running = tt.image, tt.running

			got, err := ReconcileK3sCluster(context.Background(), tt.api, f, K3sReconcileOptions{
				Cluster: testCluster, AccountID: "000000000001",
				ExpectedImage: tt.expected,
				WaitGone:      time.Second, Poll: time.Millisecond,
			})
			if (err != nil) != tt.wantErr {
				t.Fatalf("ReconcileK3sCluster error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("action = %v, want %v", got, tt.want)
			}
			if deleted := len(tt.api.deleted) > 0; deleted != tt.wantDeleted {
				t.Errorf("deleted = %v, want %v", deleted, tt.wantDeleted)
			}
			// The datastore is what makes the recreate a repair rather than a
			// full BOM redeploy; nothing here may drop it.
			if len(f.volumes) != 0 {
				t.Errorf("removed volumes %v, want the datastore preserved", f.volumes)
			}
		})
	}
}

// Only an explicit purge drops the datastore, and then it must cover both
// names Floci might have used.
func TestReconcileK3sClusterDropsTheVolumeOnlyWhenAsked(t *testing.T) {
	t.Parallel()

	f := aliveK3s(testContainer)
	f.image = "stale"
	api := &fakeClusters{exists: true, ok: true}

	if _, err := ReconcileK3sCluster(context.Background(), api, f, K3sReconcileOptions{
		Cluster: testCluster, AccountID: "000000000001",
		ExpectedImage: "ghcr.io/example/hmd-img-k3s-floci:0.3",
		DropVolume:    true,
		WaitGone:      time.Second, Poll: time.Millisecond,
	}); err != nil {
		t.Fatalf("ReconcileK3sCluster: %v", err)
	}
	want := K3sVolumeCandidates(testCluster, "000000000001")
	if strings.Join(f.volumes, ",") != strings.Join(want, ",") {
		t.Errorf("volumes = %v, want %v", f.volumes, want)
	}
}

func TestK3sVolumeCandidatesCoverBothNamings(t *testing.T) {
	t.Parallel()

	if got := K3sVolumeCandidates("c", ControlPlaneAccountID); len(got) != 1 || got[0] != K3sContainerPrefix+"c" {
		t.Errorf("control-plane candidates = %v, want just the unqualified name", got)
	}
	got := K3sVolumeCandidates("c", "000000000001")
	want := []string{K3sContainerPrefix + "000000000001.c", K3sContainerPrefix + "c"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("candidates = %v, want %v", got, want)
	}
}

// floci's copy of the name rule must not drift from internal/container's.
func TestK3sContainerNamePrefersTheQualifiedName(t *testing.T) {
	t.Parallel()

	if got := K3sContainerName("c", ControlPlaneAccountID, nil); got != K3sContainerPrefix+"c" {
		t.Errorf("control plane = %q, want the unqualified name", got)
	}
	if got := K3sContainerName("c", "000000000001", nil); got != K3sContainerPrefix+"000000000001.c" {
		t.Errorf("default = %q, want the qualified name", got)
	}
	// A cluster created under the pre-2.0 name keeps working.
	existing := map[string]bool{K3sContainerPrefix + "c": true}
	if got := K3sContainerName("c", "000000000001", existing); got != K3sContainerPrefix+"c" {
		t.Errorf("legacy = %q, want the name that actually exists", got)
	}
}

// Docker creates a missing bind-mount source as a directory. A deploy that
// mounts the kubeconfig before the cluster has written it therefore leaves a
// directory in its place, and every subsequent write and mount fails -- the
// deploy reporting only `IsADirectoryError: '/root/.kube/config'`, which names
// the container's path and nothing that leads back to this file.
func TestWriteKubeconfigReplacesADirectoryDockerCreated(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "k3s", "kubeconfig")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}

	d := &kubeconfigExecer{out: []byte("apiVersion: v1\nclusters:\n- cluster:\n    server: https://127.0.0.1:6443\n  name: default\n")}
	if err := WriteKubeconfig(context.Background(), d, "k3s", path, 19072); err != nil {
		t.Fatalf("WriteKubeconfig: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.IsDir() {
		t.Fatal("the kubeconfig is still a directory")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "19072") {
		t.Errorf("kubeconfig does not point at the host port:\n%s", data)
	}
}

// kubeconfigExecer answers `cat /etc/rancher/k3s/k3s.yaml` with a fixed config.
type kubeconfigExecer struct{ out []byte }

func (k *kubeconfigExecer) Exec(_ context.Context, _ string, _ ...string) ([]byte, error) {
	return k.out, nil
}

func TestAnUnfilteredBridgeIsDiagnosed(t *testing.T) {
	t.Parallel()
	log := `time="2026-09-25T15:11:17Z" level=fatal msg="the kernel behind this container engine has no br_netfilter loaded: ` +
		BridgePath + ` does not exist. Bridged frames would bypass netfilter."`
	d := k3sDiagnosis(log)
	if !strings.Contains(d, "modprobe br_netfilter") {
		t.Errorf("diagnosis does not name the command: %q", d)
	}
}

// The token must not be "br_netfilter": k3s logs a harmless warning about it on
// every boot, including every healthy one, so a looser match would attach this
// remedy to every unrelated death.
func TestK3sOwnHarmlessModuleWarningIsNotDiagnosed(t *testing.T) {
	t.Parallel()
	log := `time="2026-09-25T15:11:17Z" level=warning msg="Failed to load kernel module br_netfilter with modprobe"
time="2026-09-25T15:11:18Z" level=fatal msg="something else entirely"`
	if d := k3sDiagnosis(log); d != "" {
		t.Errorf("a healthy-boot warning produced a diagnosis: %q", d)
	}
}
