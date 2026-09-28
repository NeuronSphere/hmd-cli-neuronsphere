Fix a start that fails
======================

Two failures account for most first runs that do not come up, and they are
related: the second is usually what someone did about the first. A third does
not stop the start at all, which is what makes it hard to recognise.

Start with ``nsctl doctor``. It asks every question a start depends on,
including the three below, and it never stops at the first answer::

   nsctl doctor

An image will not pull
----------------------

A pinned image that the configured registry does not publish is not reported
where you would expect it. Floci resolves these references itself when it
creates an instance, answers with a 404 from the Docker daemon, leaves the
instance ``failed``, and Terraform polls ``Still creating...`` until someone
kills it.

``nsctl doctor`` asks the registry directly, so the answer arrives before
anything is pulled::

   image hmd-postgres-base  failed  ghcr.io/neuronsphere/hmd-postgres-base:0.3.12 does not
                                    exist: the repository publishes 0.1.6, 0.2.11, stable
                                    HMD_LOCAL_NS_CONTAINER_REGISTRY=ghcr.io/neuronsphere,
                                    set in your shell environment. nsctl's own default is
                                    ghcr.io/hmdlabs, which is where these images are built
                                    and published. Unset the variable, or set it to
                                    ghcr.io/hmdlabs.

The setting to change is almost always ``HMD_LOCAL_NS_CONTAINER_REGISTRY``,
and the message says **where it was set**, because that decides how to change
it:

- *your shell environment* — ``unset HMD_LOCAL_NS_CONTAINER_REGISTRY``, and
  remove it from your shell profile. Nothing under ``HMD_HOME`` can clear this
  one.
- a path ending in ``.config/hmd.env`` — edit that file.
- *nsctl's own default* — the variable is not set anywhere, so the registry is
  right and the **tag** is what is wrong. Pin one that exists with
  ``HMD_POSTGRES_BASE_VERSION``, ``HMD_IMG_GREMLIN_SERVER_VERSION`` or
  ``HMD_LOCAL_K3S_WRAPPER_IMAGE``.

A registry that cannot be reached at all is reported as a warning, not a
failure: being offline is not a misconfiguration.

A deploy dies in ``tofu init``
------------------------------

If a deploy fails several hundred lines in with something like::

   Error: Error refreshing state
   operation error S3: GetObject, exceeded maximum number of attempts, 5,
   https response error StatusCode: 500 ... api error InternalError

then Floci is answering but cannot reach its own storage. The usual cause is
that ``$HMD_HOME`` was deleted while the platform was running — see the
warning under :doc:`../modes`. Deleting the home stops nothing: the containers
are named globally and keyed on the home's *path*, so Floci keeps running and
keeps writing into a directory that no longer exists.

``nsctl doctor`` reports this as a storage row, and the next
``nsctl control-plane start`` recreates Floci by itself. The state Floci held
is gone either way, so if the registry under ``$HMD_HOME`` survived, rebuild
the control plane with it::

   nsctl control-plane reset

Nothing in the cluster can reach a Service
------------------------------------------

This one does not fail a start. ``nsctl env start`` succeeds, the node reports
``Ready``, and then:

- an ``ExternalSecret`` never syncs, so whatever waits on the ``Secret`` it
  would have produced waits forever;
- interfaces behind an ``Ingress`` answer ``503``;
- ports exposed through a ``LoadBalancer`` refuse connections;
- anything in a pod that resolves a name fails, because cluster DNS is itself
  reached through a ``ClusterIP``.

All four are one cause: the engine's kernel has no ``br_netfilter``, so bridged
frames bypass netfilter and a reply to a ``ClusterIP`` comes back carrying the
backend pod's own address instead of the address it was asked for. ``nsctl
doctor`` names it::

   bridge netfilter     warning  the kernel behind this engine has no br_netfilter
                                 loaded, so bridged frames bypass netfilter ...
                                 nsctl loads it itself when it builds a cluster.
                                 To do it by hand on Colima:
                                   colima ssh -- sudo modprobe br_netfilter

``nsctl`` loads the module itself when it builds a cluster, so this is usually
already handled. When it could not -- an engine whose virtual machine ships no
module tree, or one that refuses a privileged container -- the cluster image
refuses to start rather than come up broken, and the start fails naming the
same command. See :doc:`choose-a-container-engine`, which also covers making it
survive an engine restart.

An exited container that is not a crash
---------------------------------------

``docker ps -a`` on a healthy platform shows one or more
``floci-hmd_ms_*`` containers as ``Exited``, often with a Python traceback in
their logs. These are emulated Lambdas. Floci starts one to serve a request and
stops it when it goes idle, so an exited one means it did its job, not that it
failed -- and the traceback is the runtime being shut down underneath it.

The containers worth worrying about are the ones a start names: the control
plane's own, and ``floci-eks-*``, which is the cluster.

To start over properly
----------------------

Never ``rm -rf $HMD_HOME``. Use the commands that take the platform down with
it::

   nsctl control-plane stop    # stop everything, keeping its state
   nsctl env purge --yes       # destroy every environment and the control plane

If the home has already been deleted, point ``HMD_HOME`` back at the path it
had before purging. The sweep finds orphaned containers by a hash of that
path, so a different one misses every single one of them::

   export HMD_HOME=/the/path/you/deleted
   nsctl env purge --yes
   docker ps -a --filter name=floci    # should be empty
