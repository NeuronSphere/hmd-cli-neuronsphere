Operating Modes
===============

``hmd neuronsphere up`` supports two operating modes, controlled by the
``HMD_LOCAL_NEURONSPHERE_MODE`` environment variable. Both modes use the same
``nsplugin.json`` plugin definitions and the same CLI entrypoint.

Extend Mode (Default)
---------------------

Extend mode is the default. It brings up a **minimal core** that mirrors the
cloud control plane and deploys services through a DAG-based workflow, rather
than starting the whole platform as Docker Compose containers.

**The minimal core** — everything ``hmd neuronsphere up`` starts by default:

- a Docker network (``neuronsphere_default``);
- a single **Floci** instance emulating the AWS account (S3, DynamoDB, SQS,
  Lambda, API Gateway, IAM, ECR, EKS, Secrets Manager, RDS on port 4566);
- the core databases (PostgreSQL, with ``hmd_ms_naming`` / ``hmd_ms_deployment``
  created directly);
- a **k3s** cluster on Floci's EKS emulation;
- the **deployment control plane** — ``hmd-ms-deployment``, ``hmd-ms-naming``,
  and ``hmd-ms-dbaccount`` as Floci Lambdas behind the nginx proxy;
- the **graph database** (Neptune/JanusGraph), treated as foundational infra
  (opt out with ``HMD_LOCAL_NEURONSPHERE_ENABLE_GRAPH=false``).

Everything else — Airflow, Trino, Superset, ``hmd-ms-transform``, Jupyter,
ClickHouse, Hive Metastore, OTel/telemetry, MinIO, DynamoDB-standalone — is an
**optional plugin, off by default** (see `Local core vs optional plugins`_).

**How it works:**

1. The core containers (``db``, ``floci``, ``proxy``, plus ``graph``) start.
2. Floci provisions the core databases; the control-plane Lambdas deploy behind
   the API Gateway proxy.
3. On the **first** ``up`` (bootstrap), the base NERD0004 ResourceDefinition
   catalog is seeded (``seed_base_resource_definitions``) and the concrete local
   Resources — the Docker network and the k3s cluster — are submitted, tagged
   ``environment=local``, so cloud repos that declare a resource dependency
   resolve against the local environment (see `Cloud parity via NERD0004`_).
4. A ``LocalWorkflowRunner`` executes any deployment DAG using
   ``hmd-img-projectbuilder`` containers, running the same ``hmd deploy``
   commands used by Argo Workflows in the cloud. CDKTF targets Floci's AWS APIs;
   Helm deploys to Floci's EKS (k3s).
5. A subsequent ``down`` then ``up`` takes a **restart fast-path** — the
   persistent Floci/PostgreSQL state is reused and the BOM/DAG is not re-run.

**To use Extend mode** (no action required -- it is the default):

.. code-block:: bash

    hmd neuronsphere up

.. note::

   For backwards compatibility, ``HMD_LOCAL_NEURONSPHERE_MODE=deploy`` is
   still accepted and maps to Extend mode.

Local core vs optional plugins
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

The core above is always on. Every other app/infra service is opt-in and
enabled per user, two ways:

- **Env flag:** ``export HMD_LOCAL_NEURONSPHERE_ENABLE_<NAME>=true`` (e.g.
  ``HMD_LOCAL_NEURONSPHERE_ENABLE_TRINO=true``) before ``hmd neuronsphere up``.
- **Interactive:** ``hmd neuronsphere configure`` lists the optional plugins,
  persists the choices to ``$HMD_HOME/.config/hmd.env``, and shows the always-on
  core.

Cloud parity via NERD0004
~~~~~~~~~~~~~~~~~~~~~~~~~~~

Because ``hmd-ms-deployment`` implements NERD0004 (Resources), local ``up``
records the things it actually created as concrete **Resources** typed by the
base catalog (``network.neuronsphere.io/docker-network``,
``kubernetes.neuronsphere.io/kubernetes-cluster``,
``compute.neuronsphere.io/compute-node``).

All local default/core Resources are owned by a single RepoClass —
``hmd-cli-neuronsphere`` itself, the thing that bootstraps them. It is seeded as
a ``local-neuronsphere`` instance (deployed via the ``skip`` strategy) that
``apply_changeset`` creates as a real environment **producer**, declared to
produce the core types before the changeset applies. A cloud repo whose
``manifest.json`` declares a ``resource`` dependency on those supertypes then
resolves against ``local-neuronsphere`` — the same RepoClass deploys against a
real VPC/EKS in the cloud and against the Docker network / k3s locally without
changing its dependency. Discover the Resources with
``GET /apiop/find_resources_by_tag/environment/local``.

.. note::

   A dependency role may carry **both** a cloud ``repo_class_name`` (a
   suggestion) and an authoritative ``resource`` block. Locally, where the cloud
   RepoClass (e.g. ``hmd-inf-eks-cluster``) isn't registered, ``hmd-ms-deployment``
   silently ignores the suggestion and resolves the ``resource`` against the
   local producer — so one shared manifest works in both environments.

Two-phase changeset bootstrap
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

``hmd neuronsphere up`` applies **two** separate ChangeSets rather than one:

- **Phase A** — a changeset containing only the ``local-neuronsphere`` instance.
  Once applied, its concrete Resources are submitted: the Docker network, k3s
  cluster/compute/ingress-controller, the shared Postgres, JanusGraph, and an
  ``application.neuronsphere.io/microservice`` Resource for each of the
  bootstrapped-before-ms-deployment-exists HMDMS Lambdas
  (``hmd-ms-deployment``, ``hmd-ms-naming``, ``hmd-ms-dbaccount``,
  ``hmd-ms-artifact-lib``) — these four are never registered as
  ``RepoClass``/``RepoInstance`` entities themselves (their own manifests'
  required deps, e.g. a real VPC/Argo/Datadog, will never resolve locally); what
  they *provide* is represented purely as this shared Resource type.
- **Phase B** — a second changeset with everything else: ext-secrets, the
  built-in/custom BOM, and every plugin-contributed entry.

Submitting Phase A's Resources *before* Phase B's changeset validates means a
Phase-B dependency on one of those Resources can use a real
``tag_selector`` (e.g. ``"repo_class=hmd-ms-dbaccount"``) to pick the specific
microservice it needs, instead of accepting any producer of the shared type.

External Secrets local dev-deploy loop
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

``hmd neuronsphere up`` deploys ``hmd-inf-ext-secrets-crds`` then
``hmd-inf-ext-secrets`` onto the local k3s cluster through
``hmd-img-projectbuilder`` by default — the same tool and ``hmd deploy`` path
used in the cloud. Their ``eks-cluster`` / ``compute`` dependencies resolve
against the ``local-neuronsphere`` producer via their manifests' SPEC0008 ``resource``
blocks, and each repo's produced Resource output (rendered by
``src/helm/templates/resource-outputs.yaml`` and submitted by ``hmd deploy``)
is tracked in ``hmd-ms-deployment``:

.. code-block:: bash

   hmd neuronsphere up
   # then, against http://localhost/hmd_ms_deployment
   #   GET /apiop/get_deployment_resources/<ext-secrets rid>
   #       -> the external-secrets-operator Resource + outputs
   #   GET /apiop/list_resource_definitions?resource_namespace=external-secrets.neuronsphere.io

Opt out with ``HMD_LOCAL_NEURONSPHERE_ENABLE_EXT_SECRETS=false`` before
``hmd neuronsphere up`` if you don't need the ``ExternalSecret`` stack locally.

``hmd-inf-ext-secrets`` ships a Floci-safe ``src/local/cdktf`` overlay so its
AWS-only IRSA/IAM step is a no-op locally while the Helm install still runs.

Platform Mode
-------------

Platform mode is the legacy compose-everything path: it starts all enabled
plugins as Docker Compose services. It is no longer the default; prefer Extend
mode with opt-in plugins. Select it explicitly:

.. code-block:: bash

    export HMD_LOCAL_NEURONSPHERE_MODE=platform
    hmd neuronsphere up

.. note::

   For backwards compatibility, ``HMD_LOCAL_NEURONSPHERE_MODE=legacy`` is
   still accepted and maps to Platform mode.

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

Floci Lambda Image Resolution
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Floci spawns Lambda containers through the mounted host ``docker.sock``,
so any image already in the host's Docker image cache is reusable
directly. ``floci_deployer._ensure_local_image()`` does **not** push to
Floci's bundled ECR sidecar — Floci's Lambda runner pulls (or reuses)
the image via the host daemon using the original URI.

This avoids two problems with Floci 1.5.8's bundled ECR sidecar:

- The sidecar runs on the Docker bridge network with no host port
  publish, so a host-side ``docker push`` cannot reach it.
- Its default port (5000) collides with macOS AirPlay Receiver, which
  intercepts requests with ``403 Forbidden``.

If you need an image that isn't yet on the host, ``hmd build`` it (or
``docker pull`` it) before ``hmd neuronsphere up``.

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
