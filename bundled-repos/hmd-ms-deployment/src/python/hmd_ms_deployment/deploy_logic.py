import logging
from typing import Set, Dict, List

from hmd_cli_tools import ServiceException
from hmd_graphql_client.relationship_support import RelationshipSupport
from hmd_lang_deployment.repo_instance import RepoInstance
from hmd_lang_deployment.repo_instance_deployment import RepoInstanceDeployment
from hmd_lang_deployment.repo_instance_has_repo_instance_deployment import (
    RepoInstanceHasRepoInstanceDeployment,
)
from hmd_lang_deployment.repo_instance_req_repo_instance import (
    RepoInstanceReqRepoInstance,
)
from hmd_ms_deployment import DESTROYED, DEPLOY_NEXT, DEPLOYED

from hmd_ms_deployment.environment_information import (
    EnvironmentInformation,
    get_current_repo_instance_deployment,
    get_repo_instance_deployment_for_status,
)

logger = logging.getLogger(f"HMD.{__name__}")


class GraphNode:
    def __init__(self, instance: RepoInstance):
        self.instance = instance
        self.children = set()

    def __hash__(self):
        return hash(self.instance.identifier)


class DeploymentDag:
    def __init__(self, nodes: Dict[RepoInstance, GraphNode]):
        self.nodes: Dict[RepoInstance, GraphNode]
        self.nodes = nodes


def get_i_depend_on_directly(ri: RepoInstance, rs: RelationshipSupport):
    result = []
    rels = ri.get_from_repo_instance_req_repo_instance_hmd_lang_deployment()
    for rel in rels:
        ri_rid = get_current_repo_instance_deployment(rs.ref_to(rel), rs)
        if not ri_rid or rs.ref_to(ri_rid).status != DESTROYED:
            result.append(rs.ref_to(rel))
    return result


def get_depends_on_me_directly(ri: RepoInstance, rs: RelationshipSupport):
    result = []
    rels = ri.get_to_repo_instance_req_repo_instance_hmd_lang_deployment()
    for rel in rels:
        ri_rid = get_current_repo_instance_deployment(rs.ref_to(rel), rs)
        if not ri_rid or rs.ref_to(ri_rid).status != DESTROYED:
            result.append(rs.ref_from(rel))
    return result


def validate_dependencies_satisfied(
    environment_info: EnvironmentInformation,
    repo_instance_deployment_ids: List[str],
) -> None:
    """Validate that all dependencies of instances being deployed are satisfied.

    For each instance in the changeset, check that all its dependencies are either:
    1. Already DEPLOYED (have a current RID with DEPLOYED status), OR
    2. Being deployed in this changeset (have a RID in repo_instance_deployment_ids)

    This function uses cached data from environment_info (populated by load_environment())
    to avoid database queries.

    :param environment_info: Environment information with loaded instances
    :param repo_instance_deployment_ids: List of RID identifiers in the current changeset
    :raises ServiceException: If any dependencies are not satisfied
    """
    rs = environment_info.rel_support
    missing_dependencies = []

    # Build a set for O(1) lookups
    changeset_rid_ids = set(repo_instance_deployment_ids)

    # Pre-build lookup maps from cached relationships for efficiency
    # Map: RI identifier -> list of RI->RID relationships
    ri_to_rid_rels = {}
    for rel in environment_info.relationships.get(RepoInstanceHasRepoInstanceDeployment, {}).values():
        ref_from = rel.ref_from if isinstance(rel.ref_from, str) else rel.ref_from.identifier
        if ref_from not in ri_to_rid_rels:
            ri_to_rid_rels[ref_from] = []
        ri_to_rid_rels[ref_from].append(rel)

    # Map: RI identifier -> list of dependency relationships (RI -> dependency RI)
    ri_to_dep_rels = {}
    for rel in environment_info.relationships.get(RepoInstanceReqRepoInstance, {}).values():
        ref_from = rel.ref_from if isinstance(rel.ref_from, str) else rel.ref_from.identifier
        if ref_from not in ri_to_dep_rels:
            ri_to_dep_rels[ref_from] = []
        ri_to_dep_rels[ref_from].append(rel)

    # Get all RepoInstances being deployed in this changeset
    for rid_id in repo_instance_deployment_ids:
        # Get the RepoInstanceDeployment from cache
        rid = environment_info.nouns.get(RepoInstanceDeployment, {}).get(rid_id)
        if not rid:
            continue

        # Find the RepoInstance for this RID using our pre-built map
        repo_instance = None
        for ri_id, rels in ri_to_rid_rels.items():
            for rel in rels:
                ref_to = rel.ref_to if isinstance(rel.ref_to, str) else rel.ref_to.identifier
                if ref_to == rid_id:
                    repo_instance = rs.ref_from(rel)
                    break
            if repo_instance:
                break

        if not repo_instance:
            continue

        # Get dependencies from cached relationships
        ri_id = repo_instance.identifier
        dep_rels = ri_to_dep_rels.get(ri_id, [])

        for dep_rel in dep_rels:
            dependency = rs.ref_to(dep_rel)
            dep_id = dependency.identifier

            # Check if dependency is in the changeset using cached relationships
            dep_rid_rels = ri_to_rid_rels.get(dep_id, [])
            in_changeset = any(
                (rel.ref_to if isinstance(rel.ref_to, str) else rel.ref_to.identifier) in changeset_rid_ids
                for rel in dep_rid_rels
            )

            if in_changeset:
                # Dependency is being deployed in this changeset
                continue

            # Check if dependency is already DEPLOYED using cached relationships
            current_rid = None
            for rel in dep_rid_rels:
                if rel.current == "true":
                    current_rid = rs.ref_to(rel)
                    break

            if current_rid and current_rid.status == DEPLOYED:
                # Dependency is already deployed
                continue

            # Dependency is neither in changeset nor deployed
            missing_dependencies.append(
                f"{repo_instance.name} depends on {dependency.name} which is not deployed "
                f"and not in the current changeset"
            )

    if missing_dependencies:
        raise ServiceException(
            f"Missing dependencies: {'; '.join(missing_dependencies)}"
        )


def build_deployment_dag(
    environment_info: EnvironmentInformation,
    repo_instance_deployment_ids: List[str],
    destroy=False,
) -> DeploymentDag:
    rs = environment_info.rel_support
    # make sure all objects have been loaded and cached...
    environment_info.load_environment()
    logger.debug("Environment Info Loaded")
    # get all RepoInstance objects
    all_repo_instances = set(
        environment_info.nouns[RepoInstance].values()
    )  # type: Set[RepoInstance]

    repo_instances = []
    for repo_instance in all_repo_instances:
        ri_rid = get_current_repo_instance_deployment(repo_instance, rs)

        # Get RepoInstanceDeployments related to current RepoInstance if we care about it
        instance_deployments = [
            rs.ref_to(rel)
            for rel in repo_instance.get_from_repo_instance_has_repo_instance_deployment_hmd_lang_deployment()
            if rel.ref_to in repo_instance_deployment_ids
        ]
        assert len(instance_deployments) <= 1
        target_instance = len(instance_deployments) > 0
        if not ri_rid or target_instance or rs.ref_to(ri_rid).status != DESTROYED:
            repo_instances.append(repo_instance)

    logger.debug(f"Building DAG for {len(repo_instances)} RepoInstances")

    # precompute identifier set for O(1) membership checks
    repo_instance_ids = {ri.identifier for ri in repo_instances}

    # make a graph node for each RepoInstance...
    nodes = {inst: GraphNode(inst) for inst in repo_instances}
    if len(nodes) == 0:
        raise ServiceException("No data found when creating the deployment dag.")

    # connect the nodes...
    for inst in repo_instances:
        node = nodes[inst]

        if not destroy:
            rels = inst.get_from_repo_instance_req_repo_instance_hmd_lang_deployment()
            i_depend_on_directly = [
                rs.ref_to(rel)
                for rel in rels
                if rel.ref_to in repo_instance_ids
            ]
        else:
            rels = inst.get_to_repo_instance_req_repo_instance_hmd_lang_deployment()
            i_depend_on_directly = [
                rs.ref_from(rel)
                for rel in rels
                if rel.ref_from in repo_instance_ids
            ]

        for ri in i_depend_on_directly:
            node.children.add(nodes[ri])

    # make sure everything is acyclic...
    nothing_depends_on = []
    for ri in repo_instances:
        if not destroy:
            has_dependents = any(
                rel.ref_from in repo_instance_ids
                for rel in ri.get_to_repo_instance_req_repo_instance_hmd_lang_deployment()
            )
        else:
            has_dependents = any(
                rel.ref_to in repo_instance_ids
                for rel in ri.get_from_repo_instance_req_repo_instance_hmd_lang_deployment()
            )
        if not has_dependents:
            nothing_depends_on.append(ri)

    if not nothing_depends_on:
        raise ServiceException("No independent nodes in graph.")

    for ri in nothing_depends_on:
        verify_acyclicness(nodes[ri], set())

    # transitive reduction with memoized dependency lookups
    memo = {}
    for ri in repo_instances:
        node = nodes[ri]
        children_to_remove = set()
        for child in node.children:
            for sibling in node.children:
                if child.instance in find_i_depend_on(sibling.instance, rs, destroy, memo):
                    children_to_remove.add(child)
        for child in children_to_remove:
            node.children.remove(child)

    return DeploymentDag(nodes)


def verify_acyclicness(node: GraphNode, visited: Set):
    for child in node.children:
        if child.instance in visited:
            raise Exception(
                f"Graph is not acyclic: {node.instance.name}: {child.instance.name}"
            )
        visited.add(child.instance)
        verify_acyclicness(child, visited)
        visited.remove(child.instance)


def find_depends_on_me(
    me_: RepoInstance,
    rs: RelationshipSupport,
) -> Set[RepoInstance]:
    depends_on_me = set(
        [
            rs.ref_from(rel)
            for rel in me_.get_to_repo_instance_req_repo_instance_hmd_lang_deployment()
        ]
    )

    for dom in depends_on_me:
        dodom = find_depends_on_me(dom, rs)
        if me_ in dodom:
            raise Exception(f"Circular dependency: RepoInstance({me_.identifier})")
        depends_on_me = depends_on_me.union(dodom)

    return depends_on_me


def find_i_depend_on(
    me_: RepoInstance, rs: RelationshipSupport, destroy: bool,
    _memo: Dict[str, Set[RepoInstance]] = None,
) -> Set[RepoInstance]:
    if _memo is None:
        _memo = {}
    cache_key = me_.identifier
    if cache_key in _memo:
        return _memo[cache_key]

    i_depend_on = set(
        [
            rs.ref_to(rel) if not destroy else rs.ref_from(rel)
            for rel in (
                me_.get_from_repo_instance_req_repo_instance_hmd_lang_deployment()
                if not destroy
                else me_.get_to_repo_instance_req_repo_instance_hmd_lang_deployment()
            )
        ]
    )

    for ido in i_depend_on:
        idodo = find_i_depend_on(ido, rs, destroy, _memo)
        if me_ in idodo:
            raise Exception(f"Circular dependency: RepoInstance({me_.identifier})")
        i_depend_on = i_depend_on.union(idodo)

    _memo[cache_key] = i_depend_on
    return i_depend_on
