package authd

import "path/filepath"

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
