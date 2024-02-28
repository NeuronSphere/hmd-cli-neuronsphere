import os
from pathlib import Path
import shutil
from typing import Dict, List

import yaml

_dirname = Path(os.path.dirname(__file__))
_services_dir = _dirname / ".." / "services"


def enabled(config_overrides: Dict[str, bool] = {}):
    val = os.environ.get("HMD_LOCAL_NEURONSPHERE_ENABLE_TRINO")
    if val is not None:
        return val == "true"
    return config_overrides.get("trino", True)


def get_resources():
    return {}


def prepare_hmd_home(hmd_home: str, configs: Dict[str, bool] = {}):
    HMD_HOME = Path(hmd_home)

    required_dirs = [
        Path("trino", "data"),
        Path("trino", "config"),
        Path("trino", "hadoop", "dfs", "name"),
        Path("trino", "hadoop", "dfs", "data"),
        Path("hive", "config"),
        Path("hadoop", "config"),
        Path(".cache", "hadoop"),
        Path("warehouse"),
    ]

    for dir_ in required_dirs:
        full_dir = HMD_HOME / dir_
        if not full_dir.exists():
            os.umask(0)
            print("make", str(full_dir))
            os.makedirs(full_dir, exist_ok=True)
    if len(os.listdir(HMD_HOME / "trino" / "config")) == 0:
        shutil.copytree(
            _services_dir / "trino" / "config",
            HMD_HOME / "trino" / "config",
            dirs_exist_ok=True,
        )

    if not configs.get("graph", True):
        graph_catalog_path = (
            Path(HMD_HOME) / "trino" / "config" / "catalog" / "local-graph"
        )

        if graph_catalog_path.exists():
            os.unlink(graph_catalog_path)

    if len(os.listdir(HMD_HOME / "hive" / "config")) == 0:
        shutil.copytree(
            _services_dir / "hive",
            HMD_HOME / "hive" / "config",
            dirs_exist_ok=True,
        )
    if len(os.listdir(HMD_HOME / "hadoop" / "config")) == 0:
        shutil.copytree(
            _services_dir / "hadoop",
            HMD_HOME / "hadoop" / "config",
            dirs_exist_ok=True,
        )

    if not (HMD_HOME / ".cache" / "hadoop" / "hadoop-hive.env").exists():
        shutil.copy2(
            _services_dir / "hadoop-hive.env",
            HMD_HOME / ".cache" / "hadoop" / "hadoop-hive.env",
        )


def render_compose_yaml(
    resources: Dict[str, List[str]],
    cache_dir: Path,
    configs: Dict[str, bool] = {},
):
    compose_dict = {}
    with open(_services_dir / "docker-compose.trino.yml", "r") as dc:
        compose_dict = yaml.safe_load(dc)

    compose_path = cache_dir / "docker-compose.trino.yml"
    if compose_path.exists():
        os.unlink(compose_path)

    with open(compose_path, "w") as dc_out:
        yaml.safe_dump(compose_dict, dc_out)

    return compose_path
