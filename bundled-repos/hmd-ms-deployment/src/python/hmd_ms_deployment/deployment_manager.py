import logging
import os
import time
import datetime
from json import dumps
from typing import Dict, Tuple, Optional, Set, List

import requests
from boto3 import Session

from hmd_cli_tools import ServiceException
from hmd_cli_tools.hmd_cli_tools import get_secret, get_session
from hmd_graphql_client.relationship_support import RelationshipSupport
from hmd_lang_deployment.change_set import ChangeSet
from hmd_lang_deployment.change_set_deployment import ChangeSetDeployment
from hmd_lang_deployment.change_set_deployment_has_change_set import (
    ChangeSetDeploymentHasChangeSet,
)
from hmd_lang_deployment.change_set_deployment_has_change_set_env_deployment import (
    ChangeSetDeploymentHasChangeSetEnvDeployment,
)
from hmd_lang_deployment.change_set_deployment_has_deployment_set import (
    ChangeSetDeploymentHasDeploymentSet,
)
from hmd_lang_deployment.change_set_env_deployment import ChangeSetEnvDeployment
from hmd_lang_deployment.change_set_env_deployment_has_environment import (
    ChangeSetEnvDeploymentHasEnvironment,
)
from hmd_lang_deployment.change_set_env_deployment_has_repo_instance_deployment import (
    ChangeSetEnvDeploymentHasRepoInstanceDeployment,
)
from hmd_lang_deployment.deployment_set import DeploymentSet
from hmd_lang_deployment.hmd_lang_deployment_client import HmdLangDeploymentClient
from hmd_lang_deployment.repo_instance import RepoInstance
from hmd_lang_deployment.repo_instance_deployment import RepoInstanceDeployment
from hmd_lang_deployment.repo_instance_deployment_has_repo_class_version import (
    RepoInstanceDeploymentHasRepoClassVersion,
)
from hmd_lang_deployment.repo_instance_has_repo_instance_deployment import (
    RepoInstanceHasRepoInstanceDeployment,
)
from hmd_ms_deployment import (
    DEPLOY_NEXT,
    CREATED,
    CSED_STATUS_OPTIONS,
    CSD_STATUS_OPTIONS,
    FAILED,
    COMPLETED,
    STARTED,
    SKIPPED,
    DESTROY_NEXT,
    DEPLOYED,
)
from hmd_ms_deployment.class_information import ClassInformation
from hmd_ms_deployment.deploy_logic import (
    build_deployment_dag,
    GraphNode,
    DeploymentDag,
    validate_dependencies_satisfied,
)
from hmd_ms_deployment.deploy_workflow_creator import DeployWorkflowCreator
from hmd_ms_deployment.local_deployment import generate_local_deploy_manifest
from hmd_ms_deployment.environment_information import (
    EnvironmentInformation,
    get_current_repo_instance_deployment,
    mark_rid_status,
)

logger = logging.getLogger(f"HMD.{__name__}")


def _get_argo_url():
    return f"https://{os.environ['ARGO_INSTANCE_NAME']}.{os.environ['HMD_CUSTOMER_CODE']}-admin-neuronsphere.io"


def _get_runner_url():
    """The workflow runner to submit to instead of Argo, or None for Argo.

    This is the whole of the runner selection. Locally there is no Argo -- the
    ARGO_* variables are not even set, so building a workflow raises KeyError,
    which is why ``skip_async`` existed: to stop this service doing what it does
    in the cloud. Configuring an endpoint instead makes that a positive choice
    rather than a suppression, and leaves the cloud path untouched, since
    nothing sets this variable there.
    """
    return os.environ.get("HMD_WORKFLOW_RUNNER_URL") or None


def call_deploy_change_set_deployment(identifier, auth_token: str):
    """Asynchronously call this lambda to complete the apply_changeset process.

    :param identifier:
    :type identifier:
    :return:
    :rtype:
    """
    session = Session()
    lambda_client = session.client("lambda")
    payload = {
        "path": f"/apiop/deploy_change_set_deployment/{identifier}",
        "httpMethod": "POST",
        "body": None,
        "headers": {"Authorization": auth_token},
        "resource": {},
        "requestContext": {},
    }
    lambda_result = lambda_client.invoke(
        FunctionName=os.environ["AWS_LAMBDA_FUNCTION_NAME"],
        Payload=dumps(payload),
        InvocationType="Event",
    )

    # This shouldn't happen because we catch exceptions and return from the lambda
    # Still...
    if lambda_result["StatusCode"] != 202:
        raise Exception(lambda_result["Payload"])

    return {}


def call_deploy_destroy_changeset_deployment(
    identifier: str, auth_token: str, destroy_from: List[str]
):
    """Asynchronously call this lambda to complete the destroy_deployment_set process.

    :param identifier: The ChangeSetDeployment identifier
    :param auth_token: Authorization token
    :param destroy_from: Required list of instance names to destroy from. Cannot be empty.
    :raises ValueError: If destroy_from is empty or None
    """
    if not destroy_from:
        raise ValueError("destroy_from is required and cannot be empty")

    session = Session()
    lambda_client = session.client("lambda")
    payload = {
        "path": f"/apiop/deploy_destroy_changeset_deployment/{identifier}",
        "httpMethod": "POST",
        "body": dumps({"destroy_from": destroy_from}),
        "headers": {"Authorization": auth_token},
        "resource": {},
        "requestContext": {},
    }
    lambda_result = lambda_client.invoke(
        FunctionName=os.environ["AWS_LAMBDA_FUNCTION_NAME"],
        Payload=dumps(payload),
        InvocationType="Event",
    )

    # This shouldn't happen because we catch exceptions and return from the lambda
    # Still...
    if lambda_result["StatusCode"] != 202:
        raise Exception(lambda_result["Payload"])

    return {}


class DeploymentManager:
    def __init__(self, client: HmdLangDeploymentClient, class_info: ClassInformation):
        self.client = client
        self.rs = RelationshipSupport([client._base_client])
        self.class_info = class_info

    def apply_changeset(
        self,
        change_set: ChangeSet,
        deployment_set: DeploymentSet,
        auth_token: Optional[str] = None,
        deployment_lock_name: Optional[str] = None,
        callback_url: Optional[str] = None,
        skip_async: bool = False,
    ):

        csd = ChangeSetDeployment(
            deployment_set_name=deployment_set.name,
            deployment_set_definition=deployment_set.definition,
            deploy_time=datetime.datetime.now(datetime.timezone.utc),
            csd_status=CREATED,
            deployment_lock_name=deployment_lock_name,
        )
        if callback_url is not None:
            csd.callback_url = callback_url
        self.client.upsert(csd)

        csd_cs = ChangeSetDeploymentHasChangeSet(
            ref_from=csd.identifier, ref_to=change_set.identifier
        )
        self.client.upsert(csd_cs)

        csd_ds = ChangeSetDeploymentHasDeploymentSet(
            ref_from=csd.identifier, ref_to=deployment_set.identifier
        )
        self.client.upsert(csd_ds)

        environments = self.client.search_environment_hmd_lang_deployment({})
        for i, env_def in enumerate(csd.deployment_set_definition):
            env_info = EnvironmentInformation(
                environment=[
                    env for env in environments if env.type == env_def["environment"]
                ][0],
                client=self.client,
            )
            logger.info(
                f"Applying changeset, {change_set.name}, to environment {env_info.environment.type}"
            )
            csed = self.apply_changeset_to_environment(env_info, change_set, csd, i)

        if not skip_async:
            logger.info(f"Asynchronously deploying ChangeSetDeployment, {csd.identifier}.")
            call_deploy_change_set_deployment(csd.identifier, auth_token)
        else:
            logger.info(
                f"skip_async=True — ChangeSetDeployment {csd.identifier} ready for local execution."
            )

        return {"csd_nid": csd.identifier}

    def deploy_change_set_deployment(
        self, csd_nid: str, customer_code: str, hmd_region: str,
        auth_token: Optional[str] = None
    ):
        logger.info(f"Completing deployment for ChangeSetDeployment, {csd_nid}")
        csd = self.client.get_change_set_deployment_hmd_lang_deployment(csd_nid)
        # apply_changeset invokes this async (InvocationType="Event"); the entity write
        # may not have propagated yet, so retry briefly before giving up.
        attempts = 0
        while csd is None and attempts < 5:
            time.sleep(0.5)
            attempts += 1
            logger.info(
                f"ChangeSetDeployment {csd_nid} not yet visible; retry {attempts}/5"
            )
            csd = self.client.get_change_set_deployment_hmd_lang_deployment(csd_nid)
        if csd is None:
            raise ServiceException(
                f"ChangeSetDeployment not found for identifier {csd_nid}"
            )

        try:
            deployment_set = self.rs.ref_to(
                self.client.get_from_change_set_deployment_has_deployment_set_hmd_lang_deployment(
                    csd
                )[
                    0
                ]
            )
            change_set = self.rs.ref_to(
                self.client.get_from_change_set_deployment_has_change_set_hmd_lang_deployment(
                    csd
                )[
                    0
                ]
            )
            env_infos: Dict[
                str, Tuple[EnvironmentInformation, ChangeSetEnvDeployment, DeploymentDag]
            ] = dict()
            for csed in [
                self.rs.ref_to(rel)
                for rel in self.client.get_from_change_set_deployment_has_change_set_env_deployment_hmd_lang_deployment(
                    csd
                )
            ]:
                env_info = EnvironmentInformation(
                    self.rs.ref_to(
                        self.client.get_from_change_set_env_deployment_has_environment_hmd_lang_deployment(
                            csed
                        )[
                            0
                        ]
                    ),
                    client=self.client,
                )
                repo_instance_deployment_ids = [
                    rel.ref_to
                    for rel in self.client.get_from_change_set_env_deployment_has_repo_instance_deployment_hmd_lang_deployment(
                        csed
                    )
                ]
                logger.debug(
                    f"{len(repo_instance_deployment_ids)} RepoInstanceDeployment ids associated with ChangeSetEnvDeployment."
                )
                assert (
                    len(repo_instance_deployment_ids) > 0
                ), f"No RepoInstanceDeployment's found for environment, {env_info.environment.type}"
                deployment_dag = build_deployment_dag(
                    env_info, repo_instance_deployment_ids, destroy=False
                )
                # Validate that all dependencies are satisfied before proceeding
                validate_dependencies_satisfied(
                    env_info, repo_instance_deployment_ids
                )
                env_infos[env_info.environment.type] = (env_info, csed, deployment_dag)

            runner_url = _get_runner_url()
            if runner_url:
                # Branch here rather than inside submit_workfow: generating the
                # workflow is itself the cloud-only part, needing HMD_APP_IMAGE,
                # WORKERS_INSTANCE_NAME and an Okta service-account token that
                # do not exist locally.
                submit_results = self.submit_local_workflow(
                    csd, next(iter(env_infos)), runner_url
                )
            else:
                workflow = self.generate_deploy_workflow(
                    csd,
                    change_set,
                    env_infos,
                    customer_code,
                    hmd_region,
                    deployment_set.name,
                    deployment_lock_name=csd.deployment_lock_name,
                )
                submit_results = self.submit_workfow(workflow, csd)
            submit_results["csd_nid"] = csd.identifier
            return submit_results
        except Exception as e:
            logger.error(f"Error during deployment workflow generation: {str(e)}", exc_info=True)
            if csd is not None:
                self.update_change_set_deployment_status(csd, FAILED, auth_token=auth_token)
            raise

    def destroy_deployment_set(
        self,
        deployment_set: DeploymentSet,
        destroy_from: List[str],
        auth_token: Optional[str] = None,
        deployment_lock_name: Optional[str] = None,
        callback_url: Optional[str] = None,
        skip_async: bool = False,
    ):
        """Initiate destruction of instances in a deployment set.

        This method returns quickly with a ChangeSetDeployment ID. The actual
        heavy work (DAG building, instance enumeration, workflow generation)
        is performed asynchronously.

        :param deployment_set: The deployment set to destroy instances in
        :param destroy_from: Required list of instance names to destroy from.
            Cannot be empty - must specify which instances to destroy.
        :param auth_token: Authorization token for async invocation
        :param deployment_lock_name: Optional mutex name for workflow coordination
        :param callback_url: Optional URL to receive status notifications
        :param skip_async: Prepare the destroy synchronously and return, instead
            of invoking this Lambda asynchronously to generate and submit an Argo
            workflow. This is the local-deploy path: there is no Argo to submit
            to and no asynchronous Lambda invocation to make, so the caller
            prepares the ``DESTROY_NEXT`` deployments here and then drives
            execution itself via ``generate_local_deployment``. Mirrors the same
            flag on :meth:`apply_changeset`.
        :returns: Dict with csd_nid (ChangeSetDeployment identifier)
        :raises ValueError: If destroy_from is empty or None
        """
        if not destroy_from:
            raise ValueError(
                "destroy_from is required and cannot be empty - "
                "must specify instances to destroy"
            )

        logger.info(
            f"Destroying deployment set, {deployment_set.name}, "
            f"destroy_from: [{','.join(destroy_from)}]"
        )

        # Create ChangeSetDeployment
        csd = ChangeSetDeployment(
            deployment_set_name=deployment_set.name,
            deployment_set_definition=deployment_set.definition,
            deploy_time=datetime.datetime.now(datetime.timezone.utc),
            csd_status=CREATED,
            deployment_lock_name=deployment_lock_name,
        )
        if callback_url is not None:
            csd.callback_url = callback_url
        self.client.upsert(csd)

        csd_ds = ChangeSetDeploymentHasDeploymentSet(
            ref_from=csd.identifier, ref_to=deployment_set.identifier
        )
        self.client.upsert(csd_ds)

        # Create minimal CSED records for each environment
        # RepoInstanceDeployments are created in the async path
        environments = self.client.search_environment_hmd_lang_deployment({})
        for i, env_def in enumerate(deployment_set.definition):
            env = [e for e in environments if e.type == env_def["environment"]][0]

            csed = ChangeSetEnvDeployment(
                deploy_time=datetime.datetime.now(datetime.timezone.utc),
                csed_status=CREATED,
            )
            csed = self.client.upsert(csed)

            self.client.upsert(
                ChangeSetDeploymentHasChangeSetEnvDeployment(
                    ref_from=csd.identifier,
                    ref_to=csed.identifier,
                    order=i,
                )
            )
            self.client.upsert(
                ChangeSetEnvDeploymentHasEnvironment(
                    ref_from=csed.identifier, ref_to=env.identifier
                )
            )
            logger.debug(
                f"Created ChangeSetEnvDeployment for environment {env.type}"
            )

        if skip_async:
            # No Argo, no async Lambda: create the DESTROY_NEXT deployments now
            # so the caller can immediately ask for a local destroy manifest.
            env_infos = self.prepare_destroy_environments(csd, destroy_from)
            if not env_infos:
                logger.warning("No environments have instances to destroy")
                self.update_change_set_deployment_status(csd, COMPLETED)
                return {
                    "csd_nid": csd.identifier,
                    "message": "No instances to destroy",
                }
            logger.info(
                f"skip_async=True — ChangeSetDeployment {csd.identifier} ready "
                "for local destroy execution."
            )
            return {"csd_nid": csd.identifier}

        logger.info(
            f"Asynchronously destroying ChangeSetDeployment, {csd.identifier}."
        )
        call_deploy_destroy_changeset_deployment(csd.identifier, auth_token, destroy_from)

        return {"csd_nid": csd.identifier}

    def destroy_deployment_set_in_environment(
        self,
        env_info: EnvironmentInformation,
        change_set_deployment: ChangeSetDeployment,
        order: int,
        destroy_from: List[str] = None,
    ) -> ChangeSetEnvDeployment:
        if destroy_from is None:
            destroy_from = []

        csed = ChangeSetEnvDeployment(
            deploy_time=datetime.datetime.now(datetime.timezone.utc), csed_status=CREATED
        )
        csed = self.client.upsert(csed)

        csd_csed = ChangeSetDeploymentHasChangeSetEnvDeployment(
            ref_from=change_set_deployment.identifier,
            ref_to=csed.identifier,
            order=order,
        )
        self.client.upsert(csd_csed)

        csed_env = ChangeSetEnvDeploymentHasEnvironment(
            ref_from=csed.identifier, ref_to=env_info.environment.identifier
        )
        self.client.upsert(csed_env)

        deployment_dag = build_deployment_dag(
            env_info, repo_instance_deployment_ids=[], destroy=True
        )
        instances = self.destroy_from_instance(env_info, deployment_dag, destroy_from)
        logger.debug(
            f"{len(instances)} instances to be destroyed. {', '.join(inst.name for inst in instances)}"
        )
        for repo_instance in instances:
            logger.debug(f"Evaluating instance, {repo_instance.name} for deletion.")

            current_ri_rid = get_current_repo_instance_deployment(
                repo_instance, self.rs
            )
            logger.debug(
                f"Instance, {repo_instance.name} has current: {current_ri_rid is not None}"
            )
            if current_ri_rid:
                current_rid = self.rs.ref_to(current_ri_rid)
                logger.debug(f"Current deployment status: {current_rid.status}")
                if current_rid.status in [DEPLOYED, FAILED]:
                    new_deployment = RepoInstanceDeployment(
                        deployment_id="aaa", status=DESTROY_NEXT
                    )
                    self.client.upsert(new_deployment)
                    self.client.upsert(
                        RepoInstanceHasRepoInstanceDeployment(
                            ref_from=repo_instance.identifier,
                            ref_to=new_deployment.identifier,
                            current="false",
                        )
                    )
                    # make sure the new objects are cached
                    self.rs.pull_relationship_nouns(
                        self.client.get_from_repo_instance_has_repo_instance_deployment_hmd_lang_deployment(
                            repo_instance
                        )
                    )
                    new_rel = ChangeSetEnvDeploymentHasRepoInstanceDeployment(
                        ref_from=csed.identifier,
                        ref_to=new_deployment.identifier,
                    )
                    self.client.upsert(new_rel)
                    csed.from_rels[
                        ChangeSetEnvDeploymentHasRepoInstanceDeployment.get_namespace_name()
                    ].append(new_rel)

                    rid_rcvs = self.client.get_from_repo_instance_deployment_has_repo_class_version_hmd_lang_deployment(
                        current_rid
                    )
                    self.client.upsert(
                        RepoInstanceDeploymentHasRepoClassVersion(
                            ref_from=new_deployment.identifier,
                            ref_to=rid_rcvs[0].ref_to,
                        )
                    )
                    # make sure the new objects are cached
                    self.rs.pull_relationship_nouns(
                        self.client.get_from_repo_instance_deployment_has_repo_class_version_hmd_lang_deployment(
                            new_deployment
                        )
                    )
        return csed

    def prepare_destroy_environments(
        self, csd: ChangeSetDeployment, destroy_from: List[str]
    ) -> Dict[
        str, Tuple[EnvironmentInformation, ChangeSetEnvDeployment, DeploymentDag]
    ]:
        """Create the ``DESTROY_NEXT`` deployments for a destroy ChangeSetDeployment.

        For each of the CSD's ChangeSetEnvDeployments: build the environment's
        full deployment DAG, expand ``destroy_from`` to the instances that must
        go with it, and record a new ``DESTROY_NEXT`` RepoInstanceDeployment for
        each one that is currently ``DEPLOYED`` or ``FAILED``. An environment
        with nothing to destroy is marked ``SKIPPED`` and left out of the result.

        This is the half of a destroy that mutates the graph. It is deliberately
        separate from workflow generation so a local (Argo-less) deploy can run
        it and then drive execution itself via ``generate_local_deployment`` --
        see ``destroy_deployment_set(skip_async=True)``.

        :returns: ``{environment_type: (env_info, csed, deployment_dag)}`` for
            every environment that has at least one instance to destroy.
        """
        env_infos: Dict[
            str, Tuple[EnvironmentInformation, ChangeSetEnvDeployment, DeploymentDag]
        ] = dict()

        for csed in [
            self.rs.ref_to(rel)
            for rel in self.client.get_from_change_set_deployment_has_change_set_env_deployment_hmd_lang_deployment(
                csd
            )
        ]:
            env_info = EnvironmentInformation(
                self.rs.ref_to(
                    self.client.get_from_change_set_env_deployment_has_environment_hmd_lang_deployment(
                        csed
                    )[0]
                ),
                client=self.client,
            )

            deployment_dag = build_deployment_dag(
                env_info, repo_instance_deployment_ids=[], destroy=True
            )
            instances = self.destroy_from_instance(
                env_info, deployment_dag, destroy_from
            )
            logger.debug(
                f"{len(instances)} instances to be destroyed in "
                f"{env_info.environment.type}. "
                f"{', '.join(inst.name for inst in instances)}"
            )

            repo_instance_deployment_ids = []
            for repo_instance in instances:
                logger.debug(f"Evaluating instance, {repo_instance.name} for deletion.")
                current_ri_rid = get_current_repo_instance_deployment(
                    repo_instance, self.rs
                )
                logger.debug(
                    f"Instance, {repo_instance.name} has current: "
                    f"{current_ri_rid is not None}"
                )

                if current_ri_rid:
                    current_rid = self.rs.ref_to(current_ri_rid)
                    logger.debug(f"Current deployment status: {current_rid.status}")

                    if current_rid.status in [DEPLOYED, FAILED]:
                        new_deployment = RepoInstanceDeployment(
                            deployment_id=current_rid.deployment_id,
                            status=DESTROY_NEXT,
                        )
                        self.client.upsert(new_deployment)
                        self.client.upsert(
                            RepoInstanceHasRepoInstanceDeployment(
                                ref_from=repo_instance.identifier,
                                ref_to=new_deployment.identifier,
                                current="false",
                            )
                        )
                        # Make sure the new objects are cached
                        self.rs.pull_relationship_nouns(
                            self.client.get_from_repo_instance_has_repo_instance_deployment_hmd_lang_deployment(
                                repo_instance
                            )
                        )
                        new_rel = ChangeSetEnvDeploymentHasRepoInstanceDeployment(
                            ref_from=csed.identifier,
                            ref_to=new_deployment.identifier,
                        )
                        self.client.upsert(new_rel)
                        csed.from_rels[
                            ChangeSetEnvDeploymentHasRepoInstanceDeployment.get_namespace_name()
                        ].append(new_rel)

                        rid_rcvs = self.client.get_from_repo_instance_deployment_has_repo_class_version_hmd_lang_deployment(
                            current_rid
                        )
                        self.client.upsert(
                            RepoInstanceDeploymentHasRepoClassVersion(
                                ref_from=new_deployment.identifier,
                                ref_to=rid_rcvs[0].ref_to,
                            )
                        )
                        # Make sure the new objects are cached
                        self.rs.pull_relationship_nouns(
                            self.client.get_from_repo_instance_deployment_has_repo_class_version_hmd_lang_deployment(
                                new_deployment
                            )
                        )
                        repo_instance_deployment_ids.append(new_deployment.identifier)

            if len(repo_instance_deployment_ids) == 0:
                logger.warning(
                    f"No instances to destroy in environment "
                    f"{env_info.environment.type}"
                )
                csed.csed_status = SKIPPED
                self.client.upsert(csed)
                continue

            # Use the original full-environment DAG (already has all dependencies).
            # The CSED->RID relationships control what gets destroyed, while the
            # DAG provides correct dependency ordering.
            env_infos[env_info.environment.type] = (env_info, csed, deployment_dag)

        return env_infos

    def deploy_destroy_changeset_deployment(
        self,
        csd_nid: str,
        customer_code: str,
        hmd_region: str,
        destroy_from: List[str],
        auth_token: Optional[str] = None,
    ):
        """Complete the async destroy workflow generation and submission.

        This method performs the heavy work that was moved from the sync path:
        - Builds deployment DAG for each environment
        - Identifies instances to destroy based on destroy_from
        - Creates RepoInstanceDeployment records with DESTROY_NEXT status
        - Generates and submits the Argo workflow

        :param csd_nid: ChangeSetDeployment identifier
        :param customer_code: HMD customer code
        :param hmd_region: HMD region
        :param destroy_from: Required list of instance names to destroy from
        :param auth_token: Authorization token for callback
        :raises ValueError: If destroy_from is empty
        """
        if not destroy_from:
            raise ValueError("destroy_from is required and cannot be empty")

        logger.info(
            f"Completing destroy for ChangeSetDeployment, {csd_nid}, "
            f"destroy_from: {destroy_from}"
        )
        csd = self.client.get_change_set_deployment_hmd_lang_deployment(csd_nid)

        try:
            deployment_set = self.rs.ref_to(
                self.client.get_from_change_set_deployment_has_deployment_set_hmd_lang_deployment(
                    csd
                )[0]
            )

            env_infos = self.prepare_destroy_environments(csd, destroy_from)

            if not env_infos:
                logger.warning("No environments have instances to destroy")
                self.update_change_set_deployment_status(csd, COMPLETED)
                return {"csd_nid": csd.identifier, "message": "No instances to destroy"}

            runner_url = _get_runner_url()
            if runner_url:
                submit_results = self.submit_local_workflow(
                    csd, next(iter(env_infos)), runner_url
                )
            else:
                workflow = self.generate_deploy_workflow(
                    csd,
                    None,
                    env_infos,
                    customer_code,
                    hmd_region,
                    deployment_set.name,
                    destroy=True,
                    deployment_lock_name=csd.deployment_lock_name,
                )
                submit_results = self.submit_workfow(workflow, csd)
            submit_results["csd_nid"] = csd.identifier
            return submit_results
        except Exception as e:
            logger.error(
                f"Error during destroy workflow generation: {str(e)}", exc_info=True
            )
            self.update_change_set_deployment_status(csd, FAILED, auth_token=auth_token)
            raise

    def destroy_from_instance(
        self,
        env_info: EnvironmentInformation,
        deployment_dag: DeploymentDag,
        repo_instances: List[str] = None,
    ) -> Set[RepoInstance]:

        if not repo_instances:
            rels = (
                self.client.get_from_environment_has_repo_instance_hmd_lang_deployment(
                    env_info.environment
                )
            )
            self.rs.pull_relationship_nouns(rels)
            instances = set(self.rs.ref_to(env_ri) for env_ri in rels)
        else:
            instances: Set[RepoInstance] = set()
            for repo_instance in repo_instances:
                # Find the instance from which to destroy...
                if not repo_instance in [
                    inst.name for inst in deployment_dag.nodes.keys()
                ]:
                    raise ServiceException(
                        f"RepoInstance with name, {repo_instance}, not found in Environment, {env_info.environment.type}"
                    )

                def get_downstream(node: GraphNode, instances: Set[RepoInstance]):
                    instances.add(node.instance)
                    for node in node.children:
                        get_downstream(node, instances)

                instance = [
                    inst
                    for inst in deployment_dag.nodes.keys()
                    if inst.name == repo_instance
                ][0]
                node = deployment_dag.nodes[instance]
                get_downstream(node, instances)
        return instances

    def destroy_deployment_set_dry_run(
        self,
        deployment_set: DeploymentSet,
        destroy_from: List[str] = None,
    ):
        if destroy_from is None:
            destroy_from = []

        environments = self.client.search_environment_hmd_lang_deployment({})
        result = {}
        for i, env_def in enumerate(deployment_set.definition):
            env_info = EnvironmentInformation(
                environment=[
                    env for env in environments if env.type == env_def["environment"]
                ][0],
                client=self.client,
            )
            deployment_dag = build_deployment_dag(
                env_info, repo_instance_deployment_ids=[], destroy=True
            )
            instances = self.destroy_from_instance(
                env_info, deployment_dag, destroy_from
            )
            result[env_info.environment.type] = sorted(
                [inst.name for inst in instances]
            )
        return result

    def apply_changeset_to_environment(
        self,
        env_info: EnvironmentInformation,
        change_set: ChangeSet,
        change_set_deployment: ChangeSetDeployment,
        order: int,
    ) -> ChangeSetEnvDeployment:
        csed = ChangeSetEnvDeployment(
            deploy_time=datetime.datetime.now(datetime.timezone.utc), csed_status=CREATED
        )
        csed = self.client.upsert(csed)

        csd_csed = ChangeSetDeploymentHasChangeSetEnvDeployment(
            ref_from=change_set_deployment.identifier,
            ref_to=csed.identifier,
            order=order,
        )
        self.client.upsert(csd_csed)

        csed_env = ChangeSetEnvDeploymentHasEnvironment(
            ref_from=csed.identifier, ref_to=env_info.environment.identifier
        )
        self.client.upsert(csed_env)

        for change in change_set.definition:
            logger.info(
                f"Adding repo instance: {change['repo_instance_name']}:"
                f"{change['repo_class_name']}, {change['repo_class_version']}"
            )
            ri, rid = env_info.add_repo_instance(
                name=change["repo_instance_name"],
                deployment_id=change["deployment_id"],
                instance_configuration=change.get("instance_configuration"),
                repo_class_version=self.class_info.get_repo_class_version(
                    change["repo_class_name"], change["repo_class_version"]
                ),
                config_spec=change.get("config_spec"),
                hmd_region=change.get("hmd_region"),
                dependencies=env_info.instance_names_to_refs(
                    change.get("dependencies")
                ),
                image_only=change.get("image_only", False),
                auto_deploy=change.get("auto_deploy", "false"),
            )

            new_rel = ChangeSetEnvDeploymentHasRepoInstanceDeployment(
                ref_from=csed.identifier, ref_to=rid.identifier
            )
            new_rel = self.client.upsert(new_rel)
            csed.from_rels[
                ChangeSetEnvDeploymentHasRepoInstanceDeployment.get_namespace_name()
            ].append(new_rel)
        return csed

    def update_change_set_env_deployment_status(
        self, change_set_deployment: ChangeSetEnvDeployment, status: str
    ):
        if status not in CSED_STATUS_OPTIONS:
            raise ServiceException(
                f"status must be one of {', '.join(CSED_STATUS_OPTIONS)}; was {status}"
            )
        change_set_deployment.csed_status = status
        self.client.upsert(change_set_deployment)

    def update_change_set_deployment_status(
        self, change_set_deployment: ChangeSetDeployment, status: str, auth_token:str = None
    ):
        if status not in CSD_STATUS_OPTIONS:
            raise ServiceException(
                f"status must be one of {', '.join(CSD_STATUS_OPTIONS)}; was {status}"
            )
        change_set_deployment.csd_status = status
        self.client.upsert(change_set_deployment)

        if status == FAILED:
            rels = self.client.get_from_change_set_deployment_has_change_set_env_deployment_hmd_lang_deployment(
                change_set_deployment
            )
            self.rs.pull_relationship_nouns(rels)
            for csed in [self.rs.ref_to(rel) for rel in rels]:
                if csed.csed_status != COMPLETED:
                    # if it's been started, then it must have failed, otherwise we didn't get to it...
                    if csed.csed_status == STARTED:
                        csed.csed_status = FAILED
                    else:
                        csed.csed_status = SKIPPED
                    self.client.upsert(csed)
                    csed_rids = self.client.get_from_change_set_env_deployment_has_repo_instance_deployment_hmd_lang_deployment(
                        csed
                    )
                    self.rs.pull_relationship_nouns(csed_rids)
                    for rid in [self.rs.ref_to(rel) for rel in csed_rids]:
                        if rid.status in [DEPLOY_NEXT, DESTROY_NEXT]:
                            mark_rid_status(self.client, rid, SKIPPED)
        
        if change_set_deployment.callback_url:
            try:
                requests.post(change_set_deployment.callback_url, json={"status": change_set_deployment.csd_status, "csd_id": change_set_deployment.identifier}, headers={"Authorization": auth_token})
            except Exception as e:
                logger.warning(f"Callback failed with {e}")
                pass


    def generate_deploy_workflow(
        self,
        csd: ChangeSetDeployment,
        change_set: Optional[ChangeSet],
        env_infos: Dict[
            str, Tuple[EnvironmentInformation, ChangeSetEnvDeployment, DeploymentDag]
        ],
        customer_code: str,
        hmd_region: str,
        deployment_set_name: str,
        destroy: bool = False,
        deployment_lock_name: Optional[str] = None,
    ):
        workflow_creator = DeployWorkflowCreator(self.client)
        return workflow_creator.create_deploy_workflow(
            csd,
            change_set,
            env_infos,
            customer_code,
            hmd_region,
            deployment_set_name,
            destroy,
            None,
            deployment_lock_name=deployment_lock_name,
        )

    def submit_local_workflow(
        self,
        csd: ChangeSetDeployment,
        environment_type: str,
        runner_url: str,
    ):
        """Submit a changeset to a local DAG runner.

        Deliberately the same shape as :meth:`submit_workfow`: the same route,
        the same reply, and the same ``workflow_name`` recorded on the
        ChangeSetDeployment. What differs is the payload, because there is no
        argoproj.io CRD locally -- the runner takes the node manifest
        ``generate_local_deploy_manifest`` already produces, which is the same
        manifest the CLI used to fetch and execute itself.
        """
        manifest = generate_local_deploy_manifest(self.client, self.rs, csd.identifier)
        manifest["csd_nid"] = csd.identifier
        manifest["environment"] = environment_type

        url = f"{runner_url.rstrip('/')}/api/v1/workflows/{environment_type}"
        logger.debug(f"Workflow runner url: {url}")

        # No Authorization header. Argo's token exists because its server is
        # reachable across a cluster; the local runner publishes no host port
        # and is reachable only from the platform network, where every other
        # control-plane service already sits.
        result = requests.post(
            url,
            json={"namespace": environment_type, "workflow": manifest},
        )
        if result.status_code != 200:
            raise Exception(
                f"Error submitting workflow to the runner at {url}: {result.text}"
            )

        workflow_name = result.json().get("metadata", {}).get("name", "<unknown>")

        if workflow_name != "<unknown>":
            csd.workflow_name = workflow_name
            csd.workflow_submitted_at = datetime.datetime.now(datetime.timezone.utc)
            self.client.upsert(csd)
            logger.info(
                f"Stored workflow_name '{workflow_name}' in ChangeSetDeployment {csd.identifier}"
            )

        return {"workflow_name": workflow_name}

    def submit_workfow(self, workflow, csd: ChangeSetDeployment = None):
        payload = {"namespace": "argo-aaa", "serverDryRun": False, "workflow": workflow}
        url = f"{_get_argo_url()}/api/v1/workflows/{os.environ['ARGO_INSTANCE_NAME']}-{os.environ['ARGO_DEPLOYMENT_ID']}"

        logger.debug(f"Argo url: {url}")

        sa_token_secret_name = f"{os.environ.get('HMD_INSTANCE_NAME')}-{os.environ.get('HMD_REPO_NAME')}-{os.environ.get('HMD_DID')}-sa-token"
        result = requests.post(
            url,
            json=payload,
            headers={
                "Authorization": f"Bearer {get_secret(get_session(), sa_token_secret_name)}"
            },
        )
        if result.status_code != 200:
            raise Exception(f"Error submitting workflow to argo: {result.json()}")

        result_data = result.json()
        workflow_name = result_data.get("metadata", {}).get("name", "<unknown>")

        # Store workflow_name in ChangeSetDeployment for log retrieval
        if csd is not None and workflow_name != "<unknown>":
            csd.workflow_name = workflow_name
            csd.workflow_submitted_at = datetime.datetime.now(datetime.timezone.utc)
            self.client.upsert(csd)
            logger.info(f"Stored workflow_name '{workflow_name}' in ChangeSetDeployment {csd.identifier}")

        return {"workflow_name": workflow_name}
