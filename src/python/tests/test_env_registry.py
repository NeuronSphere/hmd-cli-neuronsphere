"""Unit tests for the named local environment registry (``env_registry``).

Everything here runs against a temp ``HMD_HOME`` -- no Docker, no services.

Run directly: ``python -m pytest src/python/tests/test_env_registry.py``
"""

import json
import os
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from hmd_cli_neuronsphere import env_registry as er


class _TempHome(unittest.TestCase):
    """Base: every test gets its own HMD_HOME so registries never bleed."""

    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.home = Path(self._tmp.name)
        self._env = mock.patch.dict(
            os.environ,
            {"HMD_HOME": str(self.home)},
            clear=False,
        )
        self._env.start()
        # These would otherwise leak in from the developer's shell and change
        # container/cluster names mid-test.
        for var in (
            "HMD_LOCAL_ENV",
            "HMD_LOCAL_COMPOSE_PROJECT_NAME",
            "HMD_LOCAL_DOCKER_NETWORK",
            "HMD_LOCAL_K3S_CLUSTER_NAME",
            "HMD_LOCAL_K3S_KUBECONFIG",
            "HMD_LOCAL_ENV_PORT_BASE",
            "HMD_LOCAL_ENV_PORT_RANGE",
        ):
            os.environ.pop(var, None)

    def tearDown(self):
        self._env.stop()
        self._tmp.cleanup()


class ValidateSlugTests(_TempHome):
    def test_accepts_simple_names(self):
        for name in ("local", "dev2", "a", "my-env-1"):
            self.assertEqual(er.validate_slug(name), name)

    def test_normalizes_case_and_whitespace(self):
        self.assertEqual(er.validate_slug("  Dev2 "), "dev2")

    def test_rejects_invalid_shapes(self):
        for name in ("", "-lead", "UPPER!", "a" * 17, "has space", "under_score"):
            with self.assertRaises(er.EnvRegistryError):
                er.validate_slug(name)

    def test_rejects_reserved_route_paths(self):
        # An env named `argo` would make /argo/ ambiguous with the control-plane route.
        for name in ("argo", "api", "aws", "restapis", "hmd_ms_deployment"):
            with self.assertRaises(er.EnvRegistryError):
                er.validate_slug(name)


class CreateAndResolveTests(_TempHome):
    def test_create_populates_derived_identity(self):
        env = er.create_env("dev2")
        self.assertEqual(env.slug, "dev2")
        self.assertEqual(env.deployment_id, "dev2")
        # Shared: one Floci serves every account.
        self.assertEqual(env.floci_container, "floci")
        self.assertEqual(env.floci_alias, "neuronsphere")
        self.assertEqual(env.db_container, "hmd_db-dev2")
        self.assertEqual(env.graph_container, "global-graph-dev2")
        self.assertEqual(env.core_instance_name, "local-neuronsphere")
        self.assertNotEqual(env.account_id, er.CONTROL_PLANE_ACCOUNT_ID)
        self.assertTrue(env.state_dir.endswith("/.cache/environments/dev2"))
        self.assertTrue(
            env.kubeconfig.endswith("/.cache/environments/dev2/k3s/kubeconfig")
        )

    def test_instance_name_is_not_env_scoped(self):
        # repo_instance is unique by name *per Environment*, so the core
        # instance keeps the same name in every env -- matching the cloud.
        a = er.create_env("alpha")
        b = er.create_env("beta")
        self.assertEqual(a.core_instance_name, b.core_instance_name)

    def test_create_rejects_duplicates(self):
        er.create_env("dev2")
        with self.assertRaises(er.EnvRegistryError):
            er.create_env("dev2")

    def test_accounts_are_unique_and_never_control_plane(self):
        envs = [er.create_env(f"e{i}") for i in range(5)]
        ids = [e.account_id for e in envs]
        self.assertEqual(len(set(ids)), len(ids))
        self.assertNotIn(er.CONTROL_PLANE_ACCOUNT_ID, ids)

    def test_resolve_prefers_explicit_then_env_var_then_default(self):
        er.ensure_default_env()
        er.create_env("dev2")
        self.assertEqual(er.resolve_env("dev2").slug, "dev2")
        with mock.patch.dict(os.environ, {"HMD_LOCAL_ENV": "dev2"}):
            self.assertEqual(er.resolve_env().slug, "dev2")
            # An explicit name still wins over the env var.
            self.assertEqual(er.resolve_env("local").slug, "local")
        self.assertEqual(er.resolve_env().slug, "local")

    def test_resolve_unknown_names_the_create_command(self):
        er.ensure_default_env()
        with self.assertRaises(er.EnvRegistryError) as ctx:
            er.resolve_env("nope")
        self.assertIn("env create nope", str(ctx.exception))

    def test_ensure_default_is_idempotent(self):
        a = er.ensure_default_env()
        b = er.ensure_default_env()
        self.assertEqual(a.account_id, b.account_id)
        self.assertEqual(len(er.list_envs()), 1)


class PortSlotTests(_TempHome):
    def test_ports_derive_from_slot(self):
        env = er.create_env("dev2")
        self.assertEqual(env.floci_port, env.port_base + env.port_slot * 4)
        self.assertEqual(env.trino_port, env.floci_port + 1)
        self.assertEqual(env.graph_port, env.floci_port + 2)
        self.assertEqual(env.spare_port, env.floci_port + 3)

    def test_same_name_gets_the_same_slot_across_registries(self):
        first = er.create_env("dev2").port_slot
        er.remove_env("dev2")
        self.assertEqual(er.create_env("dev2").port_slot, first)

    def test_slots_never_collide(self):
        envs = [er.create_env(f"env{i}") for i in range(er.MAX_ENVS)]
        slots = [e.port_slot for e in envs]
        self.assertEqual(len(set(slots)), er.MAX_ENVS)

    def test_exhausting_slots_raises(self):
        for i in range(er.MAX_ENVS):
            er.create_env(f"env{i}")
        with self.assertRaises(er.EnvRegistryError):
            er.create_env("one-too-many")

    def test_env_port_range_covers_every_slot(self):
        lo, hi = (int(p) for p in er.env_port_range().split("-"))
        self.assertEqual(lo, er.DEFAULT_PORT_BASE)
        self.assertEqual(hi, er.DEFAULT_PORT_BASE + er.MAX_ENVS * er.PORTS_PER_ENV - 1)
        env = er.create_env("dev2")
        self.assertTrue(lo <= env.floci_port <= hi and lo <= env.spare_port <= hi)


class PersistenceTests(_TempHome):
    def test_round_trips_through_disk(self):
        created = er.create_env("dev2")
        er.record_bootstrap(created, csd_nid="csd-1", k3s_uid="uid-1")

        loaded = er.resolve_env("dev2")
        self.assertEqual(loaded.account_id, created.account_id)
        self.assertEqual(loaded.k3s_cluster, created.k3s_cluster)
        self.assertEqual(loaded.port_slot, created.port_slot)
        self.assertEqual(loaded.bootstrap["csd_nid"], "csd-1")
        self.assertEqual(loaded.bootstrap["k3s_uid"], "uid-1")

    def test_derived_properties_are_not_persisted(self):
        # Persisting derived ports would let a stale value outlive a slot change.
        er.create_env("dev2")
        raw = json.loads(er.registry_path().read_text())
        stored = raw["environments"]["dev2"]
        for derived in ("floci_port", "trino_port", "graph_port", "spare_port"):
            self.assertNotIn(derived, stored)

    def test_save_is_atomic_leaving_no_temp_file(self):
        er.create_env("dev2")
        leftovers = list(er.registry_path().parent.glob("*.tmp"))
        self.assertEqual(leftovers, [])

    def test_clear_bootstrap(self):
        env = er.create_env("dev2")
        er.record_bootstrap(env, csd_nid="csd-1")
        er.clear_bootstrap(env)
        self.assertEqual(er.resolve_env("dev2").bootstrap, {})

    def test_remove_reassigns_default(self):
        er.ensure_default_env()
        er.create_env("dev2")
        er.set_default("dev2")
        er.remove_env("dev2")
        self.assertEqual(er.load().default_env, "local")


class LegacyMigrationTests(_TempHome):
    def _write_marker(self, payload):
        path = er.legacy_marker_path()
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(payload))

    def test_marker_migrates_into_a_legacy_default_env(self):
        self._write_marker({"mode": "extend", "csd_nid": "csd-9", "k3s_uid": "uid-9"})
        env = er.resolve_env("local")
        self.assertTrue(env.legacy_layout)
        # The legacy env *is* the control plane: same containers, same account.
        self.assertEqual(env.floci_container, "floci")
        self.assertEqual(env.db_container, "hmd_db")
        self.assertEqual(env.graph_container, "global-graph")
        self.assertEqual(env.account_id, er.CONTROL_PLANE_ACCOUNT_ID)
        self.assertEqual(env.bootstrap["csd_nid"], "csd-9")

    def test_persisted_floci_state_alone_triggers_migration(self):
        data = self.home / "floci" / "data"
        data.mkdir(parents=True)
        (data / "s3-buckets.json").write_text("{}")
        reg = er.load()
        self.assertIn("local", reg.environments)
        self.assertTrue(reg.environments["local"].legacy_layout)
        self.assertTrue(reg.control_plane.bootstrapped)

    def test_fresh_home_gets_the_split_layout(self):
        reg = er.load()
        self.assertEqual(reg.environments, {})
        env = er.ensure_default_env()
        self.assertFalse(env.legacy_layout)
        self.assertEqual(env.floci_container, "floci")
        # Its own account inside that shared Floci is what makes it distinct.
        self.assertNotEqual(env.account_id, er.CONTROL_PLANE_ACCOUNT_ID)

    def test_migration_does_not_rewrite_an_existing_registry(self):
        er.create_env("dev2")
        self._write_marker({"csd_nid": "csd-old"})
        self.assertNotIn("local", er.load().environments)


class ComposeEnvTests(_TempHome):
    def test_exports_every_placeholder_the_compose_file_needs(self):
        env = er.create_env("dev2")
        exported = env.compose_env()
        for key in (
            "NS_ENV_SLUG",
            "NS_ENV_ACCOUNT_ID",
            "NS_ENV_DB_CONTAINER",
            "NS_ENV_GRAPH_CONTAINER",
            "NS_ENV_STATE_DIR",
            "NS_ENV_DEPLOYMENT_ID",
        ):
            self.assertTrue(exported.get(key), f"{key} missing or empty")
        self.assertTrue(all(isinstance(v, str) for v in exported.values()))

    def test_exports_no_floci_placeholders(self):
        """The environment compose file no longer defines a Floci service."""
        exported = er.create_env("dev2").compose_env()
        self.assertNotIn("NS_ENV_FLOCI_CONTAINER", exported)
        self.assertNotIn("NS_ENV_FLOCI_ALIAS", exported)

    def test_state_dirs_are_all_under_the_cache_env_dir(self):
        env = er.create_env("dev2")
        root = er.environments_root() / "dev2"
        for path in env.state_dirs():
            self.assertTrue(str(path).startswith(str(root)), path)

    def test_state_dirs_no_longer_include_a_floci_data_dir(self):
        """An environment's Floci state lives in the shared Floci, keyed by account.

        Creating the old per-environment directory would resurrect exactly the
        state `_assert_no_legacy_env_floci_state` refuses to start over.
        """
        env = er.create_env("dev2")
        self.assertNotIn(env.floci_data_dir, env.state_dirs())


class LegacyFlociStateTests(_TempHome):
    """Pre-collapse per-environment Floci state must be detected, never ignored.

    It cannot be migrated (Floci keys persisted records by an undocumented
    account prefix), and silently skipping it would make an environment's
    Lambdas, gateways, buckets and secrets vanish while `up` reported success.
    """

    def test_an_empty_or_absent_dir_is_not_stale(self):
        er.create_env("dev2")
        self.assertEqual(er.legacy_env_floci_state(er.load()), [])

    def test_a_populated_dir_is_reported(self):
        env = er.create_env("dev2")
        env.floci_data_dir.mkdir(parents=True, exist_ok=True)
        (env.floci_data_dir / "s3.json").write_text("{}")
        self.assertEqual(
            [e.slug for e in er.legacy_env_floci_state(er.load())], ["dev2"]
        )

    def test_a_legacy_layout_env_is_exempt(self):
        """Its "own" Floci data dir *is* the control plane's -- where it belongs."""
        reg = er.load()
        env = er._legacy_environment({})
        env.floci_data_dir.mkdir(parents=True, exist_ok=True)
        (env.floci_data_dir / "s3.json").write_text("{}")
        reg.environments[env.slug] = env
        self.assertEqual(er.legacy_env_floci_state(reg), [])


if __name__ == "__main__":
    unittest.main()
