"""Unit tests for the minimal-core plugin defaults.

`hmd neuronsphere up` starts only a minimal core (network + Floci + databases +
k3s + the deployment control plane + graph). Every other app/infra plugin is
opt-in (off by default) and enabled per-user via
``HMD_LOCAL_NEURONSPHERE_ENABLE_<NAME>`` or ``hmd neuronsphere configure``.

These tests lock in that contract:

* each app/infra plugin's ``enabled()`` is off unless its env flag is set;
* ``LocalPluginLoader.is_plugin_enabled`` short-circuits the CORE plugins on and
  defaults every other (unflagged) plugin off.

Run directly (``python -m pytest src/python/tests/test_plugin_defaults.py``) or
via unittest — no service or Docker required.
"""

import importlib
import os
import unittest
from unittest import mock

from hmd_cli_neuronsphere.loaders.local_plugin_loader import (
    CORE_PLUGINS,
    LocalPluginLoader,
)

# The app/infra plugins that must be opt-in (off by default).
# Note: clickhouse and telemetry have been moved to hmd-cli-plugin-ns-telemetry
# and are no longer part of the core plugins.
OPT_IN_PLUGINS = [
    "airflow",
    "apache_superset",
    "argo",
    "hive_metastore",
    "jupyter",
    "transform",
    "trino",
]


def _env_var(plugin: str) -> str:
    return f"HMD_LOCAL_NEURONSPHERE_ENABLE_{plugin.upper()}"


class OptInPluginDefaults(unittest.TestCase):
    def _mod(self, plugin: str):
        return importlib.import_module(f"hmd_cli_neuronsphere.plugins.{plugin}")

    def test_off_by_default(self):
        # With no env flag and no nsplugin.json override, every app/infra
        # plugin is disabled.
        for plugin in OPT_IN_PLUGINS:
            mod = self._mod(plugin)
            with mock.patch.object(mod, "load_nsplugin_config", return_value=None):
                with mock.patch.dict(os.environ, {}, clear=False):
                    os.environ.pop(_env_var(plugin), None)
                    self.assertFalse(
                        mod.enabled(),
                        f"{plugin}.enabled() should default to False (opt-in)",
                    )

    def test_env_flag_enables(self):
        for plugin in OPT_IN_PLUGINS:
            mod = self._mod(plugin)
            with mock.patch.object(mod, "load_nsplugin_config", return_value=None):
                with mock.patch.dict(
                    os.environ, {_env_var(plugin): "true"}, clear=False
                ):
                    self.assertTrue(
                        mod.enabled(),
                        f"{plugin}.enabled() should be True when its flag is set",
                    )

    def test_env_flag_explicit_off(self):
        for plugin in OPT_IN_PLUGINS:
            mod = self._mod(plugin)
            with mock.patch.object(mod, "load_nsplugin_config", return_value=None):
                with mock.patch.dict(
                    os.environ, {_env_var(plugin): "false"}, clear=False
                ):
                    self.assertFalse(mod.enabled())


class CoreShortCircuit(unittest.TestCase):
    def test_core_plugins_always_enabled(self):
        loader = LocalPluginLoader()
        self.assertEqual(CORE_PLUGINS, {"floci", "main", "graph"})
        for plugin in CORE_PLUGINS:
            self.assertTrue(
                loader.is_plugin_enabled(plugin),
                f"core plugin {plugin} must always be enabled",
            )

    def test_unflagged_plugin_disabled(self):
        loader = LocalPluginLoader()
        with mock.patch.object(
            loader, "load_raw_plugin_config", return_value=None
        ), mock.patch.object(loader, "_is_explicitly_listed", return_value=False):
            with mock.patch.dict(os.environ, {}, clear=False):
                os.environ.pop(_env_var("trino"), None)
                self.assertFalse(loader.is_plugin_enabled("trino"))

    def test_env_flag_enables_via_loader(self):
        loader = LocalPluginLoader()
        with mock.patch.object(
            loader, "load_raw_plugin_config", return_value=None
        ), mock.patch.object(loader, "_is_explicitly_listed", return_value=False):
            with mock.patch.dict(os.environ, {_env_var("trino"): "true"}, clear=False):
                self.assertTrue(loader.is_plugin_enabled("trino"))


if __name__ == "__main__":
    unittest.main()
