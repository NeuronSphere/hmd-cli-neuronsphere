package manifest

import "sort"

// Scope is which manifest a document is: an environment's, or the control
// plane's. The two share a schema (SPEC001) and differ in what they may name,
// so the scope is what selects the reserved set and the wording of a refusal.
//
// The zero value is ScopeEnvironment, which keeps a Manifest built in memory --
// by `repo add` against a slug that has none -- validating as it always did.
type Scope int

const (
	// ScopeEnvironment is $HMD_HOME/environments/<slug>.yaml.
	ScopeEnvironment Scope = iota
	// ScopeControlPlane is $HMD_HOME/.config/control-plane.yaml.
	ScopeControlPlane
)

// ControlPlaneName is the only value a control-plane manifest's `name` may
// take. A manifest naming anything else is rejected rather than applied,
// because the only thing another name could mean is that the file was meant to
// be an environment manifest and landed in the wrong directory.
const ControlPlaneName = "control-plane"

// reservedInstanceNames are the instance names the environment substrate owns.
//
// A manifest may not declare one. They are created by `env apply` itself, so
// treating one as user-declared would make it eligible for removal the moment
// it left the manifest -- and removing the environment's database or cluster
// because a line was deleted is not a reconcile anyone wants.
//
// These duplicate the constants in internal/bom, which cannot be imported here
// without a cycle (bom builds entries *from* a manifest). TestReservedNames-
// MatchTheSubstrate in that package fails if the two ever drift, so the
// duplication is checked rather than trusted.
var reservedInstanceNames = map[string]bool{
	"local-neuronsphere": true,
	"base-vpc":           true,
	"environment-db":     true,
	"eks-cluster":        true,
}

// controlPlaneReservedNames are the names the control plane already uses, per
// NERD004 SPEC011: its own three instances, the three bootstrap services, and
// the service keys in the bundled compose file.
//
// The service keys are not a collision hazard -- SPEC004 prefixes an extension's
// service keys with its instance name, so an instance called "proxy" would key
// a service "proxy-something" and collide with nothing. They are refused
// because the *name* would read in `status` as though it were the control
// plane's own proxy, and removing that ambiguity costs nothing.
//
// The environment substrate's names are refused here too. They are not the
// control plane's, but an instance called eks-cluster in a control-plane
// manifest is a file that landed in the wrong directory, which is the same
// mistake SPEC001's fixed `name` catches one level up.
var controlPlaneReservedNames = map[string]bool{
	"control-plane-vpc":   true,
	"control-plane-db":    true,
	"control-plane-graph": true,
	"hmd-ms-naming":       true,
	"hmd-ms-artifact-lib": true,
	"hmd-ms-deployment":   true,
	"proxy":               true,
	"floci":               true,
	"deployment-gui":      true,
	"authd":               true,
	"dnsd":                true,
}

// Reserved reports whether an instance name belongs to the substrate.
func Reserved(instanceName string) bool { return ScopeEnvironment.Reserved(instanceName) }

// ReservedNames lists the substrate instance names, for an error that tells a
// user which names are unavailable rather than only that theirs was.
func ReservedNames() []string { return ScopeEnvironment.ReservedNames() }

// Reserved reports whether this scope forbids an instance name.
func (s Scope) Reserved(instanceName string) bool {
	if reservedInstanceNames[instanceName] {
		return true
	}
	return s == ScopeControlPlane && controlPlaneReservedNames[instanceName]
}

// ReservedNames lists every name this scope forbids, sorted.
func (s Scope) ReservedNames() []string {
	names := make([]string, 0, len(reservedInstanceNames)+len(controlPlaneReservedNames))
	for name := range reservedInstanceNames {
		names = append(names, name)
	}
	if s == ScopeControlPlane {
		for name := range controlPlaneReservedNames {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// Noun names what this scope's file is, for a message a reader can act on.
func (s Scope) Noun() string {
	if s == ScopeControlPlane {
		return "control-plane manifest"
	}
	return "environment manifest"
}

// reservedReason says who owns a refused name, which is what tells the user
// whether to rename their instance or move their whole file.
func (s Scope) reservedReason(instanceName string) string {
	if s == ScopeControlPlane && !reservedInstanceNames[instanceName] {
		return "is reserved by the control plane"
	}
	return "is reserved for the environment substrate"
}
