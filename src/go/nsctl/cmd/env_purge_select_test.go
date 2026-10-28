package cmd

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/environment"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/lease"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// NERD035 SPEC007: env purge --idle / --keep select stale pool environments.

// poolRegistry is a member (local) and three pool-created environments.
const poolRegistry = `{
  "control_plane": {"bootstrapped": true, "compose_project": "cp", "floci_data_dir": "/d", "network": "net",
                    "ports": {"http": 1, "floci": 1}},
  "default_env": "local",
  "environments": {
    "local": {"account_id": "000000000001", "bootstrap": {}, "compose_project": "p1",
              "core_instance_name": "local-neuronsphere", "db_container": "hmd_db-local",
              "deployment_id": "local", "graph_container": "global-graph-local",
              "k3s_cluster": "ns-local-abc", "kubeconfig": "/k", "legacy_layout": false,
              "name": "local", "port_base": 19000, "port_slot": 8, "slug": "local", "state_dir": "/s"},
    "cc-1": {"account_id": "000000000011", "bootstrap": {}, "compose_project": "p11",
             "core_instance_name": "local-neuronsphere", "db_container": "hmd_db-cc-1",
             "deployment_id": "cc-1", "graph_container": "global-graph-cc-1",
             "k3s_cluster": "ns-cc-1-abc", "kubeconfig": "/k11", "legacy_layout": false,
             "name": "cc-1", "port_base": 19000, "port_slot": 11, "slug": "cc-1", "state_dir": "/s11"},
    "cc-2": {"account_id": "000000000012", "bootstrap": {}, "compose_project": "p12",
             "core_instance_name": "local-neuronsphere", "db_container": "hmd_db-cc-2",
             "deployment_id": "cc-2", "graph_container": "global-graph-cc-2",
             "k3s_cluster": "ns-cc-2-abc", "kubeconfig": "/k12", "legacy_layout": false,
             "name": "cc-2", "port_base": 19000, "port_slot": 12, "slug": "cc-2", "state_dir": "/s12"},
    "cc-3": {"account_id": "000000000013", "bootstrap": {}, "compose_project": "p13",
             "core_instance_name": "local-neuronsphere", "db_container": "hmd_db-cc-3",
             "deployment_id": "cc-3", "graph_container": "global-graph-cc-3",
             "k3s_cluster": "ns-cc-3-abc", "kubeconfig": "/k13", "legacy_layout": false,
             "name": "cc-3", "port_base": 19000, "port_slot": 13, "slug": "cc-3", "state_dir": "/s13"}
  },
  "version": 1
}`

type purgeRecorder struct {
	mu   sync.Mutex
	envs []string
	all  bool
	// heldDuring is the lease list while each purge ran.
	heldDuring []string
}

func (r *purgeRecorder) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.envs...)
}

// stubPurge records purges for one test. Not parallel, for stubStart's reason.
func stubPurge(t *testing.T, home string) *purgeRecorder {
	t.Helper()
	r := &purgeRecorder{}
	prevOne, prevAll := purgeEnvironment, purgeAllEnvironments
	purgeEnvironment = func(_ context.Context, _ *environment.Options, name string) error {
		leases, _, _ := lease.New(home).List()
		r.mu.Lock()
		defer r.mu.Unlock()
		r.envs = append(r.envs, name)
		for _, l := range leases {
			if l.Env == name {
				r.heldDuring = append(r.heldDuring, l.Holder)
			}
		}
		return nil
	}
	purgeAllEnvironments = func(context.Context, *environment.Options, environment.ControlPlaneTeardown) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.all = true
		return nil
	}
	t.Cleanup(func() { purgeEnvironment, purgeAllEnvironments = prevOne, prevAll })
	return r
}

// stalePool is poolRegistry with release times: local 5h, cc-1 3h, cc-2 10m,
// cc-3 1h ago.
func stalePool(t *testing.T) string {
	t.Helper()
	home := registryHome(t, poolRegistry)
	now := time.Now()
	markReleased(t, home, "local", now.Add(-5*time.Hour))
	markReleased(t, home, "cc-1", now.Add(-3*time.Hour))
	markReleased(t, home, "cc-2", now.Add(-10*time.Minute))
	markReleased(t, home, "cc-3", now.Add(-1*time.Hour))
	return home
}

func purgeEnv(home string) func(string) string {
	return fakeEnv(map[string]string{
		"HMD_HOME":                    home,
		"HMD_LOCAL_MS_DEPLOYMENT_URL": "http://127.0.0.1:1/hmd_ms_deployment",
	})
}

func TestPurgeIdleSelectsOnlyPoolEnvironmentsIdleLongEnough(t *testing.T) {
	home := stalePool(t)
	purges := stubPurge(t, home)

	if _, stderr, err := run(t, purgeEnv(home), "env", "purge", "--idle", "2h", "--yes"); err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	// local is idle longest but is a configured member, never a candidate.
	if got := purges.got(); len(got) != 1 || got[0] != "cc-1" {
		t.Errorf("purged %v, want [cc-1]", got)
	}
	if purges.all {
		t.Error("a selector fell through to purging everything")
	}
	if len(purges.heldDuring) != 1 || !strings.Contains(purges.heldDuring[0], "purge") {
		t.Errorf("leases during the purge = %v, want cc-1 held by the purge", purges.heldDuring)
	}
}

func TestPurgeKeepKeepsTheMostRecentlyReleased(t *testing.T) {
	home := stalePool(t)
	purges := stubPurge(t, home)

	if _, stderr, err := run(t, purgeEnv(home), "env", "purge", "--keep", "1", "--yes"); err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	got := purges.got()
	if len(got) != 2 || got[0] != "cc-1" || got[1] != "cc-3" {
		t.Errorf("purged %v, want [cc-1 cc-3], oldest first, keeping cc-2", got)
	}
}

func TestPurgeIdleAndKeepMustBothSelect(t *testing.T) {
	home := stalePool(t)
	purges := stubPurge(t, home)

	// --keep 1 alone would take cc-1 and cc-3; --idle 2h alone, cc-1.
	if _, _, err := run(t, purgeEnv(home), "env", "purge", "--keep", "1", "--idle", "2h", "--yes"); err != nil {
		t.Fatal(err)
	}
	if got := purges.got(); len(got) != 1 || got[0] != "cc-1" {
		t.Errorf("purged %v, want [cc-1]", got)
	}
}

func TestPurgeSelectorSkipsLeasedAndNeverReleasedEnvironments(t *testing.T) {
	home := registryHome(t, poolRegistry)
	markReleased(t, home, "cc-1", time.Now().Add(-3*time.Hour))
	// cc-2 is idle but leased; cc-3 was never released.
	markReleased(t, home, "cc-2", time.Now().Add(-3*time.Hour))
	if _, _, err := run(t, purgeEnv(home), "env", "lease", "acquire", "cc-2", "--pid", "0"); err != nil {
		t.Fatal(err)
	}
	purges := stubPurge(t, home)

	dry, _, err := run(t, purgeEnv(home), "env", "purge", "--idle", "1h", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(dry, "cc-2") || strings.Contains(dry, "cc-3") {
		t.Errorf("dry run %q lists a leased or never-released environment", dry)
	}
	if _, _, err := run(t, purgeEnv(home), "env", "purge", "--idle", "1h", "--yes"); err != nil {
		t.Fatal(err)
	}
	if got := purges.got(); len(got) != 1 || got[0] != "cc-1" {
		t.Errorf("purged %v, want [cc-1]", got)
	}
}

func TestPurgeDryRunListsAndPurgesNothing(t *testing.T) {
	home := stalePool(t)
	purges := stubPurge(t, home)
	m := &manifest.Manifest{Version: manifest.Version, Name: "cc-1", Template: "telemetry",
		Repos: []manifest.Repo{{InstanceName: "their-otel", RepoClassName: "hmd-inf-otel-collector",
			Source: &manifest.Source{Type: manifest.SourceLocal, Path: t.TempDir()}}}}
	if err := m.Save(manifest.DefaultPath(home, "cc-1")); err != nil {
		t.Fatal(err)
	}

	out, _, err := run(t, purgeEnv(home), "env", "purge", "--idle", "2h", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"cc-1", "3h", "telemetry", "their-otel"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run %q lacks %q", out, want)
		}
	}
	if got := purges.got(); len(got) != 0 {
		t.Errorf("--dry-run purged %v", got)
	}
}

func TestPurgeSelectorWithoutYesListsAndRefuses(t *testing.T) {
	home := stalePool(t)
	purges := stubPurge(t, home)

	out, _, err := run(t, purgeEnv(home), "env", "purge", "--idle", "2h")
	if nserr.CodeOf(err) != nserr.Usage || !strings.Contains(err.Error(), "--yes") {
		t.Errorf("error = %v, want a usage error naming --yes", err)
	}
	if !strings.Contains(out, "cc-1") {
		t.Errorf("the refusal %q does not list what it would purge", out)
	}
	if got := purges.got(); len(got) != 0 {
		t.Errorf("purged %v without --yes", got)
	}
}

func TestPurgeSelectorThatSelectsNothingNeverPurgesEverything(t *testing.T) {
	home := stalePool(t)
	purges := stubPurge(t, home)

	out, _, err := run(t, purgeEnv(home), "env", "purge", "--idle", "100h", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "nothing to purge") {
		t.Errorf("output %q, want nothing to purge", out)
	}
	if purges.all || len(purges.got()) != 0 {
		t.Errorf("purged %v (all: %v), want nothing", purges.got(), purges.all)
	}
}

func TestPurgeSelectorFlagsAreChecked(t *testing.T) {
	t.Parallel()
	home := stalePool(t)

	for _, args := range [][]string{
		{"env", "purge", "cc-1", "--idle", "1h", "--yes"},
		{"env", "purge", "--keep", "-1", "--yes"},
		{"env", "purge", "--dry-run"},
	} {
		if _, _, err := run(t, purgeEnv(home), args...); nserr.CodeOf(err) != nserr.Usage {
			t.Errorf("%v = %v, want a usage error", args, err)
		}
	}
}

func TestLeaseListShowsIdlePoolEnvironments(t *testing.T) {
	t.Parallel()
	home := stalePool(t)
	m := &manifest.Manifest{Version: manifest.Version, Name: "cc-1", Template: "telemetry"}
	if err := m.Save(manifest.DefaultPath(home, "cc-1")); err != nil {
		t.Fatal(err)
	}

	out, _, err := run(t, purgeEnv(home), "env", "lease", "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "cc-1") || !strings.Contains(out, "idle 3h") || !strings.Contains(out, "telemetry") {
		t.Errorf("lease list %q does not show cc-1 idle 3h with its template", out)
	}
	if strings.Contains(out, "local") && strings.Contains(out, "idle 5h") {
		t.Errorf("lease list %q shows the member local as a purge candidate", out)
	}
}

func TestPurgeSelectorNeverTakesAConfiguredMemberEvenIfNamedLikeThePools(t *testing.T) {
	home := stalePool(t)
	writePool(t, home, "[pool]\nsize = 4\nmembers = [\"local\", \"cc-1\"]\n")
	purges := stubPurge(t, home)

	if _, _, err := run(t, purgeEnv(home), "env", "purge", "--idle", "30m", "--yes"); err != nil {
		t.Fatal(err)
	}
	if got := purges.got(); len(got) != 1 || got[0] != "cc-3" {
		t.Errorf("purged %v, want [cc-3]: cc-1 is a configured member", got)
	}
}
