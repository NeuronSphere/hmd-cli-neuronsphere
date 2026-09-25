.. NERD027 Ports Without Reservation

NERD027 Ports Without Reservation
=================================

.. req:: A local platform shall publish the host ports it is using, and no others
    :id: HMD_CLI_NEURONSPHERE_NERD027
    :status: proposed

    The number of host ports a local NeuronSphere binds shall be a function of
    what is running, not of what could theoretically run.

    No port shall be bound on behalf of a service that has not been deployed,
    an environment that does not exist, or a capacity nobody uses.

    Adding or removing a port shall not interrupt a route that something else
    is in the middle of using.

Motivation
----------

``hmd_proxy`` publishes 84 host ports. On the machine this was written on, four
listeners exist behind them::

    listen 80     (x2: default_server and the wildcard vhost)
    listen 4566   (the Floci stream)
    listen 19003  (the Deployment GUI)

Eighty of the eighty-four are the environment band, ``19000-19079``, and most
of it cannot ever carry anything. Per environment the slot stride reserves four
ports, of which:

- ``FlociPort`` (``+0``) carries no listener. ``registry.go`` says so itself:
  the single Floci is reached on the control plane's ``4566``.
- ``TrinoPort`` (``+1``) carries one, and only when a coordinator was found.
- ``GraphPort`` (``+2``) is never called outside tests.
- ``SparePort`` (``+3``) was written into ``router.Env`` at three call sites and
  read nowhere. :doc:`NERD025_Port_First_Addressing` removed the field.

Sixteen environments times three dead ports, less the one the Deployment GUI
occupies by coincidence of a constant, is **47 ports bound at every start that
nothing can ever answer on**. Each is an individual Engine API binding
(``compose.portSpec``) and an individual in-use probe (``compose.HostPorts``).

Since NERD025 SPEC008 they cost a third thing. ``nsctl`` now chooses its ports
around whatever else is on the machine, and the band has to be placed as one
contiguous run -- so the reservation is also the single hardest thing to find
room for. An 80-wide window is far more likely to fail than the handful of
ports actually in use.

Why it is a band at all
~~~~~~~~~~~~~~~~~~~~~~~

One constraint, and it is real. **An engine cannot add a published port to a
running container.** A compose ``ports:`` list is fixed when the container is
created, so a port that was not published can only be added by recreating.

``hmd_proxy`` is the single thing every route passes through, including the
route an in-flight deploy is using to reach ms-deployment. Recreating it to add
a port would cut the deploy that asked for the port -- and a deploy is exactly
when a new port appears, because deploying ``hmd-inf-trino`` is what makes a
Trino coordinator exist.

So the reservation is not laziness. It is the only way to publish a port before
it is needed **while there is one publisher**. That last clause is the part
worth attacking.

Design
------

.. spec:: The control-plane proxy publishes only what is always there
    :id: HMD_CLI_NEURONSPHERE_NERD027_SPEC001
    :links: HMD_CLI_NEURONSPHERE_NERD027
    :status: proposed

    ``hmd_proxy`` shall publish four ports and no range: the HTTP port, the
    Floci stream, the Deployment GUI, and the resolver's UDP listener. Each
    exists for the life of the control plane, so none of them ever needs to be
    added to a running container.

    **The GUI port must be published explicitly.** Today it is bound only
    because ``19003`` happens to fall inside ``19000-19079``; removing the
    range without naming it would take the Deployment GUI off the host with no
    other sign than a refused connection. This is the one place where deleting
    the reservation is not purely subtractive.

    Two things settled while preparing this, both of which the implementation
    depends on:

    **The GUI becomes a chosen port in its own right.** It already had to become
    a *recorded* one: closing out NERD025 SPEC008 showed that a moved band left
    the GUI bound to nothing, so ``registry.PortGUI`` exists and both readers --
    the vhost writer and ``env status`` -- read it back. Until the band is gone it
    moves with the band, because that is where it is published from. Once the band
    goes it joins ``portPlans`` and is probed like every other single port, which
    is also the moment its default stops being derivable from slot arithmetic.

    **The bundled compose file is generated, not authored.**
    ``internal/bundled/services/docker-compose.control-plane.yml`` is a copy that
    ``make generate-local`` overwrites from
    ``src/python/hmd_cli_neuronsphere/services/docker-compose.control-plane.yml``,
    and the copy is gitignored. The published port list is therefore edited in the
    Python tree -- which also means the deprecated front end sees the same four
    ports, and its own refusal of a moved home (NERD025 SPEC008) is what keeps it
    from addressing them wrongly.

.. spec:: An environment publishes its own ports
    :id: HMD_CLI_NEURONSPHERE_NERD027_SPEC002
    :links: HMD_CLI_NEURONSPHERE_NERD027
    :status: proposed

    Each environment shall have a router container of its own --
    ``hmd_router-<slug>``, beside ``hmd_db-<slug>`` and ``global-graph-<slug>``
    -- publishing exactly the host ports that environment uses: its Trino
    listener where a coordinator was found, its k3s API listener where a
    cluster is running, and the historical ``18080`` for the default
    environment.

    It is created with the environment and removed with it, and recreated when
    that set changes. Recreating it interrupts a ``kubectl`` session or a Trino
    client against **that one environment**. It does not touch
    ``hmd_proxy``, so it cannot interrupt a service route, a deploy, or another
    environment -- which is the whole reason the set can now change at runtime.

    Per environment rather than one shared dynamic publisher, for the same
    reason every other piece of environment state is: a second environment
    appearing must not disturb the first. It costs one ``nginx:stable-alpine``
    per environment, which is the price of not reserving eighty ports for
    sixteen environments nobody runs.

    How it is created, which the text above leaves open and the code cannot:

    **Not as a compose service.** No environment runs a compose project today.
    ``hmd_db-<slug>``, ``global-graph-<slug>`` and the cluster are all
    Floci-spawned and reached by Docker network alias, and ``env.ComposeProject``
    is recorded in the registry and used by nothing. ``hmd_router-<slug>`` is
    therefore the first container ``nsctl`` creates for an environment itself,
    through the ``docker`` CLI, carrying that recorded project as its label --
    which is exactly what makes SPEC004's ``OurPorts`` clause a one-line change
    rather than a new mechanism.

    **Its config is its own root.** ``internal/router`` assumes one config
    directory and one container. The environment's *stream* fragment moves to a
    per-environment root with a ``stream``-only ``nginx.conf`` -- no ``http``
    block, no vhosts -- and ``Reload`` becomes slug-scoped. The environment's
    **HTTP routes and vhosts stay on** ``hmd_proxy``, because they are served
    through the HTTP port, so ``RemoveEnvRoutes`` spans two roots and
    ``StreamsPort`` -- which is what ``env status`` reads to decide whether Trino
    is routed -- follows the fragment to the new root or silently reports every
    environment as having no Trino.

    **It is recreated only when the set changes**, compared against the running
    container's own published ports. An ordinary restart with the same set must
    not churn a ``kubectl`` session for nothing.

    **It is created after the set is known and before the kubeconfig is
    written.** Both dynamic ports are discovered inside ``startCluster`` -- the
    k3s upstream first, the Trino coordinator only if one was found -- and
    ``WriteKubeconfig`` bakes the k3s port into the file every later ``kubectl``
    uses. The post-deploy path recreates it, because deploying is what makes a
    Trino coordinator exist.

    **Every lifecycle stage grows a case**, and the removal has to be the sweep
    that already removes the environment's containers rather than a second one:
    the purge's catch-all sweep matches a Floci account label, which a container
    ``nsctl`` created itself does not carry. ``env delete`` must refuse while it
    is running, for the same reason it refuses on a running database.

    The k3s API stays behind a proxy rather than being published by the
    Floci-spawned cluster container. ``registry.go`` records why and it has not
    changed: the engine re-creates that container's port forward on every
    restart, and a re-created forward silently truncates writes past roughly
    one MTU -- which a TLS 1.3 ClientHello carrying a post-quantum key share
    exceeds, hanging every client against a healthy cluster.

.. spec:: The slot arithmetic survives as an address, not a reservation
    :id: HMD_CLI_NEURONSPHERE_NERD027_SPEC003
    :links: HMD_CLI_NEURONSPHERE_NERD027
    :status: proposed

    ``FlociPort``, ``TrinoPort``, ``GraphPort``, ``SparePort`` and ``K3sPort``
    shall keep their current arithmetic. An environment's Trino stays where it
    is, and so does the Deployment GUI's ``19003``.

    Nothing is renumbered, and that is deliberate. ``PortsPerEnv`` cannot
    shrink without moving ports that people have in scripts and bookmarks, and
    the 47 dead ports do not need to be reclaimed -- they need to stop being
    *bound*. Under SPEC002 they are simply never published, because no
    container asks for them.

    The arithmetic keeps its one real virtue: the same environment name lands
    on the same ports across machines and re-creations.

.. spec:: The exclusive-publisher rule names two publishers
    :id: HMD_CLI_NEURONSPHERE_NERD027_SPEC004
    :links: HMD_CLI_NEURONSPHERE_NERD027
    :status: proposed

    ``compose.ProxyService`` encodes "only ``hmd_proxy`` may publish a host
    port", and ``CheckExclusivePublisher`` refuses a start that breaks it. That
    invariant caught a real defect in NERD026 -- the resolver publishing its own
    port -- so it is narrowed rather than dropped: **the control-plane proxy and
    an environment's own router** may publish, and nothing else.

    ``OurPorts`` shall count an environment router's ports as ours, or the next
    start would probe the platform's own listeners, find them busy and move the
    ports out from under the environment that is using them.

    **As scoped, this is a documentary change plus that one clause.**
    ``CheckExclusivePublisher`` inspects the *bundled compose project*, and the
    environment router is not a service in it (SPEC002), so the check has nothing
    to narrow: it keeps refusing any bundled service but the proxy that publishes
    a port, which is still right. The second, independent encoding of the same
    rule for control-plane extensions is left alone deliberately -- an extension
    genuinely may not publish a host port, and nothing here changes that. What
    changes is the rule as stated in ``compose.ProxyService``'s own comment, which
    is where a reader looks to find out who may publish, and ``OurPorts``, which
    is the only place the invariant is load-bearing at runtime.

    The ownership check needs a case too. ``hmd_router-<slug>`` is slug-scoped, so
    the name is global across homes exactly as the pinned control-plane names are,
    and two homes running an environment of the same name would otherwise fight
    over it silently.

.. spec:: Ports are checked per environment, not as a window
    :id: HMD_CLI_NEURONSPHERE_NERD027_SPEC005
    :links: HMD_CLI_NEURONSPHERE_NERD027
    :status: proposed

    ``ChoosePorts`` (NERD025 SPEC008) shall stop looking for a contiguous run
    for the band, because there is no longer a band to place. An environment's
    two ports are probed when its router is created, and an environment whose
    ports are taken moves only its own.

    This is strictly easier than what it replaces: two ports anywhere beat
    eighty in a row.

    **What "moves only its own" means concretely.** The slot arithmetic stays the
    address (SPEC003), so an environment's ports are derived rather than stored,
    and a derived port cannot be moved. The environment therefore records an
    explicit override for the one port that was taken -- beside the port slot it
    keeps -- and the router publishes that. Re-deriving from a fresh slot would
    move both of its ports and break the one property the arithmetic is kept for:
    the same environment name landing on the same ports across machines and
    re-creations.

    The GUI's port joins ``portPlans`` in the same change, since it is no longer
    published by the band it used to sit inside (SPEC001).

Alternatives considered
-----------------------

**Recreate ``hmd_proxy`` when the port set changes.** The obvious answer, and
unsafe: the set changes during a deploy -- a Trino coordinator appears because
something deployed it -- and recreating the proxy mid-deploy cuts the deploy's
own route to ms-deployment. This is the constraint the reservation exists to
work around, and moving the dynamic ports off the proxy is what removes it.

**One shared publisher for all dynamic ports.** Fewer containers, and it
reintroduces the coupling in miniature: bringing up a second environment would
interrupt the first environment's ``kubectl``. The per-environment container
costs more and owes nothing to anyone else.

**A smaller band.** Eight ports instead of eighty would fix the arithmetic and
keep the mistake: a reservation is still a bet on how many environments exist,
made at a moment when the answer is unknown.

**Expose k3s and Trino on demand,** the way ``kubectl port-forward`` does, with
no published port at all. It reserves nothing, and it gives up the stable
address: a kubeconfig that works only while a command is running is worse than
a port nobody is using.

Risks
-----

- **A new container in the environment lifecycle.** Create, stop, delete,
  purge, ``env status`` and the ownership check all grow a case. A router left
  behind by a failed delete holds ports the next start will then route around,
  which is a confusing way to lose a port. The sweep that removes an
  environment's containers has to be the same sweep, not a second one.
- **The Deployment GUI is one edit away from disappearing.** SPEC001 names it,
  and a test should assert the proxy publishes it, because nothing else would
  notice until someone opened it.
- **More containers on a small machine.** One per environment, tiny, but real.
- **This has not been run against a live engine.** A container-lifecycle change
  is not something unit tests finish the argument about.

  The base is no longer unverified, which was the other half of this risk when it
  was written: NERD025 and NERD026 are closed out, with the four
  chosen-port-not-read-back defects fixed and a live run behind them. This is
  still the change most likely to need a second pass after meeting a real engine,
  and it is deliberately the last one.
