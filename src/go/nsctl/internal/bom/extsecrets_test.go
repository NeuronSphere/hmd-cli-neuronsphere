package bom

import "testing"

func extSecretsEntry(entries []Entry, name string) *Entry {
	for i := range entries {
		if entries[i].RepoInstanceName == name {
			return &entries[i]
		}
	}
	return nil
}

// The CRDs are a prior instance, not a chart flag, and the operator depends on
// them by name. installCRDs is false precisely because the other entry owns
// them; both installing would make the second deploy fight the first.
func TestTheCRDsAreTheirOwnInstanceAndComeFirst(t *testing.T) {
	t.Parallel()

	entries := ExtSecrets(Environment{DeploymentID: "dev", AccessKeyID: "100000000001"})
	if len(entries) != 2 || entries[0].RepoInstanceName != ExtSecretsCRDsInstance {
		t.Fatalf("want the CRDs first, got %+v", entries)
	}
	operator := extSecretsEntry(entries, ExtSecretsInstance)
	if operator.Dependencies["crds"] != ExtSecretsCRDsInstance {
		t.Errorf("the operator does not depend on the CRDs: %v", operator.Dependencies)
	}
	if operator.InstanceConfiguration["installCRDs"] != false {
		t.Errorf("installCRDs = %v; the CRDs instance owns them", operator.InstanceConfiguration["installCRDs"])
	}
	for _, e := range entries {
		if e.Dependencies["eks-cluster"] != EKSClusterInstance || e.Dependencies["compute"] != CoreInstanceName {
			t.Errorf("%s must depend on the cluster and on compute: %v", e.RepoInstanceName, e.Dependencies)
		}
	}
}

// These deploy in the second changeset. substrateInstances decides the phase,
// and the first phase is applied before the concrete core Resources are
// submitted -- where a dependency carrying a tag_selector cannot resolve.
func TestExtSecretsIsNotPartOfTheSubstratePhase(t *testing.T) {
	t.Parallel()

	for _, name := range []string{ExtSecretsCRDsInstance, ExtSecretsInstance} {
		if IsSubstrate(name) {
			t.Errorf("%s is in the substrate phase, which applies before the core Resources exist", name)
		}
	}
}

// The chart's cloud defaults authenticate by IRSA against
// sts.<region>.amazonaws.com, which resolves nowhere locally: the store sits at
// InvalidProviderConfig and every ExternalSecret silently never syncs.
func TestTheLocalOverridesThatMakeTheOperatorWork(t *testing.T) {
	t.Parallel()

	config := ExtSecrets(Environment{AccessKeyID: "100000000001"})[1].InstanceConfiguration

	for _, store := range []string{"clusterSecretStore", "parameterStoreSecretStore"} {
		s, ok := config[store].(map[string]any)
		if !ok {
			t.Fatalf("%s is missing", store)
		}
		if s["local"] != true {
			t.Errorf("%s.local = %v; without it the store authenticates by IRSA", store, s["local"])
		}
	}

	// The local CDKTF overlay writes this secret with
	// hmd_lib_secrets_backend.create_secret(), which always writes to SSM
	// Parameter Store. A Secrets Manager store looks in the wrong place and
	// reports "Secret does not exist".
	docker, _ := config["dockerRepoSecret"].(map[string]any)
	if docker["secretStoreName"] != "aws-parameter-store" {
		t.Errorf("dockerRepoSecret.secretStoreName = %v, want aws-parameter-store", docker["secretStoreName"])
	}

	// hmd-cli-helm's _set_local_standard_values applies aws_region with --set,
	// which unconditionally beats a values file, so a key here is silently
	// clobbered and reads as configuration that does something.
	if _, present := config["aws_region"]; present {
		t.Error("aws_region is set here, where --set overrides it silently")
	}
}

// The trap. One Floci serves every account and reads which one off the 12-digit
// access key, so two environments sharing a key both resolve the same account's
// secrets -- and nothing fails, because the secret names are identical across
// environments. It has to be asserted precisely because it cannot be noticed.
func TestTwoEnvironmentsAuthenticateAsDifferentAccounts(t *testing.T) {
	t.Parallel()

	dev := ExtSecrets(Environment{DeploymentID: "dev", AccessKeyID: "100000000001"})
	InjectExtSecretsAccount(dev, "100000000001")
	prod := ExtSecrets(Environment{DeploymentID: "prod", AccessKeyID: "100000000002"})
	InjectExtSecretsAccount(prod, "100000000002")

	devKey := storeKey(t, dev, "clusterSecretStore")
	prodKey := storeKey(t, prod, "clusterSecretStore")
	if devKey == prodKey {
		t.Fatalf("both environments sign as %s; one is reading the other's secrets", devKey)
	}
	// The operative one is the store's, not the pod's: a ClusterSecretStore
	// with secretRef auth reads its credentials from the Secret the chart
	// renders and never consults the pod's environment. Setting only extraEnv
	// left the store signing as the default account while everything looked
	// configured.
	if devKey != "100000000001" || prodKey != "100000000002" {
		t.Errorf("store keys are %s and %s", devKey, prodKey)
	}
	if got := podKey(t, dev); got != "100000000001" {
		t.Errorf("the pod's AWS_ACCESS_KEY_ID is %s", got)
	}
}

// A shallow copy of an entry still points at the same maps, so a per-environment
// rewrite that mutated in place would give every environment whichever account
// was injected last. The Python carries two separate copy-before-mutate
// defences and a test asserting its module default stays clean.
func TestInjectingAnAccountDoesNotContaminateTheNext(t *testing.T) {
	t.Parallel()

	first := ExtSecrets(Environment{AccessKeyID: "100000000001"})
	InjectExtSecretsAccount(first, "100000000009")

	fresh := ExtSecrets(Environment{AccessKeyID: "100000000002"})
	if got := storeKey(t, fresh, "parameterStoreSecretStore"); got != "100000000002" {
		t.Errorf("a later environment inherited %s from an earlier injection", got)
	}
}

func storeKey(t *testing.T, entries []Entry, store string) string {
	t.Helper()
	config := extSecretsEntry(entries, ExtSecretsInstance).InstanceConfiguration
	s, ok := config[store].(map[string]any)
	if !ok {
		t.Fatalf("%s is missing", store)
	}
	key, _ := s["localAccessKeyId"].(string)
	return key
}

func podKey(t *testing.T, entries []Entry) string {
	t.Helper()
	config := extSecretsEntry(entries, ExtSecretsInstance).InstanceConfiguration
	for _, v := range config["extraEnv"].([]any) {
		if e, ok := v.(map[string]any); ok && e["name"] == "AWS_ACCESS_KEY_ID" {
			value, _ := e["value"].(string)
			return value
		}
	}
	t.Fatal("AWS_ACCESS_KEY_ID is not in extraEnv")
	return ""
}
