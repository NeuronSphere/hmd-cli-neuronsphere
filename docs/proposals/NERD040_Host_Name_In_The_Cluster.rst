.. NERD040 Host Name In The Cluster

NERD040 Host Name In The Cluster
================================

.. req:: A pod shall resolve host.docker.internal to the workstation, and keep resolving it across applies
    :id: HMD_CLI_NEURONSPHERE_NERD040
    :status: proposed

    A pod in a local environment's k3s cluster shall resolve
    ``host.docker.internal`` to the same address the k3s node container
    resolves it to, and so reach a service listening on the workstation.

    The name shall survive every rewrite of the cluster's ``coredns-custom``
    ConfigMap, on ``nsctl env start`` and ``nsctl env apply``. nsctl owns that
    ConfigMap, so nobody should have to maintain a hand-made record against it.

    When the node cannot resolve the name, nsctl shall leave it out and say so.
    It shall never guess an address.

Problem
-------

Observed on 2026-11-19 with nsctl 1.0.249, environment ``memory`` (account
``000000000004``), on a Colima engine.

``hmd-inf-loom`` runs the ClickHouse Loom memory server. Locally its LLM and
embedding provider is Ollama on the workstation, so no API key is needed and
nothing is spent. The Ollama URL lives only in the chart's local profile:
``http://host.docker.internal:11434/v1``.

Inside the cluster that name does not resolve:

- The k3s node container carries ``192.168.5.2 host.docker.internal`` in its
  own ``/etc/hosts``.
- A pod resolves through CoreDNS. CoreDNS forwards to the node's
  ``/etc/resolv.conf``, which is Docker's embedded DNS (``127.0.0.11``). The
  embedded DNS never consults the node's ``/etc/hosts``.
- So the pod gets NXDOMAIN. Loom's embedder and LLM client fail to connect.
  ``http://192.168.5.2:11434`` works from the same pod.

The workaround was a hand-added server block in ``kube-system/coredns-custom``.
It lasted until the next apply.

``CoreDNSRecords`` (``internal/k3s/operators.go``) builds the whole ConfigMap
from scratch. ``EnsureCoreDNSRecords`` runs at cluster provision, and
``refreshSpawnedAliases`` (``internal/environment/apply.go``) reruns it on
every ``env apply``. Both drop any record nsctl did not produce.

During the session, a monitor loop had to re-add the record every five
seconds while each apply ran. Without the loop, Loom would have lost its
provider until someone noticed.

This is the same gap ``CoreDNSRecords`` already closes for the database, the
graph, Floci and the identity provider. The node can reach a name and the pods
cannot; the gap is the name's only symptom. The host is the one name it
doesn't yet cover.

Which engines show it
~~~~~~~~~~~~~~~~~~~~~

The gap was seen on Colima, where ``host.docker.internal`` is only an
``/etc/hosts`` entry in each container.

Docker Desktop answers the name from its own DNS. Pods there may already
resolve it through the forward to ``127.0.0.11``, but this has not been
checked.

The specs below do not depend on which engine is in use. They take the
address from the node, whatever put it there. On an engine that already
resolves the name, the record is redundant but agrees with it.

Not in scope
~~~~~~~~~~~~

- **User-supplied records.** A list of extra records in environment config
  would let a user add any name. That is a larger surface: it would need
  validation, collision rules and a place to report them. The host gateway
  is the one name every engine already defines and every local integration
  with a workstation process needs. A general mechanism can follow if a
  second name appears.
- **What charts address.** The name belongs in a chart's local profile or
  config, never in its cloud values. This NERD gives that local config
  something to point at. It does not change how charts are configured.
- **Exposure of host services.** A record makes a name resolve, nothing
  more. What listens on the workstation, and on which interface, stays the
  user's choice. For example, Ollama binds to the address the user gives it.

.. spec:: The address comes from the k3s node, never from a constant
    :id: HMD_CLI_NEURONSPHERE_NERD040_SPEC001
    :links: HMD_CLI_NEURONSPHERE_NERD040
    :status: proposed

    ``CoreDNSRecords`` reads the k3s node container's ``/etc/hosts`` through
    the existing ``Docker.Exec``. It takes the first IPv4 address listed for
    ``host.docker.internal``.

    The node is the right witness. Pods run on it, and it already has the
    engine's answer: on Colima a ``host-gateway`` entry, on Docker Desktop
    whatever its runtime wrote.

    nsctl hard-codes no address, whether ``192.168.5.2``, ``192.168.65.254``
    or the bridge gateway. The address depends on the engine and the VM,
    and a wrong one fails later and more quietly than a missing one.

.. spec:: The record is part of every rewrite
    :id: HMD_CLI_NEURONSPHERE_NERD040_SPEC002
    :links: HMD_CLI_NEURONSPHERE_NERD040
    :status: proposed

    The host record is appended inside ``CoreDNSRecords``. Provision and
    every ``env apply`` refresh build their ConfigMap through it, so both
    carry the record with no second code path.

    It is rendered like every other record: its own
    ``host.docker.internal:53`` server block, with ``hosts`` and
    ``fallthrough``. It is never a second ``hosts`` plugin inside ``.:53``,
    which crashes CoreDNS.

    The ``CoreDNS:`` progress line lists it with the others, as
    ``host.docker.internal->192.168.5.2``.

.. spec:: An unresolvable host is skipped with a warning, never a failure
    :id: HMD_CLI_NEURONSPHERE_NERD040_SPEC003
    :links: HMD_CLI_NEURONSPHERE_NERD040
    :status: proposed

    If the node has no entry for the name, or the exec fails, the record is
    left out. nsctl warns once, naming the consequence: workloads that reach
    the workstation by ``host.docker.internal`` will not connect.

    The other records still apply. The apply carries on, because most
    environments never address the host.

    A missing node container is already handled upstream, by
    ``K3sContainerName`` returning empty. It adds no new warning.

.. spec:: Tested at the unit and the live level
    :id: HMD_CLI_NEURONSPHERE_NERD040_SPEC004
    :links: HMD_CLI_NEURONSPHERE_NERD040
    :status: proposed

    Unit tests (``operators_test.go``) feed ``CoreDNSRecords`` a fake exec
    result and cover:

    - an ``/etc/hosts`` containing the entry, alongside comments, IPv6 lines
      and aliases, which yields exactly one record with the IPv4 address;
    - an ``/etc/hosts`` without the entry, which yields no host record and
      one warning;
    - an exec error, which behaves like the missing entry;
    - the rendered ConfigMap, which contains the server block.

    The live test (``live_test.go``, gated on ``NSCTL_LIVE`` like the
    others) starts an environment and resolves ``host.docker.internal`` from a throwaway pod.
    It checks the answer equals the node's ``/etc/hosts`` entry. It then
    runs ``env apply`` and resolves again, and the record must still be
    there.
