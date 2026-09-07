from typing import List

from hmd_lang_deployment.environment import Environment
from hmd_lang_deployment.hmd_lang_deployment_client import HmdLangDeploymentClient
from hmd_ms_deployment.deploy_logic import DeploymentDag
from .deploy_base import DeployBase
from .deployment_dag_traversal import traverse_deployment_dag


class DeployScriptCreator(DeployBase):
    def __init__(self, client: HmdLangDeploymentClient):
        super().__init__(client)

    def create_deploy_script(
        self,
        environment: Environment,
        customer_code: str,
        deployment_dag: DeploymentDag,
        repo_instance_deployment_ids: List[str],
        instance_configs,
        destroy: bool = False,
        profile: str = None,
        generate_deployed_call: bool = True,
    ):

        command_list = []
        context = {
            "client": self.client,
            "command_list": command_list,
            "destroy": destroy,
            "generate_deployed_call": generate_deployed_call,
            "environment": environment.type,
            "instance_configs": instance_configs,
            "repo_instance_deployment_ids": repo_instance_deployment_ids,
        }
        if profile:
            context["profile"] = profile
        traverse_deployment_dag(
            deployment_dag,
            self.deploy_node,
            context,
        )

        preamble = f"""#!/bin/sh
set -e

"""

        command_list.insert(0, (preamble,))

        return "\n".join([cl[0] for cl in command_list])
