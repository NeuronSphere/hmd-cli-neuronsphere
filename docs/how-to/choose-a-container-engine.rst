Choose a container engine
=========================

``nsctl`` needs a container engine, and it does not care which one. It resolves
the engine exactly as your ``docker`` CLI does, so whatever ``docker ps`` talks
to is what ``nsctl`` talks to.

What an engine has to provide
-----------------------------

Rather than a list of blessed products, four properties. Anything with all four
works:

- The Docker Engine API, reachable at an endpoint your ``docker`` CLI resolves.
- A Linux daemon.
- Published container ports forwarded to the host's loopback address.
- Host paths shared with the daemon, if it runs in a virtual machine.

Docker Desktop, Colima, OrbStack, Rancher Desktop and a plain Linux ``dockerd``
all satisfy them.

How nsctl finds your engine
---------------------------

In the docker CLI's own order:

#. ``DOCKER_HOST``, if set.
#. ``DOCKER_CONTEXT``, if set.
#. The current ``docker context``.
#. The legacy socket, ``unix:///var/run/docker.sock``.

To point ``nsctl`` somewhere else, point ``docker`` there::

   docker context ls
   docker context use colima

``nsctl doctor`` prints what it resolved and where that came from::

   $ nsctl doctor
   docker CLI           ok       found and answering
   engine endpoint      ok       unix:///Users/you/.colima/default/docker.sock (the docker context "colima")
   engine reachable     ok       Alpine Linux v3.20 27.3.1 (linux)
   engine capacity      ok       4 CPUs and 12.0 GiB of memory
   bind mounts          ok       the engine runs in a virtual machine ...
   CLI and API agree    ok       unix:///Users/you/.colima/default/docker.sock
   host names           ok       resolve to loopback

It changes nothing and exits ``2`` when something needs fixing.

An ``ssh://`` endpoint is refused: reaching one needs the docker CLI's own
connection helper, which ``nsctl`` does not embed. Point ``nsctl`` at a local
engine, or set ``DOCKER_HOST`` to a directly reachable endpoint.

Give the engine enough room
---------------------------

The local platform runs Floci, Postgres, a graph and a k3s cluster. Below 4
CPUs and 8 GiB it starts slowly, and services may be OOM-killed with no message
of their own. ``nsctl doctor`` warns when the engine is smaller than that; it
never refuses on size alone.

Colima's defaults are well under it, so size it at creation::

   colima stop
   colima start --cpu 8 --memory 20 --disk 150

Those are the numbers a full ``control-plane start`` plus ``env start`` was
measured on, not a minimum. The disk is the one worth attention: a long-lived
Docker Desktop install has accumulated the images of every platform that
machine ever ran, and a newly created VM holds none of them, so a first start
pulls the lot. Colima's 20 GiB default runs out partway through. Disk can only
be grown after creation, never shrunk.

On Docker Desktop, use *Settings -> Resources*.

Published ports
---------------

The local platform publishes ``80``, ``4566``, ``18080`` and the whole
``19000-19079`` band on the host, and two engines cannot both forward the same
host ports. So only one engine can run the platform at a time: stop the
platform on one before starting it on the other, rather than expecting them to
coexist.

Both properties this needs were measured on Colima 0.10.3 with its default
``ssh`` port forwarder. Privileged ports below 1024 forward to the host, so
``hmd_proxy`` on ``:80`` works. The eighty-port band forwards completely, and
takes roughly five seconds after the container starts before every port in it
accepts a connection -- so a check that runs the instant a container appears
can see a port that is about to work.

Keep bind-mounted paths where the engine can see them
-----------------------------------------------------

On macOS every engine runs a Linux virtual machine, and a bind mount only works
if the path is shared into it. This matters because a source the daemon cannot
see is **created by it as an empty directory** rather than reported as an
error, so the failure surfaces later and somewhere else.

``nsctl`` keeps its own temporary material -- generated deploy scripts, overlay
workspaces, the container-facing kubeconfig -- under ``$HMD_HOME`` for this
reason. What is left to you is ``HMD_HOME`` itself and your repository
checkouts.

The rule that holds for every engine: keep them under your home directory.
Colima mounts ``$HOME`` writable by default -- confirmed on 0.10.3, which
generates a lima ``mounts:`` entry of ``location: "~"`` with ``writable: true``
over virtiofs -- and Docker Desktop shares a configurable set that includes
``/Users``. ``nsctl doctor`` warns, naming the paths, when it finds one outside
your home directory on a VM-backed engine.

To share another path with Colima::

   colima stop
   colima start --mount /opt/hmd:w

Rootless daemons
----------------

A rootless daemon's socket is not at ``/var/run/docker.sock``, and the
containers ``nsctl`` runs that talk to the engine bind-mount that path.
``nsctl doctor`` warns when the engine reports itself rootless -- an extra
``engine socket`` line, absent on the rootful engines that are the ordinary
case -- and deploy nodes that drive the engine themselves will need the socket
path adjusted. Prefer a rootful engine for the local platform.
