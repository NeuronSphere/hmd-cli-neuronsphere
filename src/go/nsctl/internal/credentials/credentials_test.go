package credentials

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bacon"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/floci"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
)

// classWith builds a reader over in-memory manifests, keyed by class.
func classWith(t *testing.T, byClass map[string][]bacon.AccessEntry) ClassReader {
	t.Helper()
	return func(class string) (*bacon.Store, error) {
		entries, ok := byClass[class]
		if !ok {
			return nil, fmt.Errorf("no tree for %s", class)
		}
		dir := t.TempDir()
		s, err := bacon.Init(dir, class, "d")
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if _, err := bacon.AddAccess(s.Doc, e); err != nil {
				return nil, err
			}
		}
		if err := s.Save(); err != nil {
			return nil, err
		}
		return bacon.Open(dir)
	}
}

func supersetAccess() bacon.AccessEntry {
	return bacon.AccessEntry{
		Name:     "superset",
		URL:      "http://{ingress_host}/",
		Username: "admin",
		Notes:    "Self-registration is off.",
		Secret: &bacon.AccessSecret{
			Store:    bacon.StoreSecretsManager,
			Key:      "{instance_name}-{deployment_id}-{environment}-admin-credentials",
			Property: "password",
		},
	}
}

func baseOptions(t *testing.T) Options {
	return Options{
		Environment:  "dev",
		DeploymentID: "aaaa",
		Repos: []manifest.Repo{
			{InstanceName: "superset", RepoClassName: "hmd-inf-superset"},
			{InstanceName: "cache", RepoClassName: "hmd-inf-redis"},
		},
		Class: classWith(t, map[string][]bacon.AccessEntry{
			"hmd-inf-superset": {supersetAccess()},
			"hmd-inf-redis":    nil,
		}),
	}
}

// Every placeholder resolves from what nsctl already holds, which is what makes
// one declaration work in any environment under any instance name.
func TestResolveFillsThePlaceholders(t *testing.T) {
	t.Parallel()

	got := Resolve(context.Background(), baseOptions(t), false)
	if len(got) != 1 {
		t.Fatalf("want one entry -- only superset declares access -- got %d: %+v", len(got), got)
	}
	e := got[0]
	if e.URL != "http://superset.dev.ns.local/" {
		t.Errorf("URL = %q", e.URL)
	}
	if e.Key != "superset-aaaa-dev-admin-credentials" {
		t.Errorf("Key = %q", e.Key)
	}
	if e.Store != bacon.StoreSecretsManager || e.Property != "password" {
		t.Errorf("store/property = %q/%q", e.Store, e.Property)
	}
	if e.Username != "admin" || e.Instance != "superset" || e.RepoClass != "hmd-inf-superset" {
		t.Errorf("entry = %+v", e)
	}
	// Withheld by default, and without asking anything: no reader was supplied
	// and none was needed.
	if e.Secret != "" {
		t.Errorf("a resolve that was not asked to reveal returned a value: %q", e.Secret)
	}
	if e.Problem != "" {
		t.Errorf("unexpected problem: %s", e.Problem)
	}
}

// --reveal reads the value, and the property is taken out of the JSON secret the
// chart's ExternalSecret reads the same way.
func TestResolveRevealsTheDeclaredProperty(t *testing.T) {
	t.Parallel()

	o := baseOptions(t)
	var askedStore, askedName string
	o.Get = func(_ context.Context, store, name string) (string, error) {
		askedStore, askedName = store, name
		return `{"username":"admin","password":"s3cret"}`, nil
	}

	got := Resolve(context.Background(), o, true)
	if len(got) != 1 {
		t.Fatalf("want one entry, got %d", len(got))
	}
	if got[0].Secret != "s3cret" {
		t.Errorf("Secret = %q, want the password property", got[0].Secret)
	}
	if askedStore != bacon.StoreSecretsManager {
		t.Errorf("asked store %q; the store is declared and must not be guessed", askedStore)
	}
	if askedName != "superset-aaaa-dev-admin-credentials" {
		t.Errorf("asked for %q", askedName)
	}
}

// An entry that cannot resolve is reported with its reason rather than dropped:
// its name is otherwise shown nowhere, and a deploy in progress is the common
// case.
func TestResolveReportsWhyAnEntryDidNotResolve(t *testing.T) {
	t.Parallel()

	o := baseOptions(t)
	o.Get = func(context.Context, string, string) (string, error) {
		return "", fmt.Errorf("%w: not there", floci.ErrNoSuchSecret)
	}
	got := Resolve(context.Background(), o, true)
	if len(got) != 1 {
		t.Fatalf("an unresolved entry must still be reported, got %d", len(got))
	}
	if !strings.Contains(got[0].Problem, "written when the instance deploys") {
		t.Errorf("Problem = %q, want the missing-secret wording", got[0].Problem)
	}
	if got[0].Secret != "" {
		t.Errorf("a failed read left a value: %q", got[0].Secret)
	}
	// The URL still resolved, and is still the useful half.
	if got[0].URL == "" {
		t.Error("the URL should resolve even when the secret does not")
	}

	// A property the secret does not have says which ones it does.
	o.Get = func(context.Context, string, string) (string, error) {
		return `{"username":"admin","pass":"x"}`, nil
	}
	got = Resolve(context.Background(), o, true)
	if !strings.Contains(got[0].Problem, `has no "password" property`) ||
		!strings.Contains(got[0].Problem, "pass, username") {
		t.Errorf("Problem = %q, want the available properties named", got[0].Problem)
	}

	// Asking to reveal with no reader available says so rather than reporting
	// the entry as having no credential.
	o.Get = nil
	got = Resolve(context.Background(), o, true)
	if !strings.Contains(got[0].Problem, "no secret reader") {
		t.Errorf("Problem = %q", got[0].Problem)
	}
}

// NERD023 SPEC004: where the producer published the name, that name wins. nsctl
// re-deriving it from a template would be nsctl disagreeing with the thing that
// wrote the secret.
func TestResolvePrefersTheProducersPublishedName(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	recorded := []map[string]any{{
		"resource_name": "r",
		"output":        map[string]any{"secret_name": "written-by-the-chart"},
	}}
	data, err := json.Marshal(recorded)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "api.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	o := Options{
		Environment:  "dev",
		DeploymentID: "aaaa",
		OutputDir:    dir,
		Repos:        []manifest.Repo{{InstanceName: "api", RepoClassName: "hmd-ms-api"}},
		Class: classWith(t, map[string][]bacon.AccessEntry{
			"hmd-ms-api": {{
				Name:   "api",
				URL:    "http://{ingress_host}/api/",
				Secret: &bacon.AccessSecret{Store: bacon.StoreParameterStore, Output: "secret_name"},
			}},
		}),
	}
	got := Resolve(context.Background(), o, false)
	if len(got) != 1 {
		t.Fatalf("want one entry, got %d", len(got))
	}
	if got[0].Key != "written-by-the-chart" {
		t.Errorf("Key = %q, want the producer's published name", got[0].Key)
	}

	// Nothing recorded yet: said plainly rather than silently falling back to a
	// name nobody wrote.
	o.OutputDir = t.TempDir()
	got = Resolve(context.Background(), o, false)
	if !strings.Contains(got[0].Problem, "produced no resource output yet") {
		t.Errorf("Problem = %q", got[0].Problem)
	}
}

// --instance narrows the result, and the sort makes the same environment report
// the same bytes twice.
func TestResolveFiltersAndSorts(t *testing.T) {
	t.Parallel()

	o := Options{
		Environment: "dev",
		Repos: []manifest.Repo{
			{InstanceName: "zeta", RepoClassName: "c"},
			{InstanceName: "alpha", RepoClassName: "c"},
		},
		Class: classWith(t, map[string][]bacon.AccessEntry{
			"c": {
				{Name: "ui", URL: "http://{ingress_host}/"},
				{Name: "api", URL: "http://{ingress_host}/api/"},
			},
		}),
	}
	got := Resolve(context.Background(), o, false)
	var order []string
	for _, e := range got {
		order = append(order, e.Instance+"/"+e.Name)
	}
	want := []string{"alpha/api", "alpha/ui", "zeta/api", "zeta/ui"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", order, want)
	}

	o.Instance = "zeta"
	got = Resolve(context.Background(), o, false)
	if len(got) != 2 || got[0].Instance != "zeta" {
		t.Errorf("--instance did not narrow the result: %+v", got)
	}

	// An entry with no secret is not a problem; it simply has none.
	if got[0].HasSecret() || got[0].Problem != "" {
		t.Errorf("an entry that declares no credential should be clean: %+v", got[0])
	}
}

// Any decides whether a deploy summary points at `env credentials`. A pointer to
// an empty table is noise, so a class that declares nothing must answer false.
func TestAny(t *testing.T) {
	t.Parallel()

	class := classWith(t, map[string][]bacon.AccessEntry{
		"hmd-inf-superset": {supersetAccess()},
		"hmd-inf-redis":    nil,
	})
	if !Any([]manifest.Repo{{InstanceName: "s", RepoClassName: "hmd-inf-superset"}}, nil, class) {
		t.Error("a class that declares access should answer true")
	}
	if Any([]manifest.Repo{{InstanceName: "c", RepoClassName: "hmd-inf-redis"}}, nil, class) {
		t.Error("a class that declares none should answer false")
	}
	// A class with no tree on this machine contributes nothing and is not an
	// error: an environment built from pruned artifacts is an ordinary state.
	if Any([]manifest.Repo{{InstanceName: "x", RepoClassName: "not-here"}}, nil, class) {
		t.Error("an unresolvable class should answer false")
	}
	if Any(nil, nil, class) {
		t.Error("no instances should answer false")
	}
}

// stackOptions is baseOptions plus a stack that declares access for one of the
// instances it declared, which is the SPEC007 shape.
func stackOptions(t *testing.T, entries ...bacon.AccessEntry) Options {
	t.Helper()
	o := baseOptions(t)
	o.Stacks = []manifest.StackRecord{{
		Name: "analytics", Version: "0.1.3", Class: "hmd-stack-analytics",
		Declared: []string{"superset", "cache"},
	}}
	o.Class = classWith(t, map[string][]bacon.AccessEntry{
		"hmd-inf-superset":    {supersetAccess()},
		"hmd-inf-redis":       nil,
		"hmd-stack-analytics": entries,
	})
	return o
}

// A stack replaces the class's entry for the instance it names. The motivating
// case: hmd-app-airflow documents the `admin` user its own default_configuration
// creates, and a stack that replaces that users fixture leaves the class's entry
// describing a login that does not exist.
func TestAStackSupersedesTheClassEntryItNames(t *testing.T) {
	t.Parallel()

	o := stackOptions(t, bacon.AccessEntry{
		Name: "superset", Instance: "superset",
		URL: "http://{ingress_host}/", Username: "analytics-admin",
		Notes: "This stack pins its own admin.",
	})
	got := Resolve(context.Background(), o, false)

	var superset []Entry
	for _, e := range got {
		if e.Instance == "superset" {
			superset = append(superset, e)
		}
	}
	if len(superset) != 1 {
		t.Fatalf("want one entry for superset, got %d: %+v", len(superset), superset)
	}
	e := superset[0]
	if e.Username != "analytics-admin" {
		t.Errorf("the stack's username should win, got %q", e.Username)
	}
	if e.Stack != "analytics" {
		t.Errorf("the entry should name the stack that declared it, got %q", e.Stack)
	}
	// It templates against the instance it names, so the URL and the secret
	// resolve exactly as the class's would have.
	if e.URL != "http://superset.dev.ns.local/" {
		t.Errorf("URL = %q", e.URL)
	}
}

// An entry for an instance nothing else describes is added, not a replacement.
func TestAStackEntryForAnUnclaimedInstanceIsAdded(t *testing.T) {
	t.Parallel()

	o := stackOptions(t, bacon.AccessEntry{
		Name: "cache", Instance: "cache",
		URL: "http://{ingress_host}/", Username: "redis",
	})
	got := Resolve(context.Background(), o, false)

	found := false
	for _, e := range got {
		if e.Instance == "cache" && e.Name == "cache" {
			found, _ = true, e
			if e.Stack != "analytics" {
				t.Errorf("stack = %q", e.Stack)
			}
		}
	}
	if !found {
		t.Errorf("the stack's entry for cache should be reported; got %+v", got)
	}
	// And the class entry it did not name is untouched.
	for _, e := range got {
		if e.Instance == "superset" && e.Stack != "" {
			t.Errorf("superset should still come from its class, got stack %q", e.Stack)
		}
	}
}

// A stack that names an instance the environment does not declare has a defect
// in it, and the report is where its author finds out.
func TestAStackEntryNamingAnAbsentInstanceIsReported(t *testing.T) {
	t.Parallel()

	o := stackOptions(t, bacon.AccessEntry{
		Name: "ghost", Instance: "not-declared", URL: "http://x/",
	})
	for _, e := range Resolve(context.Background(), o, false) {
		if e.Name == "ghost" {
			if e.Problem == "" {
				t.Error("an entry naming an absent instance should carry a problem")
			}
			return
		}
	}
	t.Error("the entry should be reported, not dropped")
}

// Without an instance there is nothing to template against: {ingress_host} and
// a secret name are both derived from one.
func TestAStackEntryWithNoInstanceIsReported(t *testing.T) {
	t.Parallel()

	o := stackOptions(t, bacon.AccessEntry{Name: "nowhere", URL: "http://{ingress_host}/"})
	for _, e := range Resolve(context.Background(), o, false) {
		if e.Name == "nowhere" {
			if e.Problem == "" {
				t.Error("an entry naming no instance should carry a problem")
			}
			return
		}
	}
	t.Error("the entry should be reported, not dropped")
}

// A record written before the class was persisted cannot reach its declaration.
// Saying so beats silence: a stack that declares a front door and is ignored
// looks exactly like a stack that declares none.
func TestAStackRecordWithNoClassSaysWhyItCannotBeRead(t *testing.T) {
	t.Parallel()

	o := baseOptions(t)
	o.Stacks = []manifest.StackRecord{{Name: "analytics", Version: "0.1.3"}}
	for _, e := range Resolve(context.Background(), o, false) {
		if e.Stack == "analytics" && e.Problem != "" {
			return
		}
	}
	t.Error("a record with no class should be reported as unreadable")
}

// --instance filters stack entries by the instance they name, not by the stack.
func TestInstanceFilterAppliesToStackEntries(t *testing.T) {
	t.Parallel()

	o := stackOptions(t, bacon.AccessEntry{
		Name: "cache", Instance: "cache", URL: "http://{ingress_host}/",
	})
	o.Instance = "superset"
	for _, e := range Resolve(context.Background(), o, false) {
		if e.Instance != "superset" {
			t.Errorf("--instance superset should exclude %+v", e)
		}
	}
}

// The deploy summary's pointer must appear when the only declaration is a
// stack's, or an environment deploys three interfaces and says nothing.
func TestAnyCountsAStacksOwnDeclaration(t *testing.T) {
	t.Parallel()

	class := classWith(t, map[string][]bacon.AccessEntry{
		"hmd-inf-redis":       nil,
		"hmd-stack-analytics": {{Name: "superset", Instance: "superset", URL: "http://x/"}},
	})
	repos := []manifest.Repo{{InstanceName: "cache", RepoClassName: "hmd-inf-redis"}}
	stacks := []manifest.StackRecord{{Name: "analytics", Class: "hmd-stack-analytics"}}
	if !Any(repos, stacks, class) {
		t.Error("a stack that declares access should answer true")
	}
	if Any(repos, nil, class) {
		t.Error("without the stack there is no declaration and it should answer false")
	}
}
