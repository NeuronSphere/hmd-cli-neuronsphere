package floci

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/container"
)

// K3sContainerPrefix is the prefix Floci gives a spawned k3s container.
const K3sContainerPrefix = "floci-eks-"

// K3sAPIPort is the API server's port inside the container.
const K3sAPIPort = 6443

// k3sLogTail is how much of a dead container's output is quoted back. Enough to
// carry the fatal line and the lines around it, short enough to read.
const k3sLogTail = 50

// K3sClusterState is what EnsureK3sRunning found.
type K3sClusterState int

const (
	// K3sRunning means the container was already up.
	K3sRunning K3sClusterState = iota
	// K3sStarted means a stopped container was started in place, preserving the
	// k3s datastore and every Helm release on it. This is what a non-purge stop
	// leaves behind and what keeps a restart cheap.
	K3sStarted
	// K3sMissing means there is no container. Creating one is a Terraform
	// resource in the eks-cluster DAG node, not something nsctl does.
	K3sMissing
	// K3sStale means the container exists on an image other than the configured
	// wrapper. Recreating it is ReconcileK3sCluster's job, ahead of this.
	K3sStale
)

// K3sExecer runs a command inside the k3s node container.
type K3sExecer interface {
	Exec(ctx context.Context, name string, args ...string) ([]byte, error)
}

// K3sInspector answers whether the node container is alive, and why not.
type K3sInspector interface {
	InspectState(ctx context.Context, name string) (container.State, bool)
	Logs(ctx context.Context, name string, lines int) string
}

// K3sProbe is what WaitForK3sAPI needs: a way to ask the API, and a way to tell
// a slow start from a dead container.
type K3sProbe interface {
	K3sExecer
	K3sInspector
}

// K3sContainers is the Docker surface the k3s lifecycle needs.
type K3sContainers interface {
	K3sProbe
	ContainerNames(ctx context.Context) map[string]bool
	ContainerNetworks(ctx context.Context, name string) []string
	Running(ctx context.Context, name string) (bool, error)
	ImageOf(ctx context.Context, name string) string
	Start(ctx context.Context, name string) error
	Stop(ctx context.Context, name string) error
	DetachFromDefaultBridge(ctx context.Context, name, keep string) (bool, error)
}

// K3sDiedError is returned when the k3s container is not running.
//
// It exists so a dead cluster reads as one failure with a cause rather than as
// the dozen "container is not running" warnings each provisioning step
// otherwise produces on its way to a Ready summary.
type K3sDiedError struct {
	Container string
	State     container.State
	Log       string
}

func (e *K3sDiedError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "the k3s container %s is not running (%s", e.Container, e.State.Status)
	if !e.State.Running && e.State.ExitCode != 0 {
		fmt.Fprintf(&b, ", exit code %d", e.State.ExitCode)
	}
	if e.State.OOMKilled {
		b.WriteString(", out of memory")
	}
	b.WriteString("); nothing was provisioned onto the cluster and nothing will schedule")
	if line := k3sFatalLine(e.Log); line != "" {
		fmt.Fprintf(&b, "\n  k3s said: %s", line)
	}
	if d := k3sDiagnosis(e.Log); d != "" {
		fmt.Fprintf(&b, "\n  %s", d)
	}
	fmt.Fprintf(&b, "\n  See the rest with `docker logs --tail %d %s`.", k3sLogTail, e.Container)
	return b.String()
}

// k3sFatalLine picks the line worth quoting out of a log tail: k3s names its
// own cause with level=fatal, and that one line is usually the whole story.
func k3sFatalLine(log string) string {
	var last string
	for _, line := range strings.Split(log, "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "level=fatal") || strings.Contains(line, "FATAL") {
			last = line
		}
	}
	return last
}

// k3sDiagnosis turns a known fatal into the move that fixes it. A log it does
// not recognise gets no guess -- a wrong remedy costs more than none.
func k3sDiagnosis(log string) string {
	switch {
	// Matched on the sysctl path rather than on "br_netfilter", deliberately:
	// k3s logs "Failed to load kernel module br_netfilter with modprobe" on
	// every boot, including every healthy one, so that token would attach this
	// remedy to any dead container at all. This phrase is the wrapper image's
	// own and appears nowhere else (NERD028 SPEC006).
	case strings.Contains(log, BridgePath+" does not exist"):
		return "the kernel behind your container engine has no br_netfilter loaded, so bridged " +
			"frames bypass netfilter. Every pod here shares one bridge, so a reply to a ClusterIP " +
			"comes back with the pod's own address and is dropped -- cluster DNS, every Service " +
			"call, ingress and the LoadBalancer ports all fail while the node reports Ready, which " +
			"is why the image refuses to start instead. nsctl tries to load it for you; it could " +
			"not here. Load it in the engine's virtual machine and run `nsctl env start` again -- " +
			"on Colima, `colima ssh -- sudo modprobe br_netfilter`. It does not survive " +
			"`colima stop`. `nsctl doctor` reports this as the `bridge netfilter` row."
	case strings.Contains(log, "failed to find interface with specified node ip"):
		return "the cluster's stored node IP is on no interface. Floci spawns this container on " +
			"Docker's default bridge as well as the NeuronSphere network, and the bridge's " +
			"addresses churn by start order, so the address the datastore recorded goes stale. " +
			"The hmd-img-k3s-floci wrapper disables the NetworkPolicy controller that reads it: " +
			"build or pull the current wrapper image and re-run `nsctl env start`, which " +
			"recreates the container and keeps the datastore."
	case strings.Contains(log, "--storage-backend invalid"):
		return "kube-apiserver rejected Floci's storage-backend override, which the " +
			"hmd-img-k3s-floci wrapper exists to strip. The container is not on the wrapper image."
	case strings.Contains(log, "missing controllers") && strings.Contains(log, "kubepods"):
		return "kubelet could not enable cgroup controllers for kubepods, almost always because " +
			"an earlier stop was cut off mid-teardown (SIGKILLed before containerd finished tearing " +
			"down pod cgroups) and left the subtree in a state this boot cannot delegate into. The " +
			"container cannot self-heal from `env start` -- run `nsctl env purge` to rebuild it clean."
	}
	return ""
}

// K3sContainerName is the Docker name of the k3s container Floci spawned.
//
// Floci 2.0 qualifies it by account for every account except the default, so an
// environment's cluster is floci-eks-<account>.<cluster> while the control
// plane's stays floci-eks-<cluster>. The qualified name is checked against what
// exists rather than trusted by rule alone, so a cluster created under the
// pre-2.0 name keeps working.
func K3sContainerName(cluster, accountID string, existing map[string]bool) string {
	legacy := K3sContainerPrefix + cluster
	if accountID == "" || accountID == ControlPlaneAccountID {
		return legacy
	}
	qualified := K3sContainerPrefix + accountID + "." + cluster
	if existing[qualified] {
		return qualified
	}
	if existing[legacy] {
		return legacy
	}
	return qualified
}

// K3sStartOptions configures EnsureK3sRunning.
type K3sStartOptions struct {
	Cluster       string
	AccountID     string
	ExpectedImage string
	// Network is the NeuronSphere network the container must be alone on, so
	// k3s takes its address for eth0. Empty skips normalization entirely.
	Network string
	// NormalizeRunning bounces a *running* container that is still on the
	// default bridge. A disconnect only reaches k3s on its next start, and the
	// restart is cheap because the datastore is in a volume -- but it is still
	// a restart, so it is opt-in and announced.
	NormalizeRunning bool
	// Timeout is the cold-start budget. 0 means three minutes.
	Timeout time.Duration
	// ProbeTimeout is the budget for confirming an already-running container.
	// 0 means one minute: a healthy cluster answers the first exec, so this
	// only matters when something is wrong, and a dead container is caught in
	// one poll regardless.
	ProbeTimeout time.Duration
	// Poll is the interval between attempts. 0 means three seconds.
	Poll time.Duration
}

// K3sResult is what EnsureK3sRunning found and did.
type K3sResult struct {
	Container string
	State     K3sClusterState
	// Detached reports that the container was taken off the default bridge.
	Detached bool
	// Restarted reports that a running container was bounced to apply that.
	Restarted bool
	// DetachErr is a failed normalization. Kept apart from the returned error
	// because it is worth saying and never worth refusing to start over: a
	// container on the wrong network still runs, and the cluster it serves is
	// more useful than no cluster.
	DetachErr error
}

// EnsureK3sRunning starts a stopped k3s container in place.
//
// It deliberately does not create or recreate one. Cluster creation is a
// Terraform resource in the eks-cluster DAG node, and recreating a stale
// container is ReconcileK3sCluster's job, which runs ahead of this and knows
// how to keep the datastore. A stopped container on the expected image is not
// stale: it is what a non-purge stop leaves behind.
//
// Before starting, the container is taken off Docker's default bridge so k3s
// picks the NeuronSphere network for its node IP. See DetachFromDefaultBridge
// for why Floci leaves it dual-homed and what the churn costs.
func EnsureK3sRunning(ctx context.Context, d K3sContainers, o K3sStartOptions) (K3sResult, error) {
	name := K3sContainerName(o.Cluster, o.AccountID, d.ContainerNames(ctx))
	res := K3sResult{Container: name, State: K3sMissing}
	if !d.ContainerNames(ctx)[name] {
		return res, nil
	}

	running, _ := d.Running(ctx, name)
	image := d.ImageOf(ctx, name)

	// An unreadable image is not staleness. ImageOf reports "" both for a
	// container that is gone and for one it could not inspect, and treating a
	// transient failure as stale would condemn a live cluster.
	if o.ExpectedImage != "" && image != "" && image != o.ExpectedImage {
		res.State = K3sStale
		return res, nil
	}

	if !running {
		res.State = K3sStarted
		res.Detached, res.DetachErr = d.DetachFromDefaultBridge(ctx, name, o.Network)
		if err := d.Start(ctx, name); err != nil {
			res.State = K3sStale
			return res, fmt.Errorf("starting the k3s container %s: %w", name, err)
		}
		return res, WaitForK3sAPI(ctx, d, name, o.Timeout, o.Poll)
	}

	// Running, but possibly still dual-homed from an earlier start.
	if o.NormalizeRunning && o.Network != "" && onDefaultBridge(ctx, d, name) {
		res.State = K3sStarted
		if err := d.Stop(ctx, name); err != nil {
			return res, fmt.Errorf("stopping the k3s container %s to take it off Docker's default bridge: %w", name, err)
		}
		res.Detached, res.DetachErr = d.DetachFromDefaultBridge(ctx, name, o.Network)
		res.Restarted = true
		if err := d.Start(ctx, name); err != nil {
			res.State = K3sStale
			return res, fmt.Errorf("restarting the k3s container %s: %w", name, err)
		}
		return res, WaitForK3sAPI(ctx, d, name, o.Timeout, o.Poll)
	}

	// Already up and on one network. `docker inspect` saying Running is not the
	// same as the cluster answering, so ask it.
	res.State = K3sRunning
	probe := o.ProbeTimeout
	if probe == 0 {
		probe = time.Minute
	}
	return res, WaitForK3sAPI(ctx, d, name, probe, o.Poll)
}

func onDefaultBridge(ctx context.Context, d K3sContainers, name string) bool {
	for _, n := range d.ContainerNetworks(ctx, name) {
		if n == container.DefaultBridgeNetwork {
			return true
		}
	}
	return false
}

// WaitForK3sAPI blocks until the node reports Ready.
//
// The EKS record is ACTIVE well before the kubelet is, so the API's own answer
// is the only thing worth waiting on.
//
// A container that exits mid-wait short-circuits rather than burning the whole
// budget: every exec against it fails identically, and three minutes of that
// buries the one line in `docker logs` that says why.
func WaitForK3sAPI(ctx context.Context, d K3sProbe, name string, timeout, poll time.Duration) error {
	if timeout == 0 {
		timeout = 3 * time.Minute
	}
	if poll == 0 {
		poll = 3 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		out, err := d.Exec(ctx, name, "kubectl", "get", "nodes", "--no-headers")
		if err == nil {
			for _, line := range strings.Split(string(out), "\n") {
				if fields := strings.Fields(line); len(fields) >= 2 && fields[1] == "Ready" {
					return nil
				}
			}
		} else if died := k3sDied(ctx, d, name); died != nil {
			return died
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the k3s cluster in %s had no Ready node within %s", name, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}

// k3sDied reports a container that has stopped for good, or nil.
//
// A state that could not be read is not death, and neither is `created` or
// `restarting`: condemning a container mid-launch, or over an unanswered docker
// question, would fail starts that were fine.
func k3sDied(ctx context.Context, d K3sInspector, name string) error {
	st, ok := d.InspectState(ctx, name)
	if !ok || st.Running || st.Status == "created" || st.Status == "restarting" {
		return nil
	}
	return &K3sDiedError{Container: name, State: st, Log: d.Logs(ctx, name, k3sLogTail)}
}

// VerifyK3sAlive reports an error when the k3s container is not running.
//
// Every step in the cluster provisioning is a `docker exec` whose failure is a
// warning -- right individually, wrong in aggregate: a container that died
// mid-provision produces a page of "container is not running" warnings, fails
// at nothing, and is then advertised as ready on a port nothing listens on.
// This is the check that turns that into an exit code.
//
// A container whose state cannot be read is reported alive. Never fail a start
// on a question docker did not answer.
func VerifyK3sAlive(ctx context.Context, d K3sInspector, name string) error {
	if name == "" {
		return nil
	}
	return k3sDied(ctx, d, name)
}

// WriteKubeconfig fetches the cluster's kubeconfig and writes it host-readable.
//
// It is read from /etc/rancher/k3s/k3s.yaml inside the container, which carries
// the real client certificate and key -- Floci's synthesized fallback uses a
// placeholder token that cannot authenticate, and every later kubectl then
// fails with "the server has asked for the client to provide credentials",
// which reads like a cluster problem rather than a missing file.
//
// hostPort is the hmd_proxy stream listener, not the port Floci publishes on
// the container. Docker re-creates that published forward on every restart and
// a re-created one truncates the TLS 1.3 ClientHello kubectl sends, so the
// cluster looks unreachable while being perfectly healthy.
func WriteKubeconfig(ctx context.Context, d K3sExecer, name, path string, hostPort int) error {
	out, err := d.Exec(ctx, name, "cat", "/etc/rancher/k3s/k3s.yaml")
	if err != nil {
		return fmt.Errorf("reading the kubeconfig from %s: %w", name, err)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(out)), "apiVersion") {
		return fmt.Errorf("%s returned no kubeconfig", name)
	}
	rewritten, err := PointKubeconfigAtHost(out, hostPort)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	// A *directory* here is Docker's doing, not a user's. A deploy that mounts
	// this path before the cluster has written it makes the daemon create the
	// missing source as a directory, after which every write fails ("is a
	// directory") and every later deploy mounts the directory instead of a
	// config -- projectbuilder then dies with `IsADirectoryError: [Errno 21]
	// Is a directory: '/root/.kube/config'`, naming the container's path and
	// nothing that leads back here. A directory is never a valid kubeconfig,
	// so removing it is unambiguous.
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("removing the directory Docker created at %s: %w", path, err)
		}
	}
	if err := os.WriteFile(path, rewritten, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// PointKubeconfigAtHost rewrites a kubeconfig's server to a host-reachable port.
//
// k3s writes https://127.0.0.1:6443, which is correct only inside the
// container. TLS verification is turned off with the repoint because the
// certificate is issued for the in-cluster name.
func PointKubeconfigAtHost(raw []byte, hostPort int) ([]byte, error) {
	var cfg map[string]any
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parsing the kubeconfig: %w", err)
	}
	clusters, ok := cfg["clusters"].([]any)
	if !ok {
		return nil, fmt.Errorf("the kubeconfig declares no clusters")
	}
	for _, entry := range clusters {
		item, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		cluster, ok := item["cluster"].(map[string]any)
		if !ok {
			cluster = map[string]any{}
			item["cluster"] = cluster
		}
		cluster["server"] = "https://127.0.0.1:" + strconv.Itoa(hostPort)
		cluster["insecure-skip-tls-verify"] = true
		delete(cluster, "certificate-authority-data")
		delete(cluster, "certificate-authority")
	}
	return yaml.Marshal(cfg)
}

// K3sClusters is the EKS surface the reconcile needs.
type K3sClusters interface {
	ClusterExists(ctx context.Context, name string) (exists, ok bool)
	DeleteCluster(ctx context.Context, name string) error
}

// K3sReconcileContainers is the Docker surface the reconcile needs, including
// the destructive half EnsureK3sRunning deliberately does without.
type K3sReconcileContainers interface {
	ContainerNames(ctx context.Context) map[string]bool
	Running(ctx context.Context, name string) (bool, error)
	ImageOf(ctx context.Context, name string) string
	RemoveContainer(ctx context.Context, name string) error
	RemoveVolumes(ctx context.Context, names ...string) error
}

// K3sReconcileAction is what ReconcileK3sCluster did.
type K3sReconcileAction int

const (
	// K3sReconcileNone means the cluster is healthy, or repairable in place by
	// EnsureK3sRunning.
	K3sReconcileNone K3sReconcileAction = iota
	// K3sReconcileNoCluster means there is no record yet; the DAG creates it.
	K3sReconcileNoCluster
	// K3sReconcileUnreadable means the container's image could not be read, so
	// the cluster was left alone.
	K3sReconcileUnreadable
	// K3sReconcileCleared means the record and container were dropped, and the
	// next deploy will recreate them.
	K3sReconcileCleared
)

// K3sReconcileOptions configures ReconcileK3sCluster.
type K3sReconcileOptions struct {
	Cluster       string
	AccountID     string
	ExpectedImage string
	// WaitGone is how long to wait for Floci to finish the delete. 0 means one
	// minute.
	WaitGone time.Duration
	// Poll is the interval between checks. 0 means two seconds.
	Poll time.Duration
	// DropVolume also removes the cluster's /var/lib/rancher/k3s datastore.
	//
	// Off for a repair, and that is a deliberate divergence from the Python's
	// reconcile_k3s_container, which always drops it. Its reason was the
	// ghost-node failure: a respawned container took a new random hostname,
	// registered as a second Node, and left the first NotReady forever,
	// orphaning StatefulSet pods. The k3s wrapper image pins --node-name now,
	// so a container respawned against the same volume re-registers as the
	// same Node and its pods resume.
	//
	// Keeping the volume is what makes a wrapper-image bump a repair instead of
	// a full BOM redeploy: Floci names the volume after the cluster, so the
	// recreated container re-adopts the same datastore and every Helm release
	// on it. Two things must hold for that to be safe -- the pinned --node-name
	// above, and a k3s that does not die on the datastore's recorded node IP
	// (which is why the wrapper disables the NetworkPolicy controller). If
	// either is reverted, this must go back to dropping the volume.
	DropVolume bool
}

// K3sVolumeCandidates is every name Floci might have given this cluster's
// /var/lib/rancher/k3s volume.
//
// Floci derives the volume name the way it derives the container name, so an
// environment's is account-qualified. Unlike the container, there is no cheap
// way to confirm which form a *deleted* cluster used, and `docker volume rm` on
// a name that is not there is a no-op -- so both are returned and the ambiguity
// costs nothing.
func K3sVolumeCandidates(cluster, accountID string) []string {
	legacy := K3sContainerPrefix + cluster
	if accountID == "" || accountID == ControlPlaneAccountID {
		return []string{legacy}
	}
	return []string{K3sContainerPrefix + accountID + "." + cluster, legacy}
}

// ReconcileK3sCluster clears a cluster record whose backing container Floci
// cannot serve, so the eks-cluster deploy creates it fresh.
//
// This is the cluster-side counterpart ReconcileRDSInstance's comment points
// at, ported from the Python's reconcile_k3s_container. Terraform's refresh
// reconciles the *record*: a cluster that exists is "no changes", however dead
// the container behind it is, and Floci pins the node image into the record at
// creation, so bumping FLOCI_SERVICES_EKS_DEFAULT_IMAGE is not drift on any
// attribute Terraform tracks. Only clearing the record rebuilds the container.
//
// A stopped container on the expected image is not stale -- it is what a
// non-purge stop leaves behind, and EnsureK3sRunning starts it in place.
//
// Two cases are deliberately stricter than the Python. Floci failing to answer
// never leads to a delete, and a container whose image cannot be read is left
// alone: both are questions rather than answers, and destroying a cluster over
// one costs a datastore. A container that exists but will not start is also
// left alone, where the Python recreates it -- `docker start` returning
// non-zero is not evidence the datastore is bad, and `hmd neuronsphere down --purge`
// is the deliberate way to ask for that.
func ReconcileK3sCluster(ctx context.Context, api K3sClusters, d K3sReconcileContainers,
	o K3sReconcileOptions) (K3sReconcileAction, error) {

	// "" means "do not judge staleness", the same rule EnsureK3sRunning follows.
	// Without a pin to compare against there is no such thing as a stale
	// container, and guessing one would delete a healthy cluster.
	if o.ExpectedImage == "" {
		return K3sReconcileNone, nil
	}

	exists, ok := api.ClusterExists(ctx, o.Cluster)
	if !ok {
		return K3sReconcileNone, fmt.Errorf(
			"could not ask Floci about the k3s cluster %s; leaving it alone", o.Cluster)
	}
	if !exists {
		return K3sReconcileNoCluster, nil
	}

	name := K3sContainerName(o.Cluster, o.AccountID, d.ContainerNames(ctx))
	image := d.ImageOf(ctx, name)
	if image == o.ExpectedImage {
		// Healthy, or stopped on the right image: EnsureK3sRunning's job.
		return K3sReconcileNone, nil
	}
	if image == "" && d.ContainerNames(ctx)[name] {
		// ImageOf reports "" both for a container that is gone and for one it
		// could not inspect. Only the first is staleness.
		return K3sReconcileUnreadable, nil
	}

	if err := api.DeleteCluster(ctx, o.Cluster); err != nil {
		return K3sReconcileNone, err
	}
	if err := waitForK3sClusterGone(ctx, api, o.Cluster, o.WaitGone, o.Poll); err != nil {
		return K3sReconcileNone, err
	}
	// Floci's DeleteCluster tears the container down asynchronously and leaves
	// both it and the volume behind, so the recreate must not race a survivor.
	if err := d.RemoveContainer(ctx, name); err != nil {
		return K3sReconcileNone, err
	}
	if o.DropVolume {
		if err := d.RemoveVolumes(ctx, K3sVolumeCandidates(o.Cluster, o.AccountID)...); err != nil {
			return K3sReconcileNone, err
		}
	}
	return K3sReconcileCleared, nil
}

// waitForK3sClusterGone blocks until Floci stops reporting the cluster.
//
// A create issued while the teardown is still running races it and comes back
// ResourceInUseException, which reads like a name collision rather than a
// timing problem.
func waitForK3sClusterGone(ctx context.Context, api K3sClusters, name string, timeout, poll time.Duration) error {
	if timeout == 0 {
		timeout = time.Minute
	}
	if poll == 0 {
		poll = 2 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		exists, ok := api.ClusterExists(ctx, name)
		if ok && !exists {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("Floci still reports the k3s cluster %s after %s; not removing its container", name, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}
