"""Environment scoping in the BOM seeder.

Each named local environment gets its **own** ``hmd_lang_deployment.environment``
row, and ``repo_instance`` is unique by name *per Environment* -- so instance
names are deliberately identical across environments. That makes two things
load-bearing, and both are tested here:

1. ``scope_bom_entries`` stamps ``deployment_id`` and rewrites **nothing** else.
2. Every ``repo_instance`` lookup is environment-filtered. Unfiltered, they
   search by name alone, resolve to an arbitrary environment's instance, and
   would silently attach core Resources to the wrong cluster.

Run directly: ``python -m pytest src/python/tests/test_bom_env_scoping.py``
"""

import os
import unittest
from unittest import mock

from hmd_cli_neuronsphere import bom_seeder as b


class _Env:
    """Stand-in for env_registry.LocalEnvironment (no HMD_HOME needed)."""

    def __init__(self, slug, account_id="000000000002"):
        self.slug = slug
        self.name = slug
        self.deployment_id = slug
        self.account_id = account_id
        self.core_instance_name = "local-neuronsphere"
        self.db_container = f"hmd_db-{slug}"
        self.graph_container = f"global-graph-{slug}"
        self.floci_container = f"floci-{slug}"
        self.floci_alias = f"neuronsphere-{slug}"
        self.k3s_cluster = f"ns-{slug}-abc123"
        self.legacy_layout = False

    @property
    def is_default(self):
        return self.slug == "local"


class ScopeBomEntriesTests(unittest.TestCase):
    BOM = [
        {
            "repo_instance_name": "ext-secrets",
            "repo_class_name": "hmd-inf-ext-secrets",
            "deployment_id": "local",
            "instance_configuration": {},
            "dependencies": {
                "eks-cluster": "local-neuronsphere",
                "crds": "ext-secrets-crds",
            },
        }
    ]

    def test_sets_deployment_id(self):
        scoped = b.scope_bom_entries(self.BOM, _Env("dev2"))
        self.assertEqual(scoped[0]["deployment_id"], "dev2")

    def test_does_not_rename_instances_or_dependencies(self):
        # Renaming would break cloud parity: the same BOM entry must read
        # identically in every environment.
        scoped = b.scope_bom_entries(self.BOM, _Env("dev2"))
        self.assertEqual(scoped[0]["repo_instance_name"], "ext-secrets")
        self.assertEqual(
            scoped[0]["dependencies"],
            {"eks-cluster": "local-neuronsphere", "crds": "ext-secrets-crds"},
        )

    def test_does_not_mutate_the_input(self):
        original = [dict(e) for e in self.BOM]
        b.scope_bom_entries(self.BOM, _Env("dev2"))
        self.assertEqual(self.BOM, original)

    def test_no_env_is_a_passthrough(self):
        self.assertEqual(b.scope_bom_entries(self.BOM, None), self.BOM)


class DeploymentMatchesEnvTests(unittest.TestCase):
    def test_matches_on_deployment_id(self):
        env = _Env("dev2")
        self.assertTrue(b._deployment_matches_env({"deployment_id": "dev2"}, env))
        self.assertFalse(b._deployment_matches_env({"deployment_id": "local"}, env))

    def test_missing_deployment_id_still_matches(self):
        # A graph seeded before deployment_id was populated must keep resolving.
        self.assertTrue(b._deployment_matches_env({}, _Env("dev2")))

    def test_no_env_matches_everything(self):
        self.assertTrue(b._deployment_matches_env({"deployment_id": "other"}, None))


class FindCoreDeploymentNodeTests(unittest.TestCase):
    """Two environments hold an identically-named `local-neuronsphere` instance."""

    INSTANCES = [
        {"identifier": "inst-local", "name": "local-neuronsphere"},
        {"identifier": "inst-dev2", "name": "local-neuronsphere"},
    ]
    EDGES = [
        {"ref_from": "inst-local", "ref_to": "rid-local", "_created": "2026-01-01"},
        {"ref_from": "inst-dev2", "ref_to": "rid-dev2", "_created": "2026-01-02"},
    ]
    DEPLOYMENTS = [
        {"identifier": "rid-local", "deployment_id": "local"},
        {"identifier": "rid-dev2", "deployment_id": "dev2"},
    ]

    def _search(self, base_url, entity, filt):
        if entity.endswith("repo_instance"):
            return list(self.INSTANCES)
        if entity.endswith("repo_instance_has_repo_instance_deployment"):
            return list(self.EDGES)
        if entity.endswith("repo_instance_deployment"):
            return list(self.DEPLOYMENTS)
        return []

    def test_resolves_the_requested_environments_instance(self):
        with mock.patch.object(b, "_search_entities", side_effect=self._search):
            nodes = b.find_core_deployment_node("http://x", _Env("dev2"))
        self.assertEqual(
            nodes, [{"instance_name": "local-neuronsphere", "rid_nid": "rid-dev2"}]
        )

    def test_resolves_the_other_environment_independently(self):
        with mock.patch.object(b, "_search_entities", side_effect=self._search):
            nodes = b.find_core_deployment_node("http://x", _Env("local"))
        self.assertEqual(
            nodes, [{"instance_name": "local-neuronsphere", "rid_nid": "rid-local"}]
        )

    def test_unknown_environment_resolves_to_nothing(self):
        with mock.patch.object(b, "_search_entities", side_effect=self._search):
            self.assertEqual(b.find_core_deployment_node("http://x", _Env("nope")), [])


class RepoInstanceStatusTests(unittest.TestCase):
    INSTANCES = [
        {"identifier": "inst-local", "name": "project-bucket"},
        {"identifier": "inst-dev2", "name": "project-bucket"},
    ]
    EDGES = [
        {"ref_from": "inst-local", "ref_to": "rid-local", "_created": "2026-01-01"},
        {"ref_from": "inst-dev2", "ref_to": "rid-dev2", "_created": "2026-01-02"},
    ]
    DEPLOYMENTS = [
        {"identifier": "rid-local", "deployment_id": "local", "status": "DEPLOYED"},
        {"identifier": "rid-dev2", "deployment_id": "dev2", "status": "FAILED"},
    ]

    def _search(self, base_url, entity, filt):
        if entity.endswith("repo_instance"):
            return list(self.INSTANCES)
        if entity.endswith("repo_instance_has_repo_instance_deployment"):
            return list(self.EDGES)
        if entity.endswith("repo_instance_deployment"):
            return list(self.DEPLOYMENTS)
        return []

    def test_status_is_per_environment(self):
        with mock.patch.object(b, "_search_entities", side_effect=self._search):
            local = b._repo_instance_status("http://x", _Env("local"))
            dev2 = b._repo_instance_status("http://x", _Env("dev2"))
        self.assertEqual(local["project-bucket"], "DEPLOYED")
        self.assertEqual(dev2["project-bucket"], "FAILED")

    def test_compute_new_entries_uses_the_environments_status(self):
        bom = [{"repo_instance_name": "project-bucket", "repo_class_name": "x"}]
        with mock.patch.object(b, "_search_entities", side_effect=self._search):
            # DEPLOYED in `local` -> nothing new; FAILED in dev2 -> retry-eligible.
            self.assertEqual(
                b.compute_new_bom_entries("http://x", bom=bom, env=_Env("local")), []
            )
            self.assertEqual(
                len(b.compute_new_bom_entries("http://x", bom=bom, env=_Env("dev2"))), 1
            )


class BuildLocalCoreResourcesTests(unittest.TestCase):
    def _by_name(self, resources):
        return {r["resource_name"]: r for r in resources}

    def test_the_graph_points_at_the_environments_container(self):
        res = self._by_name(b.build_local_core_resources(env=_Env("dev2")))
        self.assertEqual(
            res["global-graph"]["output"]["endpoint"],
            "ws://global-graph-dev2:8182/gremlin",
        )

    def test_postgres_is_not_hand_seeded(self):
        """The Postgres Resource is produced by a real hmd-postgres-rds deploy.

        Seeding one here as well would give ms-deployment two producers of
        `database.neuronsphere.io/postgres` in the same environment, and a
        `database-instance` dependency could resolve to the hand-written record
        describing a container that no longer exists.
        """
        res = self._by_name(b.build_local_core_resources(env=_Env("dev2")))
        self.assertNotIn("hmd_db", res)
        seeded = {
            r["resource_definition"]["resource_definition_name"] for r in res.values()
        }
        self.assertNotIn("postgres", seeded)

    def test_resource_and_instance_names_are_not_env_scoped(self):
        res = self._by_name(b.build_local_core_resources(env=_Env("dev2")))
        self.assertIn("global-graph", res)
        self.assertEqual(res["global-graph"]["instance_name"], b.CORE_INSTANCE_NAME)

    def test_deployment_id_tag_added_alongside_environment_tag(self):
        res = self._by_name(b.build_local_core_resources(env=_Env("dev2")))
        tags = {t["key"]: t["value"] for t in res["global-graph"]["tags"]}
        # `environment` carries the environment's name -- its Environment.type,
        # and what the generated deploy runs with as `--environment`.
        self.assertEqual(tags["environment"], "dev2")
        self.assertEqual(tags["deployment_id"], "dev2")

    def test_environment_tag_defaults_to_local_without_an_env(self):
        res = self._by_name(b.build_local_core_resources())
        tags = {t["key"]: t["value"] for t in res["global-graph"]["tags"]}
        self.assertEqual(tags["environment"], "local")

    def test_without_env_the_control_plane_defaults_are_kept(self):
        res = self._by_name(b.build_local_core_resources())
        self.assertEqual(
            res["global-graph"]["output"]["endpoint"], "ws://global-graph:8182/gremlin"
        )
        tags = {t["key"] for t in res["global-graph"]["tags"]}
        self.assertNotIn("deployment_id", tags)

    def test_service_resources_carry_the_env_tag(self):
        services = [
            {
                "service_name": "hmd_ms_transform",
                "repo_class_name": "hmd-ms-transform",
                "api_base_url": "http://localhost/dev2/hmd_ms_transform",
            }
        ]
        res = b.build_service_resources(services, _Env("dev2"))
        tags = {t["key"]: t["value"] for t in res[0]["tags"]}
        self.assertEqual(tags["deployment_id"], "dev2")
        self.assertEqual(res[0]["instance_name"], b.CORE_INSTANCE_NAME)


class EnsureEnvironmentTests(unittest.TestCase):
    """``Environment.type`` is the environment's name.

    ``type`` is the Environment's business id and the only field ms-deployment
    resolves an environment by: ``get_valid_environment`` asserts exactly one
    match and ``apply_changeset``/``destroy_deploymentset`` index ``[0]``. Typing
    every local environment ``local`` therefore made a second environment either
    500 or silently operate on the first one's graph.
    """

    def test_searches_and_creates_by_name(self):
        searches = []

        def _search(base_url, entity, filt):
            searches.append(filt)
            return []

        with mock.patch.object(
            b, "_search_entities", side_effect=_search
        ), mock.patch.object(
            b, "_put_entity", return_value={"identifier": "e2"}
        ) as put:
            b.ensure_environment("http://x", _Env("dev2", account_id="000000000003"))
        self.assertEqual(searches[0]["attribute"], "type")
        self.assertEqual(searches[0]["value"], "dev2")
        payload = put.call_args[0][2]
        self.assertEqual(payload["type"], "dev2")
        self.assertEqual(payload["account_number"], "000000000003")
        # instance_name is not in the Environment schema; writing it was what
        # made every local environment look identical to ms-deployment.
        self.assertNotIn("instance_name", payload)

    def test_reuses_the_row_for_that_name(self):
        existing = [{"identifier": "e1", "type": "dev2"}]
        with mock.patch.object(
            b, "_search_entities", return_value=existing
        ), mock.patch.object(b, "_put_entity") as put:
            result = b.ensure_environment("http://x", _Env("dev2"))
        put.assert_not_called()
        self.assertEqual(result["identifier"], "e1")

    def test_default_environment_is_still_typed_local(self):
        # The default environment's name *is* `local`, so nothing changes for a
        # single-environment install -- including one predating named envs.
        existing = [{"identifier": "old", "type": "local"}]
        with mock.patch.object(
            b, "_search_entities", return_value=existing
        ), mock.patch.object(b, "_put_entity") as put:
            result = b.ensure_environment("http://x", _Env("local"))
        put.assert_not_called()
        self.assertEqual(result["identifier"], "old")

    def test_legacy_alias_still_works(self):
        with mock.patch.object(
            b, "_search_entities", return_value=[{"identifier": "e"}]
        ):
            self.assertEqual(b.ensure_local_environment("http://x")["identifier"], "e")


class ChangeSetNamingTests(unittest.TestCase):
    def test_default_environment_keeps_the_historical_name(self):
        with mock.patch.object(b, "_search_entities", return_value=[]):
            self.assertEqual(b._new_change_set_name("http://x"), "local-changeset")

    def test_named_environment_gets_its_own_base_name(self):
        with mock.patch.object(b, "_search_entities", return_value=[]):
            self.assertEqual(
                b._new_change_set_name("http://x", "dev2"), "dev2-changeset"
            )

    def test_collision_gets_a_suffix(self):
        with mock.patch.object(
            b, "_search_entities", return_value=[{"name": "dev2-changeset"}]
        ):
            name = b._new_change_set_name("http://x", "dev2")
        self.assertTrue(name.startswith("dev2-changeset-"))
        self.assertNotEqual(name, "dev2-changeset")


if __name__ == "__main__":
    unittest.main()
