import io
from typing import Dict

from hmd_graphql_client.relationship_support import RelationshipSupport
from hmd_lang_deployment.repo_class import RepoClass
from hmd_lang_deployment.repo_class_version import RepoClassVersion
from hmd_lang_deployment.repo_instance_deployment import RepoInstanceDeployment
from hmd_ms_deployment import DEPLOYED
from hmd_ms_deployment.deploy_logic import GraphNode, DeploymentDag
from .deployment_dag_traversal import traverse_deployment_dag


def draw_node(node: GraphNode, context: Dict):

    assert (
        "node_file" in context
    ), "I expected a file like object for nodes in the context, what's up?"
    assert (
        "relationship_file" in context
    ), "I expected a file like object for relationships in the context, what's up?"
    assert (
        "relationship_support" in context
    ), "I expected an RelationshipSupport object in the context, what's up?"
    node_file_ = context["node_file"]
    relationship_file_ = context["relationship_file"]
    rel_support: RelationshipSupport = context["relationship_support"]

    ri = node.instance
    rid: RepoInstanceDeployment = rel_support.ref_to(
        ri.get_from_repo_instance_has_repo_instance_deployment_hmd_lang_deployment()[0]
    )
    rcv = rel_support.ref_to(
        rid.get_from_repo_instance_deployment_has_repo_class_version_hmd_lang_deployment()[
            0
        ]
    )  # type: RepoClassVersion
    rc = rel_support.ref_to(
        ri.get_from_repo_instance_isa_repo_class_hmd_lang_deployment()[0]
    )  # type: RepoClass

    color = "lightgrey" if rid.status == DEPLOYED else "lightgreen"
    print(
        f"component [instance: {ri.name}\\nclass: {rc.repo_class_name}:{rcv.version}\\nstatus: {rid.status}] as {ri.identifier.replace('-', '_')} #{color}",
        file=node_file_,
    )

    for ri2 in [node.instance for node in node.children]:
        print(
            f"{ri.identifier.replace('-', '_')} --> {ri2.identifier.replace('-', '_')}",
            file=relationship_file_,
        )


def generate_dag_diagram(deployment_dag: DeploymentDag):
    with io.StringIO() as node_file_:
        with io.StringIO() as relationship_file_:
            traverse_deployment_dag(
                deployment_dag,
                draw_node,
                {"node_file": node_file_, "relationship_file": relationship_file_},
            )
            return f"""@startuml 
skinparam ranksep 100
{node_file_.getvalue()}
{relationship_file_.getvalue()}
@enduml
"""
