Licensing
=========

NeuronSphere is an **open-source CLI on a source-available platform**. The
words are chosen carefully: ``nsctl`` and everything it embeds are Open
Source; the three services it runs are not, and become Open Source four years
after each release. Do not describe the platform as a whole as "open source".

What is free and what is commercial
-----------------------------------

Local development is free and needs no cloud tenant: create BACON manifests,
deploy your own checkouts, run local Docker environments, register your own
build artifacts with the local librarian, and create or check locks from
local inputs. See :doc:`tutorials/create-repository-manifest` for a complete
local-only example.

**All NeuronSphere cloud features in this documentation require paid
NeuronSphere.** This includes cloud Artifact Librarian access, querying its
published versions, fetching its artifacts, and inspecting or importing a cloud
environment's BOM. These workflows require an authorised account and endpoints
for your organisation's NeuronSphere tenant. Installing the free CLI or
starting a local control plane does not create a paid tenant or grant access.

Stacks and CLI plugins (:doc:`how-to/use-stacks`,
:doc:`how-to/install-cli-plugins`) are free to install from a public
registry namespace such as ``ghcr.io/hmdlabs``: an anonymous pull needs no
tenant and no token. A stack's descriptor files -- its lock, its OCI
manifest, a plugin's descriptor -- are Apache 2.0; the RepoClass zips inside
a stack carry their own licences, declared by each repository in its
manifest's ``license`` and annotated on every layer. A private or
curated namespace uses the same protocol with a login token, and ``nsctl``
does not distinguish the two. See :doc:`explanation/oci-distribution`.

Product access and source-code licences are separate. The requirement for paid
cloud features does not change the component licence grants described below,
and the presence of a cloud command in the open-source CLI does not include
the corresponding paid service.

What is licensed how
--------------------

.. list-table::
   :header-rows: 1
   :widths: 34 22 44

   * - Component
     - Licence
     - Why
   * - ``nsctl`` binary, this repository
     - Apache License 2.0
     - The free entry point. Permissive, with a patent grant, so it clears
       enterprise legal review without a conversation.
   * - Deploy descriptors embedded in the binary (``meta-data/``,
       ``src/cdktf/``, ``src/helm/``, ``src/local/``, ``src/opa-bundles/`` of
       every bundled repo class)
     - Apache License 2.0
     - Other repo classes depend on them; the format has to be free to copy.
   * - Platform libraries and images the descriptors need
       (``hmd-lib-cdktf``, ``hmd-lib-cdktf-factories``, ``hmd-ms-base``,
       ``hmd-img-projectbuilder``, ``hmd-img-k3s-floci``, ...)
     - Apache License 2.0
     - Framework code third parties build their own repo classes on.
   * - The BACON manifest specification (``hmd-docs-bacon``)
     - CC-BY-4.0 prose; Apache 2.0 schemas and examples
     - Adoption of the format is the point. Anyone may implement it.
   * - **Deployment engine** (``hmd-ms-deployment``), **Artifact Librarian**
       (``hmd-ms-librarian``), **NeuronSphere GUI** (``hmd-app-neuronsphere``)
       -- their ``src/python/``, ``src/docker/``, ``src/typescript/``
     - Business Source License 1.1, Change Licence Apache 2.0
     - The services that carry the commercial product. See below.
   * - Third-party components (Floci -- MIT, k3s -- Apache 2.0, PostgreSQL,
       nginx)
     - Their own
     - Redistributed unchanged.

The three BUSL repositories are *path-scoped*: one ``LICENSE.txt`` grants
Apache 2.0 to the deploy-descriptor paths and BUSL 1.1 to everything else,
with the full Apache text alongside as ``LICENSE-Apache-2.0.txt``. The repo is
not split, so the BACON convention that a repo class is one repository holds.

What the Business Source License means for you
----------------------------------------------

The Additional Use Grant, identical in all three repositories:

* **Any local, evaluation, development or test use is free**, for an
  organisation of any size -- including every environment
  ``nsctl env start`` brings up. This is the base BUSL grant plus a sentence
  making it explicit.
* **Non-commercial production use is free**: individuals for personal
  purposes, teaching and academic research, non-profits for their own
  purposes, and running a project whose source is public under an Open
  Source licence.
* **Production use by or on behalf of a business requires a commercial
  agreement** with HMD Labs, Inc., whatever the size of the business.
* **Nobody may offer the services to third parties as a hosted or managed
  service** without an agreement.
* **Four years after a version is published it becomes Apache 2.0.**

The grant is enforced the way every source-available licence is: legally,
against organisations. There are no licence keys, no phone-home and no
feature gates, and ``nsctl`` never checks anything.

Existing customers running the platform in their own AWS accounts are
governed by their agreement with HMD Labs, not by ``LICENSE.txt``; the grant
permits their use in any case.

Why not one licence for everything
----------------------------------

*Apache 2.0 for everything* would make the services free to run at any scale
and free to offer as a competing hosted product; the code that costs the most
to build would earn nothing. *BUSL for everything*, including ``nsctl``,
would put a non-OSI licence on the one artifact whose whole job is
zero-friction adoption. Splitting by artifact -- open client, protected
service, Open Source eventually -- is the shape Elastic, HashiCorp, MariaDB
and CockroachDB settled on for the same reason.

``hmd-cli-bartleby`` is also BUSL 1.1. That predates this policy and differs
deliberately: bartleby is a documentation toolchain adjacent to paid work,
not the funnel entry point.

Keeping the binary single-licence
---------------------------------

``nsctl`` is Apache 2.0 only if nothing BUSL-licensed is compiled or embedded
into it. ``tools/repopack`` therefore never packs ``src/python/``,
``src/typescript/`` or ``src/docker/`` from any repo class, and
``internal/bundled`` has a test that fails the build if a shipped archive
contains them. The services themselves reach the local platform as container
images ``nsctl`` pulls at runtime -- a dependency like PostgreSQL, not part of
the binary. Every NeuronSphere image carries an
``org.opencontainers.image.licenses`` label, and the three BUSL images also
ship their licence texts under ``/licenses/<repo>/``.

The same split governs what ``nsctl`` publishes *from* those repositories
as stack layers and RepoClass artifacts, by the same mechanism anyone's
repository uses: a ``license`` declaration in the manifest
(``spdx: Apache-2.0``, excluding ``src/python/``, ``src/docker/`` and
``src/typescript/``), which ``nsctl artifact push``, ``stack build`` and
``stack push`` honour when they zip a tree and record on every layer as
``org.opencontainers.image.licenses``. ``nsctl`` itself infers no licence
and refuses no artifact: the declaration is each repository's, and a
third party publishes an MIT service, or keeps a proprietary directory out
of a public stack, the same way. See ``NERD017`` SPEC011.

Trademarks and contributions
----------------------------

Neither licence grants rights to the NeuronSphere, nsctl or HMD Labs names or
logo; see ``TRADEMARKS.md``. Contributions to Apache-licensed repositories
are accepted under the Developer Certificate of Origin (``git commit -s``);
see ``CONTRIBUTING.md``.
