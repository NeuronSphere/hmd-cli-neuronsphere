"""
Loader for local filesystem plugins.

This module provides:
- Explicit local plugin paths via HMD_LOCAL_PLUGINS environment variable
- Discovery of plugins in HMD_REPO_HOME (when HMD_LOCAL_PLUGINS_SCAN_REPO_HOME=true)
- Local plugin priority over installed plugins

Usage:
    # Explicit paths (recommended)
    export HMD_LOCAL_PLUGINS=/path/to/repo1:/path/to/repo2

    # Or scan HMD_REPO_HOME for all plugins with src/local/nsplugin.json
    export HMD_LOCAL_PLUGINS_SCAN_REPO_HOME=true
"""

import json
import logging
import os
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Dict, List, Optional

logger = logging.getLogger(__name__)


@dataclass
class LocalPluginInfo:
    """Information about a discovered local plugin."""

    plugin_name: str
    repo_name: str
    repo_path: Path
    local_dir: Path
    config_path: Path


class LocalPluginLoader:
    """Loader for local filesystem plugins."""

    def __init__(self, repo_home: Optional[Path] = None) -> None:
        """
        Initialize the loader.

        Args:
            repo_home: Path to HMD_REPO_HOME. If None, reads from environment.
        """
        repo_home_str = os.environ.get("HMD_REPO_HOME", "")
        self.repo_home = repo_home or (Path(repo_home_str) if repo_home_str else None)
        self._discovered_plugins: Optional[Dict[str, LocalPluginInfo]] = None

    def _load_plugin_from_path(self, repo_path: Path) -> Optional[LocalPluginInfo]:
        """
        Load a plugin from a specific repository path.

        Args:
            repo_path: Path to the repository root

        Returns:
            LocalPluginInfo if valid plugin found, None otherwise
        """
        if not repo_path.is_dir():
            logger.debug(f"Not a directory: {repo_path}")
            return None

        # Check for nsplugin.json in src/local/
        nsplugin_path = repo_path / "src" / "local" / "nsplugin.json"
        if not nsplugin_path.exists():
            logger.debug(f"No nsplugin.json at: {nsplugin_path}")
            return None

        try:
            with open(nsplugin_path, "r") as f:
                config = json.load(f)

            plugin_name = config.get("plugin_name")
            if not plugin_name:
                logger.warning(f"Missing plugin_name in: {nsplugin_path}")
                return None

            return LocalPluginInfo(
                plugin_name=plugin_name,
                repo_name=repo_path.name,
                repo_path=repo_path,
                local_dir=repo_path / "src" / "local",
                config_path=nsplugin_path,
            )
        except json.JSONDecodeError as e:
            logger.warning(f"Invalid JSON in {nsplugin_path}: {e}")
            return None
        except IOError as e:
            logger.warning(f"Error reading {nsplugin_path}: {e}")
            return None

    def discover_plugins(self) -> Dict[str, LocalPluginInfo]:
        """
        Discover local plugins from explicit paths or HMD_REPO_HOME scanning.

        Discovery methods (in order of priority):
        1. HMD_LOCAL_PLUGINS: Colon-separated list of explicit repo paths
        2. HMD_REPO_HOME scan: Only if HMD_LOCAL_PLUGINS_SCAN_REPO_HOME=true

        Returns:
            Dictionary mapping plugin_name to LocalPluginInfo
        """
        if self._discovered_plugins is not None:
            return self._discovered_plugins

        self._discovered_plugins = {}

        # Method 1: Explicit paths from HMD_LOCAL_PLUGINS
        explicit_plugins = os.environ.get("HMD_LOCAL_PLUGINS", "")
        if explicit_plugins:
            for path_str in explicit_plugins.split(":"):
                path_str = path_str.strip()
                if not path_str:
                    continue

                repo_path = Path(path_str).expanduser().resolve()
                info = self._load_plugin_from_path(repo_path)
                if info:
                    self._discovered_plugins[info.plugin_name] = info
                    logger.info(
                        f"Loaded local plugin '{info.plugin_name}' from {repo_path}"
                    )

        # Method 2: Scan HMD_REPO_HOME (only if explicitly enabled)
        scan_repo_home = os.environ.get(
            "HMD_LOCAL_PLUGINS_SCAN_REPO_HOME", ""
        ).lower() in ("true", "1", "yes")

        if scan_repo_home and self.repo_home and self.repo_home.exists():
            logger.debug(f"Scanning HMD_REPO_HOME: {self.repo_home}")
            for item in self.repo_home.iterdir():
                if not item.is_dir():
                    continue

                # Skip if already loaded via explicit path
                info = self._load_plugin_from_path(item)
                if info and info.plugin_name not in self._discovered_plugins:
                    self._discovered_plugins[info.plugin_name] = info
                    logger.info(
                        f"Discovered local plugin '{info.plugin_name}' in {item}"
                    )

        return self._discovered_plugins

    def _is_explicitly_listed(self, plugin_name: str) -> bool:
        """
        Check if a plugin was loaded from HMD_LOCAL_PLUGINS (explicit list).

        Args:
            plugin_name: Name of the plugin

        Returns:
            True if plugin was explicitly listed
        """
        explicit_plugins = os.environ.get("HMD_LOCAL_PLUGINS", "")
        if not explicit_plugins:
            return False

        # Check if plugin's repo path is in the explicit list
        discovered = self.discover_plugins()
        info = discovered.get(plugin_name)
        if not info:
            return False

        for path_str in explicit_plugins.split(":"):
            path_str = path_str.strip()
            if not path_str:
                continue
            explicit_path = Path(path_str).expanduser().resolve()
            if explicit_path == info.repo_path:
                return True

        return False

    def is_plugin_enabled(self, plugin_name: str) -> bool:
        """
        Check if a plugin is enabled.

        Plugins are enabled if:
        1. Listed explicitly in HMD_LOCAL_PLUGINS (auto-enabled), OR
        2. Discovered via scan AND has HMD_LOCAL_NEURONSPHERE_ENABLE_<NAME>=true

        Args:
            plugin_name: Name of the plugin (e.g., 'transform')

        Returns:
            True if the plugin is enabled
        """
        # Explicitly listed plugins are always enabled
        if self._is_explicitly_listed(plugin_name):
            return True

        # Scanned plugins need explicit env var
        env_var = (
            f"HMD_LOCAL_NEURONSPHERE_ENABLE_{plugin_name.upper().replace('-', '_')}"
        )
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
