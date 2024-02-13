import os
from pathlib import Path
import shutil
from typing import Dict, List

import yaml

_dirname = Path(os.path.dirname(__file__))
_services_dir = _dirname / ".." / "services"


def enabled(config_overrides: Dict[str, bool] = {}):
    val = os.environ.get("HMD_LOCAL_NEURONSPHERE_ENABLE_MINIO")
    if val is not None:
        return val == "true"
    return config_overrides.get("minio", True)


def get_resources():
    return {}


def prepare_hmd_home(hmd_home: str, configs: Dict[str, bool] = {}):
    HMD_HOME = Path(hmd_home)

    required_dirs = [
        Path("data", "librarians"),
    ]

    for dir_ in required_dirs:
        full_dir = HMD_HOME / dir_
        if not full_dir.exists():
            os.umask(0)
            print("make", str(full_dir))
            os.makedirs(full_dir, exist_ok=True)


def render_compose_yaml(
    resources: Dict[str, List[str]],
    cache_dir: Path,
    configs: Dict[str, bool] = {},
):
    compose_dict = {}
    with open(_services_dir / "docker-compose.minio.yml", "r") as dc:
        compose_dict = yaml.safe_load(dc)

    compose_path = cache_dir / "docker-compose.minio.yml"
    if compose_path.exists():
        os.unlink(compose_path)

    with open(compose_path, "w") as dc_out:
        yaml.safe_dump(compose_dict, dc_out)

    return compose_path
