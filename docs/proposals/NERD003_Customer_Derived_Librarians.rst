.. NERD003 Customer-Derived Librarians on Local NeuronSphere

NERD003 Customer-Derived Librarians on Local NeuronSphere
=========================================================

.. req:: Run customer-derived librarians as first-class HMDMS plugins on the local stack
    :id: HMD_CLI_NEURONSPHERE_NERD003
    :status: proposed

    The local NeuronSphere shall support running a *customer-derived
    librarian* -- a microservice whose Docker image inherits from
    ``hmd-ms-librarian`` (or one of its subclasses such as
    ``hmd-ms-device-lib``) but is owned by an external customer repository
    with its own repo class, language pack(s), ``content_item_type_config``,
    ``content_path_configs``, S3 bucket, and DynamoDB table.

    The customer's repo declares itself via ``src/local/nsplugin.json``
    following the same contract as ``hmd-ms-artifact-lib``. ``hmd
    neuronsphere up`` then (a) deploys the librarian as a Floci Lambda using
    the locally built ``<repo-name>:<VERSION>`` image, (b) provisions its S3
    bucket inside Floci, (c) points it at the shared local JanusGraph and at
    Floci's DynamoDB (where the librarian creates its own table on first
    request), (d) optionally mounts customer-owned language packs and
    content adaptation repos from ``$HMD_REPO_HOME`` so iteration on entity
    schemas and path configs does not require image rebuilds, and (e) seeds
    the customer's repo class into ``hmd-ms-deployment`` as DEPLOYED.

    All five behaviors must work without any code changes inside
    ``hmd-cli-neuronsphere`` for a new customer librarian -- the contract
    is fully data-driven from the customer's manifest and nsplugin files.

Motivation
----------

NeuronSphere ships several first-party librarian microservices --
``hmd-ms-artifact-lib`` (build artifacts), ``hmd-ms-device-lib`` (device run
data) -- all derived from the ``hmd-ms-librarian`` base. Customers
frequently fork one of these to build their own domain-specific librarian:

- A device-OEM running a manufacturing-floor NeuronSphere needs a
  *manufacturing librarian* with content types for serialized assemblies,
  test fixtures, and process steps.
- A medical device customer needs a *device librarian* whose content paths
  are organized around their internal trial structure rather than the
  generic ``device/run/category`` hierarchy ``hmd-ms-device-lib`` ships.

The customer's librarian:

- Inherits the librarian image (``FROM ghcr.io/hmdlabs/hmd-ms-device-lib:<ver>``)
- Defines a custom language pack (``acme-lang-device-librarian``) on top of
  ``hmd-lang-librarian`` / ``hmd-lang-device-librarian``
- Defines a custom ``content_item_type_config`` (a YAML/JSON repo such as
  ``acme-config-device-cit``)
- Defines its own ``content_path_configs`` describing how content items are
  filed in the graph
- Has its own repo class (``acme-ms-device-lib``), DynamoDB table
  (``acme-ms-device-lib-librarian``), and S3 bucket (``acme-device-content``)

Today, only ``hmd-ms-artifact-lib`` ships a working ``src/local/nsplugin.json``.
Even ``hmd-ms-device-lib`` lacks one. There is no documented contract for what
a derived-librarian repo must contain, no canonical example of mounting custom
language packs and config repos from the local filesystem, and no command for
fast iteration on content type and path config edits. This NERD closes those
gaps.

Why librarians are a special case
---------------------------------

Ordinary HMDMS services need a Lambda runtime and (sometimes) one or two AWS
resources. Librarians need three coordinated pieces of state, two of which
are created lazily by the librarian process itself:

1. **An S3 bucket per librarian** -- content storage. Provisioned inside
   Floci by ``hmd neuronsphere up`` from the ``hmdms_service.buckets[]``
   declaration.
2. **A DynamoDB table per librarian** -- entity persistence. Created by the
   librarian itself on first request via ``hmd-ms-base``'s dynamo engine.
   The table lives **inside Floci's DynamoDB service** at
   ``http://floci:4566`` -- there is no separate dynamodb container in the
   local stack.
3. **A connection to the shared graph database** -- ``global-graph``
   (JanusGraph locally, Neptune in the cloud). Vertex labels, edge labels,
   and indexes are created on first request by the librarian via
   ``hmd-ms-base``'s gremlin engine.

The librarian *cannot* be brought up correctly by simply provisioning Lambda
+ S3 + DynamoDB + graph-DB resources. It also needs:

- The right language pack(s) on its Python ``sys.path`` so its
  ``service_loader`` can resolve entity classes.
- The right ``content_item_type_config`` artifact so it knows what types of
  content items are valid.
- The right ``content_path_configs`` so it knows how to file new content in
  the graph.

These three artifacts are exactly what a customer wants to iterate on. This
NERD specifies how they are delivered.

Reference: today's ``artifact-lib`` path
----------------------------------------

The mechanics for running a librarian-shaped HMDMS service locally already
exist. ``hmd-ms-artifact-lib/src/local/nsplugin.json`` is consumed by
``LocalPluginLoader.get_hmdms_lambda_spec``
(``loaders/local_plugin_loader.py:533``) and seeded into ``ms-deployment`` by
``hmdms_seeder.seed_hmdms_services`` (``hmdms_seeder.py:98``). Each declared
S3 bucket is provisioned in Floci and registered as a DEPLOYED
``hmd-inf-s3bucket`` instance. Manifest dependencies that aren't locally
deployed (``hmd-inf-datadog-lambdas``, ``hmd-inf-opa-authorizer``) are
SKIPPED-mocked so dependency resolution succeeds.

The work in this NERD is to (a) make that contract explicit and copy-pasteable
for customer repos, (b) extend the contract with a best-effort
``HMD_REPO_HOME``-based mount for language packs and config artifacts, and
(c) add a single CLI command for fast cold-restart iteration.

User workflow
-------------

A customer building ``acme-ms-device-lib``:

.. code-block:: bash

    # 1. Customer's repo lives in their own workspace
    cd ~/work/acme-ms-device-lib
    hmd build                    # produces acme-ms-device-lib:0.1

    # 2. Optionally clone the language pack(s) the librarian uses
    cd $HMD_REPO_HOME
    git clone git@github.com/acme/acme-lang-device-librarian.git

    # 3. Tell the local CLI where to find the librarian repo
    export HMD_LOCAL_PLUGINS=~/work/acme-ms-device-lib

    # 4. Bring up the local stack
    hmd neuronsphere up

    # 5. Iterate: edit content types, path configs, or lang pack source
    vi $HMD_REPO_HOME/acme-lang-device-librarian/.../entities.json
    hmd neuronsphere restart acme-device-lib

    # 6. Exercise the librarian
    curl -X PUT http://localhost/hmd_ms_acme_device_lib/api/content_item ...

If the customer has not cloned ``acme-lang-device-librarian``, step 2 is
skipped and the librarian uses the language pack already baked into its
Docker image -- no error, no warning.

.. spec:: Customer librarian repo contract
    :id: HMD_CLI_NEURONSPHERE_NERD003_SPEC001
    :links: HMD_CLI_NEURONSPHERE_NERD003
    :status: proposed

    A customer's derived-librarian repo declares itself as a local NeuronSphere
    HMDMS plugin by shipping the four files below. The contract is fully
    data-driven -- ``hmd-cli-neuronsphere`` requires no per-customer code.

    **(a)** ``meta-data/manifest.json`` declaring repo identity and cloud
    deployment dependencies. The local CLI reads ``name`` and the
    ``deploy.dependencies`` block (to seed SKIPPED-mocked deployments) and
    ``deploy.default_configuration.service_config`` (to merge into the
    Lambda's ``SERVICE_CONFIG`` env var).

    .. code-block:: json

        {
          "name": "acme-ms-device-lib",
          "project_type": {"name": "microservice"},
          "docker": {"build": {"context_dir": "./"}},
          "build": {"commands": [["docker"], ["cdktf"]]},
          "deploy": {
            "register_service": {"http": true, "lambda": true},
            "commands": [["docker"], ["cdktf"]],
            "dependencies": {
              "neptune-db": {
                "instance_name": "global-graph",
                "repo_class_name": "hmd-inf-neptune",
                "required": "true",
                "version_spec": "~= 0.1.7"
              },
              "lib-repo": {
                "repo_class_name": "hmd-inf-s3bucket",
                "required": "true",
                "version_spec": "~= 0.1.8"
              },
              "logging": {
                "repo_class_name": "hmd-inf-api-gateway",
                "required": "true",
                "version_spec": "~= 0.1.7"
              }
            },
            "default_configuration": {
              "service_config": {
                "loader_config": {
                  "local": [
                    "hmd-lang-librarian",
                    "hmd-lang-device-librarian",
                    "acme-lang-device-librarian"
                  ]
                },
                "service_loader": "local",
                "operations_modules": [
                  "hmd_ms_base.crud_operations",
                  "hmd_ms_librarian.hmd_ms_librarian"
                ],
                "hmd_db_engines": {
                  "dynamo": {
                    "engine_type": "dynamo",
                    "engine_config": {"point_in_time_recovery": "enabled"}
                  },
                  "graph": {
                    "engine_type": "gremlin",
                    "engine_config": {"db_host": "dependency:neptune-db"}
                  }
                },
                "hmd_entity_config": {
                  "__default__": {"persistence": ["dynamo", "graph"]}
                }
              }
            }
          }
        }

    **(b)** ``meta-data/config_local.json`` carrying local-only overrides.
    Critical fields: a per-librarian ``dynamo_table`` name, the literal local
    graph host (``global-graph``), and the customer's content type and path
    configs.

    .. code-block:: json

        {
          "content_item_type_config": "acme-config-device-cit@0.1.0",
          "content_path_configs": {
            "asm_run": [
              {
                "path": "/hmd_lang_librarian.ns_day@iso_date:local/acme_lang_manufacturing.assembly@serial:local/acme_lang_manufacturing.test_run@run_id:local",
                "relationships": [
                  "hmd_lang_librarian.content_item_has_ns_day:local",
                  "acme_lang_manufacturing.content_item_has_assembly:local"
                ]
              }
            ]
          },
          "service_config": {
            "loader_config": {
              "local": [
                "hmd-lang-librarian",
                "hmd-lang-device-librarian",
                "acme-lang-device-librarian"
              ]
            },
            "hmd_db_engines": {
              "dynamo": {
                "engine_type": "dynamo",
                "engine_config": {
                  "dynamo_table": "acme-ms-device-lib-librarian"
                }
              },
              "graph": {
                "engine_type": "gremlin",
                "engine_config": {
                  "db_host": "global-graph",
                  "db_protocol": "ws",
                  "with_strategies": false
                }
              }
            },
            "hmd_entity_config": {
              "__default__": {"persistence": ["dynamo", "graph"]}
            }
          }
        }

    **Do not** set ``dynamo_url``. See SPEC004.

    **(c)** ``src/local/nsplugin.json`` declaring the HMDMS service block.

    .. code-block:: json

        {
          "plugin_name": "acme-device-lib",
          "hmdms_service": {
            "lambda_name": "hmd_ms_acme_device_lib",
            "repo_class_name": "acme-ms-device-lib",
            "buckets": [
              {"name": "acme-device-content", "env_var": "DEVICE_BUCKET"}
            ],
            "repo_mounts": [
              "acme-lang-device-librarian",
              "acme-config-device-cit"
            ]
          },
          "resources": {
            "services": [
              {"name": "ms-acme-device-lib",
               "url": "http://hmd_proxy/hmd_ms_acme_device_lib/"}
            ]
          },
          "dependencies": {
            "requires_plugins": [],
            "requires_services": ["graph-db"]
          },
          "env_var_override": "HMD_LOCAL_NEURONSPHERE_ENABLE_ACME_DEVICE_LIB",
          "enabled_by_default": true
        }

    **(d)** ``src/docker/Dockerfile`` inheriting the librarian base image and
    installing any language packs the customer wants baked in (so the
    librarian still works even if the developer hasn't cloned the lang
    repo locally -- see SPEC002).

    .. code-block:: docker

        FROM ghcr.io/hmdlabs/hmd-ms-device-lib:0.5.467

        COPY src/python /tmp/acme
        RUN pip install /tmp/acme

    The local image tag is ``acme-ms-device-lib:<VERSION>`` produced by
    ``hmd build`` -- matching the convention ``LocalPluginLoader`` already
    expects.

.. spec:: Best-effort mounting of language packs and config repos from HMD_REPO_HOME
    :id: HMD_CLI_NEURONSPHERE_NERD003_SPEC002
    :links: HMD_CLI_NEURONSPHERE_NERD003
    :status: proposed

    Per NeuronSphere convention, language packs (``hmd-lang-*``,
    ``acme-lang-*``) and content adaptation/config repos (``hmd-config-*``,
    ``acme-config-*``) live as separate repos alongside the librarian under
    ``$HMD_REPO_HOME``, not bundled into the librarian repo. When a developer
    has those sibling repos checked out locally, the librarian Lambda mounts
    the source trees directly so edits take effect on the next librarian
    start. When the developer does not have them, the librarian falls back
    silently to the copy already baked into its Docker image.

    **Mount declaration.** ``nsplugin.json`` extends the ``hmdms_service``
    block with an optional ``repo_mounts[]`` field listing sibling repos by
    name:

    .. code-block:: json

        "hmdms_service": {
          "lambda_name": "hmd_ms_acme_device_lib",
          "repo_class_name": "acme-ms-device-lib",
          "buckets": [...],
          "repo_mounts": [
            "acme-lang-device-librarian",
            "acme-config-device-cit"
          ]
        }

    **Resolution rule.** For each name, the CLI looks up
    ``$HMD_REPO_HOME/<name>`` and resolves the importable subdirectory using
    the standard NeuronSphere layout (``src/python/<package>`` for Python
    language packs; ``./`` for config repos that ship YAML/JSON
    artifacts). The exact subpath rule and an optional override field are
    open questions for the implementation PR -- this NERD specifies the
    contract intent only.

    **Best-effort semantics (REQUIRED).** Mounts MUST be best-effort:

    - If ``$HMD_REPO_HOME/<name>`` exists, the resolved subdirectory is
      added to the Lambda container as a bind mount on a path that is
      already on the librarian's ``sys.path`` (e.g. inserted ahead of the
      ``pip install``-ed copy so host edits win).
    - If ``$HMD_REPO_HOME/<name>`` does not exist, log a single
      *debug-level* line (``"<name> not found in HMD_REPO_HOME, using
      bundled copy"``) and skip. Do not error. Do not warn.
    - The bundled copy in the librarian image (installed by the customer's
      Dockerfile, possibly inherited from the base librarian image) is the
      always-available fallback.

    **Why best-effort.** A developer may not have every dependent repo
    cloned locally. They may simply want to run their librarian against the
    pre-baked language packs that came with their image. Forcing them to
    clone every ``hmd-lang-*`` ancestor would be a needless friction
    barrier. The mount is purely an iteration accelerator, not a
    dependency.

    **Loader transparency.** The customer's
    ``service_config.loader_config.local`` lists the language pack module
    names. ``hmd-ms-base``'s loader resolves them from ``sys.path``
    regardless of whether the module came from a host bind-mount or from a
    baked-in ``pip install``.

    **Implementation surface.** ``LocalPluginLoader.get_hmdms_lambda_spec``
    is extended to emit a ``mounts: [{host_path, container_path}, ...]``
    list on the spec, populated only with repos that exist on disk.
    ``floci_deployer`` is extended to honor those mounts when calling
    Floci's Lambda ``create_function``. The implementation work is deferred
    to a follow-up PR; this NERD specifies the contract.

.. spec:: Per-librarian S3 bucket auto-provisioning
    :id: HMD_CLI_NEURONSPHERE_NERD003_SPEC003
    :links: HMD_CLI_NEURONSPHERE_NERD003
    :status: proposed

    Each S3 bucket the librarian needs is declared in
    ``hmdms_service.buckets[]`` as a ``{name, env_var}`` pair. The bucket
    name is customer-owned -- it must be globally unique inside Floci's S3
    namespace, and an ``acme-`` prefix is recommended to avoid collisions
    with first-party plugins. The env var is customer-chosen.

    On ``hmd neuronsphere up``:

    1. ``floci_deployer.provision_resources`` creates the bucket in Floci
       (already implemented; no change required).
    2. ``hmdms_seeder._seed_instance_and_deployment`` registers the bucket
       as a DEPLOYED ``hmd-inf-s3bucket`` instance in ``ms-deployment``
       (``hmdms_seeder.py:139``).
    3. The bucket's env var is exported to the librarian Lambda as
       ``<env_var>=s3://<bucket-name>``, and to *every other enabled HMDMS
       plugin* via the existing cross-plugin injection in
       ``plugins/transform.py:204``. This is what makes
       ``hmd-ms-transform`` automatically able to write to a customer's
       librarian bucket without per-customer wiring.

    Customer librarians get this behavior for free by declaring buckets in
    ``nsplugin.json``.

.. spec:: DynamoDB table on first start (in Floci)
    :id: HMD_CLI_NEURONSPHERE_NERD003_SPEC004
    :links: HMD_CLI_NEURONSPHERE_NERD003
    :status: proposed

    The librarian creates its own DynamoDB table on the first request via
    ``hmd-ms-base``'s dynamo engine. Locally that table lives **inside
    Floci's DynamoDB service** at ``http://floci:4566``. There is no
    separate ``dynamodb`` container, no LocalStack-style dynamodb-local on
    port 8000.

    Contract:

    - ``hmd-cli-neuronsphere`` does NOT pre-create the table.
    - The librarian's ``meta-data/config_local.json`` MUST set
      ``hmd_db_engines.dynamo.engine_config.dynamo_table`` to a name
      unique to that librarian. The convention is
      ``<repo-name>-librarian`` (e.g. ``acme-ms-device-lib-librarian``).
    - The librarian's config MUST NOT set ``dynamo_url``. The standard
      HMDMS-base env block injected by
      ``LocalPluginLoader.get_hmdms_lambda_spec``
      (``loaders/local_plugin_loader.py:616``) already provides
      ``AWS_ENDPOINT_URL=http://floci:4566``. boto3's dynamodb client
      picks that up automatically and routes to Floci.

    *Cleanup note (covered in SPEC009):*
    ``hmd-ms-device-lib/meta-data/config_local.json`` currently carries a
    legacy ``dynamo_url: "http://dynamodb:8000/"`` from the pre-Floci
    MiniStack era. That key is stale and must be removed when device-lib
    receives its ``src/local/nsplugin.json``.

.. spec:: Shared graph database on first start
    :id: HMD_CLI_NEURONSPHERE_NERD003_SPEC005
    :links: HMD_CLI_NEURONSPHERE_NERD003
    :status: proposed

    All librarians on the local stack share the single ``global-graph``
    JanusGraph instance brought up by core nsplugins. The customer's
    ``meta-data/config_local.json`` sets:

    .. code-block:: json

        "graph": {
          "engine_type": "gremlin",
          "engine_config": {
            "db_host": "global-graph",
            "db_protocol": "ws",
            "with_strategies": false
          }
        }

    On the librarian's first request, ``hmd-ms-base``'s gremlin engine
    creates the vertex labels, edge labels, and indexes implied by the
    librarian's loaded language packs. Customers do not provision graph
    infrastructure; they only declare the dependency in ``manifest.json``
    (``hmd-inf-neptune`` with ``instance_name: global-graph``) for cloud
    parity. ``hmdms_seeder`` treats that dependency as locally deployed
    (the global graph is real, not mocked) so dependency resolution
    succeeds.

.. spec:: Plugin discovery for customer repos outside the standard projects directory
    :id: HMD_CLI_NEURONSPHERE_NERD003_SPEC006
    :links: HMD_CLI_NEURONSPHERE_NERD003
    :status: proposed

    Customer librarian repos commonly live outside ``$HMD_REPO_HOME`` (in a
    customer-internal workspace or product monorepo). The existing
    ``HMD_LOCAL_PLUGINS`` colon-separated path list
    (``loaders/local_plugin_loader.py:113``) handles this case unchanged:

    .. code-block:: bash

        export HMD_LOCAL_PLUGINS=~/work/acme-ms-device-lib

    Repos listed via ``HMD_LOCAL_PLUGINS`` are auto-enabled --
    ``LocalPluginLoader.is_plugin_enabled`` returns true for them without
    requiring the per-plugin ``HMD_LOCAL_NEURONSPHERE_ENABLE_*`` env var
    that scanned plugins need (``local_plugin_loader.py:179``).

    Repos cloned into ``$HMD_REPO_HOME`` work too, via the
    ``HMD_LOCAL_PLUGINS_SCAN_REPO_HOME=true`` scan path
    (``local_plugin_loader.py:128``); these need the per-plugin enable
    env var. No code changes required for either path.

.. spec:: Repo class registration in ms-deployment
    :id: HMD_CLI_NEURONSPHERE_NERD003_SPEC007
    :links: HMD_CLI_NEURONSPHERE_NERD003
    :status: proposed

    On ``hmd neuronsphere up``, ``hmdms_seeder.seed_hmdms_services``
    (``hmdms_seeder.py:98``) iterates every enabled HMDMS plugin and:

    1. Calls ``add_repo_class_version`` on ``ms-deployment`` to register
       the customer's repo class, version, manifest dependencies, and
       ``default_configuration``.
    2. Creates a ``hmd_lang_deployment.repo_instance`` and a
       ``hmd_lang_deployment.repo_instance_deployment`` with
       ``status="DEPLOYED"`` and ``deployment_image=<repo>:<version>``.
    3. Seeds each declared S3 bucket as a DEPLOYED ``hmd-inf-s3bucket``
       instance.
    4. For each manifest dependency that is NOT locally deployed
       (``hmd-inf-datadog-lambdas``, ``hmd-inf-opa-authorizer``,
       ``hmd-database-account``, etc.), creates a SKIPPED deployment
       record so dependency resolution succeeds.

    Customer repo classes are treated identically to first-party ones --
    same code path, same idempotent seeding, no special-casing. The only
    requirement is a well-formed ``manifest.json`` and ``nsplugin.json``.

.. spec:: Iteration loop -- cold restart, no hot reload
    :id: HMD_CLI_NEURONSPHERE_NERD003_SPEC008
    :links: HMD_CLI_NEURONSPHERE_NERD003
    :status: proposed

    Live in-process reload of language packs,
    ``content_item_type_config``, or ``content_path_configs`` is
    **explicitly out of scope**. The supported iteration loop is:

    *edit -> cold restart of the librarian Lambda -> exercise.*

    To make this fast, the CLI provides a new command that restarts a
    single plugin's Lambda inside Floci without tearing down or re-running
    ``hmd neuronsphere up``:

    .. code-block:: bash

        hmd neuronsphere restart <plugin-name>
        # e.g.
        hmd neuronsphere restart acme-device-lib

    Behavior:

    1. Look up the plugin by ``plugin_name`` via ``LocalPluginLoader``;
       error out if not enabled.
    2. Re-read ``meta-data/config_local.json`` and ``nsplugin.json`` from
       the plugin's repo so any edits to ``content_item_type_config``,
       ``content_path_configs``, or other config fields take effect.
    3. Re-derive the Lambda spec via
       ``LocalPluginLoader.get_hmdms_lambda_spec`` (which re-evaluates
       ``repo_mounts`` against the current state of ``$HMD_REPO_HOME``,
       so newly cloned sibling repos are picked up).
    4. Update the Lambda in Floci. If only mutable fields changed
       (env vars, mounts), call ``update_function_configuration``. If the
       image tag itself changed, fall back to ``delete_function`` +
       ``create_function`` to guarantee a fresh container.
    5. Re-push the updated ``default_configuration`` to ``ms-deployment``
       via ``add_repo_class_version`` so any consumer fetching the
       librarian's config sees the new values.
    6. Warm the Lambda with a single invocation so the first user request
       does not pay cold-start latency.

    For language pack *source* changes, the same restart command suffices
    because SPEC002 mounts the pack directly from ``$HMD_REPO_HOME`` --
    no ``pip install`` loop, no image rebuild.

    The restart command is generic. While motivated by librarian
    iteration, it should ship as a plugin-restart command usable by any
    HMDMS service plugin, not librarian-only.

    Implementation deferred to a follow-up PR; this NERD specifies the
    command's contract and behavior.

.. spec:: Documentation deliverables
    :id: HMD_CLI_NEURONSPHERE_NERD003_SPEC009
    :links: HMD_CLI_NEURONSPHERE_NERD003
    :status: proposed

    Three documentation deliverables, sequenced as follow-up work:

    1. **Quickstart guide.** A new RST page in
       ``hmd-cli-neuronsphere/docs/`` walking a customer through the
       SPEC001 contract end to end: scaffolding the four files, building
       the image, exporting ``HMD_LOCAL_PLUGINS``, running ``hmd
       neuronsphere up``, and verifying the librarian is reachable. The
       quickstart should reproduce the four file templates from SPEC001
       and call out the ``acme-`` bucket-name prefix convention to avoid
       collisions with first-party buckets.

    2. **Reference example.** A reference ``src/local/nsplugin.json``
       added to ``hmd-ms-device-lib`` (which currently lacks one) so that
       device-lib itself becomes the canonical "derived librarian"
       example a customer can read and copy. The same PR removes the
       stale ``dynamo_url: "http://dynamodb:8000/"`` from
       ``hmd-ms-device-lib/meta-data/config_local.json`` (see SPEC004).

    3. **Migration note.** A short note in the NeuronSphere local
       development docs reminding readers that any pre-Floci config
       referencing ``http://dynamodb:8000/``, ``http://s3:9000/``, etc.
       is stale and should be replaced with reliance on
       ``AWS_ENDPOINT_URL=http://floci:4566`` (which the standard HMDMS
       env block already provides).

Acceptance criteria
-------------------

The top-level requirement is satisfied when, following the SPEC001 templates,
a customer can:

- Build their librarian image with ``hmd build`` -- output tagged
  ``<repo-name>:<VERSION>``.
- Run ``hmd neuronsphere up`` with their repo path on
  ``HMD_LOCAL_PLUGINS`` and observe:

  - The librarian Lambda registered in Floci
    (visible via ``aws --endpoint-url=http://localhost:4566 lambda
    list-functions``).
  - Their S3 bucket present in Floci S3.
  - The librarian responds to
    ``GET http://localhost/hmd_ms_<lambda_name>/health``.
  - A ``PUT /api/content_item`` against a custom-typed payload persists
    to the per-librarian DynamoDB table inside Floci and to the shared
    JanusGraph.
  - ``hmd-ms-deployment`` reports the customer's repo class as DEPLOYED.

- Edit ``content_item_type_config``, ``content_path_configs``, or a
  language pack source file under ``$HMD_REPO_HOME`` and pick up the
  change with a single ``hmd neuronsphere restart <plugin>`` call.

- Run the librarian without cloning the language pack repos locally and
  see the librarian start successfully using the bundled copy from the
  Docker image.

No code changes inside ``hmd-cli-neuronsphere`` are required to onboard a
new customer librarian -- only the implementation work for SPEC002
(mount support) and SPEC008 (restart command) deferred to a follow-up
implementation NERD/PR.

Risks and open questions
------------------------

- **Repo-mount field name and resolution rules.** SPEC002 introduces a new
  ``hmdms_service.repo_mounts[]`` field. The exact name (alternatives:
  ``mount_repos``, ``local_dev_mounts``) and the path-resolution rule
  (Python-only ``src/python/<package>`` vs. configurable per-entry
  ``subpath``) are open. Settled in the implementation PR.

- **Restart-command scope.** SPEC008's ``hmd neuronsphere restart`` is
  motivated by librarian iteration but applies to any HMDMS service
  plugin. Recommendation is to ship it as a generic plugin-restart
  command rather than a librarian-only one; final naming is open.

- **Bucket name collisions.** A customer who picks a bucket name that
  collides with a first-party plugin (or with another customer running
  on the same machine) will see Floci ``BucketAlreadyExists`` errors at
  ``hmd ns up`` time. Recommendation: ``<customer>-`` prefix convention
  documented in the SPEC009 quickstart.

- **OPA enforcement gap.** SPEC007 SKIPPED-mocks the
  ``hmd-inf-opa-authorizer`` dependency, so local librarians run without
  policy enforcement. Customers needing to test policies must contribute
  an OPA plugin or run their librarian against a cloud environment.
  This matches today's first-party behavior and is not a regression.

- **Floci Lambda cold-start cost.** SPEC008's restart command invokes
  ``delete_function`` + ``create_function`` for image-tag changes.
  Floci's Lambda warm-pool is per-function; each restart pays one
  cold-start at warm time. Acceptable for an iteration command run
  by hand; not for hot-loop workflows.
