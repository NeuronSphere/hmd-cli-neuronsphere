"""The reconcile plan and the destroy guard.

``up --prune`` is the first thing in this CLI that destroys a deployed instance,
so the plan that drives it has to be wrong in only one direction: it may under-
report a removal, never over-report one. The cases pinned here are the ones
where over-reporting would cost a developer a running environment:

1. Core instances (``local-neuronsphere``, ``local-databases``) are never
   proposed for removal -- they are created by the bootstrap, never declared,
   and a naive "deployed but not desired" rule would list them first.
2. A graph the CLI could not read yields a ``degraded`` plan with nothing to do,
   not a plan where everything looks removed.
3. A destroy that would cascade into a still-declared instance is refused
   outright rather than partially applied -- ms-deployment destroys everything
   *downstream* of what it is given, which is easy to forget when the only thing
   you removed from the manifest was one leaf.

Drift detection deliberately treats a missing snapshot as "no information", so
an environment bootstrapped before snapshots existed does not propose redeploying
everything it already has.

The Helm release cross-check is the one place the plan looks past the graph at
the cluster itself, and it is fail-safe in both directions: an entry that
recorded no release is never flagged, and a cluster that could not be read is
not mistaken for an empty one.

Run directly: ``python -m pytest src/python/tests/test_env_reconcile.py``
"""

import json
import os
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import requests

from hmd_cli_neuronsphere import bom_seeder as b
from hmd_cli_neuronsphere import change_set_builder as csb
from hmd_cli_neuronsphere import env_reconcile as er


class _Env:
    def __init__(self, slug="dev2", state_dir=None):
        self.slug = slug
        self.name = slug
        self.deployment_id = slug
        self.account_id = "000000000002"
        self.core_instance_name = "local-neuronsphere"
        self.legacy_layout = False
        self._state_dir = state_dir

    @property
    def state_path(self):
        return Path(self._state_dir)

    @property
    def is_default(self):
        return self.slug == "local"


def _entry(name, repo_class="hmd-ms-myapi", version="1.0.0", config=None):
    return {
        "deployment_id": "dev2",
        "repo_instance_name": name,
        "repo_class_name": repo_class,
        "repo_class_version": version,
        "instance_configuration": config or {},
        "dependencies": {},
    }


class _ReconcileTest(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.env = _Env(state_dir=self._tmp.name)

    def tearDown(self):
        self._tmp.cleanup()

    def _plan(self, desired, statuses):
        """Compute a plan against a stubbed desired state and graph."""
        with mock.patch.object(
            csb, "full_definition", return_value=desired
        ), mock.patch.object(b, "_repo_instance_status", return_value=statuses):
            return er.compute_plan("http://x", env=self.env)


class PlanTests(_ReconcileTest):
    def test_undeployed_entry_is_an_addition(self):
        plan = self._plan([_entry("my-api")], {})
        self.assertEqual([e["repo_instance_name"] for e in plan.add], ["my-api"])
        self.assertEqual(plan.remove, [])

    def test_failed_entry_is_retried_as_an_addition(self):
        plan = self._plan([_entry("my-api")], {"my-api": "FAILED"})
        self.assertEqual([e["repo_instance_name"] for e in plan.add], ["my-api"])

    def test_deployed_entry_without_a_snapshot_is_unchanged(self):
        # No snapshot means no drift information -- never a mass redeploy.
        plan = self._plan([_entry("my-api")], {"my-api": "DEPLOYED"})
        self.assertEqual(plan.unchanged, ["my-api"])
        self.assertEqual(plan.add, [])
        self.assertEqual(plan.change, [])

    def test_deployed_entry_matching_its_snapshot_is_unchanged(self):
        entry = _entry("my-api")
        er.write_snapshot(self.env, [entry])
        plan = self._plan([entry], {"my-api": "DEPLOYED"})
        self.assertEqual(plan.unchanged, ["my-api"])

    def test_configuration_drift_is_a_change(self):
        er.write_snapshot(self.env, [_entry("my-api", config={"replicas": 1})])
        plan = self._plan(
            [_entry("my-api", config={"replicas": 2})], {"my-api": "DEPLOYED"}
        )
        self.assertEqual([e["repo_instance_name"] for e in plan.change], ["my-api"])
        self.assertEqual(plan.add, [])

    def test_version_bump_is_a_change(self):
        er.write_snapshot(self.env, [_entry("my-api", version="1.0.0")])
        plan = self._plan([_entry("my-api", version="1.1.0")], {"my-api": "DEPLOYED"})
        self.assertEqual([e["repo_instance_name"] for e in plan.change], ["my-api"])

    def test_deployed_but_undeclared_is_a_removal(self):
        plan = self._plan(
            [_entry("my-api")], {"my-api": "DEPLOYED", "gone": "DEPLOYED"}
        )
        self.assertEqual(plan.remove, ["gone"])

    def test_undeclared_but_not_deployed_is_not_a_removal(self):
        # Nothing to tear down for an instance that never finished deploying.
        plan = self._plan([_entry("my-api")], {"my-api": "DEPLOYED", "gone": "FAILED"})
        self.assertEqual(plan.remove, [])

    def test_core_instances_are_never_removed(self):
        statuses = {
            "local-neuronsphere": "DEPLOYED",
            "local-databases": "DEPLOYED",
            "my-api": "DEPLOYED",
        }
        plan = self._plan([_entry("my-api")], statuses)
        self.assertEqual(plan.remove, [])

    def test_an_unreadable_graph_is_degraded_not_empty(self):
        with mock.patch.object(
            csb, "full_definition", return_value=[_entry("my-api")]
        ), mock.patch.object(
            b,
            "_repo_instance_status",
            side_effect=requests.RequestException("boom"),
        ):
            plan = er.compute_plan("http://x", env=self.env)
        self.assertTrue(plan.degraded)
        self.assertEqual(plan.remove, [])
        self.assertEqual(plan.add, [])

    def test_summary_and_render(self):
        er.write_snapshot(self.env, [_entry("changed", config={"a": 1})])
        plan = self._plan(
            [_entry("added"), _entry("changed", config={"a": 2})],
            {"changed": "DEPLOYED", "gone": "DEPLOYED"},
        )
        self.assertIn("+1 add", plan.summary())
        self.assertIn("~1 change", plan.summary())
        self.assertIn("-1 remove", plan.summary())
        rendered = "\n".join(plan.render())
        self.assertIn("- gone", rendered)
        self.assertIn("+ added", rendered)
        self.assertIn("~ changed", rendered)


class HelmReleaseCrossCheckTests(_ReconcileTest):
    """A DEPLOYED instance whose Helm release is gone must be redeployed.

    ms-deployment records intent; it cannot see the cluster. Without this check
    a release someone uninstalled -- or one lost with a replaced cluster --
    leaves the graph reporting DEPLOYED forever and `up` reporting "matches its
    declared state" over a missing workload.
    """

    def _plan_with_releases(self, desired, statuses, live):
        with mock.patch.object(
            csb, "full_definition", return_value=desired
        ), mock.patch.object(
            b, "_repo_instance_status", return_value=statuses
        ), mock.patch(
            "hmd_cli_neuronsphere.k3s_operators.live_helm_releases", return_value=live
        ):
            return er.compute_plan("http://x", env=self.env)

    def test_missing_release_makes_a_deployed_entry_an_addition(self):
        entry = _entry("redis")
        er.write_snapshot(self.env, [entry], releases={"redis": "redis-dev2"})
        plan = self._plan_with_releases([entry], {"redis": "DEPLOYED"}, set())
        self.assertEqual([e["repo_instance_name"] for e in plan.add], ["redis"])
        self.assertEqual(plan.unchanged, [])

    def test_present_release_stays_unchanged(self):
        entry = _entry("redis")
        er.write_snapshot(self.env, [entry], releases={"redis": "redis-dev2"})
        plan = self._plan_with_releases(
            [entry], {"redis": "DEPLOYED"}, {"redis-dev2", "argo-dev2"}
        )
        self.assertEqual(plan.unchanged, ["redis"])
        self.assertEqual(plan.add, [])

    def test_entry_with_no_recorded_release_is_never_flagged(self):
        # S3 buckets, cdktf-only repos and the skip-strategy core instance
        # install no Helm release; an empty cluster must not implicate them.
        entry = _entry("project-bucket")
        er.write_snapshot(self.env, [entry])
        plan = self._plan_with_releases([entry], {"project-bucket": "DEPLOYED"}, set())
        self.assertEqual(plan.unchanged, ["project-bucket"])
        self.assertEqual(plan.add, [])

    def test_an_unreadable_cluster_is_not_an_empty_one(self):
        entry = _entry("redis")
        er.write_snapshot(self.env, [entry], releases={"redis": "redis-dev2"})
        plan = self._plan_with_releases([entry], {"redis": "DEPLOYED"}, None)
        self.assertEqual(plan.unchanged, ["redis"])
        self.assertEqual(plan.add, [])

    def test_the_cluster_is_not_queried_when_nothing_recorded_a_release(self):
        entry = _entry("my-api")
        er.write_snapshot(self.env, [entry])
        with mock.patch.object(
            csb, "full_definition", return_value=[entry]
        ), mock.patch.object(
            b, "_repo_instance_status", return_value={"my-api": "DEPLOYED"}
        ), mock.patch(
            "hmd_cli_neuronsphere.k3s_operators.live_helm_releases"
        ) as live:
            er.compute_plan("http://x", env=self.env)
        live.assert_not_called()

    def test_merge_preserves_a_release_for_entries_not_reapplied(self):
        first, second = _entry("redis"), _entry("argo")
        er.write_snapshot(self.env, [first, second], releases={"redis": "redis-dev2"})
        # Only argo redeploys; redis keeps its recorded release.
        er.merge_snapshot(
            self.env, [first, second], [second], releases={"argo": "argo-dev2"}
        )
        self.assertEqual(
            er.load_release_map(self.env),
            {"redis": "redis-dev2", "argo": "argo-dev2"},
        )


class SnapshotTests(_ReconcileTest):
    def test_round_trip(self):
        entry = _entry("my-api")
        er.write_snapshot(self.env, [entry])
        self.assertEqual(er.load_snapshot(self.env), {"my-api": csb.entry_hash(entry)})

    def test_a_corrupt_snapshot_reads_as_no_information(self):
        (Path(self._tmp.name) / er.SNAPSHOT_FILENAME).write_text("{not json")
        self.assertEqual(er.load_snapshot(self.env), {})

    def test_merge_records_only_what_was_applied(self):
        definition = [_entry("a"), _entry("b")]
        er.merge_snapshot(self.env, definition, [definition[0]])
        # 'b' never deployed, so it stays absent and remains eligible for retry.
        self.assertEqual(list(er.load_snapshot(self.env)), ["a"])

    def test_merge_keeps_previously_recorded_entries(self):
        a, bb = _entry("a"), _entry("b")
        er.write_snapshot(self.env, [a, bb])
        changed_a = _entry("a", config={"x": 1})
        er.merge_snapshot(self.env, [changed_a, bb], [changed_a])
        snapshot = er.load_snapshot(self.env)
        self.assertEqual(snapshot["a"], csb.entry_hash(changed_a))
        self.assertEqual(snapshot["b"], csb.entry_hash(bb))

    def test_merge_with_nothing_applied_leaves_the_snapshot_alone(self):
        a = _entry("a")
        er.write_snapshot(self.env, [a])
        er.merge_snapshot(self.env, [a], [])
        self.assertEqual(list(er.load_snapshot(self.env)), ["a"])


class DestroyGuardTests(unittest.TestCase):
    """`destroy_from` is a starting point: everything downstream goes too."""

    def setUp(self):
        self.env = _Env()

    def _apiop(self, closure, calls=None):
        def _post(base_url, operation, payload=None, tolerate_exists=False):
            if calls is not None:
                calls.append((operation, payload))
            if operation == "destroy_deploymentset":
                if payload.get("dry_run"):
                    return {"dev2": list(closure)}
                return {"csd_nid": "csd-1"}
            if operation.startswith("generate_local_deployment/"):
                return {
                    "destroy": True,
                    "nodes": [
                        {
                            "instance_name": n,
                            "repo_class_name": "hmd-ms-myapi",
                            "rid_nid": f"rid-{n}",
                            "script": "hmd deploy --destroy",
                        }
                        for n in closure
                    ],
                }
            return {}

        return _post

    def test_cascade_into_a_declared_instance_is_refused(self):
        calls = []
        with mock.patch.object(
            b, "_post_apiop", side_effect=self._apiop(["gone", "still-wanted"], calls)
        ):
            with self.assertRaises(b.DestroyCascadeError) as ctx:
                b.destroy_instances(
                    "http://x",
                    env=self.env,
                    instance_names=["gone"],
                    keep={"still-wanted"},
                )
        self.assertIn("still-wanted", str(ctx.exception))
        # Refusal must happen on the dry run: nothing may have been destroyed.
        self.assertEqual([op for op, _ in calls], ["destroy_deploymentset"])
        self.assertTrue(calls[0][1]["dry_run"])

    def test_a_clean_closure_proceeds(self):
        calls = []
        with mock.patch.object(
            b, "_post_apiop", side_effect=self._apiop(["gone"], calls)
        ):
            csd_nid, nodes = b.destroy_instances(
                "http://x", env=self.env, instance_names=["gone"], keep={"my-api"}
            )
        self.assertEqual(csd_nid, "csd-1")
        self.assertEqual([n["instance_name"] for n in nodes], ["gone"])
        real = [
            p for op, p in calls if op == "destroy_deploymentset" and not p["dry_run"]
        ]
        self.assertEqual(len(real), 1)
        self.assertTrue(real[0]["skip_async"])
        self.assertEqual(real[0]["deployment_set_name"], "dev2")

    def test_nothing_to_destroy_is_a_no_op(self):
        with mock.patch.object(b, "_post_apiop", side_effect=self._apiop([])):
            self.assertEqual(
                b.destroy_instances("http://x", env=self.env, instance_names=["gone"]),
                (None, []),
            )

    def test_no_instance_names_never_calls_the_service(self):
        with mock.patch.object(b, "_post_apiop") as post:
            self.assertEqual(
                b.destroy_instances("http://x", env=self.env, instance_names=[]),
                (None, []),
            )
        post.assert_not_called()

    def test_dry_run_reads_the_environments_own_key(self):
        # The dry run answers per Environment.type, which for a local
        # environment is its slug -- not the literal "local".
        def _post(base_url, operation, payload=None, tolerate_exists=False):
            return {"dev2": ["a"], "local": ["should-not-be-used"]}

        with mock.patch.object(b, "_post_apiop", side_effect=_post):
            self.assertEqual(b.plan_destroy("http://x", self.env, ["a"]), ["a"])


class DeploymentSetTargetingTests(unittest.TestCase):
    """A deployment set must name the environment it belongs to.

    Rows written before environments were typed by name all say
    ``environment: "local"``, and they outlive ``env delete``/``env create``
    because the deployment graph lives in the shared control-plane Postgres.
    Left alone, a recreated environment would apply its changesets -- and its
    prunes -- to the default environment's graph.
    """

    def _row(self, name, environments, identifier="ds-1"):
        import base64

        definition = [
            {
                "environment": e,
                "deployment_gate": {"transforms": [], "approval": False},
            }
            for e in environments
        ]
        return {
            "identifier": identifier,
            "name": name,
            "definition": base64.b64encode(json.dumps(definition).encode()).decode(),
        }

    def test_creates_when_absent(self):
        puts = []
        with mock.patch.object(
            b, "_search_entities", return_value=[]
        ), mock.patch.object(
            b, "_put_entity", side_effect=lambda u, t, d: puts.append(d) or d
        ):
            b.ensure_deployment_set("http://x", "dev2", "dev2")
        self.assertEqual(len(puts), 1)
        self.assertEqual(
            b._decode_collection(puts[0]["definition"])[0]["environment"], "dev2"
        )
        self.assertNotIn("identifier", puts[0])

    def test_correct_row_is_left_alone(self):
        row = self._row("dev2", ["dev2"])
        with mock.patch.object(
            b, "_search_entities", return_value=[row]
        ), mock.patch.object(b, "_put_entity") as put:
            self.assertEqual(b.ensure_deployment_set("http://x", "dev2", "dev2"), row)
        put.assert_not_called()

    def test_stale_local_row_is_repaired(self):
        stale = self._row("dev2", ["local"])
        fixed = self._row("dev2", ["dev2"])
        searches = [[stale], [fixed]]
        with mock.patch.object(
            b, "_search_entities", side_effect=lambda *a, **k: searches.pop(0)
        ), mock.patch.object(b, "_put_entity", return_value=fixed) as put:
            result = b.ensure_deployment_set("http://x", "dev2", "dev2")
        self.assertEqual(result, fixed)
        payload = put.call_args[0][2]
        # The identifier must be present, or the CRUD layer creates a duplicate
        # row rather than updating the stale one.
        self.assertEqual(payload["identifier"], "ds-1")

    def test_an_unrepairable_row_raises_instead_of_proceeding(self):
        stale = self._row("dev2", ["local"])
        with mock.patch.object(
            b, "_search_entities", return_value=[stale]
        ), mock.patch.object(b, "_put_entity", return_value=stale):
            with self.assertRaises(RuntimeError) as ctx:
                b.ensure_deployment_set("http://x", "dev2", "dev2")
        self.assertIn("down --purge", str(ctx.exception))

    def test_a_duplicate_after_repair_raises(self):
        stale = self._row("dev2", ["local"])
        fixed = self._row("dev2", ["dev2"], identifier="ds-2")
        searches = [[stale], [stale, fixed]]
        with mock.patch.object(
            b, "_search_entities", side_effect=lambda *a, **k: searches.pop(0)
        ), mock.patch.object(b, "_put_entity", return_value=fixed):
            with self.assertRaises(RuntimeError):
                b.ensure_deployment_set("http://x", "dev2", "dev2")


if __name__ == "__main__":
    unittest.main()
