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
    ``stack add``/``remove`` and ``instance add``/``remove``/``import`` with ``--env`` shall
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
    :status: implemented

    ``nsctl env lease acquire --session`` takes a session lease.

    - ``Lease`` gains ``scope`` (``run`` or ``session``; absent means
      ``run``), ``template`` and ``repos`` (absolute paths). ``list`` shows
      all three.
    - The TTL defaults to ``[pool] session_ttl`` (default ``8h``) instead
      of ``10m``.
    - ``--pid`` defaults to the session's process, not the caller's parent.
      The session's process is the nearest ancestor named ``claude``. A
      Claude Code session runs each command in a shell of its own, which
      exits with the command, so the immediate parent would end the lease
      at once. Failing a ``claude`` ancestor, it is the caller's parent: a
      person's interactive shell. Ancestry is read from the process table
      (``sysctl`` on macOS, ``/proc`` on Linux), not by running ``ps``,
      which a sandboxed caller may not be allowed to exec. A session whose
      process dies loses its lease, exactly as a run does.
    - ``nsctl env lease heartbeat [--token]`` renews without naming the
      environment, because the token identifies it. It is meant for a hook
      or a background loop.
    - ``acquire --session --shell`` prints ``export HMD_LOCAL_ENV=<env>`` and
      ``export NSCTL_LEASE_TOKEN=<token>``. ``--json`` carries the same two
      values. From then on every ``nsctl``, ``hmd deploy --local`` and
      ``hmd bender`` in that shell targets the leased environment without
      flags.
    - **Nesting.** When ``NSCTL_LEASE_TOKEN`` names a live session lease on
      environment *X*, ``acquire`` for a run -- bare, naming *X*, or with
      ``--pool`` -- returns that session lease, marked ``nested``, instead
      of contending. A run asking for a *different* environment by name
      contends normally. This lets existing scripts that take run leases
      work unchanged inside a session.
    - A nested run is handed the session's own token, so ``release`` cannot
      tell the run from the session by the token alone. ``release`` of a
      session lease therefore needs ``--session``. Without it, ``release``
      leaves the lease in place, says so, and exits zero: that is what a
      script written for run leases expects. The session's own end -- a
      hook, or a person -- passes ``--session``.
    - ``nsctl env lease whoami`` prints the lease ``NSCTL_LEASE_TOKEN``
      names: environment, scope, holder, template, repositories, expiry and
      the base URL of the environment's routes (``/<env>/<service>/``). It
      exits non-zero when there is none.

    Run leases never take an environment a session holds. The pool treats a
    session-held environment as busy, as it treats any held one today.

.. spec:: A template is a named environment manifest
    :id: HMD_CLI_NEURONSPHERE_NERD035_SPEC003
    :links: HMD_CLI_NEURONSPHERE_NERD035
    :status: implemented

    A template is a file in the environment manifest's own shape
    (``internal/manifest``). It has ``repos``, ``stacks``, ``profiles``,
    ``substrate`` and ``bindings``, and is stored at
    ``$HMD_HOME/templates/<name>.yaml``. Its ``name`` is the template's
    name. There is no second schema.

    - ``nsctl template add <name> <file>`` copies a manifest in.
    - ``nsctl template add <name> --stack <ref>`` runs ``nsctl stack add``'s
      planner (``NERD017``) against an empty manifest and stores the result:
      the stack's declared instances, at their pinned versions and from
      their artifacts, plus its stack record. ``--profile``,
      ``--all-profiles``, ``--lean`` and ``--name`` mean what they mean
      there. The fetch happens here, once, so composing a session from the
      template (SPEC004) is a merge of manifests and needs no network.
    - ``nsctl template add <name> --from-env <env>`` snapshots an existing
      environment's manifest, minus the instances it declares
      ``source: {type: local}`` -- working trees -- and says which it
      dropped. An instance with no ``source`` is kept: it resolves through
      the usual tiers at deploy time.
    - Adding over an existing template is refused without ``--force``.
    - ``nsctl template list|show <name>|remove <name>``.

    Template names follow environment names' rules (lowercase letters,
    digits and hyphens, starting with a letter or digit), up to 63
    characters.

    nsctl ships **no** templates and carries no list of them, for the same
    reason ``NERD017`` gives for stacks. "Analytics" and "telemetry" are
    published stacks, or template files a team checks in and adds.

.. spec:: The session manifest is the template plus the repositories being edited
    :id: HMD_CLI_NEURONSPHERE_NERD035_SPEC004
    :links: HMD_CLI_NEURONSPHERE_NERD035
    :status: implemented

    A **composition** is one manifest built from a template and the
    repositories being edited -- ``--template <t> --repo <path> [--repo
    <path> ...]``:

    1. Start from the template. With no ``--template``, start from an empty
       manifest. The template's top-level ``bindings`` and ``profiles`` are
       dropped: they record one repository's plan, and each repository
       below would read them as its own.
    2. For each ``--repo``, in order, run the planner ``env add --from-repo``
       uses (``planFromRepo``) against the manifest composed so far, with no
       recorded bindings. ``--profile``, ``--all-profiles``, ``--lean`` and
       ``--name`` apply to every repository; a ``--name`` must be used by at
       least one. The repository is declared ``source: {type: local, path:
       <its path>}``; its lock companions ``source: artifact`` at their pinned
       versions.
    3. **Reuse.** What the composition already fills is bound, not declared
       again, by ``NERD017`` SPEC010's rules: a role's resource type is
       already produced, or an instance of the same name and class already
       exists. Two repositories needing Postgres get one Postgres. The
       version already declared is kept. A role nothing fills follows
       ``--from-repo``'s own rules for external and resource-only roles; it
       is not an error the way it is for a stack.
    4. **The repository being edited wins.** When the composition declares
       exactly one other instance of a ``--repo``'s class -- from the template
       or as another repository's companion, in either order -- the working
       tree takes its place and its name, and every dependency and stack
       binding on it follows. Two or more are left as they are and reported:
       which one to replace is not guessed.
    5. **Conflicts are errors.** An instance name the composition already
       gives a different class is refused, naming both, never replaced.
       Two ``--repo`` of the same class are refused.

    The manifest records ``template: <t>``; every working tree is its
    ``source.path``. A composed manifest records no ``bindings`` or
    ``profiles``: it is recomposed, never re-planned.

    ``env add <name> --template <t> --repo <path>...`` composes a named
    environment without a lease. ``acquire --session`` composes the same way
    and writes the result as part of bring-up (SPEC005).

.. spec:: Acquire places the session, then brings the environment up
    :id: HMD_CLI_NEURONSPHERE_NERD035_SPEC005
    :links: HMD_CLI_NEURONSPHERE_NERD035
    :status: implemented

    .. note:: Verified live 2026-10-08 on Colima, with two exec RepoClasses
       from working trees and a substrate-``none`` template. Two sessions took
       ``local`` and a pool-created ``cc-1`` and each deployed its own tree.
       Each lease refused the other's token (exit 3). A released session's
       successor reused ``local`` warm ("0 to deploy", 3.5 s). With ``cc-1``
       released last, a session for the first tree still chose ``local``,
       because ``cc-1`` held the other session's tree. The run found two
       defects, both fixed: bring-up skipped the control-plane start (see
       step 4), and the service image's builder was older than ``go.mod``.

    ``acquire --session`` takes ``--template``, a repeatable ``--repo`` and
    the planner flags ``--profile``, ``--all-profiles``, ``--lean`` and
    ``--name``, and composes the session manifest by SPEC004 *before* it
    takes a lease: a composition that cannot be built costs no lease and
    starts nothing. The lease records the template and the repositories'
    absolute paths.

    **Placement.** A named environment is leased as it is. With ``--pool``,
    ``AcquireFromPool`` scores each free environment against the session
    manifest, lowest first:

    1. An environment whose manifest records a different ``template``
       scores 1000 more, so a same-template environment always wins.
    2. ``manifestDistance(want, have)``, as today.
    3. Plus one for each ``source: local`` instance in *have* that *want*
       does not declare: another session's working tree, which stays
       deployed (below).
    4. Ties go to the most recently released environment, as today.

    With nothing free, a new ``cc-N`` is created if ``[pool] size`` allows
    it. Otherwise the acquire queues with ``--wait`` or fails, as today.

    **Bring-up.** After the grant, a session acquire:

    1. prints the lease -- the ``--shell`` exports, ``--json`` or text -- to
       stdout, so a session that ``eval``\ s it holds its environment even if
       what follows fails;
    2. fetches artifacts the composition needs and the cache lacks, unless
       ``--no-pull``;
    3. writes the session manifest as the environment's manifest, keeping the
       environment's recorded ``substrate`` when the composition names none;
    4. runs what ``env start`` runs -- the control plane if it is down, then
       the environment, ending by applying the manifest. The same function,
       not a copy: the live acceptance found a bring-up that called only the
       environment half waiting five minutes on a Floci nothing had started. ``NERD034`` makes working-tree changes reach the
       environment.

    Progress goes to stderr, keeping stdout to step 1. If bring-up fails, the
    command exits non-zero and the lease is still held, so the session can
    fix and re-run ``nsctl env start`` rather than lose its place.
    ``--no-start`` stops after step 3.

    Instances the previous holder declared and this session does not are
    **not torn down**: nsctl never destroys on a user's behalf, and
    ``env apply`` reports them rather than removing them. That is why
    placement penalises them, and why ``env purge`` (SPEC007) is the reset.

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
    - The environment's ``source: local`` entries are kept in the manifest.
      The next session's composition replaces the manifest: an instance it
      declares again is redeployed from the new tree, and one it does not
      is left deployed and reported (SPEC005), not torn down.
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
    template, instead of failing. SPEC005's bring-up reshapes what it
    declares; what it no longer declares stays until ``env purge``.

What this is not
----------------

- Not a replacement for run leases. CI and one-off runs keep them, with
  their short TTL and no stop on release.
- Not a cloud feature. Every SPEC acts on local environments in one
  ``$HMD_HOME``.
- Not cross-host. Leases are host-local, as today.
- Not a scheduler. There is no priority, preemption or fair share beyond
  arrival order.
