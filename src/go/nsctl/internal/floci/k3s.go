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
)

// K3sContainerPrefix is the prefix Floci gives a spawned k3s container.
const K3sContainerPrefix = "floci-eks-"

// K3sAPIPort is the API server's port inside the container.
const K3sAPIPort = 6443

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
	// wrapper. Recreating it is the DAG's job too, and doing it here would drop
	// the datastore.
	K3sStale
)

// K3sContainers is the Docker surface the k3s lifecycle needs.
type K3sContainers interface {
	ContainerNames(ctx context.Context) map[string]bool
	Running(ctx context.Context, name string) (bool, error)
	ImageOf(ctx context.Context, name string) string
	Start(ctx context.Context, name string) error
	Exec(ctx context.Context, name string, args ...string) ([]byte, error)
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

// EnsureK3sRunning starts a stopped k3s container in place.
//
// It deliberately does not create or recreate one. Cluster creation is a
// Terraform resource in the eks-cluster DAG node, and recreating a stale
// container drops the k3s datastore along with every Helm release on it -- which
// is exactly what makes the next start redeploy the whole BOM. A stopped
// container on the expected image is not stale: it is what a non-purge stop
// leaves behind.
func EnsureK3sRunning(ctx context.Context, d K3sContainers, cluster, accountID, expectedImage string, timeout, poll time.Duration) (string, K3sClusterState, error) {
	name := K3sContainerName(cluster, accountID, d.ContainerNames(ctx))
	if !d.ContainerNames(ctx)[name] {
		return name, K3sMissing, nil
	}

	running, _ := d.Running(ctx, name)
	image := d.ImageOf(ctx, name)

	// An unreadable image is not staleness. ImageOf reports "" both for a
	// container that is gone and for one it could not inspect, and treating a
	// transient failure as stale would condemn a live cluster.
	if expectedImage != "" && image != "" && image != expectedImage {
		return name, K3sStale, nil
	}
	if running {
		return name, K3sRunning, nil
	}
	if err := d.Start(ctx, name); err != nil {
		return name, K3sStale, fmt.Errorf("starting the k3s container %s: %w", name, err)
	}
	if err := WaitForK3sAPI(ctx, d, name, timeout, poll); err != nil {
		return name, K3sStarted, err
	}
	return name, K3sStarted, nil
}

// WaitForK3sAPI blocks until the node reports Ready.
//
// The EKS record is ACTIVE well before the kubelet is, so the API's own answer
// is the only thing worth waiting on.
func WaitForK3sAPI(ctx context.Context, d K3sContainers, container string, timeout, poll time.Duration) error {
	if timeout == 0 {
		timeout = 3 * time.Minute
	}
	if poll == 0 {
		poll = 3 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		out, err := d.Exec(ctx, container, "kubectl", "get", "nodes", "--no-headers")
		if err == nil {
			for _, line := range strings.Split(string(out), "\n") {
				if fields := strings.Fields(line); len(fields) >= 2 && fields[1] == "Ready" {
					return nil
				}
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the k3s cluster in %s had no Ready node within %s", container, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
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
func WriteKubeconfig(ctx context.Context, d K3sContainers, container, path string, hostPort int) error {
	out, err := d.Exec(ctx, container, "cat", "/etc/rancher/k3s/k3s.yaml")
	if err != nil {
		return fmt.Errorf("reading the kubeconfig from %s: %w", container, err)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(out)), "apiVersion") {
		return fmt.Errorf("%s returned no kubeconfig", container)
	}
	rewritten, err := PointKubeconfigAtHost(out, hostPort)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
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
