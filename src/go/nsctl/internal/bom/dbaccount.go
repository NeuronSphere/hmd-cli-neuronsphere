package bom

import (
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
)

// DBAccountConsumerClass is the repo class of an instance that asks for a
// database account.
//
// It is the consumer, not the service: hmd-database-account's deploy posts to
// hmd-ms-dbaccount, and its create-service role is what selects that service's
// microservice Resource (see foundationServices). A repository that wants a
// database declares one of these -- airflow-db-account, trino-db-account -- so
// the presence of the class in the assembled BOM is the presence of demand.
const DBAccountConsumerClass = "hmd-database-account"

// RequiresDBAccount reports whether anything in entries asks for a database
// account, and therefore whether the environment needs the dbaccount service
// deployed (NERD024 SPEC001).
//
// Matched on the class rather than on a role name, which is the opposite of
// RequiresGraph and for a reason: a graph is asked for under three different
// role spellings by three different repos, while a database account is always
// a declared instance of one class. The instance is the demand.
//
// Computed from the assembled BOM rather than from what a run is about to
// deploy: an environment whose db-account instances are all deployed already
// still needs the service they were deployed through, because the next thing
// added to it will call the same service.
func RequiresDBAccount(entries []Entry) bool {
	for _, e := range entries {
		if e.RepoClassName == DBAccountConsumerClass {
			return true
		}
	}
	return false
}

// DeclaresDBAccount is RequiresDBAccount asked of an environment manifest's
// declarations rather than of assembled entries.
//
// `env start` needs the same answer before it has built a BOM -- it reads the
// manifest only to refuse core bindings -- and assembling one there merely to
// ask this would make Start depend on the environment's Floci account, which
// it has not resolved yet.
func DeclaresDBAccount(repos []manifest.Repo) bool {
	for _, r := range repos {
		if r.RepoClassName == DBAccountConsumerClass {
			return true
		}
	}
	return false
}
