BACON configuration
===================

.. include:: ../_includes/paid-cloud.txt

BACON metadata describes a RepoClass: its identity, build and deploy commands,
configuration, dependencies, and resource contracts. The repository's
``local`` section adds development companions and local bindings.
An environment manifest instantiates those classes on a particular machine.

For a complete first setup, follow
:doc:`../tutorials/create-repository-manifest`. It includes the full JSON
manifest and deploy script, then shows exactly how to add the checkout to an
environment. The examples below are field references and fragments for
extending that working setup.

Readers and supported formats
-----------------------------

The ``nsctl repoclass`` authoring group selects the first existing file:

1. ``meta-data/manifest.toml``
2. ``meta-data/manifest.json``
3. Repository-root ``neuronsphere.toml``

Describe and validate can read those formats. Authoring commands write JSON
and preserve unmodeled keys. TOML writes are currently refused; edit a TOML
manifest manually when using that reader.

The repository-adoption reader used by ``lock`` and ``--from-repo`` currently
reads ``meta-data/manifest.json``. Support for reading TOML in ``repoclass``
does not imply TOML support across every workflow. For adoption, maintain the
JSON manifest.

Create and inspect metadata
---------------------------

For a repository with no manifest::

   nsctl repoclass init acme-myapi --description "Local API development"
   nsctl repoclass describe
   nsctl repoclass validate

Init creates ``name``, ``description``, and an empty ``build`` section, plus
``meta-data/VERSION`` containing ``0.1`` if no version file exists.
It refuses to replace an existing manifest. Pass ``--path /path/to/repo`` to
operate on a different repository. These authoring commands do not require
a running platform or ``HMD_HOME``.

Validation reports errors, warnings, and notes. Errors produce a failing exit
status. ``--strict`` also fails on warnings; ``--json`` supports tools.
Validation checks metadata shape and deploy rules, not successful execution
of the workload.

Deployment identity and dependencies
------------------------------------

A dependency is keyed by role under ``deploy.dependencies``. This JSON
fragment asks for a required class-based dependency::

   "deploy": {
     "dependencies": {
       "database": {
         "repo_class_name": "hmd-postgres-rds",
         "version_spec": "0.1",
         "required": "true"
       }
     }
   }

This is a fragment, not a complete deployable manifest: a deployment also
needs valid deploy commands and any other metadata its toolchain requires.
BACON uses strings ``"true"`` and ``"false"`` for ``required``; boolean
literals are flagged by validation.

A resource-typed dependency can identify the resource contract it needs
instead of choosing an implementation. The environment's role binding must
then name an instance that produces that type. Produced-resource declarations
live under ``meta-data/resources/``.

Authoring commands
------------------

Use these commands to add configuration and dependencies consistently::

   nsctl repoclass deploy add-dependency database \
       --repo-class-name hmd-postgres-rds --required --version-spec 0.1
   nsctl repoclass deploy set-config replicas 2
   nsctl repoclass validate --strict

Other subgroups manage build, deploy, and test commands and discovery metadata.
For an existing external toolchain, an explicit argv command can be configured
with::

   nsctl repoclass deploy set-command exec ./scripts/deploy-local
   nsctl repoclass deploy set-image acme/ci-tools:1

Use an image that exists and contains the executable and tools needed by your
repository. This metadata configures deployment; the authoring command does
not run the script. The ``exec`` mechanism is an nsctl behavior with a Python
CLI compatibility limitation; see :doc:`../explanation/python-compatibility`.

Local requirements
------------------

The following ``local`` fragment demonstrates companions and dependency
customization::

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
     ],
     "dependencies": {
       "compute": {
         "bind": "local-neuronsphere"
       },
       "otel-collector": {
         "profiles": ["full", "telemetry"]
       },
       "db-credentials": {
         "dependencies": {
           "database-instance": "environment-db"
         },
         "instance_configuration": {
           "db_name": "myapi"
         }
       }
     }
   }

Use actual classes and versions for your installation. The keys in
``local.dependencies`` must refer to roles in ``deploy.dependencies``;
``otel-collector`` in this example must be optional there.

.. list-table::
   :header-rows: 1

   * - Local field
     - Meaning
   * - ``version``
     - Local-section schema version, currently ``1``
   * - ``default_profiles``
     - Profile names activated on initial adoption unless overridden
   * - ``repos``
     - Companions needed for local testing but not actual dependency roles
   * - ``repos[].version_spec``
     - Requested version or range; the lock supplies a concrete version
   * - ``repos[].profiles``
     - Activates the companion when any listed profile is active
   * - ``dependencies.<role>.profiles``
     - Activates an optional dependency role; forbidden on a required role
   * - ``dependencies.<role>.bind``
     - Uses an instance the environment already provides; creates no lock entry
   * - ``dependencies.<role>.dependencies``
     - Role bindings needed by the dependency instance itself
   * - ``dependencies.<role>.instance_configuration``
     - Configuration for that dependency instance

Companions can also carry ``dependencies`` and ``instance_configuration``.
A bound role cannot also configure a new dependency instance, because it
does not create one. Profiles filter optional entries; required dependencies
remain required. An ungated companion is included even with ``--lean``.

The lock
--------

``nsctl lock`` generates a TOML file at the repository root. Its schema
version is separate from each class version. Resolved entries record:

* ``repo_class_name`` and its concrete ``version``;
* the librarian ``content_path``;
* profile membership and dependency roles in ``satisfies``.

The file deliberately does not record local instance names. Those belong to
the environment's ``bindings`` map. Generate locks through the CLI and
review their diffs rather than hand-maintaining content paths.

Exact specifiers, explicit ``--pin`` values, and versions read with
``--from-env`` supply lock versions. A cloud version query is a separate
operation. See :doc:`../how-to/artifacts-and-locks` for choosing, checking,
and fetching pins.
