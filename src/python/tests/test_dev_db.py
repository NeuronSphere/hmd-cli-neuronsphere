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


class ProvisionAndRegisterTests(unittest.TestCase):
    def test_dbaccount_called_before_submit(self):
        order = []

        def _dbaccount(did, db_name, username, origin="dev"):
            order.append("dbaccount")

        def _post(url, op, payload=None, **k):
            order.append(op)
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
            dev_db.provision_and_register_db("deployment_gui", "deployment_gui")

        self.assertEqual(order[0], "dbaccount")
        self.assertIn("submit_resources", order)
        self.assertLess(order.index("dbaccount"), order.index("submit_resources"))


if __name__ == "__main__":
    unittest.main()
