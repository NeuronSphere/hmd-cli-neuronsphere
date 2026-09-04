package manifest

import "sort"

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

// Reserved reports whether an instance name belongs to the substrate.
func Reserved(instanceName string) bool { return reservedInstanceNames[instanceName] }

// ReservedNames lists the substrate instance names, for an error that tells a
// user which names are unavailable rather than only that theirs was.
func ReservedNames() []string {
	names := make([]string, 0, len(reservedInstanceNames))
	for name := range reservedInstanceNames {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
