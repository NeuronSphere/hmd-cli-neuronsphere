import os
from pathlib import Path

from cement.utils.shell import cmd
from dotenv import load_dotenv


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
    load_dotenv(_hmd_home / ".config" / "hmd.env", override=True)


def _get_tech_enabled(name):
    return _get_env_var(f"HMD_LOCAL_NEURONSPHERE_ENABLE_{name}") == "true"


def _get_configs():
    if not any(_configs):
        hmd_repo_home = (
            Path(_get_env_var("HMD_REPO_HOME"))
            if _get_env_var("HMD_REPO_HOME") is not None
            else None
        )
        enable_trino = _get_tech_enabled("TRINO")
        enable_dynamodb = _get_tech_enabled("DYNAMODB")
        enable_hmd_ms_core = _get_tech_enabled("HMD_MS_CORE")
        enable_hmd_ms_mesh_proto = _get_tech_enabled("HMD_MS_MESH_PROTO")
        enable_hmd_ms_deployment = _get_tech_enabled("HMD_MS_DEPLOYMENT")
        enable_hmd_ms_librarian = _get_tech_enabled("HMD_MS_LIBRARIAN")
        enable_airflow = _get_tech_enabled("AIRFLOW")
        enable_apache_superset = _get_tech_enabled("APACHE_SUPERSET")

        _configs.update(
            {
                "base": {
                    "enabled": True,
                    "path": _services_dir / f"docker-compose.main.yml",
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
                    "enabled": enable_airflow,
                    "path": _services_dir / f"docker-compose.airflow.yml",
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
    ]
    configs = _get_configs()
    if configs.get("trino").get("enabled"):
        required_dirs += [
            Path("trino", "data"),
            Path("trino", "config"),
            Path("warehouse"),
        ]
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
