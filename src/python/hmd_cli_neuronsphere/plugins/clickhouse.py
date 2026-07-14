"""
ClickHouse service plugin for local NeuronSphere.

This is a thin wrapper plugin that:
1. Checks for external artifact (nsplugin.json) first
2. Has no bundled fallback (external artifact only)
"""

import os
from pathlib import Path
from typing import Any, Dict, List, Optional

import yaml

from .base import (
    load_nsplugin_config,
    get_external_compose_path,
    has_external_artifact,
    check_dependencies,
    create_required_dirs,
    copy_configs,
    render_templates,
    copy_postgres_scripts,
    build_template_context,
)

_PLUGIN_NAME = "clickhouse"


def enabled(config_overrides: Dict[str, bool] = {}) -> bool:
    """Check if clickhouse plugin is enabled."""
    config = load_nsplugin_config(_PLUGIN_NAME)
    if config:
        env_var = config.get(
            "env_var_override", "HMD_LOCAL_NEURONSPHERE_ENABLE_CLICKHOUSE"
        )
    else:
        env_var = "HMD_LOCAL_NEURONSPHERE_ENABLE_CLICKHOUSE"

    val = os.environ.get(env_var)
    if val is not None:
        return val == "true"
    # Opt-in: off by default (minimal-core local NeuronSphere). Enable via the
    # env flag above or `hmd neuronsphere configure`.
    return config_overrides.get(_PLUGIN_NAME, False)


def get_resources() -> Dict[str, Any]:
    """Return resources provided by this plugin."""
    config = load_nsplugin_config(_PLUGIN_NAME)
    if config and "resources" in config:
        return config["resources"]

    # Fallback to hardcoded resources
    return {"endpoints": ["clickhouse:localhost:8123", "clickhouse-tcp:localhost:9000"]}


def prepare_hmd_home(hmd_home: str, configs: Dict[str, bool] = {}) -> None:
    """Prepare HMD_HOME directories and config files."""
    HMD_HOME = Path(hmd_home)
    config = load_nsplugin_config(_PLUGIN_NAME)

    if config:
        _prepare_from_nsplugin(HMD_HOME, config, configs)
    else:
        print(f"Warning: No nsplugin.json found for {_PLUGIN_NAME}")


def _prepare_from_nsplugin(
    hmd_home: Path, config: Dict[str, Any], configs: Dict[str, bool]
) -> None:
    """Prepare HMD_HOME using nsplugin.json configuration."""
    # Create required directories
    required_dirs = config.get("required_dirs", [])
    create_required_dirs(hmd_home, required_dirs)

    # Copy config files
    config_mappings = config.get("config_mappings", [])
    copy_configs(_PLUGIN_NAME, hmd_home, config_mappings)

    # Render templates if any
    templates = config.get("templates", [])
    if templates:
        context = build_template_context({}, configs)
        render_templates(_PLUGIN_NAME, hmd_home, templates, context)

    # Copy postgres scripts
    postgres_scripts = config.get("postgres_scripts", [])
    copy_postgres_scripts(_PLUGIN_NAME, hmd_home, postgres_scripts)


def render_compose_yaml(
    resources: Dict[str, List[str]],
    cache_dir: Path,
    configs: Dict[str, bool] = {},
) -> Optional[Path]:
    """Render docker-compose file to cache directory."""
    config = load_nsplugin_config(_PLUGIN_NAME)

    # Check dependencies if configured
    if config:
        deps = config.get("dependencies", {})
        required_plugins = deps.get("requires_plugins", [])
        if required_plugins and not check_dependencies(configs, required_plugins):
            print(f"Cannot start {_PLUGIN_NAME} service because dependency not enabled")
            return None

    # Get compose file path (external artifact only)
    compose_path = get_external_compose_path(_PLUGIN_NAME)

    if not compose_path or not compose_path.exists():
        print(f"Warning: No compose file found for {_PLUGIN_NAME}")
        return None

    # Load compose file
    with open(compose_path, "r") as dc:
        compose_dict = yaml.safe_load(dc)

    # Write to cache
    output_path = cache_dir / f"docker-compose.{_PLUGIN_NAME}.yml"
    if output_path.exists():
        os.unlink(output_path)

    with open(output_path, "w") as dc_out:
        yaml.safe_dump(compose_dict, dc_out)

    return output_path
