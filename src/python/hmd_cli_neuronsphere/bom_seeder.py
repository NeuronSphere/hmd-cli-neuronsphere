"""BOM seeder for local extend mode.

Seeds the ms-deployment service with repo class versions, environment,
deployment set, and changeset entities, then applies the changeset
(with skip_async=True) so the CLI can drive execution locally.

Supports loading the BOM from:
1. An explicit ``bom`` parameter
2. A JSON file via ``HMD_LOCAL_BOM_FILE`` environment variable
3. A minimal built-in ``LOCAL_BOM`` (for backward compat / testing)
"""

import json
import logging
import os
from typing import Dict, List, Optional, Tuple

import requests
from cement import minimal_logger

logger = minimal_logger("bom_seeder")

LOCAL_BOM = [
    {
        "repo_instance_name": "vpc",
        "repo_class_name": "hmd-vpc",
        "deployment_id": "local",
        "instance_configuration": {},
        "dependencies": {},
    },
    {
        "repo_instance_name": "project-bucket",
        "repo_class_name": "hmd-inf-s3bucket",
        "deployment_id": "local",
        "instance_configuration": {},
        "dependencies": {},
    },
]


def load_bom_from_file(path: str) -> List[Dict]:
    """Load a BOM from a JSON file, filtering out image-only entries.

    :param path: Path to BOM JSON file (array of deployment entries)
    :returns: Validated list of BOM entries
    :raises FileNotFoundError: If the file doesn't exist
    :raises json.JSONDecodeError: If the file is not valid JSON
    """
    with open(path) as f:
        bom = json.load(f)

    if not isinstance(bom, list):
        raise ValueError(
            f"BOM file must contain a JSON array, got {type(bom).__name__}"
        )

    # Filter out image-only entries (not deployable)
    filtered = [entry for entry in bom if not entry.get("image_only", False)]

    logger.info(
        f"Loaded {len(filtered)} entries from {path} ({len(bom) - len(filtered)} image-only filtered)"
    )
    return filtered


def _get_repo_version(repo_class_name: str, bom_version: Optional[str] = None) -> str:
    """Read VERSION from the repo in HMD_REPO_HOME, falling back to BOM version.

    Priority:
    1. Local VERSION file (developer may have a newer version)
    2. bom_version (from the snapshot file)
    3. "0.1.0" as last resort
    """
    repo_home = os.environ.get("HMD_REPO_HOME", "")
    if repo_home:
        version_path = os.path.join(repo_home, repo_class_name, "meta-data", "VERSION")
        try:
            with open(version_path) as f:
                return f.read().strip()
        except FileNotFoundError:
            pass

    if bom_version:
        return bom_version

    logger.warning(f"VERSION not found for {repo_class_name}, using 0.1.0")
    return "0.1.0"


def _get_repo_deploy_config(repo_class_name: str) -> Optional[Dict]:
    """Read default_configuration from the repo's manifest.json.

    Returns None if not found (caller should use BOM fallback).
    """
    repo_home = os.environ.get("HMD_REPO_HOME", "")
    if not repo_home:
        return None
    manifest_path = os.path.join(
        repo_home, repo_class_name, "meta-data", "manifest.json"
    )
    try:
        with open(manifest_path) as f:
            manifest = json.load(f)
        return manifest.get("deploy", {}).get("default_configuration", {})
    except (FileNotFoundError, json.JSONDecodeError):
        return None


def _get_repo_dependencies(repo_class_name: str) -> Optional[Dict]:
    """Read deploy dependencies from the repo's manifest.json.

    Returns None if not found (caller should use BOM fallback).
    """
    repo_home = os.environ.get("HMD_REPO_HOME", "")
    if not repo_home:
        return None
    manifest_path = os.path.join(
        repo_home, repo_class_name, "meta-data", "manifest.json"
    )
    try:
        with open(manifest_path) as f:
            manifest = json.load(f)
        return manifest.get("deploy", {}).get("dependencies", {})
    except (FileNotFoundError, json.JSONDecodeError):
        return None


def _put_entity(base_url: str, entity_type: str, data: Dict) -> Dict:
    """Create an entity via the CRUD PUT endpoint."""
    url = f"{base_url}/api/{entity_type}"
    resp = requests.put(url, json=data, timeout=30)
    resp.raise_for_status()
    return resp.json()


def _post_apiop(base_url: str, operation: str, payload: Dict = None) -> Dict:
    """Call an apiop endpoint."""
    url = f"{base_url}/apiop/{operation}"
    if payload is not None:
        resp = requests.post(url, json=payload, timeout=60)
    else:
        resp = requests.post(url, timeout=60)
    resp.raise_for_status()
    return resp.json()


def _resolve_bom() -> List[Dict]:
    """Resolve the BOM source based on environment configuration.

    Priority:
    1. HMD_LOCAL_BOM_FILE env var → load from file
    2. LOCAL_BOM constant (backward compat)
    """
    bom_file = os.environ.get("HMD_LOCAL_BOM_FILE")
    if bom_file:
        logger.info(f"Loading BOM from file: {bom_file}")
        return load_bom_from_file(bom_file)

    logger.info("Using built-in LOCAL_BOM (2 entries)")
    return LOCAL_BOM


def seed_bom(base_url: str, bom: List[Dict] = None) -> Tuple[str, List[Dict]]:
    """Seed the deployment graph and return (csd_nid, nodes) for local execution.

    Steps:
    1. Register repo class versions via add_repo_class_version
    2. Create Environment, DeploymentSet, ChangeSet via CRUD PUT
    3. Apply changeset with skip_async=True
    4. Generate local deployment manifest (ordered scripts)

    :param base_url: ms-deployment base URL (e.g., http://localhost/hmd_ms_deployment)
    :param bom: Optional BOM override; defaults to HMD_LOCAL_BOM_FILE or LOCAL_BOM
    :returns: Tuple of (csd_nid, nodes list from generate_local_deployment)
    """
    if bom is None:
        bom = _resolve_bom()

    logger.info(f"Seeding BOM with {len(bom)} entries")

    # 1. Register repo class versions
    for entry in bom:
        repo_name = entry["repo_class_name"]
        bom_version = entry.get("repo_class_version")
        version = _get_repo_version(repo_name, bom_version=bom_version)

        # Use local manifest data if available, fall back to BOM entry
        dependencies = _get_repo_dependencies(repo_name)
        if dependencies is None:
            dependencies = entry.get("dependencies", {})

        default_config = _get_repo_deploy_config(repo_name)
        if default_config is None:
            default_config = entry.get("instance_configuration", {})

        logger.info(f"Adding repo class version: {repo_name}@{version}")
        _post_apiop(
            base_url,
            "add_repo_class_version",
            {
                "repo_class_name": repo_name,
                "version": version,
                "dependencies": dependencies,
                "default_configuration": default_config,
            },
        )
        # Store resolved version back into entry for changeset
        entry["repo_class_version"] = version

    # 2. Create Environment
    logger.info("Creating 'local' environment")
    _put_entity(
        base_url,
        "environment",
        {
            "type": "local",
            "account_number": "000000000000",
            "hmd_region": os.environ.get("HMD_REGION", "us-west-2"),
        },
    )

    # 3. Create DeploymentSet
    logger.info("Creating 'local' deployment set")
    _put_entity(
        base_url,
        "deployment_set",
        {
            "name": "local",
            "definition": [
                {
                    "environment": "local",
                    "deployment_gate": {"transforms": [], "approval": False},
                },
            ],
        },
    )

    # 4. Create ChangeSet
    changeset_def = [
        {
            "deployment_id": entry.get("deployment_id", "local"),
            "repo_instance_name": entry["repo_instance_name"],
            "repo_class_name": entry["repo_class_name"],
            "repo_class_version": entry["repo_class_version"],
            "instance_configuration": entry.get("instance_configuration", {}),
            "dependencies": entry.get("dependencies", {}),
        }
        for entry in bom
    ]
    logger.info(
        f"Creating 'local-changeset' changeset with {len(changeset_def)} entries"
    )
    _put_entity(
        base_url,
        "change_set",
        {
            "name": "local-changeset",
            "definition": changeset_def,
        },
    )

    # 5. Apply changeset (skip async — CLI will drive execution)
    logger.info("Applying changeset (skip_async=True)")
    result = _post_apiop(
        base_url,
        "apply_changeset",
        {
            "change_set_name": "local-changeset",
            "deployment_set_name": "local",
            "skip_async": True,
        },
    )
    csd_nid = result["csd_nid"]
    logger.info(f"ChangeSetDeployment created: {csd_nid}")

    # 6. Generate local deployment manifest (scripts in DAG order)
    logger.info("Generating local deployment manifest")
    manifest = _post_apiop(base_url, f"generate_local_deployment/{csd_nid}")
    nodes = manifest.get("nodes", [])
    logger.info(f"Got {len(nodes)} deployment nodes")

    return csd_nid, nodes
