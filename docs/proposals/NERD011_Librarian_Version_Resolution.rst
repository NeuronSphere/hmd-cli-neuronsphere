.. NERD011 Librarian Version Resolution

NERD011 Librarian Version Resolution
====================================

.. include:: ../_includes/paid-cloud.txt

.. req:: Resolve a version specifier to a concrete published version
    :id: HMD_CLI_NEURONSPHERE_NERD011
    :status: implemented

    ``nsctl`` shall be able to take a BACON ``version_spec`` and a repo class
    and answer with **the concrete published version that satisfies it**, using
    an Artifact Librarian as the source of what has been published.

    The satisfaction test shall agree with ``hmd-ms-deployment``, because
    ``hmd-ms-deployment`` re-validates every version it is handed and a
    disagreement means ``nsctl`` picks a version the control plane then
    rejects.

    Where agreement is impossible because the existing implementation is
    incorrect, ``nsctl`` shall **refuse the specifier** rather than choose
    between two wrong answers, and shall say which specifier to write instead.

.. note::

    This document exists because ``NERD010`` SPEC003 needed one tier of it and
    the mechanism turned out to be general: ``hmd build``'s
    ``pre_build_artifacts`` pins ranges, ``deploy.dependencies`` pins ranges,
    and anything that wants to know "what would this resolve to today" needs
    exactly this. Per house rule the capability gets its own document rather
    than being buried in its first consumer.

Motivation
----------

A BACON manifest almost never names a version. It names a range: of the 572
``version_spec`` values in a full workspace, **554 are** ``~=``, 13 are ``==``
and 5 are ``>=``. So any tool that wants to pin, lock, cache or pre-fetch a
dependency has to turn a range into a version, and today nothing can.

``hmd-ms-deployment`` does not do this, and it is worth being precise about why,
because it looks as though it should. It **validates candidates**: given a set
of already-registered ``RepoClassVersion`` instances it filters them with
``VersionSpecifier.validate``. It never asks a librarian what exists, and it
never selects a version from a range. Selection is a genuinely new operation
with no existing authority to defer to.

The cost of not having it is that every range in the platform is resolved by a
human reading git tags.

The evidence this document is built on
--------------------------------------

Everything below was measured against a live cloud librarian and against
``hmd_ms_deployment.version`` on 2026-09-15, rather than reasoned from the
source. That matters because four of the six findings contradict what the code
appears to do.

.. list-table::
   :header-rows: 1

   * - Question
     - Answer
   * - Can a librarian enumerate a repo's versions?
     - **Yes**, in three requests. See SPEC001.
   * - Does ``~= 0.1`` mean "0.1.x"?
     - **No.** It means major ``0`` and minor ``>= 1``, so ``0.2.5`` and
       ``0.9.99`` satisfy it. This matches PEP 440 and contradicts the obvious
       reading.
   * - Are ``>``, ``>=``, ``<``, ``<=`` usable?
     - **No.** Five of eleven representative comparisons are wrong, and all
       five real-world uses fail to parse at all. See SPEC002.
   * - Is ``sort_versions`` a version ordering?
     - **No.** It is patch-primary and ascending in major. See SPEC003.
   * - Does the librarian's ``repo_has_repo_version`` relationship work?
     - **No.** Declared and never populated: zero edges for every repo.
   * - Can every content item be listed at once?
     - **No.** ``502``; the response is far too large.

.. spec:: Enumerating a repo class's published versions
    :id: HMD_CLI_NEURONSPHERE_NERD011_SPEC001
    :links: HMD_CLI_NEURONSPHERE_NERD011
    :status: implemented

    The published versions of a repo class shall be obtained in three requests
    against an Artifact Librarian:

    #. ``POST /api/hmd_lang_artifact_librarian.repo`` with an **empty body**,
       giving ``repo_name`` to ``identifier`` for every repo at once -- 217 rows
       in 1.3 s, and cached for the life of the process. A *filtered* body
       answers ``500``, so the filtering is done client side.
    #. ``GET /api/hmd_lang_artifact_librarian.content_item_has_repo/to/<identifier>``
       -- the content items belonging to that repo, as edges whose ``ref_from``
       is the content item.
    #. ``POST /apiop/get_by_nid`` with those ids, then parse each result's
       ``content_item_path`` with the grammar the librarian itself writes::

           repository:/<repo>/<version>/<repo>_<version>_<item_type>.zip

       The version and the item type are both **in the path**, so neither is
       ever fetched as an entity.

    The parse is ``librarian.Spec``'s grammar read backwards, and shall reuse
    that type rather than a second regular expression. A path that does not
    match is skipped rather than failing the enumeration -- a librarian holds
    content items that are not build artifacts, and they are not this
    document's business.

    **Four routes that do not work**, recorded because each looks correct and
    three of them fail silently rather than loudly:

    - ``/apiop/search`` with ``like``, ``contains``, ``startswith`` or
      ``begins_with`` on ``content_item_path``: all four answer ``500``.
    - Any *filtered* CRUD search: ``500``, while the unfiltered body succeeds.
    - Listing every ``hmd_lang_librarian.content_item``: ``502``.
    - ``hmd_lang_artifact_librarian.repo_has_repo_version``: **declared in the
      language pack and never populated.** Every repo returns zero edges. The
      content-path parser writes ``content_item_has_repo`` and
      ``content_item_has_repo_version`` instead. This is the trap worth naming
      twice, because it is the relationship an implementer would reach for and
      it returns an empty list rather than an error.

    **Cost, and what it forces.** Measured: 18 versions in 4.8 s, 96 in 8.2 s,
    392 in 29.3 s.

    - The ``get_by_nid`` batch shall be **chunked at roughly one hundred ids**.
      The 392-id request is close enough to a Lambda timeout that a repo class
      with a longer history would simply fail.
    - ``get_by_nid`` mints a presigned ``download_url`` for every item, so this
      route pays for URL signing purely to read paths. That is acceptable in a
      command a developer runs occasionally and is **not** acceptable on any
      path a deploy takes.

    **Implemented 2026-09-15** as ``internal/librarian``'s search surface --
    ``Repos``, ``RepoIdentifier``, ``ContentItemIDs``, ``ContentItemsByNID`` and
    ``ParseContentPath`` -- driven by ``internal/versions.Enumerate``.

    Three things writing it settled that the proposal did not state.

    The repo list is **memoised on the client**, not merely "cached for the life
    of the process" as a note: one unfiltered search brings back every repo, so
    a second enumeration in the same command costs nothing, and the client is
    where that belongs because nothing else would know it was safe.

    The **chunking lives in the client**, not in its caller. The hundred-id limit
    is a fact about the service, and a second caller that had to rediscover it
    would rediscover it as a timeout in whichever repo class first grew a long
    enough history.

    ``ParseContentPath`` **verifies by re-rendering**: it parses the path, then
    asserts that ``Spec.ContentPath()`` reproduces it exactly. That is stronger
    than reading the grammar backwards by hand, and it is what makes "reuse that
    type rather than a second regular expression" true rather than merely
    intended -- a file name that names a different repo or version than its own
    directories fails the round trip and is skipped.

.. spec:: The satisfaction test, and the operators that must be refused
    :id: HMD_CLI_NEURONSPHERE_NERD011_SPEC002
    :links: HMD_CLI_NEURONSPHERE_NERD011
    :status: implemented

    ``hmd_ms_deployment.version.VersionSpecifier`` is the authority, because it
    is what re-validates whatever ``nsctl`` picks. It is **not** PEP 440 and it
    is **not** ``packaging``; it is a hand-rolled evaluator, and it shall be
    ported by observed behaviour rather than by reading its source.

    A specifier is a comma-separated list of clauses with all spaces stripped.
    Every clause must be satisfied.

    **``~=`` (compatible), ``==`` and ``!=`` are correct and shall be ported
    exactly.**

    ``~=`` with a spec of *n* components requires the first *n-1* components to
    match exactly and the *n*\ th component of the version to be greater than or
    equal to the *n*\ th of the spec. So, verified:

    .. code-block:: text

        ~= 0.1     accepts 0.1.0, 0.1.225, 0.2.5, 0.9.99
                   rejects 0.0.9, 1.0.0
        ~= 0.1.7   accepts 0.1.7, 0.1.225
                   rejects 0.1.6, 0.2.0

    The first line is the one that will surprise every reader, and it is why
    this SPEC lists cases rather than describing the rule: **``~= 0.1`` is not
    "the 0.1 series".** An implementation that filtered by the ``0.1.`` prefix
    would be wrong for every repo class that has reached ``0.2``, and would be
    *accidentally right* for one that has not -- which is the worst way for a
    bug to behave.

    ``==`` permits ``*`` as its final component (``== 0.1.*``). ``!=`` is its
    negation. Both require at least two components.

    A version number must have all-numeric components; the count is not
    constrained, so ``0.1`` and ``0.1.2.3`` are both valid versions.

    **``>``, ``>=``, ``<`` and ``<=`` shall be refused.** They are not merely
    unhelpful, they are incorrect, in two independent ways:

    - ``Ordered.validate`` requires **every** component to satisfy the relation
      instead of comparing lexicographically with an early exit. So ``< 0.3.0``
      rejects ``0.2.9``: the minor already settles it, but the patch
      comparison (``9 < 0``) then vetoes. Five of eleven representative
      comparisons disagree with correct semantics, including
      ``> 0.1.0`` against ``1.0.0`` and ``<= 0.2.0`` against ``0.1.9``.
    - ``Ordered`` asserts **exactly three components**, so all five real-world
      uses -- every one of which is written ``>=0.3`` or ``>=0.1`` -- raise an
      ``AssertionError`` on construction. They do not resolve incorrectly; they
      do not parse.

    Refusing is the only defensible choice available. Implementing *correct*
    semantics would make ``nsctl`` pick versions ``hmd-ms-deployment`` then
    rejects, which is worse than not picking. Implementing the *observed*
    semantics would copy a defect into a second codebase and make it a
    contract. And because all five real uses already fail to parse, refusing
    breaks nothing that currently works.

    The refusal shall name the remedy, not just the problem::

        hmd-inf-coredns-addon declares `version_spec: ">=0.3"`, which nsctl
        cannot resolve.
          Ordered specifiers (>, >=, <, <=) are not supported: the platform's
          own evaluator requires exactly three components and compares them
          component-wise, so ">=0.3" does not parse and "< 0.3.0" would reject
          0.2.9.
          Write `~= 0.3` for "0.3 or later in major 0", or `== 0.3.*` for
          "the 0.3 series".

    **Fixing ``Ordered`` is referred out**, to ``hmd-ms-deployment``, and is not
    a prerequisite of this document. When it is fixed, this SPEC gains the four
    operators and the refusal becomes dead code -- which is the right shape for
    a dependency on somebody else's bug.

    **Implemented 2026-09-15** as ``internal/versionspec``. The golden table is
    ``internal/versionspec/testdata/golden.json``, 552 cases generated by
    ``testdata/generate_golden.py`` running the Python evaluator over every
    specifier and version shape the platform writes.

    Generating it settled one thing the proposal did not state. ``validate()``
    has **three** failure modes, not one, and they are not interchangeable: its
    own ``VersionSpecifierException``, an ``AssertionError`` for a version with a
    non-numeric component, and an ``IndexError`` for a version with *fewer*
    components than the clause compares -- ``~= 0.1.7`` against ``0.1`` indexes
    past the end. The table records all three separately, and ``Satisfies``
    answers false to the last two rather than raising. That is conservative in
    the direction that matters: a version ``nsctl`` cannot evaluate is one
    ``hmd-ms-deployment`` cannot evaluate either, so it must never be selected.
    (``resource_information.py`` catches the first two and returns ``False``; an
    ``IndexError`` would reach the caller as a ``500``.)

.. spec:: Selection, and why ``sort_versions`` must not be used
    :id: HMD_CLI_NEURONSPHERE_NERD011_SPEC003
    :links: HMD_CLI_NEURONSPHERE_NERD011
    :status: implemented

    Given the satisfying versions, ``nsctl`` shall select **the highest**, where
    "highest" is component-wise numeric comparison from most to least
    significant -- ordinary semantic ordering.

    This is defined here because **nothing upstream defines it.**
    ``hmd-ms-deployment`` only ever filters, so there is no existing selection
    rule to agree with, and this SPEC is therefore free to be correct.

    ``hmd_ms_deployment.version.sort_versions`` **must not** be used as that
    ordering, despite its name and despite being the only thing in the platform
    that sorts versions. It applies three stable sorts in the order major,
    minor, patch, which -- because a stable sort makes the *last* key primary --
    leaves it ordered by patch first and by major last. Verified:

    .. code-block:: text

        input              0.1.100  0.2.5  1.0.3  0.1.9  2.0.1
        sort_versions      0.1.100  0.1.9  0.2.5  1.0.3  2.0.1
        semver descending  2.0.1    1.0.3  0.2.5  0.1.100  0.1.9

    The two agree only by coincidence. Its callers are the two listing
    operations that produce ``latest_version`` for display, so the consequence
    today is a wrong "latest" in a GUI rather than a wrong deploy -- but a Go
    port that reused the algorithm for *selection* would turn a display defect
    into a deployment one.

    That defect is also referred out to ``hmd-ms-deployment`` rather than
    worked around here.

    **Ties cannot occur**, because a version string is the key. Two content
    items with the same version are one version.

    **Implemented 2026-09-15** as ``versionspec.Compare``, ``Sort`` and
    ``Highest``. Two details the proposal left open, decided so that a lock
    regenerated twice is the same bytes: a shorter version is padded with zeros
    before comparison, and two versions that are equal once padded -- ``0.1`` and
    ``0.1.0``, which are different strings and so are not the tie this SPEC rules
    out -- are ordered by component count and then by string. A version with a
    non-numeric component sorts below every real one, since nothing can select it
    anyway.

.. spec:: Caching, and staying off the deploy path
    :id: HMD_CLI_NEURONSPHERE_NERD011_SPEC004
    :links: HMD_CLI_NEURONSPHERE_NERD011
    :status: implemented

    An enumeration is cached under
    ``$HMD_HOME/.cache/neuronsphere/versions/<repo_class>.json``, carrying the
    versions, their item types, and the time of the query.

    This is **cache**, in the ``NERD005`` SPEC003 sense: wholly reconstructible,
    so deleting it costs a re-query and nothing else.

    Two rules that matter more than the cache itself:

    - **Resolution never runs during a deploy.** This capability is reached from
      ``nsctl lock`` and from nothing else. ``NERD005`` SPEC002's resolution
      tiers read the disk and do not consult a librarian, and ``NERD010``
      SPEC006's ``env apply`` is offline by default. A version resolution on the
      deploy path would make a deploy's result depend on the day it ran, which
      is the precise thing a lock exists to prevent.
    - **A cache entry is never silently refreshed.** ``nsctl lock`` re-queries;
      anything else uses what is there. An implicit refresh would reintroduce
      the non-determinism by the back door.

    The cache records the query time so that the command doing the resolving can
    report how old the data was, which is the difference between "this is the
    newest version" and "this was the newest version when somebody last asked".

    **Implemented 2026-09-15** as ``internal/versions``' ``Root``, ``Load`` and
    ``Save``, under ``$HMD_HOME/.cache/neuronsphere/versions``.

    One amendment. This SPEC said the capability "is reached from ``nsctl lock``
    and from nothing else"; it is reached from the ``artifact`` verbs and from
    nothing else, for the reason SPEC005 now records. Both halves of the rule
    that matter are unchanged: nothing on a deploy path resolves a version, and
    no cache entry is ever refreshed implicitly.

    A missing ``HMD_HOME`` disables the cache rather than failing the command.
    Refusing to answer a question that has already been answered because the
    answer cannot be filed would be the wrong trade.

.. spec:: Where it lives, and who may call it
    :id: HMD_CLI_NEURONSPHERE_NERD011_SPEC005
    :links: HMD_CLI_NEURONSPHERE_NERD011
    :status: implemented

    Two Go packages, split on the axis that matters -- whether the code touches
    a network:

    - ``internal/versionspec`` -- the specifier grammar, the satisfaction test
      and the ordering. **Pure**: no I/O, no ``net/http``, no dependency on
      ``internal/librarian``. It is a string-and-arithmetic package and shall be
      testable as one.
    - ``internal/versions`` -- the enumeration of SPEC001 and the cache of
      SPEC004. Depends on ``internal/librarian`` and on
      ``internal/versionspec``.

    The split is not tidiness. ``internal/versionspec`` is the half that
    ``nsctl status``, manifest validation and error messages can use freely,
    and keeping it free of a librarian client is what lets a dependency test
    assert that those callers cannot reach the network.

    Consumers, in the order they are expected:

    #. ``nsctl artifact versions <repo-class>`` -- reporting what is published
       and what a specifier resolves to today, writing nothing but the cache.
    #. ``nsctl artifact pull <repo-class>`` -- the same resolution, followed by
       the fetch that command already did.
    #. ``NERD005`` SPEC006 -- caching ``pre_build_artifacts``, whose specs are
       the same grammar and may carry ranges.

    **Implemented 2026-09-15**, and the consumer list above is an amendment. It
    previously named ``NERD010`` SPEC003 tier 3 -- ``nsctl lock`` resolving a
    manifest's ranges -- first, and that is the wrong home for this mechanism.

    ``nsctl lock`` snapshots an environment that has been stood up. Its first
    tier reads the instances ``ms-deployment`` is running, and those already
    carry concrete versions, so nothing there has a range to resolve; a lock born
    any other way is a guess that a set of independently-newest versions works
    together, where a running environment is proof. Its existing per-entry
    refusal -- pin it, or take a known-good environment -- is therefore honest
    advice rather than a missing feature.

    The place a version is actually **chosen** is one step earlier and one step
    later: adding repo classes while standing an environment up for the first
    time, and deciding what to move to when bumping one. Both are "ask a
    librarian what is published", and both are already the ``artifact`` verbs'
    business -- ``nsctl artifact pull`` is the one command documented as
    reaching a cloud librarian on the user's behalf, so nothing new is promised
    by putting this there.

    ``NERD010`` SPEC003 accordingly stays ``partial``, with tier 3 recorded as
    deliberately not built rather than outstanding.

.. spec:: What this is not
    :id: HMD_CLI_NEURONSPHERE_NERD011_SPEC006
    :links: HMD_CLI_NEURONSPHERE_NERD011
    :status: implemented

    **Not a change to how the cloud resolves anything.**
    ``hmd-ms-deployment`` keeps validating candidates exactly as it does today.
    This document reads a librarian and does arithmetic.

    **Not a fix for the two defects it documents.** ``Ordered``'s component-wise
    comparison and ``sort_versions``'s inverted key order are named here with
    evidence, and referred to ``hmd-ms-deployment``. Fixing them there is
    strictly better than compensating for them here, and this document is
    careful not to make either one load-bearing.

    **Not a dependency solver.** It answers "what satisfies this one specifier";
    it does not reconcile conflicting requirements across a graph. If two repo
    classes need incompatible versions of a third, that is a conflict a lock
    should report, not silently resolve, and it is not addressed here.

    That stands after implementation, and it is worth stating what it rules out.
    Two declarations naming one repo class with *different* specifiers, each
    admitting a different newest version, are to be **reported with both
    specifiers and what each admits** -- never reconciled by intersecting them
    and never settled by taking one. Intersecting is resolution across
    requirements, which is the line this SPEC draws; and ``lock.Build``'s
    existing refusal of a class pinned at two versions is the right answer rather
    than a limitation, because a lock pins a class once.

    **Not a package index.** Wheels are ``NERD006``.

    **Not on the deploy path.** See SPEC004.

Risks
-----

.. list-table::
   :header-rows: 1

   * - Risk
     - Severity
     - Mitigation
   * - The ported satisfaction test drifts from ``hmd-ms-deployment``'s, so
       ``nsctl`` picks versions the control plane rejects
     - **High**
     - A golden table of specifier/version pairs generated *from* the Python
       evaluator and asserted in Go, so drift fails a test rather than a
       deploy. The table is the contract; the prose is commentary.
   * - ``Ordered`` gets fixed upstream and the refusal becomes wrong
     - Medium
     - The refusal names the operators explicitly, so re-enabling them is one
       change in one package with the golden table to prove it.
   * - Enumeration is slow enough to make ``lock`` feel broken
     - Medium
     - Chunked batches, a cache, and per-repo progress output: 29 s of silence
       reads as a hang.
   * - A librarian's content items include paths this grammar cannot parse
     - Low
     - Unparseable paths are skipped, not fatal.
   * - The version cache goes stale and a lock is generated from old data
     - Low
     - The query time is recorded and reported; ``lock`` always re-queries.

Acceptance criteria
-------------------

#. A golden table derived from ``hmd_ms_deployment.version.VersionSpecifier``
   passes in Go, including every case listed in SPEC002 -- in particular
   ``~= 0.1`` accepting ``0.2.5`` and ``0.9.99``.
#. ``>=0.3`` is refused with a message naming ``~= 0.3`` and ``== 0.3.*``.
#. Enumerating ``hmd-inf-trino`` returns its published build versions, and
   resolving ``~= 0.1`` against them selects the highest satisfying version.
#. Enumerating a repo class with several hundred versions succeeds, proving the
   chunking.
#. Ordering ``0.1.100, 0.2.5, 1.0.3, 0.1.9, 2.0.1`` yields
   ``2.0.1, 1.0.3, 0.2.5, 0.1.100, 0.1.9`` -- that is, it does **not** reproduce
   ``sort_versions``.
#. ``go list -deps`` shows ``internal/versionspec`` importing neither
   ``net/http`` nor ``internal/librarian``.
#. A repo class absent from the librarian is reported as absent, distinctly
   from one present with no satisfying version.

Open questions
--------------

**Should a resolution be pinned to an item type other than ``build``?**
SPEC001 reads the item type out of the path, so the data is there. Nothing yet
asks for ``schema`` or ``configuration`` versions, and adding the axis before
something does is how an interface acquires an unused parameter.

**Should ``nsctl`` resolve against the local librarian when one is running?**
It would be faster and would work offline, but it would answer with what this
machine happens to hold rather than what is published -- which is the right
answer for a cache probe and the wrong one for choosing a version.

*Answered by implementation:* two verbs, as suspected. ``nsctl artifact
versions`` and ``nsctl artifact pull`` read a **cloud** librarian and resolve;
``nsctl artifact unpack`` reads the **local** one and requires an exact version,
which is why ``parseArtifactRequest`` and ``parseArtifactRef`` are separate
grammars rather than one with a flag. ``--offline`` answers from the version
cache, which records what was published rather than what is held here.

**What should a conflict look like?** SPEC006 declines to solve cross-graph
conflicts, and now records the answer: report both specifiers and what each
admits, never intersect and never take one. Whether a lock may then hold two
versions of one class is a ``NERD010`` question and the answer today is no.

This is not yet reachable, because no consumer resolves more than one specifier
at a time -- ``artifact versions`` and ``artifact pull`` each take one repo
class. It becomes reachable the day something resolves a whole manifest.
