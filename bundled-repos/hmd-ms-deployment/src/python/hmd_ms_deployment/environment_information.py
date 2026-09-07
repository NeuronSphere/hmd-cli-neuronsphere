import io
import logging
import os
import tempfile
from collections import defaultdict
from copy import copy
from datetime import datetime
from json import load
from pathlib import Path
from typing import List, Type, Dict, Union, Optional, Tuple

from hmd_cli_tools import ServiceException
from hmd_graphql_client import BaseClient
from hmd_graphql_client.hmd_db_engine_client import DbEngineClient
from hmd_graphql_client.relationship_support import RelationshipSupport
from hmd_lang_deployment.environment import Environment
from hmd_lang_deployment.environment_has_repo_instance import EnvironmentHasRepoInstance
from hmd_lang_deployment.hmd_lang_deployment_client import HmdLangDeploymentClient
from hmd_lang_deployment.repo_class import RepoClass
from hmd_lang_deployment.repo_class_version import RepoClassVersion
from hmd_lang_deployment.repo_class_version_req_repo_class import (
    RepoClassVersionReqRepoClass,
)
from hmd_lang_deployment.repo_instance import RepoInstance
from hmd_lang_deployment.repo_instance_deployment import RepoInstanceDeployment
from hmd_lang_deployment.repo_instance_deployment_has_repo_class_version import (
    RepoInstanceDeploymentHasRepoClassVersion,
)
from hmd_lang_deployment.repo_instance_has_repo_instance_deployment import (
    RepoInstanceHasRepoInstanceDeployment,
)
from hmd_lang_deployment.repo_instance_isa_repo_class import RepoInstanceIsaRepoClass
from hmd_lang_deployment.repo_instance_req_repo_instance import (
    RepoInstanceReqRepoInstance,
)
from hmd_lib_auth.hmd_lib_auth import okta_service_account_token_by_service
from hmd_lib_librarian_client.artifact_tools import (
    retrieve_and_unzip,
    content_item_path_from_spec,
)
from hmd_meta_types import Noun, Relationship
from hmd_ms_deployment import (
    DEPLOYED,
    DEPLOY_NEXT,
    RID_STATUS_OPTIONS,
    FAILED,
    DESTROY_NEXT,
    SKIPPED,
    DESTROYED,
)
from hmd_ms_deployment.environment_query import nouns, relationships
from hmd_ms_deployment.env_cache import get_env_cache, is_volatile
from hmd_ms_deployment.version import VersionSpecifier

logger = logging.getLogger(f"HMD.{__name__}")

TERMINAL_RID_STATUSES = {DEPLOYED, FAILED, DESTROYED, SKIPPED}


def mark_rid_status(client, rid: RepoInstanceDeployment, status: str, now=None):
    """Apply a status transition to a RepoInstanceDeployment with timestamp bookkeeping.

    Sets ``rid.status``. Populates ``rid.start`` if currently null (so any
    consumer can place the RID on a timeline). Populates ``rid.end`` if the
    new status is terminal (DEPLOYED, FAILED, DESTROYED, SKIPPED) and end is
    currently null. Upserts once.

    Use this in place of bare ``rid.status = X; client.upsert(rid)`` so the
    timeline/Gantt views always have bookend timestamps to render.

    This does NOT touch the ``current`` flag on repo_instance_has_repo_instance_deployment
    relationships; callers that need to flip ``current`` (i.e. the canonical
    activate-deployment path) handle that themselves.
    """
    now_ = now or datetime.utcnow()
    rid.status = status
    if rid.start is None:
        rid.start = now_
    if status in TERMINAL_RID_STATUSES and rid.end is None:
        rid.end = now_
    client.upsert(rid)


def version_in_version_spec(version_spec, version):
    vs = VersionSpecifier(version_spec)
    vs.validate(version)


def get_repo_instance_deployment_for_status(
    ri: RepoInstance, statuses: List[str], rel_support: RelationshipSupport
) -> Optional[RepoInstanceDeployment]:
    for status in statuses:
        if status not in RID_STATUS_OPTIONS:
            raise ServiceException(
                f"Status must be one of {', '.join(RID_STATUS_OPTIONS)}, was {status}."
            )

    rids = ri.get_from_repo_instance_has_repo_instance_deployment_hmd_lang_deployment()
    result = None
    for status in statuses:
        result = [
            rel_support.ref_to(rid)
            for rid in rids
            if rel_support.ref_to(rid).status == status
            and not rel_support.ref_to(rid).end
        ]
        if result:
            break

    return result[0] if result else None


def get_current_repo_instance_deployment(
    ri: RepoInstance, rs: RelationshipSupport
) -> RepoInstanceHasRepoInstanceDeployment:
    current = [
        ri_rid
        for ri_rid in ri.get_from_repo_instance_has_repo_instance_deployment_hmd_lang_deployment()
        if ri_rid.current == "true"
    ]
    assert (
        len(current) <= 1
    ), f'Expected at most 1 "current" RID. Found {len(current)} for RI {ri.identifier}'

    return current[0] if current else None


def _build_relationship_support(client: BaseClient) -> RelationshipSupport:
    result = RelationshipSupport()
    result.register_client(client)
    return result


def fetch_auth_token():
    token = okta_service_account_token_by_service(
        os.environ["HMD_INSTANCE_NAME"],
        os.environ["HMD_REPO_NAME"],
        os.environ["HMD_DID"],
    )
    return token


def get_configuration_artifact(
    artifact_spec: str,
) -> Dict:
    """Retrieve an artifact and return the contents of a json file.

    :param artifact_spec: The artifact spec for the artifact librarian.
    :rtype: Dict

    """
    with tempfile.TemporaryDirectory() as tmp_base:
        tmp_base = Path(tmp_base)

        content_item_path, artifact_path = content_item_path_from_spec(artifact_spec)
        if not artifact_path:
            raise ServiceException(
                "Invalid configuration. Artifact spec must specify a file in the bundle."
            )

        retrieve_and_unzip(
            os.environ["HMD_CUSTOMER_CODE"],
            os.environ["HMD_REGION"],
            content_item_path,
            tmp_base,
            fetch_auth_token(),
        )
        if not os.path.exists(tmp_base / artifact_path):
            raise Exception(
                f"File, {artifact_path}, not found in content item, {content_item_path}."
            )
        with open(tmp_base / artifact_path, "r") as fl:
            return load(fl)


class EnvironmentInformation:
    def __init__(self, environment: Environment, client: HmdLangDeploymentClient):
        self.environment = environment
        self.nouns = defaultdict(dict)  # type: Dict[Type[Noun], Dict[str,Noun]]
        self.relationships = defaultdict(
            dict
        )  # type: Dict[Type[Relationship], Dict[str,Relationship]]
        self.attrs_to_display = {
            RepoClass: ["repo_class_name"],
            RepoClassVersion: ["version"],
            Environment: ["type"],
            RepoInstance: ["name"],
            RepoInstanceDeployment: ["deployment_id", "status"],
        }
        self.rel_attrs_to_display = {
            RepoInstanceReqRepoInstance: ["role"],
            RepoClassVersionReqRepoClass: ["required", "version_spec", "role"],
        }
        self.client = client
        self.rel_support = _build_relationship_support(self.client._base_client)

        self.cache_noun(environment)

    def cache_noun(self, noun: Noun):
        self.nouns[type(noun)][noun.identifier] = noun

    def cache_relationship(self, relationship: Relationship):
        self.relationships[type(relationship)][relationship.identifier] = relationship

    def _process_environment(self):
        """
        For a specified environment, load the objects that are part of the environment
        into memory in preparation for additional operations (validation, etc.)
        """
        environment = self.environment
        self.cache_noun(environment)
        rels = self.client.get_from_environment_has_repo_instance_hmd_lang_deployment(
            environment
        )
        self.rel_support.pull_relationship_nouns(rels)
        for rel in rels:
            self.cache_relationship(rel)
            # noinspection PyTypeChecker
            self._process_repo_instance(self.rel_support.ref_to(rel))

    def _process_repo_instance(self, ri: RepoInstance):
        self.cache_noun(ri)

        rels = self.client.get_from_repo_instance_req_repo_instance_hmd_lang_deployment(
            ri
        )
        self.rel_support.pull_relationship_nouns(rels)
        for rel in rels:
            self.cache_relationship(rel)

        rels = self.client.get_from_repo_instance_isa_repo_class_hmd_lang_deployment(ri)
        for rel in rels:
            self.cache_relationship(rel)
            self.rel_support.ref_from(rel)
            self.cache_noun(self.rel_support.ref_to(rel))

        rels = self.client.get_from_repo_instance_has_repo_instance_deployment_hmd_lang_deployment(
            ri
        )
        self.rel_support.pull_relationship_nouns(
            rels,
        )
        for rel in rels:
            rid = self.rel_support.ref_to(rel)
            if (
                rid.status
                in [
                    DEPLOY_NEXT,
                    DESTROY_NEXT,
                ]
                or rel.current == "true"
            ):
                self.cache_relationship(rel)
                # noinspection PyTypeChecker
                self._process_repo_instance_deployment(rid)

    def _process_class_version(self, repo_class_version: RepoClassVersion):
        self.cache_noun(repo_class_version)

        rels = self.client.get_to_repo_class_has_repo_class_version_hmd_lang_deployment(
            repo_class_version
        )

        for rel in rels:
            self.cache_relationship(rel)
            self.cache_noun(self.rel_support.ref_from(rel))

        self.rel_support.pull_relationship_nouns(
            self.client.get_from_repo_class_version_req_repo_class_hmd_lang_deployment(
                repo_class_version
            ),
        )
        for (
            rel
        ) in (
            repo_class_version.get_from_repo_class_version_req_repo_class_hmd_lang_deployment()
        ):
            self.cache_relationship(rel)
            self.cache_noun(self.rel_support.ref_to(rel))

    def _process_repo_instance_deployment(self, rid: RepoInstanceDeployment):
        self.cache_noun(rid)
        rels = self.client.get_from_repo_instance_deployment_has_repo_class_version_hmd_lang_deployment(
            rid
        )
        for rel in rels:
            self.cache_relationship(rel)
            self.rel_support.ref_from(rel)
            self._process_class_version(self.rel_support.ref_to(rel))

    def load_environment(self) -> None:
        if isinstance(self.client._base_client, DbEngineClient):
            base = self.client._base_client
            env_type = self.environment.type
            cache = get_env_cache()

            # Fast path: rehydrate the graph from Redis (NERD0005). Both the
            # topology and volatile status layers must be present for a hit;
            # anything else falls through to the Postgres queries below.
            topology, status = cache.load_layers(env_type, base.loader)
            if topology is not None and status is not None:
                for entity in topology + status:
                    # Seed the client instance cache so ref_to/ref_from resolve
                    # against these in-memory entities instead of hitting the db.
                    base.cache_instance(entity)
                    if isinstance(entity, Relationship):
                        self.cache_relationship(entity)
                    else:
                        self.cache_noun(entity)
                for entity in topology + status:
                    if isinstance(entity, Relationship):
                        self.rel_support.ref_to(entity)
                        self.rel_support.ref_from(entity)
                logger.debug(
                    "Loaded environment %s from cache (%d topology, %d status)",
                    env_type,
                    len(topology),
                    len(status),
                )
                return

            # Cache miss / disabled / error: the original Postgres load path.
            noun_entities = base.native_query_nouns(
                nouns.format(**{"environment_type": env_type}), {}
            )
            logger.debug(f"Cacheing {len(noun_entities)} Nouns")
            for noun in noun_entities:
                self.cache_noun(noun)
            logger.debug(f"Cached {len(noun_entities)} Nouns")
            rels = base.native_query_relationships(
                relationships.format(**{"environment_type": env_type}), {}
            )
            logger.debug(f"Cacheing {len(rels)} Relationships")
            for noun in rels:
                self.cache_relationship(noun)
            for rel in rels:
                self.rel_support.ref_to(rel)
                self.rel_support.ref_from(rel)
            logger.debug(f"Cached {len(rels)} Relationships")

            # Populate both cache layers for next time. Partition by the volatile
            # status membership (RID nouns + current-pointer / version-link rels);
            # everything else is stable topology.
            topology_entities = [e for e in noun_entities if not is_volatile(e)]
            status_entities = [e for e in noun_entities if is_volatile(e)]
            topology_entities += [r for r in rels if not is_volatile(r)]
            status_entities += [r for r in rels if is_volatile(r)]
            cache.store_layers(env_type, topology_entities, status_entities)
        else:
            self._process_environment()

    def generate_consolidated_diagram(self):
        self.load_environment()

        destination = io.StringIO()

        def draw_instance(
            ri: RepoInstance,
            rid: RepoInstanceDeployment,
            rc: RepoClass,
            rcv: RepoClassVersion,
        ):
            print(
                f"component [instance: {ri.name}\\nclass: {rc.repo_class_name}:{rcv.version}\\nstatus: {rid.status}] as {ri.identifier.replace('-', '_')}",
                file=destination,
            )

        def draw_instance_rel(ri: RepoInstance, ri2: RepoInstance, label: str):
            label_str = f": {label}" if label else ""
            print(
                f"{ri.identifier.replace('-', '_')} --> {ri2.identifier.replace('-', '_')}{label_str}",
                file=destination,
            )

        env = self.environment
        print("@startuml", file=destination)
        rels = env.get_from_environment_has_repo_instance_hmd_lang_deployment()
        for ri in [self.rel_support.ref_to(rel) for rel in rels]:
            ri_rid = get_current_repo_instance_deployment(ri, self.rel_support)
            if ri_rid and self.rel_support.ref_to(ri_rid).status in [DEPLOYED, FAILED]:
                rid = self.rel_support.ref_to(ri_rid)
                ri_rcv = self.rel_support.ref_to(
                    rid.get_from_repo_instance_deployment_has_repo_class_version_hmd_lang_deployment()[
                        0
                    ]
                )  # type: RepoClassVersion
                rc = self.rel_support.ref_to(
                    ri.get_from_repo_instance_isa_repo_class_hmd_lang_deployment()[0]
                )  # type: RepoClass
                draw_instance(ri, rid, rc, ri_rcv)
        for ri in [self.rel_support.ref_to(rel) for rel in rels]:
            for (
                rel
            ) in ri.get_from_repo_instance_req_repo_instance_hmd_lang_deployment():
                ri_rid = get_current_repo_instance_deployment(ri, self.rel_support)
                if ri_rid and self.rel_support.ref_to(ri_rid).status == DEPLOYED:
                    ri2 = self.rel_support.ref_to(rel)
                    ri_rid = get_current_repo_instance_deployment(ri2, self.rel_support)
                    if ri_rid and self.rel_support.ref_to(ri_rid).status == DEPLOYED:
                        draw_instance_rel(ri, ri2, rel.role)
        print("@enduml", file=destination)

        return destination.getvalue()

    def add_repo_instance(
        self,
        name: str,
        deployment_id: str,
        instance_configuration: Optional[Dict],
        repo_class_version: RepoClassVersion,
        config_spec: str = None,
        hmd_region: str = None,
        dependencies=None,
        image_only: bool = False,
        auto_deploy: str = "false",
        status=DEPLOY_NEXT,
    ) -> Tuple[RepoInstance, RepoInstanceDeployment]:
        """Add a new RepoInstance, and/or a new RepoInstanceDeployment if the
        RepoInstance exists.

        :param name:
        :type name:
        :param repo_class_version:
        :type repo_class_version:
        :param config_spec:
        :param dependencies:
        :type dependencies:
        :return:
        :rtype:
        """
        if dependencies is None:
            dependencies = {}

        repo_classes = (
            self.client.get_to_repo_class_has_repo_class_version_hmd_lang_deployment(
                repo_class_version
            )
        )
        assert (
            len(repo_classes) == 1
        ), f"Multiple RepoClass's for RepoClassVersion {repo_class_version.identifier}"
        repo_class = self.rel_support.ref_from(repo_classes[0])

        instance = self.get_repo_instance(name)
        if not instance:
            instance = RepoInstance(name=name, auto_deploy=auto_deploy)
            self.client.upsert(instance)
            env_ri = EnvironmentHasRepoInstance(
                ref_from=self.environment.identifier, ref_to=instance.identifier
            )
            self.client.upsert(env_ri)

            ri_rc = RepoInstanceIsaRepoClass(
                ref_from=instance.identifier,
                ref_to=repo_class.identifier,
            )
            self.client.upsert(ri_rc)

        else:
            # Make sure the repo-class is the same as the current RepoClass for the instance...
            if instance.auto_deploy != auto_deploy:
                instance.auto_deploy = auto_deploy
                self.client.upsert(instance)

            repo_classes = (
                self.client.get_from_repo_instance_isa_repo_class_hmd_lang_deployment(
                    instance
                )
            )
            if repo_class != self.rel_support.ref_to(repo_classes[0]):
                raise ServiceException(
                    f"When adding a new RepoInstanceDeployment for an existing RepoInstance, the RepoClass must match the "
                    f"existing RepoClass for the deployment. RepoInstance {instance.name} ({instance.identifier}) "
                )

        return instance, self._do_add_repo_instance_deployment(
            instance,
            deployment_id,
            instance_configuration,
            repo_class_version,
            config_spec,
            hmd_region,
            dependencies,
            image_only,
            status,
        )

    def _synch_dependencies(
        self,
        repo_instance: RepoInstance,
        role: str,
        new_deps: List[RepoInstance],
        current_deps: List[RepoInstanceReqRepoInstance],
    ):
        # If there are new dependencies that don't currently exist, add them...
        for ri in new_deps:
            existing = [
                exi
                for exi in current_deps
                if self.rel_support.ref_to(exi).name == ri.name
            ]
            if not existing:
                ri_ri = RepoInstanceReqRepoInstance(
                    ref_from=repo_instance.identifier, ref_to=ri.identifier, role=role
                )
                self.client.upsert(ri_ri)
        # If there are existing dependencies that are not in the new list, remove them...
        for rirri in current_deps:
            current_in_new = (
                len(
                    [
                        new
                        for new in new_deps
                        if new.name == self.rel_support.ref_to(rirri).name
                    ]
                )
                > 0
            )
            if not current_in_new:
                self.client.delete(rirri)

    def _instance_is_of_class(
        self, instance: RepoInstance, class_names: set
    ) -> bool:
        """Return True when ``instance`` is a deployment of one of ``class_names``.

        The RepoClass fallback for resource-based dependency validation: a role's
        ``req_repo_class`` suggestion still satisfies the role when the required
        resource type has no producer to validate against.
        """
        if not class_names:
            return False
        for rc_rel in self.client.get_from_repo_instance_isa_repo_class_hmd_lang_deployment(
            instance
        ):
            rc = self.rel_support.ref_to(rc_rel)
            if rc is not None and rc.repo_class_name in class_names:
                return True
        return False

    def _instance_satisfies_any_resource_req(
        self, resource_information, instance: RepoInstance, reqs: List
    ) -> bool:
        """Return True when ``instance`` satisfies at least one of the resource
        requirements ``reqs`` (each a repo_class_version_req_resource_definition
        relationship) for a role — inheritance/version/tag aware (SPEC0008)."""
        for req in reqs:
            resource_definition = self.rel_support.ref_to(req)
            def_ref = {
                "resource_namespace": resource_definition.resource_namespace,
                "resource_definition_name": resource_definition.resource_definition_name,
                "version": resource_definition.version,
            }
            if resource_information.instance_satisfies_requirement(
                instance,
                def_ref,
                version_spec=req.version_spec,
                tag_selector=req.tag_selector,
            ):
                return True
        return False

    def _do_add_repo_instance_deployment(
        self,
        repo_instance: RepoInstance,
        deployment_id: str,
        instance_configuration: Dict,
        repo_class_version: RepoClassVersion,
        config_spec: str = "",
        hmd_region: str = None,
        dependencies=None,
        image_only: bool = False,
        status: str = DEPLOY_NEXT,
    ) -> RepoInstanceDeployment:
        """Create a new RepoInstanceDeployment.

        This is used in several circumstances:

        - When a new RepoClassVersion is created for a RepoClass that is deployed in a RepoInstance.
        - When a new RepoClassVersion is created for a RepoClass that contains configuration for a deployed RepoInstance.
        - When a new RepoInstance is added to an environment.
        - When a new RepoInstanceDeployment is explicitly created.

        :param repo_instance: A RepoInstance instance. Must have been upserted prior to call.
        :param repo_class_version: A RepoClassVersion that will be the version of the deployed RepoInstance
        :param config_spec: An optional configuration spec.
        :param dependencies: The dependencies to add to the RepoInstance
        :return: The created RepoInstanceDeployment
        :rtype:
        """
        if dependencies is None:
            dependencies = {}
        if not status in [
            DEPLOY_NEXT,
            DESTROY_NEXT,
            DEPLOYED,
        ]:
            raise ServiceException(
                f"Status must be one of [{DEPLOY_NEXT}, {DESTROY_NEXT}], was {status}"
            )
        logger.info(
            f"Adding new RepoInstanceDeployment for RepoInstance, {repo_instance.name}, status, {status}"
        )
        deployment_data = {
            "deployment_id": deployment_id,
            "status": status,
            "instance_configuration": instance_configuration,
            "hmd_region": hmd_region,
            "config_artifact_spec": config_spec,
        }

        # Store the Docker image used for deployment
        if "HMD_APP_IMAGE" in os.environ:
            deployment_data["deployment_image"] = os.environ["HMD_APP_IMAGE"]

        if image_only:
            deployment_data["image_only"] = "true"

        if config_spec:
            logger.debug(f"Configuration from {config_spec}.")
            config = get_configuration_artifact(config_spec)
            deployment_data["instance_configuration"] = (
                config["instance_configuration"]
                if "instance_configuration" in config
                else {}
            )
            dependencies = self.instance_names_to_refs(config.get("dependencies"))
        else:
            logger.debug("Configuration in-line.")

        if not image_only:
            # Class-name dependencies (repo_class_version_req_repo_class) and,
            # per NERD0004 SPEC0008, resource-type dependencies
            # (repo_class_version_req_resource_definition). A role may be declared
            # by either; when both declare the same role the resource requirement
            # is authoritative and the class name is only a suggestion.
            class_reqs = self.client.get_from_repo_class_version_req_repo_class_hmd_lang_deployment(
                repo_class_version
            )
            resource_reqs = self.client.get_from_repo_class_version_req_resource_definition_hmd_lang_deployment(
                repo_class_version
            )

            resource_reqs_by_role: Dict[str, List] = defaultdict(list)
            for dep in resource_reqs:
                resource_reqs_by_role[dep.role].append(dep)

            valid_roles = {dep.role for dep in class_reqs} | {
                dep.role for dep in resource_reqs
            }
            for role in dependencies:
                if role not in valid_roles:
                    raise ServiceException(
                        f"For RepoInstance, {repo_instance.name}, role with name, {role}, not in RepoClassVersion dependencies."
                    )

            required_roles = {
                dep.role
                for dep in class_reqs
                if (dep.required or "").lower() == "true"
            } | {
                dep.role
                for dep in resource_reqs
                if (dep.required or "").lower() == "true"
            }
            for role in required_roles:
                if role not in dependencies:
                    raise ServiceException(
                        f"For RepoInstance, {repo_instance.name}, required role, {role}, not provided."
                    )

            # For resource-based roles, validate each supplied instance -- strict,
            # with a RepoClass fallback. Prefer a Resource match (the instance
            # produces a compatible resource type, respecting inheritance,
            # version_spec, and tag selector). When the required resource type has
            # no producer (or the instance doesn't produce it), fall back to the
            # role's RepoClass suggestion: an instance of a suggested repo_class
            # satisfies the role. This keeps a resource requirement from hard-
            # blocking a deploy on absent producer metadata (no producer-before-
            # consumer ordering race) while still rejecting a wholly unrelated
            # instance. A role is invalid only when BOTH checks fail.
            if resource_reqs_by_role:
                from hmd_ms_deployment.resource_information import ResourceInformation

                resource_information = ResourceInformation(self.client)
                class_names_by_role: Dict[str, set] = defaultdict(set)
                for dep in class_reqs:
                    rc = self.rel_support.ref_to(dep)
                    if rc is not None:
                        class_names_by_role[dep.role].add(rc.repo_class_name)
                for role, reqs in resource_reqs_by_role.items():
                    class_names = class_names_by_role.get(role, set())
                    for instance in dependencies.get(role, []):
                        if self._instance_satisfies_any_resource_req(
                            resource_information, instance, reqs
                        ):
                            continue
                        if class_names and self._instance_is_of_class(
                            instance, class_names
                        ):
                            logger.info(
                                f"For RepoInstance, {repo_instance.name}, role, {role}: "
                                f"instance, {instance.name}, validated by RepoClass "
                                "fallback (no producing resource of the required type "
                                "found)."
                            )
                            continue
                        raise ServiceException(
                            f"For RepoInstance, {repo_instance.name}, role, {role}: "
                            f"supplied instance, {instance.name}, satisfies neither the "
                            "required resource type (namespace/name/version, "
                            "version_spec, or tag_selector) nor a suggested repo_class."
                        )

        # todo: validate version number

        rirris = (
            self.client.get_from_repo_instance_req_repo_instance_hmd_lang_deployment(
                repo_instance
            )
        )
        current_dependencies = defaultdict(list)
        for ri_ri in rirris:
            current_dependencies[ri_ri.role].append(ri_ri)

        # This new one will be handled next, so skip any that are marked as DEPLOY_NEXT...
        for rid in [
            self.rel_support.ref_to(ririd)
            for ririd in self.client.get_from_repo_instance_has_repo_instance_deployment_hmd_lang_deployment(
                repo_instance
            )
        ]:
            if rid.status in [DEPLOY_NEXT, DESTROY_NEXT]:
                mark_rid_status(self.client, rid, SKIPPED)
        logger.debug(
            f"Creating new RepoInstanceDeployment with data: {deployment_data}"
        )
        new_instance_deployment = RepoInstanceDeployment(**deployment_data)
        new_instance_deployment = self.client.upsert(new_instance_deployment)

        # If creating a DEPLOYED deployment (bootstrap case), ensure any existing current="true" is set to false
        if status == DEPLOYED:
            existing_rels = self.client.get_from_repo_instance_has_repo_instance_deployment_hmd_lang_deployment(
                repo_instance
            )
            for existing_ri_rid in existing_rels:
                if existing_ri_rid.current == "true":
                    existing_ri_rid.current = "false"
                    self.client.upsert(existing_ri_rid)

        ri_rid = RepoInstanceHasRepoInstanceDeployment(
            ref_from=repo_instance.identifier,
            ref_to=new_instance_deployment.identifier,
            # DEPLOYED only used at bootstrap time
            current="false" if status != DEPLOYED else "true",
        )
        ri_rid = self.client.upsert(ri_rid)

        rid_rc = RepoInstanceDeploymentHasRepoClassVersion(
            ref_from=new_instance_deployment.identifier,
            ref_to=repo_class_version.identifier,
        )
        rid_rc = self.client.upsert(rid_rc)

        for dependency, instances in dependencies.items():
            self._synch_dependencies(
                repo_instance, dependency, instances, current_dependencies[dependency]
            )

        # if there are any current dependencies that are not in the new deployment, them remove them...
        for cur_dep in current_dependencies:
            if cur_dep not in dependencies:
                for ri_r_ri in current_dependencies[cur_dep]:
                    self.client.delete(ri_r_ri)

        return new_instance_deployment

    def get_repo_instance(self, name: str) -> RepoInstance:
        """Find a RepoInstance in this Environment by name.

        :param name:
        :type name:
        :return:
        :rtype:
        """
        repo_instance = self._get_noun(RepoInstance, "name", name)
        if not repo_instance:
            ris = self.client.search_repo_instance_hmd_lang_deployment(
                {"attribute": "name", "operator": "=", "value": name}
            )
            for ri in ris:
                env_ris = self.client.get_to_environment_has_repo_instance_hmd_lang_deployment(
                    ri
                )
                assert (
                    len(env_ris) == 1
                ), f"RepoInstance {name} ({ri.identifier}) is in multiple environments."
                if self.rel_support.ref_from(env_ris[0]) == self.environment:
                    self.cache_noun(ri)
                    repo_instance = ri
                    break

        return repo_instance

    def get_repo_class(self, repo_class_name: str) -> Optional[RepoClass]:
        return self._get_noun(RepoClass, "repo_class_name", repo_class_name)

    def _get_noun(
        self, type_: Type[Noun], attr_name: str, attr_value: str
    ) -> Optional[Noun]:
        result = [
            noun
            for noun in self.nouns[type_].values()
            if getattr(noun, attr_name) == attr_value
        ]
        if result:
            return result[0]
        else:
            return None

    def add_new_repo_instance_deployments(
        self, repo_class: RepoClass, repo_class_version: RepoClassVersion
    ) -> None:
        """
        Add a new RepoInstanceDeployment to any RepoInstance in this environment that is of
        the specified RepoClass.

        The information for the new deployment is taken from the currently deployed instance.
        If the configuration for the new RepoClassVersion has changed sufficiently, this might fail.
        For example, if a new dependency is added to the new RepoClassVersion, then it is not possible
        to know what value to use for the new dependency.
        """

        def get_dependencies(
            instance: RepoInstance,
        ) -> Dict[str, List[RepoInstance]]:
            result = defaultdict(list)
            riris = self.client.get_from_repo_instance_req_repo_instance_hmd_lang_deployment(
                instance
            )
            self.rel_support.pull_relationship_nouns(riris)
            for rirri in riris:
                result[rirri.role].append(self.rel_support.ref_to(rirri))

            return result

        # look at all instances in the environment
        rels = self.client.get_from_environment_has_repo_instance_hmd_lang_deployment(
            self.environment
        )
        self.rel_support.pull_relationship_nouns(rels)
        for instance in [
            self.rel_support.ref_to(rel) for rel in rels
        ]:  # type: RepoInstance
            if repo_class == self.rel_support.ref_to(
                self.client.get_from_repo_instance_isa_repo_class_hmd_lang_deployment(
                    instance
                )[0]
            ):
                ri_rids = self.client.get_from_repo_instance_has_repo_instance_deployment_hmd_lang_deployment(
                    instance
                )
                self.rel_support.pull_relationship_nouns(ri_rids)

                source: RepoInstanceDeployment = None
                for ri_rid in ri_rids:
                    if (
                        ri_rid.current == "true"
                        and self.rel_support.ref_to(ri_rid).status == DEPLOYED
                    ):
                        source = self.rel_support.ref_to(ri_rid)

                # source is the one that is currently deployed...
                if source:
                    source_data = source.serialize(encode_blobs=False)
                    del source_data["identifier"]
                    source_data["status"] = DEPLOY_NEXT

                    self._do_add_repo_instance_deployment(
                        instance,
                        source.deployment_id,
                        source.instance_configuration,
                        repo_class_version,
                        hmd_region=source.hmd_region,
                        dependencies=get_dependencies(instance),
                    )

    def update_instance_deployment_status(
        self, rid: RepoInstanceDeployment, status: str
    ):
        """
        Update the status of a RepoInstance, making sure to leave the Environment deployments in a valid state.
        """
        if status not in RID_STATUS_OPTIONS:
            raise ServiceException(
                f"status must be one of {', '.join(RID_STATUS_OPTIONS)}; was {status}"
            )

        ri: RepoInstance = self.rel_support.ref_from(
            self.client.get_to_repo_instance_has_repo_instance_deployment_hmd_lang_deployment(
                rid
            )[
                0
            ]
        )
        now_ = datetime.utcnow()
        rels: List[RepoInstanceHasRepoInstanceDeployment] = (
            self.client.get_from_repo_instance_has_repo_instance_deployment_hmd_lang_deployment(
                ri
            )
        )
        logger.debug("RELS: %d", len(rels))
        self.rel_support.pull_relationship_nouns(rels)

        # Single pass: update both relationships and deployment nodes together
        for ri_rid in rels:
            ref_to_id = ri_rid.ref_to if isinstance(ri_rid.ref_to, str) else ri_rid.ref_to.identifier
            deployment = self.rel_support.ref_to(ri_rid)
            logger.debug("CURRENT: %s (%s)", ri_rid.current, ref_to_id)
            if ref_to_id == rid.identifier:
                # This is the deployment we're activating
                ri_rid.current = "true"
                self.client.upsert(ri_rid)

                rid.status = status
                rid.start = now_
                if status == DESTROYED:
                    rid.end = now_
                self.client.upsert(rid)
            elif ri_rid.current == "true":
                # This was previously current, deactivate it
                logger.debug("Deactivating old relationship: (%s)", ri_rid.identifier)
                ri_rid.current = "false"
                self.client.upsert(ri_rid)

                # Set end time on the previously current deployment
                if deployment.end is None:
                    deployment.end = now_
                    self.client.upsert(deployment)

        if status == DESTROYED:
            self.client.delete(ri)

        # NERD0005 write-through: keep the volatile status layer warm without
        # ever touching the expensive topology layer, so a busy deployment stream
        # of status callbacks does not churn the cache.
        cache = get_env_cache()
        if cache.is_enabled:
            env_type = self.environment.type
            if status == DESTROYED:
                # The RepoInstance (a topology noun) was just deleted, so both
                # layers for this environment are now stale.
                cache.invalidate_environment(env_type)
            else:
                # Rebuild only this instance's current-or-NEXT slice in place.
                volatile: List = []
                for ri_rid in rels:
                    dep = self.rel_support.ref_to(ri_rid)
                    if ri_rid.current == "true" or dep.status in (
                        DEPLOY_NEXT,
                        DESTROY_NEXT,
                    ):
                        volatile.append(ri_rid)
                        volatile.append(dep)
                        volatile.extend(
                            self.client.get_from_repo_instance_deployment_has_repo_class_version_hmd_lang_deployment(
                                dep
                            )
                        )
                cache.splice_status(env_type, ri.identifier, volatile)

    def instance_names_to_refs(
        self, in_dict: Union[Dict[str, str], Dict[str, List[str]]]
    ):
        """A convenience method to convert a map of "role":"instance_name" to "role":"instance".

        Supports multiple resource name formats per NERD0002:
        - Full: ns:<customer_code>:<environment>:<repo_class_name>:<repo_class_version>:<instance_name>:<deployment_id>
        - Shorthand: ns:<instance_name>:<deployment_id>
        - Plain: <instance_name> (backwards compatible)
        """
        from hmd_ms_deployment.resource_name import parse_resource_name

        if in_dict is None:
            return None
        for role in in_dict.keys():
            if isinstance(in_dict[role], str):
                in_dict[role] = [in_dict[role]]
            for resource_name_str in in_dict[role]:
                # Parse the resource name to extract instance_name
                parsed = parse_resource_name(resource_name_str)

                # Find the RepoInstance by name in this environment
                repo_instance = self.get_repo_instance(parsed.instance_name)
                assert (
                    repo_instance is not None
                ), f"No repo instance found for name, {parsed.instance_name} (from resource name: {resource_name_str})"

                # Optional: validate deployment_id if provided (for shorthand/full format)
                if parsed.deployment_id:
                    current_rid_rel = get_current_repo_instance_deployment(repo_instance, self.rel_support)
                    if current_rid_rel:
                        current_rid = self.rel_support.ref_to(current_rid_rel)
                        if current_rid.deployment_id != parsed.deployment_id:
                            logger.warning(
                                f"Resource name '{resource_name_str}' specifies deployment_id '{parsed.deployment_id}' "
                                f"but current deployment has deployment_id '{current_rid.deployment_id}'"
                            )

        return {
            role: [self.get_repo_instance(parse_resource_name(n).instance_name) for n in names]
            for role, names in in_dict.items()
        }

    def get_repo_instance_history(self, name: str) -> Dict:
        ri = self.get_repo_instance(name)

        rels = self.client.get_from_repo_instance_has_repo_instance_deployment_hmd_lang_deployment(
            ri
        )
        self.rel_support.pull_relationship_nouns(
            rels,
        )

        deployments = [self.rel_support.ref_to(r).serialize() for r in rels]
        deployments = sorted(
            deployments, key=lambda k: k.get("start", ""), reverse=True
        )

        return {
            "repo_instance": ri.serialize(),
            "history": deployments,
        }

    def get_repo_instance_config(self, name: str) -> Dict:
        ri = self.get_repo_instance(name)
        rid = get_current_repo_instance_deployment(ri, self.rel_support)
        if not rid:
            raise ServiceException(
                f"RepoInstance {name} does not have a current deployment."
            )
        rid = self.rel_support.ref_to(rid)
        return rid.serialize(encode_blobs=False).get("instance_configuration", {})
