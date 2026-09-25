package cmd

import (
	"os"
	"strings"
	"testing"
)

// A registry whose resolver was moved off 19153 because something else held it.
const movedResolverRegistry = `{
  "control_plane": {"bootstrapped": true, "compose_project": "cp", "floci_data_dir": "/d",
                    "network": "net", "ports": {"dns": 19154}},
  "default_env": "local",
  "environments": {},
  "version": 1
}`

// nsctl prints the privileged step and does not take it. This is the whole
// posture of the command (NERD026 SPEC002, NERD023): it needs root, and it
// changes a file that belongs to the user.
func TestDNSInstallPrintsTheStepAndRunsNothing(t *testing.T) {
	t.Parallel()

	before, _ := os.Stat("/etc/resolver")
	out, _, err := run(t, fakeEnv(nil), "dns", "install")
	if err != nil {
		t.Fatalf("dns install: %v", err)
	}

	if !strings.Contains(out, "sudo") {
		t.Errorf("dns install prints no privileged command:\n%s", out)
	}
	if !strings.Contains(out, "does not run it for you") {
		t.Errorf("dns install does not say that it will not run it:\n%s", out)
	}
	// It must not have created or touched the resolver directory itself.
	after, _ := os.Stat("/etc/resolver")
	if before == nil && after != nil {
		t.Error("dns install created /etc/resolver; it must only print the step")
	}
}

// The printed command has to name the port that is actually served. The resolver
// is a chosen port (NERD025 SPEC008), and printing the constant told the user to
// point their machine at a port nothing listens on -- a resolver file that fails
// silently and adds latency to every lookup in the suffix.
func TestDNSInstallPrintsTheChosenPort(t *testing.T) {
	t.Parallel()

	home := registryHome(t, movedResolverRegistry)
	out, _, err := run(t, fakeEnv(map[string]string{"HMD_HOME": home}), "dns", "install")
	if err != nil {
		t.Fatalf("dns install: %v", err)
	}

	if !strings.Contains(out, "19154") {
		t.Errorf("dns install does not print the chosen port 19154:\n%s", out)
	}
	if strings.Contains(out, "19153") {
		t.Errorf("dns install still prints the default port:\n%s", out)
	}
}

// An explicit flag is the user's statement and outranks both.
func TestDNSInstallHonoursAnExplicitPort(t *testing.T) {
	t.Parallel()

	home := registryHome(t, movedResolverRegistry)
	out, _, err := run(t, fakeEnv(map[string]string{"HMD_HOME": home}),
		"dns", "install", "--port", "15353")
	if err != nil {
		t.Fatalf("dns install: %v", err)
	}
	if !strings.Contains(out, "15353") {
		t.Errorf("an explicit --port should win:\n%s", out)
	}
}

// The resolver file is filed under the suffix itself. Filed under its parent it
// would capture a whole subtree that is not ours -- the public marketing site
// under neuronsphere.io, or the entire mDNS TLD under a bare `local`.
func TestDNSInstallScopesTheResolverFileToTheSuffixItself(t *testing.T) {
	t.Parallel()

	out, _, err := run(t, fakeEnv(nil), "dns", "install")
	if err != nil {
		t.Fatalf("dns install: %v", err)
	}
	if !strings.Contains(out, "not to") {
		t.Errorf("dns install does not say what it is NOT scoped to:\n%s", out)
	}
}

// The two failures need opposite fixes: the resolver is not running, or the
// machine is not pointed at it. Collapsing them into one message that says
// `dns install` either way sends a user whose control plane is down to edit a
// resolver file that was already correct (NERD026 SPEC003).
//
// The registry here puts the resolver on a port nothing listens on, which is
// what a stopped control plane looks like from the host.
func TestDNSStatusNamesTheResolverWhenItIsNotRunning(t *testing.T) {
	t.Parallel()

	home := registryHome(t, movedResolverRegistry)
	out, _, err := run(t, fakeEnv(map[string]string{"HMD_HOME": home}), "dns", "status")
	if err != nil {
		t.Fatalf("dns status: %v", err)
	}

	if !strings.Contains(out, "control-plane start") {
		t.Errorf("a resolver that is not running should name the command that starts it:\n%s", out)
	}
	if !strings.Contains(out, "19154") {
		t.Errorf("the report should name the port it looked at:\n%s", out)
	}
}

// A status report that cannot fail is not a report. `dns status` exits 0 either
// way -- it answers a question rather than asserting a precondition -- so the
// distinction has to be visible in what it prints.
func TestDNSStatusReportsTheProbeNameItUsed(t *testing.T) {
	t.Parallel()

	home := registryHome(t, movedResolverRegistry)
	out, _, err := run(t, fakeEnv(map[string]string{"HMD_HOME": home}), "dns", "status")
	if err != nil {
		t.Fatalf("dns status: %v", err)
	}
	// The probe is a name nothing has ever deployed: resolving it is the
	// property a hosts file cannot have, and therefore the one worth asserting.
	if !strings.Contains(out, "wildcard-probe") {
		t.Errorf("the report does not name the wildcard probe it used:\n%s", out)
	}
}
