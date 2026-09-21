Use artifacts and locks
=======================

.. include:: ../_includes/paid-cloud.txt

Use a lock to share concrete dependency versions, and artifacts to supply
their repository trees without requiring every developer to clone them.
These are separate steps: a valid lock does not mean its artifacts are cached.

Local-only use
--------------

Local build registration, unpacking locally available artifacts, creating a
lock from explicit pins, and ``lock --check`` do not require paid NeuronSphere.
Neither do stacks: a published set of RepoClasses in a public registry
namespace installs with ``nsctl stack add`` and no tenant, and a RepoClass
build zip published with ``nsctl artifact push`` is fetched the same way
(:doc:`use-stacks`, :doc:`../explanation/oci-distribution`).
You can use a checkout directly as shown in
:doc:`../tutorials/create-repository-manifest`. Skip the next two sections
unless you have paid NeuronSphere tenant access.

Configure cloud access (paid NeuronSphere)
------------------------------------------

Start the local control plane before pulling into its librarian::

   nsctl control-plane start

Cloud access requires paid NeuronSphere and an authorised tenant account.
Configure a profile in
``$HMD_HOME/.config/nsctl.toml``. Replace these illustrative endpoints with
your tenant's actual issuer and service addresses::

   default_profile = "acme"

   [profile.acme]
   auth_url = "https://auth.example.com/oauth2/ns"
   client_id = "your-public-device-client-id"
   artifact_librarian_url = "https://artifacts.example.com"
   deployment_url = "https://deployment.example.com"

Sign in and inspect the active identity::

   nsctl login --profile acme
   nsctl whoami

Login prints a device code and verification URL. Complete sign-in in a browser;
``--no-browser`` prevents an attempt to open one automatically. Credentials
are stored in the shared HMD token cache, so other HMD tools can use them.

For service URLs, command-line overrides take precedence over environment
variables, which take precedence over profile addresses. For example,
``HMD_ARTIFACT_LIBRARIAN_URL`` overrides a profile's librarian URL. Check
these variables when a command contacts an unexpected tenant.

Choose and fetch a cloud version (paid NeuronSphere)
----------------------------------------------------

List published versions, optionally evaluating a BACON range::

   nsctl artifact versions hmd-inf-trino --profile acme
   nsctl artifact versions hmd-inf-trino --spec '== 0.1.*' --profile acme
   nsctl artifact versions hmd-inf-trino --offline

The offline form reads the previous query's cache and reports its age. It does
not fetch artifacts. In this version grammar, ``~= 0.1`` can match later minor
versions under major zero; use ``== 0.1.*`` to restrict the minor series.

After choosing an available version, pull it explicitly. This example uses an
illustrative published version::

   nsctl artifact pull hmd-inf-local-registry@0.1.4 --profile acme
   nsctl artifact unpack hmd-inf-local-registry@0.1.4

The default content type is ``build``; append ``:<type>`` for another.
Pull registers the content in the local librarian, normally
``http://localhost/hmd_ms_artifact_lib/``. Unpack materializes a repository tree
under ``$HMD_HOME/.cache/neuronsphere/artifacts/``.

Register your own build
-----------------------

After producing a build artifact in a repository, register it locally::

   nsctl artifact register
   nsctl artifact register /path/to/build.zip

The no-path form looks for the repository's build output. Use
``--repo /path/to/repo`` to select the repository, or ``--name``,
``--version``, and ``--type`` when its identity needs to be supplied explicitly.
Registration makes an existing artifact available; it does not build it.

To make a build available to *other* machines with no tenant, publish it to
an OCI registry instead::

   nsctl artifact push . ghcr.io/acme/classes/hmd-inf-otel-collector --token $GHCR_PAT

The printed ``source`` goes into the lock entry that pins this class, and
``nsctl stack build`` then fetches the zip from there. A directory is zipped
as its manifest's ``license`` declares (``nsctl repoclass license set``); see
:doc:`use-stacks`.

Create and update the lock
--------------------------

From a repository with local requirements::

   nsctl lock
   nsctl lock --check

Exact version specifiers can resolve directly. For unresolved ranges, provide
pins or capture a working local environment::

   nsctl lock --pin hmd-ms-transform@0.5.201
   nsctl lock --from-env local
   nsctl lock --check

Use a real compatible version in place of the example. ``artifact versions``
helps choose it, but ``lock`` does not automatically query the cloud to choose
the newest version.

Review the resulting ``neuronsphere.lock`` and commit it with the metadata
that declares the dependencies. It includes every profile's wants. The check
fails for declared classes with no pin, warns about extra obsolete pins,
and contacts no service. It does not verify availability or prove that the
pinned versions work together.

Each pinned entry may carry a ``digest``: the sha256 of the build zip its
``content_path`` names. ``nsctl lock`` fills it for anything the artifact
cache has already seen and keeps one the previous lock had at the same
version; ``artifact pull``, ``stack pull`` and ``stack push --update-lock``
fill it when they see the bytes. It is optional, the schema stays at
``version = 1``, and ``lock --check`` does not verify it -- but ``stack add``
refuses a zip whose digest disagrees with the lock inside the stack.

An entry may also carry a ``source``: an OCI reference (no version) that
serves the class's build zip as an artifact, published with ``nsctl artifact
push``. It is where a stack build fetches the zip with no tenant, and where
``nsctl lock --resolve`` enumerates versions first. ``artifact pull`` accepts
the same reference form. A regenerate keeps every entry's ``source``.

To pin ranges from what is published rather than by hand::

   nsctl lock --resolve

which asks each entry's ``source`` first and the cloud librarian second (only
with a credential), and reports where each answer came from.

Apply a changed lock
--------------------

To reread the repository declaration and lock into an existing environment::

   nsctl env apply dev --from-repo .

Missing artifacts cause a refusal with a suggested pull command. For a
local-only workflow, register your own builds. With paid NeuronSphere, fetch
them separately from your cloud librarian, or explicitly allow cloud fetching
in the same operation::

   nsctl env apply dev --from-repo . --pull

This command deploys; ``--pull`` is not a preview flag. A bare
``env plan dev`` previews the environment manifest already on disk.

Prepare for working offline
---------------------------

Before disconnecting, fetch and unpack the versions you need, and complete a
successful start and apply while Docker images and build dependencies are
available. ``nsctl stack pull <name>:<version>`` fills the cache with every
zip a stack pins without declaring anything, and ``nsctl stack versions
<name> --offline`` answers from the version cache. With paid NeuronSphere,
``nsctl artifact cache`` can also prefetch the current repository's BACON
``build.pre_build_artifacts`` from its cloud librarian.

The version-query cache, the local librarian's content, unpacked repository
trees, and Docker's image cache are different stores. An offline version listing
does not prove a deployment is ready to run offline. See
:doc:`../explanation/artifacts-offline` for the precise boundary.
