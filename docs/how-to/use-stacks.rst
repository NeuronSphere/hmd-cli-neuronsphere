Use stacks
==========

A **stack** is a published set of RepoClasses -- the three that make up
observability, the four that make a warehouse -- pinned to versions that are
known to work together, installable into a local environment with one
command, and **free**: a stack in a public registry namespace needs no
tenant, no login and no token.

Under the hood a stack is a RepoClass whose repository carries a ``local``
section and a ``neuronsphere.lock`` (see :doc:`artifacts-and-locks`),
published as one OCI artifact holding every build zip the lock pins.
``nsctl`` carries no list of stacks; one exists because a registry serves it
under a name you type.

Add a stack to an environment
-----------------------------

With an environment started (:doc:`manage-environments`)::

   nsctl stack add observability --env dev

A bare name expands to ``ghcr.io/hmdlabs/stacks/<name>`` and the expansion is
printed; a full reference works anywhere::

   nsctl stack add ghcr.io/acme/stacks/warehouse:0.4.0 --env dev

Without a version the newest published one is chosen and printed. The
command fetches the artifact, verifies every zip against its digest, unpacks
them into the artifact cache, and declares the instances in the environment
manifest -- and then stops. **Declaring is not deploying**::

   nsctl env apply dev

or, for one command, ``nsctl stack add observability --env dev --apply``.

Profiles and names
------------------

A stack's companions may be gated by profiles, exactly as a repository's
are under ``env add --from-repo``::

   nsctl stack add observability --profile full
   nsctl stack add observability --all-profiles
   nsctl stack add observability --lean
   nsctl stack add observability --name clickhouse=warehouse-ch

The stack keeps its own bindings in the manifest's ``stacks`` record, so it
can share an environment with a ``--from-repo`` repository or with another
stack.

See what is declared, and remove it
-----------------------------------

::

   nsctl stack list --env dev
   nsctl stack remove observability --env dev

``remove`` undeclares the instances the stack bound -- and only those; an
instance the substrate provides or another declaration also claims is left
alone -- and drops the record. Nothing is torn down until the next
``nsctl env apply``. The artifact cache is kept unless ``--prune-cache``.

Versions, and preparing an offline machine
------------------------------------------

::

   nsctl stack versions observability
   nsctl stack versions observability --spec "~= 0.1"
   nsctl stack versions observability --offline

``stack pull`` is the fetch half of ``stack add``: it fills the cache without
declaring anything, for a laptop that is about to leave the network or a CI
job warming a cache::

   nsctl stack pull observability:0.1.0

Private stacks
--------------

The same commands work against a private namespace with a credential, and
nothing about ``nsctl`` changes between the two. In order of precedence:

* ``HMD_REGISTRY_TOKEN`` (and ``HMD_REGISTRY_USER`` when the registry needs
  a username) in the environment or ``hmd.env``;
* a profile in ``nsctl.toml`` whose ``registry_url`` names the host, after
  ``nsctl login`` -- the login token is presented as the bearer.

A ``404`` from a public registry may mean the name is wrong *or* that the
package is private (``ghcr.io`` packages are private by default); the error
says so.

Publish your own
----------------

A stack repository is an ordinary RepoClass repository with three things: a
``local`` section naming its companions with version specs, a lock generated
from it, and a deploy phase -- which for a stack that carries nothing of its
own is a NERD009 no-op::

   {
     "name": "hmd-stack-observability",
     "deploy": {"commands": [["exec", "true"]]},
     "local": {"version": 1, "default_profiles": [], "repos": [
       {"instance_name": "otel",       "repo_class_name": "hmd-inf-otel-collector", "version_spec": "0.1.188"},
       {"instance_name": "clickhouse", "repo_class_name": "hmd-inf-clickhouse",     "version_spec": "0.3.12",
        "profiles": ["full"]}
     ]}
   }

Generate the lock and push::

   nsctl lock
   nsctl stack push . ghcr.io/acme/stacks/observability --token $GHCR_PAT --update-lock

The tag is ``meta-data/VERSION`` unless the reference names one. Each
companion's zip comes from ``--artifacts <dir>`` (the ``hmd build`` output
layout, ``<class>_<version>_build.zip``) or, without it, from the cloud
Artifact Librarian by the lock's content path -- so the *publisher* is a paid
user, and the consumer is not. ``--update-lock`` writes every zip's digest
back into the repository's lock so the next commit carries them.

On ``ghcr.io`` a newly pushed package is **private by default**; make it
public in the package's settings before telling anyone to ``stack add`` it.
The stack's images must be pullable too: ``ghcr.io/hmdlabs`` is, and a stack
whose images live elsewhere needs its consumers to set
``HMD_LOCAL_IMAGE_PULL_REGISTRIES`` in ``hmd.env``.
