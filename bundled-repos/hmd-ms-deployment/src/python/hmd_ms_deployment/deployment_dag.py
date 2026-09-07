"""The deployment DAG for a ChangeSetDeployment, per environment.

Returns the same node/edge structure that ``DeployWorkflowCreator`` turns into
Argo DAG tasks -- one node per RepoInstance being deployed, with ``dependencies``
naming the instances it waits on -- so a consumer can draw the workflow the
deployment actually ran.

The topology comes from ``build_deployment_dag``, the same call that feeds Argo
task generation, and the edge set mirrors the workflow's ``dependencies`` (see
``_changeset_dependencies`` for the one deliberate difference). Node status comes
from the RepoInstanceDeployment entities, which outlive the Argo workflow, and pod
names come from the archived log markers (see ``step_log_index``) -- so nothing in
this module depends on the Argo API and the DAG resolves identically for a running
deployment and a long-collected one.
"""

import logging
import os
from typing import Dict, List, Optional

from hmd_cli_tools.hmd_cli_tools import get_session
from hmd_graphql_client.relationship_support import RelationshipSupport
from hmd_lang_deployment.hmd_lang_deployment_client import HmdLangDeploymentClient
from hmd_ms_deployment import DESTROY_NEXT
from hmd_ms_deployment.deploy_logic import build_deployment_dag
from hmd_ms_deployment.deployment_dag_traversal import traverse_deployment_dag
from hmd_ms_deployment.environment_information import EnvironmentInformation
from hmd_ms_deployment.step_log_index import build_step_pod_index, list_pod_prefixes

logger = logging.getLogger(f"HMD.{__name__}")


def _isoformat(value) -> Optional[str]:
    if value is None:
        return None
    try:
        return value.isoformat()
    except AttributeError:
        return str(value)


def _changeset_dependencies(
    client: HmdLangDeploymentClient,
    rs: RelationshipSupport,
    node,
    repo_instance_deployment_ids: List[str],
) -> set:
    """The instances a node waits on, skipping any not in this changeset.

    This mirrors ``DeployBase.get_active_dependencies`` -- the call that produces
    the Argo task ``dependencies`` -- including its transitive skip: a child that
    is not part of this changeset is replaced by *its* dependencies, so the edge
    set matches the workflow's exactly (asserted in test_deployment_dag.py).

    It differs in one deliberate way. ``get_active_dependencies`` additionally
    requires the child's RID to still be in the target status with no ``end``
    timestamp, which is right when generating a workflow but wrong for a view:
    the moment a deployment finishes, every RID goes terminal and that predicate
    would drop every edge, leaving a past deployment drawn as disconnected boxes.
    Membership in the changeset is the property that does not change over time,
    so the graph reads the same during the run and years later.
    """
    results = set()
    for child in node.children:
        rid_rels = client.get_from_repo_instance_has_repo_instance_deployment_hmd_lang_deployment(
            child.instance
        )
        in_changeset = any(
            rel.ref_to in repo_instance_deployment_ids for rel in rid_rels
        )
        if in_changeset:
            results.add(child.instance.name)
        else:
            results.update(
                _changeset_dependencies(
                    client, rs, child, repo_instance_deployment_ids
                )
            )
    return results


def _collect_dag_nodes(
    client: HmdLangDeploymentClient,
    rs: RelationshipSupport,
    env_info: EnvironmentInformation,
    environment: str,
    repo_instance_deployment_ids: List[str],
    destroy: bool,
) -> List[Dict]:
    """Walk one environment's deployment DAG and describe each node.

    The graph comes from ``build_deployment_dag`` -- the same call that feeds
    Argo task generation, so the topology and its transitive reduction are the
    workflow's. ``deploy_node`` itself is deliberately not used: it generates
    deploy scripts and collects instance configs, which this read-only view does
    not need.
    """
    deployment_dag = build_deployment_dag(
        env_info, repo_instance_deployment_ids, destroy=destroy
    )

    nodes: List[Dict] = []

    def visit(node, context):
        instance = node.instance

        repo_class_name = None
        repo_classes = client.get_from_repo_instance_isa_repo_class_hmd_lang_deployment(
            instance
        )
        if repo_classes:
            repo_class_name = rs.ref_to(repo_classes[0]).repo_class_name

        rid = None
        version = None
        rid_rels = client.get_from_repo_instance_has_repo_instance_deployment_hmd_lang_deployment(
            instance
        )
        for rid_rel in rid_rels:
            candidate = rs.ref_to(rid_rel)
            if rid_rel.ref_to in repo_instance_deployment_ids:
                rid = candidate
                rcv_rels = client.get_from_repo_instance_deployment_has_repo_class_version_hmd_lang_deployment(
                    candidate
                )
                if rcv_rels:
                    rs.pull_relationship_nouns(rcv_rels)
                    version = rs.ref_to(rcv_rels[0]).version
                break

        if rid is None:
            # Not part of this changeset -- build_deployment_dag includes such
            # instances so dependencies can be traversed through them, but they
            # are not steps in this deployment.
            return

        nodes.append(
            {
                "instance_name": instance.name,
                "repo_class_name": repo_class_name,
                "version": version,
                "rid_nid": rid.identifier,
                "deployment_id": rid.deployment_id,
                "environment": environment,
                "status": rid.status,
                "start": _isoformat(rid.start),
                "end": _isoformat(rid.end),
                "dependencies": sorted(
                    _changeset_dependencies(
                        client, rs, node, repo_instance_deployment_ids
                    )
                ),
                "pod_names": [],
            }
        )

    traverse_deployment_dag(deployment_dag, visit, {})
    return nodes


def _attach_pod_names(
    csd, environments: List[Dict], workflow_name: Optional[str]
) -> None:
    """Fill each node's ``pod_names`` from the archived step-log markers.

    Best effort throughout: without a workflow name, a logs bucket, or archived
    logs there is simply nothing to attach, and the DAG is still meaningful --
    a deployment that has not started, or one that ran locally, has no pods.
    """
    if not workflow_name:
        return

    logs_bucket = os.environ.get("ARGO_LOGS_BUCKET")
    if not logs_bucket:
        logger.debug("ARGO_LOGS_BUCKET not configured; skipping pod index")
        return

    argo_namespace = os.environ.get("ARGO_NAMESPACE", "argo-aaa")
    workflow_submitted_at = getattr(csd, "workflow_submitted_at", None)
    date_prefix = (
        workflow_submitted_at.strftime("%Y-%m-%d") if workflow_submitted_at else None
    )

    try:
        s3_client = get_session().client("s3")
        pod_prefixes, _ = list_pod_prefixes(
            s3_client, logs_bucket, argo_namespace, workflow_name, date_prefix
        )
        index = build_step_pod_index(s3_client, logs_bucket, pod_prefixes)
    except Exception as e:  # noqa: BLE001 - the DAG is useful without pod names
        logger.warning("Could not build step/pod index for %s: %s", workflow_name, e)
        return

    for env in environments:
        for node in env["nodes"]:
            node["pod_names"] = index.get(node["instance_name"], [])


def get_deployment_dag_records(
    client: HmdLangDeploymentClient,
    rs: RelationshipSupport,
    csd_nid: str,
    include_pods: bool = True,
) -> Dict:
    """Build the per-environment deployment DAG for a ChangeSetDeployment.

    :param client: The deployment client.
    :param rs: RelationshipSupport for entity resolution.
    :param csd_nid: The ChangeSetDeployment identifier.
    :param include_pods: When true, resolve each node's archived pod logs. This
        costs a small ranged S3 read per pod, so callers polling for status
        should request it once and cache it rather than on every poll.
    :returns: ``{"csd_id", "workflow_name", "destroy", "environments":
        [{"environment", "nodes": [...]}]}``.
    """
    csd = client.get_change_set_deployment_hmd_lang_deployment(csd_nid)
    workflow_name = getattr(csd, "workflow_name", None)

    environments: List[Dict] = []
    dag_destroy = False

    for csed_rel in client.get_from_change_set_deployment_has_change_set_env_deployment_hmd_lang_deployment(
        csd
    ):
        csed = rs.ref_to(csed_rel)

        env_rels = client.get_from_change_set_env_deployment_has_environment_hmd_lang_deployment(
            csed
        )
        if not env_rels:
            continue
        env = rs.ref_to(env_rels[0])
        env_info = EnvironmentInformation(env, client=client)

        rid_rels = client.get_from_change_set_env_deployment_has_repo_instance_deployment_hmd_lang_deployment(
            csed
        )
        repo_instance_deployment_ids = [rel.ref_to for rel in rid_rels]

        # A destroy ChangeSetDeployment is one whose deployments are DESTROY_NEXT
        # rather than DEPLOY_NEXT; this mirrors generate_local_deploy_manifest.
        rs.pull_relationship_nouns(rid_rels)
        destroy = any(rs.ref_to(rel).status == DESTROY_NEXT for rel in rid_rels)
        dag_destroy = dag_destroy or destroy

        try:
            nodes = _collect_dag_nodes(
                client,
                rs,
                env_info,
                env.type,
                repo_instance_deployment_ids,
                destroy,
            )
        except Exception as e:  # noqa: BLE001 - one bad env should not blank the view
            logger.warning("Could not build DAG for environment %s: %s", env.type, e)
            nodes = []

        environments.append({"environment": env.type, "nodes": nodes})

    if include_pods:
        _attach_pod_names(csd, environments, workflow_name)

    return {
        "csd_id": csd_nid,
        "workflow_name": workflow_name,
        "destroy": dag_destroy,
        "environments": environments,
    }
