import json
import os
from pathlib import Path
from typing import Dict, List
from tempfile import TemporaryDirectory
from pkgutil import get_loader

from cement.utils.shell import cmd
from dotenv import load_dotenv
from hmd_cli_tools import cd
from hmd_cli_tools.okta_tools import get_auth_token
import yaml


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


def _get_configs():
    if not any(_configs):
        hmd_repo_home = (
            Path(_get_env_var("HMD_REPO_HOME"))
            if _get_env_var("HMD_REPO_HOME") is not None
            else None
        )
        enable_project = _get_tech_enabled("PROJECT", True)
        enable_trino = _get_tech_enabled("TRINO")
        enable_dynamodb = _get_tech_enabled("DYNAMODB")
        enable_hmd_ms_core = _get_tech_enabled("HMD_MS_CORE")
        enable_hmd_ms_mesh_proto = _get_tech_enabled("HMD_MS_MESH_PROTO")
        enable_hmd_ms_deployment = _get_tech_enabled("HMD_MS_DEPLOYMENT")
        enable_hmd_ms_librarian = _get_tech_enabled("HMD_MS_LIBRARIAN")
        enable_transform = _get_tech_enabled("TRANSFORM")
        enable_apache_superset = _get_tech_enabled("APACHE_SUPERSET")

        _configs.update(
            {
                "base": {
                    "enabled": True,
                    "path": _services_dir / f"docker-compose.main.yml",
                },
                "project": {
                    "enabled": enable_project,
                    "path": _services_dir / f"docker-compose.project.yml",
                },
                "jupyter": {
                    "enabled": True,
                    "path": _services_dir / f"docker-compose.jupyter.yml",
                },
                "trino": {
                    "enabled": enable_trino,
                    "path": _services_dir / f"docker-compose.trino.yml",
                },
                "dynamodb": {
                    "enabled": enable_dynamodb,
                    "path": _services_dir / f"docker-compose.dynambodb.yml",
                },
                "hmd-ms-core": {
                    "enabled": enable_hmd_ms_core,
                    "path": _services_dir / f"docker-compose.hmd-ms-core.yml",
                },
                "hmd-ms-mesh-proto": {
                    "enabled": enable_hmd_ms_mesh_proto,
                    "path": _services_dir / f"docker-compose.hmd-ms-mesh-proto.yml",
                },
                "hmd-ms-deployment": {
                    "enabled": enable_hmd_ms_deployment,
                    "path": _services_dir / f"docker-compose.hmd-ms-deployment.yml",
                },
                "hmd-ms-librarian": {
                    "enabled": enable_hmd_ms_librarian,
                    "path": _services_dir / f"docker-compose.hmd-ms-librarian.yml",
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
            }
        )
        if hmd_repo_home is not None:

            def set_config_path(tech_name, repo_name, file_name="docker-compose.yaml"):
                repo_path = hmd_repo_home / repo_name / "src" / "docker" / file_name
                if repo_path.exists():
                    _configs.get(tech_name)["path"] = repo_path

            set_config_path("base", "hmd-img-local-ns")
            set_config_path("trino", "hmd-img-local-ns", "docker-compose.hive.yml")
            set_config_path(
                "dynamodb", "hmd-img-local-ns", "docker-compose.dynamodb.yml"
            )
            set_config_path("jupyter", "hmd-img-jupyter-server")
            set_config_path("hmd-ms-core", "hmd-ms-core", "docker-compose.local.yaml")
            set_config_path(
                "hmd-ms-mesh-proto", "hmd-ms-mesh-proto", "docker-compose.local.yaml"
            )
            set_config_path("hmd-ms-deployment", "hmd-ms-deployment")
            set_config_path(
                "apache-superset", "hmd-inf-superset", "docker-compose-non-dev.yml"
            )
            set_config_path("airflow", "hmd-img-airflow", "docker-compose-local.yml")
            set_config_path("transform", "hmd-ms-transform", "docker-compose.local.yml")
            set_config_path("hmd-ms-librarian", "hmd-ms-librarian")

    return _configs


def _get_base_command():
    stdout, _, _ = _exec(
        ["pip", "config", "get", "global.extra-index-url"], capture=True
    )
    pip_url = stdout.decode("utf-8")
    os.environ["PIP_EXTRA_INDEX_URL"] = pip_url
    command = ["docker-compose", "--project-name", "neuronsphere"]
    configs = _get_configs()

    for name, details in configs.items():
        if details.get("enabled"):
            command += ["-f", str(details.get("path"))]
    return command


def start_neuronsphere():
    load_env()
    required_dirs = [
        Path("data", "raw"),
        Path("studio", "projects"),
        Path("postgresql", "data"),
        Path("datadog", "s6"),
        Path("datadog", "log"),
        Path("transform"),
    ]
    configs = _get_configs()
    if configs.get("trino").get("enabled"):
        required_dirs += [
            Path("trino", "data"),
            Path("trino", "config"),
            Path("trino", "hadoop", "dfs", "name"),
            Path("trino", "hadoop", "dfs", "data"),
            Path("warehouse"),
        ]
    if configs.get("transform").get("enabled"):
        required_dirs += [Path("transform", "airflow", "logs")]
        required_dirs += [Path("transform", "airflow", "provider_transforms")]
        required_dirs += [Path("transform", "airflow", "dag_generators")]
        required_dirs += [Path("data", "local_transforms")]
    for dir in required_dirs:
        full_dir = _hmd_home / dir
        if not full_dir.exists():
            print("make", str(_hmd_home / dir))
            (_hmd_home / dir).mkdir(exist_ok=True, parents=True)

    command = [*_get_base_command(), "up", "--remove-orphans", "--force-recreate", "-d"]
    _exec(command)


def stop_neuronsphere():
    load_env()
    command = [*_get_base_command(), "down"]
    _exec(command)


def merge_configs(config: Dict, default: Dict):
    for key, value in config.items():
        if isinstance(value, dict):
            node = default.setdefault(key, {})
            merge_configs(value, node)
        else:
            default[key] = value

    return default


MICROSERVICE_DB_INIT_SQL = """
CREATE USER {username} WITH PASSWORD '{password}';
CREATE DATABASE {database};
GRANT ALL PRIVILEGES ON DATABASE {database} TO {username};
"""


def run_local_service(
    repo_name: str, repo_version: str, mount_packages: List[str] = []
):
    load_env()
    stdout, _, _ = _exec(
        ["pip", "config", "get", "global.extra-index-url"], capture=True
    )
    pip_url = stdout.decode("utf-8")
    os.environ["PIP_EXTRA_INDEX_URL"] = pip_url
    os.environ["HMD_AUTH_TOKEN"] = get_auth_token()
    command = [
        "docker-compose",
        "--project-name",
        "neuronsphere",
    ]
    volumes = [{"type": "bind", "source": "$HOME/.aws", "target": "/root/.aws"}]

    for mnt in mount_packages:
        pkg_path = get_loader(mnt.replace("-", "_"))

        volumes.append(
            {
                "type": "bind",
                "source": str(Path.resolve(Path(pkg_path.get_filename()).parent)),
                "target": f"/usr/local/lib/python3.9/site-packages/{mnt.replace('-','_')}",
            }
        )

    service_config = {}

    if os.path.exists("./meta-data/config_local.json"):
        with open("./meta-data/config_local.json", "r") as local_cfg:
            service_config = json.load(local_cfg)

    default_config = {
        "version": "3.2",
        "services": {
            repo_name.replace("-", "_"): {
                "image": f"{os.environ.get('HMD_CONTAINER_REGISTRY')}/{repo_name}:{repo_version}",
                "container_name": repo_name.replace("-", "_"),
                "environment": {
                    "HMD_INSTANCE_NAME": repo_name,
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
                    "AWS_PROFILE": os.environ.get("AWS_PROFILE"),
                    "AWS_XRAY_SDK_ENABLED": False,
                    "SERVICE_CONFIG": json.dumps(service_config),
                    "DD_LAMBDA_HANDLER": "hmd_ms_base.hmd_ms_base.handler",
                    "DD_API_KEY": "${DD_API_KEY}",
                    "DD_LOCAL_TEST": True,
                    "DD_TRACE_ENABLED": False,
                    "DD_SERVERLESS_LOGS_ENABLED": False,
                },
                "expose": [8080],
                "volumes": volumes,
            },
            "db_init": {
                "image": "${HMD_CONTAINER_REGISTRY}/hmd-postgres-base:${HMD_POSTGRES_BASE_VERSION}",
                "container_name": f"{repo_name}_db_init",
                "environment": {
                    "HMD_ENVIRONMENT": os.environ.get("HMD_ENVIRONMENT", "local"),
                    "HMD_REGION": os.environ.get("HMD_REGION", "local"),
                    "HMD_CUSTOMER_CODE": os.environ.get("HMD_CUSTOMER_CODE"),
                    "HMD_DID": "aaa",
                    "PGPASSWORD": "admin",
                },
                "ports": ["15432:5432"],
                "command": 'psql -h db --username postgres -a --dbname "$POSTGRES_DB" -f /root/sql/db_init.sql',
            },
        },
    }

    with cd("./src/docker"):
        config = {}
        if os.path.exists("docker-compose.local.yaml"):
            with open("docker-compose.local.yaml", "r") as dc:
                config = yaml.safe_load(dc)

    final_config = merge_configs(config, default_config)

    with TemporaryDirectory() as tmpdir:

        path = Path(tmpdir) / "docker-compose.local.yaml"
        sql_path = Path(tmpdir) / "db_init.sql"

        with open(sql_path, "w") as sql:
            sql.write(
                MICROSERVICE_DB_INIT_SQL.format(
                    username=repo_name.replace("-", "_"),
                    password=repo_name.replace("-", "_"),
                    database=repo_name.replace("-", "_"),
                )
            )

        final_config["services"]["db_init"]["volumes"] = [
            {"type": "bind", "source": str(sql_path), "target": "/root/sql/db_init.sql"}
        ]

        with open(path, "w") as fcfg:
            yaml.dump(final_config, fcfg)

        command.extend(
            [
                "-f",
                str(path),
                "up",
                "--force-recreate",
                "-d",
            ]
        )

        _exec(command)
