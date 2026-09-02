import getpass
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
from typing import Dict, List, Optional
from importlib_metadata import entry_points
from importlib.util import find_spec

from cement.utils.shell import cmd
from dotenv import load_dotenv
from hmd_cli_tools import cd
from hmd_cli_tools.okta_tools import get_auth_token
from hmd_cli_tools.hmd_cli_tools import load_hmd_env
import time

import requests
import yaml

from cement import App, minimal_logger, shell

from .floci_deployer import COMPOSE_PROJECT_NAME, DOCKER_NETWORK_NAME
from .loaders import LocalPluginLoader
from .startup_display import (
    print_banner,
    print_header,
    print_step,
    print_startup_summary,
    print_shutdown_summary,
)
from .validators.port_validator import validate_ports

logger = minimal_logger("hmd_cli_neuronsphere")

ENABLED_PLUGIN_ENTRY_POINT = "hmd_cli_neuronsphere.enabled"
PREPARE_PLUGIN_ENTRY_POINT = "hmd_cli_neuronsphere.prepare_hmd_home"
RESOURCES_PLUGIN_ENTRY_POINT = "hmd_cli_neuronsphere.get_resources"
COMPOSE_PLUGIN_ENTRY_POINT = "hmd_cli_neuronsphere.render_compose_yaml"
POST_DEPLOY_NOTICES_ENTRY_POINT = "hmd_cli_neuronsphere.get_post_deploy_notices"


def _collect_post_deploy_notices(env) -> List[str]:
    """Best-effort per-plugin lines to append to `up`'s "Ready" summary.

    Each entry point is a callable ``(env) -> List[str]``, letting an
    installed plugin (e.g. hmd-cli-plugin-ns-visualization reporting its
    Superset admin login) surface something after deploy without this
    module knowing anything about what the plugin is or what it deployed.
    A broken or misbehaving contributor logs a warning and is skipped --
    an optional plugin's summary line must never block or crash `up` for
    everyone else.
    """
    notices: List[str] = []
    for entrypoint in entry_points(group=POST_DEPLOY_NOTICES_ENTRY_POINT):
        try:
            notices.extend(entrypoint.load()(env) or [])
        except Exception as e:
            logger.warning(
                f"Plugin '{entrypoint.name}' failed to produce post-deploy "
                f"notices ({e}); skipping"
            )
    return notices


def _get_required_env_var(var_name, default=None):
    value = os.environ.get(var_name, default)

    if value is None:
        raise Exception(f"Required environment variable, {var_name}, not set.")
    return value


def _exec(command, capture=False, quiet=False):
    _cmd = " ".join(list(map(str, command)))
    if not quiet:
        print(_cmd)
    return cmd(_cmd, capture=capture)


_hmd_home = Path(_get_required_env_var("HMD_HOME"))
_project_name = COMPOSE_PROJECT_NAME


def _get_base_command(files: List[str], quiet: bool = False, project_name: str = None):
    """Build a `docker compose` invocation.

    ``project_name`` selects the compose project: the control plane has one,
    and each named environment has its own, so their containers and lifecycles
    never overlap.
    """
    stdout, _, _ = _exec(
        ["pip", "config", "get", "global.extra-index-url"],
        capture=True,
        quiet=quiet,
    )
    pip_url = stdout.decode("utf-8")
    os.environ["PIP_EXTRA_INDEX_URL"] = pip_url
    compose_cmd = json.loads(
        os.environ.get("DOCKER_COMPOSE_CMD", '["docker", "compose"]')
    )
    command = [
        *compose_cmd,
        "--project-directory",
        str(_hmd_home / ".cache"),
        "--project-name",
        project_name or _project_name,
    ]
    for file_ in files:
        command += ["-f", str(file_)]
    return command


def _wait_for_service(url: str, timeout: int = 180):
    """Poll a URL until the service responds with any non-5xx status.

    A 4xx (e.g. 404 from `/` on hmd-ms-base, which only registers
    `/api/...` and `/apiop/...` routes) confirms the proxy → API Gateway
    → Lambda chain is wired and the runtime is invokable. Downstream
    callers hit real endpoints and surface their own errors if anything
    is wrong further in.
    """
    start = time.time()
    while time.time() - start < timeout:
        try:
            resp = requests.get(url, timeout=5)
            if resp.status_code < 500:
                return
        except requests.RequestException:
            pass
        time.sleep(3)
    logger.warning(f"Service at {url} not ready after {timeout}s")


def _wait_for_hmd_db(timeout: int = 120, container: str = "hmd_db") -> bool:
    """Wait until a PostgreSQL container is accepting connections.

    The dbaccount Lambda provisions per-service databases by connecting to
    Postgres over the Docker network. If provisioning runs before postgres is
    up (and registered in Docker DNS), the Lambda fails with a name-resolution
    or connection error and the service databases are never created. Polling
    `pg_isready` here removes that race.

    ``container`` defaults to the control-plane ``hmd_db``; each environment has
    its own (``hmd_db-<slug>``).

    :returns: True once ready, False if the timeout elapses.
    """
    start = time.time()
    while time.time() - start < timeout:
        result = subprocess.run(
            ["docker", "exec", container, "pg_isready", "-U", "postgres"],
            capture_output=True,
            text=True,
        )
        if result.returncode == 0:
            return True
        time.sleep(2)
    logger.warning(f"{container} not ready after {timeout}s")
    return False


def _reload_nginx() -> None:
    """Signal the hmd_proxy nginx to reload its config.

    Delegates to ``nginx_router.reload``, which runs ``nginx -t`` first: a
    reload with a bad config leaves the *previous* config serving with only a
    message on stderr, which otherwise surfaces much later as every route 404ing
    (and as `_ensure_nginx_routed` burning all of its retries).
    """
    from . import nginx_router

    nginx_router.reload()


def _ensure_nginx_routed(check_url: str, attempts: int = 12, delay: int = 2) -> bool:
    """Reload nginx and confirm a service route is actually being served.

    An `nginx -s reload` right after writing the config occasionally does not
    take effect on the first try (the running config keeps serving the catch-all
    404). Callers depend on the route being live before probing the service, so
    reload-and-verify: if a POST to ``check_url`` still hits the catch-all
    (`{"error": "no route defined"}`), reload again. Any real backend response
    (even a Lambda cold-start error) means the route is wired.
    """
    for _ in range(attempts):
        _reload_nginx()
        try:
            resp = requests.post(check_url, json={}, timeout=5)
            if '"no route defined"' not in resp.text:
                return True
        except requests.RequestException:
            pass
        time.sleep(delay)
    logger.warning(f"nginx route not live after {attempts} reloads: {check_url}")
    return False


def _wait_for_ms_deployment(base_url: str, timeout: int = 180):
    """Poll ms-deployment by exercising the hmd_lang_deployment.environment CRUD path.

    Stronger than ``_wait_for_service``: a successful POST to
    ``/api/hmd_lang_deployment.environment`` confirms the CRUD layer is
    actually serving entity calls (not just that the runtime is invokable).
    If the search returns an empty list, also seed a ``local`` Environment
    so downstream flows have one available.
    """
    env_url = f"{base_url}/api/hmd_lang_deployment.environment"
    start = time.time()
    while time.time() - start < timeout:
        try:
            resp = requests.post(env_url, json={}, timeout=5)
            if resp.status_code == 200:
                envs = resp.json()
                if isinstance(envs, list):
                    if not envs:
                        put_resp = requests.put(
                            env_url,
                            json={
                                "type": "local",
                                "account_number": "000000000000",
                                "hmd_region": os.environ.get("HMD_REGION", "us-west-2"),
                            },
                            timeout=30,
                        )
                        put_resp.raise_for_status()
                    return
        except requests.RequestException:
            pass
        time.sleep(3)
    logger.warning(f"Service at {env_url} not ready after {timeout}s")


def _read_repo_version(repo_home: str, repo_name: str, default: str = "stable") -> str:
    """Read VERSION from a repo's meta-data, falling back to ``default``.

    ``default`` is the floating ``stable`` tag unless the caller ships a pin for
    the repo, which is a version this CLI was actually tested against.
    """
    if repo_home:
        version_path = os.path.join(repo_home, repo_name, "meta-data", "VERSION")
        try:
            with open(version_path) as f:
                return f.read().strip()
        except FileNotFoundError:
            pass
    return default


def _load_entry_point(name: str, group: str):
    entrypoints = entry_points(name=name, group=group)

    for entrypoint in entrypoints:
        if entrypoint.name == name:
            return entrypoint


def _load_plugins(config_overrides: Dict[str, bool] = {}):
    # Load installed plugins via entry points
    entrypoints = entry_points(group=ENABLED_PLUGIN_ENTRY_POINT)
    plugins = {}
    for entrypoint in entrypoints:
        logger.debug(f"Loading plugin...{entrypoint.name}")
        plugins[entrypoint.name] = entrypoint.load()(config_overrides)

    # Discover and add local plugins from HMD_REPO_HOME
    local_loader = LocalPluginLoader()
    for plugin_name in local_loader.get_enabled_plugins():
        if plugin_name in plugins:
            logger.debug(f"Local plugin overriding installed: {plugin_name}")
        else:
            logger.debug(f"Loading local plugin: {plugin_name}")
        # Local enabled plugins override installed
        plugins[plugin_name] = True

    return plugins


def _prepare_local_plugin(
    local_loader: LocalPluginLoader,
    plugin_name: str,
    hmd_home: Path,
    plugins: Dict[str, bool],
) -> None:
    """
    Prepare HMD_HOME for a local plugin.

    This handles creating directories, copying configs, rendering templates,
    and copying postgres scripts for plugins loaded from HMD_REPO_HOME.

    Args:
        local_loader: The LocalPluginLoader instance
        plugin_name: Name of the plugin
        hmd_home: Path to HMD_HOME
        plugins: Dictionary of plugin enabled states
    """
    from .plugins.base import (
        create_required_dirs,
        copy_configs,
        render_templates,
        copy_postgres_scripts,
        build_template_context,
    )

    config = local_loader.get_plugin_config(plugin_name)
    if not config:
        return

    local_dir = local_loader.get_plugin_local_dir(plugin_name)
    if not local_dir:
        return

    # Create required directories
    required_dirs = config.get("required_dirs", [])
    create_required_dirs(hmd_home, required_dirs)

    # Copy config files - use custom copy since local dir is different
    config_mappings = config.get("config_mappings", [])
    for mapping in config_mappings:
        source = local_dir / mapping["source"]
        dest = hmd_home / mapping["dest"]

        if not source.exists():
            continue

        # Handle if_empty option
        if mapping.get("if_empty"):
            if dest.exists():
                if dest.is_dir() and os.listdir(dest):
                    continue
                elif dest.is_file():
                    continue

        if source.is_dir():
            os.makedirs(dest.parent, exist_ok=True)
            shutil.copytree(source, dest, dirs_exist_ok=True)
        else:
            os.makedirs(dest.parent, exist_ok=True)
            if mapping.get("merge") and dest.exists():
                with open(source, "r") as sf:
                    source_data = json.load(sf)
                with open(dest, "r") as df:
                    dest_data = json.load(df)
                merged = {**dest_data, **source_data}
                with open(dest, "w") as df:
                    json.dump(merged, df)
            else:
                shutil.copy2(source, dest)

    # Render templates - use custom rendering since local dir is different
    templates = config.get("templates", [])
    if templates:
        try:
            from jinja2 import Environment, FileSystemLoader

            templates_dir = local_dir / "templates"
            if templates_dir.exists():
                env = Environment(loader=FileSystemLoader(str(templates_dir)))
                context = build_template_context({}, plugins)

                for template_def in templates:
                    template_file = template_def["source"]
                    dest = hmd_home / template_def["dest"]

                    if "/" in template_file:
                        template_file = os.path.basename(template_file)

                    try:
                        template = env.get_template(template_file)
                        rendered = template.render(**context)
                        os.makedirs(dest.parent, exist_ok=True)
                        with open(dest, "w") as f:
                            f.write(rendered)
                        print(f"Rendered template: {template_file} -> {dest}")
                    except Exception as e:
                        print(
                            f"Warning: Failed to render template {template_file}: {e}"
                        )
        except ImportError:
            print("Warning: Jinja2 not installed, skipping template rendering")

    # Note: postgres_scripts are now handled via auto-generated db init containers
    # in start_neuronsphere() using get_db_init_compose()


def _get_local_plugin_resources(
    local_loader: LocalPluginLoader, plugin_name: str
) -> Dict[str, List]:
    """
    Get resources from a local plugin's nsplugin.json.

    Args:
        local_loader: The LocalPluginLoader instance
        plugin_name: Name of the plugin

    Returns:
        Dictionary of resources
    """
    config = local_loader.get_plugin_config(plugin_name)
    if not config:
        return {}

    return config.get("resources", {})


def _rewrite_floci_depends(compose_path: str, floci_provided: set) -> None:
    """Rewrite depends_on entries for Floci-provided services to point to 'floci'.

    When Floci is enabled, standalone containers like dynamodb and minio no longer
    exist. This rewrites depends_on references in compose files so they depend on
    the floci container instead.
    """
    with open(compose_path, "r") as f:
        cfg = yaml.safe_load(f)

    modified = False
    for svc, svc_cfg in cfg.get("services", {}).items():
        deps = svc_cfg.get("depends_on")
        if isinstance(deps, list):
            new_deps = []
            for d in deps:
                if d in floci_provided:
                    if "floci" not in new_deps:
                        new_deps.append("floci")
                    modified = True
                else:
                    new_deps.append(d)
            svc_cfg["depends_on"] = new_deps
        elif isinstance(deps, dict):
            new_deps = {}
            for d, cond in deps.items():
                if d in floci_provided:
                    if "floci" not in new_deps:
                        new_deps["floci"] = cond
                    modified = True
                else:
                    new_deps[d] = cond
            svc_cfg["depends_on"] = new_deps

    if modified:
        with open(compose_path, "w") as f:
            yaml.safe_dump(cfg, f)
        logger.debug(f"Rewrote Floci-provided depends_on in {compose_path}")


def _resolve_compose_var(value: str) -> str:
    """Resolve Docker Compose ${VAR:-default} and ${VAR} syntax using os.environ."""
    import re

    def _replace(match):
        var = match.group(1)
        if ":-" in var:
            name, default = var.split(":-", 1)
            return os.environ.get(name, default)
        elif "-" in var:
            name, default = var.split("-", 1)
            return os.environ.get(name, default)
        return os.environ.get(var, "")

    return re.sub(r"\$\{([^}]+)\}", _replace, str(value))


def _extract_lambda_services(compose_files: list, resources: dict) -> None:
    """Extract Lambda-deployable services from compose files for Floci deployment.

    When Floci is enabled, services with DD_LAMBDA_HANDLER in their environment
    should be deployed as Lambdas, not as compose containers. This function removes
    them from compose files and marks them in resources for Lambda deployment.
    """
    for compose_path in compose_files:
        path = Path(str(compose_path))
        if not path.exists():
            continue

        with open(path, "r") as f:
            cfg = yaml.safe_load(f)

        if not cfg or "services" not in cfg:
            continue

        to_extract = []
        for svc_name, svc_cfg in cfg["services"].items():
            env = svc_cfg.get("environment", {})
            if "DD_LAMBDA_HANDLER" in env:
                to_extract.append(svc_name)

        if not to_extract:
            continue

        for svc_name in to_extract:
            svc_cfg = cfg["services"].pop(svc_name)
            image = _resolve_compose_var(svc_cfg.get("image", ""))
            raw_env = svc_cfg.get("environment", {})
            # Resolve Docker Compose variable syntax; Lambda env vars must be strings
            env_vars = {}
            for k, v in raw_env.items():
                if v is None:
                    continue
                env_vars[k] = _resolve_compose_var(v) if isinstance(v, str) else str(v)
            container_name = svc_cfg.get("container_name", svc_name)

            # Mark in resources for Lambda deployment
            # Match by container_name, service name, or HMD_INSTANCE_NAME
            # with normalization to handle hmd_ prefix and _/- differences
            instance_name = env_vars.get("HMD_INSTANCE_NAME", "")
            match_names = {
                container_name,
                svc_name,
                instance_name,
                container_name.replace("hmd_", "").replace("_", "-"),
                svc_name.replace("_", "-"),
                f"hmd_{svc_name}",
            }
            match_names.discard("")
            for svc in resources.get("services", []):
                if isinstance(svc, dict) and svc.get("name") in match_names:
                    svc["deploy_as_lambda"] = True
                    svc["image"] = image
                    svc["env_vars"] = env_vars
                    break

            # Remove depends_on references from remaining services
            for remaining_cfg in cfg["services"].values():
                deps = remaining_cfg.get("depends_on")
                if isinstance(deps, list) and svc_name in deps:
                    deps.remove(svc_name)
                elif isinstance(deps, dict) and svc_name in deps:
                    deps.pop(svc_name)

            logger.debug(
                f"Extracted {svc_name} from {path} for Floci Lambda deployment"
            )

        # Write updated compose (may now have only supporting services)
        if cfg["services"]:
            with open(path, "w") as f:
                yaml.safe_dump(cfg, f)
        else:
            # No services left, remove the compose file
            path.unlink()
            compose_files.remove(compose_path)


def _is_local_only_plugin(local_loader: LocalPluginLoader, plugin_name: str) -> bool:
    """
    Check if a plugin is local-only (no installed entry point).

    Args:
        local_loader: The LocalPluginLoader instance
        plugin_name: Name of the plugin

    Returns:
        True if plugin is local-only
    """
    # Check if there's an entry point for this plugin
    entrypoints = entry_points(group=ENABLED_PLUGIN_ENTRY_POINT)
    for ep in entrypoints:
        if ep.name == plugin_name:
            return False
    # If no entry point, it's local-only
    return local_loader.has_local_plugin(plugin_name)


def _aggregate_hmdms_resources(
    local_loader: LocalPluginLoader, resources: Dict[str, List]
) -> None:
    """Merge hmdms_service buckets into the shared resources dict in-place."""
    for plugin_name in local_loader.get_enabled_plugins():
        hmdms_resources = local_loader.get_hmdms_resources(plugin_name)
        for k, v in hmdms_resources.items():
            if isinstance(v, list):
                resources[k] = [*resources.get(k, []), *v]
            else:
                resources[k] = v


def _apply_env_overrides(env_vars: Dict[str, str], env, target=None) -> None:
    """Point a Lambda's environment at its own environment's account and database.

    ``get_hmdms_lambda_spec`` builds control-plane defaults: ``AWS_ENDPOINT_URL``
    of ``neuronsphere:4566``, a ``SERVICE_CONFIG`` whose postgres host is
    ``hmd_db``, and ``HMD_DID`` from the ambient process env. An environment's
    Lambda must instead reach *its* Floci and *its* Postgres, so rewrite those
    three here (in place) rather than plumbing ``env`` through the whole spec
    builder.

    Database and user names are deliberately untouched: each environment has its
    own Postgres server, so ``hmd_ms_transform`` in one environment is a
    different database on a different host -- exactly as in the cloud.
    """
    from .floci_deployer import env_target as _env_target

    target = target or _env_target(env)
    env_vars["AWS_ENDPOINT_URL"] = target.internal_endpoint
    env_vars["HMD_DID"] = env.deployment_id

    service_config = env_vars.get("SERVICE_CONFIG")
    if not service_config:
        return
    try:
        config = json.loads(service_config)
    except (TypeError, ValueError):
        logger.warning("SERVICE_CONFIG is not valid JSON; leaving its DB host as-is")
        return
    engines = config.get("hmd_db_engines") or {}
    changed = False
    for engine in engines.values():
        engine_config = engine.get("engine_config") or {}
        if engine_config.get("host") == "hmd_db":
            engine_config["host"] = env.db_container
            changed = True
        # Gremlin engines address the graph container by name too.
        if engine_config.get("db_host") == "global-graph":
            engine_config["db_host"] = env.graph_container
            changed = True
    if changed:
        env_vars["SERVICE_CONFIG"] = json.dumps(config)


def _deploy_hmdms_service_lambdas(
    api_id: str,
    local_loader: LocalPluginLoader,
    plugin_filter: Optional[List[str]] = None,
    skip_function_names: Optional[set] = None,
    exclude_plugins: Optional[List[str]] = None,
    target=None,
    env=None,
    verbose: bool = False,
) -> List[Dict]:
    """Deploy each enabled HMDMS-service plugin as a Floci Lambda.

    Returns a list of deployed spec dicts (from get_hmdms_lambda_spec) suitable
    for ms-deployment seeding. Skips plugins whose image is not cached locally,
    logging a clear warning so the user can run `hmd build` and retry.

    `plugin_filter`: when set, only deploy plugins whose name is in this list.
    Used to bring up dbaccount alone before any DBs exist (two-phase up).

    `exclude_plugins`: when set, skip plugins whose name is in this list. Used to
    keep control-plane services (artifact-lib) out of the per-environment pass.

    `skip_function_names`: when set, skip plugins whose function_name is already
    in this set. Used by the post-DB-provisioning pass to avoid re-deploying
    dbaccount.

    `target`/`env`: which Floci account to deploy into. The spec builder emits
    control-plane defaults (`neuronsphere:4566`, `hmd_db`, the ambient HMD_DID),
    so for an environment those three are overridden here rather than threading
    `env` through the ~200-line spec builder.
    """
    from .floci_deployer import (
        resolve_image_uri,
        setup_service,
        build_gozer_rds_secrets,
    )
    from .startup_display import spinner_step

    deployed: List[Dict] = []
    seen_function_names: set = set(skip_function_names or [])

    for plugin_name in local_loader.get_enabled_plugins():
        if plugin_filter is not None and plugin_name not in plugin_filter:
            continue
        if exclude_plugins is not None and plugin_name in exclude_plugins:
            continue

        spec = local_loader.get_hmdms_lambda_spec(plugin_name)
        if not spec:
            continue

        function_name = spec["function_name"]
        if function_name in seen_function_names:
            if skip_function_names and function_name in skip_function_names:
                logger.debug(
                    f"Skipping {function_name} (already deployed in earlier pass)"
                )
            else:
                logger.warning(
                    f"Duplicate hmdms_service lambda_name '{function_name}' "
                    f"(plugin '{plugin_name}'); skipping subsequent registration"
                )
            continue
        seen_function_names.add(function_name)

        with spinner_step(f"Deploying {plugin_name}", verbose=verbose) as step:
            repo_name = spec["repo_name"]
            repo_version = spec["repo_version"]
            image = resolve_image_uri(repo_name, repo_version)
            if image is None:
                logger.warning(
                    f"HMDMS service '{plugin_name}' image for "
                    f"{repo_name}:{repo_version} not found locally. "
                    f"Run `hmd build` in the repo and retry. Skipping Lambda deploy."
                )
                hint = ""
                if plugin_name == "artifact-lib":
                    hint = (
                        " Without it, `hmd neuronsphere push-artifact` and "
                        "`pull-artifact` will return 'no route defined'."
                    )
                step.fail(f"{plugin_name} (image not cached)")
                print(f"  Run `hmd build` in {repo_name} and retry.{hint}")
                continue

            env_vars = dict(spec["env_vars"])
            graph_host = env.graph_container if env is not None else "global-graph"
            if plugin_name == "gozer":
                env_vars["RDS_SECRETS"] = json.dumps(
                    build_gozer_rds_secrets(local_loader)
                )
                env_vars["NEPTUNE_ENDPOINTS"] = json.dumps({"global-graph": graph_host})
                env_vars.setdefault("LIBRARIAN_DYNAMO_TABLES", "{}")
                env_vars.setdefault("S3_BUCKETS", "{}")
                env_vars.setdefault("DYNAMO_TABLE_NAMES", "[]")

            if env is not None:
                _apply_env_overrides(env_vars, env, target)

            spec["image"] = image
            spec["env_vars"] = env_vars
            svc_api_id = setup_service(
                function_name, image, env_vars, api_id=api_id, target=target
            )
            spec["api_id"] = svc_api_id
            deployed.append(spec)
            logger.debug(f"Deployed HMDMS service Lambda: {function_name} ({image})")
            step.ok(plugin_name)

    return deployed


def _deploy_ms_deployment_lambda(api_id: str) -> str:
    """Deploy ms-deployment as a Floci Lambda; return its function ARN.

    Used by both Platform and Extend modes so ms-deployment is the single
    source of truth for "what's deployed locally".
    """
    from .environments import MS_DEPLOYMENT_VERSION
    from .floci_deployer import resolve_image_uri, setup_service

    repo_home = os.environ.get("HMD_REPO_HOME", "")
    deployment_version = os.environ.get(
        "HMD_MS_DEPLOYMENT_VERSION",
        _read_repo_version(
            repo_home, "hmd-ms-deployment", default=MS_DEPLOYMENT_VERSION
        ),
    )

    image_uri = resolve_image_uri("hmd-ms-deployment", deployment_version)
    if image_uri is None:
        raise RuntimeError(
            f"hmd-ms-deployment image for version {deployment_version} not "
            f"cached locally. Run `hmd build` in hmd-ms-deployment and retry."
        )

    deployment_service_config = json.dumps(
        {
            "loader_config": {
                "deployment": ["hmd-lang-deployment", "hmd-lang-timestamp-record"],
                "artifact": ["hmd-lang-librarian"],
            },
            "service_loader": "deployment",
            "operations_modules": [
                "hmd_ms_base.crud_operations",
                "hmd_ms_deployment.deployment_ops",
            ],
            "hmd_db_engines": {
                "postgres": {
                    "engine_type": "postgres",
                    "engine_config": {
                        "host": "hmd_db",
                        "user": "hmd_ms_deployment",
                        "password": "hmd_ms_deployment",
                        "db_name": "hmd_ms_deployment",
                    },
                }
            },
            "hmd_entity_config": {"__default__": {"persistence": ["postgres"]}},
        }
    )

    deployment_env = {
        "HMD_CUSTOMER_CODE": os.environ.get("HMD_CUSTOMER_CODE", "none"),
        "HMD_DID": os.environ.get("HMD_DID", "aaa"),
        "HMD_ENVIRONMENT": "local",
        "HMD_REGION": os.environ.get("HMD_REGION", "reg1"),
        "HMD_INSTANCE_NAME": "ms-deployment",
        "HMD_REPO_NAME": "hmd-ms-deployment",
        "HMD_REPO_VERSION": deployment_version,
        "HMD_HOSTNAME": os.environ.get("HMD_HOSTNAME", "localhost"),
        "HMD_USE_FASTAPI": "true",
        "SERVICE_CONFIG": deployment_service_config,
        "AWS_DEFAULT_REGION": os.environ.get("AWS_REGION", "us-west-2"),
        "AWS_ACCESS_KEY_ID": os.environ.get("AWS_ACCESS_KEY_ID", "dummykey"),
        "AWS_SECRET_ACCESS_KEY": os.environ.get("AWS_SECRET_ACCESS_KEY", "dummykey"),
        "AWS_XRAY_SDK_ENABLED": "false",
        "DD_LAMBDA_HANDLER": "hmd_ms_base.hmd_ms_base.handler",
        "DD_TRACE_ENABLED": "false",
        "DD_LOCAL_TEST": "true",
    }

    return setup_service(
        "hmd_ms_deployment",
        image_uri,
        deployment_env,
        api_id=api_id,
    )


def _seed_telemetry_profiles(
    local_loader: LocalPluginLoader, plugins: Dict[str, bool]
) -> None:
    """Seed telemetry profiles from local plugins into telemetry-debug service.

    Skips gracefully if telemetry-debug is not enabled or no profiles are defined.
    Waits for the service to be ready before sending profiles.

    Args:
        local_loader: The LocalPluginLoader instance
        plugins: Dictionary of plugin enabled states
    """
    # Skip if telemetry-debug is not an enabled plugin
    if not plugins.get("telemetry-debug"):
        logger.debug("telemetry-debug plugin not enabled, skipping profile seeding")
        return

    profiles = local_loader.get_telemetry_profiles()
    if not profiles:
        logger.debug("No telemetry_profiles found in any plugin")
        return

    logger.debug(
        f"Seeding {len(profiles)} telemetry profile(s) into telemetry-debug..."
    )

    # Wait for telemetry-debug service to be ready
    base_url = "http://localhost/ms-telemetry-debug"
    ready = False
    for attempt in range(30):
        try:
            resp = requests.post(
                f"{base_url}/api/service_profile",
                json={},
                timeout=5,
            )
            if resp.status_code < 500:
                ready = True
                break
        except requests.RequestException:
            pass
        logger.debug(
            f"Waiting for telemetry-debug to be ready (attempt {attempt + 1}/30)..."
        )
        time.sleep(2)

    if not ready:
        logger.warning(
            "telemetry-debug service not ready after 60s, skipping profile seeding"
        )
        return

    # Seed profiles
    try:
        resp = requests.post(
            f"{base_url}/apiop/seed_profiles",
            json={"profiles": profiles},
            timeout=30,
        )
        if resp.status_code == 200:
            result = resp.json()
            logger.debug(f"Telemetry profile seeding complete: {result}")
        else:
            logger.warning(
                f"Telemetry profile seeding failed: "
                f"HTTP {resp.status_code} - {resp.text}"
            )
    except requests.RequestException as e:
        logger.warning(f"Telemetry profile seeding failed: {e}")


def _assert_no_legacy_env_floci_state() -> None:
    """Refuse to start over state from the container-per-environment layout.

    Environments used to run their own Floci with their own
    ``<state_dir>/floci/data``; they are now accounts inside the single
    control-plane Floci. That old state cannot be migrated -- Floci namespaces
    persisted records by an undocumented account key prefix -- and starting
    anyway would abandon each environment's Lambdas, gateways, buckets and
    secrets while reporting success. Fail loudly with the one command that fixes
    it instead.
    """
    from . import env_registry

    try:
        stale = env_registry.legacy_env_floci_state(env_registry.load())
    except Exception as e:  # a registry we cannot read is not this check's problem
        logger.debug(f"Skipping legacy Floci state check: {e}")
        return
    if not stale:
        return
    names = ", ".join(sorted(e.slug for e in stale))
    raise SystemExit(
        f"\n  ERROR: environment(s) {names} still hold state from the "
        f"per-environment Floci layout.\n\n"
        f"  Every environment is now an account inside the single Floci, and that "
        f"older\n  state cannot be migrated into it. Start clean with:\n\n"
        f"      hmd neuronsphere down --purge\n"
    )


def _resolve_mode() -> str:
    """Resolve the operating mode, mapping legacy names to current names.

    Extend mode (single-Floci control plane with DAG-based deployment) is the
    default. The legacy compose-only path is still reachable via
    ``HMD_LOCAL_NEURONSPHERE_MODE=platform`` (or the deprecated ``legacy``).
    """
    mode = os.environ.get("HMD_LOCAL_NEURONSPHERE_MODE", "extend")
    return {"legacy": "platform", "deploy": "extend"}.get(mode, mode)


# ── Bootstrap state ─────────────────────────────────────────────────────────
# The first `up` fully bootstraps the deployment graph (seeds the BOM and runs
# the deployment DAG, which deploys repo classes into Floci/k3s). Subsequent
# `up`s after a `down` must NOT re-run that expensive workflow — Floci storage
# (`FLOCI_STORAGE_MODE: persistent`) and the PostgreSQL bind mount preserve the
# deployment state, so we only restart containers and re-wire the Lambda/proxy
# routing. A marker file records that a successful bootstrap happened; the
# ms-deployment graph itself is the authoritative fallback if the marker is
# missing but the persisted data survived.


def _bootstrap_marker_path() -> Path:
    return _hmd_home / ".cache" / "neuronsphere" / "bootstrap.json"


def _read_bootstrap_marker() -> Optional[Dict]:
    path = _bootstrap_marker_path()
    if not path.exists():
        return None
    try:
        return json.loads(path.read_text())
    except (json.JSONDecodeError, OSError):
        return None


def _write_bootstrap_marker(
    csd_nid: str, mode: str = "extend", k3s_uid: Optional[str] = None
) -> None:
    path = _bootstrap_marker_path()
    try:
        path.parent.mkdir(parents=True, exist_ok=True)
        marker = {"mode": mode, "csd_nid": csd_nid}
        if k3s_uid:
            marker["k3s_uid"] = k3s_uid
        path.write_text(json.dumps(marker, indent=2))
    except OSError as e:
        logger.warning(f"Could not write bootstrap marker: {e}")


def _clear_bootstrap_marker() -> None:
    try:
        _bootstrap_marker_path().unlink()
    except FileNotFoundError:
        pass


def _ms_deployment_has_completed_csd(base_url: str) -> bool:
    """Return True if ms-deployment has ANY COMPLETED ChangeSetDeployment.

    .. warning::
       This is **not environment-scoped**. Once more than one named environment
       exists, a completed changeset from *another* environment satisfies it, so
       it must never gate a specific environment's bootstrap -- doing so would
       leave a brand-new environment permanently un-bootstrapped. Per-environment
       bootstrap state lives in the environment registry
       (``env_registry.record_bootstrap`` / ``LocalEnvironment.bootstrap``) and is
       what ``environments._bootstrap_environment`` actually branches on. This
       remains only as a control-plane-wide signal.

    Any connection/parse error (e.g. the service isn't deployed yet) is treated
    as "not bootstrapped", which is the safe default.
    """
    url = f"{base_url}/api/hmd_lang_deployment.change_set_deployment"
    try:
        resp = requests.post(url, json={}, timeout=10)
        if resp.status_code != 200:
            return False
        records = resp.json()
        if not isinstance(records, list):
            records = records.get("items", []) if isinstance(records, dict) else []
        for rec in records:
            if isinstance(rec, dict) and rec.get("csd_status") == "COMPLETED":
                return True
    except (requests.RequestException, ValueError):
        return False
    return False


def _is_already_bootstrapped(base_url: str) -> bool:  # pragma: no cover - legacy shim
    """Decide whether the deployment workflow has already run for this env.

    Fast path: the local marker file (written at the end of a successful
    bootstrap, cleared only by `down --purge`). Fallback: query the persisted
    ms-deployment graph for a COMPLETED ChangeSetDeployment.
    """
    if _read_bootstrap_marker() is not None:
        return True
    return _ms_deployment_has_completed_csd(base_url)


def start_neuronsphere(
    config_overrides: Dict[str, bool] = {},
    verbose: bool = False,
    upgrade: bool = False,
    env_name: str = None,
    prune: bool = False,
) -> bool:
    print_banner()

    # Verify the host can resolve `neuronsphere`/`neuronsphere-workload` to
    # loopback before doing anything else. Without this, presigned URLs
    # returned by in-network services would be unreachable from the host
    # and the user would hit confusing DNS errors deep in the build/publish
    # flow rather than a clear setup instruction up front.
    from .floci_deployer import ensure_neuronsphere_hosts_entry

    ensure_neuronsphere_hosts_entry()
    _assert_no_legacy_env_floci_state()

    mode = _resolve_mode()
    if mode == "extend":
        return start_neuronsphere_extend(
            verbose=verbose, upgrade=upgrade, env_name=env_name, prune=prune
        )
    else:
        if env_name:
            raise SystemExit(
                "  ERROR: --env requires extend mode. Platform (legacy) mode runs a "
                "single stack with no named environments.\n"
                "  Unset HMD_LOCAL_NEURONSPHERE_MODE to use the default extend mode."
            )
        if prune:
            raise SystemExit(
                "  ERROR: --prune requires extend mode. Platform (legacy) mode has "
                "no declared environment state to reconcile against."
            )
        start_neuronsphere_platform(
            config_overrides=config_overrides, verbose=verbose, upgrade=upgrade
        )
        return True


def _naming_lambda_env():
    """Build the ms-naming Lambda's environment and resolve its image.

    ms-naming is control-plane: one instance serves every environment, so its
    config points at the control-plane Postgres and Floci.

    :returns: ``(env_vars, image_uri)``; ``image_uri`` is None when not cached.
    """
    from .floci_deployer import resolve_image_uri

    repo_home = os.environ.get("HMD_REPO_HOME", "")
    naming_version = os.environ.get(
        "HMD_MS_NAMING_VERSION",
        _read_repo_version(repo_home, "hmd-ms-naming"),
    )

    naming_service_config = json.dumps(
        {
            "loader_config": {
                "naming": ["hmd-lang-naming"],
                "deployment": ["hmd-lang-deployment"],
            },
            "service_loader": "naming",
            "operations_modules": [
                "hmd_ms_naming.hmd_ms_naming",
                "hmd_ms_base.crud_operations",
            ],
            "hmd_db_engines": {
                "postgres": {
                    "engine_type": "postgres",
                    "engine_config": {
                        "host": "hmd_db",
                        "user": "hmd_ms_naming",
                        "password": "hmd_ms_naming",
                        "db_name": "hmd_ms_naming",
                    },
                }
            },
            "hmd_entity_config": {"__default__": {"persistence": ["postgres"]}},
        }
    )

    naming_env = {
        "HMD_CUSTOMER_CODE": os.environ.get("HMD_CUSTOMER_CODE", "none"),
        "HMD_DID": os.environ.get("HMD_DID", "aaa"),
        "HMD_ENVIRONMENT": "local",
        "HMD_REGION": os.environ.get("HMD_REGION", "reg1"),
        "HMD_INSTANCE_NAME": "ms-naming",
        "HMD_REPO_NAME": "hmd-ms-naming",
        "HMD_REPO_VERSION": naming_version,
        "HMD_HOSTNAME": os.environ.get("HMD_HOSTNAME", "localhost"),
        "HMD_USE_FASTAPI": "true",
        "SERVICE_CONFIG": naming_service_config,
        "AWS_DEFAULT_REGION": os.environ.get("AWS_REGION", "us-west-2"),
        "AWS_ACCESS_KEY_ID": os.environ.get("AWS_ACCESS_KEY_ID", "dummykey"),
        "AWS_SECRET_ACCESS_KEY": os.environ.get("AWS_SECRET_ACCESS_KEY", "dummykey"),
        "AWS_XRAY_SDK_ENABLED": "false",
        "DD_LAMBDA_HANDLER": "hmd_ms_base.hmd_ms_base.handler",
        "DD_TRACE_ENABLED": "false",
        "DD_LOCAL_TEST": "true",
    }

    return naming_env, resolve_image_uri("hmd-ms-naming", naming_version)


def start_neuronsphere_extend(
    verbose: bool = False,
    upgrade: bool = False,
    env_name: str = None,
    prune: bool = False,
) -> bool:
    """Start the control plane, then one named environment.

    :returns: whether everything reached its declared state. `up` reports
        "Ready" only for a True; anything else is degraded and exits non-zero,
        so a failed bootstrap is not buried above a success banner.

    The control plane (ms-deployment, ms-naming, artifact-lib, plus the
    supporting Floci, nginx, Postgres and JanusGraph) is shared. Each named
    environment is a self-contained account with its own Floci, k3s cluster,
    Postgres, JanusGraph and ms-dbaccount -- see ``environments.py``.
    """
    from . import env_registry
    from .environments import ensure_control_plane, start_environment

    load_hmd_env()
    print_header("Starting")

    reg = env_registry.load()
    if env_name:
        env = env_registry.resolve_env(env_name, reg)
    else:
        env = env_registry.ensure_default_env(reg)
        env_registry.save(reg)

    # Shared across both calls below so plugin discovery (a filesystem scan
    # of HMD_REPO_HOME / HMD_LOCAL_PLUGINS) runs once per `up`, not once per
    # caller -- each constructs its own loader by default otherwise.
    local_loader = LocalPluginLoader()

    if not ensure_control_plane(
        verbose=verbose, upgrade=upgrade, local_loader=local_loader
    ):
        print_header("Ready (degraded)")
        print("\n  The control plane is up but ms-deployment is unavailable;")
        print("  environment bootstrap was skipped.\n")
        return False

    ok = start_environment(
        env, verbose=verbose, upgrade=upgrade, prune=prune, local_loader=local_loader
    )

    print_header("Ready" if ok else "Ready (degraded)")
    print("\n  Control plane")
    print("    ms-deployment:  http://localhost/hmd_ms_deployment/")
    print("    ms-naming:      http://localhost/hmd_ms_naming/")
    print("    artifact-lib:   http://localhost/hmd_ms_artifact_lib/")
    print("    Floci:          http://localhost:4566")
    print(f"\n  Environment '{env.slug}' (account {env.account_id})")
    print(f"    services:       http://localhost/{env.slug}/<service>/")
    print(f"    Floci:          http://localhost:{env.floci_port}")
    print(f"    Trino:          localhost:{env.trino_port}")
    for notice in _collect_post_deploy_notices(env):
        print(f"    {notice}")
    others = [e.slug for e in env_registry.list_envs(reg) if e.slug != env.slug]
    if others:
        print(f"\n  Other environments: {', '.join(others)}")
    if not ok:
        print(f"\n  Environment '{env.slug}' did not reach its declared state —")
        print("  see the errors above. The endpoints listed are still routed.")
    print()
    return ok


def start_neuronsphere_platform(
    config_overrides: Dict[str, bool] = {},
    verbose: bool = False,
    upgrade: bool = False,
):
    load_hmd_env()

    print_header("Starting")

    # --upgrade: repull the compose-service images first (docker only fetches
    # changed layers). Non-fatal.
    if upgrade:
        print_step("Upgrade — pulling latest images...")
        try:
            update_images()
        except Exception as e:
            logger.warning(f"Image pull failed (non-fatal): {e}")
            print(f"  Warning: image pull failed: {e}")

    home_projects_path = _hmd_home / "studio" / "projects"
    hmd_repo_home = os.environ.get("HMD_REPO_HOME")

    if os.environ.get("HMD_PROJECTS_PATH") is None:
        os.environ["HMD_PROJECTS_PATH"] = (
            str(home_projects_path)
            if os.path.exists(home_projects_path)
            else hmd_repo_home
        )

    assert (
        os.environ.get("HMD_PROJECTS_PATH") is not None
    ), "Cannot find path to NeuronSphere Projects. Please set the HMD_REPO_HOME environment variable to location of Neuronsphere Projects with hmd configure set-env."

    os.environ["UID"] = getpass.getuser()

    if os.path.exists(_hmd_home / "transform" / "queries" / "query_config.json"):
        with open(_hmd_home / "transform" / "queries" / "query_config.json", "r") as qc:
            os.environ["TRANSFORM_GRAPH_QUERY_CONFIG"] = json.dumps(json.load(qc))
    else:
        os.environ["TRANSFORM_GRAPH_QUERY_CONFIG"] = "{}"

    print_step("Loading plugins...")
    plugins = _load_plugins(config_overrides=config_overrides)
    resources = {"buckets": [], "s3_buckets": []}

    # Create local plugin loader for handling local-only plugins
    local_loader = LocalPluginLoader()
    # dbaccount is foundational for cloud-parity DB provisioning — auto-load
    # it from HMD_REPO_HOME if the user hasn't listed it in HMD_LOCAL_PLUGINS.
    local_loader.ensure_foundation_plugin("dbaccount", "hmd-ms-dbaccount")
    # artifact-lib backs the `hmd neuronsphere push-artifact` / `pull-artifact`
    # CLI commands, so it must always be available locally.
    local_loader.ensure_foundation_plugin("artifact-lib", "hmd-ms-artifact-lib")

    # Aggregate HMDMS-service buckets (canonical s3_buckets key)
    _aggregate_hmdms_resources(local_loader, resources)

    # Get Plugin Resources
    for plugin, enabled in plugins.items():
        if enabled:
            entrypoint = _load_entry_point(plugin, RESOURCES_PLUGIN_ENTRY_POINT)

            if entrypoint is not None:
                plugin_resources = entrypoint.load()()
            else:
                plugin_resources = {}

            # Use local plugin resources as fallback when entry point returned nothing
            if not plugin_resources and local_loader.has_local_plugin(plugin):
                local_resources = _get_local_plugin_resources(local_loader, plugin)
                for k, v in local_resources.items():
                    if isinstance(v, list):
                        plugin_resources[k] = [*plugin_resources.get(k, []), *v]
                    else:
                        plugin_resources[k] = v

            for k, v in plugin_resources.items():
                if k in resources:
                    resources[k] = [*resources[k], *v]
                else:
                    resources[k] = v

    print_step("Preparing environment...")
    # Prepare HMD_HOME from plugins
    for plugin, enabled in plugins.items():
        if enabled:
            entrypoint = _load_entry_point(plugin, PREPARE_PLUGIN_ENTRY_POINT)
            if entrypoint is not None:
                entrypoint.load()(_hmd_home, plugins)
            elif local_loader.has_local_plugin(plugin):
                _prepare_local_plugin(local_loader, plugin, _hmd_home, plugins)
    compose_files = []

    cache_dir = Path(_hmd_home) / ".cache" / "local_services"
    # Floci is required — all microservices deploy as Lambdas behind API Gateway
    use_floci = True
    # Services provided inside the Floci container that replace standalone containers
    floci_provided_services = {"dynamodb", "minio"}

    if not os.path.exists(cache_dir):
        os.makedirs(cache_dir, exist_ok=True)

    # Clean stale compose files for microservices when Floci is enabled
    # (microservices are now deployed as Lambdas, not compose containers)
    if use_floci and os.path.exists(cache_dir):
        for svc_dir in os.listdir(cache_dir):
            svc_path = cache_dir / svc_dir
            if svc_path.is_dir() and svc_dir.startswith("hmd-ms-"):
                for f in os.listdir(svc_path):
                    if f.startswith("docker-compose."):
                        stale = svc_path / f
                        logger.debug(
                            f"Removing stale compose file for Floci Lambda service: {stale}"
                        )
                        os.unlink(stale)

    if os.path.exists(cache_dir):
        for root, _, files in os.walk(cache_dir):
            for f in files:
                if f.startswith("docker-compose."):
                    compose_path = os.path.join(root, f)

                    if use_floci:
                        _rewrite_floci_depends(compose_path, floci_provided_services)

                    compose_files.append(compose_path)
                    with open(compose_path, "r") as yml:
                        cfg = yaml.safe_load(yml)
                    for svc, svc_cfg in cfg.get("services", {}).items():
                        if "BUCKET_NAME" in svc_cfg.get("environment", {}):
                            resources["buckets"].append(
                                {
                                    "name": svc_cfg.get("container_name", svc),
                                    "url": svc_cfg.get("environment", {}).get(
                                        "BUCKET_NAME"
                                    ),
                                }
                            )

                if f.startswith("resources."):
                    with open(os.path.join(root, f), "r") as rjson:
                        local_resources = json.load(rjson)
                        for k, v in local_resources.items():
                            resources[k] = [*resources.get(k, []), *v]

    # Render Compose Files
    from .k3s_chart_plugins import k3s_charts_enabled, is_k3s_chart_plugin

    _k3s_charts = k3s_charts_enabled()
    for plugin, enabled in plugins.items():
        if enabled:
            # Converted plugins run on k3s (deployed after operators); don't also
            # start them as compose containers.
            if _k3s_charts and is_k3s_chart_plugin(plugin):
                logger.debug(f"Plugin '{plugin}' runs on k3s; skipping compose service")
                continue
            entrypoint = _load_entry_point(plugin, COMPOSE_PLUGIN_ENTRY_POINT)
            compose_file = None
            if entrypoint is not None:
                compose_file = entrypoint.load()(
                    resources, _hmd_home / ".cache", plugins
                )
                if compose_file is not None:
                    if use_floci:
                        _rewrite_floci_depends(
                            str(compose_file), floci_provided_services
                        )
                    compose_files.append(compose_file)
            if compose_file is None and local_loader.has_local_plugin(plugin):
                compose_path = local_loader.get_compose_path(plugin)
                if compose_path:
                    # Copy to cache so we don't modify the source repo
                    cached = _hmd_home / ".cache" / compose_path.name
                    shutil.copy2(str(compose_path), str(cached))
                    if use_floci:
                        _rewrite_floci_depends(str(cached), floci_provided_services)
                    compose_files.append(str(cached))
                    logger.debug(f"Added local compose file: {cached}")

    # Extract Lambda-deployable services from compose when Floci is enabled
    if use_floci:
        _extract_lambda_services(compose_files, resources)

    # Inject environment variables from local plugin configs
    for plugin_name in local_loader.get_enabled_plugins():
        env_vars = local_loader.get_env_vars(plugin_name)
        for key, value in env_vars.items():
            os.environ[key] = value
            logger.debug(f"Set env var from local plugin {plugin_name}: {key}")

    # Generate db init containers for local plugins with postgres_scripts
    db_init_services = {}
    for plugin_name in local_loader.get_enabled_plugins():
        init_compose = local_loader.get_db_init_compose(plugin_name)
        if init_compose:
            db_init_services.update(init_compose)
            logger.debug(f"Generated db init container for plugin: {plugin_name}")

    if db_init_services:
        db_init_compose = {
            "services": db_init_services,
            "networks": {
                "neuronsphere_default": {
                    "external": True,
                    "name": DOCKER_NETWORK_NAME,
                }
            },
        }
        db_init_path = _hmd_home / ".cache" / "docker-compose.db-init.yml"
        with open(db_init_path, "w") as f:
            yaml.dump(db_init_compose, f)
        compose_files.append(str(db_init_path))

    # Ensure docker-compose.main.yml is first (it creates the neuronsphere_default network)
    main_compose = str(_hmd_home / ".cache" / "docker-compose.main.yml")
    if main_compose in [str(f) for f in compose_files]:
        compose_files = [f for f in compose_files if str(f) != main_compose]
        compose_files.insert(0, main_compose)

    print_step("Validating ports...")
    # Check for port conflicts before starting containers
    validate_ports(compose_files)

    # Create the (per-HMD_HOME-scoped) Docker network (some compose files declare it as external)
    _exec(
        ["docker", "network", "create", DOCKER_NETWORK_NAME],
        capture=True,
        quiet=not verbose,
    )

    quiet = not verbose

    # Write a placeholder nginx config so hmd_proxy can start in phase 1
    # before write_nginx_config runs after Lambda deployment.
    nginx_config_path = _hmd_home / ".cache" / "nginx" / "neuronsphere.conf"
    if not nginx_config_path.exists():
        nginx_config_path.write_text(
            "events {}\nhttp {\n  server {\n    listen 80;\n"
            "    location / { return 503 'starting...'; }\n  }\n}\n"
        )

    # Drop Floci's persisted API Gateway state before it starts -- see the
    # matching call in `start_neuronsphere_extend`. Every gateway is recreated
    # by `setup_service` below, so nothing is lost.
    from .floci_deployer import clear_apigateway_state

    clear_apigateway_state(_hmd_home / "floci" / "data")

    # Phase 1: bring up only foundation services (db, floci, proxy).
    # Application services start in phase 2 after dbaccount has provisioned
    # their DBs (cloud-parity flow). Both platform and extend modes now run a
    # single Floci instance.
    print_step("Starting foundation containers...")
    phase1_services = ["db", "floci", "proxy"]
    command = [
        *_get_base_command(compose_files, quiet=quiet),
        "up",
        "-d",
        "--no-deps",
        "--quiet-pull",
        *phase1_services,
    ]
    if quiet:
        stdout, stderr, retcode = _exec(command, capture=True, quiet=True)
        if retcode != 0:
            err_output = stderr.decode("utf-8") if stderr else ""
            if err_output:
                print(f"\n  Error starting foundation containers:\n{err_output}")
    else:
        _exec(command)

    # Floci post-startup: provision resources, deploy Lambdas, configure API Gateway
    from .floci_deployer import (
        wait_for_floci,
        provision_resources,
        setup_service,
        write_nginx_config,
        create_api_gateway,
        deploy_api_gateway,
        ensure_k3s_cluster,
        wait_for_k3s_ready,
        write_kubeconfig,
        K3S_CLUSTER_NAME,
        K3S_KUBECONFIG_PATH,
        provision_plugin_databases,
    )

    print_step("Waiting for Floci...")
    floci_ready = True
    try:
        wait_for_floci()
    except RuntimeError as e:
        logger.warning(f"{e} — skipping Floci provisioning")
        print(f"  Warning: {e}")
        floci_ready = False

    svc_names: Dict[str, str] = {}
    hmdms_deployed: List[Dict] = []
    ms_deployment_url = None

    if floci_ready:
        print_step("Provisioning Floci resources...")
        provision_resources(resources, local_loader=local_loader)

        # Deploy dbaccount alone first so it can provision every other DB.
        dbaccount_deployed = _deploy_hmdms_service_lambdas(
            None, local_loader, plugin_filter=["dbaccount"]
        )
        if dbaccount_deployed:
            hmdms_deployed.extend(dbaccount_deployed)
            for spec in dbaccount_deployed:
                svc_names[spec["function_name"]] = spec["api_id"]
            print_step("Deploying API Gateway stage for dbaccount...")
            deploy_api_gateway(dbaccount_deployed[0]["api_id"])
            write_nginx_config(svc_names, nginx_config_path)
            _exec(
                ["docker", "exec", "hmd_proxy", "nginx", "-s", "reload"],
                capture=True,
                quiet=True,
            )

            print_step("Provisioning databases via ms-dbaccount...")
            # Platform mode is a single stack: its dbaccount serves the core
            # databases too (there is no separate control plane to create them).
            provision_plugin_databases(local_loader, include_core=True)
        else:
            logger.warning(
                "dbaccount Lambda not deployed (image missing or plugin not enabled); "
                "skipping DB provisioning. Application services may fail to start."
            )

    # Phase 2: bring up the remaining containers now that the DBs exist.
    print_step("Starting application containers...")
    command = [
        *_get_base_command(compose_files, quiet=quiet),
        "up",
        "--remove-orphans",
        "-d",
        "--quiet-pull",
    ]
    if quiet:
        stdout, stderr, retcode = _exec(command, capture=True, quiet=True)
        if retcode != 0:
            err_output = stderr.decode("utf-8") if stderr else ""
            if err_output:
                print(f"\n  Error starting application containers:\n{err_output}")
    else:
        _exec(command)

    if floci_ready:
        # Create the k3s cluster on Floci EKS so plugins (Argo, etc.) can install onto it
        if os.environ.get("HMD_LOCAL_NEURONSPHERE_ENABLE_K3S", "true").lower() not in (
            "false",
            "0",
            "no",
        ):
            print_step(f"Creating k3s cluster '{K3S_CLUSTER_NAME}'...")
            try:
                ensure_k3s_cluster()
                wait_for_k3s_ready()
                kubeconfig_path = write_kubeconfig()
                os.environ["KUBECONFIG"] = str(kubeconfig_path)
                print_step(f"  k3s ready (kubeconfig: {kubeconfig_path})")
                print_step("Installing cluster operators onto k3s...")
                from .k3s_operators import provision_k3s_operators
                from .k3s_chart_plugins import (
                    k3s_charts_enabled,
                    provision_k3s_chart_plugins,
                )

                provision_k3s_operators()
                if k3s_charts_enabled():
                    print_step("Deploying bundled Helm charts to k3s...")
                    provision_k3s_chart_plugins(local_loader, plugins, resources)
            except Exception as e:
                logger.warning(f"k3s cluster creation failed: {e}")
                print(
                    f"  Warning: k3s unavailable — Argo and k3s-dependent plugins will be skipped: {e}"
                )

        print_step("Deploying services to Floci...")
        # One API Gateway per service: Floci's matcher treats every
        # `{proxy+}` resource as a global wildcard regardless of parent
        # path, so multiple services on a single gateway end up catching
        # each other's traffic. Per-service gateways have exactly one
        # `{proxy+}` resource each, eliminating the ambiguity.
        for svc in resources.get("services", []):
            if isinstance(svc, dict) and svc.get("deploy_as_lambda"):
                image_uri = svc["image"]
                env_vars = svc.get("env_vars", {})
                svc_api_id = setup_service(svc["name"], image_uri, env_vars)
                svc_names[svc["name"]] = svc_api_id
                svc["url"] = f"http://hmd_proxy/{svc['name']}/"

        # Deploy remaining HMDMS-service plugins as Floci Lambdas, skipping
        # any (dbaccount) we already deployed in phase 1.
        already = {s["function_name"] for s in hmdms_deployed}
        rest = _deploy_hmdms_service_lambdas(
            None, local_loader, skip_function_names=already
        )
        hmdms_deployed.extend(rest)
        for spec in rest:
            svc_names[spec["function_name"]] = spec["api_id"]

        # Deploy ms-deployment Lambda (always available in both modes)
        if os.environ.get(
            "HMD_LOCAL_NEURONSPHERE_DISABLE_MS_DEPLOYMENT", ""
        ).lower() not in ("true", "1", "yes"):
            try:
                ms_deployment_api_id = _deploy_ms_deployment_lambda(None)
                svc_names["hmd_ms_deployment"] = ms_deployment_api_id
                ms_deployment_url = "http://localhost/hmd_ms_deployment"
            except Exception as e:
                logger.warning(f"ms-deployment Lambda deploy failed: {e}")
                print(f"  Warning: ms-deployment not available: {e}")

        if svc_names:
            print_step("Deploying API Gateway stages...")
            for svc_api_id in set(svc_names.values()):
                deploy_api_gateway(svc_api_id)

        # Rewrite nginx config with full per-service routing and reload
        write_nginx_config(svc_names, nginx_config_path)
        _exec(
            ["docker", "exec", "hmd_proxy", "nginx", "-s", "reload"],
            capture=True,
            quiet=True,
        )

        # Platform mode has no BOM/changeset flow, and hmd-ms-deployment/
        # hmd-ms-naming/hmd-ms-dbaccount are never registered as RepoClass/
        # RepoInstance entities (see extend mode's Phase A) -- just wait for
        # ms-deployment to come up so routing/proxying works.
        if ms_deployment_url and (hmdms_deployed or "hmd_ms_deployment" in svc_names):
            print_step("Waiting for ms-deployment...")
            _wait_for_ms_deployment(ms_deployment_url)

    print_step("Registering services...")
    logger.debug("Upserting local services to Naming Service...")
    for svc in resources.get("services", []):
        if isinstance(svc, dict):
            name = svc.get("name")
            url = svc.get("url")

            if name is None or url is None:
                logger.debug(
                    f"Cannot upsert service name or url missing. Name: {name} URL: {url}"
                )
                continue

            requests.put(
                f"http://localhost/ms-naming/apiop/service/{name}/local",
                data={"httpEndpoint": url},
            )

    logger.debug("Updating database connections file...")
    conn_file_path = cache_dir / ".." / "connections.yml"

    conns = {"databases": {}}
    if conn_file_path.exists():
        with open(conn_file_path, "r") as c:
            conns = yaml.safe_load(c)

    for db in resources.get("databases", []):
        if not isinstance(db, dict):
            continue
        logger.debug(f"Adding {db['database']}")
        conns["databases"][db["database"]] = {"host": "hmd_db", **db}

    with open(conn_file_path, "w") as c:
        yaml.dump(conns, c)

    # Seed telemetry profiles from local plugins
    _seed_telemetry_profiles(local_loader, plugins)

    print_startup_summary(resources)


def _get_cached_compose_files(include_local_services: bool = False):
    load_hmd_env()
    home_projects_path = _hmd_home / "studio" / "projects"
    hmd_repo_home = os.environ.get("HMD_REPO_HOME")

    if os.environ.get("HMD_PROJECTS_PATH") is None:
        os.environ["HMD_PROJECTS_PATH"] = (
            str(home_projects_path)
            if os.path.exists(home_projects_path)
            else hmd_repo_home
        )

    compose_files = []
    for file_ in os.listdir(_hmd_home / ".cache"):
        if file_ == "connections.yml":
            continue
        if file_.endswith(".yml"):
            compose_files.append(_hmd_home / ".cache" / file_)

    if include_local_services:
        cache_dir = Path(_hmd_home) / ".cache" / "local_services"

        if os.path.exists(cache_dir):
            for root, _, files in os.walk(cache_dir):
                for f in files:
                    if f.startswith("docker-compose."):
                        compose_files.append(os.path.join(root, f))

    return compose_files


def stop_neuronsphere(verbose: bool = False, purge: bool = False, env_name: str = None):
    mode = _resolve_mode()
    if mode == "extend":
        stop_neuronsphere_extend(verbose=verbose, purge=purge, env_name=env_name)
    else:
        if env_name:
            raise SystemExit(
                "  ERROR: --env requires extend mode. Platform (legacy) mode runs a "
                "single stack with no named environments."
            )
        stop_neuronsphere_platform(verbose=verbose)


def _purge_control_plane_state(verbose: bool = False) -> None:
    """Remove the control plane's persisted Floci/PostgreSQL/graph state.

    Forces the next `up` to re-run the full bootstrap. Per-environment state is
    purged separately by ``environments.stop_environment`` -- see
    ``stop_neuronsphere_extend``, which purges every environment first.
    """
    print_step("Purging control-plane state (Floci, PostgreSQL, graph)...")
    _clear_bootstrap_marker()
    for rel in ("floci/data", "postgresql/data", "graph_db"):
        target = _hmd_home / rel
        try:
            if target.exists():
                shutil.rmtree(target)
                if verbose:
                    print(f"  removed {target}")
        except OSError as e:
            logger.warning(f"Could not remove {target}: {e}")


# Retained for backwards compatibility with external callers.
_purge_persistent_state = _purge_control_plane_state


def stop_neuronsphere_extend(
    verbose: bool = False, purge: bool = False, env_name: str = None
):
    """Stop the control plane and its environments.

    With ``env_name`` only that environment is stopped and the control plane is
    left running -- other environments keep working. Without it, every
    registered environment is stopped (in reverse creation order) and then the
    control plane itself.

    Without ``purge`` this stops containers rather than removing them and leaves
    the Docker network in place, so a following ``up`` restarts everything as it
    was and reconciles instead of redeploying the whole BOM.

    ``purge`` tears the containers and network down and drops persisted state.
    Note that purging the control plane destroys ms-deployment's graph for
    *every* environment, so the caller is expected to have confirmed that.
    """
    from . import env_registry
    from .environments import (
        control_plane_compose_files,
        export_control_plane_compose_env,
        stop_environment,
    )

    load_hmd_env()
    print_header("Stopping")

    reg = env_registry.load()

    if env_name:
        env = env_registry.resolve_env(env_name, reg)
        stop_environment(env, verbose=verbose, purge=purge)
        print_shutdown_summary()
        return

    for env in reversed(env_registry.list_envs(reg)):
        try:
            stop_environment(env, verbose=verbose, purge=purge)
        except Exception as e:
            logger.warning(f"Could not stop environment '{env.slug}': {e}")
            print(f"  Warning: could not stop environment '{env.slug}': {e}")

    local_loader = LocalPluginLoader()
    compose_files = control_plane_compose_files(local_loader)
    # Sets COMPOSE_PROFILES, without which `stop` does not see the Deployment GUI
    # service and would leave its container running.
    export_control_plane_compose_env()

    print_step("Stopping control-plane containers...")
    quiet = not verbose
    command = [
        *_get_base_command(compose_files, quiet=quiet),
        "down" if purge else "stop",
    ]
    _exec(command, capture=quiet, quiet=quiet)

    if purge:
        # Only torn down on a purge. A stopped container's endpoint pins the
        # network by *id*, so removing and recreating it would leave every
        # stopped container -- the k3s node in particular -- unable to start,
        # forcing the cluster recreate (and full BOM redeploy) that stopping
        # rather than deleting is meant to avoid. An idle network costs nothing.
        print_step("Removing network...")
        _exec(
            ["docker", "network", "rm", DOCKER_NETWORK_NAME],
            capture=True,
            quiet=not verbose,
        )

        _purge_control_plane_state(verbose=verbose)
        try:
            shutil.rmtree(env_registry.environments_root(), ignore_errors=True)
            env_registry.registry_path().unlink()
        except FileNotFoundError:
            pass
        except OSError as e:
            logger.warning(f"Could not remove the environment registry: {e}")

    print_shutdown_summary()


def stop_neuronsphere_platform(verbose: bool = False):
    load_hmd_env()

    print_header("Stopping")

    compose_files = _get_cached_compose_files(include_local_services=True)

    # Also discover and include local plugin compose files
    local_loader = LocalPluginLoader()
    for plugin_name in local_loader.get_enabled_plugins():
        compose_path = local_loader.get_compose_path(plugin_name)
        if compose_path and str(compose_path) not in [str(f) for f in compose_files]:
            compose_files.append(str(compose_path))
            logger.debug(f"Added local plugin compose file for down: {compose_path}")

    # Best-effort: delete the k3s cluster while Floci is still up.
    try:
        from .floci_deployer import delete_k3s_cluster

        delete_k3s_cluster()
    except Exception as e:
        logger.debug(f"k3s cluster delete (best-effort) skipped: {e}")

    print_step("Stopping containers...")
    quiet = not verbose
    command = [*_get_base_command(compose_files, quiet=quiet), "down"]
    _exec(command, capture=quiet, quiet=quiet)

    print_step("Removing network...")
    # Clean up the neuronsphere_default network
    _exec(
        ["docker", "network", "rm", DOCKER_NETWORK_NAME],
        capture=True,
        quiet=not verbose,
    )

    print_shutdown_summary()


def restart_service(service_name: List[str] = None):
    load_hmd_env()
    compose_files = _get_cached_compose_files(include_local_services=True)

    # Also discover and include local plugin compose files
    local_loader = LocalPluginLoader()
    for plugin_name in local_loader.get_enabled_plugins():
        compose_path = local_loader.get_compose_path(plugin_name)
        if compose_path and str(compose_path) not in [str(f) for f in compose_files]:
            compose_files.append(str(compose_path))
            logger.debug(f"Added local plugin compose file for restart: {compose_path}")

    command = [*_get_base_command(compose_files), "up", "-d"]

    if service_name is not None:
        command += service_name
    _exec(command)


def merge_configs(config: Dict, default: Dict):
    for key, value in config.items():
        if isinstance(value, dict):
            node = default.setdefault(key, {})
            merge_configs(value, node)
        elif isinstance(value, list):
            node = default.get(key, [])
            default[key] = [*value, *node]
        else:
            default[key] = value

    return default


MICROSERVICE_DB_INIT_SQL = """
CREATE USER {username} WITH PASSWORD '{password}';
CREATE DATABASE {database};
GRANT ALL PRIVILEGES ON DATABASE {database} TO {username};
"""


def run_local_service(
    repo_name: str,
    repo_version: str,
    instance_name: str,
    mount_packages: List[str] = [],
    db_init: bool = True,
    docker_compose: dict = {},
):
    load_hmd_env()

    use_floci = (
        os.environ.get(
            "HMD_LOCAL_NEURONSPHERE_ENABLE_FLOCI",
            os.environ.get("HMD_LOCAL_NEURONSPHERE_ENABLE_MINISTACK", "true"),
        )
        != "false"
    )

    if use_floci:
        _run_local_service_floci(repo_name, repo_version, instance_name, db_init)
        return

    stdout, _, _ = _exec(
        ["pip", "config", "get", "global.extra-index-url"], capture=True
    )
    pip_url = stdout.decode("utf-8")
    os.environ["PIP_EXTRA_INDEX_URL"] = pip_url
    auth_token = get_auth_token()
    if auth_token is not None:
        os.environ["HMD_AUTH_TOKEN"] = auth_token
    volumes = []

    for mnt in mount_packages:
        spec = find_spec(mnt.replace("-", "_"))

        pkg_path = spec.origin

        volumes.append(
            {
                "type": "bind",
                "source": str(Path.resolve(Path(pkg_path).parent)),
                "target": f"/usr/local/lib/python3.9/site-packages/{mnt.replace('-','_')}",
            }
        )

    resources = {
        "services": [
            {"name": instance_name, "url": f"http://hmd_proxy/{instance_name}"}
        ]
    }
    service_config = {}

    if os.path.exists("./meta-data/config_local.json"):
        with open("./meta-data/config_local.json", "r") as local_cfg:
            service_config = json.load(local_cfg)

    default_config = {
        "version": "3.7",
        "services": {
            repo_name.replace("-", "_"): {
                "image": f"{os.environ.get('HMD_CONTAINER_REGISTRY')}/{repo_name}:{repo_version}",
                "container_name": instance_name,
                "environment": {
                    "HMD_INSTANCE_NAME": instance_name,
                    "HMD_REPO_NAME": repo_name,
                    "HMD_REPO_VERSION": repo_version,
                    "HMD_ENVIRONMENT": os.environ.get("HMD_ENVIRONMENT", "local"),
                    "HMD_REGION": os.environ.get("HMD_REGION", "local"),
                    "HMD_AUTH_TOKEN": os.environ.get("HMD_AUTH_TOKEN"),
                    "HMD_CUSTOMER_CODE": os.environ.get("HMD_CUSTOMER_CODE"),
                    "HMD_DID": "aaa",
                    "HMD_DB_HOST": "db",
                    "HMD_DB_USER": repo_name.replace("-", "_"),
                    "HMD_DB_PASSWORD": repo_name.replace("-", "_"),
                    "HMD_DB_NAME": repo_name.replace("-", "_"),
                    "HMD_USE_FASTAPI": "true",
                    "AWS_XRAY_SDK_ENABLED": False,
                    "AWS_ACCESS_KEY_ID": "dummykey",
                    "AWS_SECRET_ACCESS_KEY": "dummykey",
                    "AWS_DEFAULT_REGION": os.environ.get("AWS_REGION", "us-west-2"),
                    "SERVICE_CONFIG": json.dumps(service_config),
                    "DD_LAMBDA_HANDLER": "hmd_ms_base.hmd_ms_base.handler",
                    "DD_API_KEY": "${DD_API_KEY}",
                    "DD_LOCAL_TEST": True,
                    "DD_TRACE_ENABLED": False,
                    "DD_SERVERLESS_LOGS_ENABLED": False,
                },
                "expose": [8080],
                "volumes": volumes,
                "networks": ["neuronsphere_default"],
            },
        },
    }

    if os.environ.get("HMD_LOCAL_NEURONSPHERE_ENABLE_TELEMETRY", "true") == "true":
        default_config["services"][repo_name.replace("-", "_")]["environment"][
            "HMD_OTEL_ENDPOINT"
        ] = "http://otel-collector:4317/"

    if db_init:
        default_config["services"][f"{repo_name}_db_init"] = {
            "image": "${HMD_LOCAL_NS_CONTAINER_REGISTRY}/hmd-postgres-base:${HMD_POSTGRES_BASE_VERSION:-stable}",
            "container_name": f"{repo_name}_db_init",
            "environment": {
                "HMD_ENVIRONMENT": os.environ.get("HMD_ENVIRONMENT", "local"),
                "HMD_REGION": os.environ.get("HMD_REGION", "local"),
                "HMD_CUSTOMER_CODE": os.environ.get("HMD_CUSTOMER_CODE"),
                "HMD_DID": "aaa",
                "PGPASSWORD": "admin",
            },
            # No published port: this container only ever runs `psql -h db`
            # in-network. The old mapping derived its host port from the number
            # of local services, so it silently shifted whenever one was added
            # or removed and could collide with an already-running container.
            "command": 'psql -h db --username postgres -a --dbname "$POSTGRES_DB" -f /root/sql/db_init.sql',
            "networks": ["neuronsphere_default"],
        }
        resources["databases"] = [
            {
                "username": repo_name.replace("-", "_"),
                "password": repo_name.replace("-", "_"),
                "database": repo_name.replace("-", "_"),
            }
        ]

    config = docker_compose
    if os.path.exists("./src/docker"):
        with cd("./src/docker"):
            if os.path.exists("docker-compose.local.yaml"):
                with open("docker-compose.local.yaml", "r") as dc:
                    config = yaml.safe_load(dc)

    final_config = merge_configs(config, default_config)

    cache_dir = Path(os.environ["HMD_HOME"]) / ".cache" / "local_services" / repo_name

    if not os.path.exists(cache_dir):
        os.makedirs(cache_dir, exist_ok=True)

    with cd(cache_dir):
        path = cache_dir / f"docker-compose.{instance_name}.yaml"
        resource_path = cache_dir / f"resources.{instance_name}.json"
        if db_init:
            sql_path = cache_dir / "db_init.sql"

            with open(sql_path, "w") as sql:
                sql.write(
                    MICROSERVICE_DB_INIT_SQL.format(
                        username=repo_name.replace("-", "_"),
                        password=repo_name.replace("-", "_"),
                        database=repo_name.replace("-", "_"),
                    )
                )

            final_config["services"][f"{repo_name}_db_init"]["volumes"] = [
                {
                    "type": "bind",
                    "source": str(sql_path),
                    "target": "/root/sql/db_init.sql",
                }
            ]

        with open(path, "w") as fcfg:
            yaml.dump(final_config, fcfg)

        with open(resource_path, "w") as r:
            json.dump(resources, r, indent=2)

        start_neuronsphere()


def _run_local_service_floci(
    repo_name: str,
    repo_version: str,
    instance_name: str,
    db_init: bool = True,
):
    """Deploy a local service as a Lambda function in Floci."""
    from .floci_deployer import (
        setup_service,
        create_api_gateway,
        deploy_api_gateway,
        wait_for_floci,
    )

    image_uri = f"{os.environ.get('HMD_CONTAINER_REGISTRY')}/{repo_name}:{repo_version}"

    service_config = {}
    if os.path.exists("./meta-data/config_local.json"):
        with open("./meta-data/config_local.json", "r") as local_cfg:
            service_config = json.load(local_cfg)

    env_vars = {
        "HMD_INSTANCE_NAME": instance_name,
        "HMD_REPO_NAME": repo_name,
        "HMD_REPO_VERSION": repo_version,
        "HMD_ENVIRONMENT": os.environ.get("HMD_ENVIRONMENT", "local"),
        "HMD_REGION": os.environ.get("HMD_REGION", "local"),
        "HMD_CUSTOMER_CODE": os.environ.get("HMD_CUSTOMER_CODE", ""),
        "HMD_DID": "aaa",
        "HMD_DB_HOST": "hmd_db",
        "HMD_DB_USER": repo_name.replace("-", "_"),
        "HMD_DB_PASSWORD": repo_name.replace("-", "_"),
        "HMD_DB_NAME": repo_name.replace("-", "_"),
        "HMD_USE_FASTAPI": "true",
        "AWS_XRAY_SDK_ENABLED": "false",
        "AWS_ACCESS_KEY_ID": "dummykey",
        "AWS_SECRET_ACCESS_KEY": "dummykey",
        "AWS_DEFAULT_REGION": os.environ.get("AWS_REGION", "us-west-2"),
        "SERVICE_CONFIG": json.dumps(service_config),
    }

    if os.environ.get("HMD_LOCAL_NEURONSPHERE_ENABLE_TELEMETRY", "true") == "true":
        env_vars["HMD_OTEL_ENDPOINT"] = "http://otel-collector:4317/"

    wait_for_floci()
    api_id = setup_service(instance_name, image_uri, env_vars)
    deploy_api_gateway(api_id)
    service_url = f"http://hmd_proxy/{instance_name}/"

    resources = {"services": [{"name": instance_name, "url": service_url}]}
    if db_init:
        resources["databases"] = [
            {
                "username": repo_name.replace("-", "_"),
                "password": repo_name.replace("-", "_"),
                "database": repo_name.replace("-", "_"),
            }
        ]

    # Save resources for service registration
    cache_dir = Path(os.environ["HMD_HOME"]) / ".cache" / "local_services" / repo_name
    if not os.path.exists(cache_dir):
        os.makedirs(cache_dir, exist_ok=True)

    resource_path = cache_dir / f"resources.{instance_name}.json"
    with open(resource_path, "w") as r:
        json.dump(resources, r, indent=2)

    start_neuronsphere()


def print_status(json_mode: bool = False, env_name: str = None) -> None:
    """Print what's deployed in local NeuronSphere.

    Reads from ms-deployment (when reachable) and Floci's Lambda registry.
    Cross-references the local plugin map for image, bucket env vars, and
    plugin-source info. Output is human-readable by default; ``--json`` emits
    a single JSON document for the future Django app to consume.

    Lambdas are listed from the selected environment's own Floci account --
    each environment has its own, so an unscoped listing would mix them.
    """
    import json as _json

    from . import env_registry

    load_hmd_env()

    try:
        env = env_registry.resolve_env(env_name)
    except env_registry.EnvRegistryError:
        env = None

    local_loader = LocalPluginLoader()
    hmdms_specs = {}
    for plugin_name in local_loader.get_enabled_plugins():
        spec = local_loader.get_hmdms_lambda_spec(plugin_name)
        if spec:
            hmdms_specs[spec["function_name"]] = spec

    floci_lambdas: Dict[str, Dict] = {}
    try:
        from .floci_deployer import _get_client, env_target

        client = _get_client("lambda", env_target(env) if env else None)
        for fn in client.list_functions().get("Functions", []):
            floci_lambdas[fn["FunctionName"]] = {
                "arn": fn.get("FunctionArn"),
                "image_uri": fn.get("Code", {}).get("ImageUri"),
            }
    except Exception as e:
        logger.debug(f"Could not list Floci Lambdas: {e}")

    deployments: List[Dict] = []
    base_url = "http://localhost/hmd_ms_deployment"
    try:
        resp = requests.get(
            f"{base_url}/api/hmd_lang_deployment.repo_instance_deployment",
            timeout=10,
        )
        if resp.status_code == 200:
            body = resp.json()
            deployments = body if isinstance(body, list) else body.get("items", [body])
    except requests.RequestException as e:
        logger.debug(f"Could not query ms-deployment: {e}")

    services: List[Dict] = []
    for fn_name, spec in hmdms_specs.items():
        floci = floci_lambdas.get(fn_name, {})
        matching_deployments = [
            d
            for d in deployments
            if isinstance(d.get("instance_configuration"), dict)
            and d["instance_configuration"].get("repo_class_name")
            == spec["repo_class_name"]
        ]
        status = (
            matching_deployments[0].get("status") if matching_deployments else "UNKNOWN"
        )
        services.append(
            {
                "plugin": spec["plugin_name"],
                "repo_class_name": spec["repo_class_name"],
                "lambda_name": fn_name,
                "image": spec["image"],
                "lambda_arn": floci.get("arn"),
                "lambda_image": floci.get("image_uri"),
                "buckets": [
                    b.get("name")
                    for b in spec.get("buckets", [])
                    if isinstance(b, dict)
                ],
                "status": status,
                "url": f"http://localhost/{fn_name}/",
            }
        )

    mocked: List[Dict] = []
    for d in deployments:
        cfg = d.get("instance_configuration") or {}
        if isinstance(cfg, dict) and cfg.get("mocked"):
            mocked.append(
                {
                    "repo_class_name": cfg.get("repo_class_name"),
                    "for": cfg.get("for"),
                    "role": cfg.get("role"),
                    "status": d.get("status"),
                }
            )

    output = {
        "mode": _resolve_mode(),
        "hmdms_services": services,
        "mocked_dependencies": mocked,
    }

    if json_mode:
        print(_json.dumps(output, indent=2))
        return

    print(f"\nMode: {output['mode']}\n")
    if services:
        print("HMDMS services:")
        for svc in services:
            print(f"  - {svc['repo_class_name']}  " f"[{svc['status']}]  {svc['url']}")
            print(
                f"      lambda={svc['lambda_name']}  "
                f"image={svc['image']}  "
                f"buckets={svc['buckets']}"
            )
            if svc.get("lambda_arn"):
                print(f"      arn={svc['lambda_arn']}")
        print()
    else:
        print("No HMDMS services registered.\n")

    if mocked:
        print("Mocked dependencies (status=SKIPPED):")
        for m in mocked:
            print(f"  - {m['repo_class_name']}  (for {m['for']}, role={m['role']})")
        print()


def update_images(compose_files: Optional[List[str]] = None):
    """Pull the latest images for the given compose files.

    :param compose_files: Explicit compose files to pull for (Extend mode
        passes its own control-plane/environment compose files). Defaults to
        scanning ``$HMD_HOME/.cache`` for Platform mode's per-plugin compose
        files, which is where that legacy mode copies them.
    """
    load_hmd_env()
    home_projects_path = _hmd_home / "studio" / "projects"
    hmd_repo_home = os.environ.get("HMD_REPO_HOME")

    if os.environ.get("HMD_PROJECTS_PATH") is None:
        os.environ["HMD_PROJECTS_PATH"] = (
            str(home_projects_path)
            if os.path.exists(home_projects_path)
            else hmd_repo_home
        )
    assert (
        os.environ.get("HMD_PROJECTS_PATH") is not None
    ), "Cannot find path to NeuronSphere Projects. Please set the HMD_REPO_HOME environment variable to location of Neuronsphere Projects with hmd configure set-env."

    if compose_files is None:
        compose_files = _get_cached_compose_files()
    command = [*_get_base_command(compose_files), "--verbose", "pull"]
    _exec(command)


def pull_artifact(
    repo_name: str,
    version: str,
    cloud_customer: Optional[str] = None,
    cloud_region: Optional[str] = None,
    cloud_url: Optional[str] = None,
    artifact_type: str = "build",
    local_url: str = "http://localhost/hmd_ms_artifact_lib/",
) -> None:
    """Copy an artifact from a cloud artifact librarian to the local one.

    Uses the same library `hmd build` uses (`hmd_lib_librarian_client`) so the
    cloud-vs-local routing is transparent. The function temporarily flips
    HMD_ARTIFACT_LIBRARIAN_URL between the two endpoints.
    """
    import tempfile

    from hmd_lib_librarian_client.artifact_tools import (
        get_artifact_librarian_client,
        ARTIFACT_NAME_TEMPLATE,
    )

    # urljoin strips the last path segment when the base lacks a trailing
    # slash (e.g. `urljoin("http://x/svc", "apiop/put")` → `http://x/apiop/put`),
    # which makes nginx's `/svc/` location block miss. Normalize defensively.
    if local_url and not local_url.endswith("/"):
        local_url = local_url + "/"

    artifact_name = ARTIFACT_NAME_TEMPLATE.format(
        name=repo_name, version=version, type=artifact_type
    )
    content_path = f"repository:/{repo_name}/{version}/{artifact_name}"

    saved_url = os.environ.get("HMD_ARTIFACT_LIBRARIAN_URL")
    saved_key = os.environ.get("HMD_ARTIFACT_LIBRARIAN_API_KEY")

    try:
        # 1) Pull from cloud
        if cloud_url:
            os.environ["HMD_ARTIFACT_LIBRARIAN_URL"] = cloud_url
        elif "HMD_ARTIFACT_LIBRARIAN_URL" in os.environ:
            del os.environ["HMD_ARTIFACT_LIBRARIAN_URL"]

        cloud_client = get_artifact_librarian_client(
            hmd_customer_code=cloud_customer or os.environ.get("HMD_CUSTOMER_CODE", ""),
            hmd_region=cloud_region or os.environ.get("HMD_REGION", ""),
        )

        with tempfile.NamedTemporaryFile(suffix=".zip", delete=False) as tmp:
            tmp_path = tmp.name

        print(f"Downloading {content_path} from cloud librarian...")
        cloud_client.get_file(
            content_path=content_path, file_name=tmp_path, force_overwrite=True
        )

        # 2) Push to local
        os.environ["HMD_ARTIFACT_LIBRARIAN_URL"] = local_url
        # Local librarian does not require a real API key; allow get_auth_token to no-op.
        os.environ.setdefault("HMD_ARTIFACT_LIBRARIAN_API_KEY", "local-dummy")

        local_client = get_artifact_librarian_client(
            hmd_customer_code=os.environ.get("HMD_CUSTOMER_CODE", "local"),
            hmd_region=os.environ.get("HMD_REGION", "reg1"),
        )
        print(f"Uploading {content_path} to {local_url}...")
        local_client.put_file(
            content_path=content_path,
            file_name=tmp_path,
            content_item_type=artifact_type,
        )
        print(
            f"Pulled {repo_name}:{version} ({artifact_type}) into local artifact librarian"
        )
    finally:
        # Restore prior env state
        if saved_url is not None:
            os.environ["HMD_ARTIFACT_LIBRARIAN_URL"] = saved_url
        elif "HMD_ARTIFACT_LIBRARIAN_URL" in os.environ:
            del os.environ["HMD_ARTIFACT_LIBRARIAN_URL"]
        if saved_key is not None:
            os.environ["HMD_ARTIFACT_LIBRARIAN_API_KEY"] = saved_key


def _resolve_repo_and_version(
    repo_path: Optional[str],
    repo: Optional[str],
    version: Optional[str],
):
    resolved_path = Path(repo_path or os.getcwd())
    if not repo:
        manifest_path = resolved_path / "meta-data" / "manifest.json"
        if not manifest_path.exists():
            raise Exception(f"--repo not given and {manifest_path} not found")
        with open(manifest_path) as f:
            manifest = json.load(f)
        repo = manifest.get("name")
        if not repo:
            raise Exception(f"--repo not given and 'name' missing from {manifest_path}")
    if not version:
        version_file = resolved_path / "meta-data" / "VERSION"
        if not version_file.exists():
            raise Exception(f"--version not given and {version_file} not found")
        version = version_file.read_text().strip()
    return resolved_path, repo, version


def push_artifact(
    repo: Optional[str] = None,
    version: Optional[str] = None,
    repo_path: Optional[str] = None,
    build_path: Optional[str] = None,
    artifact_type: str = "build",
    local_url: str = "http://localhost/hmd_ms_artifact_lib/",
) -> None:
    """Register a local repo's build artifact in the local artifact librarian.

    Two modes:

    * Auto-build (default): runs ``hmd build`` in ``repo_path`` with
      ``HMD_BUILD_OUTPUT_DIR`` pointing at a CLI-managed temp dir, then uploads
      the resulting zip via ``hmd_lib_librarian_client``. Does **not** set
      ``HMD_AUTO_PUBLISH``, so no Docker/PyPI publishing occurs.
    * Pre-built: if ``build_path`` is given, zips that directory directly via
      ``zip_and_archive`` instead of running a build.

    ``repo`` defaults to ``manifest.json``'s ``name``; ``version`` defaults to
    ``meta-data/VERSION``. Either may be overridden to register the artifact
    under any tag.
    """
    from hmd_lib_librarian_client.artifact_tools import (
        zip_and_archive,
        get_artifact_librarian_client,
        ARTIFACT_NAME_TEMPLATE,
    )

    # urljoin strips the last path segment when the base lacks a trailing
    # slash (e.g. `urljoin("http://x/svc", "apiop/put")` → `http://x/apiop/put`),
    # which makes nginx's `/svc/` location block miss. Normalize defensively.
    if local_url and not local_url.endswith("/"):
        local_url = local_url + "/"

    resolved_path, repo, version = _resolve_repo_and_version(repo_path, repo, version)
    artifact_name = ARTIFACT_NAME_TEMPLATE.format(
        name=repo, version=version, type=artifact_type
    )
    content_path = f"repository:/{repo}/{version}/{artifact_name}"

    saved_url = os.environ.get("HMD_ARTIFACT_LIBRARIAN_URL")
    saved_key = os.environ.get("HMD_ARTIFACT_LIBRARIAN_API_KEY")
    try:
        os.environ["HMD_ARTIFACT_LIBRARIAN_URL"] = local_url
        os.environ.setdefault("HMD_ARTIFACT_LIBRARIAN_API_KEY", "local-dummy")
        customer = os.environ.get("HMD_CUSTOMER_CODE", "local")
        region = os.environ.get("HMD_REGION", "reg1")

        if build_path is not None:
            print(f"Uploading {build_path} -> {content_path} at {local_url}")
            zip_and_archive(customer, region, content_path, artifact_type, build_path)
        else:
            with tempfile.TemporaryDirectory() as out_dir:
                build_env = {
                    **os.environ,
                    "HMD_BUILD_OUTPUT_DIR": out_dir,
                    "HMD_REPO_NAME": repo,
                    "HMD_REPO_VERSION": version,
                }
                # Strip HMD_AUTO_PUBLISH so hmd build only writes the zip and
                # does not push Docker/PyPI artifacts to JFrog/PyPI.
                build_env.pop("HMD_AUTO_PUBLISH", None)
                print(f"Running hmd build in {resolved_path}...")
                subprocess.run(
                    ["hmd", "build"],
                    cwd=str(resolved_path),
                    env=build_env,
                    check=True,
                )
                zip_path = Path(out_dir) / f"{repo}-{version}-build" / artifact_name
                if not zip_path.exists():
                    raise Exception(
                        f"hmd build did not produce expected zip at {zip_path}"
                    )
                print(f"Uploading {zip_path} -> {content_path} at {local_url}")
                client = get_artifact_librarian_client(
                    hmd_customer_code=customer, hmd_region=region
                )
                client.put_file(
                    content_path=content_path,
                    file_name=str(zip_path),
                    content_item_type=artifact_type,
                )

        print(
            f"Registered {repo}:{version} ({artifact_type}) in local artifact librarian"
        )
    finally:
        if saved_url is not None:
            os.environ["HMD_ARTIFACT_LIBRARIAN_URL"] = saved_url
        elif "HMD_ARTIFACT_LIBRARIAN_URL" in os.environ:
            del os.environ["HMD_ARTIFACT_LIBRARIAN_URL"]
        if saved_key is not None:
            os.environ["HMD_ARTIFACT_LIBRARIAN_API_KEY"] = saved_key
        elif "HMD_ARTIFACT_LIBRARIAN_API_KEY" in os.environ:
            del os.environ["HMD_ARTIFACT_LIBRARIAN_API_KEY"]
