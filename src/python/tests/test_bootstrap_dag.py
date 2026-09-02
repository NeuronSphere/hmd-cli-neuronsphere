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


class DeployScriptGrammarTests(unittest.TestCase):
    """The generated command must match `hmd deploy`'s real argument surface.

    This exists because an earlier version invented a positional tool name
    (`hmd deploy ... cdktf`), which `hmd deploy` rejects -- it takes flags and
    reads `manifest.json`'s `deploy.commands` to know which tool to run. The
    failure only appeared at `up`, inside a projectbuilder container, as
    "invalid choice: 'cdktf'".
    """

    # Verified against `hmd --help` / `hmd deploy --help` in
    # ghcr.io/hmdlabs/hmd-img-projectbuilder.
    GLOBAL_FLAGS = {"--repo-name", "--repo-version", "--hmd-region", "--debug"}
    DEPLOY_FLAGS = {
        "--instance-name",
        "--environment",
        "--deployment-id",
        "--config-file",
        "--local",
        "--register",
        "--destroy",
        "--account",
        "--artifact-root",
        "--repo-instance-deployment-id",
        "--status-file",
    }

    def _command_line(self):
        from hmd_cli_neuronsphere.local_workflow_runner import _localize_deploy_script

        script = _localize_deploy_script(_nodes()[0]["script"])
        return script.splitlines()[0]

    def test_every_flag_used_is_one_the_cli_accepts(self):
        known = self.GLOBAL_FLAGS | self.DEPLOY_FLAGS
        used = {t for t in self._command_line().split() if t.startswith("--")}
        self.assertTrue(used, "no flags in the generated command")
        self.assertEqual(used - known, set(), "unknown flag(s) in the deploy command")

    def test_there_is_no_positional_tool_name(self):
        """`hmd deploy`'s only positional is `status`; a tool name there is the
        bug this class was added for."""
        line = self._command_line()
        after = line.split(" deploy ", 1)[1]
        positionals = [
            t for t in after.split() if not t.startswith("-") and not t.startswith("<<")
        ]
        # Every remaining bare word must be a flag's value, never a subcommand.
        tokens = after.split()
        for i, tok in enumerate(tokens):
            if tok in positionals:
                self.assertTrue(
                    i > 0 and tokens[i - 1].startswith("--"),
                    f"{tok!r} is a positional, not a flag value",
                )

    def test_localizing_inserts_local_right_after_deploy(self):
        self.assertIn(" deploy --local ", self._command_line())

    def test_the_config_arrives_as_a_quoted_heredoc(self):
        script = _nodes()[0]["script"]
        self.assertIn("--config-file STDIN <<'EOF'", script)
        self.assertTrue(script.rstrip().endswith("EOF"))

    def test_the_config_overrides_the_aurora_defaults(self):
        """The manifest's default_configuration describes Aurora; left to it, the
        local aws_db_instance would get engine_version 17.9 and db.r7g.large."""
        import json

        script = _nodes()[0]["script"]
        body = script.split("<<'EOF'\n", 1)[1].rsplit("\nEOF", 1)[0]
        config = json.loads(body)
        self.assertEqual(config["db_username"], "postgres")
        self.assertNotEqual(config.get("instance_type"), "db.r7g.large")

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


class SubnetGroupPlacementTests(unittest.TestCase):
    """Both RDS deploys must name a subnet group explicitly.

    Floci seeds a region's default VPC and subnets once per *region*
    (`Ec2Service.seededRegions`) but stores them per *account*, so every account
    after the first has no default VPC and `CreateDBInstance` fails with
    "No subnets available for DB subnet group default". Since Phase 1 put every
    environment in one Floci, that is the common case rather than an edge one.
    """

    def test_the_control_plane_node_names_the_group(self):
        from hmd_cli_neuronsphere.floci_deployer import LOCAL_DB_SUBNET_GROUP

        config = bd.postgres_instance_config()
        self.assertEqual(config["db_subnet_group_name"], LOCAL_DB_SUBNET_GROUP)

    def test_the_environment_entry_names_the_same_group(self):
        from hmd_cli_neuronsphere import bom_seeder
        from hmd_cli_neuronsphere.floci_deployer import LOCAL_DB_SUBNET_GROUP

        entry = next(
            e
            for e in bom_seeder.LOCAL_CORE_BOM
            if e["repo_instance_name"] == bom_seeder.ENV_DB_INSTANCE
        )
        self.assertEqual(
            entry["instance_configuration"]["db_subnet_group_name"],
            LOCAL_DB_SUBNET_GROUP,
        )

    def test_the_group_reaches_the_deploy_command(self):
        """It travels in the heredoc config, so a rename cannot silently drop it."""
        import json

        script = _nodes()[0]["script"]
        body = script.split("<<'EOF'\n", 1)[1].rsplit("\nEOF", 1)[0]
        self.assertIn("db_subnet_group_name", json.loads(body))
