package k3s

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/container"
	"gopkg.in/yaml.v3"
)

// Canonical in-cluster hostnames. Mapping these to an environment's own
// containers is what lets cloud Helm charts run unmodified: a chart's
// AWS_ENDPOINT_URL=http://neuronsphere:4566, its JDBC URL against hmd_db and
// its Gremlin endpoint on global-graph are all scoped to that environment's own
// account and databases without editing the chart.
const (
	FlociHost      = "neuronsphere"
	WorkloadHost   = "neuronsphere-workload"
	ControlHost    = "neuronsphere-control"
	DBHost         = "hmd_db"
	GraphHost      = "global-graph"
	ProxyContainer = "hmd_proxy"
	// FlociContainer is the single Floci container's Docker name. Every
	// environment shares it and is told apart by the account its requests are
	// signed for, not by hostname.
	FlociContainer = "floci"
)

// Traefik, which serves every Ingress an environment's charts expose.
const (
	traefikManifestPath  = "/var/lib/rancher/k3s/server/manifests/neuronsphere-traefik.yaml"
	traefikLabelSelector = "app.kubernetes.io/name=traefik"
	traefikResourceKinds = "deployments,services,serviceaccounts,ingressclasses,clusterroles,clusterrolebindings"
	// TraefikController is the controller name the alb IngressClass points at.
	TraefikController = "traefik.io/ingress-controller"
	// DefaultIngressClass is the class the cloud's charts ask for. Every
	// NeuronSphere repo renders its Ingress for the AWS ALB controller, so
	// locally Traefik is made to answer to that class rather than editing the
	// charts -- the same way Floci answers to the AWS APIs and CoreDNS answers
	// to the in-network hostnames.
	DefaultIngressClass = "alb"
)

// DockerAPI is the host-side Docker surface the operators need. Only three
// things genuinely have to happen outside the cluster: resolving a container's
// IP for the CoreDNS records, and the two edits to the k3s node's own
// filesystem.
type DockerAPI interface {
	ContainerIP(ctx context.Context, name, network string) string
	// ContainerIPByAlias resolves a network alias, which ContainerIP cannot:
	// docker inspect takes a container name or id and an alias is neither.
	ContainerIPByAlias(ctx context.Context, alias, network string) string
	Exec(ctx context.Context, name string, args ...string) ([]byte, error)
	Run(ctx context.Context, args ...string) (stdout, stderr []byte, err error)
	// InspectState reads a container's runtime state, used to stop Provision
	// running further steps once the cluster is confirmed gone rather than
	// exec'ing into it a handful more times.
	InspectState(ctx context.Context, name string) (container.State, bool)
}

// Environment is what the operators need to know about the target environment.
type Environment struct {
	Slug string
	// DBContainer and GraphContainer are the per-environment DNS aliases, which
	// the connection secrets carry.
	DBContainer    string
	GraphContainer string
	// CoreInstanceName is what workload charts pin their nodeAffinity to.
	CoreInstanceName string
	// AuthHost is the mock identity provider's issuer hostname, or "" when it
	// is not enabled.
	AuthHost string
	// K3sCluster is the cluster name; K3sContainer is the Floci-spawned
	// container that holds its manifests.
	K3sCluster   string
	K3sContainer string
}

// Operators provisions the cluster-level state an environment needs before
// anything deploys onto it.
type Operators struct {
	Kube   *Kube
	Docker DockerAPI
	Env    Environment
	// Network is the Docker network container IPs are resolved on.
	Network string
	// IngressClass is the class Traefik is made to answer to.
	IngressClass string
	// IngressEnabled removes the baked-in Traefik when false.
	IngressEnabled bool
	Out            io.Writer
	Err            io.Writer
}

func (o *Operators) step(format string, a ...any) {
	if o.Out != nil {
		fmt.Fprintf(o.Out, "  "+format+"\n", a...)
	}
}

func (o *Operators) warn(format string, a ...any) {
	if o.Err != nil {
		fmt.Fprintf(o.Err, "warning: "+format+"\n", a...)
	}
}

func (o *Operators) ingressClass() string {
	if o.IngressClass == "" {
		return DefaultIngressClass
	}
	return o.IngressClass
}

// Provision brings the cluster to the state an environment's workloads assume.
//
// Best effort throughout: a step that fails warns and the rest still run,
// because a partially provisioned cluster is more useful than none and each
// failure names itself.
//
// The k3s image bakes the Traefik chart into k3s's auto-deploying manifests
// directory, so there is no helm install to run from here -- which is why
// SPEC008 can say helm is eliminated rather than relocated.
func (o *Operators) Provision(ctx context.Context) error {
	// One container: wait for a Ready node, reap the ghosts a reused data
	// volume leaves behind, and label the live node.
	if err := o.PrepareNode(ctx); err != nil {
		o.warn("%v", err)
	}
	if !o.k3sAlive(ctx) {
		return nil
	}
	// One container, plus host-side IP lookups that cannot happen inside it.
	if err := o.EnsureCoreDNSRecords(ctx); err != nil {
		o.warn("%v", err)
	}
	if !o.k3sAlive(ctx) {
		return nil
	}
	if err := o.EnsureIngressController(ctx); err != nil {
		o.warn("%v", err)
	}
	return nil
}

// k3sAlive reports whether the k3s container is still running, so Provision
// can stop issuing more docker execs once the cluster is confirmed gone
// instead of turning one death into a cascade of unrelated-looking warnings.
//
// A hiccup asking (InspectState's second return false) is read as "keep
// going," not "it died" -- the same doctrine InspectState itself documents:
// a docker hiccup misread as a dead cluster would fail a start that was fine.
// VerifyK3sAlive (internal/floci) still runs after Provision returns and
// remains the source of the actual diagnosis; this only stops wasted work.
func (o *Operators) k3sAlive(ctx context.Context) bool {
	state, ok := o.Docker.InspectState(ctx, o.Env.K3sContainer)
	return !ok || state.Running
}

// prepareNodeScript waits for readiness, reaps stale registrations and labels
// the node, in a single container.
//
// The wait is a loop *inside* the container rather than repeated container
// starts, which is what keeps a 120-second wait from costing thirty container
// launches.
const prepareNodeScript = `set -u

# wait_for_node_ready: the Floci EKS status is ACTIVE well before the kubelet,
# CNI and CoreDNS are. Scheduling anything before a node is Ready fails in ways
# that read like chart problems.
deadline=$(( $(date +%%s) + %d ))
while [ "$(date +%%s)" -lt "$deadline" ]; do
    if kubectl get nodes --no-headers 2>/dev/null | awk '{print $2}' | grep -qx Ready; then
        break
    fi
    sleep 4
done

# clean_stale_nodes: Floci reuses the k3s data volume across cluster
# delete/recreate, so each recreation leaves the previous node registered but
# NotReady. Those ghosts, and the node-affine local-path PVs bound to them,
# poison scheduling for new pods.
live=""
kubectl get nodes --no-headers 2>/dev/null | while read -r name status rest; do
    if [ "$status" = "Ready" ]; then
        echo "$name" >> /tmp/live-nodes
    else
        kubectl delete node "$name" --ignore-not-found >/dev/null 2>&1
    fi
done
touch /tmp/live-nodes

# Release PVs pinned by node affinity to a node that no longer exists, so their
# PVCs re-provision on the live one.
kubectl get pv -o 'jsonpath={range .items[*]}{.metadata.name}{"|"}{.spec.nodeAffinity.required.nodeSelectorTerms[0].matchExpressions[0].values[0]}{"\n"}{end}' 2>/dev/null \
| while IFS='|' read -r pv node; do
    [ -n "$pv" ] || continue
    [ -n "$node" ] || continue
    if ! grep -qx "$node" /tmp/live-nodes 2>/dev/null; then
        kubectl delete pv "$pv" --ignore-not-found --wait=false >/dev/null 2>&1
    fi
done

# ensure_node_topology_labels: cloud EKS nodes carry topology zone/region
# labels and their node groups carry hmdlabs.io/repo-instance-name. A chart with
# topologySpreadConstraints keyed on zone finds "0/1 nodes match" without them,
# and a workload pinning a required nodeAffinity to its compute dependency --
# which resolves to the core instance locally -- never schedules at all.
for node in $(kubectl get nodes -o name 2>/dev/null); do
    kubectl label "$node" \
        topology.kubernetes.io/zone=local \
        topology.kubernetes.io/region=local \
        'hmdlabs.io/repo-instance-name=%s' \
        --overwrite >/dev/null 2>&1
done

kubectl get nodes --no-headers 2>/dev/null | awk '{print $1, $2}'
`

// PrepareNode waits for a Ready node, reaps stale registrations and orphaned
// PVs, and applies the topology labels.
func (o *Operators) PrepareNode(ctx context.Context) error {
	script := fmt.Sprintf(prepareNodeScript, 120, o.Env.CoreInstanceName)
	stdout, stderr, err := o.Kube.RunKube(ctx, []byte(script), nil)
	if err != nil {
		return &StepError{Step: "preparing the k3s node", Stdout: stdout, Stderr: stderr, Err: err}
	}
	ready := 0
	for _, line := range strings.Split(strings.TrimSpace(string(stdout)), "\n") {
		if fields := strings.Fields(line); len(fields) >= 2 && fields[1] == "Ready" {
			ready++
		}
	}
	if ready == 0 {
		return fmt.Errorf("no k3s node became Ready; workloads will not schedule")
	}
	o.step("%d node(s) Ready and labelled", ready)
	return nil
}

// CoreDNSRecord is one canonical hostname and the address it answers with.
type CoreDNSRecord struct {
	Host string
	IP   string
}

// CoreDNSRecords resolves the canonical hostnames to this environment's
// containers.
//
// A name that does not resolve on the Docker network is skipped rather than
// guessed at. The database is warned about because every chart addressing it by
// name then fails to connect; the graph is not, because it is provisioned only
// when something asks for one and its absence is the normal case.
func (o *Operators) CoreDNSRecords(ctx context.Context, dbContainer, graphContainer string) []CoreDNSRecord {
	var records []CoreDNSRecord
	add := func(host, container string) bool {
		ip := o.Docker.ContainerIP(ctx, container, o.Network)
		if ip == "" {
			return false
		}
		records = append(records, CoreDNSRecord{Host: host, IP: ip})
		return true
	}

	flociIP := o.Docker.ContainerIP(ctx, FlociContainer, o.Network)
	if flociIP == "" {
		o.warn("could not resolve the Floci container's IP; skipping the CoreDNS records entirely")
		return nil
	}
	records = append(records,
		CoreDNSRecord{Host: FlociHost, IP: flociIP},
		CoreDNSRecord{Host: WorkloadHost, IP: flociIP},
		// The control plane is shared; charts that need it address it
		// explicitly rather than through the per-environment name.
		CoreDNSRecord{Host: ControlHost, IP: flociIP},
	)
	add(ProxyContainer, ProxyContainer)

	// The identity provider's issuer hostname, resolved to the proxy that
	// answers for it.
	//
	// A pod is the third of the three places the issuer has to resolve, and the
	// hardest to notice when it does not: the browser and the Floci Lambdas get
	// there through /etc/hosts and a Docker network alias respectively, and a
	// cluster that cannot resolve the name fails only inside whichever
	// application is trying to complete a login. Empty when auth is off.
	if o.Env.AuthHost != "" {
		add(o.Env.AuthHost, ProxyContainer)
	}

	// The database under *both* names it is addressed by. hmd_db is what
	// unmodified cloud charts use; hmd_db-<slug> is what the connection secrets
	// carry, because on the Docker network plain hmd_db is the control plane's
	// database and an environment's workloads must not reach that one. A chart
	// and the secret it consumes disagree about which name they use, so both
	// have to answer inside the cluster.
	if dbContainer != "" {
		if ip := o.Docker.ContainerIP(ctx, dbContainer, o.Network); ip != "" {
			records = append(records, CoreDNSRecord{Host: DBHost, IP: ip})
			if o.Env.DBContainer != "" && o.Env.DBContainer != DBHost {
				records = append(records, CoreDNSRecord{Host: o.Env.DBContainer, IP: ip})
			}
		} else {
			o.warn("could not resolve the database container for %q; %s will not resolve in-cluster and every chart addressing the database by name will fail to connect", o.Env.Slug, DBHost)
		}
	} else {
		o.warn("no database container for %q; %s will not resolve in-cluster", o.Env.Slug, DBHost)
	}

	// The graph, under both names, for the same reason. Absent is normal.
	if graphContainer != "" {
		if ip := o.Docker.ContainerIP(ctx, graphContainer, o.Network); ip != "" {
			records = append(records, CoreDNSRecord{Host: GraphHost, IP: ip})
			if o.Env.GraphContainer != "" && o.Env.GraphContainer != GraphHost {
				records = append(records, CoreDNSRecord{Host: o.Env.GraphContainer, IP: ip})
			}
		}
	} else if ip := o.Docker.ContainerIPByAlias(ctx, GraphHost, o.Network); ip != "" {
		// No graph of its own does not mean no graph. An environment may bind
		// graph-db to the shared control-plane one instead of deploying a
		// second -- hmd-stack-analytics does exactly that -- and then
		// `global-graph` answers on the Docker network while nothing answers
		// for it in the cluster.
		//
		// A pod still has to resolve it. Trino loads its catalogs at startup
		// and the nsgraph connector opens ws://global-graph:8182/gremlin
		// there, so the coordinator never reaches ready, and with --atomic the
		// release is rolled back before anything can be inspected: the only
		// symptom is "context deadline exceeded" on a chart that looks fine.
		records = append(records, CoreDNSRecord{Host: GraphHost, IP: ip})
	}
	return records
}

// CoreDNSConfigMap renders the coredns-custom ConfigMap.
//
// k3s imports /etc/coredns/custom/*.server, and each hostname gets its own
// server block -- not a second hosts plugin inside .:53, which crashes CoreDNS.
func CoreDNSConfigMap(records []CoreDNSRecord) ([]byte, error) {
	var server strings.Builder
	for _, r := range records {
		fmt.Fprintf(&server, "%s:53 {\n    hosts {\n        %s %s\n        fallthrough\n    }\n}\n", r.Host, r.IP, r.Host)
	}
	return yaml.Marshal(map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "coredns-custom", "namespace": "kube-system"},
		"data":       map[string]any{"neuronsphere.server": server.String()},
	})
}

// coreDNSScript applies the ConfigMap and rolls CoreDNS so it reloads at once.
//
// The ConfigMap arrives as a mounted file rather than on stdin: `apply -f -`
// has no equivalent when the caller is a script inside a container.
//
// The wait is for a race only a cold start reaches. k3s creates CoreDNS from an
// addon manifest *after* the node goes Ready, and on a cluster the deploy has
// just created this script can get there first -- `apply` succeeds, then
// `rollout restart` fails with `deployments.apps "coredns" not found` and
// `set -e` takes the whole step down. The records are on disk by then, so the
// failure costs only the immediate reload, but it is reported as a warning
// against a cluster that looks healthy, which is the least useful place to
// learn about it. Waiting for the deployment to exist is the same shape as the
// node-Ready wait above, for the same reason.
const coreDNSScript = `set -eu
kubectl apply -f "$STEP_DIR/coredns.yaml"
i=0
while [ "$i" -lt 60 ]; do
  kubectl -n kube-system get deploy coredns >/dev/null 2>&1 && break
  i=$((i+1))
  sleep 2
done
kubectl -n kube-system rollout restart deploy coredns
kubectl -n kube-system rollout status deploy coredns --timeout=60s
`

// EnsureCoreDNSRecords maps the canonical hostnames to this environment's
// containers. Idempotent: it rewrites the whole ConfigMap.
func (o *Operators) EnsureCoreDNSRecords(ctx context.Context) error {
	return o.EnsureCoreDNSRecordsFor(ctx, o.Env.DBContainer, o.Env.GraphContainer)
}

// EnsureCoreDNSRecordsFor is EnsureCoreDNSRecords with the backing containers
// named explicitly, since they are Floci-spawned and opaquely named.
func (o *Operators) EnsureCoreDNSRecordsFor(ctx context.Context, dbContainer, graphContainer string) error {
	records := o.CoreDNSRecords(ctx, dbContainer, graphContainer)
	if len(records) == 0 {
		return fmt.Errorf("no CoreDNS records to apply")
	}
	manifest, err := CoreDNSConfigMap(records)
	if err != nil {
		return fmt.Errorf("rendering the CoreDNS ConfigMap: %w", err)
	}
	stdout, stderr, err := o.Kube.RunKube(ctx, []byte(coreDNSScript), map[string][]byte{"coredns.yaml": manifest})
	if err != nil {
		return &StepError{Step: "applying the CoreDNS records", Stdout: stdout, Stderr: stderr, Err: err}
	}
	names := make([]string, 0, len(records))
	for _, r := range records {
		names = append(names, r.Host+"->"+r.IP)
	}
	o.step("CoreDNS: %s", strings.Join(names, ", "))
	return nil
}

// PatchTraefikManifest makes the baked-in Traefik manifest schedulable and
// ALB-classed.
//
// Both edits are applied to the *file*, inside the k3s container. The Deployment
// is owned by a k3s Addon, which reverts any live `kubectl patch`, so the
// manifest is the only durable place to change it -- and editing it changes the
// content hash, which is what makes the addon controller re-apply.
//
//  1. Drop hostPort 80/443. k3s ServiceLB creates svclb-* pods for every
//     type: LoadBalancer service, and those are system-node-critical, so a
//     chart exposing 443 preempts Traefik off the host port permanently.
//     Traefik needs no host port here; it is reached through a NodePort.
//  2. Set the ingress class, so cloud charts resolve unmodified.
//
// Idempotent: both edits match nothing on a second run.
func (o *Operators) PatchTraefikManifest(ctx context.Context) error {
	container := o.Env.K3sContainer
	if container == "" {
		return fmt.Errorf("no k3s container name; cannot patch the Traefik manifest")
	}

	if _, err := o.Docker.Exec(ctx, container,
		"sed", "-i", `/^[[:space:]]*hostPort: \(80\|443\)$/d`, traefikManifestPath,
	); err != nil {
		return fmt.Errorf("could not strip Traefik's host ports in %s: %w\nthe ingress controller will stay Pending if any LoadBalancer service holds host port 80 or 443, and no UI will be reachable", container, err)
	}

	arg := "--providers.kubernetesingress.ingressclass=" + o.ingressClass()
	if _, err := o.Docker.Exec(ctx, container, "grep", "-q", "--", arg, traefikManifestPath); err == nil {
		return nil // already set
	}
	// Inserted after the container's args: key, reusing its indentation. The
	// manifest has exactly one args: line, so a plain substitution needs no
	// line address -- which matters because k3s ships busybox sed, not GNU sed.
	expr := `s|^\([[:space:]]*\)args:$|\1args:\n\1  - "` + arg + `"|`
	if _, err := o.Docker.Exec(ctx, container, "sed", "-i", expr, traefikManifestPath); err != nil {
		return fmt.Errorf("could not set Traefik's ingress class in %s: %w\nIngresses declaring class %q will not be served", container, err, o.ingressClass())
	}
	return nil
}

// IngressClassManifest is the alb IngressClass backed by the local Traefik.
//
// It complements the Traefik arg: the arg is what actually makes Traefik serve
// these Ingresses, while this object makes spec.ingressClassName: alb a live
// reference rather than a dangling one, so `kubectl get ingress` reports the
// class correctly.
//
// Deliberately not marked the default class: k3s already ships a default
// traefik IngressClass, and two defaults make the API server reject class-less
// Ingresses as ambiguous.
func IngressClassManifest(class string) ([]byte, error) {
	return json.Marshal(map[string]any{
		"apiVersion": "networking.k8s.io/v1",
		"kind":       "IngressClass",
		"metadata": map[string]any{
			"name":   class,
			"labels": map[string]any{"app.kubernetes.io/managed-by": "hmd-cli-neuronsphere"},
		},
		"spec": map[string]any{"controller": TraefikController},
	})
}

// ingressWaitScript waits for Traefik and then registers the class, in one
// container. The wait is a loop inside it rather than repeated launches.
const ingressWaitScript = `set -u
deadline=$(( $(date +%%s) + %d ))
ready=0
while [ "$(date +%%s)" -lt "$deadline" ]; do
    if kubectl -n kube-system rollout status deploy/traefik --timeout=10s >/dev/null 2>&1; then
        ready=1
        break
    fi
    sleep 4
done
if [ "$ready" -ne 1 ]; then
    echo "traefik-not-ready"
    exit 1
fi
kubectl apply -f "$STEP_DIR/ingressclass.json"
echo "traefik-ready"
`

// ingressDisableScript removes the baked-in Traefik.
const ingressDisableScript = `set -u
kubectl -n kube-system delete ` + traefikResourceKinds + ` -l ` + traefikLabelSelector + ` --ignore-not-found
`

// EnsureIngressController waits for the image-baked Traefik, or removes it.
//
// The k3s image renders the Traefik chart at build time and bakes the resulting
// plain manifest into k3s's auto-deploying manifests directory, so it installs
// itself on cluster boot. k3s only applies that manifest on a content-hash
// change, so it will not resurrect resources a prior disable removed -- hence
// the re-apply before waiting, which makes toggling ingress back on work
// without recreating the cluster.
func (o *Operators) EnsureIngressController(ctx context.Context) error {
	if !o.IngressEnabled {
		o.step("ingress disabled; removing the baked-in Traefik")
		stdout, stderr, err := o.Kube.RunKube(ctx, []byte(ingressDisableScript), nil)
		if err != nil {
			return &StepError{Step: "removing Traefik", Stdout: stdout, Stderr: stderr, Err: err}
		}
		return nil
	}

	o.warnLegacyTraefikRelease(ctx)

	if err := o.PatchTraefikManifest(ctx); err != nil {
		o.warn("%v", err)
	}
	// Re-apply from inside the k3s node container: a no-op if the addon
	// controller already applied it at boot, but it restores resources a prior
	// disable removed, which k3s does not otherwise reconcile.
	if o.Env.K3sContainer != "" {
		if _, err := o.Docker.Exec(ctx, o.Env.K3sContainer, "kubectl", "apply", "-f", traefikManifestPath); err != nil {
			// Not fatal: the addon controller usually got there first.
			o.warn("could not re-apply the baked-in Traefik manifest: %v", err)
		}
	}

	class, err := IngressClassManifest(o.ingressClass())
	if err != nil {
		return fmt.Errorf("rendering the IngressClass: %w", err)
	}
	script := fmt.Sprintf(ingressWaitScript, 120)
	stdout, stderr, err := o.Kube.RunKube(ctx, []byte(script), map[string][]byte{"ingressclass.json": class})
	if err != nil {
		// Worth surfacing rather than only logging: a Traefik that never
		// schedules leaves every Ingress-exposed UI silently unreachable, with
		// nothing in the output pointing at the cause.
		return &StepError{
			Step:   "the ingress controller (Traefik) did not become ready; Ingress-exposed UIs will be unreachable",
			Stdout: stdout, Stderr: stderr, Err: err,
		}
	}
	o.step("ingress controller ready; IngressClass %q -> %s", o.ingressClass(), TraefikController)
	return nil
}

// readsScript collects the two cluster reads in one container.
const readsScript = `set -u
echo "---uid---"
kubectl get namespace kube-system -o jsonpath='{.metadata.uid}' 2>/dev/null
echo
echo "---releases---"
kubectl get secrets --all-namespaces -l owner=helm -o 'jsonpath={range .items[*]}{.metadata.labels.name}{"\n"}{end}' 2>/dev/null
`

// ClusterState is what one read of the cluster reports.
type ClusterState struct {
	// IncarnationID fingerprints the running cluster's identity. The
	// kube-system Namespace is created fresh when a cluster comes up and never
	// recreated for its lifetime, so its UID distinguishes "the cluster was
	// deleted and recreated" from "the container restarted". Empty when the
	// cluster could not be read.
	IncarnationID string
	// HelmReleases are the releases currently installed. Helm stores one Secret
	// per release revision, labelled owner=helm with the release name in
	// metadata.labels.name; listing those is cheaper than `helm list -A` and
	// needs no helm binary anywhere.
	HelmReleases map[string]bool
	// Read is false when the cluster could not be reached at all -- explicitly
	// distinct from "the cluster has no releases", so a caller can decline to
	// act rather than propose a wholesale redeploy.
	Read bool
}

// ReadClusterState collects the cluster identity and its Helm releases.
func (o *Operators) ReadClusterState(ctx context.Context) ClusterState {
	stdout, _, err := o.Kube.RunKube(ctx, []byte(readsScript), nil)
	if err != nil {
		return ClusterState{}
	}
	state := ClusterState{HelmReleases: map[string]bool{}, Read: true}
	section := ""
	for _, line := range strings.Split(string(stdout), "\n") {
		line = strings.TrimSpace(line)
		switch line {
		case "---uid---", "---releases---":
			section = line
			continue
		case "":
			continue
		}
		switch section {
		case "---uid---":
			if state.IncarnationID == "" {
				state.IncarnationID = line
			}
		case "---releases---":
			state.HelmReleases[line] = true
		}
	}
	return state
}

// HelmReleaseName is the release `hmd deploy` installs for one BOM entry.
//
// Both the release and its namespace are named
// <repo_instance_name>-<deployment_id>, so the redis entry in the local
// environment becomes the redis-local release in the redis-local namespace.
func HelmReleaseName(repoInstanceName, deploymentID string) string {
	return repoInstanceName + "-" + deploymentID
}

// SortedRecords is the records in a stable order, for output and tests.
func SortedRecords(records []CoreDNSRecord) []CoreDNSRecord {
	out := append([]CoreDNSRecord(nil), records...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Host < out[j].Host })
	return out
}

// legacyTraefikReleaseScript looks for the Helm release the Python CLI's
// one-time migration removes.
//
// A Helm release is a Secret in its namespace labelled owner=helm,name=<release>,
// so this needs no helm binary -- which is the point: SPEC008 makes docker the
// only host tool, and helm is not one of the two that run inside the k3s node.
const legacyTraefikReleaseScript = `set -e
kubectl -n kube-system get secret -l owner=helm,name=traefik -o name 2>/dev/null | head -n1
`

// warnLegacyTraefikRelease names a cluster that predates the baked-in ingress.
//
// SPEC014 recorded two host-only helm paths dropped rather than ported, and
// said both must be "detected and named, not silently mishandled". This is the
// half that is still live. The k3s image bakes the rendered Traefik chart into
// k3s's auto-deploying manifests now, but a cluster or volume created before
// that may still carry the Helm release the Python installed at runtime, under
// the same name and namespace -- and the two collide.
//
// Named rather than removed: uninstalling a Helm release needs helm, which
// nsctl deliberately does not have, and doing it by deleting the release Secret
// would leave the release's own resources behind. A warning points at the one
// command that fixes it.
func (o *Operators) warnLegacyTraefikRelease(ctx context.Context) {
	stdout, _, err := o.Kube.RunKube(ctx, []byte(legacyTraefikReleaseScript), nil)
	if err != nil {
		// Unreadable is not the same as absent, and this is advisory: a cluster
		// that cannot answer has bigger problems, reported elsewhere.
		return
	}
	if strings.TrimSpace(string(stdout)) == "" {
		return
	}
	o.warn("this cluster still carries the runtime-installed Traefik Helm release from before " +
		"ingress was baked into the k3s image, which collides with the baked-in manifest. " +
		"nsctl cannot remove it -- uninstalling a Helm release needs helm, which nsctl does not " +
		"ship. Run `hmd neuronsphere up --env <name>` once, which does the migration, or " +
		"`helm uninstall traefik --namespace kube-system` against this cluster.")
}
