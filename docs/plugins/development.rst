.. Extension Development

Extension Development
==========================================

.. include:: ../_includes/paid-cloud.txt

The local NeuronSphere is extended by **adding RepoClasses to a manifest**.
There is no plugin discovery: nothing is scanned, enumerated or
auto-registered, and an extension runs because a manifest names it and for no
other reason.

A RepoClass already declares what it is and what it needs, in its BACON
``meta-data/manifest.json`` and its NERD0004 ``meta-data/resources/*.yaml``.
``nsctl`` reads exactly those, because a second place to say the same thing is
the one that goes stale.

Two scopes
----------

Where an extension is declared decides its lifetime, not just its location.

.. list-table::
   :header-rows: 1
   :widths: 22 39 39

   * -
     - Environment
     - Control plane
   * - Manifest
     - ``$HMD_HOME/environments/<slug>.yaml``
     - ``$HMD_HOME/.config/control-plane.yaml``
   * - Deployed into
     - that environment's k3s cluster
     - alongside the control plane's containers
   * - Deployed by
     - a ChangeSet through ``hmd-ms-deployment``
     - merging ``src/local/docker-compose.extension.yml``
   * - Lifetime
     - removed by ``nsctl env purge``
     - survives every environment
   * - Cardinality
     - one per environment
     - one per ``HMD_HOME``
   * - Available when
     - that environment is running
     - the control plane is running
   * - Verbs
     - ``nsctl repo …`` / ``nsctl env apply``
     - ``nsctl control-plane repo …`` / ``control-plane apply``
   * - Specified by
     - ``docs/reference/commands.rst``
     - ``NERD004``

Most things are workloads and belong to an environment. Choose the control
plane when a thing must outlive an environment, must be reachable when none is
running, or should exist once per machine rather than once per environment
slot -- a package registry is all three, which is why ``NERD006`` is the
worked example.

Adding one
----------

.. code-block:: bash

    # to an environment
    nsctl repo add hmd-ms-myapi --env dev2 --name my-api \
        --depends eks-cluster=eks-cluster \
        --depends database-instance=environment-db
    nsctl env apply --env dev2

.. code-block:: bash

    # to the control plane
    nsctl control-plane repo add hmd-inf-local-registry --name package-registry \
        --config url=http://registry.local.neuronsphere.io \
        --config upstream=server:8080
    nsctl control-plane apply

Editing the manifest by hand and running ``apply`` is the same operation. The
imperative verbs are wrappers, not a second way to say the same thing.

.. code-block:: yaml

    # $HMD_HOME/environments/dev2.yaml
    version: 1
    name: dev2
    repos:
      - instance_name: my-api
        repo_class_name: hmd-ms-myapi
        version: "0.3"
        instance_configuration: {replicas: 2}
        dependencies:
          eks-cluster: eks-cluster
          database-instance: environment-db

Nothing is auto-wired: a declaration's dependencies are what the manifest says
and no more.

Where the code comes from
-------------------------

By default a RepoClass resolves to a working tree at
``$HMD_REPO_HOME/<repo_class_name>``, or to an explicit ``source.path``. That
is the development path and it stays the development path.

For distribution, ``NERD005`` adds a versioned artifact held by the control
plane's Artifact Librarian:

.. code-block:: yaml

    - instance_name: package-registry
      repo_class_name: hmd-inf-local-registry
      version: "0.1.4"
      source: {type: artifact}

.. code-block:: bash

    nsctl artifact pull hmd-inf-local-registry@0.1.4    # from a cloud librarian
    nsctl artifact register                             # or from a local hmd build

A consumer then needs a version number rather than a git checkout. A working
tree still wins when you ask for it -- via ``source.path``, ``=local``, or
``HMD_LOCAL_NEURONSPHERE_PREFER_LOCAL_VERSIONS`` -- so both paths coexist on
one machine, and ``nsctl status`` reports which one is live.

Superseded: the Python entrypoint plugin model
-----------------------------------------------

.. req:: Local NeuronSphere plugins via Python entrypoints
    :id: HMD_CLI_NEURONSPHERE_PLUGINS_ENTRYPOINTS
    :status: withdrawn

    The local NeuronSphere was to be extendable through the standard Python
    entrypoints specification. A plugin package registered functions against
    four entrypoints in ``hmd-cli-neuronsphere`` -- ``enabled``,
    ``get_resources``, ``prepare_hmd_home`` and ``render_compose_yaml`` --
    which the CLI discovered by iterating installed packages and called during
    startup, each plugin rendering one Docker Compose file into
    ``$HMD_HOME/.cache/local_services/plugins/``.

    **Withdrawn.** ``nsctl`` implements none of it. What was wrong with it
    was never Docker Compose -- a control-plane extension is a compose file
    again, in the same ``src/local`` directory (``NERD004`` SPEC004). It was
    the two things wrapped around it: *discovery*, iterating installed
    packages to find out what should run, where an extension now runs because
    a manifest names it and for no other reason; and ``nsplugin.json``, which
    asked a plugin to declare its resources a second time, in a per-plugin
    inventory parallel to the BACON manifest and the resource declarations
    the RepoClass already carries.

    A workload, meanwhile, is not a container at all any more: it is a
    RepoClass deployed into an environment's k3s through the same DAG the
    cloud uses. Compose survives only where the control plane's own services
    already live.

    ``manifest.Manifest`` still parses the ``plugins`` and ``plugin_config``
    keys, preserves them verbatim when it writes a manifest back, and reports
    them through ``Unsupported()`` -- so a manifest carrying them is not
    silently mangled, and a user is told plainly that they had no effect. It
    obeys neither.

    Superseded by ``NERD004`` (the extension surface), ``NERD005`` (how an
    extension is distributed) and ``NERD006`` (the worked example).

.. note::

    This requirement previously carried the id ``HMD_CLI_NEURONSPHERE_NERD001``,
    which duplicated the requirement in
    ``docs/proposals/NERD001_Floci_Local_Architecture.rst``. It has been given
    its own id.
