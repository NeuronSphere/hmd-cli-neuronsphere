"""The graph is provisioned only when something asks for one.

It used to run unconditionally as a compose container in every environment --
a JVM per environment that most local work never touches. It is now a Floci
Neptune cluster, added to the BOM only when an entry declares a dependency on
`database.neuronsphere.io/graph-database`.

Two things have to move together for that to work. The core RepoClass must stop
declaring itself a producer of `graph-database` (otherwise a consumer resolves
to a record describing a container nobody deployed), and the consumers' role
mappings -- which installed plugin packages pin to `CORE_INSTANCE_NAME` -- have
to be repointed at the real producer. That is the same normalisation
`_repoint_database_instance` performs for Postgres, and for the same reason:
plugins ship independently, so fixing it in each would require a coordinated
release and would still break for an older installed one.
"""

import types
import unittest
from unittest import mock

from hmd_cli_neuronsphere import bom_seeder as b


def _env(slug="local"):
    return types.SimpleNamespace(
        slug=slug,
        account_id="000000000001",
        deployment_id=slug,
        graph_container=f"global-graph-{slug}",
        legacy_layout=False,
    )


CONSUMER = {
    "repo_instance_name": "trino",
    "repo_class_name": "hmd-inf-trino",
    "dependencies": {"graph-db": b.CORE_INSTANCE_NAME},
}
NO_CONSUMER = {
    "repo_instance_name": "superset",
    "repo_class_name": "hmd-inf-superset",
    "dependencies": {},
}


class DetectingDemand(unittest.TestCase):
    def test_a_graph_dependency_is_demand(self):
        self.assertTrue(b.bom_requires_graph([CONSUMER]))

    def test_transforms_neptune_db_role_counts_too(self):
        entry = {"repo_instance_name": "t", "dependencies": {"neptune-db": "x"}}
        self.assertTrue(b.bom_requires_graph([entry]))

    def test_no_graph_dependency_is_no_demand(self):
        self.assertFalse(b.bom_requires_graph([NO_CONSUMER]))

    def test_an_empty_bom_needs_no_graph(self):
        self.assertFalse(b.bom_requires_graph([]))


class TheCoreNoLongerClaimsToProduceIt(unittest.TestCase):
    def test_graph_database_is_not_a_core_produced_type(self):
        produced = {d["resource_definition_name"] for d in b.CORE_PRODUCED_DEFINITIONS}
        self.assertNotIn("graph-database", produced)

    def test_no_graph_resource_is_hand_seeded(self):
        seeded = {
            r["resource_definition"]["resource_definition_name"]
            for r in b.build_local_core_resources(env=_env())
        }
        self.assertNotIn("graph-database", seeded)


class ConsumersArePointedAtTheRealProducer(unittest.TestCase):
    def test_the_role_is_repointed(self):
        bom = [dict(CONSUMER, dependencies=dict(CONSUMER["dependencies"]))]
        b._repoint_graph_database(bom)
        self.assertEqual(bom[0]["dependencies"]["graph-db"], b.GRAPH_INSTANCE)

    def test_an_explicit_instance_is_left_alone(self):
        """Only the core stand-in is normalised; a deliberate choice stays."""
        bom = [{"repo_instance_name": "t", "dependencies": {"graph-db": "my-graph"}}]
        b._repoint_graph_database(bom)
        self.assertEqual(bom[0]["dependencies"]["graph-db"], "my-graph")


class TheEntryBypassesFlocisProxy(unittest.TestCase):
    def test_host_is_the_environments_alias(self):
        config = b.graph_bom_entry(_env("dev2"))["instance_configuration"]
        self.assertEqual(config["graph_host"], "global-graph-dev2")

    def test_port_is_gremlin_not_a_proxy_port(self):
        """Floci's Gremlin proxy is not restored after a Floci restart -- the
        cluster still reports available while every connection is reset."""
        self.assertEqual(
            b.graph_bom_entry(_env())["instance_configuration"]["graph_port"], 8182
        )

    def test_it_is_deployed_by_the_real_repo_class(self):
        self.assertEqual(
            b.graph_bom_entry(_env())["repo_class_name"], "hmd-inf-neptune"
        )


class TheOverrideStillWins(unittest.TestCase):
    def test_disabled_means_no_graph_even_with_a_consumer(self):
        with mock.patch.dict(
            "os.environ", {"HMD_LOCAL_NEURONSPHERE_ENABLE_GRAPH": "false"}
        ):
            self.assertFalse(b.graph_enabled())

    def test_unset_means_lazy_not_off(self):
        with mock.patch.dict("os.environ", {}, clear=True):
            self.assertTrue(b.graph_enabled())


if __name__ == "__main__":
    unittest.main()
