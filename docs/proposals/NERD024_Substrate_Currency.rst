.. NERD024 Substrate Currency

NERD024 Substrate Currency
==========================

.. req:: An environment's substrate shall be no larger than it needs and no older than the binary operating it
    :id: HMD_CLI_NEURONSPHERE_NERD024
    :status: implemented

    ``nsctl`` shall deploy a substrate service only when something in the
    environment selects it, and shall reconcile the version of what is already
    deployed against the version the running binary resolves.

    A service reached through an environment route shall be probed after it is
    deployed or refreshed, and a ``5xx`` shall be reported with its cause
    named -- the deployed version, the compatibility floor, and the command
    that repairs it -- rather than relayed as a deploy failure.

    No part of this shall require a credential, and no failure on a local path
    shall be reported in terms that suggest one.

    .. note::

        ``implemented`` as of 2026-09-24. Every SPEC below is built, unit
        tested, and exercised against a running platform -- see
        `The acceptance run`_, which also records the one thing it could not
        prove and why.

Motivation
----------

On 2026-09-24 a user's ``nsctl env apply local`` failed at its first
``hmd-database-account`` node with a bare HTTP 500 from
``/<slug>/hmd_ms_dbaccount/``. The Lambda log said
``jwt.exceptions.DecodeError: Not enough segments``. The session spent an hour
on it and concluded, wrongly, that the caller needed to sign in.

Every part of that hour was avoidable, and each part is a separate defect.

**The crash was already fixed, and the fix could not reach the user.**
``hmd_proxy`` must spend ``Authorization`` on Floci's SigV4 credential scope:
one emulator serves every account, a 12-digit access key *is* the account, and
that scope is the only thing Floci resolves a v1 REST API's owner from
(:doc:`/environments`, ``internal/router/router.go:488-514``). On
``hmd-ms-base`` at or below ``0.2.279`` that scope reached ``get_claims()``
inside ``UserMiddleware`` -- middleware, so outside any route's error
handling, which is why the 500 carried no body.  ``hmd-ms-base`` ``0.2.282``
closes both halves: ``0.2.280`` reads the relocated ``X-NS-Authorization``
header, and ``0.2.282`` adds ``hmd-lib-auth`` ``0.1.128``'s guard returning
``{}`` for non-JWT input. ``hmd-ms-dbaccount`` ``0.1.47`` consumes it, and
``nsctl`` ``1.0.216`` pinned it.

The user had a newer ``nsctl`` than that. It did not help, because
``EnsureDBAccount`` runs only from ``environment.Start``
(``internal/environment/environment.go:245``) and never from ``Apply``. An
environment whose Lambda was deployed by ``1.0.215`` keeps ``0.1.46``
indefinitely under any later binary. Nothing detects the drift, nothing
reports it, and the command the user was running could not repair it.

**The substrate carried a service nothing had asked for.** ``planFor``
(``internal/environment/substrate.go:25-34``) gives ``DBAccount`` to both
``core`` and ``full``, so every start pays an image pull, a Lambda create, a
REST API create, a stage deploy and an nginx rewrite -- whether or not any
instance will ever ask for a database. Nothing selects it either:
``foundationServices`` advertises it as a Resource that "every
``hmd-database-account`` instance's create-service role selects"
(``internal/environment/apply.go:648-660``), and with no such instance there is
no consumer. The environment NERD014 made possible -- a repo class deploying
with ``exec`` against infrastructure the customer already runs -- pays for a
NeuronSphere-internal microservice it will never call.

**A 5xx was relayed, not diagnosed.** ``hmd-ms-base`` registers only
``/api/...`` and ``/apiop/...`` routes, so a healthy service answers ``404`` at
its route root and only a broken one answers ``5xx``. ``internal/status``
already relies on exactly that distinction (``status.go:192-197``). The signal
was available at every layer and was read by none of them.

**Nothing said that local needs no credential, so an agent assumed it did.**
Every local path is anonymous by construction: ``msdeploy.New`` sets no token
and its ``headers`` is a documented no-op, ``librarian.NewLocal`` exists
precisely to bypass the credential check that is "correct for a cloud librarian
and wrong for this one", the deploy containers in ``internal/runner/runner.go``
carry only a dummy AWS pair, the local identity provider is off by default
(``internal/authd/authd.go:41-61``), and ``AUTHORIZATION`` defaults to ``NONE``
on every local Lambda. Eleven messages in the binary say "run ``nsctl login``",
every one of them for a cloud tenant or a registry -- and no shipped agent
skill, no local tutorial and no ``whoami`` output says that a local verb never
sends a credential. The inference was available and nothing contradicted it.

The evidence this document is built on
--------------------------------------

- ``ServiceEnv`` (``internal/floci/lambda.go:97-123``) already writes
  ``HMD_REPO_VERSION`` into every substrate Lambda, and ``GetFunction`` is
  already on the Lambda interface (``lambda.go:25``). What was deployed is
  therefore readable from the deployment itself; no new record is needed, and
  none can drift from it.
- ``EnsureDBAccount`` (``internal/environment/dbaccount.go:26-63``) is already
  idempotent -- ``SetupService`` updates the function in place and recreates
  the gateway so stale routes cannot hijack traffic. Refreshing is the call
  that already exists, not a new one.
- ``Start`` already handles the absent case: the ``else`` branch at
  ``environment.go:247-252`` rewrites the env fragment empty rather than
  leaving it, "so a dbaccount route from an earlier full run would not
  advertise a service that no longer answers".
- ``doctor.Gate`` is the fast preflight a start must pass; ``doctor.Run`` is
  documented as "the whole suite, for ``nsctl doctor``… the checks a start
  makes elsewhere or not at all", and "never stops at the first failure"
  (``internal/doctor/doctor.go:96,140-143``). The seam for a reporting-only
  check already exists and is already named.
- ``status.Reporter`` keeps ``internal/status`` free of ``HMD_HOME``'s layout
  by taking ``Substrate`` and ``Routed`` as injected functions
  (``status.go:325-340``). The same pattern keeps ``internal/doctor`` free of
  it.

Specifications
--------------

.. spec:: Demand decides presence
    :id: HMD_CLI_NEURONSPHERE_NERD024_SPEC001
    :status: implemented

    A substrate service shall be deployed when something in the environment
    selects it, not because the substrate mode names it.

    For ``hmd-ms-dbaccount`` the selector is the one that already exists: an
    instance whose dependencies resolve against the ``hmd_ms_dbaccount``
    microservice Resource ``foundationServices`` advertises. An environment
    declaring no such instance shall start with no dbaccount Lambda and no
    ``/<slug>/hmd_ms_dbaccount/`` route.

    The Resource shall continue to be advertised regardless. A Resource with no
    consumer costs nothing, and withdrawing it would break resolution for a
    deploy that adds its first consumer in the same ``apply``.

    This does not lower ``core``'s meaning. ``core`` still promises a database
    and a graph; it stops promising a Lambda that only matters to a consumer
    that may not exist.

.. spec:: Currency is read from the deployed artifact
    :id: HMD_CLI_NEURONSPHERE_NERD024_SPEC002
    :status: implemented

    The deployed version of a substrate service shall be read back from its
    Lambda's ``HMD_REPO_VERSION`` environment variable and compared with what
    ``repoclass.Resolver.ResolveVersion`` resolves for the running binary.

    A function that does not exist shall read as the empty version, which is
    indistinguishable from "deploy it" and needs no separate absence check.

.. spec:: env apply reconciles what it finds
    :id: HMD_CLI_NEURONSPHERE_NERD024_SPEC003
    :status: implemented

    ``nsctl env apply`` shall reconcile the dbaccount service before the phase
    that calls it: absent and selected, deploy; present and stale, refresh with
    the same idempotent call ``env start`` makes; present and current, do
    nothing beyond the one read SPEC002 requires.

    A deploy or refresh shall be followed by the route refresh and proxy reload
    in the order ``environment.Start`` uses. ``SetupService`` recreates the REST
    API and ``WriteEnvRoutes`` rewrites the environment fragment wholesale, so
    any other order erases the DAG-discovered routes spliced in after it.

    ``apply`` reconciles as well as ``start`` because ``start`` answers the
    question only for the environment as it stood when it started. A consumer
    added to an environment that is already running is deployed by ``apply``,
    which is also the last moment before the node that calls the service.

.. spec:: A 5xx through an environment route is diagnosed, not relayed
    :id: HMD_CLI_NEURONSPHERE_NERD024_SPEC004
    :status: implemented

    After a deploy or refresh the service's route root shall be probed. A
    healthy ``hmd-ms-base`` service answers ``404`` there -- it registers only
    ``/api/...`` and ``/apiop/...`` -- so a ``5xx`` is diagnostic rather than
    ambiguous.

    A ``5xx`` shall be reported with the deployed version, the floor SPEC005
    names, the command that repairs it, and one sentence stating that it is not
    an authentication failure. A failure whose message invites the reader to
    debug authentication costs more than the failure itself.

    A route that answers *nothing* shall warn rather than fail. It is weak
    evidence about the deploy -- the node that calls the service reaches it over
    the container network, and an unreachable proxy has already been refused by
    the deployment-service check -- and failing on it would stop an apply that
    would otherwise have worked.

.. spec:: The compatibility floor is declared, not inferred
    :id: HMD_CLI_NEURONSPHERE_NERD024_SPEC005
    :status: implemented

    ``hmd-ms-base`` ``0.2.282`` shall be named in the source as the floor for
    any service reached through an environment route, with the reason: below it
    the injected credential scope is parsed as a bearer token inside
    ``UserMiddleware`` and every request returns a bare 500.

    It is declared rather than probed because a service image does not report
    the version of the base it was built on, and a floor that has to be
    discovered from a crash is the state this document exists to leave.

.. spec:: nsctl doctor reports currency without repairing it
    :id: HMD_CLI_NEURONSPHERE_NERD024_SPEC006
    :status: implemented

    ``nsctl doctor`` shall report, for each running environment, whether its
    substrate services are current and whether their routes answer. Stale shall
    be a warning naming ``nsctl env start <slug>``; a ``5xx`` shall be a failure
    carrying SPEC004's message.

    These checks shall run in ``doctor.Run`` and never in ``doctor.Gate``.
    ``Gate`` is a preflight other commands call before starting anything, and a
    preflight that makes network calls to Floci to decide whether a start may
    proceed is a preflight that fails when the thing it gates is what would have
    fixed it.

    ``doctor`` shall name repairs and perform none, consistent with its existing
    contract. An absent control plane, an environment that is stopped, and a
    home with no environments shall each be reported as nothing to check, never
    as a failure.

.. spec:: Local operation states that it needs no credential
    :id: HMD_CLI_NEURONSPHERE_NERD024_SPEC007
    :status: implemented

    The property is already true; this makes it legible.

    ``nsctl whoami`` while signed out shall say that this is the ordinary state
    and that nothing local needs a credential, rather than presenting a bare
    instruction to sign in. Every remaining ``nsctl login`` prompt shall name
    what it is for -- a hosted tenant, or a private registry.

    The shipped agent skills shall state, where an agent will read it while
    diagnosing, that a local failure is never an authentication failure: no
    local verb sends a credential, so ``login``, ``logout`` and ``whoami`` are
    neither diagnostic steps nor repairs.

    The skills are the leverage here rather than the documentation. They are
    embedded in the binary and installed into ``.claude/skills/`` and
    ``.agents/skills/``, so they are what an agent has in front of it at the
    moment the wrong conclusion is available.

The acceptance run
------------------

2026-09-24, against a real control plane in ``$HMD_HOME=~/hmdtr1``, in a
dedicated ``nerd024`` environment on ``--substrate core`` so nothing touched the
environments already registered there. Both ``hmd-ms-dbaccount`` images were
already cached, so the run needed no network.

**SPEC001.** Started with nothing declared. The summary printed ``database`` and
``substrate core`` and **no** ``dbaccount`` line; the route fragment carried no
``hmd_ms_dbaccount`` marker; ``env status`` listed no ``dbaccount`` route; and
``doctor`` reported nothing about the environment's substrate. Under the
previous behaviour every one of those would have announced a service that was
not there. An ``hmd-database-account`` consumer was then declared and the next
``apply`` deployed the service, so presence follows demand in both directions.

**SPEC002, SPEC003.** With ``HMD_LOCAL_VERSION_HMD_MS_DBACCOUNT=0.1.46`` the
apply deployed that version on demand. Unpinned, the next apply printed
``hmd-ms-dbaccount in "nerd024" is 0.1.46, this nsctl resolves 0.1.47;
refreshing it``, redeployed and rerouted it. A third apply printed nothing at
all, which is the "present and current" path costing one ``GetFunction``.
``env status`` still listed the route afterwards, which is the ordering
constraint this document warns about.

**SPEC004.** On the stale version the route answered ``500`` and the apply
stopped **before** the phase whose first node calls the service, naming the
deployed version, the floor, the repair that would work, and that it is not an
authentication failure. After the refresh the same route answered ``404`` with
FastAPI's ``{"detail":"Not Found"}``.

**SPEC006.** ``doctor`` reported ``ok`` against the live Lambda, ``warning``
with ``nsctl env start`` as the remedy under a forced version difference, and
``failure`` with exit 2 on a 5xx.

**What the run changed.** The first apply of the deliberately broken ``0.1.46``
*passed* the probe and failed at the deploy node instead. ``hmd_proxy`` answers
``404`` with ``{"error": "no route defined"}`` for a path it is not yet routing,
and a healthy ``hmd-ms-base`` service answers ``404`` too -- so a probe run in
the moment between writing the fragment and nginx reloading it read the proxy's
answer as the service's. The probe now tells them apart and keeps waiting rather
than concluding. That defect was invisible to every unit test and is the reason
this section exists.

**What it could not prove.** That the ``hmd-database-account`` node itself
succeeds after the refresh. ``hmd-img-projectbuilder:0.5.389`` ships
``hmd-cli-dbaccount`` 0.1.7, whose local-path predicate is ``environment ==
"local"`` alone; the ``AWS_ENDPOINT_URL`` fallback that a non-``local``
environment needs exists in the repository but is not in that image. The node
therefore takes the cloud path and dies looking up an API Gateway key, in any
environment not named ``local``, with or without this change. Unrelated to this
document and recorded here so the gap is not mistaken for one of its own.

Alternatives considered
-----------------------

**Remove ``hmd-ms-dbaccount`` from the local substrate entirely**, and have
``nsctl`` create databases, users and secrets directly against the
environment's Postgres. It could: ``floci/provision.go:154-164`` already writes
the admin secret the service itself reads, and ``floci/lambda.go:96`` already
encodes the naming convention the service applies. It would delete a
NeuronSphere-internal concept from the substrate of an environment that may be
entirely foreign, and would have made this class of bug impossible in
``env start``.

Rejected for now, on parity. In the cloud an ``hmd-database-account`` node
really does POST to ms-dbaccount; a local path that provisioned databases some
other way would stop exercising the one it is meant to be a twin of, and the
first divergence would be discovered in the cloud. SPEC001 takes the part of
the benefit that costs nothing -- an environment that needs no database account
runs no database-account service -- and leaves the deploy-time call path
identical for the environments that do.

The decision is recorded rather than closed. If the local and cloud
provisioning paths diverge for some other reason, or if the foreign-toolset
environments of NERD009 become the common case rather than the new one, it
should be revisited.

**Probe the base version instead of declaring a floor.** A service image does
not report the ``hmd-ms-base`` it was built on, so this would mean either a
label convention every service repo must adopt or an inspection of the image's
site-packages. SPEC005's constant is one line and is exact for the case that
actually occurs.

Out of scope
------------

- **Rebuilding the rest of the fleet onto the floor.** Only
  ``hmd-ms-dbaccount`` is at ``0.2.282`` today. ``hmd-ms-transform``,
  ``-cluster``, ``-connection``, ``-device``, ``-gozer``, ``-identity`` and
  ``-telemetry-debug`` pin ``0.2.279``; ``-siesta`` pins ``0.2.280``, which
  relocates the header but lacks the ``get_claims`` guard and so still crashes;
  ``-naming``, ``-audit``, ``-case``, ``-mfr`` and ``-projects`` pin
  ``0.2.252``. Each will return the identical bare 500 the moment it is
  deployed into a local environment as a workload. Those ``src/docker/Dockerfile``
  bumps, and the microservice cookiecutter template that will otherwise keep
  producing new services below the floor, are a separate change.
- **Extending currency checks to DAG-deployed workloads.** This document covers
  the substrate ``nsctl`` owns directly. A workload's version is the
  deployment service's record, not nsctl's, and reconciling it is a different
  mechanism with a different owner.
- **Authorising anything.** Nothing here changes what any service accepts. It
  changes what nsctl deploys, what it checks, and what it says.
