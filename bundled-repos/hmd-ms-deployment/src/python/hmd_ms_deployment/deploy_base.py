import io
import json
import logging
import os
from copy import deepcopy
from typing import Dict, List, Set, Union

from hmd_cli_tools.hmd_cli_tools import deep_update
from hmd_graphql_client.relationship_support import RelationshipSupport
from hmd_lang_deployment.hmd_lang_deployment_client import HmdLangDeploymentClient
from hmd_lang_deployment.repo_class import RepoClass
from hmd_lang_deployment.repo_class_version import RepoClassVersion
from hmd_lang_deployment.repo_instance import RepoInstance
from hmd_lang_deployment.repo_instance_has_repo_instance_deployment import (
    RepoInstanceHasRepoInstanceDeployment,
)
from hmd_lang_deployment.repo_instance_req_repo_instance import (
    RepoInstanceReqRepoInstance,
)
from hmd_ms_deployment import DEPLOY_NEXT, DEPLOYED, DESTROY_NEXT, DESTROYED, FAILED
from hmd_ms_deployment.deploy_logic import GraphNode
from hmd_ms_deployment.environment_information import (
    get_repo_instance_deployment_for_status,
)
from hmd_ms_deployment.resource_information import ResourceInformation

logger = logging.getLogger(f"HMD.{__name__}")
status_check = """

hmd_exit=$?
status="{status}"
if [ $hmd_exit != 0 ]; then
  status="FAILED"
fi

"""

final_status_check = """
# if the status update failed, then fail...
if [ $? != 0 ]; then
    exit $?
fi
# otherwise use the status of the deployment command...
exit $hmd_exit
"""

EFS_MOUNT_POINT = "/mnt/efs/"

# Repo class of the telemetry-debug microservice. When an environment has a
# current deployment of this class, the deploy step exports HMD_DEBUG_URL to it
# so repos that ship meta-data/telemetry_profiles.json get their profiles seeded.
TELEMETRY_DEBUG_REPO_CLASS = "hmd-ms-telemetry-debug"


class DeployBase:
    def __init__(self, client: HmdLangDeploymentClient, rs: RelationshipSupport = None):
        self.client = client
        self.rs = rs if rs is not None else RelationshipSupport([client._base_client])
        self._instance_config_cache = {}

    def get_instance_config(
        self,
        instance: RepoInstance,
        destroy: bool,
        include_depenency_details: bool = True,
    ):
        cache_key = (instance.identifier, destroy, include_depenency_details)
        if cache_key in self._instance_config_cache:
            return self._instance_config_cache[cache_key]

        logger.debug(
            f"Pulling configuration for RepoInstance, {instance.name}, destroy, {destroy}"
        )
        repo_classes = (
            instance.get_from_repo_instance_isa_repo_class_hmd_lang_deployment()
        )
        logger.info(f"Repo classes for instance {instance.name} ({instance.identifier}): {repo_classes}")

        # If repo_classes is not cached, query the database
        if not repo_classes:
            logger.debug(f"RepoClass not cached for {instance.name}, querying database")
            repo_classes = self.client.get_from_repo_instance_isa_repo_class_hmd_lang_deployment(instance)
            logger.info(f"Repo classes from database: {repo_classes}")

        if not repo_classes:
            logger.error(
                f"RepoInstance {instance.name} ({instance.identifier}) has no associated RepoClass. "
                f"Cannot retrieve configuration."
            )
            self._instance_config_cache[cache_key] = {}
            return {}

        instance_class: RepoClass = self.rs.ref_to(repo_classes[0])
        if not destroy:
            instance_deployment = self._get_next_or_deployed(instance)
        else:
            instance_deployment = self._get_current_rid(instance)

        if not instance_deployment:
            logger.debug(f"No RepoInstanceDeployment for RepoInstance, {instance.name}")
            self._instance_config_cache[cache_key] = {}
            return {}

        logger.debug(
            f"RepoInstanceDeployment: id:{instance_deployment.identifier}, status: {instance_deployment.status}, "
            f"end: {instance_deployment.end}"
        )
        # Query the client for the RCV relationship rather than relying on
        # entity-cached relationships, which may be stale when multiple
        # RelationshipSupport instances are in play.
        rcv_rels = self.client.get_from_repo_instance_deployment_has_repo_class_version_hmd_lang_deployment(
            instance_deployment
        )
        self.rs.pull_relationship_nouns(rcv_rels)
        version: RepoClassVersion = self.rs.ref_to(rcv_rels[0])
        config = dict()
        config["dependencies"] = {}
        # config["instance_configuration"] = instance_deployment.instance_configuration
        config["instance_name"] = instance.name
        config["version"] = version.version
        config["deployment_id"] = instance_deployment.deployment_id
        if instance_deployment.hmd_region:
            config["hmd_region"] = instance_deployment.hmd_region
        config["repo_name"] = instance_class.repo_class_name

        if include_depenency_details:
            default_config = (
                deepcopy(version.default_configuration)
                if version.default_configuration
                else {}
            )

            if instance_deployment.instance_configuration:
                deep_update(default_config, instance_deployment.instance_configuration)

            config.update(default_config)

        dependencies: List[RepoInstanceReqRepoInstance] = (
            instance.get_from_repo_instance_req_repo_instance_hmd_lang_deployment()
        )
        # If dependencies not cached on entity, query the database
        if not dependencies:
            dependencies = self.client.get_from_repo_instance_req_repo_instance_hmd_lang_deployment(instance)
            self.rs.pull_relationship_nouns(dependencies)
        for dep in dependencies:
            dep_instance = self.rs.ref_to(dep)
            role_cfg = self.get_instance_config(dep_instance, destroy, False)
            # NERD0006: attach the dependency's produced Resource outputs (baked when
            # already deployed, or a runtime pointer when deployed in this ChangeSet).
            self._attach_dependency_resources(role_cfg, dep_instance, destroy)
            if dep.role not in config["dependencies"]:
                config["dependencies"][dep.role] = role_cfg
            else:
                if not isinstance(config["dependencies"][dep.role], list):
                    new_list = list()
                    new_list.append(config["dependencies"][dep.role])
                    config["dependencies"][dep.role] = new_list
                config["dependencies"][dep.role].append(role_cfg)
        self._instance_config_cache[cache_key] = config
        return config

    def _attach_dependency_resources(
        self, role_cfg: Dict, dep_instance: RepoInstance, destroy: bool
    ) -> None:
        """Attach a dependency's produced Resource outputs to its role config (NERD0006).

        For an already-deployed dependency the outputs exist and are stable (the
        deployment mutex serializes deploys), so they are **baked** into
        ``hmd_resources`` now. For a dependency being deployed in this same ChangeSet
        the outputs do not exist yet, so only a runtime pointer ``hmd_resource_ref``
        (the dependency's not-yet-deployed RepoInstanceDeployment identifier) is
        stamped; ``hmd deploy`` resolves it to ``hmd_resources`` just before the deploy
        command runs. Both keys carry the ``hmd_`` prefix so they cannot collide with
        user-supplied configuration. Best-effort: never raises into config generation.
        """
        if not isinstance(role_cfg, dict):
            return
        # Idempotent: role_cfg objects are cached and shared across parents.
        if "hmd_resources" in role_cfg or "hmd_resource_ref" in role_cfg:
            return
        try:
            dep_rid = (
                self._get_current_rid(dep_instance)
                if destroy
                else self._get_next_or_deployed(dep_instance)
            )
            if dep_rid is None:
                return
            if dep_rid.status == DEPLOY_NEXT:
                # Pending in this ChangeSet -- outputs don't exist yet; resolve at deploy time.
                role_cfg["hmd_resource_ref"] = {
                    "repo_instance_deployment_id": dep_rid.identifier
                }
            else:
                # Already deployed -- bake the current outputs into the config now.
                role_cfg["hmd_resources"] = ResourceInformation(
                    self.client
                ).serialize_resources_for_deployment(dep_rid)
        except Exception as e:
            logger.warning(
                f"Could not attach resource outputs for dependency "
                f"{getattr(dep_instance, 'name', '?')}: {e}"
            )

    def get_active_dependencies(
        self, node: GraphNode, statuses: List[str], repo_instance_deployment_ids: List[str]
    ) -> Set:
        results = set()
        for child in node.children:
            # Use client query instead of cached entity method to ensure
            # newly created RIDs (e.g., DESTROY_NEXT) are found
            rids = self.client.get_from_repo_instance_has_repo_instance_deployment_hmd_lang_deployment(
                child.instance
            )
            instance_deployment = None
            for rid_rel in rids:
                rid = self.rs.ref_to(rid_rel)
                # Filter by repo_instance_deployment_ids to only consider RIDs in the current changeset
                # This prevents orphaned DEPLOY_NEXT RIDs from being returned as dependencies
                if rid.status in statuses and not rid.end and rid_rel.ref_to in repo_instance_deployment_ids:
                    instance_deployment = rid
                    break

            if instance_deployment:
                results.add(child.instance.name)
            else:
                results.update(
                    self.get_active_dependencies(child, statuses, repo_instance_deployment_ids)
                )

        return results

    def deploy_node(self, node: GraphNode, context: Dict):
        assert (
            "command_list" in context
        ), "I expected a list object in the context, what's up?"
        assert (
            "environment" in context
        ), "I expected an environment in the context, what's up?"
        file_ = io.StringIO()
        environment: str = context["environment"]
        account: str = context.get("account")
        command_list = context["command_list"]
        destroy: bool = context.get("destroy", False)
        profile: str = context.get("profile", None)
        configuration_directory: str = context.get("configuration_directory")
        repo_instance_deployment_ids = context["repo_instance_deployment_ids"]
        profile_str: str = f" --profile {profile} " if profile else ""
        account_str: str = (
            f" --account {account} " if account and environment != "admin" else ""
        )
        instance_configs: Dict[str, Dict] = context["instance_configs"]
        generate_deployed_call = context.get("generate_deployed_call", True)

        instance = node.instance
        logger.debug(
            f"Generating deployment for instance, {instance.name}, destroy, {destroy}"
        )
        repo_classes = (
            instance.get_from_repo_instance_isa_repo_class_hmd_lang_deployment()
        )

        # If repo_classes is not cached, query the database
        if not repo_classes:
            logger.debug(f"RepoClass not cached for {instance.name}, querying database")
            repo_classes = self.client.get_from_repo_instance_isa_repo_class_hmd_lang_deployment(instance)

        if not repo_classes:
            logger.error(
                f"RepoInstance {instance.name} ({instance.identifier}) has no associated RepoClass. "
                f"Cannot generate deployment."
            )
            return

        instance_class = self.rs.ref_to(repo_classes[0])  # type: RepoClass
        repo_class_name = instance_class.repo_class_name
        instance_deployments = [
            self.rs.ref_to(rel)
            for rel in self.client.get_from_repo_instance_has_repo_instance_deployment_hmd_lang_deployment(instance)
            if rel.ref_to in repo_instance_deployment_ids
        ]
        assert len(instance_deployments) <= 1

        if not instance_deployments:
            logger.debug("No instance deployment.")
            return

        instance_deployment = instance_deployments[0]
        assert (DESTROY_NEXT if destroy else DEPLOY_NEXT) == instance_deployment.status
        # Query the client for the RCV relationship rather than relying on
        # entity-cached relationships, which may be stale when multiple
        # RelationshipSupport instances are in play.
        rcv_rels = self.client.get_from_repo_instance_deployment_has_repo_class_version_hmd_lang_deployment(
            instance_deployment
        )
        self.rs.pull_relationship_nouns(rcv_rels)
        version: RepoClassVersion = self.rs.ref_to(rcv_rels[0])

        print(
            file=file_,
        )
        # Machine-readable step marker, emitted as the first line of this step's
        # stdout. Argo garbage-collects workflows after a few days, but the pod
        # logs stay archived in S3 indefinitely -- so the log itself is the only
        # place a step -> RepoInstanceDeployment mapping survives the workflow.
        # step_log_index.build_step_pod_index reads it back to answer "which pod
        # ran this instance". Keep the format in sync with its MARKER_RE.
        print(
            f"echo '##HMD_STEP env={environment} instance={instance.name} "
            f"rid={instance_deployment.identifier}'",
            file=file_,
        )
        print(
            f"echo  '####'\necho '{repo_class_name}:{instance.name}: {version.version}'",
            file=file_,
        )

        region_arg = ""
        if instance_deployment.hmd_region:
            region_arg = f"--hmd-region {instance_deployment.hmd_region}"

        destroy_string = "" if not destroy else "--destroy"
        register_string = "" if not generate_deployed_call else "--register"
        if not instance_deployment.image_only:
            # Pass the RepoInstanceDeployment identifier via an environment variable
            # rather than a CLI flag so an older HMD_APP_IMAGE whose hmd-cli-deploy
            # predates resource submission ignores it (unknown env vars are harmless)
            # instead of failing on an unrecognized argument. Newer hmd-cli-deploy
            # reads it as the default for --repo-instance-deployment-id.
            command_string = (
                f"export HMD_REPO_INSTANCE_DEPLOYMENT_ID={instance_deployment.identifier}\n"
                f"hmd --debug {profile_str} --repo-name {instance_class.repo_class_name} --repo-version "
                f"{version.version} {region_arg} deploy {destroy_string} {register_string} "
                f"--instance-name {instance.name} --environment {environment} {account_str} "
                f"--deployment-id {instance_deployment.deployment_id} "
                f"--config-file "
            )
            command_string += (
                # Quote the heredoc delimiter (<<'EOF', not <<EOF) so the shell that
                # runs this generated script does NOT perform parameter/backtick/backslash
                # expansion on the JSON config body. Unquoted, bash collapses JSON's own
                # `\\` escapes and expands `$`, corrupting the payload before the child
                # `hmd ... deploy --config-file STDIN` reads it with safe_load(sys.stdin)
                # (e.g. Airflow's AIRFLOW__CORE__DAG_IGNORE_FILE_REGEX, full of `\` and `$`,
                # triggered "while scanning a double-quoted scalar"). Only the local path
                # uses this branch; cloud writes a real file (else-branch) and is unaffected.
                "STDIN <<'EOF'"
                if not configuration_directory
                else f"/mnt/deploy_base/{configuration_directory}/{instance.name}.json"
            )
        else:
            command_string = (
                f"hmd --debug {profile_str} --repo-name {instance_class.repo_class_name} "
                f"--repo-version {version.version} {region_arg} "
                f"docker {'deploy' if not destroy else 'destroy'} {account_str}"
            )
        logger.debug(f"Command string: {command_string}")
        print(
            command_string,
            file=file_,
        )
        if not instance_deployment.image_only:
            if not configuration_directory:
                json.dump(instance_configs[instance.name], file_, indent=2),

                print(
                    "\nEOF",
                    file=file_,
                )
            else:
                dir_path = f"{EFS_MOUNT_POINT}/{configuration_directory}"
                if not os.path.exists(dir_path):
                    os.makedirs(dir_path, exist_ok=True)
                with open(f"{dir_path}/{instance.name}.json", "w") as fl:
                    json.dump(instance_configs[instance.name], fl, indent=2)

        if generate_deployed_call:
            print(
                status_check.format(
                    **{"status": DEPLOYED if not destroy else DESTROYED}
                ),
                file=file_,
            )
            print(
                f"hmd --hmd-region {os.environ.get('HMD_REGION', 'reg1')} deployment set-deployment-status --neuronsphere-id {instance_deployment.identifier} --status $status",
                file=file_,
            )

            print(final_status_check, file=file_)

        generated = file_.getvalue()
        if generated:
            command_list.append(
                (
                    generated,
                    node,
                    self.get_active_dependencies(
                        node,
                        [DEPLOY_NEXT] if not destroy else [DESTROY_NEXT],
                        repo_instance_deployment_ids,
                    ),
                )
            )

    def _get_next_or_deployed(self, instance: RepoInstance):
        result = None
        rels = instance.get_from_repo_instance_has_repo_instance_deployment_hmd_lang_deployment()
        # If relationships not cached on entity, query the database
        if not rels:
            rels = self.client.get_from_repo_instance_has_repo_instance_deployment_hmd_lang_deployment(instance)
            self.rs.pull_relationship_nouns(rels)

        for rel in rels:
            if self.rs.ref_to(rel).status == DEPLOY_NEXT:
                result = self.rs.ref_to(rel)
                break

        if not result:
            for rel in rels:
                if rel.current.lower() == "true" and self.rs.ref_to(rel).status in [
                    DEPLOYED,
                    FAILED,
                ]:
                    result = self.rs.ref_to(rel)
                    break

        return result

    def _get_current_rid(self, instance):
        result = None
        rels = instance.get_from_repo_instance_has_repo_instance_deployment_hmd_lang_deployment()
        # If relationships not cached on entity, query the database
        if not rels:
            rels = self.client.get_from_repo_instance_has_repo_instance_deployment_hmd_lang_deployment(instance)
            self.rs.pull_relationship_nouns(rels)

        for rel in rels:
            if rel.current.lower() == "true":
                result = self.rs.ref_to(rel)
                break

        return result

    def get_telemetry_debug_url(self, environment, customer_code):
        """Resolve the telemetry-debug (``ms-telemetry``) endpoint for an environment.

        Discovers whether the environment currently has a deployed
        ``hmd-ms-telemetry-debug`` instance and, if so, builds its public URL so
        the deploy step can export it as ``HMD_DEBUG_URL`` and seed telemetry
        service profiles. "Available" means the instance has a *current*
        RepoInstanceDeployment -- an instance being stood up for the first time
        in this same change set is not yet current, so seeding starts on the
        next deploy once it is.

        Best-effort: returns ``None`` when no telemetry instance is present or
        discovery fails, in which case the downstream seeding step is skipped.
        """
        try:
            rels = self.client.get_from_environment_has_repo_instance_hmd_lang_deployment(
                environment
            )
            self.rs.pull_relationship_nouns(rels)
            for rel in rels:
                instance = self.rs.ref_to(rel)
                repo_classes = self.client.get_from_repo_instance_isa_repo_class_hmd_lang_deployment(
                    instance
                )
                if not repo_classes:
                    continue
                repo_class = self.rs.ref_to(repo_classes[0])
                if repo_class.repo_class_name != TELEMETRY_DEBUG_REPO_CLASS:
                    continue

                rid = self._get_current_rid(instance)
                if not rid:
                    logger.debug(
                        f"Telemetry instance {instance.name} has no current "
                        f"deployment; HMD_DEBUG_URL will not be set."
                    )
                    return None

                hmd_region = rid.hmd_region or environment.hmd_region
                env_str = "" if environment.type == "prod" else f"-{environment.type}"
                return (
                    f"https://{instance.name}-{rid.deployment_id}-{hmd_region}."
                    f"{customer_code}{env_str}-neuronsphere.io"
                )
        except Exception as e:
            logger.warning(
                f"Could not resolve telemetry-debug URL for environment "
                f"{getattr(environment, 'type', '?')}: {e}"
            )
        return None
