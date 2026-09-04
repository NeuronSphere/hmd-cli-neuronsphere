// Package k3s provisions an environment's k3s cluster.
//
// Every Kubernetes call runs inside the k3s node container itself, through
// `docker exec`. SPEC008 proposed batching them into hmd-img-projectbuilder
// instead, on the premise that it is the image that already runs every Helm and
// CDKTF deploy and is therefore version-matched to the cluster. The premise is
// half right and the conclusion does not follow: projectbuilder ships helm but
// **no kubectl at all** -- verified against :stable, :0.5, :0.5.383 and
// :localdev, none of which have it.
//
// The k3s container is the better target anyway, and not only because it works:
//
//   - It ships kubectl at /bin/kubectl, matched to the cluster by construction
//     rather than by a pin anybody has to maintain.
//   - It reads /etc/rancher/k3s/k3s.yaml natively, so there is no kubeconfig to
//     rewrite for in-container use and no credentials to mount.
//   - It is `docker exec` into a container that is already running -- a cluster
//     that is down has no Kubernetes work to do -- rather than `docker run` of a
//     fresh one, so a full environment start launches no containers at all for
//     this instead of the twenty-nine the host-kubectl version issues.
//   - It removes projectbuilder as a prerequisite of *cluster provisioning*,
//     which SPEC008 accepted as a real cost and listed as a MEDIUM risk. It
//     stays a prerequisite of deploys, which is where it always was.
//
// It is also what the Python already does for the one Kubernetes call it makes
// this way: `_ensure_ingress_controller` re-applies the Traefik manifest with
// `docker exec <k3s container> kubectl apply -f`.
package k3s

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// WorkDir is where a step's script and its manifests are copied inside the k3s
// container.
const WorkDir = "/tmp/nsctl-kube"

// ScriptName is the entry script within a step's directory.
const ScriptName = "step.sh"

// Exec runs a command, returning stdout and stderr separately.
type Exec func(ctx context.Context, args ...string) (stdout, stderr []byte, err error)

// Kube runs Kubernetes work against one environment's cluster.
type Kube struct {
	// Cluster is the k3s cluster name.
	Cluster string
	// Container is the Floci-spawned k3s container. Every call execs into it.
	Container string
	// Kubeconfig is the host-side path write_kubeconfig produced. RunKube does
	// not need it -- the node has its own -- but the deploy path does, through
	// KubeconfigForContainer.
	Kubeconfig string
	// Run executes a docker subcommand.
	Run Exec
}

// RunKube runs a script inside the k3s node container.
//
// The script is copied in and executed by path, never passed as an argument --
// the same rule the deploy node follows for /tmp/hmd-deploy-node.sh, and for the
// same reason: a step with much inlined configuration exceeds ARG_MAX and fails
// before it runs with "argument list too long". Copying a whole directory also
// gives `kubectl apply -f` real files to read, which is what replaces the two
// sites that feed JSON on stdin.
//
// Steps are batched by the caller, so provisioning an environment is a handful
// of execs rather than one per kubectl invocation.
func (k *Kube) RunKube(ctx context.Context, script []byte, files map[string][]byte) (stdout, stderr []byte, err error) {
	if k.Container == "" {
		return nil, nil, fmt.Errorf("no k3s container to run against")
	}

	local, err := os.MkdirTemp("", "nsctl-kube-")
	if err != nil {
		return nil, nil, fmt.Errorf("creating a working directory: %w", err)
	}
	defer os.RemoveAll(local)

	for name, body := range files {
		if strings.Contains(name, "/") || name == ScriptName {
			return nil, nil, fmt.Errorf("invalid mounted file name %q", name)
		}
		if err := os.WriteFile(filepath.Join(local, name), body, 0o644); err != nil {
			return nil, nil, fmt.Errorf("writing %s: %w", name, err)
		}
	}
	if err := os.WriteFile(filepath.Join(local, ScriptName), script, 0o755); err != nil {
		return nil, nil, fmt.Errorf("writing the step script: %w", err)
	}

	// A content-addressed directory, so a retried step reuses its own and two
	// steps in flight cannot collide. Deterministic rather than random because
	// nothing here needs to be unpredictable.
	sum := sha256.Sum256(script)
	remote := WorkDir + "-" + hex.EncodeToString(sum[:])[:12]

	if _, errOut, err := k.Run(ctx, "exec", k.Container, "rm", "-rf", remote); err != nil {
		return nil, errOut, fmt.Errorf("clearing %s in %s: %w", remote, k.Container, err)
	}
	if _, errOut, err := k.Run(ctx, "exec", k.Container, "mkdir", "-p", remote); err != nil {
		return nil, errOut, fmt.Errorf("creating %s in %s: %w", remote, k.Container, err)
	}
	if _, errOut, err := k.Run(ctx, "cp", local+"/.", k.Container+":"+remote); err != nil {
		return nil, errOut, fmt.Errorf("copying the step into %s: %w", k.Container, err)
	}
	defer func() {
		// Best effort: a leftover directory in an ephemeral node container is
		// not worth failing a step over.
		_, _, _ = k.Run(context.WithoutCancel(ctx), "exec", k.Container, "rm", "-rf", remote)
	}()

	return k.Run(ctx, "exec", "-e", "STEP_DIR="+remote, k.Container, "sh", remote+"/"+ScriptName)
}

// KubeconfigForContainer rewrites the kubeconfig for use from inside a
// container on the NeuronSphere network.
//
// RunKube does not need this -- the k3s node has its own credentials -- but the
// deploy path does: a projectbuilder container running `hmd helm --local` gets
// the host kubeconfig mounted, and its server points at a host-published port
// that is unreachable from inside a container. The server is repointed at the
// in-network k3s container, and TLS verification goes with it because the
// certificate is issued for the host-facing name.
//
// The host-reachable kubeconfig is left exactly as it is: it is the user's own.
func (k *Kube) KubeconfigForContainer() (path string, cleanup func(), err error) {
	noop := func() {}
	if k.Kubeconfig == "" || k.Container == "" {
		return k.Kubeconfig, noop, nil
	}

	raw, err := os.ReadFile(k.Kubeconfig)
	if err != nil {
		// Fall back to the raw path rather than failing: a missing kubeconfig
		// is reported by the step that needs it, with the command that failed.
		return k.Kubeconfig, noop, nil
	}

	var cfg map[string]any
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return k.Kubeconfig, noop, nil
	}
	clusters, ok := cfg["clusters"].([]any)
	if !ok {
		return k.Kubeconfig, noop, nil
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
		cluster["server"] = "https://" + k.Container + ":6443"
		cluster["insecure-skip-tls-verify"] = true
		delete(cluster, "certificate-authority-data")
		delete(cluster, "certificate-authority")
	}

	rewritten, err := yaml.Marshal(cfg)
	if err != nil {
		return k.Kubeconfig, noop, nil
	}
	f, err := os.CreateTemp("", "nsctl-kubeconfig-*.yaml")
	if err != nil {
		return k.Kubeconfig, noop, nil
	}
	name := f.Name()
	if _, err := f.Write(rewritten); err != nil {
		f.Close()
		os.Remove(name)
		return k.Kubeconfig, noop, nil
	}
	f.Close()
	return name, func() { os.Remove(name) }, nil
}

// StepError carries a failed step's output under the step that failed, rather
// than letting it surface later and elsewhere.
type StepError struct {
	Step   string
	Stdout []byte
	Stderr []byte
	Err    error
}

func (e *StepError) Error() string {
	msg := fmt.Sprintf("%s: %v", e.Step, e.Err)
	if out := strings.TrimSpace(string(e.Stderr)); out != "" {
		msg += "\n" + out
	}
	return msg
}

func (e *StepError) Unwrap() error { return e.Err }
