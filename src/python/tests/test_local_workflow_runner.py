"""Unit tests for LocalWorkflowRunner local-source deploy selection.

A local NeuronSphere has no Artifact Librarian, so the in-container ``hmd deploy``
must take its code from a local source: the mounted workspace (primary, via
``--local``) or a previously-downloaded artifact bundle (secondary, via
``HMD_ARTIFACT_ROOT``). These tests cover the two pure helpers that drive that:

* ``_localize_deploy_script`` — injects ``--local`` into the generated deploy command;
* ``LocalWorkflowRunner._resolve_artifact_bundle_dir`` — selects a bundle dir when one
  holds the repo's ``<repo>_<ver>_build.zip``.

Run directly (``python -m pytest src/python/tests/test_local_workflow_runner.py``) —
no service or Docker required.
"""

import os
import tempfile
import unittest
from unittest import mock

from hmd_cli_neuronsphere.local_workflow_runner import (
    LocalWorkflowRunner,
    _localize_deploy_script,
)

# The shape ms-deployment's deploy_base.deploy_node emits.
GENERATED = (
    "export HMD_REPO_INSTANCE_DEPLOYMENT_ID=rid-123\n"
    "hmd --debug  --repo-name hmd-inf-ext-secrets-crds --repo-version 0.1 "
    "--hmd-region reg1 deploy  --instance-name ext-secrets-crds "
    "--environment local --deployment-id local --config-file STDIN <<EOF\n"
    '{"deploy": "value", "nested": {"deploy": true}}\n'
    "EOF"
)


class LocalizeDeployScriptTests(unittest.TestCase):
    def test_injects_local_once_on_command_line(self):
        out = _localize_deploy_script(GENERATED)
        lines = out.splitlines()
        self.assertIn(" deploy --local ", lines[1])
        self.assertEqual(lines[1].count("--local"), 1)

    def test_leaves_export_and_heredoc_untouched(self):
        lines = _localize_deploy_script(GENERATED).splitlines()
        self.assertEqual(lines[0], "export HMD_REPO_INSTANCE_DEPLOYMENT_ID=rid-123")
        self.assertEqual(lines[2], '{"deploy": "value", "nested": {"deploy": true}}')
        self.assertEqual(lines[3], "EOF")

    def test_idempotent(self):
        once = _localize_deploy_script(GENERATED)
        self.assertEqual(_localize_deploy_script(once), once)

    def test_already_local_unchanged(self):
        s = "hmd --repo-name x --repo-version 1 deploy --local --instance-name y"
        self.assertEqual(_localize_deploy_script(s), s)

    def test_destroy_variant(self):
        s = "hmd --repo-name x --repo-version 1 deploy --destroy --instance-name y"
        self.assertEqual(
            _localize_deploy_script(s),
            "hmd --repo-name x --repo-version 1 deploy --local --destroy --instance-name y",
        )

    def test_non_deploy_script_untouched(self):
        self.assertEqual(
            _localize_deploy_script("bash src/local/deploy_local.sh"),
            "bash src/local/deploy_local.sh",
        )

    def test_empty(self):
        self.assertEqual(_localize_deploy_script(""), "")


class ResolveArtifactBundleDirTests(unittest.TestCase):
    def _node(self):
        return {
            "repo_class_name": "hmd-inf-ext-secrets-crds",
            "repo_class_version": "0.1",
        }

    def test_none_when_env_unset(self):
        with mock.patch.dict(os.environ, {}, clear=True):
            self.assertIsNone(
                LocalWorkflowRunner._resolve_artifact_bundle_dir(self._node())
            )

    def test_selects_dir_when_bundle_present(self):
        with tempfile.TemporaryDirectory() as d:
            open(os.path.join(d, "hmd-inf-ext-secrets-crds_0.1_build.zip"), "w").close()
            with mock.patch.dict(
                os.environ, {"HMD_LOCAL_DEPLOY_ARTIFACT_ROOT": d}, clear=True
            ):
                self.assertEqual(
                    LocalWorkflowRunner._resolve_artifact_bundle_dir(self._node()), d
                )

    def test_none_when_bundle_absent(self):
        with tempfile.TemporaryDirectory() as d:
            with mock.patch.dict(
                os.environ, {"HMD_LOCAL_DEPLOY_ARTIFACT_ROOT": d}, clear=True
            ):
                self.assertIsNone(
                    LocalWorkflowRunner._resolve_artifact_bundle_dir(self._node())
                )


if __name__ == "__main__":
    unittest.main()
