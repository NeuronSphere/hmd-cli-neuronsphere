from unittest.mock import Mock, MagicMock, patch

import hmd_ms_deployment.deploy_base
from hmd_schema_loader import DefaultLoader

from hmd_graphql_client.hmd_memory_client import MemoryClient
from hmd_graphql_client.relationship_support import RelationshipSupport
from hmd_lang_deployment.change_set import ChangeSet
from hmd_lang_deployment.hmd_lang_deployment_client import HmdLangDeploymentClient
from hmd_ms_deployment import (
    deploy_base,
    deploy_workflow_creator,
)
from hmd_ms_deployment.class_information import ClassInformation
from hmd_ms_deployment.deployment_manager import DeploymentManager


def make_rel_support(client):
    result = RelationshipSupport()
    result.register_client(client)
    return result


def make_client(data):
    loader = DefaultLoader("schemas/local")
    mem_client = MemoryClient(loader, data)

    client = HmdLangDeploymentClient(mem_client)
    rel_support = make_rel_support(mem_client)
    return client, rel_support


wkfl = {
    "metadata": {"generateName": "hmd-deploy-"},
    "spec": {
        "templates": [
            {},
            {
                "name": "deploy-dag",
                "dag": {"tasks": []},
            },
        ],
    },
}

def get_hmd_app_image(environment):
    return "foo:1.2.3"


def get_template_with_name(workflow, name):
    return [tmp for tmp in workflow["spec"]["templates"] if tmp["name"] == name][0]

def test_simple_deploy(base_environment, tmp_path):
    client, rs = base_environment

    class_info = ClassInformation(client)
    dm = DeploymentManager(client, class_info)

    get_configuration_artifact_mock = Mock()
    get_configuration_artifact_mock.return_value = {"hello": "there"}
    deploy_base.get_configuration_artifact = get_configuration_artifact_mock
    deploy_workflow_creator.wkfl = wkfl

    change_set = ChangeSet(
        name="tester",
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

    session_mock = MagicMock()
    # invoke_mock.return_value = {"StatusCode": "202"}
    hmd_ms_deployment.deployment_manager.call_deploy_change_set_deployment = session_mock
    hmd_ms_deployment.deploy_workflow_creator.okta_service_account_token_by_service = MagicMock(return_value="token")
    with patch("hmd_ms_deployment.deploy_workflow_creator._get_hmd_app_image", get_hmd_app_image):
        with patch("hmd_ms_deployment.deploy_base.EFS_MOUNT_POINT", str(tmp_path)):
            with patch("hmd_ms_deployment.deploy_workflow_creator.EFS_MOUNT_POINT", str(tmp_path)):
                result = dm.apply_changeset(change_set, deployment_set, "auth_token")
                result = dm.deploy_change_set_deployment(result["csd_nid"], "hmdd2", "reg1")

    workflow = workflow_dict["workflow"]

    overall = get_template_with_name(workflow, "deploy-change-set-to-deployment-set")
    steps = overall["steps"]
    assert [step["name"] for sublist in steps for step in sublist] == [
        "do-create-auth-secret",
        "update-status-started",
        "deploy-gate-dev",
        "suspend-dev",
        "deploy-gate-test",
    ]
    template_names = [temp["name"] for temp in workflow["spec"]["templates"]]
    assert "deploy-dev" in template_names
    assert "gate-dev" in template_names
    assert "deploy-test" in template_names
    assert "gate-test" in template_names

    deploy_dev_template = get_template_with_name(workflow, "deploy-dev")
    tasks = deploy_dev_template["dag"]["tasks"]
    task_ri1 = [task for task in tasks if task["name"] == "ri1"][0]
    task_ri2 = [task for task in tasks if task["name"] == "ri2"][0]
    task_ri3 = [task for task in tasks if task["name"] == "ri3"][0]

    assert len(task_ri2["dependencies"]) == 0
    assert len(task_ri3["dependencies"]) == 0
    assert len(task_ri1["dependencies"]) == 2
    assert "ri2" in task_ri1["dependencies"]
    assert "ri3" in task_ri1["dependencies"]


def test_simple_destroy(base_environment, tmp_path):
    client, rs = base_environment

    class_info = ClassInformation(client)
    dm = DeploymentManager(client, class_info)

    get_configuration_artifact_mock = Mock()
    get_configuration_artifact_mock.return_value = {"hello": "there"}
    deploy_base.get_configuration_artifact = get_configuration_artifact_mock
    deploy_workflow_creator.wkfl = wkfl
    deploy_workflow_creator.okta_service_account_token_by_service = MagicMock(return_value="token")

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

    destroy_from = ["ri1", "ri2", "ri3"]
    with patch("hmd_ms_deployment.deploy_workflow_creator._get_hmd_app_image", get_hmd_app_image):
        with patch("hmd_ms_deployment.deploy_base.EFS_MOUNT_POINT", str(tmp_path)):
            with patch("hmd_ms_deployment.deploy_workflow_creator.EFS_MOUNT_POINT", str(tmp_path)):
                with patch("hmd_ms_deployment.deployment_manager.call_deploy_destroy_changeset_deployment") as mock_async:
                    result = dm.destroy_deployment_set(deployment_set, destroy_from=destroy_from)
                # Manually call the async method to generate the workflow
                dm.deploy_destroy_changeset_deployment(
                    result["csd_nid"], "hmdp1", "reg1", destroy_from=destroy_from
                )
    workflow = workflow_dict["workflow"]

    deploy_dev_template = get_template_with_name(workflow, "deploy-dev")
    tasks = deploy_dev_template["dag"]["tasks"]

    task_ri1 = [task for task in tasks if task["name"] == "ri1"][0]
    task_ri2 = [task for task in tasks if task["name"] == "ri2"][0]
    task_ri3 = [task for task in tasks if task["name"] == "ri3"][0]

    assert len(task_ri2["dependencies"]) == 1
    assert len(task_ri3["dependencies"]) == 1
    assert len(task_ri1["dependencies"]) == 0
    assert "ri1" in task_ri2["dependencies"]
    assert "ri1" in task_ri3["dependencies"]


def test_deploy_skip(base_environment, tmp_path):
    client, rs = base_environment

    class_info = ClassInformation(client)
    dm = DeploymentManager(client, class_info)

    get_configuration_artifact_mock = Mock()
    get_configuration_artifact_mock.return_value = {"hello": "there"}
    deploy_base.get_configuration_artifact = get_configuration_artifact_mock
    deploy_workflow_creator.wkfl = wkfl

    change_set = ChangeSet(
        name="tester",
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
                "repo_instance_name": "ri1",
                "repo_class_name": "rc1",
                "repo_class_version": "0.1.1",
                "instance_configuration": {},
                "dependencies": {"rc1-rc2": ["ri2"], "rc1-rc3": ["ri3"]},
            },
        ],
    )
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
    with patch("hmd_ms_deployment.deploy_workflow_creator._get_hmd_app_image", get_hmd_app_image):
        with patch("hmd_ms_deployment.deploy_base.EFS_MOUNT_POINT", str(tmp_path)):
            with patch("hmd_ms_deployment.deploy_workflow_creator.EFS_MOUNT_POINT", str(tmp_path)):
                result = dm.apply_changeset(change_set, deployment_set, "auth_token")
                result = dm.deploy_change_set_deployment(result["csd_nid"], "hmdd2", "reg1")

    workflow = workflow_dict["workflow"]

    overall = get_template_with_name(workflow, "deploy-change-set-to-deployment-set")
    steps = overall["steps"]
    assert [step["name"] for sublist in steps for step in sublist] == [
        "do-create-auth-secret",
        "update-status-started",
        "deploy-gate-dev",
        "suspend-dev",
        "deploy-gate-test",
    ]
    template_names = [temp["name"] for temp in workflow["spec"]["templates"]]
    assert "deploy-dev" in template_names
    assert "gate-dev" in template_names
    assert "deploy-test" in template_names
    assert "gate-test" in template_names

    deploy_dev_template = get_template_with_name(workflow, "deploy-dev")
    tasks = deploy_dev_template["dag"]["tasks"]
    task_ri1 = [task for task in tasks if task["name"] == "ri1"][0]
    task_ri3 = [task for task in tasks if task["name"] == "ri3"][0]

    assert len(task_ri3["dependencies"]) == 0
    assert len(task_ri1["dependencies"]) == 1
    assert "ri3" in task_ri1["dependencies"]
