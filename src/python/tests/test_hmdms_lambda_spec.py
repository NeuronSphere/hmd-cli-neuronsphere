"""Unit tests for the HMDMS-service Lambda spec built for compose-path plugins.

Two things this spec gets wrong silently if left unguarded:

* the ``dependency:db-credentials`` placeholder is resolved here into a literal
  Secrets Manager name, and its customer code has to match whatever the seeding
  side wrote. ``floci_deployer.local_customer_code()`` is the one source for
  that (``hmd.env``, defaulting to ``none``); a second, independent default is
  the bug commit b160612 fixed on the seeding side.
* ``AWS_ENDPOINT_URL`` is the *control-plane* default. An environment-scoped
  Lambda gets it rewritten by ``_apply_env_overrides``; it must not be a
  duplicated string literal that can drift from ``FLOCI_INTERNAL_ENDPOINT``.

Run directly (``python -m pytest src/python/tests/test_hmdms_lambda_spec.py``)
or via unittest — no service or Docker required.
"""

import json
import os
import tempfile
import unittest
from unittest import mock

from hmd_cli_neuronsphere.floci_deployer import FLOCI_INTERNAL_ENDPOINT
from hmd_cli_neuronsphere.loaders.local_plugin_loader import LocalPluginLoader


class HmdmsLambdaSpecTests(unittest.TestCase):
    def _spec(self, environ):
        """Build the Lambda spec for a throwaway plugin under a given env."""
        with tempfile.TemporaryDirectory() as root:
            repo = os.path.join(root, "hmd-ms-widget")
            os.makedirs(os.path.join(repo, "src", "local"))
            os.makedirs(os.path.join(repo, "meta-data"))

            with open(os.path.join(repo, "src", "local", "nsplugin.json"), "w") as f:
                json.dump(
                    {
                        "plugin_name": "widget",
                        "hmdms_service": {
                            "lambda_name": "hmd_ms_widget",
                            "repo_class_name": "hmd-ms-widget",
                            "buckets": [],
                        },
                    },
                    f,
                )
            with open(os.path.join(repo, "meta-data", "manifest.json"), "w") as f:
                json.dump(
                    {
                        "name": "hmd-ms-widget",
                        "deploy": {
                            "default_configuration": {
                                "service_config": {
                                    "hmd_db_engines": {
                                        "postgres": {
                                            "engine_type": "postgres",
                                            "engine_config": {
                                                "db_secret_name": (
                                                    "dependency:db-credentials"
                                                ),
                                                "db_name": "widget",
                                            },
                                        },
                                        "dynamo": {
                                            "engine_type": "dynamo",
                                            "engine_config": {},
                                        },
                                    }
                                }
                            }
                        },
                    },
                    f,
                )
            with open(os.path.join(repo, "meta-data", "VERSION"), "w") as f:
                f.write("0.2\n")

            env = dict(environ)
            env["HMD_LOCAL_PLUGINS"] = repo
            env["HMD_LOCAL_NEURONSPHERE_ENABLE_WIDGET"] = "true"
            with mock.patch.dict(os.environ, env, clear=True):
                return LocalPluginLoader().get_hmdms_lambda_spec("widget")

    def _db_secret_name(self, spec):
        config = json.loads(spec["env_vars"]["SERVICE_CONFIG"])
        engine = config["hmd_db_engines"]["postgres"]
        return engine["engine_config"]["db_secret_name"]

    def test_db_secret_customer_code_defaults_to_none(self):
        # Not "hmd": the seeding side writes `none` when HMD_CUSTOMER_CODE is
        # unset, so an independent default here means the lookup always misses.
        spec = self._spec({})
        self.assertEqual(
            self._db_secret_name(spec),
            "hmd_db_hmd-postgres-base_aaa_local_reg1_none_widget",
        )

    def test_db_secret_customer_code_follows_the_environment(self):
        spec = self._spec({"HMD_CUSTOMER_CODE": "hmdtr1"})
        self.assertEqual(
            self._db_secret_name(spec),
            "hmd_db_hmd-postgres-base_aaa_local_reg1_hmdtr1_widget",
        )

    def test_dynamo_table_uses_the_same_customer_code(self):
        spec = self._spec({"HMD_CUSTOMER_CODE": "hmdtr1"})
        config = json.loads(spec["env_vars"]["SERVICE_CONFIG"])
        self.assertEqual(
            config["hmd_db_engines"]["dynamo"]["engine_config"]["dynamo_table"],
            "hmd_ms_widget_hmd-ms-widget_aaa_local_reg1_hmdtr1",
        )
        # ...and the Lambda is told the same code it was named with.
        self.assertEqual(spec["env_vars"]["HMD_CUSTOMER_CODE"], "hmdtr1")

    def test_endpoint_is_the_shared_control_plane_constant(self):
        spec = self._spec({})
        self.assertEqual(spec["env_vars"]["AWS_ENDPOINT_URL"], FLOCI_INTERNAL_ENDPOINT)


if __name__ == "__main__":
    unittest.main()
