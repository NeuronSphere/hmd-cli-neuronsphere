"""Which emulated AWS account an API call reaches (``floci_deployer`` targets).

There is one Floci container. The control plane and every named environment are
separate *accounts* inside it, and ``FlociTarget.access_key_id`` is what selects
one: Floci resolves the account from the SigV4 access key id on the request, so a
12-digit AKID *is* the account. Every storage-backed service namespaces its data
under it.

That makes the access key the load-bearing field, and getting it wrong fails
silently -- the call succeeds against the wrong account rather than erroring.

``container`` and ``alias`` remain distinct: ``container`` is for
``docker exec``/``docker inspect``, ``alias`` is the only one that may go on the
wire, because Compose registers every *service key* as a network alias on the
shared network.

Run directly: ``python -m pytest src/python/tests/test_floci_targets.py``
"""

import os
import unittest
from unittest import mock

from hmd_cli_neuronsphere import floci_deployer as fd


# Service keys in the shipped compose files. Docker DNS resolves each of these
# to more than one container as soon as an environment is up.
AMBIGUOUS_SERVICE_KEYS = {"floci", "db", "graph", "proxy"}


class _Env:
    legacy_layout = False

    def __init__(self, slug, account_id="000000000002"):
        self.slug = slug
        self.account_id = account_id


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

    def test_signs_as_the_control_plane_account(self):
        target = fd.control_plane_target()
        self.assertEqual(target.access_key_id, "000000000000")
        self.assertEqual(target.access_key_id, target.account_id)


class EnvTargetTests(unittest.TestCase):
    def test_shares_the_single_floci(self):
        target = fd.env_target(_Env("dev2"))
        cp = fd.control_plane_target()
        self.assertEqual(target.alias, cp.alias)
        self.assertEqual(target.container, cp.container)
        self.assertEqual(target.endpoint, cp.endpoint)
        self.assertEqual(target.internal_endpoint, cp.internal_endpoint)

    def test_is_told_apart_only_by_its_account(self):
        target = fd.env_target(_Env("dev2", account_id="000000000002"))
        self.assertEqual(target.account_id, "000000000002")
        self.assertEqual(target.access_key_id, "000000000002")
        self.assertNotEqual(
            target.access_key_id, fd.control_plane_target().access_key_id
        )

    def test_two_environments_never_share_an_account(self):
        a = fd.env_target(_Env("dev2", account_id="000000000002"))
        b = fd.env_target(_Env("dev3", account_id="000000000003"))
        self.assertNotEqual(a.access_key_id, b.access_key_id)

    def test_a_legacy_env_resolves_to_the_control_plane(self):
        """A legacy-layout env *is* the control-plane account."""
        target = fd.env_target(_LegacyEnv("local"))
        self.assertEqual(target, fd.control_plane_target())
        self.assertEqual(target.alias, "neuronsphere")


class ClientCredentialTests(unittest.TestCase):
    """The client must sign with the target's account, not an ambient key.

    An ambient ``$AWS_ACCESS_KEY_ID`` used to be harmless -- accounts were
    separated by endpoint. Now it would silently route every environment's call
    into whichever account that key names.
    """

    def _akid(self, target, environ):
        with mock.patch.dict(os.environ, environ, clear=True):
            client = fd._get_client("s3", target)
        return client._request_signer._credentials.access_key

    def test_uses_the_targets_account_over_an_ambient_key(self):
        target = fd.env_target(_Env("dev2", account_id="000000000002"))
        self.assertEqual(
            self._akid(target, {"AWS_ACCESS_KEY_ID": "000000000009"}), "000000000002"
        )

    def test_defaults_to_the_control_plane_account(self):
        self.assertEqual(self._akid(None, {}), "000000000000")


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
