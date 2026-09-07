from typing import Dict, Union, List, Set

from hmd_graphql_client.relationship_support import RelationshipSupport
from hmd_lang_deployment.hmd_lang_deployment_client import HmdLangDeploymentClient
from hmd_ms_deployment.deploy_logic import DeploymentDag, GraphNode
from .deploy_base import DeployBase
from .deployment_dag_traversal import traverse_deployment_dag


class InstanceConfigCollector(DeployBase):
    def __init__(
        self, client: HmdLangDeploymentClient, rs: RelationshipSupport = None
    ):
        super().__init__(client, rs)

    def collect_config(self, node: GraphNode, context: Dict):
        assert (
            "instance_configs" in context
        ), "I expected a instance_config dict object in the context, what's up?"
        instance_configs: Dict[str, Dict] = context["instance_configs"]
        assert (
            "destroy" in context
        ), "I expected a destroy bool in the context, what's up?"
        destroy: bool = context["destroy"]

        instance_configs[node.instance.name] = self.get_instance_config(node.instance, destroy)


    def collect_instance_configs(self,
        deployment_dag: DeploymentDag,
        destroy: bool = False,
    ) -> Dict[str, Dict]:

        context = {
            "instance_configs": {},
            "destroy": destroy
        }
        traverse_deployment_dag(
            deployment_dag,
            self.collect_config,
            context,
        )
        instance_configs = context["instance_configs"]
        def get_instance_names(config: Union[List, Dict], names: Set[str]):
            my_config = config
            if not isinstance(my_config, list):
                my_config = [config]
            for cfg in my_config:
                if "instance_name" in cfg:
                    names.add(cfg["instance_name"])
                for sub_cfg in cfg.get("dependencies", {}).values():
                    get_instance_names(sub_cfg, names)

        # populate the details section
        result = {}
        for instance_name in instance_configs:

            config = instance_configs[instance_name]
            # make a copy...
            config = {key: value for key, value in config.items()}
            names: Set[str] = set()
            get_instance_names(config, names)
            config["details"] = {
                key: {
                    key2: value2
                    for key2, value2 in an_instance_config.items()
                    if key2 != "dependencies"
                }
                for key, an_instance_config in instance_configs.items()
                if key in names
            }
            result[instance_name] = config

        return result