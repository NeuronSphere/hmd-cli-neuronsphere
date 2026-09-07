"""Tests for get_deployment_dag_records and the step -> pod log index.

The DAG the GUI draws must be the DAG Argo ran, so the central assertion here is
that ``get_deployment_dag_records`` produces the same node and edge set as the
Argo DAG tasks built from the same changeset (see test_deploy_dags.py).
"""

import datetime
from unittest.mock import MagicMock, Mock, patch

from hmd_graphql_client.relationship_support import RelationshipSupport

import hmd_ms_deployment.deployment_manager
from hmd_lang_deployment.change_set import ChangeSet
from hmd_ms_deployment import deploy_base, deploy_workflow_creator
from hmd_ms_deployment.class_information import ClassInformation
from hmd_ms_deployment.deployment_dag import get_deployment_dag_records
from hmd_ms_deployment.deployment_manager import DeploymentManager
from hmd_ms_deployment.step_log_index import (
    build_step_pod_index,
    parse_step_marker,
    pod_name_from_prefix,
)

from test_deploy_dags import get_hmd_app_image, get_template_with_name, wkfl


def _changeset_with_deps():
    """ri1 depends on ri2 and ri3; ri2 and ri3 have no dependencies."""
    return ChangeSet(
        name="dag-tester",
        definition=[
            {
                "deployment_id": "aaa",
                "repo_instance_name": "ri3",
                "repo_class_name": "rc3",
                "repo_class_version": "0.1.1",
                "instance_configuration": {},
                "dependencies": {},
            },
            {
                "deployment_id": "aaa",
                "repo_instance_name": "ri2",
                "repo_class_name": "rc2",
                "repo_class_version": "0.1.1",
                "instance_configuration": {},
                "dependencies": {},
            },
            {
                "deployment_id": "aaa",
                "repo_instance_name": "ri1",
                "repo_class_name": "rc1",
                "repo_class_version": "0.1.1",
                "instance_configuration": {},
                "dependencies": {"rc1-rc2": ["ri2"], "rc1-rc3": ["ri3"]},
            },
        ],
    )


def _apply(client, change_set, tmp_path):
    """Apply the changeset and return (csd_nid, argo_workflow_dict)."""
    class_info = ClassInformation(client)
    dm = DeploymentManager(client, class_info)

    get_configuration_artifact_mock = Mock()
    get_configuration_artifact_mock.return_value = {"hello": "there"}
    deploy_base.get_configuration_artifact = get_configuration_artifact_mock
    deploy_workflow_creator.wkfl = wkfl

    client.upsert(change_set)

    workflow_dict = {}

    def save_workflow(wrkflow, csd):
        workflow_dict["workflow"] = wrkflow
        return {"wrkflow": "test"}

    submit_workflow_mock = MagicMock()
    submit_workflow_mock.side_effect = save_workflow
    dm.submit_workfow = submit_workflow_mock

    deployment_set = client.search_deployment_set_hmd_lang_deployment(
        {"attribute": "name", "operator": "=", "value": "dev_test"}
    )[0]

    hmd_ms_deployment.deployment_manager.call_deploy_change_set_deployment = MagicMock()
    hmd_ms_deployment.deploy_workflow_creator.okta_service_account_token_by_service = (
        MagicMock(return_value="token")
    )

    with patch(
        "hmd_ms_deployment.deploy_workflow_creator._get_hmd_app_image",
        get_hmd_app_image,
    ):
        with patch("hmd_ms_deployment.deploy_base.EFS_MOUNT_POINT", str(tmp_path)):
            with patch(
                "hmd_ms_deployment.deploy_workflow_creator.EFS_MOUNT_POINT",
                str(tmp_path),
            ):
                applied = dm.apply_changeset(change_set, deployment_set, "auth_token")
                csd_nid = applied["csd_nid"]
                dm.deploy_change_set_deployment(csd_nid, "hmdd2", "reg1")

    return csd_nid, workflow_dict["workflow"]


def _op_rel_support(client):
    """The RelationshipSupport the operation builds for itself.

    ``base_environment`` registers the HmdLangDeploymentClient facade, which has
    no ``loader``; the operation wraps the underlying base client, so tests must
    too or they exercise a shape production never sees.
    """
    return RelationshipSupport([client._base_client])


def _dev_nodes(result):
    envs = {e["environment"]: e for e in result["environments"]}
    return {n["instance_name"]: n for n in envs["dev"]["nodes"]}


def test_dag_edges_match_the_argo_dag_tasks(base_environment, tmp_path):
    """The whole point of the view: same nodes, same edges as the workflow."""
    client, _ = base_environment
    rs = _op_rel_support(client)
    csd_nid, workflow = _apply(client, _changeset_with_deps(), tmp_path)

    tasks = get_template_with_name(workflow, "deploy-dev")["dag"]["tasks"]
    argo_edges = {t["name"]: set(t["dependencies"]) for t in tasks}

    result = get_deployment_dag_records(client, rs, csd_nid, include_pods=False)
    nodes = _dev_nodes(result)
    dag_edges = {name: set(n["dependencies"]) for name, n in nodes.items()}

    assert dag_edges == argo_edges
    assert dag_edges["ri1"] == {"ri2", "ri3"}
    assert dag_edges["ri2"] == set()
    assert dag_edges["ri3"] == set()


def test_dag_nodes_carry_rid_status_and_identity(base_environment, tmp_path):
    client, _ = base_environment
    rs = _op_rel_support(client)
    csd_nid, _ = _apply(client, _changeset_with_deps(), tmp_path)

    result = get_deployment_dag_records(client, rs, csd_nid, include_pods=False)

    assert result["csd_id"] == csd_nid
    assert result["destroy"] is False

    nodes = _dev_nodes(result)
    ri1 = nodes["ri1"]
    assert ri1["repo_class_name"] == "rc1"
    assert ri1["version"] == "0.1.1"
    assert ri1["rid_nid"]
    assert ri1["deployment_id"] == "aaa"
    assert ri1["environment"] == "dev"
    # Freshly applied, so every instance is queued to deploy.
    assert ri1["status"] == "DEPLOY_NEXT"
    # pod_names is always present so consumers need not special-case it.
    assert ri1["pod_names"] == []


def test_dag_covers_every_environment_in_the_deployment_set(
    base_environment, tmp_path
):
    client, _ = base_environment
    rs = _op_rel_support(client)
    csd_nid, _ = _apply(client, _changeset_with_deps(), tmp_path)

    result = get_deployment_dag_records(client, rs, csd_nid, include_pods=False)

    assert {e["environment"] for e in result["environments"]} == {"dev", "test"}


def test_dag_does_not_require_a_workflow_name(base_environment, tmp_path):
    """A CSD that never reached Argo still has a meaningful DAG.

    get_deployment_logs raises without a workflow_name; the DAG op must not,
    since the shape and RID statuses come from the graph, not from Argo.
    """
    client, _ = base_environment
    rs = _op_rel_support(client)
    csd_nid, _ = _apply(client, _changeset_with_deps(), tmp_path)

    csd = client.get_change_set_deployment_hmd_lang_deployment(csd_nid)
    csd.workflow_name = None
    client.upsert(csd)

    result = get_deployment_dag_records(client, rs, csd_nid, include_pods=True)

    assert result["workflow_name"] is None
    assert _dev_nodes(result)["ri1"]["dependencies"] == ["ri2", "ri3"]


# ---------------------------------------------------------------- step markers


def test_parse_step_marker_reads_the_hmd_step_line():
    head = (
        "##HMD_STEP env=dev instance=ri1 rid=abc-123\n"
        "####\n"
        "rc1:ri1: 0.1.1\n"
    )
    assert parse_step_marker(head) == {
        "env": "dev",
        "instance": "ri1",
        "rid": "abc-123",
    }


def test_parse_step_marker_falls_back_to_the_legacy_echo():
    """Deployments that ran before the marker existed still resolve."""
    head = "\n####\nrc1:ri1: 0.1.1\nsome other output\n"
    marker = parse_step_marker(head)
    assert marker["instance"] == "ri1"
    assert marker["rid"] is None


def test_parse_step_marker_returns_none_when_unidentifiable():
    assert parse_step_marker("just some deploy output\nnothing useful\n") is None


def test_pod_name_from_prefix():
    assert (
        pod_name_from_prefix("argo-aaa/2026-09-03/wf-abc/wf-abc-run-script-123/")
        == "wf-abc-run-script-123"
    )


def _s3_stub(heads):
    """S3 client whose get_object returns the given head text per key."""
    s3 = MagicMock()

    def get_object(Bucket, Key, Range=None):
        if Key not in heads:
            raise KeyError(Key)
        body = MagicMock()
        body.read.return_value = heads[Key].encode("utf-8")
        return {"Body": body}

    s3.get_object.side_effect = get_object
    return s3


def test_build_step_pod_index_maps_instances_to_pods():
    prefixes = ["ns/d/wf/pod-a/", "ns/d/wf/pod-b/"]
    s3 = _s3_stub(
        {
            "ns/d/wf/pod-a/main.log": "##HMD_STEP env=dev instance=ri1 rid=r1\n",
            "ns/d/wf/pod-b/main.log": "##HMD_STEP env=dev instance=ri2 rid=r2\n",
        }
    )

    index = build_step_pod_index(s3, "bucket", prefixes)

    assert index == {"ri1": ["pod-a"], "ri2": ["pod-b"]}


def test_build_step_pod_index_keeps_every_retry_attempt():
    """run-script has retryStrategy.limit 2, so a step can archive several pods."""
    prefixes = ["ns/d/wf/pod-a/", "ns/d/wf/pod-b/"]
    s3 = _s3_stub(
        {
            "ns/d/wf/pod-a/main.log": "##HMD_STEP env=dev instance=ri1 rid=r1\n",
            "ns/d/wf/pod-b/main.log": "##HMD_STEP env=dev instance=ri1 rid=r1\n",
        }
    )

    index = build_step_pod_index(s3, "bucket", prefixes)

    assert index == {"ri1": ["pod-a", "pod-b"]}


def test_build_step_pod_index_skips_unreadable_pods():
    prefixes = ["ns/d/wf/pod-a/", "ns/d/wf/missing/"]
    s3 = _s3_stub({"ns/d/wf/pod-a/main.log": "##HMD_STEP env=dev instance=ri1 rid=r1\n"})

    index = build_step_pod_index(s3, "bucket", prefixes)

    assert index == {"ri1": ["pod-a"]}


def test_deploy_script_emits_the_step_marker(base_environment, tmp_path):
    """The marker must actually reach the generated script, or the index is empty."""
    client, _ = base_environment
    _, workflow = _apply(client, _changeset_with_deps(), tmp_path)

    tasks = get_template_with_name(workflow, "deploy-dev")["dag"]["tasks"]
    task_ri1 = [t for t in tasks if t["name"] == "ri1"][0]
    script = [
        p["value"] for p in task_ri1["arguments"]["parameters"] if p["name"] == "script"
    ][0]

    assert "##HMD_STEP env=dev instance=ri1 rid=" in script
    assert parse_step_marker(script)["instance"] == "ri1"


def test_dag_edges_survive_the_deployment_completing(base_environment, tmp_path):
    """Edges must not vanish once the steps finish.

    DeployBase.get_active_dependencies only counts a dependency whose RID is
    still in the target status with no end timestamp -- correct when generating
    a workflow, wrong for a view, since the common case is looking at a
    deployment that already ran. The DAG's edge set has to be derived from
    changeset membership, which does not change, rather than live status.
    """
    client, _ = base_environment
    rs = _op_rel_support(client)
    csd_nid, _ = _apply(client, _changeset_with_deps(), tmp_path)

    # Finish the deployment: every RID terminal, with an end timestamp.
    before = _dev_nodes(get_deployment_dag_records(client, rs, csd_nid, include_pods=False))
    for name, node in before.items():
        rid = client.get_repo_instance_deployment_hmd_lang_deployment(node["rid_nid"])
        rid.status = "DEPLOYED"
        rid.end = datetime.datetime.now(datetime.timezone.utc)
        client.upsert(rid)

    after = _dev_nodes(get_deployment_dag_records(client, rs, csd_nid, include_pods=False))

    assert set(after) == {"ri1", "ri2", "ri3"}
    assert after["ri1"]["status"] == "DEPLOYED"
    assert set(after["ri1"]["dependencies"]) == {"ri2", "ri3"}
