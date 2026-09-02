Control Plane and Named Environments
=====================================

A local NeuronSphere is one shared **control plane** plus N named
**environments**. This is what allows several local NeuronSpheres to run on the
same machine: previously every container published a fixed host port, so a
second stack collided immediately.

Topology
--------

.. code-block:: text

    control plane          compose project local_neuronsphere-<hash>
      hmd_proxy            nginx — the ONLY container with published ports
      hmd_db               postgres      (no host port)
      global-graph         janusgraph    (no host port)
      floci                THE Floci — every account lives in this one container
                           account 000000000000
                             hosts ms-deployment, ms-naming, artifact-lib
                           account 000000000001  ("local")   + k3s ns-local-<hash>
                             hosts ms-dbaccount + that env's HMDMS Lambdas
                           account 000000000002  ("dev2")    + k3s ns-dev2-<hash>

      floci-rds-<opaque>   postgres  (RDS instances, one per account,
                           aliased hmd_db / hmd_db-<env>)

    env "local"            compose project ns-<hash>-env-local
      global-graph-local   janusgraph

    env "dev2"             compose project ns-<hash>-env-dev2
      global-graph-dev2    janusgraph

Each environment is a self-contained emulated AWS account: its own account inside
the shared Floci, its own EKS/k3s cluster, its own Postgres, its own JanusGraph
and its own ``hmd-ms-dbaccount`` — mirroring the cloud, where every account
carries its own dbaccount, RDS and Neptune.

Postgres is that mirror taken literally: it is a **Floci RDS instance deployed by
``hmd-postgres-rds``** — the same RepoClass the cloud uses, with a
``src/local/cdktf`` overlay swapping the Aurora-on-a-VPC stack for a plain
``aws_db_instance`` — rather than a compose container. The control plane's is
deployed by its bootstrap DAG (see :doc:`modes`) and each environment's by the
first phase of its own changeset, so a ``database-instance`` dependency resolves
against a Resource that a real deploy produced.

Floci names the container it spawns opaquely and finds it by label, so the CLI
gives it the canonical network alias — ``hmd_db`` for the control plane,
``hmd_db-<env>`` for an environment. That is what keeps every consumer
unchanged, and keeps the port at 5432 rather than routing through Floci's
7001-7099 RDS proxy range, which is not re-established after a Floci restart.

There is exactly **one Floci container**. Floci isolates accounts internally,
resolving which one a request belongs to from the SigV4 access key id it is
signed with — a 12-digit access key *is* the account — and namespacing every
storage-backed service (S3, DynamoDB, SQS, Lambda, Secrets Manager, IAM, EKS,
RDS) beneath it. The CLI never relies on an ambient ``$AWS_ACCESS_KEY_ID`` for
this: ``floci_deployer.FlociTarget.access_key_id`` carries the account for every
call, and is threaded through the boto3 client factory, the projectbuilder
containers that run deploy nodes, and the External Secrets operator's chart
values. Signing with the wrong key does not fail — it quietly reads and writes
another account.

Because every environment has its own Postgres, **database and user names are
identical across environments** (``hmd_ms_transform`` and friends). Nothing is
renamed; a repo's BOM entry reads the same in every environment.

Commands
--------

.. code-block:: bash

    hmd neuronsphere up                     # control plane + the default env
    hmd neuronsphere up --env dev2          # control plane + dev2

    hmd neuronsphere env create dev2        # new account + cluster + Local BOM
    hmd neuronsphere env create dev2 --manifest ./dev2.yaml
    hmd neuronsphere env list
    hmd neuronsphere env status dev2
    hmd neuronsphere env plan --env dev2    # what `up` would add/change/destroy
    hmd neuronsphere env show --env dev2    # the resolved change_set definition
    hmd neuronsphere env use dev2           # change the default
    hmd neuronsphere env delete dev2

    hmd neuronsphere up --env dev2 --upgrade   # deploy additions and changes
    hmd neuronsphere up --env dev2 --prune     # destroy what is no longer declared

    hmd neuronsphere down --env dev2        # stop one env; others keep running
    hmd neuronsphere down                   # stop every env, then the control plane

``--env`` is accepted by ``up``, ``down``, ``status``, ``plan``, ``show``,
``route-service``, ``db-provision`` and ``db-register``. When omitted it
resolves in this order: ``$HMD_LOCAL_ENV``, the registry's default environment,
then ``local``.

Stopping vs. purging
~~~~~~~~~~~~~~~~~~~~

``down`` without ``--purge`` is a **stop**, not a teardown. Containers are
stopped rather than removed, the environment's k3s cluster is stopped rather
than deleted, and the Docker network stays. The deployment graph in PostgreSQL,
the registry's ``csd_nid`` and ``k3s_uid``, and the applied-changeset snapshot
all survive, so the next ``up`` reconciles rather than redeploying.

As of Floci 1.7.0 the k3s cluster's datastore survives a plain ``down`` as well:
the ``floci-eks-<cluster>`` container and volume are re-adopted on restart rather
than torn down, so the ``kube-system`` UID is unchanged and ``up`` takes the true
fast path. The applied-changeset snapshot's Helm-release cross-check remains as a
safety net for the cases where the cluster genuinely is replaced -- see
:doc:`modes`.

``down --purge`` destroys instead: it deletes the k3s cluster *and* its
``floci-eks-<cluster>`` volume, removes the containers and the Docker network,
deletes ``$HMD_HOME/.cache/environments/<slug>/``, and clears the environment's
bootstrap marker. The next ``up`` then runs the full two-phase bootstrap. Note
that a whole-stack ``--purge`` also drops ms-deployment's graph for *every*
environment; ``--env <name> --purge`` scopes it to one.

Declaring an environment
------------------------

An environment's contents can be **declared** in a manifest at
``$HMD_HOME/environments/<name>.yaml`` (``.yml`` and ``.json`` work too;
``HMD_LOCAL_ENV_MANIFEST`` overrides the lookup). The manifest says which
plugins to enable and which extra repo instances to deploy; ``up`` compiles it
into a ``hmd_lang_deployment.change_set`` definition and reconciles the
environment against it.

.. code-block:: yaml

    version: 1
    name: dev2

    # Allow-list of entry-point names in the
    # `hmd_cli_neuronsphere.get_local_bom_entries` group. Listing the key at all
    # switches this environment to strict mode: only these plugins contribute.
    plugins:
      - ns-telemetry
      - ns-trino

    # Optional configuration handed to a plugin's contributor callable.
    plugin_config:
      ns-telemetry:
        profile: minimal

    # Extra repo instances, on top of what the plugins contribute.
    repos:
      - instance_name: my-api
        repo_class_name: hmd-ms-myapi
        source:
          type: local        # only `local` today; `artifact` is reserved
          path: null         # default: $HMD_REPO_HOME/<repo_class_name>
        version: null        # default: the bundled artifact's version
        instance_configuration: {}
        dependencies:
          eks-cluster: local-neuronsphere

An environment with **no** manifest behaves exactly as it did before manifests
existed: the built-in Local BOM plus every installed plugin package, with no
reconcile plan and no pruning.

Naming a plugin that is not installed is an error, not a warning — silently
dropping it would silently shrink the declared state, and under ``--prune``
that means silently destroying what was dropped.

Version resolution
------------------

Every repo instance deploys at some ``repo_class_version``. It is resolved
**artifact-first**: the version that ships with the plugin package wins, and a
local working tree is used only when you ask for it. A published artifact is a
reproducible, resolvable version; a working tree is a work in progress.

.. list-table::
   :header-rows: 1
   :widths: 10 40 50

   * - #
     - Source
     - When it applies
   * - 1
     - ``HMD_LOCAL_VERSION_<REPO_CLASS>`` set to a version
     - An explicit pin; wins over everything.
   * - 2
     - The working tree's ``meta-data/VERSION``
     - Only with an override set (see below).
   * - 3
     - The bundled artifact's ``meta-data/VERSION``
     - The unpacked build output shipped inside the plugin package.
   * - 4
     - The declared ``version:``
     - From this manifest, or from a plugin's BOM contributor.
   * - 5
     - The working tree's ``meta-data/VERSION``
     - Last resort, with a warning, when there is neither an artifact nor a
       declared version — a repo is better identified by its own tree than by
       the ``0.1.0`` sentinel.

The repo's ``dependencies`` and ``default_configuration`` are read from
wherever the version came from, so a registered ``RepoClassVersion`` always
describes one build rather than a mix of two.

Overriding with your working tree
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

.. code-block:: bash

    # One repo, from your checkout
    export HMD_LOCAL_VERSION_HMD_MS_MYAPI=local

    # One repo, pinned to an exact version
    export HMD_LOCAL_VERSION_HMD_MS_MYAPI=1.2.3

    # Every repo, from your checkouts
    export HMD_LOCAL_NEURONSPHERE_PREFER_LOCAL_VERSIONS=true

    # ...except this one, which stays on its artifact
    export HMD_LOCAL_VERSION_HMD_INF_TRINO=false

The repo-class name is upper-cased with ``-`` replaced by ``_``, so
``hmd-inf-ext-secrets`` becomes ``HMD_LOCAL_VERSION_HMD_INF_EXT_SECRETS``.

Note that ``source.path`` says *where* a repo's tree is, not that the tree's
version wins — it selects which tree is read, and the tiers above still decide.

**The override moves the code too, not just the number.** The same rule decides
which directory a repo deploys from: the bundled artifact is mounted and
deployed by default, and your checkout only under an override, so a deploy
always runs the build it is registered as. Without the override a checkout at
``2.3.5`` is *not* what deploys — the bundled ``2.3.4`` is. Set
``HMD_LOCAL_VERSION_<REPO_CLASS>=local`` for the repo you are developing and
your edits deploy again, under your tree's version. ``up`` warns whenever a
checked-out tree is being passed over, and names the variable that selects it.

The same applies to the Helm charts installed on the local k3s cluster: an
operator's chart comes from its bundled artifact unless that repo has a local
version override.

Bundled artifacts are discovered in the ``external/`` directory of this package
and of every installed plugin package, keyed by the ``name`` in each artifact's
``meta-data/manifest.json``. A plugin that keeps its artifacts elsewhere can
point at them with ``HMD_LOCAL_NEURONSPHERE_ARTIFACT_ROOTS`` (``:``-separated).

When two packages bundle the same repo class, **the higher version wins** and
``up`` warns, naming the copy it shadowed. Ownership of a repo class moves
between packages, and until the old owner drops its ``pre_build_artifacts``
pin both copies ship; resolving to the older one would register a
``RepoClassVersion`` whose ``manifest.json`` dependencies no longer match the
BOM entry naming its roles, which ``apply_changeset`` rejects outright.

Reconciling
-----------

Every ``up`` on an already-bootstrapped environment prints a plan:

.. code-block:: text

    Reconcile plan: +1 add, ~1 change, -1 remove, 6 unchanged
      - otel-collector (declared no longer; destroy)
      + my-api (hmd-ms-myapi@2.3.4)
      ~ clickhouse (hmd-inf-clickhouse@0.4.1; configuration changed)

Printing is unconditional; acting is not.

.. list-table::
   :header-rows: 1

   * - Category
     - Meaning
     - Applied by
   * - ``+ add``
     - Declared, but not ``DEPLOYED`` in the graph (new, or a previous failure)
     - ``--upgrade``
   * - ``~ change``
     - Deployed, but its version or configuration no longer matches
     - ``--upgrade``
   * - ``- remove``
     - Deployed, but no longer declared (plugin disabled, repo entry deleted)
     - ``--prune``

Within one ``up``, destroys run **before** deploys: a removed instance may still
hold a Helm release name or a Resource that an added entry is about to claim.

Two safety rules govern ``--prune``:

- Core instances (``local-neuronsphere``, ``local-databases``) are created by
  the bootstrap, never declared, and are never candidates for removal.
- ``destroy_from`` is a *starting point*: ms-deployment destroys everything
  downstream of it. Before destroying anything the CLI asks for the closure via
  a dry run, and if it includes an instance the environment still declares, the
  whole prune is refused with those names listed. Remove them from the manifest
  too, or keep the instance they depend on.

Drift is detected against a snapshot of the last successfully applied
definition, written to
``$HMD_HOME/.cache/environments/<name>/applied-changeset.json``. An environment
with no snapshot — one bootstrapped before this feature — reports its deployed
entries as *unchanged* rather than proposing a wholesale redeploy. Only entries
that actually deployed are recorded, so a failed one stays eligible for retry.

Addressing
----------

``hmd_proxy`` is the single ingress. Control-plane services keep their
historical **unprefixed** paths, so existing tooling and test suites are
unaffected; environment services are **prefixed** with the environment name:

.. list-table::
   :header-rows: 1
   :widths: 45 55

   * - URL
     - Serves
   * - ``http://localhost/hmd_ms_deployment/``
     - control-plane ms-deployment
   * - ``http://localhost/hmd_ms_naming/``
     - control-plane ms-naming
   * - ``http://localhost/hmd_ms_artifact_lib/``
     - control-plane artifact-lib
   * - ``http://localhost/<env>/<service>/``
     - that environment's services
   * - ``http://<app>.<env>.neuronsphere.io/``
     - that environment's Ingress-exposed UIs (Airflow, Argo)
   * - ``http://localhost:4566``
     - control-plane Floci (nginx ``stream``)
   * - ``http://localhost:<19000+4n>``
     - environment *n*'s Floci
   * - ``localhost:<19000+4n+1>``
     - environment *n*'s Trino

Non-HTTP protocols (Trino, Floci's AWS wire protocol) get L4 ``stream``
listeners rather than HTTP locations. The whole ``19000-19063`` range is
published by ``hmd_proxy`` up front — a compose ``ports:`` list is static — and
individual listeners inside it are added and removed at runtime with an nginx
reload, never a container restart. That is 16 environments × 4 slots; override
the range with ``HMD_LOCAL_ENV_PORT_RANGE``.

Databases are deliberately **not** reachable from the host. Use
``docker exec hmd_db-<env> psql -U postgres``.

Two kinds of service are routed under ``/<env>/``. Services this CLI deploys as
Lambdas are named after the plugin (``/local/hmd_ms_dbaccount/``). Services the
deployment DAG deploys are named after their **repo instance**
(``/local/transform/``), and their routes are discovered from the environment's
Floci *after* the DAG has run — which is also the only way to learn the real
API Gateway stage name, since a CDKTF stage is not called ``local``. Use
``hmd neuronsphere route-service <repo>`` to (re-)add one by hand.

UIs are reached the way the cloud reaches them: through the chart's own
``Ingress``. Locally the ingress controller is the k3s Traefik, configured to
answer to the cloud's ``alb`` ingress class so charts deploy unmodified;
``hmd_proxy`` Host-routes ``*.<env>.neuronsphere.io`` to it. Because
``/etc/hosts`` has no wildcards, each UI hostname needs an entry — ``up`` prints
the exact ``sudo`` line for any that do not yet resolve.

How cloud charts work unmodified
--------------------------------

Inside each environment's k3s cluster, CoreDNS maps the canonical hostnames to
that environment's own containers:

.. list-table::
   :header-rows: 1
   :widths: 45 55

   * - name in-cluster
     - resolves to
   * - ``neuronsphere``, ``neuronsphere-workload``
     - the one ``floci`` (which account is selected by the caller's credentials,
       not by this name)
   * - ``hmd_db``
     - ``hmd_db-<env>``
   * - ``global-graph``
     - ``global-graph-<env>``
   * - ``neuronsphere-control``
     - the control-plane Floci
   * - ``hmd_proxy``
     - the control-plane proxy

So a chart's unmodified ``AWS_ENDPOINT_URL=http://neuronsphere:4566``, its JDBC
URL against ``hmd_db`` and its Gremlin endpoint on ``global-graph`` are all
automatically scoped to that environment's account and databases.

How environments are modelled in ms-deployment
-----------------------------------------------

Each named environment gets its **own** ``hmd_lang_deployment.environment``
entity, whose ``type`` **is** the environment name — ``type`` is the
Environment's business id and the only field ms-deployment resolves an
environment by (``get_valid_environment``, ``apply_changeset`` and
``destroy_deploymentset`` all match on it, and the first two assert or index a
single result). A local ``dev2`` is therefore typed ``dev2``, exactly as a cloud
environment is typed ``dev`` or ``prod``; the default environment keeps
``type: local`` because its name *is* ``local``. The deployment set an
environment's changesets are applied through names that same type, and its
``deployment_id`` is the environment name as well.

Since ``repo_instance`` is unique by name *per Environment*, ``project-bucket``
in ``dev2`` is a different instance than ``project-bucket`` in ``local`` — which
is why no BOM entry needs renaming.

The corollary is that any lookup by instance name alone is ambiguous once a
second environment exists. ``find_core_deployment_node`` and
``_repo_instance_status`` therefore filter by ``deployment_id``; see
``test_bom_env_scoping.py``.

Because the environment name is also the ``--environment`` a generated deploy
script runs with, it is the environment component of every
``make_standard_name`` the deploy produces. The CLI passes the same name when it
seeds the admin DB secret and tags core Resources, so producer and consumer
agree on the name in every environment, not just the default one.

Registry and state
------------------

The registry lives at
``$HMD_HOME/.cache/neuronsphere/environments.json`` and records each
environment's identity: name, account id, container names, compose project, k3s
cluster, kubeconfig path, port slot and bootstrap state. Every derived value is
computed once at create time and then persisted, so changing a derivation rule
later cannot orphan running containers.

Per-environment data lives under
``$HMD_HOME/.cache/environments/<name>/`` (``floci/data``,
``postgresql/data``, ``graph_db``, ``k3s/kubeconfig``). Control-plane state
stays at its historical ``$HMD_HOME/{floci/data,postgresql/data,graph_db}``
paths, so upgrading an existing install moves no data.

Upgrading an existing install
-----------------------------

The first ``up`` after upgrading migrates a pre-multi-environment
``$HMD_HOME`` into a ``local`` environment marked ``legacy_layout``: the
control-plane Floci, Postgres and JanusGraph *are* that environment's, and the
k3s cluster keeps the name Floci already spawned its container and volume under.
Nothing is redeployed. The visible changes are that ``5432`` and ``8182`` are no
longer published, and that ``4566`` now arrives via ``hmd_proxy``.

Fresh ``$HMD_HOME`` s always get the split layout.

Upgrading PostgreSQL
~~~~~~~~~~~~~~~~~~~~

Floci recreates an RDS instance's container from the **current**
``FLOCI_SERVICES_RDS_DEFAULT_POSTGRES_IMAGE`` on every start, while reusing the
instance's named volume. A postgres *major*-version bump in ``hmd-postgres-base``
therefore leaves the old data directory behind, and the new binary refuses it:

.. code-block:: text

    FATAL:  database files are incompatible with server
    DETAIL: The data directory was initialized by PostgreSQL version 14,
            which is not compatible with this version 16.3.

Nothing reports that at the point of the change — Floci still calls the instance
``available``, because it has recorded it — so the first symptom would be
whatever connects next failing, a layer removed from the cause.

``up`` therefore checks before Floci starts, comparing the image's ``PG_MAJOR``
against the ``PG_VERSION`` file postgres writes into its own data directory. Both
are readable without starting anything, so the check costs nothing and runs early
enough that no container has crash-looped yet. On a mismatch it names the
volumes, and offers the two ways out:

.. code-block:: bash

    hmd neuronsphere db upgrade --check   # report what would be migrated
    hmd neuronsphere db upgrade           # dump, re-initialise, restore
    hmd neuronsphere down --purge         # or discard the data

``db upgrade`` works in place, because Floci looks for the volume by the name it
derived when it spawned the container; a differently-named copy would simply be
ignored. It dumps with a stock ``postgres:<old>-alpine`` (the configured image
cannot read that data directory — that is the whole problem), clears the volume
so the new image runs ``initdb``, and restores. The old data directory is copied
to ``hmd-pgbackup-<volume>-pg<major>`` first and **never deleted** — this
rewrites a database, and a migration that destroys its only copy is not one worth
offering. That backup deliberately sits outside Floci's ``floci-rds-`` namespace,
so it is neither rescanned by the check nor mistaken by Floci for a live volume.

Upgrading past the single-Floci collapse (breaking)
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Environments used to run their **own** Floci container, with their own state
under ``$HMD_HOME/.cache/environments/<slug>/floci/data``. They are now accounts
inside the single control-plane Floci, and that older state **cannot be
migrated**: Floci keys persisted records by an account prefix whose on-disk
format it does not document, so any conversion would be a guess.

``up`` therefore refuses to start when it finds a non-empty per-environment
``floci/data``, and names the environments involved. Silently ignoring those
directories would be worse than failing — the environment's Lambdas, API
gateways, buckets and secrets would appear to have vanished while ``up``
reported success. Start clean:

.. code-block:: bash

    hmd neuronsphere down --purge

A ``legacy_layout`` environment is exempt: its "own" Floci data dir *is* the
control plane's, which is still exactly where its state belongs.

Limitations
-----------

- All containers share one Docker network. Environments are isolated at the
  account, cluster and deployment-id level, not at L3.
- ``compose_substitute`` plugin containers stay control-plane-scoped and shared,
  because their compose files hardcode ``container_name``.
- Each environment runs a Postgres container (spawned by Floci as an RDS
  instance), JanusGraph and a k3s cluster. The Floci that serves its account is
  shared with every other environment, so a second environment costs noticeably
  less than the first. Use
  ``HMD_LOCAL_NEURONSPHERE_ENABLE_GRAPH=false`` or
  ``HMD_LOCAL_NEURONSPHERE_ENABLE_K3S=false`` to trim one.
- Platform (legacy) mode has no environments; ``--env`` is rejected there.
