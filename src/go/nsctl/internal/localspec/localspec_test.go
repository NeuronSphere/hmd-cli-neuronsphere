package localspec

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// writeManifest puts a BACON manifest where Load looks for it and returns the
// repo directory.
func writeManifest(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "meta-data", "manifest.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The manifest every activation test reads. Deliberately the shape a real one
// has: `required` is the *string* "true"/"false" in all 517 dependency blocks
// across the workspace, never a bool.
const fixture = `{
  "name": "hmd-ms-myapi",
  "deploy": {
    "dependencies": {
      "base-vpc":       {"repo_class_name": "hmd-vpc",               "required": "true",  "version_spec": "~= 0.1"},
      "app-store":      {"repo_class_name": "hmd-inf-s3bucket",      "required": "true",  "version_spec": "0.1.13"},
      "neptune-db":     {"repo_class_name": "hmd-inf-neptune",       "required": "true",  "version_spec": "~= 0.1", "instance_name": "global-graph"},
      "otel-collector": {"repo_class_name": "hmd-inf-otel-collector","required": "false", "version_spec": "~= 0.1"},
      "authorizer":     {"repo_class_name": "hmd-inf-opa-authorizer","required": "false", "version_spec": "~= 0.1"}
    }
  },
  "local": {
    "version": 1,
    "default_profiles": ["transforms"],
    "repos": [
      {"instance_name": "app-db",       "repo_class_name": "hmd-inf-postgres",  "version_spec": "~= 0.8"},
      {"instance_name": "ms-transform", "repo_class_name": "hmd-ms-transform",  "version_spec": "~= 0.5",
       "profiles": ["transforms", "full"], "dependencies": {"librarian": "data-lib"}},
      {"instance_name": "data-lib",     "repo_class_name": "hmd-ms-librarian",  "version_spec": "~= 0.2",
       "profiles": ["transforms", "full"]}
    ],
    "dependencies": {
      "otel-collector": {"profiles": ["full", "telemetry"]}
    }
  }
}`

func TestLoadReadsBothHalves(t *testing.T) {
	t.Parallel()

	m, err := Load(writeManifest(t, fixture))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if m.RepoClassName != "hmd-ms-myapi" {
		t.Errorf("RepoClassName = %q, want hmd-ms-myapi", m.RepoClassName)
	}
	if len(m.Dependencies) != 5 {
		t.Fatalf("read %d dependencies, want 5", len(m.Dependencies))
	}
	// Sorted by role, so a lock generated twice from one manifest is the same
	// bytes. Go's map iteration order is not.
	var roles []string
	for _, d := range m.Dependencies {
		roles = append(roles, d.Role)
	}
	want := []string{"app-store", "authorizer", "base-vpc", "neptune-db", "otel-collector"}
	if !reflect.DeepEqual(roles, want) {
		t.Errorf("roles = %v, want %v", roles, want)
	}

	byRole := map[string]Dependency{}
	for _, d := range m.Dependencies {
		byRole[d.Role] = d
	}
	if !byRole["base-vpc"].Required {
		t.Error(`required: "true" did not read as required`)
	}
	if byRole["authorizer"].Required {
		t.Error(`required: "false" read as required`)
	}
	// Only 9 of 517 blocks in the workspace carry one, and it is tier three of
	// the naming order when they do.
	if byRole["neptune-db"].InstanceName != "global-graph" {
		t.Errorf("neptune-db instance_name = %q, want global-graph", byRole["neptune-db"].InstanceName)
	}
	if m.Local.DefaultProfiles[0] != "transforms" || len(m.Local.Repos) != 3 {
		t.Errorf("local section did not read: %+v", m.Local)
	}
}

// A manifest with no `local` section is not an error: deploy.dependencies alone
// is still something to lock, and most repos will never grow one.
func TestLoadWithoutALocalSection(t *testing.T) {
	t.Parallel()

	m, err := Load(writeManifest(t, `{"name":"hmd-ms-bare","deploy":{"dependencies":{
		"base-vpc": {"repo_class_name":"hmd-vpc","required":"true","version_spec":"~= 0.1"}}}}`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(m.Dependencies) != 1 || len(m.Local.Repos) != 0 {
		t.Errorf("got %d deps and %d local repos, want 1 and 0", len(m.Dependencies), len(m.Local.Repos))
	}
	if got := m.Activate(nil); len(got) != 1 {
		t.Errorf("activated %d wants, want the one required dependency", len(got))
	}
}

// docker compose's semantics, which is the whole reason profiles were chosen
// over a second grouping concept: an entry with no `profiles` key always starts,
// one with a `profiles` key starts only when one of them is activated, and lean
// is what activating nothing gives you -- so no author has to declare it.
func TestActivateFollowsComposeSemantics(t *testing.T) {
	t.Parallel()

	m, err := Load(writeManifest(t, fixture))
	if err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		name     string
		profiles []string
		want     []string // Want.Key, sorted
	}{
		// Lean. Every required dependency, every ungated companion, and none of
		// the gated ones -- with nothing declared to say so.
		{"none", nil, []string{"app-db", "app-store", "base-vpc", "neptune-db"}},
		{"one profile", []string{"transforms"},
			[]string{"app-db", "app-store", "base-vpc", "data-lib", "ms-transform", "neptune-db"}},
		// "full" reaches the same two companions by the other name, and also the
		// gated optional dependency.
		{"two profiles", []string{"full", "telemetry"},
			[]string{"app-db", "app-store", "base-vpc", "data-lib", "ms-transform", "neptune-db", "otel-collector"}},
		// The gate names only this optional dependency. `authorizer` is optional
		// and unmentioned, so it keeps today's behaviour: never declared.
		{"a profile only the gate names", []string{"telemetry"},
			[]string{"app-db", "app-store", "base-vpc", "neptune-db", "otel-collector"}},
		{"an unknown profile", []string{"nope"},
			[]string{"app-db", "app-store", "base-vpc", "neptune-db"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var got []string
			for _, w := range m.Activate(tt.profiles) {
				got = append(got, w.Key)
			}
			sort.Strings(got)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Activate(%v) = %v, want %v", tt.profiles, got, tt.want)
			}
		})
	}
}

// An optional dependency `local` does not mention keeps exactly today's
// behaviour. Nothing in a manifest distinguishes "optional in the cloud" from
// "optional for this process to boot", so nsctl changes nothing it was not told
// to change -- stated separately from the table because it is the rule most
// likely to be "simplified" away later.
func TestAnUnmentionedOptionalDependencyIsNeverActivated(t *testing.T) {
	t.Parallel()

	m, err := Load(writeManifest(t, fixture))
	if err != nil {
		t.Fatal(err)
	}
	for _, profiles := range [][]string{nil, {"transforms"}, {"full"}, {"telemetry"}, m.AllProfiles()} {
		for _, w := range m.Activate(profiles) {
			if w.Key == "authorizer" {
				t.Fatalf("Activate(%v) declared the unmentioned optional dependency", profiles)
			}
		}
	}
}

func TestAllProfiles(t *testing.T) {
	t.Parallel()

	m, err := Load(writeManifest(t, fixture))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"full", "telemetry", "transforms"}
	if got := m.AllProfiles(); !reflect.DeepEqual(got, want) {
		t.Errorf("AllProfiles = %v, want %v", got, want)
	}
}

// The naming order's lower two tiers. The upper two -- an existing manifest
// binding and a --name override -- belong to whoever writes a manifest, because
// this package never sees one.
func TestWantDefaultName(t *testing.T) {
	t.Parallel()

	m, err := Load(writeManifest(t, fixture))
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]Want{}
	for _, w := range m.Activate(m.AllProfiles()) {
		byKey[w.Key] = w
	}
	for _, tt := range []struct {
		key, name, class string
		satisfies        []string
	}{
		// Tier four: the role, which is already how these read.
		{key: "base-vpc", name: "base-vpc", class: "hmd-vpc", satisfies: []string{"base-vpc"}},
		// Tier three: the block said so.
		{key: "neptune-db", name: "global-graph", class: "hmd-inf-neptune", satisfies: []string{"neptune-db"}},
		// A companion. No role, so nothing to satisfy -- which is what lets a
		// reader tell a test fixture from a real graph edge.
		{key: "ms-transform", name: "ms-transform", class: "hmd-ms-transform", satisfies: nil},
	} {
		t.Run(tt.key, func(t *testing.T) {
			t.Parallel()

			w, ok := byKey[tt.key]
			if !ok {
				t.Fatalf("no want keyed %q", tt.key)
			}
			if w.DefaultName != tt.name {
				t.Errorf("DefaultName = %q, want %q", w.DefaultName, tt.name)
			}
			if w.RepoClassName != tt.class {
				t.Errorf("RepoClassName = %q, want %q", w.RepoClassName, tt.class)
			}
			if !reflect.DeepEqual(w.Satisfies, tt.satisfies) {
				t.Errorf("Satisfies = %v, want %v", w.Satisfies, tt.satisfies)
			}
		})
	}
}

// One repo class filling several roles is three wants and one class, which is
// why the lock is keyed by class and `satisfies` is a list. hmd-inf-credentials
// does exactly this three times in hmd-inf-trino.
func TestOneClassCanFillSeveralRoles(t *testing.T) {
	t.Parallel()

	m, err := Load(writeManifest(t, `{"name":"hmd-inf-trino","deploy":{"dependencies":{
		"users":       {"repo_class_name":"hmd-inf-credentials","required":"true","version_spec":"0.1.9"},
		"ro-users":    {"repo_class_name":"hmd-inf-credentials","required":"true","version_spec":"0.1.9"},
		"adhoc-users": {"repo_class_name":"hmd-inf-credentials","required":"true","version_spec":"0.1.9"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	wants := m.Activate(nil)
	if len(wants) != 3 {
		t.Fatalf("got %d wants, want 3 -- one per role", len(wants))
	}
	for _, w := range wants {
		if w.RepoClassName != "hmd-inf-credentials" {
			t.Errorf("want %q names %q", w.Key, w.RepoClassName)
		}
		if w.DefaultName != w.Key {
			t.Errorf("%q defaults to %q; three roles sharing one class must default apart", w.Key, w.DefaultName)
		}
	}
}

func TestLoadRefusals(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name, body, want string
	}{
		{
			// ms-deployment fails the whole ChangeSet on an unmet required role
			// and a SKIPPED status does not mock one, so gating a required
			// dependency produces a failure three layers from its cause.
			name: "gating a required dependency",
			body: `{"name":"x","deploy":{"dependencies":{
				"neptune-db":{"repo_class_name":"hmd-inf-neptune","required":"true","version_spec":"~= 0.1"}}},
				"local":{"version":1,"dependencies":{"neptune-db":{"profiles":["full"]}}}}`,
			want: "is required",
		},
		{
			name: "gating a role no dependency declares",
			body: `{"name":"x","deploy":{"dependencies":{}},
				"local":{"version":1,"dependencies":{"otel":{"profiles":["full"]}}}}`,
			want: "no dependency named",
		},
		{
			// Both would default to the same instance name and one would
			// silently win.
			name: "a companion named after a dependency role",
			body: `{"name":"x","deploy":{"dependencies":{
				"app-db":{"repo_class_name":"hmd-inf-postgres","required":"true","version_spec":"0.8.1"}}},
				"local":{"version":1,"repos":[
					{"instance_name":"app-db","repo_class_name":"hmd-inf-postgres","version_spec":"0.8.1"}]}}`,
			want: "dependency role",
		},
		{
			name: "two companions with one name",
			body: `{"name":"x","local":{"version":1,"repos":[
				{"instance_name":"a","repo_class_name":"hmd-inf-one","version_spec":"0.1.0"},
				{"instance_name":"a","repo_class_name":"hmd-inf-two","version_spec":"0.1.0"}]}}`,
			want: "duplicate",
		},
		{
			name: "a companion with no repo class",
			body: `{"name":"x","local":{"version":1,"repos":[{"instance_name":"a","version_spec":"0.1.0"}]}}`,
			want: "repo_class_name",
		},
		{
			name: "a companion with no instance name",
			body: `{"name":"x","local":{"version":1,"repos":[{"repo_class_name":"hmd-inf-one","version_spec":"0.1.0"}]}}`,
			want: "instance_name",
		},
		{
			name: "an unknown local schema version",
			body: `{"name":"x","local":{"version":7}}`,
			want: "version 7",
		},
		{
			name: "a dependency with no repo class",
			body: `{"name":"x","deploy":{"dependencies":{"r":{"required":"true","version_spec":"0.1.0"}}}}`,
			want: "repo_class_name",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := Load(writeManifest(t, tt.body))
			if err == nil {
				t.Fatal("succeeded, want a refusal")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not mention %q", err, tt.want)
			}
		})
	}
}

func TestLoadNamesTheFileItCouldNotRead(t *testing.T) {
	t.Parallel()

	_, err := Load(t.TempDir())
	if err == nil {
		t.Fatal("succeeded on a directory with no manifest")
	}
	if !strings.Contains(err.Error(), filepath.Join("meta-data", "manifest.json")) {
		t.Errorf("error %q does not name the file it looked for", err)
	}
}

// A required role a cloud manifest fills with a class nothing local can stand in
// for -- hmd-inf-eks-node-group for compute -- is bound to what the environment
// provides; one whose instance needs its own wiring -- hmd-database-account --
// carries that wiring on the want. Both are required, and neither is gated.
const wiredFixture = `{
  "name": "hmd-ms-myapi",
  "deploy": {"dependencies": {
    "compute":        {"repo_class_name": "hmd-inf-eks-node-group", "required": "true",  "version_spec": "~= 0.1"},
    "db-credentials": {"repo_class_name": "hmd-database-account",   "required": "true",  "version_spec": "0.1.9"},
    "otel-collector": {"repo_class_name": "hmd-inf-otel-collector", "required": "false", "version_spec": "0.1.7"}
  }},
  "local": {"version": 1, "dependencies": {
    "compute":        {"bind": "local-neuronsphere"},
    "db-credentials": {"instance_configuration": {"db_name": "myapi"},
                       "dependencies": {"database-instance": "environment-db"}},
    "otel-collector": {"profiles": ["telemetry"], "bind": "local-neuronsphere"}
  }}
}`

func TestAGateCanBindOrWireARequiredRole(t *testing.T) {
	t.Parallel()

	m, err := Load(writeManifest(t, wiredFixture))
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]Want{}
	for _, w := range m.Activate([]string{"telemetry"}) {
		byKey[w.Key] = w
	}

	compute, ok := byKey["compute"]
	if !ok {
		t.Fatal("compute was not activated: a required role always is")
	}
	if compute.Bind != "local-neuronsphere" || len(compute.Profiles) != 0 {
		t.Errorf("compute = %+v, want bound to local-neuronsphere and ungated", compute)
	}

	db := byKey["db-credentials"]
	if db.Bind != "" {
		t.Errorf("db-credentials is bound to %q, want declared", db.Bind)
	}
	if got := db.Dependencies["database-instance"]; got != "environment-db" {
		t.Errorf("db-credentials dependencies = %v, want the gate's wiring", db.Dependencies)
	}
	if got := db.InstanceConfiguration["db_name"]; got != "myapi" {
		t.Errorf("db-credentials instance_configuration = %v, want the gate's", db.InstanceConfiguration)
	}

	// An optional role may be both gated and bound: it activates under its
	// profiles and, when it does, is bound rather than deployed.
	otel := byKey["otel-collector"]
	if otel.Bind != "local-neuronsphere" || !reflect.DeepEqual(otel.Profiles, []string{"telemetry"}) {
		t.Errorf("otel-collector = %+v, want gated on telemetry and bound", otel)
	}
	if _, on := lookup(m.Activate(nil), "otel-collector"); on {
		t.Error("otel-collector activated with no profile, want the gate respected")
	}
}

func lookup(wants []Want, key string) (Want, bool) {
	for _, w := range wants {
		if w.Key == key {
			return w, true
		}
	}
	return Want{}, false
}

func TestGateRefusals(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name, body, want string
	}{
		{
			// Nothing is declared for a bound role, so there is nothing for
			// these to configure and one of them would silently lose.
			name: "bind with wiring",
			body: `{"name":"x","deploy":{"dependencies":{
				"compute":{"repo_class_name":"hmd-inf-eks-node-group","required":"true","version_spec":"0.1.0"}}},
				"local":{"version":1,"dependencies":{"compute":{"bind":"local-neuronsphere","instance_configuration":{"a":1}}}}}`,
			want: "drop 'dependencies' and 'instance_configuration'",
		},
		{
			// A required role always activates, so an entry for one that gates
			// nothing and wires nothing is a no-op the author did not intend.
			name: "an empty entry on a required role",
			body: `{"name":"x","deploy":{"dependencies":{
				"compute":{"repo_class_name":"hmd-inf-eks-node-group","required":"true","version_spec":"0.1.0"}}},
				"local":{"version":1,"dependencies":{"compute":{}}}}`,
			want: "must say something",
		},
		{
			// Still refused: profiles on a required role, whatever else is set.
			name: "profiles beside a bind on a required role",
			body: `{"name":"x","deploy":{"dependencies":{
				"compute":{"repo_class_name":"hmd-inf-eks-node-group","required":"true","version_spec":"0.1.0"}}},
				"local":{"version":1,"dependencies":{"compute":{"bind":"local-neuronsphere","profiles":["full"]}}}}`,
			want: "cannot be profile-gated",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := Load(writeManifest(t, tt.body))
			if err == nil {
				t.Fatal("succeeded, want a refusal")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not mention %q", err, tt.want)
			}
		})
	}
}
