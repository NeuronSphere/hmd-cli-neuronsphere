Control plane and environments
==============================

A local platform has shared services and independently managed environments.
``HMD_HOME`` identifies the platform's configuration and state. A repository
checkout is an input to deployments; it is not itself the platform home.

Shared services
---------------

The control plane supplies the proxy, Floci, GUI, naming service, artifact
librarian, and deployment registry and resolver. It starts before environment
infrastructure and remains available when an individual environment stops.

Floci emulates AWS services. One Floci container serves multiple account IDs:
the control plane uses its own reserved account and each named environment
receives another. Selecting the correct account determines which local
resources an API call sees.

The deployment service runs the ``hmd-ms-deployment-core`` image under the
existing ``hmd-ms-deployment`` service name and route. Core maintains the
registry, resource definitions, dependency resolution, and instance records.
The CLI orders and executes local deployment work. It does not require the
premium ChangeSet and Argo orchestration path to start an environment.

Per-environment infrastructure
------------------------------

Each registered environment has persisted resource names, an account ID,
port allocation, and state directory. With the full substrate it has a
database, k3s cluster, and supporting components such as External Secrets.
Graph infrastructure is requested by workloads that need it.

A smaller substrate can omit Kubernetes or all environment foundation
infrastructure. This changes what dependencies a workload can bind to;
selecting ``none`` does not automatically provide replacements for the
database or cluster.

The main relationships are::

   HMD_HOME
     shared control plane
       proxy and Floci
       naming, artifact librarian, deployment core, GUI
       optional control-plane extensions
     environment: local
       account and recorded resource names
       selected substrate
       declared workload instances
     environment: review
       separate account and recorded resource names
       selected substrate
       declared workload instances

Routing and hostnames
---------------------

The proxy provides the host-facing routes. Services and pods often receive
URLs referring to ``neuronsphere`` or ``neuronsphere-workload``, so those names
need to resolve from both containers and the host. Docker aliases cover
container traffic.

On the host there are three ways in, in the order they are preferred. A user
interface is published on a **port**, which needs no name at all. The two Floci
names are **dialled on loopback by nsctl itself**, which is why a first run
needs no ``/etc/hosts`` entry and why nothing refuses to start without one; the
``Host`` header is left alone, because it is signed under SigV4 and rewriting
the URL would void the signature. What is left -- the identity provider's
issuer, the package registry, control-plane extensions, and the legacy Python
artifact path -- genuinely needs a **resolved name**, and a local wildcard
resolver covers the whole suffix at once where ``/etc/hosts`` could only ever
name what already exists.

An environment route also selects the correct Floci account. This is why
bypassing the expected route can reach the wrong account or return a 404 even
when the target service exists.

Extension lifetime
------------------

An application usually belongs in an environment. A package registry that
should remain available after an environment is purged belongs in the
control-plane extension manifest. Environment deployment uses RepoClass
deployment commands; control-plane extensions contribute Compose services
to the shared project.

This distinction also explains shutdown order: stop or purge environment
resources while the shared services needed to manage them still run, then stop
the control plane.

Isolation limits
----------------

Separate environments share a Docker daemon, proxy, and Floci process.
They are development environments, not independent security boundaries.
The current global control-plane container names also mean that two different
homes cannot run their control planes concurrently on one daemon.

See :doc:`../how-to/manage-environments` for lifecycle commands and
:doc:`../reference/manifests` for the configuration that controls each scope.
