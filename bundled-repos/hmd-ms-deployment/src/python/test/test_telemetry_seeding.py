"""Tests for telemetry-debug URL discovery used to seed service profiles on deploy."""

from types import SimpleNamespace
from unittest.mock import MagicMock

from hmd_ms_deployment.deploy_base import DeployBase


def _make_base(instances):
    """Build a DeployBase whose environment resolves to ``instances``.

    ``instances`` is a list of ``(repo_class_name, current_rid_or_None)`` tuples.
    """
    client = MagicMock()
    rs = MagicMock()

    env_rels = []
    ref_map = {}
    isa_map = {}
    rid_map = {}
    for i, (rcn, rid) in enumerate(instances):
        rel = SimpleNamespace(idx=i)
        instance = SimpleNamespace(name=f"inst{i}")
        env_rels.append(rel)
        ref_map[id(rel)] = instance

        isa_rel = SimpleNamespace(idx=i)
        repo_class = SimpleNamespace(repo_class_name=rcn)
        isa_map[id(instance)] = [isa_rel]
        ref_map[id(isa_rel)] = repo_class
        rid_map[id(instance)] = rid

    client.get_from_environment_has_repo_instance_hmd_lang_deployment.return_value = (
        env_rels
    )
    client.get_from_repo_instance_isa_repo_class_hmd_lang_deployment.side_effect = (
        lambda inst: isa_map.get(id(inst), [])
    )
    rs.ref_to.side_effect = lambda rel: ref_map[id(rel)]
    rs.pull_relationship_nouns.return_value = None

    base = DeployBase(client, rs)
    base._get_current_rid = lambda inst: rid_map.get(id(inst))
    return base


def test_url_discovered_prod_has_no_env_suffix():
    rid = SimpleNamespace(deployment_id="aaa", hmd_region=None)
    base = _make_base([("hmd-inf-eks-cluster", None), ("hmd-ms-telemetry-debug", rid)])
    env = SimpleNamespace(type="prod", hmd_region="reg1")

    url = base.get_telemetry_debug_url(env, "cust")

    assert url == "https://inst1-aaa-reg1.cust-neuronsphere.io"


def test_url_has_env_suffix_for_non_prod():
    rid = SimpleNamespace(deployment_id="aaa", hmd_region="reg1")
    base = _make_base([("hmd-ms-telemetry-debug", rid)])
    env = SimpleNamespace(type="dev1", hmd_region="reg1")

    url = base.get_telemetry_debug_url(env, "cust")

    assert url == "https://inst0-aaa-reg1.cust-dev1-neuronsphere.io"


def test_rid_region_overrides_environment_region():
    rid = SimpleNamespace(deployment_id="bbb", hmd_region="reg2")
    base = _make_base([("hmd-ms-telemetry-debug", rid)])
    env = SimpleNamespace(type="admin", hmd_region="reg1")

    url = base.get_telemetry_debug_url(env, "cust")

    assert url == "https://inst0-bbb-reg2.cust-admin-neuronsphere.io"


def test_url_none_when_no_telemetry_instance():
    base = _make_base([("hmd-inf-eks-cluster", None)])
    env = SimpleNamespace(type="prod", hmd_region="reg1")

    assert base.get_telemetry_debug_url(env, "cust") is None


def test_url_none_when_telemetry_not_current():
    # instance present but no current RepoInstanceDeployment (e.g. first-time
    # deploy in the same change set) -> not yet available -> skip seeding.
    base = _make_base([("hmd-ms-telemetry-debug", None)])
    env = SimpleNamespace(type="prod", hmd_region="reg1")

    assert base.get_telemetry_debug_url(env, "cust") is None


def test_url_none_best_effort_on_error():
    client = MagicMock()
    client.get_from_environment_has_repo_instance_hmd_lang_deployment.side_effect = (
        RuntimeError("db down")
    )
    base = DeployBase(client, MagicMock())
    env = SimpleNamespace(type="prod", hmd_region="reg1")

    assert base.get_telemetry_debug_url(env, "cust") is None
