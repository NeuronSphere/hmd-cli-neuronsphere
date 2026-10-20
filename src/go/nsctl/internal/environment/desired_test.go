package environment

import (
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bom"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
)

func entryNamed(entries []bom.Entry, name string) *bom.Entry {
	for i := range entries {
		if entries[i].RepoInstanceName == name {
			return &entries[i]
		}
	}
	return nil
}

// The regression this exists for. Apply injected the External Secrets operator
// and Plan did not, so a plan's binder never saw ext-secrets: an instance
// requiring the role (hmd-app-neuronsphere) planned as
//
//	error [missing_required_role] app-neuronsphere: Required dependency role 'ext-secrets' ... is not supplied.
//
// while the apply it was predicting bound it and succeeded. Both now compose
// the desired state through desiredEntries.
func TestDesiredEntriesCarryTheExtSecretsOperator(t *testing.T) {
	t.Parallel()

	env := bom.Environment{DeploymentID: "relpub", AccessKeyID: "000000000014"}
	repos := []manifest.Repo{{InstanceName: "app-neuronsphere", RepoClassName: "hmd-app-neuronsphere"}}
	entries := desiredEntries(testOptions(t.TempDir(), nil), env, manifest.SubstrateFull, repos)

	if entryNamed(entries, "app-neuronsphere") == nil {
		t.Fatal("the declared instance is missing")
	}
	if entryNamed(entries, bom.ExtSecretsCRDsInstance) == nil {
		t.Errorf("%s is missing from the desired state", bom.ExtSecretsCRDsInstance)
	}
	operator := entryNamed(entries, bom.ExtSecretsInstance)
	if operator == nil {
		t.Fatalf("%s is missing from the desired state", bom.ExtSecretsInstance)
	}
	// The defaults and the account are applied too: without them the plan's
	// digest of ext-secrets differs from the apply's and it reads as drifted.
	store, _ := operator.InstanceConfiguration["parameterStoreSecretStore"].(map[string]any)
	if store["localAccessKeyId"] != "000000000014" {
		t.Errorf("parameterStoreSecretStore.localAccessKeyId = %v, want the environment's account", store["localAccessKeyId"])
	}
}

func TestDesiredEntriesOmitExtSecretsWhereApplyDoes(t *testing.T) {
	t.Parallel()

	env := bom.Environment{DeploymentID: "relpub"}
	cases := map[string]struct {
		mode   manifest.Substrate
		lookup map[string]string
	}{
		"opted out":    {manifest.SubstrateFull, map[string]string{bom.ExtSecretsEnabledEnv: "false"}},
		"no k3s":       {manifest.SubstrateFull, map[string]string{"HMD_LOCAL_NEURONSPHERE_ENABLE_K3S": "false"}},
		"no cluster":   {manifest.SubstrateCore, nil},
		"no substrate": {manifest.SubstrateNone, nil},
	}
	for name, c := range cases {
		entries := desiredEntries(testOptions(t.TempDir(), c.lookup), env, c.mode, nil)
		if entryNamed(entries, bom.ExtSecretsInstance) != nil || entryNamed(entries, bom.ExtSecretsCRDsInstance) != nil {
			t.Errorf("%s: ext-secrets is in the desired state", name)
		}
	}
}
