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
