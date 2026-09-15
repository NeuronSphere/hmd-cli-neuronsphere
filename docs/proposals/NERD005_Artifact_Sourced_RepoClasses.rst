.. NERD005 Artifact-Sourced RepoClasses

NERD005 Artifact-Sourced RepoClasses
====================================

.. req:: Deploy a RepoClass from a versioned artifact in the local Artifact Librarian
    :id: HMD_CLI_NEURONSPHERE_NERD005
    :status: proposed

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
    :status: proposed

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

.. spec:: The resolution tier, and precedence
    :id: HMD_CLI_NEURONSPHERE_NERD005_SPEC002
    :links: HMD_CLI_NEURONSPHERE_NERD005
    :status: proposed

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

.. spec:: Unpack and cache
    :id: HMD_CLI_NEURONSPHERE_NERD005_SPEC003
    :links: HMD_CLI_NEURONSPHERE_NERD005
    :status: proposed

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

.. spec:: ``nsctl artifact pull``
    :id: HMD_CLI_NEURONSPHERE_NERD005_SPEC004
    :links: HMD_CLI_NEURONSPHERE_NERD005
    :status: proposed

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

.. spec:: ``nsctl artifact register``
    :id: HMD_CLI_NEURONSPHERE_NERD005_SPEC005
    :links: HMD_CLI_NEURONSPHERE_NERD005
    :status: proposed

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

.. spec:: Pre-build artifacts
    :id: HMD_CLI_NEURONSPHERE_NERD005_SPEC006
    :links: HMD_CLI_NEURONSPHERE_NERD005
    :status: proposed

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

.. spec:: Offline behaviour and failure messages
    :id: HMD_CLI_NEURONSPHERE_NERD005_SPEC007
    :links: HMD_CLI_NEURONSPHERE_NERD005
    :status: proposed

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

.. spec:: Plugin distribution
    :id: HMD_CLI_NEURONSPHERE_NERD005_SPEC008
    :links: HMD_CLI_NEURONSPHERE_NERD005
    :status: proposed

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

.. spec:: Credentials
    :id: HMD_CLI_NEURONSPHERE_NERD005_SPEC009
    :links: HMD_CLI_NEURONSPHERE_NERD005
    :status: proposed

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
