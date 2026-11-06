.. NERD037 Control Plane Graph Recovery

NERD037 Control Plane Graph Recovery
====================================

.. req:: A bootstrapped control plane shall not start, or report healthy, without its graph
    :id: HMD_CLI_NEURONSPHERE_NERD037
    :status: proposed

    Once the control plane is bootstrapped its Neptune graph
    (``control-plane-graph``, aliased ``global-graph``) is part of it, the
    same as its database. ``nsctl`` shall treat a missing graph container on
    a bootstrapped control plane as a failure to repair, not as a normal
    state to skip.

    A warm start shall leave the graph answering on ``global-graph:8182``, or
    stop and name the command that recovers it. When the graph had to be
    recreated empty, ``nsctl`` shall refill the Artifact Librarian catalog it
    held, or say exactly how to.

    ``nsctl control-plane status`` shall report the graph and the Artifact
    Librarian, and shall not call the control plane healthy while either
    fails.

    Environments keep their current meaning: a graph is provisioned there only
    when something in the BOM asks for one, and its absence stays normal.

Problem
-------

``nsctl`` gives the shared control plane its own Neptune graph: instance
``control-plane-graph`` of ``hmd-inf-neptune``, deployment id ``cp``, aliased
``global-graph`` on the control-plane network
(``internal/controlplane/bootstrap.go``: ``CPGraphInstance``, ``CPGraphHost``,
``CPGraphIdentifier``). The Artifact Librarian (``hmd-ms-artifact-lib``) keeps
its catalog in it.

Observed on 2026-11-05:

- Floci's cluster record
  ``control-plane-graph-hmd-inf-neptune-cp-local-reg1-hmdtr1`` reported
  ``available``.
- Its backing Docker container did not exist -- not stopped, absent -- and no
  volume survived. Floci had been restarted about 28 hours earlier.
- Every request to ``hmd-ms-artifact-lib`` answered 500
  ``Cannot connect to host global-graph:8182``. That blocked every Librarian
  setup (``get_item_types`` and the artifact pull behind it) and every
  transform-config pull, in every environment on this control plane.
- ``nsctl control-plane status`` reported the control plane healthy. It does
  not list the graph or the Artifact Librarian at all; its containers are
  proxy, Floci, the optional deployment GUI and the database.

The warm start in ``internal/controlplane/controlplane.go`` (around lines
680-700) calls ``floci.EnsureNeptuneRunning`` for the graph.
``EnsureNeptuneRunning`` (``internal/floci/rds.go``, around lines 136-185)
starts a *stopped* container and re-aliases it, but returns ``""`` with no
error when there is *no* container, because for an environment an absent graph
is the normal case. The warm start's ``switch`` has no branch for ``""``, so on
the control plane a lost graph passes with no message at all.

Re-running the deploy does not fix it either. Terraform's refresh reconciles
the *record*: an ``available`` cluster is "no changes", so the node that would
create the container never does. ``ReconcileRDSInstance`` is the same repair on
the database side, but it deliberately touches only a ``failed`` record.

Recreating the cluster through Floci then failed as well, for a second reason.
After the stale record was deleted, ``CreateDBCluster`` answered ``Failed to
start Gremlin proxy for cluster control-plane-graph-… on port 8182`` -- also
when asked for port 8183. Floci (``floci/floci:2.0.1``, the version
``docker-compose.control-plane.yml`` pins by default) keeps the set of Neptune
proxy ports it has handed out in memory only (``NeptuneService.usedPorts``):

- after a restart the set starts empty, although the clusters reloaded from
  ``neptune-clusters.json`` still hold their ports. So tel-e2e's graph, created
  after the restart, was given 8182 while the control-plane record still
  claimed it;
- deleting the control-plane record then *released* 8182, although tel-e2e's
  live proxy held it, and the next create was handed 8182 again and could not
  bind it;
- 2.0.1 ignores the requested ``Port``. The fork's main honours it, but has
  the same in-memory bookkeeping.

So while any environment's graph is live, SPEC001's recreate cannot succeed
through this Floci. Clients do not need the proxy at all: they reach the graph
by its alias, on the container's own port 8182. On 2026-11-05 the control
plane was recovered by running ``hmd-img-gremlin-server`` directly, aliased
``global-graph``, with no Floci record.

The graph's data goes with the container. Recreating it gives an empty
catalog, and the Artifact Librarian then answers 200 with nothing in it -- a
quieter failure than the 500, and one ``status`` must not read as health.

Specifications
--------------

.. spec:: A warm start shall recreate a lost control-plane graph
    :id: HMD_CLI_NEURONSPHERE_NERD037_SPEC001
    :status: proposed

    On a warm start of a bootstrapped control plane, when Floci holds a
    cluster record for ``CPGraphIdentifier`` and no container backs it,
    ``nsctl`` shall:

    1. say so, naming the identifier and that the graph's data is gone;
    2. delete the Floci cluster record (and its instance), so the deploy sees
       a missing resource rather than an ``available`` one;
    3. re-run the bootstrap's graph node (``deployNode`` for
       ``CPGraphInstance`` / ``CPGraphRepoClass`` with ``graphConfig()``);
    4. alias the new container as ``global-graph`` and restart it onto its new
       address, through the same ``EnsureNeptuneRunning`` path the bootstrap
       uses;
    5. continue only when the container is running; otherwise stop with an
       error that names the identifier and the recovery command
       (``nsctl control-plane reset`` or the narrowest equivalent that exists
       when this is implemented).

    It shall never continue past this step silently.

    **Recreate rather than fail.** Recreating loses nothing: the data was
    already gone with the container, so deleting the record only removes a
    lie. Failing would hand the user a manual sequence -- delete a Floci record
    by API, re-run one DAG node, alias, restart -- that ``nsctl`` already knows
    and the bootstrap already performs. The rule that keeps
    ``ReconcileRDSInstance`` away from an ``available`` record (it may be a
    transient Floci state) does not carry over: Floci never restarts a Neptune
    container on its own (see ``EnsureNeptuneRunning``), so an absent one will
    not reappear. The failure path remains for when the recreate itself fails.

    **Verification.** A Go unit test in ``internal/controlplane`` with the
    ``Containers`` fake reporting no graph container, a fake RDS/Neptune API
    reporting the cluster ``available``, and a recording deployer, shall
    assert, in order: the cluster record is deleted, the graph node is
    deployed, ``EnsureNetworkAlias`` is called with ``global-graph`` on the
    control-plane network, and the container is restarted when the alias
    reconnected it. A second test, with the deployer returning an error, shall
    assert the warm start returns an error whose text contains the identifier
    and the recovery command. A third shall assert a stopped (present)
    container is started and nothing is deleted or deployed.

.. spec:: An empty Artifact Librarian after a recreate shall be refilled or reported
    :id: HMD_CLI_NEURONSPHERE_NERD037_SPEC002
    :status: proposed

    After SPEC001 recreates the graph, and once ``hmd-ms-artifact-lib``
    answers, ``nsctl`` shall read its catalog. When it is empty, ``nsctl``
    shall re-register the artifacts it can account for from what it already
    holds -- the unpacked copies under
    ``$HMD_HOME/.cache/neuronsphere/artifacts`` that ``nsctl artifact pull``
    and ``register`` left behind -- without reaching a cloud librarian.

    Whatever it cannot re-register locally it shall list, one
    ``nsctl artifact pull <repo-class>@<version>`` line per artifact, and the
    start shall end with a warning that the control plane is degraded until
    they are run. It shall not report the control plane healthy while the
    catalog is empty and the cache shows it should not be.

    Re-registering from the cache keeps NERD005's rule that only
    ``nsctl artifact pull`` reaches the network on the user's behalf.

    **Verification.** A Go unit test with a fake local librarian returning an
    empty catalog and a temporary cache holding two unpacked artifacts shall
    assert both are registered and no cloud client is constructed. A second,
    with one cached artifact lacking what ``register`` needs, shall assert the
    output contains the exact ``nsctl artifact pull`` line for it and the
    result is marked degraded.

.. spec:: ``control-plane status`` shall report the graph and the Artifact Librarian
    :id: HMD_CLI_NEURONSPHERE_NERD037_SPEC003
    :status: proposed

    ``nsctl control-plane status`` (and its JSON form) shall list the
    control-plane graph container alongside the database -- role ``graph``,
    state ``running``, ``stopped`` or ``absent`` -- looked up by
    ``CPGraphIdentifier`` in the control-plane account.

    It shall probe the Artifact Librarian with
    ``/hmd_ms_artifact_lib/apiop/service_status`` and a cheap catalog read
    (one ``get_item_types``-style call), because ``service_status`` alone can
    answer while the graph behind it does not.

    The control plane shall be reported **degraded**, with the failing part
    named, when it is bootstrapped and the graph is not running, or the
    Artifact Librarian probe fails, or the catalog is empty while the
    artifact cache is not. ``status`` remains best-effort: a failed probe is
    reported, never a failed command.

    **Verification.** Go unit tests in ``internal/status`` with the fake
    ``Docker`` and a fake ``Prober``: (a) a bootstrapped control plane with no
    graph container lists ``graph absent`` and is degraded; (b) a stopped
    container lists ``stopped`` and is degraded; (c) a running graph with the
    catalog read returning 500 is degraded and names the Artifact Librarian;
    (d) running graph and a non-empty catalog is healthy. A ``cmd`` test
    asserts the rendered text shows the ``graph`` row and the degraded reason.

.. spec:: ``EnsureNeptuneRunning`` shall distinguish a required graph from an optional one
    :id: HMD_CLI_NEURONSPHERE_NERD037_SPEC004
    :status: proposed

    ``floci.EnsureNeptuneRunning`` shall take whether the graph is required
    (a ``required`` flag or an options struct). When required and no
    container exists, it shall return a typed error (for example
    ``ErrGraphContainerMissing``) carrying the identifier, which SPEC001
    acts on. When not required it shall keep returning ``""`` with no error.

    The control plane's warm start and bootstrap shall pass required;
    environment start (``cmd/env.go``) shall pass not required. No caller
    shall be left with a ``""`` result it does not handle.

    **Verification.** Go unit tests in ``internal/floci`` with the
    ``fakeContainers`` fake: absent and required returns
    ``ErrGraphContainerMissing`` (checked with ``errors.Is``) naming the
    identifier; absent and not required returns ``""`` and ``nil``; present and
    stopped is started in both modes; the existing restart-on-realias tests
    pass unchanged.

.. spec:: A lost graph shall be recovered end to end
    :id: HMD_CLI_NEURONSPHERE_NERD037_SPEC005
    :status: proposed

    An integration test shall reproduce the 2026-11-05 failure on a
    throwaway control plane and show the recovery.

    **Verification.** In a fresh, temporary ``HMD_HOME`` (never the user's):

    1. bootstrap the control plane and fill the Artifact Librarian with at
       least one artifact (``nsctl artifact pull`` or ``register``);
    2. ``docker rm -f`` the control-plane graph container, leaving Floci's
       record ``available``;
    3. assert ``nsctl control-plane status`` reports the graph ``absent`` and
       the control plane degraded;
    4. run the warm start (``nsctl env start`` or ``nsctl control-plane
       start``);
    5. assert a Gremlin request to ``global-graph:8182`` from a container on
       the control-plane network succeeds;
    6. assert ``status`` reports healthy only once the catalog again lists the
       artifact from step 1 -- and, with the cache emptied before step 4,
       that it stays degraded and prints the ``nsctl artifact pull`` line.

    The test lives with nsctl's other Docker-backed tests behind their build
    tag, and runs as a ``hmd bender`` suite where the repo has one.

.. spec:: Recreating the control-plane graph shall not depend on Floci's proxy-port bookkeeping
    :id: HMD_CLI_NEURONSPHERE_NERD037_SPEC006
    :status: proposed

    The recreate in SPEC001 shall succeed while environments' graphs are
    running. Either of these satisfies it; the first is preferred:

    1. **A Floci that keeps its port bookkeeping.** The Floci that
       ``docker-compose.control-plane.yml`` pins shall (a) rebuild its used
       Neptune proxy ports from the clusters it reloads at start, (b) release
       a port on delete only if no other live cluster holds it, and (c) honour
       a requested ``Port`` inside the proxy range. This is a patch to our
       Floci fork (``feature/ns-<ver>``, recorded in ``PATCHES.md``) with an
       upstream issue first, per the fork's contribution rules. nsctl's
       default Floci image then moves to the fork's release that carries it.
    2. **Until then, a clear failure.** When ``CreateDBCluster`` for the
       control-plane graph fails with Floci's proxy-start error, nsctl shall
       stop with a message naming the cause (another live Neptune proxy holds
       the port, a known Floci limit) and the workaround: run the graph image
       with the ``global-graph`` alias. It shall not retry in a loop, or delete
       or touch any environment's graph to free the port.

    **Verification.**

    * Go unit test: a fake Neptune API returning the proxy-start error yields
      an error containing the cluster identifier, ``global-graph`` and the
      workaround, and no environment graph is touched.
    * For option 1, a Floci test in the fork: restart with two persisted
      clusters, then create a third. It gets a free port. Deleting one does
      not free a port the other still holds.
    * SPEC005's end-to-end run repeats with an environment graph live while
      the control-plane graph is recreated.

Not covered
-----------

- **Why the container disappeared.** Open question, not confirmed: Floci's
  restart removing (rather than stopping) the containers it spawned, or a
  ``docker system prune`` / ``container prune`` run while the graph was
  stopped. The answer decides whether a Floci patch (our fork,
  ``feature/ns-<ver>``) or a prune-safe label belongs in a follow-up; this
  NERD makes nsctl recover from either.
- **Preserving the graph's data.** A volume or snapshot for the
  control-plane graph would make a recreate lossless. That is a change to
  ``hmd-inf-neptune``'s local deploy and Floci's Neptune backend, not to
  ``nsctl``.
- **Environment graphs.** An environment whose BOM declares a graph and loses
  its container has the same problem; SPEC004 leaves room for it, but
  deciding when an environment's graph is required is out of scope here.
- **The database.** ``ReconcileRDSInstance`` and ``EnsureRDSRunning`` already
  cover the control-plane database's equivalent cases.
