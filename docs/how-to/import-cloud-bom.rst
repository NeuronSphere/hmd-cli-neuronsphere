Import a cloud BOM
==================

.. include:: ../_includes/paid-cloud.txt

A bill of materials (BOM) records the instances and versions deployed in a cloud
environment. Import a selection to reproduce that workload locally. The cloud
environment is read as an input; the import writes local declarations and
fetches artifacts.

Prepare the local destination
-----------------------------

This entire workflow requires paid NeuronSphere and access to an existing
tenant. Obtain its deployment and librarian endpoints and account access from
your organisation's NeuronSphere administrator. Configure a tenant profile and sign
in, as described in :doc:`artifacts-and-locks`. Then register and start a local
destination if it does not already exist::

   nsctl env add scratch
   nsctl env start scratch

In the examples below, ``dev`` names the cloud environment and ``scratch``
names the local environment. They are separate arguments, even when you
happen to use the same name for both.

Inspect and select
------------------

List cloud environments and inspect one::

   nsctl bom envs --profile acme
   nsctl bom show dev --profile acme
   nsctl bom show dev --instance ms-transform --profile acme

Replace the profile, environment, and instance with ones present in your tenant.
The output identifies class, version, deployment status, and whether the
artifact is cached. Plain ``show`` queries the deployment service but does
not fetch artifacts from the librarian.

Selection can use repeatable ``--instance`` or ``--class`` flags.
``--all`` selects the whole BOM. Import without an explicit selection is
refused.

Dependencies affect the selection
---------------------------------

Selecting one workload also selects the providers of its required roles.
Optional roles are not followed by default. Resolve missing class metadata
when you need a more complete preview::

   nsctl bom show dev --instance ms-transform --resolve --profile acme

Unlike plain ``show``, ``--resolve`` may fetch artifacts so the CLI can read
the dependency declarations for the exact versions in the BOM.

Use ``--with <role>`` to include an optional role, and ``--exclude <instance>``
to leave a member out. A role supplied by the local substrate is bound locally
instead of importing the cloud infrastructure instance.

Some required roles name a class without requiring a resource type. When no
local provider is selected, the CLI may bind such a role to
``local-neuronsphere`` and print a warning. That placeholder does not implement
the excluded service. If the workload calls it at runtime, the call still
fails. Use ``--no-stub-roles`` to require an actual provider. Resource-typed
roles cannot be satisfied by that placeholder.

Review a selection file
-----------------------

For a large selection, save and edit it::

   nsctl bom show dev --instance ms-transform --resolve \
       --save-selection selection.toml --profile acme

The file contains entries such as::

   [[instance]]
   take = true
   name = "ms-transform"
   repo_class = "hmd-ms-transform"
   version = "0.5.201"
   why = "asked for"

Change ``take`` to include or exclude entries, then inspect the import without
writing declarations or fetching artifacts::

   nsctl bom import dev --selection selection.toml --env scratch \
       --dry-run --profile acme

Required-role rules still apply to the edited selection. Selection files cannot
be combined with ``--instance``, ``--class``, ``--all``, or ``--exclude``.
Unknown keys are rejected instead of silently ignored.

Import, plan, and deploy
------------------------

Perform the import and inspect the local result::

   nsctl bom import dev --selection selection.toml --env scratch --profile acme
   nsctl repo list --env scratch
   nsctl env plan scratch
   nsctl env apply scratch

Imported entries use artifact sources at the cloud versions; an unrelated
checkout does not silently replace them. If fetching fails, inspect the error
and the local manifest before continuing: successful members may have been
declared while failed members are omitted. Retry the missing artifacts before
deploying a partial selection.

``--no-pull`` writes declarations without fetching and prints the pulls to run.
``--apply`` performs deployment after a successful import; it cannot be used
with ``--dry-run`` or ``--no-pull``. For example::

   nsctl bom import dev --instance ms-transform --env scratch \
       --profile acme --apply -V

Use the separate import, plan, and apply sequence when you need to review the
local change first. A cloud BOM is evidence of versions deployed together,
but cloud-specific configuration and runtime assumptions still need review
for local use.

The same BOM can seed a **stack** rather than one machine's environment:
``nsctl stack init <name> --from-bom bom.json --select <instances>`` derives
a stack repository -- manifest, lock and reference BOM -- from the selected
instances and their required dependencies, for CI to publish so that others
install the set with ``nsctl stack add`` and no tenant. See
:doc:`use-stacks`, *Publish your own*.
