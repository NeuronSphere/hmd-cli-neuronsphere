import copy
import io
from collections import defaultdict
from typing import Dict, List, Optional

from hmd_cli_tools import ServiceException
from hmd_lang_deployment.repo_class_version_has_repo_class_version_notes import (
    RepoClassVersionHasRepoClassVersionNotes,
)
from hmd_lang_deployment.repo_class_version_notes import RepoClassVersionNotes
from hmd_lang_deployment.repo_instance_req_repo_instance import (
    RepoInstanceReqRepoInstance,
)
from hmd_lang_deployment.repo_instance_deployment import RepoInstanceDeployment
from hmd_lang_deployment.repo_instance import RepoInstance
from hmd_lang_deployment.environment import Environment
from hmd_graphql_client import BaseClient
from hmd_graphql_client.relationship_support import RelationshipSupport
from hmd_meta_types import Noun, Relationship, Entity
from hmd_lang_deployment.hmd_lang_deployment_client import HmdLangDeploymentClient
from hmd_lang_deployment.repo_class import RepoClass
from hmd_lang_deployment.repo_class_has_repo_class_version import (
    RepoClassHasRepoClassVersion,
)
from hmd_lang_deployment.repo_class_version import RepoClassVersion
from hmd_lang_deployment.repo_class_version_req_repo_class import (
    RepoClassVersionReqRepoClass,
)
from hmd_lang_deployment.repo_class_version_req_resource_definition import (
    RepoClassVersionReqResourceDefinition,
)
from hmd_lang_deployment.external_artifact import ExternalArtifact
from hmd_lang_deployment.repo_class_version_has_external_artifact import (
    RepoClassVersionHasExternalArtifact,
)


def build_relationship_support(client: BaseClient) -> RelationshipSupport:
    result = RelationshipSupport()
    result.register_client(client)
    return result


def draw_puml_instance(
    entity: Noun, attributes: List[str], destination, color, ids: bool = False
):
    attr_values = ", ".join([getattr(entity, attr) for attr in attributes])
    attr_str = f"\\n{attr_values}" if attr_values else ""
    id_trunc = entity.identifier.rsplit("-", maxsplit=1)[1]
    id_str = f" ({id_trunc if ids else ''})"
    print(
        f"component [{entity.__class__.__name__}{id_str}{attr_str}] as "
        "{entity.identifier.replace('-','_')} #{color}",
        file=destination,
    )


def draw_puml_relationship(
    relationship: Relationship, destination, attributes: List[str]
):
    type_str = "\\n".join([getattr(relationship, attr) for attr in attributes])
    type_str = f": {type_str}" if type_str else ""
    print(
        f"{relationship.ref_from.replace('-','_')} --> "
        f"{relationship.ref_to.replace('-','_')}{type_str}",
        file=destination,
    )


def generate_full_diagram(
    nouns: Dict,
    relationships: Dict,
    ids: bool = False,
):
    attrs_to_display = {
        RepoClass: ["repo_class_name"],
        RepoClassVersion: ["version"],
        Environment: ["type"],
        RepoInstance: ["name"],
        RepoInstanceDeployment: ["deployment_id", "status"],
    }
    rel_attrs_to_display = {
        RepoInstanceReqRepoInstance: ["role"],
        RepoClassVersionReqRepoClass: ["required", "version_spec", "role"],
        RepoClassVersionReqResourceDefinition: [
            "required",
            "version_spec",
            "role",
            "tag_selector",
        ],
    }

    colors = [
        "Red",
        "Pink",
        "LightGrey",
        "Green",
        "LightBlue",
        "LightGreen",
        "Purple",
    ]

    nouns_to_display = [noun for type_ in nouns.values() for noun in type_.values()]
    types = set([noun.__class__ for noun in nouns_to_display])
    color_map = {type_: color for type_, color in zip(types, colors[: len(types)])}

    dest = io.StringIO()
    print("@startuml", file=dest)
    for noun in nouns_to_display:
        draw_puml_instance(
            noun,
            attrs_to_display.get(noun.__class__, []),
            dest,
            color_map[noun.__class__],
            ids=ids,
        )

    rels = [rel for type_ in relationships.values() for rel in type_.values()]
    for rel in rels:
        draw_puml_relationship(rel, dest, rel_attrs_to_display.get(rel.__class__, []))
    print("@enduml", file=dest)
    return dest.getvalue()


class ClassInformation:
    def __init__(self, client: HmdLangDeploymentClient):
        self.client = client
        self.nouns = defaultdict(dict)
        self.relationships = defaultdict(dict)
        self.relationship_support = build_relationship_support(client._base_client)

    def cache_noun(self, noun: Noun):
        self.nouns[type(noun)][noun.identifier] = noun

    def cache_relationship(self, relationship: Relationship):
        self.relationships[type(relationship)][relationship.identifier] = relationship

    def upsert(self, entity: Entity):
        self.client._base_client.upsert_entity(entity)
        if isinstance(entity, Noun):
            self.cache_noun(entity)
        else:
            self.cache_relationship(entity)

    def _process_repo_classes(self):
        for rc in self.client.search_repo_class_hmd_lang_deployment({}):
            self.cache_noun(rc)
            for (
                rc_rcv
            ) in self.client.get_from_repo_class_has_repo_class_version_hmd_lang_deployment(
                rc
            ):
                self.cache_relationship(rc_rcv)
                self._process_class_version(self.relationship_support.ref_to(rc_rcv))

    def _process_class_version(self, repo_class_version: RepoClassVersion):
        self.cache_noun(repo_class_version)

        for (
            rel
        ) in self.client.get_from_repo_class_version_req_repo_class_hmd_lang_deployment(
            repo_class_version
        ):
            self.cache_relationship(rel)
            self.cache_noun(self.relationship_support.ref_to(rel))

        # NERD0004 SPEC0008: cache resource-based dependency edges and their
        # target ResourceDefinitions alongside the class-name dependencies.
        for (
            rel
        ) in self.client.get_from_repo_class_version_req_resource_definition_hmd_lang_deployment(
            repo_class_version
        ):
            self.cache_relationship(rel)
            self.cache_noun(self.relationship_support.ref_to(rel))

    def get_repo_class(self, repo_class_name: str) -> RepoClass:
        """Retrieve a RepoClass opbject with the given ``repo_class_name``.

        :param repo_class_name: The name of the RepoClass.
        :type repo_class_name: str
        :return:
        :rtype: RepoClass
        """
        rcs: List[RepoClass] = self.client.search_repo_class_hmd_lang_deployment(
            {"attribute": "repo_class_name", "operator": "=", "value": repo_class_name}
        )
        assert (
            len(rcs) <= 1
        ), f"Found {len(rcs)} RepoClass's with repo_class_name {repo_class_name}. Expected 0 or 1"
        if not (len(rcs)) == 1:
            raise ServiceException(f"RepoClass, {repo_class_name}, not found.")

        return rcs[0]

    def add_repo_version(
        self,
        repo_class: RepoClass,
        version: str,
        dependencies: Dict,
        default_configuration: Dict,
        version_notes: Optional[RepoClassVersionNotes] = None,
        external_artifacts: Optional[List[ExternalArtifact]] = [],
        discovery: Optional[Dict] = None,
    ) -> RepoClassVersion:
        """Add a new RepoClassVersion to a RepoClass.

        The dependencies argument is of the form::

          {
            "repo_class": a_repo_class,
            "version": "1.2.3",
            "dependencies": {
              "dep1": {
                "repo_class": a_repo_class,
                "required": "true" | "false",
                "version_spec": "~= 0.1"
              },
              "dep2" {...},
              ...
            }
          }

        :param default_configuration:
        :type default_configuration:
        :param repo_class: The event initiating the request.
        :type repo_class: RepoClass
        :param version: The new version string.
        :type version: str
        :param dependencies: The dependencies to put in place for the new version.
        :type dependencies: Dict[str, Dict]
        :param version_notes: The release notes for the new version
        :type version_notes: Optional[RepoClassVersionNotes]
        :param external_artifacts: Any build artifacts stored outside of the Artifact Librarian
        :type external_artifacts: Optional[List[ExternalArtifact]]
        :param discovery: BACON discovery metadata (summary, entry_points, capabilities,
            related_docs) copied from the repo class's manifest.json.
        :type discovery: Optional[Dict]
        :returns: The newly created RepoClassVersion object
        :rtype: RepoClassVersion
        """
        rc_rcv_rels = (
            self.client.get_from_repo_class_has_repo_class_version_hmd_lang_deployment(
                repo_class
            )
        )
        if any(
            rcv.version == version
            for rcv in [self.relationship_support.ref_to(rel) for rel in rc_rcv_rels]
        ):
            raise ServiceException(
                f"RepoClass, {repo_class.repo_class_name}, already has version {version}."
            )

        new_version = RepoClassVersion(
            version=version,
            default_configuration=default_configuration,
            discovery=discovery,
        )
        self.client.upsert(new_version)

        if version_notes:
            version_notes = self.client.upsert(version_notes)
            rcv_rcvn = RepoClassVersionHasRepoClassVersionNotes(
                ref_from=new_version.identifier, ref_to=version_notes.identifier
            )
            self.client.upsert(rcv_rcvn)

        for external_artifact in external_artifacts:
            external_artifact = self.client.upsert(external_artifact)
            rcv_ea = RepoClassVersionHasExternalArtifact(
                ref_from=new_version.identifier, ref_to=external_artifact.identifier
            )
            self.client.upsert(rcv_ea)

        rc_rv = RepoClassHasRepoClassVersion(
            ref_from=repo_class.identifier, ref_to=new_version.identifier
        )
        self.client.upsert_repo_class_has_repo_class_version_hmd_lang_deployment(rc_rv)

        self._wire_dependencies(new_version, dependencies)

        return new_version

    @staticmethod
    def _ref_to_id(rel) -> str:
        return rel.ref_to if isinstance(rel.ref_to, str) else rel.ref_to.identifier

    def _wire_dependencies(
        self, repo_class_version: RepoClassVersion, dependencies: Dict
    ) -> None:
        """Create the dependency edges (class-name and/or resource-type) for
        every role in ``dependencies`` against an existing ``repo_class_version``.

        Each role is wired independently: one role's failure does not stop the
        rest from being wired. Failures are collected and raised together as a
        single ``ServiceException`` so a bad role is reported completely and
        immediately, instead of silently truncating every role declared after
        it in manifest.json dict order -- which would otherwise leave a
        RepoClassVersion permanently short a role, since re-registering the
        same version number is rejected (see ``add_repo_version``). This is
        not full atomicity: edges already upserted for other roles in this
        call stay committed even if one role fails.

        Idempotent: an edge already present for a given (role, target) is left
        alone rather than duplicated. The storage layer does not dedupe
        relationship upserts on its own (a freshly-constructed relationship
        gets its own identifier regardless of any existing edge with the same
        ref_from/ref_to/role), so this is required for
        ``resync_repo_class_version_dependencies`` to be safely re-callable
        against a version that's already partially or fully wired.
        """
        existing_class_reqs = {
            (rel.role, self._ref_to_id(rel))
            for rel in self.client.get_from_repo_class_version_req_repo_class_hmd_lang_deployment(
                repo_class_version
            )
        }
        existing_resource_reqs = {
            (rel.role, self._ref_to_id(rel))
            for rel in self.client.get_from_repo_class_version_req_resource_definition_hmd_lang_deployment(
                repo_class_version
            )
        }

        failures: List[str] = []
        for role in dependencies:
            dep = dependencies[role]
            try:
                # A role that names a RepoClass gets the class-name dependency
                # edge. When the same role also declares a resource (below),
                # the resource requirement is authoritative and this edge is
                # retained as a *suggestion* (NERD0004 SPEC0008).
                if dep.get("repo_class") is not None:
                    ref_to = dep["repo_class"].identifier
                    if (role, ref_to) not in existing_class_reqs:
                        new_rel = RepoClassVersionReqRepoClass(
                            ref_from=repo_class_version.identifier,
                            ref_to=ref_to,
                            required=dep.get("required"),
                            version_spec=dep.get("version_spec"),
                            role=role,
                        )
                        self.client.upsert(new_rel)
                        self.cache_relationship(new_rel)

                # NERD0004 SPEC0008: a role may declare a resource-type
                # dependency. When present, it overrides the class-name
                # dependency; any RepoClass that produces the required
                # ResourceDefinition (or a subtype) with matching
                # version/tags can satisfy it.
                resource = dep.get("resource")
                if resource is not None:
                    self._add_resource_dependency(
                        repo_class_version, role, dep, resource, existing_resource_reqs
                    )
            except Exception as ex:
                failures.append(f"{role}: {ex}")

        if failures:
            raise ServiceException(
                "Failed to wire RepoClassVersion dependencies for role(s): "
                + "; ".join(failures)
            )

    def _add_resource_dependency(
        self,
        repo_class_version: RepoClassVersion,
        role: str,
        dep: Dict,
        resource: Dict,
        existing_resource_reqs: Optional[set] = None,
    ) -> None:
        """Create the authoritative ``repo_class_version_req_resource_definition``
        edge for a role that declares a resource-type dependency."""
        # Imported lazily to avoid a circular import (resource_information
        # imports build_relationship_support from this module).
        from .resource_information import ResourceInformation

        resource_namespace = resource.get("resource_namespace")
        resource_definition_name = resource.get("resource_definition_name")
        version = resource.get("version")
        missing = [
            name
            for name, value in (
                ("resource_namespace", resource_namespace),
                ("resource_definition_name", resource_definition_name),
                ("version", version),
            )
            if not value
        ]
        if missing:
            raise ServiceException(
                f"For role, {role}, 'resource' is missing required field(s): "
                f"{', '.join(missing)}."
            )

        resource_information = ResourceInformation(self.client)
        rd = resource_information.find_resource_definition(
            resource_namespace, resource_definition_name, version
        )
        if rd is None:
            # Tolerant registration: the referenced ResourceDefinition may not be
            # registered yet (shared local<->cloud manifests, producer-before-
            # consumer ordering). Create a minimal stub via the find-first upsert
            # so the authoritative definition later reconciles into the SAME row
            # (no duplicate) and enriches it. Apply-time validation still enforces
            # a real producer before anything deploys.
            rd = resource_information.upsert_resource_definition(
                resource_namespace, resource_definition_name, version
            )

        if existing_resource_reqs is not None and (
            role,
            rd.identifier,
        ) in existing_resource_reqs:
            return

        res_rel = RepoClassVersionReqResourceDefinition(
            ref_from=repo_class_version.identifier,
            ref_to=rd.identifier,
            required=dep.get("required"),
            version_spec=resource.get("version_spec"),
            role=role,
            tag_selector=resource.get("tag_selector"),
        )
        self.client.upsert(res_rel)
        self.cache_relationship(res_rel)

    def add_repo_version_by_name(
        self,
        repo_class_name: str,
        version: str,
        dependencies: Dict,
        default_configuration: Dict,
        version_notes: Optional[RepoClassVersionNotes] = None,
        external_artifacts: Optional[List[ExternalArtifact]] = [],
        discovery: Optional[Dict] = None,
    ) -> RepoClassVersion:
        """Add a new RepoClassVersion to a RepoClass.

        The dependencies argument is of the form::

          {
            "repo_class": "a_repo_class",
            "version": "1.2.3",
            "dependencies": {
              "dep1": {
                "repo_class_name": a_repo_class,
                "required": "true" | "false",
                "version_spec": "~= 0.1"
              },
              "dep2" {...},
              ...
            }
          }

        :param repo_class_name: The event initiating the request.
        :type repo_class_name: str
        :param version: The new version string.
        :type version: str
        :param dependencies: The dependencies to put in place for the new version.
        :type dependencies: Dict[str, Dict]
        :param version_notes: The release notes for the new version
        :type version_notes: Optional[RepoClassVersionNotes]
        :param external_artifacts: Any build artifacts stored outside of the Artifact Librarian
        :type external_artifacts: Optional[List[ExternalArtifact]]
        :param discovery: BACON discovery metadata (summary, entry_points, capabilities,
            related_docs) copied from the repo class's manifest.json.
        :type discovery: Optional[Dict]
        :returns: The newly created RepoClassVersion object
        :rtype: RepoClassVersion
        """
        try:
            repo_class = self.get_repo_class(repo_class_name)
        except ServiceException:
            repo_class = RepoClass(repo_class_name=repo_class_name)
            self.upsert(repo_class)

        self._resolve_dependency_classes(dependencies)

        return self.add_repo_version(
            repo_class,
            version,
            dependencies,
            default_configuration,
            version_notes,
            external_artifacts,
            discovery,
        )

    def _resolve_dependency_classes(self, dependencies: Dict) -> None:
        """Resolve each role's ``repo_class_name`` to a ``RepoClass`` object,
        mutating ``dependencies`` in place by setting ``dep["repo_class"]``.

        NERD0004 SPEC0008: a role may instead (or additionally) declare a
        ``resource`` block; when a named RepoClass isn't registered locally but
        a resource is present, the class name degrades to a non-resolvable
        suggestion rather than failing outright.
        """
        for role in dependencies:
            dep = dependencies[role]
            if dep.get("repo_class_name"):
                try:
                    dep["repo_class"] = self.get_repo_class(dep["repo_class_name"])
                except ServiceException:
                    # NERD0004 SPEC0008: when the role also declares a resource,
                    # the resource requirement is authoritative and the
                    # repo_class_name degrades to a (non-resolvable) suggestion.
                    # Shared local↔cloud manifests keep repo_class_name for the
                    # cloud path even when that class isn't registered locally.
                    if "resource" not in dep:
                        # Tolerant registration: create the referenced RepoClass as
                        # a stub via the same find-first get-or-create used for the
                        # top-level class, so a later authoritative registration of
                        # that repo reuses the SAME row (no duplicate). Apply-time
                        # validation still enforces a real instance before deploy.
                        stub = RepoClass(repo_class_name=dep["repo_class_name"])
                        self.upsert(stub)
                        dep["repo_class"] = stub
            elif "resource" not in dep:
                # NERD0004 SPEC0008: a role must declare either a RepoClass
                # (by name) or a resource-type dependency.
                raise ServiceException(
                    f"For role, {role}, neither 'repo_class_name' nor 'resource' provided."
                )

    def resync_repo_class_version_dependencies(
        self, repo_class_name: str, version: str, dependencies: Dict
    ) -> RepoClassVersion:
        """Re-wire an EXISTING RepoClassVersion's dependency edges from a
        (possibly updated) ``dependencies`` block, without creating a new
        version.

        Registration can leave a RepoClassVersion permanently short a role's
        edges if that role's wiring failed when the version was first
        registered (see ``_wire_dependencies``), and re-registering the same
        version number is rejected outright by ``add_repo_version``. This
        gives operators a way to repair that gap in place: edge upserts are
        idempotent, so calling this repeatedly -- or against a version that's
        already fully wired -- is safe and just re-affirms what's correct.

        :param repo_class_name: The RepoClass the version belongs to.
        :param version: The existing version to resync.
        :param dependencies: The manifest ``deploy.dependencies`` block (by
            ``repo_class_name``, mirroring ``add_repo_version_by_name``).
        :returns: The existing RepoClassVersion.
        """
        repo_class_version = self.get_repo_class_version(repo_class_name, version)
        dependencies = copy.deepcopy(dependencies)
        self._resolve_dependency_classes(dependencies)
        self._wire_dependencies(repo_class_version, dependencies)
        return repo_class_version

    def get_repo_class_version(self, repo_class_name: str, version: str):
        rc = self.get_repo_class(repo_class_name)
        rcvs = (
            self.client.get_from_repo_class_has_repo_class_version_hmd_lang_deployment(
                rc
            )
        )
        rcvs = [
            self.relationship_support.ref_to(rcv)
            for rcv in rcvs
            if self.relationship_support.ref_to(rcv).version == version
        ]
        if len(rcvs) == 0:
            raise ServiceException(
                f"No version found for {repo_class_name} : {version}."
            )
        assert (
            len(rcvs) == 1
        ), f"Multiple versions found for {repo_class_name} : {version}."

        return rcvs[0]

    def generate_diagram(self):
        self._process_repo_classes()
        return generate_full_diagram(self.nouns, self.relationships)
