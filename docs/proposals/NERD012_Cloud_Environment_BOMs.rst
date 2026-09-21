.. NERD012 Cloud Environment BOMs

NERD012 Cloud Environment BOMs
==============================

.. include:: ../_includes/paid-cloud.txt

.. req:: Build a local environment from a cloud environment's Bill of Materials
    :id: HMD_CLI_NEURONSPHERE_NERD012
    :status: implemented

    ``nsctl`` shall be able to read the **Bill of Materials of an environment
    running in the cloud** -- every deployed repo instance, its concrete repo
    class version, its configuration and its dependencies by role -- from a
    cloud ``hmd-ms-deployment``.

    It shall be able to select a subset of that BOM, copy the corresponding
    artifacts into the control plane's Artifact Librarian, and declare the
    selection in a local environment's manifest, so that the existing
    ``nsctl env apply`` deploys it unchanged.

    The addresses of the cloud ``hmd-ms-deployment`` and the cloud Artifact
    Librarian shall be configurable, per profile, in
    ``$HMD_HOME/.config/nsctl.toml``.

    Every operation in this document is a **read** of a cloud service. Nothing
    here writes to one.

    .. note::

        ``implemented`` as of 2026-09-15, on a live acceptance run against
        ``hmdtr1``'s cloud ``dev`` environment. See `The acceptance run`_ --
        which is also where the one amended acceptance criterion is recorded.

.. note::

    This is the third and last of the "reach a cloud service on the user's
    behalf" capabilities. ``NERD005`` fetches a **named** artifact; ``NERD011``
    chooses **which version** of one; this document answers the question that
    precedes both -- *which versions are known to work together, because
    something is running them.*

Motivation
----------

``NERD010`` SPEC003 makes an argument that this document takes seriously: a
known-good environment is the only thing that has ever proved a set of versions
works together, which is why ``nsctl lock --from-env`` is the strongest tier and
why resolving each range independently to its newest satisfying version is a
guess by comparison.

That argument does not stop at the machine boundary. A cloud ``dev`` or ``prod``
environment is a known-good version set that somebody is running in earnest, and
it is the obvious thing to seed a local environment from. Today reproducing one
locally means reading it by hand -- through the Deployment GUI, or
``hmd ns-bootstrap``'s ``get-deployment-bom`` -- and retyping the result as
``nsctl repo add`` lines, once per instance, with the versions transcribed.

The cost is not only the typing. A version transcribed by hand is a version that
can be transcribed wrongly, and the failure surfaces as a deploy that behaves
differently here than there -- the exact class of problem the local platform
exists to eliminate.

The evidence this document is built on
--------------------------------------

Unlike ``NERD011``, whose findings were measured against live services, **this
document was built by reading source.** No request was made to a cloud
``hmd-ms-deployment`` while it was written. That is a real difference in
confidence and it is recorded here rather than glossed: the wire *shape* below
was derived from the code that produces it, not from a response that was
observed.

Acceptance criterion 1 existed to close that gap before the decoder was
trusted. It has since been run -- see `The acceptance run`_ -- and every claim
in the table below held.

.. list-table::
   :header-rows: 1

   * - Question
     - Answer
   * - Does the deployment service expose a BOM for an environment?
     - **Yes**, in one request: ``GET /apiop/get_deployment_bom/<type>``
       (``deployment_ops.py:872``). It traverses the deployment DAG server side
       and is Redis-cached there.
   * - Does a BOM entry carry a concrete version?
     - **Yes**, as ``repo_class_version``, read from the
       ``RepoClassVersion`` reached through the instance's *current* deployment.
       ``repo_instance`` itself carries no version at all.
   * - Is the BOM a new wire format for ``nsctl``?
     - **No.** Its keys are ``change_set.definition``'s keys, which are
       ``internal/bom``'s ``Entry`` tags. The same schema read in two
       directions.
   * - Can ``internal/msdeploy`` call it today?
     - **No**, for two mechanical reasons: it sends no credentials at all, and
       its ``APIOp``/``APIOpRaw`` are POST-only while this operation is a
       ``GET`` returning a JSON *array*. See SPEC002.
   * - Does ``EnvironmentInstances`` already answer this?
     - **Not for a cloud graph.** Ten unfiltered whole-table searches, and it
       picks the current deployment by a different rule than the server does.
       See SPEC003.
   * - Is an API key required?
     - **Not normally.** ``hmd-ms-deployment``'s manifest sets
       ``use_api_key: false`` and ``create_okta_identity: true``; the OIDC token
       is the credential, and ``hmd-cli-deploy`` tolerates no API key at all.

Scope
-----

**In scope.**

- Reading an environment's BOM from a cloud ``hmd-ms-deployment``.
- Listing the environments such a service knows.
- Per-profile configuration of both cloud endpoints, with one stated
  precedence rule shared by both.
- Selecting a subset of a BOM and declaring it in a local environment manifest.
- Copying the selected artifacts into the control plane's librarian.

**Out of scope.**

- Writing anything to a cloud service. No changeset is created, no status is
  set, no instance is registered. A cloud BOM is an input to a local manifest
  and never an output to a cloud deploy -- the same direction of travel
  ``nsctl artifact pull`` established.
- Deploying. ``import`` ends by naming ``nsctl env apply``; see SPEC005.
- Diffing two environments. ``compare_environments`` exists server side and a
  ``nsctl bom diff`` is a later document.
- Fetching an API Gateway key through ``apigateway:GetApiKeys``. See SPEC002.
- Replacing ``EnvironmentInstances`` on the local path.

Reference: what already exists
------------------------------

.. list-table::
   :header-rows: 1
   :widths: 45 55

   * - Piece
     - Where it already is
   * - BOM production, DAG-ordered, server side
     - ``GET /apiop/get_deployment_bom/<type>``
   * - The BOM entry's fields
     - ``hmd-ms-deployment``'s ``deploy_bom_creator.py``
   * - Cloud endpoint composition from customer and region
     - ``internal/librarian/client.go``'s ``endpoint``
   * - The cached token, and ``x-api-key`` beside it
     - ``internal/librarian/client.go``'s ``authToken`` and ``headers``
   * - ``nsctl.toml`` and its profiles
     - ``internal/nsconfig``
   * - Turning a deployed instance into a manifest declaration
     - ``cmd/repo.go``'s ``declarationFor``
   * - Copying an artifact into the control plane
     - ``cmd/artifact.go``'s ``storeLocally``
   * - Layering a flag over the injected ``Lookup``
     - ``cmd/artifact.go``'s ``librarians.cloud``

Nothing in this document is a new subsystem. It is one new client, four new
configuration keys, and a verb that joins two existing halves together.

.. spec:: A profile names a tenant, not just an issuer
    :id: HMD_CLI_NEURONSPHERE_NERD012_SPEC001
    :links: HMD_CLI_NEURONSPHERE_NERD012
    :status: implemented

    ``$HMD_HOME/.config/nsctl.toml``'s ``[profile.<name>]`` table shall carry
    the addresses of the tenant's services alongside the issuer it already
    names:

    .. code-block:: toml

        default_profile = "acme"

        [profile.acme]
        auth_url               = "https://auth-aaa-reg1.acme-admin-neuronsphere.io/oauth2/ns"
        customer_code          = "acme"
        region                 = "reg1"
        deployment_url         = "https://ms-deployment-aaa-reg1.acme-admin-neuronsphere.io"
        artifact_librarian_url = "https://artifact-aaa-reg1.acme-admin-neuronsphere.io"

    All four new keys are optional and ``auth_url`` remains the only required
    one, so every file written before this document keeps parsing unchanged.
    The file is still decoded with unknown fields refused, so a misspelt key is
    still an error naming its line rather than a silently ignored setting.

    ``customer_code`` and ``region`` are carried as well as the URLs because
    both hostnames are *composed* from them today, and a profile that had to
    spell out two long URLs to say "the acme tenant in reg1" would be a worse
    description of the same fact.

    **One resolution rule**, shared by both services and stated once:

    #. the command's ``--url`` flag
    #. the service's environment variable
    #. the profile's explicit ``deployment_url`` / ``artifact_librarian_url``
    #. composed from ``customer_code`` and ``region`` -- the profile's first,
       then ``HMD_CUSTOMER_CODE`` and ``HMD_REGION``
    #. refuse, naming every one of the above

    The templates are
    ``https://artifact-aaa-<region>.<customer>-admin-neuronsphere.io`` and
    ``https://ms-deployment-aaa-<region>.<customer>-admin-neuronsphere.io``.
    The first is unchanged and merely moved; the second is what
    ``hmd-cli-ns-bootstrap`` and ``hmd-cli-deploy`` both compose, with
    ``admin`` hard-coded in both.

    An environment variable beating a configuration file is the ordinary
    convention and is kept. It has one sharp edge, and it gets one guard:
    **when a profile was named explicitly and an environment variable overrides
    a URL that profile also sets, that shall be reported on stderr, naming
    both.** Without it, "I passed ``--profile staging`` and reached production"
    is invisible, and it is invisible in precisely the situation -- several
    tenants on one machine -- that profiles exist for.

    **Implemented 2026-09-15** as ``internal/nsconfig``'s ``ResolveEndpoint``,
    ``Service`` and ``Endpoint``, with ``internal/logincfg`` renamed to
    ``internal/nsconfig`` first: a package called ``logincfg`` holding the
    address of an Artifact Librarian is a misnomer that costs the next reader
    ten minutes.

    Three things writing it settled that the proposal did not state.

    The deployment service's variable is ``HMD_CLOUD_MS_DEPLOYMENT_URL``, not
    ``HMD_DEPLOYMENT_SERVICE_URL``. That name is taken and means the opposite:
    ``hmd-cli-deploy`` treats it as the *local* short-circuit, and ``nsctl``
    sets it on every projectbuilder container to point at the control plane's
    own service through ``hmd_proxy``. A cloud address under that name would be
    read as a local one by half the platform.

    ``librarian.endpoint`` is **gone** rather than kept alongside the shared
    rule, and ``librarian.URLEnv``, ``CustomerCodeEnv`` and ``RegionEnv`` are
    now aliases of ``nsconfig``'s. Two spellings of one variable name is the
    kind of drift that shows up only as a client talking to the wrong host.
    ``librarian.Config`` gained ``Profile`` and ``FlagURL``, and the
    lookup-shadowing closure ``librarians.cloud`` used for ``--url`` is gone
    with them: a flag smuggled in as an environment variable cannot be told
    apart from one that was really set, which is exactly what the shadowing
    warning has to distinguish.

    ``--profile`` is bound **explicitly**, by ``librarians.bindTenant``, rather
    than falling out of the shared flag set. ``nsctl env apply`` and
    ``nsctl env add`` already have a ``--profile`` and it means something else
    entirely -- ``NERD010``'s local profiles, which select part of a
    repository's declared environment. The two have never needed to appear on
    one command; binding the tenant flag by hand is what makes that a decision
    rather than an accident, since a future command wanting both now fails to
    start instead of shipping one flag that means two things.

.. spec:: Authenticating to a cloud ``hmd-ms-deployment``
    :id: HMD_CLI_NEURONSPHERE_NERD012_SPEC002
    :links: HMD_CLI_NEURONSPHERE_NERD012
    :status: implemented

    ``internal/msdeploy`` talks to the control plane's own service over
    loopback and therefore **sends no headers at all**. A cloud client shall be
    a second constructor on the same package rather than a second package: the
    entity names, the ``Filter`` shape, the collection encoding and the error
    type are all the same service's protocol, and duplicating them would create
    two places for one fact.

    The credential is what ``hmd-cli-deploy`` and ``hmd-lib-librarian-client``
    both send:

    - ``Authorization``, set to the **raw token with no** ``Bearer`` **prefix**.
      This is not an oversight being copied; it is what ``RestClient``'s
      ``_get_headers`` does and what the service's authorizer expects.
    - ``x-api-key``, when one is configured, alongside rather than instead of
      the token.

    The token is resolved exactly as ``internal/librarian`` already resolves
    it: ``HMD_AUTH_TOKEN``, else ``$HMD_HOME/.cache/tokens.yaml``'s
    ``login.access_token`` -- the file ``nsctl login`` and ``hmd login`` both
    write. A missing or malformed file yields no token rather than an error.
    Construction fails when neither credential is available, so the refusal
    arrives before a request rather than as a ``401``.

    **The API key is read from an environment variable and is not fetched from
    AWS.** The Python obtains it with an ``apigateway:GetApiKeys`` call under a
    named profile, which needs AWS credentials, a region mapping and a boto
    client -- a substantial dependency for a value that is normally absent:
    ``hmd-ms-deployment``'s own manifest sets ``use_api_key: false``, and
    ``hmd-cli-deploy`` proceeds with ``api_key = None``. If a tenant turns API
    keys on, the variable is the escape hatch.

    Two mechanical additions are needed beyond credentials, and both are
    properties of this one operation:

    - ``get_deployment_bom`` is declared ``GET`` while ``APIOp`` and
      ``APIOpRaw`` are POST-only, so a ``GET``-capable raw variant is required.
    - It answers with a JSON **array**, which today's ``APIOp`` silently
      discards into an empty map. That is the failure mode worth naming: it
      does not error, it returns nothing.

    **Implemented 2026-09-15** as ``msdeploy.NewCloud``, ``CloudConfig`` and
    ``APIOpGet``, with the token read through ``internal/tokenstore`` rather
    than a second YAML parser.

    Two things writing it settled that the proposal did not state.

    ``Reachable`` and ``ServiceVersion`` build their own requests and were
    therefore unauthenticated. Both now send the headers too. ``Reachable`` in
    particular would otherwise have been *accidentally* right against a cloud
    service -- it accepts anything below 500, and an unauthenticated probe
    answers ``401`` -- which is the worst way for a check to pass.

    The cloud client's timeout is three minutes rather than the 90 seconds a
    control-plane client uses. The existing value is sized for a loopback call
    to a container on this machine; this is a round trip over whatever
    connection the user has.

.. spec:: Fetching an environment's BOM
    :id: HMD_CLI_NEURONSPHERE_NERD012_SPEC003
    :links: HMD_CLI_NEURONSPHERE_NERD012
    :status: implemented

    ``GET /apiop/get_deployment_bom/<environment_type>``, one request. The path
    parameter is ``environment.type`` -- the environment's slug, the same field
    ``EnsureEnvironment`` writes locally and the only field the service resolves
    an environment by. A type matching no environment, or more than one, is a
    server-side assertion failure, so a client shall report it as "no such
    environment" with the list from SPEC004 rather than as an internal error.

    An entry carries:

    .. code-block:: text

        repo_instance_name       always
        repo_class_name          always
        repo_class_version       always -- the concrete version
        deployment_id            always
        status                   always
        hmd_region               when set
        auto_deploy              when set
        image_only               when true; then no configuration and no dependencies
        instance_configuration   unless image_only
        dependencies             unless image_only; {role: name} or {role: [names]}
        config_artifact_spec     when set

    Three properties of the response that a client must not re-derive:

    - **It is in deployment-DAG order.** The server traverses the reduced
      dependency graph to build it, which is the order the instances can be
      brought up in.
    - **It contains only instances whose current deployment is** ``DEPLOYED``
      **or** ``FAILED``. Anything else is dropped server side, so a BOM is not a
      listing of everything an environment declares.
    - **A role with one target is a string and with several is a list.** Both
      spellings are valid in a manifest, and a decoder must accept either.

    ``EnvironmentInstances`` shall not be used for this. It exists, it returns
    very nearly the same fields, and it is the wrong tool twice over. It issues
    **ten unfiltered whole-table searches** and joins them client side, which is
    tolerable against a local graph holding one environment and is not tolerable
    against a cloud graph holding every environment a tenant has ever deployed.
    And it selects an instance's current deployment by the newest ``_created``
    on the edge, where the server selects the edge whose ``current`` attribute
    is ``"true"``. Those rules agree most of the time, which is the property
    that makes the disagreement hard to notice.

    That divergence is **recorded, not fixed.** The local path has used the
    ``_created`` rule since it was written and nothing has gone wrong; changing
    it is a separate question with its own evidence to gather.

    **Implemented 2026-09-15** as ``msdeploy.BOMEntry`` and ``DeploymentBOM``.

    Two decoding details the proposal did not state, both of which are ways to
    invent a fact that is not in the response.

    Every conditional key is ``omitempty`` and decodes to a nil rather than an
    empty value. An ``image_only`` entry that came back with an empty
    ``dependencies`` map would have turned "this instance deploys an image" into
    "this instance has no dependencies", which is a different claim about it.

    ``auto_deploy`` is decoded as ``any``. The entity types it as a *string*
    holding ``"true"``, while the BOM copies whatever is stored; decoding it as
    a bool fails on the string spelling, and nothing in this document needs its
    value.

    ``internal/bom.Entry``'s six shared JSON tags are pinned against
    ``BOMEntry``'s by a test in ``internal/bom``, which is the side that can see
    both. ``change_set.definition`` sets ``additionalProperties: false``, so a
    key misspelt on either side is a ``422`` rather than a field quietly
    ignored.

.. spec:: Listing the environments a deployment service knows
    :id: HMD_CLI_NEURONSPHERE_NERD012_SPEC004
    :links: HMD_CLI_NEURONSPHERE_NERD012
    :status: implemented

    ``POST /api/hmd_lang_deployment.environment`` with an empty filter, giving
    ``type``, ``account_number`` and ``hmd_region`` for every environment in one
    request. This is the same unfiltered-search idiom ``internal/librarian``
    uses for ``Repos`` and the same empty-body encoding ``Filter``'s
    ``MarshalJSON`` already produces.

    The listing shall **not** report an instance count per environment. There is
    no field carrying one, so it would mean one BOM fetch per environment --
    turning a single cheap request into an N+1 hidden behind a listing, against
    a service whose BOM operation is the expensive one.

    **Implemented 2026-09-15** as ``msdeploy.Environments``. A row carrying no
    ``type`` is skipped rather than listed as an environment with no name: the
    slug is the business id every other operation takes, so a row without one is
    not addressable and listing it would only invite someone to try.

.. spec:: Cherry-picking a BOM into a local environment
    :id: HMD_CLI_NEURONSPHERE_NERD012_SPEC005
    :links: HMD_CLI_NEURONSPHERE_NERD012
    :status: implemented

    A new ``nsctl bom`` family, rather than flags on ``nsctl repo``. Every verb
    in it reaches a cloud service, and nothing in ``repo`` does; keeping the
    boundary visible in the command name is worth more than the reuse.

    .. code-block:: text

        nsctl bom envs                     the environments a tenant's service knows
        nsctl bom show <env>               the BOM, and which artifacts are held here
        nsctl bom import <env> [selection] declare a selection locally

    Selection is by flag -- ``--instance`` and ``--class``, repeatable, plus
    ``--all`` and ``--exclude`` -- and ``show`` accepts the same flags, so a
    ``show`` is exactly a preview of the ``import`` that follows it, closure
    included. An empty selection shall be refused rather than defaulting to
    everything: importing a sixty-instance cloud environment is a thing to ask
    for out loud.

    ``--exclude`` applies **after** the closure and refuses to remove something
    the closure requires, naming what wanted it. An exclusion that silently
    unfilled a role would reintroduce exactly the failure the closure exists to
    prevent.

    **Amended 2026-09-16: ``import --apply``.** Declaring is not deploying, and
    stays that way by default -- ``import`` writes the manifest and names
    ``nsctl env apply`` as the next thing to run. ``--apply`` runs that apply
    here, on the same environment, once the selection is declared: the
    round-trip ``NERD013``'s fourth acceptance criterion asks for becomes one
    command instead of two. It is the same ``environment.Apply`` that
    ``nsctl env apply`` calls and nothing else; SPEC007 is untouched, since the
    apply talks only to the local control plane. It is refused with
    ``--dry-run``, which deploys nothing, and with ``--no-pull``, since an apply
    refuses an uncached artifact anyway and the refusal is better here than
    minutes later. A **partial** import -- one where an artifact could not be
    fetched -- is declared but *not* applied: SPEC006's rule that the command
    exits non-zero naming the failures stands, and deploying a knowingly
    incomplete import on the way out would be the quiet failure that rule
    exists to prevent.

    ``import`` shall proceed in this order, and the order is load-bearing:
    **select, filter, close over dependencies, fetch, declare.** Nothing is
    written to the manifest until the artifact behind it is in the control
    plane, so an import never leaves a declaration that cannot deploy.

    **Filtering** drops three things, each for a reason that already exists:

    - Instances whose names are the local substrate's -- ``nsctl`` deploys that
      substrate whether or not a manifest names it, and declaring one would make
      it removable by deleting a line.
    - Instances the local manifest already declares. A hand-edited declaration
      is never overwritten, exactly as in ``nsctl repo import``.
    - Instances whose status is not ``DEPLOYED``, unless asked otherwise. A
      ``FAILED`` instance appears in a BOM and is not a thing to reproduce by
      default.

    **The selection shall be closed under its dependencies.** Selecting
    ``ms-transform`` selects whatever fills its roles, transitively, and says so.

    This is not a convenience. ``hmd-ms-deployment`` **fails the whole
    ChangeSet** on a required role nothing fills, so a selection that dropped
    roles would routinely produce a manifest that cannot deploy -- and the
    failure would arrive at ``env apply``, minutes later, naming a role rather
    than the import that omitted it.

    It would be better to close over *required* roles only, and that is not
    available: a BOM entry's ``dependencies`` is ``{role: target}`` with **no
    required flag** -- the flag lives on the RepoClassVersion's declared
    dependencies, not on the deployed edge. Closing over every role is the
    conservative direction, and it is conservative in the direction that
    matters: importing an instance nobody needed costs a fetch, while omitting
    one costs a deploy.

    **Superseded by NERD013.** Both halves of that sentence are true and the
    conclusion does not follow. The flag lives on the RepoClassVersion's
    declared dependencies -- which is ``deploy.dependencies.<role>.required`` in
    the repo class's own BACON manifest, a file this CLI already reads through
    ``internal/repoclass`` and already hands to ``add_repo_class_version``. It
    travels inside the artifact ``import`` fetches. What was missing was not the
    flag but the observation that the fetch could precede the expansion; see
    ``NERD013`` SPEC001 and SPEC005.

    Each target resolves in one of four ways, and the resolution is reported:

    #. **Substrate** -- ``base-vpc``, ``eks-cluster``, ``environment-db``,
       ``local-neuronsphere``. Bound to the local substrate instance. Not
       imported, because ``nsctl`` deploys it either way.
    #. **Already declared locally.** Bound by name. Not imported, and not
       overwritten.
    #. **Present in the same BOM.** Added to the selection, carrying its own
       concrete version, and closed over in turn.
    #. **Absent from the BOM.** Warned about by name, with the role that wanted
       it. This is what a cloud-only dependency looks like -- something whose
       instance was never ``DEPLOYED``, or lives outside the environment -- and
       naming it is the whole of the honest answer available here.

    The closure is reported apart from the explicit selection, because "I asked
    for one thing and got nine" needs to be visible rather than merely true.
    ``--no-deps`` takes the selection literally instead, warning about every
    role it leaves unfilled; that is the flag for a machine that already holds
    the rest.

    **A selection is declared from an artifact**, explicitly -- not left to
    default resolution. The version came from a cloud librarian, and a
    declaration that could silently resolve to whatever checkout happens to sit
    under ``HMD_REPO_HOME`` would deploy something other than what the cloud is
    running, which is the one thing this whole document exists to prevent.

    ``import`` shall **not deploy.** It ends by naming ``nsctl env apply``.
    Declaring is not deploying anywhere else in ``nsctl`` and this is not the
    verb to make an exception for: a networked fetch and a ten-minute deploy
    should not be the same keystroke.

    **Implemented 2026-09-15** as ``cmd/bom.go`` and ``cmd/bom_select.go``, the
    selection kept in its own file as a function over data -- a BOM, a "is it
    declared" predicate and a "is it reserved" predicate in, a decision out --
    so the closure is testable without a server.

    Three things writing it settled that the proposal did not state.

    The closure is **iterative rather than recursive**. The BOM is a DAG and a
    recursive walk would be shorter; it would also overflow a stack rather than
    terminate if a graph ever stopped being the DAG it claims to be, and a
    visited set costs one line.

    An instance is recorded as "pulled in by the closure" **only when it is
    actually imported**. The first version recorded it on arrival, which
    reported ``eks-cluster`` as both pulled in by the closure and bound without
    importing -- two accounts of one thing -- and reported a target that is not
    in the BOM at all as though it had been imported.

    A **selector matching nothing is refused**, listing what the BOM does have.
    The proposal only refused an *empty* selection. But ``--instance
    ms-transfrom`` contributing nothing has exactly the shape of a successful
    small import, and finding out at the end that an import was smaller than
    intended is much worse than finding out before anything is fetched.

.. spec:: Copying the chosen artifacts down
    :id: HMD_CLI_NEURONSPHERE_NERD012_SPEC006
    :links: HMD_CLI_NEURONSPHERE_NERD012
    :status: implemented

    Each selected entry names a repo class and a concrete version, which is
    already an artifact address. Fetching it is ``nsctl artifact pull``'s
    existing path and shall reuse it rather than restate it: fetch from the
    cloud librarian, ``Put`` into the control plane's, invalidate the unpacked
    copy, unpack the new one.

    This is the one part of ``import`` that is slow, so it reports per artifact.

    A fetch that fails shall **remove that instance from the import** -- it is
    not declared -- shall be reported, and shall make the command exit as a
    failure *after every other instance has been handled*. Stopping at the first
    failure would leave a half-imported manifest whose remaining work is a
    different command each time.

    ``--no-pull`` declares nothing and prints the ``nsctl artifact pull`` lines
    instead, for a machine that already holds them or a user who wants the fetch
    to be a separate, resumable step.

    The **cloud librarian and the cloud deployment service are two endpoints**
    resolved independently by SPEC001's one rule. They are ordinarily the same
    tenant, and nothing requires them to be: reading a BOM from one tenant and
    fetching from another is a legitimate thing that falls out of resolving
    them separately, and is not a case this document goes out of its way to
    support.

    **Implemented 2026-09-15**, reusing ``nsctl artifact pull``'s path through a
    split: ``storeLocally`` keeps its two lines of reporting for the caller that
    stores one artifact, and ``storeArtifact`` is the same work without them for
    the caller that stores twenty. A split rather than a second implementation,
    because what must not differ between them is the invalidate-before-store
    order -- an unpacked copy left in place would deploy the previous build
    under the new build's version.

    One addition the proposal did not state: an artifact **already unpacked in
    the cache is not fetched again**, and the run says so. These are
    multi-megabyte zips addressed by an exact version, so a copy already here is
    the same bytes; ``artifact.Cached`` is the same check the ``CACHED`` column
    reports.

.. spec:: What this is not
    :id: HMD_CLI_NEURONSPHERE_NERD012_SPEC007
    :links: HMD_CLI_NEURONSPHERE_NERD012
    :status: implemented

    **Not a write path to the cloud.** ``NERD010`` SPEC008 says a lock is a
    local convenience and never an input to a cloud deploy; ``NERD011`` SPEC006
    says this platform's local tooling does not change how the cloud resolves
    anything. Both stand. This document reads a cloud service and writes a local
    file, which is the same direction ``nsctl artifact pull`` already travels.

    **Not a promotion mechanism.** Copying ``dev`` into a local environment is
    not promoting anything anywhere. There is no approval, no gate, no record
    kept, and the cloud environment does not learn that this happened.

    **Not a reproduction of a cloud environment.** A local environment has a
    different substrate, one account, no real VPC and an emulated AWS. A BOM
    import brings *versions and configuration* across; it does not claim the
    result is the same environment, and the dropped-role warning of SPEC005 is
    where that stops being a silent difference.

    **Not a dependency solver.** ``NERD011`` SPEC006's line holds: this selects
    what was asked for and reports what it could not bind, rather than
    reconciling anything.

    **Not a second version-resolution path.** A BOM carries concrete versions.
    Nothing here consults ``internal/versionspec`` or a librarian's version
    enumeration, and a BOM import is not affected by what has been published
    since.

    **Implemented 2026-09-15**, and all five still stand. The one worth
    re-stating after the fact is the third: an import brings versions and
    configuration across, and does not claim the result is the same
    environment. The dropped-role warning is where that stops being a silent
    difference -- a cloud environment's roles reach VPCs, API gateways and an
    identity provider that have no local counterpart, and the honest thing
    available is to name each one.

The acceptance run
------------------

Run on 2026-09-15 against ``https://ms-deployment-aaa-reg1.hmdtr1-admin-neuronsphere.io``,
the tenant's real deployment service, with the token ``hmd login`` had just
cached. Every step was a read.

``nsctl bom envs`` answered in one request with ``admin``, ``dev`` and
``dev-eu``. ``nsctl bom show dev`` returned **109 instances**.

**Criterion 1, amended.** It asked for the response to be captured *and checked
in as a fixture*. It was captured and it is deliberately **not** checked in: a
real BOM's ``instance_configuration`` carries that tenant's infrastructure --
``customer_ips``, ``hmd_ips``, ``assume_principals``, ``proxy_secret``,
``ca_account``, account numbers -- and one instance name contains a person's
name. None of that belongs in a repository, and the criterion's purpose was to
stop a decoder written from source being trusted, not to publish a customer's
network.

So it was satisfied differently: the response was decoded **with
``DisallowUnknownFields``**, which is a stronger check than a committed fixture
would have been, since a field the struct lacks fails the whole decode. All 109
entries decoded. What that proved, now recorded as assertions in
``cmd/bom_test.go``:

.. list-table::
   :header-rows: 1

   * - Claim
     - Live result
   * - The struct carries every field the service emits
     - **Yes.** 109/109 with ``DisallowUnknownFields``.
   * - ``auto_deploy`` is a string, not a bool
     - **Yes**, on all 109 -- ``"false"`` ×106, ``"true"`` ×3. Decoding it as a
       bool would have failed the entire response.
   * - Conditional keys really are absent
     - **Yes.** ``dependencies`` on 69, ``instance_configuration`` on 57,
       ``hmd_region`` on 3, ``image_only`` on 1.
   * - An ``image_only`` entry carries neither configuration nor dependencies
     - **Yes**, on the one that exists.
   * - Both role spellings occur
     - **Yes.** 269 roles as a bare string, 5 as a list.
   * - Every entry carries a concrete version
     - **Yes.** Zero without one.
   * - Only DEPLOYED and FAILED appear
     - **Yes.** 104 DEPLOYED, 5 FAILED.

**What the run found that no fake could have.** Selecting the one
``hmd-ms-transform`` instance closed over **67 of the 109** -- and two of those
67 were the substrate. ``dev`` runs ``hmd-inf-eks-cluster`` as an instance
called ``eks-upg`` and ``hmd-postgres-rds`` as ``core-rds``, while every local
environment deploys those same classes as ``eks-cluster`` and
``environment-db``. SPEC005's substrate binding matched on the instance name
alone, so it recognised neither, and the closure imported both as workloads --
locally, a second cluster and a second database inside the environment that
already has them. Fixed by matching the substrate by **repo class** as well,
and rewriting the role to this environment's name for it; the closure is now 66
and the run reports ``substrate, as eks-cluster``.

**What the run leaves open.** Sixty-six instances is a faithful closure and a
large import, and much of it -- ``hmd-inf-acm``, ``hmd-inf-wafv2``,
``hmd-inf-api-gateway``, ``hmd-inf-neptune``, ``hmd-inf-private-ca``,
``hmd-inf-karpenter`` -- has no local counterpart at all. Nothing is wrong with
the closure: those roles are real and ``ms-deployment`` would refuse the
ChangeSet without them. What is unresolved is whether importing a cloud
workload into a local environment is *useful* at that size, and that is a
product question rather than a defect. See `Open questions`_.

Risks
-----

.. list-table::
   :header-rows: 1

   * - Risk
     - Severity
     - Mitigation
   * - The BOM's wire shape was derived from source rather than observed, so
       the decoder is wrong in a way tests written from the same source cannot
       catch
     - **High**
     - Acceptance criterion 1, as amended: capture one real response and
       decode it with ``DisallowUnknownFields`` before the decoder is trusted.
       Not checked in as a fixture -- it carries the tenant's network; see
       `The acceptance run`_.
   * - A cherry-pick drops a role the instance actually needs, and the failure
       appears much later as a refused ChangeSet
     - Medium
     - The selection is closed under its dependencies by default; a target the
       BOM does not contain is warned about by name at import time, and the
       manifest is validated before it is written.
   * - The closure pulls in far more than the user expected, including
       cloud-only infrastructure
     - Medium
     - The closure is reported apart from the explicit selection and shown by
       ``--dry-run`` before anything is fetched; ``--no-deps`` takes the
       selection literally.
   * - An environment variable silently overrides the profile someone named
     - Medium
     - SPEC001's guard reports the shadowing, naming both values.
   * - A cloud BOM names repo classes whose artifacts were never published, so
       the import half-succeeds
     - Medium
     - Fetch before declare; a failed fetch declares nothing for that instance
       and the command exits as a failure naming all of them.
   * - ``get_deployment_bom`` is slow enough on a large environment to read as
       a hang
     - Low
     - It is one request and is cached server side; progress is printed before
       it is issued.
   * - The token in ``tokens.yaml`` is expired and every verb fails at the same
       point
     - Low
     - The existing refusal already names ``nsctl login``; construction fails
       before a request when no credential exists at all.

Acceptance criteria
-------------------

#. One real ``get_deployment_bom`` response is captured from a cloud
   ``hmd-ms-deployment`` and the decoder is verified against it, including at
   least one ``image_only`` entry and one role with several targets. *Amended:
   the response is verified in place with* ``DisallowUnknownFields`` *and not
   checked in; see* `The acceptance run`_.
#. ``nsctl bom envs`` lists a tenant's environments in one request, with no BOM
   fetched.
#. ``nsctl bom show <env>`` reports every deployed instance with its concrete
   ``repo_class_version``, and says for each whether this machine already holds
   that artifact.
#. ``nsctl bom import <env> --instance <one> --dry-run`` writes nothing, fetches
   nothing, and reports the dependency closure apart from the explicit
   selection, naming any target the BOM does not contain.
#. Selecting an instance with dependencies imports those dependencies too, at
   their own versions, while binding roles filled by the substrate rather than
   importing them; ``--no-deps`` imports only what was named.
#. ``nsctl bom import <env> --instance <one>`` declares that instance, at the
   cloud's version, from an artifact, and ``nsctl repo list`` then shows it with
   its artifact cached.
#. An import whose artifact cannot be fetched declares nothing for that instance
   and exits non-zero naming it.
#. A profile with neither a ``deployment_url`` nor a ``customer_code`` is
   refused with a message naming all five tiers of SPEC001's rule.
#. An ``nsctl.toml`` containing only ``auth_url`` still parses and
   ``nsctl login`` is unaffected.

Open questions
--------------

**Should ``nsctl bom import`` be able to target a repository's**
``neuronsphere.lock`` **rather than an environment manifest?** A cloud BOM is
the strongest possible ``generated_from`` -- stronger than ``env:<name>``, since
the environment is one somebody depends on. It is not done here because a lock
pins repo classes against what a *repository* declares it wants, and a cloud
environment contains a great deal that no single repository wants; the mapping
is not obvious enough to guess at.

**Should the two cloud endpoints be allowed to come from different profiles?**
SPEC006 notes that resolving them independently permits it. Adding a flag to
*express* it would be inventing a use case.

**What should happen to** ``instance_configuration`` **that names cloud
resources?** It is copied verbatim today. A configuration holding a real VPC id
or a cloud bucket name will not mean anything locally, and the honest answer is
probably to warn on values that look like ARNs or account numbers -- but that is
a heuristic, and heuristics in this position are how a tool acquires a reputation
for lying.

The acceptance run gave this one teeth. A real ``instance_configuration``
carries ``customer_ips``, ``hmd_ips``, ``assume_principals``, ``proxy_secret``
and ``ca_account``; copying those into a local manifest is at best meaningless
and at worst a secret written to a file nobody expected to hold one.

**Is a sixty-six-instance closure useful?** *Answered by* ``NERD013``.
Selecting one workload from ``hmdtr1``'s ``dev`` brings two-thirds of the
environment with it, and a large part of that -- ACM certificates, WAFv2, API
Gateway, Neptune, a private CA, Karpenter -- has no local counterpart and will
fail at ``env apply`` even though it fetched cleanly.

The closure is not wrong: those roles are declared and ``ms-deployment`` refuses
a ChangeSet without them. But "correct and unusable" is still unusable. Three
answers were plausible here and none was chosen: refuse above a threshold and
make the user pass a flag; teach the local substrate to *satisfy* more roles, so
that cloud-only infrastructure binds the way ``eks-cluster`` now does; or scope
the verb to repo classes that have a local deploy path at all.

The answer turned out to be a fourth, and it is a general one rather than
anything to do with cloud BOMs, so it has its own document. **The closure
follows every role because it cannot tell a required one from an optional one
-- and it can.** Roughly half of the dependency roles in the workspace are
authored ``required: "false"``, and following only the required ones takes
``hmd-ms-transform`` from 56 classes to 25, pruning the private CA and Karpenter
among them. ``NERD013`` specifies that, the opt-in for an optional role, and
what to do with the required roles that remain unfillable.

The threshold idea is rejected there as a louder failure rather than a smaller
one; the substrate idea survives, narrowed to the two resource-typed classes
that actually need it. And the remaining piece of work is unchanged: one attempt
at actually applying an imported workload, which is ``NERD013``'s fourth
acceptance criterion.
