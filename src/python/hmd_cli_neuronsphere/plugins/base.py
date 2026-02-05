"""
Base module for thin wrapper plugins that load compose files from external artifacts.

This module provides helper functions for:
- Loading nsplugin.json configuration from extracted artifacts
- Getting paths to compose files and config directories
- Creating required directories in HMD_HOME
- Copying config files according to manifest mappings
- Rendering Jinja2 templates
- Copying PostgreSQL init scripts
- Checking plugin dependencies
"""

import json
import os
import shutil
from pathlib import Path
from typing import Any, Dict, List, Optional

import yaml

try:
    from jinja2 import Environment, FileSystemLoader

    HAS_JINJA2 = True
except ImportError:
    HAS_JINJA2 = False

_dirname = Path(os.path.dirname(__file__))
_external_dir = _dirname / ".." / "external"
_services_dir = (
    _dirname / ".." / "services"
)  # Keep for core services and backwards compatibility


def load_nsplugin_config(plugin_name: str) -> Optional[Dict[str, Any]]:
    """
    Load the nsplugin.json configuration for an external plugin.

    Args:
        plugin_name: Name of the plugin (e.g., 'transform', 'trino')

    Returns:
        Dictionary containing the plugin configuration, or None if not found
    """
    config_path = _external_dir / plugin_name / "src" / "local" / "nsplugin.json"
    if config_path.exists():
        with open(config_path, "r") as f:
            return json.load(f)
    return None


def get_external_compose_path(plugin_name: str) -> Optional[Path]:
    """
    Get path to docker-compose file from external artifact.

    Args:
        plugin_name: Name of the plugin

    Returns:
        Path to the compose file, or None if not found
    """
    config = load_nsplugin_config(plugin_name)
    if config:
        compose_file = config.get("compose_file")
        if compose_file:
            compose_path = _external_dir / plugin_name / "src" / "local" / compose_file
            if compose_path.exists():
                return compose_path
    return None


def get_external_local_dir(plugin_name: str) -> Optional[Path]:
    """
    Get path to the src/local directory from external artifact.

    Args:
        plugin_name: Name of the plugin

    Returns:
        Path to the local directory, or None if not found
    """
    local_dir = _external_dir / plugin_name / "src" / "local"
    if local_dir.exists():
        return local_dir
    return None


def get_external_config_dir(plugin_name: str) -> Optional[Path]:
    """
    Get path to config directory from external artifact.

    Args:
        plugin_name: Name of the plugin

    Returns:
        Path to the config directory, or None if not found
    """
    config_path = _external_dir / plugin_name / "src" / "local" / "config"
    if config_path.exists():
        return config_path
    return None


def has_external_artifact(plugin_name: str) -> bool:
    """
    Check if an external artifact exists for this plugin.

    Args:
        plugin_name: Name of the plugin

    Returns:
        True if external artifact exists with nsplugin.json
    """
    return load_nsplugin_config(plugin_name) is not None


def check_dependencies(plugins: Dict[str, bool], required: List[str]) -> bool:
    """
    Check if all required plugins are enabled.

    Args:
        plugins: Dictionary of plugin_name -> enabled status
        required: List of required plugin names

    Returns:
        True if all required plugins are enabled
    """
    for req in required:
        if not plugins.get(req, False):
            return False
    return True


def create_required_dirs(hmd_home: Path, dirs: List[str]) -> None:
    """
    Create required directories in HMD_HOME.

    Args:
        hmd_home: Path to HMD_HOME
        dirs: List of directory paths relative to HMD_HOME
    """
    for dir_ in dirs:
        full_dir = hmd_home / dir_
        if not full_dir.exists():
            os.umask(0)
            print(f"make {full_dir}")
            os.makedirs(full_dir, exist_ok=True)


def copy_configs(
    plugin_name: str, hmd_home: Path, mappings: List[Dict[str, Any]]
) -> None:
    """
    Copy config files according to manifest mappings.

    Supports:
    - Direct file copy
    - Directory copy (recursive)
    - if_empty: Only copy if destination is empty
    - merge: Merge JSON files (source overwrites destination keys)

    Args:
        plugin_name: Name of the plugin
        hmd_home: Path to HMD_HOME
        mappings: List of mapping dictionaries with source, dest, and options
    """
    local_dir = get_external_local_dir(plugin_name)
    if not local_dir:
        return

    for mapping in mappings:
        source = local_dir / mapping["source"]
        dest = hmd_home / mapping["dest"]

        if not source.exists():
            continue

        # Handle if_empty option
        if mapping.get("if_empty"):
            if dest.exists():
                if dest.is_dir() and os.listdir(dest):
                    continue
                elif dest.is_file():
                    continue

        if source.is_dir():
            os.makedirs(dest.parent, exist_ok=True)
            shutil.copytree(source, dest, dirs_exist_ok=True)
        else:
            os.makedirs(dest.parent, exist_ok=True)
            if mapping.get("merge") and dest.exists():
                # Merge JSON files
                with open(source, "r") as sf:
                    source_data = json.load(sf)
                with open(dest, "r") as df:
                    dest_data = json.load(df)
                merged = {**dest_data, **source_data}
                with open(dest, "w") as df:
                    json.dump(merged, df)
            else:
                shutil.copy2(source, dest)


def render_templates(
    plugin_name: str,
    hmd_home: Path,
    templates: List[Dict[str, Any]],
    context: Dict[str, Any],
) -> None:
    """
    Render Jinja2 templates to destination files.

    Args:
        plugin_name: Name of the plugin
        hmd_home: Path to HMD_HOME
        templates: List of template definitions with source, dest
        context: Dictionary of variables to pass to templates
    """
    if not HAS_JINJA2:
        print("Warning: Jinja2 not installed, skipping template rendering")
        return

    local_dir = get_external_local_dir(plugin_name)
    if not local_dir:
        return

    templates_dir = local_dir / "templates"
    if not templates_dir.exists():
        return

    env = Environment(loader=FileSystemLoader(str(templates_dir)))

    for template_def in templates:
        template_file = template_def["source"]
        dest = hmd_home / template_def["dest"]

        # Handle both full path and just filename
        if "/" in template_file:
            template_file = os.path.basename(template_file)

        try:
            template = env.get_template(template_file)
            rendered = template.render(**context)

            os.makedirs(dest.parent, exist_ok=True)
            with open(dest, "w") as f:
                f.write(rendered)
            print(f"Rendered template: {template_file} -> {dest}")
        except Exception as e:
            print(f"Warning: Failed to render template {template_file}: {e}")


def copy_postgres_scripts(plugin_name: str, hmd_home: Path, scripts: List[str]) -> None:
    """
    Copy PostgreSQL init scripts to the appropriate location.

    Args:
        plugin_name: Name of the plugin
        hmd_home: Path to HMD_HOME
        scripts: List of script paths relative to src/local/
    """
    local_dir = get_external_local_dir(plugin_name)
    if not local_dir:
        return

    scripts_dest = hmd_home / "postgresql" / "scripts" / "always-initdb.d"
    os.makedirs(scripts_dest, exist_ok=True)

    for script in scripts:
        source = local_dir / script
        if source.exists():
            shutil.copy2(source, scripts_dest / source.name)
            print(f"Copied postgres script: {source.name}")


def get_bundled_compose_path(compose_filename: str) -> Optional[Path]:
    """
    Get path to a bundled compose file in the services directory.
    Used for backwards compatibility when external artifacts don't exist.

    Args:
        compose_filename: Name of the compose file (e.g., 'docker-compose.transform.yml')

    Returns:
        Path to the compose file, or None if not found
    """
    compose_path = _services_dir / compose_filename
    if compose_path.exists():
        return compose_path
    return None


def get_bundled_services_dir() -> Path:
    """
    Get path to the bundled services directory.
    Used for backwards compatibility.

    Returns:
        Path to the services directory
    """
    return _services_dir


def build_template_context(
    resources: Dict[str, Any],
    configs: Dict[str, bool],
    extra_context: Optional[Dict[str, Any]] = None,
) -> Dict[str, Any]:
    """
    Build a context dictionary for template rendering.

    Includes:
    - All resources (services, databases, endpoints, etc.)
    - Plugin configurations (enabled/disabled states)
    - Environment variables (HMD_* prefixed)
    - Any extra context provided

    Args:
        resources: Aggregated resources from all plugins
        configs: Plugin enabled/disabled states
        extra_context: Additional context to include

    Returns:
        Context dictionary for template rendering
    """
    context = {
        "resources": resources,
        "configs": configs,
        "env": {},
    }

    # Add HMD environment variables
    for key, value in os.environ.items():
        if key.startswith("HMD_"):
            context["env"][key] = value

    # Add extra context
    if extra_context:
        context.update(extra_context)

    return context
