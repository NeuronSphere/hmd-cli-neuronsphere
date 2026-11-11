.. NERD036 Bundle Profiles Locally

NERD036 Bundle Profiles Locally
===============================

.. req:: Switch a local environment between a bundle's profiles with one command
    :id: HMD_CLI_NEURONSPHERE_NERD036
    :status: proposed

    ``nsctl`` shall read the top-level ``profiles`` and ``default_profiles`` a
    bundle file declares (``NERD0010_HMD_MS_DEPLOYMENT_SPEC0010``) and plan a
    local environment from them with the same meaning the deployment service
    gives them: the same roles included, the same members wired to each other,
    and the same configuration reaching each member.

    Adding a bundle again with a different profile set shall switch the
    environment to it. Members that stay shall be rewired. Members the new
    set leaves out shall, when the user asks, be undeclared and torn down --
    only those the bundle alone declared.

Motivation
----------

The ``telemetry`` bundle in ``hmd-bundle-core`` is getting three profiles:
``clickhouse`` (the OTel collector writing to ClickHouse), ``lake`` (the
collector writing only to its S3 bucket, read with DuckDB) and ``hybrid``
(both, with ClickHouse as a hot tier of a few days). The collector picks its
exporters from the dependency roles it is bound to, so a profile is a choice
of members and, through that, of the collector's wiring. Comparing the
architectures means switching one local environment between them, more than
once, and measuring each (``HMD_LIB_OTEL_DUCKDB_NERD002``).

An audit of nsctl (2026-10-30) found what stands in the way:

* ``internal/localspec/bundle.go`` gates optional roles by profile only
  through ``local.roles.<role>.profiles``, a second vocabulary the server
  does not read. A required role is never gated, and every telemetry role is
  required.
* It ignores ``default_configuration`` and ``config_map``, so a profile's
  configuration (the hot tier's retention, the debug service's backend)
  never reaches a member.
* It wires only what ``bind`` names. The server binds a member dependency
  role to the bundle role of the same name when ``bind`` is silent
  (``bundle_resolution.py:369-378``), so the server wires the collector to
  ``otel-storage``, ``clickhouse``, ``eks-alb`` and ``ext-secrets`` and the
  local plan does not.
* ``instanceOf`` covers every role, so a companion's dependency on a role
  its profile leaves out is declared anyway, dangles, and fails at
  ``env apply`` with "No repo instance", or silently wires to a stale
  instance still in the graph.
* Adding a bundle again keeps instances the new plan leaves out, but the
  record then forgets them: ``Bindings`` keeps only the new plan's
  (``fromrepo.go:595``) and ``Declared`` only this run's additions
  (``stack.go:288-293``). ``bundle remove`` can no longer undeclare them, and
  switching back treats them as foreign and binds them instead of
  re-declaring them, so their dependencies and configuration are never
  updated.
* ``env apply`` never destroys anything. An undeclared instance that is
  still deployed is warned about and left running (``apply.go:254-258``);
  only ``env purge`` removes it. A ``lake`` trial would keep paying for
  ClickHouse.
* ``bundle add`` takes only an OCI reference, and ``--from-repo`` refuses a
  repository that ships several bundles, so ``hmd-bundle-core``'s working
  tree cannot be tried without pushing it to a registry first.

.. spec:: Top-level profiles in a bundle file
    :id: HMD_CLI_NEURONSPHERE_NERD036_SPEC001
    :links: HMD_CLI_NEURONSPHERE_NERD036
    :status: proposed

    ``localspec`` shall read a bundle file's top-level ``profiles`` and
    ``default_profiles`` as ``NERD0010_HMD_MS_DEPLOYMENT_SPEC0010`` defines
    them, expand ``extends``, and gate each role an expanded profile lists by
    that profile (and every profile that extends it), so the existing planner
    sees the profiles it already understands. A role a profile lists is gated
    whatever its ``required`` says, as on the server. ``localspec`` shall
    report the same validation problems the server rejects at registration.

    ``local.roles.<role>.profiles`` and ``local.default_profiles`` keep
    working. A file that uses both forms for one role, or both default lists,
    is a validation problem.

.. spec:: Bundle configuration reaches members locally
    :id: HMD_CLI_NEURONSPHERE_NERD036_SPEC002
    :links: HMD_CLI_NEURONSPHERE_NERD036
    :status: proposed

    At plan time, when the active profiles are known, the planner shall build
    the bundle's effective configuration as the server does --
    ``default_configuration``, then each active profile's ``configuration``
    -- and copy it into each companion's ``instance_configuration`` through
    the role's ``config_map``, on top of the role's static
    ``instance_configuration``. ``config_schema`` is checked against it; a
    failure stops the plan.

.. spec:: Binding as the server binds
    :id: HMD_CLI_NEURONSPHERE_NERD036_SPEC003
    :links: HMD_CLI_NEURONSPHERE_NERD036
    :status: proposed

    At plan time, where the cached class trees are available, each
    companion's dependency roles are read from its class manifest's
    ``deploy.dependencies``. A role ``bind`` does not name is bound to the
    bundle role of the same name when the bundle has one.

    After activation, every companion's dependencies are filtered to the
    instances the active profiles select: a scalar target outside the set is
    dropped and a list target is filtered, as the server drops a binding to
    an omitted role. The planner neither errors nor declares a left-out role
    to fill it. ``ToBundle`` stays the inverse of this.

.. spec:: Switching profiles with bundle add
    :id: HMD_CLI_NEURONSPHERE_NERD036_SPEC004
    :links: HMD_CLI_NEURONSPHERE_NERD036
    :status: proposed

    The bundle record shall keep every instance it declared until the
    instance is undeclared. An instance a re-add leaves declared stays in the
    record's ``Declared`` and counts as owned when composing, so switching
    back re-declares it with the new plan's dependencies and configuration
    instead of binding it as foreign.

    ``nsctl bundle add`` for a bundle the environment already records
    re-declares the record's instances with the new plan's dependencies and
    configuration. ``NERD017`` SPEC010's "bound rather than declared twice"
    applies only to instances another record or the user declared.

    With ``--prune``, an instance the record owns that the new plan does not
    ask for is undeclared. The rule is ``stack remove``'s guard (``NERD017``
    SPEC004), extracted from ``cmd/stack.go`` into one helper that both verbs
    use: an instance another record, the manifest's own bindings or the
    substrate reserves is kept. The verb prints what it undeclares and what
    it keeps, and why. With ``--apply``, the undeclared instances are then
    torn down (SPEC007). Without ``--prune`` they stay declared, as today, and
    the output names them and the flag.

.. spec:: Test coverage
    :id: HMD_CLI_NEURONSPHERE_NERD036_SPEC005
    :links: HMD_CLI_NEURONSPHERE_NERD036
    :status: proposed

    Unit tests shall cover:

    - each validation problem;
    - ``extends``, and a profile gating a required role;
    - configuration precedence and ``config_map``;
    - same-name binding, and an unwired binding to a left-out role;
    - switching a telemetry-shaped bundle from ``clickhouse`` to ``lake`` and
      back, with and without ``--prune``, including an instance another
      record shares, and checking the record still owns what it declared;
    - history entries across add, switch and remove;
    - teardown order, and keeping a shared instance;
    - ``bundle add --from``.

    An end-to-end run shall switch a local environment through all three
    telemetry profiles with ``--prune --apply``. After each switch it checks
    the collector's exporters, which instances are deployed (ClickHouse gone
    under ``lake``), and the hot tier's retention.

.. spec:: Profile history
    :id: HMD_CLI_NEURONSPHERE_NERD036_SPEC006
    :links: HMD_CLI_NEURONSPHERE_NERD036
    :status: proposed

    The environment manifest gains a top-level ``bundle_history`` list,
    which outlives the bundle records, so ``bundle remove`` does not erase it.
    An entry is: bundle name, version, profiles, the release or source
    reference when the bundle came from a published artifact, the members
    (role, instance, provisioned or reused), ``declared_at``,
    ``converged_at`` and ``ended_at``. These are the fields of
    ``NERD0010_HMD_MS_DEPLOYMENT_SPEC0014``, so local and server history
    feed the same attribution (``NERD0010_HMD_MS_DEPLOYMENT_SPEC0016``).

    * ``bundle add`` appends an entry when the version, release or profiles change,
      and closes the previous entry for that bundle.
    * ``bundle remove`` closes the open entry.
    * ``env apply`` sets ``converged_at`` on the open entry the first time it
      finishes with no failures, and writes the manifest to record it.
      Today apply writes only the state-directory snapshot.

    These are the local trial windows ``HMD_LIB_OTEL_DUCKDB_NERD002``
    compares. ``nsctl bundle history [<name>] [--json]`` prints them.

.. spec:: Tearing down an undeclared instance
    :id: HMD_CLI_NEURONSPHERE_NERD036_SPEC007
    :links: HMD_CLI_NEURONSPHERE_NERD036
    :status: proposed

    ``nsctl env apply --prune`` shall destroy each instance the reconcile
    plan puts in ``Remove`` (deployed, no longer declared). It goes in reverse
    dependency order, after every add and change in the same apply has
    succeeded. Each destroy goes through the instance's own deploy tools in
    reverse of its deploy order: ``helm uninstall`` of the release (and its
    namespace) and ``cdktf destroy`` of the stack, against the environment's
    Floci and k3s. It then removes the instance from the deployment service's
    graph and from the state snapshot.

    * An instance that a still-declared instance depends on is not
      destroyed; the plan reports it as a conflict.
    * ``env plan --prune`` shows what would be destroyed.
    * Without ``--prune``, apply warns as today.
    * A failed destroy leaves the instance in ``Remove`` with the error, and
      the next ``--prune`` retries it.

    ``bundle add --prune --apply`` passes ``--prune`` through.

.. spec:: Adding a bundle from a working tree
    :id: HMD_CLI_NEURONSPHERE_NERD036_SPEC008
    :links: HMD_CLI_NEURONSPHERE_NERD036
    :status: proposed

    ``nsctl bundle add --from <dir> [--bundle <name>]`` shall plan from a
    repository's working tree instead of a fetched artifact.

    * ``--bundle`` names one of the repository's bundle files; it is required
      when the repository ships more than one.
    * Member classes resolve as ``env add --from-repo`` resolves them, so no
      ``neuronsphere.lock`` is needed in the bundle repository.
    * It records a bundle record as an OCI add does, with ``Ref`` set to the
      directory, so switching, ``--prune``, history and ``bundle remove``
      behave the same.

    ``localspec.Load`` gains the same bundle-name selector.

What this is not
----------------

- Not new profile semantics. The deployment service's NERD0010 SPEC0010
  defines them, and this reads them.
- Not data migration. Tearing down ClickHouse loses what it held; the
  collector's S3 objects stay.
