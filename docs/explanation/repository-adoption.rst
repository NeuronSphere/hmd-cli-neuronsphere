Repository adoption
===================

Repository adoption connects portable development requirements to a particular
local environment. It keeps decisions about which versions to use separate
from decisions about where they run and what they are named.

Three layers of intent
----------------------

.. list-table::
   :header-rows: 1

   * - Layer
     - Question answered
     - File
   * - Repository declaration
     - What dependency roles and testing companions does this project need?
     - ``meta-data/manifest.json``
   * - Resolved versions
     - Which concrete versions should everyone use?
     - ``neuronsphere.lock``
   * - Local environment
     - What instances, paths, profiles, and bindings does this machine use?
     - ``$HMD_HOME/environments/<slug>.yaml``

The first two travel with the checkout. The third is generated or edited for
the local platform. Copying a local manifest with absolute checkout paths is
not equivalent to sharing a lock.

Dependencies and companions
---------------------------

A dependency role belongs to the repository's actual deploy contract. A local
companion makes a test meaningful without necessarily being a dependency of
the subject. For example, a runtime that calls your API may be needed in a
development fixture even though your API does not depend on that runtime.

``local.repos`` describes companions. ``local.dependencies`` customizes
existing dependency roles: it can gate optional roles, provide configuration
for a dependency instance, or bind to something the substrate already supplies.

A binding such as ``compute: {bind: local-neuronsphere}`` creates no new
instance and requires no pin for that provider. It asserts that the
environment supplies it, so choosing a substrate that omits it makes the
binding invalid.

Profiles filter a fixed set of versions
---------------------------------------

A companion without profile gates is always included. A gated companion is
included when any of its profiles is active. Optional dependency roles can
be gated in the same way; required roles cannot.

``--lean`` activates no optional profiles. It does not remove required roles
or unconditional companions. ``--all-profiles`` activates all profiles named
by the lock. Both flags together are refused.

The lock covers every profile in advance. Switching profiles therefore changes
selection, not version resolution. Existing environment profiles are reused
unless explicitly changed during adoption.

Names belong to the environment
-------------------------------

The lock names classes and the roles they satisfy, not local instance names.
An explicit ``--name`` override takes precedence. Otherwise, a recorded binding
is reused; a new binding falls back to its declaration name or role default.
This prevents ordinary reapplication from creating duplicates under a default
name while allowing an intentional rename.

A change of instance name is not an in-place rename of deployed infrastructure.
When adoption replaces a name, the old deployment is left running and reported.
Similarly, profile deactivation leaves previously declared instances in place
unless pruning is requested, and pruning only undeclares them.

Adoption does not implicitly preview
------------------------------------

``env add --from-repo`` consumes the repository and lock, registers the
environment, fetches missing artifacts by default, and writes declarations.
``env start`` then starts infrastructure and applies.

``env apply --from-repo`` rereads the repository and lock and applies the
result immediately. ``env plan`` separately previews the existing environment
manifest. It has no ``--from-repo`` flag. This distinction matters when
reviewing a proposed profile or lock change: review the inputs and resulting
declaration rather than assuming apply contains a confirmation stage.

The current adoption reader requires ``meta-data/manifest.json``. The wider
TOML support in the RepoClass authoring group does not extend to this reader.
See :doc:`../tutorials/adopt-repository` for the end-to-end workflow.
