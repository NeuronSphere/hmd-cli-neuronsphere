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


def _get_client(service: str):
    return boto3.client(
        service,
        endpoint_url=FLOCI_ENDPOINT,
        aws_access_key_id=os.environ.get("AWS_ACCESS_KEY_ID", "dummykey"),
        aws_secret_access_key=os.environ.get("AWS_SECRET_ACCESS_KEY", "dummykey"),
        region_name=REGION,
    )


def wait_for_floci(timeout: int = 300, endpoint: str = None):
    """Poll Floci health endpoint until all services are available.

    :param timeout: Max seconds to wait
    :param endpoint: Base URL to check (defaults to FLOCI_ENDPOINT)
    """
    ep = endpoint or FLOCI_ENDPOINT
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

    Floci 1.5.8 hardcodes ``--kube-apiserver-arg=storage-backend=sqlite3`` when
    spawning k3s, which the kube-apiserver rejects. We work around this by
    pointing Floci at a wrapper image whose entrypoint drops the bad flag
    before calling the real k3s binary. The image lives in ``hmd-img-k3s-floci``.

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


def ensure_k3s_cluster(name: str = K3S_CLUSTER_NAME) -> Dict[str, Any]:
    """Create a Floci EKS k3s cluster (idempotent).

    Floci's EKS service in real mode (FLOCI_SERVICES_EKS_MOCK=false) starts a
    privileged k3s container per cluster on the configured Docker network,
    binding the API server to a host port from 6500-6599.

    Returns the describe_cluster response payload.
    """
    ensure_k3s_wrapper_image()
    eks = _get_client("eks")

    def _create() -> None:
        eks.create_cluster(
            name=name,
            roleArn=f"arn:aws:iam::{ACCOUNT_ID}:role/eks-role",
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
        # is missing, stopped, or not the wrapper image we expect, recreate the
        # cluster so Floci respawns it from the current
        # FLOCI_SERVICES_EKS_DEFAULT_IMAGE.
        image = _k3s_container_image(name)
        running = _k3s_container_running(name)
        if image != K3S_WRAPPER_IMAGE or not running:
            logger.warning(
                f"Existing k3s cluster {name} is stale "
                f"(image={image or 'missing'}, running={running}, "
                f"expected={K3S_WRAPPER_IMAGE}); recreating to pick up the "
                f"current wrapper image."
            )
            delete_k3s_cluster(name)
            _wait_for_cluster_gone(name)
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


def _wait_for_cluster_gone(name: str, timeout: int = 60) -> None:
    """Block until Floci reports the cluster no longer exists.

    Floci's ``delete_cluster`` tears the k3s container down asynchronously; a
    follow-up ``create_cluster`` issued too soon races the teardown and gets
    another ``ResourceInUseException``. Poll ``describe_cluster`` until it 404s,
    then force-remove any container Floci left behind.
    """
    eks = _get_client("eks")
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
    name: str = K3S_CLUSTER_NAME, timeout: int = 300
) -> Dict[str, Any]:
    """Poll describe_cluster until status == ACTIVE."""
    eks = _get_client("eks")
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


def write_kubeconfig(name: str = K3S_CLUSTER_NAME, path: Path = None) -> Path:
    """Fetch the k3s cluster's kubeconfig and write it to disk.

    Preference order:
      1. Floci's custom kubeconfig endpoint (if exposed).
      2. ``/etc/rancher/k3s/k3s.yaml`` from inside the spawned k3s container,
         with the server URL rewritten to point at the host-published port.
         This carries the real client cert/key needed to actually authenticate.
      3. Synthesized minimal kubeconfig from the EKS describe payload (last
         resort — uses a placeholder token that won't authenticate).
    """
    out_path = path or K3S_KUBECONFIG_PATH
    out_path.parent.mkdir(parents=True, exist_ok=True)

    # Try Floci's custom kubeconfig endpoint first.
    for url in [
        f"{FLOCI_ENDPOINT}/_floci/eks/{name}/kubeconfig",
        f"{FLOCI_ENDPOINT}/_floci/services/eks/clusters/{name}/kubeconfig",
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
    eks = _get_client("eks")
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


def delete_k3s_cluster(name: str = K3S_CLUSTER_NAME) -> None:
    """Delete the k3s cluster (best-effort, used by teardown)."""
    eks = _get_client("eks")
    try:
        eks.delete_cluster(name=name)
        logger.info(f"Deleted k3s cluster: {name}")
    except ClientError as e:
        code = e.response["Error"]["Code"]
        if code in ("ResourceNotFoundException", "NotFoundException"):
            logger.debug(f"k3s cluster already gone: {name}")
        else:
            logger.warning(f"Failed to delete k3s cluster {name}: {e}")


def purge_k3s_container_and_volume(name: str = K3S_CLUSTER_NAME) -> None:
    """Force-remove the Floci-spawned k3s container AND its persistent volume.

    `delete_k3s_cluster` only asks Floci to delete the cluster; the
    `/var/lib/rancher/k3s` docker volume (``floci-eks-<name>``) survives. A
    subsequent `up` respawns a container that reuses that stale sqlite-backed
    state: the new container gets a fresh random hostname and registers as a
    brand-new Node while the previous one lingers forever as NotReady, so
    StatefulSet pods pinned (via node affinity) to the dead node's
    ``hmdlabs.io/repo-instance-name`` label can never schedule.

    `down --purge` promises a clean slate, so it must drop the volume too.
    Mirrors the belt-and-suspenders cleanup in `_wait_for_cluster_gone`.
    """
    for args in (
        ["docker", "rm", "-f", f"floci-eks-{name}"],
        ["docker", "volume", "rm", "-f", f"floci-eks-{name}"],
    ):
        try:
            subprocess.run(args, capture_output=True, timeout=15)
        except (subprocess.SubprocessError, OSError) as e:
            logger.debug(f"k3s purge step {args} skipped: {e}")


def _store_local_admin_db_secret() -> None:
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
    """
    from hmd_cli_tools.hmd_cli_tools import make_standard_name
    from .bom_seeder import CORE_INSTANCE_NAME, CORE_REPO_CLASS

    did = os.environ.get("HMD_DID", "aaa")
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
            "host": "hmd_db",
            "port": 5432,
        }
    )

    sm = _get_client("secretsmanager")

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

    _put(
        make_standard_name(
            "hmd_db", "hmd-postgres-base", did, "local", region, customer_code
        )
    )
    _put(
        make_standard_name(
            CORE_INSTANCE_NAME, CORE_REPO_CLASS, "local", "local", region, customer_code
        )
    )


def provision_resources(resources: Dict[str, List[Dict[str, Any]]], local_loader=None):
    """Provision AWS resources declared by plugins.

    Creates SQS queues, S3 buckets, and the local admin postgres secret
    from the aggregated resource dictionary. Each call is idempotent.
    Per-DB user secrets are created later by ms-dbaccount via
    `provision_plugin_databases()`. DynamoDB tables are created lazily
    by `hmd-entity-storage`'s `DynamoDbEngine` on first service
    invocation, so they have the correct attributes, key schema, and
    GSIs.
    """
    _store_local_admin_db_secret()

    # Create SQS queues
    sqs = _get_client("sqs")
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
    bucket_names.append(f"hmd.{ACCOUNT_ID}.{hmd_region}.tfstate")

    s3 = _get_client("s3")
    for name in bucket_names:
        try:
            s3.create_bucket(
                Bucket=name,
                CreateBucketConfiguration={"LocationConstraint": REGION},
            )
            logger.info(f"Created S3 bucket: {name}")
        except ClientError as e:
            code = e.response["Error"]["Code"]
            if code in ("BucketAlreadyOwnedByYou", "BucketAlreadyExists"):
                logger.debug(f"S3 bucket already exists: {name}")
            else:
                raise


def _local_db_secret_base() -> str:
    """Compute the make_standard_name secret_base for the local admin DB.

    Mirrors `_store_local_admin_db_secret`: instance_name=hmd_db,
    repo_class=hmd-postgres-base. The resulting prefix is what
    `hmd-ms-dbaccount`'s `do_create_db_account` uses for both the admin
    `_db-secret` and the per-user `_<username>` credential secrets.
    """
    from hmd_cli_tools.hmd_cli_tools import make_standard_name

    return make_standard_name(
        "hmd_db",
        "hmd-postgres-base",
        os.environ.get("HMD_DID", "aaa"),
        "local",
        os.environ.get("HMD_REGION", "reg1"),
        # From hmd.env, fallback "none"; see local_customer_code / _store_local_admin_db_secret.
        local_customer_code(),
    )


CORE_DATABASES = [
    {"db_name": "hmd_ms_naming", "username": "hmd_ms_naming"},
    {"db_name": "hmd_ms_deployment", "username": "hmd_ms_deployment"},
]


def _post_create_db_account(did: str, db_name: str, username: str, origin: str) -> None:
    """POST to ms-dbaccount to idempotently create a database/user.

    `origin` is a label (plugin name or "core") used in log lines. Retries
    on 404 ``Invalid API id`` to absorb the brief gap between
    ``deploy_api_gateway()`` and Floci having the route fully live. Lets
    KeyboardInterrupt propagate so Ctrl+C aborts the provisioning loop
    promptly instead of running through every plugin's per-call timeout.
    """
    payload = {
        "db_repo_class": "hmd-postgres-base",
        "db_instance_name": "hmd_db",
        "db_deployment_id": did,
        "db_name": db_name,
        "username": username,
    }
    url = "http://localhost/hmd_ms_dbaccount/api/create_db_account"
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


def provision_plugin_databases(local_loader) -> None:
    """Call hmd-ms-dbaccount to create every local DB and user.

    POSTs to `http://localhost/ms-dbaccount/api/create_db_account` with
    `db_instance_name=hmd_db`, `db_repo_class=hmd-postgres-base`. dbaccount
    detects `HMD_ENVIRONMENT=local` and uses `password=username` so the
    resulting secret matches the convention every local service compose
    config hardcodes.

    Provisions both `CORE_DATABASES` (ms-naming, ms-deployment) and every
    enabled plugin's `resources.databases` entries. Idempotent on warm
    restarts: dbaccount returns `"No secret created."` when both the user
    and secret already exist.
    """
    did = os.environ.get("HMD_DID", "aaa")

    for db in CORE_DATABASES:
        _post_create_db_account(did, db["db_name"], db["username"], origin="core")

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
            _post_create_db_account(did, db_name, username, origin=plugin_name)


def _psql(sql: str, dbname: str = "postgres") -> subprocess.CompletedProcess:
    """Run a SQL statement in the hmd_db container as the postgres superuser."""
    return subprocess.run(
        [
            "docker",
            "exec",
            "hmd_db",
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


def ensure_core_databases_direct(timeout: int = 240) -> None:
    """Guarantee the foundational control-plane databases/users exist.

    ms-naming and ms-deployment are foundational to the local control plane and
    their Lambdas connect with the local ``password == username`` convention.
    The cloud-parity dbaccount path (`provision_plugin_databases`) can be flaky
    against Floci's freshly-started API Gateway, so this creates the core
    database + login role directly via psql as a deterministic fallback. Waits
    for PostgreSQL to accept connections first, then runs idempotently.
    """
    # Wait for postgres to accept connections (container may still be running
    # its entrypoint init on a cold boot).
    start = time.time()
    while time.time() - start < timeout:
        if _psql("SELECT 1").returncode == 0:
            break
        time.sleep(2)
    else:
        logger.warning(
            f"hmd_db not accepting connections after {timeout}s; "
            f"cannot ensure core databases directly"
        )
        return

    for db in CORE_DATABASES:
        name = db["db_name"]
        user = db["username"]
        role_sql = (
            f"DO $do$ BEGIN "
            f"IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '{user}') THEN "
            f"CREATE ROLE \"{user}\" LOGIN PASSWORD '{user}'; "
            f"END IF; END $do$;"
        )
        r = _psql(role_sql)
        if r.returncode != 0:
            logger.warning(f"Ensuring role {user} failed: {r.stderr.strip()}")
        # CREATE DATABASE cannot run inside a DO block / transaction, so guard
        # it with an existence check.
        exists = _psql(f"SELECT 1 FROM pg_database WHERE datname = '{name}'")
        if exists.stdout.strip() != "1":
            c = _psql(f'CREATE DATABASE "{name}" OWNER "{user}";')
            if c.returncode != 0:
                logger.warning(f"Creating database {name} failed: {c.stderr.strip()}")
        _psql(f'GRANT ALL PRIVILEGES ON DATABASE "{name}" TO "{user}";')
        logger.info(f"Ensured core database/user '{name}' (direct psql)")


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


def _image_cached(image_uri: str) -> bool:
    """Return True if the image is present in the local docker cache."""
    result = subprocess.run(
        ["docker", "image", "inspect", image_uri],
        capture_output=True,
    )
    return result.returncode == 0


def resolve_image_uri(repo_name: str, version: str) -> Optional[str]:
    """Find a locally-cached Docker image URI for a repo/version.

    Tries each candidate prefix in order and returns the first that
    `docker image inspect` finds. Returns None if none are cached.

    Candidates (in priority):
      1. ``$HMD_CONTAINER_REGISTRY/<repo>:<version>`` (matches `hmd build`)
      2. ``$HMD_LOCAL_NS_CONTAINER_REGISTRY/<repo>:<version>``
      3. ``ghcr.io/neuronsphere/<repo>:<version>`` (default registry)
      4. ``<repo>:<version>`` (bare tag)

    Steps 1 and 2 are skipped when the corresponding env var is unset.
    """
    candidates: List[str] = []
    seen: set = set()

    def _add(uri: str) -> None:
        if uri and uri not in seen:
            candidates.append(uri)
            seen.add(uri)

    build_registry = os.environ.get("HMD_CONTAINER_REGISTRY")
    if build_registry:
        _add(f"{build_registry}/{repo_name}:{version}")

    local_registry = os.environ.get("HMD_LOCAL_NS_CONTAINER_REGISTRY")
    if local_registry:
        _add(f"{local_registry}/{repo_name}:{version}")

    _add(f"ghcr.io/neuronsphere/{repo_name}:{version}")
    _add(f"{repo_name}:{version}")

    for uri in candidates:
        if _image_cached(uri):
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
) -> str:
    """Deploy a Docker image as a Lambda function in Floci.

    Returns the function ARN.
    """
    image_uri = _ensure_local_image(image_uri)
    client = _get_client("lambda")

    function_config = {
        "FunctionName": function_name,
        "PackageType": "Image",
        "Code": {"ImageUri": image_uri},
        "Role": f"arn:aws:iam::{ACCOUNT_ID}:role/lambda-role",
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
    api_name: str = "neuronsphere-local", recreate: bool = False
) -> str:
    """Create or get a REST API Gateway in Floci.

    When ``recreate`` is True, any existing API Gateway with the same name
    is deleted first so we start with a clean resource tree. This is
    important because Floci's resource matcher appears to prefer the
    earliest-created `{proxy+}` resource on a tie, so accumulated stale
    routes from previous runs can hijack traffic for newly-registered
    services.

    Returns the REST API ID.
    """
    client = _get_client("apigateway")

    apis = client.get_rest_apis()
    for api in apis.get("items", []):
        if api["name"] == api_name:
            if recreate:
                client.delete_rest_api(restApiId=api["id"])
                logger.info(f"Deleted existing API Gateway: {api['id']}")
            else:
                logger.info(f"Found existing API Gateway: {api['id']}")
                return api["id"]

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
) -> None:
    """Add routes at the API Gateway root that proxy to a Lambda function.

    Creates `/` (root) and `/{proxy+}` integrations for every HTTP method.
    Designed for one-Lambda-per-gateway: the gateway has no service-name
    prefix in its path, so nginx must strip the service prefix before
    proxying to Floci. The Lambda then receives clean `/api/...` paths
    (rather than `/{service_name}/api/...`), which FastAPI/hmd-ms-base
    routes natively.
    """
    client = _get_client("apigateway")
    lambda_client = _get_client("lambda")

    # Get root resource ID
    resources = client.get_resources(restApiId=api_id)
    root_id = None
    existing_paths = {}
    for r in resources["items"]:
        existing_paths[r["path"]] = r["id"]
        if r["path"] == "/":
            root_id = r["id"]

    # Get Lambda function ARN
    func = lambda_client.get_function(FunctionName=function_name)
    function_arn = func["Configuration"]["FunctionArn"]
    integration_uri = (
        f"arn:aws:apigateway:{REGION}:lambda:path"
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


def deploy_api_gateway(api_id: str, stage_name: str = "local") -> str:
    """Deploy the API Gateway to a stage.

    Returns the invoke URL for use within the Docker network.
    """
    client = _get_client("apigateway")
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
        f"{FLOCI_INTERNAL_ENDPOINT}/restapis/{api_id}/{stage_name}/_user_request_"
    )
    logger.info(f"API Gateway deployed: {invoke_url}")
    return invoke_url


def get_api_gateway_url(api_id: str, stage: str = "local") -> str:
    """Get the invoke URL for an API Gateway stage.

    Returns the URL usable within the Docker network.
    """
    return f"{FLOCI_INTERNAL_ENDPOINT}/restapis/{api_id}/{stage}/_user_request_"


def setup_service(
    service_name: str,
    image_uri: str,
    env_vars: Dict[str, str],
    api_id: str = None,
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
    # Deploy Lambda
    deploy_lambda_function(service_name, image_uri, env_vars)

    # Create or reuse API Gateway
    if api_id is None:
        api_id = create_api_gateway(
            api_name=f"neuronsphere-{service_name}", recreate=True
        )

    # Add route for this service
    add_api_gateway_route(api_id, service_name, service_name)

    logger.info(f"Service {service_name} routed via API Gateway {api_id}")
    return api_id


def write_nginx_config(
    services: Dict[str, str],
    config_path: Path,
    api_id: str = None,
    stage: str = "local",
    extra_locations: Dict[str, str] = None,
):
    """Write nginx config that proxies each service via API Gateway.

    Args:
        services: Mapping of service_name to api_id.
        config_path: Path to write the nginx config file.
        api_id: Shared API Gateway ID (overrides per-service values).
        stage: API Gateway stage name.
        extra_locations: Optional mapping of {path: upstream_url} for routes
            that bypass API Gateway (e.g., /argo/ → k3s NodePort).
    """

    def _api_location(path: str, gw_id: str) -> str:
        # Strip the `/{path}` prefix before proxying so the Lambda receives
        # clean paths like `/api/foo` instead of `/{path}/api/foo` (which
        # FastAPI would 404).
        return f"""        location /{path}/ {{
            proxy_pass http://neuronsphere:4566/restapis/{gw_id}/{stage}/_user_request_/;
            proxy_set_header X-Real-IP $remote_addr;
            proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
            proxy_set_header X-Forwarded-Proto $scheme;
            proxy_read_timeout 300s;
            proxy_connect_timeout 75s;
        }}"""

    location_blocks = []
    routed_paths = set()
    for service_name in services:
        gw_id = api_id or services[service_name]
        location_blocks.append(_api_location(service_name, gw_id))
        routed_paths.add(service_name)

    # Instance-name aliases: the robot suites build their URL from
    # HMD_INSTANCE_NAME, which may be `ms-deployment`, `ms_deployment`, or
    # `hmd-ms-deployment` depending on how bender is invoked. Route all of them
    # to the same gateway so the acceptance suite resolves regardless.
    _SERVICE_ALIASES = {
        "hmd_ms_deployment": ["ms-deployment", "ms_deployment", "hmd-ms-deployment"],
        "hmd_ms_naming": ["ms-naming", "ms_naming", "hmd-ms-naming"],
    }
    for canonical, aliases in _SERVICE_ALIASES.items():
        if canonical in services:
            gw_id = api_id or services[canonical]
            for alias in aliases:
                if alias not in routed_paths:
                    location_blocks.append(_api_location(alias, gw_id))
                    routed_paths.add(alias)

    # Argo is enabled by default; expose its UI/API at /argo/.
    if extra_locations is None and os.environ.get(
        "HMD_LOCAL_NEURONSPHERE_ENABLE_ARGO", "true"
    ).lower() not in ("false", "0", "no"):
        argo_upstream = os.environ.get(
            "HMD_LOCAL_ARGO_UPSTREAM", "http://host.docker.internal:30246"
        )
        extra_locations = {"argo": argo_upstream}

    for path, upstream in (extra_locations or {}).items():
        path_clean = path.strip("/")
        # Preserve trailing slash semantics: location /argo/ → upstream/
        upstream_with_slash = upstream.rstrip("/") + "/"
        location_blocks.append(
            f"""        location /{path_clean}/ {{
            proxy_pass {upstream_with_slash};
            proxy_set_header X-Real-IP $remote_addr;
            proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
            proxy_set_header X-Forwarded-Proto $scheme;
            proxy_set_header Host $host;
            proxy_read_timeout 300s;
            proxy_connect_timeout 75s;
        }}"""
        )

    locations = "\n".join(location_blocks)
    config = f"""events {{
    worker_connections 1024;
}}
http {{
    server {{
        listen 80 default_server;
        server_name _;
{locations}
        location / {{
            return 404 '{{"error": "no route defined"}}';
        }}
    }}
}}
"""
    os.makedirs(config_path.parent, exist_ok=True)
    with open(config_path, "w") as f:
        f.write(config)
    logger.info(f"Wrote nginx config to {config_path}")
