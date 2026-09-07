nsctl -- the Go CLI
===================

``nsctl`` runs the local NeuronSphere as a single Go binary whose only host
prerequisite is Docker. It is an alternative front end to the same local
platform ``hmd neuronsphere`` drives, not a replacement for the ``hmd`` CLI:
both read and write the same environment registry, address the same
containers, and can be used against the same ``HMD_HOME``.

``nsctl`` is the recommended way to run the local platform, and the only one
that needs nothing but Docker. It is not a superset of ``hmd neuronsphere``:
see `What it does not do yet`_ before switching a project over. See
:doc:`proposals/NERD002_Go_CLI_Port` for the design and for what each phase
delivers.

Installing
----------

**Homebrew** (macOS):

.. code-block:: shell

   brew install neuronsphere/tap/nsctl

**Install script** (macOS and Linux) -- ``darwin`` and ``linux`` on both
``amd64`` and ``arm64``:

.. code-block:: shell

   curl -fsSL https://raw.githubusercontent.com/HMDLabs/hmd-cli-neuronsphere/main/install.sh | sh

It installs to ``~/.local/bin`` by default; set ``PREFIX`` for anywhere else,
and ``NSCTL_VERSION`` to pin a version rather than take the latest release. It
verifies the download against the release's ``checksums.txt`` and refuses to
install on a mismatch.

**GitHub Releases** -- the same archives, at
https://github.com/HMDLabs/hmd-cli-neuronsphere/releases, if you would rather
unpack one yourself.

**From a working tree**, which is what you want while changing ``nsctl``:

.. code-block:: shell

   make install          # builds and copies to ~/.local/bin
   make install PREFIX=/usr/local/bin

``make uninstall`` removes it.

There is no Windows build. ``nsctl`` drives Docker by shelling out to the CLI,
mounts ``/var/run/docker.sock``, and composes host absolute paths that sibling
containers have to resolve identically; none of that has a Windows equivalent.
Under WSL2 it is an ordinary ``linux/amd64`` binary and the install script
works unchanged.

Which version you are running
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

``nsctl version`` prints two different kinds of version depending on how the
binary got there, and the difference is informative rather than a bug:

.. code-block:: shell

   $ nsctl version
   nsctl 0.5
   hmd-ms-deployment 0.4 (http://localhost/hmd_ms_deployment)

- ``0.5`` -- two components, no patch -- is ``meta-data/VERSION``, the BACON
  MAJOR.MINOR this repository builds under. Only ``make install`` and
  ``make build`` produce it.
- ``0.5.0`` -- three components -- comes from a ``v``-prefixed release tag, and
  means Homebrew, the install script or a downloaded release.

The repository carries both tag spaces on one history: ``0.5.202``-style tags
belong to the BACON build pipeline and release nothing, while ``v0.5.0`` is
what triggers a release. GoReleaser is configured to ignore the former.

The second line appears only when a control plane is answering, and reports
what *it* is running rather than what ``nsctl`` is -- the two version themselves
independently. With no platform up, ``version`` prints one line and returns
immediately.

What it does not do yet
-----------------------

``nsctl`` and ``hmd neuronsphere`` work against the same ``HMD_HOME``, so
nothing here is a one-way door -- reach for the Python CLI for any of it and
carry on:

- **``nsctl env purge`` does not exist.** To delete an environment's containers,
  volumes and state, use ``hmd neuronsphere down --purge``.
- **There is no automated parity harness.** SPEC012 proposed a test suite
  parameterised on the front end under test; it was never built. Parity is
  checked by performing an operation with one front end and verifying it with
  the other, which is thorough but manual -- so a command that has not been
  exercised that way has not been proven equivalent.
- **Plugin-contributed workloads are invisible until imported.** Anything an
  installed plugin package contributed to an environment brought up by ``hmd``
  needs ``nsctl repo import`` once. See `Using it alongside the Python CLI`_.
- **The DAG runner is off by default**, and turning it on costs a one-time
  local image build. See `The DAG runner`_.

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
   * - ``nsctl env attach [name]``
     - Watch a deployment already in flight.
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

The DAG runner
--------------

A deployment can be executed by the CLI or submitted to **nsrunner**, a
control-plane service that runs it instead. Submitting is what makes a deploy
outlive the command that started it: the terminal can be closed, the deployment
carries on, and ``nsctl env attach`` rejoins it from anywhere. A second
``env start`` against an environment already deploying is refused rather than
allowed to race.

It is off by default. To use it:

.. code-block:: shell

   export HMD_LOCAL_NEURONSPHERE_ENABLE_RUNNER=true
   nsctl control-plane start

Nothing else: no registry login and no ``make image``. The runner image is not
published anywhere, so ``nsctl`` builds it on the first start that needs it,
from a copy of its own sources carried inside the binary. That build takes a
minute or two and is then reused -- the image is tagged with the sources'
digest, so it is rebuilt exactly when the sources change and not otherwise.
Paying that cost only when asked is why the runner is opt-in rather than the
default.

``make image`` does the same build from a working tree, which is what you want
while changing the runner itself; ``HMD_NSRUNNER_IMAGE`` then names the tag to
run. That image is also a perfectly good way to run ``nsctl`` in CI --
``docker run --rm <image> env list`` -- since it is the same binary with the
Docker CLI already on its PATH.

``nsctl`` then submits automatically. With no runner answering it executes the
nodes itself, which is what every existing installation keeps doing -- the
check is a three-second probe, so a machine without one does not pay for the
question.

The runner container mounts ``$HMD_HOME`` and ``$HMD_REPO_HOME`` at their own
absolute paths. That is load-bearing rather than tidy: the runner launches
``hmd-img-projectbuilder`` as a *sibling* container through the Docker socket,
so every bind mount it composes is resolved by the host's daemon and not inside
its own filesystem. Making the two path spaces identical is what lets a repo
working tree reach a deploy node unchanged.

How many nodes deploy at once
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

The runner schedules a changeset on its dependency edges rather than walking
the list, so instances that do not depend on one another deploy together. Four
at a time by default -- the same ``parallelism`` the cloud path's Argo Workflow
carries. Set ``HMD_LOCAL_RUNNER_PARALLELISM`` to change it;
``HMD_LOCAL_RUNNER_PARALLELISM=1`` restores a strictly sequential deploy, which
is what to reach for when bisecting a failure that only appears under
concurrency.

Concurrent nodes share one log, so each line is prefixed with the instance that
wrote it. With one node in flight there is nothing to disambiguate and the
prefix is absent.

A failure stops anything that has not started; nodes already running are left
to finish rather than killed part-way through a ``terraform apply``, so a
failed deploy can take as long as its slowest in-flight node to report. What
landed is still recorded, and a retry picks up from there.

The in-process fallback -- used when no runner is answering -- is unaffected
and stays sequential.

Who submits the workflow
~~~~~~~~~~~~~~~~~~~~~~~~

By default ``nsctl`` submits. Setting ``HMD_LOCAL_SERVICE_SUBMIT=1`` inverts
the last step so ``hmd-ms-deployment`` submits instead -- exactly as it submits
to Argo in the cloud -- and ``nsctl`` only watches. That needs the control
plane configured with ``HMD_WORKFLOW_RUNNER_URL``, and is opt-in because it
routes through an async self-invoking Lambda whose behaviour under Floci is not
yet proven.

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
