"""Tests for deployment_image field on RepoInstanceDeployment."""

import os
import pytest
from unittest.mock import patch

from hmd_graphql_client.hmd_memory_client import MemoryClient
from hmd_graphql_client.relationship_support import RelationshipSupport
from hmd_lang_deployment.environment import Environment
from hmd_lang_deployment.hmd_lang_deployment_client import HmdLangDeploymentClient
from hmd_ms_deployment import DEPLOYED, DEPLOY_NEXT
from hmd_ms_deployment.class_information import ClassInformation
from hmd_ms_deployment.environment_information import EnvironmentInformation
from hmd_schema_loader import DefaultLoader


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


def test_deployment_image_stored_on_repo_instance_deployment(deployment_class_only):
    """Test that HMD_APP_IMAGE is stored on RepoInstanceDeployment."""
    client, rs = make_client(deployment_class_only)

    # Set HMD_APP_IMAGE environment variable
    test_image = "ghcr.io/hmdlabs/hmd-img-projectbuilder:0.1.5"
    with patch.dict(os.environ, {"HMD_APP_IMAGE": test_image}):
        client.upsert(
            Environment(type="dev", account_number="123456789", hmd_region="reg1")
        )

        class_info = ClassInformation(client)
        env_info = EnvironmentInformation(
            client.search_environment_hmd_lang_deployment({})[0], client
        )

        # Add repo instance
        ri, rid = env_info.add_repo_instance(
            "base-vpc",
            "aaa",
            {},
            class_info.get_repo_class_version("hmd-vpc", "0.1.17"),
        )

        # Verify deployment_image is set
        assert rid.deployment_image is not None
        assert rid.deployment_image == test_image


def test_deployment_image_not_set_when_env_var_missing(deployment_class_only):
    """Test that deployment_image is not set if HMD_APP_IMAGE is not in environment."""
    client, rs = make_client(deployment_class_only)

    # Ensure HMD_APP_IMAGE is not set
    env_backup = os.environ.pop("HMD_APP_IMAGE", None)
    try:
        client.upsert(
            Environment(type="dev", account_number="123456789", hmd_region="reg1")
        )

        class_info = ClassInformation(client)
        env_info = EnvironmentInformation(
            client.search_environment_hmd_lang_deployment({})[0], client
        )

        # Add repo instance
        ri, rid = env_info.add_repo_instance(
            "base-vpc",
            "aaa",
            {},
            class_info.get_repo_class_version("hmd-vpc", "0.1.17"),
        )

        # Verify deployment_image is not set
        assert not hasattr(rid, "deployment_image") or rid.deployment_image is None
    finally:
        # Restore environment variable if it was set
        if env_backup:
            os.environ["HMD_APP_IMAGE"] = env_backup



def test_deployment_image_persists_across_updates(deployment_class_only):
    """Test that deployment_image persists when creating multiple deployments."""
    client, rs = make_client(deployment_class_only)

    test_image_1 = "ghcr.io/hmdlabs/hmd-img-projectbuilder:0.1.7"
    test_image_2 = "ghcr.io/hmdlabs/hmd-img-projectbuilder:0.1.8"

    client.upsert(
        Environment(type="dev", account_number="123456789", hmd_region="reg1")
    )

    class_info = ClassInformation(client)
    env_info = EnvironmentInformation(
        client.search_environment_hmd_lang_deployment({})[0], client
    )

    # Add first deployment with image 1
    with patch.dict(os.environ, {"HMD_APP_IMAGE": test_image_1}):
        ri, rid1 = env_info.add_repo_instance(
            "base-vpc",
            "aaa",
            {},
            class_info.get_repo_class_version("hmd-vpc", "0.1.17"),
        )
        env_info.update_instance_deployment_status(rid1, DEPLOYED)

    # Add second deployment with image 2
    with patch.dict(os.environ, {"HMD_APP_IMAGE": test_image_2}):
        ri, rid2 = env_info.add_repo_instance(
            "base-vpc",
            "aaa",
            {},
            class_info.get_repo_class_version("hmd-vpc", "0.1.17"),
        )

    # Verify both deployments have correct images
    ri_rids = client.get_from_repo_instance_has_repo_instance_deployment_hmd_lang_deployment(
        ri
    )
    deployments = [rs.ref_to(rel) for rel in ri_rids]

    # Find the deployments by status
    deployed_rid = [d for d in deployments if d.status == DEPLOYED][0]
    deploy_next_rid = [d for d in deployments if d.status == DEPLOY_NEXT][0]

    assert deployed_rid.deployment_image == test_image_1
    assert deploy_next_rid.deployment_image == test_image_2


def test_deployment_image_different_per_instance(deployment_class_only):
    """Test that different instances can have different deployment images."""
    client, rs = make_client(deployment_class_only)

    client.upsert(
        Environment(type="dev", account_number="123456789", hmd_region="reg1")
    )

    class_info = ClassInformation(client)
    env_info = EnvironmentInformation(
        client.search_environment_hmd_lang_deployment({})[0], client
    )

    # Add first instance with image 1
    test_image_1 = "ghcr.io/hmdlabs/hmd-img-projectbuilder:0.1.9"
    with patch.dict(os.environ, {"HMD_APP_IMAGE": test_image_1}):
        ri1, rid1 = env_info.add_repo_instance(
            "base-vpc",
            "aaa",
            {},
            class_info.get_repo_class_version("hmd-vpc", "0.1.17"),
        )

    # Add second instance with image 2
    test_image_2 = "ghcr.io/hmdlabs/hmd-img-projectbuilder:0.1.10"
    with patch.dict(os.environ, {"HMD_APP_IMAGE": test_image_2}):
        ri2, rid2 = env_info.add_repo_instance(
            "base-datadog",
            "aaa",
            {},
            class_info.get_repo_class_version("hmd-inf-datadog", "0.1.10"),
        )

    # Verify each has correct image
    assert rid1.deployment_image == test_image_1
    assert rid2.deployment_image == test_image_2
