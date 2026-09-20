nsctl -- the Go CLI
===================

``nsctl`` runs the local NeuronSphere as a single Go binary whose only host
prerequisite is Docker. It is an alternative front end to the same local
platform ``hmd neuronsphere`` drives, not a replacement for the ``hmd`` CLI:
both read and write the same environment registry, address the same
containers, and can be used against the same ``HMD_HOME``.

``nsctl`` is the recommended way to run the local platform, and the only one
that needs nothing but Docker. It is not a superset of ``hmd neuronsphere``:
see `What it does not do yet`_ before switching a project over. See
:doc:`proposals/NERD002_Go_CLI_Port` for the design and for what each phase
delivers.

Installing
----------

**Homebrew** (macOS):

.. code-block:: shell

   brew install neuronsphere/tap/nsctl

**Install script** (macOS and Linux) -- ``darwin`` and ``linux`` on both
``amd64`` and ``arm64``:

.. code-block:: shell

   curl -fsSL https://raw.githubusercontent.com/neuronsphere/hmd-cli-neuronsphere/main/install.sh | sh

It installs to ``~/.local/bin`` by default; set ``PREFIX`` for anywhere else,
and ``NSCTL_VERSION`` to pin a version rather than take the latest release. It
verifies the download against the release's ``checksums.txt`` and refuses to
install on a mismatch.

**GitHub Releases** -- the same archives, at
https://github.com/neuronsphere/hmd-cli-neuronsphere/releases, if you would rather
unpack one yourself.

**From a working tree**, which is what you want while changing ``nsctl``:

.. code-block:: shell

   make install          # builds and copies to ~/.local/bin
   make install PREFIX=/usr/local/bin

``make uninstall`` removes it.

There is no Windows build. ``nsctl`` drives Docker by shelling out to the CLI,
mounts ``/var/run/docker.sock``, and composes host absolute paths that sibling
containers have to resolve identically; none of that has a Windows equivalent.
Under WSL2 it is an ordinary ``linux/amd64`` binary and the install script
works unchanged.

Which version you are running
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

``nsctl version`` prints two different kinds of version depending on how the
binary got there, and the difference is informative rather than a bug:

.. code-block:: shell

   $ nsctl version
   nsctl 0.5
   hmd-ms-deployment-core 0.1 (http://localhost/hmd_ms_deployment)

- ``0.5`` -- two components, no patch -- is ``meta-data/VERSION``, the BACON
  MAJOR.MINOR this repository builds under. Only ``make install`` and
  ``make build`` produce it.
- ``0.5.0`` -- three components -- comes from a ``v``-prefixed release tag, and
  means Homebrew, the install script or a downloaded release.

The repository carries both tag spaces on one history: ``0.5.202``-style tags
belong to the BACON build pipeline and release nothing, while ``v0.5.0`` is
what triggers a release. GoReleaser is configured to ignore the former.

The second line appears only when a control plane is answering, and reports
what *it* is running rather than what ``nsctl`` is -- the two version themselves
independently. With no platform up, ``version`` prints one line and returns
immediately.

What it does not do yet
-----------------------

``nsctl`` and ``hmd neuronsphere`` work against the same ``HMD_HOME``, so
nothing here is a one-way door -- reach for the Python CLI for any of it and
carry on:

- **The parity harness covers the core and no more.**
  ``test/nsctl_parity.robot`` checks ``env start``, ``env stop``, ``status``,
  ``env purge`` and the registry from both front ends, in both directions. Every
  other verb is still checked by hand, so one that has not been exercised that
  way has not been proven equivalent. One test inside it is opt-in and unproven:
  reading an environment ``hmd neuronsphere up`` created, because that verb
  deploys its whole default BOM and took over 45 minutes where
  ``nsctl env start`` took three and a half. Run it with
  ``make test-parity NSCTL_PARITY_ENV=<slug> ROBOT_FLAGS=... -v DEEP:True`` and
  give it an hour.

- **``nsctl env purge`` and ``hmd neuronsphere down --purge --env`` are not the
  same operation.** Both destroy an environment's cluster, database, graph and
  state. ``nsctl`` also unregisters it; the Python keeps the registration, so
  another ``up`` brings it back. Neither is wrong -- but a script that swaps one
  for the other will surprise someone.

- **An environment not named** ``local`` **needs a recent projectbuilder.**
  ``hmd-lib-cdktf`` used to decide S3 path-style addressing by comparing the
  environment's *name*, while ``hmd-ms-deployment`` passes the environment's own
  slug as ``hmd deploy --environment`` -- so any other name failed its first
  CDKTF node in ``tofu init``. Fixed there on 2026-09-08 by asking whether
  ``AWS_ENDPOINT_URL`` is set instead, but the fix reaches a deploy through the
  projectbuilder image, so a stale one still fails. ``nsctl`` warns at
  ``env add`` and again before the deploy.
- **Only one control plane runs at a time per machine.** The five control-plane
  services pin ``container_name``, so those names are global while the network,
  compose project and Floci data directory are namespaced per ``HMD_HOME``. Two
  homes can take turns -- ``nsctl control-plane stop --home <the other one>``
  releases the names -- but not both be up. Running them concurrently is
  :doc:`NERD007 <proposals/NERD007_Concurrent_Control_Planes>`.
- **It detects a PostgreSQL major-version bump; it does not migrate one.** Floci
  reuses an RDS instance's volume across starts, so a data directory written by
  an older major makes the new binary refuse it. ``nsctl`` compares the
  configured image's ``PG_MAJOR`` against each volume before Floci starts and
  names both remedies; the migration itself is ``hmd neuronsphere db upgrade``,
  which works because both front ends address the same ``HMD_HOME``. See
  :doc:`environments`.
- **Plugin-contributed workloads are invisible until imported.** Anything an
  installed plugin package contributed to an environment brought up by ``hmd``
  needs ``nsctl repo import`` once. See `Using it alongside the Python CLI`_.
- **Deploys are ordered and run by the CLI**, against the deployment service's
  registry-and-resolver core. See `How env start orders deploys`_.

What it ships
-------------

``nsctl`` ships **the control plane and the environment substrate, and nothing
above them**:

- **The control plane**, one per ``HMD_HOME``: the nginx proxy, Floci, the
  Deployment GUI, and the foundation services ``hmd-ms-naming``,
  ``hmd-ms-artifact-lib`` and ``hmd-ms-deployment`` -- the last of which runs the
  ``hmd-ms-deployment-core`` image (see `Core and premium`_).
- **The environment substrate**, per environment: the core instance, its
  Postgres, its k3s cluster and ``hmd-ms-dbaccount`` -- as much of it as the
  environment asks for (`Choosing a substrate`_).

A new environment is therefore *empty but usable* -- a cluster, a database, and
the External Secrets operator, with nothing else deployed on them. The operator
is there because a cloud chart that renders an ``ExternalSecret`` fails without
it, so an environment lacking one is not usable in the way this sentence
claims. Everything else -- Airflow, Argo, Trino, Superset, transform, and your
own services -- is a RepoClass you add.

Choosing a substrate
~~~~~~~~~~~~~~~~~~~~

Not every environment needs all of that. A repo that deploys with its own
toolset against infrastructure it already runs (`Deploying a repo with its
own toolset`_) needs none of it, and three services that share a database
need no cluster. ``nsctl env start --substrate <mode>`` picks one of three
and records the choice in the environment manifest, where every later
``env start``, ``env apply``, ``env status`` and ``repo list`` reads it:

.. list-table::
   :header-rows: 1
   :widths: 10 45 45

   * - mode
     - runs
     - deploys in the first changeset
   * - ``full``
     - the environment database, graph, k3s cluster with its operators and
       ingress, and ``hmd-ms-dbaccount``. The default.
     - ``local-neuronsphere``, ``base-vpc``, ``environment-db``,
       ``eks-cluster``, then the External Secrets operator
   * - ``core``
     - the database, graph and ``hmd-ms-dbaccount``; no cluster
     - ``local-neuronsphere``, ``base-vpc``, ``environment-db``
   * - ``none``
     - nothing beyond the control plane and the Floci account
     - nothing -- the manifest's instances are the whole apply

Every mode still deploys the manifest's instances through ms-deployment;
what changes is what they can depend on. Under ``none`` there is no
``local-neuronsphere``, so a dependency bound to it (the stub ``env add
--from-repo`` supplies for a required name-only role) is refused at start,
naming the binding and ``--substrate core`` as the fix. Raising the mode later
adds what is missing and redeploys nothing; lowering it never destroys
anything -- ``env stop`` stops what is running and ``env purge`` is the
teardown. The mode is NERD014's.

This is the main difference from ``hmd neuronsphere up``, which resolves its
workloads from whichever plugin packages happen to be pip-installed.

Where the images come from
~~~~~~~~~~~~~~~~~~~~~~~~~~

Every image default names ``ghcr.io/hmdlabs`` and an explicit patch version.
``ghcr.io/neuronsphere`` was a mirror carrying only the released subset, and it
is stale: two of the three references Floci spawns its backends from, and the
projectbuilder every deploy node runs in, existed under no tag there at all. A
machine with nothing set -- which is every fresh install -- could bring up
neither a graph nor a cluster, and failed the bootstrap's first node.

``HMD_LOCAL_NS_CONTAINER_REGISTRY`` still overrides all of them at once, for a
mirror that carries everything. The versions are patch pins rather than a
floating tag: ``hmdlabs`` publishes a moving ``latest`` and no ``stable``, and a
floating default makes "what was this install built against" unanswerable.

Pulls go through ``docker pull`` rather than the Engine API. The daemon does not
read ``~/.docker/config.json`` -- resolving credentials is the CLI's job -- so an
Engine-API pull carries none, and ``ghcr.io`` answers a credential-less request
with ``unauthorized`` even for a public image.

Commands
--------

.. list-table::
   :header-rows: 1
   :widths: 40 60

   * - Command
     - What it does
   * - ``nsctl control-plane start|stop|status``
     - The shared control plane. ``stop`` refuses while an environment is
       running unless ``--force``.
   * - ``nsctl control-plane apply``
     - Converge the running control plane to its extension manifest, without
       the rest of a start. ``start`` applies too.
   * - ``nsctl control-plane repo add|remove|list``
     - Declare what the control plane runs alongside itself. See ``NERD004``.
   * - ``nsctl env add <name>``
     - Register a *further* environment, allocating its account and port slot.
       The first one needs no ``add`` -- see ``env start``. With ``--from-repo``
       it also declares what a repository says it needs; see
       `Repo-rooted environments`_.
   * - ``nsctl env start [name]``
     - Start an environment: database, graph, cluster, routes, then apply.
       Starts the control plane first if it is down. On an ``HMD_HOME`` with
       nothing registered it registers the first environment itself, so a new
       install needs this command and nothing before it. Once any environment
       exists a name matching none of them is refused -- then it is a typo, not
       a first run -- and refused immediately, before the control plane starts,
       rather than after a bootstrap it would have to pay for first.
       ``--substrate none|core|full`` chooses how much infrastructure the
       environment runs and records it (`Choosing a substrate`_); a bad value
       is refused the same early way.
   * - ``nsctl env apply [name]``
     - Reconcile an environment to its manifest without restarting it.
       ``--from-repo`` re-reads a repository's lock first.
   * - ``nsctl env plan [name] [--output text|json|md]``
     - Preview what ``env apply`` would add or change, without applying it.
       See `Previewing a change with env plan`_.
   * - ``nsctl env stop [name]``
     - Stop it, leaving its state in place.
   * - ``nsctl env list`` / ``env status [name]``
     - What is registered, and what is running.
   * - ``nsctl env delete <name>``
     - Unregister it. Refuses while its containers are running.
   * - ``nsctl env purge [name]``
     - Destroy it: cluster, database, graph, containers, volumes, routes and
       state. With no name, every environment and the control plane.
   * - ``nsctl repo add <class>[@<version>]``
     - Declare a repo instance in the environment's manifest.
   * - ``nsctl repo remove <instance>``
     - Undeclare it. Does not tear down what is deployed.
   * - ``nsctl repo list``
     - What the manifest declares, beside what the graph has deployed.
   * - ``nsctl repo import``
     - Write the environment's deployed instances into its manifest.
   * - ``nsctl repoclass init <name>``
     - Write the minimum viable repo class manifest under ``--path``. See
       `Authoring the repo class manifest`_. No verb in this group needs an
       ``HMD_HOME``.
   * - ``nsctl repoclass describe [--json]``
     - The normalized summary ``hmd describe`` prints, as a table or JSON.
   * - ``nsctl repoclass validate [--strict]``
     - The schema and the rules that predict a failed deploy, at three
       severities; exit 1 on errors, or on warnings under ``--strict``.
   * - ``nsctl repoclass build | deploy | test | discovery ...``
     - The write verbs: ``set-mechanism``, ``add-command``,
       ``set-command exec``, ``set-image``, ``add-dependency``,
       ``add-resource``, ``set-config``, ``set-summary``, ``add-capability``
       and their inverses. Each prints one line naming the file and key.
   * - ``nsctl bom envs`` / ``bom show <env>``
     - What a cloud ``hmd-ms-deployment`` is running. See
       `Cloud environments`_.
   * - ``nsctl bom import <env>``
     - Copy part of a cloud environment's BOM down and declare it here.
       ``--apply`` deploys it in the same breath.
   * - ``nsctl lock``
     - Pin what a repository needs locally into a checked-in
       ``neuronsphere.lock``. ``--check`` validates it and writes nothing.
   * - ``nsctl login``
     - Sign in with the device authorization grant. See `Signing in`_.
   * - ``nsctl logout`` / ``nsctl whoami``
     - Discard the cached credential; show what it says about you.

``--home`` overrides ``HMD_HOME`` on any command. Exit codes are ``0`` success,
``1`` error, ``2`` invalid usage or refused precondition, ``3`` refused because
something is in use, ``4`` a deploy node failed.

Signing in
----------

``nsctl login`` authenticates through the **OAuth 2.0 device authorization
grant** (RFC 8628). It prints a code and a URL; you open the URL in whatever
browser you have, on whatever machine you have, and type the code:

.. code-block:: console

    $ nsctl login

      Open:  https://auth-aaa-us-west-2.acme-admin-neuronsphere.io/oauth2/ns/v1/device
      Code:  MYWM-PPFD

    Waiting for you to finish signing in...
    Signed in as alice, until Mon, 14 Sep 2026 19:00:37 EDT.

Nothing binds a port on this machine and nothing needs a browser on it, so this
works over SSH and inside a container -- which the Python ``hmd login`` cannot
do, since it runs a server on ``localhost:8082`` and opens a browser at it.
``--no-browser`` skips the attempt to open one; the URL is printed either way.

Where the endpoint comes from
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

``$HMD_HOME/.config/nsctl.toml``, one table per profile:

.. code-block:: toml

    default_profile = "acme"

    [profile.acme]
    auth_url = "https://auth-aaa-us-west-2.acme-admin-neuronsphere.io/oauth2/ns"

``auth_url`` is the only required key, and it is the **issuer** rather than a
hostname: discovery is fetched from ``{auth_url}/.well-known/...``, and an
authorization server lives at a path. It names your identity provider's own
issuer -- an Okta authorization server, an Auth0 tenant, or the local mock.

A profile may also name the tenant's **services**, which is what lets
``--profile`` select where an artifact is fetched from:

.. code-block:: toml

    [profile.acme]
    auth_url      = "https://auth-aaa-reg1.acme-admin-neuronsphere.io/oauth2/ns"
    customer_code = "acme"
    region        = "reg1"

``customer_code`` and ``region`` are usually all you need: both service
hostnames are composed from them, the same way the Python tools compose them.
``artifact_librarian_url`` and ``deployment_url`` name them outright for a
deployment that does not follow that convention.

Every address resolves by one rule, in order: the command's ``--url``, then the
service's environment variable (``HMD_ARTIFACT_LIBRARIAN_URL``,
``HMD_CLOUD_MS_DEPLOYMENT_URL``), then the profile's explicit URL, then
``customer_code``/``region`` -- the profile's before ``HMD_CUSTOMER_CODE`` and
``HMD_REGION`` -- and then a refusal naming all four. ``nsctl`` never guesses an
endpoint. If an environment variable overrides an address the profile you named
also carries, it still wins and says so.

``client_id`` is optional here and needed in practice against a real provider,
which registers its own applications and rejects a client it does not know. It
is not a secret: the device grant is a public-client flow, and there is no
client secret to configure anywhere.

With no configuration and a terminal to answer, ``nsctl login`` asks for the URL
once and writes the file. With no terminal -- in CI, or under
``< /dev/null`` -- it refuses with exit ``2`` and prints the two lines to write.
A file that exists and does not parse is an error naming the line, and is never
silently replaced.

A build may also carry a default endpoint, linked in at build time and appearing
as a profile named ``neuronsphere``; where one is present, ``nsctl login`` needs
no configuration at all, and a ``[profile.neuronsphere]`` table in your own file
replaces it. No published build sets one yet.

``--profile`` selects one; ``--auth-url`` overrides the file for a single run,
and with ``--save`` writes the profile, so a scripted install can pass the URL
once and never author TOML.

Staying signed in
~~~~~~~~~~~~~~~~~

Profiles request ``offline_access`` by default, so the server issues a refresh
token. ``nsctl login`` against a credential that is still valid says so and does
nothing; against an expired one it renews silently and never opens a browser.
Only when there is no refresh token, or the server rejects it, is a sign-in
needed again.

Where the token goes
~~~~~~~~~~~~~~~~~~~~

``$HMD_HOME/.cache/tokens.yaml``, at mode ``0600`` -- the same file the Python
``hmd login`` writes and every HMD tool reads, so signing in with either front
end authenticates both. The new fields (``refresh_token``, ``expires_at``,
``issuer``, ``profile``) sit inside the existing ``login:`` mapping, where
readers that want only ``login.access_token`` ignore them.

Trying it against the local mock
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

``nsctl authd`` implements the grant, so the whole flow runs with no cloud and
no credentials:

.. code-block:: console

    $ export HMD_LOCAL_NEURONSPHERE_ENABLE_AUTH=true
    $ nsctl control-plane start
    $ printf '[profile.local]\nauth_url = "http://auth.local.neuronsphere.io/oauth2/ns"\n' \
        > "$HMD_HOME/.config/nsctl.toml"
    $ nsctl login --profile local

The sign-in page asks which groups the token should carry, so a policy can be
exercised against a realistic claim set. It signs with a key it generated and
will mint a token with any claims asked of it: it is not an authorization server
anyone should trust. See ``NERD008``.

The control-plane manifest
--------------------------

``$HMD_HOME/.config/control-plane.yaml`` declares RepoClasses that run beside
the control plane rather than inside an environment: they outlive every
``env purge``, are up whenever the control plane is, and there is one of each
per ``HMD_HOME``. Absent the file nothing extra runs.

.. code-block:: yaml

    version: 1
    name: control-plane
    repos:
      - instance_name: package-registry
        repo_class_name: hmd-inf-local-registry
        version: "0.1"
        instance_configuration:
          url: http://registry.local.neuronsphere.io
          upstream: server:3141
          pypi: {enabled: true}

The RepoClass contributes ``src/local/docker-compose.extension.yml``, whose
services join the control plane's own compose project. It may not pin
``container_name``, publish a host port or use a named volume;
``instance_configuration`` reaches it as ``${NS_CONFIG_*}`` variables and as
compose profiles. An extension that fails is reported and skipped -- nothing
else is affected, and ``start`` still succeeds. ``NERD004`` is the
specification.

The environment manifest
------------------------

An environment's desired state lives at
``$HMD_HOME/environments/<slug>.yaml``, in the same format
:doc:`environments` documents for the Python CLI:

.. code-block:: yaml

   version: 1
   name: dev2
   repos:
     - instance_name: my-api
       repo_class_name: hmd-ms-myapi
       version: "0.3"
       instance_configuration:
         replicas: 2
       dependencies:
         eks-cluster: eks-cluster
         database-instance: environment-db

The manifest is the source of truth. ``nsctl repo add`` and ``repo remove``
edit this file and nothing else, and ``nsctl env apply`` deploys the result --
so editing it by hand and running ``env apply`` is exactly the same operation.

One more top-level key is ``nsctl``'s own: ``substrate: none|core|full``,
written by ``nsctl env start --substrate`` and read by everything after
(`Choosing a substrate`_). Absent means ``full``. The Python CLI ignores it
and preserves it.

Two keys are honoured by the Python CLI and ignored by ``nsctl``: ``plugins``
and ``plugin_config`` configure plugin discovery, which ``nsctl`` does not
implement. It preserves both when it rewrites the file and warns that they had
no effect, rather than silently obeying or silently dropping them.

A typical session
-----------------

.. code-block:: shell

   nsctl env start scratch              # control plane, then the substrate

   nsctl repo add hmd-ms-myapi \
       --env scratch \
       --depends eks-cluster=eks-cluster \
       --config replicas=2
   nsctl env apply scratch              # deploys it

   nsctl repo list --env scratch        # declared vs deployed
   nsctl env stop scratch

Reconciling
-----------

``env apply`` deploys only what is new or has changed. It compares the desired
definition against two things: the deployment graph, which says what is
deployed, and a snapshot at ``<state_dir>/applied-changeset.json``, which
records what each instance was deployed *from*. An instance that is deployed
and matches its snapshot is left alone.

Every uncertain case errs towards deploying rather than skipping:

- If the deployment graph cannot be read, everything is deployed, because
  nothing is known.
- If there is no snapshot, deployed instances are left alone -- no snapshot
  means no information, not "everything changed".
- Only instances that a run settled, or that were already found current, get a
  recorded digest.

An instance that is deployed but no longer declared is **reported and left
running**. Removing a line from a manifest is not an instruction to destroy
what it deployed; tear it down yourself if that is what you meant.

``--force-full-redeploy`` skips the comparison entirely.

Previewing a change with env plan
----------------------------------

``nsctl env plan [name] [--output text|json|md]`` builds exactly what
``env apply`` would build -- the same reconcile diff above, the same
class-version and resource-type catalog registrations -- and stops there: it
never calls ``apply_changeset`` and never runs a node.

Beyond the diff, it POSTs the would-be ChangeSet definition to
hmd-ms-deployment's ``validate_changeset``, which catches a missing required
role, an unresolved dependency, or a cycle. And because ``validate_changeset``
only checks that a resource-typed dependency's bound instance *exists* -- not
that it produces the required resource type, which is checked only at
``apply_changeset`` time -- ``env plan`` separately calls
``suggest_resource_dependencies`` per resource-typed role and warns on any
bound instance that is neither a satisfying producer nor an instance of the
role's suggested RepoClass. A bound instance that is itself new in the same
plan cannot be a candidate yet -- the service only knows instances that already
have a deployment -- so it is judged by what its class declares it produces
(``meta-data/resources/*.yaml``, own type and parent), which is what
``apply_changeset`` validates against once the producer's deployment record
exists ahead of the consumer's. That is the gap between "this looks fine" and
"this would actually deploy," closed before a reviewer merges it rather than
after a failed ``env apply``.

``--output md`` renders the same result as Markdown suitable for pasting
directly into a pull request body -- a proposal in the NeuronSphere sense is a
PR carrying a new or changed RepoClass, the ``environments/<name>.yaml`` diff
``nsctl repo add`` produced, and this output. ``--output json`` is the same
data for a script to consume.

Extending the control plane
---------------------------

An environment manifest deploys into one environment's cluster. Some things
should not: a package registry has to outlive ``env purge``, be reachable when
no environment is running, and exist once per machine rather than once per
environment slot.

Those are declared instead at ``$HMD_HOME/.config/control-plane.yaml``, in the
same schema:

.. code-block:: yaml

   version: 1
   name: control-plane
   repos:
     - instance_name: package-registry
       repo_class_name: hmd-inf-local-registry
       version: "0.1.4"
       source: {type: artifact}
       instance_configuration:
         pypi: {enabled: true}

``.config/`` rather than ``environments/`` because this is authored
configuration alongside ``hmd.env`` and ``uv.toml``, and because it is not
``.cache`` -- ``env purge`` never touches it. A missing file is not an error:
the control plane starts exactly as it does today and deploys nothing extra,
which is the point.

``nsctl control-plane repo add|remove|list`` edit the file and
``control-plane apply`` deploys the result, mirroring the environment verbs.
Editing by hand and applying is the same operation.

An extension is reached by name, not by port -- ``hmd_proxy`` is the only
container permitted to publish a host port. ``control-plane start`` prints the
``/etc/hosts`` line to add, exactly as the identity provider needs one.

An extension may also contribute environment variables *back*, so nothing has
to be told about it twice:

.. code-block:: yaml

   instance_configuration:
     url: http://registry.local.neuronsphere.io
     handback:
       - name: PYTHON_REGISTRIES
         merge: json-map
         key: neuronsphere
         value:
           url: "${NS_CONFIG_URL}/hmd/local/+simple/"
           publish: true

These land in a delimited block at the end of ``$HMD_HOME/.config/hmd.env``,
rewritten whole on every start and every apply -- so a variable belonging to an
extension you no longer declare goes away by itself. Everything outside the
block is left untouched, comments and all.

**A value you set yourself wins, and what that means depends on the shape.**
For a plain value nsctl writes nothing at all and leaves yours as the only
assignment. For a JSON map like ``PYTHON_REGISTRIES`` it merges its entry in
beside yours and yields to a key of the same name, because replacing the value
would delete the JFrog index you configured. For a delimited list like
``GOPROXY`` your entries stay first and it appends, once.

``control-plane status`` shows what the block carries and what it yielded to,
and a handback never carries a credential -- ``NERD004`` SPEC010.

Artifacts
---------

A RepoClass normally resolves to a working tree under ``$HMD_REPO_HOME``. It
can instead resolve to a versioned artifact held by the control plane's
Artifact Librarian, which is how an extension is distributed to someone who
wants to run it rather than develop it::

   source: {type: artifact}

The version is required -- an artifact is addressed by version, so there is
nothing to infer one from.

.. code-block:: bash

   # from a cloud librarian into this machine's control plane
   nsctl artifact pull hmd-inf-local-registry@0.1.4

   # or from a local `hmd build`, run in the repo
   nsctl artifact register

Both write into the local librarian at
``http://localhost/hmd_ms_artifact_lib/``, which the control plane already
runs. Resolution unpacks to
``$HMD_HOME/.cache/neuronsphere/artifacts/<class>@<version>/`` and reuses it
thereafter, so an apply of an already-pulled version needs no network.

Resolution never reaches for the cloud on its own. A version that has not been
pulled fails naming every place it looked and the command that would fetch it,
rather than an apply behaving differently on an aeroplane.

A working tree still wins when you ask for it -- an explicit ``source.path``,
``=local``, or ``HMD_LOCAL_NEURONSPHERE_PREFER_LOCAL_VERSIONS`` -- and
``nsctl`` says so when it happens. It does **not** win by accident: an
artifact-sourced instance ignores an unrelated checkout that happens to be
sitting in ``$HMD_REPO_HOME``, because deploying that would report a version
it did not use. ``nsctl status`` reports the version and where it came from.

See ``NERD005``.

Repo-rooted environments
------------------------

A repository can declare the local environment it needs in order to be tested,
and ``nsctl`` can create that environment from it in one command. Two files, two
jobs -- the pairing every reader already knows from ``pyproject.toml`` beside
``uv.lock``.

**What the repository wants** goes in a ``local`` section of its BACON manifest,
by hand:

.. code-block:: json

   "local": {
     "version": 1,
     "default_profiles": ["transforms"],
     "repos": [
       {"instance_name": "cache", "repo_class_name": "hmd-inf-redis",
        "version_spec": "0.2.1"},
       {"instance_name": "ms-transform", "repo_class_name": "hmd-ms-transform",
        "version_spec": "~= 0.5", "profiles": ["transforms", "full"]}
     ],
     "dependencies": {"otel-collector": {"profiles": ["full", "telemetry"]}}
   }

``local.repos`` are **companions**: things that are not dependencies of this
repository but are what make testing it meaningful -- a Transform runtime that
calls its endpoints, a Librarian that Transform reads from. Its real
dependencies stay in ``deploy.dependencies`` and are not repeated here;
``local.dependencies`` says how a role is filled *here*, in one of three ways:

.. code-block:: json

   "dependencies": {
     "otel-collector": {"profiles": ["full", "telemetry"]},
     "compute":        {"bind": "local-neuronsphere"},
     "db-credentials": {"instance_configuration": {"db_name": "myapi"},
                        "dependencies": {"database-instance": "environment-db"}}
   }

``profiles`` gates an *optional* role. ``bind`` fills a role with an instance
the environment already provides -- the substrate's ``local-neuronsphere``, the
``ext-secrets`` the control plane deploys -- when the cloud fills it with a
class nothing local can stand in for; nothing is declared or pinned for it.
``dependencies`` and ``instance_configuration`` are what the *declared*
dependency instance itself needs -- a database account's ``database-instance``
and ``db_name`` -- which ``deploy.dependencies`` has nowhere to say. The last
two are allowed on a required role; ``profiles`` is not.

Profiles are ``docker compose``'s: an entry with a ``profiles`` key starts only
when one of them is activated, an entry without one always starts, and **lean
needs no declaration at all** -- it is what activating nothing gives you. A
*required* dependency cannot be gated, and saying so is a manifest error rather
than a silent no-op: ``ms-deployment`` fails the whole ChangeSet on an unmet
required role.

**What those wants resolve to** is generated:

.. code-block:: bash

   nsctl lock                      # from exact specifiers and --pin
   nsctl lock --from-env local     # or from what an environment is running
   nsctl lock --check              # in pre-commit and CI; writes nothing

``neuronsphere.lock`` goes at the repository root, in TOML, and is checked in.
It pins every profile's entries rather than only the active ones, because
activation is a read-time filter and a lock covering one profile would force a
re-resolve -- a network trip, and a different answer -- the first time anyone
switched.

Resolving a range needs a list of published versions, which is a separate
mechanism (see ``NERD011``). Until it exists, ``nsctl lock`` settles exact
specifiers, ``--pin`` and ``--from-env``, and reports anything else with both
remedies rather than guessing. ``--from-env`` is the one to reach for: a
known-good environment is the only thing that has ever proved a set of versions
works together.

**Standing it up:**

.. code-block:: bash

   nsctl env add dev --from-repo .            # fetches what is missing
   nsctl env start dev

   nsctl env apply dev --from-repo . --profile full   # offline; --pull to fetch

Every activated entry is declared at its pinned version as
``source: {type: artifact}``, and the repository itself is declared from its
working tree -- which is what makes it the thing under test. A want the
environment substrate already provides (``base-vpc``, ``eks-cluster``,
``environment-db``) is bound to but not declared, and says so.

``env add`` fetches what this machine does not hold, because that is first start
and there is no environment yet to have an offline expectation about.
``env apply`` does **not**: a missing artifact fails naming
``nsctl artifact pull``, and ``--pull`` fetches as a declared step. An apply that
reached the internet unasked would behave differently on an aeroplane.

**Naming.** The lock names repo classes and dependency *roles*, never instances.
That is deliberate: two engineers may run the same class at the same locked
version under different local instance names and must still resolve the same
roles from one checked-in lock. An instance is named, most specific first, by
what the environment manifest already bound, then ``--name``, then the
declaration's own ``instance_name``, then the dependency role::

   nsctl env add dev --from-repo . --name neptune-db=my-graph

The environment manifest records the activated ``profiles`` and a ``bindings``
map, so a later bare ``nsctl env apply`` reconciles to the same set under the
same names instead of re-defaulting. Renaming an instance undeclares the old
name; deactivating a profile leaves its instances declared unless ``--prune``.
Nothing is ever torn down on your behalf.

See ``NERD010``.

Cloud environments
------------------

A cloud ``dev`` or ``prod`` environment is a known-good version set -- somebody
is running it in earnest -- which makes it the strongest thing to seed a local
environment from. ``nsctl bom`` reads one and builds part of it here.

.. code-block:: console

    $ nsctl bom envs --profile acme
    ENVIRONMENT  ACCOUNT       REGION
    dev          123456789012  reg1
    prod         210987654321  reg1

    $ nsctl bom show dev
    dev is running 63 instances.

    INSTANCE      REPO CLASS           VERSION  STATUS    CACHED
    artifact-lib  hmd-ms-artifact-lib  0.4.12   deployed  no
    ms-transform  hmd-ms-transform     0.5.201  deployed  no
    trino         hmd-inf-trino        0.2.5    deployed  yes

``CACHED`` says whether this machine already holds that artifact, and is
answered from the cache alone -- ``show`` reaches no librarian.

**Cherry-picking.** ``--instance`` and ``--class`` are repeatable, and ``--all``
takes the lot; an empty selection is refused, because importing a
sixty-instance cloud environment is a thing to ask for out loud. ``show``
accepts the same flags, so it is a preview of the ``import`` that follows it:

.. code-block:: console

    $ nsctl bom import dev --instance ms-transform
    INSTANCE      REPO CLASS           VERSION  STATUS    CACHED  SELECTED
    artifact-lib  hmd-ms-artifact-lib  0.4.12   deployed  no      ms-transform needs it for librarian
    device-lib    hmd-ms-device-lib    0.4.3    deployed  no      ms-transform needs it for librarian
    ms-transform  hmd-ms-transform     0.5.201  deployed  no      asked for

    Fetching 3 artifacts from https://artifact-aaa-reg1.acme-admin-neuronsphere.io:
      hmd-ms-artifact-lib  0.4.12   ok
      hmd-ms-device-lib    0.4.3    ok
      hmd-ms-transform     0.5.201  ok

    Declared 3 instances in ~/hmd/environments/local.yaml
    Run `nsctl env apply local` to deploy it.

**The selection is closed under the roles the repo classes mark required.**
``hmd-ms-deployment`` fails a whole ChangeSet on a required role nothing fills,
so importing one instance without what fills those roles would produce a
manifest that cannot deploy. An **optional** role is not followed, and not
following one prunes whatever it reached in turn -- which is most of the
difference between an import you can deploy and one you cannot. Roles filled by
the environment substrate -- ``eks-cluster``, ``environment-db``, ``base-vpc``
-- are bound rather than imported, under this environment's name for them.

Whether a role is required is read from the repo class's own manifest, at the
version the BOM names, and that manifest arrives with the artifact. ``import``
fetches as it goes, so it knows; ``show`` reads only what is already unpacked
here unless you pass ``--resolve``, and says how many classes it therefore
could not read:

.. code-block:: console

    $ nsctl bom show dev --instance ms-transform --resolve
    dev is running 63 instances; this selects 14 of them.
    ...
    note: 19 optional roles not followed, so they were not imported (pass --with <role> to add one):
      ms-transform:otel              hmd-inf-otel-collector -> otel-main
      ms-transform:authorizer        hmd-inf-opa-authorizer -> opa

``--with <role>`` follows one anyway, by role name or repo class, and what it
reaches is then closed over by the ordinary rule. A ``--with`` that matches
nothing is refused, listing the optional roles there are.

**A required role nothing here fills** is bound to the core instance when only
its presence is validated, which is what ``hmd-ms-deployment`` checks for a role
that names a producer rather than a resource type. It is reported, loudly,
because the core instance does not deploy whatever was stubbed:

.. code-block:: text

    warning: 2 required roles bound to local-neuronsphere because nothing here fills them.
             Only the role's presence is validated, so the deploy will proceed -- but the
             instance below is NOT deployed, and anything that needs it at runtime will not find it.
             Pass --no-stub-roles to refuse instead.
      api-gateway:acm                wanted acm-main (hmd-inf-acm)

A role that asks for a **resource type** cannot be stubbed -- the producer is
validated against what it really produces -- so it is named as an error
instead. ``--no-stub-roles`` refuses in both cases.

``--exclude`` follows the same rule. Excluding an optional closure member is
ordinary; excluding one that fills a required *name-only* role is allowed, and
that role is bound as above; excluding one that fills a required resource-typed
role is refused, naming the type. This is how a cloud-only class that a required
role reaches -- an ACM certificate, a WAF, a private CA -- is left out on
purpose. ``--no-deps`` takes the selection literally instead.

**Narrowing a large closure by hand.** Sixty entries is small enough to read and
too large to narrow by typing flags, so write it out, edit it, and pass it back:

.. code-block:: console

    $ nsctl bom show dev --instance ms-transform --resolve --save-selection sel.toml
    Wrote sel.toml. Edit it, then: nsctl bom import dev --selection sel.toml

    $ head -12 sel.toml
    # nsctl bom selection, from the dev environment.
    # Flip `take` and pass this back with `nsctl bom import dev --selection <file>`.

    [[instance]]
    take       = true
    name       = "ms-transform"
    repo_class = "hmd-ms-transform"
    version    = "0.5.201"
    why        = "asked for"

    $ nsctl bom import dev --selection sel.toml

A ``take = true`` is taken as an explicit instance and a ``take = false`` as an
exclusion, so the ordinary rules apply to an edited file -- including the
refusal above. The file is parsed strictly: a misspelt key is an error naming
its line rather than a setting quietly ignored. It cannot be combined with
``--instance``, ``--class``, ``--all`` or ``--exclude``.

Each instance is declared at the cloud's version as ``source: {type: artifact}``
-- never from a checkout, because a version that came from a cloud librarian
must not silently resolve to whatever sits under ``$HMD_REPO_HOME``. The
artifacts are fetched **before** anything is written, so an import never leaves
a declaration that cannot deploy; one that cannot be fetched is named and that
instance is not declared. ``--dry-run`` writes and fetches nothing,
``--no-pull`` declares and prints the ``nsctl artifact pull`` lines instead.

Declaring is not deploying: ``nsctl env apply`` is still what deploys the
result. ``--apply`` runs it for you, once the selection is declared, so the
whole round-trip from a cloud BOM to a running local instance is one line:

.. code-block:: console

    $ nsctl bom import dev --instance ms-transform --env scratch --apply -V

It is the same apply ``nsctl env apply scratch`` would run and nothing more. It
is refused with ``--dry-run`` and ``--no-pull``, and an import that could not
fetch every artifact is declared but not applied -- the command exits non-zero
naming what is missing, as it always has, and leaves the apply to you.

Nothing here writes to a cloud service -- a BOM is an input to a local
manifest, never an output to a cloud deploy. See ``NERD012`` for reading a
cloud BOM and ``NERD013`` for which of its roles are followed.

The local identity provider
---------------------------

Nothing on the local platform has a token. Applications run with database
authentication, microservices run unauthenticated, and the Rego policies every
service authorizes against are only ever exercised in the cloud. ``nsctl
authd`` is a local stand-in for Okta that makes them testable here.

It is off by default. To use it:

.. code-block:: shell

   export HMD_LOCAL_NEURONSPHERE_ENABLE_AUTH=true
   nsctl control-plane start

Nothing else to build: the identity provider is this binary in a container
(``hmd-img-nsctl``), which ``control-plane start`` builds from sources embedded
in the binary the first time it is needed. ``HMD_NSCTL_IMAGE`` names a tag to
use instead, for iterating on it with ``make image``.

Minting a token
~~~~~~~~~~~~~~~

``nsctl authd token`` prints a signed JWT with whatever claims you name, and
works whether or not the control plane is running -- which is what makes it
usable from a test.

.. code-block:: shell

   nsctl authd token --server services --sub ms-deployment
   nsctl authd token --group 'NeuronSphere Superset Admin - Local (none)'
   nsctl authd token --claim tenant=acme --claim 'level=3' --show-claims

A ``--claim`` value that parses as JSON is used as JSON, so a policy reading a
number, a boolean or a list is testable without a second flag. ``--decode``
prints the claims of a token you already have.

The ``groups`` claim matters most. It decides a user's role in Superset and
Airflow and every policy decision about them, and the applications parse it by
shape::

    NeuronSphere <app> <role> - <Environment> (<customer code>)
    NeuronSphere Airflow Admin - Local (none)

Two authorization servers
~~~~~~~~~~~~~~~~~~~~~~~~~

``hmd-lib-auth`` already distinguishes a service account from a user, and the
policies check the difference, so there are two:

============================  ================================  ==============
Server                        Audience                          For
============================  ================================  ==============
``/oauth2/ns``                ``api://neuronsphere``            users, app SSO
``/oauth2/services``          ``api://neuronsphere-services``   service accounts
============================  ================================  ==============

Every Rego bundle's ``token_is_valid`` admits exactly those two audiences, so a
token minted for the wrong one is denied -- which is the thing worth being able
to try.

One issuer, three resolvers
~~~~~~~~~~~~~~~~~~~~~~~~~~~

The issuer is the base URL every consumer appends to, and it is stamped into
every token's ``iss``. It has to be **one string** wherever it is read: a
consumer that fetched the keys under one name and reads ``iss`` as another
rejects every token, and the failure shows up as a policy denial nowhere near
the cause.

So ``auth.local.neuronsphere.io`` is made to resolve to ``hmd_proxy`` three
ways -- an nginx vhost for the browser, a Docker network alias for Floci's
Lambda containers, and a CoreDNS record for cluster pods. Override it with
``HMD_LOCAL_AUTH_ISSUER`` if you must; the hostname the vhost, the alias and the
record use is derived from it, so they cannot drift apart.

Reaching it from a browser costs an ``/etc/hosts`` entry, exactly as the
environment UIs do::

    127.0.0.1 auth.local.neuronsphere.io

What it does not do
~~~~~~~~~~~~~~~~~~~

It signs with a key it generated and will mint a token with any claims asked of
it. It is not an authorization server anyone should trust, and it is not
reachable from outside this machine.

It also does not yet satisfy ``hmd-lib-auth.verify_token``, which the OPA
authorizer Lambda calls before it consults a policy: ``okta_jwt_verifier``
rejects any issuer that is not ``https://`` and offers no way to turn that off,
so the authorizer end to end still needs a certificate that nothing here
issues. Policy evaluation itself does not -- every Rego bundle decodes the
token with ``io.jwt.decode`` rather than verifying it, so realistic *claims*
are all a policy needs.

The local package registry
--------------------------

Not part of the control plane, and not carried by this binary. It is a
RepoClass -- ``hmd-inf-local-registry`` -- added the way any other control-plane
extension is, and it is the worked example of the two sections above:

.. code-block:: bash

   nsctl artifact pull hmd-inf-local-registry@0.1.4
   # declare it in $HMD_HOME/.config/control-plane.yaml, then
   nsctl control-plane start

It hosts internal packages, caches upstream, and becomes the index the platform
resolves through. Python is on by default; Docker/OCI, Go and npm are each
behind their own switch and off.

One hostname per format, each served at its root::

   http://registry.local.neuronsphere.io/hmd/local/+simple/   pip / uv

An extension declares one ``url`` and one ``upstream``, and the router emits a
server block with a single ``location /`` for it. There is no per-format path
prefix: adding route lists to ``cpext`` on behalf of one extension is the
coupling ``NERD004`` exists to avoid, so the formats that follow take sibling
hostnames rather than sibling paths. It also removes rather than solves devpi's
absolute-link rewriting.

Reaching it costs an ``/etc/hosts`` entry, as the identity provider does::

   127.0.0.1 registry.local.neuronsphere.io

Docker additionally needs it in ``insecure-registries``. The hostname has a
dot, so Docker treats it as a registry rather than a Docker Hub namespace, but
it will not speak plain HTTP to it unless told. Nothing here warrants TLS, for
the same reason the identity provider runs without it.

Nothing else to configure: ``control-plane start`` and ``control-plane apply``
write ``PYTHON_REGISTRIES``, ``TYPESCRIPT_REGISTRIES``,
``HMD_LOCAL_IMAGE_PULL_REGISTRIES`` and ``GOPROXY`` into
``$HMD_HOME/.config/hmd.env``, from which the existing tooling carries them into
``uv.toml``, ``.pypirc``, ``.npmrc``, deploy nodes and Docker builds. A value
you set yourself always wins -- see the handback below for what that means for
a variable holding a map or a list.

Credentials for private upstreams -- JFrog, ghcr, GitHub npm -- stay in the
host keychain. The manifest names where each lives; it never carries one, and
nothing writes one under ``$HMD_HOME``.

See ``NERD006``.

Deploying a repo with its own toolset
-------------------------------------

A repo class need not be shaped like a NeuronSphere repo to deploy locally.
One whose deploy is a ``make deploy``, a ``kubectl apply``, a dbt run or a
shell script its CI already runs can declare that command and the image its
CI runs it in, and ``nsctl`` runs exactly that -- no ``src/helm``, no
``src/local/deploy_local.sh``, no ``hmd`` in the container. This is NERD009's
foreign-node path, and the contract below is the product: a command that
expects something the contract does not give fails in a way ``nsctl`` cannot
explain, so it is written down here.

Such an environment usually wants no substrate at all: start it with
``nsctl env start --substrate none`` and the seven-minute cluster and database
bring-up becomes seconds (`Choosing a substrate`_).

**The declaration** is two keys in the repo class manifest:

.. code-block:: json

   {
     "name": "acme-api",
     "description": "The Acme public API",
     "build": {"mechanism": "external"},
     "deploy": {
       "commands": [["exec", "make", "deploy"]],
       "image": "ghcr.io/acme/ci:3.2",
       "dependencies": {
         "database": {"required": "true",
                      "resource": {"resource_namespace": "database.neuronsphere.io",
                                   "resource_definition_name": "postgres",
                                   "version": "0.1.0"}}
       }
     }
   }

That file is written by the verbs below rather than by hand; the block above
is seven commands (`Authoring the repo class manifest`_).

``exec`` is BACON's own spec-mandatory "run this command in the repo root"
tool; ``deploy.image`` is the one addition. Exactly one ``exec`` entry, and
nothing beside it: two, or an ``exec`` next to ``helm``, are refused rather
than half-run. Absent ``deploy.image`` the command runs in
``hmd-img-projectbuilder``, so a repo that declares ``exec`` and nothing else
behaves as it did before the key existed. Only a reference is supported --
``nsctl`` pulls it and builds nothing.

**Precedence.** An ``exec`` entry outranks ``src/local/deploy_local.sh``,
which outranks the generated ``hmd ... deploy`` command. A repo that has
written down its deploy command has said something more specific than a
conventional filename. Nothing under ``src/local`` applies to an ``exec``
node.

**The contract.** The container is started with the argv after the image,
verbatim -- ``docker run ... ghcr.io/acme/ci:3.2 make deploy`` -- so the
image's own entrypoint, if it has one, receives it. ``nsctl`` assumes
nothing about the image: no ``bash``, no ``hmd``, no ``python3``, no root
user. What it injects, and this is the whole list:

.. list-table::
   :header-rows: 1
   :widths: 30 70

   * - Injected
     - Meaning
   * - ``/workspace``
     - The repo class root, mounted read-write, and the working directory.
       A tree the binary carries, or one unpacked from an artifact, is
       copied first; a checkout declared with ``source.path`` is mounted as
       it is.
   * - ``/var/run/docker.sock``
     - The host's Docker daemon, for a toolset that builds images. It is a
       *sibling*: every bind mount asked of it resolves on the host, not
       inside this container.
   * - ``KUBECONFIG``
     - When the environment has a cluster: the kubeconfig, mounted
       read-only at ``/etc/nsctl/kubeconfig`` and named by the variable
       every Kubernetes tool reads. Nothing is mounted under ``/root``.
   * - ``AWS_ENDPOINT_URL``, ``AWS_ACCESS_KEY_ID``,
       ``AWS_SECRET_ACCESS_KEY``, ``AWS_DEFAULT_REGION``
     - Floci, with the access key selecting the environment's account.
   * - ``HMD_INSTANCE_NAME``, ``HMD_REPO_NAME``, ``HMD_REPO_VERSION``
     - The node's identity: the instance, its repo class, and the version
       the resolver registered it under.
   * - ``HMD_INSTANCE_CONFIG``
     - The resolved configuration as JSON -- see below.
   * - ``HMD_DID``, ``HMD_ENVIRONMENT``, ``HMD_CUSTOMER_CODE``,
       ``HMD_LOCAL_K3S_CLUSTER_NAME``
     - The deployment id, ``local``, the customer code, and the k3s cluster
       name, which is what an image import into the cluster is scoped by.
   * - The platform network and two labels
     - So the node reaches Floci and the environment's services by name,
       and so ``env purge`` can find a container an interrupted deploy left
       behind.

Not injected, because they exist only to satisfy ``hmd-cli-*``:
``HMD_HOME``, the dummy ``DOCKER_USERNAME``/``DOCKER_PASSWORD``,
``HMD_DEPLOYMENT_SERVICE_URL``, ``NS_LOCAL_PROXY``, and whatever
``hmd.env`` adds for the native tools.

**The configuration.** ``HMD_INSTANCE_CONFIG`` is the same document
``hmd deploy`` receives on the native path: the class's
``default_configuration`` merged with the instance's values, and under
``dependencies``, every role with the instance filling it and -- the part a
toolset actually wants -- ``hmd_resources``, the Resources that instance
produced, each with its ``resource_definition`` and ``output``. A dbt
project depending on a ``database`` role reads its host and port from
``dependencies.database.hmd_resources[0].output``. A dependency deployed in
the same changeset has no outputs when the configuration is generated, so
``nsctl`` resolves the pointer it carries instead
(``hmd_resource_ref``) immediately before the node runs, exactly as
``hmd deploy`` does; a foreign node may therefore depend on anything.

**Produced Resources** are collected the same way as for every other node:
each ``meta-data/resources_output/*.json`` the command leaves in the
workspace is submitted after it exits. ``nsctl`` creates that directory
before the node runs, so the command only has to write into it. A document
is the ``submit_resources`` shape -- ``resource_name``,
``resource_definition``, ``output``, ``tags`` -- and the type it names must
be declared under ``meta-data/resources/`` with ``produces: true``.

**What the contract does not give.** No git identity, no registry
credential, no ``~/.kube`` and no ``~/.aws`` -- a command that expects one
must be told otherwise in its own image or its own argv. The mounted
kubeconfig keeps the host file's permissions, which matters for an image
that runs as a non-root user. An image the command builds through the
socket lands in the *host* daemon, not in the k3s node, so a pod referencing
it fails with ``ImagePullBackOff`` until it is imported (NERD009 SPEC014,
not yet built). And this path is ``nsctl``'s: Python ``hmd deploy`` does not
implement ``exec`` yet, so the same manifest fails there with ``CLI command,
exec, not found`` (NERD009 SPEC012).

Authoring the repo class manifest
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

``nsctl repoclass`` is the BACON authoring group (NERD009 SPEC001): the
``hmd manifest`` and ``hmd describe`` verbs in Go, plus three that detection
needs and the Python does not have. Every verb takes a persistent ``--path``
(default ``.``) and none of them needs an ``HMD_HOME`` or a running platform
-- pointing the binary at a repository before installing anything is the
whole motion.

The manifest is read through one store (SPEC006) with one precedence:
``meta-data/manifest.toml``, then ``meta-data/manifest.json``, then a repo-root
``neuronsphere.toml``. The document is held as an ordered map, so every key
``nsctl`` does not model survives a rewrite and re-running a verb changes the
value and nothing else in the diff. JSON is written back in the layout
``hmd_lib_manifest.write_manifest`` uses (``json.dump(indent=2)``), so a file
edited by either front end does not churn under the other. A TOML tier is read
-- ``describe`` and ``validate`` work on it -- but every write refuses it with
the file named: a surgical key-path edit that leaves untouched text
byte-identical has no library behind it here, and a rewrite that drops the
user's comments is worse than a refusal.

The seven-command version of the example above, as the Demo 0 recording runs
it on a customer repo:

.. code-block:: console

   $ nsctl repoclass --path products/customer-revenue init acme-dp-customer-revenue \
       --description "Net revenue per customer per month"
   wrote meta-data/manifest.json name, description, build
   $ nsctl repoclass --path products/customer-revenue build set-mechanism external
   wrote meta-data/manifest.json build.mechanism
   $ nsctl repoclass --path products/customer-revenue deploy set-command exec acme-deploy
   wrote meta-data/manifest.json deploy.commands
   $ nsctl repoclass --path products/customer-revenue deploy set-image acme/ci-tools:1
   wrote meta-data/manifest.json deploy.image
   $ nsctl repoclass --path products/customer-revenue deploy add-dependency warehouse \
       --resource-namespace acme.com --resource-definition-name sql-warehouse --resource-version 0.1.0
   wrote meta-data/manifest.json deploy.dependencies.warehouse
   $ nsctl repoclass --path products/customer-revenue deploy set-config schedule "0 6 * * *"
   wrote meta-data/manifest.json deploy.default_configuration.schedule
   $ nsctl repoclass --path products/customer-revenue validate
   note: deploy.commands exec is nsctl's; hmd deploy does not implement it yet (NERD009 SPEC012)
   validate: ok -- 0 errors, 0 warnings, 1 note

The verbs, under ``nsctl repoclass [--path DIR]``:

- ``init <name> [--description T]`` writes ``name``, ``description`` and
  ``build: {}`` -- the three members the schema requires -- and
  ``meta-data/VERSION`` as ``0.1`` when there is none. It refuses when a
  manifest already exists.
- ``describe [--json]`` is the summary ``hmd describe`` prints, with two
  additions: each dependency carries its ``resource`` block, and a ``test``
  section is reported. The Python's output is a strict subset.
- ``validate [--strict] [--json]`` runs the schema's shape and NERD009
  SPEC011's rules. **Errors** will fail a deploy: a ``deploy`` with no
  command, an empty ``exec``, two of them, a native tool without its source
  directory, a required dependency naming nothing. **Warnings** will surprise:
  ``required`` as a JSON boolean, a ``VERSION`` that is absent or not
  ``MAJOR.MINOR``, a resources YAML without its identity, a name colliding with
  a bundled class or reserved instance. **Notes** are inert: ``exec`` under
  ``hmd deploy``, ``deploy.mechanism``. Advisory by default; ``--strict``
  promotes warnings. It never rewrites the file.
- ``build set-mechanism <tool_set|external>``, ``build add-command <tool>
  [args...]``, ``build remove-command <tool>``; the same three under
  ``deploy`` and ``test``.
- ``deploy set-command exec <argv...>`` and ``test set-command exec
  <argv...>`` make the phase exactly one ``exec`` entry, naming what they
  replaced. ``deploy set-image <ref>``.
- ``deploy add-dependency <role> [--repo-class-name] [--required|--optional]
  [--version-spec] [--resource-namespace] [--resource-definition-name]
  [--resource-version] [--resource-version-spec] [--tag K=V]`` and
  ``remove-dependency``; ``required`` is written as the string the schema's
  enum demands, and ``--required`` is the default because two front ends with
  the same verb and opposite defaults is worse than the footgun. ``deploy
  add-resource <name> --resource-namespace --resource-definition-name
  --version [--produces|--no-produces] [--role] [--description]`` and
  ``remove-resource``.
- ``deploy set-config <dotted.key> <value>`` and ``unset-config`` write under
  ``deploy.default_configuration`` with ``nsctl repo add --config``'s typing:
  valid JSON is JSON, anything else the literal string.
- ``discovery set-summary <text>``, ``add-entry-point <path> --description``,
  ``add-capability <name> --kind --description [--location]``,
  ``add-related-doc <title> <path>``; each ``add-*`` replaces the entry with
  the same key.

Not built: ``detect`` (SPEC009/010), TOML writes and the root location for
``init`` (SPEC006), and the BACON JSON Schema file
(SPEC013) -- ``validate``'s structural pass is hand-coded from the schema
document until one is shipped.

Agent Skills
------------

``nsctl agent skills`` installs bundled guidance for coding agents; it does
not configure an agent, an AI model, or an MCP server.  The default project
scope makes the installed skill visible to agents working in the selected
repository, and keeps it reviewable with that project:

.. code-block:: console

   $ nsctl agent skills list --path .
   $ nsctl agent skills install nsctl-onboard nsctl-local-environment --host all --path .
   installed /work/service/.agents/skills/nsctl-onboard
   installed /work/service/.claude/skills/nsctl-onboard
   ...

Codex skills install into ``.agents/skills/<skill>/SKILL.md`` and Claude Code
skills into ``.claude/skills/<skill>/SKILL.md``.  Pass ``--scope user`` to
install under the invoking user's corresponding home directory instead; it is
intended for broadly useful packs such as ``nsctl-onboard`` and ``nsctl-debug``.
``--path`` is valid only for project scope.

The initial pack is ``nsctl-onboard``, ``nsctl-local-environment``,
``nsctl-repoclass-author``, ``nsctl-local-plugin``, ``nsctl-debug``,
``nsctl-deploy-plan`` and ``nsctl-auth-and-profiles``.  ``list`` and ``doctor``
show each host target and its state.  ``install`` refuses to replace an
existing skill without ``--force`` and supports ``--dry-run``.  ``remove``
removes only an unmodified skill installed by ``nsctl`` unless ``--force`` is
given; it never removes an enclosing agent skills directory.

See :doc:`proposals/NERD015_Agent_Skills` for the command and safety contract.

How env start orders deploys
----------------------------

``env start`` (and ``env apply``) never asks the deployment service to run
anything. After it has registered the catalogue -- repo class versions, resource
definitions, produced-resource declarations, the Environment and DeploymentSet --
it does the rest itself:

1. **Orders the entries** topologically over the role bindings in the
   environment manifest. Every role, resource-typed or not, is bound to an
   instance name there (``nsctl bom select`` fills the resource-typed ones), so
   the manifest already holds every edge.
2. **Records the plan**: one ``register_deployed_instance`` per entry, in that
   order, with ``status: DEPLOY_NEXT`` and its ``dependencies``. The service
   creates the same RepoInstance, RepoInstanceDeployment and dependency edges a
   ChangeSet would, and refuses a dependency it cannot find -- the graph stays
   the service's to keep consistent.
3. **Fetches each node's configuration** with ``get_deployment_config``: class
   defaults, the instance's own configuration and, per dependency, that
   dependency's produced Resources (baked when it is already deployed, a
   ``hmd_resource_ref`` when it is in this batch and still to run).
4. **Renders and runs** the ``hmd deploy`` script per node in a projectbuilder
   container, independent nodes concurrently (``HMD_LOCAL_RUNNER_PARALLELISM``,
   default 4, the cloud path's ``spec.parallelism``; ``1`` makes a run
   sequential again). Each node reports ``set_deployment_status`` and submits its
   ``meta-data/resources_output`` when it finishes; a failure stops dispatch and
   lets in-flight nodes finish.

This is what lets the local control plane run the deployment service's **core**
image: the ChangeSet state machine, the deployment DAG and Argo execution are the
orchestrator's, and none of them is needed to bring up an environment.

Core and premium
~~~~~~~~~~~~~~~~

The deployment service is two RepoClasses (``hmd-ms-deployment`` NERD0015):
``hmd-ms-deployment-core`` -- registry, ResourceDefinition catalogue, resolver,
discovery search, RepoInstance records -- and ``hmd-ms-deployment``, the
orchestrator, whose image is ``FROM`` the core. ``nsctl`` runs the core image
under the ``hmd-ms-deployment`` service name: the Lambda is still
``hmd_ms_deployment``, the route ``/hmd_ms_deployment/``, and every consumer --
``hmd deploy --local``, the GUI, the proxy -- is unchanged. The version it runs is
the bundled descriptor's, pinned with ``HMD_LOCAL_VERSION_HMD_MS_DEPLOYMENT_CORE``
(or ``=local`` for a checkout). ``nsctl version`` reports it as
``hmd-ms-deployment-core``.

To run the **premium** image locally -- every core route plus the ChangeSet,
DAG, timeline and log routes, against the same local database -- name it in
full:

.. code-block:: shell

   export HMD_LOCAL_IMAGE_HMD_MS_DEPLOYMENT=ghcr.io/hmdlabs/hmd-ms-deployment:0.4.900
   export HMD_LOCAL_IMAGE_HMD_APP_NEURONSPHERE=ghcr.io/hmdlabs/hmd-app-neuronsphere:0.1.100
   nsctl control-plane start

``HMD_LOCAL_IMAGE_<SERVICE>`` is a full reference: it is pulled if absent (the
premium package needs a ``docker login`` to ghcr) and bypasses version resolution
entirely. Under the override the service's ``SERVICE_CONFIG`` comes from the
service-named class's own descriptor when a tree for it resolves (a checkout of
``hmd-ms-deployment`` under ``$HMD_REPO_HOME``, or ``HMD_LOCAL_VERSION_HMD_MS_DEPLOYMENT``),
so the premium ``operations_modules`` load; without one the core descriptor
applies and only the core routes answer. ``env start`` works unchanged against
it, because the premium image is a superset. What it does not give is Argo
execution: that still needs the cloud control plane.

Using it alongside the Python CLI
---------------------------------

Both front ends work against one ``HMD_HOME``. ``nsctl`` stamps the
``com.docker.compose.*`` labels on every container it creates, so
``docker compose ps``, ``hmd neuronsphere down`` and the Python port validator
all still find them.

Two notes on migrating from the Python CLI:

- **Plugin-contributed workloads.** Entries that come from installed
  plugin packages are invisible to ``nsctl`` until they are written into the
  environment's manifest. ``nsctl repo import`` does that: it reads what the
  deployment graph holds for the environment and declares it, after which
  ``env apply`` reports nothing deployed-but-undeclared. Run it once per
  environment you brought up with ``hmd``.

- **A RepoClass may not declare everything a plugin did.** ``nsctl`` reads a
  service's configuration from its ``manifest.json``, so anything declared
  only in a repo's ``src/local/nsplugin.json`` is invisible to it.
  ``hmd-ms-artifact-lib`` is the known instance: its bucket is declared only
  there, so ``nsctl`` bootstraps it, says that it will not serve until the
  bucket is declared in the manifest, and carries on. Everything else in the
  control plane is unaffected.
