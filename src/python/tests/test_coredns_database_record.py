"""The database must resolve inside k3s, under both names it is addressed by.

The database is the one canonical name that is *not* a Docker container name.
Floci spawns RDS backends with opaque names (`floci-rds-db-<HEX>-<suffix>`) and
the CLI gives them a *network alias* -- but `docker inspect` resolves container
names, never aliases, so asking it for `hmd_db-local` simply errors and the
record is silently skipped.

That is why the live `coredns-custom` ConfigMap contained `neuronsphere`,
`global-graph` and `hmd_proxy` -- all real container names -- and no database
entry at all, and why hive-metastore's schematool reported:

    nc: getaddrinfo for host "hmd_db-local" port 5432: Name or service not known

Underscores are not the problem: `hmd_proxy` resolves in-cluster, verified
against a running CoreDNS.

Both names have to answer, because a chart and the secret it consumes disagree
about which to use: unmodified cloud charts address `hmd_db`, while connection
secrets carry `hmd_db-<slug>` (on the Docker network plain `hmd_db` is the
*control plane's* database, which an environment must not reach).
"""

import types
import unittest
from unittest import mock

from hmd_cli_neuronsphere import k3s_operators as ko


def _env(slug="local"):
    return types.SimpleNamespace(
        slug=slug,
        account_id="000000000001",
        deployment_id=slug,
        db_container=f"hmd_db-{slug}",
        graph_container=f"global-graph-{slug}",
        floci_container="floci",
        legacy_layout=False,
    )


REAL_DB_CONTAINER = "floci-rds-db-2DA37352D4204591932BBD0D-72b727"


class DatabaseRecordsAreWritten(unittest.TestCase):
    def _records(self, db_container=REAL_DB_CONTAINER):
        """Capture the (name, ip) pairs the CoreDNS pass would write."""
        applied = {}

        def fake_ip(container):
            return {
                "floci": "172.18.0.2",
                "hmd_proxy": "172.18.0.4",
                "global-graph-local": "172.18.0.8",
                REAL_DB_CONTAINER: "172.18.0.10",
            }.get(container)

        def fake_run(args, env=None, **kwargs):
            if args[:2] == ["kubectl", "apply"]:
                import yaml

                with open(args[-1]) as fh:
                    cm = yaml.safe_load(fh)
                applied["server"] = cm["data"]["neuronsphere.server"]
            return mock.Mock(returncode=0, stdout="", stderr="")

        with mock.patch.object(ko, "_resolve_floci_ip", side_effect=fake_ip):
            with mock.patch.object(ko, "_rds_container_for", return_value=db_container):
                with mock.patch.object(ko, "_run", side_effect=fake_run):
                    ko._ensure_coredns_floci_entry(_env())
        return applied.get("server", "")

    def test_canonical_name_resolves(self):
        """What unmodified cloud charts address."""
        self.assertIn("hmd_db:53 {", self._records())

    def test_environment_specific_name_resolves(self):
        """What the connection secrets carry."""
        self.assertIn("hmd_db-local:53 {", self._records())

    def test_both_point_at_the_backing_container(self):
        server = self._records()
        self.assertEqual(server.count("172.18.0.10"), 2)

    def test_an_unresolvable_database_is_reported_not_skipped_silently(self):
        """The previous behaviour: no record, no message, and a connection error
        minutes later in an unrelated pod."""
        with self.assertLogs(level="WARNING") as logs:
            server = self._records(db_container=None)
        self.assertNotIn("hmd_db:53", server)
        self.assertTrue(
            any("will not resolve inside the cluster" in m for m in logs.output),
            logs.output,
        )


class ContainerLookupUsesLabelsNotAliases(unittest.TestCase):
    def test_the_alias_is_never_passed_to_docker_inspect(self):
        """`docker inspect hmd_db-local` fails with "no such object" -- aliases
        are not objects. The lookup must go through Floci's labels."""
        with mock.patch(
            "hmd_cli_neuronsphere.floci_deployer.rds_container_name",
            return_value=REAL_DB_CONTAINER,
        ) as lookup:
            with mock.patch(
                "hmd_cli_neuronsphere.bom_seeder.env_db_identifier",
                return_value="environment-db-x",
            ):
                self.assertEqual(ko._rds_container_for(_env()), REAL_DB_CONTAINER)
        self.assertEqual(lookup.call_args.args[0], "environment-db-x")

    def test_a_lookup_failure_is_not_fatal(self):
        with mock.patch(
            "hmd_cli_neuronsphere.bom_seeder.env_db_identifier",
            side_effect=RuntimeError("boom"),
        ):
            self.assertIsNone(ko._rds_container_for(_env()))


if __name__ == "__main__":
    unittest.main()
