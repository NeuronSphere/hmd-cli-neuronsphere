import re
from collections import Counter

import pytest

from hmd_cli_tools import ServiceException
from hmd_graphql_client.hmd_memory_client import MemoryClient
from hmd_graphql_client.relationship_support import RelationshipSupport
from hmd_lang_deployment.environment import Environment
from hmd_lang_deployment.hmd_lang_deployment_client import HmdLangDeploymentClient
from hmd_ms_deployment import DEPLOYED, DEPLOY_NEXT, SKIPPED
from hmd_ms_deployment.class_information import ClassInformation
from hmd_ms_deployment.environment_information import (
    EnvironmentInformation,
)
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


def test_simple(deployment_class_only):
    client, rs = make_client(deployment_class_only)

    client.upsert(
        Environment(type="dev", account_number="123456789", hmd_region="reg1")
    )

    class_info = ClassInformation(client)
    env_info = EnvironmentInformation(
        client.search_environment_hmd_lang_deployment({})[0], client
    )

    env_info.add_repo_instance(
        "base-vpc",
        "aaa",
        {},
        class_info.get_repo_class_version("hmd-vpc", "0.1.17"),
    )

    env_ris = client.get_from_environment_has_repo_instance_hmd_lang_deployment(
        env_info.environment
    )
    assert len(env_ris) == 1
    vpc_ri = rs.ref_to(env_ris[0])

    ri_rids = (
        client.get_from_repo_instance_has_repo_instance_deployment_hmd_lang_deployment(
            vpc_ri
        )
    )
    assert len(ri_rids) == 1
    ri_rid = ri_rids[0]
    assert ri_rid.current == "false"
    assert rs.ref_to(ri_rid).status == DEPLOY_NEXT

    # Add another instance and make sure the first one is now skipped...
    env_info.add_repo_instance(
        "base-vpc",
        "aaa",
        {},
        class_info.get_repo_class_version("hmd-vpc", "0.1.17"),
    )

    env_ris = client.get_from_environment_has_repo_instance_hmd_lang_deployment(
        env_info.environment
    )
    assert len(env_ris) == 1
    vpc_ri = rs.ref_to(env_ris[0])

    ri_rids = (
        client.get_from_repo_instance_has_repo_instance_deployment_hmd_lang_deployment(
            vpc_ri
        )
    )
    assert len(ri_rids) == 2
    counter = Counter((ri_rid.current, rs.ref_to(ri_rid).status) for ri_rid in ri_rids)
    assert counter[("false", DEPLOY_NEXT)] == 1
    assert counter[("false", SKIPPED)] == 1
    rid = [
        rs.ref_to(ri_rid)
        for ri_rid in ri_rids
        if rs.ref_to(ri_rid).status == DEPLOY_NEXT
    ][0]

    # mark the newest instance as deployed...
    env_info.update_instance_deployment_status(rid, DEPLOYED)
    ri_rids = (
        client.get_from_repo_instance_has_repo_instance_deployment_hmd_lang_deployment(
            vpc_ri
        )
    )
    assert len(ri_rids) == 2
    counter = Counter((ri_rid.current, rs.ref_to(ri_rid).status) for ri_rid in ri_rids)
    assert counter[("true", DEPLOYED)] == 1
    assert counter[("false", SKIPPED)] == 1

    # Add another instance...
    env_info.add_repo_instance(
        "base-vpc",
        "aaa",
        {},
        class_info.get_repo_class_version("hmd-vpc", "0.1.17"),
    )

    env_ris = client.get_from_environment_has_repo_instance_hmd_lang_deployment(
        env_info.environment
    )
    assert len(env_ris) == 1
    vpc_ri = rs.ref_to(env_ris[0])

    ri_rids = (
        client.get_from_repo_instance_has_repo_instance_deployment_hmd_lang_deployment(
            vpc_ri
        )
    )
    assert len(ri_rids) == 3
    counter = Counter((ri_rid.current, rs.ref_to(ri_rid).status) for ri_rid in ri_rids)
    assert counter[("true", DEPLOYED)] == 1
    assert counter[("false", DEPLOY_NEXT)] == 1
    assert counter[("false", SKIPPED)] == 1
    rid = [
        rs.ref_to(ri_rid)
        for ri_rid in ri_rids
        if rs.ref_to(ri_rid).status == DEPLOY_NEXT
    ][0]

    dd_ri, rid = env_info.add_repo_instance(
        "base-datadog",
        "aaa",
        {},
        class_info.get_repo_class_version("hmd-inf-datadog", "0.1.10"),
    )
    env_info.update_instance_deployment_status(rid, DEPLOYED)

    ddl_ri, rid = env_info.add_repo_instance(
        "datadog-lambda",
        "aaa",
        {},
        class_info.get_repo_class_version("hmd-inf-datadog-lambdas", "0.1.5"),
        dependencies={"datadog": [dd_ri]},
    )
    env_info.update_instance_deployment_status(rid, DEPLOYED)

    with pytest.raises(
        ServiceException,
        match=re.escape(
            "For RepoInstance, core-rds, required role, base-vpc, not provided."
        ),
    ):
        env_info.add_repo_instance(
            "core-rds",
            "aaa",
            {},
            class_info.get_repo_class_version("hmd-postgres-rds", "0.2.33"),
            dependencies={"datadog-lambda": [ddl_ri]},
        )

    with pytest.raises(
        ServiceException,
        match=re.escape(
            "For RepoInstance, core-rds, role with name, extra, not in RepoClassVersion dependencies. (400)"
        ),
    ):
        env_info.add_repo_instance(
            "core-rds",
            "aaa",
            {},
            class_info.get_repo_class_version("hmd-postgres-rds", "0.2.33"),
            dependencies={
                "datadog-lambda": [ddl_ri],
                "base-vpc": [vpc_ri],
                "extra": [vpc_ri],
            },
        )

    rds_ri, rid = env_info.add_repo_instance(
        "core-rds",
        "aaa",
        {},
        class_info.get_repo_class_version("hmd-postgres-rds", "0.2.33"),
        dependencies={
            "datadog-lambda": [ddl_ri],
            "base-vpc": [vpc_ri],
        },
    )

    ri_ris = client.get_from_repo_instance_req_repo_instance_hmd_lang_deployment(rds_ri)
    assert len(ri_ris) == 2

    class_info.add_repo_version_by_name(
        "hmd-postgres-rds",
        "0.2.34",
        {
            "datadog-lambda": {
                "repo_class_name": "hmd-inf-datadog-lambdas",
                "version_spec": "~= 0.1",
                "required": "true",
            }
        },
        {},
    )

    rds_ri, rid = env_info.add_repo_instance(
        "core-rds",
        "aaa",
        {},
        class_info.get_repo_class_version("hmd-postgres-rds", "0.2.34"),
        dependencies={
            "datadog-lambda": [ddl_ri],
        },
    )

    ri_ris = client.get_from_repo_instance_req_repo_instance_hmd_lang_deployment(rds_ri)
    assert len(ri_ris) == 1

    rds_ri, rid = env_info.add_repo_instance(
        "core-rds",
        "aaa",
        {},
        class_info.get_repo_class_version("hmd-postgres-rds", "0.2.33"),
        dependencies={
            "datadog-lambda": [ddl_ri],
            "base-vpc": [vpc_ri],
        },
    )

    ri_ris = client.get_from_repo_instance_req_repo_instance_hmd_lang_deployment(rds_ri)
    assert len(ri_ris) == 2


def test_skipped_cascade_populates_start_and_end_timestamps(deployment_class_only):
    """A predecessor RID forced to SKIPPED gets bookend timestamps so the
    Timeline panel can render it instead of dropping the row."""
    client, rs = make_client(deployment_class_only)
    client.upsert(
        Environment(type="dev", account_number="123456789", hmd_region="reg1")
    )
    class_info = ClassInformation(client)
    env_info = EnvironmentInformation(
        client.search_environment_hmd_lang_deployment({})[0], client
    )

    rcv = class_info.get_repo_class_version("hmd-vpc", "0.1.17")
    env_info.add_repo_instance("base-vpc", "aaa", {}, rcv)
    env_info.add_repo_instance("base-vpc", "aaa", {}, rcv)

    env_ris = client.get_from_environment_has_repo_instance_hmd_lang_deployment(
        env_info.environment
    )
    vpc_ri = rs.ref_to(env_ris[0])
    ri_rids = (
        client.get_from_repo_instance_has_repo_instance_deployment_hmd_lang_deployment(
            vpc_ri
        )
    )
    skipped_rids = [
        rs.ref_to(ri_rid)
        for ri_rid in ri_rids
        if rs.ref_to(ri_rid).status == SKIPPED
    ]
    assert len(skipped_rids) == 1
    skipped = skipped_rids[0]
    assert skipped.start is not None, "SKIPPED RID must have a start timestamp"
    assert skipped.end is not None, "SKIPPED RID must have an end timestamp"


def test_instance_names_to_refs_plain(deployment_class_only):
    """Test instance_names_to_refs with plain instance names (backwards compatible)."""
    client, rs = make_client(deployment_class_only)

    client.upsert(
        Environment(type="dev", account_number="123456789", hmd_region="reg1")
    )

    class_info = ClassInformation(client)
    env_info = EnvironmentInformation(
        client.search_environment_hmd_lang_deployment({})[0], client
    )

    # Add two instances
    env_info.add_repo_instance(
        "base-vpc",
        "aaa",
        {},
        class_info.get_repo_class_version("hmd-vpc", "0.1.17"),
    )
    env_info.add_repo_instance(
        "base-datadog",
        "aaa",
        {},
        class_info.get_repo_class_version("hmd-inf-datadog", "0.1.10"),
    )

    # Test plain instance name format
    deps = {"vpc": "base-vpc", "monitoring": "base-datadog"}
    result = env_info.instance_names_to_refs(deps)

    assert "vpc" in result
    assert "monitoring" in result
    assert len(result["vpc"]) == 1
    assert len(result["monitoring"]) == 1
    assert result["vpc"][0].name == "base-vpc"
    assert result["monitoring"][0].name == "base-datadog"


def test_instance_names_to_refs_shorthand(deployment_class_only):
    """Test instance_names_to_refs with shorthand resource names (NERD0002)."""
    client, rs = make_client(deployment_class_only)

    client.upsert(
        Environment(type="dev", account_number="123456789", hmd_region="reg1")
    )

    class_info = ClassInformation(client)
    env_info = EnvironmentInformation(
        client.search_environment_hmd_lang_deployment({})[0], client
    )

    # Add instance
    vpc_ri, vpc_rid = env_info.add_repo_instance(
        "base-vpc",
        "aaa",
        {},
        class_info.get_repo_class_version("hmd-vpc", "0.1.17"),
    )
    env_info.update_instance_deployment_status(vpc_rid, DEPLOYED)

    # Test shorthand resource name format: ns:<instance_name>:<deployment_id>
    deps = {"vpc": "ns:base-vpc:aaa"}
    result = env_info.instance_names_to_refs(deps)

    assert "vpc" in result
    assert len(result["vpc"]) == 1
    assert result["vpc"][0].name == "base-vpc"


def test_instance_names_to_refs_full(deployment_class_only):
    """Test instance_names_to_refs with full resource names (NERD0002)."""
    client, rs = make_client(deployment_class_only)

    client.upsert(
        Environment(type="dev", account_number="123456789", hmd_region="reg1")
    )

    class_info = ClassInformation(client)
    env_info = EnvironmentInformation(
        client.search_environment_hmd_lang_deployment({})[0], client
    )

    # Add instance
    vpc_ri, vpc_rid = env_info.add_repo_instance(
        "base-vpc",
        "aaa",
        {},
        class_info.get_repo_class_version("hmd-vpc", "0.1.17"),
    )
    env_info.update_instance_deployment_status(vpc_rid, DEPLOYED)

    # Test full resource name format
    deps = {"vpc": "ns:cust01:dev:hmd-vpc:0.1.17:base-vpc:aaa"}
    result = env_info.instance_names_to_refs(deps)

    assert "vpc" in result
    assert len(result["vpc"]) == 1
    assert result["vpc"][0].name == "base-vpc"


def test_instance_names_to_refs_mixed_formats(deployment_class_only):
    """Test instance_names_to_refs with mixed resource name formats."""
    client, rs = make_client(deployment_class_only)

    client.upsert(
        Environment(type="dev", account_number="123456789", hmd_region="reg1")
    )

    class_info = ClassInformation(client)
    env_info = EnvironmentInformation(
        client.search_environment_hmd_lang_deployment({})[0], client
    )

    # Add instances
    vpc_ri, vpc_rid = env_info.add_repo_instance(
        "base-vpc",
        "aaa",
        {},
        class_info.get_repo_class_version("hmd-vpc", "0.1.17"),
    )
    env_info.update_instance_deployment_status(vpc_rid, DEPLOYED)

    s3_ri, s3_rid = env_info.add_repo_instance(
        "base-s3",
        "aaa",
        {},
        class_info.get_repo_class_version("hmd-inf-s3bucket", "0.1.7"),
    )
    env_info.update_instance_deployment_status(s3_rid, DEPLOYED)

    dd_ri, dd_rid = env_info.add_repo_instance(
        "base-datadog",
        "aaa",
        {},
        class_info.get_repo_class_version("hmd-inf-datadog", "0.1.10"),
    )
    env_info.update_instance_deployment_status(dd_rid, DEPLOYED)

    # Mix of all three formats
    deps = {
        "vpc": "base-vpc",  # Plain
        "storage": "ns:base-s3:aaa",  # Shorthand
        "monitoring": "ns:cust01:dev:hmd-inf-datadog:0.1.10:base-datadog:aaa",  # Full
    }
    result = env_info.instance_names_to_refs(deps)

    assert len(result) == 3
    assert result["vpc"][0].name == "base-vpc"
    assert result["storage"][0].name == "base-s3"
    assert result["monitoring"][0].name == "base-datadog"


def test_instance_names_to_refs_list_values(deployment_class_only):
    """Test instance_names_to_refs with list values (multiple dependencies per role)."""
    client, rs = make_client(deployment_class_only)

    client.upsert(
        Environment(type="dev", account_number="123456789", hmd_region="reg1")
    )

    class_info = ClassInformation(client)
    env_info = EnvironmentInformation(
        client.search_environment_hmd_lang_deployment({})[0], client
    )

    # Add instances
    vpc_ri, vpc_rid = env_info.add_repo_instance(
        "base-vpc",
        "aaa",
        {},
        class_info.get_repo_class_version("hmd-vpc", "0.1.17"),
    )
    env_info.update_instance_deployment_status(vpc_rid, DEPLOYED)

    dd_ri, dd_rid = env_info.add_repo_instance(
        "base-datadog",
        "aaa",
        {},
        class_info.get_repo_class_version("hmd-inf-datadog", "0.1.10"),
    )
    env_info.update_instance_deployment_status(dd_rid, DEPLOYED)

    # Test with list of dependencies using different formats
    deps = {
        "infrastructure": ["base-vpc", "ns:base-datadog:aaa"],
    }
    result = env_info.instance_names_to_refs(deps)

    assert "infrastructure" in result
    assert len(result["infrastructure"]) == 2
    assert result["infrastructure"][0].name == "base-vpc"
    assert result["infrastructure"][1].name == "base-datadog"


def test_instance_names_to_refs_nonexistent_instance(deployment_class_only):
    """Test instance_names_to_refs with nonexistent instance raises assertion."""
    client, rs = make_client(deployment_class_only)

    client.upsert(
        Environment(type="dev", account_number="123456789", hmd_region="reg1")
    )

    class_info = ClassInformation(client)
    env_info = EnvironmentInformation(
        client.search_environment_hmd_lang_deployment({})[0], client
    )

    # Try to reference nonexistent instance
    deps = {"vpc": "nonexistent-instance"}

    with pytest.raises(AssertionError, match="No repo instance found"):
        env_info.instance_names_to_refs(deps)
