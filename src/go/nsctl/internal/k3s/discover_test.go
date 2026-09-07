package k3s

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// Applied as a separate service rather than by mutating the workload's own:
// Traefik's service is owned by a k3s Addon, which reverts out-of-band edits.
func TestNodePortManifest(t *testing.T) {
	t.Parallel()

	spec := TraefikNodePortSpec("traefik-local-nodeport", "kube-system", 31080)
	manifest, err := spec.Manifest()
	if err != nil {
		t.Fatal(err)
	}

	var svc map[string]any
	if err := json.Unmarshal(manifest, &svc); err != nil {
		t.Fatalf("the manifest is not valid JSON: %v", err)
	}
	if svc["kind"] != "Service" {
		t.Errorf("kind = %v", svc["kind"])
	}
	spec2 := svc["spec"].(map[string]any)
	if spec2["type"] != "NodePort" {
		t.Errorf("type = %v, want NodePort", spec2["type"])
	}
	port := spec2["ports"].([]any)[0].(map[string]any)
	if port["nodePort"] != float64(31080) {
		t.Errorf("nodePort = %v, want 31080", port["nodePort"])
	}
	if port["targetPort"] != "web" {
		t.Errorf("targetPort = %v, want the named port", port["targetPort"])
	}
	meta := svc["metadata"].(map[string]any)
	if meta["namespace"] != "kube-system" {
		t.Errorf("namespace = %v", meta["namespace"])
	}
}

func TestEnsureNodePortMountsTheManifest(t *testing.T) {
	t.Parallel()

	exec := &recordExec{}
	o := testOperators(exec, &fakeDocker{})

	spec := TraefikNodePortSpec("svc", "kube-system", 31080)
	if err := o.EnsureNodePort(context.Background(), spec); err != nil {
		t.Fatalf("EnsureNodePort: %v", err)
	}
	if !strings.Contains(exec.seen["nodeport.json"], "NodePort") {
		t.Errorf("the manifest was not copied in; saw %v", exec.seen)
	}
}

const trinoServices = `{"items":[
  {"metadata":{"name":"some-other","namespace":"x"},"spec":{"selector":{"a":"b"},"ports":[{"port":80}]}},
  {"metadata":{"name":"trino-local-hmd-inf-trino","namespace":"trino-local"},
   "spec":{"selector":{"app":"trino","component":"coordinator"},
           "ports":[{"port":8080,"targetPort":"http-coord"}]}}
]}`

func TestFindTrinoCoordinator(t *testing.T) {
	t.Parallel()

	o := testOperators(&recordExec{stdout: trinoServices}, &fakeDocker{})
	coord, ok := o.FindTrinoCoordinator(context.Background())
	if !ok {
		t.Fatal("the Trino coordinator was not found")
	}
	if coord.Namespace != "trino-local" {
		t.Errorf("namespace = %q", coord.Namespace)
	}
	if coord.Port != 8080 {
		t.Errorf("port = %d", coord.Port)
	}
	// Kubernetes accepts an integer or a named port, and coercing a name to a
	// number breaks the service.
	if coord.TargetPort != "http-coord" {
		t.Errorf("targetPort = %v, want the name preserved", coord.TargetPort)
	}
	if coord.Selector["component"] != "coordinator" {
		t.Errorf("selector = %v", coord.Selector)
	}
}

// Trino not being deployed is the normal case for an environment that has not
// been given it.
func TestFindTrinoCoordinatorReportsAbsence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		exec *recordExec
	}{
		{"no trino service", &recordExec{stdout: `{"items":[]}`}},
		{"unreadable cluster", &recordExec{err: errors.New("exit status 1")}},
		{"malformed output", &recordExec{stdout: "not json"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			o := testOperators(tt.exec, &fakeDocker{})
			if _, ok := o.FindTrinoCoordinator(context.Background()); ok {
				t.Error("a coordinator was reported where there is none")
			}
		})
	}
}

func TestDecodeTargetPortKeepsItsKind(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want any
	}{
		{"a named port", `"http-coord"`, "http-coord"},
		{"a numeric port", `8080`, 8080},
		{"absent", ``, "http-coord"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := decodeTargetPort(json.RawMessage(tt.raw)); got != tt.want {
				t.Errorf("decodeTargetPort(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

const ingressList = `{"items":[
  {"spec":{"rules":[{"host":"airflow.local.neuronsphere.io"},{"host":"airflow.local.neuronsphere.io"}]}},
  {"spec":{"rules":[{"host":"argo.local.neuronsphere.io"}]}},
  {"spec":{"rules":[{"host":""}]}}
]}`

func TestIngressHostsAreDeduplicated(t *testing.T) {
	t.Parallel()

	o := testOperators(&recordExec{stdout: ingressList}, &fakeDocker{})
	hosts := o.IngressHosts(context.Background())

	if len(hosts) != 2 {
		t.Fatalf("got %v, want two distinct hosts", hosts)
	}
	if hosts[0] != "airflow.local.neuronsphere.io" || hosts[1] != "argo.local.neuronsphere.io" {
		t.Errorf("hosts = %v", hosts)
	}
}

func TestIngressHostsOnAnUnreadableCluster(t *testing.T) {
	t.Parallel()

	o := testOperators(&recordExec{err: errors.New("exit status 1")}, &fakeDocker{})
	if got := o.IngressHosts(context.Background()); got != nil {
		t.Errorf("got %v, want none", got)
	}
}

// The shapes the cloud charts actually ship: trino spells its class in the
// legacy annotation and its path in ALB syntax, airflow uses the modern class
// field and a path Traefik already matches.
const albIngressList = `{"items":[
  {"metadata":{"name":"alb-ingress","namespace":"trino-local","annotations":{"kubernetes.io/ingress.class":"alb"}},
   "spec":{"rules":[{"host":"trino.local.neuronsphere.io","http":{"paths":[{"path":"/*"}]}}]}},
  {"metadata":{"name":"airflow-local-web","namespace":"airflow-local"},
   "spec":{"ingressClassName":"alb","rules":[{"host":"airflow.local.neuronsphere.io","http":{"paths":[{"path":""}]}}]}},
  {"metadata":{"name":"other","namespace":"elsewhere","annotations":{"kubernetes.io/ingress.class":"nginx"}},
   "spec":{"rules":[{"host":"other.example.com","http":{"paths":[{"path":"/*"}]}}]}}
]}`

// A UI that is up, routed and reachable on its NodePort still 404s through its
// own hostname while Traefik reads `/*` literally.
func TestALBWildcardPathsAreRewritten(t *testing.T) {
	t.Parallel()

	var list struct {
		Items []ingress `json:"items"`
	}
	if err := json.Unmarshal([]byte(albIngressList), &list); err != nil {
		t.Fatal(err)
	}
	patches := albPathPatches(list.Items, "alb")

	if len(patches) != 1 {
		t.Fatalf("got %+v, want only trino's wildcard rewritten", patches)
	}
	want := ingressPathPatch{Namespace: "trino-local", Name: "alb-ingress", NewPath: "/"}
	if patches[0] != want {
		t.Errorf("patch = %+v, want %+v", patches[0], want)
	}
}

func TestTraefikPathTranslatesALBPatterns(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in      string
		want    string
		rewrite bool
	}{
		{"/*", "/", true},
		{"/api/*", "/api", true},
		{"*", "/", true},
		// Already matchable, so left exactly as the chart wrote it.
		{"", "", false},
		{"/", "/", false},
		{"/api", "/api", false},
	}
	for _, tt := range tests {
		got, rewrite := traefikPath(tt.in)
		if got != tt.want || rewrite != tt.rewrite {
			t.Errorf("traefikPath(%q) = %q, %v; want %q, %v", tt.in, got, rewrite, tt.want, tt.rewrite)
		}
	}
}

func TestNormalizeIngressPathsPatchesTheCluster(t *testing.T) {
	t.Parallel()

	exec := &recordExec{stdout: albIngressList}
	o := testOperators(exec, &fakeDocker{})

	changed := o.NormalizeIngressPaths(context.Background())

	if len(changed) != 1 || changed[0] != "trino-local/alb-ingress" {
		t.Fatalf("changed = %v, want trino-local/alb-ingress", changed)
	}
	script := exec.seen[ScriptName]
	for _, want := range []string{
		"kubectl patch ingress -n trino-local alb-ingress",
		`"path":"/spec/rules/0/http/paths/0/path","value":"/"`,
		`"path":"/spec/rules/0/http/paths/0/pathType","value":"Prefix"`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("the patch script is missing %q:\n%s", want, script)
		}
	}
}

// Nothing to rewrite must cost no exec: the common case is a cluster whose
// paths are already matchable.
func TestNormalizeIngressPathsSkipsAnUnaffectedCluster(t *testing.T) {
	t.Parallel()

	exec := &recordExec{stdout: `{"items":[
	  {"metadata":{"name":"airflow-local-web","namespace":"airflow-local"},
	   "spec":{"ingressClassName":"alb","rules":[{"host":"airflow.local.neuronsphere.io","http":{"paths":[{"path":"/"}]}}]}}
	]}`}
	o := testOperators(exec, &fakeDocker{})

	if changed := o.NormalizeIngressPaths(context.Background()); changed != nil {
		t.Errorf("changed = %v, want none", changed)
	}
	if strings.Contains(exec.seen[ScriptName], "kubectl patch") {
		t.Error("a patch was issued against a cluster with nothing to rewrite")
	}
}

func TestNodePortAddress(t *testing.T) {
	t.Parallel()

	if got := NodePortAddress("172.18.0.5", 31880); got != "172.18.0.5:31880" {
		t.Errorf("NodePortAddress = %q", got)
	}
	// No IP means no route can be wired, and an address of ":31880" would be
	// silently wrong rather than absent.
	if got := NodePortAddress("", 31880); got != "" {
		t.Errorf("NodePortAddress with no IP = %q, want empty", got)
	}
}
