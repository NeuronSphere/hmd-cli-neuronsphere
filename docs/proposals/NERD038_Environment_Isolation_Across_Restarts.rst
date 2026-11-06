.. NERD038 Environment Isolation Across Restarts

NERD038 Environment Isolation Across Restarts
=============================================

.. req:: Starting or restarting one environment shall not stop, empty or rewire another
    :id: HMD_CLI_NEURONSPHERE_NERD038
    :status: draft

    An environment that was running when something else started shall still
    be running afterwards, with its own graph, and its pods shall resolve
    ``global-graph`` to that graph.

    This shall hold across a control-plane stop and start: an environment the
    user had running before the control plane went down shall not be treated
    as one Floci woke by accident.

    When an environment whose BOM provisions its own graph has lost that
    graph's container, ``nsctl env start`` shall recreate it, or stop and say
    so. It shall never point the environment at the control-plane graph
    instead.

    This NERD extends NERD037 to environment graphs. NERD037 left them out of
    scope. Its SPEC004 (required vs optional graph) is the hook this NERD uses.

Problem
-------

Observed on 2026-11-06 with nsctl 1.0.249 and Floci ``floci/floci:2.0.1``. The
environment ``tel-e2e`` (account ``000000000004``) was running a transform
end-to-end suite. Another session was creating the environment ``agent``
(account ``000000000002``).

**1. Every platform container was stopped, then started again.** Between
16:11:06Z and 16:11:51Z every container on the control-plane network stopped.
The order was the one ``controlplane.Stop`` gives:

- the registry server first;
- then the k3s nodes and the control-plane graph;
- then ``hmd_deployment_gui`` and ``hmd_dnsd``;
- then Floci itself, then ``hmd_proxy``.

Floci's shutdown log found every RDS and Neptune container already stopped
(``Status 304``). That is ``stopFlociSpawned`` at work. The Docker VM did not
restart; its uptime was 4.2 days. Everything came back up from 16:14:13Z.
Which command issued the stop is not recorded anywhere nsctl writes.

**2. The wake sweep then stopped tel-e2e.** The ``agent`` start took its
``runningBefore`` snapshot (``environment.go``, around line 139) while
everything was still down. Then:

- Floci restored every persisted EKS cluster. Its log says "Adopted surviving
  k3s container … for EKS cluster ns-tel-e2e-57aa833c", ACTIVE at 16:15:02Z.
- ``sweepWokenEnvironments`` saw tel-e2e's k3s node up now, and down in a
  snapshot taken during the outage. It stopped the node at 16:15:57Z.

The invariant ``floci.StopWoken`` states is "starting one thing does not change
which other environments are running". It held only relative to a snapshot
that was already wrong. tel-e2e had been running at 16:11; it was down at the
snapshot only because of the stop.

**3. tel-e2e's graph container disappeared.** Before the stop, the container
``floci-neptune-global-graph-hmd-inf-neptune-tel-e2e-local-reg1-hmdtr1``
existed. Floci's shutdown listed exactly one Neptune container to stop. When
tel-e2e was next started, about 17:42Z, the container was gone. Floci still
held the cluster record ``000000000004/global-graph-…-tel-e2e-…`` as
``available``.

Nothing in Floci's log removed it, and the only nsctl removal paths (purge,
k3s reconcile, idle foundation Lambdas) do not match it. The cause is not
established; Docker's event buffer no longer reaches back that far.

The graph's data went with the container. ``hmd-img-gremlin-server`` persists
TinkerGraph to ``/data/graph.kryo``, but Floci mounts no volume. The data
survives a stop, but not a removal.

**4. The environment was silently rewired to the control-plane graph.** At
17:42Z ``EnsureNeptuneRunning`` returned ``""`` for the missing container,
the same silent case NERD037 describes for the control plane.
``k3s.coreDNSRecords`` (``internal/k3s/operators.go``, around lines 313-334)
then took its fallback branch. That branch serves environments that bind the
*shared* graph, such as hmd-stack-analytics. It mapped the in-cluster name
``global-graph`` to the control-plane graph (172.19.0.10) and wrote no record
for ``global-graph-tel-e2e`` at all. As a result:

- Trino's ``nsgraph`` catalog, which opens ``global-graph-tel-e2e``, failed
  its startup and crash-looped.
- ms-transform's k3s workers (the ``graphro:`` engine, ``ws://global-graph:8182``)
  read and wrote the **control-plane** graph, while the transform Lambda, whose
  ``SERVICE_CONFIG`` carries the environment's own alias, used tel-e2e's.
  One environment's entities were split across two graphs. The control-plane
  graph already held tel-e2e transform vertices, so this fallback had happened
  before without anyone noticing.

Hand repair: run the gremlin image aliased ``global-graph-tel-e2e`` and add a
``coredns-custom`` record. That brought Trino back. The transform Lambda then
failed at ``addE(hmd_lang_transform.entity_query_has_entity_query_run)``,
because the new graph is empty while Postgres still holds every transform and
entity query.

**5. Floci's proxy ports collide.** After the restart, Floci gave ``agent``'s new
graph proxy port 8182. All three environment cluster records in
``neptune-clusters.json`` now claim 8182. This is the in-memory bookkeeping
bug of NERD037 SPEC006. It is listed here only because it is the same restart.
NERD037 SPEC006 owns the fix.

Specifications
--------------

.. spec:: The wake sweep shall use the environments nsctl started, not a Docker snapshot
    :id: HMD_CLI_NEURONSPHERE_NERD038_SPEC001
    :status: draft

    nsctl shall record, per environment, whether the user last started or
    stopped it. ``env start`` and ``env stop`` already know this, and the
    registry already holds each environment. ``StopWoken`` shall exempt every
    environment whose recorded state is *started*, in addition to its current
    exemptions: the environment being started, and those active in the same
    window. The ``runningBefore`` snapshot remains a second guard, so an
    environment the user started by hand and nsctl never recorded is still
    left alone.

    ``controlplane.Stop`` and ``stopFlociSpawned`` shall not change any
    environment's recorded state. A control-plane stop and start is an outage,
    not a decision about environments.

    **Verification.** Go unit tests in ``internal/floci`` and
    ``internal/environment``, with the ``WakeDocker`` fake:

    * An environment recorded *started*, down in ``before`` and up now, is not
      stopped.
    * An environment recorded *stopped*, down in ``before`` and up now, is
      stopped. This is today's behaviour, kept.
    * ``controlplane.Stop`` leaves every recorded state unchanged.

.. spec:: A control-plane start shall bring back the environments that were running
    :id: HMD_CLI_NEURONSPHERE_NERD038_SPEC002
    :status: draft

    After the control plane starts, nsctl shall start the containers of every
    environment recorded *started* in SPEC001 that Floci did not bring back.
    That means its database, its graph (SPEC003) and its k3s node. It shall
    print one line per environment.

    ``control-plane stop`` shall say which environments it stops with it, and
    that the next start brings them back. Its existing refusal when
    environments are running stays the caller's decision, unchanged.

    **Verification.** A Go unit test with a registry holding two environments,
    one recorded *started* and one *stopped*, after a simulated control-plane
    stop and start. The started one's containers are started; the stopped one's
    are not.

.. spec:: An environment's own graph, when lost, shall be recreated or the start shall stop
    :id: HMD_CLI_NEURONSPHERE_NERD038_SPEC003
    :status: draft

    For an environment whose registry entry records a ``GraphContainer``, or
    whose BOM carries the ``bom.Graph`` entry, the graph is *required* in the
    sense of NERD037 SPEC004. When Floci holds a cluster record for it and no
    container backs it, ``env start`` shall:

    1. say so, naming the identifier and that the graph's data is gone;
    2. recreate it through the same path as NERD037 SPEC001: delete the stale
       record, re-run the graph node, alias the new container as the
       environment's ``GraphContainer``, and restart it onto its address;
    3. report that the graph is empty. It shall name the instances in the
       environment's BOM that declare a graph role (``bom.GraphRoles``) as the
       ones whose graph data must be rebuilt.

    If the recreate fails, the start shall stop with an error naming the
    identifier and the recovery command. It shall never continue to the k3s
    and CoreDNS steps with the graph missing.

    **Verification.** Go unit tests in ``internal/environment`` with the
    ``Containers`` and Neptune fakes:

    * Present-but-stopped is started, and nothing is deleted. This is today's
      behaviour, kept.
    * Absent with an ``available`` record: the record is deleted, the node is
      deployed and the alias is attached, in that order. The output names the
      graph-role instances.
    * A failing deploy returns an error containing the identifier, and no
      CoreDNS update is attempted.

.. spec:: CoreDNS shall never map an environment's graph names to another graph
    :id: HMD_CLI_NEURONSPHERE_NERD038_SPEC004
    :status: draft

    ``k3s.coreDNSRecords`` shall take the shared-graph fallback (``global-graph``
    to the control-plane graph) only for an environment that has no graph of
    its own: no ``GraphContainer`` recorded and no ``bom.Graph`` entry. That is
    the hmd-stack-analytics case the fallback was written for.

    For an environment with its own graph, ``global-graph`` and its
    ``GraphContainer`` alias shall both resolve to that graph's container. If
    that container has no address, nsctl shall write **no** ``global-graph``
    record and warn, naming the environment. A failed lookup is visible; a
    lookup that silently answers from the control plane is not.

    ``nsctl env status`` shall show, for the graph, the container
    ``global-graph`` resolves to inside the cluster. It shall flag a mismatch
    with the environment's own graph.

    **Verification.**

    * Go unit tests on ``coreDNSRecords``:

      - own graph present: both names point at it;
      - own graph absent: neither name is written, and there is a warning;
      - no own graph and a shared alias present: ``global-graph`` points at
        the shared graph. This is today's behaviour, kept.

    * A status test with a cluster whose CoreDNS maps ``global-graph`` to
      another container reports the mismatch.

.. spec:: The isolation shall hold end to end across a control-plane restart
    :id: HMD_CLI_NEURONSPHERE_NERD038_SPEC005
    :status: draft

    A Robot suite under ``test/``, run with ``hmd bender`` against a dedicated
    pair of environments A and B, where A declares a graph:

    1. Start A, and write a marker vertex to A's graph from a pod in A.
    2. ``control-plane stop``, then ``env start B``.
    3. Assert that A's k3s node and graph container are running, and that the
       marker vertex is still there, read through ``global-graph`` from a pod
       in A.
    4. Remove A's graph container, then ``env start A``.
    5. Assert that the start reports the graph recreated and empty, and names
       the graph-role instances.
    6. Assert that ``global-graph`` and ``global-graph-A`` resolve, in A's
       cluster, to A's new container, and not to the control-plane graph.

    Tagged with this spec's id.

Not covered
-----------

- **Who removed tel-e2e's graph container.** The cause is unknown (see
  Problem 3). SPEC003 makes nsctl recover from it whatever the cause.
- **Keeping the graph's data across a removal.** Mounting a named volume at
  ``/data`` for each environment graph would make a recreate lossless. Floci
  spawns the container with no mounts, so this needs either a Floci fork
  patch (a volume for each Neptune backend) or nsctl running the gremlin image
  itself, as NERD037 recommends for the control plane. Decide that together
  with NERD037's "Preserving the graph's data".
- **Rebuilding an empty graph from Postgres.** ms-transform and the Librarian
  persist every entity to Postgres and gremlin. Re-syncing gremlin from Postgres
  is a service-side tool, out of scope for nsctl. SPEC003 only names who needs
  it.
- **Floci's proxy-port bookkeeping.** This is owned by NERD037 SPEC006.
- **hmd-cli-helm's ``graphro:`` fallback** (``ws://global-graph:8182``). It is
  correct once SPEC004 holds, because ``global-graph`` then means the
  environment's own graph inside its cluster.
