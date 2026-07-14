"""Unit tests for the local BOM seeder (bom_seeder).

Cover the parts that need no running service or Docker:

* ``_resolve_bom`` always prepends the ``local-k3s`` core entry, appends the
  ext-secrets add-ons only when opted in, and de-dupes by instance name;
* ``build_local_core_resources`` stamps every core Resource with the single
  ``hmd-cli-neuronsphere`` RepoClass / ``local-k3s`` instance;
* ``submit_local_resources`` submits against the ``local-k3s`` node's rid;
* ``declare_core_produces`` declares the three core produced types;
* ``upsert_repo_resource_definitions`` upserts a repo's declared RDs.

Run directly (``python -m pytest src/python/tests/test_bom_seeder.py``) — no
service or Docker required.
"""

import os
import unittest
from unittest import mock

from hmd_cli_neuronsphere import bom_seeder as b


class ResolveBomTests(unittest.TestCase):
    def setUp(self):
        # A clean env: no BOM file, ext-secrets off.
        self._env = mock.patch.dict(
            os.environ,
            {
                k: v
                for k, v in os.environ.items()
                if k
                not in {
                    "HMD_LOCAL_BOM_FILE",
                    "HMD_LOCAL_NEURONSPHERE_ENABLE_EXT_SECRETS",
                }
            },
            clear=True,
        )
        self._env.start()

    def tearDown(self):
        self._env.stop()

    def test_core_entry_is_prepended(self):
        names = [e["repo_instance_name"] for e in b._resolve_bom()]
        self.assertEqual(names[0], b.CORE_INSTANCE_NAME)
        entry = b._resolve_bom()[0]
        self.assertEqual(entry["repo_class_name"], b.CORE_REPO_CLASS)

    def test_ext_secrets_off_by_default(self):
        names = [e["repo_instance_name"] for e in b._resolve_bom()]
        self.assertNotIn("ext-secrets", names)
        self.assertNotIn("ext-secrets-crds", names)

    def test_ext_secrets_opt_in_appends_crds_first(self):
        os.environ["HMD_LOCAL_NEURONSPHERE_ENABLE_EXT_SECRETS"] = "true"
        names = [e["repo_instance_name"] for e in b._resolve_bom()]
        self.assertEqual(names[0], b.CORE_INSTANCE_NAME)
        self.assertIn("ext-secrets-crds", names)
        self.assertLess(names.index("ext-secrets-crds"), names.index("ext-secrets"))

    def test_ext_secrets_dep_names_local_producer(self):
        os.environ["HMD_LOCAL_NEURONSPHERE_ENABLE_EXT_SECRETS"] = "1"
        bom = {e["repo_instance_name"]: e for e in b._resolve_bom()}
        deps = bom["ext-secrets"]["dependencies"]
        self.assertEqual(deps["eks-cluster"], b.CORE_INSTANCE_NAME)
        self.assertEqual(deps["compute"], b.CORE_INSTANCE_NAME)
        self.assertEqual(deps["crds"], "ext-secrets-crds")

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


class CoreResourceTests(unittest.TestCase):
    def test_all_core_resources_owned_by_cli_repo_class(self):
        res = b.build_local_core_resources(cluster_name="ns-local")
        self.assertTrue(res)
        for r in res:
            self.assertEqual(r["repo_class_name"], b.CORE_REPO_CLASS)
            self.assertEqual(r["instance_name"], b.CORE_INSTANCE_NAME)
        types = {r["resource_definition"]["resource_definition_name"] for r in res}
        self.assertEqual(
            types, {"docker-network", "kubernetes-cluster", "compute-node"}
        )

    def test_network_only_without_cluster(self):
        res = b.build_local_core_resources(cluster_name=None)
        self.assertEqual(len(res), 1)
        self.assertEqual(
            res[0]["resource_definition"]["resource_definition_name"], "docker-network"
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
    def test_declares_three_core_types(self):
        def fake_post(url, op, payload=None, **k):
            if op.startswith("find_repo_class_versions/"):
                return [{"identifier": "rcv-core", "version": "0.5"}]
            return {}

        with mock.patch.object(b, "_post_apiop", side_effect=fake_post) as post:
            n = b.declare_core_produces("http://x")
        self.assertEqual(n, 3)
        declared = [
            c.args[2]["resource_definition"]["resource_definition_name"]
            for c in post.mock_calls
            if c.args[1] == "declare_produces_resource_definition"
        ]
        self.assertEqual(
            set(declared),
            {"kubernetes-cluster", "compute-node", "docker-network"},
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


if __name__ == "__main__":
    unittest.main()
