package router

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testEnv() Env {
	// The live environment: slot 8, so Trino 19033 and k3s 19072.
	return Env{Slug: "local", AccountID: "000000000001", TrinoPort: 19033, K3sPort: 19072, SparePort: 19035, IsDefault: true}
}

func readFragment(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// An unsigned proxy_pass resolves in the *default* account, where an
// environment's API does not exist -- and Floci answers 404, indistinguishable
// from a route that was never wired.
func TestEnvRoutesCarryTheAccountCredentialScope(t *testing.T) {
	t.Parallel()

	r := New(t.TempDir(), fakeEnv(nil))
	if err := r.WriteEnvRoutes(testEnv(), map[string]string{"hmd_ms_dbaccount": "gw1"}, "", nil); err != nil {
		t.Fatal(err)
	}
	body := readFragment(t, filepath.Join(r.HTTPDir(), EnvFragmentName("local")))

	if !strings.Contains(body, "location /local/hmd_ms_dbaccount/") {
		t.Errorf("the route is not prefixed with the slug:\n%s", body)
	}
	if !strings.Contains(body, `proxy_set_header Authorization "AWS4-HMAC-SHA256 Credential=000000000001/`) {
		t.Errorf("the credential scope is not injected:\n%s", body)
	}
	// The caller's own token has to survive being displaced, or an
	// authenticated request reaches the service as an anonymous one.
	if !strings.Contains(body, "proxy_set_header "+AltAuthHeader+" $http_authorization;") {
		t.Errorf("the caller's token is not relocated:\n%s", body)
	}
}

// The default environment keeps the historical fixed Trino port so existing
// integration tests need no change.
func TestEnvStreamEntries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		env       Env
		trino     string
		k3s       string
		wantPorts []int
	}{
		{"both, default env", testEnv(), "1.2.3.4:31880", "1.2.3.4:6443", []int{19033, LegacyTrinoHostPort, 19072}},
		{"k3s only", testEnv(), "", "1.2.3.4:6443", []int{19072}},
		{"trino only", testEnv(), "1.2.3.4:31880", "", []int{19033, LegacyTrinoHostPort}},
		{"neither", testEnv(), "", "", nil},
		{
			"a non-default env gets no legacy port",
			Env{Slug: "dev", TrinoPort: 19005, K3sPort: 19065},
			"1.2.3.4:31880", "1.2.3.4:6443", []int{19005, 19065},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			entries := EnvStreamEntries(tt.env, tt.trino, tt.k3s)
			if len(entries) != len(tt.wantPorts) {
				t.Fatalf("got %v, want ports %v", entries, tt.wantPorts)
			}
			for i, port := range tt.wantPorts {
				if entries[i].Port != port {
					t.Errorf("entry %d port = %d, want %d", i, entries[i].Port, port)
				}
			}
		})
	}
}

// Host is forwarded verbatim because Traefik picks the Ingress rule from it.
func TestEnvVhostForwardsHostAndUpgrades(t *testing.T) {
	t.Parallel()

	r := New(t.TempDir(), fakeEnv(nil))
	if err := r.WriteEnvVhosts(testEnv(), "1.2.3.4:31080", nil); err != nil {
		t.Fatal(err)
	}
	body := readFragment(t, filepath.Join(r.VhostDir(), EnvFragmentName("local")))

	if !strings.Contains(body, "server_name *.local."+IngressDomain+";") {
		t.Errorf("the wildcard server_name is wrong:\n%s", body)
	}
	if !strings.Contains(body, "proxy_set_header Host $host;") {
		t.Errorf("Host is not forwarded verbatim; every UI would resolve to the same backend:\n%s", body)
	}
	// What lets Argo stream workflow logs.
	if !strings.Contains(body, "proxy_set_header Upgrade $http_upgrade;") {
		t.Errorf("the upgrade headers are missing:\n%s", body)
	}
}

// A port route sets Host rather than forwarding it, because the browser sends
// localhost:<port>, which matches no Ingress rule.
func TestPortVhostSetsHostAndUndoesItOnRedirects(t *testing.T) {
	t.Parallel()

	r := New(t.TempDir(), fakeEnv(nil))
	host := IngressHostFor("airflow")
	if err := r.WriteEnvVhosts(testEnv(), "1.2.3.4:31080", []PortRoute{{Port: 19035, IngressHost: host}}); err != nil {
		t.Fatal(err)
	}
	body := readFragment(t, filepath.Join(r.VhostDir(), EnvFragmentName("local")))

	if !strings.Contains(body, "proxy_set_header Host "+host+";") {
		t.Errorf("Host is not set to the Ingress host:\n%s", body)
	}
	// Otherwise an absolute Location sends the browser to a name it cannot
	// resolve.
	if !strings.Contains(body, "proxy_redirect http://"+host+"/ http://localhost:19035/;") {
		t.Errorf("the redirect is not undone:\n%s", body)
	}
}

// hmd-cli-helm renders every local chart with alb.hostname=<instance>.local.<domain>,
// and the slug there is the literal "local" in every environment.
func TestIngressHostForUsesTheLiteralLocalSlug(t *testing.T) {
	t.Parallel()

	for _, instance := range []string{"airflow", "argo", "superset"} {
		want := instance + ".local." + IngressDomain
		if got := IngressHostFor(instance); got != want {
			t.Errorf("IngressHostFor(%q) = %q, want %q", instance, got, want)
		}
	}
}

// The bulk writers only see services this CLI deploys; a DAG-deployed service's
// route is spliced in separately, bounded by its markers.
func TestUpsertServiceRouteReplacesExactlyOneRoute(t *testing.T) {
	t.Parallel()

	r := New(t.TempDir(), fakeEnv(nil))
	env := testEnv()
	if err := r.WriteEnvRoutes(env, map[string]string{"hmd_ms_dbaccount": "gw1"}, "", nil); err != nil {
		t.Fatal(err)
	}
	if err := r.UpsertServiceRoute(env, "transform", "gw2", "cdktf-stage"); err != nil {
		t.Fatal(err)
	}
	body := readFragment(t, filepath.Join(r.HTTPDir(), EnvFragmentName("local")))

	if !strings.Contains(body, "location /local/hmd_ms_dbaccount/") {
		t.Errorf("the existing route was lost:\n%s", body)
	}
	// CDKTF names its stage after the stack, not "local", so a route built with
	// the default stage answers {"message": "Stage not found"}.
	if !strings.Contains(body, "/restapis/gw2/cdktf-stage/") {
		t.Errorf("the spliced route is missing or uses the wrong stage:\n%s", body)
	}

	if err := r.UpsertServiceRoute(env, "transform", "gw3", "cdktf-stage"); err != nil {
		t.Fatal(err)
	}
	body = readFragment(t, filepath.Join(r.HTTPDir(), EnvFragmentName("local")))
	if strings.Contains(body, "gw2") {
		t.Errorf("the old route survived the replacement:\n%s", body)
	}
	if strings.Count(body, "location /local/transform/") != 1 {
		t.Errorf("the route was duplicated:\n%s", body)
	}
}

func TestUpsertServiceRouteCreatesTheFragmentWhenAbsent(t *testing.T) {
	t.Parallel()

	r := New(t.TempDir(), fakeEnv(nil))
	if err := r.UpsertServiceRoute(testEnv(), "transform", "gw1", "stage"); err != nil {
		t.Fatal(err)
	}
	body := readFragment(t, filepath.Join(r.HTTPDir(), EnvFragmentName("local")))
	if !strings.Contains(body, "location /local/transform/") {
		t.Errorf("the route was not written:\n%s", body)
	}
}

// Deleting an environment is an unlink, in each of the three directories.
func TestRemoveEnvRoutesRemovesEveryFragment(t *testing.T) {
	t.Parallel()

	r := New(t.TempDir(), fakeEnv(nil))
	env := testEnv()
	if err := r.WriteEnvRoutes(env, map[string]string{"a": "gw"}, "", nil); err != nil {
		t.Fatal(err)
	}
	if err := r.WriteEnvStreams(env, []StreamEntry{{Port: 19033, Upstream: "x:1"}}); err != nil {
		t.Fatal(err)
	}
	if err := r.WriteEnvVhosts(env, "x:1", nil); err != nil {
		t.Fatal(err)
	}

	if err := r.RemoveEnvRoutes("local"); err != nil {
		t.Fatalf("RemoveEnvRoutes: %v", err)
	}
	for _, dir := range []string{r.HTTPDir(), r.StreamDir(), r.VhostDir()} {
		if _, err := os.Stat(filepath.Join(dir, EnvFragmentName("local"))); !os.IsNotExist(err) {
			t.Errorf("a fragment survived in %s", dir)
		}
	}
	// Removing an environment that was never written is not an error.
	if err := r.RemoveEnvRoutes("never"); err != nil {
		t.Errorf("removing an absent environment: %v", err)
	}
}

// The stream fragment is the record of what an environment actually routes, so
// `env status` can report a Trino endpoint only when one exists rather than
// deriving it from the port slot. NERD023 SPEC003.
func TestStreamsPortReadsTheFragment(t *testing.T) {
	t.Parallel()

	r := New(t.TempDir(), fakeEnv(nil))
	env := testEnv()

	// Never started: nothing is routed, and that is an answer rather than an
	// error -- an environment with no fragment routes nothing.
	if r.StreamsPort(env.Slug, env.TrinoPort) {
		t.Error("a missing fragment must not report a route")
	}

	// k3s only, which is what startCluster writes before Trino is looked for.
	if err := r.WriteEnvStreams(env, EnvStreamEntries(env, "", "1.2.3.4:6443")); err != nil {
		t.Fatal(err)
	}
	if r.StreamsPort(env.Slug, env.TrinoPort) {
		t.Error("Trino is not routed, so it must not be reported")
	}
	if !r.StreamsPort(env.Slug, env.K3sPort) {
		t.Error("k3s is routed and should be reported")
	}

	// Trino found: EnvStreamEntries adds its listener, and the reader sees it.
	if err := r.WriteEnvStreams(env, EnvStreamEntries(env, "1.2.3.4:31880", "1.2.3.4:6443")); err != nil {
		t.Fatal(err)
	}
	if !r.StreamsPort(env.Slug, env.TrinoPort) {
		t.Error("a routed Trino should be reported")
	}
	// The default environment also keeps the legacy fixed port.
	if !r.StreamsPort(env.Slug, LegacyTrinoHostPort) {
		t.Error("the default environment's legacy Trino port should be reported")
	}

	// Another environment's fragment is not this one's.
	if r.StreamsPort("other", env.TrinoPort) {
		t.Error("a slug with no fragment must not read another's")
	}
}

// NERD024 SPEC001: the fragment is the record of what was actually routed, so
// it answers "is that service there" for a caller that must stay cheap and must
// work on a stopped environment -- StreamsPort's contract, for HTTP.
func TestRoutesServiceReadsTheFragment(t *testing.T) {
	t.Parallel()

	r := New(t.TempDir(), fakeEnv(nil))

	// Before anything is written: an environment that has never started routes
	// nothing, and that is an answer rather than an error.
	if r.RoutesService("local", "hmd_ms_dbaccount") {
		t.Error("a missing fragment must not report a route")
	}

	if err := r.WriteEnvRoutes(testEnv(), map[string]string{"hmd_ms_dbaccount": "gw1"}, "", nil); err != nil {
		t.Fatal(err)
	}
	if !r.RoutesService("local", "hmd_ms_dbaccount") {
		t.Error("a written route should be found")
	}
	// Scoped to the service and to the environment, not merely to the file.
	if r.RoutesService("local", "hmd_ms_transform") {
		t.Error("a service with no route must not be reported")
	}
	if r.RoutesService("other", "hmd_ms_dbaccount") {
		t.Error("another environment's fragment is not this one's")
	}

	// Rewritten without it -- which is what an environment that stops
	// declaring a database-account consumer does on its next start.
	if err := r.WriteEnvRoutes(testEnv(), nil, "", nil); err != nil {
		t.Fatal(err)
	}
	if r.RoutesService("local", "hmd_ms_dbaccount") {
		t.Error("a route rewritten away must not still be reported")
	}
}
