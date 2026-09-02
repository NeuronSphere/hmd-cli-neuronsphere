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
import binascii
import importlib.util
import json
import logging
import os
import uuid
from typing import Any, Dict, List, NamedTuple, Optional, Tuple

import requests
from cement import minimal_logger

try:
    from importlib.metadata import entry_points
    from importlib.metadata import version as distribution_version
except ImportError:
    from importlib_metadata import entry_points
    from importlib_metadata import version as distribution_version

from .docker_credentials import local_docker_config_json
from .floci_deployer import DOCKER_NETWORK_NAME

logger = minimal_logger("bom_seeder")

# Entry-point group any installed plugin package (e.g. hmd-cli-plugin-ns-telemetry) can
# populate to contribute additional local BOM entries -- see `_collect_plugin_bom_entries`.
BOM_ENTRIES_ENTRY_POINT = "hmd_cli_neuronsphere.get_local_bom_entries"

# Set truthy to resolve every repo class's version from its local working tree
# instead of from the artifact bundled with its plugin package. Per-repo, use
# `HMD_LOCAL_VERSION_<REPO_CLASS>` -- see `_version_override`.
PREFER_LOCAL_VERSIONS_ENV = "HMD_LOCAL_NEURONSPHERE_PREFER_LOCAL_VERSIONS"
LOCAL_VERSION_ENV_PREFIX = "HMD_LOCAL_VERSION_"

# Extra `external/` directories to search for bundled artifacts, os.pathsep-separated.
# The escape hatch for plugin packages `_artifact_roots` cannot discover on its own.
ARTIFACT_ROOTS_ENV = "HMD_LOCAL_NEURONSPHERE_ARTIFACT_ROOTS"

# The entry-point groups a plugin package may register. Any one of them identifies
# the package as ours, and therefore its `external/` as an artifact root.
ARTIFACT_ENTRY_POINT_GROUPS = (
    "hmd_cli_neuronsphere.enabled",
    "hmd_cli_neuronsphere.prepare_hmd_home",
    "hmd_cli_neuronsphere.get_resources",
    "hmd_cli_neuronsphere.render_compose_yaml",
    BOM_ENTRIES_ENTRY_POINT,
)


class VersionResolution(NamedTuple):
    """A resolved repo class version and where it came from.

    ``source`` is one of ``pin``, ``local``, ``bundled``, ``declared``,
    ``local-fallback`` or ``default``; ``root`` is the directory holding the
    ``meta-data/`` the version was read from, when there is one.
    """

    version: str
    source: str
    root: Optional[str]


# repo_class_name -> (version, artifact_dir); built lazily by `_artifact_version_index`.
_ARTIFACT_VERSION_INDEX: Optional[Dict[str, Tuple[str, str]]] = None


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
    s3_bucket_bom_entry("project-bucket"),
    # hmd_cli_opa.deploy() uploads any repo's `src/opa-bundles/` to a bucket named
    # opa-bucket-hmd-inf-s3bucket-<deployment_id>-<environment>-<region>-<customer_code>
    # unconditionally when the opa CLI is installed (e.g. in the projectbuilder image);
    # without this entry that upload fails locally with "OPA Deploy Failed" (e.g.
    # deploying hmd-ms-transform).
    s3_bucket_bom_entry("opa-bucket"),
]

# The CLI itself is the RepoClass that owns every local default/core Resource
# (docker-network, k3s cluster, compute pool, …). A single owning RepoClass keeps
# the local Resource graph coherent and — crucially — lets SPEC0008 resource-type
# dependencies (e.g. ext-secrets depending on a kubernetes-cluster) resolve against
# a real, environment-scoped producing instance.
CORE_REPO_CLASS = "hmd-cli-neuronsphere"

# The single core instance name that produces the local core resource types. Repos
# with resource-type deps name this instance in their BOM `dependencies` map.
CORE_INSTANCE_NAME = "local-neuronsphere"

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
    {
        # hmd-inf-neptune declares producing this exact type (see its
        # meta-data/manifest.json deploy.resources). JanusGraph -- already part of
        # core, running unconditionally via docker-compose.graph.yml -- substitutes
        # for Neptune locally, so any repo's resource-typed dependency on a
        # graph-database (e.g. hmd-inf-trino.graph-db) resolves here instead of
        # requiring an (impossible) local Neptune deploy.
        "resource_namespace": "database.neuronsphere.io",
        "resource_definition_name": "graph-database",
        "version": "0.1.0",
        "role": "graph",
    },
    {
        # hmd-ms-deployment/hmd-ms-naming/hmd-ms-dbaccount/hmd-ms-artifact-lib are
        # bootstrapped-before-ms-deployment-exists Lambdas the CLI owns directly --
        # never registered as RepoClass/RepoInstance entities (their own manifests'
        # required deps, e.g. base-vpc/argo/datadog-lambda, will never resolve
        # locally). What they provide is represented purely as concrete
        # `application/microservice` Resources (see build_service_resources),
        # produced type-level by this same core instance so a resource-typed
        # dependency (e.g. hmd-database-account.create-service) can resolve against
        # them without needing those services to exist as RepoInstances.
        "resource_namespace": "application.neuronsphere.io",
        "resource_definition_name": "microservice",
        "version": "0.1.0",
        "role": "microservice",
    },
    {
        # Generic stand-in for "some network the instance lives in" -- satisfies a
        # repo's resource-typed dependency on a base network (e.g.
        # hmd-inf-hive-metastore.base-vpc) without requiring an actual hmd-vpc
        # deploy, which is never applicable locally (Docker networking replaces
        # it). Distinct from the concrete `network.neuronsphere.io/docker-network`
        # type above, which types the actual submitted Resource.
        "resource_namespace": "network.neuronsphere.io",
        "resource_definition_name": "network",
        "version": "0.1.0",
        "role": "network",
    },
    {
        # And the concrete `vpc` subtype, for repos whose base-vpc dependency
        # names it specifically rather than the abstract `network` above (e.g.
        # hmd-postgres-rds). Producing the parent type does not satisfy a
        # requirement for a child, so both have to be declared. Docker networking
        # substitutes for a VPC locally either way -- an hmd-vpc deploy is never
        # applicable.
        "resource_namespace": "network.neuronsphere.io",
        "resource_definition_name": "vpc",
        "version": "0.1.0",
        "role": "network",
    },
]

# BOM entry #0 — always prepended for Phase A (see seed_bom's two-phase callers in
# hmd_cli_neuronsphere.py). `apply_changeset` creates `local-neuronsphere` as a
# proper DEPLOY_NEXT environment instance of `hmd-cli-neuronsphere` (with all the
# env/instance/deployment/RCV edges); LocalWorkflowRunner hardcodes CORE_REPO_CLASS
# as a no-op node, so it's marked DEPLOYED without the runner executing anything.
# This environment's Postgres, deployed as a Floci RDS instance by the same
# RepoClass the cloud uses. `repo_instance` is unique by name *per Environment*,
# so the name is constant across environments (like CORE_INSTANCE_NAME) while
# each deploy lands in its own emulated AWS account.
ENV_DB_INSTANCE = "environment-db"
ENV_DB_REPO_CLASS = "hmd-postgres-rds"

LOCAL_CORE_BOM = [
    {
        "repo_instance_name": CORE_INSTANCE_NAME,
        "repo_class_name": CORE_REPO_CLASS,
        "deployment_id": "local",
        "instance_configuration": {},
        "dependencies": {},
    },
    {
        # Produces this environment's `database.neuronsphere.io/postgres`
        # Resource, which is what every `database-instance` dependency resolves
        # against (hmd-database-account's above all). It is in the *core*
        # changeset because ms-dbaccount and every plugin database depend on it.
        #
        # Every dependency the RepoClassVersion marks required must be supplied,
        # even though the local overlay (`src/local/cdktf/cdktf_local.py` in that
        # repo) replaces the Aurora-on-a-VPC stack with a plain aws_db_instance
        # and references none of them: ms-deployment validates required *roles*
        # at changeset apply, not at deploy ("required role, rds-loggroup, not
        # provided").
        #
        # `base-vpc` is resource-typed, so the core instance must genuinely
        # declare producing `network.neuronsphere.io/vpc` -- see
        # CORE_PRODUCED_DEFINITIONS. `datadog-lambda` and `rds-loggroup` are
        # name-only roles that nothing validates beyond presence; there is no
        # CloudWatch or Datadog locally, and the overlay creates neither.
        "repo_instance_name": ENV_DB_INSTANCE,
        "repo_class_name": ENV_DB_REPO_CLASS,
        "deployment_id": "local",
        "instance_configuration": {},
        "dependencies": {
            "base-vpc": CORE_INSTANCE_NAME,
            "datadog-lambda": CORE_INSTANCE_NAME,
            "rds-loggroup": CORE_INSTANCE_NAME,
        },
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
    # AWS_ACCESS_KEY_ID is rewritten per environment by _inject_floci_account():
    # one Floci serves every account and resolves which one from the 12-digit
    # access key, so a static key here would make every environment's External
    # Secrets operator read the *same* account's secrets.
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


# -- Deployment GUI ---------------------------------------------------------
#
# hmd-app-neuronsphere is the Django front end to hmd-ms-deployment: the same GUI
# the cloud platform serves, pointed at the local control plane so a local user
# browses environment BOMs and applies ChangeSets the way they would in the cloud.
#
# It is the control plane's own management surface rather than a platform
# workload, so it is *not* in any BOM: it runs as the `deployment-gui` service in
# `services/docker-compose.control-plane.yml`, beside hmd_db and floci, and its
# Postgres database is one of `floci_deployer.CORE_DATABASES`. What is left here
# is only what the rest of the CLI needs in order to talk about it.
GUI_DB_NAME = "deployment_gui"

# hmd_proxy is on the same Docker network as the GUI container, so the GUI reaches
# the control-plane ms-deployment route by container name.
GUI_DEPLOYMENT_API_URL = "http://hmd_proxy/hmd_ms_deployment"

# The host port hmd_proxy serves the GUI on. It is the spare slot of port slot 0
# (env_registry's DEFAULT_PORT_BASE + PORTS_PER_ENV - 1), already inside the range
# the proxy publishes, so no compose change is needed to reach it. A constant
# rather than an environment's `spare_port`: the GUI is a control-plane singleton
# serving every environment, not one instance per environment.
_DEFAULT_GUI_PORT = 19003


def gui_port() -> int:
    """The host port hmd_proxy serves the Deployment GUI at, at its root path."""
    raw = os.environ.get("HMD_LOCAL_GUI_HOST_PORT")
    if raw:
        try:
            return int(raw)
        except ValueError:
            logger.warning(
                f"HMD_LOCAL_GUI_HOST_PORT={raw!r} is not an integer; "
                f"falling back to {_DEFAULT_GUI_PORT}"
            )
    return _DEFAULT_GUI_PORT


def gui_enabled() -> bool:
    """True unless HMD_LOCAL_NEURONSPHERE_ENABLE_GUI is explicitly falsy."""
    return not _is_falsy(os.environ.get("HMD_LOCAL_NEURONSPHERE_ENABLE_GUI"))


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

    logger.debug(
        f"Loaded {len(filtered)} entries from {path} ({len(bom) - len(filtered)} image-only filtered)"
    )
    return filtered


def _repo_dir(repo_class_name: str, repo_path: Optional[str] = None) -> Optional[str]:
    """The working tree to read a repo's metadata from.

    ``repo_path`` (a manifest ``source.path``) wins over the
    ``$HMD_REPO_HOME/<repo_class_name>`` convention, so a repo instance declared
    from a tree outside HMD_REPO_HOME still resolves its own VERSION and
    manifest.json rather than silently falling back to a same-named repo that
    happens to sit in HMD_REPO_HOME.
    """
    if repo_path:
        return repo_path
    repo_home = os.environ.get("HMD_REPO_HOME", "")
    if not repo_home or not repo_class_name:
        return None
    return os.path.join(repo_home, repo_class_name)


def _local_tree_version(
    repo_class_name: str, repo_path: Optional[str] = None
) -> Optional[str]:
    """The ``meta-data/VERSION`` of the repo's working tree, or None."""
    repo_dir = _repo_dir(repo_class_name, repo_path)
    if not repo_dir:
        return None
    try:
        with open(os.path.join(repo_dir, "meta-data", "VERSION")) as f:
            return f.read().strip() or None
    except OSError:
        return None


def _local_version_env_var(repo_class_name: str) -> str:
    """The per-repo version-override variable name for ``repo_class_name``.

    ``hmd-inf-ext-secrets`` -> ``HMD_LOCAL_VERSION_HMD_INF_EXT_SECRETS``. Mirrors
    the per-plugin variable construction in
    :meth:`loaders.local_plugin_loader.LocalPluginLoader.is_plugin_enabled`.
    """
    normalized = (repo_class_name or "").upper().replace("-", "_").replace(".", "_")
    return f"{LOCAL_VERSION_ENV_PREFIX}{normalized}"


def _version_override(repo_class_name: str) -> Tuple[bool, Optional[str]]:
    """Resolve the version-override configuration for one repo class.

    :returns: ``(prefer_local, pinned_version)``.

    The per-repo variable carries both a *mode* and an optional literal pin, so
    the value is interpreted in this order:

    ``local``
        Use the working tree's VERSION.
    ``true``/``1``/``yes``/``on``
        Same as ``local`` -- these are not plausible version strings.
    ``false``/``0``/``no``/``bundled``/``artifact``
        Opt this one repo *out* of a global prefer-local. Without this the
        "prefer local everywhere except this repo" case is unexpressible.
    anything else
        A literal version pin, which wins over every other source.

    With the per-repo variable unset, the global
    ``HMD_LOCAL_NEURONSPHERE_PREFER_LOCAL_VERSIONS`` decides.
    """
    raw = (os.environ.get(_local_version_env_var(repo_class_name)) or "").strip()
    if raw:
        lowered = raw.lower()
        if lowered == "local" or _is_truthy(raw):
            return True, None
        if _is_falsy(raw) or lowered in {"bundled", "artifact"}:
            return False, None
        return False, raw
    return _is_truthy(os.environ.get(PREFER_LOCAL_VERSIONS_ENV)), None


def _artifact_roots() -> List[str]:
    """Every installed package's bundled ``external/`` artifact root.

    Discovery order (which root a duplicate is *reported* against; the winner
    is chosen by version -- see :func:`_artifact_version_index`):

    1. This package's own ``external/`` (the ``pre_build_artifacts`` unpacked at
       build time -- see ``meta-data/manifest.json``).
    2. The ``external/`` directory of every package registering a
       ``hmd_cli_neuronsphere.*`` entry point, sorted by package name. The
       owning module is located with :func:`importlib.util.find_spec` rather
       than ``entrypoint.load()`` on purpose: resolving a version must not
       execute third-party module bodies.
    3. Anything listed in ``HMD_LOCAL_NEURONSPHERE_ARTIFACT_ROOTS``
       (``os.pathsep``-separated).

    Step 2 does **not** find a plugin package that bundles artifacts without
    registering any ``hmd_cli_neuronsphere.*`` entry point, nor one that keeps
    its artifacts somewhere other than ``<package>/external`` (which is exactly
    why :func:`plugins.base.load_nsplugin_config` takes an ``external_dir``
    argument). Both cases are served by the environment variable.
    """
    roots: List[str] = [os.path.join(os.path.dirname(__file__), "external")]

    discovered: Dict[str, str] = {}
    for group in ARTIFACT_ENTRY_POINT_GROUPS:
        try:
            eps = entry_points(group=group)
        except Exception:  # pragma: no cover - importlib backport differences
            continue
        for ep in eps:
            root_pkg = (ep.value or "").split(":")[0].split(".")[0]
            if not root_pkg or root_pkg in discovered:
                continue
            try:
                spec = importlib.util.find_spec(root_pkg)
                locations = list(
                    getattr(spec, "submodule_search_locations", None) or []
                )
            except Exception as e:
                logger.debug(
                    f"Could not locate package '{root_pkg}' for artifacts: {e}"
                )
                continue
            if locations:
                discovered[root_pkg] = os.path.join(locations[0], "external")
    roots.extend(discovered[pkg] for pkg in sorted(discovered))

    extra = os.environ.get(ARTIFACT_ROOTS_ENV, "")
    roots.extend(p for p in (part.strip() for part in extra.split(os.pathsep)) if p)

    seen = set()
    unique = []
    for root in roots:
        resolved = os.path.realpath(root)
        if resolved in seen:
            continue
        seen.add(resolved)
        unique.append(root)
    return unique


def _version_sort_key(version: str) -> Tuple:
    """Order two bundled versions of the same repo class.

    A bundled version is a build number appended to a repo's MAJOR.MINOR
    (``0.2.70``, ``3.44.117``), so a component-wise numeric compare is the whole
    job. A non-numeric component sorts below any number rather than raising: an
    artifact nobody can order is not a reason to fail `up`.
    """
    return tuple(
        (1, int(part), "") if part.isdigit() else (0, 0, part)
        for part in (version or "").split(".")
    )


def _artifact_version_index() -> Dict[str, Tuple[str, str]]:
    """Map ``repo_class_name`` -> ``(version, artifact_dir)`` for bundled artifacts.

    Each child of an :func:`_artifact_roots` directory is a repo's unpacked
    build output, keyed by the ``name`` in its ``meta-data/manifest.json``.
    That name -- not the directory name -- is the repo class: the directories
    are named after the *plugin* (``ext-secrets``, ``apache_superset``) while
    the repo classes are ``hmd-inf-ext-secrets``, ``hmd-inf-superset``.

    When two roots bundle the same repo class, **the higher version wins**, not
    the earlier root. Ownership moves: a repo class this package once bundled
    can be handed to a plugin package, and until this package's own
    ``pre_build_artifacts`` pin is dropped it would otherwise keep serving a
    stale copy -- registering a RepoClassVersion whose ``manifest.json``
    dependencies no longer match the BOM entry that names its roles, which
    ``apply_changeset`` rejects. Only a checkout can shadow a newer artifact,
    and only when explicitly asked for (see :func:`resolve_repo_version`).

    An empty index is legitimate: ``external/`` is only populated by
    ``hmd build``, so a source checkout has none.
    """
    global _ARTIFACT_VERSION_INDEX
    if _ARTIFACT_VERSION_INDEX is not None:
        return _ARTIFACT_VERSION_INDEX

    index: Dict[str, Tuple[str, str]] = {}
    for root in _artifact_roots():
        try:
            children = sorted(os.listdir(root))
        except OSError:
            continue
        for child in children:
            artifact_dir = os.path.join(root, child)
            meta_dir = os.path.join(artifact_dir, "meta-data")
            try:
                with open(os.path.join(meta_dir, "VERSION")) as f:
                    version = f.read().strip()
            except OSError:
                continue
            if not version:
                continue

            key = None
            try:
                with open(os.path.join(meta_dir, "manifest.json")) as f:
                    key = (json.load(f) or {}).get("name")
            except (OSError, json.JSONDecodeError):
                pass
            if not key:
                key = child
                logger.debug(
                    f"Bundled artifact '{artifact_dir}' has no manifest.json name; "
                    f"indexing it under its directory name"
                )

            if key in index:
                existing_version, existing_dir = index[key]
                if existing_version == version:
                    continue
                found = (version, artifact_dir)
                existing = (existing_version, existing_dir)
                if _version_sort_key(version) > _version_sort_key(existing_version):
                    used, shadowed = found, existing
                else:
                    used, shadowed = existing, found
                logger.debug(
                    f"Bundled artifact '{key}' found twice: {used[0]} in "
                    f"{used[1]} (used) and {shadowed[0]} in {shadowed[1]} (shadowed)"
                )
                index[key] = used
                continue
            index[key] = (version, artifact_dir)

    _ARTIFACT_VERSION_INDEX = index
    return index


def _reset_artifact_version_index() -> None:
    """Drop the cached bundled-artifact index (roots are partly env-derived)."""
    global _ARTIFACT_VERSION_INDEX
    _ARTIFACT_VERSION_INDEX = None


def _core_distribution_version() -> Optional[str]:
    """This CLI's own installed distribution version.

    ``hmd-cli-neuronsphere`` *is* the installed package, so its distribution
    version is its artifact version -- there is no ``external/`` entry for it.
    """
    try:
        return distribution_version("hmd-cli-neuronsphere")
    except Exception:
        return None


def _warn_once(repo_class_name: str, warned_repos: Optional[set], message: str) -> None:
    """Log ``message`` at warning level, at most once per ``repo_class_name``.

    A BOM can name the same repo class in more than one entry (e.g. two
    ``s3_bucket_bom_entry`` instances both resolving ``hmd-inf-s3bucket``),
    which would otherwise repeat an identical warning once per entry. Pass
    the same set across every repo class in one pass to dedupe across it;
    omit to always warn, for a one-off lookup.
    """
    if warned_repos is not None:
        if repo_class_name in warned_repos:
            return
        warned_repos.add(repo_class_name)
    logger.warning(message)


def resolve_repo_version(
    repo_class_name: str,
    bom_version: Optional[str] = None,
    repo_path: Optional[str] = None,
    shadow_batch: Optional[Dict[str, Tuple[str, str]]] = None,
    warned_repos: Optional[set] = None,
) -> "VersionResolution":
    """Resolve a repo class's deployed version, artifact-first.

    Priority:

    0. ``HMD_LOCAL_VERSION_<REPO_CLASS>`` set to a literal version -- an
       explicit pin wins over everything.
    1. The working tree's ``meta-data/VERSION``, **only** when a local override
       is set (``HMD_LOCAL_VERSION_<REPO_CLASS>=local`` or
       ``HMD_LOCAL_NEURONSPHERE_PREFER_LOCAL_VERSIONS=true``).
    2. The bundled artifact shipped with the plugin package
       (``external/<dir>/meta-data/VERSION``, keyed by the manifest ``name``).
    3. ``bom_version`` -- the version declared by an environment manifest or a
       plugin's BOM contributor.
    4. The working tree's VERSION as a last resort, with a warning: a repo with
       neither a bundled artifact nor a declared version is better identified by
       its own tree than by the ``0.1.0`` sentinel.
    5. ``"0.1.0"``.

    A published artifact is a reproducible, resolvable version; a working tree
    is a work in progress. That is why the artifact wins by default and a
    developer must ask for their tree's version explicitly.

    :param shadow_batch: When resolving many repo classes in one pass (e.g. a
        BOM's worth), pass a shared dict here so a shadowed-local-tree warning
        (tiers 2/3, see :func:`_warn_shadowed_local`) is collected into it
        instead of logged immediately -- the caller then reports every
        shadowed repo in one combined warning via
        :func:`_flush_shadowed_local_warnings`. Omit for a one-off lookup,
        which logs immediately as before.
    :param warned_repos: Passed to :func:`_warn_once` for tiers 4 and 5 (no
        bundled artifact or declared version; VERSION not found at all). Pass
        the same set across every repo class in one BOM so a repo class named
        by more than one entry (e.g. two ``s3_bucket_bom_entry`` instances
        both resolving ``hmd-inf-s3bucket``) warns once, not once per entry.
        Omit to always warn, for a one-off lookup.
    :returns: the chosen version, which tier produced it, and the directory it
        came from (``None`` for a pin, a declared version or the sentinel), so
        callers can read the rest of the repo's metadata from the same place.
    """
    prefer_local, pinned = _version_override(repo_class_name)
    local_version = _local_tree_version(repo_class_name, repo_path)
    repo_dir = _repo_dir(repo_class_name, repo_path)

    if pinned:
        logger.debug(
            f"{repo_class_name}: pinned to {pinned} by "
            f"{_local_version_env_var(repo_class_name)}"
        )
        return VersionResolution(pinned, "pin", None)

    if prefer_local:
        if local_version:
            logger.debug(
                f"{repo_class_name}: using working-tree version {local_version} from "
                f"{repo_dir} (local version override)"
            )
            return VersionResolution(local_version, "local", repo_dir)
        logger.warning(
            f"{repo_class_name}: a local version override is set but no "
            f"meta-data/VERSION was found under {repo_dir}; falling back to the "
            f"bundled artifact"
        )

    if repo_class_name == CORE_REPO_CLASS:
        core_version = _core_distribution_version()
        if core_version:
            return VersionResolution(core_version, "bundled", None)

    bundled = _artifact_version_index().get(repo_class_name)
    if bundled:
        version, artifact_dir = bundled
        _warn_shadowed_local(
            repo_class_name,
            version,
            "bundled artifact",
            local_version,
            repo_dir,
            batch=shadow_batch,
        )
        return VersionResolution(version, "bundled", artifact_dir)

    if bom_version:
        _warn_shadowed_local(
            repo_class_name,
            bom_version,
            "declared",
            local_version,
            repo_dir,
            batch=shadow_batch,
        )
        return VersionResolution(bom_version, "declared", None)

    if local_version:
        _warn_once(
            repo_class_name,
            warned_repos,
            f"{repo_class_name}: no bundled artifact and no declared version; "
            f"falling back to the working tree's {local_version} from {repo_dir}",
        )
        return VersionResolution(local_version, "local-fallback", repo_dir)

    _warn_once(
        repo_class_name,
        warned_repos,
        f"VERSION not found for {repo_class_name}, using 0.1.0",
    )
    return VersionResolution("0.1.0", "default", None)


def _warn_shadowed_local(
    repo_class_name: str,
    chosen: str,
    source: str,
    local_version: Optional[str],
    repo_dir: Optional[str],
    batch: Optional[Dict[str, Tuple[str, str]]] = None,
) -> None:
    """Warn when the artifact version differs from a checked-out working tree.

    This is the case whose behaviour changed -- the tree used to win -- so it
    stays a warning, and names the way back, until the developer opts in.

    :param batch: When resolving many repo classes in one pass, collect into
        this dict instead of logging immediately -- see
        :func:`_flush_shadowed_local_warnings`, which reports the whole batch
        as one combined warning. Omit for a one-off lookup (e.g. resolving a
        single app's image version): not part of any batch, so this case
        logs at debug rather than warning -- a single instance is routine
        (most repos only get rebuilt occasionally) and not worth surfacing on
        every `up` the way an unreported *group* of shadowed repos would be.
    """
    if not local_version or local_version == chosen:
        return
    if batch is not None:
        batch[repo_class_name] = (chosen, local_version)
        return
    logger.debug(
        f"{repo_class_name}: deploying version {chosen} ({source}); the working tree "
        f"at {repo_dir} is {local_version}. Set "
        f"{_local_version_env_var(repo_class_name)}=local (or "
        f"{PREFER_LOCAL_VERSIONS_ENV}=true) to use the working-tree version."
    )


def _flush_shadowed_local_warnings(batch: Dict[str, Tuple[str, str]]) -> None:
    """Emit one combined warning for every repo class collected in ``batch``.

    Resolving a whole BOM this way turns what would otherwise be one warning
    per shadowed repo -- each repeating the same env-var instructions -- into
    a single line naming all of them, with the instructions stated once.
    """
    if not batch:
        return
    names = ", ".join(
        f"{name} ({chosen}, local {local})"
        for name, (chosen, local) in sorted(batch.items())
    )
    logger.warning(
        f"{len(batch)} repo(s) deploying a bundled/declared version that differs "
        f"from their local working tree: {names}. Set "
        f"{PREFER_LOCAL_VERSIONS_ENV}=true to use working-tree versions for all "
        f"of them, or HMD_LOCAL_VERSION_<REPO>=local per repo."
    )


def _get_repo_version(
    repo_class_name: str,
    bom_version: Optional[str] = None,
    repo_path: Optional[str] = None,
    shadow_batch: Optional[Dict[str, Tuple[str, str]]] = None,
    warned_repos: Optional[set] = None,
) -> str:
    """The version :func:`resolve_repo_version` chose for this repo class.

    :param shadow_batch: see :func:`resolve_repo_version`.
    :param warned_repos: see :func:`resolve_repo_version`.
    """
    return resolve_repo_version(
        repo_class_name,
        bom_version,
        repo_path,
        shadow_batch=shadow_batch,
        warned_repos=warned_repos,
    ).version


def repo_root_candidates(
    repo_class_name: str, repo_path: Optional[str] = None
) -> List[str]:
    """Directories a repo's code may deploy from, most-preferred first.

    The same rule that decides the version decides the code, so a deploy runs
    the build it is registered as: the bundled artifact first, and the working
    tree only under a local version override
    (``HMD_LOCAL_VERSION_<REPO_CLASS>=local`` or
    ``HMD_LOCAL_NEURONSPHERE_PREFER_LOCAL_VERSIONS``). Otherwise a developer's
    checkout would deploy under the artifact's version number, and the
    registered version would be a label rather than a fact.

    A *list* rather than one directory because callers need different things
    from a root -- Helm charts, a deploy script, a mountable workspace -- and
    the preferred root is not guaranteed to carry all of them. Each caller
    takes the first candidate that satisfies it.
    """
    prefer_local, _ = _version_override(repo_class_name)
    tree = _repo_dir(repo_class_name, repo_path)
    bundled = _artifact_version_index().get(repo_class_name)
    bundle_dir = bundled[1] if bundled else None
    order = [tree, bundle_dir] if prefer_local else [bundle_dir, tree]
    return [d for d in order if d and os.path.isdir(d)]


def _read_repo_manifest(
    repo_class_name: str,
    repo_path: Optional[str] = None,
    metadata_root: Optional[str] = None,
) -> Optional[Dict]:
    """Parse a repo's ``meta-data/manifest.json``, or None when unreadable.

    :param metadata_root: Read from this directory instead of the working tree
        -- the bundled artifact the version was resolved from, so a repo's
        dependencies and default_configuration describe the same build as its
        registered version.
    """
    repo_dir = metadata_root or _repo_dir(repo_class_name, repo_path)
    if not repo_dir:
        return None
    manifest_path = os.path.join(repo_dir, "meta-data", "manifest.json")
    try:
        with open(manifest_path) as f:
            return json.load(f)
    except (FileNotFoundError, NotADirectoryError, json.JSONDecodeError):
        return None


def _get_repo_deploy_config(
    repo_class_name: str,
    repo_path: Optional[str] = None,
    metadata_root: Optional[str] = None,
) -> Optional[Dict]:
    """Read default_configuration from the repo's manifest.json.

    Returns None if not found (caller should use BOM fallback).
    """
    manifest = _read_repo_manifest(repo_class_name, repo_path, metadata_root)
    if manifest is None:
        return None
    return manifest.get("deploy", {}).get("default_configuration", {})


def _get_repo_dependencies(
    repo_class_name: str,
    repo_path: Optional[str] = None,
    metadata_root: Optional[str] = None,
) -> Optional[Dict]:
    """Read deploy dependencies from the repo's manifest.json.

    Returns None if not found (caller should use BOM fallback).
    """
    manifest = _read_repo_manifest(repo_class_name, repo_path, metadata_root)
    if manifest is None:
        return None
    return manifest.get("deploy", {}).get("dependencies", {})


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


def _decode_collection(value) -> List:
    """Read back a ``collection`` attribute, however the CRUD layer served it.

    The inverse of :func:`_encode_collection`, tolerant of both shapes: ms-base
    transmits collections as base64-encoded JSON strings, but a native list is
    accepted too so this keeps working if that ever changes (and so tests can
    hand it a plain list).
    """
    if value is None:
        return []
    if isinstance(value, list):
        return value
    if isinstance(value, str):
        try:
            return json.loads(base64.b64decode(value).decode())
        except (ValueError, TypeError, binascii.Error):
            try:
                return json.loads(value)
            except ValueError:
                logger.warning("Could not decode a collection attribute")
                return []
    return []


def _raise_for_status(resp, what: str) -> None:
    """`raise_for_status`, but keep the body.

    ms-deployment reports *why* it refused in the response body (ms-base turns a
    ServiceException into ``{"message": ...}``), and `raise_for_status` throws
    all of it away -- so a changeset rejected for e.g. a role the registered
    RepoClassVersion does not declare surfaces as a bare "400 Client Error",
    which names neither the entry nor the role.
    """
    if resp.status_code < 400:
        return
    body = (resp.text or "").strip()
    raise requests.HTTPError(
        f"{what}: HTTP {resp.status_code}" + (f" - {body}" if body else ""),
        response=resp,
    )


def _put_entity(base_url: str, entity_type: str, data: Dict) -> Dict:
    """Create an entity via the CRUD PUT endpoint."""
    url = f"{base_url}/api/{entity_type}"
    resp = requests.put(url, json=data, timeout=30)
    _raise_for_status(resp, f"PUT {entity_type}")
    return resp.json()


def _search_entities(base_url: str, entity_type: str, filter_: Dict) -> List[Dict]:
    """Search entities via the ms-base CRUD POST (filter is the top-level body)."""
    url = f"{base_url}/api/{entity_type}"
    resp = requests.post(url, json=filter_, timeout=30)
    _raise_for_status(resp, f"search {entity_type}")
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
        logger.debug(f"{operation} idempotent skip: {resp.text}")
        return {}
    _raise_for_status(resp, operation)
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
            logger.debug(
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
    logger.debug(f"Seeded {len(seeded)} base resource definitions")
    return seeded


def build_service_resources(services: Optional[List[Dict]], env=None) -> List[Dict]:
    """Build ``application/microservice`` Resources for the seeded local services.

    Each entry is ``{"service_name", "repo_class_name", "api_base_url"}`` (derived
    from the HMDMS-service specs). The Resource is owned by the core
    ``hmd-cli-neuronsphere`` / ``local-neuronsphere`` instance, typed by the base
    ``application.neuronsphere.io/microservice`` definition, and tagged
    ``environment=<slug>`` plus ``repo_class=<name>`` so a consumer's dependency role
    (e.g. ``deployment-service`` -> ``hmd-ms-deployment``) can select the right one
    via ``find_resources_by_selector``. This is how a service-backed role resolves in
    a dev ``hmd deploy --local`` config -- the same tag-based path as the cluster/DB.
    """
    # `environment` carries the environment's slug -- its Environment.type, and
    # what a consumer's deploy is invoked with as `--environment`. Resource
    # queries scope by environment through `find_resources_by_selector`'s
    # separate `environment_type` argument, so this tag is descriptive; it is
    # kept accurate so a hand-written selector reads the same locally as in the
    # cloud.
    common_tags = [
        {"key": "environment", "value": env.slug if env is not None else "local"}
    ]
    if env is not None:
        common_tags = common_tags + [
            {"key": "deployment_id", "value": env.deployment_id}
        ]
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


def env_db_identifier(env) -> str:
    """The DBInstanceIdentifier this environment's Postgres node creates.

    Mirrors what the CDKTF overlay derives from ``HmdCdkTfStack.base_name``
    (``make_standard_name``, lowercased with hyphens). The CLI looks the instance
    up by this id to find its container and alias it, so the two derivations must
    agree -- a mismatch leaves the database running but unreachable by name.
    """
    from hmd_cli_tools.hmd_cli_tools import make_standard_name

    from .floci_deployer import local_customer_code

    base = make_standard_name(
        ENV_DB_INSTANCE,
        ENV_DB_REPO_CLASS,
        env.deployment_id,
        env.slug,
        os.environ.get("HMD_REGION", "reg1"),
        local_customer_code(),
    )
    return base.replace("_", "-").lower()


def build_local_core_resources(
    *,
    network_name: str = DOCKER_NETWORK_NAME,
    cluster_name: Optional[str] = None,
    cluster_endpoint: Optional[str] = None,
    services: Optional[List[Dict]] = None,
    env=None,
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

    When ``env`` is given, the Postgres and graph Resources point at *that*
    environment's own containers (each environment runs its own Postgres and
    JanusGraph, matching the cloud), and every Resource carries an extra
    ``deployment_id`` tag so selectors can target one environment. The instance
    name stays ``local-neuronsphere`` in every environment -- repo_instance is
    unique by name per Environment, so it is not ambiguous.
    """
    graph_host = env.graph_container if env is not None else "global-graph"

    # `environment` carries the environment's slug -- its Environment.type, and
    # what a consumer's deploy is invoked with as `--environment`. Resource
    # queries scope by environment through `find_resources_by_selector`'s
    # separate `environment_type` argument, so this tag is descriptive; it is
    # kept accurate so a hand-written selector reads the same locally as in the
    # cloud.
    common_tags = [
        {"key": "environment", "value": env.slug if env is not None else "local"}
    ]
    if env is not None:
        common_tags = common_tags + [
            {"key": "deployment_id", "value": env.deployment_id}
        ]
    # Every core Resource is owned by the single `hmd-cli-neuronsphere` RepoClass /
    # `local-neuronsphere` instance (created as BOM entry #0), so they all attach to that
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
        {
            # JanusGraph substitutes for Amazon Neptune locally. Satisfies any
            # repo's resource-typed dependency on
            # database.neuronsphere.io/graph-database (e.g. hmd-inf-trino.graph-db)
            # -- see CORE_PRODUCED_DEFINITIONS. Like Postgres, each environment
            # runs its own.
            "instance_name": CORE_INSTANCE_NAME,
            "repo_class_name": CORE_REPO_CLASS,
            "resource_name": "global-graph",
            "resource_definition": {
                "resource_namespace": "database.neuronsphere.io",
                "resource_definition_name": "graph-database",
                "version": "0.1.0",
            },
            "output": {"endpoint": f"ws://{graph_host}:8182/gremlin"},
            "tags": common_tags,
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
                # The class is `alb`, not `traefik`: local Traefik is configured to
                # answer to the cloud's ALB class (see
                # `k3s_operators._patch_traefik_manifest`) so charts render the same
                # Ingress in both places. Advertising `traefik` here would hand
                # consumers a class nothing serves.
                "output": {
                    "name": "traefik",
                    "namespace": "kube-system",
                    "ingress_class": "alb",
                },
                "tags": common_tags + [{"key": "cluster_type", "value": "k3s"}],
            }
        )
    # microservice Resources for the seeded HMDMS services (deployment-service etc.)
    resources.extend(build_service_resources(services, env))
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

    The owning RepoInstanceDeployment is the ``local-neuronsphere`` node created by the
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
            logger.debug(
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
    ``local-neuronsphere`` producing instance. Idempotent (``declare_produces`` is deduped
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
    logger.debug(f"Declared {declared} core produced resource definition(s)")
    return declared


def _deployment_matches_env(deployment: Dict, env) -> bool:
    """Whether a RepoInstanceDeployment belongs to ``env``.

    Instance names are identical across environments (repo_instance is unique by
    name *per Environment*), so a name-only lookup is ambiguous once more than
    one environment exists -- it would resolve to an arbitrary environment's
    instance and attach Resources to the wrong cluster. ``deployment_id`` is the
    discriminator we set on every BOM entry.

    Deployments that carry no ``deployment_id`` at all are treated as matching,
    so a graph seeded before this field was populated still resolves.
    """
    if env is None:
        return True
    did = deployment.get("deployment_id")
    return did is None or did == env.deployment_id


def find_core_deployment_node(base_url: str, env=None) -> List[Dict]:
    """Return the ``nodes`` shape for the existing ``local-neuronsphere`` deployment, or [].

    On a restart (the deployment graph already bootstrapped), the ``local-neuronsphere``
    RepoInstanceDeployment persists in ms-deployment, but we no longer have the
    ``nodes`` list :func:`seed_bom` returns. This looks that deployment up so
    :func:`submit_local_resources` can attach Resources to it *without* re-running
    ``seed_bom`` (which would create duplicate changesets):

        repo_instance(name == local-neuronsphere) --has--> repo_instance_deployment

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
        instance_nids = {i["identifier"] for i in instances}
        # The relationship search isn't assumed to filter by ref_from, so fetch all
        # has-deployment edges and filter client-side (the local graph is tiny).
        edges = _search_entities(
            base_url,
            "hmd_lang_deployment.repo_instance_has_repo_instance_deployment",
            {},
        )
        deployments = _search_entities(
            base_url, "hmd_lang_deployment.repo_instance_deployment", {}
        )
    except (requests.RequestException, ValueError, KeyError) as e:
        logger.warning(f"Could not resolve the '{CORE_INSTANCE_NAME}' deployment: {e}")
        return []

    # Every environment has its own `local-neuronsphere` instance, so narrow to
    # the deployments belonging to this one before picking the most recent.
    by_nid = {d.get("identifier"): d for d in deployments}
    owned = [
        e
        for e in edges
        if e.get("ref_from") in instance_nids
        and _deployment_matches_env(by_nid.get(e.get("ref_to"), {}), env)
    ]
    if not owned:
        return []
    # If the instance was redeployed, prefer the most-recent deployment.
    owned.sort(key=lambda e: e.get("_created", ""), reverse=True)
    return [{"instance_name": CORE_INSTANCE_NAME, "rid_nid": owned[0]["ref_to"]}]


def resync_local_resources(
    base_url: str,
    cluster_name: Optional[str],
    services: Optional[List[Dict]] = None,
    env=None,
) -> int:
    """Idempotently refresh the bootstrapped core Resources — no DAG, no re-seed.

    Safe to call on every ``up`` restart: it re-runs only the parts of the bootstrap
    that upsert/dedupe server-side, so a running env picks up newly-defined core
    Resources (e.g. a new ingress-controller) without a destructive rebuild:

    1. :func:`seed_base_resource_definitions` — refresh the base ResourceDefinition
       catalog (registers any new base types).
    2. :func:`declare_core_produces` — refresh the ``local-neuronsphere`` producer's
       produced-type declarations.
    3. :func:`build_local_core_resources` + :func:`submit_local_resources` against
       the *existing* ``local-neuronsphere`` deployment (found via
       :func:`find_core_deployment_node`).

    :returns: The number of Resources (re)submitted; 0 if the env isn't bootstrapped.
    """
    seed_base_resource_definitions(base_url)
    declare_core_produces(base_url)
    nodes = find_core_deployment_node(base_url, env)
    if not nodes:
        logger.debug(
            f"No existing '{CORE_INSTANCE_NAME}' deployment found; skipping local "
            "resource resync (run a full `up` first)."
        )
        return 0
    resources = build_local_core_resources(
        cluster_name=cluster_name, services=services, env=env
    )
    return submit_local_resources(base_url, resources, nodes)


def _repo_instance_status(base_url: str, env=None) -> Dict[str, Optional[str]]:
    """Map each RepoInstance's name to its most recent deployment's status.

    Mirrors :func:`find_core_deployment_node`'s most-recent-edge-by-``_created``
    pattern, generalized across every instance instead of just the core one, and
    scoped to ``env`` for the same reason: identical instance names exist in
    every environment, so an unscoped map would report another environment's
    deployment status.
    """
    instances = _search_entities(base_url, "hmd_lang_deployment.repo_instance", {})
    edges = _search_entities(
        base_url, "hmd_lang_deployment.repo_instance_has_repo_instance_deployment", {}
    )
    deployments = _search_entities(
        base_url, "hmd_lang_deployment.repo_instance_deployment", {}
    )
    by_nid = {d.get("identifier"): d for d in deployments}
    name_by_instance = {
        i.get("identifier"): i.get("name") for i in instances if i.get("name")
    }

    # Resolve per *name*, not per instance id. Every environment has its own
    # RepoInstance row under the same name, so a per-instance map would be
    # collapsed by name afterwards and the last row scanned would win --
    # including a sibling environment's row that this env-scoped pass correctly
    # found no deployment for, clobbering a real status with None.
    latest: Dict[str, Tuple[str, Optional[str]]] = {}
    for e in edges:
        deployment = by_nid.get(e.get("ref_to"), {})
        if not _deployment_matches_env(deployment, env):
            continue
        name = name_by_instance.get(e.get("ref_from"))
        if not name:
            continue
        created = e.get("_created", "")
        if name not in latest or created >= latest[name][0]:
            latest[name] = (created, deployment.get("status"))

    # Known instance names with no deployment in this environment stay present
    # with a None status, so callers can distinguish "never deployed here" from
    # "no such instance".
    status_by_name: Dict[str, Optional[str]] = {
        name: None for name in name_by_instance.values()
    }
    status_by_name.update({name: status for name, (_, status) in latest.items()})
    return status_by_name


def compute_new_bom_entries(
    base_url: str,
    bom: Optional[List[Dict]] = None,
    env=None,
    manifest=None,
) -> List[Dict]:
    """Resolved BOM entries not yet deployed in the local env.

    "Done" means a real deploy succeeded (status ``DEPLOYED``); everything else
    -- no record yet, or FAILED/SKIPPED -- stays eligible for a delta-apply
    retry. Lets a restart pick up entries newly contributed by a plugin
    installed or enabled after bootstrap, or retry one left unfinished by a
    prior attempt, without touching anything already deployed. Fail-safe: a
    query error returns ``[]`` (deploy nothing) rather than risking a redeploy
    of already-bootstrapped instances.
    """
    if bom is None:
        bom = _resolve_bom(env=env, manifest=manifest)
    try:
        status_by_name = _repo_instance_status(base_url, env)
    except (requests.RequestException, ValueError, KeyError) as e:
        logger.warning(f"Could not compute new BOM entries: {e}")
        return []

    new_entries = []
    for entry in bom:
        name = entry.get("repo_instance_name")
        status = status_by_name.get(name)
        if status == "DEPLOYED":
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
        logger.debug(
            "No local Docker credentials found -- private image pulls (e.g. "
            "ghcr.io/hmdlabs/*) from local k3s will fail until you `docker login`"
        )
        return
    for entry in targets:
        entry.setdefault("instance_configuration", {})[
            "docker_config_json"
        ] = docker_config_json


def _inject_floci_account(bom: List[Dict], env=None) -> None:
    """Point any ``hmd-inf-ext-secrets`` entry at ``env``'s emulated AWS account.

    Mutates ``bom`` in place. The External Secrets operator runs *inside* the
    environment's k3s cluster and reads from the single Floci, which resolves the
    account from the SigV4 access key id. Left at the shipped placeholder, every
    environment's operator would authenticate as the same account and resolve the
    control plane's secrets instead of its own -- silently, since the secret names
    are identical across environments.
    """
    targets = [e for e in bom if e.get("repo_class_name") == "hmd-inf-ext-secrets"]
    if not targets:
        return
    from .floci_deployer import control_plane_target, env_target

    account = (
        env_target(env) if env is not None else control_plane_target()
    ).access_key_id
    for entry in targets:
        config = entry.setdefault("instance_configuration", {})
        extra_env = [dict(v) for v in config.get("extraEnv", [])]
        for var in extra_env:
            if var.get("name") == "AWS_ACCESS_KEY_ID":
                var["value"] = account
        if extra_env:
            config["extraEnv"] = extra_env


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


def _topo_sort_bom(bom: List[Dict]) -> List[Dict]:
    """Order ``bom`` so every entry appears after every other entry it depends on.

    ``apply_changeset_to_environment`` (ms-deployment) processes changeset entries
    in list order and resolves each entry's ``dependencies`` values against
    ``repo_instance``s already added earlier in the *same* call -- it does not
    itself topologically sort. A single plugin's own BOM list is naturally
    written in dependency order, but ``_collect_plugin_bom_entries`` concatenates
    *different* plugins' contributions in whatever order ``entry_points()``
    happens to scan them, with no awareness of cross-plugin dependency edges
    (e.g. telemetry's ``otel-collector`` referencing analytics-engines'
    ``clickhouse``) -- if the referencing entry lands earlier in the merged list
    than the entry it points to, ``apply_changeset`` 500s with "No repo instance
    found for name, X". A stable topological sort (Kahn's algorithm, preserving
    input order among entries with no ordering constraint between them) fixes
    this generically for any current or future cross-plugin dependency, rather
    than requiring every plugin to guess at a safe concatenation order.
    """
    by_name = {e["repo_instance_name"]: e for e in bom if e.get("repo_instance_name")}
    indegree = {name: 0 for name in by_name}
    dependents: Dict[str, List[str]] = {name: [] for name in by_name}
    for name, entry in by_name.items():
        for dep_target in (entry.get("dependencies") or {}).values():
            if dep_target in by_name and dep_target != name:
                dependents[dep_target].append(name)
                indegree[name] += 1

    # Stable Kahn's algorithm: always pick the earliest-in-original-order
    # ready node, so entries with no ordering constraint keep their relative
    # input order (a plain sort key on original index, not a queue/heap).
    order = {name: i for i, name in enumerate(by_name)}
    ready = sorted([name for name, deg in indegree.items() if deg == 0], key=order.get)
    sorted_names: List[str] = []
    while ready:
        ready.sort(key=order.get)
        name = ready.pop(0)
        sorted_names.append(name)
        for dependent in dependents[name]:
            indegree[dependent] -= 1
            if indegree[dependent] == 0:
                ready.append(dependent)

    if len(sorted_names) != len(by_name):
        # A real cycle (or a self-referential edge missed above) -- fall back to
        # the original order rather than dropping entries; apply_changeset will
        # surface the same "not found" error, at least no worse than today.
        logger.warning("BOM dependency graph has a cycle; deploying in unsorted order")
        return bom

    return [by_name[name] for name in sorted_names]


def _call_bom_contributor(entrypoint, config: Optional[Dict]) -> List[Dict]:
    """Invoke one plugin's BOM contributor, with or without configuration.

    The historical contract is a **zero-arg** callable, and every plugin written
    against it must keep working. A contributor that wants the manifest's
    ``plugin_config`` block simply declares matching keyword parameters; if the
    call raises ``TypeError`` for an unexpected keyword we retry with no
    arguments, so passing config to a plugin that does not accept it is a no-op
    rather than an error.
    """
    fn = entrypoint.load()
    if config:
        try:
            return fn(**config) or []
        except TypeError as e:
            logger.debug(
                f"Plugin '{entrypoint.name}' does not accept configuration "
                f"({e}); calling it with no arguments"
            )
    return fn() or []


def available_bom_plugins() -> List[str]:
    """The entry-point names of every installed BOM-contributing plugin."""
    return sorted(ep.name for ep in entry_points(group=BOM_ENTRIES_ENTRY_POINT))


def _collect_plugin_bom_entries(
    enabled: Optional[set] = None,
    config: Optional[Dict[str, Dict]] = None,
) -> List[Dict]:
    """Collect BOM entries contributed by installed plugin packages.

    Each entry point in ``BOM_ENTRIES_ENTRY_POINT`` is a callable returning a list of
    BOM-entry dicts (see ``hmd_cli_plugin_ns_telemetry.bom`` for an example).
    Best-effort per contributor: a broken/misbehaving plugin package logs a warning and
    is skipped rather than blocking BOM resolution for everyone else.

    :param enabled: When given, only entry points whose name is in this set may
        contribute -- the manifest's ``plugins`` allow-list. ``None`` keeps the
        historical behaviour of accepting every installed contributor.
    :param config: Optional per-plugin configuration keyed by entry-point name.
    :raises ValueError: if ``enabled`` names a plugin that is not installed.
        Silently ignoring the typo would quietly shrink the desired state, and
        under ``--prune`` that means quietly destroying what it dropped.
    """
    config = config or {}
    entries: List[Dict] = []
    seen_names = set()

    for entrypoint in entry_points(group=BOM_ENTRIES_ENTRY_POINT):
        seen_names.add(entrypoint.name)
        if enabled is not None and entrypoint.name not in enabled:
            logger.debug(
                f"Plugin '{entrypoint.name}' is installed but not enabled for this "
                "environment; skipping its BOM entries"
            )
            continue
        try:
            contributed = _call_bom_contributor(entrypoint, config.get(entrypoint.name))
            if contributed:
                entries.extend(contributed)
        except Exception as e:
            logger.warning(
                f"Could not load local BOM entries from plugin '{entrypoint.name}': {e}"
            )

    if enabled is not None:
        missing = sorted(set(enabled) - seen_names)
        if missing:
            installed = ", ".join(sorted(seen_names)) or "(none)"
            raise ValueError(
                f"Environment manifest enables plugin(s) that are not installed: "
                f"{', '.join(missing)}. Installed BOM plugins: {installed}"
            )
    return entries


def scope_bom_entries(bom: List[Dict], env=None) -> List[Dict]:
    """Stamp every entry with the environment's ``deployment_id``.

    That is the *only* rewrite needed. Instance names and dependency targets are
    deliberately left alone: ``repo_instance`` is unique by name **per
    Environment**, and each named local environment owns its own Environment
    entity, so ``project-bucket`` in ``dev2`` is already a different instance
    than ``project-bucket`` in ``local``. Keeping the names identical is what
    gives cloud parity -- a repo's BOM entry reads the same in every
    environment.

    Returns copies; the module-level BOM constants are never mutated.
    """
    if env is None:
        return list(bom)
    scoped = []
    for entry in bom:
        item = dict(entry)
        item["deployment_id"] = env.deployment_id
        scoped.append(item)
    return scoped


def resolve_plugin_bom(env=None, manifest=None) -> List[Dict]:
    """Resolve the Phase B BOM: everything except the core ``LOCAL_CORE_BOM`` entry.

    When ``manifest`` is given (an :class:`env_manifest.EnvManifest`), resolution is
    delegated to :func:`change_set_builder.build_definition`, which additionally
    honours the manifest's plugin allow-list and its declared repo instances. With
    ``manifest=None`` this behaves exactly as it did before manifests existed.

    This is what ``seed_bom`` applies as the second of the two changesets a bootstrap
    submits (see the Phase A / Phase B split in ``hmd_cli_neuronsphere.py``) — the
    core ``local-neuronsphere`` instance (Phase A) already exists by the time this
    resolves, so entries here that reference ``CORE_INSTANCE_NAME`` by name (e.g.
    ext-secrets' ``eks-cluster``/``compute`` roles) resolve against it directly.

    Base source:
    - The built-in ``LOCAL_BOM`` constant is always the base.
    - If ``HMD_LOCAL_BOM_FILE`` is set, its entries are **merged into** ``LOCAL_BOM``
      (not replacing it): the file's entries are placed first so that, after the
      de-dupe below (keep-first by ``repo_instance_name``), a file entry overrides a
      built-in entry of the same instance name while every built-in entry the file
      does not mention (e.g. ``project-bucket``) is still retained.

    Augmentation (applied to the resolved base):
    - ``EXT_SECRETS_BOM`` is **appended** by default; set
      ``HMD_LOCAL_NEURONSPHERE_ENABLE_EXT_SECRETS=false`` to opt out.
    - Entries contributed by installed plugin packages (via ``BOM_ENTRIES_ENTRY_POINT``)
      are **appended** last.
    - Any ``hmd-inf-ext-secrets`` entry present (from either of the above) has the
      host's Docker credentials injected into its ``instance_configuration`` (see
      :func:`_inject_docker_credentials`), so its local CDKTF overlay can seed the
      ``hmd-docker-repo-secret`` k3s uses to pull private images.

    Augmented, in order, with ``EXT_SECRETS_BOM`` (unless opted out) and the
    entries contributed by installed plugin packages.

    The result is de-duped by ``repo_instance_name`` (so an explicit BOM file that
    already lists these entries stays idempotent) and topologically sorted (see
    :func:`_topo_sort_bom`) so cross-plugin dependency edges resolve regardless of
    entry_points() scan order.
    """
    if manifest is not None:
        from .change_set_builder import build_definition

        return build_definition(env=env, manifest=manifest)

    base = list(LOCAL_BOM)
    bom_file = os.environ.get("HMD_LOCAL_BOM_FILE")
    if bom_file:
        logger.debug(f"Merging BOM file into built-in LOCAL_BOM: {bom_file}")
        # File entries first so they win on repo_instance_name collision (the
        # keep-first _dedupe_bom below), while built-in entries the file omits
        # (e.g. project-bucket) are still retained -- a merge, not a replace.
        base = load_bom_from_file(bom_file) + base
    else:
        logger.debug("Using built-in LOCAL_BOM")

    bom = list(base)
    if not _is_falsy(os.environ.get("HMD_LOCAL_NEURONSPHERE_ENABLE_EXT_SECRETS")):
        logger.debug("ext-secrets enabled by default — appending EXT_SECRETS_BOM")
        bom = bom + EXT_SECRETS_BOM

    plugin_entries = _collect_plugin_bom_entries()
    if plugin_entries:
        logger.debug(
            f"Appending {len(plugin_entries)} BOM entrie(s) from installed plugins"
        )
        bom = bom + plugin_entries

    _inject_docker_credentials(bom)
    _inject_floci_account(bom, env)
    return scope_bom_entries(_topo_sort_bom(_dedupe_bom(bom)), env)


def _resolve_bom(env=None, manifest=None) -> List[Dict]:
    """The full resolved BOM: ``LOCAL_CORE_BOM`` (Phase A) + :func:`resolve_plugin_bom`
    (Phase B), combined. Used where callers need the complete desired-state list
    rather than a single phase (e.g. :func:`bom_includes_repo_class`,
    :func:`compute_new_bom_entries`'s delta detection, and the reconcile plan in
    :mod:`env_reconcile`).
    """
    bom = list(LOCAL_CORE_BOM) + resolve_plugin_bom(env=env, manifest=manifest)
    return scope_bom_entries(_topo_sort_bom(_dedupe_bom(bom)), env)


def bom_includes_repo_class(repo_class_name: str) -> bool:
    """True if the resolved local BOM includes an entry of the given repo class.

    Used to detect when an installed plugin's BOM contribution already owns a shared
    dependency (e.g. ext-secrets) so a hardcoded direct-install path elsewhere (see
    ``k3s_operators.py``) can step aside instead of installing it twice.
    """
    return any(e.get("repo_class_name") == repo_class_name for e in _resolve_bom())


def ensure_environment(base_url: str, env=None) -> Dict:
    """Idempotently create the Environment entity for a named local environment.

    Each named local environment gets its **own** ``hmd_lang_deployment.environment``
    row. That is what makes identical instance names safe across environments:
    ``repo_instance`` is unique by name *per Environment*, so ``project-bucket``
    in ``dev2`` is a different instance than the one in ``local``, and no BOM
    entry needs renaming.

    ``type`` **is** the environment's name -- it is the Environment's
    ``business_id`` and the only field ms-deployment resolves an environment by
    (``get_valid_environment``, ``apply_changeset``, ``destroy_deploymentset``
    all match on it, and the first two assert or index a single result). So a
    named local environment is typed by its slug, exactly as a cloud environment
    is typed ``dev`` or ``prod``. The default environment keeps ``type=local``
    because its slug *is* ``local``.

    ms-base ``PUT`` always takes the create branch (no upsert on business key),
    so find-first: reuse the matching env and only PUT when none exists. Without
    the guard, each call adds another row and ``get_valid_environment`` 500s.
    """
    slug = env.slug if env is not None else "local"
    account_number = env.account_id if env is not None else "000000000000"
    logger.debug(f"Ensuring '{slug}' environment exists")

    existing = _search_entities(
        base_url,
        "hmd_lang_deployment.environment",
        {"attribute": "type", "operator": "=", "value": slug},
    )
    if existing:
        return existing[0]

    payload = {
        "type": slug,
        "account_number": account_number,
        "hmd_region": os.environ.get("HMD_REGION", "us-west-2"),
    }
    return _put_entity(base_url, "hmd_lang_deployment.environment", payload)


def ensure_local_environment(base_url: str) -> Dict:
    """Backwards-compatible alias for :func:`ensure_environment` (default env)."""
    return ensure_environment(base_url)


def _deployment_set_definition(env_slug: str) -> List[Dict]:
    """The one-environment deployment set definition for ``env_slug``.

    A deployment set names its environments by ``Environment.type``, which for a
    local environment is its slug (see :func:`ensure_environment`). Hardcoding
    ``"local"`` here would make every named environment's changeset resolve to
    the default environment's graph.
    """
    return [
        {
            "environment": env_slug,
            "deployment_gate": {"transforms": [], "approval": False},
        },
    ]


def ensure_deployment_set(base_url: str, name: str, env_slug: str) -> Dict:
    """Idempotently create -- or repair -- an environment's DeploymentSet.

    The repair path exists because deployment sets written before environments
    were typed by name all say ``environment: "local"``. Such a row survives
    ``env delete``/``env create`` (the deployment graph lives in the shared
    control-plane Postgres, not in the environment's own state), so without this
    a recreated environment would keep applying its changesets to the default
    environment's graph -- silently, and with ``--prune`` destructively.

    :raises RuntimeError: if the row names the wrong environment and cannot be
        repaired, rather than proceeding against the wrong graph.
    """
    existing = _search_entities(
        base_url,
        "hmd_lang_deployment.deployment_set",
        {"attribute": "name", "operator": "=", "value": name},
    )
    definition = _deployment_set_definition(env_slug)

    if not existing:
        logger.debug(f"Creating '{name}' deployment set")
        return _put_entity(
            base_url,
            "hmd_lang_deployment.deployment_set",
            {"name": name, "definition": _encode_collection(definition)},
        )

    row = existing[0]
    environments = {
        d.get("environment")
        for d in _decode_collection(row.get("definition"))
        if isinstance(d, dict)
    }
    if environments == {env_slug}:
        return row

    logger.warning(
        f"Deployment set '{name}' targets {sorted(environments) or '(nothing)'} "
        f"but this environment is '{env_slug}'; repairing it."
    )
    payload = {
        "identifier": row.get("identifier"),
        "name": name,
        "definition": _encode_collection(definition),
    }
    try:
        _put_entity(base_url, "hmd_lang_deployment.deployment_set", payload)
    except requests.RequestException as e:
        raise RuntimeError(
            f"Deployment set '{name}' targets environment(s) "
            f"{sorted(environments)} instead of '{env_slug}', and could not be "
            f"updated ({e}). It predates environments being typed by name. "
            f"Recreate the local deployment graph with `hmd neuronsphere down "
            f"--purge` before starting '{env_slug}' again."
        ) from e

    # Re-read: a CRUD layer that ignored the identifier would have created a
    # second row instead of updating, which is worse than not trying.
    after = _search_entities(
        base_url,
        "hmd_lang_deployment.deployment_set",
        {"attribute": "name", "operator": "=", "value": name},
    )
    repaired = [
        r
        for r in after
        if {
            d.get("environment")
            for d in _decode_collection(r.get("definition"))
            if isinstance(d, dict)
        }
        == {env_slug}
    ]
    if len(after) != 1 or not repaired:
        raise RuntimeError(
            f"Deployment set '{name}' still does not target environment "
            f"'{env_slug}' after a repair attempt ({len(after)} row(s) found). "
            f"It predates environments being typed by name. Recreate the local "
            f"deployment graph with `hmd neuronsphere down --purge` before "
            f"starting '{env_slug}' again."
        )
    return repaired[0]


def _new_change_set_name(base_url: str, env_slug: str = "local") -> str:
    """A changeset name not already in use.

    ms-base PUT never upserts on business key, and ``apply_changeset`` asserts
    exactly one changeset matches the given name, so every :func:`seed_bom`
    invocation needs a name distinct from any prior one -- including one from a
    different environment, which is why the slug is part of the base name. Keeps
    the readable ``"<slug>-changeset"`` name on first use; later calls (e.g. a
    delta-apply on restart) get a unique suffix.
    """
    base = f"{env_slug}-changeset"
    existing = {
        c.get("name")
        for c in _search_entities(base_url, "hmd_lang_deployment.change_set", {})
    }
    if base not in existing:
        return base
    return f"{base}-{uuid.uuid4().hex[:8]}"


def seed_bom(
    base_url: str,
    bom: List[Dict] = None,
    env=None,
    repo_paths: Optional[Dict[str, str]] = None,
) -> Tuple[str, List[Dict]]:
    """Seed the deployment graph and return (csd_nid, nodes) for local execution.

    Steps:
    1. Register repo class versions via add_repo_class_version
    2. Create Environment, DeploymentSet, ChangeSet via CRUD PUT
    3. Apply changeset with skip_async=True
    4. Generate local deployment manifest (ordered scripts)

    :param base_url: ms-deployment base URL (e.g., http://localhost/hmd_ms_deployment)
    :param bom: Optional BOM override; defaults to HMD_LOCAL_BOM_FILE or LOCAL_BOM
    :param env: The named local environment being seeded. Selects the Environment
        entity, the deployment set and the changeset name, and stamps each entry's
        ``deployment_id``.
    :param repo_paths: Optional ``repo_class_name`` -> working-tree overrides for
        repos declared in a manifest with an explicit ``source.path``, so the
        tree consulted is the declared one rather than a same-named repo in
        ``HMD_REPO_HOME``. It selects *which* tree, not whether the tree wins:
        see :func:`resolve_repo_version`.
    :returns: Tuple of (csd_nid, nodes list from generate_local_deployment)
    """
    if bom is None:
        bom = _resolve_bom(env=env)
    else:
        bom = scope_bom_entries(bom, env)
    repo_paths = repo_paths or {}

    env_slug = env.slug if env is not None else "local"
    deployment_set_name = env_slug
    logger.debug(f"Seeding BOM with {len(bom)} entries for environment '{env_slug}'")

    # 1. Register repo class versions
    shadow_batch: Dict[str, Tuple[str, str]] = {}
    warned_no_version: set = set()
    for entry in bom:
        repo_name = entry["repo_class_name"]
        bom_version = entry.get("repo_class_version")
        repo_path = repo_paths.get(repo_name)
        resolution = resolve_repo_version(
            repo_name,
            bom_version=bom_version,
            repo_path=repo_path,
            shadow_batch=shadow_batch,
            warned_repos=warned_no_version,
        )
        version = resolution.version
        # Read the rest of the repo's metadata from wherever the version came
        # from: registering a bundled artifact's version alongside a working
        # tree's dependencies would describe a build that never existed.
        metadata_root = resolution.root if resolution.source == "bundled" else None

        # Use the resolved manifest data if available, fall back to BOM entry
        dependencies = _get_repo_dependencies(repo_name, repo_path, metadata_root)
        if dependencies is None:
            dependencies = entry.get("dependencies", {})

        default_config = _get_repo_deploy_config(repo_name, repo_path, metadata_root)
        if default_config is None:
            default_config = entry.get("instance_configuration", {})

        logger.debug(f"Adding repo class version: {repo_name}@{version}")
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

    logger.debug(f"Registered {len(bom)} repo class version(s)")
    _flush_shadowed_local_warnings(shadow_batch)

    # 1b. Declare that the core RepoClass (hmd-cli-neuronsphere) produces the local
    # core resource types, before the changeset applies, so resource-type
    # dependency validation resolves against the local-neuronsphere producing instance.
    declare_core_produces(base_url)

    # 2. Create this environment's Environment entity
    ensure_environment(base_url, env)

    # 3. Create DeploymentSet, one per named environment (find-first — ms-base
    # PUT never upserts on business key, so a second seed_bom call, e.g. a
    # delta-apply on restart, must not create a duplicate row).
    ensure_deployment_set(base_url, deployment_set_name, env_slug)

    # 4. Create ChangeSet. apply_changeset asserts exactly one changeset row with
    # the given name, so every call needs a name not already in use (a repeat
    # seed_bom call, e.g. a delta-apply on restart, would otherwise collide with
    # the first call's "local-changeset" row and fail that assertion).
    change_set_name = _new_change_set_name(base_url, env_slug)
    changeset_def = [
        {
            "deployment_id": entry.get("deployment_id", env_slug),
            "repo_instance_name": entry["repo_instance_name"],
            "repo_class_name": entry["repo_class_name"],
            "repo_class_version": entry["repo_class_version"],
            "instance_configuration": entry.get("instance_configuration", {}),
            "dependencies": entry.get("dependencies", {}),
        }
        for entry in bom
    ]
    logger.debug(
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
    logger.debug("Applying changeset (skip_async=True)")
    result = _post_apiop(
        base_url,
        "apply_changeset",
        {
            "change_set_name": change_set_name,
            "deployment_set_name": deployment_set_name,
            "skip_async": True,
        },
    )
    csd_nid = result["csd_nid"]
    logger.debug(f"ChangeSetDeployment created: {csd_nid}")

    # 6. Generate local deployment manifest (scripts in DAG order)
    logger.debug("Generating local deployment manifest")
    manifest = _post_apiop(base_url, f"generate_local_deployment/{csd_nid}")
    nodes = manifest.get("nodes", [])
    logger.debug(f"Got {len(nodes)} deployment nodes")

    return csd_nid, nodes


class DestroyCascadeError(RuntimeError):
    """A requested destroy would also take instances that are still declared."""


def plan_destroy(
    base_url: str, env=None, instance_names: List[str] = None
) -> List[str]:
    """The full set of instances a destroy of ``instance_names`` would remove.

    ``destroy_from`` is a *starting point*, not a list: ms-deployment walks the
    deployment DAG downwards from each named instance and destroys everything
    that depends on it (``DeploymentManager.destroy_from_instance``). Asking for
    the closure up front is what makes the cascade guard in
    :func:`destroy_instances` possible.

    :returns: The sorted instance names in this environment's destroy closure.
    """
    env_slug = env.slug if env is not None else "local"
    result = _post_apiop(
        base_url,
        "destroy_deploymentset",
        {
            "deployment_set_name": env_slug,
            "destroy_from": list(instance_names or []),
            "dry_run": True,
        },
    )
    # The dry run answers per environment type, which for a local environment is
    # its slug (see ensure_environment).
    if isinstance(result, dict):
        return sorted(result.get(env_slug, []))
    return sorted(result or [])


def destroy_instances(
    base_url: str,
    env=None,
    instance_names: List[str] = None,
    keep: Optional[set] = None,
) -> Tuple[Optional[str], List[Dict]]:
    """Prepare a local destroy and return ``(csd_nid, nodes)`` to execute.

    Mirrors :func:`seed_bom` for the teardown direction: prepare the graph
    server-side, then hand back the ordered nodes for
    :class:`local_workflow_runner.LocalWorkflowRunner` to run. The nodes come
    back in reverse dependency order with ``deploy --destroy`` scripts.

    :param instance_names: The instances to destroy from.
    :param keep: Instance names that must survive. If the destroy closure
        includes any of them, nothing is destroyed and
        :class:`DestroyCascadeError` is raised instead -- destroying a
        still-declared instance because something else was removed is exactly
        the surprise a reconcile must never spring on a developer.
    :returns: ``(csd_nid, nodes)``, or ``(None, [])`` when there is nothing to do.
    :raises DestroyCascadeError: if the closure would take a kept instance.
    """
    instance_names = [n for n in (instance_names or []) if n]
    if not instance_names:
        return None, []

    env_slug = env.slug if env is not None else "local"

    closure = plan_destroy(base_url, env, instance_names)
    if not closure:
        logger.debug("Destroy dry run reported nothing to destroy")
        return None, []

    collateral = sorted(set(closure) & set(keep or ()))
    if collateral:
        raise DestroyCascadeError(
            "Destroying "
            + ", ".join(sorted(instance_names))
            + " would also destroy "
            + ", ".join(collateral)
            + ", which the environment still declares. Remove those from the "
            "environment first, or keep the instance they depend on."
        )

    logger.debug(
        f"Destroying {len(closure)} instance(s) in '{env_slug}': {', '.join(closure)}"
    )
    result = _post_apiop(
        base_url,
        "destroy_deploymentset",
        {
            "deployment_set_name": env_slug,
            "destroy_from": instance_names,
            "dry_run": False,
            "skip_async": True,
        },
    )
    csd_nid = result.get("csd_nid")
    if not csd_nid:
        logger.warning(f"Destroy did not produce a ChangeSetDeployment: {result}")
        return None, []
    if result.get("message") == "No instances to destroy":
        return None, []

    manifest = _post_apiop(base_url, f"generate_local_deployment/{csd_nid}")
    nodes = manifest.get("nodes", [])
    logger.debug(f"Got {len(nodes)} destroy node(s)")
    return csd_nid, nodes
