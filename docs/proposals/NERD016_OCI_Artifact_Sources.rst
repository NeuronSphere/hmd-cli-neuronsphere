.. NERD016 OCI Artifact Sources

NERD016 OCI Artifact Sources
============================

.. req:: Fetch and publish versioned artifacts through any OCI registry
    :id: HMD_CLI_NEURONSPHERE_NERD016
    :status: proposed

    ``nsctl`` shall be able to **fetch a versioned artifact from, and publish
    one to, any registry that speaks the OCI Distribution API** -- anonymously
    when the namespace is public, and with a bearer credential when it is not
    -- verifying every byte it receives against a content digest, and never
    contacting a registry except from a command whose name says it fetches.

    This is a mechanism, not a feature. It has no verbs of its own. Its
    consumers are ``NERD017`` (stacks: a set of RepoClass build artifacts
    installable into a local environment) and ``NERD018`` (CLI plugins:
    executables that add a noun to ``nsctl``). It is written separately so
    that a third consumer can appear without either of those documents
    changing, and so that the trust and credential rules are stated once.

Motivation
----------

Today a RepoClass that is not a checkout reaches a local environment one way:
``nsctl artifact pull`` against the **cloud Artifact Librarian**
(``internal/librarian``), which needs a tenant credential, and which
:doc:`/licensing` says is a paid feature. A free user has no download path at
all for a third-party RepoClass. They can clone a repository, or they can be
handed a zip and run ``nsctl artifact register``. Neither is "install".

The gap is narrow, and it is only a *source* gap. ``source: {type: artifact}``
resolution (``internal/repoclass/repoclass.go``, ``artifactDir``) reads
nothing but the unpacked cache at
``$HMD_HOME/.cache/neuronsphere/artifacts/<class>@<version>/``, and the
deploy runner bind-mounts that tree with ``--local``
(``internal/runner/runner.go``, ``Localize``). Put bytes in the cache and
everything above it -- ``env add --from-repo``, ``env apply``, the lock, the
plan -- already works, offline, with no credential. What is missing is a way
to get bytes into the cache that does not require a paid tenant.

There is a second gap of the same shape on the other side of the product.
Every NeuronSphere container image already lives at ``ghcr.io/hmdlabs``
(``repoclass.PublishedRegistry``), which is public, anonymous, and the
registry a local environment must be able to reach anyway to run anything. An
OCI registry stores arbitrary artifacts, not only images, under the same
protocol, the same tags, the same digests and the same anonymous token flow.
Using it for the build zips means the *one* host a stack's images must be
pullable from is also the host its zips are pullable from, and that "is this
stack usable for free" is one visibility decision on one package, not two.

**Free and paid on one protocol.** The licensing posture is that ``nsctl``
never checks anything: no keys, no phone-home, no gates. What separates paid
from free is *access to a store*, and only that. An OCI registry has exactly
that property built in. A public namespace answers anonymous pulls; a private
one answers a bearer token; the client code path is identical. That is the
whole monetisation model for distribution and this document is careful not to
add anything to it: a curated or private namespace that needs a login is a
paid product, a public one is a free one, and ``nsctl`` cannot tell the
difference and must not try.

Scope and terminology
---------------------

* An **artifact** here is an OCI image manifest whose config blob and layers
  are arbitrary bytes with declared media types -- the "OCI artifact" usage
  of the 1.1 specification -- not a runnable image. ``nsctl`` never pulls an
  image through this package; images stay with Docker.
* A **source reference** (or **ref**) names a registry host, a repository
  path within it, and optionally a tag or digest. Its grammar is SPEC001.
* A **credential** is what the client presents to a registry: nothing, a
  static bearer token, or a username and secret exchanged for a token. Where
  one comes from is SPEC006 and nowhere else.
* A **consumer** is a package that gives the fetched bytes meaning (a stack, a
  plugin). This package hands back verified blobs and their annotations, and
  never interprets them.

Out of scope: signing and attestation (``nsctl`` itself ships unsigned with a
sha256 ``checksums.txt``; the same digest-only posture applies here and is
recorded as a risk, not solved), registry mirroring, the Docker credential
helper protocol, and any registry-side listing beyond ``tags/list``.

.. spec:: Source reference grammar
    :id: HMD_CLI_NEURONSPHERE_NERD016_SPEC001
    :links: HMD_CLI_NEURONSPHERE_NERD016
    :status: proposed

    A source reference is:

    .. code-block:: text

        ref      := [ "oci://" ] host "/" repository [ version ]
        version  := ":" tag | "@" tag | "@" "sha256:" hex64
        host     := the first path segment, which must contain "." or ":"
                    or be exactly "localhost"

    The ``oci://`` scheme is optional and is the only scheme accepted. The
    ``@<tag>`` form is accepted alongside the OCI-conventional ``:<tag>``
    because every reference a user of ``nsctl`` has typed so far has been
    ``<class>@<version>``, and the two are normalised to the same thing. A
    ``@sha256:`` suffix pins a manifest digest; a fetch by digest never
    consults ``tags/list``.

    **This package never guesses a host.** A reference without one is refused
    with ``ErrNoHost``. A consuming *verb* may expand a bare name against a
    namespace compiled into that verb -- ``NERD017`` expands ``analytics`` to
    ``ghcr.io/hmdlabs/stacks/analytics`` -- and when it does, it prints the
    expansion before fetching. The argument is the one ``nsconfig.DefaultAuthURL``
    makes: a default that is a *build identity* is fine, and one that is an
    inference from the environment is not.

    A reference whose host is ``github.com`` is recognised and refused with a
    message naming SPEC008: it is the shape a GitHub Releases source would
    have, and the refusal is so that a user who tries it learns the plan rather
    than a parse error.

    Repository paths of three or more segments (``hmdlabs/stacks/analytics``)
    are legal in the Distribution specification and on ``ghcr.io``; Docker Hub
    accepts two. The canonical namespaces in ``NERD017`` and ``NERD018`` are
    three-level, and a consumer wanting Docker Hub must choose flatter names.

.. spec:: Anonymous and authenticated pull are one code path
    :id: HMD_CLI_NEURONSPHERE_NERD016_SPEC002
    :links: HMD_CLI_NEURONSPHERE_NERD016
    :status: proposed

    Every request is first made with whatever the resolved credential is --
    which, for a free user, is nothing. On ``401`` the client parses the
    ``WWW-Authenticate`` challenge. For a ``Bearer`` challenge it requests a
    token from the ``realm`` with the ``service`` and ``scope`` parameters the
    registry supplied, presenting HTTP Basic when a username and secret were
    resolved and nothing otherwise, and retries the original request once with
    the returned ``token`` (or ``access_token``). For a ``Basic`` challenge it
    retries with Basic directly. A static bearer credential is sent on the
    first request without waiting for a challenge, and is also offered as the
    Basic password on a challenge, because that is what ``ghcr.io`` expects of
    a personal access token.

    Tokens are cached per ``(host, scope)`` for the life of the process and
    are never written to disk. A token is never sent to a host other than the
    one it was obtained from or resolved for.

    This is deliberately the anonymous token flow ``ghcr.io`` runs for public
    packages: an unauthenticated ``GET realm?scope=repository:<name>:pull``
    returns a token for a public package. For a private one it also returns a
    token, and the retried request then fails. SPEC003 says what the message
    must say about that.

.. spec:: Retrieval verifies every byte before a consumer sees it
    :id: HMD_CLI_NEURONSPHERE_NERD016_SPEC003
    :links: HMD_CLI_NEURONSPHERE_NERD016
    :status: proposed

    A manifest is requested with ``Accept`` naming the OCI image manifest and
    OCI image index media types. An index is refused with a message saying a
    consumer artifact is a single manifest; the client does not select a
    platform from an index, because neither consumer publishes one (a plugin
    carries its platforms as layers of one manifest, SPEC002 of ``NERD018``).
    The manifest's digest is computed over the bytes received and compared to
    ``Docker-Content-Digest`` when present and to the requested digest when
    the fetch was by digest.

    Every blob -- config and each layer -- is streamed through a digest and a
    byte count and is handed to the consumer only after both match the
    descriptor. A mismatch is a runtime failure naming the descriptor's digest
    and the computed one. Nothing partially verified is ever returned or
    written; a consumer that wants to write to disk receives a reader that
    errors at EOF on mismatch, and it is the consumer's job to stage and
    rename, as ``artifact.Store`` already does.

    A ``404`` on a manifest is reported with the reference, and -- because
    ``ghcr.io`` packages are private by default and a private package looks
    identical to a missing one from outside -- the message says so: the
    package may not exist, or may be private and need a credential
    (SPEC006). A ``401`` after a credential was presented names the
    credential's *source* (flag, variable, profile) so the user knows which
    one was rejected, and never prints the credential.

.. spec:: Version enumeration is the registry's tag list
    :id: HMD_CLI_NEURONSPHERE_NERD016_SPEC004
    :links: HMD_CLI_NEURONSPHERE_NERD016
    :status: proposed

    ``GET /v2/<name>/tags/list`` is followed through ``Link`` pagination until
    exhausted. Tags that are not versions under ``versionspec.IsVersion`` are
    dropped -- ``latest``, ``stable``, a branch name -- and the remainder is
    ordered with ``versionspec.Sort``. A consumer that takes a
    ``version_spec`` selects with ``versionspec.Spec.Highest`` over that list,
    exactly as ``NERD011`` does over the librarian's, and the chosen version is
    always printed, because a user who typed a range must be able to see what
    it resolved to.

    A reference with no version means "the newest tag that is a version".
    ``latest`` is never pulled: it is a tag a publisher moves, and everything
    downstream (the lock, the cache directory name, the environment manifest)
    is keyed by a version that does not move.

    Enumeration results may be cached by a consumer under
    ``$HMD_HOME/.cache/neuronsphere/versions/`` using ``internal/versions``'s
    ``Published`` record, and the rules there apply: never refreshed
    implicitly, never on the deploy path.

.. spec:: Push
    :id: HMD_CLI_NEURONSPHERE_NERD016_SPEC005
    :links: HMD_CLI_NEURONSPHERE_NERD016
    :status: proposed

    Publishing is the reverse of SPEC003 and is kept in ``nsctl`` so that a
    publisher's CI needs no second tool. For each blob: ``HEAD`` the digest and
    skip it if present; else ``POST /v2/<name>/blobs/uploads/`` and complete
    with one monolithic ``PUT <location>?digest=``. Then ``PUT
    /v2/<name>/manifests/<tag>`` with the manifest, which sets **both** the
    top-level ``artifactType`` and ``config.mediaType``. The digest the
    registry answers with is compared to the one computed locally, and a
    mismatch is a failure.

    Some registries predate the 1.1 ``artifactType`` field and reject a
    manifest carrying it. On a ``400`` naming the manifest, the client retries
    once without the top-level field; the config media type still identifies
    the artifact.

    Push never runs anonymously. Absent a credential it refuses with the three
    ways to supply one (SPEC006), before any network request.

.. spec:: Credential resolution, and the never-guess rule
    :id: HMD_CLI_NEURONSPHERE_NERD016_SPEC006
    :links: HMD_CLI_NEURONSPHERE_NERD016
    :status: proposed

    A credential for host ``H`` is resolved in this order, and the first hit
    wins; its origin is kept as ``Source`` and printed on the fetch line the
    way ``nsconfig.Endpoint`` reports ``Shadowed``:

    1. ``--token`` on the verb (push verbs only; pull verbs do not take one,
       because a pull that needs a flag to work is a pull the user should set
       up once in a profile).
    2. ``HMD_REGISTRY_TOKEN`` from the layered environment (process over
       ``hmd.env``), with ``HMD_REGISTRY_USER`` as the Basic username when the
       registry challenges (default ``nsctl``).
    3. The profile whose ``registry_url`` -- a new optional key in
       ``$HMD_HOME/.config/nsctl.toml`` -- has host ``H``, in which case the
       login access token in ``$HMD_HOME/.cache/tokens.yaml`` is the bearer.
       This is the paid path: ``nsctl login`` against the tenant, and the
       tenant's private namespace answers.
    4. Nothing. The request is anonymous.

    A credential resolved for ``H`` is presented to ``H`` and to the token
    realm ``H``'s challenge names, and to nothing else. The Docker credential
    store (``~/.docker/config.json``) is deliberately **not** consulted: it is
    another tool's file, it may hold credentials for hosts the user never
    meant ``nsctl`` to speak to, and reading it would be the kind of ambient
    inference the platform has removed everywhere else.

    Pull verbs never *require* a credential. Push verbs require one.

.. spec:: The offline guarantee is enforced by imports
    :id: HMD_CLI_NEURONSPHERE_NERD016_SPEC007
    :links: HMD_CLI_NEURONSPHERE_NERD016
    :status: proposed

    ``internal/oci`` is imported only by consumer packages and by the verbs
    that fetch. It is never imported by ``internal/repoclass``,
    ``internal/environment``, ``internal/artifact`` or ``internal/runner``,
    so that resolution and ``env apply`` cannot acquire a network dependency
    by accident. The rule is a test in the style of
    ``internal/versionspec/imports_test.go``: it fails the build if any of
    those packages transitively imports ``internal/oci``.

    Conversely ``internal/oci`` imports the standard library, the OCI
    specification types (``image-spec``, ``go-digest`` -- both already in the
    module graph through the Docker client), and ``internal/nsconfig`` and
    ``internal/tokenstore`` for SPEC006. It does not import ``oras-go``; see
    Alternatives.

.. spec:: Deferred: a GitHub Releases source
    :id: HMD_CLI_NEURONSPHERE_NERD016_SPEC008
    :links: HMD_CLI_NEURONSPHERE_NERD016
    :status: proposed

    ``nsctl`` itself ships as a GoReleaser tarball on a GitHub Release with a
    ``checksums.txt``, and a Go plugin author will reach for the same
    template. A source of the form ``github.com/<owner>/<repo>[@<tag>]`` would
    map a release to an artifact: assets to layers, ``checksums.txt`` to
    digests, and a ``manifest.json`` asset to the config blob.

    It is **not built** by this NERD. The fetch side of the client is an
    interface (``Fetcher``: ``Tags`` and ``Fetch``) so that this can be added
    behind it without a consumer changing, and ``github.com`` references are
    refused naming this section. It is deferred rather than rejected because
    it has no answer for a private namespace -- the GitHub API is
    rate-limited unauthenticated and its credential model is a different
    thing from the platform's login -- and because a publisher who can run
    GoReleaser can run ``nsctl plugin push`` in the same job.

Testing
-------

An in-process fake registry (``internal/oci/ocitest``, importable by consumer
tests) serves ``GET /v2/``, a token realm, ``tags/list`` with ``Link``
pagination, ``GET``/``HEAD`` manifests and blobs, and the upload and manifest
``PUT`` legs, with options to require a token, answer anonymously, page the
tag list, and reject ``artifactType``. Tests prove: the reference grammar
table including the ``github.com`` refusal; challenge parsing with quoted
commas; the anonymous, Basic-to-token and static-bearer flows; that a token
is never sent to a second host; tag filtering and ordering; manifest and blob
digest and size mismatches as typed failures; index refusal; the ``404``
private-package wording; and a push round trip with ``HEAD`` skipping and the
``artifactType`` retry. The imports test of SPEC007 runs with ``make check``.

Alternatives considered
-----------------------

**oras-go.** It is the reference client for exactly this. It also brings a
content-store abstraction, graph copying, referrers, retries and ``x/sync``
that nothing here uses, for a protocol that is seven HTTP calls. Every other
remote client in this binary -- librarian, ms-deployment, device
authorisation -- is a thin ``net/http`` client with an ``httptest`` fake and a
typed error, and a reviewer already knows that shape. The cost of writing the
challenge parser and the monolithic upload is a few hundred lines, and the
benefit is that SPEC007 can be an allowlist of four imports.

**Shell out to ``docker`` or ``oras``.** Docker is the only host prerequisite
and it has no artifact pull. ``oras`` would be a second prerequisite for the
one feature most aimed at a first-time user.

**A public, read-only Artifact Librarian.** The librarian is BUSL, is
tenant-scoped by construction, and authenticates every request. Standing one
up for anonymous access would be new infrastructure to run and to pay for, to
reproduce what ``ghcr.io`` does for free.

**A static index over HTTPS (S3, CloudFront).** Cheap, but a new piece of
infrastructure, a new integrity format, and no path to a private namespace
that is not a second system.
