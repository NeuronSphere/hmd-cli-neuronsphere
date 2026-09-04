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
* **The dependency order is explicit**, and reads the way the cloud's does.

Not yet true: the control plane does **not** appear in its own graph. Bootstrap
nodes carry locally generated ids (:func:`_rid`) that no ms-deployment entity
corresponds to, so the buffered status updates and Resource submissions 404 on
replay and record nothing. Making them real needs a control-plane BOM seeded and
applied the way an environment's is, so the entities exist to report against.

Nodes that provision what the deployment service itself depends on cannot be
deployed *through* that service, so they carry a ``handler`` (see
``LocalWorkflowRunner._execute_node``) and run in-process instead of in a
projectbuilder container. That is a property of the executor, not of the DAG's
shape: moving such a node onto the projectbuilder path later is a per-node
change here, and the ordering it participates in is already correct.
"""

import json
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

# The DNS alias the CLI gives the control-plane RDS container on the Docker
# network (environments._CANONICAL_DB_HOST). Consumers address the database by
# this name on 5432, never through Floci's 7001-7099 proxy range.
CONTROL_PLANE_DB_HOST = "hmd_db"

# The instance and repo class that provision the control plane's graph.
# `hmd-ms-artifact-lib` declares a *required* `neptune-db` dependency (its
# `hmd_db_engines` has no `postgres` engine at all -- persistence is
# `["dynamo", "graph"]`), so unlike an environment's lazily-provisioned graph
# this one is not optional and is deployed unconditionally, the same way
# CONTROL_PLANE_DB_INSTANCE is.
CONTROL_PLANE_GRAPH_INSTANCE = "control-plane-graph"
CONTROL_PLANE_GRAPH_REPO_CLASS = "hmd-inf-neptune"

# The DNS alias the CLI gives the control-plane graph container. Bare
# `global-graph`, not `global-graph-<slug>`: it matches what the legacy default
# environment (whose Floci account *is* the control plane -- see
# `env_registry._legacy_environment`) already expects at this name, and what
# `LocalPluginLoader.get_hmdms_lambda_spec`'s gremlin-engine resolution already
# falls back to for a control-plane (``env=None``) deploy.
CONTROL_PLANE_GRAPH_HOST = "global-graph"


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


def _repo_version(repo_class_name: str) -> str:
    """The version the deploy is invoked with, from the repo's own VERSION file.

    ms-deployment resolves this from the registered RepoClassVersion; during
    bootstrap there is no registry to ask, so it comes from the working tree
    that is about to be mounted and deployed.
    """
    from .bom_seeder import _get_repo_version

    try:
        return _get_repo_version(repo_class_name)
    except Exception as e:  # a version we cannot resolve must not abort `up`
        logger.warning(f"Could not resolve a version for {repo_class_name}: {e}")
        return "0.1"


def postgres_instance_config() -> Dict:
    """Configuration for the control-plane Postgres deploy.

    Must be passed explicitly and cannot be left to the manifest: the cloud
    ``default_configuration`` describes Aurora (``db_engine:
    aurora-postgresql``, ``engine_version: 17.9``, ``instance_type:
    db.r7g.large``) and would otherwise override the local overlay's defaults
    with values a plain ``aws_db_instance`` cannot use.

    ``engine_version`` is read from the image Floci will actually spawn rather
    than hardcoded, so the declared version and the binary that initialises the
    data directory cannot drift apart -- the drift ``pg_upgrade`` exists to
    catch.
    """
    from .floci_deployer import LOCAL_DB_SUBNET_GROUP
    from .pg_upgrade import configured_postgres_image, image_pg_major

    config = {
        # Explicit placement: Floci's implicit "default" subnet group is unusable
        # for any account but the first to touch EC2 in a region.
        "db_subnet_group_name": LOCAL_DB_SUBNET_GROUP,
        # Address the backend container directly rather than Floci's RDS proxy,
        # which does not survive a Floci restart. `ensure_rds_network_alias`
        # creates this name; recording it in the admin secret is what lets
        # ms-dbaccount reconnect after a restart.
        "db_host": CONTROL_PLANE_DB_HOST,
        "db_port": 5432,
        "db_username": "postgres",
        # Matches what hmd-postgres-base bakes in (ENV POSTGRES_PASSWORD).
        "db_password": "admin",
        "instance_type": "db.t3.micro",
        "allocated_storage": 20,
    }
    major = image_pg_major(configured_postgres_image())
    if major:
        config["engine_version"] = major
    return config


def graph_instance_config() -> Dict:
    """Configuration for the control-plane graph deploy.

    Mirrors ``bom_seeder.graph_bom_entry``'s per-environment shape:
    ``graph_host``/``graph_port`` tell ``hmd-inf-neptune``'s local overlay
    (``deploy_local.sh``) which alias the CLI will attach once this node
    succeeds -- never Floci's own Gremlin proxy, which is not restored after a
    Floci restart.
    """
    return {
        "graph_host": CONTROL_PLANE_GRAPH_HOST,
        "graph_port": 8182,
    }


def deploy_script(
    repo_class_name: str,
    instance_name: str,
    repo_version: str,
    *,
    environment: str = "local",
    deployment_id: str = CONTROL_PLANE_DEPLOYMENT_ID,
    config: Optional[Dict] = None,
) -> str:
    """The deploy command a projectbuilder node runs.

    Mirrors what ms-deployment's ``deploy_base.deploy_node`` generates: the repo
    identity is carried by *global* ``hmd`` flags ahead of the ``deploy``
    subcommand, and the resolved instance configuration arrives on stdin as a
    heredoc. There is no per-tool positional -- ``hmd deploy`` reads
    ``manifest.json``'s ``deploy.commands`` to know this is a cdktf repo.

    ``LocalWorkflowRunner._localize_deploy_script`` then inserts ``--local``
    directly after ``deploy``, so the deploy takes its code from the mounted
    workspace rather than an Artifact Librarian that does not exist locally.

    Two deliberate omissions from the generated form, both because
    ms-deployment does not exist yet while this runs:

    * no ``--register``, which would have the deploy report completion to it;
    * no ``HMD_REPO_INSTANCE_DEPLOYMENT_ID`` export, which newer hmd-cli-deploy
      reads as the default for ``--repo-instance-deployment-id`` and uses to
      submit produced Resources.

    The runner records both itself, buffered until the replay.

    The heredoc delimiter is quoted (``<<'EOF'``) so the shell performs no
    parameter or backslash expansion on the JSON body.
    """
    region = os.environ.get("HMD_REGION", "reg1")
    body = json.dumps(config or {}, indent=2)
    return (
        f"hmd --debug --repo-name {repo_class_name} --repo-version {repo_version} "
        f"--hmd-region {region} deploy "
        f"--instance-name {instance_name} --environment {environment} "
        f"--deployment-id {deployment_id} --config-file STDIN <<'EOF'\n"
        f"{body}\n"
        f"EOF"
    )


def control_plane_nodes(
    *,
    ensure_databases: Callable,
    ensure_graph: Callable,
    deploy_naming: Callable,
    deploy_artifact_lib: Callable,
    deploy_ms_deployment: Callable,
    postgres_config: Optional[Dict] = None,
    graph_config: Optional[Dict] = None,
) -> List[Dict]:
    """The ordered control-plane DAG.

    ``hmd-postgres-rds`` and ``hmd-inf-neptune`` both run first, in real
    projectbuilder containers: they are ordinary infrastructure with no
    dependency on the deployment service, so there is no reason for either to
    be anything other than a normal deploy. Everything after them needs the
    database (``hmd-ms-artifact-lib`` needs the graph too), and ms-deployment
    is last so that every node ahead of it is recorded on replay.

    The five callables are passed in rather than imported so this module stays
    free of the control-plane lifecycle it describes -- and so a test can build
    the DAG without deploying anything.
    """
    config = dict(postgres_config or postgres_instance_config())
    version = _repo_version(CONTROL_PLANE_DB_REPO_CLASS)
    postgres = node(
        CONTROL_PLANE_DB_INSTANCE,
        CONTROL_PLANE_DB_REPO_CLASS,
        repo_class_version=version,
        script=deploy_script(
            CONTROL_PLANE_DB_REPO_CLASS,
            CONTROL_PLANE_DB_INSTANCE,
            version,
            config=config,
        ),
    )
    postgres["instance_configuration"] = config

    graph_cfg = dict(graph_config or graph_instance_config())
    graph_version = _repo_version(CONTROL_PLANE_GRAPH_REPO_CLASS)
    graph = node(
        CONTROL_PLANE_GRAPH_INSTANCE,
        CONTROL_PLANE_GRAPH_REPO_CLASS,
        repo_class_version=graph_version,
        script=deploy_script(
            CONTROL_PLANE_GRAPH_REPO_CLASS,
            CONTROL_PLANE_GRAPH_INSTANCE,
            graph_version,
            config=graph_cfg,
        ),
    )
    graph["instance_configuration"] = graph_cfg

    return [
        postgres,
        # Created by direct psql rather than by ms-dbaccount: dbaccount is
        # per-environment (matching the cloud), so the control plane has none,
        # and ms-deployment's own database has to exist before ms-deployment can.
        node("core-databases", CONTROL_PLANE_DB_REPO_CLASS, handler=ensure_databases),
        graph,
        # Aliases the container the node above just had Floci spawn -- a
        # separate node for the same reason core-databases is: the alias is a
        # CLI-side Docker network operation, not something the projectbuilder
        # deploy can do from inside its own container.
        node(
            "control-plane-graph-alias",
            CONTROL_PLANE_GRAPH_REPO_CLASS,
            handler=ensure_graph,
        ),
        node("hmd_ms_naming", "hmd-ms-naming", handler=deploy_naming),
        # After the graph: artifact-lib's `neptune-db` dependency is required,
        # and `_deploy_artifact_lib` resolves it to `CONTROL_PLANE_GRAPH_HOST`,
        # which only resolves once the alias node above has run.
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


def control_plane_graph_identifier(target) -> str:
    """The DBClusterIdentifier the control-plane graph node creates.

    Must match what ``hmd-inf-neptune``'s ``deploy_local.sh`` derives (the same
    ``make_standard_name`` shape as :func:`control_plane_db_identifier`), and
    what :func:`bom_seeder.graph_cluster_identifier` derives for a
    per-environment graph, so the CLI looks the cluster up by this id to find
    its container and alias it as ``CONTROL_PLANE_GRAPH_HOST``.
    """
    from hmd_cli_tools.hmd_cli_tools import make_standard_name

    from .floci_deployer import local_customer_code

    base = make_standard_name(
        CONTROL_PLANE_GRAPH_INSTANCE,
        CONTROL_PLANE_GRAPH_REPO_CLASS,
        CONTROL_PLANE_DEPLOYMENT_ID,
        "local",
        os.environ.get("HMD_REGION", "reg1"),
        local_customer_code(),
    )
    return base.replace("_", "-").lower()


def csd_nid() -> str:
    """ChangeSetDeployment id for a bootstrap run."""
    return f"csd-bootstrap-{uuid.uuid4().hex[:8]}"
