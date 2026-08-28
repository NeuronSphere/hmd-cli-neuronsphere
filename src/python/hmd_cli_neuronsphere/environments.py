"""Control-plane and named-environment orchestration.

The local stack is split in two:

**Control plane** (one per ``$HMD_HOME``) -- ``hmd-ms-deployment``,
``hmd-ms-naming`` and ``hmd-ms-artifact-lib`` as Floci Lambdas, plus the
supporting Floci, ``hmd_proxy``, Postgres and JanusGraph. It is the deployment
graph and the single ingress; it is not tied to any one environment.

**Environments** (N per ``$HMD_HOME``) -- each a self-contained emulated AWS
account: its own Floci, its own EKS/k3s cluster, its own Postgres, its own
JanusGraph, its own ``hmd-ms-dbaccount``, and the Local BOM deployed into it.
This mirrors the cloud, where every account carries its own dbaccount, RDS and
Neptune.

``hmd_proxy`` is the only container that publishes host ports, which is what
lets several environments coexist on one machine (see ``nginx_router``).
"""

import os
import shutil
from pathlib import Path
from typing import Dict, List, Optional

from cement import minimal_logger
from hmd_cli_tools.hmd_cli_tools import load_hmd_env

from . import env_registry, nginx_router
from .env_registry import LocalEnvironment
from .loaders.local_plugin_loader import LocalPluginLoader
from .startup_display import print_header, print_step
from .validators.port_validator import validate_ports

logger = minimal_logger("environments")

# Deployed into the control-plane Floci, never into an environment's.
CONTROL_PLANE_PLUGINS = ["artifact-lib"]

_MS_DEPLOYMENT_URL = "http://localhost/hmd_ms_deployment"


def _services_dir() -> Path:
    return Path(__file__).parent / "services"


def _hmd_home() -> Path:
    return Path(os.environ["HMD_HOME"])


# ---------------------------------------------------------------------------
# Control plane
# ---------------------------------------------------------------------------


def control_plane_compose_files(local_loader: LocalPluginLoader) -> List[str]:
    """Compose files that make up the control plane.

    ``compose_substitute`` plugin containers stay control-plane-scoped in v1:
    their compose files hardcode ``container_name``, which would collide if the
    same file were started once per environment.
    """
    files = [str(_services_dir() / "docker-compose.control-plane.yml")]

    for plugin_name in local_loader.get_enabled_plugins():
        config = local_loader.get_plugin_config(plugin_name)
        if (
            config
            and config.get("local_deploy", {}).get("strategy") == "compose_substitute"
        ):
            compose_path = local_loader.get_compose_path(plugin_name)
            if compose_path and str(compose_path) not in files:
                files.append(str(compose_path))
                print_step(f"  compose_substitute (plugin): {plugin_name}")

    if _graph_enabled():
        graph_compose = _services_dir() / "docker-compose.graph.yml"
        if graph_compose.exists() and str(graph_compose) not in files:
            files.append(str(graph_compose))
            os.makedirs(_hmd_home() / "graph_db", exist_ok=True)
            print_step("  control plane: graph (JanusGraph/Neptune)")

    return files


def _graph_enabled() -> bool:
    return os.environ.get(
        "HMD_LOCAL_NEURONSPHERE_ENABLE_GRAPH", "true"
    ).lower() not in (
        "false",
        "0",
        "no",
    )


def _reload_proxy_when_ready(attempts: int = 10, delay: float = 2.0) -> bool:
    """Reload hmd_proxy, tolerating a container that is still coming up.

    ``docker exec`` against a container that compose created moments ago fails
    until it is actually running, so a single attempt would silently skip the
    reload exactly on the fresh-install path that needs it most.
    """
    import time

    for attempt in range(attempts):
        if nginx_router.reload():
            return True
        if attempt < attempts - 1:
            time.sleep(delay)
    logger.warning(
        "Could not reload hmd_proxy after rewriting its config; "
        "routes may be stale until the next `up`."
    )
    return False


def ensure_control_plane(verbose: bool = False, upgrade: bool = False) -> bool:
    """Bring up (or reconcile) the control plane. Idempotent.

    Returns True when ms-deployment is reachable afterwards.
    """
    from .floci_deployer import (
        DOCKER_NETWORK_NAME,
        clear_apigateway_state,
        control_plane_target,
        ensure_core_databases_direct,
        provision_resources,
        resolve_image_uri,
        setup_service,
        wait_for_floci,
    )
    from .hmd_cli_neuronsphere import (
        _aggregate_hmdms_resources,
        _deploy_hmdms_service_lambdas,
        _deploy_ms_deployment_lambda,
        _ensure_nginx_routed,
        _exec,
        _get_base_command,
        _naming_lambda_env,
        _wait_for_hmd_db,
        _wait_for_ms_deployment,
        update_images,
    )

    print_step("Control plane")
    target = control_plane_target()
    reg = env_registry.load()

    local_loader = LocalPluginLoader()
    # artifact-lib backs `hmd neuronsphere push-artifact` / `pull-artifact`, so
    # it must always be available. It is control-plane, not per-environment.
    local_loader.ensure_foundation_plugin("artifact-lib", "hmd-ms-artifact-lib")

    if upgrade:
        print_step("Upgrade — pulling latest images...")
        try:
            update_images(
                control_plane_compose_files(local_loader) + _env_compose_files()
            )
        except Exception as e:
            logger.warning(f"Image pull failed (non-fatal): {e}")
            print(f"  Warning: image pull failed: {e}")

    compose_files = control_plane_compose_files(local_loader)

    cache_dir = _hmd_home() / ".cache"
    os.makedirs(cache_dir / "nginx", exist_ok=True)
    os.makedirs(_hmd_home() / "floci" / "data", exist_ok=True)

    # Drop Floci's persisted API Gateway state before it starts: Floci writes v1
    # gateway records with null fields and rehydrates them, so a restart would
    # otherwise serve back unusable, undeletable gateways. Every gateway is
    # recreated below anyway, so nothing is lost.
    clear_apigateway_state(_hmd_home() / "floci" / "data")

    # A 503 placeholder so hmd_proxy can start before any route exists -- but
    # one that already streams :4566, because that is the address `up` polls to
    # decide whether Floci came up, and Floci publishes no host port of its own.
    bootstrap_path = nginx_router.base_config_path()
    bootstrap_rewritten = not nginx_router.config_serves_floci(bootstrap_path)
    nginx_router.write_bootstrap_config(floci_host=target.alias)

    print_step("Validating ports...")
    validate_ports(compose_files, strict=True)

    _exec(
        ["docker", "network", "create", DOCKER_NETWORK_NAME],
        capture=True,
        quiet=not verbose,
    )

    print_step("Starting control-plane containers...")
    quiet = not verbose
    command = [
        *_get_base_command(compose_files, quiet=quiet),
        "up",
        "-d",
        "--remove-orphans",
        "--quiet-pull",
    ]
    if quiet:
        _, stderr, retcode = _exec(command, capture=True, quiet=True)
        if retcode != 0 and stderr:
            print(f"\n  Error starting control-plane containers:\n{stderr.decode()}")
    else:
        _exec(command)

    # A proxy container that was already running kept its previously loaded
    # config through `compose up` (an unchanged service spec is not recreated),
    # so rewriting the file above is not enough -- it has to be reloaded before
    # anything polls :4566. Only needed when the config actually changed; a
    # freshly created container has already read the new one.
    if bootstrap_rewritten:
        _reload_proxy_when_ready()

    print_step("Waiting for Floci...")
    try:
        wait_for_floci(target=target)
    except RuntimeError as e:
        logger.warning(f"{e} — skipping control-plane Lambda deployment")
        print(f"  Warning: {e}")
        print(
            "  The control-plane Floci is reached through hmd_proxy's :4566 "
            "stream, not directly. Check `docker logs hmd_proxy` and that "
            f"`docker inspect -f '{{{{.State.Running}}}}' {target.container}` is true."
        )
        return False

    print_step("Waiting for PostgreSQL (hmd_db)...")
    _wait_for_hmd_db()

    # The control plane has no dbaccount of its own -- dbaccount is
    # per-environment, matching the cloud -- so its databases are created
    # directly and deterministically.
    print_step("Ensuring control-plane databases...")
    ensure_core_databases_direct()

    resources: Dict[str, List] = {"s3_buckets": []}
    _aggregate_hmdms_resources(local_loader, resources)
    print_step("Provisioning control-plane Floci resources...")
    provision_resources(resources, local_loader=local_loader, target=target)

    service_api_ids: Dict[str, str] = {}

    print_step("Deploying ms-deployment Lambda...")
    ms_deployment_available = False
    try:
        service_api_ids["hmd_ms_deployment"] = _deploy_ms_deployment_lambda(None)
        ms_deployment_available = True
    except Exception as e:
        logger.warning(f"ms-deployment Lambda deploy failed: {e}")
        print(f"  Warning: ms-deployment not available: {e}")

    print_step("Deploying ms-naming Lambda...")
    naming_env, naming_image = _naming_lambda_env()
    if naming_image is None:
        raise RuntimeError(
            "hmd-ms-naming image not cached locally. "
            "Run `hmd build` in hmd-ms-naming and retry."
        )
    service_api_ids["hmd_ms_naming"] = setup_service(
        "hmd_ms_naming", naming_image, naming_env, target=target
    )

    # artifact-lib (and any other control-plane HMDMS plugin).
    cp_deployed = _deploy_hmdms_service_lambdas(
        None, local_loader, plugin_filter=CONTROL_PLANE_PLUGINS, target=target
    )
    for spec in cp_deployed:
        service_api_ids[spec["function_name"]] = spec["api_id"]

    from .floci_deployer import deploy_api_gateway

    print_step("Deploying API Gateway stages...")
    for api_id in set(service_api_ids.values()):
        deploy_api_gateway(api_id, target=target)

    print_step("Configuring control-plane routes...")
    nginx_router.render_base_config()
    nginx_router.write_control_plane_routes(service_api_ids)
    nginx_router.write_control_plane_streams(target.alias)
    if ms_deployment_available:
        _ensure_nginx_routed(
            f"{_MS_DEPLOYMENT_URL}/api/hmd_lang_deployment.environment"
        )
    else:
        nginx_router.reload()

    if ms_deployment_available:
        print_step("Waiting for ms-deployment...")
        _wait_for_ms_deployment(_MS_DEPLOYMENT_URL)
        reg.control_plane.bootstrapped = True
        env_registry.save(reg)

    return ms_deployment_available


# ---------------------------------------------------------------------------
# Environments
# ---------------------------------------------------------------------------


def _export_env_vars(env: LocalEnvironment) -> None:
    """Publish this environment's NS_ENV_* values for docker compose."""
    for key, value in env.compose_env().items():
        os.environ[key] = value


def _env_compose_files() -> List[str]:
    return [str(_services_dir() / "docker-compose.environment.yml")]


def _wait_for_container_health(container: str, timeout: int = 180) -> bool:
    """Wait for a container to report healthy (or at least be running).

    Used before the environment's Floci has an nginx stream route, so its health
    cannot yet be polled over HTTP.
    """
    import json as _json
    import subprocess
    import time

    start = time.time()
    while time.time() - start < timeout:
        result = subprocess.run(
            ["docker", "inspect", container],
            capture_output=True,
            text=True,
        )
        if result.returncode == 0:
            try:
                state = _json.loads(result.stdout)[0]["State"]
            except (ValueError, KeyError, IndexError):
                state = {}
            health = (state.get("Health") or {}).get("Status")
            if health == "healthy":
                return True
            if health is None and state.get("Running"):
                return True
            if health == "unhealthy":
                logger.debug(f"{container} reported unhealthy; still waiting")
        time.sleep(3)
    logger.warning(f"{container} did not become healthy within {timeout}s")
    return False


def start_environment(
    env: LocalEnvironment,
    verbose: bool = False,
    upgrade: bool = False,
    prune: bool = False,
) -> bool:
    """Start (or reconcile) one named environment.

    :returns: whether the environment reached its declared state.

    Assumes the control plane is already up.

    :param upgrade: Deploy entries the environment declares but has not
        deployed yet, and redeploy ones whose declared configuration changed.
    :param prune: Destroy instances that are deployed but no longer declared.
        Off by default: a reconcile reports removals but never performs one
        unless asked, so a mistyped or half-edited manifest cannot tear down a
        running instance.
    """
    from .floci_deployer import (
        clear_apigateway_state,
        deploy_api_gateway,
        ensure_k3s_cluster,
        env_target,
        prune_control_plane_strays,
        provision_plugin_databases,
        provision_resources,
        wait_for_k3s_ready,
        write_kubeconfig,
    )
    from .hmd_cli_neuronsphere import (
        _aggregate_hmdms_resources,
        _deploy_hmdms_service_lambdas,
        _exec,
        _get_base_command,
        _wait_for_hmd_db,
    )

    print_step(f"Environment '{env.slug}'")
    target = env_target(env)
    local_loader = LocalPluginLoader()
    # dbaccount is foundational for cloud-parity DB provisioning and is
    # per-environment, exactly as in the cloud.
    local_loader.ensure_foundation_plugin("dbaccount", "hmd-ms-dbaccount")

    if not env.legacy_layout:
        for directory in env.state_dirs():
            os.makedirs(directory, exist_ok=True)

        _export_env_vars(env)
        compose_files = _env_compose_files()

        print_step("Validating ports...")
        validate_ports(compose_files, strict=True)

        print_step(f"Starting containers for '{env.slug}'...")
        quiet = not verbose
        command = [
            *_get_base_command(
                compose_files, quiet=quiet, project_name=env.compose_project
            ),
            "up",
            "-d",
            "--remove-orphans",
            "--quiet-pull",
        ]
        if quiet:
            _, stderr, retcode = _exec(command, capture=True, quiet=True)
            if retcode != 0 and stderr:
                print(f"\n  Error starting '{env.slug}' containers:\n{stderr.decode()}")
        else:
            _exec(command)

        # The environment's Floci has no host port of its own; its stream route
        # must exist before it can be polled over HTTP, and the container must
        # exist before the route can point anywhere. So: wait on the container,
        # then publish the route, then verify over HTTP.
        print_step("Waiting for environment containers...")
        _wait_for_container_health(env.floci_container)
        _wait_for_hmd_db(container=env.db_container)

        nginx_router.write_env_streams(env)
        nginx_router.reload()

        from .floci_deployer import wait_for_floci

        print_step(f"Waiting for Floci (account {env.account_id})...")
        try:
            wait_for_floci(target=target)
        except RuntimeError as e:
            logger.warning(f"{e} — environment '{env.slug}' is degraded")
            print(f"  Warning: {e}")
            return False

        clear_apigateway_state(env.floci_data_dir)

    # Control-plane objects that landed in this account while `floci` was an
    # ambiguous Docker DNS name (a Compose service key on both compose files,
    # so it round-robined between the control-plane and env Flocis). Bounded
    # allowlist, and never runs against the control plane or a legacy env.
    strays = prune_control_plane_strays(target)
    if strays:
        print_step(f"Removed {strays} stray control-plane object(s) from '{env.slug}'")

    # Floci resources for this account, including the admin DB secret its own
    # dbaccount reads back to reach its own Postgres.
    resources: Dict[str, List] = {"s3_buckets": []}
    _aggregate_hmdms_resources(local_loader, resources)
    print_step("Provisioning environment Floci resources...")
    provision_resources(
        resources,
        local_loader=local_loader,
        target=target,
        did=env.deployment_id,
        db_host=env.db_container,
        core_instance_name=env.core_instance_name,
        environment_name=env.slug,
    )

    # k3s cluster, spawned by *this* environment's Floci.
    k3s_uid = None
    cluster_name = None
    if os.environ.get("HMD_LOCAL_NEURONSPHERE_ENABLE_K3S", "true").lower() not in (
        "false",
        "0",
        "no",
    ):
        print_step(f"Creating k3s cluster '{env.k3s_cluster}'...")
        try:
            ensure_k3s_cluster(env.k3s_cluster, target=target)
            wait_for_k3s_ready(env.k3s_cluster, target=target)
            cluster_name = env.k3s_cluster
            kubeconfig = write_kubeconfig(
                env.k3s_cluster, env.kubeconfig_path, target=target
            )
            # The default environment also writes the historical shared path so
            # host-side kubectl and the robot suites keep working unchanged.
            if env.is_default:
                legacy = _hmd_home() / ".cache" / "k3s" / "kubeconfig"
                legacy.parent.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(kubeconfig, legacy)
                os.environ.setdefault("KUBECONFIG", str(legacy))
            print_step(f"  k3s ready (kubeconfig: {kubeconfig})")

            print_step("Installing cluster operators onto k3s...")
            from .k3s_operators import cluster_incarnation_id, provision_k3s_operators

            provision_k3s_operators(env)
            k3s_uid = cluster_incarnation_id(env)
        except Exception as e:
            logger.warning(f"k3s cluster creation failed: {e}")
            print(
                f"  Warning: k3s unavailable — Argo and k3s-dependent plugins "
                f"will be skipped: {e}"
            )

    # dbaccount first, so it can provision the other services' databases.
    service_api_ids: Dict[str, str] = {}
    dbaccount_deployed = _deploy_hmdms_service_lambdas(
        None, local_loader, plugin_filter=["dbaccount"], target=target, env=env
    )
    if dbaccount_deployed:
        for spec in dbaccount_deployed:
            service_api_ids[spec["function_name"]] = spec["api_id"]
        deploy_api_gateway(dbaccount_deployed[0]["api_id"], target=target)
        nginx_router.write_env_routes(env, service_api_ids)
        nginx_router.reload()

        print_step("Provisioning databases via ms-dbaccount...")
        provision_plugin_databases(local_loader, env)
    else:
        logger.warning(
            "dbaccount Lambda not deployed (image missing or plugin not enabled); "
            "skipping DB provisioning. Lambdas may fail to connect to their DBs."
        )

    # The remaining HMDMS services for this environment.
    already = {s["function_name"] for s in dbaccount_deployed}
    hmdms_deployed = _deploy_hmdms_service_lambdas(
        None,
        local_loader,
        skip_function_names=already,
        exclude_plugins=CONTROL_PLANE_PLUGINS,
        target=target,
        env=env,
    )
    hmdms_deployed = list(dbaccount_deployed) + list(hmdms_deployed)
    for spec in hmdms_deployed:
        service_api_ids[spec["function_name"]] = spec["api_id"]

    print_step("Deploying API Gateway stages...")
    for api_id in set(service_api_ids.values()):
        deploy_api_gateway(api_id, target=target)

    print_step(f"Configuring routes for '{env.slug}'...")
    nginx_router.write_env_routes(env, service_api_ids)
    nginx_router.reload()

    ok = _bootstrap_environment(
        env, hmdms_deployed, cluster_name, k3s_uid, upgrade, prune=prune
    )

    # Services the DAG deployed carry CDKTF-managed API Gateways that
    # `write_env_routes` above never saw -- and whose stage names are not
    # "local", so only a discovered route works. Unconditional: the fragment was
    # rewritten above even on a reconcile that deployed nothing.
    try:
        routed = nginx_router.refresh_deployed_service_routes(env)
        if routed:
            print_step(f"  Routed {routed} DAG-deployed service(s) under /{env.slug}/")
    except Exception as e:
        logger.warning(f"DAG service route refresh skipped (non-fatal): {e}")

    # Expose this environment's Trino coordinator on its own host stream port.
    try:
        if nginx_router.configure_trino_host_route(env):
            nginx_router.reload()
            print_step(f"  Trino exposed on host :{env.trino_port}")
    except Exception as e:
        logger.warning(f"Trino host route setup skipped (non-fatal): {e}")

    # Host-route the environment's Ingress-exposed UIs (Airflow, Argo, ...)
    # through hmd_proxy, the same way the cloud reaches them through an ALB.
    try:
        if nginx_router.configure_ingress_host_route(env):
            nginx_router.reload()
            print_step(
                f"  UIs served at http://<app>.{env.slug}.{nginx_router.INGRESS_DOMAIN}/"
            )
            _warn_unresolvable_ingress_hosts(env)
    except Exception as e:
        logger.warning(f"Ingress host route setup skipped (non-fatal): {e}")

    return ok


def _warn_unresolvable_ingress_hosts(env) -> None:
    """Tell the user which UI hostnames still need an ``/etc/hosts`` entry.

    A warning, never a failure: unlike the ``neuronsphere`` alias, an
    unresolvable UI hostname affects nothing but that UI's browser access.
    """
    missing = nginx_router.unresolvable_ingress_hosts(env)
    if not missing:
        return
    print(
        "\n"
        "  UI hostnames are not resolvable on this host. Run once (requires sudo):\n"
        "\n"
        f"      sudo sh -c 'echo \"127.0.0.1 {' '.join(missing)}\" >> /etc/hosts'\n"
    )


def _service_specs(env: LocalEnvironment, hmdms_deployed: List[Dict]) -> List[Dict]:
    """``application/microservice`` Resource specs for this environment's services.

    Control-plane services are addressed unprefixed; an environment's own are
    under its ``/<slug>/`` prefix.
    """
    specs = [
        {
            "service_name": s.get("function_name"),
            "repo_class_name": s.get("repo_class_name"),
            "api_base_url": f"http://localhost/{env.slug}/{s.get('function_name')}",
        }
        for s in (hmdms_deployed or [])
        if s.get("function_name")
    ]
    specs += [
        {
            "service_name": "hmd_ms_deployment",
            "repo_class_name": "hmd-ms-deployment",
            "api_base_url": f"{_MS_DEPLOYMENT_URL}",
        },
        {
            "service_name": "hmd_ms_naming",
            "repo_class_name": "hmd-ms-naming",
            "api_base_url": "http://localhost/hmd_ms_naming",
        },
    ]
    return specs


def _bootstrap_environment(
    env: LocalEnvironment,
    hmdms_deployed: List[Dict],
    cluster_name: Optional[str],
    k3s_uid: Optional[str],
    upgrade: bool,
    prune: bool = False,
) -> bool:
    """Seed and deploy this environment's BOM, or reconcile an existing one.

    :returns: whether the environment reached its declared state. A False here
        is what keeps `up` from printing "Ready" over a broken bootstrap.
    """
    from .bom_seeder import (
        ensure_environment,
        resync_local_resources,
        seed_base_resource_definitions,
    )
    from .change_set_builder import declared_repo_paths
    from .env_manifest import load_manifest
    from .local_workflow_runner import LocalWorkflowRunner

    base_url = _MS_DEPLOYMENT_URL
    specs = _service_specs(env, hmdms_deployed)

    try:
        ensure_environment(base_url, env)
    except Exception as e:
        logger.warning(f"Could not ensure environment entity: {e}")
        print(f"  Error: could not register environment '{env.slug}': {e}")
        return False

    # The manifest is this environment's declared desired state. None means the
    # environment has none, in which case the desired state is resolved the way
    # it always was: built-ins plus every installed plugin package.
    try:
        manifest = load_manifest(env.slug)
    except Exception as e:
        logger.warning(f"Could not load the manifest for '{env.slug}': {e}")
        print(f"\n  Error: {e}\n")
        return False
    if manifest is not None:
        print_step(f"Using environment manifest: {manifest.path}")

    repo_paths = declared_repo_paths(manifest)
    bootstrapped = bool(env.bootstrap.get("csd_nid"))
    runner = LocalWorkflowRunner(
        base_url, cluster_name=cluster_name, env=env, repo_paths=repo_paths
    )

    if bootstrapped:
        # A recreated k3s cluster has nothing deployed on it even though the
        # persisted graph still says otherwise. Only trust a mismatch when both
        # UIDs are known, so a bootstrap recorded before this feature existed
        # doesn't force a surprise redeploy.
        recorded_uid = env.bootstrap.get("k3s_uid")
        cluster_recreated = bool(k3s_uid and recorded_uid and recorded_uid != k3s_uid)

        print_step(
            f"'{env.slug}' already bootstrapped — reconciling "
            "(skipping BOM seeding and DAG execution)."
        )
        print_step("Resyncing core resources...")
        try:
            count = resync_local_resources(
                base_url, cluster_name, services=specs, env=env
            )
            print_step(f"  {count} core resource(s) resynced")
        except Exception as e:
            logger.warning(f"Local resource resync failed (non-fatal): {e}")

        if cluster_recreated:
            print_step(
                "k3s cluster was recreated since the last bootstrap — the "
                "persisted deployment graph no longer matches reality; "
                "redeploying the full BOM onto the new cluster."
            )
            return _run_full_bootstrap(
                env, runner, specs, cluster_name, k3s_uid, base_url, manifest=manifest
            )

        ok = _reconcile_environment(
            env, runner, base_url, manifest, upgrade=upgrade, prune=prune
        )

        if k3s_uid and not env.bootstrap.get("k3s_uid"):
            env_registry.record_bootstrap(
                env, env.bootstrap.get("csd_nid", "control-plane"), k3s_uid=k3s_uid
            )
        return ok

    print_step("Seeding base resource definitions...")
    try:
        seeded = seed_base_resource_definitions(base_url)
        print_step(f"  {len(seeded)} base resource definitions")
    except Exception as e:
        logger.warning(f"Base resource definition seeding failed: {e}")

    return _run_full_bootstrap(
        env, runner, specs, cluster_name, k3s_uid, base_url, manifest=manifest
    )


def _reconcile_environment(
    env: LocalEnvironment,
    runner,
    base_url: str,
    manifest,
    upgrade: bool,
    prune: bool,
) -> bool:
    """Converge an already-bootstrapped environment on its declared state.

    The plan is always printed, whatever the flags: seeing what ``up`` *would*
    do is the point, and it costs one graph query. Acting on it is gated --
    ``--upgrade`` for additions and redeploys, ``--prune`` for destroys -- so
    neither an editing mistake nor a plugin uninstalled by accident silently
    changes a running environment.

    Destroys run *before* deploys: a removed instance may still hold a Helm
    release name, a k8s namespace or a Resource that an added entry is about to
    claim.
    """
    from .bom_seeder import (
        DestroyCascadeError,
        destroy_instances,
        seed_bom,
    )
    from .change_set_builder import declared_repo_paths
    from . import env_reconcile

    try:
        plan = env_reconcile.compute_plan(base_url, env=env, manifest=manifest)
    except Exception as e:
        logger.warning(f"Could not compute the reconcile plan: {e}")
        print(f"  Error: could not compute the reconcile plan: {e}")
        return False

    if plan.degraded:
        print_step("Could not read the deployment graph; skipping reconcile this run.")
        return True

    if plan.is_empty:
        print_step(f"Environment matches its declared state ({plan.summary()}).")
        return True

    print_step(f"Reconcile plan: {plan.summary()}")
    for line in plan.render():
        print(line)

    repo_paths = declared_repo_paths(manifest)
    ok = True

    # --- removals -------------------------------------------------------
    if plan.remove and not prune:
        print_step(
            f"{len(plan.remove)} instance(s) are deployed but no longer declared; "
            f"run `hmd neuronsphere up --env {env.slug} --prune` to destroy them."
        )
    elif plan.remove:
        keep = {e["repo_instance_name"] for e in plan.desired}
        keep |= env_reconcile.protected_instance_names()
        try:
            csd_nid, nodes = destroy_instances(
                base_url, env=env, instance_names=plan.remove, keep=keep
            )
        except DestroyCascadeError as e:
            # The removals are unsafe as declared, but the additions are not --
            # skip only the prune and let `--upgrade` do its half of the work.
            print(f"\n  Refusing to prune: {e}\n")
            csd_nid, nodes = None, []
        except Exception as e:
            logger.warning(f"Destroy preparation failed: {e}")
            print(f"  Error: could not prepare the destroy: {e}")
            csd_nid, nodes = None, []
            ok = False
        if csd_nid:
            print_step(f"Destroying {len(nodes)} instance(s)...")
            if not runner.run(csd_nid, nodes, destroy=True):
                # A half-torn-down environment must not get new deploys layered
                # on top of it: the surviving instances' state is unknown.
                print("\n  Warning: some destroys failed; skipping deployments")
                return False

    # --- additions and redeploys ----------------------------------------
    pending = plan.add + plan.change
    if not pending:
        env_reconcile.write_snapshot(env, plan.desired)
        return ok
    if not upgrade:
        print_step(
            f"{len(pending)} entrie(s) to deploy; run "
            f"`hmd neuronsphere up --env {env.slug} --upgrade` to apply them."
        )
        return ok

    names = ", ".join(e["repo_instance_name"] for e in pending)
    print_step(f"Deploying {len(pending)} entrie(s): {names}")
    try:
        csd_nid, nodes = seed_bom(base_url, bom=pending, env=env, repo_paths=repo_paths)
    except Exception as e:
        logger.warning(f"Delta changeset seeding failed: {e}")
        print(f"  Error: could not seed the delta changeset: {e}")
        return False

    print_step(f"  {len(nodes)} deployment node(s)")
    ok_run = runner.run(csd_nid, nodes)
    if not ok_run:
        print("\n  Warning: some deployments failed")
    # Record only what actually landed, so a failed entry stays eligible for
    # retry on the next `up --upgrade` while a succeeded one is not redeployed.
    settled = [
        e for e in pending if e["repo_instance_name"] in set(runner.last_succeeded)
    ]
    env_reconcile.merge_snapshot(env, plan.desired, settled)
    return ok and bool(ok_run)


def _run_full_bootstrap(
    env: LocalEnvironment,
    runner,
    specs: List[Dict],
    cluster_name: Optional[str],
    k3s_uid: Optional[str],
    base_url: str,
    manifest=None,
) -> bool:
    """The two-phase changeset bootstrap for one environment.

    Phase A applies a changeset containing only the core instance and submits
    its concrete Resources; Phase B applies everything else. The order is
    load-bearing: a Phase-B entry that depends on one of those Resources needs
    the concrete Resource to already exist for selector matching to find it.
    """
    from .bom_seeder import (
        LOCAL_CORE_BOM,
        build_local_core_resources,
        resolve_plugin_bom,
        seed_bom,
        submit_local_resources,
    )
    from .change_set_builder import declared_repo_paths, full_definition
    from . import env_reconcile

    repo_paths = declared_repo_paths(manifest)
    csd_nid = None
    ok = True
    try:
        print_step("Seeding core changeset (Phase A)...")
        csd_nid_a, nodes_a = seed_bom(base_url, bom=list(LOCAL_CORE_BOM), env=env)
        print_step(f"  {len(nodes_a)} deployment node(s)")

        print_step("Submitting core resources...")
        try:
            resources = build_local_core_resources(
                cluster_name=cluster_name, services=specs, env=env
            )
            count = submit_local_resources(base_url, resources, nodes_a)
            print_step(f"  {count} core resource(s) submitted")
        except Exception as e:
            logger.warning(f"Local resource submission failed (non-fatal): {e}")

        # Phase A must still run through the runner -- even though its one node
        # is a hardcoded no-op -- so the RID transitions DEPLOY_NEXT -> DEPLOYED.
        # Skipping it would leave the core instance permanently DEPLOY_NEXT and
        # break the reconcile fast-path.
        runner.run(csd_nid_a, nodes_a)
        core_succeeded = list(runner.last_succeeded)

        print_step("Seeding deployment graph (Phase B)...")
        phase_b = resolve_plugin_bom(env=env, manifest=manifest)
        csd_nid, nodes = seed_bom(base_url, bom=phase_b, env=env, repo_paths=repo_paths)
        print_step(f"  {len(nodes)} deployment node(s)")

        print_step("Running local deployments...")
        if not runner.run(csd_nid, nodes):
            print("\n  Warning: some deployments failed")
            ok = False

        # Seed the drift snapshot from what actually deployed across both
        # phases, so the next `up` reports real changes rather than treating
        # every entry as untracked. The definition is re-resolved through
        # full_definition rather than assembled from the two phase lists: the
        # snapshot stores entry digests, and they only compare equal to a later
        # reconcile if both sides normalize the entries the same way.
        definition = full_definition(env=env, manifest=manifest)
        settled = set(core_succeeded) | set(runner.last_succeeded)
        env_reconcile.merge_snapshot(
            env,
            definition,
            [e for e in definition if e["repo_instance_name"] in settled],
        )
    except Exception as e:
        logger.warning(f"BOM seeding failed: {e}")
        print(f"\n  Error: BOM seeding failed: {e}")
        ok = False

    env_registry.record_bootstrap(env, csd_nid or "control-plane", k3s_uid=k3s_uid)
    return ok


# ---------------------------------------------------------------------------
# Lifecycle
# ---------------------------------------------------------------------------


def create_environment(
    name: str,
    deploy: bool = True,
    verbose: bool = False,
    bom_file: Optional[str] = None,
    manifest_file: Optional[str] = None,
) -> LocalEnvironment:
    """Register and start a new named environment.

    :param manifest_file: An environment manifest declaring which plugins to
        enable and which repo instances to deploy. Installed into
        ``$HMD_HOME/environments/<slug>.<ext>`` so later ``up`` runs reconcile
        against it. Validated before installation, so a bad manifest fails here
        rather than half-way through a bootstrap.
    :param bom_file: Deprecated predecessor of ``manifest_file``: a flat BOM
        JSON array, merged into the built-in BOM as before.
    """
    from .env_manifest import install_manifest

    load_hmd_env()
    if bom_file:
        print_step(
            "--bom-file is deprecated; prefer --manifest with a declarative "
            "environment manifest (see docs/environments.rst)."
        )
        os.environ["HMD_LOCAL_BOM_FILE"] = bom_file

    env = env_registry.create_env(name)
    print_header(f"Creating environment '{env.slug}'")
    print_step(f"  account {env.account_id}, cluster {env.k3s_cluster}")
    print_step(f"  routes  http://localhost/{env.slug}/<service>/")
    print_step(f"  Floci   http://localhost:{env.floci_port}")

    if manifest_file:
        installed = install_manifest(env.slug, manifest_file)
        print_step(f"  manifest {installed}")

    if not ensure_control_plane(verbose=verbose):
        print(
            "\n  Warning: control plane is not fully available; environment "
            "creation may be incomplete."
        )

    if deploy:
        start_environment(env, verbose=verbose)
    else:
        print_step("--no-deploy: registered only, nothing started.")
    print_header("Ready")
    return env


def stop_environment(
    env: LocalEnvironment, verbose: bool = False, purge: bool = False
) -> None:
    """Stop one environment's containers and remove its routes."""
    from .floci_deployer import (
        delete_k3s_cluster,
        env_target,
        purge_k3s_container_and_volume,
    )
    from .hmd_cli_neuronsphere import _exec, _get_base_command

    print_step(f"Stopping environment '{env.slug}'...")
    try:
        delete_k3s_cluster(env.k3s_cluster, target=env_target(env))
    except Exception as e:
        logger.debug(f"k3s cluster delete skipped: {e}")

    if not env.legacy_layout:
        _export_env_vars(env)
        command = [
            *_get_base_command(
                _env_compose_files(),
                quiet=not verbose,
                project_name=env.compose_project,
            ),
            "down",
        ]
        _exec(command, capture=not verbose, quiet=not verbose)

    nginx_router.remove_env_routes(env)
    nginx_router.reload()

    if purge:
        purge_k3s_container_and_volume(env.k3s_cluster)
        if not env.legacy_layout:
            shutil.rmtree(env.state_path, ignore_errors=True)
        env_registry.clear_bootstrap(env)
        print_step(f"  purged persistent state for '{env.slug}'")


def delete_environment(name: str, purge: bool = True, force: bool = False) -> None:
    """Stop an environment and remove it from the registry."""
    load_hmd_env()
    reg = env_registry.load()
    env = reg.get(name)
    if env is None:
        raise env_registry.EnvRegistryError(f"Unknown local environment '{name}'.")
    if env.slug == reg.default_env and len(reg.environments) > 1 and not force:
        raise env_registry.EnvRegistryError(
            f"'{env.slug}' is the default environment. Choose another default with "
            f"`hmd neuronsphere env use <name>` first, or pass --force."
        )

    stop_environment(env, purge=purge)
    env_registry.remove_env(env.slug)
    print_step(f"Removed environment '{env.slug}' (port slot {env.port_slot} freed)")


def environment_definition(env: LocalEnvironment) -> List[Dict]:
    """This environment's fully resolved change_set definition.

    What ``up`` would apply: the manifest's declared repos plus everything the
    enabled plugins and built-ins contribute, de-duped, topologically sorted and
    stamped with the environment's ``deployment_id``.
    """
    from .change_set_builder import full_definition
    from .env_manifest import load_manifest

    load_hmd_env()
    return full_definition(env=env, manifest=load_manifest(env.slug))


def environment_plan(env: LocalEnvironment):
    """The reconcile plan for ``env`` -- what ``up`` would add, change, destroy."""
    from . import env_reconcile
    from .env_manifest import load_manifest

    load_hmd_env()
    return env_reconcile.compute_plan(
        _MS_DEPLOYMENT_URL, env=env, manifest=load_manifest(env.slug)
    )


def environment_status(env: LocalEnvironment) -> Dict:
    """A serializable snapshot of one environment."""
    import subprocess

    def _running(container: str) -> bool:
        result = subprocess.run(
            ["docker", "inspect", "-f", "{{.State.Running}}", container],
            capture_output=True,
            text=True,
        )
        return result.returncode == 0 and result.stdout.strip() == "true"

    containers = {
        "floci": env.floci_container,
        "db": env.db_container,
        "graph": env.graph_container,
        "k3s": f"floci-eks-{env.k3s_cluster}",
    }
    return {
        "name": env.slug,
        "account_id": env.account_id,
        "deployment_id": env.deployment_id,
        "legacy_layout": env.legacy_layout,
        "k3s_cluster": env.k3s_cluster,
        "compose_project": env.compose_project,
        "state_dir": env.state_dir,
        "bootstrapped": bool(env.bootstrap.get("csd_nid")),
        "routes": {
            "services": f"http://localhost/{env.slug}/<service>/",
            "floci": f"http://localhost:{env.floci_port}",
            "trino": f"localhost:{env.trino_port}",
        },
        "containers": {
            role: {"name": name, "running": _running(name)}
            for role, name in containers.items()
        },
    }
