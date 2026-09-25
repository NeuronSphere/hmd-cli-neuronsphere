.. NERD002 nsctl -- a Standalone Go CLI for the Local NeuronSphere

NERD002 nsctl -- a Standalone Go CLI for the Local NeuronSphere
===============================================================

.. req:: Deliver the local NeuronSphere as a single Go binary requiring only Docker
    :id: HMD_CLI_NERD002
    :status: implemented

    The extend-mode local platform currently reachable only through
    ``hmd neuronsphere`` shall be delivered as ``nsctl`` -- a single
    statically-linked, cross-platform Go binary modeled on ``kubectl``, whose
    only prerequisite is Docker. ``nsctl`` presents a purpose-built command
    surface (``nsctl env start|stop|add|delete|purge``, ``nsctl load-plugin``)
    rather than mirroring the Python CLI's, and adds a control-plane DAG-runner
    service so ``hmd-ms-deployment`` submits deployments to a workflow engine
    locally exactly as it submits them to Argo in the cloud. The
    ``nsplugin.json`` format, the BACON ``manifest.json`` contract, and the
    ``$HMD_HOME`` on-disk layout are unchanged (amended 2026-09-25: SPEC005
    withdrew ``nsplugin.json`` and ``nsctl load-plugin`` with it; ``nsctl``
    reads no plugin index, and the format survives only in the Python CLI).
    The Python ``hmd neuronsphere`` surface coexists throughout the port. All other ``hmd-cli-*`` packages,
    all ``hmd-ms-*`` microservices, and all ``hmd-inf-*`` repos remain Python.

    *Implemented 2026-09-07, and the gate was run rather than argued.* A machine
    with ``HMD_REPO_HOME`` pointed at an empty directory, a purged
    ``$HMD_HOME``, and an empty deployment graph bootstrapped a control plane
    and brought up an environment -- cluster, database and a working External
    Secrets operator -- entirely from the repo trees inside the binary. That is
    this requirement's own sentence, and until it had been run it was an
    argument. See `Verified from nothing`_.

    One thing qualifies it. **The inverted submit path does not work**, for a
    reason now known exactly (SPEC010) -- it is opt-in, was always opt-in, and
    the default path is the one every deploy above took.

    The registry qualifier is **withdrawn** as of 2026-09-14: one registry now
    serves all three backend images, and a fresh install has been run end to end
    against them. See item 22 and `A second cold start, from a genuinely empty
    HMD_HOME`_ -- which also found that the claim had been false in four further
    ways nobody had tested, every one of them hidden by state a working machine
    already has.

.. note::

    **This revision withdraws the original NERD002 scope.** The first version of
    this proposal ported all 40 ``hmd-cli-*`` repositories into a single Go
    ``hmd`` monorepo with 100% command-line compatibility, on a ~36-week plan.
    That scope was wrong for the goal it claimed. Adoption is gated by the local
    platform -- the thing a newcomer runs first -- not by ``hmd bartleby``,
    ``hmd bender``, or ``hmd repo``, each of which shells out to a Python tool
    anyway and so cannot make anyone's machine Python-free. Preserving the
    ``hmd`` surface verbatim also preserved a surface designed for existing
    users, at precisely the moment the point was to serve new ones. This
    revision ports one thing, gives it a surface built for the audience, and
    leaves the rest of the ecosystem alone.

Implementation Status
---------------------

All six planned phases have landed on ``feat/nsctl-go-cli``, and three
unplanned ones with them -- the cold start, the verification pass, and the local
identity provider. ``nsctl`` bootstraps a
control plane and an environment from an empty ``HMD_HOME`` and an empty
deployment graph -- which it could not do until the graph was genuinely empty
for the first time; see `The cold start had never run`_ -- starts and stops
environments, reconciles them to a manifest, and coexists with the Python CLI
against one ``HMD_HOME``. Deployments are submitted to a DAG-runner service rather than run
inside the CLI, so they outlive the command that started them, and the runner
schedules their nodes on the dependency edges the payload has always carried
rather than one at a time. It ships through a Homebrew tap, an install script
and GitHub Releases, and the documentation recommends it.

**And it has been run from nothing.** Every claim in this table was made from
unit tests until 2026-09-07; the notes below now distinguish what was built from
what has been exercised against a live platform with no ``HMD_REPO_HOME``, no
``$HMD_HOME`` and no deployment graph. Six more defects came out of doing that,
recorded in `What implementation changed about the design`_ and
`Verified from nothing`_.

.. list-table::
   :header-rows: 1
   :widths: 12 30 58

   * - SPEC
     - Status
     - Note
   * - 001
     - implemented
     - The surface is complete. The ``repo`` verbs and ``env apply`` were added
       to it, ``load-plugin`` withdrawn.
   * - 002
     - **amended**
     - The lifecycle is implemented, ``env purge`` included, and both have been
       run from nothing. "Cold bootstrap included" was wrong until 2026-09-07:
       five seeding steps had never run, and three more defects surfaced when
       the six items of that day were verified. ``env purge`` itself was wrong
       the first time it ran; see item 19.
   * - 003
     - implemented
     - Cross-checked against the literal JSON the Python CLI writes.
   * - 004
     - implemented
     -
   * - 005
     - **withdrawn**
     - Plugin discovery is not implemented in any form. See the SPEC.
   * - 006
     - **amended**
     - The compose file, the runner sources and ten repo trees are embedded,
       and version resolution prefers a bundled tree over a checkout. **Run
       from nothing:** a control plane and an environment deployed from the
       bundled trees alone, with ``HMD_REPO_HOME`` pointed at an empty
       directory. Seven of the ten still travel as committed trees, undeclared
       as ``pre_build_artifacts``; ``hmd-inf-neptune`` no longer does -- it was
       verified to publish a consumable ``build`` artifact on 2026-09-11 and is
       pinned at ``0.3.36``. Whether the remaining seven do is *still*
       unverified. See item 22.
   * - 007
     - implemented
     - EC2 is not used at all -- ``hmd-vpc`` deploys the network instead,
       which removed 24 MB from the binary.
   * - 008
     - **amended**
     - Two of its routes were wrong; the conclusion holds. See the SPEC.
   * - 009
     - **amended**
     - Its seeding sequence was incomplete: resource definitions, the
       DeploymentSet payload, and the two-phase apply that submits the concrete
       core Resources. See the SPEC.
   * - 010
     - **amended**
     - The submit envelope is Argo's; its contents are not. The CLI-submits
       path is what every deploy in this document took and it works. The
       **service-submits path is wired and does not work** --
       ``HMD_WORKFLOW_RUNNER_URL`` is set and reconciled in both directions, the
       async self-invocation is reached, and it dies on a ``None``
       ``Authorization`` header before submitting anything. Exact cause in the
       SPEC; the fix is in ``hmd-ms-deployment``, is written and unit-tested as
       of 2026-09-08 at the three sites that share the literal, and has not been
       run end to end. It is opt-in and always was.
   * - 011
     - **amended**
     - Implemented. One clause of it is not implementable as written, and
       concurrency needed a bound the DAG does not express -- see the SPEC.
   * - 012
     - partly implemented
     - The harness exists for the core SPEC012 states -- ``env start``,
       ``env stop``, ``status``, ``env purge`` and the registry -- plus
       ``env add``/``env delete`` and the slug rules, added and run on
       2026-09-08. ``env apply``, ``env attach``, the ``repo`` verbs,
       ``control-plane`` and ``authd`` are still checked by hand. ``env purge``
       was added deliberately and at a cost the SPEC now records: each direction
       destroys and rebuilds the environment, so a run costs two cold starts.
   * - 013
     - **amended**
     - Implemented. Windows is dropped and the Docker image is built rather
       than published. See the SPEC.
   * - 014
     - **amended**
     - The HIGH risk is retired. Of its three gaps, one is **closed**:
       ``BUCKET_NAME`` is derived here, and the cross-repo edit the SPEC
       prescribed would have been wrong (item 16). The container-name collision
       is still detected and not fixed, deliberately. The environment-name
       collision (item 17) is **closed** since 2026-09-08 and its
       two-environment verification run. The unpublished backend images
       (item 22) remain, fixable only in the image repositories.
   * - 015
     - **amended**
     - Phase 3's content changed substantially once plugins were dropped.
   * - 016
     - implemented
     - Added after the fact: the local identity provider was built, shipped and
       documented in ``docs/nsctl.rst`` before this proposal recorded it at all.
       Its one gap -- ``hmd-lib-auth.verify_token`` demanding an ``https://``
       issuer -- is named in the SPEC.

What implementation changed about the design
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Twenty-two things this proposal asserted turned out to be wrong, and are
corrected in the SPECs rather than quietly worked around. The first fifteen came
out of building it; items 16 to 22 came out of `Verified from nothing`_, and
five of the seven were invisible until a genuinely cold platform ran them.

1. **``hmd-img-projectbuilder`` ships no ``kubectl``** (SPEC008). Every
   published tag was checked. Kubernetes work execs into the k3s node instead,
   which launches no container and removes projectbuilder as a prerequisite for
   provisioning a cluster.
2. **``docker compose`` is not needed either** (SPEC008). Three containers
   described by one embedded file are materialised through the Docker Engine
   API, so ``docker`` is genuinely the only host binary.
3. **Plugin discovery should not be ported at all** (SPEC005). A RepoClass
   already declares what it needs and produces, in the files the cloud deploy
   path reads. The parallel inventory is the copy that goes stale -- and did.
4. **A second Go module was the wrong shape for the runner** (SPEC010). Go
   scopes ``internal/`` per module, so ``src/go/nsrunner`` could not have
   imported ``internal/runner`` and would have had to duplicate node execution
   or force it public. It ships as ``nsctl runner serve`` in the one module
   instead, as an image of the same binary -- ``docker:cli``, not the scratch
   base first proposed, since ``internal/container`` shells out to the Docker
   CLI and on scratch every deploy node dies with ``exec: "docker": executable
   file not found in $PATH``.

5. **The control plane's bootstrap does not need to run on every start.** The
   Python runs it each time because the work is idempotent; ``nsctl`` runs it
   only when the control plane has not been bootstrapped, and a warm start
   recovers what it needs from Floci's persistent state.
6. **Fail-fast cannot be a context cancellation** (SPEC011). Node execution
   runs ``docker run`` under ``exec.CommandContext``, so cancelling the context
   the nodes execute under kills them where they stand -- which is exactly the
   half-applied ``terraform apply`` the "let in-flight nodes finish" half of
   the same sentence exists to avoid. Dispatch is gated on the scheduler
   instead, and the node context is left alone.
7. **``windows/amd64`` was never a shippable target** (SPEC013). Shelling out
   to ``docker``, mounting ``/var/run/docker.sock``, and composing host
   absolute paths that a sibling container resolves identically are three
   things Windows has no equivalent for. The archive would have installed and
   then failed at the first command that touched Docker.
8. **The runner image is built, not published** (SPEC013). A published tag was
   a promise the repository was not keeping -- the compose default named one
   that does not exist -- and publishing it would still not have let a
   Homebrew install build a modified runner. ``nsctl`` carries its own sources
   and builds the image on demand instead.
9. **The seeding sequence in SPEC009 is incomplete** (SPEC009). Resource
   definitions must be registered before anything declares producing one, a
   DeploymentSet needs a base64-JSON ``definition``, and the concrete core
   Resources have to be submitted between two changesets. All three are
   invisible on a graph something else has already seeded.
10. **Declaring a produced type does not satisfy a tag_selector** (SPEC009). A
    selector matches concrete Resources. This is why an apply is two
    changesets rather than one: the Resources attach to a deployment that only
    the first changeset creates.
11. **In-degree scheduling is not a sufficient concurrency bound** (SPEC011).
    Nodes of one RepoClass share a working tree, and the DAG has no edge to
    express it. The scheduler serialises per class.
12. **The control plane cannot submit to the runner** (SPEC010, Phase 4).
    ``hmd-ms-deployment`` reads ``HMD_WORKFLOW_RUNNER_URL`` and nothing sets
    it, so the inversion this phase exists for has never run. Phase 4 recorded
    it as a met deliverable. *Wired since:* ``floci.ServiceEnv`` sets it for
    that one service, gated on the runner being enabled, and a warm start
    reconciles it onto an already-deployed Lambda.

13. **The Python's purge deletes Floci's resources after stopping Floci**
    (SPEC001, SPEC002). ``stop_neuronsphere_extend`` removes the ``floci``
    container and *then* asks Floci to delete the control plane's RDS instance
    and graph, so both calls fail against a dead endpoint -- every purge warns
    ``Could not connect to the endpoint URL "http://localhost:4566/"`` and
    leaves their containers and volumes behind. Forty-nine stale ``floci-*``
    volumes had accumulated on one machine. ``nsctl env purge`` deletes first
    and stops afterwards, which is the whole shape of the verb; a name sweep
    afterwards clears what earlier purges left.

14. **Version resolution had the working tree in the wrong tier** (SPEC006).
    ``ResolveVersion`` put a checkout above the version a BOM entry declared,
    unconditionally, where ``bom_seeder.resolve_repo_version`` only prefers a
    tree when the developer explicitly asks -- by a ``=local`` pin or
    ``HMD_LOCAL_NEURONSPHERE_PREFER_LOCAL_VERSIONS``. Its own test encoded the
    divergence: the third case was named "then the declared version" and
    asserted the tree won. Corrected with the bundled tier, where it matters,
    because a bundled tree that a stray checkout silently shadowed would be
    worse than no bundled tree at all.
15. **Injecting per-environment values only inside ``Seed`` makes them read as
    drift** (SPEC009). ``InjectEndpoints`` runs inside ``Seed``, while the
    reconcile snapshot hashes the desired state composed before it. Anything
    injected in one place and not the other is hashed pre-injection and never
    matches what was seeded. ``change_set_builder`` calls
    ``_inject_floci_account`` in *both* places for exactly this reason and says
    so -- "Omitting it left ext-secrets permanently drifted" -- which is the bug
    the new ext-secrets account injection would have reproduced.

16. **A librarian's bucket is derived, not declared** (SPEC014). SPEC014
    recorded ``hmd-ms-artifact-lib``'s ``BUCKET_NAME`` as a gap only another
    repository could close, named the exact edit -- a literal under
    ``deploy.default_configuration`` -- and said "nothing in this one can close
    it". Both halves were wrong. The cloud never declares that name:
    ``librarian_base.get_full_bucket_name`` composes it from the librarian's own
    required ``lib-repo`` dependency on ``hmd-inf-s3bucket``, and
    ``hmd-inf-s3bucket``'s own stack names the bucket ``make_standard_name`` of
    the same six parts. A literal would have been a *second* source of truth for
    a value that already has one, free to drift from the bucket that actually
    gets created. ``floci.LibrarianBucketName`` derives it from the manifest, so
    the gap closed here. The half SPEC014 got right survives: a librarian
    declaring no bucket dependency still gets a warning and no guess.

17. **Only an environment named** ``local`` **can deploy anything** (SPEC014).
    ``ms-deployment`` passes an environment's own slug as
    ``hmd deploy --environment``, because a local ``Environment`` entity is
    typed by its slug -- deliberately, in *both* front ends, so identical
    instance names stay safe across environments. ``hmd-lib-cdktf`` then reads
    that same value as the AWS deployment *tier*:
    ``use_path_style=True if self.environment == "local" else None``. Any other
    name therefore gets virtual-host S3 addressing against a Floci that
    publishes no per-bucket DNS, and the environment's first CDKTF node dies in
    ``tofu init`` with ``no such host`` for the tfstate bucket. One name doing
    two jobs. Not introduced by this port -- the Python seeds the identical
    ``Environment.type``. It had simply never been tried, because every local
    environment anyone had deployed was called ``local``.

    *Fixed 2026-09-08, in ``hmd-lib-cdktf``.* ``is_local_environment()`` is one
    predicate and ``HmdCdkTfStack.is_local`` is resolved from it once; the
    seventeen sites that compared the environment's *name* now ask it instead.
    It is true when the environment is named ``local`` **or**
    ``AWS_ENDPOINT_URL`` is set -- the first clause keeping every environment
    that works today bit-identical, the second being the fix.
    ``AWS_ENDPOINT_URL`` is what those callers are really asking about, is set
    by both front ends for deploy nodes, and is set by no cloud path.
    ``HMD_ENVIRONMENT`` was the obvious alternative and is the wrong one: it is
    commonly ``local`` in a developer's ``hmd.env``, so keying off it would make
    ``hmd deploy --environment dev`` from a laptop take local branches against
    real AWS.

18. **The kubeconfig has to be written between the two changesets** (SPEC009).
    ``Start`` knows the cluster "may only now exist" and re-runs
    ``startCluster`` for that case -- but after ``Apply`` returns, and the
    cluster is created inside Apply's *first* phase and consumed by its second.
    So Phase B's first Helm chart ran against a kubeconfig nothing had written,
    Docker created the missing bind-mount source as a directory, and the deploy
    died with ``IsADirectoryError: [Errno 21] Is a directory:
    '/root/.kube/config'``. The fifth cold-start defect had taught
    ``WriteKubeconfig`` to replace such a directory, which is the right repair
    and did not help: nothing called ``WriteKubeconfig`` at all.

19. **The purge was wrong the first time it ran** (SPEC002). ``env purge`` had
    been unit-tested and never executed. Its first real run left seven
    containers, three volumes and the platform network behind, and the three
    failures were one failure with two consequences: ``floci-ecr-registry`` is
    not one of the three Floci services the purge names, so it survived holding
    both a volume and a network endpoint, and the volume sweep and network
    removal failed behind it. ``RemoveVolumes`` then returned on the first
    volume it could not remove -- a sweep whose stated purpose is "the volumes
    an earlier purge could not reach", stopping at the first volume it could not
    reach. And ``compose.Runner.Remove``, whose comment reads "Only a purge does
    this", was never called: ``PurgeAll`` went through ``Stop``, leaving four
    control-plane containers Exited. What item 13 got right is that the
    *ordering* holds: no purge has ever warned ``Could not connect to the
    endpoint URL``.

20. **The deployment graph outlives an environment purge** (SPEC002, SPEC009).
    The graph lives in the control plane's Postgres, so ``env purge <name>``
    destroys an environment's resources and leaves every
    ``RepoInstanceDeployment`` reading ``DEPLOYED``. Purge, ``env add``,
    ``env start`` planned "3 unchanged" and never recreated the database -- two
    lines below its own warning that no database existed. ``K3sMissing`` already
    forced the cluster back into the plan for this exact reason; nothing did the
    same for anything else. The manifest survives a purge too, in
    ``$HMD_HOME/environments/``, and is silently re-adopted by a later
    ``env add`` of the same name.

21. **The service-submits path fails on a header, not on Floci** (SPEC010,
    Phase 4). Phase 4's *Not done* named ``call_deploy_change_set_deployment``
    as the expected obstacle and guessed the reason would be Floci's Lambda
    emulation. It is not. ``apply_changeset`` self-invokes with a hand-built
    event whose headers are ``{"Authorization": auth_token}``; locally there is
    no auth, so the value is ``None``, and Mangum's API Gateway handler does
    ``[[k.encode(), v.encode()] for k, v in headers.items()]`` --
    ``AttributeError: 'NoneType' object has no attribute 'encode'``. Because it
    is an ``Event`` invocation nothing surfaces: ``apply_changeset`` has already
    returned 200 and the CLI waits out its full timeout for a workflow that was
    never going to exist. A cloud caller passes a real token, so it is
    local-only, and the fix belongs in ``hmd-ms-deployment``. It is **three**
    lines, not the one this said: the same literal builds the destroy path's
    event and the ``callback_url`` POST. See SPEC010.

22. **"Only Docker" is also a claim about the image registries** (SPEC014).
    **Closed 2026-09-14.** The compose file used to pin
    ``hmd-postgres-base:${HMD_POSTGRES_BASE_VERSION:-stable}``,
    ``hmd-img-gremlin-server:0.3.5`` and ``hmd-img-k3s-floci:0.3.2`` under
    ``${HMD_LOCAL_NS_CONTAINER_REGISTRY:-ghcr.io/neuronsphere}``, and no single
    registry served all three: the Postgres pin existed only under the
    ``ghcr.io/neuronsphere`` default, the gremlin and k3s pins only under
    ``ghcr.io/hmdlabs``.

    The premise was wrong rather than the arithmetic. ``ghcr.io/neuronsphere``
    was a mirror of ``ghcr.io/hmdlabs`` carrying only the released subset, and it
    had gone stale; ``hmdlabs`` is where images are built and tagged, and its
    packages are now public. The defaults name it, and the versions are explicit
    patch pins because ``hmdlabs`` publishes a moving ``latest`` and no
    ``stable``. Two of the old defaults -- the gremlin server and the
    projectbuilder's ``:stable`` -- named tags that had never existed anywhere.

    What was left was ``hmd-postgres-base``, whose only ``hmdlabs`` artifact was
    an ``amd64``-only leaf: its index tags were listed but resolved to no
    manifest, so an arm64 host had nothing to pull. Its ``main`` also carried a
    ``postgres:14-alpine`` bump that had been committed and never built, so
    *every* published tag in *either* registry still shipped 12.9. Rebuilding it
    as ``0.3.12`` produced both a multi-architecture image and the postgres 14 the
    repository had specified for years -- which is a major version bump over
    existing data directories, and is why ``internal/pgcheck`` now exists.

    The guard that used to *refuse* on an uncached image made this worse rather
    than surfacing it, and now pulls; the refusal survives for a reference that
    genuinely resolves nowhere.

A second cold start, from a genuinely empty HMD_HOME
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

`The cold start had never run`_ describes a platform with no *deployment graph*.
It still ran on a developer's machine, with that machine's ``hmd.env``, its
Docker image cache and its ``HMD_REPO_HOME``. On 2026-09-14 the remaining three
were taken away too: a new ``HMD_HOME``, no repositories, and an environment
scrubbed to ``PATH``, ``HOME`` and ``HMD_HOME``. Five more defects, and the
shape is the same one as before -- every single one was hidden by state that any
machine which has run the platform already has.

1. **The default image registry named a mirror that had gone stale.** Item 22,
   above, in full. Invisible because a developer's ``hmd.env`` sets
   ``HMD_LOCAL_NS_CONTAINER_REGISTRY`` and so never reaches the default.

2. **The Engine API cannot pull an image.** ``ImagePull`` sends only the
   credentials in ``PullOptions``; the daemon never reads
   ``~/.docker/config.json``, because resolving credentials is the CLI's job. So
   the compose runner's pull was credential-less, and ``ghcr.io`` answers a
   credential-less request with ``unauthorized`` -- for a *public* image, one
   that ``docker pull`` fetches anonymously without complaint. Invisible because
   the image is in the cache of every machine that has run the platform, and
   ``EnsureImage`` returns early when it is. It pulls through
   ``container.PullImage`` now, and ``ImagePull`` is gone from the narrowed
   ``dockerAPI`` so the broken path cannot be reached back for.

3. **``env start`` refused on a fresh home -- after bootstrapping it.** A
   synthesized registry holds no environments, so the default slug resolved to
   nothing; but only once ``environment.Start`` was reached, past the entire
   control-plane bootstrap, with a remedy the user had no way to know to run
   first. The command now settles which environment it is about before anything
   starts. Invisible because every developer's registry already had ``local`` in
   it.

4. **A stopped control plane blocked another ``HMD_HOME`` permanently.** The
   ownership guard counted containers that merely existed, and
   ``control-plane stop`` stops rather than removes -- so doing exactly what the
   refusal instructed left the names held and the next start refused again, with
   nothing left to try short of ``docker rm``. Only running containers count
   now. Invisible because it takes two ``HMD_HOME``\ s to see, and until this
   run there had only ever been one. Taking turns is as far as this goes;
   running two at once is :doc:`NERD007 <NERD007_Concurrent_Control_Planes>`.

5. **The Docker preflight was written and never called.** ``Available`` reports
   a missing binary or an unresponsive daemon in the words someone chose for
   exactly that moment, and ``grep`` found no caller. Invisible because the
   machine developing a Docker CLI has Docker.

The lesson is the one the previous section drew, and it did not take the second
time: *a guard that finds what it needs already present does not exercise the
code that would have created it.* Every defect here is a default, a fallback or
a refusal -- the branches a working machine never takes. They are only testable
by taking the machine's state away, and taking away "the graph" was not the same
as taking away "everything".

The cold start had never run
~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Every claim this document made about bootstrapping from nothing was untested
until 2026-09-07, and five of them were false. The reason is worth recording,
because it is not a coding mistake and the same shape will recur.

``nsctl`` was developed against machines where ``hmd neuronsphere up`` had run
at some point. The deployment graph is durable -- it lives in the control
plane's Postgres, and survives ``env delete``, restarts, and everything short
of ``down --purge`` -- so the rows the Python CLI seeds *stay seeded*. Every one
of nsctl's seeding steps is guarded find-first, which is correct and is exactly
what hid the gap: a step that finds what it needs already present returns
without exercising the code that would have created it. The first genuinely
empty graph found all five in sequence, each one only reachable once the
previous was fixed:

1. **Resource definitions were never registered.** ``declare_produces_resource_definition``
   was called for types nothing had created (SPEC009, amended).
2. **A DeploymentSet was created with the wrong payload** -- no ``definition``,
   and an ``environment`` field the entity does not have.
3. **The concrete core Resources were never submitted at all**, so a dependency
   carrying a ``tag_selector`` could not resolve (SPEC009, amended).
4. **nginx capped a runner submission at 1 MB**, which 28 instances exceed.
5. **A Docker-created directory sat where the kubeconfig belongs**, because a
   deploy mounted the path before the cluster had written it.

Three of these are ordering: the graph has a shape that must be built up in a
particular sequence, and a warm graph already has it. The lesson for the rest of
this port is that "verified against a running platform" and "verified from
nothing" are different claims, and only the second one tests a bootstrap.

Verified from nothing
~~~~~~~~~~~~~~~~~~~~~

`The cold start had never run`_ ends with the lesson that "verified against a
running platform" and "verified from nothing" are different claims, and only the
second tests a bootstrap. Six items landed on 2026-09-07 -- the runner URL,
``env purge``, container-ownership detection, the bundled repo trees, the two
SPEC014 gaps and the parity suite -- each unit-tested, none of them ever run.
This section records making the second claim, and what making it cost.

**The setup, because it is the whole of the claim.** ``HMD_REPO_HOME`` pointed
at an empty directory, overridden in the process environment rather than merely
unset, because ``$HMD_HOME/.config/hmd.env`` sets it. ``nsctl env purge`` with
no name first, so the control plane, the environment and the deployment graph
were all genuinely absent. Then ``control-plane start``, ``env add``,
``env start``. Nothing borrowed from the platform that had been there.

**What passed.**

- The control plane bootstrapped from bundled trees alone: ``hmd-vpc``,
  ``hmd-postgres-rds``, ``hmd-inf-neptune`` and the three foundation Lambdas,
  with no checkout of any of them on the machine. This is the requirement's own
  sentence and it had never been executed.
- The environment's substrate followed: core instance, ``base-vpc``,
  ``environment-db`` and ``eks-cluster``, again from bundled trees.
- **External Secrets came from the code, not from a manifest.** The environment
  was created by ``nsctl env add`` and had no manifest at all, so the two
  ext-secrets entries could only have come from ``bom.ExtSecrets`` -- which is
  exactly the declaration that commit added and exactly what an environment
  whose manifest the Python CLI wrote would have hidden. Both
  ``ClusterSecretStore``\ s reconciled to ``Valid``/``Ready``, not to the
  ``InvalidProviderConfig`` that is this chart's silent local failure.
- The operator authenticates as the environment's own account:
  ``AWS_ACCESS_KEY_ID=000000000001`` in the deployed pod, against the control
  plane's ``000000000000``.
- ``HMD_WORKFLOW_RUNNER_URL`` is set on ``hmd-ms-deployment`` and on nothing
  else, and ``syncRunnerSelection`` reconciles it in **both** directions across
  warm starts -- toggled off, the variable goes and the service says it deploys
  in-process again; toggled on, it returns.
- ``compose.CheckOwnership`` produced no false refusal across a dozen starts.
- SPEC014's ``BUCKET_NAME`` gap closed: the derived bucket
  ``lib-repo-hmd-inf-s3bucket-cp-local-reg1-hmdtr1`` is created in Floci and set
  on the deployed Lambda, and the warning no longer fires.

**What failed, and is why this section exists.** Items 17 to 22 of
`What implementation changed about the design`_, plus four failures in the
parity suite's first complete run, three of which were defects in the suite
itself (SPEC012). Two were invisible for the
reason the earlier section names -- a warm graph and a warm ``$HMD_HOME``
already had what a cold one has to create. Three were verbs that had only ever
been unit-tested. One, the unpublished images, is not a defect in this
repository at all and could not have been found any other way.

**What could not be verified here, and why.** Two of the three, now: the second
environment was blocked on item 17 and has been run since it was fixed.

- **A second environment.** *Blocked then; run on 2026-09-08, once item 17 was
  fixed.* Two environments up at once, ``dev2`` and ``dev3``, each with no
  manifest so their ext-secrets entries came from ``bom.ExtSecrets`` alone::

      dev2  account 000000000002   pod AWS_ACCESS_KEY_ID 000000000002   store secret 000000000002
      dev3  account 000000000003   pod AWS_ACCESS_KEY_ID 000000000003   store secret 000000000003

  Checked in **both** places the code distinguishes -- the pod's environment and
  the rendered ClusterSecretStore Secret, which is the operative one because a
  ``secretRef`` store signs with what the chart rendered and never consults the
  pod's environment -- and both stores reconciled to ``Valid`` in each cluster.
  This is the check that catches nothing when it passes and everything when it
  fails: one Floci serves every account and reads the account off the key, and
  the secret names are identical across environments, so a shared key would
  have meant one environment silently reading the other's secrets.
- **The legacy-Traefik warning.** It fires only on a cluster predating the
  baked-in ingress, carrying the Helm release the Python installed at runtime.
  The purge destroyed the only such cluster on this machine. Planting an
  ``owner=helm,name=traefik`` Secret would prove the grep works and nothing
  else, so it was not done: the detection is unit-tested and unverified in
  situ, and saying so is worth more than a synthetic pass.
- **Artifact resolution for the eight unfetched bundled classes.** Probing them
  with ``hmd build --prebuild-download-only`` returns ``Unauthorized``: the
  librarian wants credentials. Pinning them blind is the thing that breaks
  ``hmd build`` for everyone, so they stay as committed trees and this stays
  open (item 22 in SPEC006's note).

A real cold bootstrap, measured
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Phase 5's *Not done* said nothing in the port measured a real cold bootstrap's
speedup, only a synthetic DAG of the same shape, and predicted the real gain
would be smaller because the real one is dominated by container pulls and
Terraform. It was not measurable until a cold start worked from nothing at all.

Two complete runs, identical but for ``HMD_LOCAL_RUNNER_PARALLELISM``, each
starting from ``nsctl env purge`` with no name -- no control plane, no
environment, no deployment graph -- and ending with a reconciled environment.
Same machine, same binary, same warm image cache.

.. list-table::
   :header-rows: 1
   :widths: 34 22 22 22

   * - Phase
     - Parallelism 4
     - Parallelism 1
     - Difference
   * - Control-plane bootstrap
     - 132 s
     - 145 s
     - 13 s (noise)
   * - Environment (4 substrate nodes + 2 declared)
     - 223 s
     - 237 s
     - 14 s (6%)
   * - Total
     - 355 s
     - 382 s
     - 27 s (7%)

**The prediction was right and the number is unflattering.** The environment
phase -- the only one the scheduler touches -- saves **14 seconds out of 223, or
six per cent**. Against the synthetic DAG's 8 s against 21 s, a 62% saving, that
is a different claim entirely.

Worse, and more usefully: the control-plane row is the same measurement with the
scheduler taken out of the picture, and it moved by 13 seconds between two runs
that differ only in a flag it does not read. So the run-to-run variance of a
cold bootstrap on this machine is about the size of the gain. Six per cent is
real -- it appears in the right direction, in the right phase, and a third run
of the earlier binary at the default parallelism landed at 213 s -- but a single
pair of runs cannot separate it from noise, and this document should say so
rather than quote 6% as though it had been isolated.

Two structural reasons, both of which the synthetic DAG could not have shown.
The control-plane bootstrap does not use the scheduler at all -- it runs through
``internal/runner`` in-process, because ms-deployment is its own last node --
so nothing in that phase can overlap however the flag is set; it is included
above only because a cold start pays for it and its variance is the honest
noise floor for the row below. And the environment's substrate is four nodes of
which three are chained: the core instance, then ``base-vpc``, then
``eks-cluster`` and ``environment-db`` in parallel. One overlapping pair is the
whole of the available concurrency, where the synthetic DAG had six independent
nodes and could show 8 s against 21 s.

The measurement stands as the correction to Phase 5's deliverable rather than as
an argument against the scheduler. Concurrency that saves a few seconds on a
four-node substrate is the same concurrency that will matter on a manifest of
twenty-eight, which is what an environment with workloads in it actually is --
but this document should not have been claiming a real speedup it had only ever
measured synthetically.

Motivation
----------

1. **Installation friction is the adoption barrier.** Trying the local
   NeuronSphere today requires Python 3.9+, pip, a virtualenv, and ~40 pip
   packages with transitive dependencies (cement, boto3, pyyaml, jinja2,
   requests, kubernetes, pg8000, inquirerpy, colorlog, python-dotenv), plus
   version alignment across all of them. Conflicts with system Python are
   routine. A single binary reduces this to "install Docker, download
   ``nsctl``".

2. **Most of the Python is already behind a container boundary.**
   ``LocalWorkflowRunner`` does not run ``hmd deploy`` on the host -- it runs
   each generated deploy script inside an ``hmd-img-projectbuilder`` container
   (``local_workflow_runner.py``, ``_execute_in_projectbuilder``). The Python
   that performs a deploy is therefore an implementation detail of an image,
   not a host prerequisite. What remains on the host is orchestration: Docker
   and Compose, k3s and Floci provisioning, nginx config generation, the
   environment registry, and the ms-deployment BOM protocol. That is the
   portable part, and it is the part a Go binary is good at.

3. **Startup latency.** Python interpreter startup plus importlib scanning of
   40+ distributions costs 1-3 s per invocation. A Go binary starts in under
   10 ms. This matters most for the read-only commands (``env list``,
   ``status``) that users run repeatedly.

4. **Ecosystem alignment.** Docker, Kubernetes, Helm, Traefik and Argo are all
   Go. The tools this CLI drives and the k3s cluster it provisions are Go
   artifacts, and so are the two Go binaries already in this repo family (see
   item 6).

5. **Parallel deploys become natural.** The local deploy manifest already
   carries dependency edges per node and the current runner ignores them,
   executing strictly sequentially. Go's concurrency primitives make a
   dependency-respecting worker pool a small amount of code (SPEC011).

6. **There is a precedent in this repo family.** ``hmd-cli-bartleby`` is
   already a Go rewrite of an ``hmd`` CLI plugin, shipped via
   ``brew install bartleby``, with its Python source retained alongside as
   legacy and its docs stating plainly that the Python CLI "has been replaced
   by the Go binary" and that "no Python runtime [is] required". ``nsx``
   (in the ``neuronsphere`` repo) is a second Go binary that today shells out
   to ``hmd neuronsphere up``. The conventions, the build pattern, and the
   distribution channel are established.

Scope
-----

**In scope:**

- ``hmd-cli-neuronsphere``'s **extend mode** orchestration, ported to
  ``nsctl``: environment lifecycle, control-plane lifecycle, Floci
  provisioning, k3s cluster and operator provisioning, nginx routing, the
  environment registry, BOM seeding and the ms-deployment apiop protocol,
  reconcile/delta-apply, and the environment manifest that replaces plugin
  discovery (SPEC005).
- A new **DAG-runner service** for the control plane (SPEC010), replacing the
  in-process ``LocalWorkflowRunner`` call with a submitted workflow.
- Parallel, dependency-respecting node execution (SPEC011).

**Out of scope (unchanged, remain Python):**

- All other ``hmd-cli-*`` packages. ``hmd build``, ``hmd deploy``,
  ``hmd bender``, ``hmd bartleby``, ``hmd repo`` and the rest are untouched;
  a developer contributing to NeuronSphere still installs them.
- All ``hmd-ms-*`` microservices, ``hmd-img-*`` images, ``hmd-inf-*``
  infrastructure repos, and ``hmd-lang-*`` language packs.
- ``hmd-img-projectbuilder``, which continues to execute every BOM node's
  deploy script. ``nsctl`` inherits its contract rather than replacing it.

**Explicitly not ported -- legacy Platform mode.** ``nsctl`` implements extend
mode only. ``HMD_LOCAL_NEURONSPHERE_MODE=platform`` remains available through
the Python CLI for as long as it is supported there. Excluding it removes
roughly 1,100 lines from the port: ``start_neuronsphere_platform`` /
``stop_neuronsphere_platform`` (~500 lines), ``k3s_chart_plugins.py`` (416
lines, reachable only from platform mode), and ``local_storage_provisioner.py``
(210 lines, which has no call site anywhere in the package).

**Formats unchanged:** ``nsplugin.json``, BACON ``manifest.json`` (including
``deploy.default_configuration`` and ``pre_build_artifacts``),
``config_local.json``, the ``hmd_lang_deployment.change_set`` variant-C entry
shape, ``$HMD_HOME/.config/hmd.env``, and every ``HMD_*`` /
``HMD_LOCAL_NEURONSPHERE_ENABLE_*`` environment variable. (Amended
2026-09-25: ``nsplugin.json`` is the one exception. SPEC005 withdrew it and
``nsctl`` reads no plugin index, so the format is unchanged only in the sense
that the Python CLI still reads it.)

Architecture Overview
---------------------

``nsctl`` lives in this repository at ``src/go/nsctl/``, following the house
Go layout established by ``hmd-cli-bartleby``, ``nsx``, and the
``go_cli_repo`` cookiecutter in ``hmd-cookiecutter-default-repo``: ``cmd/``
holds thin cobra adapters, ``internal/`` holds all behavior, the build runs
from a repository-root ``Makefile`` rather than a BACON build command, and the
version is injected by ``-ldflags`` from ``meta-data/VERSION``.

::

    hmd-cli-neuronsphere/
    +-- Makefile                     # go build/test/vet, version from meta-data/VERSION
    +-- meta-data/VERSION
    +-- src/
    |   +-- python/                  # unchanged; coexists through the port
    |   +-- go/
    |       +-- nsctl/
    |           +-- go.mod           # github.com/neuronsphere/hmd-cli-neuronsphere
    |           +-- main.go
    |           +-- cmd/
    |           |   +-- root.go
    |           |   +-- env.go          # start/stop/apply/add/delete/list/status
    |           |   +-- controlplane.go # start/stop/status
    |           |   +-- repo.go         # add/remove/list/import
    |           +-- internal/
    |               +-- registry/     # environments.json reader/writer (SPEC003)
    |               +-- manifest/     # the environment manifest (desired state)
    |               +-- compose/      # YAML -> Docker Engine API, port validation
    |               +-- container/    # docker shell-out, network resolution
    |               +-- floci/        # aws-sdk-go-v2 against Floci (SPEC007)
    |               +-- k3s/          # cluster lifecycle, operators, kubeconfig
    |               +-- router/       # nginx config generation
    |               +-- msdeploy/     # ms-deployment apiop + CRUD client (SPEC009)
    |               +-- bom/          # BOM entries, topological sort, seeding
    |               +-- repoclass/    # manifest + meta-data/resources resolution
    |               +-- reconcile/    # plan/snapshot/digests
    |               +-- runner/       # in-process DAG node execution
    |               +-- controlplane/ # lifecycle and the bootstrap DAG
    |               +-- environment/  # lifecycle and apply
    |               +-- status/       # what env status and cp status report
    |               +-- bundled/      # go:embed of services/ (SPEC006)
    ``internal/plugin`` is absent: plugin discovery is withdrawn (SPEC005).

    ``src/go/nsrunner/`` was reserved for the DAG-runner service and is **not**
    what was built. Go scopes ``internal/`` per module, so a second module could
    not import ``internal/runner`` -- the finished node executor -- and would
    have had to duplicate it or force it public. The runner instead ships inside
    this module as ``internal/nsrunner`` behind a hidden ``nsctl runner serve``
    subcommand, with ``Dockerfile`` producing a ``docker:cli`` image of the
    same binary -- not the scratch image first proposed, since
    ``internal/container`` shells out to the Docker CLI. One binary that can
    also serve costs a command nobody types. Nothing publishes that image;
    ``nsctl`` builds it on demand from sources it carries (SPEC013).

    *2026-09-20:* ``internal/nsrunner`` and ``nsctl runner serve`` were removed
    again when ``env start`` moved to client-side ordering against the
    deployment service's core image (``hmd-ms-deployment`` NERD0015); the code
    is archived in ``hmd-lib-nsrunner``. The image survives as
    ``hmd-img-nsctl``, because the identity provider (``authd``) runs from it.

.. spec:: Command surface
    :id: HMD_CLI_NERD002_SPEC001
    :links: HMD_CLI_NERD002
    :status: implemented

    ``nsctl`` is organised as ``nsctl <noun> <verb>`` after ``kubectl``, not as
    a transliteration of the Cement controller tree.

    .. list-table::
       :header-rows: 1

       * - ``nsctl``
         - Python equivalent today
       * - ``nsctl env start <name|default>``
         - ``hmd neuronsphere up --env <name>``
       * - ``nsctl env stop <name|default>``
         - ``hmd neuronsphere down --env <name>``
       * - ``nsctl env add --name <n> --bom-json <path>``
         - ``hmd neuronsphere env create <n> --bom-file <path>``
       * - ``nsctl env delete --name <n>``
         - ``hmd neuronsphere env delete <n>``
       * - ``nsctl env purge [<name>]``
         - ``hmd neuronsphere down --purge [--env <n>]``
       * - ``nsctl env apply [<name>]``
         - ``hmd neuronsphere up --env <name> --upgrade``
       * - ``nsctl repo add <class>[@<version>]``
         - (no equivalent -- edit the manifest by hand)
       * - ``nsctl repo remove <instance>``
         - (no equivalent -- edit the manifest by hand)
       * - ``nsctl repo list``
         - (no equivalent)
       * - ``nsctl env list`` / ``nsctl env status``
         - ``hmd neuronsphere env list`` / ``status``
       * - ``nsctl control-plane start|stop|status``
         - (no equivalent -- see SPEC002)

    .. note::

       ``nsctl load-plugin`` was in the original table. It is withdrawn along
       with the rest of plugin discovery; see the amendment on SPEC005.

    **What nsctl ships.** The control plane, one per ``$HMD_HOME``, and the
    environment *substrate* -- the core instance, the environment's Postgres,
    its k3s cluster, ``hmd-ms-dbaccount``, and the External Secrets operator
    with its CRDs. ``nsctl env add`` yields an environment that is empty but
    usable: a cluster, a database, and the operator a cloud chart's secrets
    resolve through.

    *Widened 2026-09-07.* This said "a cluster and a database with nothing
    deployed on them", which was a nicer sentence and not true of an environment
    anyone could deploy into: the first cloud chart rendering an
    ``ExternalSecret`` fails on the missing CRD. It read as true only because
    every environment anyone had tried had the entries written into its manifest
    by the Python CLI.

    Everything above that is a RepoClass the user adds. Airflow, Argo,
    transform, Trino, Superset and the rest are **not** built into the binary;
    they are what ``repo add`` declares and ``env apply`` deploys. The
    environment manifest at ``$HMD_HOME/environments/<slug>.yaml`` is the
    desired state, and the ``repo`` verbs are wrappers that edit that file --
    editing it by hand and running ``env apply`` is the same operation, so
    scripted and hand-authored use converge on one artifact rather than
    diverging into two.

    ``start`` and ``stop`` **must be behaviourally identical** to extend-mode
    ``up`` and ``down``, including the ``--upgrade`` and ``--prune``
    delta-apply flags that make a restart cheap rather than a cold bootstrap.

    Two deliberate departures from the Python surface:

    - **``purge`` is a verb, not a flag.** Destroying an environment's Floci
      account, k3s cluster, Postgres and graph state deserves its own word.
      With no name it purges every environment plus the control plane, and
      requires an interactive confirmation (or ``--yes``), preserving the
      guard ``_confirm_full_purge`` provides today.
    - **``env add`` takes a BOM JSON directly.** The Python ``env create``
      splits this across ``--manifest`` (declarative, preferred) and a
      deprecated ``--bom-file``. ``--bom-json`` accepts either shape,
      detecting a flat array as the legacy BOM form.

.. spec:: Control-plane lifecycle is explicit and separate
    :id: HMD_CLI_NERD002_SPEC002
    :links: HMD_CLI_NERD002
    :status: amended

    A local NeuronSphere is **one shared control plane per ``HMD_HOME``**
    (``hmd-ms-deployment``, ``hmd-ms-naming``, ``hmd-ms-artifact-lib``, the
    Deployment GUI, plus the single Floci, nginx, Postgres and JanusGraph)
    serving **N environments**, each a self-contained emulated AWS account with
    its own k3s cluster, Postgres, JanusGraph and ``hmd-ms-dbaccount``.

    The Floci is shared: an environment is an *account* inside the control
    plane's single Floci container, not a container of its own (SPEC007), which
    is what makes a second environment cost noticeably less than the first.

    ``nsctl env start`` starts the control plane implicitly if it is down, and
    then **leaves it running**. ``nsctl env stop`` must never stop it: stopping
    it now takes every other environment's emulated AWS with it -- their
    Lambdas, gateways, buckets and secrets, not just the deployment graph's
    availability.

    The control plane is therefore given its own verb:

    - ``nsctl control-plane start`` -- idempotent; the same work ``env start``
      does implicitly.
    - ``nsctl control-plane stop`` -- **the only command that stops it.**
      Refuses (exit 3) while any environment is still running, unless
      ``--force`` is given.
    - ``nsctl control-plane status`` -- reports bootstrapped state, the
      ms-deployment health probe, and which environments are up.

    ``nsctl cp`` is registered as an alias.

    This closes a genuine gap in the Python CLI, where the control plane has no
    independent lifecycle at all -- it is only ever stopped as a side effect of
    a bare ``hmd neuronsphere down``.

    *``env purge`` was run for the first time on 2026-09-07 and was wrong.* It
    had been unit-tested throughout; its first real execution left seven
    containers, three volumes and the platform network behind, and the three
    failures were one failure with two consequences.

    ``floci-ecr-registry`` is not one of the three Floci services the purge names
    by resource identifier -- eks, rds, neptune -- so it survived, holding both
    its own volume and a live endpoint on the platform network. The
    leftover-volume sweep and the network removal then failed *behind* it, and
    two ``floci-rds-db-*`` volumes survived with it. Both sweeps are by label
    now: ``io.floci.account`` scoped to the environment, and a cross-account
    ``floci`` sweep between the control-plane teardown and the volume and
    network removals. The ``floci`` label is on every container Floci spawns and
    on nothing else -- the compose-created ``floci`` container does not carry it
    -- so a service Floci grows next needs no change here.

    ``RemoveVolumes`` returned on the first volume it could not remove. A sweep
    whose stated purpose is "the volumes an earlier purge could not reach"
    cannot stop at the first volume it cannot reach; that is exactly how
    forty-nine of them accumulated.

    ``compose.Runner.Remove`` was written for this -- its comment reads "Only a
    purge does this" -- and nothing called it. ``PurgeAll`` went through
    ``Stop``, which is ``compose stop``, so a full purge left ``hmd_proxy``,
    ``floci``, ``hmd_deployment_gui`` and ``hmd_nsrunner`` as Exited containers
    named after a platform whose network and every ``$HMD_HOME`` path they were
    configured from had just been deleted.

    What item 13 got right is the ordering, and it held: no purge has ever
    warned ``Could not connect to the endpoint URL "http://localhost:4566/"``.

    *Two things a purge deliberately does not destroy, and one it should have.*
    The environment manifest lives in ``$HMD_HOME/environments/``, outside the
    state directory a purge removes; it is the user's declaration, often
    version-controlled, and rebuilding from it is a common reason to purge. But
    a purge also unregisters the environment, so the file is orphaned and
    silently re-adopted by a later ``env add`` of the same name -- a purge
    announcing "none of it comes back" followed by a start trying to deploy 28
    workload repos nobody had declared in that session. It is kept, and now
    named. The deployment graph is the one that should have been handled: it
    lives in the control plane's Postgres and outlives an environment purge, so
    ``env purge``, ``env add``, ``env start`` planned "3 unchanged" and never
    recreated the database. See item 20.

.. spec:: The environment registry is read, never re-derived
    :id: HMD_CLI_NERD002_SPEC003
    :links: HMD_CLI_NERD002
    :status: implemented

    ``$HMD_HOME/.cache/neuronsphere/environments.json`` persists **every**
    derived value for the control plane and each environment: container names,
    compose project name, Docker network name, k3s cluster name, kubeconfig
    path, account id, port slot, and the bootstrap record
    (``{csd_nid, k3s_uid}``). ``env_registry.py`` computes these once, at
    create time, and never recomputes them -- deliberately, so that changing a
    derivation rule later cannot orphan containers or volumes named under the
    old rule.

    **``nsctl`` MUST read this file rather than re-derive any of it.** The
    ``HMD_HOME``-to-network-name hash already exists in three independent
    copies (``floci_deployer.py``, ``hmd-cli-bender``, and ``nsx``'s
    ``internal/container/network.go``, which carries a "keep in sync" comment
    counting them). A fourth copy inside ``nsctl`` would be the one nobody
    updates. The hash is implemented only as a fallback for a never-bootstrapped
    ``HMD_HOME``, and is exercised by a test asserting it matches the Python
    value for a known input.

    Re-derivation is not merely redundant, it is **wrong for a case that
    exists in the field**: an environment migrated from the pre-multi-env
    layout carries ``legacy_layout: true``, shares the control plane's Postgres
    and graph rather than running its own, and so has a container named
    ``hmd_db`` -- matching no current naming rule at all.

    Note ``floci_container``/``floci_alias`` are **not** registry fields. Every
    environment shares the one Floci, so both are derived constants (``floci``
    and ``neuronsphere``); a registry written by an older CLI still carries
    per-environment values, and the loader drops them. The field that does the
    work is ``account_id`` (SPEC007).

    Registry writes (``env add``, ``env delete``, port-slot and account-id
    allocation, ``record_bootstrap``) must preserve the exact JSON shape,
    including the slot arithmetic (``DEFAULT_PORT_BASE=19000``,
    ``PORTS_PER_ENV=4``, ``MAX_ENVS=16``; slot *n* takes its base at ``base+4n``,
    trino ``+1``, graph ``+2``, spare ``+3``) and the slug rules
    (``^[a-z0-9][a-z0-9-]{0,15}$`` plus the reserved-slug set).

    **The regression fixture is the literal JSON the Python CLI emits**, not
    Go structs marshalled and read back -- a round-trip fixture passes however
    wrong the struct tags are.

.. spec:: CLI framework -- Cobra replacing Cement
    :id: HMD_CLI_NERD002_SPEC004
    :links: HMD_CLI_NERD002
    :status: implemented

    ``spf13/cobra`` v1.8.0 (the house version, used by ``nsx``, ``goblin``,
    ``bartleby`` and the ``go_cli_repo`` cookiecutter), with ``RunE``
    everywhere and a typed error carrying an exit code.

    .. list-table::
       :header-rows: 1

       * - Cement concept
         - Go equivalent
       * - ``Controller`` class
         - ``cobra.Command`` with subcommands
       * - ``@ex()`` decorator on a method
         - ``cobra.Command{RunE: func}``
       * - ``stacked_type = "nested"``
         - cobra parent/child command tree
       * - ``self.app.pargs``
         - ``cmd.Flags()``
       * - ``minimal_logger()``
         - ``log/slog``
       * - ``shell.cmd()``
         - ``os/exec.Command``
       * - ``importlib.metadata.version()``
         - ``-ldflags -X`` build-time injection

    **Per-command flags live in constructor closures, not package globals.**
    ``nsx`` found this necessary rather than stylistic: ``t.Setenv`` panics
    under ``t.Parallel()``, so state-dependent command tests can only run in
    parallel if every command is constructed by a function taking its state.
    The same reason motivates a ``--home`` flag overriding ``HMD_HOME``.

    **Exit codes are distinct, never overloaded as counters:** ``0`` success,
    ``1`` nsctl error, ``2`` invalid usage or refused precondition, ``3``
    refused because a resource is in use, ``4`` a deploy node failed.

    ``$HMD_HOME/.config/hmd.env`` is loaded with ``joho/godotenv``, preserving
    the current precedence (process environment wins over file). The file is
    mode 600 and holds live secrets; it is read for the ``HMD_*`` keys and
    never logged.

.. spec:: Plugin discovery, and the plugin bundle
    :id: HMD_CLI_NERD002_SPEC005
    :links: HMD_CLI_NERD002
    :status: withdrawn

    .. warning::

       **Withdrawn.** ``nsctl`` implements no plugin discovery: neither the
       ``nsplugin.json`` half described below nor the ``nsbundle.json`` format
       proposed to replace the entry-point half. ``internal/plugin``,
       ``nsctl load-plugin`` and ``compose_substitute`` do not exist, and
       ``LocalPluginLoader`` is not ported.

       A repo already declares what it needs and what it produces, in its
       BACON ``manifest.json`` and its NERD0004
       ``meta-data/resources/*.yaml`` -- the same files the cloud deploy path
       reads. A per-plugin inventory says the same things a second time, and
       is the copy that goes stale. That is not hypothetical: nsctl briefly
       carried a hardcoded table of what ``hmd-vpc`` produces, and it named a
       different resource namespace than ``hmd-vpc``'s own declaration did.

       What replaces each of ``LocalPluginLoader``'s jobs:

       .. list-table::
          :header-rows: 1
          :widths: 45 55

          * - ``LocalPluginLoader`` today
            - ``nsctl``
          * - ``_deploy_hmdms_service_lambdas``
            - A fixed bootstrap order for the shipped foundation services
              only. Everything else is a user-added RepoClass the DAG
              deploys.
          * - ``_aggregate_hmdms_resources``
            - ``repoclass.Produces`` reads the repo's own
              ``meta-data/resources/*.yaml``.
          * - ``provision_plugin_databases``
            - The databases a RepoClass declares, POSTed to the
              environment's dbaccount.
          * - ``compose_substitute``
            - Gone. No shipped plugin used it.

       The consequence to plan around is that ``nsctl`` supports **only** the
       manifest-driven path: ``_collect_plugin_bom_entries`` has no Go
       analogue, so an environment whose entries come from installed plugin
       packages has nothing for nsctl to read until those entries are written
       into its manifest. ``plugins`` and ``plugin_config`` keys in a manifest
       are preserved on save and reported as having no effect, rather than
       silently obeyed or silently dropped.

    The rest of this SPEC is retained as a record of what was proposed.

    Plugins reach the local platform two ways today, and only one of them
    ports directly.

    **(a) Filesystem plugins port unchanged.** A repo is a plugin if it has
    ``src/local/nsplugin.json``. Discovery is explicit colon-separated paths in
    ``HMD_LOCAL_PLUGINS``, then a scan of ``HMD_REPO_HOME`` when
    ``HMD_LOCAL_PLUGINS_SCAN_REPO_HOME=true``. Enablement precedence is
    preserved exactly: the core set ``{floci, main, graph}`` is always on;
    then explicit listing in ``HMD_LOCAL_PLUGINS``; then
    ``env_var_override`` / ``HMD_LOCAL_NEURONSPHERE_ENABLE_<NAME>``; then
    ``enabled_by_default`` (false, so every non-core plugin is opt-in).
    ``ensure_foundation_plugin`` force-loads ``artifact-lib`` and
    ``dbaccount`` regardless of user configuration.

    The ``nsplugin.json`` **schema is unchanged**, including the
    extend-mode-critical ``hmdms_service`` block from which a full Floci Lambda
    spec is synthesized (function name, ``<repo_name>:<version>`` image,
    ``SERVICE_CONFIG`` merged from BACON ``deploy.default_configuration`` plus
    ``src/local/config_local.json``, one ``<VAR>=s3://<bucket>`` per declared
    bucket, and ``dependency:db-credentials`` resolved to a literal Secrets
    Manager name). The validator (``nsplugin_validator.py``) is ported
    check-for-check.

    **(b) Installed-package plugins are the half with no Go analogue.**
    ``hmd-cli-plugin-ns-{telemetry,analytics-engines,orchestration,visualization}``
    contribute through setuptools entry-point groups -- chiefly
    ``hmd_cli_neuronsphere.get_local_bom_entries``, plus
    ``get_post_deploy_notices`` -- and ship their own ``external/`` artifact
    roots that ``bom_seeder`` walks during version resolution. Go cannot
    enumerate installed Python distributions.

    **Auditing what those four functions actually do settles the format.**
    Their ``bom.py`` modules run 209-410 lines each, and essentially all of it
    is a Python literal: ``repo_instance_name``, ``repo_class_name``,
    ``deployment_id``, a nested ``instance_configuration`` dict, and a
    ``dependencies`` map of role to instance name. The executable surface
    across all four is four constructs and nothing else:

    .. list-table::
       :header-rows: 1
       :widths: 34 66

       * - Construct in ``bom.py``
         - Declarative equivalent
       * - ``os.environ.get(HMD_LOCAL_NEURONSPHERE_ENABLE_<X>, "true")`` not in
           ``{false, 0, no}`` -- all four plugins
         - ``enabled_by: {env: ..., default: true}`` on the bundle
       * - ``distribution("hmd-cli-plugin-ns-<sibling>")`` plus that sibling's
           enable flag, gating a shared instance (``redis``) so two plugins do
           not both contribute it
         - ``contribute_if_absent: true`` on the entry
       * - A boolean feature flag guarding one entry or one config key
           (telemetry's ``HMD_..._TELEMETRY_COLLECT_K3S``, default false)
         - ``when: {env: ..., default: false}`` on the entry
       * - An env var supplying an instance name or list
           (``HMD_LOCAL_ORCHESTRATION_TRANSFORM_BUCKETS``), and the imported
           ``CORE_INSTANCE_NAME`` constant
         - ``${env:VAR}`` and reserved tokens ``${core}``, ``${ext_secrets}``,
           ``${env.deployment_id}``, ``${env.slug}``

    ``contribute_if_absent`` deserves note because it **deletes the one
    construct that cannot port**. Both halves of the ``redis`` coordination
    already declare, in comments, that their entries are byte-for-byte
    identical and that ``bom_seeder`` de-dupes by ``repo_instance_name``, so
    the ``importlib.metadata`` probe is buying only the avoidance of a de-dup
    that is already a no-op. Declaring the entry and letting the loader
    de-dupe is the same outcome without either bundle needing to know the
    other exists.

    **Therefore: ``load-plugin`` consumes a bundle, not an image.** A plugin
    bundle is a zip -- in practice the repo's existing
    ``<repo>_<version>_build.zip``, the artifact the BACON build already
    produces and the Artifact Librarian already stores -- carrying::

        <plugin>/
          meta-data/VERSION, manifest.json
          src/local/nsplugin.json           # unchanged
          nsbundle.json                     # NEW: declarative BOM contribution
          external/<repo>/...               # artifact roots, as today

    ``nsctl load-plugin <path|name>`` validates ``nsbundle.json``, registers
    the bundle under ``$HMD_HOME/.cache/neuronsphere/plugins/<name>/``, and
    adds its ``external/`` tree to the artifact-root index used for version
    resolution. A loaded bundle is thereafter indistinguishable from a bundled
    one. ``nsctl load-plugin --list`` / ``--remove`` manage the set.

    **Why not a Docker image that loads itself.** Running plugin-supplied code
    to answer "what does this plugin contribute" makes every cheap read
    expensive and every cheap read a trust decision:

    - ``nsctl env plan``, ``env list`` and ``env status`` must answer *what
      would deploy* without side effects. Behind an image, each becomes N
      image pulls and N container runs, and is unusable offline.
    - The reconcile digests (SPEC009) hash the declared entries. A stable
      digest needs a stable, inspectable input; a program's output is neither
      diffable nor reviewable.
    - Executing a third-party image at host authority to *configure* the
      platform is a far larger grant than reading its declarative data, and
      it is the grant nothing else in this design asks for.

    **The escape hatch already exists, one layer down.** A plugin that must
    genuinely *run* something does so in its own deploy node -- ``src/local/
    deploy_local.sh``, executed in ``hmd-img-projectbuilder`` by the runner
    (SPEC010), already scoped to one instance and already sandboxed in a
    container. Load time is for declaring; deploy time is for doing. If a
    future plugin needs computation at load time that no primitive above
    covers, an optional ``loader_image`` field may be added to
    ``nsbundle.json`` -- but it is deliberately not specified here, because
    specifying it now would make "arbitrary code" the loader's contract from
    day one for a need no existing plugin has.

    **Post-deploy notices are declarative too.** The only implementation
    (Superset's admin login) reads the secret
    ``superset-${env.deployment_id}-${env.slug}-admin-credentials`` and formats
    two of its fields. That is a ``post_deploy_notices`` list of
    ``{secret, template}`` in ``nsbundle.json``, evaluated by ``nsctl``, with
    the same best-effort contract as today (any failure is logged, never
    surfaced as a deploy failure).

    **Migration of the four existing plugins is mechanical.** Each keeps its
    repo, its ``external/`` artifact roots and its BACON build; ``bom.py``
    becomes ``nsbundle.json``, the setuptools ``entry_points`` block is
    retained so the Python CLI keeps working during coexistence (SPEC012), and
    a test asserts the two produce the same entry list. Telemetry additionally
    moves its ``data/otel_databases.json`` into the bundle verbatim -- it is
    already pure data that ``bom.py`` only opens and inlines.

.. spec:: Bundled artifacts and go:embed
    :id: HMD_CLI_NERD002_SPEC006
    :links: HMD_CLI_NERD002
    :status: amended

    .. warning::

       **Implemented, and it was not optional.** Until 2026-09-07 a
       Homebrew-installed ``nsctl`` could not deploy an environment at all: the
       substrate's first node failed with ``no working tree for hmd-vpc under
       <HMD_REPO_HOME>``, so "a single binary whose only prerequisite is Docker"
       -- the requirement at the top of this NERD -- was untrue of everything
       past ``control-plane start``, and the whole of Phase 6's distribution
       work shipped something that stopped there.

       **Ten trees, not the six the manifest declared then (seven now, with
       ``hmd-inf-neptune`` pinned).** Four of the
       ``pre_build_artifacts`` entries -- superset, hyperdx, telemetry-debug and
       s3bucket -- are workloads or plugin content and are not wanted. The other
       two are, but not for the reason the manifest declares them: they were
       pre-build artifacts so ``k3s_operators.py`` could ``helm upgrade
       --install`` them directly, and that path is dead now that ext-secrets is
       a default BOM entry. They deploy as ordinary DAG nodes, which is why they
       hit the same failure as everything else. Eight more had to be added: the
       control plane's own instances (``hmd-vpc``, ``hmd-postgres-rds``,
       ``hmd-inf-neptune``), the substrate's cluster
       (``hmd-inf-eks-cluster``) and the four foundation services.

       **382 KB gzipped in total**, measured over ``meta-data/`` and ``src/``
       with build outputs excluded, dominated by ``hmd-ms-deployment`` (165 KB)
       and the ext-secrets CRDs (136 KB). The binary went from 34.4 MB to
       34.8 MB against SPEC013's 50 MB target: the size question this was
       previously framed around does not exist.

       **The build was the hard part, and it is now a fetch with no committed
       fallback.** ``make generate`` runs from GoReleaser's ``before.hooks``,
       so whatever it does has to work on a CI runner with no ``hmd``
       installed. It resolves each class from the ``pre_build_artifacts``
       destination when ``hmd build`` has populated it, then from
       ``src/go/nsctl/.artifacts/<class>@<version>/``, and otherwise fetches
       the published ``build`` artifact from the artifact librarian directly --
       ``internal/librarian``, a port of ``artifact_tools`` and
       ``okta_tools.get_auth_token`` and nothing else. CI authenticates with
       ``HMD_ARTIFACT_LIBRARIAN_API_KEY``; a developer authenticates with the
       token ``hmd login`` already cached, so neither has to configure anything
       twice. The cache is deliberately **not** the ``pre_build_artifacts``
       destination, which ``hmd build`` deletes when it finishes.

       Until 2026-09-11 the last tier was ten trees committed under
       ``bundled-repos/``, because the probe below had returned
       ``Unauthorized`` and seven pins nobody could resolve would have broken
       ``hmd build`` for everyone. They are gone, and they were not merely
       redundant. ``bundled-repos/hmd-vpc`` had drifted from its repository and
       was missing the ``NERD0004`` ``add_resource_output`` block, so a binary
       built from it deployed a VPC that emitted no Resource and handed
       ``hmd-postgres-rds`` no ``db_subnet_group_name``. Every committed tree
       also carried the ``MAJOR.MINOR`` stub in ``meta-data/VERSION`` -- ``0.4``
       for ``hmd-ms-deployment``, not ``0.4.854`` -- which is the version
       ``ResolveVersion`` then reported for a tree it had not used. That is the
       failure mode ``NERD005`` names as the worst available, arrived at from
       the other direction.

       **A tree has to be materialised on disk, not merely embedded.**
       projectbuilder is launched as a *sibling* through the Docker socket, so
       the host daemon resolves every bind mount: a tree inside the binary is
       not mountable. ``internal/repotree`` unpacks to
       ``$HMD_HOME/.cache/neuronsphere/repos/<class>@<digest>``, the same shape
       ``EnsureRunnerImage`` already uses for the runner's own sources, and for
       the same reason -- ``$HMD_HOME`` is bound into the runner container at
       its own absolute path, so the two path spaces coincide.

       One consequence worth stating: a bundled tree is **always** copied before
       a deploy runs in it. The workspace is mounted read-write and deploys
       write ``meta-data/resources_output/``, so a shared digest-addressed cache
       would stop matching what the binary carries and would hand the next
       environment the last one's outputs.

    The BACON ``build.pre_build_artifacts`` mechanism unpacks other repos'
    build outputs into ``src/python/hmd_cli_neuronsphere/external/<name>/`` at
    build time; each contributes its ``src/local/`` tree, its ``src/helm/``
    chart and its ``meta-data/`` (VERSION + manifest). Bundled artifacts
    **win over** a working tree during version resolution unless the user opts
    out via ``HMD_LOCAL_VERSION_<REPO_CLASS>`` or
    ``HMD_LOCAL_NEURONSPHERE_PREFER_LOCAL_VERSIONS``.

    ``nsctl`` embeds the same trees with ``//go:embed``, plus the
    ``services/docker-compose.*.yml`` files:

    .. code-block:: go

        //go:embed services/*.yml services/*.env
        //go:embed external/*/src/local/* external/*/meta-data/*
        var bundled embed.FS

    ``pre_build_artifacts`` is unchanged; only the unpack destination moves.
    At runtime the embedded FS is consulted first and a filesystem plugin of
    the same name overrides it, preserving today's precedence. Files consumed
    by ``docker compose`` or ``helm`` are materialised into
    ``$HMD_HOME/.cache`` before invocation, since neither tool can read an
    ``embed.FS``.

    **Docker Compose files are not templates.** They use Compose's native
    ``${VAR:-default}`` interpolation, handled by Compose itself. Only
    user-authored ``src/local/templates/`` entries are rendered, by
    ``text/template``; no bundled plugin defines any today, so the Jinja2
    syntax divergence has no current blast radius and a migration note in the
    plugin development guide is sufficient.

    *Verified from nothing on 2026-09-07.* A control plane and an environment
    deployed entirely from the bundled trees, with ``HMD_REPO_HOME`` pointed at
    an empty directory -- which is the claim this SPEC exists to support and had
    never been executed. Nothing in the exclusion list turned out to have
    dropped a file a deploy reads: the ten trees are packed as
    ``meta-data/`` plus ``src/``, minus build outputs and
    ``meta-data/resources_output/``, and every node deployed.

    **A repo must resolve to a working tree; there is no artifact source.**
    ``manifest.validateSource`` accepts ``type: local`` and refuses
    ``type: artifact`` with *"source type "artifact" is not supported yet; use
    'type: local' to deploy from a local working tree"*. The name is reserved so
    that declaring one fails with that sentence rather than "unknown source
    type", but nothing resolves it: every declared repo is a checkout or a
    bundled tree. Note the asymmetry this leaves, which ``NERD005`` exists to
    close: the *build* now resolves a repo class from a published artifact,
    while a *deploy* still cannot.

    *Resolved 2026-09-11.* All ten classes are declared as
    ``pre_build_artifacts`` and ``bundled-repos/`` is deleted. The earlier
    ``Unauthorized`` was a stale credential, not a missing publication: with a
    fresh ``hmd login`` every one of the seven resolved on the first attempt,
    each stamped to exactly the version its latest pipeline tag names.

    .. list-table:: The probe, run against every undeclared class
       :header-rows: 1
       :widths: 30 14 56

       * - Repo class
         - Pinned
         - What the artifact carries
       * - ``hmd-vpc``
         - ``0.2.41``
         - ``meta-data/`` + ``src/{cdktf,local}``; the same file set as the
           committed tree, whose ``cdktf_local.py`` had gone stale
       * - ``hmd-postgres-rds``
         - ``0.8.51``
         - the same file set as the committed tree
       * - ``hmd-inf-eks-cluster``
         - ``0.7.115``
         - the same file set as the committed tree
       * - ``hmd-ms-naming``
         - ``0.1.58``
         - drops ``src/{docker,python}``, adds
           ``meta-data/docker.ext_artifact.hmdentity``
       * - ``hmd-ms-artifact-lib``
         - ``0.1.94``
         - as above
       * - ``hmd-ms-deployment``
         - ``0.4.854``
         - as above
       * - ``hmd-ms-dbaccount``
         - ``0.1.44``
         - as above

    The four ``hmd-ms-*`` artifacts drop ``src/docker/`` and ``src/python/``,
    which are build-time inputs already baked into the published image and
    which no deploy reads, and gain
    ``meta-data/docker.ext_artifact.hmdentity`` -- the image the build actually
    produced, e.g. ``ghcr.io/hmdlabs/hmd-ms-deployment:0.4.854``. The committed
    trees carried that nowhere. Net effect: the ten archives fell from 382 KB
    to 222 KB, and 247 files and 3.5 MB left the repository.

    *Verified on the path that matters.* ``goreleaser build --snapshot
    --clean`` from a tree containing only tracked files -- no
    ``bundled-repos/``, no ``external/``, no fetch cache -- fetches all ten and
    produces a binary carrying every one at its published version. Without
    credentials the same build fails naming the class, the version and every
    place that was looked, rather than at ``go:embed``.

.. spec:: AWS SDK -- aws-sdk-go-v2 against Floci
    :id: HMD_CLI_NERD002_SPEC007
    :links: HMD_CLI_NERD002
    :status: implemented

    ``floci_deployer.py`` drives S3, Secrets Manager, SSM, Lambda, API Gateway,
    RDS and EKS against the Floci emulator via boto3. ``internal/floci`` uses
    ``aws-sdk-go-v2`` with a static-credentials provider and an explicit base
    endpoint:

    .. code-block:: go

        cfg, err := config.LoadDefaultConfig(ctx,
            config.WithRegion(region),
            config.WithBaseEndpoint(endpoint),
            config.WithCredentialsProvider(
                // The account selector -- see below. NOT a dummy value, and
                // never read from the ambient AWS_ACCESS_KEY_ID.
                credentials.NewStaticCredentialsProvider(accountID, "dummykey", ""),
            ),
        )

    Notes carried forward from the Python implementation:

    - **The access key selects the account, and the endpoint does not.** There
      is exactly one Floci container behind the ``neuronsphere`` alias. The
      control plane and every environment are separate *accounts* inside it,
      and Floci resolves which one a request belongs to from the SigV4 access
      key id it is signed with -- a 12-digit access key *is* the account --
      namespacing every storage-backed service beneath it.

      So the Go port must carry the account on the target, exactly as
      ``floci_deployer.FlociTarget.access_key_id`` does, and thread it through
      every credential-building site: the SDK config above, the projectbuilder
      containers that run deploy nodes, and the External Secrets operator's
      chart values. **Signing with the wrong key does not fail** -- the call
      succeeds against the wrong account, which is why an ambient
      ``AWS_ACCESS_KEY_ID`` must never be used here.

      This supersedes the earlier container-per-environment rule (each
      environment addressed by its own Floci container address rather than the
      ``neuronsphere`` alias), which no longer applies.
    - Errors are matched with ``errors.As`` on ``smithy.APIError`` rather than
      by string-matching a response dict.
    - ``clear_apigateway_state`` must be preserved: Floci persists API Gateway
      v1 entities all-null and serves them back as undeletable ghosts.
    - Secret names are produced by ``hmd_cli_tools.make_standard_name``, which
      must be ported **bit-for-bit** -- the microservices reading those secrets
      stay Python and compute the same name independently.

.. spec:: Docker is the only host tool -- kubectl and helm run in projectbuilder
    :id: HMD_CLI_NERD002_SPEC008
    :links: HMD_CLI_NERD002
    :status: amended

    .. warning::

       **Two claims in this SPEC were wrong, and are corrected here.** The
       conclusion -- Docker is the only host tool -- holds; two of the routes
       it proposed for getting there do not.

       **kubectl does not run in projectbuilder, because projectbuilder ships
       no kubectl.** No published tag has one: ``:stable``, ``:0.5``,
       ``:0.5.383`` and ``:localdev`` were all checked. ``RunKube`` instead
       ``docker exec``\ s into the running k3s node container and uses its
       ``/bin/kubectl``, which launches no container at all and removes
       projectbuilder as a prerequisite for provisioning a cluster. The
       batched-script design below is unchanged and is what makes that
       practical; only the container it runs in differs.

       **``docker compose`` is not used either.** In extend mode compose
       manages three containers described by one embedded file, and
       ``docker-compose.environment.yml`` declares no services at all.
       ``internal/compose`` parses that file and materialises the services
       through the Docker Engine API, so ``docker`` really is the only host
       binary -- and the ``pip config get`` shellout that
       ``_get_base_command`` performed on every compose invocation goes with
       it.

       Coexistence (SPEC012) is what makes this safe: nsctl stamps the
       ``com.docker.compose.*`` labels on every container it creates, so
       ``docker compose ps|stop|down`` and ``port_validator.py`` still find
       them. One label matters more than the rest --
       ``com.docker.compose.config-hash``. Its *presence* is what makes a
       container discoverable to compose; without it ``docker compose ps``
       skips the container even when every other label matches, verified
       against compose v2.40.

    ``nsctl`` requires **``docker`` (or ``nerdctl``) on the host and nothing
    else.** Postgres stays ``docker exec ... psql``; image staging into the
    k3s node's containerd stays
    ``docker save | docker exec ... ctr images import``. Everything that today
    needs a ``kubectl`` or ``helm`` binary on the host either moves into a
    container -- the k3s node for Kubernetes work, ``hmd-img-projectbuilder``
    for the Helm and CDKTF deploys it already runs as DAG nodes (SPEC010) --
    or is not ported at all.

    **Helm is eliminated, not relocated.** There are three host uses and none
    survives:

    1. ``env_reconcile``'s release cross-check already needs no helm binary.
       ``k3s_operators.live_helm_releases`` lists Helm's own release Secrets
       (``-l owner=helm``, reading ``metadata.labels.name``) and its docstring
       states the reason outright: it is cheaper than ``helm list -A`` and needs
       no helm on the host. It is a Kubernetes read, and moves with the rest.
    2. ``_install_operator`` / ``_helm_upgrade`` / ``_ensure_chart_dependencies``
       is **dead on the default path.** ``_OPERATORS`` contains exactly
       ``ext-secrets-crds`` and ``ext-secrets``, and ``provision_k3s_operators``
       skips both whenever ext-secrets deploys through the DAG -- which is the
       default (``HMD_LOCAL_NEURONSPHERE_ENABLE_EXT_SECRETS`` is on unless
       explicitly disabled) and is also true whenever a plugin's BOM already
       contributes an ``ext-secrets`` instance. This is the tail of the
       completed migration that moved ClickHouse, KEDA and cert-manager onto
       BOM entries. ``nsctl`` does not port it; the opt-out becomes "declare it
       in the BOM", which is what every other component already does.
    3. ``helm uninstall traefik`` is a one-time migration off a helm-installed
       Traefik onto the image-baked addon. Not ported. A cluster predating that
       migration is handled by the Python CLI or by deleting the cluster, and
       ``nsctl`` says so rather than silently leaving two ingress controllers.

    **kubectl becomes a batched exec into projectbuilder.** The ~29 host call
    sites (22 in ``k3s_operators.py``, 7 in ``nginx_router.py``) are all plain
    Kubernetes API operations in three groups:

    - **Provisioning mutations** -- the CoreDNS custom-records ConfigMap plus a
      ``rollout restart``; the Traefik ingress-class apply and Addon patch; node
      topology labels; stale-Node and orphaned-PV reaping.
    - **Waits** -- ``_wait_for_node_ready`` polling node readiness.
    - **Reads** -- ``get svc -A -o json`` (NodePort discovery),
      ``get ingress -A -o json`` (host routes), the ``kube-system`` namespace
      UID behind ``cluster_incarnation_id``, and the Helm release Secrets above.

    ``internal/k3s`` exposes one helper that runs a **script**, not a command,
    in a throwaway projectbuilder container, and the call sites are grouped so
    a full ``env start`` costs roughly six containers rather than twenty-nine:

    .. code-block:: go

        // RunKube mounts script at /tmp/kube-step.sh along with the
        // container-rewritten kubeconfig, and returns stdout separately
        // from stderr.
        func (k *Kube) RunKube(
            ctx context.Context, script []byte,
        ) (stdout, stderr []byte, err error)

    Details that follow from the container boundary, each with a precedent
    already in the codebase:

    - **Scripts are mounted, never passed as arguments** -- the same rule the
      deploy node already follows for ``/tmp/hmd-deploy-node.sh``, and for the
      same reason (``ARG_MAX`` and quoting).
    - **``kubectl apply -f -`` becomes ``apply -f <mounted file>``.** Two sites
      feed JSON on stdin today (the ingress class, the CoreDNS ConfigMap); both
      become files in the same mount.
    - **The kubeconfig is the in-container rewrite**, produced by the existing
      ``_kubeconfig_for_container`` logic that repoints the server at the
      in-network Floci EKS alias. The host-reachable kubeconfig is still written
      to ``env.kubeconfig_path`` for the *user's* own ``kubectl``; ``nsctl``
      never reads it.
    - **A wait is a loop inside one container**, not repeated container starts.
    - **stdout carries data, stderr carries diagnosis.** ``-o json`` parsing is
      unchanged; failures buffer their output and print under the failed step,
      exactly as ``_print_failure_detail`` does for a deploy node today.

    **Why not client-go.** Vendoring ``k8s.io/client-go`` would also remove the
    binary dependency and would be faster per call, but it trades one problem
    for two: the Kubernetes client version becomes ``nsctl``'s to keep aligned
    with the cluster (local k3s tracks the cloud EKS version, currently 1.34,
    and that pin already lives in two places), and it costs tens of megabytes
    against SPEC013's size target. The image is already version-matched to the
    cluster it provisions and is already the pin for the CDKTF and Helm
    toolchain. One place to pin the Kubernetes toolchain is worth more than the
    microseconds.

    **Consequence, stated plainly:** ``hmd-img-projectbuilder`` becomes a
    prerequisite of *cluster provisioning*, not merely of deploys. A cold or
    offline first run must have it before k3s is usable at all. ``nsctl``
    therefore pulls it once, early in ``control-plane start``, with an explicit
    progress line -- rather than discovering it missing midway through
    provisioning a cluster.

    Port validation (``port_validator.py``) is reimplemented natively with
    ``net.DialTimeout``; it needs no cluster. Selective use of
    ``github.com/docker/docker/client`` is permitted for inspection-shaped work
    (container status, health, logs) where parsing CLI output would be fragile,
    as ``hmd-cli-bartleby`` already does.

.. spec:: ms-deployment client -- the BOM and apiop protocol
    :id: HMD_CLI_NERD002_SPEC009
    :links: HMD_CLI_NERD002
    :status: amended

    .. warning::

       **The sequence below is not the whole of what seed_bom does, and the
       missing parts are the ones a cold graph needs.** Three steps had to be
       added; every one of them is invisible on a graph the Python CLI has
       already seeded.

       **Resource types must be registered before anything produces one.**
       ``seed_base_resource_definitions`` for the supertypes ms-deployment
       bundles, then each producing repo's own ``meta-data/resources/*.yaml``.
       A repo's definition parents onto a base type -- hmd-postgres-rds's
       ``aws.neuronsphere.io/aurora-postgres`` onto
       ``database.neuronsphere.io/postgres`` -- so the order is the service's
       contract. Without it ``declare_produces_resource_definition`` answers
       ``400 ... aurora-postgres ... not found``.

       **A DeploymentSet carries a base64-JSON ``definition``, not an
       ``environment`` field.** The entity has no such field and requires the
       definition, so creating one answered ``422 {"loc":["body","definition"]}``.
       It names the environment by *slug*: hardcoding ``local`` would make
       every named environment's changeset resolve to the default
       environment's graph.

       **The concrete core Resources are not optional, and they need a phase
       of their own.** A dependency carrying a ``tag_selector`` is matched
       against Resources, not against the produced *types* this SPEC's
       ``declare_produces`` step records -- hmd-database-account's
       ``create-service`` role asks for an
       ``application.neuronsphere.io/microservice`` tagged
       ``repo_class=hmd-ms-dbaccount``. Those Resources attach to a
       RepoInstanceDeployment, which only exists once a changeset has created
       one, so an apply is **two changesets**: the substrate deploys, its
       Resources are submitted (``bom.LocalCoreResources`` --
       the Docker network, the cluster's compute node and its
       Traefik-as-ingress-controller, and one tagged microservice Resource per
       foundation service), and only then is what the manifest declares
       seeded. The Python has always done this; the sequence below simply did
       not describe it.

    ``internal/bom`` re-implements ``bom_seeder.py`` (the single densest module
    in the port, ~2,350 lines) over ``net/http``. Nothing here is a library
    swap; it is protocol.

    The sequence ``seed_bom`` performs, which must be reproduced exactly:
    resolve each entry's version (bundled artifact, then declared, then working
    tree only under an override); ``POST add_repo_class_version``;
    ``upsert_repo_resource_definitions``; ``declare_core_produces``;
    ``PUT`` the ``hmd_lang_deployment.environment`` / ``deployment_set`` /
    ``change_set`` entities with the definition base64+JSON encoded; then
    ``POST apply_changeset`` and ``POST generate_local_deployment/<csd_nid>``.

    **Two topological sorts exist and must not be conflated.**

    1. ``_topo_sort_bom`` (CLI-side, *before* ``apply_changeset``) exists
       because ``apply_changeset_to_environment`` processes changeset entries
       in list order and resolves each entry's dependencies only against repo
       instances already added earlier in the same call. An out-of-order merged
       list fails with "No repo instance". This pass makes **graph
       construction** succeed and stays sequential.
    2. ``traverse_deployment_dag`` (service-side, Kahn's algorithm over the
       reduced ``RepoInstanceReqRepoInstance`` edges) makes **execution order**
       correct. This is the one SPEC011 parallelises.

    ``internal/reconcile`` ports ``change_set_builder.py`` and
    ``env_reconcile.py``. Its ``entry_hash`` / ``definition_hash`` digests and
    the v2 ``applied-changeset.json`` snapshot format must reproduce
    identically to the Python implementation, or every user's first
    ``nsctl env start`` degrades into a full redeploy. This is a golden-vector
    test against snapshots written by the Python CLI, not a self-consistency
    test.

    *Two more ordering defects, found on 2026-09-07 by the same means.* Both
    are the shape this SPEC's amendment already describes -- a step that a warm
    platform had always found already done.

    **The kubeconfig has to exist between the two changesets.** The two-phase
    apply above creates the k3s cluster in Phase A and deploys the first Helm
    chart onto it in Phase B, and nothing wrote the cluster's kubeconfig in
    between: ``startCluster`` is its only writer and a cold start skips it,
    there being no container yet. ``Start``'s "the cluster may only now exist"
    recovery runs *after* ``Apply`` returns, one phase too late. Docker then
    created the missing bind-mount source as a directory and Phase B's first
    node died with ``IsADirectoryError``. ``provisionNewCluster`` runs between
    the phases now.

    **A container that is aliased is a container that has been renumbered.**
    Attaching the ``global-graph`` alias disconnects and reconnects the graph
    container, which gives it a new IP; the Gremlin server binds the container's
    specific address rather than ``0.0.0.0``, so it kept listening on the
    address it had at startup. ``docker ps`` reported it healthy, its own log
    showed a working server, and every client got connection refused -- the
    artifact librarian answered 500 to every request. The graph is restarted
    when, and only when, aliasing actually reconnected it; a warm start finds
    the alias already attached and does nothing.

.. spec:: The DAG-runner service replaces the in-process runner
    :id: HMD_CLI_NERD002_SPEC010
    :links: HMD_CLI_NERD002
    :status: amended

    .. warning::

       **The submit envelope is Argo's. Its contents are not.** This SPEC
       implies ``nsrunner`` receives what ``submit_workfow`` sends, and it does
       not: ``submit_workfow`` posts an ``argoproj.io`` ``Workflow`` CRD, while
       the local path has ``generate_local_deploy_manifest``'s
       ``{"nodes": [...], "destroy": bool}``. Building a CRD locally so the
       payloads matched would produce a document nothing consumes, from
       generation code that needs ``HMD_APP_IMAGE``, ``WORKERS_INSTANCE_NAME``
       and an Okta service-account token that do not exist locally.

       So the *route* is kept and the *payload* is the node manifest. That is
       enough for the claim this SPEC actually rests on -- selecting a runner is
       a client choice, not a second protocol -- and it is why the branch in
       ``hmd-ms-deployment`` sits **before** ``generate_deploy_workflow`` rather
       than inside ``submit_workfow``.

       **The open questions are closed.** Authentication: none, and
       deliberately. Argo's bearer token exists because its server is reachable
       across a cluster; the runner publishes no host port -- the
       exclusive-publisher rule permits only the proxy -- so it sits inside the
       same trust boundary as every other control-plane service, where a token
       would secure nothing and would need somewhere to live. An in-flight
       workflow when the control plane stops: the runner stops listening, waits
       up to 30 s for running nodes, and a record still marked ``Running`` when
       a later process reads it is recovered as failed rather than left holding
       its environment forever.

       **Two things this SPEC did not anticipate.** Node execution runs in a
       container now, and it launches projectbuilder as a *sibling* through the
       Docker socket -- so every bind mount it composes is resolved by the host
       daemon, not inside its own filesystem. ``HMD_HOME`` and
       ``HMD_REPO_HOME`` are therefore mounted at their own absolute paths, and
       overlay workspaces moved out of the system temp dir and under
       ``HMD_HOME``, so the two path spaces coincide and the existing workspace
       code needs no translation. And the in-process runner is **not** removed:
       the control-plane bootstrap deploys ``hmd-ms-deployment`` as its last
       node, so routing it through a service that is itself part of the control
       plane would be circular.

    **Today the CLI pulls.** ``bom_seeder`` posts ``apply_changeset`` with
    ``skip_async: true`` -- a payload boolean whose only function is to stop
    ``hmd-ms-deployment`` from doing what it does in the cloud, namely build an
    Argo ``Workflow`` and ``POST`` it to the Argo server -- then posts
    ``generate_local_deployment/<csd_nid>``, receives a node list, and executes
    it in-process inside the CLI. The deployment therefore lives and dies with
    the CLI process, and the control plane has no idea a workflow engine
    exists.

    **This proposal inverts it.** ``nsrunner`` is a small Go service, built
    from this repository and run as a container in the control-plane compose
    project, exposing a submit API deliberately shaped like the one
    ``deployment_manager.submit_workfow`` already speaks::

        POST /api/v1/workflows/<namespace>
        {"namespace": "...", "workflow": {...}}      -> {"metadata": {"name": ...}}
        GET  /api/v1/workflows/<namespace>/<name>    -> status
        GET  /api/v1/workflows/<namespace>/<name>/log

    ``hmd-ms-deployment`` gains a runner selection alongside its Argo client;
    ``skip_async: true`` is retired as the local marker in favour of the
    control plane simply having a different workflow endpoint configured. The
    changeset deployment records the returned workflow name exactly as it
    records Argo's.

    **Node execution is inherited verbatim, not redesigned.** For each node
    the runner performs what ``_execute_in_projectbuilder`` does today:

    - ``image_cache.ensure_lambda_image`` equivalent -- stage the bare
      ``<repo>:<version>`` tag in the host Docker cache (Floci runs Lambdas off
      the host daemon and has no ECR).
    - Import the image into the k3s node's containerd
      (``docker save | ctr images import``).
    - Resolve the repo working tree; apply the ``src/local/`` overlay into an
      isolated temporary copy -- **the developer's tree is never mutated** --
      or let ``deploy_local.sh`` replace the script entirely.
    - Insert ``--local`` after the ``hmd ... deploy`` subcommand so
      ``hmd-cli-deploy`` takes source from the mount rather than the absent
      Artifact Librarian.
    - ``docker run --rm --entrypoint bash`` the
      ``hmd-img-projectbuilder`` image on the platform network, with the script
      passed as a **mounted file** (generated scripts routinely exceed
      ``ARG_MAX``), injecting ``AWS_ENDPOINT_URL``, ``HMD_ENVIRONMENT=local``,
      ``HMD_HOME=/root/hmd``, ``HMD_DEPLOYMENT_SERVICE_URL``,
      ``NS_LOCAL_PROXY``, ``HMD_LOCAL_K3S_CLUSTER_NAME`` and ``HMD_DID``, and
      mounting the Docker socket, the rewritten kubeconfig, and the workspace.
    - On success, read ``meta-data/resources_output/`` from the workspace and
      submit the NERD0004 Resources to ms-deployment.
    - Report status by ``set_deployment_status/<rid>/<status>`` and
      ``set_change_set_deployment_status/<csd>/<status>``.

    That last point is what makes the inversion tractable: **status already
    flows over REST**, not through the CLI's memory, so moving execution out of
    the CLI changes who calls those endpoints and nothing else.

    Consequences worth stating: a deploy survives the CLI exiting;
    ``nsctl env start`` can attach to and stream an in-flight deployment rather
    than owning it; and a second ``nsctl env start`` against the same
    environment can be refused by the runner rather than racing.

    *Both of this SPEC's original open questions -- the submit path's
    authentication, and what becomes of an in-flight workflow when the control
    plane stops -- are answered in the warning above, and the paragraph that
    asked them here has been removed rather than left to contradict it.*

    *The service-submits path was run on 2026-09-07 and does not work.* Both
    paths are wired -- ``HMD_WORKFLOW_RUNNER_URL`` is set on
    ``hmd-ms-deployment`` at bootstrap and reconciled in both directions on
    every warm start -- and with ``HMD_LOCAL_SERVICE_SUBMIT=1`` the CLI reaches
    ``waiting for the control plane to submit the workflow...`` and no workflow
    ever comes.

    The obstacle is not Floci's Lambda emulation, which Phase 4 assumed it would
    be. ``apply_changeset`` applies the changeset, logs "Asynchronously
    deploying ChangeSetDeployment" and self-invokes through
    ``call_deploy_change_set_deployment``, which builds its own event::

        payload = {
            "path": f"/apiop/deploy_change_set_deployment/{identifier}",
            "httpMethod": "POST",
            "body": None,
            "headers": {"Authorization": auth_token},
            ...
        }

    Locally there is no auth, so ``auth_token`` is ``None``, and Mangum's API
    Gateway handler does
    ``[[k.encode(), v.encode()] for k, v in headers.items()]`` --
    ``AttributeError: 'NoneType' object has no attribute 'encode'``. The
    deploying invocation dies before it submits anything, and because it is an
    ``Event`` invocation the failure reaches only that Lambda's own log:
    ``apply_changeset`` has already returned 200, so the CLI waits out its full
    timeout for a workflow that was never going to be created.

    A cloud caller passes a real token, so this is local-only. The fix belongs
    in ``hmd-ms-deployment`` -- omit the header when there is no token -- and
    cannot be made here.

    **It is not one line, which this document asserted three times before
    anyone counted.** The same ``{"Authorization": auth_token}`` literal
    appears at three sites in ``deployment_manager.py``:
    ``call_deploy_change_set_deployment``, the one named above;
    ``call_deploy_destroy_changeset_deployment``, which builds the identical
    event for the destroy path and dies the identical death; and the
    ``callback_url`` POST in ``update_change_set_deployment_status``, where it
    is ``requests`` rather than Mangum that refuses a ``None`` header value,
    and the ``try``/``except`` around it turns the refusal into a warning
    nobody reads. Fixing only the first would have left the destroy path
    failing the same way and looking unrelated.

    *Written 2026-09-08, and not yet run.* A ``_auth_headers`` helper returning
    ``{}`` when there is no token replaces all three, and the two signatures
    that declared ``auth_token: str`` while every caller passed
    ``Optional[str]`` now say ``Optional[str]``. Seven unit tests cover it,
    including two that reproduce Mangum's ``v.encode()`` over the event the
    self-invocation actually builds -- the assertion being that the header is
    *absent*, not that it is empty, since an empty string encodes fine and
    would have hidden the bug. **The end-to-end run is still owed**: it needs a
    control plane whose ms-deployment is answering, and the machine this was
    written on has its control-plane Postgres in Floci state ``failed``. Until
    that run happens ``HMD_LOCAL_SERVICE_SUBMIT`` stays opt-in, which it always
    was, and the CLI-submits path is the one every deploy in this document
    took.

    ``nsctl``'s own message was wrong too and is fixed. It said "the control
    plane may not be configured with ``HMD_WORKFLOW_RUNNER_URL``" -- the one
    thing ``syncRunnerSelection`` guarantees, and which this code path cannot be
    reached without, since the runner has to be answering for the CLI to be
    waiting at all. ``NoWorkflowError`` names the cause and the string to grep
    the Lambda log for.

.. spec:: Parallel node execution respecting dependencies
    :id: HMD_CLI_NERD002_SPEC011
    :links: HMD_CLI_NERD002
    :status: amended

    .. warning::

       **Fail-fast cannot cancel the context the nodes run under.** This SPEC
       asks for both halves of one sentence -- cancel the ``context`` so no
       unstarted node begins, *and* let in-flight nodes finish and report --
       and they contradict each other in this implementation.
       ``internal/container`` runs ``docker run`` through
       ``exec.CommandContext``, so cancelling that context kills the deploys
       already in flight. A node killed part-way through ``terraform apply``
       or ``helm upgrade`` leaves state worse than one allowed to finish, and
       avoiding exactly that is why the second half of the sentence is there.

       So the gate is a flag on the scheduler: after a failure it dispatches
       nothing further, and the nodes already running keep the context they
       started with. The observable behaviour SPEC011 asks for is unchanged --
       no unstarted node begins -- and the context is still what stops
       dispatch when the *runner itself* is shutting down, which is the one
       case where killing in-flight work is the point.

       **A node that succeeds after another has failed still counts as
       landed.** ``Succeeded`` is what the reconcile snapshot is built from,
       and it must name exactly what is deployed. Dropping a node that
       genuinely finished because a sibling failed would redeploy it on every
       subsequent run.

       **Interleaved logs needed a decision, not a default.** Every node
       writes through one ``logBuffer``, and that buffer is what
       ``nsctl env start`` and ``nsctl env attach`` put on screen. Four
       concurrent nodes shred each other's narration. Output is therefore
       prefixed per instance (``[hmd-vpc] deploying ...``), and only when more
       than one node can be in flight. The alternative -- a buffer per node,
       flushed on completion -- was rejected because the container's own
       output is not streamed at all (``Docker.Run`` collects it and returns
       it at the end), so these progress lines are the only live signal a
       watching user has; withholding them until a node finished would leave a
       multi-minute deploy silent.

       **A cycle is reported rather than hung on.** Nothing upstream should
       emit one, but a scheduler that waits forever on an unsatisfiable
       in-degree also holds its environment's mutex forever, which is a far
       worse failure than a wrong answer. When no node is running and none is
       runnable, the workflow fails naming the instances that never became
       runnable.

       **The per-environment mutex arrived early.** It landed with the runner
       service in Phase 4 -- ``Store.ActiveFor`` plus the submit lock -- so a
       second submission against a busy environment is refused ``409`` rather
       than interleaved.

       **Two nodes of one repo class must not run together, and the DAG does
       not say so.** In-degree scheduling over the payload's ``dependencies``
       is necessary and not sufficient: nodes of the same RepoClass share a
       working tree, and therefore share its ``cdktf.out``. Three
       ``hmd-inf-s3bucket`` instances dispatched at once and one node's
       ``rmtree("cdktf.out")`` deleted the provider directory another was
       walking, killing the deploy with ``FileNotFoundError: 'linux_arm64'``.
       An environment declaring six buckets and four database accounts meets
       this every run.

       It is not a dependency -- there is no edge to add, and adding one would
       impose an order where none exists -- it is a shared resource, so the
       scheduler carries it: a class is busy while one of its nodes runs, and
       ``take()`` passes *over* a ready node whose class is busy rather than
       stopping at it. Stopping would idle every worker behind the first
       bucket; passing over keeps unrelated repos overlapping, which is the
       whole of what this SPEC bought. Freeing the class is separate from
       releasing the dependency edge: a failed node frees its class too, or one
       failure strands every sibling while dispatch drains.

    ``LocalWorkflowRunner.run()`` is a strictly sequential ``for node in
    nodes:`` that returns at the first failure. It has no threads, no
    ``concurrent.futures``, and no use of the edge data.

    **The edges are already in the payload.** Each node emitted by
    ``generate_local_deployment`` is
    ``{instance_name, repo_class_name, version, rid_nid, script, dependencies}``,
    where ``dependencies`` is ``get_active_dependencies(...)`` -- the
    instance's real ``RepoInstanceReqRepoInstance`` edges, transitively
    reduced by ``build_deployment_dag`` and filtered to the instances actually
    present in this changeset. The current runner simply never reads the field.

    **Therefore parallelism requires no change to ``hmd-ms-deployment``.**
    ``nsrunner`` maintains an in-degree map over ``dependencies``, dispatches
    every in-degree-zero node to a bounded worker pool, decrements successors
    on completion, and on the first failure cancels the ``context`` so no
    unstarted node begins while in-flight nodes are allowed to finish and
    report.

    Parity targets taken from the cloud path, which already does exactly this
    through Argo: a default concurrency of **4** (Argo's
    ``spec.parallelism: 4``), and a **per-environment mutex** mirroring the
    workflow-level ``synchronization.mutex`` keyed on the deployment set, so
    two submissions cannot interleave on one environment. Concurrency is
    overridable per submission.

    **Granularity: a RepoInstance is the unit, and is not divisible.** One
    instance is one DAG node is one generated bash script is one
    ``hmd ... deploy`` invocation -- in the cloud and locally alike. There are
    no sub-steps modelled anywhere, so any intra-instance concurrency would
    have to come from inside ``hmd-cli-deploy`` / ``-helm`` / ``-cdktf`` and is
    out of scope.

    Nodes for the core repo class (``hmd-cli-neuronsphere``) remain a pure
    status flip that executes nothing, and destroy manifests keep their
    reversed edge direction, arriving already reversed from the service. Both
    still go through the scheduler: the core node executes nothing but must
    still flip its status and release its successors, and a reversed destroy
    graph is scheduled by the same in-degree map without being reversed again.

    **What was built.** ``nsrunner``'s ``execute`` builds the in-degree map
    from each node's ``dependencies``, dispatches every in-degree-zero node to
    a bounded pool, and releases successors as each completes. A dependency
    naming an instance outside the changeset is treated as already met -- the
    field arrives filtered to the instances present, so an edge that survived
    to something absent points at something already deployed. Concurrency is
    ``Manifest.parallelism`` on the submission, defaulting to 4 and never
    exceeding the node count; ``HMD_LOCAL_RUNNER_PARALLELISM`` is how a user
    reaches it, and ``=1`` is the way back to a sequential deploy for anyone
    bisecting a problem that only appears under concurrency. The in-process
    fallback runner (used when no runner service is answering) is unchanged
    and remains sequential.

    *Measured:* six independent nodes through the real container boundary,
    submitted twice against the same runner -- 21 s at ``parallelism: 1``
    against 8 s at the default 4, both Succeeded. ``test/verify-nsrunner.sh``
    performs this comparison, and checks separately that a diamond's edges are
    honoured and that its two independent middle nodes genuinely overlap.

.. spec:: Coexistence with the Python CLI
    :id: HMD_CLI_NERD002_SPEC012
    :links: HMD_CLI_NERD002
    :status: partly implemented

    The Python ``hmd neuronsphere`` surface is **not** deprecated by this
    proposal and is not removed on any schedule set here. Both front ends
    operate on the same state and must remain interchangeable mid-project:

    - the same ``$HMD_HOME/.cache/neuronsphere/environments.json`` (SPEC003),
    - the same ``$HMD_HOME`` layout, cache directories and nginx fragments,
    - the same ms-deployment graph and the same ``applied-changeset.json``
      digests (SPEC009),
    - the same Docker network, compose project names and container names.

    A user may run ``nsctl env start dev`` and then
    ``hmd neuronsphere status --env dev``, or the reverse, in either order. The
    acceptance test for each ported command is exactly this: perform the
    operation with one front end, verify with the other.

    This mirrors what ``hmd-cli-bartleby`` did -- the Go binary shipped, the
    Python source stayed in the repo as legacy, and the switch was made by
    documentation rather than by removal.

    The existing Robot Framework suites under ``test/`` invoke the CLI as a
    subprocess and remain the behavioural specification; a parallel suite
    parameterised on the binary under test (``hmd neuronsphere`` vs ``nsctl``)
    is the parity harness.

    *Built 2026-09-07, for the core and no more.* ``test/nsctl_parity.robot``
    performs each operation with one front end and verifies it with the other,
    in both directions, for ``env start``, ``env stop``, ``status``,
    ``env purge`` and the registry. **That is the whole of it.** Full parity
    across every verb is a much larger suite, and claiming this one covers it
    would repeat the mistake Phase 4 made about the runner: recording a
    deliverable as met because part of it was.

    *The suite had never finished a run. Finishing one found four failures --
    three of them in the suite -- and a second run found two more.* This is the
    same shape as everything else that landed on 2026-09-07: written,
    unit-reasoned, unrun. It now completes green: six passed, one skipped by
    design, exit 0.

    - **A test asserted the opposite of its own documentation.** "Both Front
      Ends Report The Same Environment Status" opens "Not a string comparison --
      the two render differently on purpose", and then asserted the environment's
      name appears in *both* outputs. The Python's extend-mode status renders
      ``Mode: extend`` and the registered HMDMS services, and does not print the
      environment name at all. nsctl's rendering is still checked for the name;
      the Python is checked for its verdict, which is the only thing the two are
      claimed to agree about.

    - **A purge assertion matched prose.** ``Registry Should Not List`` used a
      substring search, and the Python's empty-state message is "No **local**
      environments yet" -- so the default environment reads as still listed
      precisely *because* the listing is empty. Line-anchored now: a listing row
      starts with the slug, prose about it does not.

    - **That failure took the next test with it.** The rebuild lived in the test
      body, so a mid-test failure skipped it and left the environment purged;
      the following test's ``down --purge --env local`` then correctly refused an
      environment that no longer existed. The rebuild is a ``[Teardown]`` now, so
      a destructive test restores the platform whether it passes or not.

    - **The one that was not the suite's fault:** Robot SIGKILLed
      ``hmd neuronsphere up`` at twenty minutes, reporting rc ``-9``, which reads
      as a parity failure and is a stopwatch. The timeout is 45 minutes now, and
      a named variable rather than a literal repeated in two keywords.

      The reason is worth stating, because it makes the suite permanently
      lopsided: **the two front ends' start verbs are not the same size.**
      ``nsctl env start`` brings up the substrate -- core instance, VPC,
      database, cluster, and the ext-secrets pair -- and stops, because every
      workload above that is a RepoClass a user adds. ``hmd neuronsphere up``
      seeds its own plugin-derived BOM and deploys the lot: airflow, argo, the
      transform broker, cert-manager, trino, superset and the rest.

      Raising the timeout to 45 minutes did not make it pass either; the second
      run was SIGKILLed at 45 with pods still coming up. So that test is now
      opt-in, behind ``-v DEEP:True``, with the measurement in its own
      documentation. A test that cannot pass is worse than one that says why it
      did not run, and burying half an hour inside a suite people are told to
      run is how a suite stops being run at all. **It remains unproven**, which
      is the honest position: the case it covers -- reading an environment the
      Python brought up -- is the case every existing user is in, and this
      document should not claim it has been checked by a suite that has never
      completed it.

    - **The two purge verbs are not the same operation**, which this suite
      asserted before anyone ran it. ``nsctl env purge`` destroys an environment
      *and* unregisters it -- SPEC001 made it "the teardown ``env delete``
      refuses to be". ``hmd neuronsphere down --purge --env`` destroys the state
      and **keeps** the registration, so another ``up`` brings the environment
      back. Both are defensible; they are not interchangeable, and a test
      demanding the second behave like the first was testing a symmetry that had
      never been claimed. What must agree is everything else: the resources are
      gone from Docker, and both front ends read the same registry, so an
      environment the Python left registered is one nsctl still lists. That is
      what it checks now.

    *``env purge`` added after the verification pass, and the cost is recorded
    rather than discovered.* It is the newest verb, the one where a leftover is
    the entire failure mode, and the one whose first real run left seven
    containers, three volumes and the platform network behind. Each direction
    destroys the environment and rebuilds it, so covering it adds two cold
    starts to every run of the suite -- and the rebuild is itself the
    assertion, because a purge that left something behind surfaces as a
    bootstrap failure rather than silently. The purge tests also ask Docker
    directly, not just the two registries: what a purge leaves is not something
    either CLI is in a position to report.

    *Two registry cases added 2026-09-08, and run.* ``env add`` and
    ``env delete`` were outside the five states and are the cheapest parity
    claim in the suite -- ``nsctl env add`` says of itself that it "only writes
    the registry", so both directions cost a registry write and no provisioning
    at all. That makes them worth having rather than merely cheap: the registry
    is SPEC003's entire subject, and a slug one front end registers and the
    other cannot see is the failure SPEC003 exists to prevent. Nothing had
    asked. A second case asserts both front ends refuse the same malformed
    slug, since the slug is what every derived name is computed from once and
    never recomputed.

    They were run rather than reasoned about, and the first run failed --
    which is the point. ``nsctl env delete`` requires ``--yes``, refusing with
    exit 2 and naming the account and port slot it would free; the test passed
    no flag, so the delete was refused, and the teardown omitted it too and
    left the scratch environment registered. The right default for a verb that
    frees a slot another environment will later be given, and invisible to
    anyone who only reads the code. The case now asserts *both* halves -- the
    refusal without the flag and the deletion with it. A scratch slug is used
    throughout, never ``${ENV}``: a registry test able to unregister the
    environment the rest of the suite starts and stops is one that can take the
    suite with it, which is how the first complete run failed.

    That leaves the uncovered set smaller and still real: ``env apply``,
    ``env attach``, the four ``repo`` verbs, ``control-plane
    start|stop|status``, and ``authd``. SPEC012 stays **partly implemented**
    and names them rather than rounding up.

    It needs a real platform by definition, so it sits with the Docker-dependent
    suites and is not wired into ``make test-cli`` or CI. ``make test-parity``
    requires ``NSCTL_PARITY_ENV`` to be named explicitly: it starts and stops a
    real environment, and ``HMD_HOME`` alone is not consent, being set in every
    shell anyone works in. That guard was added after running the suite at a
    live platform by reflex during its own development.

.. spec:: Build and distribution
    :id: HMD_CLI_NERD002_SPEC013
    :links: HMD_CLI_NERD002
    :status: amended

    .. warning::

       **Implemented, with one target dropped and one channel narrowed.** All
       four channels exist; two of them are not quite what this SPEC described.

       **windows/amd64 is gone, and was never going to work.** ``nsctl`` drives
       Docker by shelling out to the CLI, mounts ``/var/run/docker.sock``, and
       composes host absolute paths that a sibling container must resolve
       *identically* -- that last one is the whole reason the runner binds
       ``$HMD_HOME`` and ``$HMD_REPO_HOME`` at their own absolute paths
       (SPEC010). None of the three has a Windows equivalent, so the archive
       this SPEC asked for would have installed and then failed at the first
       command that touched Docker. Publishing a target that cannot run is
       worse than not publishing it. Released targets are ``darwin`` and
       ``linux`` on ``amd64`` and ``arm64`` -- the set ``hmd-cli-bartleby``
       ships -- and WSL2 users take ``linux/amd64``, which is what they were
       going to get anyway.

       **The Docker image is built, not published.** This SPEC listed "Docker
       image for CI" alongside three channels that publish. It is instead the
       DAG-runner image (``hmd-img-nsrunner``), which ``nsctl`` builds on
       demand from sources embedded in the binary, and whose ``ENTRYPOINT`` was
       split from its ``CMD`` so the same image is also a CLI: ``docker run
       <image> env list``. Nothing pushes it to a registry. That closes a
       promise the repository was not keeping -- the compose file defaulted
       ``HMD_NSRUNNER_IMAGE`` to ``ghcr.io/neuronsphere/hmd-img-nsrunner:stable``,
       a tag that does not exist -- and it is what lets a Homebrew install
       enable the runner at all, since a released binary has no working tree to
       build from.

       **Two things the version story needed that this SPEC did not say.**
       ``-s -w`` is load-bearing, not tidiness: 34.4 MB stripped against
       49.4 MB unstripped, so a GoReleaser build without them meets the 50 MB
       target by 0.6 MB instead of 15. And the repository has *two* version
       spaces -- ``meta-data/VERSION`` (``0.5``, which ``make install``
       injects) and ``v``-prefixed release tags (``v0.5.0``, which GoReleaser
       injects) -- which coexist on one history alongside BACON pipeline tags
       (``0.5.202``) that release nothing. GoReleaser is configured to ignore
       the pipeline tags, and ``docs/nsctl.rst`` tells a user which of the two
       they are looking at.

    **Build.** A repository-root ``Makefile`` with ``build``/``test``/``vet``/
    ``tidy``/``clean`` targets, binary at ``src/go/nsctl/build/nsctl``, version
    injected via ``-ldflags "-X main.version=$(VERSION)"`` read from
    ``meta-data/VERSION`` -- the pattern used by ``hmd-cli-bartleby``,
    ``goblins`` and the ``go_cli_repo`` cookiecutter. ``build.commands`` in
    ``manifest.json`` stays ``[["python"]]``; the Go build is not wired into
    BACON, exactly as in the other Go repos here. ``pre_build_artifacts`` runs
    first so ``//go:embed`` has files to embed (SPEC006).

    Go 1.25, cobra v1.8.0, CGO disabled.

    **Distribution.**

    1. **Homebrew tap** -- ``neuronsphere/tap/nsctl``, the channel
       ``bartleby`` already uses. Shipped as a cask rather than the formula
       ``bartleby`` uses, because GoReleaser deprecated ``brews:``; casks are
       macOS-only, and Linux users take the install script.
    2. **GitHub Releases** via GoReleaser: ``darwin/{amd64,arm64}``,
       ``linux/{amd64,arm64}``, ``windows/amd64``. *Windows dropped -- see the
       amendment above.*
    3. **Install script** -- platform-detecting curl-pipe-sh.
    4. **Docker image** for CI. *Built on demand, not published -- see the
       amendment above.*

    ``nsctl version`` reports the binary version and, when a control plane is
    reachable, the ms-deployment version it is talking to.

    Binary size target under 50 MB including embedded artifacts (``kubectl``
    ~49 MB, ``helm`` ~46 MB for reference). The target is comfortable precisely
    because SPEC008 vendors neither ``client-go`` nor the Helm SDK.

.. spec:: Gaps, risks, and mitigations
    :id: HMD_CLI_NERD002_SPEC014
    :links: HMD_CLI_NERD002
    :status: amended

    **RETIRED -- HIGH: reconcile digest compatibility.** If ``entry_hash`` /
    ``definition_hash`` differ by so much as key ordering, every existing user's
    first ``nsctl env start`` becomes a full redeploy of their environment
    -- tens of minutes -- and reads as a bug. Mitigation: golden vectors
    captured from the Python implementation, and an explicit
    ``--force-full-redeploy`` escape hatch rather than a silent fallback.

    This landed as specified and the risk is closed. The vectors are generated
    by *calling* ``change_set_builder.entry_hash``, not by transcribing it, and
    they cover the four ways Go's encoder differs from CPython's: separators,
    ``ensure_ascii``, float repr, and HTML escaping. They caught one real
    divergence -- Go models ``repo_class_version`` as a string, so an
    unresolved version flattens to ``""`` where Python hashes ``None`` as
    ``null``.

    Worth recording for anyone extending this: the vectors test the hash
    function, not what is fed to it. A second, invisible variant of the same
    bug survived them -- versions were being resolved *after* the plan was
    computed, so every digest covered an empty version and a version bump was
    undetectable. It took running against a live platform to find.

    **GAP: a RepoClass may not declare what its plugin did.** SPEC005 asserts
    that ``manifest.json`` plus ``meta-data/resources/*.yaml`` cover what
    ``nsplugin.json`` declared. That is true of the foundation services'
    ``service_config`` and of the librarian parameters, both of which are read
    from the manifest. It is not universally true: ``hmd-ms-artifact-lib``
    reads ``BUCKET_NAME`` and its bucket is declared **only** in
    ``src/local/nsplugin.json``. ``nsctl`` bootstraps it, says it will not
    serve until the bucket is declared in the manifest, and continues.

    Reported rather than closed on purpose. Inventing a bucket name in
    ``nsctl`` would hide the gap and point the librarian at a bucket nothing
    else uses; the fix is a declaration in that repo. This is the concrete
    instance of the residual risk SPEC005's audit predicted, and the pattern to
    follow for any others: name the missing declaration, do not substitute for
    it.

    **CLOSED 2026-09-07, and the fix above was wrong.** The paragraph that stood
    here prescribed a literal ``BUCKET_NAME`` under
    ``deploy.default_configuration`` in ``hmd-ms-artifact-lib``, and asserted
    that "this is a change in another repository and nothing in this one can
    close it". Both claims were false, and the second one is why this sat open.

    The cloud does not declare that name anywhere.
    ``hmd_lib_cdktf_factories.librarian_base.get_full_bucket_name`` *derives* it
    from the librarian's own required ``lib-repo`` dependency on
    ``hmd-inf-s3bucket``::

        f"{lib-repo.instance_name}-{lib-repo.repo_name}-{lib-repo.deployment_id}-"
        f"{environment}-{hmd_region}-{customer_code}"

    and ``hmd-inf-s3bucket``'s own local stack names the bucket
    ``self.base_name.replace("_", "-")``, which is ``make_standard_name`` of the
    same six parts. So the RepoClass *does* name the bucket -- through a
    dependency rather than a literal -- and the declaration this SPEC asked for
    would have been a second source of truth for a value that already has one,
    free to drift from the bucket that actually gets created.

    ``floci.LibrarianBucketName`` reads the dependency out of the manifest and
    derives through ``tools.ResourceIdentifier``, matching the repo that
    *creates* the bucket rather than the one that reads it; the bootstrap then
    creates it with the ``EnsureBucket`` the provisioner already uses for the
    tfstate bucket, because ms-deployment is the last node of that bootstrap and
    there is nothing yet to submit a BOM entry to. Verified on a cold bootstrap:
    ``lib-repo-hmd-inf-s3bucket-cp-local-reg1-hmdtr1`` exists in Floci and is set
    on the deployed Lambda.

    The half this SPEC got right survives and is the general rule: a librarian
    that declares no bucket dependency at all still gets the warning and no
    guess. Naming a missing declaration beats substituting for it -- what was
    wrong was believing there was no declaration to read.

    Also worth recording, because it changes what the gap *was*: the Python CLI
    does not set ``BUCKET_NAME`` in extend mode either. Its only assignment is
    ``local_plugin_loader``'s, on the platform-mode path, from
    ``nsplugin.json``'s bare bucket name. The local artifact librarian had been
    equally unserved under both front ends for as long as extend mode has
    existed.

    **GAP: the RepoClass declares cloud wiring, which must be resolved.** A
    service's declared engines name a Secrets Manager secret by dependency and
    route through pgbouncer; locally there is neither. Left unresolved the
    service starts and fails every request with "Secret
    dependency:db-credentials not found in PS or SM". ``LocalizeServiceConfig``
    resolves postgres to a direct connection, gremlin to the graph alias, and
    fills in the DynamoDB table name CDKTF would have supplied. Declared values
    are never overridden, only gaps filled -- so this stays a translation of
    the RepoClass rather than a second source of truth.

    **GAP: a control plane's containers are not namespaced by ``HMD_HOME``.**
    ``hmd_proxy``, ``floci``, ``hmd_deployment_gui`` and ``hmd_nsrunner`` are
    fixed container names, so two ``HMD_HOME``\ s cannot run control planes
    concurrently even though their networks, compose projects and Floci data
    directories are all distinct. Starting one recreates the other's containers
    pointed at the new home. Not introduced by this port -- the names come from
    the compose file -- but ``nsctl`` makes it easier to hit, and nothing
    currently detects it.

    *Detected since 2026-09-07, and still not fixed.* ``compose.CheckOwnership``
    compares each container's ``com.docker.compose.project`` label against this
    project before anything is created, and ``control-plane start`` refuses
    (exit 3) naming both container and both ``HMD_HOME``\ s. ``upService``
    inspected by name and consulted only its config hash, so the ownership label
    was being written on every create and read back nowhere.

    **Say plainly what this does not do: the collision remains.** Two
    ``HMD_HOME``\ s still cannot run control planes at the same time. They now
    fail loudly instead of eating each other, which is what "detected and named,
    not silently mishandled" asks for and is not the same as being fixed.

    Namespacing the names is the fix and is a breaking change with no obvious
    seam: the compose file's own service aliases, the nginx fragments, the Robot
    suites, the ``/etc/hosts`` guidance and the Python CLI all address these
    containers by name, and they would have to move together. Floci's network
    *alias* (``neuronsphere``) cannot move at all -- it is baked into the
    presigned URLs Floci hands out -- so a rename would have to distinguish the
    container name from the alias, which today are deliberately close.

    This was chosen over the rename deliberately, not for cost: a refusal is
    correct on its own terms, while a half-completed rename would leave the same
    silent overwrite behind a name nobody recognises.

    **GAP: only an environment named** ``local`` **can deploy.** ``ms-deployment``
    passes an environment's own slug as ``hmd deploy --environment``, because a
    local ``Environment`` entity is typed by its slug -- deliberately, in both
    front ends. ``hmd-lib-cdktf`` reads that same value as the AWS deployment
    *tier* and enables path-style S3 addressing only when it is literally
    ``"local"``, so any other name addresses Floci virtual-host style and the
    environment's first CDKTF node dies in ``tofu init``::

        Failed to get existing workspaces: ... Get
        "http://hmd.000000000001.reg1.tfstate.neuronsphere:4566/?list-type=2..."
        dial tcp: lookup hmd.000000000001.reg1.tfstate.neuronsphere: no such host

    The fix belongs in ``hmd-lib-cdktf``, three lines under its own comment that
    "the endpoint itself comes from AWS_ENDPOINT_URL in the environment": the
    predicate should be "the endpoint is a local Floci", not "the tier is spelled
    local". Not introduced by this port, not fixable from it, and affecting the
    Python identically -- it had simply never been tried. ``nsctl env add`` and
    ``env start`` name it at both ends rather than letting it surface 955 lines
    into Terraform output. Registering, starting, stopping and purging such an
    environment all work; only deploys are affected.

    **CLOSED 2026-09-08.** ``hmd-lib-cdktf`` gained
    ``is_local_environment()`` and ``HmdCdkTfStack.is_local``, and the
    seventeen name comparisons across it and ``hmd-lib-cdktf-factories`` moved
    onto them. Verified: an environment named ``dev2`` deploys its whole
    substrate -- ``base-vpc``, ``eks-cluster``, ``environment-db`` and the
    External Secrets pair -- where before it failed on its first CDKTF node.
    The fix is in a library, so it reaches a deploy through the projectbuilder
    image; ``nsctl``'s warning stays until one ships carrying it.

    **GAP: the images Floci spawns its backends from are not published under the
    default registry.** The compose file pins
    ``hmd-postgres-base:${HMD_POSTGRES_BASE_VERSION:-stable}``,
    ``hmd-img-gremlin-server:0.3.5`` and ``hmd-img-k3s-floci:0.3.2`` under
    ``${HMD_LOCAL_NS_CONTAINER_REGISTRY:-ghcr.io/neuronsphere}``. Checked against
    both registries on 2026-09-07, only the Postgres pin resolved: the gremlin
    pin is absent from the default registry, and the k3s pin was absent from
    *both* at ``0.2``, ``0.3`` and ``stable`` -- though the image carried a
    RepoDigest, so a tag had existed and gone. Re-checked on 2026-09-11, after
    the wrapper's build pipeline was fixed: ``ghcr.io/hmdlabs`` serves
    ``hmd-img-k3s-floci:0.3.2``, which is what the pin now names, and the
    gremlin pin too. Neither registry serves all three -- the Postgres pin is
    published only under ``ghcr.io/neuronsphere``, the other two only under
    ``ghcr.io/hmdlabs`` -- and there is still no floating ``0.3`` anywhere.

    This is the one gap that contradicts the requirement's own sentence
    directly: a machine whose only prerequisite is Docker cannot provision a
    cluster or a graph if the images cannot be fetched, however self-contained
    the binary is. It is a publishing gap in the image repositories and nothing
    here can close it. What ``nsctl`` changed is that an uncached image is now
    *pulled* rather than refused -- the old guard turned "not cached" into a
    hard stop, which on a genuinely fresh machine is every image -- and the
    refusal that remains names ``HMD_LOCAL_NS_CONTAINER_REGISTRY`` as a likely
    cause, which is what it actually was.

    **SUPERSEDED -- MEDIUM: installed-package plugin contributions.** Plugin
    discovery is withdrawn entirely (SPEC005), so neither half ports and the
    bundle format this entry proposed does not exist. What replaces it in
    practice is ``nsctl repo import``, which reads an environment's deployed
    instances out of the deployment graph and writes them into its manifest --
    so an environment brought up by the Python CLI is migrated by reading what
    it actually deployed rather than by re-describing its plugins. The residual
    risk named below still stands, and is now recorded concretely as the first
    GAP above.

    The original reasoning, retained: Go cannot enumerate
    Python distributions, so the entry-point half of the plugin system does not
    port. Downgraded from HIGH by the audit in SPEC005: all four existing
    plugins' contributions are static data plus four declarative constructs, so
    the bundle format covers them without loss, and the one construct that
    could not port (``importlib.metadata`` sibling probing) is deleted rather
    than emulated. Residual risk is ergonomic, not structural -- a plugin is no
    longer discovered merely by being pip-installed, so the plugin development
    guide and each plugin's README must change in the same wave as the bundle
    conversion.

    **MEDIUM: the inverted DAG submission touches ``hmd-ms-deployment``.**
    This proposal is not purely additive -- it asks a Python microservice to
    grow a second workflow-runner target. Mitigation: the runner speaks Argo's
    submit shape, so the change is a client selection rather than a new
    protocol; and ``skip_async: true`` remains functional throughout, so the
    old path is a working fallback at every point.

    **MEDIUM: ``make_standard_name`` divergence.** A one-character difference
    in secret naming produces a runtime failure in a Python microservice, far
    from the Go code that caused it. Mitigation: a table-driven test built from
    the Python function's outputs. Landed as specified.

    A related trap is worth naming, because it cost real time: the *identifier*
    helpers are per-instance, and asking the wrong one for a resource finds
    nothing and returns no error. ``floci.GraphIdentifier`` names the
    environment's graph; the control plane's is a different instance under a
    different deployment id. Using the former for the latter left the
    control-plane graph stopped after every restart while the code read as
    correct, and surfaced four layers away as Trino failing to resolve
    ``global-graph``.

    **MEDIUM: k3s operator provisioning is intricate.** CoreDNS custom records
    pointing ``neuronsphere`` at the shared Floci, Traefik
    ingress-class patching (emulating the ``alb`` class rather than editing
    charts), node topology labels, stale-Node and orphaned-PV reaping, and
    ``cluster_incarnation_id`` fingerprinting via the ``kube-system`` namespace
    UID. All of it is plain Kubernetes API work, so under SPEC008 it becomes
    ``RunKube`` scripts and ports mechanically -- but it is where the local
    platform's hard-won bug fixes live and it must be ported wholesale, not
    reconstructed. Batching it into per-phase scripts is the part needing care:
    a step silently dropped from a batch fails later and elsewhere.

    Ported wholesale as specified, with ``RunKube`` execing into the k3s node
    rather than launching projectbuilder. One addition the Python does not
    have: a stopped container's empty ``IPAddress`` renders as the literal
    string ``invalid IP``, which is truthy -- so a CoreDNS record was being
    written for a container that was not running. Addresses are parsed now.

    **LOW: Jinja2 template syntax.** No bundled plugin defines ``templates``
    entries. User-authored templates need a syntax note; ``pongo2`` is a
    last-resort fallback.

    **LOW: Robot Framework parity suites.** Tests invoke a CLI as a
    subprocess; parameterising the binary is a fixture change.

    **RETIRED -- MEDIUM: projectbuilder becomes an earlier prerequisite.**
    This rested on kubectl running inside projectbuilder, which it cannot: no
    published tag ships one (SPEC008). Kubernetes work execs into the k3s node,
    so projectbuilder is a deploy-time dependency only, exactly as before. It
    is still pulled early, because a first deploy otherwise pays for it at the
    least convenient moment.

    A different pre-flight turned out to matter more. Floci spawns its database
    and graph backends from image references pinned into its own configuration,
    and a reference that resolves nowhere is not reported as an error: the
    instance goes to state ``failed`` while Terraform polls "Still creating..."
    indefinitely. Both references are now checked before a bootstrap deploys
    anything, so a wrong registry or version costs seconds and names the
    variable at fault.

    **GAP: two host-only helm paths are dropped rather than ported.** The direct
    ``helm upgrade --install`` of ext-secrets (dead on the default path) and the
    one-time ``helm uninstall traefik`` migration have no ``nsctl`` equivalent.
    A cluster predating the Traefik migration, or a user who disables
    ext-secrets without declaring it in the BOM, needs the Python CLI. Both
    cases must be detected and named, not silently mishandled.

    *Amended 2026-09-07: one half is moot and the other is detected.*

    **The ext-secrets half does not exist.** The direct ``helm upgrade
    --install`` it names is dead in the Python too, not merely on the default
    path -- ``k3s_operators``' own comment says so, "dead code once ext-secrets
    is part of the BOM", which it has been by default since. ``nsctl`` declares
    the same two BOM entries now (SPEC006) and bundles the repos, so it deploys
    them as ordinary DAG nodes. There is no path left to detect, and building a
    detector for one would have been work in service of a sentence rather than
    of a user.

    **The Traefik half is live and is now named.** The k3s image bakes the
    rendered Traefik chart into k3s's auto-deploying manifests, but a cluster or
    volume created before that still carries the Helm release the Python
    installed at runtime, under the same name and namespace, and the two
    collide. ``EnsureIngressController`` looks for the release's own Secret --
    ``owner=helm,name=traefik`` in ``kube-system``, which needs no helm binary
    -- and warns, naming both ``hmd neuronsphere up`` and the ``helm uninstall``
    that fixes it.

    Named rather than removed, deliberately: uninstalling a Helm release needs
    helm, which SPEC008 keeps off the host on purpose, and deleting the release
    Secret directly would leave the release's own resources behind -- a cluster
    that looks migrated and is not.

    **GAP: Platform mode is not ported.** Users on
    ``HMD_LOCAL_NEURONSPHERE_MODE=platform`` stay on the Python CLI. This is
    intentional (see Scope) and is one more reason coexistence is
    indefinite rather than transitional.

.. spec:: Phased implementation strategy
    :id: HMD_CLI_NERD002_SPEC015
    :links: HMD_CLI_NERD002
    :status: amended

    Each phase is independently shippable and independently useful. Phases 1
    and 2 are verifiable against a platform brought up by the Python CLI, which
    means the port is exercised long before it can break anything.

    **All six planned phases have landed, and three unplanned ones after
    them.** Phase 3's content changed substantially once plugin discovery was
    withdrawn; its entry below is rewritten to describe what was built rather
    than what was planned. Phases 0, 2, 4 and 6 carry corrections where the
    entry as written turned out not to match what shipped. Phases 7, 8 and 9
    were not planned at all: the first two came out of running what had only
    been reasoned about, and the third out of a capability local parity had
    never had.

    **Phase 0 -- Foundation. [LANDED]** Scaffold ``src/go/nsctl/`` from the
    ``go_cli_repo`` cookiecutter; root ``Makefile``; cobra root with
    ``--home``; ``hmd.env`` loading; ``nsctl version``; CI (build, vet, test).
    *Deliverable:* the binary builds and reports its version.

    *Corrected:* CI did not land here. What landed was a ``make check`` target
    -- ``fmt-check vet test``, commented "the CI target" -- that nothing
    invoked; the repository had no ``.github/`` directory at all until Phase 6
    added one. (``hmd-cli-bartleby`` has the same gap: its only workflow runs
    GoReleaser and never ``make check``.)

    **Phase 1 -- Read-only. [LANDED]** ``internal/registry`` (SPEC003) including the
    ``legacy_layout`` case and the literal-JSON fixture; ``nsctl env list``,
    ``nsctl env status``, ``nsctl control-plane status``.
    *Deliverable:* ``nsctl`` accurately describes a platform the Python CLI
    brought up. The registry contract is proven before anything can mutate it.

    **Phase 2 -- Lifecycle without deploys. [LANDED]** Compose invocation and port
    validation; the ``RunKube`` projectbuilder helper and the early image pull
    (SPEC008); Floci provisioning (SPEC007); k3s cluster, operators and
    kubeconfig; nginx routing; ``control-plane start|stop``; ``env start
    --no-deploy``, ``env stop``, ``env purge``. ``RunKube`` lands first in this
    phase -- k3s provisioning, ingress and route discovery all sit on it.
    *Deliverable:* ``nsctl`` brings up a control plane and an environment's
    infrastructure; the Python CLI can then complete the deploy.

    *Corrected:* ``env purge`` is listed above and did not land here. It landed
    on 2026-09-07, four phases later, and until then
    ``hmd neuronsphere down --purge`` was the only way to purge an environment.

    It is not a transliteration of the Python's, because the Python's is wrong
    in the one way a purge cannot afford to be -- see item 13 of `What
    implementation changed about the design`_.

    **Phase 3 -- BOM, manifests and the cold bootstrap. [LANDED]** Rewritten:
    the plugin half of this phase is withdrawn (SPEC005), and the cold
    bootstrap moved into it from Phase 2, where the original plan had deferred
    it as "lands with the deploy path".

    What was built: ``internal/manifest`` (the environment manifest, in the
    format ``env_manifest.py`` defines, so either front end reads what the
    other wrote); ``internal/bom`` (SPEC009) with both topological sorts and
    version resolution; ``internal/reconcile`` with golden-vector digest tests
    and ``--force-full-redeploy``; ``repoclass.Produces`` reading a repo's own
    ``meta-data/resources/*.yaml``; the control-plane bootstrap DAG; and the
    verbs ``env add``, ``env delete``, ``env apply``, ``repo add``,
    ``repo remove``, ``repo list`` and ``repo import``.

    *Deliverable:* ``nsctl`` bootstraps a control plane from an empty
    ``HMD_HOME`` and reconciles an environment to its manifest.

    *Verified:* a cold bootstrap of a fresh ``HMD_HOME`` deploys the VPC,
    Postgres, core databases, graph and all three foundation services and exits
    0, with ``hmd-ms-deployment`` answering; a second start skips the
    bootstrap. Against an environment the Python CLI brought up, ``repo
    import`` recovered 29 instances and ``env apply`` then reported "0 to
    deploy, 0 changed, 32 unchanged, 0 deployed but undeclared".

    *Not done:* ``env purge``, and the full parity harness -- parity was
    checked by targeted cross-front-end operations rather than by a suite.

    **Phase 4 -- The runner service. [DONE]** ``internal/nsrunner`` with the
    submit API and inherited node execution (SPEC010); the runner selection in
    ``hmd-ms-deployment``; ``nsctl env attach`` streaming an in-flight
    deployment.

    *Deliverable met:* a deploy survives the CLI exiting, and ``skip_async``
    stops being hardcoded -- ``HMD_WORKFLOW_RUNNER_URL`` gives the control plane
    somewhere real to submit.

    *Not done:* the fully inverted path is opt-in
    (``HMD_LOCAL_SERVICE_SUBMIT=1``) rather than default, because it routes
    through ``call_deploy_change_set_deployment`` -- an async self-invoking
    Lambda whose behaviour under Floci is unverified. The runner container is
    likewise off by default until its image is published.

    *Corrected (2026-09-07):* the deliverable above is half met, and the
    *Not done* names the wrong obstacle. **Nothing sets
    ``HMD_WORKFLOW_RUNNER_URL``.** ``hmd-ms-deployment`` reads it --
    ``deployment_manager.py`` resolves the runner from it, with tests -- so the
    service half of "the runner selection in ``hmd-ms-deployment``" did land.
    The CLI half did not: ``floci.ServiceEnv`` builds the foundation Lambdas'
    environment and does not include it, the compose file does not set it, and
    neither does the Python CLI. So the control plane has nowhere to submit,
    and ``HMD_LOCAL_SERVICE_SUBMIT=1`` cannot work at all rather than working
    unverifiably: ``env start`` seeds the changeset, waits its two minutes and
    fails with "no workflow appeared". The Lambda's behaviour under Floci
    remains unverified, but it is not what is blocking -- it has never been
    reached.

    Whoever picks this up: the variable belongs in ``floci.ServiceEnv``'s map
    for ``hmd-ms-deployment``, pointing at the runner's in-network address
    (``http://hmd_nsrunner:8080``, not the host route), and only when the
    runner is enabled -- a control plane told to submit to a runner that is not
    running would fail every deploy instead of falling back.

    *Wired (2026-09-07):* exactly that. ``ServiceEnv`` grew a ``ServiceEnvOpts``
    rather than a fifth positional argument, and sets the variable only for
    ``hmd-ms-deployment`` -- the rule lives in the one function every foundation
    Lambda's environment comes from, so no call site can hand ms-naming a
    runner. ``controlplane.RunnerInternalURL`` is now the single source of the
    address, shared with ``RunnerRoute``, which had the same string as a second
    literal.

    One thing the correction above did not anticipate: the bootstrap deploys the
    foundation Lambdas **once**, and a warm start skips it, so the variable would
    only ever have reached a control plane that had never been started -- which
    is no existing machine. ``syncRunnerSelection`` reconciles it on every start
    against the deployed function's own configuration, in both directions.

    *Run at last (2026-09-07), and it does not work.*
    ``HMD_LOCAL_SERVICE_SUBMIT=1`` against a control plane with the runner on
    reaches ``waiting for the control plane to submit the workflow...``, and no
    workflow ever comes. The obstacle this *Not done* named is real and the
    reason it guessed is not: Floci's Lambda emulation is fine.
    ``apply_changeset`` applies the changeset, logs "Asynchronously deploying
    ChangeSetDeployment" and self-invokes with a hand-built event whose headers
    are ``{"Authorization": auth_token}``. Locally there is no auth, so the
    value is ``None``, and Mangum's API Gateway handler does
    ``[[k.encode(), v.encode()] for k, v in headers.items()]``. The deploying
    invocation dies with ``AttributeError: 'NoneType' object has no attribute
    'encode'`` before submitting anything, and because it is an ``Event``
    invocation nothing surfaces -- ``apply_changeset`` has already returned 200.

    A cloud caller passes a real token, so this is local-only, and the fix
    belongs in ``hmd-ms-deployment``: omit the header when there is no token.
    Written on 2026-09-08 across the three sites that share the literal, with
    unit tests, and **not yet run end to end** -- see SPEC010. Until it is, the
    flag stays opt-in and the CLI-submits path -- which every deploy in this
    document took -- is the one that works.

    ``nsctl``'s own message was also wrong and is fixed: it blamed
    ``HMD_WORKFLOW_RUNNER_URL``, the one thing ``syncRunnerSelection``
    guarantees and which this code path cannot be reached without.
    ``NoWorkflowError`` now names the cause and the string to grep the Lambda
    log for.

    *Verified (2026-09-07):* ``syncRunnerSelection`` reconciles in both
    directions on a warm start -- toggled off, ``HMD_WORKFLOW_RUNNER_URL``
    disappears from the deployed function and the step says the service deploys
    in-process again; toggled on, it returns. The "predates this commit" case it
    was written for cannot be reproduced any more, no such control plane having
    survived the purge, but the reconcile is the same function reached the same
    way.

    *Superseded by Phase 6:* the image is not published and will not be. It is
    built on demand from sources embedded in the binary, so a Homebrew install
    can enable the runner. It stays off by default for a different reason --
    the first start with it on pays a one-to-two-minute local build -- and
    turning it on is now ``HMD_LOCAL_NEURONSPHERE_ENABLE_RUNNER=true`` and
    nothing else.

    **Phase 5 -- Parallelism. [DONE]** In-degree scheduling over the node
    payload's ``dependencies``, a bounded worker pool defaulting to the cloud
    path's parallelism of 4 and overridable per submission, and fail-fast that
    stops dispatch without killing what is already running (SPEC011). The
    per-environment mutex had already landed in Phase 4.

    *Deliverable met:* six independent nodes take 8 s at the default
    concurrency against 21 s sequentially, with identical outcomes -- measured
    end to end through the container boundary by
    ``test/verify-nsrunner.sh``, which also asserts the edges are respected.

    *Amended:* fail-fast is a dispatch gate rather than a context
    cancellation, because cancelling the context nodes execute under kills
    them mid-deploy. See SPEC011.

    *Not done, and now done:* nothing in the port measured a *real* cold
    bootstrap's speedup, only a synthetic DAG of the same shape. The prediction
    that "the real one is dominated by container pulls and Terraform, and its
    gain will be smaller" is recorded below, measured, and correct -- see
    `A real cold bootstrap, measured`_. It was not measurable until a cold start
    worked from nothing at all.

    *Amended again (2026-09-07):* the first real DAG run showed in-degree
    scheduling is necessary and not sufficient. Nodes of one RepoClass share a
    working tree and corrupt each other's ``cdktf.out``; the scheduler now
    serialises per class while unrelated repos still overlap. The synthetic DAG
    could not have caught it -- every node in it is a different instance of the
    same fake class, and none of them touches a working tree.

    **Phase 6 -- Distribution. [DONE]** GoReleaser, the Homebrew tap, the
    install script, and documentation making ``nsctl`` the recommended path for
    new users while the Python CLI remains supported. Also: the CI Phase 0
    claimed, a repository-root ``README.md`` (there was none), the contract
    suite ``make test-cli`` had been pointing at a nonexistent file for, and
    ``nsctl version`` reporting the ms-deployment version, which SPEC013 asked
    for and Phase 2a's absence had blocked.

    *Deliverable met:* ``brew install neuronsphere/tap/nsctl``, a
    checksum-verifying ``install.sh``, and tag-triggered GitHub Releases for
    four platforms -- 33.5 MB (linux/arm64), 34.6 MB (darwin/arm64), 38.0 MB
    (linux/amd64) and 38.9 MB (darwin/amd64), against SPEC013's 50 MB target.

    *Amended:* ``windows/amd64`` is dropped, and the Docker image is built on
    demand rather than published. Both are recorded in SPEC013.

    *Not done:* the recommendation is qualified rather than unconditional.
    ``nsctl env purge`` still does not exist and SPEC012's parity harness was
    never built, so ``docs/nsctl.rst`` and the README carry a "What it does not
    do yet" section naming both, and say what to use instead. Recommending the
    binary without that would have been a claim the repository could not
    support.

    *Closed (2026-09-07):* ``env purge`` exists and the parity harness covers
    the core, so both bullets are gone from the two documents. The parity one is
    replaced rather than deleted: what is now unproven is every verb *outside*
    that core, which is a smaller and more honest claim than "there is no
    harness".

    *A third bullet added, from verification:* only an environment named
    ``local`` can deploy (item 17). It is not a ``nsctl`` defect and affects the
    Python identically, but a document recommending ``nsctl env add <name>`` to
    new users has to say that ``<name>`` is not free.

    *Untested:* the Homebrew cask cannot be exercised without pushing to
    ``neuronsphere/homebrew-tap``. GoReleaser generates it and its shape was
    checked against ``hmd-cli-bartleby``'s, but no ``brew install`` has run.

    **Phase 7 -- The cold start. [DONE]** Not planned, and not optional. The
    purge that Phase 6's verification prompted produced the first empty
    deployment graph nsctl had ever seen, and five bootstrap defects with it:
    resource-definition seeding, the DeploymentSet payload, the concrete core
    Resources and their two-phase apply (SPEC009), nginx's body cap, and a
    Docker-created directory where the kubeconfig belongs. See `The cold start
    had never run`_.

    *Deliverable met:* ``nsctl env start`` brings up a control plane, an
    environment and a 28-instance manifest from a purged ``HMD_HOME`` with no
    Python CLI involved.

    **Phase 8 -- Verification. [DONE]** Not planned either, and the same kind of
    thing Phase 7 was. Six items had landed unit-tested and unrun; running them
    from nothing -- no ``HMD_REPO_HOME``, no ``$HMD_HOME``, no deployment graph
    -- produced items 16 to 22 of `What implementation changed about the
    design`_ and closed one SPEC014 gap outright.

    *Deliverable met:* the requirement's own sentence, executed. A machine with
    no checkout of any repo deploys a control plane and an environment from the
    trees inside the binary, with External Secrets reconciled and no manifest
    involved. See `Verified from nothing`_ for what passed, what failed, and
    what could not be verified here and why.

    **Phase 9 -- The local identity provider. [DONE]** Not planned either.
    Nothing on the local platform has a token, so the Rego policies every
    service authorizes against were only ever exercised in the cloud.
    ``nsctl authd`` is the token source that makes them testable here, and it
    ships as a fifth control-plane container running the same image as the DAG
    runner. See SPEC016.

    *Deliverable met:* ``nsctl authd token`` mints a JWT whose signature, issuer
    and audience PyJWT verifies against the published JWKS, and the real
    ``system/shared.rego`` from ``hmd-inf-openpolicyagent`` -- in the OPA version
    and v0 mode that chart pins -- allows ``token_is_valid`` for the right
    audience and leaves it undefined for the wrong one.

    *Not done:* the OPA authorizer end to end. ``hmd-lib-auth.verify_token``
    calls ``okta_jwt_verifier``, which rejects any issuer that is not
    ``https://`` and offers no way to turn that off, so the authorizer Lambda
    needs a certificate nothing here issues. Policy *evaluation* does not --
    every Rego bundle decodes with ``io.jwt.decode`` rather than verifying --
    which is why realistic claims were the thing worth building.

.. spec:: The local identity provider
    :id: HMD_CLI_NERD002_SPEC016
    :links: HMD_CLI_NERD002
    :status: implemented

    **Nothing on the local platform has a token.** Applications run with
    database authentication, microservices run unauthenticated, and the
    consequence is that the Rego policies every service authorizes against are
    exercised only in the cloud -- the one part of the stack local parity did
    not reach. ``nsctl authd`` is a stand-in for Okta that makes them testable
    here. It was not in the original scope and is recorded now rather than left
    to live only in ``docs/nsctl.rst``.

    **Okta-shaped, because the consumers are.** Superset and Airflow build
    their endpoints as ``OKTA_BASE_URL + "v1/token"``, and ``hmd-lib-auth``
    hands an issuer to ``AccessTokenVerifier``, which fetches
    ``{issuer}/v1/keys``. None of that is configurable, so the paths are
    Okta's: ``/oauth2/{ns,services}/v1/...``, with both discovery documents
    served because Superset and Airflow ask for ``oauth-authorization-server``
    while Trino asks for ``openid-configuration``.

    **Two authorization servers, not one.**
    ``lambda_helper._get_issuer_and_audience`` picks ``services_issuer`` with
    ``api://neuronsphere-services`` for a service account and ``ns_issuer``
    with ``api://neuronsphere`` otherwise, and every Rego bundle's
    ``token_is_valid`` admits exactly those two. One server issuing both
    audiences would have made the distinction untestable, which is most of what
    there is to test.

    **One issuer, three resolvers.** The issuer is the base URL every consumer
    appends to and is stamped into every token's ``iss``, so it has to be one
    string wherever it is read: a consumer that fetched the keys under one name
    and reads ``iss`` as another rejects every token, and the failure surfaces
    as a policy denial nowhere near the cause. ``auth.local.neuronsphere.io``
    is therefore made to resolve to ``hmd_proxy`` three ways -- an nginx vhost
    for the browser, a Docker network alias for Floci's Lambda containers, and
    a CoreDNS record for cluster pods -- all derived from
    ``HMD_LOCAL_AUTH_ISSUER`` so they cannot drift apart.

    The vhost defers its upstream through a resolver variable rather than
    naming the container literally. nginx resolves a literal ``proxy_pass``
    host once, at config load, so an absent container would fail ``nginx -t``
    and have the whole reload rejected -- taking every other route down with
    it. An optional service that is off must cost its own 502 and nothing else.

    **Off by default, and the default is load-bearing.**
    ``HMD_LOCAL_NEURONSPHERE_ENABLE_AUTH=true`` adds it, exactly as the DAG
    runner is gated by its own flag. Turning it on is what makes an application
    demand a login and a service reject an unauthenticated call, so a
    default-on switch would break every local script, ``curl`` and Robot suite
    that carries no token today. It is the **same image** as the runner --
    the Dockerfile's ``ENTRYPOINT`` is ``/nsctl`` and its ``CMD`` only a
    default, so a ``command:`` selects the other mode and nothing extra is
    built or published.

    The ``okta`` secret is written to Floci on every start rather than at
    bootstrap, for the same reason ``syncRunnerSelection`` runs on every start:
    the bootstrap happens once, and enabling the provider afterwards has to
    take effect without purging the control plane to get a second one.

    **GAP: it does not satisfy** ``hmd-lib-auth.verify_token``. That function is
    what the OPA authorizer Lambda calls before it consults a policy, and it
    goes through ``okta_jwt_verifier``, which rejects any issuer that is not
    ``https://`` and offers no way to turn that off. So the authorizer end to
    end still needs a certificate nothing here issues. Policy evaluation itself
    does not: every Rego bundle decodes the token with ``io.jwt.decode`` rather
    than verifying it, so realistic *claims* are all a policy needs -- which is
    the capability this exists for, and the reason the gap is named rather than
    worked around.

    **Say plainly what this is not.** It signs with a key it generated, mints a
    token with any claims asked of it, and registers no clients -- a client's
    secret is its id, the convention local Postgres users already follow. It is
    not an authorization server anyone should trust, and it is not reachable
    from outside this machine.

Dependency Map
--------------

.. list-table::
   :header-rows: 1
   :widths: 25 30 45

   * - Python
     - Go replacement
     - Notes
   * - ``cement``
     - ``spf13/cobra`` v1.8.0
     - House CLI framework; no viper -- ``godotenv`` plus explicit precedence
   * - ``boto3`` / ``botocore``
     - ``aws/aws-sdk-go-v2``
     - Service clients only; the ``resource`` API has no analogue and is
       already unused
   * - ``requests``
     - ``net/http`` (stdlib)
     - The ms-deployment apiop protocol; the largest behavioural surface
   * - ``pyyaml``
     - ``gopkg.in/yaml.v3``
     - Compose files, helm values, PV manifests, env manifests
   * - ``json`` (stdlib)
     - ``encoding/json`` (stdlib)
     - nsplugin.json, manifest.json, environments.json, resources output
   * - ``jinja2``
     - ``text/template`` (stdlib)
     - User-authored plugin templates only; unused by bundled plugins
   * - ``python-dotenv``
     - ``joho/godotenv``
     - ``$HMD_HOME/.config/hmd.env``
   * - ``InquirerPy``
     - ``charmbracelet/huh``
     - Plugin selection and purge confirmation
   * - ``yaspin``
     - ``charmbracelet/lipgloss`` (or plain lines under ``--verbose``)
     - Step spinners; ``--verbose`` must stay pipe-safe
   * - ``importlib.metadata``
     - (no analogue)
     - Entry-point plugins replaced by ``load-plugin`` (SPEC005)
   * - ``hmd_cli_tools``
     - ``internal/tools``
     - Only the handful actually used: ``load_hmd_env``, ``set_hmd_env``,
       ``make_standard_name``, ``get_version``
   * - ``kubernetes`` / helm clients
     - (none needed, and no ``client-go`` either)
     - Already subprocesses; SPEC008 moves them into projectbuilder rather than
       vendoring a Kubernetes client
   * - ``pg8000`` / ``psycopg``
     - (none needed)
     - Already ``docker exec ... psql``
   * - Docker SDK
     - ``os/exec`` (+ ``docker/docker/client`` for inspection)
     - Already subprocess; ``nerdctl`` selection preserved

Risk Assessment
---------------

.. list-table::
   :header-rows: 1
   :widths: 10 38 52

   * - Level
     - Risk
     - Mitigation
   * - High
     - Reconcile digests diverge, forcing a full redeploy on first use
     - Golden vectors captured from the Python implementation; explicit
       ``--force-full-redeploy`` rather than a silent fallback
   * - Medium
     - Entry-point plugin contributions have no Go analogue
     - Bundle format (SPEC005) covers all four existing plugins declaratively;
       a plugin is no longer auto-discovered by being installed, so docs and
       READMEs change in the same wave
   * - Medium
     - A plugin needs computation at load time that no primitive covers
     - Deploy-time execution already exists per instance
       (``src/local/deploy_local.sh`` in projectbuilder); an optional
       ``loader_image`` is reserved but deliberately unspecified
   * - Medium
     - Inverting DAG submission requires a change in ``hmd-ms-deployment``
     - Runner speaks Argo's submit shape; ``skip_async: true`` stays functional
       as a fallback throughout
   * - Medium
     - ``make_standard_name`` divergence breaks Python microservices remotely
     - Table-driven test built from the Python function's outputs
   * - Medium
     - k3s operator provisioning encodes many hard-won fixes
     - Port wholesale rather than reconstruct; it is subprocess work, so the
       port is mechanical
   * - Medium
     - Re-deriving names instead of reading the registry orphans containers
     - SPEC003 makes the registry authoritative; hash retained only as a
       fallback, with a cross-language equality test
   * - Low
     - Jinja2 template syntax differences
     - No bundled plugin uses templates; migration note; ``pongo2`` fallback
   * - Low
     - Robot Framework parity
     - Suites invoke the CLI as a subprocess; parameterise the binary
   * - Medium
     - projectbuilder is now needed to provision a cluster, not just to deploy
     - Pull it once, early in ``control-plane start``, with an explicit
       progress line and an actionable failure; in exchange ``docker`` is the
       only host tool required
   * - Low
     - Platform mode users left behind
     - Intentional; the Python CLI is not deprecated (SPEC012)
