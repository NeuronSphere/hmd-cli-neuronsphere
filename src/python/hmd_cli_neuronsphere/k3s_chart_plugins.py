"""
Deploy bundled NeuronSphere Helm charts onto the local Floci k3s cluster.

Every bundled local service is an ``hmd-inf-*``/``hmd-app-*``/``hmd-ms-*`` repo with its
own ``src/helm`` chart. For cloud parity we can run those charts on the Floci k3s cluster
(the same way ``hmd-inf-clickhouse`` was validated) instead of as Docker Compose containers.

This is **opt-in** and only runs when:
  * ``HMD_LOCAL_NEURONSPHERE_K3S_CHARTS`` is truthy, AND
  * ``hmd_cli_helm`` is importable (the deploy goes through ``hmd helm deploy --local``).

When enabled, ``provision_k3s_chart_plugins`` (called from ``hmd neuronsphere up`` right after
the operators are installed) seeds each chart's Floci fixtures (S3 buckets + Secrets Manager
secrets its ``ExternalSecret``s read), then deploys the chart to k3s in dependency order. The
compose render loop suppresses the container for any plugin in this registry (see
``is_k3s_chart_plugin``), so a converted service runs on k3s only.

Everything here is best-effort: a failure logs a warning and never aborts ``up``.
"""

import importlib.util
import json
import os
import subprocess
from pathlib import Path
from typing import Any, Dict, List, Optional

import boto3
from botocore.exceptions import ClientError
from cement import minimal_logger

from hmd_cli_tools.hmd_cli_tools import make_standard_name

from . import k3s_operators as ko
from .floci_deployer import (
    DOCKER_NETWORK_NAME,
    FLOCI_ENDPOINT,
    local_customer_code,
)

logger = minimal_logger("ns_k3s_charts")

_ENABLE_ENV = "HMD_LOCAL_NEURONSPHERE_K3S_CHARTS"

# Local identity used for every chart deploy (matches k3s_operators._standard_values
# and hmd_cli_helm._set_local_standard_values, and the ClusterSecretStore's region).
# Region and customer code come from hmd.env so this agrees with every other
# producer/consumer of make_standard_name -- a hardcoded value here would name
# secrets nothing else looks up (the failure mode commit b160612 fixed).
_DID = "local"
_ENV = "local"
_HMD_REGION = os.environ.get("HMD_REGION", "reg1")
_CUSTOMER = local_customer_code()
# The ClusterSecretStore (installed by k3s_operators with aws_region=local) queries this
# region, so every secret a chart's ExternalSecret reads MUST be seeded here.
_SEED_REGION = "local"

# Charts pin pods to the compute node group via a hardcoded nodeAffinity on the
# label ``hmdlabs.io/repo-instance-name=<dependencies.compute.instance_name>``. On
# single-node k3s there is no node group, so we label the live node with this value
# and every chart's config_local.json sets ``dependencies.compute.instance_name``
# to it, so those pods schedule.
_COMPUTE_INSTANCE = "local"


# Some charts hardcode a cloud StorageClass name (e.g. `default`, `gp2`, `gp3`) in
# their volumeClaimTemplates instead of templating it. Alias those names to the k3s
# `local-path` provisioner so those PVCs bind locally.
_STORAGE_CLASS_ALIASES = ["default", "gp2", "gp3"]


def _ensure_storage_classes() -> None:
    for name in _STORAGE_CLASS_ALIASES:
        manifest = (
            "apiVersion: storage.k8s.io/v1\n"
            "kind: StorageClass\n"
            f"metadata:\n  name: {name}\n"
            "provisioner: rancher.io/local-path\n"
            "reclaimPolicy: Delete\n"
            "volumeBindingMode: WaitForFirstConsumer\n"
        )
        subprocess.run(
            ["kubectl", "apply", "-f", "-"],
            input=manifest,
            capture_output=True,
            text=True,
        )


def _label_nodes_for_compute() -> None:
    """Label every Ready node so charts' compute nodeAffinity is satisfied locally."""
    result = subprocess.run(
        ["kubectl", "get", "nodes", "--no-headers"], capture_output=True, text=True
    )
    for line in result.stdout.splitlines():
        cols = line.split()
        if len(cols) >= 2 and cols[1] == "Ready":
            subprocess.run(
                [
                    "kubectl",
                    "label",
                    "node",
                    cols[0],
                    f"hmdlabs.io/repo-instance-name={_COMPUTE_INSTANCE}",
                    "--overwrite",
                ],
                capture_output=True,
                text=True,
            )


# ---------------------------------------------------------------------------
# Chart registry — ordered by dependency tier. Each converted plugin lists the
# Floci fixtures its chart needs. `secrets[*]` name is
# make_standard_name(instance, repo, did, local, reg1, _CUSTOMER) — optionally
# plus a `suffix` — to match the chart's ExternalSecret remoteRef.key; value is
# the JSON the ExternalSecret pulls keys from. Never spell a fixture name out in
# full: the customer code has to track `local_customer_code()`, and a literal
# silently drifts from whatever the seeding side wrote.
#
# ClickHouse and the OTEL collector ("telemetry") used to be hardcoded here too. Both
# are now deployed through the real DAG (BOM entries contributed by the optional
# hmd-cli-plugin-ns-telemetry package, see bom_seeder.BOM_ENTRIES_ENTRY_POINT) instead
# of this direct `hmd helm deploy --local` shortcut.
# ---------------------------------------------------------------------------
_CHART_PLUGINS: List[Dict[str, Any]] = [
    {
        "plugin_name": "redis",
        "repo": "hmd-inf-redis",
        "external_name": "redis",
        "instance_name": "redis",
        "buckets": [],
        "secrets": [
            {
                "instance": "redis",
                "repo": "hmd-inf-redis",
                "did": "local",
                "value": {"username": "redis", "password": "redispass"},
            }
        ],
        "node_port": None,
        "nginx_path": None,
    },
    {
        "plugin_name": "transform-broker",
        "repo": "hmd-inf-transform-broker",
        "external_name": "transform-broker",
        "instance_name": "broker",
        "buckets": [],
        "secrets": [
            {
                "instance": "broker",
                "repo": "hmd-inf-transform-broker",
                "did": "local",
                "value": {"username": "transform", "password": "transformpass"},
            }
        ],
        "node_port": None,
        "nginx_path": None,
    },
    {
        "plugin_name": "hive_metastore",
        "repo": "hmd-inf-hive-metastore",
        "external_name": "hive-metastore",
        "instance_name": "hive",
        "buckets": [],
        "secrets": [
            {
                # DB creds — the `metastore` DB already exists in the compose
                # Postgres (hmd_db); host is resolved to its container IP.
                # `hmd-db` (dash) is not a typo: it is the literal
                # `dependencies.db-credentials.dependencies.database-instance.
                # instance_name` in hmd-inf-hive-metastore's config_local.json,
                # which the chart's `awsDbSecretName` helper joins verbatim.
                "instance": "hmd-db",
                "repo": "hmd-postgres-base",
                "did": _DID,
                "suffix": "_metastore",
                "value": {"username": "postgres", "password": "admin", "port": "5432"},
                "host_from_container": "hmd_db",
            },
            {
                "instance": "hive-bucket",
                "repo": "hmd-inf-trino-store-access",
                "did": _DID,
                "suffix": "-bucketaccess",
                "value": {"S3_ACCESS_KEY": "test", "S3_ACCESS_SECRET": "test"},
            },
        ],
        "node_port": None,
        "nginx_path": None,
    },
]


def k3s_charts_enabled() -> bool:
    """True when chart-on-k3s deploys are opted in AND hmd-cli-helm is available."""
    if os.environ.get(_ENABLE_ENV, "false").lower() not in ("true", "1", "yes"):
        return False
    if importlib.util.find_spec("hmd_cli_helm") is None:
        logger.debug(
            "%s is set but hmd_cli_helm is not installed; running plugins as compose",
            _ENABLE_ENV,
        )
        return False
    return True


def is_k3s_chart_plugin(plugin_name: str) -> bool:
    """Whether ``plugin_name`` is converted to run on k3s (compose is suppressed)."""
    return any(c["plugin_name"] == plugin_name for c in _CHART_PLUGINS)


def _floci_client(service: str, env=None):
    """A Floci client scoped to ``env``'s account (control plane when None).

    Never ``$AWS_ACCESS_KEY_ID``: the fixtures seeded through this client are
    read by charts running in an environment's own cluster, so seeding them into
    the control plane's account (which is what a placeholder resolves to) leaves
    every one of those lookups failing on a name that exists -- in the wrong
    account. An ambient key is worse still, redirecting them into whichever
    account a developer's real credentials name.
    """
    from .floci_deployer import account_access_key

    account = account_access_key(env)
    return boto3.client(
        service,
        endpoint_url=FLOCI_ENDPOINT,
        region_name=_SEED_REGION,
        aws_access_key_id=account,
        aws_secret_access_key=account,
    )


def _seed_fixtures(chart: Dict[str, Any]) -> None:
    """Create the S3 buckets + Secrets Manager secrets the chart's templates expect."""
    buckets = chart.get("buckets", [])
    if buckets:
        s3 = _floci_client("s3")
        for bucket in buckets:
            try:
                s3.create_bucket(Bucket=bucket)
            except ClientError as e:
                code = e.response["Error"]["Code"]
                if code not in ("BucketAlreadyOwnedByYou", "BucketAlreadyExists"):
                    logger.warning(f"Could not create bucket {bucket}: {e}")

    secrets = chart.get("secrets", [])
    if secrets:
        sm = _floci_client("secretsmanager")
        for sec in secrets:
            # Secret name is built from make_standard_name plus an optional
            # `suffix` (some charts append a db_name/qualifier the standard-name
            # helper can't express, e.g. `_metastore`, `-bucketaccess`). A fully
            # explicit `name` is still honoured, but prefer the derived form so
            # the customer code stays tied to `local_customer_code()`.
            name = sec.get("name") or (
                make_standard_name(
                    sec["instance"],
                    sec["repo"],
                    sec["did"],
                    _ENV,
                    _HMD_REGION,
                    _CUSTOMER,
                )
                + sec.get("suffix", "")
            )
            value = dict(sec["value"])
            # DB-creds secrets carry a `host`; resolve it to the compose Postgres
            # container's IP so k3s pods (which can't resolve the compose hostname)
            # connect directly.
            container = sec.get("host_from_container")
            if container:
                ip = _container_ip(container)
                if ip:
                    value["host"] = ip
            payload = json.dumps(value)
            try:
                sm.create_secret(Name=name, SecretString=payload)
            except ClientError:
                try:
                    sm.put_secret_value(SecretId=name, SecretString=payload)
                except ClientError as e:
                    logger.warning(f"Could not seed secret {name}: {e}")


def _container_ip(container: str) -> Optional[str]:
    """IP of a compose container on the k3s Docker network (reachable from pods)."""
    fmt = (
        '{{with index .NetworkSettings.Networks "'
        + DOCKER_NETWORK_NAME
        + '"}}{{.IPAddress}}{{end}}'
    )
    result = subprocess.run(
        ["docker", "inspect", "-f", fmt, container], capture_output=True, text=True
    )
    ip = result.stdout.strip()
    return ip if result.returncode == 0 and ip else None


# Resources whose fields get server-side-apply ownership from a controller
# (ESO normalizes ExternalSecret.spec.refreshInterval; KEDA owns ScaledObject
# fields; an HPA owns the target Deployment's .spec.replicas via the scale
# subresource). On a helm-4 re-deploy those cause SSA "conflict"s, so we delete
# them first and let helm recreate them fresh — surgical, unlike a full uninstall
# (which churns StatefulSet pods). StatefulSets/Deployments upgrade in place.
_OPERATOR_CR_TYPES = ["externalsecret", "scaledobject", "horizontalpodautoscaler"]


def _prepare_namespace_for_redeploy(namespace: str) -> None:
    """If the namespace already exists (a prior deploy), clear operator-owned CRs
    so the next ``helm upgrade`` doesn't hit a server-side-apply field conflict."""
    ns = subprocess.run(
        ["kubectl", "get", "namespace", namespace], capture_output=True, text=True
    )
    if ns.returncode != 0:
        return  # first deploy — nothing to clean
    ko._wait_namespace_not_terminating(namespace)
    for cr_type in _OPERATOR_CR_TYPES:
        subprocess.run(
            [
                "kubectl",
                "delete",
                cr_type,
                "--all",
                "-n",
                namespace,
                "--ignore-not-found",
                "--timeout=60s",
            ],
            capture_output=True,
            text=True,
        )


def _deploy_chart(chart: Dict[str, Any]) -> bool:
    """Deploy one chart to k3s via ``hmd helm deploy --local`` (best-effort)."""
    repo_root = ko._resolve_repo_root(
        {"repo": chart["repo"], "name": chart["external_name"]}
    )
    if not repo_root:
        logger.warning(
            f"Chart '{chart['plugin_name']}' ({chart['repo']}) not found in "
            f"HMD_REPO_HOME or bundled artifacts; skipping"
        )
        return False

    config_local = repo_root / "meta-data" / "config_local.json"
    if not config_local.exists():
        logger.warning(
            f"Chart '{chart['plugin_name']}' has no meta-data/config_local.json; skipping"
        )
        return False

    ko._ensure_chart_dependencies(repo_root / "src" / "helm")

    namespace = f"{chart['instance_name']}-{_DID}"
    _prepare_namespace_for_redeploy(namespace)

    env = {
        **os.environ,
        "HMD_CUSTOMER_CODE": _CUSTOMER,
        "HMD_REGION": _HMD_REGION,
        "HMD_ENVIRONMENT": _ENV,
    }
    # --no-atomic: apply and return without wait/rollback. Workload charts often
    # depend on cross-chart setup (bootstrap tables, other services) that isn't
    # ready at deploy time; --atomic would roll the whole release back. Pods stay
    # and retry as their dependencies come up.
    command = [
        "hmd",
        "helm",
        "deploy",
        "--local",
        "--no-atomic",
        "-cf",
        "meta-data/config_local.json",
        "-in",
        chart["instance_name"],
        "-di",
        _DID,
    ]
    logger.debug(
        f"Deploying chart '{chart['plugin_name']}' to k3s: {' '.join(command)}"
    )
    result = subprocess.run(
        command, cwd=str(repo_root), env=env, capture_output=True, text=True
    )
    if result.returncode != 0:
        logger.warning(
            f"Chart '{chart['plugin_name']}' deploy failed (exit={result.returncode}):\n"
            f"{result.stdout[-1500:]}\n{result.stderr[-1500:]}"
        )
        return False
    return True


def provision_k3s_chart_plugins(
    local_loader=None,
    plugins: Optional[Dict[str, bool]] = None,
    resources: Optional[Dict[str, Any]] = None,
) -> None:
    """Seed fixtures and deploy every enabled converted chart to k3s, in order.

    Requires ``KUBECONFIG`` to point at the Floci k3s cluster and the operators
    (``provision_k3s_operators``) to have run first.
    """
    if not k3s_charts_enabled():
        return
    if not os.environ.get("KUBECONFIG"):
        logger.debug("KUBECONFIG not set; skipping k3s chart deploys")
        return

    ko._wait_for_node_ready()
    _label_nodes_for_compute()
    _ensure_storage_classes()

    deployed = []
    for chart in _CHART_PLUGINS:
        if plugins is not None and not plugins.get(chart["plugin_name"], True):
            continue
        try:
            _seed_fixtures(chart)
            if _deploy_chart(chart):
                deployed.append(chart["plugin_name"])
        except Exception as e:  # best-effort: never abort `up`
            logger.warning(f"Chart '{chart['plugin_name']}' provisioning error: {e}")
    if deployed:
        logger.debug(f"Deployed k3s chart plugins: {', '.join(deployed)}")
