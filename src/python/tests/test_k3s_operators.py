"""Unit tests for k3s_operators -- pure config-shaping helpers only.

Run directly (``python -m pytest src/python/tests/test_k3s_operators.py``) — no
service, Docker, or k3s cluster required.
"""

import unittest

from hmd_cli_neuronsphere import k3s_operators as k


class ExtSecretsPassesTests(unittest.TestCase):
    def test_two_passes_returned(self):
        passes = k._ext_secrets_passes()
        self.assertEqual(len(passes), 2)

    def test_pass_one_has_no_store_or_docker_secret(self):
        base, _store = k._ext_secrets_passes()
        self.assertFalse(base["clusterSecretStore"]["enabled"])
        self.assertFalse(base["dockerRepoSecret"]["enabled"])

    def test_pass_two_enables_cluster_secret_store(self):
        _base, store = k._ext_secrets_passes()
        self.assertTrue(store["clusterSecretStore"]["enabled"])
        self.assertEqual(store["clusterSecretStore"]["name"], "aws-secrets-manager")

    def test_pass_two_enables_docker_repo_secret_against_parameter_store(self):
        _base, store = k._ext_secrets_passes()
        self.assertTrue(store["dockerRepoSecret"]["enabled"])
        self.assertEqual(
            store["dockerRepoSecret"]["secretStoreName"], "aws-parameter-store"
        )
        self.assertTrue(store["parameterStoreSecretStore"]["enabled"])
        self.assertTrue(store["parameterStoreSecretStore"]["local"])


if __name__ == "__main__":
    unittest.main()
