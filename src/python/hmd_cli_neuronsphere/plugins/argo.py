"""
Argo Workflows plugin for local NeuronSphere.

Argo lives on the Floci EKS k3s cluster — there is no Docker Compose file.
The plugin's job is to:
1. Locate the external nsplugin artifact (hmd-app-argo/src/local/nsplugin.json) via the
   standard LocalPluginLoader pattern.
2. Run the install script (hmd-app-argo/src/local/scripts/install_argo.sh) against
   the running k3s cluster after `hmd neuronsphere up` brings k3s online.
3. Capture the service-account bearer token the script emits and stash it in Floci
   Secrets Manager so ms-transform can read it at runtime via ARGO_TOKEN.

The plugin reports an `argo-server` service so ms-naming registers it; the actual
HTTP route is served by nginx (`/argo/`) which proxies to the k3s NodePort.
"""

import os
import subprocess
from pathlib import Path
from typing import Any, Dict, List, Optional

from cement import minimal_logger

from .base import load_nsplugin_config, get_external_local_dir, create_required_dirs

logger = minimal_logger("ns_plugin_argo")

_PLUGIN_NAME = "argo"
_DEFAULT_NODEPORT = "30246"
_DEFAULT_NAMESPACE = "argo"
_DEFAULT_SERVICEACCOUNT = "transform"


def enabled(config_overrides: Dict[str, bool] = {}) -> bool:
    config = load_nsplugin_config(_PLUGIN_NAME)
    env_var = (
        config.get("env_var_override", "HMD_LOCAL_NEURONSPHERE_ENABLE_ARGO")
        if config
        else "HMD_LOCAL_NEURONSPHERE_ENABLE_ARGO"
    )
    val = os.environ.get(env_var)
    if val is not None:
        return val.lower() == "true"
    if config and "enabled_by_default" in config:
        return bool(config["enabled_by_default"])
    # Opt-in: off by default (minimal-core local NeuronSphere). Enable via the
    # env flag above or `hmd neuronsphere configure`.
    return config_overrides.get(_PLUGIN_NAME, False)


def get_resources() -> Dict[str, Any]:
    config = load_nsplugin_config(_PLUGIN_NAME)
    if config and "resources" in config:
        return config["resources"]
    return {
        "services": [
            {"name": "argo-server", "url": "http://hmd_proxy/argo/"},
        ],
    }


def prepare_hmd_home(hmd_home: str, configs: Dict[str, bool] = {}) -> None:
    """Create required dirs and (best-effort) install Argo onto k3s.

    The install runs here (not in render_compose_yaml) because by the time
    prepare_hmd_home is called, KUBECONFIG should already be set by the
    main startup flow after wait_for_k3s_ready() succeeds.
    """
    config = load_nsplugin_config(_PLUGIN_NAME) or {}
    create_required_dirs(Path(hmd_home), config.get("required_dirs", []))

    if not os.environ.get("KUBECONFIG"):
        logger.debug("KUBECONFIG not set; deferring Argo install (k3s not ready)")
        return

    install_argo_on_k3s()


def render_compose_yaml(
    resources: Dict[str, List[Dict[str, str]]],
    cache_dir: Path,
    configs: Dict[str, bool] = {},
) -> Optional[Path]:
    """Argo has no Docker Compose footprint — it runs on k3s."""
    return None


def install_argo_on_k3s() -> Optional[str]:
    """Run the install script against the live k3s cluster.

    Stashes the emitted service-account token in Floci Secrets Manager
    under the secret name `argo-token`. Returns the token string (or None
    if the install failed).
    """
    local_dir = get_external_local_dir(_PLUGIN_NAME)
    if not local_dir:
        logger.warning(
            f"hmd-app-argo external local plugin not found in HMD_REPO_HOME; skipping Argo install"
        )
        return None

    script = local_dir / "scripts" / "install_argo.sh"
    if not script.exists():
        logger.warning(f"Argo install script missing: {script}")
        return None

    env = os.environ.copy()
    env.setdefault("ARGO_NAMESPACE", _DEFAULT_NAMESPACE)
    env.setdefault("ARGO_NODEPORT", _DEFAULT_NODEPORT)
    env.setdefault("ARGO_SERVICEACCOUNT", _DEFAULT_SERVICEACCOUNT)

    logger.debug(f"Running Argo install script: {script}")
    result = subprocess.run(
        ["bash", str(script)],
        env=env,
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        logger.warning(
            f"Argo install failed (exit={result.returncode}):\n"
            f"stdout={result.stdout}\nstderr={result.stderr}"
        )
        return None

    # Parse the ARGO_TOKEN=... line out of the script's stdout.
    token: Optional[str] = None
    for line in result.stdout.splitlines():
        if line.startswith("ARGO_TOKEN="):
            token = line.partition("=")[2].strip()
            break

    if token:
        _store_argo_token(token)
    else:
        logger.debug(
            "Argo install succeeded but no token emitted; transform will fall back to anonymous"
        )

    return token


def _store_argo_token(token: str) -> None:
    """Persist the Argo service-account bearer token in Floci Secrets Manager."""
    try:
        from ..floci_deployer import _get_client
    except Exception as e:
        logger.debug(f"Secrets Manager client unavailable: {e}")
        return

    sm = _get_client("secretsmanager")
    try:
        sm.create_secret(Name="argo-token", SecretString=token)
        logger.debug("Stored argo-token in Floci Secrets Manager")
    except Exception:
        try:
            sm.put_secret_value(SecretId="argo-token", SecretString=token)
            logger.debug("Updated argo-token in Floci Secrets Manager")
        except Exception as e:
            logger.warning(f"Failed to write argo-token secret: {e}")
