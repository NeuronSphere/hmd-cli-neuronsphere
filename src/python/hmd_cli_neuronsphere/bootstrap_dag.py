"""The control-plane bootstrap DAG.

The control plane is brought up by a deployment DAG that runs *before*
``hmd-ms-deployment`` exists -- ms-deployment is its last node. That is possible
because :class:`local_workflow_runner.LocalWorkflowRunner` takes a plain ordered
list of node dicts and never asks the deployment service for them; its only
coupling to that service is three callbacks, which ``tracking=False`` buffers and
:meth:`~local_workflow_runner.LocalWorkflowRunner.replay_into` flushes once the
service is serving.

Why a DAG rather than a straight-line sequence of imperative calls:

* **The real RepoClasses do the work.** The control-plane Postgres is deployed by
  ``hmd-postgres-rds`` -- the same repo class the cloud uses -- through the same
  projectbuilder path every other deploy takes. Its produced
  ``database.neuronsphere.io/postgres`` Resource is a real one, read from
  ``meta-data/resources_output/``, rather than a record hand-seeded by
  ``bom_seeder`` to describe a container compose happened to start.
* **The control plane appears in its own graph.** After the replay, ms-deployment
  reports the instances that bootstrapped it as ``DEPLOYED``, with their
  Resources attached.
* **The dependency order is explicit** and reads the way the cloud's does:
  everything ms-deployment needs is ahead of it.

Nodes that provision what the deployment service itself depends on cannot be
deployed *through* that service, so they carry a ``handler`` (see
``LocalWorkflowRunner._execute_node``) and run in-process instead of in a
projectbuilder container. That is a property of the executor, not of the DAG's
shape: moving such a node onto the projectbuilder path later is a per-node
change here, and the ordering it participates in is already correct.
"""

import os
import uuid
from typing import Callable, Dict, List, Optional

from cement import minimal_logger

logger = minimal_logger("bootstrap_dag")

# The instance and repo class that provision the control plane's Postgres. The
# instance name is what `make_standard_name` folds into the admin DB secret, so
# consumers deriving that name independently must agree with it.
CONTROL_PLANE_DB_INSTANCE = "control-plane-db"
CONTROL_PLANE_DB_REPO_CLASS = "hmd-postgres-rds"

# Deployment id for control-plane instances. The control plane is not one of the
# named environments, and `local` is the default environment's id, so it needs an
# id of its own to keep their standard names distinct.
CONTROL_PLANE_DEPLOYMENT_ID = "cp"


def _rid(instance_name: str) -> str:
    """A RepoInstanceDeployment id for a bootstrap node.

    Generated rather than allocated by ms-deployment, which does not exist yet.
    It only has to be stable across the buffered status update and the Resource
    submission that references it within one run.
    """
    return f"rid-bootstrap-{instance_name}-{uuid.uuid4().hex[:8]}"


def node(
    instance_name: str,
    repo_class_name: str,
    *,
    handler: Optional[Callable] = None,
    script: str = "",
    repo_class_version: str = "",
) -> Dict:
    """One node in the shape ``LocalWorkflowRunner.run`` consumes."""
    entry: Dict = {
        "instance_name": instance_name,
        "repo_class_name": repo_class_name,
        "rid_nid": _rid(instance_name),
        "deployment_id": CONTROL_PLANE_DEPLOYMENT_ID,
    }
    if handler is not None:
        entry["handler"] = handler
    if script:
        entry["script"] = script
    if repo_class_version:
        entry["repo_class_version"] = repo_class_version
    return entry


def deploy_script(repo_class_name: str) -> str:
    """The deploy command a projectbuilder node runs.

    Mirrors what ms-deployment's ``deploy_base.deploy_node`` generates for a
    cdktf repo; ``LocalWorkflowRunner._localize_deploy_script`` then inserts
    ``--local`` so the deploy takes its code from the mounted workspace rather
    than an Artifact Librarian that does not exist locally.
    """
    return f"hmd deploy --instance-name {CONTROL_PLANE_DB_INSTANCE} cdktf"


def control_plane_nodes(
    *,
    ensure_databases: Callable,
    deploy_naming: Callable,
    deploy_artifact_lib: Callable,
    deploy_ms_deployment: Callable,
    postgres_config: Optional[Dict] = None,
) -> List[Dict]:
    """The ordered control-plane DAG.

    ``hmd-postgres-rds`` runs first and in a real projectbuilder container: it is
    ordinary infrastructure with no dependency on the deployment service, so
    there is no reason for it to be anything other than a normal deploy.
    Everything after it needs that database, and ms-deployment is last so that
    every node ahead of it is recorded on replay.

    The four callables are passed in rather than imported so this module stays
    free of the control-plane lifecycle it describes -- and so a test can build
    the DAG without deploying anything.
    """
    postgres = node(
        CONTROL_PLANE_DB_INSTANCE,
        CONTROL_PLANE_DB_REPO_CLASS,
        script=deploy_script(CONTROL_PLANE_DB_REPO_CLASS),
    )
    if postgres_config:
        postgres["instance_configuration"] = dict(postgres_config)

    return [
        postgres,
        # Created by direct psql rather than by ms-dbaccount: dbaccount is
        # per-environment (matching the cloud), so the control plane has none,
        # and ms-deployment's own database has to exist before ms-deployment can.
        node("core-databases", CONTROL_PLANE_DB_REPO_CLASS, handler=ensure_databases),
        node("hmd_ms_naming", "hmd-ms-naming", handler=deploy_naming),
        node("hmd_ms_artifact_lib", "hmd-ms-artifact-lib", handler=deploy_artifact_lib),
        # Last, deliberately: everything above is what it needs to run.
        node("hmd_ms_deployment", "hmd-ms-deployment", handler=deploy_ms_deployment),
    ]


def control_plane_db_identifier(target) -> str:
    """The DBInstanceIdentifier the control-plane Postgres node creates.

    Must match what the CDKTF overlay derives from ``HmdCdkTfStack.base_name``
    (``make_standard_name`` lowercased with hyphens), because the CLI looks the
    instance up by this id to find its container and alias it as ``hmd_db``.
    """
    from hmd_cli_tools.hmd_cli_tools import make_standard_name

    from .floci_deployer import local_customer_code

    base = make_standard_name(
        CONTROL_PLANE_DB_INSTANCE,
        CONTROL_PLANE_DB_REPO_CLASS,
        CONTROL_PLANE_DEPLOYMENT_ID,
        "local",
        os.environ.get("HMD_REGION", "reg1"),
        local_customer_code(),
    )
    return base.replace("_", "-").lower()


def csd_nid() -> str:
    """ChangeSetDeployment id for a bootstrap run."""
    return f"csd-bootstrap-{uuid.uuid4().hex[:8]}"
