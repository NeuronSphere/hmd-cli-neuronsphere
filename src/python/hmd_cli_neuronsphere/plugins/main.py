import os
from pathlib import Path
import shutil
from typing import Dict, List

import yaml

_dirname = Path(os.path.dirname(__file__))
_services_dir = _dirname / ".." / "services"


def enabled(config_overrides: Dict[str, bool] = {}):
    return True


def get_resources():
    return {"services": ["ms-naming"]}


def prepare_hmd_home(hmd_home: str, configs: Dict[str, bool] = {}):
    HMD_HOME = Path(hmd_home)

    required_dirs = [
        Path("data"),
        Path("language_packs"),
        Path(".cache"),
        Path("postgresql", "data"),
        Path(".cache", "nginx"),
        Path(".cache", "naming"),
    ]

    for dir_ in required_dirs:
        full_dir = HMD_HOME / dir_
        if not full_dir.exists():
            os.umask(0)
            print("make", str(full_dir))
            os.makedirs(full_dir, exist_ok=True)

    # Copy over included Postgres Init scripts
    _pg_scripts_path = _services_dir / "postgres"

    for root, _, files in os.walk(_pg_scripts_path):
        for f in files:
            dest = (
                HMD_HOME
                / "postgresql"
                / "scripts"
                / (Path(root) / f).relative_to(_pg_scripts_path)
            )

            if not os.path.exists(dest.parent):
                os.makedirs(dest.parent, mode=0o777, exist_ok=True)

            shutil.copy2(
                Path(root) / f,
                dest,
            )

    shutil.copy2(
        _services_dir / "nginx" / "neuronsphere.conf",
        HMD_HOME / ".cache" / "nginx" / "neuronsphere.conf",
    )

    shutil.copy2(
        _services_dir / "naming" / "db_init.sql",
        HMD_HOME / ".cache" / "naming" / "db_init.sql",
    )


def render_compose_yaml(
    resources: Dict[str, List[str]],
    cache_dir: Path,
    configs: Dict[str, bool] = {},
):
    compose_dict = {}
    with open(_services_dir / "docker-compose.main.yml", "r") as dc:
        compose_dict = yaml.safe_load(dc)

    compose_path = cache_dir / "docker-compose.main.yml"
    if compose_path.exists():
        os.unlink(compose_path)

    with open(compose_path, "w") as dc_out:
        yaml.safe_dump(compose_dict, dc_out)

    return compose_path
