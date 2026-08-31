"""
Floci deployer module.

Handles provisioning AWS resources, deploying Lambda functions, and
configuring API Gateway routes in Floci for the local NeuronSphere environment.
"""

import base64
import hashlib
import json
import os
import subprocess
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Dict, List, Optional

import boto3
import requests
from botocore.exceptions import ClientError
from cement import minimal_logger

logger = minimal_logger("floci_deployer")

FLOCI_ENDPOINT = os.environ.get(
    "FLOCI_ENDPOINT", os.environ.get("MINISTACK_ENDPOINT", "http://localhost:4566")
)
# `neuronsphere` is the canonical in-network hostname for the Floci
# container, registered as a Docker network alias on the compose service so
# Docker DNS resolves it inside the `neuronsphere_default` network. It is
# also the hostname baked into presigned S3/API URLs returned to host-side
# consumers. To make those URLs resolvable from the host (CLI, `hmd build`),
# the host requires a one-time `/etc/hosts` entry:
# `127.0.0.1 neuronsphere neuronsphere-workload`. The
# `ensure_neuronsphere_hosts_entry` pre-flight in the up-path verifies this
# and prints setup instructions if missing.
FLOCI_INTERNAL_ENDPOINT = "http://neuronsphere:4566"

# The former split "workload" Floci has been collapsed into the single Floci
# environment above. These constants are retained as backward-compatible
# aliases of the admin values so existing importers keep working; both the
# `neuronsphere` and `neuronsphere-workload` network aliases now resolve to
# the same instance. Override via FLOCI_WORKLOAD_ENDPOINT if ever needed.
FLOCI_WORKLOAD_ENDPOINT = os.environ.get("FLOCI_WORKLOAD_ENDPOINT", FLOCI_ENDPOINT)
FLOCI_WORKLOAD_INTERNAL_ENDPOINT = FLOCI_INTERNAL_ENDPOINT

REGION = os.environ.get("AWS_REGION", "us-west-2")
ACCOUNT_ID = "000000000000"
# Single-account local: workload deployments share the control-plane account.
WORKLOAD_ACCOUNT_ID = ACCOUNT_ID

# Local default when HMD_CUSTOMER_CODE is not configured. Must match the value
# the ms-dbaccount Lambda + workflow runner deploy with, or admin/user DB
# secrets get seeded under a name the consumer never looks up.
_DEFAULT_CUSTOMER_CODE = "none"


def local_customer_code() -> str:
    """Resolve the local ``HMD_CUSTOMER_CODE``.

    Sourced from ``$HMD_HOME/.config/hmd.env`` -- ``load_hmd_env()`` (called at
    every CLI command entry, override=True) loads it into the environment -- and
    falls back to the local default ``"none"`` when it is not configured there.
    Reading it in one place keeps producer (secret seeding) and consumer
    (ms-dbaccount ``make_standard_name`` lookups) in agreement.
    """
    return os.environ.get("HMD_CUSTOMER_CODE") or _DEFAULT_CUSTOMER_CODE


# Hostnames the host machine must resolve to a loopback address so it can
# consume URLs (e.g. presigned S3 URLs) returned by services running inside
# the Floci Docker network. The same names are registered as Docker network
# aliases on the Floci compose services so in-network DNS resolves them
# automatically.
NEURONSPHERE_HOST_ALIASES = ("neuronsphere", "neuronsphere-workload")


def ensure_neuronsphere_hosts_entry() -> None:
    """Verify that the host can resolve `neuronsphere`/`neuronsphere-workload`
    to a loopback address.

    Presigned URLs returned by services in the Floci network bake the
    in-network hostname (e.g. `neuronsphere:4566`) into the URL itself; the
    host must map that name to 127.0.0.1 (where Floci's published port
    lives) for those URLs to work outside the network. We can't add the
    entry automatically without sudo, so when missing we print a one-line
    setup command and abort. The check runs once per `up` and is cheap.

    Raises ``SystemExit`` with a non-zero status if any required hostname
    is missing or resolves to a non-loopback address.
    """
    import socket

    missing = []
    for host in NEURONSPHERE_HOST_ALIASES:
        try:
            ip = socket.gethostbyname(host)
        except socket.gaierror:
            missing.append(host)
            continue
        if not (ip.startswith("127.") or ip == "::1"):
            missing.append(host)

    if not missing:
        return

    hosts_line = "127.0.0.1 " + " ".join(NEURONSPHERE_HOST_ALIASES)
    print(
        "\n"
        "  ERROR: NeuronSphere requires the following hostnames to resolve\n"
        f"         to a loopback address on this machine: {', '.join(missing)}\n"
        "\n"
        "  Run this once (requires sudo):\n"
        "\n"
        f"      sudo sh -c 'echo \"{hosts_line}\" >> /etc/hosts'\n"
        "\n"
        "  Why: presigned S3/API URLs returned by services inside the Floci\n"
        "  network use the in-network hostname (e.g. neuronsphere:4566). The\n"
        "  host must map that name to 127.0.0.1 so those URLs can be reached\n"
        "  from `hmd build`, `push-artifact`, and other CLI commands.\n"
    )
    raise SystemExit(1)


@dataclass(frozen=True)
class FlociTarget:
    """Which Floci instance an API call is aimed at.

    There is one Floci for the control plane and one per named environment
    (each emulating its own AWS account). Every function that talks to Floci
    takes an optional ``target``; omitting it means the control plane, so all
    pre-existing call sites keep their original behaviour.

    ``endpoint`` is reachable from the host (the control plane's published
    :4566, or an env's ``hmd_proxy`` stream port); ``internal_endpoint`` is the
    in-Docker-network address baked into API Gateway invoke URLs and handed to
    Lambdas as ``AWS_ENDPOINT_URL``.

    ``container`` and ``alias`` are deliberately distinct. ``container`` is the
    Docker container name -- use it for ``docker exec``/``docker inspect`` and
    nothing else. ``alias`` is the name that may be put *on the wire* (nginx
    upstreams, ``AWS_ENDPOINT_URL``, chart hostnames): it is always an explicit
    ``networks.<net>.aliases`` entry, never a Compose *service key*. Compose
    registers every service key as a network alias in every project sharing the
    network, and both docker-compose.control-plane.yml and
    docker-compose.environment.yml key their Floci service ``floci`` -- so
    ``floci`` round-robins across the control-plane and every environment's
    Floci, silently splitting API Gateway and Lambda state between accounts.
    """

    name: str
    endpoint: str
    internal_endpoint: str
    account_id: str
    container: str
    alias: str
    region: str = REGION


def control_plane_target() -> FlociTarget:
    """The control-plane Floci -- ms-deployment, ms-naming, artifact-lib."""
    return FlociTarget(
        name="control-plane",
        endpoint=FLOCI_ENDPOINT,
        internal_endpoint=FLOCI_INTERNAL_ENDPOINT,
        account_id=ACCOUNT_ID,
        container="floci",
        alias="neuronsphere",
    )


def env_target(env) -> FlociTarget:
    """The Floci account belonging to ``env`` (an ``env_registry.LocalEnvironment``).

    A legacy-layout environment shares the control-plane Floci (see
    ``env_registry._legacy_environment``), so it resolves to the same endpoints
    and account as the control plane.
    """
    if getattr(env, "legacy_layout", False):
        return control_plane_target()
    return FlociTarget(
        name=env.slug,
        endpoint=f"http://localhost:{env.floci_port}",
        internal_endpoint=f"http://{env.floci_container}:4566",
        account_id=env.account_id,
        container=env.floci_container,
        alias=env.floci_alias,
    )


def _resolve_target(target: Optional[FlociTarget]) -> FlociTarget:
    return target or control_plane_target()


def _get_client(service: str, target: Optional[FlociTarget] = None):
    target = _resolve_target(target)
    return boto3.client(
        service,
        endpoint_url=target.endpoint,
        aws_access_key_id=os.environ.get("AWS_ACCESS_KEY_ID", "dummykey"),
        aws_secret_access_key=os.environ.get("AWS_SECRET_ACCESS_KEY", "dummykey"),
        region_name=target.region,
    )


def get_client(service: str, env=None):
    """Public Floci client factory for plugin entry-point hooks (e.g.
    ``hmd_cli_neuronsphere.get_post_deploy_notices``) that need to read their
    own secrets/resources back from an environment's Floci. Thin wrapper
    around the internal ``_get_client`` -- kept separate so the ~20 existing
    internal call sites don't need to change."""
    return _get_client(service, env_target(env) if env else None)


def wait_for_floci(
    timeout: int = 300, endpoint: str = None, *, target: Optional[FlociTarget] = None
):
    """Poll Floci health endpoint until all services are available.

    :param timeout: Max seconds to wait
    :param endpoint: Base URL to check (overrides ``target``)
    :param target: Which Floci to poll (defaults to the control plane)
    """
    ep = endpoint or _resolve_target(target).endpoint
    start = time.time()
    while time.time() - start < timeout:
        try:
            r = requests.get(f"{ep}/_floci/health", timeout=5)
            if r.status_code == 200:
                logger.info(f"Floci healthy at {ep}: {r.json()}")
                return
        except requests.RequestException:
            pass
        logger.debug(
            f"Waiting for Floci at {ep} ({int(time.time() - start)}s/{timeout}s)..."
        )
        time.sleep(3)
    raise RuntimeError(f"Floci at {ep} not ready after {timeout}s")


# ---------------------------------------------------------------------------
# Per-HMD_HOME Docker resource naming
# ---------------------------------------------------------------------------
# The Docker Compose project, the shared Docker network, and the Floci-spawned
# k3s cluster (container + data volume) are all named from a single shared
# hash of the resolved HMD_HOME path, so two different HMD_HOME environments
# on the same machine (e.g. a temp HMD_HOME used for testing, or two customer
# sandboxes) never collide on the same containers/volumes/network -- each
# gets its own persistent state, and the same HMD_HOME always resolves back
# to the same names across `up`/`down` cycles. Falls back to the historical
# fixed names when HMD_HOME isn't set.


def _hmd_home_hash() -> str:
    hmd_home = os.environ.get("HMD_HOME")
    if not hmd_home:
        return ""
    return hashlib.sha256(os.path.abspath(hmd_home).encode()).hexdigest()[:8]


_HMD_HOME_HASH = _hmd_home_hash()

# The Docker Compose project name (`docker compose --project-name`), used for
# container-name prefixing and `com.docker.compose.project` label filtering
# (see port_validator.get_neuronsphere_container_ports).
COMPOSE_PROJECT_NAME = os.environ.get("HMD_LOCAL_COMPOSE_PROJECT_NAME") or (
    f"local_neuronsphere-{_HMD_HOME_HASH}" if _HMD_HOME_HASH else "local_neuronsphere"
)

# The shared Docker network every local NeuronSphere container attaches to.
# Declared `external: true` in the compose files (so Compose requires it to
# pre-exist rather than auto-creating it), with its real name templated via
# `${NEURONSPHERE_DOCKER_NETWORK}` -- exported below so every `docker compose`
# invocation and any non-compose-managed container (Floci's spawned k3s,
# projectbuilder) attach to the same, correctly-scoped network. The Compose
# *key* every service-level `networks:` list references stays the fixed
# `neuronsphere_default` string across all files -- only this one's `name:`
# mapping (in the two files that own the top-level `networks:` block) changes.
DOCKER_NETWORK_NAME = os.environ.get("HMD_LOCAL_DOCKER_NETWORK") or (
    f"neuronsphere_default-{_HMD_HOME_HASH}"
    if _HMD_HOME_HASH
    else "neuronsphere_default"
)
os.environ.setdefault("NEURONSPHERE_DOCKER_NETWORK", DOCKER_NETWORK_NAME)


# ---------------------------------------------------------------------------
# EKS / k3s cluster management
# ---------------------------------------------------------------------------


def _default_k3s_cluster_name() -> str:
    """Per-HMD_HOME-unique default k3s cluster name.

    Floci names the k3s container/volume it spawns (``floci-eks-<name>``)
    purely from the cluster ``name`` given to its EKS ``create_cluster`` API
    -- it has no other identifying context. See the "Per-HMD_HOME Docker
    resource naming" note above.
    """
    if not _HMD_HOME_HASH:
        return "neuronsphere"
    return f"neuronsphere-{_HMD_HOME_HASH}"


K3S_CLUSTER_NAME = (
    os.environ.get("HMD_LOCAL_K3S_CLUSTER_NAME") or _default_k3s_cluster_name()
)
K3S_KUBECONFIG_PATH = Path(
    os.environ.get(
        "HMD_LOCAL_K3S_KUBECONFIG",
        os.path.join(os.environ.get("HMD_HOME", "/tmp"), ".cache", "k3s", "kubeconfig"),
    )
)

K3S_WRAPPER_IMAGE = os.environ.get(
    "HMD_LOCAL_K3S_WRAPPER_IMAGE",
    f"{os.environ.get('HMD_LOCAL_NS_CONTAINER_REGISTRY', 'ghcr.io/neuronsphere')}"
    "/hmd-img-k3s-floci:0.2",
)


def ensure_k3s_wrapper_image(image: str = K3S_WRAPPER_IMAGE) -> str:
    """Verify the configured k3s wrapper image is available, pulling it if not.

    Floci hardcodes ``--kube-apiserver-arg=storage-backend=sqlite3`` when
    spawning k3s, which the kube-apiserver rejects. Still present as of
    1.5.34 (unfixed upstream). We work around this by pointing Floci at a
    wrapper image whose entrypoint drops the bad flag before calling the
    real k3s binary. The image lives in ``hmd-img-k3s-floci``.

    The default resolves to ``$HMD_LOCAL_NS_CONTAINER_REGISTRY`` (or
    ``ghcr.io/neuronsphere``) -- the same published registry every other
    bundled image (Postgres, Airflow, Trino, ...) defaults to -- so a
    developer who hasn't built this repo locally still gets a working cluster:
    if the image isn't cached, pull it from there rather than requiring a
    local ``hmd build``. Set ``HMD_LOCAL_K3S_WRAPPER_IMAGE`` to point at a
    different tag (e.g. a locally-built dev tag) instead.
    """
    inspect = subprocess.run(["docker", "image", "inspect", image], capture_output=True)
    if inspect.returncode == 0:
        return image
    logger.info(f"k3s wrapper image '{image}' not cached locally; pulling...")
    pull = subprocess.run(["docker", "pull", image], capture_output=True, text=True)
    if pull.returncode == 0:
        return image
    raise RuntimeError(
        f"k3s wrapper image '{image}' not found locally and could not be "
        f"pulled:\n{pull.stderr}\n"
        f"Build it with `hmd build` in the hmd-img-k3s-floci repo, or "
        f"override via HMD_LOCAL_K3S_WRAPPER_IMAGE."
    )


def ensure_k3s_cluster(
    name: str = K3S_CLUSTER_NAME, *, target: Optional[FlociTarget] = None
) -> Dict[str, Any]:
    """Create a Floci EKS k3s cluster (idempotent).

    Floci's EKS service in real mode (FLOCI_SERVICES_EKS_MOCK=false) starts a
    privileged k3s container per cluster on the configured Docker network,
    binding the API server to a host port from 6500-6599.

    Each named environment owns a cluster in its own Floci account, so
    ``target`` selects which Floci is asked to spawn it.

    Returns the describe_cluster response payload.
    """
    ensure_k3s_wrapper_image()
    target = _resolve_target(target)
    eks = _get_client("eks", target)

    def _create() -> None:
        eks.create_cluster(
            name=name,
            roleArn=f"arn:aws:iam::{target.account_id}:role/eks-role",
            resourcesVpcConfig={"subnetIds": [], "securityGroupIds": []},
            # Track the cloud EKS version (hmd-inf-eks-cluster cluster_version) so
            # operator/CRD charts targeting the cloud API also install locally.
            # The actual k3s version is baked into the wrapper image
            # (HMD_LOCAL_K3S_WRAPPER_IMAGE); keep them in sync.
            version=os.environ.get("HMD_LOCAL_K3S_VERSION", "1.34"),
        )
        logger.info(f"Created k3s cluster: {name}")

    try:
        _create()
    except ClientError as e:
        if e.response["Error"]["Code"] not in (
            "ResourceInUseException",
            "ConflictException",
        ):
            raise
        # A cluster with this name already exists in Floci's *persistent* store.
        # Floci pins the node image into the cluster record at creation time, so
        # a cluster created before the wrapper image was wired up (or against a
        # stale/old image) keeps respawning that image and crash-loops on the
        # bad --kube-apiserver-arg=storage-backend flag. If the spawned container
        # is missing or not the wrapper image we expect, recreate the cluster so
        # Floci respawns it from the current FLOCI_SERVICES_EKS_DEFAULT_IMAGE.
        image = _k3s_container_image(name)
        running = _k3s_container_running(name)
        # A stopped container running the *expected* image is not stale -- it is
        # what a non-purge `down` leaves behind (see `stop_k3s_cluster`).
        # Recreating it would drop the cluster's datastore along with every Helm
        # release on it, which is exactly what makes the next `up` redeploy the
        # whole BOM. Start it back up instead and keep the cluster's identity.
        if image == K3S_WRAPPER_IMAGE and not running:
            logger.info(f"k3s cluster {name} is stopped; restarting it in place")
            if start_k3s_container(name):
                running = True
            else:
                logger.warning(
                    f"Could not restart the stopped k3s container for {name} "
                    f"(the Docker network may have been removed); recreating."
                )
        if image != K3S_WRAPPER_IMAGE or not running:
            logger.warning(
                f"Existing k3s cluster {name} is stale "
                f"(image={image or 'missing'}, running={running}, "
                f"expected={K3S_WRAPPER_IMAGE}); recreating to pick up the "
                f"current wrapper image."
            )
            delete_k3s_cluster(name, target=target)
            _wait_for_cluster_gone(name, target=target)
            _create()
        else:
            logger.debug(f"k3s cluster already exists and healthy: {name}")
    return eks.describe_cluster(name=name)["cluster"]


def _k3s_host_port(name: str) -> str:
    """Return the host port that maps to the k3s API server (6443) on the
    Floci-spawned container, or an empty string if it can't be discovered.
    """
    try:
        result = subprocess.run(
            [
                "docker",
                "inspect",
                f"floci-eks-{name}",
                "--format",
                '{{(index (index .NetworkSettings.Ports "6443/tcp") 0).HostPort}}',
            ],
            capture_output=True,
            text=True,
            timeout=5,
        )
        return result.stdout.strip() if result.returncode == 0 else ""
    except (subprocess.SubprocessError, OSError):
        return ""


def _k3s_container_image(name: str) -> str:
    """Return the image the spawned ``floci-eks-<name>`` container was launched
    with, or an empty string if the container is missing or docker is
    unreachable.
    """
    try:
        result = subprocess.run(
            [
                "docker",
                "inspect",
                f"floci-eks-{name}",
                "--format",
                "{{.Config.Image}}",
            ],
            capture_output=True,
            text=True,
            timeout=5,
        )
        return result.stdout.strip() if result.returncode == 0 else ""
    except (subprocess.SubprocessError, OSError):
        return ""


def _k3s_container_running(name: str) -> bool:
    """True if the spawned ``floci-eks-<name>`` container exists and is running."""
    try:
        result = subprocess.run(
            [
                "docker",
                "inspect",
                f"floci-eks-{name}",
                "--format",
                "{{.State.Running}}",
            ],
            capture_output=True,
            text=True,
            timeout=5,
        )
        return result.returncode == 0 and result.stdout.strip() == "true"
    except (subprocess.SubprocessError, OSError):
        return False


def _wait_for_cluster_gone(
    name: str, timeout: int = 60, *, target: Optional[FlociTarget] = None
) -> None:
    """Block until Floci reports the cluster no longer exists.

    Floci's ``delete_cluster`` tears the k3s container down asynchronously; a
    follow-up ``create_cluster`` issued too soon races the teardown and gets
    another ``ResourceInUseException``. Poll ``describe_cluster`` until it 404s,
    then force-remove any container Floci left behind.
    """
    eks = _get_client("eks", target)
    start = time.time()
    while time.time() - start < timeout:
        try:
            eks.describe_cluster(name=name)
        except ClientError as e:
            if e.response["Error"]["Code"] in (
                "ResourceNotFoundException",
                "NotFoundException",
            ):
                break
        time.sleep(2)
    # Belt-and-suspenders: drop any lingering container so the recreate spawns
    # fresh from the current FLOCI_SERVICES_EKS_DEFAULT_IMAGE.
    try:
        subprocess.run(
            ["docker", "rm", "-f", f"floci-eks-{name}"],
            capture_output=True,
            timeout=15,
        )
    except (subprocess.SubprocessError, OSError):
        pass
    # Also drop the persistent /var/lib/rancher/k3s volume. Without this, the
    # respawned container reuses the old sqlite3-backed cluster state, and
    # since the k3s node's hostname defaults to the (new, random) container
    # ID, it registers as a brand-new Node while the previous one lingers
    # forever as NotReady — breaking EndpointSlice reconciliation for every
    # Service and orphaning any StatefulSet pods pinned to the dead node.
    try:
        subprocess.run(
            ["docker", "volume", "rm", "-f", f"floci-eks-{name}"],
            capture_output=True,
            timeout=15,
        )
    except (subprocess.SubprocessError, OSError):
        pass


def _k3s_container_logs(name: str) -> str:
    """Best-effort fetch of the spawned k3s container's recent logs.

    Floci names the per-cluster k3s container ``floci-eks-<cluster>``. Returns
    an empty string if the container is missing or docker isn't reachable.
    """
    try:
        result = subprocess.run(
            ["docker", "logs", "--tail", "30", f"floci-eks-{name}"],
            capture_output=True,
            text=True,
            timeout=5,
        )
        output = (result.stdout + result.stderr).strip()
        return output
    except (subprocess.SubprocessError, OSError):
        return ""


def wait_for_k3s_ready(
    name: str = K3S_CLUSTER_NAME,
    timeout: int = 300,
    *,
    target: Optional[FlociTarget] = None,
) -> Dict[str, Any]:
    """Poll describe_cluster until status == ACTIVE."""
    eks = _get_client("eks", target)
    start = time.time()
    last_status = None
    while time.time() - start < timeout:
        try:
            cluster = eks.describe_cluster(name=name)["cluster"]
            status = cluster.get("status")
            if status != last_status:
                logger.info(f"k3s cluster {name} status: {status}")
                last_status = status
            if status == "ACTIVE":
                return cluster
            if status == "FAILED":
                logs = _k3s_container_logs(name)
                detail = f"\nfloci-eks-{name} logs:\n{logs}" if logs else ""
                raise RuntimeError(f"k3s cluster {name} entered FAILED status{detail}")
        except ClientError as e:
            logger.debug(f"describe_cluster failed: {e}")
        time.sleep(3)
    logs = _k3s_container_logs(name)
    detail = f"\nfloci-eks-{name} logs:\n{logs}" if logs else ""
    raise RuntimeError(f"k3s cluster {name} not ACTIVE after {timeout}s{detail}")


def write_kubeconfig(
    name: str = K3S_CLUSTER_NAME,
    path: Path = None,
    *,
    target: Optional[FlociTarget] = None,
) -> Path:
    """Fetch the k3s cluster's kubeconfig and write it to disk.

    Preference order:
      1. Floci's custom kubeconfig endpoint (if exposed).
      2. ``/etc/rancher/k3s/k3s.yaml`` from inside the spawned k3s container,
         with the server URL rewritten to point at the host-published port.
         This carries the real client cert/key needed to actually authenticate.
      3. Synthesized minimal kubeconfig from the EKS describe payload (last
         resort — uses a placeholder token that won't authenticate).
    """
    floci_endpoint = _resolve_target(target).endpoint
    out_path = path or K3S_KUBECONFIG_PATH
    out_path.parent.mkdir(parents=True, exist_ok=True)

    # Try Floci's custom kubeconfig endpoint first.
    for url in [
        f"{floci_endpoint}/_floci/eks/{name}/kubeconfig",
        f"{floci_endpoint}/_floci/services/eks/clusters/{name}/kubeconfig",
    ]:
        try:
            r = requests.get(url, timeout=10)
            if r.status_code == 200 and r.text.strip().startswith("apiVersion"):
                out_path.write_text(r.text)
                logger.info(f"Wrote kubeconfig from {url} to {out_path}")
                return out_path
        except requests.RequestException:
            continue

    # Pull the real kubeconfig out of the k3s container.
    host_port = _k3s_host_port(name)
    try:
        result = subprocess.run(
            ["docker", "exec", f"floci-eks-{name}", "cat", "/etc/rancher/k3s/k3s.yaml"],
            capture_output=True,
            text=True,
            timeout=10,
        )
        if result.returncode == 0 and result.stdout.strip().startswith("apiVersion"):
            kubeconfig = result.stdout
            if host_port:
                # k3s writes server: https://127.0.0.1:6443 — rewrite to the host-published port.
                kubeconfig = kubeconfig.replace(
                    "https://127.0.0.1:6443", f"https://localhost:{host_port}"
                )
            out_path.write_text(kubeconfig)
            logger.info(f"Wrote kubeconfig from k3s container to {out_path}")
            return out_path
    except (subprocess.SubprocessError, OSError) as e:
        logger.debug(f"Failed to read kubeconfig from container: {e}")

    # Fallback: synthesize a minimal kubeconfig pointing at the cluster endpoint.
    # Floci returns a server URL using the in-Docker-network hostname
    # (e.g. https://floci-eks-<name>:6443). For host-side kubectl use, swap
    # in the host-published port from the spawned k3s container.
    eks = _get_client("eks", target)
    cluster = eks.describe_cluster(name=name)["cluster"]
    endpoint = cluster.get("endpoint")
    if not endpoint:
        raise RuntimeError(
            f"Cannot retrieve kubeconfig for {name}: no endpoint and no /_floci/eks/.../kubeconfig endpoint"
        )
    host_port = _k3s_host_port(name)
    if host_port:
        endpoint = f"https://localhost:{host_port}"
    cert = cluster.get("certificateAuthority", {}).get("data", "")
    kubeconfig = f"""apiVersion: v1
kind: Config
clusters:
- name: {name}
  cluster:
    server: {endpoint}
    insecure-skip-tls-verify: true
contexts:
- name: {name}
  context:
    cluster: {name}
    user: {name}
current-context: {name}
users:
- name: {name}
  user:
    token: floci-local
"""
    out_path.write_text(kubeconfig)
    logger.info(f"Wrote synthesized kubeconfig to {out_path}")
    return out_path


def delete_k3s_cluster(
    name: str = K3S_CLUSTER_NAME, *, target: Optional[FlociTarget] = None
) -> None:
    """Delete the k3s cluster (best-effort, used by teardown)."""
    eks = _get_client("eks", target)
    try:
        eks.delete_cluster(name=name)
        logger.info(f"Deleted k3s cluster: {name}")
    except ClientError as e:
        code = e.response["Error"]["Code"]
        if code in ("ResourceNotFoundException", "NotFoundException"):
            logger.debug(f"k3s cluster already gone: {name}")
        else:
            logger.warning(f"Failed to delete k3s cluster {name}: {e}")


def stop_k3s_cluster(name: str = K3S_CLUSTER_NAME) -> bool:
    """Stop the Floci-spawned k3s container without deleting the cluster.

    This is what a non-purge ``down`` does. Asking Floci to *delete* the cluster
    (``delete_k3s_cluster``) also drops its ``floci-eks-<name>`` volume -- the
    ``/var/lib/rancher/k3s`` datastore -- so the next ``up`` gets a brand-new
    cluster with a new ``kube-system`` UID. ``environments._bootstrap_environment``
    reads that as "the cluster was recreated since the last bootstrap" and
    redeploys the entire BOM, which is precisely the slow restart this avoids.

    Stopping the container instead keeps the datastore, the cluster's identity
    and every Helm release on it, so ``up`` can take the reconcile fast path.
    Best-effort: returns whether the container was stopped.
    """
    try:
        result = subprocess.run(
            ["docker", "stop", f"floci-eks-{name}"],
            capture_output=True,
            timeout=60,
        )
    except (subprocess.SubprocessError, OSError) as e:
        logger.debug(f"k3s container stop skipped for {name}: {e}")
        return False
    if result.returncode == 0:
        logger.info(f"Stopped k3s container: floci-eks-{name}")
        return True
    logger.debug(f"k3s container stop for {name} returned {result.returncode}")
    return False


def start_k3s_container(name: str = K3S_CLUSTER_NAME) -> bool:
    """Start a k3s container previously stopped by :func:`stop_k3s_cluster`.

    Fails (returning False) if the container's Docker network was removed while
    it was stopped -- a stopped endpoint holds the network by *id*, and a
    recreated network gets a new one. Callers fall back to recreating the
    cluster, which is why ``down`` keeps the network unless purging.
    """
    try:
        result = subprocess.run(
            ["docker", "start", f"floci-eks-{name}"],
            capture_output=True,
            text=True,
            timeout=120,
        )
    except (subprocess.SubprocessError, OSError) as e:
        logger.debug(f"k3s container start failed for {name}: {e}")
        return False
    if result.returncode == 0:
        logger.info(f"Started k3s container: floci-eks-{name}")
        return True
    logger.warning(f"Could not start floci-eks-{name}: {(result.stderr or '').strip()}")
    return False


def purge_k3s_container_and_volume(name: str = K3S_CLUSTER_NAME) -> None:
    """Force-remove the Floci-spawned k3s container AND its persistent volume.

    Belt-and-suspenders after `delete_k3s_cluster`: Floci tears the container
    down asynchronously, and a leftover `/var/lib/rancher/k3s` docker volume
    (``floci-eks-<name>``) would be reused by a later `up`. A container respawned
    against stale sqlite state gets a fresh random hostname and registers as a
    brand-new Node while the previous one lingers forever as NotReady, so
    StatefulSet pods pinned (via node affinity) to the dead node's
    ``hmdlabs.io/repo-instance-name`` label can never schedule.

    `down --purge` promises a clean slate, so it must drop the volume too.
    Mirrors the cleanup in `_wait_for_cluster_gone`.
    """
    for args in (
        ["docker", "rm", "-f", f"floci-eks-{name}"],
        ["docker", "volume", "rm", "-f", f"floci-eks-{name}"],
    ):
        try:
            subprocess.run(args, capture_output=True, timeout=15)
        except (subprocess.SubprocessError, OSError) as e:
            logger.debug(f"k3s purge step {args} skipped: {e}")


def clear_apigateway_state(data_dir) -> None:
    """Drop Floci's persisted API Gateway **v1** state before Floci starts.

    Floci runs with ``FLOCI_STORAGE_MODE: persistent`` and writes every v1 API
    Gateway entity to ``$HMD_HOME/floci/data/apigateway-*.json`` with all of its
    fields null -- ids, names, resource paths and stage names are all lost::

        "000000000000/us-west-2::a8b73e0e83" : { "id": null, "name": null, ... }

    (v1 only; lambda, s3, secretsmanager, iam, eks and apigatewayv2 all persist
    real values.) Floci 1.5.34 rehydrates those records on start and serves them
    from ``GET /restapis``, so a restart over an existing data dir comes back
    with unusable, *undeletable* (``id: null``) gateways that accumulate one per
    gateway per `up`.

    Dropping the files is safe because no API Gateway state is expected to
    survive a restart: every `up` recreates each service's gateway from scratch
    via ``setup_service`` -> ``create_api_gateway(recreate=True)``, and rewrites
    the nginx config, before the "already bootstrapped" restart fast-path runs.

    Best-effort by design -- a missing directory or an unremovable file must
    never fail `up`; `create_api_gateway` skips any ghost that survives (which
    is also what covers an already-running Floci, whose in-memory store this
    cannot reach).

    :param data_dir: the Floci data directory to clean. Each environment has
        its own (``$HMD_HOME/.cache/environments/<slug>/floci/data``), separate
        from the control plane's ``$HMD_HOME/floci/data``.
    """
    data_dir = Path(data_dir)
    try:
        # `apigateway-*` deliberately does not match `apigatewayv2-*`.
        stale = sorted(data_dir.glob("apigateway-*.json"))
    except OSError as e:
        logger.debug(f"Could not scan {data_dir} for API Gateway state: {e}")
        return

    for path in stale:
        try:
            path.unlink()
        except OSError as e:
            logger.debug(f"Could not remove stale API Gateway state {path}: {e}")
    if stale:
        logger.info(f"Cleared {len(stale)} persisted Floci API Gateway state file(s)")


def _store_local_admin_db_secret(
    *,
    target: Optional[FlociTarget] = None,
    did: Optional[str] = None,
    db_host: str = "hmd_db",
    core_instance_name: Optional[str] = None,
    environment_name: str = "local",
) -> None:
    """Bootstrap the local postgres admin secret in Floci Secrets Manager.

    `hmd-ms-dbaccount`'s `do_create_db_account` reads its admin DB credentials
    from a SecretsManager secret named `{secret_base}_db-secret`, where
    `secret_base = make_standard_name(instance_name, repo_class, did, env,
    region, customer_code)`.

    We store the admin secret under TWO identities, both pointing at the same
    shared `hmd_db` Postgres:

    1. `hmd_db` / `hmd-postgres-base` / `did` — the container/image identity,
       the convention every local *service compose* (and core DB provisioning)
       expects.
    2. `CORE_INSTANCE_NAME` / `CORE_REPO_CLASS` / `local` — the identity a
       *resource-typed* `database.neuronsphere.io/postgres` dependency resolves
       to locally (the core instance produces that resource; see
       `bom_seeder.CORE_PRODUCED_DEFINITIONS`). A consumer chart derives its DB
       secret name from *this* identity (e.g. hive-metastore's `awsDbSecretName`),
       and `hmd-cli-dbaccount._deploy_local` now names the user secret from the
       same resolved `database-instance` identity — so producer and consumer
       agree. Without this second admin secret, `ms-dbaccount` couldn't find the
       admin creds when a plugin's db-account deploys under the core identity.

    Idempotent: put_secret_value overwrites if the secret already exists.

    Each environment runs its own Postgres and its own dbaccount Lambda, so
    this is written into *that environment's* Floci (``target``) with that
    environment's ``did`` and Postgres container (``db_host``). The dbaccount
    that reads it back computes the same ``make_standard_name`` prefix, so
    producer and consumer must be given the same values -- a mismatch here is
    the failure mode commit b160612 fixed.

    ``environment_name`` is that prefix's environment component. It must be the
    environment's slug, because that is its ``Environment.type`` and therefore
    what ms-deployment passes to the consumer's deploy as ``--environment``.
    Hardcoding ``"local"`` would name the admin secret correctly only in the
    default environment.
    """
    from hmd_cli_tools.hmd_cli_tools import make_standard_name
    from .bom_seeder import CORE_INSTANCE_NAME, CORE_REPO_CLASS

    did = did or os.environ.get("HMD_DID", "aaa")
    core_instance_name = core_instance_name or CORE_INSTANCE_NAME
    region = os.environ.get("HMD_REGION", "reg1")
    # From hmd.env, falling back to "none". Must match the customer code the
    # ms-dbaccount Lambda + workflow runner deploy with, or the admin secret is
    # seeded under a name the consumer never looks up and every
    # hmd-database-account deploy fails with ResourceNotFoundException.
    customer_code = local_customer_code()

    secret_value = json.dumps(
        {
            "username": "postgres",
            "password": "admin",
            "engine": "aurora-postgresql",
            "host": db_host,
            "port": 5432,
        }
    )

    sm = _get_client("secretsmanager", target)

    def _put(secret_base: str) -> None:
        secret_name = f"{secret_base}_db-secret"
        try:
            sm.create_secret(Name=secret_name, SecretString=secret_value)
            logger.info(f"Stored local admin DB secret: {secret_name}")
        except ClientError as e:
            if e.response["Error"]["Code"] == "ResourceExistsException":
                sm.put_secret_value(SecretId=secret_name, SecretString=secret_value)
                logger.info(f"Updated local admin DB secret: {secret_name}")
            else:
                raise

    # The RepoInstance name stays "hmd_db" in every environment: repo_instance
    # is unique by name *per Environment*, so this is not ambiguous.
    _put(
        make_standard_name(
            "hmd_db", "hmd-postgres-base", did, environment_name, region, customer_code
        )
    )
    _put(
        make_standard_name(
            core_instance_name,
            CORE_REPO_CLASS,
            environment_name,
            environment_name,
            region,
            customer_code,
        )
    )


def provision_resources(
    resources: Dict[str, List[Dict[str, Any]]],
    local_loader=None,
    *,
    target: Optional[FlociTarget] = None,
    did: Optional[str] = None,
    db_host: str = "hmd_db",
    core_instance_name: Optional[str] = None,
    environment_name: str = "local",
):
    """Provision AWS resources declared by plugins.

    Creates SQS queues, S3 buckets, and the local admin postgres secret
    from the aggregated resource dictionary. Each call is idempotent.
    Per-DB user secrets are created later by ms-dbaccount via
    `provision_plugin_databases()`. DynamoDB tables are created lazily
    by `hmd-entity-storage`'s `DynamoDbEngine` on first service
    invocation, so they have the correct attributes, key schema, and
    GSIs.

    ``target`` selects the Floci account (control plane, or one environment's),
    and ``environment_name`` the environment slug those resources are named for.
    """
    target = _resolve_target(target)
    _store_local_admin_db_secret(
        target=target,
        did=did,
        db_host=db_host,
        core_instance_name=core_instance_name,
        environment_name=environment_name,
    )

    # Create SQS queues
    sqs = _get_client("sqs", target)
    for queue in resources.get("sqs_queues", []):
        name = queue["name"]
        try:
            sqs.create_queue(QueueName=name)
            logger.info(f"Created SQS queue: {name}")
        except ClientError as e:
            if e.response["Error"]["Code"] == "QueueAlreadyExists":
                logger.debug(f"SQS queue already exists: {name}")
            else:
                raise

    # Create S3 buckets. Always include the CDKTF tfstate bucket so `hmd cdktf
    # deploy` (which uses an S3 backend at hmd.<account>.<hmd_region>.tfstate) can
    # `tofu init` locally. The bucket name uses the HMD region (matching
    # HmdCdkTfStack's backend), not the cloud LocationConstraint region.
    hmd_region = os.environ.get("HMD_REGION", "reg1")
    bucket_names = [b["name"] for b in resources.get("s3_buckets", [])]
    bucket_names.append(f"hmd.{target.account_id}.{hmd_region}.tfstate")

    s3 = _get_client("s3", target)
    for name in bucket_names:
        try:
            s3.create_bucket(
                Bucket=name,
                CreateBucketConfiguration={"LocationConstraint": target.region},
            )
            logger.info(f"Created S3 bucket: {name}")
        except ClientError as e:
            code = e.response["Error"]["Code"]
            if code in ("BucketAlreadyOwnedByYou", "BucketAlreadyExists"):
                logger.debug(f"S3 bucket already exists: {name}")
            else:
                raise


def _local_db_secret_base(
    did: Optional[str] = None, environment_name: str = "local"
) -> str:
    """Compute the make_standard_name secret_base for the local admin DB.

    Mirrors `_store_local_admin_db_secret`: instance_name=hmd_db,
    repo_class=hmd-postgres-base. The resulting prefix is what
    `hmd-ms-dbaccount`'s `do_create_db_account` uses for both the admin
    `_db-secret` and the per-user `_<username>` credential secrets.

    ``did`` defaults to ``HMD_DID``; pass an environment's ``deployment_id``
    to compute that environment's prefix. ``environment_name`` is the
    environment's slug (its ``Environment.type``) and must match what
    ``_store_local_admin_db_secret`` wrote.
    """
    from hmd_cli_tools.hmd_cli_tools import make_standard_name

    return make_standard_name(
        "hmd_db",
        "hmd-postgres-base",
        did or os.environ.get("HMD_DID", "aaa"),
        environment_name,
        os.environ.get("HMD_REGION", "reg1"),
        # From hmd.env, fallback "none"; see local_customer_code / _store_local_admin_db_secret.
        local_customer_code(),
    )


CORE_DATABASES = [
    {"db_name": "hmd_ms_naming", "username": "hmd_ms_naming"},
    {"db_name": "hmd_ms_deployment", "username": "hmd_ms_deployment"},
]


def _post_create_db_account(
    did: str,
    db_name: str,
    username: str,
    origin: str,
    route_prefix: str = "",
) -> None:
    """POST to ms-dbaccount to idempotently create a database/user.

    `origin` is a label (plugin name or "core") used in log lines. Retries
    on 404 ``Invalid API id`` to absorb the brief gap between
    ``deploy_api_gateway()`` and Floci having the route fully live. Lets
    KeyboardInterrupt propagate so Ctrl+C aborts the provisioning loop
    promptly instead of running through every plugin's per-call timeout.

    ``route_prefix`` selects an environment's dbaccount (deployed into that
    environment's own Floci and routed at ``/<slug>/hmd_ms_dbaccount/``);
    empty means the unprefixed control-plane route.
    """
    payload = {
        # Both names are RepoInstance-scoped, and repo_instance is unique by
        # name *per Environment* -- so they stay constant across environments.
        "db_repo_class": "hmd-postgres-base",
        "db_instance_name": "hmd_db",
        "db_deployment_id": did,
        "db_name": db_name,
        "username": username,
    }
    prefix = f"/{route_prefix.strip('/')}" if route_prefix else ""
    url = f"http://localhost{prefix}/hmd_ms_dbaccount/api/create_db_account"
    max_attempts = 6
    backoff = 0.5
    for attempt in range(1, max_attempts + 1):
        try:
            resp = requests.post(url, json=payload, timeout=20)
        except KeyboardInterrupt:
            raise
        except Exception as e:
            logger.warning(f"dbaccount provisioning for {origin}/{db_name} failed: {e}")
            return

        # Floci returns 404 with body containing "Invalid API id" while the
        # API Gateway is still propagating after deploy_api_gateway. Retry
        # briefly before giving up.
        if resp.status_code == 404 and "Invalid API id" in resp.text:
            if attempt < max_attempts:
                time.sleep(backoff)
                backoff = min(backoff * 2, 4.0)
                continue
            logger.warning(
                f"dbaccount provisioning for {origin}/{db_name} returned 404 "
                f"after {max_attempts} attempts: {resp.text}"
            )
            return

        if resp.status_code >= 400:
            logger.warning(
                f"dbaccount provisioning for {origin}/{db_name} "
                f"returned {resp.status_code}: {resp.text}"
            )
        else:
            logger.info(f"dbaccount provisioning for {origin}/{db_name}: {resp.text}")
        return


def provision_plugin_databases(
    local_loader, env=None, include_core: bool = False
) -> None:
    """Call an environment's hmd-ms-dbaccount to create its DBs and users.

    POSTs to `http://localhost/<slug>/hmd_ms_dbaccount/api/create_db_account`
    with `db_instance_name=hmd_db`, `db_repo_class=hmd-postgres-base`.
    dbaccount detects `HMD_ENVIRONMENT=local` and uses `password=username` so
    the resulting secret matches the convention every local service compose
    config hardcodes.

    Provisions each enabled plugin's `resources.databases` entries against the
    environment's own Postgres.

    `CORE_DATABASES` are provisioned only when ``include_core`` is set. In the
    control-plane/environment layout they are *not*: ms-naming and ms-deployment
    belong to the control plane, which has no dbaccount of its own (dbaccount is
    per-environment, matching the cloud) and uses `ensure_core_databases_direct`
    instead. Platform (legacy) mode is a single stack whose dbaccount does serve
    the core databases, so it passes ``include_core=True``.

    Database and user names are **not** environment-scoped -- each environment
    runs its own Postgres, so `hmd_ms_transform` in one env is a different
    database on a different server, exactly as in the cloud.

    Idempotent on warm restarts: dbaccount returns `"No secret created."` when
    both the user and secret already exist.
    """
    did = env.deployment_id if env is not None else os.environ.get("HMD_DID", "aaa")
    route_prefix = env.slug if env is not None and not env.legacy_layout else ""

    if include_core:
        for db in CORE_DATABASES:
            _post_create_db_account(
                did,
                db["db_name"],
                db["username"],
                origin="core",
                route_prefix=route_prefix,
            )

    for plugin_name in local_loader.get_enabled_plugins():
        config = local_loader.get_plugin_config(plugin_name)
        if not config:
            continue
        databases = config.get("resources", {}).get("databases", []) or []
        for db in databases:
            db_name = db.get("database")
            username = db.get("username") or db_name
            if not db_name:
                continue
            _post_create_db_account(
                did, db_name, username, origin=plugin_name, route_prefix=route_prefix
            )


def _psql(
    sql: str, dbname: str = "postgres", container: str = "hmd_db"
) -> subprocess.CompletedProcess:
    """Run a SQL statement in a Postgres container as the postgres superuser.

    ``container`` defaults to the control-plane ``hmd_db``; each environment
    has its own (``hmd_db-<slug>``).
    """
    return subprocess.run(
        [
            "docker",
            "exec",
            container,
            "psql",
            "-U",
            "postgres",
            "-d",
            dbname,
            "-tAc",
            sql,
        ],
        capture_output=True,
        text=True,
    )


def ensure_core_databases_direct(
    timeout: int = 240, container: str = "hmd_db", databases: List[Dict] = None
) -> None:
    """Guarantee the foundational control-plane databases/users exist.

    ms-naming, ms-deployment and artifact-lib are foundational to the local
    control plane and their Lambdas connect with the local
    ``password == username`` convention. The control plane has no dbaccount of
    its own -- dbaccount is per-environment, matching the cloud -- so this
    deterministic psql path is the *only* mechanism that creates them. Waits
    for PostgreSQL to accept connections first, then runs idempotently.
    """
    databases = CORE_DATABASES if databases is None else databases

    # Wait for postgres to accept connections (container may still be running
    # its entrypoint init on a cold boot).
    start = time.time()
    while time.time() - start < timeout:
        if _psql("SELECT 1", container=container).returncode == 0:
            break
        time.sleep(2)
    else:
        logger.warning(
            f"{container} not accepting connections after {timeout}s; "
            f"cannot ensure core databases directly"
        )
        return

    def _psql_c(sql: str) -> subprocess.CompletedProcess:
        return _psql(sql, container=container)

    for db in databases:
        name = db["db_name"]
        user = db["username"]
        role_sql = (
            f"DO $do$ BEGIN "
            f"IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '{user}') THEN "
            f"CREATE ROLE \"{user}\" LOGIN PASSWORD '{user}'; "
            f"END IF; END $do$;"
        )
        r = _psql_c(role_sql)
        if r.returncode != 0:
            logger.warning(f"Ensuring role {user} failed: {r.stderr.strip()}")
        # CREATE DATABASE cannot run inside a DO block / transaction, so guard
        # it with an existence check.
        exists = _psql_c(f"SELECT 1 FROM pg_database WHERE datname = '{name}'")
        if exists.stdout.strip() != "1":
            c = _psql_c(f'CREATE DATABASE "{name}" OWNER "{user}";')
            if c.returncode != 0:
                logger.warning(f"Creating database {name} failed: {c.stderr.strip()}")
        _psql_c(f'GRANT ALL PRIVILEGES ON DATABASE "{name}" TO "{user}";')
        logger.info(f"Ensured core database/user '{name}' in {container} (direct psql)")


def build_gozer_rds_secrets(local_loader) -> Dict[str, List[str]]:
    """Build the RDS_SECRETS map gozer Lambda expects.

    Maps each plugin's DB to a `[db_name, secret_name]` tuple keyed by the
    cloud-style identifier `dbaccount-<plugin_name>`. Tests refer to these
    identifiers via `pre_test_db_clear["rds_services"]`.
    """
    secret_base = _local_db_secret_base()
    rds_secrets: Dict[str, List[str]] = {}

    for plugin_name in local_loader.get_enabled_plugins():
        config = local_loader.get_plugin_config(plugin_name)
        if not config:
            continue
        databases = config.get("resources", {}).get("databases", []) or []
        for db in databases:
            db_name = db.get("database")
            username = db.get("username") or db_name
            if not db_name:
                continue
            rds_secrets[f"dbaccount-{plugin_name}"] = [
                db_name,
                f"{secret_base}_{username}",
            ]

    return rds_secrets


def _ensure_local_image(image_uri: str) -> str:
    """Resolve a Docker image URI for use by Floci's Lambda executor.

    Floci spawns Lambda containers through the mounted host ``docker.sock``,
    so any image already present in the host's image cache is reusable
    directly — no push to Floci's bundled ECR sidecar is required. The
    sidecar isn't reliably reachable from the host CLI on macOS Docker
    Desktop anyway (bridge network, port unpublished), so we skip it.

    For images not in the cache, return the URI unchanged so Floci's
    Lambda runner can pull from the original registry.
    """
    result = subprocess.run(
        ["docker", "image", "inspect", image_uri],
        capture_output=True,
    )
    if result.returncode == 0:
        logger.debug(f"Image {image_uri} cached locally; Floci will reuse it")
    else:
        logger.debug(f"Image {image_uri} not cached; Floci will pull from registry")
    return image_uri


def resolve_image_uri(repo_name: str, version: str) -> Optional[str]:
    """Find a locally-cached Docker image URI for a repo/version.

    Tries each candidate ref in order and returns the first that
    `<cli> image inspect` finds. Returns None if none are cached.

    The candidate order lives in :func:`image_cache.image_candidates` -- the
    same list :func:`image_cache.ensure_lambda_image` stages against, so
    resolution and staging can't drift.
    """
    from .image_cache import image_cached, image_candidates

    candidates = image_candidates(repo_name, version)
    for uri in candidates:
        if image_cached(uri):
            logger.debug(f"Resolved image for {repo_name}:{version} → {uri}")
            return uri

    logger.debug(
        f"No locally-cached image for {repo_name}:{version}; tried {candidates}"
    )
    return None


def deploy_lambda_function(
    function_name: str,
    image_uri: str,
    env_vars: Dict[str, str],
    timeout: int = 300,
    memory_size: int = 512,
    *,
    target: Optional[FlociTarget] = None,
) -> str:
    """Deploy a Docker image as a Lambda function in Floci.

    Returns the function ARN.
    """
    image_uri = _ensure_local_image(image_uri)
    target = _resolve_target(target)
    client = _get_client("lambda", target)

    function_config = {
        "FunctionName": function_name,
        "PackageType": "Image",
        "Code": {"ImageUri": image_uri},
        "Role": f"arn:aws:iam::{target.account_id}:role/lambda-role",
        "Timeout": timeout,
        "MemorySize": memory_size,
        "Environment": {"Variables": env_vars},
    }

    try:
        resp = client.create_function(**function_config)
        logger.info(f"Created Lambda function: {function_name}")
        return resp["FunctionArn"]
    except ClientError as e:
        if e.response["Error"]["Code"] == "ResourceConflictException":
            # Function exists -- update it
            client.update_function_code(FunctionName=function_name, ImageUri=image_uri)
            client.update_function_configuration(
                FunctionName=function_name,
                Timeout=timeout,
                MemorySize=memory_size,
                Environment={"Variables": env_vars},
            )
            resp = client.get_function(FunctionName=function_name)
            logger.info(f"Updated Lambda function: {function_name}")
            return resp["Configuration"]["FunctionArn"]
        raise


# ---------------------------------------------------------------------------
# API Gateway management
# ---------------------------------------------------------------------------


def create_api_gateway(
    api_name: str = "neuronsphere-local",
    recreate: bool = False,
    *,
    target: Optional[FlociTarget] = None,
) -> str:
    """Create or get a REST API Gateway in Floci.

    When ``recreate`` is True, any existing API Gateway with the same name
    is deleted first so we start with a clean resource tree. This is
    important because Floci's resource matcher appears to prefer the
    earliest-created `{proxy+}` resource on a tie, so accumulated stale
    routes from previous runs can hijack traffic for newly-registered
    services.

    Entries without an ``id``/``name`` are skipped: Floci persists every API
    Gateway *v1* entity with all-null fields (see `clear_apigateway_state`),
    and 1.5.34 serves those records back after a restart. botocore drops the
    null members, so such a "ghost" arrives here as a dict with no ``name``
    key at all. A ghost carries no id, so it can neither be reused nor
    deleted through the API -- only ignored.

    Returns the REST API ID.
    """
    client = _get_client("apigateway", target)

    apis = client.get_rest_apis()
    ghosts = 0
    for api in apis.get("items", []):
        if not api.get("id") or not api.get("name"):
            ghosts += 1
            continue
        if api["name"] == api_name:
            if recreate:
                client.delete_rest_api(restApiId=api["id"])
                logger.info(f"Deleted existing API Gateway: {api['id']}")
            else:
                logger.info(f"Found existing API Gateway: {api['id']}")
                return api["id"]
    if ghosts:
        logger.debug(
            f"Ignored {ghosts} API Gateway record(s) with no id/name -- Floci "
            f"persists REST API state with null fields and rehydrates it on start"
        )

    resp = client.create_rest_api(
        name=api_name,
        description="NeuronSphere local API Gateway",
    )
    logger.info(f"Created API Gateway: {resp['id']}")
    return resp["id"]


def add_api_gateway_route(
    api_id: str,
    service_name: str,
    function_name: str,
    *,
    target: Optional[FlociTarget] = None,
) -> None:
    """Add routes at the API Gateway root that proxy to a Lambda function.

    Creates `/` (root) and `/{proxy+}` integrations for every HTTP method.
    Designed for one-Lambda-per-gateway: the gateway has no service-name
    prefix in its path, so nginx must strip the service prefix before
    proxying to Floci. The Lambda then receives clean `/api/...` paths
    (rather than `/{service_name}/api/...`), which FastAPI/hmd-ms-base
    routes natively.
    """
    target = _resolve_target(target)
    client = _get_client("apigateway", target)
    lambda_client = _get_client("lambda", target)

    # Get root resource ID. Skip pathless records: Floci persists resources
    # with null fields too, so a rehydrated store can serve back ghosts
    # alongside the real tree (see `create_api_gateway`).
    resources = client.get_resources(restApiId=api_id)
    root_id = None
    existing_paths = {}
    for r in resources.get("items", []):
        if not r.get("path"):
            continue
        existing_paths[r["path"]] = r.get("id")
        if r["path"] == "/":
            root_id = r.get("id")

    # Get Lambda function ARN
    func = lambda_client.get_function(FunctionName=function_name)
    function_arn = func["Configuration"]["FunctionArn"]
    integration_uri = (
        f"arn:aws:apigateway:{target.region}:lambda:path"
        f"/2015-03-31/functions/{function_arn}/invocations"
    )

    # Use root resource directly for `/`; create `/{proxy+}` as its child
    svc_resource_id = root_id

    proxy_path = "/{proxy+}"
    if proxy_path in existing_paths:
        proxy_resource_id = existing_paths[proxy_path]
    else:
        proxy_resource = client.create_resource(
            restApiId=api_id,
            parentId=root_id,
            pathPart="{proxy+}",
        )
        proxy_resource_id = proxy_resource["id"]

    # Add methods with Lambda proxy integration on both resources.
    # Floci does not support the ANY catch-all method, so register each
    # HTTP method individually. Delete any pre-existing method first so a
    # partial prior registration (e.g. POST set but GET missing) can't leave
    # gaps that cause Floci's matcher to fall through to a sibling
    # `{proxy+}` resource.
    http_methods = ["GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS"]
    for resource_id in [svc_resource_id, proxy_resource_id]:
        for method in http_methods:
            try:
                client.delete_method(
                    restApiId=api_id,
                    resourceId=resource_id,
                    httpMethod=method,
                )
            except ClientError as e:
                if e.response["Error"]["Code"] != "NotFoundException":
                    raise

            client.put_method(
                restApiId=api_id,
                resourceId=resource_id,
                httpMethod=method,
                authorizationType="NONE",
            )

            client.put_integration(
                restApiId=api_id,
                resourceId=resource_id,
                httpMethod=method,
                type="AWS_PROXY",
                integrationHttpMethod="POST",
                uri=integration_uri,
            )

    logger.info(
        f"Added API Gateway route: /{service_name}/{{proxy+}} -> {function_name}"
    )


def deploy_api_gateway(
    api_id: str, stage_name: str = "local", *, target: Optional[FlociTarget] = None
) -> str:
    """Deploy the API Gateway to a stage.

    Returns the invoke URL for use within the Docker network.
    """
    target = _resolve_target(target)
    client = _get_client("apigateway", target)
    resp = client.create_deployment(restApiId=api_id)
    deployment_id = resp["id"]

    # Delete any existing stage first — Floci does not support
    # update_stage patch operations reliably.
    try:
        client.delete_stage(restApiId=api_id, stageName=stage_name)
    except ClientError:
        pass  # Stage doesn't exist yet, that's fine

    # Create the stage linked to the new deployment.
    # Floci does not auto-create stages from create_deployment's stageName.
    client.create_stage(
        restApiId=api_id,
        stageName=stage_name,
        deploymentId=deployment_id,
    )

    invoke_url = (
        f"{target.internal_endpoint}/restapis/{api_id}/{stage_name}/_user_request_"
    )
    logger.info(f"API Gateway deployed: {invoke_url}")
    return invoke_url


def get_api_gateway_url(
    api_id: str, stage: str = "local", *, target: Optional[FlociTarget] = None
) -> str:
    """Get the invoke URL for an API Gateway stage.

    Returns the URL usable within the Docker network.
    """
    endpoint = _resolve_target(target).internal_endpoint
    return f"{endpoint}/restapis/{api_id}/{stage}/_user_request_"


# Function names that can only ever exist in the control-plane account. An
# environment's deployments exclude artifact-lib (``CONTROL_PLANE_PLUGINS``,
# environments.py) and never deploy ms-deployment or ms-naming, so any of these
# found in an environment's Floci is a stray left by the `floci` DNS-alias
# collision: `floci` was a Compose service key on both compose files, so
# control-plane API calls round-robined into environment accounts.
CONTROL_PLANE_ONLY_FUNCTIONS = {
    "hmd_ms_deployment",
    "hmd_ms_naming",
    "hmd_ms_artifact_lib",
}


def prune_control_plane_strays(target: FlociTarget) -> int:
    """Delete control-plane-only Lambdas and gateways from an environment account.

    Only ever called with an environment ``target``; the allowlist above is
    bounded and cannot match a user service, so this can never delete something
    the environment legitimately deployed. Best-effort: a Floci that refuses a
    delete must not fail ``up``.

    Returns the number of objects removed.
    """
    if target.account_id == ACCOUNT_ID:
        # The control plane itself (or a legacy env sharing it) -- these are
        # exactly where the objects belong.
        return 0

    removed = 0

    apigw = _get_client("apigateway", target)
    try:
        apis = apigw.get_rest_apis(limit=500).get("items", [])
    except Exception as e:  # pragma: no cover - defensive
        logger.debug(f"Could not list REST APIs on {target.name}: {e}")
        apis = []
    stray_names = {f"neuronsphere-{fn}" for fn in CONTROL_PLANE_ONLY_FUNCTIONS}
    for api in apis:
        if not api.get("id") or api.get("name") not in stray_names:
            continue
        try:
            apigw.delete_rest_api(restApiId=api["id"])
        except Exception as e:
            logger.warning(f"Could not delete stray gateway {api['name']}: {e}")
            continue
        removed += 1
        logger.info(
            f"Removed stray control-plane API Gateway '{api['name']}' "
            f"({api['id']}) from environment account {target.name} -- left by "
            f"the `floci` DNS-alias collision"
        )

    lam = _get_client("lambda", target)
    try:
        functions = lam.list_functions().get("Functions", [])
    except Exception as e:  # pragma: no cover - defensive
        logger.debug(f"Could not list Lambdas on {target.name}: {e}")
        functions = []
    for fn in functions:
        name = fn.get("FunctionName")
        if name not in CONTROL_PLANE_ONLY_FUNCTIONS:
            continue
        try:
            lam.delete_function(FunctionName=name)
        except Exception as e:
            logger.warning(f"Could not delete stray Lambda {name}: {e}")
            continue
        removed += 1
        logger.info(
            f"Removed stray control-plane Lambda '{name}' from environment "
            f"account {target.name} -- left by the `floci` DNS-alias collision"
        )

    return removed


def find_deployed_rest_api(
    name_filter: str, *, target: Optional[FlociTarget] = None
) -> Optional[Dict[str, str]]:
    """Find a CDKTF-deployed API Gateway REST API whose name contains ``name_filter``.

    Services deployed via the DAG-based ``hmd deploy`` workflow (CDKTF-managed
    API Gateways, e.g. every ``hmd-ms-*``) aren't tracked by the Platform-mode
    service registry that ``write_nginx_config`` rewrites from -- their
    gateway id/stage have to be discovered directly from Floci instead. REST
    API names follow the CDKTF stack's ``base_name`` convention (e.g.
    ``device_hmd-ms-device-lib_aaa_local_reg1_hmdtr1-rest-api``), so matching
    on the repo name is enough to find it without needing the full base_name.

    Returns ``{"rest_api_id": ..., "stage_name": ..., "name": ...}`` for the
    first (name, id) match with at least one deployed stage, or ``None``.
    """
    client = _get_client("apigateway", target)
    apis = client.get_rest_apis(limit=500).get("items", [])
    matches = [a for a in apis if name_filter in a.get("name", "")]
    for api in matches:
        stages = client.get_stages(restApiId=api["id"]).get("item", [])
        if stages:
            return {
                "rest_api_id": api["id"],
                "stage_name": stages[0]["stageName"],
                "name": api["name"],
            }
    return None


def setup_service(
    service_name: str,
    image_uri: str,
    env_vars: Dict[str, str],
    api_id: str = None,
    *,
    target: Optional[FlociTarget] = None,
) -> str:
    """Deploy a service as a Lambda function and add an API Gateway route.

    If ``api_id`` is provided, adds a route to that existing API Gateway.
    Otherwise creates a fresh API Gateway named ``neuronsphere-<service_name>``.
    Per-service gateways are the default because Floci's matcher treats every
    ``{proxy+}`` resource as a global wildcard; a single shared gateway with
    multiple services would have every service catching every other service's
    traffic.

    Returns the API Gateway ID. Caller should call ``deploy_api_gateway()``
    once per returned api_id after all services are registered.
    """
    target = _resolve_target(target)

    # Deploy Lambda
    deploy_lambda_function(service_name, image_uri, env_vars, target=target)

    # Create or reuse API Gateway
    if api_id is None:
        api_id = create_api_gateway(
            api_name=f"neuronsphere-{service_name}", recreate=True, target=target
        )

    # Add route for this service
    add_api_gateway_route(api_id, service_name, service_name, target=target)

    logger.info(f"Service {service_name} routed via API Gateway {api_id}")
    return api_id


def _floci_eks_ip(name: str = K3S_CLUSTER_NAME, network: str = None) -> Optional[str]:
    """IP of the floci-eks k3s container on the local Docker network, where its
    NodePorts are reachable from sibling containers such as hmd_proxy."""
    fmt = (
        '{{with index .NetworkSettings.Networks "'
        + (network or DOCKER_NETWORK_NAME)
        + '"}}{{.IPAddress}}{{end}}'
    )
    try:
        r = subprocess.run(
            ["docker", "inspect", f"floci-eks-{name}", "--format", fmt],
            capture_output=True,
            text=True,
            timeout=5,
        )
        return r.stdout.strip() or None
    except (subprocess.SubprocessError, OSError):
        return None


# ---------------------------------------------------------------------------
# nginx routing (moved to nginx_router)
# ---------------------------------------------------------------------------
# Route generation now lives in `nginx_router`, which assembles per-owner
# fragments instead of rewriting one file -- the only shape that composes
# across a control plane plus N environments. These aliases keep the previous
# import paths working for one release.

from .nginx_router import (  # noqa: E402,F401
    add_service_route,
    configure_trino_host_route,
    reload as reload_nginx,
    write_control_plane_routes,
    write_env_routes,
    write_nginx_config,
)
