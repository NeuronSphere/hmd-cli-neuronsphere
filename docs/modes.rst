Operating Modes
===============

``hmd neuronsphere up`` supports two operating modes, controlled by the
``HMD_LOCAL_NEURONSPHERE_MODE`` environment variable. Both modes use the same
``nsplugin.json`` plugin definitions and the same CLI entrypoint.

Legacy Mode (Default)
---------------------

Legacy mode is the default. It starts all enabled plugins as Docker Compose
services and uses **Floci** for AWS emulation (S3, DynamoDB, SQS, Lambda, API
Gateway).

**How it works:**

1. Plugins are discovered from entry points and ``HMD_REPO_HOME``.
2. Each enabled plugin's ``compose_file`` is rendered and composed together.
3. ``docker compose up`` starts all services.
4. Floci provisions AWS resources declared by plugins (queues, buckets, tables).
5. Services with ``deploy_as_lambda`` are deployed as Lambda functions behind
   Floci's API Gateway.
6. Services are registered with ``ms-naming`` for discovery.

**To use Legacy mode** (no action required -- it is the default):

.. code-block:: bash

    hmd neuronsphere up

Or explicitly:

.. code-block:: bash

    export HMD_LOCAL_NEURONSPHERE_MODE=legacy
    hmd neuronsphere up

Deploy Mode (Future)
--------------------

Deploy mode is under development and will be available in a future release.
When complete, it will start an admin control plane and deploy services
through a DAG-based workflow, mirroring the cloud deployment architecture.

**Planned architecture:**

- An admin control plane (Floci, ``hmd-ms-deployment``, ``hmd-ms-naming``,
  PostgreSQL) starts first.
- A Platform BOM (Bill of Materials) defines all required RepoClasses.
- A ``LocalWorkflowRunner`` executes the deployment DAG using
  ``hmd-img-projectbuilder`` containers, running the same ``hmd deploy``
  commands used by Argo Workflows in the cloud.
- CDKTF targets Floci's AWS APIs. Helm deploys to Floci's EKS (k3s).
- Only genuinely unsupported services (e.g., Neptune) use a Docker Compose
  substitute.

**To try Deploy mode** (currently prints a stub message):

.. code-block:: bash

    export HMD_LOCAL_NEURONSPHERE_MODE=deploy
    hmd neuronsphere up

See :doc:`proposals/NERD001_Floci_Local_Architecture` for the full
specification.

Floci (AWS Emulator)
--------------------

Floci is a free, MIT-licensed AWS emulator that provides 34+ AWS services
on a single port (4566). It replaced MiniStack starting in version 0.5.

**Key capabilities:**

- S3, DynamoDB, SQS, SNS, Lambda, API Gateway (v1 & v2)
- Step Functions, Secrets Manager, IAM (real policy evaluation)
- ECS, EKS (k3s clusters), RDS (Postgres/MySQL)
- CloudFormation, EventBridge, Kinesis, Glue Data Catalog
- 24 ms startup, ~90 MB Docker image

**Configuration:**

The Floci container is configured via environment variables:

- ``FLOCI_SERVICES``: Comma-separated list of services to enable.
- ``FLOCI_STORAGE_MODE``: Set to ``persistent`` for data to survive restarts.
- ``FLOCI_REGION``: AWS region (default: ``us-west-2``).

Data is persisted to ``$HMD_HOME/floci/data/`` when persistence is enabled.

Migrating from MiniStack
-------------------------

Floci is a drop-in replacement for MiniStack. The following environment
variables have been renamed but the old names are still accepted for
backwards compatibility:

.. list-table::
   :header-rows: 1
   :widths: 40 40 20

   * - Old Variable
     - New Variable
     - Notes
   * - ``HMD_LOCAL_NEURONSPHERE_ENABLE_MINISTACK``
     - ``HMD_LOCAL_NEURONSPHERE_ENABLE_FLOCI``
     - Enable/disable the AWS emulator plugin
   * - ``MINISTACK_ENDPOINT``
     - ``FLOCI_ENDPOINT``
     - Override the emulator endpoint URL

Both Floci and MiniStack expose port 4566 and use the standard AWS SDK wire
protocol, so all ``boto3`` / ``aws-sdk`` calls work unchanged.
