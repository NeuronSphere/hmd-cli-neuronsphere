package k3s

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/router"
)

// NodePortSpec is a NodePort service fronting an in-cluster workload.
//
// A NodePort is how anything on the Docker network reaches a ClusterIP inside
// k3s: hmd_proxy connects to floci-eks-<cluster>:<nodePort>. It is applied as a
// *separate* service rather than by mutating the workload's own, because
// Traefik's service is owned by a k3s Addon that reverts out-of-band edits.
type NodePortSpec struct {
	Name       string
	Namespace  string
	Selector   map[string]string
	Port       int
	TargetPort any
	NodePort   int
}

// Manifest renders the NodePort service.
func (s NodePortSpec) Manifest() ([]byte, error) {
	return json.Marshal(map[string]any{
		"apiVersion": "v1",
		"kind":       "Service",
		"metadata": map[string]any{
			"name":      s.Name,
			"namespace": s.Namespace,
			"labels":    map[string]any{"app.kubernetes.io/managed-by": "hmd-cli-neuronsphere"},
		},
		"spec": map[string]any{
			"type":     "NodePort",
			"selector": s.Selector,
			"ports": []any{map[string]any{
				"name": "http", "port": s.Port, "targetPort": s.TargetPort,
				"nodePort": s.NodePort, "protocol": "TCP",
			}},
		},
	})
}

const applyNodePortScript = `set -eu
kubectl apply -f "$STEP_DIR/nodeport.json"
`

// EnsureNodePort idempotently applies a NodePort service.
func (o *Operators) EnsureNodePort(ctx context.Context, spec NodePortSpec) error {
	manifest, err := spec.Manifest()
	if err != nil {
		return fmt.Errorf("rendering the NodePort service %s: %w", spec.Name, err)
	}
	stdout, stderr, err := o.Kube.RunKube(ctx, []byte(applyNodePortScript), map[string][]byte{"nodeport.json": manifest})
	if err != nil {
		return &StepError{Step: "applying the NodePort service " + spec.Name, Stdout: stdout, Stderr: stderr, Err: err}
	}
	return nil
}

// TraefikNodePortSpec fronts the ingress controller.
func TraefikNodePortSpec(name, namespace string, nodePort int) NodePortSpec {
	return NodePortSpec{
		Name: name, Namespace: namespace,
		Selector: map[string]string{
			"app.kubernetes.io/instance": "traefik-kube-system",
			"app.kubernetes.io/name":     "traefik",
		},
		Port: 80, TargetPort: "web", NodePort: nodePort,
	}
}

const listServicesScript = `set -eu
kubectl get svc -A -o json
`

// TrinoCoordinator is the deployed Trino coordinator's ClusterIP service.
type TrinoCoordinator struct {
	Namespace  string
	Selector   map[string]string
	Port       int
	TargetPort any
}

// FindTrinoCoordinator locates the Trino coordinator service, or reports false
// when Trino is not deployed -- which is the normal case for an environment
// that has not been given it.
func (o *Operators) FindTrinoCoordinator(ctx context.Context) (TrinoCoordinator, bool) {
	stdout, _, err := o.Kube.RunKube(ctx, []byte(listServicesScript), nil)
	if err != nil {
		return TrinoCoordinator{}, false
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Spec struct {
				Selector map[string]string `json:"selector"`
				Ports    []struct {
					Port       int             `json:"port"`
					TargetPort json.RawMessage `json:"targetPort"`
				} `json:"ports"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal(stdout, &list); err != nil {
		return TrinoCoordinator{}, false
	}
	for _, item := range list.Items {
		if !strings.HasSuffix(item.Metadata.Name, "hmd-inf-trino") {
			continue
		}
		if len(item.Spec.Selector) == 0 || len(item.Spec.Ports) == 0 {
			continue
		}
		p := item.Spec.Ports[0]
		port := p.Port
		if port == 0 {
			port = 8080
		}
		return TrinoCoordinator{
			Namespace:  item.Metadata.Namespace,
			Selector:   item.Spec.Selector,
			Port:       port,
			TargetPort: decodeTargetPort(p.TargetPort),
		}, true
	}
	return TrinoCoordinator{}, false
}

// decodeTargetPort keeps a targetPort's original kind: Kubernetes accepts an
// integer or a named port, and coercing a name to a number breaks the service.
func decodeTargetPort(raw json.RawMessage) any {
	if len(raw) == 0 {
		return "http-coord"
	}
	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		return n
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil && s != "" {
		return s
	}
	return "http-coord"
}

const listIngressScript = `set -eu
kubectl get ingress -A -o json
`

// ingress is the part of an Ingress the local ALB emulation reads.
type ingress struct {
	Metadata struct {
		Name        string            `json:"name"`
		Namespace   string            `json:"namespace"`
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
	Spec struct {
		IngressClassName string `json:"ingressClassName"`
		Rules            []struct {
			Host string `json:"host"`
			HTTP struct {
				Paths []struct {
					Path string `json:"path"`
				} `json:"paths"`
			} `json:"http"`
		} `json:"rules"`
	} `json:"spec"`
}

// class is the ingress class this Ingress asks for.
//
// Charts spell it either way -- airflow sets spec.ingressClassName, argo and
// trino the legacy annotation -- and both are served, so both are read.
func (i ingress) class() string {
	if i.Spec.IngressClassName != "" {
		return i.Spec.IngressClassName
	}
	return i.Metadata.Annotations["kubernetes.io/ingress.class"]
}

func (o *Operators) listIngresses(ctx context.Context) []ingress {
	stdout, _, err := o.Kube.RunKube(ctx, []byte(listIngressScript), nil)
	if err != nil {
		return nil
	}
	var list struct {
		Items []ingress `json:"items"`
	}
	if err := json.Unmarshal(stdout, &list); err != nil {
		return nil
	}
	return list.Items
}

// IngressHosts is every Ingress hostname declared in this environment's
// cluster.
func (o *Operators) IngressHosts(ctx context.Context) []string {
	var hosts []string
	seen := map[string]bool{}
	for _, item := range o.listIngresses(ctx) {
		for _, rule := range item.Spec.Rules {
			if rule.Host != "" && !seen[rule.Host] {
				seen[rule.Host] = true
				hosts = append(hosts, rule.Host)
			}
		}
	}
	return hosts
}

// ingressPathPatch is one Ingress path rewritten out of ALB syntax.
type ingressPathPatch struct {
	Namespace string
	Name      string
	Rule      int
	Path      int
	NewPath   string
}

// traefikPath translates one ALB path pattern into the prefix Traefik matches,
// reporting whether a rewrite is needed at all.
func traefikPath(p string) (string, bool) {
	if !strings.HasSuffix(p, "*") {
		return p, false
	}
	prefix := strings.TrimSuffix(strings.TrimSuffix(p, "*"), "/")
	if prefix == "" {
		return "/", true
	}
	return prefix, true
}

// albPathPatches is every wildcard path to rewrite, for the Ingresses this
// environment's class actually serves.
func albPathPatches(items []ingress, class string) []ingressPathPatch {
	var patches []ingressPathPatch
	for _, item := range items {
		if item.class() != class {
			continue
		}
		for r, rule := range item.Spec.Rules {
			for p, path := range rule.HTTP.Paths {
				next, ok := traefikPath(path.Path)
				if !ok {
					continue
				}
				patches = append(patches, ingressPathPatch{
					Namespace: item.Metadata.Namespace, Name: item.Metadata.Name,
					Rule: r, Path: p, NewPath: next,
				})
			}
		}
	}
	return patches
}

// patchIngressPathsScript batches every rewrite into one exec, the same way the
// other Kubernetes steps do.
//
// pathType moves to Prefix alongside the path: ALB charts leave it
// ImplementationSpecific, which is the controller's choice to interpret, and
// naming the semantics is what keeps the rewritten path matching a subtree.
func patchIngressPathsScript(patches []ingressPathPatch) string {
	var b strings.Builder
	b.WriteString("set -eu\n")
	for _, p := range patches {
		op := fmt.Sprintf(
			`[{"op":"replace","path":"/spec/rules/%d/http/paths/%d/path","value":"%s"},`+
				`{"op":"replace","path":"/spec/rules/%d/http/paths/%d/pathType","value":"Prefix"}]`,
			p.Rule, p.Path, p.NewPath, p.Rule, p.Path,
		)
		fmt.Fprintf(&b, "kubectl patch ingress -n %s %s --type=json -p '%s'\n", p.Namespace, p.Name, op)
	}
	return b.String()
}

// ingressHostPatch is one Ingress rule's host rewritten to name its environment.
type ingressHostPatch struct {
	Namespace string
	Name      string
	Rule      int
	NewHost   string
}

// ingressHostPatches is every Ingress host to rewrite, for the Ingresses this
// cluster's ingress class owns.
//
// hmd-cli-helm renders alb.hostname with the literal "local" in *every*
// environment, through --set, which beats any values file. Two environments
// deploying the same chart therefore ask for one hostname between them, and
// before this only the default environment's vhost was ever written -- so a
// second environment's user interfaces were unreachable by name at all.
//
// Rewritten on the deployed object rather than in hmd-cli-helm, which is the
// same move as pointing the alb IngressClass at Traefik and as absorbing the ALB
// path dialect: it is what lets a cloud chart deploy unmodified. Idempotent -- a
// host already in the target shape yields no patch, so a start does not rewrite
// what the last one rewrote (NERD025 SPEC005).
func ingressHostPatches(items []ingress, class, slug string) []ingressHostPatch {
	var patches []ingressHostPatch
	for _, item := range items {
		if item.class() != class {
			continue
		}
		for r, rule := range item.Spec.Rules {
			want := rewrittenHost(rule.Host, slug)
			if want == "" || want == rule.Host {
				continue
			}
			patches = append(patches, ingressHostPatch{
				Namespace: item.Metadata.Namespace, Name: item.Metadata.Name,
				Rule: r, NewHost: want,
			})
		}
	}
	return patches
}

// rewrittenHost is the host an instance's UI should answer on, or "" when this
// is not a host this platform owns.
//
// The instance is the leftmost label of whatever the chart rendered; everything
// after it is replaced, so a host left over from another environment is
// corrected rather than compounded.
func rewrittenHost(host, slug string) string {
	if host == "" {
		return ""
	}
	instance, rest, found := strings.Cut(host, ".")
	if !found || instance == "" {
		return ""
	}
	// Only the two suffixes this platform owns: the one hmd-cli-helm renders,
	// and the one it is rewritten to. Anything else is somebody else's name.
	if !strings.HasSuffix(host, router.HelmIngressDomain) && !strings.HasSuffix(rest, router.IngressDomain) {
		return ""
	}
	return router.IngressHostFor(instance, slug)
}

// NormalizeIngressHosts rewrites every Ingress host to name its environment,
// returning the Ingresses it changed as namespace/name.
func (o *Operators) NormalizeIngressHosts(ctx context.Context, slug string) []string {
	if !o.IngressEnabled {
		return nil
	}
	patches := ingressHostPatches(o.listIngresses(ctx), o.ingressClass(), slug)
	if len(patches) == 0 {
		return nil
	}
	stdout, stderr, err := o.Kube.RunKube(ctx, []byte(patchIngressHostsScript(patches)), nil)
	if err != nil {
		o.warn("%v", &StepError{
			Step:   "rewriting Ingress hosts to name the environment; the UIs they front will be unreachable by name",
			Stdout: stdout, Stderr: stderr, Err: err,
		})
		return nil
	}
	var changed []string
	seen := map[string]bool{}
	for _, p := range patches {
		full := p.Namespace + "/" + p.Name
		if !seen[full] {
			seen[full] = true
			changed = append(changed, full)
		}
	}
	return changed
}

// patchIngressHostsScript batches every rewrite into one exec, as the path
// rewrite does.
func patchIngressHostsScript(patches []ingressHostPatch) string {
	var b strings.Builder
	b.WriteString("set -eu\n")
	for _, p := range patches {
		op := fmt.Sprintf(`[{"op":"replace","path":"/spec/rules/%d/host","value":"%s"}]`, p.Rule, p.NewHost)
		fmt.Fprintf(&b, "kubectl patch ingress -n %s %s --type=json -p '%s'\n", p.Namespace, p.Name, op)
	}
	return b.String()
}

// NormalizeIngressPaths rewrites the ALB path patterns Traefik cannot match,
// returning the Ingresses it changed as namespace/name.
//
// ALB Ingresses spell "everything under this host" as `path: /*` -- valid ALB
// syntax, and what the cloud charts ship (hmd-inf-trino does). Traefik reads it
// literally, as PathPrefix(`/*`), so every request 404s: the UI is running,
// routed and reachable on its NodePort, yet dead through its own hostname.
// Absorbing the ALB dialect here is the same move as pointing the alb
// IngressClass at Traefik -- it is what lets cloud charts deploy unmodified,
// and the alternative, editing charts to say `/`, would break the real ALB.
//
// Unlike the Traefik Deployment, these Ingresses belong to Helm releases rather
// than a k3s Addon, so nothing reverts the patch. A chart redeploy reintroduces
// the ALB syntax, and the next start normalizes it again.
//
// Idempotent: a rewritten path has no wildcard left to match.
func (o *Operators) NormalizeIngressPaths(ctx context.Context) []string {
	if !o.IngressEnabled {
		return nil
	}
	patches := albPathPatches(o.listIngresses(ctx), o.ingressClass())
	if len(patches) == 0 {
		return nil
	}
	stdout, stderr, err := o.Kube.RunKube(ctx, []byte(patchIngressPathsScript(patches)), nil)
	if err != nil {
		o.warn("%v", &StepError{
			Step:   "rewriting ALB wildcard Ingress paths; the UIs they front will 404 through their hostnames",
			Stdout: stdout, Stderr: stderr, Err: err,
		})
		return nil
	}
	var changed []string
	seen := map[string]bool{}
	for _, p := range patches {
		full := p.Namespace + "/" + p.Name
		if !seen[full] {
			seen[full] = true
			changed = append(changed, full)
		}
	}
	return changed
}

// NodePortAddress is <ip>:<port>, the address hmd_proxy streams to.
func NodePortAddress(ip string, port int) string {
	if ip == "" {
		return ""
	}
	return ip + ":" + strconv.Itoa(port)
}
