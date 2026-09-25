package router

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeEnv(vars map[string]string) Lookup {
	return func(key string) string { return vars[key] }
}

// golden reads a reference file produced by the Python nginx_router.
func golden(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "python-"+name))
	if err != nil {
		t.Fatalf("reading the golden file: %v", err)
	}
	return string(data)
}

// controlPlaneServices is the set the control-plane bootstrap routes.
func controlPlaneServices() map[string]string {
	return map[string]string{
		"hmd_ms_deployment":   "abc123",
		"hmd_ms_naming":       "def456",
		"hmd_ms_artifact_lib": "ghi789",
	}
}

// The golden files were written by the Python nginx_router, not by marshalling
// these functions and reading them back. hmd_proxy loads whichever of the two
// wrote last, so a difference here is a difference in what nginx actually
// serves.
func TestBaseConfigMatchesThePython(t *testing.T) {
	t.Parallel()

	r := New(t.TempDir(), fakeEnv(nil))
	if got, want := r.BaseConfig(), golden(t, "neuronsphere.conf"); got != want {
		t.Errorf("base config differs from the Python's:\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}
}

func TestControlPlaneRoutesMatchThePython(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	r := New(home, fakeEnv(nil))
	if err := r.WriteControlPlaneRoutes(controlPlaneServices(), "", "", nil); err != nil {
		t.Fatalf("WriteControlPlaneRoutes: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(r.HTTPDir(), ControlPlaneFragment))
	if err != nil {
		t.Fatal(err)
	}
	if want := golden(t, "http.d_00-control-plane.conf"); string(got) != want {
		t.Errorf("HTTP fragment differs from the Python's:\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}
}

func TestControlPlaneStreamsMatchThePython(t *testing.T) {
	t.Parallel()

	r := New(t.TempDir(), fakeEnv(nil))
	if err := r.WriteControlPlaneStreams("neuronsphere", "", 0); err != nil {
		t.Fatalf("WriteControlPlaneStreams: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(r.StreamDir(), ControlPlaneFragment))
	if err != nil {
		t.Fatal(err)
	}
	if want := golden(t, "stream.d_00-control-plane.conf"); string(got) != want {
		t.Errorf("stream fragment differs:\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}
	// Without this listener, `start` polls localhost:4566 for five minutes and
	// reports a degraded bootstrap no matter how healthy Floci is.
	if !strings.Contains(string(got), "listen 4566;") {
		t.Error("the control-plane stream does not listen on 4566")
	}
}

func TestControlPlaneVhostsMatchThePython(t *testing.T) {
	t.Parallel()

	r := New(t.TempDir(), fakeEnv(nil))
	if err := r.WriteControlPlaneVhosts(ControlPlaneVhosts{GUIEnabled: true, GUIPort: 19003}); err != nil {
		t.Fatalf("WriteControlPlaneVhosts: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(r.VhostDir(), ControlPlaneFragment))
	if err != nil {
		t.Fatal(err)
	}
	if want := golden(t, "vhost.d_00-control-plane.conf"); string(got) != want {
		t.Errorf("vhost fragment differs:\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}
}

// Always written: when the GUI is disabled the fragment is truncated to its
// header, which removes a listener a previous run may have added.
func TestVhostFragmentIsTruncatedWhenTheGUIIsDisabled(t *testing.T) {
	t.Parallel()

	r := New(t.TempDir(), fakeEnv(nil))
	if err := r.WriteControlPlaneVhosts(ControlPlaneVhosts{GUIEnabled: true, GUIPort: 19003}); err != nil {
		t.Fatal(err)
	}
	if err := r.WriteControlPlaneVhosts(ControlPlaneVhosts{GUIPort: 19003}); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(filepath.Join(r.VhostDir(), ControlPlaneFragment))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "listen 19003") {
		t.Errorf("the GUI listener survived being disabled:\n%s", got)
	}
	if !strings.Contains(string(got), "control-plane vhosts") {
		t.Errorf("the header was lost:\n%s", got)
	}
}

// The aliases exist because the robot suites build their URL from
// HMD_INSTANCE_NAME, which arrives as any of these spellings.
func TestControlPlaneRoutesCarryTheServiceAliases(t *testing.T) {
	t.Parallel()

	r := New(t.TempDir(), fakeEnv(nil))
	if err := r.WriteControlPlaneRoutes(controlPlaneServices(), "", "", nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(r.HTTPDir(), ControlPlaneFragment))
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)

	for _, alias := range []string{"ms-deployment", "ms_deployment", "hmd-ms-deployment", "ms-naming", "ms_naming", "hmd-ms-naming"} {
		if !strings.Contains(body, "location /"+alias+"/") {
			t.Errorf("no location for the alias %q", alias)
		}
	}
	// artifact-lib has no aliases, so it must not gain any.
	if strings.Count(body, "location /") != 9 {
		t.Errorf("got %d locations, want 3 services plus 6 aliases", strings.Count(body, "location /"))
	}
}

func TestControlPlaneRoutesIncludeExtraPassthroughs(t *testing.T) {
	t.Parallel()

	r := New(t.TempDir(), fakeEnv(nil))
	if err := r.WriteControlPlaneRoutes(map[string]string{}, "", "", map[string]string{"/thing/": "http://somewhere:8080/"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(r.HTTPDir(), ControlPlaneFragment))
	body := string(data)

	if !strings.Contains(body, "location /thing/") {
		t.Errorf("no passthrough location:\n%s", body)
	}
	// A passthrough goes straight at the URL, with no API Gateway path.
	if strings.Contains(body, "/restapis/") {
		t.Errorf("the passthrough went through API Gateway:\n%s", body)
	}
	// One trailing slash, not two.
	if !strings.Contains(body, "proxy_pass http://somewhere:8080/;") {
		t.Errorf("the upstream slash was not normalised:\n%s", body)
	}
}

// An unsigned proxy_pass lands in the default account, so an environment's API
// needs the credential scope injected or Floci answers 404 -- indistinguishable
// from a route that was never wired.
func TestAPILocationInjectsTheAccountOnlyWhenGiven(t *testing.T) {
	t.Parallel()

	r := New(t.TempDir(), fakeEnv(nil))

	without := r.apiLocation("svc", "http://neuronsphere:4566", "gw", "local", "")
	if strings.Contains(without, "Authorization") {
		t.Errorf("the control-plane location touched Authorization:\n%s", without)
	}
	if strings.Contains(without, AltAuthHeader) {
		t.Errorf("the control-plane location relocated the caller's token:\n%s", without)
	}

	with := r.apiLocation("svc", "http://neuronsphere:4566", "gw", "local", "000000000001")
	if !strings.Contains(with, `proxy_set_header Authorization "AWS4-HMAC-SHA256 Credential=000000000001/`) {
		t.Errorf("the account's credential scope was not set:\n%s", with)
	}
	if !strings.Contains(with, "proxy_set_header "+AltAuthHeader+" $http_authorization;") {
		t.Errorf("the caller's token was not relocated:\n%s", with)
	}
}

// The credential scope must be unconditional. Filling only an empty
// Authorization made the account selector and the caller's JWT mutually
// exclusive: a request carrying a token kept it, left Floci nothing to resolve
// the account from, and 404d -- so only the environment whose account happens
// to be Floci's default could serve an authenticated request.
func TestAPILocationDoesNotYieldAuthorizationToTheCaller(t *testing.T) {
	t.Parallel()

	r := New(t.TempDir(), fakeEnv(nil))
	got := r.apiLocation("svc", "http://neuronsphere:4566", "gw", "local", "000000000002")

	// $http_authorization may appear only as the *value* relocated into the
	// side header, never as Authorization's own value.
	if strings.Contains(got, "proxy_set_header Authorization $http_authorization;") {
		t.Errorf("Authorization still defers to the caller:\n%s", got)
	}
	if strings.Contains(got, "$ns_auth") || strings.Contains(got, "$ns_account") {
		t.Errorf("the location still depends on the removed map:\n%s", got)
	}
}

// The region rides in the credential scope, and Floci ignores it -- but a
// mismatch with AWS_DEFAULT_REGION would make the rendered config churn.
func TestAPILocationUsesTheConfiguredRegion(t *testing.T) {
	t.Parallel()

	r := New(t.TempDir(), fakeEnv(map[string]string{"AWS_DEFAULT_REGION": "eu-west-1"}))
	got := r.apiLocation("svc", "http://neuronsphere:4566", "gw", "local", "000000000001")
	if !strings.Contains(got, "/eu-west-1/execute-api/aws4_request") {
		t.Errorf("the credential scope ignored AWS_DEFAULT_REGION:\n%s", got)
	}
}

// The trailing slash on both sides is what makes nginx strip the prefix, so the
// Lambda receives clean /api/... paths rather than /<svc>/api/... (which
// FastAPI would 404).
func TestAPILocationStripsThePrefix(t *testing.T) {
	t.Parallel()

	got := New(t.TempDir(), fakeEnv(nil)).apiLocation("svc", "http://neuronsphere:4566", "gw", "local", "")
	if !strings.Contains(got, "location /svc/ {") {
		t.Errorf("location has no trailing slash:\n%s", got)
	}
	if !strings.Contains(got, "/restapis/gw/local/_user_request_/;") {
		t.Errorf("proxy_pass has no trailing slash:\n%s", got)
	}
}

// A literal proxy_pass host:port is resolved once at config load, and a name
// that does not resolve makes nginx refuse to start -- taking every other route
// down with it. Every upstream that can legitimately be absent goes through a
// variable.
func TestStreamUpstreamsResolvePerConnection(t *testing.T) {
	t.Parallel()

	got := streamServer(4566, "neuronsphere:4566", "ns_floci")
	if !strings.Contains(got, `set $ns_floci "neuronsphere:4566";`) || !strings.Contains(got, "proxy_pass $ns_floci;") {
		t.Errorf("the upstream is not deferred through a variable:\n%s", got)
	}
	if strings.Contains(got, "proxy_pass neuronsphere:4566;") {
		t.Errorf("the upstream is a literal:\n%s", got)
	}
}

func TestBootstrapConfigServesFloci(t *testing.T) {
	t.Parallel()

	r := New(t.TempDir(), fakeEnv(nil))
	got := r.BootstrapConfig("neuronsphere")

	if !strings.Contains(got, "listen 4566;") {
		t.Errorf("the placeholder does not listen on 4566:\n%s", got)
	}
	if !strings.Contains(got, "return 503") {
		t.Errorf("the placeholder does not answer 503:\n%s", got)
	}
	// It must not include the fragment directories: the bind mount may not be
	// visible yet, and stale fragments can reference containers that are down.
	if strings.Contains(got, NSDir+"/http.d") || strings.Contains(got, NSDir+"/stream.d") {
		t.Errorf("the placeholder includes the fragment directories:\n%s", got)
	}
}

// On a normal restart the full config is on disk and still correct; replacing
// it with a placeholder would drop every route until the reload later on.
func TestWriteBootstrapConfigLeavesAWorkingConfigAlone(t *testing.T) {
	t.Parallel()

	r := New(t.TempDir(), fakeEnv(nil))
	if err := r.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	if err := r.WriteBaseConfig(); err != nil {
		t.Fatal(err)
	}
	if err := r.WriteControlPlaneStreams("neuronsphere", "", 0); err != nil {
		t.Fatal(err)
	}

	wrote, err := r.WriteBootstrapConfig("neuronsphere")
	if err != nil {
		t.Fatal(err)
	}
	if wrote {
		t.Error("the placeholder overwrote a config that already serves 4566")
	}
	data, _ := os.ReadFile(r.BaseConfigPath())
	if !strings.Contains(string(data), "include "+NSDir+"/http.d/*.conf;") {
		t.Error("the full config was replaced")
	}
}

// An include-based config whose fragment directory was wiped looks complete but
// serves nothing on 4566 -- precisely the state that hangs a start.
func TestWriteBootstrapConfigReplacesAnIncludeConfigWithNoFragment(t *testing.T) {
	t.Parallel()

	r := New(t.TempDir(), fakeEnv(nil))
	if err := r.WriteBaseConfig(); err != nil {
		t.Fatal(err)
	}
	// No stream fragment on disk.

	wrote, err := r.WriteBootstrapConfig("neuronsphere")
	if err != nil {
		t.Fatal(err)
	}
	if !wrote {
		t.Error("a config that serves nothing on 4566 was left in place")
	}
	data, _ := os.ReadFile(r.BaseConfigPath())
	if !strings.Contains(string(data), "listen 4566;") {
		t.Error("the replacement does not listen on 4566")
	}
}

func TestWriteBootstrapConfigWritesWhenNothingExists(t *testing.T) {
	t.Parallel()

	r := New(t.TempDir(), fakeEnv(nil))
	wrote, err := r.WriteBootstrapConfig("neuronsphere")
	if err != nil {
		t.Fatal(err)
	}
	if !wrote {
		t.Error("nothing was written for a fresh HMD_HOME")
	}
}

// The region now rides in each location's credential scope rather than a
// shell-level map, so only the resolver is a base-config concern --
// TestAPILocationUsesTheConfiguredRegion covers the other half.
func TestResolverOverride(t *testing.T) {
	t.Parallel()

	r := New(t.TempDir(), fakeEnv(map[string]string{"HMD_LOCAL_NGINX_RESOLVER": "10.0.0.53"}))
	cfg := r.BaseConfig()
	if !strings.Contains(cfg, "resolver 10.0.0.53 valid=10s ipv6=off;") {
		t.Errorf("the resolver override was ignored:\n%s", cfg)
	}
}

func TestProxyContainerOverride(t *testing.T) {
	t.Parallel()

	if got := New("", fakeEnv(nil)).ProxyContainerName(); got != "hmd_proxy" {
		t.Errorf("default proxy container = %q", got)
	}
	if got := New("", fakeEnv(map[string]string{"HMD_LOCAL_PROXY_CONTAINER": "other"})).ProxyContainerName(); got != "other" {
		t.Errorf("override ignored, got %q", got)
	}
}

func TestStreamVarNameIsConfigSafe(t *testing.T) {
	t.Parallel()

	tests := []struct {
		parts []string
		want  string
	}{
		{[]string{"floci"}, "ns_floci"},
		{[]string{"env", "my-slug"}, "ns_env_my_slug"},
		{[]string{"trino", "19033"}, "ns_trino_19033"},
		{[]string{"a.b"}, "ns_a_b"},
	}
	for _, tt := range tests {
		if got := StreamVarName(tt.parts...); got != tt.want {
			t.Errorf("StreamVarName(%v) = %q, want %q", tt.parts, got, tt.want)
		}
	}
}

// A reload with a bad config leaves the previous config serving with only a
// message on stderr, which surfaces much later as every route 404ing. The
// validation has to come first, and a failed test must stop the reload.
func TestReloadValidatesBeforeReloading(t *testing.T) {
	t.Parallel()

	var calls [][]string
	exec := func(_ context.Context, container string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{container}, args...))
		return nil, nil
	}

	r := New(t.TempDir(), fakeEnv(nil))
	if err := r.Reload(context.Background(), exec); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("made %d calls, want a test then a reload: %v", len(calls), calls)
	}
	if calls[0][1] != "nginx" || calls[0][2] != "-t" {
		t.Errorf("first call = %v, want nginx -t", calls[0])
	}
	if calls[1][2] != "-s" || calls[1][3] != "reload" {
		t.Errorf("second call = %v, want nginx -s reload", calls[1])
	}
	if calls[0][0] != "hmd_proxy" {
		t.Errorf("exec ran against %q, want hmd_proxy", calls[0][0])
	}
}

// A container that is not there is not a broken config. The daemon's own words
// were buried under "the nginx configuration is invalid", which sent the first
// people to hit it to a file that was fine.
func TestReloadSaysSoWhenTheContainerIsNotThere(t *testing.T) {
	t.Parallel()

	r := New("", fakeEnv(nil))
	err := r.Reload(context.Background(), func(_ context.Context, name string, _ ...string) ([]byte, error) {
		return []byte("Error response from daemon: No such container: " + name), errors.New("exit status 1")
	})
	if err == nil {
		t.Fatal("a missing container reloaded cleanly")
	}
	if !strings.Contains(err.Error(), "hmd_proxy") {
		t.Errorf("the message does not name the container: %v", err)
	}
	if strings.Contains(err.Error(), "configuration is invalid") {
		t.Errorf("a missing container was reported as a broken config: %v", err)
	}
}

func TestReloadStopsWhenTheConfigIsInvalid(t *testing.T) {
	t.Parallel()

	var calls int
	exec := func(context.Context, string, ...string) ([]byte, error) {
		calls++
		return []byte("nginx: [emerg] unknown directive"), errors.New("exit status 1")
	}

	r := New(t.TempDir(), fakeEnv(nil))
	err := r.Reload(context.Background(), exec)
	if err == nil {
		t.Fatal("Reload succeeded with an invalid config")
	}
	if calls != 1 {
		t.Errorf("made %d calls, want only the validation", calls)
	}
	// The message has to carry nginx's own diagnosis, or the operator has
	// nothing to act on.
	if !strings.Contains(err.Error(), "unknown directive") {
		t.Errorf("error %q does not carry nginx's output", err)
	}
}

// A passthrough may carry a large body -- a ChangeSet definition for an
// environment declaring 28 instances exceeds nginx's 1 MB default -- and the
// failure would be an nginx 413 the upstream never sees.
func TestPassthroughLocationLiftsTheBodySizeCap(t *testing.T) {
	t.Parallel()

	got := passthroughLocation("authd", "http://hmd_authd:8080")
	if !strings.Contains(got, "client_max_body_size 0;") {
		t.Errorf("passthrough location caps the body size:\n%s", got)
	}
}

// The resolver's stream block was never exercised: every test called
// WriteControlPlaneStreams with a dnsPort of 0, so the one listener that carries
// a different protocol, a different upstream shape and proxy_responses went out
// untested (NERD026 SPEC001).
func TestControlPlaneStreamsCarryTheResolver(t *testing.T) {
	t.Parallel()

	r := New(t.TempDir(), fakeEnv(nil))
	if err := r.WriteControlPlaneStreams("neuronsphere", "hmd_dnsd", 19154); err != nil {
		t.Fatalf("WriteControlPlaneStreams: %v", err)
	}
	got := readFragment(t, filepath.Join(r.StreamDir(), ControlPlaneFragment))

	for _, want := range []string{
		"listen 19154 udp;",
		"hmd_dnsd:19154",
		// One query, one answer. Without it nginx holds the session open until
		// proxy_timeout and spends a worker connection per lookup.
		"proxy_responses 1;",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the resolver stream block is missing %q:\n%s", want, got)
		}
	}
	// Floci's own listener is the control: a change that dropped it while adding
	// the resolver would otherwise read as a pass.
	if !strings.Contains(got, "listen 4566;") {
		t.Errorf("the Floci stream listener is gone:\n%s", got)
	}
}

// A resolver that is turned off leaves no listener behind -- and the Floci
// stream still has to be there, because a fragment rewritten without it takes
// every presigned URL down with it.
func TestControlPlaneStreamsOmitTheResolverWhenItIsOff(t *testing.T) {
	t.Parallel()

	r := New(t.TempDir(), fakeEnv(nil))
	if err := r.WriteControlPlaneStreams("neuronsphere", "", 0); err != nil {
		t.Fatalf("WriteControlPlaneStreams: %v", err)
	}
	got := readFragment(t, filepath.Join(r.StreamDir(), ControlPlaneFragment))
	if strings.Contains(got, "udp") {
		t.Errorf("a disabled resolver still wrote a UDP listener:\n%s", got)
	}
	if !strings.Contains(got, "listen 4566;") {
		t.Errorf("the Floci stream listener is gone:\n%s", got)
	}
}
