.. NERD006 A Local Package Registry

NERD006 A Local Package Registry
================================

.. req:: Run a local package registry that hosts internal packages, caches upstream, and is the build-time index
    :id: HMD_CLI_NEURONSPHERE_NERD006
    :status: proposed

    The local NeuronSphere shall be extendable with a **package registry**
    that does three things:

    1. **Hosts internal packages**, giving ``hmd build`` a publish target on
       this machine, so a wheel built from a checkout can be installed by
       another repo without a cloud round trip.
    2. **Caches upstream**, including authenticated private upstreams such as
       the JFrog PyPI index and ``ghcr.io/hmdlabs``, so repeated builds are
       fast and work offline.
    3. **Is the build-time index**, resolved automatically wherever the
       platform installs packages -- the host shell, a deploy node, a Docker
       build -- rather than being something each of them must be told about.

    Python is the primary format and the only one enabled by default.
    Docker/OCI, Go and npm are specified here and gated off.

    It shall **not** be part of the default control plane, and shall be added
    from the user's own ``$HMD_HOME`` configuration -- as a ``NERD004``
    control-plane extension, distributed as a ``NERD005`` artifact.

    **No credential shall ever be written under** ``$HMD_HOME``. Upstream
    credentials live in the host keychain and nowhere else.

Motivation
----------

``hmd build`` with ``HMD_AUTO_PUBLISH=true`` pushes Python wheels to
``hmdlabs.jfrog.io/artifactory/api/pypi/hmd_pypi`` and Docker images to
``ghcr.io/hmdlabs``, and installs resolve from those same hosted indexes.
Every one of those round trips is over the internet, authenticated, and about
an artifact that in local development never leaves the machine.

The consequence is not slowness so much as a fork in the workflow. Because
there is nowhere local to publish a wheel, cross-repo iteration is done with
editable installs and bind mounts -- a second mechanism, with second failure
modes, that exists only locally. The cloud resolves a version from an index;
the laptop resolves a path from an environment variable. Closing that gap is
the same cloud-parity argument that produced Floci and the DAG runner, applied
to the last place the local platform still does something structurally
different from production.

Offline is the second motivation and the more concrete one. A rebuild with no
network fails today at the first ``pip install``.

Scope
-----

**In scope.**

- A RepoClass, ``hmd-inf-local-registry``, deployed as a ``NERD004``
  control-plane extension.
- Python (PyPI) hosting and caching, enabled by default.
- Docker/OCI, Go and npm, each behind its own switch, off by default.
- Authenticated private upstreams, with credentials confined to the host
  keychain.
- Wiring the resulting index URLs into the platform through the
  ``<TOOL>_REGISTRIES`` contract that already exists.

**Out of scope.**

- A containerd mirror for k3s. ``hmd-cli-helm`` imports images into the node
  directly, so pods do not pull through a registry today. Parity
  nice-to-have, not a blocker -- and stated so it is not assumed.
- Replacing JFrog or ghcr for CI. This is a local cache and a local host,
  not a hosted registry.
- Publishing *from* the local registry to anywhere else.
- Multi-user access. It is on loopback, for one developer.

The finding that shapes this document
-------------------------------------

**Most of requirement 3 already exists, and needs no new code.**

``NEP036`` defines a ``<TOOL>_REGISTRIES`` contract in ``hmd-core-arch-docs``,
specifically so that a site can point the toolchain at its own registries:

- ``PYTHON_REGISTRIES`` is JSON read by ``hmd python login``, which writes
  ``[[index]]`` entries into ``$HMD_HOME/.config/uv.toml`` and a
  ``[neuronsphere]`` server into ``~/.pypirc`` from whichever registry carries
  ``"publish": true``. Python publishing is ``twine upload -r neuronsphere``,
  which reads that stanza -- so retargeting the publish is a configuration
  change, not a code change.
- ``TYPESCRIPT_REGISTRIES`` does the same for ``~/.npmrc``.
- ``HMD_CONTAINER_REGISTRY``, ``HMD_LOCAL_NS_CONTAINER_REGISTRY`` and
  ``HMD_LOCAL_IMAGE_PULL_REGISTRIES`` do it for images.
- Docker builds already mount ``pipconfig``, ``uvconfig``, ``npmrc`` and
  ``pipcache`` as BuildKit secrets.

So the local registry is a **conforming consumer of an existing contract**,
not a new mechanism, and the implementation surface collapses to: run the
services, give them one URL, and write the right values into ``hmd.env``.

Two smaller findings carry through the SPECs:

- ``hmd-img-projectbuilder`` already sets
  ``UV_INDEX_STRATEGY=unsafe-best-match``, so ``uv`` searches every configured
  index rather than the first that answers. The usual trap -- ``uv``, unlike
  ``pip``, not falling through to PyPI on a 404 -- is already defused in the
  place it would have bitten hardest.
- **Go has nothing.** No ``GOPROXY``, no ``GOPRIVATE``, no
  ``<TOOL>_REGISTRIES`` analogue anywhere in the platform. Go is the one
  format whose wiring is genuinely new work, which is part of why it is off
  by default.

Architecture Overview
---------------------

::

    host shell    -.
    deploy node   -+--> registry.local.neuronsphere.io --> hmd_proxy
    k3s pod       -'                                          |
                                                              |
                             /pypi/ --> devpi      <-----------+
                             /go/   --> athens     <-----------+
                             /npm/  --> verdaccio  <-----------+
                             /v2/   --> zot        <-----------'

    hmd_proxy also serves the credential-injecting upstream locations,
    whose config lives on a tmpfs and never on disk:

        /upstream/hmd/  --> hmdlabs.jfrog.io       (Authorization added here)
        pypi.org, ghcr.io, npm.pkg.github.com      (via each service directly)

Component choice
----------------

Three candidates were considered as a single service covering all four
formats, and all three were rejected:

- **Gitea / Forgejo.** One Go binary, hosts PyPI, npm, Go modules and OCI
  images among twenty-odd formats. It **cannot proxy or cache an upstream** --
  the feature request has been open since 2022 -- which fails requirement 2
  outright. Attractive enough that it is worth recording why it does not work,
  because it is the first thing anyone will suggest.
- **Sonatype Nexus Repository OSS.** Genuinely does all four, hosted and proxy
  and group, in one container. It is a JVM wanting on the order of 2 GB of
  RAM, which fails "lightweight" for something meant to run alongside k3s,
  Floci and Postgres on a laptop. Retained as the documented escape hatch for
  anyone who would rather run one heavy thing than four small ones.
- **Pulp.** Plugin-based and fully open source, but deploys as Postgres plus
  Redis plus workers. Not lightweight.

The chosen shape is one small service per format, each of which does hosting
*and* upstream proxying in a single process, behind one vhost:

.. list-table::
   :header-rows: 1
   :widths: 12 18 44 26

   * - Format
     - Service
     - Why this one
     - Image
   * - PyPI
     - ``devpi-server``
     - Index inheritance: a caching mirror ``root/pypi`` inherited by a hosted
       ``hmd/local``, so **one** index URL serves internal and cached-upstream
       packages alike
     - none official; needs ``hmd-img-devpi``
   * - OCI
     - ``zot``
     - A single Go binary that hosts *and* pull-through-caches **several**
       upstreams at once, which ``registry:2`` cannot -- it is either a
       registry or a mirror, never both
     - ``ghcr.io/project-zot/zot-*``
   * - Go
     - ``Athens``
     - Hosts private modules and proxies upstream in one process; removes the
       need for ``GOPRIVATE``
     - ``gomods/athens``
   * - npm
     - ``Verdaccio``
     - Zero-config private registry that proxies and caches npmjs
     - ``verdaccio/verdaccio``

Only devpi needs a wrapper image, which per house convention lives in its own
``hmd-img-*`` repo rather than being bundled into a CLI repo.

The alternative considered for Python was **pypiserver plus proxpi** -- two
even smaller processes, one hosting and one caching. It was not chosen because
it produces two index URLs where devpi produces one, and every additional index
URL is another thing to configure in ``uv.toml``, ``pip.conf``, a Dockerfile
and a deploy node. One service that does both is worth a wrapper image.

.. spec:: The RepoClass
    :id: HMD_CLI_NEURONSPHERE_NERD006_SPEC001
    :links: HMD_CLI_NEURONSPHERE_NERD006
    :status: implemented

    ``hmd-inf-local-registry`` is an ordinary BACON RepoClass. It is not
    bundled into ``nsctl``, and ``nsctl`` contains no reference to it -- that
    is the test of whether ``NERD004`` produced a real extension surface, and
    it passed: nothing in ``nsctl`` changed to run this.

    It is declared in ``$HMD_HOME/.config/control-plane.yaml``, written by
    ``nsctl control-plane repo add`` rather than by hand:

    .. code-block:: yaml

        version: 1
        name: control-plane
        repos:
          - instance_name: registry
            repo_class_name: hmd-inf-local-registry
            source:
              type: local
              path: /path/to/hmd-inf-local-registry
            instance_configuration:
              url: http://registry.local.neuronsphere.io
              upstream: server:3141
              pypi: {enabled: true}

    Two departures from what was drafted above, both forced and both small:

    - **The instance is named** ``registry``, **not** ``package-registry``.
      ``NERD004`` SPEC008 puts an extension's durable state at
      ``$HMD_HOME/<instance-name>/``, so the instance name *is* SPEC007's
      directory. Naming it ``registry`` makes ``$HMD_HOME/registry/`` fall out
      rather than having to be arranged.
    - **The source is** ``local``, **not** ``artifact``. ``NERD005`` is
      unimplemented -- ``manifest.validateSource`` rejects
      ``type: artifact`` outright -- so distribution is a working tree until it
      lands. This is the one place NERD006 is waiting on another document
      rather than on itself.

    ``url`` and ``upstream`` are ``NERD004``'s keys, not this document's:
    together they are what produces the vhost, the proxy alias and the status
    line. ``upstream`` names the *unprefixed* compose service key.

    One component per enabled format, gated on a compose profile, each a
    container on the platform network, each with its own data directory under
    SPEC007's root. A format that is not enabled starts nothing and consumes
    nothing -- ``pypi: {enabled: true}`` activates the profile ``registry-pypi``
    and nothing else, so the three unwritten services cost a reader three lines
    and cost a running platform nothing.

    It declares what it produces in ``meta-data/resources/*.yaml`` per
    NERD0004 -- ``registry.neuronsphere.io/python-package-index`` -- so that a
    RepoClass which needs a package index could one day depend on one by
    resource rather than by name.

    **The base type is not there, deliberately.** This document said the base
    type would be produced by the RepoClass that produces the rest of the local
    core, per the local-core ownership convention. ``hmd-cli-neuronsphere``
    carries no ``meta-data/resources`` tree at all, so there is nothing to
    inherit from; creating the convention's first base type is a larger change
    than this one and belongs to whoever needs it. The definition is declared
    parentless and re-parented later. Nothing consumes it either way:
    ``NERD004`` leaves whether a control-plane extension submits its Resources
    at all as an open question, since an extension runs as compose and never
    reaches ``hmd-ms-deployment``.

    **Enabling a format must not require a purge.** Turning on ``oci`` after
    the fact is a manifest edit and an apply; the reconcile delta brings up
    one more container and leaves the running ones alone. Untested -- there is
    no second format yet.

.. spec:: One URL
    :id: HMD_CLI_NEURONSPHERE_NERD006_SPEC002
    :links: HMD_CLI_NEURONSPHERE_NERD006
    :status: implemented

    The registry is reached at::

        http://registry.local.neuronsphere.io

    resolved three ways per ``NERD004`` SPEC007 -- an nginx ``server_name``
    block for the host, a Docker network alias for Floci Lambdas and sibling
    containers, and a ``coredns-custom`` record for pods. Under the same
    ``local.neuronsphere.io`` suffix the identity provider and the environment
    UIs already use, so the host-side resolution people have covers this too.
    Set as ``instance_configuration.url``, from which the vhost, the alias and
    the record are all derived. Two of the three are verified working; the
    CoreDNS leg is ``NERD004``'s deferred one and remains untested here.

    That it must be one string is not a preference. An index URL is written
    into ``uv.toml``, ``~/.pypirc``, ``~/.npmrc``, ``GOPROXY``, image tags and
    BuildKit secrets, at different times, by different tools. A registry
    reachable as ``localhost:5000`` from the shell and ``hmd_proxy`` from a
    pod would need every one of those to be format-and-location specific.

    **Per-format path prefixes were specified here and are not what was
    built.** This document originally gave one host with a location per
    format::

        /pypi/hmd/local/+simple/   devpi
        /go/                       athens
        /npm/                      verdaccio
        /v2/                       zot

    ``NERD004``'s router has no such shape. An extension declares exactly one
    ``url`` and one ``upstream``, and ``router.namedVhostServer`` emits a whole
    ``server`` block whose single ``location /`` proxies to it. Path prefixes
    would mean extending ``cpext.Extension`` to carry a route list and
    ``WriteExtensionVhosts`` to emit several locations -- changes to ``nsctl``,
    made on behalf of one extension, which is the coupling ``NERD004`` was
    shaped to avoid.

    So **each format gets a hostname, not a path**. Python serves the vhost
    root::

        http://registry.local.neuronsphere.io/hmd/local/+simple/   index
        http://registry.local.neuronsphere.io/hmd/local/           twine

    and later formats take sibling names -- ``oci.registry.local...``,
    ``go.registry.local...`` -- as separate instances of the same RepoClass, or
    as one instance once ``NERD004`` grows multi-route extensions on evidence
    from more than one caller.

    This is the better trade for a second reason, not only the cheaper one. A
    path prefix is exactly what forces ``devpi --outside-url`` to rewrite every
    absolute link it emits, and OCI cannot be prefixed at all -- the
    distribution spec fixes it at ``/v2/``. Serving each format at a root
    removes the whole class of problem rather than solving it once per format.

    Two host-side prerequisites, both one-time, both printed by
    ``control-plane start`` rather than left to be discovered:

    - An ``/etc/hosts`` entry, exactly as ``auth.local.neuronsphere.io``
      needs::

          127.0.0.1 registry.local.neuronsphere.io

    - For Docker only, an ``insecure-registries`` entry. The hostname contains
      a dot, so Docker treats it as a registry host rather than a Docker Hub
      namespace -- but it will not speak plain HTTP to it without being told.
      Nothing here warrants TLS, for the same reason the identity provider
      runs without it. Not needed until OCI is enabled.

.. spec:: The Python index
    :id: HMD_CLI_NEURONSPHERE_NERD006_SPEC003
    :links: HMD_CLI_NEURONSPHERE_NERD006
    :status: implemented

    devpi is configured with two indexes: ``root/pypi``, a caching mirror of
    an upstream, and ``hmd/local``, a hosted index that **inherits** from it.
    Consumers are given one URL, the inheriting one::

        http://registry.local.neuronsphere.io/hmd/local/+simple/

    A request for an internal wheel is served from ``hmd/local``. A request
    for a public package falls through to ``root/pypi``, which fetches it once
    and caches it. The consumer sees one index and does not know the
    difference, which is what makes a single ``UV_INDEX_URL`` sufficient.

    That inheritance is the reason devpi was chosen over the smaller
    alternatives. ``uv`` does not fall through across indexes the way ``pip``
    does; it is only because ``unsafe-best-match`` is already the platform
    default that a two-index arrangement would work at all, and depending on
    that would make the registry's correctness contingent on a setting made
    somewhere else for another reason.

    Publishing is ``twine upload -r neuronsphere``, unchanged, against the
    ``.pypirc`` stanza SPEC008 arranges. devpi accepts standard uploads;
    nothing about the publish path is registry-specific.

    **The risk that was to be run down, and its answer.** devpi's ``+simple``
    pages have historically been rejected by ``uv``'s stricter PEP 503
    parsing, which reads URL path segments as malformed filenames and skips
    every candidate -- surfacing as "no solution found" rather than as a parse
    error. The reported issue was closed without a reproduction, so the state
    was genuinely unknown and everything else was contingent on it.

    **It does not reproduce.** Against ``devpi-server`` 6.20.3, ``uv`` resolved
    and installed both a public package and an internal wheel from the single
    ``hmd/local/+simple/`` URL, the public one served through inheritance from
    ``root/pypi``. The fallback -- pypiserver plus proxpi, two index URLs,
    ``unsafe-best-match`` doing the joining -- is not needed and is recorded
    only so a future regression has somewhere to go.

.. spec:: The container registry
    :id: HMD_CLI_NEURONSPHERE_NERD006_SPEC004
    :links: HMD_CLI_NEURONSPHERE_NERD006
    :status: proposed

    zot serves ``/v2/`` as both a hosted registry for locally built images and
    a pull-through cache for several upstreams -- ``ghcr.io/hmdlabs``,
    ``ghcr.io/neuronsphere``, Docker Hub -- in one instance. CNCF Distribution
    cannot do this: ``registry:2`` in ``proxy`` mode is a mirror of exactly one
    upstream and refuses pushes, so covering the same ground would take three
    instances and a routing rule.

    Locally built images keep the existing tag convention,
    ``<repo_name>:<version-from-VERSION>``, and are pushed to the registry
    under it. This does not change how images reach k3s: ``hmd-cli-helm``
    imports them into the node directly, scoped by
    ``HMD_LOCAL_K3S_CLUSTER_NAME``, and continues to.

    The value here is the *cache*, not the host. The image-resolution order --
    ``HMD_CONTAINER_REGISTRY``, then ``HMD_LOCAL_NS_CONTAINER_REGISTRY``, then
    ``ghcr.io/neuronsphere``, then a bare local tag -- means a cold start pulls
    a dozen images from ghcr, and ``HMD_LOCAL_IMAGE_PULL_REGISTRIES`` already
    exists as the hook for putting a mirror in front of that.

    OCI is off by default because it is the least load-bearing of the four:
    Docker's own layer cache already covers the common case, and the k3s
    import path means nothing in a running environment depends on it.

.. spec:: Go and npm
    :id: HMD_CLI_NEURONSPHERE_NERD006_SPEC005
    :links: HMD_CLI_NEURONSPHERE_NERD006
    :status: proposed

    **npm.** Verdaccio serves ``/npm/``, hosting internal scoped packages and
    proxying npmjs. It slots into the existing ``TYPESCRIPT_REGISTRIES``
    contract, which already writes ``~/.npmrc`` in the
    ``//{url}:_authToken=`` and ``@{scope}:registry=`` form, and ``~/.npmrc``
    is already mounted into Docker builds as the ``npmrc`` BuildKit secret.
    Nothing structural is new.

    **Go is different, and this is the SPEC's substance.** There is no
    ``GOPROXY``, ``GOPRIVATE`` or ``GONOSUMDB`` set anywhere in the platform;
    Go code here builds against the public proxy with default settings. So
    unlike the other three formats, adding Athens is not "point an existing
    contract somewhere else" -- there is no contract, and ``GOPROXY`` has to
    be introduced.

    That is worth naming rather than glossing, because it changes the cost and
    the risk. ``GOPROXY`` is process-global and affects every Go build on the
    machine, including builds of ``nsctl`` itself. A misconfigured Athens is
    therefore able to break the tool that deploys it -- which is a bootstrap
    hazard none of the other formats have.

    Both are off by default. The platform's Go and TypeScript surface is small
    next to its Python surface, and an off-by-default component that is
    specified is cheaper to turn on later than an on-by-default one is to
    turn off.

.. spec:: Private upstreams
    :id: HMD_CLI_NEURONSPHERE_NERD006_SPEC006
    :links: HMD_CLI_NEURONSPHERE_NERD006
    :status: proposed

    The public upstreams need no credentials. The ones that matter do:

    .. list-table::
       :header-rows: 1
       :widths: 10 46 44

       * - Format
         - Private upstream
         - Credential
       * - PyPI
         - ``hmdlabs.jfrog.io/artifactory/api/pypi/hmd_pypi/simple``
         - host keychain, service ``uv:hmdlabs.jfrog.io``
       * - PyPI
         - AWS CodeArtifact
         - a **twelve-hour** token from
           ``codeartifact.get_authorization_token()``
       * - OCI
         - ``ghcr.io/hmdlabs``
         - GitHub PAT
       * - npm
         - ``npm.pkg.github.com/HMDLabs``
         - GitHub PAT

    Upstreams are declared **without** credentials, naming only where each
    lives -- the ``NERD004`` SPEC006 shape:

    .. code-block:: yaml

        pypi:
          enabled: true
          upstreams:
            - {name: pypi, url: "https://pypi.org/simple"}
            - name: hmd
              url: "https://hmdlabs.jfrog.io/artifactory/api/pypi/hmd_pypi/simple"
              credential: {keyring_service: "uv:hmdlabs.jfrog.io"}

    **The constraint that shapes this.** devpi persists its index
    configuration -- ``mirror_url`` included -- into its own data directory. A
    mirror URL carrying ``https://user:token@hmdlabs.jfrog.io/...`` would
    write a JFrog token in plaintext under ``$HMD_HOME/registry/pypi/``, which
    is exactly the outcome this requirement forbids. For PyPI, therefore,
    delivering the secret safely to the service is not sufficient: **the
    service must never learn it.**

    **Resolution: a credential-injecting upstream location on ``hmd_proxy``.**
    The component that already fronts everything becomes the only one that
    holds a secret. devpi's stored ``mirror_url`` becomes::

        http://hmd_proxy/upstream/hmd/simple

    with no credential in it, and nginx adds the ``Authorization`` header on
    the way out. The fragment carrying that header is written into a **tmpfs**
    mount inside ``hmd_proxy`` -- ``/etc/nginx/ns-secret``, ``include``d by
    the generated shell -- and **not** under ``$HMD_HOME/.cache/nginx/`` where
    every other fragment lives.

    Per format, pick whichever mechanism keeps the secret off disk:

    .. list-table::
       :header-rows: 1
       :widths: 20 30 50

       * - Format
         - Mechanism
         - Why
       * - PyPI (devpi)
         - injecting location on ``hmd_proxy``
         - devpi would otherwise persist the credential
       * - OCI (zot)
         - ``credentialsFile`` on tmpfs
         - registry auth is a token dance -- 401, ``WWW-Authenticate``, token
           endpoint, Bearer -- and injecting a static header into that chain
           is fragile. zot reads a credentials file natively.
       * - Go (Athens)
         - ``ATHENS_NETRC_PATH`` pointing at a netrc on tmpfs
         - native
       * - npm (Verdaccio)
         - uplink ``auth.token_env``, config on tmpfs
         - native

    **CodeArtifact is the exception worth writing down.** Its token expires in
    twelve hours. A long-lived cache holding one starts returning 401s that
    present as an empty index, hours after anything changed and with no event
    to correlate against. CodeArtifact upstreams must be re-resolved on a
    schedule rather than only at ``control-plane start``, and the failure
    reported as an expired credential rather than a missing package.

    **The acceptance gate is mechanical.** After a start with every private
    upstream configured, a recursive search for each secret across
    ``$HMD_HOME`` -- and across every path bind-mounted into these containers
    -- must find nothing. This is a test, not a review item; credential
    leakage is the highest-severity risk in this document and reasoning about
    it is not evidence.

.. spec:: Storage and lifetime
    :id: HMD_CLI_NEURONSPHERE_NERD006_SPEC007
    :links: HMD_CLI_NEURONSPHERE_NERD006
    :status: implemented

    ``$HMD_HOME/registry/<format>/`` -- the concrete instance of ``NERD004``
    SPEC008. A sibling of ``floci/`` and ``environments/``, deliberately
    **not** under ``.cache/``.

    It comes out of the instance name rather than being configured: ``NERD004``
    SPEC008 puts an extension's state at ``$HMD_HOME/<instance-name>/`` and
    exposes it as ``NS_EXTENSION_DIR``, so an instance named ``registry``
    produces exactly this path, and the compose file binds
    ``${NS_EXTENSION_DIR}/pypi``.

    It fails ``NERD004`` SPEC008's test: a hosted internal wheel exists
    nowhere else, and deleting the directory loses it. The contrast with
    ``NERD005``'s artifact cache is instructive and belongs in the docs --
    that one *is* under ``.cache``, because it is wholly refetchable. Two
    directories, one question, opposite answers.

    Consequences:

    - ``env purge`` does not touch it, per ``NERD004`` SPEC002. A developer
      who purges an environment does not lose the packages they published.
    - ``control-plane stop`` and ``start`` preserve it.
    - The upstream cache portion is technically refetchable, but it is not
      separated: splitting the tree into "hosted" and "cached" halves in order
      to make half of it disposable is a complication with no current buyer.

    ``nsctl`` creates the directories before the containers start, for the
    stated reason that a missing directory would be created by Docker as
    root-owned.

.. spec:: Conforming to NEP036
    :id: HMD_CLI_NEURONSPHERE_NERD006_SPEC008
    :links: HMD_CLI_NEURONSPHERE_NERD006
    :status: implemented

    When the registry is enabled, ``control-plane start`` writes into
    ``$HMD_HOME/.config/hmd.env`` per ``NERD004`` SPEC010 -- layered, in a
    delimited block, with an explicitly set user value always winning:

    - ``PYTHON_REGISTRIES`` -- the local index appended, carrying
      ``"publish": true`` so that ``.pypirc``'s ``[neuronsphere]`` server, and
      therefore ``twine upload -r neuronsphere``, targets it.
    - ``TYPESCRIPT_REGISTRIES`` -- the local npm registry appended.
    - ``HMD_LOCAL_IMAGE_PULL_REGISTRIES`` -- the local OCI prefix added.
    - ``GOPROXY`` -- new, per SPEC005.

    From there the existing machinery carries it the rest of the way without
    modification: ``hmd python login`` writes ``uv.toml`` and ``.pypirc``,
    ``hmd-cli-typescript`` writes ``.npmrc``, ``runner.Config.Extra`` forwards
    the environment into every projectbuilder deploy node, and Docker builds
    pick up ``uvconfig``/``pipconfig``/``npmrc`` as BuildKit secrets.

    **None of these values carries a secret.** The local registry's credential
    is a dummy on the loopback side (SPEC010), and every private credential
    lives on its upstream side under SPEC006. That is what lets ``hmd.env``
    remain a plain file, and it is a property to state rather than an accident
    of the first implementation to preserve by luck.

    **Written by ``nsctl``.** Phase 1 was first wired by editing ``hmd.env``
    by hand, which proved the rest of the chain works unmodified: ``hmd python
    login`` reads ``PYTHON_REGISTRIES``, writes ``uv.toml`` and ``~/.pypirc``,
    and everything downstream follows. ``NERD004`` SPEC010's writer now
    produces that edit instead, so requirement 3 -- *resolved automatically
    wherever the platform installs packages* -- **is met**.

    **One thing SPEC010's first rule did not cover, found here.**
    ``PYTHON_REGISTRIES`` is a JSON *map*, not a scalar, so "an explicitly set
    user value always wins" cannot mean replacing the value: a user who has
    configured JFrog would lose it. The managed block must merge the local
    entry into the user's map and let a user-defined key of the same name win.
    The same is true of ``TYPESCRIPT_REGISTRIES``. ``GOPROXY`` and
    ``HMD_LOCAL_IMAGE_PULL_REGISTRIES`` are delimited *lists*, which is a third
    shape again. Whole-value precedence is the wrong rule for all three.
    ``NERD004`` SPEC010 now says so, in three named merge shapes, and this
    document is where the requirement for them came from.

    A second-order consequence, already live: ``login()`` **replaces the whole**
    ``[[index]]`` **array in** ``uv.toml``. Anything hand-added there is lost on
    the next login, so the local index has to arrive through
    ``PYTHON_REGISTRIES`` and cannot be a local edit to ``uv.toml``.

    Three known leaks bypass the contract and will keep reaching JFrog
    directly regardless of what is configured, because they hardcode the URL:
    ``hmd-cli-bender``'s generated ``pip.conf``, ``hmd-cli-bartleby``'s
    generated ``pip.conf``, and ``hmd-img-transform-can``'s Dockerfile. This
    document originally named two; bartleby is the third, found while wiring
    phase 1. None is fixed by this NERD. All are recorded so that "the local
    index is configured but the network is still being hit" has a documented
    cause.

.. spec:: Publishing from ``hmd build``
    :id: HMD_CLI_NEURONSPHERE_NERD006_SPEC009
    :links: HMD_CLI_NEURONSPHERE_NERD006
    :status: partial

    Publishing to the local registry requires **no new publish code**. It is
    ``twine upload -r neuronsphere`` against a ``.pypirc`` stanza that
    SPEC008's ``PYTHON_REGISTRIES`` pointed at the local index.

    The care needed is on the other side. ``HMD_AUTO_PUBLISH=true`` is
    all-or-nothing: in ``hmd-cli-build``'s controller it triggers each tool's
    ``publish()`` *and* archives the build zip to the artifact librarian. A
    developer who sets it to get a local wheel also pushes Docker images to
    ghcr and Python packages to whatever ``[neuronsphere]`` currently names.

    So the requirement is a **local publish that is not the cloud publish**:
    the wheel goes to the local index and nothing is pushed to JFrog, ghcr or
    a cloud librarian. Whether that is a separate switch, or
    ``HMD_AUTO_PUBLISH`` resolving to whatever the registries are configured
    to be, is an implementation question -- but "just set
    ``HMD_AUTO_PUBLISH=true``" is not the answer, and the acceptance test is
    that no cloud endpoint is contacted.

    **Two obstacles were in the way, and neither was the publish code.** Both
    were in ``hmd-cli-python``'s ``login()``, and both presented as the local
    registry being configured and ignored:

    - ``~/.pypirc`` **was only written when it did not already exist.** Any
      developer who had ever logged in kept a ``[neuronsphere]`` pointing at
      JFrog, and ``twine upload -r neuronsphere`` kept publishing there --
      silently, because a successful upload to the wrong registry looks
      exactly like a successful upload. Now a read-modify-write rewrites
      ``[distutils]`` and ``[neuronsphere]`` and preserves every other server.
    - **The repository URL was derived by a substring replace of**
      ``"simple/"``. devpi's index URL ends ``/hmd/local/+simple/``, which
      became ``/hmd/local/+``. The segment is now dropped as a segment.

    Neither is a change to how publishing works, which is the point: the claim
    that retargeting the publish is configuration and not code turned out to
    be true only after two bugs stopped it from being true.

    Note the interaction with ``NERD005``: ``hmd build``'s two branches now
    have two local destinations. A **wheel** goes to this registry, resolved
    by name and constraint. The **build zip** goes to the local Artifact
    Librarian via ``nsctl artifact register``, resolved by content path. They
    are different artifacts with different consumers, and conflating them is
    the mistake SPEC011 exists to prevent.

.. spec:: Authentication of the local registry
    :id: HMD_CLI_NEURONSPHERE_NERD006_SPEC010
    :links: HMD_CLI_NEURONSPHERE_NERD006
    :status: implemented

    The local registry has **no meaningful authentication**. It is reachable
    only through ``hmd_proxy`` on loopback, on a single developer's machine,
    and the threat model does not include an attacker who already has that
    interface -- the same trade the local identity provider and the
    ``local-dummy`` librarian key already make.

    **It is not literally anonymous, and the reason is worth recording.** This
    document expected anonymous read and write, with a dummy credential named
    only as a fallback if some tool refused a passwordless index. What was
    built takes the dummy credential -- ``hmd``/``hmd``, following the local
    database convention where the password is the username -- as the default
    case, for a reason that has nothing to do with that check:

    - **twine has no anonymous upload.** It sends HTTP Basic credentials or it
      does not upload, and devpi's ``acl_upload`` defaults to the index owner.
      Making the upload genuinely anonymous means widening that ACL *and*
      giving twine a credential anyway, which is a longer way round to the same
      exposure.

    The check this document expected to be the forcing constraint is **not**
    one. ``hmd-cli-tools``' ``pip_conf_hosts_missing_password()`` guards
    ``username and not password``: a URL carrying no userinfo at all is
    explicitly ignored as a public index. A genuinely anonymous index would
    never have tripped it. The risk table said otherwise and was wrong; what
    *would* trip it is a half-configured index -- a username with no resolvable
    keyring password -- which is a shape to avoid rather than a constraint on
    the design.

    The second cost stands unchanged:

    - **Anything on the machine can publish to it.** A wheel in the local
      index is not evidence of anything. It follows that the local index must
      never become an input to a cloud build -- and since the wiring in
      SPEC008 lives in ``$HMD_HOME/.config/hmd.env``, which CI does not read,
      that separation holds by construction rather than by rule.

    If authentication is ever wanted, it is the identity provider's job, and
    it should arrive as a ``NERD004`` extension rather than as four
    per-format password files.

.. spec:: What this is not
    :id: HMD_CLI_NEURONSPHERE_NERD006_SPEC011
    :links: HMD_CLI_NEURONSPHERE_NERD006
    :status: proposed

    **This is not the Artifact Librarian, and it does not replace it.** The
    distinction is easy to get wrong and ``NERD005`` makes it easier still, so
    it is worth being exact:

    .. list-table::
       :header-rows: 1

       * -
         - Artifact Librarian (``NERD005``)
         - Package registry (this document)
       * - Addressing
         - content path: ``repository:/<repo>/<ver>/<file>.zip``
         - name plus version constraint, resolved by PEP 503, npm or the Go
           module protocol
       * - Answer to a request
         - exactly these bytes
         - a candidate set the client resolves against
       * - Holds
         - build zips, transform bundles, schemas, installers
         - wheels, images, modules, npm packages
       * - Consumed by
         - ``hmd deploy``, ``nsctl`` resolution
         - ``pip``/``uv``, ``docker``, ``go``, ``npm``

    "We already have a registry" is a true sentence about the wrong noun. A
    librarian cannot answer "the newest ``hmd-lib-auth`` compatible with
    ``~=0.3``", and a package index cannot hold a RepoClass's deploy tree.

    **This is not reachable off this machine.** It listens on loopback through
    ``hmd_proxy`` and nothing publishes a port.

    **This is not a replacement for JFrog or ghcr.** It caches them and hosts
    alongside them. CI is unaffected, and SPEC010's last paragraph explains
    why that is structural rather than a convention.

.. spec:: Phasing
    :id: HMD_CLI_NEURONSPHERE_NERD006_SPEC012
    :links: HMD_CLI_NEURONSPHERE_NERD006
    :status: proposed

    **Phase 1 -- Python, public upstream only.** devpi with ``root/pypi``
    mirroring pypi.org, ``hmd/local`` inheriting it, one vhost, SPEC008's
    ``PYTHON_REGISTRIES`` handback. This is the whole of the value for most
    users and it settles SPEC003's ``uv`` question, which everything else is
    contingent on. It needs ``hmd-img-devpi`` and nothing else new.

    **Done.** ``hmd-img-devpi`` and ``hmd-inf-local-registry`` exist, the
    extension runs, and the ``uv`` question is settled in devpi's favour. The
    handback is built -- ``NERD004`` SPEC010 -- so the wiring is a product of
    ``control-plane start`` rather than a hand edit, and the carve-out this
    paragraph used to carry is closed.

    **Phase 2 -- private upstreams.** SPEC006's injecting location, keychain
    resolution, and the ``grep`` gate. Separated from phase 1 deliberately:
    it is the highest-risk part, and pinning it to a working phase-1 registry
    means a failure is unambiguous.

    **Phase 3 -- local publish.** SPEC009's local target for ``hmd build``
    without the cloud side effects.

    Half of this arrived early, because phase 1 could not be demonstrated
    without it: ``twine upload -r neuronsphere`` now reaches the local index
    (SPEC009's two ``hmd-cli-python`` fixes). What is still owed is the *switch*
    -- a local publish that is not ``HMD_AUTO_PUBLISH=true``, which remains
    all-or-nothing.

    **Phase 4 -- OCI, then Go and npm**, each behind its switch. OCI first
    because ``HMD_LOCAL_IMAGE_PULL_REGISTRIES`` already exists to receive it;
    Go last because SPEC005's ``GOPROXY`` is the only genuinely new contract
    and the only one that can break ``nsctl``'s own build.

    Phases 1 to 3 are the requirement. Phase 4 is the requirement's stated
    scope, and a phase-3 stopping point would be a partial delivery rather
    than a complete one.

Acceptance criteria
-------------------

- ``uv pip install`` resolves **both** an internal wheel and a public package
  from the single index URL in SPEC003. This is the gate phase 1 exists to
  settle.

- A wheel built in one checkout and published locally installs into another
  checkout with no network access.

- ``hmd build`` configured against the local registry publishes the wheel and
  contacts **no** cloud endpoint -- verified by observation, not by reading
  the configuration.

- After a start with every private upstream configured, a recursive search
  for each secret across ``$HMD_HOME`` and every bind-mounted path finds
  nothing.

- A build with the network removed succeeds for packages the cache already
  holds.

- The registry is reachable at one URL from the host shell, a projectbuilder
  deploy node, and a pod in a running environment.

- ``nsctl env purge`` leaves the published wheels intact.

- Enabling ``oci`` in an already-running deployment brings up one more
  container without a purge and without disturbing the Python index.

- ``nsctl`` contains no reference to ``hmd-inf-local-registry``.

Risks and open questions
------------------------

.. list-table::
   :header-rows: 1
   :widths: 12 38 50

   * - Level
     - Risk
     - Mitigation
   * - **High**
     - **Credential leakage.** devpi persisting its ``mirror_url`` is the
       known case; the general one is any service that writes resolved
       configuration back into its data directory.
     - SPEC006's injecting location keeps the secret out of devpi entirely,
       and the ``grep`` gate is a test that runs rather than a claim that is
       reviewed.
   * - **High**
     - A tmpfs-delivered configuration is lost on any container restart
       ``nsctl`` did not drive. The registry comes back anonymous and 401s
       against private upstreams, presenting as a missing package.
     - ``NERD004`` SPEC006 requires the writer on every start; SPEC006 here
       requires the health check that distinguishes the two failures.
   * - **Closed**
     - **``uv`` may reject devpi's PEP 503 HTML.** Reported and closed
       without reproduction, so the state was unknown. It would surface as
       "no solution found", not as a parse error.
     - **Does not reproduce** against ``devpi-server`` 6.20.3. Both an
       internal wheel and a public package resolve through ``uv`` from the
       single index URL. See SPEC003.
   * - **Closed**
     - ``pip_conf_hosts_missing_password()`` fails fast on a passwordless
       host, which is what an anonymous local index is.
     - **Overstated.** The check guards ``username and not password`` and
       ignores a URL with no userinfo at all. An anonymous index never trips
       it; a username with no resolvable password does. See SPEC010.
   * - **Medium**
     - Three hardcoded JFrog URLs bypass ``PYTHON_REGISTRIES`` entirely --
       ``hmd-cli-bender``'s and ``hmd-cli-bartleby``'s generated ``pip.conf``,
       and ``hmd-img-transform-can``'s Dockerfile.
     - Out of scope, recorded in SPEC008 so the symptom has a documented
       cause rather than being diagnosed twice.
   * - **Medium**
     - **An index URL that does not resolve inside a Docker build.**
       ``merge_uv_indexes_into_pip_conf`` folds every ``uv.toml`` index into
       the ``pipconfig`` BuildKit secret, so a repo whose build has a
       ``docker`` command gets ``registry.local.neuronsphere.io`` in a
       container that cannot resolve it.
     - Untested in phase 1: ``hmd-lang-siesta`` has no Docker build. The hook
       exists -- ``hmd-cli-docker`` already attaches builds to
       ``HMD_DOCKER_NETWORK`` -- and this is the first thing to break when a
       Docker-building repo is tried.
   * - **Medium**
     - ``HMD_AUTO_PUBLISH=true`` also pushes Docker to ghcr and archives to
       the librarian. A developer reaching for a local publish gets all of
       it.
     - SPEC009 requires a local publish that is not the cloud publish, with
       "no cloud endpoint contacted" as the test.
   * - **Medium**
     - ``GOPROXY`` is process-global. A misconfigured Athens breaks every Go
       build on the machine, ``nsctl``'s own included.
     - Off by default, sequenced last in SPEC012, and called out in SPEC005
       as a bootstrap hazard the other formats do not have.
   * - **Low**
     - ``$HMD_HOME/registry/`` grows without bound; the upstream cache is not
       separable from the hosted packages.
     - Accepted. Splitting the tree to make half of it disposable has no
       current buyer; a size report in ``nsctl status`` is the cheap
       mitigation.
   * - **Low**
     - Four services rather than one is four things to keep working.
     - Three of the four are single-binary containers with official images,
       and each is independently switchable. Nexus remains the documented
       escape hatch.

Open questions, to be settled in implementation:

- **How a control-plane extension actually deploys.** Every bundled
  ``hmd-inf-*`` RepoClass is a helm chart, and a control-plane extension has
  no cluster to deploy into. The registry is the case that forces an answer,
  and the likely one is a ``src/local`` overlay running plain containers on
  the platform network -- which is also ``NERD004``'s largest open question.

- **Whether ``hmd_proxy`` should gain a tmpfs mount unconditionally**, or only
  when an extension declares it needs one. Unconditionally is simpler and
  costs a few kilobytes; conditionally means the control-plane compose file
  changes shape based on an extension, which is a coupling ``NERD004``
  otherwise avoids.

- **Whether the OCI registry should also be reachable at a published host
  port.** ``localhost:<port>`` is treated as insecure by Docker
  automatically, which would remove SPEC002's ``insecure-registries``
  prerequisite -- but only ``hmd_proxy`` may publish, so it would mean
  widening the control-plane port band for an off-by-default format.
