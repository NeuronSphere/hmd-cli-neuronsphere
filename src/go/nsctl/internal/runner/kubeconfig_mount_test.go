package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
)

// A kubeconfig that is not there must not be mounted. Docker creates a missing
// bind-mount source as an empty directory -- on the host as well as inside the
// container -- and every node a fresh environment runs before its cluster
// exists names this path.
func TestKubeconfigMountDeclinesWhatIsNotAFile(t *testing.T) {
	dir := t.TempDir()

	real := filepath.Join(dir, "kubeconfig")
	if err := os.WriteFile(real, []byte("apiVersion: v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	asDir := filepath.Join(dir, "kubeconfig-as-dir")
	if err := os.Mkdir(asDir, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ name, path, want string }{
		{"unset", "", ""},
		{"absent", filepath.Join(dir, "nope"), ""},
		{"the directory Docker leaves behind", asDir, ""},
		{"a real kubeconfig", real, real},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := (Config{Kubeconfig: tc.path}).kubeconfigMount(); got != tc.want {
				t.Fatalf("kubeconfigMount() = %q, want %q", got, tc.want)
			}
		})
	}
}

// The mount must be absent from the argv entirely rather than passed with an
// empty source, and the variable naming it must go with it: a node told where
// its kubeconfig is, and handed a directory, fails later and somewhere else.
func TestDockerArgsOmitAnAbsentKubeconfig(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "kubeconfig")
	if err := os.WriteFile(present, []byte("apiVersion: v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	node := msdeploy.DeploymentNode{InstanceName: "thing"}

	for _, tc := range []struct {
		name    string
		path    string
		mounted bool
	}{
		{"absent", filepath.Join(dir, "nope"), false},
		{"present", present, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &Runner{Config: Config{Kubeconfig: tc.path, Image: "img", Network: "net"}}

			native := strings.Join(r.dockerArgs(node, "/ws", "/script.sh", false), "\x00")
			if got := strings.Contains(native, "/root/.kube/config"); got != tc.mounted {
				t.Fatalf("native: kubeconfig mounted = %v, want %v\n%s", got, tc.mounted, native)
			}
			if got := strings.Contains(native, "HMD_LOCAL_K3S_KUBECONFIG"); got != tc.mounted {
				t.Fatalf("native: HMD_LOCAL_K3S_KUBECONFIG present = %v, want %v", got, tc.mounted)
			}

			foreign := strings.Join(r.dockerArgsForeign(node, "/ws", nodeCommand{Argv: []string{"make"}}, []byte("{}")), "\x00")
			if got := strings.Contains(foreign, ForeignKubeconfigPath); got != tc.mounted {
				t.Fatalf("foreign: kubeconfig mounted = %v, want %v\n%s", got, tc.mounted, foreign)
			}
		})
	}
}
