Manage environments and substrates
==================================

Use named environments to keep separate development or review workloads under
one control plane. These examples assume ``HMD_HOME`` is set and the host is
prepared as in :doc:`../tutorials/first-environment`.

Register and select an environment
----------------------------------

The first ``env start`` can register an environment automatically. Once any
environment exists, create additional ones explicitly::

   nsctl env add review
   nsctl env start review
   nsctl env list
   nsctl env status review

Names use lowercase letters, digits, and hyphens, begin with a letter or digit,
and have at most 16 characters. Some names are reserved for control-plane
routes. Registration allocates an account and port slot; it does not start
infrastructure. An unknown name passed to ``env start`` after the first
registration is refused.

``env status``'s ``Routes`` block lists what the environment actually routes,
not the ports its slot reserves: a Trino endpoint appears once Trino is
deployed and not before, and the deployed services and Ingress-exposed UIs are
named individually. An environment that reports no route for something you
expected has not deployed it — ``nsctl env plan <name>`` says what is declared.

A command without an explicit name selects ``HMD_LOCAL_ENV``, then the
registry's default, then ``local``. You can make a newly registered environment
the default with ``nsctl env add review --default``. Explicit names are useful
in scripts and when several environments are active.

Choose the substrate at startup
-------------------------------

.. list-table::
   :header-rows: 1

   * - Mode
     - Infrastructure supplied
     - Typical use
   * - ``full`` (default)
     - Database, k3s, operators, and environment foundation services
     - Kubernetes workloads and local chart development
   * - ``core``
     - Database and foundation services, without Kubernetes
     - Workloads that need database infrastructure but no cluster
   * - ``none``
     - Control plane and environment account only
     - Workloads supplying or addressing their own infrastructure

Where applicable, graph infrastructure is demand-driven by workload
dependencies rather than an application installed in every environment.

Set the mode with ``env start``, not ``env add``::

   nsctl env add lightweight
   nsctl env start lightweight --substrate core

The choice is written to the environment manifest. Later starts and applies
reuse it. Raising the mode adds missing infrastructure; lowering it does not
destroy resources already created. Use status to distinguish the declared
mode from resources left from earlier starts.

A workload bound to ``local-neuronsphere`` cannot use ``none``, because that
substrate instance is absent. Choose at least ``core``. A workload needing
``eks-cluster`` requires the Kubernetes substrate or an appropriate alternative.

Change a running environment
----------------------------

After editing its manifest or adding a repository::

   nsctl env plan review
   nsctl env apply review
   nsctl repo list --env review

There is no need to restart the environment for each declaration change.
``nsctl stack add <name> --env review`` and ``stack remove`` change the
declarations the same way -- several instances at a time, from a published
stack (:doc:`use-stacks`) -- and are followed by the same plan and apply.
``env start review --no-deploy`` starts infrastructure without reconciling
workloads. ``env apply review --force-full-redeploy`` forces declared entries
through deployment even when reconciliation considers them current.

See :doc:`../explanation/reconciliation` before using force to diagnose an
unexpectedly empty plan.

Stop, unregister, or destroy
----------------------------

These operations have different effects:

.. list-table::
   :header-rows: 1

   * - Command
     - Effect
   * - ``nsctl env stop review``
     - Stops the environment and retains state for restart.
   * - ``nsctl env delete review --yes``
     - Removes the registry entry; refuses while resources are running.
       This is not resource cleanup.
   * - ``nsctl env purge review --yes``
     - Destroys the environment's resources and state, then unregisters it.
   * - ``nsctl env purge``
     - Targets all environments and the control plane, with confirmation.

Keep the control plane running while purging: resources created through Floci
must be deleted through Floci before it stops. For routine shutdown, stop the
environments, then run ``nsctl control-plane stop``. That command normally
refuses while environments are running.

Two homes and troubleshooting
-----------------------------

Pass ``--home`` to operate on another home without changing your shell::

   nsctl env list --home /absolute/path/to/other-home
   nsctl control-plane status --home /absolute/path/to/other-home

The current control plane uses globally named containers; two homes cannot
run control planes concurrently on one Docker daemon. Stop one before
starting the other.

If startup reports a PostgreSQL major-version mismatch, changing the image
does not migrate its data directory. Use the Python CLI's
``hmd neuronsphere db upgrade`` workflow described in :doc:`../environments`,
or deliberately recreate disposable data. For failed deploy nodes, use
``env apply review -V`` and inspect the reported command output.
