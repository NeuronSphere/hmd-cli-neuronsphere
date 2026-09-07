import logging
import os
from collections import defaultdict
from datetime import datetime
from json import dumps
from typing import List, Dict, DefaultDict, Tuple, Optional

import semver

from hmd_cli_tools import ServiceException
from hmd_graphql_client import BaseClient
from hmd_graphql_client.relationship_support import RelationshipSupport
from hmd_lang_deployment.change_set import ChangeSet
from hmd_lang_deployment.deployment_set import DeploymentSet
from hmd_lang_deployment.hmd_lang_deployment_client import HmdLangDeploymentClient
from hmd_lang_deployment.repo_class import RepoClass
from hmd_lang_deployment.repo_class_version import RepoClassVersion
from hmd_lang_deployment.repo_instance import RepoInstance
from hmd_lang_deployment.repo_instance_deployment import RepoInstanceDeployment
from hmd_lang_deployment.repo_instance_req_repo_instance import (
    RepoInstanceReqRepoInstance,
)
from hmd_lang_librarian.content_item_tag import ContentItemTag
from hmd_lang_librarian.hmd_lang_librarian_client import HmdLangLibrarianClient
from hmd_lib_librarian_monitor.abstract_artifact_monitor import (
    AbstractArtifactMonitor,
    parse_content_item_path,
)
from hmd_ms_deployment.class_information import ClassInformation
from hmd_ms_deployment.deployment_manager import DeploymentManager
from hmd_ms_deployment.environment_information import (
    EnvironmentInformation,
    get_current_repo_instance_deployment,
)

logger = logging.getLogger(f"HMD.{__name__}")


class AutoDeployMonitor(AbstractArtifactMonitor):
    def __init__(
        self,
        librarian_client: BaseClient,
        deployment_client: BaseClient,
        named_query: str,
        timestamp_name: str,
    ):
        super().__init__(
            librarian_client=librarian_client,
            timestamp_record_client=deployment_client,
            named_query=named_query,
            timestamp_name=timestamp_name,
            static_query_params={
                "tag_filter": {
                    "attribute": "tag_name",
                    "operator": "=",
                    "value": "auto_deploy",
                },
                "content_item_type_filter": {
                    "or": [
                        {
                            "attribute": "short_name",
                            "operator": "=",
                            "value": "build",
                        },
                        {
                            "attribute": "short_name",
                            "operator": "=",
                            "value": "configuration",
                        },
                    ]
                },
            },
        )
        self.typed_lib_client = HmdLangLibrarianClient(self.librarian_client)
        self.deployment_client = HmdLangDeploymentClient(deployment_client)
        self.deployment_manager = DeploymentManager(
            self.deployment_client, ClassInformation(self.deployment_client)
        )
        self.rs = RelationshipSupport([librarian_client, deployment_client])
        self.class_information = ClassInformation(self.deployment_client)

    def _do_process_new_content_items(
        self, results: List[Dict], since_timestamp: str, until_timestamp: str, data: Optional[Dict] = None
    ):
        assert data is not None and "Authorization" in data, f"Expected the 'Authorization' token."
        auth_token = data["Authorization"]

        deployment_set_dict: DefaultDict[
            str, Dict[str, Tuple[str, Dict]]
        ] = defaultdict(dict)
        deployment_sets = {}
        for result in results:
            logger.debug(
                f"Processing content item, {result['content_item']['identifier']}, "
                f"path: {result['content_item']['content_item_path']}"
            )
            deployment_set = self.get_deployment_set(
                result, since_timestamp, until_timestamp
            )
            if deployment_set:
                logger.debug(f"Deployment set name: {deployment_set.name}")
                if (
                    not deployment_set.auto_deploy
                    or deployment_set.auto_deploy == "false"
                ):
                    logger.warning(
                        f"auto_deploy flag for DeploymentSet {deployment_set.name} is 'false'"
                    )
                    continue

                deployment_sets[deployment_set.name] = deployment_set
                repo_name, repo_version = parse_content_item_path(
                    result["content_item"]["content_item_path"]
                )
                repo_class_dict = deployment_set_dict[deployment_set.name]
                if repo_name in repo_class_dict:
                    # There's already a version for this repository... (should never really happen:)
                    existing_version = repo_class_dict[repo_name][0]
                    if semver.compare(existing_version, repo_version) < 0:
                        repo_class_dict[repo_name] = (repo_version, result)
                else:
                    repo_class_dict[repo_name] = (repo_version, result)

        for deployment_set_name in deployment_set_dict:
            self.deploy_change_set_to_deployment_set(
                deployment_sets[deployment_set_name],
                deployment_set_dict[deployment_set_name],
                auth_token,
            )

    def get_deployment_set(
        self, result, since_timestamp: str, until_timestamp: str
    ) -> Optional[DeploymentSet]:
        content_item = self.typed_lib_client.get_content_item_hmd_lang_librarian(
            result["content_item"]["identifier"]
        )
        tag_rels = (
            self.typed_lib_client.get_from_content_item_has_tag_hmd_lang_librarian(
                content_item
            )
        )
        auto_deploy_tags: List[ContentItemTag] = [
            self.rs.ref_to(rel)
            for rel in tag_rels
            if self.rs.ref_to(rel).tag_name == "auto_deploy"
        ]
        assert len(auto_deploy_tags) <= 1, (
            f'Expected at most 1 tag with name "auto_deploy" was {len(auto_deploy_tags)}. '
            f"ContentItem {content_item.identifier}"
        )

        result = None
        if len(auto_deploy_tags) == 0:
            logger.warning(
                f'For content item, {content_item.identifier}, no "auto_deploy" tag found.'
            )
        else:
            deployment_set_name = auto_deploy_tags[0].tag_value

            deployment_sets = (
                self.deployment_client.search_deployment_set_hmd_lang_deployment(
                    {"attribute": "name", "operator": "=", "value": deployment_set_name}
                )
            )
            assert (
                len(deployment_sets) <= 1
            ), f"Expected 0 or 1 deployment set with name {deployment_set_name}, as {len(deployment_sets)}"
            if len(deployment_sets) == 0:
                logger.warning(f"No deployment sets with name: {deployment_set_name}.")
            else:
                result = deployment_sets[0]
        return result

    def deploy_change_set_to_deployment_set(
        self,
        deployment_set: DeploymentSet,
        stuff_to_deploy: Dict[str, Tuple[str, Dict]],
        auth_token: str,
    ):
        env_name = deployment_set.definition[0]["environment"]
        environment = self.deployment_client.search_environment_hmd_lang_deployment(
            {"attribute": "type", "operator": "=", "value": env_name}
        )[0]

        env_info = EnvironmentInformation(environment, self.deployment_client)

        changeset_def = []
        for repo_name in stuff_to_deploy:
            content_item_type = stuff_to_deploy[repo_name][1]["content_item_type"]
            changeset_def += self.create_deploy_specs_for_artifact(
                repo_name,
                stuff_to_deploy[repo_name][0],
                env_info,
                content_item_type,
            )

        logger.debug(f"ChangeSet definition: {dumps(changeset_def, indent=2)}")
        if len(changeset_def) > 0:
            new_changeset = ChangeSet(
                name=f"auto_deploy_{deployment_set.name}_{datetime.utcnow().isoformat(timespec='seconds')}",
                definition=changeset_def,
            )
            new_changeset = self.deployment_client.upsert(new_changeset)
            try:
                result = self.deployment_manager.apply_changeset(
                    new_changeset,
                    deployment_set,
                    auth_token,
                )
                logger.debug(f"Apply changeset result: {result}")
            except ServiceException as se:
                logger.error(
                    f"Error applying changeset: {str(se)}"
                )

    def create_deploy_specs_for_artifact(
        self,
        repo_name: str,
        version: str,
        env_info: EnvironmentInformation,
        content_item_type: str,
    ):
        logger.debug(
            f"Creating deploy specs for: {repo_name}@{version}:{content_item_type}..."
        )
        results = []
        repo_instance: RepoInstance
        for repo_instance in [
            self.rs.ref_to(rel)
            for rel in self.deployment_client.get_from_environment_has_repo_instance_hmd_lang_deployment(
                env_info.environment
            )
        ]:
            rc: RepoClass = self.rs.ref_to(
                self.deployment_client.get_from_repo_instance_isa_repo_class_hmd_lang_deployment(
                    repo_instance
                )[
                    0
                ]
            )
            if content_item_type == "build" and repo_name != rc.repo_class_name:
                continue

            ri_rids = self.deployment_client.get_from_repo_instance_has_repo_instance_deployment_hmd_lang_deployment(
                repo_instance
            )
            self.rs.pull_relationship_nouns(ri_rids)
            current_rid = get_current_repo_instance_deployment(repo_instance, self.rs)
            if not current_rid:
                logger.debug(
                    f"No current RepoInstanceDeployment for RepoInstance {repo_instance.name}."
                )
            else:
                rid: RepoInstanceDeployment = self.rs.ref_to(current_rid)
                rcv: RepoClassVersion = self.rs.ref_to(
                    self.deployment_client.get_from_repo_instance_deployment_has_repo_class_version_hmd_lang_deployment(
                        rid
                    )[
                        0
                    ]
                )
                if (
                    content_item_type == "configuration"
                    and rid.config_artifact_spec
                    and rid.config_artifact_spec.startswith(f"{repo_name}@")
                ) or (content_item_type == "build" and repo_name == rc.repo_class_name):
                    if (
                        not repo_instance.auto_deploy
                        or repo_instance.auto_deploy == "false"
                    ):
                        logger.warning(
                            f"auto_deploy flag for RepoInstance {repo_instance.name} is 'false'."
                        )
                        continue
                    data = {
                        "repo_instance_name": repo_instance.name,
                        "repo_class_name": rc.repo_class_name,
                        "repo_class_version": version,
                        "deployment_id": rid.deployment_id,
                        "auto_deploy": repo_instance.auto_deploy
                        if repo_instance.auto_deploy
                        else "false",
                    }
                    if rid.hmd_region:
                        data["hmd_region"] = rid.hmd_region
                    if rid.config_artifact_spec:
                        if content_item_type == "configuration":
                            file_spec_part = rid.config_artifact_spec.rsplit(
                                ":", maxsplit=1
                            )[1]
                            data["config_spec"] = f"{repo_name}@{version}:configuration:{file_spec_part}"
                        else:
                            data["config_spec"] = rid.config_artifact_spec
                    else:
                        data["instance_configuration"] = (
                            rid.instance_configuration
                            if rid.instance_configuration
                            else {}
                        )
                        ri_ris: List[RepoInstanceReqRepoInstance]
                        ri_ris = self.deployment_client.get_from_repo_instance_req_repo_instance_hmd_lang_deployment(
                            repo_instance
                        )
                        data["dependencies"] = {
                            ri_ri.role: self.rs.ref_to(ri_ri).name for ri_ri in ri_ris
                        }
                    results.append(data)

        return results
