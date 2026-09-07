"""Local deployment support for generating deploy scripts without Argo.

When running in local deploy mode, ms-deployment generates deploy scripts
(the same scripts it would include in an Argo workflow) and returns them
via API so the CLI can execute them in hmd-img-projectbuilder containers.
"""

import io
import json
import logging
from typing import Dict, List, Optional

from hmd_graphql_client.relationship_support import RelationshipSupport
from hmd_lang_deployment.hmd_lang_deployment_client import HmdLangDeploymentClient
from hmd_ms_deployment import DEPLOY_NEXT, DESTROY_NEXT
from hmd_ms_deployment.deploy_logic import build_deployment_dag, GraphNode
from hmd_ms_deployment.deployment_dag_traversal import traverse_deployment_dag
from hmd_ms_deployment.deploy_base import DeployBase
from hmd_ms_deployment.instance_config_collector import InstanceConfigCollector
from hmd_ms_deployment.environment_information import EnvironmentInformation

logger = logging.getLogger(f"HMD.{__name__}")


def generate_local_deploy_manifest(
    client: HmdLangDeploymentClient,
    rs: RelationshipSupport,
    csd_nid: str,
) -> Dict:
    """Generate a local deployment manifest from a ChangeSetDeployment.

    Builds the deployment DAG and generates per-node deploy scripts using the
    same code path as Argo workflow generation, but returns the scripts as a
    JSON manifest instead of submitting to Argo.

    Handles both directions. A ChangeSetDeployment whose deployments are
    ``DESTROY_NEXT`` yields a destroy manifest: the DAG is walked in reverse
    dependency order and each script is a ``deploy --destroy``. The caller can
    tell which it got from the ``destroy`` flag, and must record the resulting
    node statuses as ``DESTROYED`` rather than ``DEPLOYED``.

    :param client: The deployment client.
    :param rs: RelationshipSupport for entity resolution.
    :param csd_nid: The ChangeSetDeployment identifier.
    :returns: Dict with a ``destroy`` flag and a ``nodes`` list, each node
              containing instance_name, repo_class_name, version, rid_nid,
              script, and dependencies.
    """
    csd = client.get_change_set_deployment_hmd_lang_deployment(csd_nid)

    all_nodes = []
    manifest_destroy = False

    # Process each ChangeSetEnvDeployment
    for csed_rel in client.get_from_change_set_deployment_has_change_set_env_deployment_hmd_lang_deployment(csd):
        csed = rs.ref_to(csed_rel)

        env = rs.ref_to(
            client.get_from_change_set_env_deployment_has_environment_hmd_lang_deployment(csed)[0]
        )
        env_info = EnvironmentInformation(env, client=client)

        rid_rels = client.get_from_change_set_env_deployment_has_repo_instance_deployment_hmd_lang_deployment(csed)
        repo_instance_deployment_ids = [rel.ref_to for rel in rid_rels]

        # A destroy ChangeSetDeployment is one whose deployments are DESTROY_NEXT
        # rather than DEPLOY_NEXT (see DeploymentManager.prepare_destroy_environments).
        # Everything downstream -- DAG direction, instance config collection and
        # the generated script's `deploy --destroy` -- keys off this flag, so it
        # is derived once from the deployments the changeset actually carries.
        rs.pull_relationship_nouns(rid_rels)
        target_status = DEPLOY_NEXT
        for rel in rid_rels:
            if rs.ref_to(rel).status == DESTROY_NEXT:
                target_status = DESTROY_NEXT
                break
        destroy = target_status == DESTROY_NEXT
        manifest_destroy = manifest_destroy or destroy

        deployment_dag = build_deployment_dag(
            env_info, repo_instance_deployment_ids, destroy=destroy
        )

        # Collect instance configs (same as Argo workflow generation)
        config_collector = InstanceConfigCollector(client, rs)
        instance_configs = config_collector.collect_instance_configs(
            deployment_dag, destroy=destroy
        )

        # Generate deploy scripts per node using DeployBase
        deploy_base = DeployBase(client, rs)
        command_list = []
        context = {
            "client": client,
            "command_list": command_list,
            "destroy": destroy,
            "generate_deployed_call": False,  # Runner handles status
            "environment": env.type,
            "instance_configs": instance_configs,
            "repo_instance_deployment_ids": repo_instance_deployment_ids,
        }
        traverse_deployment_dag(deployment_dag, deploy_base.deploy_node, context)

        # Build node descriptors from the command_list
        # deploy_node appends (script_text, node, active_deps) tuples
        for script_text, graph_node, active_deps in command_list:
            instance = graph_node.instance

            # Find the repo class name and version for this instance
            repo_classes = client.get_from_repo_instance_isa_repo_class_hmd_lang_deployment(instance)
            repo_class = rs.ref_to(repo_classes[0])

            # Find the RID for this instance in the current changeset
            rids = client.get_from_repo_instance_has_repo_instance_deployment_hmd_lang_deployment(instance)
            rid_nid = None
            version = None
            for rid_rel in rids:
                rid = rs.ref_to(rid_rel)
                if rid.status == target_status and rid_rel.ref_to in repo_instance_deployment_ids:
                    rid_nid = rid.identifier
                    # Get version from RID -> RCV relationship
                    rcv_rels = client.get_from_repo_instance_deployment_has_repo_class_version_hmd_lang_deployment(rid)
                    if rcv_rels:
                        rs.pull_relationship_nouns(rcv_rels)
                        version = rs.ref_to(rcv_rels[0]).version
                    break

            all_nodes.append({
                "instance_name": instance.name,
                "repo_class_name": repo_class.repo_class_name,
                "version": version,
                "rid_nid": rid_nid,
                "script": script_text,
                "dependencies": list(active_deps),
            })

    return {"nodes": all_nodes, "destroy": manifest_destroy}
