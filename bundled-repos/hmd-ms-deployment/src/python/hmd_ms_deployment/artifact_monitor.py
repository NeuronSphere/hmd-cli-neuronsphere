import json
import os.path
from datetime import datetime
from logging import getLogger
from pathlib import Path
from tempfile import TemporaryDirectory
from typing import Dict, List, Optional

import yaml

from hmd_cli_tools import ServiceException
from hmd_graphql_client import BaseClient
from hmd_lang_deployment.repo_class_version_notes import (
    RepoClassVersionNotes,
)
from hmd_lang_deployment.external_artifact import ExternalArtifact
from hmd_lib_librarian_monitor.abstract_artifact_monitor import (
    AbstractArtifactMonitor,
    parse_content_item_path,
)
from hmd_lib_manifest.hmd_lib_manifest import read_manifest
from .class_information import ClassInformation
from .resource_information import ResourceInformation

logger = getLogger(f"HMD.{__name__}")


class ArtifactMonitor(AbstractArtifactMonitor):
    def __init__(
        self,
        librarian_client: BaseClient,
        class_information: ClassInformation,
        deployment_client: BaseClient,
        named_query: str,
        timestamp_name: str,
    ):
        super().__init__(
            librarian_client,
            deployment_client,
            named_query,
            timestamp_name,
        )
        self.class_information = class_information

    def _do_process_new_content_items(
        self,
        results: List[Dict],
        since_timestamp: datetime,
        until_timestamp: datetime,
        data: Optional[Dict] = None,
    ):
        for result in results:
            content_item = result["content_item"]
            download_url = result["download_url"]

            logger.debug(f"Content item path: {content_item['content_item_path']}")
            with TemporaryDirectory() as tmpdir:
                repo_class_name, repo_class_version = parse_content_item_path(
                    content_item["content_item_path"]
                )
                self._download_deployment_artifact(download_url, tmpdir)
                manifest = read_manifest(tmpdir)
                deploy = manifest.deploy or {}
                dependencies = deploy.get("dependencies", {})
                default_config = deploy.get("default_configuration", {})
                resources = deploy.get("resources", {})
                discovery = manifest.discovery or {}
                version_notes = None
                version_notes_file = (
                    Path(tmpdir) / "meta-data" / "version_notes.hmdentity"
                )
                if os.path.exists(version_notes_file):
                    with open(version_notes_file, "r") as fl:
                        version_notes = RepoClassVersionNotes(**json.load(fl))

                ext_artifacts: List[ExternalArtifact] = []
                ext_artifact_paths = Path(tmpdir) / "meta-data"
                ext_artifact_paths = ext_artifact_paths.rglob(
                    "*.ext_artifact.hmdentity"
                )

                for p in ext_artifact_paths:
                    if os.path.exists(p):
                        with open(p, "r") as ea:
                            ext_artifacts.append(ExternalArtifact(**json.load(ea)))

                try:
                    repo_class_version_entity = (
                        self.class_information.add_repo_version_by_name(
                            repo_class_name,
                            repo_class_version,
                            dependencies,
                            default_config,
                            version_notes,
                            discovery=discovery,
                        )
                    )
                except ServiceException as ex:
                    logger.error(
                        f"Error adding {repo_class_name}@{repo_class_version}: {str(ex)}"
                    )
                    continue

                self._ingest_resource_definitions(
                    Path(tmpdir), repo_class_version_entity, resources
                )

    def _ingest_resource_definitions(
        self,
        tmpdir: Path,
        repo_class_version,
        manifest_resources: Optional[Dict] = None,
    ):
        """Ingest ResourceDefinitions and declare what this RepoClassVersion produces.

        ``meta-data/resources/*.yaml`` (SPEC0003) is the sole place a
        ResourceDefinition *type* (schema, ``parent``/isa) gets defined or
        shipped -- that always runs, unchanged. manifest.json's
        ``deploy.resources`` (SPEC0010) is a separate, complementary way to
        declare that this RepoClassVersion *produces* an existing type,
        referencing it by identity only; it never defines or overwrites a
        type's schema. Declaring production of the same type through both a
        YAML file's inline ``produces: true`` and a ``deploy.resources`` entry
        is harmless -- ``declare_produces`` is idempotent.
        """
        resource_information = ResourceInformation(self.class_information.client)
        self._ingest_resource_definitions_from_yaml(
            tmpdir, repo_class_version, resource_information
        )
        if manifest_resources:
            self._declare_produces_from_manifest(
                manifest_resources, repo_class_version, resource_information
            )

    def _ingest_resource_definitions_from_yaml(
        self,
        tmpdir: Path,
        repo_class_version,
        resource_information: ResourceInformation,
    ):
        """Ingest ResourceDefinitions shipped in ``meta-data/resources/*.yaml``.

        Each YAML document describes a resource type the RepoClass produces
        (NERD0004 SPEC0003). Definitions are upserted idempotently on their
        (namespace, name, version) identity and linked to the producing
        RepoClassVersion via ``repo_class_version_has_resource_definition``. A
        document may declare ``produces: true`` to also record a
        ``repo_class_version_produces_resource_definition`` edge.
        """
        resource_paths = sorted((tmpdir / "meta-data" / "resources").glob("*.yaml"))
        if not resource_paths:
            return

        for path in resource_paths:
            try:
                with open(path, "r") as fl:
                    definition = yaml.safe_load(fl)
                if not definition:
                    continue
                produced_by_rcv_id = (
                    repo_class_version.identifier
                    if definition.get("produces")
                    else None
                )
                resource_information.upsert_resource_definition(
                    resource_namespace=definition["resource_namespace"],
                    resource_definition_name=definition["resource_definition_name"],
                    version=definition["version"],
                    description=definition.get("description"),
                    resource_metadata=definition.get("resource_metadata"),
                    output_schema=definition.get("output_schema"),
                    parent_ref=definition.get("parent"),
                    has_rcv_id=repo_class_version.identifier,
                    produced_by_rcv_id=produced_by_rcv_id,
                    role=definition.get("role"),
                )
            except (ServiceException, KeyError, yaml.YAMLError) as ex:
                logger.error(
                    f"Error ingesting resource definition {path.name}: {str(ex)}"
                )

    def _declare_produces_from_manifest(
        self,
        resources: Dict,
        repo_class_version,
        resource_information: ResourceInformation,
    ):
        """Declare produced resources from manifest.json's ``deploy.resources``.

        Each entry references an existing ResourceDefinition *by identity*
        (``resource_namespace``/``resource_definition_name``/``version``) --
        the type's schema/``parent`` are never read from here, only from
        ``meta-data/resources/*.yaml``. When the referenced definition isn't
        registered yet (producer-before-definition ordering), a minimal stub
        is upserted so the identity exists and the authoritative definition
        later reconciles into the same row -- mirroring the tolerant
        registration already used for consumer-side resource dependencies
        (``ClassInformation._add_resource_dependency``). ``produces`` defaults
        to ``true`` per the BACON spec when omitted; an explicit ``false``
        skips the entry entirely.
        """
        for resource_name, definition in resources.items():
            if not isinstance(definition, dict):
                logger.error(
                    f"deploy.resources.{resource_name} is not an object; skipping."
                )
                continue
            if not definition.get("produces", True):
                continue

            resource_namespace = definition.get("resource_namespace")
            resource_definition_name = definition.get("resource_definition_name")
            version = definition.get("version")
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
                logger.error(
                    f"deploy.resources.{resource_name} is missing required "
                    f"field(s): {', '.join(missing)}."
                )
                continue

            try:
                rd = resource_information.find_resource_definition(
                    resource_namespace, resource_definition_name, version
                )
                if rd is None:
                    rd = resource_information.upsert_resource_definition(
                        resource_namespace, resource_definition_name, version
                    )
                resource_information.declare_produces(
                    repo_class_version, rd, role=definition.get("role")
                )
            except ServiceException as ex:
                logger.error(
                    f"Error declaring deploy.resources.{resource_name}: {str(ex)}"
                )
