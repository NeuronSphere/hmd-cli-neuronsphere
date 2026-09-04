package environment

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
)

func testOptions(home string, env map[string]string) *Options {
	return &Options{
		Home:   home,
		Lookup: func(key string) string { return env[key] },
		Out:    io.Discard,
		Err:    io.Discard,
	}
}

func registryHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	path := registry.Path(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{
  "control_plane": {"bootstrapped": true, "compose_project": "cp", "floci_data_dir": "` + home + `/floci/data", "network": "net"},
  "default_env": "local",
  "environments": {"local": {"account_id": "000000000001", "bootstrap": {"csd_nid": "n"},
    "compose_project": "p", "core_instance_name": "local-neuronsphere", "db_container": "hmd_db-local",
    "deployment_id": "local", "graph_container": "global-graph-local", "k3s_cluster": "ns-local-abc",
    "kubeconfig": "` + home + `/k3s/kubeconfig", "legacy_layout": false, "name": "local",
    "port_base": 19000, "port_slot": 8, "slug": "local", "state_dir": "` + home + `/env"}},
  "version": 1
}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return home
}

// The BOM reconcile lands with the deploy path. Until then a bare `env start`
// refuses by name rather than half-starting and reporting success.
func TestStartWithoutNoDeployRefusesAndNamesTheAlternative(t *testing.T) {
	t.Parallel()

	err := Start(context.Background(), testOptions(registryHome(t), nil), "local")
	if err == nil {
		t.Fatal("a full start succeeded; the deploy path does not exist yet")
	}
	if got := nserr.CodeOf(err); got != nserr.Usage {
		t.Errorf("exit code = %d, want %d (usage)", got, nserr.Usage)
	}
	for _, want := range []string{"--no-deploy", "hmd neuronsphere up"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q:\n%s", want, err)
		}
	}
}

func TestStartOnAnUnknownEnvironmentIsAUsageError(t *testing.T) {
	t.Parallel()

	opts := testOptions(registryHome(t), nil)
	opts.NoDeploy = true
	err := Start(context.Background(), opts, "nope")
	if err == nil {
		t.Fatal("an unknown environment was accepted")
	}
	if got := nserr.CodeOf(err); got != nserr.Usage {
		t.Errorf("exit code = %d, want %d", got, nserr.Usage)
	}
	if !strings.Contains(err.Error(), "local") {
		t.Errorf("the error does not name what exists: %v", err)
	}
}

func TestIngressEnabled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{"default", nil, true},
		{"false", map[string]string{"HMD_LOCAL_NEURONSPHERE_ENABLE_INGRESS": "false"}, false},
		{"0", map[string]string{"HMD_LOCAL_NEURONSPHERE_ENABLE_INGRESS": "0"}, false},
		{"no", map[string]string{"HMD_LOCAL_NEURONSPHERE_ENABLE_INGRESS": "NO"}, false},
		{"anything else enables it", map[string]string{"HMD_LOCAL_NEURONSPHERE_ENABLE_INGRESS": "yes"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ingressEnabled(testOptions("", tt.env)); got != tt.want {
				t.Errorf("ingressEnabled = %v, want %v", got, tt.want)
			}
		})
	}
}

// An empty expected image means "do not judge staleness". Guessing a default
// here would condemn a healthy cluster whose pin lives in the compose file's
// FLOCI_SERVICES_EKS_DEFAULT_IMAGE.
func TestExpectedK3sImageDefaultsToNoJudgement(t *testing.T) {
	t.Parallel()

	if got := expectedK3sImage(testOptions("", nil)); got != "" {
		t.Errorf("expectedK3sImage = %q, want empty so staleness is not guessed at", got)
	}
	opts := testOptions("", map[string]string{"HMD_LOCAL_K3S_WRAPPER_IMAGE": "my/image:1"})
	if got := expectedK3sImage(opts); got != "my/image:1" {
		t.Errorf("expectedK3sImage = %q, want the override", got)
	}
}

// /etc/hosts has no wildcards, so the vhost's *.<slug> server_name cannot help
// until each name resolves.
func TestUnresolvableHosts(t *testing.T) {
	t.Parallel()

	// localhost resolves to loopback everywhere; the other cannot resolve.
	got := unresolvableHosts([]string{"localhost", "definitely-not-a-real-host.invalid"})
	if len(got) != 1 || got[0] != "definitely-not-a-real-host.invalid" {
		t.Errorf("unresolvableHosts = %v, want only the unresolvable one", got)
	}
	if len(unresolvableHosts(nil)) != 0 {
		t.Error("an empty list produced warnings")
	}
}

func TestLegacyKubeconfigPath(t *testing.T) {
	t.Parallel()

	// The historical shared path, so host-side kubectl and the robot suites
	// keep working unchanged.
	if got := legacyKubeconfigPath("/home/hmd"); got != "/home/hmd/.cache/k3s/kubeconfig" {
		t.Errorf("legacyKubeconfigPath = %q", got)
	}
}

func TestCopyFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.WriteFile(src, []byte("body"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "nested", "dst")
	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copyFile: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "body" {
		t.Errorf("copied %q", got)
	}
}
