"""
Install NeuronSphere cluster operators onto the local Floci k3s cluster.

The cloud EKS cluster gets its operators (External Secrets, KEDA, the ClickHouse
operator, cert-manager, ...) from dedicated ``hmd-inf-*`` Helm repos. For local
cloud-parity we install those *same* charts onto the Floci k3s cluster so that
chart repos deployed with ``hmd helm deploy --local`` render and apply their
``ExternalSecret`` / ``ScaledObject`` / ``ClickHouseCluster`` resources unchanged.

The charts are bundled into this CLI as ``pre_build_artifacts`` (unzipped under
``external/<name>``) or resolved from a checked-out repo in ``HMD_REPO_HOME``.
Each operator chart carries its base values in ``meta-data/manifest.json``'s
``deploy.default_configuration`` (there is no committed ``values.yaml``), exactly
as the cloud ``hmd helm deploy`` path consumes it — so we dump that to a values
file and ``helm upgrade --install`` with it, layering a local overlay where the
cloud config assumes AWS (IRSA, real endpoints).

This runs right after the k3s cluster becomes ready (KUBECONFIG set), is gated by
``HMD_LOCAL_NEURONSPHERE_ENABLE_K3S_OPERATORS`` (default on), and is best-effort:
a failure logs a warning and never aborts ``hmd neuronsphere up``.
"""

import json
import os
import subprocess
import time
from pathlib import Path
from tempfile import NamedTemporaryFile
from typing import Any, Dict, List, Optional

import yaml
from cement import minimal_logger

from .plugins.base import _external_dir

logger = minimal_logger("ns_k3s_operators")

_ENABLE_ENV = "HMD_LOCAL_NEURONSPHERE_ENABLE_K3S_OPERATORS"

_DID = "local"

# Pods running inside k3s (behind flannel + CoreDNS) cannot resolve the
# ``neuronsphere`` Docker network alias out of the box. ``_ensure_coredns_floci_entry``
# adds a CoreDNS record so pods resolve ``neuronsphere``/``neuronsphere-workload``
# to Floci's IP — then charts use the exact same in-network hostname as the cloud
# (full parity), and pods reach Floci by egressing through the k3s node.
_FLOCI_CONTAINER = os.environ.get("HMD_LOCAL_FLOCI_CONTAINER", "floci")
_FLOCI_WORKLOAD_CONTAINER = os.environ.get(
    "HMD_LOCAL_FLOCI_WORKLOAD_CONTAINER", "floci-workload"
)
# Core docker-network services charts reach by name from inside k3s (same as the
# cloud in-network hostnames): the nginx edge proxy fronting the microservice
# Lambdas, and the shared Postgres. Registered in CoreDNS so pods resolve them by
# name instead of a churny container IP baked into config.
_PROXY_CONTAINER = os.environ.get("HMD_LOCAL_PROXY_CONTAINER", "hmd_proxy")
_DB_CONTAINER = os.environ.get("HMD_LOCAL_DB_CONTAINER", "hmd_db")

# The NeuronSphere k3s image ships without a bundled ingress controller, but the
# local BOM seeds a ``kubernetes.neuronsphere.io/ingress-controller`` Resource
# (name=traefik, ingress_class=traefik) that repos wire their ``eks-alb`` role to.
# We install Traefik so that Resource is real and Ingress objects are actually
# served -- the local analog of the cloud AWS Load Balancer Controller.
_INGRESS_ENABLE_ENV = "HMD_LOCAL_NEURONSPHERE_ENABLE_INGRESS"
_TRAEFIK_CHART_REPO = os.environ.get(
    "HMD_LOCAL_TRAEFIK_REPO", "https://traefik.github.io/charts"
)
_TRAEFIK_CHART_VERSION = os.environ.get("HMD_LOCAL_TRAEFIK_VERSION", "34.0.0")
_FLOCI_EKS_NETWORK = os.environ.get(
    "FLOCI_SERVICES_EKS_DOCKER_NETWORK", "neuronsphere_default"
)
# In-network Floci endpoint (resolvable from pods once the CoreDNS record exists).
_FLOCI_INTERNAL_ENDPOINT = os.environ.get(
    "FLOCI_INTERNAL_ENDPOINT", "http://neuronsphere:4566"
)


def _resolve_floci_ip(container: str) -> Optional[str]:
    """Return the given Floci container's IP on the k3s Docker network."""
    fmt = (
        '{{with index .NetworkSettings.Networks "' + _FLOCI_EKS_NETWORK + '"}}'
        "{{.IPAddress}}{{end}}"
    )
    try:
        result = subprocess.run(
            ["docker", "inspect", "-f", fmt, container],
            capture_output=True,
            text=True,
            timeout=10,
        )
        ip = result.stdout.strip()
        if result.returncode == 0 and ip:
            return ip
    except (subprocess.SubprocessError, OSError) as e:
        logger.debug(f"Could not resolve IP for {container}: {e}")
    return None


def _ensure_coredns_floci_entry() -> None:
    """Make ``neuronsphere``/``neuronsphere-workload`` resolvable from k3s pods.

    Applies a ``coredns-custom`` ConfigMap (the k3s-supported extension point,
    imported via ``import /etc/coredns/custom/*.server``) with a per-hostname
    server block pointing at the Floci container IP(s). Uses separate server
    blocks (not a second ``hosts`` plugin in ``.:53``, which would crash CoreDNS).
    Best-effort; logs and returns on any failure.
    """
    floci_ip = _resolve_floci_ip(_FLOCI_CONTAINER)
    if not floci_ip:
        logger.warning("Could not resolve Floci IP; skipping CoreDNS record")
        return
    workload_ip = _resolve_floci_ip(_FLOCI_WORKLOAD_CONTAINER) or floci_ip

    def _block(host: str, ip: str) -> str:
        return (
            f"{host}:53 {{\n"
            f"    hosts {{\n        {ip} {host}\n        fallthrough\n    }}\n}}\n"
        )

    # Floci endpoints plus the core docker-network services (best-effort: a name
    # that doesn't resolve on the k3s Docker network is simply skipped).
    entries = [
        ("neuronsphere", floci_ip),
        ("neuronsphere-workload", workload_ip),
    ]
    for name in (_PROXY_CONTAINER, _DB_CONTAINER):
        ip = _resolve_floci_ip(name)
        if ip:
            entries.append((name, ip))

    server = "".join(_block(host, ip) for host, ip in entries)
    configmap = {
        "apiVersion": "v1",
        "kind": "ConfigMap",
        "metadata": {"name": "coredns-custom", "namespace": "kube-system"},
        "data": {"neuronsphere.server": server},
    }
    with NamedTemporaryFile(mode="w", suffix=".yaml", delete=False) as cf:
        yaml.safe_dump(configmap, cf)
        cm_path = cf.name
    try:
        apply = subprocess.run(
            ["kubectl", "apply", "-f", cm_path], capture_output=True, text=True
        )
        if apply.returncode != 0:
            logger.warning(f"CoreDNS record apply failed: {apply.stderr}")
            return
        # Roll CoreDNS so it reloads the custom config immediately.
        subprocess.run(
            ["kubectl", "-n", "kube-system", "rollout", "restart", "deploy", "coredns"],
            capture_output=True,
            text=True,
        )
        subprocess.run(
            [
                "kubectl",
                "-n",
                "kube-system",
                "rollout",
                "status",
                "deploy",
                "coredns",
                "--timeout=60s",
            ],
            capture_output=True,
            text=True,
        )
        logger.info("CoreDNS: " + ", ".join(f"{host}->{ip}" for host, ip in entries))
    finally:
        try:
            os.unlink(cm_path)
        except OSError:
            pass


def _ensure_ingress_controller() -> None:
    """Install the k3s ingress controller (Traefik) -- best-effort, idempotent.

    This NeuronSphere k3s image ships no ingress controller, so the seeded
    ``ingress-controller`` Resource (Traefik) would otherwise be aspirational and
    Ingress objects would go unserved. Traefik binds the node's :80/:443 via
    ``hostPort`` (no dependency on a servicelb), so it is reachable at the k3s node
    container's name on the Floci docker network -- the local parity of reaching
    the cloud app through its ALB hostname. ``helm upgrade --install`` keeps it
    idempotent; a failure is logged and never aborts the bring-up.
    """
    if os.environ.get(_INGRESS_ENABLE_ENV, "true").lower() in ("false", "0", "no"):
        logger.info("Ingress controller install disabled via " + _INGRESS_ENABLE_ENV)
        return

    values = {
        # ClusterIP + hostPort avoids needing a servicelb to reach the node's :80.
        "service": {"type": "ClusterIP"},
        "ingressClass": {"enabled": True, "isDefaultClass": True},
        "deployment": {"replicas": 1},
        "ports": {
            "web": {
                "port": 8000,
                "hostPort": 80,
                "exposedPort": 80,
                "expose": {"default": True},
            },
            "websecure": {
                "port": 8443,
                "hostPort": 443,
                "exposedPort": 443,
                "expose": {"default": True},
            },
        },
        "resources": {
            "requests": {"cpu": "50m", "memory": "64Mi"},
            "limits": {"memory": "256Mi"},
        },
    }
    with NamedTemporaryFile(
        mode="w", suffix=".yaml", prefix="traefik-values-", delete=False
    ) as vf:
        yaml.safe_dump(values, vf)
        values_path = vf.name
    try:
        command = [
            "helm",
            "upgrade",
            "--install",
            "traefik",
            "traefik",
            "--repo",
            _TRAEFIK_CHART_REPO,
            "--version",
            _TRAEFIK_CHART_VERSION,
            "--namespace",
            "kube-system",
            "--wait",
            "--timeout",
            "5m",
            "--values",
            values_path,
        ]
        logger.info("Installing ingress controller (Traefik): " + " ".join(command))
        result = subprocess.run(command, capture_output=True, text=True)
        if result.returncode != 0:
            logger.warning(
                f"Traefik ingress-controller install failed "
                f"(exit={result.returncode}):\n{result.stderr}"
            )
        else:
            logger.info("Ingress controller (Traefik) installed")
    finally:
        try:
            os.unlink(values_path)
        except OSError:
            pass


def _ext_secrets_passes() -> List[Dict[str, Any]]:
    """Two install passes for the External Secrets operator.

    Pass 1 installs the operator + validating webhook (no ClusterSecretStore).
    Pass 2 adds the ``aws-secrets-manager`` ClusterSecretStore once the webhook is
    ready — it can't be created in the same release that first installs the
    webhook that validates it. Both authenticate to Floci with static creds
    (no IRSA locally) and point the AWS SDK at Floci via the in-network hostname
    (resolvable thanks to the CoreDNS record).
    """
    base: Dict[str, Any] = {
        "installCRDs": False,  # CRDs come from hmd-inf-ext-secrets-crds (first)
        "clusterSecretStore": {"enabled": False},
        "parameterStoreSecretStore": {"enabled": False},
        "dockerRepoSecret": {"enabled": False},
        "extraEnv": [
            {"name": "AWS_ENDPOINT_URL", "value": _FLOCI_INTERNAL_ENDPOINT},
            {"name": "AWS_ACCESS_KEY_ID", "value": "test"},
            {"name": "AWS_SECRET_ACCESS_KEY", "value": "test"},
            {"name": "AWS_REGION", "value": "local"},
        ],
    }
    store = {
        **base,
        "clusterSecretStore": {
            "enabled": True,
            "local": True,
            "name": "aws-secrets-manager",
        },
    }
    return [base, store]


# Ordered: CRDs must exist before the operator; operators before workload charts.
# ``passes`` (or ``passes_builder``) applies successive ``helm upgrade``s where a
# single release can't converge — e.g. ESO's self-validated ClusterSecretStore.
_OPERATORS: List[Dict[str, Any]] = [
    {
        "name": "ext-secrets-crds",
        "repo": "hmd-inf-ext-secrets-crds",
        "release": "ext-secrets-crds",
    },
    {
        "name": "ext-secrets",
        "repo": "hmd-inf-ext-secrets",
        "release": "external-secrets",
        "passes_builder": _ext_secrets_passes,
    },
    {
        "name": "clickhouse-operator",
        "repo": "hmd-inf-clickhouse-operator",
        "release": "clickhouse-operator",
    },
    {
        "name": "keda",
        "repo": "hmd-inf-keda",
        "release": "keda",
    },
]


def _namespace(op: Dict[str, Any]) -> str:
    """Install namespace — matches the ``<instance>-<did>`` the charts hardcode."""
    return f"{op['release']}-{_DID}"


def _enabled() -> bool:
    return os.environ.get(_ENABLE_ENV, "true").lower() not in ("false", "0", "no")


def _wait_for_node_ready(timeout: int = 120) -> bool:
    """Wait for at least one k3s node to report Ready.

    ``wait_for_k3s_ready`` only confirms the Floci EKS API status is ACTIVE; the
    kubelet/CNI/CoreDNS may still be warming up. Installing operators before a
    node is schedulable makes ``helm --wait`` fail, so gate on real node
    readiness here.
    """
    deadline = time.time() + timeout
    while time.time() < deadline:
        result = subprocess.run(
            ["kubectl", "get", "nodes", "--no-headers"],
            capture_output=True,
            text=True,
        )
        for line in result.stdout.splitlines():
            cols = line.split()
            if len(cols) >= 2 and cols[1] == "Ready":
                return True
        time.sleep(4)
    logger.warning("No k3s node became Ready in time; installing operators anyway")
    return False


def _clean_stale_nodes() -> None:
    """Delete ``NotReady`` ghost node registrations left in the k3s datastore.

    Floci reuses the k3s data volume across cluster delete/recreate, so each
    recreation leaves the previous node registered but NotReady. Those ghosts —
    and the node-affine local-path PVs bound to them — poison scheduling for new
    pods ("didn't match PersistentVolume's node affinity"). Remove them, and the
    orphaned PVs whose node no longer exists, so fresh PVCs bind to the live node.
    """
    result = subprocess.run(
        ["kubectl", "get", "nodes", "--no-headers"], capture_output=True, text=True
    )
    live = set()
    for line in result.stdout.splitlines():
        cols = line.split()
        if len(cols) >= 2:
            if cols[1] == "Ready":
                live.add(cols[0])
            else:
                subprocess.run(
                    ["kubectl", "delete", "node", cols[0], "--ignore-not-found"],
                    capture_output=True,
                    text=True,
                )
    # Release PVs pinned (node affinity) to a node that no longer exists so their
    # PVCs can re-provision on the live node.
    pvs = subprocess.run(
        [
            "kubectl",
            "get",
            "pv",
            "-o",
            'jsonpath={range .items[*]}{.metadata.name}{"|"}'
            '{.spec.nodeAffinity.required.nodeSelectorTerms[0].matchExpressions[0].values[0]}{"\\n"}{end}',
        ],
        capture_output=True,
        text=True,
    )
    for line in pvs.stdout.splitlines():
        if "|" not in line:
            continue
        pv_name, node = line.split("|", 1)
        if node and node not in live:
            subprocess.run(
                [
                    "kubectl",
                    "delete",
                    "pv",
                    pv_name,
                    "--ignore-not-found",
                    "--wait=false",
                ],
                capture_output=True,
                text=True,
            )


def _wait_namespace_not_terminating(namespace: str, timeout: int = 60) -> None:
    """If ``namespace`` is stuck Terminating, wait (bounded) for it to clear.

    Floci's k3s datastore persists across cluster delete/recreate, so a namespace
    a prior teardown deleted can still be Terminating when ``up`` reinstalls into
    it — helm then fails with "namespace ... is being terminated".
    """
    deadline = time.time() + timeout
    while time.time() < deadline:
        result = subprocess.run(
            [
                "kubectl",
                "get",
                "namespace",
                namespace,
                "-o",
                "jsonpath={.status.phase}",
            ],
            capture_output=True,
            text=True,
        )
        if result.returncode != 0 or result.stdout.strip() != "Terminating":
            return
        logger.info(f"Waiting for namespace {namespace} to finish terminating...")
        time.sleep(4)


def _standard_values(op: Dict[str, Any]) -> Dict[str, Any]:
    """The standard NeuronSphere values every chart expects.

    In the cloud these are injected as ``--set`` flags by ``hmd helm deploy``
    (``_set_standard_values``). The up-time install bypasses that path, so supply
    local equivalents here (dummy AWS values; ALB/ACM/WAF are not emulated).
    Namespace-derived values match the ``<instance>-<did>`` the charts assume.
    """
    instance = op["release"]
    namespace = _namespace(op)
    return {
        "instance_name": instance,
        "deployment_id": _DID,
        "namespace_name": namespace,
        "account": "000000000000",
        "aws_region": "local",
        "standard_name": namespace,
        "env": {
            "HMD_INSTANCE_NAME": instance,
            "HMD_REPO_NAME": op["repo"],
            "HMD_DID": _DID,
            "HMD_ENVIRONMENT": "local",
            "HMD_REGION": "reg1",
            "HMD_REPO_VERSION": "local",
            "HMD_CUSTOMER_CODE": "hmd",
        },
    }


def _resolve_repo_root(op: Dict[str, Any]) -> Optional[Path]:
    """Resolve an operator's repo root, preferring a checked-out HMD_REPO_HOME copy.

    Returns a directory containing ``src/helm`` and ``meta-data/manifest.json``.
    """
    repo_home = os.environ.get("HMD_REPO_HOME")
    if repo_home:
        candidate = Path(repo_home) / op["repo"]
        if (candidate / "src" / "helm" / "Chart.yaml").exists():
            return candidate
    # Bundled pre-build artifact (unzipped build/ output of the repo).
    bundled = (_external_dir / op["name"]).resolve()
    if (bundled / "src" / "helm" / "Chart.yaml").exists():
        return bundled
    return None


def _load_default_configuration(repo_root: Path) -> Dict[str, Any]:
    manifest = repo_root / "meta-data" / "manifest.json"
    if not manifest.exists():
        return {}
    try:
        with open(manifest) as f:
            data = json.load(f)
        return data.get("deploy", {}).get("default_configuration", {}) or {}
    except (json.JSONDecodeError, OSError) as e:
        logger.warning(f"Could not read default_configuration from {manifest}: {e}")
        return {}


def _deep_merge(base: Dict[str, Any], overlay: Dict[str, Any]) -> Dict[str, Any]:
    out = dict(base)
    for k, v in overlay.items():
        if isinstance(v, dict) and isinstance(out.get(k), dict):
            out[k] = _deep_merge(out[k], v)
        else:
            out[k] = v
    return out


def _ensure_chart_dependencies(chart_dir: Path) -> None:
    """Build subchart dependencies if the chart declares them but ``charts/`` is empty.

    Bundled ``:build`` artifacts already have resolved ``charts/*.tgz``; a raw
    HMD_REPO_HOME checkout may not, so fetch them (needs network for that repo).
    """
    chart_yaml = chart_dir / "Chart.yaml"
    try:
        with open(chart_yaml) as f:
            chart = yaml.safe_load(f) or {}
    except (yaml.YAMLError, OSError):
        return
    if not chart.get("dependencies"):
        return
    charts_sub = chart_dir / "charts"
    if charts_sub.exists() and any(charts_sub.iterdir()):
        return
    logger.info(f"Building chart dependencies for {chart_dir}")
    subprocess.run(
        ["helm", "dependency", "build", str(chart_dir)],
        capture_output=True,
        text=True,
    )


def _helm_upgrade(op: Dict[str, Any], chart_dir: Path, overlay: Dict[str, Any]) -> bool:
    """Run a single ``helm upgrade --install`` pass for an operator."""
    values = _load_default_configuration(chart_dir.parent.parent)
    values = _deep_merge(values, _standard_values(op))
    if overlay:
        values = _deep_merge(values, overlay)

    with NamedTemporaryFile(
        mode="w", suffix=".yaml", prefix=f"{op['name']}-values-", delete=False
    ) as vf:
        yaml.safe_dump(values, vf)
        values_path = vf.name

    try:
        command = [
            "helm",
            "upgrade",
            "--install",
            op["release"],
            str(chart_dir),
            "--namespace",
            _namespace(op),
            "--create-namespace",
            "--wait",
            "--timeout",
            op.get("timeout", "5m"),
            "--values",
            values_path,
        ]
        _wait_namespace_not_terminating(_namespace(op))
        logger.info(f"Installing operator '{op['name']}': {' '.join(command)}")
        result = subprocess.run(command, capture_output=True, text=True)
        if result.returncode != 0:
            logger.warning(
                f"Operator '{op['name']}' install failed (exit={result.returncode}):\n"
                f"stdout={result.stdout}\nstderr={result.stderr}"
            )
            return False
        return True
    finally:
        try:
            os.unlink(values_path)
        except OSError:
            pass


def _install_operator(op: Dict[str, Any]) -> bool:
    repo_root = _resolve_repo_root(op)
    if not repo_root:
        logger.warning(
            f"Operator chart '{op['name']}' ({op['repo']}) not found in "
            f"HMD_REPO_HOME or bundled artifacts; skipping"
        )
        return False

    chart_dir = repo_root / "src" / "helm"
    _ensure_chart_dependencies(chart_dir)

    # Most operators are a single release; some (ESO) need successive passes so a
    # CR the operator itself validates isn't applied before its webhook is ready.
    if op.get("passes_builder"):
        passes = op["passes_builder"]()
    else:
        passes = op.get("passes", [op.get("overlay") or {}])
    for overlay in passes:
        if not _helm_upgrade(op, chart_dir, overlay):
            return False
    return True


def provision_k3s_operators() -> None:
    """Install all cluster operators onto the running k3s cluster (best-effort).

    Requires ``KUBECONFIG`` to already point at the Floci k3s cluster. Safe to
    call repeatedly (each install is ``helm upgrade --install``).
    """
    if not _enabled():
        logger.info("k3s operator provisioning disabled via " + _ENABLE_ENV)
        return
    if not os.environ.get("KUBECONFIG"):
        logger.info("KUBECONFIG not set; skipping k3s operator provisioning")
        return

    # The cluster's EKS status is ACTIVE, but the node may still be warming up;
    # wait for it before installing (helm --wait would otherwise fail).
    _wait_for_node_ready()
    _clean_stale_nodes()

    # Make the in-network Floci hostname resolvable from pods first, so operators
    # and workload charts alike can reach `neuronsphere:4566` (cloud parity).
    _ensure_coredns_floci_entry()

    # Deploy the ingress controller (Traefik) so the seeded ingress-controller
    # Resource is real and Ingress objects are served on the node's :80/:443.
    _ensure_ingress_controller()

    # When the External Secrets stack is opted into the ms-deployment DAG
    # (HMD_LOCAL_NEURONSPHERE_ENABLE_EXT_SECRETS), the DAG is its sole installer —
    # skip the operators-path install here so the two don't collide on CRD
    # ownership (different helm release names for the same CRDs).
    ext_secrets_via_dag = os.environ.get(
        "HMD_LOCAL_NEURONSPHERE_ENABLE_EXT_SECRETS", ""
    ).strip().lower() in ("1", "true", "yes", "on")
    ext_secrets_ops = {"ext-secrets-crds", "ext-secrets"}

    installed = []
    for op in _OPERATORS:
        if ext_secrets_via_dag and op["name"] in ext_secrets_ops:
            logger.info(
                f"Skipping operator '{op['name']}' — deployed via the ms-deployment "
                "DAG (HMD_LOCAL_NEURONSPHERE_ENABLE_EXT_SECRETS)"
            )
            continue
        if _install_operator(op):
            installed.append(op["name"])
    if installed:
        logger.info(f"Installed k3s operators: {', '.join(installed)}")
