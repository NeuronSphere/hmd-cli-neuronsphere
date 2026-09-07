package controlplane

import (
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/authd"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
)

// The in-network address, not the host route. hmd-ms-deployment runs as a
// Lambda inside Floci: http://localhost/nsrunner is hmd_proxy's route from the
// host, and localhost inside that Lambda is its own container.
func TestTheRunnerURLIsInNetworkAndOffByDefault(t *testing.T) {
	t.Parallel()

	off := &Options{Lookup: func(string) string { return "" }}
	if got := RunnerInternalURL(off); got != "" {
		t.Errorf("RunnerInternalURL with no runner = %q, want empty", got)
	}
	if route := RunnerRoute(off); route != nil {
		t.Errorf("RunnerRoute with no runner = %v, want nil", route)
	}

	on := &Options{Lookup: func(k string) string {
		if k == "HMD_LOCAL_NEURONSPHERE_ENABLE_RUNNER" {
			return "true"
		}
		return ""
	}}
	want := "http://hmd_nsrunner:8080"
	if got := RunnerInternalURL(on); got != want {
		t.Errorf("RunnerInternalURL = %q, want %q", got, want)
	}
	// The proxy passthrough and the Lambda's variable must name the same place;
	// they were two literals before.
	if got := RunnerRoute(on)[RunnerRoutePath]; got != want {
		t.Errorf("RunnerRoute[%s] = %q, want %q", RunnerRoutePath, got, want)
	}
}

// The identity provider is off unless asked for, and the default is load-
// bearing: turning it on is what makes an application demand a login and a
// service reject an unauthenticated call, so a default-on switch would break
// every local script, curl and Robot suite that carries no token today.
func TestTheIdentityProviderIsOffUnlessAskedFor(t *testing.T) {
	t.Parallel()

	off := &Options{Lookup: func(string) string { return "" }}
	if AuthEnabled(off) {
		t.Error("auth is on with nothing set")
	}
	if host := AuthHost(off); host != "" {
		t.Errorf("AuthHost = %q while auth is off; the vhost and the CoreDNS record key off this", host)
	}

	for _, value := range []string{"true", "1", "yes", "TRUE"} {
		on := &Options{Lookup: func(k string) string {
			if k == authd.EnabledEnv {
				return value
			}
			return ""
		}}
		if !AuthEnabled(on) {
			t.Errorf("%q did not enable it", value)
		}
	}
}

// The hostname is derived from the issuer rather than configured beside it, so
// the two cannot disagree: whatever answers for the name has to be the server
// the issuer claims to be, or a consumer fetches keys from one and rejects
// tokens minted by the other.
func TestTheAuthHostIsDerivedFromTheIssuer(t *testing.T) {
	t.Parallel()

	opts := &Options{Lookup: func(k string) string {
		switch k {
		case authd.EnabledEnv:
			return "true"
		case authd.IssuerEnv:
			return "http://idp.example.test:9999"
		}
		return ""
	}}
	if got := AuthHost(opts); got != "idp.example.test" {
		t.Errorf("AuthHost = %q, want the issuer's hostname", got)
	}
	if got := AuthIssuerBase(opts); got != "http://idp.example.test:9999" {
		t.Errorf("AuthIssuerBase = %q", got)
	}
}

// Both services are the same image with different arguments, so the compose
// overlay has to name it whenever either is enabled -- not only for the runner.
// Without this, enabling auth alone leaves the compose file's own default in
// play, a bare hmd-img-nsrunner:latest that only a hand build ever creates.
func TestEnablingAuthAloneStillPinsTheImage(t *testing.T) {
	t.Parallel()

	opts := &Options{Lookup: func(k string) string {
		if k == authd.EnabledEnv {
			return "true"
		}
		return ""
	}}
	env := ComposeEnv(opts, &registry.Registry{}, "")
	if ref := env("HMD_NSRUNNER_IMAGE"); ref == "" {
		t.Error("the image is unset with auth enabled and the runner off")
	}
	if issuer := env(authd.IssuerEnv); issuer != authd.DefaultIssuerBase {
		t.Errorf("the container was not told its issuer: %q", issuer)
	}
}
