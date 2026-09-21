Artifacts and offline operation
===============================

.. include:: ../_includes/paid-cloud.txt

A deployment needs a repository tree as well as configuration. That tree can
come from a local checkout or from a versioned build artifact. Selecting the
source explicitly determines whether you are testing your edits or a
published build.

Local and artifact sources
--------------------------

A local source uses ``source.path``, or a checkout under
``HMD_REPO_HOME`` when no path is given. An artifact source names a class,
concrete version, and content type. It should not silently resolve to an
unrelated checkout simply because one exists on this machine.

Repository adoption uses both: the subject repository runs from its working
tree, while locked companions use artifact sources. This lets developers edit
one repository without independently rebuilding every dependency.

Deliberate local overrides can still select working trees. When diagnosing a
version discrepancy, inspect the declared source, explicit paths, and local
version preferences as well as the version string.

Four kinds of cached state
--------------------------

.. list-table::
   :header-rows: 1

   * - Store
     - What it provides
     - What it does not prove
   * - Published-version query cache
     - Versions returned by a previous librarian query
     - That any artifact payload is on this machine
   * - Local artifact librarian
     - Registered or pulled artifact content
     - That all container images are available
   * - Unpacked artifact tree cache
     - Files the deployment can mount from a resolved artifact
     - That the workload's external dependencies are cached
   * - Docker image cache
     - Previously downloaded or built container images
     - That the required repository artifact is available

The lock is not a cache. It records what versions to use, not their bytes.
Likewise, a cached version list describes a previous answer from the cloud;
it is not refreshed by an offline lookup.

When fetching is intentional
----------------------------

``artifact pull`` is an explicit cloud fetch. ``artifact register`` supplies
local build content. ``artifact versions`` queries available versions;
its ``--offline`` form uses only the prior answer.

``env add --from-repo`` fetches missing artifacts as part of initial adoption.
``--no-pull`` suppresses that. Later ``env apply --from-repo`` does not fetch
missing cloud artifacts unless you specify ``--pull``.

This boundary makes repeated local applies predictable: absence of a required
artifact is reported with a fetch remedy instead of choosing another version.
It does not mean the entire deployment is network-free. The local librarian
is itself an HTTP service, Docker may need an image, and workload deploy
commands may access package registries or other services.

Reproducibility has a scope
---------------------------

A concrete dependency version prevents a fresh apply from choosing a newer
published version based on the date. It does not freeze your subject checkout,
environment variables, external service data, or every tool inside a deployment
image. A working environment is stronger evidence of compatible versions than
a set of individually valid ranges.

Prepare offline work by fetching artifacts, materializing trees, and
successfully starting the selected workloads while all required dependencies
are available. See :doc:`../how-to/artifacts-and-locks` for the commands.
