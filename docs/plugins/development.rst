.. Plugin Development

Plugin Development
==========================================

.. req:: Local NeuronSphere Plugins
    :id: HMD_CLI_NEURONSPHERE_NERD001

    The local NeuronSphere should be extendable via the standard Python entrypoints specification.
    Plugin authors can register Python modules to know entrypoints in the ``hmd-cli-neuronsphere`` package.
    These plugins will be given a name and should contain functions describing what services they add to the local NeuronSphere.

Python has a powerful built-in mechanism for developing plugins called ``entrypoints``.
It allows a Python package to register a module to a named ``entrypoint`` in another package.
When that package is run, it can iterate through the installed and registered packages to run some known function.
The ``hmd-cli-neuronsphere`` package can use this functionality to provide extension points for other developers to run more Docker containers in the NeuronSphere network.
It should provide the following entrypoints, each registering a function.

* ``hmd_cli_neuronsphere.enabled``: this should be a function that returns a boolean of whether the plugin is enabled or not
* ``hmd_cli_neuronsphere.get_resources``: this should be a function returning a dictionary of services and buckets that will be created
* ``hmd_cli_neuronsphere.prepare_hmd_home``: this should be a function that creates any necessary directories and files in HMD_HOME needed by the plugin's services
* ``hmd_cli_neuronsphere.render_compose_yaml``: this should return a dictionary representing docker-compose.yaml file to run

.. spec:: Enabling plugin via environment variables
    :id: HMD_CLI_NEURONSPHERE_NERD001_SPEC001
    :links: HMD_CLI_NEURONSPHERE_NERD001
    :status: proposed

    Plugins should enabled/disabled via a environment variable.
    The ``hmd-cli-neuronsphere`` package will first loop through all registered plugins and call the ``enabled`` function.
    If it returns ``True``, the other functions will be called during the startup process.

.. spec:: Declaring resources to run
    :id: HMD_CLI_NEURONSPHERE_NERD001_SPEC002
    :links: HMD_CLI_NEURONSPHERE_NERD001
    :status: proposed

    Plugins should declare in the ``get_resources`` function a dictionary of what will be run.
    This will be passed to later functions of all plugins to allow for dynamic configuration of certain services.
    The dictionary can contain any top level keys that must be a list of strings, being the names of the resources.
    The resulting dictionary will be deep-merged into previous dictionaries.

By default, ``hmd-cli-neuronsphere`` core plugins and services will only look for ``services`` and ``buckets`` in the dictionary.
The ``services`` will be names of NeuronSphere microservice containers to be run, and ``buckets`` will be object buckets created in Minio.

.. spec:: Prepare files in HMD_HOME
    :id: HMD_CLI_NEURONSPHERE_NERD001_SPEC003
    :links: HMD_CLI_NEURONSPHERE_NERD001
    :status: proposed

    Plugins are responsible for updating any necessary directories and files in ``HMD_HOME`` that it needs to run

Before any Docker Compose files are rendered, the plugins will be called to create any directories or files in ``HMD_HOME`` that might be mounted into running containers.

.. spec:: Render Docker Compose YAML file
    :id: HMD_CLI_NEURONSPHERE_NERD001_SPEC004
    :links: HMD_CLI_NEURONSPHERE_NERD001
    :status: proposed

    Plugins are responsible for rendering a single Docker Compose YAML file in ``$HMD_HOME/.cache/local_services/plugins/`` directory.

Once each plugin has rendered the YAML file, the ``docker compose up`` command will be called with each file present in the directory.
