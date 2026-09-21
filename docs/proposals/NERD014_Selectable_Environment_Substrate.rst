.. NERD014 Selectable Environment Substrate

NERD014 Selectable Environment Substrate
========================================

.. req:: Let an environment choose how much substrate it runs: none, core, or full
    :id: HMD_CLI_NEURONSPHERE_NERD014
    :status: implemented

    ``nsctl env start`` shall accept ``--substrate none|core|full`` and shall
    bring up only the infrastructure that mode names: nothing beyond the Floci
    account and the control plane for ``none``; the environment database, the
    graph and the database-account service for ``core``; everything, including
    the k3s cluster and the ``eks-cluster`` DAG node, for ``full``, which
    remains the default.

    The choice shall be recorded with the environment and honoured by every
    later ``env start``, ``env apply``, ``env status`` and ``repo list`` until
    it is changed. Raising the mode shall add what is missing without
    redeploying what exists; lowering it shall never destroy anything.

    .. note::

        ``implemented`` as of 2026-09-17: every SPEC below through SPEC009 is
        built and covered by unit tests (``make check``); SPEC010 was added the
        same day and is still ``proposed``. The live acceptance run against
        ``demo-a`` is recorded under `The acceptance run`_ once it has been
        done; until that entry exists, treat the ``none`` path as verified by
        tests only.

Motivation
----------

Every environment ``nsctl`` starts today gets the same substrate: a Postgres
instance, a Neptune graph, a k3s cluster with its operators and ingress, the
``hmd-ms-dbaccount`` Lambda, and a two-phase ChangeSet that deploys
``local-neuronsphere``, ``base-vpc``, ``environment-db`` and ``eks-cluster``
through ms-deployment before a single declared instance runs. That is right
for the environment it was designed for -- a local twin of a cloud tier, where
the first user chart needs a cluster and the first user service needs a
database -- and it is the cost of parity: ``nsctl env start`` on a warm machine
takes three and a half minutes, and a cold one closer to seven.

It is wrong for the environment NERD009 made possible. A repo class that
deploys with ``exec`` in its own image against infrastructure the customer
already runs -- a docker-compose Postgres, a cron container -- needs none of
it. The Demo 0 acceptance run of 2026-09-16 (``acme-data-platform``, seven
classes, ``nsctl env apply`` twice) spends roughly six of its seven reset
minutes on a cluster nothing schedules onto and a database nothing connects
to, and then advertises a Trino port and a k3s port that mean nothing to the
customer. The runbook has to explain that the wait is not the product.

The same is true, less starkly, of a service-only environment: three Lambdas
that talk to the environment database need ``core`` and not a cluster.

The evidence this document is built on
--------------------------------------

What each step of ``environment.Start`` is *for*, read from the code rather than
the step names:

- ``EnsureRDSRunning`` / ``EnsureNeptuneRunning``
  (``internal/environment/environment.go:135-180``) start containers Floci
  spawned during an earlier DAG and does not restart. They exist for consumers
  of ``database.neuronsphere.io/postgres`` -- ``hmd-ms-dbaccount`` above all
  (``dbaccount.go:39-52`` wires ``env.DBContainer`` into its configuration) --
  and for the graph the ``local-neuronsphere`` core instance advertises.
- The k3s block (``:186-262``) and ``startCluster`` (``:380-490``) exist for
  charts. The DAG runner needs none of it: ``runner.go`` mounts the kubeconfig
  and sets ``KUBECONFIG`` only when ``Kubeconfig != ""``, and the foreign-node
  path's environment is pinned without a cluster
  (``TestForeignEnvIsExactlySPEC005``).
- ``bom.Substrate`` (``internal/bom/bom.go:130-179``) is Phase A of every
  apply. Its four entries are what let *cloud-shaped* repo classes resolve
  their ``base-vpc``, ``environment-db`` and cluster roles locally. A repo
  class whose roles are resource-typed against its own definitions resolves
  against nothing in Phase A.
- ``Seeder.Seed`` (``internal/bom/bom.go:312-460``) talks only to the control
  plane's ms-deployment: repo class versions, base resource definitions,
  ``EnsureEnvironment``, ``EnsureDeploymentSet``, ChangeSet put and apply,
  ``GenerateLocalDeployment``. None of it touches the environment's database
  or graph. **An environment with no substrate at all still deploys ChangeSets.**
- ``k3sEnabled`` (``apply.go:457-463``, ``HMD_LOCAL_NEURONSPHERE_ENABLE_K3S``)
  already removes ``eks-cluster`` and the External Secrets operator from the
  BOM -- but ``Start`` does not consult it, so the k3s container is started
  and provisioned regardless. It is half a mode, expressed as an environment
  variable nothing records.

What the Python front end does with a key it does not know, because the
persistence decision turns on it:

- ``env_registry.load`` rebuilds each ``LocalEnvironment`` from the dataclass's
  known fields and ``save`` writes only those (``env_registry.py:489-491``). A
  registry key ``nsctl`` adds is dropped, silently, the first time the Python
  CLI saves the registry.
- ``parse_manifest`` reads ``environments/<slug>.yaml`` with ``doc.get(...)``
  and never writes it back. An extra key survives.

Scope
-----

In scope: the flag, the three modes, their gating in ``Start`` and ``Apply``,
the recorded mode and every reader of it, the closing summary, ``env status``,
``env list`` and ``repo list``, the refusals, unit coverage, and the docs.

Out of scope: a per-instance or per-role substrate (``core`` with a cluster but
no database); making ``k3sEnabled`` and ``extSecretsEnabled`` modes of their
own (they stay as the environment-variable overrides they are, ANDed with the
mode); any change to the control plane, which is one per machine and is not
substrate; ``hmd neuronsphere up``, which keeps its own behaviour.

Reference: what already exists
------------------------------

- ``cmd/env.go:69-123`` -- ``env start``: ``RequireHome`` → ``resolveStartTarget``
  (registers a first environment, refuses a typo, both before the control
  plane starts) → ``controlplane.Start`` → ``environment.Start``.
- ``internal/environment/environment.go:28-48`` -- ``Options`` shared by
  ``Start``, ``Apply``, ``Stop`` and ``Purge``.
- ``internal/environment/apply.go:53-370`` -- ``Apply``: BOM composition
  (``:87`` substrate, ``:127`` External Secrets), runner configuration
  (``:277-278`` ``K3sCluster``/``Kubeconfig``), Phase A, core Resources
  (``:325-336``), ``provisionNewCluster`` (``:339``), Phase B, snapshot.
- ``internal/manifest/manifest.go:125-160`` -- the environment manifest;
  ``Profiles`` is the precedent for recording a creation-time choice rather
  than recomputing it; ``Extra`` carries unknown keys.
- ``internal/status/status.go:204-254``, ``cmd/env.go:391-443`` -- the status
  rows and their rendering; ``cmd/repo.go:303-306`` prints the reserved names
  as substrate rows.
- ``internal/environment/purge.go:225-248`` -- purge keeps the environment
  manifest; ``env add`` re-adopts it.

.. spec:: Three modes, and what each one runs
    :id: HMD_CLI_NEURONSPHERE_NERD014_SPEC001
    :status: implemented

    The modes shall be ``none``, ``core`` and ``full``; ``full`` is today's
    behaviour and the default for an environment that records nothing.

    .. list-table::
       :header-rows: 1
       :widths: 46 18 18 18

       * - step
         - ``none``
         - ``core``
         - ``full``
       * - control plane; Floci account, health wait, provisioning; ghost
           prune; state dirs; environment routes and reload
         - yes
         - yes
         - yes
       * - environment database (``EnsureRDSRunning``)
         - --
         - yes
         - yes
       * - graph (``EnsureNeptuneRunning``)
         - --
         - yes
         - yes
       * - k3s reconcile, ensure, ``startCluster``, ``VerifyK3sAlive``, and the
           post-apply recovery
         - --
         - --
         - yes
       * - ``hmd-ms-dbaccount`` (``EnsureDBAccount``)
         - --
         - yes
         - yes
       * - BOM Phase A
         - *(empty)*
         - ``local-neuronsphere``, ``base-vpc``, ``environment-db``
         - the three, plus ``eks-cluster`` when k3s is enabled
       * - External Secrets operator; ``provisionNewCluster``; runner
           ``Kubeconfig`` / ``K3sCluster``
         - --
         - --
         - yes
       * - core Resources (``LocalCoreResources`` / ``SubmitResources``)
         - --
         - yes, with no cluster
         - yes

    Under ``none`` the runner's ``Kubeconfig`` shall be empty, not merely
    unused: Docker creates a directory at a bind-mount source that does not
    exist, and ``kubeconfigUnusable`` (``apply.go:600-615``) is the record of
    what that cost last time.

    Under ``none`` the environment routes fragment shall still be rewritten,
    empty, so a ``dbaccount`` route left by an earlier ``full`` run does not
    advertise a service that no longer answers.

.. spec:: The mode is recorded in the environment manifest
    :id: HMD_CLI_NEURONSPHERE_NERD014_SPEC002
    :status: implemented

    The mode shall be recorded as ``substrate: <mode>`` at the top level of
    ``environments/<slug>.yaml``, next to ``profiles``; absent means ``full``.
    ``nsctl env start --substrate <mode>`` shall write it before the control
    plane starts, creating a manifest with an empty ``repos`` list if none
    exists -- the way ``repo add`` already does through ``openManifest``.

    Not in the registry, for the reason under *The evidence*: the Python front
    end would drop it. Not recomputed from the environment's contents, for the
    reason ``Profiles`` gives: a later bare ``env apply`` must not reconcile
    away, or add, a cluster according to a rule nobody re-read. And in the
    manifest rather than a separate file because ``env purge`` keeps the
    manifest and ``env add`` re-adopts it: the mode belongs with the declared
    workloads, since a repo re-added after a purge should not suddenly deploy
    a cluster it never needed.

    Every reader -- ``Start``, ``Apply``, ``env status``, ``env list``,
    ``repo list`` -- shall read the recorded mode through one helper and shall
    treat an unreadable manifest as ``full`` with a warning, never as ``none``.

.. spec:: An unknown mode is refused before anything starts
    :id: HMD_CLI_NEURONSPHERE_NERD014_SPEC003
    :status: implemented

    A value other than the three shall be a usage error (exit ``2``) raised
    before ``controlplane.Start`` -- the ``resolveStartTarget`` rule applied to
    the flag: a typo's cost is one line, not a bootstrap. The same parser shall
    validate the recorded key on read, so a hand-edited manifest fails the
    same way with the file named.

.. spec:: The BOM is composed per mode
    :id: HMD_CLI_NEURONSPHERE_NERD014_SPEC004
    :status: implemented

    ``internal/bom`` shall gain ``SubstrateFor(env, mode, withCluster)``
    beside ``Substrate``: ``none`` yields no entries, ``core`` the first three,
    ``full`` what ``Substrate`` yields today. The External Secrets operator is
    appended only under ``full``, as it is only appended when k3s is enabled
    today; the two gates are ANDed, not replaced. ``IsSubstrate`` and the
    reserved instance names are unchanged -- a name is reserved whether or not
    this environment deploys it.

    Core Resources shall be submitted under ``core`` and ``full`` and skipped
    under ``none``, where the deployment they would attach to does not exist.

.. spec:: Raising the mode adds; it never redeploys
    :id: HMD_CLI_NEURONSPHERE_NERD014_SPEC005
    :status: implemented

    ``nsctl env start --substrate full`` on an environment recorded as ``core``
    or ``none`` shall record the new mode and let the reconcile do the rest:
    the plan (``apply.go:196-235``) finds the missing substrate entries absent
    from the snapshot and adds them; ``Start``'s k3s block marks a missing
    cluster for redeploy as it does on a first start. Nothing already deployed
    is redeployed.

.. spec:: Lowering the mode never destroys
    :id: HMD_CLI_NEURONSPHERE_NERD014_SPEC006
    :status: implemented

    ``nsctl env start --substrate none`` on an environment recorded as ``full``
    shall record the new mode, skip the steps the mode no longer names, and
    leave every container and deployment in place. The reconcile shall report
    the substrate entries as deployed-but-undeclared in the words it already
    uses (``apply.go:229``) and leave them. ``env stop`` stops them;
    ``env purge`` is the teardown. There is no partial teardown, on purpose:
    a cluster's datastore is not something a flag should be able to delete.

.. spec:: Status, lists and the closing summary show only what the mode runs
    :id: HMD_CLI_NEURONSPHERE_NERD014_SPEC007
    :status: implemented

    ``env status`` shall print a ``substrate`` line naming the mode; the
    ``db`` and ``graph`` rows only under ``core`` and ``full``; the ``k3s``
    row, and the ``k3s`` and ``trino`` routes, only under ``full``. ``env
    list`` shall gain a ``SUBSTRATE`` column. ``repo list`` shall print as
    substrate rows only the instances the mode deploys. The closing summary of
    ``env start`` shall print ``Ready.`` and the services line under every
    mode, the database and dbaccount lines under ``core``, and today's lines
    under ``full``.

    A row that would say ``absent`` for something the mode never starts is
    not information; it is the same misreport as the Trino port on an
    environment with no Trino.

.. spec:: Refusals
    :id: HMD_CLI_NEURONSPHERE_NERD014_SPEC008
    :status: implemented

    ``none`` shall be refused, naming the instance and the role, when the
    environment manifest binds any dependency to ``local-neuronsphere``: that
    is the stub NERD013 supplies for a required name-only role, and it is only
    a stub because the core instance exists. Under ``none`` it does not, and
    the ChangeSet would fail later with ``No repo instance`` -- the refusal
    says now what the deploy would say in ten minutes, and names
    ``--substrate core`` as the fix.

    An environment in the legacy layout shall ignore the flag with a warning:
    its database and graph *are* the control plane's, and there is nothing to
    skip.

.. spec:: ``env apply`` and ``--no-deploy`` read the recorded mode
    :id: HMD_CLI_NEURONSPHERE_NERD014_SPEC009
    :status: implemented

    ``env apply`` and ``bom apply`` compose the BOM from the recorded mode with
    no flag of their own; there is one place the mode is set, and it is
    ``env start``. ``env start --no-deploy`` honours the mode for the
    infrastructure steps and skips the apply as it does today.

.. spec:: Foundation Lambdas can be redeployed without a full re-bootstrap
    :id: HMD_CLI_NEURONSPHERE_NERD014_SPEC010
    :status: proposed

    ``nsctl control-plane reset [repo-class...]`` shall redeploy the
    foundation Lambdas (``hmd-ms-naming``, ``hmd-ms-artifact-lib``,
    ``hmd-ms-deployment``, or only the ones named) on a control plane that
    has already bootstrapped, re-resolving each one's version the same way
    ``Bootstrap`` does on a first start. It shall refuse on a control plane
    that has not bootstrapped yet, and shall not touch the VPC, the
    control-plane database, the graph, or any environment's k3s/Postgres/
    graph state -- those do not need a version bump, and this is not
    ``Bootstrap``. ``control-plane stop && start`` does not do this: the
    foundation Lambdas are only ever deployed from inside ``Bootstrap``,
    which only runs once.

What writing it settled
-----------------------

- **``none`` is a real mode, not ``--no-deploy``.** ``--no-deploy`` brings up
  the whole substrate and skips the ChangeSet; ``none`` skips the substrate and
  runs the ChangeSet. They are opposites, and the second is the one a
  customer with their own platform wants.
- **``k3sEnabled`` stays an override.** Turning it into ``core`` would have
  been tidy and would have changed the meaning of an environment variable
  people have set. It is ANDed with the mode: ``full`` with k3s disabled is a
  ``full`` whose cluster is elsewhere, as today.
- **The manifest, not the registry.** Settled by reading what the Python
  saves, not by preference. A Go-only key in a file both front ends write is
  a key that vanishes.
- **No partial teardown.** Lowering the mode was first drafted to stop the
  containers the mode no longer names. That makes a flag on ``start`` a
  destructive operation on ``stop``'s territory; the draft did not survive
  being written down.

The acceptance run
------------------

To be run on the live ``demo-a`` control plane -- the one running on this
machine; ``hmdtr1`` is stopped and a second control plane must not be started
(NERD007). The customer repo is ``acme-data-platform``.

1. ``make reset-a`` with ``nsctl env start local --substrate none``: record
   the wall time against the ~7 minutes of the 2026-09-16 run. ``nsctl env
   status`` prints ``substrate none`` and no ``db``, ``graph`` or ``k3s`` row;
   ``nsctl repo list`` prints no substrate rows.
2. ``nsctl env apply`` twice (the Demo 0 tapes): seven instances ``DEPLOYED``
   through ChangeSets with an empty Phase A.
3. ``nsctl env start local --substrate core`` on the same environment: the
   database and graph appear, the reconcile adds three entries, the seven
   instances are reported unchanged.
4. ``nsctl env start local --substrate bogus`` exits ``2`` before any step
   line is printed.

Things to look at during the run, because the code does not settle them:
whether the reconcile prints a ``Remove`` warning for the absent substrate
entries on every ``core``/``none`` apply of an environment that was once
``full``; whether ``DeclareCoreProduces`` with an empty Phase A is silent;
whether ``Reload`` tolerates the empty routes fragment ``none`` writes.

Risks
-----

- A ``core`` environment whose user chart needs a cluster fails at the chart,
  not at the mode. The failure names the missing ``kubernetes-cluster``
  producer; the docs name the fix.
- The recorded mode and an environment variable (``HMD_LOCAL_NEURONSPHERE_ENABLE_K3S=false``)
  can disagree in a way that reads as ``core``. It is the same disagreement
  that exists today between the variable and ``Start``; this NERD reduces it
  by giving the variable a mode to be ANDed with.
- The Python front end does not know the key. ``hmd neuronsphere up`` on a
  ``none`` environment brings up the full substrate. That is the Python CLI's
  behaviour today and it is unchanged; the manifest key survives the trip.

Acceptance criteria
-------------------

1. ``make check`` green with the unit coverage below.
2. The acceptance run above, steps 1 through 4, on ``demo-a``.
3. ``docs/nsctl.rst`` documents the flag, the key and the three modes.

Unit coverage: flag validation; ``SubstrateFor`` per mode; manifest
round-trip of the key in YAML and JSON and its validation; the pure start
plan per mode; the closing summary per mode; status rows and routes per mode;
``repo list`` substrate rows per mode.

Open questions
--------------

- Whether ``env add --substrate`` should exist so a mode can be chosen before
  the first start. Deferred: ``env start`` is the first command a new install
  runs, and it registers the environment.
- Whether ``core`` should be the default once the local Trino and Superset
  charts move behind their own plugin. Not this NERD's call.
