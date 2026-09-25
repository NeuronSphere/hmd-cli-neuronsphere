package hosturl

import "testing"

func TestDefaultsAreTheHistoricalURLs(t *testing.T) {
	Reset()
	if got := Base(); got != "http://localhost" {
		t.Errorf("Base = %q, want http://localhost with no port", got)
	}
	if got := Route("hmd_ms_deployment"); got != "http://localhost/hmd_ms_deployment" {
		t.Errorf("Route = %q", got)
	}
	if got := Floci(); got != "http://localhost:4566" {
		t.Errorf("Floci = %q", got)
	}
}

func TestAMovedPortReachesEveryURL(t *testing.T) {
	Apply2(8080, 14566)
	defer Reset()

	if got := Base(); got != "http://localhost:8080" {
		t.Errorf("Base = %q", got)
	}
	if got := Route("/local/transform/"); got != "http://localhost:8080/local/transform" {
		t.Errorf("Route = %q", got)
	}
	if got := Floci(); got != "http://localhost:14566" {
		t.Errorf("Floci = %q", got)
	}
}

// Zero means "not known", not "reset to default" -- a half-read registry must
// not silently move the other port back.
func TestApplyIgnoresZero(t *testing.T) {
	Apply2(8080, 14566)
	defer Reset()

	Apply(0, 0)
	if HTTPPort() != 8080 || FlociPort() != 14566 {
		t.Errorf("zero reset the ports: %d, %d", HTTPPort(), FlociPort())
	}
}

// A user interface is reached at a hostname, not at localhost, but it is served
// by the same proxy on the same port -- so the port has to follow the hostname.
// It did not: the start summary built "http://" + host + "/" by hand and printed
// an unreachable link on any home whose HTTP port had moved (NERD025 SPEC006).
func TestHostBaseCarriesThePortOnlyWhenItMoved(t *testing.T) {
	Reset()
	if got := HostBase("airflow.ns.local"); got != "http://airflow.ns.local" {
		t.Errorf("HostBase on the default port = %q, want no port", got)
	}

	Apply2(8080, 14566)
	defer Reset()
	if got := HostBase("airflow.ns.local"); got != "http://airflow.ns.local:8080" {
		t.Errorf("HostBase on a moved port = %q, want :8080", got)
	}
}

// The Floci port is not the HTTP port. A UI is HTTP, so HostBase must not follow
// the stream port when only that one moved.
func TestHostBaseIgnoresTheFlociPort(t *testing.T) {
	Apply2(80, 14566)
	defer Reset()
	if got := HostBase("auth.ns.local"); got != "http://auth.ns.local" {
		t.Errorf("HostBase = %q, want no port", got)
	}
}
