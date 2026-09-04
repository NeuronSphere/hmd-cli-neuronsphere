package k3s

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// recordExec captures the docker arguments RunKube builds.
type recordExec struct {
	args   []string
	calls  [][]string
	stdout string
	stderr string
	err    error
	// seen is the mounted directory's contents at call time, since RunKube
	// deletes it afterwards.
	seen map[string]string
}

func (r *recordExec) run(_ context.Context, args ...string) ([]byte, []byte, error) {
	r.args = args
	if r.seen == nil {
		r.seen = map[string]string{}
	}
	// `docker cp <local>/. <container>:<remote>` is where the step's files are
	// visible; capture them before RunKube deletes the directory.
	if len(args) >= 3 && args[0] == "cp" {
		host := strings.TrimSuffix(args[1], "/.")
		if entries, err := os.ReadDir(host); err == nil {
			for _, e := range entries {
				body, _ := os.ReadFile(filepath.Join(host, e.Name()))
				r.seen[e.Name()] = string(body)
			}
		}
	}
	r.calls = append(r.calls, args)
	return []byte(r.stdout), []byte(r.stderr), r.err
}

func testKube(t *testing.T, exec *recordExec) *Kube {
	t.Helper()
	return &Kube{
		Cluster:   "ns-local-abc",
		Container: "floci-eks-000000000001.ns-local-abc",
		Run:       exec.run,
	}
}

// The script is mounted, never passed as an argument -- the same rule the
// deploy node follows for /tmp/hmd-deploy-node.sh, and for the same reason: a
// step with much inlined configuration exceeds ARG_MAX and fails before it runs.
func TestRunKubeMountsTheScriptRatherThanPassingIt(t *testing.T) {
	t.Parallel()

	exec := &recordExec{stdout: "ok"}
	k := testKube(t, exec)

	script := []byte("kubectl get nodes\n")
	if _, _, err := k.RunKube(context.Background(), script, nil); err != nil {
		t.Fatalf("RunKube: %v", err)
	}

	for _, call := range exec.calls {
		if strings.Contains(strings.Join(call, " "), "kubectl get nodes") {
			t.Errorf("the script was passed as an argument: %v", call)
		}
	}
	if exec.seen[ScriptName] != string(script) {
		t.Errorf("the script was not copied in; saw %v", exec.seen)
	}
	if runScriptCall(exec) == nil {
		t.Errorf("no call runs the copied script: %v", exec.calls)
	}
}

// runScriptCall finds the exec that runs the step script. The cleanup exec runs
// after it, so position alone would be misleading.
func runScriptCall(exec *recordExec) []string {
	for _, call := range exec.calls {
		joined := strings.Join(call, " ")
		if call[0] == "exec" && strings.Contains(joined, "sh ") && strings.Contains(joined, "/"+ScriptName) {
			return call
		}
	}
	return nil
}

// Mounting a directory is what replaces the two sites that fed JSON on stdin:
// `apply -f -` has no equivalent for a script inside a container.
func TestRunKubeMountsExtraFilesAlongsideTheScript(t *testing.T) {
	t.Parallel()

	exec := &recordExec{}
	k := testKube(t, exec)

	files := map[string][]byte{"coredns.yaml": []byte("kind: ConfigMap\n")}
	if _, _, err := k.RunKube(context.Background(), []byte(`kubectl apply -f "$STEP_DIR/coredns.yaml"`), files); err != nil {
		t.Fatalf("RunKube: %v", err)
	}
	if exec.seen["coredns.yaml"] != "kind: ConfigMap\n" {
		t.Errorf("the manifest was not copied in; saw %v", exec.seen)
	}
}

func TestRunKubeRejectsUnsafeFileNames(t *testing.T) {
	t.Parallel()

	k := testKube(t, &recordExec{})
	for _, name := range []string{"../escape", "sub/dir", ScriptName} {
		if _, _, err := k.RunKube(context.Background(), []byte("true"), map[string][]byte{name: []byte("x")}); err == nil {
			t.Errorf("accepted the file name %q", name)
		}
	}
}

// Every call execs into the running k3s node rather than launching a
// container: projectbuilder ships no kubectl, and the node's own is matched to
// the cluster by construction.
func TestRunKubeExecsIntoTheK3sNode(t *testing.T) {
	t.Parallel()

	exec := &recordExec{}
	k := testKube(t, exec)
	if _, _, err := k.RunKube(context.Background(), []byte("true"), nil); err != nil {
		t.Fatal(err)
	}

	for _, call := range exec.calls {
		if call[0] == "run" {
			t.Errorf("a container was launched: %v", call)
		}
	}
	call := runScriptCall(exec)
	if call == nil {
		t.Fatalf("no call runs the step: %v", exec.calls)
	}
	found := false
	for _, a := range call {
		if a == "floci-eks-000000000001.ns-local-abc" {
			found = true
		}
	}
	if !found {
		t.Errorf("the step did not target the k3s container: %v", call)
	}
}

// The copied directory is cleaned up so a long-lived node does not accumulate
// one per step per start.
func TestRunKubeCleansUpItsWorkingDirectory(t *testing.T) {
	t.Parallel()

	exec := &recordExec{}
	if _, _, err := testKube(t, exec).RunKube(context.Background(), []byte("true"), nil); err != nil {
		t.Fatal(err)
	}
	removals := 0
	for _, call := range exec.calls {
		if strings.Contains(strings.Join(call, " "), "rm -rf "+WorkDir) {
			removals++
		}
	}
	if removals < 2 {
		t.Errorf("expected the directory cleared before and after the step, saw %d removals", removals)
	}
}

func TestRunKubeRefusesWithoutAContainer(t *testing.T) {
	t.Parallel()

	k := &Kube{Run: (&recordExec{}).run}
	if _, _, err := k.RunKube(context.Background(), []byte("true"), nil); err == nil {
		t.Error("RunKube ran with no k3s container")
	}
}

// stdout carries data a caller parses; stderr carries diagnosis. Merging them
// makes `kubectl get -o json` unparseable the moment anything warns.
func TestRunKubeKeepsStdoutAndStderrApart(t *testing.T) {
	t.Parallel()

	exec := &recordExec{stdout: `{"items":[]}`, stderr: "W0101 deprecated"}
	stdout, stderr, err := testKube(t, exec).RunKube(context.Background(), []byte("true"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(stdout) != `{"items":[]}` {
		t.Errorf("stdout = %q", stdout)
	}
	if string(stderr) != "W0101 deprecated" {
		t.Errorf("stderr = %q", stderr)
	}
}

// The mounted kubeconfig's server is a host-published port, unreachable from
// inside a container on the NeuronSphere network.
func TestKubeconfigForContainerRepointsTheServer(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "kubeconfig")
	original := `apiVersion: v1
clusters:
- cluster:
    certificate-authority-data: c29tZS1jYQ==
    server: https://127.0.0.1:19072
  name: default
kind: Config
`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	k := &Kube{Container: "floci-eks-000000000001.ns-local-abc", Kubeconfig: path}
	rewritten, cleanup, err := k.KubeconfigForContainer()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	if rewritten == path {
		t.Fatal("the host kubeconfig was returned unchanged")
	}
	data, err := os.ReadFile(rewritten)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	cluster := cfg["clusters"].([]any)[0].(map[string]any)["cluster"].(map[string]any)

	if got := cluster["server"]; got != "https://floci-eks-000000000001.ns-local-abc:6443" {
		t.Errorf("server = %v, want the in-network address", got)
	}
	// The certificate is issued for the host-facing name, so verification has
	// to go with the repoint.
	if cluster["insecure-skip-tls-verify"] != true {
		t.Errorf("TLS verification was left on: %v", cluster)
	}
	if _, ok := cluster["certificate-authority-data"]; ok {
		t.Error("the certificate authority was left in place")
	}

	// The user's own kubeconfig is untouched.
	after, _ := os.ReadFile(path)
	if string(after) != original {
		t.Error("the host kubeconfig was modified")
	}
}

func TestKubeconfigForContainerFallsBackRatherThanFailing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		k    *Kube
	}{
		{"no kubeconfig", &Kube{Container: "c"}},
		{"no container", &Kube{Kubeconfig: "/nope"}},
		{"missing file", &Kube{Container: "c", Kubeconfig: "/definitely/not/here"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, cleanup, err := tt.k.KubeconfigForContainer()
			defer cleanup()
			if err != nil {
				t.Errorf("KubeconfigForContainer: %v", err)
			}
			if got != tt.k.Kubeconfig {
				t.Errorf("got %q, want the raw path back", got)
			}
		})
	}
}

func TestKubeconfigForContainerFallsBackOnMalformedYAML(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "kubeconfig")
	if err := os.WriteFile(path, []byte("\tnot: [valid"), 0o600); err != nil {
		t.Fatal(err)
	}
	k := &Kube{Container: "c", Kubeconfig: path}
	got, cleanup, err := k.KubeconfigForContainer()
	defer cleanup()
	if err != nil {
		t.Errorf("KubeconfigForContainer: %v", err)
	}
	if got != path {
		t.Errorf("got %q, want the raw path back", got)
	}
}

// A failed step's output belongs under the step that failed, not surfacing
// later and elsewhere.
func TestStepErrorCarriesTheDiagnosis(t *testing.T) {
	t.Parallel()

	err := &StepError{
		Step:   "applying the CoreDNS records",
		Stderr: []byte("Error from server: namespaces \"kube-system\" not found\n"),
		Err:    errors.New("exit status 1"),
	}
	msg := err.Error()
	for _, want := range []string{"applying the CoreDNS records", "exit status 1", "namespaces"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not contain %q", msg, want)
		}
	}
	if !errors.Is(err, err.Err) {
		t.Error("the cause is not reachable through Unwrap")
	}
}
