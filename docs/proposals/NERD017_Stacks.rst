.. NERD017 Stacks

NERD017 Stacks
==============

.. req:: Install a published set of RepoClasses into a local environment with one command
    :id: HMD_CLI_NEURONSPHERE_NERD017
    :status: implemented

    A **stack** is a RepoClass whose repository carries a ``local`` section
    (``NERD010`` SPEC001) and a checked-in ``neuronsphere.lock``, published as
    **one** artifact that holds the build zip of every RepoClass the lock pins.

    A user with **no credential of any kind** shall be able to add a stack to
    a local environment with one command, naming only the stack and a version
    or range, and deploy it with ``nsctl env apply``. After the one fetch the
    environment shall be as offline as any other.

    ``nsctl`` shall carry **no list of stacks**. A stack exists because a
    registry serves it under a name the user typed, and for no other reason.

Motivation
----------

The most common thing a plugin will be is not code. It is "the three
RepoClasses that make up observability", or "the four that make a warehouse
you can query", pinned to versions that are known to work together, and
wanted by someone who has just installed ``nsctl`` and does not have a
tenant. Everything needed to *describe* that already exists:

* ``NERD010`` gave a repository a ``local`` section to declare the companions
  it wants, profiles to make some of them optional, and a generated
  ``neuronsphere.lock`` at the repository root pinning each to a version and a
  content path. Its own open question asked whether "a platform team could
  publish a 'standard local stack' lock". This is that.
* ``nsctl env add --from-repo`` (``cmd/fromrepo.go``) already turns a lock into
  an environment: it activates profiles, resolves instance names through the
  four-tier naming order, declares every companion as
  ``source: {type: artifact}``, records ``bindings``, and hands off to
  ``env apply``.
* ``NERD005`` gave ``source: {type: artifact}`` a cache to resolve from, and
  made resolution offline by construction.

What does not exist is a way to *obtain* the artifacts without a tenant. That
is ``NERD016``'s job. This document is what sits between the two: what a
published stack looks like, and the verbs that move it from a registry into
an environment.

The lock is the pivot. It already names every class, every version, and the
roles each fills, and it is already the thing a reader knows to look for. A
stack's OCI artifact therefore carries the lock as its config blob and a
layer per zip, and installing a stack is: fetch, verify, unpack into the
cache, then run the ``--from-repo`` planner over the cached stack tree as if
the user had cloned it. No second planner, no second naming order, no second
manifest shape.

Scope and terminology
---------------------

* A **stack** is the RepoClass; a **stack artifact** is its published OCI
  form; a **stack record** is what an environment manifest keeps about one it
  has added.
* A **companion** is a RepoClass the lock pins (the ``NERD010`` word).
* The **subject** is the stack RepoClass itself, deployed from its own zip.
  ``--from-repo`` declares the subject ``source: local``; a stack declares it
  ``source: artifact``. That is the one difference in the planner.
* The **canonical namespace** for stacks published by the platform is
  ``ghcr.io/neuronsphere/stacks``, derived from
  ``repoclass.DistributionRegistry``. A stack may live anywhere; the namespace
  is a *default expansion* for a bare name, not a registry of known stacks.

  .. note:: Amended 2026-09-21. The namespace was first specified as
     ``ghcr.io/hmdlabs/stacks``, derived from ``repoclass.PublishedRegistry``
     (the image registry). Stacks and plugins are distributed from the public
     ``neuronsphere`` org instead -- where ``nsctl`` is released and where a
     stack repository's ``GITHUB_TOKEN`` can push -- while images stay on
     ``ghcr.io/hmdlabs`` and are named by full reference inside the stack.

Out of scope: composing one lock from another (still ``NERD010``'s open
question), a stack that carries images rather than referencing them, and any
account of what a stack costs or who may publish under the canonical
namespace (a ``ghcr.io`` permission, not an ``nsctl`` one).

.. spec:: What a stack is
    :id: HMD_CLI_NEURONSPHERE_NERD017_SPEC001
    :links: HMD_CLI_NEURONSPHERE_NERD017
    :status: implemented

    A stack repository is an ordinary RepoClass repository with three
    properties: a ``local`` section in ``meta-data/manifest.json`` (or
    ``.toml``) naming its companions, ``local.stack: true`` in that section,
    and a ``neuronsphere.lock`` generated from it.

    .. note::

        Because a stack is an ordinary RepoClass, it declares its front door and
        credentials with NERD023 SPEC004's top-level ``access`` block and nothing
        stack-specific.  A stack's reported access is its own entries together
        with those of the instances it declared -- which the environment
        manifest's stack record already identifies -- so the declaration belongs
        to each workload's own RepoClass and applies however that instance
        arrived.  A stack authored later inherits it without restating it.

    ``local.stack`` reverses this spec's original rule that nothing marks a
    stack, "because the two files are the declaration". That held only while
    every stack was an instance, which made the distinction cost nothing. It
    does not hold now. A wrapper and an ordinary repository under test have the
    same shape -- both carry a ``local`` section, both may omit
    ``deploy.commands`` -- so deciding from shape alone drops the repository the
    user pointed at, which is the one thing ``--from-repo`` exists to declare.
    The key sits in ``local`` because that is the section a stack already owns,
    and a repository with no ``local`` section can never be one.

    A stack that *does* carry something -- a dashboard, a seed job, an
    ``hmd.env`` handback -- is itself one instance of the environment, deployed
    from its own build zip the normal way. It still sets ``local.stack``; the
    marker says what the repository *is*, and its deploy phase says what it
    *does*.

    A stack that carries nothing is **not** an instance. It is declared in the
    ``stacks`` record (SPEC009) and nowhere else: no instance, no deploy node,
    nothing in the BOM. ``stack list`` reads that record, ``stack remove`` takes
    it as its subject, and ``env status`` reports the stack from it rather than
    from an instance. Carrying nothing means no ``deploy.commands``; the key
    may then be omitted entirely, since nothing reads it.

    This supersedes the original rule, which made every stack an instance and
    gave an empty one a ``NERD009`` exec no-op
    (``deploy.commands: [["exec", "true"]]``). That rule was wrong twice over.

    It does not work. ``NERD009`` SPEC005 passes an exec node's argv to the
    image verbatim so the image's own entrypoint receives it, and an empty
    ``deploy.image`` means projectbuilder, whose entrypoint is ``hmd``. The
    argv ``true`` therefore runs as ``hmd true``, which argparse rejects with
    exit 2. Dropping ``deploy.commands`` instead is worse: ``hmd-cli-deploy``
    reads ``manifest["deploy"]["commands"]`` unguarded and raises ``KeyError``.
    An empty list is the only spelling that survives both, and it exists only
    to satisfy a node that should not be there.

    It is not needed. The two reasons given for the instance -- a subject for
    ``stack remove`` and visibility in ``env status`` -- are both served by the
    ``stacks`` record SPEC009 already defines. The third, a stack with a real
    payload, is preserved above.

    What it costs is a projectbuilder container per apply to run nothing, and a
    node that can fail -- and did, three distinct ways -- for a RepoClass whose
    entire job is to name other RepoClasses.

    The lock is authoritative over the manifest's version specs: what is
    installed is what is pinned, and a range in the ``local`` section is
    advice to ``nsctl lock``, not to ``stack add``.

.. spec:: The stack artifact
    :id: HMD_CLI_NEURONSPHERE_NERD017_SPEC002
    :links: HMD_CLI_NEURONSPHERE_NERD017
    :status: implemented

    **Amended 2026-09-21.** Each layer also carries
    ``org.opencontainers.image.licenses`` when the class's manifest declares
    a ``license`` (SPEC011): the declared SPDX expression, read from the
    manifest inside the zip, whichever source the zip came from. The
    manifest-level ``org.opencontainers.image.licenses`` is the *subject's*
    declaration, not a fixed ``Apache-2.0``: a stack is nothing by fiat, and
    a third party's stack is whatever they say it is. An undeclared class
    or subject is unannotated. A zip ``nsctl`` makes from a tree leaves out
    what the tree's manifest declares under ``license.exclude``; a zip that
    arrives as bytes is published whole. The text below is the original.

    A stack is published as one OCI image manifest:

    ================================ ======================================================
    Field                            Value
    ================================ ======================================================
    ``artifactType``                 ``application/vnd.neuronsphere.stack.v1+toml``
    ``config``                       the ``neuronsphere.lock`` bytes,
                                     ``mediaType: application/vnd.neuronsphere.lock.v1+toml``
    ``layers[]``                     one per pinned RepoClass **and** one for the stack
                                     itself, ``mediaType:
                                     application/vnd.neuronsphere.repoclass.build.v1+zip``
    ================================ ======================================================

    Each layer is annotated ``io.neuronsphere.repoclass.name``,
    ``io.neuronsphere.repoclass.version``, ``io.neuronsphere.repoclass.item_type``
    (``build``) and ``org.opencontainers.image.title`` with the zip's librarian
    file name (``<class>_<version>_<type>.zip``), so that a registry UI shows
    something legible and a reader with ``curl`` can see what is inside
    without ``nsctl``. The manifest is annotated ``io.neuronsphere.stack.name``,
    ``io.neuronsphere.stack.version``, ``org.opencontainers.image.version``,
    ``org.opencontainers.image.source`` (the repository URL when known) and
    ``org.opencontainers.image.licenses`` (``Apache-2.0``: a stack's descriptor
    files are Apache, per :doc:`/licensing`; the zips inside carry their own
    -- superseded by the amendment above: the value is the subject's
    declaration).

    The tag is the stack's version. Lock entries and layers must correspond
    one to one (plus the subject's layer); an artifact with a layer no entry
    names, or an entry no layer carries, is refused at install rather than
    half-applied, for the same reason ``lock.Parse`` refuses an unknown
    schema.

.. spec:: nsctl stack add
    :id: HMD_CLI_NEURONSPHERE_NERD017_SPEC003
    :links: HMD_CLI_NEURONSPHERE_NERD017
    :status: implemented

    .. code-block:: text

        nsctl stack add <ref> [--env <name>] [--profile p,q | --all-profiles | --lean]
                              [--name <role-or-class>=<instance>]... [--apply]

    ``<ref>`` is a ``NERD016`` reference, or a bare name, which is expanded
    against the canonical namespace and the expansion printed. A missing
    version means the newest (``NERD016`` SPEC004); a range is resolved the
    same way and the result printed.

    The verb, in order:

    1. Resolve a credential (``NERD016`` SPEC006); anonymous is the expected
       case and is not a warning.
    2. Fetch and verify the manifest and every layer.
    3. For each layer, ``artifact.Invalidate`` then ``artifact.Store`` into
       ``$HMD_HOME/.cache/neuronsphere/artifacts/<class>@<version>/``. The
       subject's tree additionally receives the lock at its root, so that the
       cached stack tree is indistinguishable from a checkout to everything
       downstream.
    4. Best-effort ``Put`` of each zip into the local Artifact Librarian.
       This keeps ``nsctl artifact unpack`` and in-container
       ``pre_build_artifacts`` (``NERD005`` SPEC006) working for those
       classes, but the **cache is authoritative**: resolution never reads
       the librarian, so a control plane that is down makes this a warning
       naming the remedy, never a failure.
    5. Plan through the ``--from-repo`` rules (``NERD010`` SPEC004-SPEC006)
       with the subject declared ``source: {type: artifact}`` at the stack's
       version, and every companion likewise. ``--profile``,
       ``--all-profiles``, ``--lean`` and ``--name`` mean exactly what they
       mean on ``env add --from-repo``.
    6. Record a stack record in the environment manifest (SPEC009), save, and
       print the declared instances and ``Run: nsctl env apply <env>``.

    **Declaring is not deploying.** ``stack add`` mutates the manifest and
    the cache and nothing running. ``--apply`` runs ``env apply`` afterwards
    for the user who wants one command; it is a convenience, not a change of
    rule. Adding a stack already recorded at the same version is idempotent;
    at a different version it replaces the record and re-plans, and the
    previous version's cache directories are left for ``env apply`` to stop
    using.

.. spec:: nsctl stack list and stack remove
    :id: HMD_CLI_NEURONSPHERE_NERD017_SPEC004
    :links: HMD_CLI_NEURONSPHERE_NERD017
    :status: implemented

    ``stack list [--env]`` prints each stack record: name, version, reference,
    manifest digest, and the instances it bound. It reads the manifest and
    nothing else.

    ``stack remove <name> [--env] [--prune-cache]`` undeclares the instances
    the stack record bound -- and only those; an instance bound to a
    substrate reserved name or declared by another stack or by hand is not
    the stack's to remove -- deletes the record, and saves. It tears nothing
    down: the next ``env apply`` reconciles, as with any undeclaration. The
    cache is kept, because it is cache (``NERD005`` SPEC004); ``--prune-cache``
    deletes the stack's own directories when the user wants the disk back.

.. spec:: nsctl stack versions and stack pull
    :id: HMD_CLI_NEURONSPHERE_NERD017_SPEC005
    :links: HMD_CLI_NEURONSPHERE_NERD017
    :status: implemented

    ``stack versions <ref> [--spec "~= 0.1"] [--offline]`` lists the
    published versions (``NERD016`` SPEC004) and, with ``--spec``, the one
    that would be chosen. Results are cached under
    ``$HMD_HOME/.cache/neuronsphere/versions/`` in ``internal/versions``'s
    record with a key of the form ``stack~<host>~<path>.json``; ``--offline``
    reads only the cache, as ``artifact versions --offline`` does.

    ``stack pull <ref>`` performs steps 1-4 of SPEC003 and stops: it fills the
    cache without declaring anything, for a user preparing a machine that
    will later be offline, or a CI job warming a cache.

.. spec:: nsctl stack push
    :id: HMD_CLI_NEURONSPHERE_NERD017_SPEC006
    :links: HMD_CLI_NEURONSPHERE_NERD017
    :status: implemented

    .. code-block:: text

        nsctl stack push [<repo-dir>] <ref> [--artifacts <dir>] [--token <t>]
                         [--profile <tenant>] [--update-lock]

    From a repository with a ``local`` section and a lock, build the artifact
    of SPEC002 and push it (``NERD016`` SPEC005). The subject's zip is
    ``artifact.Zip(repoDir)`` -- deterministic, honours ``SkipDirs`` and the
    manifest's declared ``license.exclude`` (SPEC011), and always contains
    ``meta-data/`` and the lock. Each companion's zip comes
    from ``--artifacts <dir>`` (the ``hmd build`` output layout, for a
    publisher who built them) or else from the cloud Artifact Librarian by
    the lock's ``content_path``: **the publisher is a paid user**, and that is
    the asymmetry this feature exists to create. The consumer is not.

    Every zip's digest is written into the lock embedded in the artifact
    (SPEC007). ``--update-lock`` writes those digests back into the
    repository's own lock so the next commit carries them.

    The tag is the repository's version (``meta-data/VERSION``, with the
    build number the way ``hmd build`` computes it) unless ``<ref>`` names
    one. Push requires a credential and says so before any request.

.. spec:: The lock gains a digest
    :id: HMD_CLI_NEURONSPHERE_NERD017_SPEC007
    :links: HMD_CLI_NEURONSPHERE_NERD017
    :status: implemented

    Each ``[[resolved]]`` entry may carry ``digest = "sha256:<hex>"``, the
    digest of the build zip the ``content_path`` names. The schema version
    stays ``1``: ``lock.Parse`` is a non-strict decode, so an older ``nsctl``
    ignores the key, and the key is ``omitempty`` so an older lock is
    byte-identical on rewrite when nothing is known.

    ``nsctl lock`` fills it when the class is already cached (the cache keeps
    a digest sidecar beside each unpacked tree from this NERD on);
    ``artifact pull``, ``stack pull`` and ``stack add`` fill it when they have
    just seen the bytes. ``stack add`` **refuses** a layer whose digest
    disagrees with its lock entry's, because the lock is the publisher's
    statement of what they tested. ``nsctl lock --check`` does not verify
    digests: it runs on an aeroplane.

.. spec:: Coexistence with the existing resolution tiers
    :id: HMD_CLI_NEURONSPHERE_NERD017_SPEC008
    :links: HMD_CLI_NEURONSPHERE_NERD017
    :status: implemented

    A stack changes nothing about how a class resolves. A working tree under
    ``HMD_REPO_HOME`` still pre-empts a cached artifact and is still reported
    (``repoclass.PreemptedArtifact``); ``env apply`` is still offline; two
    stacks, or a stack and a ``--from-repo`` subject, in one environment each
    keep their own bindings and refuse an instance-name collision with the
    ``--name`` remedy.

    **Amended 2026-09-21.** A second transport -- a tenant's deployed
    Artifact Librarian, for private stacks -- is proposed in ``NERD020``;
    it changes nothing here.

    **Images are the v1 limitation.** A companion's image resolves through
    ``ImageCandidates`` (``internal/controlplane/images.go``): the local
    registries, then ``ghcr.io/hmdlabs``. A stack published by the platform
    therefore works with nothing set. A third-party stack whose images live
    elsewhere needs ``HMD_LOCAL_IMAGE_PULL_REGISTRIES`` to name their prefix.
    This NERD proposes, and does not build, a manifest annotation
    ``io.neuronsphere.stack.image_registries`` that ``stack add`` would
    surface as a line the user must add to ``hmd.env`` -- surfaced, not
    written, for the ``NERD004`` handback reason: an artifact must not edit a
    user's environment file silently.

.. spec:: The stack record in the environment manifest
    :id: HMD_CLI_NEURONSPHERE_NERD017_SPEC009
    :links: HMD_CLI_NEURONSPHERE_NERD017
    :status: implemented

    .. code-block:: yaml

        stacks:
          - name: observability
            version: 0.1.0
            ref: oci://ghcr.io/neuronsphere/stacks/observability
            digest: sha256:...          # the manifest digest that was installed
            bindings:                   # role or class -> instance, as NERD010 SPEC005
              compute: local-neuronsphere
              otel-collector: otel
            declared: [otel]            # what the stack itself declared (SPEC010);
                                        # a bound instance is not in it, and
                                        # neither is the stack unless SPEC001
                                        # made it an instance

    ``stacks`` is optional and the manifest schema version is unchanged; a
    manifest without it is every manifest that exists today. ``Validate``
    checks that every bound instance is declared. The record exists so that
    ``stack remove`` knows what is the stack's, ``stack list`` has something
    to read, and a second ``stack add`` can tell "same again" from "upgrade".

.. spec:: Composition: reuse if present, provide if absent
    :id: HMD_CLI_NEURONSPHERE_NERD017_SPEC010
    :links: HMD_CLI_NEURONSPHERE_NERD017
    :status: implemented

    **Amended 2026-09-21.** A stack is a slice of an environment, never a
    whole one, and slices overlap: observability's S3 sink and the
    warehouse's bucket are one bucket, and airflow needs "a trino", not its
    own. So ``stack add`` resolves every role a stack names -- its
    ``local.dependencies`` and each companion's ``dependencies`` -- against
    the environment **before** declaring anything:

    1. An instance the environment already declares that **produces** the
       role's resource (from its class manifest's ``deploy.resources`` and
       ``meta-data/resources``, read from the cache; offline) fills it: the
       role is **bound**, nothing is declared.
    2. Else a companion the stack's lock pins for that role is **declared**,
       and fills it.
    3. Else the role is **unsatisfied**: the verb refuses, naming the resource
       and, when the stack's manifest carries a ``suggest`` for the role, the
       stack to add first (``nsctl stack add warehouse``) or the ``--name
       <role>=<instance>`` that binds an existing instance. Nothing is added
       on the user's behalf.

    The same rule applies to a companion itself: a companion whose class
    already runs in the environment under the instance name the stack would
    give it is bound rather than declared twice, and ``stack list`` shows it
    as *shared*. ``stack remove`` already leaves an instance another record
    binds; this is the other half of that promise.

    Rule 1 is deliberately by *resource*, not by class name, because that is
    how ms-deployment resolves the dependency once declared (``NERD0004``),
    and a stack that bound by class would be wired differently from how it
    deploys.

.. spec:: Declared licence
    :id: HMD_CLI_NEURONSPHERE_NERD017_SPEC011
    :links: HMD_CLI_NEURONSPHERE_NERD017
    :status: implemented

    Anyone may declare and build a stack under whatever licence they
    choose, and ``nsctl`` records the declaration and enforces nothing. The
    declaration is a top-level ``license`` member of the BACON manifest
    (``hmd-docs-bacon``, *License* section):

    .. code-block:: json

        "license": "MIT"

        "license": {
          "spdx": "Apache-2.0",
          "exclude": ["src/python/", "src/docker/", "src/typescript/"]
        }

    ``spdx`` is the SPDX expression of *what nsctl publishes from the tree*
    -- the value every published layer is annotated with. ``exclude`` lists
    root-relative paths that stay out of every zip ``nsctl`` makes from the
    tree, matched on whole path segments (``src/python`` covers
    ``src/python/app.py`` and not ``src/pythonic/`` or
    ``src/local/scripts/python/``); entries must be relative and inside the
    tree. The string form is shorthand for ``{"spdx": ...}``. Absent means
    the tree travels whole and is published unannotated.

    **Where it applies.** ``artifact.Zip`` reads the tree's own manifest and
    leaves the excluded paths out, so every verb that zips a tree honours
    the declaration with no call-site knowledge: the stack subject and the
    cache re-zip (``stack build``, ``stack push``), ``stack init
    --bundle-local``, ``artifact push <dir>`` and ``artifact register
    <dir>``. A zip that arrives as bytes -- a release directory, the kept
    cache zip, an OCI source, the librarian -- is the author's bytes: it is
    published whole and annotated from the manifest *inside it*.

    **What is surfaced.** ``stack build`` and ``stack push`` print one
    ``Licences:`` line naming every layer's declaration (or
    ``(undeclared)``); ``artifact push`` prints ``Licence:``; ``stack add``
    shows each layer's beside its digest; ``repoclass describe`` shows the
    manifest's. ``nsctl repoclass license set <spdx> [--exclude <path>]...``
    and ``license clear`` author the member through the ordered store, and
    ``repoclass validate`` checks its shape -- only its shape: an exclude
    that covers a deploy tool's source directory is deliberate in the repos
    that do it, whose deploy re-tags an image it never builds
    (:doc:`/licensing`).

    **What is not done.** No licence is inferred from a class name or a
    ``LICENSE`` file, no push is refused on licence grounds, and there is no
    override flag because there is nothing to override. HMD's own path
    split (descriptor paths Apache 2.0, ``src/python``, ``src/docker`` and
    ``src/typescript`` BUSL 1.1 in three repositories) is a declaration in
    *those* repositories' manifests, not a rule in ``nsctl``: the same
    mechanism a third party uses to publish an MIT service, or to keep a
    proprietary directory out of a public stack. That a local deploy needs
    none of the excluded paths is a fact about ``hmd deploy --local`` --
    the runner mounts the tree and the tools read ``src/cdktf``,
    ``src/helm``, ``src/opa-bundles`` and ``src/local``; the docker tool
    re-tags a pulled image -- proven by the classes ``tools/repopack``
    bundles without them.

Testing
-------

Unit tests build stack artifacts in memory and serve them from the ``NERD016``
fake registry. They prove: ``Read`` refuses a missing layer, an extra layer and
a lock-digest mismatch; ``Build`` is deterministic; ``Install`` into a
temporary ``HMD_HOME`` leaves ``artifact.Cached`` true for every class and the
lock at the subject's root; ``stack add`` in a fresh home with no credential
writes a manifest whose subject is ``source.type: artifact``, whose
``stacks[0]`` record and bindings are as SPEC009, and whose output names
``env apply``; a local-librarian ``Put`` against a closed port is a warning and
exit 0; a second ``add`` is idempotent; ``remove`` undeclares only the stack's
instances and keeps the cache; ``versions`` sorts and filters; and
``push`` from a fixture repository followed by ``add`` from the same fake
round-trips byte for byte. ``test/nsctl_cli.robot`` gains the no-Docker
contract cases: ``stack add`` without ``HMD_HOME`` names both ways to supply
it, a ``github.com`` reference is refused naming ``NERD016``, and
``stack versions`` against a closed port fails cleanly with the host in the
message. For SPEC011: ``artifact.Zip`` drops a declared exclude and keeps a
same-named directory elsewhere; ``Build`` annotates each layer and the
manifest from the declarations inside the zips and nothing else; ``Retag``
keeps the annotation; ``artifact push`` of a zip file publishes it whole;
and a robot case authors a declaration and validates it.

The acceptance run publishes a real stack (the observability pair,
``hmd-inf-otel-collector`` and ``hmd-inf-clickhouse``, under a small stack
repository) to the canonical namespace, flips the package public, and in a
scratch ``HMD_HOME`` with no ``nsctl.toml``, no ``tokens.yaml`` and no
librarian variables runs ``env start``, ``stack versions``, ``stack add``,
``env apply`` and ``env status``, then ``stack remove``.

Alternatives considered
-----------------------

**A tarball of zips over HTTPS.** No digests unless we invent a checksum
format, no version enumeration, no private-namespace story, and a second
hosting decision for something the images already settled.

**A Helm-style chart bundle.** The unit is wrong. A stack is RepoClasses,
which are what ``ms-deployment`` plans, ``hmd deploy`` deploys and ``env
status`` reports; a chart is one of the things a RepoClass may contain.

**Librarian only.** It is the paid path and stays the paid path for a
tenant's private artifacts. The point of this NERD is the user who has no
tenant.

**A catalogue in ``nsctl``.** Rejected in ``NERD004`` and rejected here for the
same reason: a list of known stacks inside the binary is a second BOM seeder
with a release cadence, and a stack a user cannot install without an ``nsctl``
release is a stack that does not exist yet.
