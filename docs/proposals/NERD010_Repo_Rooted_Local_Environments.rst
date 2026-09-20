.. NERD010 Repo-Rooted Local Environments

NERD010 Repo-Rooted Local Environments
======================================

.. req:: Stand up a local environment for a repo from the repo itself
    :id: HMD_CLI_NEURONSPHERE_NERD010
    :status: implemented

    A repository shall be able to **declare, in files it checks in, the local
    environment it needs in order to be tested** -- the versioned RepoClass
    artifacts to stand up alongside it, and which of them are optional.

    ``nsctl`` shall be able to read that declaration and, in **one command**,
    create an environment from it, fetch every artifact it names, and deploy
    the repo into it.

    The declaration shall come in two files with two different jobs. What the
    repo *wants* is declared by hand in its BACON manifest. What those wants
    *resolve to* is pinned in a generated, checked-in
    ``neuronsphere.lock`` at the repository root.

    Optionality shall be expressed as **profiles**, so that one repository can
    be run lean -- itself and nothing else -- or with the companions that make
    an integration test meaningful, without editing a file to switch.

    **Partly implemented 2026-09-15.** SPEC001, SPEC002, SPEC004, SPEC005,
    SPEC006, SPEC007 and SPEC008 are implemented; SPEC003 is partial, because
    tier 3 -- resolving a range against a librarian -- is ``NERD011``'s and is
    deliberately not built here. Tiers 1 and 2 are a usable product on their own,
    which is why they were built first.

    It stays ``partial`` because three of the eight acceptance criteria have not
    been run against a live platform. Criteria 2, 3, 4, 7, 8 and 9 were exercised
    on 2026-09-15 against a scratch ``HMD_HOME`` with no containers, and hold:
    ``--lean`` deploys the unconditional entries alone and no gated ones;
    ``--profile`` adds exactly the entries naming it; a second bare ``env apply``
    reconciles to the same set from the recorded ``profiles`` and ``bindings``,
    *including* for a lean environment; ``--check`` passes a current lock and
    fails a stale one without writing; an unknown schema version is refused
    naming what was found and what is supported; and one lock applied twice under
    different ``--name`` overrides resolves every role in both.

    **The live run followed the same day, and every criterion holds.** Against
    the real control plane, with ``$HMD_REPO_HOME`` pointed at an **empty
    directory** throughout:

    - ``nsctl env apply scratch --from-repo`` declared two instances, skipped
      ``base-vpc`` as substrate, and deployed both --
      ``nerd010-bucket (hmd-inf-s3bucket@0.1.13)`` from its **artifact** and the
      repository itself from its working tree. Floci holds the bucket the CDKTF
      deploy created, named
      ``nerd010-bucket-hmd-inf-s3bucket-scratch-scratch-reg1-hmdtr1`` -- so the
      ``--name`` override reached real infrastructure, which is the strongest
      available statement of SPEC005's naming order.
    - ``nsctl repo list`` reported that instance ``FROM artifact`` while a
      checkout of ``hmd-inf-s3bucket`` sat in the real ``$HMD_REPO_HOME``,
      confirming that distribution beats an unasked-for checkout on the
      environment path as well as the control-plane one.
    - A second bare ``env apply`` planned "0 to deploy, 5 unchanged": the
      reconcile is idempotent across the repo-rooted path.
    - ``nsctl lock --from-env scratch`` reproduced the same versions with
      ``generated_from = "env:scratch"``, and ``--check`` passed on the result.

    Criterion 5 is satisfied in the sense that matters: the apply performed no
    artifact fetch, resolving entirely from the cache. The stronger property --
    that resolution *cannot* fetch -- is structural rather than observational,
    since ``internal/artifact`` imports no HTTP client.

    One prediction was wrong and is worth recording: a subject repository
    declaring empty ``deploy.commands`` was expected to be the fragile part of
    the run, and ``ms-deployment`` deployed it without complaint.

.. note::

    This document is the consumer of ``NERD005``. ``NERD005`` makes a RepoClass
    deployable from a versioned artifact; this one makes a *repository* able to
    ask for a set of them. Neither is useful alone: without ``NERD005`` there is
    nothing for a lock to pin, and without this document ``NERD005``'s manifests
    are authored by hand in ``$HMD_HOME`` and never leave the machine.

Motivation
----------

``nsctl`` now gets a newcomer a control plane and an environment substrate from
one binary and Docker. ``NERD009`` gets their own repository deployable into it.
What neither addresses is the step in between, and it is the step that decides
whether any of it is used: **standing up the things the repository needs in
order to be worth testing.**

Today that knowledge lives in people. An engineer who wants to exercise a
service locally has to know that it needs a Trino, that the Trino needs a Hive
metastore, that the version of the metastore that works with the Trino they have
is not the latest one, and that the transform runtime which actually drives
their endpoints is a separate thing again. None of that is written down
anywhere a new joiner can read, and all of it is written down *partially* in chat
threads and in the shape of somebody's ``$HMD_HOME``.

The consequence is not that local development is hard. It is that **local
development is not reproducible**, which is a different and worse problem: two
engineers on the same repository are testing against different platforms and
neither knows it.

What a repository already says, and what it does not
----------------------------------------------------

A BACON manifest already declares the repository's real dependencies, by role,
with a version specifier and a resource:

.. code-block:: json

    "deploy": {
      "dependencies": {
        "neptune-db": {
          "instance_name": "global-graph",
          "repo_class_name": "hmd-inf-neptune",
          "required": "true",
          "version_spec": "~= 0.1"
        }
      }
    }

That is a genuine, single-sourced statement of need and this document does not
duplicate it. Two things it cannot express, and both are load bearing:

**1. Companions that are not dependencies.** Consider a service whose endpoints
are *called by* a Transform, which in turn reads data from a Librarian. Neither
the Transform runtime nor the Librarian is a dependency of the service -- the
arrows point the other way, and in the cloud they are deployed by somebody
else entirely. Locally they are exactly what is needed to see whether the
service works. There is nowhere in BACON to say so, because BACON describes a
deployment graph and this is a *test fixture*.

**2. Optionality with more than one axis.** ``required: "false"`` already marks
a dependency as optional, but it is a single boolean evaluated the same way
every time. What a developer needs is to run lean while iterating on their own
code and run wide before opening a pull request, from the same checkout,
without editing anything.

Scope
-----

**In scope.**

- A ``local`` section in the BACON manifest: profile-gated companion instances,
  and profile-gating of *optional* dependencies.
- ``neuronsphere.lock``: a generated, checked-in, repo-root TOML file pinning
  every declared want to a concrete version and librarian content path.
- ``nsctl lock`` and ``nsctl lock --check``.
- ``nsctl env add --from-repo`` and ``nsctl env apply --from-repo``.

**Out of scope.**

- Deploying a RepoClass from an artifact at all. That is ``NERD005``, and this
  document assumes every one of its SPECs.
- Generating the manifest for a repository that has none. That is ``NERD009``
  ``nsctl repoclass init``, which is a prerequisite for a foreign repo and is
  not restated here.
- Changing how the *cloud* resolves dependencies. ``ms-deployment`` resolves a
  ``version_spec`` against real infrastructure at ``apply_changeset`` time and
  keeps doing exactly that. A lock is a local convenience, never an input to a
  cloud deploy.
- A package index. Wheels are ``NERD006``.
- Any second place to declare resources, databases or dependencies. See
  SPEC008.

Reference: what already exists
------------------------------

.. list-table::
   :header-rows: 1

   * - Piece
     - Where it already is
   * - The declared-instance schema
     - ``manifest.Repo`` -- ``instance_name``, ``repo_class_name``, ``version``,
       ``source``, ``instance_configuration``, ``dependencies`` -- already
       parsed, validated and written by ``nsctl repo add``
   * - The manifest store, with a repo-root tier
     - ``NERD009`` SPEC006: ``meta-data/manifest.toml``, then
       ``meta-data/manifest.json``, then repo-root ``neuronsphere.toml``
   * - Surgical TOML writes that preserve unmodelled keys
     - ``NERD009`` SPEC006 and SPEC008, and the ordered-map rule that comes
       with them
   * - TOML in the Go binary
     - ``go-toml/v2``, already a module dependency
   * - Reading an environment's deployed instances back out of the graph
     - ``nsctl repo import`` -- ``client.EnvironmentInstances`` and
       ``declarationFor``
   * - The content-path grammar
     - ``librarian.Spec.ContentPath()``, which is also BACON's
       ``pre_build_artifacts`` grammar
   * - Creating and reconciling an environment from a manifest
     - ``nsctl env add`` and ``nsctl env apply``

Nothing in this document is a new subsystem. It is a new *section* in a file
that already exists, a new generated file, and three verbs that wrap verbs that
already exist.

.. spec:: The ``local`` section, and profiles rather than groups
    :id: HMD_CLI_NEURONSPHERE_NERD010_SPEC001
    :links: HMD_CLI_NEURONSPHERE_NERD010
    :status: implemented

    A ``local`` section becomes valid at the top level of a BACON manifest,
    read through ``NERD009`` SPEC006's store and therefore available in
    ``manifest.toml``, ``manifest.json`` or a repo-root ``neuronsphere.toml``
    without this document choosing between them.

    .. code-block:: toml

        [local]
        version = 1
        default_profiles = ["transforms"]

        # No profiles key: part of running this repo at all.
        [[local.repos]]
        instance_name   = "app-db"
        repo_class_name = "hmd-inf-postgres"
        version_spec    = "~= 0.8"

        # Started only when "transforms" or "full" is activated.
        [[local.repos]]
        instance_name   = "ms-transform"
        repo_class_name = "hmd-ms-transform"
        version_spec    = "~= 0.5"
        profiles        = ["transforms", "full"]
        dependencies    = { librarian = "data-lib" }

        [[local.repos]]
        instance_name   = "data-lib"
        repo_class_name = "hmd-ms-librarian"
        version_spec    = "~= 0.2"
        profiles        = ["transforms", "full"]

        # Profiles gate optional dependencies too.
        [local.dependencies]
        otel-collector = { profiles = ["full", "telemetry"] }

    **Profiles, and not a second grouping concept.** The semantics are
    ``docker compose``'s, deliberately, because they are one concept instead of
    two and because they are already in the fingers of everyone who will read
    this:

    - an entry carrying a ``profiles`` key starts **only** when at least one of
      those profiles is activated;
    - an entry with no ``profiles`` key **always** starts;
    - therefore **lean requires no declaration at all** -- it is what activating
      nothing gives you, and no author has to remember to define it.

    The rejected alternative was named groups selected by a separate profile
    layer. It was rejected because the two levels buy nothing: a profile can
    already name a bundle (``full``) or a single concern (``transforms``), the
    two overlap freely, and a second level would have to answer what happens
    when a group is in two profiles.

    ``local.repos`` entries are the ``manifest.Repo`` shape, extended with
    ``profiles`` and with ``version`` replaced by ``version_spec`` -- the
    concrete version is SPEC002's job, and an author writing one here would be
    hand-maintaining a lock.

    **Optional dependencies are gateable; required ones are not.** A
    ``required: "false"`` dependency named under ``local.dependencies`` is
    deployed only when one of its profiles is activated. A required dependency
    named there is a manifest error, refused with the reason: ``ms-deployment``
    fails the whole ChangeSet on an unmet required role, and a ``SKIPPED``
    status does not mock one. This matters more than it looks -- without it,
    "lean" only means "no companions", and 53 of the 387 JSON manifests in a
    full workspace carry at least one optional dependency.

    **Gating is opt-in per entry.** An optional dependency that ``local`` does
    not mention keeps exactly today's behaviour. Nothing in a manifest
    distinguishes "optional in the cloud" from "optional for this process to
    boot", so the safe default is that ``nsctl`` changes nothing it was not
    told to change.

    A new ``internal/localspec`` reads the section. Per ``NERD009`` SPEC006 it
    must not model the whole document as a Go struct: the in-memory document is
    an ordered map so that every key ``nsctl`` does not model survives a
    rewrite, because forty Python packages write this file.

    **Amended 2026-09-15.** The section is read out of
    ``meta-data/manifest.json``, and **not** through ``NERD009`` SPEC006's store,
    because that store does not exist. ``NERD009`` is entirely ``proposed``;
    ``go-toml/v2`` is linked into the binary but ``internal/logincfg`` is its
    only importer, and nothing in the module reads a TOML manifest or a repo-root
    ``neuronsphere.toml`` at all. ``internal/localspec`` therefore parses the
    JSON with ``encoding/json``, the way ``repoclass.Manifest``,
    ``librarian.PreBuildArtifacts`` and ``cmd.repoIdentityFrom`` already do.

    Implementing the store on the way past was rejected deliberately: it is
    ``NERD009``'s, it changes what forty Python packages write, and folding it in
    here would leave neither change reviewable. When the store arrives this
    paragraph is the only thing that moves -- ``localspec`` takes its bytes from
    somewhere else and parses the same shape.

    The ordered-map requirement in the paragraph above is **not** met, and does
    not need to be, because this package only *reads*. Every write to the
    ``local`` section is ``NERD009``'s store per SPEC008, so no key nsctl does
    not model is at risk here. A writer must not be grown on top of these
    structs, which model what nsctl understands and would drop the rest.

    **Implemented 2026-09-15** as ``internal/localspec``. Two refusals this
    document did not predict fell out of writing it, and both are the same bug:
    two declarations silently claiming one instance. A ``local.repos``
    ``instance_name`` equal to a dependency **role** is refused, because both
    would default to that name and which one won would depend on iteration
    order; and ``local.dependencies`` naming a role no ``deploy.dependencies``
    entry declares is refused as the typo it is, rather than gating nothing.

    **Amended 2026-09-16: a gate may bind a role, or wire the instance that
    fills it.** The first repository to adopt this section was an application
    rather than a microservice -- ``ev-app-annotation-review``, a Django app
    deployed by Helm -- and its cloud manifest showed two things a profile gate
    cannot say:

    - A **required role filled by a class nothing local can stand in for.**
      Its ``compute`` is ``hmd-inf-eks-node-group`` and its ``ext-secrets`` is
      ``hmd-inf-ext-secrets``; locally the k3s node *is* ``local-neuronsphere``
      and the control plane already deploys ``ext-secrets``. Declaring an
      artifact of either class would deploy a second thing or fail, and
      ``--name compute=local-neuronsphere`` is refused as a substrate name.
    - A **dependency instance that needs wiring of its own.**
      ``deploy.dependencies`` describes an edge, not the node at its far end:
      ``hmd-database-account`` cannot deploy without a ``database-instance``
      and a ``db_name``, and ``hmd-inf-redis`` without a storage class k3s
      has. Only the repository that wants them locally knows which. Companions
      under ``local.repos`` already carry ``dependencies`` and
      ``instance_configuration`` for exactly this reason; dependency wants did
      not, and ``--from-repo`` re-declared them bare on every apply.

    A ``local.dependencies`` entry may therefore also carry ``bind``, or
    ``dependencies`` and ``instance_configuration``, and may do so on a
    **required** role -- what stays refused is ``profiles`` on one:

    .. code-block:: json

        "local": {"version": 1, "dependencies": {
          "compute":        {"bind": "local-neuronsphere"},
          "ext-secrets":    {"bind": "ext-secrets"},
          "db-credentials": {"instance_configuration": {"db_name": "annreview"},
                             "dependencies": {"database-instance": "environment-db",
                                              "create-service": "local-neuronsphere"}}
        }}

    A **bound** role is bound to that instance by the repository and by nothing
    else: nothing is declared for it, the lock does not pin its class (SPEC002;
    a pin nobody reads would only go stale, and ``--pin`` on it is refused
    saying so), ``--name`` cannot rename it, and the plan reports it as
    ``bound: provided by the environment``. ``bind`` beside ``dependencies`` or
    ``instance_configuration`` is refused, because nothing is declared for one
    of them to configure. A **wired** role's ``dependencies`` and
    ``instance_configuration`` ride on the want and are written onto the
    declared instance by every ``--from-repo`` run, so the manifest stays the
    only place they are said. An entry on a required role that says none of
    these is refused as the no-op it is.

.. spec:: ``neuronsphere.lock``
    :id: HMD_CLI_NEURONSPHERE_NERD010_SPEC002
    :links: HMD_CLI_NEURONSPHERE_NERD010
    :status: implemented

    ``nsctl`` shall generate, and the repository shall check in, a file named
    ``neuronsphere.lock`` at the **repository root**, in **TOML**.

    .. code-block:: toml

        version = 1
        repo_class_name = "hmd-ms-myapi"
        generated_from = "env:dev"          # or "pins" | "manifest"

        [[resolved]]
        repo_class_name = "hmd-inf-trino"
        version         = "0.3.12"
        profiles        = []
        satisfies       = "eks-cluster"
        content_path    = "repository:/hmd-inf-trino/0.3.12/hmd-inf-trino_0.3.12_build.zip"

        [[resolved]]
        repo_class_name = "hmd-ms-transform"
        version         = "0.5.201"
        profiles        = ["transforms", "full"]
        content_path    = "repository:/hmd-ms-transform/0.5.201/hmd-ms-transform_0.5.201_build.zip"

    ``profiles = []`` means unconditional. ``satisfies`` records the dependency
    role an entry was resolved for, and is absent for a companion, which is
    what lets a reader tell a real graph edge from a test fixture.

    **The lock pins every profile's entries, not only the activated ones.**
    Activation is a read-time filter. A lock that covered only the profile in
    use when it was generated would force a re-resolve -- and therefore a
    network trip, and therefore a different answer -- the first time anybody
    switched profiles, which defeats the purpose of having a lock.

    ``content_path`` is generated by ``librarian.Spec.ContentPath()`` and never
    by a second format string in this package. BACON's spec grammar and the
    librarian's content paths are one vocabulary; a private copy of the format
    is how they become two.

    The ``version`` key at the top of the file is the lock **schema** version.
    A lock declaring a schema version ``nsctl`` does not know is refused with
    the version it found and the versions it supports -- never partially
    honoured, because a silently-ignored ``resolved`` entry is a missing
    instance nobody looks for.

    **Amended 2026-09-15.** ``satisfies`` is a **list** of roles, not a single
    role. One repo class routinely fills several: ``hmd-inf-credentials`` fills
    ``users``, ``ro-users`` and ``adhoc-users`` in ``hmd-inf-trino``, and
    ``hmd-inf-eks-node-group`` fills ``compute`` and ``worker-compute``. A string
    could not say so, and the alternative -- one entry per role -- would pin the
    same class several times, which is the one thing a lock must not do. An empty
    or absent list still means "a companion", which is the distinction the field
    exists for.

    **No instance name ever appears in a lock**, and this is load bearing rather
    than an omission. Two engineers may deploy the same class at the same locked
    version under **different local instance names**, and both must still resolve
    the same roles from the same checked-in lock: a role is portable across
    machines and an instance name is not. That is why entries are keyed by repo
    class and why ``satisfies`` names roles. What an instance is called locally is
    the environment manifest's business -- see SPEC005's naming order.

    It follows that two declared wants of one class which resolve to *different*
    versions is a refusal naming both, rather than a lock pinning the class twice
    or silently taking whichever was seen first. Profiles are unioned across the
    wants that name a class, and one unconditional want makes the class
    unconditional: a profile is a filter, and something that always starts cannot
    be filtered out by a profile some other entry named.

    **Implemented 2026-09-15** as ``internal/lock``. The file is written through
    ``internal/atomicfile`` rather than ``os.WriteFile``, because a ``pre-commit``
    hook or a CI job may be reading it at that moment.

.. spec:: ``nsctl lock``, and where a version actually comes from
    :id: HMD_CLI_NEURONSPHERE_NERD010_SPEC003
    :links: HMD_CLI_NEURONSPHERE_NERD010
    :status: partial

    ``nsctl lock [<path>]`` reads a repository's ``deploy.dependencies`` and
    its ``local`` section and writes ``neuronsphere.lock``.

    The substance of this SPEC is that **resolving a version specifier to a
    concrete version is the hard part, and the Artifact Librarian may not be
    able to help.** Every manifest in a real workspace writes ranges
    (``"version_spec": "~= 0.1"``); a range needs a list of published versions;
    and the librarian's ``/apiop/get`` takes an exact content path, a
    ``like`` search has been observed to answer ``500``, and its graph queries
    (``new_deployables``, ``new_auto_deploys``, ``new_tagged``,
    ``by_content_item_type_tag``) enumerate *new* content items rather than a
    repository's versions.

    So the tiers are ordered by how certain they are, and the first one needs
    no resolver at all:

    #. ``--from-env <env>`` -- **pin what is actually running.** Read the
       environment's deployed instances out of ``ms-deployment`` and write their
       versions, reusing ``client.EnvironmentInstances`` and ``declarationFor``,
       which ``nsctl repo import`` already has. This is how a lock should be
       born: a known-good environment is the only thing that has ever proved
       these versions work together.
    #. ``--pin <class>@<version>``, repeatable, and any ``version_spec`` that is
       already an exact version, taken verbatim.
    #. Range resolution against a librarian, **only if it is possible.** Whether
       it is shall be settled by a spike before this tier is implemented, and
       the outcome recorded in this SPEC rather than discovered later. The
       candidates, in order: the generic ``search_entity`` CRUD surface over
       ``hmd_lang_artifact_librarian.repo_version``, filtered by the
       ``content_item_has_repo_version`` relationship; and ``/apiop/search``
       with a ``content_item_path`` filter, to confirm or refute the recorded
       ``like``-search failure.

    If tier 3 turns out to be impossible, ``nsctl lock`` fails **per entry**,
    naming the specifier it could not resolve and both remedies::

        cannot resolve `~= 0.5` for hmd-ms-transform.
          pin it:                 nsctl lock --pin <class>@<version>
          or take a known-good:   nsctl lock --from-env <env>

    That is a usable product on its own, which is why the tiers are built in
    this order and not the other one.

    ``lock`` never contacts a librarian in tiers 1 and 2. Tier 1 contacts
    ``ms-deployment``, which is local.

    **Spike outcome, 2026-09-15: tier 3 is feasible, and it has its own
    document.** Measured against a real cloud librarian: a repo class's
    published versions can be enumerated in three requests, and resolving
    ``~= 0.1`` for ``hmd-inf-trino`` selects the highest satisfying version out
    of 96 candidates.

    Because it is feasible it is also general -- ``hmd build``'s
    ``pre_build_artifacts`` pins ranges too -- so the mechanism is specified in
    ``NERD011`` and this SPEC consumes it. Four things that document settles and
    this one therefore does not repeat: the enumeration route and its dead ends,
    the measured cost and the batch chunking it forces, the fact that ``~= 0.1``
    accepts ``0.2.5`` rather than meaning "the 0.1 series", and the refusal of
    the ordered operators.

    What stays this SPEC's business is the **order of the tiers**, and the spike
    does not change it. Tiers 1 and 2 remain the default: a known-good
    environment is better evidence that a set of versions works together than
    the highest version each range independently admits. Tier 3 is what makes
    ``lock`` possible for a repo that has no working environment yet.

    One consequence for ``lock``'s output: an entry resolved by tier 3 records
    ``generated_from = "manifest"``, and a reader should treat that as weaker
    than ``"env:<name>"``. Resolving each range to its newest satisfying version
    is a guess that they are mutually compatible; a running environment is proof.

    **Partly implemented 2026-09-15.** Tiers 1 and 2 are built as ``nsctl lock``;
    tier 3 is not, and stays ``NERD011``'s. An entry neither tier settles is
    reported with the specifier and both remedies, exactly as above, and every
    such entry is reported at once rather than one per run -- a real manifest
    carries several ranges, and one failure per run would mean one edit per run.

    Two things writing it settled that the proposal did not state.

    ``--pin`` beats ``--from-env``. One is a version the user typed for this run
    and the other is whatever happens to be deployed; an explicit answer should
    never lose to an ambient one.

    An **optional dependency the ``local`` section does not gate is not locked**,
    because it is never deployed locally -- SPEC001 says an unmentioned optional
    dependency keeps today's behaviour, and today's behaviour is to stay
    undeclared. So a range on one is not a reason ``nsctl lock`` cannot run, and
    ``--pin`` naming one is refused with *that* reason ("gate it under
    ``local.dependencies``") rather than with "this repository does not declare
    it", which would send the reader to correct a spelling that is already right.

    **Tier 3 is not being built here, and this SPEC stays** ``partial``
    **deliberately rather than pending.** ``NERD011`` built the mechanism
    -- ``internal/versionspec`` and ``internal/versions`` -- and put its
    consumers on the ``artifact`` verbs instead, for a reason about what ``lock``
    is *for*.

    ``lock`` snapshots an environment that has been stood up by hand: repo
    classes added, dependencies wired, ``env apply`` run until it works. Tier 1
    reads that environment's instances, and a deployed instance already carries a
    concrete version -- so in the workflow this command exists to serve there is
    no range left to resolve by the time anybody runs it. A lock born any other
    way is a guess that a set of independently-newest versions works together,
    where a running environment is proof; that is the same asymmetry
    ``generated_from`` records, taken to its conclusion.

    So the refusal above is the product, not a placeholder for one. A version is
    *chosen* one step earlier -- while adding repo classes -- and one step later,
    when bumping one, and ``nsctl artifact versions`` answers both without
    writing anything. The sequence is: ask what is published, add the version you
    chose, apply, and lock the result.

    Should something later resolve a whole manifest's ranges at once, ``NERD011``
    is the mechanism it uses and this SPEC is where the tier is written down.

.. spec:: ``nsctl lock --check``
    :id: HMD_CLI_NEURONSPHERE_NERD010_SPEC004
    :links: HMD_CLI_NEURONSPHERE_NERD010
    :status: implemented

    ``nsctl lock --check`` reports whether ``neuronsphere.lock`` still covers
    what the manifest declares, and **writes nothing**.

    It is the hook a repository puts in ``pre-commit`` and in CI, and the
    no-write rule is ``NERD009`` SPEC006's: a validate verb that rewrites a
    file cannot be run on a dirty tree, which is exactly when it is wanted.

    Three findings, reported separately because they have different fixes:

    - a declared want with no ``resolved`` entry -- the lock is stale, run
      ``nsctl lock``;
    - a ``resolved`` entry no longer declared -- the lock is stale in the other
      direction, and this one is a warning rather than an error, because a
      developer mid-refactor should not be blocked by it;
    - a lock whose ``version`` this binary does not know -- refuse, per SPEC002.

    It does **not** check that the pinned versions still exist in any
    librarian. That is a network operation, and ``--check`` must run on an
    aeroplane and in a CI job with no librarian credential.

    **Implemented 2026-09-15.** The first finding exits ``1``; the third exits
    ``2``, because a lock this binary cannot read is a file the caller must fix
    rather than a drift it can regenerate away. The second is a warning on
    standard error and exits ``0``.

.. spec:: ``nsctl env add --from-repo``
    :id: HMD_CLI_NEURONSPHERE_NERD010_SPEC005
    :links: HMD_CLI_NEURONSPHERE_NERD010
    :status: implemented

    .. code-block:: bash

        nsctl env add <name> --from-repo [<path>] \
            [--profile p,q | --all-profiles | --lean] [--no-pull]

    The steps, in order:

    #. Read ``neuronsphere.lock``. Absent, fail naming ``nsctl lock`` -- never
       fall back to resolving on the fly, because the whole point of the lock is
       that a fresh clone gets the same answer as the machine that wrote it.
    #. Activate profiles: ``--profile`` if given; otherwise
       ``local.default_profiles``. ``--lean`` activates none and
       ``--all-profiles`` activates every profile the lock mentions.
    #. **Pull** every activated entry not already in the ``NERD005`` artifact
       cache, printing each fetch on its own line. This is the one place a pull
       happens without being asked for, and it is justified because this *is*
       first start: there is no environment yet, so there is no offline
       expectation to violate. ``--no-pull`` suppresses it.
    #. Write the environment manifest: every activated entry as
       ``source: {type: artifact}`` at its pinned version, plus **this
       repository** as ``source: {type: local, path: <absolute path>}``.
    #. Record the activated profiles in the environment manifest.
    #. Print the ``nsctl env start <name>`` line.

    Step 5 is not bookkeeping. Without it a later bare ``nsctl env apply``
    would silently fall back to ``default_profiles`` and reconcile away
    instances the user explicitly asked for, which is the worst kind of
    delta-apply: correct according to a file nobody re-read.

    **Amended 2026-09-15: what an instance is called.** Step 4 says what to
    declare and never says what it is *named*, and the answer turns out to be
    load bearing. It is acceptable -- expected, even -- for two engineers to
    deploy the same repo class at the same locked version under **different
    local instance names**, and both must still resolve the right dependencies
    from the same checked-in lock. So the name is **defaulted but overridable**,
    and the lock never carries one.

    The order, most specific first:

    #. **What the environment manifest already binds.** A new ``bindings`` map
       records, per want, the instance name it was given here. On a re-apply that
       is authoritative, so an instance somebody renamed stays renamed rather
       than being duplicated under its default name.
    #. ``--name <role-or-declared-name>=<instance>``, repeatable, on both this
       verb and SPEC006's.
    #. The dependency block's own ``instance_name``, or the companion's.
    #. The **dependency role**.

    Tier four is the role and never the repo class. Only 9 of the 517 dependency
    blocks in a full workspace carry an ``instance_name``, the role names already
    read as instance names (``base-vpc``, ``neptune-db``, ``db-credentials``), and
    a class-derived default would collide the moment one class fills two roles --
    which ``hmd-inf-credentials`` does three times in ``hmd-inf-trino``.

    Whatever tier answers, the repository's own declaration carries
    ``dependencies: {<role>: <resolved instance>}`` for every role it declared.
    That map is what ``ms-deployment`` resolves against, and it is why a lock
    naming only classes and roles is sufficient.

    Two refusals follow. A ``--name`` whose *value* is a reserved substrate name
    is refused, because the substrate creates that instance whatever a manifest
    says. A ``--name`` addressing a key this repository does not declare is
    refused rather than ignored: silently dropping one leaves the user believing
    they renamed something, and the manifest disagreeing several minutes into a
    deploy.

    **Amended 2026-09-15: the substrate.** A want whose resolved instance name is
    reserved for the environment scope -- ``base-vpc``, ``eks-cluster``,
    ``environment-db``, ``local-neuronsphere`` -- is **not declared**, because the
    substrate deploys it whether or not a manifest names it and
    ``manifest.Validate`` refuses a declaration that claims the name. It is
    printed as ``provided by the substrate`` and the repository still binds to
    it, so the role resolves. Real manifests hit this immediately:
    ``hmd-ms-transform`` and ``hmd-ms-artifact-lib`` both name ``base-vpc``.

    **Implemented 2026-09-15.** The declaration and the lock are read *before*
    the registry is written, so a broken manifest or a missing lock costs
    nothing: an environment registered against a repository that cannot be read
    is a port slot and an account allocated for nothing.

.. spec:: ``nsctl env apply --from-repo``
    :id: HMD_CLI_NEURONSPHERE_NERD010_SPEC006
    :links: HMD_CLI_NEURONSPHERE_NERD010
    :status: implemented

    ``nsctl env apply [<name>] --from-repo [<path>] [--profile ...] [--pull]``
    re-reads the lock and reconciles an environment that already exists. With
    no ``--profile``, it uses what the environment manifest recorded.

    **It is offline by default**, and this is the asymmetry with SPEC005 worth
    stating plainly. ``NERD005`` SPEC004 refuses to let resolution reach the
    internet unasked, on the grounds that an apply which does behaves
    differently on an aeroplane. A missing artifact therefore fails with
    ``NERD005`` SPEC007's message, naming ``nsctl artifact pull``; ``--pull``
    fetches what is missing first, as a declared step that prints what it does.

    Changing profiles is an ordinary delta-apply. Activating one adds
    instances. Deactivating one **leaves them deployed** unless ``--prune``,
    which is the promise ``nsctl repo remove`` already makes -- these verbs edit
    a manifest, and nothing tears an instance down on the user's behalf.

    Both verbs are thin: they compose ``localspec``, the lock reader, the
    ``NERD005`` pull, and the existing ``openManifest`` / ``Save`` / apply path.
    No new deployment mechanism appears in this document.

    **Amended 2026-09-15: what ``--prune`` covers, and what it does not.** Two
    kinds of instance stop being asked for, and they are not the same request.

    An instance whose **name changed** -- through ``--name``, or through a
    binding that no longer matches -- is *always* undeclared, with or without
    ``--prune``. Naming an instance something else is a request about that
    instance, and leaving both names declared would deploy the same thing twice
    into one environment, which is the duplicate the naming order exists to
    prevent.

    An instance dropped by **deactivating a profile** stays declared unless
    ``--prune``. The user narrowed which profiles are on and said nothing about
    those instances; quietly undeclaring them would make a later apply look as
    though it had lost them.

    Neither is ever torn down, and ``--prune`` does not tear down either -- it
    undeclares. An instance this repository never declared is untouched by
    ``--prune`` in any case, because these verbs own only what they wrote.

    **Implemented 2026-09-15.** One thing the acceptance test caught that the
    proposal did not predict, and it is this SPEC's own failure mode: with no
    ``--profile``, "use what the environment recorded" cannot be decided by
    looking at the recorded *profile list*, because a **lean environment records
    an empty one** -- indistinguishable from an environment never created from a
    repository. Reading it that way makes a bare ``nsctl env apply`` silently
    re-expand a lean environment to ``default_profiles``, which is precisely the
    delta-apply SPEC005 step 5 forbids. The decision is therefore made on the
    ``bindings`` map, which a ``--from-repo`` run always writes because it always
    binds the repository itself.

.. spec:: Why the root, and why TOML
    :id: HMD_CLI_NEURONSPHERE_NERD010_SPEC007
    :links: HMD_CLI_NEURONSPHERE_NERD010
    :status: implemented

    The lock is at the repository root and in TOML. Both halves are decisions
    and both are cheap to get wrong later, so they are recorded here.

    **Root**, because ``NERD009`` SPEC006 already names a repo-root
    ``neuronsphere.toml`` as the direction the platform intends, and a
    generated file can go there *today* where the manifest cannot: the manifest
    is held back only by ``hmd_lib_manifest``'s path list not looking at the
    root, and nothing but ``nsctl`` ever reads the lock. Putting it under
    ``meta-data/`` would mean moving it on the day of the flip for no benefit
    in the meantime.

    **TOML**, because it is what the platform is converging on, because BACON
    already supports it, and because ``go-toml/v2`` is already linked into the
    binary. A second serialisation format in the same repository is a second
    set of edge cases for no expressive gain.

    The pairing is also the one every reader already knows -- a hand-authored
    declaration beside a generated lock, as with ``pyproject.toml`` and
    ``uv.lock``, or ``Cargo.toml`` and ``Cargo.lock``. That is worth something
    on its own: nobody has to be told which file to edit.

    **Implemented 2026-09-15** alongside SPEC002. Both halves stand as written.

    **The declaration stays in the manifest**, and the contrast with the lock
    is the point. A generated, ``nsctl``-owned file is free to be new. A
    *declaration* is not, because ``nsplugin.json`` was withdrawn for exactly
    that: it restated resources, databases and dependencies the manifest
    already carried, so it was a second place to say one thing and it went
    stale. ``local`` restates nothing -- no other file anywhere says "when
    running locally, optionally also start a transform runtime" -- so the
    precedent does not apply, and the manifest gets a section rather than the
    repository getting a third file.

.. spec:: What this is not
    :id: HMD_CLI_NEURONSPHERE_NERD010_SPEC008
    :links: HMD_CLI_NEURONSPHERE_NERD010
    :status: implemented

    **Not a package index.** The lock pins RepoClass build artifacts held by a
    librarian. Wheels, images and npm packages are ``NERD006``, and the
    distinction is ``NERD006`` SPEC011's.

    **Not a second manifest editor.** Every write to the ``local`` section goes
    through ``NERD009``'s ``nsctl repoclass`` store and its surgical key-path
    edits. This document adds a section and its schema, not a second way to
    write the file.

    **Not a dependency resolver for the cloud.** ``ms-deployment`` resolves
    ``version_spec`` and resource requirements at ``apply_changeset`` time, in
    the cloud and locally alike. A lock is never an input to that. If the two
    disagree, ``ms-deployment`` is right and the lock is stale.

    **Not a way to skip a required dependency.** See SPEC001.

    **Not a replacement for ``$HMD_REPO_HOME`` development.** The repository
    under test is deployed from its working tree, and ``NERD005`` SPEC002's
    precedence is what lets a developer of any *companion* do the same for
    theirs.

Risks
-----

.. list-table::
   :header-rows: 1

   * - Risk
     - Severity
     - Mitigation
   * - Range resolution is impossible, so ``lock`` cannot be generated from a
       manifest alone
     - **Retired**
     - Settled by the SPEC003 spike on 2026-09-15: it is feasible, in three
       requests per repo class. Tiers 1 and 2 remain the default regardless,
       because a known-good environment is better evidence than the highest
       version matching a range.
   * - Tier 3 is slow enough to time out on a long-lived repo
     - Medium
     - Measured at 29.3 s for 392 versions. The ``get_by_nid`` batch is chunked
       at roughly a hundred ids, and tier 3 never runs during resolution --
       only in ``nsctl lock``.
   * - A lock drifts from the manifest and nobody notices
     - Medium
     - ``lock --check``, designed to run in ``pre-commit`` and CI without a
       network or a credential.
   * - Profiles multiply until nobody knows what a bare ``env add`` deploys
     - Medium
     - ``nsctl env add`` prints the activated profiles and every instance it
       declared; the profiles are recorded in the environment manifest, so
       ``env status`` can report them.
   * - ``local`` grows until the manifest is unreadable
     - Low
     - A realistic section is 15 to 30 lines against manifests that already run
       129 to 408. Revisit if a real repository disproves this.
   * - Gating an optional dependency breaks a repo that needed it to boot
     - Low
     - Gating is opt-in per entry; an unmentioned optional dependency keeps
       today's behaviour.

Acceptance criteria
-------------------

#. A repository with a ``local`` section and a checked-in lock stands up an
   environment with ``nsctl env add --from-repo . && nsctl env start``, from a
   fresh clone, with ``$HMD_REPO_HOME`` pointed at an empty directory.
#. ``--lean`` on the same repository deploys the repository and its
   unconditional entries, and none of the profile-gated ones.
#. ``--profile transforms`` on the same repository additionally deploys exactly
   the entries naming that profile.
#. A second ``env apply`` with no ``--profile`` reconciles to the same set,
   having read the profiles back from the environment manifest.
#. With every artifact already cached, all of the above run with no network.
#. ``nsctl lock --from-env <env>`` against a working environment produces a lock
   that reproduces that environment on another machine.
#. ``nsctl lock --check`` fails on a stale lock, succeeds on a current one, and
   modifies no file in either case.
#. A lock declaring an unknown schema ``version`` is refused, naming the
   version found and the versions supported.

Open questions
--------------

**Does ``nsctl lock`` pin the repository's own version?** Today the repository
under test is always ``source: local``, which is what makes it the thing being
tested. Pinning it would let one lock describe an environment the repository is
not the local half of -- useful for a demo or a reproduction, and possibly more
than is wanted.

**Should a lock be able to name another repository's lock?** A platform team
could publish a "standard local stack" lock that product repositories include.
This is composition, and it is the kind of feature that is much easier to add
than to remove.

**What does ``env status`` show for a profile-gated instance that is deployed
but whose profile is no longer active?** It is neither declared nor undeclared.
The honest answer is probably a third state, which argues for naming it before
somebody has to infer it.
