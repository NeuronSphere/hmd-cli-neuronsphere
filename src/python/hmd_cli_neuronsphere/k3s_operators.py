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
# Core docker-network services charts reach by name from inside k3s (same as the
# cloud in-network hostnames): the nginx edge proxy fronting the microservice
# Lambdas, and the shared Postgres. Registered in CoreDNS so pods resolve them by
# name instead of a churny container IP baked into config.
_PROXY_CONTAINER = os.environ.get("HMD_LOCAL_PROXY_CONTAINER", "hmd_proxy")
_DB_CONTAINER = os.environ.get("HMD_LOCAL_DB_CONTAINER", "hmd_db")
# Local JanusGraph (Gremlin server, substitutes for Neptune). Trino's graph catalog
# (connector nsgraph) connects to it by name from inside k3s, so it needs a CoreDNS
# record just like the proxy/db above.
_GRAPH_CONTAINER = os.environ.get("HMD_LOCAL_GRAPH_CONTAINER", "global-graph")

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
# environment (via the compose files), not this CLI process's, so it's not
# a usable override here.
_FLOCI_EKS_NETWORK = DOCKER_NETWORK_NAME
# In-network Floci endpoint (resolvable from pods once the CoreDNS record exists).
_FLOCI_INTERNAL_ENDPOINT = os.environ.get(
    "FLOCI_INTERNAL_ENDPOINT", "http://neuronsphere:4566"
)


def _run(args: List[str], env=None, **kwargs) -> subprocess.CompletedProcess:
    """Run a kubectl/helm command against a specific environment's cluster.

    Every environment has its own k3s cluster, so the cluster a command lands on
    must come from the environment being operated on -- never from an ambient
    process-wide ``KUBECONFIG``. Passing the kubeconfig explicitly per call is
    what keeps two concurrent environments from stepping on each other; a call
    that skips this helper silently targets whichever cluster the process
    happens to point at.
    """
    proc_env = dict(os.environ)
    if env is not None:
        proc_env["KUBECONFIG"] = str(env.kubeconfig)
    kwargs.setdefault("capture_output", True)
    kwargs.setdefault("text", True)
    return subprocess.run(args, env=proc_env, **kwargs)


def _rds_container_for(env=None) -> Optional[str]:
    """The real Docker name of the Postgres container backing ``env``.

    Needed because the database is the one canonical name that is *not* a
    container name. Floci spawns RDS backends with opaque names
    (``floci-rds-db-<HEX>-<suffix>``) and the CLI gives them a network alias
    (``hmd_db``, ``hmd_db-<slug>``) -- but ``docker inspect`` resolves container
    names, never aliases, so asking it for the alias simply fails and the record
    is skipped. That is why `hmd_db` was absent from the live CoreDNS ConfigMap
    while `global-graph` and `hmd_proxy`, which *are* container names, were
    present.

    Resolved through the same label lookup Floci's own naming requires
    (``io.floci.account`` + ``io.floci.resource-id``).
    """
    from .floci_deployer import (
        control_plane_target,
        env_target,
        rds_container_name,
    )

    try:
        if env is not None and not getattr(env, "legacy_layout", False):
            from . import bom_seeder

            target = env_target(env)
            identifier = bom_seeder.env_db_identifier(env)
        else:
            from .bootstrap_dag import control_plane_db_identifier

            target = control_plane_target()
            identifier = control_plane_db_identifier(target)
        return rds_container_name(identifier, target) or None
    except Exception as e:  # never abort the CoreDNS pass over this
        logger.debug(f"Could not resolve the RDS container for CoreDNS: {e}")
        return None


def _resolve_floci_ip(container: str) -> Optional[str]:
    """Return the given container's IP on the k3s Docker network."""
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


def refresh_coredns_records(env=None) -> None:
    """Re-apply the canonical-name records after a backing container appears.

    The records are written once during `provision_k3s_operators`, which runs
    before the environment's database and graph containers exist -- any name
    that does not resolve on the Docker network at that moment is skipped, so
    `hmd_db` was simply missing and every chart addressing it failed to resolve.
    Callers invoke this once the container is up; it rewrites the whole
    ConfigMap, so it is idempotent and safe to repeat.
    """
    _ensure_coredns_floci_entry(env)


def _ensure_coredns_floci_entry(env=None) -> None:
    """Map the canonical NeuronSphere hostnames to this environment's containers.

    Applies a ``coredns-custom`` ConfigMap (the k3s-supported extension point,
    imported via ``import /etc/coredns/custom/*.server``) with a per-hostname
    server block. Uses separate server blocks (not a second ``hosts`` plugin in
    ``.:53``, which would crash CoreDNS). Best-effort; logs and returns on any
    failure.

    This is what lets cloud Helm charts run unmodified in every environment.
    Inside environment ``<slug>``'s cluster:

    ==========================  =========================
    name in-cluster             resolves to
    ==========================  =========================
    neuronsphere                floci-<slug>
    neuronsphere-workload       floci-<slug>
    hmd_db                      hmd_db-<slug>
    global-graph                global-graph-<slug>
    neuronsphere-control        the control-plane floci
    hmd_proxy                   the control-plane proxy
    ==========================  =========================

    So a chart's unmodified ``AWS_ENDPOINT_URL=http://neuronsphere:4566``, its
    JDBC URL against ``hmd_db``, and its Gremlin endpoint on ``global-graph`` are
    all automatically scoped to that environment's own account and databases.
    """
    # Canonical name -> the container that should answer it in this environment.
    if env is not None and not getattr(env, "legacy_layout", False):
        env_floci = env.floci_container
        env_db = env.db_container
        env_graph = env.graph_container
    else:
        env_floci, env_db, env_graph = _FLOCI_CONTAINER, _DB_CONTAINER, _GRAPH_CONTAINER

    floci_ip = _resolve_floci_ip(env_floci)
    if not floci_ip:
        logger.warning(f"Could not resolve IP for {env_floci}; skipping CoreDNS record")
        return

    def _block(host: str, ip: str) -> str:
        return (
            f"{host}:53 {{\n"
            f"    hosts {{\n        {ip} {host}\n        fallthrough\n    }}\n}}\n"
        )

    entries = [
        ("neuronsphere", floci_ip),
        ("neuronsphere-workload", floci_ip),
    ]
    # Canonical name -> this environment's container (best-effort: a name that
    # doesn't resolve on the Docker network is simply skipped).
    #
    # `hmd_db` resolves through the network alias the CLI puts on the Floci-spawned
    # RDS container (`environments._alias_environment_database`), not through a
    # compose container name -- Floci names what it spawns opaquely. Aliasing the
    # backend directly is also what keeps the port at 5432 for unmodified cloud
    # charts, instead of Floci's 7001-7099 RDS proxy range.
    aliased = [
        (_GRAPH_CONTAINER, env_graph),
        # The control plane is shared; charts that need it address it explicitly.
        ("neuronsphere-control", _FLOCI_CONTAINER),
        (_PROXY_CONTAINER, _PROXY_CONTAINER),
    ]
    for canonical, container in aliased:
        ip = _resolve_floci_ip(container)
        if ip:
            entries.append((canonical, ip))
    # The database, under *both* names it is addressed by. `hmd_db` is what
    # unmodified cloud charts use; `hmd_db-<slug>` is what the connection secrets
    # carry, because on the Docker network plain `hmd_db` is the control plane's
    # database and an environment's Lambdas must not reach that one. Both have to
    # answer inside the cluster, since a chart and the secret it consumes
    # disagree about which name they use.
    db_container = _rds_container_for(env)
    db_ip = _resolve_floci_ip(db_container) if db_container else None
    if db_ip:
        entries.append((_DB_CONTAINER, db_ip))
        if env_db != _DB_CONTAINER:
            entries.append((env_db, db_ip))
    else:
        logger.warning(
            f"Could not resolve the database container for '{getattr(env, 'slug', 'control plane')}'; "
            f"{_DB_CONTAINER}/{env_db} will not resolve inside the cluster and "
            f"every chart addressing the database by name will fail to connect."
        )

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
        apply = _run(["kubectl", "apply", "-f", cm_path], env)
        if apply.returncode != 0:
            logger.warning(f"CoreDNS record apply failed: {apply.stderr}")
            return
        # Roll CoreDNS so it reloads the custom config immediately.
        _run(
            ["kubectl", "-n", "kube-system", "rollout", "restart", "deploy", "coredns"],
            env,
        )
        _run(
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
            env,
        )
        logger.debug("CoreDNS: " + ", ".join(f"{host}->{ip}" for host, ip in entries))
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

# The class the cloud's charts ask for. Every NeuronSphere repo renders its
# Ingress for the AWS ALB controller -- `spec.ingressClassName: alb` on newer
# charts, the legacy `kubernetes.io/ingress.class: alb` annotation on older ones.
# Locally we make Traefik *answer to that class* rather than editing the charts,
# the same way Floci answers to the AWS APIs and CoreDNS answers to the
# in-network hostnames. That is what lets cloud charts deploy here unmodified.
_ALB_INGRESS_CLASS = os.environ.get("HMD_LOCAL_INGRESS_CLASS", "alb")
_TRAEFIK_CONTROLLER = "traefik.io/ingress-controller"
# Traefik's own arg. It is matched against BOTH `spec.ingressClassName` and the
# legacy annotation, which is why an IngressClass object alone is not enough:
# an annotation-only Ingress never consults the IngressClass API at all.
_TRAEFIK_INGRESS_CLASS_ARG = (
    f"--providers.kubernetesingress.ingressclass={_ALB_INGRESS_CLASS}"
)


def _k3s_container(cluster, env=None) -> str:
    """The k3s container's Docker name for this cluster.

    Account-qualified outside the default account (Floci 2.0), so an
    environment's is `floci-eks-<account>.<cluster>` -- see
    floci_deployer.k3s_container_name.
    """
    from .floci_deployer import control_plane_target, env_target, k3s_container_name

    target = env_target(env) if env is not None else control_plane_target()
    return k3s_container_name(cluster, target)


def _patch_traefik_manifest(cluster: str, env=None) -> None:
    """Make the baked-in Traefik manifest schedulable and ALB-classed.

    Two edits, both applied to the *file*: the Deployment is owned by a k3s
    Addon, which reverts any live ``kubectl patch``, so the manifest is the only
    durable place to change it. Editing it changes the content hash, which is
    what makes the addon controller re-apply.

    1. **Drop ``hostPort: 80``/``443``.** k3s ServiceLB creates ``svclb-*`` pods
       for every ``type: LoadBalancer`` service, and those are
       ``system-node-critical``. A chart exposing port 443 (the OTEL collector
       gateway does) therefore *preempts* Traefik -- priority 0 -- off host port
       443 permanently: ``0/1 nodes are available: 1 node(s) didn't have free
       ports for the requested pod ports``. Traefik needs no host port here; it
       is reached through a NodePort (see ``nginx_router.ingress_upstream``).
    2. **Set the ingress class to ``alb``** so cloud charts resolve unmodified.

    Idempotent: both edits match nothing on a second run, and on a future image
    that already ships this way they are no-ops.

    ``env`` is required for anything but the control plane. Resolving the
    container without it yields the *unqualified* name, which does not exist for
    an environment's cluster (Floci 2.0 qualifies it by account) -- so every
    ``docker exec`` here failed, the return codes were discarded, and the whole
    function was a silent no-op. The visible symptom was Traefik stuck Pending
    with "didn't have free ports for the requested pod ports", several layers
    from anything naming this function.
    """
    container = _k3s_container(cluster, env)

    # 1. Strip the host ports that make Traefik unschedulable.
    stripped = _docker_exec(
        container,
        [
            "sed",
            "-i",
            r"/^[[:space:]]*hostPort: \(80\|443\)$/d",
            _TRAEFIK_MANIFEST_PATH,
        ],
    )
    if stripped is None or stripped.returncode != 0:
        detail = (
            (stripped.stderr or "").strip()
            if stripped is not None
            else "docker unavailable"
        )
        logger.warning(
            f"Could not patch the Traefik manifest in {container} ({detail}); "
            f"the ingress controller will stay Pending if any LoadBalancer "
            f"service holds host port 80 or 443, and no UI will be reachable."
        )
        return

    # 2. Add the ingress-class arg, unless it is already there. Inserted after
    #    the container's `args:` key, reusing its indentation. The manifest has
    #    exactly one `args:` line, so a plain substitution needs no line address
    #    -- which matters because k3s ships busybox sed, not GNU sed.
    check = _docker_exec(
        container,
        ["grep", "-q", "--", _TRAEFIK_INGRESS_CLASS_ARG, _TRAEFIK_MANIFEST_PATH],
    )
    if check is not None and check.returncode != 0:
        added = _docker_exec(
            container,
            [
                "sed",
                "-i",
                r"s|^\([[:space:]]*\)args:$|\1args:\n\1  - \""
                + _TRAEFIK_INGRESS_CLASS_ARG
                + r"\"|",
                _TRAEFIK_MANIFEST_PATH,
            ],
        )
        if added is None or added.returncode != 0:
            logger.warning(
                f"Could not set Traefik's ingress class in {container}; "
                f"Ingresses declaring class '{_ALB_INGRESS_CLASS}' will not be served."
            )


def _docker_exec(container: str, args: List[str]):
    """Run a command inside a container; None when docker itself is unavailable."""
    try:
        return subprocess.run(
            ["docker", "exec", container, *args],
            capture_output=True,
            text=True,
            timeout=30,
        )
    except (subprocess.SubprocessError, OSError) as e:
        logger.warning(f"Could not exec in {container}: {e}")
        return None


def _ensure_alb_ingress_class(env=None) -> None:
    """Register an ``alb`` IngressClass backed by the local Traefik controller.

    Complements the Traefik arg set in :func:`_patch_traefik_manifest`: the arg
    is what actually makes Traefik serve these Ingresses, while this object is
    what makes ``spec.ingressClassName: alb`` a live reference rather than a
    dangling one, so ``kubectl get ingress`` reports the class correctly.

    Deliberately **not** marked the default class: k3s already ships a default
    ``traefik`` IngressClass, and two defaults make the API server reject
    class-less Ingresses as ambiguous.
    """
    ingress_class = {
        "apiVersion": "networking.k8s.io/v1",
        "kind": "IngressClass",
        "metadata": {
            "name": _ALB_INGRESS_CLASS,
            "labels": {"app.kubernetes.io/managed-by": "hmd-cli-neuronsphere"},
        },
        "spec": {"controller": _TRAEFIK_CONTROLLER},
    }
    result = _run(["kubectl", "apply", "-f", "-"], env, input=json.dumps(ingress_class))
    if result.returncode != 0:
        logger.warning(
            f"Could not register the '{_ALB_INGRESS_CLASS}' IngressClass: "
            f"{result.stderr.strip()}"
        )
    else:
        logger.debug(
            f"IngressClass '{_ALB_INGRESS_CLASS}' -> {_TRAEFIK_CONTROLLER} registered"
        )


def _ensure_ingress_controller(timeout: int = 120, env=None) -> None:
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
    migrated = _run(["helm", "uninstall", "traefik", "--namespace", "kube-system"], env)
    if migrated.returncode == 0:
        logger.debug(
            "Removed legacy runtime-installed Traefik release "
            "(ingress is now baked into the k3s image)"
        )

    if os.environ.get(_INGRESS_ENABLE_ENV, "true").lower() in ("false", "0", "no"):
        logger.debug(
            "Ingress controller disabled via "
            + _INGRESS_ENABLE_ENV
            + "; removing baked-in Traefik"
        )
        _run(
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
            env,
        )
        return

    # Re-apply the baked-in manifest from inside the k3s node container
    # (idempotent): a no-op if k3s's own addon controller already applied it
    # at boot, but restores resources a prior disable removed -- k3s only
    # (re-)applies a manifest on a content-hash change, it doesn't otherwise
    # reconcile resources deleted out-of-band.
    from .floci_deployer import K3S_CLUSTER_NAME

    cluster = env.k3s_cluster if env is not None else K3S_CLUSTER_NAME
    _patch_traefik_manifest(cluster, env)
    subprocess.run(
        [
            "docker",
            "exec",
            _k3s_container(cluster, env),
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
        result = _run(
            [
                "kubectl",
                "-n",
                "kube-system",
                "rollout",
                "status",
                "deploy/traefik",
                "--timeout=10s",
            ],
            env,
        )
        if result.returncode == 0:
            logger.debug("Ingress controller (Traefik) ready")
            _ensure_alb_ingress_class(env)
            return
        time.sleep(4)
    # Printed, not just logged: a Traefik that never schedules leaves every
    # Ingress-exposed UI silently unreachable, with nothing in the `up` output
    # pointing at the cause.
    logger.warning("Ingress controller (Traefik) did not become ready in time")
    print(
        "  Warning: the ingress controller (Traefik) did not become ready; "
        "Ingress-exposed UIs (Airflow, Argo) will be unreachable."
    )


def _ext_secrets_passes(env=None) -> List[Dict[str, Any]]:
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
    from .floci_deployer import account_access_key

    # The account these stores resolve their lookups in. This install runs on
    # every `up`, including the restart fast path that skips the BOM -- so
    # leaving it at the chart's `test` default would quietly revert an
    # environment's operator to the control plane's account after any restart,
    # and every ExternalSecret would start reporting "Secret does not exist"
    # for secrets that exist.
    account = account_access_key(env)
    base: Dict[str, Any] = {
        "installCRDs": False,  # CRDs come from hmd-inf-ext-secrets-crds (first)
        "clusterSecretStore": {"enabled": False},
        "parameterStoreSecretStore": {"enabled": False},
        "dockerRepoSecret": {"enabled": False},
        "extraEnv": [
            {"name": "AWS_ENDPOINT_URL", "value": _FLOCI_INTERNAL_ENDPOINT},
            {"name": "AWS_ACCESS_KEY_ID", "value": account},
            {"name": "AWS_SECRET_ACCESS_KEY", "value": account},
            {"name": "AWS_REGION", "value": "us-west-2"},
        ],
    }
    store = {
        **base,
        "aws_region": "us-west-2",
        # `localAccessKeyId` is the operative one: an AWS ClusterSecretStore with
        # `secretRef` auth reads its credentials from the Secret the chart
        # renders from this value, and never consults the operator pod's
        # environment. Setting only `extraEnv` above looks right and changes
        # nothing.
        "clusterSecretStore": {
            "enabled": True,
            "local": True,
            "name": "aws-secrets-manager",
            "localAccessKeyId": account,
        },
        "parameterStoreSecretStore": {
            "enabled": True,
            "local": True,
            "name": "aws-parameter-store",
            "localAccessKeyId": account,
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


def cluster_incarnation_id(env=None) -> Optional[str]:
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
        result = _run(
            [
                "kubectl",
                "get",
                "namespace",
                "kube-system",
                "-o",
                "jsonpath={.metadata.uid}",
            ],
            env,
            timeout=10,
        )
    except (subprocess.SubprocessError, OSError):
        return None
    uid = result.stdout.strip()
    return uid if result.returncode == 0 and uid else None


def live_helm_releases(env=None) -> Optional[set]:
    """Names of the Helm releases currently installed on this env's cluster.

    Helm stores one Secret per release revision, labelled ``owner=helm`` with
    the release name in ``metadata.labels.name``. Listing those is cheaper than
    shelling out to ``helm list -A`` and needs no helm binary on the host.

    Used by ``env_reconcile.compute_plan`` to catch the case the deployment
    graph cannot see: an instance the graph still calls ``DEPLOYED`` whose
    release is gone from the cluster.

    Returns ``None`` -- explicitly "could not read the cluster", as distinct
    from the empty set "the cluster has no releases" -- on any kubectl failure,
    so callers can decline to act rather than propose a wholesale redeploy.
    """
    try:
        result = _run(
            [
                "kubectl",
                "get",
                "secrets",
                "--all-namespaces",
                "-l",
                "owner=helm",
                "-o",
                'jsonpath={range .items[*]}{.metadata.labels.name}{"\\n"}{end}',
            ],
            env,
            timeout=30,
        )
    except (subprocess.SubprocessError, OSError) as e:
        logger.warning(f"Could not list Helm releases: {e}")
        return None
    if result.returncode != 0:
        logger.warning(f"Could not list Helm releases: {(result.stderr or '').strip()}")
        return None
    return {line.strip() for line in result.stdout.splitlines() if line.strip()}


def helm_release_name(repo_instance_name: str, env) -> str:
    """The Helm release ``hmd deploy`` installs for one BOM entry.

    Both the release and its namespace are named
    ``<repo_instance_name>-<deployment_id>`` -- e.g. the ``redis`` entry in the
    ``local`` environment becomes the ``redis-local`` release in the
    ``redis-local`` namespace.
    """
    return f"{repo_instance_name}-{env.deployment_id}"


def _wait_for_node_ready(timeout: int = 120, env=None) -> bool:
    """Wait for at least one k3s node to report Ready.

    ``wait_for_k3s_ready`` only confirms the Floci EKS API status is ACTIVE; the
    kubelet/CNI/CoreDNS may still be warming up. Installing operators before a
    node is schedulable makes ``helm --wait`` fail, so gate on real node
    readiness here.
    """
    deadline = time.time() + timeout
    while time.time() < deadline:
        result = _run(["kubectl", "get", "nodes", "--no-headers"], env)
        for line in result.stdout.splitlines():
            cols = line.split()
            if len(cols) >= 2 and cols[1] == "Ready":
                return True
        time.sleep(4)
    logger.warning("No k3s node became Ready in time; installing operators anyway")
    return False


def _ensure_node_topology_labels(env=None) -> None:
    """Label the k3s node with zone/region topology + the core compute identity.

    Cloud EKS nodes carry ``topology.kubernetes.io/{zone,region}`` labels;
    the single local k3s node has none. Charts with a ``topologySpreadConstraints``
    keyed on zone (e.g. ClickHouse's Keeper StatefulSet) then find "0/1 nodes
    match" and every replica beyond the first sticks in Pending forever. Since
    there's only one node locally, any single zone value trivially satisfies
    max-skew for all such constraints.

    Cloud node groups are also labelled ``hmdlabs.io/repo-instance-name=<node
    group>`` and workload charts pin to their ``compute``/``worker-compute``
    dependency with a *required* ``nodeAffinity`` on that key (e.g. Hive
    Metastore, Trino). Locally those dependencies resolve to the core instance
    (``CORE_INSTANCE_NAME``), so label the single node with it — otherwise every
    such pod is ``FailedScheduling: didn't match Pod's node affinity/selector``.
    """
    from .bom_seeder import CORE_INSTANCE_NAME

    # The instance name is identical in every environment (repo_instance is
    # unique by name per Environment), so the affinity label is too.
    instance_name = env.core_instance_name if env is not None else CORE_INSTANCE_NAME

    result = _run(["kubectl", "get", "nodes", "-o", "name"], env)
    for line in result.stdout.splitlines():
        node = line.strip()
        if not node:
            continue
        _run(
            [
                "kubectl",
                "label",
                node,
                "topology.kubernetes.io/zone=local",
                "topology.kubernetes.io/region=local",
                f"hmdlabs.io/repo-instance-name={instance_name}",
                "--overwrite",
            ],
            env,
        )


def _clean_stale_nodes(env=None) -> None:
    """Delete ``NotReady`` ghost node registrations left in the k3s datastore.

    Floci reuses the k3s data volume across cluster delete/recreate, so each
    recreation leaves the previous node registered but NotReady. Those ghosts —
    and the node-affine local-path PVs bound to them — poison scheduling for new
    pods ("didn't match PersistentVolume's node affinity"). Remove them, and the
    orphaned PVs whose node no longer exists, so fresh PVCs bind to the live node.
    """
    result = _run(["kubectl", "get", "nodes", "--no-headers"], env)
    live = set()
    for line in result.stdout.splitlines():
        cols = line.split()
        if len(cols) >= 2:
            if cols[1] == "Ready":
                live.add(cols[0])
            else:
                _run(
                    ["kubectl", "delete", "node", cols[0], "--ignore-not-found"],
                    env,
                )
    # Release PVs pinned (node affinity) to a node that no longer exists so their
    # PVCs can re-provision on the live node.
    pvs = _run(
        [
            "kubectl",
            "get",
            "pv",
            "-o",
            'jsonpath={range .items[*]}{.metadata.name}{"|"}'
            '{.spec.nodeAffinity.required.nodeSelectorTerms[0].matchExpressions[0].values[0]}{"\\n"}{end}',
        ],
        env,
    )
    for line in pvs.stdout.splitlines():
        if "|" not in line:
            continue
        pv_name, node = line.split("|", 1)
        if node and node not in live:
            _run(
                [
                    "kubectl",
                    "delete",
                    "pv",
                    pv_name,
                    "--ignore-not-found",
                    "--wait=false",
                ],
                env,
            )


def _wait_namespace_not_terminating(
    namespace: str, timeout: int = 60, env=None
) -> None:
    """If ``namespace`` is stuck Terminating, wait (bounded) for it to clear.

    Floci's k3s datastore persists across cluster delete/recreate, so a namespace
    a prior teardown deleted can still be Terminating when ``up`` reinstalls into
    it — helm then fails with "namespace ... is being terminated".
    """
    deadline = time.time() + timeout
    while time.time() < deadline:
        result = _run(
            [
                "kubectl",
                "get",
                "namespace",
                namespace,
                "-o",
                "jsonpath={.status.phase}",
            ],
            env,
        )
        if result.returncode != 0 or result.stdout.strip() != "Terminating":
            return
        logger.debug(f"Waiting for namespace {namespace} to finish terminating...")
        time.sleep(4)


def _standard_values(op: Dict[str, Any], env=None) -> Dict[str, Any]:
    """The standard NeuronSphere values every chart expects.

    In the cloud these are injected as ``--set`` flags by ``hmd helm deploy``
    (``_set_standard_values``). The up-time install bypasses that path, so supply
    local equivalents here (dummy AWS values; ALB/ACM/WAF are not emulated).
    Namespace-derived values match the ``<instance>-<did>`` the charts assume.
    """
    from .floci_deployer import local_customer_code

    instance = op["release"]
    namespace = _namespace(op)
    did = env.deployment_id if env is not None else _DID
    account = env.account_id if env is not None else "000000000000"
    return {
        "instance_name": instance,
        "deployment_id": did,
        "namespace_name": namespace,
        "account": account,
        "aws_region": "local",
        "standard_name": namespace,
        "env": {
            "HMD_INSTANCE_NAME": instance,
            "HMD_REPO_NAME": op["repo"],
            "HMD_DID": did,
            "HMD_ENVIRONMENT": "local",
            "HMD_REGION": os.environ.get("HMD_REGION", "reg1"),
            "HMD_REPO_VERSION": "local",
            # Sourced from hmd.env like every other producer/consumer of
            # make_standard_name; a hardcoded value here would name secrets
            # nothing else looks up.
            "HMD_CUSTOMER_CODE": local_customer_code(),
        },
    }


def _resolve_repo_root(op: Dict[str, Any]) -> Optional[Path]:
    """Resolve an operator's repo root -- the chart it installs.

    Returns a directory containing ``src/helm`` and ``meta-data/manifest.json``.
    The bundled pre-build artifact (the unzipped ``build/`` output of the repo)
    wins over a checked-out ``HMD_REPO_HOME`` copy, so the chart installed is
    the one the operator's registered version names; a checkout is used only
    under a local version override. See :func:`bom_seeder.repo_root_candidates`.
    """
    from .bom_seeder import repo_root_candidates

    for candidate in repo_root_candidates(op["repo"]):
        if (Path(candidate) / "src" / "helm" / "Chart.yaml").exists():
            return Path(candidate)
    # `repo_root_candidates` keys bundled artifacts by their manifest `name`;
    # fall back to this module's own name table for an artifact that ships no
    # manifest.json, so an operator chart is never lost to a missing key.
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
    logger.debug(f"Building chart dependencies for {chart_dir}")
    subprocess.run(
        ["helm", "dependency", "build", str(chart_dir)],
        capture_output=True,
        text=True,
    )


def _helm_upgrade(
    op: Dict[str, Any], chart_dir: Path, overlay: Dict[str, Any], env=None
) -> bool:
    """Run a single ``helm upgrade --install`` pass for an operator."""
    values = _load_default_configuration(chart_dir.parent.parent)
    values = _deep_merge(values, _standard_values(op, env))
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
        _wait_namespace_not_terminating(_namespace(op), env=env)
        logger.debug(f"Installing operator '{op['name']}': {' '.join(command)}")
        result = _run(command, env)
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


def _install_operator(op: Dict[str, Any], env=None) -> bool:
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
        passes = op["passes_builder"](env)
    else:
        passes = op.get("passes", [op.get("overlay") or {}])
    for overlay in passes:
        if not _helm_upgrade(op, chart_dir, overlay, env):
            return False
    return True


def provision_k3s_operators(env=None) -> None:
    """Install all cluster operators onto an environment's k3s cluster.

    Best-effort. ``env`` selects which cluster: every command below is issued
    with that environment's kubeconfig, never an ambient ``KUBECONFIG``, so two
    environments provisioned in the same process cannot cross-contaminate. Safe
    to call repeatedly (each install is ``helm upgrade --install``).
    """
    if not _enabled():
        logger.debug("k3s operator provisioning disabled via " + _ENABLE_ENV)
        return
    if env is None and not os.environ.get("KUBECONFIG"):
        logger.debug("KUBECONFIG not set; skipping k3s operator provisioning")
        return

    # The cluster's EKS status is ACTIVE, but the node may still be warming up;
    # wait for it before installing (helm --wait would otherwise fail).
    _wait_for_node_ready(env=env)
    _clean_stale_nodes(env)
    _ensure_node_topology_labels(env)

    # Map the canonical hostnames (neuronsphere, hmd_db, global-graph) to this
    # environment's own containers so cloud charts run unmodified.
    _ensure_coredns_floci_entry(env)

    # Confirm the image-baked ingress controller (Traefik) is up (or tear it
    # down if disabled) so the seeded ingress-controller Resource is real and
    # Ingress objects are served on the node's :80/:443.
    _ensure_ingress_controller(env=env)

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
            logger.debug(
                f"Skipping operator '{op['name']}' — deployed via the ms-deployment "
                "DAG (HMD_LOCAL_NEURONSPHERE_ENABLE_EXT_SECRETS)"
            )
            continue
        if _install_operator(op, env):
            installed.append(op["name"])
    if installed:
        logger.debug(f"Installed k3s operators: {', '.join(installed)}")
