.. NERD007 Concurrent Control Planes

NERD007 Concurrent Control Planes
=================================

.. req:: Run a control plane per HMD_HOME, concurrently
    :id: HMD_CLI_NEURONSPHERE_NERD007
    :status: proposed

    Two or more ``HMD_HOME``\ s on one machine shall be able to run their
    control planes **at the same time**, each serving its own environments,
    without either taking anything from the other.

    Today they cannot. The control plane's five services pin
    ``container_name``, and ``hmd_proxy`` publishes fixed host ports -- so the
    names and the ports are global while everything else is already namespaced
    per home. Starting a second control plane recreates the first's containers
    pointed at the second's home.

Motivation
----------

One machine, several platforms, is not a hypothetical. It is what happens when
a developer keeps a stable ``HMD_HOME`` for the work they depend on and a
throwaway one for an experiment; it is what a `nsctl` contributor needs in order
to test a change without destroying the platform they are testing *with*; and it
is what any parity or integration suite that wants a clean platform needs in
order to run without evicting whatever was already there.

What exists today is a refusal. ``compose.CheckOwnership`` detects that another
home owns the container names and stops, which is right -- the alternative,
recorded in ``NERD002`` SPEC014, is that the second start force-removes the
first's containers and the first is left with no proxy. As of 2026-09-14 the
refusal counts only *running* containers, so two homes can **take turns**:
stopping one releases the names for the other. That closes the worst of it and
none of the actual ask.

It is worth being precise about what is already right, because it is most of
the work. Per ``HMD_HOME`` the following are **already** namespaced by an
``HMD_HOME`` hash and need nothing from this document:

- the Docker network (``neuronsphere_default-<hash>``)
- the compose project (``ns-<hash>``)
- Floci's persistent state (``$HMD_HOME/floci/data``)
- the k3s cluster and its node (``ns-<slug>-<hash>``)
- every environment's state directory, kubeconfig and route fragments

Three things are not.

Scope
-----

**In scope.** The control plane's container names, the host ports ``hmd_proxy``
publishes, the hostname Floci bakes into the URLs it hands back, and the
allocation of emulated AWS account ids.

**Out of scope.**

- *Sharing* anything between platforms. Two control planes are two platforms;
  this proposal makes them independent, not cooperative.
- The Python CLI's own start path beyond the compose file both front ends read.
  The compose file is shared, so a change here is a change there, and that is
  the constraint rather than a second implementation.
- Running two *environments* concurrently. They already do -- that is what the
  port slots are for.

What blocks it
--------------

1. Container names are global
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

``hmd_proxy``, ``floci``, ``hmd_deployment_gui``, ``hmd_nsrunner`` and
``hmd_authd`` are pinned with ``container_name:`` in
``docker-compose.control-plane.yml``. A pinned name is global to the daemon.

This is the easy one, and it is easy because the pattern already exists three
times over in the same file: suffix with the ``HMD_HOME`` hash the network and
project already use. The cost is not the change, it is the reach -- these names
appear in status reporting, in the nginx route fragments, in
``floci.ContainerName``, in ``controlplane.RunnerContainer`` and
``AuthContainer``, and in the Python CLI. They have to stop being constants and
start being derived, in both front ends.

2. Host ports are global, and two of them are load-bearing
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

``hmd_proxy`` publishes ``80``, ``4566``,
``${HMD_LOCAL_TRINO_HOST_PORT:-18080}`` and
``${HMD_LOCAL_ENV_PORT_RANGE:-19000-19079}``. The last two are already
variables. The first two are not, and they are not incidental:

- **4566** is where Floci answers, and ``neuronsphere:4566`` is baked into every
  presigned S3 and API URL Floci hands back. The host resolves ``neuronsphere``
  to loopback through ``/etc/hosts``, which is why those URLs work from the host
  at all.
- **80** carries every control-plane route. ``controlplane.MSDeploymentURL`` is
  the literal ``http://localhost/hmd_ms_deployment``, and the route paths under
  it are how everything addresses the foundation services.

So a second platform needs its own port base *and* every URL derived from that
base rather than written down. That is the substance of this proposal.

3. The hostname is global too
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Moving Floci to a second port does not finish the job, because
``FLOCI_HOSTNAME: neuronsphere`` is what Floci writes into the URLs it returns.
Two platforms both claiming ``neuronsphere`` on loopback differ only by port,
and a presigned URL that names the wrong port reaches the wrong Floci -- which
is worse than failing, because it succeeds against another platform's data.

Each home therefore needs its own hostname, which means its own ``/etc/hosts``
entry. That is the one part of this that cannot be made invisible to the user:
``/etc/hosts`` needs root, and ``nsctl`` deliberately does not.

4. Account ids collide
~~~~~~~~~~~~~~~~~~~~~~

``registry.AllocateAccountID`` allocates from a space starting at
``000000000001``, and ``UsedAccounts`` consults *this registry only*. Two homes
therefore hand their first environment the same account id. Inside one Floci
that is the account, and Floci-spawned container and volume names are derived
from it -- so two platforms would generate colliding names for unrelated
resources. This is invisible while the platforms take turns and becomes a data
hazard the moment they do not.

Design
------

The shape is: **one derived base per HMD_HOME, and everything else computed from
it.** Nothing in this design adds a second mechanism where one exists -- the
``HMD_HOME`` hash is already the namespacing primitive, and this extends it to
the three things it does not yet cover.

.. spec:: A per-home port base, and a URL built from it
    :id: HMD_CLI_NEURONSPHERE_NERD007_SPEC001
    :links: HMD_CLI_NEURONSPHERE_NERD007
    :status: proposed

    The control plane gains a **port base**, persisted in the registry beside
    ``ControlPlane.Network`` and ``ComposeProject``, allocated the way an
    environment's port slot already is: derived from the ``HMD_HOME`` hash so it
    is stable across restarts, with a lowest-free fallback on collision.

    The first home keeps ``80`` and ``4566``, so nothing existing moves. Later
    homes take a band above the environment port range.

    ``controlplane.MSDeploymentURL`` and every sibling constant become functions
    of the registry, not literals. This is the bulk of the work and most of the
    risk: a URL that is still written down somewhere reaches the *other*
    platform, and does so successfully.

    Persisted rather than recomputed, for the reason ``internal/registry``'s
    package doc already gives: a derived value that changes later orphans the
    containers named under the old rule.

.. spec:: A per-home Floci hostname
    :id: HMD_CLI_NEURONSPHERE_NERD007_SPEC002
    :links: HMD_CLI_NEURONSPHERE_NERD007
    :status: proposed

    ``FLOCI_HOSTNAME`` becomes ``neuronsphere-<hash>`` for any home that is not
    the first, and the network alias follows it. ``CheckHostsEntries`` already
    refuses a start when the names do not resolve to loopback and prints the
    exact line to add; it extends to the derived name unchanged.

    The first home keeps the bare ``neuronsphere``, so an existing install needs
    no ``/etc/hosts`` change and no re-signing of anything.

    This is the user-visible cost of the feature and should be stated as such
    rather than engineered around: a second concurrent platform costs one line
    in ``/etc/hosts``, added once.

.. spec:: Account ids partitioned by home
    :id: HMD_CLI_NEURONSPHERE_NERD007_SPEC003
    :links: HMD_CLI_NEURONSPHERE_NERD007
    :status: proposed

    ``AllocateAccountID`` allocates from a range derived from the ``HMD_HOME``
    hash rather than from ``000000000001`` upward, so two homes cannot issue the
    same id.

    Existing registries are not renumbered. An account id is exactly the kind of
    persisted derived value ``internal/registry`` refuses to recompute -- it
    names containers and volumes that already exist -- so the new rule applies
    to allocations made after it lands, and ``UsedAccounts`` continues to
    protect whatever is already recorded.

.. spec:: Namespaced container names
    :id: HMD_CLI_NEURONSPHERE_NERD007_SPEC004
    :links: HMD_CLI_NEURONSPHERE_NERD007
    :status: proposed

    The five pinned ``container_name`` values gain the ``HMD_HOME`` hash
    suffix, as the network and compose project already have.

    The first home keeps the bare names. That is not only for continuity: the
    Python CLI, the parity suite, and every piece of operator muscle memory
    (``docker logs hmd_proxy``) know them, and a rename that applies to
    everything at once is a breaking change for a feature nobody has asked to be
    breaking.

    ``compose.CheckOwnership`` stays. With namespaced names it stops firing in
    the common case, and it remains the guard for the case that will still
    exist: two homes that both believe they are the first.

Verification
------------

The claim is concurrency, so the test has to be concurrency -- not two starts in
sequence. A suite that brings up two homes, asserts each answers its own
foundation services on its own port with its own account ids, deploys something
to one and asserts the other is unchanged, then stops one and asserts the other
survives it.

``NERD002``'s lesson applies directly and is worth restating, because this
document is where it will bite next: *a guard that finds what it needs already
present does not exercise the code that would have created it.* Every defect in
:doc:`NERD002 <NERD002_Go_CLI_Port>`\ 's "A second cold start, from a genuinely
empty HMD_HOME" was a default or a fallback that a working machine never
reaches. The equivalent here is a URL
still written down somewhere -- it will work perfectly on a machine with one
platform, and reach the wrong one on a machine with two.

Alternatives considered
-----------------------

**Leave it at taking turns.** Already implemented and genuinely most of the
value for the common case, which is a developer with one platform who
occasionally wants a clean one. It is only insufficient when both platforms must
be *up* -- comparing behaviour across them, or running a suite against one while
working in the other.

**Give each platform its own Docker daemon** (a second context, or a VM). No
code change, complete isolation, and it works today. Rejected as the design but
worth documenting as the workaround: it costs a second daemon's memory, and the
``/var/run/docker.sock`` that Floci and every projectbuilder mount is the
daemon's own, so the platforms cannot share an image cache -- every pull and
every ``hmd build`` happens twice.

**Drop ``container_name`` entirely** and let compose name containers
``<project>-<service>-1``. Simpler than a suffix, and rejected: the Python CLI
and the operator both address these by name, and ``hmd_proxy`` in particular is
a name that appears in documentation, scripts and habit.
