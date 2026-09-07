import pytest

from hmd_cli_tools import ServiceException
from hmd_graphql_client.hmd_memory_client import MemoryClient
from hmd_graphql_client.relationship_support import RelationshipSupport
from hmd_lang_deployment.hmd_lang_deployment_client import HmdLangDeploymentClient
from hmd_ms_deployment.class_information import ClassInformation
from hmd_ms_deployment.resource_information import ResourceInformation
from hmd_schema_loader import DefaultLoader


def make_client():
    loader = DefaultLoader("schemas/local")
    mem_client = MemoryClient(loader, {})
    client = HmdLangDeploymentClient(mem_client)
    rs = RelationshipSupport()
    rs.register_client(client)
    return client, rs


def _rid_from_base_environment(client):
    """Return a RepoInstanceDeployment from the base_environment fixture."""
    rids = client.search_repo_instance_deployment_hmd_lang_deployment({})
    assert rids
    return rids[0]


# --------------------------------------------------------------------------- #
# ResourceDefinition management
# --------------------------------------------------------------------------- #


def test_upsert_resource_definition_creates_and_is_idempotent():
    client, _ = make_client()
    ri = ResourceInformation(client)

    rd1 = ri.upsert_resource_definition(
        "kubernetes",
        "kubernetes-cluster",
        "0.1.0",
        description="A k8s cluster",
        output_schema={"type": "object"},
    )
    rd2 = ri.upsert_resource_definition(
        "kubernetes",
        "kubernetes-cluster",
        "0.1.0",
        description="Updated description",
    )

    assert rd1.identifier == rd2.identifier
    all_rds = client.search_resource_definition_hmd_lang_deployment({})
    assert len(all_rds) == 1
    assert ri.find_resource_definition(
        "kubernetes", "kubernetes-cluster", "0.1.0"
    ).description == "Updated description"


def test_resource_definition_isa_parent_link_and_replacement():
    client, _ = make_client()
    ri = ResourceInformation(client)

    ri.upsert_resource_definition("kubernetes", "kubernetes-cluster", "0.1.0")
    ri.upsert_resource_definition("kubernetes", "openshift-cluster", "0.1.0")
    child = ri.upsert_resource_definition(
        "aws",
        "eks-cluster",
        "0.1.0",
        parent_ref={
            "resource_namespace": "kubernetes",
            "resource_definition_name": "kubernetes-cluster",
            "version": "0.1.0",
        },
    )

    parent = ri._get_parent(child)
    assert parent is not None
    assert parent.resource_definition_name == "kubernetes-cluster"

    # Re-parenting replaces the single (0..1) inheritance link.
    ri.upsert_resource_definition(
        "aws",
        "eks-cluster",
        "0.1.0",
        parent_ref={
            "resource_namespace": "kubernetes",
            "resource_definition_name": "openshift-cluster",
            "version": "0.1.0",
        },
    )
    rels = client.get_from_resource_definition_isa_resource_definition_hmd_lang_deployment(
        child
    )
    assert len(rels) == 1
    assert ri._get_parent(child).resource_definition_name == "openshift-cluster"


def test_resource_definition_self_parent_rejected():
    client, _ = make_client()
    ri = ResourceInformation(client)
    ri.upsert_resource_definition("ns", "a", "0.1.0")

    with pytest.raises(ServiceException):
        ri.upsert_resource_definition(
            "ns",
            "a",
            "0.1.0",
            parent_ref={
                "resource_namespace": "ns",
                "resource_definition_name": "a",
                "version": "0.1.0",
            },
        )


def test_resource_definition_isa_cycle_rejected():
    client, _ = make_client()
    ri = ResourceInformation(client)

    ri.upsert_resource_definition("ns", "a", "0.1.0")
    ri.upsert_resource_definition(
        "ns",
        "b",
        "0.1.0",
        parent_ref={
            "resource_namespace": "ns",
            "resource_definition_name": "a",
            "version": "0.1.0",
        },
    )

    # a -> b would close the cycle a -> b -> a.
    with pytest.raises(ServiceException):
        ri.upsert_resource_definition(
            "ns",
            "a",
            "0.1.0",
            parent_ref={
                "resource_namespace": "ns",
                "resource_definition_name": "b",
                "version": "0.1.0",
            },
        )


def test_missing_parent_raises():
    client, _ = make_client()
    ri = ResourceInformation(client)
    with pytest.raises(ServiceException):
        ri.upsert_resource_definition(
            "ns",
            "a",
            "0.1.0",
            parent_ref={
                "resource_namespace": "ns",
                "resource_definition_name": "nope",
                "version": "0.1.0",
            },
        )


def test_list_resource_definitions_sorted_and_filtered():
    client, _ = make_client()
    ri = ResourceInformation(client)
    ri.upsert_resource_definition("bbb", "z", "0.1.0")
    ri.upsert_resource_definition("aaa", "y", "0.1.0")
    ri.upsert_resource_definition("aaa", "x", "0.1.0")

    all_rds = ri.list_resource_definitions()
    assert [
        (rd.resource_namespace, rd.resource_definition_name) for rd in all_rds
    ] == [("aaa", "x"), ("aaa", "y"), ("bbb", "z")]

    filtered = ri.list_resource_definitions(resource_namespace="aaa")
    assert len(filtered) == 2
    assert all(rd.resource_namespace == "aaa" for rd in filtered)


def test_declare_produces_dedups_and_stores_role(base_environment):
    client, _ = base_environment
    class_info = ClassInformation(client)
    rcv = class_info.get_repo_class_version("rc1", "0.1.1")

    ri = ResourceInformation(client)
    rd = ri.upsert_resource_definition("ns", "thing", "0.1.0")

    ri.declare_produces(rcv, rd, role="primary")
    ri.declare_produces(rcv, rd, role="primary")

    rels = client.get_from_repo_class_version_produces_resource_definition_hmd_lang_deployment(
        rcv
    )
    assert len(rels) == 1
    assert rels[0].role == "primary"


def test_has_provenance_recorded(base_environment):
    client, _ = base_environment
    class_info = ClassInformation(client)
    rcv = class_info.get_repo_class_version("rc1", "0.1.1")

    ri = ResourceInformation(client)
    rd = ri.upsert_resource_definition(
        "ns", "thing", "0.1.0", has_rcv_id=rcv.identifier
    )

    rels = client.get_from_repo_class_version_has_resource_definition_hmd_lang_deployment(
        rcv
    )
    assert len(rels) == 1
    assert rels[0].ref_to == rd.identifier


# --------------------------------------------------------------------------- #
# Resource submission (deployment-time outputs)
# --------------------------------------------------------------------------- #


def test_submit_resources_creates_nouns_and_relationships(base_environment):
    client, _ = base_environment
    ri = ResourceInformation(client)
    rs = ri.relationship_support
    ri.upsert_resource_definition(
        "aws",
        "eks-cluster",
        "0.1.0",
        output_schema={
            "type": "object",
            "required": ["cluster_name"],
            "properties": {"cluster_name": {"type": "string"}},
        },
    )
    rid = _rid_from_base_environment(client)

    resources = ri.submit_resources(
        rid,
        [
            {
                "resource_name": "prod-eks",
                "resource_definition": {
                    "resource_namespace": "aws",
                    "resource_definition_name": "eks-cluster",
                    "version": "0.1.0",
                },
                "output": {"cluster_name": "prod-eks"},
                "tags": [
                    {"key": "env", "value": "prod"},
                    {"key": "team", "value": "platform"},
                ],
            }
        ],
    )

    assert len(resources) == 1
    resource = resources[0]
    assert resource.output == {"cluster_name": "prod-eks"}

    # typed by its definition
    isa = client.get_from_resource_isa_resource_definition_hmd_lang_deployment(resource)
    assert len(isa) == 1
    assert rs.ref_to(isa[0]).resource_definition_name == "eks-cluster"

    # linked to the producing deployment
    linked = ri.get_resources_for_deployment(rid)
    assert [r.identifier for r in linked] == [resource.identifier]

    # searchable tags
    tag_rels = client.get_from_resource_has_resource_tag_hmd_lang_deployment(resource)
    assert {
        (rs.ref_to(t).key, rs.ref_to(t).value) for t in tag_rels
    } == {("env", "prod"), ("team", "platform")}


def test_serialize_resources_for_deployment_returns_rich_shape(base_environment):
    client, _ = base_environment
    ri = ResourceInformation(client)
    ri.upsert_resource_definition(
        "aws",
        "eks-cluster",
        "0.1.0",
        output_schema={
            "type": "object",
            "required": ["cluster_name"],
            "properties": {"cluster_name": {"type": "string"}},
        },
    )
    rid = _rid_from_base_environment(client)
    ri.submit_resources(
        rid,
        [
            {
                "resource_name": "prod-eks",
                "resource_definition": {
                    "resource_namespace": "aws",
                    "resource_definition_name": "eks-cluster",
                    "version": "0.1.0",
                },
                "output": {"cluster_name": "prod-eks"},
                "tags": [{"key": "env", "value": "prod"}],
            }
        ],
    )

    # Rich shape mirrors the submit_resources input so a consumer can match by
    # ResourceDefinition and read output (NERD0006).
    assert ri.serialize_resources_for_deployment(rid) == [
        {
            "resource_name": "prod-eks",
            "resource_definition": {
                "resource_namespace": "aws",
                "resource_definition_name": "eks-cluster",
                "version": "0.1.0",
            },
            "output": {"cluster_name": "prod-eks"},
            "tags": [{"key": "env", "value": "prod"}],
        }
    ]


def test_submit_resources_unknown_definition_raises(base_environment):
    client, _ = base_environment
    ri = ResourceInformation(client)
    rid = _rid_from_base_environment(client)

    with pytest.raises(ServiceException):
        ri.submit_resources(
            rid,
            [
                {
                    "resource_name": "x",
                    "resource_definition": {
                        "resource_namespace": "aws",
                        "resource_definition_name": "missing",
                        "version": "0.1.0",
                    },
                    "output": {},
                }
            ],
        )


def test_submit_resources_output_schema_validation(base_environment):
    client, _ = base_environment
    ri = ResourceInformation(client)
    ri.upsert_resource_definition(
        "aws",
        "eks-cluster",
        "0.1.0",
        output_schema={
            "type": "object",
            "required": ["cluster_name"],
            "properties": {"cluster_name": {"type": "string"}},
        },
    )
    rid = _rid_from_base_environment(client)

    with pytest.raises(ServiceException):
        ri.submit_resources(
            rid,
            [
                {
                    "resource_name": "bad",
                    "resource_definition": {
                        "resource_namespace": "aws",
                        "resource_definition_name": "eks-cluster",
                        "version": "0.1.0",
                    },
                    "output": {"cluster_name": 123},  # wrong type
                }
            ],
        )


def test_submit_resources_idempotent_by_deployment_and_name(base_environment):
    client, _ = base_environment
    ri = ResourceInformation(client)
    ri.upsert_resource_definition("aws", "eks-cluster", "0.1.0")
    rid = _rid_from_base_environment(client)

    spec = {
        "resource_name": "prod-eks",
        "resource_definition": {
            "resource_namespace": "aws",
            "resource_definition_name": "eks-cluster",
            "version": "0.1.0",
        },
        "output": {"endpoint": "v1"},
        "tags": [{"key": "env", "value": "prod"}],
    }
    ri.submit_resources(rid, [spec])

    spec2 = dict(spec)
    spec2["output"] = {"endpoint": "v2"}
    spec2["tags"] = [{"key": "env", "value": "staging"}]
    ri.submit_resources(rid, [spec2])

    resources = ri.get_resources_for_deployment(rid)
    assert len(resources) == 1
    assert resources[0].output == {"endpoint": "v2"}

    # tags reconciled to the latest submission
    tag_rels = client.get_from_resource_has_resource_tag_hmd_lang_deployment(
        resources[0]
    )
    rs = ri.relationship_support
    assert {
        (rs.ref_to(t).key, rs.ref_to(t).value) for t in tag_rels
    } == {("env", "staging")}


def test_find_resources_by_tag(base_environment):
    client, _ = base_environment
    ri = ResourceInformation(client)
    ri.upsert_resource_definition("aws", "eks-cluster", "0.1.0")
    rid = _rid_from_base_environment(client)

    ri.submit_resources(
        rid,
        [
            {
                "resource_name": "prod-eks",
                "resource_definition": {
                    "resource_namespace": "aws",
                    "resource_definition_name": "eks-cluster",
                    "version": "0.1.0",
                },
                "output": {},
                "tags": [{"key": "env", "value": "prod"}],
            },
            {
                "resource_name": "dev-eks",
                "resource_definition": {
                    "resource_namespace": "aws",
                    "resource_definition_name": "eks-cluster",
                    "version": "0.1.0",
                },
                "output": {},
                "tags": [{"key": "env", "value": "dev"}],
            },
        ],
    )

    prod = ri.find_resources_by_tag("env", "prod")
    assert [r.resource_name for r in prod] == ["prod-eks"]

    any_env = ri.find_resources_by_tag("env")
    assert {r.resource_name for r in any_env} == {"prod-eks", "dev-eks"}


# --------------------------------------------------------------------------- #
# Effective (isa-merged) output schema
# --------------------------------------------------------------------------- #


def _seed_k8s_and_eks(ri):
    """Seed a kubernetes-cluster parent and an eks-cluster child (eks isa k8s)."""
    ri.upsert_resource_definition(
        "kubernetes",
        "kubernetes-cluster",
        "0.1.0",
        output_schema={
            "type": "object",
            "required": ["cluster_name"],
            "properties": {"cluster_name": {"type": "string"}},
        },
    )
    return ri.upsert_resource_definition(
        "aws",
        "eks-cluster",
        "0.1.0",
        output_schema={
            "type": "object",
            "required": ["cluster_arn"],
            "properties": {"cluster_arn": {"type": "string"}},
        },
        parent_ref={
            "resource_namespace": "kubernetes",
            "resource_definition_name": "kubernetes-cluster",
            "version": "0.1.0",
        },
    )


def test_get_ancestry_root_first():
    client, _ = make_client()
    ri = ResourceInformation(client)
    eks = _seed_k8s_and_eks(ri)

    ancestry = ri.get_ancestry(eks)
    assert [rd.resource_definition_name for rd in ancestry] == [
        "kubernetes-cluster",
        "eks-cluster",
    ]


def test_effective_output_schema_merges_parent_and_child():
    client, _ = make_client()
    ri = ResourceInformation(client)
    eks = _seed_k8s_and_eks(ri)

    schema = ri.get_effective_output_schema(eks)
    assert set(schema["properties"].keys()) == {"cluster_name", "cluster_arn"}
    assert set(schema["required"]) == {"cluster_name", "cluster_arn"}


def test_submit_resources_validates_against_effective_schema(base_environment):
    client, _ = base_environment
    ri = ResourceInformation(client)
    _seed_k8s_and_eks(ri)
    rid = _rid_from_base_environment(client)

    spec = {
        "resource_name": "prod-eks",
        "resource_definition": {
            "resource_namespace": "aws",
            "resource_definition_name": "eks-cluster",
            "version": "0.1.0",
        },
        # missing the parent-required "cluster_name"
        "output": {"cluster_arn": "arn:aws:eks:..."},
    }
    with pytest.raises(ServiceException):
        ri.submit_resources(rid, [spec])

    # supplying both the parent- and child-required fields passes
    spec["output"]["cluster_name"] = "prod-eks"
    resources = ri.submit_resources(rid, [spec])
    assert len(resources) == 1


# --------------------------------------------------------------------------- #
# Producers (with subtype awareness)
# --------------------------------------------------------------------------- #


def test_get_producers_direct_and_with_subtypes(base_environment):
    client, _ = base_environment
    class_info = ClassInformation(client)
    rcv = class_info.get_repo_class_version("rc1", "0.1.1")

    ri = ResourceInformation(client)
    eks = _seed_k8s_and_eks(ri)
    k8s = ri.find_resource_definition("kubernetes", "kubernetes-cluster", "0.1.0")

    # rc1@0.1.1 produces the eks (subtype); nobody directly produces k8s.
    ri.declare_produces(rcv, eks)

    assert ri.get_producers(k8s) == []
    subtype_producers = ri.get_producers(k8s, include_subtypes=True)
    assert [p.identifier for p in subtype_producers] == [rcv.identifier]

    # direct producers of eks are found without include_subtypes
    assert [p.identifier for p in ri.get_producers(eks)] == [rcv.identifier]


# --------------------------------------------------------------------------- #
# Tag selector (AND semantics)
# --------------------------------------------------------------------------- #


def test_find_resources_by_selector_and_semantics(base_environment):
    client, _ = base_environment
    ri = ResourceInformation(client)
    ri.upsert_resource_definition("aws", "eks-cluster", "0.1.0")
    rid = _rid_from_base_environment(client)

    def _spec(name, tags):
        return {
            "resource_name": name,
            "resource_definition": {
                "resource_namespace": "aws",
                "resource_definition_name": "eks-cluster",
                "version": "0.1.0",
            },
            "output": {},
            "tags": tags,
        }

    ri.submit_resources(
        rid,
        [
            _spec(
                "prod-west",
                [{"key": "tier", "value": "prod"}, {"key": "region", "value": "west"}],
            ),
            _spec(
                "prod-east",
                [{"key": "tier", "value": "prod"}, {"key": "region", "value": "east"}],
            ),
            _spec("dev-west", [{"key": "tier", "value": "dev"}, {"key": "region", "value": "west"}]),
        ],
    )

    both = ri.find_resources_by_selector({"tier": "prod", "region": "west"})
    assert [r.resource_name for r in both] == ["prod-west"]

    tier_only = ri.find_resources_by_selector({"tier": "prod"})
    assert {r.resource_name for r in tier_only} == {"prod-west", "prod-east"}

    assert ri.find_resources_by_selector({}) == []
    assert ri.find_resources_by_selector({"tier": "prod", "region": "north"}) == []


# --------------------------------------------------------------------------- #
# Resource-based dependencies (SPEC0008)
# --------------------------------------------------------------------------- #

from hmd_lang_deployment.environment import Environment
from hmd_ms_deployment.environment_information import EnvironmentInformation


def _make_env(client, type_="dev"):
    env = Environment(type=type_, account_number="1", hmd_region="reg1")
    client.upsert(env)
    return EnvironmentInformation(env, client)


def _k8s_and_eks(ri):
    """Create a ``kubernetes-cluster`` definition and an ``eks-cluster`` subtype."""
    ri.upsert_resource_definition(
        "kubernetes",
        "kubernetes-cluster",
        "0.1.0",
        output_schema={"type": "object"},
    )
    return ri.upsert_resource_definition(
        "aws",
        "eks-cluster",
        "0.1.0",
        parent_ref={
            "resource_namespace": "kubernetes",
            "resource_definition_name": "kubernetes-cluster",
            "version": "0.1.0",
        },
    )


K8S_REF = {
    "resource_namespace": "kubernetes",
    "resource_definition_name": "kubernetes-cluster",
    "version": "0.1.0",
}


def test_add_repo_version_resource_dependency_creates_dual_edge():
    client, _ = make_client()
    ri = ResourceInformation(client)
    _k8s_and_eks(ri)
    class_info = ClassInformation(client)
    class_info.add_repo_version_by_name("eks-provider", "0.1.1", {}, {})

    consumer = class_info.add_repo_version_by_name(
        "needs-cluster",
        "0.1.1",
        {
            "cluster": {
                "required": "true",
                "repo_class_name": "eks-provider",  # retained as a suggestion
                "resource": {
                    "resource_namespace": "kubernetes",
                    "resource_definition_name": "kubernetes-cluster",
                    "version": "0.1.0",
                    "version_spec": "~= 0.1",
                    "tag_selector": "tier=prod",
                },
            }
        },
        {},
    )

    class_reqs = client.get_from_repo_class_version_req_repo_class_hmd_lang_deployment(
        consumer
    )
    res_reqs = client.get_from_repo_class_version_req_resource_definition_hmd_lang_deployment(
        consumer
    )
    assert len(class_reqs) == 1 and class_reqs[0].role == "cluster"  # suggestion kept
    assert len(res_reqs) == 1
    req = res_reqs[0]
    assert req.role == "cluster"
    assert req.required == "true"
    assert req.version_spec == "~= 0.1"
    assert req.tag_selector == "tier=prod"


def test_resource_only_role_has_no_class_edge():
    client, _ = make_client()
    ri = ResourceInformation(client)
    _k8s_and_eks(ri)
    class_info = ClassInformation(client)

    consumer = class_info.add_repo_version_by_name(
        "needs-cluster2",
        "0.1.1",
        {"cluster": {"required": "false", "resource": dict(K8S_REF)}},
        {},
    )
    assert (
        client.get_from_repo_class_version_req_repo_class_hmd_lang_deployment(consumer)
        == []
    )
    assert (
        len(
            client.get_from_repo_class_version_req_resource_definition_hmd_lang_deployment(
                consumer
            )
        )
        == 1
    )


def test_unregistered_class_with_resource_degrades_to_suggestion():
    """NERD0004 SPEC0008: a role naming an unregistered RepoClass must not raise
    when it also declares a ``resource`` — the resource requirement is
    authoritative and the missing class silently degrades to a suggestion. This
    lets shared local↔cloud manifests keep ``repo_class_name`` for the cloud path
    even when that class isn't registered locally."""
    client, _ = make_client()
    ri = ResourceInformation(client)
    _k8s_and_eks(ri)
    class_info = ClassInformation(client)

    consumer = class_info.add_repo_version_by_name(
        "needs-cluster3",
        "0.1.1",
        {
            "cluster": {
                "required": "true",
                "repo_class_name": "not-registered-anywhere",
                "resource": dict(K8S_REF),
            }
        },
        {},
    )

    # No class edge (the class was never registered), but the authoritative
    # resource edge is created.
    assert (
        client.get_from_repo_class_version_req_repo_class_hmd_lang_deployment(consumer)
        == []
    )
    res_reqs = (
        client.get_from_repo_class_version_req_resource_definition_hmd_lang_deployment(
            consumer
        )
    )
    assert len(res_reqs) == 1 and res_reqs[0].role == "cluster"


def test_unregistered_class_only_creates_stub_not_raises():
    """Tolerant registration: a class-only role naming an unregistered RepoClass no
    longer raises — a stub RepoClass is created via find-first get-or-create so a
    later authoritative registration reuses the same row, and the suggestion edge is
    formed. Apply-time validation still enforces a real instance."""
    client, _ = make_client()
    class_info = ClassInformation(client)
    cv = class_info.add_repo_version_by_name(
        "bad3",
        "0.1.1",
        {"cluster": {"required": "true", "repo_class_name": "not-registered"}},
        {},
    )
    # Stub RepoClass created (find-first, single row) and the class-req edge exists.
    assert class_info.get_repo_class("not-registered").repo_class_name == "not-registered"
    class_edges = client.get_from_repo_class_version_req_repo_class_hmd_lang_deployment(cv)
    assert len(class_edges) == 1 and class_edges[0].role == "cluster"


def test_role_without_class_or_resource_raises():
    client, _ = make_client()
    class_info = ClassInformation(client)
    with pytest.raises(ServiceException):
        class_info.add_repo_version_by_name(
            "bad", "0.1.1", {"role1": {"required": "true"}}, {}
        )


def test_resource_dependency_missing_definition_creates_stub_not_raises():
    """Tolerant registration: a resource requirement naming an unregistered
    ResourceDefinition no longer raises — a minimal stub is created via the
    find-first upsert and the authoritative req edge is formed."""
    client, _ = make_client()
    ri = ResourceInformation(client)
    class_info = ClassInformation(client)
    cv = class_info.add_repo_version_by_name(
        "bad2",
        "0.1.1",
        {
            "cluster": {
                "resource": {
                    "resource_namespace": "x",
                    "resource_definition_name": "y",
                    "version": "0.1.0",
                }
            }
        },
        {},
    )
    # Stub ResourceDefinition created and the resource-req edge exists.
    assert ri.find_resource_definition("x", "y", "0.1.0") is not None
    res_edges = client.get_from_repo_class_version_req_resource_definition_hmd_lang_deployment(
        cv
    )
    assert len(res_edges) == 1 and res_edges[0].role == "cluster"


def test_missing_definition_stub_reconciled_no_duplicate():
    """A stub ResourceDefinition created during tolerant registration is reconciled
    (not duplicated) when the authoritative definition is later upserted."""
    client, _ = make_client()
    ri = ResourceInformation(client)
    class_info = ClassInformation(client)
    class_info.add_repo_version_by_name(
        "cons-stub",
        "0.1.1",
        {
            "cluster": {
                "resource": {
                    "resource_namespace": "kubernetes",
                    "resource_definition_name": "kubernetes-cluster",
                    "version": "0.1.0",
                }
            }
        },
        {},
    )
    # Now the authoritative definition is registered (e.g. base-resource seeding).
    ri.upsert_resource_definition(
        "kubernetes",
        "kubernetes-cluster",
        "0.1.0",
        output_schema={"type": "object"},
        description="the real one",
    )
    # find_resource_definition asserts <=1 row; the stub was enriched in place.
    rd = ri.find_resource_definition("kubernetes", "kubernetes-cluster", "0.1.0")
    assert rd is not None and rd.description == "the real one"
    all_rds = ri.list_resource_definitions("kubernetes")
    assert (
        len(
            [
                r
                for r in all_rds
                if r.resource_definition_name == "kubernetes-cluster"
                and r.version == "0.1.0"
            ]
        )
        == 1
    )


def test_wiring_failure_does_not_truncate_other_roles():
    """A role with a malformed resource block reports its own failure clearly
    and does not silently prevent OTHER roles (declared before or after it in
    dict order) from being wired -- unlike the pre-fix behavior where one
    role's exception aborted the rest of the registration loop, leaving a
    RepoClassVersion permanently short every role declared after the bad one."""
    client, _ = make_client()
    class_info = ClassInformation(client)
    class_info.add_repo_version_by_name("before-class", "0.1.1", {}, {})
    class_info.add_repo_version_by_name("after-class", "0.1.1", {}, {})

    with pytest.raises(ServiceException) as exc_info:
        class_info.add_repo_version_by_name(
            "needs-many",
            "0.1.1",
            {
                "before": {"required": "true", "repo_class_name": "before-class"},
                "bad": {
                    "required": "true",
                    "resource": {"resource_namespace": "x"},  # missing name/version
                },
                "after": {"required": "true", "repo_class_name": "after-class"},
            },
            {},
        )
    assert "bad" in str(exc_info.value)

    cv = class_info.get_repo_class_version("needs-many", "0.1.1")
    class_edges = (
        client.get_from_repo_class_version_req_repo_class_hmd_lang_deployment(cv)
    )
    assert {e.role for e in class_edges} == {"before", "after"}


def test_resource_dependency_missing_field_raises_clear_error_not_keyerror():
    """_add_resource_dependency validates required resource fields explicitly
    instead of letting a raw KeyError propagate from a dict subscript."""
    client, _ = make_client()
    class_info = ClassInformation(client)
    with pytest.raises(ServiceException) as exc_info:
        class_info.add_repo_version_by_name(
            "needs-version",
            "0.1.1",
            {
                "cluster": {
                    "required": "true",
                    "resource": {
                        "resource_namespace": "kubernetes",
                        "resource_definition_name": "kubernetes-cluster",
                        # "version" intentionally omitted
                    },
                }
            },
            {},
        )
    assert "missing required field(s): version" in str(exc_info.value)


def test_resync_backfills_missing_role_dependency():
    """resync_repo_class_version_dependencies repairs an already-registered
    RepoClassVersion that's missing a role's edges (simulating a version left
    partially wired by a prior failed registration attempt), without needing a
    new version number -- and is safe to call twice."""
    client, _ = make_client()
    ri = ResourceInformation(client)
    _k8s_and_eks(ri)
    class_info = ClassInformation(client)
    class_info.add_repo_version_by_name("prov", "0.1.1", {}, {})

    # Only "role_ok" wired -- simulates a version left short "role_missing"'s
    # edges by a prior partial-registration failure.
    cv = class_info.add_repo_version_by_name(
        "needs-two",
        "0.1.1",
        {"role_ok": {"required": "true", "repo_class_name": "prov"}},
        {},
    )
    assert (
        len(client.get_from_repo_class_version_req_repo_class_hmd_lang_deployment(cv))
        == 1
    )

    full_dependencies = {
        "role_ok": {"required": "true", "repo_class_name": "prov"},
        "role_missing": {"required": "true", "resource": dict(K8S_REF)},
    }

    class_info.resync_repo_class_version_dependencies(
        "needs-two", "0.1.1", full_dependencies
    )
    class_edges = client.get_from_repo_class_version_req_repo_class_hmd_lang_deployment(
        cv
    )
    res_edges = client.get_from_repo_class_version_req_resource_definition_hmd_lang_deployment(
        cv
    )
    assert len(class_edges) == 1 and class_edges[0].role == "role_ok"
    assert len(res_edges) == 1 and res_edges[0].role == "role_missing"

    # Calling it again is idempotent -- no duplicate edges.
    class_info.resync_repo_class_version_dependencies(
        "needs-two", "0.1.1", full_dependencies
    )
    assert (
        len(client.get_from_repo_class_version_req_repo_class_hmd_lang_deployment(cv))
        == 1
    )
    assert (
        len(
            client.get_from_repo_class_version_req_resource_definition_hmd_lang_deployment(
                cv
            )
        )
        == 1
    )


def test_class_only_dependency_unchanged():
    """Backward-compat: a role with only a repo_class_name creates no resource edge."""
    client, _ = make_client()
    class_info = ClassInformation(client)
    class_info.add_repo_version_by_name("prov", "0.1.1", {}, {})
    cv = class_info.add_repo_version_by_name(
        "cons",
        "0.1.1",
        {"r": {"required": "true", "version_spec": "~= 0.1", "repo_class_name": "prov"}},
        {},
    )
    assert (
        len(client.get_from_repo_class_version_req_repo_class_hmd_lang_deployment(cv))
        == 1
    )
    assert (
        client.get_from_repo_class_version_req_resource_definition_hmd_lang_deployment(cv)
        == []
    )


def _provider_env(client):
    """A dev env with an ``eks-provider`` instance (produces eks) and a
    non-producing ``plain`` instance. Returns (ri, env_info, eks_inst, plain_inst)."""
    ri = ResourceInformation(client)
    eks = _k8s_and_eks(ri)
    class_info = ClassInformation(client)
    class_info.add_repo_version_by_name("eks-provider", "0.1.1", {}, {})
    provider_cv = class_info.get_repo_class_version("eks-provider", "0.1.1")
    ri.declare_produces(provider_cv, eks)
    class_info.add_repo_version_by_name("other", "0.1.1", {}, {})

    env_info = _make_env(client)
    eks_inst, eks_rid = env_info.add_repo_instance(
        "my-eks", "aaa", {}, provider_cv, status="DEPLOYED"
    )
    plain_inst, _ = env_info.add_repo_instance(
        "plain",
        "aaa",
        {},
        class_info.get_repo_class_version("other", "0.1.1"),
        status="DEPLOYED",
    )
    env_info.load_environment()
    return ri, env_info, eks_inst, eks_rid, plain_inst


def test_find_satisfying_instances_respects_inheritance():
    client, _ = make_client()
    ri, env_info, _, _, _ = _provider_env(client)

    # An eks-cluster producer satisfies a requirement for a kubernetes-cluster.
    names = {i.name for i in ri.find_satisfying_instances(env_info, K8S_REF)}
    assert names == {"my-eks"}


def test_instance_satisfies_requirement_positive_and_negative():
    client, _ = make_client()
    ri, env_info, eks_inst, _, plain_inst = _provider_env(client)

    assert ri.instance_satisfies_requirement(eks_inst, K8S_REF)
    assert not ri.instance_satisfies_requirement(plain_inst, K8S_REF)


def test_find_satisfying_instances_tag_selector():
    client, _ = make_client()
    ri, env_info, eks_inst, eks_rid, _ = _provider_env(client)

    # No resources produced yet -> the tag selector filters everything out.
    assert ri.find_satisfying_instances(env_info, K8S_REF, tag_selector="tier=prod") == []

    ri.submit_resources(
        eks_rid,
        [
            {
                "resource_name": "c1",
                "resource_definition": {
                    "resource_namespace": "aws",
                    "resource_definition_name": "eks-cluster",
                    "version": "0.1.0",
                },
                "output": {},
                "tags": [{"key": "tier", "value": "prod"}],
            }
        ],
    )

    assert {
        i.name
        for i in ri.find_satisfying_instances(env_info, K8S_REF, tag_selector="tier=prod")
    } == {"my-eks"}
    assert (
        ri.find_satisfying_instances(env_info, K8S_REF, tag_selector="tier=dev") == []
    )


def test_find_satisfying_instances_version_spec_broadens():
    client, _ = make_client()
    ri = ResourceInformation(client)
    # Two versions of the same definition; only 0.2.0 has a producer instance.
    ri.upsert_resource_definition("kubernetes", "kubernetes-cluster", "0.1.0")
    ri.upsert_resource_definition("kubernetes", "kubernetes-cluster", "0.2.0")
    class_info = ClassInformation(client)
    class_info.add_repo_version_by_name("v2-provider", "0.1.1", {}, {})
    v2_cv = class_info.get_repo_class_version("v2-provider", "0.1.1")
    ri.declare_produces(
        v2_cv,
        ri.find_resource_definition("kubernetes", "kubernetes-cluster", "0.2.0"),
    )

    env_info = _make_env(client)
    env_info.add_repo_instance("v2-inst", "aaa", {}, v2_cv, status="DEPLOYED")
    env_info.load_environment()

    # Requirement references 0.1.0: without a version_spec the 0.2.0 producer
    # does not satisfy it; with ~= 0.1 the compatible 0.2.0 version is accepted.
    assert ri.find_satisfying_instances(env_info, K8S_REF) == []
    assert {
        i.name
        for i in ri.find_satisfying_instances(env_info, K8S_REF, version_spec="~= 0.1")
    } == {"v2-inst"}


def test_apply_time_validation_accepts_compatible_and_creates_edge():
    client, _ = make_client()
    ri, env_info, eks_inst, _, _ = _provider_env(client)
    class_info = ClassInformation(client)
    class_info.add_repo_version_by_name(
        "app",
        "0.1.1",
        {"cluster": {"required": "true", "resource": dict(K8S_REF)}},
        {},
    )
    app_cv = class_info.get_repo_class_version("app", "0.1.1")

    app_inst, _ = env_info.add_repo_instance(
        "app-1",
        "aaa",
        {},
        app_cv,
        dependencies={"cluster": [eks_inst]},
        status="DEPLOYED",
    )

    dep_edges = client.get_from_repo_instance_req_repo_instance_hmd_lang_deployment(
        app_inst
    )
    assert len(dep_edges) == 1
    assert env_info.rel_support.ref_to(dep_edges[0]).name == "my-eks"
    assert dep_edges[0].role == "cluster"


def test_local_core_producer_satisfies_ext_secrets_style_deps():
    """End-to-end SPEC0008 shape matching the local ext-secrets dev-deploy loop.

    A single ``hmd-cli-neuronsphere`` producer instance (``local-k3s``) produces
    BOTH ``kubernetes-cluster`` and ``compute-node``. A consumer
    (``hmd-inf-ext-secrets``) declares two roles that each carry BOTH an
    *unregistered* cloud ``repo_class_name`` (a suggestion) and an authoritative
    ``resource`` block. Applying the consumer naming ``local-k3s`` for both roles
    validates and creates a dependency edge per role — proving the CLI's producer
    wiring lets ext-secrets resolve locally without the cloud eks RepoClasses.
    """
    client, _ = make_client()
    ri = ResourceInformation(client)
    ri.upsert_resource_definition(
        "kubernetes.neuronsphere.io",
        "kubernetes-cluster",
        "0.1.0",
        output_schema={"type": "object"},
    )
    ri.upsert_resource_definition(
        "compute.neuronsphere.io",
        "compute-node",
        "0.1.0",
        output_schema={"type": "object"},
    )
    class_info = ClassInformation(client)

    # The CLI is the RepoClass that owns/produces the local core resource types.
    core_cv = class_info.add_repo_version_by_name("hmd-cli-neuronsphere", "0.5", {}, {})
    ri.declare_produces(
        core_cv,
        ri.find_resource_definition(
            "kubernetes.neuronsphere.io", "kubernetes-cluster", "0.1.0"
        ),
    )
    ri.declare_produces(
        core_cv,
        ri.find_resource_definition(
            "compute.neuronsphere.io", "compute-node", "0.1.0"
        ),
    )

    env_info = _make_env(client)
    core_inst, _ = env_info.add_repo_instance(
        "local-k3s", "local", {}, core_cv, status="DEPLOYED"
    )
    env_info.load_environment()

    # ext-secrets-style consumer: dual-attribute roles, unregistered eks classes.
    consumer_cv = class_info.add_repo_version_by_name(
        "hmd-inf-ext-secrets",
        "0.2",
        {
            "eks-cluster": {
                "repo_class_name": "hmd-inf-eks-cluster",
                "required": "true",
                "version_spec": "~= 0.1.8",
                "resource": {
                    "resource_namespace": "kubernetes.neuronsphere.io",
                    "resource_definition_name": "kubernetes-cluster",
                    "version": "0.1.0",
                    "version_spec": "~= 0.1",
                },
            },
            "compute": {
                "repo_class_name": "hmd-inf-eks-node-group",
                "required": "true",
                "version_spec": "~= 0.1.5",
                "resource": {
                    "resource_namespace": "compute.neuronsphere.io",
                    "resource_definition_name": "compute-node",
                    "version": "0.1.0",
                    "version_spec": "~= 0.1",
                },
            },
        },
        {},
    )

    app_inst, _ = env_info.add_repo_instance(
        "ext-secrets",
        "local",
        {},
        consumer_cv,
        dependencies={"eks-cluster": [core_inst], "compute": [core_inst]},
        status="DEPLOY_NEXT",
    )

    dep_edges = client.get_from_repo_instance_req_repo_instance_hmd_lang_deployment(
        app_inst
    )
    assert sorted(e.role for e in dep_edges) == ["compute", "eks-cluster"]
    for edge in dep_edges:
        assert env_info.rel_support.ref_to(edge).name == "local-k3s"


def test_apply_time_validation_rejects_incompatible():
    client, _ = make_client()
    ri, env_info, _, _, plain_inst = _provider_env(client)
    class_info = ClassInformation(client)
    class_info.add_repo_version_by_name(
        "app2",
        "0.1.1",
        {"cluster": {"required": "true", "resource": dict(K8S_REF)}},
        {},
    )
    app_cv = class_info.get_repo_class_version("app2", "0.1.1")

    with pytest.raises(ServiceException):
        env_info.add_repo_instance(
            "app-2",
            "aaa",
            {},
            app_cv,
            dependencies={"cluster": [plain_inst]},
            status="DEPLOYED",
        )


def test_apply_time_validation_falls_back_to_repo_class():
    """Resource→RepoClass fallback: when the supplied instance doesn't produce the
    required resource type, an instance of the role's suggested repo_class still
    satisfies the role (strict, but degrades to class validation)."""
    client, _ = make_client()
    ri, env_info, _, _, plain_inst = _provider_env(client)
    class_info = ClassInformation(client)
    # Role declares BOTH the resource requirement and the suggested class "other"
    # (plain_inst's class). plain_inst produces no resource of the required type.
    class_info.add_repo_version_by_name(
        "app3",
        "0.1.1",
        {
            "cluster": {
                "required": "true",
                "repo_class_name": "other",
                "resource": dict(K8S_REF),
            }
        },
        {},
    )
    app_cv = class_info.get_repo_class_version("app3", "0.1.1")

    app_inst, _ = env_info.add_repo_instance(
        "app-3",
        "aaa",
        {},
        app_cv,
        dependencies={"cluster": [plain_inst]},
        status="DEPLOYED",
    )
    dep_edges = client.get_from_repo_instance_req_repo_instance_hmd_lang_deployment(
        app_inst
    )
    assert len(dep_edges) == 1
    assert env_info.rel_support.ref_to(dep_edges[0]).name == "plain"


def test_apply_time_validation_rejects_when_neither_resource_nor_class_matches():
    """Strict: an instance satisfying neither the required resource type nor a
    suggested repo_class is rejected."""
    client, _ = make_client()
    ri, env_info, _, _, plain_inst = _provider_env(client)
    class_info = ClassInformation(client)
    class_info.add_repo_version_by_name("mismatch-class", "0.1.1", {}, {})
    class_info.add_repo_version_by_name(
        "app4",
        "0.1.1",
        {
            "cluster": {
                "required": "true",
                "repo_class_name": "mismatch-class",
                "resource": dict(K8S_REF),
            }
        },
        {},
    )
    app_cv = class_info.get_repo_class_version("app4", "0.1.1")

    with pytest.raises(ServiceException):
        env_info.add_repo_instance(
            "app-4",
            "aaa",
            {},
            app_cv,
            dependencies={"cluster": [plain_inst]},
            status="DEPLOYED",
        )
