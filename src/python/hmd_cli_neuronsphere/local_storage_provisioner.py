"""
Local storage provisioner for Extend mode.

Creates static Kubernetes PersistentVolumes in k3s backed by ${HMD_HOME}
directories, using volume declarations from plugins' nsplugin.json files.

Per NERD001 SPEC003/SPEC013, this replaces EFS/EBS infrastructure repos that
cannot run on k3s with hostPath-backed PVs pointing to the same ${HMD_HOME}
directories used by Legacy mode's Docker Compose bind mounts.
"""

import os
import subprocess
from pathlib import Path
from typing import Any, Dict, List, Optional

import yaml

from cement import minimal_logger

logger = minimal_logger("local_storage_provisioner")

# Path inside the k3s node where ${HMD_HOME} is bind-mounted
K3S_HMD_HOME_MOUNT = "/hmd_home"

# Default PV capacity (informational; hostPath has no real limit)
DEFAULT_CAPACITY = "10Gi"


def collect_plugin_volumes(
    plugin_loader,
) -> List[Dict[str, Any]]:
    """Collect volume declarations from all enabled plugins.

    Iterates enabled plugins via the LocalPluginLoader, loads each plugin's
    nsplugin.json, and collects entries from the optional ``volumes`` field.

    Args:
        plugin_loader: A LocalPluginLoader instance.

    Returns:
        List of volume entries, each augmented with 'plugin_name'.
    """
    volumes = []
    for plugin_name in plugin_loader.get_enabled_plugins():
        config = plugin_loader.get_plugin_config(plugin_name)
        if not config:
            continue
        for vol in config.get("volumes", []):
            volumes.append({**vol, "plugin_name": plugin_name})
    return volumes


def generate_pv_manifest(
    volume: Dict[str, Any],
    namespace: str = "default",
) -> Dict[str, Any]:
    """Generate a PersistentVolume manifest for a single volume entry.

    Args:
        volume: A volume entry from nsplugin.json (augmented with plugin_name).
        namespace: The Kubernetes namespace for the PVC claimRef.

    Returns:
        A dict representing the PV YAML manifest.
    """
    plugin_name = volume["plugin_name"]
    vol_name = volume["name"]
    hmd_home_path = volume["hmd_home_path"]
    access_mode = volume.get("access_mode", "ReadWriteOnce")
    pvc_name = volume.get("pvc_name", f"{plugin_name}-{vol_name}")

    pv_name = f"{plugin_name}-{vol_name}-pv"
    host_path = f"{K3S_HMD_HOME_MOUNT}/{hmd_home_path}"

    manifest = {
        "apiVersion": "v1",
        "kind": "PersistentVolume",
        "metadata": {
            "name": pv_name,
            "labels": {
                "neuronsphere.io/plugin": plugin_name,
                "neuronsphere.io/volume": vol_name,
            },
        },
        "spec": {
            "capacity": {"storage": DEFAULT_CAPACITY},
            "accessModes": [access_mode],
            "persistentVolumeReclaimPolicy": "Retain",
            "hostPath": {
                "path": host_path,
                "type": "DirectoryOrCreate",
            },
            "claimRef": {
                "name": pvc_name,
                "namespace": namespace,
            },
        },
    }

    return manifest


def generate_all_pv_manifests(
    volumes: List[Dict[str, Any]],
    namespace: str = "default",
) -> List[Dict[str, Any]]:
    """Generate PV manifests for all volume entries.

    Args:
        volumes: List of volume entries from collect_plugin_volumes().
        namespace: The Kubernetes namespace for PVC claimRefs.

    Returns:
        List of PV manifest dicts.
    """
    return [generate_pv_manifest(vol, namespace) for vol in volumes]


def apply_pv_manifests(
    manifests: List[Dict[str, Any]],
    kubeconfig: Optional[str] = None,
) -> None:
    """Apply PV manifests to the k3s cluster via kubectl.

    Args:
        manifests: List of PV manifest dicts to apply.
        kubeconfig: Optional path to kubeconfig file for the k3s cluster.
    """
    if not manifests:
        logger.info("No PV manifests to apply.")
        return

    combined = yaml.dump_all(manifests, default_flow_style=False)

    kubectl_cmd = ["kubectl", "apply", "-f", "-"]
    env = os.environ.copy()
    if kubeconfig:
        env["KUBECONFIG"] = kubeconfig

    result = subprocess.run(
        kubectl_cmd,
        input=combined,
        capture_output=True,
        text=True,
        env=env,
    )

    if result.returncode != 0:
        logger.error(f"kubectl apply failed: {result.stderr}")
        raise RuntimeError(f"Failed to apply PV manifests to k3s: {result.stderr}")

    for line in result.stdout.strip().split("\n"):
        if line:
            logger.info(line)


def ensure_hmd_home_dirs(
    hmd_home: Path,
    volumes: List[Dict[str, Any]],
) -> None:
    """Ensure all volume directories exist under ${HMD_HOME}.

    Args:
        hmd_home: Path to HMD_HOME.
        volumes: List of volume entries with 'hmd_home_path' keys.
    """
    for vol in volumes:
        dir_path = hmd_home / vol["hmd_home_path"]
        dir_path.mkdir(parents=True, exist_ok=True)


def provision_local_storage(
    plugin_loader,
    hmd_home: Path,
    namespace: str = "default",
    kubeconfig: Optional[str] = None,
) -> None:
    """Main entry point: provision local storage for Extend mode.

    Collects volume declarations from enabled plugins, ensures ${HMD_HOME}
    directories exist, generates static PV manifests, and applies them to k3s.

    Args:
        plugin_loader: A LocalPluginLoader instance.
        hmd_home: Path to HMD_HOME.
        namespace: Kubernetes namespace for PVC claimRefs.
        kubeconfig: Optional path to kubeconfig for the k3s cluster.
    """
    volumes = collect_plugin_volumes(plugin_loader)

    if not volumes:
        logger.info(
            "No plugin volume declarations found. Skipping local storage provisioning."
        )
        return

    logger.info(
        f"Provisioning local storage: {len(volumes)} volumes from "
        f"{len({v['plugin_name'] for v in volumes})} plugins"
    )

    # Ensure directories exist under ${HMD_HOME}
    ensure_hmd_home_dirs(hmd_home, volumes)

    # Generate and apply PV manifests
    manifests = generate_all_pv_manifests(volumes, namespace)
    apply_pv_manifests(manifests, kubeconfig)

    logger.info("Local storage provisioning complete.")
