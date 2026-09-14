package k3s

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// fakeDocker answers the three host-side things the operators need.
type fakeDocker struct {
	ips   map[string]string
	execs [][]string
	// execErr fails the exec whose joined args contain this substring.
	execErr  string
	grepMiss bool
}

func (f *fakeDocker) ContainerIP(_ context.Context, name, _ string) string {
	return f.ips[name]
}

func (f *fakeDocker) Exec(_ context.Context, name string, args ...string) ([]byte, error) {
	f.execs = append(f.execs, append([]string{name}, args...))
	joined := strings.Join(args, " ")
	if f.execErr != "" && strings.Contains(joined, f.execErr) {
		return nil, errors.New("exit status 1")
	}
	if args[0] == "grep" {
		if f.grepMiss {
			return nil, errors.New("exit status 1")
		}
		return nil, nil
	}
	return nil, nil
}

func (f *fakeDocker) Run(context.Context, ...string) ([]byte, []byte, error) { return nil, nil, nil }

func testOperators(exec *recordExec, d *fakeDocker) *Operators {
	return &Operators{
		Kube:   &Kube{Container: "floci-eks-1.ns-local-abc", Run: exec.run},
		Docker: d,
		Env: Environment{
			Slug: "local", DBContainer: "hmd_db-local", GraphContainer: "global-graph-local",
			CoreInstanceName: "local-neuronsphere",
			K3sCluster:       "ns-local-abc", K3sContainer: "floci-eks-1.ns-local-abc",
		},
		Network: "net", IngressEnabled: true,
		Out: io.Discard, Err: io.Discard,
	}
}

// A chart's unmodified AWS_ENDPOINT_URL, JDBC URL and Gremlin endpoint all have
// to resolve to this environment's own containers.
func TestCoreDNSRecordsMapTheCanonicalNames(t *testing.T) {
	t.Parallel()

	d := &fakeDocker{ips: map[string]string{
		"floci":                   "172.20.0.2",
		"hmd_proxy":               "172.20.0.3",
		"floci-rds-db-ABC":        "172.20.0.4",
		"floci-neptune-graph-ABC": "172.20.0.5",
	}}
	o := testOperators(&recordExec{}, d)

	records := o.CoreDNSRecords(context.Background(), "floci-rds-db-ABC", "floci-neptune-graph-ABC")
	got := map[string]string{}
	for _, r := range records {
		got[r.Host] = r.IP
	}

	want := map[string]string{
		"neuronsphere":          "172.20.0.2",
		"neuronsphere-workload": "172.20.0.2",
		"neuronsphere-control":  "172.20.0.2",
		"hmd_proxy":             "172.20.0.3",
		// The database under both names: hmd_db is what unmodified cloud charts
		// use, hmd_db-<slug> is what the connection secrets carry.
		"hmd_db":             "172.20.0.4",
		"hmd_db-local":       "172.20.0.4",
		"global-graph":       "172.20.0.5",
		"global-graph-local": "172.20.0.5",
	}
	for host, ip := range want {
		if got[host] != ip {
			t.Errorf("%s -> %q, want %q", host, got[host], ip)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d records, want %d: %v", len(got), len(want), got)
	}
}

// Absent is normal for the graph: it is provisioned only when something in the
// BOM asks for one.
func TestCoreDNSRecordsOmitAnAbsentGraph(t *testing.T) {
	t.Parallel()

	d := &fakeDocker{ips: map[string]string{"floci": "172.20.0.2", "floci-rds-db-ABC": "172.20.0.4"}}
	o := testOperators(&recordExec{}, d)

	for _, r := range o.CoreDNSRecords(context.Background(), "floci-rds-db-ABC", "") {
		if strings.Contains(r.Host, "graph") {
			t.Errorf("a graph record was emitted with no graph container: %+v", r)
		}
	}
}

// No Floci means no records at all -- every canonical name is relative to it.
func TestCoreDNSRecordsAreSkippedWithoutFloci(t *testing.T) {
	t.Parallel()

	o := testOperators(&recordExec{}, &fakeDocker{ips: map[string]string{}})
	if got := o.CoreDNSRecords(context.Background(), "db", "graph"); got != nil {
		t.Errorf("got %v, want none", got)
	}
}

// Separate server blocks, not a second hosts plugin inside .:53 -- that crashes
// CoreDNS.
func TestCoreDNSConfigMapUsesPerHostServerBlocks(t *testing.T) {
	t.Parallel()

	manifest, err := CoreDNSConfigMap([]CoreDNSRecord{
		{Host: "neuronsphere", IP: "172.20.0.2"},
		{Host: "hmd_db", IP: "172.20.0.4"},
	})
	if err != nil {
		t.Fatal(err)
	}

	var cm map[string]any
	if err := yaml.Unmarshal(manifest, &cm); err != nil {
		t.Fatalf("the ConfigMap is not valid YAML: %v", err)
	}
	meta := cm["metadata"].(map[string]any)
	if meta["name"] != "coredns-custom" || meta["namespace"] != "kube-system" {
		t.Errorf("metadata = %v, want the k3s extension point", meta)
	}

	server := cm["data"].(map[string]any)["neuronsphere.server"].(string)
	for _, want := range []string{
		"neuronsphere:53 {", "172.20.0.2 neuronsphere", "hmd_db:53 {", "172.20.0.4 hmd_db", "fallthrough",
	} {
		if !strings.Contains(server, want) {
			t.Errorf("the server config does not contain %q:\n%s", want, server)
		}
	}
	if strings.Contains(server, ".:53") {
		t.Errorf("a catch-all .:53 block would crash CoreDNS:\n%s", server)
	}
}

// The ConfigMap arrives as a mounted file: `apply -f -` has no equivalent for a
// script running inside a container.
func TestEnsureCoreDNSRecordsMountsTheManifest(t *testing.T) {
	t.Parallel()

	exec := &recordExec{}
	o := testOperators(exec, &fakeDocker{ips: map[string]string{"floci": "172.20.0.2", "db": "172.20.0.4"}})

	if err := o.EnsureCoreDNSRecordsFor(context.Background(), "db", ""); err != nil {
		t.Fatalf("EnsureCoreDNSRecordsFor: %v", err)
	}
	if !strings.Contains(exec.seen["coredns.yaml"], "coredns-custom") {
		t.Errorf("the ConfigMap was not mounted; saw %v", exec.seen)
	}
	script := exec.seen[ScriptName]
	for _, want := range []string{"apply -f \"$STEP_DIR/coredns.yaml\"", "rollout restart deploy coredns", "rollout status deploy coredns"} {
		if !strings.Contains(script, want) {
			t.Errorf("the script does not %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, "apply -f -") {
		t.Errorf("the script still feeds a manifest on stdin:\n%s", script)
	}
}

// Node preparation is one container: the wait is a loop inside it rather than
// repeated launches.
func TestPrepareNodeIsASingleContainerWithAnInternalWait(t *testing.T) {
	t.Parallel()

	exec := &recordExec{stdout: "k3s-node Ready\n"}
	o := testOperators(exec, &fakeDocker{})

	if err := o.PrepareNode(context.Background()); err != nil {
		t.Fatalf("PrepareNode: %v", err)
	}
	script := exec.seen[ScriptName]
	for _, want := range []string{
		"while [", "sleep 4", // the wait loop
		"kubectl delete node", // stale registrations
		"kubectl delete pv",   // orphaned node-affine PVs
		"topology.kubernetes.io/zone=local",
		"hmdlabs.io/repo-instance-name=local-neuronsphere",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("the script does not contain %q:\n%s", want, script)
		}
	}
}

func TestPrepareNodeFailsWhenNoNodeIsReady(t *testing.T) {
	t.Parallel()

	exec := &recordExec{stdout: "k3s-node NotReady\n"}
	o := testOperators(exec, &fakeDocker{})
	if err := o.PrepareNode(context.Background()); err == nil {
		t.Error("PrepareNode reported success with no Ready node")
	}
}

// Both edits go to the file, because the Deployment is owned by a k3s Addon
// that reverts any live kubectl patch.
func TestPatchTraefikManifestEditsTheFile(t *testing.T) {
	t.Parallel()

	d := &fakeDocker{grepMiss: true}
	o := testOperators(&recordExec{}, d)

	if err := o.PatchTraefikManifest(context.Background()); err != nil {
		t.Fatalf("PatchTraefikManifest: %v", err)
	}

	var strippedPorts, setClass bool
	for _, call := range d.execs {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, "hostPort") && strings.Contains(joined, "sed") {
			strippedPorts = true
		}
		if strings.Contains(joined, "ingressclass=alb") && strings.Contains(joined, "sed") {
			setClass = true
		}
		if strings.Contains(joined, "kubectl patch") {
			t.Errorf("a live patch was used; the Addon controller would revert it: %s", joined)
		}
	}
	if !strippedPorts {
		t.Error("the host ports were not stripped; Traefik stays Pending if any LoadBalancer holds 80 or 443")
	}
	if !setClass {
		t.Error("the ingress-class arg was not added; Ingresses declaring alb would not be served")
	}
	// Every edit targets the manifest inside the k3s container.
	for _, call := range d.execs {
		if call[0] != "floci-eks-1.ns-local-abc" {
			t.Errorf("an edit ran against %q, not the k3s container", call[0])
		}
	}
}

// Idempotent: a second run finds the arg already there and changes nothing.
func TestPatchTraefikManifestSkipsAnAlreadySetClass(t *testing.T) {
	t.Parallel()

	d := &fakeDocker{grepMiss: false}
	o := testOperators(&recordExec{}, d)
	if err := o.PatchTraefikManifest(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, call := range d.execs {
		if strings.Contains(strings.Join(call, " "), "ingressclass=alb") && call[1] == "sed" {
			t.Error("the arg was re-added when it was already present")
		}
	}
}

// The arg makes Traefik serve these Ingresses; the object makes
// spec.ingressClassName: alb a live reference rather than a dangling one.
func TestIngressClassManifest(t *testing.T) {
	t.Parallel()

	manifest, err := IngressClassManifest("alb")
	if err != nil {
		t.Fatal(err)
	}
	body := string(manifest)
	for _, want := range []string{`"kind":"IngressClass"`, `"name":"alb"`, TraefikController} {
		if !strings.Contains(body, want) {
			t.Errorf("the manifest does not contain %q:\n%s", want, body)
		}
	}
	// k3s already ships a default traefik IngressClass, and two defaults make
	// the API server reject class-less Ingresses as ambiguous.
	if strings.Contains(body, "is-default-class") {
		t.Errorf("the class was marked default:\n%s", body)
	}
}

func TestEnsureIngressControllerWaitsThenRegistersTheClass(t *testing.T) {
	t.Parallel()

	exec := &recordExec{stdout: "traefik-ready\n"}
	o := testOperators(exec, &fakeDocker{grepMiss: true})

	if err := o.EnsureIngressController(context.Background()); err != nil {
		t.Fatalf("EnsureIngressController: %v", err)
	}
	script := exec.seen[ScriptName]
	if !strings.Contains(script, "rollout status deploy/traefik") {
		t.Errorf("the script does not wait for Traefik:\n%s", script)
	}
	if !strings.Contains(exec.seen["ingressclass.json"], "IngressClass") {
		t.Errorf("the IngressClass was not mounted; saw %v", exec.seen)
	}
}

// A Traefik that never schedules leaves every Ingress-exposed UI silently
// unreachable, so this has to be an error rather than a debug line.
func TestEnsureIngressControllerReportsAFailedWait(t *testing.T) {
	t.Parallel()

	exec := &recordExec{stdout: "traefik-not-ready\n", err: errors.New("exit status 1")}
	o := testOperators(exec, &fakeDocker{grepMiss: true})

	err := o.EnsureIngressController(context.Background())
	if err == nil {
		t.Fatal("a Traefik that never became ready was reported as success")
	}
	if !strings.Contains(err.Error(), "unreachable") {
		t.Errorf("the error does not say what breaks: %v", err)
	}
}

func TestEnsureIngressControllerRemovesTraefikWhenDisabled(t *testing.T) {
	t.Parallel()

	exec := &recordExec{}
	o := testOperators(exec, &fakeDocker{})
	o.IngressEnabled = false

	if err := o.EnsureIngressController(context.Background()); err != nil {
		t.Fatalf("EnsureIngressController: %v", err)
	}
	script := exec.seen[ScriptName]
	if !strings.Contains(script, "delete") || !strings.Contains(script, traefikLabelSelector) {
		t.Errorf("Traefik was not removed:\n%s", script)
	}
}

// The kube-system Namespace UID distinguishes "the cluster was deleted and
// recreated" from "the container restarted"; the Helm releases come from
// label-selected Secrets, which needs no helm binary anywhere.
func TestReadClusterState(t *testing.T) {
	t.Parallel()

	exec := &recordExec{stdout: "---uid---\na3db9972-f83c-4f0a-b147-8f7e73422eec\n---releases---\nredis-local\ntrino-local\n"}
	o := testOperators(exec, &fakeDocker{})

	state := o.ReadClusterState(context.Background())
	if !state.Read {
		t.Fatal("the cluster was reported unreadable")
	}
	if state.IncarnationID != "a3db9972-f83c-4f0a-b147-8f7e73422eec" {
		t.Errorf("incarnation = %q", state.IncarnationID)
	}
	if !state.HelmReleases["redis-local"] || !state.HelmReleases["trino-local"] {
		t.Errorf("releases = %v", state.HelmReleases)
	}
	if len(state.HelmReleases) != 2 {
		t.Errorf("releases = %v, want exactly the two", state.HelmReleases)
	}
}

// "Could not read the cluster" must stay distinct from "the cluster has no
// releases", or a caller proposes a wholesale redeploy on a transient failure.
func TestReadClusterStateReportsAnUnreadableCluster(t *testing.T) {
	t.Parallel()

	o := testOperators(&recordExec{err: errors.New("exit status 1")}, &fakeDocker{})
	state := o.ReadClusterState(context.Background())
	if state.Read {
		t.Error("an unreachable cluster was reported as read")
	}
	if len(state.HelmReleases) != 0 {
		t.Errorf("releases = %v, want none", state.HelmReleases)
	}
}

func TestReadClusterStateDistinguishesAnEmptyClusterFromAnUnreadableOne(t *testing.T) {
	t.Parallel()

	o := testOperators(&recordExec{stdout: "---uid---\nuid\n---releases---\n"}, &fakeDocker{})
	state := o.ReadClusterState(context.Background())
	if !state.Read {
		t.Error("a reachable cluster with no releases was reported unreadable")
	}
	if len(state.HelmReleases) != 0 {
		t.Errorf("releases = %v, want none", state.HelmReleases)
	}
}

func TestHelmReleaseName(t *testing.T) {
	t.Parallel()

	if got := HelmReleaseName("redis", "local"); got != "redis-local" {
		t.Errorf("HelmReleaseName = %q, want redis-local", got)
	}
}

// SPEC014 said the two dropped host-only helm paths must be "detected and
// named, not silently mishandled". This is the one that is still live: a
// cluster or volume created before ingress was baked into the k3s image carries
// the Helm release the Python installed at runtime, under the same name and
// namespace as the baked-in manifest, and the two collide. Its owner has no way
// to know.
func TestALegacyTraefikReleaseIsNamed(t *testing.T) {
	t.Parallel()

	var warnings strings.Builder
	ops := &Operators{
		Kube: &Kube{Cluster: "k3s-dev", Container: "floci-eks-dev",
			Run: func(_ context.Context, args ...string) ([]byte, []byte, error) {
				// The release Secret, which is how a Helm release is stored --
				// so this needs no helm binary, and nsctl ships none.
				if len(args) > 0 && args[0] == "exec" {
					return []byte("secret/sh.helm.release.v1.traefik.v1\n"), nil, nil
				}
				return nil, nil, nil
			}},
		Err: &warnings,
	}
	ops.warnLegacyTraefikRelease(context.Background())

	got := warnings.String()
	if !strings.Contains(got, "helm uninstall traefik") || !strings.Contains(got, "hmd neuronsphere up") {
		t.Errorf("the warning does not name a fix: %q", got)
	}
}

// Nothing found and nothing readable are both silence: this is advisory, and a
// cluster that cannot answer has larger problems already reported elsewhere.
func TestNoLegacyReleaseMeansNoWarning(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name   string
		stdout []byte
		err    error
	}{
		{"a migrated cluster", []byte("\n"), nil},
		{"an unreadable one", nil, errors.New("exec failed")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var warnings strings.Builder
			ops := &Operators{
				Kube: &Kube{Container: "c", Run: func(context.Context, ...string) ([]byte, []byte, error) {
					return tt.stdout, nil, tt.err
				}},
				Err: &warnings,
			}
			ops.warnLegacyTraefikRelease(context.Background())
			if warnings.Len() != 0 {
				t.Errorf("warned anyway: %q", warnings.String())
			}
		})
	}
}

// The CoreDNS step has to survive a cluster that does not have CoreDNS yet.
//
// k3s creates it from an addon manifest after the node goes Ready, so on a
// cluster a deploy has just created this script can arrive first: `apply`
// succeeds, `rollout restart` fails with `deployments.apps "coredns" not
// found`, and `set -e` takes the step down. A cold start hit exactly that.
func TestTheCoreDNSScriptWaitsForTheDeploymentToExist(t *testing.T) {
	t.Parallel()

	if !strings.Contains(coreDNSScript, "get deploy coredns") {
		t.Error("the script does not wait for the CoreDNS deployment to exist")
	}
	// The wait has to come before the restart, or it is not a wait.
	wait := strings.Index(coreDNSScript, "get deploy coredns")
	restart := strings.Index(coreDNSScript, "rollout restart")
	if wait < 0 || restart < 0 || wait > restart {
		t.Errorf("the wait does not precede the restart:\n%s", coreDNSScript)
	}
	// And it must not be the thing that fails: the probe is the loop's
	// condition, not a command `set -e` can abort on.
	if !strings.Contains(coreDNSScript, "get deploy coredns >/dev/null 2>&1 && break") {
		t.Errorf("the existence probe is not guarded against set -e:\n%s", coreDNSScript)
	}
}
