import os
from json import load
from typing import Tuple

import pytest

from hmd_graphql_client.relationship_support import RelationshipSupport
from hmd_lang_deployment.deployment_set import DeploymentSet
from hmd_lang_deployment.environment import Environment

from hmd_lang_deployment.hmd_lang_deployment_client import HmdLangDeploymentClient

from hmd_graphql_client.hmd_memory_client import MemoryClient
from hmd_schema_loader import DefaultLoader

from hmd_ms_deployment.class_information import ClassInformation
os.environ["HMD_INSTANCE_NAME"] = "deployment"
os.environ["HMD_REPO_NAME"] = "deployment"
os.environ["HMD_REPO_VERSION"] = "0.1.2"
os.environ["HMD_DID"] = "aaa"
os.environ["HMD_REGION"] = "reg1"
os.environ["HMD_CUSTOMER_CODE"] = "hmd"
os.environ["HMD_ENVIRONMENT"] = "dev"
os.environ["HMD_APP_IMAGE"] = "appimage"
os.environ["HMD_DEPLOYMENT_IMAGE"] = "deployment-image"
os.environ["ARGO_INSTANCE_NAME"] = "argo"
os.environ["ARGO_DEPLOYMENT_ID"] = "aaa"
os.environ["DEPLOYMENT_STORAGE_CLASS_NAME"] = "dpl_storage_class"
os.environ["DEPLOYMENT_PVC_NAME"] = "dpl_pvc"
os.environ["DEPLOYMENT_PV_NAME"] = "dpl_pv"
os.environ["AWS_LAMBDA_FUNCTION_NAME"] = "deployment"
from hmd_ms_deployment.environment_information import EnvironmentInformation



@pytest.fixture()
def test_simple():
    with open("./test/test_simple.json", "r") as fl:
        data = load(fl)
    return data


@pytest.fixture()
def test_destroy():
    with open("./test/test_destroy.json", "r") as fl:
        data = load(fl)
    return data


@pytest.fixture()
def deployment_class_only():
    with open("./test/deployment_class_only.json", "r") as fl:
        data = load(fl)
    return data


@pytest.fixture()
def test_deploy_skip():
    with open("./test/test_deploy_skip.json", "r") as fl:
        data = load(fl)
    return data


@pytest.fixture()
def deployment_hmdp1():
    with open("./test/deployment_hmdp1.json", "r") as fl:
        data = load(fl)
    return data


@pytest.fixture()
def base_environment() -> Tuple[HmdLangDeploymentClient, RelationshipSupport]:
    loader = DefaultLoader("schemas/local")
    mem_client = MemoryClient(loader, {})
    client = HmdLangDeploymentClient(mem_client)
    rs = RelationshipSupport()
    rs.register_client(client)

    class_info = ClassInformation(client)
    class_info.add_repo_version_by_name("rc3", "0.1.1", {}, {"config_rc3": "value_rc3"})
    class_info.add_repo_version_by_name("rc2", "0.1.1", {}, {"config_rc2": "value_rc2"})
    class_info.add_repo_version_by_name(
        "rc1",
        "0.1.1",
        {
            "rc1-rc2": {
                "required": "true",
                "version_spec": "~= 0.1",
                "repo_class_name": "rc2",
            },
            "rc1-rc3": {
                "required": "false",
                "version_spec": "~= 0.1",
                "repo_class_name": "rc3",
            },
        },
        {"config_rc1": "value_rc1"},
    )

    new_env = Environment(
        **{
            "type": "dev",
            "account_number": "123456789",
            "hmd_region": "reg1",
        }
    )

    client.upsert(new_env)
    env_info = EnvironmentInformation(new_env, client)

    ri2, rid2 = env_info.add_repo_instance(
        "ri2",
        "aaa",
        {"config_ri2": "value_ri2"},
        class_info.get_repo_class_version("rc2", "0.1.1"),
        status="DEPLOYED",
    )
    ri3, rid3 = env_info.add_repo_instance(
        "ri3",
        "aaa",
        {"config_ri3": "value_ri3"},
        class_info.get_repo_class_version("rc3", "0.1.1"),
        status="DEPLOYED",
    )
    ri1, rid1 = env_info.add_repo_instance(
        "ri1",
        "aaa",
        {"config_ri2": "value_ri2"},
        class_info.get_repo_class_version("rc1", "0.1.1"),
        dependencies={
            "rc1-rc2": [ri2],
            "rc1-rc3": [ri3],
        },
        status="DEPLOYED",
    )

    new_env = Environment(
        **{
            "type": "test",
            "account_number": "987654321",
            "hmd_region": "reg1",
        }
    )

    client.upsert(new_env)
    env_info = EnvironmentInformation(new_env, client)

    ri2, rid2 = env_info.add_repo_instance(
        "ri2",
        "aaa",
        {"config_ri2": "value_ri2"},
        class_info.get_repo_class_version("rc2", "0.1.1"),
        status="DEPLOYED",
    )
    ri3, rid3 = env_info.add_repo_instance(
        "ri3",
        "aaa",
        {"config_ri3": "value_ri3"},
        class_info.get_repo_class_version("rc3", "0.1.1"),
        status="DEPLOYED",
    )
    ri1, rid1 = env_info.add_repo_instance(
        "ri1",
        "aaa",
        {"config_ri2": "value_ri2"},
        class_info.get_repo_class_version("rc1", "0.1.1"),
        dependencies={
            "rc1-rc2": [ri2],
            "rc1-rc3": [ri3],
        },
        status="DEPLOYED",
    )
    deployment_set = DeploymentSet(
        name= "dev_test",
        definition=[
            {
                "environment": "dev",
                "deployment_gate": {
                    "transforms": [
                        {
                            "apply_to": "ri1",
                            "transform": {
                                "image_name": "a_transform_image",
                                "tag": "0.1.2",
                            },
                            "artifact_ref": "my_test_artifact@0.5.6:build",
                        },
                    ],
                    "approval": True,
                },
            },
            {
                "environment": "test",
                "deployment_gate": {
                    "transforms": [
                        {
                            "apply_to": "ri1",
                            "transform": {
                                "image_name": "a_transform_image",
                                "tag": "0.1.2",
                            },
                            "artifact_ref": "my_test_artifact@0.5.6:build",
                        },
                    ],
                    "approval": True,
                },
            },
        ],
    )
    client.upsert(deployment_set)

    return client, rs