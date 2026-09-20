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
import subprocess
from pathlib import Path
from typing import Dict, List, Optional

from cement import minimal_logger
from hmd_cli_tools.hmd_cli_tools import load_hmd_env

from . import env_registry, nginx_router
from .env_registry import LocalEnvironment
from .loaders.local_plugin_loader import LocalPluginLoader
from .startup_display import print_header, print_section, print_step, spinner_step
from .validators.port_validator import validate_ports

logger = minimal_logger("environments")

# Deployed into the control-plane Floci, never into an environment's.
CONTROL_PLANE_PLUGINS = ["artifact-lib"]

# The one name the whole platform uses for the control-plane Postgres: compose
# peers (the Deployment GUI, Hive metastore, Trino, Airflow, Superset), `_psql`,
# and cloud Helm charts running unmodified in k3s. Floci names the RDS backend
# container it spawns opaquely, so the container is aliased to this on the
# NeuronSphere network rather than every consumer being rewritten.
_CANONICAL_DB_HOST = "hmd_db"

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

    # No graph here. The control plane's own services -- ms-deployment,
    # ms-naming, artifact-lib, dbaccount -- are all Postgres-only (their
    # SERVICE_CONFIGs declare a single `postgres` engine), so the JanusGraph
    # that used to run unconditionally alongside them had no reader at all.
    # An environment's graph is now a lazily-provisioned Floci Neptune cluster;
    # see bom_seeder.graph_bom_entry.
    _notice_orphaned_control_plane_graph()

    return files


_LEGACY_GRAPH_CONTAINER = "global-graph"


def _notice_orphaned_control_plane_graph() -> None:
    """Point out a control-plane graph left over from before it was removed.

    Deliberately not deleted here: it is the user's container, and while nothing
    in the control plane reads it, someone may have been using it directly. Its
    data lives in a bind mount under HMD_HOME and survives removal either way.
    """
    try:
        r = subprocess.run(
            [
                "docker",
                "ps",
                "-a",
                "--format",
                "{{.Names}}",
                "--filter",
                f"name=^{_LEGACY_GRAPH_CONTAINER}$",
            ],
            capture_output=True,
            text=True,
            timeout=10,
        )
    except (subprocess.SubprocessError, OSError):
        return
    if r.returncode == 0 and r.stdout.strip():
        print_step(
            f"  note: '{_LEGACY_GRAPH_CONTAINER}' is left over from a previous "
            f"install and is no longer started or used. Remove it with "
            f"`docker rm -f {_LEGACY_GRAPH_CONTAINER}` when convenient."
        )


# The Deployment GUI's own published registry -- its manifest's
# `deploy.default_configuration.image.repository`. Not one of
# `image_cache.image_candidates`' prefixes, which cover the NeuronSphere registry
# and whatever a developer added, so it is appended explicitly.
GUI_PUBLISHED_REGISTRY = "ghcr.io/hmdlabs"
GUI_REPO_CLASS = "hmd-app-neuronsphere"

# Which version of the GUI this CLI ships against. A plain pin, because the app is
# no longer a `pre_build_artifacts` entry: nothing of it is bundled here now that
# it runs from its published image instead of its Helm chart. Bump it the way the
# artifact pins in `meta-data/manifest.json` are bumped.
GUI_IMAGE_VERSION = "0.1.74"

# Compose profile guarding the `deployment-gui` service in
# services/docker-compose.control-plane.yml.
GUI_COMPOSE_PROFILE = "deployment-gui"
# The DAG-runner service, which only nsctl starts (controlplane.RunnerProfile).
# The Python CLI never activates it on `up`, but it MUST activate it on a stop:
# a profile-gated service compose cannot see is a container `down` leaves
# running, and this one outlived `hmd neuronsphere down --purge` until it was
# listed here.
RUNNER_COMPOSE_PROFILE = "nsrunner"
# Every profile the control-plane compose file defines. A teardown activates
# all of them regardless of what this CLI would have started, because what has
# to be stopped is whatever is *running*, not whatever is currently configured.
ALL_COMPOSE_PROFILES = (GUI_COMPOSE_PROFILE, RUNNER_COMPOSE_PROFILE)

# The `deployment-gui` service's container_name, which the MCP key bootstrap
# execs into. `nginx_router.GUI_UPSTREAM` spells the same name with its port.
GUI_CONTAINER = "hmd_deployment_gui"

# `--name` for the MCP API key `up` mints. The management command's
# `--if-not-exists` keys off this name, so it is also what makes a second `up` a
# no-op instead of a rotation.
MCP_KEY_NAME = "local dev"

# Prefix every platform MCP key carries (`deployments.models.MCPApiKey.PREFIX`).
# The key is printed once and never stored in the clear, so this is how it gets
# picked out of the management command's stdout.
MCP_KEY_PREFIX = "nsmcp_"

# Which version of ms-deployment the control plane runs when nothing else says.
# A checked-out `$HMD_REPO_HOME/hmd-ms-deployment` still wins, and
# `HMD_MS_DEPLOYMENT_VERSION` wins over both -- this only replaces the `stable`
# floating tag a machine without the repo used to fall back to, so `up` there
# gets a version this CLI was actually tested against.
MS_DEPLOYMENT_VERSION = "0.1.848"


def deployment_gui_image() -> str:
    """The image ref the control-plane compose file runs the Deployment GUI from.

    Version resolution is `bom_seeder.resolve_repo_version` with
    :data:`GUI_IMAGE_VERSION` as the declared version, so the pin above is the
    default while a developer can still override it -- with
    ``HMD_LOCAL_VERSION_HMD_APP_NEURONSPHERE=<version>``, or ``=local`` to take
    their working tree's ``meta-data/VERSION``.

    A locally cached image wins over a published one -- the same rule
    `image_cache.ensure_lambda_image` applies to every Lambda -- so `hmd build` in
    the app repo is enough to iterate on the GUI. Nothing cached falls through to
    the published ref, which compose then pulls.
    """
    from .bom_seeder import resolve_repo_version
    from .image_cache import image_cached, image_candidates

    version = resolve_repo_version(
        GUI_REPO_CLASS, bom_version=GUI_IMAGE_VERSION
    ).version
    published = f"{GUI_PUBLISHED_REGISTRY}/{GUI_REPO_CLASS}:{version}"

    for ref in image_candidates(GUI_REPO_CLASS, version) + [published]:
        if image_cached(ref):
            logger.debug(f"Deployment GUI image: {ref} (cached)")
            return ref

    logger.debug(f"Deployment GUI image: {published} (will be pulled)")
    return published


def export_control_plane_compose_env(*, all_profiles: bool = False) -> None:
    """Set the variables the control-plane compose file interpolates.

    Called before *every* control-plane compose invocation, not just ``up``:
    ``COMPOSE_PROFILES`` decides whether ``docker compose stop`` sees a
    profile-gated service at all, so a `down`/`stop` that skipped this would
    leave the container running.

    ``all_profiles`` is for teardown. Activating only what *this* CLI would
    start is right for ``up`` and wrong for ``stop``, because the two are not
    the same set: ``nsctl`` starts the DAG runner under a profile this CLI
    never activates, and ``hmd neuronsphere down --purge`` consequently left
    ``hmd_nsrunner`` running. The same asymmetry bites within one front end --
    start with the GUI on, unset the variable, stop, and its container
    survives. A teardown therefore names every profile.
    """
    from .bom_seeder import gui_enabled, gui_port

    os.environ["HMD_LOCAL_GUI_HOST_PORT"] = str(gui_port())

    if all_profiles:
        os.environ["COMPOSE_PROFILES"] = ",".join(ALL_COMPOSE_PROFILES)
        # No image resolution: stopping a container does not need to know what
        # it would have been started from, and the compose file's defaults
        # interpolate fine without it.
        return

    if not gui_enabled():
        logger.debug("Deployment GUI disabled by HMD_LOCAL_NEURONSPHERE_ENABLE_GUI")
        return

    os.environ["COMPOSE_PROFILES"] = GUI_COMPOSE_PROFILE
    try:
        os.environ["HMD_DEPLOYMENT_GUI_IMAGE"] = deployment_gui_image()
    except Exception as e:
        # Falling through leaves the compose file's own default in play, which is
        # the published `:stable` tag -- degraded, but not a reason to fail `up`.
        logger.warning(f"Could not resolve the Deployment GUI image: {e}")


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


def ensure_control_plane(
    verbose: bool = False,
    upgrade: bool = False,
    local_loader: Optional[LocalPluginLoader] = None,
) -> bool:
    """Bring up (or reconcile) the control plane. Idempotent.

    Returns True when ms-deployment is reachable afterwards.

    :param local_loader: Shared :class:`LocalPluginLoader` to discover plugins
        with. Pass the same instance a sibling ``start_environment`` call uses
        so plugin discovery (a filesystem scan) runs once per `up`, not once
        per caller. A fresh one is constructed when omitted.
    """
    from . import bootstrap_dag
    from .floci_deployer import (
        DOCKER_NETWORK_NAME,
        prune_apigateway_ghosts,
        control_plane_target,
        ensure_core_databases_direct,
        ensure_neptune_network_alias,
        ensure_rds_network_alias,
        provision_resources,
        resolve_image_uri,
        setup_service,
        wait_for_floci,
        wait_for_rds_instance,
    )
    from .local_workflow_runner import LocalWorkflowRunner
    from .hmd_cli_neuronsphere import (
        _aggregate_hmdms_resources,
        _deploy_hmdms_service_lambdas,
        _deploy_ms_deployment_lambda,
        _ensure_nginx_routed,
        _exec,
        _get_base_command,
        _naming_lambda_env,
        _wait_for_ms_deployment,
        update_images,
    )

    print_section("Control Plane")
    target = control_plane_target()
    reg = env_registry.load()

    local_loader = local_loader or LocalPluginLoader()
    # artifact-lib backs `hmd neuronsphere push-artifact` / `pull-artifact`, so
    # it must always be available. It is control-plane, not per-environment.
    local_loader.ensure_foundation_plugin("artifact-lib", "hmd-ms-artifact-lib")

    export_control_plane_compose_env()

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

    # Drop the *unusable* persisted API Gateway records before Floci starts:
    # Floci has written v1 gateway records with null fields and rehydrates them,
    # so a restart would otherwise serve back undeletable ghosts. Only ghosts go
    # -- a real gateway is kept, because a service the deployment DAG deployed
    # owns a CDKTF-managed gateway that no later step here recreates.
    prune_apigateway_ghosts(_hmd_home() / "floci" / "data")

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

    with spinner_step("Waiting for Floci...", verbose=verbose) as step:
        try:
            wait_for_floci(target=target)
            step.ok()
        except RuntimeError as e:
            step.fail()
            logger.warning(f"{e} — skipping control-plane Lambda deployment")
            print(
                "  The control-plane Floci is reached through hmd_proxy's :4566 "
                "stream, not directly. Check `docker logs hmd_proxy` and that "
                f"`docker inspect -f '{{{{.State.Running}}}}' {target.container}` is true."
            )
            return False

    # --- the control-plane bootstrap DAG ------------------------------------
    #
    # The control plane is brought up by a deployment DAG whose last node is
    # ms-deployment itself, so its Postgres is deployed by the real
    # `hmd-postgres-rds` RepoClass through the same projectbuilder path every
    # other deploy takes, and the whole bring-up is recorded in the deployment
    # graph once that service is serving (see bootstrap_dag).
    #
    # The nodes after the database carry handlers rather than deploy scripts:
    # they provision what the deployment service needs, so they cannot be
    # deployed *through* it. Each closure is the step that used to run inline
    # here, unchanged.
    service_api_ids: Dict[str, str] = {}
    ms_deployment_available = False

    def _ensure_databases(node, destroy):
        # dbaccount is per-environment (matching the cloud), so the control
        # plane has none and its databases are created directly.
        container = wait_for_rds_instance(
            bootstrap_dag.control_plane_db_identifier(target), target=target
        )
        if not container:
            return False
        ensure_rds_network_alias(
            bootstrap_dag.control_plane_db_identifier(target),
            _CANONICAL_DB_HOST,
            target=target,
        )
        ensure_core_databases_direct(container=container)
        return True

    def _ensure_graph(node, destroy):
        return _ensure_control_plane_graph(target)

    def _deploy_naming(node, destroy):
        naming_env, naming_image = _naming_lambda_env()
        if naming_image is None:
            raise RuntimeError(
                "hmd-ms-naming image not cached locally. "
                "Run `hmd build` in hmd-ms-naming and retry."
            )
        service_api_ids["hmd_ms_naming"] = setup_service(
            "hmd_ms_naming", naming_image, naming_env, target=target
        )
        return True

    def _deploy_artifact_lib(node, destroy):
        for spec in _deploy_hmdms_service_lambdas(
            None,
            local_loader,
            plugin_filter=CONTROL_PLANE_PLUGINS,
            target=target,
            verbose=verbose,
        ):
            service_api_ids[spec["function_name"]] = spec["api_id"]
        return True

    def _deploy_deployment(node, destroy):
        nonlocal ms_deployment_available
        try:
            service_api_ids["hmd_ms_deployment"] = _deploy_ms_deployment_lambda(None)
            ms_deployment_available = True
        except Exception as e:
            # Kept non-fatal, as it was inline: the rest of the control plane is
            # still worth having, and `up` reports the degraded state.
            logger.warning(f"ms-deployment Lambda deploy failed: {e}")
            print(f"  Warning: ms-deployment not available: {e}")
            return False
        return True

    # Before the DAG, because its first node is a real CDKTF deploy and CDKTF
    # keeps its state in the `hmd.<account>.<region>.tfstate` bucket this creates
    # -- `tofu init` fails outright against a bucket that does not exist yet.
    # Nothing here needs the database: the admin secret records `hmd_db` as the
    # host, which is the alias the instance gets once it is up.
    print_step("Provisioning control-plane Floci resources...")
    resources: Dict[str, List] = {"s3_buckets": []}
    _aggregate_hmdms_resources(local_loader, resources)
    provision_resources(resources, local_loader=local_loader, target=target)

    runner = LocalWorkflowRunner(
        _MS_DEPLOYMENT_URL,
        env=None,
        verbose=verbose,
        # ms-deployment is this DAG's last node, so there is nothing to report
        # to until it finishes. Buffered now, replayed below.
        tracking=False,
    )
    bootstrap_csd = bootstrap_dag.csd_nid()
    runner.run(
        bootstrap_csd,
        bootstrap_dag.control_plane_nodes(
            ensure_databases=_ensure_databases,
            ensure_graph=_ensure_graph,
            deploy_naming=_deploy_naming,
            deploy_artifact_lib=_deploy_artifact_lib,
            deploy_ms_deployment=_deploy_deployment,
        ),
    )

    from .floci_deployer import deploy_api_gateway

    print_step("Deploying API Gateway stages...")
    for api_id in set(service_api_ids.values()):
        deploy_api_gateway(api_id, target=target)

    print_step("Configuring control-plane routes...")
    nginx_router.render_base_config()
    nginx_router.write_control_plane_routes(service_api_ids)
    nginx_router.write_control_plane_streams(target.alias)
    nginx_router.write_control_plane_vhosts()
    if ms_deployment_available:
        _ensure_nginx_routed(
            f"{_MS_DEPLOYMENT_URL}/api/hmd_lang_deployment.environment"
        )
    else:
        nginx_router.reload()

    if ms_deployment_available:
        print_step("Waiting for ms-deployment...")
        _wait_for_ms_deployment(_MS_DEPLOYMENT_URL)
        # Flush what the DAG buffered while ms-deployment did not exist. Today
        # this records nothing: bootstrap nodes carry locally generated ids that
        # no ms-deployment entity corresponds to, so the calls 404 (see
        # LocalWorkflowRunner.replay_into). Kept, and reporting only what
        # actually landed, so it starts working the moment those entities are
        # registered rather than silently claiming to already.
        replayed = runner.replay_into(_MS_DEPLOYMENT_URL)
        if replayed:
            print_step(f"  recorded {replayed} bootstrap event(s)")
        reg.control_plane.bootstrapped = True
        env_registry.save(reg)

    ensure_mcp_api_key()
    _print_gui_url()

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


def start_environment(
    env: LocalEnvironment,
    verbose: bool = False,
    upgrade: bool = False,
    prune: bool = False,
    local_loader: Optional[LocalPluginLoader] = None,
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
    :param local_loader: Shared :class:`LocalPluginLoader` to discover plugins
        with. Pass the same instance a preceding ``ensure_control_plane`` call
        used so plugin discovery (a filesystem scan) runs once per `up`, not
        once per caller. A fresh one is constructed when omitted.
    """
    from .floci_deployer import (
        prune_apigateway_ghosts,
        deploy_api_gateway,
        env_target,
        prune_control_plane_strays,
        provision_plugin_databases,
        provision_resources,
    )
    from .hmd_cli_neuronsphere import (
        _aggregate_hmdms_resources,
        _deploy_hmdms_service_lambdas,
        _exec,
        _get_base_command,
    )

    print_section(f"Environment: {env.slug}")
    target = env_target(env)
    local_loader = local_loader or LocalPluginLoader()
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

        # Nothing to wait for here any more: the Floci serving this
        # environment's account is the control plane's (already healthy), and its
        # Postgres is an RDS instance the core changeset has not deployed yet --
        # `_run_full_bootstrap` waits for and aliases it between the phases.

        nginx_router.write_env_streams(env)
        nginx_router.reload()

        from .floci_deployer import wait_for_floci

        with spinner_step(
            f"Waiting for Floci (account {env.account_id})...", verbose=verbose
        ) as step:
            try:
                wait_for_floci(target=target)
                step.ok()
            except RuntimeError as e:
                step.fail()
                logger.warning(f"{e} — environment '{env.slug}' is degraded")
                return False

        prune_apigateway_ghosts(env.floci_data_dir)

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

    # The k3s cluster's *name* is deterministic (env.k3s_cluster) whether or not
    # it exists yet -- creation is now a Terraform resource in the `eks-cluster`
    # Phase A DAG node, run inside `_bootstrap_environment` (which also does the
    # container-level reconcile, wait-for-ready, kubeconfig, and operator
    # install -- all of it needs to happen after Phase A's DAG run, which in
    # turn needs to happen after this environment's containers/Floci are up,
    # which is exactly where we are now).
    k3s_enabled = os.environ.get(
        "HMD_LOCAL_NEURONSPHERE_ENABLE_K3S", "true"
    ).lower() not in ("false", "0", "no")
    cluster_name = env.k3s_cluster if k3s_enabled else None

    # dbaccount first, so it can provision the other services' databases.
    service_api_ids: Dict[str, str] = {}
    dbaccount_deployed = _deploy_hmdms_service_lambdas(
        None,
        local_loader,
        plugin_filter=["dbaccount"],
        target=target,
        env=env,
        verbose=verbose,
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
        verbose=verbose,
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
        env,
        hmdms_deployed,
        cluster_name,
        upgrade,
        prune=prune,
        verbose=verbose,
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


def _gui_health(timeout: int = 120) -> Optional[Dict]:
    """Poll the Deployment GUI's ``/health/`` until it answers, and return it.

    A 200 here is a stronger signal than the container's healthcheck (whose
    ``interval`` is 30s) or ``_wait_for_service`` (which accepts any non-5xx and
    throws the body away): the container's own command loops
    ``until python manage.py migrate --noinput``, so gunicorn serving at all
    means the schema is applied. The body then says whether the MCP server came
    up, which is what decides whether minting a key makes sense.

    Returns ``None`` on timeout rather than raising -- nothing here is worth
    failing `up` over.
    """
    import time

    import requests

    from .bom_seeder import gui_port

    url = f"http://localhost:{gui_port()}/health/"
    start = time.time()
    while time.time() - start < timeout:
        try:
            resp = requests.get(url, timeout=5)
            if resp.status_code == 200:
                return resp.json()
        except (requests.RequestException, ValueError):
            pass
        time.sleep(3)
    logger.warning(f"Deployment GUI health at {url} not ready after {timeout}s")
    return None


def _docker_exec(container: str, args: List[str]):
    """Run a command inside a container; ``None`` when docker itself is unavailable."""
    import subprocess

    try:
        return subprocess.run(
            ["docker", "exec", container, *args],
            capture_output=True,
            text=True,
            timeout=60,
        )
    except (subprocess.SubprocessError, OSError) as e:
        logger.warning(f"Could not exec in {container}: {e}")
        return None


def ensure_mcp_api_key() -> None:
    """Mint the local MCP bearer token once, and print it.

    The control plane runs the GUI with ``MCP_API_KEYS_ENABLED`` because Okta is
    not emulated locally, but nothing ever created a credential -- so ``/mcp/``
    answered 401 for every caller. This closes that gap at the one moment the
    plaintext exists: ``MCPApiKey`` stores only a SHA-256, so a key is readable
    exactly once, here.

    Idempotent by way of the management command's ``--if-not-exists``, which
    keys off ``MCP_KEY_NAME``: a second ``up`` prints nothing and leaves an
    already-configured client working.
    """
    from .bom_seeder import gui_enabled

    try:
        if not gui_enabled():
            return

        health = _gui_health()
        if not health:
            return
        if not (health.get("mcp") or {}).get("enabled"):
            # HMD_LOCAL_GUI_MCP_ENABLED=false, or a GUI image predating /mcp.
            logger.debug(
                "MCP server is not enabled on the Deployment GUI; no key minted"
            )
            return

        # The same default the compose file gives DJANGO_SUPERUSER_USERNAME, so
        # overriding the superuser does not mint against a nonexistent account.
        user = os.environ.get("HMD_LOCAL_GUI_SUPERUSER", "testadmin")
        result = _docker_exec(
            GUI_CONTAINER,
            [
                "python",
                "manage.py",
                "create_mcp_api_key",
                "--user",
                user,
                "--name",
                MCP_KEY_NAME,
                "--if-not-exists",
            ],
        )
        if result is None or result.returncode != 0:
            detail = (result.stderr or "").strip() if result else "docker unavailable"
            logger.warning(f"Could not mint an MCP API key: {detail}")
            return

        key = next(
            (
                line.strip()
                for line in (result.stdout or "").splitlines()
                if line.strip().startswith(MCP_KEY_PREFIX)
            ),
            None,
        )
        if key is None:
            # `--if-not-exists` found one; it was printed by the `up` that minted it.
            logger.debug(f"An MCP API key named '{MCP_KEY_NAME}' already exists")
            return

        _print_mcp_api_key(user, key)
    except Exception as e:
        # No key means MCP clients get a 401 -- a worse local stack, but not a
        # reason to fail `up`.
        logger.warning(f"MCP API key bootstrap skipped (non-fatal): {e}")


def _print_mcp_api_key(user: str, key: str) -> None:
    """Show the freshly minted key, once.

    Padded and unindented rather than a ``print_step`` line: this is the only
    time the plaintext exists, so it must not read as one more status line.
    """
    from .bom_seeder import gui_port

    print(
        "\n"
        f"  MCP API key minted for {user} (shown once -- store it now):\n"
        "\n"
        f"      {key}\n"
        "\n"
        f"  Point an MCP client at http://localhost:{gui_port()}/mcp/ -- note the\n"
        "  trailing slash -- sending 'Authorization: Bearer <key>'.\n"
    )


def _print_gui_url() -> None:
    """Point the user at the Deployment GUI.

    Printed separately from the environments' wildcard-hostname line because the
    GUI is a control-plane container reached on a port, so it needs no /etc/hosts
    entry -- and it is the one UI a local user is most likely to want immediately.
    """
    from .bom_seeder import gui_enabled, gui_port

    if not gui_enabled():
        return
    # The same defaults the compose file gives the container's DJANGO_SUPERUSER_*
    # env, so overriding one of them does not make this line a lie.
    user = os.environ.get("HMD_LOCAL_GUI_SUPERUSER", "testadmin")
    password = os.environ.get("HMD_LOCAL_GUI_SUPERUSER_PASSWORD", "testpassword")
    print_step(
        f"  Deployment GUI at http://localhost:{gui_port()}/ "
        f"(sign in as {user}/{password})"
    )


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


def _record_k3s_uid(env: LocalEnvironment, k3s_uid: Optional[str]) -> None:
    """Re-stamp the environment's bootstrap with the cluster it now runs on.

    Without this a recovered environment keeps pointing at the dead cluster's
    UID and every subsequent ``up`` re-detects the same "cluster was recreated".
    """
    if not k3s_uid:
        return
    env_registry.record_bootstrap(
        env, env.bootstrap.get("csd_nid", "control-plane"), k3s_uid=k3s_uid
    )


def _bootstrap_environment(
    env: LocalEnvironment,
    hmdms_deployed: List[Dict],
    cluster_name: Optional[str],
    upgrade: bool,
    prune: bool = False,
    verbose: bool = False,
) -> bool:
    """Seed and deploy this environment's BOM, or reconcile an existing one.

    :returns: whether the environment reached its declared state. A False here
        is what keeps `up` from printing "Ready" over a broken bootstrap.
    """
    from .bom_seeder import (
        EKS_CLUSTER_INSTANCE,
        LOCAL_CORE_BOM,
        ensure_environment,
        resync_local_resources,
        seed_base_resource_definitions,
        seed_bom,
    )
    from . import env_reconcile
    from .change_set_builder import declared_repo_paths
    from .env_manifest import load_manifest
    from .floci_deployer import (
        env_target,
        reconcile_k3s_container,
        wait_for_k3s_ready,
        write_kubeconfig,
    )
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
        base_url,
        cluster_name=cluster_name,
        env=env,
        repo_paths=repo_paths,
        verbose=verbose,
    )

    # Phase A (the core instance, environment-db and -- as a real Terraform DAG
    # node -- the eks-cluster) runs on *every* `up`, bootstrapped or not. Its
    # entries are cheap, idempotent applies once healthy: running
    # `reconcile_k3s_container` immediately before this lets Terraform's own
    # refresh pick up a stale-image recreation with no special-casing here.
    # Computing this environment's live `k3s_uid` below (needed to decide
    # whether the cluster was recreated since the last bootstrap) requires the
    # cluster to already be up, which is why none of this can wait until after
    # the bootstrapped/not-bootstrapped branch below.
    target = env_target(env)
    core_bom = list(LOCAL_CORE_BOM)
    if cluster_name:
        try:
            reconcile_k3s_container(cluster_name, target=target)
        except Exception as e:
            logger.warning(f"k3s container reconcile failed: {e}")
    else:
        core_bom = [
            e for e in core_bom if e.get("repo_instance_name") != EKS_CLUSTER_INSTANCE
        ]

    print_step("Seeding core changeset (Phase A)...")
    csd_nid_a, nodes_a = seed_bom(base_url, bom=core_bom, env=env)
    print_step(f"  {len(nodes_a)} deployment node(s)")
    # Phase A runs through the runner both to deploy this environment's
    # Postgres/cluster and so the core instances' RIDs transition DEPLOY_NEXT ->
    # DEPLOYED. Skipping it would leave them permanently DEPLOY_NEXT and break
    # the reconcile fast-path.
    runner.run(csd_nid_a, nodes_a)
    core_succeeded = list(runner.last_succeeded)

    k3s_uid = None
    if cluster_name:
        try:
            wait_for_k3s_ready(cluster_name, target=target)
            # Before the kubeconfig is written, and so before any kubectl runs:
            # the server URL below points at this stream listener.
            if nginx_router.configure_k3s_host_route(env):
                nginx_router.reload()
                print_step(f"  k3s API served on host :{env.k3s_port}")
            kubeconfig = write_kubeconfig(
                cluster_name,
                env.kubeconfig_path,
                target=target,
                host_port=env.k3s_port,
            )
            # The default environment also writes the historical shared path so
            # host-side kubectl and the robot suites keep working unchanged.
            if env.is_default:
                legacy = _hmd_home() / ".cache" / "k3s" / "kubeconfig"
                legacy.parent.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(kubeconfig, legacy)
                os.environ.setdefault("KUBECONFIG", str(legacy))

            from .k3s_operators import cluster_incarnation_id, provision_k3s_operators

            provision_k3s_operators(env)
            k3s_uid = cluster_incarnation_id(env)
        except Exception as e:
            logger.warning(f"k3s cluster setup failed: {e}")
            print(
                f"  Warning: k3s unavailable — Argo and k3s-dependent plugins "
                f"will be skipped: {e}"
            )

    # Right after Phase A (which created the RDS instance) and the k3s setup
    # above: every Phase-B entry addresses the database as `hmd_db-<slug>` --
    # ms-dbaccount above all -- and the alias's CoreDNS refresh needs the
    # cluster's API server up and its kubeconfig written, both of which just
    # happened. Floci names the container it spawned opaquely, so the alias is
    # what makes that name resolve.
    _alias_environment_database(env)

    if bootstrapped:
        # A recreated k3s cluster has nothing deployed on it even though the
        # persisted graph still says otherwise. Only trust a mismatch when both
        # UIDs are known, so a bootstrap recorded before this feature existed
        # doesn't force a surprise redeploy.
        recorded_uid = env.bootstrap.get("k3s_uid")
        cluster_recreated = bool(k3s_uid and recorded_uid and recorded_uid != k3s_uid)

        print_step(
            f"'{env.slug}' already bootstrapped — reconciling "
            "(skipping Phase B BOM seeding and DAG execution)."
        )
        # Floci stops its Neptune container on shutdown and never restarts it,
        # so on this path -- the fast restart, which runs no Phase B DAG -- the
        # graph would otherwise stay down while its cluster still reports
        # `available`. Also re-publishes the CoreDNS record, which the k3s pass
        # skipped while the container was stopped.
        _alias_environment_graph(env)
        print_step("Resyncing core resources...")
        try:
            count = resync_local_resources(
                base_url, cluster_name, services=specs, env=env
            )
            print_step(f"  {count} core resource(s) resynced")
        except Exception as e:
            logger.warning(f"Local resource resync failed (non-fatal): {e}")

        if cluster_recreated:
            # Only the instances that deploy *onto k3s* were lost with it.
            # Everything else -- S3 buckets, cdktf-to-Floci stacks, Lambdas --
            # lives in Floci, whose state does persist, so redeploying the whole
            # BOM would redo a great deal of work that is still perfectly good.
            # The snapshot records which entries installed a Helm release, and
            # the reconcile plan's cluster cross-check turns exactly those into
            # additions once the cluster comes back empty. Upgrade is forced:
            # this is a recovery, not a discretionary change, and the entries
            # involved are already known to be missing from the cluster.
            known_releases = env_reconcile.load_release_map(env)
            if not known_releases:
                print_step(
                    "k3s cluster was recreated since the last bootstrap and "
                    "there is no record of which instances deploy onto it; "
                    "redeploying the full BOM onto the new cluster."
                )
                return _run_full_bootstrap(
                    env,
                    runner,
                    specs,
                    cluster_name,
                    k3s_uid,
                    base_url,
                    nodes_a,
                    core_succeeded,
                    manifest=manifest,
                )
            print_step(
                "k3s cluster was recreated since the last bootstrap — "
                f"redeploying the {len(known_releases)} instance(s) that deploy "
                "onto it; Floci-side state is kept."
            )
            ok = _reconcile_environment(
                env, runner, base_url, manifest, upgrade=True, prune=prune
            )
            _record_k3s_uid(env, k3s_uid)
            return ok

        ok = _reconcile_environment(
            env, runner, base_url, manifest, upgrade=upgrade, prune=prune
        )

        if k3s_uid and not env.bootstrap.get("k3s_uid"):
            _record_k3s_uid(env, k3s_uid)
        return ok

    print_step("Seeding base resource definitions...")
    try:
        seeded = seed_base_resource_definitions(base_url)
        print_step(f"  {len(seeded)} base resource definitions")
    except Exception as e:
        logger.warning(f"Base resource definition seeding failed: {e}")

    return _run_full_bootstrap(
        env,
        runner,
        specs,
        cluster_name,
        k3s_uid,
        base_url,
        nodes_a,
        core_succeeded,
        manifest=manifest,
    )


def _observed_releases(env, entries: List[Dict]) -> Dict[str, str]:
    """Map applied entries to the Helm releases they actually installed.

    Recorded in the drift snapshot so a later reconcile can tell "this instance
    is DEPLOYED and its release is on the cluster" from "the graph says DEPLOYED
    but the release is gone". Entries that install no release (S3 buckets,
    cdktf-only repos, the ``skip``-strategy core instance) never appear here, so
    they can never be flagged as missing.
    """
    if not entries:
        return {}
    from .k3s_operators import helm_release_name, live_helm_releases

    try:
        live = live_helm_releases(env)
    except Exception as e:
        logger.warning(f"Could not read Helm releases after apply: {e}")
        return {}
    if not live:
        return {}
    observed = {}
    for entry in entries:
        name = entry.get("repo_instance_name")
        if not name:
            continue
        release = helm_release_name(name, env)
        if release in live:
            observed[name] = release
    return observed


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
        GRAPH_INSTANCE,
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

    # --- the graph: unconditional, like Phase A ---------------------------
    # Lazy in *whether* it exists, not in whether a converging `up` deploys it
    # once something in the declared state requires one -- gating this behind
    # `--upgrade` like a discretionary plugin change would leave that
    # dependency broken until the user happened to pass it. Mirrors the
    # unconditional graph changeset `_run_full_bootstrap` seeds on first
    # bootstrap (its `graph_entries` split).
    pending = plan.add + plan.change
    graph_pending = [
        e for e in pending if e.get("repo_instance_name") == GRAPH_INSTANCE
    ]
    pending = [e for e in pending if e.get("repo_instance_name") != GRAPH_INSTANCE]

    graph_settled: List[Dict] = []
    if graph_pending:
        print_step("Seeding graph database...")
        try:
            csd_nid_graph, nodes_graph = seed_bom(
                base_url, bom=graph_pending, env=env, repo_paths=repo_paths
            )
            if runner.run(csd_nid_graph, nodes_graph):
                _alias_environment_graph(env)
                graph_settled = list(graph_pending)
                env_reconcile.merge_snapshot(
                    env,
                    plan.desired,
                    graph_settled,
                    releases=_observed_releases(env, graph_settled),
                )
            else:
                print("\n  Warning: graph deployment failed")
                ok = False
        except Exception as e:
            logger.warning(f"Graph changeset seeding failed: {e}")
            print(f"  Error: could not seed the graph changeset: {e}")
            ok = False

    # --- everything else: additions and redeploys, gated as before --------
    if not pending:
        # Nothing else to deploy (a prune-only run, or the graph above was the
        # only addition). Carry the recorded release map forward -- rewriting
        # the snapshot without it would erase the very thing the next run's
        # cluster cross-check reads. A graph that just failed above must not
        # be recorded as settled by hash, or it would stop being retried.
        desired = plan.desired
        if graph_pending and not graph_settled:
            desired = [
                e for e in desired if e.get("repo_instance_name") != GRAPH_INSTANCE
            ]
        env_reconcile.write_snapshot(
            env, desired, releases=env_reconcile.load_release_map(env)
        )
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
    env_reconcile.merge_snapshot(
        env, plan.desired, settled, releases=_observed_releases(env, settled)
    )
    return ok and bool(ok_run)


def _alias_environment_database(env: LocalEnvironment) -> None:
    """Give this environment's RDS container its canonical DNS name.

    Every consumer addresses the environment's Postgres as ``hmd_db-<slug>`` on
    the Docker network (and as plain ``hmd_db`` inside its k3s cluster, via
    CoreDNS). Floci names the container it spawns opaquely and finds it by label,
    so the alias is what preserves those names -- and keeps the port at 5432
    rather than routing through Floci's RDS proxy range, which does not survive a
    Floci restart. Best-effort: a failure here surfaces as a connection error
    from the consumer, which is more legible than aborting the bootstrap.
    """
    from . import bom_seeder
    from .floci_deployer import ensure_rds_network_alias, env_target

    identifier = bom_seeder.env_db_identifier(env)
    if not ensure_rds_network_alias(
        identifier, env.db_container, target=env_target(env)
    ):
        logger.warning(
            f"Could not alias {identifier} as {env.db_container}; consumers "
            f"addressing that name will fail to connect"
        )
        return

    # Re-apply the in-cluster records now that the database container exists.
    # `provision_k3s_operators` runs long before Phase A creates the RDS
    # instance, so its CoreDNS pass had no container to resolve and simply
    # skipped `hmd_db` -- leaving every chart in k3s unable to resolve the one
    # name they all address the database by. Idempotent: it rewrites the whole
    # `coredns-custom` ConfigMap.
    from .k3s_operators import refresh_coredns_records

    refresh_coredns_records(env)


def _ensure_control_plane_graph(target) -> bool:
    """Make the control-plane graph reachable at ``global-graph``.

    The handler behind the ``control-plane-graph-alias`` bootstrap node. Fatal
    where the environment counterpart below is best-effort: `hmd-ms-artifact-lib`
    declares a *required* `neptune-db` dependency (its persistence is
    ``["dynamo", "graph"]``, with no postgres engine at all), so a control plane
    without a served graph is not worth continuing the DAG for.

    `hmd-inf-neptune`'s deploy_local.sh is idempotent -- an existing cluster is
    "nothing to do" -- so on every `up` after the first, nothing has spawned a
    container by the time this runs. `ensure_neptune_running` is what starts the
    one Floci stopped on the last `down`.
    """
    from . import bootstrap_dag
    from .floci_deployer import (
        ensure_neptune_network_alias,
        ensure_neptune_running,
        neptune_cluster_exists,
    )

    identifier = bootstrap_dag.control_plane_graph_identifier(target)
    if not ensure_neptune_running(identifier, target):
        if neptune_cluster_exists(identifier, target):
            logger.warning(
                f"Control-plane graph cluster {identifier} exists but its "
                "container is missing or would not start"
            )
        else:
            logger.warning(
                f"No control-plane graph cluster {identifier} after its deploy"
            )
        return False
    if not ensure_neptune_network_alias(
        identifier, bootstrap_dag.CONTROL_PLANE_GRAPH_HOST, target=target
    ):
        logger.warning(
            f"Could not alias the control-plane graph as "
            f"{bootstrap_dag.CONTROL_PLANE_GRAPH_HOST}; artifact-lib will "
            "fail to connect"
        )
        return False
    return True


def _alias_environment_graph(env: LocalEnvironment) -> None:
    """Give this environment's graph container its canonical DNS name.

    A no-op unless a graph was actually provisioned -- it is lazy now, deployed
    only when a BOM entry declares a ``database.neuronsphere.io/graph-database``
    dependency. When there is one, the alias is what keeps consumers on
    ``global-graph:8182`` instead of Floci's Gremlin proxy, which is not restored
    after a Floci restart.
    """
    from . import bom_seeder
    from .floci_deployer import (
        ensure_neptune_network_alias,
        ensure_neptune_running,
        env_target,
        neptune_cluster_exists,
        neptune_container_name,
    )

    try:
        identifier = bom_seeder.graph_cluster_identifier(env)
    except Exception as e:
        # Unlike the database, a graph is optional -- most environments have
        # none -- so an unresolvable identifier means "nothing to alias", not a
        # failure worth interrupting `up` for.
        logger.debug(f"No graph identifier for '{getattr(env, 'slug', '?')}': {e}")
        return

    target = env_target(env)
    # Read before the call so a restart can be reported as one. Floci stops its
    # Neptune container on shutdown and never brings it back, so "not running"
    # is the normal state after a restart rather than evidence of no graph.
    was_running = neptune_container_name(identifier, target) is not None
    if not ensure_neptune_running(identifier, target):
        # A missing cluster *record* is the normal case -- most environments
        # have no graph at all. A record that exists with no usable container is
        # a real problem (the container was removed, or Gremlin Server never came
        # up) and must not read the same as "nothing to alias", or it silently
        # stays broken forever.
        if neptune_cluster_exists(identifier, target):
            logger.warning(
                f"Graph cluster {identifier} exists for '{env.slug}' but its "
                "container is missing or would not start; consumers "
                "addressing the graph will fail to connect"
            )
        else:
            logger.debug(f"No graph deployed for '{env.slug}'; nothing to alias")
        return
    if not was_running:
        print_step(f"  restarted the graph container for '{env.slug}'")

    if not ensure_neptune_network_alias(identifier, env.graph_container, target=target):
        logger.warning(
            f"Could not alias {identifier} as {env.graph_container}; consumers "
            f"addressing that name will fail to connect"
        )
        return

    # Publish the in-cluster records now that the name resolves on the Docker
    # network -- the CoreDNS pass skips any canonical name it cannot resolve at
    # the time it runs, and this one only became resolvable a moment ago.
    from .k3s_operators import refresh_coredns_records

    refresh_coredns_records(env)


def _run_full_bootstrap(
    env: LocalEnvironment,
    runner,
    specs: List[Dict],
    cluster_name: Optional[str],
    k3s_uid: Optional[str],
    base_url: str,
    nodes_a: List[Dict],
    core_succeeded: List[str],
    manifest=None,
) -> bool:
    """The Phase B half of the two-phase changeset bootstrap for one environment.

    Phase A (the core instance, environment-db, and the eks-cluster) has
    already been seeded and run by the caller (:func:`_bootstrap_environment`)
    by the time this is called -- it runs on every `up`, not just the first
    one, so its result is passed in rather than redone here. This applies
    everything else. The order is load-bearing: a Phase-B entry that depends
    on a Phase A Resource needs the concrete Resource to already exist for
    selector matching to find it, which is why core-resource submission below
    still happens before Phase B is seeded.
    """
    from .bom_seeder import (
        GRAPH_INSTANCE,
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
        print_step("Submitting core resources...")
        try:
            resources = build_local_core_resources(
                cluster_name=cluster_name, services=specs, env=env
            )
            count = submit_local_resources(base_url, resources, nodes_a)
            print_step(f"  {count} core resource(s) submitted")
        except Exception as e:
            logger.warning(f"Local resource submission failed (non-fatal): {e}")

        phase_b = resolve_plugin_bom(env=env, manifest=manifest)

        # The graph (when the BOM requires one) deploys in its own changeset
        # ahead of the rest of Phase B, exactly like Phase A is split out for
        # the database: any Phase-B sibling seeded in the *same* batch as the
        # graph (e.g. Trino's nsgraph catalog, which resolves `global-graph`
        # eagerly at connector construction) would otherwise start before
        # `_alias_environment_graph` -- which only runs after its deployments
        # finish -- has published the CoreDNS record it needs.
        graph_entries = [
            e for e in phase_b if e.get("repo_instance_name") == GRAPH_INSTANCE
        ]
        rest_entries = [
            e for e in phase_b if e.get("repo_instance_name") != GRAPH_INSTANCE
        ]

        if graph_entries:
            print_step("Seeding graph database...")
            csd_nid_graph, nodes_graph = seed_bom(
                base_url, bom=graph_entries, env=env, repo_paths=repo_paths
            )
            if not runner.run(csd_nid_graph, nodes_graph):
                print("\n  Warning: graph deployment failed")
                ok = False
            _alias_environment_graph(env)

        print_step("Seeding deployment graph (Phase B)...")
        csd_nid, nodes = seed_bom(
            base_url, bom=rest_entries, env=env, repo_paths=repo_paths
        )
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
        applied = [e for e in definition if e["repo_instance_name"] in settled]
        env_reconcile.merge_snapshot(
            env,
            definition,
            applied,
            releases=_observed_releases(env, applied),
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
    from .floci_deployer import FLOCI_ENDPOINT

    print_step(f"  Floci   {FLOCI_ENDPOINT} (account {env.account_id})")

    if manifest_file:
        installed = install_manifest(env.slug, manifest_file)
        print_step(f"  manifest {installed}")

    # Shared across both calls below so plugin discovery (a filesystem scan)
    # runs once, not once per caller.
    local_loader = LocalPluginLoader()

    if not ensure_control_plane(verbose=verbose, local_loader=local_loader):
        print(
            "\n  Warning: control plane is not fully available; environment "
            "creation may be incomplete."
        )

    if deploy:
        start_environment(env, verbose=verbose, local_loader=local_loader)
    else:
        print_step("--no-deploy: registered only, nothing started.")
    print_header("Ready")
    return env


def stop_environment(
    env: LocalEnvironment, verbose: bool = False, purge: bool = False
) -> None:
    """Stop one environment's containers and remove its routes.

    Without ``purge`` this is a *stop*, not a teardown: the k3s cluster is
    stopped rather than deleted and the containers are stopped rather than
    removed, so the next ``up`` restarts them in place and takes the reconcile
    fast path instead of redeploying the whole BOM. ``purge`` is what promises a
    clean slate, and only then is anything actually destroyed.
    """
    from .floci_deployer import (
        delete_k3s_cluster,
        env_target,
        purge_k3s_container_and_volume,
        stop_k3s_cluster,
        stop_neptune_container,
        stop_rds_instance,
    )
    from .hmd_cli_neuronsphere import _exec, _get_base_command

    print_step(f"Stopping environment '{env.slug}'...")
    try:
        if purge:
            delete_k3s_cluster(env.k3s_cluster, target=env_target(env))
        else:
            stop_k3s_cluster(env.k3s_cluster, target=env_target(env))
    except Exception as e:
        logger.debug(f"k3s cluster stop/delete skipped: {e}")

    if not purge:
        # The database and graph are Floci-spawned containers, not compose
        # services or something k3s manages, so nothing else in this function
        # reaches them -- without this they keep running after `down`. Deletion
        # (the purge branch below) already tears them down, so this is only
        # needed here; a no-op (returns False) when the environment never
        # provisioned one.
        from . import bom_seeder

        try:
            stop_rds_instance(bom_seeder.env_db_identifier(env), target=env_target(env))
        except Exception as e:
            logger.debug(f"RDS container stop skipped: {e}")
        try:
            stop_neptune_container(
                bom_seeder.graph_cluster_identifier(env), target=env_target(env)
            )
        except Exception as e:
            logger.debug(f"Neptune container stop skipped: {e}")

    if not env.legacy_layout:
        _export_env_vars(env)
        command = [
            *_get_base_command(
                _env_compose_files(),
                quiet=not verbose,
                project_name=env.compose_project,
            ),
            "down" if purge else "stop",
        ]
        _exec(command, capture=not verbose, quiet=not verbose)

    nginx_router.remove_env_routes(env)
    nginx_router.reload()

    if purge:
        purge_k3s_container_and_volume(env.k3s_cluster, target=env_target(env))
        # The database is a Floci RDS instance, not a compose container, so
        # `compose down` above does not touch it -- and its volume deliberately
        # survives a plain restart (FLOCI_STORAGE_PRUNE_VOLUMES_ON_DELETE is
        # pinned false), which is exactly what a purge has to undo.
        try:
            from . import bom_seeder
            from .floci_deployer import delete_rds_instance, env_target

            delete_rds_instance(
                bom_seeder.env_db_identifier(env), target=env_target(env)
            )
        except Exception as e:
            # A purge must still tear down everything else it can; the RDS
            # instance is the one piece whose id we derive rather than read back,
            # so it is also the one that can fail to resolve.
            logger.warning(f"Could not delete {env.slug}'s RDS instance: {e}")
        try:
            from . import bom_seeder
            from .floci_deployer import delete_neptune_cluster, env_target

            # Floci mounts no volume for Neptune: the graph lives in the
            # container's writable layer, so removing the container *is* the
            # data deletion. Nothing else in `down` touches it, which is what
            # lets a plain restart keep the graph.
            delete_neptune_cluster(
                bom_seeder.graph_cluster_identifier(env), target=env_target(env)
            )
        except Exception as e:
            logger.warning(f"Could not delete {env.slug}'s graph cluster: {e}")
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

    from .bom_seeder import gui_enabled, gui_port

    def _running(container: str) -> bool:
        result = subprocess.run(
            ["docker", "inspect", "-f", "{{.State.Running}}", container],
            capture_output=True,
            text=True,
        )
        return result.returncode == 0 and result.stdout.strip() == "true"

    from .floci_deployer import FLOCI_ENDPOINT, env_target, k3s_container_name

    containers = {
        # Shared: one Floci serves every environment's account.
        "floci": env.floci_container,
        "db": env.db_container,
        "graph": env.graph_container,
        "k3s": k3s_container_name(env.k3s_cluster, env_target(env)),
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
            "floci": FLOCI_ENDPOINT,
            "trino": f"localhost:{env.trino_port}",
            **(
                {
                    "deployment_gui": f"http://localhost:{gui_port()}",
                    # The key itself is unrecoverable -- only its hash is stored --
                    # so this surfaces the endpoint alone.
                    "deployment_gui_mcp": f"http://localhost:{gui_port()}/mcp/",
                }
                if gui_enabled()
                else {}
            ),
        },
        "containers": {
            role: {"name": name, "running": _running(name)}
            for role, name in containers.items()
        },
    }
