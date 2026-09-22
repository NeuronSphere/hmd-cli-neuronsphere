Your first local environment
============================

By the end of this tutorial you will have a running local control plane and
one named environment, know how to inspect them, and be able to stop and
restart your environment without discarding its data.

You need macOS or Linux, including Linux under WSL2, and a running container
engine that your ``docker`` CLI can reach -- Docker Desktop, Colima, OrbStack,
Rancher Desktop or a plain Linux daemon. Native Windows is not supported. The
first start downloads images and creates infrastructure, so allow more time and
disk space than a restart.

Prepare the host
----------------

Check that ``nsctl`` can reach your container engine::

   nsctl doctor

It prints the endpoint it resolved and where that came from, the engine's
version and size, and whether the host names below resolve. It changes nothing,
and exits ``2`` when something needs fixing.

If it cannot reach an engine, start yours and select it with ``docker context
use <name>``; ``docker context ls`` lists them. See
:doc:`../how-to/choose-a-container-engine` for what each engine needs.

The host must resolve these two names to loopback. Add the following line to
``/etc/hosts`` if it is not already present::

   127.0.0.1 neuronsphere neuronsphere-workload

Containers already receive Docker network aliases. The host entry is needed
because URLs returned by local services must also work from your shell and
browser. Startup checks this and reports a missing entry.

Install nsctl
-------------

Choose one installation method. With Homebrew::

   brew install neuronsphere/tap/nsctl

Or use the release installer on macOS or Linux::

   curl -fsSL https://raw.githubusercontent.com/neuronsphere/hmd-cli-neuronsphere/main/install.sh | sh

The installer supports amd64 and arm64, verifies the release checksum, and
installs to ``~/.local/bin`` by default. Add that directory to your shell's
``PATH`` if necessary. You can also download an archive from
`GitHub Releases <https://github.com/neuronsphere/hmd-cli-neuronsphere/releases>`_
or run ``make install`` from a source checkout.

Confirm that your shell finds the binary::

   nsctl version
   nsctl --help

Choose a home and start
-----------------------

Choose a persistent directory for this local platform. Use the same value in
later shells; a different home means a different registry and configuration::

   export HMD_HOME="$HOME/hmd"
   nsctl env start local

On a fresh home, ``env start`` registers the first environment automatically,
starts the control plane, prepares the substrate, and applies the environment's
desired state. ``HMD_HOME`` has no implicit default. You can instead pass
``--home /absolute/path`` on a command.

The default substrate includes Postgres, k3s, and the External Secrets
components needed by workloads. A graph is provisioned when the workload
dependencies require one. Applications such as Airflow, Trino, and your own
services must be declared separately.

Inspect the result
------------------

Run::

   nsctl env list
   nsctl env status local
   nsctl control-plane status
   nsctl repo list --env local

The registry listing should contain ``local``. Status reports the environment's
resources and their state; the repository listing distinguishes declarations
from deployed instances. A successful startup alone does not imply an
application has been installed: this tutorial has created the substrate.

The quickest way to put something real on it is a **stack** -- a published
set of RepoClasses pinned to versions known to work together, free to
install from a public registry with no tenant or token::

   nsctl stack add observability --env local --apply

``stack add`` declares the stack's instances in the environment and
``--apply`` deploys them; ``nsctl stack list --env local`` shows what it
declared. See :doc:`../how-to/use-stacks` for names, versions, profiles and
removal.

Stop and resume
---------------

Stop just the environment, retaining its state::

   nsctl env stop local

The control plane is shared and remains available. Restart with::

   nsctl env start local

To stop the whole platform after stopping its environments::

   nsctl env stop local
   nsctl control-plane stop

Cleanup is a separate operation
-------------------------------

When you no longer need this environment or its data, remove it explicitly::

   nsctl env purge local --yes

This destroys the environment's resources and unregisters it. It cannot be
undone by starting again. Omitting the name from ``env purge`` targets every
environment and the control plane, so keep the name when cleaning up this
tutorial.

If startup fails
----------------

* A missing ``HMD_HOME`` means the export was not set in this shell.
* A hostname error means the host entry is missing or resolves incorrectly.
* An image-pull error needs attention to Docker connectivity and the named
  registry's access requirements; retrying with a different home will not
  resolve it.
* For a deploy failure, rerun ``nsctl env start local -V`` to see the
  underlying command output. Inspect the failing node before purging data.

Next, follow :doc:`create-repository-manifest` to give your own repository
deployable BACON metadata and add it locally. If it already has that metadata,
use :doc:`adopt-repository` for locked dependencies and profiles, or
:doc:`../how-to/add-workloads` to declare one workload at a time. To install
a published set of workloads instead of authoring them, see
:doc:`../how-to/use-stacks`.
