"""Unit tests for k3s_operators -- pure config-shaping helpers only.

Run directly (``python -m pytest src/python/tests/test_k3s_operators.py``) — no
service, Docker, or k3s cluster required.
"""

import subprocess
import unittest
from unittest import mock

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


class ClusterIncarnationIdTests(unittest.TestCase):
    def _run(self, returncode=0, stdout=""):
        return mock.patch.object(
            k.subprocess,
            "run",
            return_value=subprocess.CompletedProcess(
                args=[], returncode=returncode, stdout=stdout
            ),
        )

    def test_returns_uid_on_success(self):
        with self._run(stdout="abc-123\n"):
            self.assertEqual(k.cluster_incarnation_id(), "abc-123")

    def test_none_on_nonzero_returncode(self):
        with self._run(returncode=1, stdout=""):
            self.assertIsNone(k.cluster_incarnation_id())

    def test_none_on_empty_stdout(self):
        with self._run(stdout=""):
            self.assertIsNone(k.cluster_incarnation_id())

    def test_none_on_subprocess_error(self):
        with mock.patch.object(
            k.subprocess, "run", side_effect=OSError("kubectl not found")
        ):
            self.assertIsNone(k.cluster_incarnation_id())


if __name__ == "__main__":
    unittest.main()
