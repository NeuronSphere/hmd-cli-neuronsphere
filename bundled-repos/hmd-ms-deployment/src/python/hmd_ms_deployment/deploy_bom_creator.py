from collections import OrderedDict, defaultdict
from typing import Dict, List

from hmd_graphql_client.relationship_support import RelationshipSupport
from hmd_lang_deployment.hmd_lang_deployment_client import HmdLangDeploymentClient
from hmd_lang_deployment.repo_class import RepoClass
from hmd_lang_deployment.repo_class_version import RepoClassVersion
from hmd_lang_deployment.repo_instance_deployment import RepoInstanceDeployment
from hmd_lang_deployment.repo_instance_req_repo_instance import (
    RepoInstanceReqRepoInstance,
)
from hmd_ms_deployment.deploy_logic import GraphNode, DeploymentDag
from hmd_ms_deployment.environment_information import (
    get_current_repo_instance_deployment,
)
from . import DEPLOYED, FAILED
from .deployment_dag_traversal import traverse_deployment_dag


class DeployBomCreator:
    def __init__(self, client: HmdLangDeploymentClient):
        self.client = client
        self.rs = RelationshipSupport([client._base_client])

    def create_node_bom(self, node: GraphNode, context: Dict):
        assert (
            "deployed_list" in context
        ), "I expected a list object in the context, what's up?"
        deployed_list = context["deployed_list"]

        result = {}
        result["instance_name"] = node.instance.name

        rc: RepoClass = self.rs.ref_to(
            node.instance.get_from_repo_instance_isa_repo_class_hmd_lang_deployment()[0]
        )
        ri_rid = get_current_repo_instance_deployment(node.instance, self.rs)
        if ri_rid and self.rs.ref_to(ri_rid).status in [DEPLOYED, FAILED]:
            rid: RepoInstanceDeployment = self.rs.ref_to(ri_rid)
            rcv = self.rs.ref_to(
                rid.get_from_repo_instance_deployment_has_repo_class_version_hmd_lang_deployment()[
                    0
                ]
            )  # type: RepoClassVersion

            instance_config = (
                rid.instance_configuration if rid.instance_configuration else {}
            )

            data = [
                ("repo_instance_name", node.instance.name),
                ("repo_class_name", rc.repo_class_name),
                ("repo_class_version", rcv.version),
                ("deployment_id", rid.deployment_id),
            ]
            if rid.hmd_region:
                data.append(("hmd_region", rid.hmd_region))
            if node.instance.auto_deploy:
                data.append(("auto_deploy", node.instance.auto_deploy))
            data += [
                ("status", rid.status),
            ]
            if rid.image_only:
                data.append(("image_only", True))
                data = OrderedDict(data)
            else:
                data += [
                    ("instance_configuration", instance_config),
                ]

                data = OrderedDict(data)
                ri_ris: List[RepoInstanceReqRepoInstance]
                ri_ris = (
                    node.instance.get_from_repo_instance_req_repo_instance_hmd_lang_deployment()
                )
                depedency_dict = defaultdict(list)
                for ri_ri in ri_ris:
                    depedency_dict[ri_ri.role].append(self.rs.ref_to(ri_ri).name)

                data["dependencies"] = OrderedDict(
                    sorted(
                        [(role, deps if len(deps) > 1 else deps[0]) for role, deps in depedency_dict.items()],
                        key=lambda x: x[0],
                    )
                )

                if rid.config_artifact_spec:
                    data["config_artifact_spec"] = rid.config_artifact_spec

            deployed_list.append(data)

    def create_deploy_bom(
        self,
        deployment_dag: DeploymentDag,
    ):

        deployed_list = []
        context = {"deployed_list": deployed_list}
        traverse_deployment_dag(
            deployment_dag,
            self.create_node_bom,
            context,
        )
        return deployed_list
