package environment

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
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

// A deploy against an unreachable service says which service and how to start
// it, rather than failing somewhere inside the seeding protocol.
func TestApplyRefusesWhenMSDeploymentIsUnreachable(t *testing.T) {
	t.Parallel()

	// A port nothing serves, so the test depends on nothing running here.
	opts := testOptions(registryHome(t), map[string]string{
		"HMD_LOCAL_MS_DEPLOYMENT_URL": "http://127.0.0.1:1/hmd_ms_deployment",
	})
	err := Apply(context.Background(), opts, "local")
	if err == nil {
		t.Fatal("Apply succeeded against an unreachable service")
	}
	if !strings.Contains(err.Error(), "control-plane start") {
		t.Errorf("the error does not name the remedy: %v", err)
	}
}

func TestMSDeploymentURLIsOverridable(t *testing.T) {
	t.Parallel()

	if got := msDeploymentURL(testOptions("", nil)); got != DefaultMSDeploymentURL {
		t.Errorf("default = %q", got)
	}
	opts := testOptions("", map[string]string{"HMD_LOCAL_MS_DEPLOYMENT_URL": "http://elsewhere/x"})
	if got := msDeploymentURL(opts); got != "http://elsewhere/x" {
		t.Errorf("override ignored, got %q", got)
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

// fakeEnvReader stands in for the running Floci container's environment.
type fakeEnvReader map[string]string

func (f fakeEnvReader) ContainerEnv(context.Context, string) map[string]string { return f }

// The pin that decides staleness is the one Floci was actually started with.
// Reading it back beats reconstructing it, and a Floci that cannot be read
// still means "do not judge staleness" rather than a guess.
func TestExpectedK3sImageReadsFlociRatherThanGuessing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	if got := expectedK3sImage(ctx, testOptions("", nil), fakeEnvReader(nil)); got != "" {
		t.Errorf("expectedK3sImage = %q, want empty so staleness is not guessed at", got)
	}

	floci := fakeEnvReader{"FLOCI_SERVICES_EKS_DEFAULT_IMAGE": "ghcr.io/example/hmd-img-k3s-floci:0.3"}
	if got := expectedK3sImage(ctx, testOptions("", nil), floci); got != "ghcr.io/example/hmd-img-k3s-floci:0.3" {
		t.Errorf("expectedK3sImage = %q, want the pin Floci was started with", got)
	}

	opts := testOptions("", map[string]string{"HMD_LOCAL_K3S_WRAPPER_IMAGE": "my/image:1"})
	if got := expectedK3sImage(ctx, opts, floci); got != "my/image:1" {
		t.Errorf("expectedK3sImage = %q, want the override to win", got)
	}
}

// A dead cluster must not be advertised on a port nothing listens on: that is
// exactly how a k3s container that exited eleven seconds in came to be reported
// as a successful start.
func TestReadySummaryOmitsTheK3sPortWhenTheClusterIsDead(t *testing.T) {
	t.Parallel()

	env := &registry.Environment{Slug: "local", PortSlot: 8}

	healthy := strings.Join(readySummary(env, manifest.SubstrateFull, nil), "\n")
	if !strings.Contains(healthy, "Ready.") {
		t.Errorf("a healthy summary should lead with Ready., got:\n%s", healthy)
	}
	for _, want := range []string{"services", "trino", "k3s"} {
		if !strings.Contains(healthy, want) {
			t.Errorf("a healthy summary should list %s, got:\n%s", want, healthy)
		}
	}

	dead := strings.Join(readySummary(env, manifest.SubstrateFull, errors.New("it exited")), "\n")
	if strings.Contains(dead, "k3s        localhost:") {
		t.Errorf("a dead cluster must not advertise its port, got:\n%s", dead)
	}
	if strings.Contains(dead, "Ready.") {
		t.Errorf("a dead cluster must not report Ready., got:\n%s", dead)
	}
	if !strings.Contains(dead, "not running") {
		t.Errorf("a dead cluster should say so, got:\n%s", dead)
	}
	// The rest of the platform is still up and still worth naming.
	if !strings.Contains(dead, "services") || !strings.Contains(dead, "trino") {
		t.Errorf("a dead cluster should still list what does work, got:\n%s", dead)
	}
}

// NERD014 SPEC007: the summary names only what the mode runs. A trino port on
// an environment with no cluster is the same misreport as a dead one.
func TestReadySummaryPerSubstrate(t *testing.T) {
	t.Parallel()

	env := &registry.Environment{Slug: "local", PortSlot: 8, DBContainer: "hmd_db-local"}

	none := strings.Join(readySummary(env, manifest.SubstrateNone, nil), "\n")
	for _, absent := range []string{"trino", "k3s", "database", "dbaccount"} {
		if strings.Contains(none, absent) {
			t.Errorf("none should not list %s, got:\n%s", absent, none)
		}
	}
	for _, want := range []string{"Ready.", "services", "substrate  none"} {
		if !strings.Contains(none, want) {
			t.Errorf("none should list %s, got:\n%s", want, none)
		}
	}

	core := strings.Join(readySummary(env, manifest.SubstrateCore, nil), "\n")
	for _, absent := range []string{"trino", "k3s"} {
		if strings.Contains(core, absent) {
			t.Errorf("core should not list %s, got:\n%s", absent, core)
		}
	}
	for _, want := range []string{"database   hmd_db-local:5432", "dbaccount", "substrate  core"} {
		if !strings.Contains(core, want) {
			t.Errorf("core should list %s, got:\n%s", want, core)
		}
	}

	full := strings.Join(readySummary(env, manifest.SubstrateFull, nil), "\n")
	if strings.Contains(full, "substrate") {
		t.Errorf("full is the default and should not announce itself, got:\n%s", full)
	}
}

// The exit code is the half of "fail loudly" a message cannot carry.
func TestADeadClusterExitsFail(t *testing.T) {
	t.Parallel()

	err := nserr.Wrap(nserr.Fail, errors.New("the k3s container is not running"))
	if got := nserr.CodeOf(err); got != nserr.Fail {
		t.Errorf("CodeOf = %v, want %v", got, nserr.Fail)
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

// The defect this names is not in this repository, so the test is about the
// message: an environment named anything but `local` fails its first CDKTF node
// in `tofu init`, and until hmd-lib-cdktf stops comparing the deployment tier to
// a literal, the only useful thing nsctl can do is say so before the deploy
// rather than after 955 lines of Terraform output.
func TestAnUndeployableSlugIsNamedBeforeItCostsADeploy(t *testing.T) {
	t.Parallel()

	var got []string
	warn := func(format string, a ...any) { got = append(got, fmt.Sprintf(format, a...)) }

	WarnUndeployableSlug(warn, DeployableSlug)
	if len(got) != 0 {
		t.Fatalf("the deployable slug must warn about nothing: %v", got)
	}

	WarnUndeployableSlug(warn, "")
	if len(got) != 0 {
		t.Fatalf("an unnamed environment is not a naming problem: %v", got)
	}

	WarnUndeployableSlug(warn, "dev2")
	if len(got) != 1 {
		t.Fatalf("want one warning, got %v", got)
	}
	// The message has to carry enough to act on: which environment, what
	// breaks, and where the fix is. A warning that only says "this may not
	// work" costs a deploy to interpret.
	for _, want := range []string{"dev2", "local", "tofu init", "hmd-lib-cdktf", "SPEC014"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("the warning does not mention %q: %s", want, got[0])
		}
	}
}
