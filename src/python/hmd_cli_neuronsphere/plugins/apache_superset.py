"""
Apache Superset service plugin for local NeuronSphere.

This is a thin wrapper plugin that:
1. Checks for external artifact (nsplugin.json) first
2. Falls back to bundled services/ for backwards compatibility
"""

import os
from pathlib import Path
import shutil
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
    get_bundled_compose_path,
    build_template_context,
)

_PLUGIN_NAME = "apache_superset"
_dirname = Path(os.path.dirname(__file__))
_services_dir = _dirname / ".." / "services"


def enabled(config_overrides: Dict[str, bool] = {}) -> bool:
    """Check if apache_superset plugin is enabled."""
    config = load_nsplugin_config(_PLUGIN_NAME)
    if config:
        env_var = config.get(
            "env_var_override", "HMD_LOCAL_NEURONSPHERE_ENABLE_APACHE_SUPERSET"
        )
    else:
        env_var = "HMD_LOCAL_NEURONSPHERE_ENABLE_APACHE_SUPERSET"

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
    return {"endpoints": ["superset:localhost:8088"]}


def prepare_hmd_home(hmd_home: str, configs: Dict[str, bool] = {}) -> None:
    """Prepare HMD_HOME directories and config files."""
    HMD_HOME = Path(hmd_home)
    config = load_nsplugin_config(_PLUGIN_NAME)

    if config:
        # Use external artifact configuration
        _prepare_from_nsplugin(HMD_HOME, config, configs)
    else:
        # Fallback to legacy implementation
        _prepare_hmd_home_legacy(HMD_HOME, configs)


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

    # Fallback: if .env-non-dev wasn't copied (e.g. missing from external artifact),
    # copy from bundled services directory
    env_non_dev_target = hmd_home / ".cache" / "superset" / ".env-non-dev"
    if not env_non_dev_target.exists():
        bundled_env = _services_dir / "superset" / ".env-non-dev"
        if bundled_env.exists():
            env_non_dev_target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(bundled_env, env_non_dev_target)

    # Render templates if any
    templates = config.get("templates", [])
    if templates:
        context = build_template_context({}, configs)
        render_templates(_PLUGIN_NAME, hmd_home, templates, context)

    # Copy postgres scripts
    postgres_scripts = config.get("postgres_scripts", [])
    copy_postgres_scripts(_PLUGIN_NAME, hmd_home, postgres_scripts)


def _prepare_hmd_home_legacy(hmd_home: Path, configs: Dict[str, bool] = {}) -> None:
    """Legacy prepare_hmd_home implementation for backwards compatibility."""
    required_dirs = [Path("superset_home"), Path(".cache", "superset")]

    for dir_ in required_dirs:
        full_dir = hmd_home / dir_
        if not full_dir.exists():
            os.umask(0)
            print("make", str(full_dir))
            os.makedirs(full_dir, exist_ok=True)

    shutil.copy2(
        _services_dir / "superset" / ".env-non-dev",
        hmd_home / ".cache" / "superset" / ".env-non-dev",
    )


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

    # Get compose file path (external artifact or bundled)
    compose_path = get_external_compose_path(_PLUGIN_NAME)

    if not compose_path:
        # Fallback to bundled compose file
        compose_path = get_bundled_compose_path("docker-compose.apache-superset.yml")

    if not compose_path or not compose_path.exists():
        print(f"Warning: No compose file found for {_PLUGIN_NAME}")
        return None

    # Load compose file
    with open(compose_path, "r") as dc:
        compose_dict = yaml.safe_load(dc)

    # Write to cache
    output_path = cache_dir / "docker-compose.apache-superset.yml"
    if output_path.exists():
        os.unlink(output_path)

    with open(output_path, "w") as dc_out:
        yaml.safe_dump(compose_dict, dc_out)

    return output_path
