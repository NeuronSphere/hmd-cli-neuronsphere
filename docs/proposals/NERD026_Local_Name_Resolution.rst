.. NERD026 Local Name Resolution

NERD026 Local Name Resolution
=============================

.. req:: A name that must be one string shall resolve on the host without a per-name privileged edit
    :id: HMD_CLI_NEURONSPHERE_NERD026
    :status: implemented

    Where local NeuronSphere requires a hostname that a browser, a sibling
    container and a cluster pod must all resolve to the same place, the host's
    half of that resolution shall be arranged **once per machine** and shall
    then cover every such name, including names for services that have not been
    deployed yet.

    The arrangement shall be offline-capable, shall depend on no public DNS
    zone, and shall be scoped so that it cannot capture a name that is not
    ours.

    ``nsctl`` shall print the one privileged step and shall not perform it.

    .. note::

        ``implemented``. SPEC001 through SPEC004 are built: the resolver runs in
        the control plane by default and answers the suffix by wildcard at any
        depth, ``nsctl dns install`` prints one ``sudo`` line and runs nothing,
        ``dns status`` and a ``doctor`` row tell the two failures apart, and the
        two bare single-label names stay with NERD025 SPEC003's dial override.

        SPEC005 remains ``proposed``. It is blocked on NERD007 SPEC002 -- there
        is no per-home Floci hostname yet to place under the suffix -- and not on
        anything in this document.

Motivation
----------

:doc:`NERD025_Port_First_Addressing` removes the ``/etc/hosts`` requirement
from everything that starts or deploys a platform. What is left is every name
a person actually opens, and the names more than one kind of consumer reads:

- **The identity provider's issuer.** ``internal/authd/authd.go:9-17`` spells
  out why: the ``iss`` claim has to be a single string however the server was
  reached, "or a consumer that fetched JWKS under one and reads ``iss`` as
  another rejects every token -- and the failure surfaces as a policy denial,
  nowhere near the cause."
- **The package registry.** NERD006 SPEC002 makes the same argument from a
  different direction: an index URL is written into ``uv.toml``, ``~/.pypirc``,
  ``~/.npmrc``, ``GOPROXY``, image tags and BuildKit secrets, by different
  tools at different times, and cannot be format-specific.
- **Control-plane extensions**, which are served by name
  (``internal/cpext/apply.go:265-276``) and whose set is open-ended.
- **Every Ingress-exposed user interface.** NERD025 SPEC001 briefly served
  these on ports instead; it was withdrawn, because a port costs a rewritten
  ``Host`` header, diverges from how the cloud reaches the same chart, and
  reserved 32 ports to serve a typical three.
- **A second concurrent control plane.** NERD007 needs a per-home Floci
  hostname and concludes at ``:113-115`` that this "cannot be made invisible to
  the user: ``/etc/hosts`` needs root, and ``nsctl`` deliberately does not."

Each of these is, today, another line in ``/etc/hosts``. And the file cannot
express the thing that would actually solve it: ``/etc/hosts`` has no
wildcards, so it can never cover a name that does not exist yet. Every new
extension, every new environment, every new registry format is another
privileged edit.

A resolver can express it. One wildcard, arranged once, covers every name in
the suffix forever -- including the ones nobody has thought of.

Design
------

.. spec:: A resolver answers the local suffix from the control plane
    :id: HMD_CLI_NEURONSPHERE_NERD026_SPEC001
    :links: HMD_CLI_NEURONSPHERE_NERD026
    :status: implemented

    The control plane shall run a small DNS server answering every name under
    ``local.neuronsphere.io`` with ``127.0.0.1``, reachable from the host at
    ``127.0.0.1:19153``.

    It publishes no host port of its own. ``hmd_proxy`` is the only service
    that may publish one -- ``compose.ProxyService``, and the invariant is
    checked -- so the proxy carries the listener and streams it on to the
    resolver over UDP, with ``proxy_responses 1`` so a lookup is one query and
    one answer rather than a session held open until it times out.

    The proxy binds this one explicitly to ``127.0.0.1``, unlike the rest of
    the band: it answers ``127.0.0.1`` for every name it owns, which is an
    answer that means nothing to anyone else.

    It is authoritative for that suffix and forwards nothing. It therefore
    works with no network at all, which a public zone cannot, and it exposes
    nothing, which a public zone does.

    It answers by wildcard, not from a list. Nothing registers a name with it
    and nothing has to: a name that will exist after the next deploy already
    resolves.

    One correction from closing it out. ``HMD_LOCAL_DNS_PORT`` moved the
    published port and the nginx upstream but not the resolver's own listener,
    which was a literal in the container's command -- so a home that had to move
    the resolver published one port and served another, and the suffix stopped
    resolving with nothing to see but a timeout. Listener, publish and upstream
    are one variable now, and a test parses the bundled file with the port moved
    and asserts all three followed.

.. spec:: The machine is pointed at it once, and never edited again
    :id: HMD_CLI_NEURONSPHERE_NERD026_SPEC002
    :links: HMD_CLI_NEURONSPHERE_NERD026
    :status: implemented

    ``nsctl dns install`` shall **print** the single privileged command for the
    detected platform and shall not run it -- the same posture NERD023 takes
    toward the user's shell profile, and the reason ``nsctl`` has no ``sudo``
    in it today.

    On macOS that is a resolver file::

        /etc/resolver/local.neuronsphere.io
            nameserver 127.0.0.1
            port 19153

    **The file is named for** ``local.neuronsphere.io`` **and not for**
    ``neuronsphere.io``. A resolver file captures its whole subtree, so the
    broader name would route ``www.neuronsphere.io`` -- a real, public,
    unrelated marketing site -- into a resolver that answers ``127.0.0.1`` for
    anything it is asked. The narrow suffix is a correctness requirement, not
    tidiness.

    On Linux there is no ``/etc/resolver``. The equivalent is a
    ``systemd-resolved`` routing domain, and where that is not available the
    platform falls back to the ``/etc/hosts`` line it uses today. Platform is
    detected through the ``GOOS`` seam ``internal/doctor/doctor.go:21`` already
    carries.

    After this one step the file is never touched again. A new extension, a new
    environment or a second control plane costs nothing further.

    The printed step names **the port this home actually serves**, not the
    default. The resolver is a chosen port (NERD025 SPEC008), and printing the
    constant told the user to point their machine at a port nothing listens on --
    a resolver file that fails silently and, per the Risks below, adds latency to
    every lookup in the suffix. Precedence is the user's own ``--port`` or
    ``HMD_LOCAL_DNS_PORT``, then what this home recorded, then the default; a
    home that cannot be read is not an error, because ``dns install`` is
    informational and has to work anywhere.

.. spec:: Resolution is a reported fact
    :id: HMD_CLI_NEURONSPHERE_NERD026_SPEC003
    :links: HMD_CLI_NEURONSPHERE_NERD026
    :status: implemented

    ``nsctl dns status`` and a ``doctor`` row shall report whether the suffix
    resolves, distinguishing the two failures that need different fixes: the
    resolver container is not running, or the machine is not pointed at it.

    The check shall resolve a name that has deliberately **not** been
    configured anywhere -- proving the wildcard, which is the property
    ``/etc/hosts`` cannot have and therefore the property worth asserting.

    As built, the two failures are told apart by asking two independent
    questions: does the resolver answer when asked **directly** on its own port,
    and does the name resolve through **the system resolver**. That gives four
    states rather than two, and the fourth is worth having -- the suffix resolves
    while the resolver is down, which means an ``/etc/hosts`` line or a stale
    cache is answering and names that have not been deployed yet will not
    resolve. ``dnsd.Answers`` is the direct probe; it is a hand-rolled A query
    over UDP, for the same reason the server is hand-rolled -- a DNS library here
    drags in ``golang.org/x/net`` and bumps ``golang.org/x/term`` with it.

    The ``doctor`` row is ``local names``, separate from the existing ``host
    names`` row: one is the wildcard suffix, the other the two bare single-label
    names that no suffix-scoped resolver can claim (SPEC004). They fail
    independently and are fixed differently. Both belong to ``doctor.Run``, the
    diagnostic, and neither to ``doctor.Gate``, the start's preflight -- nothing
    here is a reason to refuse a start.

.. spec:: Single-label names stay with the dial override
    :id: HMD_CLI_NEURONSPHERE_NERD026_SPEC004
    :links: HMD_CLI_NEURONSPHERE_NERD026
    :status: implemented

    ``neuronsphere`` and ``neuronsphere-workload`` have no dot, and a
    suffix-scoped resolver cannot claim them. They remain served by NERD025
    SPEC003's dial override inside ``nsctl``.

    ``FLOCI_HOSTNAME`` shall **not** be changed to an FQDN to bring it under
    the suffix. Presigned URLs already issued name the old host, the bare name
    is what the in-cluster CoreDNS records map (:doc:`/environments`
    ``:458-482``) and what charts use for cloud parity
    (``http://neuronsphere:4566``), and the rename would have to move all of it
    together for no gain the override does not already deliver.

.. spec:: A second control plane stops costing a privileged edit
    :id: HMD_CLI_NEURONSPHERE_NERD026_SPEC005
    :links: HMD_CLI_NEURONSPHERE_NERD026
    :status: proposed

    NERD007 SPEC002 gives a non-first home the hostname
    ``neuronsphere-<hash>`` and records the ``/etc/hosts`` line as "the
    user-visible cost of the feature", to be "stated as such rather than
    engineered around".

    Under this document that cost is gone, provided the derived name is placed
    **under the covered suffix** rather than as a bare label -- so
    ``<hash>.local.neuronsphere.io`` rather than ``neuronsphere-<hash>``. The
    wildcard already answers it.

    NERD007 is amended to say so rather than rewritten; its port-base and
    account-id specs are unaffected.

    .. note::

        **Not built, and blocked on NERD007 SPEC002 rather than on anything
        here.** A per-home Floci hostname does not exist yet: ``FLOCI_HOSTNAME``
        is the flat literal ``neuronsphere`` and ``loopback.Names`` is a fixed
        two-element list. The home hash is derived (``container.HMDHomeHash``)
        but its only hostname-shaped use is the k3s cluster name. There is
        therefore no name to place under the suffix. The amendment to NERD007 is
        in place, so the shape is settled the moment that spec is built; this one
        stays ``proposed`` until then.

Alternatives considered
-----------------------

**A public wildcard record in the** ``neuronsphere.io`` **zone.** The least
work by a wide margin -- one A record and every user is done, with nothing
installed. Rejected for the reasons given in NERD025: the record is not ours to
add, it fails with no network, and DNS-rebinding protection rejects public
names that answer with loopback on exactly the managed laptops least able to
diagnose it. A local resolver gets the same wildcard and none of that.

**Relying on** ``*.localhost``. The specification reserves it and browsers
resolve it internally, so it looks like a free answer. It is not one on macOS:
``getaddrinfo`` does not resolve ``foo.localhost``, which was confirmed
directly on a current macOS during this investigation. Anything that is not a
browser -- ``curl``, a Go client, a Python client, the Robot suites -- would
fail, and it would fail differently per tool.

**mDNS and a** ``.local`` **name.** Resolves without configuration on macOS,
but ``.local`` is mDNS-reserved, collides with the ``local.neuronsphere.io``
suffix in a way that confuses more than it helps, is unreliable inside
containers, and gives no wildcard.

**Having the user install dnsmasq through Homebrew or apt.** Same end state,
but it makes local NeuronSphere depend on the user's package manager and on a
daemon we do not version. Docker is already the one prerequisite; the resolver
should be an image like everything else.

**Writing** ``/etc/hosts`` **from** ``nsctl`` **under** ``--fix``. Rejected in
NERD025 for the same reasons, and it would not help here anyway: the residue
this document addresses is open-ended, and a hosts file cannot hold a wildcard
however it is written.

Out of scope
------------

- **TLS for local hostnames.** A resolver makes the names work; it does not
  make them ``https``. Nothing in scope requires TLS today
  (``internal/authd/authd.go:21-28`` records why), and a certificate is a
  separate question.
- **Container and pod resolution.** Already solved and unchanged: Docker
  network aliases on ``hmd_proxy``
  (``internal/container/docker.go:388-421``) and ``coredns-custom`` records in
  the cluster. This document is about the host's leg only.
- **Making the resolver mandatory.** Starting a platform and deploying to it
  need nothing from it; only reaching a user interface does. It therefore runs
  by default and is inert until the machine is pointed at it, but nothing
  refuses to start without it.

Risks
-----

- **A VPN or corporate resolver that overrides per-domain settings.** Some
  managed configurations take precedence over ``/etc/resolver``. Where that
  happens the ``/etc/hosts`` line remains available for the fixed names, and
  NERD025's ports remain available for the UIs -- so the failure degrades to
  today's behaviour rather than to nothing.
- **A stale resolver file outliving the platform.** A resolver file pointing at
  a port nothing listens on adds latency to every lookup in the suffix.
  ``nsctl dns status`` is what makes this visible, and an uninstall step should
  print the removal as plainly as the install printed the creation.
- **The obvious port does not work, and this was checked rather than assumed.**
  5353 is mDNS, and on macOS it is not only ``mDNSResponder``: Chrome and
  Spotify were both found holding ``*:5353`` on a developer machine. They share
  it with ``SO_REUSEPORT``, which Docker's port publisher does not set, so a
  plain bind to ``127.0.0.1:5353`` is refused with ``EADDRINUSE`` -- verified
  directly, alongside the same bind with ``SO_REUSEPORT`` succeeding. A default
  of 5353 would have failed to start on a typical Mac.

  The resolver therefore listens on **19153**: above the ``19000-19079`` band
  ``hmd_proxy`` publishes, since a port inside it could not be bound by a
  second container at all; below the 49152 ephemeral floor, so the OS never
  hands it out at random; and leaving ``19080-19152`` as headroom if the
  published band ever grows. ``HMD_LOCAL_DNS_PORT`` overrides it, and the
  resolver file carries whatever port is chosen, so a machine that needs a
  different one costs nothing.
