"""``SERVICE_CONFIG``'s gremlin ``db_host`` must name a container that exists.

``LocalPluginLoader.get_hmdms_lambda_spec`` resolves a manifest's
``dependency:neptune-db`` gremlin config to a hardcoded ``"global-graph"`` --
it builds the spec from the plugin's manifest alone, with no ``env`` to know
whether the deploy is the control plane's own Lambda (whose graph really is
aliased bare ``global-graph``) or one running inside a named environment
(aliased ``global-graph-<slug>`` instead). ``_resolve_graph_host`` is the
correction applied afterwards, in ``_deploy_hmdms_service_lambdas``, once the
caller knows which of those this deploy actually is.

Run directly: ``python -m pytest src/python/tests/test_graph_host_resolution.py``
"""

import json
import unittest

from hmd_cli_neuronsphere.hmd_cli_neuronsphere import _resolve_graph_host


def _service_config(**hmd_db_engines):
    return json.dumps({"hmd_db_engines": hmd_db_engines})


class ResolveGraphHostTests(unittest.TestCase):
    def test_rewrites_a_gremlin_engines_host(self):
        env_vars = {
            "SERVICE_CONFIG": _service_config(
                graph={
                    "engine_type": "gremlin",
                    "engine_config": {"db_host": "global-graph"},
                }
            )
        }
        _resolve_graph_host(env_vars, "global-graph-dev2")
        config = json.loads(env_vars["SERVICE_CONFIG"])
        self.assertEqual(
            config["hmd_db_engines"]["graph"]["engine_config"]["db_host"],
            "global-graph-dev2",
        )

    def test_control_plane_host_is_a_no_op_when_already_correct(self):
        # The loader's own hardcoded default already matches the control
        # plane's real alias -- nothing should change.
        env_vars = {
            "SERVICE_CONFIG": _service_config(
                graph={
                    "engine_type": "gremlin",
                    "engine_config": {"db_host": "global-graph"},
                }
            )
        }
        before = env_vars["SERVICE_CONFIG"]
        _resolve_graph_host(env_vars, "global-graph")
        self.assertEqual(env_vars["SERVICE_CONFIG"], before)

    def test_a_plugin_with_no_gremlin_engine_is_untouched(self):
        env_vars = {
            "SERVICE_CONFIG": _service_config(
                postgres={"engine_type": "postgres", "engine_config": {}}
            )
        }
        before = env_vars["SERVICE_CONFIG"]
        _resolve_graph_host(env_vars, "global-graph-dev2")
        self.assertEqual(env_vars["SERVICE_CONFIG"], before)

    def test_no_service_config_at_all_does_not_raise(self):
        env_vars = {}
        _resolve_graph_host(env_vars, "global-graph-dev2")
        self.assertEqual(env_vars, {})

    def test_malformed_service_config_does_not_raise(self):
        env_vars = {"SERVICE_CONFIG": "not json"}
        _resolve_graph_host(env_vars, "global-graph-dev2")
        self.assertEqual(env_vars["SERVICE_CONFIG"], "not json")

    def test_a_missing_engine_config_is_created(self):
        env_vars = {"SERVICE_CONFIG": _service_config(graph={"engine_type": "gremlin"})}
        _resolve_graph_host(env_vars, "global-graph-dev2")
        config = json.loads(env_vars["SERVICE_CONFIG"])
        self.assertEqual(
            config["hmd_db_engines"]["graph"]["engine_config"]["db_host"],
            "global-graph-dev2",
        )


if __name__ == "__main__":
    unittest.main()
