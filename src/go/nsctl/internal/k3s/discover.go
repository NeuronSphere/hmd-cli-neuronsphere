package k3s

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
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

// IngressHosts is every Ingress hostname declared in this environment's
// cluster.
func (o *Operators) IngressHosts(ctx context.Context) []string {
	stdout, _, err := o.Kube.RunKube(ctx, []byte(listIngressScript), nil)
	if err != nil {
		return nil
	}
	var list struct {
		Items []struct {
			Spec struct {
				Rules []struct {
					Host string `json:"host"`
				} `json:"rules"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal(stdout, &list); err != nil {
		return nil
	}
	var hosts []string
	seen := map[string]bool{}
	for _, item := range list.Items {
		for _, rule := range item.Spec.Rules {
			if rule.Host != "" && !seen[rule.Host] {
				seen[rule.Host] = true
				hosts = append(hosts, rule.Host)
			}
		}
	}
	return hosts
}

// NodePortAddress is <ip>:<port>, the address hmd_proxy streams to.
func NodePortAddress(ip string, port int) string {
	if ip == "" {
		return ""
	}
	return ip + ":" + strconv.Itoa(port)
}
