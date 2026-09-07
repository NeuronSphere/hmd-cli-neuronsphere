from unittest.mock import Mock, patch

from hmd_schema_loader import DefaultLoader

from hmd_graphql_client.hmd_memory_client import MemoryClient
from hmd_graphql_client.relationship_support import RelationshipSupport
from hmd_lang_deployment.change_set import ChangeSet
from hmd_lang_deployment.change_set_deployment import ChangeSetDeployment
from hmd_lang_deployment.deployment_set import DeploymentSet
from hmd_lang_deployment.environment import Environment
from hmd_lang_deployment.hmd_lang_deployment_client import HmdLangDeploymentClient
from hmd_ms_deployment import (
    DEPLOYED,
    DEPLOY_NEXT,
    deployment_manager,
    DESTROY_NEXT,
    DESTROYED,
)
from hmd_ms_deployment.class_information import ClassInformation
from hmd_ms_deployment.deployment_manager import DeploymentManager
from hmd_ms_deployment.environment_information import (
    EnvironmentInformation,
)


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


def test_simple(deployment_class_only, tmp_path):
    client, rs = make_client(deployment_class_only)

    client.upsert(
        Environment(type="dev", account_number="123456789", hmd_region="reg1")
    )

    client.upsert(
        Environment(type="test", account_number="987654321", hmd_region="reg1")
    )

    dep_set = DeploymentSet(
        name="dev_test",
        definition=[
            {
                "environment": "dev",
                "deployment_gate": {"transforms": [], "approval": True},
            },
            {
                "environment": "test",
                "deployment_gate": {"transforms": [], "approval": True},
            },
        ],
    )

    client.upsert(dep_set)

    change_set = ChangeSet(
        name="my_change",
        definition=[
            {
                "deployment_id": "aaa",
                "repo_instance_name": "base-vpc",
                "repo_class_name": "hmd-vpc",
                "repo_class_version": "0.1.17",
                "dependencies": {},
                "instance_configuration": {"azs": ["us-west-2a", "us-west-2b"]},
            },
            {
                "deployment_id": "aaa",
                "repo_instance_name": "core-acm",
                "repo_class_name": "hmd-inf-acm",
                "repo_class_version": "0.1.3",
                "dependencies": {},
                "instance_configuration": {},
            },
        ],
    )
    client.upsert(change_set)

    class_info = ClassInformation(client)
    dm = DeploymentManager(client, class_info)

    requests_mock = Mock()
    requests_mock.post.return_value.status_code = 200
    requests_mock.post.return_value.json.return_value = {}
    deployment_manager.requests = requests_mock

    hmd_cli_tools_mock = Mock()
    hmd_cli_tools_mock.hmd_cli_tools.get_secret.return_value = "test bearer"
    deployment_manager.get_secret = hmd_cli_tools_mock

    with patch("hmd_ms_deployment.deploy_base.EFS_MOUNT_POINT", str(tmp_path)) as _:
        with patch(
            "hmd_ms_deployment.deployment_manager.call_deploy_change_set_deployment"
        ) as mock_async:
            dm.apply_changeset(change_set, dep_set, "auth_token")

    csds = client.get_to_change_set_deployment_has_change_set_hmd_lang_deployment(
        change_set
    )
    assert len(csds) == 1
    csd: ChangeSetDeployment = rs.ref_from(csds[0])
    assert csd.csd_status == "CREATED"
    csd_cseds = client.get_from_change_set_deployment_has_change_set_env_deployment_hmd_lang_deployment(
        csd
    )
    assert len(csd_cseds) == 2

    for csed in (rs.ref_to(csd_csed) for csd_csed in csd_cseds):
        csed_rids = client.get_from_change_set_env_deployment_has_repo_instance_deployment_hmd_lang_deployment(
            csed
        )
        assert len(csed_rids) == 2
        env = rs.ref_to(
            client.get_from_change_set_env_deployment_has_environment_hmd_lang_deployment(
                csed
            )[
                0
            ]
        )
        env_info = EnvironmentInformation(env, client)
        for rid in (rs.ref_to(csed_rid) for csed_rid in csed_rids):
            assert rid.status == DEPLOY_NEXT
            env_info.update_instance_deployment_status(rid, DEPLOYED)

    # Sync phase: destroy_deployment_set now requires destroy_from and returns quickly
    # RIDs are created in the async path (deploy_destroy_changeset_deployment)
    with patch("hmd_ms_deployment.deploy_base.EFS_MOUNT_POINT", str(tmp_path)) as _:
        with patch(
            "hmd_ms_deployment.deployment_manager.call_deploy_destroy_changeset_deployment"
        ) as mock_async:
            result = dm.destroy_deployment_set(
                dep_set, destroy_from=["base-vpc", "core-acm"]
            )

    # Verify sync phase returns CSD ID immediately
    assert "csd_nid" in result
    csd_nid = result["csd_nid"]

    # Verify async was called with correct parameters
    mock_async.assert_called_once()
    call_args = mock_async.call_args
    assert call_args[0][0] == csd_nid  # identifier
    assert call_args[0][2] == ["base-vpc", "core-acm"]  # destroy_from

    csds = client.search_change_set_deployment_hmd_lang_deployment({})
    max_time = max(csd.deploy_time for csd in csds)
    csd = [csd for csd in csds if csd.deploy_time == max_time][0]
    assert csd.csd_status == "CREATED"
    csd_cseds = client.get_from_change_set_deployment_has_change_set_env_deployment_hmd_lang_deployment(
        csd
    )
    assert len(csd_cseds) == 2

    # After sync phase, CSEDs exist but have NO RIDs yet (created in async)
    for csed in (rs.ref_to(csd_csed) for csd_csed in csd_cseds):
        csed_rids = client.get_from_change_set_env_deployment_has_repo_instance_deployment_hmd_lang_deployment(
            csed
        )
        assert len(csed_rids) == 0  # No RIDs yet - created in async path

    # Async phase: simulate calling deploy_destroy_changeset_deployment
    with patch("hmd_ms_deployment.deploy_base.EFS_MOUNT_POINT", str(tmp_path)) as _:
        with patch.object(dm, "generate_deploy_workflow") as mock_gen:
            with patch.object(dm, "submit_workfow") as mock_submit:
                mock_gen.return_value = {"metadata": {"name": "test-workflow"}}
                mock_submit.return_value = {"workflow_name": "test-workflow"}
                dm.deploy_destroy_changeset_deployment(
                    csd_nid, "hmd", "reg1", destroy_from=["base-vpc", "core-acm"]
                )

    # After async phase, RIDs should be created with DESTROY_NEXT status
    for csed in (rs.ref_to(csd_csed) for csd_csed in csd_cseds):
        csed_rids = client.get_from_change_set_env_deployment_has_repo_instance_deployment_hmd_lang_deployment(
            csed
        )
        assert len(csed_rids) == 2
        env = rs.ref_to(
            client.get_from_change_set_env_deployment_has_environment_hmd_lang_deployment(
                csed
            )[0]
        )
        env_info = EnvironmentInformation(env, client)
        for rid in (rs.ref_to(csed_rid) for csed_rid in csed_rids):
            assert rid.status == DESTROY_NEXT
            env_info.update_instance_deployment_status(rid, DESTROYED)


def test_destroy_from(deployment_hmdp1, tmp_path):
    client, rs = make_client(deployment_hmdp1)

    class_info = ClassInformation(client)
    dm = DeploymentManager(client, class_info)

    hmd_cli_tools_mock = Mock()
    hmd_cli_tools_mock.hmd_cli_tools.get_secret.return_value = "test bearer"
    deployment_manager.get_secret = hmd_cli_tools_mock

    dep_set = DeploymentSet(
        identifier="ds1",
        name="dev_test",
        definition=[
            {
                "environment": "admin",
                "deployment_gate": {"transforms": [], "approval": True},
            },
        ],
    )
    client.upsert(dep_set)  # Persist for async path lookup

    ris = client.search_repo_instance_hmd_lang_deployment({})
    destroyed = [ri.name for ri in ris]
    assert dm.destroy_deployment_set_dry_run(dep_set) == {
        "admin": sorted(destroyed)
    }

    destroyed = [
        "argo",
        "efs-csi",
        "eks-alb",
        "eks-base",
        "eks-datadog",
        "ms-deployment",
    ]
    assert dm.destroy_deployment_set_dry_run(dep_set, ["eks-base"]) == {
        "admin": sorted(destroyed)
    }

    # Sync phase: destroy_deployment_set returns quickly with CSD ID
    # RIDs are created in async path
    with patch("hmd_ms_deployment.deploy_base.EFS_MOUNT_POINT", str(tmp_path)) as _:
        with patch(
            "hmd_ms_deployment.deployment_manager.call_deploy_destroy_changeset_deployment"
        ) as mock_async:
            result = dm.destroy_deployment_set(dep_set, destroy_from=["eks-base"])

    # Verify sync phase returns CSD ID immediately
    assert "csd_nid" in result
    csd_nid = result["csd_nid"]

    # Verify async was called with correct destroy_from
    mock_async.assert_called_once()
    call_args = mock_async.call_args
    assert call_args[0][2] == ["eks-base"]  # destroy_from

    # After sync phase, NO RIDs with DESTROY_NEXT status yet
    rids = client.search_repo_instance_deployment_hmd_lang_deployment({})
    assert len([rid for rid in rids if rid.status == DESTROY_NEXT]) == 0

    # Async phase: simulate calling deploy_destroy_changeset_deployment
    with patch("hmd_ms_deployment.deploy_base.EFS_MOUNT_POINT", str(tmp_path)) as _:
        with patch.object(dm, "generate_deploy_workflow") as mock_gen:
            with patch.object(dm, "submit_workfow") as mock_submit:
                mock_gen.return_value = {"metadata": {"name": "test-workflow"}}
                mock_submit.return_value = {"workflow_name": "test-workflow"}
                dm.deploy_destroy_changeset_deployment(
                    csd_nid, "hmd", "reg1", destroy_from=["eks-base"]
                )

    # After async phase, RIDs should be created with DESTROY_NEXT status
    rids = client.search_repo_instance_deployment_hmd_lang_deployment({})
    assert len([rid for rid in rids if rid.status == DESTROY_NEXT]) == len(destroyed)


def test_destroy_requires_destroy_from(deployment_class_only, tmp_path):
    """Test that destroy_deployment_set requires destroy_from parameter."""
    import pytest

    client, rs = make_client(deployment_class_only)

    client.upsert(
        Environment(type="dev", account_number="123456789", hmd_region="reg1")
    )

    dep_set = DeploymentSet(
        name="dev_test",
        definition=[
            {
                "environment": "dev",
                "deployment_gate": {"transforms": [], "approval": True},
            },
        ],
    )
    client.upsert(dep_set)

    class_info = ClassInformation(client)
    dm = DeploymentManager(client, class_info)

    # Should raise ValueError when destroy_from is not provided
    with pytest.raises(ValueError, match="destroy_from is required"):
        dm.destroy_deployment_set(dep_set, destroy_from=None)

    # Should raise ValueError when destroy_from is empty list
    with pytest.raises(ValueError, match="destroy_from is required"):
        dm.destroy_deployment_set(dep_set, destroy_from=[])


def test_deploy_destroy_changeset_deployment_requires_destroy_from(
    deployment_class_only, tmp_path
):
    """Test that deploy_destroy_changeset_deployment requires destroy_from parameter."""
    import pytest

    client, rs = make_client(deployment_class_only)

    class_info = ClassInformation(client)
    dm = DeploymentManager(client, class_info)

    # Should raise ValueError when destroy_from is not provided
    with pytest.raises(ValueError, match="destroy_from is required"):
        dm.deploy_destroy_changeset_deployment(
            "csd_123", "hmd", "reg1", destroy_from=None
        )

    # Should raise ValueError when destroy_from is empty list
    with pytest.raises(ValueError, match="destroy_from is required"):
        dm.deploy_destroy_changeset_deployment(
            "csd_123", "hmd", "reg1", destroy_from=[]
        )


def test_deploy_change_set_deployment_not_found_raises_service_exception():
    """A csd_nid that never resolves raises a clear ServiceException, not a masked
    AttributeError, and does not attempt to mark a None deployment FAILED."""
    import pytest

    from hmd_cli_tools import ServiceException

    client = Mock()
    client.get_change_set_deployment_hmd_lang_deployment.return_value = None

    dm = DeploymentManager(client, Mock())

    with patch.object(dm, "update_change_set_deployment_status") as mock_update:
        with patch("hmd_ms_deployment.deployment_manager.time.sleep") as mock_sleep:
            with pytest.raises(ServiceException, match="csd_missing"):
                dm.deploy_change_set_deployment("csd_missing", "hmd", "reg1")

    # 1 initial fetch + 5 retries
    assert client.get_change_set_deployment_hmd_lang_deployment.call_count == 6
    assert mock_sleep.call_count == 5
    # Never try to update status on a None deployment (the source of the old AttributeError)
    mock_update.assert_not_called()


def test_deploy_change_set_deployment_retries_until_visible():
    """When the entity write has not yet propagated, the fetch is retried and, once
    visible, deployment proceeds past the None guard with a real csd."""
    import pytest

    csd = Mock()
    client = Mock()
    # None until the third fetch (simulates read-after-write propagation delay)
    client.get_change_set_deployment_hmd_lang_deployment.side_effect = [None, None, csd]
    # Short-circuit the heavy workflow body with a sentinel so we only exercise the guard
    client.get_from_change_set_deployment_has_deployment_set_hmd_lang_deployment.side_effect = (
        RuntimeError("reached-body")
    )

    dm = DeploymentManager(client, Mock())

    with patch.object(dm, "update_change_set_deployment_status") as mock_update:
        with patch("hmd_ms_deployment.deployment_manager.time.sleep") as mock_sleep:
            with pytest.raises(RuntimeError, match="reached-body"):
                dm.deploy_change_set_deployment("csd_123", "hmd", "reg1")

    assert client.get_change_set_deployment_hmd_lang_deployment.call_count == 3
    assert mock_sleep.call_count == 2
    # csd was non-None on the except path, so status is marked FAILED with the real entity
    mock_update.assert_called_once()
    assert mock_update.call_args[0][0] is csd
