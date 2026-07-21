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


def _get_base_command(files: List[str], quiet: bool = False):
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
        _project_name,
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


def _wait_for_hmd_db(timeout: int = 120) -> bool:
    """Wait until the `hmd_db` PostgreSQL container is accepting connections.

    The dbaccount Lambda provisions per-service databases by connecting to
    `hmd_db` over the Docker network. If provisioning runs before postgres is
    up (and registered in Docker DNS), the Lambda fails with a name-resolution
    or connection error and the service databases are never created. Polling
    `pg_isready` here removes that race (previously masked incidentally by the
    now-removed workload-Floci wait).

    :returns: True once ready, False if the timeout elapses.
    """
    start = time.time()
    while time.time() - start < timeout:
        result = subprocess.run(
            ["docker", "exec", "hmd_db", "pg_isready", "-U", "postgres"],
            capture_output=True,
            text=True,
        )
        if result.returncode == 0:
            return True
        time.sleep(2)
    logger.warning(f"hmd_db not ready after {timeout}s")
    return False


def _reload_nginx() -> None:
    """Signal the hmd_proxy nginx to reload its config."""
    _exec(
        ["docker", "exec", "hmd_proxy", "nginx", "-s", "reload"],
        capture=True,
        quiet=True,
    )


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


def _read_repo_version(repo_home: str, repo_name: str) -> str:
    """Read VERSION from a repo's meta-data, falling back to 'stable'."""
    if repo_home:
        version_path = os.path.join(repo_home, repo_name, "meta-data", "VERSION")
        try:
            with open(version_path) as f:
                return f.read().strip()
        except FileNotFoundError:
            pass
    return "stable"


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
            logger.info(f"Local plugin overriding installed: {plugin_name}")
        else:
            logger.info(f"Loading local plugin: {plugin_name}")
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
        logger.info(f"Rewrote Floci-provided depends_on in {compose_path}")


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

            logger.info(f"Extracted {svc_name} from {path} for Floci Lambda deployment")

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


def _deploy_hmdms_service_lambdas(
    api_id: str,
    local_loader: LocalPluginLoader,
    plugin_filter: Optional[List[str]] = None,
    skip_function_names: Optional[set] = None,
) -> List[Dict]:
    """Deploy each enabled HMDMS-service plugin as a Floci Lambda.

    Returns a list of deployed spec dicts (from get_hmdms_lambda_spec) suitable
    for ms-deployment seeding. Skips plugins whose image is not cached locally,
    logging a clear warning so the user can run `hmd build` and retry.

    `plugin_filter`: when set, only deploy plugins whose name is in this list.
    Used to bring up dbaccount alone before any DBs exist (two-phase up).

    `skip_function_names`: when set, skip plugins whose function_name is already
    in this set. Used by the post-DB-provisioning pass to avoid re-deploying
    dbaccount.
    """
    from .floci_deployer import (
        resolve_image_uri,
        setup_service,
        build_gozer_rds_secrets,
    )

    deployed: List[Dict] = []
    seen_function_names: set = set(skip_function_names or [])

    for plugin_name in local_loader.get_enabled_plugins():
        if plugin_filter is not None and plugin_name not in plugin_filter:
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
            print(
                f"  Warning: image for {repo_name}:{repo_version} not cached. "
                f"Run `hmd build` in the source repo first.{hint}"
            )
            continue

        env_vars = dict(spec["env_vars"])
        if plugin_name == "gozer":
            env_vars["RDS_SECRETS"] = json.dumps(build_gozer_rds_secrets(local_loader))
            env_vars["NEPTUNE_ENDPOINTS"] = json.dumps({"global-graph": "global-graph"})
            env_vars.setdefault("LIBRARIAN_DYNAMO_TABLES", "{}")
            env_vars.setdefault("S3_BUCKETS", "{}")
            env_vars.setdefault("DYNAMO_TABLE_NAMES", "[]")

        spec["image"] = image
        spec["env_vars"] = env_vars
        svc_api_id = setup_service(function_name, image, env_vars, api_id=api_id)
        spec["api_id"] = svc_api_id
        deployed.append(spec)
        logger.info(f"Deployed HMDMS service Lambda: {function_name} ({image})")

    return deployed


def _deploy_ms_deployment_lambda(api_id: str) -> str:
    """Deploy ms-deployment as a Floci Lambda; return its function ARN.

    Used by both Platform and Extend modes so ms-deployment is the single
    source of truth for "what's deployed locally".
    """
    from .floci_deployer import resolve_image_uri, setup_service

    repo_home = os.environ.get("HMD_REPO_HOME", "")
    deployment_version = os.environ.get(
        "HMD_MS_DEPLOYMENT_VERSION",
        _read_repo_version(repo_home, "hmd-ms-deployment"),
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

    logger.info(f"Seeding {len(profiles)} telemetry profile(s) into telemetry-debug...")

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
            logger.info(f"Telemetry profile seeding complete: {result}")
        else:
            logger.warning(
                f"Telemetry profile seeding failed: "
                f"HTTP {resp.status_code} - {resp.text}"
            )
    except requests.RequestException as e:
        logger.warning(f"Telemetry profile seeding failed: {e}")


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
    """Return True if ms-deployment already has a COMPLETED ChangeSetDeployment.

    Authoritative signal that a prior `up` fully bootstrapped the deployment
    graph. Any connection/parse error (e.g. the service isn't deployed yet)
    is treated as "not bootstrapped" so the caller falls back to a full
    bootstrap, which is the safe default.
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


def _is_already_bootstrapped(base_url: str) -> bool:
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
):
    # Verify the host can resolve `neuronsphere`/`neuronsphere-workload` to
    # loopback before doing anything else. Without this, presigned URLs
    # returned by in-network services would be unreachable from the host
    # and the user would hit confusing DNS errors deep in the build/publish
    # flow rather than a clear setup instruction up front.
    from .floci_deployer import ensure_neuronsphere_hosts_entry

    ensure_neuronsphere_hosts_entry()

    mode = _resolve_mode()
    if mode == "extend":
        start_neuronsphere_extend(verbose=verbose, upgrade=upgrade)
    else:
        start_neuronsphere_platform(
            config_overrides=config_overrides, verbose=verbose, upgrade=upgrade
        )


def start_neuronsphere_extend(verbose: bool = False, upgrade: bool = False):
    """Start NeuronSphere in extend mode (admin control plane with DAG-based deployment).

    Starts the admin control plane (Floci, PostgreSQL, nginx), deploys
    ms-deployment and ms-naming as Lambda functions behind Floci's API Gateway,
    and starts any compose_substitute overrides (e.g., JanusGraph for Neptune).
    """
    from .floci_deployer import (
        wait_for_floci,
        setup_service,
        resolve_image_uri,
        write_nginx_config,
        create_api_gateway,
        deploy_api_gateway,
        ensure_k3s_cluster,
        wait_for_k3s_ready,
        write_kubeconfig,
        K3S_CLUSTER_NAME,
    )

    load_hmd_env()
    print_header("Starting")
    print_step("Extend mode — admin control plane")

    # --upgrade: repull the compose-service images first (docker only fetches
    # changed layers) so the restart runs on the latest images. Non-fatal — a
    # fresh env without cached compose files falls through to a normal start.
    if upgrade:
        print_step("Upgrade — pulling latest images...")
        try:
            update_images()
        except Exception as e:
            logger.warning(f"Image pull failed (non-fatal): {e}")
            print(f"  Warning: image pull failed: {e}")

    # Build compose file list
    services_dir = Path(__file__).parent / "services"
    admin_compose = str(services_dir / "docker-compose.admin.yml")
    compose_files = [admin_compose]

    # Collect compose_substitute overrides from LocalPluginLoader
    local_loader = LocalPluginLoader()
    # dbaccount is foundational for cloud-parity DB provisioning — auto-load
    # it from HMD_REPO_HOME if the user hasn't listed it in HMD_LOCAL_PLUGINS.
    local_loader.ensure_foundation_plugin("dbaccount", "hmd-ms-dbaccount")
    # artifact-lib backs the `hmd neuronsphere push-artifact` / `pull-artifact`
    # CLI commands, so it must always be available locally.
    local_loader.ensure_foundation_plugin("artifact-lib", "hmd-ms-artifact-lib")
    for plugin_name in local_loader.get_enabled_plugins():
        config = local_loader.get_plugin_config(plugin_name)
        if (
            config
            and config.get("local_deploy", {}).get("strategy") == "compose_substitute"
        ):
            compose_path = local_loader.get_compose_path(plugin_name)
            if compose_path and str(compose_path) not in compose_files:
                compose_files.append(str(compose_path))
                print_step(f"  compose_substitute (plugin): {plugin_name}")

    # Graph (Neptune/JanusGraph) is part of the local core: the minimal default
    # is network + Floci + core DBs + k3s + deployment control plane + graph.
    # Every other app/infra service is opt-in. Users can drop graph with
    # HMD_LOCAL_NEURONSPHERE_ENABLE_GRAPH=false.
    if os.environ.get("HMD_LOCAL_NEURONSPHERE_ENABLE_GRAPH", "true").lower() not in (
        "false",
        "0",
        "no",
    ):
        graph_compose = services_dir / "docker-compose.graph.yml"
        if graph_compose.exists() and str(graph_compose) not in compose_files:
            compose_files.append(str(graph_compose))
            os.makedirs(_hmd_home / "graph_db", exist_ok=True)
            print_step("  core: graph (JanusGraph/Neptune)")

    # Ensure cache directories and Floci data dirs
    cache_dir = _hmd_home / ".cache"
    os.makedirs(cache_dir / "nginx", exist_ok=True)
    os.makedirs(_hmd_home / "floci" / "data", exist_ok=True)

    # Write a placeholder nginx config (will be rewritten after Lambda deployment)
    placeholder_nginx = cache_dir / "nginx" / "neuronsphere.conf"
    if not placeholder_nginx.exists():
        placeholder_nginx.write_text(
            "events {}\nhttp {\n  server {\n    listen 80;\n"
            "    location / { return 503 'starting...'; }\n  }\n}\n"
        )

    print_step("Validating ports...")
    validate_ports(compose_files)

    # Create the (per-HMD_HOME-scoped) Docker network
    _exec(
        ["docker", "network", "create", DOCKER_NETWORK_NAME],
        capture=True,
        quiet=not verbose,
    )

    # Phase 1: bring up only foundation services (db, floci, proxy).
    # Lambda-deployed services (ms-naming, ms-deployment, HMDMS plugins) are
    # provisioned after dbaccount creates their DBs.
    print_step("Starting foundation containers...")
    quiet = not verbose
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

    # Wait for the single Floci instance to be healthy
    print_step("Waiting for Floci...")
    try:
        wait_for_floci()
    except RuntimeError as e:
        logger.warning(f"{e} — skipping Lambda deployment")
        print(f"  Warning: {e}")
        print_header("Ready (degraded)")
        return

    # Wait for PostgreSQL before provisioning: the dbaccount Lambda creates the
    # per-service databases by connecting to `hmd_db`, so it must be up and
    # registered in Docker DNS first (otherwise DB provisioning races and the
    # service databases are never created).
    print_step("Waiting for PostgreSQL (hmd_db)...")
    _wait_for_hmd_db()

    # Aggregate HMDMS-service buckets into Floci resources and provision
    from .floci_deployer import (
        provision_resources,
        provision_plugin_databases,
        ensure_core_databases_direct,
    )

    extend_resources: Dict[str, List] = {"s3_buckets": []}
    _aggregate_hmdms_resources(local_loader, extend_resources)
    print_step("Provisioning Floci resources...")
    provision_resources(extend_resources, local_loader=local_loader)

    # Deploy dbaccount alone first so it can provision ms-naming, ms-deployment,
    # and every HMDMS plugin's DB before those Lambdas start.
    service_api_ids: Dict[str, str] = {}
    dbaccount_deployed = _deploy_hmdms_service_lambdas(
        None, local_loader, plugin_filter=["dbaccount"]
    )
    if dbaccount_deployed:
        for spec in dbaccount_deployed:
            service_api_ids[spec["function_name"]] = spec["api_id"]
        print_step("Deploying API Gateway stage for dbaccount...")
        deploy_api_gateway(dbaccount_deployed[0]["api_id"])
        write_nginx_config(service_api_ids, placeholder_nginx)
        _exec(
            ["docker", "exec", "hmd_proxy", "nginx", "-s", "reload"],
            capture=True,
            quiet=True,
        )

        print_step("Provisioning databases via ms-dbaccount...")
        provision_plugin_databases(local_loader)
    else:
        logger.warning(
            "dbaccount Lambda not deployed (image missing or plugin not enabled); "
            "skipping DB provisioning. Lambdas may fail to connect to their DBs."
        )

    # Guarantee the foundational control-plane databases exist even if the
    # dbaccount/Floci path was flaky (the ms-naming and ms-deployment Lambdas
    # can't start without them). Deterministic, idempotent, no API dependency.
    print_step("Ensuring core control-plane databases...")
    ensure_core_databases_direct()

    # Phase 2: bring up any compose_substitute application containers now
    # that DBs exist.
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

    # Create the k3s cluster on Floci EKS (shared with platform mode behavior)
    k3s_cluster_name = None
    k3s_uid = None
    if os.environ.get("HMD_LOCAL_NEURONSPHERE_ENABLE_K3S", "true").lower() not in (
        "false",
        "0",
        "no",
    ):
        print_step(f"Creating k3s cluster '{K3S_CLUSTER_NAME}'...")
        try:
            ensure_k3s_cluster()
            wait_for_k3s_ready()
            k3s_cluster_name = K3S_CLUSTER_NAME
            kubeconfig_path = write_kubeconfig()
            os.environ["KUBECONFIG"] = str(kubeconfig_path)
            print_step(f"  k3s ready (kubeconfig: {kubeconfig_path})")
            print_step("Installing cluster operators onto k3s...")
            from .k3s_operators import cluster_incarnation_id, provision_k3s_operators

            provision_k3s_operators()
            # Fingerprint this cluster incarnation so the restart fast-path below
            # can tell a genuinely-reused cluster apart from one Floci silently
            # recreated (e.g. the stale-image recovery in ensure_k3s_cluster) --
            # a recreated cluster has nothing deployed on it even though
            # ms-deployment's persisted graph still says otherwise.
            k3s_uid = cluster_incarnation_id()
        except Exception as e:
            logger.warning(f"k3s cluster creation failed: {e}")
            print(
                f"  Warning: k3s unavailable — Argo and k3s-dependent plugins will be skipped: {e}"
            )

    # API Gateways are created per-service inside setup_service. Floci's
    # matcher treats every `{proxy+}` resource as a global wildcard, so a
    # shared gateway would have every service catching every other
    # service's traffic.

    # Deploy ms-deployment as Lambda function behind API Gateway
    print_step("Deploying ms-deployment Lambda...")
    repo_home = os.environ.get("HMD_REPO_HOME", "")
    naming_version = os.environ.get(
        "HMD_MS_NAMING_VERSION",
        _read_repo_version(repo_home, "hmd-ms-naming"),
    )

    ms_deployment_available = False
    try:
        service_api_ids["hmd_ms_deployment"] = _deploy_ms_deployment_lambda(None)
        ms_deployment_available = True
    except Exception as e:
        logger.warning(f"ms-deployment Lambda deploy failed: {e}")
        print(f"  Warning: ms-deployment not available: {e}")

    # Deploy ms-naming as Lambda function behind API Gateway
    print_step("Deploying ms-naming Lambda...")
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

    naming_image = resolve_image_uri("hmd-ms-naming", naming_version)
    if naming_image is None:
        raise RuntimeError(
            f"hmd-ms-naming image for version {naming_version} not cached "
            f"locally. Run `hmd build` in hmd-ms-naming and retry."
        )
    service_api_ids["hmd_ms_naming"] = setup_service(
        "hmd_ms_naming",
        naming_image,
        naming_env,
    )

    # Deploy remaining HMDMS-service plugins as Floci Lambdas, skipping
    # dbaccount (already deployed before DB provisioning).
    already = {s["function_name"] for s in dbaccount_deployed}
    hmdms_deployed = _deploy_hmdms_service_lambdas(
        None, local_loader, skip_function_names=already
    )
    hmdms_deployed = list(dbaccount_deployed) + list(hmdms_deployed)
    for spec in hmdms_deployed:
        service_api_ids[spec["function_name"]] = spec["api_id"]

    # Deploy each per-service API Gateway stage
    print_step("Deploying API Gateway stages...")
    for svc_api_id in set(service_api_ids.values()):
        deploy_api_gateway(svc_api_id)

    print_step("Configuring API Gateway proxy...")
    write_nginx_config(service_api_ids, placeholder_nginx)
    # Reload-and-verify: confirm the ms-deployment route is actually served
    # before seeding (an initial reload occasionally doesn't take effect).
    if ms_deployment_available:
        _ensure_nginx_routed(
            "http://localhost/hmd_ms_deployment/api/hmd_lang_deployment.environment"
        )
    else:
        _reload_nginx()

    if ms_deployment_available:
        ms_deployment_url = "http://localhost/hmd_ms_deployment"
        # Poll ms-deployment readiness through the proxy
        print_step("Waiting for ms-deployment...")
        _wait_for_ms_deployment(ms_deployment_url)

        # Restart fast-path: if a prior `up` already bootstrapped the deployment
        # graph, the Floci + PostgreSQL persistent state still holds every
        # RepoInstanceDeployment. Re-seeding the BOM would create duplicate
        # changesets, and re-running the DAG would redeploy everything to
        # Floci/k3s again — exactly what a restart must avoid. The Lambda/proxy
        # wiring above already re-ran (idempotent), so the control plane is live.
        if _is_already_bootstrapped(ms_deployment_url):
            marker = _read_bootstrap_marker() or {}
            # A recreated k3s cluster (see cluster_incarnation_id's docstring) has
            # nothing deployed on it, even though ms-deployment's persisted graph
            # still says every RepoInstanceDeployment succeeded -- the "skip BOM
            # seeding and DAG execution" fast-path below would otherwise leave the
            # new cluster permanently empty. Only trust a mismatch when both UIDs
            # are known, so a marker written before this feature existed (no
            # recorded k3s_uid yet) doesn't force a surprise redeploy.
            cluster_recreated = bool(
                k3s_uid and marker.get("k3s_uid") and marker["k3s_uid"] != k3s_uid
            )
            print_step(
                "Already bootstrapped — restarting existing deployment "
                "(skipping BOM seeding and DAG execution)."
            )
            # Idempotently re-sync the core Resources against the existing
            # `local-neuronsphere` deployment so a restart picks up any newly-defined core
            # Resource (e.g. a new ingress-controller) without a destructive
            # re-bootstrap. seed_base_defs / declare_produces / submit_resources all
            # upsert/dedupe server-side, so this is safe on every `up`. Non-fatal.
            print_step("Resyncing local core resources...")
            try:
                from .bom_seeder import resync_local_resources

                service_specs = [
                    {
                        "service_name": s.get("function_name"),
                        "repo_class_name": s.get("repo_class_name"),
                        "api_base_url": f"http://localhost/{s.get('function_name')}",
                    }
                    for s in (hmdms_deployed or [])
                    if s.get("function_name")
                ]
                count = resync_local_resources(
                    ms_deployment_url, k3s_cluster_name, services=service_specs
                )
                print_step(f"  {count} core resource(s) resynced")
            except Exception as e:
                logger.warning(f"Local resource resync failed (non-fatal): {e}")
                print(f"  Warning: local resource resync failed: {e}")

            if cluster_recreated:
                # The persisted deployment graph no longer matches reality: redeploy
                # the FULL resolved BOM (not just the delta) onto the new cluster,
                # same as a first bootstrap would. CDKTF/Floci-only instances are
                # unaffected by a k3s-only recreate, but re-applying them is a safe
                # no-op (state-based). Unlike the delta path below, this isn't gated
                # on --upgrade -- an empty cluster isn't an optional reconciliation.
                print_step(
                    "k3s cluster was recreated since the last bootstrap — the "
                    "persisted deployment graph no longer matches reality; "
                    "redeploying the full BOM onto the new cluster."
                )
                try:
                    from .bom_seeder import (
                        LOCAL_CORE_BOM,
                        build_local_core_resources,
                        resolve_plugin_bom,
                        seed_bom,
                        submit_local_resources,
                    )
                    from .local_workflow_runner import LocalWorkflowRunner

                    recreate_service_specs = [
                        {
                            "service_name": s.get("function_name"),
                            "repo_class_name": s.get("repo_class_name"),
                            "api_base_url": f"http://localhost/{s.get('function_name')}",
                        }
                        for s in (hmdms_deployed or [])
                        if s.get("function_name")
                    ] + [
                        {
                            "service_name": "hmd_ms_deployment",
                            "repo_class_name": "hmd-ms-deployment",
                            "api_base_url": "http://localhost/hmd_ms_deployment",
                        },
                        {
                            "service_name": "hmd_ms_naming",
                            "repo_class_name": "hmd-ms-naming",
                            "api_base_url": "http://localhost/hmd_ms_naming",
                        },
                    ]

                    runner = LocalWorkflowRunner(
                        ms_deployment_url,
                        cluster_name=k3s_cluster_name,
                    )

                    # Phase A: re-visit the core instance and resubmit its
                    # Resources onto the recreated cluster (same two-phase
                    # sequence as first bootstrap -- see that branch for why).
                    csd_nid_a, nodes_a = seed_bom(
                        ms_deployment_url, bom=list(LOCAL_CORE_BOM)
                    )
                    local_resources = build_local_core_resources(
                        cluster_name=k3s_cluster_name,
                        services=recreate_service_specs,
                    )
                    submit_local_resources(ms_deployment_url, local_resources, nodes_a)
                    runner.run(csd_nid_a, nodes_a)

                    # Phase B: the rest (ext-secrets + LOCAL_BOM + plugins).
                    csd_nid, nodes = seed_bom(
                        ms_deployment_url, bom=resolve_plugin_bom()
                    )
                    print_step(f"  {len(nodes)} deployment node(s)")
                    if not runner.run(csd_nid, nodes):
                        print(
                            "\n  Warning: some deployments failed after cluster "
                            "recreation"
                        )
                    _write_bootstrap_marker(
                        csd_nid or marker.get("csd_nid", "control-plane"),
                        k3s_uid=k3s_uid,
                    )
                except Exception as e:
                    logger.warning(f"Post-recreation full redeploy failed: {e}")
                    print(f"  Warning: post-recreation full redeploy failed: {e}")
            else:
                # Non-destructively deploy any newly-available plugin-contributed BOM
                # entries (e.g. a plugin installed, or an HMD_LOCAL_NEURONSPHERE_ENABLE_*
                # flag flipped on, after this env was already bootstrapped). The delta
                # changeset carries ONLY the new instances, so apply_changeset/DAG
                # generation emit deploy scripts for those alone -- already-deployed
                # instances (core, ext-secrets, ...) are left untouched (they appear
                # in the DAG only as dependency-ordering context). Deploying is gated
                # on --upgrade (the flag that already means "reconcile to current
                # desired state"); a plain restart just prints a hint so it stays
                # fast.
                try:
                    from .bom_seeder import compute_new_bom_entries

                    new_entries = compute_new_bom_entries(ms_deployment_url)
                    if new_entries and upgrade:
                        names = ", ".join(e["repo_instance_name"] for e in new_entries)
                        print_step(
                            f"Found {len(new_entries)} new plugin resource(s) — "
                            f"deploying: {names}"
                        )
                        from .bom_seeder import seed_bom
                        from .local_workflow_runner import LocalWorkflowRunner

                        csd_nid, nodes = seed_bom(ms_deployment_url, bom=new_entries)
                        print_step(f"  {len(nodes)} deployment node(s)")
                        runner = LocalWorkflowRunner(
                            ms_deployment_url,
                            cluster_name=k3s_cluster_name,
                        )
                        if not runner.run(csd_nid, nodes):
                            print("\n  Warning: some new plugin deployments failed")
                    elif new_entries:
                        names = ", ".join(e["repo_instance_name"] for e in new_entries)
                        print_step(
                            f"{len(new_entries)} new plugin resource(s) detected "
                            f"({names}); run `hmd neuronsphere up --upgrade` to "
                            "deploy them."
                        )
                except Exception as e:
                    logger.warning(f"New-plugin delta deploy failed (non-fatal): {e}")
                    print(f"  Warning: new-plugin delta deploy failed: {e}")
                # Opportunistically record k3s_uid if the marker predates this
                # feature, so a future restart can detect recreation.
                if k3s_uid and not marker.get("k3s_uid"):
                    _write_bootstrap_marker(
                        marker.get("csd_nid", "control-plane"), k3s_uid=k3s_uid
                    )
        else:
            # hmd-ms-deployment/hmd-ms-naming/hmd-ms-dbaccount/hmd-ms-artifact-lib
            # are never registered as RepoClass/RepoInstance entities (their own
            # manifests' required deps -- base-vpc/argo/datadog-lambda/etc. -- will
            # never resolve locally). What they provide is represented instead as
            # `application/microservice` Resources on the core instance (Phase A,
            # below).

            # Seed the standard base ResourceDefinition catalog (NERD0004) so
            # per-repo concrete definitions that parent these supertypes resolve
            # cleanly. Idempotent and non-fatal.
            print_step("Seeding base resource definitions...")
            try:
                from .bom_seeder import seed_base_resource_definitions

                seeded = seed_base_resource_definitions(ms_deployment_url)
                print_step(f"  {len(seeded)} base resource definitions")
            except Exception as e:
                logger.warning(f"Base resource definition seeding failed: {e}")
                print(f"  Warning: base resource definition seeding failed: {e}")

            # Best-effort starter-BOM seeding + DAG execution, in two phases. This
            # is NOT required for the control plane to function (the deployment
            # tests seed their own data); it only pre-populates a starter graph.
            # Any failure here is non-fatal.
            from .bom_seeder import (
                LOCAL_CORE_BOM,
                build_local_core_resources,
                resolve_plugin_bom,
                seed_bom,
                submit_local_resources,
            )
            from .local_workflow_runner import LocalWorkflowRunner

            # microservice Resources for each seeded HMDMS service so a dev
            # `hmd deploy --local` config -- and any manifest resource-typed
            # dependency, e.g. hmd-database-account.create-service -- can bind
            # service-backed roles by tag.
            service_specs = [
                {
                    "service_name": s.get("function_name"),
                    "repo_class_name": s.get("repo_class_name"),
                    "api_base_url": f"http://localhost/{s.get('function_name')}",
                }
                for s in (hmdms_deployed or [])
                if s.get("function_name")
            ] + [
                {
                    "service_name": "hmd_ms_deployment",
                    "repo_class_name": "hmd-ms-deployment",
                    "api_base_url": "http://localhost/hmd_ms_deployment",
                },
                {
                    "service_name": "hmd_ms_naming",
                    "repo_class_name": "hmd-ms-naming",
                    "api_base_url": "http://localhost/hmd_ms_naming",
                },
            ]

            csd_nid = None
            nodes = []
            try:
                # Phase A: apply a changeset containing ONLY the core instance
                # (`local-neuronsphere`), then submit its concrete Resources (Docker
                # network, k3s cluster + compute pool, shared Postgres, JanusGraph,
                # and the HMDMS microservice Resources above). This must run BEFORE
                # Phase B's changeset: a repo that depends on one of these
                # Resources (e.g. ext-secrets on the kubernetes-cluster, or
                # hmd-database-account on the microservice type via tag_selector)
                # needs the concrete Resource to already exist for selector
                # matching to find it (NERD0006).
                print_step("Seeding core control-plane changeset (Phase A)...")
                csd_nid_a, nodes_a = seed_bom(
                    ms_deployment_url, bom=list(LOCAL_CORE_BOM)
                )
                print_step(f"  {len(nodes_a)} deployment node(s)")

                print_step("Submitting local core resources...")
                try:
                    local_resources = build_local_core_resources(
                        cluster_name=k3s_cluster_name,
                        services=service_specs,
                    )
                    count = submit_local_resources(
                        ms_deployment_url, local_resources, nodes_a
                    )
                    print_step(f"  {count} local resource(s) submitted")
                except Exception as e:
                    logger.warning(f"Local resource submission failed (non-fatal): {e}")
                    print(f"  Warning: local resource submission failed: {e}")

                # Phase A must still run through the local runner -- even though
                # its one node (CORE_REPO_CLASS) is a hardcoded no-op -- so the RID
                # transitions DEPLOY_NEXT -> DEPLOYED (see LocalWorkflowRunner.run).
                # Skipping this would leave `local-neuronsphere` permanently
                # DEPLOY_NEXT, breaking the restart fast-path.
                runner = LocalWorkflowRunner(
                    ms_deployment_url,
                    cluster_name=k3s_cluster_name,
                )
                runner.run(csd_nid_a, nodes_a)

                # Phase B: apply a second changeset with everything else
                # (ext-secrets + LOCAL_BOM + plugin-contributed entries). Entries
                # here that reference the core instance by name (e.g. ext-secrets'
                # eks-cluster/compute roles) resolve fine -- it already exists in
                # the "local" environment from Phase A.
                bom_file = os.environ.get("HMD_LOCAL_BOM_FILE")
                if bom_file:
                    print_step(f"Seeding deployment graph from {bom_file} (Phase B)...")
                else:
                    print_step("Seeding deployment graph (built-in BOM, Phase B)...")

                csd_nid, nodes = seed_bom(ms_deployment_url, bom=resolve_plugin_bom())
                print_step(f"  {len(nodes)} deployment node(s)")

                # Execute Phase B's deployment DAG locally
                print_step("Running local deployments...")
                if not runner.run(csd_nid, nodes):
                    print("\n  Warning: Some deployments failed")
            except Exception as e:
                logger.warning(f"Starter-BOM seeding failed (non-fatal): {e}")
                print(f"\n  Warning: starter-BOM seeding failed (non-fatal): {e}")

            # The control plane (Floci + DBs + ms-deployment/ms-naming Lambdas +
            # routing) is up. Record the bootstrap independent of the best-effort
            # starter BOM so subsequent `up`s take the restart fast-path and skip
            # the expensive Lambda/DAG setup above.
            _write_bootstrap_marker(csd_nid or "control-plane", k3s_uid=k3s_uid)
    else:
        print_step("Skipping ms-deployment readiness/seeding (Lambda not deployed)")

    print_header("Ready")
    print(f"\n  ms-deployment:    http://localhost/hmd_ms_deployment/")
    print(f"  ms-naming:        http://localhost/hmd_ms_naming/")
    print(f"  Floci:            http://localhost:4566")
    print(f"  PostgreSQL:       localhost:5432\n")


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
                        logger.info(
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
                logger.info(f"Plugin '{plugin}' runs on k3s; skipping compose service")
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
                    logger.info(f"Added local compose file: {cached}")

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
            logger.info(f"Generated db init container for plugin: {plugin_name}")

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
            provision_plugin_databases(local_loader)
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
    logger.info("Upserting local services to Naming Service...")
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

    logger.info("Updating database connections file...")
    conn_file_path = cache_dir / ".." / "connections.yml"

    conns = {"databases": {}}
    if conn_file_path.exists():
        with open(conn_file_path, "r") as c:
            conns = yaml.safe_load(c)

    for db in resources.get("databases", []):
        if not isinstance(db, dict):
            continue
        logger.info(f"Adding {db['database']}")
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


def stop_neuronsphere(verbose: bool = False, purge: bool = False):
    mode = _resolve_mode()
    if mode == "extend":
        stop_neuronsphere_extend(verbose=verbose, purge=purge)
    else:
        stop_neuronsphere_platform(verbose=verbose)


def _purge_persistent_state(verbose: bool = False) -> None:
    """Remove persisted Floci/PostgreSQL state and the bootstrap marker.

    Forces the next `up` to re-run the full bootstrap workflow. Called only
    for `down --purge`; a plain `down` preserves this state so restarts are
    fast.
    """
    print_step("Purging persistent state (Floci data, PostgreSQL data, marker)...")
    _clear_bootstrap_marker()
    for rel in ("floci/data", "postgresql/data"):
        target = _hmd_home / rel
        try:
            if target.exists():
                shutil.rmtree(target)
                if verbose:
                    print(f"  removed {target}")
        except OSError as e:
            logger.warning(f"Could not remove {target}: {e}")


def stop_neuronsphere_extend(verbose: bool = False, purge: bool = False):
    """Stop NeuronSphere in extend mode.

    Tears down the single-Floci control plane (Floci, PostgreSQL, nginx) and any
    compose_substitute services. Lambda functions are cleaned up when Floci stops.
    When ``purge`` is set, also removes the persisted Floci/PostgreSQL state and
    the bootstrap marker so the next ``up`` re-runs the full deployment workflow.
    """
    load_hmd_env()
    print_header("Stopping")

    # Reconstruct compose file list (same as start)
    services_dir = Path(__file__).parent / "services"
    admin_compose = str(services_dir / "docker-compose.admin.yml")
    compose_files = [admin_compose]

    # Graph (Neptune/JanusGraph) — same gating as start_neuronsphere_extend.
    if os.environ.get("HMD_LOCAL_NEURONSPHERE_ENABLE_GRAPH", "true").lower() not in (
        "false",
        "0",
        "no",
    ):
        graph_compose = services_dir / "docker-compose.graph.yml"
        if graph_compose.exists() and str(graph_compose) not in compose_files:
            compose_files.append(str(graph_compose))

    local_loader = LocalPluginLoader()
    for plugin_name in local_loader.get_enabled_plugins():
        config = local_loader.get_plugin_config(plugin_name)
        if (
            config
            and config.get("local_deploy", {}).get("strategy") == "compose_substitute"
        ):
            compose_path = local_loader.get_compose_path(plugin_name)
            if compose_path and str(compose_path) not in compose_files:
                compose_files.append(str(compose_path))

    # Best-effort: delete the k3s cluster while Floci is still up.
    # Floci will reap the k3s container on its own when it stops, but explicit
    # delete keeps Floci's state file consistent across restarts.
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
    _exec(
        ["docker", "network", "rm", DOCKER_NETWORK_NAME],
        capture=True,
        quiet=not verbose,
    )

    if purge:
        _purge_persistent_state(verbose=verbose)

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
            logger.info(f"Added local plugin compose file for down: {compose_path}")

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
            logger.info(f"Added local plugin compose file for restart: {compose_path}")

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

    local_svcs = os.listdir(_hmd_home / ".cache" / "local_services")
    port = f"{len(local_svcs)+2}5432"
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
            "ports": [f"{port}:5432"],
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


def print_status(json_mode: bool = False) -> None:
    """Print what's deployed in local NeuronSphere.

    Reads from ms-deployment (when reachable) and Floci's Lambda registry.
    Cross-references the local plugin map for image, bucket env vars, and
    plugin-source info. Output is human-readable by default; ``--json`` emits
    a single JSON document for the future Django app to consume.
    """
    import json as _json

    load_hmd_env()

    local_loader = LocalPluginLoader()
    hmdms_specs = {}
    for plugin_name in local_loader.get_enabled_plugins():
        spec = local_loader.get_hmdms_lambda_spec(plugin_name)
        if spec:
            hmdms_specs[spec["function_name"]] = spec

    floci_lambdas: Dict[str, Dict] = {}
    try:
        from .floci_deployer import _get_client

        client = _get_client("lambda")
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


def update_images():
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
