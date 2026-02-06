"""
Loader for local filesystem plugins from HMD_REPO_HOME.

This module provides:
- Discovery of plugins in HMD_REPO_HOME with src/local/nsplugin.json
- Explicit enabling via environment variables
- Local plugin priority over installed plugins
"""

import json
import os
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Dict, List, Optional


@dataclass
class LocalPluginInfo:
    """Information about a discovered local plugin."""

    plugin_name: str
    repo_name: str
    repo_path: Path
    local_dir: Path
    config_path: Path


class LocalPluginLoader:
    """Loader for local filesystem plugins from HMD_REPO_HOME."""

    def __init__(self, repo_home: Optional[Path] = None) -> None:
        """
        Initialize the loader.

        Args:
            repo_home: Path to HMD_REPO_HOME. If None, reads from environment.
        """
        repo_home_str = os.environ.get("HMD_REPO_HOME", "")
        self.repo_home = repo_home or (Path(repo_home_str) if repo_home_str else None)
        self._discovered_plugins: Optional[Dict[str, LocalPluginInfo]] = None

    def discover_plugins(self) -> Dict[str, LocalPluginInfo]:
        """
        Scan HMD_REPO_HOME for repos with src/local/nsplugin.json.

        Returns:
            Dictionary mapping plugin_name to LocalPluginInfo
        """
        if self._discovered_plugins is not None:
            return self._discovered_plugins

        self._discovered_plugins = {}

        if not self.repo_home or not self.repo_home.exists():
            return self._discovered_plugins

        # Scan for repos with src/local/nsplugin.json
        for item in self.repo_home.iterdir():
            if not item.is_dir():
                continue

            # Check for nsplugin.json in src/local/
            nsplugin_path = item / "src" / "local" / "nsplugin.json"
            if not nsplugin_path.exists():
                continue

            try:
                with open(nsplugin_path, "r") as f:
                    config = json.load(f)

                plugin_name = config.get("plugin_name")
                if not plugin_name:
                    continue

                self._discovered_plugins[plugin_name] = LocalPluginInfo(
                    plugin_name=plugin_name,
                    repo_name=item.name,
                    repo_path=item,
                    local_dir=item / "src" / "local",
                    config_path=nsplugin_path,
                )
            except (json.JSONDecodeError, IOError):
                # Skip invalid plugins
                continue

        return self._discovered_plugins

    def is_plugin_enabled(self, plugin_name: str) -> bool:
        """
        Check if a plugin is explicitly enabled via environment variable.

        The environment variable follows the pattern:
        HMD_LOCAL_NEURONSPHERE_ENABLE_<PLUGIN_NAME_UPPER>

        Args:
            plugin_name: Name of the plugin (e.g., 'transform')

        Returns:
            True if the plugin is enabled
        """
        env_var = f"HMD_LOCAL_NEURONSPHERE_ENABLE_{plugin_name.upper()}"
        value = os.environ.get(env_var, "").lower()
        return value in ("true", "1", "yes")

    def get_enabled_plugins(self) -> List[str]:
        """
        Get list of discovered plugins that are explicitly enabled.

        Returns:
            List of enabled plugin names
        """
        discovered = self.discover_plugins()
        return [name for name in discovered.keys() if self.is_plugin_enabled(name)]

    def get_plugin_info(self, plugin_name: str) -> Optional[LocalPluginInfo]:
        """
        Get info for a discovered local plugin.

        Args:
            plugin_name: Name of the plugin

        Returns:
            LocalPluginInfo if found and enabled, None otherwise
        """
        discovered = self.discover_plugins()
        info = discovered.get(plugin_name)
        if info and self.is_plugin_enabled(plugin_name):
            return info
        return None

    def get_plugin_config(self, plugin_name: str) -> Optional[Dict[str, Any]]:
        """
        Load nsplugin.json for a local plugin.

        Args:
            plugin_name: Name of the plugin

        Returns:
            Configuration dictionary or None if not found/enabled
        """
        info = self.get_plugin_info(plugin_name)
        if not info:
            return None

        try:
            with open(info.config_path, "r") as f:
                return json.load(f)
        except (json.JSONDecodeError, IOError):
            return None

    def get_plugin_local_dir(self, plugin_name: str) -> Optional[Path]:
        """
        Get the src/local directory path for a local plugin.

        Args:
            plugin_name: Name of the plugin

        Returns:
            Path to local directory or None if not found/enabled
        """
        info = self.get_plugin_info(plugin_name)
        return info.local_dir if info else None

    def get_compose_path(self, plugin_name: str) -> Optional[Path]:
        """
        Get path to docker-compose file for a local plugin.

        Args:
            plugin_name: Name of the plugin

        Returns:
            Path to compose file or None if not found
        """
        config = self.get_plugin_config(plugin_name)
        if not config:
            return None

        compose_file = config.get("compose_file")
        if not compose_file:
            return None

        info = self.get_plugin_info(plugin_name)
        if not info:
            return None

        compose_path = info.local_dir / compose_file
        return compose_path if compose_path.exists() else None

    def has_local_plugin(self, plugin_name: str) -> bool:
        """
        Check if a local plugin exists and is enabled.

        Args:
            plugin_name: Name of the plugin

        Returns:
            True if local plugin exists and is enabled
        """
        return self.get_plugin_info(plugin_name) is not None

    def get_local_config(self, plugin_name: str) -> Dict[str, Any]:
        """
        Load meta-data/config_local.json from the plugin's repo.

        This file contains local overrides for plugin configuration,
        such as SERVICE_CONFIG for microservices.

        Args:
            plugin_name: Name of the plugin

        Returns:
            Configuration dictionary from config_local.json, or empty dict
        """
        info = self.get_plugin_info(plugin_name)
        if not info:
            return {}

        config_local_path = info.repo_path / "meta-data" / "config_local.json"
        if not config_local_path.exists():
            return {}

        try:
            with open(config_local_path, "r") as f:
                return json.load(f)
        except (json.JSONDecodeError, IOError):
            return {}

    def get_merged_config(self, plugin_name: str) -> Dict[str, Any]:
        """
        Get merged configuration for a plugin.

        Merges:
        1. Defaults from nsplugin.json config section
        2. Overrides from meta-data/config_local.json

        Args:
            plugin_name: Name of the plugin

        Returns:
            Merged configuration dictionary
        """
        plugin_config = self.get_plugin_config(plugin_name)
        if not plugin_config:
            return {}

        # Get defaults from nsplugin.json config section
        config_schema = plugin_config.get("config", {})
        defaults = {}
        for key, schema in config_schema.items():
            if isinstance(schema, dict) and "default" in schema:
                defaults[key] = schema["default"]
            elif not isinstance(schema, dict):
                # Simple value as default
                defaults[key] = schema

        # Get overrides from config_local.json
        local_config = self.get_local_config(plugin_name)

        # Merge: local overrides defaults
        merged = {**defaults, **local_config}
        return merged

    def get_env_vars(self, plugin_name: str) -> Dict[str, str]:
        """
        Get environment variables to inject for a plugin.

        Reads the config section from nsplugin.json and config_local.json,
        then formats values as environment variables.

        Args:
            plugin_name: Name of the plugin

        Returns:
            Dictionary of environment variable name -> value
        """
        plugin_config = self.get_plugin_config(plugin_name)
        if not plugin_config:
            return {}

        config_schema = plugin_config.get("config", {})
        merged_config = self.get_merged_config(plugin_name)
        env_vars = {}

        for key, value in merged_config.items():
            schema = config_schema.get(key, {})

            # Determine env var name
            if isinstance(schema, dict):
                env_name = schema.get("env_var", key)
                value_type = schema.get("type", "string")
            else:
                env_name = key
                value_type = "string"

            # Format value based on type
            if value_type == "json" or isinstance(value, (dict, list)):
                env_vars[env_name] = json.dumps(value)
            else:
                env_vars[env_name] = str(value)

        return env_vars
