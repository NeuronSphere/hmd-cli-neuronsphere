.. NERD001 Floci-Based Local NeuronSphere Architecture

NERD001 Floci-Based Local NeuronSphere Architecture
====================================================

.. req:: Close the local/cloud deployment gap using Floci
    :id: HMD_CLI_NEURONSPHERE_NERD001
    :status: proposed

    The local NeuronSphere development environment should achieve near-parity with
    cloud deployments by adopting Floci as the AWS emulation layer and providing
    two operating modes that serve distinct user classes. **Platform mode**
    (default) provides end users with a cloud-identical experience: services run as
    Floci Lambdas, ``image_sequence`` transforms execute on Argo Workflows (k3s),
    and all user-facing APIs match the cloud. **Extend mode** adds
    ``hmd-ms-deployment`` for NeuronSphere engineers who need ``DeploymentConfig``
    to validate CDKTF/Helm infrastructure code. Both modes derive their behavior
    from the same nsplugin definitions. The ``hmd neuronsphere up`` command remains
    the single entrypoint.

    .. note::

       **Amended 2026-09-25.** ``nsplugin.json`` was withdrawn with
       NERD002 SPEC005, so every specification below that
       derives behaviour from a plugin definition names a file ``nsctl`` does
       not read. A RepoClass declares what it produces and consumes in its
       BACON ``meta-data/manifest.json`` and its ``meta-data/resources/*.yaml``,
       and ``internal/repoclass`` reads exactly those; an extension runs
       because a manifest names it and for no other reason
       (NERD009). The ``nsplugin.json`` format
       itself survives only in the legacy Python ``hmd neuronsphere`` CLI,
       which still loads and validates it.

       Extend mode is also the default now, not Platform mode. The text is
       kept as written as the record of what was proposed.

Motivation
----------

NeuronSphere deploys to AWS using Argo Workflows on Kubernetes, with
``hmd-ms-deployment`` orchestrating a dependency DAG of RepoInstances. Each step
in the Argo workflow runs the ``hmd-img-projectbuilder`` image, executing
``hmd deploy`` commands that download build archives from ``hmd-ms-artifact-lib``,
run Terraform/CDKTF/Helm/Docker, and report status back. Locally,
``hmd neuronsphere up`` uses flat Docker Compose files and MiniStack for AWS
emulation. This creates several gaps:

- **No Argo Workflows for transforms.** ``image_sequence`` transforms run on Argo
  in the cloud but are rendered as Airflow DockerOperator DAGs locally. Users
  cannot verify container specs, resource requests, or inter-task dependencies
  until they push to the cloud.
- **No local deployment service.** ``hmd-ms-deployment`` does not run locally.
  Engineers building new RepoClasses cannot generate a ``DeploymentConfig`` to
  render their CDKTF stacks or Helm charts.
- **Separate code paths.** CLI tools often branch on ``HMD_ENVIRONMENT == "local"``
  with entirely different logic rather than targeting the same AWS-compatible
  APIs.
- **MiniStack limitations.** MiniStack provides S3, DynamoDB, SQS, Lambda, and
  API Gateway, but lacks Step Functions, Secrets Manager, IAM policy evaluation,
  CloudFormation, and ECS/EKS emulation.

User Classes
------------

The local NeuronSphere serves two distinct classes of users with different needs.
When tradeoffs arise, the primary class (end users) is favored.

**Class 1: NeuronSphere End Users (Primary)**

Data analysts, data scientists, and data engineers who use the NeuronSphere
platform to build and maintain data pipelines and assets. They develop Airflow
DAGs, SQL queries (Trino), Superset dashboards, and NeuronSphere Transforms
(``provider``, ``image_sequence``, and ``dbt`` types). They need the local
platform to behave identically to the cloud:

- Same APIs for deploying transforms and services
- Same execution engines (Argo for ``image_sequence``, Airflow for
  ``provider``/``dbt``)
- Services running as Floci Lambdas behind API Gateway, same as cloud
- Data stored in Floci S3, queryable via Trino

They do **not** need to understand CDKTF, Helm, RepoClasses, or infrastructure
deployment internals.

**Class 2: NeuronSphere Engineers (Secondary)**

NeuronSphere employees and advanced users building new platform features. They
write new RepoClasses to extend the platform -- typically new applications
deployed to EKS via Helm or new semantic microservices deployed as Lambdas via
CDKTF. They need to validate that infrastructure code will work in the cloud:

- Render CDKTF output (``cdktf synth``) to inspect generated Terraform JSON
- Render Helm templates (``helm template``) to inspect generated Kubernetes
  manifests
- Both require a ``DeploymentConfig`` from ``hmd-ms-deployment`` to resolve
  dependency configurations (host names, ARNs, ports from upstream repos)

They do **not** need the full 28-node platform deployment DAG executing every
startup. They register their specific repo, get a config, and validate locally.

Floci Evaluation
----------------

`Floci <https://github.com/floci-io/floci>`_ is a free, MIT-licensed AWS
emulator released in March 2026 as a response to LocalStack sunsetting its
community edition. It is built on Java/Quarkus compiled to a GraalVM native
binary.

**Key capabilities relevant to NeuronSphere:**

.. list-table::
   :header-rows: 1
   :widths: 30 70

   * - Capability
     - Details
   * - AWS Services
     - 34 services: S3, DynamoDB, SQS, SNS, Lambda, API Gateway (v1 & v2),
       Step Functions, ECS, EKS (k3s), RDS (Postgres/MySQL), IAM (real policy
       eval), Cognito, KMS, Secrets Manager, CloudWatch, CloudFormation,
       EventBridge, Kinesis, Glue Data Catalog, AppConfig
   * - Performance
     - 24 ms startup, 13 MiB idle memory, ~90 MB Docker image
   * - Lambda
     - Real Docker execution with warm pools, container image support, event
       source mappings (SQS, DynamoDB Streams)
   * - ECS
     - Real Docker container orchestration with task definitions and lifecycle
   * - EKS
     - Mock mode (metadata only) or real k3s clusters
   * - Step Functions
     - 18 operations, full state machine support with Pass, Task, Choice, Wait,
       Parallel, Map states
   * - Secrets Manager
     - Full CRUD for secrets, rotation stubs
   * - Wire protocol
     - Standard AWS SDK compatible on port 4566 (same as MiniStack/LocalStack)

**Comparison to MiniStack:**

.. list-table::
   :header-rows: 1
   :widths: 20 40 40

   * - Dimension
     - Floci
     - MiniStack
   * - Services
     - 34 (Lambda, RDS, ECS, EKS, Step Functions, IAM, Cognito, KMS, Secrets
       Manager, CloudFormation, etc.)
     - ~10 (S3, DynamoDB, SQS, Lambda, API Gateway, SNS)
   * - License
     - MIT, no auth required
     - MIT, no auth required
   * - Startup
     - 24 ms
     - Comparable
   * - Port
     - 4566
     - 4566
   * - Lambda
     - Real Docker containers with warm pools
     - Real Docker containers
   * - Maturity
     - New (March 2026), ~1 month old
     - Slightly more mature

**Risk assessment:** Floci is very new. Phase 0 of the migration uses only the
same services MiniStack already provides (S3, DynamoDB, SQS, Lambda, API
Gateway), so risk is contained. Advanced services (EKS/k3s, Secrets Manager,
IAM) are introduced in later phases after validation.

Architecture Overview
---------------------

The local NeuronSphere is structured in two layers. Platform mode (Layer 1)
provides everything end users need. Extend mode (Layer 2) adds
``hmd-ms-deployment`` for engineers.

**The key insight:** The simplification is in how *platform infrastructure*
starts (Docker Compose for data services, Floci Lambdas for microservices, k3s
for Argo), NOT in how *users interact* with the platform. User-facing workflows
match the cloud exactly.

.. uml::

    @startuml
    !theme plain
    skinparam componentStyle rectangle
    skinparam defaultFontSize 11
    skinparam packageStyle frame
    skinparam linetype ortho

    title Platform Mode vs Extend Mode Architecture

    together {
        package "Platform Mode (Default)" as platform {
            component "hmd neuronsphere up" as cli

            package "Core Infrastructure\n(Docker Compose)" as core {
                component "Floci :4566\nS3, DynamoDB, SQS,\nLambda, API GW,\nSecrets Manager,\nEKS (k3s)" as floci
                component "PostgreSQL\n(hmd_db)" as pg
                component "nginx proxy\n:80" as nginx
            }

            package "Microservices\n(Floci Lambdas)" as lambdas {
                component "ms-naming\n(service discovery)" as naming
                component "ms-transform\n(transform engine)" as transform
                component "User services\n(hmd neuronsphere run)" as user_svc
            }

            package "Execution Engines\n(nsplugins)" as engines {
                component "Airflow\n(Docker Compose)\nprovider + dbt" as airflow
                component "Argo Workflows\n(k3s on Floci EKS)\nimage_sequence" as argo
            }

            package "Data Services\n(nsplugins)" as data {
                component "Trino :8081\nHive Metastore\nSuperset :8088\nClickHouse\nJanusGraph\nOTel" as data_svc
            }

            cli -down-> core
            lambdas -up-> floci : deployed as\nLambda functions
            transform -right-> argo : image_sequence
            transform -right-> airflow : provider/dbt
            nginx -down-> lambdas : proxies API\nGateway routes
            nginx -down-> argo : /argo/ UI
        }

        package "Extend Mode (Adds)" as extend {
            component "ms-deployment\n(Floci Lambda)\nDeploymentConfig API" as ms_deploy

            component "Engineer CLI\nhmd neuronsphere register-repo\nhmd neuronsphere get-config\nhmd neuronsphere deploy-repo" as eng_cli

            eng_cli -down-> ms_deploy
        }
    }
    @enduml

**Two operating modes, one entrypoint:**

``hmd neuronsphere up`` checks ``HMD_LOCAL_NEURONSPHERE_MODE``:

- **Platform** (default, ``HMD_LOCAL_NEURONSPHERE_MODE`` unset, ``platform``, or
  ``legacy`` for backwards compatibility): Starts core infrastructure via Docker
  Compose, deploys microservices as Floci Lambdas, creates a k3s cluster via
  Floci EKS for Argo Workflows, and starts all enabled nsplugins. End users
  interact with the platform through the same APIs as the cloud.
- **Extend** (``HMD_LOCAL_NEURONSPHERE_MODE=extend`` or ``deploy`` for backwards
  compatibility): Everything Platform mode starts, plus ``hmd-ms-deployment``
  as an additional Floci Lambda. Engineers use this to register RepoClasses,
  generate ``DeploymentConfig``, and validate infrastructure code.

Platform Mode: Cloud-Parity User Experience
--------------------------------------------

Platform mode is the primary and recommended mode. It is not a stepping stone
or legacy fallback -- it is the product. The goal is that every user-facing
interaction works identically to the cloud.

**What starts:**

1. **Core infrastructure** (Docker Compose + Floci):

   - Floci (port 4566): S3, DynamoDB, SQS, Lambda, API Gateway, Secrets Manager,
     EKS with k3s cluster auto-created
   - PostgreSQL (``hmd_db``): Shared database
   - nginx proxy (port 80): Routes to Floci API Gateways and Argo UI

2. **Microservices** (Floci Lambdas):

   - ``ms-naming``: Service discovery. Deployed as Lambda behind API Gateway.
   - ``ms-transform``: Transform engine. Deployed as Lambda behind API Gateway.
     Configured with ``TF_ENGINES=["argo", "airflow"]`` so ``image_sequence``
     routes to Argo and ``provider``/``dbt`` route to Airflow.
   - User services: Deployed via ``hmd neuronsphere run`` as Floci Lambdas.

3. **Execution engines** (nsplugins):

   - Airflow (``hmd-app-airflow`` plugin): Scheduler, webserver, triggerer via
     Docker Compose. Handles ``provider`` and ``dbt`` transforms.
   - Argo Workflows (``hmd-app-argo`` plugin): Installed on k3s via Helm.
     Handles ``image_sequence`` transforms. Argo UI exposed via nginx at
     ``/argo/``.

4. **Data services** (nsplugins): Trino, Superset, ClickHouse, JanusGraph, OTel,
   Hive Metastore -- each as an nsplugin with its own ``nsplugin.json``.

5. **AWS resources** (Floci): S3 buckets, SQS queues, DynamoDB tables provisioned
   from plugin resource declarations.

**What does NOT start in Platform mode:**

- ``hmd-ms-deployment``: Not needed by end users. No BOM seeding, no deployment
  DAG, no projectbuilder containers. Infrastructure is handled by Docker Compose
  and nsplugins, not by a deployment pipeline.

Argo Workflows on Local EKS (k3s)
----------------------------------

.. spec:: Argo Workflows as an nsplugin on Floci EKS (k3s)
    :id: HMD_CLI_NEURONSPHERE_NERD001_SPEC_ARGO
    :links: HMD_CLI_NEURONSPHERE_NERD001
    :status: proposed

    In the cloud, ``image_sequence`` transforms execute on Argo Workflows on EKS.
    Locally, the Argo plugin (``hmd-app-argo``) deploys Argo Workflows to a k3s
    cluster running on Floci's EKS service, providing identical execution for
    ``image_sequence`` transforms.

    **k3s cluster auto-creation:**

    The k3s cluster is created automatically during ``hmd neuronsphere up`` via
    Floci's EKS API. It is part of core platform startup, not owned by any single
    plugin.

    **Argo nsplugin (``hmd-app-argo/src/local/nsplugin.json``):**

    The Argo plugin follows the standard nsplugin pattern. It:

    1. Installs Argo Workflows (controller + server) on k3s via Helm or
       ``kubectl apply``, using the charts already in ``hmd-app-argo/src/helm/``.
    2. Creates the namespace, service account, and RBAC rules needed for
       ``ms-transform`` to submit workflows.
    3. Stores the Argo token in Floci Secrets Manager.
    4. Registers ``argo-server`` with ``ms-naming`` for service discovery.
    5. Exposes the Argo UI via nginx proxy at ``/argo/`` for end users to monitor
       ``image_sequence`` workflow execution.

    Only Argo Workflows is installed -- not Argo Events, not Argo CD.

    The plugin is enabled by default (``enabled_by_default: true``) and can be
    disabled via ``HMD_LOCAL_NEURONSPHERE_ENABLE_ARGO=false`` for
    resource-constrained environments. When disabled, ``image_sequence``
    transforms fall back to Airflow DockerOperator rendering (graceful
    degradation).

    **ms-transform engine configuration:**

    The transform plugin (``hmd-ms-transform/src/local/nsplugin.json``) adds
    ``argo`` to its ``dependencies.requires_plugins`` and configures the engine
    list:

    .. code-block:: json

        {
            "config": {
                "tf_engines": {
                    "default": "[\"argo\", \"airflow\"]",
                    "env_var": "TF_ENGINES",
                    "type": "json"
                },
                "argo_host": {
                    "default": "http://argo-server:2746",
                    "env_var": "ARGO_HOST",
                    "type": "string"
                }
            },
            "dependencies": {
                "requires_plugins": ["graph", "airflow", "argo"]
            }
        }

    With ``TF_ENGINES=["argo", "airflow"]``:

    - ``image_sequence`` routes to ``ArgoTransformEngine`` (Argo listed first,
      supports this type)
    - ``provider`` routes to ``AirflowTransformEngine`` (Argo does not support it)
    - ``dbt`` routes to ``AirflowTransformEngine`` (Argo does not support it)

    This matches cloud engine selection exactly.

    **ArgoTransformEngine URL override:**

    The ``ArgoTransformEngine`` currently constructs the Argo URL from
    ``ARGO_INSTANCE_NAME`` and domain logic. For local use, a direct ``ARGO_HOST``
    environment variable overrides this construction:

    .. code-block:: python

        argo_host = os.environ.get("ARGO_HOST") or f"https://{ARGO_INSTANCE_NAME}.{domain}"

    This small change to ``ArgoTransformEngine.py`` enables local use without
    altering cloud behavior.

    **Networking:**

    The ``ms-transform`` Lambda runs inside Floci. The Argo server runs on k3s
    (also inside Floci's EKS). For Lambda-to-k3s communication:

    - Argo server exposed via NodePort on the k3s container
    - k3s container is on the ``neuronsphere_default`` Docker network
    - The ``ARGO_HOST`` URL points to the k3s container's hostname + NodePort
    - nginx proxy adds an ``/argo/`` route for UI access

    **Container image access:**

    Argo workflows pull container images specified in ``image_sequence`` configs.
    Locally, k3s uses locally built or pulled images from the local Docker daemon
    (or images loaded into k3s's containerd). No private registry configuration
    is needed initially.

    **Resource impact:**

    - k3s cluster: ~500 MB -- 1 GB RAM baseline
    - Argo controller: ~200 -- 400 MB RAM
    - Total additional: ~700 MB -- 1.4 GB RAM
    - Acceptable for cloud parity on a core end-user feature
    - Disableable via env var for resource-constrained environments

.. spec:: Replace MiniStack with Floci as the AWS emulation layer
    :id: HMD_CLI_NEURONSPHERE_NERD001_SPEC001
    :links: HMD_CLI_NEURONSPHERE_NERD001
    :status: implemented

    Floci replaces MiniStack as a drop-in AWS emulator. The switch is
    straightforward because both expose port 4566 and both use the standard AWS
    SDK wire protocol.

    Changes to ``hmd-cli-neuronsphere``:

    - ``docker-compose.ministack.yml``: Change image from
      ``ministackorg/ministack:latest`` to ``hectorvent/floci:latest``.
    - ``ministack_deployer.py``: Update health check endpoint from
      ``/_ministack/health`` to Floci's health endpoint. Rename module to
      ``floci_deployer.py`` (with a backwards-compatible alias). All boto3 calls
      via ``_get_client()`` remain unchanged.
    - Environment variable: Rename ``HMD_LOCAL_NEURONSPHERE_ENABLE_MINISTACK``
      to ``HMD_LOCAL_NEURONSPHERE_ENABLE_FLOCI`` with a fallback that reads the
      old variable for backwards compatibility.

    The ``nsplugin.json`` resource types ``sqs_queues``, ``s3_buckets``, and
    ``dynamodb_tables`` continue to work unchanged. Floci additionally enables
    ``secrets`` (Secrets Manager) and ``step_functions`` resource types in future
    phases.

Extend Mode: Infrastructure Validation for Engineers
-----------------------------------------------------

.. spec:: Extend mode adds ms-deployment for infrastructure validation
    :id: HMD_CLI_NEURONSPHERE_NERD001_SPEC002
    :links: HMD_CLI_NEURONSPHERE_NERD001
    :status: proposed

    When ``HMD_LOCAL_NEURONSPHERE_MODE=extend``, ``hmd neuronsphere up`` starts
    everything Platform mode starts, plus ``hmd-ms-deployment`` as an additional
    Floci Lambda.

    **What Extend mode adds:**

    1. **ms-deployment** (Floci Lambda): Deployment orchestration service.
       Deployed as a Lambda function behind API Gateway, same as
       ``ms-naming`` and ``ms-transform``.
    2. **ms-deployment DB init**: Database initialization container for the
       deployment service tables.
    3. **CLI commands** for engineers:

       - ``hmd neuronsphere register-repo``: Register a RepoClass with
         ms-deployment.
       - ``hmd neuronsphere get-config``: Get a ``DeploymentConfig`` for a
         registered repo.
       - ``hmd neuronsphere deploy-repo``: Execute a single repo's deployment
         via ``LocalWorkflowRunner`` (projectbuilder container).

    **What Extend mode does NOT do:**

    - No automatic BOM seeding of 28+ RepoClasses at startup.
    - No full deployment DAG execution at startup.
    - No dual Floci instances (single Floci serves all needs).
    - No projectbuilder containers running automatically.

    **Why this is sufficient for engineers:**

    Engineers primarily need a ``DeploymentConfig`` to render CDKTF/Helm output.
    In the cloud, ``ms-deployment`` builds a ``DeploymentConfig`` by looking up
    the RepoClass and its dependencies, resolving dependency configurations, and
    producing a JSON config file that CDKTF and Helm consume. Locally, engineers
    need this same config to run ``cdktf synth`` or ``helm template``. They do
    NOT need to execute the full deployment -- they need to see the rendered
    output to verify correctness.

    **Single Floci instance:**

    The current deploy mode uses dual Floci instances (admin on :4566, workload
    on :4567) to mirror the cloud's multi-account architecture. This is
    unnecessary locally since IAM account boundaries are not enforced. Extend
    mode uses a single Floci instance, saving ~200 MB RAM and simplifying
    networking.

Per-Repo Local Deployment Overrides
------------------------------------

.. spec:: Per-repo local deployment overrides via nsplugin.json
    :id: HMD_CLI_NEURONSPHERE_NERD001_SPEC003
    :links: HMD_CLI_NEURONSPHERE_NERD001
    :status: withdrawn

    .. warning::

       **Withdrawn 2026-09-25.** ``nsplugin.json`` was withdrawn with
       NERD002 SPEC005: it restated resources, databases and
       dependencies the BACON manifest and ``meta-data/resources/*.yaml``
       already carried, so it was a second place to say one thing and it went
       stale. How a repo deploys locally is settled by its own manifest and by the
       environment manifest that names its checkout
       (NERD010), not by a ``local_deploy``
       section in a parallel inventory.

       The schema below is kept as the record of what was proposed.

    Each repo that needs a local override declares it in its own
    ``nsplugin.json`` via an optional ``local_deploy`` section. There is
    no centralized override file. Each repo owns its local deployment behavior.

    **Schema: ``local_deploy`` field in nsplugin.json**

    .. code-block:: json

        {
            "local_deploy": {
                "strategy": "<strategy>",
                "reason": "Human-readable explanation",
                "compose_file": "docker-compose.local-deploy.yml",
                "script": "scripts/local_deploy.sh",
                "config_overrides": {},
                "provides_services": ["service-name"]
            }
        }

    **Field definitions:**

    - ``strategy`` (string, required): One of ``compose_substitute``,
      ``script``, ``config_override``, ``skip``, or ``local_storage``.
    - ``reason`` (string, optional): Documents why this override exists.
    - ``compose_file`` (string, conditional): Required when strategy is
      ``compose_substitute``. Path relative to ``src/local/`` for the
      Docker Compose file to start.
    - ``script`` (string, conditional): Required when strategy is ``script``.
      Path relative to ``src/local/`` for the bash/shell script to execute.
    - ``config_overrides`` (object, optional): Key-value pairs that override
      the default deployment configuration.
    - ``provides_services`` (array of strings, optional): Service names that
      this override satisfies for downstream dependency resolution.

    **Five strategies:**

    - ``"compose_substitute"``: Start a Docker Compose file instead of deploying
      through the DAG. Example: ``hmd-inf-neptune`` starts JanusGraph as a
      Gremlin-compatible substitute.
    - ``"script"``: Run a bash/shell script. Examples: creating static
      PersistentVolumes, adding k3s node groups.
    - ``"config_override"``: Deploy through the DAG normally but inject
      configuration overrides.
    - ``"skip"``: Remove this RepoClass from the local deployment DAG entirely.
      Used for infrastructure with no local equivalent (EFS CSI, EBS CSI, WAF).
    - ``"local_storage"``: Creates static Kubernetes PersistentVolumes backed by
      ``${HMD_HOME}`` directories.

    **Note:** In Platform mode, the ``local_deploy`` section is informational
    only -- there is no deployment DAG. These overrides are used when an engineer
    explicitly runs ``hmd neuronsphere deploy-repo`` in Extend mode, or when the
    ``LocalWorkflowRunner`` executes a single-repo deployment.

    **Example: ``hmd-inf-neptune/src/local/nsplugin.json``**

    .. code-block:: json

        {
            "plugin_name": "graph",
            "compose_file": "docker-compose.janusgraph.yml",
            "local_deploy": {
                "strategy": "compose_substitute",
                "compose_file": "docker-compose.janusgraph.yml",
                "provides_services": ["graph-db"],
                "reason": "Floci does not emulate Neptune; JanusGraph provides Gremlin compatibility"
            }
        }

    **Example: ``hmd-inf-efs-csi/src/local/nsplugin.json``**

    .. code-block:: json

        {
            "plugin_name": "efs_csi",
            "local_deploy": {
                "strategy": "skip",
                "reason": "EFS CSI driver not applicable on k3s"
            }
        }

Plugin-Declared Volumes
-----------------------

.. spec:: Plugin-declared volumes for local storage
    :id: HMD_CLI_NEURONSPHERE_NERD001_SPEC013
    :links: HMD_CLI_NEURONSPHERE_NERD001
    :status: withdrawn

    .. warning::

       **Withdrawn 2026-09-25.** ``nsplugin.json`` was withdrawn with
       NERD002 SPEC005: it restated resources, databases and
       dependencies the BACON manifest and ``meta-data/resources/*.yaml``
       already carried, so it was a second place to say one thing and it went
       stale. Storage a workload needs is declared where the workload is declared --
       its chart values and its resource declarations -- rather than in a
       ``volumes`` field on a plugin index.

       The schema below is kept as the record of what was proposed.

    Plugins declare their persistent volume requirements in ``nsplugin.json``
    using an optional ``volumes`` field. This enables the creation of static
    PersistentVolumes in k3s backed by ``${HMD_HOME}`` directories.

    **Schema extension to nsplugin.json:**

    .. code-block:: json

        {
            "volumes": [
                {
                    "name": "dags",
                    "hmd_home_path": "airflow/dags",
                    "container_path": "/opt/airflow/dags",
                    "access_mode": "ReadWriteMany"
                }
            ]
        }

    **Field definitions:**

    - ``name``: Identifier for the volume within this plugin.
    - ``hmd_home_path``: Path relative to ``${HMD_HOME}``.
    - ``container_path``: The mount path inside the container. Informational
      for documentation and validation.
    - ``access_mode``: Kubernetes access mode (``ReadWriteOnce``,
      ``ReadWriteMany``, ``ReadOnlyMany``).

nsplugin Schema Extensions
--------------------------

.. spec:: nsplugin.json extended with volumes and local_deploy
    :id: HMD_CLI_NEURONSPHERE_NERD001_SPEC009
    :links: HMD_CLI_NEURONSPHERE_NERD001
    :status: withdrawn

    .. warning::

       **Withdrawn 2026-09-25.** ``nsplugin.json`` was withdrawn with
       NERD002 SPEC005: it restated resources, databases and
       dependencies the BACON manifest and ``meta-data/resources/*.yaml``
       already carried, so it was a second place to say one thing and it went
       stale. Both fields this section adds belong to specifications that are
       themselves withdrawn above, so the extension has nothing left to
       extend.

       The schema below is kept as the record of what was proposed.

    The ``nsplugin.json`` schema gains two optional additive fields:
    ``volumes`` (SPEC013) for storage declarations and ``local_deploy``
    (SPEC003) for declaring how the repo deploys locally. All existing fields
    are unchanged. Both modes use the same plugin definitions differently:

    **Platform mode** uses from each nsplugin:

    - ``compose_file``: Started via Docker Compose (for data services).
    - ``resources``: Registered in ms-naming and provisioned in Floci
      (including ``deploy_as_lambda`` services).
    - ``dependencies``: Validated at startup.
    - ``required_dirs``: Directories created in ``${HMD_HOME}``.
    - ``config``, ``config_mappings``, ``templates``, ``postgres_scripts``:
      Applied to the environment.
    - ``volumes``: Used for k3s PV creation when Argo or other k3s services
      need persistent storage.
    - ``local_deploy``: **Informational only.** Platform mode does not use the
      deployment DAG.

    **Extend mode** additionally uses:

    - ``local_deploy``: Determines override strategy when an engineer runs
      ``hmd neuronsphere deploy-repo`` for a specific repo. If present, the
      declared strategy replaces or modifies the standard DAG deployment for
      that repo.

    **The ``manifest.json`` is the source of truth for Extend mode.** The
    ``deploy.dependencies`` and ``deploy.default_configuration`` sections drive
    the deployment graph when engineers register repos with ms-deployment.

User Workflows
--------------

**End User: Transform Development (image_sequence)**

.. code-block:: bash

    # Start the platform (transform + airflow + argo + trino plugins)
    hmd neuronsphere up

    # ms-transform is running as a Floci Lambda
    # Argo Workflows is running on k3s
    # Airflow is running (:175)
    # Trino is running (:8081)

    # Build your transform project
    cd ~/repos/my-image-seq-transform
    hmd build

    # Deploy transform -- calls ms-transform Lambda API (same as cloud)
    hmd transform deploy --name my-transform --version 0.1.0

    # ms-transform routes image_sequence to ArgoTransformEngine
    # ArgoTransformEngine submits Argo Workflow YAML to k3s
    # Argo executes container sequence on k3s
    # Same Workflow YAML, same container orchestration as cloud

    # Monitor in Argo UI at http://localhost/argo/
    # Results land in Floci S3, queryable via Trino

    hmd neuronsphere down

**End User: Transform Development (provider/dbt)**

.. code-block:: bash

    hmd neuronsphere up

    cd ~/repos/my-provider-transform
    hmd build

    hmd transform deploy --name my-transform --version 0.1.0

    # ms-transform routes provider to AirflowTransformEngine
    # Airflow DAG generated via 000_hmd_dag_maker
    # Scheduler picks up DAG, workers execute against Trino
    # Same as cloud

**End User: Deploy a Microservice Locally**

.. code-block:: bash

    hmd neuronsphere up

    cd ~/repos/hmd-ms-my-service
    hmd build

    # Deploy as Floci Lambda -- same as cloud Lambda deployment
    hmd neuronsphere run my-service

    # Lambda behind API Gateway in Floci
    # Registered with ms-naming for service discovery
    # Available at http://localhost/hmd_ms_my_service/

    curl http://localhost/hmd_ms_my_service/api/health

**End User: SQL Query Development**

.. code-block:: bash

    hmd neuronsphere up  # with trino plugin

    # Connect Trino client to localhost:8081
    trino --server localhost:8081 --catalog hive --schema default
    > SELECT * FROM my_table;

    # Same catalogs, schemas, and connectors as cloud
    # Data stored in Floci S3

**End User: Superset Dashboard Development**

.. code-block:: bash

    hmd neuronsphere up  # with superset + trino plugins

    # Superset at :8088
    # Create datasets pointing to Trino
    # Build charts and dashboards
    # Export dashboard JSON for import to cloud Superset

**Engineer: Validate CDKTF for a New Microservice**

.. code-block:: bash

    HMD_LOCAL_NEURONSPHERE_MODE=extend hmd neuronsphere up

    cd ~/repos/hmd-ms-my-service
    hmd build

    # Register with ms-deployment to get a DeploymentConfig
    hmd neuronsphere register-repo --repo-name hmd-ms-my-service
    hmd neuronsphere get-config --repo-name hmd-ms-my-service -o config.json

    # Render CDKTF output
    cd src/cdktf
    AWS_ENDPOINT_URL=http://localhost:4566 cdktf synth

    # Inspect the generated Terraform JSON
    cat cdktf.out/stacks/*/cdk.tf.json | jq .

    # Optionally deploy to Floci to test Lambda creation
    AWS_ENDPOINT_URL=http://localhost:4566 cdktf deploy --auto-approve

    # Test the deployed Lambda
    curl http://localhost/hmd_ms_my_service/api/health

**Engineer: Validate Helm Chart for a New Application**

.. code-block:: bash

    HMD_LOCAL_NEURONSPHERE_MODE=extend hmd neuronsphere up

    cd ~/repos/hmd-inf-my-app
    hmd neuronsphere register-repo --repo-name hmd-inf-my-app
    hmd neuronsphere get-config --repo-name hmd-inf-my-app -o config.json

    # Render Helm template (k3s already running from platform mode)
    cd src/helm
    helm template my-app . -f values.yaml --set-file config=../../config.json

    # Inspect rendered Kubernetes manifests

    # Optionally deploy to the running k3s
    helm install my-app . -f values.yaml
    kubectl get pods

**Engineer: Full Deploy Script Test (Rare)**

.. code-block:: bash

    HMD_LOCAL_NEURONSPHERE_MODE=extend hmd neuronsphere up

    cd ~/repos/hmd-ms-my-service
    hmd build

    hmd neuronsphere register-repo --repo-name hmd-ms-my-service

    # Run the full deploy script in projectbuilder, same as Argo would
    hmd neuronsphere deploy-repo --repo-name hmd-ms-my-service
    # Uses LocalWorkflowRunner for this single repo

LocalWorkflowRunner (On-Demand)
-------------------------------

.. spec:: LocalWorkflowRunner for single-repo deployment testing
    :id: HMD_CLI_NEURONSPHERE_NERD001_SPEC006
    :links: HMD_CLI_NEURONSPHERE_NERD001
    :status: proposed

    The ``LocalWorkflowRunner`` is retained for engineers who need to test the
    full deployment flow for a specific repo. It is **not invoked during
    startup** -- it is available via ``hmd neuronsphere deploy-repo``.

    **Design:**

    - Reuses the existing ``DeploymentDag``, ``build_deployment_dag()``, and
      ``traverse_deployment_dag()`` functions unchanged.
    - Executes each node's script inside an ``hmd-img-projectbuilder`` Docker
      container via ``docker run``.
    - Uses ``concurrent.futures.ThreadPoolExecutor(max_workers=4)`` matching
      Argo's parallelism setting.

    **Container execution per DAG node:**

    .. code-block:: bash

        docker run --rm \
          --network neuronsphere_default \
          -e HMD_ENVIRONMENT=local \
          -e AWS_ENDPOINT_URL=http://floci:4566 \
          -v /var/run/docker.sock:/run/containerd/containerd.sock \
          -v ${HMD_REPO_HOME}/${repo_name}:/workspace \
          ${HMD_APP_IMAGE} \
          bash -c "${deploy_script}"

    **Handling ``local_deploy`` overrides:** When the runner encounters a DAG
    node whose repo has a ``local_deploy`` section in its ``nsplugin.json``
    (see SPEC003), it applies the declared strategy instead of running
    ``hmd deploy`` in the projectbuilder container.

    **Single-repo focus:** The runner is designed for testing one repo at a
    time, not for deploying the full 28-node platform. Engineers register their
    repo with ms-deployment, then run ``hmd neuronsphere deploy-repo`` to
    execute that repo's deployment in the same projectbuilder container that
    Argo would use in the cloud.

CLI Tool Convergence
--------------------

.. spec:: Enable CLI tools to target Floci with minimal code changes
    :id: HMD_CLI_NEURONSPHERE_NERD001_SPEC011
    :links: HMD_CLI_NEURONSPHERE_NERD001
    :status: proposed

    All ``hmd-cli-*`` tools already use ``ServiceManager`` / ms-naming for
    service discovery. When running locally, ms-naming resolves service URLs to
    local endpoints. The only change needed per CLI tool is ensuring AWS SDK
    calls include ``endpoint_url`` when ``HMD_ENVIRONMENT=local``.

    **Centralized boto3 session factory in ``hmd-cli-tools``:**

    A new utility function wraps boto3 session/client creation. When
    ``HMD_ENVIRONMENT=local``, it automatically sets:

    - ``endpoint_url=http://localhost:4566`` (or ``FLOCI_ENDPOINT`` env var)
    - ``aws_access_key_id=dummykey``
    - ``aws_secret_access_key=dummykey``
    - ``region_name`` from ``HMD_REGION``

    This eliminates the need for each CLI tool to independently handle
    local/cloud AWS endpoint switching.

Per-Repository Changes
----------------------

**hmd-cli-neuronsphere** (primary changes):

- Rename ``ministack_deployer.py`` to ``floci_deployer.py`` (done).
- Update Docker Compose to use Floci image (done).
- Add ``HMD_LOCAL_NEURONSPHERE_MODE`` handling: ``platform`` (default) and
  ``extend`` values, with backwards compatibility for ``legacy`` and ``deploy``.
- Rename ``start_neuronsphere_legacy()`` to ``start_neuronsphere_platform()``.
- Simplify ``start_neuronsphere_deploy()`` to ``start_neuronsphere_extend()``:
  starts Platform mode plus ms-deployment Lambda. No BOM seeding or DAG
  execution.
- Add k3s cluster creation via Floci EKS API during platform startup.
- Add CLI commands: ``register-repo``, ``get-config``, ``deploy-repo``.
- Remove ``local_overrides.json`` from startup path (retained for
  ``deploy-repo`` fallback).
- Remove dual Floci instance configuration.
- Remove automatic BOM seeding from startup.

**hmd-app-argo** (new nsplugin):

- Create ``src/local/nsplugin.json`` with Argo plugin configuration.
- Plugin installs Argo Workflows on k3s using existing Helm charts.
- Plugin creates namespace, service account, RBAC for ms-transform.
- Plugin registers ``argo-server`` with ms-naming.

**hmd-ms-transform** (nsplugin update):

- Add ``argo`` to ``dependencies.requires_plugins``.
- Add ``TF_ENGINES`` and ``ARGO_HOST`` to config section.
- Add ``ARGO_HOST`` env var override to ``ArgoTransformEngine.py``.

**hmd-ms-deployment** (retained for Extend mode):

- ``local_workflow_runner.py`` retained for ``deploy-repo`` command.
- No changes needed to deployment service itself.

**hmd-cli-tools** (shared utilities):

- Add centralized boto3 session factory with Floci endpoint override.

Risk Assessment
---------------

.. list-table::
   :header-rows: 1
   :widths: 15 40 45

   * - Level
     - Risk
     - Mitigation
   * - Low
     - Floci replacing MiniStack (Phase 0)
     - Same port, same SDK interface. Rollback is a one-line image change.
   * - Low
     - CLI tool convergence (Phase 4)
     - Incremental, per-repo. No big-bang migration.
   * - Medium
     - Floci maturity (new project)
     - Phase 0 uses only proven services (S3, DynamoDB, SQS, Lambda, API GW).
       EKS/k3s introduced in Phase 1 after validation.
   * - Medium
     - Argo on k3s fidelity
     - k3s may not support all Kubernetes features Argo uses. Mitigated by
       testing with real ``image_sequence`` transforms in Phase 1. Graceful
       fallback to Airflow if Argo plugin is disabled.
   * - Medium
     - k3s resource consumption (~700 MB -- 1.4 GB)
     - Acceptable for cloud parity. Plugin disableable via env var. Most
       developer machines have sufficient RAM.
   * - Low
     - Per-repo ``local_deploy`` maintenance
     - Each repo owns its override declaration. Only needed for repos that
       cannot deploy via Floci (very few). Clear errors surface when overrides
       are needed.
   * - Medium
     - Lambda-to-k3s networking
     - Floci Lambda containers need to reach Argo server on k3s. Validated
       during Phase 1 implementation. NodePort + Docker network is the primary
       approach.

Phased Migration Strategy
--------------------------

.. spec:: Phased migration strategy
    :id: HMD_CLI_NEURONSPHERE_NERD001_SPEC012
    :links: HMD_CLI_NEURONSPHERE_NERD001
    :status: proposed

    The migration is divided into four phases, each independently shippable.

    **Phase 0: Floci drop-in replacement for MiniStack (DONE)**

    - Replace MiniStack Docker image with Floci.
    - Update health check endpoint.
    - Rename ``ministack_deployer.py`` to ``floci_deployer.py``.
    - Test all existing functionality (S3, DynamoDB, SQS, Lambda, API Gateway).
    - No user-facing behavioral changes.

    **Phase 1: Platform mode + Argo nsplugin**

    - Rename ``legacy`` to ``platform`` with backwards compatibility.
    - Add k3s cluster auto-creation via Floci EKS API during
      ``hmd neuronsphere up``.
    - Create Argo nsplugin in ``hmd-app-argo/src/local/``.
    - Update ``hmd-ms-transform/src/local/nsplugin.json`` to depend on Argo
      and set ``TF_ENGINES=["argo", "airflow"]``.
    - Add ``ARGO_HOST`` env var override to ``ArgoTransformEngine.py``.
    - Verify all 3 transform types route to correct engines:
      ``image_sequence`` to Argo, ``provider``/``dbt`` to Airflow.
    - Expose Argo UI via nginx at ``/argo/``.
    - Document end-user workflows.

    **Phase 2: Extend mode for engineers**

    - Add ``hmd-ms-deployment`` as a Floci Lambda in Extend mode.
    - Implement ``hmd neuronsphere register-repo`` CLI command.
    - Implement ``hmd neuronsphere get-config`` CLI command.
    - Implement ``hmd neuronsphere deploy-repo`` CLI command using
      ``LocalWorkflowRunner`` for single-repo deployment.
    - Remove automatic BOM seeding and full DAG execution from startup.
    - Simplify to single Floci instance (remove dual-Floci configuration).
    - Test with 2-3 real RepoClasses (e.g., ``hmd-inf-redis``,
      ``hmd-ms-transform``).

    **Phase 3: CLI tool convergence**

    - Add centralized boto3 session factory to ``hmd-cli-tools``.
    - Migrate individual repos incrementally to use the shared factory.
    - Add Floci-specific resource provisioning (Secrets Manager, IAM roles).

Key Design Decisions
--------------------

.. list-table::
   :header-rows: 1
   :widths: 25 30 45

   * - Decision
     - Tradeoff
     - Justification
   * - Argo as nsplugin, enabled by default
     - ~700 MB -- 1.4 GB additional RAM
     - Cloud parity for ``image_sequence`` transforms. Plugin pattern allows
       disabling (``HMD_LOCAL_NEURONSPHERE_ENABLE_ARGO=false``). Falls back to
       Airflow-only.
   * - Services as Floci Lambdas
     - More complexity than Docker containers
     - Cloud parity. Same Lambda execution model, same API Gateway routing.
       Non-negotiable for matching cloud behavior.
   * - k3s auto-created in platform startup
     - Higher baseline resource usage
     - Core infrastructure for Argo. Also benefits Extend mode engineers who
       can deploy Helm charts without extra setup.
   * - No full infrastructure DAG at startup
     - Cannot test full 28-node pipeline locally
     - Infrastructure deployment is an engineering concern. The DAG deploys
       VPC, EKS, ALBs -- none of which exist locally. Docker Compose provides
       the equivalent. Engineers use ``deploy-repo`` for single-repo testing.
   * - Single Floci instance
     - No admin/workload account separation
     - IAM boundaries are not enforced locally. Saves ~200 MB RAM and
       simplifies networking.
   * - ms-deployment only in Extend mode
     - End users cannot query deployment graph
     - End users do not interact with ms-deployment. They use ms-transform,
       ms-naming, Argo, Airflow -- all available in Platform mode.
   * - Platform mode is the product
     - Not a stepping stone to "full deploy mode"
     - Platform mode provides cloud-parity user experience. Extend mode is
       for infrastructure engineers, not a more complete version of Platform.
