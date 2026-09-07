package authd

import (
	"net/url"
	"path/filepath"
	"strings"
)

// IssuerEnv names the one URL this server is reached at.
//
// One variable, read by both the service and the CLI, because the `iss` claim
// has to be a single string however the server was reached: the browser goes
// through hmd_proxy on the host, application pods resolve it through a CoreDNS
// record, and Floci's Lambda containers through a Docker network alias. All
// three must land on the same name, or a consumer that fetched JWKS under one
// and reads `iss` as another rejects every token -- and the failure surfaces as
// a policy denial, nowhere near the cause.
const IssuerEnv = "HMD_LOCAL_AUTH_ISSUER"

// DefaultIssuerBase is that name.
//
// Under the same `*.local.neuronsphere.io` suffix the ingress hosts use
// (router.IngressDomain), so the host-side resolution people already have for
// the UIs covers this too. Plain http on purpose: nothing in scope requires
// TLS. Authlib, which drives Superset's and Airflow's OAuth, enforces no
// scheme, and the Rego policies decode tokens without verifying them. The one
// consumer that would demand https is okta_jwt_verifier inside
// hmd-lib-auth.verify_token, which the OPA authorizer calls -- and that is
// deliberately not yet wired up locally.
const DefaultIssuerBase = "http://auth.local.neuronsphere.io"

// KeyDir is where the signing key lives, under $HMD_HOME so the control-plane
// container and the CLI on the host read the same one -- the compose service
// mounts $HMD_HOME at its own absolute path, so the paths coincide.
func KeyDir(home string) string {
	return filepath.Join(home, ".cache", "neuronsphere", "authd")
}

// EnabledEnv turns the identity provider on.
const EnabledEnv = "HMD_LOCAL_NEURONSPHERE_ENABLE_AUTH"

// Lookup resolves an environment variable, returning "" when unset.
type Lookup = func(string) string

// Enabled reports whether the identity provider should run.
//
// Off by default, and the default matters more here than for the DAG runner.
// Turning this on is what makes an application ask for a login and a service
// reject an unauthenticated call, so a default-on switch would break every
// local script, curl and Robot suite on the first `up` after it landed -- none
// of which carries a token today.
func Enabled(lookup Lookup) bool {
	if lookup == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(lookup(EnabledEnv))) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// IssuerBase is the one URL the identity provider is reached at.
func IssuerBase(lookup Lookup) string {
	if lookup != nil {
		if v := strings.TrimSpace(lookup(IssuerEnv)); v != "" {
			return v
		}
	}
	return DefaultIssuerBase
}

// Host is the hostname the issuer resolves to, or "" when auth is off.
//
// Derived from the issuer rather than configured beside it, so the two cannot
// disagree: whatever answers for this name has to be the thing the issuer
// claims to be, or a consumer fetches keys from one server and rejects tokens
// minted by another.
//
// These three live here, in a leaf package, rather than in controlplane where
// the rest of the control-plane switches are. internal/environment needs the
// hostname for a CoreDNS record and deliberately does not import
// internal/controlplane -- the same decoupling ControlPlaneTeardown exists to
// preserve -- so the shared knowledge sits below both.
func Host(lookup Lookup) string {
	if !Enabled(lookup) {
		return ""
	}
	parsed, err := url.Parse(IssuerBase(lookup))
	if err != nil || parsed.Host == "" {
		return ""
	}
	return parsed.Hostname()
}
