.. NERD001 Floci-Based Local NeuronSphere Architecture

NERD001 Floci-Based Local NeuronSphere Architecture
====================================================

.. req:: Close the local/cloud deployment gap using Floci
    :id: HMD_CLI_NEURONSPHERE_NERD001
    :status: proposed

    The local NeuronSphere development environment should achieve near-parity with
    cloud deployments by adopting Floci as the AWS emulation layer, running
    ``hmd-ms-deployment`` and ``hmd-ms-naming`` in a local "admin" control plane,
    and deploying RepoClasses locally using the same ``hmd-img-projectbuilder``
    image and commands used by Argo in the cloud. The ``hmd neuronsphere up``
    command remains the single entrypoint, operating in Legacy mode by default and
    in Deploy mode when ``HMD_LOCAL_NEURONSPHERE_MODE=deploy`` is set. The
    existing ``nsplugin.json`` schema is unchanged -- both modes derive their
    behavior from the same plugin definitions.

Motivation
----------

NeuronSphere deploys to AWS using Argo Workflows on Kubernetes, with
``hmd-ms-deployment`` orchestrating a dependency DAG of RepoInstances. Each step
in the Argo workflow runs the ``hmd-img-projectbuilder`` image, executing
``hmd deploy`` commands that download build archives from ``hmd-ms-artifact-lib``,
run Terraform/CDKTF/Helm/Docker, and report status back. Locally,
``hmd neuronsphere up`` uses flat Docker Compose files and MiniStack for AWS
emulation. This creates several gaps:

- **No local deployment service.** ``hmd-ms-deployment`` does not run locally.
  Developers cannot test deployment logic, dependency resolution, or changeset
  workflows without a cloud environment.
- **No DAG-based orchestration.** Docker Compose ``depends_on`` is a shallow
  health-check dependency, not a true deployment DAG. Services start in a
  non-deterministic order that does not reflect cloud behavior.
- **No RepoClass development loop.** Developers building new RepoClasses must
  push to the cloud to test their deployment flow. There is no way to register
  a RepoClass, build a changeset, and apply it locally.
- **Separate code paths.** CLI tools often branch on ``HMD_ENVIRONMENT == "local"``
  with entirely different logic rather than targeting the same AWS-compatible
  APIs.
- **MiniStack limitations.** MiniStack provides S3, DynamoDB, SQS, Lambda, and
  API Gateway, but lacks Step Functions, Secrets Manager, IAM policy evaluation,
  CloudFormation, and ECS/EKS emulation.

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
Gateway), so risk is contained. Advanced services (Step Functions, Secrets
Manager, IAM) are introduced in later phases after validation.

Architecture Overview
---------------------

Deploy mode introduces a local "admin" control plane that mirrors the cloud
admin account. The control plane consists of Floci, ``hmd-ms-deployment``,
and ``hmd-ms-naming``. Once the control plane is up, bundled plugins are
"deployed" as RepoClasses through the same changeset flow used in the cloud,
with the ``hmd-img-projectbuilder`` image executing ``hmd deploy`` commands
locally via the ``LocalWorkflowRunner``.

.. uml::

    @startuml
    !theme plain
    skinparam componentStyle rectangle
    skinparam defaultFontSize 11
    skinparam packageStyle frame
    skinparam linetype ortho

    title Cloud vs Local (Deploy Mode) Architecture

    together {
        package "Cloud" as cloud {
            component "Client\nhmd deploy ..." as cloud_cli
            component "ms-deployment\n(Lambda, admin account)\napply_changeset()\nbuild DAG\nsubmit to Argo" as cloud_deploy
            component "Argo Workflows\n(EKS / K8s)\nparallelism: 4" as cloud_argo
            component "hmd-img-projectbuilder\nhmd deploy\n  --repo-name ...\n  --config-file ..." as cloud_pb
            component "Real AWS\nS3, DynamoDB, SQS, Lambda,\nAPI GW, Neptune, EKS,\nRDS, ElastiCache, ..." as cloud_aws
            component "ms-artifact-lib\n(S3 + Neptune)" as cloud_artifacts

            cloud_cli -down-> cloud_deploy
            cloud_deploy -down-> cloud_argo
            cloud_argo -down-> cloud_pb
            cloud_pb -down-> cloud_aws
            cloud_pb .right.> cloud_artifacts : fetch\narchives
        }

        package "Local (Deploy Mode)" as local {
            component "hmd neuronsphere up\n(MODE=deploy)" as local_cli

            package "Admin Control Plane\n(Docker Compose)" as control_plane {
                component "Floci\n(port 4566)" as floci
                component "ms-deployment" as local_deploy
                component "ms-naming" as naming
                component "PostgreSQL" as pg
                component "nginx proxy" as nginx
            }

            component "LocalWorkflowRunner\n(subprocess, parallelism=4)" as local_runner
            component "hmd-img-projectbuilder\nhmd deploy\n  --repo-name ...\n  --config-file ..." as local_pb

            package "Floci Services" as floci_services {
                component "CDKTF targets\nS3, DynamoDB, SQS,\nLambda, API GW,\nElastiCache, RDS, ..." as floci_aws
                component "EKS (k3s)\nHelm deploys:\nAirflow, Argo, Redis,\nTrino, ClickHouse,\nOTel, Superset, ..." as floci_eks
            }

            component "JanusGraph\n(Docker Compose)\nOnly override:\nNeptune substitute" as janusgraph

            component "Local artifacts\n(Floci S3 + local\ncode / archives)" as local_artifacts

            local_cli -down-> control_plane
            local_deploy -down-> local_runner
            local_runner -down-> local_pb
            local_pb -down-> floci_aws
            local_pb -down-> floci_eks
            local_pb .right.> local_artifacts : fetch\narchives
            floci_aws -up[hidden]- floci
            floci_eks -up[hidden]- floci
            janusgraph -up-> floci_services : Gremlin\ncompat
        }
    }
    @enduml

**Two operating modes, one entrypoint, no nsplugin.json changes:**

``hmd neuronsphere up`` checks ``HMD_LOCAL_NEURONSPHERE_MODE``:

- **Legacy** (default, ``HMD_LOCAL_NEURONSPHERE_MODE`` unset or ``legacy``):
  Today's behavior unchanged. Docker Compose starts services, Floci (replacing
  MiniStack) provisions AWS resources. Every plugin's ``compose_file`` is used.
- **Deploy** (``HMD_LOCAL_NEURONSPHERE_MODE=deploy``): Starts the admin control
  plane (Floci, ms-deployment, ms-naming, PostgreSQL), then deploys all enabled
  plugins as RepoClasses via the changeset flow. CDKTF targets Floci's AWS APIs,
  Helm deploys to Floci's EKS (k3s). Only genuinely unsupported services (Neptune)
  use a Docker Compose substitute.

Deploying RepoClasses Locally via Floci
---------------------------------------

In the cloud, every RepoClass deploys using a combination of CDKTF (for AWS
infrastructure), Helm (for Kubernetes workloads), and Docker (for Lambda
functions). Floci's breadth of service emulation means that **nearly all of
these deploy commands can run unchanged against Floci locally:**

.. list-table::
   :header-rows: 1
   :widths: 30 20 25 25

   * - Cloud RepoClass
     - deploy.commands
     - Floci Target
     - Status
   * - ``hmd-inf-redis``
     - cdktf, helm
     - ElastiCache + EKS (k3s)
     - Supported
   * - ``hmd-app-airflow``
     - cdktf, helm
     - CDKTF to Floci + Helm to EKS (k3s)
     - Supported
   * - ``hmd-app-argo``
     - cdktf, helm
     - CDKTF to Floci + Helm to EKS (k3s)
     - Supported
   * - ``hmd-inf-otel-collector``
     - cdktf, helm
     - CDKTF to Floci + Helm to EKS (k3s)
     - Supported
   * - ``hmd-inf-trino``
     - cdktf, helm
     - CDKTF to Floci + Helm to EKS (k3s)
     - Supported
   * - ``hmd-inf-clickhouse``
     - cdktf, helm
     - CDKTF to Floci + Helm to EKS (k3s)
     - Supported
   * - ``hmd-inf-superset``
     - cdktf, helm
     - CDKTF to Floci + Helm to EKS (k3s)
     - Supported
   * - ``hmd-inf-hive-metastore``
     - helm
     - Helm to EKS (k3s)
     - Supported
   * - ``hmd-inf-s3bucket``
     - cdktf
     - Floci S3
     - Supported
   * - ``hmd-inf-eks-cluster``
     - cdktf
     - Floci EKS (creates k3s cluster)
     - Supported
   * - ``hmd-ms-transform``
     - docker, cdktf, helm
     - Lambda + CDKTF + EKS (k3s)
     - Supported
   * - ``hmd-ms-deployment``
     - docker, cdktf
     - Lambda + CDKTF to Floci
     - Supported
   * - ``hmd-ms-naming``
     - docker, cdktf
     - Lambda + CDKTF to Floci
     - Supported
   * - ``hmd-inf-neptune``
     - cdktf
     - **No Floci equivalent**
     - Needs local override

**Neptune is the only service that requires a local override.** Floci does not
emulate Neptune (AWS's graph database). Locally, the ``graph`` plugin provides
JanusGraph as a Gremlin-compatible substitute via its ``compose_file``. This
is the same substitute used in Legacy mode today.

**All other RepoClasses deploy through the DAG** using the same
``hmd-img-projectbuilder`` image and the same ``hmd deploy`` commands that run
in Argo in the cloud. CDKTF targets Floci's AWS API (S3, ElastiCache, EKS,
Lambda, API Gateway, etc.). Helm deploys to Floci's EKS, which runs real k3s
clusters. Docker builds and deploys to Floci's Lambda service.

The ``local_service_mapping.json`` (see SPEC003) only needs entries for
genuinely unsupported services. For the current NeuronSphere, that means only
Neptune. As Floci matures or new unsupported dependencies emerge, this mapping
is the single place to add overrides.

.. spec:: Replace MiniStack with Floci as the AWS emulation layer
    :id: HMD_CLI_NEURONSPHERE_NERD001_SPEC001
    :links: HMD_CLI_NEURONSPHERE_NERD001
    :status: proposed

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

.. spec:: Admin control plane for Deploy mode
    :id: HMD_CLI_NEURONSPHERE_NERD001_SPEC002
    :links: HMD_CLI_NEURONSPHERE_NERD001
    :status: proposed

    When ``HMD_LOCAL_NEURONSPHERE_MODE=deploy``, ``hmd neuronsphere up`` starts
    a local "admin" control plane before deploying any application services. This
    mirrors the cloud architecture where ``hmd-ms-deployment``, ``hmd-ms-naming``,
    and ``hmd-ms-artifact-lib`` run in a dedicated admin AWS account.

    **Control plane components (Docker Compose):**

    1. **Floci** (port 4566): AWS emulation for all deployed services.
    2. **PostgreSQL** (``hmd_db``): Shared database for deployment graph, naming
       service, and application services.
    3. **hmd-ms-naming**: Service discovery. Registers itself and all subsequently
       deployed services.
    4. **hmd-ms-deployment**: Deployment orchestration. Runs the same code as the
       cloud Lambda, but as a long-running Docker container.
    5. **nginx proxy** (``hmd_proxy``): Routes all traffic on port 80.

    **Startup sequence:**

    1. Start Floci, PostgreSQL, nginx, and ms-naming (Docker Compose).
    2. Wait for health checks.
    3. Start ms-deployment, which seeds its database on first boot.
    4. Start local override services from ``local_overrides.json`` (currently
       just JanusGraph for Neptune; see SPEC003).
    5. Seed the deployment graph from the platform BOM (see SPEC004, SPEC005).
    6. Build and apply a changeset from the BOM. This deploys the entire
       platform -- VPC, EKS cluster, S3 buckets, Redis, Airflow, Argo, Trino,
       ClickHouse, microservices, etc. -- through the DAG using Floci's
       CDKTF/Helm/Lambda targets (see SPEC005, SPEC006).

    **Local artifact resolution:**

    In the cloud, ``hmd deploy`` fetches build archives from
    ``hmd-ms-artifact-lib`` (S3-backed). Locally, two artifact sources are
    supported:

    - **Local code** (``HMD_REPO_HOME``): The ``hmd-img-projectbuilder``
      container mounts the repo source directory directly. The ``hmd deploy``
      handler detects ``HMD_ENVIRONMENT=local`` and skips the artifact-lib
      download, using the local source tree instead.
    - **Build archives**: Pre-built archives (from ``hmd-ms-artifact-lib`` or
      local ``hmd build`` output) are uploaded to Floci S3. The projectbuilder
      resolves them via a local artifact-lib stub or directly from S3.

.. spec:: Local override mapping for unsupported cloud services
    :id: HMD_CLI_NEURONSPHERE_NERD001_SPEC003
    :links: HMD_CLI_NEURONSPHERE_NERD001
    :status: proposed

    Because Floci supports nearly all AWS services that NeuronSphere depends on,
    **most RepoClasses deploy through the DAG unchanged** -- their CDKTF targets
    Floci's AWS API and their Helm charts deploy to Floci's EKS (k3s). Only
    services with no Floci equivalent need a local override.

    A **local override mapping** configuration identifies these exceptions. This
    is a single configuration file maintained in ``hmd-cli-neuronsphere`` --
    **not** a change to ``nsplugin.json``.

    **Mapping file: ``local_overrides.json``**

    .. code-block:: json

        {
            "hmd-inf-neptune": {
                "strategy": "compose_substitute",
                "plugin": "graph",
                "provides_services": ["graph-db"],
                "reason": "Floci does not emulate Neptune (graph DB)"
            }
        }

    **Currently, Neptune is the only override.** All other infrastructure
    (Redis, Airflow, Argo, OTel Collector, Trino, ClickHouse, Superset,
    Hive Metastore, S3, EKS) deploys via the same CDKTF/Helm commands targeting
    Floci.

    **Two strategies:**

    - ``"compose_substitute"``: Start the named plugin's ``compose_file`` before
      the DAG runs. The cloud dependency is satisfied by this local Docker
      service. In the current mapping, the ``graph`` plugin starts JanusGraph as
      a Gremlin-compatible substitute for Neptune.
    - ``"skip"``: Dependency is not applicable locally. The DAG node for this
      RepoClass is removed and its dependents are re-linked to the next
      available ancestor. (Not currently needed, but available for future use.)

    **How this works in practice:**

    When building the local deployment DAG, the system consults
    ``local_overrides.json``. For each RepoClass in the DAG:

    - If the RepoClass has a ``compose_substitute`` override, the substitute
      plugin's ``compose_file`` is started before DAG execution begins. The
      RepoClass node is removed from the DAG (it's already satisfied).
    - If the RepoClass has a ``skip`` override, its node is removed from the DAG.
    - **Otherwise (the common case)**: The RepoClass is deployed via the DAG
      using the ``hmd-img-projectbuilder`` container, the same as cloud.

    **Default behavior for unmapped RepoClasses:** Deploy via the DAG. If a
    deploy step fails because Floci doesn't support a particular API, it will
    surface as a clear error and can be added to ``local_overrides.json``.

    **No changes to nsplugin.json.** The existing ``dependencies.requires_plugins``
    and ``dependencies.requires_services`` fields continue to work as-is.

.. spec:: Versioned platform snapshot (BOM) for the base NeuronSphere
    :id: HMD_CLI_NEURONSPHERE_NERD001_SPEC004
    :links: HMD_CLI_NEURONSPHERE_NERD001
    :status: proposed

    Deploying the local NeuronSphere requires not just the plugin repos (like
    ``hmd-ms-transform`` and ``hmd-app-airflow``) but **their entire transitive
    dependency tree** -- EKS clusters, S3 buckets, Redis, ALBs, external secrets
    CRDs, and more. In the cloud, these are individually registered RepoClasses
    deployed through changesets. Locally, they must all be available as build
    artifacts.

    A **Platform BOM** (Bill of Materials) is a versioned snapshot that pins
    every RepoClass required to stand up the base NeuronSphere platform. It is
    the local equivalent of the cloud's ``DeploymentSet`` + ``ChangeSet``
    combination. The deployment service already has a ``DeployBomCreator``
    (``deploy_bom_creator.py``) that produces this exact structure for cloud
    environments.

    **BOM structure:**

    .. code-block:: json

        {
            "bom_version": "2026.04.1",
            "description": "NeuronSphere Base Platform",
            "repo_classes": [
                {
                    "repo_instance_name": "eks-base",
                    "repo_class_name": "hmd-inf-eks-cluster",
                    "repo_class_version": "0.7.0",
                    "deployment_id": "local",
                    "instance_configuration": {},
                    "dependencies": {
                        "base-vpc": "base-vpc",
                        "datadog-lambda": "datadog-lambda",
                        "neptune-db": "graph-db"
                    }
                },
                {
                    "repo_instance_name": "base-vpc",
                    "repo_class_name": "hmd-vpc",
                    "repo_class_version": "0.2.0",
                    "deployment_id": "local",
                    "instance_configuration": {}
                }
            ],
            "local_overrides": {
                "hmd-inf-neptune": "graph"
            }
        }

    **Full transitive dependency tree (required RepoClasses):**

    The base NeuronSphere platform requires ~28 RepoClasses when all
    dependencies are resolved transitively. These fall into three tiers:

    *Tier 1 -- Foundation (deployed first, no NeuronSphere dependencies):*

    - ``hmd-vpc`` -- VPC networking
    - ``hmd-inf-acm`` -- Certificate management
    - ``hmd-inf-neptune`` -- Graph database (override: JanusGraph)
    - ``hmd-database-account`` -- RDS credentials
    - ``hmd-inf-datadog-lambdas`` -- Observability (optional, can be skipped)

    *Tier 2 -- Kubernetes + Storage (depends on Tier 1):*

    - ``hmd-inf-eks-cluster`` -- EKS cluster (Floci k3s)
    - ``hmd-inf-eks-node-group`` -- Node groups
    - ``hmd-inf-eks-alb`` -- Application load balancer
    - ``hmd-inf-wafv2`` -- WAF rules
    - ``hmd-inf-ebs-csi-addon`` -- EBS storage driver
    - ``hmd-inf-efs``, ``hmd-inf-efs-eks-addon``, ``hmd-inf-eks-efs-storage``
      -- EFS storage
    - ``hmd-inf-ext-secrets-crds``, ``hmd-inf-ext-secrets`` -- Secrets operator
    - ``hmd-inf-s3bucket`` -- S3 buckets
    - ``hmd-inf-credentials`` -- Credential management
    - ``hmd-inf-api-gateway`` -- API Gateway

    *Tier 3 -- Applications + Services (depends on Tiers 1 and 2):*

    - ``hmd-inf-redis`` -- Redis cache
    - ``hmd-app-argo`` -- Argo Workflows
    - ``hmd-app-airflow`` -- Airflow orchestrator
    - ``hmd-inf-otel-collector`` -- OpenTelemetry
    - ``hmd-inf-clickhouse`` -- Analytics database
    - ``hmd-inf-trino``, ``hmd-inf-hive-metastore`` -- Query engine
    - ``hmd-inf-superset`` -- Dashboards
    - ``hmd-ms-naming`` -- Service discovery
    - ``hmd-ms-deployment`` -- Deployment orchestration
    - ``hmd-ms-transform`` -- Transform engine

    **BOM generation:**

    A new command ``hmd neuronsphere generate-bom`` produces the BOM by:

    1. Starting from the enabled nsplugins (leaf services).
    2. Walking their ``manifest.json`` ``deploy.dependencies`` transitively.
    3. For each RepoClass encountered, resolving the version from its
       ``meta-data/VERSION`` file and recording its ``deploy.dependencies``,
       ``deploy.commands``, and ``deploy.default_configuration``.
    4. Filtering through ``local_overrides.json`` to mark overridden services.
    5. Writing the BOM to ``meta-data/platform_bom.json`` (checked into
       ``hmd-cli-neuronsphere``).

    **BOM versioning:**

    The BOM is versioned with a date-based scheme (e.g., ``2026.04.1``) and
    checked into the ``hmd-cli-neuronsphere`` repository. Each release of
    ``hmd-cli-neuronsphere`` bundles a specific BOM version that is known to
    work together. This is analogous to how the cloud maintains a curated
    ``DeploymentSet`` with tested version combinations.

    **Build artifact bundling:**

    Each RepoClass in the BOM must have its build artifacts available locally.
    Two approaches:

    - **Pre-built archive bundle**: A single downloadable archive containing the
      build output (CDKTF stacks, Helm charts, Docker images) for every
      RepoClass in the BOM. This is produced by CI and uploaded to
      ``hmd-ms-artifact-lib``. The ``hmd neuronsphere up`` (Deploy mode) command
      downloads this bundle on first run and caches it in ``$HMD_HOME/.cache/``.
    - **Build from source**: If ``HMD_REPO_HOME`` contains the repo, the
      projectbuilder mounts the local source tree and builds from it. This is
      the development workflow.

    The pre-built bundle enables Deploy mode without requiring all ~28 repos
    to be cloned locally. Only repos being actively developed need to be present
    in ``HMD_REPO_HOME``.

.. spec:: Seed deployment graph and apply changeset from BOM
    :id: HMD_CLI_NEURONSPHERE_NERD001_SPEC005
    :links: HMD_CLI_NEURONSPHERE_NERD001
    :status: proposed

    Once the control plane is running, the BOM is used to seed the deployment
    graph and apply a changeset that brings up the full platform.

    **Step 1: Seed the deployment graph.**

    ``hmd neuronsphere seed-deployment`` (called automatically during Deploy mode
    startup) reads the BOM and creates deployment entities:

    - One ``Environment`` entity with ``type=local``.
    - One ``DeploymentSet`` named ``local-deployment-set``.
    - One ``RepoClass`` and ``RepoClassVersion`` per BOM entry.
    - Dependency relationships (``RepoClassVersionReqRepoClass``) from the
      BOM's ``dependencies`` fields.
    - Entries marked in ``local_overrides`` are registered but flagged as
      pre-satisfied (their compose substitute is already running).

    Seeding is idempotent -- running it again updates versions and
    configurations without duplicating entities.

    **Step 2: Build a ChangeSet from the BOM.**

    Every RepoClass in the BOM becomes a change entry in the changeset. The
    changeset structure is identical to what the cloud uses:

    .. code-block:: json

        {
            "repo_instance_name": "eks-base",
            "repo_class_name": "hmd-inf-eks-cluster",
            "repo_class_version": "0.7.0",
            "deployment_id": "local",
            "instance_configuration": {},
            "dependencies": {
                "base-vpc": "base-vpc",
                "datadog-lambda": "datadog-lambda",
                "neptune-db": "graph-db"
            }
        }

    **Step 3: Apply the ChangeSet to the local DeploymentSet.**

    This calls ``ms-deployment``'s ``apply_changeset`` API, which:

    - Creates ``RepoInstance`` entities (if new) in the local environment.
    - Creates ``RepoInstanceDeployment`` records with status ``DEPLOY_NEXT``.
    - Links instances via ``RepoInstanceReqRepoInstance`` dependency edges.
    - Calls ``deploy_change_set_deployment`` to trigger execution.

    **Step 4: Execute via LocalWorkflowRunner (see SPEC006).**

    The deployment DAG is built from the full dependency graph. The tiers
    described in SPEC004 naturally emerge from the DAG -- Tier 1 has no
    dependencies and deploys first, Tier 2 depends on Tier 1, etc. The
    ``LocalWorkflowRunner`` executes up to 4 nodes in parallel within each
    tier, matching Argo's behavior.

    **Step 5: Status tracking.**

    Each node reports ``DEPLOYED`` or ``FAILED`` back to ``ms-deployment`` via
    the same ``hmd deployment set-deployment-status`` callback used in the cloud.
    ``hmd neuronsphere up`` waits for the changeset to reach ``COMPLETED`` status
    and prints a summary.

.. spec:: LocalWorkflowRunner using hmd-img-projectbuilder
    :id: HMD_CLI_NEURONSPHERE_NERD001_SPEC006
    :links: HMD_CLI_NEURONSPHERE_NERD001
    :status: proposed

    A new module ``local_workflow_runner.py`` in ``hmd-ms-deployment`` replaces
    the Argo workflow submission path when ``HMD_ENVIRONMENT=local``.

    **Design:**

    - Reuses the existing ``DeploymentDag``, ``build_deployment_dag()``, and
      ``traverse_deployment_dag()`` functions unchanged.
    - Reuses ``DeployBase.deploy_node()`` which generates the same shell scripts
      (``hmd deploy --repo-name ... --repo-version ... --config-file ...``) that
      Argo would execute.
    - Instead of generating Argo Workflow YAML, the runner executes each node's
      script inside a ``hmd-img-projectbuilder`` Docker container via
      ``docker run``.
    - Uses ``concurrent.futures.ThreadPoolExecutor(max_workers=4)`` matching
      Argo's parallelism setting.
    - The DAG traversal in ``deployment_dag_traversal.py`` implements a
      level-by-level BFS that respects dependencies. The runner uses the same
      traversal but executes containers directly instead of generating YAML.

    **Container execution per DAG node:**

    .. code-block:: bash

        docker run --rm \
          --network neuronsphere_default \
          -e HMD_ENVIRONMENT=local \
          -e HMD_REGION=${HMD_REGION} \
          -e HMD_CUSTOMER_CODE=${HMD_CUSTOMER_CODE} \
          -e AWS_ENDPOINT_URL=http://floci:4566 \
          -e HMD_ARTIFACT_LIBRARIAN_URL=http://hmd_gateway/ms-artifact-lib \
          -v /var/run/docker.sock:/run/containerd/containerd.sock \
          -v ${HMD_REPO_HOME}/${repo_name}:/workspace \
          ${HMD_APP_IMAGE} \
          bash -c "${deploy_script}"

    The ``deploy_script`` is the exact output of ``DeployBase.deploy_node()``
    -- the same script Argo would run in a workflow pod.

    **Routing:** In ``deployment_manager.py``, the ``submit_workflow()`` method
    currently POSTs to the Argo API. When ``HMD_ENVIRONMENT=local``, it calls
    ``LocalWorkflowRunner.execute_dag()`` instead.

    **Why not Argo on Floci's EKS (k3s)?**

    - Resource cost: k3s + Argo + workflow pods adds 2-4 GB RAM on a laptop.
    - Startup time: k3s cluster creation + Argo install = 30-60 seconds minimum.
    - Debugging: Failures in k3s pods inside Floci inside Docker are very hard
      to diagnose.
    - The DAG is the contract, not Argo. ``build_deployment_dag()`` and
      ``traverse_deployment_dag()`` are the actual logic. Argo is one execution
      backend; a local Docker runner is another.

    **Why not Step Functions?**

    - Impedance mismatch: existing code generates Argo YAML, not Step Functions
      state machine JSON.
    - The DAG already exists in Python; adding container execution is ~150 lines
      vs ~500+ for Step Functions translation.
    - No benefit for local: Step Functions add network hops through Floci for no
      gain.

.. spec:: User-extensible local deployments
    :id: HMD_CLI_NEURONSPHERE_NERD001_SPEC007
    :links: HMD_CLI_NEURONSPHERE_NERD001
    :status: proposed

    After the base NeuronSphere environment is running in Deploy mode, developers
    can extend it by deploying their own RepoClasses. This follows the same flow
    as the cloud and enables a fast feedback loop for RepoClass development.

    **From local code (development workflow):**

    .. code-block:: bash

        # 1. Build the RepoClass locally
        cd ~/repos/hmd-ms-my-service
        hmd build

        # 2. Register the RepoClass (creates RepoClass + RepoClassVersion)
        hmd deployment register-repo-class \
          --repo-name hmd-ms-my-service \
          --repo-version 0.1.0

        # 3. Build a changeset
        hmd deployment create-changeset \
          --name "add-my-service" \
          --add hmd-ms-my-service:0.1.0:my-service

        # 4. Apply the changeset
        hmd deployment apply-changeset \
          --changeset "add-my-service" \
          --deployment-set "local-deployment-set"

    This triggers the same ``apply_changeset`` -> ``deploy_change_set_deployment``
    -> ``LocalWorkflowRunner`` flow. The projectbuilder container mounts the local
    source tree from ``HMD_REPO_HOME`` and executes ``hmd deploy``.

    **From a build archive (distributed workflow):**

    .. code-block:: bash

        # 1. Upload archive to local artifact-lib (Floci S3)
        hmd artifact-lib upload \
          --repo-name hmd-ms-my-service \
          --version 0.1.0 \
          --archive ./target/hmd-ms-my-service-0.1.0.tar.gz

        # 2-4. Same register/changeset/apply flow as above

    The projectbuilder container downloads the archive from Floci S3 via the
    local ``hmd-ms-artifact-lib`` stub (or directly from S3 if no stub is
    running).

    **This enables faster feedback for RepoClass development** because:

    - No cloud push required to test deployment logic.
    - The changeset/DAG/dependency flow is exercised locally.
    - ``hmd deploy`` handlers run in the real projectbuilder image.
    - Infrastructure repos (CDKTF/Terraform) target Floci instead of real AWS.

.. spec:: Preserve Legacy mode as the default
    :id: HMD_CLI_NEURONSPHERE_NERD001_SPEC008
    :links: HMD_CLI_NEURONSPHERE_NERD001
    :status: proposed

    ``hmd neuronsphere up`` continues to work exactly as it does today when
    ``HMD_LOCAL_NEURONSPHERE_MODE`` is unset or set to ``legacy``.

    **Legacy mode behavior (unchanged):**

    - Docker Compose starts all enabled plugins directly.
    - Floci (replacing MiniStack) provisions AWS resources.
    - The nsplugin system controls which services start via ``compose_file``.
    - No ``ms-deployment``, no changeset flow, no DAG.

    **Mode switching in ``start_neuronsphere()``:**

    .. code-block:: python

        mode = os.environ.get("HMD_LOCAL_NEURONSPHERE_MODE", "legacy")
        if mode == "deploy":
            start_neuronsphere_deploy(verbose=verbose)
        else:
            start_neuronsphere_legacy(verbose=verbose)

    The existing ``start_neuronsphere()`` logic becomes
    ``start_neuronsphere_legacy()``. The new ``start_neuronsphere_deploy()``
    implements the control plane startup, seeding, and changeset application.

    Users opt into Deploy mode explicitly via environment variable. There is no
    automatic migration. Legacy mode remains the default.

.. spec:: nsplugin.json unchanged -- both modes derive from the same definitions
    :id: HMD_CLI_NEURONSPHERE_NERD001_SPEC009
    :links: HMD_CLI_NEURONSPHERE_NERD001
    :status: proposed

    The ``nsplugin.json`` schema requires **no changes** for Deploy mode support.
    Both modes use the same plugin definitions differently:

    **Legacy mode** uses from each nsplugin:

    - ``compose_file``: Started via Docker Compose.
    - ``resources``: Registered in ms-naming and provisioned in Floci.
    - ``dependencies``: Validated at startup.
    - ``config``, ``config_mappings``, ``templates``, ``postgres_scripts``:
      Applied to the environment.

    **Deploy mode** uses from each nsplugin:

    - ``compose_file``: Started via Docker Compose **only for plugins listed
      in** ``local_overrides.json`` (currently just the ``graph`` plugin for
      Neptune). All other plugins are deployed via the DAG.
    - ``resources``: Used to provision resources in Floci and register services
      in ms-naming after DAG deployment completes.
    - ``dependencies``: Used alongside the ``manifest.json``
      ``deploy.dependencies`` for DAG construction.
    - ``config``, ``config_mappings``, ``templates``, ``postgres_scripts``:
      Applied to the environment before DAG execution.

    **The ``manifest.json`` is the source of truth for Deploy mode.** The
    ``deploy.dependencies`` and ``deploy.default_configuration`` sections drive
    the deployment graph. The ``nsplugin.json`` supplements this with
    local-specific resource declarations and configuration.

    **Determining deployability:** A plugin is deployable (participates in the
    DAG) if its associated repository's ``manifest.json`` has a
    ``deploy.commands`` section. Plugins without a manifest (or without deploy
    commands) are treated as local-only infrastructure and their ``compose_file``
    is always used.

.. spec:: Validate and evolve the nsplugin system
    :id: HMD_CLI_NEURONSPHERE_NERD001_SPEC010
    :links: HMD_CLI_NEURONSPHERE_NERD001
    :status: proposed

    The nsplugin system remains the source of truth for local service topology.

    **Plugin discovery in Deploy mode:**

    - The ``LocalPluginLoader`` adds a method ``get_deployable_plugins()`` that
      returns plugins whose associated ``manifest.json`` has ``deploy.commands``.
    - A method ``get_override_plugins()`` returns the small set of plugins
      referenced by ``local_overrides.json`` (currently just ``graph``).
    - Override plugins are started via Docker Compose before the DAG runs.
    - All other enabled plugins are deployed via the changeset/DAG flow.

    **Plugin discovery in Legacy mode:**

    - Unchanged. All enabled plugins with compose files are started.

    **Compatibility:** Existing plugins work in both modes without modification.
    The ``hmd neuronsphere validate-plugin`` command continues to work for all
    plugins. The ``hmd neuronsphere configure`` interactive menu works for both
    modes.

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
    local/cloud AWS endpoint switching. The ``ministack_deployer.py`` module
    already demonstrates this pattern via ``_get_client()``.

    **Deploy mode benefit:** Because Deploy mode uses the real ``ms-deployment``
    and ``ms-naming`` services, CLI tools like ``hmd deployment`` work against
    the local environment with zero code changes -- they just point to the local
    endpoint.

.. spec:: Phased migration strategy
    :id: HMD_CLI_NEURONSPHERE_NERD001_SPEC012
    :links: HMD_CLI_NEURONSPHERE_NERD001
    :status: proposed

    The migration is divided into five phases, each independently shippable.

    **Phase 0: Floci drop-in replacement for MiniStack**

    - Replace MiniStack Docker image with Floci in ``docker-compose.ministack.yml``.
    - Update health check endpoint and rename env var to
      ``HMD_LOCAL_NEURONSPHERE_ENABLE_FLOCI`` (with backwards-compat fallback).
    - Rename ``ministack_deployer.py`` to ``floci_deployer.py``.
    - Test all existing functionality (S3, DynamoDB, SQS, Lambda, API Gateway).
    - No user-facing behavioral changes. Legacy mode only.
    - Risk: Low. Same port, same AWS SDK interface. Rollback is trivial.

    **Phase 1: Platform BOM + admin control plane**

    - Implement ``hmd neuronsphere generate-bom`` to walk the transitive
      dependency tree and produce ``meta-data/platform_bom.json``.
    - Create ``docker-compose.admin.yml`` with Floci, PostgreSQL, ms-naming, and
      ms-deployment.
    - Create ``local_overrides.json`` for unsupported services (currently only
      Neptune -> JanusGraph).
    - Add ``HMD_LOCAL_NEURONSPHERE_MODE=deploy`` env var check to
      ``start_neuronsphere()``.
    - Implement ``start_neuronsphere_deploy()`` that starts the control plane,
      starts override services, and waits for health checks.
    - Implement ``hmd neuronsphere seed-deployment`` to populate the deployment
      graph from the BOM.
    - Risk: Medium. Requires ms-deployment to run as a Docker container
      and a correct BOM. The ``DeployBomCreator`` provides a reference
      implementation.

    **Phase 2: LocalWorkflowRunner + full platform deployment**

    - Add ``local_workflow_runner.py`` to ``hmd-ms-deployment``.
    - Route ``submit_workflow()`` to local runner when ``HMD_ENVIRONMENT=local``.
    - The runner executes ``hmd deploy`` commands inside ``hmd-img-projectbuilder``
      containers on the ``neuronsphere_default`` network.
    - Implement automatic changeset creation from the BOM during
      ``hmd neuronsphere up`` (Deploy mode).
    - Build the pre-built artifact bundle for the BOM (CI pipeline).
    - Test end-to-end deploying the full platform DAG: VPC -> EKS -> ALB ->
      services. Start with Tier 1 + 2 (infrastructure), then add Tier 3.
    - Risk: Medium-High. Full DAG is ~28 nodes. Must replicate Argo's dependency
      ordering exactly. CDKTF stacks must work against Floci. Helm charts must
      work on k3s. Mitigated by reusing ``deploy_logic.py`` and testing
      tier-by-tier.

    **Phase 3: User-extensible deployments**

    - Enable ``hmd deployment register-repo-class`` to work against local
      ms-deployment.
    - Enable ``hmd deployment create-changeset`` and ``apply-changeset`` locally.
    - Support mounting local source trees into projectbuilder containers.
    - Support uploading build archives to Floci S3 for distribution.
    - Risk: Medium. Individual repo deploy handlers may have cloud-specific
      assumptions. Start with 2-3 repos and iterate.

    **Phase 4: CLI tool convergence**

    - Add centralized boto3 session factory to ``hmd-cli-tools``.
    - Migrate individual repos incrementally to use the shared factory.
    - Add Floci-specific resource provisioning (Secrets Manager, IAM roles).
    - Risk: Low. Incremental, per-repo changes.

Per-Repository Changes
----------------------

**hmd-cli-neuronsphere** (primary changes):

- Rename ``ministack_deployer.py`` to ``floci_deployer.py``.
- Update ``docker-compose.ministack.yml`` to use Floci image.
- Add ``HMD_LOCAL_NEURONSPHERE_MODE`` handling to ``start_neuronsphere()``.
- Add ``start_neuronsphere_deploy()`` for Deploy mode startup.
- Add ``hmd neuronsphere seed-deployment`` and ``hmd neuronsphere generate-bom``
  commands.
- Add ``docker-compose.admin.yml`` for the control plane.
- Add ``meta-data/platform_bom.json`` (versioned, checked in).
- Add ``local_overrides.json`` for genuinely unsupported services (currently
  only Neptune).
- Add ``get_deployable_plugins()`` and ``get_override_plugins()`` to
  ``LocalPluginLoader``.

**hmd-ms-deployment** (new module + routing):

- Add ``local_workflow_runner.py`` alongside ``deploy_workflow_creator.py``.
- Add environment routing in ``deployment_manager.py`` ``submit_workflow()``:
  when ``HMD_ENVIRONMENT=local``, use ``LocalWorkflowRunner`` instead of Argo.
- Ensure ``nsplugin.json`` in ``src/local/`` is complete for local container
  startup.

**hmd-cli-tools** (shared utilities):

- Add centralized boto3 session factory with Floci endpoint override.
- Ensure ``ServiceManager`` / ms-naming client handles local URLs correctly.

**Individual service repos** (no changes required):

- ``nsplugin.json`` is unchanged.
- ``manifest.json`` is unchanged.
- ``hmd deploy`` handlers already use AWS SDK calls; they pick up the endpoint
  override from the shared session factory.

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
     - Floci maturity (1 month old)
     - Phase 0 uses only proven services (S3, DynamoDB, SQS, Lambda, API GW).
       Advanced services deferred to later phases after validation.
   * - Medium
     - LocalWorkflowRunner correctness
     - Reuses battle-tested ``deploy_logic.py`` and
       ``deployment_dag_traversal.py``. Same DAG, different executor.
   * - Medium
     - Deployment graph seeding accuracy
     - Derived from ``manifest.json`` (authoritative source) filtered through
       ``local_service_mapping.json``. Seeding is idempotent.
   * - Medium
     - projectbuilder image running locally
     - Large image (~2 GB with all tools). Docker socket access required.
       Already proven pattern from MiniStack Lambda deployment.
   * - Low
     - ``local_overrides.json`` maintenance
     - Only needed for genuinely unsupported services (currently just Neptune).
       Unmapped RepoClasses deploy via the DAG by default.
   * - Medium
     - CDKTF/Helm targeting Floci
     - CDKTF providers must accept Floci endpoints. Helm charts deploy to
       Floci's EKS (k3s). Most should work unchanged; edge cases surface as
       clear errors during Phase 2 testing.
   * - High
     - Floci EKS (k3s) fidelity for Helm deployments
     - k3s may not support all Kubernetes features used by Helm charts (e.g.,
       storage classes, ingress controllers, CRDs). Phase 2 tests with Airflow
       and Argo first. Charts may need local value overrides.
