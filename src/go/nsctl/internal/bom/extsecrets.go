package bom

// The External Secrets operator and its CRDs.
//
// This widens what "substrate" means, deliberately. SPEC001 said an environment
// nsctl creates is "a cluster and a database with nothing on them"; it is now a
// cluster, a database, and the operator that lets a cloud chart's secrets
// resolve. Without the operator the first chart rendering an ExternalSecret
// fails on a missing CRD, so "nothing on them" was never quite true of an
// environment anyone could deploy into -- it was true only because the Python
// CLI had written the entries into the manifest of every environment anyone had
// tried.
//
// Everything below mirrors bom_seeder.EXT_SECRETS_BOM and
// _EXT_SECRETS_LOCAL_CONFIG. The values are not guessable and the reasons are
// not obvious, so the reasons travel with them.
const (
	ExtSecretsCRDsInstance  = "ext-secrets-crds"
	ExtSecretsCRDsRepoClass = "hmd-inf-ext-secrets-crds"

	ExtSecretsInstance  = "ext-secrets"
	ExtSecretsRepoClass = "hmd-inf-ext-secrets"
)

// ExtSecretsEnabledEnv opts out, matching the Python. Default-on.
const ExtSecretsEnabledEnv = "HMD_LOCAL_NEURONSPHERE_ENABLE_EXT_SECRETS"

// ControlPlaneAccountID mirrors floci.ControlPlaneAccountID. Not imported: this
// package is pure data about a BOM and importing the AWS layer for one constant
// would invert the dependency. It is the default only so that an unset account
// reads as the control plane's rather than as a placeholder like "test" -- both
// resolve to the same account in Floci, but a placeholder reads as "no account
// chosen here", which is exactly how an environment ended up silently
// authenticating as the control plane.
const ControlPlaneAccountID = "000000000000"

// FlociInternalEndpoint is where a pod inside the cluster reaches Floci, which
// resolves through the CoreDNS custom record. Mirrors
// bom_seeder._FLOCI_INTERNAL_ENDPOINT's default.
const FlociInternalEndpoint = "http://neuronsphere:4566"

// ExtSecrets is the CRDs and the operator, in that order.
//
// The CRDs are a separate instance and a declared dependency, not a flag: the
// operator's own installCRDs is false precisely because the prior entry owns
// them, and letting both install them makes the second deploy fight the first.
func ExtSecrets(env Environment) []Entry {
	return []Entry{
		{
			RepoInstanceName:      ExtSecretsCRDsInstance,
			RepoClassName:         ExtSecretsCRDsRepoClass,
			DeploymentID:          env.DeploymentID,
			InstanceConfiguration: map[string]any{},
			Dependencies: map[string]any{
				"eks-cluster": EKSClusterInstance,
				"compute":     CoreInstanceName,
			},
		},
		{
			RepoInstanceName:      ExtSecretsInstance,
			RepoClassName:         ExtSecretsRepoClass,
			DeploymentID:          env.DeploymentID,
			InstanceConfiguration: extSecretsConfig(env.AccessKeyID),
			Dependencies: map[string]any{
				"eks-cluster": EKSClusterInstance,
				"compute":     CoreInstanceName,
				"crds":        ExtSecretsCRDsInstance,
			},
		},
	}
}

// extSecretsConfig is the chart values that make the operator work locally.
//
// The chart's own defaults assume cloud: its ClusterSecretStores authenticate
// by IRSA, against sts.<region>.amazonaws.com, which resolves nowhere here. The
// store then sits at InvalidProviderConfig and every ExternalSecret silently
// never syncs -- nothing errors, the secrets simply never appear.
//
// Built fresh each call rather than copied from a package-level literal. The
// Python's equivalent is shared by reference through a shallow dict(), which is
// why _inject_floci_account has two separate copy-before-mutate defences and a
// test asserting the module default stays uncontaminated.
func extSecretsConfig(accessKeyID string) map[string]any {
	if accessKeyID == "" {
		accessKeyID = ControlPlaneAccountID
	}
	return map[string]any{
		// The prior entry owns them.
		"installCRDs": false,
		"clusterSecretStore": map[string]any{
			"enabled": true,
			// Static credentials against Floci instead of IRSA.
			"local": true,
			"name":  "aws-secrets-manager",
			// The account selector. See InjectExtSecretsAccount.
			"localAccessKeyId": accessKeyID,
		},
		"parameterStoreSecretStore": map[string]any{
			"enabled":          true,
			"local":            true,
			"name":             "aws-parameter-store",
			"localAccessKeyId": accessKeyID,
		},
		"dockerRepoSecret": map[string]any{
			"enabled": true,
			// aws-parameter-store, not the Secrets Manager store. The local
			// CDKTF overlay writes this secret's value with
			// hmd_lib_secrets_backend.create_secret(), which always writes to
			// SSM Parameter Store whatever its naming suggests -- so a Secrets
			// Manager store looks in the wrong place and reports "Secret does
			// not exist".
			"secretStoreName": "aws-parameter-store",
		},
		// The pod's own environment. Not what the ClusterSecretStore signs
		// with -- see InjectExtSecretsAccount -- but what everything else the
		// operator does reaches Floci through.
		"extraEnv": []any{
			map[string]any{"name": "AWS_ENDPOINT_URL", "value": FlociInternalEndpoint},
			map[string]any{"name": "AWS_ACCESS_KEY_ID", "value": accessKeyID},
			map[string]any{"name": "AWS_SECRET_ACCESS_KEY", "value": accessKeyID},
		},
		// No aws_region here. hmd-cli-helm's _set_local_standard_values applies
		// it with --set, which unconditionally beats a values file, so a key
		// here would be silently clobbered.
	}
}

// InjectExtSecretsAccount points every ext-secrets entry at an environment's
// emulated AWS account.
//
// One Floci serves every account and resolves which one a request belongs to
// from the 12-digit SigV4 access key id. Left at a shared default, every
// environment's operator authenticates as the same account and resolves the
// control plane's secrets instead of its own -- silently, because the secret
// names are identical across environments and nothing fails.
//
// Two places carry the key and only one is operative. extraEnv puts
// AWS_ACCESS_KEY_ID in the operator pod's environment, but a ClusterSecretStore
// configured with secretRef auth reads its credentials from the Kubernetes
// Secret the chart renders -- the pod's environment is never consulted. Setting
// only extraEnv left the store signing as the default account and every
// environment-scoped lookup failing with "Secret does not exist" for a secret
// that existed all along in the environment's own account.
//
// AWS_SECRET_ACCESS_KEY is deliberately not rewritten: only the key id selects
// the account.
func InjectExtSecretsAccount(entries []Entry, accessKeyID string) {
	if accessKeyID == "" {
		return
	}
	for i := range entries {
		if entries[i].RepoClassName != ExtSecretsRepoClass {
			continue
		}
		// Copied at every level before mutating. A shallow copy of the entry
		// still points at the same maps, so writing through one would give
		// every environment whichever account was seeded last.
		config := copyConfig(entries[i].InstanceConfiguration)
		for _, key := range []string{"clusterSecretStore", "parameterStoreSecretStore"} {
			store, ok := config[key].(map[string]any)
			if !ok {
				continue
			}
			// A non-local store authenticates by IRSA and has no access key to
			// select an account with; leave it alone.
			if local, _ := store["local"].(bool); !local {
				continue
			}
			config[key] = copyConfig(store)
			config[key].(map[string]any)["localAccessKeyId"] = accessKeyID
		}
		if extra, ok := config["extraEnv"].([]any); ok {
			copied := make([]any, 0, len(extra))
			for _, v := range extra {
				entry, ok := v.(map[string]any)
				if !ok {
					copied = append(copied, v)
					continue
				}
				entry = copyConfig(entry)
				if entry["name"] == "AWS_ACCESS_KEY_ID" {
					entry["value"] = accessKeyID
				}
				copied = append(copied, entry)
			}
			config["extraEnv"] = copied
		}
		entries[i].InstanceConfiguration = config
	}
}

// ApplyExtSecretsDefaults fills in the local-mode chart values on any
// ext-secrets entry that is missing them.
//
// bom.ExtSecrets carries those values, but an environment manifest that
// declares ext-secrets itself shadows that entry: TopoSort dedupes by instance
// name and keeps the first, and Declared's entries are appended before
// ExtSecrets'. A manifest the Python CLI wrote carries the values too, so this
// never showed; one written by `nsctl env add` alone carries no
// instance_configuration at all, and the chart then fell through to its cloud
// branch -- IRSA against sts.<region>.amazonaws.com and a role in the control
// plane's account rather than the environment's. Every ClusterSecretStore sat
// at InvalidProviderConfig reporting InvalidIdentityToken, every ExternalSecret
// failed to sync, and every chart waiting on one timed out under helm --atomic.
//
// Defaults go *under* what the manifest declared, never over it: a manifest
// that sets these deliberately still wins. Runs before InjectExtSecretsAccount,
// which then has the keys it only ever patches rather than creates.
func ApplyExtSecretsDefaults(entries []Entry, accessKeyID string) {
	for i := range entries {
		if entries[i].RepoClassName != ExtSecretsRepoClass {
			continue
		}
		defaults := extSecretsConfig(accessKeyID)
		config := copyConfig(entries[i].InstanceConfiguration)
		for key, def := range defaults {
			existing, present := config[key]
			if !present {
				config[key] = def
				continue
			}
			// A declared store keeps its own keys and gains the ones it left
			// out -- `local` above all, which is what selects static-credential
			// auth over IRSA.
			defMap, defOK := def.(map[string]any)
			curMap, curOK := existing.(map[string]any)
			if !defOK || !curOK {
				continue
			}
			merged := copyConfig(curMap)
			for k, v := range defMap {
				if _, ok := merged[k]; !ok {
					merged[k] = v
				}
			}
			config[key] = merged
		}
		entries[i].InstanceConfiguration = config
	}
}
