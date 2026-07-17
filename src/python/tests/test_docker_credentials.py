"""Unit tests for docker_credentials -- host Docker credential harvesting.

Uses real temp files for the Docker config (matching test_local_workflow_runner's
KubeconfigForContainerTests style) and mocks subprocess for credential-helper
resolution -- no real Docker installation or credential store required.

Run directly (``python -m pytest src/python/tests/test_docker_credentials.py``).
"""

import json
import os
import tempfile
import unittest
from unittest import mock

from hmd_cli_neuronsphere import docker_credentials as dc


class LocalDockerConfigJsonTests(unittest.TestCase):
    def setUp(self):
        self._tmpdir = tempfile.TemporaryDirectory()
        self._env = mock.patch.dict(
            os.environ, {"DOCKER_CONFIG": self._tmpdir.name}, clear=False
        )
        self._env.start()

    def tearDown(self):
        self._env.stop()
        self._tmpdir.cleanup()

    def _write_config(self, config: dict):
        with open(os.path.join(self._tmpdir.name, "config.json"), "w") as f:
            json.dump(config, f)

    def test_missing_config_file_returns_none(self):
        self.assertIsNone(dc.local_docker_config_json())

    def test_unparseable_config_returns_none(self):
        with open(os.path.join(self._tmpdir.name, "config.json"), "w") as f:
            f.write("{not json")
        self.assertIsNone(dc.local_docker_config_json())

    def test_literal_auth_entry_is_used_directly(self):
        self._write_config({"auths": {"ghcr.io": {"auth": "dXNlcjpwYXNz"}}})
        result = json.loads(dc.local_docker_config_json())
        self.assertEqual(result["auths"]["ghcr.io"]["auth"], "dXNlcjpwYXNz")

    def test_no_auths_or_cred_helpers_returns_none(self):
        self._write_config({})
        self.assertIsNone(dc.local_docker_config_json())

    @mock.patch("hmd_cli_neuronsphere.docker_credentials._resolve_via_helper")
    def test_credstore_entry_resolved_via_helper(self, mock_resolve):
        mock_resolve.return_value = {"username": "me", "secret": "tok"}
        self._write_config({"auths": {"ghcr.io": {}}, "credsStore": "desktop"})
        result = json.loads(dc.local_docker_config_json())
        mock_resolve.assert_called_once_with("desktop", "ghcr.io")
        import base64

        expected_auth = base64.b64encode(b"me:tok").decode()
        self.assertEqual(result["auths"]["ghcr.io"]["auth"], expected_auth)

    @mock.patch("hmd_cli_neuronsphere.docker_credentials._resolve_via_helper")
    def test_per_host_cred_helper_overrides_default(self, mock_resolve):
        mock_resolve.return_value = {"username": "me", "secret": "tok"}
        self._write_config(
            {
                "auths": {"ghcr.io": {}},
                "credsStore": "desktop",
                "credHelpers": {"ghcr.io": "special-helper"},
            }
        )
        dc.local_docker_config_json()
        mock_resolve.assert_called_once_with("special-helper", "ghcr.io")

    @mock.patch("hmd_cli_neuronsphere.docker_credentials._resolve_via_helper")
    def test_failed_helper_lookup_skips_that_host_only(self, mock_resolve):
        def side_effect(helper, host):
            return None if host == "ghcr.io" else {"username": "u", "secret": "s"}

        mock_resolve.side_effect = side_effect
        self._write_config(
            {
                "auths": {"ghcr.io": {}, "docker.io": {}},
                "credsStore": "desktop",
            }
        )
        result = json.loads(dc.local_docker_config_json())
        self.assertNotIn("ghcr.io", result["auths"])
        self.assertIn("docker.io", result["auths"])

    def test_no_helper_available_skips_host(self):
        self._write_config({"auths": {"ghcr.io": {}}})
        self.assertIsNone(dc.local_docker_config_json())


class ResolveViaHelperTests(unittest.TestCase):
    @mock.patch("hmd_cli_neuronsphere.docker_credentials.subprocess.run")
    def test_success_parses_username_and_secret(self, mock_run):
        mock_run.return_value = mock.Mock(
            returncode=0,
            stdout=json.dumps(
                {"ServerURL": "ghcr.io", "Username": "me", "Secret": "tok"}
            ),
        )
        result = dc._resolve_via_helper("desktop", "ghcr.io")
        self.assertEqual(result, {"username": "me", "secret": "tok"})

    @mock.patch("hmd_cli_neuronsphere.docker_credentials.subprocess.run")
    def test_nonzero_exit_returns_none(self, mock_run):
        mock_run.return_value = mock.Mock(returncode=1, stdout="")
        self.assertIsNone(dc._resolve_via_helper("desktop", "ghcr.io"))

    @mock.patch("hmd_cli_neuronsphere.docker_credentials.subprocess.run")
    def test_missing_binary_returns_none(self, mock_run):
        mock_run.side_effect = FileNotFoundError()
        self.assertIsNone(dc._resolve_via_helper("desktop", "ghcr.io"))

    @mock.patch("hmd_cli_neuronsphere.docker_credentials.subprocess.run")
    def test_unparseable_output_returns_none(self, mock_run):
        mock_run.return_value = mock.Mock(returncode=0, stdout="not json")
        self.assertIsNone(dc._resolve_via_helper("desktop", "ghcr.io"))


if __name__ == "__main__":
    unittest.main()
