"""Unit tests for k3s_operators -- pure config-shaping helpers only.

Run directly (``python -m pytest src/python/tests/test_k3s_operators.py``) — no
service, Docker, or k3s cluster required.
"""

import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from hmd_cli_neuronsphere import bom_seeder as b
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


class ResolveRepoRootTests(unittest.TestCase):
    """Which chart an operator installs: the bundled artifact's, by default.

    A checked-out copy used to win outright, so the chart installed could be an
    in-progress one while the registered version named the published build.
    """

    OP = {"repo": "hmd-inf-ext-secrets", "name": "ext-secrets"}

    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.root = Path(self._tmp.name)
        self.repo_home = self.root / "repos"
        self.artifacts = self.root / "external"

        self._roots = mock.patch.object(
            b, "_artifact_roots", return_value=[str(self.artifacts)]
        )
        self._roots.start()
        b._reset_artifact_version_index()
        self._env = mock.patch.dict(
            os.environ, {"HMD_REPO_HOME": str(self.repo_home)}, clear=True
        )
        self._env.start()

    def tearDown(self):
        self._env.stop()
        self._roots.stop()
        b._reset_artifact_version_index()
        self._tmp.cleanup()

    def _make_chart(self, base, version=None, manifest_name=None):
        helm = Path(base) / "src" / "helm"
        helm.mkdir(parents=True, exist_ok=True)
        (helm / "Chart.yaml").write_text("name: chart\n")
        if version is not None:
            meta = Path(base) / "meta-data"
            meta.mkdir(parents=True, exist_ok=True)
            (meta / "VERSION").write_text(f"{version}\n")
            if manifest_name is not None:
                (meta / "manifest.json").write_text(json.dumps({"name": manifest_name}))
        return Path(base)

    def test_the_bundled_chart_wins_over_a_checkout(self):
        self._make_chart(self.repo_home / "hmd-inf-ext-secrets")
        bundled = self._make_chart(
            self.artifacts / "ext-secrets",
            version="0.2.50",
            manifest_name="hmd-inf-ext-secrets",
        )
        self.assertEqual(k._resolve_repo_root(self.OP), bundled)

    def test_a_local_override_selects_the_checkout(self):
        checkout = self._make_chart(self.repo_home / "hmd-inf-ext-secrets")
        self._make_chart(
            self.artifacts / "ext-secrets",
            version="0.2.50",
            manifest_name="hmd-inf-ext-secrets",
        )
        with mock.patch.dict(
            os.environ,
            {b._local_version_env_var("hmd-inf-ext-secrets"): "local"},
            clear=False,
        ):
            self.assertEqual(k._resolve_repo_root(self.OP), checkout)

    def test_a_checkout_is_used_when_the_artifact_has_no_chart(self):
        checkout = self._make_chart(self.repo_home / "hmd-inf-ext-secrets")
        meta = self.artifacts / "ext-secrets" / "meta-data"
        meta.mkdir(parents=True)
        (meta / "VERSION").write_text("0.2.50\n")
        (meta / "manifest.json").write_text(json.dumps({"name": "hmd-inf-ext-secrets"}))
        self.assertEqual(k._resolve_repo_root(self.OP), checkout)

    def test_an_artifact_without_a_manifest_is_found_by_its_directory_name(self):
        # `repo_root_candidates` keys on the manifest name, which this artifact
        # does not have -- the module's own name table has to cover it.
        bundled = self._make_chart(self.artifacts / "ext-secrets")
        with mock.patch.object(k, "_external_dir", self.artifacts):
            self.assertEqual(k._resolve_repo_root(self.OP), bundled.resolve())

    def test_none_when_no_chart_exists_anywhere(self):
        with mock.patch.object(k, "_external_dir", self.artifacts):
            self.assertIsNone(k._resolve_repo_root(self.OP))


if __name__ == "__main__":
    unittest.main()
