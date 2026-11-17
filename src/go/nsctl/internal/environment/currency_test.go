package environment

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/floci"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/router"
)

// fakeLambda answers GetFunction for hmd_ms_dbaccount with the given
// environment variables, or 404 when vars is nil, and records every request it
// saw so a test can assert what a reconcile did and did not touch.
func fakeLambda(t *testing.T, vars map[string]string) (floci.Target, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if vars == nil || !strings.HasSuffix(r.URL.Path, "/functions/hmd_ms_dbaccount") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"Type":"User","Message":"Function not found"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"Configuration": map[string]any{
				"FunctionName": "hmd_ms_dbaccount",
				"Environment":  map[string]any{"Variables": vars},
			},
		})
	}))
	t.Cleanup(srv.Close)
	target := floci.ControlPlane(func(k string) string {
		if k == "FLOCI_ENDPOINT" {
			return srv.URL
		}
		return ""
	})
	return target, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
}

// The deployed version is what the Lambda's HMD_REPO_VERSION says, and a
// function that is absent, or carries no version, reads as the empty version.
//
// Requirements: HMD_CLI_NEURONSPHERE_NERD024_SPEC002
func TestDeployedVersionIsReadFromTheLambdasHMDRepoVersion(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		vars map[string]string
		want string
	}{
		{"deployed", map[string]string{"HMD_REPO_VERSION": "0.1.47", "OTHER": "x"}, "0.1.47"},
		{"no version recorded", map[string]string{"OTHER": "x"}, ""},
		{"function absent", nil, ""},
	} {
		target, _ := fakeLambda(t, c.vars)
		services, err := floci.NewServices(context.Background(), target)
		if err != nil {
			t.Fatal(err)
		}
		if got := deployedVersion(context.Background(), services, DBAccountRepoClass); got != c.want {
			t.Errorf("%s: deployedVersion = %q, want %q", c.name, got, c.want)
		}
	}
}

// The deployed version is compared with what ResolveVersion resolves for this
// binary: equal and well-named does nothing beyond the one read, and any other
// version is reported as the deployed one before a refresh.
//
// Requirements: HMD_CLI_NEURONSPHERE_NERD024_SPEC002, HMD_CLI_NEURONSPHERE_NERD024_SPEC003
func TestReconcileDBAccountDoesNothingBeyondTheOneReadWhenCurrent(t *testing.T) {
	t.Parallel()

	const resolved = "7.7.7"
	opts := testOptions(t.TempDir(), map[string]string{"HMD_LOCAL_VERSION_HMD_MS_DBACCOUNT": resolved})
	target, requests := fakeLambda(t, map[string]string{
		"HMD_REPO_VERSION":     resolved,
		"HMD_ENVIRONMENT_NAME": "scratch",
	})

	state, err := ReconcileDBAccount(context.Background(), opts, nil, nil, nil, nil,
		router.Env{}, target, floci.Names{Environment: "scratch"})
	if err != nil {
		t.Fatalf("ReconcileDBAccount: %v", err)
	}
	if state.Resolved != resolved || state.Deployed != resolved || state.Serving != resolved {
		t.Errorf("state = %+v, want deployed, resolved and serving all %s", state, resolved)
	}
	if state.Changed {
		t.Error("a current service was reported as changed")
	}
	got := requests()
	if len(got) != 1 || !strings.HasPrefix(got[0], "GET ") {
		t.Errorf("requests = %v, want exactly one read and nothing written", got)
	}
}

// hmd-ms-base 0.2.282 is the declared floor, and the reason it is a floor --
// the credential scope parsed as a bearer token in middleware, every request a
// bare 500 -- is what a below-floor failure states.
//
// Requirements: HMD_CLI_NEURONSPHERE_NERD024_SPEC005
func TestMSBaseFloorIsDeclaredWithItsReason(t *testing.T) {
	t.Parallel()

	if MSBaseFloor != "0.2.282" {
		t.Fatalf("MSBaseFloor = %q, want 0.2.282", MSBaseFloor)
	}
	msg := brokenRoute(nil, "local", "hmd_ms_dbaccount", "0.1.46", "0.1.47", 500)
	for _, want := range []string{"hmd-ms-base " + MSBaseFloor, "bearer token", "middleware", "500 with no body"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the floor's reason lacks %q:\n%s", want, msg)
		}
	}
}
