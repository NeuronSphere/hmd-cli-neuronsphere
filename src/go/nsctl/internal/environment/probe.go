package environment

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/repoclass"
)

// DefaultProxyBaseURL is hmd_proxy as seen from the host.
const DefaultProxyBaseURL = "http://localhost"

// ProxyBaseURL is where to reach the proxy.
//
// Overridable for the same reason MSDeploymentURL is: a hardcoded localhost
// would make every unit test depend on whatever is listening on port 80 of the
// machine running it.
func ProxyBaseURL(lookup func(string) string) string {
	if lookup != nil {
		if v := lookup("HMD_LOCAL_PROXY_URL"); v != "" {
			return strings.TrimRight(v, "/")
		}
	}
	return DefaultProxyBaseURL
}

// ServiceRouteURL is a service's route root under an environment's prefix.
func ServiceRouteURL(lookup func(string) string, slug, service string) string {
	return fmt.Sprintf("%s/%s/%s/", ProxyBaseURL(lookup), slug, service)
}

// The route root is probed rather than an operation: the operations are POSTs
// that do something, and the question here is only whether the service answers.
const (
	routeProbeAttempts = 5
	routeProbeBackoff  = 500 * time.Millisecond
	routeProbeTimeout  = 10 * time.Second
)

// probeRoute reports the status code a service's route root answers with, and
// -1 when nothing answers at all.
//
// Retried, because this runs immediately after a deploy: Floci registers the
// gateway's routes a moment after the stage is deployed, and the first request
// also pays the Lambda's cold start.
func probeRoute(ctx context.Context, lookup func(string) string, slug, service string) int {
	return probeRouteN(ctx, lookup, slug, service, routeProbeAttempts)
}

// probeRouteN is probeRoute with the retry budget named.
//
// `doctor` asks for one attempt: nothing was just deployed, so there is no cold
// start to wait out, and a report that takes five seconds per environment to
// say "ok" is a report people stop running.
func probeRouteN(ctx context.Context, lookup func(string) string, slug, service string, attempts int) int {
	client := &http.Client{Timeout: routeProbeTimeout}
	url := ServiceRouteURL(lookup, slug, service)
	code := -1
	for attempt := 1; attempt <= attempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return -1
		}
		resp, err := client.Do(req)
		if err == nil {
			code = resp.StatusCode
			resp.Body.Close()
			// Anything below 500 is an answer: hmd-ms-base registers only
			// /api/... and /apiop/... routes, so a healthy service 404s at its
			// route root. That is the distinction internal/status already
			// relies on, and it is what makes a 5xx here diagnostic rather
			// than ambiguous (NERD024 SPEC004).
			if code < 500 {
				return code
			}
		}
		if attempt == attempts {
			return code
		}
		select {
		case <-ctx.Done():
			return code
		case <-time.After(routeProbeBackoff):
		}
	}
	return code
}

// brokenRoute explains a 5xx from an environment route in full, which is what
// NERD024 SPEC004 asks for and what the original failure did not do.
//
// deployed is what Floci is serving and want is what this binary resolves.
// They decide the repair, and getting that right is the point: when they
// differ, a restart deploys the newer one; when they agree, a restart deploys
// the same broken image again, and saying "restart it" would send the reader
// round the loop they are already in.
//
// The last line is not padding. The failure this replaces was a bare 500 from a
// service behind a header the proxy rewrites, and the first conclusion drawn
// from it -- by a person, and then by an agent -- was that the caller needed to
// sign in. Nothing local sends a credential, so that reading costs an hour and
// cannot succeed.
func brokenRoute(lookup func(string) string, slug, service, deployed, want string, code int) string {
	answered := fmt.Sprintf("answers %d", code)
	if code < 0 {
		answered = "does not answer"
	}
	version := deployed
	if version == "" {
		version = "an unrecorded version"
	}

	var repair string
	switch {
	case deployed == "" || deployed == want:
		repair = fmt.Sprintf(
			"This nsctl resolves %s, which is what is deployed, so restarting the environment redeploys\n"+
				"  the same image. Upgrade nsctl, or pin a known-good one with %s=<version>.",
			want, repoclass.VersionEnvVar(DBAccountRepoClass))
	default:
		repair = fmt.Sprintf(
			"This nsctl resolves %s. Run `nsctl env start %s` to redeploy the service at that version.",
			want, slug)
	}

	return fmt.Sprintf(
		"%s %s at %s, where a healthy service answers 404.\n"+
			"  Deployed: %s. A service reached through an environment route must be built on hmd-ms-base %s or\n"+
			"  newer; below that, the SigV4 credential scope the proxy must put in Authorization is parsed as a\n"+
			"  bearer token inside its middleware, and every request returns a 500 with no body.\n"+
			"  %s\n"+
			"  This is not an authentication failure: no local command sends a credential, and signing in\n"+
			"  changes nothing.",
		service, answered, ServiceRouteURL(lookup, slug, service), version, MSBaseFloor, repair)
}
