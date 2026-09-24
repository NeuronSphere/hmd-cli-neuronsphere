.. NERD004 Extending the Control Plane from HMD_HOME

NERD004 Extending the Control Plane from ``HMD_HOME``
=====================================================

.. req:: Extend the control plane with declared RepoClasses from the user's own configuration
    :id: HMD_CLI_NEURONSPHERE_NERD004
    :status: proposed

    The control plane shall be extendable, from a manifest under the user's
    own ``$HMD_HOME``, with RepoClasses that are not part of the default
    control plane and are not carried by the ``nsctl`` binary.

    An extension declared this way is deployed alongside the control plane
    rather than inside an environment: it outlives every environment, it is
    reachable whether or not one is running, and there is one of it per
    machine rather than one per environment slot. It runs as containers on
    the platform network, from a compose file the RepoClass carries in its
    ``src/local`` directory -- the overlay convention ``nsctl`` already reads
    for a repo's local deploy alternates.

    Absent the manifest, nothing extra is deployed and the control plane is
    exactly what it is today. That default is the requirement, not a
    convenience: an extension surface whose cost is paid by people who do not
    use it is the wrong surface.

.. note::

    This document specifies the mechanism only. Its first two consumers are
    ``NERD005`` (artifact-sourced RepoClasses, which is how an extension is
    distributed without a checkout) and ``NERD006`` (the local package
    registry, which is the worked example).

Motivation
----------

``nsctl`` ships the environment substrate and nothing above it. Every
workload is a RepoClass a user adds, declared in an environment manifest at
``$HMD_HOME/environments/<slug>.yaml`` and deployed into that environment's
k3s cluster. That is the right model for a workload, and the wrong one for
three kinds of thing:

1. **Things that must outlive an environment.** ``env purge``
   (``internal/environment/purge.go``) deletes the k3s container's volumes
   and then removes the whole state directory with ``os.RemoveAll`` --
   Postgres data, graph data, kubeconfig and reconcile snapshots in one
   call. Anything with a durable local store loses it.

2. **Things that must be reachable when no environment is running.**
   ``hmd build`` runs before ``nsctl env start`` and after ``env stop``. A
   service a build depends on is absent exactly when the build wants it.

3. **Things there should be one of.** ``registry.PortsPerEnv`` allows
   sixteen environments on one ``HMD_HOME``. Sixteen copies of a
   machine-global cache is not a scaling story, it is a bug.

A package registry is all three at once, which is what surfaced this. But the
answer is not to special-case a registry. It is to give the control plane the
same declarative extension surface an environment already has, and to let the
registry be an ordinary consumer of it.

Scope
-----

**In scope.**

- A control-plane manifest under ``$HMD_HOME/.config/``, reusing the
  environment manifest schema.
- Applying it during ``nsctl control-plane start`` and ``control-plane
  apply``, by merging the declared RepoClasses' ``src/local`` compose files
  into the control plane's own compose project.
- The conventions an extension needs in order to be configured, to be
  reachable, and to keep durable state.
- Isolating a failed extension from everything else the control plane runs.

**Out of scope.**

- Where an extension's *code* comes from. A manifest may name a working tree
  under ``$HMD_REPO_HOME`` today; ``NERD005`` adds the artifact source that
  makes distribution possible.
- Any particular extension. ``NERD006`` is the first.
- Plugin *discovery*. There is none, deliberately, and SPEC013 says so.
- Registering an extension in the deployment graph. An extension is not a
  RepoInstanceDeployment and does not appear in a BOM; see SPEC003.

Architecture Overview
---------------------

::

    $HMD_HOME/
      .config/
        hmd.env                  # layered environment, already read by internal/hmdenv
        control-plane.yaml       # NEW -- the extension manifest (SPEC001)
        uv.toml, pip.conf        # authored tool configuration, already here
      environments/
        <slug>.yaml              # the per-environment manifest, unchanged
      <extension-name>/          # durable extension state (SPEC008)
      .cache/                    # machine state; purge may delete any of it

The apply path is the control plane's own container path, with more services
in it::

    control-plane start
      -> Bootstrap()                        # unchanged: naming, artifact-lib, ms-deployment
      -> manifest.LoadControlPlane(.config/control-plane.yaml)
      -> repoclass.Resolver.Dir(class)      # unchanged: pin, working tree, bundled
      -> <tree>/src/local/docker-compose.extension.yml
      -> compose.Parse(..., extension lookup)
      -> compose.Runner.UpEach(...)         # NEW: per-service failure isolation
      -> router vhost fragments / hmd_proxy aliases

.. spec:: The control-plane manifest
    :id: HMD_CLI_NEURONSPHERE_NERD004_SPEC001
    :links: HMD_CLI_NEURONSPHERE_NERD004
    :status: proposed

    The manifest lives at ``$HMD_HOME/.config/control-plane.yaml``, with
    ``.yml`` and ``.json`` accepted in the order ``manifest.Extensions``
    already defines. ``HMD_LOCAL_CP_MANIFEST`` overrides discovery, mirroring
    ``manifest.PathOverride``.

    ``.config/`` because that is where authored configuration already lives --
    ``hmd.env``, ``uv.toml``, ``pip.conf`` -- and because it is not
    ``.cache/``. The distinction is the one ``manifest.Root`` already makes
    about environment manifests: *"a manifest is input a developer edits and
    may commit, not machine state env purge is free to delete."* The same
    sentence decides this file's location.

    **The schema is the environment manifest's schema**, ``manifest.Manifest``
    at version 1, reused rather than re-declared. A second struct describing
    the same three fields is the copy that goes stale, and the fields are the
    same three: which RepoClass, at which version, configured how.

    .. code-block:: yaml

        version: 1
        name: control-plane
        repos:
          - instance_name: package-registry
            repo_class_name: hmd-inf-local-registry
            version: "0.1"
            source: {type: artifact}          # NERD005
            instance_configuration:
              url: http://registry.local.neuronsphere.io
              upstream: server:8080
              pypi: {enabled: true}
              oci:  {enabled: true}

    ``name`` is fixed at ``control-plane``; a manifest naming anything else is
    rejected rather than silently applied, because the only thing that name
    could mean is that the file was meant to be an environment manifest and
    landed in the wrong directory.

    ``dependencies`` is parsed and preserved but has no meaning here. There is
    no graph to resolve a role against (SPEC003), and ordering between two
    extensions is not something this mechanism offers -- an extension that
    needs another to be up first must tolerate it not being up yet, exactly as
    every container on a shared network already must.

    Validation is ``Manifest.Validate`` in a control-plane *scope*: the same
    checks, plus SPEC011's reserved names and the fixed ``name``. ``plugins``
    and ``plugin_config`` are preserved verbatim and reported through
    ``Unsupported()``, exactly as they are for an environment -- see SPEC013.

.. spec:: Scope and lifetime
    :id: HMD_CLI_NEURONSPHERE_NERD004_SPEC002
    :links: HMD_CLI_NEURONSPHERE_NERD004
    :status: proposed

    A declared extension runs as containers in the control plane's own compose
    project, on the control plane's network, alongside ``hmd_proxy``,
    ``floci``, the Deployment GUI, the DAG runner and the identity provider --
    and **not** inside any environment.

    Three consequences, each of which is the reason this document exists:

    - **``env purge`` does not touch it.** Purge is scoped by environment: it
      deletes that environment's Floci records, containers labelled with that
      environment's account, its nginx fragments and its state directory. An
      extension is in none of those sets. Stated positively, because a reader
      whose registry survived a purge should be able to confirm that was
      intended rather than lucky.

    - **It is up whenever the control plane is up.** ``control-plane start``
      brings it up; ``control-plane stop`` takes it down with everything else;
      no ``env start`` is required in between. It is also up before
      ``hmd-ms-deployment`` is, which is the property a package registry needs
      and the DAG path could not have given it.

    - **There is one per ``HMD_HOME``.** Not one per environment. An
      extension that genuinely wants to be per-environment belongs in an
      environment manifest, and the doc should say that too, because the
      wrong choice here is silent.

    ``control-plane stop`` stops an extension's containers without removing
    them, matching ``controlplane.Stop``'s existing behaviour for the
    services it already manages. Only the purge path -- ``nsctl env purge``
    with no name, which tears the whole control plane down -- removes them.

    Both sweep by compose label rather than by re-resolving the manifest. A
    container whose repo class has since been deleted, or whose declaration
    has been removed, still has to stop, and it is exactly the one a
    name-driven sweep would miss and leave running against a network that is
    about to go away.

.. spec:: Applying the manifest
    :id: HMD_CLI_NEURONSPHERE_NERD004_SPEC003
    :links: HMD_CLI_NEURONSPHERE_NERD004
    :status: proposed

    **An extension is applied by merging its compose services into the control
    plane's compose project.** It is not seeded into a BOM, does not become a
    ChangeSet, and never reaches ``hmd-ms-deployment``.

    This was the document's largest open question and the answer is not the
    one it guessed at. Three things decide it:

    - **The control plane's own services are compose services**, not graph
      instances. ``hmd_proxy``, ``floci``, the GUI, the runner and the
      identity provider are five entries in one bundled compose file that
      ``compose.Parse`` reads and ``compose.Runner`` creates over the Engine
      API. A thing that sits beside them is the same kind of thing, and
      making it a different kind of thing needs a reason.

    - **The DAG path cannot run early enough.** A deploy node is executed by
      the runner, which submits to ``hmd-ms-deployment``, which is the last of
      the three ``bootstrapServices``. An extension deployed that way can only
      exist once the graph does. The registry's whole purpose is to serve
      ``hmd build``, which runs before any of that -- so the ordering the DAG
      path forces is precisely the ordering that makes the extension useless.

    - **There is no cluster to deploy a chart into.** Every bundled
      ``hmd-inf-*`` RepoClass is a helm chart aimed at an environment's k3s.
      A control-plane extension is independent of every environment by
      definition, so it has nowhere to put one.

    The path, then:

    - ``manifest.LoadControlPlane`` reads the manifest. **A missing manifest
      is not an error.** It returns nil and the control plane starts exactly
      as it does today -- the same decision ``manifest.Load`` already makes
      for an environment with no manifest.
    - ``repoclass.Resolver.Dir`` locates each declared RepoClass's tree,
      through the tiers it already implements: an explicit ``source.path``, a
      checkout the user asked for, the tree the binary carries, whatever
      checkout is lying around. ``ResolveVersion`` reports the version *and
      where it came from*, which SPEC012 puts in ``status``.
    - The tree's ``src/local/docker-compose.extension.yml`` is parsed by
      ``compose.Parse`` against the extension's own lookup (SPEC005), and its
      services are validated against SPEC004's rules.
    - Every extension's services go into one ``compose.Project`` carrying the
      **same project name** as the control plane's, so the compose labels
      group them together and ``docker compose -p <project> ps`` still sees
      the whole platform.
    - ``compose.Runner.UpEach`` creates and starts them, per SPEC006.

    **Both ``start`` and ``apply`` do this.** ``control-plane start`` applies
    the manifest as part of starting -- SPEC002 says an extension is up
    whenever the control plane is, and a start that left a declared extension
    down would contradict it. ``control-plane apply`` is the fast path: it
    converges the extensions alone, without the Floci health wait, the
    bootstrap, the gateway sweep or the route rewrite. Environments have both
    verbs for the same reason, that ``env start`` is cheap and ``env apply``
    is not.

    **There is no reconcile snapshot.** An environment needs
    ``applied-changeset.json`` because a deployed helm release does not carry
    what it was deployed *from*, so drift is otherwise invisible. A container
    does carry it: ``compose.Runner`` already stamps
    ``io.neuronsphere.nsctl.config-hash`` on every container it creates and
    compares it on the next run, which is exactly the question a snapshot
    exists to answer, per container, with no file to lose. Adding a second
    record of the same fact would be the copy that goes stale.

    **An instance that is running but no longer declared is reported and left
    alone**, never destroyed -- the environment path's rule, for the
    environment path's reason. Removing a line from a manifest is not an
    instruction to destroy what it started; ``docker rm`` and ``nsctl env
    purge`` are the explicit verbs.

.. spec:: The extension's compose file
    :id: HMD_CLI_NEURONSPHERE_NERD004_SPEC004
    :links: HMD_CLI_NEURONSPHERE_NERD004
    :status: proposed

    A RepoClass declares its containers at
    ``src/local/docker-compose.extension.yml``.

    ``src/local`` because that is already where a RepoClass keeps the
    alternates that apply only to a local deploy --
    ``src/local/deploy_local.sh``, ``src/local/cdktf``, ``src/local/helm``,
    ``src/local/config_local.json`` -- and ``internal/runner/workspace.go``
    already reads exactly that directory for exactly that purpose. A fifth
    shape in an established directory is cheaper than a new convention.

    **A fixed name, and no index file.** Nothing is scanned and nothing
    enumerates candidates: the file is at that path or the extension has none.
    ``nsplugin.json`` -- the legacy index naming a ``compose_file`` -- is not
    coming back, for SPEC013's reason. Named ``.extension.yml`` rather than
    ``.control-plane.yml`` so it is never confused with ``nsctl``'s own
    bundled ``services/docker-compose.control-plane.yml``, which describes the
    control plane rather than an addition to it. A ``src/local`` holding some
    other ``docker-compose*.yml`` and not this one is reported by name, since
    "no compose file" would be a lie a reader has to go and disprove.

    Four rules, each refused at validation with its reason:

    .. list-table::
       :header-rows: 1
       :widths: 26 34 40

       * - Rule
         - Why
         - Enforced by
       * - No ``container_name``
         - The container is then
           ``<project>-<instance>-<key>-1``, and the project name already
           carries the eight-hex ``HMD_HOME`` digest. A pinned name is global
           and is how a second checkout recreates the first's containers.
         - New check; the naming falls out of ``compose.Service.Name``
       * - No published ``ports``
         - ``hmd_proxy`` is the only container in the stack permitted to
           publish one. SPEC007 is how an extension is reached instead.
         - ``compose.CheckExclusivePublisher``, already
       * - No named volumes
         - A volume is invisible in ``$HMD_HOME``, survives a purge that was
           meant to be total, and cannot be inspected with ``ls``. SPEC008 is
           where state goes.
         - ``compose.parseVolumes``, already
       * - Service keys are prefixed ``<instance_name>-``
         - Two extensions may both key a service ``server``; no extension can
           key one ``proxy`` and inherit the publisher exemption.
         - New, applied on parse

    The file declares the platform network external, exactly as the bundled
    one does, and ``nsctl`` interpolates ``NEURONSPHERE_DOCKER_NETWORK`` into
    it so it lands on the right one:

    .. code-block:: yaml

        services:
          server:
            image: ${NS_CONFIG_IMAGE:-ghcr.io/example/thing:0.1}
            restart: unless-stopped
            volumes:
              - "${NS_EXTENSION_DIR}/data:/var/lib/thing"
            networks: [neuronsphere_default]

        networks:
          neuronsphere_default:
            external: true
            name: ${NEURONSPHERE_DOCKER_NETWORK:-neuronsphere_default}

.. spec:: Configuration, without a generator
    :id: HMD_CLI_NEURONSPHERE_NERD004_SPEC005
    :links: HMD_CLI_NEURONSPHERE_NERD004
    :status: proposed

    **``instance_configuration`` reaches the compose file as interpolated
    variables and compose profiles. There is no generator script.**

    A generator was the obvious shape -- the withdrawn Python plugin model had
    one, ``render_compose_yaml``, and every plugin implemented it. It is not
    adopted here, and the reason is worth recording rather than rediscovering.
    A script is either a second host prerequisite, against a binary whose only
    prerequisite is Docker, or a container run on every single start to render
    a few kilobytes of YAML. And the work the legacy generators actually did
    turns out to be expressible without one: injecting bucket URLs into an
    environment, removing a service that Floci replaced, wiring a token,
    switching a component off. Those are interpolation and profiles. The door
    is not nailed shut -- a concrete case that defeats both is the argument
    for reopening it -- but a mechanism should not ship with an escape hatch
    nobody has needed yet.

    **Variables.** ``instance_configuration`` is flattened into namespaced
    variables, with non-scalars JSON-encoded:

    .. code-block:: yaml

        instance_configuration:
          pypi: {enabled: true}       # -> NS_CONFIG_PYPI_ENABLED=true

    Alongside them, four facts ``nsctl`` knows and the manifest should not
    have to repeat: ``NS_EXTENSION_NAME``, ``NS_EXTENSION_VERSION``,
    ``NS_EXTENSION_DIR`` (SPEC008's state directory) and
    ``NS_EXTENSION_REPO_DIR`` (the resolved tree). Everything else --
    ``HMD_HOME``, ``HMD_REPO_HOME``, ``NEURONSPHERE_DOCKER_NETWORK``, and any
    variable a user set -- comes from ``controlplane.ComposeEnv``'s existing
    layered lookup underneath.

    **The manifest wins for its own names.** ``NS_CONFIG_*`` and
    ``NS_EXTENSION_*`` are resolved before the process environment, which
    inverts ``hmdenv.Layered``'s usual precedence and does so deliberately:
    those names are ``nsctl``'s, the manifest is the declarative source of
    truth for them, and a stray ``NS_CONFIG_PYPI_ENABLED`` exported in a shell
    silently overriding a checked-in manifest is a bug with no symptom. Every
    other variable keeps the normal rule -- process environment, then
    ``hmd.env``.

    **Profiles switch components.** A profile ``p`` on an extension service is
    active when the instance configuration selects it, by any of:

    .. code-block:: yaml

        instance_configuration:
          profiles: [pypi, oci]     # explicit list
          pypi: {enabled: true}     # or a block with enabled: true
          oci: true                 # or a plain boolean

    The second form is there because it is how ``NERD006`` already writes it,
    and a mechanism that made its first consumer restate its configuration
    would be the wrong mechanism.

    **Switching one on must not require a purge.** Turning on a format after
    the fact is a manifest edit and an ``apply``: the extension project is
    recomputed, the new container is created, and the ones whose config hash
    did not change are left running.

.. spec:: A failed extension is isolated
    :id: HMD_CLI_NEURONSPHERE_NERD004_SPEC006
    :links: HMD_CLI_NEURONSPHERE_NERD004
    :status: proposed

    **An extension that fails must not stop anything else from working.** Not
    the control plane's own services, not another extension, and not an
    environment start.

    This is not free. ``compose.Runner.Up`` iterates services in sorted key
    order and returns on the first error, which is right for the five bundled
    services -- a proxy that will not start is not a partial success -- and
    wrong the moment a sixth service arrives that nobody vouched for. With
    extensions in the same project, one unpullable image would abort the
    control plane's own containers, and which ones survived would depend on
    where the extension's name sorted.

    So: the core project keeps ``Up`` and keeps aborting. The extension
    project gets ``UpEach``, which starts every service and collects
    per-service failures rather than stopping at the first.

    **Resolution failures are isolated the same way**, and there are several:
    no working tree for the repo class, no
    ``src/local/docker-compose.extension.yml``, YAML that will not parse, a
    forbidden ``container_name`` or published port or named volume. Each is
    reported against the instance that caused it, that instance is skipped,
    and every other instance and the whole control plane carry on.

    **``start`` warns and succeeds; ``apply`` exits non-zero.** The asymmetry
    is forced rather than chosen: ``env start`` calls ``controlplane.Start``
    and returns its error, so an extension able to fail a start is an
    extension able to fail every environment start on the machine. ``apply``
    has no such caller, and a converge command that reported success while
    something did not converge would be useless in a script -- so it exits
    ``nserr.DeployFailed``.

    Both print what failed and why, per instance, at the end rather than
    buried mid-run. A registry that failed to come up will otherwise be
    blamed for every build failure that follows until somebody scrolls.

.. spec:: Reaching an extension -- one URL
    :id: HMD_CLI_NEURONSPHERE_NERD004_SPEC007
    :links: HMD_CLI_NEURONSPHERE_NERD004
    :status: proposed

    An extension that serves anything is reached at **one URL**, declared in
    its instance configuration alongside the service and port that answers
    for it:

    .. code-block:: yaml

        instance_configuration:
          url: http://registry.local.neuronsphere.io
          upstream: server:8080     # <service key>:<port>

    One URL is not a nicety. It is the identity provider's constraint, and
    the reason it was forced there applies verbatim: ``authd``'s issuer *"has
    to be one string however the server was reached: the browser goes through
    hmd_proxy on the host, application pods resolve it through a CoreDNS
    record, and Floci's Lambda containers through a Docker network alias. All
    three must land on the same name."* Substitute "package index URL" for
    "issuer" and nothing changes -- an index URL is stamped into ``uv.toml``,
    ``.pypirc``, ``.npmrc``, ``GOPROXY`` and image tags, and a consumer that
    resolved one name and was configured with another fails somewhere far from
    the cause.

    .. list-table::
       :header-rows: 1
       :widths: 26 40 34

       * - Consumer
         - Mechanism
         - Status
       * - Host shell
         - an nginx ``server_name`` block on :80, in a ``vhost.d/`` fragment,
           plus host-side resolution of the name -- the local resolver of
           :doc:`NERD026_Local_Name_Resolution`, or an ``/etc/hosts`` entry
         - ``router.namedVhostServer``
       * - Floci Lambdas, sibling containers
         - Docker network alias on ``hmd_proxy``
         - the control-plane compose aliases
       * - k3s pods
         - a ``coredns-custom`` record pointing at ``hmd_proxy``
         - ``k3s.CoreDNSRecords``; **deferred**, see the open questions

    The hostname is derived from the single configured URL, so the vhost and
    the alias cannot drift apart. ``authd.Host`` parses the issuer for exactly
    this reason and is the function to copy.

    The vhost goes in a fragment under ``vhost.d/``, written whole on every
    start *and every apply* so that an extension which is no longer declared
    loses its listener by being absent -- the property
    ``WriteControlPlaneVhosts`` already relies on. Declaring nothing rewrites
    it empty rather than leaving the previous one in place, which is the case
    that makes the property true rather than nearly true.

    The alias is appended to ``hmd_proxy``'s network attachment before the
    core project is started, which changes the proxy's config hash and so
    recreates it when the set of extensions changes.

    **The two legs therefore land at different times, and this is stated
    rather than left to be discovered.** ``apply`` writes the vhost, so the URL
    works from the host -- where ``/etc/hosts`` points the name at the proxy --
    as soon as it returns. The alias needs the proxy recreated, which only
    ``start`` may do: ``hmd_proxy`` is the only container publishing host ports
    and every route on the machine goes through it, so a fast path may not
    restart it, and reconnecting it to the network live would change its IP
    under every established consumer. ``apply`` reports the gap by name and
    says which verb closes it, because an unresolvable hostname inside a
    sibling container has no other clue attached to it.

    Reaching it from the host costs one ``/etc/hosts`` line, exactly as the
    identity provider and the environment UIs do. ``control-plane start``
    prints the line to add rather than leaving the reader to derive it.

    **Amended (2026-09-24).** The environment UIs no longer cost such a line --
    :doc:`NERD025_Port_First_Addressing` SPEC001 serves them on published ports
    -- so extensions and the identity provider are now the cases that still
    want a name. For them the cost is paid once, for the whole suffix, by
    NERD026 rather than per extension. ``control-plane start`` continues to
    print the ``/etc/hosts`` line as the fallback for a machine without the
    resolver, and the open question below -- whether ``CheckHostsEntries``
    should warn for declared extensions -- is answered by NERD025 SPEC004,
    which turns that check into a report covering every name it knows about.

.. spec:: Durable state
    :id: HMD_CLI_NEURONSPHERE_NERD004_SPEC008
    :links: HMD_CLI_NEURONSPHERE_NERD004
    :status: proposed

    An extension that keeps durable data keeps it at
    ``$HMD_HOME/<extension-name>/`` -- a sibling of ``floci/`` and
    ``environments/``, and deliberately **not** under ``.cache/``. It reaches
    the compose file as ``NS_EXTENSION_DIR``.

    The test is one question: *if this directory were deleted, would anything
    be lost that cannot be recreated?* If yes it is state and belongs beside
    ``floci/``; if no it is cache and belongs under ``.cache/``. The two
    consumers land on opposite sides of that line and the contrast is worth
    keeping in the docs: ``NERD005``'s unpacked artifacts are wholly
    refetchable and live in ``.cache``; ``NERD006``'s hosted internal wheels
    exist nowhere else and do not.

    ``nsctl`` creates the directory before the container starts. This is not
    tidiness -- ``controlplane.Start`` already pre-creates its own cache and
    Floci data directories for the stated reason that *"a missing directory
    would be created by Docker as root-owned"*, and an extension inherits
    that hazard exactly.

    Named Docker volumes are not used, and SPEC004 refuses them outright.

.. spec:: Secrets
    :id: HMD_CLI_NEURONSPHERE_NERD004_SPEC009
    :links: HMD_CLI_NEURONSPHERE_NERD004
    :status: proposed

    **A manifest names where a credential lives. It never carries one, and
    nothing writes one under** ``$HMD_HOME``.

    The manifest is a file a developer edits and may commit -- the same
    sentence that put it in ``.config/`` rather than ``.cache/`` in SPEC001
    also forbids it from holding a secret.

    .. code-block:: yaml

        instance_configuration:
          credentials:
            - name: hmd-upstream
              keyring_service: "uv:hmdlabs.jfrog.io"
              keyring_user: "${USER}"      # optional; defaults to $USER
              deliver: file                # file (default) | env

    A ``password:`` key is refused outright rather than honoured, because a
    manifest that carried one would put it in a git history.

    **Resolution.** ``control-plane start`` and ``control-plane apply`` resolve
    each reference through the host keychain, using the probe order that
    already exists in ``hmd-cli-tools``' ``registry_tools.resolve_index_password()``:
    ``uv:<host>``, then the full URL, then ``scheme://host``, then the bare
    host. An explicit ``keyring_service`` is probed ahead of all four.

    **That order is ported into Go rather than called.** This reverses what
    this SPEC first said, and the reason is the one that outranks it: calling
    the Python helper means an interpreter and an installed ``hmd-cli-tools``
    on the host, and ``nsctl``'s whole premise is a single binary whose only
    prerequisite is Docker. ``internal/keyring`` shells out to the platform's
    own client -- ``security`` on macOS, ``secret-tool`` on Linux -- and takes
    no new module dependency.

    The objection to a second copy stands and is answered rather than
    dismissed. The failure it predicts is exact: a probe that checks three of
    the four keys finds nothing and reports a missing credential the user can
    see with their own eyes in Keychain Access. So the candidate order is
    pinned by a test that quotes the Python it came from, and the port was
    checked against the real thing -- a throwaway keychain item that both
    readers resolved to the same value.

    **The second leg is per platform, and that is faithfulness rather than an
    omission.** The Python tries ``get_password(service, username)`` and then
    ``get_credential(service, None)``, the latter covering a credential ``uv``
    stored under an account name that differs from the index username. As of
    ``keyring`` 25.6.0 the macOS backend does not override ``get_credential``,
    so the base implementation returns ``None`` outright whenever the username
    is ``None`` -- the leg is dead code on macOS. Implementing it there
    (``find-generic-password -s <service> -w``, which does work) would make
    ``nsctl`` resolve credentials ``hmd-cli-tools`` cannot, and two tools
    disagreeing about whether a credential exists is worse than one lookup
    fewer. The Linux SecretService backend does override it, so there it is
    implemented.

    **Delivery. Never to the host filesystem.** Two mechanisms, chosen by
    what the consuming service can read:

    1. **A tmpfs mount, written after the container starts.** The service
       declares ``tmpfs: [/run/ns-secrets]``; ``nsctl`` writes each resolved
       value to ``/run/ns-secrets/<name>`` once the container is up, mode
       0600. Nothing is written under ``$HMD_HOME``, and nothing appears in
       ``docker inspect``. This is the default. A credential that resolved and
       found no service declaring the mount is reported, because a credential
       delivered nowhere is the silent half of one that did not resolve.

       **Through an exec, not ``docker cp``.** This is the sharp implementation
       detail and it was found the hard way. ``CopyToContainer`` resolves its
       destination in the container rootfs *as the daemon sees it*, where a
       tmpfs mounted inside the container does not exist -- so the write lands
       in the writable layer underneath the mount. It is invisible to the
       container, which is how it goes unnoticed, and it is on disk, which is
       the one outcome this SPEC exists to prevent. ``docker diff`` reported
       ``A /run/ns-secrets/upstream`` on the first real run. An exec runs in
       the container's own mount namespace and sees the tmpfs; the value
       travels on standard input, so it appears in no process listing. A
       container with no shell cannot be written to this way and says so.

    2. **An environment variable**, only for a service whose configuration
       cannot reference a file -- ``deliver: env``, arriving as
       ``NS_SECRET_<NAME>``. It is visible in ``docker inspect`` and to
       anything that can read the daemon socket. Acceptable on a
       single-developer machine, recorded here as the weaker option, and
       never chosen when (1) is available. It does buy one thing: the value
       reaches the container's configuration hash, so a rotated credential
       recreates the container instead of being ignored until something else
       restarts it.

    A tmpfs is empty again after a restart. Whatever writes it must therefore
    run on **every** start, including a restart Docker initiated and
    ``nsctl`` did not drive -- otherwise the service comes back anonymous and
    fails against its upstream in a way that reads as a missing package. This
    is the sharpest edge in the whole mechanism and belongs in the risk
    table, not only here. Delivery therefore runs on every ``apply``, whether
    or not that apply created anything; it was reproduced deliberately --
    ``docker restart`` empties the directory, the next ``apply`` refills it --
    rather than reasoned about.

    Nothing yet re-delivers between applies. A container that Docker restarts
    while nobody is running ``nsctl`` stays anonymous until the next one, and
    the health check that would distinguish "no credential" from "no package"
    belongs to the consumer that has an upstream to check against.

    **Rotation.** A changed keychain entry takes effect on the next
    ``control-plane start``. Nothing watches the keychain. Credentials that
    expire on their own schedule -- an AWS CodeArtifact token lives twelve
    hours -- need periodic re-resolution and are specified by the consumer
    that has them; see ``NERD006`` SPEC006.

    **Never logged.** ``scrub_secrets``' expression is ported alongside the
    probe order and applied to every error path that can carry an index URL.
    ``apply`` and ``status`` report *which* credential references resolved and
    which did not, **and where each was looked for**, without printing a
    value. Without the last part an unresolved credential surfaces as a 401
    from a cache -- indistinguishable from an empty index -- and the reader is
    left guessing which of four service names to store an entry under.

    **The acceptance gate is mechanical**, as ``NERD006`` SPEC006 requires: a
    recursive search for the value across ``$HMD_HOME`` finds nothing, and
    ``docker diff`` shows no entry for the delivered file. Only the empty tmpfs
    mount point appears there, which is Docker creating the mount point and
    not a write.

.. spec:: Configuration handback
    :id: HMD_CLI_NEURONSPHERE_NERD004_SPEC010
    :links: HMD_CLI_NEURONSPHERE_NERD004
    :status: implemented

    An extension may contribute environment variables back to the platform --
    the URL it ended up serving on, the index it now provides -- so that
    nothing has to be told about it twice.

    They are written into ``$HMD_HOME/.config/hmd.env``, which
    ``internal/hmdenv`` already parses and layers, and which
    ``runner.Config.Extra`` already forwards into every projectbuilder deploy
    node. There is no second mechanism and no third file.

    **Declared where the credentials are declared.** A ``handback`` list in
    ``instance_configuration``, read the way ``credentialsFrom`` reads
    ``credentials``. The manifest stays the one declarative source, and
    SPEC013 stays true: nothing is scanned and nothing enumerates candidates.
    Values interpolate against the extension's own layered lookup, so the URL
    is stated once and referred to rather than repeated:

    .. code-block:: yaml

        instance_configuration:
          url: http://registry.local.neuronsphere.io
          handback:
            - name: PYTHON_REGISTRIES
              merge: json-map
              key: neuronsphere
              value:
                url: "${NS_CONFIG_URL}/hmd/local/+simple/"
                publish: true

    **An explicit user value always wins -- but "wins" is not one rule.** The
    first draft of this SPEC said the handback writes a delimited, regenerated
    block, in the way ``router.UpsertServiceRoute`` already splices one route
    between markers without disturbing what is around it, and that a user's
    value replaces the contributed one outright. The block is right. The
    replacement is not, and ``NERD006`` SPEC008 found why while wiring the
    first consumer: every variable this mechanism actually has to write is
    shaped, and whole-value precedence destroys the shape.

    .. list-table::
       :header-rows: 1
       :widths: 16 32 52

       * - ``merge``
         - Variables
         - What an explicitly set user value does
       * - ``scalar``
         - anything
         - **Wins outright.** The block omits the name entirely, so the file
           holds exactly one assignment to it and there is nothing to reason
           about.
       * - ``json-map``
         - ``PYTHON_REGISTRIES``, ``TYPESCRIPT_REGISTRIES``
         - **Wins per key.** The contribution is merged into the user's map
           under ``key``, and only when that key is absent. Replacing the
           value would delete the JFrog index they configured.
       * - ``list``
         - ``GOPROXY``, ``HMD_LOCAL_IMAGE_PULL_REGISTRIES``
         - **Wins by position.** Their entries keep their order and stay
           first; the contribution is appended, and only when it is not
           already there. ``separator`` defaults to ``,``.

    "The user's value" is what the file says with the managed block removed,
    never the merged result of a previous run -- otherwise each apply
    re-absorbs its own output and the block grows without bound.

    **The block goes last, and that is load bearing.** Both readers of this
    file resolve a repeated name last-write-wins: ``hmdenv.Parse`` by map
    assignment in line order, python-dotenv by the same in
    ``resolve_variables``. A trailing block therefore beats a user's own line
    above it, which is correct for ``json-map`` and ``list`` -- the emitted
    value already contains their data -- and is exactly why ``scalar`` must
    omit rather than emit. A handback that silently overwrote a hand-set
    variable would be indistinguishable from the variable never having been
    set.

    The block is therefore **relocated** to the end on every write rather than
    replaced where it sits. ``hmd configure`` appends each new name through
    python-dotenv's ``set_key``, so a block left in place would sooner or later
    have a user's line below it and lose -- silently, and only for whoever had
    run ``configure`` most recently.

    **Written whole on every apply and every start**, so a variable belonging
    to an extension that is no longer declared disappears by absence, and
    declaring nothing rewrites the block empty. This is
    ``WriteControlPlaneVhosts``' property, applied to a second file for the
    same reason.

    **Everything outside the block survives byte for byte.** ``hmdenv.Parse``
    discards comments, blank lines, key order, ``export `` prefixes and the
    original quoting, so the writer splices raw text and never re-serialises
    the parsed map -- this is a file a person edits. It writes through a
    temporary sibling and renames, preserving the file's mode and creating at
    ``0600``; a half-written ``hmd.env`` is a broken toolchain rather than a
    broken variable. A file whose block is unclosed or doubled is refused with
    the reason, because guessing how far it runs would take every variable
    below it along.

    **A contributed value must be fully resolved.** python-dotenv expands
    ``$VAR`` in every value whatever the quoting; ``hmdenv.Parse`` expands
    none. A ``$`` surviving into the block would therefore mean two different
    things to the two readers of one file, so a value naming a variable nothing
    sets is refused at validation rather than quietly interpolated to nothing.

    **``status`` reports the block** -- each variable, its merge shape, its
    contributing instance, and whether the block carries it or it yielded to a
    value set outside. The last is the fact worth surfacing: a hand-set value
    is otherwise an invisible reason for a local index not being used.

    **One gap, reported rather than closed.** ``cmd/root.go`` loads ``hmd.env``
    once at command start, so a handback written during a start is not visible
    to that start. The lookup is not refreshed mid-run: it is captured in every
    service's compose config hash, and refreshing it would let two services in
    one start hash the same input differently. The write says which command
    picks the value up instead.

    **A handback carries no secret.** SPEC009 keeps credentials off disk;
    this SPEC writes a plain file. The two are consistent only because an
    extension is expected to be anonymous on the loopback side, with any
    credential living on its *upstream* side. That is a property to state,
    not to leave as an accident of the first implementation.

    It is also enforced rather than asserted. A ``Credential``'s resolved
    value is already unreachable from the interpolation a handback uses, so a
    ``${NS_SECRET_*}`` reference could only ever expand to nothing; the
    reference is refused at validation so that it is an error a reader can
    act on instead of an empty string they have to notice. A dummy loopback
    credential written literally is *not* refused -- ``NERD006`` SPEC008
    depends on one -- which is the whole distinction SPEC009 draws between
    naming where a secret lives and holding one.

.. spec:: Reserved names
    :id: HMD_CLI_NEURONSPHERE_NERD004_SPEC011
    :links: HMD_CLI_NEURONSPHERE_NERD004
    :status: proposed

    An extension may not take an instance name the substrate or the control
    plane already uses. ``manifest/reserved.go`` already refuses
    ``local-neuronsphere``, ``base-vpc``, ``environment-db`` and
    ``eks-cluster`` for environment manifests; the control-plane manifest
    additionally refuses the control plane's own instances --
    ``control-plane-vpc``, ``control-plane-db``, ``control-plane-graph`` --
    the bootstrap services ``hmd-ms-naming``, ``hmd-ms-artifact-lib`` and
    ``hmd-ms-deployment``, and the five bundled compose service keys
    ``proxy``, ``floci``, ``deployment-gui``, ``nsrunner`` and ``authd``.

    The last five are there because SPEC004 prefixes an extension's service
    keys with its instance name: an instance called ``proxy`` would key a
    service ``proxy-something``, which is harmless, but the *name* would read
    in ``status`` as though it were the control plane's proxy. Refusing it
    costs nothing and removes the ambiguity.

    Refused at validation, with the reason, rather than at start time.

    Container names are the same hazard one level down.
    ``compose.CheckOwnership`` already refuses to start when another
    ``HMD_HOME`` owns ``hmd_proxy``, because those names are not namespaced
    by ``HMD_HOME``. SPEC004's ban on ``container_name`` is what keeps an
    extension out of that trap: its containers are named from the compose
    project, which already carries the eight-hex ``HMD_HOME`` digest
    ``container.DefaultProject`` computes.

.. spec:: Verbs
    :id: HMD_CLI_NEURONSPHERE_NERD004_SPEC012
    :links: HMD_CLI_NEURONSPHERE_NERD004
    :status: proposed

    ``nsctl control-plane repo add|remove|list``, mirroring ``nsctl repo``
    against the control-plane manifest, and ``nsctl control-plane apply``,
    mirroring ``nsctl env apply``.

    **Editing the file and running apply is the same operation as using the
    verbs.** This is already the stated design of the environment manifest --
    *"the imperative verbs are wrappers, not a second way to say the same
    thing"* -- and it is worth restating rather than assuming, because a verb
    that writes state the file does not capture is how a declarative surface
    stops being declarative.

    ``nsctl control-plane status`` gains a section listing declared
    extensions, their resolved versions and version *sources*, whether each is
    running, and the URL each is reached at. The version source matters as
    much as the version: "0.1.4 from a working tree" and "0.1.4 from an
    artifact" are different facts, and ``NERD005`` SPEC002 makes the
    difference load bearing.

.. spec:: What this is not
    :id: HMD_CLI_NEURONSPHERE_NERD004_SPEC013
    :links: HMD_CLI_NEURONSPHERE_NERD004
    :status: proposed

    **This is not plugin discovery.** Nothing is scanned, enumerated or
    auto-registered. An extension is deployed because a manifest names it,
    and for no other reason.

    That is a deliberate reversal of the model ``docs/plugins/development.rst``
    described: Python entrypoints registering ``enabled()``,
    ``get_resources()``, ``prepare_hmd_home()`` and ``render_compose_yaml()``,
    discovered by iterating installed packages. ``nsctl`` implements none of
    it. ``manifest.Manifest`` still parses ``plugins`` and ``plugin_config``,
    preserves them verbatim on save, and reports them through
    ``Unsupported()`` so a user is told plainly that they had no effect --
    but it obeys neither.

    **This is not ``nsplugin.json``.** That the compose file is back does not
    bring the index back with it. A RepoClass already declares what it
    produces and consumes, in its BACON ``meta-data/manifest.json`` and its
    ``meta-data/resources/*.yaml``, and ``internal/repoclass`` reads exactly
    those. What an extension *runs* is the compose file; there is no third
    file restating what the other two already say.

    **This is not a deployment.** An extension is not a RepoInstance, gets no
    RepoInstanceDeployment, appears in no ChangeSet and is invisible to the
    Deployment GUI. That is a real limitation and the one thing lost by
    choosing compose over the DAG: a RepoClass cannot today depend on a
    control-plane extension *by resource*, because nothing submits the
    Resources it declares. See the open questions.

    **This is not a way to modify the default control plane.** The manifest
    adds services. It cannot remove ``hmd_proxy``, retarget Floci, or
    reconfigure the bootstrap services. An extension that needs the control
    plane itself to change is asking for a change to ``nsctl``.

Acceptance criteria
-------------------

The requirement is satisfied when:

- ``nsctl control-plane start`` with no ``$HMD_HOME/.config/control-plane.yaml``
  behaves exactly as it does today, deploying nothing extra and printing
  nothing extra.

- A manifest declaring one RepoClass whose tree carries
  ``src/local/docker-compose.extension.yml`` starts that RepoClass's
  containers in the control plane's compose project, named from the project
  and so carrying the ``HMD_HOME`` digest, and ``nsctl control-plane status``
  reports the instance with its resolved version, its version source and the
  URL it is reached at.

- The deployed extension is reachable at one URL from both the host shell and
  a sibling container on the platform network.

- An extension whose image cannot be pulled, whose tree is missing, or whose
  compose file breaks a SPEC004 rule is reported, skipped, and leaves every
  other container -- the control plane's own included -- running.
  ``nsctl env start`` still succeeds with that extension declared.

- ``nsctl env purge <slug>`` leaves it running, and its directory under
  ``$HMD_HOME`` intact.

- ``nsctl control-plane stop`` followed by ``start`` returns it to service
  with its state intact.

- Turning a component on in ``instance_configuration`` and re-applying starts
  one more container and leaves the running ones alone -- no purge.

- Removing the instance from the manifest and re-applying reports it as
  running-but-undeclared, does **not** destroy it, and removes its vhost.

Risks and open questions
------------------------

.. list-table::
   :header-rows: 1
   :widths: 12 38 50

   * - Level
     - Risk
     - Mitigation
   * - **High**
     - An extension shares the control plane's compose project, so a bad
       extension is one sort order away from aborting ``hmd_proxy``.
     - SPEC006: a separate project object and ``UpEach``, with the core
       services keeping ``Up``'s abort-on-first-failure.
   * - **High**
     - A delivered credential reaches disk without anything reporting it,
       because ``docker cp`` writes underneath a tmpfs rather than into it.
     - Delivery goes through an exec in the container's own mount namespace
       (SPEC009), and the acceptance gate is ``docker diff`` plus a recursive
       search rather than a reading of the code.
   * - **Medium**
     - The Go port of the keychain probe order drifts from
       ``hmd-cli-tools``, and nsctl reports a credential missing that uv can
       see.
     - The order is pinned by a test that quotes the Python it came from, and
       a manual parity test reads one real keychain item with both.
   * - **Medium**
     - An extension is invisible to the deployment graph, so a RepoClass
       cannot depend on one by resource -- which ``NERD006`` SPEC001 assumed
       it could.
     - Stated in SPEC013 rather than left to be discovered. Whether a
       post-bootstrap resource submission is worth adding is an open question
       below.
   * - **Medium**
     - Reaching an extension by name costs an ``/etc/hosts`` entry, which
       ``nsctl`` cannot write. A user who skips it sees a name that does not
       resolve and no obvious cause.
     - ``control-plane start`` prints the exact line. Open question whether
       ``CheckHostsEntries`` should also *warn* for declared extensions, as
       it errors for ``neuronsphere``.
   * - **Medium**
     - Two manifests -- environment and control-plane -- with one schema
       invites declaring a thing in the wrong one. The symptom (lost on
       purge, or sixteen copies) appears long after the mistake.
     - SPEC002 states the choice explicitly, and ``status`` reports which
       manifest an instance came from.
   * - **Medium**
     - Interpolation and profiles turn out not to be enough, and extension
       authors work around it with progressively stranger compose files.
     - SPEC005 records the generator as considered and deferred rather than
       rejected, with "a concrete case that defeats both" as the bar for
       reopening it.
   * - **Low**
     - The control-plane manifest could grow into a second BOM seeder, with
       a built-in catalogue -- exactly what ``internal/bom`` was written to
       avoid.
     - SPEC013's "nothing is scanned" is the boundary. Any list of known
       extensions inside ``nsctl`` violates it.

Settled by this revision:

- **How a control-plane extension deploys.** As containers, from the
  RepoClass's ``src/local`` compose file, merged into the control plane's
  compose project. SPEC003 and SPEC004.

- **Whether ``control-plane apply`` exists separately from ``start``.** Both;
  ``start`` applies, ``apply`` is the fast path that converges extensions
  alone. SPEC003.

- **What ``nsctl`` does when an extension fails to deploy.** Reports it,
  skips it, and carries on -- warning from ``start`` so an environment start
  cannot be taken down by it, exiting non-zero from ``apply`` so a script
  notices. SPEC006.

- **How SPEC009 resolves a credential without a host Python.** The probe order
  is ported to Go, shelling out to the platform's own keychain client. The
  order is pinned by a test quoting its source, and the port was checked
  against the Python on a real keychain item.

Open, to be settled later:

- **Whether an extension should submit the Resources it declares** once
  ``hmd-ms-deployment`` is up, so a RepoClass can depend on a package index
  by resource rather than by hostname. It would mean an extension is a
  compose deployment *and* a graph record, and the second half can only exist
  after the bootstrap -- which is exactly the ordering SPEC003 rejected. The
  cheap version is a post-bootstrap best-effort submission that nothing waits
  on.

- **The CoreDNS leg of SPEC007.** A pod resolves an extension's hostname only
  through a ``coredns-custom`` record, and that record is written per
  environment during ``env start`` -- so it lands in the environment path
  rather than in ``control-plane start``, and is deferred.

- **Whether ``hmd_proxy`` should gain a tmpfs mount unconditionally**, or
  only when an extension declares it needs one. Unconditionally is simpler
  and costs a few kilobytes; conditionally means the bundled compose file
  changes shape based on an extension, which is a coupling this document
  otherwise avoids.
