"""Tests for the bundled base ResourceDefinition catalog (NERD0004).

The base catalog is a set of abstract, vendor-neutral supertypes shipped with the
service (namespaced under ``*.neuronsphere.io``) that per-repo concrete definitions
``parent`` off of. These tests validate the bundled YAML, the parent-first loader
ordering, idempotent seeding, the inheritance edges, and end-to-end substitutability.
"""

import pytest

from hmd_graphql_client.hmd_memory_client import MemoryClient
from hmd_graphql_client.relationship_support import RelationshipSupport
from hmd_lang_deployment.environment import Environment
from hmd_lang_deployment.hmd_lang_deployment_client import HmdLangDeploymentClient
from hmd_ms_deployment.class_information import ClassInformation
from hmd_ms_deployment.environment_information import EnvironmentInformation
from hmd_ms_deployment.resource_information import (
    BASE_RESOURCES_DIR,
    ResourceInformation,
    load_base_resource_documents,
    _def_key,
)
from hmd_schema_loader import DefaultLoader


EXPECTED_BASE_COUNT = 21


def make_client():
    loader = DefaultLoader("schemas/local")
    mem_client = MemoryClient(loader, {})
    client = HmdLangDeploymentClient(mem_client)
    rs = RelationshipSupport()
    rs.register_client(client)
    return client, rs


# --------------------------------------------------------------------------- #
# Bundled documents
# --------------------------------------------------------------------------- #


def test_base_resources_dir_exists_and_is_populated():
    files = list(BASE_RESOURCES_DIR.glob("*.yaml"))
    assert len(files) == EXPECTED_BASE_COUNT


def test_all_documents_qualified_under_neuronsphere_io():
    for doc in load_base_resource_documents():
        assert doc["resource_namespace"].endswith(".neuronsphere.io"), doc
        assert doc["version"] == "0.1.0"


def test_every_parent_ref_resolves_within_catalog():
    docs = load_base_resource_documents()
    keys = {_def_key(doc) for doc in docs}
    for doc in docs:
        parent = doc.get("parent")
        if parent is not None:
            assert _def_key(parent) in keys, doc


def test_loader_orders_parents_before_children():
    ordered = load_base_resource_documents()
    seen = set()
    for doc in ordered:
        parent = doc.get("parent")
        if parent is not None:
            assert _def_key(parent) in seen, (
                f"{_def_key(doc)} emitted before its parent {_def_key(parent)}"
            )
        seen.add(_def_key(doc))


# --------------------------------------------------------------------------- #
# Seeding
# --------------------------------------------------------------------------- #


def test_seed_upserts_full_catalog():
    client, _ = make_client()
    ri = ResourceInformation(client)

    seeded = ri.seed_base_resource_definitions()

    assert len(seeded) == EXPECTED_BASE_COUNT
    all_rds = client.search_resource_definition_hmd_lang_deployment({})
    assert len(all_rds) == EXPECTED_BASE_COUNT


def test_seed_is_idempotent():
    client, _ = make_client()
    ri = ResourceInformation(client)

    ri.seed_base_resource_definitions()
    ri.seed_base_resource_definitions()

    all_rds = client.search_resource_definition_hmd_lang_deployment({})
    assert len(all_rds) == EXPECTED_BASE_COUNT


def test_seed_creates_inheritance_edges():
    client, _ = make_client()
    ri = ResourceInformation(client)
    ri.seed_base_resource_definitions()

    for child_name in ("postgres", "graph-database"):
        child = ri.find_resource_definition(
            "database.neuronsphere.io", child_name, "0.1.0"
        )
        ancestry = [rd.resource_definition_name for rd in ri.get_ancestry(child)]
        assert ancestry == ["database", child_name]

    microservice = ri.find_resource_definition(
        "application.neuronsphere.io", "microservice", "0.1.0"
    )
    ancestry = [rd.resource_definition_name for rd in ri.get_ancestry(microservice)]
    assert ancestry == ["application", "microservice"]

    # Both a cloud vpc and the local docker-network inherit from the generic
    # network supertype, so either can satisfy a generic `network` requirement.
    for child_name in ("vpc", "docker-network"):
        child = ri.find_resource_definition(
            "network.neuronsphere.io", child_name, "0.1.0"
        )
        ancestry = [rd.resource_definition_name for rd in ri.get_ancestry(child)]
        assert ancestry == ["network", child_name]


def test_seed_effective_output_schema_merges_parent():
    """A subtype's effective schema includes the parent's properties (SPEC0002)."""
    client, _ = make_client()
    ri = ResourceInformation(client)
    ri.seed_base_resource_definitions()

    postgres = ri.find_resource_definition(
        "database.neuronsphere.io", "postgres", "0.1.0"
    )
    schema = ri.get_effective_output_schema(postgres)
    props = schema.get("properties", {})
    # from database (parent)
    assert "host" in props and "port" in props
    # from postgres (child)
    assert "database_name" in props and "secret_name" in props


# --------------------------------------------------------------------------- #
# Kubernetes-native supertypes (operator / CRD / ingress-controller / Service)
# --------------------------------------------------------------------------- #


def test_kubernetes_native_base_types_present():
    """The five K8s-native supertypes ship in the catalog."""
    names = {
        (doc["resource_namespace"], doc["resource_definition_name"])
        for doc in load_base_resource_documents()
    }
    for name in (
        "deployment",
        "service",
        "operator",
        "custom-resource-definition",
        "ingress-controller",
    ):
        assert ("kubernetes.neuronsphere.io", name) in names


def test_service_carries_internal_dns():
    """The Service supertype exposes the cluster-internal URL primitive."""
    client, _ = make_client()
    ri = ResourceInformation(client)
    ri.seed_base_resource_definitions()

    service = ri.find_resource_definition(
        "kubernetes.neuronsphere.io", "service", "0.1.0"
    )
    props = ri.get_effective_output_schema(service).get("properties", {})
    assert "internal_dns" in props


def test_ingress_controller_inherits_full_chain():
    """ingress-controller -> operator -> deployment: the effective schema merges the
    whole chain, and the ancestry is ordered root-first."""
    client, _ = make_client()
    ri = ResourceInformation(client)
    ri.seed_base_resource_definitions()

    ic = ri.find_resource_definition(
        "kubernetes.neuronsphere.io", "ingress-controller", "0.1.0"
    )
    ancestry = [rd.resource_definition_name for rd in ri.get_ancestry(ic)]
    assert ancestry == ["deployment", "operator", "ingress-controller"]

    props = ri.get_effective_output_schema(ic).get("properties", {})
    # from deployment (root)
    assert "name" in props and "namespace" in props and "service_account" in props
    # from operator (mid)
    assert "watched_api_groups" in props and "irsa_role_arn" in props
    # from ingress-controller (leaf)
    assert "ingress_class" in props


# --------------------------------------------------------------------------- #
# Per-repo produced definitions validate their rendered outputs
# --------------------------------------------------------------------------- #

# Sample outputs captured from `helm template --show-only` of each repo's
# resource-outputs ConfigMap (Part C), validated against the effective schema of the
# concrete definition parented to the new K8s-native base types.
_REPO_PRODUCED = [
    {
        "namespace": "external-secrets.neuronsphere.io",
        "name": "external-secrets-crds",
        "parent": ("kubernetes.neuronsphere.io", "custom-resource-definition"),
        "output": {
            "api_group": "external-secrets.io",
            "kinds": ["ExternalSecret", "SecretStore", "ClusterSecretStore"],
            "scope": "Cluster",
            "served_versions": ["v1beta1", "v1"],
        },
    },
    {
        "namespace": "aws.neuronsphere.io",
        "name": "aws-load-balancer-controller",
        "parent": ("kubernetes.neuronsphere.io", "ingress-controller"),
        "output": {
            "name": "rel-aws-load-balancer-controller",
            "namespace": "alb-aaa",
            "service_account": "alb-aaa",
            "ingress_class": "alb",
            "irsa_role_arn": "arn:aws:iam::123456789012:role/alb-aaa-alb-ctrl-role",
        },
    },
    {
        "namespace": "external-secrets.neuronsphere.io",
        "name": "external-secrets-operator",
        "parent": ("kubernetes.neuronsphere.io", "operator"),
        "output": {
            "name": "rel-external-secrets",
            "namespace": "es-aaa",
            "service_account": "es-aaa",
            "cluster_secret_store_names": ["aws-secrets-manager", "aws-parameter-store"],
            "webhook_internal_dns": "rel-external-secrets-webhook.es-aaa.svc.cluster.local",
        },
    },
]


@pytest.mark.parametrize("spec", _REPO_PRODUCED, ids=[s["name"] for s in _REPO_PRODUCED])
def test_repo_produced_output_validates_against_effective_schema(spec):
    """Each inf repo's concrete definition (parenting a K8s-native base type) accepts
    its rendered output against the inherited effective schema."""
    client, _ = make_client()
    ri = ResourceInformation(client)
    ri.seed_base_resource_definitions()

    parent_ns, parent_name = spec["parent"]
    rd = ri.upsert_resource_definition(
        spec["namespace"],
        spec["name"],
        "0.1.0",
        parent_ref={
            "resource_namespace": parent_ns,
            "resource_definition_name": parent_name,
            "version": "0.1.0",
        },
    )

    # Must not raise: the rendered output conforms to the merged (inherited) schema.
    ri._validate_output(spec["output"], rd)


# --------------------------------------------------------------------------- #
# End-to-end substitutability
# --------------------------------------------------------------------------- #


def _make_env(client, type_="dev"):
    env = Environment(type=type_, account_number="1", hmd_region="reg1")
    client.upsert(env)
    return EnvironmentInformation(env, client)


def test_subtype_producer_satisfies_base_requirement():
    """A provider producing a concrete subtype (parenting a seeded base type)
    satisfies a requirement expressed against the base type."""
    client, _ = make_client()
    ri = ResourceInformation(client)
    ri.seed_base_resource_definitions()

    # Provider repo ships a concrete subtype parenting the base kubernetes-cluster.
    eks = ri.upsert_resource_definition(
        "aws.neuronsphere.io",
        "eks-cluster",
        "0.1.0",
        parent_ref={
            "resource_namespace": "kubernetes.neuronsphere.io",
            "resource_definition_name": "kubernetes-cluster",
            "version": "0.1.0",
        },
    )

    class_info = ClassInformation(client)
    class_info.add_repo_version_by_name("eks-provider", "0.1.1", {}, {})
    provider_cv = class_info.get_repo_class_version("eks-provider", "0.1.1")
    ri.declare_produces(provider_cv, eks)

    env_info = _make_env(client)
    env_info.add_repo_instance("my-eks", "aaa", {}, provider_cv, status="DEPLOYED")
    env_info.load_environment()

    base_ref = {
        "resource_namespace": "kubernetes.neuronsphere.io",
        "resource_definition_name": "kubernetes-cluster",
        "version": "0.1.0",
    }
    names = {i.name for i in ri.find_satisfying_instances(env_info, base_ref)}
    assert names == {"my-eks"}
