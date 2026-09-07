import logging
from pathlib import Path
from typing import Dict, List, Optional

import yaml
from hmd_cli_tools import ServiceException
from hmd_graphql_client.relationship_support import RelationshipSupport
from hmd_lang_deployment.hmd_lang_deployment_client import HmdLangDeploymentClient
from hmd_lang_deployment.repo_class_version import RepoClassVersion
from hmd_lang_deployment.repo_class_version_has_resource_definition import (
    RepoClassVersionHasResourceDefinition,
)
from hmd_lang_deployment.repo_class_version_produces_resource_definition import (
    RepoClassVersionProducesResourceDefinition,
)
from hmd_lang_deployment.repo_instance import RepoInstance
from hmd_lang_deployment.repo_instance_deployment import RepoInstanceDeployment
from hmd_lang_deployment.repo_instance_deployment_has_resource import (
    RepoInstanceDeploymentHasResource,
)
from hmd_lang_deployment.resource import Resource
from hmd_lang_deployment.resource_definition import ResourceDefinition
from hmd_lang_deployment.resource_definition_isa_resource_definition import (
    ResourceDefinitionIsaResourceDefinition,
)
from hmd_lang_deployment.resource_has_resource_tag import ResourceHasResourceTag
from hmd_lang_deployment.resource_isa_resource_definition import (
    ResourceIsaResourceDefinition,
)
from hmd_lang_deployment.resource_tag import ResourceTag
from jsonschema import ValidationError
from jsonschema import validate as jsonschema_validate

from hmd_ms_deployment import DEPLOYED, DESTROYED

from .class_information import build_relationship_support
from .version import VersionSpecifier, VersionSpecifierException

LOGGER = logging.getLogger(f"HMD.{__name__}")


def parse_tag_selector(raw: Optional[str]) -> List[tuple]:
    """Parse a Kubernetes-style equality selector ``'key=value,key=value'`` into
    a list of ``(key, value)`` pairs. Blank/malformed pairs are skipped.
    """
    items: List[tuple] = []
    for pair in (raw or "").split(","):
        pair = pair.strip()
        if not pair or "=" not in pair:
            continue
        key, value = pair.split("=", 1)
        items.append((key.strip(), value.strip()))
    return items


def version_matches(version: str, version_spec: Optional[str]) -> bool:
    """Return True when ``version`` satisfies ``version_spec`` (``~= 0.1`` etc.).

    An empty/None spec matches any version. Uses the same
    :class:`VersionSpecifier` semantics as class-name dependency resolution.
    """
    if not version_spec:
        return True
    try:
        VersionSpecifier(version_spec).validate(version)
        return True
    except (VersionSpecifierException, AssertionError):
        return False


BASE_RESOURCES_DIR = Path(__file__).parent / "base_resources"

_BASE_REQUIRED_KEYS = ("resource_namespace", "resource_definition_name", "version")


def _def_key(ref: Dict) -> tuple:
    """The (namespace, name, version) identity tuple of a definition/parent ref."""
    return (
        ref["resource_namespace"],
        ref["resource_definition_name"],
        ref["version"],
    )


def load_base_resource_documents(directory: Optional[Path] = None) -> List[Dict]:
    """Load the bundled base ResourceDefinition documents, ordered parent-first.

    Reads every ``*.yaml`` under ``directory`` (defaults to the packaged
    ``base_resources/`` directory), validates the required identity keys, and
    returns the documents topologically sorted so that a definition always
    appears after its ``parent`` (roots first). This ordering lets the seeder
    upsert each definition knowing its parent already exists.

    Raises :class:`ServiceException` if a document is missing a required key, if a
    ``parent`` references a definition not present in the catalog, or if the
    ``parent`` links form a cycle.
    """
    directory = directory or BASE_RESOURCES_DIR
    docs: List[Dict] = []
    for path in sorted(Path(directory).glob("*.yaml")):
        with open(path, "r") as fl:
            doc = yaml.safe_load(fl)
        if not doc:
            continue
        missing = [k for k in _BASE_REQUIRED_KEYS if not doc.get(k)]
        if missing:
            raise ServiceException(
                f"Base resource definition {path.name} is missing required "
                f"key(s): {', '.join(missing)}."
            )
        docs.append(doc)

    by_key = {_def_key(doc): doc for doc in docs}

    ordered: List[Dict] = []
    emitted = set()
    visiting = set()

    def emit(doc: Dict) -> None:
        key = _def_key(doc)
        if key in emitted:
            return
        if key in visiting:
            raise ServiceException(
                f"Cycle detected in base resource definition 'parent' links at "
                f"{key[0]}/{key[1]}/{key[2]}."
            )
        parent = doc.get("parent")
        if parent is not None:
            parent_key = _def_key(parent)
            if parent_key not in by_key:
                raise ServiceException(
                    f"Base resource definition {key[0]}/{key[1]}/{key[2]} references "
                    f"parent {parent_key[0]}/{parent_key[1]}/{parent_key[2]} which is "
                    f"not part of the base catalog."
                )
            visiting.add(key)
            emit(by_key[parent_key])
            visiting.discard(key)
        emitted.add(key)
        ordered.append(doc)

    for doc in docs:
        emit(doc)
    return ordered


class ResourceInformation:
    """Manages ResourceDefinition, Resource and ResourceTag entities (NERD0004).

    ResourceDefinitions are CRD-like schema documents describing the outputs a
    RepoClass produces. Resources are the concrete, deployment-time outputs typed
    by a ResourceDefinition and linked to the producing RepoInstanceDeployment.
    """

    def __init__(self, client: HmdLangDeploymentClient):
        self.client = client
        self.relationship_support: RelationshipSupport = build_relationship_support(
            client._base_client
        )

    # ------------------------------------------------------------------ #
    # ResourceDefinition management
    # ------------------------------------------------------------------ #

    def find_resource_definition(
        self, resource_namespace: str, resource_definition_name: str, version: str
    ) -> Optional[ResourceDefinition]:
        """Find a ResourceDefinition by its (namespace, name, version) business identity.

        The storage layer does not enforce composite business ids, so the lookup
        searches a single attribute and filters the remainder in Python.
        """
        candidates: List[
            ResourceDefinition
        ] = self.client.search_resource_definition_hmd_lang_deployment(
            {
                "attribute": "resource_namespace",
                "operator": "=",
                "value": resource_namespace,
            }
        )
        matches = [
            rd
            for rd in candidates
            if rd.resource_definition_name == resource_definition_name
            and rd.version == version
        ]
        assert len(matches) <= 1, (
            f"Found {len(matches)} ResourceDefinitions for "
            f"{resource_namespace}/{resource_definition_name}/{version}. Expected 0 or 1"
        )
        return matches[0] if matches else None

    def get_resource_definition(self, identifier: str) -> Optional[ResourceDefinition]:
        return self.client.get_resource_definition_hmd_lang_deployment(identifier)

    def list_resource_definitions(
        self, resource_namespace: Optional[str] = None
    ) -> List[ResourceDefinition]:
        if resource_namespace:
            filter_ = {
                "attribute": "resource_namespace",
                "operator": "=",
                "value": resource_namespace,
            }
        else:
            filter_ = {}
        rds = self.client.search_resource_definition_hmd_lang_deployment(filter_)
        return self._sort_resource_definitions(rds)

    def list_resources(
        self,
        limit: int = 50,
        offset: int = 0,
        environment_type: Optional[str] = None,
    ):
        """List deployed Resources, sorted and paginated.

        Returns ``(page, total)`` — ``page`` is the slice of Resource entities for
        the requested window, ``total`` the full count. When ``environment_type`` is
        given, only resources deployed in that environment are listed (via the
        ``environment -> repo_instance -> repo_instance_deployment -> resource`` walk
        in :meth:`get_resources_for_environment`). Without it, mirrors
        ``list_resource_definitions``: an empty-filter search returns every
        ``Resource`` node in one indexed-by-type query.

        Scalability note: the generic entity engine has no SQL LIMIT/OFFSET, so this
        fetches and deserializes the full result set then slices in Python. Fine for
        current volumes; if resource counts grow large, replace the unscoped branch
        with a raw-SQL ``LIMIT/OFFSET`` + ``COUNT(*)`` on the ``entity`` table filtered
        on ``name = 'hmd_lang_deployment.resource'`` (see ``list_change_set_deployments``
        in deployment_ops.py for that pattern).
        """
        if environment_type:
            resources = self.get_resources_for_environment(environment_type)
        else:
            resources = self.client.search_resource_hmd_lang_deployment({})
        resources.sort(key=lambda r: (r.resource_name or "").lower())
        total = len(resources)
        page = resources[offset : offset + limit]
        return page, total

    def upsert_resource_definition(
        self,
        resource_namespace: str,
        resource_definition_name: str,
        version: str,
        description: Optional[str] = None,
        resource_metadata: Optional[Dict] = None,
        output_schema: Optional[Dict] = None,
        parent_ref: Optional[Dict] = None,
        has_rcv_id: Optional[str] = None,
        produced_by_rcv_id: Optional[str] = None,
        role: Optional[str] = None,
    ) -> ResourceDefinition:
        """Idempotently create or update a ResourceDefinition.

        ``parent_ref`` (a ``{resource_namespace, resource_definition_name, version}``
        dict) establishes single inheritance via ``resource_definition_isa_resource_definition``.
        ``has_rcv_id`` records provenance (the RepoClassVersion whose artifact shipped
        the definition). ``produced_by_rcv_id`` declares that a RepoClassVersion
        produces this resource type.
        """
        existing = self.find_resource_definition(
            resource_namespace, resource_definition_name, version
        )
        if existing is not None:
            rd = existing
            if description is not None:
                rd.description = description
            if resource_metadata is not None:
                rd.resource_metadata = resource_metadata
            if output_schema is not None:
                rd.output_schema = output_schema
            rd = self.client.upsert(rd)
        else:
            rd = ResourceDefinition(
                resource_namespace=resource_namespace,
                resource_definition_name=resource_definition_name,
                version=version,
                description=description,
                resource_metadata=resource_metadata,
                output_schema=output_schema,
            )
            rd = self.client.upsert(rd)

        if parent_ref is not None:
            self._set_parent(rd, parent_ref)

        if has_rcv_id is not None:
            self._record_has_provenance(has_rcv_id, rd)

        if produced_by_rcv_id is not None:
            rcv = self.client.get_repo_class_version_hmd_lang_deployment(
                produced_by_rcv_id
            )
            if rcv is None:
                raise ServiceException(
                    f"RepoClassVersion, {produced_by_rcv_id}, not found."
                )
            self.declare_produces(rcv, rd, role=role)

        return rd

    def seed_base_resource_definitions(
        self, directory: Optional[Path] = None
    ) -> List[Dict]:
        """Upsert the standard base ResourceDefinition catalog (NERD0004).

        Loads the bundled base definitions (parent-first) and idempotently upserts
        each one. These are abstract supertypes with no producer, so no
        ``has``/``produces`` provenance edges are recorded — provider repos ship
        concrete subtypes that ``parent`` these and declare ``produces: true``.

        Safe to call repeatedly (e.g. on every startup / ``hmd ns up``). Returns a
        summary list of the ``{resource_namespace, resource_definition_name,
        version}`` identities that were seeded.
        """
        seeded: List[Dict] = []
        for doc in load_base_resource_documents(directory):
            self.upsert_resource_definition(
                resource_namespace=doc["resource_namespace"],
                resource_definition_name=doc["resource_definition_name"],
                version=doc["version"],
                description=doc.get("description"),
                resource_metadata=doc.get("resource_metadata"),
                output_schema=doc.get("output_schema"),
                parent_ref=doc.get("parent"),
            )
            seeded.append(
                {
                    "resource_namespace": doc["resource_namespace"],
                    "resource_definition_name": doc["resource_definition_name"],
                    "version": doc["version"],
                }
            )
        return seeded

    def declare_produces(
        self,
        repo_class_version: RepoClassVersion,
        resource_definition: ResourceDefinition,
        role: Optional[str] = None,
    ) -> RepoClassVersionProducesResourceDefinition:
        """Declare that a RepoClassVersion produces a ResourceDefinition (deduped)."""
        for rel in self.client.get_from_repo_class_version_produces_resource_definition_hmd_lang_deployment(
            repo_class_version
        ):
            if rel.ref_to == resource_definition.identifier:
                if role is not None and rel.role != role:
                    rel.role = role
                    rel = self.client.upsert(rel)
                return rel
        return self.client.upsert(
            RepoClassVersionProducesResourceDefinition(
                ref_from=repo_class_version.identifier,
                ref_to=resource_definition.identifier,
                role=role,
            )
        )

    def _record_has_provenance(
        self, repo_class_version_id: str, resource_definition: ResourceDefinition
    ) -> None:
        rcv = self.client.get_repo_class_version_hmd_lang_deployment(
            repo_class_version_id
        )
        if rcv is None:
            raise ServiceException(
                f"RepoClassVersion, {repo_class_version_id}, not found."
            )
        for rel in self.client.get_from_repo_class_version_has_resource_definition_hmd_lang_deployment(
            rcv
        ):
            if rel.ref_to == resource_definition.identifier:
                return
        self.client.upsert(
            RepoClassVersionHasResourceDefinition(
                ref_from=rcv.identifier, ref_to=resource_definition.identifier
            )
        )

    def _set_parent(self, rd: ResourceDefinition, parent_ref: Dict) -> None:
        parent = self.find_resource_definition(
            parent_ref["resource_namespace"],
            parent_ref["resource_definition_name"],
            parent_ref["version"],
        )
        if parent is None:
            raise ServiceException(
                f"Parent ResourceDefinition {parent_ref} not found."
            )
        if parent.identifier == rd.identifier or self._would_create_cycle(rd, parent):
            raise ServiceException(
                "Setting the parent would create a cycle in the resource "
                "definition 'isa' graph."
            )
        # Single inheritance (0..1): replace any existing parent link.
        for rel in self.client.get_from_resource_definition_isa_resource_definition_hmd_lang_deployment(
            rd
        ):
            self.client.delete(rel)
        self.client.upsert(
            ResourceDefinitionIsaResourceDefinition(
                ref_from=rd.identifier, ref_to=parent.identifier
            )
        )

    def _would_create_cycle(
        self, child: ResourceDefinition, proposed_parent: ResourceDefinition
    ) -> bool:
        """Return True if making ``proposed_parent`` the parent of ``child`` would
        introduce a cycle, i.e. ``child`` is already an ancestor of ``proposed_parent``."""
        current = proposed_parent
        seen = set()
        while current is not None:
            if current.identifier == child.identifier:
                return True
            if current.identifier in seen:
                break
            seen.add(current.identifier)
            current = self._get_parent(current)
        return False

    def _get_parent(self, rd: ResourceDefinition) -> Optional[ResourceDefinition]:
        rels = self.client.get_from_resource_definition_isa_resource_definition_hmd_lang_deployment(
            rd
        )
        if not rels:
            return None
        return self.relationship_support.ref_to(rels[0])

    def get_ancestry(self, rd: ResourceDefinition) -> List[ResourceDefinition]:
        """Return the ``isa`` ancestry of ``rd``, root first, ending with ``rd``.

        For example an ``aws/eks-cluster`` that ``isa`` ``kubernetes/kubernetes-cluster``
        yields ``[kubernetes-cluster, eks-cluster]``. The ``isa`` graph is acyclic
        (enforced on write) but a ``seen`` guard keeps this defensive.
        """
        chain: List[ResourceDefinition] = []
        seen = set()
        current: Optional[ResourceDefinition] = rd
        while current is not None and current.identifier not in seen:
            chain.append(current)
            seen.add(current.identifier)
            current = self._get_parent(current)
        chain.reverse()
        return chain

    def get_effective_output_schema(self, rd: ResourceDefinition) -> Dict:
        """Return the effective output schema of ``rd``: its ancestors' ``output_schema``s
        merged root->child (child wins), per SPEC0002/SPEC0005."""
        effective: Dict = {}
        for ancestor in self.get_ancestry(rd):
            schema = ancestor.output_schema
            if schema:
                effective = self._deep_merge_schema(effective, schema)
        return effective

    def _deep_merge_schema(self, parent: Dict, child: Dict) -> Dict:
        """Deep-merge two JSON Schema dicts with child precedence.

        ``properties`` dicts are merged key-wise (child overriding), ``required``
        lists are unioned (order-preserving), and any other key is taken from the
        child when present, else the parent.
        """
        merged = dict(parent)
        for key, child_val in child.items():
            if (
                key == "properties"
                and isinstance(child_val, dict)
                and isinstance(merged.get("properties"), dict)
            ):
                props = dict(merged["properties"])
                props.update(child_val)
                merged["properties"] = props
            elif (
                key == "required"
                and isinstance(child_val, list)
                and isinstance(merged.get("required"), list)
            ):
                merged_required = list(merged["required"])
                for item in child_val:
                    if item not in merged_required:
                        merged_required.append(item)
                merged["required"] = merged_required
            else:
                merged[key] = child_val
        return merged

    def _get_descendants(self, rd: ResourceDefinition) -> List[ResourceDefinition]:
        """Return all ResourceDefinitions that (transitively) ``isa`` ``rd``.

        Children point at their parent via ``resource_definition_isa_resource_definition``
        (``ref_from`` = child, ``ref_to`` = parent), so descendants are found by
        following the relationship in the ``to`` direction. Cycle-guarded.
        """
        descendants: List[ResourceDefinition] = []
        seen = {rd.identifier}
        stack = [rd]
        while stack:
            current = stack.pop()
            for rel in self.client.get_to_resource_definition_isa_resource_definition_hmd_lang_deployment(
                current
            ):
                child = self.relationship_support.ref_from(rel)
                if child.identifier in seen:
                    continue
                seen.add(child.identifier)
                descendants.append(child)
                stack.append(child)
        return descendants

    def get_producers(
        self, resource_definition: ResourceDefinition, include_subtypes: bool = False
    ) -> List[RepoClassVersion]:
        """Return the RepoClassVersions that declare they ``produce`` ``resource_definition``.

        When ``include_subtypes`` is true, producers of any subtype (a definition
        that ``isa`` ``resource_definition``) are included too, mirroring the
        substitutability that resource-based dependency resolution relies on.
        """
        targets = [resource_definition]
        if include_subtypes:
            targets.extend(self._get_descendants(resource_definition))

        producers: Dict[str, RepoClassVersion] = {}
        for target in targets:
            for rel in self.client.get_to_repo_class_version_produces_resource_definition_hmd_lang_deployment(
                target
            ):
                rcv = self.relationship_support.ref_from(rel)
                producers[rcv.identifier] = rcv
        return list(producers.values())

    # ------------------------------------------------------------------ #
    # Resource-based dependency resolution (SPEC0008)
    # ------------------------------------------------------------------ #

    def _resolve_target_definitions(
        self,
        resource_namespace: str,
        resource_definition_name: str,
        version: str,
        version_spec: Optional[str] = None,
    ) -> List[ResourceDefinition]:
        """Return the ResourceDefinition(s) a requirement targets.

        Always includes the exact (namespace, name, version). When a
        ``version_spec`` is given, same-named definitions whose ``version``
        satisfies the spec are included too, so a requirement can be satisfied
        by a compatible newer/older definition version.
        """
        targets: Dict[str, ResourceDefinition] = {}
        exact = self.find_resource_definition(
            resource_namespace, resource_definition_name, version
        )
        if exact is not None:
            targets[exact.identifier] = exact
        if version_spec:
            for rd in self.client.search_resource_definition_hmd_lang_deployment(
                {
                    "attribute": "resource_namespace",
                    "operator": "=",
                    "value": resource_namespace,
                }
            ):
                if (
                    rd.resource_definition_name == resource_definition_name
                    and version_matches(rd.version, version_spec)
                ):
                    targets[rd.identifier] = rd
        return list(targets.values())

    def get_producer_class_version_ids(
        self, def_ref: Dict, version_spec: Optional[str] = None
    ) -> set:
        """Return the set of RepoClassVersion identifiers that produce the
        required resource type or any subtype of it (inheritance-aware)."""
        targets = self._resolve_target_definitions(
            def_ref["resource_namespace"],
            def_ref["resource_definition_name"],
            def_ref["version"],
            version_spec,
        )
        producer_ids = set()
        for target in targets:
            for rcv in self.get_producers(target, include_subtypes=True):
                producer_ids.add(rcv.identifier)
        return producer_ids

    def _instance_matches_producers(
        self, repo_instance: RepoInstance, producer_ids: set, selector: List[tuple]
    ) -> bool:
        """Return True when any non-destroyed deployment of ``repo_instance`` is
        of a producing RepoClassVersion and (if a selector is given) has produced
        a resource whose tags satisfy the selector.

        All non-destroyed deployments are considered — not just the ``current``
        one — so an instance being deployed in the *same* changeset (which has no
        ``current`` deployment yet) still validates.
        """
        matched_rids: List[RepoInstanceDeployment] = []
        for rid_rel in self.client.get_from_repo_instance_has_repo_instance_deployment_hmd_lang_deployment(
            repo_instance
        ):
            rid = self.relationship_support.ref_to(rid_rel)
            if rid.status == DESTROYED:
                continue
            rcv_ids = {
                rel.ref_to if isinstance(rel.ref_to, str) else rel.ref_to.identifier
                for rel in self.client.get_from_repo_instance_deployment_has_repo_class_version_hmd_lang_deployment(
                    rid
                )
            }
            if rcv_ids & producer_ids:
                matched_rids.append(rid)

        if not matched_rids:
            return False
        if not selector:
            return True
        return any(
            self._resource_satisfies_selector(resource, selector)
            for rid in matched_rids
            for resource in self.get_resources_for_deployment(rid)
        )

    def find_satisfying_instances(
        self,
        env_info,
        def_ref: Dict,
        version_spec: Optional[str] = None,
        tag_selector: Optional[str] = None,
    ) -> List[RepoInstance]:
        """Return the RepoInstances in ``env_info`` that satisfy a resource-type
        requirement.

        An instance satisfies the requirement when a non-destroyed deployment is
        of a RepoClassVersion that **produces** the required ResourceDefinition or
        a subtype of it (respecting inheritance and ``version_spec``). When a
        ``tag_selector`` is present, the instance must additionally have produced
        a ``resource`` whose tags satisfy every ``key=value`` pair in the selector.
        """
        producer_ids = self.get_producer_class_version_ids(def_ref, version_spec)
        if not producer_ids:
            return []
        selector = parse_tag_selector(tag_selector)
        return [
            repo_instance
            for repo_instance in env_info.nouns.get(RepoInstance, {}).values()
            if self._instance_matches_producers(repo_instance, producer_ids, selector)
        ]

    def instance_satisfies_requirement(
        self,
        repo_instance: RepoInstance,
        def_ref: Dict,
        version_spec: Optional[str] = None,
        tag_selector: Optional[str] = None,
    ) -> bool:
        """Return True when ``repo_instance`` satisfies the resource requirement
        (used to validate an operator-supplied dependency at apply time)."""
        producer_ids = self.get_producer_class_version_ids(def_ref, version_spec)
        if not producer_ids:
            return False
        return self._instance_matches_producers(
            repo_instance, producer_ids, parse_tag_selector(tag_selector)
        )

    def _resource_tag_set(self, resource: Resource) -> set:
        return {
            (
                self.relationship_support.ref_to(rel).key,
                self.relationship_support.ref_to(rel).value,
            )
            for rel in self.client.get_from_resource_has_resource_tag_hmd_lang_deployment(
                resource
            )
        }

    def _resource_satisfies_selector(
        self, resource: Resource, selector: List[tuple]
    ) -> bool:
        tag_set = self._resource_tag_set(resource)
        return all(pair in tag_set for pair in selector)

    # ------------------------------------------------------------------ #
    # Resource management (deployment-time outputs)
    # ------------------------------------------------------------------ #

    def submit_resources(
        self,
        repo_instance_deployment: RepoInstanceDeployment,
        resources: List[Dict],
    ) -> List[Resource]:
        """Record the concrete resources produced by a RepoInstanceDeployment.

        Each entry: ``{resource_name, resource_definition: {resource_namespace,
        resource_definition_name, version}, output: {...}, tags: [{key, value}]}``.
        Idempotent per (deployment, resource_name).
        """
        results: List[Resource] = []
        existing_resources = self.get_resources_for_deployment(repo_instance_deployment)
        existing_by_name = {r.resource_name: r for r in existing_resources}

        for spec in resources:
            def_ref = spec["resource_definition"]
            rd = self.find_resource_definition(
                def_ref["resource_namespace"],
                def_ref["resource_definition_name"],
                def_ref["version"],
            )
            if rd is None:
                raise ServiceException(
                    f"ResourceDefinition {def_ref} not found."
                )

            output = spec.get("output", {})
            self._validate_output(output, rd)

            existing = existing_by_name.get(spec["resource_name"])
            if existing is not None:
                existing.output = output
                resource = self.client.upsert(existing)
            else:
                resource = self.client.upsert(
                    Resource(resource_name=spec["resource_name"], output=output)
                )
                self.client.upsert(
                    ResourceIsaResourceDefinition(
                        ref_from=resource.identifier, ref_to=rd.identifier
                    )
                )
                self.client.upsert(
                    RepoInstanceDeploymentHasResource(
                        ref_from=repo_instance_deployment.identifier,
                        ref_to=resource.identifier,
                    )
                )

            self._sync_tags(resource, spec.get("tags", []))
            results.append(resource)

        return results

    def get_resources_for_deployment(
        self, repo_instance_deployment: RepoInstanceDeployment
    ) -> List[Resource]:
        rels = self.client.get_from_repo_instance_deployment_has_resource_hmd_lang_deployment(
            repo_instance_deployment
        )
        return [self.relationship_support.ref_to(rel) for rel in rels]

    def serialize_resource(self, resource: Resource) -> Dict:
        """Serialize a single Resource to the consumer-facing dict shape.

        ``{resource_name, resource_definition: {resource_namespace,
        resource_definition_name, version}, output, tags: [{key, value}]}`` --
        the shape a consumer uses to match a produced resource by its Resource
        Definition and read its ``output``. Shared by
        ``serialize_resources_for_deployment`` and the dev-config resolver so both
        emit an identical shape.
        """
        definition = None
        isa_rels = self.client.get_from_resource_isa_resource_definition_hmd_lang_deployment(
            resource
        )
        if isa_rels:
            rd = self.relationship_support.ref_to(isa_rels[0])
            definition = {
                "resource_namespace": rd.resource_namespace,
                "resource_definition_name": rd.resource_definition_name,
                "version": rd.version,
            }
        return {
            "resource_name": resource.resource_name,
            "resource_definition": definition,
            "output": resource.output or {},
            "tags": [
                {"key": key, "value": value}
                for key, value in sorted(self._resource_tag_set(resource))
            ],
        }

    def serialize_resources_for_deployment(
        self, repo_instance_deployment: RepoInstanceDeployment
    ) -> List[Dict]:
        """Return this deployment's resources as consumer-facing dicts.

        Each entry mirrors the ``submit_resources`` input shape (see
        ``serialize_resource``) so a consumer can match a produced resource by its
        Resource Definition and read its ``output``. This is the single shape used
        both by the config-time bake path (``DeployBase.get_instance_config``) and
        the ``get_deployment_resources`` endpoint (NERD0006).
        """
        return [
            self.serialize_resource(resource)
            for resource in self.get_resources_for_deployment(repo_instance_deployment)
        ]

    def get_resources_for_environment(
        self, environment_type: str
    ) -> List[Resource]:
        """The currently-deployed Resources in an environment, deduped by identifier.

        Resources carry no environment attribute, so we reach them by walking
        ``environment -> repo_instance -> repo_instance_deployment -> resource``,
        keeping only each instance's ``current`` deployment and only when it is
        ``DEPLOYED``. The ``current`` flag on
        ``repo_instance_has_repo_instance_deployment`` is what marks the live
        deployment -- a superseded deployment keeps ``status == DEPLOYED`` and only
        has ``current`` flipped to ``"false"`` (see
        ``EnvironmentInformation.update_instance_deployment_status``), so filtering
        on status alone returns one row per historical deploy: ``submit_resources``
        is idempotent per *deployment*, so every redeploy mints a fresh Resource
        with the same ``resource_name``. Status still matters on top of the flag so
        pending (DEPLOY_NEXT/DESTROY_NEXT), FAILED, SKIPPED and DESTROYED
        deployments are excluded. Dedupe is by identifier because a resource can be
        shared across instances. Returns ``[]`` for an unknown environment.
        """
        environment = self.client.search_environment_hmd_lang_deployment(
            {"attribute": "type", "operator": "=", "value": environment_type}
        )
        if not environment:
            return []
        resources: Dict[str, Resource] = {}
        for env_rel in self.client.get_from_environment_has_repo_instance_hmd_lang_deployment(
            environment[0]
        ):
            repo_instance = self.relationship_support.ref_to(env_rel)
            for rid_rel in self.client.get_from_repo_instance_has_repo_instance_deployment_hmd_lang_deployment(
                repo_instance
            ):
                if (rid_rel.current or "").lower() != "true":
                    continue
                rid = self.relationship_support.ref_to(rid_rel)
                if rid.status != DEPLOYED:
                    continue
                for resource in self.get_resources_for_deployment(rid):
                    resources[resource.identifier] = resource
        return list(resources.values())

    def _scope_to_environment(
        self, resources: List[Resource], environment_type: Optional[str]
    ) -> List[Resource]:
        """Filter ``resources`` to those deployed in ``environment_type`` (no-op if
        no environment is given)."""
        if not environment_type:
            return resources
        env_ids = {
            r.identifier for r in self.get_resources_for_environment(environment_type)
        }
        return [r for r in resources if r.identifier in env_ids]

    def find_resources_by_tag(
        self,
        key: str,
        value: Optional[str] = None,
        environment_type: Optional[str] = None,
    ) -> List[Resource]:
        tags: List[ResourceTag] = self.client.search_resource_tag_hmd_lang_deployment(
            {"attribute": "key", "operator": "=", "value": key}
        )
        if value is not None:
            tags = [t for t in tags if t.value == value]

        resources: Dict[str, Resource] = {}
        for tag in tags:
            for rel in self.client.get_to_resource_has_resource_tag_hmd_lang_deployment(
                tag
            ):
                resource = self.relationship_support.ref_from(rel)
                resources[resource.identifier] = resource
        return self._scope_to_environment(
            list(resources.values()), environment_type
        )

    def find_resources_by_selector(
        self, selector: Dict[str, str], environment_type: Optional[str] = None
    ) -> List[Resource]:
        """Return Resources whose tags satisfy every ``key=value`` pair in ``selector``.

        Kubernetes label-selector semantics: all pairs must match (AND). An empty
        selector matches nothing (callers should use ``list``/``find_resources_by_tag``
        for unqualified queries). When ``environment_type`` is given, results are
        further scoped to resources deployed in that environment.
        """
        if not selector:
            return []
        items = list(selector.items())
        first_key, first_value = items[0]
        # Inner call stays env-unscoped; we apply the environment filter once below.
        candidates = {
            r.identifier: r for r in self.find_resources_by_tag(first_key, first_value)
        }
        for resource in list(candidates.values()):
            tag_set = {
                (self.relationship_support.ref_to(rel).key,
                 self.relationship_support.ref_to(rel).value)
                for rel in self.client.get_from_resource_has_resource_tag_hmd_lang_deployment(
                    resource
                )
            }
            if not all((k, v) in tag_set for k, v in items):
                candidates.pop(resource.identifier, None)
        return self._scope_to_environment(
            list(candidates.values()), environment_type
        )

    def _validate_output(self, output: Dict, rd: ResourceDefinition) -> None:
        """Draft-07 validation of a resource's output against the definition's
        *effective* output_schema (its own schema merged with its ``isa`` ancestors',
        per SPEC0002/SPEC0005)."""
        schema = self.get_effective_output_schema(rd)
        if not schema:
            return
        try:
            jsonschema_validate(instance=output, schema=schema)
        except ValidationError as ex:
            raise ServiceException(
                f"Resource output does not conform to the effective output_schema of "
                f"{rd.resource_namespace}/{rd.resource_definition_name}/{rd.version}: "
                f"{ex.message}"
            )

    def _sync_tags(self, resource: Resource, tags: List[Dict]) -> None:
        """Reconcile a resource's resource_has_resource_tag edges to the submitted set."""
        existing_rels = self.client.get_from_resource_has_resource_tag_hmd_lang_deployment(
            resource
        )
        existing_by_kv = {}
        for rel in existing_rels:
            tag = self.relationship_support.ref_to(rel)
            existing_by_kv[(tag.key, tag.value)] = rel

        submitted = {(t["key"], t["value"]) for t in tags}

        for (key, value), rel in existing_by_kv.items():
            if (key, value) not in submitted:
                self.client.delete(rel)

        for key, value in submitted:
            if (key, value) in existing_by_kv:
                continue
            tag = self._find_or_create_tag(key, value)
            self.client.upsert(
                ResourceHasResourceTag(
                    ref_from=resource.identifier, ref_to=tag.identifier
                )
            )

    def _find_or_create_tag(self, key: str, value: str) -> ResourceTag:
        candidates = self.client.search_resource_tag_hmd_lang_deployment(
            {"attribute": "key", "operator": "=", "value": key}
        )
        for tag in candidates:
            if tag.value == value:
                return tag
        return self.client.upsert(ResourceTag(key=key, value=value))

    def _sort_resource_definitions(
        self, rds: List[ResourceDefinition]
    ) -> List[ResourceDefinition]:
        return sorted(
            rds,
            key=lambda rd: (
                rd.resource_namespace,
                rd.resource_definition_name,
                rd.version,
            ),
        )


def seed_base_catalog_best_effort(
    client: HmdLangDeploymentClient,
) -> Optional[List[Dict]]:
    """Idempotently seed the bundled base ResourceDefinition catalog (NERD0004),
    swallowing any failure instead of raising.

    Intended to be called from service startup (e.g. ``deployment_ops.setup()`` on
    every Lambda cold start / FastAPI app init) so cloud environments -- which never
    run the local platform's ``hmd neuronsphere up`` CLI, historically the only
    caller of ``seed_base_resource_definitions`` -- get the base catalog without any
    external trigger. A startup failure here (e.g. a transient DB error) must not
    fail the whole service bootstrap, since that would break every operation the
    service exposes, not just resource-definition ones -- it's idempotent, so the
    next cold start simply retries. Returns the seeded identities on success, or
    None if seeding failed.
    """
    try:
        seeded = ResourceInformation(client).seed_base_resource_definitions()
        LOGGER.info("Seeded %d base ResourceDefinitions at startup.", len(seeded))
        return seeded
    except Exception:
        LOGGER.exception(
            "Failed to seed base ResourceDefinitions catalog at startup; continuing "
            "without it (idempotent -- will retry on next cold start)."
        )
        return None
