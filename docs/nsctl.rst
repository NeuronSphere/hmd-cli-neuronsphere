nsctl -- the Go CLI
===================

``nsctl`` runs the local NeuronSphere as a single Go binary whose only host
prerequisite is Docker. It is an alternative front end to the same local
platform ``hmd neuronsphere`` drives, not a replacement for the ``hmd`` CLI:
both read and write the same environment registry, address the same
containers, and can be used against the same ``HMD_HOME``.

It is in development. See :doc:`proposals/NERD002_Go_CLI_Port` for the design
and for what each phase delivers.

Installing
----------

.. code-block:: shell

   make install          # builds and copies to ~/.local/bin
   make install PREFIX=/usr/local/bin

``make uninstall`` removes it. A published binary and a Homebrew tap are
planned but not yet available, so a Go toolchain is needed to build it.

What it ships
-------------

``nsctl`` ships **the control plane and the environment substrate, and nothing
above them**:

- **The control plane**, one per ``HMD_HOME``: the nginx proxy, Floci, the
  Deployment GUI, and the foundation services ``hmd-ms-naming``,
  ``hmd-ms-artifact-lib`` and ``hmd-ms-deployment``.
- **The environment substrate**, per environment: the core instance, its
  Postgres, its k3s cluster and ``hmd-ms-dbaccount``.

A new environment is therefore *empty but usable* -- a cluster and a database
with nothing deployed on them. Everything else -- Airflow, Argo, Trino,
Superset, transform, and your own services -- is a RepoClass you add.

This is the main difference from ``hmd neuronsphere up``, which resolves its
workloads from whichever plugin packages happen to be pip-installed.

Commands
--------

.. list-table::
   :header-rows: 1
   :widths: 40 60

   * - Command
     - What it does
   * - ``nsctl control-plane start|stop|status``
     - The shared control plane. ``stop`` refuses while an environment is
       running unless ``--force``.
   * - ``nsctl env add <name>``
     - Register an environment, allocating its account and port slot.
   * - ``nsctl env start [name]``
     - Start an environment: database, graph, cluster, routes, then apply.
   * - ``nsctl env apply [name]``
     - Reconcile an environment to its manifest without restarting it.
   * - ``nsctl env stop [name]``
     - Stop it, leaving its state in place.
   * - ``nsctl env list`` / ``env status [name]``
     - What is registered, and what is running.
   * - ``nsctl env delete <name>``
     - Unregister it. Refuses while its containers are running.
   * - ``nsctl repo add <class>[@<version>]``
     - Declare a repo instance in the environment's manifest.
   * - ``nsctl repo remove <instance>``
     - Undeclare it. Does not tear down what is deployed.
   * - ``nsctl repo list``
     - What the manifest declares, beside what the graph has deployed.
   * - ``nsctl repo import``
     - Write the environment's deployed instances into its manifest.

``--home`` overrides ``HMD_HOME`` on any command. Exit codes are ``0`` success,
``1`` error, ``2`` invalid usage or refused precondition, ``3`` refused because
something is in use, ``4`` a deploy node failed.

The environment manifest
------------------------

An environment's desired state lives at
``$HMD_HOME/environments/<slug>.yaml``, in the same format
:doc:`environments` documents for the Python CLI:

.. code-block:: yaml

   version: 1
   name: dev2
   repos:
     - instance_name: my-api
       repo_class_name: hmd-ms-myapi
       version: "0.3"
       instance_configuration:
         replicas: 2
       dependencies:
         eks-cluster: eks-cluster
         database-instance: environment-db

The manifest is the source of truth. ``nsctl repo add`` and ``repo remove``
edit this file and nothing else, and ``nsctl env apply`` deploys the result --
so editing it by hand and running ``env apply`` is exactly the same operation.

Two keys are honoured by the Python CLI and ignored by ``nsctl``: ``plugins``
and ``plugin_config`` configure plugin discovery, which ``nsctl`` does not
implement. It preserves both when it rewrites the file and warns that they had
no effect, rather than silently obeying or silently dropping them.

A typical session
-----------------

.. code-block:: shell

   nsctl env add scratch
   nsctl env start scratch              # substrate only: a cluster and a database

   nsctl repo add hmd-ms-myapi \
       --env scratch \
       --depends eks-cluster=eks-cluster \
       --config replicas=2
   nsctl env apply scratch              # deploys it

   nsctl repo list --env scratch        # declared vs deployed
   nsctl env stop scratch

Reconciling
-----------

``env apply`` deploys only what is new or has changed. It compares the desired
definition against two things: the deployment graph, which says what is
deployed, and a snapshot at ``<state_dir>/applied-changeset.json``, which
records what each instance was deployed *from*. An instance that is deployed
and matches its snapshot is left alone.

Every uncertain case errs towards deploying rather than skipping:

- If the deployment graph cannot be read, everything is deployed, because
  nothing is known.
- If there is no snapshot, deployed instances are left alone -- no snapshot
  means no information, not "everything changed".
- Only instances that a run settled, or that were already found current, get a
  recorded digest.

An instance that is deployed but no longer declared is **reported and left
running**. Removing a line from a manifest is not an instruction to destroy
what it deployed; tear it down yourself if that is what you meant.

``--force-full-redeploy`` skips the comparison entirely.

Using it alongside the Python CLI
---------------------------------

Both front ends work against one ``HMD_HOME``. ``nsctl`` stamps the
``com.docker.compose.*`` labels on every container it creates, so
``docker compose ps``, ``hmd neuronsphere down`` and the Python port validator
all still find them.

Two notes on migrating from the Python CLI:

- **Plugin-contributed workloads.** Entries that come from installed
  plugin packages are invisible to ``nsctl`` until they are written into the
  environment's manifest. ``nsctl repo import`` does that: it reads what the
  deployment graph holds for the environment and declares it, after which
  ``env apply`` reports nothing deployed-but-undeclared. Run it once per
  environment you brought up with ``hmd``.

- **A RepoClass may not declare everything a plugin did.** ``nsctl`` reads a
  service's configuration from its ``manifest.json``, so anything declared
  only in a repo's ``src/local/nsplugin.json`` is invisible to it.
  ``hmd-ms-artifact-lib`` is the known instance: its bucket is declared only
  there, so ``nsctl`` bootstraps it, says that it will not serve until the
  bucket is declared in the manifest, and carries on. Everything else in the
  control plane is unaffected.
