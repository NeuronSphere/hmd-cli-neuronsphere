"""Unit tests for the local dev-loop DB helpers (dev_db).

No running service required -- the ms-deployment REST calls and the dbaccount
provisioning call are mocked.
"""

import unittest
from unittest import mock

from hmd_cli_neuronsphere import dev_db


def _fake_post_apiop(calls):
    def _inner(url, op, payload=None, **k):
        calls.append((op, payload))
        if op == "register_deployed_instance":
            return {"repo_instance_deployment_id": "rid-owner"}
        return {}

    return _inner


def _common_patches(calls, dbaccount=None):
    return [
        mock.patch.object(
            dev_db._rest, "post_apiop", side_effect=_fake_post_apiop(calls)
        ),
        mock.patch.object(dev_db._rest, "post_apiop_idempotent", return_value={}),
        mock.patch.object(dev_db, "_get_repo_version", return_value="0.1.0"),
        mock.patch.object(dev_db, "_local_db_secret_base", return_value="base"),
        mock.patch.object(
            dev_db,
            "_post_create_db_account",
            side_effect=dbaccount or (lambda *a, **k: None),
        ),
    ]


class RegisterDbResourceTests(unittest.TestCase):
    def test_postgres_resource_shape(self):
        calls = []
        patches = _common_patches(calls)
        for p in patches:
            p.start()
        try:
            dev_db.register_db_resource("deployment_gui", "deployment_gui")
        finally:
            for p in patches:
                p.stop()

        submit = [p for op, p in calls if op == "submit_resources"][0]
        res = submit["resources"][0]
        self.assertEqual(
            res["resource_definition"], dev_db.POSTGRES_RESOURCE_DEFINITION
        )
        self.assertEqual(res["output"]["database_name"], "deployment_gui")
        # secret_name follows the {secret_base}_{username} convention.
        self.assertEqual(res["output"]["secret_name"], "base_deployment_gui")
        tags = {t["key"]: t["value"] for t in res["tags"]}
        self.assertEqual(tags["environment"], "local")
        self.assertEqual(tags["database"], "deployment_gui")
        self.assertEqual(tags["role"], "db-credentials")

    def test_owner_rid_from_register_passed_to_submit(self):
        calls = []
        patches = _common_patches(calls)
        for p in patches:
            p.start()
        try:
            dev_db.register_db_resource("db1", "db1")
        finally:
            for p in patches:
                p.stop()
        submit = [p for op, p in calls if op == "submit_resources"][0]
        self.assertEqual(submit["repo_instance_deployment_id"], "rid-owner")

    def test_register_existing_does_not_call_dbaccount(self):
        calls = []
        dba = mock.MagicMock()
        patches = _common_patches(calls, dbaccount=dba)
        for p in patches:
            p.start()
        try:
            dev_db.register_db_resource("db1", "db1", host="myhost")
        finally:
            for p in patches:
                p.stop()
        dba.assert_not_called()


class _Env:
    """Stand-in for env_registry.LocalEnvironment (no HMD_HOME needed)."""

    def __init__(self, slug="dev2", legacy=False):
        self.slug = slug
        self.deployment_id = slug
        self.db_container = f"hmd_db-{slug}"
        self.legacy_layout = legacy


class ProvisionAndRegisterTests(unittest.TestCase):
    def _run(self, calls, env=None, **kwargs):
        """Drive provision_and_register_db against fakes, recording call order."""

        def _dbaccount(did, db_name, username, origin="dev", route_prefix=""):
            calls.append(("dbaccount", {"did": did, "route_prefix": route_prefix}))

        def _post(url, op, payload=None, **k):
            calls.append((op, payload))
            if op == "register_deployed_instance":
                return {"repo_instance_deployment_id": "rid-owner"}
            return {}

        with mock.patch.object(
            dev_db, "_post_create_db_account", side_effect=_dbaccount
        ), mock.patch.object(
            dev_db._rest, "post_apiop", side_effect=_post
        ), mock.patch.object(
            dev_db._rest, "post_apiop_idempotent", return_value={}
        ), mock.patch.object(
            dev_db, "_get_repo_version", return_value="0.1.0"
        ), mock.patch.object(
            dev_db, "_local_db_secret_base", return_value="base"
        ):
            dev_db.provision_and_register_db(
                "deployment_gui", "deployment_gui", env=env, **kwargs
            )

    def test_dbaccount_called_before_submit(self):
        calls = []
        self._run(calls)
        order = [op for op, _ in calls]
        self.assertEqual(order[0], "dbaccount")
        self.assertIn("submit_resources", order)
        self.assertLess(order.index("dbaccount"), order.index("submit_resources"))

    def test_dbaccount_is_routed_to_the_environments_own_service(self):
        """dbaccount is per-environment, reached at ``/<slug>/hmd_ms_dbaccount/``.

        Without the prefix the call lands on whichever dbaccount the unprefixed
        route points at, provisioning the database in the wrong account.
        """
        calls = []
        self._run(calls, env=_Env("dev2"))
        payload = [p for op, p in calls if op == "dbaccount"][0]
        self.assertEqual(payload["route_prefix"], "dev2")
        self.assertEqual(payload["did"], "dev2")

    def test_a_legacy_environment_is_prefixed_too(self):
        """A legacy env shares the control plane's containers, not its routes.

        ``write_env_routes`` prefixes on slug for every environment, so skipping
        the prefix here sent the call to a control-plane path where
        ms-dbaccount is not routed, and it 404'd.
        """
        calls = []
        self._run(calls, env=_Env("local", legacy=True))
        payload = [p for op, p in calls if op == "dbaccount"][0]
        self.assertEqual(payload["route_prefix"], "local")

    def test_no_route_prefix_without_an_environment(self):
        calls = []
        self._run(calls)
        self.assertEqual(
            [p for op, p in calls if op == "dbaccount"][0]["route_prefix"], ""
        )


if __name__ == "__main__":
    unittest.main()
