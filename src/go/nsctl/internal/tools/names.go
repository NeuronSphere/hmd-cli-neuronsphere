// Package tools ports the handful of hmd_cli_tools helpers nsctl needs.
//
// These are contracts, not conveniences. The microservices that read what
// nsctl names stay Python and compute the same name independently, so a
// one-character difference here surfaces as a runtime failure inside a service,
// far from the Go code that caused it.
package tools

import "strings"

// standardNameLimit is the length at which make_standard_name starts shortening.
// The comparison is >=, not >, in the Python; reproduced exactly.
const standardNameLimit = 64

// MakeStandardName ports hmd_cli_tools.make_standard_name bit-for-bit.
//
// It joins the six parts with underscores and, if the result reaches 64
// characters, retries with the environment abbreviated to its first character
// and the customer code to its last. If that still reaches 64, it falls back to
// four parts. The two fallbacks are lossy on purpose -- a longer name would be
// rejected downstream -- and the exact thresholds matter because ms-dbaccount
// and the CDKTF stacks derive the same name for the same resource.
func MakeStandardName(instanceName, repoName, deploymentID, environment, hmdRegion, customerCode string) string {
	name := strings.Join([]string{
		instanceName, repoName, deploymentID, environment, hmdRegion, customerCode,
	}, "_")
	if len(name) < standardNameLimit {
		return name
	}

	env := environment
	if env != "" {
		env = env[:1]
	}
	cc := customerCode
	if cc != "" {
		cc = cc[len(cc)-1:]
	}
	name = strings.Join([]string{
		instanceName, repoName, deploymentID, env, hmdRegion, cc,
	}, "_")
	if len(name) < standardNameLimit {
		return name
	}

	return strings.Join([]string{instanceName, repoName, deploymentID, hmdRegion}, "_")
}

// ResourceIdentifier is MakeStandardName in the form Floci records as its
// io.floci.resource-id label: underscores become hyphens and the whole thing is
// lowercased. Both env_db_identifier and graph_cluster_identifier end with this
// same `base.replace("_", "-").lower()`.
func ResourceIdentifier(instanceName, repoName, deploymentID, environment, hmdRegion, customerCode string) string {
	base := MakeStandardName(instanceName, repoName, deploymentID, environment, hmdRegion, customerCode)
	return strings.ToLower(strings.ReplaceAll(base, "_", "-"))
}
