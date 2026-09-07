import datetime
import logging
import os
import zoneinfo
from typing import Dict, List, Optional, Tuple

import boto3
from botocore.exceptions import ClientError
from psycopg2 import sql

from hmd_base_service import BaseService
from hmd_cli_tools import ServiceException, HmdEntityNotFoundException
from hmd_cli_tools.hmd_cli_tools import get_session
from hmd_graphql_client.hmd_db_engine_client import DbEngineClient
from hmd_graphql_client.relationship_support import RelationshipSupport
from hmd_lang_deployment.change_set_deployment import ChangeSetDeployment
from hmd_lang_deployment.hmd_lang_deployment_client import HmdLangDeploymentClient
from hmd_lang_deployment.repo_class_version_notes import RepoClassVersionNotes
from hmd_lang_deployment.repo_class_version import RepoClassVersion
from hmd_lang_deployment.repo_instance import RepoInstance
from hmd_lib_auth.hmd_lib_auth import (
    okta_service_account_token_by_service,
)
from hmd_lib_auth.lambda_helper import is_token_expired
from hmd_lib_naming.service_manager import ServiceManager
from hmd_ms_base.crud_operations import get_first_db_engine
from hmd_schema_loader import DefaultLoader
from . import (
    DEPLOYED,
    RID_STATUS_OPTIONS,
    CSD_STATUS_OPTIONS,
    CSED_STATUS_OPTIONS,
)
from .artifact_monitor import ArtifactMonitor
from .auto_deploy_monitor import AutoDeployMonitor
from .class_information import ClassInformation
from .deploy_bom_creator import DeployBomCreator
from .deploy_logic import build_deployment_dag, DeploymentDag
from .deployment_manager import DeploymentManager
from .deployment_dag import get_deployment_dag_records
from .deployment_timeline import get_deployment_timeline_records
from .deploy_base import DeployBase
from .step_log_index import list_pod_prefixes, pod_name_from_prefix
from .env_cache import get_env_cache
from .environment_information import EnvironmentInformation
from .resource_information import ResourceInformation, seed_base_catalog_best_effort
from .version import VersionSpecifier, sort_versions

LOGGER = logging.getLogger(f"HMD.{__name__}")


def _get_deploy_client(evt, ctx):
    db = get_first_db_engine(evt, ctx)
    loader = ctx["loader"]  # type: DefaultLoader
    base_client = DbEngineClient(db, loader)
    deploy_client = HmdLangDeploymentClient(base_client)
    return deploy_client


def _resolve_rcv_targets(deploy_client, rcv_rels) -> List[RepoClassVersion]:
    """Resolve repo_class_has_repo_class_version targets, dropping stale refs."""
    rs = RelationshipSupport()
    rs.register_client(deploy_client._base_client)
    resolved: List[RepoClassVersion] = []
    for rel in rcv_rels:
        rcv = rs.ref_to(rel)
        if rcv is None:
            LOGGER.warning(
                "RepoClassVersion referenced by rel %s could not be resolved; skipping",
                getattr(rel, "identifier", "?"),
            )
            continue
        resolved.append(rcv)
    return resolved


def _get_artifact_monitors(
    service: BaseService,
) -> Tuple[Optional[ArtifactMonitor], Optional[AutoDeployMonitor]]:
    if "ARTIFACT_LIBRARIAN_NAME" in os.environ:
        artifact_service_name = os.environ["ARTIFACT_LIBRARIAN_NAME"]
        deployment_client = _get_deploy_client(evt={}, ctx=service.context)

        def fetch_auth_token():
            LOGGER.debug("Fetching auth token...")

            token = okta_service_account_token_by_service(
                os.environ["HMD_INSTANCE_NAME"],
                os.environ["HMD_REPO_NAME"],
                os.environ["HMD_DID"],
            )

            if is_token_expired(token):
                LOGGER.debug(f"Auth token is invalid...")
            else:
                LOGGER.debug("Auth token is valid...")

            return token

        loaders = service.context["loaders"]
        service_manager = ServiceManager(
            service_names=[artifact_service_name],
            environment=os.environ["HMD_ENVIRONMENT"],
            loaders={artifact_service_name: loaders[artifact_service_name]},
            auth_token=fetch_auth_token(),
            expired_auth_token_callback=fetch_auth_token,
            naming_region=os.environ["HMD_REGION"],
        )
        return (
            ArtifactMonitor(
                librarian_client=service_manager.get_service(artifact_service_name),
                class_information=ClassInformation(deployment_client),
                deployment_client=deployment_client._base_client,
                named_query="new_deployables",
                timestamp_name="deployable",
            ),
            AutoDeployMonitor(
                librarian_client=service_manager.get_service(artifact_service_name),
                deployment_client=deployment_client._base_client,
                named_query="new_auto_deploys",
                timestamp_name="auto_deploy",
            ),
        )

    else:
        return (None, None)


def _query_params(evt) -> Dict[str, str]:
    """The request's query-string parameters.

    ``evt.get("query")`` only resolves on hmd-base-service builds that special-case
    the key; on older builds it falls through to the raw Lambda event, which carries
    query params under ``queryStringParameters`` and never ``query`` -- so every
    ``?filter=`` on every operation silently read as empty. Prefer the request
    itself, which is present on all REST paths (the GraphQL handler builds events
    with ``request=None``).
    """
    request = getattr(evt, "request", None)
    if request is not None and hasattr(request, "query_params"):
        return dict(request.query_params)
    return evt.get("query", {}) or {}


def get_valid_environment(
    deploy_client: HmdLangDeploymentClient, environment_type: str
):
    environment = deploy_client.search_environment_hmd_lang_deployment(
        {"attribute": "type", "operator": "=", "value": environment_type}
    )
    if not environment:
        raise ServiceException(f"Environment, {environment_type}, not found.")
    assert (
        len(environment) == 1
    ), f"Expected 1 environment with type, {environment_type}. Found {len(environment)}."

    return environment[0]


def setup(service):
    global artifact_monitor, auto_deploy_monitor
    artifact_monitor, auto_deploy_monitor = _get_artifact_monitors(service)

    try:
        seed_base_catalog_best_effort(_get_deploy_client(evt={}, ctx=service.context))
    except Exception:
        LOGGER.exception(
            "Failed to build a deploy client for base ResourceDefinition catalog "
            "seeding at startup; continuing without it."
        )

    @service.operation(
        rest_path="/apiop/set_deployment_status/<args_id>/<args_status>",
        rest_methods=["POST"],
        args={"id": "string", "status": "string"},
    )
    def set_deployment_status(evt, ctx):
        deploy_client = _get_deploy_client(evt, ctx)
        id_ = evt["args"]["id"]
        status = evt["args"]["status"]

        status_options = RID_STATUS_OPTIONS
        if status not in status_options:
            raise ServiceException(
                f"Status must be one of {', '.join(status_options)}, was {status}"
            )

        rid = deploy_client.get_repo_instance_deployment_hmd_lang_deployment(id_)
        if not rid:
            raise HmdEntityNotFoundException("RepoInstanceDeployment", id_)

        # Get the environment for the updated RID and alter the current deployed instance if necessary
        rel = deploy_client.get_to_repo_instance_has_repo_instance_deployment_hmd_lang_deployment(
            rid
        )[
            0
        ]
        ri = deploy_client.get_repo_instance_hmd_lang_deployment(rel.ref_from)
        rel = deploy_client.get_to_environment_has_repo_instance_hmd_lang_deployment(
            ri
        )[0]
        env = deploy_client.get_environment_hmd_lang_deployment(rel.ref_from)

        env_info = EnvironmentInformation(env, deploy_client)
        env_info.update_instance_deployment_status(rid, status)
        return {"message": f"Status updated to {status}"}

    @service.operation(
        rest_path="/apiop/set_change_set_deployment_status/<args_id>/<args_status>",
        rest_methods=["POST"],
        args={"id": "string", "status": "string"},
    )
    def set_change_set_deployment_status(evt, ctx):
        deploy_client = _get_deploy_client(evt, ctx)
        id_ = evt["args"]["id"]
        status = evt["args"]["status"]

        status_options = CSD_STATUS_OPTIONS
        if status not in status_options:
            raise ServiceException(
                f"Status must be one of {', '.join(status_options)}, was {status}."
            )

        csd = deploy_client.get_change_set_deployment_hmd_lang_deployment(id_)
        if not csd:
            raise HmdEntityNotFoundException("ChangeSetDeployment", id_)

        class_info = ClassInformation(deploy_client)
        dm = DeploymentManager(deploy_client, class_info)
        dm.update_change_set_deployment_status(csd, status,auth_token=evt["headers"].get("Authorization"),)
        return {"message": f"Status updated to {status}"}

    @service.operation(
        rest_path="/apiop/set_change_set_env_deployment_status/<args_id>/<args_status>",
        rest_methods=["POST"],
        args={"id": "string", "status": "string"},
    )
    def set_change_set_env_deployment_status(evt, ctx):
        deploy_client = _get_deploy_client(evt, ctx)
        id_ = evt["args"]["id"]
        status = evt["args"]["status"]

        status_options = CSED_STATUS_OPTIONS
        if status not in status_options:
            raise ServiceException(
                f"Status must be one of {', '.join(status_options)}, was {status}."
            )

        csed = deploy_client.get_change_set_env_deployment_hmd_lang_deployment(id_)
        if not csed:
            raise HmdEntityNotFoundException("ChangeSetEnvDeployment", id_)

        class_info = ClassInformation(deploy_client)
        dm = DeploymentManager(deploy_client, class_info)
        dm.update_change_set_env_deployment_status(csed, status)
        return {"message": f"Status updated to {status}"}

    @service.operation(
        rest_path="/apiop/add_repo_class_version",
        rest_methods=["POST"],
        args={"payload": "json"},
    )
    def add_repo_class_version(evt, ctx):
        """Add a new RepoClassVersion to a RepoClass.

        The payload is of the form::

          {
            "repo_class_name": "name",
            "version": "1.2.3",
            "dependencies": {
              "dep1": {
                "repo_class_name": "a_name",
                "required": "true" | "false",
                "version_spec": "~= 0.1"
              },
              "dep2": {
                # NERD0004 SPEC0008: a role may declare a resource-type
                # dependency. When a "resource" is present it is authoritative
                # and "repo_class_name" (if given) degrades to a suggestion.
                "repo_class_name": "hmd-eks-cluster",   # optional suggestion
                "required": "true" | "false",
                "resource": {
                  "resource_namespace": "kubernetes",
                  "resource_definition_name": "kubernetes-cluster",
                  "version": "0.1.0",
                  "version_spec": "~= 0.1",             # optional
                  "tag_selector": "tier=prod,region=us-west-2"  # optional
                }
              }
            },
            "default_configuration": {
               ...
            },
            "version_notes": {
                "version_notes": "some notes",
                "requirment_identifiers": ["1","2"]
            },
            "discovery": {
                "summary": "...",
                "entry_points": [{"path": "...", "description": "..."}],
                "capabilities": [{"name": "...", "kind": "endpoint", "description": "..."}],
                "related_docs": [{"title": "...", "path": "..."}]
            }
          }

        :param evt: The event initiating the request.
        :param ctx: The request context.
        """
        deploy_client = _get_deploy_client(evt, ctx)
        payload = evt["args"]["payload"]

        # use the VersionSpecifier to validate the version number is okay...
        VersionSpecifier.validate_version_number(payload["version"])

        class_information = ClassInformation(deploy_client)

        # Make sure the dependency data is good...
        dependencies = payload.get("dependencies", {})
        default_configuration = payload.get("default_configuration", {})
        discovery = payload.get("discovery", {})

        # find the specified repo-class
        repo_class_name = payload["repo_class_name"]

        version_notes = (
            None
            if "version_notes" not in payload
            else RepoClassVersionNotes(**payload["version_notes"])
        )

        class_information.add_repo_version_by_name(
            repo_class_name=repo_class_name,
            version=payload["version"],
            dependencies=dependencies,
            default_configuration=default_configuration,
            version_notes=version_notes,
            discovery=discovery,
        )

        # NERD0005: a new class version is a structural (topology) change, so the
        # cached topology of every environment may now be stale. Status is
        # unaffected. Targeted-invalidate rather than rebuild.
        get_env_cache().invalidate_all_topology()

    @service.operation(
        rest_path="/apiop/resync_repo_class_version_dependencies",
        rest_methods=["POST"],
        args={"payload": "json"},
    )
    def resync_repo_class_version_dependencies(evt, ctx):
        """Repair an EXISTING RepoClassVersion's dependency edges from its
        current manifest ``deploy.dependencies`` block, without registering a
        new version.

        Registration (``add_repo_class_version``) can leave a version
        permanently short a role's edges if that role's wiring failed the
        first time (e.g. a transient error), since re-registering the same
        version number is rejected outright. This op re-runs the same
        role-wiring logic against the already-existing version to backfill
        any missing edges; it's idempotent, so calling it repeatedly -- or
        against a version that's already fully wired -- is safe.

        Payload is of the same form as ``add_repo_class_version``, minus
        ``default_configuration``/``version_notes``::

          {
            "repo_class_name": "name",
            "version": "1.2.3",
            "dependencies": { ... }
          }

        :param evt: The event initiating the request.
        :param ctx: The request context.
        """
        deploy_client = _get_deploy_client(evt, ctx)
        payload = evt["args"]["payload"]

        class_information = ClassInformation(deploy_client)

        class_information.resync_repo_class_version_dependencies(
            repo_class_name=payload["repo_class_name"],
            version=payload["version"],
            dependencies=payload.get("dependencies", {}),
        )

        # NERD0005: this can add dependency edges to an existing version, so
        # the cached topology of every environment may now be stale.
        get_env_cache().invalidate_all_topology()

    @service.operation(
        rest_path="/apiop/upsert_resource_definition",
        rest_methods=["POST"],
        args={"payload": "json"},
    )
    def upsert_resource_definition(evt, ctx):
        """Register (idempotently) a ResourceDefinition (NERD0004).

        The payload is of the form::

          {
            "resource_namespace": "aws",
            "resource_definition_name": "eks-cluster",
            "version": "0.1.0",
            "description": "An EKS cluster",
            "resource_metadata": { ... },
            "output_schema": { <JSON Schema> },
            "parent": {
                "resource_namespace": "kubernetes",
                "resource_definition_name": "kubernetes-cluster",
                "version": "0.1.0"
            },
            "repo_class_version_id": "<identifier>",
            "produced_by_repo_class_version_id": "<identifier>",
            "role": "primary"
          }

        The identity tuple is (resource_namespace, resource_definition_name,
        version); re-submitting the same tuple updates the existing definition.

        :param evt: The event initiating the request.
        :param ctx: The request context.
        """
        deploy_client = _get_deploy_client(evt, ctx)
        payload = evt["args"]["payload"]

        VersionSpecifier.validate_version_number(payload["version"])

        resource_information = ResourceInformation(deploy_client)
        resource_definition = resource_information.upsert_resource_definition(
            resource_namespace=payload["resource_namespace"],
            resource_definition_name=payload["resource_definition_name"],
            version=payload["version"],
            description=payload.get("description"),
            resource_metadata=payload.get("resource_metadata"),
            output_schema=payload.get("output_schema"),
            parent_ref=payload.get("parent"),
            has_rcv_id=payload.get("repo_class_version_id"),
            produced_by_rcv_id=payload.get("produced_by_repo_class_version_id"),
            role=payload.get("role"),
        )
        return resource_definition.serialize()

    @service.operation(
        rest_path="/apiop/seed_base_resource_definitions",
        rest_methods=["POST"],
        args={},
    )
    def seed_base_resource_definitions(evt, ctx):
        """Seed the standard base ResourceDefinition catalog (NERD0004).

        Idempotently upserts the abstract, vendor-neutral supertypes bundled with
        the service (namespaced under ``*.neuronsphere.io``) so that per-repo
        concrete definitions can ``parent`` them. Takes no payload and is safe to
        call on every startup (e.g. from ``hmd ns up``).

        Returns the list of seeded ``{resource_namespace, resource_definition_name,
        version}`` identities.

        :param evt: The event initiating the request.
        :param ctx: The request context.
        """
        deploy_client = _get_deploy_client(evt, ctx)
        return ResourceInformation(deploy_client).seed_base_resource_definitions()

    @service.operation(
        rest_path="/apiop/get_resource_definition/<args_id>",
        rest_methods=["GET"],
        args={"id": "string"},
    )
    def get_resource_definition(evt, ctx):
        """Fetch a single ResourceDefinition by its ``identifier``."""
        deploy_client = _get_deploy_client(evt, ctx)
        id_ = evt["args"]["id"]
        resource_definition = ResourceInformation(deploy_client).get_resource_definition(
            id_
        )
        if not resource_definition:
            raise HmdEntityNotFoundException("ResourceDefinition", id_)
        return resource_definition.serialize()

    @service.operation(
        rest_path="/apiop/list_resource_definitions",
        rest_methods=["GET"],
        args={},
    )
    def list_resource_definitions(evt, ctx):
        """List ResourceDefinitions, optionally filtered by ``resource_namespace``.

        Results are sorted by (resource_namespace, resource_definition_name,
        version).
        """
        deploy_client = _get_deploy_client(evt, ctx)
        query_params = _query_params(evt)
        resource_namespace = query_params.get("resource_namespace")
        resource_definitions = ResourceInformation(
            deploy_client
        ).list_resource_definitions(resource_namespace=resource_namespace)
        return [rd.serialize() for rd in resource_definitions]

    @service.operation(
        rest_path="/apiop/declare_produces_resource_definition",
        rest_methods=["POST"],
        args={"payload": "json"},
    )
    def declare_produces_resource_definition(evt, ctx):
        """Declare that a RepoClassVersion produces a ResourceDefinition.

        The payload is of the form::

          {
            "repo_class_version_id": "<identifier>",
            "resource_definition": {
                "resource_namespace": "aws",
                "resource_definition_name": "eks-cluster",
                "version": "0.1.0"
            },
            "role": "primary"
          }
        """
        deploy_client = _get_deploy_client(evt, ctx)
        payload = evt["args"]["payload"]

        rcv = deploy_client.get_repo_class_version_hmd_lang_deployment(
            payload["repo_class_version_id"]
        )
        if not rcv:
            raise HmdEntityNotFoundException(
                "RepoClassVersion", payload["repo_class_version_id"]
            )

        resource_information = ResourceInformation(deploy_client)
        def_ref = payload["resource_definition"]
        resource_definition = resource_information.find_resource_definition(
            def_ref["resource_namespace"],
            def_ref["resource_definition_name"],
            def_ref["version"],
        )
        if not resource_definition:
            raise ServiceException(f"ResourceDefinition {def_ref} not found.")

        resource_information.declare_produces(
            rcv, resource_definition, role=payload.get("role")
        )
        return {"message": "produces relationship recorded"}

    @service.operation(
        rest_path="/apiop/submit_resources",
        rest_methods=["POST"],
        args={"payload": "json"},
    )
    def submit_resources(evt, ctx):
        """Record the concrete resources produced by a RepoInstanceDeployment.

        Intended for a deploy CLI to call at the end of a deployment. The payload
        is of the form::

          {
            "repo_instance_deployment_id": "<identifier>",
            "resources": [
              {
                "resource_name": "prod-eks",
                "resource_definition": {
                    "resource_namespace": "aws",
                    "resource_definition_name": "eks-cluster",
                    "version": "0.1.0"
                },
                "output": { "cluster_name": "prod-eks", "endpoint": "..." },
                "tags": [ {"key": "env", "value": "prod"} ]
              }
            ]
          }

        The RepoInstanceDeployment is resolved directly by ``identifier``.
        Submission is idempotent per (deployment, resource_name).
        """
        deploy_client = _get_deploy_client(evt, ctx)
        payload = evt["args"]["payload"]

        id_ = payload["repo_instance_deployment_id"]
        rid = deploy_client.get_repo_instance_deployment_hmd_lang_deployment(id_)
        if not rid:
            raise HmdEntityNotFoundException("RepoInstanceDeployment", id_)

        resources = ResourceInformation(deploy_client).submit_resources(
            rid, payload["resources"]
        )
        return [resource.serialize() for resource in resources]

    @service.operation(
        rest_path="/apiop/get_deployment_resources/<args_id>",
        rest_methods=["GET"],
        args={"id": "string"},
    )
    def get_deployment_resources(evt, ctx):
        """List the resources recorded for a RepoInstanceDeployment ``identifier``.

        Each entry carries ``resource_name``, ``resource_definition``, ``output`` and
        ``tags`` (the ``submit_resources`` shape) so a deploy CLI can match a produced
        resource by its Resource Definition and read its ``output`` (NERD0006).
        """
        deploy_client = _get_deploy_client(evt, ctx)
        id_ = evt["args"]["id"]
        rid = deploy_client.get_repo_instance_deployment_hmd_lang_deployment(id_)
        if not rid:
            raise HmdEntityNotFoundException("RepoInstanceDeployment", id_)
        return ResourceInformation(deploy_client).serialize_resources_for_deployment(rid)

    @service.operation(
        rest_path="/apiop/find_resources_by_tag/<args_key>/<args_value>",
        rest_methods=["GET"],
        args={"key": "string", "value": "string"},
    )
    def find_resources_by_tag(evt, ctx):
        """Find resources carrying a searchable ``key``=``value`` tag.

        Pass ``?environment=<type>`` to scope results to resources deployed in that
        environment (the whole graph otherwise).
        """
        deploy_client = _get_deploy_client(evt, ctx)
        query_params = _query_params(evt)
        environment = (query_params.get("environment", "") or "").strip() or None
        resources = ResourceInformation(deploy_client).find_resources_by_tag(
            evt["args"]["key"], evt["args"]["value"], environment_type=environment
        )
        return [resource.serialize() for resource in resources]

    @service.operation(
        rest_path="/apiop/get_effective_output_schema/<args_id>",
        rest_methods=["GET"],
        args={"id": "string"},
    )
    def get_effective_output_schema(evt, ctx):
        """Return the effective (isa-merged) output schema of a ResourceDefinition."""
        deploy_client = _get_deploy_client(evt, ctx)
        id_ = evt["args"]["id"]
        resource_information = ResourceInformation(deploy_client)
        rd = resource_information.get_resource_definition(id_)
        if not rd:
            raise HmdEntityNotFoundException("ResourceDefinition", id_)
        return resource_information.get_effective_output_schema(rd)

    @service.operation(
        rest_path="/apiop/get_resource_definition_ancestry/<args_id>",
        rest_methods=["GET"],
        args={"id": "string"},
    )
    def get_resource_definition_ancestry(evt, ctx):
        """Return the ``isa`` ancestry of a ResourceDefinition, root first."""
        deploy_client = _get_deploy_client(evt, ctx)
        id_ = evt["args"]["id"]
        resource_information = ResourceInformation(deploy_client)
        rd = resource_information.get_resource_definition(id_)
        if not rd:
            raise HmdEntityNotFoundException("ResourceDefinition", id_)
        return [ancestor.serialize() for ancestor in resource_information.get_ancestry(rd)]

    @service.operation(
        rest_path="/apiop/get_producers/<args_id>",
        rest_methods=["GET"],
        args={"id": "string"},
    )
    def get_producers(evt, ctx):
        """Return the RepoClassVersions that produce a ResourceDefinition.

        Pass ``?include_subtypes=true`` to also include producers of any subtype
        (a definition that ``isa`` this one).
        """
        deploy_client = _get_deploy_client(evt, ctx)
        id_ = evt["args"]["id"]
        query_params = _query_params(evt)
        include_subtypes = str(
            query_params.get("include_subtypes", "false")
        ).lower() == "true"
        resource_information = ResourceInformation(deploy_client)
        rd = resource_information.get_resource_definition(id_)
        if not rd:
            raise HmdEntityNotFoundException("ResourceDefinition", id_)
        producers = resource_information.get_producers(
            rd, include_subtypes=include_subtypes
        )
        return [rcv.serialize() for rcv in producers]

    @service.operation(
        rest_path="/apiop/find_resources_by_selector",
        rest_methods=["GET"],
        args={},
    )
    def find_resources_by_selector(evt, ctx):
        """Find resources whose tags satisfy every ``key=value`` pair in the selector.

        The selector is supplied as a comma-separated ``tags`` query parameter, e.g.
        ``?tags=tier=prod,region=us-west-2`` (all pairs must match). Pass
        ``?environment=<type>`` to additionally scope results to that environment.
        """
        deploy_client = _get_deploy_client(evt, ctx)
        query_params = _query_params(evt)
        raw = query_params.get("tags", "") or ""
        environment = (query_params.get("environment", "") or "").strip() or None
        selector = {}
        for pair in raw.split(","):
            pair = pair.strip()
            if not pair or "=" not in pair:
                continue
            key, value = pair.split("=", 1)
            selector[key.strip()] = value.strip()
        resources = ResourceInformation(deploy_client).find_resources_by_selector(
            selector, environment_type=environment
        )
        return [resource.serialize() for resource in resources]

    @service.operation(
        rest_path="/apiop/list_resources",
        rest_methods=["GET"],
        args={},
    )
    def list_resources(evt, ctx):
        """List deployed resources, paginated.

        ``?limit=&offset=`` (defaults 50/0, limit clamped to 500). Pass
        ``?environment=<type>`` to scope to resources deployed in that environment
        (the whole graph otherwise). Returns an
        ``{"items", "total", "limit", "offset"}`` envelope, matching
        ``list_change_set_deployments``. Tags are optional metadata, so this is the
        unfiltered companion to the tag-scoped ``find_resources_by_*`` endpoints.
        """
        deploy_client = _get_deploy_client(evt, ctx)
        query_params = _query_params(evt)
        limit = min(int(query_params.get("limit", 50) or 50), 500)
        offset = max(int(query_params.get("offset", 0) or 0), 0)
        environment = (query_params.get("environment", "") or "").strip() or None
        page, total = ResourceInformation(deploy_client).list_resources(
            limit, offset, environment_type=environment
        )
        return {
            "items": [resource.serialize() for resource in page],
            "total": total,
            "limit": limit,
            "offset": offset,
        }

    @service.operation(
        rest_path="/apiop/suggest_resource_dependencies/<args_type>",
        rest_methods=["GET"],
        args={"type": "string"},
    )
    def suggest_resource_dependencies(evt, ctx):
        """Suggest RepoInstances that could satisfy a resource-type dependency
        in an environment (NERD0004 SPEC0008).

        The operator still *supplies* the satisfying instance when applying a
        changeset; this endpoint auto-resolves the candidates so an operator or
        agent can discover what to supply. Resolution is inheritance-aware (a
        producer of a subtype satisfies), honors ``version_spec``, and filters by
        an optional tag selector.

        Two query forms are supported against the environment ``<type>``:

        - ``?repo_class_version_id=<id>`` — enumerate the consumer's resource
          requirements and return candidates per required ``role`` (also
          surfacing the retained ``repo_class_name`` as a suggestion).
        - ``?resource_namespace=..&resource_definition_name=..&version=..`` with
          optional ``version_spec`` and ``tags`` (``key=value,key=value``) —
          return candidates for a single ad-hoc requirement.
        """
        deploy_client = _get_deploy_client(evt, ctx)
        environment_type = evt["args"]["type"]
        query_params = _query_params(evt)

        environment = get_valid_environment(
            deploy_client=deploy_client, environment_type=environment_type
        )
        env_info = EnvironmentInformation(environment, deploy_client)
        env_info.load_environment()
        resource_information = ResourceInformation(deploy_client)

        def _candidates(def_ref, version_spec, tag_selector):
            return [
                {"name": ri.name, "identifier": ri.identifier}
                for ri in resource_information.find_satisfying_instances(
                    env_info, def_ref, version_spec, tag_selector
                )
            ]

        rcv_id = query_params.get("repo_class_version_id")
        if rcv_id:
            rcv = deploy_client.get_repo_class_version_hmd_lang_deployment(rcv_id)
            if not rcv:
                raise HmdEntityNotFoundException("RepoClassVersion", rcv_id)

            # Retained class-name dependencies are suggestions per role.
            suggested_by_role = {}
            for creq in deploy_client.get_from_repo_class_version_req_repo_class_hmd_lang_deployment(
                rcv
            ):
                suggested_by_role[creq.role] = env_info.rel_support.ref_to(
                    creq
                ).repo_class_name

            result = {}
            for req in deploy_client.get_from_repo_class_version_req_resource_definition_hmd_lang_deployment(
                rcv
            ):
                rd = env_info.rel_support.ref_to(req)
                def_ref = {
                    "resource_namespace": rd.resource_namespace,
                    "resource_definition_name": rd.resource_definition_name,
                    "version": rd.version,
                }
                result[req.role] = {
                    "resource_definition": def_ref,
                    "version_spec": req.version_spec,
                    "tag_selector": req.tag_selector,
                    "required": req.required,
                    "suggested_repo_class_name": suggested_by_role.get(req.role),
                    "candidates": _candidates(
                        def_ref, req.version_spec, req.tag_selector
                    ),
                }
            return result

        required_keys = (
            "resource_namespace",
            "resource_definition_name",
            "version",
        )
        if not all(query_params.get(k) for k in required_keys):
            raise ServiceException(
                "suggest_resource_dependencies requires either "
                "'repo_class_version_id' or all of "
                "'resource_namespace', 'resource_definition_name', 'version'."
            )
        def_ref = {k: query_params[k] for k in required_keys}
        return {
            "candidates": _candidates(
                def_ref,
                query_params.get("version_spec"),
                query_params.get("tags"),
            )
        }

    @service.operation(
        rest_path="/apiop/get_deployment_info/<args_type>",
        rest_methods=["GET"],
        args={"type": "string"},
    )
    def get_deployment_info(evt, ctx):
        deploy_client = _get_deploy_client(evt, ctx)
        environment_type = evt["args"]["type"]

        environment = deploy_client.search_environment_hmd_lang_deployment(
            {"attribute": "type", "operator": "=", "value": environment_type}
        )
        if len(environment) == 0:
            raise ServiceException(
                f"No Environment with type, {environment_type}, not found."
            )

        assert (
            len(environment) == 1
        ), f"Expected 1 environment with type, {environment_type}. Found {len(environment)}."

        environment = environment[0]
        env_info = EnvironmentInformation(environment, deploy_client)
        diagram = env_info.generate_consolidated_diagram()

        return {
            "diagram": diagram,
        }

    @service.operation(
        rest_path="/apiop/get_deployment_bom/<args_type>",
        rest_methods=["GET"],
        args={"type": "string"},
    )
    def get_deployment_bom(evt, ctx):
        deploy_client = _get_deploy_client(evt, ctx)
        environment_type = evt["args"]["type"]

        environment = get_valid_environment(
            deploy_client=deploy_client, environment_type=environment_type
        )
        env_info = EnvironmentInformation(environment, deploy_client)
        deployment_dag = build_deployment_dag(env_info, [])
        deploy_bom_creator = DeployBomCreator(deploy_client)
        bom = deploy_bom_creator.create_deploy_bom(deployment_dag)
        return bom

    @service.operation(
        rest_path="/apiop/get_deployment_history/<args_type>/<args_name>",
        rest_methods=["GET"],
        args={"type": "string", "name": "string"},
    )
    def get_deployment_history(evt, ctx):
        deploy_client = _get_deploy_client(evt, ctx)
        environment_type = evt["args"]["type"]
        ri_name = evt["args"]["name"]

        environment = get_valid_environment(
            deploy_client=deploy_client, environment_type=environment_type
        )
        env_info = EnvironmentInformation(environment, deploy_client)
        return env_info.get_repo_instance_history(ri_name)

    @service.operation(
        rest_path="/apiop/get_deployment_config/<args_type>/<args_name>",
        rest_methods=["GET"],
        args={"type": "string", "name": "string"},
    )
    def get_deployment_config(evt, ctx):
        deploy_client = _get_deploy_client(evt, ctx)
        environment_type = evt["args"]["type"]
        ri_name = evt["args"]["name"]

        environment = get_valid_environment(
            deploy_client=deploy_client, environment_type=environment_type
        )
        env_info = EnvironmentInformation(environment, deploy_client)
        env_info.load_environment()
        ri = env_info.get_repo_instance(ri_name)
        LOGGER.info(ri.serialize(encode_blobs=False))
        deploy_base = DeployBase(deploy_client, env_info.rel_support)
        deploy_config = deploy_base.get_instance_config(ri, False)
        return deploy_config

    @service.operation(
        rest_path="/apiop/register_deployed_instance",
        rest_methods=["POST"],
        args={"payload": "json"},
    )
    def register_deployed_instance(evt, ctx):
        """Register an already-deployed RepoInstance into an environment with the
        full edge set.

        Reuses the exact path ``apply_changeset`` uses
        (``EnvironmentInformation.add_repo_instance`` with ``status=DEPLOYED``), so a
        seeded service/resource becomes a first-class, resolvable environment member
        (``EnvironmentHasRepoInstance`` + ``RepoInstanceIsaRepoClass`` +
        ``RepoInstanceDeployment`` + ``RepoInstanceDeploymentHasRepoClassVersion``)
        rather than a floating record. The RepoClassVersion must already exist
        (register it first via ``add_repo_class_version``).

        Payload: ``{environment?, repo_class_name, version, instance_name,
        deployment_id?, instance_configuration?}``.
        """
        deploy_client = _get_deploy_client(evt, ctx)
        payload = evt["args"]["payload"]
        environment_type = payload.get("environment", "local")
        repo_class_name = payload["repo_class_name"]
        version = payload["version"]
        instance_name = payload["instance_name"]
        deployment_id = payload.get("deployment_id", "local")
        instance_configuration = payload.get("instance_configuration", {})

        environment = get_valid_environment(
            deploy_client=deploy_client, environment_type=environment_type
        )
        env_info = EnvironmentInformation(environment, deploy_client)
        env_info.load_environment()

        rcv = ClassInformation(deploy_client).get_repo_class_version(
            repo_class_name, version
        )

        ri, rid = env_info.add_repo_instance(
            instance_name,
            deployment_id,
            instance_configuration,
            rcv,
            status=DEPLOYED,
        )
        return {
            "repo_instance_id": ri.identifier,
            "repo_instance_deployment_id": rid.identifier,
        }

    @service.operation(
        rest_path="/apiop/generate_dev_deployment_config",
        rest_methods=["POST"],
        args={"payload": "json"},
    )
    def generate_dev_deployment_config(evt, ctx):
        """Resolve a dev ``--config-file`` from a repo's manifest ``deploy`` block.

        Binds each dependency role to the local NeuronSphere's default Resources (and
        a registered DB / env-linked seeded services) WITHOUT persisting the consumer
        as a RepoInstance or touching the ChangeSet graph. Unresolved roles (no local
        producer) are emitted as editable scaffolds. The returned dict is the exact
        ``{...default_configuration, dependencies, details}`` shape
        ``hmd deploy --local --config-file`` consumes.

        Payload: ``{environment?, instance_name?, default_configuration,
        dependencies, bindings?}`` -- ``default_configuration`` and ``dependencies``
        copied verbatim from the repo manifest's ``deploy`` block.
        """
        from .dev_config_resolver import (
            generate_dev_deployment_config as _generate_dev_config,
        )

        deploy_client = _get_deploy_client(evt, ctx)
        payload = evt["args"]["payload"]
        environment_type = payload.get("environment", "local")

        environment = get_valid_environment(
            deploy_client=deploy_client, environment_type=environment_type
        )
        env_info = EnvironmentInformation(environment, deploy_client)
        env_info.load_environment()

        return _generate_dev_config(
            deploy_client,
            env_info,
            instance_name=payload.get(
                "instance_name", payload.get("repo_class_name", "dev")
            ),
            default_configuration=payload.get("default_configuration", {}),
            dependencies=payload.get("dependencies", {}),
            bindings=payload.get("bindings"),
        )

    @service.operation(
        rest_path="/apiop/apply_changeset",
        rest_methods=["POST"],
        args={"payload": "json"},
    )
    def apply_changeset(evt, ctx):
        deploy_client = _get_deploy_client(evt, ctx)
        payload = evt["args"]["payload"]

        change_set = deploy_client.search_change_set_hmd_lang_deployment(
            {"attribute": "name", "operator": "=", "value": payload["change_set_name"]}
        )
        if not change_set:
            raise ServiceException(
                f"ChangeSet with name, {payload['change_set_name']}, not found."
            )

        assert (
            len(change_set) == 1
        ), f"Expected 1 ChangeSet with name, {payload['change_set_name']}. Found {len(change_set)}."
        change_set = change_set[0]

        deployment_set = deploy_client.search_deployment_set_hmd_lang_deployment(
            {
                "attribute": "name",
                "operator": "=",
                "value": payload["deployment_set_name"],
            }
        )
        if not deployment_set:
            raise ServiceException(
                f"DeploymentSet with name, {payload['deployment_set_name']}, not found."
            )
        assert (
            len(deployment_set) == 1
        ), f"Expected 1 DeploymentSet with name, {payload['deployment_set_name']}. Found {len(deployment_set)}."
        deployment_set = deployment_set[0]

        deployment_manager = DeploymentManager(
            deploy_client, ClassInformation(deploy_client)
        )
        result = deployment_manager.apply_changeset(
            change_set,
            deployment_set,
            evt["headers"].get("Authorization"),
            skip_async=payload.get("skip_async", False),
        )

        # NERD0005: applying a changeset can add instances / dependency edges
        # (topology) and DEPLOY_NEXT/DESTROY_NEXT deployments (status) across the
        # environments of the deployment set. Targeted-invalidate both layers.
        get_env_cache().invalidate_all()
        return result

    @service.operation(
        rest_path="/apiop/validate_changeset",
        rest_methods=["POST"],
        args={"payload": "json"},
    )
    def validate_changeset(evt, ctx):
        """Dry-run validation of a ChangeSet definition.

        Body: ``{"changes": [<change item>, ...]}`` where each change item
        matches the shape stored in ``ChangeSet.definition`` (at minimum
        ``repo_instance_name``, ``repo_class_name``, ``repo_class_version``,
        and optionally ``dependencies`` as ``{role: instance_name}``).

        Returns ``{"valid": bool, "errors": [...], "warnings": [...]}`` where
        each error/warning is ``{"type", "instance", "message"}``. The endpoint
        verifies that each referenced RepoClass + version exists, that every
        required role on the class version is supplied, that every named
        dependency resolves to another change in the set, and that the implied
        instance dependency graph has no cycles. It performs no DB writes.
        """
        deploy_client = _get_deploy_client(evt, ctx)
        payload = evt["args"]["payload"]
        changes = payload.get("changes", [])

        errors: List[dict] = []
        warnings: List[dict] = []

        def _err(kind: str, instance: str, message: str) -> None:
            errors.append({"type": kind, "instance": instance, "message": message})

        def _warn(kind: str, instance: str, message: str) -> None:
            warnings.append({"type": kind, "instance": instance, "message": message})

        if not isinstance(changes, list) or not changes:
            return {
                "valid": False,
                "errors": [
                    {
                        "type": "schema",
                        "instance": "",
                        "message": "'changes' must be a non-empty list.",
                    }
                ],
                "warnings": [],
            }

        class_info = ClassInformation(deploy_client)

        # Index changes by repo_instance_name so dependency references can be
        # resolved against siblings in the same proposed changeset.
        instances_by_name: dict = {}
        for change in changes:
            name = change.get("repo_instance_name")
            if not name:
                _err(
                    "schema",
                    "",
                    "Each change must include 'repo_instance_name'.",
                )
                continue
            if name in instances_by_name:
                _err(
                    "duplicate",
                    name,
                    f"Repo instance '{name}' appears more than once in the change set.",
                )
            instances_by_name[name] = change

        # Per-instance validation: class + version exists, version_spec on each
        # required role on the class version is supplied, dependency targets
        # resolve.
        for name, change in instances_by_name.items():
            repo_class_name = change.get("repo_class_name")
            repo_class_version = change.get("repo_class_version")
            if not repo_class_name or not repo_class_version:
                _err(
                    "schema",
                    name,
                    "Each change must include 'repo_class_name' and 'repo_class_version'.",
                )
                continue

            try:
                rcv = class_info.get_repo_class_version(
                    repo_class_name, repo_class_version
                )
            except ServiceException as e:
                _err("missing_class_version", name, str(e))
                continue

            required_roles: List[Tuple[str, str, str]] = []
            for rel in deploy_client.get_from_repo_class_version_req_repo_class_hmd_lang_deployment(
                rcv
            ):
                role = rel.role
                version_spec = rel.version_spec
                required = str(rel.required).lower() == "true"
                required_roles.append((role, version_spec, required))

            supplied_deps = change.get("dependencies") or {}
            for role, version_spec, required in required_roles:
                if role not in supplied_deps:
                    if required:
                        _err(
                            "missing_required_role",
                            name,
                            f"Required dependency role '{role}' (spec {version_spec}) is not supplied.",
                        )
                    else:
                        _warn(
                            "missing_optional_role",
                            name,
                            f"Optional dependency role '{role}' (spec {version_spec}) is not supplied.",
                        )

            for role, dep_target in supplied_deps.items():
                target_name = dep_target
                # NERD0002 shorthand: ns:<instance>:<deployment_id>
                if isinstance(dep_target, str) and dep_target.startswith("ns:"):
                    parts = dep_target.split(":")
                    if len(parts) >= 2:
                        target_name = parts[1]
                if target_name not in instances_by_name:
                    # Allow already-deployed instances (best-effort check via
                    # RepoInstance search) — only flag if it isn't a sibling
                    # change AND no matching RepoInstance exists.
                    existing = deploy_client.search_repo_instance_hmd_lang_deployment(
                        {"attribute": "name", "operator": "=", "value": target_name}
                    )
                    if not existing:
                        _err(
                            "unresolved_dependency",
                            name,
                            f"Dependency '{role}' -> '{target_name}' is neither in the change set "
                            f"nor an existing deployed RepoInstance.",
                        )

        # Cycle detection over instances supplied in this changeset.
        WHITE, GRAY, BLACK = 0, 1, 2
        color = {name: WHITE for name in instances_by_name}

        def _visit(name: str, stack: List[str]) -> Optional[List[str]]:
            color[name] = GRAY
            stack.append(name)
            for _, target in (instances_by_name[name].get("dependencies") or {}).items():
                target_name = target
                if isinstance(target, str) and target.startswith("ns:"):
                    parts = target.split(":")
                    if len(parts) >= 2:
                        target_name = parts[1]
                if target_name not in color:
                    continue
                if color[target_name] == GRAY:
                    return stack[stack.index(target_name) :] + [target_name]
                if color[target_name] == WHITE:
                    cycle = _visit(target_name, stack)
                    if cycle is not None:
                        return cycle
            stack.pop()
            color[name] = BLACK
            return None

        reported_cycles: set = set()
        for name in list(color.keys()):
            if color[name] == WHITE:
                cycle = _visit(name, [])
                if cycle is not None:
                    key = tuple(sorted(set(cycle)))
                    if key not in reported_cycles:
                        reported_cycles.add(key)
                        _err(
                            "circular_dependency",
                            cycle[0],
                            "Circular dependency detected: " + " -> ".join(cycle),
                        )

        return {
            "valid": not errors,
            "errors": errors,
            "warnings": warnings,
        }

    @service.operation(
        rest_path="/apiop/apply_cluster_changeset",
        rest_methods=["POST"],
        args={"payload": "json"},
    )
    def apply_cluster_changeset(evt, ctx):
        deploy_client = _get_deploy_client(evt, ctx)
        payload = evt["args"]["payload"]

        change_set = deploy_client.search_change_set_hmd_lang_deployment(
            {"attribute": "name", "operator": "=", "value": payload["change_set_name"]}
        )
        if not change_set:
            raise ServiceException(
                f"ChangeSet with name, {payload['change_set_name']}, not found."
            )

        assert (
            len(change_set) == 1
        ), f"Expected 1 ChangeSet with name, {payload['change_set_name']}. Found {len(change_set)}."
        change_set = change_set[0]

        deployment_set = deploy_client.search_deployment_set_hmd_lang_deployment(
            {
                "attribute": "name",
                "operator": "=",
                "value": payload["deployment_set_name"],
            }
        )
        if not deployment_set:
            raise ServiceException(
                f"DeploymentSet with name, {payload['deployment_set_name']}, not found."
            )
        assert (
            len(deployment_set) == 1
        ), f"Expected 1 DeploymentSet with name, {payload['deployment_set_name']}. Found {len(deployment_set)}."
        deployment_set = deployment_set[0]

        deployment_lock_name = payload["deployment_lock_name"]

        deployment_manager = DeploymentManager(
            deploy_client, ClassInformation(deploy_client)
        )
        result = deployment_manager.apply_changeset(
            change_set,
            deployment_set,
            evt["headers"].get("Authorization"),
            deployment_lock_name=deployment_lock_name,
            callback_url=payload.get("callback_url")
        )
        return result

    @service.operation(
        rest_path="/apiop/deploy_change_set_deployment/<args_id>",
        rest_methods=["POST"],
        args={"id": "string"},
    )
    def deploy_change_set_deployment(evt, ctx):
        deploy_client = _get_deploy_client(evt, ctx)
        id_ = evt["args"]["id"]
        auth_token = evt["headers"].get("Authorization")

        deployment_manager = DeploymentManager(
            deploy_client, ClassInformation(deploy_client)
        )

        result = deployment_manager.deploy_change_set_deployment(
            id_, os.environ["HMD_CUSTOMER_CODE"], os.environ["HMD_REGION"],
            auth_token=auth_token
        )
        return result

    @service.operation(
        rest_path="/apiop/generate_local_deployment/<args_csd_nid>",
        rest_methods=["POST"],
        args={"csd_nid": "string"},
    )
    def generate_local_deployment(evt, ctx):
        """Generate a local deployment manifest from a ChangeSetDeployment.

        Builds the deployment DAG and generates per-node deploy scripts
        (same code as Argo workflow generation), returning them as a JSON
        manifest for the CLI's LocalWorkflowRunner to execute in
        hmd-img-projectbuilder containers.
        """
        from .local_deployment import generate_local_deploy_manifest

        deploy_client = _get_deploy_client(evt, ctx)
        rs = RelationshipSupport([deploy_client._base_client])
        csd_nid = evt["args"]["csd_nid"]

        result = generate_local_deploy_manifest(deploy_client, rs, csd_nid)
        return result

    @service.operation(
        rest_path="/apiop/deploy_destroy_changeset_deployment/<args_id>",
        rest_methods=["POST"],
        args={"id": "string", "payload": "json"},
    )
    def deploy_destroy_changeset_deployment(evt, ctx):
        """Complete the async destroy workflow generation and submission.

        This endpoint is called asynchronously by destroy_deployment_set().
        It performs the heavy work: DAG building, instance enumeration,
        RepoInstanceDeployment creation, and workflow submission.
        """
        deploy_client = _get_deploy_client(evt, ctx)
        id_ = evt["args"]["id"]
        payload = evt["args"].get("payload") or {}
        destroy_from = payload.get("destroy_from")
        auth_token = evt["headers"].get("Authorization")

        # Safety: require destroy_from to prevent accidental deletion
        if not destroy_from:
            raise ServiceException(
                "destroy_from is required and cannot be empty"
            )

        deployment_manager = DeploymentManager(
            deploy_client, ClassInformation(deploy_client)
        )

        result = deployment_manager.deploy_destroy_changeset_deployment(
            id_,
            os.environ["HMD_CUSTOMER_CODE"],
            os.environ["HMD_REGION"],
            destroy_from=destroy_from,
            auth_token=auth_token,
        )
        return result

    @service.operation(
        rest_path="/apiop/destroy_deploymentset",
        rest_methods=["POST"],
        args={"payload": "json"},
    )
    def destroy_deploymentset(evt, ctx):
        """Destroy instances in a deployment set.

        Format of the payload is::

          {
            "deployment_set_name": "<name>",
            "dry_run": true|false,
            "destroy_from": ["<instance_name>", ...],
            "skip_async": true|false
          }

        The ``destroy_from`` parameter specifies which instance(s) to destroy from.
        All instances that depend on the specified instances will also be destroyed.

        **SAFETY**: When ``dry_run=False``, ``destroy_from`` is **required** to
        prevent accidental deletion of all instances.

        If ``dry_run`` is ``True`` (default), the names of all instances that
        would be destroyed are returned without making any changes.

        If ``dry_run`` is ``False``, a destroy workflow is created and submitted
        for asynchronous execution. The CSD ID is returned immediately for
        status tracking.

        ``skip_async`` (local deploy mode) prepares the destroy synchronously and
        skips both the async Lambda invocation and the Argo submission, leaving
        the caller to drive execution through ``generate_local_deployment``.

        :param evt: Event data
        :param ctx: Context
        :returns: For dry_run=True: list of instances to destroy.
            For dry_run=False: {"csd_nid": "<id>"} for status tracking
        :raises ServiceException: If destroy_from is missing when dry_run=False
        """
        deploy_client = _get_deploy_client(evt, ctx)
        payload = evt["args"]["payload"]

        dry_run = payload.get("dry_run", True)
        destroy_from = payload.get("destroy_from", None)
        if destroy_from and isinstance(destroy_from, str):
            destroy_from = [destroy_from]

        # Safety: require destroy_from when actually destroying (not dry run)
        if not dry_run and not destroy_from:
            raise ServiceException(
                "destroy_from is required when dry_run=False. "
                "Must specify which instances to destroy to prevent accidental deletion."
            )

        deployment_set = deploy_client.search_deployment_set_hmd_lang_deployment(
            {
                "attribute": "name",
                "operator": "=",
                "value": payload["deployment_set_name"],
            }
        )
        if not deployment_set:
            raise ServiceException(
                f"DeploymentSet with name, {payload['deployment_set_name']}, not found."
            )
        assert (
            len(deployment_set) == 1
        ), f"Expected 1 DeploymentSet with name, {payload['change_set_name']}. Found {len(deployment_set)}."
        deployment_set = deployment_set[0]

        deployment_manager = DeploymentManager(
            deploy_client, ClassInformation(deploy_client)
        )

        deployment_lock_name = payload.get("deployment_lock_name")
        if not dry_run:
            result = deployment_manager.destroy_deployment_set(
                deployment_set,
                destroy_from,
                evt["headers"].get("Authorization"),
                deployment_lock_name=deployment_lock_name,
                callback_url=payload.get("callback_url"),
                skip_async=payload.get("skip_async", False),
            )
            # A destroy creates DESTROY_NEXT deployments and, once executed,
            # removes instances from the environment -- both topology and
            # status change, exactly as for apply_changeset (NERD0005).
            get_env_cache().invalidate_all()
        else:
            result = deployment_manager.destroy_deployment_set_dry_run(
                deployment_set, destroy_from
            )

        return result

    @service.operation(
        rest_path="/apiop/check_for_new_deployables",
        rest_methods=["POST"],
        args={"payload": "json"},
    )
    def check_for_new_deployables(evt, ctx):
        if not artifact_monitor:
            raise ServiceException("No artifact librarian configured.")

        LOGGER.info("Checking for new deployables...")
        artifact_monitor.check_for_new_content_items()

    @service.operation(
        rest_path="/apiop/check_for_auto_deployments",
        rest_methods=["POST"],
        args={"payload": "json"},
    )
    def check_for_auto_deployments(evt, ctx):
        if not artifact_monitor:
            raise ServiceException("No artifact librarian configured.")

        LOGGER.info("Checking for new auto deployments...")
        auto_deploy_monitor.check_for_new_content_items(evt["headers"])

    @service.operation(
        rest_path="/apiop/compare_environments",
        rest_methods=["POST"],
        args={"payload": "json"},
    )
    def compare_environments(evt, ctx):
        deploy_client = _get_deploy_client(evt, ctx)
        payload = evt["args"]["payload"]

        from_env = payload["from_env"]
        to_env = payload["to_env"]

        from_environment = get_valid_environment(
            deploy_client=deploy_client, environment_type=from_env
        )
        to_environment = get_valid_environment(
            deploy_client=deploy_client, environment_type=to_env
        )

        from_info = EnvironmentInformation(from_environment, deploy_client)
        to_info = EnvironmentInformation(to_environment, deploy_client)

        from_dag = build_deployment_dag(from_info, [])

        try:
            to_dag = build_deployment_dag(to_info, [])
        except ServiceException as e:
            if e.message != "No data found when creating the deployment dag.":
                raise e
            to_dag = DeploymentDag({})

        deploy_bom_creator = DeployBomCreator(deploy_client)

        from_bom = deploy_bom_creator.create_deploy_bom(from_dag)
        to_bom = deploy_bom_creator.create_deploy_bom(to_dag)

        differences = []
        to_dict = {change["repo_instance_name"]: change for change in to_bom}

        from_names = [ri["repo_instance_name"] for ri in from_bom]
        new_in_from = [rin for rin in from_names if not rin in to_dict]
        removed_from_to = [rin for rin in to_dict if not rin in from_names]

        for name in from_names:
            frm = [frm for frm in from_bom if frm["repo_instance_name"] == name][0]
            if name in new_in_from:
                differences.append(frm)
            else:
                to = [to for to in to_bom if to["repo_instance_name"] == name][0]
                if frm != to:
                    differences.append(frm)

        return {
            "deploy_change_set": differences,
            "removed_instances": removed_from_to,
            "existing_change_set": [
                to_dict.get(change["repo_instance_name"])
                for change in differences
                if change["repo_instance_name"] in to_dict
            ],
        }

    @service.operation(
        rest_path="/apiop/find_repo_class_versions/<args_repo_class_name>",
        rest_methods=["POST"],
        args={"repo_class_name": "string"},
    )
    def find_repo_class_versions(evt, ctx):
        """List a repo class's versions, newest-first.

        Legacy default (no query params) returns a bare list of version dicts,
        each with a resolved ``dependencies`` map. Opt-in pagination is
        triggered by any of ``?limit=&offset=&q=&include_deps=``:

        - ``limit`` (default 50, clamped to 500) / ``offset`` (default 0) page
          the sorted list; the response becomes an
          ``{"items", "total", "limit", "offset"}`` envelope like
          ``list_resources``.
        - ``q`` filters versions by case-insensitive substring before paging.
        - ``include_deps=false`` skips the per-version dependency resolution
          entirely (the version picker only needs the version string).

        The expensive dependency resolution runs *after* the slice, so a paged
        call only resolves deps for the page (or not at all), never the whole
        list.
        """
        deploy_client = _get_deploy_client(evt, ctx)
        repo_class_name = evt["args"]["repo_class_name"]
        query = _query_params(evt)

        paginated = any(
            k in query for k in ("limit", "offset", "q", "include_deps")
        )
        limit = min(int(query.get("limit", 50) or 50), 500)
        offset = max(int(query.get("offset", 0) or 0), 0)
        q = (query.get("q", "") or "").strip().lower()
        include_deps = str(query.get("include_deps", "true")).lower() != "false"

        repo_classes = deploy_client.search_repo_class_hmd_lang_deployment(
            {"attribute": "repo_class_name", "operator": "=", "value": repo_class_name}
        )

        versions: List[RepoClassVersion] = []

        for repo_class in repo_classes:
            rcv_rels = deploy_client.get_from_repo_class_has_repo_class_version_hmd_lang_deployment(
                repo_class
            )
            versions.extend(_resolve_rcv_targets(deploy_client, rcv_rels))

        versions = sort_versions(versions, lambda rcv: rcv.version)

        if q:
            versions = [
                rcv for rcv in versions if q in str(rcv.version or "").lower()
            ]

        total = len(versions)
        page = versions[offset : offset + limit] if paginated else versions

        rs = RelationshipSupport()
        rs.register_client(deploy_client._base_client)
        result = []
        for rcv in page:
            rcv_dict = rcv.serialize(encode_blobs=False)
            deps = {}
            if include_deps:
                for rel in deploy_client.get_from_repo_class_version_req_repo_class_hmd_lang_deployment(
                    rcv
                ):
                    target_rc = rs.ref_to(rel)
                    if target_rc is None:
                        continue
                    deps[rel.role] = {
                        "repo_class_name": target_rc.repo_class_name,
                        "required": str(rel.required).lower(),
                        "version_spec": rel.version_spec,
                    }
            rcv_dict["dependencies"] = deps
            result.append(rcv_dict)

        if not paginated:
            return result

        return {
            "items": result,
            "total": total,
            "limit": limit,
            "offset": offset,
        }

    @service.operation(
        rest_path="/apiop/get_repo_class_version_detail/<args_repo_class_name>/<args_version>",
        rest_methods=["GET"],
        args={"repo_class_name": "string", "version": "string"},
    )
    def get_repo_class_version_detail(evt, ctx):
        """Fetch a single RepoClassVersion's full detail, including its
        BACON ``discovery`` metadata, for the ``hmd-app-neuronsphere``
        RepoClassVersion detail view.

        :param evt: The event initiating the request.
        :param ctx: The request context.
        """
        deploy_client = _get_deploy_client(evt, ctx)
        repo_class_name = evt["args"]["repo_class_name"]
        version = evt["args"]["version"]

        class_information = ClassInformation(deploy_client)
        rcv = class_information.get_repo_class_version(repo_class_name, version)

        rs = RelationshipSupport()
        rs.register_client(deploy_client._base_client)
        rcv_dict = rcv.serialize(encode_blobs=False)
        deps = {}
        for rel in deploy_client.get_from_repo_class_version_req_repo_class_hmd_lang_deployment(
            rcv
        ):
            target_rc = rs.ref_to(rel)
            if target_rc is None:
                continue
            deps[rel.role] = {
                "repo_class_name": target_rc.repo_class_name,
                "required": str(rel.required).lower(),
                "version_spec": rel.version_spec,
            }
        rcv_dict["dependencies"] = deps
        return rcv_dict

    @service.operation(
        rest_path="/apiop/get_repo_class_instances/<args_repo_class_name>/<args_type>",
        rest_methods=["GET"],
        args={"repo_class_name": "string", "type": "string"},
    )
    def find_repo_class_instances(evt, ctx):
        deploy_client = _get_deploy_client(evt, ctx)
        repo_class_name = evt["args"]["repo_class_name"]
        environment_type = evt["args"]["type"]

        environment = get_valid_environment(
            deploy_client=deploy_client, environment_type=environment_type
        )
        env_info = EnvironmentInformation(environment, deploy_client)
        deployment_dag = build_deployment_dag(env_info, [])
        deploy_bom_creator = DeployBomCreator(deploy_client)
        bom = deploy_bom_creator.create_deploy_bom(deployment_dag)

        instances = list(filter(lambda ri: ri.repo_class_name == repo_class_name, bom))

        return instances

    @service.operation(
        rest_path="/apiop/list_repo_classes",
        rest_methods=["GET"],
        args={},
    )
    def list_repo_classes(evt, ctx):
        """List every registered RepoClass with its known versions.

        Returns a list of ``{"repo_class_name": str, "identifier": str,
        "versions": [<sorted version strings>]}`` entries. Versions are sorted
        with the same comparator used by ``find_repo_class_versions`` so the
        GUI can render selectors consistently.
        """
        deploy_client = _get_deploy_client(evt, ctx)

        result = []
        for repo_class in deploy_client.search_repo_class_hmd_lang_deployment({}):
            rcv_rels = deploy_client.get_from_repo_class_has_repo_class_version_hmd_lang_deployment(
                repo_class
            )
            versions = _resolve_rcv_targets(deploy_client, rcv_rels)
            versions = sort_versions(versions, lambda rcv: rcv.version)
            version_strings = [rcv.version for rcv in versions]

            result.append(
                {
                    "repo_class_name": repo_class.repo_class_name,
                    "identifier": repo_class.identifier,
                    "versions": version_strings,
                    "latest_version": version_strings[0] if version_strings else None,
                }
            )

        result.sort(key=lambda rc: rc["repo_class_name"])
        return result

    @service.operation(
        rest_path="/apiop/list_change_set_deployments",
        rest_methods=["GET"],
        args={},
    )
    def list_change_set_deployments(evt, ctx):
        """List ChangeSetDeployments with optional filtering and pagination.

        Query params:
        - ``deployment_set``: filter by ``deployment_set_name`` (exact match)
        - ``status``: filter by ``csd_status`` (exact match)
        - ``since``: ISO-8601 timestamp; only CSDs with ``deploy_time >= since``
        - ``limit``: max items to return (default 50, max 500)
        - ``offset``: pagination offset (default 0)

        Returns ``{"items": [<slim CSD dict>...], "total": int, "limit": int,
        "offset": int}`` with items sorted by ``deploy_time`` descending so the
        GUI shows the most recent deployments first.

        The list response intentionally omits ``deployment_set_definition``
        (the full BOM at deploy time, often the largest field on the record).
        Callers that need the full CSD should fetch it by id via the
        ``GET /hmd_lang_deployment.change_set_deployment/<id>`` route.
        """
        query_params = _query_params(evt)

        deployment_set = query_params.get("deployment_set")
        status = query_params.get("status")
        since = query_params.get("since")

        try:
            limit = int(query_params.get("limit", "50"))
        except (TypeError, ValueError):
            limit = 50
        try:
            offset = int(query_params.get("offset", "0"))
        except (TypeError, ValueError):
            offset = 0
        limit = max(1, min(limit, 500))
        offset = max(0, offset)

        if status and status not in CSD_STATUS_OPTIONS:
            raise ServiceException(
                f"status must be one of {', '.join(CSD_STATUS_OPTIONS)}; was {status}."
            )

        # Push filter + sort + limit + offset into a single Postgres query so
        # the work scales with page size, not table size. The earlier path
        # materialized every matching CSD into Python and sorted in memory,
        # which timed out as the table grew. ISO-8601 strings sort
        # lexicographically the same as the timestamps they represent (when
        # all values are tz-aware UTC), so the `since` >= and the deploy_time
        # ORDER BY are both correct as text comparisons.
        csd_namespace = ChangeSetDeployment.get_namespace_name()
        where_clauses = [
            sql.SQL("name = {ns}").format(ns=sql.Placeholder("ns")),
            sql.SQL("is_deleted = FALSE"),
        ]
        params: dict = {"ns": csd_namespace}

        def _add_text_filter(field: str, op: str, value: str, key: str):
            params[key] = value
            where_clauses.append(
                sql.SQL("(content ->> {field}) {op} {ph}").format(
                    field=sql.Literal(field),
                    op=sql.SQL(op),
                    ph=sql.Placeholder(key),
                )
            )

        if deployment_set:
            _add_text_filter("deployment_set_name", "=", deployment_set, "ds")
        if status:
            _add_text_filter("csd_status", "=", status, "st")
        if since:
            _add_text_filter("deploy_time", ">=", since, "sn")

        where_sql = sql.SQL(" AND ").join(where_clauses)

        select_fields = sql.SQL(
            "id, created_at, updated_at, "
            "content ->> 'deployment_set_name'   AS deployment_set_name, "
            "content ->> 'csd_status'            AS csd_status, "
            "content ->> 'deploy_time'           AS deploy_time, "
            "content ->> 'workflow_name'         AS workflow_name, "
            "content ->> 'workflow_submitted_at' AS workflow_submitted_at, "
            "content ->> 'callback_url'          AS callback_url, "
            "content ->> 'deployment_lock_name'  AS deployment_lock_name"
        )
        page_sql = sql.SQL(
            "SELECT {fields} FROM entity WHERE {where} "
            "ORDER BY (content ->> 'deploy_time') DESC NULLS LAST, id DESC "
            "LIMIT {limit} OFFSET {offset}"
        ).format(
            fields=select_fields,
            where=where_sql,
            limit=sql.Placeholder("limit"),
            offset=sql.Placeholder("offset"),
        )
        count_sql = sql.SQL(
            "SELECT COUNT(*) AS n FROM entity WHERE {where}"
        ).format(where=where_sql)

        db = get_first_db_engine(evt, ctx)
        with db.get_cursor() as cursor:
            cursor.execute(page_sql, {**params, "limit": limit, "offset": offset})
            rows = cursor.fetchall()
        with db.get_cursor() as cursor:
            cursor.execute(count_sql, params)
            count_row = cursor.fetchone()
        total = int(count_row["n"]) if count_row and count_row["n"] is not None else 0

        def _utc_iso(dt):
            if dt is None:
                return None
            return dt.replace(tzinfo=zoneinfo.ZoneInfo("UTC")).isoformat()

        items = [
            {
                "identifier": row["id"],
                "deployment_set_name": row["deployment_set_name"],
                "csd_status": row["csd_status"],
                "deploy_time": row["deploy_time"],
                "workflow_name": row["workflow_name"],
                "workflow_submitted_at": row["workflow_submitted_at"],
                "callback_url": row["callback_url"],
                "deployment_lock_name": row["deployment_lock_name"],
                "_created": _utc_iso(row["created_at"]),
                "_updated": _utc_iso(row["updated_at"]),
            }
            for row in rows
        ]

        return {
            "items": items,
            "total": total,
            "limit": limit,
            "offset": offset,
        }

    @service.operation(
        rest_path="/apiop/get_deployment_logs/<args_csd_id>",
        rest_methods=["GET"],
        args={"csd_id": "string"},
    )
    def get_deployment_logs(evt, ctx):
        """
        Retrieve Argo workflow logs for a ChangeSetDeployment.

        Query params:
        - pod_name: specific pod to fetch (optional)
        - list_only: if "true", return list of pods without logs (optional)
        - tail: return only last N lines per pod (optional)
        """
        deploy_client = _get_deploy_client(evt, ctx)
        csd_id = evt["args"]["csd_id"]
        query_params = _query_params(evt)
        pod_name = query_params.get("pod_name")
        list_only = query_params.get("list_only", "false").lower() == "true"
        tail = query_params.get("tail")
        if tail:
            tail = int(tail)

        # Get the ChangeSetDeployment
        try:
            csd = deploy_client.get_change_set_deployment_hmd_lang_deployment(csd_id)
        except Exception as e:
            raise HmdEntityNotFoundException(f"ChangeSetDeployment not found: {csd_id}")

        workflow_name = getattr(csd, "workflow_name", None)
        workflow_submitted_at = getattr(csd, "workflow_submitted_at", None)

        if not workflow_name:
            raise ServiceException(
                f"No workflow_name stored for ChangeSetDeployment {csd_id}. "
                "This deployment may have been created before workflow tracking was enabled."
            )

        # Get S3 bucket and namespace from environment
        logs_bucket = os.environ.get("ARGO_LOGS_BUCKET")
        argo_namespace = os.environ.get("ARGO_NAMESPACE", "argo-aaa")

        if not logs_bucket:
            raise ServiceException(
                "ARGO_LOGS_BUCKET environment variable not configured. "
                "Cannot retrieve workflow logs."
            )

        # Construct S3 path prefix
        # Format: {namespace}/{YYYY}-{MM}-{DD}/{workflow_name}/
        if workflow_submitted_at:
            date_prefix = workflow_submitted_at.strftime("%Y-%m-%d")
        else:
            # Fallback: list all dates and search for workflow
            date_prefix = None

        s3_client = get_session().client("s3")

        result = {
            "csd_id": csd_id,
            "workflow_name": workflow_name,
            "workflow_date": date_prefix,
            "pods": [],
        }

        try:
            pod_prefixes, resolved_date = list_pod_prefixes(
                s3_client, logs_bucket, argo_namespace, workflow_name, date_prefix
            )
            result["workflow_date"] = resolved_date

            # Filter by pod_name if specified
            if pod_name:
                pod_prefixes = {p for p in pod_prefixes if pod_name in p}

            # Process each pod
            for pod_prefix in sorted(pod_prefixes):
                pod_info = {
                    "pod_name": pod_name_from_prefix(pod_prefix),
                    "s3_path": pod_prefix,
                }

                if not list_only:
                    # Fetch the log content
                    log_key = f"{pod_prefix}main.log"
                    try:
                        response = s3_client.get_object(Bucket=logs_bucket, Key=log_key)
                        log_content = response["Body"].read().decode("utf-8")

                        if tail:
                            lines = log_content.split("\n")
                            log_content = "\n".join(lines[-tail:])

                        pod_info["logs"] = log_content
                    except ClientError as e:
                        if e.response["Error"]["Code"] == "NoSuchKey":
                            pod_info["logs"] = f"Log file not found: {log_key}"
                        else:
                            pod_info["logs"] = f"Error fetching logs: {str(e)}"

                result["pods"].append(pod_info)

            if not result["pods"]:
                LOGGER.warning(
                    f"No logs found for workflow {workflow_name} in bucket {logs_bucket}"
                )

        except ClientError as e:
            raise ServiceException(f"Error accessing S3 logs bucket: {str(e)}")

        return result

    @service.operation(
        rest_path="/apiop/get_deployment_timeline/<args_csd_id>",
        rest_methods=["GET"],
        args={"csd_id": "string"},
    )
    def get_deployment_timeline(evt, ctx):
        """Return per-instance deployment timeline records for a ChangeSetDeployment.

        For each RepoInstanceDeployment associated with the CSD's environments,
        returns ``{repo_instance_name, repo_class_name, environment, status,
        start, end, deployment_id, rid_nid}`` so consumers can render a Gantt.
        """
        deploy_client = _get_deploy_client(evt, ctx)
        rs = RelationshipSupport([deploy_client._base_client])
        csd_id = evt["args"]["csd_id"]
        return get_deployment_timeline_records(deploy_client, rs, csd_id)

    @service.operation(
        rest_path="/apiop/get_deployment_dag/<args_csd_id>",
        rest_methods=["GET"],
        args={"csd_id": "string"},
    )
    def get_deployment_dag(evt, ctx):
        """Return the per-environment deployment DAG for a ChangeSetDeployment.

        One node per RepoInstance being deployed, with ``dependencies`` naming
        the instances it waits on -- the same node/edge structure the Argo DAG
        tasks are built from -- plus each node's RID status and the archived
        pods that ran it, so consumers can draw the workflow.

        Query params:
        - include_pods: if "false", skip the per-pod S3 reads that resolve
          ``pod_names`` (optional, defaults to true)

        Unlike ``get_deployment_logs``, a ChangeSetDeployment with no
        ``workflow_name`` is not an error here: the DAG is still meaningful for
        a deployment that has not started yet or that ran locally.
        """
        deploy_client = _get_deploy_client(evt, ctx)
        rs = RelationshipSupport([deploy_client._base_client])
        csd_id = evt["args"]["csd_id"]
        include_pods = (
            _query_params(evt).get("include_pods", "true").lower() != "false"
        )
        return get_deployment_dag_records(
            deploy_client, rs, csd_id, include_pods=include_pods
        )
