package container

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every filter value needs the literal "label=" prefix. Without it docker
// rejects the filter outright, and since that is an error rather than an empty
// result, the status command reported every database and graph as never
// provisioned -- which reads as data loss rather than a typo.
func TestFlociPSArgsPrefixEveryFilterWithLabel(t *testing.T) {
	t.Parallel()

	args := flociPSArgs("rds", "000000000001", "environment-db-hmd-postgres-rds-local-local-reg1-hmdtr1")

	filters := 0
	for i, a := range args {
		if a != "--filter" {
			continue
		}
		filters++
		if i+1 >= len(args) {
			t.Fatalf("--filter at %d has no value", i)
		}
		if !strings.HasPrefix(args[i+1], "label=") {
			t.Errorf("filter %q is missing the label= prefix", args[i+1])
		}
	}
	if filters != 3 {
		t.Errorf("got %d filters, want the service/account/resource triple", filters)
	}
}

func TestFlociPSArgsMatchTheRealLabels(t *testing.T) {
	t.Parallel()

	// These are the labels a live Floci actually wrote, read back with
	// `docker inspect` on the running containers.
	args := flociPSArgs("neptune", "000000000001", "global-graph-hmd-inf-neptune-local-local-reg1-hmdtr1")
	joined := strings.Join(args, " ")

	for _, want := range []string{
		"label=io.floci.service=neptune",
		"label=io.floci.account=000000000001",
		"label=io.floci.resource-id=global-graph-hmd-inf-neptune-local-local-reg1-hmdtr1",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args do not contain %q:\n%s", want, joined)
		}
	}
}

// `docker ps -a`, not `docker ps`: after `env stop` a stopped database is the
// expected state, and reporting it as unprovisioned would read as data loss.
func TestFlociPSArgsIncludeStoppedContainers(t *testing.T) {
	t.Parallel()

	args := flociPSArgs("rds", "a", "b")
	if len(args) < 2 || args[0] != "ps" || args[1] != "-a" {
		t.Errorf("args start with %v, want `ps -a`", args[:2])
	}
}

// Docker renders a stopped container's empty IPAddress as the literal string
// "invalid IP". It is truthy, so a caller that only checks for a non-empty
// string writes it straight into whatever it was building -- which is how
// "invalid IP global-graph" ended up in a CoreDNS server block.
func TestContainerIPRejectsDockersInvalidIPPlaceholder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		out  string
		want string
	}{
		{"a real address", "172.18.0.3", "172.18.0.3"},
		{"docker's placeholder for a stopped container", "invalid IP", ""},
		{"an empty result", "", ""},
		{"anything unparseable", "not-an-address", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := parseContainerIP(tt.out); got != tt.want {
				t.Errorf("parseContainerIP(%q) = %q, want %q", tt.out, got, tt.want)
			}
		})
	}
}

// A state docker could not answer for must be distinguishable from one it
// answered "dead" to. Reading the first as the second fails a start that was
// fine, which is the same doctrine ImageOf's empty result follows.
func TestParseStateSeparatesUnreadableFromDead(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		out      string
		wantOK   bool
		running  bool
		status   string
		exitCode int
	}{
		{
			name:     "the k3s container after kube-router killed it",
			out:      `{"Status":"exited","Running":false,"ExitCode":1,"OOMKilled":false,"Error":""}`,
			wantOK:   true,
			status:   "exited",
			exitCode: 1,
		},
		{
			name:    "a healthy container",
			out:     `{"Status":"running","Running":true,"ExitCode":0}`,
			wantOK:  true,
			running: true,
			status:  "running",
		},
		{"docker said nothing", "", false, false, "", 0},
		{"docker said something unparseable", "not json", false, false, "", 0},
		{"valid json with no status is not an answer", `{}`, false, false, "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := parseState(tt.out)
			if ok != tt.wantOK {
				t.Fatalf("parseState(%q) ok = %v, want %v", tt.out, ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if got.Running != tt.running || got.Status != tt.status || got.ExitCode != tt.exitCode {
				t.Errorf("parseState(%q) = %+v, want running=%v status=%q exit=%d",
					tt.out, got, tt.running, tt.status, tt.exitCode)
			}
		})
	}
}

// A best-effort sweep that stops at its first failure is not a sweep. The first
// real `nsctl env purge` proved it: floci-ecr-registry-data was in use, and the
// two floci-rds-db-* volumes queued behind it survived a purge whose entire
// stated purpose is the volumes an earlier purge could not reach.
func TestRemoveVolumesKeepsGoingPastOneItCannotRemove(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "docker")
	log := filepath.Join(dir, "attempted")
	// Fails only for the in-use volume, exactly as dockerd does, and records
	// every name it was asked for so the test can see what was attempted rather
	// than only what succeeded.
	script := "#!/bin/sh\n" +
		"name=$4\n" +
		"echo \"$name\" >> " + log + "\n" +
		"if [ \"$name\" = stuck ]; then\n" +
		"  echo 'Error response from daemon: remove stuck: volume is in use' >&2\n" +
		"  exit 1\n" +
		"fi\n" +
		"exit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	d := &Docker{Bin: bin}
	err := d.RemoveVolumes(context.Background(), "stuck", "after-one", "after-two")
	if err == nil {
		t.Fatal("a volume that could not be removed must still be reported")
	}
	if !strings.Contains(err.Error(), "stuck") {
		t.Errorf("the error does not name the volume that failed: %v", err)
	}

	attempted, readErr := os.ReadFile(log)
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, want := range []string{"after-one", "after-two"} {
		if !strings.Contains(string(attempted), want) {
			t.Errorf("%s was never attempted: the sweep stopped at the first failure", want)
		}
	}
}

// A volume that is simply not there is not a failure -- the sweep runs against
// a name list that may predate someone else's cleanup.
func TestRemoveVolumesTreatsAMissingVolumeAsDone(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "docker")
	script := "#!/bin/sh\necho 'Error: No such volume: gone' >&2\nexit 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	d := &Docker{Bin: bin}
	if err := d.RemoveVolumes(context.Background(), "gone"); err != nil {
		t.Errorf("a missing volume must not be an error: %v", err)
	}
}

// Adding an Ingress hostname to the proxy must keep the aliases it already
// carries -- the identity provider's issuer among them -- because Docker only
// takes aliases at connect time and the reconnect is the whole endpoint.
func TestEnsureNetworkAliasesKeepsTheExistingOnes(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "docker")
	log := filepath.Join(dir, "commands")
	// `inspect` answers with two aliases already on the endpoint; everything
	// else is recorded and succeeds.
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> " + log + "\n" +
		"if [ \"$1\" = inspect ]; then echo '[\"hmd_proxy\",\"auth.local.neuronsphere.io\"]'; fi\n" +
		"exit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	d := &Docker{Bin: bin}

	reconnected, err := d.EnsureNetworkAliases(context.Background(), "hmd_proxy", "net",
		[]string{"argo.local.neuronsphere.io", "auth.local.neuronsphere.io"})
	if err != nil {
		t.Fatal(err)
	}
	if !reconnected {
		t.Fatal("a missing alias must reconnect")
	}
	commands, _ := os.ReadFile(log)
	got := string(commands)
	for _, want := range []string{
		"network disconnect net hmd_proxy",
		"--alias hmd_proxy", "--alias auth.local.neuronsphere.io", "--alias argo.local.neuronsphere.io",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("commands do not include %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "--alias auth.local.neuronsphere.io") != 1 {
		t.Errorf("an alias present on both sides was passed twice:\n%s", got)
	}

	// Nothing missing: no reconnect, and the endpoint is left alone.
	os.Remove(log)
	reconnected, err = d.EnsureNetworkAliases(context.Background(), "hmd_proxy", "net",
		[]string{"auth.local.neuronsphere.io"})
	if err != nil || reconnected {
		t.Fatalf("reconnected=%v err=%v, want an untouched endpoint", reconnected, err)
	}
	commands, _ = os.ReadFile(log)
	if strings.Contains(string(commands), "disconnect") {
		t.Error("an endpoint that already carries every alias was reconnected")
	}
}
