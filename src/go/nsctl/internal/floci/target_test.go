package floci

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func fakeEnv(vars map[string]string) Lookup {
	return func(key string) string { return vars[key] }
}

func TestControlPlaneTarget(t *testing.T) {
	t.Parallel()

	got := ControlPlane(fakeEnv(nil))
	if got.AccountID != ControlPlaneAccountID || got.AccessKeyID != ControlPlaneAccountID {
		t.Errorf("account/key = %q/%q, want %q", got.AccountID, got.AccessKeyID, ControlPlaneAccountID)
	}
	if got.Endpoint != DefaultEndpoint() || got.InternalEndpoint != InternalEndpoint {
		t.Errorf("endpoints = %q / %q", got.Endpoint, got.InternalEndpoint)
	}
	if got.Container != "floci" || got.Alias != "neuronsphere" {
		t.Errorf("container/alias = %q/%q", got.Container, got.Alias)
	}
}

// SPEC007: the access key selects the account and the endpoint does not. Every
// account shares one container, one endpoint and one alias.
func TestForAccountDiffersOnlyByTheAccessKey(t *testing.T) {
	t.Parallel()

	cp := ControlPlane(fakeEnv(nil))
	env := ForAccount(fakeEnv(nil), "000000000001", false)

	if env.AccessKeyID != "000000000001" || env.AccountID != "000000000001" {
		t.Errorf("account/key = %q/%q, want the environment's", env.AccountID, env.AccessKeyID)
	}
	if env.Endpoint != cp.Endpoint {
		t.Errorf("endpoint = %q, want the same as the control plane -- the endpoint does not select the account", env.Endpoint)
	}
	if env.Container != cp.Container || env.Alias != cp.Alias {
		t.Errorf("container/alias diverged: %q/%q vs %q/%q", env.Container, env.Alias, cp.Container, cp.Alias)
	}
}

// A legacy-layout environment *is* the control-plane account.
func TestForAccountResolvesLegacyLayoutToTheControlPlane(t *testing.T) {
	t.Parallel()

	got := ForAccount(fakeEnv(nil), "000000000001", true)
	if got.AccountID != ControlPlaneAccountID || got.AccessKeyID != ControlPlaneAccountID {
		t.Errorf("legacy target = %q/%q, want the control plane's account", got.AccountID, got.AccessKeyID)
	}
}

func TestEndpointOverrides(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"the default", nil, DefaultEndpoint()},
		{"FLOCI_ENDPOINT wins", map[string]string{"FLOCI_ENDPOINT": "http://x:1"}, "http://x:1"},
		{"MINISTACK_ENDPOINT is the legacy name", map[string]string{"MINISTACK_ENDPOINT": "http://y:2"}, "http://y:2"},
		{"FLOCI_ENDPOINT beats MINISTACK_ENDPOINT", map[string]string{"FLOCI_ENDPOINT": "http://x:1", "MINISTACK_ENDPOINT": "http://y:2"}, "http://x:1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ControlPlane(fakeEnv(tt.env)).Endpoint; got != tt.want {
				t.Errorf("endpoint = %q, want %q", got, tt.want)
			}
		})
	}
}

// The account must reach the SDK as the access key id, never from the ambient
// environment: signing with the wrong key succeeds against the wrong account.
func TestConfigSignsWithTheAccountID(t *testing.T) {
	t.Parallel()

	target := ForAccount(fakeEnv(nil), "000000000042", false)
	cfg, err := target.Config(context.Background())
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	creds, err := cfg.Credentials.Retrieve(context.Background())
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if creds.AccessKeyID != "000000000042" {
		t.Errorf("access key = %q, want the account id", creds.AccessKeyID)
	}
	if cfg.Region != DefaultRegion {
		t.Errorf("region = %q, want %q", cfg.Region, DefaultRegion)
	}
}

func TestWaitForHealthReturnsOnA200(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_floci/health" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	target := ControlPlane(fakeEnv(map[string]string{"FLOCI_ENDPOINT": srv.URL}))
	if err := target.WaitForHealth(context.Background(), 5*time.Second, nil); err != nil {
		t.Errorf("WaitForHealth: %v", err)
	}
}

func TestWaitForHealthTimesOutWithAnAddressableMessage(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	target := ControlPlane(fakeEnv(map[string]string{"FLOCI_ENDPOINT": srv.URL}))
	target.PollInterval = time.Millisecond
	err := target.WaitForHealth(context.Background(), 20*time.Millisecond, nil)
	if err == nil {
		t.Fatal("WaitForHealth succeeded against a 503")
	}
	if !strings.Contains(err.Error(), srv.URL) {
		t.Errorf("error %q does not name the endpoint", err)
	}
}

func TestWaitForHealthHonoursCancellation(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	target := ControlPlane(fakeEnv(map[string]string{"FLOCI_ENDPOINT": srv.URL}))
	if err := target.WaitForHealth(ctx, time.Minute, nil); err == nil {
		t.Error("WaitForHealth ignored a cancelled context")
	}
}
