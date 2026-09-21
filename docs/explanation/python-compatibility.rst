Python CLI compatibility
========================

``nsctl`` and ``hmd neuronsphere`` address the same local platform state.
This lets an existing Python workflow use nsctl's environment operations
without first creating a different home. It does not make the two command
surfaces identical.

What is shared
--------------

Both read the environment registry under ``HMD_HOME`` and understand the
environment manifest format. Persisted container names, account IDs, and state
paths let either front end address the resources created by the other,
including older layouts.

Login credentials use the shared ``.cache/tokens.yaml`` file.
A successful sign-in can therefore be consumed by other HMD tools. Tenant
profile configuration in ``nsctl.toml`` is separate from that shared token.

Workload discovery differs
--------------------------

The Python CLI can derive workloads and configuration from installed plugin
packages. nsctl uses explicit RepoClass declarations. It does not discover
workloads just because a Python package is installed.

For an environment started through Python, ``nsctl repo import --dry-run``
shows which deployed entries can become explicit declarations. Run the import
for each environment that needs this transition, then inspect its manifest
and plan before applying.

The manifest keys ``plugins`` and ``plugin_config`` are preserved by nsctl
but have no effect on its deployment choices. Configuration present only in
a plugin's ``nsplugin.json`` also does not automatically become RepoClass
configuration. Review those values when adopting a plugin-driven workload.

Lifecycle differences
---------------------

``nsctl env purge <name> --yes`` destroys the environment and unregisters it.
The Python ``down --purge --env`` path retains the registration.
Scripts that assume another start can reuse the old registration need to
account for that difference.

The Go CLI detects incompatible PostgreSQL major versions but does not perform
the migration. The Python ``hmd neuronsphere db upgrade`` workflow remains
the migration path; see :doc:`../environments`.

The ``substrate`` choice is an nsctl setting. Do not assume a Python startup
will enforce an nsctl environment's ``none`` or ``core`` selection.

Authoring and execution boundaries
----------------------------------

The RepoClass authoring group can inspect JSON and TOML, but currently writes
JSON only. Repository adoption and locking still read the JSON manifest.
An authoring command accepting a format does not prove every other reader
does.

The ``deploy.commands`` ``exec`` convention is implemented by nsctl's
local runner; the Python ``hmd deploy`` path does not implement that extension.
Native HMD deploy tools and explicit external-tool commands should therefore
be checked in the execution path you intend to use.

Scope of compatibility testing
------------------------------

The repository's parity harness covers core environment lifecycle and registry
behavior. It does not establish equivalence for every verb. The deeper test of
reading an environment fully created by Python is opt-in.

For an existing project, keep using the same ``HMD_HOME``, inspect the registry,
preview importing deployed instances, and review the resulting plan. Test
the particular operation your workflow depends on before substituting command
names in automation. The older Python-specific guides remain at
:doc:`../readme`, :doc:`../modes`, and :doc:`../environments`.
