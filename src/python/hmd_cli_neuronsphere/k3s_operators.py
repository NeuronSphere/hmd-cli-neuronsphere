"""
Install NeuronSphere cluster operators onto the local Floci k3s cluster.

The cloud EKS cluster gets its operators (External Secrets, KEDA, the ClickHouse
operator, cert-manager, ...) from dedicated ``hmd-inf-*`` Helm repos. For local
cloud-parity we install those *same* charts onto the Floci k3s cluster so that
chart repos deployed with ``hmd helm deploy --local`` render and apply their
``ExternalSecret`` / ``ScaledObject`` / ``ClickHouseCluster`` resources unchanged.

Only External Secrets is still installed directly here. KEDA, the ClickHouse
operator, and cert-manager instead deploy through the real ms-deployment DAG (a
BOM entry contributed by the optional ``hmd-cli-plugin-ns-telemetry`` package, see
``bom_seeder.BOM_ENTRIES_ENTRY_POINT``), so they're only installed when that
plugin is actually installed, instead of unconditionally.

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

from .floci_deployer import DOCKER_NETWORK_NAME
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

# Floci disables k3s's own packaged ingress controller (--disable=traefik, to
# emulate a raw EKS control plane), but the local BOM seeds a
# ``kubernetes.neuronsphere.io/ingress-controller`` Resource (name=traefik,
# ingress_class=traefik) that repos wire their ``eks-alb`` role to. The
# hmd-img-k3s-floci wrapper image bakes a rendered Traefik manifest into k3s's
# native auto-deploying manifests directory so it installs itself at cluster
# boot -- this module just waits for/tears down that baked-in install (see
# ``_ensure_ingress_controller``), it doesn't install Traefik itself.
_INGRESS_ENABLE_ENV = "HMD_LOCAL_NEURONSPHERE_ENABLE_INGRESS"
# The per-HMD_HOME-scoped Docker network (see floci_deployer.DOCKER_NETWORK_NAME).
# FLOCI_SERVICES_EKS_DOCKER_NETWORK is only set inside the `floci` container's own
# environment (via docker-compose.admin.yml), not this CLI process's, so it's not
# a usable override here.
_FLOCI_EKS_NETWORK = DOCKER_NETWORK_NAME
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


_TRAEFIK_LABEL_SELECTOR = "app.kubernetes.io/name=traefik"
_TRAEFIK_RESOURCE_KINDS = (
    "deployments,services,serviceaccounts,ingressclasses,"
    "clusterroles,clusterrolebindings"
)
_TRAEFIK_MANIFEST_PATH = (
    "/var/lib/rancher/k3s/server/manifests/neuronsphere-traefik.yaml"
)


def _ensure_ingress_controller(timeout: int = 120) -> None:
    """Wait for the image-baked ingress controller (Traefik), or remove it.

    The k3s image (hmd-img-k3s-floci) renders the Traefik chart at build time
    and bakes the resulting plain manifest into k3s's native auto-deploying
    manifests directory, so it installs itself on cluster boot -- there is no
    helm install to run from here. k3s only applies that manifest once (on a
    content-hash change), so it won't resurrect resources removed by a prior
    disable -- re-apply it (idempotent) before waiting for readiness, so
    toggling ``HMD_LOCAL_NEURONSPHERE_ENABLE_INGRESS`` back on works without
    recreating the cluster. Waiting itself mirrors ``_wait_for_node_ready``'s
    poll-with-deadline pattern.
    """
    # One-time migration: a cluster/volume created before ingress was baked
    # into the image may still carry the Helm release this used to install at
    # runtime, under the same `traefik` name/namespace the baked-in plain
    # manifest now also uses -- clear it so the two don't collide.
    migrated = subprocess.run(
        ["helm", "uninstall", "traefik", "--namespace", "kube-system"],
        capture_output=True,
        text=True,
    )
    if migrated.returncode == 0:
        logger.info(
            "Removed legacy runtime-installed Traefik release "
            "(ingress is now baked into the k3s image)"
        )

    if os.environ.get(_INGRESS_ENABLE_ENV, "true").lower() in ("false", "0", "no"):
        logger.info(
            "Ingress controller disabled via "
            + _INGRESS_ENABLE_ENV
            + "; removing baked-in Traefik"
        )
        subprocess.run(
            [
                "kubectl",
                "-n",
                "kube-system",
                "delete",
                _TRAEFIK_RESOURCE_KINDS,
                "-l",
                _TRAEFIK_LABEL_SELECTOR,
                "--ignore-not-found",
            ],
            capture_output=True,
            text=True,
        )
        return

    # Re-apply the baked-in manifest from inside the k3s node container
    # (idempotent): a no-op if k3s's own addon controller already applied it
    # at boot, but restores resources a prior disable removed -- k3s only
    # (re-)applies a manifest on a content-hash change, it doesn't otherwise
    # reconcile resources deleted out-of-band.
    from .floci_deployer import K3S_CLUSTER_NAME

    subprocess.run(
        [
            "docker",
            "exec",
            f"floci-eks-{K3S_CLUSTER_NAME}",
            "kubectl",
            "apply",
            "-f",
            _TRAEFIK_MANIFEST_PATH,
        ],
        capture_output=True,
        text=True,
    )

    deadline = time.time() + timeout
    while time.time() < deadline:
        result = subprocess.run(
            [
                "kubectl",
                "-n",
                "kube-system",
                "rollout",
                "status",
                "deploy/traefik",
                "--timeout=10s",
            ],
            capture_output=True,
            text=True,
        )
        if result.returncode == 0:
            logger.info("Ingress controller (Traefik) ready")
            return
        time.sleep(4)
    logger.warning("Ingress controller (Traefik) did not become ready in time")


def _ext_secrets_passes() -> List[Dict[str, Any]]:
    """Two install passes for the External Secrets operator.

    Pass 1 installs the operator + validating webhook (no ClusterSecretStore).
    Pass 2 adds the ``aws-secrets-manager``/``aws-parameter-store`` ClusterSecretStores
    once the webhook is ready — they can't be created in the same release that first
    installs the webhook that validates them — and, once those stores exist, the
    ``dockerRepoSecret`` ClusterExternalSecret that syncs ``hmd-docker-repo-secret``
    into every namespace (same mechanism cloud uses). ``dockerRepoSecret`` targets
    ``aws-parameter-store`` specifically: the local CDKTF overlay that provisions the
    secret's value calls ``hmd_lib_secrets_backend.create_secret()``, which always
    writes to SSM Parameter Store regardless of its "Secrets Manager" naming — a
    ClusterSecretStore reading from Secrets Manager would never find it. Both passes
    authenticate to Floci with static creds (no IRSA locally) and point the AWS SDK at
    Floci via the in-network hostname (resolvable thanks to the CoreDNS record).

    NOTE: as of the ClickHouse-operator/telemetry work, ``ext-secrets`` is deployed
    through the real ms-deployment DAG (see ``bom_seeder.EXT_SECRETS_BOM`` /
    ``_EXT_SECRETS_LOCAL_CONFIG``) whenever it's part of the BOM — which
    ``bom_includes_repo_class`` makes true by default now (opt out via
    ``HMD_LOCAL_NEURONSPHERE_ENABLE_EXT_SECRETS=false``). This function's multi-pass
    logic only fires via the legacy hardcoded ``_OPERATORS`` path below, which is
    skipped in that case (kept in sync for when/if that path is ever exercised again).

    Region: the deploy pipeline auto-injects a top-level ``aws_region: "local"`` value
    for every local chart, but ``get_deployer_target_session`` -- what CDKTF stacks
    actually resolve their boto3 region to -- lands on ``"us-west-2"`` regardless
    (confirmed empirically against existing local credentials). ``aws_region``/
    ``AWS_REGION`` here override that auto-injected default to match where CDKTF
    writes actually land, mirroring ``bom_seeder._EXT_SECRETS_LOCAL_CONFIG``.
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
            {"name": "AWS_REGION", "value": "us-west-2"},
        ],
    }
    store = {
        **base,
        "aws_region": "us-west-2",
        "clusterSecretStore": {
            "enabled": True,
            "local": True,
            "name": "aws-secrets-manager",
        },
        "parameterStoreSecretStore": {
            "enabled": True,
            "local": True,
            "name": "aws-parameter-store",
        },
        "dockerRepoSecret": {
            "enabled": True,
            "secretStoreName": "aws-parameter-store",
        },
    }
    return [base, store]


# Ordered: CRDs must exist before the operator; operators before workload charts.
# ``passes`` (or ``passes_builder``) applies successive ``helm upgrade``s where a
# single release can't converge — e.g. ESO's self-validated ClusterSecretStore.
#
# The ClickHouse operator (CHOP), cert-manager, and KEDA used to be hardcoded here
# too. They're now deployed through the real DAG (BOM entries contributed by the
# optional hmd-cli-plugin-ns-telemetry package, see
# bom_seeder.BOM_ENTRIES_ENTRY_POINT) so they're only installed when that plugin is
# actually installed, instead of unconditionally.
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
]


def _namespace(op: Dict[str, Any]) -> str:
    """Install namespace — matches the ``<instance>-<did>`` the charts hardcode."""
    return f"{op['release']}-{_DID}"


def _enabled() -> bool:
    return os.environ.get(_ENABLE_ENV, "true").lower() not in ("false", "0", "no")


def cluster_incarnation_id() -> Optional[str]:
    """Fingerprint the running k3s cluster's identity.

    The ``kube-system`` Namespace is created fresh the moment a cluster comes
    up and never recreated for the cluster's lifetime, so its UID is a cheap,
    reliable stand-in for "which cluster incarnation is this" -- a
    delete+recreate (see ``floci_deployer.ensure_k3s_cluster``'s stale-image
    recovery) always yields a new UID, while a plain container restart of the
    same cluster keeps it. Callers use this to detect "the k3s cluster was
    recreated since our last successful deploy" and force a redeploy instead
    of trusting a stale ms-deployment bootstrap marker. Returns ``None`` on
    any kubectl failure (e.g. cluster not reachable yet).
    """
    try:
        result = subprocess.run(
            [
                "kubectl",
                "get",
                "namespace",
                "kube-system",
                "-o",
                "jsonpath={.metadata.uid}",
            ],
            capture_output=True,
            text=True,
            timeout=10,
        )
    except (subprocess.SubprocessError, OSError):
        return None
    uid = result.stdout.strip()
    return uid if result.returncode == 0 and uid else None


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


def _ensure_node_topology_labels() -> None:
    """Label the k3s node with zone/region topology, best-effort.

    Cloud EKS nodes carry ``topology.kubernetes.io/{zone,region}`` labels;
    the single local k3s node has none. Charts with a ``topologySpreadConstraints``
    keyed on zone (e.g. ClickHouse's Keeper StatefulSet) then find "0/1 nodes
    match" and every replica beyond the first sticks in Pending forever. Since
    there's only one node locally, any single zone value trivially satisfies
    max-skew for all such constraints.
    """
    result = subprocess.run(
        ["kubectl", "get", "nodes", "-o", "name"], capture_output=True, text=True
    )
    for line in result.stdout.splitlines():
        node = line.strip()
        if not node:
            continue
        subprocess.run(
            [
                "kubectl",
                "label",
                node,
                "topology.kubernetes.io/zone=local",
                "topology.kubernetes.io/region=local",
                "--overwrite",
            ],
            capture_output=True,
            text=True,
        )


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
    _ensure_node_topology_labels()

    # Make the in-network Floci hostname resolvable from pods first, so operators
    # and workload charts alike can reach `neuronsphere:4566` (cloud parity).
    _ensure_coredns_floci_entry()

    # Confirm the image-baked ingress controller (Traefik) is up (or tear it
    # down if disabled) so the seeded ingress-controller Resource is real and
    # Ingress objects are served on the node's :80/:443.
    _ensure_ingress_controller()

    # The External Secrets stack deploys through the ms-deployment DAG by default now
    # (opt out via HMD_LOCAL_NEURONSPHERE_ENABLE_EXT_SECRETS=false) — same as when an
    # installed plugin's BOM contribution already brings its own ext-secrets instance
    # (e.g. hmd-cli-plugin-ns-telemetry, whose ClickHouse chart needs a real
    # ClusterSecretStore). Either way the DAG is its sole installer: skip the
    # operators-path install here so the two don't collide on CRD ownership (different
    # helm release names for the same CRDs).
    from .bom_seeder import bom_includes_repo_class

    ext_secrets_via_dag = os.environ.get(
        "HMD_LOCAL_NEURONSPHERE_ENABLE_EXT_SECRETS", ""
    ).strip().lower() not in ("false", "0", "no") or bom_includes_repo_class(
        "hmd-inf-ext-secrets"
    )
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
