"""Tests for NERD0006 resource-output attachment in DeployBase.get_instance_config.

An already-deployed dependency's produced Resource outputs are *baked* into its role
config as ``hmd_resources`` at generation time; a dependency being deployed in the same
ChangeSet (``DEPLOY_NEXT``) instead gets an ``hmd_resource_ref`` runtime pointer.
"""

from hmd_ms_deployment.class_information import ClassInformation
from hmd_ms_deployment.deploy_base import DeployBase
from hmd_ms_deployment.environment_information import EnvironmentInformation
from hmd_ms_deployment.resource_information import ResourceInformation

VPC_DEF = {
    "resource_namespace": "network",
    "resource_definition_name": "vpc",
    "version": "0.1.0",
}


def _instance(client, name):
    matches = client.search_repo_instance_hmd_lang_deployment(
        {"attribute": "name", "operator": "=", "value": name}
    )
    assert matches, f"no RepoInstance named {name}"
    return matches[0]


def _dev_env(client):
    envs = client.search_environment_hmd_lang_deployment(
        {"attribute": "type", "operator": "=", "value": "dev"}
    )
    assert envs
    return envs[0]


def _submit_vpc(client, rid, output, tags=None):
    ri = ResourceInformation(client)
    ri.upsert_resource_definition(
        "network", "vpc", "0.1.0", output_schema={"type": "object"}
    )
    ri.submit_resources(
        rid,
        [
            {
                "resource_name": "base-vpc",
                "resource_definition": VPC_DEF,
                "output": output,
                "tags": tags or [],
            }
        ],
    )


def test_attach_bakes_resources_for_deployed_dependency(base_environment):
    client, _ = base_environment
    db = DeployBase(client)
    ri2 = _instance(client, "ri2")
    rid = db._get_next_or_deployed(ri2)
    _submit_vpc(
        client,
        rid,
        {"vpc_id": "vpc-123", "public_subnet_ids": ["s1", "s2"]},
        tags=[{"key": "env", "value": "dev"}],
    )

    role_cfg = {}
    db._attach_dependency_resources(role_cfg, ri2, destroy=False)

    assert "hmd_resource_ref" not in role_cfg
    assert role_cfg["hmd_resources"] == [
        {
            "resource_name": "base-vpc",
            "resource_definition": VPC_DEF,
            "output": {"vpc_id": "vpc-123", "public_subnet_ids": ["s1", "s2"]},
            "tags": [{"key": "env", "value": "dev"}],
        }
    ]


def test_attach_stamps_pointer_for_deploy_next_dependency(base_environment):
    client, _ = base_environment
    db = DeployBase(client)
    env_info = EnvironmentInformation(_dev_env(client), client)
    rcv = ClassInformation(client).get_repo_class_version("rc3", "0.1.1")
    pending_ri, pending_rid = env_info.add_repo_instance(
        "ri-pending", "aaa", {}, rcv, status="DEPLOY_NEXT"
    )

    role_cfg = {}
    db._attach_dependency_resources(role_cfg, pending_ri, destroy=False)

    assert "hmd_resources" not in role_cfg
    assert role_cfg["hmd_resource_ref"] == {
        "repo_instance_deployment_id": pending_rid.identifier
    }


def test_get_instance_config_attaches_baked_resources_to_role(base_environment):
    client, _ = base_environment
    db = DeployBase(client)
    ri1 = _instance(client, "ri1")

    dep_rels = client.get_from_repo_instance_req_repo_instance_hmd_lang_deployment(ri1)
    db.rs.pull_relationship_nouns(dep_rels)
    rc1_rc2 = next(db.rs.ref_to(d) for d in dep_rels if d.role == "rc1-rc2")
    _submit_vpc(client, db._get_next_or_deployed(rc1_rc2), {"vpc_id": "vpc-abc"})

    config = db.get_instance_config(ri1, False)
    role_cfg = config["dependencies"]["rc1-rc2"]

    assert role_cfg["hmd_resources"][0]["output"] == {"vpc_id": "vpc-abc"}
    assert role_cfg["hmd_resources"][0]["resource_definition"] == VPC_DEF
    assert "hmd_resource_ref" not in role_cfg


def test_attach_skips_when_already_populated(base_environment):
    client, _ = base_environment
    db = DeployBase(client)
    ri2 = _instance(client, "ri2")

    role_cfg = {"hmd_resources": ["sentinel"]}
    db._attach_dependency_resources(role_cfg, ri2, destroy=False)

    # Idempotent: a role_cfg is a cached object shared across parents.
    assert role_cfg["hmd_resources"] == ["sentinel"]
