package environment

import (
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bom"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
)

// desiredEntries is everything an environment deploys: its substrate, what its
// manifest declares, and what nsctl adds on its behalf (the External Secrets
// operator, the on-demand graph), with the ext-secrets configuration patched.
//
// Apply and ComputePlan both build their desired state here. They used to each
// compose it inline, and the plan's copy lost the operator: its binder then saw
// no ext-secrets instance, so a class requiring the role planned as
// missing_required_role while the apply it predicted bound it and succeeded.
func desiredEntries(opts *Options, bomEnv bom.Environment, mode manifest.Substrate, declaredRepos []manifest.Repo) []bom.Entry {
	// The cluster entry only makes sense when k3s is enabled: without it the
	// eks-cluster node deploys a cluster nothing will run.
	substrate := bom.SubstrateFor(bomEnv, mode, k3sEnabled(opts))
	entries := append(substrate, bom.DeclaredWithLocalDefaults(bomEnv, declaredRepos, opts.lookup, opts.warn)...)

	// The External Secrets operator, unless opted out. It reaches nsctl through
	// the environment manifest today, which on a developer machine the Python
	// CLI wrote -- so an environment created by `nsctl env add` alone got a
	// cluster with no operator on it, and the first cloud chart rendering an
	// ExternalSecret failed on the missing CRD. Bundling the repo (SPEC006) is
	// what lets it deploy; declaring it here is what makes it deploy at all.
	//
	// Not in Substrate's own list: substrateInstances decides which of the two
	// changesets an entry lands in, and these belong in the second. The first
	// is applied before the concrete core Resources are submitted, and a
	// dependency carrying a tag_selector cannot resolve against a type nothing
	// has produced yet.
	if deploysExtSecrets(opts, mode) {
		entries = append(entries, bom.ExtSecrets(bomEnv)...)
	}

	// The graph, when something actually wants one. Lazily, for the same reason
	// the Python is lazy: it is a JVM per environment that most local work never
	// touches, so a default environment deploys none.
	//
	// Not gated on the cluster: it is a Floci Neptune cluster, not a chart.
	// Not in Substrate's own list either -- a substrate name is reserved in
	// every environment, and this one is deployed only on demand.
	//
	// Without this a stack that binds graph-db to `global-graph` -- the shape
	// `nsctl stack init --from-env` produces, and what hmd-stack-analytics
	// ships -- fails at register time on an instance that never existed:
	//
	//	AssertionError: No repo instance found for name, global-graph
	//
	// The alias answered on the network throughout, which is what hid this.
	if graphEnabled(opts) {
		if bom.RequiresGraph(entries) && !bom.DeclaresGraph(entries) {
			entries = append(entries, bom.Graph(bomEnv))
		}
		bom.RepointGraphDatabase(entries)
	}

	// Before the plan is computed, not only inside Seed.
	//
	// Seed injects the per-environment endpoints on the way past, but the
	// reconcile snapshot hashes the desired state built here. Injecting only in
	// Seed means the digest covers a pre-injection entry that never matches
	// what was seeded, and ext-secrets reads as drifted on every single apply.
	// change_set_builder does the same thing in both places for the same
	// reason, and says so: "Omitting it left ext-secrets permanently drifted."
	// Defaults first, then the account. A manifest that declares ext-secrets
	// itself shadows bom.ExtSecrets' entry, and InjectExtSecretsAccount only
	// patches keys that already exist -- so without this an environment whose
	// manifest carries no instance_configuration got the chart's cloud
	// defaults: IRSA against a role in the control plane's account, every
	// ClusterSecretStore stuck at InvalidProviderConfig.
	bom.ApplyExtSecretsDefaults(entries, bomEnv.AccessKeyID)
	bom.InjectExtSecretsAccount(entries, bomEnv.AccessKeyID)
	return entries
}

// deploysExtSecrets is whether nsctl adds the External Secrets operator: an
// environment with a cluster, unless opted out.
func deploysExtSecrets(opts *Options, mode manifest.Substrate) bool {
	return planFor(mode).Cluster && extSecretsEnabled(opts) && k3sEnabled(opts)
}

// InjectedNames are the instances nsctl deploys into an environment beyond its
// substrate list and its manifest -- the External Secrets operator and its
// CRDs -- so a listing can call them what they are rather than leftovers of a
// deleted declaration. The on-demand graph is not here: whether it deploys
// depends on what the manifest's instances require.
func InjectedNames(lookup func(string) string, mode manifest.Substrate) []string {
	if !deploysExtSecrets(&Options{Lookup: lookup}, mode) {
		return nil
	}
	return []string{bom.ExtSecretsCRDsInstance, bom.ExtSecretsInstance}
}
