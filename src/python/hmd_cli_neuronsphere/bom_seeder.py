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
import uuid
from typing import Any, Dict, List, Optional, Tuple

import requests
from cement import minimal_logger

try:
    from importlib.metadata import entry_points
except ImportError:
    from importlib_metadata import entry_points

from .docker_credentials import local_docker_config_json

logger = minimal_logger("bom_seeder")

# Entry-point group any installed plugin package (e.g. hmd-cli-plugin-ns-telemetry) can
# populate to contribute additional local BOM entries -- see `_collect_plugin_bom_entries`.
BOM_ENTRIES_ENTRY_POINT = "hmd_cli_neuronsphere.get_local_bom_entries"


def s3_bucket_bom_entry(instance_name: str) -> Dict[str, Any]:
    """A local BOM entry requesting a dedicated ``hmd-inf-s3bucket`` instance.

    Centralizes the shape of an S3-bucket-backed BOM entry so any plugin can request
    its own named bucket (e.g. ``clickhouse-storage``) without hardcoding
    ``hmd-inf-s3bucket``'s BOM-entry shape itself. The real bucket is created by that
    repo's own CDKTF (``make_standard_name``-derived name) once this entry deploys via
    the local DAG -- same mechanism ``LOCAL_BOM``'s ``project-bucket`` entry already
    proves out.
    """
    return {
        "repo_instance_name": instance_name,
        "repo_class_name": "hmd-inf-s3bucket",
        "deployment_id": "local",
        "instance_configuration": {},
        "dependencies": {},
    }


def credentials_bom_entry(instance_name: str) -> Dict[str, Any]:
    """A local BOM entry requesting a dedicated ``hmd-inf-credentials`` instance.

    Centralizes the shape of a credentials-backed BOM entry the same way
    :func:`s3_bucket_bom_entry` does for S3 buckets.
    """
    return {
        "repo_instance_name": instance_name,
        "repo_class_name": "hmd-inf-credentials",
        "deployment_id": "local",
        "instance_configuration": {},
        "dependencies": {},
    }


LOCAL_BOM = [
    {
        "repo_instance_name": "vpc",
        "repo_class_name": "hmd-vpc",
        "deployment_id": "local",
        "instance_configuration": {},
        "dependencies": {},
    },
    s3_bucket_bom_entry("project-bucket"),
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
    {
        # k3s ships Traefik enabled, so the local cluster already serves Ingress.
        # We declare the abstract kubernetes.neuronsphere.io/ingress-controller type
        # (not the AWS-specific aws-load-balancer-controller subtype) so cloud repos
        # with a SPEC0008 resource dependency on an ingress-controller resolve locally.
        "resource_namespace": "kubernetes.neuronsphere.io",
        "resource_definition_name": "ingress-controller",
        "version": "0.1.0",
        "role": "ingress-controller",
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

# In-network Floci endpoint (resolvable from pods once the CoreDNS record exists).
# Mirrors k3s_operators._FLOCI_INTERNAL_ENDPOINT's default -- not imported from there
# to avoid coupling bom_seeder to k3s_operators for one constant.
_FLOCI_INTERNAL_ENDPOINT = os.environ.get(
    "FLOCI_INTERNAL_ENDPOINT", "http://neuronsphere:4566"
)

# ext-secrets' Helm defaults (meta-data/manifest.json's default_configuration) assume
# cloud: ClusterSecretStores authenticate via IRSA (AssumeRoleWithWebIdentity against
# sts.<region>.amazonaws.com, unresolvable locally). k3s_operators._ext_secrets_passes()
# used to override this for local, but that function only runs via the legacy
# hardcoded _OPERATORS install path -- dead code once ext-secrets is part of the BOM
# (see bom_includes_repo_class), which is true by default now (opt out via
# HMD_LOCAL_NEURONSPHERE_ENABLE_EXT_SECRETS=false). Without this override, every
# ExternalSecret/ClusterExternalSecret (including hmd-docker-repo-secret) never syncs
# locally -- the store just sits at InvalidProviderConfig.
#
# dockerRepoSecret specifically must target `parameterStoreSecretStore`
# (aws-parameter-store / SSM), not `clusterSecretStore` (aws-secrets-manager): the local
# CDKTF overlay that provisions the secret's value
# (hmd-inf-ext-secrets/src/local/cdktf/cdktf_local.py) calls
# hmd_lib_secrets_backend.create_secret(), which -- despite its "Secrets Manager" naming
# in older comments -- *always* writes to SSM Parameter Store (see its own docstring:
# "Writes always go to Parameter Store SecureString"). A ClusterSecretStore reading from
# Secrets Manager would never find it ("Secret does not exist").
#
# No `aws_region` override needed here: hmd-cli-helm's `_set_local_standard_values`
# resolves it correctly (real AWS region via get_cloud_region(hmd_region), matching
# where CDKTF writes land) and applies it via `--set`, which unconditionally wins over
# any `-f values-file` content -- an `aws_region` key here would be silently clobbered.
_EXT_SECRETS_LOCAL_CONFIG: Dict[str, Any] = {
    "installCRDs": False,  # CRDs come from hmd-inf-ext-secrets-crds (a prior BOM entry)
    "clusterSecretStore": {
        "enabled": True,
        "local": True,  # static test creds against Floci instead of IRSA
        "name": "aws-secrets-manager",
    },
    "parameterStoreSecretStore": {
        "enabled": True,
        "local": True,  # static test creds against Floci instead of IRSA
        "name": "aws-parameter-store",
    },
    "dockerRepoSecret": {
        "enabled": True,
        "secretStoreName": "aws-parameter-store",  # create_secret() always writes here
    },
    "extraEnv": [
        {"name": "AWS_ENDPOINT_URL", "value": _FLOCI_INTERNAL_ENDPOINT},
        {"name": "AWS_ACCESS_KEY_ID", "value": "test"},
        {"name": "AWS_SECRET_ACCESS_KEY", "value": "test"},
    ],
}

# On by default; opt out via HMD_LOCAL_NEURONSPHERE_ENABLE_EXT_SECRETS=false. crds
# first (its RepoClass must exist before ext-secrets, which depends on it by class
# name). The eks-cluster / compute roles are satisfied by the local k3s producer via
# their manifests' SPEC0008 `resource` blocks.
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
        "instance_configuration": dict(_EXT_SECRETS_LOCAL_CONFIG),
        "dependencies": {
            "eks-cluster": CORE_INSTANCE_NAME,
            "compute": CORE_INSTANCE_NAME,
            "crds": "ext-secrets-crds",
        },
    },
]


def _is_truthy(value: Optional[str]) -> bool:
    return (value or "").strip().lower() in {"1", "true", "yes", "on"}


def _is_falsy(value: Optional[str]) -> bool:
    return (value or "").strip().lower() in {"0", "false", "no"}


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


def _search_entities(base_url: str, entity_type: str, filter_: Dict) -> List[Dict]:
    """Search entities via the ms-base CRUD POST (filter is the top-level body)."""
    url = f"{base_url}/api/{entity_type}"
    resp = requests.post(url, json=filter_, timeout=30)
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


def build_service_resources(services: Optional[List[Dict]]) -> List[Dict]:
    """Build ``application/microservice`` Resources for the seeded local services.

    Each entry is ``{"service_name", "repo_class_name", "api_base_url"}`` (derived
    from the HMDMS-service specs). The Resource is owned by the core
    ``hmd-cli-neuronsphere`` / ``local-k3s`` instance, typed by the base
    ``application.neuronsphere.io/microservice`` definition, and tagged
    ``environment=local`` plus ``repo_class=<name>`` so a consumer's dependency role
    (e.g. ``deployment-service`` -> ``hmd-ms-deployment``) can select the right one
    via ``find_resources_by_selector``. This is how a service-backed role resolves in
    a dev ``hmd deploy --local`` config -- the same tag-based path as the cluster/DB.
    """
    common_tags = [{"key": "environment", "value": "local"}]
    resources: List[Dict] = []
    for svc in services or []:
        service_name = svc.get("service_name")
        repo_class_name = svc.get("repo_class_name")
        if not service_name:
            continue
        resources.append(
            {
                "instance_name": CORE_INSTANCE_NAME,
                "repo_class_name": CORE_REPO_CLASS,
                "resource_name": f"local-service-{service_name}",
                "resource_definition": {
                    "resource_namespace": "application.neuronsphere.io",
                    "resource_definition_name": "microservice",
                    "version": "0.1.0",
                },
                "output": {
                    "api_base_url": svc.get(
                        "api_base_url", f"http://localhost/{service_name}"
                    )
                },
                "tags": common_tags
                + (
                    [{"key": "repo_class", "value": repo_class_name}]
                    if repo_class_name
                    else []
                ),
            }
        )
    return resources


def build_local_core_resources(
    *,
    network_name: str = "neuronsphere_default",
    cluster_name: Optional[str] = None,
    cluster_endpoint: Optional[str] = None,
    services: Optional[List[Dict]] = None,
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
        # The endpoint is the k3s API as reached from **inside** the
        # neuronsphere_default network (where the projectbuilder deploy runs), not
        # the host-published port. Floci names the k3s container `floci-eks-<cluster>`.
        # hmd-cli-helm reads this `endpoint` from the resolved kubernetes-cluster
        # Resource (NERD0006) and connects there — the Resource is the source of
        # truth for cluster addressing (auth stays environment-derived).
        cluster_output = {
            "cluster_name": cluster_name,
            "endpoint": cluster_endpoint or f"https://floci-eks-{cluster_name}:6443",
        }
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
        # The k3s cluster's built-in Traefik is the local ingress controller. Typed by
        # the abstract kubernetes.neuronsphere.io/ingress-controller base definition, it
        # satisfies any cloud repo's resource dependency on an ingress-controller.
        resources.append(
            {
                "instance_name": CORE_INSTANCE_NAME,
                "repo_class_name": CORE_REPO_CLASS,
                "resource_name": f"{cluster_name}-traefik",
                "resource_definition": {
                    "resource_namespace": "kubernetes.neuronsphere.io",
                    "resource_definition_name": "ingress-controller",
                    "version": "0.1.0",
                },
                # k3s runs Traefik as a Deployment named `traefik` in kube-system.
                # `name`/`namespace` satisfy the effective schema inherited from the
                # `deployment` base type; `ingress_class` is the ingress-controller field.
                "output": {
                    "name": "traefik",
                    "namespace": "kube-system",
                    "ingress_class": "traefik",
                },
                "tags": common_tags + [{"key": "cluster_type", "value": "k3s"}],
            }
        )
    # microservice Resources for the seeded HMDMS services (deployment-service etc.)
    resources.extend(build_service_resources(services))
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


def find_core_deployment_node(base_url: str) -> List[Dict]:
    """Return the ``nodes`` shape for the existing ``local-k3s`` deployment, or [].

    On a restart (the deployment graph already bootstrapped), the ``local-k3s``
    RepoInstanceDeployment persists in ms-deployment, but we no longer have the
    ``nodes`` list :func:`seed_bom` returns. This looks that deployment up so
    :func:`submit_local_resources` can attach Resources to it *without* re-running
    ``seed_bom`` (which would create duplicate changesets):

        repo_instance(name == local-k3s) --has--> repo_instance_deployment

    Returns ``[{"instance_name": CORE_INSTANCE_NAME, "rid_nid": <nid>}]`` (the shape
    :func:`_rid_for_instance` expects), or ``[]`` when the env isn't bootstrapped.
    Best-effort: any lookup error resolves to ``[]`` so the caller falls back to a
    full ``up``.
    """
    try:
        instances = _search_entities(
            base_url,
            "hmd_lang_deployment.repo_instance",
            {"attribute": "name", "operator": "=", "value": CORE_INSTANCE_NAME},
        )
        if not instances:
            return []
        instance_nid = instances[0]["identifier"]
        # The relationship search isn't assumed to filter by ref_from, so fetch all
        # has-deployment edges and filter client-side (the local graph is tiny).
        edges = _search_entities(
            base_url,
            "hmd_lang_deployment.repo_instance_has_repo_instance_deployment",
            {},
        )
    except (requests.RequestException, ValueError, KeyError) as e:
        logger.warning(f"Could not resolve the '{CORE_INSTANCE_NAME}' deployment: {e}")
        return []

    owned = [e for e in edges if e.get("ref_from") == instance_nid]
    if not owned:
        return []
    # If the instance was redeployed, prefer the most-recent deployment.
    owned.sort(key=lambda e: e.get("_created", ""), reverse=True)
    return [{"instance_name": CORE_INSTANCE_NAME, "rid_nid": owned[0]["ref_to"]}]


def resync_local_resources(
    base_url: str, cluster_name: Optional[str], services: Optional[List[Dict]] = None
) -> int:
    """Idempotently refresh the bootstrapped core Resources — no DAG, no re-seed.

    Safe to call on every ``up`` restart: it re-runs only the parts of the bootstrap
    that upsert/dedupe server-side, so a running env picks up newly-defined core
    Resources (e.g. a new ingress-controller) without a destructive rebuild:

    1. :func:`seed_base_resource_definitions` — refresh the base ResourceDefinition
       catalog (registers any new base types).
    2. :func:`declare_core_produces` — refresh the ``local-k3s`` producer's
       produced-type declarations.
    3. :func:`build_local_core_resources` + :func:`submit_local_resources` against
       the *existing* ``local-k3s`` deployment (found via
       :func:`find_core_deployment_node`).

    :returns: The number of Resources (re)submitted; 0 if the env isn't bootstrapped.
    """
    seed_base_resource_definitions(base_url)
    declare_core_produces(base_url)
    nodes = find_core_deployment_node(base_url)
    if not nodes:
        logger.info(
            f"No existing '{CORE_INSTANCE_NAME}' deployment found; skipping local "
            "resource resync (run a full `up` first)."
        )
        return 0
    resources = build_local_core_resources(cluster_name=cluster_name, services=services)
    return submit_local_resources(base_url, resources, nodes)


def _repo_instance_status(base_url: str) -> Dict[str, Optional[str]]:
    """Map each RepoInstance's name to its most recent deployment's status.

    Mirrors :func:`find_core_deployment_node`'s most-recent-edge-by-``_created``
    pattern, generalized across every instance instead of just the core one.
    """
    instances = _search_entities(base_url, "hmd_lang_deployment.repo_instance", {})
    edges = _search_entities(
        base_url, "hmd_lang_deployment.repo_instance_has_repo_instance_deployment", {}
    )
    deployments = _search_entities(
        base_url, "hmd_lang_deployment.repo_instance_deployment", {}
    )
    status_by_rid = {d.get("identifier"): d.get("status") for d in deployments}

    latest_rid_by_instance: Dict[str, str] = {}
    latest_created_by_instance: Dict[str, str] = {}
    for e in edges:
        inst = e.get("ref_from")
        created = e.get("_created", "")
        if created >= latest_created_by_instance.get(inst, ""):
            latest_created_by_instance[inst] = created
            latest_rid_by_instance[inst] = e.get("ref_to")

    status_by_name: Dict[str, Optional[str]] = {}
    for i in instances:
        name, nid = i.get("name"), i.get("identifier")
        if not name:
            continue
        rid = latest_rid_by_instance.get(nid)
        status_by_name[name] = status_by_rid.get(rid) if rid else None
    return status_by_name


# Strategies LocalWorkflowRunner never runs a real deploy script for -- it marks
# them DEPLOYED the instant it visits them (see local_workflow_runner.py). If
# LocalWorkflowRunner fails fast on an earlier node, though, one of these can be
# left at ms-deployment's un-visited default status ("SKIPPED") forever, even
# though re-attempting it would be a pure no-op. Mirrored here (not imported)
# because only the central-overrides tier is checked -- the per-repo
# nsplugin.json tier isn't worth a filesystem lookup just for this comparison.
_NO_OP_STRATEGIES = {"skip", "local_storage", "compose_substitute"}


def compute_new_bom_entries(
    base_url: str,
    bom: Optional[List[Dict]] = None,
    overrides: Optional[Dict] = None,
) -> List[Dict]:
    """Resolved BOM entries not yet done in the local env.

    "Done" means either a real deploy succeeded (status ``DEPLOYED``), or the
    entry's repo_class is configured with a no-op strategy (``_NO_OP_STRATEGIES``,
    matching ``local_workflow_runner.SKIP_STRATEGIES`` plus ``compose_substitute``)
    -- those never run a real deploy script, so any existing record for one is
    terminal even if it's stuck at "SKIPPED" from an earlier fail-fast run.
    Everything else -- no record yet, or FAILED/SKIPPED for a repo that IS meant
    to really deploy -- stays eligible for a delta-apply retry.

    Lets a restart pick up entries newly contributed by a plugin installed or
    enabled after bootstrap, or retry one left unfinished by a prior attempt,
    without touching anything already deployed. Fail-safe: a query error returns
    ``[]`` (deploy nothing) rather than risking a redeploy of already-bootstrapped
    instances. ``overrides`` should be the central ``local_overrides.json`` dict
    (e.g. from ``load_local_overrides()``) -- omitting it treats every entry as
    "default" strategy, which is conservative but will keep re-offering a true
    no-op entry that a fail-fast run left at "SKIPPED".
    """
    if bom is None:
        bom = _resolve_bom()
    overrides = overrides or {}
    try:
        status_by_name = _repo_instance_status(base_url)
    except (requests.RequestException, ValueError, KeyError) as e:
        logger.warning(f"Could not compute new BOM entries: {e}")
        return []

    new_entries = []
    for entry in bom:
        name = entry.get("repo_instance_name")
        status = status_by_name.get(name)
        strategy = overrides.get(entry.get("repo_class_name"), {}).get("strategy")
        if strategy in _NO_OP_STRATEGIES:
            if status is not None:
                continue
        elif status == "DEPLOYED":
            continue
        new_entries.append(entry)
    return new_entries


def _inject_docker_credentials(bom: List[Dict]) -> None:
    """Patch any ``hmd-inf-ext-secrets`` BOM entry with host Docker credentials.

    Mutates ``bom`` in place. No-op if the resolved BOM has no ext-secrets entry
    (skips the host filesystem read entirely), or if
    :func:`docker_credentials.local_docker_config_json` resolved nothing (e.g. the
    developer never ran ``docker login``) -- the local CDKTF overlay
    (``hmd-inf-ext-secrets/src/local/cdktf/cdktf_local.py``) treats a missing
    ``docker_config_json`` as "skip provisioning the secret," not an error, so
    private-image pulls simply keep failing until credentials are available.
    """
    targets = [e for e in bom if e.get("repo_class_name") == "hmd-inf-ext-secrets"]
    if not targets:
        return
    docker_config_json = local_docker_config_json()
    if not docker_config_json:
        logger.info(
            "No local Docker credentials found -- private image pulls (e.g. "
            "ghcr.io/hmdlabs/*) from local k3s will fail until you `docker login`"
        )
        return
    for entry in targets:
        entry.setdefault("instance_configuration", {})[
            "docker_config_json"
        ] = docker_config_json


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


def _collect_plugin_bom_entries() -> List[Dict]:
    """Collect BOM entries contributed by installed plugin packages.

    Each entry point in ``BOM_ENTRIES_ENTRY_POINT`` is a zero-arg callable returning a
    list of BOM-entry dicts (see ``hmd_cli_plugin_ns_telemetry.bom`` for an example).
    Best-effort per contributor: a broken/misbehaving plugin package logs a warning and
    is skipped rather than blocking BOM resolution for everyone else.
    """
    entries: List[Dict] = []
    for entrypoint in entry_points(group=BOM_ENTRIES_ENTRY_POINT):
        try:
            contributed = entrypoint.load()()
            if contributed:
                entries.extend(contributed)
        except Exception as e:
            logger.warning(
                f"Could not load local BOM entries from plugin '{entrypoint.name}': {e}"
            )
    return entries


def _resolve_bom() -> List[Dict]:
    """Resolve the BOM source and augment it with the local core + opt-in add-ons.

    Base source priority:
    1. HMD_LOCAL_BOM_FILE env var → load from file
    2. LOCAL_BOM constant (backward compat)

    Augmentation (applied to whichever base was resolved):
    - ``LOCAL_CORE_BOM`` is always **prepended** so the ``local-k3s`` producer
      instance (RepoClass ``hmd-cli-neuronsphere``) exists for resource-type deps.
    - ``EXT_SECRETS_BOM`` is **appended** by default; set
      ``HMD_LOCAL_NEURONSPHERE_ENABLE_EXT_SECRETS=false`` to opt out.
    - Entries contributed by installed plugin packages (via ``BOM_ENTRIES_ENTRY_POINT``)
      are **appended** last.
    - Any ``hmd-inf-ext-secrets`` entry present (from either of the above) has the
      host's Docker credentials injected into its ``instance_configuration`` (see
      :func:`_inject_docker_credentials`), so its local CDKTF overlay can seed the
      ``hmd-docker-repo-secret`` k3s uses to pull private images.

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
    if not _is_falsy(os.environ.get("HMD_LOCAL_NEURONSPHERE_ENABLE_EXT_SECRETS")):
        logger.info("ext-secrets enabled by default — appending EXT_SECRETS_BOM")
        bom = bom + EXT_SECRETS_BOM

    plugin_entries = _collect_plugin_bom_entries()
    if plugin_entries:
        logger.info(
            f"Appending {len(plugin_entries)} BOM entrie(s) from installed plugins"
        )
        bom = bom + plugin_entries

    _inject_docker_credentials(bom)
    return _dedupe_bom(bom)


def bom_includes_repo_class(repo_class_name: str) -> bool:
    """True if the resolved local BOM includes an entry of the given repo class.

    Used to detect when an installed plugin's BOM contribution already owns a shared
    dependency (e.g. ext-secrets) so a hardcoded direct-install path elsewhere (see
    ``k3s_operators.py``) can step aside instead of installing it twice.
    """
    return any(e.get("repo_class_name") == repo_class_name for e in _resolve_bom())


def ensure_local_environment(base_url: str) -> Dict:
    """Idempotently create the ``local`` Environment.

    Called early in ``up`` (before ``seed_hmdms_services``) so seeded services can be
    registered as env-linked instances, and again by :func:`seed_bom`. ms-base ``PUT``
    always takes the create branch (no upsert on business key), so find-first: reuse an
    existing ``type=local`` env and only PUT when none exists. Without the guard, each
    call adds another ``local`` env (two per bootstrap) and ``get_valid_environment``
    (which asserts exactly one) 500s.
    """
    logger.info("Ensuring 'local' environment exists")
    existing = _search_entities(
        base_url,
        "hmd_lang_deployment.environment",
        {"attribute": "type", "operator": "=", "value": "local"},
    )
    if existing:
        return existing[0]
    return _put_entity(
        base_url,
        "hmd_lang_deployment.environment",
        {
            "type": "local",
            "account_number": "000000000000",
            "hmd_region": os.environ.get("HMD_REGION", "us-west-2"),
        },
    )


def _new_change_set_name(base_url: str) -> str:
    """A changeset name not already in use.

    ms-base PUT never upserts on business key, and ``apply_changeset`` asserts
    exactly one changeset matches the given name, so every :func:`seed_bom`
    invocation needs a name distinct from any prior one. Keeps the readable
    ``"local-changeset"`` base name on first use; later calls (e.g. a
    delta-apply on restart) get a unique suffix.
    """
    base = "local-changeset"
    existing = {
        c.get("name")
        for c in _search_entities(base_url, "hmd_lang_deployment.change_set", {})
    }
    if base not in existing:
        return base
    return f"{base}-{uuid.uuid4().hex[:8]}"


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
    ensure_local_environment(base_url)

    # 3. Create DeploymentSet (find-first — ms-base PUT never upserts on business
    # key, so a second seed_bom call, e.g. a delta-apply on restart, must not
    # create a duplicate "local" row).
    existing_ds = _search_entities(
        base_url,
        "hmd_lang_deployment.deployment_set",
        {"attribute": "name", "operator": "=", "value": "local"},
    )
    if not existing_ds:
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

    # 4. Create ChangeSet. apply_changeset asserts exactly one changeset row with
    # the given name, so every call needs a name not already in use (a repeat
    # seed_bom call, e.g. a delta-apply on restart, would otherwise collide with
    # the first call's "local-changeset" row and fail that assertion).
    change_set_name = _new_change_set_name(base_url)
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
        f"Creating '{change_set_name}' changeset with {len(changeset_def)} entries"
    )
    _put_entity(
        base_url,
        "hmd_lang_deployment.change_set",
        {
            "name": change_set_name,
            "definition": _encode_collection(changeset_def),
        },
    )

    # 5. Apply changeset (skip async — CLI will drive execution)
    logger.info("Applying changeset (skip_async=True)")
    result = _post_apiop(
        base_url,
        "apply_changeset",
        {
            "change_set_name": change_set_name,
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
