.. NERD002 nsctl -- a Standalone Go CLI for the Local NeuronSphere

NERD002 nsctl -- a Standalone Go CLI for the Local NeuronSphere
===============================================================

.. req:: Deliver the local NeuronSphere as a single Go binary requiring only Docker
    :id: HMD_CLI_NERD002
    :status: in progress

    The extend-mode local platform currently reachable only through
    ``hmd neuronsphere`` shall be delivered as ``nsctl`` -- a single
    statically-linked, cross-platform Go binary modeled on ``kubectl``, whose
    only prerequisite is Docker. ``nsctl`` presents a purpose-built command
    surface (``nsctl env start|stop|add|delete|purge``, ``nsctl load-plugin``)
    rather than mirroring the Python CLI's, and adds a control-plane DAG-runner
    service so ``hmd-ms-deployment`` submits deployments to a workflow engine
    locally exactly as it submits them to Argo in the cloud. The
    ``nsplugin.json`` format, the BACON ``manifest.json`` contract, and the
    ``$HMD_HOME`` on-disk layout are unchanged. The Python ``hmd neuronsphere``
    surface coexists throughout the port. All other ``hmd-cli-*`` packages,
    all ``hmd-ms-*`` microservices, and all ``hmd-inf-*`` repos remain Python.

.. note::

    **This revision withdraws the original NERD002 scope.** The first version of
    this proposal ported all 40 ``hmd-cli-*`` repositories into a single Go
    ``hmd`` monorepo with 100% command-line compatibility, on a ~36-week plan.
    That scope was wrong for the goal it claimed. Adoption is gated by the local
    platform -- the thing a newcomer runs first -- not by ``hmd bartleby``,
    ``hmd bender``, or ``hmd repo``, each of which shells out to a Python tool
    anyway and so cannot make anyone's machine Python-free. Preserving the
    ``hmd`` surface verbatim also preserved a surface designed for existing
    users, at precisely the moment the point was to serve new ones. This
    revision ports one thing, gives it a surface built for the audience, and
    leaves the rest of the ecosystem alone.

Implementation Status
---------------------

Phases 0 through 3 have landed on ``feat/nsctl-go-cli``. ``nsctl`` bootstraps a
control plane from an empty ``HMD_HOME``, starts and stops environments,
reconciles them to a manifest, and coexists with the Python CLI against one
``HMD_HOME``.

.. list-table::
   :header-rows: 1
   :widths: 12 30 58

   * - SPEC
     - Status
     - Note
   * - 001
     - implemented
     - ``env purge`` is the one verb still missing; the ``repo`` verbs and
       ``env apply`` were added to the surface, ``load-plugin`` withdrawn.
   * - 002
     - implemented
     - Cold bootstrap included. ``env purge`` outstanding.
   * - 003
     - implemented
     - Cross-checked against the literal JSON the Python CLI writes.
   * - 004
     - implemented
     -
   * - 005
     - **withdrawn**
     - Plugin discovery is not implemented in any form. See the SPEC.
   * - 006
     - partly implemented
     - The compose file is embedded; pre-build artifacts are not, so version
       resolution has no artifact tier to prefer over a working tree.
   * - 007
     - implemented
     - EC2 is not used at all -- ``hmd-vpc`` deploys the network instead,
       which removed 24 MB from the binary.
   * - 008
     - **amended**
     - Two of its routes were wrong; the conclusion holds. See the SPEC.
   * - 009
     - implemented
     -
   * - 010
     - not started
     - Phase 4.
   * - 011
     - not started
     - Phase 5.
   * - 012
     - implemented
     - Verified by running the Python CLI against nsctl-created containers.
   * - 013
     - partly implemented
     - ``make install`` exists; GoReleaser and the Homebrew tap are Phase 6.
   * - 014
     - **amended**
     - The HIGH risk is retired; three gaps found in implementation are
       recorded there.
   * - 015
     - **amended**
     - Phase 3's content changed substantially once plugins were dropped.

What implementation changed about the design
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Four things this proposal asserted turned out to be wrong, and are corrected in
the SPECs rather than quietly worked around:

1. **``hmd-img-projectbuilder`` ships no ``kubectl``** (SPEC008). Every
   published tag was checked. Kubernetes work execs into the k3s node instead,
   which launches no container and removes projectbuilder as a prerequisite for
   provisioning a cluster.
2. **``docker compose`` is not needed either** (SPEC008). Three containers
   described by one embedded file are materialised through the Docker Engine
   API, so ``docker`` is genuinely the only host binary.
3. **Plugin discovery should not be ported at all** (SPEC005). A RepoClass
   already declares what it needs and produces, in the files the cloud deploy
   path reads. The parallel inventory is the copy that goes stale -- and did.
4. **The control plane's bootstrap does not need to run on every start.** The
   Python runs it each time because the work is idempotent; ``nsctl`` runs it
   only when the control plane has not been bootstrapped, and a warm start
   recovers what it needs from Floci's persistent state.

Motivation
----------

1. **Installation friction is the adoption barrier.** Trying the local
   NeuronSphere today requires Python 3.9+, pip, a virtualenv, and ~40 pip
   packages with transitive dependencies (cement, boto3, pyyaml, jinja2,
   requests, kubernetes, pg8000, inquirerpy, colorlog, python-dotenv), plus
   version alignment across all of them. Conflicts with system Python are
   routine. A single binary reduces this to "install Docker, download
   ``nsctl``".

2. **Most of the Python is already behind a container boundary.**
   ``LocalWorkflowRunner`` does not run ``hmd deploy`` on the host -- it runs
   each generated deploy script inside an ``hmd-img-projectbuilder`` container
   (``local_workflow_runner.py``, ``_execute_in_projectbuilder``). The Python
   that performs a deploy is therefore an implementation detail of an image,
   not a host prerequisite. What remains on the host is orchestration: Docker
   and Compose, k3s and Floci provisioning, nginx config generation, the
   environment registry, and the ms-deployment BOM protocol. That is the
   portable part, and it is the part a Go binary is good at.

3. **Startup latency.** Python interpreter startup plus importlib scanning of
   40+ distributions costs 1-3 s per invocation. A Go binary starts in under
   10 ms. This matters most for the read-only commands (``env list``,
   ``status``) that users run repeatedly.

4. **Ecosystem alignment.** Docker, Kubernetes, Helm, Traefik and Argo are all
   Go. The tools this CLI drives and the k3s cluster it provisions are Go
   artifacts, and so are the two Go binaries already in this repo family (see
   item 6).

5. **Parallel deploys become natural.** The local deploy manifest already
   carries dependency edges per node and the current runner ignores them,
   executing strictly sequentially. Go's concurrency primitives make a
   dependency-respecting worker pool a small amount of code (SPEC011).

6. **There is a precedent in this repo family.** ``hmd-cli-bartleby`` is
   already a Go rewrite of an ``hmd`` CLI plugin, shipped via
   ``brew install bartleby``, with its Python source retained alongside as
   legacy and its docs stating plainly that the Python CLI "has been replaced
   by the Go binary" and that "no Python runtime [is] required". ``nsx``
   (in the ``neuronsphere`` repo) is a second Go binary that today shells out
   to ``hmd neuronsphere up``. The conventions, the build pattern, and the
   distribution channel are established.

Scope
-----

**In scope:**

- ``hmd-cli-neuronsphere``'s **extend mode** orchestration, ported to
  ``nsctl``: environment lifecycle, control-plane lifecycle, Floci
  provisioning, k3s cluster and operator provisioning, nginx routing, the
  environment registry, BOM seeding and the ms-deployment apiop protocol,
  reconcile/delta-apply, and the environment manifest that replaces plugin
  discovery (SPEC005).
- A new **DAG-runner service** for the control plane (SPEC010), replacing the
  in-process ``LocalWorkflowRunner`` call with a submitted workflow.
- Parallel, dependency-respecting node execution (SPEC011).

**Out of scope (unchanged, remain Python):**

- All other ``hmd-cli-*`` packages. ``hmd build``, ``hmd deploy``,
  ``hmd bender``, ``hmd bartleby``, ``hmd repo`` and the rest are untouched;
  a developer contributing to NeuronSphere still installs them.
- All ``hmd-ms-*`` microservices, ``hmd-img-*`` images, ``hmd-inf-*``
  infrastructure repos, and ``hmd-lang-*`` language packs.
- ``hmd-img-projectbuilder``, which continues to execute every BOM node's
  deploy script. ``nsctl`` inherits its contract rather than replacing it.

**Explicitly not ported -- legacy Platform mode.** ``nsctl`` implements extend
mode only. ``HMD_LOCAL_NEURONSPHERE_MODE=platform`` remains available through
the Python CLI for as long as it is supported there. Excluding it removes
roughly 1,100 lines from the port: ``start_neuronsphere_platform`` /
``stop_neuronsphere_platform`` (~500 lines), ``k3s_chart_plugins.py`` (416
lines, reachable only from platform mode), and ``local_storage_provisioner.py``
(210 lines, which has no call site anywhere in the package).

**Formats unchanged:** ``nsplugin.json``, BACON ``manifest.json`` (including
``deploy.default_configuration`` and ``pre_build_artifacts``),
``config_local.json``, the ``hmd_lang_deployment.change_set`` variant-C entry
shape, ``$HMD_HOME/.config/hmd.env``, and every ``HMD_*`` /
``HMD_LOCAL_NEURONSPHERE_ENABLE_*`` environment variable.

Architecture Overview
---------------------

``nsctl`` lives in this repository at ``src/go/nsctl/``, following the house
Go layout established by ``hmd-cli-bartleby``, ``nsx``, and the
``go_cli_repo`` cookiecutter in ``hmd-cookiecutter-default-repo``: ``cmd/``
holds thin cobra adapters, ``internal/`` holds all behavior, the build runs
from a repository-root ``Makefile`` rather than a BACON build command, and the
version is injected by ``-ldflags`` from ``meta-data/VERSION``.

::

    hmd-cli-neuronsphere/
    +-- Makefile                     # go build/test/vet, version from meta-data/VERSION
    +-- meta-data/VERSION
    +-- src/
    |   +-- python/                  # unchanged; coexists through the port
    |   +-- go/
    |       +-- nsctl/
    |           +-- go.mod           # github.com/neuronsphere/hmd-cli-neuronsphere
    |           +-- main.go
    |           +-- cmd/
    |           |   +-- root.go
    |           |   +-- env.go          # start/stop/apply/add/delete/list/status
    |           |   +-- controlplane.go # start/stop/status
    |           |   +-- repo.go         # add/remove/list/import
    |           +-- internal/
    |               +-- registry/     # environments.json reader/writer (SPEC003)
    |               +-- manifest/     # the environment manifest (desired state)
    |               +-- compose/      # YAML -> Docker Engine API, port validation
    |               +-- container/    # docker shell-out, network resolution
    |               +-- floci/        # aws-sdk-go-v2 against Floci (SPEC007)
    |               +-- k3s/          # cluster lifecycle, operators, kubeconfig
    |               +-- router/       # nginx config generation
    |               +-- msdeploy/     # ms-deployment apiop + CRUD client (SPEC009)
    |               +-- bom/          # BOM entries, topological sort, seeding
    |               +-- repoclass/    # manifest + meta-data/resources resolution
    |               +-- reconcile/    # plan/snapshot/digests
    |               +-- runner/       # in-process DAG node execution
    |               +-- controlplane/ # lifecycle and the bootstrap DAG
    |               +-- environment/  # lifecycle and apply
    |               +-- status/       # what env status and cp status report
    |               +-- bundled/      # go:embed of services/ (SPEC006)
    +-- src/go/nsrunner/              # the DAG-runner service (SPEC010, Phase 4)
        +-- cmd/, internal/, Dockerfile

    ``internal/plugin`` is absent: plugin discovery is withdrawn (SPEC005).
    ``nsrunner`` does not exist yet.

.. spec:: Command surface
    :id: HMD_CLI_NERD002_SPEC001
    :links: HMD_CLI_NERD002
    :status: implemented (except env purge)

    ``nsctl`` is organised as ``nsctl <noun> <verb>`` after ``kubectl``, not as
    a transliteration of the Cement controller tree.

    .. list-table::
       :header-rows: 1
       :widths: 45 55

       * - ``nsctl``
         - Python equivalent today
       * - ``nsctl env start <name|default>``
         - ``hmd neuronsphere up --env <name>``
       * - ``nsctl env stop <name|default>``
         - ``hmd neuronsphere down --env <name>``
       * - ``nsctl env add --name <n> --bom-json <path>``
         - ``hmd neuronsphere env create <n> --bom-file <path>``
       * - ``nsctl env delete --name <n>``
         - ``hmd neuronsphere env delete <n>``
       * - ``nsctl env purge [<name>]``
         - ``hmd neuronsphere down --purge [--env <n>]``
       * - ``nsctl env apply [<name>]``
         - ``hmd neuronsphere up --env <name> --upgrade``
       * - ``nsctl repo add <class>[@<version>]``
         - (no equivalent -- edit the manifest by hand)
       * - ``nsctl repo remove <instance>``
         - (no equivalent -- edit the manifest by hand)
       * - ``nsctl repo list``
         - (no equivalent)
       * - ``nsctl env list`` / ``nsctl env status``
         - ``hmd neuronsphere env list`` / ``status``
       * - ``nsctl control-plane start|stop|status``
         - (no equivalent -- see SPEC002)

    .. note::

       ``nsctl load-plugin`` was in the original table. It is withdrawn along
       with the rest of plugin discovery; see the amendment on SPEC005.

    **What nsctl ships.** The control plane, one per ``$HMD_HOME``, and the
    environment *substrate* -- the core instance, the environment's Postgres,
    its k3s cluster and ``hmd-ms-dbaccount``. ``nsctl env add`` yields an
    environment that is empty but usable: a cluster and a database with nothing
    deployed on them.

    Everything above that is a RepoClass the user adds. Airflow, Argo,
    transform, Trino, Superset and the rest are **not** built into the binary;
    they are what ``repo add`` declares and ``env apply`` deploys. The
    environment manifest at ``$HMD_HOME/environments/<slug>.yaml`` is the
    desired state, and the ``repo`` verbs are wrappers that edit that file --
    editing it by hand and running ``env apply`` is the same operation, so
    scripted and hand-authored use converge on one artifact rather than
    diverging into two.

    ``start`` and ``stop`` **must be behaviourally identical** to extend-mode
    ``up`` and ``down``, including the ``--upgrade`` and ``--prune``
    delta-apply flags that make a restart cheap rather than a cold bootstrap.

    Two deliberate departures from the Python surface:

    - **``purge`` is a verb, not a flag.** Destroying an environment's Floci
      account, k3s cluster, Postgres and graph state deserves its own word.
      With no name it purges every environment plus the control plane, and
      requires an interactive confirmation (or ``--yes``), preserving the
      guard ``_confirm_full_purge`` provides today.
    - **``env add`` takes a BOM JSON directly.** The Python ``env create``
      splits this across ``--manifest`` (declarative, preferred) and a
      deprecated ``--bom-file``. ``--bom-json`` accepts either shape,
      detecting a flat array as the legacy BOM form.

.. spec:: Control-plane lifecycle is explicit and separate
    :id: HMD_CLI_NERD002_SPEC002
    :links: HMD_CLI_NERD002
    :status: implemented (except env purge)

    A local NeuronSphere is **one shared control plane per ``HMD_HOME``**
    (``hmd-ms-deployment``, ``hmd-ms-naming``, ``hmd-ms-artifact-lib``, the
    Deployment GUI, plus the single Floci, nginx, Postgres and JanusGraph)
    serving **N environments**, each a self-contained emulated AWS account with
    its own k3s cluster, Postgres, JanusGraph and ``hmd-ms-dbaccount``.

    The Floci is shared: an environment is an *account* inside the control
    plane's single Floci container, not a container of its own (SPEC007), which
    is what makes a second environment cost noticeably less than the first.

    ``nsctl env start`` starts the control plane implicitly if it is down, and
    then **leaves it running**. ``nsctl env stop`` must never stop it: stopping
    it now takes every other environment's emulated AWS with it -- their
    Lambdas, gateways, buckets and secrets, not just the deployment graph's
    availability.

    The control plane is therefore given its own verb:

    - ``nsctl control-plane start`` -- idempotent; the same work ``env start``
      does implicitly.
    - ``nsctl control-plane stop`` -- **the only command that stops it.**
      Refuses (exit 3) while any environment is still running, unless
      ``--force`` is given.
    - ``nsctl control-plane status`` -- reports bootstrapped state, the
      ms-deployment health probe, and which environments are up.

    ``nsctl cp`` is registered as an alias.

    This closes a genuine gap in the Python CLI, where the control plane has no
    independent lifecycle at all -- it is only ever stopped as a side effect of
    a bare ``hmd neuronsphere down``.

.. spec:: The environment registry is read, never re-derived
    :id: HMD_CLI_NERD002_SPEC003
    :links: HMD_CLI_NERD002
    :status: implemented

    ``$HMD_HOME/.cache/neuronsphere/environments.json`` persists **every**
    derived value for the control plane and each environment: container names,
    compose project name, Docker network name, k3s cluster name, kubeconfig
    path, account id, port slot, and the bootstrap record
    (``{csd_nid, k3s_uid}``). ``env_registry.py`` computes these once, at
    create time, and never recomputes them -- deliberately, so that changing a
    derivation rule later cannot orphan containers or volumes named under the
    old rule.

    **``nsctl`` MUST read this file rather than re-derive any of it.** The
    ``HMD_HOME``-to-network-name hash already exists in three independent
    copies (``floci_deployer.py``, ``hmd-cli-bender``, and ``nsx``'s
    ``internal/container/network.go``, which carries a "keep in sync" comment
    counting them). A fourth copy inside ``nsctl`` would be the one nobody
    updates. The hash is implemented only as a fallback for a never-bootstrapped
    ``HMD_HOME``, and is exercised by a test asserting it matches the Python
    value for a known input.

    Re-derivation is not merely redundant, it is **wrong for a case that
    exists in the field**: an environment migrated from the pre-multi-env
    layout carries ``legacy_layout: true``, shares the control plane's Postgres
    and graph rather than running its own, and so has a container named
    ``hmd_db`` -- matching no current naming rule at all.

    Note ``floci_container``/``floci_alias`` are **not** registry fields. Every
    environment shares the one Floci, so both are derived constants (``floci``
    and ``neuronsphere``); a registry written by an older CLI still carries
    per-environment values, and the loader drops them. The field that does the
    work is ``account_id`` (SPEC007).

    Registry writes (``env add``, ``env delete``, port-slot and account-id
    allocation, ``record_bootstrap``) must preserve the exact JSON shape,
    including the slot arithmetic (``DEFAULT_PORT_BASE=19000``,
    ``PORTS_PER_ENV=4``, ``MAX_ENVS=16``; slot *n* takes its base at ``base+4n``,
    trino ``+1``, graph ``+2``, spare ``+3``) and the slug rules
    (``^[a-z0-9][a-z0-9-]{0,15}$`` plus the reserved-slug set).

    **The regression fixture is the literal JSON the Python CLI emits**, not
    Go structs marshalled and read back -- a round-trip fixture passes however
    wrong the struct tags are.

.. spec:: CLI framework -- Cobra replacing Cement
    :id: HMD_CLI_NERD002_SPEC004
    :links: HMD_CLI_NERD002
    :status: implemented

    ``spf13/cobra`` v1.8.0 (the house version, used by ``nsx``, ``goblin``,
    ``bartleby`` and the ``go_cli_repo`` cookiecutter), with ``RunE``
    everywhere and a typed error carrying an exit code.

    .. list-table::
       :header-rows: 1
       :widths: 40 60

       * - Cement concept
         - Go equivalent
       * - ``Controller`` class
         - ``cobra.Command`` with subcommands
       * - ``@ex()`` decorator on a method
         - ``cobra.Command{RunE: func}``
       * - ``stacked_type = "nested"``
         - cobra parent/child command tree
       * - ``self.app.pargs``
         - ``cmd.Flags()``
       * - ``minimal_logger()``
         - ``log/slog``
       * - ``shell.cmd()``
         - ``os/exec.Command``
       * - ``importlib.metadata.version()``
         - ``-ldflags -X`` build-time injection

    **Per-command flags live in constructor closures, not package globals.**
    ``nsx`` found this necessary rather than stylistic: ``t.Setenv`` panics
    under ``t.Parallel()``, so state-dependent command tests can only run in
    parallel if every command is constructed by a function taking its state.
    The same reason motivates a ``--home`` flag overriding ``HMD_HOME``.

    **Exit codes are distinct, never overloaded as counters:** ``0`` success,
    ``1`` nsctl error, ``2`` invalid usage or refused precondition, ``3``
    refused because a resource is in use, ``4`` a deploy node failed.

    ``$HMD_HOME/.config/hmd.env`` is loaded with ``joho/godotenv``, preserving
    the current precedence (process environment wins over file). The file is
    mode 600 and holds live secrets; it is read for the ``HMD_*`` keys and
    never logged.

.. spec:: Plugin discovery, and the plugin bundle
    :id: HMD_CLI_NERD002_SPEC005
    :links: HMD_CLI_NERD002
    :status: withdrawn

    .. warning::

       **Withdrawn.** ``nsctl`` implements no plugin discovery: neither the
       ``nsplugin.json`` half described below nor the ``nsbundle.json`` format
       proposed to replace the entry-point half. ``internal/plugin``,
       ``nsctl load-plugin`` and ``compose_substitute`` do not exist, and
       ``LocalPluginLoader`` is not ported.

       A repo already declares what it needs and what it produces, in its
       BACON ``manifest.json`` and its NERD0004
       ``meta-data/resources/*.yaml`` -- the same files the cloud deploy path
       reads. A per-plugin inventory says the same things a second time, and
       is the copy that goes stale. That is not hypothetical: nsctl briefly
       carried a hardcoded table of what ``hmd-vpc`` produces, and it named a
       different resource namespace than ``hmd-vpc``'s own declaration did.

       What replaces each of ``LocalPluginLoader``'s jobs:

       .. list-table::
          :header-rows: 1
          :widths: 45 55

          * - ``LocalPluginLoader`` today
            - ``nsctl``
          * - ``_deploy_hmdms_service_lambdas``
            - A fixed bootstrap order for the shipped foundation services
              only. Everything else is a user-added RepoClass the DAG
              deploys.
          * - ``_aggregate_hmdms_resources``
            - ``repoclass.Produces`` reads the repo's own
              ``meta-data/resources/*.yaml``.
          * - ``provision_plugin_databases``
            - The databases a RepoClass declares, POSTed to the
              environment's dbaccount.
          * - ``compose_substitute``
            - Gone. No shipped plugin used it.

       The consequence to plan around is that ``nsctl`` supports **only** the
       manifest-driven path: ``_collect_plugin_bom_entries`` has no Go
       analogue, so an environment whose entries come from installed plugin
       packages has nothing for nsctl to read until those entries are written
       into its manifest. ``plugins`` and ``plugin_config`` keys in a manifest
       are preserved on save and reported as having no effect, rather than
       silently obeyed or silently dropped.

    The rest of this SPEC is retained as a record of what was proposed.

    Plugins reach the local platform two ways today, and only one of them
    ports directly.

    **(a) Filesystem plugins port unchanged.** A repo is a plugin if it has
    ``src/local/nsplugin.json``. Discovery is explicit colon-separated paths in
    ``HMD_LOCAL_PLUGINS``, then a scan of ``HMD_REPO_HOME`` when
    ``HMD_LOCAL_PLUGINS_SCAN_REPO_HOME=true``. Enablement precedence is
    preserved exactly: the core set ``{floci, main, graph}`` is always on;
    then explicit listing in ``HMD_LOCAL_PLUGINS``; then
    ``env_var_override`` / ``HMD_LOCAL_NEURONSPHERE_ENABLE_<NAME>``; then
    ``enabled_by_default`` (false, so every non-core plugin is opt-in).
    ``ensure_foundation_plugin`` force-loads ``artifact-lib`` and
    ``dbaccount`` regardless of user configuration.

    The ``nsplugin.json`` **schema is unchanged**, including the
    extend-mode-critical ``hmdms_service`` block from which a full Floci Lambda
    spec is synthesized (function name, ``<repo_name>:<version>`` image,
    ``SERVICE_CONFIG`` merged from BACON ``deploy.default_configuration`` plus
    ``src/local/config_local.json``, one ``<VAR>=s3://<bucket>`` per declared
    bucket, and ``dependency:db-credentials`` resolved to a literal Secrets
    Manager name). The validator (``nsplugin_validator.py``) is ported
    check-for-check.

    **(b) Installed-package plugins are the half with no Go analogue.**
    ``hmd-cli-plugin-ns-{telemetry,analytics-engines,orchestration,visualization}``
    contribute through setuptools entry-point groups -- chiefly
    ``hmd_cli_neuronsphere.get_local_bom_entries``, plus
    ``get_post_deploy_notices`` -- and ship their own ``external/`` artifact
    roots that ``bom_seeder`` walks during version resolution. Go cannot
    enumerate installed Python distributions.

    **Auditing what those four functions actually do settles the format.**
    Their ``bom.py`` modules run 209-410 lines each, and essentially all of it
    is a Python literal: ``repo_instance_name``, ``repo_class_name``,
    ``deployment_id``, a nested ``instance_configuration`` dict, and a
    ``dependencies`` map of role to instance name. The executable surface
    across all four is four constructs and nothing else:

    .. list-table::
       :header-rows: 1
       :widths: 34 66

       * - Construct in ``bom.py``
         - Declarative equivalent
       * - ``os.environ.get(HMD_LOCAL_NEURONSPHERE_ENABLE_<X>, "true")`` not in
           ``{false, 0, no}`` -- all four plugins
         - ``enabled_by: {env: ..., default: true}`` on the bundle
       * - ``distribution("hmd-cli-plugin-ns-<sibling>")`` plus that sibling's
           enable flag, gating a shared instance (``redis``) so two plugins do
           not both contribute it
         - ``contribute_if_absent: true`` on the entry
       * - A boolean feature flag guarding one entry or one config key
           (telemetry's ``HMD_..._TELEMETRY_COLLECT_K3S``, default false)
         - ``when: {env: ..., default: false}`` on the entry
       * - An env var supplying an instance name or list
           (``HMD_LOCAL_ORCHESTRATION_TRANSFORM_BUCKETS``), and the imported
           ``CORE_INSTANCE_NAME`` constant
         - ``${env:VAR}`` and reserved tokens ``${core}``, ``${ext_secrets}``,
           ``${env.deployment_id}``, ``${env.slug}``

    ``contribute_if_absent`` deserves note because it **deletes the one
    construct that cannot port**. Both halves of the ``redis`` coordination
    already declare, in comments, that their entries are byte-for-byte
    identical and that ``bom_seeder`` de-dupes by ``repo_instance_name``, so
    the ``importlib.metadata`` probe is buying only the avoidance of a de-dup
    that is already a no-op. Declaring the entry and letting the loader
    de-dupe is the same outcome without either bundle needing to know the
    other exists.

    **Therefore: ``load-plugin`` consumes a bundle, not an image.** A plugin
    bundle is a zip -- in practice the repo's existing
    ``<repo>_<version>_build.zip``, the artifact the BACON build already
    produces and the Artifact Librarian already stores -- carrying::

        <plugin>/
          meta-data/VERSION, manifest.json
          src/local/nsplugin.json           # unchanged
          nsbundle.json                     # NEW: declarative BOM contribution
          external/<repo>/...               # artifact roots, as today

    ``nsctl load-plugin <path|name>`` validates ``nsbundle.json``, registers
    the bundle under ``$HMD_HOME/.cache/neuronsphere/plugins/<name>/``, and
    adds its ``external/`` tree to the artifact-root index used for version
    resolution. A loaded bundle is thereafter indistinguishable from a bundled
    one. ``nsctl load-plugin --list`` / ``--remove`` manage the set.

    **Why not a Docker image that loads itself.** Running plugin-supplied code
    to answer "what does this plugin contribute" makes every cheap read
    expensive and every cheap read a trust decision:

    - ``nsctl env plan``, ``env list`` and ``env status`` must answer *what
      would deploy* without side effects. Behind an image, each becomes N
      image pulls and N container runs, and is unusable offline.
    - The reconcile digests (SPEC009) hash the declared entries. A stable
      digest needs a stable, inspectable input; a program's output is neither
      diffable nor reviewable.
    - Executing a third-party image at host authority to *configure* the
      platform is a far larger grant than reading its declarative data, and
      it is the grant nothing else in this design asks for.

    **The escape hatch already exists, one layer down.** A plugin that must
    genuinely *run* something does so in its own deploy node -- ``src/local/
    deploy_local.sh``, executed in ``hmd-img-projectbuilder`` by the runner
    (SPEC010), already scoped to one instance and already sandboxed in a
    container. Load time is for declaring; deploy time is for doing. If a
    future plugin needs computation at load time that no primitive above
    covers, an optional ``loader_image`` field may be added to
    ``nsbundle.json`` -- but it is deliberately not specified here, because
    specifying it now would make "arbitrary code" the loader's contract from
    day one for a need no existing plugin has.

    **Post-deploy notices are declarative too.** The only implementation
    (Superset's admin login) reads the secret
    ``superset-${env.deployment_id}-${env.slug}-admin-credentials`` and formats
    two of its fields. That is a ``post_deploy_notices`` list of
    ``{secret, template}`` in ``nsbundle.json``, evaluated by ``nsctl``, with
    the same best-effort contract as today (any failure is logged, never
    surfaced as a deploy failure).

    **Migration of the four existing plugins is mechanical.** Each keeps its
    repo, its ``external/`` artifact roots and its BACON build; ``bom.py``
    becomes ``nsbundle.json``, the setuptools ``entry_points`` block is
    retained so the Python CLI keeps working during coexistence (SPEC012), and
    a test asserts the two produce the same entry list. Telemetry additionally
    moves its ``data/otel_databases.json`` into the bundle verbatim -- it is
    already pure data that ``bom.py`` only opens and inlines.

.. spec:: Bundled artifacts and go:embed
    :id: HMD_CLI_NERD002_SPEC006
    :links: HMD_CLI_NERD002
    :status: partly implemented

    The BACON ``build.pre_build_artifacts`` mechanism unpacks other repos'
    build outputs into ``src/python/hmd_cli_neuronsphere/external/<name>/`` at
    build time; each contributes its ``src/local/`` tree, its ``src/helm/``
    chart and its ``meta-data/`` (VERSION + manifest). Bundled artifacts
    **win over** a working tree during version resolution unless the user opts
    out via ``HMD_LOCAL_VERSION_<REPO_CLASS>`` or
    ``HMD_LOCAL_NEURONSPHERE_PREFER_LOCAL_VERSIONS``.

    ``nsctl`` embeds the same trees with ``//go:embed``, plus the
    ``services/docker-compose.*.yml`` files:

    .. code-block:: go

        //go:embed services/*.yml services/*.env
        //go:embed external/*/src/local/* external/*/meta-data/*
        var bundled embed.FS

    ``pre_build_artifacts`` is unchanged; only the unpack destination moves.
    At runtime the embedded FS is consulted first and a filesystem plugin of
    the same name overrides it, preserving today's precedence. Files consumed
    by ``docker compose`` or ``helm`` are materialised into
    ``$HMD_HOME/.cache`` before invocation, since neither tool can read an
    ``embed.FS``.

    **Docker Compose files are not templates.** They use Compose's native
    ``${VAR:-default}`` interpolation, handled by Compose itself. Only
    user-authored ``src/local/templates/`` entries are rendered, by
    ``text/template``; no bundled plugin defines any today, so the Jinja2
    syntax divergence has no current blast radius and a migration note in the
    plugin development guide is sufficient.

.. spec:: AWS SDK -- aws-sdk-go-v2 against Floci
    :id: HMD_CLI_NERD002_SPEC007
    :links: HMD_CLI_NERD002
    :status: implemented

    ``floci_deployer.py`` drives S3, Secrets Manager, SSM, Lambda, API Gateway,
    RDS and EKS against the Floci emulator via boto3. ``internal/floci`` uses
    ``aws-sdk-go-v2`` with a static-credentials provider and an explicit base
    endpoint:

    .. code-block:: go

        cfg, err := config.LoadDefaultConfig(ctx,
            config.WithRegion(region),
            config.WithBaseEndpoint(endpoint),
            config.WithCredentialsProvider(
                // The account selector -- see below. NOT a dummy value, and
                // never read from the ambient AWS_ACCESS_KEY_ID.
                credentials.NewStaticCredentialsProvider(accountID, "dummykey", ""),
            ),
        )

    Notes carried forward from the Python implementation:

    - **The access key selects the account, and the endpoint does not.** There
      is exactly one Floci container behind the ``neuronsphere`` alias. The
      control plane and every environment are separate *accounts* inside it,
      and Floci resolves which one a request belongs to from the SigV4 access
      key id it is signed with -- a 12-digit access key *is* the account --
      namespacing every storage-backed service beneath it.

      So the Go port must carry the account on the target, exactly as
      ``floci_deployer.FlociTarget.access_key_id`` does, and thread it through
      every credential-building site: the SDK config above, the projectbuilder
      containers that run deploy nodes, and the External Secrets operator's
      chart values. **Signing with the wrong key does not fail** -- the call
      succeeds against the wrong account, which is why an ambient
      ``AWS_ACCESS_KEY_ID`` must never be used here.

      This supersedes the earlier container-per-environment rule (each
      environment addressed by its own Floci container address rather than the
      ``neuronsphere`` alias), which no longer applies.
    - Errors are matched with ``errors.As`` on ``smithy.APIError`` rather than
      by string-matching a response dict.
    - ``clear_apigateway_state`` must be preserved: Floci persists API Gateway
      v1 entities all-null and serves them back as undeletable ghosts.
    - Secret names are produced by ``hmd_cli_tools.make_standard_name``, which
      must be ported **bit-for-bit** -- the microservices reading those secrets
      stay Python and compute the same name independently.

.. spec:: Docker is the only host tool -- kubectl and helm run in projectbuilder
    :id: HMD_CLI_NERD002_SPEC008
    :links: HMD_CLI_NERD002
    :status: amended

    .. warning::

       **Two claims in this SPEC were wrong, and are corrected here.** The
       conclusion -- Docker is the only host tool -- holds; two of the routes
       it proposed for getting there do not.

       **kubectl does not run in projectbuilder, because projectbuilder ships
       no kubectl.** No published tag has one: ``:stable``, ``:0.5``,
       ``:0.5.383`` and ``:localdev`` were all checked. ``RunKube`` instead
       ``docker exec``\ s into the running k3s node container and uses its
       ``/bin/kubectl``, which launches no container at all and removes
       projectbuilder as a prerequisite for provisioning a cluster. The
       batched-script design below is unchanged and is what makes that
       practical; only the container it runs in differs.

       **``docker compose`` is not used either.** In extend mode compose
       manages three containers described by one embedded file, and
       ``docker-compose.environment.yml`` declares no services at all.
       ``internal/compose`` parses that file and materialises the services
       through the Docker Engine API, so ``docker`` really is the only host
       binary -- and the ``pip config get`` shellout that
       ``_get_base_command`` performed on every compose invocation goes with
       it.

       Coexistence (SPEC012) is what makes this safe: nsctl stamps the
       ``com.docker.compose.*`` labels on every container it creates, so
       ``docker compose ps|stop|down`` and ``port_validator.py`` still find
       them. One label matters more than the rest --
       ``com.docker.compose.config-hash``. Its *presence* is what makes a
       container discoverable to compose; without it ``docker compose ps``
       skips the container even when every other label matches, verified
       against compose v2.40.

    ``nsctl`` requires **``docker`` (or ``nerdctl``) on the host and nothing
    else.** Postgres stays ``docker exec ... psql``; image staging into the
    k3s node's containerd stays
    ``docker save | docker exec ... ctr images import``. Everything that today
    needs a ``kubectl`` or ``helm`` binary on the host either moves into a
    container -- the k3s node for Kubernetes work, ``hmd-img-projectbuilder``
    for the Helm and CDKTF deploys it already runs as DAG nodes (SPEC010) --
    or is not ported at all.

    **Helm is eliminated, not relocated.** There are three host uses and none
    survives:

    1. ``env_reconcile``'s release cross-check already needs no helm binary.
       ``k3s_operators.live_helm_releases`` lists Helm's own release Secrets
       (``-l owner=helm``, reading ``metadata.labels.name``) and its docstring
       states the reason outright: it is cheaper than ``helm list -A`` and needs
       no helm on the host. It is a Kubernetes read, and moves with the rest.
    2. ``_install_operator`` / ``_helm_upgrade`` / ``_ensure_chart_dependencies``
       is **dead on the default path.** ``_OPERATORS`` contains exactly
       ``ext-secrets-crds`` and ``ext-secrets``, and ``provision_k3s_operators``
       skips both whenever ext-secrets deploys through the DAG -- which is the
       default (``HMD_LOCAL_NEURONSPHERE_ENABLE_EXT_SECRETS`` is on unless
       explicitly disabled) and is also true whenever a plugin's BOM already
       contributes an ``ext-secrets`` instance. This is the tail of the
       completed migration that moved ClickHouse, KEDA and cert-manager onto
       BOM entries. ``nsctl`` does not port it; the opt-out becomes "declare it
       in the BOM", which is what every other component already does.
    3. ``helm uninstall traefik`` is a one-time migration off a helm-installed
       Traefik onto the image-baked addon. Not ported. A cluster predating that
       migration is handled by the Python CLI or by deleting the cluster, and
       ``nsctl`` says so rather than silently leaving two ingress controllers.

    **kubectl becomes a batched exec into projectbuilder.** The ~29 host call
    sites (22 in ``k3s_operators.py``, 7 in ``nginx_router.py``) are all plain
    Kubernetes API operations in three groups:

    - **Provisioning mutations** -- the CoreDNS custom-records ConfigMap plus a
      ``rollout restart``; the Traefik ingress-class apply and Addon patch; node
      topology labels; stale-Node and orphaned-PV reaping.
    - **Waits** -- ``_wait_for_node_ready`` polling node readiness.
    - **Reads** -- ``get svc -A -o json`` (NodePort discovery),
      ``get ingress -A -o json`` (host routes), the ``kube-system`` namespace
      UID behind ``cluster_incarnation_id``, and the Helm release Secrets above.

    ``internal/k3s`` exposes one helper that runs a **script**, not a command,
    in a throwaway projectbuilder container, and the call sites are grouped so
    a full ``env start`` costs roughly six containers rather than twenty-nine:

    .. code-block:: go

        // RunKube mounts script at /tmp/kube-step.sh along with the
        // container-rewritten kubeconfig, and returns stdout separately
        // from stderr.
        func (k *Kube) RunKube(
            ctx context.Context, script []byte,
        ) (stdout, stderr []byte, err error)

    Details that follow from the container boundary, each with a precedent
    already in the codebase:

    - **Scripts are mounted, never passed as arguments** -- the same rule the
      deploy node already follows for ``/tmp/hmd-deploy-node.sh``, and for the
      same reason (``ARG_MAX`` and quoting).
    - **``kubectl apply -f -`` becomes ``apply -f <mounted file>``.** Two sites
      feed JSON on stdin today (the ingress class, the CoreDNS ConfigMap); both
      become files in the same mount.
    - **The kubeconfig is the in-container rewrite**, produced by the existing
      ``_kubeconfig_for_container`` logic that repoints the server at the
      in-network Floci EKS alias. The host-reachable kubeconfig is still written
      to ``env.kubeconfig_path`` for the *user's* own ``kubectl``; ``nsctl``
      never reads it.
    - **A wait is a loop inside one container**, not repeated container starts.
    - **stdout carries data, stderr carries diagnosis.** ``-o json`` parsing is
      unchanged; failures buffer their output and print under the failed step,
      exactly as ``_print_failure_detail`` does for a deploy node today.

    **Why not client-go.** Vendoring ``k8s.io/client-go`` would also remove the
    binary dependency and would be faster per call, but it trades one problem
    for two: the Kubernetes client version becomes ``nsctl``'s to keep aligned
    with the cluster (local k3s tracks the cloud EKS version, currently 1.34,
    and that pin already lives in two places), and it costs tens of megabytes
    against SPEC013's size target. The image is already version-matched to the
    cluster it provisions and is already the pin for the CDKTF and Helm
    toolchain. One place to pin the Kubernetes toolchain is worth more than the
    microseconds.

    **Consequence, stated plainly:** ``hmd-img-projectbuilder`` becomes a
    prerequisite of *cluster provisioning*, not merely of deploys. A cold or
    offline first run must have it before k3s is usable at all. ``nsctl``
    therefore pulls it once, early in ``control-plane start``, with an explicit
    progress line -- rather than discovering it missing midway through
    provisioning a cluster.

    Port validation (``port_validator.py``) is reimplemented natively with
    ``net.DialTimeout``; it needs no cluster. Selective use of
    ``github.com/docker/docker/client`` is permitted for inspection-shaped work
    (container status, health, logs) where parsing CLI output would be fragile,
    as ``hmd-cli-bartleby`` already does.

.. spec:: ms-deployment client -- the BOM and apiop protocol
    :id: HMD_CLI_NERD002_SPEC009
    :links: HMD_CLI_NERD002
    :status: implemented

    ``internal/bom`` re-implements ``bom_seeder.py`` (the single densest module
    in the port, ~2,350 lines) over ``net/http``. Nothing here is a library
    swap; it is protocol.

    The sequence ``seed_bom`` performs, which must be reproduced exactly:
    resolve each entry's version (bundled artifact, then declared, then working
    tree only under an override); ``POST add_repo_class_version``;
    ``upsert_repo_resource_definitions``; ``declare_core_produces``;
    ``PUT`` the ``hmd_lang_deployment.environment`` / ``deployment_set`` /
    ``change_set`` entities with the definition base64+JSON encoded; then
    ``POST apply_changeset`` and ``POST generate_local_deployment/<csd_nid>``.

    **Two topological sorts exist and must not be conflated.**

    1. ``_topo_sort_bom`` (CLI-side, *before* ``apply_changeset``) exists
       because ``apply_changeset_to_environment`` processes changeset entries
       in list order and resolves each entry's dependencies only against repo
       instances already added earlier in the same call. An out-of-order merged
       list fails with "No repo instance". This pass makes **graph
       construction** succeed and stays sequential.
    2. ``traverse_deployment_dag`` (service-side, Kahn's algorithm over the
       reduced ``RepoInstanceReqRepoInstance`` edges) makes **execution order**
       correct. This is the one SPEC011 parallelises.

    ``internal/reconcile`` ports ``change_set_builder.py`` and
    ``env_reconcile.py``. Its ``entry_hash`` / ``definition_hash`` digests and
    the v2 ``applied-changeset.json`` snapshot format must reproduce
    identically to the Python implementation, or every user's first
    ``nsctl env start`` degrades into a full redeploy. This is a golden-vector
    test against snapshots written by the Python CLI, not a self-consistency
    test.

.. spec:: The DAG-runner service replaces the in-process runner
    :id: HMD_CLI_NERD002_SPEC010
    :links: HMD_CLI_NERD002
    :status: not started

    **Today the CLI pulls.** ``bom_seeder`` posts ``apply_changeset`` with
    ``skip_async: true`` -- a payload boolean whose only function is to stop
    ``hmd-ms-deployment`` from doing what it does in the cloud, namely build an
    Argo ``Workflow`` and ``POST`` it to the Argo server -- then posts
    ``generate_local_deployment/<csd_nid>``, receives a node list, and executes
    it in-process inside the CLI. The deployment therefore lives and dies with
    the CLI process, and the control plane has no idea a workflow engine
    exists.

    **This proposal inverts it.** ``nsrunner`` is a small Go service, built
    from this repository and run as a container in the control-plane compose
    project, exposing a submit API deliberately shaped like the one
    ``deployment_manager.submit_workfow`` already speaks::

        POST /api/v1/workflows/<namespace>
        {"namespace": "...", "workflow": {...}}      -> {"metadata": {"name": ...}}
        GET  /api/v1/workflows/<namespace>/<name>    -> status
        GET  /api/v1/workflows/<namespace>/<name>/log

    ``hmd-ms-deployment`` gains a runner selection alongside its Argo client;
    ``skip_async: true`` is retired as the local marker in favour of the
    control plane simply having a different workflow endpoint configured. The
    changeset deployment records the returned workflow name exactly as it
    records Argo's.

    **Node execution is inherited verbatim, not redesigned.** For each node
    the runner performs what ``_execute_in_projectbuilder`` does today:

    - ``image_cache.ensure_lambda_image`` equivalent -- stage the bare
      ``<repo>:<version>`` tag in the host Docker cache (Floci runs Lambdas off
      the host daemon and has no ECR).
    - Import the image into the k3s node's containerd
      (``docker save | ctr images import``).
    - Resolve the repo working tree; apply the ``src/local/`` overlay into an
      isolated temporary copy -- **the developer's tree is never mutated** --
      or let ``deploy_local.sh`` replace the script entirely.
    - Insert ``--local`` after the ``hmd ... deploy`` subcommand so
      ``hmd-cli-deploy`` takes source from the mount rather than the absent
      Artifact Librarian.
    - ``docker run --rm --entrypoint bash`` the
      ``hmd-img-projectbuilder`` image on the platform network, with the script
      passed as a **mounted file** (generated scripts routinely exceed
      ``ARG_MAX``), injecting ``AWS_ENDPOINT_URL``, ``HMD_ENVIRONMENT=local``,
      ``HMD_HOME=/root/hmd``, ``HMD_DEPLOYMENT_SERVICE_URL``,
      ``NS_LOCAL_PROXY``, ``HMD_LOCAL_K3S_CLUSTER_NAME`` and ``HMD_DID``, and
      mounting the Docker socket, the rewritten kubeconfig, and the workspace.
    - On success, read ``meta-data/resources_output/`` from the workspace and
      submit the NERD0004 Resources to ms-deployment.
    - Report status by ``set_deployment_status/<rid>/<status>`` and
      ``set_change_set_deployment_status/<csd>/<status>``.

    That last point is what makes the inversion tractable: **status already
    flows over REST**, not through the CLI's memory, so moving execution out of
    the CLI changes who calls those endpoints and nothing else.

    Consequences worth stating: a deploy survives the CLI exiting;
    ``nsctl env start`` can attach to and stream an in-flight deployment rather
    than owning it; and a second ``nsctl env start`` against the same
    environment can be refused by the runner rather than racing.

    **Open:** the submit path's authentication (Argo's carries a bearer token
    from Secrets Manager) and what happens to an in-flight workflow when the
    control plane stops.

.. spec:: Parallel node execution respecting dependencies
    :id: HMD_CLI_NERD002_SPEC011
    :links: HMD_CLI_NERD002
    :status: not started

    ``LocalWorkflowRunner.run()`` is a strictly sequential ``for node in
    nodes:`` that returns at the first failure. It has no threads, no
    ``concurrent.futures``, and no use of the edge data.

    **The edges are already in the payload.** Each node emitted by
    ``generate_local_deployment`` is
    ``{instance_name, repo_class_name, version, rid_nid, script, dependencies}``,
    where ``dependencies`` is ``get_active_dependencies(...)`` -- the
    instance's real ``RepoInstanceReqRepoInstance`` edges, transitively
    reduced by ``build_deployment_dag`` and filtered to the instances actually
    present in this changeset. The current runner simply never reads the field.

    **Therefore parallelism requires no change to ``hmd-ms-deployment``.**
    ``nsrunner`` maintains an in-degree map over ``dependencies``, dispatches
    every in-degree-zero node to a bounded worker pool, decrements successors
    on completion, and on the first failure cancels the ``context`` so no
    unstarted node begins while in-flight nodes are allowed to finish and
    report.

    Parity targets taken from the cloud path, which already does exactly this
    through Argo: a default concurrency of **4** (Argo's
    ``spec.parallelism: 4``), and a **per-environment mutex** mirroring the
    workflow-level ``synchronization.mutex`` keyed on the deployment set, so
    two submissions cannot interleave on one environment. Concurrency is
    overridable per submission.

    **Granularity: a RepoInstance is the unit, and is not divisible.** One
    instance is one DAG node is one generated bash script is one
    ``hmd ... deploy`` invocation -- in the cloud and locally alike. There are
    no sub-steps modelled anywhere, so any intra-instance concurrency would
    have to come from inside ``hmd-cli-deploy`` / ``-helm`` / ``-cdktf`` and is
    out of scope.

    Nodes for the core repo class (``hmd-cli-neuronsphere``) remain a pure
    status flip that executes nothing, and destroy manifests keep their
    reversed edge direction, arriving already reversed from the service.

.. spec:: Coexistence with the Python CLI
    :id: HMD_CLI_NERD002_SPEC012
    :links: HMD_CLI_NERD002
    :status: implemented

    The Python ``hmd neuronsphere`` surface is **not** deprecated by this
    proposal and is not removed on any schedule set here. Both front ends
    operate on the same state and must remain interchangeable mid-project:

    - the same ``$HMD_HOME/.cache/neuronsphere/environments.json`` (SPEC003),
    - the same ``$HMD_HOME`` layout, cache directories and nginx fragments,
    - the same ms-deployment graph and the same ``applied-changeset.json``
      digests (SPEC009),
    - the same Docker network, compose project names and container names.

    A user may run ``nsctl env start dev`` and then
    ``hmd neuronsphere status --env dev``, or the reverse, in either order. The
    acceptance test for each ported command is exactly this: perform the
    operation with one front end, verify with the other.

    This mirrors what ``hmd-cli-bartleby`` did -- the Go binary shipped, the
    Python source stayed in the repo as legacy, and the switch was made by
    documentation rather than by removal.

    The existing Robot Framework suites under ``test/`` invoke the CLI as a
    subprocess and remain the behavioural specification; a parallel suite
    parameterised on the binary under test (``hmd neuronsphere`` vs ``nsctl``)
    is the parity harness.

.. spec:: Build and distribution
    :id: HMD_CLI_NERD002_SPEC013
    :links: HMD_CLI_NERD002
    :status: partly implemented

    **Build.** A repository-root ``Makefile`` with ``build``/``test``/``vet``/
    ``tidy``/``clean`` targets, binary at ``src/go/nsctl/build/nsctl``, version
    injected via ``-ldflags "-X main.version=$(VERSION)"`` read from
    ``meta-data/VERSION`` -- the pattern used by ``hmd-cli-bartleby``,
    ``goblins`` and the ``go_cli_repo`` cookiecutter. ``build.commands`` in
    ``manifest.json`` stays ``[["python"]]``; the Go build is not wired into
    BACON, exactly as in the other Go repos here. ``pre_build_artifacts`` runs
    first so ``//go:embed`` has files to embed (SPEC006).

    Go 1.25, cobra v1.8.0, CGO disabled.

    **Distribution.**

    1. **Homebrew tap** -- ``neuronsphere/tap/nsctl``, the channel
       ``bartleby`` already uses.
    2. **GitHub Releases** via GoReleaser: ``darwin/{amd64,arm64}``,
       ``linux/{amd64,arm64}``, ``windows/amd64``.
    3. **Install script** -- platform-detecting curl-pipe-sh.
    4. **Docker image** for CI.

    ``nsctl version`` reports the binary version and, when a control plane is
    reachable, the ms-deployment version it is talking to.

    Binary size target under 50 MB including embedded artifacts (``kubectl``
    ~49 MB, ``helm`` ~46 MB for reference). The target is comfortable precisely
    because SPEC008 vendors neither ``client-go`` nor the Helm SDK.

.. spec:: Gaps, risks, and mitigations
    :id: HMD_CLI_NERD002_SPEC014
    :links: HMD_CLI_NERD002
    :status: amended

    **RETIRED -- HIGH: reconcile digest compatibility.** If ``entry_hash`` /
    ``definition_hash`` differ by so much as key ordering, every existing user's
    first ``nsctl env start`` becomes a full redeploy of their environment
    -- tens of minutes -- and reads as a bug. Mitigation: golden vectors
    captured from the Python implementation, and an explicit
    ``--force-full-redeploy`` escape hatch rather than a silent fallback.

    This landed as specified and the risk is closed. The vectors are generated
    by *calling* ``change_set_builder.entry_hash``, not by transcribing it, and
    they cover the four ways Go's encoder differs from CPython's: separators,
    ``ensure_ascii``, float repr, and HTML escaping. They caught one real
    divergence -- Go models ``repo_class_version`` as a string, so an
    unresolved version flattens to ``""`` where Python hashes ``None`` as
    ``null``.

    Worth recording for anyone extending this: the vectors test the hash
    function, not what is fed to it. A second, invisible variant of the same
    bug survived them -- versions were being resolved *after* the plan was
    computed, so every digest covered an empty version and a version bump was
    undetectable. It took running against a live platform to find.

    **GAP: a RepoClass may not declare what its plugin did.** SPEC005 asserts
    that ``manifest.json`` plus ``meta-data/resources/*.yaml`` cover what
    ``nsplugin.json`` declared. That is true of the foundation services'
    ``service_config`` and of the librarian parameters, both of which are read
    from the manifest. It is not universally true: ``hmd-ms-artifact-lib``
    reads ``BUCKET_NAME`` and its bucket is declared **only** in
    ``src/local/nsplugin.json``. ``nsctl`` bootstraps it, says it will not
    serve until the bucket is declared in the manifest, and continues.

    Reported rather than closed on purpose. Inventing a bucket name in
    ``nsctl`` would hide the gap and point the librarian at a bucket nothing
    else uses; the fix is a declaration in that repo. This is the concrete
    instance of the residual risk SPEC005's audit predicted, and the pattern to
    follow for any others: name the missing declaration, do not substitute for
    it.

    **GAP: the RepoClass declares cloud wiring, which must be resolved.** A
    service's declared engines name a Secrets Manager secret by dependency and
    route through pgbouncer; locally there is neither. Left unresolved the
    service starts and fails every request with "Secret
    dependency:db-credentials not found in PS or SM". ``LocalizeServiceConfig``
    resolves postgres to a direct connection, gremlin to the graph alias, and
    fills in the DynamoDB table name CDKTF would have supplied. Declared values
    are never overridden, only gaps filled -- so this stays a translation of
    the RepoClass rather than a second source of truth.

    **GAP: a control plane's containers are not namespaced by ``HMD_HOME``.**
    ``hmd_proxy``, ``floci`` and ``hmd_deployment_gui`` are fixed container
    names, so two ``HMD_HOME``\ s cannot run control planes concurrently even
    though their networks, compose projects and Floci data directories are all
    distinct. Starting one recreates the other's containers pointed at the new
    home. Not introduced by this port -- the names come from the compose file
    -- but ``nsctl`` makes it easier to hit, and nothing currently detects it.

    **SUPERSEDED -- MEDIUM: installed-package plugin contributions.** Plugin
    discovery is withdrawn entirely (SPEC005), so neither half ports and the
    bundle format this entry proposed does not exist. What replaces it in
    practice is ``nsctl repo import``, which reads an environment's deployed
    instances out of the deployment graph and writes them into its manifest --
    so an environment brought up by the Python CLI is migrated by reading what
    it actually deployed rather than by re-describing its plugins. The residual
    risk named below still stands, and is now recorded concretely as the first
    GAP above.

    The original reasoning, retained: Go cannot enumerate
    Python distributions, so the entry-point half of the plugin system does not
    port. Downgraded from HIGH by the audit in SPEC005: all four existing
    plugins' contributions are static data plus four declarative constructs, so
    the bundle format covers them without loss, and the one construct that
    could not port (``importlib.metadata`` sibling probing) is deleted rather
    than emulated. Residual risk is ergonomic, not structural -- a plugin is no
    longer discovered merely by being pip-installed, so the plugin development
    guide and each plugin's README must change in the same wave as the bundle
    conversion.

    **MEDIUM: the inverted DAG submission touches ``hmd-ms-deployment``.**
    This proposal is not purely additive -- it asks a Python microservice to
    grow a second workflow-runner target. Mitigation: the runner speaks Argo's
    submit shape, so the change is a client selection rather than a new
    protocol; and ``skip_async: true`` remains functional throughout, so the
    old path is a working fallback at every point.

    **MEDIUM: ``make_standard_name`` divergence.** A one-character difference
    in secret naming produces a runtime failure in a Python microservice, far
    from the Go code that caused it. Mitigation: a table-driven test built from
    the Python function's outputs. Landed as specified.

    A related trap is worth naming, because it cost real time: the *identifier*
    helpers are per-instance, and asking the wrong one for a resource finds
    nothing and returns no error. ``floci.GraphIdentifier`` names the
    environment's graph; the control plane's is a different instance under a
    different deployment id. Using the former for the latter left the
    control-plane graph stopped after every restart while the code read as
    correct, and surfaced four layers away as Trino failing to resolve
    ``global-graph``.

    **MEDIUM: k3s operator provisioning is intricate.** CoreDNS custom records
    pointing ``neuronsphere`` at the shared Floci, Traefik
    ingress-class patching (emulating the ``alb`` class rather than editing
    charts), node topology labels, stale-Node and orphaned-PV reaping, and
    ``cluster_incarnation_id`` fingerprinting via the ``kube-system`` namespace
    UID. All of it is plain Kubernetes API work, so under SPEC008 it becomes
    ``RunKube`` scripts and ports mechanically -- but it is where the local
    platform's hard-won bug fixes live and it must be ported wholesale, not
    reconstructed. Batching it into per-phase scripts is the part needing care:
    a step silently dropped from a batch fails later and elsewhere.

    Ported wholesale as specified, with ``RunKube`` execing into the k3s node
    rather than launching projectbuilder. One addition the Python does not
    have: a stopped container's empty ``IPAddress`` renders as the literal
    string ``invalid IP``, which is truthy -- so a CoreDNS record was being
    written for a container that was not running. Addresses are parsed now.

    **LOW: Jinja2 template syntax.** No bundled plugin defines ``templates``
    entries. User-authored templates need a syntax note; ``pongo2`` is a
    last-resort fallback.

    **LOW: Robot Framework parity suites.** Tests invoke a CLI as a
    subprocess; parameterising the binary is a fixture change.

    **RETIRED -- MEDIUM: projectbuilder becomes an earlier prerequisite.**
    This rested on kubectl running inside projectbuilder, which it cannot: no
    published tag ships one (SPEC008). Kubernetes work execs into the k3s node,
    so projectbuilder is a deploy-time dependency only, exactly as before. It
    is still pulled early, because a first deploy otherwise pays for it at the
    least convenient moment.

    A different pre-flight turned out to matter more. Floci spawns its database
    and graph backends from image references pinned into its own configuration,
    and a reference that resolves nowhere is not reported as an error: the
    instance goes to state ``failed`` while Terraform polls "Still creating..."
    indefinitely. Both references are now checked before a bootstrap deploys
    anything, so a wrong registry or version costs seconds and names the
    variable at fault.

    **GAP: two host-only helm paths are dropped rather than ported.** The direct
    ``helm upgrade --install`` of ext-secrets (dead on the default path) and the
    one-time ``helm uninstall traefik`` migration have no ``nsctl`` equivalent.
    A cluster predating the Traefik migration, or a user who disables
    ext-secrets without declaring it in the BOM, needs the Python CLI. Both
    cases must be detected and named, not silently mishandled.

    **GAP: Platform mode is not ported.** Users on
    ``HMD_LOCAL_NEURONSPHERE_MODE=platform`` stay on the Python CLI. This is
    intentional (see Scope) and is one more reason coexistence is
    indefinite rather than transitional.

.. spec:: Phased implementation strategy
    :id: HMD_CLI_NERD002_SPEC015
    :links: HMD_CLI_NERD002
    :status: amended

    Each phase is independently shippable and independently useful. Phases 1
    and 2 are verifiable against a platform brought up by the Python CLI, which
    means the port is exercised long before it can break anything.

    **Phases 0-3 have landed.** Phase 3's content changed substantially once
    plugin discovery was withdrawn; its entry below is rewritten to describe
    what was built rather than what was planned. Phases 4-6 are unchanged and
    not started.

    **Phase 0 -- Foundation. [LANDED]** Scaffold ``src/go/nsctl/`` from the
    ``go_cli_repo`` cookiecutter; root ``Makefile``; cobra root with
    ``--home``; ``hmd.env`` loading; ``nsctl version``; CI (build, vet, test).
    *Deliverable:* the binary builds and reports its version.

    **Phase 1 -- Read-only. [LANDED]** ``internal/registry`` (SPEC003) including the
    ``legacy_layout`` case and the literal-JSON fixture; ``nsctl env list``,
    ``nsctl env status``, ``nsctl control-plane status``.
    *Deliverable:* ``nsctl`` accurately describes a platform the Python CLI
    brought up. The registry contract is proven before anything can mutate it.

    **Phase 2 -- Lifecycle without deploys. [LANDED]** Compose invocation and port
    validation; the ``RunKube`` projectbuilder helper and the early image pull
    (SPEC008); Floci provisioning (SPEC007); k3s cluster, operators and
    kubeconfig; nginx routing; ``control-plane start|stop``; ``env start
    --no-deploy``, ``env stop``, ``env purge``. ``RunKube`` lands first in this
    phase -- k3s provisioning, ingress and route discovery all sit on it.
    *Deliverable:* ``nsctl`` brings up a control plane and an environment's
    infrastructure; the Python CLI can then complete the deploy.

    **Phase 3 -- BOM, manifests and the cold bootstrap. [LANDED]** Rewritten:
    the plugin half of this phase is withdrawn (SPEC005), and the cold
    bootstrap moved into it from Phase 2, where the original plan had deferred
    it as "lands with the deploy path".

    What was built: ``internal/manifest`` (the environment manifest, in the
    format ``env_manifest.py`` defines, so either front end reads what the
    other wrote); ``internal/bom`` (SPEC009) with both topological sorts and
    version resolution; ``internal/reconcile`` with golden-vector digest tests
    and ``--force-full-redeploy``; ``repoclass.Produces`` reading a repo's own
    ``meta-data/resources/*.yaml``; the control-plane bootstrap DAG; and the
    verbs ``env add``, ``env delete``, ``env apply``, ``repo add``,
    ``repo remove``, ``repo list`` and ``repo import``.

    *Deliverable:* ``nsctl`` bootstraps a control plane from an empty
    ``HMD_HOME`` and reconciles an environment to its manifest.

    *Verified:* a cold bootstrap of a fresh ``HMD_HOME`` deploys the VPC,
    Postgres, core databases, graph and all three foundation services and exits
    0, with ``hmd-ms-deployment`` answering; a second start skips the
    bootstrap. Against an environment the Python CLI brought up, ``repo
    import`` recovered 29 instances and ``env apply`` then reported "0 to
    deploy, 0 changed, 32 unchanged, 0 deployed but undeclared".

    *Not done:* ``env purge``, and the full parity harness -- parity was
    checked by targeted cross-front-end operations rather than by a suite.

    **Phase 4 -- The runner service. [NOT STARTED]** ``src/go/nsrunner`` with the submit API
    and inherited node execution (SPEC010); the runner selection in
    ``hmd-ms-deployment``; ``nsctl`` attaching to and streaming an in-flight
    deployment.
    *Deliverable:* deploys survive the CLI exiting; ``skip_async`` retires as
    the local marker.

    **Phase 5 -- Parallelism. [NOT STARTED]** In-degree scheduling, the bounded worker pool,
    fail-fast cancellation, and the per-environment mutex (SPEC011).
    *Deliverable:* a cold bootstrap measurably faster than the sequential
    path, with identical outcomes.

    **Phase 6 -- Distribution. [NOT STARTED]** GoReleaser, the Homebrew tap, the install
    script, and documentation making ``nsctl`` the recommended path for new
    users while the Python CLI remains supported.

Dependency Map
--------------

.. list-table::
   :header-rows: 1
   :widths: 25 30 45

   * - Python
     - Go replacement
     - Notes
   * - ``cement``
     - ``spf13/cobra`` v1.8.0
     - House CLI framework; no viper -- ``godotenv`` plus explicit precedence
   * - ``boto3`` / ``botocore``
     - ``aws/aws-sdk-go-v2``
     - Service clients only; the ``resource`` API has no analogue and is
       already unused
   * - ``requests``
     - ``net/http`` (stdlib)
     - The ms-deployment apiop protocol; the largest behavioural surface
   * - ``pyyaml``
     - ``gopkg.in/yaml.v3``
     - Compose files, helm values, PV manifests, env manifests
   * - ``json`` (stdlib)
     - ``encoding/json`` (stdlib)
     - nsplugin.json, manifest.json, environments.json, resources output
   * - ``jinja2``
     - ``text/template`` (stdlib)
     - User-authored plugin templates only; unused by bundled plugins
   * - ``python-dotenv``
     - ``joho/godotenv``
     - ``$HMD_HOME/.config/hmd.env``
   * - ``InquirerPy``
     - ``charmbracelet/huh``
     - Plugin selection and purge confirmation
   * - ``yaspin``
     - ``charmbracelet/lipgloss`` (or plain lines under ``--verbose``)
     - Step spinners; ``--verbose`` must stay pipe-safe
   * - ``importlib.metadata``
     - (no analogue)
     - Entry-point plugins replaced by ``load-plugin`` (SPEC005)
   * - ``hmd_cli_tools``
     - ``internal/tools``
     - Only the handful actually used: ``load_hmd_env``, ``set_hmd_env``,
       ``make_standard_name``, ``get_version``
   * - ``kubernetes`` / helm clients
     - (none needed, and no ``client-go`` either)
     - Already subprocesses; SPEC008 moves them into projectbuilder rather than
       vendoring a Kubernetes client
   * - ``pg8000`` / ``psycopg``
     - (none needed)
     - Already ``docker exec ... psql``
   * - Docker SDK
     - ``os/exec`` (+ ``docker/docker/client`` for inspection)
     - Already subprocess; ``nerdctl`` selection preserved

Risk Assessment
---------------

.. list-table::
   :header-rows: 1
   :widths: 10 38 52

   * - Level
     - Risk
     - Mitigation
   * - High
     - Reconcile digests diverge, forcing a full redeploy on first use
     - Golden vectors captured from the Python implementation; explicit
       ``--force-full-redeploy`` rather than a silent fallback
   * - Medium
     - Entry-point plugin contributions have no Go analogue
     - Bundle format (SPEC005) covers all four existing plugins declaratively;
       a plugin is no longer auto-discovered by being installed, so docs and
       READMEs change in the same wave
   * - Medium
     - A plugin needs computation at load time that no primitive covers
     - Deploy-time execution already exists per instance
       (``src/local/deploy_local.sh`` in projectbuilder); an optional
       ``loader_image`` is reserved but deliberately unspecified
   * - Medium
     - Inverting DAG submission requires a change in ``hmd-ms-deployment``
     - Runner speaks Argo's submit shape; ``skip_async: true`` stays functional
       as a fallback throughout
   * - Medium
     - ``make_standard_name`` divergence breaks Python microservices remotely
     - Table-driven test built from the Python function's outputs
   * - Medium
     - k3s operator provisioning encodes many hard-won fixes
     - Port wholesale rather than reconstruct; it is subprocess work, so the
       port is mechanical
   * - Medium
     - Re-deriving names instead of reading the registry orphans containers
     - SPEC003 makes the registry authoritative; hash retained only as a
       fallback, with a cross-language equality test
   * - Low
     - Jinja2 template syntax differences
     - No bundled plugin uses templates; migration note; ``pongo2`` fallback
   * - Low
     - Robot Framework parity
     - Suites invoke the CLI as a subprocess; parameterise the binary
   * - Medium
     - projectbuilder is now needed to provision a cluster, not just to deploy
     - Pull it once, early in ``control-plane start``, with an explicit
       progress line and an actionable failure; in exchange ``docker`` is the
       only host tool required
   * - Low
     - Platform mode users left behind
     - Intentional; the Python CLI is not deprecated (SPEC012)
