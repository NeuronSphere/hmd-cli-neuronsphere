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

A second private transport -- pushing a stack to, and adding it from, a
tenant's deployed Artifact Librarian -- is proposed in
:doc:`/proposals/NERD020_Stacks_Via_The_Artifact_Librarian`.

Publish your own
----------------

A stack repository is an ordinary RepoClass repository with three things: a
``local`` section naming its companions, a lock generated from it, and a
deploy phase -- a NERD009 no-op for a stack that carries nothing of its own.
The primary way to make and publish one is **from a running environment,
through CI**.

**Derive it from what you are running.** Pick the instances a consumer should
get; their required dependencies follow::

   nsctl stack init hmd-stack-analytics --from-env local --select superset,trino,airflow --dry-run

The table says what each instance reached becomes: a selected root is a
companion in its own profile; anything else reached is a companion pinned to
its running version; the substrate and the classes ``nsctl`` bundles are bound
roles the consumer's environment provides; an instance another stack declared
becomes an *external* role with that stack suggested (``--include-provided``
bundles it instead); an instance deployed from an explicit working tree has
no published artifact and is refused unless ``--bundle-local`` names it.
Configuration is copied only from the environment manifest's declarations,
and values that look tied to your machine are listed for review. Drop
``--dry-run`` to write the repository: the manifest, ``neuronsphere.lock``,
``meta-data/reference-bom.json`` (what CI re-derives from) and the workflow.

**Or author it by hand**::

   nsctl stack init hmd-stack-observability
   cd hmd-stack-observability
   nsctl repoclass local add hmd-inf-otel-collector --spec "~= 0.1" --name otel
   nsctl repoclass local add hmd-inf-clickhouse --spec "~= 0.3" --name clickhouse --profile full
   nsctl repoclass deploy add-dependency sink --repo-class-name hmd-inf-s3bucket \
       --resource-namespace storage.neuronsphere.io --resource-definition-name bucket --resource-version 0.1.0
   nsctl repoclass local require sink --suggest storage      # another stack provides it
   nsctl lock --resolve                                      # ranges -> newest published

**Commit, and let CI publish.** ``stack init`` wrote
``.github/workflows/stack.yml`` with three jobs. ``verify`` runs on every
pull request: ``nsctl repoclass validate`` (the lock covers every want, every
role is bound, external or pinned) and ``nsctl stack build``, which writes the
artifact as an OCI image layout under ``build/stack`` and keeps it as a
workflow artifact. ``release`` runs on the default branch::

   nsctl stack push ghcr.io/<owner>/stacks/analytics --from build/stack --bump

with ``HMD_REGISTRY_TOKEN: ${{ secrets.GITHUB_TOKEN }}`` -- no PAT, no
tenant. ``--bump`` tags the push with the next patch of the newest version the
registry holds; a published version is never overwritten. ``refresh`` runs
weekly: ``nsctl lock --resolve`` and a re-derivation from the reference BOM,
opening a pull request when either moved. The one manual step is making the
``ghcr.io`` package public after the first release; the workflow's header
says where.

``stack build`` gets each pinned zip from the first source that has it:
``--artifacts <dir>``, the artifact cache (an environment you derived from
deployed from it), the lock entry's ``source`` -- a RepoClass published as an
OCI artifact with ``nsctl artifact push``, which is how a third party's own
classes reach CI with no tenant -- and, with a credential, the cloud Artifact
Librarian. The tier that served each entry is printed, and so is the
licence each layer declares.

Declare what you publish. The manifest's ``license`` names the SPDX
expression of what ``nsctl`` publishes from your tree and, optionally, the
paths it keeps out of every zip it makes from it::

   nsctl repoclass license set MIT
   nsctl repoclass license set Apache-2.0 --exclude src/python --exclude src/docker

Every published layer is annotated ``org.opencontainers.image.licenses``
with the expression, and ``stack build``/``stack push`` print a
``Licences:`` line. Companions that arrive as zips are published as their
authors made them and annotated from their own manifests. A class that
declares nothing is published whole and unannotated; ``nsctl`` infers
nothing and refuses nothing -- the declaration is yours.

The stack's images must be pullable too: ``ghcr.io/hmdlabs`` is, and a stack
whose images live elsewhere needs its consumers to set
``HMD_LOCAL_IMAGE_PULL_REGISTRIES`` in ``hmd.env``.
