"""Which Floci an API call reaches (``floci_deployer`` targets).

``FlociTarget`` carries two names for the same container and they are not
interchangeable. ``container`` is for ``docker exec``/``docker inspect``.
``alias`` is the only one that may go on the wire, because Compose registers
every *service key* as a network alias in every project sharing the network --
and both docker-compose.control-plane.yml and docker-compose.environment.yml
key their Floci service ``floci``. Addressing ``floci`` therefore round-robins
between the control-plane Floci and every environment's, splitting API Gateway
and Lambda state across emulated AWS accounts; the visible symptom is Floci
answering ``{"message":"Invalid API id specified"}`` for a gateway it never saw.

Run directly: ``python -m pytest src/python/tests/test_floci_targets.py``
"""

import unittest

from hmd_cli_neuronsphere import floci_deployer as fd


# Service keys in the shipped compose files. Docker DNS resolves each of these
# to more than one container as soon as an environment is up.
AMBIGUOUS_SERVICE_KEYS = {"floci", "db", "graph", "proxy"}


class _Env:
    legacy_layout = False

    def __init__(self, slug):
        self.slug = slug
        self.floci_container = f"floci-{slug}"
        self.floci_alias = f"neuronsphere-{slug}"
        self.floci_port = 19004
        self.account_id = "000000000002"


class _LegacyEnv(_Env):
    legacy_layout = True


class ControlPlaneTargetTests(unittest.TestCase):
    def test_addresses_the_explicit_alias_not_the_service_key(self):
        target = fd.control_plane_target()
        self.assertEqual(target.alias, "neuronsphere")
        self.assertEqual(target.internal_endpoint, "http://neuronsphere:4566")

    def test_keeps_the_container_name_for_docker_commands(self):
        # `docker exec`/`docker inspect` need the container, which is legitimately
        # named `floci` -- the ambiguity is only in DNS.
        self.assertEqual(fd.control_plane_target().container, "floci")


class EnvTargetTests(unittest.TestCase):
    def test_addresses_its_own_alias(self):
        target = fd.env_target(_Env("dev2"))
        self.assertEqual(target.alias, "neuronsphere-dev2")
        self.assertEqual(target.container, "floci-dev2")

    def test_a_legacy_env_resolves_to_the_control_plane(self):
        """A legacy-layout env shares the control-plane Floci, so it shares its
        names -- including the container name ``floci``."""
        target = fd.env_target(_LegacyEnv("local"))
        self.assertEqual(target, fd.control_plane_target())
        self.assertEqual(target.alias, "neuronsphere")


class NoTargetNamesAnAmbiguousHostTests(unittest.TestCase):
    def test_no_alias_is_a_compose_service_key(self):
        for target in (fd.control_plane_target(), fd.env_target(_Env("dev2"))):
            with self.subTest(target=target.name):
                self.assertNotIn(target.alias, AMBIGUOUS_SERVICE_KEYS)

    def test_no_internal_endpoint_is_a_compose_service_key(self):
        for target in (fd.control_plane_target(), fd.env_target(_Env("dev2"))):
            with self.subTest(target=target.name):
                host = target.internal_endpoint.split("//", 1)[1].split(":", 1)[0]
                self.assertNotIn(host, AMBIGUOUS_SERVICE_KEYS)


class PruneControlPlaneStraysTests(unittest.TestCase):
    """The pruner clears objects the alias collision put in the wrong account."""

    def test_the_control_plane_is_never_pruned(self):
        # Same allowlist, but in the control-plane account these are exactly
        # where they belong.
        self.assertEqual(fd.prune_control_plane_strays(fd.control_plane_target()), 0)

    def test_the_allowlist_cannot_match_a_user_service(self):
        self.assertEqual(
            fd.CONTROL_PLANE_ONLY_FUNCTIONS,
            {"hmd_ms_deployment", "hmd_ms_naming", "hmd_ms_artifact_lib"},
        )


if __name__ == "__main__":
    unittest.main()
