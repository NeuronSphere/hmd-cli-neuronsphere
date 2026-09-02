"""Unit tests for the local BOM seeder (bom_seeder).

Cover the parts that need no running service or Docker:

* ``_resolve_bom`` always prepends the ``local-neuronsphere`` core entry, appends the
  ext-secrets add-ons by default (unless explicitly opted out), and de-dupes
  by instance name;
* ``build_local_core_resources`` stamps every core Resource with the single
  ``hmd-cli-neuronsphere`` RepoClass / ``local-neuronsphere`` instance;
* ``submit_local_resources`` submits against the ``local-neuronsphere`` node's rid;
* ``declare_core_produces`` declares the three core produced types;
* ``upsert_repo_resource_definitions`` upserts a repo's declared RDs.

Run directly (``python -m pytest src/python/tests/test_bom_seeder.py``) — no
service or Docker required.
"""

import os
from pathlib import Path
import json
import unittest
from unittest import mock

from hmd_cli_neuronsphere import bom_seeder as b


class ResolveBomTests(unittest.TestCase):
    def setUp(self):
        # A clean env: no BOM file, ext-secrets at its default (on).
        self._env = mock.patch.dict(
            os.environ,
            {
                k: v
                for k, v in os.environ.items()
                if k
                not in {
                    "HMD_LOCAL_BOM_FILE",
                    "HMD_LOCAL_NEURONSPHERE_ENABLE_EXT_SECRETS",
                    "HMD_LOCAL_NEURONSPHERE_ENABLE_GUI",
                    "HMD_LOCAL_GUI_HOST_PORT",
                }
            },
            clear=True,
        )
        self._env.start()
        # ext-secrets is on by default now, so _resolve_bom() always attempts
        # docker-credential injection -- mock it so tests don't touch the real
        # ~/.docker/config.json. Individual tests below layer their own patch of
        # the same target where they want a specific return value; that's a
        # harmless nested override, not a conflict.
        self._docker_creds = mock.patch(
            "hmd_cli_neuronsphere.bom_seeder.local_docker_config_json",
            return_value=None,
        )
        self._docker_creds.start()
        # Isolate from whatever hmd-cli-* plugin packages happen to be pip-installed
        # in the dev environment (e.g. hmd-cli-plugin-ns-telemetry, whose own
        # EXT_SECRETS_BOM copy would otherwise mask the core opt-out flag below).
        self._plugin_entries = mock.patch.object(
            b, "_collect_plugin_bom_entries", return_value=[]
        )
        self._plugin_entries.start()

    def tearDown(self):
        self._plugin_entries.stop()
        self._docker_creds.stop()
        self._env.stop()

    def test_core_entry_is_prepended(self):
        names = [e["repo_instance_name"] for e in b._resolve_bom()]
        self.assertEqual(names[0], b.CORE_INSTANCE_NAME)
        entry = b._resolve_bom()[0]
        self.assertEqual(entry["repo_class_name"], b.CORE_REPO_CLASS)

    def test_resolve_plugin_bom_excludes_core_entry(self):
        # Phase B's BOM (resolve_plugin_bom) must never include the core
        # instance -- Phase A applies that alone, in its own changeset.
        names = [e["repo_instance_name"] for e in b.resolve_plugin_bom()]
        self.assertNotIn(b.CORE_INSTANCE_NAME, names)

    def test_resolve_bom_is_core_plus_plugin_bom(self):
        full_names = [e["repo_instance_name"] for e in b._resolve_bom()]
        plugin_names = [e["repo_instance_name"] for e in b.resolve_plugin_bom()]
        core_names = [e["repo_instance_name"] for e in b.LOCAL_CORE_BOM]
        self.assertEqual(full_names[: len(core_names)], core_names)
        self.assertEqual(full_names[len(core_names) :], plugin_names)

    def test_the_core_bom_provisions_the_environments_database(self):
        """Everything with a `database-instance` dependency resolves against it,
        so it belongs in the core changeset rather than the plugin one."""
        core = {e["repo_instance_name"]: e for e in b.LOCAL_CORE_BOM}
        self.assertIn(b.ENV_DB_INSTANCE, core)
        self.assertEqual(core[b.ENV_DB_INSTANCE]["repo_class_name"], "hmd-postgres-rds")

    def test_ext_secrets_on_by_default(self):
        names = [e["repo_instance_name"] for e in b._resolve_bom()]
        self.assertIn("ext-secrets", names)
        self.assertIn("ext-secrets-crds", names)

    def test_ext_secrets_opt_out_removes_entries(self):
        os.environ["HMD_LOCAL_NEURONSPHERE_ENABLE_EXT_SECRETS"] = "false"
        names = [e["repo_instance_name"] for e in b._resolve_bom()]
        self.assertNotIn("ext-secrets", names)
        self.assertNotIn("ext-secrets-crds", names)

    @mock.patch(
        "hmd_cli_neuronsphere.bom_seeder.local_docker_config_json", return_value=None
    )
    def test_ext_secrets_opt_in_appends_crds_first(self, _mock_docker_creds):
        os.environ["HMD_LOCAL_NEURONSPHERE_ENABLE_EXT_SECRETS"] = "true"
        names = [e["repo_instance_name"] for e in b._resolve_bom()]
        self.assertEqual(names[0], b.CORE_INSTANCE_NAME)
        self.assertIn("ext-secrets-crds", names)
        self.assertLess(names.index("ext-secrets-crds"), names.index("ext-secrets"))

    # -- Deployment GUI ----------------------------------------------------
    #
    # The GUI is the control plane's own management surface, not a platform
    # workload, so it runs as a container in the control-plane compose file and
    # is absent from every BOM. These guard against it creeping back in.

    def test_gui_is_not_in_the_bom(self):
        names = [e["repo_instance_name"] for e in b._resolve_bom()]
        self.assertNotIn("deployment-gui", names)
        self.assertNotIn("deployment-gui-db-account", names)

    def test_gui_repo_class_is_not_in_the_bom(self):
        """It is deployed by compose, so no DAG node should reference the chart."""
        classes = [e.get("repo_class_name") for e in b._resolve_bom()]
        self.assertNotIn("hmd-app-neuronsphere", classes)

    def test_gui_port_defaults_to_the_slot_zero_spare_port(self):
        os.environ.pop("HMD_LOCAL_GUI_HOST_PORT", None)
        self.assertEqual(b.gui_port(), 19003)

    def test_gui_port_is_overridable(self):
        os.environ["HMD_LOCAL_GUI_HOST_PORT"] = "19107"
        self.assertEqual(b.gui_port(), 19107)

    def test_gui_port_falls_back_when_the_override_is_not_a_number(self):
        os.environ["HMD_LOCAL_GUI_HOST_PORT"] = "not-a-port"
        self.assertEqual(b.gui_port(), 19003)

    def test_gui_enabled_by_default_and_opt_out(self):
        self.assertTrue(b.gui_enabled())
        os.environ["HMD_LOCAL_NEURONSPHERE_ENABLE_GUI"] = "false"
        self.assertFalse(b.gui_enabled())

    @mock.patch(
        "hmd_cli_neuronsphere.bom_seeder.local_docker_config_json", return_value=None
    )
    def test_ext_secrets_dep_names_local_producer(self, _mock_docker_creds):
        os.environ["HMD_LOCAL_NEURONSPHERE_ENABLE_EXT_SECRETS"] = "1"
        bom = {e["repo_instance_name"]: e for e in b._resolve_bom()}
        deps = bom["ext-secrets"]["dependencies"]
        self.assertEqual(deps["eks-cluster"], b.CORE_INSTANCE_NAME)
        self.assertEqual(deps["compute"], b.CORE_INSTANCE_NAME)
        self.assertEqual(deps["crds"], "ext-secrets-crds")

    @mock.patch(
        "hmd_cli_neuronsphere.bom_seeder.local_docker_config_json", return_value=None
    )
    def test_ext_secrets_instance_config_uses_local_secret_store_auth(
        self, _mock_docker_creds
    ):
        os.environ["HMD_LOCAL_NEURONSPHERE_ENABLE_EXT_SECRETS"] = "true"
        bom = {e["repo_instance_name"]: e for e in b._resolve_bom()}
        config = bom["ext-secrets"]["instance_configuration"]
        self.assertTrue(config["clusterSecretStore"]["enabled"])
        self.assertTrue(config["clusterSecretStore"]["local"])
        self.assertEqual(config["clusterSecretStore"]["name"], "aws-secrets-manager")
        self.assertTrue(config["parameterStoreSecretStore"]["enabled"])
        self.assertTrue(config["parameterStoreSecretStore"]["local"])
        self.assertEqual(
            config["parameterStoreSecretStore"]["name"], "aws-parameter-store"
        )
        self.assertTrue(config["dockerRepoSecret"]["enabled"])
        self.assertEqual(
            config["dockerRepoSecret"]["secretStoreName"], "aws-parameter-store"
        )

    def test_dedupe_is_idempotent(self):
        bom = b._resolve_bom()
        self.assertEqual(b._dedupe_bom(bom + bom), bom)

    def test_truthy(self):
        self.assertTrue(b._is_truthy("true"))
        self.assertTrue(b._is_truthy("YES"))
        self.assertTrue(b._is_truthy("1"))
        self.assertFalse(b._is_truthy(None))
        self.assertFalse(b._is_truthy("false"))
        self.assertFalse(b._is_truthy(""))

    def test_falsy(self):
        self.assertTrue(b._is_falsy("false"))
        self.assertTrue(b._is_falsy("FALSE"))
        self.assertTrue(b._is_falsy("0"))
        self.assertTrue(b._is_falsy("no"))
        self.assertFalse(b._is_falsy(None))
        self.assertFalse(b._is_falsy(""))
        self.assertFalse(b._is_falsy("true"))


class InjectDockerCredentialsTests(unittest.TestCase):
    def _ext_secrets_entry(self):
        return {
            "repo_instance_name": "ext-secrets",
            "repo_class_name": "hmd-inf-ext-secrets",
            "deployment_id": "local",
            "instance_configuration": {},
            "dependencies": {},
        }

    @mock.patch("hmd_cli_neuronsphere.bom_seeder.local_docker_config_json")
    def test_no_ext_secrets_entry_skips_host_read(self, mock_creds):
        bom = [{"repo_instance_name": "vpc", "repo_class_name": "hmd-vpc"}]
        b._inject_docker_credentials(bom)
        mock_creds.assert_not_called()

    @mock.patch(
        "hmd_cli_neuronsphere.bom_seeder.local_docker_config_json",
        return_value='{"auths": {"ghcr.io": {"auth": "xyz"}}}',
    )
    def test_ext_secrets_entry_gets_docker_config_json(self, _mock_creds):
        entry = self._ext_secrets_entry()
        b._inject_docker_credentials([entry])
        self.assertEqual(
            entry["instance_configuration"]["docker_config_json"],
            '{"auths": {"ghcr.io": {"auth": "xyz"}}}',
        )

    @mock.patch(
        "hmd_cli_neuronsphere.bom_seeder.local_docker_config_json", return_value=None
    )
    def test_no_resolved_credentials_leaves_config_untouched(self, _mock_creds):
        entry = self._ext_secrets_entry()
        b._inject_docker_credentials([entry])
        self.assertNotIn("docker_config_json", entry["instance_configuration"])

    @mock.patch(
        "hmd_cli_neuronsphere.bom_seeder.local_docker_config_json",
        return_value='{"auths": {}}',
    )
    def test_all_ext_secrets_entries_patched(self, _mock_creds):
        entry_a, entry_b = self._ext_secrets_entry(), self._ext_secrets_entry()
        b._inject_docker_credentials([entry_a, entry_b])
        self.assertEqual(
            entry_a["instance_configuration"]["docker_config_json"], '{"auths": {}}'
        )
        self.assertEqual(
            entry_b["instance_configuration"]["docker_config_json"], '{"auths": {}}'
        )


class CoreResourceTests(unittest.TestCase):
    def test_all_core_resources_owned_by_cli_repo_class(self):
        res = b.build_local_core_resources(cluster_name="ns-local")
        self.assertTrue(res)
        for r in res:
            self.assertEqual(r["repo_class_name"], b.CORE_REPO_CLASS)
            self.assertEqual(r["instance_name"], b.CORE_INSTANCE_NAME)
        types = {r["resource_definition"]["resource_definition_name"] for r in res}
        self.assertEqual(
            types,
            {
                "docker-network",
                # No "postgres": produced by the hmd-postgres-rds deploy.
                "graph-database",
                "kubernetes-cluster",
                "compute-node",
                "ingress-controller",
            },
        )

    def test_network_only_without_cluster(self):
        # Without a cluster, only the always-on core resources remain: Docker
        # network, shared Postgres, and JanusGraph (the cluster-conditional
        # resources -- kubernetes-cluster/compute-node/ingress-controller --
        # are omitted).
        res = b.build_local_core_resources(cluster_name=None)
        types = {r["resource_definition"]["resource_definition_name"] for r in res}
        self.assertEqual(types, {"docker-network", "graph-database"})

    def test_service_microservice_resources(self):
        services = [
            {
                "service_name": "hmd_ms_deployment",
                "repo_class_name": "hmd-ms-deployment",
                "api_base_url": "http://localhost/hmd_ms_deployment",
            }
        ]
        res = b.build_local_core_resources(cluster_name="ns-local", services=services)
        micro = [
            r
            for r in res
            if r["resource_definition"]["resource_definition_name"] == "microservice"
        ]
        self.assertEqual(len(micro), 1)
        self.assertEqual(micro[0]["repo_class_name"], b.CORE_REPO_CLASS)
        self.assertEqual(
            micro[0]["output"]["api_base_url"], "http://localhost/hmd_ms_deployment"
        )
        tags = {t["key"]: t["value"] for t in micro[0]["tags"]}
        self.assertEqual(tags["environment"], "local")
        self.assertEqual(tags["repo_class"], "hmd-ms-deployment")

    def test_no_services_keeps_core_only(self):
        # Default (no services) must not change the core resource set.
        res = b.build_local_core_resources(cluster_name="ns-local")
        types = {r["resource_definition"]["resource_definition_name"] for r in res}
        self.assertEqual(
            types,
            {
                "docker-network",
                # No "postgres": produced by the hmd-postgres-rds deploy.
                "graph-database",
                "kubernetes-cluster",
                "compute-node",
                "ingress-controller",
            },
        )

    def test_ingress_controller_only_with_cluster(self):
        # The Traefik ingress-controller Resource is tied to the k3s cluster.
        res = b.build_local_core_resources(cluster_name="ns-local")
        ingress = [
            r
            for r in res
            if r["resource_definition"]["resource_definition_name"]
            == "ingress-controller"
        ]
        self.assertEqual(len(ingress), 1)
        self.assertEqual(
            ingress[0]["resource_definition"]["resource_namespace"],
            "kubernetes.neuronsphere.io",
        )
        # Output must satisfy the effective schema inherited from the `deployment`
        # base type (name + namespace required) plus the ingress-controller field.
        # The class is `alb`, not `traefik`: local Traefik is configured to answer
        # to the cloud's ALB class, so charts render one Ingress for both. The
        # controller is still Traefik -- only the class it claims differs.
        self.assertEqual(ingress[0]["output"]["ingress_class"], "alb")
        self.assertEqual(ingress[0]["output"]["name"], "traefik")
        self.assertEqual(ingress[0]["output"]["namespace"], "kube-system")
        self.assertEqual(ingress[0]["resource_name"], "ns-local-traefik")

        # Absent when there is no cluster (see test_network_only_without_cluster).
        no_cluster = b.build_local_core_resources(cluster_name=None)
        self.assertNotIn(
            "ingress-controller",
            {r["resource_definition"]["resource_definition_name"] for r in no_cluster},
        )

    def test_rid_lookup(self):
        nodes = [
            {"instance_name": "other", "rid_nid": "rid-1"},
            {"instance_name": b.CORE_INSTANCE_NAME, "rid_nid": "rid-core"},
        ]
        self.assertEqual(b._rid_for_instance(nodes, b.CORE_INSTANCE_NAME), "rid-core")
        self.assertIsNone(b._rid_for_instance(nodes, "missing"))
        self.assertIsNone(b._rid_for_instance(None, b.CORE_INSTANCE_NAME))


class SubmitLocalResourcesTests(unittest.TestCase):
    def test_submits_against_core_node_rid(self):
        resources = b.build_local_core_resources(cluster_name="ns-local")
        nodes = [{"instance_name": b.CORE_INSTANCE_NAME, "rid_nid": "rid-core"}]
        calls = []
        with mock.patch.object(
            b,
            "_post_apiop",
            side_effect=lambda url, op, payload=None, **k: calls.append((op, payload))
            or {},
        ):
            n = b.submit_local_resources("http://x", resources, nodes)
        self.assertEqual(n, len(resources))
        for op, payload in calls:
            self.assertEqual(op, "submit_resources")
            self.assertEqual(payload["repo_instance_deployment_id"], "rid-core")

    def test_no_core_node_skips(self):
        resources = b.build_local_core_resources(cluster_name="ns-local")
        with mock.patch.object(b, "_post_apiop") as post:
            n = b.submit_local_resources("http://x", resources, nodes=[])
        self.assertEqual(n, 0)
        post.assert_not_called()


class DeclareCoreProducesTests(unittest.TestCase):
    def test_declares_core_types(self):
        def fake_post(url, op, payload=None, **k):
            if op.startswith("find_repo_class_versions/"):
                return [{"identifier": "rcv-core", "version": "0.5"}]
            return {}

        with mock.patch.object(b, "_post_apiop", side_effect=fake_post) as post:
            n = b.declare_core_produces("http://x")
        self.assertEqual(n, 8)
        declared = [
            c.args[2]["resource_definition"]["resource_definition_name"]
            for c in post.mock_calls
            if c.args[1] == "declare_produces_resource_definition"
        ]
        self.assertEqual(
            set(declared),
            {
                "kubernetes-cluster",
                "compute-node",
                "docker-network",
                "ingress-controller",
                # No "postgres": hmd-postgres-rds produces it now, so declaring
                # the core RepoClass as a producer too would give the same
                # environment two.
                "graph-database",
                "vpc",
                "microservice",
                "network",
            },
        )

    def test_no_rcv_is_noop(self):
        with mock.patch.object(b, "_post_apiop", return_value=[]):
            self.assertEqual(b.declare_core_produces("http://x"), 0)


class UpsertRepoResourceDefinitionsTests(unittest.TestCase):
    def test_reads_ext_secrets_resource_yaml(self):
        repo_home = os.environ.get("HMD_REPO_HOME")
        if not repo_home or not os.path.isdir(
            os.path.join(
                repo_home, "hmd-inf-ext-secrets-crds", "meta-data", "resources"
            )
        ):
            self.skipTest("HMD_REPO_HOME with ext-secrets repos not available")
        calls = []
        with mock.patch.object(
            b,
            "_post_apiop",
            side_effect=lambda url, op, payload=None, **k: calls.append(payload) or {},
        ):
            n = b.upsert_repo_resource_definitions(
                "http://x", "hmd-inf-ext-secrets-crds"
            )
        self.assertEqual(n, 1)
        self.assertEqual(calls[0]["resource_definition_name"], "external-secrets-crds")
        self.assertEqual(
            calls[0]["parent"]["resource_definition_name"],
            "custom-resource-definition",
        )


class FindCoreDeploymentNodeTests(unittest.TestCase):
    def _search(self, instances, edges):
        """Return a _search_entities stand-in over (repo_instance, has_deployment)."""

        def fake_search(url, entity_type, filter_):
            if entity_type.endswith("repo_instance"):
                return instances
            if entity_type.endswith("repo_instance_has_repo_instance_deployment"):
                return edges
            return []

        return fake_search

    def test_returns_node_for_local_k3s(self):
        instances = [{"identifier": "inst-core", "name": b.CORE_INSTANCE_NAME}]
        edges = [{"ref_from": "inst-core", "ref_to": "rid-core", "_created": "2026"}]
        with mock.patch.object(
            b, "_search_entities", side_effect=self._search(instances, edges)
        ):
            nodes = b.find_core_deployment_node("http://x")
        self.assertEqual(
            nodes, [{"instance_name": b.CORE_INSTANCE_NAME, "rid_nid": "rid-core"}]
        )

    def test_empty_when_no_instance(self):
        with mock.patch.object(b, "_search_entities", side_effect=self._search([], [])):
            self.assertEqual(b.find_core_deployment_node("http://x"), [])

    def test_empty_when_no_edge(self):
        instances = [{"identifier": "inst-core", "name": b.CORE_INSTANCE_NAME}]
        # Edge belongs to a different instance -> filtered out.
        edges = [{"ref_from": "other", "ref_to": "rid-other", "_created": "2026"}]
        with mock.patch.object(
            b, "_search_entities", side_effect=self._search(instances, edges)
        ):
            self.assertEqual(b.find_core_deployment_node("http://x"), [])

    def test_picks_most_recent_deployment(self):
        instances = [{"identifier": "inst-core", "name": b.CORE_INSTANCE_NAME}]
        edges = [
            {"ref_from": "inst-core", "ref_to": "rid-old", "_created": "2026-01-01"},
            {"ref_from": "inst-core", "ref_to": "rid-new", "_created": "2026-07-01"},
        ]
        with mock.patch.object(
            b, "_search_entities", side_effect=self._search(instances, edges)
        ):
            nodes = b.find_core_deployment_node("http://x")
        self.assertEqual(nodes[0]["rid_nid"], "rid-new")


class ResyncLocalResourcesTests(unittest.TestCase):
    def test_refreshes_and_submits(self):
        with mock.patch.object(
            b, "seed_base_resource_definitions"
        ) as seed, mock.patch.object(
            b, "declare_core_produces"
        ) as declare, mock.patch.object(
            b,
            "find_core_deployment_node",
            return_value=[
                {"instance_name": b.CORE_INSTANCE_NAME, "rid_nid": "rid-core"}
            ],
        ), mock.patch.object(
            b, "submit_local_resources", return_value=4
        ) as submit:
            n = b.resync_local_resources("http://x", "neuronsphere")
        self.assertEqual(n, 4)
        seed.assert_called_once()
        declare.assert_called_once()
        # Submitted the built core resources against the found node.
        submit_args = submit.call_args
        self.assertEqual(
            submit_args.args[2],
            [{"instance_name": b.CORE_INSTANCE_NAME, "rid_nid": "rid-core"}],
        )
        types = {
            r["resource_definition"]["resource_definition_name"]
            for r in submit_args.args[1]
        }
        self.assertIn("ingress-controller", types)

    def test_noop_when_not_bootstrapped(self):
        with mock.patch.object(b, "seed_base_resource_definitions"), mock.patch.object(
            b, "declare_core_produces"
        ), mock.patch.object(
            b, "find_core_deployment_node", return_value=[]
        ), mock.patch.object(
            b, "submit_local_resources"
        ) as submit:
            n = b.resync_local_resources("http://x", "neuronsphere")
        self.assertEqual(n, 0)
        submit.assert_not_called()


class ComputeNewBomEntriesTests(unittest.TestCase):
    def _search(self, instances, edges, deployments):
        """A _search_entities stand-in over (repo_instance, has_deployment, deployment)."""

        def fake_search(url, entity_type, filter_):
            if entity_type.endswith("repo_instance_has_repo_instance_deployment"):
                return edges
            if entity_type.endswith("repo_instance_deployment"):
                return deployments
            if entity_type.endswith("repo_instance"):
                return instances
            return []

        return fake_search

    def test_filters_deployed(self):
        bom = [
            {"repo_instance_name": "local-neuronsphere"},
            {"repo_instance_name": "vpc"},
            {"repo_instance_name": "clickhouse"},
        ]
        instances = [
            {"identifier": "i-1", "name": "local-neuronsphere"},
            {"identifier": "i-2", "name": "vpc"},
        ]
        edges = [
            {"ref_from": "i-1", "ref_to": "rid-1", "_created": "2026-01-01"},
            {"ref_from": "i-2", "ref_to": "rid-2", "_created": "2026-01-01"},
        ]
        deployments = [
            {"identifier": "rid-1", "status": "DEPLOYED"},
            {"identifier": "rid-2", "status": "DEPLOYED"},
        ]
        with mock.patch.object(
            b,
            "_search_entities",
            side_effect=self._search(instances, edges, deployments),
        ):
            new = b.compute_new_bom_entries("http://x", bom=bom)
        self.assertEqual(new, [{"repo_instance_name": "clickhouse"}])

    def test_failed_instance_is_still_new(self):
        # A RepoInstance can exist (from a prior attempt) without having ever
        # successfully deployed -- it must stay eligible for a delta-apply retry.
        bom = [{"repo_instance_name": "clickhouse-user"}]
        instances = [{"identifier": "i-1", "name": "clickhouse-user"}]
        edges = [{"ref_from": "i-1", "ref_to": "rid-1", "_created": "2026-01-01"}]
        deployments = [{"identifier": "rid-1", "status": "FAILED"}]
        with mock.patch.object(
            b,
            "_search_entities",
            side_effect=self._search(instances, edges, deployments),
        ):
            new = b.compute_new_bom_entries("http://x", bom=bom)
        self.assertEqual(new, bom)

    def test_skipped_instance_is_still_new(self):
        # SKIPPED (e.g. a dependency failed) is also not a successful deploy.
        bom = [{"repo_instance_name": "otel-collector"}]
        instances = [{"identifier": "i-1", "name": "otel-collector"}]
        edges = [{"ref_from": "i-1", "ref_to": "rid-1", "_created": "2026-01-01"}]
        deployments = [{"identifier": "rid-1", "status": "SKIPPED"}]
        with mock.patch.object(
            b,
            "_search_entities",
            side_effect=self._search(instances, edges, deployments),
        ):
            new = b.compute_new_bom_entries("http://x", bom=bom)
        self.assertEqual(new, bom)

    def test_all_new_when_env_empty(self):
        bom = [
            {"repo_instance_name": "local-neuronsphere"},
            {"repo_instance_name": "vpc"},
        ]
        with mock.patch.object(
            b, "_search_entities", side_effect=self._search([], [], [])
        ):
            new = b.compute_new_bom_entries("http://x", bom=bom)
        self.assertEqual(new, bom)

    def test_none_when_all_deployed(self):
        bom = [
            {"repo_instance_name": "local-neuronsphere"},
            {"repo_instance_name": "vpc"},
        ]
        instances = [
            {"identifier": "i-1", "name": "local-neuronsphere"},
            {"identifier": "i-2", "name": "vpc"},
        ]
        edges = [
            {"ref_from": "i-1", "ref_to": "rid-1", "_created": "2026-01-01"},
            {"ref_from": "i-2", "ref_to": "rid-2", "_created": "2026-01-01"},
        ]
        deployments = [
            {"identifier": "rid-1", "status": "DEPLOYED"},
            {"identifier": "rid-2", "status": "DEPLOYED"},
        ]
        with mock.patch.object(
            b,
            "_search_entities",
            side_effect=self._search(instances, edges, deployments),
        ):
            new = b.compute_new_bom_entries("http://x", bom=bom)
        self.assertEqual(new, [])

    def test_picks_most_recent_deployment_status(self):
        # Redeployed instance: an old FAILED attempt followed by a newer DEPLOYED one.
        bom = [{"repo_instance_name": "clickhouse"}]
        instances = [{"identifier": "i-1", "name": "clickhouse"}]
        edges = [
            {"ref_from": "i-1", "ref_to": "rid-old", "_created": "2026-01-01"},
            {"ref_from": "i-1", "ref_to": "rid-new", "_created": "2026-07-01"},
        ]
        deployments = [
            {"identifier": "rid-old", "status": "FAILED"},
            {"identifier": "rid-new", "status": "DEPLOYED"},
        ]
        with mock.patch.object(
            b,
            "_search_entities",
            side_effect=self._search(instances, edges, deployments),
        ):
            new = b.compute_new_bom_entries("http://x", bom=bom)
        self.assertEqual(new, [])

    def test_search_error_returns_empty(self):
        bom = [{"repo_instance_name": "clickhouse"}]
        with mock.patch.object(
            b, "_search_entities", side_effect=b.requests.RequestException("boom")
        ):
            new = b.compute_new_bom_entries("http://x", bom=bom)
        self.assertEqual(new, [])

    def test_defaults_to_resolve_bom(self):
        sentinel = [{"repo_instance_name": "sentinel"}]
        with mock.patch.object(
            b, "_resolve_bom", return_value=sentinel
        ) as resolve, mock.patch.object(
            b, "_search_entities", side_effect=self._search([], [], [])
        ):
            new = b.compute_new_bom_entries("http://x")
        resolve.assert_called_once()
        self.assertEqual(new, sentinel)

    def test_core_repo_class_instance_with_non_deployed_record_is_new(self):
        # LocalWorkflowRunner hardcodes CORE_REPO_CLASS as a no-op node (always
        # marked DEPLOYED without executing), so even if a prior fail-fast run
        # left it at some other status ("SKIPPED"), compute_new_bom_entries
        # doesn't need to special-case it -- treating it as retry-eligible like
        # any other non-DEPLOYED entry is harmless, since re-including it just
        # causes the runner to mark it DEPLOYED again on the next pass.
        bom = [
            {
                "repo_instance_name": "local-neuronsphere",
                "repo_class_name": "hmd-cli-neuronsphere",
            }
        ]
        instances = [{"identifier": "i-1", "name": "local-neuronsphere"}]
        edges = [{"ref_from": "i-1", "ref_to": "rid-1", "_created": "2026-01-01"}]
        deployments = [{"identifier": "rid-1", "status": "SKIPPED"}]
        with mock.patch.object(
            b,
            "_search_entities",
            side_effect=self._search(instances, edges, deployments),
        ):
            new = b.compute_new_bom_entries("http://x", bom=bom)
        self.assertEqual(new, bom)

    def test_core_repo_class_instance_with_no_record_is_new(self):
        bom = [
            {
                "repo_instance_name": "local-neuronsphere",
                "repo_class_name": "hmd-cli-neuronsphere",
            }
        ]
        with mock.patch.object(
            b, "_search_entities", side_effect=self._search([], [], [])
        ):
            new = b.compute_new_bom_entries("http://x", bom=bom)
        self.assertEqual(new, bom)

    def test_skipped_instance_with_repo_class_is_still_new(self):
        # A repo that never got reached (SKIPPED) must stay retry-eligible,
        # regardless of what repo_class_name it carries.
        bom = [
            {
                "repo_instance_name": "otel-collector",
                "repo_class_name": "hmd-inf-otel-collector",
            }
        ]
        instances = [{"identifier": "i-1", "name": "otel-collector"}]
        edges = [{"ref_from": "i-1", "ref_to": "rid-1", "_created": "2026-01-01"}]
        deployments = [{"identifier": "rid-1", "status": "SKIPPED"}]
        with mock.patch.object(
            b,
            "_search_entities",
            side_effect=self._search(instances, edges, deployments),
        ):
            new = b.compute_new_bom_entries("http://x", bom=bom)
        self.assertEqual(new, bom)


class SeedBomIdempotencyTests(unittest.TestCase):
    def _run_seed_bom(self, state, bom):
        """Drive seed_bom against a minimal fake in-memory ms-deployment.

        ``state`` accumulates deployment_set / change_set rows created via
        _put_entity, so a second call in the same test sees the first call's
        writes when it does its own find-first search.
        """

        def fake_search(url, entity_type, filter_):
            if entity_type.endswith("deployment_set"):
                return state["deployment_sets"]
            if entity_type.endswith("change_set"):
                return state["change_sets"]
            return []

        def fake_put(url, entity_type, body):
            # Record the whole body, definition included: seed_bom re-reads the
            # deployment set's definition to check it targets this environment,
            # so a fake that dropped it would make every restart look like a
            # stale row in need of repair.
            row = {"identifier": body.get("identifier", "nid-1"), **body}
            if entity_type.endswith("deployment_set"):
                state["deployment_sets"].append(row)
            if entity_type.endswith("change_set"):
                state["change_sets"].append(row)
            return row

        def fake_post(url, op, payload=None, **k):
            if op == "apply_changeset":
                state["apply_changeset_calls"].append(payload["change_set_name"])
                return {"csd_nid": f"csd-{len(state['apply_changeset_calls'])}"}
            if op == "generate_local_deployment":
                return {"nodes": []}
            if op.startswith("generate_local_deployment/"):
                return {"nodes": []}
            if op == "add_repo_class_version":
                return {}
            return {}

        with mock.patch.object(
            b, "_search_entities", side_effect=fake_search
        ), mock.patch.object(b, "_put_entity", side_effect=fake_put), mock.patch.object(
            b, "_post_apiop", side_effect=fake_post
        ), mock.patch.object(
            b,
            "resolve_repo_version",
            return_value=b.VersionResolution("0.1.0", "declared", None),
        ), mock.patch.object(
            b, "_get_repo_dependencies", return_value={}
        ), mock.patch.object(
            b, "_get_repo_deploy_config", return_value={}
        ), mock.patch.object(
            b, "declare_core_produces"
        ), mock.patch.object(
            b, "ensure_environment"
        ), mock.patch.object(
            b, "upsert_repo_resource_definitions"
        ):
            return b.seed_bom("http://x", bom=bom)

    def test_deployment_set_created_once(self):
        state = {
            "deployment_sets": [],
            "change_sets": [],
            "apply_changeset_calls": [],
        }
        bom = [
            {
                "repo_instance_name": "local-neuronsphere",
                "repo_class_name": "hmd-cli-neuronsphere",
            }
        ]
        self._run_seed_bom(state, bom)
        self._run_seed_bom(state, bom)
        self.assertEqual(len(state["deployment_sets"]), 1)

    def test_change_set_name_unique_per_call(self):
        state = {
            "deployment_sets": [],
            "change_sets": [],
            "apply_changeset_calls": [],
        }
        bom = [
            {
                "repo_instance_name": "local-neuronsphere",
                "repo_class_name": "hmd-cli-neuronsphere",
            }
        ]
        self._run_seed_bom(state, bom)
        self._run_seed_bom(state, bom)

        change_set_names = [c["name"] for c in state["change_sets"]]
        self.assertEqual(len(change_set_names), 2)
        self.assertEqual(len(set(change_set_names)), 2)
        self.assertEqual(change_set_names[0], "local-changeset")

        # Every apply_changeset call used the name just PUT in that same call.
        self.assertEqual(state["apply_changeset_calls"], change_set_names)


if __name__ == "__main__":
    unittest.main()


class RequiredRolesAreSuppliedTests(unittest.TestCase):
    """Every dependency a RepoClassVersion marks required must be in the BOM entry.

    ms-deployment validates required *roles* when the changeset is applied, not
    when the deploy runs -- so a missing one fails `up` with "required role, X,
    not provided" long before the deploy that would (or would not) have used it.
    A local overlay ignoring a dependency does not exempt the entry from
    declaring it.
    """

    def _manifest_dependencies(self, repo_class_name):
        repo_home = os.environ.get("HMD_REPO_HOME")
        if not repo_home:
            self.skipTest("HMD_REPO_HOME not set")
        path = Path(repo_home) / repo_class_name / "meta-data" / "manifest.json"
        if not path.is_file():
            self.skipTest(f"{repo_class_name} not checked out")
        return json.loads(path.read_text())["deploy"].get("dependencies", {})

    def test_the_environment_db_entry_supplies_every_required_role(self):
        deps = self._manifest_dependencies(b.ENV_DB_REPO_CLASS)
        required = {
            role
            for role, dep in deps.items()
            if str(dep.get("required", "")).lower() == "true"
        }
        entry = next(
            e for e in b.LOCAL_CORE_BOM if e["repo_instance_name"] == b.ENV_DB_INSTANCE
        )
        missing = required - set(entry.get("dependencies", {}))
        self.assertEqual(missing, set(), f"unsupplied required role(s): {missing}")

    def test_a_resource_typed_required_role_has_a_local_producer(self):
        """A resource-typed role is validated against what the supplied instance
        actually produces, so presence in the map is not enough."""
        deps = self._manifest_dependencies(b.ENV_DB_REPO_CLASS)
        produced = {
            (d["resource_namespace"], d["resource_definition_name"])
            for d in b.CORE_PRODUCED_DEFINITIONS
        }
        entry = next(
            e for e in b.LOCAL_CORE_BOM if e["repo_instance_name"] == b.ENV_DB_INSTANCE
        )
        for role, target in entry.get("dependencies", {}).items():
            resource = (deps.get(role) or {}).get("resource")
            if not resource or target != b.CORE_INSTANCE_NAME:
                continue
            key = (resource["resource_namespace"], resource["resource_definition_name"])
            self.assertIn(
                key,
                produced,
                f"role {role!r} needs {key[0]}/{key[1]}, which the core "
                f"RepoClass does not declare producing",
            )


class DatabaseInstanceRepointingTests(unittest.TestCase):
    """`database-instance` must resolve to the real Postgres producer.

    Installed plugin packages map the role to CORE_INSTANCE_NAME, which was
    correct while the core RepoClass stood in as the postgres producer. It is a
    real hmd-postgres-rds deploy now, so ms-deployment rejects the changeset:
    "supplied instance, local-neuronsphere, satisfies neither the required
    resource type ... nor a suggested repo_class."

    Normalised in the assembled BOM rather than fixed in each plugin, because
    plugins ship as independent packages -- an older installed one would still
    supply the core instance.
    """

    def test_a_core_instance_target_is_repointed(self):
        bom = [
            {
                "repo_instance_name": "hive-metastore-db-account",
                "dependencies": {"database-instance": b.CORE_INSTANCE_NAME},
            }
        ]
        b._repoint_database_instance(bom)
        self.assertEqual(bom[0]["dependencies"]["database-instance"], b.ENV_DB_INSTANCE)

    def test_an_explicit_target_is_left_alone(self):
        bom = [
            {
                "repo_instance_name": "x",
                "dependencies": {"database-instance": "some-other-db"},
            }
        ]
        b._repoint_database_instance(bom)
        self.assertEqual(bom[0]["dependencies"]["database-instance"], "some-other-db")

    def test_other_roles_are_untouched(self):
        bom = [
            {
                "repo_instance_name": "x",
                "dependencies": {"eks-cluster": b.CORE_INSTANCE_NAME},
            }
        ]
        b._repoint_database_instance(bom)
        self.assertEqual(bom[0]["dependencies"]["eks-cluster"], b.CORE_INSTANCE_NAME)

    def test_the_resolved_bom_never_points_the_role_at_the_core_instance(self):
        for entry in b._resolve_bom():
            target = (entry.get("dependencies") or {}).get("database-instance")
            if target is not None:
                self.assertNotEqual(
                    target,
                    b.CORE_INSTANCE_NAME,
                    f"{entry['repo_instance_name']} still points at the core instance",
                )
