"""The admin DB secret is a contract with ms-dbaccount, not a description.

Two fields in it are load-bearing in ways that are not obvious, and getting
either wrong fails *silently* -- the request returns 200 and creates nothing,
surfacing minutes later as an ExternalSecret stuck on a secret nobody wrote:

    error processing spec.data[0] (key: environment-db_hmd-postgres-rds_local_
    local_reg1_hmdtr1_hive_metastore), err: Secret does not exist

``engine``
    ``hmd-ms-dbaccount.do_create_db_account`` gates on the exact string
    ``aurora-postgresql`` and returns "no action" for anything else. The local
    overlay creates a plain ``aws_db_instance``, so reporting ``postgres``
    truthfully made every database and user request a successful no-op.

``host``/``port``
    Floci publishes an instance's endpoint as its own IP on a port in its
    7001-7099 RDS proxy range, and that proxy is not restored across a Floci
    restart -- connections are reset while the backend container keeps serving.
    The CLI aliases each backend container on the Docker network for exactly
    this reason; the secret has to name the alias, because what the secret says
    is ms-dbaccount's only route to the database.
"""

import types
import unittest

from hmd_cli_neuronsphere import bom_seeder as bs
from hmd_cli_neuronsphere import bootstrap_dag as bd


def _env(slug="local", db_container="hmd_db-local"):
    return types.SimpleNamespace(
        slug=slug,
        account_id="000000000001",
        db_container=db_container,
        legacy_layout=False,
    )


def _env_db_config(env):
    bom = [dict(e) for e in bs.LOCAL_CORE_BOM]
    bs._inject_env_db_endpoint(bom, env)
    entry = next(e for e in bom if e["repo_instance_name"] == bs.ENV_DB_INSTANCE)
    return entry["instance_configuration"]


class EnvironmentDatabaseBypassesTheProxy(unittest.TestCase):
    def test_host_is_the_environments_own_alias(self):
        self.assertEqual(_env_db_config(_env())["db_host"], "hmd_db-local")

    def test_port_is_postgres_not_flocis_proxy_range(self):
        self.assertEqual(_env_db_config(_env())["db_port"], 5432)

    def test_each_environment_gets_its_own_alias(self):
        self.assertEqual(
            _env_db_config(_env("dev2", "hmd_db-dev2"))["db_host"], "hmd_db-dev2"
        )

    def test_the_shipped_bom_is_not_mutated(self):
        """LOCAL_CORE_BOM entries are module-level and shared; writing through
        them would give every environment whichever alias was seeded last."""
        _env_db_config(_env("dev2", "hmd_db-dev2"))
        shipped = next(
            e
            for e in bs.LOCAL_CORE_BOM
            if e["repo_instance_name"] == bs.ENV_DB_INSTANCE
        )
        self.assertNotIn("db_host", shipped["instance_configuration"])

    def test_no_environment_means_no_rewrite(self):
        bom = [dict(e) for e in bs.LOCAL_CORE_BOM]
        bs._inject_env_db_endpoint(bom, None)
        entry = next(e for e in bom if e["repo_instance_name"] == bs.ENV_DB_INSTANCE)
        self.assertNotIn("db_host", entry["instance_configuration"])


class ControlPlaneDatabaseBypassesTheProxy(unittest.TestCase):
    def test_host_is_the_canonical_alias(self):
        self.assertEqual(bd.postgres_instance_config()["db_host"], "hmd_db")

    def test_port_is_postgres(self):
        self.assertEqual(bd.postgres_instance_config()["db_port"], 5432)


if __name__ == "__main__":
    unittest.main()


class CoreDnsLearnsTheDatabaseLate(unittest.TestCase):
    """`hmd_db` must resolve inside k3s, and it only can after Phase A.

    `provision_k3s_operators` writes the CoreDNS records long before Phase A
    creates the RDS instance, and any canonical name whose container does not
    resolve on the Docker network at that moment is skipped. So `hmd_db` was
    absent from the live `coredns-custom` ConfigMap entirely -- confirmed on a
    running cluster, which listed only neuronsphere, neuronsphere-workload,
    global-graph, neuronsphere-control and hmd_proxy -- and every chart
    addressing the database by that name failed to resolve it.
    """

    def _alias(self, alias_ok):
        from unittest import mock

        from hmd_cli_neuronsphere import environments as ev

        env = _env()
        with mock.patch(
            "hmd_cli_neuronsphere.bom_seeder.env_db_identifier",
            return_value="environment-db-x",
        ):
            with mock.patch(
                "hmd_cli_neuronsphere.floci_deployer.ensure_rds_network_alias",
                return_value=alias_ok,
            ):
                with mock.patch(
                    "hmd_cli_neuronsphere.k3s_operators.refresh_coredns_records"
                ) as refresh:
                    ev._alias_environment_database(env)
        return env, refresh

    def test_aliasing_the_database_refreshes_the_records(self):
        env, refresh = self._alias(True)
        refresh.assert_called_once_with(env)

    def test_a_failed_alias_does_not_publish_a_stale_record(self):
        _env_, refresh = self._alias(False)
        refresh.assert_not_called()
