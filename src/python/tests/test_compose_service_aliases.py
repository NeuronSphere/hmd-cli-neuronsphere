"""Shipped compose files and configs must not create ambiguous hostnames.

Docker Compose registers each *service key* as a network alias on that
service's containers, in every project that joins the network -- independent of
``container_name``. The control-plane and environment compose files share one
external network, so any key they have in common resolves round-robin to two
different containers.

That is a live hazard rather than a theoretical one: it split the control
plane's API Gateway and Lambda state across emulated AWS accounts (surfacing as
Floci's ``{"message":"Invalid API id specified"}``) and pointed the Hive
metastore at whichever Postgres won a coin flip. The rule these tests enforce is
that a name we put on the wire is always an explicit ``networks.<net>.aliases``
entry or a ``container_name`` -- never a service key.

Run directly: ``python -m pytest src/python/tests/test_compose_service_aliases.py``
"""

import unittest
from pathlib import Path

import yaml

import hmd_cli_neuronsphere


SERVICES = Path(hmd_cli_neuronsphere.__file__).parent / "services"

# Names the CLI, the charts and the shipped configs actually dial. None of them
# may be a service key, or the name resolves to more than one container.
CANONICAL_HOSTNAMES = {
    "neuronsphere",
    "neuronsphere-workload",
    "neuronsphere-control",
    "hmd_db",
    "global-graph",
    "hmd_proxy",
}


def _compose_files():
    return sorted(SERVICES.glob("docker-compose*.yml"))


class ServiceKeyTests(unittest.TestCase):
    def test_there_are_compose_files_to_check(self):
        self.assertTrue(_compose_files())

    def test_no_canonical_hostname_is_a_compose_service_key(self):
        for path in _compose_files():
            with self.subTest(compose=path.name):
                doc = yaml.safe_load(path.read_text()) or {}
                keys = set((doc.get("services") or {}).keys())
                collisions = keys & CANONICAL_HOSTNAMES
                self.assertFalse(
                    collisions,
                    f"{path.name} uses {sorted(collisions)} as service key(s); "
                    f"Compose turns each into a network alias, so the name would "
                    f"resolve to this container as well as the one it names",
                )


class HiveConfigTests(unittest.TestCase):
    """The Hive metastore addresses a container, not a service key.

    ``db`` is a service key in both compose files, so ``jdbc:postgresql://db/``
    reached the control-plane Postgres or an environment's at random.
    """

    def test_metastore_addresses_hmd_db(self):
        for name in ("hive-site.xml", "metastore-site.xml"):
            with self.subTest(config=name):
                text = (SERVICES / "hive" / name).read_text()
                self.assertNotIn("postgresql://db/", text)
                self.assertIn("postgresql://hmd_db/", text)


class FlociHostnameTests(unittest.TestCase):
    """``FLOCI_HOSTNAME`` is baked into the URLs Floci hands back."""

    def test_the_control_plane_publishes_its_alias(self):
        doc = yaml.safe_load(
            (SERVICES / "docker-compose.control-plane.yml").read_text()
        )
        floci = doc["services"]["floci"]
        self.assertEqual(floci["environment"]["FLOCI_HOSTNAME"], "neuronsphere")
        aliases = floci["networks"]["neuronsphere_default"]["aliases"]
        self.assertIn("neuronsphere", aliases)

    def test_an_environment_publishes_its_own_container_name(self):
        doc = yaml.safe_load((SERVICES / "docker-compose.environment.yml").read_text())
        floci = doc["services"]["floci"]
        # Already unique per environment, and deliberately coupled to
        # env_target().internal_endpoint.
        self.assertEqual(
            floci["environment"]["FLOCI_HOSTNAME"], "${NS_ENV_FLOCI_CONTAINER}"
        )


if __name__ == "__main__":
    unittest.main()
