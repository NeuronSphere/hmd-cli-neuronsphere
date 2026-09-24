package status

import (
	"context"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
)

func roles(snap Environment) []string {
	var out []string
	for _, c := range snap.Containers {
		out = append(out, c.Role)
	}
	return out
}

func has(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// NERD014 SPEC007: rows and routes name only what the mode runs.
func TestEnvironmentStatusListsOnlyWhatTheSubstrateRuns(t *testing.T) {
	t.Parallel()

	d := &fakeDocker{running: map[string]bool{"floci": true}}
	cases := []struct {
		mode          manifest.Substrate
		rows, noRows  []string
		routes, noRts []string
	}{
		{manifest.SubstrateNone,
			[]string{"floci"}, []string{"db", "graph", "k3s"},
			[]string{"services", "floci"}, []string{"dbaccount", "trino", "k3s"}},
		{manifest.SubstrateCore,
			[]string{"floci", "db", "graph"}, []string{"k3s"},
			[]string{"services", "floci", "dbaccount"}, []string{"trino", "k3s"}},
		{manifest.SubstrateFull,
			[]string{"floci", "db", "graph", "k3s"}, nil,
			[]string{"services", "floci", "dbaccount", "trino", "k3s"}, nil},
	}
	for _, c := range cases {
		// Trino and dbaccount routed throughout: this case table is about what
		// a *mode* runs, and TestTrinoRouteOnlyWhenRouted and
		// TestDBAccountRouteOnlyWhenRouted cover the routing conditions.
		r := &Reporter{Docker: d, Lookup: fakeEnv(nil),
			Substrate:     func(string) manifest.Substrate { return c.mode },
			Routed:        func(string, int) bool { return true },
			RoutedService: func(string, string) bool { return true }}
		snap := r.EnvironmentStatus(context.Background(), liveEnv())
		if snap.Substrate != c.mode {
			t.Errorf("%s: snapshot carries %q", c.mode, snap.Substrate)
		}
		got := roles(snap)
		for _, want := range c.rows {
			if !has(got, want) {
				t.Errorf("%s: missing row %s in %v", c.mode, want, got)
			}
		}
		for _, absent := range c.noRows {
			if has(got, absent) {
				t.Errorf("%s: row %s should not be listed, got %v", c.mode, absent, got)
			}
		}
		for _, want := range c.routes {
			if _, ok := snap.Routes[want]; !ok {
				t.Errorf("%s: missing route %s in %v", c.mode, want, snap.RouteOrder)
			}
		}
		for _, absent := range c.noRts {
			if _, ok := snap.Routes[absent]; ok {
				t.Errorf("%s: route %s should not be listed, got %v", c.mode, absent, snap.RouteOrder)
			}
		}
	}
}

// A nil hook is full for every environment: the behaviour every caller had
// before the mode existed.
func TestEnvironmentStatusDefaultsToFull(t *testing.T) {
	t.Parallel()

	r := &Reporter{Docker: &fakeDocker{}, Lookup: fakeEnv(nil)}
	snap := r.EnvironmentStatus(context.Background(), liveEnv())
	if snap.Substrate != manifest.SubstrateFull || !has(roles(snap), "k3s") {
		t.Errorf("default snapshot = %q %v, want full with a k3s row", snap.Substrate, roles(snap))
	}
}

// The shared Floci is the control plane's: an environment that owns nothing
// running is stopped, whatever Floci is doing.
func TestRunningDoesNotCountTheSharedFloci(t *testing.T) {
	t.Parallel()

	onlyFloci := Environment{Containers: []Container{{Role: "floci", Running: true}}}
	if onlyFloci.Running() {
		t.Error("an environment with only the shared Floci up reads as running")
	}
	withDB := Environment{Containers: []Container{{Role: "floci", Running: true}, {Role: "db", Running: true}}}
	if !withDB.Running() {
		t.Error("an environment with its own database up reads as stopped")
	}
}
