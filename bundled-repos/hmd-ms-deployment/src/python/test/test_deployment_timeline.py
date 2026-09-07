"""Tests for get_deployment_timeline_records."""

from datetime import datetime, timedelta
from unittest.mock import patch

from hmd_schema_loader import DefaultLoader
from hmd_graphql_client.hmd_memory_client import MemoryClient
from hmd_graphql_client.relationship_support import RelationshipSupport
from hmd_lang_deployment.change_set import ChangeSet
from hmd_lang_deployment.deployment_set import DeploymentSet
from hmd_lang_deployment.environment import Environment
from hmd_lang_deployment.hmd_lang_deployment_client import HmdLangDeploymentClient
from hmd_ms_deployment.class_information import ClassInformation
from hmd_ms_deployment.deployment_manager import DeploymentManager
from hmd_ms_deployment.deployment_timeline import get_deployment_timeline_records


def _make_client():
    loader = DefaultLoader("schemas/local")
    mem_client = MemoryClient(loader, {})
    client = HmdLangDeploymentClient(mem_client)
    rs = RelationshipSupport()
    rs.register_client(mem_client)
    return client, rs


def _seed_and_apply(client, rs, tmp_path):
    class_info = ClassInformation(client)
    class_info.add_repo_version_by_name("hmd-vpc", "0.2.1", {}, {"cidr_block": "10.0.0.0/16"})
    class_info.add_repo_version_by_name("hmd-inf-s3bucket", "0.1.1", {}, {})

    env = Environment(type="local", account_number="000000000000", hmd_region="us-west-2")
    client.upsert(env)

    dep_set = DeploymentSet(
        name="local",
        definition=[{"environment": "local", "deployment_gate": {"transforms": [], "approval": False}}],
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
                "dependencies": {},
            },
        ],
    )
    client.upsert(change_set)

    dm = DeploymentManager(client, class_info)
    with patch("hmd_ms_deployment.deploy_base.EFS_MOUNT_POINT", str(tmp_path)):
        with patch("hmd_ms_deployment.deployment_manager.call_deploy_change_set_deployment"):
            result = dm.apply_changeset(change_set, dep_set, "auth_token", skip_async=True)
    return result["csd_nid"]


def test_timeline_returns_one_record_per_rid(tmp_path):
    client, rs = _make_client()
    csd_nid = _seed_and_apply(client, rs, tmp_path)

    result = get_deployment_timeline_records(client, rs, csd_nid)

    assert result["csd_id"] == csd_nid
    # deploy_time is exposed at the top level so consumers can render an
    # approximate timeline when per-RID start timestamps are missing.
    assert "deploy_time" in result
    instances = result["instances"]
    assert len(instances) == 2
    names = {r["repo_instance_name"] for r in instances}
    assert names == {"vpc", "project-bucket"}


def test_timeline_start_falls_back_to_deploy_time_when_rid_start_null(tmp_path):
    client, rs = _make_client()
    csd_nid = _seed_and_apply(client, rs, tmp_path)

    # Stamp a deploy_time on the CSD so the fallback has something to use.
    csd = client.get_change_set_deployment_hmd_lang_deployment(csd_nid)
    csd.deploy_time = datetime(2026, 5, 1, 9, 30, 0)
    client.upsert(csd)

    result = get_deployment_timeline_records(client, rs, csd_nid)
    assert result["deploy_time"] == csd.deploy_time.isoformat()

    # Fresh RIDs from apply_changeset are DEPLOY_NEXT with start=None; the
    # response should resolve start to the CSD's deploy_time and flag it as a
    # fallback so the GUI can render an "approximate" indicator.
    for rec in result["instances"]:
        assert rec["start"] == csd.deploy_time.isoformat()
        assert rec["start_is_fallback"] is True
        assert rec["end"] is None  # DEPLOY_NEXT is not terminal
        assert rec["end_is_fallback"] is False


def test_timeline_start_not_flagged_fallback_when_rid_start_set(tmp_path):
    client, rs = _make_client()
    csd_nid = _seed_and_apply(client, rs, tmp_path)

    csd = client.get_change_set_deployment_hmd_lang_deployment(csd_nid)
    csd.deploy_time = datetime(2026, 5, 1, 9, 30, 0)
    client.upsert(csd)

    # Stamp a real start on the first RID.
    rid_nid = get_deployment_timeline_records(client, rs, csd_nid)["instances"][0]["rid_nid"]
    rid = client.get_repo_instance_deployment_hmd_lang_deployment(rid_nid)
    rid.start = datetime(2026, 5, 1, 10, 0, 0)
    client.upsert(rid)

    refreshed = get_deployment_timeline_records(client, rs, csd_nid)["instances"]
    record = next(r for r in refreshed if r["rid_nid"] == rid_nid)
    assert record["start"] == rid.start.isoformat()
    assert record["start_is_fallback"] is False


def test_timeline_records_include_repo_class_and_environment(tmp_path):
    client, rs = _make_client()
    csd_nid = _seed_and_apply(client, rs, tmp_path)

    instances = get_deployment_timeline_records(client, rs, csd_nid)["instances"]
    by_name = {r["repo_instance_name"]: r for r in instances}

    assert by_name["vpc"]["repo_class_name"] == "hmd-vpc"
    assert by_name["project-bucket"]["repo_class_name"] == "hmd-inf-s3bucket"
    assert all(r["environment"] == "local" for r in instances)


def test_timeline_records_initial_status_is_deploy_next_with_fallback_start(tmp_path):
    client, rs = _make_client()
    csd_nid = _seed_and_apply(client, rs, tmp_path)

    result = get_deployment_timeline_records(client, rs, csd_nid)
    instances = result["instances"]

    # Freshly applied change sets create RIDs in DEPLOY_NEXT. The RID itself
    # has no transition timestamps yet; the timeline endpoint resolves `start`
    # to the CSD's deploy_time so the Gantt can still render a row, and flags
    # the value as a fallback so the GUI can render it as "approximate".
    assert all(r["status"] == "DEPLOY_NEXT" for r in instances)
    assert all(r["start"] == result["deploy_time"] for r in instances)
    assert all(r["start_is_fallback"] is True for r in instances)
    # DEPLOY_NEXT is not terminal, so end stays null (no _updated fallback).
    assert all(r["end"] is None for r in instances)
    assert all(r["end_is_fallback"] is False for r in instances)


def test_timeline_serializes_timestamps_as_iso(tmp_path):
    client, rs = _make_client()
    csd_nid = _seed_and_apply(client, rs, tmp_path)

    # Manually stamp start/end on one RID to verify ISO serialization.
    instances = get_deployment_timeline_records(client, rs, csd_nid)["instances"]
    rid_nid = instances[0]["rid_nid"]
    rid = client.get_repo_instance_deployment_hmd_lang_deployment(rid_nid)
    started = datetime(2026, 4, 28, 10, 0, 0)
    finished = started + timedelta(minutes=5)
    rid.start = started
    rid.end = finished
    client.upsert(rid)

    refreshed = get_deployment_timeline_records(client, rs, csd_nid)["instances"]
    record = next(r for r in refreshed if r["rid_nid"] == rid_nid)
    assert record["start"] == started.isoformat()
    assert record["end"] == finished.isoformat()
