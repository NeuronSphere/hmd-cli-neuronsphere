.. NERD028 Bridged Traffic on the Local Cluster

NERD028 Bridged Traffic on the Local Cluster
============================================

.. req:: A local cluster shall not report Ready when nothing in it can reach a Service
    :id: HMD_CLI_NEURONSPHERE_NERD028
    :status: partial

    The kernel property the local cluster's Service networking depends on shall
    be satisfied by the platform where it can be, reported where it cannot, and
    refused rather than started around.

    No local cluster shall advertise itself as ready while every ClusterIP in it
    is unreachable.

    .. note::

        ``partial`` as of 2026-09-25. Every specification but SPEC009 is
        implemented and was measured live on a Colima VM that reproduced the
        reported failure from a cold start -- see `The acceptance run`_. SPEC009
        waits on publishing the wrapper image, because a pin to a tag the
        registry does not serve would break every start.

Motivation
----------

A platform started on Colima came up green -- ``nsctl env start`` succeeded, the
node reported ``Ready`` -- and then failed three ways that look unrelated to each
other and to their cause:

- an ``ExternalSecret`` never synced, so the Redis in ``hmd-stack-analytics``
  waited forever for a ``Secret`` that never arrived;
- interfaces behind an ``Ingress`` would have answered ``503``;
- ports exposed through a ``LoadBalancer`` Service refused connections.

One kernel property explains all three. It was fixed by hand, on the engine's
virtual machine, with a command no part of NeuronSphere had ever mentioned::

    colima ssh -- sh -c 'sudo modprobe br_netfilter && sudo sysctl -w \
        net.bridge.bridge-nf-call-iptables=1 net.bridge.bridge-nf-call-ip6tables=1'

The mechanism
~~~~~~~~~~~~~

k3s runs as a privileged container Floci spawns for its EKS emulation. At boot it
tries to load the kernel modules it needs and cannot: the container has no
``/lib/modules`` bind, and Floci exposes no knob to add one. Kernel modules are
host-wide, so ``br_netfilter`` is entirely the engine VM's business.

Without it, bridged frames bypass netfilter and conntrack. Every pod on a
single-node cluster shares the ``cni0`` bridge, so a pod dialling a ClusterIP is
DNAT'd on the way in, and the reply is forwarded straight back at layer 2
carrying the backend pod's own address rather than being reverse-NAT'd to the
ClusterIP. The client drops it as unsolicited.

Cluster DNS is the first casualty, because it is itself a ClusterIP with a pod
behind it. After that, every Service call. The node reports ``Ready`` throughout,
because a kubelet talking to an API server through a routed path is unaffected.

Why the symptoms look unrelated
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

The failure is not uniform, which is what makes it hard to recognise. Traffic
that is *routed* still works; only traffic that is *bridged between two ports of
the same bridge* is lost. So:

- **Ingress survives at the edge.** ``hmd_proxy`` reaches the node's published
  NodePort, which arrives on ``eth0`` -- off the pod bridge, therefore routed --
  and Traefik load-balances across pod *endpoints* rather than a ClusterIP. Both
  hops are fine.
- **The pods behind it do not.** They need cluster DNS, so they fail, and Traefik
  answers ``503`` for want of a healthy backend.
- **LoadBalancer ports fail at the front door.** k3s implements them with
  ``svclb-*`` pods that DNAT to the Service's ClusterIP -- which puts the broken
  hop on the ingress path itself.

Three unrelated-looking bugs, one cause, and nothing anywhere that says
``br_netfilter``.

The evidence this document is built on
--------------------------------------

Measured on a live platform on 2026-09-25, on Docker Desktop unless stated.

- k3s reports its own failure on **every** boot, including healthy ones::

      level=warning msg="Failed to load kernel module br_netfilter with modprobe"
      level=warning msg="Failed to load kernel module iptable_nat with modprobe"

- The container has four volumes and no ``/lib/modules`` bind
  (``docker inspect``), and is ``Privileged=true``.
- Docker Desktop's kernel has the module: ``/proc/sys/net/bridge/bridge-nf-call-iptables``
  reads ``1``, both in the k3s container and in an ordinary unprivileged
  throwaway container. That second reading is what makes the probe in SPEC002
  possible.
- The external-secrets controller reaches Floci by **name**::

      AWS_ENDPOINT_URL=http://neuronsphere:4566

  so it cannot work without cluster DNS.
- k3s's LoadBalancer helper DNATs to a ClusterIP, not to a pod::

      svclb-redis-local-...  DEST_IPS=10.43.241.106   # the Service's ClusterIP

- Floci's EKS configuration has no mount knob. Its full key set is
  ``api-server-base-port``, ``api-server-max-port``, ``data-path``,
  ``default-image``, ``disable-cni``, ``docker-network``, ``ecr-registry-mirror``,
  ``enabled``, ``endpoint-mode``, ``iam-auth-webhook``, ``keep-running-on-shutdown``,
  ``mock``, ``model``, ``provider``.
- The k3s image ships a trimmed CNI set -- ``bridge``, ``host-local``,
  ``loopback``, ``portmap``, ``firewall``, ``bandwidth``, all symlinks to one
  multicall binary. ``ptp``, ``macvlan``, ``ipvlan`` and ``static`` are absent.
  See `Alternatives considered`_.

Scope
-----

In scope: detecting the property, reporting it, refusing to build a cluster
without it, and loading it where the platform is able to.

Out of scope: making Floci bind ``/lib/modules`` into the container it spawns,
which would let k3s's own module loading succeed unaided and make most of this
document unnecessary. It is a code change in Floci rather than configuration,
and it is the right long-term answer. This document works around its absence.

Reference: what already exists
------------------------------

- ``internal/doctor/doctor.go`` -- ``Gate`` and ``Run``, the ``Check``/``Status``
  model, and the existing engine rows.
- ``internal/controlplane/imagecheck.go`` -- ``ImageChecks``/``imageCheck``, the
  split between a wrapper that touches the world and a pure decision that does
  not. The model for SPEC002.
- ``internal/floci/k3s.go`` -- ``k3sFatalLine`` and ``k3sDiagnosis``, which turn a
  known fatal in a dead container's log into the move that fixes it. The
  machinery SPEC006 hooks into.
- ``internal/environment/envrouter.go`` -- reuses ``nginx:stable-alpine``
  precisely so nothing new is pulled.
- ``hmd-img-k3s-floci``'s entrypoint -- three existing guarded patches around
  Floci's defects, which SPEC005 extends by one.

Specifications
--------------

.. spec:: The prerequisite is named, and it belongs to the engine
    :id: HMD_CLI_NEURONSPHERE_NERD028_SPEC001
    :links: HMD_CLI_NEURONSPHERE_NERD028
    :status: implemented

    ``br_netfilter`` shall be documented as a property of the container engine's
    kernel, not of NeuronSphere, and the documentation shall say that it does not
    survive an engine restart.

.. spec:: The kernel is probed on the endpoint the gate proved
    :id: HMD_CLI_NEURONSPHERE_NERD028_SPEC002
    :links: HMD_CLI_NEURONSPHERE_NERD028
    :status: implemented

    The probe shall run against the endpoint ``Gate`` resolved, never against
    whatever the ambient docker context happens to be -- the defect
    :doc:`NERD021_Runtime_Agnostic_Container_Engine` exists to prevent.

    It shall read the sysctl in a **fresh network namespace**, because these
    values are per-namespace and a fresh namespace is what the k3s container
    gets. Reading the host namespace would answer a different question.

.. spec:: The probe pulls nothing, and an absent probe image is no row
    :id: HMD_CLI_NEURONSPHERE_NERD028_SPEC003
    :links: HMD_CLI_NEURONSPHERE_NERD028
    :status: implemented

    The probe shall reuse an image the platform already has. When that image is
    not cached, the check shall emit no row at all rather than pull one.

    A preflight that pulls cannot run offline, and an offline machine is not a
    misconfigured one.

.. spec:: The row warns and does not refuse
    :id: HMD_CLI_NEURONSPHERE_NERD028_SPEC004
    :links: HMD_CLI_NEURONSPHERE_NERD028
    :status: implemented

    The preflight row shall warn, never fail.

    ``Gate``'s only caller is ``controlplane.Start``, which does not create a
    cluster -- k3s is per-environment and separately gated. A failure would refuse
    control-plane starts and k3s-disabled environments over a property they never
    touch, and ``Gate`` has neither a BOM nor a plan with which to tell the
    difference. This is the false-negative class that already cost the
    ``host names`` row its failing status.

.. spec:: The cluster refuses rather than coming up Ready and broken
    :id: HMD_CLI_NEURONSPHERE_NERD028_SPEC005
    :links: HMD_CLI_NEURONSPHERE_NERD028
    :status: implemented

    The wrapper image shall refuse to start ``server`` or ``agent`` when the
    sysctl does not exist, exiting non-zero with a fatal line. Other subcommands
    shall remain usable, so the broken machine can still be inspected.

    It shall test the sysctl's **existence only**, never its value: k3s sets the
    value for its own namespace at boot, so refusing on a ``0`` would refuse a
    cluster that was about to work.

.. spec:: A refusal reads as one diagnosis
    :id: HMD_CLI_NEURONSPHERE_NERD028_SPEC006
    :links: HMD_CLI_NEURONSPHERE_NERD028
    :status: implemented

    The refusal shall be phrased so ``k3sFatalLine`` selects it and
    ``k3sDiagnosis`` can attach the remedy, so the user sees one cause and one
    command rather than a dead container and a log tail.

    The diagnosis shall match on a token unique to the refusal. It shall **not**
    match on ``br_netfilter`` alone, which k3s logs on every healthy boot and
    which would therefore attach this remedy to unrelated failures.

.. spec:: The platform loads the module where it can
    :id: HMD_CLI_NEURONSPHERE_NERD028_SPEC007
    :links: HMD_CLI_NEURONSPHERE_NERD028
    :status: implemented

    Before building a cluster, and only when a cluster is actually being built,
    the platform shall attempt to load the module itself with a privileged
    one-shot container carrying ``/lib/modules``.

    The attempt shall be announced, skippable, and silent when the module is
    already loaded. A failed load shall not fail the start: it falls through to
    SPEC004's warning and SPEC005's refusal.

.. spec:: nsctl doctor may create a container, and says so
    :id: HMD_CLI_NEURONSPHERE_NERD028_SPEC008
    :links: HMD_CLI_NEURONSPHERE_NERD028
    :status: implemented

    ``nsctl doctor`` currently promises to create, start, pull and remove
    nothing. The probe breaks that promise, so the promise shall be amended
    wherever it is made -- the command's help, its generated reference, and the
    engine how-to -- rather than quietly falsified.

.. spec:: The wrapper pin moves in lockstep
    :id: HMD_CLI_NEURONSPHERE_NERD028_SPEC009
    :links: HMD_CLI_NEURONSPHERE_NERD028
    :status: proposed

    The image tag is pinned exactly, in one source of truth with generated and
    documented copies. Publishing a new wrapper shall move all of them together,
    and shall require no volume purge: the guard lives in an image layer, and a
    container whose image no longer matches is already recreated with its
    datastore intact.

.. spec:: The escape hatch is the previous pin
    :id: HMD_CLI_NEURONSPHERE_NERD028_SPEC010
    :links: HMD_CLI_NEURONSPHERE_NERD028
    :status: implemented

    Floci passes no arbitrary environment into the containers it spawns, so an
    environment variable in the entrypoint is reachable only by someone running
    the image by hand. The documented way to start a cluster anyway shall be to
    pin the previous wrapper image, which the platform already honours.

Alternatives considered
-----------------------

Remove the dependency instead of satisfying it
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

``br_netfilter`` is only needed because flannel puts every pod on a shared
``cni0`` bridge. Wiring pods point-to-point instead -- the CNI ``ptp`` plugin,
veth pairs with ``/32`` routes and no bridge -- would make every pod-to-pod hop
*routed*, so netfilter would see it unconditionally and the module would be
irrelevant. It would also be closer to the cloud, where the VPC CNI is
route-based and no such bridge exists.

**Rejected on evidence, 2026-09-25.** k3s bundles a trimmed CNI set and ``ptp``
is not in it (see `The evidence this document is built on`_). ``bridge`` is the
only pod-connecting plugin present, so flannel has nothing route-based to
delegate to.

It could be revived by vendoring the upstream ``ptp`` binary into the wrapper
image, which is a supply-chain decision rather than a configuration change, and
a much larger one than the work this document proposes. Worth reopening if this
class of defect recurs.

Have Floci bind ``/lib/modules``
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

The cleanest fix: the container is already privileged, so with the module tree
visible k3s's own startup would load ``br_netfilter`` and none of this would be
needed. Floci has no configuration knob for container mounts, so this is an
upstream code change and is out of scope here.

Risks
-----

- **The probe cannot distinguish "absent" from "unreadable" without help.** On a
  rootless or user-namespaced daemon a read may fail for permission, which is not
  a missing module. The probe reports three outcomes rather than two so this
  cannot be misread as a red.
- **A kernel with bridge netfilter compiled out** rather than modular reports the
  same absence, and ``modprobe`` will not help it. The remedy is worded for the
  engine where this actually happens rather than as a universal claim.
- **The reasoning is specific to kube-proxy in iptables mode.** A future k3s
  default of ``nftables`` or ``ipvs`` would need this revisited.
- **If Floci ever stops bridge-attaching the k3s container**, this check becomes a
  check on something the work no longer depends on -- which this codebase holds to
  be worse than no check at all.

Open questions
--------------

- **When should the row be promoted to a failure?** Only once ``Gate`` can see
  that a k3s substrate is actually being asked for. That is a different change:
  the preflight today has no environment plan to consult.
- **Should the wrapper refuse when the sysctl exists but reads 0?** Currently no,
  because k3s sets it itself. Untested against a kernel in that state, because
  nobody has produced one.
- *Is a fresh Colima VM red out of the box?* **Answered 2026-09-25: yes.**
  A VM created and started from scratch has no ``br_netfilter`` and no
  ``/proc/sys/net/bridge`` at all, and Docker starting on it does not load the
  module. See `The acceptance run`_.

The acceptance run
------------------

Run on 2026-09-25 against a Colima VM (Ubuntu 24.04.4, kernel
``6.8.0-117-generic``, Docker 29.5.2), beside an untouched Docker Desktop.

**The open question is closed: a freshly created Colima VM is red out of the
box.** Immediately after ``colima start``, before anything else ran::

    $ colima ssh -- sh -c 'lsmod | grep br_netfilter || echo "NOT LOADED"'
    NOT LOADED
    $ colima ssh -- ls /proc/sys/net/bridge/
    ls: cannot access '/proc/sys/net/bridge/': No such file or directory

Docker starting on that VM does not load the module. This was the one link in
the diagnosis that had been inferred rather than observed.

What each specification was measured against
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

- **SPEC002/SPEC004** -- ``nsctl doctor`` against the Colima endpoint reported
  ``bridge netfilter  warning`` with the remedy, and **exited 0**. The same
  warning appeared in the ``nsctl env start`` preflight, which then carried on.
- **SPEC003** -- with ``nginx:stable-alpine`` removed from the engine, the row
  vanished entirely rather than pulling or failing; restoring the image brought
  it back.
- **SPEC005** -- ``docker run hmd-img-k3s-floci:0.3-brnf-dev server`` on the
  unfiltered kernel exited **1** with the fatal line. ``crictl`` under the same
  entrypoint was not refused, so the broken machine stayed inspectable.
- **SPEC006** -- the real dead container was fed to ``floci.VerifyK3sAlive``,
  which produced the quoted fatal line *and* the diagnosis naming
  ``modprobe br_netfilter``, exactly as a user would see it from ``env start``.
- **SPEC007** -- ``floci.EnsureBridgeNetfilter`` returned ``BridgeLoaded`` from
  a genuinely absent start, and ``lsmod`` then showed the module. A second call
  returned ``BridgeAlreadyLoaded`` without running ``modprobe``. In the live
  start it printed::

      loaded br_netfilter on the engine's kernel, without which no Service would answer

**End to end.** From the natural red state, ``nsctl env start local`` warned,
loaded the module itself, brought the cluster up (``1 node(s) Ready``), and
finished ``Ready.`` with exit 0. A pod then resolved a name through the kube-dns
ClusterIP whose CoreDNS pod sits on the same bridge -- the hop that fails
without the module::

    Name:	kubernetes.default.svc.cluster.local
    Address: 10.43.0.1

**The cause was then confirmed by a controlled A/B on that live cluster**, one
pod and one ClusterIP, changing only the sysctl *inside the k3s container's
namespace*::

    bridge-nf-call-iptables=1  ->  ClusterIP 10.43.0.10:53 REACHABLE
    bridge-nf-call-iptables=0  ->  ClusterIP UNREACHABLE
    bridge-nf-call-iptables=1  ->  REACHABLE again

Three things the run taught that the design had only assumed
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

- **The per-namespace nature of the sysctl is load-bearing, and was nearly
  missed.** Setting ``net.bridge.bridge-nf-call-iptables=0`` in the *VM's* host
  namespace changed nothing: the ``cni0`` bridge lives in the k3s container's
  own namespace, which keeps its own value. Only setting it inside that
  container reproduced the failure. This is why SPEC002 requires the probe to
  read a **fresh** namespace rather than the host's -- a host-namespace probe
  would have reported a healthy engine as broken and vice versa.
- **``modprobe -r br_netfilter`` is not a safe way to recreate the red state.**
  It takes the ``bridge`` module with it, destroying ``docker0`` and breaking
  the engine's own networking for every new container
  (``failed to create endpoint ... adding interface to bridge docker0 failed``).
  Recovering needs ``modprobe bridge`` plus a Docker restart. The clean way to
  reach the red state is ``colima restart``, which leaves ``bridge`` loaded by
  dockerd and ``br_netfilter`` absent -- precisely the reported machine's state.
- **CoreDNS churns during a deploy**, so a DNS assertion run too early answers
  ``NXDOMAIN`` or ``connection refused`` for reasons that have nothing to do
  with this defect. Two readings during the run disagreed until the pod was
  waited on with ``kubectl wait --for=condition=ready``. Any future test of
  this must wait for the endpoint, not merely for the node.

Still owed
~~~~~~~~~~

SPEC009. The wrapper guard was verified against a locally built image; the pin
still points at ``0.3.4`` and moves only once ``0.3.5`` is published, because a
pin to a tag the registry does not serve would break every start.
