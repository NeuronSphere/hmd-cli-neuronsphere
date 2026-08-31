"""``_collect_post_deploy_notices`` -- the ``hmd_cli_neuronsphere.get_post_deploy_notices``
entry point that lets an installed plugin contribute lines to `up`'s "Ready" summary
without this package knowing anything about what the plugin is or does.

Run directly: ``python -m pytest src/python/tests/test_post_deploy_notices.py``
"""

import unittest
from unittest import mock

from hmd_cli_neuronsphere import hmd_cli_neuronsphere as hcn


class _FakeEntryPoint:
    def __init__(self, name, fn):
        self.name = name
        self._fn = fn

    def load(self):
        return self._fn


class CollectPostDeployNoticesTests(unittest.TestCase):
    def _patch_entry_points(self, entry_points_list):
        return mock.patch.object(hcn, "entry_points", return_value=entry_points_list)

    def test_collects_notices_from_a_well_behaved_contributor(self):
        ep = _FakeEntryPoint(
            "visualization", lambda env: [f"Superset admin: admin / {env}"]
        )
        with self._patch_entry_points([ep]):
            notices = hcn._collect_post_deploy_notices("some-env")
        self.assertEqual(notices, ["Superset admin: admin / some-env"])

    def test_a_broken_contributor_is_skipped_not_raised(self):
        broken = _FakeEntryPoint("broken", lambda env: 1 / 0)
        good = _FakeEntryPoint("visualization", lambda env: ["ok"])
        with self._patch_entry_points([broken, good]):
            notices = hcn._collect_post_deploy_notices("some-env")
        self.assertEqual(notices, ["ok"])

    def test_a_contributor_returning_none_contributes_nothing(self):
        ep = _FakeEntryPoint("quiet", lambda env: None)
        with self._patch_entry_points([ep]):
            notices = hcn._collect_post_deploy_notices("some-env")
        self.assertEqual(notices, [])

    def test_no_contributors_installed_returns_an_empty_list(self):
        with self._patch_entry_points([]):
            notices = hcn._collect_post_deploy_notices("some-env")
        self.assertEqual(notices, [])


if __name__ == "__main__":
    unittest.main()
