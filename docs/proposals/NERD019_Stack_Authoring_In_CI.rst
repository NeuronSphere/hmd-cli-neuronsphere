.. NERD019 Stack Authoring in CI

NERD019 Stack Authoring in CI
=============================

.. req:: Author, build and publish a stack from checked-in files, in CI, with no tenant and no running platform
    :id: HMD_CLI_NEURONSPHERE_NERD019
    :status: implemented

    A stack (``NERD017``) shall be **built and published by a plain CI
    workflow** -- GitHub Actions with the repository's own ``GITHUB_TOKEN``
    is the reference -- from exactly two checked-in files, the manifest's
    ``local`` section and ``neuronsphere.lock``, deterministically, with no
    NeuronSphere tenant, no running environment and no tool but ``nsctl``.

    A stack shall be **derivable from a running environment, or from that
    environment's exported BOM**, by selecting the instances a consumer
    should get and walking their required dependencies; what is derived is
    the two checked-in files, reviewed as a pull request, never a live
    artifact.

    This is the **one** publishing path. The platform's own stacks use it
    too: ``hmd build`` is not extended to build stacks, and the librarian
    build-number counter is not involved. The registry's own tag list is
    the version ledger.

Motivation
----------

``NERD017`` made a stack installable. It left publishing as a single verb
run by hand from a checkout with a personal access token, with the
companions' zips fetched from the paid librarian at push time. That is a
demo, not a workflow: nothing is reviewable between "I typed push" and "it
is public", a third party with no tenant cannot build one at all, and the
most natural way to *author* a stack -- "give a public user superset, trino
and airflow out of the environment I am running" -- had no verb.

CI as the primary path forces two design decisions that are better made on
purpose than backed into:

* **Derivation must not need a live platform.** The graph a stack is cut
  from is available as an exported BOM (``nsctl bom show --json``,
  ``NERD012``) and is the same shape as the live graph
  (``msdeploy.BOMEntry`` and ``msdeploy.DeployedInstance`` carry the same
  fields). One walk over either input, with the live one as sugar.
* **Companions must be fetchable without a tenant.** A third party's
  companions are their own RepoClasses, and they have no librarian. So a
  RepoClass build zip must itself be publishable as an OCI artifact, and a
  lock entry must be able to say so (``NERD016`` SPEC009).

Scope and terminology
---------------------

* **Authoring** is producing the two files; **building** is turning them
  and the pinned zips into a stack artifact on disk; **publishing** is
  pushing that artifact. The three are separate verbs so CI can inspect,
  attest or retry between them.
* A **reference environment** is the BOM export a stack was derived from,
  checked in beside the manifest so drift is a diff.
* An **OCI image layout** is the on-disk form of an artifact the OCI
  specification defines (``oci-layout``, ``index.json``, ``blobs/``): what
  ``stack build`` writes and every registry tool can read.

Out of scope: extending ``hmd build``; a stack pulling in other stacks by
lock (``NERD010``'s open question, unchanged); signing (a layout on disk is
what a later signing step would consume, which is one reason it is a
layout); changing a ``ghcr.io`` package's visibility (no API exists; it is
the one manual, one-time act, and the workflow says so).

.. spec:: nsctl stack build writes an OCI image layout
    :id: HMD_CLI_NEURONSPHERE_NERD019_SPEC001
    :links: HMD_CLI_NEURONSPHERE_NERD019
    :status: implemented

    .. code-block:: text

        nsctl stack build [<repo-dir>] [--out build/stack] [--artifacts <dir>] [--tag <version>]

    Reads the ``local`` section and the lock, obtains every pinned zip
    (SPEC002), zips the repository itself as the subject, assembles the
    ``NERD017`` SPEC002 manifest, and writes it as an OCI image layout under
    ``--out`` (default ``build/stack``, which ``artifact.SkipDirs`` already
    excludes from the subject's zip, as does whatever the manifest declares
    under ``license.exclude`` -- ``NERD017`` SPEC011). ``index.json`` carries one manifest
    with ``org.opencontainers.image.ref.name`` set to the tag, which is
    ``--tag`` or ``meta-data/VERSION``. The lock inside the layout carries
    every zip's digest.

    Given the same lock and the same bytes the layout is identical, so a
    build in CI reproduces a build on a laptop. The verb contacts no
    registry; it is the offline half of publishing.

.. spec:: Where a pinned zip comes from
    :id: HMD_CLI_NEURONSPHERE_NERD019_SPEC002
    :links: HMD_CLI_NEURONSPHERE_NERD019
    :status: implemented

    **Amended 2026-09-21.** Every tier hands over bytes that are published
    whole and annotated from the manifest inside them (``NERD017``
    SPEC011); only tier 2's re-zip of a tree, and the subject's own zip,
    are made by ``nsctl`` and so honour that tree's declared
    ``license.exclude``.

    For each lock entry, in order, the first source that answers wins, and
    the bytes are verified against the entry's ``digest`` when it has one:

    1. ``--artifacts <dir>`` by the librarian file name
       (``<class>_<version>_build.zip``).
    2. The artifact cache, which from this NERD on keeps the original zip
       beside each unpacked tree: what a stack derived from a running
       environment finds, since the environment deployed from those zips.
       A tree cached before that -- no zip kept -- is re-zipped, and only a
       lock entry with no digest can accept that, since re-zipping never
       reproduces the original bytes.
    3. The entry's ``source`` when it is an OCI reference (``NERD016``
       SPEC009): anonymous or credentialed, by tag and then digest.
    4. The cloud Artifact Librarian by ``content_path`` -- the paid path,
       taken only when a credential is present, and reported when taken.

    An entry no source answers is refused naming every source tried and
    the ``nsctl artifact push`` that would create source 3.

.. spec:: nsctl stack push publishes a layout, and --bump chooses the version
    :id: HMD_CLI_NEURONSPHERE_NERD019_SPEC003
    :links: HMD_CLI_NEURONSPHERE_NERD019
    :status: implemented

    .. code-block:: text

        nsctl stack push <ref> --from build/stack [--bump | --tag <version>] [--token <t>]

    ``--from`` pushes a built layout: every blob, then the manifest, under
    the tag. Without ``--from`` the verb builds first (``NERD017`` SPEC006's
    behaviour, kept for the laptop case).

    ``--bump`` lists the registry's version-shaped tags (``NERD016``
    SPEC004) and tags the push with the next patch of the newest -- or with
    ``meta-data/VERSION.0`` when there is none, or when ``VERSION`` is a
    newer major.minor than any published tag. The chosen tag is printed, and
    a push that would overwrite an existing tag is refused: a published
    version is immutable. The registry is the ledger; no build counter, no
    tenant.

    ``GITHUB_TOKEN`` is a valid ``HMD_REGISTRY_TOKEN`` for ``ghcr.io`` within
    the repository's owner, so the reference workflow needs no secret of its
    own.

.. spec:: nsctl stack init scaffolds, and derives from an environment or a BOM
    :id: HMD_CLI_NEURONSPHERE_NERD019_SPEC004
    :links: HMD_CLI_NEURONSPHERE_NERD019
    :status: implemented

    .. code-block:: text

        nsctl stack init <name> [--path <dir>]
        nsctl stack init <name> --from-env <env> --select a,b,c [--dry-run] [--bundle-local x,y] [--include-provided]
        nsctl stack init <name> --from-bom <bom.json> --select a,b,c [--dry-run] [--diff]

    Bare ``init`` writes a stack RepoClass: ``meta-data/manifest.json`` with
    a description, ``deploy.commands: [["exec", "true"]]`` and an empty
    ``local`` v1 section; ``meta-data/VERSION`` at ``0.1``; and the CI
    workflow of SPEC007. It refuses an existing manifest.

    With ``--from-bom``, the BOM is a JSON list of ``msdeploy.BOMEntry`` as
    ``nsctl bom show --json`` writes it. With ``--from-env``, the live graph
    is read through ``msdeploy.EnvironmentInstances`` and converted to the
    same shape, and is also written beside the manifest as
    ``meta-data/reference-bom.json`` so CI has what the laptop had. Each
    entry of that file carries the environment manifest's *declared*
    ``instance_configuration`` for the instance -- never the live
    instance's, the rule the derivation itself follows -- because CI's
    ``--from-bom`` re-derive has no environment manifest to overlay and
    would otherwise strip every instance's configuration (amended
    2026-09-21, found by the first ``--diff`` against a checked-in stack).

    ``--select`` names the roots. The walk follows each root's dependency
    roles to the instances filling them, transitively, and classifies every
    instance reached:

    ====================================== ==============================================================
    The instance is…                       …and becomes
    ====================================== ==============================================================
    a substrate instance, or one of a      a **bound role**: ``local.dependencies.<role>.bind`` to the
    class ``nsctl`` bundles                instance every environment has; nothing bundled
    declared by another stack in the       a **cross-stack role**: ``local.dependencies.<role>`` with the
    environment (a ``stacks`` record       resource type from the class's manifest and ``suggest`` naming
    binds it)                              that stack's ``ref``; nothing bundled unless ``--include-provided``
    a selected root                        a **companion** in its own profile named after the instance,
                                           all roots in ``default_profiles``
    anything else                          a **companion**, unconditional
    ====================================== ==============================================================

    A companion is ``local.repos[]`` with ``instance_name`` as deployed,
    ``version_spec: == <running version>``, ``dependencies`` rewritten to
    the walk's names, and ``instance_configuration`` copied **from the
    environment manifest's declaration only** -- never from the live
    instance's configuration, which carries host paths, ports and secret
    references. A copied value that looks host-specific (an absolute path,
    a loopback address, a port) is listed for the author to review.

    An instance deployed from a working tree (an explicit
    ``source: {type: local}`` in the environment manifest; one with no source
    block resolves through the tier chain at the published version the graph
    records) has no published artifact and is refused, naming
    ``--bundle-local <instance>`` which zips its tree as the subject is
    zipped. ``--dry-run`` prints the classification table and writes
    nothing. ``--diff`` (with ``--from-bom``, for the refresh job) reports
    whether the derived ``local`` section differs from the checked-in one and
    exits 1 when it does.

.. spec:: nsctl lock --resolve pins ranges from what is published
    :id: HMD_CLI_NEURONSPHERE_NERD019_SPEC005
    :links: HMD_CLI_NEURONSPHERE_NERD019
    :status: implemented

    ``NERD010`` SPEC003's third tier, deliberately left to ``NERD011`` and
    never wired: ``nsctl lock --resolve`` enumerates each ranged want's
    published versions and pins the highest satisfying one. Two sources of
    "published", tried in this order per class: the OCI reference the
    current lock entry already names (free), then the cloud librarian
    (paid, only with a credential). A range no source can answer is
    reported with both remedies. Without ``--resolve`` a range is refused
    as today; ``--from-env`` and ``--pin`` are unchanged.

.. spec:: repoclass validate knows a stack
    :id: HMD_CLI_NEURONSPHERE_NERD019_SPEC006
    :links: HMD_CLI_NEURONSPHERE_NERD019
    :status: implemented

    On a manifest with a ``local`` section that names companions,
    ``validate`` additionally runs ``lock --check`` (every want pinned, no
    stale pin), refuses a stack with no companions, and reports every
    ``local.dependencies`` role that is neither bound nor answered by a
    companion's ``satisfies``. This is the CI ``verify`` job's first step.

.. spec:: The reference workflow and the setup action
    :id: HMD_CLI_NEURONSPHERE_NERD019_SPEC007
    :links: HMD_CLI_NEURONSPHERE_NERD019
    :status: implemented

    ``stack init`` writes ``.github/workflows/stack.yml`` with three jobs:

    ``verify`` (every pull request)
        install ``nsctl``; ``nsctl repoclass validate``; ``nsctl stack build``;
        upload ``build/stack`` as a workflow artifact.

    ``release`` (push to the default branch)
        ``nsctl stack push <ref> --from build/stack --bump`` with
        ``HMD_REGISTRY_TOKEN: ${{ secrets.GITHUB_TOKEN }}`` and
        ``permissions: packages: write``.

    ``refresh`` (weekly and on demand)
        ``nsctl lock --resolve``; when a reference BOM is checked in,
        ``nsctl stack init --from-bom ... --diff``; open a pull request when
        either changed, so ``verify`` runs on it and a human merges.

    The workflow installs ``nsctl`` with this repository's ``install.sh``
    pinned to a release, through a composite action
    ``.github/actions/setup-nsctl`` this repository publishes so the
    install line is one ``uses:``. The generated file carries a comment on
    the one manual step: making the ``ghcr.io`` package public after the
    first release.

.. spec:: nsctl repoclass local authors the section by hand
    :id: HMD_CLI_NEURONSPHERE_NERD019_SPEC008
    :links: HMD_CLI_NEURONSPHERE_NERD019
    :status: implemented

    .. code-block:: text

        nsctl repoclass local add <repo-class> --spec "~= 0.3" [--name <instance>] [--profile p]... [--depends role=instance]...
        nsctl repoclass local remove <instance>
        nsctl repoclass local bind <role> <substrate-instance>
        nsctl repoclass local require <role> [--suggest <stack-ref>]
        nsctl repoclass local set-default-profiles p,q
        nsctl repoclass local list

    The same manifest-verb shape as ``repoclass deploy``. A role the stack
    needs is declared with ``deploy add-dependency`` (with a resource type,
    so ``NERD017`` SPEC010 matches by what is needed) and then gated:
    ``bind`` for one the substrate provides, ``require`` for one another
    stack provides -- written as ``local.dependencies.<role>.external: true``,
    which ``nsctl lock`` never pins and ``stack add`` resolves or refuses. A
    companion never "satisfies" a role: under ``NERD010`` the role's own
    dependency want is the instance, and a companion is something else
    started beside it. Last in the build order: once derivation exists these
    are for adjusting what it wrote.

Testing
-------

``stack build`` against fixtures proves layout determinism (two builds,
identical bytes), the four zip sources in order with a fake registry for
source 3, and the refusal naming ``artifact push``. ``stack push --from``
round-trips a layout through the fake registry byte for byte; ``--bump``
is tested against tag lists of none, ``0.1.3``, and a newer ``VERSION``,
and refuses an existing tag. ``stack init --from-bom`` is tested on a
fixture BOM modelling the platform (substrate, two stacks' worth of
instances, a working-tree instance) for every row of the classification
table, ``--dry-run`` writing nothing, ``--diff`` exit codes, and the
host-specific configuration report; ``--from-env`` on the same data
through a fake ms-deployment. ``lock --resolve`` against the fake registry
and a fake librarian. ``validate`` on a stack with a stale lock and an
unbound role. The generated workflow is asserted to name only verbs the
built tree has (the ``suggestions_test`` rule, applied to the template).

The acceptance run derives a stack from the user's running platform
(``--from-env local --select superset,trino,airflow``), pushes it from a
GitHub Actions run of the generated workflow with ``GITHUB_TOKEN``, and
installs it anonymously into a scratch environment.

Alternatives considered
-----------------------

**Extend ``hmd build`` to build stacks.** Rejected: it would put ``nsctl`` in
the platform's Python build pipeline for one artifact type, tie stack
versions to the ms-projects counter a third party cannot reach, and leave
two publishing paths to keep equivalent. One path, and it is the one a
third party can run.

**Derive only from a live environment.** Rejected: CI would need Docker,
k3s and a platform for a derivation that reads a graph. The BOM export is
the same data.

**Fetch companions at push time.** Rejected: nothing to review between the
lock and the registry, and no offline build.

**A tarball instead of an OCI layout.** Rejected: the layout is what
``oras``, ``crane`` and ``cosign`` read, and a build output nobody else can
open is a build output nobody attests.
