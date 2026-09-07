"""Tests for local deployment mode: skip_async and generate_local_deployment."""

import io
import os
from unittest.mock import Mock, patch, MagicMock

import pytest

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
    DESTROY_NEXT,
    CREATED,
    deployment_manager,
)
from hmd_ms_deployment.class_information import ClassInformation
from hmd_ms_deployment.deployment_manager import DeploymentManager
from hmd_ms_deployment.environment_information import EnvironmentInformation
from hmd_ms_deployment.local_deployment import generate_local_deploy_manifest


def _make_client():
    loader = DefaultLoader("schemas/local")
    mem_client = MemoryClient(loader, {})
    client = HmdLangDeploymentClient(mem_client)
    rs = RelationshipSupport()
    rs.register_client(mem_client)
    return client, rs


def _seed_simple_environment(client, rs):
    """Seed two repo classes (hmd-vpc, hmd-inf-s3bucket) and a local environment."""
    class_info = ClassInformation(client)

    # hmd-vpc has no dependencies
    class_info.add_repo_version_by_name(
        "hmd-vpc", "0.2.1", {}, {"cidr_block": "10.0.0.0/16"}
    )

    # hmd-inf-s3bucket has no dependencies (for this test)
    class_info.add_repo_version_by_name(
        "hmd-inf-s3bucket", "0.1.1", {}, {}
    )

    # Create local environment
    env = Environment(type="local", account_number="000000000000", hmd_region="us-west-2")
    client.upsert(env)

    # Create deployment set targeting local environment
    dep_set = DeploymentSet(
        name="local",
        definition=[
            {
                "environment": "local",
                "deployment_gate": {"transforms": [], "approval": False},
            },
        ],
    )
    client.upsert(dep_set)

    # Create changeset with both repos
    change_set = ChangeSet(
        name="local-changeset",
        definition=[
            {
                "deployment_id": "local",
                "repo_instance_name": "vpc",
                "repo_class_name": "hmd-vpc",
                "repo_class_version": "0.2.1",
                "instance_configuration": {},
                "dependencies": {},
            },
            {
                "deployment_id": "local",
                "repo_instance_name": "project-bucket",
                "repo_class_name": "hmd-inf-s3bucket",
                "repo_class_version": "0.1.1",
                "instance_configuration": {},
                "dependencies": {},
            },
        ],
    )
    client.upsert(change_set)

    return class_info, dep_set, change_set


class TestApplyChangesetSkipAsync:
    """Test that apply_changeset with skip_async creates entities but skips Lambda invoke."""

    def test_skip_async_creates_csd_and_rids(self, tmp_path):
        client, rs = _make_client()
        class_info, dep_set, change_set = _seed_simple_environment(client, rs)

        dm = DeploymentManager(client, class_info)

        with patch("hmd_ms_deployment.deploy_base.EFS_MOUNT_POINT", str(tmp_path)):
            with patch(
                "hmd_ms_deployment.deployment_manager.call_deploy_change_set_deployment"
            ) as mock_async:
                result = dm.apply_changeset(
                    change_set, dep_set, "auth_token", skip_async=True
                )

        # CSD should be created
        assert "csd_nid" in result

        # Lambda self-invoke should NOT have been called
        mock_async.assert_not_called()

        # Verify CSD exists with correct status
        csd = client.get_change_set_deployment_hmd_lang_deployment(result["csd_nid"])
        assert csd.csd_status == CREATED

    def test_skip_async_creates_rids_with_deploy_next(self, tmp_path):
        client, rs = _make_client()
        class_info, dep_set, change_set = _seed_simple_environment(client, rs)

        dm = DeploymentManager(client, class_info)

        with patch("hmd_ms_deployment.deploy_base.EFS_MOUNT_POINT", str(tmp_path)):
            with patch(
                "hmd_ms_deployment.deployment_manager.call_deploy_change_set_deployment"
            ):
                result = dm.apply_changeset(
                    change_set, dep_set, "auth_token", skip_async=True
                )

        csd = client.get_change_set_deployment_hmd_lang_deployment(result["csd_nid"])
        csd_cseds = client.get_from_change_set_deployment_has_change_set_env_deployment_hmd_lang_deployment(
            csd
        )
        assert len(csd_cseds) == 1  # one environment (local)

        csed = rs.ref_to(csd_cseds[0])
        csed_rids = client.get_from_change_set_env_deployment_has_repo_instance_deployment_hmd_lang_deployment(
            csed
        )
        assert len(csed_rids) == 2  # vpc + s3bucket

        for csed_rid in csed_rids:
            rid = rs.ref_to(csed_rid)
            assert rid.status == DEPLOY_NEXT

    def test_normal_apply_still_calls_async(self, tmp_path):
        """Verify default behavior (skip_async=False) still calls Lambda."""
        client, rs = _make_client()
        class_info, dep_set, change_set = _seed_simple_environment(client, rs)

        dm = DeploymentManager(client, class_info)

        with patch("hmd_ms_deployment.deploy_base.EFS_MOUNT_POINT", str(tmp_path)):
            with patch(
                "hmd_ms_deployment.deployment_manager.call_deploy_change_set_deployment"
            ) as mock_async:
                result = dm.apply_changeset(change_set, dep_set, "auth_token")

        mock_async.assert_called_once()


class TestGenerateLocalDeployment:
    """Test that generate_local_deploy_manifest returns correct scripts and node info."""

    def _apply_and_get_csd(self, client, rs, class_info, dep_set, change_set, tmp_path):
        dm = DeploymentManager(client, class_info)
        with patch("hmd_ms_deployment.deploy_base.EFS_MOUNT_POINT", str(tmp_path)):
            with patch(
                "hmd_ms_deployment.deployment_manager.call_deploy_change_set_deployment"
            ):
                result = dm.apply_changeset(
                    change_set, dep_set, "auth_token", skip_async=True
                )
        return result["csd_nid"]

    def test_returns_nodes_with_scripts(self, tmp_path):
        client, rs = _make_client()
        class_info, dep_set, change_set = _seed_simple_environment(client, rs)
        csd_nid = self._apply_and_get_csd(
            client, rs, class_info, dep_set, change_set, tmp_path
        )

        with patch("hmd_ms_deployment.deploy_base.EFS_MOUNT_POINT", str(tmp_path)):
            result = generate_local_deploy_manifest(client, rs, csd_nid)

        assert "nodes" in result
        nodes = result["nodes"]
        assert len(nodes) == 2

        instance_names = {n["instance_name"] for n in nodes}
        assert instance_names == {"vpc", "project-bucket"}

        for node in nodes:
            assert "instance_name" in node
            assert "repo_class_name" in node
            assert "version" in node
            assert "rid_nid" in node
            assert "script" in node
            assert "dependencies" in node
            # Script should contain hmd deploy command
            assert "hmd" in node["script"]

    def test_nodes_contain_correct_repo_class_names(self, tmp_path):
        client, rs = _make_client()
        class_info, dep_set, change_set = _seed_simple_environment(client, rs)
        csd_nid = self._apply_and_get_csd(
            client, rs, class_info, dep_set, change_set, tmp_path
        )

        with patch("hmd_ms_deployment.deploy_base.EFS_MOUNT_POINT", str(tmp_path)):
            result = generate_local_deploy_manifest(client, rs, csd_nid)

        nodes_by_name = {n["instance_name"]: n for n in result["nodes"]}
        assert nodes_by_name["vpc"]["repo_class_name"] == "hmd-vpc"
        assert nodes_by_name["vpc"]["version"] == "0.2.1"
        assert nodes_by_name["project-bucket"]["repo_class_name"] == "hmd-inf-s3bucket"
        assert nodes_by_name["project-bucket"]["version"] == "0.1.1"

    def test_respects_dag_order_with_dependencies(self, tmp_path):
        """When s3bucket depends on vpc, vpc should come first in node list."""
        client, rs = _make_client()
        class_info = ClassInformation(client)

        class_info.add_repo_version_by_name(
            "hmd-vpc", "0.2.1", {}, {}
        )
        class_info.add_repo_version_by_name(
            "hmd-inf-s3bucket",
            "0.1.1",
            {
                "vpc": {
                    "required": "true",
                    "version_spec": "~= 0.2",
                    "repo_class_name": "hmd-vpc",
                },
            },
            {},
        )

        env = Environment(type="local", account_number="000000000000", hmd_region="us-west-2")
        client.upsert(env)

        dep_set = DeploymentSet(
            name="local",
            definition=[
                {"environment": "local", "deployment_gate": {"transforms": [], "approval": False}},
            ],
        )
        client.upsert(dep_set)

        change_set = ChangeSet(
            name="local-changeset",
            definition=[
                {
                    "deployment_id": "local",
                    "repo_instance_name": "vpc",
                    "repo_class_name": "hmd-vpc",
                    "repo_class_version": "0.2.1",
                    "instance_configuration": {},
                    "dependencies": {},
                },
                {
                    "deployment_id": "local",
                    "repo_instance_name": "project-bucket",
                    "repo_class_name": "hmd-inf-s3bucket",
                    "repo_class_version": "0.1.1",
                    "instance_configuration": {},
                    "dependencies": {"vpc": ["vpc"]},
                },
            ],
        )
        client.upsert(change_set)

        csd_nid = self._apply_and_get_csd(
            client, rs, class_info, dep_set, change_set, tmp_path
        )

        with patch("hmd_ms_deployment.deploy_base.EFS_MOUNT_POINT", str(tmp_path)):
            result = generate_local_deploy_manifest(client, rs, csd_nid)

        nodes = result["nodes"]
        # vpc (leaf node, no deps) should come before s3bucket (depends on vpc)
        names_in_order = [n["instance_name"] for n in nodes]
        assert names_in_order.index("vpc") < names_in_order.index("project-bucket")

        # s3bucket should list vpc as a dependency
        s3_node = [n for n in nodes if n["instance_name"] == "project-bucket"][0]
        assert "vpc" in s3_node["dependencies"]


class TestLocalDestroy:
    """Destroying locally: no Argo, no async Lambda, and reverse-order scripts.

    ``destroy_deployment_set`` previously always fired an asynchronous Lambda
    invocation to build an Argo workflow -- neither of which exists in a local
    NeuronSphere. ``skip_async`` prepares the DESTROY_NEXT deployments inline so
    the CLI can pull a destroy manifest and execute it itself, exactly as
    ``apply_changeset(skip_async=True)`` already allowed for deploys.
    """

    def _deploy_everything(self, client, rs, class_info, dep_set, change_set, tmp_path):
        """Apply the changeset and mark every instance DEPLOYED."""
        dm = DeploymentManager(client, class_info)
        with patch("hmd_ms_deployment.deploy_base.EFS_MOUNT_POINT", str(tmp_path)):
            with patch(
                "hmd_ms_deployment.deployment_manager.call_deploy_change_set_deployment"
            ):
                result = dm.apply_changeset(
                    change_set, dep_set, "auth_token", skip_async=True
                )

        csd = client.get_change_set_deployment_hmd_lang_deployment(result["csd_nid"])
        csed = rs.ref_to(
            client.get_from_change_set_deployment_has_change_set_env_deployment_hmd_lang_deployment(
                csd
            )[0]
        )
        env = rs.ref_to(
            client.get_from_change_set_env_deployment_has_environment_hmd_lang_deployment(
                csed
            )[0]
        )
        env_info = EnvironmentInformation(env, client=client)
        for rel in client.get_from_change_set_env_deployment_has_repo_instance_deployment_hmd_lang_deployment(
            csed
        ):
            env_info.update_instance_deployment_status(rs.ref_to(rel), DEPLOYED)
        return dm

    def _destroy(self, dm, dep_set, destroy_from, tmp_path):
        with patch("hmd_ms_deployment.deploy_base.EFS_MOUNT_POINT", str(tmp_path)):
            with patch(
                "hmd_ms_deployment.deployment_manager."
                "call_deploy_destroy_changeset_deployment"
            ) as mock_async:
                result = dm.destroy_deployment_set(
                    dep_set, destroy_from, "auth_token", skip_async=True
                )
        mock_async.assert_not_called()
        return result

    def test_skip_async_creates_destroy_next_rids(self, tmp_path):
        client, rs = _make_client()
        class_info, dep_set, change_set = _seed_simple_environment(client, rs)
        dm = self._deploy_everything(
            client, rs, class_info, dep_set, change_set, tmp_path
        )

        result = self._destroy(dm, dep_set, ["project-bucket"], tmp_path)
        assert "csd_nid" in result

        csd = client.get_change_set_deployment_hmd_lang_deployment(result["csd_nid"])
        csed = rs.ref_to(
            client.get_from_change_set_deployment_has_change_set_env_deployment_hmd_lang_deployment(
                csd
            )[0]
        )
        rids = [
            rs.ref_to(rel)
            for rel in client.get_from_change_set_env_deployment_has_repo_instance_deployment_hmd_lang_deployment(
                csed
            )
        ]
        assert rids, "expected at least one DESTROY_NEXT deployment"
        assert all(rid.status == DESTROY_NEXT for rid in rids)

    def test_destroy_rids_keep_their_deployment_id(self, tmp_path):
        """The new RID must stay in the environment it belongs to.

        A hardcoded deployment_id would put the DESTROY_NEXT/DESTROYED record in
        a different deployment than the DEPLOYED one it replaces, so an
        environment-scoped status lookup would keep reporting the instance as
        deployed and never stop proposing to destroy it.
        """
        client, rs = _make_client()
        class_info, dep_set, change_set = _seed_simple_environment(client, rs)
        dm = self._deploy_everything(
            client, rs, class_info, dep_set, change_set, tmp_path
        )

        result = self._destroy(dm, dep_set, ["project-bucket"], tmp_path)
        csd = client.get_change_set_deployment_hmd_lang_deployment(result["csd_nid"])
        csed = rs.ref_to(
            client.get_from_change_set_deployment_has_change_set_env_deployment_hmd_lang_deployment(
                csd
            )[0]
        )
        rids = [
            rs.ref_to(rel)
            for rel in client.get_from_change_set_env_deployment_has_repo_instance_deployment_hmd_lang_deployment(
                csed
            )
        ]
        assert all(rid.deployment_id == "local" for rid in rids)

    def test_manifest_is_flagged_as_a_destroy_with_destroy_scripts(self, tmp_path):
        client, rs = _make_client()
        class_info, dep_set, change_set = _seed_simple_environment(client, rs)
        dm = self._deploy_everything(
            client, rs, class_info, dep_set, change_set, tmp_path
        )
        result = self._destroy(dm, dep_set, ["project-bucket"], tmp_path)

        with patch("hmd_ms_deployment.deploy_base.EFS_MOUNT_POINT", str(tmp_path)):
            manifest = generate_local_deploy_manifest(client, rs, result["csd_nid"])

        assert manifest["destroy"] is True
        assert manifest["nodes"], "a destroy manifest must carry nodes to run"
        for node in manifest["nodes"]:
            assert node["rid_nid"], "each destroy node needs its DESTROY_NEXT rid"
            assert "--destroy" in node["script"]

    def test_deploy_manifest_is_not_flagged_as_a_destroy(self, tmp_path):
        client, rs = _make_client()
        class_info, dep_set, change_set = _seed_simple_environment(client, rs)
        dm = DeploymentManager(client, class_info)
        with patch("hmd_ms_deployment.deploy_base.EFS_MOUNT_POINT", str(tmp_path)):
            with patch(
                "hmd_ms_deployment.deployment_manager.call_deploy_change_set_deployment"
            ):
                result = dm.apply_changeset(
                    change_set, dep_set, "auth_token", skip_async=True
                )
            manifest = generate_local_deploy_manifest(client, rs, result["csd_nid"])

        assert manifest["destroy"] is False
        for node in manifest["nodes"]:
            assert "--destroy" not in node["script"]

    def test_destroy_cascades_to_dependents(self, tmp_path):
        """Destroying an instance takes everything that depends on it."""
        client, rs = _make_client()
        class_info = ClassInformation(client)
        class_info.add_repo_version_by_name("hmd-vpc", "0.2.1", {}, {})
        class_info.add_repo_version_by_name(
            "hmd-inf-s3bucket",
            "0.1.1",
            {
                "vpc": {
                    "required": "true",
                    "version_spec": "~= 0.2",
                    "repo_class_name": "hmd-vpc",
                },
            },
            {},
        )
        env = Environment(
            type="local", account_number="000000000000", hmd_region="us-west-2"
        )
        client.upsert(env)
        dep_set = DeploymentSet(
            name="local",
            definition=[
                {
                    "environment": "local",
                    "deployment_gate": {"transforms": [], "approval": False},
                },
            ],
        )
        client.upsert(dep_set)
        change_set = ChangeSet(
            name="local-changeset",
            definition=[
                {
                    "deployment_id": "local",
                    "repo_instance_name": "vpc",
                    "repo_class_name": "hmd-vpc",
                    "repo_class_version": "0.2.1",
                    "instance_configuration": {},
                    "dependencies": {},
                },
                {
                    "deployment_id": "local",
                    "repo_instance_name": "project-bucket",
                    "repo_class_name": "hmd-inf-s3bucket",
                    "repo_class_version": "0.1.1",
                    "instance_configuration": {},
                    "dependencies": {"vpc": ["vpc"]},
                },
            ],
        )
        client.upsert(change_set)

        dm = self._deploy_everything(
            client, rs, class_info, dep_set, change_set, tmp_path
        )

        # Dry run first: this is the closure the CLI's cascade guard checks.
        closure = dm.destroy_deployment_set_dry_run(dep_set, ["vpc"])
        assert set(closure["local"]) == {"vpc", "project-bucket"}
