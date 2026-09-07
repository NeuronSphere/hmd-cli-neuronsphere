import json
import logging
import os
import shutil
import subprocess
import sys
from pathlib import Path
from tempfile import TemporaryDirectory
from typing import Dict, List

from hmd_lib_librarian_client.artifact_tools import (
    retrieve_and_unzip,
    content_item_path_from_spec,
    zip_and_archive,
)

root = "/prep_root/"
logger = logging.getLogger(f"HMD.{__name__}")
logging.basicConfig(stream=sys.stdout, level=10)


def prepare_workflow_gate(config: List[Dict]):
    """Prepare the file system to support deployment workflow gating.
    Preparation includes downloading the artifact an exploding it into
    the appropriate input directory.

    The config is collection of Dicts of the following format...
    {
        "environment": <environment_type>,
        "seq_no": <transform_sequence>,
        "repo_instance_name": <repo_instance_ame>,
        "transform": {
            "image_name": <image_name>,
            "tag": <image_tag>
        },
        "instance_configuration": <dict of instance config>,
        "artifact": {
            "artifact_name": change["repo_class_name"],
            "artifact_version": change["repo_class_version"],
        }
        "input_path": <input_dir_path>,
        "output_path": <output_dir_path>,
    }
    :param config:
    :type config:
    :return:
    :rtype:
    """
    for cfg in config:
        input_path = Path(root) / Path(cfg["input_path"])
        output_path = Path(root) / Path(cfg["output_path"])

        os.makedirs(input_path, exist_ok=False)
        os.makedirs(output_path, exist_ok=False)

        artifact_spec = cfg["artifact_ref"]
        content_item_path, _ = content_item_path_from_spec(artifact_spec)
        retrieve_and_unzip(
            os.environ["HMD_CUSTOMER_CODE"],
            os.environ["HMD_REGION"],
            content_item_path,
            input_path,
        )


def gather_bender_results(change_set_deployment: Dict, change_set: Dict):
    with TemporaryDirectory() as tmp_dir:
        tmp_dir_path = Path(tmp_dir)
        with open(tmp_dir_path / "change_set_deployment.json", "w") as csd_file:
            csd_file.write(json.dumps(change_set_deployment, indent=2))

        with open(tmp_dir_path / "change_set.json", "w") as cs_file:
            cs_file.write(json.dumps(change_set, indent=2))

        bender_output_found = False
        output_paths = []
        paths_to_cleanup = []
        for base, dirs, files in os.walk(root):
            # look for "output/bender"...
            logger.debug(f"Considering: {base}")
            base_path = Path(base)
            if len(base_path.parts) > 2 and base_path.parts[-2:] == (
                "output",
                "bender",
            ):
                logger.debug(f"Files: {files}")
                bender_output_found = True
                shutil.copytree(
                    base_path,
                    Path(tmp_dir_path, *base_path.parts[-4:]),
                    dirs_exist_ok=True,
                )
                output_paths.append(
                    str(Path(tmp_dir_path, *base_path.parts[-4:]) / "output.xml")
                )
                # Track the environment/instance directory for cleanup (e.g., "dev/instance_name_0")
                env_instance_dir = Path(root) / base_path.parts[1] / base_path.parts[2]
                if env_instance_dir not in paths_to_cleanup:
                    paths_to_cleanup.append(env_instance_dir)

        if bender_output_found:
            rebot_command = [
                "rebot",
                f"--settag=change_set_deployment_id:{change_set_deployment['identifier']}",
                "--outputdir",
                str(tmp_dir_path),
                "--output",
                "output.xml",
            ] + output_paths

            subprocess.run(rebot_command)

            # should always be there, but was added late, so just in case...
            deployment_set_name = change_set_deployment.get(
                "deployment_set_name", "unknown"
            )
            # should be in iso format
            deploy_time: str = change_set_deployment["deploy_time"]
            assert (
                len(deploy_time) >= 10
            ), f"Expected deploy_time to be in ISO format, was '{deploy_time}'."
            deploy_time = deploy_time.replace(":", "-")
            # Strip timezone info if present (e.g., "+00:00" or "Z")
            if deploy_time.endswith("Z"):
                deploy_time = deploy_time[:-1]
            if "+" in deploy_time:
                deploy_time = deploy_time.split("+", 1)[0]
            zip_and_archive(
                os.environ["HMD_CUSTOMER_CODE"],
                os.environ["HMD_REGION"],
                f"deploy:/{deployment_set_name}/{deploy_time[:10]}/bender_results_{deploy_time}.zip",
                "bender_results",
                tmp_dir_path,
            )

            # Cleanup transform output directories after successful archival
            logger.info(f"Cleaning up {len(paths_to_cleanup)} transform output directories")
            for cleanup_path in paths_to_cleanup:
                try:
                    if cleanup_path.exists():
                        logger.info(f"Removing directory: {cleanup_path}")
                        shutil.rmtree(cleanup_path)
                except Exception as e:
                    logger.warning(f"Failed to cleanup {cleanup_path}: {e}")
