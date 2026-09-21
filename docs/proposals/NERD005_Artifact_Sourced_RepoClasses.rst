.. NERD005 Artifact-Sourced RepoClasses

NERD005 Artifact-Sourced RepoClasses
====================================

.. include:: ../_includes/paid-cloud.txt

.. req:: Deploy a RepoClass from a versioned artifact in the local Artifact Librarian
    :id: HMD_CLI_NEURONSPHERE_NERD005
    :status: partial

    .. note::

       **The gate is passed, 2026-09-15.** "A manifest declaring
       ``source: {type: artifact}`` with a version deploys successfully with
       ``$HMD_REPO_HOME`` pointed at an empty directory" -- the criterion this
       document calls *the* gate, everything else being a detail of it -- was run
       live and holds. ``hmd-inf-s3bucket@0.1.13`` deployed from its artifact
       with an empty ``$HMD_REPO_HOME``, ignoring the checkout of that same class
       sitting in the real one, and Floci holds the bucket it created.

       This stays ``partial`` for **one** remaining criterion: ``hmd build`` in a
       checkout, then ``nsctl artifact register``, then an apply of a manifest
       naming that version, deploying the code just built with no network at any
       point. Re-registering and redeploying the *new* bytes is proven; the
       ``hmd build`` leg of the chain is not.

    A RepoClass declared in a manifest shall be deployable from a **versioned
    artifact held by the control plane's Artifact Librarian**, and not only
    from a working tree under ``$HMD_REPO_HOME``.

    ``nsctl`` shall be able to fill that librarian two ways: by **pulling** a
    versioned artifact from a cloud Artifact Librarian, and by **registering**
    the output of a local ``hmd build``. It shall additionally be able to
    extract a build's declared ``pre_build_artifacts`` and cache those
    locally, so a subsequent build resolves them from the control plane
    rather than from the cloud.

    This is how plugins are distributed. A user who wants an extension should
    need a version number, not a git checkout.

.. note::

    ``manifest.SourceArtifact`` already exists in ``internal/manifest``. It is
    accepted by the parser and rejected by validation, deliberately, *"so a
    manifest written against the eventual name fails with 'not supported yet'
    rather than 'unknown'."* This document is that eventual name arriving.

Motivation
----------

``nsctl`` resolves a RepoClass to a directory on disk, and there are only two
places that directory can come from today: a tree the binary carries
(``internal/bundled`` plus ``internal/repotree``), or a checkout under
``$HMD_REPO_HOME``. Both are wrong for distributing something.

- **Bundled** means shipping it inside ``nsctl``. That is right for the ten
  substrate repos ``make generate`` embeds and wrong for everything else: it
  couples an extension's release to the CLI's, and it is precisely the
  "built-in catalogue" that ``internal/bom`` was written to avoid. Note that
  those ten now *arrive* as published artifacts -- ``tools/repopack`` fetches
  them through ``internal/librarian``, which is the client SPEC005 and SPEC006
  need. What they still lack is the second half this document proposes: a
  manifest that can name one, and a resolver that reads it at deploy time
  rather than at build time.

- **A checkout** means the user clones a repository to run a plugin. That is
  the development path, and it is the right one *for development*. As a
  distribution mechanism it asks a user to have a git remote, credentials for
  it, and a correct branch, to run something they only want to consume.

Meanwhile the control plane already runs an artifact store. ``hmd-ms-artifact-lib``
is the second of the three ``bootstrapServices``, deployed before
``hmd-ms-deployment`` because everything above it depends on it, and reachable
at ``http://localhost/hmd_ms_artifact_lib/``. ``hmd build`` already zips a
repo's build output and archives it there under a well-known content path.
``hmd deploy`` already retrieves it. The only thing missing is for a manifest
to be able to say "use that one".

Scope
-----

**In scope.**

- ``source: {type: artifact}`` in both the environment manifest and the
  control-plane manifest (``NERD004`` SPEC001).
- A resolution tier for artifact-sourced instances, and its precedence
  against the tiers that already exist.
- Unpacking and caching the artifact under ``$HMD_HOME``.
- ``nsctl artifact pull`` (cloud librarian to local) and
  ``nsctl artifact register`` (local build to local librarian).
- Caching a build's declared ``pre_build_artifacts`` locally.

**Out of scope.**

- Publishing to a *cloud* librarian. ``hmd build`` with ``HMD_AUTO_PUBLISH``
  already does that, and this document deliberately does not add a second
  way.
- Package indexes. A wheel is not an artifact in this sense; see SPEC010 and
  ``NERD006``.
- Signing or provenance. Named as a gap in the risk table, not solved here.
- Replacing ``$HMD_REPO_HOME`` development. SPEC002 exists to protect it.

Reference: what already exists
------------------------------

Nearly every piece is present, which is the strongest argument for the shape
this document proposes.

.. list-table::
   :header-rows: 1
   :widths: 34 66

   * - Piece
     - Where it already is
   * - The store, running locally
     - ``hmd-ms-artifact-lib``, second in ``controlplane.bootstrapServices``,
       routed at ``http://localhost/hmd_ms_artifact_lib/``
   * - The content-path convention
     - ``artifact_tools.content_item_path_from_parts`` ->
       ``repository:/<repo>/<version>/<repo>_<version>_<type>.zip``
   * - The spec-string grammar
     - ``artifact_tools.content_item_path_from_spec`` parses
       ``<name>@<version>:<item_type>{:<file_part>}`` -- **the same grammar
       BACON's** ``build.pre_build_artifacts`` **already uses**
   * - Store and fetch
     - ``zip_and_archive()`` / ``retrieve_and_unzip()``, the pair ``hmd build``
       and ``hmd deploy`` use
   * - The reserved source kind
     - ``manifest.SourceArtifact``, parsed and rejected with "not supported yet"
   * - Staged unpack-and-cache
     - ``repotree.Dir`` -- stage, rename, package-level mutex, digest in the
       directory name
   * - The two-librarian copy
     - ``hmd_cli_neuronsphere.py``'s ``ns artifact pull`` / ``register``

That the pre-build-artifact spec string and the librarian content path are
already parsed by *the same function* is the finding that shapes SPEC006. The
two vocabularies are not merely compatible; they are one vocabulary that has
so far only been used from one end.

Architecture Overview
---------------------

::

    manifest                     resolution                    deploy
    --------                     ----------                    ------
    source: {type: local}    ->  $HMD_REPO_HOME/<class>    ->  runner mounts it
    source: {type: artifact} ->  local librarian
                                   -> .cache/.../artifacts/  ->  runner mounts it
                                      <class>@<version>/

    filling the librarian
    ---------------------
    cloud librarian  --(nsctl artifact pull)-->   local librarian
    hmd build output --(nsctl artifact register)-> local librarian
    pre_build_artifacts --(nsctl artifact cache)-> local librarian

The right-hand column is the point: an unpacked artifact is an ordinary host
directory, so it enters ``bom.RepoPaths()`` and ``runner.Config.RepoPaths``
exactly as an explicit ``source.path`` does today. Nothing downstream of
resolution learns that anything changed.

.. spec:: The artifact source kind
    :id: HMD_CLI_NEURONSPHERE_NERD005_SPEC001
    :links: HMD_CLI_NEURONSPHERE_NERD005
    :status: implemented

    ``source: {type: artifact}`` becomes valid in a manifest, in both the
    environment manifest and the control-plane manifest.

    .. code-block:: yaml

        repos:
          - instance_name: package-registry
            repo_class_name: hmd-inf-local-registry
            version: "0.1.4"
            source: {type: artifact}

    **A version is required.** ``manifest.Validate`` must refuse an
    artifact-sourced instance with no ``version``, because there is nothing
    to resolve one from: a working tree has ``meta-data/VERSION``, and an
    artifact is addressed *by* version. The sentinel ``0.1.0`` would be a
    silently wrong answer rather than a missing one.

    The content path is built with the existing helper, not a new one::

        repository:/<repo_class>/<version>/<repo_class>_<version>_build.zip

    The ``build`` item type is the default, and ``source.artifact_type``
    overrides it for a RepoClass distributed as something else. The permitted
    values are the librarian's existing content item types, not a new
    vocabulary -- see SPEC006.

    ``source.path`` remains meaningless for an artifact source and is
    rejected rather than ignored, in the same way ``RepoPath`` already
    returns ``""`` for a non-local source.

    **Amended 2026-09-15.** ``source.artifact_type`` is validated by *shape*
    (``^[a-z][a-z0-9_-]*$``) rather than against an allowlist of the librarian's
    content item types. The installer variants' exact strings are not present in
    this repository, and a wrong allowlist rejects a legitimate manifest with an
    error the author cannot fix -- whereas a wrong type fails at ``pull`` with
    ``ErrNotPublished`` naming the exact content path, which is already clear and
    actionable. This is a deliberate softening of a literal reading of the
    paragraph above.

.. spec:: The resolution tier, and precedence
    :id: HMD_CLI_NEURONSPHERE_NERD005_SPEC002
    :links: HMD_CLI_NEURONSPHERE_NERD005
    :status: implemented

    ``repoclass.Resolver`` gains ``SourceArtifact``. Where it sits is the
    substance of this SPEC, and it is the only decision in this document that
    is not forced by something already in the tree.

    The existing order, from ``ResolveVersion``: an explicit pin; a checkout
    the user asked for; the tree the binary carries; the version a BOM entry
    declared; any checkout under ``$HMD_REPO_HOME``; the sentinel.

    **Development beats distribution.** A working tree still wins when the
    user asked for it -- an explicit ``source.path``, ``=local``, or
    ``HMD_LOCAL_NEURONSPHERE_PREFER_LOCAL_VERSIONS`` -- because the entire
    purpose of ``$HMD_REPO_HOME`` is to run uncommitted changes, and an
    artifact source that overrode that would make the platform untestable by
    the people who build it.

    **Distribution beats an unasked-for checkout.** An instance declared
    ``source: {type: artifact}`` resolves from the librarian even when a
    checkout of that RepoClass happens to be sitting in ``$HMD_REPO_HOME``.
    This is the reversal of the last existing tier and it is deliberate: the
    manifest asked for a version, a stale checkout is not that version, and
    quietly substituting it produces the worst outcome available -- a deploy
    that reports a version it did not use.

    So the order becomes::

        1. HMD_LOCAL_VERSION_<REPO_CLASS>            (explicit pin)
        2. a checkout the user explicitly asked for  (source.path, =local, PREFER_LOCAL)
        3. the artifact, when source.type == artifact     <-- NEW
        4. the tree the binary carries
        5. the version a BOM entry declared
        6. any checkout under $HMD_REPO_HOME
        7. the sentinel

    When tier 2 pre-empts a declared artifact source, ``nsctl`` says so on the
    way past. "Using the working tree at ``<path>`` for ``<class>`` instead of
    the declared artifact ``<version>``" is a line the user asked for by
    setting the variable, and printing it costs nothing next to debugging its
    absence.

    ``Resolution.Source`` gains an ``artifact`` value so ``nsctl status`` can
    report *where* a version came from, which ``NERD004`` SPEC012 relies on.

    ``Resolver.Dir()`` returns the unpacked artifact tree for an
    artifact-sourced class, preserving the invariant the package already
    states: the version, the dependencies and the deploy sources all come
    from one place. A ``Dir()`` that disagreed with ``ResolveVersion()`` would
    deploy one version's code under another version's name.

    **Amended 2026-09-15.** Reading the code found that **there are two
    resolution paths, and this SPEC named only one.** ``repoclass.Resolver`` is
    the one described above; ``runner.Runner.repoPath`` re-implements the same
    tiers, holds no resolver, and is what a deploy node actually consults. An
    artifact tree reaches a deploy only through ``runner.Config.RepoPaths``, fed
    from ``internal/environment/apply.go``.

    Worse, ``bom.RepoPaths`` **silently drops artifact-sourced instances**,
    because it calls ``manifest.Repo.RepoPath``, which returns an empty string
    for any non-local source. So an artifact instance contributes nothing to
    ``repoPaths`` and ``Runner.repoPath`` then falls through to its own bundled
    and any-checkout tiers. That is the concrete mechanism of this document's
    High risk row -- "a deploy that reports a version it did not use" -- and it
    is not hypothetical.

    Consequently ``internal/environment/apply.go`` must seed the resolver's
    artifact refs **and** merge the artifact paths into ``repoPaths``. It is the
    single load-bearing integration point in this document, and an
    implementation that changes only ``repoclass`` will appear to work and
    deploy the wrong code.

    Collapsing the two duplicate tier implementations is worth doing and is
    explicitly **not** part of this document.

    **Implemented 2026-09-15**, and it found two more things than the amendment
    above predicted.

    ``Resolver.Dir`` must answer ``""`` for a declared artifact that is not
    cached, rather than continuing down the tiers. Falling through returns the
    bundled tree or a checkout -- code that is not the version ``ResolveVersion``
    just reported -- so the caller gets the wrong tree silently, which is the
    High risk arriving by a second route. ``Resolve`` needs the same guard on its
    directory fallback, or the dependencies come out of one build and the version
    out of another.

    **The runner's isolation flag is load bearing and was not in the plan.** An
    unpacked artifact reaches a deploy through ``runner.Config.RepoPaths``, which
    until now meant "a checkout somebody declared" -- and the runner mounts such a
    tree read-write and directly. An artifact tree is not that: it is keyed by
    version, shared by every environment on this ``HMD_HOME``, and a deploy
    writing ``meta-data/resources_output/`` into it would hand the next
    environment this one's resources. That is the exact hazard the bundled tier
    already isolates against. ``Runner.repoPath`` now asks whether a path lies
    under one of the cache roots rather than tracking the answer alongside it, so
    a third cache added later is covered by having put it in the same place.

    The pre-flight is ``internal/environment.checkArtifacts``, run before the plan
    for the reason ``EnsureBackendImages`` is: a manifest naming three uncached
    versions should say so in seconds and name all three.

    **Amended 2026-09-15.** ``Resolution.Source`` reaching ``nsctl status`` was
    only half done. ``nsctl control-plane repo list`` printed it; the environment
    listing did not, because ``nsctl repo list`` carried two different facts in
    one SOURCE column -- where the *declaration* came from, beside the manifest's
    *unresolved* version. An artifact-sourced instance read ``0.1.4 / manifest``
    and a working tree read ``- / manifest``, so neither said where the version
    came from, which is the acceptance criterion.

    The column is split: DECLARED keeps the three literals, FROM carries
    ``Resolution.Source``, and VERSION is the resolved version rather than the
    declared one. The listing builds a real resolver the way ``apply.go`` does --
    resolution is offline by construction and every tier either stats the
    filesystem or answers from the manifest, so it adds no way for a listing to
    fail, which matters because ``repo list`` is already best-effort about a
    control plane that is not running. A declared artifact whose bytes are absent
    reads ``artifact (uncached)`` and the listing names the ``nsctl artifact
    pull`` that fixes it: this is where a user notices, rather than at the apply
    that refuses.

    That made three call sites seeding a resolver from a manifest, so the
    seeding is now one helper, ``repoclass.Seed``. The line worth holding in one
    place is the one above: an artifact instance goes into ``Artifacts``, never
    into ``Paths``. Unifying them also settled a disagreement the two existing
    sites had -- the environment path compared the *resulting* paths and dropped
    one equal to the ``$HMD_REPO_HOME/<class>`` convention, while the
    control-plane path asked only whether ``source.path`` was set, so a
    control-plane manifest spelling the convention out got a tier-two, unstat'd
    answer where an environment manifest got a tier-six one. The stricter rule
    is the one kept: a manifest naming the convention is naming the convention,
    however it wrote it down.

    This is the seeding duplication only. The two duplicate *tier*
    implementations -- ``repoclass.Resolver`` and ``runner.Runner.repoPath`` --
    remain, and remain out of scope.

.. spec:: Unpack and cache
    :id: HMD_CLI_NEURONSPHERE_NERD005_SPEC003
    :links: HMD_CLI_NEURONSPHERE_NERD005
    :status: implemented

    A fetched artifact is unpacked to::

        $HMD_HOME/.cache/neuronsphere/artifacts/<repo_class>@<version>/

    **This one is cache**, and the contrast with ``NERD004`` SPEC008 is worth
    stating: an unpacked artifact is wholly reconstructible from the
    librarian, so deleting it costs a refetch and nothing else. ``NERD006``'s
    hosted wheels are not, and do not live here.

    The mechanics are ``repotree``'s, reused rather than rewritten: unpack to
    a staging directory, rename into place, hold a package-level mutex, and
    put the identifying token in the directory name so two versions coexist
    and a concurrent unpack cannot half-populate the one being read.
    ``repotree`` uses a content digest; this uses the version, because the
    version is what the manifest asked for and what ``status`` will report.

    **Present means no fetch.** Resolution is idempotent and offline for an
    already-cached version. This is what makes ``nsctl env apply`` on a
    disconnected machine work at all.

    An open question left to implementation: whether a *cached* artifact
    should ever be re-verified against the librarian. Immutability of a
    published version is the assumption; a local ``register`` overwriting one
    is the case that breaks it, and SPEC005 addresses it there.

    **Amended 2026-09-15.** "Reuse ``repotree``'s mechanics" means reuse its
    *control flow*, not its code: ``repotree.Unpack`` reads a gzipped tar, and
    artifacts are zips. What is mirrored is the sequence -- stat the
    token-named directory, take a package-level mutex, unpack to a staging
    directory, rename into place, re-stat on a lost rename race -- which is
    roughly thirty-five lines.

    A correct, zip-slip-safe zip extractor **already exists** as the unexported
    ``unzip`` in ``tools/repopack``. Lifting it into the new package and having
    ``repopack`` call it is a deletion rather than an addition.

    One narrowing of the Medium risk row: validation of an unpacked tree
    requires ``meta-data/manifest.json`` and ``meta-data/VERSION`` but **not**
    ``src/``, because SPEC001 permits ``schema`` and ``configuration`` item
    types, which legitimately have no ``src/``.

    The open question about a shared cache versus ``env purge`` is **already
    answered by the code**: purge removes an environment's state directory and
    never touches ``.cache/neuronsphere``. One cache keyed by class and version
    is correct and needs no purge work, only a test asserting the cache root
    lies outside any environment state directory.

    **Implemented 2026-09-15** as ``internal/artifact``. ``Store`` mirrors
    ``repotree.Dir``'s control flow and returns an error where ``repotree``
    returns ``""``; ``Cached`` stats ``meta-data`` inside the tree rather than
    the tree itself; ``UnzipInto`` is ``tools/repopack``'s ``unzip``, lifted, and
    ``repopack`` now calls it. The package imports ``internal/manifest`` and the
    standard library and nothing else, which is what makes the offline guarantee
    a property of the dependency graph rather than of a runtime flag --
    ``repoclass`` will import *this* package, so a fetch cannot be reached from
    resolution even by accident.

.. spec:: ``nsctl artifact pull``
    :id: HMD_CLI_NEURONSPHERE_NERD005_SPEC004
    :links: HMD_CLI_NEURONSPHERE_NERD005
    :status: implemented

    ``nsctl artifact pull <repo_class>@<version>[:<type>]`` downloads a
    versioned artifact from a cloud Artifact Librarian and stores it in the
    control plane's local one.

    Two librarians, two endpoints, two credential stories:

    .. list-table::
       :header-rows: 1

       * -
         - Endpoint
         - Auth
       * - Cloud
         - ``HMD_ARTIFACT_LIBRARIAN_URL``, defaulting to
           ``https://artifact-aaa-{region}.{customer}-admin-neuronsphere.io``
         - ``HMD_ARTIFACT_LIBRARIAN_API_KEY`` or an Okta token, resolved per
           ``NERD004`` SPEC009
       * - Local
         - ``http://localhost/hmd_ms_artifact_lib/``
         - anonymous; the key is the literal ``local-dummy``

    The behaviour is the Python ``ns artifact pull``'s, ported rather than
    reinvented: build the content path from the parts, ``get_file`` from the
    cloud into a temporary file, ``put_file`` into the local librarian under
    the same content path and content item type, and restore whatever
    endpoint configuration was in effect.

    Two traps that prior art already found, and which a Go port will hit
    again:

    - **The trailing slash on the local URL is load bearing.** ``urljoin``
      drops the last path segment when the base lacks one, so
      ``http://localhost/hmd_ms_artifact_lib`` resolves to ``/apiop/...`` and
      misses nginx's location block entirely. Normalise it.
    - **The temporary file is not in ``$HMD_HOME``.** It is an artifact in
      flight, and leaving it behind in a state directory makes it look like
      a cached one.

    ``pull`` is explicit. Resolution does **not** silently reach for the
    cloud: a missing artifact fails per SPEC007 and names the command that
    would fetch it. An apply that reaches the internet without being asked is
    an apply that behaves differently on an aeroplane.

    **Amended 2026-09-15.** Two traps named above do not survive the port, and
    one new constraint replaces them.

    **The temporary file should not exist in Go.** ``librarian.Client.Fetch``
    already returns the artifact as bytes; writing them to disk in order to read
    them straight back adds an I/O round trip *and* creates the very
    left-behind-temp-file hazard this SPEC warns about. Peak memory is one build
    zip. If streaming ever matters, it belongs inside ``Fetch``, not in a
    temporary file.

    **The trailing-slash trap is a Python ``urljoin`` bug and does not exist in
    Go.** The Go client trims the trailing slash and concatenates the path. Keep
    the slash in the local-URL constant regardless, and pin it with a test, so
    that a later refactor to ``url.JoinPath`` cannot reintroduce it.

    **Endpoint overrides must not mutate the process environment.** Python saves
    and restores ``HMD_ARTIFACT_LIBRARIAN_URL`` in a ``finally``; the Go port
    layers an override over the injected lookup instead, because every test in
    ``cmd/`` runs in parallel and ``t.Setenv`` panics there.

    Version *ranges* on ``pull`` are out of scope, and the open question about
    them is transferred to ``NERD010`` SPEC003, which owns version resolution.

    **Implemented 2026-09-15.** ``pull`` fetches from the cloud, stores in the
    control plane's librarian, **and** unpacks into the cache, because resolution
    never fetches and a librarian holding bytes nothing has unpacked resolves to
    nothing.

    That left one state unreachable -- the librarian holds a version whose
    unpacked copy has been deleted -- so ``nsctl artifact unpack`` exists as the
    repair path: control plane to cache, no cloud round trip and nothing rebuilt.
    It is also what makes the offline acceptance criterion testable without a
    network at all.

.. spec:: ``nsctl artifact register``
    :id: HMD_CLI_NEURONSPHERE_NERD005_SPEC005
    :links: HMD_CLI_NEURONSPHERE_NERD005
    :status: implemented

    ``nsctl artifact register [<path>]`` stores a local ``hmd build`` output
    in the control plane's librarian, so a developer's own build becomes
    deployable by an artifact-sourced manifest without the cloud being
    involved at any point.

    The input is the zip ``hmd build`` writes to ``$HMD_BUILD_OUTPUT_DIR``
    when ``HMD_AUTO_PUBLISH`` is *not* set -- the existing fork in
    ``hmd-cli-build``'s controller, where the same artifact either goes to the
    librarian or is written locally. ``register`` is the second half of that
    fork, made available after the fact.

    With no argument it resolves the repo name and version from
    ``meta-data/manifest.json`` and ``meta-data/VERSION`` in the working
    directory, as ``_resolve_repo_and_version`` already does.

    **Re-registering a version overwrites it**, and this must be said out
    loud because it is the one place the immutability assumption in SPEC003
    breaks. The rule: ``register`` overwrites in the librarian *and*
    invalidates the unpacked cache entry for that version, so the next
    resolution re-unpacks. A ``register`` that left a stale unpacked tree in
    place would produce a deploy of the previous build with the new build's
    version, which is the exact failure SPEC002 refuses to allow from the
    other direction.

    This closes the loop that makes an extension developable at all: build
    it, register it, and the manifest that names it by version picks it up --
    without a checkout in ``$HMD_REPO_HOME`` and without a cloud round trip.

    **Amended 2026-09-15.** Two decisions that narrow this SPEC.

    **``register`` does not shell out to ``hmd build``.** The Python
    ``push_artifact`` does so by default, but ``nsctl``'s premise is that Docker
    is the only host prerequisite, and a ``register`` that needs the Python CLI
    installed fails in exactly the environment ``nsctl`` exists to serve. The
    input is therefore, in order: an explicit zip; a directory zipped on the
    fly; or the conventional path ``hmd build`` writes under
    ``$HMD_BUILD_OUTPUT_DIR`` when ``HMD_AUTO_PUBLISH`` is unset. With none of
    the three, ``register`` fails naming all three and printing the ``hmd
    build`` invocation that produces the last.

    **Multipart upload is refused loudly rather than implemented.** The put
    protocol is a three-leg exchange -- ``/apiop/put`` for presigned part URLs,
    a credential-free ``PUT`` per part, then ``/apiop/close`` with the ETags --
    and the client chooses the part layout. Every artifact in scope here is a
    single part under the hundred-megabyte default, and the upstream client
    sends the content type **only** when there is exactly one part, so the
    multipart header contract is unobserved and cannot be ported faithfully.
    The implementation requests one part and, if the service answers with more
    than one, returns a typed error naming the size. Silently uploading the
    first part alone would store a corrupt zip that fails to unpack three
    commands later.

    Two Go-specific requirements on the presigned leg, both discovered rather
    than designed: the request's content length must be set explicitly, or Go
    sends a chunked body which presigned uploads reject; and no credential
    header may be sent, for the same reason ``Fetch`` sends none.

    **Partly implemented 2026-09-15.** The upload leg exists as
    ``librarian.Client.Put`` and ``librarian.NewLocal``; the ``register`` command
    that drives it does not yet, so this SPEC stays short of implemented.

    ``NewLocal`` deliberately does not go through ``New``, which fails when
    neither an API key nor a token is configured. That is correct for a cloud
    librarian and would make ``register`` impossible on the machine ``nsctl``
    exists to serve, since the local librarian is anonymous. ``token`` is simply
    left empty rather than a flag being added for it -- ``headers()`` already
    omits the header when there is none, so the zero value says "anonymous"
    without a second thing to keep in step.

    Nothing is retried, which is a decision rather than an omission and is
    recorded in the doc comment: both API legs are loopback and the upload is a
    single buffered part, and a retry cannot be added later without also changing
    the body, because replaying a request built over an ``io.Reader`` uploads zero
    bytes on the second attempt and reports success.

.. spec:: Pre-build artifacts
    :id: HMD_CLI_NEURONSPHERE_NERD005_SPEC006
    :links: HMD_CLI_NEURONSPHERE_NERD005
    :status: partial

    BACON declares a repo's build-time inputs as::

        "build": {
          "pre_build_artifacts": [["hmd-lang-foo@0.3:schema", "src/schemas/"]]
        }

    ``nsctl`` shall be able to extract those from a build and cache them in
    the local librarian, so a subsequent build in the same ``HMD_HOME``
    resolves them locally instead of reaching for the cloud.

    **The spec string is already the librarian's own grammar.**
    ``artifact_tools.content_item_path_from_spec`` parses
    ``<name>@<version>:<item_type>{:<file_part>}`` and returns a
    ``repository:/`` content path -- the same string BACON writes, parsed by
    the same function, into the same address space. Nothing needs translating
    and nothing new needs naming.

    Likewise the ``<item_type>`` suffix maps onto an axis the librarian
    already has. ``hmd-ms-artifact-lib`` ships content item types for
    ``build``, ``transform``, ``configuration``, ``schema``, ``entity``,
    ``binary``, ``secret`` and the installer variants. This is a new *use* of
    the type dimension, not a new dimension -- and inventing a parallel type
    vocabulary here is the obvious wrong turn, which is why it is called out.

    Two directions, both wanted:

    - **Cache what a build consumed.** After a build resolves its
      pre-build artifacts from the cloud, store them in the local librarian
      under the identical content path. The next build in this ``HMD_HOME``
      finds them without the network.
    - **Cache what a build produced.** A repo that *is* another repo's
      pre-build artifact -- a language pack, a schema bundle -- becomes
      locally resolvable through SPEC005's ``register``, with the item type
      taken from what was built rather than defaulted to ``build``.

    Whether ``hmd build`` consults the local librarian first is a question for
    ``hmd-cli-build``, not for ``nsctl``, and this SPEC deliberately stops at
    making the artifacts present and correctly addressed. Recording the
    boundary matters: a change to the resolution order inside ``hmd build``
    affects every build everywhere, including CI, and does not belong in a
    local-platform NERD.

    **Partly implemented 2026-09-15** as ``nsctl artifact cache``, which reads
    ``build.pre_build_artifacts`` and copies each entry from the cloud librarian
    into the control plane's under the identical content path. The parser is
    shared with ``tools/repopack``, which had the only copy of it.

    The risk table's condition -- run against a real repo with real
    ``pre_build_artifacts`` -- **was met on 2026-09-15**: all 14 of this
    repository's entries copied from the cloud librarian into the control plane's.

    It stays *partial* for the other direction only: a repo that *is* another
    repo's pre-build artifact, registered with its own item type through
    ``register --type``. That path is unit-tested and has still not been
    exercised against a real build.

.. spec:: Offline behaviour and failure messages
    :id: HMD_CLI_NEURONSPHERE_NERD005_SPEC007
    :links: HMD_CLI_NEURONSPHERE_NERD005
    :status: implemented

    With no network, an already-cached artifact deploys. That is the whole
    point of caching it, and it is the acceptance test.

    A *missing* artifact fails with the version that was wanted and every
    place that was looked, not a bare 404 from the librarian's HTTP client::

        hmd-inf-local-registry@0.1.4 is not available.
          artifact cache:   $HMD_HOME/.cache/neuronsphere/artifacts/hmd-inf-local-registry@0.1.4  (absent)
          local librarian:  repository:/hmd-inf-local-registry/0.1.4/...  (404)
          working tree:     $HMD_REPO_HOME/hmd-inf-local-registry  (absent; and not requested)
        Fetch it with:  nsctl artifact pull hmd-inf-local-registry@0.1.4

    This is the standard ``NERD002`` set for the cold bootstrap, and the
    reason is the same. The distinguishable failures here are "the version
    was never pulled", "the local librarian is not running", and "the cloud
    credential expired", and all three arrive at the caller as a failed HTTP
    request unless something takes the trouble to tell them apart.

    In particular: if the local librarian itself is unreachable, say *that*.
    A librarian that is down and a librarian that does not have the artifact
    are one status code apart and a long debugging session apart.

    **Amended 2026-09-15.** There is a **fourth** distinguishable cause, and it
    is the one most likely to be met first on the local leg.

    The local librarian's ``/apiop/get`` answers with a presigned URL hosted at
    ``neuronsphere:4566``, which resolves from the host only because of an
    ``/etc/hosts`` entry plus the nginx stream that fronts it. When a download
    fails with a name-resolution error, that is neither a missing artifact nor a
    down librarian, and reporting it as either sends the reader in the wrong
    direction. ``controlplane.CheckHostsEntries`` is the existing diagnostic and
    its output is the remedy to print.

    The message for the ordinary case -- resolution found nothing cached -- must
    also say that **nothing was contacted**, rather than implying a 404 from a
    librarian it never asked. Resolution is offline by construction, and a
    message that suggests otherwise invites the reader to debug a network.

    The shape of this message, and its branching on the credential cases, is
    already implemented as ``unavailable`` in ``tools/repopack`` and should be
    ported rather than reinvented.

.. spec:: Plugin distribution
    :id: HMD_CLI_NEURONSPHERE_NERD005_SPEC008
    :links: HMD_CLI_NEURONSPHERE_NERD005
    :status: implemented

    Taken with ``NERD004``, this is a complete distribution story for an
    extension, and it is worth writing out end to end because neither
    document contains all of it.

    **The author** builds the RepoClass and publishes it, once, with the
    existing ``hmd build`` and ``HMD_AUTO_PUBLISH`` path. Nothing new.

    **The consumer**:

    .. code-block:: bash

        nsctl artifact pull hmd-inf-local-registry@0.1.4
        # then, in $HMD_HOME/.config/control-plane.yaml:
        #   - instance_name: package-registry
        #     repo_class_name: hmd-inf-local-registry
        #     version: "0.1.4"
        #     source: {type: artifact}
        nsctl control-plane start

    No git remote, no checkout, no credentials beyond the librarian's, and no
    change to ``nsctl``. That last clause is the test of whether this
    mechanism is real: if shipping a new extension requires a new ``nsctl``
    release, the extension surface is decorative.

    **The developer of an extension** keeps the checkout path unchanged.
    SPEC002's precedence is what lets both coexist on one machine, and
    ``nsctl status`` reporting the version *source* is what keeps the reader
    aware of which one is live.

    **Amended 2026-09-15.** The consumer side of this story now has its own
    document. ``NERD010`` gives a *repository* a way to declare the versioned
    artifacts it needs in order to be tested locally -- profile-gated companions
    in its BACON manifest, pinned in a checked-in ``neuronsphere.lock`` -- and
    one command that creates an environment from them. The end-to-end motion
    above remains correct for a hand-authored manifest; ``NERD010`` is what
    makes it reproducible across machines.

    **Implemented 2026-09-15.** Every command in the motion above exists, the
    whole chain is covered end to end at the unit level against librarians stood
    up in-process, and it has now been run against a live control plane -- which
    is the condition this SPEC was holding out for.

    The run that closed it belongs to ``NERD010``, and that is not a coincidence:
    the consumer story's last step is an environment that deploys what it was
    given, and ``NERD010`` is the document that creates one. With
    ``$HMD_REPO_HOME`` pointed at an empty directory,
    ``hmd-inf-s3bucket@0.1.13`` deployed from its artifact and Floci holds the
    bucket its CDKTF stage created.

.. spec:: Credentials
    :id: HMD_CLI_NEURONSPHERE_NERD005_SPEC009
    :links: HMD_CLI_NEURONSPHERE_NERD005
    :status: implemented

    The cloud librarian's credential is resolved per ``NERD004`` SPEC009:
    named in configuration, resolved from the host keychain, never written
    under ``$HMD_HOME``, never logged.

    The local librarian is anonymous. Its API key is the literal string
    ``local-dummy``, and it is reachable only through ``hmd_proxy`` on
    loopback. That is a deliberate property rather than an oversight, and it
    is the same trade ``NERD006`` SPEC010 makes for the package registry: the
    threat model for a single-developer local platform does not include an
    attacker who already has the loopback interface.

    An artifact carries no credential of its own. If a RepoClass's *deploy*
    needs one, it takes it from ``NERD004`` SPEC009 like any other extension;
    packaging a secret into a distributed artifact is out of scope and should
    stay that way.

.. spec:: What this is not
    :id: HMD_CLI_NEURONSPHERE_NERD005_SPEC010
    :links: HMD_CLI_NEURONSPHERE_NERD005
    :status: proposed

    **This is not a package index.** The Artifact Librarian is
    content-addressed: a caller asks for an exact content path and receives
    those bytes. A package index resolves a *name and a version constraint*
    through PEP 503 or the npm or Go protocols, and returns a set of
    candidates. ``NERD006`` needs the second thing, and the fact that these
    two documents land together is exactly why the boundary needs stating --
    "we already have a registry" is a true sentence about the wrong noun.

    **This is not a replacement for ``$HMD_REPO_HOME``.** SPEC002's
    precedence exists to protect the development path, not to deprecate it.

    **This is not a new artifact store.** No new bucket, no new service, no
    new entity type. ``hmd-ms-artifact-lib`` is already running in the
    control plane; this document gives it a second reader.

    **This does not publish to the cloud.** ``register`` writes to the local
    librarian only. Publishing remains ``hmd build`` with
    ``HMD_AUTO_PUBLISH``, and adding a second path to a shared store from a
    local-platform CLI would be a mistake regardless of how convenient it
    looked.

Acceptance criteria
-------------------

The requirement is satisfied when:

- A manifest declaring ``source: {type: artifact}`` with a version deploys
  successfully **with ``$HMD_REPO_HOME`` pointed at an empty directory**.
  This is the gate; everything else is a detail of it.

- ``nsctl artifact pull <class>@<version>`` fetches from a cloud librarian
  and the artifact is subsequently retrievable from the local one.

- ``hmd build`` in a checkout, followed by ``nsctl artifact register``,
  followed by an apply of a manifest naming that version, deploys the code
  that was just built -- with no network access at any point.

- Re-running ``register`` after a code change and re-applying deploys the
  *new* code, not the previously unpacked tree.

- With the network removed, an apply of an already-cached artifact succeeds,
  and an apply naming an uncached version fails with the message shape in
  SPEC007.

- ``nsctl status`` distinguishes "0.1.4 from an artifact" from "0.1.4 from a
  working tree".

- An artifact-sourced instance whose RepoClass also happens to be checked out
  under ``$HMD_REPO_HOME`` deploys from the artifact, unless the user asked
  for the checkout -- and says which it used.

Risks and open questions
------------------------

.. list-table::
   :header-rows: 1
   :widths: 12 38 50

   * - Level
     - Risk
     - Mitigation
   * - **High**
     - A silent fall back to a stale checkout deploys code that is not the
       version reported. Every downstream diagnosis then starts from a false
       premise.
     - SPEC002 makes the artifact win over an unasked-for checkout, and makes
       ``nsctl`` announce the case where a checkout deliberately pre-empts an
       artifact.
   * - **High**
     - ``register`` overwrites a version in the librarian while an unpacked
       copy of it sits in the cache. The next deploy uses the old tree under
       the new version.
     - SPEC005 requires ``register`` to invalidate the cache entry. This is
       an acceptance test, not a code comment.
   * - **Medium**
     - Nothing verifies an artifact's integrity or origin. A local librarian
       is trusted absolutely, and ``pull`` copies whatever the cloud returned.
     - Out of scope and recorded as such. ``put_file`` already checksums in
       transit, which covers corruption but not provenance. Signing is a
       follow-on and should not be quietly assumed to exist.
   * - **Medium**
     - The unpacked artifact must contain what the runner expects --
       ``meta-data/manifest.json``, ``meta-data/resources/*.yaml``,
       ``src/``. A build zip whose layout differs fails deep inside a deploy
       node.
     - Validate the unpacked tree at unpack time against what
       ``repoclass.LoadManifest`` and ``Produces`` need, and fail there with
       the path that was missing.
   * - **Medium**
     - The pre-build-artifact extraction path (SPEC006) is designed from the
       BACON spec and the shared parser, not from a build observed doing it.
     - Written as an acceptance gate to be run against a real repo with a
       real ``pre_build_artifacts`` entry before the SPEC is marked
       implemented.
   * - **Low**
     - ``$HMD_HOME/.cache/neuronsphere/artifacts/`` grows without bound, one
       directory per version ever resolved.
     - Bounded by hand today. A ``nsctl artifact prune`` is the obvious
       follow-on and is deliberately not specified here.

Open questions, to be settled in implementation:

- **Whether ``pull`` should accept a version range.** ``@0.1`` meaning "the
  newest 0.1.x" is what a user will type. It requires the librarian to
  support a search rather than a fetch, which ``search_librarian`` may
  already cover, and it introduces the question of what a manifest naming
  ``"0.1"`` resolves to on two different days.

- **Whether an environment manifest and the control-plane manifest should
  share one artifact cache.** They should -- it is keyed by class and version
  and nothing about it is scope-specific -- but the purge story then needs
  saying: ``env purge`` must not delete a cache the control plane is using.

- **Whether ``nsctl`` should be able to pull *transitively*,** fetching the
  artifacts a RepoClass's dependencies name. The manifest already carries
  dependency edges, so it is possible; whether it is desirable at the point a
  user is deliberately offline is less clear.
