"""
Transform service plugin for local NeuronSphere.

This is a thin wrapper plugin that:
1. Checks for external artifact (nsplugin.json) first
2. Falls back to bundled services/ for backwards compatibility
"""

import json
import os
from pathlib import Path
import shutil
from typing import Any, Dict, List, Optional

import yaml

from .base import (
    load_nsplugin_config,
    get_external_compose_path,
    get_external_local_dir,
    has_external_artifact,
    check_dependencies,
    create_required_dirs,
    copy_configs,
    render_templates,
    copy_postgres_scripts,
    get_bundled_compose_path,
    get_bundled_services_dir,
    build_template_context,
)

_PLUGIN_NAME = "transform"
_dirname = Path(os.path.dirname(__file__))
_services_dir = _dirname / ".." / "services"


def enabled(config_overrides: Dict[str, bool] = {}) -> bool:
    """Check if transform plugin is enabled."""
    # Check nsplugin.json for custom env var name
    config = load_nsplugin_config(_PLUGIN_NAME)
    if config:
        env_var = config.get(
            "env_var_override", "HMD_LOCAL_NEURONSPHERE_ENABLE_TRANSFORM"
        )
    else:
        env_var = "HMD_LOCAL_NEURONSPHERE_ENABLE_TRANSFORM"

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
    # Environment services are routed under their environment's slug (see
    # `nginx_router.write_env_routes`); the unprefixed path is control-plane only.
    resources = {
        "services": [
            {"name": "ms-transform", "url": "http://hmd_proxy/local/transform/"}
        ],
        "databases": [
            {
                "username": "transform",
                "password": "transform",
                "database": "transform",
            }
        ],
    }

    return resources


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
    required_dirs = [
        Path("transform"),
        Path("transform", "queries"),
        Path("data", "local_transforms"),
        Path("queues"),
    ]

    for dir_ in required_dirs:
        full_dir = hmd_home / dir_
        if not full_dir.exists():
            os.umask(0)
            print("make", str(full_dir))
            os.makedirs(full_dir, exist_ok=True)

    if len(os.listdir(hmd_home / "queues")) == 0:
        shutil.copytree(
            _services_dir / "queues",
            hmd_home / "queues",
            dirs_exist_ok=True,
        )
    if len(os.listdir(hmd_home / "transform" / "queries")) == 0:
        query_cfg = {}
        with open(_services_dir / "transform" / "query_config.json", "r") as qc:
            query_cfg = json.load(qc)

        existing_cfg = {}
        if (hmd_home / "transform" / "queries" / "query_config.json").exists():
            with open(
                hmd_home / "transform" / "queries" / "query_config.json", "r"
            ) as qc:
                existing_cfg = json.load(qc)

        with open(hmd_home / "transform" / "queries" / "query_config.json", "w") as qc:
            json.dump({**existing_cfg, **query_cfg}, qc)


def render_compose_yaml(
    resources: Dict[str, List[Dict[str, str]]],
    cache_dir: Path,
    configs: Dict[str, bool] = {},
) -> Optional[Path]:
    """Render docker-compose file to cache directory."""
    config = load_nsplugin_config(_PLUGIN_NAME)

    # Check dependencies
    if config:
        deps = config.get("dependencies", {})
        required_plugins = deps.get("requires_plugins", [])
        if not check_dependencies(configs, required_plugins):
            print(f"Cannot start {_PLUGIN_NAME} service because dependency not enabled")
            return None
    else:
        # Legacy dependency check
        if not configs.get("graph") or not configs.get("airflow"):
            print("Cannot start Transform service because dependency not enabled")
            return None

    # Get compose file path (external artifact or bundled)
    compose_path = get_external_compose_path(_PLUGIN_NAME)

    if not compose_path:
        # Check environment variable override
        env_compose = os.environ.get("HMD_MS_TRANSFORM_LOCAL_COMPOSE")
        if env_compose:
            compose_path = Path(env_compose)
        else:
            # Fallback to bundled compose file
            compose_path = get_bundled_compose_path("docker-compose.transform.yml")

    if not compose_path or not compose_path.exists():
        print(f"Warning: No compose file found for {_PLUGIN_NAME}")
        return None

    # Load and customize compose file
    with open(compose_path, "r") as dc:
        compose_dict = yaml.safe_load(dc)

    # Apply resource customizations (buckets, etc.)
    buckets = resources.get("buckets", [])

    for bucket in buckets:
        if "transform" in compose_dict.get("services", {}):
            compose_dict["services"]["transform"]["environment"][
                f'{bucket["name"].upper()}_BUCKET'
            ] = f's3://{bucket["url"]}'

    # Inject HMDMS-service bucket env vars (e.g. DEVICE_BUCKET=s3://device-librarian).
    # Each librarian declares its own content_path_configs internally; transform
    # only needs to know which bucket name to address per service.
    try:
        from ..loaders import LocalPluginLoader

        local_loader = LocalPluginLoader()
        for plugin_name, hmdms_spec in local_loader.get_all_hmdms_services().items():
            if "transform" not in compose_dict.get("services", {}):
                break
            for hmdms_bucket in hmdms_spec.get("buckets", []) or []:
                env_var = hmdms_bucket.get("env_var")
                bucket_name = hmdms_bucket.get("name")
                if env_var and bucket_name:
                    compose_dict["services"]["transform"].setdefault("environment", {})[
                        env_var
                    ] = f"s3://{bucket_name}"
    except Exception:
        pass

    # When Argo is enabled, wire ARGO_HOST + ARGO_TOKEN into transform's environment
    if configs.get("argo", False):
        _apply_argo_overrides(compose_dict)

    # Write to cache
    output_path = cache_dir / f"docker-compose.{_PLUGIN_NAME}.yml"
    if output_path.exists():
        os.unlink(output_path)

    with open(output_path, "w") as dc_out:
        yaml.safe_dump(compose_dict, dc_out)

    return output_path


def _apply_argo_overrides(compose_dict: dict) -> None:
    """Inject ARGO_HOST/ARGO_TOKEN/ARGO_NAMESPACE into transform service.

    Pulls the Argo bearer token from Floci Secrets Manager (where
    plugins/argo.py stashes it after the install script runs). Falls back
    silently if Argo isn't reachable yet — the engine will run anonymously.
    """
    services = compose_dict.get("services", {})
    if "transform" not in services:
        return

    env = services["transform"].setdefault("environment", {})
    env.setdefault(
        "ARGO_HOST", os.environ.get("ARGO_HOST", "http://host.docker.internal:30246")
    )
    env.setdefault("ARGO_NAMESPACE", os.environ.get("ARGO_NAMESPACE", "argo"))

    try:
        from ..floci_deployer import _get_client

        sm = _get_client("secretsmanager")
        secret = sm.get_secret_value(SecretId="argo-token")
        token = secret.get("SecretString")
        if token:
            env["ARGO_TOKEN"] = token
    except Exception:
        # Argo not yet installed or Secrets Manager unavailable — skip silently.
        pass
