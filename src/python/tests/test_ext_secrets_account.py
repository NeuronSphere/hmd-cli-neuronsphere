"""The External Secrets operator must sign lookups as its own environment.

One Floci serves every account and resolves which one a call belongs to from the
SigV4 access key id. The operator runs inside an environment's k3s cluster and
reads that environment's secrets, so it has to authenticate as that account.

The subtlety this file exists for: the access key appears in *two* places in the
chart's values, and only one of them is the operative one.

* `extraEnv` puts `AWS_ACCESS_KEY_ID` in the operator pod's environment.
* `clusterSecretStore.localAccessKeyId` is rendered into a Kubernetes Secret
  that the `ClusterSecretStore` references via `secretRef` auth.

An AWS store configured with `secretRef` reads its credentials from that Secret
and never consults the pod environment. Setting only `extraEnv` therefore looked
correct and changed nothing: the store kept signing as the chart's default
`test`, Floci resolved that to the default account, and every environment-scoped
lookup failed with

    error processing spec.data[0] (key: broker_hmd-inf-transform-broker_local_
    local_reg1_hmdtr1), err: Secret does not exist

for a secret that existed all along in the environment's own account.
"""

import types
import unittest

from hmd_cli_neuronsphere import bom_seeder as bs


def _env(slug, account_id):
    return types.SimpleNamespace(slug=slug, account_id=account_id, legacy_layout=False)


def _seed(env):
    bom = [dict(e) for e in bs.EXT_SECRETS_BOM]
    bs._inject_floci_account(bom, env)
    entry = next(e for e in bom if e["repo_class_name"] == "hmd-inf-ext-secrets")
    return entry["instance_configuration"]


class StoreCredentialsCarryTheAccount(unittest.TestCase):
    def test_secrets_manager_store_signs_as_the_environment(self):
        config = _seed(_env("local", "000000000001"))
        self.assertEqual(
            config["clusterSecretStore"]["localAccessKeyId"], "000000000001"
        )

    def test_parameter_store_signs_as_the_environment(self):
        config = _seed(_env("local", "000000000001"))
        self.assertEqual(
            config["parameterStoreSecretStore"]["localAccessKeyId"], "000000000001"
        )

    def test_pod_environment_is_kept_in_step(self):
        """Not what the store authenticates with, but anything else in the pod
        reaching AWS directly should resolve in the same account."""
        config = _seed(_env("local", "000000000001"))
        akid = [
            v["value"] for v in config["extraEnv"] if v["name"] == "AWS_ACCESS_KEY_ID"
        ]
        self.assertEqual(akid, ["000000000001"])


class EnvironmentsDoNotContaminateEachOther(unittest.TestCase):
    """`EXT_SECRETS_BOM` holds one shared `instance_configuration` object, built
    once at import. A shallow copy of the entry still points at it, so writing
    through it gave every environment whichever account was seeded last -- and
    permanently rewrote the module-level default for the rest of the process.
    Silent, because the secret *names* are identical across environments."""

    def test_seeding_a_second_environment_leaves_the_first_alone(self):
        first = _seed(_env("local", "000000000001"))
        second = _seed(_env("dev2", "000000000002"))
        self.assertEqual(
            first["clusterSecretStore"]["localAccessKeyId"], "000000000001"
        )
        self.assertEqual(
            second["clusterSecretStore"]["localAccessKeyId"], "000000000002"
        )

    def test_the_shipped_default_is_never_mutated(self):
        """The shipped default is the *control-plane* account, not a placeholder:
        both resolve to the same account in Floci, but "test" reads as "no
        account chosen here", which is how an environment silently ended up
        authenticating as the control plane in the first place."""
        _seed(_env("local", "000000000001"))
        self.assertEqual(
            bs._EXT_SECRETS_LOCAL_CONFIG["clusterSecretStore"]["localAccessKeyId"],
            bs._CONTROL_PLANE_ACCOUNT,
        )


if __name__ == "__main__":
    unittest.main()
