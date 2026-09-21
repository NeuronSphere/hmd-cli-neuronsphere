Adopt an existing repository
============================

.. include:: ../_includes/paid-cloud.txt

This tutorial turns a repository's local requirements into a named environment.
The repository under test runs from your checkout; companion repositories run
at versions recorded in a lock file.

Start with a working installation from :doc:`first-environment` and a repository
containing ``meta-data/manifest.json``, ``meta-data/VERSION``, and a deploy
definition that works locally. A repository with no NeuronSphere metadata
first needs :doc:`create-repository-manifest`, which supplies the complete
manifest, deploy script, and validation steps. That tutorial also covers adding
a single checkout directly to an existing environment without a lock.

Inspect the repository
----------------------

From the repository root, inspect and validate its metadata::

   nsctl repoclass describe
   nsctl repoclass validate

Its ``deploy.dependencies`` describes actual dependency roles.
A ``local`` section can add companion workloads, bind roles to substrate
instances, and group optional workloads into profiles. For example, this
fragment adds one always-present companion and one optional companion::

   "local": {
     "version": 1,
     "default_profiles": ["transforms"],
     "repos": [
       {
         "instance_name": "cache",
         "repo_class_name": "hmd-inf-redis",
         "version_spec": "0.2.1"
       },
       {
         "instance_name": "ms-transform",
         "repo_class_name": "hmd-ms-transform",
         "version_spec": "~= 0.5",
         "profiles": ["transforms", "full"]
       }
     ]
   }

These are illustrative class names and versions. Use classes published by your
librarian and supply the dependencies and configuration those classes require.
The full field rules are in :doc:`../reference/bacon`.

Create or check the lock
------------------------

A fresh clone with a checked-in lock should first run::

   nsctl lock --check

If the repository has no lock, generate it. Exact versions can be pinned
directly; ranges need an explicit choice or a known running environment::

   nsctl lock --pin hmd-ms-transform@0.5.201

The version above is an example. To capture versions from a compatible local
environment you already run, use::

   nsctl lock --from-env local

Resolve any reported unpinned classes, rerun ``nsctl lock --check``, and review
``neuronsphere.lock``. Commit it with the repository metadata. The lock covers
all profiles, even ones you will not activate in this session.

``env add --from-repo`` reads the lock; it does not create one. Checking the lock
checks coverage, not whether artifacts are still published or reachable.

Register and prepare the environment
------------------------------------

Make sure the local control plane is available before fetching artifacts::

   export HMD_HOME="$HOME/hmd"
   nsctl control-plane start
   nsctl env add dev --from-repo .

The add command registers ``dev``, writes its environment manifest, and fetches
missing artifacts into the local librarian. Fetching from the cloud requires
paid NeuronSphere and tenant access; configure its endpoint and sign in as
described in :doc:`../how-to/artifacts-and-locks`. For local-only adoption,
register your own companion builds first and pass ``--no-pull`` to ``env add``.
A repository without companions needs no cloud artifacts.

Registration does not start the environment. The running control plane is
enough to request a plan of the substrate and workloads before first startup.
Do not assume ``env start --no-deploy`` creates a complete fresh substrate:
some resources, including a new cluster, are created by the deploy it skips.

Review and apply
----------------

Inspect the declaration and preview deployment::

   nsctl repo list --env dev
   nsctl env plan dev
   nsctl env plan dev --output md > plan.md

Review additions, changes, validation errors, and candidate warnings. Planning
requires the local deployment service and may register catalog metadata, but
it does not run deploy nodes. It does not produce an executable saved plan.

Start the environment to create its infrastructure and apply the declaration::

   nsctl env start dev
   nsctl env status dev
   nsctl repo list --env dev
   nsctl env plan dev

For subsequent declaration edits on the running environment, use
``nsctl env apply dev`` without restarting it.

When all entries are deployed and match their recorded configuration, the next
plan should have no additions or changes. It may still report previously
deployed instances that are no longer declared.

Change the active profiles
--------------------------

To reread the checkout's manifest and lock, pass ``--from-repo`` again::

   nsctl env apply dev --from-repo . --profile full
   nsctl env apply dev --from-repo . --lean

These commands apply immediately; they are not previews. A bare ``env plan``
reads the existing environment manifest, not a proposed profile selection.
For a review before deployment, edit the environment declaration and plan it,
or evaluate a new selection in a separate environment.

``--lean`` activates no optional profiles. Previously declared profile
instances remain declared unless you also use ``--prune``. Pruning removes
declarations; it does not destroy the running instances.

Later applies do not fetch missing cloud artifacts automatically. If the lock
now names an uncached version, fetch it explicitly or use
``env apply dev --from-repo . --pull``.

Keep the portable and local files distinct
------------------------------------------

Commit the repository's manifest and ``neuronsphere.lock``. The generated
``$HMD_HOME/environments/dev.yaml`` contains local instance names, bindings,
profiles, and checkout paths. It belongs to this machine's environment.

To name a dependency differently when registering another environment::

   nsctl env add review --from-repo . --name neptune-db=review-graph

Replace the role in this example with one declared by your repository.
Existing environment bindings are reused on subsequent applies. See
:doc:`../explanation/repository-adoption` for the naming and profile model.

Stop with ``nsctl env stop dev`` when finished. Use the explicitly destructive
``nsctl env purge dev --yes`` only when its data is no longer needed.
