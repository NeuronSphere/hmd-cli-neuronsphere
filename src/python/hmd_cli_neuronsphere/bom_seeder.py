"""BOM seeder for local extend mode.

Seeds the ms-deployment service with repo class versions, environment,
deployment set, and changeset entities, then applies the changeset
(with skip_async=True) so the CLI can drive execution locally.

Supports loading the BOM from:
1. An explicit ``bom`` parameter
2. A JSON file via ``HMD_LOCAL_BOM_FILE`` environment variable
3. A minimal built-in ``LOCAL_BOM`` (for backward compat / testing)
"""

import base64
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

# The CLI itself is the RepoClass that owns every local default/core Resource
# (docker-network, k3s cluster, compute pool, …). A single owning RepoClass keeps
# the local Resource graph coherent and — crucially — lets SPEC0008 resource-type
# dependencies (e.g. ext-secrets depending on a kubernetes-cluster) resolve against
# a real, environment-scoped producing instance.
CORE_REPO_CLASS = "hmd-cli-neuronsphere"

# The single core instance name that produces the local core resource types. Repos
# with resource-type deps name this instance in their BOM `dependencies` map.
CORE_INSTANCE_NAME = "local-k3s"

# The base ResourceDefinition types the core RepoClass produces. Declared (via
# declare_produces_resource_definition) after the RCV is registered and before the
# changeset applies, so resource-type dependency validation passes.
CORE_PRODUCED_DEFINITIONS = [
    {
        "resource_namespace": "kubernetes.neuronsphere.io",
        "resource_definition_name": "kubernetes-cluster",
        "version": "0.1.0",
        "role": "kubernetes-cluster",
    },
    {
        "resource_namespace": "compute.neuronsphere.io",
        "resource_definition_name": "compute-node",
        "version": "0.1.0",
        "role": "compute",
    },
    {
        "resource_namespace": "network.neuronsphere.io",
        "resource_definition_name": "docker-network",
        "version": "0.1.0",
        "role": "network",
    },
]

# BOM entry #0 — always prepended. Deployed via the `skip` strategy (see
# local_overrides.json), so `apply_changeset` creates `local-k3s` as a proper
# DEPLOY_NEXT environment instance of `hmd-cli-neuronsphere` (with all the
# env/instance/deployment/RCV edges) without the runner executing anything.
LOCAL_CORE_BOM = [
    {
        "repo_instance_name": CORE_INSTANCE_NAME,
        "repo_class_name": CORE_REPO_CLASS,
        "deployment_id": "local",
        "instance_configuration": {},
        "dependencies": {},
    },
]

# Opt-in via HMD_LOCAL_NEURONSPHERE_ENABLE_EXT_SECRETS. crds first (its RepoClass
# must exist before ext-secrets, which depends on it by class name). The eks-cluster
# / compute roles are satisfied by the local k3s producer via their manifests'
# SPEC0008 `resource` blocks.
EXT_SECRETS_BOM = [
    {
        "repo_instance_name": "ext-secrets-crds",
        "repo_class_name": "hmd-inf-ext-secrets-crds",
        "deployment_id": "local",
        "instance_configuration": {},
        "dependencies": {
            "eks-cluster": CORE_INSTANCE_NAME,
            "compute": CORE_INSTANCE_NAME,
        },
    },
    {
        "repo_instance_name": "ext-secrets",
        "repo_class_name": "hmd-inf-ext-secrets",
        "deployment_id": "local",
        "instance_configuration": {},
        "dependencies": {
            "eks-cluster": CORE_INSTANCE_NAME,
            "compute": CORE_INSTANCE_NAME,
            "crds": "ext-secrets-crds",
        },
    },
]


def _is_truthy(value: Optional[str]) -> bool:
    return (value or "").strip().lower() in {"1", "true", "yes", "on"}


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


def _encode_collection(value) -> str:
    """Encode a ``collection``/mapping attribute for the CRUD PUT endpoint.

    hmd_ms_base transmits ``collection`` (and mapping/blob) attributes as a
    base64-encoded JSON string: the FastAPI request model types the field as
    ``str`` (so a native list is rejected 422 "Input should be a valid string")
    and the deserializer base64-decodes it (so a plain JSON string fails base64
    decoding). ``deployment_set.definition`` and ``change_set.definition`` are
    both ``collection`` attributes.
    """
    return base64.b64encode(json.dumps(value).encode()).decode()


def _put_entity(base_url: str, entity_type: str, data: Dict) -> Dict:
    """Create an entity via the CRUD PUT endpoint."""
    url = f"{base_url}/api/{entity_type}"
    resp = requests.put(url, json=data, timeout=30)
    resp.raise_for_status()
    return resp.json()


def _post_apiop(
    base_url: str, operation: str, payload: Dict = None, tolerate_exists: bool = False
) -> Dict:
    """Call an apiop endpoint.

    When ``tolerate_exists`` is set, a 400 whose body indicates the resource
    already exists (e.g. ``add_repo_class_version`` returns
    "RepoClass, X, already has version Y.") is treated as an idempotent
    success rather than an error. The same repo class can be registered by
    both the HMDMS seeder (as a mocked dependency) and the BOM seeder, so
    re-registration must be a no-op.
    """
    url = f"{base_url}/apiop/{operation}"
    if payload is not None:
        resp = requests.post(url, json=payload, timeout=60)
    else:
        resp = requests.post(url, timeout=60)
    if tolerate_exists and resp.status_code == 400 and "already" in resp.text.lower():
        logger.info(f"{operation} idempotent skip: {resp.text}")
        return {}
    resp.raise_for_status()
    return resp.json()


def upsert_repo_resource_definitions(base_url: str, repo_class_name: str) -> int:
    """Register a repo's declared ResourceDefinitions from meta-data/resources/*.yaml.

    Mirrors the cloud ArtifactMonitor's ``_ingest_resource_definitions``: each YAML
    doc under ``$HMD_REPO_HOME/<repo>/meta-data/resources/`` is upserted (idempotent)
    so the definition exists before the repo's deploy submits a concrete Resource of
    that type (``submit_resources`` types the Resource against it and would 400 if the
    definition were missing). Best-effort; parents must already exist (base catalog is
    seeded first). No-op when ``HMD_REPO_HOME`` is unset or the repo has no resources.

    :returns: The number of ResourceDefinitions upserted.
    """
    repo_home = os.environ.get("HMD_REPO_HOME", "")
    if not repo_home:
        return 0
    import glob

    res_dir = os.path.join(repo_home, repo_class_name, "meta-data", "resources")
    paths = sorted(
        glob.glob(os.path.join(res_dir, "*.yaml"))
        + glob.glob(os.path.join(res_dir, "*.yml"))
    )
    if not paths:
        return 0

    try:
        import yaml
    except ImportError:
        logger.warning(
            "PyYAML not available; skipping ResourceDefinition upsert for "
            f"{repo_class_name}"
        )
        return 0

    count = 0
    for path in paths:
        try:
            with open(path) as f:
                doc = yaml.safe_load(f)
            if not doc:
                continue
            payload = {
                "resource_namespace": doc["resource_namespace"],
                "resource_definition_name": doc["resource_definition_name"],
                "version": doc["version"],
            }
            for key in ("description", "resource_metadata", "output_schema", "parent"):
                if doc.get(key) is not None:
                    payload[key] = doc[key]
            _post_apiop(
                base_url, "upsert_resource_definition", payload, tolerate_exists=True
            )
            count += 1
            logger.info(
                f"Upserted ResourceDefinition {payload['resource_namespace']}/"
                f"{payload['resource_definition_name']} from {repo_class_name}"
            )
        except Exception as e:
            logger.warning(f"Failed to upsert ResourceDefinition from {path}: {e}")
    return count


def seed_base_resource_definitions(base_url: str) -> List[Dict]:
    """Seed the standard base ResourceDefinition catalog (NERD0004).

    Calls the ms-deployment ``seed_base_resource_definitions`` apiop, which
    idempotently upserts the abstract, vendor-neutral resource supertypes bundled
    with the service. Running this before the BOM seeder means per-repo concrete
    definitions (which ``parent`` these base types) resolve cleanly. Idempotent —
    safe on every ``hmd ns up``.

    :param base_url: ms-deployment base URL (e.g., http://localhost/hmd_ms_deployment)
    :returns: The list of seeded resource-definition identities.
    """
    result = _post_apiop(
        base_url, "seed_base_resource_definitions", tolerate_exists=True
    )
    seeded = result if isinstance(result, list) else []
    logger.info(f"Seeded {len(seeded)} base resource definitions")
    return seeded


def build_local_core_resources(
    *,
    network_name: str = "neuronsphere_default",
    cluster_name: Optional[str] = None,
    cluster_endpoint: Optional[str] = None,
) -> List[Dict]:
    """The registry of concrete Resources the local core actually provides.

    Each entry is submitted (via :func:`submit_local_resources`) as a NERD0004
    ``Resource`` typed by the matching **base** ResourceDefinition (the
    ``*.neuronsphere.io`` catalog seeded by
    :func:`seed_base_resource_definitions`) and tagged ``environment=local``. This
    is what gives local↔cloud parity: because ``docker-network isa network`` and
    the k3s cluster is the generic ``kubernetes-cluster`` type, a cloud repo whose
    ``manifest.json`` declares a resource dependency on
    ``network.neuronsphere.io/network`` or
    ``kubernetes.neuronsphere.io/kubernetes-cluster`` is satisfied by the local
    environment.

    The registry starts with the Docker network and (when present) the k3s
    cluster — the two with the biggest parity win; DBs / buckets / services can be
    added incrementally.
    """
    common_tags = [{"key": "environment", "value": "local"}]
    # Every core Resource is owned by the single `hmd-cli-neuronsphere` RepoClass /
    # `local-k3s` instance (created as BOM entry #0), so they all attach to that
    # instance's RepoInstanceDeployment (see submit_local_resources).
    resources: List[Dict] = [
        {
            "instance_name": CORE_INSTANCE_NAME,
            "repo_class_name": CORE_REPO_CLASS,
            "resource_name": network_name,
            "resource_definition": {
                "resource_namespace": "network.neuronsphere.io",
                "resource_definition_name": "docker-network",
                "version": "0.1.0",
            },
            "output": {"network_name": network_name, "driver": "bridge"},
            "tags": common_tags + [{"key": "platform", "value": "local"}],
        },
    ]
    if cluster_name:
        cluster_output = {"cluster_name": cluster_name}
        if cluster_endpoint:
            cluster_output["endpoint"] = cluster_endpoint
        resources.append(
            {
                "instance_name": CORE_INSTANCE_NAME,
                "repo_class_name": CORE_REPO_CLASS,
                "resource_name": cluster_name,
                "resource_definition": {
                    "resource_namespace": "kubernetes.neuronsphere.io",
                    "resource_definition_name": "kubernetes-cluster",
                    "version": "0.1.0",
                },
                "output": cluster_output,
                "tags": common_tags + [{"key": "cluster_type", "value": "k3s"}],
            }
        )
        # A concrete compute-node Resource for the cluster's node pool. Not required
        # for resource-dep validation (the `produces` edge alone satisfies a
        # selector-less dep) but submitted for completeness / discovery.
        resources.append(
            {
                "instance_name": CORE_INSTANCE_NAME,
                "repo_class_name": CORE_REPO_CLASS,
                "resource_name": f"{cluster_name}-compute",
                "resource_definition": {
                    "resource_namespace": "compute.neuronsphere.io",
                    "resource_definition_name": "compute-node",
                    "version": "0.1.0",
                },
                "output": {"node_group_name": f"{cluster_name}-compute"},
                "tags": common_tags + [{"key": "cluster_type", "value": "k3s"}],
            }
        )
    return resources


def _rid_for_instance(nodes: Optional[List[Dict]], instance_name: str) -> Optional[str]:
    """Return the RepoInstanceDeployment nid for a deployed node by instance name."""
    for node in nodes or []:
        if node.get("instance_name") == instance_name:
            return node.get("rid_nid")
    return None


def submit_local_resources(
    base_url: str, resources: List[Dict], nodes: Optional[List[Dict]] = None
) -> int:
    """Submit the concrete local core Resources into ms-deployment (NERD0004).

    The owning RepoInstanceDeployment is the ``local-k3s`` node created by the
    changeset as an instance of the ``hmd-cli-neuronsphere`` RepoClass (BOM entry
    #0); its nid is looked up from the deployed ``nodes``. Each Resource from
    :func:`build_local_core_resources` is POSTed (typed by its base definition,
    tagged for discovery) to the ``submit_resources`` apiop against that nid.
    Per-resource best-effort: a failure is logged and never aborts ``hmd ns up``.

    :returns: The number of Resources successfully submitted.
    """
    rid_nid = _rid_for_instance(nodes, CORE_INSTANCE_NAME)
    if not rid_nid:
        logger.warning(
            f"No deployed '{CORE_INSTANCE_NAME}' node found; skipping local core "
            "resource submission."
        )
        return 0

    submitted = 0
    for spec in resources:
        name = spec.get("resource_name")
        try:
            _post_apiop(
                base_url,
                "submit_resources",
                {
                    "repo_instance_deployment_id": rid_nid,
                    "resources": [
                        {
                            "resource_name": name,
                            "resource_definition": spec["resource_definition"],
                            "output": spec.get("output", {}),
                            "tags": spec.get("tags", []),
                        }
                    ],
                },
            )
            submitted += 1
            logger.info(
                f"Submitted local resource '{name}' "
                f"({spec['resource_definition']['resource_definition_name']})"
            )
        except Exception as e:
            logger.warning(f"Failed to submit local resource '{name}': {e}")
    return submitted


def _find_repo_class_version_id(
    base_url: str, repo_class_name: str, version: Optional[str] = None
) -> Optional[str]:
    """Resolve a RepoClassVersion identifier via the find_repo_class_versions apiop.

    ``find_repo_class_versions`` returns the versions ascending; when ``version``
    isn't matched we take the highest (last).
    """
    result = _post_apiop(base_url, f"find_repo_class_versions/{repo_class_name}")
    rcvs = result if isinstance(result, list) else []
    if not rcvs:
        return None
    if version:
        for rcv in rcvs:
            if rcv.get("version") == version:
                return rcv.get("identifier")
    return rcvs[-1].get("identifier")


def declare_core_produces(base_url: str) -> int:
    """Declare that the core RepoClass produces the local core resource types.

    Run after the ``hmd-cli-neuronsphere`` RepoClassVersion is registered and
    **before** ``apply_changeset`` so that resource-type dependencies (e.g. an
    ext-secrets repo requiring a ``kubernetes-cluster``) validate against the
    ``local-k3s`` producing instance. Idempotent (``declare_produces`` is deduped
    server-side) and best-effort.

    :returns: The number of produce declarations recorded.
    """
    rcv_id = _find_repo_class_version_id(base_url, CORE_REPO_CLASS)
    if not rcv_id:
        logger.warning(
            f"RepoClassVersion for {CORE_REPO_CLASS} not found; cannot declare "
            "core produced resource definitions."
        )
        return 0

    declared = 0
    for d in CORE_PRODUCED_DEFINITIONS:
        try:
            _post_apiop(
                base_url,
                "declare_produces_resource_definition",
                {
                    "repo_class_version_id": rcv_id,
                    "resource_definition": {
                        "resource_namespace": d["resource_namespace"],
                        "resource_definition_name": d["resource_definition_name"],
                        "version": d["version"],
                    },
                    "role": d["role"],
                },
            )
            declared += 1
        except Exception as e:
            logger.warning(
                f"Failed to declare core produces for "
                f"{d['resource_namespace']}/{d['resource_definition_name']}: {e}"
            )
    logger.info(f"Declared {declared} core produced resource definition(s)")
    return declared


def _dedupe_bom(bom: List[Dict]) -> List[Dict]:
    """Drop later entries whose ``repo_instance_name`` was already seen (idempotent)."""
    seen = set()
    result: List[Dict] = []
    for entry in bom:
        name = entry.get("repo_instance_name")
        if name in seen:
            continue
        seen.add(name)
        result.append(entry)
    return result


def _resolve_bom() -> List[Dict]:
    """Resolve the BOM source and augment it with the local core + opt-in add-ons.

    Base source priority:
    1. HMD_LOCAL_BOM_FILE env var → load from file
    2. LOCAL_BOM constant (backward compat)

    Augmentation (applied to whichever base was resolved):
    - ``LOCAL_CORE_BOM`` is always **prepended** so the ``local-k3s`` producer
      instance (RepoClass ``hmd-cli-neuronsphere``) exists for resource-type deps.
    - When ``HMD_LOCAL_NEURONSPHERE_ENABLE_EXT_SECRETS`` is truthy, ``EXT_SECRETS_BOM``
      is **appended**.

    The result is de-duped by ``repo_instance_name`` so an explicit BOM file that
    already lists these entries stays idempotent.
    """
    bom_file = os.environ.get("HMD_LOCAL_BOM_FILE")
    if bom_file:
        logger.info(f"Loading BOM from file: {bom_file}")
        base = load_bom_from_file(bom_file)
    else:
        logger.info("Using built-in LOCAL_BOM (2 entries)")
        base = list(LOCAL_BOM)

    bom = list(LOCAL_CORE_BOM) + base
    if _is_truthy(os.environ.get("HMD_LOCAL_NEURONSPHERE_ENABLE_EXT_SECRETS")):
        logger.info("ext-secrets opt-in enabled — appending EXT_SECRETS_BOM")
        bom = bom + EXT_SECRETS_BOM

    return _dedupe_bom(bom)


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
            tolerate_exists=True,
        )
        # Store resolved version back into entry for changeset
        entry["repo_class_version"] = version

        # Register any ResourceDefinitions the repo declares (meta-data/resources/*.yaml)
        # so a concrete Resource of that type can be typed at deploy/submit time.
        upsert_repo_resource_definitions(base_url, repo_name)

    # 1b. Declare that the core RepoClass (hmd-cli-neuronsphere) produces the local
    # core resource types, before the changeset applies, so resource-type
    # dependency validation resolves against the local-k3s producing instance.
    declare_core_produces(base_url)

    # 2. Create Environment
    logger.info("Creating 'local' environment")
    _put_entity(
        base_url,
        "hmd_lang_deployment.environment",
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
        "hmd_lang_deployment.deployment_set",
        {
            "name": "local",
            "definition": _encode_collection(
                [
                    {
                        "environment": "local",
                        "deployment_gate": {"transforms": [], "approval": False},
                    },
                ]
            ),
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
        "hmd_lang_deployment.change_set",
        {
            "name": "local-changeset",
            "definition": _encode_collection(changeset_def),
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
