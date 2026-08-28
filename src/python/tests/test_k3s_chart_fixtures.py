"""Unit tests for the legacy chart-plugin Floci fixtures.

``_CHART_PLUGINS`` seeds the Secrets Manager entries a chart's ExternalSecret
pulls from. Those names embed the customer code, and the seeding side and the
lookup side have to agree on it: when they drifted (the seeder writing ``none``
while a fixture spelled out ``hmd``), every affected chart came up with an
unresolvable secret. So the names must always be derived from
``local_customer_code()`` -- never written out in full.

Run directly (``python -m pytest src/python/tests/test_k3s_chart_fixtures.py``)
or via unittest — no service or Docker required.
"""

import importlib
import os
import unittest
from unittest import mock

from hmd_cli_tools.hmd_cli_tools import make_standard_name

from hmd_cli_neuronsphere import k3s_chart_plugins as kcp


def _reload_with_customer_code(value):
    """Reimport the module under a given HMD_CUSTOMER_CODE (read at import)."""
    env = {k: v for k, v in os.environ.items() if k != "HMD_CUSTOMER_CODE"}
    if value is not None:
        env["HMD_CUSTOMER_CODE"] = value
    with mock.patch.dict(os.environ, env, clear=True):
        return importlib.reload(kcp)


def _fixture_names(module):
    names = []
    for chart in module._CHART_PLUGINS:
        for sec in chart.get("secrets", []):
            names.append(
                sec.get("name")
                or (
                    make_standard_name(
                        sec["instance"],
                        sec["repo"],
                        sec["did"],
                        module._ENV,
                        module._HMD_REGION,
                        module._CUSTOMER,
                    )
                    + sec.get("suffix", "")
                )
            )
    return names


class ChartFixtureNameTests(unittest.TestCase):
    def tearDown(self):
        # Leave the module as the rest of the suite found it.
        importlib.reload(kcp)

    def test_no_fixture_hardcodes_its_full_name(self):
        for chart in kcp._CHART_PLUGINS:
            for sec in chart.get("secrets", []):
                self.assertNotIn(
                    "name",
                    sec,
                    f"{chart['plugin_name']} fixture spells out a secret name; "
                    "derive it from instance/repo/did/suffix instead",
                )

    def test_customer_code_comes_from_the_environment(self):
        module = _reload_with_customer_code("acme")
        for name in _fixture_names(module):
            self.assertIn("_acme", name, name)

    def test_customer_code_defaults_to_none(self):
        module = _reload_with_customer_code(None)
        self.assertEqual(module._CUSTOMER, "none")
        for name in _fixture_names(module):
            self.assertIn("_none", name, name)

    def test_no_fixture_carries_the_stale_hmd_customer_code(self):
        module = _reload_with_customer_code(None)
        for name in _fixture_names(module):
            self.assertNotIn("_reg1_hmd_", name, name)
            self.assertNotIn("_reg1_hmd-", name, name)

    def test_hive_fixtures_keep_their_chart_specific_shape(self):
        """The suffixes the standard-name helper can't express are preserved."""
        module = _reload_with_customer_code("hmdtr1")
        names = _fixture_names(module)
        self.assertIn(
            "hmd-db_hmd-postgres-base_local_local_reg1_hmdtr1_metastore", names
        )
        self.assertIn(
            "hive-bucket_hmd-inf-trino-store-access_local_local_reg1_hmdtr1"
            "-bucketaccess",
            names,
        )


if __name__ == "__main__":
    unittest.main()
