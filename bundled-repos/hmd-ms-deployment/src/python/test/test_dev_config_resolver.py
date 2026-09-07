"""Tests for the ad-hoc dev-loop deploy-config resolver.

The resolver is definition-driven (NERD0004 SPEC0008): a role resolves from its own
declared ``resource`` requirement to the local Resources whose ResourceDefinition
satisfies that type (exact or subtype), honoring ``version_spec``/``tag_selector``.
Roles with no resolvable resource requirement scaffold.
"""

import pytest

from hmd_graphql_client.hmd_memory_client import MemoryClient
from hmd_lang_deployment.environment import Environment
from hmd_lang_deployment.hmd_lang_deployment_client import HmdLangDeploymentClient
from hmd_ms_deployment.class_information import ClassInformation
from hmd_ms_deployment.dev_config_resolver import generate_dev_deployment_config
from hmd_ms_deployment.environment_information import EnvironmentInformation
from hmd_ms_deployment.resource_information import ResourceInformation
from hmd_schema_loader import DefaultLoader


def _tags(**kv):
    return [{"key": k, "value": v} for k, v in kv.items()]


def _rreq(
    ns,
    name,
    version="0.1.0",
    version_spec=None,
    tag_selector=None,
    repo_class_name=None,
):
    """A manifest dependency declaring an authoritative SPEC0008 ``resource`` block."""
    dep = {}
    if repo_class_name:
        dep["repo_class_name"] = repo_class_name
    resource = {
        "resource_namespace": ns,
        "resource_definition_name": name,
        "version": version,
    }
    if version_spec:
        resource["version_spec"] = version_spec
    if tag_selector:
        resource["tag_selector"] = tag_selector
    dep["resource"] = resource
    return dep


def _setup_local_env():
    """A MemoryClient with a `local` env, a DEPLOYED `local-k3s` owner, and the
    default local Resources (kubernetes-cluster, postgres DB, ms-deployment
    microservice, and a Traefik ingress-controller plus an nginx-ingress subtype)
    tagged environment=local."""
    loader = DefaultLoader("schemas/local")
    client = HmdLangDeploymentClient(MemoryClient(loader, {}))
    ri_info = ResourceInformation(client)
    for ns, name in (
        ("kubernetes.neuronsphere.io", "kubernetes-cluster"),
        ("compute.neuronsphere.io", "compute-node"),
        ("database.neuronsphere.io", "postgres"),
        ("application.neuronsphere.io", "microservice"),
        ("kubernetes.neuronsphere.io", "ingress-controller"),
    ):
        ri_info.upsert_resource_definition(
            ns, name, "0.1.0", output_schema={"type": "object"}
        )
    # A concrete subtype that isa the abstract ingress-controller (mirrors the cloud
    # aws-load-balancer-controller producer), so subtype matching can be exercised.
    ri_info.upsert_resource_definition(
        "networking.neuronsphere.io",
        "nginx-ingress",
        "0.1.0",
        output_schema={"type": "object"},
        parent_ref={
            "resource_namespace": "kubernetes.neuronsphere.io",
            "resource_definition_name": "ingress-controller",
            "version": "0.1.0",
        },
    )

    class_info = ClassInformation(client)
    class_info.add_repo_version_by_name("hmd-cli-neuronsphere", "0.1.0", {}, {})

    env = Environment(type="local", account_number="000000000000", hmd_region="reg1")
    client.upsert(env)
    env_info = EnvironmentInformation(env, client)
    _ri, rid = env_info.add_repo_instance(
        "local-k3s",
        "local",
        {},
        class_info.get_repo_class_version("hmd-cli-neuronsphere", "0.1.0"),
        status="DEPLOYED",
    )

    ri_info.submit_resources(
        rid,
        [
            {
                "resource_name": "neuronsphere",
                "resource_definition": {
                    "resource_namespace": "kubernetes.neuronsphere.io",
                    "resource_definition_name": "kubernetes-cluster",
                    "version": "0.1.0",
                },
                "output": {
                    "cluster_name": "neuronsphere",
                    "endpoint": "https://floci-eks-neuronsphere:6443",
                },
                "tags": _tags(environment="local"),
            },
            {
                "resource_name": "local-db-deployment_gui",
                "resource_definition": {
                    "resource_namespace": "database.neuronsphere.io",
                    "resource_definition_name": "postgres",
                    "version": "0.1.0",
                },
                "output": {
                    "host": "hmd_db",
                    "port": 5432,
                    "database_name": "deployment_gui",
                    "secret_name": "sec",
                    "engine_version": "15",
                },
                "tags": _tags(environment="local", database="deployment_gui"),
            },
            {
                "resource_name": "local-service-hmd_ms_deployment",
                "resource_definition": {
                    "resource_namespace": "application.neuronsphere.io",
                    "resource_definition_name": "microservice",
                    "version": "0.1.0",
                },
                "output": {"api_base_url": "http://localhost/hmd_ms_deployment"},
                "tags": _tags(environment="local", repo_class="hmd-ms-deployment"),
            },
            {
                "resource_name": "neuronsphere-traefik",
                "resource_definition": {
                    "resource_namespace": "kubernetes.neuronsphere.io",
                    "resource_definition_name": "ingress-controller",
                    "version": "0.1.0",
                },
                "output": {
                    "name": "traefik",
                    "namespace": "kube-system",
                    "ingress_class": "traefik",
                },
                "tags": _tags(environment="local", cluster_type="k3s"),
            },
            {
                "resource_name": "nginx-lb",
                "resource_definition": {
                    "resource_namespace": "networking.neuronsphere.io",
                    "resource_definition_name": "nginx-ingress",
                    "version": "0.1.0",
                },
                "output": {"ingress_class": "nginx"},
                "tags": _tags(environment="local"),
            },
        ],
    )

    reloaded = EnvironmentInformation(env, client)
    reloaded.load_environment()
    return client, reloaded


def _resolve(deps, default_configuration=None):
    client, env_info = _setup_local_env()
    return generate_dev_deployment_config(
        client,
        env_info,
        instance_name="dev-app",
        default_configuration=default_configuration or {},
        dependencies=deps,
    )


def _resource_names(role_cfg):
    return {r["resource_name"] for r in role_cfg.get("hmd_resources", [])}


def test_resolves_kubernetes_cluster_role_from_resource_block():
    cfg = _resolve(
        {
            "eks-cluster": _rreq(
                "kubernetes.neuronsphere.io",
                "kubernetes-cluster",
                repo_class_name="hmd-inf-eks-cluster",
            )
        }
    )
    resources = cfg["dependencies"]["eks-cluster"]["hmd_resources"]
    assert resources
    assert resources[0]["output"]["cluster_name"] == "neuronsphere"
    assert (
        resources[0]["resource_definition"]["resource_definition_name"]
        == "kubernetes-cluster"
    )


def test_resolves_db_credentials_via_postgres_resource():
    cfg = _resolve(
        {
            "db-credentials": _rreq(
                "database.neuronsphere.io",
                "postgres",
                repo_class_name="hmd-database-account",
            )
        }
    )
    out = cfg["dependencies"]["db-credentials"]["hmd_resources"][0]["output"]
    assert out["database_name"] == "deployment_gui"
    assert out["host"] == "hmd_db"
    assert out["secret_name"] == "sec"


def test_resolves_eks_alb_via_ingress_controller_resource():
    """The eks-alb role, declaring the ingress-controller resource type, resolves to
    the local Traefik Resource (the core scenario)."""
    cfg = _resolve(
        {
            "eks-alb": _rreq(
                "kubernetes.neuronsphere.io",
                "ingress-controller",
                version_spec="~= 0.1",
                repo_class_name="hmd-inf-eks-alb",
            )
        }
    )
    names = _resource_names(cfg["dependencies"]["eks-alb"])
    assert "neuronsphere-traefik" in names
    traefik = [
        r
        for r in cfg["dependencies"]["eks-alb"]["hmd_resources"]
        if r["resource_name"] == "neuronsphere-traefik"
    ][0]
    assert traefik["output"]["ingress_class"] == "traefik"


def test_subtype_resource_satisfies_supertype_requirement():
    """A requirement for the abstract ingress-controller is also satisfied by a
    Resource typed as a subtype (nginx-ingress isa ingress-controller)."""
    cfg = _resolve(
        {"eks-alb": _rreq("kubernetes.neuronsphere.io", "ingress-controller")}
    )
    names = _resource_names(cfg["dependencies"]["eks-alb"])
    assert {"neuronsphere-traefik", "nginx-lb"} <= names


def test_supertype_resource_does_not_satisfy_subtype_requirement():
    """Direction matters: requiring the subtype nginx-ingress must NOT match the
    parent-typed Traefik Resource."""
    cfg = _resolve(
        {"eks-alb": _rreq("networking.neuronsphere.io", "nginx-ingress")}
    )
    names = _resource_names(cfg["dependencies"]["eks-alb"])
    assert names == {"nginx-lb"}


def test_tag_selector_matches_and_filters():
    matched = _resolve(
        {
            "db-credentials": _rreq(
                "database.neuronsphere.io",
                "postgres",
                tag_selector="database=deployment_gui",
            )
        }
    )
    assert _resource_names(matched["dependencies"]["db-credentials"]) == {
        "local-db-deployment_gui"
    }

    filtered = _resolve(
        {
            "db-credentials": _rreq(
                "database.neuronsphere.io",
                "postgres",
                tag_selector="database=other",
            )
        }
    )
    assert filtered["dependencies"]["db-credentials"]["_scaffold"] is True


def test_no_resource_block_scaffolds():
    """A class-only dep (no resource block) is not table-resolved -- it scaffolds."""
    cfg = _resolve({"eks-cluster": {"repo_class_name": "hmd-inf-eks-cluster"}})
    role_cfg = cfg["dependencies"]["eks-cluster"]
    assert role_cfg["_scaffold"] is True
    assert role_cfg["hmd_resources"] == []


def test_no_matching_resource_scaffolds():
    """A resource requirement with no matching local Resource scaffolds."""
    cfg = _resolve(
        {"okta-app": _rreq("okta.neuronsphere.io", "okta-application")}
    )
    role_cfg = cfg["dependencies"]["okta-app"]
    assert role_cfg["_scaffold"] is True
    assert role_cfg["hmd_resources"] == []


def test_default_configuration_passthrough():
    cfg = _resolve({}, default_configuration={"replicaCount": 2, "config": {"a": "b"}})
    assert cfg["replicaCount"] == 2
    assert cfg["config"] == {"a": "b"}
    assert cfg["instance_name"] == "dev-app"


def test_serialize_resource_parity():
    """serialize_resource of one Resource matches its entry from the per-rid list."""
    client, env_info = _setup_local_env()
    ri_info = ResourceInformation(client)
    rid = client.search_repo_instance_deployment_hmd_lang_deployment({})[0]
    per_rid = ri_info.serialize_resources_for_deployment(rid)
    assert per_rid
    for entry in per_rid:
        resources = ri_info.find_resources_by_tag("environment", "local")
        match = [r for r in resources if r.resource_name == entry["resource_name"]]
        assert match
        assert ri_info.serialize_resource(match[0]) == entry
