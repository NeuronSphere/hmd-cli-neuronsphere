import json
import os
from pathlib import Path
import shutil
from typing import Dict, List

import yaml

_dirname = Path(os.path.dirname(__file__))
_services_dir = _dirname / ".." / "services"


def enabled(config_overrides: Dict[str, bool] = {}):
    val = os.environ.get("HMD_LOCAL_NEURONSPHERE_ENABLE_JUPYTER")
    if val is not None:
        return val == "true"
    return config_overrides.get("jupyter", True)


def get_resources():
    return {
        "services": [
            {"name": "ms-transform", "url": "http://hmd_gateway/hmd_ms_transform/"}
        ]
    }


def prepare_hmd_home(hmd_home: str, configs: Dict[str, bool] = {}):
    HMD_HOME = Path(hmd_home)

    required_dirs = [
        Path("transform"),
        Path("transform", "queries"),
        Path("data", "local_transforms"),
        Path("queues"),
    ]

    for dir_ in required_dirs:
        full_dir = HMD_HOME / dir_
        if not full_dir.exists():
            os.umask(0)
            print("make", str(full_dir))
            os.makedirs(full_dir, exist_ok=True)

    if len(os.listdir(HMD_HOME / "queues")) == 0:
        shutil.copytree(
            _services_dir / "queues",
            HMD_HOME / "queues",
            dirs_exist_ok=True,
        )
    if len(os.listdir(HMD_HOME / "transform" / "queries")) == 0:
        query_cfg = {}
        with open(_services_dir / "transform" / "query_config.json", "r") as qc:
            query_cfg = json.load(qc)

        existing_cfg = {}
        with open(HMD_HOME / "transform" / "queries" / "query_config.json", "r") as qc:
            existing_cfg = json.load(qc)

        with open(HMD_HOME / "transform" / "queries" / "query_config.json", "w") as qc:
            existing_cfg = json.dump({**existing_cfg, **query_cfg}, qc)


def render_compose_yaml(
    resources: Dict[str, List[Dict[str, str]]],
    cache_dir: Path,
    configs: Dict[str, bool] = {},
):
    compose_dict = {}
    with open(_services_dir / "docker-compose.transform.yml", "r") as dc:
        compose_dict = yaml.safe_load(dc)

    if not configs.get("graph") or not configs.get("airflow"):
        print("Cannot start Transform service because dependency not enabled")
        return

    buckets = resources.get("buckets", [])
    print(buckets)

    for bucket in buckets:
        compose_dict["services"]["transform"]["environment"][
            f'{bucket["name"].upper()}_BUCKET'
        ] = f's3://{bucket["url"]}'

    compose_path = cache_dir / "docker-compose.transform.yml"
    if compose_path.exists():
        os.unlink(compose_path)

    with open(compose_path, "w") as dc_out:
        yaml.safe_dump(compose_dict, dc_out)

    return compose_path
