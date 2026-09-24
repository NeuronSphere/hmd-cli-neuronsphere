BACON configuration
===================

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

How a deployed instance is reached
----------------------------------

A top-level ``access`` list says how a deployed instance of the class is
reached, who signs in, and **where** the credential lives. It never holds one::

   "access": [
     {
       "name": "superset",
       "url": "http://{ingress_host}/",
       "username": "admin",
       "secret": {
         "store": "secrets-manager",
         "key": "{instance_name}-{deployment_id}-{environment}-admin-credentials",
         "property": "password"
       },
       "notes": "Self-registration is off; admin is the only account."
     }
   ]

Author it with ``nsctl repoclass access add``, inspect it with ``access list``
or ``describe``, and resolve it against a deployment with
``nsctl env credentials <env>``.

.. list-table::
   :header-rows: 1

   * - Field
     - Meaning
   * - ``name``
     - What this way in is called; the entry's key, so a second ``add`` with the
       same name replaces it
   * - ``url``
     - Where it is reached, as a template
   * - ``username``
     - The user who signs in, when there is a fixed one
   * - ``notes``
     - One line a reader needs that the other fields do not carry
   * - ``secret.store``
     - ``secrets-manager`` or ``parameter-store``; **required**, never inferred
   * - ``secret.key``
     - The secret's name, as a template
   * - ``secret.property``
     - The field to take out of a JSON secret, e.g. ``password``
   * - ``secret.output``
     - A resource-output key to read the secret's name from, instead of
       ``secret.key``

``url``, ``secret.key`` and ``secret.property`` may use the placeholders
``{instance_name}``, ``{repo_class_name}``, ``{deployment_id}``,
``{environment}`` and ``{ingress_host}``. That is what makes one declaration
work in every environment under any instance name; an unknown placeholder is a
validation error rather than an empty substitution.

``secret.store`` is required because it cannot be inferred.
``hmd_lib_secrets_backend.create_secret()`` writes SSM Parameter Store whatever
its name suggests, while a chart's ``ExternalSecret`` may resolve through the
Secrets Manager ``ClusterSecretStore``. The two are separate namespaces, so a
reader that guesses reports "does not exist" against a secret that is present in
the other one.

Where the producing class publishes the name itself — a ``resources_output``
entry's ``secret_name`` — name that key with ``secret.output`` instead of
restating a template. The published name wins: nsctl re-deriving it would be
nsctl disagreeing with the thing that wrote the secret.

Validation **refuses** a literal ``password``, ``token``, ``api_key`` or
``secret_value`` anywhere in the declaration. A manifest is a file a developer
edits and may commit, so a secret in one is a secret in a git history.

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
