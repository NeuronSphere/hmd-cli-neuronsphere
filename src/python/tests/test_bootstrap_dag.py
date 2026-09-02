"""The control-plane bootstrap DAG (``bootstrap_dag``).

This DAG runs *before* ``hmd-ms-deployment`` exists -- it is the node that
deploys it. What the tests here pin is the ordering that makes that possible,
and the split between nodes that run as real deploys and nodes that cannot.

Run directly: ``python -m pytest src/python/tests/test_bootstrap_dag.py``
"""

import unittest

from hmd_cli_neuronsphere import bootstrap_dag as bd


def _nodes(**overrides):
    calls = {
        "ensure_databases": lambda n, d: True,
        "deploy_naming": lambda n, d: True,
        "deploy_artifact_lib": lambda n, d: True,
        "deploy_ms_deployment": lambda n, d: True,
    }
    calls.update(overrides)
    return bd.control_plane_nodes(**calls)


class OrderingTests(unittest.TestCase):
    def test_ms_deployment_is_last(self):
        """Everything the deployment service needs is deployed ahead of it."""
        self.assertEqual(_nodes()[-1]["instance_name"], "hmd_ms_deployment")

    def test_postgres_is_first(self):
        # Every later node needs a database -- ms-deployment's own included.
        self.assertEqual(_nodes()[0]["repo_class_name"], "hmd-postgres-rds")

    def test_databases_are_created_after_the_instance_exists(self):
        names = [n["instance_name"] for n in _nodes()]
        self.assertLess(
            names.index(bd.CONTROL_PLANE_DB_INSTANCE), names.index("core-databases")
        )


class NodeShapeTests(unittest.TestCase):
    def test_every_node_carries_what_the_runner_reads(self):
        for n in _nodes():
            with self.subTest(node=n["instance_name"]):
                for key in ("instance_name", "repo_class_name", "rid_nid"):
                    self.assertTrue(n.get(key), f"{key} missing")

    def test_rid_nids_are_unique(self):
        rids = [n["rid_nid"] for n in _nodes()]
        self.assertEqual(len(rids), len(set(rids)))

    def test_postgres_runs_as_a_real_deploy(self):
        """It is ordinary infrastructure -- no reason to special-case it.

        Running it through projectbuilder is what makes the produced
        `database.neuronsphere.io/postgres` Resource real rather than hand-seeded.
        """
        postgres = _nodes()[0]
        self.assertNotIn("handler", postgres)
        self.assertIn("deploy", postgres["script"])

    def test_service_nodes_carry_handlers(self):
        """They provision what the deployment service needs, so they cannot be
        deployed *through* it."""
        for n in _nodes()[1:]:
            with self.subTest(node=n["instance_name"]):
                self.assertTrue(callable(n["handler"]))

    def test_control_plane_deployment_id_is_not_an_environment_name(self):
        # `local` is the default *environment*; sharing it would collide the
        # standard names (and so the secret names) of two different things.
        self.assertNotEqual(bd.CONTROL_PLANE_DEPLOYMENT_ID, "local")
        for n in _nodes():
            self.assertEqual(n["deployment_id"], bd.CONTROL_PLANE_DEPLOYMENT_ID)


class HandlerWiringTests(unittest.TestCase):
    def test_each_handler_is_wired_to_its_own_callable(self):
        seen = []
        nodes = _nodes(
            ensure_databases=lambda n, d: seen.append("db"),
            deploy_naming=lambda n, d: seen.append("naming"),
            deploy_artifact_lib=lambda n, d: seen.append("artifact"),
            deploy_ms_deployment=lambda n, d: seen.append("deployment"),
        )
        for n in nodes:
            if "handler" in n:
                n["handler"](n, False)
        self.assertEqual(seen, ["db", "naming", "artifact", "deployment"])


if __name__ == "__main__":
    unittest.main()


class EnvironmentDatabaseIdentityTests(unittest.TestCase):
    """The identifier the CLI looks the RDS instance up by.

    It is *derived* on both sides -- by the CDKTF overlay from
    ``HmdCdkTfStack.base_name``, and here -- rather than read back from Floci. A
    divergence therefore leaves a database that is running but unreachable by
    name, with no error at the point of the mistake, so these pin the shape.
    """

    class _Env:
        slug = "dev2"
        deployment_id = "dev2"

    def _identifier(self):
        from hmd_cli_neuronsphere import bom_seeder

        return bom_seeder.env_db_identifier(self._Env())

    def test_is_a_valid_rds_identifier(self):
        # RDS identifiers are lowercase alphanumerics and hyphens.
        ident = self._identifier()
        self.assertRegex(ident, r"^[a-z0-9-]+$")

    def test_names_the_instance_and_repo_class(self):
        from hmd_cli_neuronsphere import bom_seeder

        ident = self._identifier()
        self.assertIn(bom_seeder.ENV_DB_INSTANCE, ident)
        self.assertIn("hmd-postgres-rds", ident)

    def test_the_control_plane_and_an_environment_never_collide(self):
        from hmd_cli_neuronsphere import floci_deployer

        cp = bd.control_plane_db_identifier(floci_deployer.control_plane_target())
        self.assertNotEqual(cp, self._identifier())

    def test_two_environments_get_distinct_identifiers(self):
        from hmd_cli_neuronsphere import bom_seeder

        class _Other:
            slug = "dev3"
            deployment_id = "dev3"

        self.assertNotEqual(
            bom_seeder.env_db_identifier(self._Env()),
            bom_seeder.env_db_identifier(_Other()),
        )
