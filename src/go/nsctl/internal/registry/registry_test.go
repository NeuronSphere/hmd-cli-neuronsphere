package registry

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeEnv builds a Lookup over a map, so environment-dependent defaults are
// testable without t.Setenv -- which panics under t.Parallel().
func fakeEnv(vars map[string]string) Lookup {
	return func(key string) string { return vars[key] }
}

// writeFixture puts the literal JSON the Python CLI emitted into a temporary
// HMD_HOME and returns both the home and the original bytes.
func writeFixture(t *testing.T) (string, []byte) {
	t.Helper()
	golden, err := os.ReadFile(filepath.Join("testdata", "environments.json"))
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	path := Path(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, golden, 0o644); err != nil {
		t.Fatal(err)
	}
	return home, golden
}

// This is the SPEC003 regression test, and the fixture is the literal JSON the
// Python CLI emits -- captured from a live install, not produced by marshalling
// Go structs. A round-trip fixture would pass however wrong the struct tags
// were; this one fails if a tag, a field or the key ordering drifts.
func TestSaveReproducesThePythonBytes(t *testing.T) {
	t.Parallel()

	home, golden := writeFixture(t)

	r, err := Load(home, fakeEnv(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := r.Save(home); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := os.ReadFile(Path(home))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(golden) {
		t.Errorf("Save did not reproduce the Python bytes.\n--- want ---\n%s\n--- got ---\n%s", golden, got)
	}
}

func TestLoadReadsEveryField(t *testing.T) {
	t.Parallel()

	home, _ := writeFixture(t)
	r, err := Load(home, fakeEnv(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if r.Synthesized {
		t.Error("a registry read from disk must not be marked synthesized")
	}
	if !r.ControlPlane.Bootstrapped {
		t.Error("control plane should be bootstrapped")
	}
	if got, want := r.ControlPlane.ComposeProject, "local_neuronsphere-57aa833c"; got != want {
		t.Errorf("compose project = %q, want %q", got, want)
	}
	if got, want := r.ControlPlane.Network, "neuronsphere_default-57aa833c"; got != want {
		t.Errorf("network = %q, want %q", got, want)
	}
	if got, want := r.DefaultEnv, "local"; got != want {
		t.Errorf("default env = %q, want %q", got, want)
	}
	if got, want := r.Version, 1; got != want {
		t.Errorf("version = %d, want %d", got, want)
	}

	e, err := r.Environment("", fakeEnv(nil))
	if err != nil {
		t.Fatalf("Environment: %v", err)
	}
	checks := []struct {
		field string
		got   string
		want  string
	}{
		{"account_id", e.AccountID, "000000000001"},
		{"compose_project", e.ComposeProject, "ns-57aa833c-env-local"},
		{"core_instance_name", e.CoreInstanceName, "local-neuronsphere"},
		{"db_container", e.DBContainer, "hmd_db-local"},
		{"deployment_id", e.DeploymentID, "local"},
		{"graph_container", e.GraphContainer, "global-graph-local"},
		{"k3s_cluster", e.K3sCluster, "ns-local-57aa833c"},
		{"kubeconfig", e.Kubeconfig, "/Users/aburg/hmdtr1/.cache/environments/local/k3s/kubeconfig"},
		{"name", e.Name, "local"},
		{"slug", e.Slug, "local"},
		{"state_dir", e.StateDir, "/Users/aburg/hmdtr1/.cache/environments/local"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.field, c.got, c.want)
		}
	}
	if e.LegacyLayout {
		t.Error("legacy_layout should be false")
	}
	if e.PortSlot != 8 || e.PortBase != 19000 {
		t.Errorf("port slot/base = %d/%d, want 8/19000", e.PortSlot, e.PortBase)
	}
	if !e.Bootstrapped() {
		t.Error("environment should report bootstrapped from its csd_nid")
	}
	if got, want := e.Bootstrap["k3s_uid"], "a3db9972-f83c-4f0a-b147-8f7e73422eec"; got != want {
		t.Errorf("bootstrap k3s_uid = %v, want %q", got, want)
	}
}

func TestDerivedPorts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                                string
		slot, base                          int
		floci, trino, graph, spare, k3sPort int
	}{
		// Slot 0's spare is the Deployment GUI's published 19003, which is why
		// the stride cannot be widened.
		{"slot 0", 0, 19000, 19000, 19001, 19002, 19003, 19064},
		{"the live environment, slot 8", 8, 19000, 19032, 19033, 19034, 19035, 19072},
		{"the last slot", 15, 19000, 19060, 19061, 19062, 19063, 19079},
		{"an unset port base falls back to the default", 1, 0, 19004, 19005, 19006, 19007, 19065},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := Environment{PortSlot: tt.slot, PortBase: tt.base}
			if got := e.FlociPort(); got != tt.floci {
				t.Errorf("FlociPort = %d, want %d", got, tt.floci)
			}
			if got := e.TrinoPort(); got != tt.trino {
				t.Errorf("TrinoPort = %d, want %d", got, tt.trino)
			}
			if got := e.GraphPort(); got != tt.graph {
				t.Errorf("GraphPort = %d, want %d", got, tt.graph)
			}
			if got := e.SparePort(); got != tt.spare {
				t.Errorf("SparePort = %d, want %d", got, tt.spare)
			}
			if got := e.K3sPort(); got != tt.k3sPort {
				t.Errorf("K3sPort = %d, want %d", got, tt.k3sPort)
			}
		})
	}
}

// The whole published band is 19000-19111: 16 slots of 4, then 16 k3s ports,
// then the shared UI band. A gap or an overlap here would collide two
// environments on one port.
func TestThePortBandIsContiguousAndDoesNotOverlap(t *testing.T) {
	t.Parallel()

	seen := map[int]string{}
	claim := func(owner, name string, port int) {
		if prev, dup := seen[port]; dup {
			t.Errorf("port %d is claimed by both %s and %s %s", port, prev, owner, name)
		}
		seen[port] = name
		if port < 19000 || port > 19111 {
			t.Errorf("port %d (%s %s) is outside the published 19000-19111 band", port, owner, name)
		}
	}
	for slot := 0; slot < MaxEnvs; slot++ {
		e := Environment{PortSlot: slot, PortBase: DefaultPortBase}
		for name, port := range map[string]int{
			"floci": e.FlociPort(), "trino": e.TrinoPort(),
			"graph": e.GraphPort(), "spare": e.SparePort(), "k3s": e.K3sPort(),
		} {
			claim(fmt.Sprintf("slot %d", slot), name, port)
		}
	}
	// The UI band is shared, so it is claimed once rather than per slot.
	shared := Environment{PortBase: DefaultPortBase}
	for idx := 0; idx < MaxUIPorts; idx++ {
		claim("shared", fmt.Sprintf("ui%d", idx), shared.UIPortAt(idx))
	}
	if want := MaxEnvs*5 + MaxUIPorts; len(seen) != want {
		t.Errorf("claimed %d distinct ports, want %d", len(seen), want)
	}
}

// The UI band does not move with the port slot -- it is shared, and a per-slot
// band would have made hmd_proxy publish 216 ports to serve sixteen
// environments nobody runs at once.
func TestTheUIBandIsSharedAcrossEnvironments(t *testing.T) {
	t.Parallel()

	a := Environment{PortSlot: 0, PortBase: DefaultPortBase}
	b := Environment{PortSlot: 9, PortBase: DefaultPortBase}
	if a.UIPortAt(0) != b.UIPortAt(0) {
		t.Errorf("the UI band moved with the slot: %d vs %d", a.UIPortAt(0), b.UIPortAt(0))
	}
	if got, want := a.UIPortAt(0), 19080; got != want {
		t.Errorf("UI band starts at %d, want %d (immediately above the k3s band)", got, want)
	}
}

// A UI's port is a bookmark, so it has to survive a restart and a re-creation.
func TestAUIPortIsStableForAHost(t *testing.T) {
	t.Parallel()

	reg := &Registry{Environments: map[string]Environment{}}
	e := Environment{Slug: "local", PortSlot: 0, PortBase: DefaultPortBase}
	first, err := reg.AssignUIPort(&e, "airflow.local.neuronsphere.io")
	if err != nil {
		t.Fatalf("AssignUIPort: %v", err)
	}
	again, err := reg.AssignUIPort(&e, "airflow.local.neuronsphere.io")
	if err != nil {
		t.Fatalf("AssignUIPort again: %v", err)
	}
	if first != again {
		t.Errorf("port moved on re-assignment: %d then %d", first, again)
	}

	// An environment rebuilt from the persisted map reports the same port.
	rebuilt := Environment{PortSlot: 0, PortBase: DefaultPortBase, UIPorts: e.UIPorts}
	if got, ok := rebuilt.UIPort("airflow.local.neuronsphere.io"); !ok || got != first {
		t.Errorf("UIPort after reload = %d, %v; want %d, true", got, ok, first)
	}
	// A host nothing has assigned is not silently given someone else's port.
	if got, ok := rebuilt.UIPort("superset.local.neuronsphere.io"); ok {
		t.Errorf("UIPort for an unassigned host = %d, true; want not found", got)
	}
}

// Two hosts that hash to the same offset must not land on one port.
func TestUIPortsDoNotCollide(t *testing.T) {
	t.Parallel()

	reg := &Registry{Environments: map[string]Environment{}}
	e := Environment{Slug: "dev", PortSlot: 3, PortBase: DefaultPortBase}
	seen := map[int]string{}
	for _, host := range []string{
		"airflow.local.neuronsphere.io", "argo.local.neuronsphere.io",
		"superset.local.neuronsphere.io", "web.local.neuronsphere.io",
		"trino.local.neuronsphere.io", "clickhouse.local.neuronsphere.io",
		"jupyter.local.neuronsphere.io", "registry.local.neuronsphere.io",
	} {
		port, err := reg.AssignUIPort(&e, host)
		if err != nil {
			t.Fatalf("AssignUIPort(%q): %v", host, err)
		}
		if prev, dup := seen[port]; dup {
			t.Errorf("port %d assigned to both %s and %s", port, prev, host)
		}
		seen[port] = host
		if lo, hi := e.UIPortAt(0), e.UIPortAt(MaxUIPorts-1); port < lo || port > hi {
			t.Errorf("port %d for %s is outside the UI band %d-%d", port, host, lo, hi)
		}
	}
}

// The band is shared, so an environment must not be handed a port another
// environment is already using -- which is reachable today, because two
// environments deploying one chart produce the same Ingress hostname.
func TestUIPortsDoNotCollideAcrossEnvironments(t *testing.T) {
	t.Parallel()

	reg := &Registry{Environments: map[string]Environment{}}
	a := Environment{Slug: "local", PortSlot: 0, PortBase: DefaultPortBase}
	aPort, err := reg.AssignUIPort(&a, "airflow.local.neuronsphere.io")
	if err != nil {
		t.Fatalf("AssignUIPort a: %v", err)
	}
	reg.Environments["local"] = a

	b := Environment{Slug: "dev", PortSlot: 1, PortBase: DefaultPortBase}
	bPort, err := reg.AssignUIPort(&b, "airflow.local.neuronsphere.io")
	if err != nil {
		t.Fatalf("AssignUIPort b: %v", err)
	}
	if aPort == bPort {
		t.Errorf("two environments were given the same UI port %d", aPort)
	}
}

// Exhaustion is reported rather than absorbed -- a UI with no port must say so,
// not go silently unrouted.
func TestUIPortExhaustionIsReported(t *testing.T) {
	t.Parallel()

	reg := &Registry{Environments: map[string]Environment{}}
	e := Environment{Slug: "local", PortSlot: 0, PortBase: DefaultPortBase}
	for i := 0; i < MaxUIPorts; i++ {
		if _, err := reg.AssignUIPort(&e, fmt.Sprintf("ui%d.local.neuronsphere.io", i)); err != nil {
			t.Fatalf("AssignUIPort %d: %v", i, err)
		}
	}
	_, err := reg.AssignUIPort(&e, "one-too-many.local.neuronsphere.io")
	if err == nil {
		t.Fatal("assigning one port too many succeeded; want an error naming the limit")
	}
	if !strings.Contains(err.Error(), "one-too-many.local.neuronsphere.io") {
		t.Errorf("error does not name the host that went unrouted: %v", err)
	}
}

func TestValidateSlug(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"a simple name", "dev", "dev", false},
		{"case and whitespace are normalised", "  Dev  ", "dev", false},
		{"hyphens are allowed after the first character", "my-env-2", "my-env-2", false},
		{"digits may lead", "2nd", "2nd", false},
		{"sixteen characters is the limit", "abcdefghijklmnop", "abcdefghijklmnop", false},
		{"seventeen is too many", "abcdefghijklmnopq", "", true},
		{"empty is rejected", "", "", true},
		{"whitespace only is rejected", "   ", "", true},
		{"a leading hyphen is rejected", "-dev", "", true},
		{"underscores are rejected", "my_env", "", true},
		{"uppercase normalises rather than failing", "DEV", "dev", false},
		{"a reserved slug would shadow a control-plane route", "api", "", true},
		{"hmd_ms_deployment is reserved", "hmd_ms_deployment", "", true},
		{"argo is reserved", "argo", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ValidateSlug(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ValidateSlug(%q) = %q, want an error", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateSlug(%q): %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("ValidateSlug(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestAllocatePortSlotPrefersTheNameHash(t *testing.T) {
	t.Parallel()

	r := &Registry{Environments: map[string]Environment{}}
	first, err := r.AllocatePortSlot("dev")
	if err != nil {
		t.Fatal(err)
	}
	// Stable across calls, so the same name lands on the same ports.
	again, err := r.AllocatePortSlot("dev")
	if err != nil {
		t.Fatal(err)
	}
	if first != again {
		t.Errorf("slot for %q moved between calls: %d then %d", "dev", first, again)
	}
	if first < 0 || first >= MaxEnvs {
		t.Errorf("slot %d is outside 0..%d", first, MaxEnvs-1)
	}
}

func TestAllocatePortSlotFallsBackWhenTaken(t *testing.T) {
	t.Parallel()

	taken, err := (&Registry{Environments: map[string]Environment{}}).AllocatePortSlot("dev")
	if err != nil {
		t.Fatal(err)
	}
	r := &Registry{Environments: map[string]Environment{
		"other": {Slug: "other", PortSlot: taken},
	}}
	got, err := r.AllocatePortSlot("dev")
	if err != nil {
		t.Fatal(err)
	}
	if got == taken {
		t.Errorf("allocated the taken slot %d", taken)
	}
}

func TestAllocatePortSlotRefusesWhenFull(t *testing.T) {
	t.Parallel()

	envs := map[string]Environment{}
	for i := 0; i < MaxEnvs; i++ {
		envs[string(rune('a'+i))] = Environment{PortSlot: i}
	}
	r := &Registry{Environments: envs}
	if _, err := r.AllocatePortSlot("one-too-many"); err == nil {
		t.Error("allocated a 17th slot, want a refusal")
	}
}

func TestAllocateAccountIDSkipsTheControlPlane(t *testing.T) {
	t.Parallel()

	r := &Registry{Environments: map[string]Environment{}}
	if got, want := r.AllocateAccountID(), "000000000001"; got != want {
		t.Errorf("first account = %q, want %q (never the control plane's %q)", got, want, ControlPlaneAccountID)
	}

	r.Environments["a"] = Environment{AccountID: "000000000001"}
	r.Environments["b"] = Environment{AccountID: "000000000002"}
	if got, want := r.AllocateAccountID(), "000000000003"; got != want {
		t.Errorf("next account = %q, want %q", got, want)
	}
}

func TestUnknownEnvironmentNamesWhatExists(t *testing.T) {
	t.Parallel()

	r := &Registry{
		DefaultEnv:   "local",
		Environments: map[string]Environment{"local": {Slug: "local"}, "dev": {Slug: "dev"}},
	}
	_, err := r.Environment("typo", fakeEnv(nil))
	if err == nil {
		t.Fatal("resolving a missing environment succeeded, want an error")
	}
	for _, want := range []string{"typo", "dev", "local"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestEnvironmentResolutionOrder(t *testing.T) {
	t.Parallel()

	r := &Registry{
		DefaultEnv:   "local",
		Environments: map[string]Environment{"local": {Slug: "local"}, "dev": {Slug: "dev"}},
	}

	tests := []struct {
		name string
		arg  string
		env  map[string]string
		want string
	}{
		{"an explicit name wins", "dev", map[string]string{"HMD_LOCAL_ENV": "local"}, "dev"},
		{"HMD_LOCAL_ENV is next", "", map[string]string{"HMD_LOCAL_ENV": "dev"}, "dev"},
		{"the registry default is last", "", nil, "local"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e, err := r.Environment(tt.arg, fakeEnv(tt.env))
			if err != nil {
				t.Fatal(err)
			}
			if e.Slug != tt.want {
				t.Errorf("resolved %q, want %q", e.Slug, tt.want)
			}
		})
	}
}

func TestLoadOnAnEmptyHomeIsNotBootstrapped(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	r, err := Load(home, fakeEnv(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !r.Synthesized {
		t.Error("a home with no registry file should yield a synthesized view")
	}
	if len(r.Environments) != 0 {
		t.Errorf("environments = %v, want none", r.Environments)
	}
	if r.ControlPlane.Bootstrapped {
		t.Error("an empty home must not report a bootstrapped control plane")
	}
	// The derived fallbacks still have to be filled in, or every "is it up"
	// probe answers no for a platform that is plainly running.
	if r.ControlPlane.Network == "" || r.ControlPlane.ComposeProject == "" {
		t.Errorf("derived names are empty: %+v", r.ControlPlane)
	}
}

// SPEC003 calls this out as the case re-derivation gets wrong: a pre-multi-env
// install whose containers are named `hmd_db` and match no current rule.
func TestLoadSynthesizesTheLegacyLayoutFromABootstrapMarker(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	marker := LegacyMarkerPath(home)
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte(`{"csd_nid": "old-nid"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	r, err := Load(home, fakeEnv(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !r.ControlPlane.Bootstrapped {
		t.Error("a legacy install is bootstrapped")
	}
	e, err := r.Environment("", fakeEnv(nil))
	if err != nil {
		t.Fatalf("Environment: %v", err)
	}
	if !e.LegacyLayout {
		t.Error("the synthesized environment must carry legacy_layout")
	}
	if e.DBContainer != "hmd_db" || e.GraphContainer != "global-graph" {
		t.Errorf("legacy containers = %q/%q, want hmd_db/global-graph", e.DBContainer, e.GraphContainer)
	}
	if e.AccountID != ControlPlaneAccountID {
		t.Errorf("legacy account = %q, want the control plane's %q", e.AccountID, ControlPlaneAccountID)
	}
	if e.StateDir != home {
		t.Errorf("legacy state dir = %q, want the home itself %q", e.StateDir, home)
	}
	if got := e.Bootstrap["csd_nid"]; got != "old-nid" {
		t.Errorf("marker was not carried through: %v", e.Bootstrap)
	}
	// Read-only commands must not create state.
	if !r.Synthesized {
		t.Error("the synthesized view must say so, since nothing was persisted")
	}
	if _, err := os.Stat(Path(home)); !os.IsNotExist(err) {
		t.Error("Load wrote a registry file; a read must not create state")
	}
}

// Floci state alone is not evidence of a completed bootstrap.
//
// nsctl's own bootstrap starts Floci as its first step, so a bootstrap that
// fails partway leaves exactly this state behind. Reading it as "already
// bootstrapped" made the retry skip the bootstrap and report a control plane
// ready that had no database and no services. The two cases are
// indistinguishable from the directory, so the tie-break is which mistake is
// worse: re-bootstrapping is idempotent, skipping is not recoverable without
// the user noticing.
func TestPersistedFlociStateAloneDoesNotMeanBootstrapped(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	data := filepath.Join(home, "floci", "data")
	if err := os.MkdirAll(data, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "something.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	r, err := Load(home, fakeEnv(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if r.ControlPlane.Bootstrapped {
		t.Error("Floci state left by a failed bootstrap was read as a completed one")
	}
}

// A real legacy install is recognised by its marker, which is what actually
// records that a pre-multi-env `up` completed.
func TestLegacyMarkerStillMigrates(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	marker := LegacyMarkerPath(home)
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte(`{"csd_nid": "nid"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	r, err := Load(home, fakeEnv(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(r.Environments) != 1 {
		t.Fatalf("environments = %v, want the synthesized default", r.Environments)
	}
	if !r.ControlPlane.Bootstrapped {
		t.Error("a marked legacy install was not recognised as bootstrapped")
	}
}

func TestLoadHonoursTheNetworkAndProjectOverrides(t *testing.T) {
	t.Parallel()

	r, err := Load(t.TempDir(), fakeEnv(map[string]string{
		"HMD_LOCAL_DOCKER_NETWORK":       "my-net",
		"HMD_LOCAL_COMPOSE_PROJECT_NAME": "my-project",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if r.ControlPlane.Network != "my-net" {
		t.Errorf("network = %q, want the override", r.ControlPlane.Network)
	}
	if r.ControlPlane.ComposeProject != "my-project" {
		t.Errorf("compose project = %q, want the override", r.ControlPlane.ComposeProject)
	}
}

func TestLoadReportsAMalformedRegistry(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	path := Path(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(home, fakeEnv(nil)); err == nil {
		t.Error("Load succeeded on malformed JSON, want an error naming the file")
	}
}

// A field hmd adds tomorrow must not stop nsctl reading the ones it already
// understands -- but it must also not be silently dropped on a write-back,
// which is why nsctl only writes registries it fully round-trips.
func TestUnknownKeysAreIgnoredOnLoad(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	path := Path(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{
  "control_plane": {"bootstrapped": true, "compose_project": "p", "floci_data_dir": "/d", "network": "n", "future_field": 1},
  "default_env": "local",
  "environments": {"local": {"account_id": "000000000001", "name": "local", "slug": "local", "port_slot": 0, "port_base": 19000, "floci_container": "floci", "floci_alias": "neuronsphere"}},
  "version": 1
}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	r, err := Load(home, fakeEnv(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// floci_container/floci_alias are no longer registry fields: every
	// environment shares the one Floci and is told apart by account_id.
	e, err := r.Environment("local", fakeEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	if e.AccountID != "000000000001" {
		t.Errorf("account = %q", e.AccountID)
	}
}

func TestStateDirs(t *testing.T) {
	t.Parallel()

	e := Environment{
		StateDir:   "/home/.cache/environments/dev",
		Kubeconfig: "/home/.cache/environments/dev/k3s/kubeconfig",
	}
	want := []string{
		"/home/.cache/environments/dev/postgresql/data",
		"/home/.cache/environments/dev/graph_db",
		"/home/.cache/environments/dev/k3s",
	}
	got := e.StateDirs()
	if len(got) != len(want) {
		t.Fatalf("StateDirs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("StateDirs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	// The environment's Floci state lives in the control plane's data dir now,
	// so it must not be created here.
	for _, d := range got {
		if strings.Contains(d, "floci") {
			t.Errorf("StateDirs() includes the Floci data dir: %q", d)
		}
	}
}

func TestBootstrappedNeedsACsdNid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		b    map[string]any
		want bool
	}{
		{"no bootstrap record", nil, false},
		{"an empty record", map[string]any{}, false},
		{"an empty csd_nid", map[string]any{"csd_nid": ""}, false},
		{"a k3s_uid alone is not enough", map[string]any{"k3s_uid": "u"}, false},
		{"a real csd_nid", map[string]any{"csd_nid": "ms-deployment-local"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := Environment{Bootstrap: tt.b}
			if got := e.Bootstrapped(); got != tt.want {
				t.Errorf("Bootstrapped() = %v, want %v", got, tt.want)
			}
		})
	}
}

// A never-bootstrapped environment must serialise the way the Python dataclass
// does. asdict turns its default_factory=dict into {}, so the key is present
// and empty -- not omitted, and not Go's nil-map `null`.
func TestSaveWritesAnEmptyBootstrapObject(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	r := &Registry{
		ControlPlane: ControlPlane{ComposeProject: "p", FlociDataDir: "/d", Network: "n"},
		DefaultEnv:   "local",
		Environments: map[string]Environment{
			"dev": {Name: "dev", Slug: "dev", AccountID: "000000000001", PortBase: 19000},
		},
		Version: 1,
	}
	if err := r.Save(home); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := os.ReadFile(Path(home))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `"bootstrap": {}`) {
		t.Errorf("bootstrap was not written as an empty object:\n%s", got)
	}
	if strings.Contains(string(got), `"bootstrap": null`) {
		t.Errorf("bootstrap was written as null:\n%s", got)
	}
}

// Save must survive being handed an empty registry, which is what `env add`
// starts from on a fresh HMD_HOME.
func TestSaveOnAnEmptyRegistry(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	r := &Registry{ControlPlane: ControlPlane{Network: "n"}, DefaultEnv: "local", Version: 1}
	if err := r.Save(home); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := os.ReadFile(Path(home))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `"environments": {}`) {
		t.Errorf("environments was not written as an empty object:\n%s", got)
	}
	if r.Synthesized {
		t.Error("Save should clear Synthesized")
	}
}

func TestEnsureFirstEnvironmentRegistersOnAnEmptyRegistry(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	reg, err := Load(home, nil)
	if err != nil {
		t.Fatal(err)
	}
	env, created, err := reg.EnsureFirstEnvironment(home, "", nil)
	if err != nil {
		t.Fatalf("EnsureFirstEnvironment() error = %v", err)
	}
	if !created {
		t.Fatal("EnsureFirstEnvironment() created = false, want true")
	}
	if env.Slug != DefaultEnvName {
		t.Errorf("slug = %q, want %q", env.Slug, DefaultEnvName)
	}
	if env.AccountID == ControlPlaneAccountID {
		t.Errorf("account = %s, which is the control plane's", env.AccountID)
	}

	// Persisted, not just returned: an environment only this process knows
	// about is one whose containers the next invocation cannot name.
	reread, err := Load(home, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reread.Synthesized {
		t.Error("the registry was not written")
	}
	if _, ok := reread.Environments[DefaultEnvName]; !ok {
		t.Errorf("environments = %v, want %q", reread.Names(), DefaultEnvName)
	}
	if reread.DefaultEnv != DefaultEnvName {
		t.Errorf("DefaultEnv = %q, want %q", reread.DefaultEnv, DefaultEnvName)
	}
}

func TestEnsureFirstEnvironmentHonoursAName(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	reg, err := Load(home, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Normalised through ValidateSlug, exactly as `env add` would have.
	env, created, err := reg.EnsureFirstEnvironment(home, "Dev-1", nil)
	if err != nil {
		t.Fatalf("EnsureFirstEnvironment() error = %v", err)
	}
	if !created || env.Slug != "dev-1" {
		t.Fatalf("EnsureFirstEnvironment() = %q, %v, want dev-1, true", env.Slug, created)
	}
	// DefaultEnv must follow, or the registry names a default that is not
	// there -- applyDefaults always sets it to `local`, whatever was created.
	if reg.DefaultEnv != "dev-1" {
		t.Errorf("DefaultEnv = %q, want dev-1", reg.DefaultEnv)
	}
}

func TestEnsureFirstEnvironmentPrefersHMDLocalEnv(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	reg, err := Load(home, nil)
	if err != nil {
		t.Fatal(err)
	}
	lookup := func(k string) string {
		if k == "HMD_LOCAL_ENV" {
			return "scratch"
		}
		return ""
	}
	// The same order Environment resolves in, so `env start` creates exactly
	// the environment it would then have gone looking for.
	env, _, err := reg.EnsureFirstEnvironment(home, "", lookup)
	if err != nil {
		t.Fatalf("EnsureFirstEnvironment() error = %v", err)
	}
	if env.Slug != "scratch" {
		t.Errorf("slug = %q, want scratch", env.Slug)
	}
}

func TestEnsureFirstEnvironmentIsANoOpOnceAnyExists(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	reg, err := Load(home, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.NewEnvironment(home, "dev", nil); err != nil {
		t.Fatal(err)
	}
	// A second name with one already registered is a typo, not a first run.
	env, created, err := reg.EnsureFirstEnvironment(home, "prod", nil)
	if err != nil {
		t.Fatalf("EnsureFirstEnvironment() error = %v", err)
	}
	if created || env != nil {
		t.Errorf("EnsureFirstEnvironment() = %v, %v, want nil, false", env, created)
	}
	if names := reg.Names(); len(names) != 1 {
		t.Errorf("environments = %v, want dev alone", names)
	}
}

func TestEnsureFirstEnvironmentRejectsABadName(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	reg, err := Load(home, nil)
	if err != nil {
		t.Fatal(err)
	}
	// `api` is a control-plane route: an environment there would swallow it.
	if _, created, err := reg.EnsureFirstEnvironment(home, "api", nil); err == nil {
		t.Errorf("EnsureFirstEnvironment(api) created = %v, want an error", created)
	}
	if _, err := os.Stat(Path(home)); err == nil {
		t.Error("a rejected name still wrote the registry")
	}
}

// The control plane's published ports are chosen once and then persisted, so a
// URL a user bookmarked keeps meaning what it meant.
func TestControlPlanePortsDefaultAndPersist(t *testing.T) {
	t.Parallel()

	var cp ControlPlane
	for name, want := range map[string]int{
		PortHTTP: 80, PortFloci: 4566, PortTrino: 18080,
		PortDNS: 19153, PortEnvBase: 19000,
	} {
		if got := cp.Port(name); got != want {
			t.Errorf("default Port(%q) = %d, want %d", name, got, want)
		}
	}

	// A recorded choice wins over the default.
	cp.Ports = map[string]int{PortHTTP: 8080}
	if got := cp.Port(PortHTTP); got != 8080 {
		t.Errorf("Port(http) = %d, want the recorded 8080", got)
	}
	// ...and the others still fall back.
	if got := cp.Port(PortFloci); got != 4566 {
		t.Errorf("Port(floci) = %d, want the default 4566", got)
	}
	// An unknown name is a programming error, not a silent zero.
	if got := cp.Port("nonsense"); got != 0 {
		t.Errorf("Port(nonsense) = %d, want 0", got)
	}
}

// The published range has to be derived from whichever base was chosen, or a
// moved band would publish the old one.
func TestEnvPortRangeFollowsTheChosenBase(t *testing.T) {
	t.Parallel()

	cp := ControlPlane{Ports: map[string]int{PortEnvBase: 20000}}
	lo, hi := cp.EnvPortRange()
	if lo != 20000 {
		t.Errorf("range starts at %d, want 20000", lo)
	}
	if want := 20000 + MaxEnvs*(PortsPerEnv+1) + MaxUIPorts - 1; hi != want {
		t.Errorf("range ends at %d, want %d", hi, want)
	}
	// Width is what matters, and it must not change with the base.
	base := ControlPlane{}
	blo, bhi := base.EnvPortRange()
	if hi-lo != bhi-blo {
		t.Errorf("width changed with the base: %d vs %d", hi-lo, bhi-blo)
	}
}
