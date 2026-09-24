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

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/container"
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

	// Trino routed, so that this test is about the k3s line and not about the
	// trino one; TestReadySummaryReportsTrinoOnlyWhenRouted owns that.
	routed := &found{Trino: true}

	healthy := strings.Join(readySummary(env, manifest.SubstrateFull, nil, routed), "\n")
	if !strings.Contains(healthy, "Ready.") {
		t.Errorf("a healthy summary should lead with Ready., got:\n%s", healthy)
	}
	for _, want := range []string{"services", "trino", "k3s"} {
		if !strings.Contains(healthy, want) {
			t.Errorf("a healthy summary should list %s, got:\n%s", want, healthy)
		}
	}

	dead := strings.Join(readySummary(env, manifest.SubstrateFull, errors.New("it exited"), routed), "\n")
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

	routed := &found{Trino: true, DBAccount: true}

	none := strings.Join(readySummary(env, manifest.SubstrateNone, nil, routed), "\n")
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

	core := strings.Join(readySummary(env, manifest.SubstrateCore, nil, routed), "\n")
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

	full := strings.Join(readySummary(env, manifest.SubstrateFull, nil, routed), "\n")
	if strings.Contains(full, "substrate") {
		t.Errorf("full is the default and should not announce itself, got:\n%s", full)
	}
}

// NERD024 SPEC001: the dbaccount line reports what was deployed, not what the
// mode allows. Asserted in both directions for the Trino reason below -- the
// summary used to print it from the mode alone, so a one-sided test would pass
// against exactly the misreport this replaced.
func TestReadySummaryReportsDBAccountOnlyWhenDeployed(t *testing.T) {
	t.Parallel()

	env := &registry.Environment{Slug: "local", PortSlot: 8, DBContainer: "hmd_db-local"}

	deployed := strings.Join(readySummary(env, manifest.SubstrateCore, nil, &found{DBAccount: true}), "\n")
	if !strings.Contains(deployed, "dbaccount  http://localhost/local/hmd_ms_dbaccount/") {
		t.Errorf("a deployed dbaccount should be named, got:\n%s", deployed)
	}

	absent := strings.Join(readySummary(env, manifest.SubstrateCore, nil, &found{}), "\n")
	if strings.Contains(absent, "dbaccount") {
		t.Errorf("an environment that deployed none should not advertise one, got:\n%s", absent)
	}
	// The database is the control: it is still reported from the mode, so a
	// change that dropped both lines together would not read as a pass here.
	if !strings.Contains(absent, "database   hmd_db-local:5432") {
		t.Errorf("the database is still the mode's, got:\n%s", absent)
	}
}

// NERD023 SPEC003. Asserted by control in both directions: a test that only
// checked the routed case would have passed against the defect, which printed
// the line on every full-substrate environment whether Trino was there or not.
func TestReadySummaryReportsTrinoOnlyWhenRouted(t *testing.T) {
	t.Parallel()

	env := &registry.Environment{Slug: "local", PortSlot: 8}

	absent := strings.Join(readySummary(env, manifest.SubstrateFull, nil, &found{}), "\n")
	if strings.Contains(absent, "trino") {
		t.Errorf("an environment with no Trino must not advertise one, got:\n%s", absent)
	}
	// The rest of a full substrate is unaffected.
	if !strings.Contains(absent, "k3s        localhost:") {
		t.Errorf("the k3s line should survive, got:\n%s", absent)
	}

	routed := strings.Join(readySummary(env, manifest.SubstrateFull, nil, &found{Trino: true}), "\n")
	if !strings.Contains(routed, "trino      localhost:19033") {
		t.Errorf("a routed Trino should be reported on the slot's port, got:\n%s", routed)
	}

	// A nil accumulator is the caller that discovered nothing, not a caller
	// asking for the old behaviour.
	if nilf := strings.Join(readySummary(env, manifest.SubstrateFull, nil, nil), "\n"); strings.Contains(nilf, "trino") {
		t.Errorf("a nil accumulator must not advertise Trino, got:\n%s", nilf)
	}
}

// The deployed services and UIs are discovered on every start and were reduced
// to a count. Report them, capped, because the list is what makes the
// environment usable without a second command.
func TestReadySummaryListsWhatWasDiscovered(t *testing.T) {
	t.Parallel()

	env := &registry.Environment{Slug: "dev", PortSlot: 1}

	empty := strings.Join(readySummary(env, manifest.SubstrateFull, nil, &found{}), "\n")
	if !strings.Contains(empty, "http://localhost/dev/<service>/") {
		t.Errorf("with nothing deployed the summary should show the URL shape, got:\n%s", empty)
	}

	f := &found{
		Services: []string{"hmd_ms_naming", "hmd_ms_dbaccount"},
		UIHosts:  []string{"superset.local.neuronsphere.io"},
	}
	got := strings.Join(readySummary(env, manifest.SubstrateFull, nil, f), "\n")
	for _, want := range []string{
		"http://localhost/dev/hmd_ms_dbaccount/",
		"http://localhost/dev/hmd_ms_naming/",
		"http://superset.local.neuronsphere.io/",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary should list %s, got:\n%s", want, got)
		}
	}
	if strings.Contains(got, "<service>") {
		t.Errorf("the URL shape is for an environment with no services, got:\n%s", got)
	}
	// Sorted, so the same environment renders the same bytes twice.
	if i, j := strings.Index(got, "dbaccount"), strings.Index(got, "naming"); i > j {
		t.Errorf("services should be sorted, got:\n%s", got)
	}
}

// Past a handful a list stops being readable and a count says more.
func TestSummaryListCaps(t *testing.T) {
	t.Parallel()

	many := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}
	lines := summaryList("  services   ", many)
	joined := strings.Join(lines, "\n")
	if len(lines) != summaryListCap+1 {
		t.Errorf("want %d lines, got %d:\n%s", summaryListCap+1, len(lines), joined)
	}
	if !strings.Contains(joined, "... and 3 more") {
		t.Errorf("want the remainder as a count, got:\n%s", joined)
	}
	if !strings.HasPrefix(lines[0], "  services   a") {
		t.Errorf("the label belongs on the first line, got %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "             b") {
		t.Errorf("later lines align under the label, got %q", lines[1])
	}

	// Exactly one over the cap is listed rather than replaced by "and 1 more",
	// which would be longer than the thing it elides.
	seven := summaryList("  uis        ", many[:summaryListCap+1])
	if len(seven) != summaryListCap+1 {
		t.Errorf("want all %d listed, got %d", summaryListCap+1, len(seven))
	}
	if strings.Contains(strings.Join(seven, "\n"), "more") {
		t.Errorf("one over the cap should not be elided, got:\n%s", strings.Join(seven, "\n"))
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

// A plain `docker stop` relies on Docker's 10s SIGTERM-then-SIGKILL default,
// which is not always enough for kubelet/containerd to tear pod cgroups down
// cleanly -- SIGKILL landing mid-teardown is what corrupts kubepods for the
// next boot. stopK3s must try a graceful in-container shutdown first (whose
// failure or absence is never fatal) and then stop with a longer grace period.
func TestStopK3sTriesAGracefulShutdownThenStopsWithALongerGrace(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "docker")
	log := filepath.Join(dir, "commands")
	// The graceful-shutdown exec fails (as it would on an image with no
	// k3s-killall.sh) -- that must not stop stopK3s from still calling
	// `docker stop` afterward.
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> " + log + "\n" +
		"if [ \"$1\" = exec ]; then exit 1; fi\n" +
		"exit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	d := &container.Docker{Bin: bin}

	if err := stopK3s(context.Background(), d, "floci-eks-1.ns-local-abc"); err != nil {
		t.Fatalf("stopK3s: %v", err)
	}

	commands, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(commands)), "\n")
	if len(lines) != 2 {
		t.Fatalf("want exactly a graceful-shutdown exec then a stop, got: %v", lines)
	}
	if !strings.Contains(lines[0], "exec floci-eks-1.ns-local-abc") || !strings.Contains(lines[0], "k3s-killall.sh") {
		t.Errorf("first call should attempt the graceful shutdown script: %q", lines[0])
	}
	if lines[1] != "stop -t 30 floci-eks-1.ns-local-abc" {
		t.Errorf("second call should stop with a longer grace period, got %q", lines[1])
	}
}
