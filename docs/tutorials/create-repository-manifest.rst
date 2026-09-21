Create a BACON manifest and add your repository
===============================================

This tutorial starts with a repository that has no NeuronSphere metadata and
ends with that checkout declared in a local environment. It uses the free local
workflow: no paid NeuronSphere tenant, cloud login, or published artifact is
required. Docker may still need to download the public example image.

.. include:: ../_includes/paid-cloud.txt

The example deploy writes a small result file. It proves that nsctl can run
your repository's command in its chosen image; it does not start an application
server. Once that works, replace the example command with your real deployment.

Start in the repository root
----------------------------

Complete :doc:`first-environment` first. Use a scratch repository for this
exercise, or adapt it to your existing checkout. The resulting layout is::

   your-repository/
     meta-data/
       manifest.json
       VERSION
     scripts/
       deploy-local.sh
     neuronsphere.lock      # only needed for the --from-repo workflow

The BACON manifest describes a **class**: what the repository is and how to
deploy it. Adding it to an environment creates an **instance** of that class.
An environment's own YAML manifest is a different file, written under
``$HMD_HOME/environments/``.

Create the class metadata
-------------------------

From the repository root, run::

   nsctl repoclass init acme-local-demo \
       --description "A local repository onboarding example"
   nsctl repoclass build set-mechanism external
   nsctl repoclass deploy set-command exec sh scripts/deploy-local.sh
   nsctl repoclass deploy set-image alpine:3.20

These commands create ``meta-data/manifest.json`` and, if absent,
``meta-data/VERSION`` containing ``0.1``. ``init`` refuses to replace existing
metadata. If the repository already has a manifest, inspect it with
``repoclass describe`` and edit the relevant fields instead. In particular,
``set-command`` replaces the phase's existing commands.

Here is the complete generated manifest, not a fragment:

.. literalinclude:: ../examples/local-repo/meta-data/manifest.json
   :language: json

You may create this file in an editor instead of using the authoring commands.
Also create ``meta-data/VERSION`` with the value ``0.1``. Use JSON for this
tutorial: ``lock`` and ``--from-repo`` currently read that format.

What the fields do
------------------

* ``name`` is the class name used by ``nsctl repo add``. The directory name
  does not have to match when you supply an explicit checkout path.
* ``description`` explains the repository's purpose.
* ``build`` is required metadata. ``mechanism: external`` says this repository
  uses its own build workflow; these authoring commands do not build anything.
* ``deploy.commands`` supplies the command to run. ``exec`` selects nsctl's
  external-tool runner; the remaining array entries are executable and arguments.
* ``deploy.image`` supplies the container with the tools that command needs.
  The example image includes ``sh``; choose your own image for other toolchains.
* ``meta-data/VERSION`` is separate from the manifest schema and records the
  checkout's class version in ``MAJOR.MINOR`` form.

An empty ``build`` object created by ``init`` alone does not tell nsctl how to
deploy your repository. The deploy command and its implementation are necessary.

Implement the deploy command
----------------------------

Create ``scripts/deploy-local.sh`` with this content:

.. literalinclude:: ../examples/local-repo/scripts/deploy-local.sh
   :language: sh

The command runs in ``/workspace``, where nsctl mounts your checkout. Calling
``sh scripts/deploy-local.sh`` means an executable file bit is not required.
The runner supplies the instance name, class name, version, and other context
as environment variables. It also supplies resolved configuration as JSON in
``HMD_INSTANCE_CONFIG``.

For an actual application, change this script to perform your local deploy
and choose an image containing its tools. Make the command safe to run again,
and return a nonzero exit code if deployment fails. This is a one-shot deploy
command: do not replace it with a foreground server that never exits. If you
use shell operators or variable expansion, put them in a script or explicitly
invoke a shell; an ``exec`` argument array is not implicitly shell-expanded.

Validate before adding it
-------------------------

Run::

   nsctl repoclass describe
   nsctl repoclass validate --strict

The sample should report no errors or warnings. A note about ``exec`` being an
nsctl extension is expected: the Python ``hmd deploy`` path does not implement
this command convention. Validation checks the metadata; execution still
depends on the image and script working.

Choose one way to add it
------------------------

**Add to an existing environment.** If ``local`` is already running, execute
these commands from your repository root::

   nsctl repo add acme-local-demo --env local --name local-demo --path "$PWD"
   nsctl repo list --env local
   nsctl env plan local
   nsctl env apply local -V

This writes an instance declaration pointing to the checkout. It does not need
a lock or a ``local`` section because you supplied the instance directly.
The plan should include ``local-demo`` as an addition on its first deployment.

**Create a new environment from the repository.** Generate the lock and
register a new environment instead::

   nsctl lock
   nsctl lock --check
   nsctl env add onboarding --from-repo . --no-pull
   nsctl env start onboarding --substrate none -V

The sample has no dependencies or companions, so its lock has no dependency
pins. The subject itself runs from the checkout. ``--no-pull`` makes explicit
that this example does not fetch paid cloud artifacts. The script needs no
database or Kubernetes cluster, so ``none`` is sufficient. The shared local
control plane still runs.

For repositories that need a database or cluster, choose ``core`` or ``full``
and declare the necessary roles rather than using ``none``.

Check the result and iterate
----------------------------

Read ``target/nsctl-demo.txt`` in the checkout. For the existing-environment
path, it should contain::

   Deployed instance local-demo from acme-local-demo@0.1

The repository-adoption path uses ``acme-local-demo`` as the default instance
name. Also inspect ``nsctl repo list --env local`` or
``nsctl repo list --env onboarding``, as appropriate.

After editing a deploy script, a definition-based plan may remain unchanged.
To deliberately rerun deployment, use
``nsctl env apply <name> --force-full-redeploy``. This redeploys all declared
entries, not only this script. A dedicated scratch environment keeps that
iteration isolated.

Commit the manifest, version file, and deploy script with your repository.
Commit ``neuronsphere.lock`` too if you use repository adoption. Add generated
``target/`` output to your repository's ignore rules as appropriate.

Add real requirements next
--------------------------

This sample intentionally has no dependency roles. For a real workload:

* Define required providers under ``deploy.dependencies`` and bind their roles
  to instances using ``repo add --depends role=instance``.
* For repository adoption, use ``local.dependencies.<role>.bind`` when the
  substrate already supplies that role, and ``local.repos`` for extra testing
  companions. A bound substrate role does not need a published-artifact pin.
* Use ``nsctl lock`` and local build registration for companion artifacts you
  produce yourself. Cloud Artifact Librarian access and cloud BOM import
  require paid NeuronSphere; they are optional to local onboarding.

See :doc:`../reference/bacon` for the dependency and local-section fields,
:doc:`../how-to/add-workloads` for instance bindings, and
:doc:`adopt-repository` for profiles and locked companion versions.
