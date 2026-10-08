.. NERD035 Session Environments And Templates

NERD035 Session Environments And Templates
==========================================

.. req:: A working session gets its own environment, shaped from a template, for as long as it works
    :id: HMD_CLI_NEURONSPHERE_NERD035
    :status: proposed

    A session is one engineer or one coding agent. It shall be able to lease
    one environment for as long as it works, with one command that names a
    **template** and the **repositories it is editing**. The environment
    shall come up holding the template's instances. The session's
    repositories shall be deployed from their working trees, and every lock
    companion they need shall be deployed with them. No other session shall
    deploy into, stop or purge it while the lease holds.

    When the session ends, its environment shall be stopped and kept warm.
    The next session that asks for the same shape shall get it back and
    redeploy only the difference. Nothing shall purge an environment unless
    a person asks for it.

Motivation
----------

The run leases in ``docs/environments.rst`` ("Run leases and the pool")
answer the question "may this deploy, this suite, this verify use an
environment right now". They are deliberately short. The TTL defaults to
ten minutes, the lease watches the caller's parent process, and the
environment goes back to the pool when the run ends. That suits CI and
one-off ``hmd bender`` runs.

That is not how work is done locally. An engineer, or a Claude Code
session, works on a *set* of repositories: say ``hmd-inf-clickhouse`` and
``hmd-inf-otel-collector``, or ``hmd-inf-trino`` and ``hmd-app-airflow``.
It deploys them and their dependencies into one environment, then iterates
for hours. A second session does the same with a different set. Under run
leases:

* The session loses its environment between runs. Another session's run can
  be placed there by the pool, because it scored closest, and redeploy over
  the first session's working-tree instances.
* Every run has to hand-write the ``--for`` manifest the pool scores
  against. Nothing composes "the analytics stack plus the two repositories I
  have open".
* A newly created ``cc-N`` comes back registered but not started. The caller
  has to run ``nsctl env start`` and ``nsctl env apply`` itself.
* Nothing ever stops or removes a pool environment. ``env purge`` takes one
  name, or every environment and the control plane together.

And there is a defect underneath all of it. The ``env lease`` help says that
while an environment is leased, "env apply, stop and purge from anyone else
are refused". Nothing does that refusing: ``lease.Store.Check`` has no
caller. Today a lease is advisory. SPEC001 makes it binding, because a
session lease that does not keep other sessions out is not worth taking.

Environments are expensive. Each one has its own k3s cluster, Postgres,
graph, port slot and Floci account, and an analytics stack takes minutes to
deploy. That is why the answer is not "a fresh environment per session,
thrown away at the end". It is a warm pool, extended so that a session can
hold a member for its whole life and the member is shaped by a template.

Scope and terminology
---------------------

* A **run lease** is today's lease. It is unchanged.
* A **session lease** is a lease with ``scope: session``. It is long-lived,
  heartbeated, and watches the session's process.
* A **template** is a named environment manifest that a session environment
  starts from.
* The **session manifest** is the template's manifest merged with one
  ``--from-repo`` plan per repository the session is editing.
* A **pool environment** is a configured ``[pool] members`` entry, or a
  ``cc-N`` the pool created.

.. spec:: A lease is enforced by every command that changes its environment
    :id: HMD_CLI_NEURONSPHERE_NERD035_SPEC001
    :links: HMD_CLI_NEURONSPHERE_NERD035
    :status: implemented

    ``nsctl env apply``, ``env stop``, ``env purge``, ``env delete``,
    ``stack add``/``remove`` and ``repo add``/``remove`` with ``--env`` shall
    check the target environment's lease before they change anything:

    - **No live lease:** proceed, as today.
    - **A live lease, and the caller presents its token** (``--lease-token``
      or ``NSCTL_LEASE_TOKEN``): proceed.
    - **Otherwise:** refuse with a usage error that names the holder, its
      host and PID, and when the lease expires. This is the same text
      ``lease.HeldError`` already renders.

    Read-only commands (``status``, ``plan``, ``list``, ``credentials``) are
    never refused.

    ``--ignore-lease`` overrides the check. It is the same escape hatch
    ``acquire --steal`` already is, and it shall print whose lease it
    ignored.

    ``hmd deploy --local`` and ``hmd bender`` run through nsctl or the
    deployment service. They are not nsctl commands, so they are out of this
    SPEC. They are guarded by the environment selection in SPEC002: a
    session that exported its environment deploys only there.

.. spec:: A session lease lives as long as the session
    :id: HMD_CLI_NEURONSPHERE_NERD035_SPEC002
    :links: HMD_CLI_NEURONSPHERE_NERD035
    :status: proposed

    ``nsctl env lease acquire --session`` takes a session lease.

    - ``Lease`` gains ``scope`` (``run`` or ``session``; absent means
      ``run``), ``template`` and ``repos`` (absolute paths). ``list`` shows
      all three.
    - The TTL defaults to ``[pool] session_ttl`` (default ``8h``) instead
      of ``10m``.
    - ``--pid`` defaults to the session's process, not the caller's parent.
      The session's process is the nearest ancestor that is an interactive
      shell or a ``claude`` process; failing that, the caller's parent, as
      today. A session whose process dies loses its lease, exactly as a run
      does.
    - ``nsctl env lease heartbeat [--token]`` renews without naming the
      environment, because the token identifies it. It is meant for a hook
      or a background loop.
    - ``acquire --session --shell`` prints ``export HMD_LOCAL_ENV=<env>`` and
      ``export NSCTL_LEASE_TOKEN=<token>``. ``--json`` carries the same two
      values. From then on every ``nsctl``, ``hmd deploy --local`` and
      ``hmd bender`` in that shell targets the leased environment without
      flags.
    - **Nesting.** When ``NSCTL_LEASE_TOKEN`` names a live session lease on
      environment *X*, ``acquire`` for a run, whether bare or naming *X*,
      returns that session lease instead of contending. ``release`` of a
      nested run is a no-op. A run asking for a *different* environment
      contends normally. This lets existing scripts that take run leases
      work unchanged inside a session.
    - ``nsctl env lease whoami`` prints the lease ``NSCTL_LEASE_TOKEN``
      names: environment, scope, template, repositories, expiry and the
      environment's URLs. It exits non-zero when there is none.

    A session lease never queues behind run leases for the same environment,
    and run leases never take an environment a session holds. The pool
    treats a session-held environment as busy, as it treats any held one
    today.

.. spec:: A template is a named environment manifest
    :id: HMD_CLI_NEURONSPHERE_NERD035_SPEC003
    :links: HMD_CLI_NEURONSPHERE_NERD035
    :status: proposed

    A template is a file in the environment manifest's own shape
    (``internal/manifest``). It has ``repos``, ``stacks``, ``profiles``,
    ``substrate`` and ``bindings``, and is stored at
    ``$HMD_HOME/templates/<name>.yaml``. Its ``name`` is the template's
    name. There is no second schema.

    - ``nsctl template add <name> <file>`` copies a manifest in.
    - ``nsctl template add <name> --stack <ref>`` writes a template whose
      only content is that stack record, resolved exactly as
      ``nsctl stack add`` resolves it (``NERD017``).
    - ``nsctl template add <name> --from-env <env>`` snapshots an existing
      environment's manifest, minus its ``source: local`` instances.
    - ``nsctl template list|show <name>|remove <name>``.

    nsctl ships **no** templates and carries no list of them, for the same
    reason ``NERD017`` gives for stacks. "Analytics" and "telemetry" are
    published stacks, or template files a team checks in and adds.

.. spec:: The session manifest is the template plus the repositories being edited
    :id: HMD_CLI_NEURONSPHERE_NERD035_SPEC004
    :links: HMD_CLI_NEURONSPHERE_NERD035
    :status: proposed

    ``acquire --session --template <t> --repo <path> [--repo <path> ...]``
    composes one manifest:

    1. Start from the template. With no ``--template``, start from an empty
       manifest.
    2. For each ``--repo``, run the planner ``env add --from-repo`` uses
       (``planFromRepo``, ``cmd/fromrepo.go``), with its ``--profile`` and
       ``--name`` handling. The repository is declared ``source: local``.
       Its lock companions are declared ``source: artifact`` at their pinned
       versions.
    3. Merge by instance name. An instance declared by more than one input
       with the same class and version is declared once. A repository being
       edited always wins over a template or companion declaration of the
       same class: that is the point of editing it. Any other disagreement
       about class or version is an error that names both sources. It is not
       resolved silently.

    The manifest records ``template: <t>``, and every ``source: local``
    entry records which repository path it came from. That is what SPEC005
    scores and what SPEC006 marks stale.

    This generalizes ``env add --from-repo`` from one repository to *n*.
    ``env add`` gains the same repeatable ``--repo`` and ``--template``
    flags, for someone who wants a named environment without a lease.

.. spec:: Acquire places the session, then brings the environment up
    :id: HMD_CLI_NEURONSPHERE_NERD035_SPEC005
    :links: HMD_CLI_NEURONSPHERE_NERD035
    :status: proposed

    **Placement** uses ``AcquireFromPool`` with the session manifest as
    ``--for``. Scoring extends ``manifestDistance``, lowest first:

    1. Free environments whose manifest records the same ``template``, then
       the rest.
    2. ``manifestDistance(want, have)``, as today.
    3. Plus one for each ``source: local`` instance in *have* that *want*
       does not declare. That is another session's stale working tree,
       which would have to be pruned.
    4. Ties go to the most recently released environment, as today.

    With nothing free, a new ``cc-N`` is created if ``[pool] size`` allows
    it. Otherwise the acquire queues with ``--wait`` or fails, as today.

    **Bring-up.** After the grant, a session acquire:

    1. writes the session manifest as the environment's manifest;
    2. runs ``env start`` if the environment is stopped or newly created;
    3. runs ``env apply --prune``, so the environment matches the session
       manifest. That removes another session's stale instances. NERD034
       makes working-tree changes reach the environment.

    Progress streams as ``env apply``'s does. If bring-up fails, the lease is
    still held, so the session can fix and re-apply rather than lose its
    place. ``--no-start`` grants the lease and stops after step 1.

.. spec:: A session's end stops its environment and never purges it
    :id: HMD_CLI_NEURONSPHERE_NERD035_SPEC006
    :links: HMD_CLI_NEURONSPHERE_NERD035
    :status: proposed

    A session lease that ends shall **stop** its environment, by the same
    path as ``nsctl env stop``. Stopping keeps the cluster datastore,
    volumes and manifest. The lease can end on release, on TTL expiry, or on
    the death of its process.

    - The stop runs in whichever nsctl process ends the lease. ``Release``
      runs it directly. An expired or orphaned lease is reaped by
      ``Store.current`` under the host lock, and the reaper runs the stop
      after it drops the lock. The reaper never holds the lock across a
      container operation.
    - The ``.released`` timestamp is written as today. Warmth ordering
      depends on it.
    - The environment's ``source: local`` entries are kept in the manifest
      and marked ``stale: true``. The next session's apply either replaces
      them (same repository, newer tree) or prunes them (SPEC005).
    - ``acquire --session --keep-running`` and ``release --keep-running``
      skip the stop, for someone who expects to come back within minutes.

    **Nothing purges automatically.** Not acquire, not release, not expiry,
    and not a pool or capacity limit. Stopping is the only side effect nsctl
    performs on its own. Purging is SPEC007, and only a person runs it.

    Run leases are unchanged: ending a run lease does not stop anything.

.. spec:: env purge selects stale environments; a person runs it
    :id: HMD_CLI_NEURONSPHERE_NERD035_SPEC007
    :links: HMD_CLI_NEURONSPHERE_NERD035
    :status: proposed

    ``nsctl env purge`` gains selectors instead of a separate garbage
    collection command:

    .. code-block:: text

       nsctl env purge <name>
       nsctl env purge --idle <duration> [--dry-run] [--yes]
       nsctl env purge --keep <n>        [--dry-run] [--yes]
       nsctl env purge                   # unchanged: everything, control plane included

    - ``--idle`` selects pool environments released longer ago than
      ``<duration>``.
    - ``--keep`` selects all but the ``<n>`` most recently released pool
      environments.
    - A selector is mutually exclusive with a name.
    - A selector **never** selects the control plane. It never selects a
      configured ``[pool] members`` entry, an environment outside the pool,
      or an environment with a live lease of either scope. In particular, a
      selector shall not fall through to bare ``purge``'s purge-everything
      meaning, even when it selects nothing. Selecting nothing prints
      "nothing to purge" and exits zero.
    - Each selected environment is purged by the same per-environment path
      as ``purge <name>``, one at a time, and the command stops at the first
      failure.
    - ``--dry-run`` prints the selection with each environment's idle age,
      template and stale repositories. Without ``--yes``, the command shows
      the same list and asks for confirmation.

    ``env status`` and ``env lease list`` show, for every stopped pool
    environment, its idle age and template, so a person can see when a purge
    is worth running.

.. spec:: The pool has a running budget, and it waits rather than evicts
    :id: HMD_CLI_NEURONSPHERE_NERD035_SPEC008
    :links: HMD_CLI_NEURONSPHERE_NERD035
    :status: proposed

    ``[pool]`` in ``nsctl.toml`` gains:

    .. code-block:: toml

       [pool]
       size        = 4      # environments the pool may register (existing)
       members     = ["local"]
       session_ttl = "8h"   # SPEC002
       max_running = 2      # environments a session acquire may leave running

    A session acquire that would start an environment while ``max_running``
    pool environments are already running behaves like a busy pool. It
    queues with ``--wait`` and fails without it. It shall never stop or
    purge another session's environment to make room.

    ``size`` counts registered environments, stopped or running. When every
    one is registered, acquire reuses the closest free one, whatever its
    template, instead of failing. SPEC005's ``--prune`` reshapes it.

What this is not
----------------

- Not a replacement for run leases. CI and one-off runs keep them, with
  their short TTL and no stop on release.
- Not a cloud feature. Every SPEC acts on local environments in one
  ``$HMD_HOME``.
- Not cross-host. Leases are host-local, as today.
- Not a scheduler. There is no priority, preemption or fair share beyond
  arrival order.
