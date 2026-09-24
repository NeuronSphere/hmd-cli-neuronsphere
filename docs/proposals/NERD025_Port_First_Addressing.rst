.. NERD025 Port-First Addressing

NERD025 Port-First Addressing
=============================

.. req:: Reaching a local service from the host shall cost no privileged change to the machine
    :id: HMD_CLI_NEURONSPHERE_NERD025
    :status: proposed

    A first run of ``nsctl`` shall start a control plane and an environment,
    and shall make every user interface that environment deploys reachable
    from a browser, without editing ``/etc/hosts``, without ``sudo``, and
    without a working name service.

    Where a name is genuinely load-bearing -- one string that a browser, a
    sibling container and a cluster pod must all resolve to the same place --
    it shall remain a name, and the cost of resolving it shall be paid once
    and separately (:doc:`NERD026_Local_Name_Resolution`), never per service
    and never per deploy.

    No failure on this path shall be reported as a missing entry in a file the
    user is not required to have edited.

Motivation
----------

``nsctl`` refuses to start at all until the machine resolves two names.
``CheckHostsEntries`` (``internal/controlplane/controlplane.go:133``, called
from ``Start`` at ``:228``) returns ``nserr.Usage`` -- exit 2 -- unless
``neuronsphere`` and ``neuronsphere-workload`` both answer to a loopback
address, and the remedy it prints is a ``sudo`` line against ``/etc/hosts``.
That is the first thing a new user meets, before anything has run.

The gate is the smaller half of the problem. The larger half is that the cost
**grows with use**. Every user interface an environment deploys adds a
hostname, warned about at ``internal/environment/environment.go:723-731`` and
again after each deploy at ``:965-972``. Every control-plane extension adds
another (``internal/cpext/apply.go:265-276``). :doc:`/environments` states the
reason without flinching at ``:452``:

    Because ``/etc/hosts`` has no wildcards, each UI hostname needs an entry --
    ``up`` prints the exact ``sudo`` line for any that do not yet resolve.

So the friction is not a one-time setup step that a good tutorial can absorb.
It is a recurring tax, paid in root, every time the platform does more of what
it exists to do.

Three facts decide the shape of the answer, and all three are already
load-bearing.

**Only Floci needs the bare names.** ``FLOCI_HOSTNAME: neuronsphere``
(``internal/bundled/services/docker-compose.control-plane.yml:89``) is what
Floci writes into every presigned S3 and API URL it hands back. Host-side
consumers then dereference a URL that names ``neuronsphere:4566``. That, and
nothing else, is why the machine must resolve the bare name -- as the comment
on ``HostsEntries`` (``controlplane.go:36-41``) already says.

**Some names cannot become ports, and some can.** The ``iss`` claim and a
package index URL are read by a browser, by a Floci Lambda container and by a
cluster pod, and must be one string in all three
(``internal/authd/authd.go:9-17``; NERD006 SPEC002 makes the same argument for
``uv.toml``, ``.npmrc`` and ``GOPROXY``). ``localhost:<port>`` cannot be that
string, because inside a pod ``localhost`` is the pod. But a **user interface
has exactly one consumer: a browser on the host.** Nothing in a container ever
fetches Superset's login page. For that category the multi-resolver
requirement simply does not arise, and a port is sufficient.

**The mechanism is already written.** ``portVhostServer``
(``internal/router/env.go:166-203``) serves one Ingress-exposed UI at the root
of a published host port, and its own doc comment states the purpose -- "so a
UI can be served at ``http://localhost:<port>/`` with no DNS at all". It sets
``Host`` to the Ingress host rather than forwarding it, so Traefik still
matches the rule, and adds ``proxy_redirect`` so an absolute ``Location`` does
not bounce the browser to a name it cannot resolve. ``PortRoute`` and
``WriteEnvVhosts``'s ``portRoutes`` parameter exist. There is a unit test at
``internal/router/env_test.go:110-129``. The Python twin exists at
``src/python/hmd_cli_neuronsphere/nginx_router.py:777-846``.

It has never been called. ``environment.go:714`` passes ``nil``, and
``nginx_router.py:1252`` passes nothing. The escape hatch was built, tested,
documented and left unwired -- and the one place a port *is* used, the
Deployment GUI on ``19003``, is exactly the place :doc:`/modes` (``:159``) can
say "nothing needs an ``/etc/hosts`` entry".

Design
------

.. spec:: Every Ingress-exposed user interface is served on a published port
    :id: HMD_CLI_NEURONSPHERE_NERD025_SPEC001
    :links: HMD_CLI_NEURONSPHERE_NERD025
    :status: proposed

    After a cluster is provisioned and after each deploy, every Ingress host
    discovered by ``k3s.IngressHosts`` shall be given a ``router.PortRoute``
    alongside the wildcard vhost it already gets, so the UI answers at
    ``http://localhost:<port>/`` as well as at its hostname.

    Both writers shall be wired: ``environment.go:708-718`` on the provisioning
    path and ``refreshAfterDeploy`` (``:916-977``) on the deploy path, which
    repeats the same aliasing and must not drift from it.

    The hostname is not withdrawn. It remains correct, remains what the cloud
    uses, and starts working the moment NERD026 or an ``/etc/hosts`` line makes
    it resolve. This spec removes the *requirement*, not the capability.

.. spec:: A user interface's port is allocated from a band and persisted
    :id: HMD_CLI_NEURONSPHERE_NERD025_SPEC002
    :links: HMD_CLI_NEURONSPHERE_NERD025
    :status: proposed

    ``PortsPerEnv`` shall not be widened. Slot 0's spare port is the Deployment
    GUI's published ``19003`` (``internal/status/status.go:33-35``), and
    ``internal/registry/registry.go:494`` records that the stride therefore
    cannot be renumbered without moving a port users already have.

    Instead a **UI band** is added above the k3s band, which occupies
    ``base + MaxEnvs*PortsPerEnv + slot`` (``19064-19079``)::

        UIPortBase = base + MaxEnvs*PortsPerEnv + MaxEnvs   // 19080
        UIsPerEnv  = 8
        UIPort(slot, idx) = UIPortBase + slot*UIsPerEnv + idx

    ``HMD_LOCAL_ENV_PORT_RANGE``'s default widens from ``19000-19079`` to
    ``19000-19215`` so ``hmd_proxy`` -- the only container that publishes host
    ports -- carries the band.

    The host-to-port assignment shall be **persisted** in the registry, not
    recomputed. ``internal/registry``'s package doc already gives the rule: a
    derived value that changes later orphans what was named under the old one.
    Here the thing named is a URL a user has bookmarked.

    Exhaustion shall be reported, not absorbed. A ninth UI in an environment
    shall say so, the way ``AllocatePortSlot`` says "all 16 environment port
    slots are in use", rather than silently going unrouted.

.. spec:: The Floci names are resolved by ``nsctl``, not by the machine
    :id: HMD_CLI_NEURONSPHERE_NERD025_SPEC003
    :links: HMD_CLI_NEURONSPHERE_NERD025
    :status: proposed

    ``hmd_proxy`` already publishes ``4566`` on loopback, so the address a
    presigned URL needs is reachable; only the *name* is not. ``nsctl``'s HTTP
    clients shall install a ``DialContext`` that maps ``neuronsphere:<port>``
    and ``neuronsphere-workload:<port>`` to ``127.0.0.1`` at the control
    plane's Floci port.

    The override changes where the connection goes and **leaves the** ``Host``
    **header untouched**. This is not incidental: ``host`` is a signed header
    in SigV4, so rewriting the URL to ``localhost:4566`` would invalidate the
    signature on the very URL being fetched. Overriding the dial preserves it.

    ``internal/artifact/artifact.go:460-469`` already recognises this exact
    failure -- a ``net.DNSError`` on ``neuronsphere`` -- and advises an
    ``/etc/hosts`` line. With the override in place that branch stops being the
    expected path and becomes a genuine diagnostic; its guidance changes
    accordingly.

.. spec:: Name resolution is reported, not required
    :id: HMD_CLI_NEURONSPHERE_NERD025_SPEC004
    :links: HMD_CLI_NEURONSPHERE_NERD025
    :status: proposed

    ``CheckHostsEntries`` shall stop failing ``control-plane start``. The check
    itself is kept and keeps its seam -- it resolves names through an injected
    ``Resolver`` and is not runtime-sensitive -- but its result becomes a
    ``doctor`` row (``internal/doctor/doctor.go:161-169``) rather than a gate.

    What it reports changes with it. Today it prints one mandatory remedy.
    It shall instead report the names as unresolved and name both remedies --
    NERD026's resolver, or the ``/etc/hosts`` line -- and say what is degraded
    without them, which after SPEC003 is the legacy Python artifact path rather
    than everything.

    ``doctor.go:29-35`` already removes this check from the start path's own
    suite on the grounds that the start runs it "in its own order and with its
    own error". With the gate gone, that special case goes too and the start
    reports it like any other check.

.. spec:: An Ingress hostname names its environment
    :id: HMD_CLI_NEURONSPHERE_NERD025_SPEC005
    :links: HMD_CLI_NEURONSPHERE_NERD025
    :status: proposed

    There is a defect here today. ``IngressHostFor``
    (``internal/router/env.go:129-137``) builds
    ``<instance>.local.neuronsphere.io`` from the constant ``HelmLocalSlug =
    "local"``, because ``hmd-cli-helm`` writes that literal into every local
    chart's ``alb.hostname`` in *every* environment. But the vhost written at
    ``:217`` is ``*.<env.Slug>.neuronsphere.io``. For any environment not named
    ``local`` the vhost matches nothing the charts actually ask for, and two
    environments that both deploy ``airflow`` claim one hostname between them.

    The hostname shall carry the environment in its **leftmost label**, so a
    single suffix covers every environment::

        <instance>-<slug>.local.neuronsphere.io   // any environment
        <instance>.local.neuronsphere.io          // the default `local`

    The default environment's form is preserved exactly, so every URL in the
    documentation today -- ``auth.``, ``registry.``, ``web.local.neuronsphere.io``
    -- continues to mean what it means now.

    The Ingress object's host shall be rewritten at deploy time rather than
    changing ``hmd-cli-helm``. ``NormalizeIngressPaths`` /``traefikPath``
    (``internal/k3s/discover.go:229-326``) already rewrites ALB-dialect ``/*``
    paths on deployed Ingress objects for precisely this class of local-versus-
    cloud mismatch, and this is the same kind of fix in the same place.

    ``IngressHostFor`` becomes slug-aware, and its consumer -- the
    ``{ingress_host}`` placeholder at ``internal/credentials/credentials.go:212``
    -- follows it.

    One label rather than two is also what makes a wildcard possible at all:
    DNS matches ``*`` at exactly one level, so ``*.local.neuronsphere.io``
    can cover every environment only if the environment is not its own label.
    NERD026 depends on this.

.. spec:: A start reports the address that works
    :id: HMD_CLI_NEURONSPHERE_NERD025_SPEC006
    :links: HMD_CLI_NEURONSPHERE_NERD025
    :status: proposed

    ``readySummary`` (``environment.go:551-590``) shall lead with the port URL
    for each UI, and show the hostname alongside it only when that hostname
    actually resolves -- which ``unresolvableHosts`` (``:894-913``) already
    determines and currently uses only to raise a warning.

    ``env status`` lists no UIs at all today (``internal/status/status.go:305-331``).
    It shall list them, read from the router's own fragments the way
    ``StreamsPort`` and ``RoutesService`` (``router/env.go:242-270``) already
    read what an environment routes -- so it stays cheap and still answers on a
    stopped environment.

    This continues NERD023 SPEC003's correction: report what was found, not
    what the port scheme reserves.

.. spec:: A taken port is refused, and the refusal says what to do
    :id: HMD_CLI_NEURONSPHERE_NERD025_SPEC007
    :links: HMD_CLI_NEURONSPHERE_NERD025
    :status: proposed

    Publishing a wider band makes a foreign listener more likely, and the
    existing handling did not survive contact with one: ``CheckInUse`` warned
    and the start continued into the engine's own bind failure, which names a
    port and nothing else. The diagnosis was computed and thrown away -- the
    same shape NERD023 SPEC003 corrected for Ingress hostnames.

    **The probe shall ask the question the engine asks.** A connect to
    ``127.0.0.1`` and a bind of ``0.0.0.0`` are different questions, and they
    disagree in both directions that matter: a listener pinned to a real
    interface answers no connect on loopback and still refuses the engine's
    bind, and a UDP listener answers no TCP connect at all. Both were then
    reported as a free port. ``BindProber`` attempts the bind instead, on the
    protocol and interface the binding actually names.

    It clears ``SO_REUSEADDR``, which Go sets on every listener by default and
    the engine does not. This is not a detail: with it set, binding ``0.0.0.0``
    succeeds on a BSD kernel while another socket holds the same port on a
    specific interface, so the probe passes and the engine still fails. The
    unit test asserting the interface case caught exactly this.

    A failed bind is a conflict only when it failed for being taken.
    ``EACCES`` on a port below 1024 -- Linux, where ``nsctl`` has no privilege
    -- means "cannot tell", not "in use", and falls back to the connect probe
    rather than refusing a start that would have worked.

    **A conflict shall refuse the start**, because ``hmd_proxy`` publishes its
    ports as one contiguous range: a single foreign listener fails the whole
    container and takes every route down with it, so continuing only delays the
    same failure into a worse message. The exception is an engine that could
    not be asked which ports are already ours, where the findings are guesses
    and refusing on one would block a start over the platform's own ports.

    **The refusal shall name every override** -- ``HMD_LOCAL_ENV_PORT_BASE``,
    ``HMD_LOCAL_ENV_PORT_RANGE``, ``HMD_LOCAL_TRINO_HOST_PORT``,
    ``HMD_LOCAL_GUI_HOST_PORT``, ``HMD_LOCAL_DNS_PORT`` -- and say plainly that
    80 and 4566 are fixed, rather than implying an override that does not
    exist. A test asserts each named variable is one something reads.

    **The engine's own failure shall still be translated**, as a backstop for a
    port taken in the moment between the probe and the bind. ``BindFailure``
    reads the port out of the driver's prose and reports it in the same terms
    the probe would have.

.. spec:: The published ports are chosen, not fixed
    :id: HMD_CLI_NEURONSPHERE_NERD025_SPEC008
    :links: HMD_CLI_NEURONSPHERE_NERD025
    :status: proposed

    Naming an override in a refusal (SPEC007) is still friction: the user has
    to read it, understand it and set something before anything runs. A local
    platform should coexist with whatever else is on the machine without being
    told how.

    ``80`` and ``4566`` were never fixed for a reason. They were fixed because
    ``http://localhost/...`` and ``http://localhost:4566`` were written down as
    literals, which NERD007 SPEC001 identified and left unbuilt. ``4566`` is
    also LocalStack's port, so a developer running one for another project
    cannot start a local NeuronSphere at all.

    **This home's published ports are settled before the project is built**, by
    probing and keeping the defaults wherever they are free. Only a port that
    is genuinely taken is chosen anew, and only that one is recorded -- so a
    machine that works today does not move and writes nothing. The recorded set
    describes what is unusual about the machine rather than restating the
    defaults.

    The environment band is claimed first: it needs 112 contiguous ports and
    has the least room to manoeuvre, and a single port chosen first could sit
    in the middle of the only window wide enough. The band then defers to the
    other ports' preferred values, so moving it does not evict the resolver
    from 19153 and turn one busy port into two moved ones. A moved band takes
    every environment's ``port_base`` with it, or their ports would land
    outside what ``hmd_proxy`` publishes.

    An alternative is searched for from a **conventional second choice rather
    than the next port up**: 8080 for HTTP, not 81. Scanning 81..1023 would
    probe a thousand ports Linux forbids an unprivileged process from binding,
    each falling back to a connect probe with a timeout. Nothing is chosen at
    or above 32768, the lower of the two ephemeral floors, or the operating
    system could hand the port to something else between one start and the next.

    **Every host-facing URL is derived from those ports**, through
    ``internal/hosturl``, resolved once per process before any command runs.
    This is the part NERD007 called the bulk of the work and most of the risk:
    a URL still written down somewhere reaches the wrong platform, and does so
    successfully.

    **A presigned URL keeps naming the in-network port**, because that is what
    Floci stamped into it and what the signature covers. The dial redirect of
    SPEC003 maps it to wherever the host publishes it -- so the port moves and
    the signature does not. Its rule widens accordingly: redirect a name that
    does not resolve *or resolves to loopback*, since both mean the host. A
    name answering with a routable address is a Docker alias to a sibling
    container and is left alone.

    The Python front end is **not** converted. It writes the default ports in
    some eighty places and is deprecated; it refuses a home whose ports have
    moved, naming ``nsctl env start``, rather than starting a platform
    addressed one way and talking to it another.

Alternatives considered
-----------------------

**A public wildcard record in the** ``neuronsphere.io`` **zone.** One A record,
``*.local.neuronsphere.io`` to ``127.0.0.1``, would make every hostname in this
document resolve for every user with no client-side configuration at all --
the ``localtest.me`` and ``sslip.io`` trick under a domain we already own.
Nothing in the corpus had ever considered it.

Rejected on two counts. Operationally the record is not ours to add. And on
the merits it would make local development depend on a public zone being alive
and correct: it fails offline, and resolvers that implement DNS-rebinding
protection refuse public names that answer with loopback, which would produce
a confusing failure on exactly the corporate laptops least able to debug it.
NERD026 gets the same wildcard without either exposure.

**Rewriting the presigned URL's host to** ``localhost``. Simpler than a dial
override and wrong: ``host`` is a signed header under SigV4, so the rewrite
invalidates the signature on the URL being rewritten.

**Setting** ``FLOCI_HOSTNAME`` **to** ``localhost``. Fixes the host and breaks
everything else -- in-network consumers would receive URLs naming themselves.
The name has to stay correct in-network; only the host's *resolution* of it is
negotiable.

**Having** ``nsctl`` **write** ``/etc/hosts`` **under an explicit** ``--fix``.
It would work and it is what many tools do. It contradicts two standing
decisions: ``nsctl`` deliberately does not need root (NERD007:115), and
deliberately does not edit the user's files -- NERD023 rejected writing an
``export HMD_HOME`` line into a shell profile on the grounds that "it is their
file, the correct file is unknowable, and an appended line is a change they did
not review". A hosts file is more load-bearing than a shell profile, not less.

**Path-prefixing the UIs** behind ``/local/airflow/`` instead of using ports.
Already considered and rejected when the ingress class was chosen
(``decisions/2026-08-28-local-traefik-answers-to-the-alb-ingress-class``): it
needs ``base_url`` or ``--base-href`` set in each application's chart or static
assets and redirects break, and it is not how the cloud addresses them. A port
serves the application at a root, which is the shape the application expects.

Out of scope
------------

- **The legacy Python artifact path.** ``hmd build`` and ``push-artifact``
  dereference presigned URLs through ``hmd_lib_librarian_client``, in another
  repository, which SPEC003's Go dial override does not reach. Those commands
  continue to want either the ``/etc/hosts`` line or NERD026 until the same
  treatment is applied there. This is stated in the documentation rather than
  implied away.
- **The names that must stay names.** The identity provider's issuer, the
  package registry and control-plane extensions are not user interfaces and
  cannot be reached by port. They are :doc:`NERD026_Local_Name_Resolution`.
- **TLS.** Everything here is plain HTTP, as it is today. A real certificate
  for a local hostname is a separate question and does not block any of this.

Risks
-----

- **An application that leaks its Ingress hostname in a response body.**
  ``portVhostServer`` rewrites ``Host`` and undoes the substitution on
  ``Location`` via ``proxy_redirect``, but an absolute URL emitted inside HTML
  or JavaScript is not a header and will not be rewritten. Superset and Airflow
  can both do this. This is the principal implementation risk of SPEC001, it is
  per-application rather than general, and the hostname path remains available
  as the fallback wherever it bites.
- **Widening the published port range recreates** ``hmd_proxy``. A visible
  restart on upgrade, once.
- **Ports are not cloud parity.** The cloud reaches a UI by hostname and always
  will. SPEC001 makes the zero-setup path work; it does not make ports the
  model. NERD026 is what keeps parity available to anyone who wants it, and
  SPEC001 deliberately leaves the hostname in place rather than replacing it.
