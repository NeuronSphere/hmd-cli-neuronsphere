package environment

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/hosturl"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/repoclass"
)

// DefaultProxyBaseURL is hmd_proxy as seen from the host, on the historical port.
//
// Kept as a constant for the callers that compare against it; the live default
// comes from hosturl, which carries whichever port this home actually publishes
// (NERD025 SPEC008).
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
	return hosturl.Base()
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

// noRouteBody is what hmd_proxy answers when *it* has no route for a path.
//
// It matters because that answer is a 404, and a healthy hmd-ms-base service
// answers 404 too -- so without telling the two apart, a probe run in the
// moment between writing the fragment and nginx reloading it reads "nginx does
// not know this path yet" as "the service is fine". Found exactly that way: the
// first apply of a deliberately broken 0.1.46 passed the probe and failed at
// the deploy node instead, which is the failure this check exists to pre-empt.
const noRouteBody = "no route defined"

// probeBodyLimit is how much of the body is read to recognise that answer. It
// is one short JSON object; anything longer is a real service's response and
// only needs to not be mistaken for it.
const probeBodyLimit = 512

// probeRoute reports the status code a service's route root answers with, and
// whether the proxy is routing that path at all.
//
// The code is -1 when nothing answers. routed is false when hmd_proxy answered
// that it has no such route, which is not a verdict on the service: nothing has
// reached it.
//
// Retried, because this runs immediately after a deploy: nginx reloads a moment
// after the fragment is written, Floci registers the gateway's routes a moment
// after the stage is deployed, and the first request pays the Lambda's cold
// start.
func probeRoute(ctx context.Context, lookup func(string) string, slug, service string) (int, bool) {
	return probeRouteN(ctx, lookup, slug, service, routeProbeAttempts)
}

// probeRouteN is probeRoute with the retry budget named.
//
// `doctor` asks for one attempt: nothing was just deployed, so there is no cold
// start to wait out, and a report that takes five seconds per environment to
// say "ok" is a report people stop running.
func probeRouteN(ctx context.Context, lookup func(string) string, slug, service string, attempts int) (int, bool) {
	client := &http.Client{Timeout: routeProbeTimeout}
	url := ServiceRouteURL(lookup, slug, service)
	code, routed := -1, false
	for attempt := 1; attempt <= attempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return -1, false
		}
		resp, err := client.Do(req)
		if err == nil {
			code = resp.StatusCode
			body, _ := io.ReadAll(io.LimitReader(resp.Body, probeBodyLimit))
			resp.Body.Close()
			routed = !(code == http.StatusNotFound && strings.Contains(string(body), noRouteBody))
			// Anything below 500 from the service itself is an answer:
			// hmd-ms-base registers only /api/... and /apiop/... routes, so a
			// healthy service 404s at its route root. That is the distinction
			// internal/status already relies on, and it is what makes a 5xx
			// here diagnostic rather than ambiguous (NERD024 SPEC004). A 404
			// from the proxy is not that, so it keeps waiting instead.
			if routed && code < 500 {
				return code, true
			}
		}
		if attempt == attempts {
			return code, routed
		}
		select {
		case <-ctx.Done():
			return code, routed
		case <-time.After(routeProbeBackoff):
		}
	}
	return code, routed
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
