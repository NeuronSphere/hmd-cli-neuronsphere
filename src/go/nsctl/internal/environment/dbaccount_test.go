package environment

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bom"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
)

// NERD024 SPEC001. The three answers are different questions: the mode decides
// whether the service is possible, the manifest decides whether it is wanted,
// and an unreadable manifest decides nothing.
func TestWantsDBAccount(t *testing.T) {
	t.Parallel()

	consumer := &manifest.Manifest{Repos: []manifest.Repo{
		{InstanceName: "airflow-db-account", RepoClassName: bom.DBAccountConsumerClass},
	}}
	bare := &manifest.Manifest{Repos: []manifest.Repo{
		{InstanceName: "acme-loader", RepoClassName: "acme-loader"},
	}}

	cases := []struct {
		name     string
		mode     manifest.Substrate
		declared *manifest.Manifest
		want     bool
	}{
		{"a consumer wants it", manifest.SubstrateCore, consumer, true},
		{"nothing declares it", manifest.SubstrateCore, bare, false},
		{"an empty manifest declares nothing", manifest.SubstrateCore, &manifest.Manifest{}, false},
		{"an unreadable manifest leaves the mode alone", manifest.SubstrateCore, nil, true},
		{"substrate none runs none of it", manifest.SubstrateNone, consumer, false},
		{"full is core plus a cluster, not plus a service", manifest.SubstrateFull, bare, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := wantsDBAccount(planFor(c.mode), c.declared); got != c.want {
				t.Errorf("wantsDBAccount = %v, want %v", got, c.want)
			}
		})
	}
}

// NERD024 SPEC004: a healthy hmd-ms-base service 404s at its route root,
// because it registers only /api/... and /apiop/.... That is what makes a 5xx
// diagnostic, so the probe must not treat the 404 as a failure.
func TestProbeRouteAcceptsTheHealthy404(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	got, routed := probeRoute(context.Background(), fixedLookup(srv.URL), "local", "hmd_ms_dbaccount")
	if got != http.StatusNotFound {
		t.Fatalf("probeRoute = %d, want 404", got)
	}
	if !routed {
		t.Error("a service's own 404 means the path is routed")
	}
}

func TestProbeRouteReportsA5xxAfterRetrying(t *testing.T) {
	t.Parallel()

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	if got, routed := probeRoute(context.Background(), fixedLookup(srv.URL), "local", "hmd_ms_dbaccount"); got != 500 || !routed {
		t.Fatalf("probeRoute = %d routed=%v, want 500 routed=true", got, routed)
	}
	// Retried rather than believed first time: this runs straight after a
	// deploy, where a cold Lambda and a just-registered gateway both answer
	// badly for a moment.
	if n := atomic.LoadInt32(&calls); n != routeProbeAttempts {
		t.Errorf("probed %d times, want %d", n, routeProbeAttempts)
	}
}

// The repair depends on whether a restart would change anything, which is the
// whole point of naming both versions.
func TestBrokenRouteNamesTheRepairThatWouldWork(t *testing.T) {
	t.Parallel()

	stale := brokenRoute(nil, "local", "hmd_ms_dbaccount", "0.1.46", "0.1.47", 500)
	if !strings.Contains(stale, "nsctl env start local") {
		t.Errorf("a stale service is repaired by a restart, got:\n%s", stale)
	}
	if !strings.Contains(stale, "0.1.46") || !strings.Contains(stale, "0.1.47") {
		t.Errorf("both versions belong in the message, got:\n%s", stale)
	}

	current := brokenRoute(nil, "local", "hmd_ms_dbaccount", "0.1.47", "0.1.47", 500)
	if strings.Contains(current, "nsctl env start local") {
		t.Errorf("restarting redeploys the same broken image; do not suggest it, got:\n%s", current)
	}
	if !strings.Contains(current, "HMD_LOCAL_VERSION_HMD_MS_DBACCOUNT") {
		t.Errorf("name the pin that can change the outcome, got:\n%s", current)
	}

	// The sentence this whole change exists for. An hour was spent on the
	// original failure because nothing ruled authentication out.
	for _, msg := range []string{stale, current} {
		if !strings.Contains(msg, "not an authentication failure") {
			t.Errorf("the message must rule authentication out, got:\n%s", msg)
		}
		if !strings.Contains(msg, MSBaseFloor) {
			t.Errorf("the floor must be named, got:\n%s", msg)
		}
	}

	// Nothing answering at all is a different sentence from an error code.
	if dead := brokenRoute(nil, "local", "hmd_ms_dbaccount", "", "0.1.47", -1); !strings.Contains(dead, "does not answer") {
		t.Errorf("an unanswered route should say so, got:\n%s", dead)
	}
}

func fixedLookup(base string) func(string) string {
	return func(key string) string {
		if key == "HMD_LOCAL_PROXY_URL" {
			return base
		}
		return ""
	}
}

// hmd_proxy answers 404 with this body for a path it is not routing, and a
// healthy hmd-ms-base service answers 404 too. Telling them apart is the whole
// point: the acceptance run for NERD024 deployed a deliberately broken 0.1.46,
// probed in the moment between writing the fragment and nginx reloading it,
// read the proxy's 404 as the service's, and passed.
func TestProbeRouteDoesNotReadTheProxys404AsHealth(t *testing.T) {
	t.Parallel()

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error": "no route defined"}`))
	}))
	defer srv.Close()

	code, routed := probeRoute(context.Background(), fixedLookup(srv.URL), "local", "hmd_ms_dbaccount")
	if routed {
		t.Errorf("the proxy's own 404 is not the service answering (code %d)", code)
	}
	// And it keeps waiting rather than concluding, because the usual cause is
	// a reload that has not happened yet.
	if n := atomic.LoadInt32(&calls); n != routeProbeAttempts {
		t.Errorf("probed %d times, want %d", n, routeProbeAttempts)
	}
}
