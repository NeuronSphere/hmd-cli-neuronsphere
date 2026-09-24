nsctl command reference
=======================

This page is generated from the Cobra command tree. Do not edit it by hand; run ``make docs-reference``.

nsctl
=====

nsctl runs the local NeuronSphere: one control plane per HMD_HOME and the
environment substrate your deployments sit on. Docker is the only host
prerequisite.

New here? Run "nsctl quickstart". It checks the host, creates your first
environment and offers to adopt your own repository, naming each command before
it runs it.

It ships the control plane and the substrate -- a cluster, a database, and the
External Secrets operator a cloud chart's secrets resolve through. Everything
above that is a RepoClass you add.

Usage
~~~~~

.. code-block:: text

   nsctl

Local flags
~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl agent
-----------

Install bundled skills for coding agents

Usage
~~~~~

.. code-block:: text

   nsctl agent

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl agent skills
------------------

Bundled skills teach Codex or Claude Code how to use nsctl; they do not
configure either agent, a model, or an MCP server. Project scope is the default
and writes only under the selected repository.

Usage
~~~~~

.. code-block:: text

   nsctl agent skills

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl agent skills doctor
-------------------------

Check selected skill locations and installed-skill ownership

Usage
~~~~~

.. code-block:: text

   nsctl agent skills doctor [flags]

Local flags
~~~~~~~~~~~

* ``--host`` — Agent host: codex, claude, or all (default: ``all``)
* ``--json`` — Print JSON
* ``--path`` — Project root (not valid with --scope user) (default: ``.``)
* ``--scope`` — Install scope: project or user (default: ``project``)

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl agent skills install
--------------------------

Install selected bundled skills into native agent directories

Usage
~~~~~

.. code-block:: text

   nsctl agent skills install <skill> [<skill>...] [flags]

Local flags
~~~~~~~~~~~

* ``--dry-run`` — Show destinations without writing
* ``--force`` — Replace an existing selected skill
* ``--host`` — Agent host: codex, claude, or all (default: ``all``)
* ``--path`` — Project root (not valid with --scope user) (default: ``.``)
* ``--scope`` — Install scope: project or user (default: ``project``)

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl agent skills list
-----------------------

List bundled skills and their state at the selected targets

Usage
~~~~~

.. code-block:: text

   nsctl agent skills list [flags]

Local flags
~~~~~~~~~~~

* ``--host`` — Agent host: codex, claude, or all (default: ``all``)
* ``--json`` — Print JSON
* ``--path`` — Project root (not valid with --scope user) (default: ``.``)
* ``--scope`` — Install scope: project or user (default: ``project``)

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl agent skills remove
-------------------------

Remove installed bundled skills

Usage
~~~~~

.. code-block:: text

   nsctl agent skills remove <skill> [<skill>...] [flags]

Local flags
~~~~~~~~~~~

* ``--dry-run`` — Show destinations without removing
* ``--force`` — Remove even if the selected skill changed locally
* ``--host`` — Agent host: codex, claude, or all (default: ``all``)
* ``--path`` — Project root (not valid with --scope user) (default: ``.``)
* ``--scope`` — Install scope: project or user (default: ``project``)

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl artifact
--------------

Manages the versioned artifacts a `source: {type: artifact}` instance deploys from.

A RepoClass declared that way is resolved from the control plane's own Artifact
Librarian and an unpacked copy under $HMD_HOME/.cache/neuronsphere/artifacts,
never from a checkout -- so a plugin is distributed by version number rather
than by git remote. These verbs are how that librarian gets filled.

Nothing else reaches the network for an artifact. An apply resolves from the
cache alone, which is what makes it behave the same on an aeroplane.

Usage
~~~~~

.. code-block:: text

   nsctl artifact

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl artifact cache
--------------------

Reads build.pre_build_artifacts from a BACON manifest and copies each one from
a cloud Artifact Librarian into the control plane's, so a later build in this
HMD_HOME resolves them without the network.

The spec string BACON writes -- <name>@<version>:<item_type> -- is already the
librarian's own content-path grammar, parsed by the same function. Nothing is
translated and nothing new is named.

These are build *inputs*, so they are stored and not unpacked: an apply
resolves RepoClasses, not schemas.

Usage
~~~~~

.. code-block:: text

   nsctl artifact cache [flags]

Examples
~~~~~~~~

.. code-block:: shell

   nsctl artifact cache
     nsctl artifact cache --manifest ../hmd-ms-transform/meta-data/manifest.json

Local flags
~~~~~~~~~~~

* ``--local-url`` — the control plane's Artifact Librarian (default: ``http://localhost/hmd_ms_artifact_lib/``)
* ``--manifest`` — BACON manifest to read (default meta-data/manifest.json)
* ``--profile`` — profile in nsctl.toml whose endpoints to use
* ``--url`` — cloud Artifact Librarian URL, overriding HMD_ARTIFACT_LIBRARIAN_URL

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl artifact pull
-------------------

Downloads a versioned artifact from a cloud Artifact Librarian, stores it in
the control plane's own, and unpacks it into the cache an apply resolves from.

The item type defaults to `build`, which is what `hmd build` publishes.

With no version, the newest published one is used; with a BACON version
specifier in its place, the newest that satisfies it. Either way the version
chosen is printed, because a command that quietly picked one has told you
nothing you could check. `nsctl artifact versions` answers the same question
without fetching anything.

This is the only command that reaches a cloud librarian on your behalf.
Resolution never does: an apply that reached the internet without being asked
is an apply that behaves differently on an aeroplane.

Usage
~~~~~

.. code-block:: text

   nsctl artifact pull <repo-class>[@<version>][:<type>] [flags]

Examples
~~~~~~~~

.. code-block:: shell

   nsctl artifact pull hmd-inf-local-registry@0.1.4
     nsctl artifact pull hmd-inf-local-registry
     nsctl artifact pull "hmd-inf-trino@~= 0.1"
     nsctl artifact pull hmd-lang-foo@0.3.1:schema
     nsctl artifact pull ghcr.io/acme/classes/hmd-inf-otel-collector:0.1.188   # an OCI artifact, no tenant

Local flags
~~~~~~~~~~~

* ``--local-url`` — the control plane's Artifact Librarian (default: ``http://localhost/hmd_ms_artifact_lib/``)
* ``--profile`` — profile in nsctl.toml whose endpoints to use
* ``--spec`` — with an OCI reference, choose the version by a BACON version spec
* ``--url`` — cloud Artifact Librarian URL, overriding HMD_ARTIFACT_LIBRARIAN_URL

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl artifact push
-------------------

Publish one RepoClass's build artifact to an OCI registry, where a
neuronsphere.lock entry can name it as its "source" and anyone -- with no
tenant -- can fetch it. A directory is zipped as its manifest declares: the
paths under "license.exclude" stay out, and the "license" SPDX expression
annotates the artifact (org.opencontainers.image.licenses); a zip is pushed
as is and annotated from the manifest inside it. The tag is meta-data/VERSION
unless --tag or the reference names one. A credential is required (--token,
HMD_REGISTRY_TOKEN, or a profile's registry_url after "nsctl login").

Usage
~~~~~

.. code-block:: text

   nsctl artifact push <repo-dir | zip> <ref> [flags]

Examples
~~~~~~~~

.. code-block:: shell

   nsctl artifact push . ghcr.io/acme/classes/hmd-inf-otel-collector --token $GHCR_PAT
     nsctl artifact push dist/hmd-inf-otel-collector_0.1.188_build.zip ghcr.io/acme/classes/hmd-inf-otel-collector

Local flags
~~~~~~~~~~~

* ``--tag`` — tag to publish under (default: meta-data/VERSION)
* ``--token`` — registry token or PAT (overrides HMD_REGISTRY_TOKEN)

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl artifact register
-----------------------

Stores an `hmd build` output in the control plane's Artifact Librarian, so
your own build is deployable by a manifest that names it by version -- with no
checkout under $HMD_REPO_HOME and no cloud round trip.

<path> may be a zip, or a directory which is zipped on the fly. With neither,
the zip `hmd build` writes under $HMD_BUILD_OUTPUT_DIR is used, resolving the
repo and version from meta-data/ in --repo.

Re-registering a version overwrites it and drops the unpacked copy, so the next
apply deploys the build that was just registered rather than the one before it.

Usage
~~~~~

.. code-block:: text

   nsctl artifact register [<path>] [flags]

Examples
~~~~~~~~

.. code-block:: shell

   nsctl artifact register
     nsctl artifact register ./dist/hmd-inf-local-registry_0.1.4_build.zip
     nsctl artifact register ~/work/hmd-inf-local-registry

Local flags
~~~~~~~~~~~

* ``--local-url`` — the control plane's Artifact Librarian (default: ``http://localhost/hmd_ms_artifact_lib/``)
* ``--name`` — repo class, overriding the one in the artifact
* ``--profile`` — profile in nsctl.toml whose endpoints to use
* ``--repo`` — repo whose meta-data names the build to look for (default: ``.``)
* ``--type`` — librarian content item type (default: ``build``)
* ``--url`` — cloud Artifact Librarian URL, overriding HMD_ARTIFACT_LIBRARIAN_URL
* ``--version`` — version, overriding the one in the artifact

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl artifact unpack
---------------------

Fetches a versioned artifact from the control plane's own Artifact Librarian
and unpacks it into the cache an apply resolves from.

`pull` and `register` both do this on the way past, so this is the repair
path: it refills a cache that was deleted, without a cloud round trip and
without rebuilding anything.

Usage
~~~~~

.. code-block:: text

   nsctl artifact unpack <repo-class>@<version>[:<type>] [flags]

Examples
~~~~~~~~

.. code-block:: shell

   nsctl artifact unpack hmd-inf-local-registry@0.1.4

Local flags
~~~~~~~~~~~

* ``--local-url`` — the control plane's Artifact Librarian (default: ``http://localhost/hmd_ms_artifact_lib/``)
* ``--profile`` — profile in nsctl.toml whose endpoints to use
* ``--url`` — cloud Artifact Librarian URL, overriding HMD_ARTIFACT_LIBRARIAN_URL

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl artifact versions
-----------------------

Asks a cloud Artifact Librarian what a repo class has published, newest first,
and -- with --spec -- what a BACON version specifier resolves to today.

This is the question you have when you are standing an environment up and have
to choose a version, and again when you want to know what is newer. A manifest
almost never names a version: it names a range, and nothing else in the platform
turns a range into a version.

--spec takes the grammar a manifest writes. Note that `~= 0.1` is not "the 0.1
series": it means major 0 and minor at least 1, so 0.2.5 satisfies it. Write
`== 0.1.*` for the series.

The answer is cached under $HMD_HOME/.cache/neuronsphere/versions and never
refreshed behind your back: this command queries, --offline reads what the last
query left and says how old it is. Nothing that deploys resolves a version at
all -- a deploy whose result depends on the day it ran is the thing a lock
exists to prevent.

Usage
~~~~~

.. code-block:: text

   nsctl artifact versions <repo-class> [flags]

Examples
~~~~~~~~

.. code-block:: shell

   nsctl artifact versions hmd-inf-trino
     nsctl artifact versions hmd-inf-trino --spec "~= 0.1"
     nsctl artifact versions hmd-inf-trino --offline

Local flags
~~~~~~~~~~~

* ``--all`` — List every version rather than the newest few
* ``--offline`` — Read the last query's answer instead of asking, reporting how old it is
* ``--profile`` — profile in nsctl.toml whose endpoints to use
* ``--spec`` — Report what this version specifier resolves to, as a manifest writes it
* ``--type`` — Content item type (default build)
* ``--url`` — cloud Artifact Librarian URL, overriding HMD_ARTIFACT_LIBRARIAN_URL

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl authd
-----------

A local stand-in for Okta, for testing authorization without one.

It emulates Okta's URL shape because the consumers build those URLs by string
concatenation -- Superset and Airflow from OKTA_BASE_URL, hmd-lib-auth from the
issuer -- so the cloud's own configuration runs locally unmodified.

It signs with a key it generated and will mint a token with any claims asked of
it. That is deliberate: what is being tested locally is what the platform does
with a token's claims, and neither the Rego policies nor the apps' role mapping
verify a signature.

Usage
~~~~~

.. code-block:: text

   nsctl authd

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl authd token
-----------------

Prints a signed JWT built from the claims given.

Signed with the same key the running server publishes, and minted locally --
so this works whether or not the control plane is up, which is what makes it
usable from a test.

The groups matter most. They decide a user's role in Superset and Airflow and
every Rego decision about them, and the applications parse them by shape:

    NeuronSphere Airflow Admin - Local (none)
    NeuronSphere <app> <role> - <Environment> (<customer code>)

Usage
~~~~~

.. code-block:: text

   nsctl authd token [flags]

Examples
~~~~~~~~

.. code-block:: shell

   nsctl authd token --server services --sub ms-deployment
     nsctl authd token --group 'NeuronSphere Superset Admin - Local (none)'
     nsctl authd token --claim tenant=acme --scope service
     nsctl authd token --sub alice --show-claims
     nsctl authd token --decode "$TOKEN"

Local flags
~~~~~~~~~~~

* ``--aud`` — override the server's audience
* ``--claim`` — an extra claim as key=value; repeatable, JSON values allowed (default: ``[]``)
* ``--client-id`` — client id, when it differs from the subject
* ``--decode`` — decode a token given on stdin instead of minting one
* ``--group`` — a groups claim entry; repeatable (default: ``[]``)
* ``--lifetime`` — how long the token is valid (default: ``1h0m0s``)
* ``--scope`` — a scp claim entry; repeatable (default: ``[]``)
* ``--server`` — authorization server: ns (api://neuronsphere) or services (api://neuronsphere-services) (default: ``ns``)
* ``--show-claims`` — print the claim set instead of the token
* ``--sub`` — subject; also the default client id

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl bom
---------

Reads what a cloud environment is running, and builds a local one from part of it.

A cloud environment is a known-good version set -- somebody is running it in
earnest -- which makes it the strongest thing to seed a local environment from.
`nsctl bom show` says what one is running; `nsctl bom import` copies a
selection of it down and declares it here.

Every verb here reads a cloud service and writes only local files. Nothing in
nsctl writes to a cloud hmd-ms-deployment: a BOM is an input to a local
manifest, never an output to a cloud deploy.

Which tenant is answered by --profile, from nsctl.toml. See NERD012.

Usage
~~~~~

.. code-block:: text

   nsctl bom

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl bom envs
--------------

Lists every environment in a tenant's deployment graph.

No instance counts: nothing carries one, so reporting them would mean fetching
every environment's BOM -- one expensive request each, hidden behind a listing
that looks cheap. `nsctl bom show <env>` is where a count comes from.

Usage
~~~~~

.. code-block:: text

   nsctl bom envs [flags]

Examples
~~~~~~~~

.. code-block:: shell

   nsctl bom envs
     nsctl bom envs --profile acme

Aliases: ``environments``.

Local flags
~~~~~~~~~~~

* ``--profile`` — profile in nsctl.toml whose endpoints to use
* ``--url`` — cloud hmd-ms-deployment URL, overriding HMD_CLOUD_MS_DEPLOYMENT_URL

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl bom import
----------------

Copies a selection of a cloud environment's BOM into a local environment.

Each selected instance is declared at the version the cloud is running, from an
artifact -- never from a checkout, because a version that came from a cloud
librarian must not silently resolve to whatever happens to be under
$HMD_REPO_HOME. The artifacts are fetched into the control plane's Artifact
Librarian on the way, so what is declared can actually deploy.

The selection is closed under the roles the repo classes mark *required*.
hmd-ms-deployment fails a whole ChangeSet on a required role nothing fills, so
importing one instance without what fills those roles would produce a manifest
that cannot deploy -- and the failure would arrive at `nsctl env apply`
naming a role rather than the import that omitted it. Optional roles are not
followed, and are listed; --with <role> follows one anyway.

Roles filled by the environment substrate are bound rather than imported. A
required role nothing here fills is bound to the core instance when only its
presence is validated -- reported, because the core instance does not deploy
whatever was stubbed -- and refused when the role asks for a resource type,
which cannot be faked. --no-stub-roles refuses in both cases; --no-deps takes
the selection literally instead.

To narrow a large closure by hand, write it out with
`nsctl bom show <env> --save-selection <file>`, edit the take lines, and
pass it back with --selection. See NERD013.

Declaring is not deploying. Run `nsctl env apply` afterwards, or pass
--apply to run it here once the selection is declared. A partial import -- one
where an artifact could not be fetched -- is declared but never applied.

Usage
~~~~~

.. code-block:: text

   nsctl bom import <env> [flags]

Examples
~~~~~~~~

.. code-block:: shell

   nsctl bom import dev --instance ms-transform
     nsctl bom import dev --instance ms-transform --apply
     nsctl bom import dev --class hmd-inf-trino --dry-run
     nsctl bom import dev --all --exclude superset --env scratch

Local flags
~~~~~~~~~~~

* ``--all`` — Take the whole BOM
* ``--apply`` — Run nsctl env apply on the environment once the selection is declared
* ``--class`` — Every instance of a repo class. Repeatable (default: ``[]``)
* ``--dry-run`` — Show what would be declared, writing and fetching nothing
* ``--env`` — Local environment to declare into (default: the default environment)
* ``--exclude`` — An instance to leave out. Repeatable (default: ``[]``)
* ``--include-failed`` — Include instances whose last deployment FAILED
* ``--instance`` — An instance to take, by name. Repeatable (default: ``[]``)
* ``--librarian-url`` — cloud Artifact Librarian URL, overriding HMD_ARTIFACT_LIBRARIAN_URL
* ``--local-url`` — the control plane's Artifact Librarian (default: ``http://localhost/hmd_ms_artifact_lib/``)
* ``--no-deps`` — Take the selection literally, without what fills its roles
* ``--no-pull`` — Declare without fetching, naming the pulls to run
* ``--no-stub-roles`` — Refuse a required role nothing local fills, rather than binding it to the core instance
* ``--profile`` — profile in nsctl.toml whose endpoints to use
* ``--resolve`` — With --dry-run or --no-pull, still fetch what is needed to read which roles are required
* ``--save-selection`` — Write what this run resolved to a `file`, to edit and pass back with --selection
* ``--selection`` — Take the instances an edited selection `file` marks take = true
* ``--url`` — cloud hmd-ms-deployment URL, overriding HMD_CLOUD_MS_DEPLOYMENT_URL
* ``-V, --verbose`` — With --apply, show the underlying command output
* ``--with`` — Follow an optional role, by role name or repo class. Repeatable (default: ``[]``)

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl bom show
--------------

Prints a cloud environment's Bill of Materials: every deployed instance, the
concrete version it is running, and whether this machine already holds that
artifact.

With selection flags it prints exactly what `nsctl bom import` would take,
dependency closure included -- so a show is a preview of the import that
follows it, and needs no --dry-run of its own.

The closure follows only the roles a repo class marks required, which is read
from that class's own manifest -- and that manifest arrives with its artifact.
Without --resolve this reads whatever is already unpacked here and closes over
every role of everything else, saying how many. With --resolve it fetches what
it needs, and the preview is exact.

Only instances whose current deployment is DEPLOYED or FAILED appear at all:
the service builds the BOM from those and drops the rest, so this is not a
listing of everything the environment declares.

Usage
~~~~~

.. code-block:: text

   nsctl bom show <env> [flags]

Examples
~~~~~~~~

.. code-block:: shell

   nsctl bom show dev
     nsctl bom show dev --instance ms-transform
     nsctl bom show dev --class hmd-inf-trino --json

Local flags
~~~~~~~~~~~

* ``--all`` — Take the whole BOM
* ``--class`` — Every instance of a repo class. Repeatable (default: ``[]``)
* ``--exclude`` — An instance to leave out. Repeatable (default: ``[]``)
* ``--include-failed`` — Include instances whose last deployment FAILED
* ``--instance`` — An instance to take, by name. Repeatable (default: ``[]``)
* ``--json`` — Print the BOM as the service returned it
* ``--librarian-url`` — cloud Artifact Librarian URL, overriding HMD_ARTIFACT_LIBRARIAN_URL
* ``--local-url`` — the control plane's Artifact Librarian (default: ``http://localhost/hmd_ms_artifact_lib/``)
* ``--no-deps`` — Take the selection literally, without what fills its roles
* ``--no-stub-roles`` — Refuse a required role nothing local fills, rather than binding it to the core instance
* ``--profile`` — profile in nsctl.toml whose endpoints to use
* ``--resolve`` — Fetch the artifacts needed to read which roles are required, making this an exact preview
* ``--save-selection`` — Write this selection to a `file` to edit and pass back to bom import --selection
* ``--url`` — cloud hmd-ms-deployment URL, overriding HMD_CLOUD_MS_DEPLOYMENT_URL
* ``--with`` — Follow an optional role, by role name or repo class. Repeatable (default: ``[]``)

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl control-plane
-------------------

The control plane is shared: one per HMD_HOME, serving every environment.

It is given its own verb because stopping it takes every environment's emulated
AWS with it -- their Lambdas, gateways, buckets and secrets, not just the
deployment graph's availability. The Python CLI has no independent lifecycle
for it at all; it is only ever stopped as a side effect of a bare
`hmd neuronsphere down`.

Usage
~~~~~

.. code-block:: text

   nsctl control-plane

Aliases: ``cp``.

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl control-plane apply
-------------------------

Converges the running control plane to $HMD_HOME/.config/control-plane.yaml.

`control-plane start` applies too -- an extension is up whenever the control
plane is. This is the fast path: it starts, restarts and reconfigures the
declared extensions without the Floci health wait, the bootstrap or the route
rewrite a full start does.

Editing the manifest by hand and running this is the same operation as using
`nsctl control-plane repo`.

An extension that fails is reported and skipped; nothing else is affected. The
exit status is non-zero when any did, so a script notices.

Usage
~~~~~

.. code-block:: text

   nsctl control-plane apply [flags]

Local flags
~~~~~~~~~~~

* ``-V, --verbose`` — Show the underlying command output

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl control-plane repo
------------------------

Edits the control-plane manifest at $HMD_HOME/.config/control-plane.yaml.

These verbs are wrappers: they change that file and nothing else. Editing it by
hand is equivalent, and either way `nsctl control-plane apply` is what starts the
result -- so nothing here touches a running control plane on its own.

An extension declared here outlives every environment, is up whenever the
control plane is, and there is one of it per HMD_HOME. Anything that should be
one-per-environment belongs in an environment manifest instead; see
`nsctl repo add`.

Usage
~~~~~

.. code-block:: text

   nsctl control-plane repo

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl control-plane repo add
----------------------------

Adds a repo class to the control-plane manifest.

The instance is named after the repo class with its hmd- prefix dropped unless
--name says otherwise. The repo class must carry
src/local/docker-compose.extension.yml; that file is what it contributes.

Declaring is not starting. Run `nsctl control-plane apply` to start the result.

Usage
~~~~~

.. code-block:: text

   nsctl control-plane repo add <repo-class>[@<version>] [flags]

Examples
~~~~~~~~

.. code-block:: shell

   nsctl control-plane repo add hmd-inf-local-registry --name registry \
       --config url=http://registry.local.neuronsphere.io --config upstream=server:3141
     nsctl control-plane repo add hmd-inf-local-registry --config pypi.enabled=true

Local flags
~~~~~~~~~~~

* ``--config`` — An instance configuration value as key=value; dotted keys nest; repeatable (default: ``[]``)
* ``--name`` — Instance name (default: the repo class without its hmd- prefix)
* ``--path`` — Working tree to run from (default: $HMD_REPO_HOME/<repo-class>)

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl control-plane repo list
-----------------------------

Lists the control-plane manifest's declarations with their resolved versions.

The version source matters as much as the version: "0.1.4 from a working tree"
and "0.1.4 from a bundled tree" are different facts.

Usage
~~~~~

.. code-block:: text

   nsctl control-plane repo list

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl control-plane repo remove
-------------------------------

Removes an instance from the control-plane manifest.

This edits the manifest only. What is already running stays running: nsctl does
not tear an extension down on your behalf. `nsctl env purge` with no name is
the verb that does, and it removes the whole control plane with it.

Usage
~~~~~

.. code-block:: text

   nsctl control-plane repo remove <instance>

Aliases: ``rm``.

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl control-plane reset
-------------------------

Redeploys hmd-ms-naming, hmd-ms-artifact-lib and hmd-ms-deployment (or only
the ones named) on a control plane that has already bootstrapped, resolving
each one's version the same way a first bootstrap does.

It touches nothing else: no VPC, no control-plane database, no graph, no
environment's k3s/Postgres/graph state. Use this instead of a stop/start when a
foundation service needs to pick up a newer image without a full re-bootstrap
-- stop/start does not do that for these three, since they are only ever
deployed from inside Bootstrap, which runs exactly once.

Usage
~~~~~

.. code-block:: text

   nsctl control-plane reset [repo-class...]

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl control-plane start
-------------------------

Starts the control plane and leaves it running.

Idempotent: containers whose configuration has not changed are started in
place rather than rebuilt.

Usage
~~~~~

.. code-block:: text

   nsctl control-plane start [flags]

Local flags
~~~~~~~~~~~

* ``--upgrade`` — Pull the latest images before starting
* ``-V, --verbose`` — Show the underlying command output

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl control-plane status
--------------------------

Report the control plane's containers, health and environments

Usage
~~~~~

.. code-block:: text

   nsctl control-plane status

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl control-plane stop
------------------------

Stops the control plane. This is the only command that does.

It refuses while any environment is still running, because stopping it takes
every environment's emulated AWS with it -- their Lambdas, gateways, buckets and
secrets, not just the deployment graph's availability.

Usage
~~~~~

.. code-block:: text

   nsctl control-plane stop [flags]

Local flags
~~~~~~~~~~~

* ``--force`` — Stop even while environments are running

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl doctor
------------

Reports what nsctl resolved and what the container engine says about itself.

Prints the endpoint nsctl will use and where that came from, the engine's
version and size, whether host paths are visible to it, and whether the local
host names resolve. It changes nothing.

With a home set it also reports each environment's substrate: whether the
service versions Floci is running are the ones this nsctl resolves, and whether
their routes answer. A stale one warns and names the restart that refreshes it;
a service returning 5xx where a healthy one answers 404 fails, and says what
that means. Nothing here needs a credential, and nothing here is fixed by
signing in.

Exits 2 when something needs fixing, 0 otherwise -- including when checks only
warn.

Usage
~~~~~

.. code-block:: text

   nsctl doctor

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl env
---------

Work with local environments

Usage
~~~~~

.. code-block:: text

   nsctl env

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl env add
-------------

Registers an environment and allocates its account, port slot and names.

This only writes the registry. The environment's database, cluster and routes
are created by `nsctl env start <name>`, which is also what makes it usable.

A new environment is empty: it gets the substrate and nothing else. Declare
what should run on it with `nsctl repo add --env <name> <repo-class>`.

With --from-repo it is not empty. The repository's `local` section and its
checked-in neuronsphere.lock say what to stand up alongside it, every activated
entry is declared at its pinned version, and the repository itself is declared
from its working tree -- which is what makes it the thing under test.

This is the one command that fetches an artifact without being asked, because
this is first start: there is no environment yet, so there is no offline
expectation to violate. --no-pull suppresses it.

Usage
~~~~~

.. code-block:: text

   nsctl env add <name> [flags]

Examples
~~~~~~~~

.. code-block:: shell

   nsctl env add dev
     nsctl env add dev --from-repo .
     nsctl env add dev --from-repo . --profile transforms
     nsctl env add dev --from-repo . --lean --name neptune-db=my-graph

Local flags
~~~~~~~~~~~

* ``--all-profiles`` — Activate every profile the lock mentions
* ``--default`` — Make this the default environment
* ``--from-repo`` — Build the environment from the repository at this path
* ``--lean`` — Activate no profiles: the repository and its unconditional entries alone
* ``--local-url`` — the control plane's Artifact Librarian (default: ``http://localhost/hmd_ms_artifact_lib/``)
* ``--name`` — Name one instance, as <role-or-declared-name>=<instance>. Repeatable (default: ``[]``)
* ``--no-pull`` — Do not fetch the artifacts the declaration names
* ``--profile`` — Local profiles to activate. Repeatable, or comma-separated (default: ``[]``)
* ``--url`` — cloud Artifact Librarian URL, overriding HMD_ARTIFACT_LIBRARIAN_URL

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl env apply
---------------

Deploys the environment substrate and everything the manifest declares.

The manifest at $HMD_HOME/environments/<name>.yaml is the desired state.
Editing it by hand and running this is the same operation as `nsctl repo add`,
which edits that file and reconciles for you.

This is what `env start` runs at the end, so applying after an edit does not
need a restart. An environment with no manifest gets the substrate alone.

With --from-repo it re-reads a repository's lock first and reconciles the
environment to it. Unlike `env add` this is offline: an artifact nothing has
fetched fails naming `nsctl artifact pull` rather than reaching for the
network, because an apply that reaches the internet unasked is an apply that
behaves differently on an aeroplane. --pull fetches what is missing first, as a
declared step.

With no --profile it uses the profiles the environment recorded, and with no
--name the instance names it already bound -- so a re-apply never duplicates an
instance you renamed. Deactivating a profile leaves its instances declared
unless --prune; nothing is ever torn down on your behalf.

Usage
~~~~~

.. code-block:: text

   nsctl env apply [name] [flags]

Examples
~~~~~~~~

.. code-block:: shell

   nsctl env apply
     nsctl env apply dev --from-repo .
     nsctl env apply dev --from-repo . --profile full --pull

Local flags
~~~~~~~~~~~

* ``--all-profiles`` — Activate every profile the lock mentions
* ``--force-full-redeploy`` — Deploy everything declared, ignoring what the graph says is already deployed
* ``--from-repo`` — Build the environment from the repository at this path
* ``--lean`` — Activate no profiles: the repository and its unconditional entries alone
* ``--local-url`` — the control plane's Artifact Librarian (default: ``http://localhost/hmd_ms_artifact_lib/``)
* ``--name`` — Name one instance, as <role-or-declared-name>=<instance>. Repeatable (default: ``[]``)
* ``--profile`` — Local profiles to activate. Repeatable, or comma-separated (default: ``[]``)
* ``--prune`` — Undeclare instances this repository no longer asks for. Does not tear them down
* ``--pull`` — Fetch the artifacts the declaration names but this machine does not hold
* ``--url`` — cloud Artifact Librarian URL, overriding HMD_ARTIFACT_LIBRARIAN_URL
* ``-V, --verbose`` — Show the underlying command output

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl env credentials
---------------------

Resolves every declared way in to the environment's instances -- the "access"
section of each repo class's BACON manifest (nsctl repoclass access) -- filling in
the instance name, deployment id, environment and Ingress host.

The credential itself is withheld unless --reveal is passed: what is printed by
default is the URL, the user who signs in, and where the credential lives. An
entry that could not be resolved is reported with the reason rather than omitted,
because "declared, and here is why it is not answering yet" is what a reader
needs after a deploy that has not finished.

Reading a value needs the control plane's Floci; everything else is read from
local state and works with nothing running.

Usage
~~~~~

.. code-block:: text

   nsctl env credentials [<name>] [flags]

Examples
~~~~~~~~

.. code-block:: shell

   nsctl env credentials
     nsctl env credentials dev
     nsctl env credentials dev --instance superset --reveal
     nsctl env credentials dev --json

Local flags
~~~~~~~~~~~

* ``--instance`` — Only this instance
* ``--json`` — Print the result as JSON
* ``--reveal`` — Print the credential values, not only where they live

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl env delete
----------------

Removes an environment from the registry, freeing its account and port slot.

This is a registry edit, not a teardown. Stop the environment first: an
unregistered environment whose containers are still running is worse than
either state alone, because nothing left knows how to address them.

Usage
~~~~~

.. code-block:: text

   nsctl env delete <name> [flags]

Aliases: ``rm``.

Local flags
~~~~~~~~~~~

* ``--yes`` — Confirm the removal

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl env list
--------------

List the registered environments

Usage
~~~~~

.. code-block:: text

   nsctl env list

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl env plan
--------------

Builds exactly what `env apply` would build -- the same reconcile diff,
the same catalog registrations -- and stops there: it never calls
apply_changeset and never runs a node.

Beyond the reconcile diff, it POSTs the would-be ChangeSet definition to
ms-deployment's validate_changeset, and separately warns on any resource-typed
dependency validate_changeset accepts (the bound instance exists) but
apply_changeset would reject (the instance does not produce the required
resource type). That second check otherwise only runs at apply time, which is
exactly the gap a reviewer needs closed before merging a proposal.

--output md renders the same result as Markdown suitable for pasting directly
into a pull request body; --output json is the same data for a script to
consume.

Usage
~~~~~

.. code-block:: text

   nsctl env plan [name] [flags]

Examples
~~~~~~~~

.. code-block:: shell

   nsctl env plan
     nsctl env plan dev --output json
     nsctl env plan dev --output md > plan.md

Local flags
~~~~~~~~~~~

* ``--output`` — Output format: text, json or md (default: ``text``)

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl env purge
---------------

Tears an environment down and unregisters it. This is not a stop -- the k3s
cluster, the Postgres instance, the graph, their containers and volumes, the
nginx routes and the state directory all go, and none of it comes back.

With no name it purges every environment and the control plane with them,
leaving an HMD_HOME a fresh bootstrap can start from.

Resources Floci spawned are deleted through Floci before the control plane
stops, because a delete asked of a stopped Floci is a delete that did not
happen -- and its containers and volumes are then left behind with nothing
naming them.

Usage
~~~~~

.. code-block:: text

   nsctl env purge [name] [flags]

Local flags
~~~~~~~~~~~

* ``--yes`` — Skip the confirmation

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl env start
---------------

Starts one environment: its database, k3s cluster and routes.

The control plane is started first if it is down, and is left running
afterwards -- stopping it would take every other environment's emulated AWS
with it.

The graph is not in that list because it is provisioned on demand: it is
deployed the first time something declares a graph-db, neptune-db or neptune
dependency, and a default environment runs none. That keeps a gremlin JVM out
of every environment that never reads a graph. HMD_LOCAL_NEURONSPHERE_ENABLE_GRAPH=false
suppresses it even where something asks, leaving that dependency unresolved.

--substrate chooses how much of that infrastructure this environment runs and
records the choice in its manifest for every later start, apply and status:
  full   the database, k3s cluster and the core instances (the default)
  core   the database and the graph; no cluster
  none   nothing beyond the control plane -- for repo classes that deploy with
         their own toolset against infrastructure they already have

On an HMD_HOME with no environments registered this also registers the first
one, so a new install needs this command and nothing before it. Once any
environment exists a name that matches none of them is refused, because then it
is a typo rather than a first run; use `nsctl env add` to add another.

Usage
~~~~~

.. code-block:: text

   nsctl env start [name] [flags]

Local flags
~~~~~~~~~~~

* ``--force-full-redeploy`` — Deploy everything declared, ignoring what the graph says is already deployed
* ``--no-deploy`` — Bring up the infrastructure without reconciling the BOM
* ``--substrate`` — How much substrate to run: none, core or full (default: what the environment recorded, else full)
* ``-V, --verbose`` — Show the underlying command output

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl env status
----------------

Report one environment's containers and routes

Usage
~~~~~

.. code-block:: text

   nsctl env status [name]

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl env stop
--------------

Stops one environment. This is a stop, not a teardown: the k3s cluster is
stopped rather than deleted and its containers are stopped rather than removed,
so the next start restarts them in place and keeps the cluster's datastore.

The control plane keeps running.

Usage
~~~~~

.. code-block:: text

   nsctl env stop [name]

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl lock
----------

Reads a repository's deploy.dependencies and its local section and writes
neuronsphere.lock at the repository root -- a generated file the repository
checks in, so that a fresh clone stands up the same platform as the machine
that wrote it.

Resolving a version specifier to a concrete version is the hard part, so the
tiers are ordered by how certain they are:

  --from-env <env>  pin what is actually running. A known-good environment is
                    the only thing that has ever proved these versions work
                    together, which is how a lock should be born.
  --pin c@v         and any version_spec that is already an exact version.

A specifier neither settles is reported with both remedies rather than guessed
at. Resolving a range against a librarian is a separate mechanism; see NERD011.

Every profile's entries are pinned, not only the ones active now: activation is
a read-time filter, and a lock covering one profile would force a re-resolve the
first time anybody switched.

The lock never names an instance, only a repo class and the dependency roles it
fills. That is what lets two engineers who name their local instances
differently share one checked-in lock.

Usage
~~~~~

.. code-block:: text

   nsctl lock [path] [flags]

Examples
~~~~~~~~

.. code-block:: shell

   nsctl lock
     nsctl lock --from-env local
     nsctl lock --pin hmd-ms-transform@0.5.201
     nsctl lock --check

Local flags
~~~~~~~~~~~

* ``--check`` — Report whether the lock still covers the manifest, writing nothing
* ``--from-env`` — Pin the versions an environment is actually running
* ``--pin`` — Pin one repo class, as <repo-class>@<version>. Repeatable (default: ``[]``)
* ``--profile`` — profile in nsctl.toml whose endpoints to use
* ``--resolve`` — Pin each range to the newest published version: from the lock entry's OCI source, else the cloud librarian (NERD019 SPEC005)
* ``--url`` — cloud Artifact Librarian URL, overriding HMD_ARTIFACT_LIBRARIAN_URL

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl login
-----------

Signs in through the OAuth 2.0 device authorization grant, for the things
that are not local.

Nothing on this machine requires it: local environments, deploys, repositories
and stacks from a public registry namespace need no account, tenant or token.
Sign in to reach a hosted NeuronSphere tenant -- its Artifact Librarian,
published-version queries, cloud BOM inspection -- or a private registry.

nsctl prints a code and a URL; you open the URL in whatever browser you have,
on whatever machine you have, and type the code. Nothing binds a port on this
machine and nothing needs a browser on it, so this works over SSH and inside a
container.

The endpoint comes from a profile in $HMD_HOME/.config/nsctl.toml. With no
configuration and a terminal to answer, nsctl asks for the URL once and writes
the file; with no terminal it refuses and shows what to write.

The token is cached in $HMD_HOME/.cache/tokens.yaml, which is the same file the
Python `hmd login` writes and every HMD tool reads.

Usage
~~~~~

.. code-block:: text

   nsctl login [flags]

Examples
~~~~~~~~

.. code-block:: shell

   nsctl login
     nsctl login --profile acme
     nsctl login --auth-url https://auth.example-admin-neuronsphere.io/oauth2/ns --save
     nsctl login --no-browser

Local flags
~~~~~~~~~~~

* ``--auth-url`` — authorization server issuer, overriding the profile
* ``--force`` — sign in again even if the cached token is still valid
* ``--no-browser`` — print the URL without trying to open a browser
* ``--no-prompt`` — never ask for missing configuration; refuse instead
* ``--profile`` — profile in nsctl.toml to sign in with
* ``--save`` — write --auth-url to the profile before signing in
* ``--timeout`` — give up waiting for approval after this long (default: ``0s``)

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl logout
------------

Removes the token `nsctl login` cached. The configured endpoints are left alone.

Usage
~~~~~

.. code-block:: text

   nsctl logout

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl plugin
------------

A CLI plugin is an executable that adds one top-level noun to nsctl:
"nsctl <name> ..." runs "nsctl-<name>" with every argument after the noun.

A plugin runs because $HMD_HOME/.config/nsctl.toml declares it under
[plugin.<name>], and for no other reason; nothing on PATH or under the cache
is scanned. "install" fetches a published plugin from an OCI registry (a bare
name expands to ghcr.io/neuronsphere/plugins), unpacks this platform's
binary under $HMD_HOME/.cache/neuronsphere/plugins/, and writes the
declaration. A local build is declared by hand with "path = ..." instead.

Usage
~~~~~

.. code-block:: text

   nsctl plugin

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl plugin install
--------------------

Fetch a plugin from an OCI registry and declare it. <ref> is
<host>/<repository>[:<version>]; a bare name expands to
ghcr.io/neuronsphere/plugins/<name>. Without a version the newest published
one is installed and printed. Public namespaces need no credential; a private
one takes HMD_REGISTRY_TOKEN, or a profile whose registry_url matches the
host after "nsctl login".

Usage
~~~~~

.. code-block:: text

   nsctl plugin install <ref> [flags]

Examples
~~~~~~~~

.. code-block:: shell

   nsctl plugin install hello
     nsctl plugin install ghcr.io/acme/plugins/deploy:1.4.0
     nsctl plugin install hello --spec "~= 1.2"

Local flags
~~~~~~~~~~~

* ``--spec`` — a BACON version spec to choose the version by (e.g. "~= 1.2")

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl plugin list
-----------------

Read the [plugin.*] tables of nsctl.toml. Nothing else is consulted: a binary that is present but not declared is not a plugin.

Usage
~~~~~

.. code-block:: text

   nsctl plugin list [flags]

Local flags
~~~~~~~~~~~

* ``--json`` — Print JSON

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl plugin push
-----------------

Build the plugin artifact from <dir>, which holds plugin.json and one
<name>_<version>_<os>_<arch>.tar.gz per platform (GoReleaser's default
archive layout), and push it to <ref>. The tag is the descriptor's version
unless <ref> names one. A credential is required: --token, HMD_REGISTRY_TOKEN,
or a profile whose registry_url matches the host after "nsctl login".

Usage
~~~~~

.. code-block:: text

   nsctl plugin push <dir> <ref> [flags]

Examples
~~~~~~~~

.. code-block:: shell

   nsctl plugin push dist/ ghcr.io/acme/plugins/hello --token $GHCR_PAT

Local flags
~~~~~~~~~~~

* ``--token`` — registry token or PAT (overrides HMD_REGISTRY_TOKEN)

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl plugin remove
-------------------

Remove the [plugin.<name>] table from nsctl.toml and delete every installed version under the cache. A path declared for a dev build is never touched.

Usage
~~~~~

.. code-block:: text

   nsctl plugin remove <name>

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl plugin update
-------------------

Resolve the newest version of one declared plugin, or of every plugin
declared with a source when no name is given, and install it. A plugin
declared with only a path is left alone.

Usage
~~~~~

.. code-block:: text

   nsctl plugin update [<name>]

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl quickstart
----------------

Walks a new installation through what it needs, in order: the host checks,
where local state lives, a first environment, something deployed into it, and
your own repository.

Every step names the command it runs before running it, so the session is a
transcript you can repeat by hand, and every step can be declined. It creates
nothing the named commands would not create, and never purges, deletes or
redeploys.

It does not edit your shell configuration. When HMD_HOME is unset it proposes a
path, prints the export line for you to keep, and uses --home for the rest of
the run.

With stdin closed -- every CI job -- it prints the ordered list of commands and
runs nothing.

Usage
~~~~~

.. code-block:: text

   nsctl quickstart [flags]

Examples
~~~~~~~~

.. code-block:: shell

   nsctl quickstart
     nsctl quickstart --repo ~/src/my-service

Local flags
~~~~~~~~~~~

* ``--repo`` — A repository to offer adopting, instead of asking for one
* ``--yes`` — Take the default answer to every question

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl repo
----------

Edits the environment manifest at $HMD_HOME/environments/<env>.yaml.

These verbs are wrappers: they change that file and nothing else. Editing it
by hand is equivalent, and either way `nsctl env apply` is what deploys the
result -- so nothing here touches a running environment on its own.

Usage
~~~~~

.. code-block:: text

   nsctl repo

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl repo add
--------------

Adds a repo class to the environment's manifest.

The instance is named after the repo class with its hmd- prefix dropped unless
--name says otherwise, so `nsctl repo add hmd-ms-transform` declares an
instance called ms-transform.

Declaring is not deploying. Run `nsctl env apply` to deploy the result.

Usage
~~~~~

.. code-block:: text

   nsctl repo add <repo-class>[@<version>] [flags]

Examples
~~~~~~~~

.. code-block:: shell

   nsctl repo add hmd-ms-transform
     nsctl repo add hmd-ms-transform@0.3 --name transform
     nsctl repo add hmd-inf-trino --depends eks-cluster=eks-cluster --depends database-instance=environment-db
     nsctl repo add hmd-ms-myapi --path ~/work/hmd-ms-myapi --config replicas=2

Local flags
~~~~~~~~~~~

* ``--config`` — An instance configuration value as key=value; repeatable (default: ``[]``)
* ``--depends`` — A dependency as role=instance; repeatable (default: ``[]``)
* ``--env`` — Environment to declare it in (default: the default environment)
* ``--name`` — Instance name (default: the repo class without its hmd- prefix)
* ``--path`` — Working tree to deploy from (default: $HMD_REPO_HOME/<repo-class>)

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl repo import
-----------------

Reads what the deployment graph has for this environment and declares it.

This is the migration path from the Python CLI. An environment brought up by
`hmd neuronsphere up` gets its workloads from installed plugin packages, which
nsctl does not read -- so those instances show as "undeclared" until they are
written into a manifest. This writes them.

Substrate instances are skipped: nsctl deploys those whether or not a manifest
names them, and declaring one would make it removable by deleting a line.

Instances the manifest already declares are left exactly as they are, so
running this twice changes nothing the second time and a hand-edited
declaration is never overwritten.

Usage
~~~~~

.. code-block:: text

   nsctl repo import [flags]

Local flags
~~~~~~~~~~~

* ``--all`` — Import instances that are not currently deployed too
* ``--dry-run`` — Show what would be declared without writing
* ``--env`` — Environment to import from (default: the default environment)

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl repo list
---------------

Lists the environment manifest's declarations beside the deployment graph.

The substrate is listed too, marked as such: nsctl deploys it whether or not a
manifest exists, so seeing it here explains instances you never declared.

An instance shown as undeclared is deployed but named by no manifest -- what a
deleted line leaves behind.

DECLARED and FROM answer two different questions. DECLARED is where the
declaration came from; FROM is where the version came from, which the
declaration cannot say -- an instance deploying 0.1.4 out of an artifact and one
deploying 0.1.4 out of a checkout are both declared in the manifest.

Usage
~~~~~

.. code-block:: text

   nsctl repo list [flags]

Local flags
~~~~~~~~~~~

* ``--env`` — Environment to list (default: the default environment)

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl repo remove
-----------------

Removes an instance from the environment's manifest.

This edits the manifest only. What is already deployed stays deployed: nsctl
does not tear an instance down on your behalf.

Usage
~~~~~

.. code-block:: text

   nsctl repo remove <instance> [flags]

Aliases: ``rm``.

Local flags
~~~~~~~~~~~

* ``--env`` — Environment to edit (default: the default environment)

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl repoclass
---------------

Reads and writes the repo class manifest under --path (default: the current
directory): meta-data/manifest.json, or meta-data/manifest.toml and a repo-root
neuronsphere.toml for reading. Every write goes back to the file it was read
from with every key it did not touch intact, and prints one line naming the
file and the key it changed. Reads print a table, or JSON under --json.

None of these verbs needs HMD_HOME or a running platform.

Usage
~~~~~

.. code-block:: text

   nsctl repoclass

Examples
~~~~~~~~

.. code-block:: shell

   nsctl repoclass init acme-api --description "The Acme public API"
     nsctl repoclass build set-mechanism external
     nsctl repoclass deploy set-command exec make deploy
     nsctl repoclass deploy set-image acme/ci-tools:1
     nsctl repoclass deploy add-dependency warehouse --resource-namespace acme.com --resource-definition-name sql-warehouse --resource-version 0.1.0
     nsctl repoclass license set Apache-2.0 --exclude src/python
     nsctl repoclass validate
     nsctl repoclass describe --json

Local flags
~~~~~~~~~~~

* ``--path`` — The repo class's root directory (default: ``.``)

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl repoclass access
----------------------

Write, list or remove the manifest's "access": one entry per way in to a
deployed instance -- a URL, the user who logs in, and where the credential
lives. It never holds a credential: a manifest is a file a developer edits and
may commit, so an entry names a store and a key and "nsctl repoclass validate"
refuses a literal password.

A url, secret key or secret property may use the placeholders {instance_name},
{repo_class_name}, {deployment_id}, {environment} and {ingress_host}, which is
what makes one declaration work in every environment under any instance name.
"nsctl env credentials <env>" fills them in and resolves the secret.

Usage
~~~~~

.. code-block:: text

   nsctl repoclass access

Examples
~~~~~~~~

.. code-block:: shell

   nsctl repoclass access add superset --url 'http://{ingress_host}/' --username admin \
         --secret-store secrets-manager \
         --secret-key '{instance_name}-{deployment_id}-{environment}-admin-credentials' \
         --secret-property password
     nsctl repoclass access add api --url 'http://{ingress_host}/api/' --secret-store parameter-store --secret-output secret_name
     nsctl repoclass access list
     nsctl repoclass access remove superset

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)
* ``--path`` — The repo class's root directory (default: ``.``)

nsctl repoclass access add
--------------------------

Keyed and idempotent: re-running with the same name replaces that entry
wholesale rather than adding a second or merging halves of both. --secret-store
is required with any other --secret-* flag, and a secret names either a key or
the resource output to read its name from.

Usage
~~~~~

.. code-block:: text

   nsctl repoclass access add <name> [flags]

Local flags
~~~~~~~~~~~

* ``--notes`` — One line a reader needs that the fields do not carry
* ``--secret-key`` — The secret's name; may use placeholders
* ``--secret-output`` — A resource output key to read the secret's name from, instead of --secret-key
* ``--secret-property`` — The field inside a JSON secret, e.g. password
* ``--secret-store`` — Where the credential lives: secrets-manager or parameter-store
* ``--url`` — Where it is reached; may use placeholders (required)
* ``--username`` — The user who logs in, when there is a fixed one

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)
* ``--path`` — The repo class's root directory (default: ``.``)

nsctl repoclass access list
---------------------------

As written, not as resolved: a repository has no instance name and no
environment, so the placeholders stand. Use "nsctl env credentials <env>" to see
them filled in against a deployment.

Usage
~~~~~

.. code-block:: text

   nsctl repoclass access list [flags]

Local flags
~~~~~~~~~~~

* ``--json`` — Print the declaration as JSON

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)
* ``--path`` — The repo class's root directory (default: ``.``)

nsctl repoclass access remove
-----------------------------

Removing the last entry removes the "access" key, so a repo class that declares no front door does not carry an empty list saying so.

Usage
~~~~~

.. code-block:: text

   nsctl repoclass access remove <name>

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)
* ``--path`` — The repo class's root directory (default: ``.``)

nsctl repoclass build
---------------------

Edit the build section

Usage
~~~~~

.. code-block:: text

   nsctl repoclass build

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)
* ``--path`` — The repo class's root directory (default: ``.``)

nsctl repoclass build add-command
---------------------------------

Add or replace a tool in build.commands

Usage
~~~~~

.. code-block:: text

   nsctl repoclass build add-command <tool> [args...]

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)
* ``--path`` — The repo class's root directory (default: ``.``)

nsctl repoclass build remove-command
------------------------------------

Remove a tool from build.commands

Usage
~~~~~

.. code-block:: text

   nsctl repoclass build remove-command <tool>

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)
* ``--path`` — The repo class's root directory (default: ``.``)

nsctl repoclass build set-mechanism
-----------------------------------

Record whether the build is done by the tool set or by something else

Usage
~~~~~

.. code-block:: text

   nsctl repoclass build set-mechanism <tool_set|external>

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)
* ``--path`` — The repo class's root directory (default: ``.``)

nsctl repoclass deploy
----------------------

Edit the deploy section: command, image, dependencies, resources, configuration

Usage
~~~~~

.. code-block:: text

   nsctl repoclass deploy

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)
* ``--path`` — The repo class's root directory (default: ``.``)

nsctl repoclass deploy add-command
----------------------------------

Add or replace a tool in deploy.commands

Usage
~~~~~

.. code-block:: text

   nsctl repoclass deploy add-command <tool> [args...]

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)
* ``--path`` — The repo class's root directory (default: ``.``)

nsctl repoclass deploy add-dependency
-------------------------------------

Adds or replaces deploy.dependencies.<role>. --required is the default, as it
is for hmd manifest. A resource-typed dependency names what is needed rather
than which repo produces it, and resolves by type inheritance and tag
selector; --repo-class-name alongside it is a suggestion, not a pin.

Usage
~~~~~

.. code-block:: text

   nsctl repoclass deploy add-dependency <role> [flags]

Examples
~~~~~~~~

.. code-block:: shell

   nsctl repoclass deploy add-dependency warehouse --resource-namespace acme.com --resource-definition-name sql-warehouse --resource-version 0.1.0
     nsctl repoclass deploy add-dependency cluster --repo-class-name hmd-inf-eks-cluster --optional

Local flags
~~~~~~~~~~~

* ``--optional`` — The role may be left unfilled
* ``--repo-class-name`` — The repo class that fills the role (a suggestion when a resource is also named)
* ``--required`` — The role must be filled (the default)
* ``--resource-definition-name`` — Resource type name, e.g. postgres
* ``--resource-namespace`` — Resource type namespace, e.g. database.neuronsphere.io
* ``--resource-version`` — Resource type version, e.g. 0.1.0
* ``--resource-version-spec`` — Resource type version specifier
* ``--tag`` — Tag selector key=value (repeatable) (default: ``[]``)
* ``--version-spec`` — PEP 440 version specifier for the repo class

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)
* ``--path`` — The repo class's root directory (default: ``.``)

nsctl repoclass deploy add-resource
-----------------------------------

Declare a resource type this repo class relates to, under deploy.resources

Usage
~~~~~

.. code-block:: text

   nsctl repoclass deploy add-resource <name> [flags]

Local flags
~~~~~~~~~~~

* ``--description`` — What the resource is
* ``--no-produces`` — This repo class only consumes the resource
* ``--produces`` — This repo class produces the resource
* ``--resource-definition-name`` — Resource type name (required)
* ``--resource-namespace`` — Resource type namespace (required)
* ``--role`` — The role the resource fills
* ``--version`` — Resource type version (required)

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)
* ``--path`` — The repo class's root directory (default: ``.``)

nsctl repoclass deploy remove-command
-------------------------------------

Remove a tool from deploy.commands

Usage
~~~~~

.. code-block:: text

   nsctl repoclass deploy remove-command <tool>

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)
* ``--path`` — The repo class's root directory (default: ``.``)

nsctl repoclass deploy remove-dependency
----------------------------------------

Remove a dependency role

Usage
~~~~~

.. code-block:: text

   nsctl repoclass deploy remove-dependency <role>

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)
* ``--path`` — The repo class's root directory (default: ``.``)

nsctl repoclass deploy remove-resource
--------------------------------------

Remove a resource declaration

Usage
~~~~~

.. code-block:: text

   nsctl repoclass deploy remove-resource <name>

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)
* ``--path`` — The repo class's root directory (default: ``.``)

nsctl repoclass deploy set-command
----------------------------------

Sets deploy.commands to exactly one exec entry: the argv given, run
as-is in deploy.image with the repo at /workspace. Any tool set commands that
were there are replaced, and named.

Usage
~~~~~

.. code-block:: text

   nsctl repoclass deploy set-command exec <argv...>

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)
* ``--path`` — The repo class's root directory (default: ``.``)

nsctl repoclass deploy set-config
---------------------------------

Writes a dotted key under deploy.default_configuration. The value is typed
the way nsctl repo add --config types it: valid JSON is taken as JSON
(2 is a number, true a boolean, {"a":1} an object), anything else as the
literal string.

Usage
~~~~~

.. code-block:: text

   nsctl repoclass deploy set-config <dotted.key> <value>

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)
* ``--path`` — The repo class's root directory (default: ``.``)

nsctl repoclass deploy set-image
--------------------------------

Set the image the deploy command runs in

Usage
~~~~~

.. code-block:: text

   nsctl repoclass deploy set-image <ref>

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)
* ``--path`` — The repo class's root directory (default: ``.``)

nsctl repoclass deploy set-mechanism
------------------------------------

Record whether the deploy is done by the tool set or by something else

Usage
~~~~~

.. code-block:: text

   nsctl repoclass deploy set-mechanism <tool_set|external>

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)
* ``--path`` — The repo class's root directory (default: ``.``)

nsctl repoclass deploy unset-config
-----------------------------------

Remove a default configuration value

Usage
~~~~~

.. code-block:: text

   nsctl repoclass deploy unset-config <dotted.key>

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)
* ``--path`` — The repo class's root directory (default: ``.``)

nsctl repoclass describe
------------------------

The summary hmd describe prints -- name, description, discovery, resources,
dependencies -- as a table, or as the same JSON document under --json. Each
dependency also carries its resource block, and a test section is reported
when present; both are additions the Python omits.

Usage
~~~~~

.. code-block:: text

   nsctl repoclass describe [flags]

Local flags
~~~~~~~~~~~

* ``--json`` — Print the summary as JSON

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)
* ``--path`` — The repo class's root directory (default: ``.``)

nsctl repoclass detect
----------------------

Inspects the repository and prints a classification: what deploys it, what
image it runs in, what could not be decided, and what will not be guessed --
each with the file and line the conclusion came from, so you can disagree with a
finding by opening its source.

With --apply it writes what is unambiguous: the name, a provisional description,
the build mechanism, and the deploy command with its image. Without --apply it
writes nothing. When nothing in the repository states what it is in one line,
--apply refuses and asks for --description: BACON requires one, and a manifest
without it cannot validate.

It will not write a dependency, a resource, or any discovery metadata -- not even
a provisional one. Those need the environment's vocabulary and your judgement: a
dependency role marked required that is wrong fails the entire ChangeSet, naming
only the role. The "refused" rows say so explicitly, so their absence from the
manifest is a statement rather than an omission.

Usage
~~~~~

.. code-block:: text

   nsctl repoclass detect [flags]

Examples
~~~~~~~~

.. code-block:: shell

   nsctl repoclass detect
     nsctl repoclass detect --path ~/src/my-service --json
     nsctl repoclass detect --apply

Local flags
~~~~~~~~~~~

* ``--apply`` — Write what is unambiguous
* ``--description`` — The one-line description, overriding a detected one and required when none was detected
* ``--json`` — Print the classification as JSON
* ``--quiet`` — With --apply, report only what was written; for a caller that has already shown the classification

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)
* ``--path`` — The repo class's root directory (default: ``.``)

nsctl repoclass discovery
-------------------------

Edit the discovery section: what the repo class can do, for an agent reading it

Usage
~~~~~

.. code-block:: text

   nsctl repoclass discovery

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)
* ``--path`` — The repo class's root directory (default: ``.``)

nsctl repoclass discovery add-capability
----------------------------------------

Add or replace a capability, keyed by name

Usage
~~~~~

.. code-block:: text

   nsctl repoclass discovery add-capability <name> [flags]

Local flags
~~~~~~~~~~~

* ``--description`` — What the capability does (required)
* ``--kind`` — One of endpoint, cli_command, function, class, operation (required)
* ``--location`` — Where it is implemented

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)
* ``--path`` — The repo class's root directory (default: ``.``)

nsctl repoclass discovery add-entry-point
-----------------------------------------

Add or replace an entry point, keyed by path

Usage
~~~~~

.. code-block:: text

   nsctl repoclass discovery add-entry-point <path> [flags]

Local flags
~~~~~~~~~~~

* ``--description`` — What is at this path (required)

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)
* ``--path`` — The repo class's root directory (default: ``.``)

nsctl repoclass discovery add-related-doc
-----------------------------------------

Add or replace a related document, keyed by path

Usage
~~~~~

.. code-block:: text

   nsctl repoclass discovery add-related-doc <title> <path>

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)
* ``--path`` — The repo class's root directory (default: ``.``)

nsctl repoclass discovery set-summary
-------------------------------------

Set the one-paragraph summary

Usage
~~~~~

.. code-block:: text

   nsctl repoclass discovery set-summary <text>

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)
* ``--path`` — The repo class's root directory (default: ``.``)

nsctl repoclass init
--------------------

Writes name, description and an empty build section -- the three members the
BACON schema requires -- to meta-data/manifest.json, and meta-data/VERSION as
0.1 when there is none. Refuses when a manifest already exists.

Usage
~~~~~

.. code-block:: text

   nsctl repoclass init <name> [flags]

Local flags
~~~~~~~~~~~

* ``--at`` — Where to put it: meta-data (root not implemented yet) (default: ``meta-data``)
* ``--description`` — The repo class's one-line description
* ``--format`` — Manifest format: json (toml not implemented yet) (default: ``json``)

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)
* ``--path`` — The repo class's root directory (default: ``.``)

nsctl repoclass license
-----------------------

Write or remove the manifest's "license": the SPDX expression of what nsctl
publishes from this tree, and the root-relative paths that stay out of every
zip it makes from it ("nsctl artifact push <dir>", "nsctl stack build/push",
"nsctl stack init --bundle-local", "nsctl artifact register <dir>"). Every
published layer is annotated org.opencontainers.image.licenses with the
expression. A repo class that declares nothing is published whole and
unannotated; nsctl never infers a licence and never refuses one.

Usage
~~~~~

.. code-block:: text

   nsctl repoclass license

Examples
~~~~~~~~

.. code-block:: shell

   nsctl repoclass license set MIT
     nsctl repoclass license set Apache-2.0 --exclude src/python --exclude src/docker --exclude src/typescript
     nsctl repoclass license clear

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)
* ``--path`` — The repo class's root directory (default: ``.``)

nsctl repoclass license clear
-----------------------------

Remove the declaration

Usage
~~~~~

.. code-block:: text

   nsctl repoclass license clear

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)
* ``--path`` — The repo class's root directory (default: ``.``)

nsctl repoclass license set
---------------------------

Replace the declaration. With no --exclude the shorthand form is written
("license": "<spdx>"); with one or more, the object form with the cleaned
list. An exclude is a path relative to the repository root, matched on whole
segments (src/python covers src/python/app.py, not src/pythonic).

Usage
~~~~~

.. code-block:: text

   nsctl repoclass license set <spdx> [flags]

Local flags
~~~~~~~~~~~

* ``--exclude`` — a root-relative path kept out of every published zip (repeatable) (default: ``[]``)

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)
* ``--path`` — The repo class's root directory (default: ``.``)

nsctl repoclass local
---------------------

The "local" section (NERD010) declares what a repository -- or a stack --
wants beside it locally: companions to start, and how each dependency role
under deploy.dependencies is filled. "add" declares a companion; "bind" fills
a role with an instance the environment substrate provides; "require" marks a
role external, to be filled by another stack's instance and matched by
resource; "set-default-profiles" chooses what is on by default. Run
"nsctl lock" afterwards to pin what was declared.

Usage
~~~~~

.. code-block:: text

   nsctl repoclass local

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)
* ``--path`` — The repo class's root directory (default: ``.``)

nsctl repoclass local add
-------------------------

Declare a companion to start alongside this repository

Usage
~~~~~

.. code-block:: text

   nsctl repoclass local add <repo-class> [flags]

Examples
~~~~~~~~

.. code-block:: shell

   nsctl repoclass local add hmd-inf-otel-collector --spec "~= 0.1" --name otel
     nsctl repoclass local add hmd-inf-clickhouse --spec "== 0.3.12" --name clickhouse --profile full --depends sink=bucket

Local flags
~~~~~~~~~~~

* ``--depends`` — role=instance the companion depends on (repeatable) (default: ``[]``)
* ``--name`` — instance name (default: the class without its hmd- prefix)
* ``--profile`` — profiles that activate it (none = unconditional) (default: ``[]``)
* ``--spec`` — BACON version spec, e.g. "~= 0.1" or "== 0.1.5"

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)
* ``--path`` — The repo class's root directory (default: ``.``)

nsctl repoclass local bind
--------------------------

Writes local.dependencies.<role>.bind. The substrate's instances --
local-neuronsphere for compute, environment-db for a database -- are the
usual targets. Nothing is declared or pinned for a bound role.

Usage
~~~~~

.. code-block:: text

   nsctl repoclass local bind <role> <instance>

Examples
~~~~~~~~

.. code-block:: shell

   nsctl repoclass local bind compute local-neuronsphere

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)
* ``--path`` — The repo class's root directory (default: ``.``)

nsctl repoclass local list
--------------------------

Show every want the local section declares and how it is filled

Usage
~~~~~

.. code-block:: text

   nsctl repoclass local list

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)
* ``--path`` — The repo class's root directory (default: ``.``)

nsctl repoclass local remove
----------------------------

Remove a companion

Usage
~~~~~

.. code-block:: text

   nsctl repoclass local remove <instance>

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)
* ``--path`` — The repo class's root directory (default: ``.``)

nsctl repoclass local require
-----------------------------

Writes local.dependencies.<role> with external: true. Nothing is pinned or
bundled for the role; "nsctl stack add" binds it to an instance in the
environment that produces the role's resource type, or refuses naming
--suggest. Declare the role first with "deploy add-dependency", with a
resource type, so the match is by what is needed rather than by name.

Usage
~~~~~

.. code-block:: text

   nsctl repoclass local require <role> [flags]

Examples
~~~~~~~~

.. code-block:: shell

   nsctl repoclass local require warehouse-bucket --suggest ghcr.io/neuronsphere/stacks/storage

Local flags
~~~~~~~~~~~

* ``--suggest`` — a stack reference that would satisfy the role

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)
* ``--path`` — The repo class's root directory (default: ``.``)

nsctl repoclass local set-default-profiles
------------------------------------------

Set which profiles are on by default (none for lean)

Usage
~~~~~

.. code-block:: text

   nsctl repoclass local set-default-profiles [<profile>,...]

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)
* ``--path`` — The repo class's root directory (default: ``.``)

nsctl repoclass test
--------------------

Edit the test section

Usage
~~~~~

.. code-block:: text

   nsctl repoclass test

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)
* ``--path`` — The repo class's root directory (default: ``.``)

nsctl repoclass test add-command
--------------------------------

Add or replace a tool in test.commands

Usage
~~~~~

.. code-block:: text

   nsctl repoclass test add-command <tool> [args...]

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)
* ``--path`` — The repo class's root directory (default: ``.``)

nsctl repoclass test remove-command
-----------------------------------

Remove a tool from test.commands

Usage
~~~~~

.. code-block:: text

   nsctl repoclass test remove-command <tool>

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)
* ``--path`` — The repo class's root directory (default: ``.``)

nsctl repoclass test set-command
--------------------------------

Sets test.commands to exactly one exec entry: the argv given, run
as-is in deploy.image with the repo at /workspace. Any tool set commands that
were there are replaced, and named.

Usage
~~~~~

.. code-block:: text

   nsctl repoclass test set-command exec <argv...>

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)
* ``--path`` — The repo class's root directory (default: ``.``)

nsctl repoclass validate
------------------------

Runs a structural pass against the BACON schema and a semantic pass against
the rules that actually predict a failed deploy, reporting findings at three
severities: an error will fail a deploy, a warning will surprise you, a note is
inert. Advisory by default -- the exit code is 1 only on errors -- and --strict
promotes warnings to errors. Never rewrites the file.

Usage
~~~~~

.. code-block:: text

   nsctl repoclass validate [flags]

Local flags
~~~~~~~~~~~

* ``--json`` — Print the findings as JSON
* ``--strict`` — Treat warnings as errors

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)
* ``--path`` — The repo class's root directory (default: ``.``)

nsctl stack
-----------

A stack is a RepoClass whose repository declares the companions it needs (a
"local" section and a neuronsphere.lock) published as one artifact in an OCI
registry, holding the build zip of every RepoClass the lock pins. "stack add"
fetches it -- anonymously from a public namespace, no tenant needed -- unpacks
every zip into the artifact cache, and declares the instances in an
environment manifest the way "env add --from-repo" would from a checkout.

Declaring is not deploying: run "nsctl env apply" afterwards, or pass --apply.
nsctl carries no list of stacks; a bare name expands to
ghcr.io/neuronsphere/stacks/<name> and the expansion is printed.

Usage
~~~~~

.. code-block:: text

   nsctl stack

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl stack add
---------------

Fetch a stack from an OCI registry, verify and cache every RepoClass it
pins, and declare them -- and the stack itself -- in the environment manifest
with the same rules "env add --from-repo" applies to a checkout: --profile,
--all-profiles, --lean and --name mean what they mean there. The stack keeps
its own bindings in the manifest's "stacks" record, so it can share an
environment with a --from-repo repository or another stack.

<ref> is <host>/<repository>[:<version>]; a bare name expands to
ghcr.io/neuronsphere/stacks/<name>. No version means the newest, which is
printed. A public namespace needs no credential.

Nothing is deployed until "nsctl env apply <env>"; --apply runs it.

Usage
~~~~~

.. code-block:: text

   nsctl stack add <ref> [flags]

Examples
~~~~~~~~

.. code-block:: shell

   nsctl stack add observability
     nsctl stack add observability --env dev --apply
     nsctl stack add ghcr.io/acme/stacks/warehouse:0.4.0 --profile full --name warehouse-db=db

Local flags
~~~~~~~~~~~

* ``--all-profiles`` — Activate every profile the stack's lock mentions
* ``--apply`` — Run `nsctl env apply` afterwards
* ``--env`` — Environment to declare it in (default: the default environment)
* ``--lean`` — Activate no profiles: the stack and its unconditional entries alone
* ``--local-url`` — the control plane's Artifact Librarian (default: ``http://localhost/hmd_ms_artifact_lib/``)
* ``--name`` — Name one instance, as <role-or-declared-name>=<instance>. Repeatable (default: ``[]``)
* ``--profile`` — Local profiles to activate. Repeatable, or comma-separated (default: ``[]``)
* ``--spec`` — a BACON version spec to choose the version by (e.g. "~= 0.1")
* ``-V, --verbose`` — With --apply, show the underlying command output

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl stack build
-----------------

Read the repository's "local" section and neuronsphere.lock, obtain every
pinned build zip, and write the stack artifact as an OCI image layout under
--out (default build/stack). The same lock and bytes produce the same layout,
so a build in CI reproduces a build on a laptop, and nothing is pushed.

Each pinned zip comes from the first of: --artifacts <dir>; the artifact
cache (what an environment this stack was derived from deployed from); the
lock entry's "source", an OCI reference published with "nsctl artifact push"
(no tenant needed); your organisation's cloud Artifact Librarian (only
with a tenant credential). The tier that served each entry is printed, and
so is the licence each layer's manifest declares (NERD017 SPEC011): nsctl
records what an author declared and refuses nothing on its account.

Usage
~~~~~

.. code-block:: text

   nsctl stack build [<repo-dir>] [flags]

Examples
~~~~~~~~

.. code-block:: shell

   nsctl stack build
     nsctl stack build ~/src/hmd-stack-obs --out dist/stack --artifacts ./release

Local flags
~~~~~~~~~~~

* ``--artifacts`` — directory holding <class>_<version>_build.zip for pinned companions
* ``--out`` — where to write the OCI image layout (relative to the repository) (default: ``build/stack``)
* ``--profile`` — profile in nsctl.toml whose endpoints to use
* ``--tag`` — the stack version to record (default: meta-data/VERSION)
* ``--url`` — cloud Artifact Librarian URL, overriding HMD_ARTIFACT_LIBRARIAN_URL

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl stack init
----------------

Write a stack RepoClass: meta-data/manifest.json with a no-op deploy and a
"local" section, meta-data/VERSION, and the CI workflow that builds and
publishes it (.github/workflows/stack.yml).

With --from-env <env> or --from-bom <file> (a "nsctl bom show --json" export),
the "local" section is derived: the walk starts at the --select instances and
follows their dependencies, classifying each instance reached. The substrate
becomes a bound role; an instance another stack declared becomes an external
role with a suggestion (unless --include-provided); everything else is
bundled as a companion pinned to its running version, each selected root in
its own profile. An instance deployed from a working tree has no published
artifact and is refused unless --bundle-local names it. Configuration is
copied from the environment manifest's declarations only, and values that
look tied to this machine are listed for review.

--dry-run prints the classification and writes nothing. --update rewrites the
"local" section and dependencies of an existing manifest (the refresh job);
--diff only reports whether they would change, exiting 1 when they would.
--from-env also writes meta-data/reference-bom.json so CI can re-derive.

Usage
~~~~~

.. code-block:: text

   nsctl stack init <name> [flags]

Examples
~~~~~~~~

.. code-block:: shell

   nsctl stack init hmd-stack-analytics
     nsctl stack init hmd-stack-analytics --from-env local --select superset,trino,airflow --dry-run
     nsctl stack init hmd-stack-analytics --from-env local --select superset,trino,airflow
     nsctl stack init hmd-stack-analytics --from-bom meta-data/reference-bom.json --select trino --update

Local flags
~~~~~~~~~~~

* ``--bundle-local`` — working-tree instances to bundle rather than refuse (default: ``[]``)
* ``--description`` — manifest description
* ``--diff`` — report whether an existing manifest's local section would change; exit 1 when it would
* ``--dry-run`` — print the classification and write nothing
* ``--env`` — with --from-bom, the environment whose manifest supplies sources, configuration and stack records
* ``--from-bom`` — derive from a BOM export (nsctl bom show --json)
* ``--from-env`` — derive from a running environment's graph (writes the reference BOM)
* ``--include-provided`` — bundle instances other stacks declared rather than referencing those stacks
* ``--path`` — directory to write into (default: ./<name>)
* ``--select`` — instances to derive from; their dependencies follow. Repeatable, or comma-separated (default: ``[]``)
* ``--update`` — rewrite an existing manifest's local section and dependencies

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl stack list
----------------

Read the "stacks" records of the environment manifest: name, version, reference, digest and the instances each bound.

Usage
~~~~~

.. code-block:: text

   nsctl stack list [flags]

Local flags
~~~~~~~~~~~

* ``--env`` — Environment to list (default: the default environment)
* ``--json`` — Print JSON

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl stack pull
----------------

The fetch half of "stack add": verify and cache every RepoClass the stack
pins, and nothing else. For preparing a machine that will be offline, or a CI
job warming a cache.

Usage
~~~~~

.. code-block:: text

   nsctl stack pull <ref> [flags]

Local flags
~~~~~~~~~~~

* ``--local-url`` — the control plane's Artifact Librarian (default: ``http://localhost/hmd_ms_artifact_lib/``)
* ``--spec`` — a BACON version spec to choose the version by

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl stack push
----------------

Push a stack artifact to an OCI registry. With --from, push the OCI image
layout "nsctl stack build" wrote -- the CI path: build, inspect, then push.
Without it, build from the repository first (the laptop path), taking pinned
zips from --artifacts, the artifact cache, each lock entry's "source", or the
cloud Artifact Librarian, in that order.

The tag is the reference's, or --tag, or with --bump the next patch of the
newest version the registry already holds (meta-data/VERSION.0 when it holds
none or VERSION is a newer major.minor). A tag the registry already has is
refused: a published version is immutable. A credential is required:
--token, HMD_REGISTRY_TOKEN (GITHUB_TOKEN works for ghcr.io within the
repository's owner), or a profile's registry_url after "nsctl login".

Usage
~~~~~

.. code-block:: text

   nsctl stack push [<repo-dir>] <ref> [--from build/stack] [--bump] [flags]

Examples
~~~~~~~~

.. code-block:: shell

   nsctl stack build && nsctl stack push ghcr.io/acme/stacks/obs --from build/stack --bump
     nsctl stack push . ghcr.io/neuronsphere/stacks/observability:0.1.0 --token $GHCR_PAT
     nsctl stack push ~/src/hmd-stack-obs ghcr.io/acme/stacks/obs --artifacts ./dist --update-lock

Local flags
~~~~~~~~~~~

* ``--artifacts`` — directory holding <class>_<version>_build.zip for every pinned companion
* ``--bump`` — tag with the next patch of the newest published version
* ``--from`` — push the OCI image layout `nsctl stack build` wrote at this path
* ``--profile`` — profile in nsctl.toml whose endpoints to use
* ``--tag`` — tag to publish under
* ``--token`` — registry token or PAT (overrides HMD_REGISTRY_TOKEN)
* ``--update-lock`` — write the zips' digests back into the repository's lock
* ``--url`` — cloud Artifact Librarian URL, overriding HMD_ARTIFACT_LIBRARIAN_URL

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl stack remove
------------------

Remove the instances a stack bound -- and only those; an instance provided
by the substrate, or declared by another stack or by hand, is not the stack's
to remove -- and delete its record from the environment manifest. Nothing is
torn down: the next "nsctl env apply" reconciles. The artifact cache is kept
unless --prune-cache.

Usage
~~~~~

.. code-block:: text

   nsctl stack remove <name> [flags]

Local flags
~~~~~~~~~~~

* ``--env`` — Environment to edit (default: the default environment)
* ``--prune-cache`` — Also delete the stack's artifacts from the cache

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl stack versions
--------------------

List the version-shaped tags a registry holds for a stack, newest first, and
with --spec the one a BACON version spec would choose. Results are cached
under $HMD_HOME/.cache/neuronsphere/versions/; --offline reads the cache only.

Usage
~~~~~

.. code-block:: text

   nsctl stack versions <ref> [flags]

Local flags
~~~~~~~~~~~

* ``--offline`` — read the cache only
* ``--spec`` — report which version a BACON version spec would choose

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl version
-------------

Print the nsctl version

Usage
~~~~~

.. code-block:: text

   nsctl version

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

nsctl whoami
------------

Prints who the cached token says you are.

The token is decoded, not verified -- this reports what the credential carries,
it does not decide anything. Run `nsctl login` to replace an expired one.

Nothing local reads this. Local environments, deploys and repositories need no
credential at all; a token is for a hosted NeuronSphere tenant or a private
registry.

Usage
~~~~~

.. code-block:: text

   nsctl whoami [flags]

Local flags
~~~~~~~~~~~

* ``--show-claims`` — print the full claim set as JSON

Inherited flags
~~~~~~~~~~~~~~~

* ``--home`` — Path to HMD_HOME (overrides $HMD_HOME)

