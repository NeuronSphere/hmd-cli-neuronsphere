import getpass
import json
import os
from pathlib import Path
import shutil
from typing import Dict, List
from importlib_metadata import entry_points
from importlib.util import find_spec

from cement.utils.shell import cmd
from dotenv import load_dotenv
from hmd_cli_tools import cd
from hmd_cli_tools.okta_tools import get_auth_token
from hmd_cli_tools.hmd_cli_tools import load_hmd_env
import yaml

from cement import App, minimal_logger, shell

logger = minimal_logger("hmd_cli_neuronsphere")

ENABLED_PLUGIN_ENTRY_POINT = "hmd_cli_neuronsphere.enabled"
PREPARE_PLUGIN_ENTRY_POINT = "hmd_cli_neuronsphere.prepare_hmd_home"
RESOURCES_PLUGIN_ENTRY_POINT = "hmd_cli_neuronsphere.get_resources"
COMPOSE_PLUGIN_ENTRY_POINT = "hmd_cli_neuronsphere.render_compose_yaml"


def _get_env_var(var_name, default=None):
    return os.environ.get(var_name, default)


def _get_required_env_var(var_name, default=None):
    value = os.environ.get(var_name, default)

    if value is None:
        raise Exception(f"Required environment variable, {var_name}, not set.")
    return value


def _exec(command, capture=False):
    _cmd = " ".join(list(map(str, command)))
    print(_cmd)
    return cmd(_cmd, capture=capture)


_hmd_home = Path(_get_required_env_var("HMD_HOME"))
_dirname = Path(os.path.dirname(__file__))
_services_dir = _dirname / "services"
_configs = dict()


def load_env():
    load_dotenv(_hmd_home / ".config" / "hmd.env", override=False)


def _get_tech_enabled(name, default=None):
    var = f"HMD_LOCAL_NEURONSPHERE_ENABLE_{name}"
    val = _get_env_var(var)
    if val is not None:
        return val == "true"
    return default


def _get_configs(config_overrides: Dict[str, bool] = {}):
    if not any(_configs):
        enable_trino = _get_tech_enabled("TRINO", config_overrides.get("trino", True))
        enable_dynamodb = _get_tech_enabled(
            "DYNAMODB", config_overrides.get("dynamo", True)
        )
        enable_graph = _get_tech_enabled("GRAPH", config_overrides.get("graph", True))
        enable_minio = _get_tech_enabled("MINIO", config_overrides.get("minio", True))
        enable_transform = _get_tech_enabled(
            "TRANSFORM", config_overrides.get("transform", True)
        )
        enable_apache_superset = _get_tech_enabled(
            "APACHE_SUPERSET", config_overrides.get("apache_superset", True)
        )

        _configs.update(
            {
                "base": {
                    "enabled": True,
                    "path": _services_dir / f"docker-compose.main.yml",
                },
                "jupyter": {
                    "enabled": config_overrides.get("jupyter", True),
                    "path": _services_dir / f"docker-compose.jupyter.yml",
                },
                "trino": {
                    "enabled": enable_trino,
                    "path": _services_dir / f"docker-compose.trino.yml",
                },
                "dynamodb": {
                    "enabled": enable_dynamodb,
                    "path": _services_dir / f"docker-compose.dynamodb.yml",
                },
                "graph": {
                    "enabled": enable_graph,
                    "path": _services_dir / "docker-compose.graph.yml",
                },
                "minio": {
                    "enabled": enable_minio,
                    "path": _services_dir / "docker-compose.minio.yml",
                },
                "apache-superset": {
                    "enabled": enable_apache_superset,
                    "path": _services_dir / f"docker-compose.apache-superset.yml",
                },
                "airflow": {
                    "enabled": enable_transform,
                    "path": _services_dir / f"docker-compose.airflow.yml",
                },
                "transform": {
                    "enabled": enable_transform,
                    "path": _services_dir / f"docker-compose.transform.yml",
                },
                "datadog": {
                    "enabled": os.environ.get("DD_API_KEY") is not None,
                    "path": _services_dir / "docker-compose.datadog.yml",
                },
            }
        )

    return _configs


def _get_base_command(files: List[str]):
    stdout, _, _ = _exec(
        ["pip", "config", "get", "global.extra-index-url"], capture=True
    )
    pip_url = stdout.decode("utf-8")
    os.environ["PIP_EXTRA_INDEX_URL"] = pip_url
    command = ["docker", "compose", "--project-directory", str(_hmd_home / ".cache")]
    for file_ in files:
        command += ["-f", str(file_)]
    return command


def _load_entry_point(name: str, group: str):
    entrypoints = entry_points(name=name, group=group)

    for entrypoint in entrypoints:
        if entrypoint.name == name:
            return entrypoint


def _load_plugins(config_overrides: Dict[str, bool] = {}):
    entrypoints = entry_points(group=ENABLED_PLUGIN_ENTRY_POINT)
    plugins = {}
    for entrypoint in entrypoints:
        logger.debug(f"Loading plugin...{entrypoint.name}")
        plugins[entrypoint.name] = entrypoint.load()(config_overrides)

    return plugins


def start_neuronsphere(config_overrides: Dict[str, bool] = {}):
    load_env()

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

    plugins = _load_plugins(config_overrides=config_overrides)
    resources = {"buckets": []}

    # Get Plugin Resources
    for plugin, enabled in plugins.items():
        if enabled:
            entrypoint = _load_entry_point(plugin, RESOURCES_PLUGIN_ENTRY_POINT)

            if entrypoint is not None:
                plugin_resources = entrypoint.load()()
            else:
                plugin_resources = {}

            for k, v in plugin_resources.items():
                if k in resources:
                    resources[k] = [*resources[k], *v]
                else:
                    resources[k] = v

    # Prepare HMD_HOME from plugins
    for plugin, enabled in plugins.items():
        if enabled:
            entrypoint = _load_entry_point(plugin, PREPARE_PLUGIN_ENTRY_POINT)
            if entrypoint is not None:
                entrypoint.load()(_hmd_home, plugins)
    compose_files = []

    cache_dir = Path(_hmd_home) / ".cache" / "local_services"

    if os.path.exists(cache_dir):
        for root, _, files in os.walk(cache_dir):
            for f in files:
                if f.startswith("docker-compose."):
                    compose_files.append(os.path.join(root, f))
                    with open(os.path.join(root, f), "r") as yml:
                        cfg = yaml.safe_load(yml)
                        for svc, svc_cfg in cfg["services"].items():
                            if "BUCKET_NAME" in svc_cfg.get("environment", {}):
                                resources["buckets"].append(
                                    {
                                        "name": svc,
                                        "url": svc_cfg.get("environment", {}).get(
                                            "BUCKET_NAME"
                                        ),
                                    }
                                )

    # Render Compose Files
    for plugin, enabled in plugins.items():
        if enabled:
            entrypoint = _load_entry_point(plugin, COMPOSE_PLUGIN_ENTRY_POINT)
            if entrypoint is not None:
                compose_file = entrypoint.load()(
                    resources, _hmd_home / ".cache", plugins
                )
                if compose_file is not None:
                    compose_files.append(compose_file)

    command = [
        *_get_base_command(compose_files),
    ]
    command += [
        "up",
        "--remove-orphans",
        "-d",
        "--quiet-pull",
    ]
    _exec(command)


def stop_neuronsphere():
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
        if file_.endswith(".yml"):
            compose_files.append(_hmd_home / ".cache" / file_)

    cache_dir = Path(_hmd_home) / ".cache" / "local_services"

    if os.path.exists(cache_dir):
        for root, _, files in os.walk(cache_dir):
            for f in files:
                if f.startswith("docker-compose."):
                    compose_files.append(os.path.join(root, f))

    command = [*_get_base_command(compose_files), "down"]
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

        start_neuronsphere()


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
    command = [*_get_base_command(), "--verbose", "pull"]
    _exec(command)
